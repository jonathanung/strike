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
  await writeFile(sample, "alpha beta\nsecond line\nthird line\nfourth line\nfifth line\n");
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

    // Go-to-line (#1403): Enter in the line input jumps and reports the
    // clamped position; an out-of-range entry clamps to the last line.
    await evalValue(
      send,
      `(() => {
        const input = document.getElementById("ev-goto");
        if (!input) return "no goto";
        input.value = "3";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
        return "ok";
      })()`,
    );
    await waitFor(
      send,
      `(() => {
        const n = document.querySelector("[data-ev-goto-note]");
        return n && n.textContent.includes("Line 3 of 5") ? "ok" : (n ? n.textContent : "no goto note");
      })()`,
      "goto line 3",
    );
    await evalValue(
      send,
      `(() => {
        const input = document.getElementById("ev-goto");
        input.value = "999";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
        return "ok";
      })()`,
    );
    await waitFor(
      send,
      `(() => {
        const n = document.querySelector("[data-ev-goto-note]");
        return n && n.textContent.includes("Line 5 of 5") && n.textContent.includes("clamped") ? "ok" : (n ? n.textContent : "no goto note");
      })()`,
      "goto clamp",
    );
    // Ctrl+G focuses the go-to-line input, like Ctrl+F focuses search.
    const gotoFocused = await evalValue(
      send,
      `(() => {
        const pane = document.querySelector(".ev-doc");
        pane.focus();
        pane.dispatchEvent(new KeyboardEvent("keydown", { key: "g", ctrlKey: true, bubbles: true, cancelable: true }));
        return document.activeElement && document.activeElement.id;
      })()`,
    );
    if (gotoFocused !== "ev-goto") {
      throw new Error(`ctrl+g focus = ${gotoFocused}`);
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
    // Cursor readout (#1416) + Tab indent / Shift+Tab outdent (#1408).
    await waitFor(
      send,
      `(() => document.querySelector(".ev-editor") ? "ok" : "no editor")()`,
      "editor textarea",
    );
    await evalValue(
      send,
      `(() => {
        const ta = document.querySelector(".ev-editor");
        ta.focus();
        // Caret at line 2, column 3 ("se|cond line").
        ta.setSelectionRange(13, 13);
        ta.dispatchEvent(new KeyboardEvent("keyup", { key: "ArrowRight", bubbles: true }));
        return "ok";
      })()`,
    );
    await waitFor(
      send,
      `(() => {
        const n = document.querySelector("[data-ev-cursor]");
        return n && n.textContent.includes("Ln 2, Col 3") ? "ok" : (n ? n.textContent : "no cursor readout");
      })()`,
      "cursor readout",
    );
    const tabbed = await evalValue(
      send,
      `(() => {
        const ta = document.querySelector(".ev-editor");
        ta.focus();
        // Select lines 2-3 and Tab: both indent, focus stays in the editor.
        ta.setSelectionRange(11, 27);
        ta.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true }));
        return ta.value;
      })()`,
    );
    // The draft updates asynchronously through Leptos; poll for the value.
    await waitFor(
      send,
      `(() => {
        const ta = document.querySelector(".ev-editor");
        return ta && ta.value.includes("  second line") && ta.value.includes("  third line") ? "ok" : (ta ? ta.value : "no editor");
      })()`,
      "tab indent selection",
    );
    if (String(tabbed).length === 0) throw new Error("tab dispatch returned empty");
    await evalValue(
      send,
      `(() => {
        const ta = document.querySelector(".ev-editor");
        ta.focus();
        ta.setSelectionRange(0, ta.value.length);
        ta.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", shiftKey: true, bubbles: true, cancelable: true }));
        return "ok";
      })()`,
    );
    await waitFor(
      send,
      `(() => {
        const ta = document.querySelector(".ev-editor");
        return ta && ta.value.startsWith("alpha beta") && !ta.value.includes("  second line") ? "ok" : (ta ? ta.value : "no editor");
      })()`,
      "shift+tab outdent",
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
