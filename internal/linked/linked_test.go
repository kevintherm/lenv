package linked

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lenv/internal/homebrew"
)

const formula = "postgresql@16"

func TestHomebrewLinkLifecycle(t *testing.T) {
	m, row, calls := fixture(t)
	ctx := context.Background()
	run := func(action, want string) Status {
		t.Helper()
		got, err := m.Postgres(ctx, action, formula)
		if err != nil || got.Formula != formula || got.State != want || got.Linked != (action != "unlink") {
			t.Fatalf("%s: got %+v, %v", action, got, err)
		}
		return got
	}
	if _, err := m.Postgres(ctx, "stop", formula); err == nil || !strings.Contains(err.Error(), "not linked") {
		t.Fatalf("unlinked stop: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("unlinked stop queried Homebrew: %v", *calls)
	}
	run("link", "started")
	if len(*calls) != 1 || (*calls)[0] != "services list --json" {
		t.Fatalf("link issued more than a read-only lookup: %v", *calls)
	}
	if _, err := m.Postgres(ctx, "link", formula); err == nil || !strings.Contains(err.Error(), "already linked") {
		t.Fatalf("duplicate link: %v", err)
	}
	m = &Manager{root: m.root, brew: m.brew}
	run("status", "started")
	run("stop", "stopped")
	run("stop", "stopped")
	run("start", "started")
	run("start", "started")
	before := len(*calls)
	run("unlink", "")
	if len(*calls) != before || row.Status != "started" {
		t.Fatalf("unlink touched Homebrew: %v (%s)", (*calls)[before:], row.Status)
	}
	if got, err := os.ReadFile(row.File); err != nil || string(got) != "original service file" {
		t.Fatalf("registration changed: %q, %v", got, err)
	}
	var changes []string
	for _, call := range *calls {
		if call != "services list --json" {
			changes = append(changes, call)
		}
	}
	want := []string{
		"services stop " + formula + " --keep --max-wait=10",
		"services run " + formula + " --file=" + row.File,
	}
	if !slices.Equal(changes, want) {
		t.Fatalf("Homebrew changes = %v, want %v", changes, want)
	}
}

func TestLinkRejectsUnsupportedTargets(t *testing.T) {
	for _, name := range []string{"system owner", "other user", "source plist", "missing plist", "symlink", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			m, row, calls := fixture(t)
			switch name {
			case "system owner":
				row.User = "root"
			case "other user":
				row.User = "another"
			case "source plist":
				row.File = filepath.Join(m.brew.home, "opt", formula, "sh.brew."+formula+".plist")
				writePlist(t, row.File)
			case "missing plist", "symlink":
				if err := os.Remove(row.File); err != nil {
					t.Fatal(err)
				}
				if name == "symlink" {
					target := filepath.Join(t.TempDir(), "service.plist")
					writePlist(t, target)
					if err := os.Symlink(target, row.File); err != nil {
						t.Fatal(err)
					}
				}
			case "duplicate":
				m.brew.run = func(context.Context, string, ...string) ([]byte, error) {
					return json.Marshal([]homebrew.Service{*row, *row})
				}
			}
			if _, err := m.Postgres(context.Background(), "link", formula); err == nil {
				t.Fatal("unexpected link")
			}
			if _, err := os.Stat(m.root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed link created state: %v", err)
			}
			if _, err := m.Postgres(context.Background(), "link", "--all"); err == nil || !strings.Contains(err.Error(), "invalid") {
				t.Fatalf("expected invalid formula error: %v", err)
			}
			if len(*calls) > 1 {
				t.Fatalf("invalid formula reached Homebrew: %v", *calls)
			}
		})
	}
}

func TestChangedRegistrationRequiresRelinking(t *testing.T) {
	m, row, calls := fixture(t)
	ctx := context.Background()
	if _, err := m.Postgres(ctx, "link", formula); err != nil {
		t.Fatal(err)
	}
	row.File = filepath.Join(filepath.Dir(row.File), "homebrew.mxcl."+formula+".plist")
	writePlist(t, row.File)
	if _, err := m.Postgres(ctx, "stop", formula); err == nil || !strings.Contains(err.Error(), "registration") {
		t.Fatalf("expected changed-registration error: %v", err)
	}
	if !slices.Equal(*calls, []string{"services list --json", "services list --json"}) {
		t.Fatalf("changed target was not left alone: %v", *calls)
	}
	m.brew = nil
	if _, err := m.Postgres(ctx, "unlink", formula); err != nil {
		t.Fatalf("unlink must work even when Homebrew is unavailable: %v", err)
	}
}

func TestStopChecksHomebrewState(t *testing.T) {
	m, _, _ := fixture(t)
	ctx := context.Background()
	if _, err := m.Postgres(ctx, "link", formula); err != nil {
		t.Fatal(err)
	}
	run := m.brew.run
	m.brew.run = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		if args[1] == "stop" {
			return nil, nil
		}
		return run(ctx, path, args...)
	}
	if _, err := m.Postgres(ctx, "stop", formula); err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("unconfirmed stop: %v", err)
	}
}

func fixture(t *testing.T) (*Manager, *homebrew.Service, *[]string) {
	t.Helper()
	home := t.TempDir()
	row := homebrew.Service{
		Name: formula, User: "developer", Status: "started",
		File: filepath.Join(home, "Library", "LaunchAgents", "sh.brew."+formula+".plist"),
	}
	writePlist(t, row.File)
	var calls []string
	brew := &brewServices{
		path: "/test/bin/brew", username: row.User, home: home,
		run: func(_ context.Context, path string, args ...string) ([]byte, error) {
			if path != "/test/bin/brew" || len(args) < 2 || args[0] != "services" {
				t.Fatalf("unexpected Homebrew invocation: %q %q", path, args)
			}
			calls = append(calls, strings.Join(args, " "))
			switch args[1] {
			case "list":
				return json.Marshal([]homebrew.Service{row})
			case "stop":
				row.Status = "none"
			case "run":
				row.Status = "started"
			default:
				t.Fatalf("unexpected Homebrew action: %v", args)
			}
			return nil, nil
		},
	}
	return &Manager{root: filepath.Join(t.TempDir(), "state"), brew: brew}, &row, &calls
}

func writePlist(t *testing.T, file string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("original service file"), 0o600); err != nil {
		t.Fatal(err)
	}
}
