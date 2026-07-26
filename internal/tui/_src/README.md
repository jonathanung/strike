# TUI source groups

Edit Go files here by concern. `go generate ./internal/tui` (also `make test` /
`make build`) flattens these into `internal/tui/*.go` because Go allows only one
directory per package.

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
| `test/` | Cross-cutting tests |

Independent real packages (not flattened): `../theme`, `../ui`, `../term`, `../common`.
