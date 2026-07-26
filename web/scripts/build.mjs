import { cpSync, mkdirSync, writeFileSync, readFileSync, existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const cockpit = path.resolve(root, "../internal/server/static/index.html");
const dist = path.resolve(root, "dist");

if (!existsSync(cockpit)) {
  console.error("strike-web build: missing cockpit at", cockpit);
  process.exit(1);
}

mkdirSync(dist, { recursive: true });
cpSync(cockpit, path.join(dist, "index.html"));

// Stamp so CI can assert the build ran.
const html = readFileSync(cockpit, "utf8");
writeFileSync(
  path.join(dist, "build.json"),
  JSON.stringify(
    {
      ok: true,
      source: "internal/server/static/index.html",
      bytes: Buffer.byteLength(html, "utf8"),
    },
    null,
    2,
  ) + "\n",
);

console.log("strike-web: wrote dist/index.html (" + Buffer.byteLength(html, "utf8") + " bytes)");
