import { cleanup, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ArtifactsPanel, LedgerPanel, TeamReviewPanel, verificationLabel } from "./ArtifactsReview";
import { emptyTeam } from "./teamModel";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("verificationLabel", () => {
  it("distinguishes verified, failed, claimed, unknown", () => {
    expect(verificationLabel({ passed: true }).label).toBe("verified");
    expect(verificationLabel({ passed: false }).label).toBe("failed");
    expect(verificationLabel({ claimed: true, verified: false }).label).toMatch(/claimed/);
    expect(verificationLabel(undefined).label).toBe("unknown");
  });
});

describe("ArtifactsPanel", () => {
  it("lists artifacts and opens detail", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : (input as Request).url || String(input);
        if (/\/v1\/artifacts\/art1/.test(url)) {
          return {
            ok: true,
            status: 200,
            json: async () => ({
              id: "art1", type: "findings", title: "Bug list", version: 2, content: "line a",
            }),
          };
        }
        if (url.includes("/v1/artifacts")) {
          return {
            ok: true,
            status: 200,
            json: async () => ({
              artifacts: [{ id: "art1", type: "findings", title: "Bug list", version: 1, scope: "team" }],
            }),
          };
        }
        return { ok: false, status: 404, json: async () => ({ error: "nope" }) };
      }),
    );
    render(<ArtifactsPanel available rootID="root-1" />);
    await waitFor(() => expect(screen.getByText("Bug list")).toBeInTheDocument());
    fireEvent.click(screen.getByText("Bug list"));
    await waitFor(() => expect(screen.getByLabelText("Artifact detail")).toBeInTheDocument());
    expect(screen.getByText("line a")).toBeInTheDocument();
  });

  it("shows unavailable", () => {
    render(<ArtifactsPanel available={false} />);
    expect(screen.getByText(/unavailable/i)).toBeInTheDocument();
  });
});

describe("LedgerPanel", () => {
  it("renders active entries with status", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: true,
        status: 200,
        json: async () => ({
          entries: [
            { id: "L1", kind: "decision", statement: "Ship it", status: "active" },
            { id: "L2", kind: "assumption", statement: "Old", status: "invalidated", invalidateReason: "wrong" },
          ],
        }),
      })),
    );
    render(<LedgerPanel available rootID="r1" />);
    await waitFor(() => expect(screen.getByText("Ship it")).toBeInTheDocument());
    expect(screen.getByText("Old")).toBeInTheDocument();
    expect(screen.getAllByText(/invalidated/i).length).toBeGreaterThan(0);
  });
});

describe("TeamReviewPanel", () => {
  it("shows handoffs and path overlaps", () => {
    const team = emptyTeam();
    team.members = {
      c1: {
        sessionId: "c1", name: "Builder", state: "completed", terminal: true,
        terminalSummary: "done work", filesTouched: ["a.go"],
      },
    };
    team.verifications = [{ sessionId: "c1", passed: true, summary: "tests green" }];
    team.pathOverlaps = [{ path: "a.go", sessions: ["c1", "c2"] }];
    render(<TeamReviewPanel team={team} />);
    expect(screen.getByText("done work")).toBeInTheDocument();
    expect(screen.getByText(/verified/i)).toBeInTheDocument();
    expect(screen.getAllByText("a.go").length).toBeGreaterThan(0);
  });
});
