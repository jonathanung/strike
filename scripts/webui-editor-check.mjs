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
  const other = join(repoDir, "editor-other.txt");
  const long = join(repoDir, "editor-long.txt");
  const md = join(repoDir, "editor-md.md");
  await writeFile(sample, "alpha beta\nsecond line\nthird line\nfourth line\nfifth line\n");
  await writeFile(
    md,
    "# Notes\n\n```rust\nfn greet() { println!(\"hi\"); }\n```\n\n| A | B |\n| --- | --- |\n| 1 | 2 |\n",
  );
  // Capture the next Blob download the page triggers (#1420):
  // save_download builds an object URL and clicks a transient anchor,
  // so record the blob and the anchor's download filename, then read
  // the blob text back for an exact-content assertion.
  const captureDownload = async (expectedName) => {
    const started = await evalValue(
      send,
      `(() => {
        window.__dlName = "";
        window.__dlText = null;
        const orig = URL.createObjectURL.bind(URL);
        URL.createObjectURL = (blob) => {
          blob.text().then((t) => { window.__dlText = t; });
          return orig(blob);
        };
        window.__dlMo = new MutationObserver((recs) => {
          for (const r of recs) for (const n of r.addedNodes) {
            if (n.tagName === "A" && n.getAttribute("download")) window.__dlName = n.getAttribute("download");
          }
        });
        window.__dlMo.observe(document.body, { childList: true });
        const b = document.querySelector("[data-ev-download]");
        if (!b || b.disabled) return "no download chip";
        b.click();
        return "ok";
      })()`,
    );
    if (started !== "ok") {
      throw new Error(`download start: ${started}`);
    }
    await waitFor(
      send,
      `(() => (window.__dlName === ${JSON.stringify(expectedName)} && typeof window.__dlText === "string" ? "ok" : "name=" + window.__dlName))()`,
      `download of ${expectedName}`,
    );
    const text = await evalValue(send, `window.__dlText`);
    await evalValue(
      send,
      `(() => { window.__dlMo && window.__dlMo.disconnect(); return "ok"; })()`,
    );
    return text;
  };
  await writeFile(other, "other content\n");
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
    // Go-to-line wins over an active search (#1403): the query is
    // cleared so neither the scroll effect nor the render window
    // follows the search hit instead of the requested line.
    await evalValue(
      send,
      `(() => {
        const input = document.getElementById("ev-goto");
        input.value = "4";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
        return "ok";
      })()`,
    );
    await waitFor(
      send,
      `(() => {
        const n = document.querySelector("[data-ev-goto-note]");
        const q = document.getElementById("ev-search");
        if (!n || !n.textContent.includes("Line 4 of 5")) return n ? n.textContent : "no goto note";
        return q && q.value === "" ? "ok" : "search not cleared: " + (q ? q.value : "no search");
      })()`,
      "goto clears an active search",
    );

    // Wrap toggle (#1412): default on; off flips the pane-root
    // attribute and code lines stop wrapping; toggled back on so the
    // persisted preference does not leak into later steps.
    const wrapState = await evalValue(
      send,
      `(() => document.querySelector("[data-ev-root]").getAttribute("data-ev-wrap"))()`,
    );
    if (wrapState !== "on") {
      throw new Error(`wrap default = ${JSON.stringify(wrapState)}, want on`);
    }
    await evalValue(
      send,
      `(() => { const b = document.querySelector("[data-ev-wrap-toggle]"); if (!b) return "no wrap toggle"; b.click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => {
        const root = document.querySelector("[data-ev-root]");
        const line = document.querySelector(".ev-codeview .ev-line");
        if (!root || root.getAttribute("data-ev-wrap") !== "off") return "attr not off";
        if (localStorage.getItem("strike.editorWrap") !== "off") return "pref not stored";
        return line && getComputedStyle(line).whiteSpace === "pre" ? "ok" : "white-space " + (line ? getComputedStyle(line).whiteSpace : "no line");
      })()`,
      "wrap off",
    );
    await evalValue(
      send,
      `(() => { document.querySelector("[data-ev-wrap-toggle]").click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => document.querySelector("[data-ev-root]").getAttribute("data-ev-wrap") === "on" ? "ok" : "attr not back on")()`,
      "wrap back on",
    );

    // Reload (#1407): a disk change stays stale until Reload, then
    // renders; restored to the original content for later steps.
    await writeFile(sample, "alpha beta reloaded\n");
    await evalValue(
      send,
      `(() => { const b = document.querySelector("[data-ev-reload-bar]"); if (!b || b.disabled) return "no reload"; b.click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => document.querySelector("[data-ev-root]").innerText.includes("alpha beta reloaded") ? "ok" : "not reloaded")()`,
      "reload shows disk change",
    );
    // Restore the exact original (the 5-line sample the cursor/Tab
    // steps below depend on), not just its first line.
    await writeFile(sample, "alpha beta\nsecond line\nthird line\nfourth line\nfifth line\n");
    await evalValue(
      send,
      `(() => { document.querySelector("[data-ev-reload-bar]").click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => {
        const t = document.querySelector("[data-ev-root]").innerText;
        return t.includes("alpha beta") && !t.includes("reloaded") ? "ok" : "not restored";
      })()`,
      "reload restores original",
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
    await evalValue(
      send,
      `(() => {
        const ta = document.querySelector(".ev-editor");
        ta.focus();
        // Select lines 2-3 and Tab: both indent, focus stays in the editor.
        ta.setSelectionRange(11, 27);
        ta.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true }));
        return "ok";
      })()`,
    );
    // The draft updates asynchronously through Leptos; poll for the value.
    await waitFor(
      send,
      `(() => {
        const ta = document.querySelector(".ev-editor");
        if (!ta) return "no editor";
        if (!(ta.value.includes("  second line") && ta.value.includes("  third line"))) return ta.value;
        const id = document.activeElement && document.activeElement.id;
        return id === "ev-editor" ? "ok" : "focus left the editor: " + id;
      })()`,
      "tab indent selection keeps focus",
    );
    // Ctrl+G then Escape dismisses the go-to box only: the dirty draft
    // and edit mode survive, and focus returns to the editor.
    await evalValue(
      send,
      `(() => {
        const ta = document.querySelector(".ev-editor");
        ta.dispatchEvent(new KeyboardEvent("keydown", { key: "g", ctrlKey: true, bubbles: true, cancelable: true }));
        const input = document.getElementById("ev-goto");
        input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
        return "ok";
      })()`,
    );
    await waitFor(
      send,
      `(() => {
        const ta = document.querySelector(".ev-editor");
        if (!ta) return "edit mode closed by goto Escape";
        if (!ta.value.includes("  second line")) return "draft lost: " + ta.value;
        const id = document.activeElement && document.activeElement.id;
        return id === "ev-editor" ? "ok" : "focus = " + id;
      })()`,
      "goto Escape keeps the draft",
    );
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
        const want = "alpha beta\\nsecond line\\nthird line\\nfourth line\\nfifth line\\n";
        return ta && ta.value === want ? "ok" : (ta ? JSON.stringify(ta.value) : "no editor");
      })()`,
      "shift+tab outdent",
    );
    // Edit-mode go-to-line moves the caret to the line start (#1403).
    await evalValue(
      send,
      `(() => {
        const input = document.getElementById("ev-goto");
        input.value = "3";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
        return "ok";
      })()`,
    );
    await waitFor(
      send,
      `(() => {
        const ta = document.querySelector(".ev-editor");
        const n = document.querySelector("[data-ev-cursor]");
        if (!ta || ta.selectionStart !== 23 || ta.selectionEnd !== 23) return "caret " + (ta ? ta.selectionStart + "-" + ta.selectionEnd : "no editor");
        return n && n.textContent.includes("Ln 3, Col 1") ? "ok" : (n ? n.textContent : "no cursor readout");
      })()`,
      "edit-mode goto moves the caret",
    );
    // Escape still leaves edit mode after Tab use (#1408); the draft is
    // clean again, so no guard prompt. Re-enter Edit for the guard case.
    await evalValue(
      send,
      `(() => {
        const ta = document.querySelector(".ev-editor");
        ta.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
        return "ok";
      })()`,
    );
    await waitFor(
      send,
      `(() => document.querySelector(".ev-editor") ? "still editing" : "ok")()`,
      "Escape exits edit mode",
    );
    await evalValue(
      send,
      `(() => { const b = document.querySelector("[data-ev-edit]"); if (!b || b.disabled) return "edit disabled"; b.click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => document.querySelector(".ev-editor") ? "ok" : "no editor")()`,
      "editor textarea (re-entered)",
    );

    // Dirty guard (#1394): an unsaved draft must block navigation.
    // Files-pane switch: Stay keeps the draft, Save writes then
    // switches, and a later Back to chat offers Discard. A synthetic
    // beforeunload must be cancelled while dirty.
    const setDraft = async (text) => {
      await evalValue(
        send,
        `(() => {
          const ta = document.querySelector(".ev-editor");
          if (!ta) return "no editor";
          ta.value = ${JSON.stringify(text)};
          ta.dispatchEvent(new Event("input", { bubbles: true }));
          return "ok";
        })()`,
      );
    };
    await setDraft("alpha edited\n");
    // Reload is disabled while the draft is dirty (#1407).
    await waitFor(
      send,
      `(() => { const b = document.querySelector("[data-ev-reload-bar]"); return b && b.disabled ? "ok" : "reload not disabled while dirty"; })()`,
      "reload disabled while dirty",
    );
    await waitFor(
      send,
      `(() => document.querySelector('[data-ev-open="editor-other.txt"]') ? "ok" : "no other file row")()`,
      "open button for editor-other.txt",
    );
    await evalValue(
      send,
      `(() => { document.querySelector('[data-ev-open="editor-other.txt"]').click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => {
        const guard = document.querySelector("[data-ev-unsaved-guard]");
        if (!guard) return "no guard";
        const ta = document.querySelector(".ev-editor");
        return ta && ta.value === "alpha edited\\n" ? "ok" : "draft lost: " + (ta ? ta.value : "no editor");
      })()`,
      "dirty guard on files switch (draft kept)",
    );
    const unloadBlocked = await evalValue(
      send,
      `(() => { const ev = new Event("beforeunload", { cancelable: true }); window.dispatchEvent(ev); return ev.defaultPrevented ? "ok" : "not prevented"; })()`,
    );
    if (unloadBlocked !== "ok") {
      throw new Error(`beforeunload while dirty: ${unloadBlocked}`);
    }
    await evalValue(
      send,
      `(() => { const b = document.querySelector("[data-ev-guard-stay]"); if (!b) return "no stay"; b.click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => {
        if (document.querySelector("[data-ev-unsaved-guard]")) return "guard still open";
        const ta = document.querySelector(".ev-editor");
        return ta && ta.value === "alpha edited\\n" ? "ok" : "draft lost after Stay";
      })()`,
      "Stay keeps the draft",
    );
    // Keyboard path: Escape while the prompt is open is Stay, not
    // the editor's edit-cancel — the draft must survive it.
    await evalValue(
      send,
      `(() => { document.querySelector('[data-ev-open="editor-other.txt"]').click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => document.querySelector("[data-ev-unsaved-guard]") ? "ok" : "no guard for Escape case")()`,
      "dirty guard for Escape case",
    );
    await evalValue(
      send,
      `(() => {
        const pane = document.querySelector(".ev-doc");
        pane.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
        return "ok";
      })()`,
    );
    await waitFor(
      send,
      `(() => {
        if (document.querySelector("[data-ev-unsaved-guard]")) return "guard still open after Escape";
        const ta = document.querySelector(".ev-editor");
        return ta && ta.value === "alpha edited\\n" ? "ok" : "draft lost after Escape";
      })()`,
      "Escape is Stay while the guard is open",
    );
    await evalValue(
      send,
      `(() => { document.querySelector('[data-ev-open="editor-other.txt"]').click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => document.querySelector("[data-ev-unsaved-guard]") ? "ok" : "no guard on second switch")()`,
      "dirty guard on second files switch",
    );
    await evalValue(
      send,
      `(() => { const b = document.querySelector("[data-ev-guard-save]"); if (!b) return "no guard save"; b.click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => {
        const root = document.querySelector("[data-ev-root]");
        if (!root) return "no pane";
        if (document.querySelector("[data-ev-unsaved-guard]")) return "guard still open";
        return root.innerText.includes("other content") ? "ok" : root.innerText.slice(0, 180);
      })()`,
      "guard Save writes then switches",
    );
    const saved = await fetch(
      `http://127.0.0.1:${port}/v1/file/view?path=editor-smoke.txt`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    const savedBody = await saved.json();
    if (savedBody.content !== "alpha edited\n") {
      throw new Error(`guard Save did not write: ${JSON.stringify(savedBody).slice(0, 200)}`);
    }
    // Back to chat with a fresh dirty draft: Discard drops it.
    await evalValue(
      send,
      `(() => { const b = [...document.querySelectorAll("button")].find(el => el.textContent.trim() === "Back to chat"); if (b) b.click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => document.querySelector("textarea") ? "ok" : "no composer")()`,
      "composer before discard case",
    );
    await evalValue(
      send,
      `(() => { const b = [...document.querySelectorAll("button")].find(el => el.textContent.trim() === "Files"); if (!b) return "no files tab"; b.click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => document.querySelector('[data-ev-open="editor-smoke.txt"]') ? "ok" : "no sample row")()`,
      "open button for editor-smoke.txt (discard case)",
    );
    await evalValue(
      send,
      `(() => { document.querySelector('[data-ev-open="editor-smoke.txt"]').click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => {
        const root = document.querySelector("[data-ev-root]");
        return root && root.innerText.includes("alpha edited") ? "ok" : "sample did not reopen";
      })()`,
      "sample reopened for discard case",
    );
    await evalValue(
      send,
      `(() => { const b = document.querySelector("[data-ev-edit]"); if (!b || b.disabled) return "edit disabled"; b.click(); return "ok"; })()`,
    );
    await setDraft("discard me\n");
    await evalValue(
      send,
      `(() => { const b = document.querySelector("[data-ev-back]"); if (!b) return "no back"; b.click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => {
        const guard = document.querySelector("[data-ev-unsaved-guard]");
        if (!guard) return "no guard on Back to chat";
        return guard.innerText.includes("Back to chat") ? "ok" : guard.innerText.slice(0, 160);
      })()`,
      "dirty guard on Back to chat",
    );
    await evalValue(
      send,
      `(() => { const b = document.querySelector("[data-ev-guard-discard]"); if (!b) return "no discard"; b.click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => document.querySelector("textarea") ? "ok" : "no composer after Discard")()`,
      "Discard leaves to chat",
    );
    const afterDiscard = await fetch(
      `http://127.0.0.1:${port}/v1/file/view?path=editor-smoke.txt`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    const afterDiscardBody = await afterDiscard.json();
    if (afterDiscardBody.content !== "alpha edited\n") {
      throw new Error(`Discard wrote the draft: ${JSON.stringify(afterDiscardBody).slice(0, 200)}`);
    }
    // Restore the original content and editing state for the
    // conflict case below.
    await writeFile(sample, "alpha beta\n");
    await evalValue(
      send,
      `(() => { const b = [...document.querySelectorAll("button")].find(el => el.textContent.trim() === "Files"); if (!b) return "no files tab"; b.click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => document.querySelector('[data-ev-open="editor-smoke.txt"]') ? "ok" : "no sample row")()`,
      "open button for editor-smoke.txt (conflict restore)",
    );
    await evalValue(
      send,
      `(() => { document.querySelector('[data-ev-open="editor-smoke.txt"]').click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => {
        const root = document.querySelector("[data-ev-root]");
        return root && root.innerText.includes("alpha beta") ? "ok" : "sample did not reopen for conflict case";
      })()`,
      "sample reopened for conflict case",
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
    // Copy path (#1410): the chip must surface its outcome in the
    // status line — "Path copied." only when the write resolved, the
    // generic failure line otherwise, never silence. Every load clears
    // the find, so set one here ("alpha" hits line 1 of the draft):
    // the copied text then takes path:line form.
    await evalValue(
      send,
      `(() => {
        const input = document.getElementById("ev-search");
        if (!input) return "no search";
        input.value = "alpha";
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
      "copy path find count",
    );
    try {
      await send("Browser.grantPermissions", {
        permissions: ["clipboardReadWrite", "clipboardSanitizedWrite"],
        origin: `http://127.0.0.1:${port}`,
      });
    } catch {
      // Older Chrome: the status-line assertion below still holds.
    }
    await evalValue(
      send,
      `(() => { const b = document.querySelector("[data-ev-copy-path]"); if (!b || b.disabled) return "no copy path chip"; b.click(); return "ok"; })()`,
    );
    const copyStatus = await waitFor(
      send,
      `(() => {
        const s = document.querySelector("[data-ev-status]");
        const t = s ? s.innerText : "";
        return t.includes("Path copied.") || t.includes("Copy didn't work") ? "ok" : t.slice(0, 120);
      })()`,
      "copy path status",
    );
    if (copyStatus === "ok") {
      const st = await evalValue(
        send,
        `(() => document.querySelector("[data-ev-status]").innerText)()`,
      );
      if (st.includes("Path copied.")) {
        await evalValue(
          send,
          `(() => { window.__clip = "pending"; navigator.clipboard.readText().then((t) => { window.__clip = t; }).catch(() => { window.__clip = "unreadable"; }); return "ok"; })()`,
        );
        let clip = "pending";
        for (let i = 0; i < 10 && clip === "pending"; i++) {
          await delay(200);
          clip = await evalValue(send, `window.__clip`);
        }
        // readText needs a focused document; assert only when it works.
        if (typeof clip === "string" && clip !== "pending" && clip !== "unreadable" && clip !== "" && clip !== "editor-smoke.txt:1") {
          throw new Error(`clipboard holds ${JSON.stringify(clip)}, want editor-smoke.txt:1`);
        }
      }
    }

    // Download in Edit mode (#1420 Q1): the saved copy is the current
    // buffer ("alpha edited"), not the on-disk "agent" version, and
    // the status line says unsaved changes were included.
    const editDl = await captureDownload("editor-smoke.txt");
    if (editDl !== "alpha edited\n") {
      throw new Error(`edit-mode download = ${JSON.stringify(editDl)}`);
    }
    await waitFor(
      send,
      `(() => {
        const s = document.querySelector("[data-ev-status]");
        return s && s.innerText.includes("unsaved changes") ? "ok" : (s ? s.innerText.slice(0, 120) : "no status");
      })()`,
      "download unsaved note",
    );

    const disk = await fetch(
      `http://127.0.0.1:${port}/v1/file/view?path=editor-smoke.txt`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    const body = await disk.json();
    if (body.content !== "agent\n") {
      throw new Error(`conflict wrote the file: ${JSON.stringify(body).slice(0, 200)}`);
    }

    // Long file (#1431): the render window starts at 2,000 lines,
    // "Show more" raises it a chunk at a time, "Show all" reaches the
    // tail, and a find hit past the cap auto-raises the window.
    const longLines = Array.from({ length: 5000 }, (_, i) => `line ${i + 1}`);
    longLines[4499] = "zebra-tail unique marker";
    longLines[4999] = "tail line 5000";
    await writeFile(long, `${longLines.join("\n")}\n`);
    const openLongFile = async () => {
      await evalValue(
        send,
        `(() => { const b = [...document.querySelectorAll("button")].find(el => el.textContent.trim() === "Back to chat"); if (b) b.click(); return "ok"; })()`,
      );
      // The conflict case above leaves a dirty draft, so Back to
      // chat is held by the #1394 guard: discard to proceed.
      await evalValue(
        send,
        `(() => { const b = document.querySelector("[data-ev-guard-discard]"); if (b) b.click(); return "ok"; })()`,
      );
      await waitFor(
        send,
        `(() => document.querySelector("textarea") ? "ok" : "no composer")()`,
        "composer before long file",
      );
      const tab = await evalValue(
        send,
        `(() => { const b = [...document.querySelectorAll("button")].find(el => el.textContent.trim() === "Files"); if (!b) return "no files tab"; b.click(); return "ok"; })()`,
      );
      if (tab !== "ok") {
        throw new Error(`files tab click (long): ${tab}`);
      }
      await waitFor(
        send,
        `(() => document.querySelector('[data-ev-open="editor-long.txt"]') ? "ok" : "no long file row")()`,
        "open button for editor-long.txt",
      );
      await evalValue(
        send,
        `(() => { document.querySelector('[data-ev-open="editor-long.txt"]').click(); return "ok"; })()`,
      );
    };
    await openLongFile();
    await waitFor(
      send,
      `(() => {
        if (!document.querySelector("#ev-line-2000")) return "no line 2000";
        return document.querySelector("#ev-line-2001") ? "line 2001 rendered too early" : "ok";
      })()`,
      "long file first window",
    );
    const more = await evalValue(
      send,
      `(() => { const b = document.querySelector("[data-ev-show-more]"); if (!b) return "no show more"; b.click(); return "ok"; })()`,
    );
    if (more !== "ok") {
      throw new Error(`show more click: ${more}`);
    }
    await waitFor(
      send,
      `(() => {
        if (!document.querySelector("#ev-line-4000")) return "no line 4000";
        return document.querySelector("#ev-line-4001") ? "line 4001 rendered too early" : "ok";
      })()`,
      "long file second window",
    );
    const all = await evalValue(
      send,
      `(() => { const b = document.querySelector("[data-ev-show-all]"); if (!b) return "no show all"; b.click(); return "ok"; })()`,
    );
    if (all !== "ok") {
      throw new Error(`show all click: ${all}`);
    }
    await waitFor(
      send,
      `(() => document.querySelector("#ev-line-5000") ? "ok" : "no line 5000")()`,
      "long file tail after show all",
    );
    // Reopen and find the marker at line 4,500: the window must raise
    // itself so the hit row exists (previously a silent no-op).
    await openLongFile();
    await evalValue(
      send,
      `(() => {
        const input = document.getElementById("ev-search");
        input.value = "zebra-tail";
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
      "long file search count",
    );
    await waitFor(
      send,
      `(() => document.querySelector("#ev-line-4500") ? "ok" : "hit row beyond the window")()`,
      "long file hit row auto-raised",
    );

    // Download (#1420): the saved copy is the whole file, past the
    // 2,000-line render cap — select-all on the read view cannot do
    // this, the Blob download must.
    const longDl = await captureDownload("editor-long.txt");
    if (longDl !== `${longLines.join("\n")}\n`) {
      throw new Error(
        `long download has ${longDl.split("\n").length} lines, want 5001 segments (5000 lines + trailing newline)`,
      );
    }

    // Markdown view (#1421): a fenced block renders with the fence bar
    // — language label plus a Copy button whose label reports the
    // outcome — instead of the old bare <pre>.
    await writeFile(md, "# Notes\n\n```rust\nfn greet() { println!(\"hi\"); }\n```\n");
    await evalValue(
      send,
      `(() => { const b = [...document.querySelectorAll("button")].find(el => el.textContent.trim() === "Back to chat"); if (b) b.click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => document.querySelector("textarea") ? "ok" : "no composer")()`,
      "composer before md file",
    );
    const mdTab = await evalValue(
      send,
      `(() => { const b = [...document.querySelectorAll("button")].find(el => el.textContent.trim() === "Files"); if (!b) return "no files tab"; b.click(); return "ok"; })()`,
    );
    if (mdTab !== "ok") {
      throw new Error(`files tab click (md): ${mdTab}`);
    }
    await waitFor(
      send,
      `(() => document.querySelector('[data-ev-open="editor-md.md"]') ? "ok" : "no md file row")()`,
      "open button for editor-md.md",
    );
    await evalValue(
      send,
      `(() => { document.querySelector('[data-ev-open="editor-md.md"]').click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => {
        const b = document.querySelector("[data-ev-copy-code]");
        if (!b) return "no copy-code button";
        const bar = b.closest(".sx-fence-bar");
        return bar && bar.innerText.includes("rust") ? "ok" : "no lang label: " + (bar ? bar.innerText : "no bar");
      })()`,
      "md fence bar with lang and copy",
    );
    await evalValue(
      send,
      `(() => { document.querySelector("[data-ev-copy-code]").click(); return "ok"; })()`,
    );
    await waitFor(
      send,
      `(() => {
        const b = document.querySelector("[data-ev-copy-code]");
        const t = b ? b.textContent.trim() : "gone";
        return t === "Copied" || t === "Copy failed" ? "ok" : "label=" + t;
      })()`,
      "md fence copy feedback",
    );

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
    await rm(other, { force: true });
    await rm(long, { force: true });
    await rm(md, { force: true });
  }
}
