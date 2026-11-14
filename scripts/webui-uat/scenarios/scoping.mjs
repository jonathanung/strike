// Identity scoping (#1622, #1625, #1642, #1643, #1644).
// Escape cancels only the confirm. A second ask keeps the first prompt.
// A trigger secret is labeled and cleared when the form moves on.
// A save that lands after a selection change does not hybridize the form.
// A rename started on one session does not commit onto the next.

export const scenario = async (ctx) => {
  await ctx.page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
  await ctx.waitUntil(
    `document.querySelector('[data-action="onboarding-continue"]') == null`,
    4000,
    "onboarding",
  );

  await openPalette(ctx, "gateways");
  const gateways = await ctx.waitUntil(
    `document.body && document.body.innerText.includes("Rotate token")`,
    8000,
    "gateways",
  );
  ctx.check(Boolean(gateways), "palette did not open Gateways");
  if (!gateways) return;

  await ctx.click("button", "Rotate token");
  const dialog = await ctx.waitUntil(
    `document.body && document.body.innerText.includes("Rotate token for")`,
    4000,
    "rotate confirm",
  );
  ctx.check(Boolean(dialog), "Rotate token did not open the confirm dialog");
  if (dialog) {
    await ctx.shoot("scoping-confirm-open", ["desk1024", "desk1440", "mobile"]);
    await ctx.press("Escape");
    const stayed = await ctx.waitUntil(
      `(() => {
        const text = document.body ? document.body.innerText : "";
        return text.includes("Rotate token") && !text.includes("Rotate token for");
      })()`,
      4000,
      "gateways after escape",
    );
    ctx.check(Boolean(stayed), "Escape closed the Gateways view with the confirm dialog");
    await ctx.shoot("scoping-confirm-after-escape", ["desk1024", "desk1440", "mobile"]);

    await ctx.click("button", "Rotate token");
    const again = await ctx.waitUntil(
      `document.querySelector("[data-confirm='dialog']")`,
      4000,
      "rotate confirm again",
    );
    ctx.check(Boolean(again), "second Rotate token did not open a confirm");
    if (again) {
      await ctx.page.evaluate(`(() => {
        const btn = [...document.querySelectorAll("button")].find((b) => (b.innerText || "").trim() === "Revoke grants");
        if (btn) btn.click();
      })()`);
      const kept = await ctx.waitUntil(
        `(() => {
          const title = document.querySelector("#confirm-title");
          const text = document.body ? document.body.innerText : "";
          return Boolean(title && title.textContent.includes("Rotate token")) && text.includes("Another confirmation is open");
        })()`,
        4000,
        "busy confirm",
      );
      ctx.check(Boolean(kept), "a second ask replaced the open confirm or hid the busy notice");
      await ctx.shoot("scoping-confirm-busy", ["desk1024", "desk1440", "mobile"]);
      await ctx.click("button", "Cancel");
      await ctx.waitUntil(
        `!document.querySelector("[data-confirm='dialog']")`,
        4000,
        "confirm cancelled",
      );
    }
  }

  await ctx.page.evaluate(`document.querySelector('[data-nav="primary"] button')?.click()`);
  await openPalette(ctx, "triggers");
  const triggers = await ctx.waitUntil(
    `document.body && document.body.innerText.includes("New trigger") && document.body.innerText.includes("Delivery log")`,
    8000,
    "triggers page",
  );
  ctx.check(Boolean(triggers), "palette did not open Triggers");
  if (!triggers) return;

  await saveTrigger(ctx, "Alpha hook");
  const labeled = await ctx.waitUntil(
    `document.body && document.body.innerText.includes("Copy the signing secret for Alpha hook")`,
    10000,
    "labeled secret",
  );
  ctx.check(Boolean(labeled), "secret banner was not labeled with the trigger name");
  if (labeled) {
    await ctx.shoot("scoping-secret-alpha", ["desk1024", "desk1440", "mobile"]);
    await ctx.click("button", "New");
    const cleared = await ctx.waitUntil(
      `document.body && !document.body.innerText.includes("Copy the signing secret") && document.body.innerText.includes("New trigger")`,
      4000,
      "secret cleared",
    );
    ctx.check(Boolean(cleared), "New left the previous trigger's secret on the form");
    await ctx.shoot("scoping-secret-cleared", ["desk1024", "desk1440", "mobile"]);
  }

  await saveTrigger(ctx, "Beta hook");
  await ctx.waitUntil(
    `document.body && document.body.innerText.includes("Copy the signing secret for Beta hook")`,
    10000,
    "beta secret",
  );
  await ctx.click("button", "Alpha hook");
  const onAlpha = await ctx.waitUntil(
    `(() => {
      const name = document.querySelector("#trigger-name");
      const text = document.body ? document.body.innerText : "";
      return Boolean(name && name.value === "Alpha hook") && !text.includes("Copy the signing secret");
    })()`,
    4000,
    "alpha selected",
  );
  ctx.check(Boolean(onAlpha), "selecting Alpha did not clear Beta's secret banner");

  await ctx.page.evaluate(`(() => {
    const orig = window.fetch.bind(window);
    window.fetch = (input, init) => {
      const url = typeof input === "string" ? input : (input && input.url) || "";
      const method = ((init && init.method) || (typeof input !== "string" && input && input.method) || "GET").toUpperCase();
      const slow = String(url).includes("/v1/triggers") && method !== "GET" && !String(url).includes("/test");
      const run = () => orig(input, init);
      return slow ? new Promise((resolve) => setTimeout(resolve, 1200)).then(run) : run();
    };
  })()`);
  await ctx.page.evaluate(`(() => {
    const input = document.querySelector("#trigger-prompt");
    if (!input) return;
    const proto = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
    proto.set.call(input, "alpha prompt");
    input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "alpha prompt" }));
  })()`);
  await ctx.click("button", "Save");
  await ctx.click("button", "Beta hook");
  const named = await ctx.waitUntil(
    `(() => {
      const text = document.body ? document.body.innerText : "";
      const name = document.querySelector("#trigger-name");
      return text.includes("Saved Alpha hook.") && Boolean(name && name.value === "Beta hook");
    })()`,
    8000,
    "saved alpha while beta is open",
  );
  ctx.check(Boolean(named), "a late Alpha save hybridized the Beta form");
  await ctx.shoot("scoping-save-beta", ["desk1024", "desk1440", "mobile"]);

  await ctx.page.evaluate(`document.querySelector('[data-nav="primary"] button')?.click()`);
  await ctx.waitUntil(
    `document.querySelector(".sx-title, .sx-rename")`,
    8000,
    "transcript",
  );
  const before = await ctx.page.evaluate(`document.querySelector(".sx-title")?.textContent || ""`);
  await ctx.click("button", "Rename");
  const editing = await ctx.waitUntil(
    `document.querySelector(".sx-rename")`,
    4000,
    "rename field",
  );
  ctx.check(Boolean(editing), "Rename did not open the title field");
  if (editing) {
    await ctx.page.evaluate(`(() => {
      const input = document.querySelector(".sx-rename");
      if (!input) return;
      const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
      proto.set.call(input, "draft from A");
      input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "draft from A" }));
    })()`);
    await ctx.shoot("scoping-rename-a", ["desk1024", "desk1440", "mobile"]);
    await ctx.click("button", "New");
    const moved = await ctx.waitUntil(
      `!document.querySelector(".sx-rename")`,
      4000,
      "rename cleared",
    );
    ctx.check(Boolean(moved), "switching sessions left the rename field open");
    const title = await ctx.page.evaluate(`document.querySelector(".sx-title")?.textContent || ""`);
    ctx.check(!String(title).includes("draft from A"), "the next session took the previous draft title");
    ctx.check(title !== before || !String(title).includes("draft from A"), "rename crossed sessions");
    await ctx.shoot("scoping-rename-b", ["desk1024", "desk1440", "mobile"]);
  }
};

async function openPalette(ctx, query) {
  await ctx.press("k", 2);
  const palette = await ctx.waitUntil(
    `Boolean(document.querySelector(".palette"))`,
    4000,
    `${query} palette`,
  );
  ctx.check(Boolean(palette), `Ctrl+K did not open the palette for ${query}`);
  if (!palette) return;
  await ctx.page.evaluate(`document.querySelector(".palette input")?.focus()`);
  await ctx.page.send("Input.insertText", { text: query });
  await ctx.page.evaluate(`(() => {
    const input = document.querySelector(".palette input");
    if (!input || input.value.toLowerCase().includes(${JSON.stringify(query)})) return;
    const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
    proto.set.call(input, ${JSON.stringify(query)});
    input.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(query)} }));
  })()`);
  await ctx.waitUntil(
    `(() => {
      const input = document.querySelector(".palette input");
      return Boolean(input && input.value.toLowerCase().includes(${JSON.stringify(query)}));
    })()`,
    2000,
    `${query} query`,
  );
  await ctx.press("Enter");
}

async function saveTrigger(ctx, name) {
  await ctx.page.evaluate(`(() => {
    const input = document.querySelector("#trigger-name");
    if (!input) return;
    input.focus();
    const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
    proto.set.call(input, ${JSON.stringify(name)});
    input.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(name)} }));
  })()`);
  await ctx.waitUntil(
    `document.querySelector("#trigger-name") && document.querySelector("#trigger-name").value === ${JSON.stringify(name)}`,
    2000,
    `name ${name}`,
  );
  await ctx.press("Enter");
}
