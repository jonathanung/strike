// Out-of-order completions against a real `strike serve`.
// fetch is wrapped so the first matching response is held until release.
// The daemon still produces the body; only delivery order changes.
// A providers reload failure is a one-shot HTTP 500 from the same shim
// (the issue's failure shim). Everything else is the daemon's own bytes.

import { delay } from "../browser.mjs";

const LONG = "q".repeat(201);

async function installShim(page) {
  const ok = await page.evaluate(`(() => {
    if (window.__strikeOrigFetch) return true;
    window.__strikeOrigFetch = window.fetch.bind(window);
    window.__strikeHoldPath = "";
    window.__strikeFailPath = "";
    window.__strikeRelease = null;
    window.__strikeHeld = "";
    const matches = (url, path, method) => {
      if (!path || !url.includes(path)) return false;
      if (path === "/v1/usage") return method === "GET" && !url.includes("/v1/usage/budget");
      if (path === "/v1/pair") return method === "POST" && /\\/v1\\/pair(\\?|$)/.test(url);
      if (path === "/v1/providers") return method === "GET" && !url.includes("/v1/custom-providers");
      return true;
    };
    window.fetch = async function (input, init) {
      const url = typeof input === "string" ? input : (input && input.url) || "";
      const method = ((init && init.method) || (typeof input !== "string" && input && input.method) || "GET").toUpperCase();
      if (window.__strikeFailPath && matches(url, window.__strikeFailPath, method)) {
        window.__strikeFailPath = "";
        return new Response(JSON.stringify({ error: "could not load" }), {
          status: 500,
          headers: { "content-type": "application/json" },
        });
      }
      const hold = window.__strikeHoldPath && matches(url, window.__strikeHoldPath, method);
      if (hold) window.__strikeHoldPath = "";
      const res = await window.__strikeOrigFetch(input, init);
      if (hold) {
        window.__strikeHeld = url;
        await new Promise((resolve) => {
          window.__strikeRelease = resolve;
        });
        window.__strikeRelease = null;
      }
      return res;
    };
    return true;
  })()`);
  return Boolean(ok);
}

async function armHold(page, path) {
  await page.evaluate(`(() => { window.__strikeHeld = ""; window.__strikeHoldPath = ${JSON.stringify(path)}; return true; })()`);
}

async function armFail(page, path) {
  await page.evaluate(`(() => { window.__strikeFailPath = ${JSON.stringify(path)}; return true; })()`);
}

async function release(page) {
  await page.evaluate(`(() => { if (window.__strikeRelease) window.__strikeRelease(); return true; })()`);
}

async function setInput(page, selector, value) {
  return page.evaluate(`(() => {
    const input = document.querySelector(${JSON.stringify(selector)});
    if (!input) return false;
    const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
    proto.set.call(input, ${JSON.stringify(value)});
    input.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(value)} }));
    return input.value === ${JSON.stringify(value)};
  })()`);
}

async function openCommand(ctx, query) {
  await ctx.press("k", 2);
  const opened = await ctx.waitUntil(
    `Boolean(document.querySelector(".palette input"))`,
    4000,
    `palette ${query}`,
  );
  if (!opened) return false;
  const typed = await setInput(ctx.page, ".palette input", query);
  if (!typed) return false;
  await delay(150);
  await ctx.press("Enter");
  return true;
}

async function shootMobile(ctx, name) {
  await ctx.viewport(390, 844);
  await delay(150);
  await ctx.page.evaluate(`(() => {
    const btn = [...document.querySelectorAll("button")].find((b) => (b.innerText || "").trim() === "Sessions");
    if (btn) btn.click();
    return true;
  })()`);
  await delay(150);
  await ctx.shoot(name, ["mobile"]);
}

export async function scenario(ctx) {
  const { page, check, waitUntil, click, shoot, log } = ctx;
  const shim = await installShim(page);
  check(shim, "fetch shim did not install");

  log("search: late empty hit must not clear the current error");
  await armHold(page, "/v1/session-list/search");
  const armedSearch = await setInput(page, ".ss-search", "zzzz-nomatch-a");
  check(armedSearch, "session search box missing");
  const searchHeld = await waitUntil(
    `window.__strikeHeld && window.__strikeHeld.includes("/v1/session-list/search")`,
    4000,
    "first search held",
  );
  check(Boolean(searchHeld), "first session search was not held");
  const longSet = await setInput(page, ".ss-search", LONG);
  check(longSet, "long search query did not stick");
  const errored = await waitUntil(
    `document.body && document.body.innerText.includes("HTTP 400")`,
    4000,
    "search error",
  );
  check(Boolean(errored), "current search did not show HTTP 400");
  await release(page);
  await delay(500);
  const stillError = await page.evaluate(
    `document.body && document.body.innerText.includes("HTTP 400") && (document.querySelector(".ss-search") || {}).value.length === 201`,
  );
  check(Boolean(stillError), "late search success cleared the current query error");
  await shoot("races-search-error", ["desk1024", "desk1440"]);
  await shootMobile(ctx, "races-search-error-phone");
  await setInput(page, ".ss-search", "");
  await delay(300);
  const cleared = await page.evaluate(
    `!document.body.innerText.includes("HTTP 400") && (document.querySelector(".ss-search") || {}).value === ""`,
  );
  check(Boolean(cleared), "clearing the search box did not drop the error");

  log("browse: late folder must not replace the current path");
  await click("button.ws-open", "Change workspace");
  const browseReady = await waitUntil(
    `(() => {
      const dialog = document.querySelector(".ws-dialog");
      if (!dialog) return false;
      const section = [...dialog.querySelectorAll("section")].find((s) => (s.innerText || "").startsWith("Browse"));
      return Boolean(section && section.querySelectorAll("button.ws-row").length > 1);
    })()`,
    5000,
    "browse rows",
  );
  check(Boolean(browseReady), "workspace browse did not list two folders");
  if (browseReady) {
    const names = await page.evaluate(`(() => {
      const dialog = document.querySelector(".ws-dialog");
      const section = [...dialog.querySelectorAll("section")].find((s) => (s.innerText || "").startsWith("Browse"));
      return [...section.querySelectorAll("button.ws-row .name")].map((n) => (n.innerText || "").trim());
    })()`);
    const folderA = names[0];
    const folderB = names[1];
    await armHold(page, "/v1/workspaces/browse");
    await page.evaluate(`(() => {
      const dialog = document.querySelector(".ws-dialog");
      const section = [...dialog.querySelectorAll("section")].find((s) => (s.innerText || "").startsWith("Browse"));
      section.querySelectorAll("button.ws-row")[0].click();
      return true;
    })()`);
    const browseHeld = await waitUntil(
      `window.__strikeHeld && window.__strikeHeld.includes("/v1/workspaces/browse")`,
      4000,
      "browse A held",
    );
    check(Boolean(browseHeld), "first browse response was not held");
    await page.evaluate(`(() => {
      const dialog = document.querySelector(".ws-dialog");
      const section = [...dialog.querySelectorAll("section")].find((s) => (s.innerText || "").startsWith("Browse"));
      section.querySelectorAll("button.ws-row")[1].click();
      return true;
    })()`);
    const pathExpr = `(() => {
      const dialog = document.querySelector(".ws-dialog");
      if (!dialog) return "";
      const section = [...dialog.querySelectorAll("section")].find((s) => (s.innerText || "").startsWith("Browse"));
      const path = section && section.querySelector(".path");
      return path ? path.innerText : "";
    })()`;
    const onB = await waitUntil(
      `(() => { const text = ${pathExpr}; return text.includes(${JSON.stringify(folderB)}); })()`,
      4000,
      "browse B painted",
    );
    check(Boolean(onB), `browse did not show ${folderB} (path wait failed)`);
    await release(page);
    await delay(400);
    const stayed = await page.evaluate(`(() => {
      const text = ${pathExpr};
      return { text, ok: text.includes(${JSON.stringify(folderB)}) && !text.endsWith("/" + ${JSON.stringify(folderA)}) && !text.endsWith(${JSON.stringify(folderA)}) };
    })()`);
    check(Boolean(stayed && stayed.ok), `late browse path is ${stayed && stayed.text} after racing ${folderA} then ${folderB}`);
    await shoot("races-browse", ["desk1024", "desk1440", "mobile"]);
    await ctx.press("Escape");
  }

  log("pairing: New code stays disabled until the latest mint returns");
  await armHold(page, "/v1/pair");
  const phone = await openCommand(ctx, "phone");
  check(phone, "could not open the phone dialog");
  const disabled = await waitUntil(
    `(() => { const b = document.querySelector("button.gq-new"); return Boolean(b && b.disabled); })()`,
    4000,
    "new code disabled",
  );
  check(Boolean(disabled), "New code was not disabled while the mint was in flight");
  await shoot("races-pair-disabled", ["desk1024", "desk1440", "mobile"]);
  await release(page);
  const enabled = await waitUntil(
    `(() => { const b = document.querySelector("button.gq-new"); return Boolean(b && !b.disabled); })()`,
    4000,
    "new code enabled",
  );
  check(Boolean(enabled), "New code stayed disabled after the mint finished");
  await ctx.press("Escape");

  log("providers: failed reload keeps Connected rows and labels them stale");
  const providers = await openCommand(ctx, "providers");
  check(providers, "could not open providers");
  const listed = await waitUntil(
    `document.body && document.body.innerText.includes("Connected") && document.querySelector("[data-provider=\\"echo\\"]")`,
    8000,
    "echo provider",
  );
  check(Boolean(listed), "providers page did not show echo Connected");
  await armFail(page, "/v1/providers");
  await click("button", "Refresh");
  const stale = await waitUntil(
    `(() => {
      const note = document.querySelector("[data-providers-stale]");
      const echo = document.querySelector("[data-provider=\\"echo\\"]");
      return Boolean(note && note.innerText.includes("last successful load") && echo && echo.innerText.includes("Connected"));
    })()`,
    5000,
    "stale providers",
  );
  check(Boolean(stale), "failed reload cleared Connected rows or hid the stale label");
  await shoot("races-providers-stale", ["desk1024", "desk1440", "mobile"]);

  log("usage: a late prefetch must not restore the previous budget");
  const usage = await openCommand(ctx, "usage");
  check(usage, "could not open usage");
  const budgetBox = await waitUntil(`Boolean(document.querySelector("#daily-budget"))`, 5000, "budget field");
  check(Boolean(budgetBox), "daily budget field missing");
  if (budgetBox) {
    await setInput(page, "#daily-budget", "4");
    await click("button", "Save budget");
    const saved4 = await waitUntil(
      `document.body && document.body.innerText.includes("Ceiling $4")`,
      5000,
      "ceiling 4",
    );
    check(Boolean(saved4), "saving 4 did not paint Ceiling $4");
    await armHold(page, "/v1/usage");
    await setInput(page, "#daily-budget", "4");
    await click("button", "Save budget");
    const usageHeld = await waitUntil(
      `window.__strikeHeld && window.__strikeHeld.includes("/v1/usage") && !window.__strikeHeld.includes("budget")`,
      5000,
      "usage prefetch held",
    );
    check(Boolean(usageHeld), "pre-save usage response was not held");
    await setInput(page, "#daily-budget", "9");
    await click("button", "Save budget");
    const saved9 = await waitUntil(
      `document.body && document.body.innerText.includes("Ceiling $9") && (document.querySelector("#daily-budget") || {}).value === "9"`,
      5000,
      "ceiling 9",
    );
    check(Boolean(saved9), "saving 9 did not paint Ceiling $9");
    await release(page);
    await delay(500);
    const stayed9 = await page.evaluate(
      `document.body.innerText.includes("Ceiling $9") && (document.querySelector("#daily-budget") || {}).value === "9"`,
    );
    check(Boolean(stayed9), "late usage fetch restored the previous budget");
    await shoot("races-usage-budget", ["desk1024", "desk1440", "mobile"]);
  }
}
