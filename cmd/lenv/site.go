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

const siteUsage = `Usage: lenv site <action>

  start [--version VERSION] NAME PORT
                      Proxy 127.0.0.1:PORT at https://NAME.localhost:8443
  status              Show the HTTPS URL and local CA certificate path
  stop                Stop the proxy without stopping the app
`

func runSite(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(out, siteUsage)
		return nil
	}
	action := args[0]
	positional, arity := "", 0
	switch action {
	case "start":
		positional, arity = " NAME PORT", 2
	case "stop", "status":
	default:
		return fmt.Errorf("unknown site action %q; see `lenv site --help`", action)
	}
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "write JSON output")
	var version string
	if action == "start" {
		flags.StringVar(&version, "version", "", "use an installed Caddy version instead of the daemon's PATH")
	}
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: lenv site %s [options]%s\n", action, positional)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != arity {
		return fmt.Errorf("expected %d positional arguments; see `lenv site %s --help`", arity, action)
	}
	path, err := daemon.SocketPath()
	if err != nil {
		return err
	}
	client := daemon.NewClient(path)
	defer client.Close()
	status, err := client.Site(ctx, action, flags.Arg(0), flags.Arg(1), version)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(out).Encode(status)
	}
	if !status.Running {
		fmt.Fprintln(out, "No site is running.")
		return nil
	}
	fmt.Fprintf(out, "%s proxies 127.0.0.1:%d\nLocal CA certificate: %s\n", status.URL, status.UpstreamPort, status.CAFile)
	return nil
}
