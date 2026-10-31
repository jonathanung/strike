// Composer glance chips on phone (#1711).
// The .composer-glance chips (Approval / Autonomy / Model) are flex
// items in the horizontally scrolling .composer-chips row. They had
// no flex:none or nowrap, so on phone the Autonomy chip shrank,
// wrapped "Autonomy · supervised" onto two lines, and the fixed
// 44px phone height clipped both lines (QA measured clientHeight 42
// < scrollHeight 49 at 390x844 and 360x800; desktop was one line and
// fine). The chips now keep one line and the row scrolls instead.
// This scenario measures the real chip geometry at both phone
// widths and again on desktop: no glance chip's content may exceed
// its client box on either axis.

import { delay } from "../browser.mjs";

const MEASURE = `(() => {
  const chips = [...document.querySelectorAll(".composer-chips .composer-glance")].map((el) => ({
    text: (el.textContent || "").trim(),
    clientWidth: el.clientWidth,
    scrollWidth: el.scrollWidth,
    clientHeight: el.clientHeight,
    scrollHeight: el.scrollHeight,
  }));
  const autonomy = chips.find((c) => c.text.startsWith("Autonomy"));
  return {
    count: chips.length,
    autonomy: autonomy ? autonomy.text : "",
    clipped: chips
      .filter((c) => c.scrollWidth > c.clientWidth || c.scrollHeight > c.clientHeight)
      .map((c) => c.text),
  };
})()`;

export async function scenario(ctx) {
  const { page, waitUntil, check, shoot, viewport } = ctx;

  // The cockpit pass may leave the first-run onboarding card up.
  await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);

  const ready = await waitUntil(
    `Boolean(document.querySelector('[data-autonomy-status]'))`,
    8000,
    "composer glance chips",
  );
  check(Boolean(ready), "composer glance chips did not render");
  if (!ready) return;

  const measureAt = async (width, height) => {
    await viewport(width, height);
    await delay(250);
    return page.evaluate(MEASURE);
  };

  await viewport(390, 844);
  const phone = await waitUntil(
    `document.body && document.body.getAttribute("data-phone") === "1"`,
    4000,
    "phone layout at 390",
  );
  check(Boolean(phone), "phone layout did not engage at 390x844");

  const at390 = await measureAt(390, 844);
  check(at390.count >= 3, `expected at least 3 glance chips at 390, got ${at390.count}`);
  check(
    at390.autonomy.startsWith("Autonomy · "),
    `autonomy chip missing at 390: ${JSON.stringify(at390)}`,
  );
  check(
    at390.clipped.length === 0,
    `glance chips clipped at 390x844: ${at390.clipped.join(", ")}`,
  );
  await shoot("glance-chips", ["mobile"]);

  const at360 = await measureAt(360, 800);
  check(at360.count >= 3, `expected at least 3 glance chips at 360, got ${at360.count}`);
  check(
    at360.clipped.length === 0,
    `glance chips clipped at 360x800: ${at360.clipped.join(", ")}`,
  );

  const atDesk = await measureAt(1440, 900);
  check(atDesk.count >= 3, `expected at least 3 glance chips on desktop, got ${atDesk.count}`);
  check(
    atDesk.clipped.length === 0,
    `glance chips clipped at 1440x900: ${atDesk.clipped.join(", ")}`,
  );
  await shoot("glance-chips", ["desk1440"]);
}
