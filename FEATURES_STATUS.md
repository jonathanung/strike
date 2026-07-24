# Feature Status

Snapshot as of 2026-07-24. [`features.md`](features.md) remains the canonical
backlog; this report records implementation status at the time of the final
requirements audit.

## Legend

- **Complete** — delivered for the audited requirement.
- **Partial** — useful behavior or supporting foundations exist, but the full
  backlog requirement is not delivered.
- **Blocked** — intentionally not implemented because a safety constraint must
  be resolved first.
- **Deferred** — not included in the delivered scope.

## Summary

**4 of 38 features are complete.** The command palette is partial. All final
tests, race checks, vet checks, builds, and isolated PTY smoke flows passed at
the point of implementation.

## Composer & input

| Feature | Status | Evidence / note |
| --- | --- | --- |
| ★ **Slash-command autocomplete** | Complete | Filterable fuzzy command and skill completion, descriptions, argument hints, and tab/enter completion are implemented. |
| ★ **`@file` mentions** | Blocked | Go 1.26.2 has a rooted-file final-symlink safety concern; no unsafe implementation was accepted. |
| **Multi-line editing** | Complete | Supported newline insertion and bounded auto-growing composer behavior are implemented. |
| **History recall** | Complete | Per-project persisted prompt history and empty-composer up/down recall are implemented. |
| **`ctrl+e` external editor** | Deferred | Not included in the delivered scope. |
| **Paste detection** | Deferred | Not included in the delivered scope. |
| **Kill/yank & word-wise navigation** | Partial | Stock textarea behavior is available; Ctrl+K's palette conflict and resolution prevent claiming the full readline-key requirement. |

## Transcript & rendering

| Feature | Status | Evidence / note |
| --- | --- | --- |
| ★ **Markdown rendering** | Deferred | Not included in the delivered scope. |
| ★ **Rich diff cells** | Partial | Transcript/tool metadata provides a foundation; full styled, toggleable diff cells are not delivered. |
| **Collapsible tool cells** | Partial | Tool-cell and transcript structure provides a foundation; expansion and grouped exploration cells are not complete. |
| **Live bash output** | Deferred | Not included in the delivered scope. |
| **Mouse support** | Deferred | Not included in the delivered scope. |
| **Copy affordances** | Deferred | Not included in the delivered scope. |
| **Timestamps + token/cost meter** | Partial | Event and status-line structure provides a foundation; the complete stats and context meter are not delivered. |

## Editor integration

| Feature | Status | Evidence / note |
| --- | --- | --- |
| **`/vim <fpath>` split editor** | Deferred | Not included in the delivered scope. |
| **Open-at-line** | Deferred | Not included in the delivered scope. |
| **Post-edit review** | Partial | Tool metadata and touched-file information provide a foundation; the `v` review flow is not delivered. |

## Commands, palette & discovery

| Feature | Status | Evidence / note |
| --- | --- | --- |
| ★ **Command palette** | Partial | Ctrl+K safely fuzzy-searches current provider, model, auth, agent, help, and skill actions, but not every current or future action such as theme, session, and copy. |
| **Contextual help footer** | Partial | Existing footer/status rendering provides a foundation; comprehensive focus-specific hints are not delivered. |
| **`/keys`** | Deferred | Not included in the delivered scope. |
| **Keybinding config** | Deferred | Not included in the delivered scope. |

## Sessions & continuity

| Feature | Status | Evidence / note |
| --- | --- | --- |
| ★ **Session picker / resume** | Partial | JSONL session persistence and replay-related foundations exist; the complete picker and continue flow are not delivered. |
| **Auto-titling** | Deferred | Not included in the delivered scope. |
| **Fork & rewind** | Deferred | Not included in the delivered scope. |
| **`strike exec` headless mode** | Deferred | Not included in the delivered scope. |

## Agents, models & providers

| Feature | Status | Evidence / note |
| --- | --- | --- |
| **Agent picker modal** | Partial | Agent loading, selection, cycling, descriptions, and defaults provide a foundation; the specified centered picker is not complete. |
| **Model metadata in the picker** | Partial | Catalog-backed model selection provides a foundation; all requested metadata is not shown. |
| **Catalog-driven defaults** | Partial | Catalog support exists, but provider defaults remain partly hardcoded. |
| **Provider health indicator** | Partial | Auth/provider state and notices provide a foundation; the requested proactive status indicator is not complete. |

## Permissions & safety UX

| Feature | Status | Evidence / note |
| --- | --- | --- |
| ★ **Reject with feedback** | Complete | The permission prompt collects rejection text and returns it to the model as the tool result. |
| **Diff preview in permission prompts** | Partial | Old/new metadata and permission rendering provide a foundation; a complete inline diff preview is not delivered. |
| **Remember-with-scope choices** | Partial | Session rules and layered project configuration provide a foundation; all scoped choices and persistence UI are not complete. |
| **Auto-approve countdown mode** | Deferred | Not included in the delivered scope. |

## Polish

| Feature | Status | Evidence / note |
| --- | --- | --- |
| **Theme files** | Partial | Centralized theme primitives provide a foundation; JSON themes, detection, and live preview are not delivered. |
| **Terminal title + notification** | Deferred | Not included in the delivered scope. |
| **Spinner variety & elapsed time** | Partial | Spinner and turn-state rendering provide a foundation; the full elapsed/tool-call status is not delivered. |
| **Graceful narrow-terminal layout** | Partial | Tiny-terminal safety is implemented, but there is no complete layout reflow below 80 columns. |
| **First-run onboarding** | Deferred | Not included in the delivered scope. |

## Delivered implementation highlights

- Command discovery and interaction are concentrated in
  `internal/tui/commands.go`, `internal/tui/completion.go`,
  `internal/tui/app.go`, `internal/tui/modal.go`, and
  `internal/tui/palette.go`.
- Durable, project-scoped prompt history is implemented under
  `internal/history/`, with project-root support in `internal/project/`.
- Agent and skill configuration validation is implemented in
  `internal/config/agents.go` and covered by its tests.

## Verification

The final delivered scope passed fresh runs of:

```sh
go test ./... -count=1
go test -race ./... -count=1
make vet
make build
```

Isolated PTY smoke flows also passed, and final review passed for the delivered
scope.

## Next recommended wave

Either safely unblock `@file` by upgrading the runtime or hardening the
file-open contract, or advance the transcript Markdown and rich-diff
foundations.
