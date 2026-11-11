package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type startupLog struct {
	mu    sync.Mutex
	data  bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (l *startupLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.data.Write(p)
	if strings.Contains(l.data.String(), "EasyNR listening") {
		l.once.Do(func() { close(l.ready) })
	}
	return n, err
}
func (l *startupLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.data.String() }
func TestLifecycleAndOfflineDryRun(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PUBLIC_URL", "")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	listener.Close()
	t.Setenv("PORT", port)
	start := func() *startupLog {
		t.Helper()
		log := &startupLog{ready: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- run(ctx, nil, log) }()
		select {
		case <-log.ready:
		case err := <-done:
			t.Fatal("server failed", err)
		case <-time.After(5 * time.Second):
			t.Fatal("startup timeout")
		}
		t.Cleanup(cancel)
		resp, err := http.Get("http://127.0.0.1:" + port + "/healthz")
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatal(resp.StatusCode)
		}
		var ignored bytes.Buffer
		if err := run(context.Background(), []string{"gc-dry-run"}, &ignored); err == nil || !strings.Contains(err.Error(), "in use") {
			t.Fatal("maintenance did not respect server lock", err)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("shutdown timeout")
		}
		return log
	}
	first := start()
	if strings.Count(first.String(), "bootstrap_admin_password") != 1 {
		t.Fatal("missing bootstrap")
	}
	second := start()
	if strings.Contains(second.String(), "bootstrap_admin_password") {
		t.Fatal("bootstrap repeated")
	}
	db := filepath.Join("data", "metadata.db")
	before, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	var report bytes.Buffer
	if err := run(context.Background(), []string{"gc-dry-run"}, &report); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(db)
	if !bytes.Equal(before, after) {
		t.Fatal("offline dry-run modified metadata bytes")
	}
	var result struct {
		DryRun bool `json:"dryRun"`
	}
	if err := json.Unmarshal(report.Bytes(), &result); err != nil || !result.DryRun {
		t.Fatal("invalid GC report", err)
	}
	var reset bytes.Buffer
	if err := run(context.Background(), []string{"admin", "reset-password"}, &reset); err != nil || !strings.Contains(reset.String(), "reset_admin_password") {
		t.Fatal("reset failed", err)
	}
}
func TestMaintenanceRequiresExistingData(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PORT", "")
	t.Setenv("PUBLIC_URL", "")
	if err := run(context.Background(), []string{"gc"}, &bytes.Buffer{}); err == nil {
		t.Fatal("GC created fresh data")
	}
	if _, err := os.Stat("data"); !os.IsNotExist(err) {
		t.Fatal("maintenance created data directory")
	}
}
