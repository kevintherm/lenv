package postgres

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLaunchdPID(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   int
	}{
		{"\tstate = running\n\tpid = 12345\n", 12345},
		{"\tstate = spawn scheduled\n\t\tpid = 12345\n", 0},
		{"\tstate = not running\n\tpid = -1\n", 0},
	} {
		if got := launchdPID(tc.output); got != tc.want {
			t.Errorf("launchdPID(%q) = %d, want %d", tc.output, got, tc.want)
		}
	}
}

func TestLaunchJobArguments(t *testing.T) {
	i := instance{
		Options: Options{BinDir: "/test & dir/PostgreSQL/bin", Port: 55432},
		name:    "example",
		dir:     t.TempDir(),
	}
	if err := i.writeJob(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(i.plistPath())
	if err != nil {
		t.Fatal(err)
	}
	var plist struct {
		Args []string `xml:"dict>array>string"`
	}
	if err := xml.Unmarshal(data, &plist); err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(i.BinDir, "postgres"), "-D", filepath.Join(i.dir, "data"),
		"-h", "127.0.0.1", "-p", "55432", "-c", "unix_socket_directories=",
	}
	if !slices.Equal(plist.Args, want) {
		t.Fatalf("ProgramArguments = %q, want %q", plist.Args, want)
	}
	other := i
	other.dir = t.TempDir()
	if other.label() == i.label() {
		t.Fatal("jobs with the same name in different state directories must have distinct labels")
	}
}

func TestReadinessRequiresMatchingPostmaster(t *testing.T) {
	i := instance{Options: Options{Port: 55432}}
	dir := t.TempDir()
	for _, tc := range []struct {
		pid   int
		dir   string
		port  int
		state string
		want  bool
	}{
		{123, dir, 55432, "ready", true},
		{456, dir, 55432, "ready", false},
		{123, "/some/other/data", 55432, "ready", false},
		{123, dir, 5432, "ready", false},
		{123, dir, 55432, "starting", false},
	} {
		data := fmt.Sprintf("%d\n%s\n0\n%d\n\n127.0.0.1\n\n%s   \n", tc.pid, tc.dir, tc.port, tc.state)
		if err := os.WriteFile(filepath.Join(dir, "postmaster.pid"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := i.ownsPostmaster(dir, 123); got != tc.want {
			t.Errorf("ownsPostmaster(%q) = %t, want %t", data, got, tc.want)
		}
	}
}
