package packages

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"lenv/internal/state"
)

//go:embed catalog.json
var catalogJSON []byte

type entry struct {
	Service     string `json:"service"`
	Version     string `json:"version"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	ArchiveRoot string `json:"archive_root"`
	BinDir      string `json:"bin_dir,omitempty"`
	Size        int64  `json:"bytes"`
}

var tools = map[string][]string{
	"postgres": {"postgres", "initdb", "pg_isready", "psql"},
	"mysql":    {"mysqld", "mysql"},
	"mariadb":  {"mariadbd", "mariadb"},
	"mongodb":  {"mongod"},
	"redis":    {"redis-server", "redis-cli"},
	"caddy":    {"caddy"},
	"php":      {"php"},
	"node":     {"node", "npm", "npx"},
}

var versionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)+$`)

type Version struct {
	Version   string `json:"version"`
	Bytes     int64  `json:"bytes"`
	Installed bool   `json:"installed"`
	BinDir    string `json:"bin_dir,omitempty"`
}

type Store struct {
	root    string
	catalog []entry
	client  *http.Client
	mu      sync.Mutex
}

func New(root string) (*Store, error) {
	var catalog []entry
	if err := json.Unmarshal(catalogJSON, &catalog); err != nil {
		return nil, fmt.Errorf("read package catalog: %w", err)
	}
	platform := make([]entry, 0, len(catalog))
	seen := make(map[[4]string]bool, len(catalog))
	for _, e := range catalog {
		key := [4]string{e.Service, e.Version, e.OS, e.Arch}
		if err := e.validate(); err != nil {
			return nil, fmt.Errorf("invalid package %s %s (%s/%s): %w", e.Service, e.Version, e.OS, e.Arch, err)
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate package %s %s (%s/%s)", e.Service, e.Version, e.OS, e.Arch)
		}
		seen[key] = true
		if e.OS == runtime.GOOS && e.Arch == runtime.GOARCH {
			platform = append(platform, e)
		}
	}
	client := &http.Client{
		Timeout: 4 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) >= 10 {
				return errors.New("refusing package download redirect")
			}
			return nil
		},
	}
	return &Store{root: root, catalog: platform, client: client}, nil
}

func (e entry) validate() error {
	if _, ok := tools[e.Service]; !ok || !versionPattern.MatchString(e.Version) {
		return errors.New("unknown service or invalid version")
	}
	if !validArchivePath(e.OS) || !validArchivePath(e.Arch) || filepath.Base(e.OS) != e.OS || filepath.Base(e.Arch) != e.Arch {
		return errors.New("invalid platform")
	}
	if e.ArchiveRoot != "" && (!validArchivePath(e.ArchiveRoot) || filepath.Base(e.ArchiveRoot) != e.ArchiveRoot) {
		return errors.New("invalid archive root")
	}
	if e.BinDir != "" && e.BinDir != "." && (!validArchivePath(e.BinDir) || filepath.Base(e.BinDir) != e.BinDir) {
		return errors.New("invalid binary directory")
	}
	u, err := url.Parse(e.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("expected an HTTPS download URL")
	}
	sum, err := hex.DecodeString(e.SHA256)
	if err != nil || len(sum) != sha256.Size || e.Size <= 0 || e.Size > 2<<30 {
		return errors.New("invalid SHA-256 or download size")
	}
	return nil
}

func (s *Store) Versions(service string) ([]Version, error) {
	if _, ok := tools[service]; !ok {
		return nil, fmt.Errorf("unknown service %q", service)
	}
	versions := make([]Version, 0, len(s.catalog))
	for _, e := range s.catalog {
		if e.Service == service {
			versions = append(versions, s.describe(e))
		}
	}
	return versions, nil
}

func (s *Store) eFor(service, version string) (entry, error) {
	if _, ok := tools[service]; !ok {
		return entry{}, fmt.Errorf("unknown service %q", service)
	}
	for _, e := range s.catalog {
		if service == e.Service && version == e.Version {
			return e, nil
		}
	}
	return entry{}, fmt.Errorf("%s version %q is not in the catalog for %s/%s; see `lenv service %s list`", service, version, runtime.GOOS, runtime.GOARCH, service)
}

func (s *Store) describe(e entry) Version {
	v := Version{Version: e.Version, Bytes: e.Size}
	dest := s.destination(e)
	binDir := e.binPath(dest)
	dir := s.storeDir(e.Service)
	for _, dir := range []string{s.root, filepath.Dir(dir), dir, dest, binDir} {
		if state.CheckPrivate(dir) != nil {
			return v
		}
	}
	if checkTools(e.Service, binDir) == nil {
		v.BinDir, v.Installed = binDir, true
	}
	return v
}

func (s *Store) BinDir(service, version string) (string, error) {
	e, err := s.eFor(service, version)
	if err != nil {
		return "", err
	}
	v := s.describe(e)
	if !v.Installed {
		return "", fmt.Errorf("%s %s is not installed; run `lenv service %s install %s`", service, e.Version, service, e.Version)
	}
	return v.BinDir, nil
}

func (s *Store) Install(ctx context.Context, service, version string) (Version, error) {
	e, err := s.eFor(service, version)
	if err != nil {
		return Version{}, err
	}
	if os.Geteuid() == 0 {
		return Version{}, errors.New("run the lenv daemon as a regular user, not root")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return Version{}, err
	}
	if err := state.CheckPrivate(s.root); err != nil {
		return Version{}, err
	}
	dir := s.storeDir(service)
	for _, path := range []string{filepath.Dir(dir), dir} {
		if err := state.EnsurePrivate(path); err != nil {
			return Version{}, err
		}
	}
	dest := s.destination(e)
	if _, err := os.Lstat(dest); err == nil {
		if current := s.describe(e); current.Installed {
			return current, nil
		}
		return Version{}, fmt.Errorf("refusing to overwrite incomplete package at %q", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Version{}, err
	}
	stage, err := os.MkdirTemp(dir, ".install-")
	if err != nil {
		return Version{}, err
	}
	defer os.RemoveAll(stage)
	archive := filepath.Join(stage, "package.tar.gz")
	if err := s.download(ctx, e, archive); err != nil {
		return Version{}, err
	}
	unpack := filepath.Join(stage, "files")
	if err := extract(ctx, archive, unpack, e.ArchiveRoot); err != nil {
		return Version{}, fmt.Errorf("extract %s %s: %w", service, version, err)
	}
	if err := checkTools(service, e.binPath(unpack)); err != nil {
		return Version{}, err
	}
	if err := ctx.Err(); err != nil {
		return Version{}, err
	}
	if err := os.Rename(unpack, dest); err != nil {
		if current := s.describe(e); current.Installed {
			return current, nil
		}
		return Version{}, err
	}
	return s.describe(e), nil
}

func (s *Store) download(ctx context.Context, e entry, archive string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.URL, nil)
	if err != nil {
		return err
	}
	if req.URL.Scheme != "https" || e.Size <= 0 {
		return errors.New("invalid package download descriptor")
	}
	res, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("download package: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download package: HTTP %d", res.StatusCode)
	}
	if res.ContentLength >= 0 && res.ContentLength != e.Size {
		return errors.New("package download size does not match the catalog")
	}
	f, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(res.Body, e.Size+1))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n != e.Size || hex.EncodeToString(hash.Sum(nil)) != e.SHA256 {
		return errors.New("package download failed size or SHA-256 verification")
	}
	return nil
}

func (s *Store) storeDir(service string) string {
	if service == "postgres" {
		service = "postgresql" // Preserve existing installs.
	}
	return filepath.Join(s.root, "packages", service)
}

func (s *Store) destination(e entry) string {
	return filepath.Join(s.storeDir(e.Service), e.Version+"-"+e.OS+"-"+e.Arch)
}

func (e entry) binPath(root string) string {
	if e.BinDir == "" {
		return filepath.Join(root, "bin")
	}
	return filepath.Join(root, e.BinDir)
}

func checkTools(service, binDir string) error {
	for _, name := range tools[service] {
		info, err := os.Stat(filepath.Join(binDir, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("%s executable %q unavailable in %q", service, name, binDir)
		}
	}
	return nil
}
