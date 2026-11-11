package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"easynr/internal/auth"
	"easynr/internal/httpapi"
	"easynr/internal/registry"
	"easynr/internal/state"
)

func archive(name, version, source string) []byte {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	manifest, _ := json.Marshal(map[string]any{"name": name, "version": version, "main": "index.js"})
	for path, data := range map[string][]byte{"package/package.json": manifest, "package/index.js": []byte("module.exports = " + fmt.Sprintf("%q", source) + ";\n")} {
		_ = tw.WriteHeader(&tar.Header{Name: path, Mode: 0644, Size: int64(len(data))})
		_, _ = tw.Write(data)
	}
	_ = tw.Close()
	_ = gz.Close()
	return out.Bytes()
}
func TestNPMWorkflow(t *testing.T) {
	if os.Getenv("EASYNR_E2E") != "1" {
		t.Skip("set EASYNR_E2E=1 to exercise real npm CLI")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := state.Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := auth.EnsureBootstrap(s, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	engine, err := registry.New(s, filepath.Join(dir, "data", "registry"), "")
	if err != nil {
		t.Fatal(err)
	}
	var offline atomic.Bool
	var downloads atomic.Int32
	names := []string{"nr-fixture", "@team/nr-fixture"}
	tarballs := map[string][]byte{}
	for _, name := range names {
		for _, v := range []string{"1.0.0", "3.0.0"} {
			tarballs[name+"@"+v] = archive(name, v, "remote-"+v)
		}
	}
	var remote *httptest.Server
	remote = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			w.WriteHeader(503)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/tar/") {
			key := strings.TrimPrefix(r.URL.Path, "/tar/")
			data, ok := tarballs[key]
			if !ok {
				w.WriteHeader(404)
				return
			}
			downloads.Add(1)
			_, _ = w.Write(data)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		versions := map[string]any{}
		for _, v := range []string{"1.0.0", "3.0.0"} {
			data, ok := tarballs[name+"@"+v]
			if !ok {
				w.WriteHeader(404)
				return
			}
			sum := sha512.Sum512(data)
			versions[v] = map[string]any{"name": name, "version": v, "dist": map[string]any{"tarball": remote.URL + "/tar/" + name + "@" + v, "integrity": "sha512-" + base64.StdEncoding.EncodeToString(sum[:])}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "versions": versions, "dist-tags": map[string]string{"latest": "3.0.0"}})
	}))
	defer remote.Close()
	if err := engine.SaveUpstream(registry.Upstream{ID: "fixture", Name: "fixture", URL: remote.URL}); err != nil {
		t.Fatal(err)
	}
	if err := engine.SaveRoute(registry.Route{Scope: "*", Upstream: "fixture"}); err != nil {
		t.Fatal(err)
	}
	h, err := httpapi.New(auth.Service{Store: s}, engine)
	if err != nil {
		t.Fatal(err)
	}
	var registryReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && (r.URL.Path == "/nr-fixture" || strings.HasPrefix(r.URL.Path, "/-/tarballs/")) {
			registryReads.Add(1)
		}
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	rec, token, err := engine.Auth.CreateToken("e2e", []auth.Grant{{Repository: "*", Permission: "write"}})
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "npmrc")
	if err := os.WriteFile(config, []byte("registry="+server.URL+"/\n//"+strings.TrimPrefix(server.URL, "http://")+"/:_authToken="+token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	npm := func(t *testing.T, work string, wantOK bool, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "npm", args...)
		cmd.Dir = work
		// A new npm cache for each command proves that EasyNR, rather than npm's cache, serves offline downloads.
		cmd.Env = append(os.Environ(), "NPM_CONFIG_USERCONFIG="+config, "NPM_CONFIG_GLOBALCONFIG="+filepath.Join(dir, "globalrc"), "NPM_CONFIG_CACHE="+t.TempDir(), "NPM_CONFIG_FETCH_RETRIES=0", "NPM_CONFIG_AUDIT=false", "NPM_CONFIG_FUND=false", "NPM_CONFIG_UPDATE_NOTIFIER=false", "NPM_CONFIG_PROVENANCE=false")
		output, err := cmd.CombinedOutput()
		if (err == nil) != wantOK {
			t.Fatalf("npm %v: %v\n%s", args, err, output)
		}
		return string(output)
	}
	npm(t, dir, true, "ping")
	if !strings.Contains(npm(t, dir, true, "whoami"), "e2e") {
		t.Fatal("wrong username")
	}
	t.Run("client cache", func(t *testing.T) {
		cache := t.TempDir()
		previous := registryReads.Load()
		for i := range 2 {
			work := t.TempDir()
			if err := os.WriteFile(filepath.Join(work, "package.json"), []byte(`{"name":"consumer","version":"1.0.0","private":true}`), 0600); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			output := npm(t, work, true, "install", names[0]+"@1.0.0", "--ignore-scripts", "--package-lock=false", "--cache="+cache, "--loglevel=http")
			current := registryReads.Load()
			t.Logf("install %d: %s, %d registry GETs", i+1, time.Since(start), current-previous)
			if i == 0 && current == previous {
				t.Fatal("empty client cache did not fetch from registry", output)
			}
			// npm revalidates explicitly requested packages even while fresh.
			if i == 1 && (current-previous != 1 || !strings.Contains(output, "(cache revalidated)") || !strings.Contains(output, "(cache hit)")) {
				t.Fatal("metadata body or tarball downloaded again", output)
			}
			previous = current
		}
	})
	for i, name := range names {
		t.Run(name, func(t *testing.T) {
			work := filepath.Join(dir, fmt.Sprint("consumer-", i))
			if err := os.MkdirAll(work, 0700); err != nil {
				t.Fatal(err)
			}
			_ = os.WriteFile(filepath.Join(work, "package.json"), []byte(`{"name":"consumer","version":"1.0.0","private":true}`), 0600)
			npm(t, work, true, "install", name+"@1.0.0", "--ignore-scripts", "--save-exact")
			index := filepath.Join(work, "node_modules", filepath.FromSlash(name), "index.js")
			content, _ := os.ReadFile(index)
			if !bytes.Contains(content, []byte("remote-1.0.0")) {
				t.Fatal("remote tarball not installed", string(content))
			}
			pub := filepath.Join(dir, fmt.Sprint("publish-", i))
			_ = os.MkdirAll(pub, 0700)
			publish := func(version string) {
				manifest, _ := json.Marshal(map[string]any{"name": name, "version": version, "main": "index.js"})
				_ = os.WriteFile(filepath.Join(pub, "package.json"), manifest, 0600)
				_ = os.WriteFile(filepath.Join(pub, "index.js"), []byte("module.exports = 'local-"+version+"';\n"), 0600)
				npm(t, pub, true, "publish", "--force", "--ignore-scripts", "--access=public")
			}
			publish("1.0.0")
			_ = os.RemoveAll(filepath.Join(work, "node_modules"))
			npm(t, work, true, "ci", "--ignore-scripts")
			content, _ = os.ReadFile(index)
			if !bytes.Contains(content, []byte("remote-1.0.0")) {
				t.Fatal("old lockfile URL changed contents", string(content))
			}
			publish("2.0.0")
			npm(t, pub, false, "publish", "--force", "--ignore-scripts", "--access=public")
			output := npm(t, work, true, "view", name, "versions", "--json")
			for _, v := range []string{"1.0.0", "2.0.0", "3.0.0"} {
				if !strings.Contains(output, v) {
					t.Fatal("version missing", output)
				}
			}
			if output := npm(t, work, true, "view", name, "version"); strings.TrimSpace(output) != "2.0.0" {
				t.Fatal("local tag not preferred", output)
			}
			npm(t, pub, true, "dist-tag", "add", name+"@1.0.0", "beta")
			npm(t, pub, true, "dist-tag", "rm", name, "beta")
			npm(t, work, true, "install", name+"@2.0.0", "--ignore-scripts", "--save-exact")
			content, _ = os.ReadFile(index)
			if !bytes.Contains(content, []byte("local-2.0.0")) {
				t.Fatal("local version not installed", string(content))
			}
			npm(t, work, true, "install", name+"@3.0.0", "--ignore-scripts", "--save-exact")
			offline.Store(true)
			_ = os.RemoveAll(filepath.Join(work, "node_modules"))
			npm(t, work, true, "ci", "--ignore-scripts")
			offline.Store(false)
		})
	}
	if downloads.Load() != 4 {
		t.Fatalf("expected each of four remote blobs once, got %d", downloads.Load())
	}
	if err := engine.Auth.Revoke(rec.ID); err != nil {
		t.Fatal(err)
	}
	npm(t, dir, false, "view", names[0], "version")
}
