package wabsignal

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/derekurban/wabii-signal/internal/cfg"
	"github.com/derekurban/wabii-signal/internal/cloudapi"
	"github.com/derekurban/wabii-signal/internal/grafana"
	"github.com/derekurban/wabii-signal/internal/output"
	"github.com/derekurban/wabii-signal/internal/secret"
	"github.com/spf13/cobra"
)

func newSetupCmd(opts *GlobalOptions) *cobra.Command {
	var (
		mode            string
		grafanaAPIURL   string
		stackName       string
		otlpEndpoint    string
		otlpInstanceID  string
		queryToken      string
		managementToken string
		cloudStackID    string
		cloudRegion     string
		cloudOrgID      string
		nonInteractive  bool
	)

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure hosted Grafana HTTP API access and OTLP ingest",
		Long: strings.TrimSpace(`
Human-only machine bootstrap for wabii-signal.

This command is intended to be run by a human operator, not by a coding agent.
Its job is to connect this machine to a Grafana stack, store the global read
credential in the OS keyring, and optionally store a Grafana Cloud management
credential for automatic write-token lifecycle in full-access mode.

When run in a terminal, setup launches a guided TUI wizard by default. Use
--non-interactive only for explicit operator-driven scripting or CI-style
provisioning where every required flag is already known.

The guided setup flow starts with the Grafana organization ID, then walks the
operator through the Grafana Cloud pages to open:

- https://grafana.com/orgs/<org id>
- https://grafana.com/orgs/<org id>/access-policies (full-access only)
- https://<org id>.grafana.net/org/serviceaccounts/create

Setup configures three distinct planes:

1. Read plane: Grafana HTTP API URL plus a read token for querying logs,
   metrics, and traces.
2. Write plane: OTLP endpoint plus OTLP instance ID used later to build
   project-specific OTLP headers.
3. Management plane: optional Grafana Cloud policy token, stack ID, and region
   used only in full-access mode for managed project write tokens. The stack ID
   is derived from the OTLP instance ID when possible, and the region is
   derived from the OTLP endpoint when possible.
`),
		Example: strings.TrimSpace(`
  # Human-guided setup wizard (recommended)
  wabsignal setup

  # Restrictive mode with explicit stack name
  wabsignal setup \
    --mode restrictive \
    --stack-name my-stack \
    --otlp-endpoint https://otlp-gateway-prod-us-central-0.grafana.net/otlp \
    --otlp-instance-id 123456 \
    --query-token "$GRAFANA_SERVICE_ACCOUNT_TOKEN"

  # Full-access mode using the raw Grafana read URL
  wabsignal setup \
    --mode full-access \
    --grafana-api-url https://my-stack.grafana.net/api/ds/query \
    --otlp-endpoint https://otlp-gateway-prod-us-central-0.grafana.net/otlp \
    --otlp-instance-id 123456 \
    --query-token "$GRAFANA_SERVICE_ACCOUNT_TOKEN" \
    --policy-token "$GRAFANA_CLOUD_POLICY_TOKEN" \
    --cloud-stack-id 654321 \
    --cloud-region us

  # Scripted operator setup without the TUI
  wabsignal setup \
    --non-interactive \
    --mode restrictive \
    --stack-name my-stack \
    --otlp-endpoint https://otlp-gateway-prod-us-central-0.grafana.net/otlp \
    --otlp-instance-id 123456 \
    --query-token "$GRAFANA_SERVICE_ACCOUNT_TOKEN"
`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if shouldUseSetupWizard(nonInteractive, opts.Output) {
				state := &setupWizardState{
					Mode:            mode,
					GrafanaAPIURL:   grafanaAPIURL,
					StackName:       stackName,
					OTLPEndpoint:    otlpEndpoint,
					OTLPInstanceID:  otlpInstanceID,
					QueryToken:      queryToken,
					ManagementToken: managementToken,
					CloudStackID:    cloudStackID,
					CloudRegion:     cloudRegion,
					OrgID:           cloudOrgID,
				}
				if err := runSetupWizard(state); err != nil {
					return err
				}
				mode = state.Mode
				grafanaAPIURL = state.GrafanaAPIURL
				stackName = state.StackName
				otlpEndpoint = state.OTLPEndpoint
				otlpInstanceID = state.OTLPInstanceID
				queryToken = state.QueryToken
				managementToken = state.ManagementToken
				cloudStackID = state.CloudStackID
				cloudRegion = state.CloudRegion
				cloudOrgID = state.OrgID
			}

			mode = cfg.NormalizeMode(mode)
			if mode == "" {
				return fmt.Errorf("--mode must be %q or %q", cfg.ModeRestrictive, cfg.ModeFullAccess)
			}

			var err error
			grafanaAPIURL, stackName, err = cfg.NormalizeGrafanaAPIURL(grafanaAPIURL, stackName)
			if err != nil {
				return err
			}
			grafanaAPIURL, err = normalizeURL(grafanaAPIURL, "grafana api url")
			if err != nil {
				return err
			}
			otlpEndpoint, err = promptOrValue(otlpEndpoint, "OTLP endpoint", false, nonInteractive)
			if err != nil {
				return err
			}
			otlpEndpoint, err = normalizeURL(otlpEndpoint, "otlp endpoint")
			if err != nil {
				return err
			}
			otlpInstanceID, err = promptOrValue(otlpInstanceID, "OTLP instance ID", false, nonInteractive)
			if err != nil {
				return err
			}
			queryToken, err = promptOrValue(queryToken, "Grafana service account token", true, nonInteractive)
			if err != nil {
				return err
			}

			if mode == cfg.ModeFullAccess {
				managementToken, err = promptOrValue(managementToken, "Grafana Cloud access-policy management token", true, nonInteractive)
				if err != nil {
					return err
				}
				if strings.TrimSpace(cloudStackID) == "" {
					cloudStackID = strings.TrimSpace(otlpInstanceID)
				}
				if strings.TrimSpace(cloudRegion) == "" {
					cloudRegion = deriveCloudRegionFromOTLPEndpoint(otlpEndpoint)
				}
				if strings.TrimSpace(cloudStackID) == "" {
					cloudStackID, err = promptOrValue(cloudStackID, "Grafana Cloud stack ID", false, nonInteractive)
					if err != nil {
						return err
					}
				}
				if strings.TrimSpace(cloudRegion) == "" {
					cloudRegion, err = promptOrValue(cloudRegion, "Grafana Cloud region", false, nonInteractive)
					if err != nil {
						return err
					}
				}
			}

			ctx, cancel := timeoutContext(25)
			defer cancel()

			client := grafana.New(grafanaAPIURL, queryToken)
			health, err := client.GetHealth(ctx)
			if err != nil {
				return fmt.Errorf("grafana http api validation failed: %w", err)
			}
			discoveredSources, sources, err := discoverSignalSources(ctx, client)
			if err != nil {
				return explainSetupDiscoveryError(queryToken, err)
			}
			setupConfig := cfg.SetupConfig{
				Mode:           mode,
				GrafanaAPIURL:  grafanaAPIURL,
				StackName:      stackName,
				OTLPEndpoint:   strings.TrimSpace(otlpEndpoint),
				OTLPInstanceID: strings.TrimSpace(otlpInstanceID),
				Sources:        discoveredSources,
				Cloud: cfg.CloudSetupConfig{
					OrgSlug: strings.TrimSpace(cloudOrgID),
					StackID: strings.TrimSpace(cloudStackID),
					Region:  strings.TrimSpace(cloudRegion),
				},
			}

			writeValidation := map[string]any{
				"status": "deferred",
				"reason": "restrictive mode validates OTLP writes when a project write token is attached",
			}
			if mode == cfg.ModeFullAccess {
				cloudClient := cloudapi.New(managementToken)
				if err := cloudClient.Validate(ctx, cloudRegion, cloudStackID); err != nil {
					return fmt.Errorf("grafana cloud policy validation failed: %w", err)
				}
				smokeResult, err := validateFullAccessSetupWritePath(ctx, setupConfig, managementToken, client, sources)
				if err != nil {
					return fmt.Errorf("otlp smoke test failed: %w", err)
				}
				writeValidation = map[string]any{
					"status": "validated",
					"trace":  smokeResult,
				}
			}

			config, path, err := loadConfigFromFlags(opts)
			if err != nil {
				return err
			}
			config.Setup = setupConfig
			for _, project := range config.Projects {
				if project == nil {
					continue
				}
				if project.Sources == nil {
					project.Sources = map[string]string{}
				}
				for signal, uid := range discoveredSources {
					if strings.TrimSpace(project.Sources[signal]) == "" {
						project.Sources[signal] = uid
					}
				}
			}

			if err := secret.SetQueryToken(queryToken); err != nil {
				return fmt.Errorf("failed to store read token in keyring: %w", err)
			}
			if mode == cfg.ModeFullAccess {
				if err := secret.SetManagementToken(managementToken); err != nil {
					return fmt.Errorf("failed to store policy token in keyring: %w", err)
				}
			} else if err := secret.DeleteManagementToken(); err != nil {
				return fmt.Errorf("failed to clear unused policy token: %w", err)
			}

			if err := cfg.Save(path, config); err != nil {
				return err
			}

			payload := map[string]any{
				"setup":              formatSetupSummary(mode, config)[0],
				"grafana_health":     health,
				"discovered_sources": discoveredSources,
				"keyring":            setupSecretSummary(mode, queryToken, managementToken),
				"write_validation":   writeValidation,
				"config_path":        path,
				"setup_complete":     config.SetupComplete(),
				"next_recommended":   "wabsignal project create <project-name> <primary-service> [extra-services...]",
			}
			if isJSONOutput(opts.Output) {
				return output.PrintJSON(payload)
			}

			fmt.Println("Hosted Grafana setup saved.")
			output.PrintTable(formatSetupSummary(mode, config), []string{
				"mode", "grafana_api_url", "stack_name", "otlp_endpoint", "otlp_instance_id", "cloud_stack_id", "cloud_region",
			})
			fmt.Println()
			fmt.Println("Discovered data sources:")
			rows := make([]map[string]any, 0, len(discoveredSources))
			for signal, uid := range discoveredSources {
				rows = append(rows, map[string]any{"signal": signal, "uid": uid})
			}
			output.PrintTable(rows, []string{"signal", "uid"})
			fmt.Println()
			fmt.Println("Secrets stored in OS keyring:")
			fmt.Printf("  read token: %s\n", redactSecret(queryToken))
			if mode == cfg.ModeFullAccess {
				fmt.Printf("  policy token: %s\n", redactSecret(managementToken))
			}
			fmt.Println()
			fmt.Printf("Grafana health: database=%s version=%s\n", health.Database, health.Version)
			if mode == cfg.ModeFullAccess {
				if trace, ok := writeValidation["trace"].(*traceSmokeResult); ok && trace != nil {
					fmt.Printf("OTLP write path validated with smoke trace %s.\n", trace.TraceID)
				}
			} else {
				fmt.Println("OTLP write validation is deferred until `wabsignal project create` attaches a project write token.")
			}
			fmt.Println("Next:")
			fmt.Println("  wabsignal project create <project-name> <primary-service> [extra-services...]")
			return nil
		},
	}

	cmd.Flags().StringVar(&mode, "mode", "", "Access mode: restrictive or full-access")
	cmd.Flags().StringVar(&grafanaAPIURL, "grafana-api-url", "", "Grafana stack URL or full /api/ds/query URL")
	cmd.Flags().StringVar(&stackName, "stack-name", "", "Grafana stack name used to construct https://<stack>.grafana.net")
	cmd.Flags().StringVar(&otlpEndpoint, "otlp-endpoint", "", "Grafana Cloud OTLP endpoint")
	cmd.Flags().StringVar(&otlpInstanceID, "otlp-instance-id", "", "Grafana Cloud OTLP instance ID")
	cmd.Flags().StringVar(&queryToken, "query-token", "", "Grafana service account token for HTTP API reads")
	cmd.Flags().StringVar(&managementToken, "policy-token", "", "Grafana Cloud access-policy management token (full-access only)")
	cmd.Flags().StringVar(&cloudStackID, "cloud-stack-id", "", "Grafana Cloud numeric stack ID")
	cmd.Flags().StringVar(&cloudRegion, "cloud-region", "", "Grafana Cloud region")
	cmd.Flags().StringVar(&cloudOrgID, "org-id", "", "Grafana Cloud organization id or slug used for guided setup links and stored as metadata")
	cmd.Flags().StringVar(&cloudOrgID, "cloud-org-slug", "", "Deprecated alias for --org-id")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Fail instead of prompting for missing values")
	_ = cmd.Flags().MarkHidden("cloud-org-slug")
	return cmd
}

func normalizeURL(rawURL, label string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid %s: %w", label, err)
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "https"
	}
	if parsed.Host == "" && parsed.Path != "" {
		parsed.Host = parsed.Path
		parsed.Path = ""
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func explainSetupDiscoveryError(queryToken string, err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "401 Unauthorized") || strings.Contains(message, "api-key.invalid") {
		guidance := "datasource discovery failed after Grafana HTTP API validation. The supplied read token reached the Grafana stack but cannot list datasources."
		if strings.HasPrefix(strings.TrimSpace(queryToken), "glc_") {
			guidance += " The token begins with \"glc_\", which usually indicates a Grafana Cloud token rather than a Grafana stack service-account token."
		}
		guidance += " For wabii-signal setup, use a Grafana stack service-account token with datasource read/query access. Original error: " + message
		return fmt.Errorf("%s", guidance)
	}
	return fmt.Errorf("datasource discovery failed: %w", err)
}

func setupSecretSummary(mode, queryToken, managementToken string) map[string]any {
	summary := map[string]any{
		"read_token": redactSecret(queryToken),
	}
	if cfg.NormalizeMode(mode) == cfg.ModeFullAccess {
		summary["policy_token"] = redactSecret(managementToken)
	}
	return summary
}

func deriveCloudRegionFromOTLPEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return ""
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return ""
	}
	const prefix = "otlp-gateway-"
	const suffix = ".grafana.net"
	if !strings.HasPrefix(host, prefix) || !strings.HasSuffix(host, suffix) {
		return ""
	}
	region := strings.TrimPrefix(host, prefix)
	region = strings.TrimSuffix(region, suffix)
	return strings.TrimSpace(region)
}

func validateFullAccessSetupWritePath(ctx context.Context, setup cfg.SetupConfig, managementToken string, readClient *grafana.Client, sources []grafana.DataSource) (*traceSmokeResult, error) {
	traceSource := resolveTraceSourceForValidation(&cfg.Config{Setup: setup}, nil, sources)
	if traceSource == nil {
		return nil, fmt.Errorf("no trace datasource discovered; configure Tempo in Grafana first")
	}

	project, cleanup, err := createSetupValidationProject(ctx, setup, managementToken)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = cleanup()
	}()

	return runOTLPTraceSmokeTest(ctx, &cfg.Config{Setup: setup}, project.Name, project, traceSource, readClient, true)
}

func createSetupValidationProject(ctx context.Context, setup cfg.SetupConfig, managementToken string) (*cfg.Project, func() error, error) {
	client := cloudapi.New(managementToken)
	suffix, err := randomHex(4)
	if err != nil {
		return nil, nil, err
	}

	projectName := "setup-smoke-" + suffix
	serviceName := "wabsignal-setup-" + suffix
	policyName := managedResourceName(projectName, serviceName, "write")

	policy, err := client.CreateAccessPolicy(ctx, setup.Cloud.Region, cloudapi.CreateAccessPolicyRequest{
		Name:        policyName,
		DisplayName: fmt.Sprintf("wabsignal setup smoke %s", suffix),
		Scopes:      managedWriteScopes,
		Realms: []cloudapi.AccessPolicyRealm{
			{Type: "stack", Identifier: setup.Cloud.StackID},
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create setup smoke access policy: %w", err)
	}

	token, err := client.CreateToken(ctx, setup.Cloud.Region, policy.ID, cloudapi.CreateTokenRequest{
		Name:        managedResourceName(projectName, serviceName, "token"),
		DisplayName: fmt.Sprintf("wabsignal setup smoke token %s", suffix),
	})
	if err != nil {
		cleanupCtx, cancel := timeoutContext(20)
		defer cancel()
		_ = client.DeleteAccessPolicy(cleanupCtx, setup.Cloud.Region, policy.ID)
		return nil, nil, fmt.Errorf("failed to create setup smoke token: %w", err)
	}

	project := &cfg.Project{
		Name:              projectName,
		PrimaryService:    serviceName,
		WriteToken:        strings.TrimSpace(token.Key),
		ManagedWriteToken: true,
		ManagedPolicyID:   policy.ID,
		ManagedPolicyName: policy.Name,
		ManagedTokenID:    token.ID,
		ManagedTokenName:  token.Name,
	}
	project.EnsureDefaults()

	cleanup := func() error {
		cleanupCtx, cancel := timeoutContext(20)
		defer cancel()
		return client.DeleteAccessPolicy(cleanupCtx, setup.Cloud.Region, policy.ID)
	}
	return project, cleanup, nil
}
