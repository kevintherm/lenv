package packages

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractPreservesInternalLinks(t *testing.T) {
	root := t.TempDir()
	archive := archiveFixture(t, []tar.Header{
		{Name: "package/lib", Typeflag: tar.TypeDir},
		{Name: "package/lib/", Typeflag: tar.TypeDir},
		{Name: "package/lib/export.dylib", Typeflag: tar.TypeSymlink, Linkname: "real.dylib"},
		{Name: "package/lib/another.dylib", Typeflag: tar.TypeSymlink, Linkname: "export.dylib"},
		{Name: "package/lib/copy.dylib", Typeflag: tar.TypeLink, Linkname: "package/lib/real.dylib"},
		{Name: "package/lib/real.dylib", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
		{Name: "package/bin/postgres", Typeflag: tar.TypeReg, Mode: 0o4755, Size: 1},
	})
	dest := filepath.Join(root, "extracted")
	if err := extract(context.Background(), archive, dest, "package"); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(filepath.Join(dest, "lib", "export.dylib"))
	if err != nil || link != "real.dylib" {
		t.Fatalf("symbolic link: %q (%v)", link, err)
	}
	if target, err := os.Readlink(filepath.Join(dest, "lib", "another.dylib")); err != nil || target != "real.dylib" {
		t.Fatalf("chained link: %q (%v)", target, err)
	}
	source, err := os.Stat(filepath.Join(dest, "lib", "real.dylib"))
	if err != nil {
		t.Fatal(err)
	}
	copy, err := os.Stat(filepath.Join(dest, "lib", "copy.dylib"))
	if err != nil || !os.SameFile(source, copy) {
		t.Fatalf("hard link is missing or doesn't point to the original: %v", err)
	}
	bin, err := os.Stat(filepath.Join(dest, "bin", "postgres"))
	if err != nil || bin.Mode() != 0o700 {
		t.Fatalf("unexpected executable permissions: %v (%v)", bin, err)
	}
}

func TestExtractRejectsUnsafeEntries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers []tar.Header
	}{
		{"parent traversal", []tar.Header{{Name: "package/../escape", Typeflag: tar.TypeReg}}},
		{"absolute path", []tar.Header{{Name: "/escape", Typeflag: tar.TypeReg}}},
		{"foreign root", []tar.Header{{Name: "other/bin/postgres", Typeflag: tar.TypeReg}}},
		{"symlink outside", []tar.Header{{Name: "package/link", Typeflag: tar.TypeSymlink, Linkname: "../escape"}}},
		{"absolute symlink", []tar.Header{{Name: "package/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/hosts"}}},
		{"hardlink outside", []tar.Header{{Name: "package/link", Typeflag: tar.TypeLink, Linkname: "package/../escape"}}},
		{"symlink cycle", []tar.Header{
			{Name: "package/first", Typeflag: tar.TypeSymlink, Linkname: "second"},
			{Name: "package/second", Typeflag: tar.TypeSymlink, Linkname: "first"},
		}},
		{"special file", []tar.Header{{Name: "package/device", Typeflag: tar.TypeFifo}}},
		{"duplicate file", []tar.Header{
			{Name: "package/copy", Typeflag: tar.TypeReg},
			{Name: "package/copy", Typeflag: tar.TypeReg},
		}},
		{"link parent", []tar.Header{
			{Name: "package/bin/real", Typeflag: tar.TypeReg},
			{Name: "package/link", Typeflag: tar.TypeSymlink, Linkname: "bin/real"},
			{Name: "package/link/child", Typeflag: tar.TypeReg},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "extracted")
			if err := extract(context.Background(), archiveFixture(t, tc.headers), dest, "package"); err == nil {
				t.Fatal("extraction accepted an unsafe archive")
			}
			if _, err := os.Lstat(filepath.Join(filepath.Dir(dest), "escape")); !os.IsNotExist(err) {
				t.Fatalf("archive wrote outside destination: %v", err)
			}
		})
	}
}

func TestDownloadVerifiesChecksum(t *testing.T) {
	contents := "test archive"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, contents)
	}))
	defer server.Close()
	store := &Store{client: server.Client()}
	e := entry{URL: server.URL, Size: int64(len(contents)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(contents)))}
	if err := store.download(context.Background(), e, filepath.Join(t.TempDir(), "valid")); err != nil {
		t.Fatal(err)
	}
	e.SHA256 = strings.Repeat("0", 64)
	if err := store.download(context.Background(), e, filepath.Join(t.TempDir(), "invalid")); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("expected checksum error, got %v", err)
	}
}

func TestFailedInstallNeverPublishesPackage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("installer runs as an unprivileged user")
	}
	data := "valid checksum, invalid archive"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, data)
	}))
	defer server.Close()
	e := entry{
		Service: "postgres", Version: "16.16", OS: "test", Arch: "host", URL: server.URL,
		Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(data))), ArchiveRoot: "package",
	}
	store := &Store{root: filepath.Join(t.TempDir(), "state"), catalog: []entry{e}, client: server.Client()}
	if _, err := store.Install(context.Background(), e.Service, e.Version); err == nil {
		t.Fatal("expected extraction to fail")
	}
	versions, err := store.Versions(e.Service)
	if err != nil || len(versions) != 1 || versions[0].Installed {
		t.Fatal("failed installation was reported as installed")
	}
	if entries, err := os.ReadDir(store.storeDir(e.Service)); err != nil || len(entries) != 0 {
		t.Fatalf("expected an empty store after failure, got %v (%v)", entries, err)
	}
}

func TestInstallFlatArchiveReusesPackage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("installer runs as an unprivileged user")
	}
	data, err := os.ReadFile(archiveFixture(t, []tar.Header{
		{Name: "./", Typeflag: tar.TypeDir},
		{Name: "./caddy", Typeflag: tar.TypeReg, Mode: 0o755, Size: 1},
	}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(data)
	}))
	defer server.Close()
	e := entry{Service: "caddy", Version: "2.0.0", OS: "test", Arch: "host", BinDir: ".",
		URL: server.URL, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Size: int64(len(data))}
	store := &Store{root: filepath.Join(t.TempDir(), "state"), catalog: []entry{e}, client: server.Client()}
	other, err := store.Versions("node")
	if err != nil || len(other) != 0 {
		t.Fatalf("unexpected node versions: %v (%v)", other, err)
	}
	item, err := store.Install(context.Background(), e.Service, e.Version)
	if err != nil || !item.Installed || item.BinDir != store.destination(e) {
		t.Fatalf("install = %+v (%v)", item, err)
	}
	server.Close()
	reused, err := store.Install(context.Background(), e.Service, e.Version)
	if err != nil || reused != item {
		t.Fatalf("repeat install = %+v (%v)", reused, err)
	}
}

func archiveFixture(t *testing.T, headers []tar.Header) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, header := range headers {
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(bytes.Repeat([]byte{'x'}, int(header.Size))); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "test.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
