import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { GoalsPanel } from "./Goals";
import { canAbort, canPause, canResume, canRun } from "./goals";

const response = (body: unknown, status = 200) =>
  Promise.resolve(new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));

const sampleGoal = {
  id: "g001",
  description: "pass check",
  criteria: [{ description: "cmd: true", check: "cmd: true", satisfied: false }],
  status: "pending",
  maxIterations: 5,
  maxCostUsd: 1,
  costUsd: 0,
  lastIteration: 0,
};

describe("goal control helpers", () => {
  it("gates run/pause/resume/abort by status", () => {
    expect(canRun("pending")).toBe(true);
    expect(canRun("done")).toBe(false);
    expect(canPause("active")).toBe(true);
    expect(canPause("pending")).toBe(false);
    expect(canResume("paused")).toBe(true);
    expect(canResume("active")).toBe(false);
    expect(canAbort("pending")).toBe(true);
    expect(canAbort("aborted")).toBe(false);
  });
});

describe("GoalsPanel", () => {
  beforeEach(() => {
    let goal: typeof sampleGoal & { failReason?: string } = {
      ...sampleGoal,
      criteria: sampleGoal.criteria.map((c) => ({ ...c })),
    };
    const logs: Array<{ n: number; costUsd: number; summary: string }> = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method || "GET").toUpperCase();
      if (url.endsWith("/v1/goals") && method === "GET") return response({ goals: [goal] });
      if (url.endsWith("/v1/goals") && method === "POST") {
        const body = JSON.parse(String(init?.body || "{}"));
        goal = {
          id: "g002",
          description: body.description,
          criteria: (body.criteria || []).map((c: string) => ({ description: c, check: c, satisfied: false })),
          status: "pending",
          maxIterations: body.maxIterations || 25,
          maxCostUsd: 0,
          costUsd: 0,
          lastIteration: 0,
        };
        return response(goal, 201);
      }
      if (url.includes("/v1/goals/g001/log") || url.includes("/v1/goals/g002/log")) return response({ iterations: logs });
      if (url.match(/\/v1\/goals\/g00[12]$/) && method === "GET") return response(goal);
      if (url.includes("/run") && method === "POST") {
        goal = { ...goal, status: "done", lastIteration: 1, criteria: goal.criteria.map((c) => ({ ...c, satisfied: true })) };
        logs.push({ n: 1, costUsd: 0, summary: "iter 1 [OK:cmd: true]" });
        return response(goal);
      }
      if (url.includes("/resume") && method === "POST") {
        goal = { ...goal, status: "active" };
        return response(goal);
      }
      if (url.includes("/pause") && method === "POST") {
        goal = { ...goal, status: "paused" };
        return response(goal);
      }
      if (url.includes("/abort") && method === "POST") {
        goal = { ...goal, status: "aborted", failReason: "aborted by user" };
        return response(goal);
      }
      return response({ error: `unhandled ${method} ${url}` }, 500);
    }));
  });
  afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

  it("shows unavailable when capability is absent", () => {
    render(<GoalsPanel available={false} live={false} />);
    expect(screen.getByRole("status")).toHaveTextContent("Goals unavailable");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("lists goals and disables controls when not live", async () => {
    render(<GoalsPanel available live={false} />);
    expect(await screen.findByText("pass check")).toBeInTheDocument();
    expect(screen.getByText(/Attach-only/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Pause" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Resume" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Abort" })).toBeDisabled();
  });

  it("runs a goal when live and shows iteration log", async () => {
    render(<GoalsPanel available live />);
    await screen.findByText("pass check");
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/v1/goals/g001/run"),
      expect.objectContaining({ method: "POST" }),
    ));
    await waitFor(() => expect(screen.getByText(/run: done/)).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "Details" }));
    expect(await screen.findByRole("dialog", { name: "pass check" })).toBeInTheDocument();
    expect(await screen.findByText("iter 1 [OK:cmd: true]")).toBeInTheDocument();
  });

  it("creates a pending goal via the new dialog", async () => {
    render(<GoalsPanel available live />);
    await screen.findByText("pass check");
    fireEvent.click(screen.getByRole("button", { name: "New" }));
    expect(await screen.findByRole("dialog", { name: "New goal" })).toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText("What should succeed?"), { target: { value: "ship green" } });
    fireEvent.change(screen.getByLabelText(/Criteria/), { target: { value: "cmd: true" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/v1/goals"),
      expect.objectContaining({ method: "POST" }),
    ));
    const createCall = vi.mocked(fetch).mock.calls.find((c) => String(c[0]).endsWith("/v1/goals") && (c[1] as RequestInit | undefined)?.method === "POST");
    expect(createCall).toBeTruthy();
    const body = JSON.parse(String((createCall?.[1] as RequestInit).body));
    expect(body.description).toBe("ship green");
    expect(body.criteria).toEqual(["cmd: true"]);
  });
});
