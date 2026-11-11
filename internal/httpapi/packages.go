package httpapi

import (
	"errors"
	"net/http"

	"easynr/internal/registry"
)

func adminError(w http.ResponseWriter, err error) {
	status, code, message := 500, "internal_error", "Operation failed"
	switch {
	case errors.Is(err, registry.ErrNotFound):
		status, code, message = 404, "not_found", "Resource not found"
	case errors.Is(err, registry.ErrInvalid):
		status, code, message = 400, "invalid_input", "Check package, tag, scope and upstream fields"
	case errors.Is(err, registry.ErrConflict):
		status, code, message = 409, "conflict", "Resource is in use, already exists or URL is immutable"
	case errors.Is(err, registry.ErrUpstream):
		status, code, message = 502, "upstream_unavailable", "Upstream unavailable"
	}
	failure(w, status, code, message)
}
func (a *server) packages(w http.ResponseWriter, r *http.Request) {
	items, err := a.engine.Packages()
	if err != nil {
		adminError(w, err)
		return
	}
	reply(w, 200, map[string]any{"items": items})
}
func (a *server) packageDetails(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	doc, err := a.engine.Inspect(r.Context(), name)
	if err != nil {
		adminError(w, err)
		return
	}
	reply(w, 200, doc)
}
func (a *server) removePackage(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.DeleteLocal(r.URL.Query().Get("name"), r.URL.Query().Get("version")); err != nil {
		adminError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *server) cacheStats(w http.ResponseWriter, r *http.Request) {
	stats, err := a.engine.CacheStatistics()
	if err != nil {
		adminError(w, err)
		return
	}
	reply(w, 200, stats)
}
func (a *server) clearCache(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.ClearCache(r.URL.Query().Get("name")); err != nil {
		adminError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *server) updateTag(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string `json:"name"`
		Tag     string `json:"tag"`
		Version string `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := a.engine.SetTag(in.Name, in.Tag, in.Version, false); err != nil {
		adminError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *server) deleteTag(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.SetTag(r.URL.Query().Get("name"), r.URL.Query().Get("tag"), "", true); err != nil {
		adminError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *server) upstreams(w http.ResponseWriter, r *http.Request) {
	items, err := a.engine.Upstreams()
	if err != nil {
		adminError(w, err)
		return
	}
	type public struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		URL      string `json:"url"`
		HasToken bool   `json:"hasToken"`
	}
	out := []public{}
	for _, u := range items {
		out = append(out, public{u.ID, u.Name, u.URL, u.Token != ""})
	}
	reply(w, 200, map[string]any{"items": out})
}
func (a *server) saveUpstream(w http.ResponseWriter, r *http.Request) {
	var in registry.Upstream
	if !decode(w, r, &in) {
		return
	}
	if err := a.engine.SaveUpstream(in); err != nil {
		adminError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *server) testUpstream(w http.ResponseWriter, r *http.Request) {
	result, err := a.engine.TestUpstream(r.Context(), r.PathValue("id"))
	if err != nil {
		adminError(w, err)
		return
	}
	reply(w, 200, result)
}
func (a *server) deleteUpstream(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.DeleteUpstream(r.PathValue("id")); err != nil {
		adminError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *server) routes(w http.ResponseWriter, r *http.Request) {
	items, err := a.engine.Routes()
	if err != nil {
		adminError(w, err)
		return
	}
	reply(w, 200, map[string]any{"items": items})
}
func (a *server) saveRoute(w http.ResponseWriter, r *http.Request) {
	var in registry.Route
	if !decode(w, r, &in) {
		return
	}
	if err := a.engine.SaveRoute(in); err != nil {
		adminError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *server) deleteRoute(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.DeleteRoute(r.URL.Query().Get("scope")); err != nil {
		adminError(w, err)
		return
	}
	w.WriteHeader(204)
}
