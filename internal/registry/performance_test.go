package registry

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"easynr/internal/auth"
	"easynr/internal/state"
)

func benchmarkDocument() Document {
	versions := Document{}
	for i := range 1000 {
		version := fmt.Sprintf("1.0.%d", i)
		versions[version] = Document{"name": "benchmark-package", "version": version,
			"description": strings.Repeat("package description ", 10), "readme": strings.Repeat("documentation ", 100),
			"dependencies": Document{"dependency": "^1.0.0"},
			"dist":         Document{"tarball": artifactPath(strings.Repeat("a", 64)), "integrity": "sha512-fixture"}}
	}
	return Document{"name": "benchmark-package", "versions": versions, "dist-tags": Document{"latest": "1.0.999"}}
}

func BenchmarkRenderMetadata(b *testing.B) {
	doc := benchmarkDocument()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = render(doc, "http://localhost:4873")
	}
}

func benchmarkEngine(b *testing.B) (*Engine, string) {
	b.Helper()
	dir := b.TempDir()
	s, err := state.Open(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	e, err := New(s, filepath.Join(dir, "registry"), "")
	if err != nil {
		b.Fatal(err)
	}
	_, token, err := e.Auth.CreateToken("benchmark", []auth.Grant{{Repository: "*", Permission: "read"}})
	if err != nil {
		b.Fatal(err)
	}
	doc := benchmarkDocument()
	for _, variant := range []string{"", "install:"} {
		cached := Cached{Upstream: "npmjs", Name: "benchmark-package", Document: doc, FetchedAt: time.Now()}
		if variant != "" {
			// Model the abbreviated document an npm upstream sends for installs.
			versions := Document{}
			for version, value := range object(doc["versions"]) {
				item := object(value)
				versions[version] = Document{"name": item["name"], "version": item["version"], "dependencies": item["dependencies"], "dist": item["dist"]}
			}
			cached.Document = Document{"name": doc["name"], "versions": versions, "dist-tags": doc["dist-tags"]}
		}
		if err := e.write("cache", "npmjs:"+variant+cached.Name, cached); err != nil {
			b.Fatal(err)
		}
	}
	return e, token
}

type discardResponse struct{ header http.Header }

func (w discardResponse) Header() http.Header         { return w.header }
func (w discardResponse) WriteHeader(int)             {}
func (w discardResponse) Write(p []byte) (int, error) { return io.Discard.Write(p) }

func BenchmarkCachedInstallMetadata(b *testing.B) {
	e, token := benchmarkEngine(b)
	r := httptest.NewRequest("GET", "/benchmark-package", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Accept", "application/vnd.npm.install-v1+json")
	w := discardResponse{header: http.Header{}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		e.ServeHTTP(w, r)
	}
}

func BenchmarkCacheStatistics(b *testing.B) {
	e, _ := benchmarkEngine(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := e.CacheStatistics(); err != nil {
			b.Fatal(err)
		}
	}
}
