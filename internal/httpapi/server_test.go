package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"easynr/internal/auth"
	"easynr/internal/registry"
	"easynr/internal/state"
)

func TestAdminSecurityAndManagement(t *testing.T) {
	dir := t.TempDir()
	s, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var log bytes.Buffer
	if err := auth.EnsureBootstrap(s, &log); err != nil {
		t.Fatal(err)
	}
	password := strings.TrimSpace(strings.Split(log.String(), "password=")[1])
	engine, err := registry.New(s, filepath.Join(dir, "registry"), "")
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(auth.Service{Store: s}, engine)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	csrf := ""
	req := func(method, path string, body any, origin, withCSRF bool) (int, []byte) {
		t.Helper()
		var b io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			b = bytes.NewReader(data)
		}
		r, _ := http.NewRequest(method, srv.URL+path, b)
		if origin {
			r.Header.Set("Origin", srv.URL)
		}
		if withCSRF {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, data
	}
	if code, _ := req("GET", "/api/packages", nil, false, false); code != 401 {
		t.Fatal(code)
	}
	if code, _ := req("POST", "/api/upstreams/probe/test", nil, true, false); code != 401 {
		t.Fatal("anonymous upstream probe allowed", code)
	}
	if code, _ := req("POST", "/api/login", map[string]string{"password": password}, false, false); code != 403 {
		t.Fatal("origin bypass", code)
	}
	code, data := req("POST", "/api/login", map[string]string{"password": password}, true, false)
	if code != 200 {
		t.Fatal(code, string(data))
	}
	var session map[string]string
	_ = json.Unmarshal(data, &session)
	csrf = session["csrfToken"]
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer probe.Close()
	if err := engine.SaveUpstream(registry.Upstream{ID: "probe", Name: "Probe", URL: probe.URL}); err != nil {
		t.Fatal(err)
	}
	if code, _ := req("POST", "/api/upstreams/probe/test", nil, true, false); code != 403 {
		t.Fatal("upstream probe CSRF bypass", code)
	}
	if code, _ := req("POST", "/api/upstreams/probe/test", nil, false, true); code != 403 {
		t.Fatal("upstream probe Origin bypass", code)
	}
	if code, data := req("POST", "/api/upstreams/probe/test", nil, true, true); code != 200 || !bytes.Contains(data, []byte(`"ok":true`)) || !bytes.Contains(data, []byte(`"statusCode":204`)) {
		t.Fatal("upstream probe failed", code, string(data))
	}
	if code, _ := req("POST", "/api/upstreams/missing/test", nil, true, true); code != 404 {
		t.Fatal("unknown upstream probe", code)
	}
	tokenBody := map[string]any{"name": "<img src=x onerror=alert(1)>", "grants": []auth.Grant{{Repository: "@team/*", Permission: "write"}}}
	if code, _ := req("POST", "/api/tokens", tokenBody, true, false); code != 403 {
		t.Fatal("csrf bypass", code)
	}
	code, data = req("POST", "/api/tokens", tokenBody, true, true)
	if code != 201 {
		t.Fatal(code, string(data))
	}
	var created struct {
		Secret string           `json:"secret"`
		Token  auth.TokenRecord `json:"token"`
	}
	_ = json.Unmarshal(data, &created)
	if code, data = req("GET", "/api/tokens", nil, false, false); code != 200 || bytes.Contains(data, []byte(created.Secret)) || bytes.Contains(data, []byte(`"hash"`)) {
		t.Fatal("token list leaks", code, string(data))
	}
	for _, u := range []registry.Upstream{{ID: "private", Name: "Private", URL: "https://registry.example.com", Token: "secret"}, {ID: "private", Name: "Private edited", URL: "https://registry.example.com"}} {
		if code, data := req("PUT", "/api/upstreams", u, true, true); code != 204 {
			t.Fatal(code, string(data))
		}
	}
	if code, data = req("GET", "/api/upstreams", nil, false, false); code != 200 || bytes.Contains(data, []byte(`"secret"`)) || !bytes.Contains(data, []byte(`"hasToken":true`)) {
		t.Fatal("upstream secret leaked or lost", code, string(data))
	}
	if code, data := req("PUT", "/api/routes", registry.Route{Scope: "@team", Upstream: "private"}, true, true); code != 204 {
		t.Fatal(code, string(data))
	}
	if code, _ := req("DELETE", "/api/upstreams/private", nil, true, true); code != 409 {
		t.Fatal("deleted in-use upstream", code)
	}
	if code, _ := req("PUT", "/api/routes", registry.Route{Scope: "@team", Upstream: "local-only"}, true, true); code != 400 {
		t.Fatal("invalid upstream accepted", code)
	}
	if code, _ := req("DELETE", "/api/routes?scope=%40team", nil, true, true); code != 204 {
		t.Fatal(code)
	}
	if code, _ := req("DELETE", "/api/upstreams/private", nil, true, true); code != 204 {
		t.Fatal(code)
	}
	if code, _ := req("DELETE", "/api/tokens/"+created.Token.ID, nil, true, true); code != 204 {
		t.Fatal(code)
	}
	if _, err := engine.Auth.Authenticate(created.Secret); err == nil {
		t.Fatal("revoked credential accepted")
	}
	if code, _ := req("POST", "/api/logout", nil, true, true); code != 204 {
		t.Fatal(code)
	}
	if code, _ := req("GET", "/api/session", nil, false, false); code != 401 {
		t.Fatal("session remains valid", code)
	}
	if code, _ := req("GET", "/-/ui/app.js", nil, false, false); code != 200 {
		t.Fatal("asset missing", code)
	}
	if code, _ := req("GET", "/app.js", nil, false, false); code != 401 {
		t.Fatal("valid npm name shadows static route", code)
	}
}
