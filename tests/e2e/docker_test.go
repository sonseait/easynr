package e2e

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Build the image separately as described in docs/build.md. All resources belong to this test.
func TestDockerLifecycle(t *testing.T) {
	if os.Getenv("EASYNR_DOCKER_E2E") != "1" {
		t.Skip("set EASYNR_DOCKER_E2E=1 after building easynr:local")
	}
	docker := func(wantOK bool, args ...string) string {
		t.Helper()
		cmd := exec.Command("docker", args...)
		data, err := cmd.CombinedOutput()
		if (err == nil) != wantOK {
			t.Fatalf("docker %v: %v\n%s", args, err, data)
		}
		return strings.TrimSpace(string(data))
	}
	docker(true, "info", "--format", "{{.ServerVersion}}")
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	id := "easynr-test-" + hex.EncodeToString(random)
	volume := id + "-data"
	image := os.Getenv("EASYNR_TEST_IMAGE")
	if image == "" {
		image = "easynr:local"
	}
	docker(true, "volume", "create", volume)
	defer func() { _ = exec.Command("docker", "volume", "rm", volume).Run() }()
	docker(true, "run", "-d", "--name", id, "-p", "127.0.0.1::4873", "-v", volume+":/data", image)
	defer func() { _ = exec.Command("docker", "rm", "-f", id).Run() }()
	addr := docker(true, "port", id, "4873/tcp")
	base := "http://" + strings.TrimSpace(addr)
	client := &http.Client{Timeout: 5 * time.Second}
	wait := func() {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			r, err := client.Get(base + "/healthz")
			if err == nil {
				r.Body.Close()
				if r.StatusCode == 200 {
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("container not ready")
	}
	wait()
	if user := docker(true, "inspect", "--format", "{{.Config.User}}", id); user != "65532:65532" {
		t.Fatal("unexpected runtime user", user)
	}
	logs := docker(true, "logs", id)
	password := ""
	for _, line := range strings.Split(logs, "\n") {
		if strings.HasPrefix(line, "bootstrap_admin_password password=") {
			password = strings.TrimPrefix(line, "bootstrap_admin_password password=")
		}
	}
	if password == "" {
		t.Fatal("missing bootstrap password")
	}
	jar, _ := cookiejar.New(nil)
	client.Jar = jar
	request := func(method, path string, body any, csrf string) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			reader = bytes.NewReader(data)
		}
		r, _ := http.NewRequest(method, base+path, reader)
		r.Header.Set("Origin", base)
		r.Header.Set("X-CSRF-Token", csrf)
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, data
	}
	code, data := request("POST", "/api/login", map[string]string{"password": password}, "")
	if code != 200 {
		t.Fatal("login failed", code)
	}
	var session map[string]string
	_ = json.Unmarshal(data, &session)
	code, data = request("POST", "/api/tokens", map[string]any{"name": "docker-ci", "grants": []map[string]string{{"package": "*", "permission": "write"}}}, session["csrfToken"])
	if code != 201 {
		t.Fatal("create token failed", code)
	}
	var token map[string]any
	_ = json.Unmarshal(data, &token)
	docker(false, "run", "--rm", "-v", volume+":/data", image, "gc-dry-run")
	docker(true, "stop", id)
	docker(true, "start", id)
	base = "http://" + strings.TrimSpace(docker(true, "port", id, "4873/tcp"))
	wait()
	if code, _ := request("GET", "/api/session", nil, ""); code != 200 {
		t.Fatal("session did not survive restart", code)
	}
	r, _ := http.NewRequest("GET", base+"/-/whoami", nil)
	r.Header.Set("Authorization", "Bearer "+token["secret"].(string))
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("token did not survive restart", resp.StatusCode)
	}
	if logs = docker(true, "logs", id); strings.Count(logs, "bootstrap_admin_password") != 1 {
		t.Fatal("bootstrap repeated after restart")
	}
	docker(true, "stop", id)
	report := docker(true, "run", "--rm", "-v", volume+":/data", image, "gc-dry-run")
	if !strings.Contains(report, `"dryRun":true`) {
		t.Fatal("GC dry run failed", report)
	}
	docker(true, "run", "--rm", "-v", volume+":/data", image, "gc")
	docker(true, "run", "--rm", "-v", volume+":/data", image, "admin", "reset-password")
}
