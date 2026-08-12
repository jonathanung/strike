import { cleanup, render, screen, fireEvent } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { emptyTeam } from "./teamModel";
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

  it("shows controls when team Ops are advertised and hides when attach-only", async () => {
    const team = sampleTeam();
    const sendOp = vi.fn().mockResolvedValue({ ok: true, childSessionId: "child-9" });
    const ops = [
      "team.spawn", "team.message", "team.broadcast", "team.child_interrupt",
      "team.task_transition", "team.board_create", "team.board_claim", "team.board_complete",
    ];
    const { rerender } = render(
      <TeamWorkspace
        team={team}
        onSelect={() => {}}
        protocolOps={ops}
        teamControl
        agents={["build", "explore"]}
        rootSessionId="lead-1"
        sendOp={sendOp}
      />,
    );
    fireEvent.click(screen.getByRole("tab", { name: "controls" }));
    expect(screen.getByLabelText("Team controls")).toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText(/What should the child do/), {
      target: { value: "Implement feature X" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Spawn" }));
    await vi.waitFor(() => expect(sendOp).toHaveBeenCalled());
    const [type, data] = sendOp.mock.calls[0];
    expect(type).toBe("team.spawn");
    expect(data).toMatchObject({
      objective: "Implement feature X",
      rootSessionId: "lead-1",
    });
    expect(data.idempotencyKey).toBeTruthy();

    rerender(
      <TeamWorkspace
        team={team}
        onSelect={() => {}}
        protocolOps={ops}
        teamControl
        readOnly
        sendOp={sendOp}
      />,
    );
    // read-only still shows controls tab with explanation when ops were known
    fireEvent.click(screen.getByRole("tab", { name: "controls" }));
    expect(screen.getByText(/Attach-only|disabled/i)).toBeInTheDocument();
  });

  it("confirms interrupt and sends child_interrupt", async () => {
    const team = sampleTeam();
    const sendOp = vi.fn().mockResolvedValue({ ok: true, alreadyTerminal: false });
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const ops = ["team.child_interrupt", "team.message"];
    render(
      <TeamWorkspace
        team={team}
        selectedId="c1"
        onSelect={() => {}}
        protocolOps={ops}
        teamControl
        rootSessionId="lead-1"
        sendOp={sendOp}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Interrupt" }));
    expect(confirm).toHaveBeenCalled();
    await vi.waitFor(() => expect(sendOp).toHaveBeenCalledWith(
      "team.child_interrupt",
      expect.objectContaining({ childSessionId: "c1", rootSessionId: "lead-1" }),
    ));
    confirm.mockRestore();
  });

  it("blocks duplicate in-flight spawn clicks", async () => {
    const team = sampleTeam();
    let resolve!: (v: unknown) => void;
    const sendOp = vi.fn().mockImplementation(() => new Promise((r) => { resolve = r; }));
    const ops = ["team.spawn"];
    render(
      <TeamWorkspace
        team={team}
        onSelect={() => {}}
        protocolOps={ops}
        teamControl
        rootSessionId="lead-1"
        sendOp={sendOp}
      />,
    );
    fireEvent.click(screen.getByRole("tab", { name: "controls" }));
    fireEvent.change(screen.getByPlaceholderText(/What should the child do/), {
      target: { value: "slow spawn" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Spawn" }));
    fireEvent.click(screen.getByRole("button", { name: "Spawn" }));
    await vi.waitFor(() => expect(sendOp).toHaveBeenCalledTimes(1));
    resolve({ ok: true, childSessionId: "c-new" });
    await vi.waitFor(() => expect(screen.getByText(/Spawned|OK/i)).toBeInTheDocument());
  });
});
