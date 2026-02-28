# Installation

## One-command install

```bash
curl -fsSL https://raw.githubusercontent.com/derekurban/grafana-query/main/install.sh | bash
```

PowerShell:

```powershell
irm https://raw.githubusercontent.com/derekurban/grafana-query/main/install.ps1 | iex
```

The installer will:

1. Try to download a matching binary from GitHub Releases
2. Verify checksums (`checksums.txt`)
3. Verify signed checksums with Sigstore/cosign (`checksums.txt.sig` + `checksums.txt.pem`)
4. Install to your user-local bin directory and optionally update PATH

---

## Installer environment variables

- `GRAFQUERY_INSTALL_DIR` – install destination (default `~/.local/bin`)
- `GRAFQUERY_VERSION` – `latest` (default) or specific tag (e.g. `v0.2.0`)
- `GRAFQUERY_AUTO_PATH` – `1` default, add install dir to PATH; set `0` to disable
- `GRAFQUERY_VERIFY_SIGNATURES` – `1` default, enforce cosign verification; set `0` to disable
- `GRAFQUERY_ALLOW_SOURCE_FALLBACK` – `0` default; set `1` to allow `go install` fallback when no release binary exists
- `GRAFQUERY_COSIGN_VERSION` – cosign version if cosign is not already installed (default `v2.5.3`)
- `GRAFQUERY_COSIGN_IDENTITY_RE` – cert identity regex override for cosign verification
- `GRAFQUERY_COSIGN_OIDC_ISSUER` – OIDC issuer override (default `https://token.actions.githubusercontent.com`)

Compatibility aliases are also accepted:

- `GRAFANA_QUERY_*` (same semantics as `GRAFQUERY_*`)

---

## Fallback install (source)

If you want fallback to source install (when release artifacts are unavailable):

```bash
GRAFQUERY_ALLOW_SOURCE_FALLBACK=1 \
  curl -fsSL https://raw.githubusercontent.com/derekurban/grafana-query/main/install.sh | bash
```

---

## Manual install from source

```bash
git clone https://github.com/derekurban/grafana-query.git
cd grafana-query
go build -o grafquery .
install -m 0755 grafquery ~/.local/bin/grafquery
```

---

## Verify install

```bash
grafquery --help
grafquery version
```
