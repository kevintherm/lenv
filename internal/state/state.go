package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func Root() (string, error) {
	root := os.Getenv("LENV_STATE_DIR")
	if root == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(config, "lenv")
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("LENV_STATE_DIR must be an absolute path")
	}
	return filepath.Clean(root), nil
}

func CheckPrivate(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("state directory %q must be private and owned by the current user", path)
	}
	return nil
}

func EnsurePrivate(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return CheckPrivate(path)
}
