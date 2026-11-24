package packages

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type archiveLink struct {
	name    string
	target  string
	symlink bool
}

func extract(ctx context.Context, archive, dest, prefix string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gzipReader, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	if err := os.Mkdir(dest, 0o700); err != nil {
		return err
	}
	r := tar.NewReader(gzipReader)
	seen := make(map[string]byte)
	var links []archiveLink
	var expanded int64
	for entries := 0; ; entries++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name, err := archivePath(h.Name, prefix)
		if err != nil {
			return err
		}
		if entries >= 50000 || h.Size < 0 || h.Size > (2<<30)-expanded {
			return errors.New("package archive exceeds extraction limits")
		}
		expanded += h.Size
		if previous, exists := seen[name]; exists {
			if previous == tar.TypeDir && h.Typeflag == tar.TypeDir {
				continue
			}
			return fmt.Errorf("duplicate archive entry: %q", h.Name)
		}
		seen[name] = h.Typeflag
		if name == "." {
			if h.Typeflag != tar.TypeDir {
				return errors.New("archive root must be a directory")
			}
			continue
		}
		target := filepath.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			mode := os.FileMode(0o600) | (os.FileMode(h.Mode) & 0o100)
			if err := writeArchiveFile(r, target, mode); err != nil {
				return err
			}
		case tar.TypeLink, tar.TypeSymlink:
			link := archiveLink{name: name, symlink: h.Typeflag == tar.TypeSymlink}
			if link.symlink {
				link.target = path.Join(path.Dir(name), h.Linkname)
				if h.Linkname == "" || path.IsAbs(h.Linkname) || !validArchivePath(link.target) {
					return fmt.Errorf("invalid symlink target: %q", h.Linkname)
				}
			} else {
				link.target, err = archivePath(h.Linkname, prefix)
				if err != nil {
					return err
				}
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			links = append(links, link)
		default:
			return fmt.Errorf("unsupported archive entry %q (type %d)", h.Name, h.Typeflag)
		}
	}
	// Resolve chains before creating links so extraction never writes through a symlink.
	byName := make(map[string]archiveLink, len(links))
	for _, link := range links {
		byName[link.name] = link
	}
	for i := range links {
		link := &links[i]
		target := link.target
		for depth := 0; depth < 32 && link.symlink; depth++ {
			next, ok := byName[target]
			if !ok {
				break
			}
			target = next.target
		}
		info, err := os.Lstat(filepath.Join(dest, target))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("link %q must target a regular file in the archive", link.name)
		}
		link.target = target
	}
	for _, link := range links {
		name := filepath.Join(dest, link.name)
		target := filepath.Join(dest, link.target)
		if link.symlink {
			rel, err := filepath.Rel(filepath.Dir(name), target)
			if err != nil {
				return err
			}
			if err := os.Symlink(rel, name); err != nil {
				return err
			}
		} else if err := os.Link(target, name); err != nil {
			return err
		}
	}
	return nil
}

func archivePath(name, prefix string) (string, error) {
	if prefix == "" && name == "./" {
		return ".", nil
	}
	name = strings.TrimSuffix(strings.TrimPrefix(name, "./"), "/")
	if prefix == "" && (name == "." || validArchivePath(name)) {
		return name, nil
	}
	if name == prefix {
		return ".", nil
	}
	if file, ok := strings.CutPrefix(name, prefix+"/"); ok && validArchivePath(file) {
		return file, nil
	}
	return "", fmt.Errorf("unexpected archive path %q", name)
}

func validArchivePath(name string) bool {
	return name != "." && fs.ValidPath(name) && !strings.ContainsAny(name, "\\\x00")
}

func writeArchiveFile(src io.Reader, target string, mode os.FileMode) error {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, src)
	return errors.Join(copyErr, f.Close())
}
