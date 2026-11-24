package homebrew

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Service struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	User       string `json:"user"`
	File       string `json:"file"`
	Registered bool   `json:"registered"`
	Loaded     bool   `json:"loaded"`
	LoadedFile string `json:"loaded_file"`
}

func Find(prefixes []string) string {
	for _, prefix := range prefixes {
		path := filepath.Join(prefix, "bin", "brew")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return path
		}
	}
	return ""
}

func List(ctx context.Context, brew string, run func(context.Context, string, ...string) ([]byte, error)) ([]Service, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	data, err := run(ctx, brew, "services", "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("Homebrew service listing unavailable: %w", err)
	}
	var rows []Service
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("Homebrew returned an invalid service listing: %w", err)
	}
	return rows, nil
}

func Command(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ANALYTICS=1")
	cmd.WaitDelay = 250 * time.Millisecond
	return cmd
}
