// Customize (#1281). Runs on `make webui-uat` (full suite), not on CI's
// `--suite smoke`. Seeds an MCP server through the API so the recorded
// frames never contain the env value, then checks the form shows the mask.

const SECRET = "sk-abcdefghijklmnopqrstuvwxyz0123456789";

async function setField(page, selector, value) {
  const ok = await page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return false;
    const proto = el.tagName === "TEXTAREA"
      ? HTMLTextAreaElement.prototype
      : el.tagName === "SELECT"
        ? HTMLSelectElement.prototype
        : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, "value").set.call(el, ${JSON.stringify(value)});
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
    return true;
  })()`);
  return ok === true;
}

export async function scenario(ctx) {
  const { page, check, waitUntil, shoot, phone } = ctx;
  const opened = await page.evaluate(`(() => {
    const btn = document.querySelector('[data-view="customize"]');
    if (!btn) return false;
    btn.click();
    return true;
  })()`);
  check(opened === true, "Customize switch is missing");
  const skills = await waitUntil(
    `document.body && document.body.innerText.includes("Slash skills") && document.body.innerText.includes("Skills")`,
    15000,
    "customize skills",
  );
  check(Boolean(skills), "Customize did not list skills");
  await shoot("customize-skills", ["desk768", "desk1024", "desk1440"]);
  await phone("customize-skills");

  await page.evaluate(`document.querySelector("[data-cz-new]")?.click()`);
  const form = await waitUntil(
    `!!document.querySelector('[data-cz-field="name"]') && !!document.querySelector('[data-cz-field="body"]')`,
    8000,
    "new skill form",
  );
  check(Boolean(form), "new skill form did not open");
  check(await setField(page, '[data-cz-field="name"]', "mcp"), "skill name field missing");
  check(await setField(page, '[data-cz-field="body"]', "Nope."), "skill body field missing");
  await page.evaluate(`document.querySelector("[data-cz-preview]")?.click()`);
  const reserved = await waitUntil(
    `document.body && document.body.innerText.toLowerCase().includes("reserved")`,
    10000,
    "reserved skill name",
  );
  check(Boolean(reserved), "reserved skill name was accepted");
  await shoot("customize-error-reserved", ["desk768", "desk1024", "desk1440"]);

  await page.evaluate(`document.querySelector("[data-cz-new]")?.click()`);
  await waitUntil(`!!document.querySelector('[data-cz-field="name"]')`, 8000, "second new skill");
  check(await setField(page, '[data-cz-field="scope"]', "global"), "scope field missing");
  check(await setField(page, '[data-cz-field="name"]', "uat-note"), "uat-note name missing");
  check(await setField(page, '[data-cz-field="description"]', "updated note"), "description missing");
  check(await setField(page, '[data-cz-field="body"]', "Say hello from uat."), "body missing");
  await page.evaluate(`document.querySelector("[data-cz-preview]")?.click()`);
  const diff = await waitUntil(
    `(() => {
      const text = document.querySelector(".cz-diff")?.textContent || "";
      const save = document.querySelector("[data-cz-save]");
      return text.includes("updated note") && save && !save.disabled;
    })()`,
    10000,
    "skill preview diff",
  );
  check(Boolean(diff), "skill preview diff missing");
  await shoot("customize-skill-diff", ["desk1440"]);
  await page.evaluate(`document.querySelector("[data-cz-save]")?.click()`);
  const saved = await waitUntil(
    `!!document.querySelector('[data-cz-item="uat-note"]')`,
    10000,
    "skill save",
  );
  if (!saved) {
    const extra = await page.evaluate(`document.querySelector(".cz-error")?.textContent || ""`);
    check(false, `skill save did not land: ${extra}`);
  }

  await page.evaluate(`document.querySelector('[data-cz-item="uat-note"]')?.click()`);
  const removeReady = await waitUntil(
    `!!document.querySelector("[data-cz-remove]")`,
    8000,
    "remove button",
  );
  check(Boolean(removeReady), "Remove was not offered for the saved skill");
  await page.evaluate(`document.querySelector("[data-cz-remove]")?.click()`);
  const armed = await waitUntil(
    `!!document.querySelector("[data-cz-confirm-remove]") && !!document.querySelector('[data-cz-item="uat-note"]')`,
    4000,
    "confirm remove",
  );
  check(Boolean(armed), "Remove deleted the skill before confirm");
  await shoot("customize-confirm-remove", ["desk1440"]);
  await page.evaluate(`document.querySelector("[data-cz-cancel-remove]")?.click()`);
  const stillThere = await waitUntil(
    `!!document.querySelector('[data-cz-item="uat-note"]') && !document.querySelector("[data-cz-confirm-remove]")`,
    4000,
    "cancel remove",
  );
  check(Boolean(stillThere), "Cancel did not keep the skill");

  await page.evaluate(`(() => {
    const tabs = document.querySelector('[aria-label="Customize sections"]');
    if (!tabs) return false;
    tabs.focus();
    tabs.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    return true;
  })()`);
  const agents = await waitUntil(
    `document.querySelector('[data-cz-section="agents"]')?.getAttribute("aria-selected") === "true"`,
    8000,
    "keyboard to agents",
  );
  check(Boolean(agents), "ArrowRight did not select Agents");
  await waitUntil(
    `document.body && document.body.innerText.includes("handoff")`,
    8000,
    "agents list",
  );
  await shoot("customize-agents", ["desk1440"]);

  const seeded = await page.evaluate(`(async () => {
    const body = {
      confirm: false,
      kind: "mcp",
      op: "upsert",
      scope: "global",
      name: "docs",
      baseHash: "missing",
      mcp: {
        type: "stdio",
        command: "echo",
        args: ["ok"],
        env: [{ key: "TOKEN", value: ${JSON.stringify(SECRET)} }],
        headers: [],
      },
    };
    const headers = { "content-type": "application/json" };
    const preview = await fetch("/v1/customize/preview", { method: "POST", headers, body: JSON.stringify(body) });
    const previewText = await preview.text();
    if (!preview.ok || previewText.includes(${JSON.stringify(SECRET)})) {
      return "preview " + preview.status + " " + previewText.slice(0, 240);
    }
    const parsed = JSON.parse(previewText);
    body.confirm = true;
    body.diffHash = parsed.diffHash;
    body.baseHash = parsed.baseHash || body.baseHash;
    const apply = await fetch("/v1/customize/apply", { method: "POST", headers, body: JSON.stringify(body) });
    const applyText = await apply.text();
    if (!apply.ok || applyText.includes(${JSON.stringify(SECRET)})) {
      return "apply " + apply.status + " " + applyText.slice(0, 240);
    }
    return "ok";
  })()`);
  check(seeded === "ok", `mcp seed failed: ${seeded}`);

  // The catalog loads once per pane. A direct API write does not bump it,
  // so reopen Customize before looking for the new server.
  await page.send("Page.navigate", { url: ctx.baseURL });
  const back = await waitUntil(
    `document.body && document.body.innerText.includes("Menu") && !!document.querySelector('[data-view="customize"]')`,
    20000,
    "reload after mcp seed",
  );
  check(Boolean(back), "cockpit did not return after the MCP seed");
  await page.evaluate(`document.querySelector('[data-view="customize"]')?.click()`);
  await waitUntil(
    `document.body && document.body.innerText.includes("Slash skills")`,
    15000,
    "customize after reload",
  );
  await page.evaluate(`document.querySelector('[data-cz-section="mcp"]')?.click()`);
  const listed = await waitUntil(
    `!!document.querySelector('[data-cz-item="docs"]')`,
    10000,
    "mcp docs row",
  );
  check(Boolean(listed), "seeded MCP server was not listed");
  await page.evaluate(`document.querySelector('[data-cz-item="docs"]')?.click()`);
  const masked = await waitUntil(
    `(() => {
      const values = [...document.querySelectorAll("input,textarea")].map((el) => el.value || "").join("\\n");
      return values.includes("TOKEN=") && values.includes("••••") && !values.includes(${JSON.stringify(SECRET)});
    })()`,
    10000,
    "masked mcp env",
  );
  check(Boolean(masked), "MCP env was not masked");
  const leaked = await page.evaluate(
    `document.body && document.body.innerText.includes(${JSON.stringify(SECRET)})`,
  );
  check(leaked !== true, "Customize showed an MCP secret");
  await shoot("customize-mcp-masked", ["desk768", "desk1024", "desk1440"]);
  await phone("customize-mcp-masked");

  await page.evaluate(`document.querySelector('[data-cz-section="rules"]')?.click()`);
  await waitUntil(
    `document.body && document.body.innerText.includes("AGENTS.md")`,
    8000,
    "rules",
  );
  await shoot("customize-rules", ["desk1440"]);

  await page.evaluate(`document.querySelector('[data-cz-section="roster"]')?.click()`);
  const roster = await waitUntil(
    `document.body && document.body.innerText.includes("stall")`,
    8000,
    "roster",
  );
  check(Boolean(roster), "roster section did not open");
  await page.evaluate(`document.querySelector("[data-cz-item]")?.click()`);
  await waitUntil(`!!document.querySelector('[data-cz-field="stall"]')`, 8000, "roster form");
  await shoot("customize-roster", ["desk768", "desk1024", "desk1440"]);
}
