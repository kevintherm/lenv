package linked

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"lenv/internal/homebrew"
)

type brewServices struct {
	path, username, home string
	run                  func(context.Context, string, ...string) ([]byte, error)
}

func NewHomebrew(root string) *Manager {
	var brew *brewServices
	if runtime.GOOS == "darwin" && os.Geteuid() != 0 {
		if account, err := user.Current(); err == nil {
			brew = &brewServices{
				path:     homebrew.Find([]string{"/opt/homebrew", "/usr/local"}),
				username: account.Username, home: account.HomeDir,
			}
		}
	}
	return &Manager{root: root, brew: brew}
}

func (b *brewServices) lookup(ctx context.Context, formula string) (homebrew.Service, error) {
	if b == nil {
		return homebrew.Service{}, errors.New("linking Homebrew services requires macOS and a regular user")
	}
	if b.path == "" {
		return homebrew.Service{}, errors.New("Homebrew was not found in /opt/homebrew or /usr/local")
	}
	rows, err := homebrew.List(ctx, b.path, b.command)
	if err != nil {
		return homebrew.Service{}, err
	}
	var found homebrew.Service
	for _, row := range rows {
		if row.Name != formula {
			continue
		}
		if found.Name != "" {
			return homebrew.Service{}, fmt.Errorf("Homebrew returned more than one service for %q", formula)
		}
		found = row
	}
	if found.User == b.username && b.registration(formula, found.File) {
		return found, nil
	}
	return homebrew.Service{}, fmt.Errorf("%q is not registered with Homebrew Services for the current user; see `lenv discover`", formula)
}

func (b *brewServices) registration(formula, file string) bool {
	dir := filepath.Join(b.home, "Library", "LaunchAgents")
	if file != filepath.Join(dir, "sh.brew."+formula+".plist") &&
		file != filepath.Join(dir, "homebrew.mxcl."+formula+".plist") {
		return false
	}
	info, err := os.Lstat(file)
	return err == nil && info.Mode().IsRegular()
}

func (b *brewServices) change(ctx context.Context, action string, service homebrew.Service) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{"services"}
	if action == "start" {
		// Unlike brew services start, run can load the existing file without rewriting it.
		args = append(args, "run", service.Name, "--file="+service.File)
	} else {
		args = append(args, "stop", service.Name, "--keep", "--max-wait=10")
	}
	_, err := b.command(ctx, b.path, args...)
	return err
}

func (b *brewServices) command(ctx context.Context, path string, args ...string) ([]byte, error) {
	if b.run != nil {
		return b.run(ctx, path, args...)
	}
	out, err := homebrew.Command(ctx, path, args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	return out, err
}
