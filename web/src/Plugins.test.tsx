import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PluginsPanel } from "./Plugins";

const response = (body: unknown, status = 200) =>
  Promise.resolve(
    new Response(status === 204 ? null : JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );

const catalog = {
  plugins: [
    {
      id: "acme.pack",
      name: "Acme Pack",
      version: "1.0.0",
      scope: "global",
      enabled: true,
      status: "enabled",
      trustState: "none",
      hasExecutable: true,
      panes: 1,
      capabilities: ["panes", "mcp.stdio"],
      mcp: [{ name: "lint", command: "bin/lint", envKeys: ["TOK"] }],
    },
  ],
};

const panes = {
  panes: [
    {
      id: "acme.status",
      pluginId: "acme.pack",
      pluginVersion: "1.0.0",
      title: "Acme Status",
      mode: "static",
      trusted: true,
      provenance: "plugin=acme.pack@1.0.0 pane=acme.status mode=static",
      definition: {
        schemaVersion: 1,
        id: "acme.status",
        title: "Acme Status",
        mode: "static",
        view: { type: "text", text: "Hello pane", style: "title" },
      },
    },
  ],
};

describe("PluginsPanel", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = (init?.method || "GET").toUpperCase();
        if (url.endsWith("/v1/plugins") && method === "GET") return response(catalog);
        if (url.endsWith("/v1/panes") && method === "GET") return response(panes);
        if (url.includes("/v1/plugins/disable")) return response({ ok: true });
        if (url.includes("/v1/plugins/enable")) return response({ ok: true });
        if (url.includes("/v1/plugins/trust-preview")) {
          return response({
            id: "acme.pack",
            digest: "sha256:abc",
            reviewLines: ["Grant executable trust for acme.pack?", "mcp: bin/lint"],
          });
        }
        if (url.includes("/v1/plugins/trust") && method === "POST") return response({ ok: true });
        if (url.includes("/v1/plugins/remove")) return response({ ok: true });
        if (url.includes("/v1/panes/acme.status/mount")) {
          return response({
            id: "acme.status",
            title: "Acme Status",
            mode: "static",
            mounted: true,
            view: { type: "text", text: "Hello pane", style: "title" },
            feeds: { "session.summary": { model: "echo" } },
          });
        }
        if (url.includes("/unmount")) return response({ ok: true });
        return response({ error: "not found" }, 404);
      }),
    );
    vi.stubGlobal("confirm", vi.fn(() => true));
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("lists plugins with trust and mcp env keys only", async () => {
    render(<PluginsPanel available panesAvailable />);
    expect(await screen.findByText("Acme Pack")).toBeInTheDocument();
    expect(screen.getByText("none")).toBeInTheDocument();
    expect(screen.getByText(/env: TOK/)).toBeInTheDocument();
    expect(screen.queryByText("supersecret")).not.toBeInTheDocument();
  });

  it("disables a plugin after confirm path", async () => {
    render(<PluginsPanel available />);
    await screen.findByText("Acme Pack");
    fireEvent.click(screen.getByRole("button", { name: "Disable" }));
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        expect.stringContaining("/v1/plugins/disable"),
        expect.objectContaining({ method: "POST", body: expect.stringContaining("acme.pack") }),
      ),
    );
    expect(await screen.findByText(/Disabled acme.pack/)).toBeInTheDocument();
  });

  it("shows trust review before granting trust", async () => {
    render(<PluginsPanel available />);
    await screen.findByText("Acme Pack");
    fireEvent.click(screen.getByRole("button", { name: "Trust…" }));
    expect(await screen.findByText(/Trust review/)).toBeInTheDocument();
    expect(screen.getByText(/mcp: bin\/lint/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Grant trust" }));
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        expect.stringContaining("/v1/plugins/trust"),
        expect.objectContaining({ method: "POST" }),
      ),
    );
  });

  it("mounts a static pane and renders view tree", async () => {
    render(<PluginsPanel available panesAvailable />);
    await screen.findByText("Acme Pack");
    fireEvent.click(screen.getByRole("button", { name: "Acme Status" }));
    expect(await screen.findByText("Hello pane")).toBeInTheDocument();
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        expect.stringContaining("/v1/panes/acme.status/mount"),
        expect.objectContaining({ method: "POST" }),
      ),
    );
  });

  it("shows unavailable when capability missing", () => {
    render(<PluginsPanel available={false} />);
    expect(screen.getByText(/Plugins unavailable/)).toBeInTheDocument();
  });
});
