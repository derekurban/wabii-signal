package wabsignal

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/derekurban/wabii-signal/internal/cfg"
	"github.com/derekurban/wabii-signal/internal/grafana"
	"github.com/derekurban/wabii-signal/internal/output"
	"github.com/derekurban/wabii-signal/internal/secret"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"golang.org/x/term"
	"google.golang.org/protobuf/proto"
)

const setupHint = "run `wabsignal setup` first"

func resolveConfigPath(flag string) (string, error) {
	if strings.TrimSpace(flag) != "" {
		return flag, nil
	}
	return cfg.DefaultConfigPath()
}

func loadConfigFromFlags(opts *GlobalOptions) (*cfg.Config, string, error) {
	path, err := resolveConfigPath(opts.ConfigPath)
	if err != nil {
		return nil, "", err
	}
	c, _, err := cfg.LoadWithMigration(path)
	if err != nil {
		return nil, "", err
	}
	return c, path, nil
}

func requireSetup(opts *GlobalOptions) error {
	c, _, err := loadConfigFromFlags(opts)
	if err != nil {
		return err
	}
	if !c.SetupComplete() {
		return errors.New(setupHint)
	}
	if _, err := secret.GetQueryToken(); err != nil {
		if errors.Is(err, secret.ErrNotFound) {
			return errors.New(setupHint)
		}
		return fmt.Errorf("read token lookup failed: %w", err)
	}
	return nil
}

func requireExplicitReadProject(opts *GlobalOptions, commandName string) error {
	if strings.TrimSpace(opts.Project) != "" {
		return nil
	}
	return fmt.Errorf("%s requires --project <name>; wabii-signal no longer falls back to the mutable current project for read commands", commandName)
}

func buildClient(opts *GlobalOptions) (*grafana.Client, *cfg.Config, *cfg.Project, string, error) {
	c, _, err := loadConfigFromFlags(opts)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if !c.SetupComplete() {
		return nil, c, nil, "", errors.New(setupHint)
	}
	project, name, err := c.ResolveProject(opts.Project)
	if err != nil {
		return nil, c, nil, "", err
	}
	token, err := secret.GetQueryToken()
	if err != nil {
		if errors.Is(err, secret.ErrNotFound) {
			return nil, c, nil, "", errors.New(setupHint)
		}
		return nil, c, nil, "", fmt.Errorf("read token lookup failed: %w", err)
	}
	client := grafana.New(c.Setup.GrafanaAPIURL, token)
	return client, c, project, name, nil
}

func buildSetupClient(c *cfg.Config) (*grafana.Client, error) {
	if c == nil || !c.SetupComplete() {
		return nil, errors.New(setupHint)
	}
	token, err := secret.GetQueryToken()
	if err != nil {
		if errors.Is(err, secret.ErrNotFound) {
			return nil, errors.New(setupHint)
		}
		return nil, fmt.Errorf("read token lookup failed: %w", err)
	}
	return grafana.New(c.Setup.GrafanaAPIURL, token), nil
}

func discoverSignalSources(ctx context.Context, cl *grafana.Client) (map[string]string, []grafana.DataSource, error) {
	sources, err := cl.GetDataSources(ctx)
	if err != nil {
		return nil, nil, err
	}
	discovered := map[string]string{}
	for _, ds := range sources {
		switch strings.ToLower(strings.TrimSpace(ds.Type)) {
		case "loki":
			if discovered["logs"] == "" {
				discovered["logs"] = ds.UID
			}
		case "prometheus":
			if discovered["metrics"] == "" {
				discovered["metrics"] = ds.UID
			}
		case "tempo":
			if discovered["traces"] == "" {
				discovered["traces"] = ds.UID
			}
		}
	}
	return discovered, sources, nil
}

func resolveSignalSource(ctx context.Context, cl *grafana.Client, signal string, c *cfg.Config, project *cfg.Project) (*grafana.DataSource, error) {
	sources, err := cl.GetDataSources(ctx)
	if err != nil {
		return nil, err
	}

	if project != nil {
		if uid := strings.TrimSpace(project.Sources[signal]); uid != "" {
			if ds := grafana.SourceByUID(sources, uid); ds != nil {
				return ds, nil
			}
		}
	}
	if uid := strings.TrimSpace(c.Setup.Sources[signal]); uid != "" {
		if ds := grafana.SourceByUID(sources, uid); ds != nil {
			return ds, nil
		}
	}

	wantType := map[string]string{"logs": "loki", "metrics": "prometheus", "traces": "tempo"}[signal]
	for i := range sources {
		if strings.EqualFold(sources[i].Type, wantType) {
			return &sources[i], nil
		}
	}
	return nil, fmt.Errorf("no datasource mapped for signal %q", signal)
}

func renderByOutput(mode string, payload map[string]any, rows []map[string]any) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "json":
		return output.PrintJSON(rows)
	case "raw":
		return output.PrintRaw(payload)
	case "csv":
		return output.PrintCSV(rows, []string{"Time", "ts", "line", "value"})
	case "table", "auto", "":
		output.PrintTable(rows, []string{"Time", "ts", "line", "value", "service", "level", "message"})
		return nil
	default:
		return fmt.Errorf("unsupported output mode: %s", mode)
	}
}

func isJSONOutput(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "json", "raw":
		return true
	default:
		return false
	}
}

func parseRawJSONMap(s string) (map[string]any, error) {
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func printDataSources(sources []grafana.DataSource) {
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Type == sources[j].Type {
			return sources[i].Name < sources[j].Name
		}
		return sources[i].Type < sources[j].Type
	})
	rows := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		rows = append(rows, map[string]any{
			"uid":  source.UID,
			"type": source.Type,
			"name": source.Name,
			"url":  source.URL,
		})
	}
	output.PrintTable(rows, []string{"uid", "type", "name", "url"})
}

func promptValue(label string, secretValue bool) (string, error) {
	fmt.Fprintf(os.Stdout, "%s: ", label)
	if !secretValue {
		var value string
		if _, err := fmt.Fscanln(os.Stdin, &value); err != nil {
			return "", err
		}
		return strings.TrimSpace(value), nil
	}

	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		value, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stdout)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(value)), nil
	}

	var value string
	if _, err := fmt.Fscanln(os.Stdin, &value); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func promptOrValue(value, label string, secretValue, nonInteractive bool) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		return value, nil
	}
	if nonInteractive {
		return "", fmt.Errorf("%s is required in non-interactive mode", strings.ToLower(label))
	}
	return promptValue(label, secretValue)
}

func redactSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(not set)"
	}
	if len(value) <= 8 {
		return "***"
	}
	return value[:4] + "..." + value[len(value)-4:]
}

func buildOTLPHeaders(instanceID, token string) string {
	auth := base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(instanceID) + ":" + strings.TrimSpace(token)))
	return "Authorization=Basic%20" + auth
}

func buildOTLPAuthorizationValue(instanceID, token string) string {
	auth := base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(instanceID) + ":" + strings.TrimSpace(token)))
	return "Basic " + auth
}

func buildResourceAttributes(projectName string, project *cfg.Project) string {
	attributes := map[string]string{
		"service.name":               project.PrimaryService,
		"wabsignal.project":          projectName,
		"wabsignal.primary_service":  project.PrimaryService,
		"wabsignal.project_services": strings.Join(project.AllServices(), ","),
		"wabsignal_project":          projectName,
		"wabsignal_primary_service":  project.PrimaryService,
		"wabsignal_project_services": strings.Join(project.AllServices(), ","),
	}
	if project.CurrentRun != nil && strings.TrimSpace(project.CurrentRun.ID) != "" {
		attributes["wabsignal.run_id"] = project.CurrentRun.ID
		attributes["wabsignal_run_id"] = project.CurrentRun.ID
	}
	for key, value := range project.BootstrapAttributes {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		attributes[key] = value
	}

	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, escapeResourceAttributeValue(attributes[key])))
	}
	return strings.Join(parts, ",")
}

func escapeResourceAttributeValue(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `,`, `\,`, `=`, `\=`)
	return replacer.Replace(strings.TrimSpace(value))
}

func formatSetupSummary(mode string, c *cfg.Config) []map[string]any {
	return []map[string]any{
		{
			"mode":             mode,
			"grafana_api_url":  c.Setup.GrafanaAPIURL,
			"stack_name":       c.Setup.StackName,
			"otlp_endpoint":    c.Setup.OTLPEndpoint,
			"otlp_instance_id": c.Setup.OTLPInstanceID,
			"cloud_stack_id":   c.Setup.Cloud.StackID,
			"cloud_region":     c.Setup.Cloud.Region,
		},
	}
}

func timeoutContext(seconds int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
}

func mustJSONOutput(opts *GlobalOptions, payload any) bool {
	if !isJSONOutput(opts.Output) {
		return false
	}
	_ = output.PrintJSON(payload)
	return true
}

func generateRunID() (string, error) {
	suffix, err := randomHex(4)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("run-%s-%s", time.Now().UTC().Format("20060102T150405Z"), suffix), nil
}

func randomHex(byteCount int) (string, error) {
	if byteCount <= 0 {
		return "", errors.New("byte count must be positive")
	}
	buf := make([]byte, byteCount)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func otlpTracesURL(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(parsed.Path, "/v1/traces") {
		return parsed.String(), nil
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/traces"
	return parsed.String(), nil
}

type traceSmokeResult struct {
	TraceID      string `json:"trace_id"`
	SpanID       string `json:"span_id"`
	SmokeRunID   string `json:"smoke_run_id"`
	HTTPStatus   int    `json:"http_status"`
	VisibleInUI  bool   `json:"visible_in_ui"`
	Service      string `json:"service"`
	RequestURL   string `json:"request_url"`
	RequestScope string `json:"request_scope"`
}

func runOTLPTraceSmokeTest(ctx context.Context, config *cfg.Config, projectName string, project *cfg.Project, traceDS *grafana.DataSource, readClient *grafana.Client, waitForRead bool) (*traceSmokeResult, error) {
	if config == nil || project == nil {
		return nil, errors.New("config and project are required")
	}
	if strings.TrimSpace(project.WriteToken) == "" {
		return nil, errors.New("project write token is required")
	}
	traceID, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	spanID, err := randomHex(8)
	if err != nil {
		return nil, err
	}
	smokeRunID, err := generateRunID()
	if err != nil {
		return nil, err
	}

	body, err := buildTraceExportBody(projectName, project.PrimaryService, smokeRunID, traceID, spanID)
	if err != nil {
		return nil, err
	}
	requestURL, err := otlpTracesURL(config.Setup.OTLPEndpoint)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", buildOTLPAuthorizationValue(config.Setup.OTLPInstanceID, project.WriteToken))
	req.Header.Set("Content-Type", "application/x-protobuf")

	httpClient := &http.Client{Timeout: 20 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("otlp trace export failed: %w", err)
	}
	defer resp.Body.Close()

	result := &traceSmokeResult{
		TraceID:      traceID,
		SpanID:       spanID,
		SmokeRunID:   smokeRunID,
		HTTPStatus:   resp.StatusCode,
		Service:      project.PrimaryService,
		RequestURL:   requestURL,
		RequestScope: config.Setup.StackName,
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, fmt.Errorf("otlp trace export failed: %s", resp.Status)
	}
	if !waitForRead || traceDS == nil || readClient == nil {
		return result, nil
	}

	if err := waitForTraceVisibility(ctx, readClient, traceDS, traceID); err != nil {
		return result, err
	}
	result.VisibleInUI = true
	return result, nil
}

func buildTraceExportBody(projectName, serviceName, smokeRunID, traceID, spanID string) ([]byte, error) {
	now := uint64(time.Now().UTC().UnixNano())
	end := uint64(time.Now().UTC().Add(250 * time.Millisecond).UnixNano())
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: serviceName}}},
						{Key: "wabsignal_project", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: projectName}}},
						{Key: "wabsignal_run_id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: smokeRunID}}},
						{Key: "wabsignal_doctor", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: smokeRunID}}},
					},
				},
				ScopeSpans: []*tracepb.ScopeSpans{
					{
						Scope: &commonpb.InstrumentationScope{Name: "wabsignal.doctor"},
						Spans: []*tracepb.Span{
							{
								TraceId:           mustDecodeHex(traceID),
								SpanId:            mustDecodeHex(spanID),
								Name:              "wabsignal.doctor",
								StartTimeUnixNano: now,
								EndTimeUnixNano:   end,
								Attributes: []*commonpb.KeyValue{
									{Key: "wabsignal_doctor", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: smokeRunID}}},
								},
							},
						},
					},
				},
			},
		},
	}
	return proto.Marshal(req)
}

func mustDecodeHex(value string) []byte {
	buf, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	return buf
}

func waitForTraceVisibility(ctx context.Context, client *grafana.Client, traceDS *grafana.DataSource, traceID string) error {
	deadline := time.Now().Add(20 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}

	for {
		resp, err := client.Query(ctx, grafana.QueryRequest{
			From: "now-30m",
			To:   "now",
			Queries: []grafana.QueryPayload{
				{
					RefID:      "A",
					Datasource: map[string]any{"uid": traceDS.UID, "type": traceDS.Type},
					QueryType:  "traceId",
					Raw: map[string]any{
						"query": traceID,
						"limit": 20,
					},
				},
			},
		})
		if err == nil {
			rows, _ := grafana.FrameRows(resp)
			if len(rows) > 0 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("trace smoke test write succeeded but read-back failed: %w", err)
			}
			return errors.New("trace smoke test write succeeded but trace was not queryable before timeout")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
