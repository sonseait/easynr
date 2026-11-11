package registry

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"easynr/internal/auth"
	"easynr/internal/state"
)

func engine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	s, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e, err := New(s, filepath.Join(dir, "registry"), "")
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func sri(data []byte) string {
	h := sha512.Sum512(data)
	return "sha512-" + base64.StdEncoding.EncodeToString(h[:])
}
func payload(name, version string, data []byte) []byte {
	b, _ := json.Marshal(Document{"name": name, "versions": Document{version: Document{"name": name, "version": version, "dist": Document{"integrity": sri(data)}}}, "dist-tags": Document{"latest": version}, "_attachments": Document{"package.tgz": Document{"data": base64.StdEncoding.EncodeToString(data)}}})
	return b
}

type upstreamFixture struct {
	Server              *httptest.Server
	Metadata, Downloads atomic.Int32
	Fail                atomic.Bool
	Corrupt             atomic.Bool
	NotModified         atomic.Bool
	Token               string
	Name                string
	Data                []byte
}

func remoteFixture(t *testing.T, name string) *upstreamFixture {
	t.Helper()
	f := &upstreamFixture{Name: name, Data: []byte("remote tarball"), Token: "upstream-secret"}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.Token {
			w.WriteHeader(401)
			return
		}
		if f.Fail.Load() {
			w.WriteHeader(503)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/blobs/") {
			f.Downloads.Add(1)
			if f.Corrupt.Load() {
				_, _ = w.Write([]byte("corrupt"))
			} else {
				_, _ = w.Write(f.Data)
			}
			return
		}
		f.Metadata.Add(1)
		if r.URL.Path != "/"+name {
			w.WriteHeader(404)
			return
		}
		if f.NotModified.Load() && r.Header.Get("If-None-Match") == `"fixture"` {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", `"fixture"`)
		_ = json.NewEncoder(w).Encode(Document{"name": name, "versions": Document{"1.0.0": Document{"name": name, "version": "1.0.0", "dist": Document{"tarball": f.Server.URL + "/blobs/1.tgz", "integrity": sri(f.Data)}}, "2.0.0": Document{"name": name, "version": "2.0.0", "dist": Document{"tarball": f.Server.URL + "/blobs/2.tgz", "integrity": sri(f.Data)}}}, "dist-tags": Document{"latest": "2.0.0", "next": "2.0.0"}})
	}))
	t.Cleanup(f.Server.Close)
	return f
}
func useRemote(t *testing.T, e *Engine, f *upstreamFixture, id, scope string) {
	t.Helper()
	if err := e.SaveUpstream(Upstream{ID: id, Name: id, URL: f.Server.URL, Token: f.Token}); err != nil {
		t.Fatal(err)
	}
	if err := e.SaveRoute(Route{Scope: scope, Upstream: id}); err != nil {
		t.Fatal(err)
	}
}
func idOf(v any) string {
	path := str(object(object(v)["dist"])["tarball"])
	return strings.TrimSuffix(strings.TrimPrefix(path, "/-/tarballs/"), ".tgz")
}
func TestMergeAndCache(t *testing.T) {
	e := engine(t)
	f := remoteFixture(t, "@team/pkg")
	useRemote(t, e, f, "private", "@team")
	if err := e.Publish(f.Name, payload(f.Name, "1.0.0", []byte("local"))); err != nil {
		t.Fatal(err)
	}
	doc, err := e.Metadata(context.Background(), f.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	versions := object(doc["versions"])
	if len(versions) != 2 || str(object(doc["dist-tags"])["latest"]) != "1.0.0" {
		t.Fatal("incorrect merge", doc)
	}
	a, err := e.Blob(context.Background(), idOf(versions["1.0.0"]))
	if err != nil || a.Source != "local" {
		t.Fatal(a, err)
	}
	a, err = e.Blob(context.Background(), idOf(versions["2.0.0"]))
	if err != nil || a.Source != "private" {
		t.Fatal(a, err)
	}
	if _, err := e.Metadata(context.Background(), f.Name, false); err != nil {
		t.Fatal(err)
	}
	if f.Metadata.Load() != 1 || f.Downloads.Load() != 1 {
		t.Fatal("cache miss", f.Metadata.Load(), f.Downloads.Load())
	}
	f.Fail.Store(true)
	e.TTL = -time.Second
	if _, err := e.Metadata(context.Background(), f.Name, false); err != nil {
		t.Fatal("stale cache failed", err)
	}
	if _, err := e.Blob(context.Background(), idOf(versions["2.0.0"])); err != nil {
		t.Fatal("offline blob failed", err)
	}
	if _, err := e.Metadata(context.Background(), "@team/missing", false); !errors.Is(err, ErrUpstream) {
		t.Fatal("missing offline error", err)
	}
	if err := e.DeleteLocal(f.Name, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	doc, err = e.Metadata(context.Background(), f.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	if idOf(object(doc["versions"])["1.0.0"]) == idOf(versions["1.0.0"]) {
		t.Fatal("upstream version did not reappear")
	}
}
func TestConditionalAndConcurrentDownloads(t *testing.T) {
	e := engine(t)
	f := remoteFixture(t, "pkg")
	useRemote(t, e, f, "mock", "*")
	doc, err := e.Metadata(context.Background(), "pkg", false)
	if err != nil {
		t.Fatal(err)
	}
	f.NotModified.Store(true)
	e.TTL = -time.Second
	if _, err = e.Metadata(context.Background(), "pkg", false); err != nil {
		t.Fatal(err)
	}
	if f.Metadata.Load() != 2 {
		t.Fatal("conditional request missing")
	}
	id := idOf(object(doc["versions"])["2.0.0"])
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Blob(context.Background(), id); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if f.Downloads.Load() != 1 {
		t.Fatal("download was not coalesced", f.Downloads.Load())
	}
}
func TestIntegrityAndPublishConflicts(t *testing.T) {
	e := engine(t)
	f := remoteFixture(t, "pkg")
	useRemote(t, e, f, "mock", "*")
	doc, err := e.Metadata(context.Background(), "pkg", false)
	if err != nil {
		t.Fatal(err)
	}
	id := idOf(object(doc["versions"])["1.0.0"])
	f.Corrupt.Store(true)
	if _, err := e.Blob(context.Background(), id); !errors.Is(err, ErrUpstream) {
		t.Fatal("corrupt blob accepted", err)
	}
	entries, _ := os.ReadDir(filepath.Join(e.Dir, "blobs"))
	if len(entries) != 0 {
		t.Fatal("corrupt blob persisted")
	}
	f.Corrupt.Store(false)
	if _, err := e.Blob(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	b := payload("pkg", "1.0.0", []byte("local"))
	if err := e.Publish("pkg", b); err != nil {
		t.Fatal(err)
	}
	if err := e.Publish("pkg", b); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate publish accepted", err)
	}
	var bad Document
	_ = json.Unmarshal(payload("pkg", "1.0.1", []byte("a")), &bad)
	object(object(bad["versions"])["1.0.1"])["name"] = "other"
	b, _ = json.Marshal(bad)
	if err := e.Publish("pkg", b); !errors.Is(err, ErrInvalid) {
		t.Fatal("mismatched manifest accepted")
	}
	if verifyBytes([]byte("a"), sri([]byte("b"))+" sha1-hvfkN/qlp/zhXR3cuerq6jd2Z7g=", "") == nil {
		t.Fatal("weaker SRI allowed to override stronger mismatch")
	}
}
func TestRoutesAndCredentialIsolation(t *testing.T) {
	e := engine(t)
	f := remoteFixture(t, "@team/pkg")
	useRemote(t, e, f, "first", "@team")
	doc, err := e.Metadata(context.Background(), f.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	oldID := idOf(object(doc["versions"])["2.0.0"])
	other := remoteFixture(t, f.Name)
	useRemote(t, e, other, "second", "@team")
	doc, err = e.Metadata(context.Background(), f.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	if idOf(object(doc["versions"])["2.0.0"]) == oldID || other.Metadata.Load() != 1 {
		t.Fatal("route reused wrong cache")
	}
	if err := e.DeleteUpstream("second"); !errors.Is(err, ErrConflict) {
		t.Fatal("deleted routed upstream")
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credential leaked across redirect")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	u := Upstream{URL: redirect.URL, Token: "secret"}
	req, _ := e.request(context.Background(), u, redirect.URL)
	resp, err := e.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := e.SaveUpstream(Upstream{ID: "bad", Name: "bad", URL: "https://secret@example.com"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("credentials accepted in URL", err)
	}
}
func TestGCAndCacheClear(t *testing.T) {
	e := engine(t)
	f := remoteFixture(t, "pkg")
	useRemote(t, e, f, "mock", "*")
	if err := e.Publish("pkg", payload("pkg", "1.0.0", []byte("local"))); err != nil {
		t.Fatal(err)
	}
	doc, err := e.Metadata(context.Background(), "pkg", false)
	if err != nil {
		t.Fatal(err)
	}
	localID := idOf(object(doc["versions"])["1.0.0"])
	remoteID := idOf(object(doc["versions"])["2.0.0"])
	if _, err := e.Blob(context.Background(), remoteID); err != nil {
		t.Fatal(err)
	}
	if err := e.ClearCache("pkg"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Blob(context.Background(), localID); err != nil {
		t.Fatal("clear cache removed local", err)
	}
	dbPath := e.Store.DB.Path()
	before, _ := os.ReadFile(dbPath)
	result, err := e.GC(true)
	if err != nil || result.Blobs != 1 {
		t.Fatal(result, err)
	}
	after, _ := os.ReadFile(dbPath)
	if !bytes.Equal(before, after) {
		t.Fatal("dry run mutated DB")
	}
	if result, err = e.GC(false); err != nil || result.Blobs != 1 {
		t.Fatal(result, err)
	}
	if _, err := e.Blob(context.Background(), localID); err != nil {
		t.Fatal("GC removed local", err)
	}
}
func TestProtocolPermissionsAndSelectors(t *testing.T) {
	e := engine(t)
	f := remoteFixture(t, "@team/pkg")
	useRemote(t, e, f, "mock", "@team")
	rec, secret, err := e.Auth.CreateToken("CI", []auth.Grant{{Repository: "@team/*", Permission: "read"}})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token string) int {
		r := httptest.NewRequest(method, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w.Code
	}
	if got := request("GET", "/@team%2fpkg/2.0.0", secret); got != 200 {
		t.Fatal("scoped version", got)
	}
	if got := request("GET", "/@team%2fpkg/9.0.0", secret); got != 404 || f.Metadata.Load() != 2 {
		t.Fatal("missing version not refreshed", got, f.Metadata.Load())
	}
	for _, tc := range []struct {
		method, path, token string
		status              int
	}{{"GET", "/@team%2fpkg", "", 401}, {"GET", "/other", secret, 403}, {"PUT", "/@team%2fpkg", secret, 403}, {"DELETE", "/@team%2fpkg", secret, 403}, {"GET", "/-/whoami", secret, 200}} {
		if got := request(tc.method, tc.path, tc.token); got != tc.status {
			t.Fatalf("%v got %d", tc, got)
		}
	}
	if err := e.Auth.Revoke(rec.ID); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", "/@team%2fpkg", secret); got != 401 {
		t.Fatal("revoked token accepted", got)
	}
}
func TestTagsAndPersistence(t *testing.T) {
	e := engine(t)
	f := remoteFixture(t, "pkg")
	useRemote(t, e, f, "mock", "*")
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if err := e.Publish("pkg", payload("pkg", v, []byte(v))); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.SetTag("pkg", "beta", "1.0.0", false); err != nil {
		t.Fatal(err)
	}
	if err := e.SetTag("pkg", "1.0.0", "1.1.0", false); !errors.Is(err, ErrInvalid) {
		t.Fatal("semver tag accepted")
	}
	if err := e.SetTag("pkg", "latest", "", true); err != nil {
		t.Fatal(err)
	}
	doc, err := e.Metadata(context.Background(), "pkg", false)
	if err != nil {
		t.Fatal(err)
	}
	if str(object(doc["dist-tags"])["latest"]) != "2.0.0" {
		t.Fatal("remote tag did not reappear")
	}
	path := filepath.Dir(e.Store.DB.Path())
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e, err = New(s, e.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	f.Fail.Store(true)
	if _, err := e.Metadata(context.Background(), "pkg", false); err != nil {
		t.Fatal("metadata lost on restart", err)
	}
	a, err := e.Blob(context.Background(), idOf(object(doc["versions"])["1.0.0"]))
	if err != nil || a.Source != "local" {
		t.Fatal("local lost on restart", err)
	}
}

func TestUpstreamIdentityCannotBeReused(t *testing.T) {
	e := engine(t)
	if err := e.SaveUpstream(Upstream{ID: "local", Name: "Reserved", URL: "https://example.com"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("reserved source accepted", err)
	}
	u := Upstream{ID: "private", Name: "Private", URL: "https://first.example.com", Token: "secret"}
	if err := e.SaveUpstream(u); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteUpstream(u.ID); err != nil {
		t.Fatal(err)
	}
	u.URL = "https://second.example.com"
	if err := e.SaveUpstream(u); !errors.Is(err, ErrConflict) {
		t.Fatal("ID reused for another origin", err)
	}
}
func TestSignedTarballURLAndCrossOriginToken(t *testing.T) {
	e := engine(t)
	data := []byte("signed blob")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("upstream token sent to blob origin")
		}
		if r.URL.Query().Get("signature") != "signed-value" {
			t.Error("signed URL query lost")
		}
		_, _ = w.Write(data)
	}))
	defer target.Close()
	var remote *httptest.Server
	remote = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("metadata token missing")
		}
		_ = json.NewEncoder(w).Encode(Document{"name": "pkg", "versions": Document{"1.0.0": Document{"name": "pkg", "version": "1.0.0", "dist": Document{"tarball": target.URL + "/blob?signature=signed-value", "integrity": sri(data)}}}, "dist-tags": Document{"latest": "1.0.0"}})
	}))
	defer remote.Close()
	if err := e.SaveUpstream(Upstream{ID: "signed", Name: "Signed", URL: remote.URL, Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	if err := e.SaveRoute(Route{Scope: "*", Upstream: "signed"}); err != nil {
		t.Fatal(err)
	}
	doc, err := e.Metadata(context.Background(), "pkg", false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(doc)
	if bytes.Contains(b, []byte("signed-value")) {
		t.Fatal("signed upstream URL exposed")
	}
	if _, err := e.Blob(context.Background(), idOf(object(doc["versions"])["1.0.0"])); err != nil {
		t.Fatal(err)
	}
}
func TestPackageListExcludesOrphans(t *testing.T) {
	e := engine(t)
	for _, version := range []string{"1.0.0", "2.0.0"} {
		if err := e.Publish("pkg", payload("pkg", version, []byte("same content"))); err != nil {
			t.Fatal(err)
		}
	}
	items, err := e.Packages()
	if err != nil || len(items) != 1 || items[0].LocalBytes != int64(len("same content")) {
		t.Fatal("shared blob counted twice", items, err)
	}
	if err := e.DeleteLocal("pkg", ""); err != nil {
		t.Fatal(err)
	}
	items, err = e.Packages()
	if err != nil || len(items) != 0 {
		t.Fatal("deleted package still listed from orphaned tarballs", items, err)
	}
}

func TestDirectLocalVersionDoesNotContactRemote(t *testing.T) {
	e := engine(t)
	var requests atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(503) }))
	defer remote.Close()
	if err := e.SaveUpstream(Upstream{ID: "mock", Name: "Mock", URL: remote.URL}); err != nil {
		t.Fatal(err)
	}
	if err := e.SaveRoute(Route{Scope: "*", Upstream: "mock"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Publish("pkg", payload("pkg", "1.0.0", []byte("local"))); err != nil {
		t.Fatal(err)
	}
	_, secret, err := e.Auth.CreateToken("read", []auth.Grant{{Repository: "pkg", Permission: "read"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"1.0.0", "latest"} {
		r := httptest.NewRequest("GET", "/pkg/"+selector, nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if requests.Load() != 0 {
		t.Fatal("known local version queried remote")
	}
}
func TestCacheClearDuringRevalidationDoesNotResurrectBrokenReferences(t *testing.T) {
	e := engine(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			close(entered)
			<-release
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", `"cached"`)
		_ = json.NewEncoder(w).Encode(Document{"name": "pkg", "versions": Document{"1.0.0": Document{"name": "pkg", "version": "1.0.0", "dist": Document{"tarball": "https://example.com/pkg.tgz", "integrity": sri([]byte("data"))}}}})
	}))
	defer remote.Close()
	if err := e.SaveUpstream(Upstream{ID: "mock", Name: "Mock", URL: remote.URL}); err != nil {
		t.Fatal(err)
	}
	if err := e.SaveRoute(Route{Scope: "*", Upstream: "mock"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Metadata(context.Background(), "pkg", false); err != nil {
		t.Fatal(err)
	}
	e.TTL = -time.Second
	done := make(chan error, 1)
	go func() { _, err := e.Metadata(context.Background(), "pkg", false); done <- err }()
	<-entered
	err := e.ClearCache("pkg")
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatal("cleared metadata resurrected", err)
	}
	if _, err := e.Inspect(context.Background(), "pkg"); err != nil {
		t.Fatal("refresh did not rebuild artifact references", err)
	}
}

func TestPackageListOnlyIncludesPublishedPackages(t *testing.T) {
	e := engine(t)
	remote := remoteFixture(t, "@remote/cached")
	useRemote(t, e, remote, "remote", "@remote")
	doc, err := e.Metadata(context.Background(), remote.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Blob(context.Background(), idOf(object(doc["versions"])["1.0.0"])); err != nil {
		t.Fatal(err)
	}
	items, err := e.Packages()
	if err != nil || len(items) != 0 {
		t.Fatal("remote cache appeared in published list", items, err)
	}
	local := remoteFixture(t, "@local/published")
	useRemote(t, e, local, "local-remote", "@local")
	if err := e.Publish(local.Name, payload(local.Name, "1.0.0", []byte("published content"))); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Metadata(context.Background(), local.Name, false); err != nil {
		t.Fatal(err)
	}
	items, err = e.Packages()
	if err != nil || len(items) != 1 || items[0].Name != local.Name || items[0].LocalVersions != 1 {
		t.Fatal("local package missing when also present upstream", items, err)
	}
	stats, err := e.CacheStatistics()
	if err != nil || stats.Packages != 2 || stats.Bytes != int64(len(remote.Data)) {
		t.Fatal("list filtering changed cache stats", stats, err)
	}
	if err := e.DeleteLocal(local.Name, ""); err != nil {
		t.Fatal(err)
	}
	items, err = e.Packages()
	if err != nil || len(items) != 0 {
		t.Fatal("package still listed after removing local versions", items, err)
	}
}
