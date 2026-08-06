import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SettingsDialog, applyAppearance, loadAppearance } from "./Settings";
import type { Bootstrap, Status } from "./types";

const response = (body: unknown, status = 200) =>
  Promise.resolve(new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  }));

const boot = (settings = true, auth = false): Bootstrap => ({
  version: "test",
  authRequired: false,
  attachOnly: false,
  capabilities: { live: true, settings, auth },
  agents: [{ name: "build" }, { name: "plan" }],
  skills: [],
  protocolOps: [],
});

const status: Status = {
  sessionId: "live",
  provider: "echo",
  model: "dev",
  agent: "build",
  effort: "medium",
  permissionMode: "default",
};

describe("SettingsDialog", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-appearance");
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    document.documentElement.removeAttribute("data-appearance");
  });

  it("shows unavailable when host lacks settings", async () => {
    render(<SettingsDialog boot={boot(false)} status={status} providers={[]} onClose={() => {}} />);
    expect(screen.getByRole("dialog", { name: "Workspace settings" })).toHaveTextContent("Saved defaults unavailable");
    expect(screen.getByRole("dialog")).toHaveTextContent("Provider authentication unavailable");
  });

  it("loads dials and PATCHes sandbox + auto-approve", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/v1/settings") && (!init || !init.method || init.method === "GET")) {
        return response({
          provider: "echo",
          model: "dev",
          agent: "build",
          effort: "medium",
          mode: "default",
          sandbox: "workspace-write",
          notify: "unfocused-only",
          leanCode: "lite",
          permissionAutoApproveSeconds: 0,
          permissionAutoApproveExclude: ["bash"],
          maxChildDepth: 1,
          compactionStrategy: "trim",
        });
      }
      if (url.includes("/v1/settings") && init?.method === "PATCH") {
        return response({ sandbox: "read-only", permissionAutoApproveSeconds: 15, maxChildDepth: 2 });
      }
      return response({ ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);

    const onClose = vi.fn();
    render(<SettingsDialog boot={boot(true)} status={status} providers={["echo"]} onClose={onClose} />);

    expect(await screen.findByLabelText("Sandbox")).toHaveValue("workspace-write");
    expect(screen.getByLabelText("Countdown seconds")).toHaveValue("off");
    expect(screen.getByLabelText("Exclude bash")).toBeChecked();
    expect(screen.getByLabelText("Max child depth")).toHaveValue("1");

    fireEvent.change(screen.getByLabelText("Sandbox"), { target: { value: "read-only" } });
    fireEvent.change(screen.getByLabelText("Countdown seconds"), { target: { value: "15" } });
    fireEvent.change(screen.getByLabelText("Max child depth"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const patchCall = fetchMock.mock.calls.find((c) => String(c[0]).includes("/v1/settings") && (c[1] as RequestInit)?.method === "PATCH");
    expect(patchCall).toBeTruthy();
    const body = JSON.parse(String((patchCall![1] as RequestInit).body));
    expect(body.sandbox).toBe("read-only");
    expect(body.permissionAutoApproveSeconds).toBe("15");
    expect(body.maxChildDepth).toBe("2");
  });

  it("surfaces PATCH validation errors without closing", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/v1/settings") && (!init || !init.method || init.method === "GET")) {
        return response({ sandbox: "off" });
      }
      if (url.includes("/v1/settings") && init?.method === "PATCH") {
        return response({ error: 'unknown sandbox "nope" (want off|read-only|workspace-write)' }, 400);
      }
      return response({ ok: true });
    }));

    const onClose = vi.fn();
    render(<SettingsDialog boot={boot(true)} status={status} providers={[]} onClose={onClose} />);
    await screen.findByLabelText("Sandbox");
    fireEvent.change(screen.getByLabelText("Sandbox"), { target: { value: "read-only" } });
    // Force invalid by patching body path — select only has valid options, so
    // trigger save with a compaction strategy that server rejects via mock always 400.
    fireEvent.change(screen.getByLabelText("Strategy"), { target: { value: "summarize" } });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/unknown sandbox/);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("applies appearance to documentElement", () => {
    applyAppearance("light");
    expect(document.documentElement.getAttribute("data-appearance")).toBe("light");
    expect(loadAppearance()).toBe("light");
    applyAppearance("auto");
    expect(document.documentElement.hasAttribute("data-appearance")).toBe(false);
    expect(loadAppearance()).toBe("auto");
  });
});
