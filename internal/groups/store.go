package groups

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"lenv/internal/state"
)

func (m *Manager) dir() string {
	return filepath.Join(m.root, "groups")
}

func (m *Manager) lock(name string) (*os.File, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	for _, dir := range []string{m.root, m.dir()} {
		if err := state.CheckPrivate(dir); errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("group %q does not exist", name)
		} else if err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(filepath.Join(m.dir(), ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("another group operation is in progress; try again shortly")
		}
		return nil, err
	}
	return f, nil
}

func (m *Manager) load(name string) (Group, error) {
	if err := checkName(name); err != nil {
		return Group{}, err
	}
	group, err := m.read(name)
	if errors.Is(err, os.ErrNotExist) {
		return Group{}, fmt.Errorf("group %q does not exist", name)
	}
	return group, err
}

func (m *Manager) read(name string) (Group, error) {
	for _, dir := range []string{m.root, m.dir()} {
		if err := state.CheckPrivate(dir); err != nil {
			return Group{}, err
		}
	}
	data, err := os.ReadFile(filepath.Join(m.dir(), name+".json"))
	if err != nil {
		return Group{}, err
	}
	var group Group
	if err := json.Unmarshal(data, &group); err != nil {
		return Group{}, fmt.Errorf("read group %q: %w", name, err)
	}
	if group.Name != name || group.Members == nil {
		return Group{}, fmt.Errorf("invalid definition for group %q", name)
	}
	seen := make(map[Member]bool, len(group.Members))
	for _, member := range group.Members {
		if err := checkMember(member); err != nil {
			return Group{}, fmt.Errorf("read group %q: %w", name, err)
		}
		if seen[member] {
			return Group{}, fmt.Errorf("group %q contains duplicate instance %q", name, member.Instance)
		}
		seen[member] = true
	}
	return group, nil
}

func (m *Manager) owner(member Member) (string, error) {
	files, err := os.ReadDir(m.dir())
	if err != nil {
		return "", err
	}
	for _, file := range files {
		name, ok := strings.CutSuffix(file.Name(), ".json")
		if !ok || !validName.MatchString(name) {
			continue
		}
		group, err := m.read(name)
		if err != nil {
			return "", err
		}
		if slices.Contains(group.Members, member) {
			return name, nil
		}
	}
	return "", nil
}

func (m *Manager) save(group Group, create bool) error {
	if err := checkName(group.Name); err != nil {
		return err
	}
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return err
	}
	if err := state.CheckPrivate(m.root); err != nil {
		return err
	}
	if err := state.EnsurePrivate(m.dir()); err != nil {
		return err
	}
	f, err := os.CreateTemp(m.dir(), ".group-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(group)
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	path := filepath.Join(m.dir(), group.Name+".json")
	if !create {
		return os.Rename(f.Name(), path)
	}
	if err := os.Link(f.Name(), path); errors.Is(err, os.ErrExist) {
		return fmt.Errorf("group %q already exists", group.Name)
	} else {
		return err
	}
}

func checkName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid group name %q: use up to 32 lowercase letters, numbers, and hyphens, starting with a letter", name)
	}
	return nil
}
