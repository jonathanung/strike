import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DiagnosticsPanel } from "./Diagnostics";

const response = (body: unknown, status = 200) =>
  Promise.resolve(new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  }));

describe("DiagnosticsPanel", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows unavailable when capability is off", () => {
    render(<DiagnosticsPanel available={false} />);
    expect(screen.getByRole("status")).toHaveTextContent("Diagnostics unavailable");
  });

  it("renders servers and findings with severity/path/message", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/v1/lsp") && !url.includes("retry") && !url.includes("disable")) {
        return response({
          servers: [
            { name: "gopls", command: "gopls", state: "up", extensions: [".go"], openDocs: 1 },
            { name: "tsserver", command: "tsserver", state: "error", error: "exit 1" },
          ],
        });
      }
      if (url.includes("/v1/diagnostics")) {
        return response({
          diagnostics: [
            { path: "main.go", line: 10, character: 2, severity: "error", source: "compiler", code: "E1", message: "undefined: x" },
            { path: "main.go", line: 20, character: 1, severity: "warning", message: "unused" },
          ],
          count: 2,
        });
      }
      return response({ ok: true });
    }));

    render(<DiagnosticsPanel available />);
    expect(await screen.findByRole("heading", { level: 3, name: "gopls" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 3, name: "tsserver" })).toBeInTheDocument();
    expect(screen.getByText("up")).toBeInTheDocument();
    expect(screen.getByText("exit 1")).toBeInTheDocument();
    expect(screen.getByText("undefined: x")).toBeInTheDocument();
    expect(screen.getByText("main.go:10:2")).toBeInTheDocument();
    expect(screen.getByText("unused")).toBeInTheDocument();
    // severity badges (server state "error" + finding severity "error")
    expect(screen.getAllByText("error").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("warning")).toBeInTheDocument();
  });

  it("shows soft empty note when no servers are configured", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/v1/lsp")) {
        return response({ servers: [], note: "no language servers configured (add lsp.servers in config)" });
      }
      if (url.includes("/v1/diagnostics")) {
        return response({ diagnostics: [], count: 0, note: "no language servers configured (add lsp.servers in config)" });
      }
      return response({ ok: true });
    }));
    render(<DiagnosticsPanel available />);
    expect(await screen.findAllByText(/no language servers configured/)).not.toHaveLength(0);
  });

  it("retries a server and refreshes", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/v1/lsp/retry") && init?.method === "POST") {
        return response({ ok: true });
      }
      if (url.includes("/v1/lsp") && !url.includes("retry") && !url.includes("disable")) {
        return response({ servers: [{ name: "gopls", state: "down", command: "gopls" }] });
      }
      if (url.includes("/v1/diagnostics")) {
        return response({ diagnostics: [], count: 0, note: "no live language servers (see servers status; try retry)" });
      }
      return response({ ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<DiagnosticsPanel available />);
    expect(await screen.findByRole("heading", { level: 3, name: "gopls" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/v1/lsp/retry"),
        expect.objectContaining({ method: "POST", body: JSON.stringify({ name: "gopls" }) }),
      );
    });
  });
});
