import { cleanup, render, screen, fireEvent } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { emptyTeam } from "./team";
import { orderedMembers, teamAttentionItems, TeamWorkspace } from "./Team";

afterEach(() => cleanup());

const sampleTeam = () => {
  const team = emptyTeam();
  team.leadId = "lead-1";
  team.members = {
    "lead-1": { sessionId: "lead-1", name: "Lead", state: "working", role: "lead", lastAction: "coordinating" },
    "c1": { sessionId: "c1", name: "Builder", state: "working", lastAction: "editing", objective: "Implement X" },
    "c2": { sessionId: "c2", name: "Reviewer", state: "blocked", blockReason: "waiting on tests", terminal: false },
    "c3": { sessionId: "c3", name: "Done", state: "completed", terminal: true, terminalSummary: "shipped" },
  };
  team.delegations = {
    d1: { id: "d1", state: "working", name: "Task A", ownerSessionId: "c1", version: 2 },
    d2: { id: "d2", state: "blocked", name: "Task B", ownerSessionId: "c2", reason: "dep" },
  };
  team.verifications = [{ sessionId: "c2", passed: false, summary: "tests red" }];
  team.pathOverlaps = [{ path: "src/a.go", sessions: ["c1", "c2"] }];
  team.messages = [{ from: "c2", to: "lead-1", body: "help", urgency: "blocker", kind: "escalation" }];
  return team;
};

describe("TeamWorkspace", () => {
  it("orders lead first and filters working agents", () => {
    const team = sampleTeam();
    const ordered = orderedMembers(team);
    expect(ordered[0].sessionId).toBe("lead-1");
    render(<TeamWorkspace team={team} onSelect={() => {}} />);
    expect(screen.getByLabelText("Agent roster")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Working" }));
    expect(screen.getByText(/Builder/)).toBeInTheDocument();
  });

  it("shows attention items without stealing focus", () => {
    const team = sampleTeam();
    const items = teamAttentionItems(team);
    expect(items.some((i) => i.kind === "verification")).toBe(true);
    expect(items.some((i) => i.kind === "conflict")).toBe(true);
    expect(items.some((i) => i.kind === "message")).toBe(true);
    render(<TeamWorkspace team={team} onSelect={() => {}} />);
    expect(screen.getByText(/Attention/)).toBeInTheDocument();
  });

  it("opens board columns and agent detail / transcript", () => {
    const onSelect = vi.fn();
    const onOpen = vi.fn();
    const team = sampleTeam();
    render(<TeamWorkspace team={team} selectedId="c1" onSelect={onSelect} onOpenTranscript={onOpen} />);
    fireEvent.click(screen.getByRole("tab", { name: "board" }));
    expect(screen.getByLabelText("Task board")).toBeInTheDocument();
    expect(screen.getByText("Task A")).toBeInTheDocument();
    expect(screen.getByText(/Implement X/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Open transcript/ }));
    expect(onOpen).toHaveBeenCalledWith("c1");
  });

  it("renders unavailable and read-only states", () => {
    const team = emptyTeam();
    team.available = false;
    team.unavailableReason = "Team dissolved";
    render(<TeamWorkspace team={team} onSelect={() => {}} readOnly />);
    expect(screen.getByText(/Team dissolved/)).toBeInTheDocument();
    expect(screen.getByText(/read-only/i)).toBeInTheDocument();
  });
});
