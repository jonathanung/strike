/** Last-turn harness summary for /rewind preview (mirrors TUI undoPreview). */
export type TurnFileChange = { path: string; kind?: string };

export type UndoPreview = {
  files: TurnFileChange[];
  skipped: number;
  uncovered: string[];
};

export const emptyUndoPreview = (): UndoPreview => ({ files: [], skipped: 0, uncovered: [] });

export function hasRestorableFiles(p: UndoPreview): boolean {
  return p.files.length > 0 || p.skipped > 0;
}

export function hasUncovered(p: UndoPreview): boolean {
  return p.uncovered.length > 0;
}

/** Root-lineage only — child turns do not push/pop the undo stack (TUI parity). */
export function isRootLineage(data?: Record<string, unknown>): boolean {
  if (!data) return true;
  if (data.parentSessionId) return false;
  const depth = Number(data.depth ?? 0);
  return !(depth > 0);
}

export function parseUndoPreview(data?: Record<string, unknown>): UndoPreview {
  const raw = Array.isArray(data?.files) ? data!.files : [];
  const files: TurnFileChange[] = [];
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const row = item as Record<string, unknown>;
    const path = String(row.path ?? "").trim();
    if (!path) continue;
    const kind = String(row.kind ?? "").trim();
    files.push(kind ? { path, kind } : { path });
  }
  const skipped = Number(data?.checkpointSkipped ?? 0) || 0;
  const uncovered = Array.isArray(data?.uncovered)
    ? data!.uncovered.map((v) => String(v)).filter(Boolean)
    : [];
  return { files, skipped, uncovered };
}

export type UndoPreviewLine = { text: string; tone?: "warn" };

/** Format preview lines for the undo dialog (TUI formatUndoPreview parity). */
export function formatUndoPreviewLines(p: UndoPreview, maxShow = 12): UndoPreviewLine[] {
  if (!hasRestorableFiles(p) && !hasUncovered(p)) return [];
  const lines: UndoPreviewLine[] = [];
  if (p.files.length > 0) {
    lines.push({ text: `Paths to restore (${p.files.length}):` });
    for (let i = 0; i < p.files.length; i++) {
      if (i >= maxShow) {
        lines.push({ text: `  … and ${p.files.length - maxShow} more` });
        break;
      }
      const f = p.files[i];
      lines.push({ text: f.kind ? `  ${f.kind}  ${f.path}` : `  ${f.path}` });
    }
  } else if (p.skipped > 0) {
    lines.push({ text: "No restorable harness paths in the last turn." });
  }
  if (p.skipped > 0) {
    lines.push({ text: `Checkpoint skipped: ${p.skipped} path(s) (oversized/unreadable).` });
  }
  if (hasUncovered(p)) {
    lines.push({
      text: `Warning: uncovered mutations (${p.uncovered.join(", ")}) — shell/other changes are NOT restored.`,
      tone: "warn",
    });
  }
  return lines;
}

/** Detail string for the "chat and files" choice (TUI newUndoModal parity). */
export function filesChoiceDetail(p: UndoPreview): string {
  if (p.files.length > 0) return `drop the last turn and restore ${p.files.length} path(s)`;
  if (p.skipped > 0) return "drop the last turn; checkpointed paths were not capturable";
  if (hasUncovered(p)) return "drop the last turn; no harness file snapshots (uncovered mutations)";
  return "drop the last turn and restore files edited in that turn";
}

/** Default selection: prefer files when restorable/uncovered paths exist. */
export function defaultRestoreFiles(p: UndoPreview, preferFiles: boolean): boolean {
  if (preferFiles) return true;
  if (!hasRestorableFiles(p) && !hasUncovered(p)) return false;
  // Bare /rewind still opens the picker; bias toward full restore when useful.
  return hasRestorableFiles(p) || hasUncovered(p);
}
