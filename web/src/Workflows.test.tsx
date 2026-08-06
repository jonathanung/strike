import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowsPanel } from "./Workflows";

const response = (body: unknown, status = 200) =>
  Promise.resolve(new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));

const catalog = {
  workflows: [
    {
      name: "plan-implement",
      source: "builtin",
      valid: true,
      description: "Plan then implement",
      phases: [
        {
          name: "plan",
          gate: "user",
          permissions: [{ permission: "write", pattern: "*", action: "deny" }],
        },
        { name: "implement", gate: "agent" },
      ],
    },
    {
      name: "broken",
      source: "project",
      valid: false,
      validationError: "no phases",
      phases: [],
    },
  ],
};

const doc = {
  schemaVersion: 1,
  name: "plan-implement",
  description: "Plan then implement",
  phases: [
    {
      name: "plan",
      gate: "user",
      context: "plan carefully",
      permissions: [{ permission: "write", pattern: "*", action: "deny" }],
    },
    { name: "implement", gate: "agent" },
  ],
};

describe("WorkflowsPanel", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method || "GET").toUpperCase();
      if (url.endsWith("/v1/workflows") && method === "GET") return response(catalog);
      if (url.includes("/v1/workflows/plan-implement/document")) return response(doc);
      if (url.includes("/v1/workflows/scaffold")) return response({ schemaVersion: 1, name: "my-workflow", phases: [{ name: "phase-1", gate: "agent" }] });
      if (url.includes("/v1/workflows/validate")) {
        const body = JSON.parse(String(init?.body || "{}"));
        const ok = Boolean(body.document?.name && body.document?.phases?.length);
        return response(ok ? { ok: true } : { ok: false, error: "invalid" });
      }
      if (url.includes("/v1/workflows/format")) return response({ json: JSON.stringify(doc, null, 2) });
      if (url.includes("/v1/workflows/phase-grants")) return response({ grants: [{ permission: "write", pattern: "*", action: "deny" }] });
      if (url.includes("/v1/workflow-drafts/review")) {
        return response({
          name: "plan-implement",
          valid: true,
          hasChecks: true,
          hasWidening: true,
          canonicalJson: JSON.stringify(doc),
          phases: [{
            name: "plan",
            checkHighlighted: true,
            gateCommand: "make test",
            widening: [{ permission: "bash", pattern: "*", action: "allow" }],
          }],
        });
      }
      if (url.includes("/v1/workflows/save")) {
        return response({ path: "/tmp/plan-implement.json", activated: false });
      }
      if (url.includes("/start")) {
        const body = JSON.parse(String(init?.body || "{}"));
        if (!body.confirm) return response({ error: "start requires confirm=true after grant review" }, 400);
        return response({ ok: true });
      }
      if (url.includes("/v1/workflows/stop")) return response({ ok: true });
      return response({ error: `unhandled ${method} ${url}` }, 500);
    }));
    Element.prototype.scrollIntoView = vi.fn();
    HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) { this.setAttribute("open", ""); });
    HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) { this.removeAttribute("open"); });
  });
  afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

  it("lists catalog entries and blocks start on invalid workflows", async () => {
    render(<WorkflowsPanel available draftsAvailable live rootID="live" agents={["build"]} busy={false} />);
    expect(await screen.findByText("plan-implement")).toBeInTheDocument();
    expect(screen.getByText("broken")).toBeInTheDocument();
    expect(screen.getByText(/no phases/)).toBeInTheDocument();
    const startButtons = screen.getAllByRole("button", { name: "Start" });
    // valid then invalid
    expect(startButtons[0]).not.toBeDisabled();
    expect(startButtons[1]).toBeDisabled();
  });

  it("requires grant review confirm before start", async () => {
    render(<WorkflowsPanel available draftsAvailable live rootID="live" agents={["build"]} busy={false} activeWorkflow="" />);
    await screen.findByText("plan-implement");
    fireEvent.click(screen.getAllByRole("button", { name: "Start" })[0]);
    expect(await screen.findByRole("dialog", { name: /Start plan-implement/ })).toBeInTheDocument();
    expect(screen.getByLabelText("Phase 0 grant review")).toHaveTextContent("deny write *");
    fireEvent.click(screen.getByRole("button", { name: "Confirm start" }));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/v1/workflows/plan-implement/start"),
      expect.objectContaining({ method: "POST", body: JSON.stringify({ confirm: true }) }),
    ));
  });

  it("opens builder, validates, reviews widening, and saves without activating", async () => {
    render(<WorkflowsPanel available draftsAvailable live rootID="live" agents={["build"]} busy={false} />);
    await screen.findByText("plan-implement");
    fireEvent.click(screen.getByRole("button", { name: "New" }));
    expect(await screen.findByRole("dialog", { name: "New workflow" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Preview / review" }));
    expect(await screen.findByText(/Permission widening/)).toBeInTheDocument();
    expect(screen.getByText(/Executable check gates/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/v1/workflows/save"),
      expect.objectContaining({ method: "POST" }),
    ));
    const saveCall = vi.mocked(fetch).mock.calls.find((c) => String(c[0]).includes("/v1/workflows/save"));
    expect(saveCall).toBeTruthy();
    const body = JSON.parse(String((saveCall?.[1] as RequestInit).body));
    expect(body.scope).toBe("project");
    expect(body.document.name).toBe("my-workflow");
    // ensure client never sends an activate flag on save
    expect(body.activate).toBeUndefined();
    expect(body.activated).toBeUndefined();
  });

  it("shows unavailable state when capability is absent", () => {
    render(<WorkflowsPanel available={false} draftsAvailable={false} live={false} rootID="" agents={[]} busy={false} />);
    expect(screen.getByRole("status")).toHaveTextContent("Workflows unavailable");
    expect(fetch).not.toHaveBeenCalled();
  });
});
