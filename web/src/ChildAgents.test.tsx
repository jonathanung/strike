import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChildAgentsPanel, ChildDetail } from "./ChildAgents";
import type { ChildAgent } from "./types";

afterEach(() => cleanup());

const sample: Record<string, ChildAgent> = {
  "child-1": {
    agent: "explore",
    name: "scout",
    status: "completed",
    summary: "Found the bug in parser.go",
    quality: "complete",
    budgetKind: "tokens",
    finalization: "succeeded",
  },
  "child-2": {
    agent: "build",
    status: "running",
  },
};

describe("ChildAgentsPanel", () => {
  it("lists status and quality when present", () => {
    render(
      <ChildAgentsPanel
        children={Object.entries(sample)}
        onSelect={() => {}}
      />,
    );
    expect(screen.getByLabelText("Child scout")).toHaveTextContent("completed");
    expect(screen.getByLabelText("Child scout")).toHaveTextContent("complete");
    expect(screen.getByLabelText("Child scout").querySelector(".child-state")).toHaveClass("completed");
    expect(screen.getByLabelText("Child scout").querySelector(".child-state")).toHaveAttribute("data-status", "complete");
    expect(screen.getByLabelText("Child build")).toHaveTextContent("running");
    expect(screen.getByLabelText("Child build").querySelector(".child-state")).toHaveClass("running");
    expect(screen.getByLabelText("Child build").querySelector(".child-state")).toHaveAttribute("data-status", "busy");
    expect(screen.getByLabelText("Child build").textContent).not.toMatch(/partial|unavailable|complete/);
  });

  it("shows handoff summary and budget stop reason in the detail drawer", () => {
    const onSelect = vi.fn();
    render(
      <ChildAgentsPanel
        children={Object.entries(sample)}
        selectedId="child-1"
        onSelect={onSelect}
        onOpenTranscript={vi.fn()}
      />,
    );
    const detail = screen.getByLabelText("Child handoff detail");
    expect(detail).toHaveTextContent("Found the bug in parser.go");
    expect(detail).toHaveTextContent("complete");
    expect(detail).toHaveTextContent("tokens");
    expect(detail).toHaveTextContent("finalization succeeded");
    expect(detail).toHaveTextContent("#523");
    fireEvent.click(screen.getByRole("button", { name: "Open transcript (RO)" }));
  });

  it("toggles selection when a child row is clicked", () => {
    const onSelect = vi.fn();
    render(
      <ChildAgentsPanel
        children={Object.entries(sample)}
        selectedId={undefined}
        onSelect={onSelect}
      />,
    );
    fireEvent.click(screen.getByLabelText("Child scout"));
    expect(onSelect).toHaveBeenCalledWith("child-1");
  });
});

describe("ChildDetail", () => {
  it("renders escalate stop reason when budget kind is absent", () => {
    render(
      <ChildDetail
        id="c3"
        child={{ status: "interrupted", escalateReason: "stall detected", escalateAction: "interrupted", escalateKind: "stall" }}
        onClose={() => {}}
      />,
    );
    expect(screen.getByLabelText("Child handoff detail")).toHaveTextContent("stall detected");
  });
});
