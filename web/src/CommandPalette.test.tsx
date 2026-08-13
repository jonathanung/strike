import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CommandPalette } from "./CommandPalette";
import type { CatalogCommand } from "./commands";

afterEach(() => cleanup());

const commands: CatalogCommand[] = [
  { id: "a", label: "Open MCP", detail: "Ops · mcp", action: { type: "surface", mode: "ops", surface: "mcp" } },
  { id: "b", label: "/help", detail: "Show help", action: { type: "slash", insert: "/help", run: true } },
  { id: "c", label: "Mode: Chat", detail: "Conversation", action: { type: "mode", mode: "chat" } },
];

describe("CommandPalette", () => {
  it("renders compact ListRow options and keeps listbox keyboard roles", () => {
    const onClose = vi.fn();
    const onRun = vi.fn();
    render(<CommandPalette open commands={commands} onClose={onClose} onRun={onRun} />);

    const list = screen.getByRole("listbox", { name: "Commands" });
    expect(list).toHaveClass("palette-list");
    const options = within(list).getAllByRole("option");
    expect(options).toHaveLength(3);
    expect(options[0]).toHaveClass("ui-list-row", "active");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    expect(options[0]).not.toHaveAttribute("aria-pressed");
    expect(options[0].querySelector(".ui-list-row-title")).toHaveTextContent("Open MCP");
    expect(options[0].querySelector(".ui-list-row-meta")).toHaveTextContent("Ops · mcp");
    expect(options[1]).toHaveAttribute("aria-selected", "false");

    const filter = screen.getByLabelText("Filter commands");
    fireEvent.keyDown(filter, { key: "ArrowDown" });
    expect(within(list).getAllByRole("option")[1]).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(filter, { key: "Enter" });
    expect(onRun).toHaveBeenCalledWith(commands[1]);
    expect(onClose).toHaveBeenCalled();
  });
});
