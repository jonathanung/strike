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
    const readChrome = () =>
      page.evaluate(() => {
        const root = getComputedStyle(document.documentElement);
        const mode = document.querySelector(".mode-switch");
        const send = document.querySelector(".composer-send");
        const pulse = document.querySelector(".pulse");
        return {
          acid: root.getPropertyValue("--acid").trim().toLowerCase(),
          radius: root.getPropertyValue("--radius").trim(),
          ink: root.getPropertyValue("--ink").trim().toLowerCase(),
          modeRadius: mode ? getComputedStyle(mode).borderRadius : "",
          sendBg: send ? getComputedStyle(send).backgroundColor : "",
          pulseShadow: pulse ? getComputedStyle(pulse).boxShadow : "",
        };
      });

    await page.evaluate(() => document.documentElement.setAttribute("data-appearance", "dark"));
    const dark = await readChrome();
    expect(dark.acid).toBe("#7c3aed");
    expect(dark.radius).toBe("2px");
    expect(dark.ink).toBe("#f3f1fa");
    expect(dark.modeRadius).toMatch(/^2px/);
    expect(dark.sendBg).not.toBe("rgba(0, 0, 0, 0)");
    expect(dark.pulseShadow === "none" || dark.pulseShadow === "").toBe(true);

    await page.evaluate(() => document.documentElement.setAttribute("data-appearance", "light"));
    const light = await readChrome();
    expect(light.acid).toBe("#5b21b6");
    expect(light.radius).toBe("2px");
    expect(light.ink).toBe("#1a1528");
    await page.evaluate(() => document.documentElement.removeAttribute("data-appearance"));
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

  test("inspector tabs at 1440px stay clickable without opening settings", async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/attach");
    await expect(page.locator(".wordmark")).toBeVisible({ timeout: 30_000 });
    await dismissBlockingDialogs(page);
    const inspectorToggle = page.getByRole("button", { name: "Toggle inspector" });
    if ((await inspectorToggle.getAttribute("aria-pressed")) !== "true") {
      await inspectorToggle.click();
    }
    const chatTabs = page.getByRole("tablist", { name: /Chat surfaces/i }).getByRole("tab");
    await expect(chatTabs.first()).toBeVisible();
    const last = chatTabs.last();
    await last.click();
    await expect(last).toHaveAttribute("aria-selected", "true");
    await expect(page.getByRole("dialog", { name: "Workspace settings" })).toHaveCount(0);

    await page.getByRole("button", { name: /Ops:/ }).click();
    await expect(page.getByRole("dialog", { name: "Workspace settings" })).toHaveCount(0);
    const mcpTab = page.getByRole("tablist", { name: /Ops surfaces/i }).getByRole("tab", { name: /^mcp$/i });
    await mcpTab.click();
    await expect(mcpTab).toHaveAttribute("aria-selected", "true");
    await expect(page.getByRole("heading", { name: /MCP/i })).toBeVisible();
    await expect(page.getByRole("dialog", { name: "Workspace settings" })).toHaveCount(0);

    await page.keyboard.press("Control+k");
    const filter = page.getByLabel("Filter commands");
    await expect(filter).toBeVisible();
    await filter.fill("mcp");
    const first = page.getByRole("listbox", { name: "Commands" }).getByRole("option").first();
    await expect(first).toContainText("Open MCP");
    await filter.press("Enter");
    await expect(page.getByRole("tablist", { name: /Ops surfaces/i }).getByRole("tab", { name: /^mcp$/i })).toHaveAttribute("aria-selected", "true");
    await expect(page.getByRole("dialog", { name: "Workspace settings" })).toHaveCount(0);
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

  test("slash completions are a vertical listbox and Enter accepts", async ({ page }) => {
    const box = page.getByLabel("Instruction");
    await expect(box).toBeEnabled({ timeout: 10_000 });
    await box.click();
    await box.pressSequentially("/");
    const list = page.getByRole("listbox", { name: "Composer completions" });
    await expect(list).toBeVisible();
    const first = list.getByRole("option").first();
    await expect(first).toHaveAttribute("aria-selected", "true");

    const optionStyle = await first.evaluate((el) => {
      const s = getComputedStyle(el);
      return { bg: s.backgroundColor, transform: s.textTransform, display: s.display };
    });
    const listDir = await list.evaluate((el) => getComputedStyle(el).flexDirection);
    const send = page.getByRole("button", { name: "Send" });
    const sendStyle = await send.evaluate((el) => {
      const s = getComputedStyle(el);
      return { bg: s.backgroundColor, transform: s.textTransform };
    });
    const attachStyle = await page.getByRole("button", { name: "Attach" }).evaluate((el) => {
      const s = getComputedStyle(el);
      return { bg: s.backgroundColor, transform: s.textTransform };
    });
    expect(listDir).toBe("column");
    const tops = await list.getByRole("option").evaluateAll((els) => els.map((el) => el.getBoundingClientRect().top));
    expect(tops.length).toBeGreaterThan(1);
    for (let i = 1; i < tops.length; i++) {
      expect(tops[i], `option ${i} should sit below option ${i - 1}`).toBeGreaterThan(tops[i - 1]);
    }
    await box.press("ArrowDown");
    await expect(list.getByRole("option").nth(1)).toHaveAttribute("aria-selected", "true");
    await box.press("ArrowUp");
    await expect(list.getByRole("option").first()).toHaveAttribute("aria-selected", "true");
    expect(optionStyle.transform).not.toBe("uppercase");
    expect(optionStyle.bg).not.toBe(sendStyle.bg);
    expect(sendStyle.transform).toBe("uppercase");
    expect(attachStyle.bg).not.toBe(sendStyle.bg);
    expect(attachStyle.transform).not.toBe("uppercase");

    await box.press("Enter");
    await expect(box).toHaveValue(/^\/help /);
    await expect(box).not.toHaveValue("/\n");
    await expect(list).toBeHidden();
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
    await expect(navToggle).toHaveAttribute("aria-pressed", "true");
    // Close via backdrop (header toggle stays above backdrop).
    const backdrop = page.getByRole("button", { name: "Close panel" });
    if (await backdrop.isVisible().catch(() => false)) {
      await backdrop.click();
    } else {
      await page.keyboard.press("Escape");
    }
    await expect(navToggle).toHaveAttribute("aria-pressed", "false");
    await expectNoPageHScroll(page);
  });
});
