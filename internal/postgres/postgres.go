package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"lenv/internal/state"
)

const host = "127.0.0.1"

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type Options struct {
	BinDir  string `json:"bin_dir"`
	Port    int    `json:"port"`
	Version string `json:"version,omitempty"`
}

type Status struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Version  string `json:"version,omitempty"`
	PID      int    `json:"pid,omitempty"`
	DataDir  string `json:"data_dir"`
	PassFile string `json:"pass_file"`
	LogFile  string `json:"log_file"`
}

type Manager struct {
	root string
	mu   sync.Mutex
}

type instance struct {
	Options
	name string
	dir  string
}

func New(root string) *Manager {
	return &Manager{root: root}
}

func (m *Manager) Init(ctx context.Context, name string, opts Options) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := checkName(name); err != nil {
		return Status{}, err
	}
	if opts.Port < 1024 || opts.Port > 65535 {
		return Status{}, errors.New("choose a port between 1024 and 65535")
	}
	if !filepath.IsAbs(opts.BinDir) {
		return Status{}, errors.New("--bin-dir must be an absolute path to trusted PostgreSQL binaries")
	}
	binDir, err := filepath.EvalSymlinks(opts.BinDir)
	if err != nil {
		return Status{}, err
	}
	opts.BinDir = binDir
	if err := checkBinaries(binDir); err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return Status{}, err
	}
	base, err := m.instanceRoot()
	if err != nil {
		return Status{}, err
	}
	if err := state.EnsurePrivate(base); err != nil {
		return Status{}, err
	}
	i := instance{Options: opts, name: name, dir: filepath.Join(base, name)}
	if err := os.Mkdir(i.dir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Status{}, fmt.Errorf("instance %q already exists", name)
		}
		return Status{}, err
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(i.dir)
		}
	}()

	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Status{}, err
	}
	password := base64.RawURLEncoding.EncodeToString(random[:])
	tmpPass := filepath.Join(i.dir, "init-password")
	if err := os.WriteFile(tmpPass, []byte(password+"\n"), 0o600); err != nil {
		return Status{}, err
	}
	status := i.report("stopped", 0)
	_, err = command(ctx, 45*time.Second, filepath.Join(binDir, "initdb"),
		"-D", status.DataDir, "--username=lenv", "--locale=C", "--encoding=UTF8",
		"--auth-host=scram-sha-256", "--auth-local=scram-sha-256", "--pwfile="+tmpPass, "--no-instructions")
	if err != nil {
		return Status{}, err
	}
	if err := os.Remove(tmpPass); err != nil {
		return Status{}, err
	}
	pass := fmt.Sprintf("%s:%d:*:lenv:%s\n", host, opts.Port, password)
	if err := os.WriteFile(status.PassFile, []byte(pass), 0o600); err != nil {
		return Status{}, err
	}
	if err := os.WriteFile(status.LogFile, nil, 0o600); err != nil {
		return Status{}, err
	}
	if err := i.writeJob(); err != nil {
		return Status{}, err
	}
	data, err := json.Marshal(i.Options)
	if err != nil {
		return Status{}, err
	}
	if err := os.WriteFile(filepath.Join(i.dir, "instance.json"), data, 0o600); err != nil {
		return Status{}, err
	}
	complete = true
	return status, nil
}

func (m *Manager) Start(ctx context.Context, name string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, err := m.load(name)
	if err != nil {
		return Status{}, err
	}
	job, err := i.job(ctx)
	if err != nil {
		return Status{}, err
	}
	if !job.loaded {
		if err := i.checkStart(); err != nil {
			return Status{}, err
		}
		if _, err := launchctl(ctx, "bootstrap", domain(), i.plistPath()); err != nil {
			return Status{}, err
		}
	}
	ready, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		status, err := i.status(ready)
		if err == nil && status.State == "ready" {
			return status, nil
		}
		if err != nil && ready.Err() == nil {
			return Status{}, err
		}
		select {
		case <-ready.Done():
			return Status{}, fmt.Errorf("PostgreSQL did not become ready; check `lenv service postgres logs %s`", name)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (m *Manager) CheckStart(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, err := m.load(name)
	if err != nil {
		return err
	}
	job, err := i.job(ctx)
	if err != nil || job.loaded {
		return err
	}
	return i.checkStart()
}

func (i instance) checkStart() error {
	if err := checkBinaries(i.BinDir); err != nil {
		return err
	}
	address := net.JoinHostPort(host, strconv.Itoa(i.Port))
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return fmt.Errorf("cannot start PostgreSQL on %s: %w", address, err)
	}
	return listener.Close()
}

func (m *Manager) Stop(ctx context.Context, name string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, err := m.load(name)
	if err != nil {
		return Status{}, err
	}
	job, err := i.job(ctx)
	if err != nil {
		return Status{}, err
	}
	if job.loaded {
		if job.pid > 0 {
			if err := i.shutdown(ctx); err != nil {
				return Status{}, err
			}
		}
		if _, err := launchctl(ctx, "bootout", i.target()); err != nil {
			return Status{}, err
		}
	}
	status, err := i.status(ctx)
	if err == nil && status.State != "stopped" {
		return Status{}, errors.New("launchd has not stopped the instance")
	}
	return status, err
}

func (i instance) shutdown(ctx context.Context) error {
	// PostgreSQL treats SIGINT as a clean, fast shutdown, even with open sessions.
	_, signalErr := launchctl(ctx, "kill", "SIGINT", i.target())
	deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		job, err := i.job(deadline)
		if err == nil && job.pid == 0 {
			return nil
		}
		if signalErr != nil {
			return signalErr
		}
		if err != nil && deadline.Err() == nil {
			return err
		}
		select {
		case <-deadline.Done():
			return errors.New("PostgreSQL did not finish its fast shutdown")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (m *Manager) Status(ctx context.Context, name string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, err := m.load(name)
	if err != nil {
		return Status{}, err
	}
	return i.status(ctx)
}

func (m *Manager) Logs(name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, err := m.load(name)
	if err != nil {
		return "", err
	}
	f, err := os.Open(i.report("", 0).LogFile)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	const limit = 64 << 10
	offset := max(0, info.Size()-limit)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	return string(data), err
}

func checkName(name string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("managed PostgreSQL currently requires macOS with a logged-in desktop session")
	}
	if os.Geteuid() == 0 {
		return errors.New("run the lenv daemon as a regular user, not root")
	}
	if !validName.MatchString(name) {
		return errors.New("use a name of up to 32 lowercase letters, numbers, and hyphens, starting with a letter")
	}
	return nil
}

func (m *Manager) instanceRoot() (string, error) {
	if err := state.CheckPrivate(m.root); err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(m.root)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "postgres"), nil
}

func (m *Manager) load(name string) (instance, error) {
	if err := checkName(name); err != nil {
		return instance{}, err
	}
	base, err := m.instanceRoot()
	if err == nil {
		err = state.CheckPrivate(base)
	}
	i := instance{name: name, dir: filepath.Join(base, name)}
	if err == nil {
		err = state.CheckPrivate(i.dir)
	}
	if err == nil {
		var data []byte
		data, err = os.ReadFile(filepath.Join(i.dir, "instance.json"))
		if err == nil {
			err = json.Unmarshal(data, &i.Options)
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		err = fmt.Errorf("instance %q is not initialized", name)
	}
	return i, err
}

func checkBinaries(dir string) error {
	for _, name := range []string{"initdb", "postgres", "pg_isready"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("PostgreSQL executable unavailable at %q", path)
		}
	}
	return nil
}

func (i instance) report(state string, pid int) Status {
	return Status{
		Name: i.name, State: state, Host: host, Port: i.Port, Version: i.Version, PID: pid,
		DataDir: filepath.Join(i.dir, "data"), PassFile: filepath.Join(i.dir, "pgpass"),
		LogFile: filepath.Join(i.dir, "postgres.log"),
	}
}

func (i instance) status(ctx context.Context) (Status, error) {
	job, err := i.job(ctx)
	if err != nil {
		return Status{}, err
	}
	if !job.loaded {
		return i.report("stopped", 0), nil
	}
	if job.pid == 0 {
		return i.report("waiting", 0), nil
	}
	status := i.report("starting", job.pid)
	if i.ownsPostmaster(status.DataDir, job.pid) {
		if _, err := command(ctx, 2*time.Second, filepath.Join(i.BinDir, "pg_isready"),
			"-q", "-t", "1", "-h", host, "-p", strconv.Itoa(i.Port), "-U", "lenv", "-d", "postgres"); err == nil &&
			i.ownsPostmaster(status.DataDir, job.pid) {
			status.State = "ready"
		}
	}
	return status, nil
}

func (i instance) ownsPostmaster(dataDir string, pid int) bool {
	data, err := os.ReadFile(filepath.Join(dataDir, "postmaster.pid"))
	if err != nil {
		return false
	}
	lines := strings.Split(string(data), "\n")
	return len(lines) >= 8 && lines[0] == strconv.Itoa(pid) && lines[1] == dataDir &&
		lines[3] == strconv.Itoa(i.Port) && strings.TrimSpace(lines[7]) == "ready"
}

func (i instance) label() string {
	sum := sha256.Sum256([]byte(i.dir))
	return fmt.Sprintf("org.lenv.postgres.%s.%x", i.name, sum[:8])
}

func command(ctx context.Context, timeout time.Duration, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if details := strings.TrimSpace(string(out)); details != "" {
			err = fmt.Errorf("%s: %w: %s", filepath.Base(path), err, details)
		} else {
			err = fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
	}
	return out, err
}
