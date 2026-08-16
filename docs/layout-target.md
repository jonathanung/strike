# Target layout (harness extract epic)

Destination architecture for [epic #1202](https://github.com/jonathanung/strike/issues/1202)
(*Extractable harness module and parseable repo layout*). This document is the
locked tree **before any package moves**. It describes where code will live
after waves 0–5, not where it lives today. Current packages and import rules
remain in [ARCHITECTURE.md](ARCHITECTURE.md).

Do **not** `mv` packages from this document. Child issues own the moves.

| Wave | Issue | Work |
|---|---|---|
| 0 | #1203 | This document (docs only) |
| 1 | #1204 | `pkg/protocol` + `pkg/redact` workspace modules |
| 2 | #1205 | Split tool contract / kernel tools from product tools |
| 2 | #1206 | Decouple engine from Strike product packages |
| 2 | #1207 | Rename `internal/harness` → `fn` |
| 3 | #1208 | Move kernel into `harness/` + `harness/go.mod` |
| 3 | #1216 | Sibling `providers/` module (adapters, auth flows, factory) |
| 4 | #1209 | TUI flatten target → `internal/tui/app`; isolate kit |
| 5 | #1210–#1215 | Group remaining `internal/` into persist / trust / integrate / frontend / product / eval |

#1053 (extension/signing) is a separate epic. Do not fold it into this tree.

## Rules for this epic

- **No git submodules.** Nested Go modules in this repo only (`go.work` +
  root `replace`). No extra GitHub repos.
- **Do not publish** `harness`, `providers`, `pkg/protocol`, or `pkg/redact`
  to the module proxy in this epic. `replace` keeps CI and `GOWORK=off`
  builds green.
- **`cmd/strike` stays the only composition root.**
- **Wire and config names stay.** Protocol event `harness.progress` and
  config key `harnesses` are unchanged. The GitHub label `harness` and
  `sdk/go/harness` import path may stay.
- **`engine/route.go` is persona routing**, not the providers factory. It
  stays in the engine (capability / load / cost-class / pin fallback).
  OpenAI platform-key vs ChatGPT OAuth construction lives in
  `providers/factory` (#1216).
- Out of scope: rewriting team/multi-agent out of the engine; extracting a
  TUI kit module (`ui`/`theme` stay in-tree).

## Module graph (no cycle)

```
github.com/jonathanung/strike-cli/pkg/protocol     # stdlib only
github.com/jonathanung/strike-cli/pkg/redact       # stdlib only
        ▲                         ▲
        └────────────┬────────────┘
                     │
github.com/jonathanung/strike-cli/provider         # interim (#1216): interface + echo
github.com/jonathanung/strike-cli/harness          # protocol + redact only
                     ▲                             # #1208 moves provider/ → harness/provider
                     │
github.com/jonathanung/strike-cli/providers        # provider interface only
                     ▲                             # adapters, HTTP base, auth flows, factory
                     │
github.com/jonathanung/strike-cli                  # everything else
                                                   # replace → ./harness and ./providers
```

Allowed edges only:

| Module | May import |
|---|---|
| `pkg/protocol` | stdlib |
| `pkg/redact` | stdlib |
| `harness` | `pkg/protocol`, `pkg/redact`, stdlib |
| `providers` | `harness` (provider interface / types / echo — **not** `harness/engine`), stdlib |
| `strike-cli` | `harness`, `providers`, its own `pkg/*` and `internal/*` |

Forbidden:

- `harness` → `providers` or any `…/internal/…`
- `providers` → `harness/engine` or any `…/internal/…`
- cycles among the five modules

`pkg/sdk`, `pkg/timeline`, `pkg/diag`, and `pkg/telemetry` stay in the root
module this epic (#1204 does not extract them).

## Target tree

```
harness/                          # module github.com/jonathanung/strike-cli/harness
  engine/                         # turn loop; route.go stays (persona routing)
  provider/                       # interface, types, echo only
  tool/                           # contract, registry, retry — not product tools
  tools/                          # generic builtins: fs, bash, web, todo, sleep, git, …
  permission/
  actionfacts/
  question/
  sandbox/
  scheduler/
  safefile/
  fn/                             # today's internal/fn
  fn/external/
  verify/
  fault/

providers/                        # module github.com/jonathanung/strike-cli/providers
  base/                           # HTTP/SSE + AuthFunc
  anthropic/ openaicompat/ chatgpt/ google/
  auth/                           # OAuth/PKCE/device/refresh + BearerSource (not ~/.strike path)
  factory/                        # selectProvider + OpenAI vs ChatGPT routing

pkg/protocol/                     # own go.mod
pkg/redact/                       # own go.mod
pkg/sdk/                          # stays in root module
pkg/timeline/
pkg/diag/
pkg/telemetry/

cmd/strike/                       # only composition root

internal/
  persist/    session memory issue plan artifact attachment ledger history telemetry
  trust/      admission audit secret security
  integrate/  mcp lsp plugin container
  frontend/   tui server rpc acp host
  product/    config auth models project goal update version
  eval/       replay swebench sweep tbench progressive
  tools/      product builtins
  protocol/   compatibility re-export of pkg/protocol (not a wave-5 cluster move)
```

After #1209, `ls internal/tui/` (later `internal/frontend/tui/`) is:

```
app/          # flatten target for today's _src (package tui or app)
ui/           # kit — no Strike wordmark, no protocol/host
theme/
common/
term/
```

Not hundreds of flattened app files at the `tui/` root.

## Rename map

Go import paths change; wire and config names do not.

| Today | Target | Notes |
|---|---|---|
| `internal/fn` | `harness/fn` | After #1207; #1208 moves it into the harness module |
| `internal/fn/external` | `harness/fn/external` | Same |
| protocol event `harness.progress` | **unchanged** | Tests that encode this type stay |
| config key `harnesses` | **unchanged** | External-process registry in config JSON |
| GitHub label `harness` | **unchanged** | |
| `sdk/go/harness` | **unchanged** | External subprocess SDK name is fine |

| Today | Target | Issue |
|---|---|---|
| `internal/engine` | `harness/engine` | #1208 |
| `internal/engine/route.go` | `harness/engine` (same file) | Persona/capability/load router — **not** `providers/factory` |
| `internal/provider` (interface, types, effort, retry, stream) | `harness/provider` | #1208 |
| `internal/provider/echo` | `harness/provider` (echo) | #1208 |
| `internal/provider/base` | `providers/base` | #1216 |
| `internal/provider/{anthropic,openaicompat,chatgpt,google}` | `providers/{…}` | #1216 |
| `internal/auth` login/oauth/device/pkce/openai/xai/resolve | `providers/auth` | #1216 — flows only |
| `internal/auth` store (`~/.strike/auth.json`) | `internal/product/auth` | #1214 after #1216 |
| `cmd/strike` `selectProvider` | `providers/factory` | #1216; `cmd/strike` becomes a thin call |
| `internal/tool` contract/registry/retry | `harness/tool` | #1205 then #1208 |
| `internal/tool` kernel builtins | `harness/tools` | #1205 then #1208 |
| `internal/tool` product builtins | `internal/tools` | #1205; later stays under `internal/tools` |
| `internal/permission` | `harness/permission` | #1208 |
| `internal/actionfacts` | `harness/actionfacts` | #1208 (not trust) |
| `internal/question` | `harness/question` | #1208 |
| `internal/sandbox` | `harness/sandbox` | #1208 |
| `internal/scheduler` | `harness/scheduler` | #1208 |
| `internal/safefile` | `harness/safefile` | #1208 |
| `internal/verify` | `harness/verify` | #1208 |
| `internal/fault` | `harness/fault` | #1208 |
| `internal/tui/_src` flatten → `internal/tui/*.go` | flatten → `internal/tui/app/` | #1209 |
| `internal/tui/{ui,theme,common,term}` | same paths, then `internal/frontend/tui/{…}` | #1209 then #1213 |
| `internal/replay` | `internal/eval/replay` | #1215 |

## Kernel vs product

**Kernel** = shareable agent runtime in the `harness` module. A third-party
module must be able to import it without reaching `internal/`.

**Providers** = sibling module for vendor adapters and reusable auth/API
handling. Not the kernel (a harness consumer should not be forced to take
Anthropic/OpenAI/ChatGPT wire formats).

**Product** = Strike CLI, TUI, stores, cockpit, evals. Stays in the root
module under grouped `internal/` clusters.

Every current top-level `internal/*` package:

| Current package | Destination | Kind |
|---|---|---|
| `internal/acp` | `internal/frontend/acp` | product |
| `internal/actionfacts` | `harness/actionfacts` | kernel |
| `internal/admission` | `internal/trust/admission` | product |
| `internal/artifact` | `internal/persist/artifact` | product |
| `internal/attachment` | `internal/persist/attachment` | product |
| `internal/audit` | `internal/trust/audit` | product |
| `internal/auth` | split: flows → `providers/auth`; store + `strike auth` → `internal/product/auth` | providers + product |
| `internal/config` | `internal/product/config` | product |
| `internal/container` | `internal/integrate/container` | product |
| `internal/engine` | `harness/engine` | kernel |
| `internal/eval` | `internal/eval` (cluster; already) | product |
| `internal/fault` | `harness/fault` | kernel |
| `internal/goal` | `internal/product/goal` | product |
| `internal/fn` | `harness/fn` | kernel |
| `internal/history` | `internal/persist/history` | product |
| `internal/host` | `internal/frontend/host` | product |
| `internal/issue` | `internal/persist/issue` | product |
| `internal/ledger` | `internal/persist/ledger` | product |
| `internal/lsp` | `internal/integrate/lsp` | product |
| `internal/mcp` | `internal/integrate/mcp` | product |
| `internal/memory` | `internal/persist/memory` | product |
| `internal/models` | `internal/product/models` | product |
| `internal/permission` | `harness/permission` | kernel |
| `internal/plan` | `internal/persist/plan` | product |
| `internal/plugin` | `internal/integrate/plugin` | product |
| `internal/project` | `internal/product/project` | product |
| `internal/protocol` | `internal/protocol` (compat re-export; prefer `pkg/protocol`) | product |
| `internal/provider` | split: interface+echo → `harness/provider`; adapters → `providers/` | kernel + providers |
| `internal/question` | `harness/question` | kernel |
| `internal/replay` | `internal/eval/replay` | product |
| `internal/rpc` | `internal/frontend/rpc` | product |
| `internal/safefile` | `harness/safefile` | kernel |
| `internal/sandbox` | `harness/sandbox` | kernel |
| `internal/scheduler` | `harness/scheduler` | kernel |
| `internal/secret` | `internal/trust/secret` | product |
| `internal/security` | `internal/trust/security` | product |
| `internal/server` | `internal/frontend/server` | product |
| `internal/session` | `internal/persist/session` | product |
| `internal/telemetry` | `internal/persist/telemetry` | product |
| `internal/tool` | split: contract → `harness/tool`; kernel builtins → `harness/tools`; product builtins → `internal/tools` | kernel + product |
| `internal/tui` | `internal/frontend/tui` (`app/` + kit) | product |
| `internal/update` | `internal/product/update` | product |
| `internal/verify` | `harness/verify` | kernel |
| `internal/version` | `internal/product/version` | product |

Notable subpackages (not top-level, but they split or move with a parent):

| Current | Destination | Kind |
|---|---|---|
| `internal/fn/external` | `harness/fn/external` | kernel |
| `internal/provider/echo` | `harness/provider` (echo) | kernel |
| `internal/provider/base` | `providers/base` | providers |
| `internal/provider/anthropic` | `providers/anthropic` | providers |
| `internal/provider/openaicompat` | `providers/openaicompat` | providers |
| `internal/provider/chatgpt` | `providers/chatgpt` | providers |
| `internal/provider/google` | `providers/google` | providers |
| `internal/host/local` | `internal/frontend/host/local` | product |
| `internal/tui/ui` | `internal/frontend/tui/ui` (kit; no Logo) | product |
| `internal/tui/theme` | `internal/frontend/tui/theme` | product |
| `internal/tui/common` | `internal/frontend/tui/common` | product |
| `internal/tui/term` | `internal/frontend/tui/term` | product |
| `internal/eval/swebench` | `internal/eval/swebench` | product |
| `internal/eval/sweep` | `internal/eval/sweep` | product |
| `internal/eval/tbench` | `internal/eval/tbench` | product |
| `internal/eval/progressive` | `internal/eval/progressive` | product |

### Tool split (#1205)

Kernel builtins (move with harness): read, glob, grep, edit, write,
`apply_patch`, move, delete, status, bash/process, git, sleep, todo,
webfetch/websearch/browser, task/delegate/wait, `agent_*`, `team_*`,
question, toolsearch, verify-as-tool.

Product builtins (stay in Strike `internal/tools`): `memory_*`, `issue_*`,
`plan_*`, `artifact_*`, `ledger_*`, skill, `context_bundle`,
`notebook_edit`, LSP intel/nav, `tui_snapshot`, enter/exit plan mode,
`phase_done`.

`cmd/strike` registers both sets. Tool names on the wire do not change.

### Auth split (#1216 then #1214)

`providers/auth` owns reusable flows: API-key env order, `BearerSource`,
`ChatGPTSource`, OAuth/PKCE/device, token refresh.

Strike product keeps `auth.Store` (0600 `~/.strike/auth.json`), `strike auth`
CLI, `host.Auth`, and TUI `/auth`. Config JSONC *loading* stays in
`internal/product/config`; the factory consumes already-parsed endpoint
structs. `internal/models` (models.dev catalog) stays product.

### TUI app vs kit (#1209)

- **App** (`internal/tui/app`, later `internal/frontend/tui/app`): Strike
  session UI. Flatten `_src/` here. Owns `Logo` / `LogoCompact`.
- **Kit** (`ui`, `theme`, `common`, `term`): reusable components and tokens.
  Imports only each other, stdlib, and Charm — never `protocol` or `host`.
  No Strike wordmark. No kit `go.mod` this epic.

## What success looks like (epic, not this doc)

- A third-party module can `import` the harness module without reaching
  `internal/`.
- `ls internal/` shows a handful of cluster directories, not ~45 siblings.
- `ls internal/tui/` shows `app/`, `ui/`, `theme/`, `common/`, `term/`.
- `go work` + `replace` keep CI and local `make test` green.
- Protocol event names and config key `harnesses` are unchanged.
