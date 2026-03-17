package wabsignal

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

type GlobalOptions struct {
	ConfigPath string
	Project    string
	Output     string
}

func NewRootCmd() *cobra.Command {
	opts := &GlobalOptions{}
	rootGlobalOptions = opts
	cmd := &cobra.Command{
		Use:   "wabsignal",
		Short: "Hosted Grafana signal CLI for app debugging evidence",
		Long: strings.TrimSpace(`
wabsignal is a hosted Grafana CLI for collecting, bootstrapping, and querying
application telemetry during local development, QA, and debugging.

The CLI has two audiences:

- Humans/operators: run the machine bootstrap once with "wabsignal setup",
  manage projects, and decide which Grafana stack and access model this machine
  should use.
- Agents/tools: use project metadata, OTLP env output, health checks, and
  read/query commands after setup is complete.

Important boundary:

- "wabsignal setup" is human-only. It stores machine-level credentials and
  should not be invoked by a coding agent.
- After a human completes setup, agents can safely use commands such as
  "project env", "run", "doctor", "logs", "metrics", "traces", "query",
  and "correlate".
`),
		Example: strings.TrimSpace(`
  # Human-only machine bootstrap
  wabsignal setup

  # Create a project and emit OTLP env vars for an app
  wabsignal project create shop-api shop-api shop-worker
  wabsignal run start
  wabsignal project env shop-api --format dotenv

  # Inspect runtime evidence
  wabsignal --project shop-api doctor
  wabsignal --project shop-api logs '{} |= "error"' --since 30m
  wabsignal --project shop-api traces '{}'
  wabsignal --project shop-api correlate --trace-id 4f4a6e3f7b1f4c9c
`),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if exemptFromSetupCheck(cmd) {
				return nil
			}
			return requireSetup(opts)
		},
	}

	cmd.PersistentFlags().StringVar(&opts.ConfigPath, "config", "", "Path to config file (default: ~/.config/wabsignal/config.yaml)")
	cmd.PersistentFlags().StringVar(&opts.Project, "project", "", "Override current project")
	cmd.PersistentFlags().StringVarP(&opts.Output, "output", "o", "auto", "Output mode: auto|json|table|raw|csv")

	cmd.AddCommand(newSetupCmd(opts))
	cmd.AddCommand(newProjectCmd(opts))
	cmd.AddCommand(newRunCmd(opts))
	cmd.AddCommand(newDoctorCmd(opts))
	cmd.AddCommand(newQueryCmd(opts))
	cmd.AddCommand(newSignalCmd(opts, "logs"))
	cmd.AddCommand(newSignalCmd(opts, "metrics"))
	cmd.AddCommand(newSignalCmd(opts, "traces"))
	cmd.AddCommand(newCorrelateCmd(opts))
	cmd.AddCommand(newInitCmd())
	cmd.AddCommand(newVersionCmd())

	return cmd
}

func exemptFromSetupCheck(cmd *cobra.Command) bool {
	for current := cmd; current != nil; current = current.Parent() {
		switch current.Name() {
		case "setup", "version", "help", "init":
			return true
		}
	}
	return strings.TrimSpace(cmd.Name()) == ""
}
