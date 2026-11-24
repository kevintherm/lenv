package groups

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lenv/internal/postgres"
)

type fixtureDB struct {
	ports map[string]int
	check map[string]error
	fail  map[string]error
	calls []string
}

func (db *fixtureDB) Status(_ context.Context, name string) (postgres.Status, error) {
	port, ok := db.ports[name]
	if !ok {
		return postgres.Status{}, errors.New("not a managed instance")
	}
	return postgres.Status{Name: name, Port: port, Host: "127.0.0.1", State: "stopped"}, nil
}

func (db *fixtureDB) CheckStart(_ context.Context, name string) error {
	db.calls = append(db.calls, "check "+name)
	return db.check[name]
}

func (db *fixtureDB) Start(ctx context.Context, name string) (postgres.Status, error) {
	db.calls = append(db.calls, "start "+name)
	result, err := db.Status(ctx, name)
	result.State = "ready"
	return result, errors.Join(err, db.fail["start "+name])
}

func (db *fixtureDB) Stop(ctx context.Context, name string) (postgres.Status, error) {
	db.calls = append(db.calls, "stop "+name)
	result, err := db.Status(ctx, name)
	return result, errors.Join(err, db.fail["stop "+name])
}

func TestGroupLifecycleOrderAndPersistence(t *testing.T) {
	m, db := groupFixture(t)
	ctx := context.Background()
	m = New(m.root, db)
	started, err := m.Start(ctx, "alpha")
	if err != nil || len(started.Members) != 2 || started.Members[0].State != "ready" || started.Members[1].State != "ready" {
		t.Fatalf("start: %+v, %v", started, err)
	}
	if _, err := m.Stop(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	want := []string{"check first", "check second", "start first", "start second", "stop second", "stop first"}
	if !slices.Equal(db.calls, want) {
		t.Fatalf("calls = %v, want %v", db.calls, want)
	}
	db.calls = nil
	if _, err := m.Remove("alpha", Member{"postgres", "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Add(ctx, "beta", Member{"postgres", "first"}); err != nil {
		t.Fatal(err)
	}
	if len(db.calls) != 0 {
		t.Fatalf("membership edit changed lifecycle: %v", db.calls)
	}
}

func TestGroupMembershipConstraints(t *testing.T) {
	m, _ := groupFixture(t)
	ctx := context.Background()
	for _, test := range []struct {
		group string
		input Member
		want  string
	}{
		{"alpha", Member{"postgres", "first"}, "already belongs"},
		{"beta", Member{"postgres", "first"}, "already belongs"},
		{"alpha", Member{"redis", "other"}, "only lenv-managed postgres"},
		{"alpha", Member{"postgres", "outside"}, "not a managed instance"},
		{"alpha", Member{"postgres", "../outside"}, "invalid PostgreSQL instance"},
	} {
		if _, err := m.Add(ctx, test.group, test.input); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("add %v: got %v, want %q", test.input, err, test.want)
		}
	}
	for _, name := range []string{"../outside", "alpha"} {
		if _, err := m.Create(name); err == nil {
			t.Fatalf("created invalid or duplicate group %q", name)
		}
	}
}

func TestGroupPreflightStartsNothingOnConflict(t *testing.T) {
	m, db := groupFixture(t)
	ctx := context.Background()
	db.ports["second"] = db.ports["first"]
	if _, err := m.Start(ctx, "alpha"); err == nil || !strings.Contains(err.Error(), "both use PostgreSQL port") {
		t.Fatalf("duplicate port: %v", err)
	}
	if len(db.calls) != 0 {
		t.Fatalf("unexpected calls: %v", db.calls)
	}
	db.ports["second"]++
	db.check = map[string]error{"second": errors.New("port occupied")}
	if _, err := m.Start(ctx, "alpha"); err == nil || !strings.Contains(err.Error(), "port occupied") {
		t.Fatalf("occupied port: %v", err)
	}
	if !slices.Equal(db.calls, []string{"check first", "check second"}) {
		t.Fatalf("unexpected calls: %v", db.calls)
	}
}

func TestGroupStopContinuesAfterError(t *testing.T) {
	m, db := groupFixture(t)
	db.fail = map[string]error{"stop second": errors.New("shutdown failed")}
	if _, err := m.Stop(context.Background(), "alpha"); err == nil || !strings.Contains(err.Error(), "shutdown failed") {
		t.Fatalf("stop: %v", err)
	}
	if !slices.Equal(db.calls, []string{"stop second", "stop first"}) {
		t.Fatalf("unexpected calls: %v", db.calls)
	}
}

func TestGroupMutationsShareLockAcrossManagers(t *testing.T) {
	m, db := groupFixture(t)
	lock, err := m.lock("alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	other := New(m.root, db)
	if _, err := other.Add(context.Background(), "beta", Member{"postgres", "first"}); err == nil ||
		!strings.Contains(err.Error(), "another group operation") {
		t.Fatalf("expected busy, got %v", err)
	}
}

func groupFixture(t *testing.T) (*Manager, *fixtureDB) {
	t.Helper()
	db := &fixtureDB{ports: map[string]int{"first": 55432, "second": 55433}}
	m := New(filepath.Join(t.TempDir(), "state"), db)
	for _, name := range []string{"alpha", "beta"} {
		if _, err := m.Create(name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"first", "second"} {
		if _, err := m.Add(context.Background(), "alpha", Member{"postgres", name}); err != nil {
			t.Fatal(err)
		}
	}
	return m, db
}
