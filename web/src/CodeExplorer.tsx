/**
 * Scoped Code explorer (WEBUI.8 / #1080).
 * Read-first browse / search / read / markdown / changed-files.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { request } from "./api";
import {
  breadcrumbs,
  entryIsDir,
  entryName,
  fileContent,
  fileNotice,
  filePath,
  fileSkipped,
  isMarkdownPath,
  joinPath,
  parentPath,
  parseFileEntity,
  renderMarkdownSafe,
  type DirEntryDTO,
  type FileContentDTO,
} from "./codeExplorer";

export type ChangedFile = { path: string; added: number; deleted: number; diff: string };

type Props = {
  available: boolean;
  rootID?: string;
  /** Deep-link entity: path or path:line */
  entity?: string;
  onOpenPath?: (path: string, line?: number) => void;
  changedFiles?: ChangedFile[];
  expandedDiffs?: Set<string>;
  toggleDiff?: (path: string) => void;
  readOnly?: boolean;
  /** Reviewed ApplyEdit/ApplyPatch available (live + fileApply cap). */
  canApply?: boolean;
};

function qs(rootID?: string, extra: Record<string, string> = {}): string {
  const p = new URLSearchParams(extra);
  if (rootID) p.set("root", rootID);
  const s = p.toString();
  return s ? `?${s}` : "";
}

export function CodeExplorer({
  available,
  rootID,
  entity,
  onOpenPath,
  changedFiles = [],
  expandedDiffs,
  toggleDiff,
  readOnly,
  canApply,
}: Props) {
  const [dir, setDir] = useState("");
  const [entries, setEntries] = useState<DirEntryDTO[]>([]);
  const [dirError, setDirError] = useState("");
  const [loadingDir, setLoadingDir] = useState(false);

  const [query, setQuery] = useState("");
  const [searchHits, setSearchHits] = useState<string[]>([]);
  const [searchError, setSearchError] = useState("");
  const [searching, setSearching] = useState(false);
  const searchAbort = useRef<AbortController | null>(null);

  const [selectedPath, setSelectedPath] = useState("");
  const [focusLine, setFocusLine] = useState<number | undefined>();
  const [file, setFile] = useState<FileContentDTO | null>(null);
  const [fileError, setFileError] = useState("");
  const [loadingFile, setLoadingFile] = useState(false);
  const [mdMode, setMdMode] = useState<"preview" | "raw">("preview");
  // Default to changed-files summary (legacy Files panel behavior); Browse is explicit.
  const [view, setView] = useState<"browse" | "changed">("changed");
  const [applyOld, setApplyOld] = useState("");
  const [applyNew, setApplyNew] = useState("");
  const [applyBusy, setApplyBusy] = useState(false);
  const [applyMsg, setApplyMsg] = useState("");

  const crumbs = useMemo(() => breadcrumbs(dir), [dir]);
  const bodyRef = useRef<HTMLPreElement | null>(null);

  // Root switch: clear all explorer state (no cross-workspace leak).
  useEffect(() => {
    setDir("");
    setEntries([]);
    setSelectedPath("");
    setFile(null);
    setQuery("");
    setSearchHits([]);
    setFocusLine(undefined);
    setDirError("");
    setFileError("");
    setSearchError("");
  }, [rootID]);

  // Deep link entity
  useEffect(() => {
    const { path, line } = parseFileEntity(entity);
    if (!path) return;
    setSelectedPath(path);
    setFocusLine(line);
    setDir(parentPath(path));
    setView("browse");
  }, [entity, rootID]);

  const loadDir = useCallback(async (path: string) => {
    if (!available) return;
    setLoadingDir(true);
    setDirError("");
    try {
      const res = await request<{ entries?: DirEntryDTO[] }>(
        `/v1/files${qs(rootID, path ? { path } : {})}`,
      );
      setEntries(res.entries || []);
      setDir(path);
    } catch (err) {
      setDirError(err instanceof Error ? err.message : String(err));
      setEntries([]);
    } finally {
      setLoadingDir(false);
    }
  }, [available, rootID]);

  useEffect(() => {
    void loadDir(dir);
    // only when root changes or initial — dir updates via loadDir itself
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [available, rootID]);

  const openFile = useCallback(async (path: string, line?: number) => {
    if (!available || !path) return;
    setSelectedPath(path);
    setFocusLine(line);
    setLoadingFile(true);
    setFileError("");
    setFile(null);
    onOpenPath?.(path, line);
    try {
      const res = await request<FileContentDTO>(
        `/v1/file${qs(rootID, { path })}`,
      );
      setFile(res);
      if (isMarkdownPath(path)) setMdMode("preview");
    } catch (err) {
      setFileError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoadingFile(false);
    }
  }, [available, rootID, onOpenPath]);

  useEffect(() => {
    if (selectedPath) void openFile(selectedPath, focusLine);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedPath, rootID]);

  useEffect(() => {
    if (focusLine && bodyRef.current) {
      const el = bodyRef.current.querySelector(`[data-line="${focusLine}"]`);
      el?.scrollIntoView({ block: "center" });
    }
  }, [focusLine, file]);

  const runSearch = useCallback(async (q: string) => {
    searchAbort.current?.abort();
    const ac = new AbortController();
    searchAbort.current = ac;
    const trimmed = q.trim();
    if (!trimmed) {
      setSearchHits([]);
      setSearchError("");
      setSearching(false);
      return;
    }
    setSearching(true);
    setSearchError("");
    try {
      const res = await request<{ paths?: string[]; error?: string }>(
        `/v1/files/search${qs(rootID, { q: trimmed, limit: "40" })}`,
      );
      if (ac.signal.aborted) return;
      setSearchHits(res.paths || []);
    } catch (err) {
      if (ac.signal.aborted) return;
      setSearchHits([]);
      setSearchError(err instanceof Error ? err.message : String(err));
    } finally {
      if (!ac.signal.aborted) setSearching(false);
    }
  }, [rootID]);

  useEffect(() => {
    const t = window.setTimeout(() => void runSearch(query), 200);
    return () => window.clearTimeout(t);
  }, [query, runSearch]);

  if (!available) {
    return (
      <section className="code-explorer" aria-label="Code explorer">
        <h2>Code</h2>
        <p className="muted" role="status">Files capability unavailable.</p>
      </section>
    );
  }

  const content = file ? fileContent(file) : "";
  const skipped = file ? fileSkipped(file) : false;
  const notice = file ? fileNotice(file) : "";
  const showMd = selectedPath && isMarkdownPath(selectedPath) && !skipped && content;

  return (
    <section className="code-explorer" aria-label="Code explorer">
      <header className="code-explorer-head">
        <h2>Code</h2>
        {readOnly ? <span className="muted">read-only</span> : null}
        <div className="code-view-tabs" role="tablist" aria-label="Code views">
          <button type="button" role="tab" aria-selected={view === "browse"} className={view === "browse" ? "active" : ""} onClick={() => setView("browse")}>Browse</button>
          <button type="button" role="tab" aria-selected={view === "changed"} className={view === "changed" ? "active" : ""} onClick={() => setView("changed")}>
            Changed{changedFiles.length ? ` · ${changedFiles.length}` : ""}
          </button>
        </div>
      </header>

      {view === "changed" ? (
        <div className="changed-files" aria-label="Changed files">
          {!changedFiles.length ? <p className="muted">No changed files reported.</p> : null}
          {changedFiles.map((f) => (
            <article key={f.path} className="changed-file">
              <button
                type="button"
                aria-expanded={expandedDiffs?.has(f.path)}
                onClick={() => {
                  if (toggleDiff) toggleDiff(f.path);
                  else {
                    void openFile(f.path);
                    setView("browse");
                  }
                }}
              >
                <code>{f.path}</code>
                <span className="diff-stat"><b>+{f.added}</b><b>-{f.deleted}</b></span>
              </button>
              <button
                type="button"
                className="code-open-file"
                onClick={() => {
                  void openFile(f.path);
                  setView("browse");
                }}
              >
                Open
              </button>
              {expandedDiffs?.has(f.path) ? (
                <pre className="diff-view">{f.diff || "No textual diff available."}</pre>
              ) : null}
            </article>
          ))}
        </div>
      ) : (
        <div className="code-explorer-body">
          <div className="code-sidebar">
            <label className="code-search">
              <span className="sr-only">Search files</span>
              <input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Search files…"
                aria-label="Search files"
              />
              {searching ? <span className="muted">…</span> : null}
            </label>
            {searchError ? <p className="muted" role="status">{searchError}</p> : null}
            {query.trim() && searchHits.length > 0 ? (
              <ul className="code-search-hits" aria-label="Search results">
                {searchHits.map((p) => (
                  <li key={p}>
                    <button type="button" onClick={() => {
                      if (p.endsWith("/")) {
                        void loadDir(p.replace(/\/$/, ""));
                      } else {
                        void openFile(p);
                      }
                    }}>
                      <code>{p}</code>
                    </button>
                  </li>
                ))}
              </ul>
            ) : null}
            {query.trim() && !searching && !searchHits.length && !searchError ? (
              <p className="muted">No matches.</p>
            ) : null}

            <nav className="code-breadcrumbs" aria-label="Path breadcrumbs">
              {crumbs.map((c, i) => (
                <span key={c.path || "root"}>
                  {i > 0 ? " / " : null}
                  <button type="button" onClick={() => void loadDir(c.path)}>{c.label}</button>
                </span>
              ))}
            </nav>
            {loadingDir ? <p className="muted">Loading…</p> : null}
            {dirError ? <p className="muted" role="alert">{dirError}</p> : null}
            <ul className="code-tree" role="tree" aria-label="Directory">
              {dir ? (
                <li>
                  <button type="button" onClick={() => void loadDir(parentPath(dir))}>..</button>
                </li>
              ) : null}
              {entries.map((e) => {
                const name = entryName(e);
                const isDir = entryIsDir(e);
                const path = joinPath(dir, name);
                return (
                  <li key={path} role="treeitem">
                    <button
                      type="button"
                      className={selectedPath === path ? "active" : ""}
                      onClick={() => {
                        if (isDir) void loadDir(path);
                        else void openFile(path);
                      }}
                    >
                      {isDir ? "[dir] " : ""}{name}{isDir ? "/" : ""}
                    </button>
                  </li>
                );
              })}
            </ul>
          </div>

          <div className="code-viewer" aria-label="File viewer">
            {!selectedPath ? (
              <p className="muted">Select a file to read.</p>
            ) : loadingFile ? (
              <p className="muted">Loading {selectedPath}…</p>
            ) : fileError ? (
              <p className="muted" role="alert">{fileError}</p>
            ) : skipped ? (
              <div>
                <h3><code>{filePath(file!) || selectedPath}</code></h3>
                <p className="muted" role="status">{notice || "Skipped"}</p>
              </div>
            ) : (
              <>
                <header className="code-viewer-head">
                  <h3><code>{filePath(file!) || selectedPath}</code></h3>
                  {showMd ? (
                    <div className="code-md-toggle" role="group" aria-label="Markdown view">
                      <button type="button" aria-pressed={mdMode === "preview"} className={mdMode === "preview" ? "active" : ""} onClick={() => setMdMode("preview")}>Preview</button>
                      <button type="button" aria-pressed={mdMode === "raw"} className={mdMode === "raw" ? "active" : ""} onClick={() => setMdMode("raw")}>Raw</button>
                    </div>
                  ) : null}
                  {notice ? <p className="muted">{notice}</p> : null}
                </header>
                {showMd && mdMode === "preview" ? (
                  <div
                    className="code-md-preview"
                    // Safe: renderMarkdownSafe escapes HTML first.
                    dangerouslySetInnerHTML={{ __html: renderMarkdownSafe(content) }}
                  />
                ) : (
                  <pre className="code-raw" ref={bodyRef} tabIndex={0}>
                    {content.split("\n").map((line, i) => {
                      const n = i + 1;
                      return (
                        <div
                          key={n}
                          data-line={n}
                          className={focusLine === n ? "code-line focus" : "code-line"}
                        >
                          <span className="code-gutter" aria-hidden>{n}</span>
                          <span className="code-text">{line || " "}</span>
                        </div>
                      );
                    })}
                  </pre>
                )}
                {canApply && !readOnly && selectedPath && !skipped ? (
                  <fieldset className="code-apply" aria-label="Reviewed apply edit">
                    <legend>Apply edit (reviewed)</legend>
                    <p className="muted">
                      Target <code>{selectedPath}</code>. Preview the replacement, then confirm.
                      No free-form write API.
                    </p>
                    <label>
                      Find (oldString)
                      <textarea value={applyOld} onChange={(e) => setApplyOld(e.target.value)} rows={3} disabled={applyBusy} />
                    </label>
                    <label>
                      Replace (newString)
                      <textarea value={applyNew} onChange={(e) => setApplyNew(e.target.value)} rows={3} disabled={applyBusy} />
                    </label>
                    {(applyOld || applyNew) ? (
                      <pre className="diff-view code-apply-preview" aria-label="Apply preview">
                        {`- ${applyOld.split("\n").join("\n- ")}\n+ ${applyNew.split("\n").join("\n+ ")}`}
                      </pre>
                    ) : null}
                    {applyMsg ? <p className="muted" role="status">{applyMsg}</p> : null}
                    <button
                      type="button"
                      className="primary"
                      disabled={applyBusy || !applyOld || applyOld === applyNew}
                      onClick={() => {
                        if (!window.confirm(`Apply edit to ${selectedPath}?`)) return;
                        setApplyBusy(true);
                        setApplyMsg("");
                        void request<{ ok?: boolean; path?: string; count?: number; already?: boolean }>(
                          `/v1/files/apply-edit${qs(rootID)}`,
                          {
                            method: "POST",
                            body: JSON.stringify({
                              path: selectedPath,
                              oldString: applyOld,
                              newString: applyNew,
                            }),
                          },
                        )
                          .then((res) => {
                            setApplyMsg(
                              res.already
                                ? "Already applied (no write)."
                                : `Applied ${res.count ?? 1} replacement(s) to ${res.path || selectedPath}`,
                            );
                            setApplyOld("");
                            setApplyNew("");
                            void openFile(selectedPath, focusLine);
                          })
                          .catch((err) => {
                            setApplyMsg(err instanceof Error ? err.message : String(err));
                          })
                          .finally(() => setApplyBusy(false));
                      }}
                    >
                      Confirm apply
                    </button>
                  </fieldset>
                ) : null}
              </>
            )}
          </div>
        </div>
      )}
    </section>
  );
}
