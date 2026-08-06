import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MCPPanel } from "./MCP";

const response = (body: unknown, status = 200) =>
  Promise.resolve(new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));

const catalog = {
  servers: [
    {
      name: "docs",
      command: "npx docs-mcp",
      transport: "stdio",
      state: "up",
      toolCount: 2,
      tools: ["mcp_docs_search", "mcp_docs_get"],
    },
    {
      name: "remote",
      command: "https://mcp.example/mcp",
      transport: "http",
      state: "error",
      toolCount: 0,
      error: "connection refused",
    },
  ],
};

describe("MCPPanel", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method || "GET").toUpperCase();
      if (url.endsWith("/v1/mcp") && method === "GET") return response(catalog);
      if (url.includes("/v1/mcp/retry")) return response({ ok: true });
      if (url.includes("/v1/mcp/disable")) return response({ ok: true });
      return response({ error: "not found" }, 404);
    }));
    vi.stubGlobal("confirm", vi.fn(() => true));
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("lists servers with status and tools", async () => {
    render(<MCPPanel available />);
    expect(await screen.findByText("docs")).toBeInTheDocument();
    expect(screen.getByText("remote")).toBeInTheDocument();
    expect(screen.getByText("up")).toBeInTheDocument();
    expect(screen.getByText("error")).toBeInTheDocument();
    expect(screen.getByText("connection refused")).toBeInTheDocument();
    expect(screen.getByText("mcp_docs_search")).toBeInTheDocument();
    expect(screen.getByLabelText("MCP servers")).toBeInTheDocument();
  });

  it("retries a named server", async () => {
    render(<MCPPanel available />);
    await screen.findByText("docs");
    fireEvent.click(screen.getAllByRole("button", { name: "Retry" })[0]);
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        expect.stringContaining("/v1/mcp/retry"),
        expect.objectContaining({ method: "POST", body: JSON.stringify({ name: "docs" }) }),
      ),
    );
    expect(await screen.findByText(/Retried docs/)).toBeInTheDocument();
  });

  it("retries all non-up servers", async () => {
    render(<MCPPanel available />);
    await screen.findByText("docs");
    fireEvent.click(screen.getByRole("button", { name: "Retry all" }));
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        expect.stringContaining("/v1/mcp/retry"),
        expect.objectContaining({ method: "POST", body: JSON.stringify({}) }),
      ),
    );
  });

  it("disables a server after confirm", async () => {
    render(<MCPPanel available />);
    await screen.findByText("docs");
    fireEvent.click(screen.getAllByRole("button", { name: "Disable" })[0]);
    expect(confirm).toHaveBeenCalled();
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        expect.stringContaining("/v1/mcp/disable"),
        expect.objectContaining({ method: "POST", body: JSON.stringify({ name: "docs" }) }),
      ),
    );
  });

  it("shows unavailable state when capability is absent", () => {
    render(<MCPPanel available={false} />);
    expect(screen.getByRole("status")).toHaveTextContent("MCP unavailable");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("shows empty configured message", async () => {
    vi.stubGlobal("fetch", vi.fn(() => response({ servers: [] })));
    render(<MCPPanel available />);
    expect(await screen.findByText(/No MCP servers configured/)).toBeInTheDocument();
  });
});
