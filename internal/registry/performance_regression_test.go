package registry

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"easynr/internal/auth"
)

func TestInstallMetadataVariants(t *testing.T) {
	e := engine(t)
	name := "@team/pkg"
	data := []byte("remote tarball")
	var mu sync.Mutex
	calls := map[string]int{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/blob" {
			_, _ = w.Write(data)
			return
		}
		format := r.Header.Get("Accept")
		mu.Lock()
		calls[format]++
		mu.Unlock()
		etag := fmt.Sprintf("%q", format)
		if previous := r.Header.Get("If-None-Match"); previous != "" {
			if previous != etag {
				t.Error("validator shared between full and install metadata", previous, etag)
			}
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", etag)
		doc := Document{"name": name, "versions": Document{"1.0.0": Document{"name": name, "version": "1.0.0",
			"dependencies": Document{"dep": "^1.0.0"}, "optionalDependencies": Document{"optional": "^2"},
			"peerDependencies": Document{"peer": "*"}, "bin": Document{"cli": "cli.js"},
			"dist": Document{"tarball": "/blob", "integrity": sri(data)}}}, "dist-tags": Document{"latest": "1.0.0"}}
		if format != installMediaType {
			doc["readme"] = "full documentation"
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer remote.Close()
	if err := e.SaveUpstream(Upstream{ID: "mock", Name: "mock", URL: remote.URL}); err != nil {
		t.Fatal(err)
	}
	if err := e.SaveRoute(Route{Scope: "@team", Upstream: "mock"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Publish(name, payload(name, "2.0.0", []byte("local"))); err != nil {
		t.Fatal(err)
	}
	_, token, err := e.Auth.CreateToken("read", []auth.Grant{{Repository: "*", Permission: "read"}})
	if err != nil {
		t.Fatal(err)
	}
	request := func(format string) Document {
		t.Helper()
		r := httptest.NewRequest("GET", "/@team%2fpkg", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Accept", format)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("Vary") != "Accept" {
			t.Fatal(w.Code, w.Header(), w.Body.String())
		}
		var doc Document
		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if len(object(doc["versions"])) != 2 || str(object(doc["dist-tags"])["latest"]) != "2.0.0" {
			t.Fatal("local precedence lost", doc)
		}
		item := object(object(doc["versions"])["1.0.0"])
		for _, key := range []string{"dependencies", "optionalDependencies", "peerDependencies", "bin"} {
			if item[key] == nil {
				t.Fatal("install field lost", key)
			}
		}
		if !strings.HasPrefix(str(object(item["dist"])["tarball"]), "http://example.com/-/tarballs/") {
			t.Fatal("tarball URL not rewritten", item)
		}
		return doc
	}
	install := request(installMediaType)
	if install["readme"] != nil {
		t.Fatal("install fetched full metadata")
	}
	if request("application/json")["readme"] != "full documentation" {
		t.Fatal("install cache used as full metadata")
	}
	request(installMediaType)
	request("application/json")
	mu.Lock()
	if calls[installMediaType] != 1 || calls["application/json"] != 1 {
		t.Error("metadata not cached", calls)
	}
	mu.Unlock()
	e.TTL = -time.Second
	request(installMediaType)
	request("application/json") // Verify independent ETag/304 revalidation.
	version := object(object(install["versions"])["1.0.0"])
	path := str(object(version["dist"])["tarball"])
	id := strings.TrimSuffix(strings.TrimPrefix(path, "http://example.com/-/tarballs/"), ".tgz")
	if _, err := e.Blob(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	stats, err := e.CacheStatistics()
	if err != nil || stats.Packages != 1 || stats.Bytes != int64(len(data)) {
		t.Fatal("formats double-counted", stats, err)
	}
	if result, err := e.GC(true); err != nil || result.Blobs != 0 || result.Artifacts != 0 {
		t.Fatal("GC lost references", result, err)
	}
	if err := e.ClearCache(name); err != nil {
		t.Fatal(err)
	}
	stats, err = e.CacheStatistics()
	if err != nil || stats.Packages != 0 || stats.Bytes != 0 {
		t.Fatal("clear missed a representation", stats, err)
	}
}

func TestInstallUsesExistingFullCache(t *testing.T) {
	e := engine(t)
	f := remoteFixture(t, "pkg")
	useRemote(t, e, f, "mock", "*")
	if _, err := e.Metadata(context.Background(), "pkg", false); err != nil {
		t.Fatal(err)
	}
	f.Fail.Store(true)
	for _, ttl := range []time.Duration{time.Hour, -time.Second} {
		e.TTL = ttl
		if _, err := e.metadata(context.Background(), "pkg", false, true); err != nil {
			t.Fatal("existing cache unusable for install", ttl, err)
		}
	}
}

func TestFullMetadataCanUseInstallCacheOffline(t *testing.T) {
	e := engine(t)
	f := remoteFixture(t, "pkg")
	useRemote(t, e, f, "mock", "*")
	if _, err := e.metadata(context.Background(), "pkg", false, true); err != nil {
		t.Fatal(err)
	}
	f.Fail.Store(true)
	if doc, err := e.Metadata(context.Background(), "pkg", false); err != nil || len(object(doc["versions"])) != 2 {
		t.Fatal("cached versions unavailable offline", doc, err)
	}
}

func TestClientCacheAndRevalidation(t *testing.T) {
	e := engine(t)
	f := remoteFixture(t, "pkg")
	useRemote(t, e, f, "mock", "*")
	record, token, err := e.Auth.CreateToken("read", []auth.Grant{{Repository: "pkg", Permission: "read"}})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, credential, etag string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("If-None-Match", etag)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w
	}
	metadata := request("GET", "/pkg", token, "")
	var doc Document
	if err := json.Unmarshal(metadata.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	blob := strings.TrimPrefix(str(object(object(object(doc["versions"])["1.0.0"])["dist"])["tarball"]), "http://example.com")
	for _, path := range []string{"/pkg", "/pkg/1.0.0", "/-/package/pkg/dist-tags", blob} {
		t.Run(path, func(t *testing.T) {
			first := request("GET", path, token, "")
			wantCache := "private, max-age=300, must-revalidate"
			if path == blob {
				wantCache = "private, max-age=31536000, immutable"
			}
			etag := first.Header().Get("ETag")
			if first.Code != 200 || etag == "" || first.Header().Get("Cache-Control") != wantCache {
				t.Fatal(first.Code, first.Header(), first.Body.String())
			}
			cached := request("GET", path, token, `"other", W/`+etag)
			if cached.Code != 304 || cached.Body.Len() != 0 || cached.Header().Get("ETag") != etag || cached.Header().Get("Cache-Control") != wantCache {
				t.Fatal("unchanged content transferred", cached.Code, cached.Header(), cached.Body.String())
			}
			if path != "/-/package/pkg/dist-tags" {
				head := request("HEAD", path, token, "")
				if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("ETag") != etag {
					t.Fatal("HEAD differs from GET", head.Code, head.Header(), head.Body.String())
				}
			}
			denied := request("GET", path, "invalid", etag)
			if denied.Code != 401 || denied.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("conditional request bypassed authentication", denied.Code, denied.Header())
			}
		})
	}
	if err := e.Publish("pkg", payload("pkg", "3.0.0", []byte("new local version"))); err != nil {
		t.Fatal(err)
	}
	changed := request("GET", "/pkg", token, metadata.Header().Get("ETag"))
	if changed.Code != 200 || changed.Header().Get("ETag") == metadata.Header().Get("ETag") || !strings.Contains(changed.Body.String(), "3.0.0") {
		t.Fatal("published metadata not refreshed", changed.Code, changed.Header(), changed.Body.String())
	}
	for path, status := range map[string]int{"/other": 403, "/pkg/9.0.0": 404} {
		response := request("GET", path, token, metadata.Header().Get("ETag"))
		if response.Code != status || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("error cached", path, response.Code, response.Header())
		}
	}
	if err := e.Auth.Revoke(record.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/pkg", blob} {
		response := request("GET", path, token, metadata.Header().Get("ETag"))
		if response.Code != 401 || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("revoked token accepted", response.Code, response.Header())
		}
	}
}

func TestRenderDoesNotMutateSharedMetadata(t *testing.T) {
	doc := benchmarkDocument()
	before, _ := json.Marshal(doc)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			base := fmt.Sprintf("https://registry-%d.example.com", i)
			rendered := render(doc, base)
			for _, value := range object(rendered["versions"]) {
				if !strings.HasPrefix(str(object(object(value)["dist"])["tarball"]), base+"/-/tarballs/") {
					t.Error("response reused another request's URL")
				}
			}
		})
	}
	wg.Wait()
	after, _ := json.Marshal(doc)
	if string(before) != string(after) {
		t.Fatal("rendering modified cache")
	}
}

func TestChecksumAlgorithms(t *testing.T) {
	data := []byte("checksum fixture")
	sha1sum := sha1.Sum(data)
	sha256sum := sha256.Sum256(data)
	sha384sum := sha512.Sum384(data)
	sha512sum := sha512.Sum512(data)
	for algorithm, sum := range map[string][]byte{"sha1": sha1sum[:], "sha256": sha256sum[:], "sha384": sha384sum[:], "sha512": sha512sum[:]} {
		t.Run(algorithm, func(t *testing.T) {
			integrity := algorithm + "-" + base64.StdEncoding.EncodeToString(sum)
			for _, shasum := range []string{"", hex.EncodeToString(sha1sum[:])} {
				if err := verifyBytes(data, integrity, shasum); err != nil {
					t.Fatal(err)
				}
				if err := verifyBytes([]byte("corrupt"), integrity, shasum); err == nil {
					t.Fatal("corrupt data accepted")
				}
			}
			if err := verifyBytes(data, algorithm+"-invalid "+integrity+"?option", ""); err != nil {
				t.Fatal("alternative digest of same strength rejected", err)
			}
		})
	}
	if err := verifyBytes(data, "sha999-digest", ""); err == nil {
		t.Fatal("unsupported checksum accepted")
	}
}

func TestAcceptInstallMetadata(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   bool
	}{{"", false}, {"application/json", false}, {installMediaType, true},
		{installMediaType + ";q=1.0, application/json;q=0.8, */*", true},
		{installMediaType + ";q=0", false}, {installMediaType + ";q=0.2, application/json", false},
		{installMediaType + ";q=invalid", false}} {
		if got := wantsInstallMetadata(tc.header); got != tc.want {
			t.Errorf("Accept %q: got %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestQueuedMetadataRequestCancellation(t *testing.T) {
	e := engine(t)
	_, token, err := e.Auth.CreateToken("read", []auth.Grant{{Repository: "*", Permission: "read"}})
	if err != nil {
		t.Fatal(err)
	}
	for range cap(e.metadataSlots) {
		e.metadataSlots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("GET", "/pkg", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	done := make(chan struct{})
	go func() { e.ServeHTTP(httptest.NewRecorder(), r); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disconnected request held in metadata queue")
	}
}

type blockedMetadataResponse struct {
	header  http.Header
	entered chan<- struct{}
	release <-chan struct{}
}

func (w blockedMetadataResponse) Header() http.Header { return w.header }
func (w blockedMetadataResponse) WriteHeader(int)     {}
func (w blockedMetadataResponse) Write(p []byte) (int, error) {
	w.entered <- struct{}{}
	<-w.release
	return len(p), nil
}

func TestInstallBurstBoundsMetadataThroughResponseEncoding(t *testing.T) {
	e := engine(t)
	if err := e.Publish("pkg", payload("pkg", "1.0.0", []byte("local"))); err != nil {
		t.Fatal(err)
	}
	_, token, err := e.Auth.CreateToken("read", []auth.Grant{{Repository: "*", Permission: "read"}})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	var wg sync.WaitGroup
	defer wg.Wait()
	defer close(release)
	for range 16 {
		wg.Go(func() {
			r := httptest.NewRequest("GET", "/pkg/1.0.0", nil)
			r.Header.Set("Authorization", "Bearer "+token)
			e.ServeHTTP(blockedMetadataResponse{http.Header{}, entered, release}, r)
		})
	}
	for range 4 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("metadata workers did not start")
		}
	}
	select {
	case <-entered:
		t.Fatal("more than four decoded responses active simultaneously")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestQueuedDownloadCancellationAndWarmCacheBypass(t *testing.T) {
	e := engine(t)
	f := remoteFixture(t, "pkg")
	useRemote(t, e, f, "mock", "*")
	doc, err := e.Metadata(context.Background(), "pkg", false)
	if err != nil {
		t.Fatal(err)
	}
	id := idOf(object(doc["versions"])["1.0.0"])
	for range cap(e.downloadSlots) {
		e.downloadSlots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Blob(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatal("queued download did not cancel", err)
	}
	for range cap(e.downloadSlots) {
		<-e.downloadSlots
	}
	if _, err := e.Blob(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	for range cap(e.downloadSlots) {
		e.downloadSlots <- struct{}{}
	}
	if _, err := e.Blob(ctx, id); err != nil {
		t.Fatal("warm cache waited for a download slot", err)
	}
}
