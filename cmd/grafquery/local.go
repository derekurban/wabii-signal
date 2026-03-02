package grafquery

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/derekurban/grafana-query/internal/cfg"
	"github.com/derekurban/grafana-query/internal/localstack"
	"github.com/spf13/cobra"
)

const (
	defaultLocalContextName = "local"
	localServiceAccountName = "grafquery-local"
	localGrafanaWaitTimeout = 3 * time.Minute
	localTokenCreateTimeout = 45 * time.Second
	defaultLocalSince       = "1h"
	defaultLocalOutput      = "auto"
	defaultLocalResultLimit = 100
)

func newLocalCmd(opts *GlobalOptions) *cobra.Command {
	var dirFlag string

	cmd := &cobra.Command{
		Use:   "local",
		Short: "Manage a local Grafana/Loki/Prometheus/Tempo stack in Docker",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocalSetup(opts, dirFlag, defaultLocalContextName, "", "", false, true)
		},
	}

	cmd.PersistentFlags().StringVar(&dirFlag, "dir", "", "Directory for local stack files (default: ~/.config/grafquery/local-stack)")
	cmd.AddCommand(newLocalSetupCmd(opts, &dirFlag))
	cmd.AddCommand(newLocalUpCmd(&dirFlag))
	cmd.AddCommand(newLocalDownCmd(&dirFlag))
	cmd.AddCommand(newLocalStatusCmd(&dirFlag))
	cmd.AddCommand(newLocalInfoCmd(opts, &dirFlag))
	cmd.AddCommand(newLocalPurgeCmd(opts, &dirFlag))

	return cmd
}

func newLocalSetupCmd(opts *GlobalOptions, dirFlag *string) *cobra.Command {
	var contextName string
	var grafanaUser string
	var grafanaPassword string
	var nonInteractive bool
	var switchContext bool

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Interactive setup for local stack + grafquery context",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocalSetup(opts, *dirFlag, contextName, grafanaUser, grafanaPassword, nonInteractive, switchContext)
		},
	}

	cmd.Flags().StringVar(&contextName, "context-name", defaultLocalContextName, "Context name to write in config")
	cmd.Flags().StringVar(&grafanaUser, "grafana-user", "", "Grafana admin username for local stack (default: admin)")
	cmd.Flags().StringVar(&grafanaPassword, "grafana-password", "", "Grafana admin password for local stack (default: admin)")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Skip confirmation prompts")
	cmd.Flags().BoolVar(&switchContext, "switch-context", true, "Set the created context as current context")

	return cmd
}

func newLocalUpCmd(dirFlag *string) *cobra.Command {
	var waitForGrafana bool

	cmd := &cobra.Command{
		Use:   "up",
		Short: "Start the local stack in Docker",
		RunE: func(cmd *cobra.Command, args []string) error {
			rootDir, err := localstack.ResolveRootDir(*dirFlag)
			if err != nil {
				return err
			}
			if err := localstack.CheckDockerReady(); err != nil {
				return err
			}
			if err := localstack.Up(rootDir); err != nil {
				return err
			}
			if waitForGrafana {
				ctx, cancel := context.WithTimeout(context.Background(), localGrafanaWaitTimeout)
				defer cancel()
				if err := localstack.WaitForGrafana(ctx, localstack.DefaultGrafanaURL); err != nil {
					return err
				}
			}
			user, pass, err := localstack.LoadGrafanaCredentials(rootDir)
			if err != nil {
				return err
			}
			fmt.Printf("Local stack is running in %s\n", rootDir)
			fmt.Printf("Grafana: %s\n", localstack.DefaultGrafanaURL)
			fmt.Printf("Grafana user: %s\n", user)
			fmt.Printf("Grafana password: %s\n", pass)
			fmt.Printf("OTLP gRPC: %s\n", localstack.DefaultOTLPGRPCEndpoint)
			fmt.Printf("OTLP HTTP: %s\n", localstack.DefaultOTLPHTTPEndpoint)
			return nil
		},
	}
	cmd.Flags().BoolVar(&waitForGrafana, "wait", true, "Wait for Grafana health endpoint")
	return cmd
}

func newLocalDownCmd(dirFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Stop the local stack (keeps data volumes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			rootDir, err := localstack.ResolveRootDir(*dirFlag)
			if err != nil {
				return err
			}
			if err := localstack.Down(rootDir, false); err != nil {
				return err
			}
			fmt.Println("Local stack stopped.")
			return nil
		},
	}
}

func newLocalStatusCmd(dirFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show Docker Compose service status for the local stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			rootDir, err := localstack.ResolveRootDir(*dirFlag)
			if err != nil {
				return err
			}
			out, err := localstack.Status(rootDir)
			if err != nil {
				return err
			}
			fmt.Printf("Stack directory: %s\n\n", rootDir)
			fmt.Println(out)
			return nil
		},
	}
}

func newLocalInfoCmd(opts *GlobalOptions, dirFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show local OTLP endpoints plus Grafana URL/credentials/token",
		RunE: func(cmd *cobra.Command, args []string) error {
			rootDir, err := localstack.ResolveRootDir(*dirFlag)
			if err != nil {
				return err
			}

			grafanaURL := localstack.DefaultGrafanaURL
			grafanaUser, grafanaPassword, err := localstack.LoadGrafanaCredentials(rootDir)
			if err != nil {
				return err
			}
			contextName := defaultLocalContextName
			token := ""
			if state, err := localstack.LoadState(rootDir); err == nil && state != nil {
				if strings.TrimSpace(state.GrafanaURL) != "" {
					grafanaURL = strings.TrimSpace(state.GrafanaURL)
				}
				if strings.TrimSpace(state.GrafanaUser) != "" {
					grafanaUser = strings.TrimSpace(state.GrafanaUser)
				}
				if state.GrafanaPassword != "" {
					grafanaPassword = state.GrafanaPassword
				}
				if strings.TrimSpace(state.ContextName) != "" {
					contextName = state.ContextName
				}
				token = strings.TrimSpace(state.GrafanaToken)
			}

			if token == "" {
				c, _, err := loadConfigFromFlags(opts)
				if err == nil {
					if ctx, ok := c.Contexts[contextName]; ok && ctx != nil {
						token = strings.TrimSpace(ctx.Grafana.Token)
					}
				}
			}

			fmt.Printf("Stack directory: %s\n", rootDir)
			fmt.Printf("Context: %s\n", contextName)
			fmt.Printf("Grafana URL: %s\n", grafanaURL)
			fmt.Printf("Grafana username: %s\n", grafanaUser)
			fmt.Printf("Grafana password: %s\n", grafanaPassword)
			fmt.Printf("OTLP gRPC endpoint: %s\n", localstack.DefaultOTLPGRPCEndpoint)
			fmt.Printf("OTLP HTTP endpoint: %s\n", localstack.DefaultOTLPHTTPEndpoint)
			if token == "" {
				fmt.Println("Grafana service token: (not found - run `grafquery local setup`)")
			} else {
				fmt.Printf("Grafana service token: %s\n", token)
			}
			return nil
		},
	}
}

func newLocalPurgeCmd(opts *GlobalOptions, dirFlag *string) *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Delete local stack containers, volumes, and scaffold files",
		RunE: func(cmd *cobra.Command, args []string) error {
			rootDir, err := localstack.ResolveRootDir(*dirFlag)
			if err != nil {
				return err
			}

			contextName := defaultLocalContextName
			if state, err := localstack.LoadState(rootDir); err == nil && state != nil && strings.TrimSpace(state.ContextName) != "" {
				contextName = strings.TrimSpace(state.ContextName)
			}

			if !yes {
				ok, err := promptYesNo(fmt.Sprintf("Purge local stack at %s and remove context %q?", rootDir, contextName), false)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Println("Cancelled.")
					return nil
				}
			}

			if err := localstack.Purge(rootDir); err != nil {
				return err
			}
			removed, configPath, err := removeContextFromConfig(opts, contextName)
			if err != nil {
				return err
			}

			fmt.Println("Local stack purged.")
			if removed {
				fmt.Printf("Removed context %q from %s\n", contextName, configPath)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompt")
	return cmd
}

func runLocalSetup(opts *GlobalOptions, dirValue, contextName, grafanaUser, grafanaPassword string, nonInteractive, switchContext bool) error {
	rootDir, err := localstack.ResolveRootDir(dirValue)
	if err != nil {
		return err
	}

	existingUser, existingPassword, err := localstack.LoadGrafanaCredentials(rootDir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(existingUser) == "" {
		existingUser = localstack.DefaultGrafanaAdminUser
	}
	if existingPassword == "" {
		existingPassword = localstack.DefaultGrafanaAdminPassword
	}

	if !nonInteractive {
		fmt.Println("Local Stack Setup")
		fmt.Println("  - Grafana + Loki + Prometheus + Tempo + OTEL Collector")
		fmt.Printf("  - Stack directory: %s\n", rootDir)
		ok, err := promptYesNo("Continue?", true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	grafanaUser = strings.TrimSpace(grafanaUser)
	if grafanaUser == "" {
		grafanaUser = existingUser
	}
	if grafanaPassword == "" {
		grafanaPassword = existingPassword
	}

	if !nonInteractive {
		var err error
		grafanaUser, err = promptStringDefault("Grafana admin username", grafanaUser)
		if err != nil {
			return err
		}
		grafanaPassword, err = promptStringDefault("Grafana admin password", grafanaPassword)
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(grafanaUser) == "" {
		return errors.New("grafana admin username cannot be empty")
	}
	if grafanaPassword == "" {
		return errors.New("grafana admin password cannot be empty")
	}

	fmt.Println("Checking Docker...")
	if err := localstack.CheckDockerReady(); err != nil {
		return err
	}

	if err := localstack.EnsureScaffold(rootDir); err != nil {
		return err
	}
	if err := localstack.WriteGrafanaEnv(rootDir, grafanaUser, grafanaPassword); err != nil {
		return err
	}

	fmt.Println("Starting local stack...")
	if err := localstack.Up(rootDir); err != nil {
		return err
	}

	fmt.Println("Waiting for Grafana...")
	healthCtx, cancelHealth := context.WithTimeout(context.Background(), localGrafanaWaitTimeout)
	defer cancelHealth()
	if err := localstack.WaitForGrafana(healthCtx, localstack.DefaultGrafanaURL); err != nil {
		return err
	}

	fmt.Println("Creating service token...")
	tokenCtx, cancelToken := context.WithTimeout(context.Background(), localTokenCreateTimeout)
	defer cancelToken()
	token, err := localstack.EnsureServiceToken(tokenCtx, localstack.DefaultGrafanaURL, grafanaUser, grafanaPassword, localServiceAccountName)
	if err != nil {
		return err
	}

	name := strings.TrimSpace(contextName)
	if name == "" {
		name = defaultLocalContextName
	}
	configPath, err := writeLocalContext(opts, name, token, switchContext)
	if err != nil {
		return err
	}

	if err := localstack.SaveState(rootDir, localstack.State{
		GrafanaURL:      localstack.DefaultGrafanaURL,
		GrafanaUser:     grafanaUser,
		GrafanaPassword: grafanaPassword,
		GrafanaToken:    token,
		ContextName:     name,
		CreatedAtUTC:    time.Now().UTC(),
	}); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Local observability stack is ready.")
	fmt.Printf("Grafana URL: %s\n", localstack.DefaultGrafanaURL)
	fmt.Printf("Grafana username: %s\n", grafanaUser)
	fmt.Printf("Grafana password: %s\n", grafanaPassword)
	fmt.Printf("Grafana service token: %s\n", token)
	fmt.Printf("OTLP gRPC endpoint: %s\n", localstack.DefaultOTLPGRPCEndpoint)
	fmt.Printf("OTLP HTTP endpoint: %s\n", localstack.DefaultOTLPHTTPEndpoint)
	fmt.Printf("Config updated: %s (context: %s)\n", configPath, name)
	fmt.Println("Try:")
	fmt.Printf("  grafquery --context %s config current\n", name)
	fmt.Printf("  grafquery --context %s logs '{service_name=\"demo\"}' --since 1h\n", name)
	return nil
}

func writeLocalContext(opts *GlobalOptions, contextName, token string, switchContext bool) (string, error) {
	c, path, err := loadConfigFromFlags(opts)
	if err != nil {
		return "", err
	}

	if c.Contexts == nil {
		c.Contexts = map[string]*cfg.Context{}
	}

	c.Contexts[contextName] = &cfg.Context{
		Grafana: cfg.GrafanaConfig{
			URL:   localstack.DefaultGrafanaURL,
			Token: token,
		},
		Sources: map[string]string{
			"logs":    localstack.LokiDatasourceUID,
			"metrics": localstack.PrometheusDatasourceUID,
			"traces":  localstack.TempoDatasourceUID,
		},
		Defaults: cfg.DefaultsConfig{
			Since:  defaultLocalSince,
			Limit:  defaultLocalResultLimit,
			Output: defaultLocalOutput,
			Labels: map[string]string{},
		},
	}

	if switchContext {
		c.CurrentContext = contextName
	}

	if err := cfg.Save(path, c); err != nil {
		return "", err
	}
	return path, nil
}

func removeContextFromConfig(opts *GlobalOptions, contextName string) (bool, string, error) {
	c, path, err := loadConfigFromFlags(opts)
	if err != nil {
		return false, "", err
	}
	if _, ok := c.Contexts[contextName]; !ok {
		return false, path, nil
	}
	delete(c.Contexts, contextName)
	if strings.TrimSpace(c.CurrentContext) == contextName {
		c.CurrentContext = ""
	}
	if err := cfg.Save(path, c); err != nil {
		return false, "", err
	}
	return true, path, nil
}

func promptStringDefault(prompt, fallback string) (string, error) {
	reader := bufio.NewReader(os.Stdin)
	if fallback != "" {
		fmt.Printf("%s [%s]: ", prompt, fallback)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	in := strings.TrimSpace(line)
	if in == "" {
		return fallback, nil
	}
	return in, nil
}

func promptYesNo(prompt string, defaultYes bool) (bool, error) {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("%s %s ", prompt, suffix)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		in := strings.ToLower(strings.TrimSpace(line))
		if in == "" {
			return defaultYes, nil
		}
		if in == "y" || in == "yes" {
			return true, nil
		}
		if in == "n" || in == "no" {
			return false, nil
		}
		fmt.Println("Please enter y or n.")
		if errors.Is(err, io.EOF) {
			return defaultYes, nil
		}
	}
}
