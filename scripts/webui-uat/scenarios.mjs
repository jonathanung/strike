// Discovers per-stream scenarios. A stream adds scripts/webui-uat/scenarios/<slug>.mjs
// that exports scenario(ctx). Files starting with _ are examples and are not run.
// No edit to this loader, the cockpit pass, or the CI job is required.

import { readdir } from "node:fs/promises";
import { pathToFileURL } from "node:url";

export function scenarioDir() {
  return new URL("./scenarios/", import.meta.url);
}

function dirURL(dir) {
  if (dir instanceof URL) return dir;
  const abs = String(dir).endsWith("/") ? String(dir) : `${dir}/`;
  return pathToFileURL(abs);
}

function wrap(name, mod) {
  if (typeof mod.scenario !== "function") {
    throw new Error(`scenarios/${name}.mjs must export scenario(ctx)`);
  }
  return { name, scenario: mod.scenario };
}

export async function loadScenarios(dir = scenarioDir()) {
  let names;
  try {
    names = await readdir(dir);
  } catch (err) {
    if (err?.code === "ENOENT") return [];
    throw err;
  }
  const files = names.filter((n) => n.endsWith(".mjs") && !n.startsWith("_")).sort();
  const base = dirURL(dir);
  const out = [];
  for (const file of files) {
    const name = file.slice(0, -".mjs".length);
    const mod = await import(new URL(file, base).href);
    out.push(wrap(name, mod));
  }
  return out;
}

export async function loadScenario(name, dir = scenarioDir()) {
  if (!/^[a-z][a-z0-9-]*$/.test(name)) {
    throw new Error(`scenario slug ${name}`);
  }
  const base = dirURL(dir);
  const href = new URL(`${name}.mjs`, base).href;
  try {
    const mod = await import(href);
    return wrap(name, mod);
  } catch (err) {
    const code = err?.code || err?.cause?.code;
    if (code === "ERR_MODULE_NOT_FOUND") {
      throw new Error(`no scenario scripts/webui-uat/scenarios/${name}.mjs`);
    }
    throw err;
  }
}
