package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandleCPAUpdateSchedulesDetachedDockerUpdate(t *testing.T) {
	app, _ := newTestApp(t)
	cfg := Config{Enabled: true, DBPath: "/CLIProxyAPI/plugins/cpa-toolkit.db"}

	oldGetwd := cpaUpdateGetwd
	oldLookupEnv := cpaUpdateLookupEnv
	oldNow := cpaUpdateNow
	oldRunShell := cpaUpdateRunShell
	oldStat := cpaUpdateStat
	t.Cleanup(func() {
		cpaUpdateGetwd = oldGetwd
		cpaUpdateLookupEnv = oldLookupEnv
		cpaUpdateNow = oldNow
		cpaUpdateRunShell = oldRunShell
		cpaUpdateStat = oldStat
	})

	cpaUpdateGetwd = func() (string, error) { return "/workspace", nil }
	cpaUpdateLookupEnv = func(name string) (string, bool) { return "", false }
	cpaUpdateNow = func() time.Time { return time.Unix(12345, 0) }
	cpaUpdateStat = func(name string) (os.FileInfo, error) {
		if filepath.Clean(name) == filepath.Clean("/workspace/docker-compose.yml") {
			return nil, nil
		}
		return nil, os.ErrNotExist
	}

	var gotDir, gotLog, gotCommand string
	cpaUpdateRunShell = func(dir, logPath, command string) error {
		gotDir = dir
		gotLog = logPath
		gotCommand = command
		return nil
	}

	req := ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-toolkit/cpa/update",
		Body:   []byte(`{"latest_version":"v7.2.99"}`),
		Query:  url.Values{},
	}
	resp := app.handleCPAUpdate(req, cfg)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", resp.StatusCode, string(resp.Body))
	}
	if filepath.Clean(gotDir) != filepath.Clean("/workspace") {
		t.Fatalf("run dir = %q, want /workspace", gotDir)
	}
	if filepath.Clean(gotLog) != filepath.Clean("/CLIProxyAPI/plugins/cpa-toolkit-cpa-update.log") {
		t.Fatalf("log path = %q, want plugin log path", gotLog)
	}
	for _, want := range []string{
		"docker run -d --rm",
		"--name 'cpa-toolkit-updater-12345'",
		"-v '/workspace:/workspace:ro'",
		"-v '/workspace/plugins:/CLIProxyAPI/plugins'",
		"-w '/workspace'",
		"'docker:29-cli'",
		"docker compose pull",
		"cli-proxy-api",
		"docker compose up -d --remove-orphans",
		"/CLIProxyAPI/plugins/cpa-toolkit-cpa-update.log",
	} {
		if !strings.Contains(gotCommand, want) {
			t.Fatalf("command = %q, want to contain %q", gotCommand, want)
		}
	}
	var body cpaUpdateResponse
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("Unmarshal response error = %v", err)
	}
	if !body.Accepted || filepath.Clean(body.ComposeDir) != filepath.Clean("/workspace") || body.Service != "cli-proxy-api" || body.LatestVersion != "v7.2.99" {
		t.Fatalf("response = %#v", body)
	}
}

func TestHandleCPAUpdateRejectsNonDockerConfig(t *testing.T) {
	app, _ := newTestApp(t)
	resp := app.handleCPAUpdate(ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-toolkit/cpa/update",
	}, Config{Enabled: true, DBPath: "/opt/cli-proxy-api/plugins/cpa-toolkit.db"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", resp.StatusCode, string(resp.Body))
	}
	if !strings.Contains(string(resp.Body), "仅支持 Docker") {
		t.Fatalf("body = %s, want docker-only message", string(resp.Body))
	}
}
