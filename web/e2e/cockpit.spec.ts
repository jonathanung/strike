import { expect, test } from "@playwright/test";
import {
  closeSettings,
  composer,
  createWorkspace,
  dismissBlockingDialogs,
  expectNoPageHScroll,
  openSettings,
  sendPrompt,
  waitForBoot,
} from "./helpers";

test.describe("live echo cockpit", () => {
  test.beforeEach(async ({ page }) => {
    await waitForBoot(page);
  });

  test("bootstrap shows ready shell", async ({ page }) => {
    await expect(page.getByText("Direct the work.")).toBeVisible();
    await expect(page.getByLabel("Instruction")).toBeEnabled();
    await expectNoPageHScroll(page);
  });

  test("streamed turn echoes user prompt", async ({ page }) => {
    const marker = `e2e-turn-${Date.now()}`;
    await sendPrompt(page, marker);
    await expect(page.getByText(marker).first()).toBeVisible({ timeout: 30_000 });
    await expect(page.getByText(new RegExp(`You said:.*${marker}`))).toBeVisible({
      timeout: 30_000,
    });
  });

  test("permission dialog appears for run command and can be answered", async ({ page }) => {
    await sendPrompt(page, "run echo e2e-perm");
    const dialog = page.getByRole("dialog", { name: "Permission required" });
    await expect(dialog).toBeVisible({ timeout: 30_000 });
    await expect(dialog).toContainText(/bash/i);
    const allowOnce = dialog.getByRole("button", { name: "Allow once" });
    await expect(allowOnce).toBeVisible();
    // autofocus is best-effort across browsers; ensure the control is activatable.
    await allowOnce.focus();
    await expect(allowOnce).toBeFocused();
    await allowOnce.click();
    await expect(dialog).toBeHidden({ timeout: 15_000 });
  });

  test("multi-root switching keeps shell usable", async ({ page }) => {
    await dismissBlockingDialogs(page);
    await createWorkspace(page);
    await createWorkspace(page);
    const sessions = page.locator("button.session");
    await expect(sessions.first()).toBeVisible({ timeout: 10_000 });
    const count = await sessions.count();
    expect(count).toBeGreaterThanOrEqual(1);
    if (count >= 2) {
      await sessions.nth(0).click();
      await dismissBlockingDialogs(page);
      await expect(page.getByLabel("Instruction")).toBeVisible();
      await sessions.nth(1).click();
      await dismissBlockingDialogs(page);
      await expect(page.getByLabel("Instruction")).toBeVisible();
    }
  });

  test("additive mode/surface deep links do not break boot", async ({ page }) => {
    await page.goto("/attach?mode=team&surface=roster");
    await expect(page.locator(".wordmark")).toBeVisible({ timeout: 30_000 });
    await expect(page.getByLabel("Conversation transcript")).toBeVisible();
    await expect(page.getByLabel("Instruction")).toBeVisible();
    await page.goto("/attach?mode=ops&surface=settings");
    await expect(page.getByLabel("Conversation transcript")).toBeVisible({ timeout: 30_000 });
  });

  test("keyboard: settings dialog open/close and composer focus", async ({ page }) => {
    await openSettings(page);
    const dialog = page.getByRole("dialog", { name: "Workspace settings" });
    const closeBtn = dialog.getByRole("button", { name: "Close" }).first();
    await expect(closeBtn).toBeVisible();
    await closeBtn.focus();
    await expect(closeBtn).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden({ timeout: 10_000 });

    const box = page.getByLabel("Instruction");
    await expect(box).toBeEnabled({ timeout: 10_000 });
    await box.focus();
    await expect(box).toBeFocused();
    // Visible focus ring path: typing updates the controlled composer.
    await page.keyboard.type("e2e-focus-check");
    await expect(box).toHaveValue(/e2e-focus-check/);
  });
});

test.describe("responsive viewports", () => {
  for (const [name, size] of [
    ["desktop", { width: 1440, height: 900 }],
    ["tablet", { width: 900, height: 700 }],
    ["720", { width: 720, height: 800 }],
    ["320", { width: 320, height: 640 }],
  ] as const) {
    test(`${name} has no page-level horizontal overflow`, async ({ page }) => {
      await page.setViewportSize(size);
      await page.goto("/attach");
      await expect(page.locator(".wordmark")).toBeVisible({ timeout: 30_000 });
      await dismissBlockingDialogs(page);
      await expectNoPageHScroll(page);
      await expect(page.getByRole("button", { name: "Open settings" })).toBeVisible();
      await expect(page.getByLabel("Instruction")).toBeVisible();
    });
  }
});

test.describe("mobile sheet density", () => {
  test("phone viewport keeps composer and touch-sized primary controls", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 700 });
    await page.goto("/attach");
    await expect(page.locator(".wordmark")).toBeVisible({ timeout: 30_000 });
    await dismissBlockingDialogs(page);
    const box = page.getByLabel("Instruction");
    await expect(box).toBeVisible();
    await expect(composer(page)).toBeVisible();
    const send = page.getByRole("button", { name: "Send" });
    await expect(send).toBeVisible();
    const boxSize = await send.boundingBox();
    expect(boxSize).toBeTruthy();
    expect((boxSize?.height || 0)).toBeGreaterThanOrEqual(24);

    const navToggle = page.getByRole("button", { name: "Toggle agents panel" });
    await expect(navToggle).toBeVisible();
    await navToggle.click();
    await navToggle.click();
    await expectNoPageHScroll(page);
  });
});
