package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"lenv/internal/daemon"
)

func runServicePackage(ctx context.Context, service string, args []string, out, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintf(out, "Usage: lenv service %s list | install VERSION\n", service)
		return nil
	}
	action := args[0]
	arity, suffix := 0, ""
	switch action {
	case "list":
	case "install":
		arity, suffix = 1, " VERSION"
	default:
		return fmt.Errorf("unknown %s action %q; use list or install", service, action)
	}
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "write JSON output")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: lenv service %s %s [options]%s\n", service, action, suffix)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != arity {
		return fmt.Errorf("see `lenv service %s %s --help` for usage", service, action)
	}
	path, err := daemon.SocketPath()
	if err != nil {
		return err
	}
	client := daemon.NewClient(path)
	defer client.Close()
	return renderPackage(ctx, client, out, stderr, service, action, flags.Arg(0), *asJSON)
}

func renderPackage(ctx context.Context, client daemon.Client, out, stderr io.Writer, service, action, version string, asJSON bool) error {
	if action == "install" {
		if !asJSON {
			fmt.Fprintf(stderr, "Preparing %s %s. A new install downloads and verifies the archive.\n", service, version)
		}
		item, err := client.PackageInstall(ctx, service, version)
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(out).Encode(item)
		}
		fmt.Fprintf(out, "%s %s is installed: %s\n", service, item.Version, item.BinDir)
		return nil
	}
	items, err := client.PackageVersions(ctx, service)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(out).Encode(items)
	}
	if len(items) == 0 {
		fmt.Fprintf(out, "No %s packages for this platform.\n", service)
	}
	for _, item := range items {
		if item.Installed {
			fmt.Fprintf(out, "%s\tinstalled\t%s\n", item.Version, item.BinDir)
		} else {
			fmt.Fprintf(out, "%s\tavailable\t%.1f MB download\n", item.Version, float64(item.Bytes)/1e6)
		}
	}
	return nil
}
