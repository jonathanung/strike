#!/usr/bin/env node
// Guard for #1710: the Go binary serves the committed bundle in
// internal/frontend/server/static/ (go:embed), and `make build` does NOT
// regenerate it — only `make webui-build` does. PR #1688 fixed the phone
// `.segmented` blanket hide in webui/mobile.css, but the committed bundle
// was never regenerated, so the served CSS kept the old blanket rule and
// the #1551 trap stayed armed while every source-level check stayed green.
//
// This check fails when:
//   1. any committed assets/<name>-<hash>.css differs byte-for-byte from
//      its webui/<name>.css source (or is missing / orphaned), and
//   2. the served mobile CSS does not contain the header-scoped hide
//      `body[data-phone="1"] .page-header .segmented { display: none }`,
//      or still contains a blanket `body[data-phone="1"] .segmented` hide.
//
// Fix a failure by running `make webui-build` and committing the result.
// Run locally with: node scripts/check-webui-bundle.mjs
// (or `make webui-bundle-check`).

import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const webuiDir = join(root, "webui");
const assetsDir = join(
  root,
  "internal",
  "frontend",
  "server",
  "static",
  "assets",
);

// Strip CSS comments before rule matching: mobile.css documents the old
// blanket rule in a comment, and that mention must not trip (or satisfy)
// the rule checks below.
export function stripComments(css) {
  return css.replace(/\/\*[\s\S]*?\*\//g, "");
}

const SCOPED_HIDE =
  /body\[data-phone="1"\]\s+\.page-header\s+\.segmented\s*\{[^}]*display\s*:\s*none/;
const BLANKET_HIDE = /body\[data-phone="1"\]\s+\.segmented\s*(?:\{|,)/;

export function segmentedRuleProblems(css, label) {
  const rules = stripComments(css);
  const problems = [];
  if (!SCOPED_HIDE.test(rules)) {
    problems.push(
      `${label}: missing header-scoped phone hide ` +
        `'body[data-phone="1"] .page-header .segmented { display: none }'`,
    );
  }
  if (BLANKET_HIDE.test(rules)) {
    problems.push(
      `${label}: still contains the blanket phone hide ` +
        `'body[data-phone="1"] .segmented' (#1551 trap)`,
    );
  }
  return problems;
}

function main() {
  const problems = [];

  let sources;
  let bundled;
  try {
    sources = readdirSync(webuiDir)
      .filter((name) => name.endsWith(".css"))
      .sort();
  } catch (err) {
    console.error(`check-webui-bundle: cannot read webui/: ${err.message}`);
    process.exit(1);
  }
  try {
    bundled = readdirSync(assetsDir)
      .filter((name) => name.endsWith(".css"))
      .sort();
  } catch (err) {
    console.error(
      `check-webui-bundle: cannot read committed bundle assets/: ${err.message}`,
    );
    process.exit(1);
  }

  // Map each source to its single hashed bundle file. Trunk names bundle
  // CSS `<stem>-<hash>.css`; the hash length varies, so match on the stem.
  const bundleFor = new Map();
  for (const name of bundled) {
    const match = /^(.+)-[0-9a-f]+\.css$/.exec(name);
    if (!match) {
      problems.push(`assets/${name}: bundled CSS with no trunk hash in its name`);
      continue;
    }
    const stem = `${match[1]}.css`;
    if (bundleFor.has(stem)) {
      problems.push(
        `assets/: multiple bundled copies of ${stem} (${bundleFor.get(stem)}, ${name})`,
      );
    }
    bundleFor.set(stem, name);
  }

  for (const source of sources) {
    const bundleName = bundleFor.get(source);
    if (!bundleName) {
      problems.push(
        `webui/${source}: no bundled copy in internal/frontend/server/static/assets/`,
      );
      continue;
    }
    const sourceBytes = readFileSync(join(webuiDir, source));
    const bundleBytes = readFileSync(join(assetsDir, bundleName));
    if (!sourceBytes.equals(bundleBytes)) {
      problems.push(
        `webui/${source} diverges from the committed bundle assets/${bundleName} ` +
          `(regenerate with 'make webui-build' and commit the result)`,
      );
    }
    bundleFor.delete(source);
  }
  for (const [stem, name] of bundleFor) {
    problems.push(`assets/${name}: bundled CSS with no webui/${stem} source`);
  }

  // The #1710 rule checks run on the served (bundled) mobile CSS itself,
  // so a bundle that somehow matched a regressed source still fails.
  const mobileBundle = bundled.find((name) => /^mobile-[0-9a-f]+\.css$/.test(name));
  if (!mobileBundle) {
    problems.push("assets/: no bundled mobile-*.css in the committed bundle");
  } else {
    problems.push(
      ...segmentedRuleProblems(
        readFileSync(join(assetsDir, mobileBundle), "utf8"),
        `served assets/${mobileBundle}`,
      ),
    );
  }
  problems.push(
    ...segmentedRuleProblems(
      readFileSync(join(webuiDir, "mobile.css"), "utf8"),
      "source webui/mobile.css",
    ),
  );

  if (problems.length > 0) {
    console.error("check-webui-bundle: FAILED");
    for (const problem of problems) console.error(`  - ${problem}`);
    process.exit(1);
  }
  console.log(
    `check-webui-bundle: ok — ${sources.length} webui CSS files match the committed bundle; ` +
      "the phone .segmented hide is header-scoped in the served mobile CSS",
  );
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  main();
}
