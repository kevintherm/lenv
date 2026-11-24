package linked

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"lenv/internal/homebrew"
	"lenv/internal/state"
)

var postgresFormula = regexp.MustCompile(`^postgresql(@[0-9]+(\.[0-9]+)*)?$`)

type Status struct {
	Formula string `json:"formula"`
	Manager string `json:"manager"`
	Linked  bool   `json:"linked"`
	State   string `json:"state,omitempty"`
}

type Manager struct {
	root string
	brew *brewServices
	mu   sync.Mutex
}

func (m *Manager) Postgres(ctx context.Context, action, formula string) (Status, error) {
	if !postgresFormula.MatchString(formula) {
		return Status{}, fmt.Errorf("invalid PostgreSQL formula %q; use the Homebrew ID from `lenv discover`", formula)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}

	status := Status{Formula: formula, Manager: "homebrew"}
	dir := filepath.Join(m.root, "links", "homebrew")
	path := filepath.Join(dir, formula)
	dirs := []string{m.root, filepath.Dir(dir), dir}
	if action == "link" {
		service, err := m.brew.lookup(ctx, formula)
		if err != nil {
			return Status{}, err
		}
		if err := os.MkdirAll(m.root, 0o700); err != nil {
			return Status{}, err
		}
		for _, dir := range dirs {
			if err := state.EnsurePrivate(dir); err != nil {
				return Status{}, err
			}
		}
		if err := saveLink(dir, path, service.File); errors.Is(err, os.ErrExist) {
			return Status{}, fmt.Errorf("%q is already linked", formula)
		} else if err != nil {
			return Status{}, err
		}
		status.Linked, status.State = true, brewState(service.Status)
		return status, nil
	}

	missing := fmt.Errorf("%q is not linked; use `lenv service postgres link %s` first", formula, formula)
	for _, dir := range dirs {
		if err := state.CheckPrivate(dir); errors.Is(err, os.ErrNotExist) {
			return Status{}, missing
		} else if err != nil {
			return Status{}, err
		}
	}
	if action == "unlink" {
		if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
			return Status{}, missing
		} else if err != nil {
			return Status{}, err
		}
		return status, nil
	}
	saved, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, missing
	}
	if err != nil {
		return Status{}, err
	}
	service, err := m.brew.lookup(ctx, formula)
	if err != nil {
		return Status{}, err
	}
	if service.File != string(saved) {
		return Status{}, fmt.Errorf("registration for %q changed; unlink and link it again to opt in", formula)
	}
	switch action {
	case "status":
	case "start", "stop":
		if (action == "start" && service.Status != "started") ||
			(action == "stop" && !unloaded(service)) {
			if err := m.brew.change(ctx, action, service); err != nil {
				return Status{}, fmt.Errorf("Homebrew could not %s %q: %w", action, formula, err)
			}
			service, err = m.brew.lookup(ctx, formula)
			if err != nil {
				return Status{}, fmt.Errorf("Homebrew accepted the %s request but status could not be checked: %w", action, err)
			}
			if service.File != string(saved) {
				return Status{}, fmt.Errorf("registration for %q changed during %s; unlink and link it again to opt in", formula, action)
			}
		}
		if action == "stop" && !unloaded(service) {
			return Status{}, fmt.Errorf("Homebrew did not confirm that %q was unloaded", formula)
		}
	default:
		return Status{}, fmt.Errorf("unknown linked service action %q", action)
	}
	status.Linked, status.State = true, brewState(service.Status)
	return status, nil
}

func unloaded(service homebrew.Service) bool {
	return service.Status == "none" && !service.Loaded && service.LoadedFile == ""
}

func brewState(value string) string {
	switch value {
	case "none":
		return "stopped"
	case "":
		return "unknown"
	default:
		return value
	}
}

func saveLink(dir, path, file string) error {
	f, err := os.CreateTemp(dir, ".link-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(file)
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Link(f.Name(), path)
}
