package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lenv/internal/daemon"
	"lenv/internal/discovery"
	"lenv/internal/groups"
	"lenv/internal/linked"
	"lenv/internal/packages"
	"lenv/internal/postgres"
	"lenv/internal/sites"
	"lenv/internal/state"
)

const usage = `Usage: lenv <command>

  daemon               Run the daemon in the foreground
  daemon status        Check whether the daemon is running
  status               Show daemon status
  discover             Find PostgreSQL and Redis binaries and registered services
  service              Install and manage services
  group                Manage project groups
  site                 Proxy a local app over HTTPS
`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "lenv:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(out, usage)
		return nil
	}
	name, args := args[0], args[1:]
	if name == "service" {
		return runService(ctx, args, out, stderr)
	}
	if name == "group" {
		return runGroup(ctx, args, out, stderr)
	}
	if name == "site" {
		return runSite(ctx, args, out, stderr)
	}
	if name == "daemon" && len(args) > 0 && args[0] == "status" {
		name, args = "status", args[1:]
	}
	if name != "daemon" && name != "status" && name != "discover" {
		return fmt.Errorf("unknown command %q; see `lenv help`", name)
	}

	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: lenv %s", name)
		if name == "daemon" {
			fmt.Fprintln(stderr, " [status]")
		} else {
			fmt.Fprintln(stderr, " [options]")
		}
		flags.PrintDefaults()
	}
	var asJSON bool
	var dirs []string
	if name != "daemon" {
		flags.BoolVar(&asJSON, "json", false, "write JSON output")
	}
	if name == "discover" {
		flags.Func("dir", "also look in this absolute directory and its bin subdirectory (repeatable)", func(dir string) error {
			dirs = append(dirs, dir)
			return nil
		})
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q; see `lenv %s --help`", flags.Arg(0), name)
	}
	if err := discovery.ValidateDirs(dirs); err != nil {
		return err
	}
	path, err := daemon.SocketPath()
	if err != nil {
		return err
	}
	if name == "daemon" {
		root, err := state.Root()
		if err != nil {
			return err
		}
		store, err := packages.New(root)
		if err != nil {
			return err
		}
		db := postgres.New(root)
		site := sites.New(root, os.Getenv("LENV_HTTPS_PORT"))
		return daemon.Serve(ctx, path, discovery.New().Scan, db, store, linked.NewHomebrew(root), groups.New(root, db), site, func() {
			fmt.Fprintf(stderr, "lenv daemon listening on %s\n", path)
		})
	}
	client := daemon.NewClient(path)
	defer client.Close()
	if name == "status" {
		status, err := client.Status(ctx)
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(out).Encode(status)
		}
		fmt.Fprintf(out, "Daemon running (PID %d, since %s)\n", status.PID, status.StartedAt.Format(time.RFC3339))
		return nil
	}
	result, err := client.Discover(ctx, dirs)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(out).Encode(result)
	}
	fmt.Fprintln(out, "Binaries:")
	if len(result.Binaries) == 0 {
		fmt.Fprintln(out, "  None found")
	}
	for _, binary := range result.Binaries {
		fmt.Fprintf(out, "  %s\t%s\n", binary.Service, binary.Path)
	}
	fmt.Fprintln(out, "Registered services:")
	if len(result.Services) == 0 {
		fmt.Fprintln(out, "  None found")
	}
	for _, service := range result.Services {
		fmt.Fprintf(out, "  %s\t%s (%s)\t%s\n", service.ID, service.Manager, service.Scope, service.State)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintln(stderr, "lenv:", warning)
	}
	return nil
}
