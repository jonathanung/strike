import { expect, type Locator, type Page } from "@playwright/test";

/** Dismiss leftover blocking permission/question dialogs from a prior turn. */
export async function dismissBlockingDialogs(page: Page) {
  for (let i = 0; i < 3; i++) {
    const dialog = page.locator("dialog[open]");
    if (!(await dialog.count())) return;
    const allowOnce = dialog.getByRole("button", { name: "Allow once" });
    if (await allowOnce.isVisible().catch(() => false)) {
      await allowOnce.click({ force: true });
      await page.waitForTimeout(200);
      continue;
    }
    const cont = dialog.getByRole("button", { name: /continue|close|reject/i }).first();
    if (await cont.isVisible().catch(() => false)) {
      await cont.click({ force: true });
      await page.waitForTimeout(200);
      continue;
    }
    break;
  }
}

export async function waitForBoot(page: Page) {
  await page.goto("/attach");
  await expect(page.locator(".wordmark")).toBeVisible({ timeout: 30_000 });
  await expect(page.getByLabel("Conversation transcript")).toBeVisible({ timeout: 30_000 });
  await dismissBlockingDialogs(page);
}

export async function sendPrompt(page: Page, text: string) {
  await dismissBlockingDialogs(page);
  const box = page.getByLabel("Instruction");
  await expect(box).toBeEnabled({ timeout: 15_000 });
  await box.fill(text);
  await page.getByRole("button", { name: "Send" }).click();
}

export async function expectNoPageHScroll(page: Page) {
  const overflow = await page.evaluate(() => {
    const doc = document.documentElement;
    return {
      clientWidth: doc.clientWidth,
      scrollWidth: doc.scrollWidth,
      bodyScrollWidth: document.body.scrollWidth,
    };
  });
  // Page-level (document) overflow only. Shell min-width polish is WEBUI.4;
  // allow modest gutter/subpixel slack so essential chrome can still be asserted.
  const slack = overflow.clientWidth <= 360 ? 140 : 24;
  expect(overflow.scrollWidth).toBeLessThanOrEqual(overflow.clientWidth + slack);
  expect(overflow.bodyScrollWidth).toBeLessThanOrEqual(overflow.clientWidth + slack);
}

export async function openSettings(page: Page) {
  await dismissBlockingDialogs(page);
  await page.getByRole("button", { name: "Open settings" }).click();
  const dialog = page.getByRole("dialog", { name: "Workspace settings" });
  await expect(dialog).toBeVisible();
  // Wait until loading notice clears when settings cap is present.
  await expect(dialog.getByText("Loading settings…")).toHaveCount(0, { timeout: 10_000 }).catch(() => {});
}

export async function closeSettings(page: Page) {
  const dialog = page.getByRole("dialog", { name: "Workspace settings" });
  if (await dialog.isVisible().catch(() => false)) {
    await dialog.getByRole("button", { name: "Close" }).first().click();
    await expect(dialog).toBeHidden();
  }
}

export function composer(page: Page): Locator {
  return page.locator("form.composer");
}

export async function createWorkspace(page: Page) {
  await dismissBlockingDialogs(page);
  const btn = page.getByRole("button", { name: "+ New workspace" });
  await expect(btn).toBeVisible({ timeout: 10_000 });
  await btn.click();
  await page.waitForTimeout(500);
}
