// Removal actually removes (#1544, #1562).
// A custom provider's "Remove provider" deletes the definition as well as
// the stored credential, so the provider is still gone after a full page
// reload. Removing a saved gateway profile deletes its token from the
// tab's sessionStorage token map, so re-adding the gateway cannot
// silently resurrect the old token. All names, keys, and tokens here are
// throwaway dummies created against the local echo daemon. The gateway
// half ends by removing the re-added profile while it is selected, which
// also switches the tab back to this gateway.

const PROVIDER = "uat-remove-me";
const GATEWAY = "UAT Remove GW";
const GATEWAY_URL = "http://127.0.0.1:8787";

export const scenario = async (ctx) => {
  await ctx.page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
  await ctx.waitUntil(
    `document.querySelector('[data-action="onboarding-continue"]') == null`,
    4000,
    "onboarding",
  );

  // --- Providers (#1544) ---
  await openProviders(ctx);
  await setField(ctx, "[data-new-name]", PROVIDER);
  await setField(ctx, "[data-new-url]", "http://127.0.0.1:9/v1");
  await setField(ctx, "[data-new-model]", "dummy-model");
  await setField(ctx, "[data-new-key]", "dummy-fake-key-1544");
  await ctx.page.evaluate(`document.querySelector('[data-add-provider]')?.click()`);
  const added = await ctx.waitUntil(
    `Boolean(document.querySelector('[data-provider="${PROVIDER}"] [data-remove-key]'))`,
    8000,
    "custom provider added",
  );
  ctx.check(Boolean(added), "custom provider did not appear with a Remove provider button");
  if (!added) return;
  await ctx.shoot("removal-provider-before", ["desk1440", "mobile"]);

  await ctx.page.evaluate(
    `document.querySelector('[data-provider="${PROVIDER}"] [data-remove-key]')?.click()`,
  );
  const confirm = await ctx.waitUntil(
    `(() => {
      const title = document.querySelector("#confirm-title");
      return Boolean(title && title.textContent.includes("Remove provider ${PROVIDER}"));
    })()`,
    4000,
    "remove provider confirm",
  );
  ctx.check(Boolean(confirm), "Remove provider did not ask for confirmation");
  if (!confirm) return;
  await ctx.shoot("removal-provider-confirm", ["desk1440", "mobile"]);
  await ctx.page.evaluate(`document.querySelector("[data-confirm='accept']")?.click()`);

  const gone = await ctx.waitUntil(
    `!document.querySelector('[data-provider="${PROVIDER}"]')`,
    8000,
    "provider gone after removal",
  );
  ctx.check(Boolean(gone), "provider card still present after removal");

  // The reload truth check: a full page reload must not bring it back,
  // and the daemon's own list must agree.
  await ctx.page.send("Page.reload", {});
  await ctx.waitUntil(`Boolean(document.querySelector('[data-nav]'))`, 8000, "shell after reload");
  await openProviders(ctx);
  const stillGone = await ctx.waitUntil(
    `(() => {
      const cards = [...document.querySelectorAll('[data-provider]')].map((el) => el.getAttribute('data-provider'));
      return !cards.includes("${PROVIDER}");
    })()`,
    8000,
    "provider absent after reload",
  );
  ctx.check(Boolean(stillGone), "removed provider reappeared after a full page reload");
  const daemonList = await ctx.page.evaluate(`fetch("/v1/custom-providers", { credentials: "include" })
    .then((r) => r.text()).catch(() => "")`);
  ctx.check(
    !String(daemonList).includes(PROVIDER),
    "daemon still lists the removed custom provider",
  );
  await ctx.shoot("removal-provider-after-reload", ["desk1440", "mobile"]);

  // --- Gateways (#1562) ---
  const id1 = await addGatewayProfile(ctx);
  ctx.check(Boolean(id1), "gateway profile was not saved");
  if (!id1) return;
  await switchToGateway(ctx, id1);
  await openGateways(ctx);
  await setField(ctx, `input[aria-label="Gateway token"]`, "dummy-gateway-token-1562");
  const stored = await ctx.waitUntil(
    `(() => {
      try {
        const map = JSON.parse(sessionStorage.getItem("strike.gateway.tokens") || "{}");
        return map[${JSON.stringify(id1)}] === "dummy-gateway-token-1562";
      } catch { return false; }
    })()`,
    4000,
    "token stored for profile",
  );
  ctx.check(Boolean(stored), "dummy token was not stored in the tab token map");
  await ctx.shoot("removal-gateway-before", ["desk1440", "mobile"]);

  await removeGatewayViaPalette(ctx, id1, GATEWAY);
  const cleaned = await ctx.waitUntil(
    `(() => {
      try {
        const profiles = JSON.parse(localStorage.getItem("strike.gateways") || "[]");
        const map = JSON.parse(sessionStorage.getItem("strike.gateway.tokens") || "{}");
        return !profiles.some((p) => p.id === ${JSON.stringify(id1)})
          && !Object.prototype.hasOwnProperty.call(map, ${JSON.stringify(id1)});
      } catch { return false; }
    })()`,
    4000,
    "profile and token removed together",
  );
  ctx.check(
    Boolean(cleaned),
    "removing the gateway profile left its token in the tab token map",
  );
  await ctx.shoot("removal-gateway-after", ["desk1440", "mobile"]);

  // Re-adding the same gateway must not resurrect the removed token.
  const id2 = await addGatewayProfile(ctx);
  ctx.check(Boolean(id2), "gateway profile was not re-added");
  if (id2) {
    await switchToGateway(ctx, id2);
    await openGateways(ctx);
    const resurrected = await ctx.page.evaluate(`(() => {
      const input = document.querySelector('input[aria-label="Gateway token"]');
      try {
        const map = JSON.parse(sessionStorage.getItem("strike.gateway.tokens") || "{}");
        return Boolean(input && input.value) || Object.prototype.hasOwnProperty.call(map, ${JSON.stringify(id2)});
      } catch { return true; }
    })()`);
    ctx.check(!resurrected, "re-added gateway silently resurrected the removed token");
    await ctx.shoot("removal-gateway-readded", ["desk1440", "mobile"]);
    // Removing the selected profile also returns the tab to this gateway.
    await removeGatewayViaPalette(ctx, id2, GATEWAY);
  }
};

async function clickCommand(ctx, id) {
  await ctx.page.evaluate(`document.querySelector('[data-nav="palette"]')?.click()`);
  const found = await ctx.waitUntil(
    `Boolean(document.querySelector('[data-command-id="${id}"]'))`,
    4000,
    `command ${id}`,
  );
  ctx.check(Boolean(found), `palette did not expose command ${id}`);
  if (!found) return false;
  await ctx.page.evaluate(`document.querySelector('[data-command-id="${id}"]')?.click()`);
  return true;
}

async function openProviders(ctx) {
  if (!(await clickCommand(ctx, "providers"))) return;
  const page = await ctx.waitUntil(
    `Boolean(document.querySelector('[data-providers-root]'))`,
    8000,
    "providers page",
  );
  ctx.check(Boolean(page), "palette did not open Providers");
}

async function openGateways(ctx) {
  if (!(await clickCommand(ctx, "gateways.open"))) return;
  const panel = await ctx.waitUntil(
    `Boolean(document.querySelector('input[aria-label="Gateway name"]'))`,
    8000,
    "gateways panel",
  );
  ctx.check(Boolean(panel), "palette did not open the Gateways panel");
}

async function setField(ctx, selector, value) {
  await ctx.page.evaluate(`(() => {
    const input = document.querySelector(${JSON.stringify(selector)});
    if (!input) return;
    input.focus();
    const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
    proto.set.call(input, ${JSON.stringify(value)});
    input.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(value)} }));
  })()`);
  await ctx.waitUntil(
    `document.querySelector(${JSON.stringify(selector)})?.value === ${JSON.stringify(value)}`,
    2000,
    `field ${selector}`,
  );
}

async function addGatewayProfile(ctx) {
  await openGateways(ctx);
  await setField(ctx, `input[aria-label="Gateway name"]`, GATEWAY);
  await setField(ctx, `input[aria-label="Gateway base URL"]`, GATEWAY_URL);
  await ctx.page.evaluate(`(() => {
    const panel = document.querySelector('input[aria-label="Gateway name"]')?.closest('div');
    const btn = [...document.querySelectorAll("button")].find((b) => (b.textContent || "").trim() === "Save");
    if (btn) btn.click();
  })()`);
  const found = await ctx.waitUntil(
    `(() => {
      try {
        const profiles = JSON.parse(localStorage.getItem("strike.gateways") || "[]");
        return Boolean(profiles.find((p) => p.name === ${JSON.stringify(GATEWAY)}));
      } catch { return false; }
    })()`,
    4000,
    "gateway profile saved",
  );
  if (!found) return "";
  const id = await ctx.page.evaluate(`(() => {
    const profiles = JSON.parse(localStorage.getItem("strike.gateways") || "[]");
    const hit = profiles.find((p) => p.name === ${JSON.stringify(GATEWAY)});
    return hit ? hit.id : "";
  })()`);
  return String(id || "");
}

async function switchToGateway(ctx, id) {
  await clickCommand(ctx, `gw.switch.${id}`);
  const switched = await ctx.waitUntil(
    `localStorage.getItem("strike.base") === ${JSON.stringify(GATEWAY_URL)}`,
    4000,
    "gateway selected",
  );
  ctx.check(Boolean(switched), "palette switch did not select the gateway profile");
}

async function removeGatewayViaPalette(ctx, id, name) {
  await clickCommand(ctx, `gw.remove.${id}`);
  const confirm = await ctx.waitUntil(
    `(() => {
      const title = document.querySelector("#confirm-title");
      return Boolean(title && title.textContent.includes("Remove ${name}"));
    })()`,
    4000,
    "remove gateway confirm",
  );
  ctx.check(Boolean(confirm), "gateway Remove did not ask for confirmation");
  if (!confirm) return;
  await ctx.page.evaluate(`document.querySelector("[data-confirm='accept']")?.click()`);
}
