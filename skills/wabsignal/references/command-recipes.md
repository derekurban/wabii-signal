# Command Recipes

Use these as starting points. Prefer `--output json` for agent use.

## Detect readiness

```powershell
wabsignal version --output json
wabsignal doctor --output json
wabsignal project list --output json
```

## Create or switch project

```powershell
wabsignal project create shop-api shop-api shop-worker shop-web --output json
wabsignal project create shop-api shop-api --write-token "$GRAFANA_WRITE_TOKEN" --output json
wabsignal project use shop-api --output json
wabsignal project show shop-api --output json
```

## Emit OTLP environment

```powershell
wabsignal project env shop-api --format json
wabsignal project env shop-api --format dotenv
wabsignal project env shop-api --format powershell
```

## Run scoping

```powershell
wabsignal run start --output json
wabsignal run start qa-pass-12 --output json
wabsignal run show --output json
wabsignal run stop --output json
```

## Query logs, metrics, traces

```powershell
wabsignal --project shop-api logs '{} |= "error"' --since 30m --output json
wabsignal --project shop-api logs '{} |= "panic"' --watch 5s
wabsignal --project shop-api metrics 'sum(rate(http_server_duration_seconds_count[5m]))' --output json
wabsignal --project shop-api metrics 'up' --watch 10s
wabsignal --project shop-api traces '{}' --since 30m --output json
wabsignal --project shop-api traces get 4f4a6e3f7b1f4c9c --since 24h --output json
```

## Correlate evidence

```powershell
wabsignal --project shop-api correlate --service shop-api --since 15m --output json
wabsignal --project shop-api correlate --trace-id 4f4a6e3f7b1f4c9c --output json
```

## Override datasource or scope mapping

```powershell
wabsignal project set-source shop-api traces <tempo-uid>
wabsignal project set-scope shop-api --logs-service-label service_name
wabsignal project set-scope shop-api --metrics-service-label service_name
wabsignal project set-scope shop-api --traces-service-attr resource.service.name
```

## Escape hatch

Use `query` only when the signal-specific commands are not enough.

```powershell
wabsignal query '{}' --source grafanacloud-traces --query-type range --output json
```
