# TUI source groups

**Edit Go files here only.** `go generate ./internal/tui` (also `make test` /
`make build` / CI) flattens these into `internal/tui/*.go` because Go allows
only one directory per package.

Flattened `internal/tui/*.go` files are **gitignored** and stamped
`DO NOT EDIT`. Any change to them is wiped on the next generate. If a Charm v2
(or any) migration edits flattened copies, the work is silently lost —
always edit `_src/<group>/` (or the real packages below), then regenerate.

| Directory | Concern |
|-----------|---------|
| `app/` | Model, events, keys, slash commands |
| `layout/` | View composition, splits, welcome, chrome |
| `modal/` | Overlay dialogs and pickers |
| `window/` | Right-pane windows and registry |
| `cell/` | Transcript cells and export |
| `input/` | Composer, completion, mouse, editor launch |
| `session/` | Session navigation |
| `hostui/` | MCP, project data, terminal notify, links |
| `util/` | Shims to `common/` |
| `test/` | Cross-cutting tests (`boundary_test`, style boundary, …) |

Independent real packages (not flattened): `../theme`, `../ui`, `../term`, `../common`.

## Charm stack imports (E13)

| Era | Paths |
|-----|--------|
| v1 | `github.com/charmbracelet/{lipgloss,glamour,…}` (remaining until E13.2/E13.5) |
| v2 (current for tea/bubbles) | `charm.land/{bubbletea,bubbles}/v2` (+ `charm.land/lipgloss/v2` only at bubbles theme bridge) |
| v2 (correct) | `charm.land/{bubbletea,lipgloss,bubbles,glamour}/v2` |
| v2 (wrong) | `github.com/charmbracelet/…/v2` — rejected by `TestCharmImportPaths` |

When rewriting imports for Charm v2, update path constants in the **same
commit** as the rewrites (especially `lipglossPath` in
`test/style_boundary_test.go`). Do not open a separate “allowlist only” PR
after the imports already moved.
