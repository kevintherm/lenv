package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"lenv/internal/groups"
)

func registerGroups(mux *http.ServeMux, projects *groups.Manager) {
	reply := func(w http.ResponseWriter, value any, err error) {
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, value)
	}
	mux.HandleFunc("PUT /v1/groups/{name}", func(w http.ResponseWriter, r *http.Request) {
		group, err := projects.Create(r.PathValue("name"))
		reply(w, group, err)
	})
	mux.HandleFunc("PUT /v1/groups/{name}/members/{service}/{instance}", func(w http.ResponseWriter, r *http.Request) {
		group, err := projects.Add(r.Context(), r.PathValue("name"), groupMember(r))
		reply(w, group, err)
	})
	mux.HandleFunc("DELETE /v1/groups/{name}/members/{service}/{instance}", func(w http.ResponseWriter, r *http.Request) {
		group, err := projects.Remove(r.PathValue("name"), groupMember(r))
		reply(w, group, err)
	})
	for pattern, action := range map[string]func(context.Context, string) (groups.Status, error){
		"GET /v1/groups/{name}":        projects.Status,
		"POST /v1/groups/{name}/start": projects.Start,
		"POST /v1/groups/{name}/stop":  projects.Stop,
	} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			report, err := action(r.Context(), r.PathValue("name"))
			reply(w, report, err)
		})
	}
}

func groupMember(r *http.Request) groups.Member {
	return groups.Member{Service: r.PathValue("service"), Instance: r.PathValue("instance")}
}

func (c Client) GroupEdit(ctx context.Context, action, name string, member groups.Member) (groups.Group, error) {
	path := "/v1/groups/" + url.PathEscape(name)
	method := http.MethodPut
	switch action {
	case "create":
	case "add", "remove":
		path += "/members/" + url.PathEscape(member.Service) + "/" + url.PathEscape(member.Instance)
		if action == "remove" {
			method = http.MethodDelete
		}
	default:
		return groups.Group{}, fmt.Errorf("unknown group edit %q", action)
	}
	var group groups.Group
	err := c.request(ctx, method, path, nil, &group, 30*time.Second)
	return group, err
}

func (c Client) GroupLifecycle(ctx context.Context, action, name string) (groups.Status, error) {
	path := "/v1/groups/" + url.PathEscape(name)
	method := http.MethodGet
	switch action {
	case "status":
	case "start", "stop":
		method = http.MethodPost
		path += "/" + action
	default:
		return groups.Status{}, fmt.Errorf("unknown group action %q", action)
	}
	var status groups.Status
	err := c.request(ctx, method, path, nil, &status, 5*time.Minute)
	return status, err
}
