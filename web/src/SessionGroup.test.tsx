import { useRef, useState } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { PromptQueue, SessionActivity, SessionQueue, type QueueEdit } from "./SessionGroup";
import type { QueuedPrompt } from "./queueOps";

afterEach(() => cleanup());

function QueueHarness({ initial, empty = false }: { initial: QueuedPrompt[]; empty?: boolean }) {
  const [queue, setQueue] = useState(initial);
  const [queueEdit, setQueueEdit] = useState<QueueEdit | null>(null);
  const queueEditCancel = useRef(false);
  return (
    <PromptQueue
      queue={queue}
      queueEdit={queueEdit}
      setQueueEdit={setQueueEdit}
      queueEditCancel={queueEditCancel}
      setQueue={setQueue}
      empty={empty}
    />
  );
}

describe("PromptQueue", () => {
  it("hides when empty unless empty is requested", () => {
    const { rerender } = render(<QueueHarness initial={[]} />);
    expect(screen.queryByRole("list", { name: "Queued prompts" })).not.toBeInTheDocument();
    rerender(<QueueHarness initial={[]} empty />);
    expect(screen.getByText(/Queue is empty/)).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Queued prompts" })).not.toBeInTheDocument();
  });

  it("reorders, edits, and clears queued prompts", () => {
    render(<QueueHarness initial={[{ text: "first", images: [] }, { text: "second", images: [] }]} />);
    const list = screen.getByRole("list", { name: "Queued prompts" });
    fireEvent.click(screen.getByRole("button", { name: "Move queued prompt 1 down" }));
    expect(list.textContent?.indexOf("second")).toBeLessThan(list.textContent!.indexOf("first"));
    fireEvent.click(screen.getByRole("button", { name: "Edit queued prompt 1" }));
    const editor = screen.getByLabelText("Queued prompt text 1");
    fireEvent.change(editor, { target: { value: "second-edited" } });
    fireEvent.keyDown(editor, { key: "Enter" });
    expect(list).toHaveTextContent("second-edited");
    fireEvent.click(screen.getByRole("button", { name: "Clear queue" }));
    expect(screen.queryByRole("list", { name: "Queued prompts" })).not.toBeInTheDocument();
  });
});

describe("SessionActivity", () => {
  it("lists child agents or an empty state", () => {
    const { rerender } = render(
      <SessionActivity
        childrenEntries={[["c1", { agent: "explore", name: "scout", status: "running" }]]}
        onSelectChild={() => {}}
      />,
    );
    expect(screen.getByLabelText("Activity")).toContainElement(screen.getByLabelText("Child scout"));
    rerender(<SessionActivity childrenEntries={[]} onSelectChild={() => {}} />);
    expect(screen.getByLabelText("Activity")).toHaveTextContent("No child agents");
  });
});

describe("SessionQueue", () => {
  it("renders the queue as its own surface including empty state", () => {
    const queueEditCancel = { current: false };
    const { rerender } = render(
      <SessionQueue
        queue={[{ text: "next task", images: [] }]}
        queueEdit={null}
        setQueueEdit={() => {}}
        queueEditCancel={queueEditCancel}
        setQueue={() => {}}
      />,
    );
    expect(screen.getByLabelText("Queue")).toContainElement(screen.getByRole("list", { name: "Queued prompts" }));
    expect(screen.getByRole("list", { name: "Queued prompts" })).toHaveTextContent("next task");
    rerender(
      <SessionQueue
        queue={[]}
        queueEdit={null}
        setQueueEdit={() => {}}
        queueEditCancel={queueEditCancel}
        setQueue={() => {}}
      />,
    );
    expect(screen.getByLabelText("Queue")).toHaveTextContent(/Queue is empty/);
  });
});
