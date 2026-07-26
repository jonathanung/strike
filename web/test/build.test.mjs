import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, rmSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const dist = path.join(root, "dist");
const cockpit = path.resolve(root, "../internal/server/static/index.html");

test("build copies the embedded cockpit and writes metadata", (t) => {
  t.after(() => rmSync(dist, { force: true, recursive: true }));

  execFileSync(process.execPath, ["./scripts/build.mjs"], { cwd: root, stdio: "pipe" });

  const index = path.join(dist, "index.html");
  const metadata = path.join(dist, "build.json");
  assert.ok(existsSync(index), "build should write dist/index.html");
  assert.ok(existsSync(metadata), "build should write dist/build.json");
  const source = readFileSync(cockpit, "utf8");
  assert.equal(readFileSync(index, "utf8"), source);
  for (const contract of ["<title>strike web</title>", "WebSocket", "EventSource", 'fetch("/v1/ops"']) {
    assert.ok(source.includes(contract), `cockpit should include ${contract}`);
  }

  assert.deepEqual(JSON.parse(readFileSync(metadata, "utf8")), {
    ok: true,
    source: "internal/server/static/index.html",
    bytes: Buffer.byteLength(source, "utf8"),
  });
});
