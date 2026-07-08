package plugin

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultCPAUpdateService = "cli-proxy-api"
)

var (
	cpaUpdateNow       = time.Now
	cpaUpdateGetwd     = os.Getwd
	cpaUpdateLookupEnv = os.LookupEnv
	cpaUpdateRunShell  = runDetachedShell
	cpaUpdateStat      = os.Stat
)

type cpaUpdateRequest struct {
	LatestVersion string `json:"latest_version,omitempty"`
}

type cpaUpdateResponse struct {
	Accepted      bool   `json:"accepted"`
	Message       string `json:"message"`
	ComposeDir    string `json:"compose_dir,omitempty"`
	LogPath       string `json:"log_path,omitempty"`
	Service       string `json:"service,omitempty"`
	LatestVersion string `json:"latest_version,omitempty"`
}

func (a *App) handleCPAUpdate(req ManagementRequest, cfg Config) ManagementResponse {
	if strings.ToUpper(req.Method) != http.MethodPost {
		return errorJSON(http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
	if !isDockerDBPath(cfg.DBPath) {
		return errorJSON(http.StatusBadRequest, errors.New("当前部署不是 Docker，CPA 检查/更新仅支持 Docker"))
	}
	var body cpaUpdateRequest
	if len(strings.TrimSpace(string(req.Body))) > 0 {
		if err := decodeJSON(req.Body, &body); err != nil {
			return errorJSON(http.StatusBadRequest, err)
		}
	}
	resp, err := scheduleCPAUpdate(cfg, body)
	if err != nil {
		return errorJSON(http.StatusBadGateway, err)
	}
	return jsonManagement(http.StatusAccepted, resp)
}

func scheduleCPAUpdate(cfg Config, body cpaUpdateRequest) (cpaUpdateResponse, error) {
	customCommand := strings.TrimSpace(envValue("CPA_TOOLKIT_DOCKER_UPDATE_COMMAND"))
	workDir := ""
	service := strings.TrimSpace(envValue("CPA_TOOLKIT_DOCKER_SERVICE"))
	if service == "" {
		service = defaultCPAUpdateService
	}
	logPath := updateLogPath(cfg.DBPath)
	command := customCommand
	if command == "" {
		composeDir, err := discoverComposeDir(cfg.DBPath)
		if err != nil {
			return cpaUpdateResponse{}, err
		}
		workDir = composeDir
		command = fmt.Sprintf("docker compose pull %s && docker compose up -d %s", shellQuote(service), shellQuote(service))
	}
	if err := cpaUpdateRunShell(workDir, logPath, command); err != nil {
		return cpaUpdateResponse{}, err
	}
	target := strings.TrimSpace(body.LatestVersion)
	message := "CPA 更新已启动，服务会自动拉取最新镜像并重启。请等待 20-30 秒后刷新页面。"
	if target != "" {
		message = fmt.Sprintf("CPA 更新已启动，目标版本 %s。服务会自动重启，请等待 20-30 秒后刷新页面。", target)
	}
	return cpaUpdateResponse{
		Accepted:      true,
		Message:       message,
		ComposeDir:    workDir,
		LogPath:       logPath,
		Service:       service,
		LatestVersion: target,
	}, nil
}

func isDockerDBPath(dbPath string) bool {
	return strings.HasPrefix(strings.TrimSpace(dbPath), "/CLIProxyAPI/")
}

func discoverComposeDir(dbPath string) (string, error) {
	seen := map[string]struct{}{}
	var candidates []string
	if override := strings.TrimSpace(envValue("CPA_TOOLKIT_DOCKER_COMPOSE_DIR")); override != "" {
		candidates = append(candidates, override)
	}
	if cwd, err := cpaUpdateGetwd(); err == nil && strings.TrimSpace(cwd) != "" {
		candidates = append(candidates, cwd)
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(strings.TrimSpace(dbPath)), ".."))
	candidates = append(candidates, root, "/CLIProxyAPI")
	for _, candidate := range candidates {
		candidate = filepath.Clean(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		if hasComposeFile(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("未找到 docker-compose.yml 或 compose.yml，可通过环境变量 CPA_TOOLKIT_DOCKER_COMPOSE_DIR 指定 Compose 目录")
}

func hasComposeFile(dir string) bool {
	for _, name := range []string{"docker-compose.yml", "compose.yml"} {
		if _, err := cpaUpdateStat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

func updateLogPath(dbPath string) string {
	base := filepath.Dir(strings.TrimSpace(dbPath))
	if base == "." || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "cpa-toolkit-cpa-update.log")
}

func envValue(name string) string {
	if value, ok := cpaUpdateLookupEnv(name); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func runDetachedShell(workDir, logPath, command string) error {
	launcher := fmt.Sprintf(
		"nohup sh -lc %s >> %s 2>&1 < /dev/null &",
		shellQuote(command),
		shellQuote(logPath),
	)
	cmd := exec.Command("sh", "-lc", launcher)
	if strings.TrimSpace(workDir) != "" {
		cmd.Dir = workDir
	}
	return cmd.Run()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
