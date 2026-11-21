// Browser check for Markdown Review (#1366). Called from webui-smoke after
// the document viewer check, on the same DevTools session and daemon.
// Covers: select a passage → composer quotes it; two comments in two
// sections; unsent comments survive a page reload; editing a commented
// passage marks that comment stale (not moved) while the other stays
// attached; a failed send keeps every comment with the error visible;
// a real send produces exactly one transcript turn with both comments.
import { rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

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
  throw new Error(`md review: ${label}: ${last.slice(0, 400)}`);
}

const FILE = "mdreview-spec-1366.md";
const SPEC = "# Requirements\n\nThe runner must retry three times.\n\n## Design\n\nA queue holds pending work.\n";

const clickButton = (label) =>
  `(() => { const b = [...document.querySelectorAll("button")].find((el) => el.textContent.trim() === ${JSON.stringify(label)}); if (!b) return "no " + ${JSON.stringify(label)}; b.click(); return "ok"; })()`;

export async function mdReviewBrowserCheck({ send, port, token, repoDir }) {
  const file = join(repoDir, FILE);
  await writeFile(file, SPEC);
  const openSpec = async () => {
    await evalValue(send, clickButton("Back to chat"));
    await waitFor(send, `(() => document.querySelector("textarea") ? "ok" : "no composer")()`, "composer");
    await waitFor(send, clickButton("Files"), "files tab");
    await waitFor(
      send,
      `(() => { const b = document.querySelector('[data-ev-open="${FILE}"]'); if (!b) return "no row"; b.click(); return "ok"; })()`,
      "open spec",
    );
    await waitFor(
      send,
      `(() => { const h = document.getElementById("mdr-host"); return h && h.innerText.includes("A queue holds") && document.querySelector("[data-md-review]") ? "ok" : "no host"; })()`,
      "rendered spec with review panel",
    );
  };
  // Select `needle` inside the rendered view and fire the mouseup the
  // pane listens for, as a real drag-select would.
  const comment = async (needle, body) => {
    const sel = await evalValue(
      send,
      `(() => {
        const host = document.getElementById("mdr-host");
        const walker = document.createTreeWalker(host, NodeFilter.SHOW_TEXT);
        let node;
        while ((node = walker.nextNode())) {
          const i = node.textContent.indexOf(${JSON.stringify(needle)});
          if (i >= 0) {
            const r = document.createRange();
            r.setStart(node, i);
            r.setEnd(node, i + ${needle.length});
            const s = getSelection();
            s.removeAllRanges();
            s.addRange(r);
            host.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
            return "ok";
          }
        }
        return "needle not found";
      })()`,
    );
    if (sel !== "ok") throw new Error(`md review select ${needle}: ${sel}`);
    await waitFor(
      send,
      `(() => { const c = document.querySelector("[data-mdr-composer]"); return c && c.innerText.includes(${JSON.stringify(needle)}) ? "ok" : "no composer quote"; })()`,
      `composer quotes ${needle}`,
    );
    await evalValue(
      send,
      `(() => { const ta = document.querySelector("[data-mdr-composer] textarea"); ta.value = ${JSON.stringify(body)}; ta.dispatchEvent(new Event("input", { bubbles: true })); document.querySelector(".mdr-add").click(); return "ok"; })()`,
    );
  };
  const state = `(() => JSON.stringify({
    count: (document.querySelector("[data-mdr-count]") || {}).textContent || "",
    stale: document.querySelectorAll('[data-mdr-stale="true"]').length,
    headings: [...document.querySelectorAll(".mdr-heading")].map((h) => h.textContent.trim()),
    error: (document.querySelector("[data-mdr-error]") || {}).textContent || "",
    note: (document.querySelector(".mdr-note") || {}).textContent || "",
  }))()`;
  const expectState = (pred, label) =>
    waitFor(send, `(() => { const s = ${state}; const v = JSON.parse(s); return (${pred})(v) ? "ok" : s; })()`, label);

  try {
    await openSpec();
    await comment("retry three times", "Why three? Make it configurable.");
    await comment("A queue holds", "Which queue implementation?");
    await expectState(
      `(v) => v.count === "2" && v.headings.join("|") === "Requirements|Design" && v.stale === 0`,
      "two comments grouped under two headings",
    );

    // Reload: unsent comments live in browser storage per session + file.
    await send("Page.reload", { ignoreCache: true });
    await waitFor(send, `(() => document.body && document.body.innerText.includes("Files") ? "ok" : "loading")()`, "shell after reload");
    await openSpec();
    await expectState(`(v) => v.count === "2"`, "comments survive reload");

    // Edit the first passage on disk: that comment goes stale, the other stays.
    await writeFile(file, SPEC.replace("retry three times", "retry five times"));
    await openSpec();
    await expectState(`(v) => v.count === "2" && v.stale === 1`, "edited passage marks exactly one comment stale");

    // Failed send: the op request fails; every comment stays, error shown.
    await evalValue(
      send,
      `(() => { window.__mdrFetch = window.fetch; window.fetch = function (input, init) { const u = typeof input === "string" ? input : input.url; if (String(u).includes("/v1/ops")) return Promise.reject(new TypeError("smoke offline")); return window.__mdrFetch.apply(this, arguments); }; return "ok"; })()`,
    );
    await waitFor(send, `(() => { const b = document.querySelector("[data-mdr-send]"); if (!b || b.disabled) return "send disabled"; b.click(); return "ok"; })()`, "send (failing)");
    await expectState(`(v) => v.count === "2" && v.error.includes("kept")`, "failed send keeps comments with the error");
    await evalValue(send, `(() => { window.fetch = window.__mdrFetch; return "ok"; })()`);

    // Real send: one turn, both comments, queue cleared.
    await waitFor(send, `(() => { const b = document.querySelector("[data-mdr-send]"); if (!b || b.disabled) return "send disabled"; b.click(); return "ok"; })()`, "send");
    await expectState(`(v) => v.count === "0" && v.note.includes("Sent 2 comments as one turn")`, "send clears the queue");
    await evalValue(send, clickButton("Back to chat"));
    await waitFor(
      send,
      `(() => {
        const turns = [...document.querySelectorAll('article[data-role="you"]')].filter((a) => a.innerText.includes("Markdown review of"));
        if (turns.length !== 1) return "turns=" + turns.length;
        const t = turns[0].innerText;
        return t.includes("${FILE}") && t.includes("Why three?") && t.includes("Which queue implementation?") && t.includes("no longer in the file") ? "ok" : t.slice(0, 300);
      })()`,
      "exactly one review turn with both comments",
    );
    console.log("webui-md-review-check ok");
  } finally {
    await evalValue(send, `(() => { if (window.__mdrFetch) window.fetch = window.__mdrFetch; return "ok"; })()`).catch(() => {});
    await rm(file, { force: true });
  }
}
