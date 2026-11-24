package groups

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"lenv/internal/postgres"
)

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type Postgres interface {
	Status(context.Context, string) (postgres.Status, error)
	CheckStart(context.Context, string) error
	Start(context.Context, string) (postgres.Status, error)
	Stop(context.Context, string) (postgres.Status, error)
}

type Member struct {
	Service  string `json:"service"`
	Instance string `json:"instance"`
}

type Group struct {
	Name    string   `json:"name"`
	Members []Member `json:"members"`
}

type MemberStatus struct {
	Member
	State string `json:"state,omitempty"`
	Host  string `json:"host,omitempty"`
	Port  int    `json:"port,omitempty"`
	Error string `json:"error,omitempty"`
}

type Status struct {
	Name    string         `json:"name"`
	Members []MemberStatus `json:"members"`
}

type Manager struct {
	root string
	db   Postgres
}

func New(root string, db Postgres) *Manager {
	return &Manager{root: root, db: db}
}

func (m *Manager) Create(name string) (Group, error) {
	group := Group{Name: name, Members: []Member{}}
	return group, m.save(group, true)
}

func (m *Manager) Add(ctx context.Context, name string, member Member) (Group, error) {
	lock, err := m.lock(name)
	if err != nil {
		return Group{}, err
	}
	defer lock.Close()
	group, err := m.load(name)
	if err != nil {
		return Group{}, err
	}
	if err := checkMember(member); err != nil {
		return Group{}, err
	}
	owner, err := m.owner(member)
	if err != nil {
		return Group{}, err
	}
	if owner != "" {
		return Group{}, fmt.Errorf("PostgreSQL instance %q already belongs to group %q", member.Instance, owner)
	}
	if _, err := m.db.Status(ctx, member.Instance); err != nil {
		return Group{}, err
	}
	group.Members = append(group.Members, member)
	return group, m.save(group, false)
}

func (m *Manager) Remove(name string, member Member) (Group, error) {
	lock, err := m.lock(name)
	if err != nil {
		return Group{}, err
	}
	defer lock.Close()
	group, err := m.load(name)
	if err != nil {
		return Group{}, err
	}
	at := slices.Index(group.Members, member)
	if at == -1 {
		return Group{}, fmt.Errorf("%s instance %q is not in group %q", member.Service, member.Instance, name)
	}
	group.Members = slices.Delete(group.Members, at, at+1)
	return group, m.save(group, false)
}

func (m *Manager) Status(ctx context.Context, name string) (Status, error) {
	group, err := m.load(name)
	if err != nil {
		return Status{}, err
	}
	report := newStatus(group)
	for _, member := range group.Members {
		result, err := m.db.Status(ctx, member.Instance)
		entry := describe(member, result)
		if err != nil {
			entry.Error = err.Error()
		}
		report.Members = append(report.Members, entry)
	}
	return report, nil
}

func (m *Manager) Start(ctx context.Context, name string) (Status, error) {
	lock, err := m.lock(name)
	if err != nil {
		return Status{}, err
	}
	defer lock.Close()
	group, err := m.load(name)
	if err != nil {
		return Status{}, err
	}
	ports := make(map[int]string, len(group.Members))
	for _, member := range group.Members {
		current, err := m.db.Status(ctx, member.Instance)
		if err != nil {
			return Status{}, fmt.Errorf("cannot start group %q: %s: %w", name, member.Instance, err)
		}
		if other, exists := ports[current.Port]; exists {
			return Status{}, fmt.Errorf("cannot start group %q: %q and %q both use PostgreSQL port %d", name, other, member.Instance, current.Port)
		}
		ports[current.Port] = member.Instance
	}
	for _, member := range group.Members {
		if err := m.db.CheckStart(ctx, member.Instance); err != nil {
			return Status{}, fmt.Errorf("cannot start group %q: %s: %w", name, member.Instance, err)
		}
	}
	report := newStatus(group)
	for _, member := range group.Members {
		result, err := m.db.Start(ctx, member.Instance)
		if err != nil {
			return Status{}, fmt.Errorf("starting %q in group %q: %w; members may still be running, check `lenv group status %s`", member.Instance, name, err, name)
		}
		report.Members = append(report.Members, describe(member, result))
	}
	return report, nil
}

func (m *Manager) Stop(ctx context.Context, name string) (Status, error) {
	lock, err := m.lock(name)
	if err != nil {
		return Status{}, err
	}
	defer lock.Close()
	group, err := m.load(name)
	if err != nil {
		return Status{}, err
	}
	report := Status{Name: name, Members: make([]MemberStatus, len(group.Members))}
	var errs []error
	for i := len(group.Members) - 1; i >= 0; i-- {
		member := group.Members[i]
		result, err := m.db.Stop(ctx, member.Instance)
		if err != nil {
			errs = append(errs, fmt.Errorf("stopping %q in group %q: %w", member.Instance, name, err))
		}
		report.Members[i] = describe(member, result)
	}
	return report, errors.Join(errs...)
}

func checkMember(member Member) error {
	if member.Service != "postgres" {
		return errors.New("groups currently support only lenv-managed postgres instances")
	}
	if !validName.MatchString(member.Instance) {
		return fmt.Errorf("invalid PostgreSQL instance name %q", member.Instance)
	}
	return nil
}

func newStatus(group Group) Status {
	return Status{Name: group.Name, Members: make([]MemberStatus, 0, len(group.Members))}
}

func describe(member Member, current postgres.Status) MemberStatus {
	return MemberStatus{Member: member, State: current.State, Host: current.Host, Port: current.Port}
}
