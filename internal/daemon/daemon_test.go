package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lenv/internal/discovery"
)

func TestServeAndDiscover(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	path := testSocket(t)
	called := make(chan []string, 1)
	scan := func(_ context.Context, dirs []string) discovery.Result {
		called <- dirs
		return discovery.Result{
			Binaries: []discovery.Binary{{Service: "redis", Path: "/opt/redis-server"}},
			Services: []discovery.Service{},
		}
	}
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- Serve(ctx, path, scan, nil, nil, nil, nil, nil, func() { close(ready) }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not start")
	}
	socket, err := os.Stat(path)
	if err != nil || socket.Mode().Perm() != 0o600 {
		t.Fatalf("socket is not private: %v (%v)", socket, err)
	}
	other, lock, err := listen(path)
	if err == nil {
		other.Close()
		lock.Close()
		t.Fatal("second daemon acquired the socket")
	}

	client := NewClient(path)
	t.Cleanup(client.Close)
	status, err := client.Status(ctx)
	if err != nil || status.PID != os.Getpid() || status.StartedAt.IsZero() {
		t.Fatalf("status = %#v, err = %v", status, err)
	}
	if _, err := client.Discover(ctx, []string{"relative"}); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("invalid directory error = %v", err)
	}
	select {
	case <-called:
		t.Fatal("invalid directory reached the scanner")
	default:
	}
	dir := "/a path/with+symbols&more"
	result, err := client.Discover(ctx, []string{dir})
	if err != nil || len(result.Binaries) != 1 || result.Binaries[0].Service != "redis" || result.Services == nil {
		t.Fatalf("discover = %#v, err = %v", result, err)
	}
	if dirs := <-called; len(dirs) != 1 || dirs[0] != dir {
		t.Fatalf("scanner received %q, want %q", dirs, dir)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not shut down")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remained after shutdown: %v", err)
	}
	if _, err := client.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "start it with `lenv daemon`") {
		t.Fatalf("daemon unavailable error = %v", err)
	}
}

func TestListenReplacesStaleSocket(t *testing.T) {
	path := testSocket(t)
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	listener, lock, err := listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	defer listener.Close()
}

func TestListenLeavesNonSocketAlone(t *testing.T) {
	path := testSocket(t)
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, lock, err := listen(path)
	if err == nil {
		listener.Close()
		lock.Close()
		t.Fatal("replaced non-socket path")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatalf("non-socket file changed: %q (%v)", data, err)
	}
}

func testSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lv-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}
