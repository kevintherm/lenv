package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"lenv/internal/linked"
)

func registerLinks(mux *http.ServeMux, links *linked.Manager) {
	for _, action := range []string{"link", "unlink", "status", "start", "stop"} {
		method := http.MethodPost
		if action == "status" {
			method = http.MethodGet
		}
		mux.HandleFunc(method+" /v1/links/postgresql/{formula}/"+action, func(w http.ResponseWriter, r *http.Request) {
			status, err := links.Postgres(r.Context(), action, r.PathValue("formula"))
			if err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, apiError{err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, status)
		})
	}
}

func (c Client) LinkedPostgres(ctx context.Context, action, formula string) (linked.Status, error) {
	method := http.MethodPost
	switch action {
	case "link", "unlink", "start", "stop":
	case "status":
		method = http.MethodGet
	default:
		return linked.Status{}, fmt.Errorf("unknown linked service action %q", action)
	}
	var status linked.Status
	err := c.request(ctx, method, "/v1/links/postgresql/"+url.PathEscape(formula)+"/"+action, nil, &status, 35*time.Second)
	return status, err
}
