package registry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

type UpstreamTestResult struct {
	OK         bool   `json:"ok"`
	StatusCode int    `json:"statusCode"`
	DurationMs int64  `json:"durationMs"`
	Message    string `json:"message"`
}

// TestUpstream probes the saved registry without changing routes or cache.
func (e *Engine) TestUpstream(ctx context.Context, id string) (UpstreamTestResult, error) {
	var u Upstream
	if err := e.read("upstreams", id, &u); err != nil {
		return UpstreamTestResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	started := time.Now()
	result := UpstreamTestResult{}
	req, err := e.request(ctx, u, u.URL+"/-/ping")
	if err != nil {
		return result, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := e.Client.Do(req)
	result.DurationMs = time.Since(started).Milliseconds()
	if err != nil {
		var networkError net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
			result.Message = "Connection timed out."
		} else {
			result.Message = "Connection failed. Check the registry URL, network and TLS certificate."
		}
		return result, nil
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode
	result.OK = resp.StatusCode >= 200 && resp.StatusCode < 300
	switch {
	case result.OK:
		result.Message = fmt.Sprintf("Upstream reachable (HTTP %d).", resp.StatusCode)
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		result.Message = fmt.Sprintf("Authentication rejected (HTTP %d). Check the saved token.", resp.StatusCode)
	case resp.StatusCode == 404:
		result.Message = "Registry reachable, but npm ping is not supported (HTTP 404)."
	default:
		result.Message = fmt.Sprintf("Upstream returned HTTP %d.", resp.StatusCode)
	}
	return result, nil
}
