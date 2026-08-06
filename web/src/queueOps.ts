import type { ImageAttachment } from "./types";

export type QueuedPrompt = { text: string; images: ImageAttachment[] };

/** Remove the item at index (no-op if out of range). */
export function removeQueuedAt<T>(list: T[], index: number): T[] {
  if (index < 0 || index >= list.length) return list;
  return list.filter((_, i) => i !== index);
}

/** Swap item at index with neighbor at index+delta. */
export function moveQueuedAt<T>(list: T[], index: number, delta: number): T[] {
  const j = index + delta;
  if (index < 0 || index >= list.length || j < 0 || j >= list.length) return list;
  const next = list.slice();
  const tmp = next[index]!;
  next[index] = next[j]!;
  next[j] = tmp;
  return next;
}

/** Replace text on a queued prompt; keeps images. */
export function editQueuedText(list: QueuedPrompt[], index: number, text: string): QueuedPrompt[] {
  if (index < 0 || index >= list.length) return list;
  return list.map((item, i) => (i === index ? { ...item, text } : item));
}

export function clearQueue(): QueuedPrompt[] {
  return [];
}
