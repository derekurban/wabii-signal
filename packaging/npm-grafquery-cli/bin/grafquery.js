#!/usr/bin/env node

const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

const binaryName = process.platform === "win32" ? "grafquery.exe" : "grafquery";
const binaryPath = path.join(__dirname, binaryName);

if (!fs.existsSync(binaryPath)) {
  console.error("grafquery binary is missing. Reinstall grafquery.");
  process.exit(1);
}

const result = spawnSync(binaryPath, process.argv.slice(2), { stdio: "inherit" });

if (result.error) {
  console.error(`Failed to run ${binaryName}: ${result.error.message}`);
  process.exit(1);
}

process.exit(result.status ?? 1);
