import { expect, test } from "@playwright/test";

/**
 * Attach-only smokes hit STRIKE_E2E_ATTACH_BASE when provided by web-e2e.sh.
 * Skipped when the harness did not start an attach-only server.
 */
const attachBase = process.env.STRIKE_E2E_ATTACH_BASE;

test.describe("attach-only read path", () => {
  test.skip(!attachBase, "STRIKE_E2E_ATTACH_BASE not set");

  test("composer and mutations unavailable; read shell works", async ({ page }) => {
    await page.goto(attachBase!);
    await expect(page.locator(".wordmark")).toBeVisible({ timeout: 30_000 });
    await expect(page.getByLabel("Conversation transcript")).toBeVisible({ timeout: 30_000 });

    // Empty-state copy differs in attach-only.
    await expect(page.getByText("Inspect the record.")).toBeVisible({ timeout: 15_000 });

    const instruction = page.getByLabel("Instruction");
    await expect(instruction).toBeDisabled();
    await expect(page.getByRole("button", { name: "Send" })).toBeDisabled();

    // New workspace mutation must not be offered.
    await expect(page.getByRole("button", { name: "+ New workspace" })).toHaveCount(0);

    // Settings still opens for read/appearance.
    await page.getByRole("button", { name: "Open settings" }).click();
    await expect(page.getByRole("dialog", { name: "Workspace settings" })).toBeVisible();
    await page.getByRole("dialog", { name: "Workspace settings" }).getByRole("button", { name: "Close" }).first().click();
  });
});
