package postgres

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type jobState struct {
	loaded bool
	pid    int
}

func domain() string {
	return "gui/" + strconv.Itoa(os.Geteuid())
}

func (i instance) target() string {
	return domain() + "/" + i.label()
}

func (i instance) plistPath() string {
	return filepath.Join(i.dir, "postgres.plist")
}

func (i instance) job(ctx context.Context) (jobState, error) {
	out, err := launchctl(ctx, "print", i.target())
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 113 && strings.Contains(string(out), "Could not find service") {
			return jobState{}, nil
		}
		return jobState{}, err
	}
	return jobState{loaded: true, pid: launchdPID(string(out))}, nil
}

func launchctl(ctx context.Context, action string, args ...string) ([]byte, error) {
	timeout := 5 * time.Second
	if action == "bootout" {
		timeout = 45 * time.Second
	}
	return command(ctx, timeout, "/bin/launchctl", append([]string{action}, args...)...)
}

func launchdPID(out string) int {
	for _, line := range strings.Split(out, "\n") {
		if value, ok := strings.CutPrefix(line, "\tpid = "); ok {
			if pid, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && pid > 0 {
				return pid
			}
		}
	}
	return 0
}

func (i instance) writeJob() error {
	status := i.report("", 0)
	args := []string{
		filepath.Join(i.BinDir, "postgres"), "-D", status.DataDir,
		"-h", host, "-p", strconv.Itoa(i.Port), "-c", "unix_socket_directories=",
	}
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0"><dict>` + "\n")
	writeString := func(value string) error {
		b.WriteString("<string>")
		if err := xml.EscapeText(&b, []byte(value)); err != nil {
			return err
		}
		b.WriteString("</string>\n")
		return nil
	}
	for _, entry := range [][2]string{
		{"Label", i.label()},
		{"WorkingDirectory", i.dir},
		{"StandardOutPath", status.LogFile},
		{"StandardErrorPath", status.LogFile},
	} {
		fmt.Fprintf(&b, "<key>%s</key>", entry[0])
		if err := writeString(entry[1]); err != nil {
			return err
		}
	}
	b.WriteString("<key>ProgramArguments</key><array>\n")
	for _, arg := range args {
		if err := writeString(arg); err != nil {
			return err
		}
	}
	b.WriteString(`</array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>ThrottleInterval</key><integer>3</integer>
<key>ExitTimeOut</key><integer>30</integer>
<key>Umask</key><integer>63</integer>
<key>EnvironmentVariables</key><dict>
<key>LC_ALL</key><string>C</string>
<key>LANG</key><string>C</string>
</dict>
</dict></plist>
`)
	return os.WriteFile(i.plistPath(), []byte(b.String()), 0o600)
}
