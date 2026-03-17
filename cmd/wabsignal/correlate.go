package wabsignal

import (
	"fmt"
	"strings"

	"github.com/derekurban/wabii-signal/internal/cfg"
	"github.com/derekurban/wabii-signal/internal/grafana"
	"github.com/derekurban/wabii-signal/internal/output"
	"github.com/derekurban/wabii-signal/internal/util"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

func newCorrelateCmd(opts *GlobalOptions) *cobra.Command {
	var traceID string
	var service string
	var since, from, to string
	var limit int
	var noProjectScope bool

	cmd := &cobra.Command{
		Use:   "correlate",
		Short: "Cross-signal correlation for a trace or project service",
		Long: strings.TrimSpace(`
Run a focused multi-signal investigation.

Correlate has two main modes:

- trace mode: fetch a specific trace, nearby matching logs, and a small metrics
  presence query
- service mode: fetch logs, traces, and metrics for one service or the current
  project's primary service

This is intended to be the quick "what happened across the signals?" command
for both humans and agents.

Correlate requires an explicit --project so the query scope is deterministic.
`),
		Example: strings.TrimSpace(`
  wabsignal --project shop-api correlate --trace-id 4f4a6e3f7b1f4c9c
  wabsignal --project shop-api correlate --service shop-api --since 15m
  wabsignal --project shop-api correlate --since 30m
  wabsignal --project shop-api correlate --trace-id 4f4a6e3f7b1f4c9c --output json
`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireExplicitReadProject(opts, "correlate"); err != nil {
				return err
			}
			if strings.TrimSpace(traceID) == "" && strings.TrimSpace(service) == "" {
				if noProjectScope {
					return fmt.Errorf("set --trace-id or --service")
				}
				return correlateByService(opts, "", since, from, to, limit, false)
			}
			if strings.TrimSpace(traceID) != "" {
				return correlateByTrace(opts, traceID, since, from, to, limit, noProjectScope)
			}
			return correlateByService(opts, service, since, from, to, limit, noProjectScope)
		},
	}
	cmd.Flags().StringVar(&traceID, "trace-id", "", "Trace ID to correlate")
	cmd.Flags().StringVar(&service, "service", "", "Service to correlate (defaults to current project primary service)")
	cmd.Flags().StringVar(&since, "since", "30m", "Lookback range")
	cmd.Flags().StringVar(&from, "from", "", "From timestamp")
	cmd.Flags().StringVar(&to, "to", "", "To timestamp")
	cmd.Flags().IntVar(&limit, "limit", 50, "Result limit")
	cmd.Flags().BoolVar(&noProjectScope, "no-project-scope", false, "Disable automatic project service scoping")
	return cmd
}

func correlateByTrace(opts *GlobalOptions, traceID, since, from, to string, limit int, noProjectScope bool) error {
	cl, config, project, projectName, err := buildClient(opts)
	if err != nil {
		return err
	}
	ctx, cancel := timeoutContext(45)
	defer cancel()

	f, t, err := util.ResolveGrafanaRange(since, from, to)
	if err != nil {
		return err
	}

	traceDS, err := resolveSignalSource(ctx, cl, "traces", config, project)
	if err != nil {
		return err
	}
	logsDS, err := resolveSignalSource(ctx, cl, "logs", config, project)
	if err != nil {
		return err
	}
	metricsDS, err := resolveSignalSource(ctx, cl, "metrics", config, project)
	if err != nil {
		return err
	}

	metricsProject := scopedProject(project, "", noProjectScope)
	metricsExpr := `count({__name__=~".+"})`
	if metricsProject != nil {
		metricsExpr = buildMetricsPresenceExpr(metricsProject)
	}

	var traceResp, logsResp, metricsResp map[string]any
	group, groupCtx := errgroup.WithContext(ctx)

	group.Go(func() error {
		payload := grafana.QueryPayload{
			RefID:      "A",
			Datasource: map[string]any{"uid": traceDS.UID, "type": traceDS.Type},
			QueryType:  "traceId",
			Raw: map[string]any{
				"query": strings.TrimSpace(traceID),
				"limit": limit,
			},
		}
		resp, err := cl.Query(groupCtx, grafana.QueryRequest{From: f, To: t, Queries: []grafana.QueryPayload{payload}})
		if err == nil {
			traceResp = resp
		}
		return err
	})
	group.Go(func() error {
		expr := applyProjectScope("logs", fmt.Sprintf(`{} |= "%s"`, strings.TrimSpace(traceID)), project, noProjectScope)
		payload := grafana.QueryPayload{
			RefID:      "B",
			Datasource: map[string]any{"uid": logsDS.UID, "type": logsDS.Type},
			Expr:       expr,
			QueryType:  "range",
			MaxLines:   limit,
		}
		resp, err := cl.Query(groupCtx, grafana.QueryRequest{From: f, To: t, Queries: []grafana.QueryPayload{payload}})
		if err == nil {
			logsResp = resp
		}
		return err
	})
	group.Go(func() error {
		payload := grafana.QueryPayload{
			RefID:      "C",
			Datasource: map[string]any{"uid": metricsDS.UID, "type": metricsDS.Type},
			Expr:       metricsExpr,
			QueryType:  "instant",
			Instant:    true,
		}
		resp, err := cl.Query(groupCtx, grafana.QueryRequest{From: f, To: t, Queries: []grafana.QueryPayload{payload}})
		if err == nil {
			metricsResp = resp
		}
		return err
	})

	if err := group.Wait(); err != nil {
		return err
	}

	traceRows, _ := grafana.FrameRows(traceResp)
	logRows, _ := grafana.FrameRows(logsResp)
	metricRows, _ := grafana.FrameRows(metricsResp)

	summary := map[string]any{
		"trace_id":      traceID,
		"project":       projectName,
		"time_range":    map[string]string{"from": f, "to": t},
		"trace_rows":    len(traceRows),
		"log_rows":      len(logRows),
		"metric_points": len(metricRows),
		"trace":         traceRows,
		"logs":          logRows,
		"metrics":       metricRows,
	}
	if opts.Output == "json" || opts.Output == "raw" {
		return output.PrintJSON(summary)
	}

	fmt.Printf("Trace correlate: %s\n", traceID)
	fmt.Printf("Trace rows: %d | Log rows: %d | Metric points: %d\n\n", len(traceRows), len(logRows), len(metricRows))
	fmt.Println("Logs:")
	output.PrintTable(logRows, []string{"Time", "ts", "line", "message", "service_name", "level"})
	fmt.Println()
	fmt.Println("Metrics:")
	output.PrintTable(metricRows, []string{"Time", "value", "service_name"})
	return nil
}

func correlateByService(opts *GlobalOptions, service, since, from, to string, limit int, noProjectScope bool) error {
	cl, config, project, projectName, err := buildClient(opts)
	if err != nil {
		return err
	}
	project = scopedProject(project, service, noProjectScope)
	if project == nil {
		return fmt.Errorf("set --service or select a current project")
	}

	ctx, cancel := timeoutContext(45)
	defer cancel()
	f, t, err := util.ResolveGrafanaRange(since, from, to)
	if err != nil {
		return err
	}

	logsDS, err := resolveSignalSource(ctx, cl, "logs", config, project)
	if err != nil {
		return err
	}
	metricsDS, err := resolveSignalSource(ctx, cl, "metrics", config, project)
	if err != nil {
		return err
	}
	tracesDS, err := resolveSignalSource(ctx, cl, "traces", config, project)
	if err != nil {
		return err
	}

	logsExpr := applyProjectScope("logs", "{}", project, false)
	tracesExpr := applyProjectScope("traces", "{}", project, false)
	metricsExpr := buildMetricsPresenceExpr(project)

	queries := []struct {
		name string
		ds   *grafana.DataSource
		expr string
		qt   string
		inst bool
	}{
		{"logs", logsDS, logsExpr, "range", false},
		{"metrics", metricsDS, metricsExpr, "instant", true},
		{"traces", tracesDS, tracesExpr, "range", false},
	}

	results := map[string][]map[string]any{}
	group, groupCtx := errgroup.WithContext(ctx)
	for _, query := range queries {
		query := query
		group.Go(func() error {
			payload := grafana.QueryPayload{
				RefID:      strings.ToUpper(query.name[:1]),
				Datasource: map[string]any{"uid": query.ds.UID, "type": query.ds.Type},
				Expr:       query.expr,
				QueryType:  query.qt,
				MaxLines:   limit,
				Instant:    query.inst,
			}
			resp, err := cl.Query(groupCtx, grafana.QueryRequest{From: f, To: t, Queries: []grafana.QueryPayload{payload}})
			if err != nil {
				return err
			}
			rows, _ := grafana.FrameRows(resp)
			results[query.name] = rows
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}

	summary := map[string]any{
		"project":    projectName,
		"service":    project.PrimaryService,
		"time_range": map[string]string{"from": f, "to": t},
		"logs":       results["logs"],
		"metrics":    results["metrics"],
		"traces":     results["traces"],
	}
	if opts.Output == "json" || opts.Output == "raw" {
		return output.PrintJSON(summary)
	}

	fmt.Printf("Service correlate: %s\n", project.PrimaryService)
	fmt.Printf("Logs: %d | Metrics: %d | Traces: %d\n\n", len(results["logs"]), len(results["metrics"]), len(results["traces"]))
	fmt.Println("Logs:")
	output.PrintTable(results["logs"], []string{"Time", "ts", "line", "message", "level", "service_name"})
	fmt.Println()
	fmt.Println("Metrics:")
	output.PrintTable(results["metrics"], []string{"Time", "service_name", "value"})
	fmt.Println()
	fmt.Println("Traces:")
	output.PrintTable(results["traces"], []string{"traceID", "trace_id", "duration", "service.name", "resource.service.name"})
	return nil
}

func scopedProject(project *cfg.Project, service string, noProjectScope bool) *cfg.Project {
	service = strings.TrimSpace(service)
	if project == nil && service == "" {
		return nil
	}
	if noProjectScope && service == "" {
		return nil
	}
	if project == nil {
		synthetic := &cfg.Project{PrimaryService: service}
		synthetic.EnsureDefaults()
		return synthetic
	}
	if service == "" {
		if noProjectScope {
			return nil
		}
		return project
	}
	copy := *project
	copy.PrimaryService = service
	copy.Services = nil
	copy.EnsureDefaults()
	return &copy
}

func buildMetricsPresenceExpr(project *cfg.Project) string {
	op, value := matcherForServices(project.AllServices())
	label := project.QueryScope.MetricServiceLabel
	return fmt.Sprintf(`count by (%s) ({%s%s"%s"})`, label, label, op, escapeQueryValue(value))
}
