package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"lenv/internal/packages"
)

func TestPackageRoutes(t *testing.T) {
	store, err := packages.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandler(Status{}, nil, nil, store, nil, nil, nil)
	list := func(service string) []packages.Version {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/packages/"+service, nil))
		var versions []packages.Version
		if err := json.Unmarshal(w.Body.Bytes(), &versions); w.Code != http.StatusOK || err != nil || versions == nil {
			t.Fatalf("list %s: %d %s (%v)", service, w.Code, w.Body, err)
		}
		return versions
	}
	if current, legacy := list("postgres"), list("postgresql"); !slices.Equal(current, legacy) {
		t.Fatalf("legacy listing differs: %v vs %v", current, legacy)
	}
	list("mariadb")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/packages/node/0.0.0/install", nil))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown version: %d %s", w.Code, w.Body)
	}
}
