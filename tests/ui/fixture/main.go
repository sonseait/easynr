// Disposable live server for browser acceptance tests. Never opens ./data.
package main

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"easynr/internal/auth"
	"easynr/internal/httpapi"
	"easynr/internal/registry"
	"easynr/internal/state"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	dir, err := os.MkdirTemp("", "easynr-browser-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	s, err := state.Open(dir)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := auth.EnsureBootstrap(s, &bytes.Buffer{}); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("browser-test-password"), bcrypt.MinCost)
	if err != nil {
		return err
	}
	if err := s.DB.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("settings")).Put([]byte("admin_hash"), hash) }); err != nil {
		return err
	}
	cacheData := []byte("browser cached tarball")
	cacheSum := sha512.Sum512(cacheData)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/-/ping":
			_, _ = w.Write([]byte(`{}`))
		case "/browser-cache":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "browser-cache", "versions": map[string]any{"1.0.0": map[string]any{"name": "browser-cache", "version": "1.0.0", "dist": map[string]any{"tarball": "/cache.tgz", "integrity": "sha512-" + base64.StdEncoding.EncodeToString(cacheSum[:])}}}})
		case "/cache.tgz":
			_, _ = w.Write(cacheData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	e, err := registry.New(s, filepath.Join(dir, "registry"), "")
	if err != nil {
		return err
	}
	if err := e.SaveUpstream(registry.Upstream{ID: "fixture", Name: "Fixture", URL: remote.URL}); err != nil {
		return err
	}
	if err := e.SaveRoute(registry.Route{Scope: "*", Upstream: "fixture"}); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"name": "@demo/hello", "versions": map[string]any{"1.0.0": map[string]any{"name": "@demo/hello", "version": "1.0.0", "description": "Browser fixture", "dist": map[string]any{}}}, "dist-tags": map[string]string{"latest": "1.0.0"}, "_attachments": map[string]any{"hello.tgz": map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("browser fixture"))}}})
	if err := e.Publish("@demo/hello", body); err != nil {
		return err
	}
	handler, err := httpapi.New(auth.Service{Store: s}, e)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: "127.0.0.1:14873", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
