import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App, { formatCostLabel, formatContextLabel } from "./App";

class FakeEventSource { static instances: FakeEventSource[] = []; onmessage?: (event: MessageEvent) => void; onerror?: () => void; close = vi.fn(); constructor(public url: string) { FakeEventSource.instances.push(this); } }
class FakeWebSocket { static OPEN = 1; static instances: FakeWebSocket[] = []; readyState = 1; onopen?: () => void; onmessage?: (event: MessageEvent) => void; onerror?: () => void; onclose?: () => void; send = vi.fn(); close = vi.fn(); constructor(public url: string) { FakeWebSocket.instances.push(this); queueMicrotask(() => this.onopen?.()); } }

const response = (body: unknown, status = 200) => Promise.resolve(new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));

describe("App", () => {
  beforeEach(() => {
    try { window.history.replaceState(null, "", "/"); } catch { /* jsdom */ }
    FakeEventSource.instances = []; FakeWebSocket.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource); vi.stubGlobal("WebSocket", FakeWebSocket);
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input).includes("bootstrap") ? response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, diag: true, files: false, memory: false, issues: false, roots: false }, protocolOps: ["user.input", "compact", "rewind"], status: { sessionId: "live", provider: "echo", busy: false }, agents: [{ name: "build" }], skills: [{ name: "ship", description: "Ship changes" }] }) : String(input).includes("sessions") ? response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" }) : response({ ok: true })));
    Element.prototype.scrollIntoView = vi.fn();
    // Preserve URL constructor (React.lazy / Vite dynamic import need it); only stub blob helpers.
    vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:diag");
    vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    // Deep-link replaceState must not leak mode/surface across tests.
    try { window.history.replaceState(null, "", "/"); } catch { /* jsdom */ }
  });


  it("exposes skip-all in the autonomy control and wires set.autonomy", async () => {
    render(<App />);
    await screen.findByText("Current");
    fireEvent.click(screen.getByRole("button", { name: /Runtime/ }));
    const autonomy = screen.getByLabelText("Autonomy") as HTMLSelectElement;
    expect([...autonomy.options].map((o) => o.value)).toEqual(["", "supervised", "agent", "checks", "skip-all"]);
    fireEvent.change(autonomy, { target: { value: "skip-all" } });
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/v1/ops"),
      expect.objectContaining({ body: expect.stringContaining('"type":"set.autonomy"') }),
    ));
  });

  it("shows cost empty state then token totals after usage.reported", async () => {
    render(<App />);
    await screen.findByText("Current");
    await waitFor(() => expect(FakeWebSocket.instances.length).toBeGreaterThan(0));
    expect(screen.queryByText(/in 1,200/)).not.toBeInTheDocument();
    FakeWebSocket.instances[0].onmessage?.({
      data: JSON.stringify({
        type: "usage.reported",
        time: "1",
        data: {
          input: { n: 1200, known: true },
          output: { n: 450, known: true },
          used: { n: 1650, known: true },
          source: "actual",
        },
      }),
    } as MessageEvent);
    expect(await screen.findByText(/in 1,200 · out 450/)).toBeInTheDocument();
    expect(screen.getByLabelText("Session status")).toHaveTextContent(/1,650/);
  });

  it("toggles thinking visibility and persists the preference", async () => {
    sessionStorage.clear();
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances.length).toBeGreaterThan(0));
    fireEvent.click(screen.getByRole("button", { name: /Runtime/ }));
    FakeWebSocket.instances[0].onmessage?.({
      data: JSON.stringify({ type: "reasoning.delta", time: "1", data: { turnId: "t", text: "secret chain of thought" } }),
    } as MessageEvent);
    expect(await screen.findByText("secret chain of thought")).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Show thinking"));
    expect(screen.queryByText("secret chain of thought")).not.toBeInTheDocument();
    expect(sessionStorage.getItem("strike.web.showThinking")).toBe("0");
    fireEvent.click(screen.getByLabelText("Show thinking"));
    expect(screen.getByText("secret chain of thought")).toBeInTheDocument();
    expect(sessionStorage.getItem("strike.web.showThinking")).toBe("1");
  });

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
    fireEvent.change(screen.getByLabelText(/Instruction/), { target: { value: "/ex" } });
    expect(screen.getByRole("option", { name: /export/ })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Instruction/), { target: { value: "/he" } });
    expect(screen.getByRole("option", { name: /help/ })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Instruction/), { target: { value: "/hlp" } });
    expect(screen.getByRole("option", { name: /help/ })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Instruction/), { target: { value: "note\n/he" } });
    expect(screen.queryByRole("listbox", { name: "Composer completions" })).not.toBeInTheDocument();
  });

  it("accepts slash completions with keyboard and keeps footer Send as the only primary", async () => {
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const box = screen.getByLabelText("Instruction");
    fireEvent.change(box, { target: { value: "/" } });
    const list = screen.getByRole("listbox", { name: "Composer completions" });
    expect(list.className).toContain("completion");
    expect(list.parentElement).toHaveClass("composer");
    const help = screen.getByRole("option", { name: /help/i });
    expect(help).toHaveAttribute("aria-selected", "true");
    expect(help).not.toHaveClass("composer-send");

    const options = within(list).getAllByRole("option");
    expect(options.length).toBeGreaterThan(2);
    expect(options[0]).toHaveAccessibleName(/help/i);
    expect(options[1]).toHaveAccessibleName(/export/i);
    expect(options[0].compareDocumentPosition(options[1]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    fireEvent.keyDown(box, { key: "ArrowDown" });
    expect(within(list).getAllByRole("option")[1]).toHaveAttribute("aria-selected", "true");
    expect(help).toHaveAttribute("aria-selected", "false");
    fireEvent.keyDown(box, { key: "ArrowUp" });
    expect(within(list).getAllByRole("option")[0]).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(box, { key: "n", ctrlKey: true });
    expect(within(list).getAllByRole("option")[1]).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("option", { name: /export/i })).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(box, { key: "Enter", shiftKey: true });
    expect(box).toHaveValue("/");
    expect(screen.getByRole("listbox", { name: "Composer completions" })).toBeInTheDocument();

    fireEvent.keyDown(box, { key: "Tab", shiftKey: true });
    expect(box).toHaveValue("/");
    expect(screen.getByRole("listbox", { name: "Composer completions" })).toBeInTheDocument();

    fireEvent.keyDown(box, { key: "Enter" });
    expect(box).toHaveValue("/export ");
    expect(box).not.toHaveValue("/\n");
    expect(screen.queryByRole("listbox", { name: "Composer completions" })).not.toBeInTheDocument();

    fireEvent.change(box, { target: { value: "/" } });
    fireEvent.keyDown(box, { key: "Escape" });
    expect(screen.queryByRole("listbox", { name: "Composer completions" })).not.toBeInTheDocument();
    expect(box).toHaveValue("/");

    fireEvent.change(box, { target: { value: "" } });
    fireEvent.change(box, { target: { value: "/" } });
    fireEvent.keyDown(box, { key: "Tab" });
    expect(box).toHaveValue("/help ");

    expect(screen.getByRole("button", { name: "Send" })).toHaveClass("composer-send");
    expect(screen.getByRole("button", { name: "Attach" })).toHaveClass("composer-secondary");
    expect(screen.getByRole("button", { name: "Export" })).toHaveClass("composer-secondary");
    expect(screen.getByRole("button", { name: "Attach" })).not.toHaveClass("composer-send");
  });

  it("does not steal ArrowUp/Enter from history browse when the recalled prompt is a slash token", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) {
        return response({
          version: "test", authRequired: false, attachOnly: false,
          capabilities: { live: true, history: true, roots: false },
          protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false },
          agents: [], skills: [{ name: "ship", description: "Ship changes" }],
        });
      }
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("/v1/history")) return response({ entries: ["fix tests", "/ship"] });
      return response({ ok: true });
    }));
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    await screen.findByRole("button", { name: "History" });
    const box = screen.getByLabelText("Instruction") as HTMLTextAreaElement;
    expect(box).toHaveValue("");
    fireEvent.keyDown(box, { key: "ArrowUp" });
    expect(box).toHaveValue("/ship");
    expect(screen.queryByRole("listbox", { name: "Composer completions" })).not.toBeInTheDocument();
    fireEvent.keyDown(box, { key: "ArrowUp" });
    expect(box).toHaveValue("fix tests");
    fireEvent.keyDown(box, { key: "ArrowDown" });
    expect(box).toHaveValue("/ship");
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/v1/ops"),
      expect.objectContaining({ body: expect.stringMatching(/"text":"\/ship"/) }),
    ));
    expect(box).not.toHaveValue("/ship ");
  });

  it("shows @file completions in the same listbox chrome", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) {
        return response({
          version: "test", authRequired: false, attachOnly: false,
          capabilities: { live: true, files: true, roots: false },
          protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false },
          agents: [], skills: [],
        });
      }
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("/v1/files/search")) return response({ paths: ["go.mod", "LICENSE"] });
      return response({ ok: true });
    }));
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const box = screen.getByLabelText("Instruction") as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "@go", selectionStart: 3, selectionEnd: 3 } });
    const list = await screen.findByRole("listbox", { name: "Composer completions" });
    const options = within(list).getAllByRole("option");
    expect(options[0]).toHaveAccessibleName(/go\.mod/);
    expect(options[1]).toHaveAccessibleName(/LICENSE/);
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    expect(options[0]).not.toHaveClass("composer-send");
    expect(list).toHaveClass("completion");
    fireEvent.keyDown(box, { key: "ArrowDown" });
    expect(within(list).getAllByRole("option")[1]).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(box, { key: "ArrowUp" });
    expect(within(list).getAllByRole("option")[0]).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(box, { key: "Enter" });
    expect((box as HTMLTextAreaElement).value).toContain("@go.mod");
    expect((box as HTMLTextAreaElement).value).not.toContain("@go\n");
  });

  it("shows an empty @file hint when search returns no hits", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) {
        return response({
          version: "test", authRequired: false, attachOnly: false,
          capabilities: { live: true, files: true, roots: false },
          protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false },
          agents: [], skills: [],
        });
      }
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("/v1/files/search")) return response({ paths: [] });
      return response({ ok: true });
    }));
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const box = screen.getByLabelText("Instruction") as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "@nope", selectionStart: 5, selectionEnd: 5 } });
    const list = await screen.findByRole("listbox", { name: "Composer completions" });
    expect(list).toHaveClass("completion");
    expect(list).toHaveTextContent(/no files match/i);
    expect(within(list).queryByRole("option")).not.toBeInTheDocument();
    fireEvent.keyDown(box, { key: "Enter" });
    expect(box).toHaveValue("@nope");
    expect(screen.getByRole("listbox", { name: "Composer completions" })).toBeInTheDocument();
    const opsCalls = vi.mocked(fetch).mock.calls.filter(([url, init]) => String(url).includes("/v1/ops") && (init as RequestInit | undefined)?.method === "POST");
    expect(opsCalls.some(([, init]) => String((init as RequestInit).body || "").includes("@nope"))).toBe(false);
    fireEvent.keyDown(box, { key: "Escape" });
    expect(screen.queryByRole("listbox", { name: "Composer completions" })).not.toBeInTheDocument();
    expect(box).toHaveValue("@nope");
  });

  it("opens and closes @file completion when the caret moves in or out of the mention", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) {
        return response({
          version: "test", authRequired: false, attachOnly: false,
          capabilities: { live: true, files: true, roots: false },
          protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false },
          agents: [], skills: [],
        });
      }
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("/v1/files/search")) return response({ paths: ["src/main.go"] });
      return response({ ok: true });
    }));
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const box = screen.getByLabelText("Instruction") as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "see @src extra", selectionStart: 14, selectionEnd: 14 } });
    expect(screen.queryByRole("listbox", { name: "Composer completions" })).not.toBeInTheDocument();
    fireEvent.click(box, { target: { selectionStart: 8, selectionEnd: 8 } });
    expect(await screen.findByRole("option", { name: /src\/main\.go/ })).toBeInTheDocument();
    fireEvent.click(box, { target: { selectionStart: 0, selectionEnd: 0 } });
    expect(screen.queryByRole("listbox", { name: "Composer completions" })).not.toBeInTheDocument();
  });

  it("rewrites the whole @file mention when accepting mid-token", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) {
        return response({
          version: "test", authRequired: false, attachOnly: false,
          capabilities: { live: true, files: true, roots: false },
          protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false },
          agents: [], skills: [],
        });
      }
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("/v1/files/search")) return response({ paths: ["internal/tui/app.go"] });
      return response({ ok: true });
    }));
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const box = screen.getByLabelText("Instruction") as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "see @src/old.go extra", selectionStart: 8, selectionEnd: 8 } });
    await screen.findByRole("option", { name: /internal\/tui\/app\.go/ });
    fireEvent.keyDown(box, { key: "Enter" });
    expect(box).toHaveValue("see @internal/tui/app.go extra");
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

  it("always shows context doctor tab; omits capability-gated tabs when absent", async () => {
    render(<App />);
    await screen.findByText("Current");
    expect(screen.getByRole("tab", { name: "context" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Context doctor" })).toBeInTheDocument();
    expect(screen.queryByText(/catches up with the TUI/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "files" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "memory" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "issues" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "plans" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "workflows" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "mcp" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "activity" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "project" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "capabilities" })).not.toBeInTheDocument();
    expect(screen.queryByText("No inspector panels available for this host.")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Session status")).not.toBeInTheDocument();
  });

  it("lists context plus capability-backed inspector tabs and defaults to files", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, files: true, memory: false, issues: true, workflows: true, roots: false }, protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false }, agents: [{ name: "build" }], skills: [] });
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("changed-files")) return response({ files: [] });
      if (url.includes("issues")) return response({ issues: [] });
      if (url.includes("workflows")) return response({ workflows: [] });
      return response({ ok: true });
    }));
    render(<App />);
    await screen.findByText("Current");
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    expect(screen.getByRole("tab", { name: "context" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "files" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: "issues" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "workflows" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "memory" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "plans" })).not.toBeInTheDocument();
    expect(await screen.findByText("No changed files reported.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Session status")).not.toBeInTheDocument();
    FakeWebSocket.instances[0].onmessage?.({ data: JSON.stringify({ type: "phase.changed", data: { phase: "act", workflow: "plan-implement" } }) } as MessageEvent);
    FakeWebSocket.instances[0].onmessage?.({ data: JSON.stringify({ type: "status", data: { sessionId: "live", provider: "echo", phase: "act", workflow: "plan-implement", contextUsed: 1200, contextLimit: 8000, busy: false } }) } as MessageEvent);
    const status = await screen.findByLabelText("Session status");
    expect(status).toHaveTextContent("Phase act");
    expect(status).toHaveTextContent("Workflow plan-implement");
    expect(status).toHaveTextContent("Context 1,200 / 8,000");
    fireEvent.click(screen.getByRole("tab", { name: "issues" }));
    expect(await screen.findByText("No project issues.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "workflows" }));
    expect(await screen.findByText("No workflows loaded.")).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "mcp" })).not.toBeInTheDocument();
  });

  it("renders context doctor breakdown, fit warning, and pin/exclude ops", async () => {
    render(<App />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    fireEvent.click(screen.getByRole("tab", { name: "context" }));
    const ws = FakeWebSocket.instances[0];
    ws.onmessage?.({
      data: JSON.stringify({
        type: "context.fit_warning",
        time: "1",
        data: {
          level: "warn",
          message: "projected prompt ~90k tok is ≥50% of the 200k context window",
          estimatedTokens: 90_000,
          contextLimit: 200_000,
          source: "estimated",
        },
      }),
    } as MessageEvent);
    expect(await screen.findByRole("alert", { name: "Context fit warning" })).toHaveTextContent("projected prompt");
    expect(screen.getByText("90,000 / 200,000")).toBeInTheDocument();

    ws.onmessage?.({
      data: JSON.stringify({
        type: "prompt.effective",
        time: "2",
        data: {
          fromLastStream: true,
          systemChars: 400,
          messageCount: 2,
          layers: [
            { kind: "shared", source: "builtin:shared", mode: "append", chars: 100, estTokens: 25 },
            { kind: "persona", source: "agent:build", mode: "replace", chars: 300, estTokens: 75 },
          ],
          attribution: {
            system: { n: 100, known: true },
            tools: { n: 40, known: true },
            messages: { n: 200, known: true },
            toolResults: { n: 0, known: true },
            total: { n: 340, known: true },
            source: "estimated",
          },
          pinnedKinds: [],
          excludedKinds: [],
        },
      }),
    } as MessageEvent);

    expect(await screen.findByRole("heading", { name: "Tokens by source" })).toBeInTheDocument();
    expect(screen.getAllByRole("table").length).toBeGreaterThanOrEqual(2);
    expect(screen.getAllByText("shared").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("persona").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText("~340")).toBeInTheDocument();

    const pinButtons = screen.getAllByRole("button", { name: "Pin" });
    fireEvent.click(pinButtons[0]);
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/v1/ops"),
      expect.objectContaining({
        method: "POST",
        body: expect.stringContaining("context.controls"),
      }),
    ));
    const lastCall = (fetch as unknown as { mock: { calls: unknown[][] } }).mock.calls.at(-1);
    const body = String((lastCall?.[1] as { body?: string } | undefined)?.body || "");
    expect(body).toContain("setPin");
    expect(body).toContain("shared");

    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => {
      const bodies = (fetch as unknown as { mock: { calls: unknown[][] } }).mock.calls
        .map((c) => String((c[1] as { body?: string } | undefined)?.body || ""));
      expect(bodies.some((b) => b.includes("inspect.prompt"))).toBe(true);
    });
  });


  it("loads and saves settings dials when capability is present", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("bootstrap")) {
        return response({
          version: "test", authRequired: false, attachOnly: false,
          capabilities: { live: true, settings: true, auth: false, roots: false },
          protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false },
          agents: [{ name: "build" }], skills: [],
        });
      }
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("/v1/settings") && (!init || !init.method || init.method === "GET")) {
        return response({
          provider: "echo", model: "dev", sandbox: "workspace-write",
          permissionAutoApproveSeconds: 10, maxChildDepth: 1, compactionStrategy: "trim",
        });
      }
      if (url.includes("/v1/settings") && init?.method === "PATCH") {
        return response({ sandbox: "read-only" });
      }
      return response({ ok: true });
    }));
    render(<App />);
    await screen.findByText("Current");
    fireEvent.click(screen.getByRole("button", { name: "Open settings" }));
    expect(await screen.findByLabelText("Sandbox")).toHaveValue("workspace-write");
    expect(screen.getByLabelText("Countdown seconds")).toHaveValue("10");
    fireEvent.change(screen.getByLabelText("Sandbox"), { target: { value: "read-only" } });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/v1/settings"),
      expect.objectContaining({ method: "PATCH", body: expect.stringContaining('"sandbox":"read-only"') }),
    ));
  });

  it("uses historical SSE in attach-only mode", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input).includes("bootstrap") ? response({ version: "test", authRequired: false, attachOnly: true, capabilities: { live: false, memory: true }, protocolOps: null, agents: [], skills: [] }) : String(input).includes("sessions") ? response({ sessions: [{ id: "saved", title: "Saved" }] }) : String(input).includes("roots") ? Promise.resolve(new Response("multi-root unavailable", { status: 503 })) : String(input).includes("memory") ? response({ entries: [] }) : response({ ok: true })));
    render(<App />);
    await screen.findByText("Saved");
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
    expect(FakeEventSource.instances[0].url).toContain("/v1/sessions/saved/events");
    expect(FakeWebSocket.instances).toHaveLength(0);
    expect(screen.getByRole("tab", { name: "memory" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "files" })).not.toBeInTheDocument();
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
    expect(screen.getByRole("button", { name: "Toggle inspector" })).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(screen.getByRole("button", { name: "Toggle inspector" }));
    expect(screen.getByRole("button", { name: "Toggle inspector" })).toHaveAttribute("aria-pressed", "true");
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
    const dialog = screen.getByRole("dialog", { name: "Workspace settings" });
    expect(dialog).toHaveTextContent("Provider authentication unavailable");
    expect(dialog).toHaveTextContent("Saved defaults unavailable");
    expect(dialog.querySelector('[aria-label="Download diagnostics"]')).not.toBeDisabled();
  });

  it("downloads diagnostics from the context inspector when live diag is available", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, diag: true, roots: false }, protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false }, agents: [], skills: [] });
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("/v1/diag")) {
        return Promise.resolve(new Response(JSON.stringify({ schemaVersion: "1.0.0", redacted: true }), {
          status: 200,
          headers: { "Content-Type": "application/json", "Content-Disposition": 'attachment; filename="strike-diag-live-test.json"' },
        }));
      }
      return response({ ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<App />);
    await screen.findByText("Current");
    fireEvent.click(screen.getByRole("button", { name: "Open settings" }));
    const button = screen.getByRole("button", { name: "Download diagnostics" });
    expect(button).toBeEnabled();
    fireEvent.click(button);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining("/v1/diag"), expect.objectContaining({ credentials: "same-origin" })));
    expect(URL.createObjectURL).toHaveBeenCalled();
    expect(URL.revokeObjectURL).toHaveBeenCalled();
  });

  it("disables diagnostic download when diag capability is absent", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input).includes("bootstrap") ? response({ version: "test", authRequired: false, attachOnly: true, capabilities: { live: false, diag: false }, protocolOps: null, agents: [], skills: [] }) : String(input).includes("sessions") ? response({ sessions: [{ id: "saved", title: "Saved" }] }) : String(input).includes("roots") ? Promise.resolve(new Response("multi-root unavailable", { status: 503 })) : response({ ok: true })));
    render(<App />);
    await screen.findByText("Saved");
    fireEvent.click(screen.getByRole("button", { name: "Open settings" }));
    expect(screen.getByRole("button", { name: "Download diagnostics" })).toBeDisabled();
    expect(screen.getByText(/Unavailable on this host/)).toBeInTheDocument();
  });

  it("keeps secondary runtime controls behind disclosure and issues set ops", async () => {
    render(<App />);
    await screen.findByText("Current");
    await waitFor(() => expect(FakeWebSocket.instances.length).toBeGreaterThan(0));
    const runtime = screen.getByLabelText("Runtime controls");
    expect(runtime.querySelectorAll("select")).toHaveLength(3);
    expect(screen.queryByLabelText("Effort")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Autonomy")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Permission")).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/FAST/i)).not.toBeInTheDocument();
    const ws = FakeWebSocket.instances.at(-1)!;
    await act(async () => {
      ws.onmessage?.({ data: JSON.stringify({ type: "effort.selected", data: { level: "high" } }) } as MessageEvent);
      ws.onmessage?.({ data: JSON.stringify({ type: "autonomy.selected", data: { mode: "agent" } }) } as MessageEvent);
      ws.onmessage?.({ data: JSON.stringify({ type: "permission.mode", data: { mode: "default" } }) } as MessageEvent);
    });
    expect(screen.getByRole("button", { name: /Runtime/ })).toHaveTextContent("high · agent · default");
    fireEvent.click(screen.getByRole("button", { name: /Runtime/ }));
    expect(screen.getByRole("button", { name: /Runtime/ })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByLabelText("Secondary runtime controls")).toBeInTheDocument();
    expect(screen.getByLabelText("Effort")).toHaveValue("high");
    fireEvent.change(screen.getByLabelText("Effort"), { target: { value: "low" } });
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining("/v1/ops"), expect.objectContaining({ body: expect.stringContaining('"type":"set.effort"') })));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining("/v1/ops"), expect.objectContaining({ body: expect.stringContaining('"level":"low"') })));
    fireEvent.click(screen.getByLabelText(/FAST/i));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining("/v1/ops"), expect.objectContaining({ body: expect.stringContaining('"type":"set.fast"') })));
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
      if (url.includes("/children") && method === "GET") return response({ sessions: [] });
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


  it("adjusts panel width from the drag-handle separator via keyboard", async () => {
    render(<App />);
    await screen.findByText("Current");
    const handle = screen.getByRole("separator", { name: "Resize agents panel" });
    expect(handle).toHaveAttribute("aria-valuenow", "240");
    fireEvent.keyDown(handle, { key: "ArrowRight" });
    expect(handle).toHaveAttribute("aria-valuenow", "250");
    fireEvent.keyDown(handle, { key: "ArrowLeft" });
    expect(handle).toHaveAttribute("aria-valuenow", "240");
    fireEvent.keyDown(handle, { key: "End" });
    expect(handle).toHaveAttribute("aria-valuenow", "420");
    fireEvent.keyDown(handle, { key: "Home" });
    expect(handle).toHaveAttribute("aria-valuenow", "180");
  });

  it("defaults inspector closed and hides empty child-agents chrome", async () => {
    render(<App />);
    await screen.findByText("Current");
    expect(screen.getByRole("button", { name: "Toggle inspector" })).toHaveAttribute("aria-pressed", "false");
    expect(document.querySelector(".app-shell")).toHaveStyle({ "--inspector-width": "0px" });
    expect(screen.queryByText("None dispatched")).not.toBeInTheDocument();
    expect(screen.queryByText("CHILD AGENTS")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Child agents")).not.toBeInTheDocument();
    // workspace meta is collapsed details, not a permanent ROOT/BUILD footer competing with sessions
    const meta = screen.getByText("Workspace").closest("details");
    expect(meta).toBeTruthy();
    expect(meta).not.toHaveAttribute("open");
  });

  it("keeps fork/rename/delete behind a Session menu when sessions capability is on", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, sessions: true, roots: false }, protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false }, agents: [{ name: "build" }], skills: [] });
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      return response({ ok: true });
    }));
    render(<App />);
    await screen.findByText("Current");
    expect(screen.queryByRole("button", { name: "Fork" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByText("Session…"));
    expect(screen.getByRole("menuitem", { name: "Fork" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Rename" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Delete" })).toBeInTheDocument();
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

  it("isolates drafts, queues, permissions, and transcripts across workspace switches", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("bootstrap")) {
        return response({
          version: "test", authRequired: false, attachOnly: false,
          capabilities: { live: true, roots: true, sessions: true },
          protocolOps: ["user.input", "permission.reply"],
          status: { sessionId: "root-a", provider: "echo", busy: false },
          agents: [{ name: "build" }], skills: [],
        });
      }
      if (url.includes("/v1/roots") && (!init || !init.method || init.method === "GET")) {
        return response({
          roots: [
            { id: "root-a", agent: "AgentA", busy: false },
            { id: "root-b", agent: "AgentB", busy: false },
          ],
          activeId: "root-a",
        });
      }
      if (url.includes("sessions")) {
        return response({
          sessions: [{ id: "root-a", title: "A" }, { id: "root-b", title: "B" }],
          liveId: "root-a",
        });
      }
      return response({ ok: true });
    }));

    render(<App />);
    await screen.findByText("AgentA");
    await waitFor(() => expect(FakeWebSocket.instances.length).toBeGreaterThanOrEqual(1));
    const wsA = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    expect(wsA.url).toContain("root=root-a");

    fireEvent.change(screen.getByLabelText("Instruction"), { target: { value: "draft for A" } });
    wsA.onmessage?.({ data: JSON.stringify({ type: "turn.started", time: "t1", data: { turnId: "ta" } }) } as MessageEvent);
    fireEvent.change(screen.getByLabelText(/Instruction/), { target: { value: "queued on A" } });
    fireEvent.submit(screen.getByLabelText(/Instruction/).closest("form")!);
    expect(screen.getByRole("list", { name: "Queued prompts" })).toHaveTextContent("queued on A");
    fireEvent.change(screen.getByLabelText("Instruction"), { target: { value: "still drafting A" } });
    wsA.onmessage?.({ data: JSON.stringify({ type: "user.message", time: "t2", data: { text: "hello A", turnId: "ta" } }) } as MessageEvent);
    wsA.onmessage?.({ data: JSON.stringify({ type: "permission.asked", time: "t3", data: { requestId: "pa", tool: "bash" } }) } as MessageEvent);
    expect(await screen.findByRole("dialog", { name: "Permission required" })).toBeInTheDocument();
    expect(screen.getByText("hello A")).toBeInTheDocument();

    const socketsBeforeB = FakeWebSocket.instances.length;
    fireEvent.click(screen.getByText("AgentB"));
    await waitFor(() => expect(FakeWebSocket.instances.length).toBeGreaterThan(socketsBeforeB));
    const wsB = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    expect(wsB.url).toContain("root=root-b");
    expect(wsA.close).toHaveBeenCalled();

    // B starts clean: no A's draft, queue, permission, or transcript.
    expect(screen.queryByRole("dialog", { name: "Permission required" })).not.toBeInTheDocument();
    expect(screen.queryByText("hello A")).not.toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Queued prompts" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("Instruction")).toHaveValue("");

    fireEvent.change(screen.getByLabelText("Instruction"), { target: { value: "draft for B" } });
    wsB.onmessage?.({ data: JSON.stringify({ type: "user.message", time: "tb1", data: { text: "hello B", turnId: "tb" } }) } as MessageEvent);
    expect(await screen.findByText("hello B")).toBeInTheDocument();

    // Late event on closed A socket must not interleave into B's transcript.
    wsA.onmessage?.({ data: JSON.stringify({ type: "user.message", time: "late", data: { text: "late A leak", turnId: "ta2" } }) } as MessageEvent);
    expect(screen.queryByText("late A leak")).not.toBeInTheDocument();
    expect(screen.getByText("hello B")).toBeInTheDocument();

    fireEvent.click(screen.getByText("AgentA"));
    await waitFor(() => expect(screen.getByLabelText("Instruction")).toHaveValue("still drafting A"));
    expect(screen.getByRole("list", { name: "Queued prompts" })).toHaveTextContent("queued on A");
    expect(screen.getByText("hello A")).toBeInTheDocument();
    expect(screen.queryByText("hello B")).not.toBeInTheDocument();
    expect(await screen.findByRole("dialog", { name: "Permission required" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/v1/ops?root=root-a"),
      expect.objectContaining({ body: expect.stringContaining("permission.reply") }),
    ));
  });

  it("shows timeline inspector tab when capability is set and loads snapshot", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) {
        return response({
          version: "test", authRequired: false, attachOnly: false,
          capabilities: { live: true, files: true, timeline: true, roots: false },
          protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false },
          agents: [{ name: "build" }], skills: [],
        });
      }
      if (url.includes("/timeline") && !url.includes("export")) {
        return response({ sessionId: "live", entries: [] });
      }
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      return response({ ok: true });
    }));
    render(<App />);
    await screen.findByText("Current");
    expect(screen.getByRole("tab", { name: "timeline" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "timeline" }));
    expect(await screen.findByRole("heading", { name: "Run timeline" })).toBeInTheDocument();
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining("/v1/sessions/live/timeline"), expect.anything()));
  });

  it("shows diagnostics inspector tab when lsp capability is set", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) {
        return response({
          version: "test", authRequired: false, attachOnly: false,
          capabilities: { live: true, files: true, lsp: true, roots: false },
          protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false },
          agents: [{ name: "build" }], skills: [],
        });
      }
      if (url.includes("/v1/lsp")) return response({ servers: [] });
      if (url.includes("/v1/diagnostics")) return response({ diagnostics: [], count: 0 });
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      return response({ ok: true });
    }));
    render(<App />);
    await screen.findByText("Current");
    expect(screen.getByRole("tab", { name: "diagnostics" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "diagnostics" }));
    // Lazy surface + async LSP fetch — wait past Suspense fallback and loading state.
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Diagnostics" })).toBeInTheDocument();
    }, { timeout: 4000 });
  });


  it("selects a deep-linked session id on boot", async () => {
    const original = window.location;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { ...original, search: "?session=hist-1", pathname: "/attach", href: "http://localhost/attach?session=hist-1" },
    });
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, roots: true, sessions: true }, protocolOps: ["user.input"], status: { sessionId: "root-a", provider: "echo", busy: false }, agents: [{ name: "build" }], skills: [] });
      if (url.includes("/v1/roots")) return response({ roots: [{ id: "root-a", title: "A", agent: "build", busy: false }], activeId: "root-a" });
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
    Object.defineProperty(window, "location", { configurable: true, value: original });
  });

  it("resumes a historical session into a live workspace", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method || "GET").toUpperCase();
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, roots: true, sessions: true }, protocolOps: ["user.input"], status: { sessionId: "root-a", provider: "echo", busy: false }, agents: [{ name: "build" }], skills: [] });
      if (url.includes("/resume")) return response({ id: "hist-1", sessionId: "hist-1", resumedId: "hist-1", wasActive: false });
      if (url.includes("/v1/roots") && method === "GET") {
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
  });

  it("shows LIVE badge and fork lineage on history rows", async () => {
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



  it("boots multi-root ACTIVE/HISTORY tabs and isolates drafts across workspaces", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method || "GET").toUpperCase();
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, roots: true, sessions: true }, protocolOps: ["user.input"], status: { sessionId: "root-a", provider: "echo", busy: false }, agents: [{ name: "build" }], skills: [] });
      if (url.includes("/v1/roots") && method === "GET") return response({ roots: [{ id: "root-a", title: "Alpha", agent: "build", busy: false }, { id: "root-b", title: "Beta", agent: "build", busy: false }], activeId: "root-a" });
      if (url.includes("/activate")) return response({ ok: true });
      if (url.includes("sessions")) return response({ sessions: [{ id: "root-a", title: "Alpha" }, { id: "root-b", title: "Beta" }], liveId: "root-a" });
      return response({ ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<App />);
    expect(await screen.findByRole("button", { name: "ACTIVE" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "HISTORY" })).toBeInTheDocument();
    const alpha = await screen.findByRole("button", { name: /Alpha/i });
    expect(alpha).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Instruction"), { target: { value: "draft-a" } });
    fireEvent.click(screen.getByRole("button", { name: /Beta/i }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u]) => String(u).includes("/v1/roots/root-b/activate"))).toBe(true));
    expect(screen.getByLabelText("Instruction")).toHaveValue("");
    fireEvent.change(screen.getByLabelText("Instruction"), { target: { value: "draft-b" } });
    fireEvent.click(screen.getByRole("button", { name: /Alpha/i }));
    await waitFor(() => expect(screen.getByLabelText("Instruction")).toHaveValue("draft-a"));
  });

  it("surfaces background permission attention on the rail and header within the poll window", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let roots = [
      { id: "root-a", title: "Alpha", agent: "build", busy: false, permissionPending: false },
      { id: "root-b", title: "Beta", agent: "build", busy: false, permissionPending: false },
    ];
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method || "GET").toUpperCase();
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: false, capabilities: { live: true, roots: true, sessions: true }, protocolOps: ["user.input"], status: { sessionId: "root-a", provider: "echo", busy: false }, agents: [{ name: "build" }], skills: [] });
      if (url.includes("/v1/roots") && method === "GET") return response({ roots, activeId: "root-a" });
      if (url.includes("/activate")) return response({ ok: true });
      if (url.includes("sessions")) return response({ sessions: [{ id: "root-a", title: "Alpha" }, { id: "root-b", title: "Beta" }], liveId: "root-a" });
      return response({ ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<App />);
    await screen.findByText("Alpha");
    roots = [
      { id: "root-a", title: "Alpha", agent: "build", busy: false, permissionPending: false },
      { id: "root-b", title: "Beta", agent: "build", busy: true, permissionPending: true },
    ];
    await vi.advanceTimersByTimeAsync(2100);
    expect(await screen.findByText("NEEDS YOU")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /1 needs you/i }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u]) => String(u).includes("/v1/roots/root-b/activate"))).toBe(true));
    vi.useRealTimers();
  });

  it("keeps attach-only single-session fallback without multi-root chrome", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({ version: "test", authRequired: false, attachOnly: true, capabilities: { live: false, roots: false, sessions: true }, protocolOps: null, agents: [], skills: [] });
      if (url.includes("sessions")) return response({ sessions: [{ id: "saved", title: "Saved" }] });
      if (url.includes("roots")) return Promise.resolve(new Response("multi-root unavailable", { status: 503 }));
      return response({ ok: true });
    }));
    render(<App />);
    await screen.findByText("Saved");
    expect(screen.queryByRole("button", { name: "ACTIVE" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "+ New workspace" })).not.toBeInTheDocument();
    expect(screen.queryByText("NEEDS YOU")).not.toBeInTheDocument();
  });

  it("switches workspace modes via the mode shell without dropping the session", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({
        version: "test", authRequired: false, attachOnly: false,
        capabilities: { live: true, files: true, plans: true, memory: true, issues: true, goals: true, mcp: true, timeline: true },
        protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false },
        agents: [{ name: "build" }], skills: [],
      });
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("changed-files")) return response({ files: [] });
      if (url.includes("plans")) return response({ plans: [] });
      return response({ ok: true });
    }));
    render(<App />);
    await screen.findByText("Current");
    expect(screen.getByRole("button", { name: /Chat:/ })).toHaveAttribute("aria-pressed", "true");
    fireEvent.change(screen.getByLabelText("Instruction"), { target: { value: "keep me" } });
    fireEvent.click(screen.getByRole("button", { name: /Project:/ }));
    expect(screen.getByRole("button", { name: /Project:/ })).toHaveAttribute("aria-pressed", "true");
    expect(await screen.findByRole("tab", { name: "plans" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "mcp" })).not.toBeInTheDocument();
    // Draft preserved across mode switch (per-root composer state).
    expect(screen.getByLabelText("Instruction")).toHaveValue("keep me");
    fireEvent.click(screen.getByRole("button", { name: /Chat:/ }));
    expect(screen.getByRole("button", { name: /Chat:/ })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByLabelText("Instruction")).toHaveValue("keep me");
  });


  it("exposes shell profile and moves modes to the bottom bar on phone widths", async () => {
    const prev = window.innerWidth;
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 390 });
    window.dispatchEvent(new Event("resize"));
    render(<App />);
    await screen.findByText("Current");
    const shell = document.querySelector(".app-shell");
    expect(shell).toHaveAttribute("data-shell", "phone");
    expect(screen.getByRole("navigation", { name: "Workspace mode" }).className).toContain("mode-bottom-bar");
    // Header should not duplicate the mode switch on phone.
    expect(document.querySelector(".header-mode-switch")).toBeNull();
    Object.defineProperty(window, "innerWidth", { configurable: true, value: prev || 1280 });
    window.dispatchEvent(new Event("resize"));
  });

  it("restores mode and surface from deep link after bootstrap", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1280 });
    window.dispatchEvent(new Event("resize"));
    window.history.replaceState(null, "", "/?mode=project&surface=plans&entity=plan-1");
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({
        version: "test", authRequired: false, attachOnly: false,
        capabilities: { live: true, plans: true, files: true },
        protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false },
        agents: [], skills: [],
      });
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("plans")) return response({ plans: [] });
      return response({ ok: true });
    }));
    render(<App />);
    await screen.findByText("Current");
    // Deep-link apply is async after bootstrap; wait for mode + surface.
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Project:/ })).toHaveAttribute("aria-pressed", "true");
    });
    await waitFor(() => {
      const plansTab = screen.queryByRole("tab", { name: "plans" });
      if (plansTab) {
        expect(plansTab).toHaveAttribute("aria-selected", "true");
      } else {
        expect(screen.getByRole("option", { name: /plans/i })).toHaveAttribute("aria-selected", "true");
      }
    });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Toggle inspector" })).toHaveAttribute("aria-pressed", "true");
    });
  });

  it("keeps Ops settings in the inspector instead of opening the workspace dialog", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({
        version: "test", authRequired: false, attachOnly: false,
        capabilities: { live: true, settings: true, mcp: true, auth: true, plugins: true },
        protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false },
        agents: [], skills: [],
      });
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("/v1/mcp")) return response({ servers: [] });
      return response({ ok: true });
    }));
    render(<App />);
    await screen.findByText("Current");
    fireEvent.click(screen.getByRole("button", { name: /Ops:/ }));
    expect(screen.queryByRole("dialog", { name: "Workspace settings" })).not.toBeInTheDocument();
    expect(await screen.findByRole("tab", { name: "settings" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("heading", { name: "Settings" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "mcp" }));
    expect(await screen.findByRole("heading", { name: "MCP servers" })).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: "Workspace settings" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open settings" }));
    expect(screen.getByRole("dialog", { name: "Workspace settings" })).toBeInTheDocument();
  });

  it("ranks Open MCP first in the palette for query mcp and Enter opens MCP", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("bootstrap")) return response({
        version: "test", authRequired: false, attachOnly: false,
        capabilities: { live: true, settings: true, mcp: true },
        protocolOps: ["user.input"], status: { sessionId: "live", provider: "echo", busy: false },
        agents: [], skills: [],
      });
      if (url.includes("sessions")) return response({ sessions: [{ id: "live", title: "Current" }], liveId: "live" });
      if (url.includes("/v1/mcp")) return response({ servers: [] });
      return response({ ok: true });
    }));
    render(<App />);
    await screen.findByText("Current");
    fireEvent.keyDown(window, { key: "k", metaKey: true });
    const filter = await screen.findByLabelText("Filter commands");
    fireEvent.change(filter, { target: { value: "mcp" } });
    const options = within(screen.getByRole("listbox", { name: "Commands" })).getAllByRole("option");
    expect(options[0]).toHaveTextContent("Open MCP");
    fireEvent.keyDown(filter, { key: "Enter" });
    expect(screen.getByRole("button", { name: /Ops:/ })).toHaveAttribute("aria-pressed", "true");
    expect(await screen.findByRole("tab", { name: "mcp" })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByRole("dialog", { name: "Workspace settings" })).not.toBeInTheDocument();
  });


});

describe("formatCostLabel / formatContextLabel", () => {
  it("returns graceful empty states", () => {
    expect(formatCostLabel({})).toBe("not reported");
    expect(formatContextLabel({})).toBe("not reported");
  });

  it("formats tokens and optional catalog cost", () => {
    expect(formatCostLabel({ inputTokens: 1000, outputTokens: 500 })).toBe("in 1,000 · out 500");
    expect(formatCostLabel(
      { inputTokens: 1_000_000, outputTokens: 1_000_000 },
      { inputPerM: 1, outputPerM: 2, hasCost: true },
    )).toBe("$3 · in 1,000,000 · out 1,000,000");
    expect(formatContextLabel({ contextUsed: 100, contextLimit: 200 })).toBe("100 / 200");
    expect(formatContextLabel({ contextUsed: 50 })).toBe("50 used");
  });
});
