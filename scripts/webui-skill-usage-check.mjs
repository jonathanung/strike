// Browser check for skill usage in Customize → Skills (#1442). Called from
// webui-smoke. seedSkillUsage writes a second skill that is never run, plus
// one recorded session log in which /smoke-note ran once. The daemon counts
// runs from the real session logs, so the two Customize rows must read
// differently: "Used 1 time · last 2026-11-01" and "Never used".
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

export const IDLE_SKILL = "smoke-idle";
const SEED_SESSION = "20260101T000000.000000000Z-SKILLUSAGESEED";

export async function seedSkillUsage(home) {
  await writeFile(
    join(home, ".strike", "skills", `${IDLE_SKILL}.md`),
    `---\nname: ${IDLE_SKILL}\ndescription: seeded skill that never runs\n---\nNever invoked.\n`,
  );
  const sessions = join(home, ".strike", "sessions");
  await mkdir(sessions, { recursive: true });
  const lines = [
    { type: "session.header", schemaVersion: 1, time: "2026-11-01T10:00:00Z" },
    {
      type: "user.message",
      time: "2026-11-01T10:00:01Z",
      data: { sessionId: SEED_SESSION, turnId: "seed-turn", text: "/smoke-note hello" },
    },
  ];
  await writeFile(
    join(sessions, `${SEED_SESSION}.jsonl`),
    lines.map((l) => JSON.stringify(l)).join("\n") + "\n",
  );
}

async function evalValue(send, expression) {
  const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
  const details = ev.result?.exceptionDetails;
  if (details) {
    throw new Error(`eval failed: ${JSON.stringify(details).slice(0, 400)}`);
  }
  return ev.result?.result?.value;
}

// Customize must be open on the Skills section.
export async function skillUsageCheck({ send }) {
  let last = "";
  for (let i = 0; i < 60; i++) {
    last = String(
      (await evalValue(
        send,
        `(() => {
          const read = (name) => document.querySelector('[data-cz-item="' + name + '"] [data-cz-usage]')?.getAttribute("data-cz-usage") || "";
          return JSON.stringify({ used: read("smoke-note"), idle: read(${JSON.stringify(IDLE_SKILL)}) });
        })()`,
      )) ?? "",
    );
    const rows = JSON.parse(last || "{}");
    if (rows.used === "Used 1 time · last 2026-11-01" && rows.idle === "Never used") return;
    await delay(250);
  }
  throw new Error(`skill usage rows: ${last}`);
}
