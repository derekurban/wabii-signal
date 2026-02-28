import fs from "node:fs";
import path from "node:path";

if (process.argv.length < 3) {
  console.error("Usage: node scripts/release/prepare-npm-package.mjs <tag-version>");
  process.exit(1);
}

const tag = process.argv[2];
const version = tag.startsWith("v") ? tag.slice(1) : tag;
if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version)) {
  console.error(`Invalid semver version derived from tag: ${tag}`);
  process.exit(1);
}

const packageJsonPath = path.join("packaging", "npm-grafquery-cli", "package.json");
const raw = fs.readFileSync(packageJsonPath, "utf8");
const pkg = JSON.parse(raw);
pkg.version = version;
fs.writeFileSync(packageJsonPath, `${JSON.stringify(pkg, null, 2)}\n`, "utf8");

console.log(`Updated ${packageJsonPath} to version ${version}`);
