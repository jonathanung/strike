// Browser check for the document viewer. Called from webui-smoke after the
// shell text check. Uses the same DevTools session and the auth'd daemon.
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

async function waitFor(send, expression, label) {
  let last = "";
  for (let i = 0; i < 40; i++) {
    last = String((await evalValue(send, expression)) ?? "");
    if (last.startsWith("ok")) return last;
    await delay(250);
  }
  throw new Error(`${label}: ${last.slice(0, 400)}`);
}

export async function editorBrowserCheck({ send, port, token, repoDir }) {
  const sample = join(repoDir, "editor-smoke.txt");
  await writeFile(sample, "alpha beta\n");
  try {
    const trav = await fetch(
      `http://127.0.0.1:${port}/v1/file/view?path=${encodeURIComponent("../secret.txt")}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    if (trav.status === 200) {
      throw new Error(`traversal read returned 200: ${(await trav.text()).slice(0, 200)}`);
    }

    await send("Page.navigate", {
      url: `http://127.0.0.1:${port}/?token=${encodeURIComponent(token)}`,
    });
    await waitFor(
      send,
      `(() => {
        const t = document.body ? document.body.innerText : "";
        return t.includes("Files") && t.includes("Activity") ? "ok" : t.slice(0, 120);
      })()`,
      "shell after token handoff",
    );
    await evalValue(
      send,
      `localStorage.setItem("strike.token", ${JSON.stringify(token)}); localStorage.setItem("strike.base", ""); "ok"`,
    );
    await send("Page.reload", { ignoreCache: true });
    await waitFor(
      send,
      `(() => {
        const t = document.body ? document.body.innerText : "";
        return t.includes("Files") ? "ok" : t.slice(0, 120);
      })()`,
      "shell after reload",
    );

    const clicked = await evalValue(
      send,
      `(() => { const b = [...document.querySelectorAll("button")].find(el => el.textContent.trim() === "Files"); if (!b) return "no files tab"; b.click(); return "ok"; })()`,
    );
    if (clicked !== "ok") {
      throw new Error(`files tab click: ${clicked}`);
    }
    await waitFor(
      send,
      `(() => {
        if (document.querySelector('[data-ev-open="editor-smoke.txt"]')) return "ok";
        const names = [...document.querySelectorAll("[data-ev-open]")].slice(0, 12).map(el => el.getAttribute("data-ev-open")).join(",");
        const side = document.querySelector(".side");
        const sideText = side ? side.innerText.slice(0, 280) : "no side";
        return "opens=" + document.querySelectorAll("[data-ev-open]").length + " sample=" + names + " side=" + sideText;
      })()`,
      "open button for editor-smoke.txt",
    );
    // #1521: the explicit @ mention button inserts into the composer and
    // must not open the viewer.
    await waitFor(
      send,
      `(() => document.querySelector('[data-ev-mention="editor-smoke.txt"]') ? "ok" : "no mention button")()`,
      "mention button for editor-smoke.txt",
    );
    await evalValue(
      send,
      `(() => { document.querySelector('[data-ev-mention="editor-smoke.txt"]').click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => {
        const ta = document.querySelector(".composer textarea");
        if (!ta) return "no composer";
        if (document.querySelector(".ev-doc")) return "mention opened the viewer";
        return ta.value.includes("@editor-smoke.txt") ? "ok" : "composer=" + ta.value.slice(0, 120);
      })()`,
      "mention inserts @path without opening",
    );
    await evalValue(
      send,
      `(() => {
        const ta = document.querySelector(".composer textarea");
        ta.value = "";
        ta.dispatchEvent(new Event("input", { bubbles: true }));
        return "ok";
      })()`,
    );
    // #1521: clicking the file name itself opens the viewer and leaves the
    // composer untouched.
    await evalValue(
      send,
      `(() => { document.querySelector('[data-ev-open="editor-smoke.txt"]').click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => {
        const root = document.querySelector("[data-ev-root]");
        if (!root) return "no pane";
        return root.innerText.includes("alpha beta") ? "ok" : root.innerText.slice(0, 180);
      })()`,
      "document pane",
    );
    const composerAfterOpen = await evalValue(
      send,
      `(() => { const ta = document.querySelector(".composer textarea"); return ta ? ta.value : "no composer"; })()`,
    );
    if (String(composerAfterOpen).includes("@editor-smoke.txt")) {
      throw new Error(`name click polluted the composer: ${composerAfterOpen}`);
    }

    const focused = await evalValue(
      send,
      `(() => {
        const pane = document.querySelector(".ev-doc");
        pane.focus();
        pane.dispatchEvent(new KeyboardEvent("keydown", { key: "f", ctrlKey: true, bubbles: true, cancelable: true }));
        return document.activeElement && document.activeElement.id;
      })()`,
    );
    if (focused !== "ev-search") {
      throw new Error(`ctrl+f focus = ${focused}`);
    }
    await evalValue(
      send,
      `(() => {
        const input = document.getElementById("ev-search");
        input.value = "beta";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        return "ok";
      })()`,
    );
    await waitFor(
      send,
      `(() => {
        const n = document.querySelector(".ev-count");
        return n && n.textContent.trim() === "1/1" ? "ok" : (n ? n.textContent : "no count");
      })()`,
      "search count",
    );

    await evalValue(
      send,
      `(() => { const b = document.querySelector("[data-ev-edit]"); if (!b || b.disabled) return "edit disabled"; b.click(); return "ok"; })()`,
    );
    await writeFile(sample, "agent\n");
    await evalValue(
      send,
      `(() => {
        const ta = document.querySelector(".ev-editor");
        if (!ta) return "no editor";
        ta.value = "alpha edited\\n";
        ta.dispatchEvent(new Event("input", { bubbles: true }));
        const save = document.querySelector("[data-ev-save]");
        if (!save) return "no save";
        save.click();
        return "ok";
      })()`,
    );
    await waitFor(
      send,
      `(() => {
        const t = document.body.innerText;
        return t.includes("changed on disk") ? "ok" : t.slice(0, 240);
      })()`,
      "conflict notice",
    );
    const disk = await fetch(
      `http://127.0.0.1:${port}/v1/file/view?path=editor-smoke.txt`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    const body = await disk.json();
    if (body.content !== "agent\n") {
      throw new Error(`conflict wrote the file: ${JSON.stringify(body).slice(0, 200)}`);
    }

    await evalValue(
      send,
      `(() => { const b = [...document.querySelectorAll("button")].find(el => el.textContent.trim() === "Back to chat"); if (b) b.click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => document.querySelector("textarea") ? "ok" : "no composer")()`,
      "composer",
    );
    await evalValue(
      send,
      `(() => {
        const ta = document.querySelector(".composer textarea");
        if (!ta) return "no textarea";
        ta.value = "look at README.md:1";
        ta.dispatchEvent(new Event("input", { bubbles: true }));
        const sendBtn = document.querySelector("button.send");
        if (!sendBtn) return "no send";
        sendBtn.click();
        return "ok";
      })()`,
    );
    await waitFor(
      send,
      `(() => document.querySelector('[data-ev-path="README.md"]') ? "ok" : (document.body.innerText.includes("no live session") ? "no live session" : "waiting"))()`,
      "path:line link",
    );
    await evalValue(
      send,
      `(() => { document.querySelector('[data-ev-path="README.md"]').click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => {
        const root = document.querySelector("[data-ev-root]");
        if (!root) return "no pane";
        return root.innerText.includes("strike") ? "ok" : root.innerText.slice(0, 180);
      })()`,
      "readme from path link",
    );
    console.log("webui-editor-check ok");
  } finally {
    await rm(sample, { force: true });
  }
}
