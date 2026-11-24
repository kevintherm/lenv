package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"lenv/internal/daemon"
	"lenv/internal/groups"
)

const groupUsage = `Usage: lenv group <action>

  create NAME                      Create a project group
  add NAME postgres INSTANCE       Add a lenv-managed instance
  remove NAME postgres INSTANCE    Detach an instance without stopping it
  start NAME                       Start group members in the order added
  stop NAME                        Stop group members in reverse order
  status NAME                      Show each member's state
`

func runGroup(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(out, groupUsage)
		return nil
	}
	action := args[0]
	arity, positional := 1, "NAME"
	switch action {
	case "add", "remove":
		arity, positional = 3, "NAME postgres INSTANCE"
	case "create", "start", "stop", "status":
	default:
		return fmt.Errorf("unknown group action %q; see `lenv group --help`", action)
	}
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "write JSON output")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: lenv group %s [options] %s\n", action, positional)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != arity {
		return fmt.Errorf("expected %s; see `lenv group %s --help`", positional, action)
	}
	path, err := daemon.SocketPath()
	if err != nil {
		return err
	}
	client := daemon.NewClient(path)
	defer client.Close()

	name := flags.Arg(0)
	switch action {
	case "create", "add", "remove":
		member := groups.Member{Service: flags.Arg(1), Instance: flags.Arg(2)}
		group, err := client.GroupEdit(ctx, action, name, member)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(out).Encode(group)
		}
		switch action {
		case "create":
			fmt.Fprintf(out, "Created group %s.\n", group.Name)
		case "add":
			fmt.Fprintf(out, "Added %s %s to %s.\n", member.Service, member.Instance, group.Name)
		case "remove":
			fmt.Fprintf(out, "Detached %s %s from %s without stopping it.\n", member.Service, member.Instance, group.Name)
		}
	default:
		report, err := client.GroupLifecycle(ctx, action, name)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(out).Encode(report)
		}
		if len(report.Members) == 0 {
			fmt.Fprintf(out, "%s: no members\n", report.Name)
		}
		for _, entry := range report.Members {
			if entry.Error != "" {
				fmt.Fprintf(out, "%s %s: %s\n", entry.Service, entry.Instance, entry.Error)
			} else {
				fmt.Fprintf(out, "%s %s: %s on %s:%d\n", entry.Service, entry.Instance, entry.State, entry.Host, entry.Port)
			}
		}
	}
	return nil
}
