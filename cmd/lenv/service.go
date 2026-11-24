package main

import (
	"context"
	"fmt"
	"io"
)

const serviceUsage = `Usage: lenv service <service> <action>

  postgres    List and install packages; manage PostgreSQL instances on macOS
  mysql, mariadb, mongodb, redis, caddy, php, node
              Use list to check package availability on this platform
`

func runService(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, serviceUsage)
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(out, serviceUsage)
		return nil
	case "postgres":
		return runPostgres(ctx, args[1:], out, stderr)
	case "mysql", "mariadb", "mongodb", "redis", "caddy", "php", "node":
		return runServicePackage(ctx, args[0], args[1:], out, stderr)
	default:
		return fmt.Errorf("unknown service %q; see `lenv service --help`", args[0])
	}
}
