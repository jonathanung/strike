// Triggers (#1282). Ctrl+K opens the page, Enter saves, Send test accepts,
// and a bad signature is rejected. The one-time secret stays masked and is
// dismissed before any shot.

export const scenario = async (ctx) => {
  await ctx.press("k", 2);
  const palette = await ctx.waitUntil(
    `Boolean(document.querySelector(".palette"))`,
    4000,
    "triggers palette",
  );
  ctx.check(Boolean(palette), "Ctrl+K did not open the palette");
  if (!palette) return;
  await ctx.page.evaluate(`document.querySelector(".palette input")?.focus()`);
  await ctx.page.send("Input.insertText", { text: "triggers" });
  await ctx.page.evaluate(`(() => {
    const input = document.querySelector(".palette input");
    if (!input || input.value.toLowerCase().includes("triggers")) return;
    const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
    proto.set.call(input, "triggers");
    input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "triggers" }));
  })()`);
  await ctx.waitUntil(
    `(() => {
      const input = document.querySelector(".palette input");
      return Boolean(input && input.value.toLowerCase().includes("triggers"));
    })()`,
    2000,
    "triggers palette query",
  );
  await ctx.press("Enter");
  const open = await ctx.waitUntil(
    `document.body && document.body.innerText.includes("New trigger") && document.body.innerText.includes("Delivery log")`,
    8000,
    "triggers page",
  );
  ctx.check(Boolean(open), "palette did not open Triggers");
  if (!open) return;
  await ctx.shoot("triggers-empty", ["desk1024", "desk1440", "mobile"]);
  await ctx.page.evaluate(`document.querySelector("#trigger-name")?.focus()`);
  await ctx.page.send("Input.insertText", { text: "uat hook" });
  await ctx.waitUntil(
    `(() => {
      const input = document.querySelector("#trigger-name");
      if (!input) return false;
      if (!input.value.toLowerCase().includes("uat hook")) {
        const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
        proto.set.call(input, "uat hook");
        input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "uat hook" }));
      }
      input.focus();
      return input.value.toLowerCase().includes("uat hook");
    })()`,
    2000,
    "trigger name",
  );
  await ctx.press("Enter");
  const saved = await ctx.waitUntil(
    `document.body && document.body.innerText.includes("Copy this signing secret now")`,
    10000,
    "trigger secret once",
  );
  ctx.check(Boolean(saved), "Enter did not save the trigger");
  if (saved) {
    await ctx.page.evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => (b.innerText || "").includes("Dismiss secret"));
      if (btn) btn.click();
    })()`);
    await ctx.waitUntil(
      `document.body && !document.body.innerText.includes("Copy this signing secret now")`,
      4000,
      "secret dismissed",
    );
  }
  await ctx.click("button", "Send test");
  const accepted = await ctx.waitUntil(
    `document.body && document.body.innerText.includes("Test accepted")`,
    10000,
    "trigger test accepted",
  );
  ctx.check(Boolean(accepted), "synthetic delivery was not accepted");
  await ctx.click("button", "Reject signature");
  const rejected = await ctx.waitUntil(
    `document.body && document.body.innerText.includes("Hook returned 401")`,
    10000,
    "trigger bad signature",
  );
  ctx.check(Boolean(rejected), "bad signature was not rejected");
  await ctx.shoot("triggers-log", ["desk1024", "desk1440"]);
  await ctx.viewport(390, 844);
  await ctx.page.evaluate(`document.querySelector(".triggers-log")?.scrollIntoView({block:"center"})`);
  await ctx.phone("triggers-log");
};
