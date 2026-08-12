import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { PaneView } from "./PaneView";
import { resolveFrom } from "./panesApi";

afterEach(() => cleanup());

describe("resolveFrom", () => {
  it("resolves dotted feed fields", () => {
    const feeds = {
      "session.summary": { cwd: "/work", model: "echo" },
      usage: { used: 10, limit: 100 },
    };
    expect(resolveFrom(feeds, "session.summary.cwd")).toBe("/work");
    expect(resolveFrom(feeds, "usage.used")).toBe("10");
    expect(resolveFrom(feeds, "missing.x")).toBe("");
  });
});

describe("PaneView", () => {
  it("renders static column/kv/meter with feeds", () => {
    render(
      <PaneView
        feeds={{ "session.summary": { cwd: "/proj" }, usage: { used: 5, limit: 10 } }}
        node={{
          type: "column",
          gap: 1,
          children: [
            { type: "text", text: "Session", style: "title" },
            { type: "kv", entries: [{ key: "cwd", valueFrom: "session.summary.cwd" }] },
            { type: "meter", label: "ctx", valueFrom: "usage.used", maxFrom: "usage.limit" },
          ],
        }}
      />,
    );
    expect(screen.getByText("Session")).toBeInTheDocument();
    expect(screen.getByText("/proj")).toBeInTheDocument();
  });

  it("shows unsupported placeholder for unknown nodes", () => {
    render(
      <PaneView
        node={{
          type: "column",
          children: [{ type: "nope" }, { type: "text", text: "hi" }],
        }}
      />,
    );
    expect(screen.getByText(/unsupported/i)).toBeInTheDocument();
    expect(screen.getByText("hi")).toBeInTheDocument();
  });
});
