Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-EnvValue {
  param(
    [Parameter(Mandatory = $true)][string[]]$Names,
    [Parameter()][string]$Default = ""
  )
  foreach ($name in $Names) {
    $value = [Environment]::GetEnvironmentVariable($name)
    if (-not [string]::IsNullOrWhiteSpace($value)) {
      return $value
    }
  }
  return $Default
}

$Repo = Get-EnvValue -Names @("GRAFQUERY_REPO", "GRAFANA_QUERY_REPO") -Default "derekurban/grafana-query"
$OfficialRepo = "derekurban/grafana-query"
$LegacyRepo = "derekurban2001/grafana-query"
$ModulePath = Get-EnvValue -Names @("GRAFQUERY_MODULE_PATH", "GRAFANA_QUERY_MODULE_PATH") -Default "github.com/$Repo"
$BinaryBase = "grafquery"
$BinaryName = "grafquery.exe"
$OwnershipMarkerMagic = "grafquery-owned-binary-v1"

$InstallDir = Get-EnvValue -Names @("GRAFQUERY_INSTALL_DIR", "GRAFANA_QUERY_INSTALL_DIR") -Default (Join-Path $HOME ".local\bin")
$Version = Get-EnvValue -Names @("GRAFQUERY_VERSION", "GRAFANA_QUERY_VERSION") -Default "latest"
$AutoPathRaw = Get-EnvValue -Names @("GRAFQUERY_AUTO_PATH", "GRAFANA_QUERY_AUTO_PATH") -Default "1"
$VerifySignaturesRaw = Get-EnvValue -Names @("GRAFQUERY_VERIFY_SIGNATURES", "GRAFANA_QUERY_VERIFY_SIGNATURES") -Default "1"
$AllowSourceFallbackRaw = Get-EnvValue -Names @("GRAFQUERY_ALLOW_SOURCE_FALLBACK", "GRAFANA_QUERY_ALLOW_SOURCE_FALLBACK") -Default "0"
$AutoInstallGoRaw = Get-EnvValue -Names @("GRAFQUERY_AUTO_INSTALL_GO", "GRAFANA_QUERY_AUTO_INSTALL_GO") -Default "1"
$CosignVersion = Get-EnvValue -Names @("GRAFQUERY_COSIGN_VERSION", "GRAFANA_QUERY_COSIGN_VERSION") -Default "v2.5.3"
$DefaultCosignIdentityRegex = if ($Repo -eq $OfficialRepo -or $Repo -eq $LegacyRepo) {
  "^https://github.com/(derekurban/grafana-query|derekurban2001/grafana-query)/.github/workflows/release.yml@refs/tags/.*$"
} else {
  "^https://github.com/$Repo/.github/workflows/release.yml@refs/tags/.*$"
}
$CosignIdentityRegex = Get-EnvValue -Names @("GRAFQUERY_COSIGN_IDENTITY_RE", "GRAFANA_QUERY_COSIGN_IDENTITY_RE") -Default $DefaultCosignIdentityRegex
$CosignOidcIssuer = Get-EnvValue -Names @("GRAFQUERY_COSIGN_OIDC_ISSUER", "GRAFANA_QUERY_COSIGN_OIDC_ISSUER") -Default "https://token.actions.githubusercontent.com"

function Write-Log {
  param([string]$Message)
  Write-Host "[grafquery-install] $Message"
}

function Write-WarnMessage {
  param([string]$Message)
  Write-Warning "[grafquery-install] $Message"
}

function Fail {
  param([string]$Message)
  throw "[grafquery-install] ERROR: $Message"
}

function Test-Truthy {
  param([string]$Value)
  if ([string]::IsNullOrWhiteSpace($Value)) {
    return $false
  }
  switch ($Value.ToLowerInvariant()) {
    "1" { return $true }
    "true" { return $true }
    "yes" { return $true }
    "on" { return $true }
    default { return $false }
  }
}

function Get-Arch {
  $arch = $null

  $runtimeType = [Type]::GetType("System.Runtime.InteropServices.RuntimeInformation, System.Runtime.InteropServices.RuntimeInformation")
  if ($null -ne $runtimeType) {
    try {
      $prop = $runtimeType.GetProperty("OSArchitecture")
      if ($null -ne $prop) {
        $value = $prop.GetValue($null, $null)
        if ($null -ne $value) {
          $arch = $value.ToString()
        }
      }
    } catch {
      $arch = $null
    }
  }

  if ([string]::IsNullOrWhiteSpace($arch)) {
    if (-not [string]::IsNullOrWhiteSpace($env:PROCESSOR_ARCHITEW6432)) {
      $arch = $env:PROCESSOR_ARCHITEW6432
    } elseif (-not [string]::IsNullOrWhiteSpace($env:PROCESSOR_ARCHITECTURE)) {
      $arch = $env:PROCESSOR_ARCHITECTURE
    }
  }

  if ([string]::IsNullOrWhiteSpace($arch)) {
    Fail "Unable to determine architecture"
  }

  switch ($arch.ToUpperInvariant()) {
    "X64" { return "amd64" }
    "AMD64" { return "amd64" }
    "X86_64" { return "amd64" }
    "ARM64" { return "arm64" }
    "AARCH64" { return "arm64" }
    default { Fail "Unsupported architecture: $arch" }
  }
}

function Refresh-SessionPathFromSystem {
  $machinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
  $userPath = [Environment]::GetEnvironmentVariable("Path", "User")

  if ([string]::IsNullOrWhiteSpace($machinePath) -and [string]::IsNullOrWhiteSpace($userPath)) {
    return
  }
  if ([string]::IsNullOrWhiteSpace($machinePath)) {
    $env:Path = $userPath
    return
  }
  if ([string]::IsNullOrWhiteSpace($userPath)) {
    $env:Path = $machinePath
    return
  }
  $env:Path = "$machinePath;$userPath"
}

function Ensure-GoAvailable {
  if (Get-Command go -ErrorAction SilentlyContinue) {
    return $true
  }

  if (-not (Test-Truthy $AutoInstallGoRaw)) {
    return $false
  }

  Write-Log "Go not found on PATH. Attempting automatic Go install."

  if (Get-Command winget -ErrorAction SilentlyContinue) {
    $wingetAttempts = @(
      @("--id", "GoLang.Go", "-e", "--accept-source-agreements", "--accept-package-agreements", "--silent", "--disable-interactivity"),
      @("--id", "GoLang.Go", "-e", "--scope", "machine", "--accept-source-agreements", "--accept-package-agreements", "--silent", "--disable-interactivity")
    )

    foreach ($args in $wingetAttempts) {
      try {
        & winget install @args
      } catch {
        # Continue to next attempt.
      }
      Refresh-SessionPathFromSystem
      if (Get-Command go -ErrorAction SilentlyContinue) {
        Write-Log "Installed Go using winget"
        return $true
      }
    }
  }

  if (Get-Command scoop -ErrorAction SilentlyContinue) {
    try {
      & scoop install go --no-update-scoop
    } catch {
      # Continue to final failure.
    }
    Refresh-SessionPathFromSystem
    if (Get-Command go -ErrorAction SilentlyContinue) {
      Write-Log "Installed Go using scoop"
      return $true
    }
  }

  return $false
}

function Get-FileSha256 {
  param([Parameter(Mandatory = $true)][string]$Path)
  return (Get-FileHash -Path $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Get-ExpectedChecksum {
  param(
    [Parameter(Mandatory = $true)][string]$ChecksumsFile,
    [Parameter(Mandatory = $true)][string]$AssetName
  )

  foreach ($line in Get-Content -Path $ChecksumsFile) {
    if ($line -match "^([A-Fa-f0-9]{64})\s+(.+)$") {
      $hash = $Matches[1].ToLowerInvariant()
      $name = $Matches[2].Trim()
      if ($name -eq $AssetName) {
        return $hash
      }
    }
  }
  return $null
}

function Get-OwnershipMarkerPath {
  param([Parameter(Mandatory = $true)][string]$BinaryPath)
  $dir = Split-Path -Path $BinaryPath -Parent
  $name = Split-Path -Path $BinaryPath -Leaf
  return Join-Path $dir ".$name.grafquery-owner"
}

function Write-OwnershipMarker {
  param([Parameter(Mandatory = $true)][string]$BinaryPath)

  if (-not (Test-Path $BinaryPath)) {
    return
  }

  $resolvedBinary = (Resolve-Path -Path $BinaryPath).Path
  $markerPath = Get-OwnershipMarkerPath -BinaryPath $resolvedBinary
  $timestamp = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

  @(
    $OwnershipMarkerMagic
    "path=$resolvedBinary"
    "repo=$Repo"
    "installed_at=$timestamp"
  ) | Set-Content -Path $markerPath -Encoding UTF8

  Write-Log "Wrote ownership marker $markerPath"
}

function Ensure-Cosign {
  param(
    [Parameter(Mandatory = $true)][string]$Arch,
    [Parameter(Mandatory = $true)][string]$TempDir
  )

  $existing = Get-Command cosign -ErrorAction SilentlyContinue
  if ($null -ne $existing -and $existing.Source) {
    return $existing.Source
  }

  $asset = "cosign-windows-$Arch.exe"
  $url = "https://github.com/sigstore/cosign/releases/download/$CosignVersion/$asset"
  $outFile = Join-Path $TempDir $asset

  Write-Log "cosign not found; downloading $CosignVersion (windows/$Arch)"
  Invoke-Download -Url $url -OutFile $outFile
  return $outFile
}

function Verify-ChecksumsSignature {
  param(
    [Parameter(Mandatory = $true)][string]$Arch,
    [Parameter(Mandatory = $true)][string]$TempDir,
    [Parameter(Mandatory = $true)][string]$ChecksumsPath,
    [Parameter(Mandatory = $true)][string]$SignaturePath,
    [Parameter(Mandatory = $true)][string]$CertificatePath
  )

  if (-not (Test-Truthy $VerifySignaturesRaw)) {
    Write-WarnMessage "Signature verification disabled via GRAFQUERY_VERIFY_SIGNATURES=0"
    return
  }

  $cosignBin = Ensure-Cosign -Arch $Arch -TempDir $TempDir
  & $cosignBin verify-blob `
    --certificate $CertificatePath `
    --signature $SignaturePath `
    --certificate-identity-regexp $CosignIdentityRegex `
    --certificate-oidc-issuer $CosignOidcIssuer `
    $ChecksumsPath | Out-Null
  if ($LASTEXITCODE -ne 0) {
    Fail "Signature verification failed for checksums.txt"
  }
}

function Invoke-Download {
  param(
    [Parameter(Mandatory = $true)][string]$Url,
    [Parameter(Mandatory = $true)][string]$OutFile
  )
  try {
    Invoke-WebRequest -Uri $Url -OutFile $OutFile -ErrorAction Stop
  } catch {
    $status = $null
    $desc = $null
    try {
      if ($null -ne $_.Exception.Response) {
        $status = [int]$_.Exception.Response.StatusCode
        $desc = [string]$_.Exception.Response.StatusDescription
      }
    } catch {
      $status = $null
    }

    if ($null -ne $status) {
      Fail "Download failed ($status $desc): $Url"
    }
    Fail "Download failed: $Url ($($_.Exception.Message))"
  }
}

function Get-LatestTag {
  $api = "https://api.github.com/repos/$Repo/releases/latest"
  try {
    $release = Invoke-RestMethod -Uri $api
    if ($null -ne $release -and $release.tag_name) {
      return [string]$release.tag_name
    }
  } catch {
    return $null
  }
  return $null
}

function New-TempDir {
  $tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("profilex-install-" + [System.Guid]::NewGuid().ToString("N"))
  New-Item -Path $tempDir -ItemType Directory -Force | Out-Null
  return $tempDir
}

function Install-FromRelease {
  param(
    [Parameter(Mandatory = $true)][string]$ResolvedVersion,
    [Parameter(Mandatory = $true)][string]$Arch
  )

  $verNoV = $ResolvedVersion.TrimStart("v")
  $base = "https://github.com/$Repo/releases/download/$ResolvedVersion"
  $assets = @(
    @{ Name = "${BinaryBase}_${verNoV}_windows_${Arch}.zip"; Type = "zip" },
    @{ Name = "${BinaryBase}_${verNoV}_windows_${Arch}.tar.gz"; Type = "tar.gz" }
  )

  $tempDir = New-TempDir
  try {
    try {
      Write-Log "Trying GitHub release install: $ResolvedVersion (windows/$Arch)"
      $checksumsPath = Join-Path $tempDir "checksums.txt"
      $checksumsSigPath = Join-Path $tempDir "checksums.txt.sig"
      $checksumsCertPath = Join-Path $tempDir "checksums.txt.pem"

      Invoke-Download -Url "$base/checksums.txt" -OutFile $checksumsPath
      if (Test-Truthy $VerifySignaturesRaw) {
        Invoke-Download -Url "$base/checksums.txt.sig" -OutFile $checksumsSigPath
        Invoke-Download -Url "$base/checksums.txt.pem" -OutFile $checksumsCertPath
        Verify-ChecksumsSignature `
          -Arch $Arch `
          -TempDir $tempDir `
          -ChecksumsPath $checksumsPath `
          -SignaturePath $checksumsSigPath `
          -CertificatePath $checksumsCertPath
      } else {
        Write-WarnMessage "Signature verification disabled via GRAFQUERY_VERIFY_SIGNATURES=0"
      }

      foreach ($asset in $assets) {
        $assetPath = Join-Path $tempDir $asset.Name
        $assetUrl = "$base/$($asset.Name)"
        try {
          Invoke-Download -Url $assetUrl -OutFile $assetPath
        } catch {
          continue
        }

        $expectedHash = Get-ExpectedChecksum -ChecksumsFile $checksumsPath -AssetName $asset.Name
        if ([string]::IsNullOrWhiteSpace($expectedHash)) {
          Fail "No checksum entry found for $($asset.Name)"
        }
        $actualHash = Get-FileSha256 -Path $assetPath
        if ($expectedHash -ne $actualHash) {
          Fail "Checksum mismatch for $($asset.Name)"
        }

        $extractDir = Join-Path $tempDir ("extract-" + $asset.Type.Replace(".", "-"))
        New-Item -Path $extractDir -ItemType Directory -Force | Out-Null

        if ($asset.Type -eq "zip") {
          Expand-Archive -Path $assetPath -DestinationPath $extractDir -Force
        } else {
          if (-not (Get-Command tar -ErrorAction SilentlyContinue)) {
            Write-WarnMessage "tar not found; cannot extract $($asset.Name)"
            continue
          }
          & tar -xzf $assetPath -C $extractDir | Out-Null
        }

        $candidate = Get-ChildItem -Path $extractDir -Recurse -File |
          Where-Object { $_.Name -ieq $BinaryName -or $_.Name -ieq $BinaryBase } |
          Select-Object -First 1
        if ($null -eq $candidate) {
          continue
        }

        New-Item -Path $InstallDir -ItemType Directory -Force | Out-Null
        $dest = Join-Path $InstallDir $BinaryName
        Copy-Item -Path $candidate.FullName -Destination $dest -Force
        Write-OwnershipMarker -BinaryPath $dest
        Write-Log "Installed $BinaryName to $dest"
        return $true
      }

      Write-WarnMessage "Release asset not found for windows/$Arch"
      return $false
    } catch {
      Write-WarnMessage "Release install failed: $($_.Exception.Message)"
      return $false
    }
  } finally {
    Remove-Item -Path $tempDir -Recurse -Force -ErrorAction SilentlyContinue
  }
}

function Install-WithGo {
  param([Parameter(Mandatory = $true)][string]$RequestedVersion)

  if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    if (-not (Ensure-GoAvailable)) {
      Fail "go is required for fallback install because no matching release binary was found. Install Go or publish a release tag (for example v0.1.0)."
    }
  }

  Write-Log "Falling back to go install"
  $pkgVersion = if ($RequestedVersion -eq "latest" -or [string]::IsNullOrWhiteSpace($RequestedVersion)) { "latest" } else { $RequestedVersion }

  $env:GO111MODULE = "on"
  & go install "$ModulePath@$pkgVersion"
  if ($LASTEXITCODE -ne 0) {
    Fail "go install failed"
  }

  $gobin = (& go env GOBIN).Trim()
  if ([string]::IsNullOrWhiteSpace($gobin)) {
    $gopath = (& go env GOPATH).Trim()
    if ([string]::IsNullOrWhiteSpace($gopath)) {
      Fail "Unable to resolve GOPATH from go env"
    }
    $gobin = Join-Path $gopath "bin"
  }

  $source = @(
    (Join-Path $gobin $BinaryName),
    (Join-Path $gobin $BinaryBase)
  ) | Where-Object { Test-Path $_ } | Select-Object -First 1

  if ($null -eq $source) {
    Fail "go install completed but binary not found in $gobin"
  }

  New-Item -Path $InstallDir -ItemType Directory -Force | Out-Null
  $dest = Join-Path $InstallDir $BinaryName
  Copy-Item -Path $source -Destination $dest -Force
  Write-OwnershipMarker -BinaryPath $dest
  Write-Log "Installed $BinaryName to $dest"
}

function Normalize-PathEntry {
  param([string]$Entry)
  if ([string]::IsNullOrWhiteSpace($Entry)) {
    return ""
  }
  return $Entry.Trim().TrimEnd("\")
}

function Ensure-PathContainsInstallDir {
  param([Parameter(Mandatory = $true)][string]$Dir)

  if (-not (Test-Truthy $AutoPathRaw)) {
    return
  }

  $normalizedDir = Normalize-PathEntry -Entry $Dir
  $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
  $userEntries = @()
  if (-not [string]::IsNullOrWhiteSpace($userPath)) {
    $userEntries = $userPath -split ";"
  }

  $userHasDir = $false
  foreach ($entry in $userEntries) {
    if ((Normalize-PathEntry -Entry $entry) -ieq $normalizedDir) {
      $userHasDir = $true
      break
    }
  }

  if (-not $userHasDir) {
    $newUserPath = if ([string]::IsNullOrWhiteSpace($userPath)) { $Dir } else { "$userPath;$Dir" }
    [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
    Write-Log "Added $Dir to User PATH"
  } else {
    Write-Log "User PATH already contains $Dir"
  }

  $sessionHasDir = $false
  foreach ($entry in ($env:Path -split ";")) {
    if ((Normalize-PathEntry -Entry $entry) -ieq $normalizedDir) {
      $sessionHasDir = $true
      break
    }
  }

  if (-not $sessionHasDir) {
    $env:Path = "$Dir;$env:Path"
    Write-Log "Added $Dir to PATH for current PowerShell session"
  }
}

function Main {
  $arch = Get-Arch
  $allowSourceFallback = Test-Truthy $AllowSourceFallbackRaw

  $resolvedVersion = $Version
  if ($Version -eq "latest") {
    $latest = Get-LatestTag
    if ([string]::IsNullOrWhiteSpace($latest)) {
      if ($allowSourceFallback) {
        Write-WarnMessage "Could not resolve latest release tag; using go install fallback"
        $resolvedVersion = $null
      } else {
        Fail "Could not resolve latest release tag and source fallback is disabled (set GRAFQUERY_ALLOW_SOURCE_FALLBACK=1 to enable)."
      }
    } else {
      $resolvedVersion = $latest
    }
  }

  $installedFromRelease = $false
  if (-not [string]::IsNullOrWhiteSpace($resolvedVersion) -and $resolvedVersion -ne "latest") {
    $installedFromRelease = Install-FromRelease -ResolvedVersion $resolvedVersion -Arch $arch
  }
  if (-not $installedFromRelease) {
    if ($allowSourceFallback) {
      Write-WarnMessage "Release install failed; using go install fallback"
      Install-WithGo -RequestedVersion $Version
    } else {
      Fail "Release install failed and source fallback is disabled (set GRAFQUERY_ALLOW_SOURCE_FALLBACK=1 to enable)."
    }
  }

  Ensure-PathContainsInstallDir -Dir $InstallDir

  if (-not (Get-Command $BinaryBase -ErrorAction SilentlyContinue)) {
    Write-WarnMessage "$BinaryBase is installed but not currently available in this shell. Open a new terminal and try again."
  }

  Write-Log "Done"
  Write-Log "Run: $BinaryBase --help"
}

Main
