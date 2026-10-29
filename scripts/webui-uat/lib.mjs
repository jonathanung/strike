// Pure helpers for the web UI UAT harness. No browser, no daemon.

export const SHELL_LABELS = [
  "Menu",
  "Strike",
  "Sessions",
  "Activity",
  "Files",
  "Changes",
  "Approval",
  "Steer",
  "Queue",
  "Send",
  "Connected",
];

export const DARK_TOKENS = { acid: "#7c3aed", ground: "#14131c" };
export const LIGHT_TOKENS = { acid: "#5b21b6", ground: "#ffffff" };

// Phone width used by the shell's one-column break (max-width: 960px).
// Tag stays "mobile" so shot names match the existing shell-empty-mobile.png.
export const PHONE = { width: 390, height: 844, tag: "mobile" };

export function phoneShotName(name) {
  const slug = String(name || "");
  if (!/^[a-z][a-z0-9-]*$/.test(slug)) {
    throw new Error(`phone shot name must be a slug, got ${name}`);
  }
  return `${slug}-${PHONE.tag}`;
}

// Coarse palette check. Headlines rotate and session times move, so a
// full-frame pixel match is not stable across hosts. This threshold is only
// for an opt-in downscale (`--goldens`).
export const GOLDEN_MEAN_ABS_MAX = 18;

export function parseViewport(spec) {
  const m = /^(\d+)x(\d+)$/.exec(String(spec || "").trim());
  if (!m) throw new Error(`viewport must be WxH, got ${spec}`);
  const width = Number(m[1]);
  const height = Number(m[2]);
  if (width < 200 || height < 200) throw new Error(`viewport too small: ${spec}`);
  return { width, height };
}

export function missingLabels(text, labels) {
  const body = String(text || "");
  return labels.filter((label) => !body.includes(label));
}

// Poll getText() until every label appears in the page text or the
// deadline passes, and report the last text seen plus whatever is still
// missing. The shell renders in stages — the boot stub satisfies a
// "Run a command" probe before the live session has spawned and the
// shell has painted states like "Connected" — so a one-shot snapshot
// taken right after boot races that render and intermittently reports
// labels missing on a healthy build. This waiter never weakens the
// check: if the labels genuinely never appear it returns them in
// `missing` after the full deadline, with the final page text for
// diagnostics. `sleep`/`now` are injectable for tests.
export async function waitForLabels(
  getText,
  labels,
  { timeoutMs = 30000, intervalMs = 200, sleep, now } = {},
) {
  const pause =
    sleep || ((ms) => new Promise((resolve) => setTimeout(resolve, ms)));
  const clock = now || Date.now;
  const deadline = clock() + timeoutMs;
  let text = "";
  let missing = [...labels];
  for (;;) {
    text = String((await getText()) || "");
    missing = missingLabels(text, labels);
    if (missing.length === 0) return { text, missing };
    const remaining = deadline - clock();
    if (remaining <= 0) return { text, missing };
    await pause(Math.min(intervalMs, remaining));
  }
}

export function tokenMismatch(actual, expected) {
  const out = [];
  for (const key of Object.keys(expected)) {
    const got = String(actual?.[key] || "").trim().toLowerCase();
    const want = String(expected[key]).trim().toLowerCase();
    if (got !== want) out.push(`${key}=${got || "∅"} want ${want}`);
  }
  return out;
}

export function meanAbsDiff(a, b) {
  if (!a || !b || a.length === 0 || a.length !== b.length) {
    throw new Error(`buffer length ${a?.length} vs ${b?.length}`);
  }
  let sum = 0;
  for (let i = 0; i < a.length; i++) sum += Math.abs(a[i] - b[i]);
  return sum / a.length;
}

export function sideBySideFilter(height) {
  const h = Number(height);
  if (!Number.isInteger(h) || h < 100) throw new Error(`bad side-by-side height ${height}`);
  return `[0:v]scale=-1:${h}:flags=lanczos[l];[1:v]scale=-1:${h}:flags=lanczos[r];[l][r]hstack=inputs=2`;
}

export function parseArgs(argv) {
  const opt = {
    suite: "full",
    video: true,
    out: "webui-uat-artifacts",
    goldens: false,
    updateGoldens: false,
    compareOnly: false,
    left: "",
    right: "",
    compareOut: "",
    height: 594,
    help: false,
  };
  const args = [...argv];
  const take = (flag) => {
    const v = args.shift();
    if (v == null || v.startsWith("--")) throw new Error(`${flag} needs a value`);
    return v;
  };
  while (args.length) {
    const a = args.shift();
    switch (a) {
      case "--suite":
        opt.suite = take(a);
        break;
      case "--no-video":
        opt.video = false;
        break;
      case "--video":
        opt.video = true;
        break;
      case "--out":
        opt.out = take(a);
        break;
      case "--goldens":
        opt.goldens = true;
        break;
      case "--update-goldens":
        opt.goldens = true;
        opt.updateGoldens = true;
        break;
      case "--compare-only":
        opt.compareOnly = true;
        break;
      case "--left":
        opt.left = take(a);
        break;
      case "--right":
        opt.right = take(a);
        break;
      case "--compare-out":
        opt.compareOut = take(a);
        break;
      case "--height":
        opt.height = Number(take(a));
        break;
      case "--help":
      case "-h":
        opt.help = true;
        break;
      default:
        throw new Error(`unknown argument ${a}`);
    }
  }
  if (!/^[a-z][a-z0-9-]*$/.test(opt.suite)) {
    throw new Error(`--suite must be full, smoke, or a scenario slug, got ${opt.suite}`);
  }
  return opt;
}

export const HELP = `webui-uat — boot strike serve --provider echo and drive the cockpit

  node scripts/webui-uat.mjs [--out dir] [--suite full|smoke|<slug>] [--no-video]
  node scripts/webui-uat.mjs --compare-only --left kiro.png --right strike.png --compare-out out.png

Screenshots land in <out>/screenshots. A video of the scripted pass lands in
<out>/uat.mp4 unless --no-video. --suite full (the default) runs the cockpit
pass, then every scripts/webui-uat/scenarios/<slug>.mjs. --suite <slug> runs
only that file. --suite smoke is the cockpit pass and what CI runs. The
process exits non-zero when an assertion fails. See docs/webui-uat.md.
`;
