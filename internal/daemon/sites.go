package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	"lenv/internal/packages"
	"lenv/internal/sites"
)

func registerSite(mux *http.ServeMux, site *sites.Manager, store *packages.Store) {
	mux.HandleFunc("GET /v1/site", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, site.Status())
	})
	mux.HandleFunc("DELETE /v1/site", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, site.Stop())
	})
	mux.HandleFunc("PUT /v1/site/{name}/{port}", func(w http.ResponseWriter, r *http.Request) {
		port, err := strconv.Atoi(r.PathValue("port"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{"upstream port must be an integer"})
			return
		}
		var bin string
		if version := r.URL.Query().Get("version"); version != "" {
			if store == nil {
				writeJSON(w, http.StatusUnprocessableEntity, apiError{"managed Caddy is not available"})
				return
			}
			binDir, err := store.BinDir("caddy", version)
			if err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, apiError{err.Error()})
				return
			}
			bin = filepath.Join(binDir, "caddy")
		}
		status, err := site.Start(r.Context(), r.PathValue("name"), port, bin)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, status)
	})
}

func (c Client) Site(ctx context.Context, action, name, port, version string) (sites.Status, error) {
	method, path := http.MethodGet, "/v1/site"
	switch action {
	case "status":
	case "stop":
		method = http.MethodDelete
	case "start":
		method = http.MethodPut
		path += "/" + url.PathEscape(name) + "/" + url.PathEscape(port)
		if version != "" {
			path += "?" + url.Values{"version": {version}}.Encode()
		}
	default:
		return sites.Status{}, fmt.Errorf("unknown site action %q", action)
	}
	var status sites.Status
	err := c.request(ctx, method, path, nil, &status, 18*time.Second)
	return status, err
}
