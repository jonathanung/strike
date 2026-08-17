import { type Dispatch, type RefObject, type SetStateAction } from "react";
import { ChildAgentsPanel, type ChildEntry } from "./ChildAgents";
import { clearQueue, editQueuedText, moveQueuedAt, removeQueuedAt, type QueuedPrompt } from "./queueOps";

export type QueueEdit = { index: number; text: string };

export type PromptQueueProps = {
  queue: QueuedPrompt[];
  queueEdit: QueueEdit | null;
  setQueueEdit: Dispatch<SetStateAction<QueueEdit | null>>;
  queueEditCancel: { current: boolean };
  queueRef?: RefObject<HTMLOListElement | null>;
  setQueue: Dispatch<SetStateAction<QueuedPrompt[]>>;
  /** When true, render an empty-state instead of hiding. */
  empty?: boolean;
  className?: string;
};

/** Queued-prompt list with reorder / edit / clear. Shared by composer and the Chat session group. */
export function PromptQueue({
  queue,
  queueEdit,
  setQueueEdit,
  queueEditCancel,
  queueRef,
  setQueue,
  empty = false,
  className = "prompt-queue-wrap",
}: PromptQueueProps) {
  if (!queue.length && !empty) return null;
  if (!queue.length) {
    return (
      <div className={className}>
        <p className="muted">Queue is empty. Prompts sent while the agent is busy land here.</p>
      </div>
    );
  }
  return (
    <div className={className}>
      <ol ref={queueRef} className="prompt-queue" aria-label="Queued prompts">
        {queue.map((item, index) => (
          <li key={index}>
            {queueEdit?.index === index ? (
              <input
                className="queue-edit"
                aria-label={`Queued prompt text ${index + 1}`}
                value={queueEdit.text}
                autoFocus
                onChange={(event) => setQueueEdit({ index, text: event.target.value })}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    queueEditCancel.current = false;
                    setQueue((list) => editQueuedText(list, index, queueEdit.text));
                    setQueueEdit(null);
                  }
                  if (event.key === "Escape") {
                    event.preventDefault();
                    queueEditCancel.current = true;
                    setQueueEdit(null);
                  }
                }}
                onBlur={() => {
                  if (!queueEditCancel.current) setQueue((list) => editQueuedText(list, index, queueEdit.text));
                  queueEditCancel.current = false;
                  setQueueEdit(null);
                }}
              />
            ) : (
              <span>{item.text}{item.images.length > 0 ? ` (${item.images.length} img)` : ""}</span>
            )}
            <span className="queue-actions">
              <button type="button" aria-label={`Move queued prompt ${index + 1} up`} disabled={index === 0} onClick={() => setQueue((list) => moveQueuedAt(list, index, -1))}>↑</button>
              <button type="button" aria-label={`Move queued prompt ${index + 1} down`} disabled={index === queue.length - 1} onClick={() => setQueue((list) => moveQueuedAt(list, index, 1))}>↓</button>
              <button type="button" aria-label={`Edit queued prompt ${index + 1}`} onClick={() => { queueEditCancel.current = false; setQueueEdit({ index, text: item.text }); }}>✎</button>
              <button type="button" aria-label={`Remove queued prompt ${index + 1}`} onClick={() => { setQueue((list) => removeQueuedAt(list, index)); setQueueEdit((cur) => cur?.index === index ? null : cur); }}>×</button>
            </span>
          </li>
        ))}
      </ol>
      <div className="queue-toolbar">
        <button type="button" onClick={() => { setQueue(clearQueue()); setQueueEdit(null); }}>Clear queue</button>
      </div>
    </div>
  );
}

export type SessionActivityProps = {
  childrenEntries: ChildEntry[];
  selectedChildId?: string;
  onSelectChild: (id: string | undefined) => void;
  onOpenChildTranscript?: (id: string) => void;
};

/** Exclusive Chat inspector surface for child-agent activity. */
export function SessionActivity({
  childrenEntries,
  selectedChildId,
  onSelectChild,
  onOpenChildTranscript,
}: SessionActivityProps) {
  return (
    <section className="session-surface" aria-label="Activity">
      <div className="aside-heading">ACTIVITY</div>
      {childrenEntries.length ? (
        <ChildAgentsPanel
          children={childrenEntries}
          selectedId={selectedChildId}
          onSelect={onSelectChild}
          onOpenTranscript={onOpenChildTranscript}
        />
      ) : (
        <p className="muted">No child agents</p>
      )}
    </section>
  );
}

/** Exclusive Chat inspector surface for the prompt queue. */
export function SessionQueue(props: PromptQueueProps) {
  return (
    <section className="session-surface" aria-label="Queue">
      <div className="aside-heading">QUEUE</div>
      <PromptQueue
        {...props}
        empty
        className={props.className || "session-group-queue"}
      />
    </section>
  );
}
