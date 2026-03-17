package wabsignal

import (
	"fmt"
	"strings"
	"time"

	"github.com/derekurban/wabii-signal/internal/grafana"
	"github.com/derekurban/wabii-signal/internal/util"
	"github.com/spf13/cobra"
)

func newSignalCmd(opts *GlobalOptions, signal string) *cobra.Command {
	var since, from, to string
	var limit int
	var follow bool
	var watchEvery time.Duration
	var instant bool
	var noProjectScope bool

	cmd := &cobra.Command{
		Use:     signal + " <query>",
		Short:   fmt.Sprintf("Run %s queries through Grafana HTTP API", signal),
		Long:    signalLongHelp(signal),
		Example: signalExampleHelp(signal),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if watchEvery > 0 {
				return runSignalWatch(opts, signal, args[0], since, from, to, limit, instant, watchEvery, noProjectScope)
			}
			return runSignalOnce(opts, signal, args[0], since, from, to, limit, instant, noProjectScope)
		},
	}

	if signal == "traces" {
		cmd.AddCommand(newTracesGetCmd(opts))
	}

	cmd.Flags().StringVar(&since, "since", "", "Lookback range (default from project or 1h)")
	cmd.Flags().StringVar(&from, "from", "", "From timestamp")
	cmd.Flags().StringVar(&to, "to", "", "To timestamp")
	cmd.Flags().IntVar(&limit, "limit", 0, "Limit result lines or rows")
	cmd.Flags().BoolVar(&follow, "follow", false, "Alias for --watch 3s")
	cmd.Flags().DurationVar(&watchEvery, "watch", 0, "Re-run query every interval (for example 10s)")
	cmd.Flags().BoolVar(&instant, "instant", signal == "metrics", "Instant query mode")
	cmd.Flags().BoolVar(&noProjectScope, "no-project-scope", false, "Disable automatic project service scoping")
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		if follow && watchEvery <= 0 {
			watchEvery = 3 * time.Second
		}
	}
	return cmd
}

func runSignalOnce(opts *GlobalOptions, signal, expr, since, from, to string, limit int, instant, noProjectScope bool) error {
	if err := requireExplicitReadProject(opts, signal); err != nil {
		return err
	}
	cl, config, project, projectName, err := buildClient(opts)
	if err != nil {
		return err
	}
	ctx, cancel := timeoutContext(35)
	defer cancel()

	ds, err := resolveSignalSource(ctx, cl, signal, config, project)
	if err != nil {
		return err
	}

	expr = applyProjectScope(signal, expr, project, noProjectScope)
	if strings.TrimSpace(since) == "" {
		since = project.Defaults.Since
	}
	if limit <= 0 {
		limit = project.Defaults.Limit
	}
	if limit <= 0 {
		limit = 100
	}

	f, t, err := util.ResolveGrafanaRange(since, from, to)
	if err != nil {
		return err
	}

	queryType := "range"
	if instant || signal == "metrics" {
		queryType = "instant"
	}
	payload := grafana.QueryPayload{
		RefID:      "A",
		Datasource: map[string]any{"uid": ds.UID, "type": ds.Type},
		Expr:       strings.TrimSpace(expr),
		QueryType:  queryType,
		MaxLines:   limit,
		Instant:    instant,
	}

	resp, err := cl.Query(ctx, grafana.QueryRequest{From: f, To: t, Queries: []grafana.QueryPayload{payload}})
	if err != nil {
		return err
	}
	rows, _ := grafana.FrameRows(resp)
	mode := opts.Output
	if mode == "auto" {
		mode = project.Defaults.Output
		if strings.TrimSpace(mode) == "" || mode == "auto" {
			mode = "table"
		}
	}
	if len(rows) == 0 && !noProjectScope && project != nil && !isJSONOutput(mode) {
		fmt.Printf("No rows for current scope: project=%s services=%s\n", projectName, strings.Join(project.AllServices(), ","))
		fmt.Printf("Try `wabsignal --project %s %s %q --since %s`, `wabsignal project use %s`, or `--no-project-scope`.\n\n", projectName, signal, argsafeExpr(expr), sinceOrDefault(since), projectName)
	}
	return renderByOutput(mode, resp, rows)
}

func sinceOrDefault(since string) string {
	if strings.TrimSpace(since) == "" {
		return "1h"
	}
	return strings.TrimSpace(since)
}

func argsafeExpr(expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "{}"
	}
	return expr
}

func runSignalWatch(opts *GlobalOptions, signal, expr, since, from, to string, limit int, instant bool, every time.Duration, noProjectScope bool) error {
	if every <= 0 {
		every = 5 * time.Second
	}
	for {
		fmt.Print("\033[H\033[2J")
		fmt.Printf("[%s] %s - updated %s\n\n", signal, expr, time.Now().Format(time.RFC3339))
		if err := runSignalOnce(opts, signal, expr, since, from, to, limit, instant, noProjectScope); err != nil {
			fmt.Println("error:", err)
		}
		time.Sleep(every)
	}
}

func newTracesGetCmd(opts *GlobalOptions) *cobra.Command {
	var since string
	var limit int
	cmd := &cobra.Command{
		Use:   "get <trace-id>",
		Short: "Fetch a specific trace by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTraceGetByID(opts, args[0], since, "", "", limit)
		},
	}
	cmd.Flags().StringVar(&since, "since", "24h", "Lookback range")
	cmd.Flags().IntVar(&limit, "limit", 50, "Result limit")
	return cmd
}

func runTraceGetByID(opts *GlobalOptions, traceID, since, from, to string, limit int) error {
	if err := requireExplicitReadProject(opts, "traces"); err != nil {
		return err
	}
	cl, config, project, _, err := buildClient(opts)
	if err != nil {
		return err
	}
	ctx, cancel := timeoutContext(35)
	defer cancel()

	ds, err := resolveSignalSource(ctx, cl, "traces", config, project)
	if err != nil {
		return err
	}
	if strings.TrimSpace(since) == "" {
		since = "24h"
	}
	if limit <= 0 {
		limit = 50
	}

	f, t, err := util.ResolveGrafanaRange(since, from, to)
	if err != nil {
		return err
	}

	payload := grafana.QueryPayload{
		RefID:      "A",
		Datasource: map[string]any{"uid": ds.UID, "type": ds.Type},
		QueryType:  "traceId",
		Raw: map[string]any{
			"query": strings.TrimSpace(traceID),
			"limit": limit,
		},
	}
	resp, err := cl.Query(ctx, grafana.QueryRequest{From: f, To: t, Queries: []grafana.QueryPayload{payload}})
	if err != nil {
		return err
	}
	rows, _ := grafana.FrameRows(resp)
	mode := opts.Output
	if mode == "auto" {
		mode = project.Defaults.Output
		if strings.TrimSpace(mode) == "" || mode == "auto" {
			mode = "table"
		}
	}
	return renderByOutput(mode, resp, rows)
}

func signalLongHelp(signal string) string {
	switch signal {
	case "logs":
		return strings.TrimSpace(`
Run LogQL-style log queries through the configured Grafana logs datasource.

Read commands require an explicit --project to avoid silently reusing a mutable
current project from another session or agent.

Wabii-signal injects project service scope into the query. Use
--no-project-scope only when you intentionally want to inspect the broader
stack. If you need a specific run, filter for the run ID explicitly.
`)
	case "metrics":
		return strings.TrimSpace(`
Run metric queries through the configured Grafana metrics datasource.

Metrics require an explicit --project and default to instant mode because most
debugging use cases want the current answer rather than a full chart. You can
still change the range and watch the query repeatedly during live debugging.
`)
	case "traces":
		return strings.TrimSpace(`
Run trace queries through the configured Grafana traces datasource.

Like the other signal commands, traces require an explicit --project and are
automatically scoped by project service. Use "traces get" to retrieve a
specific trace by ID. If you need a specific run, filter for the run ID
explicitly.
`)
	default:
		return ""
	}
}

func signalExampleHelp(signal string) string {
	switch signal {
	case "logs":
		return strings.TrimSpace(`
  wabsignal --project shop-api logs '{} |= "error"' --since 30m
  wabsignal --project shop-api logs '{service_name="shop-api"} |= "timeout"' --no-project-scope
  wabsignal --project shop-api logs '{} |= "panic"' --watch 5s
`)
	case "metrics":
		return strings.TrimSpace(`
  wabsignal --project shop-api metrics 'sum(rate(http_server_duration_seconds_count[5m]))'
  wabsignal --project shop-api metrics 'histogram_quantile(0.95, sum(rate(http_server_duration_seconds_bucket[5m])) by (le, service_name))'
  wabsignal --project shop-api metrics 'up' --watch 10s
`)
	case "traces":
		return strings.TrimSpace(`
  wabsignal --project shop-api traces '{}'
  wabsignal --project shop-api traces '{ resource.service.name = "shop-api" }' --no-project-scope
  wabsignal --project shop-api traces get 4f4a6e3f7b1f4c9c --since 24h
`)
	default:
		return ""
	}
}
