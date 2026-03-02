package localstack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultProjectName = "grafquery-local"
	stateFileName      = "state.json"
	envFileName        = ".env"

	DefaultGrafanaURL           = "http://localhost:13000"
	DefaultGrafanaAdminUser     = "admin"
	DefaultGrafanaAdminPassword = "admin"

	DefaultOTLPGRPCEndpoint = "localhost:4317"
	DefaultOTLPHTTPEndpoint = "http://localhost:4318"

	LokiDatasourceUID       = "local-loki"
	PrometheusDatasourceUID = "local-prometheus"
	TempoDatasourceUID      = "local-tempo"
)

type State struct {
	GrafanaURL      string    `json:"grafana_url"`
	GrafanaUser     string    `json:"grafana_user"`
	GrafanaPassword string    `json:"grafana_password"`
	GrafanaToken    string    `json:"grafana_token"`
	ContextName     string    `json:"context_name"`
	CreatedAtUTC    time.Time `json:"created_at_utc"`
}

func DefaultRootDir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "grafquery", "local-stack"), nil
}

func ResolveRootDir(flagValue string) (string, error) {
	if strings.TrimSpace(flagValue) != "" {
		return filepath.Abs(flagValue)
	}
	return DefaultRootDir()
}

func EnsureScaffold(rootDir string) error {
	if strings.TrimSpace(rootDir) == "" {
		return errors.New("root directory cannot be empty")
	}

	paths := map[string]string{
		filepath.Join(rootDir, "docker-compose.yml"):                             dockerComposeYAML,
		filepath.Join(rootDir, "provisioning", "datasources", "datasources.yml"): grafanaDatasourcesYAML,
		filepath.Join(rootDir, "prometheus", "prometheus.yml"):                   prometheusYAML,
		filepath.Join(rootDir, "tempo", "tempo.yaml"):                            tempoYAML,
		filepath.Join(rootDir, "otel-collector", "config.yaml"):                  otelCollectorYAML,
	}

	for p, content := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := writeFileIfChanged(p, content, 0o644); err != nil {
			return err
		}
	}

	envPath := filepath.Join(rootDir, envFileName)
	if _, err := os.Stat(envPath); errors.Is(err, os.ErrNotExist) {
		if err := WriteGrafanaEnv(rootDir, DefaultGrafanaAdminUser, DefaultGrafanaAdminPassword); err != nil {
			return err
		}
	}

	return nil
}

func WriteGrafanaEnv(rootDir, username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		username = DefaultGrafanaAdminUser
	}
	if password == "" {
		password = DefaultGrafanaAdminPassword
	}
	if strings.Contains(username, "\n") || strings.Contains(username, "\r") {
		return errors.New("grafana username cannot contain newlines")
	}
	if strings.Contains(password, "\n") || strings.Contains(password, "\r") {
		return errors.New("grafana password cannot contain newlines")
	}

	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return err
	}

	content := fmt.Sprintf(
		"GRAFANA_ADMIN_USER=%s\nGRAFANA_ADMIN_PASSWORD=%s\n",
		dotenvValue(username),
		dotenvValue(password),
	)
	return os.WriteFile(filepath.Join(rootDir, envFileName), []byte(content), 0o600)
}

func LoadGrafanaCredentials(rootDir string) (string, string, error) {
	user := DefaultGrafanaAdminUser
	pass := DefaultGrafanaAdminPassword

	b, err := os.ReadFile(filepath.Join(rootDir, envFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return user, pass, nil
		}
		return "", "", err
	}

	lines := strings.Split(string(b), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := parseDotenvValue(parts[1])
		switch key {
		case "GRAFANA_ADMIN_USER":
			if strings.TrimSpace(value) != "" {
				user = strings.TrimSpace(value)
			}
		case "GRAFANA_ADMIN_PASSWORD":
			pass = value
		}
	}

	return user, pass, nil
}

func CheckDockerReady() error {
	if _, err := runCommand(exec.Command("docker", "version", "--format", "{{.Server.Version}}")); err != nil {
		return fmt.Errorf("docker daemon is not reachable: %w", err)
	}
	if _, err := detectComposeCommand(); err != nil {
		return err
	}
	return nil
}

func Up(rootDir string) error {
	if err := EnsureScaffold(rootDir); err != nil {
		return err
	}
	cmd, err := composeCommand(rootDir, "up", "-d", "--remove-orphans")
	if err != nil {
		return err
	}
	if _, err := runCommand(cmd); err != nil {
		return fmt.Errorf("docker compose up failed: %w", err)
	}
	return nil
}

func Down(rootDir string, removeVolumes bool) error {
	args := []string{"down", "--remove-orphans"}
	if removeVolumes {
		args = append(args, "-v")
	}
	cmd, err := composeCommand(rootDir, args...)
	if err != nil {
		return err
	}
	if _, err := runCommand(cmd); err != nil {
		return fmt.Errorf("docker compose down failed: %w", err)
	}
	return nil
}

func Status(rootDir string) (string, error) {
	cmd, err := composeCommand(rootDir, "ps")
	if err != nil {
		return "", err
	}
	out, err := runCommand(cmd)
	if err != nil {
		return "", fmt.Errorf("docker compose ps failed: %w", err)
	}
	return out, nil
}

func Purge(rootDir string) error {
	if _, err := os.Stat(rootDir); errors.Is(err, os.ErrNotExist) {
		return nil
	}

	var errs []string
	if err := Down(rootDir, true); err != nil {
		errs = append(errs, err.Error())
	}
	if err := os.RemoveAll(rootDir); err != nil {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func WaitForGrafana(ctx context.Context, baseURL string) error {
	healthURL := strings.TrimRight(baseURL, "/") + "/api/health"
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
			lastErr = fmt.Errorf("grafana returned %s", resp.Status)
		} else if err != nil {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("timed out waiting for grafana: %w", lastErr)
			}
			return fmt.Errorf("timed out waiting for grafana: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

func EnsureServiceToken(ctx context.Context, baseURL, username, password, serviceAccountName string) (string, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	accountID, err := ensureServiceAccount(ctx, baseURL, username, password, serviceAccountName)
	if err == nil {
		token, tokenErr := createServiceAccountToken(ctx, baseURL, username, password, accountID)
		if tokenErr == nil {
			return token, nil
		}
		err = tokenErr
	}

	legacyToken, legacyErr := createLegacyAPIKey(ctx, baseURL, username, password)
	if legacyErr != nil {
		if err != nil {
			return "", fmt.Errorf("service account token failed: %v; legacy api key failed: %w", err, legacyErr)
		}
		return "", legacyErr
	}
	return legacyToken, nil
}

func SaveState(rootDir string, state State) error {
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return err
	}
	statePath := filepath.Join(rootDir, stateFileName)
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(statePath, b, 0o600)
}

func LoadState(rootDir string) (*State, error) {
	statePath := filepath.Join(rootDir, stateFileName)
	b, err := os.ReadFile(statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

type composeCommandSpec struct {
	binary string
	prefix []string
}

func detectComposeCommand() (*composeCommandSpec, error) {
	if _, err := runCommand(exec.Command("docker", "compose", "version")); err == nil {
		return &composeCommandSpec{
			binary: "docker",
			prefix: []string{"compose"},
		}, nil
	}
	if _, err := runCommand(exec.Command("docker-compose", "version")); err == nil {
		return &composeCommandSpec{
			binary: "docker-compose",
			prefix: []string{},
		}, nil
	}
	return nil, errors.New("docker compose is not available (need `docker compose` or `docker-compose`)")
}

func composeCommand(rootDir string, args ...string) (*exec.Cmd, error) {
	spec, err := detectComposeCommand()
	if err != nil {
		return nil, err
	}
	composeFile := filepath.Join(rootDir, "docker-compose.yml")
	fullArgs := append([]string{}, spec.prefix...)
	fullArgs = append(fullArgs, "-p", defaultProjectName, "-f", composeFile)
	fullArgs = append(fullArgs, args...)
	cmd := exec.Command(spec.binary, fullArgs...)
	cmd.Dir = rootDir
	return cmd, nil
}

func runCommand(cmd *exec.Cmd) (string, error) {
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(out.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func writeFileIfChanged(path, content string, perm os.FileMode) error {
	if b, err := os.ReadFile(path); err == nil {
		if string(b) == content {
			return nil
		}
	}
	return os.WriteFile(path, []byte(content), perm)
}

func dotenvValue(v string) string {
	if v == "" {
		return "\"\""
	}
	escaped := strings.ReplaceAll(v, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func parseDotenvValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
			inner := v[1 : len(v)-1]
			inner = strings.ReplaceAll(inner, `\"`, `"`)
			inner = strings.ReplaceAll(inner, `\\`, `\`)
			return inner
		}
		if strings.HasPrefix(v, `'`) && strings.HasSuffix(v, `'`) {
			return v[1 : len(v)-1]
		}
	}
	return v
}

func ensureServiceAccount(ctx context.Context, baseURL, username, password, name string) (int, error) {
	payload := map[string]any{
		"name": name,
		"role": "Admin",
	}
	var createResp struct {
		ID int `json:"id"`
	}
	status, body, err := doGrafanaJSON(ctx, http.MethodPost, baseURL+"/api/serviceaccounts", username, password, payload, &createResp)
	if err != nil {
		return 0, err
	}
	if status == 200 || status == 201 {
		if createResp.ID <= 0 {
			return 0, errors.New("grafana did not return a service account id")
		}
		return createResp.ID, nil
	}

	if status == 400 || status == 409 {
		accountID, findErr := findServiceAccountID(ctx, baseURL, username, password, name)
		if findErr == nil {
			return accountID, nil
		}
		return 0, fmt.Errorf("service account create failed: %s", strings.TrimSpace(string(body)))
	}

	return 0, fmt.Errorf("service account create failed: %s", strings.TrimSpace(string(body)))
}

func findServiceAccountID(ctx context.Context, baseURL, username, password, name string) (int, error) {
	u := baseURL + "/api/serviceaccounts/search?query=" + url.QueryEscape(name)
	var searchResp struct {
		ServiceAccounts []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"serviceAccounts"`
	}
	status, body, err := doGrafanaJSON(ctx, http.MethodGet, u, username, password, nil, &searchResp)
	if err != nil {
		return 0, err
	}
	if status != 200 {
		return 0, fmt.Errorf("service account search failed: %s", strings.TrimSpace(string(body)))
	}
	for _, item := range searchResp.ServiceAccounts {
		if strings.EqualFold(strings.TrimSpace(item.Name), strings.TrimSpace(name)) && item.ID > 0 {
			return item.ID, nil
		}
	}
	return 0, fmt.Errorf("service account %q not found", name)
}

func createServiceAccountToken(ctx context.Context, baseURL, username, password string, accountID int) (string, error) {
	tokenName := fmt.Sprintf("grafquery-local-%d", time.Now().UTC().Unix())
	payload := map[string]any{
		"name": tokenName,
	}
	var tokenResp struct {
		Key string `json:"key"`
	}
	endpoint := fmt.Sprintf("%s/api/serviceaccounts/%d/tokens", baseURL, accountID)
	status, body, err := doGrafanaJSON(ctx, http.MethodPost, endpoint, username, password, payload, &tokenResp)
	if err != nil {
		return "", err
	}
	if status != 200 && status != 201 {
		return "", fmt.Errorf("service account token create failed: %s", strings.TrimSpace(string(body)))
	}
	if strings.TrimSpace(tokenResp.Key) == "" {
		return "", errors.New("grafana returned an empty service account token")
	}
	return strings.TrimSpace(tokenResp.Key), nil
}

func createLegacyAPIKey(ctx context.Context, baseURL, username, password string) (string, error) {
	payload := map[string]any{
		"name": fmt.Sprintf("grafquery-local-%d", time.Now().UTC().Unix()),
		"role": "Admin",
	}
	var out struct {
		Key string `json:"key"`
	}
	status, body, err := doGrafanaJSON(ctx, http.MethodPost, baseURL+"/api/auth/keys", username, password, payload, &out)
	if err != nil {
		return "", err
	}
	if status != 200 && status != 201 {
		return "", fmt.Errorf("legacy api key create failed: %s", strings.TrimSpace(string(body)))
	}
	if strings.TrimSpace(out.Key) == "" {
		return "", errors.New("grafana returned an empty api key")
	}
	return strings.TrimSpace(out.Key), nil
}

func doGrafanaJSON(ctx context.Context, method, endpoint, username, password string, payload any, out any) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if out != nil && len(respBody) > 0 {
		_ = json.Unmarshal(respBody, out)
	}
	return resp.StatusCode, respBody, nil
}

const dockerComposeYAML = `services:
  grafana:
    image: grafana/grafana-oss:11.1.4
    container_name: grafquery-grafana
    environment:
      GF_SECURITY_ADMIN_USER: ${GRAFANA_ADMIN_USER}
      GF_SECURITY_ADMIN_PASSWORD: ${GRAFANA_ADMIN_PASSWORD}
      GF_USERS_ALLOW_SIGN_UP: "false"
    ports:
      - "13000:3000"
    volumes:
      - grafana-data:/var/lib/grafana
      - ./provisioning:/etc/grafana/provisioning
    depends_on:
      - loki
      - prometheus
      - tempo

  loki:
    image: grafana/loki:3.1.1
    container_name: grafquery-loki
    command: [ "-config.file=/etc/loki/local-config.yaml" ]
    ports:
      - "13100:3100"

  prometheus:
    image: prom/prometheus:v2.54.1
    container_name: grafquery-prometheus
    command:
      - "--config.file=/etc/prometheus/prometheus.yml"
      - "--web.enable-remote-write-receiver"
    ports:
      - "13090:9090"
    volumes:
      - ./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml:ro

  tempo:
    image: grafana/tempo:2.5.0
    container_name: grafquery-tempo
    command: [ "-config.file=/etc/tempo/tempo.yaml" ]
    ports:
      - "13200:3200"
    volumes:
      - ./tempo/tempo.yaml:/etc/tempo/tempo.yaml:ro

  otel-collector:
    image: otel/opentelemetry-collector-contrib:0.107.0
    container_name: grafquery-otel-collector
    command: [ "--config=/etc/otelcol/config.yaml" ]
    depends_on:
      - loki
      - prometheus
      - tempo
    ports:
      - "4317:4317"
      - "4318:4318"
    volumes:
      - ./otel-collector/config.yaml:/etc/otelcol/config.yaml:ro

volumes:
  grafana-data:
`

const grafanaDatasourcesYAML = `apiVersion: 1
datasources:
  - name: Loki
    uid: local-loki
    type: loki
    access: proxy
    url: http://loki:3100
    editable: false

  - name: Prometheus
    uid: local-prometheus
    type: prometheus
    access: proxy
    url: http://prometheus:9090
    editable: false

  - name: Tempo
    uid: local-tempo
    type: tempo
    access: proxy
    url: http://tempo:3200
    editable: false
    jsonData:
      httpMethod: GET
      tracesToLogsV2:
        datasourceUid: local-loki
      serviceMap:
        datasourceUid: local-prometheus
`

const prometheusYAML = `global:
  scrape_interval: 15s
  evaluation_interval: 15s

scrape_configs:
  - job_name: prometheus
    static_configs:
      - targets: ["localhost:9090"]
`

const tempoYAML = `stream_over_http_enabled: true

server:
  http_listen_port: 3200

distributor:
  receivers:
    otlp:
      protocols:
        grpc:
        http:

ingester:
  max_block_duration: 5m

compactor:
  compaction:
    block_retention: 24h

storage:
  trace:
    backend: local
    wal:
      path: /tmp/tempo/wal
    local:
      path: /tmp/tempo/blocks
`

const otelCollectorYAML = `receivers:
  otlp:
    protocols:
      grpc:
      http:

processors:
  batch: {}

exporters:
  otlp/tempo:
    endpoint: tempo:4317
    tls:
      insecure: true
  prometheusremotewrite:
    endpoint: http://prometheus:9090/api/v1/write
  loki:
    endpoint: http://loki:3100/loki/api/v1/push

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlp/tempo]
    metrics:
      receivers: [otlp]
      processors: [batch]
      exporters: [prometheusremotewrite]
    logs:
      receivers: [otlp]
      processors: [batch]
      exporters: [loki]
`
