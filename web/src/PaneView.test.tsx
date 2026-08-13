import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { PaneView } from "./PaneView";
import { resolveFrom } from "./panesApi";

const css = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), "styles.css"), "utf8");

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

  it("wraps row children in equal-width pane-row-cell", () => {
    const { container } = render(
      <PaneView
        node={{
          type: "row",
          children: [
            { type: "text", text: "Left" },
            { type: "text", text: "Right" },
          ],
        }}
      />,
    );
    const row = container.querySelector(".pane-row") as HTMLElement | null;
    expect(row).toBeTruthy();
    expect(row).not.toHaveClass("wrap");
    const cells = container.querySelectorAll(".pane-row > .pane-row-cell");
    expect(cells).toHaveLength(2);
    expect(row?.style.getPropertyValue("--stack-at")).toBe("160px");
    expect(css).toMatch(/\.pane-row-cell\s*\{[^}]*flex-grow:\s*1/);
    expect(css).toMatch(/\.pane-row-cell\s*\{[^}]*flex-basis:\s*calc\(\(var\(--stack-at/);
  });

  it("stacks wrap:true rows as a column", () => {
    const { container } = render(
      <PaneView
        node={{
          type: "row",
          wrap: true,
          children: [
            { type: "text", text: "A" },
            { type: "text", text: "B" },
          ],
        }}
      />,
    );
    const row = container.querySelector(".pane-row");
    expect(row).toHaveClass("wrap");
    expect(row).toHaveClass("pane-row");
    expect(container.querySelectorAll(".pane-row > .pane-row-cell")).toHaveLength(2);
    expect(css).toMatch(/\.pane-row\.wrap\s*\{[^}]*flex-direction:\s*column/);
    expect(css).toMatch(/\.pane-row\.wrap\s+\.pane-row-cell\s*\{[^}]*width:\s*100%/);
  });

  it("honors flex and min on row children", () => {
    const { container } = render(
      <PaneView
        node={{
          type: "row",
          children: [
            { type: "text", text: "Grow", flex: 2, min: 12 },
            { type: "text", text: "Base" },
          ],
        }}
      />,
    );
    const cells = container.querySelectorAll(".pane-row-cell");
    expect(cells[0]).toHaveStyle({ flexGrow: "2", minWidth: "12ch" });
    expect((cells[1] as HTMLElement).style.flexGrow).toBe("");
  });

  it("ellipsizes truncated text instead of overflowing", () => {
    render(
      <PaneView
        node={{
          type: "text",
          text: "a very long label that should not overflow the cell",
          truncate: "end",
        }}
      />,
    );
    expect(screen.getByText(/very long label/)).toHaveStyle({
      overflow: "hidden",
      textOverflow: "ellipsis",
      whiteSpace: "nowrap",
    });
  });

  it("renders list item icons from the closed set and omits unknown names", () => {
    render(
      <PaneView
        node={{
          type: "list",
          items: [
            { id: "ok", label: "Ready", icon: "check" },
            { id: "x", label: "Other", icon: "not-an-icon" },
          ],
        }}
      />,
    );
    expect(screen.getByText(/Ready/).closest("li")?.textContent).toMatch(/✓/);
    const labelGroup = screen.getByText("Ready").parentElement;
    expect(labelGroup?.textContent).toMatch(/✓/);
    expect(labelGroup).not.toBe(screen.getByText(/Ready/).closest("li"));
    expect(screen.getByText(/Other/).closest("li")?.textContent).not.toMatch(/not-an-icon/);
  });
});
