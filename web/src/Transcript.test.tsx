import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Transcript } from "./Transcript";

describe("Transcript", () => {
  afterEach(() => cleanup());

  it("renders markdown, fenced code, and file references structurally", () => {
    render(<Transcript item={{ id: "a", kind: "assistant", text: "# Result\n**Done** in src/main.go:12\n```go\nfmt.Println(\"ok\")\n```" }} />);
    expect(screen.getByRole("heading", { name: "Result" })).toBeInTheDocument();
    expect(screen.getByText("Done").tagName).toBe("STRONG");
    expect(screen.getByText(/src\/main.go/)).toHaveClass("file-ref");
    expect(screen.getByText('fmt.Println("ok")')).toBeInTheDocument();
  });

  it("renders structured tool output and diffs", () => {
    const { rerender, container } = render(<Transcript item={{ id: "t", kind: "tool", title: "read", text: '{"path":"a.go"}' }} />);
    expect(screen.getByText("structured data")).toBeInTheDocument();
    rerender(<Transcript item={{ id: "d", kind: "tool", title: "edit", text: "--- a.go\n+++ a.go\n@@ -1 +1 @@\n-old\n+new" }} />);
    expect(screen.getByText("diff")).toBeInTheDocument();
    expect([...container.querySelectorAll(".add")].some((node) => node.textContent === "+new\n")).toBe(true);
    expect([...container.querySelectorAll(".del")].some((node) => node.textContent === "-old\n")).toBe(true);
  });

  it("renders tool cards collapsed by default with a scannable summary", () => {
    const { container } = render(<Transcript item={{ id: "t", kind: "tool", title: "read", text: '{"path":"a.go"}' }} />);
    const card = container.querySelector("details.tool-card") as HTMLDetailsElement | null;
    expect(card).toBeTruthy();
    expect(card?.open).toBe(false);
    const scope = within(container);
    expect(scope.getByText("read", { selector: "summary span" })).toBeInTheDocument();
    expect(scope.getByText("structured data")).toBeInTheDocument();
    // Pretty-printed body stays in the DOM for expand, but the card starts closed.
    expect(card?.querySelector("pre")?.textContent).toContain('"path"');
  });
});

  it("hides reasoning when showThinking is false", () => {
    const { rerender } = render(<Transcript item={{ id: "r", kind: "reasoning", title: "Reasoning", text: "hidden thoughts" }} showThinking={false} />);
    expect(screen.queryByText("hidden thoughts")).not.toBeInTheDocument();
    rerender(<Transcript item={{ id: "r", kind: "reasoning", title: "Reasoning", text: "hidden thoughts" }} showThinking />);
    expect(screen.getByText("hidden thoughts")).toBeInTheDocument();
  });
