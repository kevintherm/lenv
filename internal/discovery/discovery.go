package discovery

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const MaxExtraDirs = 16

type Binary struct {
	Service string `json:"service"`
	Path    string `json:"path"`
	FoundAt string `json:"found_at"`
}

type Service struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Manager      string `json:"manager"`
	Scope        string `json:"scope"`
	State        string `json:"state"`
	Registration string `json:"registration,omitempty"`
}

type Result struct {
	Binaries []Binary  `json:"binaries"`
	Services []Service `json:"services"`
	Warnings []string  `json:"warnings,omitempty"`
}

type executable struct {
	kind string
	name string
}

var executables = []executable{
	{kind: "postgresql", name: "postgres"},
	{kind: "postgresql", name: "pg_ctl"},
	{kind: "redis", name: "redis-server"},
}

type Scanner struct {
	goos         string
	path         string
	systemDirs   []string
	brewPrefixes []string
	postgresLib  string
	systemctl    string
	run          func(context.Context, string, ...string) ([]byte, error)
}

func New() Scanner {
	s := Scanner{
		goos:       runtime.GOOS,
		path:       os.Getenv("PATH"),
		systemDirs: []string{"/usr/bin", "/usr/local/bin"},
		run:        runCommand,
	}
	switch s.goos {
	case "darwin":
		s.brewPrefixes = []string{"/opt/homebrew", "/usr/local"}
		s.systemDirs = append(s.systemDirs, "/opt/local/bin")
		s.postgresLib = "/opt/local/lib"
	case "linux":
		s.brewPrefixes = []string{"/home/linuxbrew/.linuxbrew"}
		if home, err := os.UserHomeDir(); err == nil {
			s.brewPrefixes = append(s.brewPrefixes, filepath.Join(home, ".linuxbrew"))
		}
		s.postgresLib = "/usr/lib/postgresql"
		for _, path := range []string{"/usr/bin/systemctl", "/bin/systemctl"} {
			if isExecutable(path) {
				s.systemctl = path
				break
			}
		}
	}
	return s
}

func ValidateDirs(dirs []string) error {
	if len(dirs) > MaxExtraDirs {
		return errors.New("too many extra discovery directories")
	}
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) || strings.ContainsRune(dir, 0) {
			return errors.New("discovery directories must be absolute paths")
		}
	}
	return nil
}

func (s Scanner) Scan(ctx context.Context, extraDirs []string) Result {
	result := Result{Binaries: []Binary{}, Services: []Service{}}
	seen := make(map[string]bool)
	add := func(bin executable, path string) {
		if ctx.Err() != nil || !isExecutable(path) {
			return
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil || seen[real] {
			return
		}
		seen[real] = true
		result.Binaries = append(result.Binaries, Binary{
			Service: bin.kind, Path: real, FoundAt: path,
		})
	}
	scanBinDir := func(dir string) {
		for _, bin := range executables {
			add(bin, filepath.Join(dir, bin.name))
		}
	}
	for _, dir := range append(filepath.SplitList(s.path), s.systemDirs...) {
		if filepath.IsAbs(dir) {
			scanBinDir(dir)
		}
	}
	for _, dir := range extraDirs {
		scanBinDir(dir)
		scanBinDir(filepath.Join(dir, "bin"))
	}
	for _, prefix := range s.brewPrefixes {
		cellar := filepath.Join(prefix, "Cellar")
		for _, formula := range subdirs(cellar) {
			kind := formulaKind(formula)
			if kind == "" {
				continue
			}
			rack := filepath.Join(cellar, formula)
			for _, version := range subdirs(rack) {
				for _, bin := range executables {
					if bin.kind == kind {
						add(bin, filepath.Join(rack, version, "bin", bin.name))
					}
				}
			}
		}
	}
	if s.postgresLib != "" {
		for _, name := range subdirs(s.postgresLib) {
			if (s.goos == "darwin" && !strings.HasPrefix(name, "postgresql")) ||
				(s.goos == "linux" && (name == "" || name[0] < '0' || name[0] > '9')) {
				continue
			}
			for _, bin := range executables {
				if bin.kind == "postgresql" {
					add(bin, filepath.Join(s.postgresLib, name, "bin", bin.name))
				}
			}
		}
	}

	switch s.goos {
	case "darwin":
		s.scanBrew(ctx, &result)
	case "linux":
		if s.systemctl != "" {
			s.scanSystemd(ctx, &result, "user")
			s.scanSystemd(ctx, &result, "system")
		}
	}
	sort.Slice(result.Binaries, func(i, j int) bool {
		a, b := result.Binaries[i], result.Binaries[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Path < b.Path
	})
	sort.Slice(result.Services, func(i, j int) bool {
		a, b := result.Services[i], result.Services[j]
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		return a.ID < b.ID
	})
	return result
}

func subdirs(dir string) []string {
	entries, _ := os.ReadDir(dir)
	dirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}
	return dirs
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func runCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ANALYTICS=1")
	cmd.WaitDelay = 250 * time.Millisecond
	return cmd.Output()
}
