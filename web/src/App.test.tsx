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
  afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

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
    FakeWebSocket.instances[0].onmessage?.({ data: JSON.stringify({ type: "permission.asked", data: { requestId: "p1", tool: "bash" } }) } as MessageEvent);
    expect(await screen.findByRole("dialog", { name: "Permission required" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await waitFor(() => expect(fetch).toHaveBeenLastCalledWith(expect.stringContaining("/v1/ops"), expect.objectContaining({ body: expect.stringContaining("permission.reply") })));
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
    fireEvent.change(screen.getByLabelText(/Instruction/), { target: { value: "/ex" } });
    expect(screen.getByRole("option", { name: /export/ })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Instruction/), { target: { value: "/he" } });
    expect(screen.getByRole("option", { name: /help/ })).toBeInTheDocument();
  });

  it("rejects unknown slash commands with feedback and runs /help", async () => {
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    fireEvent.change(screen.getByLabelText(/Instruction/), { target: { value: "/pets" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByText(/not available in the web cockpit/i)).toBeInTheDocument();
    const opsCalls = vi.mocked(fetch).mock.calls.filter(([url, init]) => String(url).includes("/v1/ops") && (init as RequestInit | undefined)?.method === "POST");
    expect(opsCalls.some(([, init]) => String((init as RequestInit).body || "").includes("/pets"))).toBe(false);

    fireEvent.change(screen.getByLabelText(/Instruction/), { target: { value: "/help" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByText(/Web slash commands/i)).toBeInTheDocument();
    expect(screen.getByText(/\/export/)).toBeInTheDocument();
  });

  it("exports session markdown from the header control", async () => {
    let downloaded: Blob | undefined;
    const createObjectURL = vi.fn((blob: Blob) => {
      downloaded = blob;
      return "blob:export";
    });
    const revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", { createObjectURL, revokeObjectURL });

    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    FakeWebSocket.instances[0].onmessage?.({ data: JSON.stringify({ type: "user.message", time: "1", data: { text: "hi there", turnId: "t1" } }) } as MessageEvent);
    FakeWebSocket.instances[0].onmessage?.({ data: JSON.stringify({ type: "text.delta", time: "2", data: { text: "hello back", turnId: "t1" } }) } as MessageEvent);

    // Prevent navigation side-effects from the download anchor in jsdom.
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    fireEvent.click(await screen.findByRole("button", { name: "Export markdown" }));
    await waitFor(() => expect(createObjectURL).toHaveBeenCalled());
    expect(clickSpy).toHaveBeenCalled();
    expect(downloaded).toBeInstanceOf(Blob);
    const text = await new Promise<string>((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(String(reader.result || ""));
      reader.onerror = () => reject(reader.error);
      reader.readAsText(downloaded!);
    });
    expect(text).toContain("# Strike session export");
    expect(text).toContain("hi there");
    expect(text).toContain("hello back");
  });

  it("supports queue reorder, edit, remove, and clear", async () => {
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    FakeWebSocket.instances[0].onmessage?.({ data: JSON.stringify({ type: "turn.started", data: { turnId: "t" } }) } as MessageEvent);
    for (const value of ["first", "second", "third"]) {
      fireEvent.change(screen.getByLabelText(/Instruction/), { target: { value } });
      fireEvent.submit(screen.getByLabelText(/Instruction/).closest("form")!);
    }
    const list = screen.getByRole("list", { name: "Queued prompts" });
    expect(list).toHaveTextContent("first");
    expect(list).toHaveTextContent("second");
    expect(list).toHaveTextContent("third");

    fireEvent.click(screen.getByRole("button", { name: "Move queued prompt 1 down" }));
    expect(list.textContent?.indexOf("second")).toBeLessThan(list.textContent!.indexOf("first"));

    fireEvent.click(screen.getByRole("button", { name: "Edit queued prompt 1" }));
    const editor = screen.getByLabelText("Queued prompt text 1");
    fireEvent.change(editor, { target: { value: "second-edited" } });
    fireEvent.keyDown(editor, { key: "Enter" });
    expect(list).toHaveTextContent("second-edited");

    fireEvent.click(screen.getByRole("button", { name: "Edit queued prompt 1" }));
    const editor2 = screen.getByLabelText("Queued prompt text 1");
    fireEvent.change(editor2, { target: { value: "should-not-stick" } });
    fireEvent.keyDown(editor2, { key: "Escape" });
    fireEvent.blur(editor2);
    expect(list).toHaveTextContent("second-edited");
    expect(list).not.toHaveTextContent("should-not-stick");

    fireEvent.click(screen.getByRole("button", { name: "Remove queued prompt 3" }));
    expect(list).not.toHaveTextContent("third");

    fireEvent.click(screen.getByRole("button", { name: "Clear queue" }));
    expect(screen.queryByRole("list", { name: "Queued prompts" })).not.toBeInTheDocument();
  });

  it("shows the refactored inspector tabs and unavailable project workflows", async () => {
    render(<App />);
    await screen.findByText("Current");
    expect(screen.getAllByText("not reported")).toHaveLength(2);
    expect(screen.getByRole("tab", { name: "context" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "files" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "memory" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "issues" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "plans" })).toBeInTheDocument();
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
    fireEvent.click(screen.getByRole("tab", { name: "plans" }));
    expect(screen.getByRole("status")).toHaveTextContent("Plans unavailable");
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

  it("renders changed file summaries, expandable diffs, memory, issues, plans, and panel controls", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, files: true, memory: true, issues: true, plans: true, roots: false }, protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false }, agents: [{ name: "build" }], skills: [] });
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("changed-files")) return response({ files: [{ path: "web/src/App.tsx", added: 12, deleted: 3, diff: "+new line\n-old line" }] });
      if (url.includes("memory")) return response({ entries: [{ Key: "prefs", Value: "use tests", Tags: ["project-convention"] }] });
      if (url.includes("issues")) return response({ issues: [{ ID: 7, Title: "Fix panel", Status: "open", Body: "Resize it" }] });
      if (url.includes("/v1/plans")) return response({ plans: [{ ID: "p1", Title: "Web plans", Status: "draft", Version: 1, SectionCount: 0, OwnerRoot: "live" }] });
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
    fireEvent.click(screen.getByRole("tab", { name: "plans" }));
    expect(await screen.findByText("Web plans")).toBeInTheDocument();
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

  it("selects a deep-linked session id on boot and falls back for invalid ids", async () => {
    const original = window.location;
    // jsdom: replace search without breaking the rest of location.
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { ...original, search: "?session=hist-1", pathname: "/attach", href: "http://localhost/attach?session=hist-1" },
    });
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, roots: true, sessions: true }, protocolOps: ["user.input"], status: { sessionId: "root-a", provider: "echo", busy: false }, agents: [{ name: "build" }], skills: [] });
      if (url.includes("/v1/roots")) return response({ roots: [{ id: "root-a", agent: "build", busy: false }], activeId: "root-a" });
      if (url.includes("sessions")) return response({ sessions: [{ id: "hist-1", title: "Deep history", mtime: Math.floor(Date.now() / 1000) }, { id: "root-a", title: "Live root" }], liveId: "root-a" });
      return response({ ok: true });
    }));
    render(<App />);
    expect(await screen.findByText("Deep history")).toBeInTheDocument();
    await waitFor(() => expect(FakeEventSource.instances.length).toBeGreaterThan(0));
    expect(FakeEventSource.instances[0].url).toContain("/v1/sessions/hist-1/events");
    expect(screen.getByLabelText("Instruction")).toBeDisabled();
    Object.defineProperty(window, "location", { configurable: true, value: original });
  });

  it("selects a deep-linked live root and activates it", async () => {
    const original = window.location;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { ...original, search: "?root=root-b", pathname: "/attach", href: "http://localhost/attach?root=root-b" },
    });
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, roots: true, sessions: true }, protocolOps: ["user.input"], status: { sessionId: "root-a", provider: "echo", busy: false }, agents: [{ name: "build" }], skills: [] });
      if (url.includes("/v1/roots") && (!init || !init.method || init.method === "GET")) {
        return response({ roots: [{ id: "root-a", title: "A", agent: "build", busy: false }, { id: "root-b", title: "B", agent: "build", busy: true }], activeId: "root-a" });
      }
      if (url.includes("/activate")) return response({ ok: true });
      if (url.includes("sessions")) return response({ sessions: [{ id: "root-a", title: "A" }, { id: "root-b", title: "B" }], liveId: "root-a" });
      return response({ ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances.some((ws) => ws.url.includes("root=root-b"))).toBe(true));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u]) => String(u).includes("/v1/roots/root-b/activate"))).toBe(true));
    // Header reflects selected root transport; composer is live.
    expect(screen.getByLabelText("Instruction")).not.toBeDisabled();
    Object.defineProperty(window, "location", { configurable: true, value: original });
  });

  it("resumes a historical session into a live workspace", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, roots: true, sessions: true }, protocolOps: ["user.input"], status: { sessionId: "root-a", provider: "echo", busy: false }, agents: [{ name: "build" }], skills: [] });
      if (url.includes("/resume")) return response({ id: "hist-1", sessionId: "hist-1", resumedId: "hist-1", wasActive: false });
      if (url.includes("/v1/roots") && (!init || !init.method || init.method === "GET")) {
        // After resume, hist-1 becomes live.
        const resumed = fetchMock.mock.calls.some(([u]) => String(u).includes("/resume"));
        return response({
          roots: resumed
            ? [{ id: "root-a", title: "A", agent: "build", busy: false }, { id: "hist-1", title: "Saved work", agent: "build", busy: false }]
            : [{ id: "root-a", title: "A", agent: "build", busy: false }],
          activeId: resumed ? "hist-1" : "root-a",
        });
      }
      if (url.includes("sessions")) return response({ sessions: [{ id: "hist-1", title: "Saved work", mtime: 1 }, { id: "root-a", title: "A" }], liveId: "root-a" });
      if (url.includes("/activate")) return response({ ok: true });
      return response({ ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances.length).toBeGreaterThan(0));
    fireEvent.click(screen.getByRole("button", { name: "HISTORY" }));
    fireEvent.click(await screen.findByRole("button", { name: /Saved work/i }));
    fireEvent.click(screen.getByRole("button", { name: "Resume as workspace" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u]) => String(u).includes("/v1/roots/hist-1/resume"))).toBe(true));
    await waitFor(() => expect(FakeWebSocket.instances.some((ws) => ws.url.includes("root=hist-1"))).toBe(true));
    expect(screen.getByLabelText("Instruction")).not.toBeDisabled();
  });

  it("shows LIVE badge on history rows that are active roots", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, roots: true, sessions: true }, protocolOps: ["user.input"], agents: [], skills: [] });
      if (url.includes("/v1/roots")) return response({ roots: [{ id: "live-1", title: "Live one", agent: "build", busy: false }], activeId: "live-1" });
      if (url.includes("sessions")) return response({ sessions: [{ id: "live-1", title: "Live one", mtime: 1 }, { id: "old", title: "Old", mtime: 1, forkedFrom: "live-1" }], liveId: "live-1" });
      return response({ ok: true });
    }));
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances.length).toBeGreaterThan(0));
    fireEvent.click(screen.getByRole("button", { name: "HISTORY" }));
    expect(await screen.findByRole("button", { name: /Old/i })).toBeInTheDocument();
    expect(screen.getByText("LIVE")).toBeInTheDocument();
    expect(screen.getByText(/fork of live-1/i)).toBeInTheDocument();
  });


});
