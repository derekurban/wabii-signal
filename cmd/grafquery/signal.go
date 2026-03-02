package grafquery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derekurban/grafana-query/internal/grafana"
	"github.com/derekurban/grafana-query/internal/util"
	"github.com/spf13/cobra"
)

func newSignalCmd(opts *GlobalOptions, signal string) *cobra.Command {
	var since, from, to string
	var limit int
	var follow bool
	var noDefaults bool
	var watchEvery time.Duration
	var instant bool

	cmd := &cobra.Command{
		Use:   signal + " <query>",
		Short: fmt.Sprintf("Run %s queries through Grafana", signal),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if watchEvery > 0 {
				return runSignalWatch(opts, signal, args[0], since, from, to, limit, noDefaults, instant, watchEvery)
			}
			return runSignalOnce(opts, signal, args[0], since, from, to, limit, noDefaults, instant)
		},
	}

	if signal == "traces" {
		cmd.AddCommand(newTracesGetCmd(opts))
	}

	cmd.Flags().StringVar(&since, "since", "", "Lookback range (default from context or 1h)")
	cmd.Flags().StringVar(&from, "from", "", "From timestamp")
	cmd.Flags().StringVar(&to, "to", "", "To timestamp")
	cmd.Flags().IntVar(&limit, "limit", 0, "Limit result lines/rows")
	cmd.Flags().BoolVar(&follow, "follow", false, "Alias for --watch 3s")
	cmd.Flags().DurationVar(&watchEvery, "watch", 0, "Re-run query every interval (e.g. 10s)")
	cmd.Flags().BoolVar(&noDefaults, "no-defaults", false, "Disable alias expansion and default label injection")
	cmd.Flags().BoolVar(&instant, "instant", signal == "metrics", "Instant query mode")

	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		if follow && watchEvery <= 0 {
			watchEvery = 3 * time.Second
		}
	}
	return cmd
}

func runSignalOnce(opts *GlobalOptions, signal, expr, since, from, to string, limit int, noDefaults, instant bool) error {
	cl, c, ctxCfg, _, err := buildClient(opts)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	ds, err := resolveSignalSource(ctx, cl, signal, ctxCfg)
	if err != nil {
		return err
	}

	if !noDefaults {
		expr = maybeApplyAliasAndLabels(expr, signal, c, ctxCfg)
	}
	if since == "" {
		since = ctxCfg.Defaults.Since
	}
	if limit <= 0 {
		if ctxCfg.Defaults.Limit > 0 {
			limit = ctxCfg.Defaults.Limit
		} else {
			limit = 100
		}
	}
	if signal == "traces" && limit == 0 {
		limit = 20
	}

	f, t, err := util.ResolveGrafanaRange(since, from, to)
	if err != nil {
		return err
	}

	qType := "range"
	if instant || signal == "metrics" {
		qType = "instant"
	}
	payload := grafana.QueryPayload{
		RefID:      "A",
		Datasource: map[string]any{"uid": ds.UID, "type": ds.Type},
		Expr:       strings.TrimSpace(expr),
		QueryType:  qType,
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
		if d := strings.TrimSpace(ctxCfg.Defaults.Output); d != "" {
			mode = d
		} else {
			mode = "table"
		}
	}
	return renderByOutput(mode, resp, rows)
}

func runSignalWatch(opts *GlobalOptions, signal, expr, since, from, to string, limit int, noDefaults, instant bool, every time.Duration) error {
	if every <= 0 {
		every = 5 * time.Second
	}
	for {
		fmt.Print("\033[H\033[2J")
		fmt.Printf("[%s] %s — updated %s\n\n", signal, expr, time.Now().Format(time.RFC3339))
		if err := runSignalOnce(opts, signal, expr, since, from, to, limit, noDefaults, instant); err != nil {
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
	cl, _, ctxCfg, _, err := buildClient(opts)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	ds, err := resolveSignalSource(ctx, cl, "traces", ctxCfg)
	if err != nil {
		return err
	}

	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return fmt.Errorf("trace id is required")
	}
	if since == "" {
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
			"query": traceID,
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
		if d := strings.TrimSpace(ctxCfg.Defaults.Output); d != "" {
			mode = d
		} else {
			mode = "table"
		}
	}
	return renderByOutput(mode, resp, rows)
}
