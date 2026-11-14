package registry

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"easynr/internal/auth"
)

func TestNPMTarballURLs(t *testing.T) {
	for _, name := range []string{"minimatch", "@team/pkg"} {
		t.Run(name, func(t *testing.T) {
			e := engine(t)
			f := remoteFixture(t, name)
			useRemote(t, e, f, "mock", "*")
			_, secret, err := e.Auth.CreateToken("read", []auth.Grant{{Repository: name, Permission: "read"}})
			if err != nil {
				t.Fatal(err)
			}
			request := func(method, path, token string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, nil)
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				e.ServeHTTP(w, r)
				return w
			}
			basename := name[strings.LastIndex(name, "/")+1:]
			path := "/" + name + "/-/" + basename + "-1.0.0.tgz"
			if w := request("GET", path, ""); w.Code != 401 || f.Metadata.Load() != 0 {
				t.Fatal("anonymous tarball lookup", w.Code, f.Metadata.Load())
			}
			if w := request("GET", "/other/-/other-1.0.0.tgz", secret); w.Code != 403 || f.Metadata.Load() != 0 {
				t.Fatal("forbidden tarball lookup", w.Code, f.Metadata.Load())
			}
			w := request("GET", path, secret)
			location := w.Header().Get("Location")
			if w.Code != 307 || !strings.HasPrefix(location, "/-/tarballs/") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("tarball not resolved", w.Code, w.Header(), w.Body.String())
			}
			if w := request("GET", location, secret); w.Code != 200 || w.Body.String() != string(f.Data) {
				t.Fatal("resolved tarball failed", w.Code, w.Body.String())
			}
			for _, method := range []string{"GET", "HEAD"} {
				f.Fail.Store(true)
				if w := request(method, path, secret); w.Code != 307 || w.Header().Get("Location") != location {
					t.Fatal("offline alias failed", method, w.Code)
				}
				if w := request(method, location, secret); w.Code != 200 || method == "HEAD" && w.Body.Len() != 0 {
					t.Fatal("offline tarball failed", method, w.Code)
				}
			}
			if err := e.Publish(name, payload(name, "1.0.0", []byte("local"))); err != nil {
				t.Fatal(err)
			}
			w = request("GET", path, secret)
			if w.Code != 307 || w.Header().Get("Location") == location {
				t.Fatal("local version did not take precedence", w.Code)
			}
			if w := request("GET", w.Header().Get("Location"), secret); w.Code != 200 || w.Body.String() != "local" {
				t.Fatal("local alias failed", w.Code, w.Body.String())
			}
			if w := request("GET", location, secret); w.Code != 200 || w.Body.String() != string(f.Data) {
				t.Fatal("original artifact changed", w.Code, w.Body.String())
			}
			for _, tc := range []struct {
				method, filename string
				status           int
			}{{"GET", basename + "-9.0.0.tgz", 404}, {"GET", basename + "-latest.tgz", 400}, {"GET", "wrong-1.0.0.tgz", 400}, {"GET", basename + "-1.0.0.tgz/extra", 400}, {"PUT", basename + "-1.0.0.tgz", 405}} {
				if w := request(tc.method, "/"+strings.ReplaceAll(name, "/", "%2f")+"/-/"+tc.filename, secret); w.Code != tc.status {
					t.Fatal(tc, w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestNPMErrorLogging(t *testing.T) {
	e := engine(t)
	f := remoteFixture(t, "pkg")
	useRemote(t, e, f, "mock", "*")
	var logs bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	_, secret, err := e.Auth.CreateToken("read", []auth.Grant{{Repository: "pkg", Permission: "read"}})
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w
	}
	if w := request("/pkg/-/pkg-latest.tgz?token=query-secret"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	for _, field := range []string{"level=WARN", "method=GET", "path=/pkg/-/pkg-latest.tgz", "status=400", "code=EINVALID", "expected /<package>"} {
		if !strings.Contains(logs.String(), field) {
			t.Fatal("missing log field", field, logs.String())
		}
	}
	w := request("/pkg/-/pkg-1.0.0.tgz")
	if w.Code != 307 {
		t.Fatal(w.Code)
	}
	f.Fail.Store(true)
	if w := request(w.Header().Get("Location")); w.Code != 502 {
		t.Fatal(w.Code)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "status=502") || !strings.Contains(logs.String(), "returned HTTP 503") {
		t.Fatal("missing upstream failure details", logs.String())
	}
	for _, sensitive := range []string{secret, f.Token, "query-secret", f.Server.URL} {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatal("request log contains a secret or upstream URL")
		}
	}
}
