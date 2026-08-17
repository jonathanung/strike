import { cleanup, render, screen, within, fireEvent } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { formatCostNotice } from "./cost";
import { groupTranscriptItems, Transcript, TranscriptList } from "./Transcript";

describe("Transcript", () => {
  afterEach(() => cleanup());

  it("renders markdown, fenced code, and file references structurally", () => {
    render(<Transcript item={{ id: "a", kind: "assistant", text: "# Result\n**Done** in src/main.go:12\n```go\nfmt.Println(\"ok\")\n```" }} />);
    expect(screen.getByRole("heading", { name: "Result" })).toBeInTheDocument();
    expect(screen.getByText("Done").tagName).toBe("STRONG");
    expect(screen.getByText(/src\/main.go/)).toHaveClass("file-ref");
    expect(screen.getByText('fmt.Println("ok")')).toBeInTheDocument();
  });

  it("paints running and failed tool rows with working/error roles", () => {
    const { rerender, container } = render(
      <Transcript item={{ id: "t", kind: "tool", title: "bash", text: "", data: { status: "running" } }} />,
    );
    expect(container.querySelector(".tool-state-running")).toHaveTextContent("running");
    rerender(
      <Transcript item={{ id: "t", kind: "tool", title: "bash", text: "boom", data: { status: "error" } }} />,
    );
    expect(container.querySelector(".tool-state-error")).toHaveTextContent("error");
    rerender(
      <Transcript item={{ id: "t", kind: "tool", title: "bash", text: "ok", data: { status: "done" } }} />,
    );
    expect(container.querySelector(".tool-state-done")).toHaveTextContent("done");
  });

  it("renders structured tool output and diffs", () => {
    const { rerender, container } = render(<Transcript item={{ id: "t", kind: "tool", title: "read", text: '{"path":"a.go"}' }} />);
    expect(screen.getByText("structured")).toBeInTheDocument();
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
    expect(scope.getAllByText("read").length).toBeGreaterThan(0);
    expect(scope.getByText("structured")).toBeInTheDocument();
    expect(card?.querySelector("pre")?.textContent).toContain('"path"');
  });

  it("hides reasoning when showThinking is false", () => {
    const { rerender } = render(<Transcript item={{ id: "r", kind: "reasoning", title: "Reasoning", text: "hidden thoughts" }} showThinking={false} />);
    expect(screen.queryByText("hidden thoughts")).not.toBeInTheDocument();
    rerender(<Transcript item={{ id: "r", kind: "reasoning", title: "Reasoning", text: "hidden thoughts" }} showThinking />);
    expect(screen.getByText("hidden thoughts")).toBeInTheDocument();
  });

  it("groups consecutive exploration tools", () => {
    const items = [
      { id: "1", kind: "tool" as const, title: "read", text: "a" },
      { id: "2", kind: "tool" as const, title: "grep", text: "b" },
      { id: "3", kind: "assistant" as const, text: "done" },
    ];
    const groups = groupTranscriptItems(items);
    expect(groups).toHaveLength(2);
    expect(groups[0]).toMatchObject({ kind: "exploration" });
    render(<TranscriptList items={items} />);
    expect(screen.getByText(/2 steps/)).toBeInTheDocument();
  });

  it("invokes file ref handler", () => {
    const onFileRef = vi.fn();
    render(<Transcript item={{ id: "a", kind: "assistant", text: "see src/main.go" }} onFileRef={onFileRef} />);
    fireEvent.click(screen.getByRole("button", { name: /src\/main.go/ }));
    expect(onFileRef).toHaveBeenCalledWith("src/main.go", undefined);
  });

  it("renders verification without raw JSON", () => {
    render(
      <Transcript
        item={{
          id: "v",
          kind: "system",
          title: "verification",
          text: "",
          data: { eventKind: "verification", passed: true, summary: "tests green" },
        }}
      />,
    );
    expect(screen.getByText(/Passed/)).toBeInTheDocument();
    expect(screen.getByText(/tests green/)).toBeInTheDocument();
  });
});

describe("formatCostNotice", () => {
  it("never invents a fake zero without catalog rates", () => {
    expect(formatCostNotice({ provider: "echo", model: "m", inputTokens: 10, outputTokens: 5 })).toMatch(/unknown|not reported/);
    expect(formatCostNotice({ provider: "echo", model: "m", inputTokens: 10, outputTokens: 5 })).not.toMatch(/\$0(?!\.)/);
  });

  it("uses shared rates when hasCost", () => {
    const text = formatCostNotice(
      { provider: "xai", model: "grok", inputTokens: 1_000_000, outputTokens: 1_000_000 },
      { inputPerM: 1, outputPerM: 2, hasCost: true },
    );
    expect(text).toContain("$3");
  });
});
