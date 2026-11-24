package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"lenv/internal/packages"
	"lenv/internal/postgres"
)

type logs struct {
	Text string `json:"text"`
}

func registerPostgres(mux *http.ServeMux, db *postgres.Manager, store *packages.Store) {
	handleStatus := func(w http.ResponseWriter, status postgres.Status, err error) {
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
	mux.HandleFunc("POST /v1/postgres/{name}/init", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var opts postgres.Options
		if err := decoder.Decode(&opts); err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
			writeJSON(w, http.StatusBadRequest, apiError{"invalid PostgreSQL init options"})
			return
		}
		if (opts.Version == "") == (opts.BinDir == "") {
			writeJSON(w, http.StatusBadRequest, apiError{"specify exactly one of --version or --bin-dir"})
			return
		}
		if opts.Version != "" {
			binDir, err := store.BinDir("postgres", opts.Version)
			if err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, apiError{err.Error()})
				return
			}
			opts.BinDir = binDir
		}
		status, err := db.Init(r.Context(), r.PathValue("name"), opts)
		handleStatus(w, status, err)
	})
	for pattern, action := range map[string]func(context.Context, string) (postgres.Status, error){
		"GET /v1/postgres/{name}":        db.Status,
		"POST /v1/postgres/{name}/start": db.Start,
		"POST /v1/postgres/{name}/stop":  db.Stop,
	} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			status, err := action(r.Context(), r.PathValue("name"))
			handleStatus(w, status, err)
		})
	}
	mux.HandleFunc("GET /v1/postgres/{name}/logs", func(w http.ResponseWriter, r *http.Request) {
		text, err := db.Logs(r.PathValue("name"))
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, logs{Text: text})
	})
}

func (c Client) Postgres(ctx context.Context, name, action string, opts postgres.Options) (postgres.Status, error) {
	path := "/v1/postgres/" + url.PathEscape(name)
	method := http.MethodGet
	var body io.Reader
	if action != "status" {
		method = http.MethodPost
		path += "/" + action
	}
	if action == "init" {
		data, err := json.Marshal(opts)
		if err != nil {
			return postgres.Status{}, err
		}
		body = bytes.NewReader(data)
	}
	var status postgres.Status
	err := c.request(ctx, method, path, body, &status, 60*time.Second)
	return status, err
}

func (c Client) PostgresLogs(ctx context.Context, name string) (string, error) {
	var result logs
	err := c.request(ctx, http.MethodGet, "/v1/postgres/"+url.PathEscape(name)+"/logs", nil, &result, 12*time.Second)
	return result.Text, err
}
