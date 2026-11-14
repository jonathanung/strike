// Unauthenticated phone dialog. Echo serve has no --auth and listens on
// loopback, so the dialog states the loopback opt-in, mints no pairing
// code, and shows no scannable QR or copyable URL (#1522) — only the
// restart command for a phone-reachable bind.
import { delay } from "../browser.mjs";

export async function scenario(ctx) {
  const { page, press, click, waitUntil, check, shoot } = ctx;

  await press("k", 2);
  const phonePalette = await waitUntil(
    `Boolean(document.querySelector(".palette input"))`,
    4000,
    "phone palette",
  );
  if (phonePalette) {
    await page.evaluate(`document.querySelector(".palette input")?.focus()`);
    await page.send("Input.insertText", { text: "phone" });
    await delay(200);
    let typed = await page.evaluate(`document.querySelector(".palette input")?.value === "phone"`);
    if (!typed) {
      typed = await page.evaluate(`(() => {
        const input = document.querySelector(".palette input");
        if (!input) return false;
        const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
        proto.set.call(input, "phone");
        input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "phone" }));
        return input.value === "phone";
      })()`);
    }
    check(typed, "palette query did not accept phone");
    await page.evaluate(`document.querySelector(".palette input")?.focus()`);
    await press("Enter");
  }
  const phoneDialog = await waitUntil(
    `document.body && document.body.innerText.includes("A phone cannot open") && Boolean(document.querySelector(".gq-blocked"))`,
    8000,
    "phone dialog",
  );
  check(Boolean(phoneDialog), "phone dialog missing opt-in or loopback-blocked notice");
  const phoneFields = await page.evaluate(`(() => {
    const body = document.body ? document.body.innerText : "";
    return {
      qr: Boolean(document.querySelector(".gq-qr svg")),
      url: Boolean(document.querySelector("#gq-url")),
      restart: document.querySelector(".gq-cmd")?.innerText || "",
      bodyHasTokenQuery: body.includes("?token="),
      bodyHasPair: body.includes("#pair="),
    };
  })()`);
  check(phoneFields && !phoneFields.qr, "loopback gateway still rendered a scannable QR");
  check(phoneFields && !phoneFields.url, "loopback gateway still rendered a copyable pairing URL");
  check(
    phoneFields && phoneFields.restart.includes("strike daemon install"),
    "loopback-blocked dialog did not offer the restart command",
  );
  check(
    phoneFields && !phoneFields.bodyHasTokenQuery && !phoneFields.bodyHasPair,
    "phone dialog body leaked a secret",
  );
  await shoot("shell-phone", ["desk1024", "desk1440", "mobile"]);
  // Clipboard honesty (#1582): a rejected write of the restart command
  // must say so, never claim it was copied.
  await page.evaluate(`Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText: () => Promise.reject(new Error("denied")) },
  })`);
  const copyBtn = await page.evaluate(`(() => {
    const b = [...document.querySelectorAll(".gq-dialog button")]
      .find((x) => x.textContent.trim() === "Copy restart command");
    if (b) b.click();
    return Boolean(b);
  })()`);
  check(Boolean(copyBtn), "Copy restart command button missing from the phone dialog");
  const failed = await waitUntil(
    `Boolean(document.querySelector(".gq-blocked .gq-copy-fallback"))`,
    4000,
    "clipboard failure note",
  );
  check(Boolean(failed), "rejected clipboard write did not report the failure");
  check(
    !(await page.evaluate(`document.body.innerText.includes("Restart command copied.")`)),
    "dialog claimed 'Restart command copied.' after a rejected write",
  );
  await shoot("gateway-copy-failed", ["desk1440", "mobile"]);
  await page.evaluate(`Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText: () => Promise.resolve() },
  })`);
  await page.evaluate(`[...document.querySelectorAll(".gq-dialog button")]
    .find((x) => x.textContent.trim() === "Copy restart command")?.click()`);
  const fallbackGone = await waitUntil(
    `!document.querySelector(".gq-copy-fallback")`,
    4000,
    "clipboard success clears fallback",
  );
  check(Boolean(fallbackGone), "successful copy did not clear the failure fallback");
  await click("button.gq-new", "Check again");
  const noCode = await waitUntil(
    `document.body && document.body.innerText.includes("No pairing code") && Boolean(document.querySelector(".gq-blocked"))`,
    4000,
    "no pairing code",
  );
  check(Boolean(noCode), "Check again did not keep the no-code, no-QR explanation");
  await press("Escape");
  const phoneClosed = await waitUntil(
    `!document.querySelector(".gq-dialog")`,
    4000,
    "phone dialog closed",
  );
  check(Boolean(phoneClosed), "Escape did not close the phone dialog");
}
