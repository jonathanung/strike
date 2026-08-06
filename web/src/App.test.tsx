import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";

class FakeEventSource { static instances: FakeEventSource[] = []; onmessage?: (event: MessageEvent) => void; onerror?: () => void; close = vi.fn(); constructor(public url: string) { FakeEventSource.instances.push(this); } }
class FakeWebSocket { static OPEN = 1; static instances: FakeWebSocket[] = []; readyState = 1; onopen?: () => void; onmessage?: (event: MessageEvent) => void; onerror?: () => void; onclose?: () => void; send = vi.fn(); close = vi.fn(); constructor(public url: string) { FakeWebSocket.instances.push(this); queueMicrotask(() => this.onopen?.()); } }

const response = (body: unknown, status = 200) => Promise.resolve(new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));

describe("App", () => {
  beforeEach(() => {
    FakeEventSource.instances = []; FakeWebSocket.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource); vi.stubGlobal("WebSocket", FakeWebSocket);
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input).includes("bootstrap") ? response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, files: false, memory: false, issues: false, roots: false }, protocolOps: ["user.input", "compact", "rewind"], status: { sessionId: "live", provider: "echo", busy: false }, agents: [{ name: "build" }], skills: [{ name: "ship", description: "Ship changes" }] }) : String(input).includes("sessions") ? response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" }) : response({ ok: true })));
    Element.prototype.scrollIntoView = vi.fn();
  });
  afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

  it("uses only the live transport for the live session and sends prompts", async () => {
    render(<App />);
    await screen.findByText("Current");
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    expect(FakeEventSource.instances).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Instruction"), { target: { value: "ship it" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining("/v1/ops"), expect.objectContaining({ method: "POST" })));
  });

  it("renders and resolves a blocking permission dialog", async () => {
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    FakeWebSocket.instances[0].onmessage?.({ data: JSON.stringify({ type: "permission.asked", data: { requestId: "p1", permission: "bash", patterns: ["echo hi"] } }) } as MessageEvent);
    const dialog = await screen.findByRole("dialog", { name: "Permission required" });
    expect(dialog).toHaveTextContent("bash");
    expect(dialog).toHaveTextContent("echo hi");
    expect(screen.queryByText(/"requestId"/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reject" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Allow for project" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Allow session" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Allow once" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Why is this asked/ })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await waitFor(() => expect(fetch).toHaveBeenLastCalledWith(expect.stringContaining("/v1/ops"), expect.objectContaining({ body: expect.stringContaining('"decision":"once"') })));
  });

  it("submits all four permission wire decisions", async () => {
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const ask = (id: string) => FakeWebSocket.instances[0].onmessage?.({ data: JSON.stringify({ type: "permission.asked", data: { requestId: id, permission: "write", patterns: ["README.md"] } }) } as MessageEvent);
    const resolve = async (label: string, decision: string, id: string) => {
      ask(id);
      fireEvent.click(await screen.findByRole("button", { name: label }));
      await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining("/v1/ops"), expect.objectContaining({
        body: expect.stringMatching(new RegExp(`"requestId":"${id}".*"decision":"${decision}"|"decision":"${decision}".*"requestId":"${id}"`)),
      })));
      FakeWebSocket.instances[0].onmessage?.({ data: JSON.stringify({ type: "permission.resolved", data: { requestId: id, decision } }) } as MessageEvent);
      await waitFor(() => expect(screen.queryByRole("dialog", { name: "Permission required" })).not.toBeInTheDocument());
    };
    await resolve("Reject", "reject", "d-reject");
    await resolve("Allow session", "always", "d-always");
    await resolve("Allow for project", "project", "d-project");
    await resolve("Allow once", "once", "d-once");
  });

  it("loads permission explain when the host capability is present", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, permissions: true, roots: false }, protocolOps: ["permission.reply"], status: { sessionId: "live", busy: false }, agents: [], skills: [] });
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("/v1/permissions/explain")) return response({ Permission: "bash", Pattern: "rm -rf /", Action: "ask", Layer: "defaults", Summary: "bash * → ask (defaults)" });
      return response({ ok: true });
    }));
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    FakeWebSocket.instances[0].onmessage?.({ data: JSON.stringify({ type: "permission.asked", data: { requestId: "p-ex", permission: "bash", patterns: ["rm -rf /"] } }) } as MessageEvent);
    fireEvent.click(await screen.findByRole("button", { name: "Why is this asked?" }));
    expect(await screen.findByLabelText("Permission explanation")).toHaveTextContent("bash * → ask (defaults)");
    expect(screen.getByLabelText("Permission explanation")).toHaveTextContent("Effective: ask (defaults)");
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining("/v1/permissions/explain?permission=bash&pattern=rm"), expect.anything()));
  });

  it("queues a prompt while busy and exposes slash and skill completion", async () => {
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    FakeWebSocket.instances[0].onmessage?.({ data: JSON.stringify({ type: "turn.started", data: { turnId: "t" } }) } as MessageEvent);
    fireEvent.change(screen.getByLabelText(/Instruction/), { target: { value: "next task" } });
    fireEvent.submit(screen.getByLabelText(/Instruction/).closest("form")!);
    expect(screen.getByRole("list", { name: "Queued prompts" })).toHaveTextContent("next task");
    fireEvent.change(screen.getByLabelText(/Instruction/), { target: { value: "/sh" } });
    expect(screen.getByRole("option", { name: /ship/ })).toBeInTheDocument();
  });

  it("shows the refactored inspector tabs and unavailable project workflows", async () => {
    render(<App />);
    await screen.findByText("Current");
    expect(screen.getAllByText("not reported")).toHaveLength(2);
    expect(screen.getByRole("tab", { name: "context" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "files" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "memory" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "issues" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "workflows" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "activity" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "project" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "capabilities" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "files" }));
    expect(screen.getByRole("status")).toHaveTextContent("Changed files unavailable");
    fireEvent.click(screen.getByRole("tab", { name: "memory" }));
    expect(screen.getByRole("status")).toHaveTextContent("Memory unavailable");
    fireEvent.click(screen.getByRole("tab", { name: "issues" }));
    expect(screen.getByRole("status")).toHaveTextContent("Issues unavailable");
    fireEvent.click(screen.getByRole("tab", { name: "workflows" }));
    expect(screen.getByRole("status")).toHaveTextContent("Workflows unavailable");
  });

  it("uses historical SSE in attach-only mode", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input).includes("bootstrap") ? response({ version: "test", authRequired: false, attachOnly: true, capabilities: { live: false }, protocolOps: null, agents: [], skills: [] }) : String(input).includes("sessions") ? response({ sessions: [{ id: "saved", title: "Saved" }] }) : String(input).includes("roots") ? Promise.resolve(new Response("multi-root unavailable", { status: 503 })) : response({ ok: true })));
    render(<App />);
    await screen.findByText("Saved");
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
    expect(FakeEventSource.instances[0].url).toContain("/v1/sessions/saved/events");
    expect(FakeWebSocket.instances).toHaveLength(0);
    expect(screen.getByRole("tab", { name: "memory" })).toBeInTheDocument();
  });

  it("shows a cockpit load error when bootstrap is forbidden", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response("forbidden", { status: 403, statusText: "Forbidden" }))));
    render(<App />);
    expect(await screen.findByRole("alert")).toHaveTextContent("403 Forbidden");
    expect(screen.getByRole("alert")).toHaveTextContent("Failed to load cockpit");
  });

  it("renders changed file summaries, expandable diffs, memory, issues, and panel controls", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, files: true, memory: true, issues: true, roots: false }, protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false }, agents: [{ name: "build" }], skills: [] });
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("changed-files")) return response({ files: [{ path: "web/src/App.tsx", added: 12, deleted: 3, diff: "+new line\n-old line" }] });
      if (url.includes("memory")) return response({ entries: [{ Key: "prefs", Value: "use tests", Tags: ["project-convention"] }] });
      if (url.includes("issues")) return response({ issues: [{ ID: 7, Title: "Fix panel", Status: "open", Body: "Resize it" }] });
      return response({ ok: true });
    }));
    render(<App />);
    await screen.findByText("Current");
    expect(screen.getByLabelText("Resize agents panel")).toBeInTheDocument();
    expect(screen.getByLabelText("Resize inspector panel")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Toggle agents panel" }));
    expect(screen.getByRole("button", { name: "Toggle agents panel" })).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(screen.getByRole("button", { name: "Toggle inspector" }));
    expect(screen.getByRole("button", { name: "Toggle inspector" })).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(screen.getByRole("tab", { name: "files" }));
    expect(await screen.findByText("web/src/App.tsx")).toBeInTheDocument();
    expect(screen.getByText("+12")).toBeInTheDocument();
    expect(screen.getByText("-3")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /web\/src\/App.tsx/ }));
    expect(screen.getByText(/new line/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "memory" }));
    expect(await screen.findByText("prefs")).toBeInTheDocument();
    expect(screen.getByText("use tests")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "issues" }));
    expect(await screen.findByText("#7 Fix panel")).toBeInTheDocument();
    expect(screen.getByText("Resize it")).toBeInTheDocument();
  });

  it("shows explicit settings and authentication unavailable states", async () => {
    render(<App />);
    await screen.findByText("Current");
    fireEvent.click(screen.getByRole("button", { name: "Open settings" }));
    expect(screen.getByRole("dialog", { name: "Workspace settings" })).toHaveTextContent("Provider authentication unavailable");
    expect(screen.getByRole("dialog", { name: "Workspace settings" })).toHaveTextContent("Saved defaults unavailable");
  });

  it("keeps question options blocking and associated with their request", async () => {
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    FakeWebSocket.instances[0].onmessage?.({ data: JSON.stringify({ type: "question.asked", data: { requestId: "q1", questions: [{ question: "Mode?", options: [{ label: "Safe", description: "Review changes" }] }] } }) } as MessageEvent);
    fireEvent.click(await screen.findByLabelText(/Safe/));
    fireEvent.click(screen.getByRole("button", { name: "Continue" }));
    await waitFor(() => expect(fetch).toHaveBeenLastCalledWith(expect.stringContaining("/v1/ops"), expect.objectContaining({ body: expect.stringContaining('"requestId":"q1"') })));
  });
});
