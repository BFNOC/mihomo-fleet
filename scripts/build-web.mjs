import { build } from "esbuild";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";

const root = process.cwd();
const webDir = path.join(root, "internal/app/web");
const outputDir = path.join(webDir, "vendor");

await mkdir(outputDir, { recursive: true });

await build({
  entryPoints: [path.join(root, "internal/app/web-src/app.js")],
  outfile: path.join(webDir, "app.js"),
  bundle: true,
  format: "iife",
  legalComments: "eof",
  minify: true,
  platform: "browser",
  target: ["es2020"],
});

const dependencyTree = JSON.parse(execFileSync("pnpm", ["list", "--prod", "--json", "--depth", "Infinity"], {
  cwd: root,
  encoding: "utf8",
}));
const dependencyPaths = new Set();
function collectDependencies(dependencies = {}) {
  for (const item of Object.values(dependencies)) {
    if (item.path) dependencyPaths.add(item.path);
    collectDependencies(item.dependencies);
  }
}
for (const project of dependencyTree) collectDependencies(project.dependencies);

const notices = [];
for (const packageDir of dependencyPaths) {
  const packageJSON = JSON.parse(await readFile(path.join(packageDir, "package.json"), "utf8"));
  const licenseFile = (await readdir(packageDir)).find((name) => /^(license|licence)(\.|$)/i.test(name));
  const licenseText = licenseFile ? await readFile(path.join(packageDir, licenseFile), "utf8") : "License text not bundled by package.";
  notices.push({
    name: packageJSON.name || path.basename(packageDir),
    version: packageJSON.version,
    license: packageJSON.license || "UNKNOWN",
    licenseText: licenseText.trim(),
  });
}
notices.sort((a, b) => a.name.localeCompare(b.name));

const noticeText = [
  "Third-Party Notices",
  "===================",
  "",
  "This file is generated from pnpm-lock.yaml by scripts/build-web.mjs.",
  "",
  ...notices.flatMap((item) => [
    `${item.name}@${item.version} (${item.license})`,
    "-".repeat(Math.min(78, item.name.length + item.version.length + item.license.length + 4)),
    item.licenseText,
    "",
  ]),
].join("\n");
await writeFile(path.join(outputDir, "THIRD_PARTY_NOTICES.txt"), noticeText, "utf8");
