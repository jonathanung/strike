import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TimelinePanel } from "./Timeline";

const sampleTrace = {
  schemaVersion: "1.0.0",
  sessionId: "live",
  redacted: true,
  summary: { turns: 1, tools: 1, providers: 0, children: 0, failed: 0, canceled: 0, durationMs: 42 },
  entries: [
    { id: "turn-1", kind: "turn", state: "completed", turnId: "t1", durationMs: 40 },
    { id: "tool-1", kind: "tool", state: "completed", name: "bash", callId: "c1", durationMs: 12 },
  ],
};

describe("TimelinePanel", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn());
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows unavailable when capability is off", () => {
    render(<TimelinePanel available={false} sessionID="live" />);
    expect(screen.getByRole("status")).toHaveTextContent("Timeline unavailable");
  });

  it("loads collapsed entries and triggers export downloads", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/timeline/export")) {
        return Promise.resolve(
          new Response(JSON.stringify(sampleTrace), {
            status: 200,
            headers: {
              "Content-Type": "application/json",
              "Content-Disposition": 'attachment; filename="strike-timeline-live.json"',
            },
          }),
        );
      }
      if (url.includes("/timeline")) {
        return Promise.resolve(new Response(JSON.stringify(sampleTrace), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchMock);
    vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:test");
    vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});

    render(<TimelinePanel available sessionID="live" />);
    fireEvent.click(screen.getByRole("button", { name: "Load" }));
    expect(await screen.findByLabelText("Timeline entries")).toBeInTheDocument();
    expect(screen.getByText("turn")).toBeInTheDocument();
    expect(screen.getByText("tool:bash")).toBeInTheDocument();
    expect(screen.getByLabelText("Timeline summary")).toHaveTextContent("Turns");
    expect(screen.getByText("yes")).toBeInTheDocument(); // redacted

    fireEvent.click(screen.getByRole("button", { name: "Export JSON" }));
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/v1/sessions/live/timeline/export?format=json"),
        expect.anything(),
      ),
    );
  });

  it("surfaces load errors", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response(JSON.stringify({ error: "session not found" }), { status: 404, headers: { "Content-Type": "application/json" } }))),
    );
    render(<TimelinePanel available sessionID="missing" />);
    fireEvent.click(screen.getByRole("button", { name: "Load" }));
    expect(await screen.findByRole("status")).toHaveTextContent("session not found");
  });
});
