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

  it("activates a live root on select and creates/switches workspaces", async () => {
    let activeId = "root-a";
    const rootsState = [
      { id: "root-a", title: "Alpha", agent: "build", busy: false, activeAt: Date.now() },
      { id: "root-b", title: "Beta", agent: "plan", busy: true, activeAt: Date.now() - 120_000 },
    ];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method || "GET").toUpperCase();
      if (url.includes("bootstrap")) {
        return response({
          version: "test", authRequired: false, attachOnly: false,
          capabilities: { live: true, roots: true, sessions: true },
          protocolOps: ["user.input"], status: { sessionId: "root-a", provider: "echo", busy: false },
          agents: [{ name: "build" }], skills: [],
        });
      }
      if (url.includes("/v1/sessions") && method === "GET") {
        return response({ sessions: [{ id: "root-a", title: "Alpha" }, { id: "root-b", title: "Beta" }, { id: "old", title: "Archived" }], liveId: activeId });
      }
      if (url.includes("/v1/roots") && method === "GET") {
        return response({ roots: rootsState, activeId });
      }
      if (url.includes("/activate") && method === "POST") {
        const id = url.split("/v1/roots/")[1]?.split("/")[0] || "";
        activeId = decodeURIComponent(id);
        return response({ ok: true });
      }
      if (url.endsWith("/v1/roots") && method === "POST") {
        const created = { id: "root-c", title: "Gamma", agent: "build", busy: false, activeAt: Date.now() };
        rootsState.push(created);
        activeId = created.id;
        return response({ id: created.id, sessionId: created.id }, 201);
      }
      if (url.includes("/v1/roots/") && method === "DELETE") {
        const id = decodeURIComponent(url.split("/v1/roots/")[1] || "");
        const idx = rootsState.findIndex((r) => r.id === id);
        if (idx >= 0) rootsState.splice(idx, 1);
        activeId = rootsState[0]?.id || "";
        return response({ ok: true });
      }
      return response({ ok: true });
    }));

    render(<App />);
    expect(await screen.findByText("Alpha")).toBeInTheDocument();
    expect(screen.getByText("Beta")).toBeInTheDocument();
    expect(screen.getByTitle("root-a")).toHaveTextContent(/build/);
    expect(screen.getByTitle("root-b")).toHaveTextContent(/plan/);
    expect(screen.getByTitle("root-a")).toHaveTextContent("IDLE");
    expect(screen.getByTitle("root-b")).toHaveTextContent("BUSY");
    expect(screen.getByTitle("root-a")).toHaveTextContent("ACTIVE");

    fireEvent.click(screen.getByTitle("root-b"));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining("/v1/roots/root-b/activate"), expect.objectContaining({ method: "POST" })));
    await waitFor(() => expect(screen.getByTitle("root-b")).toHaveTextContent("ACTIVE"));

    fireEvent.click(screen.getByRole("button", { name: "+ New workspace" }));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringMatching(/\/v1\/roots$/), expect.objectContaining({ method: "POST" })));
    await waitFor(() => expect(FakeWebSocket.instances.some((ws) => ws.url.includes("root=root-c"))).toBe(true));

    window.confirm = vi.fn(() => true);
    fireEvent.click(screen.getByTitle("root-b"));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining("/v1/roots/root-b/activate"), expect.anything()));
    fireEvent.click(screen.getByRole("button", { name: "Close workspace" }));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringMatching(/\/v1\/roots\/root-b$/), expect.objectContaining({ method: "DELETE" })));
    await waitFor(() => expect(screen.queryByTitle("root-b")).not.toBeInTheDocument());
  });

  it("attach-only mode never offers live create or close", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) {
        return response({ version: "test", authRequired: false, attachOnly: true, capabilities: { live: false, roots: false, sessions: true }, protocolOps: null, agents: [], skills: [] });
      }
      if (url.includes("sessions")) return response({ sessions: [{ id: "saved", title: "Saved" }] });
      if (url.includes("roots")) return Promise.resolve(new Response("multi-root unavailable", { status: 503 }));
      return response({ ok: true });
    }));
    render(<App />);
    await screen.findByText("Saved");
    expect(screen.queryByRole("button", { name: "+ New workspace" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close workspace" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Resume as workspace" })).not.toBeInTheDocument();
  });

  it("previews rewind paths and confirms chat-and-files restore", async () => {
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    FakeWebSocket.instances[0].onmessage?.({
      data: JSON.stringify({
        type: "turn.completed",
        time: "t1",
        data: {
          files: [{ path: "web/src/App.tsx", kind: "update" }],
          checkpointSkipped: 1,
          uncovered: ["bash"],
        },
      }),
    } as MessageEvent);

    fireEvent.change(screen.getByLabelText("Instruction"), { target: { value: "/rewind-files" } });
    fireEvent.submit(screen.getByLabelText("Instruction").closest("form")!);

    const dialog = await screen.findByRole("dialog", { name: "Undo last turn" });
    expect(dialog).toHaveTextContent("Paths to restore (1):");
    expect(dialog).toHaveTextContent("web/src/App.tsx");
    expect(dialog).toHaveTextContent("Checkpoint skipped: 1 path(s)");
    expect(dialog).toHaveTextContent("uncovered mutations (bash)");
    expect(dialog).toHaveTextContent("chat only");
    expect(dialog).toHaveTextContent("chat and files");
    expect(screen.getByRole("radio", { name: /chat and files/i })).toBeChecked();

    // Confirm must not have fired rewind yet.
    const opsBefore = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls.filter((c) => String(c[0]).includes("/v1/ops"));
    expect(opsBefore.some((c) => String(c[1]?.body || "").includes("rewind"))).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "Confirm undo" }));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/v1/ops"),
      expect.objectContaining({ body: expect.stringContaining('"type":"rewind"') }),
    ));
    const rewindCall = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls
      .map((c) => c[1]?.body)
      .filter(Boolean)
      .map((body) => JSON.parse(String(body)))
      .find((body) => body.type === "rewind");
    expect(rewindCall).toEqual(expect.objectContaining({ type: "rewind", data: { restoreFiles: true } }));
    expect(screen.queryByRole("dialog", { name: "Undo last turn" })).not.toBeInTheDocument();
  });
  it("cancels rewind preview without sending an op", async () => {
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const callsBefore = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls.length;

    fireEvent.change(screen.getByLabelText("Instruction"), { target: { value: "/rewind" } });
    fireEvent.submit(screen.getByLabelText("Instruction").closest("form")!);
    expect(await screen.findByRole("dialog", { name: "Undo last turn" })).toBeInTheDocument();
    // Empty preview biases chat-only (TUI parity).
    expect(screen.getByRole("radio", { name: /chat only/i })).toBeChecked();

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog", { name: "Undo last turn" })).not.toBeInTheDocument();

    const newCalls = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls.slice(callsBefore);
    expect(newCalls.some((c) => String(c[1]?.body || "").includes("rewind"))).toBe(false);
  });
});
