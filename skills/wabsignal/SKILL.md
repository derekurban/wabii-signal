---
name: wabsignal
description: Use when a repo uses `wabsignal` to route OpenTelemetry data into Grafana Cloud and an agent needs to bootstrap project-scoped OTLP env, validate telemetry wiring, or query logs, metrics, traces, and correlations. Use this for app debugging workflows driven by `wabsignal project env`, `run`, `doctor`, `logs`, `metrics`, `traces`, `query`, and `correlate`. Do not use it to perform machine setup; `wabsignal setup` is human-only.
---

# Wabsignal

Use `wabsignal` as the agent-facing control plane for Grafana-backed observability.

Keep one boundary strict:

- Never run `wabsignal setup` yourself.
- If setup is missing or credentials are not present, stop and tell the human to run `wabsignal setup`.

## Quick start

1. Confirm `wabsignal` is installed and machine setup already exists.
2. Resolve the target project and pass it explicitly on read commands.
3. Emit project OTLP env and apply it to the app or test process.
4. Start a run scope before QA, replay, or manual testing.
5. Use `doctor`, `logs`, `metrics`, `traces`, and `correlate` to inspect evidence.

For command recipes and query examples, read [references/command-recipes.md](references/command-recipes.md).

## Human-only boundary

Treat these as operator-managed:

- `wabsignal setup`
- Grafana Cloud org/stack selection
- service account token creation
- full-access policy token creation

When setup is missing, say exactly what the human needs to do:

- Run `wabsignal setup`
- Complete the guided TUI
- Re-run the agent task after setup succeeds

Do not try to fabricate tokens, OTLP endpoints, stack names, or policy settings.

## Agent workflow

### 1. Detect readiness

Run these checks first:

```powershell
wabsignal version --output json
wabsignal doctor --output json
```

Interpretation:

- If `doctor` fails with `run \`wabsignal setup\` first`, stop and hand setup back to the human.
- If setup exists but the target project is unknown, inspect projects with `wabsignal project list --output json`.
- If a project exists but datasource mapping or smoke tests fail, use `doctor` output to decide whether to switch project, set sources, or ask the human for missing write credentials.

### 2. Resolve the project

Read commands require an explicit project. Resolve the target project first:

```powershell
wabsignal project show <project> --output json
```

If the project is missing and the user explicitly wants it created:

- In restrictive mode, request or use the human-provided write token.
- In full-access mode, `project create` can mint the write token automatically if setup is already complete.

Use:

```powershell
wabsignal project create <project-name> <primary-service> [extra-services...] --output json
wabsignal project use <project-name> --output json
```

### 3. Bootstrap app telemetry

Emit environment from the project instead of constructing OTLP values manually:

```powershell
wabsignal project env <project> --format json
```

Important fields:

- `OTEL_EXPORTER_OTLP_ENDPOINT`
- `OTEL_EXPORTER_OTLP_HEADERS`
- `OTEL_SERVICE_NAME`
- `OTEL_RESOURCE_ATTRIBUTES`
- `WABSIGNAL_RUN_ID` when a run is active

Prefer `--format json` for agents that need structured parsing.
Use `--format dotenv`, `shell`, or `powershell` only when writing environment files or shell exports.

Do not rewrite the OTLP auth format yourself unless you are debugging `wabsignal`.

### 4. Scope a debugging session

Before manual QA, browser automation, or reproduction steps, start a run:

```powershell
wabsignal run start --output json
```

Or use a stable explicit ID when coordinating with tests:

```powershell
wabsignal run start qa-checkout-regression --output json
```

Then re-read `project env` so the app/test harness includes the active run ID in emitted telemetry.

### 5. Validate the wiring

After the app emits telemetry, run:

```powershell
wabsignal doctor --output json
```

Use this to verify:

- read token still works
- logs/metrics/traces datasources resolve
- project write token works
- OTLP smoke trace is queryable

If service or run scoping is wrong because the backend uses different field names, adjust project scope with:

```powershell
wabsignal project set-scope <project> --logs-service-label <label> --metrics-service-label <label> --traces-service-attr <attr>
```

### 6. Investigate runtime evidence

Use the highest-level command that matches the task:

- `logs` for log search
- `metrics` for point-in-time or rate checks
- `traces` for trace search or `traces get <trace-id>`
- `correlate` when you want a cross-signal summary
- `query` only as the escape hatch

Default behavior already scopes queries to:

- current project services

Use `--no-project-scope` only when the debugging target is intentionally outside the current project.
If you want only one run, include the run ID explicitly in the query.

## Investigation patterns

### Find recent errors

```powershell
wabsignal --project <project> logs '{} |= "error"' --since 30m --output json
```

### Check whether the service is alive

```powershell
wabsignal --project <project> metrics 'up' --output json
```

### Inspect slow or failing traces

```powershell
wabsignal --project <project> traces '{}' --since 30m --output json
wabsignal --project <project> traces get <trace-id> --since 24h --output json
```

### Pull a cross-signal view

```powershell
wabsignal --project <project> correlate --service <service> --since 15m --output json
wabsignal --project <project> correlate --trace-id <trace-id> --output json
```

## Failure handling

Use this decision rule:

- Missing setup or keyring secrets: stop and hand off to the human.
- Missing project write token in restrictive mode: ask the human for the project write token.
- Datasource mismatch: fix with `project set-source` or `project set-scope`.
- Missing explicit project on a read command: add `--project <name>` instead of relying on the mutable current project.
- Query returned no evidence: broaden time range before dropping project scoping.

## Output discipline

Prefer machine-readable output whenever possible:

- `--output json` for `doctor`, `project` lifecycle, `run`, `logs`, `metrics`, `traces`, `correlate`, and `query`
- `project env --format json` for bootstrap integration

When summarizing to the user, report:

- project name
- run ID
- exact command used
- whether results were project-scoped or `--no-project-scope`
- the most relevant error, trace ID, metric result, or datasource failure

## Known defaults

Default project scoping assumes:

- logs service label: `service_name`
- metrics service label: `service_name`
- traces service attribute: `resource.service.name`

If a repo promotes service identity differently, update the project scope instead of hardcoding different query assumptions into your own commands.
