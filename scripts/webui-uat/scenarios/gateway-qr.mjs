// Unauthenticated phone dialog. Echo serve has no --auth, so the dialog
// states the loopback opt-in and does not mint a pairing code.
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
    `document.body && document.body.innerText.includes("A phone cannot open") && document.querySelector(".gq-qr svg")`,
    8000,
    "phone dialog",
  );
  check(Boolean(phoneDialog), "phone dialog missing opt-in or QR");
  const phoneFields = await page.evaluate(`(() => {
    const field = document.querySelector("#gq-url");
    const shown = document.querySelector(".gq-url")?.innerText || "";
    const value = field ? field.value : "";
    const body = document.body ? document.body.innerText : "";
    return {
      value,
      shown,
      bodyHasTokenQuery: body.includes("?token="),
      bodyHasPair: body.includes("#pair="),
    };
  })()`);
  check(
    phoneFields &&
      !String(phoneFields.value).includes("?token=") &&
      !String(phoneFields.value).includes("#pair="),
    "phone URL value leaked a secret",
  );
  check(
    phoneFields &&
      !String(phoneFields.shown).includes("?token=") &&
      !String(phoneFields.shown).includes("#pair="),
    "phone URL text leaked a secret",
  );
  check(
    phoneFields && !phoneFields.bodyHasTokenQuery && !phoneFields.bodyHasPair,
    "phone dialog body leaked a secret",
  );
  await shoot("shell-phone", ["desk1024", "desk1440", "mobile"]);
  // Clipboard honesty (#1582): force the Clipboard API to reject, click
  // Copy URL, and the dialog must NOT claim "Pairing URL copied." — it
  // must show the manual-copy fallback instead. Then let the write
  // resolve and confirm the fallback clears.
  await page.evaluate(`Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText: () => Promise.reject(new Error("denied")) },
  })`);
  const copyBtn = await page.evaluate(`(() => {
    const b = [...document.querySelectorAll(".gq-dialog button")]
      .find((x) => x.textContent.trim() === "Copy URL");
    if (b) b.click();
    return Boolean(b);
  })()`);
  check(Boolean(copyBtn), "Copy URL button missing from the phone dialog");
  const fallback = await waitUntil(
    `Boolean(document.querySelector(".gq-copy-fallback"))`,
    4000,
    "clipboard failure fallback",
  );
  check(Boolean(fallback), "rejected clipboard write did not show the manual-copy fallback");
  const claimedCopied = await page.evaluate(
    `document.body.innerText.includes("Pairing URL copied.")`,
  );
  check(!claimedCopied, "dialog claimed 'Pairing URL copied.' after a rejected write");
  await shoot("gateway-copy-failed", ["desk1440", "mobile"]);
  await page.evaluate(`Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText: () => Promise.resolve() },
  })`);
  await page.evaluate(`[...document.querySelectorAll(".gq-dialog button")]
    .find((x) => x.textContent.trim() === "Copy URL")?.click()`);
  const fallbackGone = await waitUntil(
    `!document.querySelector(".gq-copy-fallback")`,
    4000,
    "clipboard success clears fallback",
  );
  check(Boolean(fallbackGone), "successful copy did not clear the failure fallback");
  await click("button.gq-new", "New code");
  const noCode = await waitUntil(
    `document.body && document.body.innerText.includes("No pairing code")`,
    4000,
    "no pairing code",
  );
  check(Boolean(noCode), "New code did not explain that no pairing code was minted");
  await press("Escape");
  const phoneClosed = await waitUntil(
    `!document.querySelector(".gq-dialog")`,
    4000,
    "phone dialog closed",
  );
  check(Boolean(phoneClosed), "Escape did not close the phone dialog");
}
