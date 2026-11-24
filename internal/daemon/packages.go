package daemon

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"lenv/internal/packages"
)

func registerPackages(mux *http.ServeMux, store *packages.Store) {
	service := func(r *http.Request) string {
		id := r.PathValue("service")
		if id == "postgresql" {
			return "postgres"
		}
		return id
	}
	mux.HandleFunc("GET /v1/packages/{service}", func(w http.ResponseWriter, r *http.Request) {
		versions, err := store.Versions(service(r))
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, versions)
	})
	mux.HandleFunc("POST /v1/packages/{service}/{version}/install", func(w http.ResponseWriter, r *http.Request) {
		version, err := store.Install(r.Context(), service(r), r.PathValue("version"))
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, version)
	})
}

func (c Client) PackageVersions(ctx context.Context, service string) ([]packages.Version, error) {
	var result []packages.Version
	err := c.request(ctx, http.MethodGet, "/v1/packages/"+url.PathEscape(service), nil, &result, 12*time.Second)
	return result, err
}

func (c Client) PackageInstall(ctx context.Context, service, version string) (packages.Version, error) {
	var result packages.Version
	path := "/v1/packages/" + url.PathEscape(service) + "/" + url.PathEscape(version) + "/install"
	err := c.request(ctx, http.MethodPost, path, nil, &result, 5*time.Minute)
	return result, err
}
