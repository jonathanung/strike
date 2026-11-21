// Browser check for skill proposals (#1611). Called from webui-smoke at the
// end of the Customize pass. It seeds three recorded root sessions in this
// workspace that open with the same request and run the same tools. Then it
// turns the opt-in on in Customize → Skills, clicks "Look now", expects one
// proposal card, accepts it, and expects the skill in the catalog. The
// dismiss path and near-miss handling are covered by Go tests.
import { rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

export const PROPOSED_SKILL = "regenerate-smoke-fixtures";
const OPENING = "Regenerate the smoke fixtures, rerun the linter and summarize warnings";

export async function seedRepeatedWorkflow({ home, port, token }) {
  // The workspace key the scan reads for, exactly as Customize asks for it
  // (no ?root), so seeded sessions land in the scanned workspace.
  const res = await fetch(`http://127.0.0.1:${port}/v1/skill-proposals`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  const projectKey = (await res.json()).workspace || "";
  if (!projectKey) throw new Error("skill proposals: GET /v1/skill-proposals returned no workspace");
  const sessions = join(home, ".strike", "sessions");
  const now = Date.now();
  for (let i = 0; i < 3; i++) {
    const id = `20260102T00000${i}.000000000Z-PROPOSALSEED${i}`;
    const at = (s) => new Date(now - (3 - i) * 3600_000 + s * 1000).toISOString();
    const corr = { sessionId: id, turnId: "t1" };
    const lines = [
      { type: "session.header", schemaVersion: 1, time: at(0) },
      { type: "user.message", time: at(0), data: { ...corr, text: OPENING } },
      { type: "tool.begin", time: at(1), data: { ...corr, callId: "a", name: "bash", args: {} } },
      { type: "tool.begin", time: at(2), data: { ...corr, callId: "b", name: "read", args: {} } },
      { type: "turn.completed", time: at(3), data: corr },
    ];
    await writeFile(join(sessions, `${id}.jsonl`), lines.map((l) => JSON.stringify(l)).join("\n") + "\n");
    await writeFile(join(sessions, `${id}.meta.json`), JSON.stringify({ projectKey, title: `Fixture refresh ${i + 1}` }));
  }
}

async function evalValue(send, expression) {
  const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
  const details = ev.result?.exceptionDetails;
  if (details) {
    throw new Error(`eval failed: ${JSON.stringify(details).slice(0, 400)}`);
  }
  return ev.result?.result?.value;
}

async function waitFor(send, expression, label, tries = 80) {
  let last = "";
  for (let i = 0; i < tries; i++) {
    last = String((await evalValue(send, expression)) ?? "");
    if (last.startsWith("ok")) return last;
    await delay(250);
  }
  throw new Error(`skill proposals: ${label}: ${last.slice(0, 400)}`);
}

// Customize must be mounted; this opens the Skills section itself.
export async function skillProposalsCheck({ send, repoDir }) {
  await evalValue(send, `document.querySelector('[data-cz-section="skills"]')?.click()`);
  await waitFor(
    send,
    `document.querySelector('[data-cz-proposals-toggle="false"]') ? "ok" : "toggle " + (document.querySelector('[data-cz-proposals-toggle]')?.getAttribute("data-cz-proposals-toggle") || "missing")`,
    "opt-in starts off",
  );
  await evalValue(send, `document.querySelector('[data-cz-proposals-toggle]').click()`);
  await waitFor(
    send,
    `(() => {
      const scan = document.querySelector('[data-cz-proposals-scan]');
      return document.querySelector('[data-cz-proposals-toggle="true"]') && scan && !scan.disabled ? "ok" : "not enabled";
    })()`,
    "opt-in turns on",
  );
  await evalValue(send, `document.querySelector('[data-cz-proposals-scan]').click()`);
  await waitFor(
    send,
    `(() => {
      const cards = [...document.querySelectorAll('[data-cz-proposal]')];
      const text = cards.map((c) => c.innerText).join(" | ");
      return cards.length === 1 && text.includes(${JSON.stringify(PROPOSED_SKILL)}) && text.includes("From 3 sessions")
        ? "ok"
        : "cards " + JSON.stringify(text.slice(0, 300)) + " note " + (document.querySelector('[data-cz-proposals-note]')?.innerText || "");
    })()`,
    "scan proposes the seeded workflow",
  );
  await evalValue(send, `document.querySelector('[data-cz-proposal-accept]').click()`);
  try {
    await waitFor(
      send,
      `(() => {
        const listed = document.querySelector('[data-cz-item=${JSON.stringify(PROPOSED_SKILL)}]');
        const left = document.querySelectorAll('[data-cz-proposal]').length;
        return listed && left === 0 ? "ok" : "listed " + Boolean(listed) + " cards " + left + " note " + (document.querySelector('[data-cz-proposals-note]')?.innerText || "");
      })()`,
      "accepted skill is listed and the card is gone",
    );
  } finally {
    await rm(join(repoDir, ".strike", "skills", `${PROPOSED_SKILL}.md`), { force: true });
    await rm(join(repoDir, ".strike", "skills", PROPOSED_SKILL), { recursive: true, force: true });
  }
}
