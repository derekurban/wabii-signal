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

  console.log(`[grafquery npm] Installed ${binaryName}`);
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
