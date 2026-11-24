package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestServiceCommandRouting(t *testing.T) {
	for _, tt := range []struct {
		args    []string
		wantOut string
		wantErr string
	}{
		{args: []string{"service", "--help"}, wantOut: "Usage: lenv service <service> <action>"},
		{args: []string{"service", "postgres", "--help"}, wantOut: "Usage: lenv service postgres <action>"},
		{args: []string{"service", "redis"}, wantOut: "Usage: lenv service redis list"},
		{args: []string{"service", "unknown"}, wantErr: "unknown service"},
		{args: []string{"service", "postgres", "list", "extra"}, wantErr: "takes no positional arguments"},
		{args: []string{"service", "postgres", "install"}, wantErr: "lenv service postgres list"},
		{args: []string{"service", "postgres", "link"}, wantErr: "provide exactly one Homebrew formula"},
		{args: []string{"service", "postgres", "start", "--linked"}, wantErr: "provide exactly one Homebrew formula"},
		{args: []string{"service", "postgres", "start"}, wantErr: "provide exactly one instance name"},
		{args: []string{"postgres", "list"}, wantErr: "unknown command"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var out, stderr bytes.Buffer
			err := run(context.Background(), tt.args, &out, &stderr)
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Fatalf("output = %q, want %q", out.String(), tt.wantOut)
			}
		})
	}
}
