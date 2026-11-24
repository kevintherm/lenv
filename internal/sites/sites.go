package sites

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"

	"lenv/internal/state"
)

const DefaultPort = 8443

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type Status struct {
	Running      bool   `json:"running"`
	Name         string `json:"name,omitempty"`
	URL          string `json:"url,omitempty"`
	UpstreamPort int    `json:"upstream_port,omitempty"`
	CAFile       string `json:"ca_file,omitempty"`
}

type Manager struct {
	root, port string
	mu         sync.Mutex
	proxy      *proxy
}

type proxy struct {
	status Status
	cmd    *exec.Cmd
	done   chan struct{}
	err    error
}

func New(root, port string) *Manager {
	return &Manager{root: root, port: port}
}

func (m *Manager) Start(ctx context.Context, name string, upstream int, bin string) (Status, error) {
	port := DefaultPort
	if m.port != "" {
		var err error
		port, err = strconv.Atoi(m.port)
		if err != nil || port < 1024 || port > 65535 {
			return Status{}, errors.New("LENV_HTTPS_PORT must be an integer from 1024 to 65535")
		}
	}
	if err := validate(name, port, upstream); err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	if running := m.current(); running.Running {
		return Status{}, fmt.Errorf("site %q is already running; use `lenv site stop` first", running.Name)
	}
	if os.Geteuid() == 0 {
		return Status{}, errors.New("run the lenv daemon as a regular user, not root")
	}
	if bin == "" {
		var err error
		bin, err = exec.LookPath("caddy")
		if err != nil {
			return Status{}, errors.New("use `lenv site start --version VERSION` with a managed Caddy install, or put Caddy 2 on the daemon's PATH")
		}
	}
	listen := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	probe, err := net.Listen("tcp4", listen)
	if err != nil {
		return Status{}, fmt.Errorf("HTTPS port %d is unavailable (set LENV_HTTPS_PORT before starting the daemon): %w", port, err)
	}
	probe.Close()
	dir := filepath.Join(m.root, "site")
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return Status{}, err
	}
	for _, path := range []string{m.root, dir, data} {
		if err := state.EnsurePrivate(path); err != nil {
			return Status{}, err
		}
	}
	host := name + ".localhost"
	config, err := caddyConfig(host, port, upstream, data)
	if err != nil {
		return Status{}, err
	}
	logPath := filepath.Join(dir, "caddy.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return Status{}, err
	}
	defer log.Close()
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return Status{}, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return Status{}, fmt.Errorf("cannot lock the site state directory: %w", err)
	}
	p := &proxy{done: make(chan struct{}), status: Status{
		Running: true, Name: name, URL: "https://" + net.JoinHostPort(host, strconv.Itoa(port)),
		UpstreamPort: upstream, CAFile: filepath.Join(data, "pki", "authorities", "lenv", "root.crt"),
	}}
	p.cmd = exec.Command(bin, "run", "--config", "-")
	p.cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+dir, "XDG_DATA_HOME="+dir)
	p.cmd.Stdin, p.cmd.Stdout, p.cmd.Stderr = bytes.NewReader(config), log, log
	if err := p.cmd.Start(); err != nil {
		lock.Close()
		return Status{}, err
	}
	go func() {
		p.err = p.cmd.Wait()
		lock.Close()
		close(p.done)
	}()
	if err := p.ready(ctx, host, listen); err != nil {
		p.stop()
		return Status{}, fmt.Errorf("Caddy did not become ready: %w; see %s", err, logPath)
	}
	m.proxy = p
	return p.status, nil
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current()
}

func (m *Manager) current() Status {
	if m.proxy != nil {
		select {
		case <-m.proxy.done:
			m.proxy = nil
		default:
			return m.proxy.status
		}
	}
	return Status{}
}

func (m *Manager) Stop() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current().Running {
		m.proxy.stop()
		m.proxy = nil
	}
	return Status{}
}

func (p *proxy) stop() {
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

func (p *proxy) ready(ctx context.Context, host, listen string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			if p.err != nil {
				return p.err
			}
			return errors.New("Caddy exited")
		case <-tick.C:
			pem, err := os.ReadFile(p.status.CAFile)
			if err != nil {
				continue
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(pem) {
				continue
			}
			dialer := tls.Dialer{
				NetDialer: &net.Dialer{Timeout: 500 * time.Millisecond},
				Config:    &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12},
			}
			if conn, err := dialer.DialContext(ctx, "tcp4", listen); err == nil {
				conn.Close()
				return nil
			}
		}
	}
}

func validate(name string, port, upstream int) error {
	if !validName.MatchString(name) || name[len(name)-1] == '-' {
		return errors.New("site names must be up to 32 lowercase letters, numbers, or hyphens, start with a letter, and not end in a hyphen")
	}
	if upstream < 1 || upstream > 65535 || upstream == port {
		return fmt.Errorf("upstream port must be between 1 and 65535 and differ from HTTPS port %d", port)
	}
	return nil
}
