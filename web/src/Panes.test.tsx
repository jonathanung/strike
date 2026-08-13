import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PanesPanel } from "./Panes";

const response = (body: unknown, status = 200) =>
  Promise.resolve(
    new Response(status === 204 ? null : JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );

const weather = {
  id: "weather",
  pluginId: "acme.weather",
  title: "Weather",
  mode: "static",
  trusted: true,
  provenance: "plugin=acme.weather pane=weather",
};

const board = {
  id: "board",
  pluginId: "acme.board",
  title: "Board",
  mode: "process",
  trusted: true,
  provenance: "plugin=acme.board pane=board",
};

describe("PanesPanel", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = (init?.method || "GET").toUpperCase();
        if (url.endsWith("/v1/panes") && method === "GET") return response({ panes: [weather, board] });
        if (url.includes("/v1/panes/weather/mount") || url.includes("/v1/panes/weather/snapshot")) {
          return response({
            id: "weather",
            title: "Weather",
            mode: "static",
            mounted: true,
            view: { type: "text", text: "72° clear", style: "title" },
          });
        }
        if (url.includes("/v1/panes/board/mount") || url.includes("/v1/panes/board/snapshot")) {
          return response({
            id: "board",
            title: "Board",
            mode: "process",
            mounted: true,
            view: { type: "text", text: "board view", style: "title" },
          });
        }
        if (url.includes("/input")) return response({ ok: true });
        if (url.includes("/unmount")) return response({ ok: true });
        return response({ error: "not found" }, 404);
      }),
    );
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows catalog empty-state when no panes are enabled", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        if (String(input).endsWith("/v1/panes")) return response({ panes: [] });
        return response({ ok: true });
      }),
    );
    render(<PanesPanel available />);
    expect(await screen.findByText(/No enabled plugin panes/)).toBeInTheDocument();
  });

  it("opens a focused pane without showing the catalog empty-state", async () => {
    render(<PanesPanel available focusId="weather" />);
    expect(await screen.findByText("72° clear")).toBeInTheDocument();
    expect(screen.queryByText(/No enabled plugin panes/)).not.toBeInTheDocument();
  });

  it("blocks process-pane input when read-only", async () => {
    render(<PanesPanel available focusId="board" readOnly />);
    expect(await screen.findByText("board view")).toBeInTheDocument();
    expect(screen.getByText("read-only")).toBeInTheDocument();
    fireEvent.keyDown(screen.getByLabelText("Board"), { key: "a" });
    const inputCalls = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls.filter((c) =>
      String(c[0]).includes("/input"),
    );
    expect(inputCalls).toHaveLength(0);
  });

  it("sends process-pane input when live", async () => {
    render(<PanesPanel available focusId="board" />);
    expect(await screen.findByText("board view")).toBeInTheDocument();
    fireEvent.keyDown(screen.getByLabelText("Board"), { key: "a" });
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        expect.stringContaining("/v1/panes/board/input"),
        expect.objectContaining({ method: "POST" }),
      ),
    );
  });

  it("shows unavailable when capability missing", () => {
    render(<PanesPanel available={false} />);
    expect(screen.getByText(/Plugin panes unavailable/)).toBeInTheDocument();
  });
});
