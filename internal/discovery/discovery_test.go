package discovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestScanBinariesDoesNotExecuteCandidates(t *testing.T) {
	dir := t.TempDir()
	brew := filepath.Join(dir, "brew")
	redis := filepath.Join(brew, "Cellar", "redis@7", "7.4.0", "bin", "redis-server")
	custom := filepath.Join(dir, "custom")
	postgres := filepath.Join(custom, "bin", "postgres")
	nonExecutable := filepath.Join(custom, "bin", "pg_ctl")
	marker := filepath.Join(dir, "executed")
	writeFile(t, redis, "#!/bin/sh\ntouch \""+marker+"\"\n", 0o700)
	writeFile(t, postgres, "", 0o700)
	writeFile(t, nonExecutable, "", 0o600)
	link := filepath.Join(dir, "redis-server")
	if err := os.Symlink(redis, link); err != nil {
		t.Fatal(err)
	}

	s := Scanner{goos: "darwin", path: dir, brewPrefixes: []string{brew}}
	got := s.Scan(context.Background(), []string{custom})
	realRedis, err := filepath.EvalSymlinks(redis)
	if err != nil {
		t.Fatal(err)
	}
	realPostgres, err := filepath.EvalSymlinks(postgres)
	if err != nil {
		t.Fatal(err)
	}
	want := []Binary{
		{Service: "postgresql", Path: realPostgres, FoundAt: postgres},
		{Service: "redis", Path: realRedis, FoundAt: link},
	}
	if !reflect.DeepEqual(got.Binaries, want) {
		t.Fatalf("binaries = %#v, want %#v", got.Binaries, want)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate wrote a marker, or marker check failed: %v", err)
	}
}

func TestScanHomebrewRegistrations(t *testing.T) {
	prefix := t.TempDir()
	brew := filepath.Join(prefix, "bin", "brew")
	writeFile(t, brew, "", 0o700)
	s := Scanner{
		goos:         "darwin",
		brewPrefixes: []string{prefix},
		run: func(_ context.Context, path string, args ...string) ([]byte, error) {
			if path != brew || !slices.Equal(args, []string{"services", "list", "--json"}) {
				t.Fatalf("unexpected invocation: %q %q", path, args)
			}
			return []byte(`[
				{"name":"postgresql@16","status":"none","user":null,"file":"/a/source.plist"},
				{"name":"postgresql@17","status":"started","user":"root"},
				{"name":"redis","status":"stopped","user":null},
				{"name":"redis@7","status":"error","registered":false,"loaded_file":"/a/loaded.plist"},
				{"name":"redis@8","status":"none","user":"developer"},
				{"name":"mysql","status":"started","registered":true}
			]`), nil
		},
	}
	want := []Service{
		{Kind: "postgresql", ID: "postgresql@17", Manager: "homebrew", Scope: "system", State: "started"},
		{Kind: "redis", ID: "redis", Manager: "homebrew", Scope: "user", State: "stopped"},
		{Kind: "redis", ID: "redis@7", Manager: "homebrew", Scope: "user", State: "error"},
		{Kind: "redis", ID: "redis@8", Manager: "homebrew", Scope: "user", State: "unknown"},
	}
	if got := s.Scan(context.Background(), nil); !reflect.DeepEqual(got.Services, want) || len(got.Warnings) != 0 {
		t.Fatalf("result = %#v, want services %#v with no warnings", got, want)
	}
}

func TestScanSystemdWithoutUserBus(t *testing.T) {
	s := Scanner{
		goos:      "linux",
		systemctl: "/usr/bin/systemctl",
		run: func(_ context.Context, path string, args ...string) ([]byte, error) {
			if path != "/usr/bin/systemctl" || !slices.Contains(args, "--no-pager") {
				t.Fatalf("unexpected invocation: %q %q", path, args)
			}
			if args[0] == "--user" {
				return nil, errors.New("no user bus")
			}
			if args[0] != "--system" {
				t.Fatalf("unexpected scope: %q", args)
			}
			if slices.Contains(args, "list-unit-files") {
				return []byte("postgresql@.service disabled enabled\npostgresql-16.service disabled disabled\nredis-server.service enabled enabled\nssh.service enabled enabled"), nil
			}
			if slices.Contains(args, "list-units") {
				return []byte("postgresql@17-main.service loaded active running PostgreSQL\nredis-server.service loaded failed failed Redis"), nil
			}
			t.Fatalf("unexpected action: %q", args)
			return nil, nil
		},
	}
	got := s.Scan(context.Background(), nil)
	want := []Service{
		{Kind: "postgresql", ID: "postgresql-16.service", Manager: "systemd", Scope: "system", State: "unknown", Registration: "disabled"},
		{Kind: "postgresql", ID: "postgresql@17-main.service", Manager: "systemd", Scope: "system", State: "active"},
		{Kind: "redis", ID: "redis-server.service", Manager: "systemd", Scope: "system", State: "failed", Registration: "enabled"},
	}
	if !reflect.DeepEqual(got.Services, want) || !slices.Equal(got.Warnings, []string{"systemd user service listing unavailable"}) {
		t.Fatalf("result = %#v, want services %#v and a user warning", got, want)
	}
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}
