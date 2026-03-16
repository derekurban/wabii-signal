package wabsignal

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/derekurban/wabii-signal/internal/cfg"
	"github.com/derekurban/wabii-signal/internal/cloudapi"
	"github.com/derekurban/wabii-signal/internal/grafana"
	"github.com/derekurban/wabii-signal/internal/output"
	"github.com/derekurban/wabii-signal/internal/secret"
	"github.com/spf13/cobra"
)

var managedWriteScopes = []string{
	"logs:write",
	"metrics:write",
	"traces:write",
}

func newProjectCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage projects and OTLP bootstrap state",
		Long: strings.TrimSpace(`
Manage project-level telemetry identity and write-token state.

A project is wabii-signal's unit of scoping. It captures:

- a primary service name used for OTLP bootstrap and default read scope
- optional extra services that should be included in read-side queries
- the datasource UID mapping for logs, metrics, and traces
- query-scope label/attribute overrides
- the per-project write token used when generating OTLP headers

Humans typically create and curate projects. Agents usually consume project
state via "project env", "run", and the read/query commands.
`),
		Example: strings.TrimSpace(`
  # Create a project in restrictive mode with a manual write token
  wabsignal project create shop-api shop-api --write-token "$GRAFANA_WRITE_TOKEN"

  # Create a project with extra services included in read scope
  wabsignal project create storefront shop-api shop-web shop-worker

  # See or switch the active project
  wabsignal project list
  wabsignal project use storefront
  wabsignal project show

  # Emit OTLP environment for the current project
  wabsignal project env --format dotenv
`),
	}

	var createWriteToken string
	var createNonInteractive bool
	createCmd := &cobra.Command{
		Use:   "create <project-name> <primary-service> [extra-services...]",
		Short: "Create a project and attach its write token",
		Long: strings.TrimSpace(`
Create a project and validate that its write path works end to end.

In restrictive mode, you provide the project write token manually.
In full-access mode, wabii-signal creates a managed write token and stores the
associated policy metadata so the token can be rotated or deleted later.

Before the project is saved, wabii-signal sends an OTLP trace smoke test and
waits for it to become queryable through Grafana. That keeps broken write-token
or datasource setups from silently being accepted.
`),
		Example: strings.TrimSpace(`
  wabsignal project create shop-api shop-api --write-token "$GRAFANA_WRITE_TOKEN"
  wabsignal project create storefront shop-api shop-web shop-worker
`),
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, path, err := loadConfigFromFlags(opts)
			if err != nil {
				return err
			}
			if !config.SetupComplete() {
				return fmt.Errorf(setupHint)
			}

			projectName := strings.TrimSpace(args[0])
			primaryService := strings.TrimSpace(args[1])
			extraServices := dedupePreserveOrder(args[2:])
			if projectName == "" || primaryService == "" {
				return fmt.Errorf("project name and primary service are required")
			}
			if _, exists := config.Projects[projectName]; exists {
				return fmt.Errorf("project %q already exists", projectName)
			}

			readClient, err := buildSetupClient(config)
			if err != nil {
				return err
			}
			ctx, cancel := timeoutContext(45)
			defer cancel()
			discoveredSources, sources, err := discoverSignalSources(ctx, readClient)
			if err != nil {
				return err
			}

			project := &cfg.Project{
				Name:           projectName,
				PrimaryService: primaryService,
				Services:       extraServices,
				Sources:        copyStringMap(discoveredSources),
			}
			project.EnsureDefaults()

			var smokeResult *traceSmokeResult
			switch config.Setup.Mode {
			case cfg.ModeRestrictive:
				createWriteToken, err = promptOrValue(createWriteToken, "Project write token", true, createNonInteractive)
				if err != nil {
					return err
				}
				project.WriteToken = strings.TrimSpace(createWriteToken)
			case cfg.ModeFullAccess:
				if err := createManagedProjectToken(ctx, config, project); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported setup mode %q", config.Setup.Mode)
			}

			smokeResult, err = validateProjectWriteToken(ctx, config, projectName, project, readClient, sources)
			if err != nil {
				if project.ManagedWriteToken {
					_ = cleanupManagedProjectResources(ctx, config, project)
				}
				return err
			}

			config.Projects[projectName] = project
			config.CurrentProject = projectName
			if err := cfg.Save(path, config); err != nil {
				return err
			}

			payload := projectSummaryPayload(projectName, project)
			payload["smoke_test"] = smokeResult
			if config.Setup.Mode == cfg.ModeRestrictive {
				payload["recommended_write_scopes"] = managedWriteScopes
				payload["write_guidance"] = "Grafana Cloud label policies constrain read scopes, not write scopes; write isolation is enforced through OTEL identity and CLI-side project scoping."
			}
			if isJSONOutput(opts.Output) {
				return output.PrintJSON(payload)
			}

			fmt.Printf("Project %s is ready.\n", projectName)
			fmt.Printf("Primary service: %s\n", primaryService)
			if len(extraServices) > 0 {
				fmt.Printf("Extra read-scope services: %s\n", strings.Join(extraServices, ", "))
			}
			fmt.Printf("Write token validated with smoke trace %s.\n", smokeResult.TraceID)
			fmt.Println("Next:")
			fmt.Printf("  wabsignal run start\n")
			fmt.Printf("  wabsignal project env %s --format dotenv\n", projectName)
			return nil
		},
	}
	createCmd.Flags().StringVar(&createWriteToken, "write-token", "", "Manual project write token (restrictive mode)")
	createCmd.Flags().BoolVar(&createNonInteractive, "non-interactive", false, "Fail instead of prompting for missing values")
	cmd.AddCommand(createCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			config, _, err := loadConfigFromFlags(opts)
			if err != nil {
				return err
			}
			rows := make([]map[string]any, 0, len(config.Projects))
			names := make([]string, 0, len(config.Projects))
			for name := range config.Projects {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				project := config.Projects[name]
				rows = append(rows, map[string]any{
					"name":            name,
					"current":         name == config.CurrentProject,
					"primary_service": project.PrimaryService,
					"services":        strings.Join(project.Services, ","),
					"managed":         project.ManagedWriteToken,
					"write_token":     project.WriteToken != "",
					"current_run":     currentRunID(project),
				})
			}
			if isJSONOutput(opts.Output) {
				return output.PrintJSON(rows)
			}
			output.PrintTable(rows, []string{"name", "current", "primary_service", "services", "managed", "write_token", "current_run"})
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "use <project>",
		Short: "Switch the current project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, path, err := loadConfigFromFlags(opts)
			if err != nil {
				return err
			}
			project, ok := config.Projects[args[0]]
			if !ok {
				return fmt.Errorf("project %q not found", args[0])
			}
			config.CurrentProject = args[0]
			if err := cfg.Save(path, config); err != nil {
				return err
			}
			payload := projectSummaryPayload(args[0], project)
			payload["current"] = true
			if isJSONOutput(opts.Output) {
				return output.PrintJSON(payload)
			}
			fmt.Printf("Current project: %s\n", args[0])
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show [project]",
		Short: "Show project details",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, _, err := loadConfigFromFlags(opts)
			if err != nil {
				return err
			}
			project, projectName, err := config.ResolveProject(coalesce(firstArg(args), opts.Project))
			if err != nil {
				return err
			}

			payload := projectSummaryPayload(projectName, project)
			if isJSONOutput(opts.Output) {
				return output.PrintJSON(payload)
			}
			output.PrintTable([]map[string]any{payload}, []string{
				"name", "primary_service", "services", "managed_write", "write_token", "managed_policy_id", "managed_token_id", "current_run",
			})
			return nil
		},
	})

	var envFormat string
	envCmd := &cobra.Command{
		Use:   "env [project]",
		Short: "Emit OTLP bootstrap environment variables for a project",
		Long: strings.TrimSpace(`
Emit the OTLP environment needed to point an app at the current Grafana-backed
wabii-signal setup.

The output includes:

- OTEL_EXPORTER_OTLP_ENDPOINT
- OTEL_EXPORTER_OTLP_HEADERS
- OTEL_SERVICE_NAME
- OTEL_RESOURCE_ATTRIBUTES

If a project run scope is active, WABSIGNAL_RUN_ID is also emitted so the app
can stamp telemetry for the current debugging session.
`),
		Example: strings.TrimSpace(`
  wabsignal project env
  wabsignal project env shop-api --format dotenv
  wabsignal project env shop-api --format json
`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, _, err := loadConfigFromFlags(opts)
			if err != nil {
				return err
			}
			project, projectName, err := config.ResolveProject(coalesce(firstArg(args), opts.Project))
			if err != nil {
				return err
			}
			if strings.TrimSpace(project.WriteToken) == "" {
				return fmt.Errorf("project %q has no write token configured", projectName)
			}

			vars := map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": config.Setup.OTLPEndpoint,
				"OTEL_EXPORTER_OTLP_HEADERS":  buildOTLPHeaders(config.Setup.OTLPInstanceID, project.WriteToken),
				"OTEL_SERVICE_NAME":           project.PrimaryService,
				"OTEL_RESOURCE_ATTRIBUTES":    buildResourceAttributes(projectName, project),
			}
			if runID := currentRunID(project); runID != "" {
				vars["WABSIGNAL_RUN_ID"] = runID
			}
			return renderEnv(os.Stdout, envFormat, vars)
		},
	}
	envCmd.Flags().StringVar(&envFormat, "format", "shell", "Output format: shell|powershell|dotenv|json")
	cmd.AddCommand(envCmd)

	var setTokenValue string
	var setTokenNonInteractive bool
	var setTokenForce bool
	setTokenCmd := &cobra.Command{
		Use:   "set-token [project]",
		Short: "Set or replace a project's write token",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, path, err := loadConfigFromFlags(opts)
			if err != nil {
				return err
			}
			project, projectName, err := config.ResolveProject(coalesce(firstArg(args), opts.Project))
			if err != nil {
				return err
			}
			setTokenValue, err = promptOrValue(setTokenValue, "Project write token", true, setTokenNonInteractive)
			if err != nil {
				return err
			}

			readClient, err := buildSetupClient(config)
			if err != nil {
				return err
			}
			ctx, cancel := timeoutContext(45)
			defer cancel()
			_, sources, err := discoverSignalSources(ctx, readClient)
			if err != nil {
				return err
			}

			temp := cloneProject(project)
			temp.WriteToken = strings.TrimSpace(setTokenValue)
			temp.ManagedWriteToken = false
			temp.ManagedPolicyID = ""
			temp.ManagedPolicyName = ""
			temp.ManagedTokenID = ""
			temp.ManagedTokenName = ""

			smokeResult, err := validateProjectWriteToken(ctx, config, projectName, temp, readClient, sources)
			if err != nil {
				return err
			}

			if project.ManagedWriteToken {
				if err := cleanupManagedProjectResources(ctx, config, project); err != nil && !setTokenForce {
					return err
				}
			}

			*project = *temp
			if err := cfg.Save(path, config); err != nil {
				return err
			}

			payload := projectSummaryPayload(projectName, project)
			payload["smoke_test"] = smokeResult
			payload["forced"] = setTokenForce
			if isJSONOutput(opts.Output) {
				return output.PrintJSON(payload)
			}
			fmt.Printf("Project write token updated for %s.\n", projectName)
			return nil
		},
	}
	setTokenCmd.Flags().StringVar(&setTokenValue, "token", "", "Project write token")
	setTokenCmd.Flags().BoolVar(&setTokenNonInteractive, "non-interactive", false, "Fail instead of prompting")
	setTokenCmd.Flags().BoolVar(&setTokenForce, "force", false, "Keep going even if managed-token cleanup fails")
	cmd.AddCommand(setTokenCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "rotate-token [project]",
		Short: "Rotate a managed full-access project write token",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, path, err := loadConfigFromFlags(opts)
			if err != nil {
				return err
			}
			if config.Setup.Mode != cfg.ModeFullAccess {
				return fmt.Errorf("rotate-token requires full-access setup mode")
			}
			project, projectName, err := config.ResolveProject(coalesce(firstArg(args), opts.Project))
			if err != nil {
				return err
			}
			if !project.ManagedWriteToken || strings.TrimSpace(project.ManagedPolicyID) == "" {
				return fmt.Errorf("project %q is not using a managed write token", projectName)
			}

			readClient, err := buildSetupClient(config)
			if err != nil {
				return err
			}
			ctx, cancel := timeoutContext(45)
			defer cancel()
			_, sources, err := discoverSignalSources(ctx, readClient)
			if err != nil {
				return err
			}

			newToken, err := createManagedToken(ctx, config, project)
			if err != nil {
				return err
			}
			temp := cloneProject(project)
			temp.WriteToken = strings.TrimSpace(newToken.Key)
			temp.ManagedTokenID = newToken.ID
			temp.ManagedTokenName = newToken.Name

			smokeResult, err := validateProjectWriteToken(ctx, config, projectName, temp, readClient, sources)
			if err != nil {
				_ = deleteManagedToken(ctx, config, project.ManagedPolicyID, newToken.ID)
				return err
			}

			oldTokenID := project.ManagedTokenID
			*project = *temp
			if strings.TrimSpace(oldTokenID) != "" {
				if err := deleteManagedToken(ctx, config, project.ManagedPolicyID, oldTokenID); err != nil {
					return err
				}
			}
			if err := cfg.Save(path, config); err != nil {
				return err
			}

			payload := projectSummaryPayload(projectName, project)
			payload["smoke_test"] = smokeResult
			if isJSONOutput(opts.Output) {
				return output.PrintJSON(payload)
			}
			fmt.Printf("Rotated managed write token for %s.\n", projectName)
			return nil
		},
	})

	var deleteYes bool
	var deleteForce bool
	deleteCmd := &cobra.Command{
		Use:   "delete <project>",
		Short: "Delete a project and clean up any managed Grafana Cloud access policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !deleteYes {
				return fmt.Errorf("project delete requires --yes")
			}
			config, path, err := loadConfigFromFlags(opts)
			if err != nil {
				return err
			}
			project, ok := config.Projects[args[0]]
			if !ok || project == nil {
				return fmt.Errorf("project %q not found", args[0])
			}

			ctx, cancel := timeoutContext(30)
			defer cancel()
			if project.ManagedWriteToken {
				if err := cleanupManagedProjectResources(ctx, config, project); err != nil && !deleteForce {
					return err
				}
			}

			delete(config.Projects, args[0])
			if config.CurrentProject == args[0] {
				config.CurrentProject = ""
			}
			if err := cfg.Save(path, config); err != nil {
				return err
			}

			payload := map[string]any{"deleted": args[0], "forced": deleteForce}
			if isJSONOutput(opts.Output) {
				return output.PrintJSON(payload)
			}
			fmt.Printf("Deleted project %s.\n", args[0])
			return nil
		},
	}
	deleteCmd.Flags().BoolVar(&deleteYes, "yes", false, "Confirm project deletion")
	deleteCmd.Flags().BoolVar(&deleteForce, "force", false, "Delete local config even if managed-resource cleanup fails")
	cmd.AddCommand(deleteCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "set-source <project> <signal> <uid>",
		Short: "Override the datasource UID used for logs, metrics, or traces",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, path, err := loadConfigFromFlags(opts)
			if err != nil {
				return err
			}
			project, _, err := config.ResolveProject(args[0])
			if err != nil {
				return err
			}
			signal := normalizeSignal(args[1])
			if signal == "" {
				return fmt.Errorf("signal must be logs, metrics, or traces")
			}

			readClient, err := buildSetupClient(config)
			if err != nil {
				return err
			}
			ctx, cancel := timeoutContext(25)
			defer cancel()
			sources, err := readClient.GetDataSources(ctx)
			if err != nil {
				return err
			}
			if grafana.SourceByUID(sources, args[2]) == nil {
				return fmt.Errorf("datasource uid %q was not found in Grafana", args[2])
			}

			if project.Sources == nil {
				project.Sources = map[string]string{}
			}
			project.Sources[signal] = args[2]
			if err := cfg.Save(path, config); err != nil {
				return err
			}
			payload := projectSummaryPayload(args[0], project)
			payload["updated_signal"] = signal
			if isJSONOutput(opts.Output) {
				return output.PrintJSON(payload)
			}
			fmt.Printf("Set %s datasource for %s to %s.\n", signal, args[0], args[2])
			return nil
		},
	})

	var logsServiceLabel string
	var metricsServiceLabel string
	var tracesServiceAttr string
	var logsRunLabel string
	var metricsRunLabel string
	var tracesRunAttr string
	setScopeCmd := &cobra.Command{
		Use:   "set-scope <project>",
		Short: "Override service and run label keys used for automatic query scoping",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, path, err := loadConfigFromFlags(opts)
			if err != nil {
				return err
			}
			project, _, err := config.ResolveProject(args[0])
			if err != nil {
				return err
			}
			updated := false
			if strings.TrimSpace(logsServiceLabel) != "" {
				project.QueryScope.LogServiceLabel = strings.TrimSpace(logsServiceLabel)
				updated = true
			}
			if strings.TrimSpace(metricsServiceLabel) != "" {
				project.QueryScope.MetricServiceLabel = strings.TrimSpace(metricsServiceLabel)
				updated = true
			}
			if strings.TrimSpace(tracesServiceAttr) != "" {
				project.QueryScope.TraceServiceAttr = strings.TrimSpace(tracesServiceAttr)
				updated = true
			}
			if strings.TrimSpace(logsRunLabel) != "" {
				project.QueryScope.LogRunLabel = strings.TrimSpace(logsRunLabel)
				updated = true
			}
			if strings.TrimSpace(metricsRunLabel) != "" {
				project.QueryScope.MetricRunLabel = strings.TrimSpace(metricsRunLabel)
				updated = true
			}
			if strings.TrimSpace(tracesRunAttr) != "" {
				project.QueryScope.TraceRunAttr = strings.TrimSpace(tracesRunAttr)
				updated = true
			}
			if !updated {
				return fmt.Errorf("set at least one scope override flag")
			}
			project.EnsureDefaults()
			if err := cfg.Save(path, config); err != nil {
				return err
			}
			payload := projectSummaryPayload(args[0], project)
			if isJSONOutput(opts.Output) {
				return output.PrintJSON(payload)
			}
			fmt.Printf("Updated query scope for %s.\n", args[0])
			return nil
		},
	}
	setScopeCmd.Flags().StringVar(&logsServiceLabel, "logs-service-label", "", "LogQL label used for service scoping")
	setScopeCmd.Flags().StringVar(&metricsServiceLabel, "metrics-service-label", "", "Prometheus label used for service scoping")
	setScopeCmd.Flags().StringVar(&tracesServiceAttr, "traces-service-attr", "", "TraceQL attribute used for service scoping")
	setScopeCmd.Flags().StringVar(&logsRunLabel, "logs-run-label", "", "LogQL label used for run scoping")
	setScopeCmd.Flags().StringVar(&metricsRunLabel, "metrics-run-label", "", "Prometheus label used for run scoping")
	setScopeCmd.Flags().StringVar(&tracesRunAttr, "traces-run-attr", "", "TraceQL attribute used for run scoping")
	cmd.AddCommand(setScopeCmd)

	serviceCmd := &cobra.Command{
		Use:   "service",
		Short: "Manage extra read-scope services on a project",
	}
	serviceCmd.AddCommand(&cobra.Command{
		Use:   "add <project> <service>",
		Short: "Add an extra service to project query scope",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, path, err := loadConfigFromFlags(opts)
			if err != nil {
				return err
			}
			project, _, err := config.ResolveProject(args[0])
			if err != nil {
				return err
			}
			project.Services = dedupePreserveOrder(append(project.Services, args[1]))
			if err := cfg.Save(path, config); err != nil {
				return err
			}
			payload := projectSummaryPayload(args[0], project)
			if isJSONOutput(opts.Output) {
				return output.PrintJSON(payload)
			}
			fmt.Printf("Added %s to %s query scope.\n", args[1], args[0])
			return nil
		},
	})
	serviceCmd.AddCommand(&cobra.Command{
		Use:   "remove <project> <service>",
		Short: "Remove an extra service from project query scope",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, path, err := loadConfigFromFlags(opts)
			if err != nil {
				return err
			}
			project, _, err := config.ResolveProject(args[0])
			if err != nil {
				return err
			}
			if strings.EqualFold(project.PrimaryService, args[1]) {
				return fmt.Errorf("cannot remove the primary service; create a new project if the primary service changed")
			}
			filtered := make([]string, 0, len(project.Services))
			for _, service := range project.Services {
				if !strings.EqualFold(strings.TrimSpace(service), strings.TrimSpace(args[1])) {
					filtered = append(filtered, service)
				}
			}
			project.Services = filtered
			if err := cfg.Save(path, config); err != nil {
				return err
			}
			payload := projectSummaryPayload(args[0], project)
			if isJSONOutput(opts.Output) {
				return output.PrintJSON(payload)
			}
			fmt.Printf("Removed %s from %s query scope.\n", args[1], args[0])
			return nil
		},
	})
	cmd.AddCommand(serviceCmd)

	return cmd
}

func createManagedProjectToken(ctx context.Context, config *cfg.Config, project *cfg.Project) error {
	managementToken, err := secret.GetManagementToken()
	if err != nil {
		if err == secret.ErrNotFound {
			return fmt.Errorf("missing Grafana Cloud policy token in keyring; rerun `wabsignal setup --mode full-access`")
		}
		return fmt.Errorf("failed to read Grafana Cloud policy token: %w", err)
	}

	client := cloudapi.New(managementToken)
	policyName := managedResourceName(project.Name, project.PrimaryService, "write")
	policy, err := client.CreateAccessPolicy(ctx, config.Setup.Cloud.Region, cloudapi.CreateAccessPolicyRequest{
		Name:        policyName,
		DisplayName: fmt.Sprintf("wabsignal %s writer", project.Name),
		Scopes:      managedWriteScopes,
		Realms: []cloudapi.AccessPolicyRealm{
			{Type: "stack", Identifier: config.Setup.Cloud.StackID},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create access policy: %w", err)
	}

	token, err := client.CreateToken(ctx, config.Setup.Cloud.Region, policy.ID, cloudapi.CreateTokenRequest{
		Name:        managedResourceName(project.Name, project.PrimaryService, "token"),
		DisplayName: fmt.Sprintf("wabsignal %s token", project.Name),
	})
	if err != nil {
		_ = client.DeleteAccessPolicy(ctx, config.Setup.Cloud.Region, policy.ID)
		return fmt.Errorf("failed to create access token: %w", err)
	}

	project.WriteToken = strings.TrimSpace(token.Key)
	project.ManagedWriteToken = true
	project.ManagedPolicyID = policy.ID
	project.ManagedPolicyName = policy.Name
	project.ManagedTokenID = token.ID
	project.ManagedTokenName = token.Name
	return nil
}

func createManagedToken(ctx context.Context, config *cfg.Config, project *cfg.Project) (*cloudapi.Token, error) {
	managementToken, err := secret.GetManagementToken()
	if err != nil {
		if err == secret.ErrNotFound {
			return nil, fmt.Errorf("missing Grafana Cloud policy token in keyring; rerun `wabsignal setup --mode full-access`")
		}
		return nil, fmt.Errorf("failed to read Grafana Cloud policy token: %w", err)
	}

	client := cloudapi.New(managementToken)
	token, err := client.CreateToken(ctx, config.Setup.Cloud.Region, project.ManagedPolicyID, cloudapi.CreateTokenRequest{
		Name:        managedResourceName(project.Name, project.PrimaryService, "token"),
		DisplayName: fmt.Sprintf("wabsignal %s token", project.Name),
	})
	if err != nil {
		return nil, err
	}
	return token, nil
}

func cleanupManagedProjectResources(ctx context.Context, config *cfg.Config, project *cfg.Project) error {
	if project == nil || !project.ManagedWriteToken {
		return nil
	}
	managementToken, err := secret.GetManagementToken()
	if err != nil {
		if err == secret.ErrNotFound {
			return fmt.Errorf("missing Grafana Cloud policy token in keyring; rerun `wabsignal setup --mode full-access`")
		}
		return fmt.Errorf("failed to read Grafana Cloud policy token: %w", err)
	}

	client := cloudapi.New(managementToken)
	if strings.TrimSpace(project.ManagedPolicyID) != "" {
		return client.DeleteAccessPolicy(ctx, config.Setup.Cloud.Region, project.ManagedPolicyID)
	}
	if strings.TrimSpace(project.ManagedTokenID) != "" {
		return client.DeleteToken(ctx, config.Setup.Cloud.Region, project.ManagedTokenID)
	}
	return nil
}

func deleteManagedToken(ctx context.Context, config *cfg.Config, accessPolicyID, tokenID string) error {
	managementToken, err := secret.GetManagementToken()
	if err != nil {
		if err == secret.ErrNotFound {
			return fmt.Errorf("missing Grafana Cloud policy token in keyring; rerun `wabsignal setup --mode full-access`")
		}
		return fmt.Errorf("failed to read Grafana Cloud policy token: %w", err)
	}

	client := cloudapi.New(managementToken)
	return client.DeleteToken(ctx, config.Setup.Cloud.Region, tokenID)
}

func validateProjectWriteToken(ctx context.Context, config *cfg.Config, projectName string, project *cfg.Project, readClient *grafana.Client, sources []grafana.DataSource) (*traceSmokeResult, error) {
	traceDS := resolveTraceSourceForValidation(config, project, sources)
	if traceDS == nil {
		return nil, fmt.Errorf("trace datasource is not configured; set one with `wabsignal project set-source %s traces <uid>`", projectName)
	}
	return runOTLPTraceSmokeTest(ctx, config, projectName, project, traceDS, readClient, true)
}

func resolveTraceSourceForValidation(config *cfg.Config, project *cfg.Project, sources []grafana.DataSource) *grafana.DataSource {
	if project != nil {
		if uid := strings.TrimSpace(project.Sources["traces"]); uid != "" {
			if ds := grafana.SourceByUID(sources, uid); ds != nil {
				return ds
			}
		}
	}
	if config != nil {
		if uid := strings.TrimSpace(config.Setup.Sources["traces"]); uid != "" {
			if ds := grafana.SourceByUID(sources, uid); ds != nil {
				return ds
			}
		}
	}
	for i := range sources {
		if strings.EqualFold(sources[i].Type, "tempo") {
			return &sources[i]
		}
	}
	return nil
}

func projectSummaryPayload(projectName string, project *cfg.Project) map[string]any {
	return map[string]any{
		"name":              projectName,
		"primary_service":   project.PrimaryService,
		"services":          project.Services,
		"sources":           project.Sources,
		"defaults":          project.Defaults,
		"query_scope":       project.QueryScope,
		"current_run":       currentRunID(project),
		"run_state":         project.CurrentRun,
		"bootstrap":         project.BootstrapAttributes,
		"managed_write":     project.ManagedWriteToken,
		"managed_policy_id": project.ManagedPolicyID,
		"managed_token_id":  project.ManagedTokenID,
		"write_token":       project.WriteToken != "",
	}
}

func currentRunID(project *cfg.Project) string {
	if project == nil || project.CurrentRun == nil {
		return ""
	}
	return strings.TrimSpace(project.CurrentRun.ID)
}

func renderEnv(out io.Writer, format string, vars map[string]string) error {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "shell"
	}

	keys := make([]string, 0, len(vars))
	for key := range vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	switch format {
	case "json":
		return output.PrintJSON(vars)
	case "dotenv":
		for _, key := range keys {
			fmt.Fprintf(out, "%s=%s\n", key, quoteDotenv(vars[key]))
		}
		return nil
	case "powershell":
		for _, key := range keys {
			fmt.Fprintf(out, "$env:%s=%s\n", key, quotePS(vars[key]))
		}
		return nil
	case "shell":
		for _, key := range keys {
			fmt.Fprintf(out, "export %s=%s\n", key, quoteShell(vars[key]))
		}
		return nil
	default:
		return fmt.Errorf("unsupported env format: %s", format)
	}
}

func quoteShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func quotePS(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func quoteDotenv(value string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneProject(project *cfg.Project) *cfg.Project {
	if project == nil {
		return nil
	}
	copy := *project
	copy.Services = append([]string(nil), project.Services...)
	copy.Sources = copyStringMap(project.Sources)
	copy.BootstrapAttributes = copyStringMap(project.BootstrapAttributes)
	if project.CurrentRun != nil {
		runCopy := *project.CurrentRun
		copy.CurrentRun = &runCopy
	}
	return &copy
}

func dedupePreserveOrder(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func normalizeSignal(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "logs":
		return "logs"
	case "metrics":
		return "metrics"
	case "traces":
		return "traces"
	default:
		return ""
	}
}

func managedResourceName(projectName, primaryService, suffix string) string {
	base := strings.ToLower(projectName + "-" + primaryService + "-" + suffix)
	base = strings.NewReplacer(" ", "-", "_", "-", ".", "-").Replace(base)
	base = strings.Trim(base, "-")
	if base == "" {
		return "wabsignal-" + suffix
	}
	return "wabsignal-" + base
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func coalesce(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
