package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"time"

	"lenv/internal/discovery"
	"lenv/internal/groups"
	"lenv/internal/linked"
	"lenv/internal/packages"
	"lenv/internal/postgres"
	"lenv/internal/sites"
)

type Status struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

type ScanFunc func(context.Context, []string) discovery.Result

type apiError struct {
	Error string `json:"error"`
}

func Serve(ctx context.Context, path string, scan ScanFunc, db *postgres.Manager, store *packages.Store, links *linked.Manager, projects *groups.Manager, site *sites.Manager, ready func()) error {
	listener, lock, err := listen(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer listener.Close()
	if site != nil {
		defer site.Stop()
	}

	status := Status{PID: os.Getpid(), StartedAt: time.Now().UTC()}
	server := &http.Server{
		Handler:           newHandler(status, scan, db, store, links, projects, site),
		ReadHeaderTimeout: 3 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    16 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if ready != nil {
		ready()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
			return err
		}
		<-done
		return nil
	}
}

func newHandler(status Status, scan ScanFunc, db *postgres.Manager, store *packages.Store, links *linked.Manager, projects *groups.Manager, site *sites.Manager) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, status)
	})
	mux.HandleFunc("GET /v1/discover", func(w http.ResponseWriter, r *http.Request) {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err == nil {
			err = discovery.ValidateDirs(query["dir"])
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, scan(r.Context(), query["dir"]))
	})
	if store != nil {
		registerPackages(mux, store)
	}
	if db != nil && store != nil {
		registerPostgres(mux, db, store)
	}
	if links != nil {
		registerLinks(mux, links)
	}
	if projects != nil {
		registerGroups(mux, projects)
	}
	if site != nil {
		registerSite(mux, site, store)
	}
	return mux
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type Client struct {
	http *http.Client
}

func NewClient(path string) Client {
	dialer := net.Dialer{Timeout: time.Second}
	return Client{http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", path)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c Client) Close() {
	c.http.CloseIdleConnections()
}

func (c Client) Status(ctx context.Context) (Status, error) {
	var status Status
	err := c.request(ctx, http.MethodGet, "/v1/status", nil, &status, 12*time.Second)
	return status, err
}

func (c Client) Discover(ctx context.Context, dirs []string) (discovery.Result, error) {
	var result discovery.Result
	query := url.Values{"dir": dirs}
	err := c.request(ctx, http.MethodGet, "/v1/discover?"+query.Encode(), nil, &result, 12*time.Second)
	return result, err
}

func (c Client) request(ctx context.Context, method, path string, body io.Reader, value any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://lenv"+path, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return fmt.Errorf("cannot reach the lenv daemon; start it with `lenv daemon`: %w", err)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var body apiError
		if json.NewDecoder(resp.Body).Decode(&body) == nil && body.Error != "" {
			return errors.New(body.Error)
		}
		return fmt.Errorf("daemon returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(value)
}
