package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"lenv/internal/daemon"
	"lenv/internal/postgres"
)

const postgresUsage = `Usage: lenv service postgres <action>

  list                                   List available and installed versions
  install VERSION                        Download PostgreSQL into lenv's package store
  init --version VERSION --port PORT NAME
  init --bin-dir PATH --port PORT NAME    Create a private cluster
  start NAME                             Start the instance and wait for readiness
  stop NAME                              Stop and unregister the launchd job
  status NAME                            Show instance state and connection details
  logs NAME                              Show up to 64 KiB of recent PostgreSQL logs
  link FORMULA                           Opt in to controlling a registered Homebrew service
  unlink FORMULA                         Forget a link without stopping the service
  start|stop|status --linked FORMULA      Use Homebrew instead of a lenv instance
`

func runPostgres(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(out, postgresUsage)
		return nil
	}
	action := args[0]
	switch action {
	case "list", "install", "init", "start", "stop", "status", "logs", "link", "unlink":
	default:
		return fmt.Errorf("unknown PostgreSQL action %q; see `lenv service postgres --help`", action)
	}
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: lenv service postgres %s [options]", action)
		switch action {
		case "list":
			fmt.Fprintln(stderr)
		case "install":
			fmt.Fprintln(stderr, " VERSION")
		case "link", "unlink":
			fmt.Fprintln(stderr, " FORMULA")
		default:
			fmt.Fprintln(stderr, " NAME")
		}
		flags.PrintDefaults()
	}
	var opts postgres.Options
	var asJSON, useLink bool
	if action == "start" || action == "stop" || action == "status" {
		flags.BoolVar(&useLink, "linked", false, "target a linked Homebrew formula instead of a lenv instance")
	}
	if action == "init" {
		flags.StringVar(&opts.BinDir, "bin-dir", "", "absolute path to trusted PostgreSQL binaries")
		flags.StringVar(&opts.Version, "version", "", "installed PostgreSQL version from the lenv catalog")
		flags.IntVar(&opts.Port, "port", 0, "unused local port (required)")
	}
	if action != "logs" {
		flags.BoolVar(&asJSON, "json", false, "write JSON output")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	switch action {
	case "list":
		if flags.NArg() != 0 {
			return errors.New("`lenv service postgres list` takes no positional arguments")
		}
	case "install":
		if flags.NArg() != 1 {
			return errors.New("provide one version to install; see `lenv service postgres list`")
		}
	default:
		if flags.NArg() != 1 {
			target := "instance name"
			if useLink || action == "link" || action == "unlink" {
				target = "Homebrew formula"
			}
			return fmt.Errorf("provide exactly one %s; see `lenv service postgres %s --help`", target, action)
		}
	}
	name := flags.Arg(0)
	path, err := daemon.SocketPath()
	if err != nil {
		return err
	}
	client := daemon.NewClient(path)
	defer client.Close()
	if useLink || action == "link" || action == "unlink" {
		status, err := client.LinkedPostgres(ctx, action, name)
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(out).Encode(status)
		}
		if status.Linked {
			fmt.Fprintf(out, "%s (linked through Homebrew): %s\n", status.Formula, status.State)
		} else {
			fmt.Fprintf(out, "%s unlinked. Homebrew service left unchanged.\n", status.Formula)
		}
		return nil
	}
	if action == "list" || action == "install" {
		return renderPackage(ctx, client, out, stderr, "postgres", action, name, asJSON)
	}
	if action == "logs" {
		text, err := client.PostgresLogs(ctx, name)
		if err != nil {
			return err
		}
		fmt.Fprint(out, text)
		return nil
	}
	status, err := client.Postgres(ctx, name, action, opts)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(out).Encode(status)
	}
	fmt.Fprintf(out, "%s: %s on %s:%d\n", status.Name, status.State, status.Host, status.Port)
	if status.Version != "" {
		fmt.Fprintf(out, "Managed PostgreSQL version: %s\n", status.Version)
	}
	if status.PID != 0 {
		fmt.Fprintf(out, "PID: %d\n", status.PID)
	}
	fmt.Fprintf(out, "Database: postgres (user: lenv)\nPassword file: %s\nData: %s\nLog: %s\n",
		status.PassFile, status.DataDir, status.LogFile)
	return nil
}
