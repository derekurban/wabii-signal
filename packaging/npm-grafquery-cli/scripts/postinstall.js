const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const https = require("node:https");
const { spawnSync } = require("node:child_process");

const repo = process.env.GRAFQUERY_REPO || process.env.GRAFANA_QUERY_REPO || "derekurban/grafana-query";
const packageJsonPath = path.join(__dirname, "..", "package.json");
const pkg = JSON.parse(fs.readFileSync(packageJsonPath, "utf8"));
const version = pkg.version;

const goos = mapOs(process.platform);
const goarch = mapArch(process.arch);
const ext = goos === "windows" ? "zip" : "tar.gz";
const binaryName = goos === "windows" ? "grafquery.exe" : "grafquery";
const asset = `grafquery_${version}_${goos}_${goarch}.${ext}`;
const url = `https://github.com/${repo}/releases/download/v${version}/${asset}`;

const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), "grafquery-npm-"));
const archivePath = path.join(tempDir, asset);
const extractDir = path.join(tempDir, "extract");
const outputBinaryPath = path.join(__dirname, "..", "bin", binaryName);
const userBinDir = resolveUserBinDir();

main().catch((err) => {
  console.error(`[grafquery npm] ${err.message}`);
  process.exit(1);
});

async function main() {
  fs.mkdirSync(extractDir, { recursive: true });
  fs.mkdirSync(path.dirname(outputBinaryPath), { recursive: true });

  console.log(`[grafquery npm] Downloading ${url}`);
  await download(url, archivePath);
  extractArchive(archivePath, extractDir, goos);

  const foundBinary = findFileRecursive(extractDir, binaryName);
  if (!foundBinary) {
    throw new Error(`Binary ${binaryName} not found in archive ${asset}`);
  }

  fs.copyFileSync(foundBinary, outputBinaryPath);
  try {
    fs.chmodSync(outputBinaryPath, 0o755);
  } catch {
    // Best effort for non-POSIX environments.
  }

  console.log(`[grafquery npm] Installed package-local ${binaryName}`);

  installToUserBin(outputBinaryPath);
  ensurePathContainsUserBin(userBinDir);
}

function mapOs(platform) {
  switch (platform) {
    case "linux":
      return "linux";
    case "darwin":
      return "darwin";
    case "win32":
      return "windows";
    default:
      throw new Error(`Unsupported platform: ${platform}`);
  }
}

function mapArch(arch) {
  switch (arch) {
    case "x64":
      return "amd64";
    case "arm64":
      return "arm64";
    default:
      throw new Error(`Unsupported architecture: ${arch}`);
  }
}

function download(url, destination, redirects = 5) {
  return new Promise((resolve, reject) => {
    const request = https.get(url, (response) => {
      if (
        response.statusCode &&
        response.statusCode >= 300 &&
        response.statusCode < 400 &&
        response.headers.location
      ) {
        if (redirects <= 0) {
          reject(new Error(`Too many redirects for ${url}`));
          return;
        }
        const redirectUrl = new URL(response.headers.location, url).toString();
        response.resume();
        download(redirectUrl, destination, redirects - 1).then(resolve).catch(reject);
        return;
      }

      if (response.statusCode !== 200) {
        reject(new Error(`Download failed (${response.statusCode}) for ${url}`));
        response.resume();
        return;
      }

      const file = fs.createWriteStream(destination);
      response.pipe(file);
      file.on("finish", () => {
        file.close(resolve);
      });
      file.on("error", (err) => {
        reject(err);
      });
    });

    request.on("error", (err) => reject(err));
  });
}

function extractArchive(archive, destination, targetOs) {
  if (targetOs === "windows") {
    const escapedArchive = archive.replace(/'/g, "''");
    const escapedDestination = destination.replace(/'/g, "''");
    const command = `Expand-Archive -Path '${escapedArchive}' -DestinationPath '${escapedDestination}' -Force`;
    const result = spawnSync("powershell", ["-NoProfile", "-Command", command], {
      stdio: "inherit"
    });
    if (result.status !== 0) {
      throw new Error("Failed to extract release zip on Windows");
    }
    return;
  }

  const result = spawnSync("tar", ["-xzf", archive, "-C", destination], { stdio: "inherit" });
  if (result.status !== 0) {
    throw new Error("Failed to extract release tar.gz");
  }
}

function findFileRecursive(root, fileName) {
  const entries = fs.readdirSync(root, { withFileTypes: true });
  for (const entry of entries) {
    const fullPath = path.join(root, entry.name);
    if (entry.isFile() && entry.name === fileName) {
      return fullPath;
    }
    if (entry.isDirectory()) {
      const nested = findFileRecursive(fullPath, fileName);
      if (nested) {
        return nested;
      }
    }
  }
  return null;
}

function resolveUserBinDir() {
  const explicit =
    process.env.GRAFQUERY_INSTALL_DIR ||
    process.env.GRAFANA_QUERY_INSTALL_DIR;
  if (explicit) {
    return explicit;
  }
  return path.join(os.homedir(), ".local", "bin");
}

function installToUserBin(sourceBinaryPath) {
  try {
    fs.mkdirSync(userBinDir, { recursive: true });
    const destination = path.join(userBinDir, binaryName);
    fs.copyFileSync(sourceBinaryPath, destination);
    try {
      fs.chmodSync(destination, 0o755);
    } catch {
      // Best effort for non-POSIX environments.
    }
    console.log(`[grafquery npm] Installed ${binaryName} to ${destination}`);
  } catch (err) {
    console.warn(`[grafquery npm] Warning: failed to install to ${userBinDir}: ${err.message}`);
  }
}

function ensurePathContainsUserBin(dir) {
  const autoPath = isTruthy(process.env.GRAFQUERY_NPM_AUTO_PATH ?? "1");
  if (!autoPath) {
    return;
  }

  if (pathContains(process.env.PATH || "", dir)) {
    return;
  }

  if (process.platform === "win32") {
    ensureWindowsUserPath(dir);
    return;
  }
  ensurePosixUserPath(dir);
}

function ensureWindowsUserPath(dir) {
  const escaped = dir.replace(/'/g, "''");
  const psScript = [
    `$dir='${escaped}'`,
    "$current=[Environment]::GetEnvironmentVariable('Path','User')",
    "$entries=@()",
    "if (-not [string]::IsNullOrWhiteSpace($current)) { $entries = $current -split ';' }",
    "$has=$false",
    "foreach ($entry in $entries) {",
    "  if ($entry.Trim().TrimEnd('\\') -ieq $dir.TrimEnd('\\')) { $has=$true; break }",
    "}",
    "if (-not $has) {",
    "  $newPath = if ([string]::IsNullOrWhiteSpace($current)) { $dir } else { \"$current;$dir\" }",
    "  [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')",
    "  Write-Output 'updated'",
    "} else {",
    "  Write-Output 'exists'",
    "}"
  ].join(";");

  const result = spawnSync("powershell", ["-NoProfile", "-Command", psScript], {
    encoding: "utf8"
  });
  if (result.status !== 0) {
    console.warn(
      `[grafquery npm] Warning: unable to update User PATH automatically. Add ${dir} to PATH manually.`
    );
    return;
  }
  process.env.PATH = `${dir};${process.env.PATH || ""}`;
  if ((result.stdout || "").includes("updated")) {
    console.log(`[grafquery npm] Added ${dir} to User PATH. Open a new shell to use grafquery.`);
  }
}

function ensurePosixUserPath(dir) {
  const shell = (process.env.SHELL || "").toLowerCase();
  const profile =
    shell.includes("zsh")
      ? path.join(os.homedir(), ".zshrc")
      : shell.includes("bash")
        ? path.join(os.homedir(), ".bashrc")
        : path.join(os.homedir(), ".profile");
  const line = `export PATH="${dir}:$PATH"`;
  try {
    fs.mkdirSync(path.dirname(profile), { recursive: true });
    if (!fs.existsSync(profile)) {
      fs.writeFileSync(profile, "");
    }
    const content = fs.readFileSync(profile, "utf8");
    if (!content.includes(line)) {
      fs.appendFileSync(profile, `\n# Added by grafquery npm install\n${line}\n`);
      console.log(`[grafquery npm] Added ${dir} to PATH in ${profile}. Restart your shell.`);
    }
    process.env.PATH = `${dir}:${process.env.PATH || ""}`;
  } catch (err) {
    console.warn(
      `[grafquery npm] Warning: unable to update PATH profile automatically (${err.message}).`
    );
  }
}

function pathContains(pathValue, entry) {
  const delimiter = process.platform === "win32" ? ";" : ":";
  const normalizedEntry = normalizePathEntry(entry);
  return pathValue
    .split(delimiter)
    .map((value) => normalizePathEntry(value))
    .some((value) => value === normalizedEntry);
}

function normalizePathEntry(value) {
  if (!value) {
    return "";
  }
  if (process.platform === "win32") {
    return value.trim().replace(/[\\\/]+$/, "").toLowerCase();
  }
  return value.trim().replace(/[\\\/]+$/, "");
}

function isTruthy(value) {
  switch ((value || "").toString().trim().toLowerCase()) {
    case "1":
    case "true":
    case "yes":
    case "on":
      return true;
    default:
      return false;
  }
}
