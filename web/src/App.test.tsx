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
    await waitFor(() => expect(fetch).toHaveBeenCalledWith("/v1/ops", expect.objectContaining({ method: "POST" })));
  });

  it("renders and resolves a blocking permission dialog", async () => {
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    FakeWebSocket.instances[0].onmessage?.({ data: JSON.stringify({ type: "permission.asked", data: { requestId: "p1", tool: "bash" } }) } as MessageEvent);
    expect(await screen.findByRole("dialog", { name: "Permission required" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await waitFor(() => expect(fetch).toHaveBeenLastCalledWith("/v1/ops", expect.objectContaining({ body: expect.stringContaining("permission.reply") })));
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

  it("shows tested unavailable host workflows and unknown measurements", async () => {
    render(<App />);
    await screen.findByText("Current");
    expect(screen.getAllByText("not reported")).toHaveLength(2);
    fireEvent.click(screen.getByRole("tab", { name: "files" }));
    expect(screen.getByRole("status")).toHaveTextContent("Workspace browsing and file mentions unavailable");
    fireEvent.click(screen.getByRole("tab", { name: "capabilities" }));
    expect(screen.getByText("Concurrent roots").parentElement).toHaveTextContent("Unavailable on this host");
    expect(screen.getByText(/user.input/)).toHaveTextContent("compact");
  });

  it("shows protocol operations unavailable and uses historical SSE in attach-only mode", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input).includes("bootstrap") ? response({ version: "test", authRequired: false, attachOnly: true, capabilities: { live: false }, protocolOps: null, agents: [], skills: [] }) : String(input).includes("sessions") ? response({ sessions: [{ id: "saved", title: "Saved" }] }) : response({ ok: true })));
    render(<App />);
    await screen.findByText("Saved");
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
    expect(FakeEventSource.instances[0].url).toContain("/v1/sessions/saved/events");
    expect(FakeWebSocket.instances).toHaveLength(0);
    fireEvent.click(screen.getByRole("tab", { name: "capabilities" }));
    expect(screen.getByText("Unavailable in attach-only mode")).toBeInTheDocument();
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
    await waitFor(() => expect(fetch).toHaveBeenLastCalledWith("/v1/ops", expect.objectContaining({ body: expect.stringContaining('"requestId":"q1"') })));
  });
});
