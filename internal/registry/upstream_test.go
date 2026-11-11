package registry

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUpstreamProbe(t *testing.T) {
	for _, status := range []int{200, 204, 401, 403, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			e := engine(t)
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/registry/-/ping" {
					t.Error("wrong ping path", r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer private-secret" {
					t.Error("saved token was not used")
				}
				w.WriteHeader(status)
				if status != 204 {
					_, _ = w.Write([]byte("sensitive upstream response"))
				}
			}))
			defer remote.Close()
			if err := e.SaveUpstream(Upstream{ID: "probe", Name: "Probe", URL: remote.URL + "/registry", Token: "private-secret"}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(e.Store.DB.Path())
			if err != nil {
				t.Fatal(err)
			}
			result, err := e.TestUpstream(context.Background(), "probe")
			if err != nil {
				t.Fatal(err)
			}
			if result.StatusCode != status || result.OK != (status >= 200 && status < 300) || result.DurationMs < 0 {
				t.Fatal("incorrect result", result)
			}
			if strings.Contains(result.Message, "private-secret") || strings.Contains(result.Message, "sensitive") {
				t.Fatal("probe leaked credentials or response body", result)
			}
			after, err := os.ReadFile(e.Store.DB.Path())
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("probe changed registry state", err)
			}
		})
	}
}
func TestUpstreamProbeFailures(t *testing.T) {
	e := engine(t)
	if _, err := e.TestUpstream(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown upstream", err)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	if err := e.SaveUpstream(Upstream{ID: "slow", Name: "Slow", URL: remote.URL}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := e.TestUpstream(ctx, "slow")
	if err != nil || result.OK || result.StatusCode != 0 || result.Message != "Connection timed out." {
		t.Fatal("timeout not reported", result, err)
	}
	remote.Close()
	result, err = e.TestUpstream(context.Background(), "slow")
	if err != nil || result.OK || result.StatusCode != 0 || !strings.Contains(result.Message, "Connection failed") {
		t.Fatal("connection failure not reported", result, err)
	}
}
