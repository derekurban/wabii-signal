[CmdletBinding()]
param(
	[string]$ConfigPath = "",
	[string]$StackDir = "",
	[switch]$UseInstaller,
	[switch]$SkipInstall,
	[switch]$PurgeFirst,
	[switch]$Cleanup
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$contextName = "local-e2e"
Set-Location $repoRoot

if ([string]::IsNullOrWhiteSpace($ConfigPath)) {
	$ConfigPath = Join-Path $repoRoot ".tmp\e2e\config.yaml"
}
if ([string]::IsNullOrWhiteSpace($StackDir)) {
	$StackDir = Join-Path $repoRoot ".tmp\e2e\stack"
}

$configDir = Split-Path -Parent $ConfigPath
if (-not [string]::IsNullOrWhiteSpace($configDir)) {
	New-Item -ItemType Directory -Path $configDir -Force | Out-Null
}

function Write-Step {
	param([string]$Text)
	Write-Host "==> $Text"
}

function New-RandomHex {
	param([int]$ByteCount)
	$bytes = New-Object byte[] $ByteCount
	[System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
	return ([System.BitConverter]::ToString($bytes)).Replace("-", "").ToLowerInvariant()
}

function Wait-ForCondition {
	param(
		[scriptblock]$Condition,
		[int]$TimeoutSeconds,
		[int]$IntervalSeconds,
		[string]$Description
	)
	$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
	$lastError = $null
	while ((Get-Date) -lt $deadline) {
		try {
			$value = & $Condition
			if ($null -ne $value) {
				return $value
			}
		} catch {
			$lastError = $_
		}
		Start-Sleep -Seconds $IntervalSeconds
	}
	if ($null -ne $lastError) {
		throw "Timed out waiting for $Description. Last error: $($lastError.Exception.Message)"
	}
	throw "Timed out waiting for $Description."
}

$binDir = Join-Path $repoRoot ".tmp\e2e\bin"
$localBin = Join-Path $binDir "grafquery.exe"

if (-not $SkipInstall) {
	if ($UseInstaller) {
		Write-Step "Installing grafquery via install.ps1"
		$env:GRAFQUERY_AUTO_PATH = "0"
		$env:GRAFQUERY_ALLOW_SOURCE_FALLBACK = "1"
		& (Join-Path $repoRoot "install.ps1")
		if ($LASTEXITCODE -ne 0) {
			throw "install.ps1 failed with exit code $LASTEXITCODE"
		}
	} else {
		Write-Step "Building grafquery from current repo"
		New-Item -ItemType Directory -Path $binDir -Force | Out-Null
		& go build -o $localBin .
		if ($LASTEXITCODE -ne 0) {
			throw "go build failed with exit code $LASTEXITCODE"
		}
	}
}

$grafqueryBin = if ($UseInstaller) {
	Join-Path $HOME ".local\bin\grafquery.exe"
} else {
	$localBin
}
if (-not (Test-Path $grafqueryBin)) {
	throw "grafquery binary not found at $grafqueryBin"
}

Write-Step "Using grafquery binary: $grafqueryBin"

if ($PurgeFirst) {
	Write-Step "Purging previous local stack state"
	& $grafqueryBin --config $ConfigPath local purge --dir $StackDir --yes | Out-Null
}

Write-Step "Running local setup (Grafana + Loki + Prometheus + Tempo)"
& $grafqueryBin --config $ConfigPath local setup --dir $StackDir --context-name $contextName --non-interactive --switch-context --grafana-user admin --grafana-password password | Out-Null

$statePath = Join-Path $StackDir "state.json"
if (-not (Test-Path $statePath)) {
	throw "Expected local stack state at $statePath"
}
$state = Get-Content $statePath | ConvertFrom-Json

Write-Step "Authenticating against emitted Grafana URL with service token"
$headers = @{ Authorization = "Bearer $($state.grafana_token)" }
$dataSources = Invoke-RestMethod -Method Get -Uri "$($state.grafana_url)/api/datasources" -Headers $headers
$requiredUIDs = @("local-loki", "local-prometheus", "local-tempo")
$presentUIDs = @($dataSources | ForEach-Object { $_.uid })
foreach ($uid in $requiredUIDs) {
	if ($presentUIDs -notcontains $uid) {
		throw "Datasource $uid not found via token-authenticated request."
	}
}

Write-Step "Emitting metric + trace to OTLP HTTP endpoint"
$otlpHTTP = "http://localhost:4318"
$lokiPushURL = "http://localhost:13100/loki/api/v1/push"
$lokiLabelsURL = "http://localhost:13100/loki/api/v1/labels"
$now = [DateTimeOffset]::UtcNow
$runID = "run-$($now.ToUnixTimeSeconds())"
$appLabel = "grafquery-e2e"
$traceID = New-RandomHex -ByteCount 16
$spanID = New-RandomHex -ByteCount 8
$startNano = [string]($now.ToUnixTimeMilliseconds() * 1000000)
$endNano = [string](($now.AddMilliseconds(500)).ToUnixTimeMilliseconds() * 1000000)
$metricNano = [string](($now.AddMilliseconds(200)).ToUnixTimeMilliseconds() * 1000000)
$logNano = [string](($now.AddMilliseconds(300)).ToUnixTimeMilliseconds() * 1000000)

$traceBody = @{
	resourceSpans = @(
		@{
			resource = @{ attributes = @(@{ key = "service.name"; value = @{ stringValue = "grafquery-e2e" } }) }
			scopeSpans = @(
				@{
					scope = @{ name = "grafquery-e2e" }
					spans = @(
						@{
							traceId = $traceID
							spanId = $spanID
							name = "grafquery-e2e-span"
							kind = 1
							startTimeUnixNano = $startNano
							endTimeUnixNano = $endNano
							attributes = @(
								@{ key = "test.run_id"; value = @{ stringValue = $runID } }
							)
						}
					)
				}
			)
		}
	)
}

$metricBody = @{
	resourceMetrics = @(
		@{
			resource = @{ attributes = @(@{ key = "service.name"; value = @{ stringValue = "grafquery-e2e" } }) }
			scopeMetrics = @(
				@{
					scope = @{ name = "grafquery-e2e" }
					metrics = @(
						@{
							name = "grafquery_e2e_metric"
							gauge = @{
								dataPoints = @(
									@{
										attributes = @(
											@{ key = "test_run"; value = @{ stringValue = $runID } }
										)
										asDouble = 42.0
										timeUnixNano = $metricNano
									}
								)
							}
						}
					)
				}
			)
		}
	)
}

$null = Invoke-WebRequest -Method Post -Uri "$otlpHTTP/v1/traces" -ContentType "application/json" -Body ($traceBody | ConvertTo-Json -Depth 25 -Compress) -UseBasicParsing
$null = Invoke-WebRequest -Method Post -Uri "$otlpHTTP/v1/metrics" -ContentType "application/json" -Body ($metricBody | ConvertTo-Json -Depth 25 -Compress) -UseBasicParsing

Write-Step "Pushing labeled logs to Loki"
$lokiValues = ,@($logNano, "grafquery e2e log run_id=$runID")
$lokiBody = @{
	streams = @(
		@{
			stream = @{
				app = $appLabel
				test_run = $runID
				source = "grafquery-e2e-smoke"
			}
			values = $lokiValues
		}
	)
}
$null = Invoke-WebRequest -Method Post -Uri $lokiPushURL -ContentType "application/json" -Body ($lokiBody | ConvertTo-Json -Depth 10 -Compress) -UseBasicParsing

function Invoke-GrafqueryJson {
	param([string[]]$CliArgs)
	$output = & $grafqueryBin --config $ConfigPath --context $contextName --output json @CliArgs 2>&1
	$exitCode = $LASTEXITCODE
	$text = ($output -join "`n").Trim()
	if ($exitCode -ne 0) {
		throw "grafquery failed with exit code $exitCode.`n$text"
	}
	if ([string]::IsNullOrWhiteSpace($text)) {
		return @()
	}
	try {
		return $text | ConvertFrom-Json
	} catch {
		throw "Expected JSON for arguments [$($CliArgs -join ' ')], got:`n$text"
	}
}

Write-Step "Waiting for metric to appear via grafquery metrics"
$metricRows = Wait-ForCondition -Condition {
	$rows = Invoke-GrafqueryJson -CliArgs @("metrics", "grafquery_e2e_metric", "--since", "30m", "--instant=false")
	$rowsArray = @($rows)
	if ($rowsArray.Count -gt 0) {
		return $rowsArray
	}
	return $null
} -TimeoutSeconds 120 -IntervalSeconds 3 -Description "metric query results"

Write-Step "Waiting for trace to appear via grafquery traces get"
$traceRows = Wait-ForCondition -Condition {
	$rows = Invoke-GrafqueryJson -CliArgs @("traces", "get", $traceID, "--since", "30m", "--limit", "20")
	$rowsArray = @($rows)
	if ($rowsArray.Count -gt 0) {
		return $rowsArray
	}
	return $null
} -TimeoutSeconds 120 -IntervalSeconds 3 -Description "trace query results"

Write-Step "Waiting for Loki labels to include app/test_run"
$labelNames = Wait-ForCondition -Condition {
	$resp = Invoke-RestMethod -Method Get -Uri $lokiLabelsURL
	$names = @($resp.data)
	if ($names -contains "app" -and $names -contains "test_run") {
		return $names
	}
	return $null
} -TimeoutSeconds 60 -IntervalSeconds 2 -Description "Loki label index"

$logSelector = "{app=""$appLabel"",test_run=""$runID""}"
Write-Step "Waiting for log rows via Grafana Loki query $logSelector"
$logRows = Wait-ForCondition -Condition {
	$body = @{
		from = "now-30m"
		to = "now"
		queries = @(
			@{
				refId = "A"
				datasource = @{
					uid = "local-loki"
					type = "loki"
				}
				expr = $logSelector
				queryType = "range"
				maxLines = 50
			}
		)
	} | ConvertTo-Json -Depth 20 -Compress
	$resp = Invoke-RestMethod -Method Post -Uri "$($state.grafana_url)/api/ds/query" -Headers $headers -ContentType "application/json" -Body $body
	$frames = @($resp.results.A.frames)
	if ($frames.Count -eq 0) {
		return $null
	}
	$first = $frames[0]
	$values = @($first.data.values)
	if ($values.Count -lt 1) {
		return $null
	}
	$rowCount = @($values[0]).Count
	if ($rowCount -gt 0) {
		return $rowCount
	}
	return $null
} -TimeoutSeconds 120 -IntervalSeconds 3 -Description "Loki label selector query results"

Write-Host ""
Write-Host "PASS: local setup + token auth + metric query + trace query + log label query"
Write-Host "Grafana URL: $($state.grafana_url)"
Write-Host "Metric rows: $(@($metricRows).Count)"
Write-Host "Trace rows: $(@($traceRows).Count)"
Write-Host "Log rows: $logRows"
Write-Host "Loki labels include app/test_run: $(([string[]]$labelNames) -contains 'app' -and ([string[]]$labelNames) -contains 'test_run')"
Write-Host "run_id: $runID"
Write-Host "trace_id: $traceID"

if ($Cleanup) {
	Write-Step "Stopping local stack (--cleanup requested)"
	& $grafqueryBin --config $ConfigPath local down --dir $StackDir | Out-Null
}
