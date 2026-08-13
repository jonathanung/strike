import { useEffect, useMemo, useRef, useState } from "react";
import { type CatalogCommand, filterCommands } from "./commands";
import { Dialog, ListRow } from "./ui";

export function CommandPalette({
  open,
  commands,
  onClose,
  onRun,
}: {
  open: boolean;
  commands: CatalogCommand[];
  onClose: () => void;
  onRun: (cmd: CatalogCommand) => void;
}) {
  const [query, setQuery] = useState("");
  const [index, setIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const filtered = useMemo(() => filterCommands(commands, query), [commands, query]);

  useEffect(() => {
    if (open) {
      setQuery("");
      setIndex(0);
      window.setTimeout(() => inputRef.current?.focus(), 0);
    }
  }, [open]);

  useEffect(() => {
    setIndex(0);
  }, [query]);

  if (!open) return null;

  const run = (cmd: CatalogCommand) => {
    onRun(cmd);
    onClose();
  };

  return (
    <Dialog title="Command palette" onClose={onClose} initialFocus='input[aria-label="Filter commands"]'>
      <input
        ref={inputRef}
        className="palette-input"
        type="search"
        placeholder="Search modes, surfaces, commands…"
        aria-label="Filter commands"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") {
            e.preventDefault();
            setIndex((i) => Math.min(filtered.length - 1, i + 1));
          } else if (e.key === "ArrowUp") {
            e.preventDefault();
            setIndex((i) => Math.max(0, i - 1));
          } else if (e.key === "Enter") {
            e.preventDefault();
            const cmd = filtered[index];
            if (cmd) run(cmd);
          } else if (e.key === "Escape") {
            e.preventDefault();
            onClose();
          }
        }}
      />
      <ul className="palette-list" role="listbox" aria-label="Commands">
        {filtered.map((cmd, i) => (
          <li key={cmd.id}>
            <ListRow
              role="option"
              active={i === index}
              title={cmd.label}
              meta={cmd.detail}
              onMouseEnter={() => setIndex(i)}
              onClick={() => run(cmd)}
            />
          </li>
        ))}
        {!filtered.length && <li className="muted">No matching commands</li>}
      </ul>
      <p className="muted palette-hint">↑↓ navigate · ↵ run · esc close</p>
    </Dialog>
  );
}
