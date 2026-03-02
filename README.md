# Grafana-Query (`grafquery`)

Unified observability CLI for the Grafana stack.

- One token, one config, one tool
- Native query languages remain native (LogQL / PromQL / TraceQL)
- Pipe-friendly output (`json`, `table`, `csv`, `raw`)

## Implemented phases

### Phase 1 — Foundation
- [x] Config contexts: `~/.config/grafquery/config.yaml`
- [x] `grafquery init` with datasource discovery (`/api/datasources`)
- [x] Signal commands: `logs`, `metrics`, `traces`
- [x] Raw datasource query: `grafquery query`

### Phase 2 — Correlation core
- [x] `grafquery correlate --trace-id <id>`
- [x] `grafquery correlate --service <name>`
- [x] Parallel fan-out query execution across logs/metrics/traces

### Phase 3 — Dashboard integration
- [x] `grafquery dash list`
- [x] `grafquery dash run <uid> --panel "..."`

## Install

### One-command install (recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/derekurban/grafana-query/main/install.sh | bash
```

PowerShell (Windows):

```powershell
irm https://raw.githubusercontent.com/derekurban/grafana-query/main/install.ps1 | iex
```

Installer supports secure release verification and controlled fallback options.
See: [`docs/INSTALL.md`](docs/INSTALL.md)

### Go install

```bash
go install github.com/derekurban/grafana-query@latest
```

### npm install

```bash
npm install -g grafquery
```

### Local observability stack (Docker)

```bash
grafquery local setup
```

This interactive setup boots a local Grafana + Loki + Prometheus + Tempo stack, exposes OTLP ingest endpoints, creates a Grafana service token, and writes a ready-to-use `grafquery` context.

## Releasing

Releases are tag-driven via [`.github/workflows/release.yml`](.github/workflows/release.yml).
Pushing a semantic version tag like `v0.1.0` triggers:

- `go test ./...`
- GoReleaser builds and archives for Linux/macOS/Windows (`amd64`, `arm64`)
- `checksums.txt` generation
- Sigstore `cosign` signing of checksums (`checksums.txt.sig`, `checksums.txt.pem`)
- GitHub Release publish with artifacts
- npm publish of `grafquery` when `NPM_TOKEN` is configured in repo secrets

### Create the next tag

Bash:

```bash
./scripts/release/bump-tag.sh --patch
```

PowerShell:

```powershell
./scripts/release/bump-tag.ps1 --patch
```

You can replace `--patch` with `--minor` or `--major`.

## Local stack commands

- `grafquery local setup` (interactive TUI-style bootstrap)
- `grafquery local up`
- `grafquery local down`
- `grafquery local status`
- `grafquery local info` (shows OTLP endpoints + Grafana service token)
- `grafquery local purge` (removes containers, volumes, local files, and local context)

## Quick start

```bash
# initialize

grafquery init --url https://grafana.company.io --token "$GRAFANA_TOKEN" --context-name production

# run queries
grafquery logs '{service="api"} |= "error"' --since 30m
grafquery metrics 'up{job="api"}' --output table
grafquery traces '{ resource.service.name = "api" && duration > 1s }'

# correlate
grafquery correlate --trace-id abc123def --since 30m
grafquery correlate --service api-gateway --since 30m

# dashboards
grafquery dash list
grafquery dash run abc123 --panel "Error Rate"
```

## Config example

```yaml
current-context: production
contexts:
  production:
    grafana:
      url: https://grafana.company.io
      token: ${GRAFANA_TOKEN}
    sources:
      logs: grafanacloud-logs
      metrics: grafanacloud-prom
      traces: grafanacloud-traces
    defaults:
      since: 1h
      limit: 100
      output: auto
      labels:
        cluster: prod-us-east-1
aliases:
  errors: '{level="error"}'
```

## Commands

- `grafquery init`
- `grafquery config current|list|use|set-source`
- `grafquery logs <query>`
- `grafquery metrics <query>`
- `grafquery traces <query>`
- `grafquery traces get <trace-id>`
- `grafquery query <expr> --source <uid|name>`
- `grafquery correlate --trace-id <id>`
- `grafquery correlate --service <svc>`
- `grafquery dash list`
- `grafquery dash run <dashboard-uid>`
- `grafquery local setup|up|down|status|info|purge`

## Notes

- Grafana is the single gateway (`/api/ds/query`), so credentials and audit are centralized.
- `grafquery` is read-only by design.

## License

MIT
