# wabii-signal (`wabsignal`)

Hosted Grafana CLI for application debugging evidence.

- Grafana HTTP API for reads
- OTLP endpoint for writes
- Machine-global hosted setup with per-project write tokens
- Agent-friendly CLI for logs, metrics, traces, and correlation

## Install

### One-command install

```bash
curl -fsSL https://raw.githubusercontent.com/derekurban/wabii-signal/main/install.sh | bash
```

PowerShell:

```powershell
irm https://raw.githubusercontent.com/derekurban/wabii-signal/main/install.ps1 | iex
```

See [docs/INSTALL.md](docs/INSTALL.md) for environment variables and fallback behavior.

### Go install

```bash
go install github.com/derekurban/wabii-signal@latest
```

### npm install

```bash
npm install -g wabsignal
```

## Hosted setup

`wabsignal setup` is required before any project or query commands can run.

Important: setup is a human-only machine bootstrap step. A human operator
should run it once on the machine and confirm the credentials. After that,
agents can use `project env`, `run`, `doctor`, `logs`, `metrics`, `traces`,
`query`, and `correlate`.

For the read plane, use a Grafana stack service-account token for Grafana HTTP
API access. Do not use a Grafana Cloud access-policy token as the setup read
token.

When run in an interactive terminal, `wabsignal setup` now launches a guided
TUI wizard by default. Use `--non-interactive` only for explicit operator
automation where every required flag is already known.

The setup wizard now includes explicit guidance on where to find each required
Grafana Cloud value. It starts with the Grafana organization ID and then walks
the operator through:

- `https://grafana.com/orgs/<org id>`
- `https://grafana.com/orgs/<org id>/access-policies` in full-access mode
- `https://<org id>.grafana.net/org/serviceaccounts/create`

From there it asks the human to copy:

- stack name
- OTLP endpoint
- OTLP instance ID
- Viewer service-account token
- full-access policy token when applicable

In full-access mode, `wabsignal` derives the stack ID from the OTLP instance ID
and derives the Cloud region from the OTLP endpoint when possible.

Use either the stack URL or the stack name:

```bash
wabsignal setup \
  --mode restrictive \
  --stack-name your-stack-id \
  --otlp-endpoint https://otlp-gateway-prod-us-central-0.grafana.net/otlp \
  --otlp-instance-id 123456 \
  --query-token "$GRAFANA_SERVICE_ACCOUNT_TOKEN"
```

If you already have the full read endpoint base URL, use:

```bash
wabsignal setup \
  --mode restrictive \
  --grafana-api-url https://your-stack-id.grafana.net/api/ds/query \
  --otlp-endpoint https://otlp-gateway-prod-us-central-0.grafana.net/otlp \
  --otlp-instance-id 123456 \
  --query-token "$GRAFANA_SERVICE_ACCOUNT_TOKEN"
```

`wabsignal` normalizes that URL to the stack base URL and queries Grafana through `POST https://<stack>.grafana.net/api/ds/query`.

### Full-access mode

In `full-access`, setup also stores a Grafana Cloud access-policy management token in the OS keyring and requires the Cloud stack ID and region:

```bash
wabsignal setup \
  --mode full-access \
  --stack-name your-stack-id \
  --otlp-endpoint https://otlp-gateway-prod-us-central-0.grafana.net/otlp \
  --otlp-instance-id 123456 \
  --query-token "$GRAFANA_SERVICE_ACCOUNT_TOKEN" \
  --policy-token "$GRAFANA_CLOUD_POLICY_TOKEN" \
  --cloud-stack-id 654321 \
  --cloud-region us
```

`wabsignal setup --output json` emits machine-readable setup state. In `full-access`, setup also performs an OTLP smoke test using a temporary managed write token. In `restrictive`, OTLP write validation is deferred until a project write token is attached.

## Project workflow

Create a project with one primary write target and optional extra read-scope services:

```bash
wabsignal project create shop-api shop-api shop-worker shop-web
```

In restrictive mode, provide a write token directly or let the command prompt for it:

```bash
wabsignal project create shop-api shop-api --write-token "$GRAFANA_WRITE_TOKEN"
```

Emit bootstrap variables for a local app:

```bash
wabsignal project env shop-api --format dotenv
```

That prints:

- `OTEL_EXPORTER_OTLP_ENDPOINT`
- `OTEL_EXPORTER_OTLP_HEADERS`
- `OTEL_SERVICE_NAME`
- `OTEL_RESOURCE_ATTRIBUTES`

Useful lifecycle commands:

```bash
wabsignal doctor
wabsignal run start
wabsignal run show
wabsignal run stop
wabsignal project set-source shop-api traces <tempo-uid>
wabsignal project set-scope shop-api --logs-service-label service_name
wabsignal project delete shop-api --yes
```

Most lifecycle commands also support `--output json` for agent-friendly automation.

## Query workflow

```bash
wabsignal --project shop-api logs '{} |= "error"' --since 30m
wabsignal --project shop-api metrics 'sum(rate(http_server_duration_seconds_count[5m]))'
wabsignal --project shop-api traces '{}'
wabsignal --project shop-api correlate --trace-id 4f4a6e3f7b1f4c9c
```

Read commands require `--project <name>` and are scoped to that project's primary and extra services. Use `--no-project-scope` only when you intentionally want to bypass that.

If a run scope is active through `wabsignal run start`, `project env` includes `WABSIGNAL_RUN_ID` so the app can stamp telemetry for that debugging session. Generic reads stay project-scoped by default; filter on the run ID explicitly when you want only one run.

## Notes

- Read access uses a Grafana service account token stored in the OS keyring.
- Full-access mode stores the Grafana Cloud policy-management token in the OS keyring.
- Per-project write tokens are stored in the config file by design.
- Grafana Cloud label policies constrain read scopes for logs and metrics, not write scopes. `wabsignal` enforces write intent through the emitted OTEL identity and CLI-side project scoping.

## Releasing

Releases are tag-driven through [`.github/workflows/release.yml`](.github/workflows/release.yml) and [`.goreleaser.yaml`](.goreleaser.yaml).

## License

MIT
