import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Dialog } from "./Dialog";
import { Tabs } from "./Tabs";
import { StatusBadge, statusKindFrom } from "./Status";
import { CapabilityUnavailable, Notice } from "./Notice";
import { Button } from "./Button";
import { ListRow } from "./ListRow";
import { listFocusable, trapTabKey } from "./focus";

afterEach(() => cleanup());

describe("statusKindFrom", () => {
  it("maps engine states without relying on color alone", () => {
    expect(statusKindFrom("running")).toBe("busy");
    expect(statusKindFrom("completed")).toBe("complete");
    expect(statusKindFrom("failed")).toBe("failed");
    expect(statusKindFrom("blocked")).toBe("blocked");
    expect(statusKindFrom("needs-you")).toBe("needs-you");
  });
});

describe("StatusBadge", () => {
  it("exposes text label alongside status data attribute", () => {
    render(<StatusBadge kind="needs-you" />);
    const el = screen.getByText("Needs you");
    expect(el.closest("[data-status]")?.getAttribute("data-status")).toBe("needs-you");
  });
});

describe("Tabs keyboard navigation", () => {
  it("moves selection with arrow keys and sets aria attributes", () => {
    const onChange = vi.fn();
    const { rerender } = render(
      <Tabs
        label="Demo"
        value="a"
        onChange={onChange}
        items={[
          { id: "a", label: "Alpha" },
          { id: "b", label: "Beta" },
          { id: "c", label: "Gamma" },
        ]}
      />,
    );
    const list = screen.getByRole("tablist", { name: "Demo" });
    expect(list).toBeInTheDocument();
    const alpha = screen.getByRole("tab", { name: "Alpha" });
    expect(alpha).toHaveAttribute("aria-selected", "true");
    expect(alpha).toHaveAttribute("aria-controls");
    fireEvent.keyDown(alpha, { key: "ArrowRight" });
    expect(onChange).toHaveBeenCalledWith("b");

    rerender(
      <Tabs
        label="Demo"
        value="b"
        onChange={onChange}
        items={[
          { id: "a", label: "Alpha" },
          { id: "b", label: "Beta" },
          { id: "c", label: "Gamma" },
        ]}
      />,
    );
    const beta = screen.getByRole("tab", { name: "Beta" });
    fireEvent.keyDown(beta, { key: "Home" });
    expect(onChange).toHaveBeenCalledWith("a");
    fireEvent.keyDown(beta, { key: "End" });
    expect(onChange).toHaveBeenCalledWith("c");
  });
});

describe("Dialog focus", () => {
  it("opens as a labelled dialog and restores focus on unmount", async () => {
    const invoker = document.createElement("button");
    invoker.textContent = "Open";
    document.body.appendChild(invoker);
    invoker.focus();
    expect(document.activeElement).toBe(invoker);

    const onClose = vi.fn();
    const { unmount } = render(
      <Dialog title="Sample dialog" onClose={onClose} actions={<button type="button">Done</button>}>
        <p>Body</p>
        <button type="button">Inside</button>
      </Dialog>,
    );
    expect(screen.getByRole("dialog", { name: "Sample dialog" })).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toHaveAttribute("data-mode", "modal");

    unmount();
    expect(document.activeElement).toBe(invoker);
    invoker.remove();
  });

  it("blocks Escape dismiss in blocking mode", () => {
    const onClose = vi.fn();
    render(
      <Dialog title="Permission" mode="blocking" onClose={onClose}>
        <button type="button">Allow</button>
      </Dialog>,
    );
    const dialog = screen.getByRole("dialog", { name: "Permission" });
    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();
    expect(dialog).toHaveAttribute("data-mode", "blocking");
  });
});

describe("focus trap helpers", () => {
  it("cycles Tab within a container", () => {
    const root = document.createElement("div");
    const a = document.createElement("button");
    a.textContent = "A";
    const b = document.createElement("button");
    b.textContent = "B";
    root.append(a, b);
    document.body.appendChild(root);
    // offsetParent is null in jsdom for detached layout — stub via style
    Object.defineProperty(a, "offsetParent", { get: () => root });
    Object.defineProperty(b, "offsetParent", { get: () => root });

    expect(listFocusable(root)).toHaveLength(2);
    a.focus();
    const tab = new KeyboardEvent("keydown", { key: "Tab", bubbles: true });
    // from last, Tab should wrap to first
    b.focus();
    const handled = trapTabKey(root, tab);
    expect(handled).toBe(true);
    root.remove();
  });
});

describe("Notice / CapabilityUnavailable", () => {
  it("marks unavailable capability as status", () => {
    render(<CapabilityUnavailable name="Memory" />);
    expect(screen.getByRole("status")).toHaveTextContent("Memory unavailable");
    expect(screen.getByRole("status")).toHaveTextContent("No action was attempted");
  });

  it("uses alert role for errors", () => {
    render(<Notice tone="error" title="Boom">detail</Notice>);
    expect(screen.getByRole("alert")).toHaveTextContent("Boom");
  });
});

describe("Button", () => {
  it("applies variant classes from semantic tokens", () => {
    render(<Button variant="primary">Go</Button>);
    expect(screen.getByRole("button", { name: "Go" })).toHaveClass("ui-btn-primary");
  });
});

describe("ListRow", () => {
  it("lays out title and muted meta on one compact row", () => {
    render(<ListRow title="/help" meta="Show help" />);
    const row = screen.getByRole("button", { name: /\/help/ });
    expect(row).toHaveClass("ui-list-row");
    expect(row.querySelector(".ui-list-row-main")).toBeTruthy();
    expect(row.querySelector(".ui-list-row-title")).toHaveTextContent("/help");
    expect(row.querySelector(".ui-list-row-meta")).toHaveTextContent("Show help");
    expect(row).not.toHaveAttribute("aria-selected");
  });

  it("uses option roles without aria-pressed", () => {
    render(<ListRow role="option" active title="Open MCP" meta="Ops · mcp" />);
    const option = screen.getByRole("option", { name: /Open MCP/ });
    expect(option).toHaveClass("ui-list-row", "active");
    expect(option).toHaveAttribute("aria-selected", "true");
    expect(option).not.toHaveAttribute("aria-pressed");
  });
});

describe("token CSS foundation", () => {
  it("defines shared primitive classes and density variables", () => {
    const dir = dirname(fileURLToPath(import.meta.url));
    const css = readFileSync(resolve(dir, "../styles.css"), "utf8");
    expect(css).toMatch(/--control-min-h/);
    expect(css).toMatch(/--touch-min/);
    expect(css).toMatch(/\.ui-dialog\b/);
    expect(css).toMatch(/\.ui-tab\b/);
    expect(css).toMatch(/\.ui-status-needs-you/);
    expect(css).toMatch(/data-appearance="light"/);
    expect(css).toMatch(/data-appearance="dark"/);
    // Duplicated workflow-dialog blocks removed (one definition remains).
    const workflowBlocks = css.split("/* Workflow authoring").length - 1;
    expect(workflowBlocks).toBe(1);
  });

  it("does not flex-squeeze inspector tab labels", () => {
    const dir = dirname(fileURLToPath(import.meta.url));
    const css = readFileSync(resolve(dir, "../styles.css"), "utf8");
    const inspectorRule = css.match(/\.inspector-tabs \.ui-tab,\s*\.inspector-tabs button \{[\s\S]*?\n\}/);
    expect(inspectorRule?.[0]).toBeTruthy();
    expect(inspectorRule?.[0]).toMatch(/flex:\s*0\s+0\s+auto/);
    expect(inspectorRule?.[0]).toMatch(/min-width:\s*max-content/);
    expect(inspectorRule?.[0]).not.toMatch(/flex:\s*1\b/);
    const size = inspectorRule?.[0].match(/font-size:\s*(\d+)px/);
    expect(Number(size?.[1])).toBeGreaterThanOrEqual(11);
  });

  it("shares compact single-line chrome across ListRow, palette, and completion", () => {
    const dir = dirname(fileURLToPath(import.meta.url));
    const css = readFileSync(resolve(dir, "../styles.css"), "utf8");
    expect(css).not.toMatch(/grid-template-columns:\s*150px/);
    const family = css.match(/\.ui-list-row,[\s\S]*?\.completion button\s*\{[\s\S]*?\n\}/);
    expect(family?.[0]).toMatch(/display:\s*flex/);
    expect(family?.[0]).not.toMatch(/flex-direction:\s*column/);
    const selected = css.match(
      /\.ui-list-row\.active[\s\S]*?\.completion button\[aria-selected="true"\]\s*\{[\s\S]*?\n\}/,
    );
    expect(selected?.[0]).toMatch(/background:\s*var\(--raised\)/);
    expect(selected?.[0]).toMatch(/box-shadow:\s*inset 2px 0 0 var\(--acid\)/);
    expect(css).toMatch(/\.ui-list-row-main\s*\{[^}]*flex-direction:\s*row/);
    const title = css.match(/\.ui-list-row-title,\s*\.completion button strong\s*\{[\s\S]*?\n\}/);
    expect(title?.[0]).toMatch(/font-weight:\s*inherit/);
    expect(title?.[0]).toMatch(/text-overflow:\s*ellipsis/);
    expect(title?.[0]).toMatch(/min-width:\s*0/);
  });
});
