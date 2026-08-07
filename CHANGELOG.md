# Changelog

All notable user-facing changes to strike are documented in this file.

This changelog follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are
listed newest first. Each release uses only the `Added`, `Changed`,
`Deprecated`, `Removed`, `Fixed`, and `Security` categories that apply.
Entries describe user impact rather than commit activity, group related work,
and link the relevant pull requests where practical. Breaking behavior or
required migration steps are called out as an **Upgrade note** under
`Changed`. Tests, refactors, and documentation changes appear only when they
materially affect the shipped product.

## [Unreleased]

### Added

- **Admission scan for MCP, skills, and plugins** — register/load-time
  scanners apply a severity→action matrix (`allow` / `warn` / `block` /
  `quarantine`) before MCP tools bind or skills enter the catalog. Config
  `admission.preset` (`permissive` \| `default` \| `strict`), home-anchored
  `allowPaths` only (bare relative markers rejected — spoof-via-subdirectory
  regression tested), and explicit fail-closed on `strict`. Emits
  `admission.decided` (protocol `1.14.0`) for timeline/audit. Shared
  `internal/security.Finding` types for future write-time content guards.
  Docs: [docs/admission.md](docs/admission.md)
  ([#889](https://github.com/jonathanung/strike/issues/889)).
- **Tool-chain correlation** — content-free multi-step permission correlation
  within a turn: sensitive read → network/bash, write executable → bash
  execute, and identical denial retry storms. Matches **ask** or **deny** with
  explainable chain summaries (tool names/classes only); `chainId` on
  `permission.decided` and timeline entries. State clears on turn end/interrupt
  and caps pending nodes ([#891](https://github.com/jonathanung/strike/issues/891)).
- **Container runtime foundation (E12.0)** — `internal/container` shells out to
  `docker`/`podman` via an injectable `ExecFunc` (no Moby SDK). Low-level
  `Runtime` (pull/create/start/stop/rm/exec/cp), deterministic
  `strike-<repo>-<hash>` names, and `com.strike.*` labels. Decision and boundary
  documented in `docs/container.md`
  ([#582](https://github.com/jonathanung/strike/issues/582)).
- **Plugin theme contributions** — theme packages load through the plugin
  catalog/lifecycle (same lockfile and integrity path). `/theme` shows plugin
  provenance and collision winners, live-previews on cursor move without
  persisting, applies on enter, reverts on esc, and saves defaults with
  ctrl+d. Invalid/disabled/staging plugin themes are skipped so startup cannot
  break ([#511](https://github.com/jonathanung/strike/issues/511)).
- **TUI plugin manager** — `/plugin` opens a centered manager over
  `host.Plugins`: browse installed plugins (version, source, status, trust,
  contribution counts), inspect capabilities/findings, catalog search/install,
  enable/disable, update with review, and remove/trust/untrust with explicit
  confirmation. Executable trust review names commands and contribution types;
  no secret or env values are rendered; failed ops preserve prior state
  ([#730](https://github.com/jonathanung/strike/issues/730)).
- **SWE-bench Verified subset runner (E3.3)** — `strike eval swebench` runs a
  fixed 50-instance internal regression subset with Docker per instance and
  `strike exec --json`, recording pass rate, tokens, cost, and wall-clock to
  versioned `report.json` / predictions JSONL under `evals/swebench/results/`.
  Internal signal only — do not publish pass rates in the README
  ([#561](https://github.com/jonathanung/strike/issues/561)).
- **Plugin catalog and updates** — remote catalog format (`catalog.json`),
  `strike plugin search` / `install catalog:pkg[@ver] --registry` / `outdated` /
  `update --yes`. Installs pin immutable version + verified artifact digest;
  lockfile records registry/package/version/URL/digests for reproduce; archive
  extract guards zip-slip/tar traversal; failed download/verify/validate keeps
  the prior version; contribution/capability review before update; executable
  changes clear prior trust. Catalog metadata cannot enable execution
  ([#729](https://github.com/jonathanung/strike/issues/729)).
- **`websearch` tool** — permissioned, provider-neutral web search with
  citation-ready titles/URLs/snippets, domain filters, result limits, and
  network allowlist / redaction / audit controls. Separate from `webfetch`
  (discover vs retrieve). Configure via `webSearch` (`provider: brave`,
  `apiKeyEnv`, optional `baseURL`); missing backend returns structured setup
  guidance. Default permission is ask; read-only preset denies it
  ([#882](https://github.com/jonathanung/strike/issues/882)).
- **Plugin lifecycle CLI** — `strike plugin` list/inspect/install/enable/disable/remove/doctor
  for local paths and Git sources. Atomic install into global or project scope,
  pinned Git commits in `plugins.lock.json` (source + digest, never credentials),
  disable preserves files, remove requires `--yes`, doctor reports paths without
  secrets or env values ([#727](https://github.com/jonathanung/strike/issues/727)).
- **Delegation-worthiness policy** — before `task` / `delegate` create, a deterministic gate prefers local execution for bare tiny or path-overlapping work and denies fan-out past hard ceilings (depth, optional max live children, delegation count, session budget hook). Config `session.delegationPolicy` (`mode` off|soft|enforce, thresholds). Soft local is overridable with `force_delegate`; decisions expose `policyReason` on tool metadata and `child.started`. Orchestrator guidance has a single pre-spawn decision table ([#876](https://github.com/jonathanung/strike/issues/876)).
- **Passive plugin load** — enabled bundles under `~/.strike/plugins/<id>/` and
  `./.strike/plugins/<id>/` contribute agents, skills, workflows, themes, and
  provider profiles through existing surfaces. Manifest validation, path
  confinement, strike/schema version checks, collision diagnostics, and
  `plugins.lock.json` disablement; executable MCP/harness/hook entries stay
  inactive until trusted activation ([#726](https://github.com/jonathanung/strike/issues/726)).

## [v0.2.2] - 2026-08-06

Patch release: session scratch temp, harness tool broker, partial child handoffs on soft budget stop, diagnostics tool, config JSON Schema, and plugin contract docs. Protocol wire `1.12.0`.

### Added

- **Session temporary directory** — each engine session gets a private `os.TempDir()/strike/<session-id>/` scratch root. Path tools (`write` / `edit` / `apply_patch` / `notebook_edit`) may write absolute paths under that root (siblings, `..`, and symlink escapes stay denied). The path is exposed in the environment prompt; the directory is removed on `Run` shutdown with bounded stale cleanup for crashes. Relative paths still bind only to the workspace ([#877](https://github.com/jonathanung/strike/issues/877), [#884](https://github.com/jonathanung/strike/pull/884)).
- **Harness tool execution** — custom function harnesses can run allowed tools mid-turn through the Strike runtime (`Input.Tools.Execute` embedded; additive `tool.execute` / `tool.result` on the external JSONL ABI) with the same permissions, hooks, sandbox, scheduler, redaction, and protocol events as the built-in loop. Final `harness.Result` remains the only committed assistant response. Go/TypeScript/Lean SDKs updated; provider-only Lean harnesses stay arity-compatible ([#878](https://github.com/jonathanung/strike/issues/878), [#885](https://github.com/jonathanung/strike/pull/885)).
- **Partial child handoffs on budget exhaustion** — soft per-child budgets (`tool_calls`, `tokens`, `wall_clock`, `cost_usd`, `dangerous_tools`, hard `stall`/`loop`) attempt one tools-disabled finalization turn (~45s reserve) so children can return structured findings before stop. `ChildCompleted` records `budgetKind` and finalization outcome; handoff `quality` is `complete` | `partial` | `unavailable`. Engine-tracked files and artifact refs always merge into the handoff. Hard cancel skips finalization ([#879](https://github.com/jonathanung/strike/issues/879), [#886](https://github.com/jonathanung/strike/pull/886)).
- **`diagnostics` tool** — read-only, deferred model-facing workspace diagnostics backed by the LSP manager (workspace / directory / file scopes, severity filter, bounded stable JSON). Soft structured status when servers are missing or down; allow-by-default including the read-only permission preset ([#880](https://github.com/jonathanung/strike/issues/880), [#898](https://github.com/jonathanung/strike/pull/898)).
- **Main config JSON Schema** — versioned `schemas/strike-config.schema.json` for editor autocomplete/validation of high-traffic config keys. Point `$schema` at the raw GitHub URL; Strike ignores `$schema` at runtime (no fetch). `additionalProperties: true` so unknown/future keys stay valid ([#873](https://github.com/jonathanung/strike/issues/873), [#900](https://github.com/jonathanung/strike/pull/900)).
- **Plugin bundle contract (docs)** — normative PLUGIN.1 contract in `docs/plugins.md` (manifest, contribution matrix, trust/digest, path confinement, secrets). Loaders and install UX are not shipped yet ([#725](https://github.com/jonathanung/strike/issues/725), [#899](https://github.com/jonathanung/strike/pull/899)).

### Fixed

- Fixed live sessions losing their scratch temp dir when peer `EnsureSessionTemp` ran stale cleanup after >24h idle on disk by refreshing mtime (at most hourly) on ensure hits ([#877](https://github.com/jonathanung/strike/issues/877), [#884](https://github.com/jonathanung/strike/pull/884)).
- Fixed macOS sandbox still blocking credential-backed CLIs (Keychain / SecurityServer / trustd) after the initial Keychain allowlist ([#887](https://github.com/jonathanung/strike/pull/887)).

**Full changelog:** [v0.2.0...v0.2.2](https://github.com/jonathanung/strike/compare/v0.2.0...v0.2.2)

## [v0.2.0] - 2026-08-06

Minor milestone: OS sandbox and scheduler, first-run onboarding, public protocol/SDK surfaces, LSP, multi-agent orchestration harness, plans and workflows, and enterprise settings.

### Added

- **OS sandbox for bash** — Linux `bwrap` / macOS `sandbox-exec` isolation with config/CLI dial `sandbox`: `off` | `read-only` | `workspace-write` (default `workspace-write`; `--sandbox`, `/sandbox`). Permission deny globs compile into OS filesystem denials; `/sandbox explain` shows the generated profile. Structured denials, process rlimits, and an isolation matrix document the two-dial model with `permissionMode`. `yolo` with `sandbox: off` requires `--i-know` ([#551](https://github.com/jonathanung/strike/issues/551), [#552](https://github.com/jonathanung/strike/issues/552), [#553](https://github.com/jonathanung/strike/issues/553), [#799](https://github.com/jonathanung/strike/issues/799)).
- **Network allowlist** — config `network.allow` (global/project) whitelists hosts, `*.suffix` wildcards, IPs, and CIDRs for `webfetch`. Empty means unrestricted public hosts (SSRF private blocks unchanged). Shown in `/sandbox explain` ([#527](https://github.com/jonathanung/strike/issues/527)).
- **Named-pool scheduler** — fair cancellable admission for model streams and agent bash, layered limits, command classification, build-system presets (CMake, Ninja, Gradle, Bazel, Maven, Cargo, npm/yarn/pnpm/bun), and queue state on the protocol/TUI ([#706](https://github.com/jonathanung/strike/issues/706)–[#711](https://github.com/jonathanung/strike/issues/711)).
- **`/ftue` onboarding** — setup wizard (provider, model, optional `/init`), contextual feature tour, optional scheduler preset step, and one-shot auto-open for clean installs via `~/.strike/onboarding.json`. Established installs are not surprised; `exec`/`auth`/`serve` skip onboarding ([#702](https://github.com/jonathanung/strike/issues/702)–[#705](https://github.com/jonathanung/strike/issues/705)).
- **Public protocol package and clients** — `pkg/protocol` Op/Event wire schema (through `1.11.0`), Go SDK (`pkg/sdk`), `strike rpc` stdio JSON-RPC, `strike acp` Agent Client Protocol adapter, `strike mcp-serve` (`strike_task`), and `strike exec --json` / `--output-format` envelopes. Unknown event types decode as `UnknownEvent` for forward-compat; normative [docs/protocol.md](docs/protocol.md) ([#564](https://github.com/jonathanung/strike/issues/564)–[#569](https://github.com/jonathanung/strike/issues/569), [#811](https://github.com/jonathanung/strike/issues/811)).
- **LSP** — JSON-RPC client, extension registry, diagnostics injection into file-tool results, default servers, `/lsp`, diagnostics pane, and optional definition/references/symbols nav tools ([#555](https://github.com/jonathanung/strike/issues/555)–[#558](https://github.com/jonathanung/strike/issues/558)).
- **Multi-agent orchestration harness** — atomic delegation lifecycle (`delegate` + `task` criteria/deps/subscribe; states through review/done), structured child handoffs, agent-to-agent contracts (`require_ack`, urgency, `agent_thread`), event subscriptions/`wait`, independent verification gates and claim-vs-verified attach, per-agent budgets, capability-aware routing, path-ownership overlap detection, patch-level collaboration, scoped context bundles, typed shared artifacts, and a decision/assumption ledger ([#770](https://github.com/jonathanung/strike/issues/770)–[#782](https://github.com/jonathanung/strike/issues/782), [#806](https://github.com/jonathanung/strike/issues/806)).
- **Cancellation, deadlines, retry, and tool contracts** — stable `canceled`/`timeout`/`queue_full` codes, turn timeouts, process-group kill, tool error recovery/retry policy, strong side-effect/idempotency contracts, and FS transaction safety (`baseHash`, atomic writes, turn diffs) ([#793](https://github.com/jonathanung/strike/issues/793)–[#797](https://github.com/jonathanung/strike/issues/797), [#795](https://github.com/jonathanung/strike/issues/795)).
- **Plans and workflows** — root-owned plan domain with `plan_read`/`plan_write`, editable plans pane, plan-mode handoff gate, section delegate through team runtime; workflow schema v1, lifecycle/resume, autonomy-authoritative gates, phase permission review, catalog/activation UX, model-generated drafts, visual builder, and web authoring parity ([#712](https://github.com/jonathanung/strike/issues/712)–[#724](https://github.com/jonathanung/strike/issues/724), [#719](https://github.com/jonathanung/strike/issues/719)).
- **Run timeline, sessions, and diagnostics** — structured run timeline with redacted export, storage bounds/blob spill/trace retention; durable JSONL append, schema header, export/import, retention; deterministic recording, branch-from-event, run compare, multi-agent snapshots; prompt/config diagnostic bundles; shared secret redaction and secret-ref env indirection ([#790](https://github.com/jonathanung/strike/issues/790)–[#792](https://github.com/jonathanung/strike/issues/792), [#796](https://github.com/jonathanung/strike/issues/796), [#803](https://github.com/jonathanung/strike/issues/803), [#810](https://github.com/jonathanung/strike/issues/810)).
- **Context controls** — token-by-source visibility, pin/exclude, fit warnings; compaction with structured provenance residue ([#802](https://github.com/jonathanung/strike/issues/802), [#804](https://github.com/jonathanung/strike/issues/804)).
- **Permissions and settings** — explain/scopes/presets/audit trail; `/settings` ports for compaction/prune, `permissionAutoApprove`, `maxChildDepth`, and other high-value dials; main config JSONC + optional `$schema`; managed/MDM config layer for enterprise policy ([#761](https://github.com/jonathanung/strike/issues/761)–[#764](https://github.com/jonathanung/strike/issues/764), [#798](https://github.com/jonathanung/strike/issues/798), [#509](https://github.com/jonathanung/strike/issues/509)).
- **Harness trust and undo UX** — TUI harness trust/control surfaces; `/undo` previews harness paths from the last turn, surfaces checkpoint-skipped counts, and warns on uncovered bash mutations (`SessionRewound` carries restored paths + `uncovered`). Full bash snapshot coverage remains [#572](https://github.com/jonathanung/strike/issues/572); checkpoint stack across `--continue` remains [#573](https://github.com/jonathanung/strike/issues/573) ([#801](https://github.com/jonathanung/strike/issues/801), [#809](https://github.com/jonathanung/strike/issues/809)).
- **TUI polish** — queue browser for buffered prompts, richer DiffPreview (LCS/word-diff/gutters), `/pets` companion pane, hide file bodies from chat for reads and `@` mentions, `toolsearch` quoted phrases ([#525](https://github.com/jonathanung/strike/issues/525), [#524](https://github.com/jonathanung/strike/issues/524), [#395](https://github.com/jonathanung/strike/issues/395), [#746](https://github.com/jonathanung/strike/issues/746), [#8](https://github.com/jonathanung/strike/issues/8)).
- **Session replay and harness eval** — deterministic replay against the echo provider, prompt regression suite, and harness evaluation/regression tracking ([#559](https://github.com/jonathanung/strike/issues/559), [#560](https://github.com/jonathanung/strike/issues/560), [#807](https://github.com/jonathanung/strike/issues/807)).

### Changed

- **Upgrade note:** Bash runs under the OS sandbox by default (`sandbox: workspace-write`). Set `sandbox: off` (or `--sandbox off`) to restore unsandboxed shell; combining `yolo` with sandbox off requires `--i-know` ([#552](https://github.com/jonathanung/strike/issues/552)).
- **Bash sandbox network default** — host networking stays on under default permissions so `gh`, `git`, and package managers work; network isolation applies only when both `webfetch` and `mcp` are hard-deny on `*`. Air-gap is opt-in via `NoNetwork` on the sandbox policy ([#750](https://github.com/jonathanung/strike/issues/750)).
- **FTUE build presets** — scheduler preset rows use `[x]` / `[ ]` checkbox marks so selected vs unselected tools are obvious ([#747](https://github.com/jonathanung/strike/issues/747)).

### Fixed

- Fixed default OS sandbox / bash path guard blocking normal shell use: redirects, temp-dir writes, and tool caches (`~/.cache`, Go/npm/cargo) are shared-writable while the workspace stays isolated ([#752](https://github.com/jonathanung/strike/issues/752)).
- Fixed bash under the default OS sandbox failing DNS/network (`gh auth status`, `git push`, etc.) by keeping host networking as the policy zero value ([#750](https://github.com/jonathanung/strike/issues/750)).
- Fixed macOS sandbox blocking Keychain access needed for credentials and system tools ([#868](https://github.com/jonathanung/strike/pull/868)).
- Fixed multi-root agent order jumping when switching sessions ([#865](https://github.com/jonathanung/strike/issues/865), [#869](https://github.com/jonathanung/strike/pull/869)).
- Hardened path-mutation writes against TOCTOU races and closed cheap holes in the destructive bash guard ([#549](https://github.com/jonathanung/strike/issues/549)–[#551](https://github.com/jonathanung/strike/issues/551), [#699](https://github.com/jonathanung/strike/pull/699)).

### Security

- OS-level isolation for bash (filesystem + optional network), permission-compiled deny profiles, webfetch host allowlists, shared credential redaction on exports/traces, and secret-ref env indirection reduce accidental credential and workspace leakage ([#551](https://github.com/jonathanung/strike/issues/551), [#527](https://github.com/jonathanung/strike/issues/527), [#796](https://github.com/jonathanung/strike/issues/796)).

**Full changelog:** [v0.1.2...v0.2.0](https://github.com/jonathanung/strike/compare/v0.1.2...v0.2.0)

## [v0.1.2] - 2026-08-05

Patch release focused on TUI composer input and live subagent transcript stability.

### Changed

- **Upgrade note:** Default tool-apply keybind is now `alt+a` (was bare `a`/`A`). Override `nav.tool-apply` in `keybinds.jsonc` to restore the previous binding ([#693](https://github.com/jonathanung/strike/issues/693), [#694](https://github.com/jonathanung/strike/pull/694)).

### Fixed

- Fixed bare `a`/`A` being captured as tool-apply instead of typing in the chat composer when a tool cell was selected ([#693](https://github.com/jonathanung/strike/issues/693), [#694](https://github.com/jonathanung/strike/pull/694)).
- Fixed focusing a running subagent corrupting the live transcript and multi-pane layout while the child continued streaming ([#692](https://github.com/jonathanung/strike/issues/692), [#695](https://github.com/jonathanung/strike/pull/695)).
- Reduced repeated disk I/O when listing sessions via a short-lived in-memory cache ([#619](https://github.com/jonathanung/strike/issues/619), [#696](https://github.com/jonathanung/strike/pull/696)).

## [v0.1.1] - 2026-08-01

Patch release focused on TUI layout stability and launch-screen usability.

### Fixed

- Fixed lean launch-screen autocomplete suggestions appearing far above the prompt on tall terminals ([#688](https://github.com/jonathanung/strike/issues/688), [#690](https://github.com/jonathanung/strike/pull/690)).
- Fixed Egyptian hieroglyphs and other historic scripts disrupting multi-column TUI layout when rendered in terminals that display them double-wide ([#689](https://github.com/jonathanung/strike/issues/689), [#691](https://github.com/jonathanung/strike/pull/691)).

## [v0.1.0] - 2026-07-31

First minor milestone: agent teams, layout polish, Charm v2 TUI stack, and safer session defaults.

### Added

- Added **agent teams**: an implicit session-scoped team (lead + `task` children in the same session tree) with `agent_roster`, `agent_message`, and `agent_broadcast` for mid-turn peer coordination, boundary-safe mailbox delivery, and default-allow permissions (out-of-team fails closed). Optional stable `name` aliases on `task` spawn. Shared `team_task` board with claim/CAS for multi-agent work items. Parent-only `task_*` workflows remain unchanged when team tools are unused ([#404](https://github.com/jonathanung/strike/issues/404), [#607](https://github.com/jonathanung/strike/issues/607)–[#616](https://github.com/jonathanung/strike/issues/616), [#644](https://github.com/jonathanung/strike/pull/644)).
- Added team roster and message surfaces in the agents and activity panes ([#642](https://github.com/jonathanung/strike/pull/642)).
- Added a pre-first-prompt **home layout** (centered wordmark and prompt, thin context bar) that switches to the multi-pane workspace after the first transcript cell ([#677](https://github.com/jonathanung/strike/issues/677), [#682](https://github.com/jonathanung/strike/pull/682)).
- Added content-aware flex sizing for stacked right panes so sparse panes (context, system) shrink to content and activity fills the remainder ([#680](https://github.com/jonathanung/strike/issues/680), [#682](https://github.com/jonathanung/strike/pull/682)).
- Added stronger prompt chrome: composer mode title, focused glyph, and send-state / image / queue / pending-approval chips ([#678](https://github.com/jonathanung/strike/issues/678), [#682](https://github.com/jonathanung/strike/pull/682)).
- Added context-sensitive footer hints (composer vs right-pane navigation) instead of one overloaded keybind sentence ([#679](https://github.com/jonathanung/strike/issues/679), [#682](https://github.com/jonathanung/strike/pull/682)).
- Added `ctrl+shift+o` / `ctrl+shift+p` (and `/group-next` / `/group-prev`) to cycle right-pane **stack groups** ([#671](https://github.com/jonathanung/strike/issues/671), [#676](https://github.com/jonathanung/strike/pull/676)).
- Added Family-style soft rounded bento chrome and a calmer Default palette density ([#648](https://github.com/jonathanung/strike/pull/648), [#654](https://github.com/jonathanung/strike/pull/654), [#656](https://github.com/jonathanung/strike/pull/656)).
- Added terminal background detection so adaptive theme colors follow the host terminal ([#643](https://github.com/jonathanung/strike/pull/643)).
- Added task **function harnesses** with Go, TypeScript, and Lean SDKs plus examples for custom turn-loop controllers ([#627](https://github.com/jonathanung/strike/pull/627)).

### Changed

- **Upgrade note:** `session.worktree` defaults to `off` again (launch cwd). Isolated git worktrees are opt-in via `session.worktree` `auto`/`always` or `strike --worktree`. This reverses the v0.0.14 default of always-on worktrees ([#672](https://github.com/jonathanung/strike/issues/672), [#674](https://github.com/jonathanung/strike/pull/674)).
- Migrated the TUI stack to Charm v2 (Bubble Tea, Lip Gloss, Bubbles, Glamour) with adaptive theme colors ([#636](https://github.com/jonathanung/strike/pull/636), [#640](https://github.com/jonathanung/strike/pull/640), [#646](https://github.com/jonathanung/strike/pull/646), [#647](https://github.com/jonathanung/strike/pull/647)).
- Refactored the experimental web cockpit workspace panels for clearer attach UX ([#622](https://github.com/jonathanung/strike/pull/622)).

### Fixed

- Fixed web cockpit attach token auth: opening `/attach?token=…` sets an HttpOnly cookie and redirects so subsequent API/SSE/WebSocket calls authenticate without leaving the secret in the address bar ([#662](https://github.com/jonathanung/strike/issues/662), [#673](https://github.com/jonathanung/strike/pull/673)).
- Fixed sibling `agent_message` delivery when models address teammates by unique short session-id prefixes ([#650](https://github.com/jonathanung/strike/issues/650), [#681](https://github.com/jonathanung/strike/pull/681)).
- Restored bash access for the default `general` persona when spawned as a task child ([#651](https://github.com/jonathanung/strike/issues/651), [#668](https://github.com/jonathanung/strike/pull/668)).
- Soft-failed session worktree bind outside git repositories instead of hard-erroring on launch ([#661](https://github.com/jonathanung/strike/issues/661), [#667](https://github.com/jonathanung/strike/pull/667)).
- Limited left-pane focus highlight to the prompt box rather than the whole column ([#663](https://github.com/jonathanung/strike/issues/663), [#666](https://github.com/jonathanung/strike/pull/666)).
- Clarified the activity empty state (no idle keybind noise) and visualizer metric (tokens/turn) ([#669](https://github.com/jonathanung/strike/issues/669), [#670](https://github.com/jonathanung/strike/issues/670), [#675](https://github.com/jonathanung/strike/pull/675)).
- Preserved composer drafts across palette actions and restored embedded Vim truecolor / terminal color queries ([#653](https://github.com/jonathanung/strike/pull/653), [#657](https://github.com/jonathanung/strike/pull/657), [#658](https://github.com/jonathanung/strike/pull/658), [#660](https://github.com/jonathanung/strike/pull/660)).
- Showed bang/shell tool activity immediately in the activity pane ([#631](https://github.com/jonathanung/strike/pull/631)).
- Treated Linux idle/iowait regression as busy CPU so telemetry no longer reports 0% under heavy I/O ([#630](https://github.com/jonathanung/strike/pull/630)).
- Allowed child-agent permission allow to upgrade a parent ask without failing closed incorrectly ([#668](https://github.com/jonathanung/strike/pull/668)).

**Contributors:** [@jonathanung](https://github.com/jonathanung), [@FrederickPu](https://github.com/FrederickPu), [@dayvidpham](https://github.com/dayvidpham), and [@NicholasTamm](https://github.com/NicholasTamm).

**Full changelog:** [v0.0.14...v0.1.0](https://github.com/jonathanung/strike/compare/v0.0.14...v0.1.0)

## [v0.0.14] - 2026-07-30

### Added

- Added an interactive keybind editor with default and override columns, conflict warnings, save-or-discard prompts, and reset support through `/keys` and the command palette ([#603](https://github.com/jonathanung/strike/pull/603), [#606](https://github.com/jonathanung/strike/pull/606), [#617](https://github.com/jonathanung/strike/pull/617)).
- Added a session switcher with filtering, numbered root shortcuts, and `ctrl+n` for starting a new session, plus mouse-click pane focus ([#526](https://github.com/jonathanung/strike/pull/526), [#618](https://github.com/jonathanung/strike/pull/618), [#623](https://github.com/jonathanung/strike/pull/623)).
- Added `strike restore` to recreate missing or corrupt `.strike` metadata without touching sessions or other durable data ([#535](https://github.com/jonathanung/strike/pull/535)).
- Added dedicated layered `keybinds.jsonc` and `mcp.jsonc` project and global configuration files ([#518](https://github.com/jonathanung/strike/pull/518), [#519](https://github.com/jonathanung/strike/pull/519)).
- Added support for following directory and file symlinks for global and project strike state ([#532](https://github.com/jonathanung/strike/pull/532)).

### Changed

- **Upgrade note:** New sessions in Git repositories now use an isolated git worktree by default. Set `session.worktree` to `off` or `auto` to retain the previous behavior ([#520](https://github.com/jonathanung/strike/pull/520)).
- Permission modes can now change during an active turn, allowing `/mode`, `/mode-next`, and `Shift+Tab` to take effect without interrupting it ([#533](https://github.com/jonathanung/strike/pull/533)).

### Fixed

- Corrected macOS CPU and memory telemetry, excluded reclaimable file cache from memory pressure, made cache display neutral, and hid the swap row when no swap is configured ([#534](https://github.com/jonathanung/strike/pull/534), [#601](https://github.com/jonathanung/strike/pull/601), [#604](https://github.com/jonathanung/strike/pull/604), [#605](https://github.com/jonathanung/strike/pull/605)).
- Corrected ChatGPT subscription prompt-cache request fields, session routing, and cache usage reporting ([#536](https://github.com/jonathanung/strike/pull/536)).

**Contributors:** [@jonathanung](https://github.com/jonathanung), [@tianyaohu](https://github.com/tianyaohu), and [@NicholasTamm](https://github.com/NicholasTamm).

**Full changelog:** [v0.0.12...v0.0.14](https://github.com/jonathanung/strike/compare/v0.0.12...v0.0.14)

## [v0.0.12] - 2026-07-29

This is the first release orchestrated by strike itself.

### Added

- Added executable and embedded Go function harnesses for custom turn-loop controllers ([#483](https://github.com/jonathanung/strike/pull/483)).
- Added configurable tool-result pruning thresholds and protected tools ([#501](https://github.com/jonathanung/strike/pull/501)).
- Added estimated system, tool, message, and tool-result token attribution to `/context` ([#507](https://github.com/jonathanung/strike/pull/507)).
- Added prompt cache keys and cached-token usage reporting for OpenAI-compatible providers ([#505](https://github.com/jonathanung/strike/pull/505)).

### Changed

- System telemetry (CPU/RAM/disk pane) is on by default again; disable with `/telemetry off` ([#486](https://github.com/jonathanung/strike/pull/486)).
- Reduced TUI rendering work during streaming and over SSH through incremental viewport rendering, redraw coalescing, static SSH working chrome, and unchanged-pane caching ([#499](https://github.com/jonathanung/strike/pull/499), [#504](https://github.com/jonathanung/strike/pull/504), [#506](https://github.com/jonathanung/strike/pull/506), [#508](https://github.com/jonathanung/strike/pull/508)).

### Fixed

- Prevented SGR mouse sequences from leaking into the prompt ([#487](https://github.com/jonathanung/strike/pull/487)).
- Fixed rename-modal caret placement and space input ([#503](https://github.com/jonathanung/strike/pull/503)).
- Rendered completed Markdown correctly after switching back to a background agent ([#500](https://github.com/jonathanung/strike/pull/500)).

**Contributors:** [@FrederickPu](https://github.com/FrederickPu) and [@jonathanung](https://github.com/jonathanung).

**Full changelog:** [v0.0.11...v0.0.12](https://github.com/jonathanung/strike/compare/v0.0.11...v0.0.12)

## [v0.0.11] - 2026-07-28

### Added

- Added the Tokyo Night built-in theme palette ([#480](https://github.com/jonathanung/strike/pull/480)).

### Fixed

- Stopped continuous idle spinner ticks and files-pane polling during the welcome/init screen so SSH sessions only repaint on real events ([#482](https://github.com/jonathanung/strike/pull/482)).

**Contributors:** [@jonathanung](https://github.com/jonathanung).

**Full changelog:** [v0.0.10...v0.0.11](https://github.com/jonathanung/strike/compare/v0.0.10...v0.0.11)

## [v0.0.10] - 2026-07-28

### Fixed

- Advertised `xterm-256color` to embedded editors and cleared inherited `COLORTERM`, so Vim and similar tools pick a compatible palette inside the TUI ([#478](https://github.com/jonathanung/strike/pull/478)).

**Contributors:** [@dayvidpham](https://github.com/dayvidpham).

**Full changelog:** [v0.0.9...v0.0.10](https://github.com/jonathanung/strike/compare/v0.0.9...v0.0.10)

## [v0.0.9] - 2026-07-28

### Changed

- **Upgrade note:** Renamed the canonical Google AI Studio provider id from `gemini` to `google`. Existing `gemini` CLI, config, session, agent, environment, and stored-credential paths remain compatible and migrate to `google` when saved ([#476](https://github.com/jonathanung/strike/pull/476)).

### Fixed

- Fixed Google Gemini requests returning HTTP 400 by sending the required lower-camel-case `generateContent` fields ([#476](https://github.com/jonathanung/strike/pull/476)).
- Colored `/legend` glyph samples with their live theme colors ([#470](https://github.com/jonathanung/strike/pull/470)).
- Made the agent `needs you` state consistently visible in yellow across built-in themes and constrained legend rows ([#473](https://github.com/jonathanung/strike/pull/473), [#474](https://github.com/jonathanung/strike/pull/474), [#477](https://github.com/jonathanung/strike/pull/477)).

**Contributors:** [@jonathanung](https://github.com/jonathanung).

**Full changelog:** [v0.0.8...v0.0.9](https://github.com/jonathanung/strike/compare/v0.0.8...v0.0.9)

## [v0.0.8] - 2026-07-27

### Added

- Added a filterable `/legend` modal that explains TUI icons, agent statuses, and chrome glyphs ([#462](https://github.com/jonathanung/strike/pull/462)).

### Changed

- **Upgrade note:** Gemini authentication now uses API keys only; the unsupported Google OAuth flow was removed. Configure `GEMINI_API_KEY` or `GOOGLE_API_KEY`, or paste a Google AI Studio key when prompted ([#468](https://github.com/jonathanung/strike/pull/468)).

### Fixed

- Preserved whole words when wrapping transcript messages, except when a single token exceeds the available width ([#464](https://github.com/jonathanung/strike/pull/464)).
- Prevented slow network or FUSE disk probes from freezing telemetry updates ([#465](https://github.com/jonathanung/strike/pull/465)).
- Listed Gemini models correctly after API-key authentication and applied their catalog context limits ([#468](https://github.com/jonathanung/strike/pull/468)).

**Contributors:** [@jonathanung](https://github.com/jonathanung).

**Full changelog:** [v0.0.7...v0.0.8](https://github.com/jonathanung/strike/compare/v0.0.7...v0.0.8)

## [v0.0.7] - 2026-07-27

### Added

- Added an installable Nix flake package ([#360](https://github.com/jonathanung/strike/pull/360)).
- Added per-task model selection for subagent spawning ([#364](https://github.com/jonathanung/strike/pull/364)).
- Added modal or embedded Vim and markdown-reader presentation, plus a first-class `/nano` command and `nanoMode` setting ([#366](https://github.com/jonathanung/strike/pull/366), [#367](https://github.com/jonathanung/strike/pull/367)).
- Replaced the experimental web cockpit with a React workspace and added active-agent workspaces and session history ([#359](https://github.com/jonathanung/strike/pull/359), [#372](https://github.com/jonathanung/strike/pull/372)).
- Added Google OAuth and Gemini bearer-token authentication ([#371](https://github.com/jonathanung/strike/pull/371)).
- Added a pluggable engine harness interface for custom turn-loop controllers ([#377](https://github.com/jonathanung/strike/pull/377)).
- Added `/exit` and `/quit`, recurring `/loop` jobs, configurable provider disabling, `@path` arguments in file commands, and persistent defaults in `/settings` ([#389](https://github.com/jonathanung/strike/pull/389), [#397](https://github.com/jonathanung/strike/pull/397), [#398](https://github.com/jonathanung/strike/pull/398), [#401](https://github.com/jonathanung/strike/pull/401), [#405](https://github.com/jonathanung/strike/pull/405)).
- Added user-renamable sessions, `/rewind` session forks, local shell commands with the `!` composer prefix, and last-response copying through `alt+y` or `/copy` ([#396](https://github.com/jonathanung/strike/pull/396), [#415](https://github.com/jonathanung/strike/pull/415), [#417](https://github.com/jonathanung/strike/pull/417), [#416](https://github.com/jonathanung/strike/pull/416)).
- Added effort to the status bar, settings defaults, and task model pins ([#429](https://github.com/jonathanung/strike/pull/429)).
- Added Anthropic prompt-cache breakpoints and deferred tool-schema loading through `toolsearch` ([#440](https://github.com/jonathanung/strike/pull/440), [#445](https://github.com/jonathanung/strike/pull/445)).
- Made `/model` list models from every authenticated provider ([#458](https://github.com/jonathanung/strike/pull/458)).

### Changed

- **Upgrade note:** Default keybindings changed for pane focus, newlines, and tool expansion. See [`docs/keybinds.md`](https://github.com/jonathanung/strike/blob/v0.0.7/docs/keybinds.md) or `/keys` in the TUI ([#423](https://github.com/jonathanung/strike/pull/423), [#427](https://github.com/jonathanung/strike/pull/427), [#428](https://github.com/jonathanung/strike/pull/428)).
- System telemetry is now opt-in and disabled by default ([#430](https://github.com/jonathanung/strike/pull/430)).
- Slash commands can map to keybind actions and jump directly to named panes ([#409](https://github.com/jonathanung/strike/pull/409), [#411](https://github.com/jonathanung/strike/pull/411)).
- Reduced model-token usage by pruning old tool results, compacting always-on schemas and guidance, tightening tool output caps, and compacting context earlier with configurable thresholds ([#433](https://github.com/jonathanung/strike/pull/433), [#441](https://github.com/jonathanung/strike/pull/441), [#442](https://github.com/jonathanung/strike/pull/442), [#444](https://github.com/jonathanung/strike/pull/444), [#443](https://github.com/jonathanung/strike/pull/443)).
- Improved multi-question navigation, confirmation, and batch progression ([#457](https://github.com/jonathanung/strike/pull/457)).

### Fixed

- Prevented concurrent global-config writes from losing settings ([#370](https://github.com/jonathanung/strike/pull/370)).
- Continued DeepSeek conversations correctly after image messages ([#376](https://github.com/jonathanung/strike/pull/376)).
- Permission, plan, or question rejection now interrupts the active turn reliably, and `esc` reliably interrupts in-flight turns ([#391](https://github.com/jonathanung/strike/pull/391), [#418](https://github.com/jonathanung/strike/pull/418)).
- Bash tool calls now restore the session root instead of inheriting a previous call's changed directory ([#403](https://github.com/jonathanung/strike/pull/403)).
- Preserved input typed in the embedded editor ([#431](https://github.com/jonathanung/strike/pull/431)).

### Security

- Enforced a hard workspace sandbox around destructive tool operations ([#393](https://github.com/jonathanung/strike/pull/393)).

**Contributors:** [@dayvidpham](https://github.com/dayvidpham), [@NicholasTamm](https://github.com/NicholasTamm), and [@jonathanung](https://github.com/jonathanung).

**Full changelog:** [v0.0.6...v0.0.7](https://github.com/jonathanung/strike/compare/v0.0.6...v0.0.7)

## [v0.0.6] - 2026-07-26

### Added

- Added the `/goal` loop harness with persistent goals, guards, critics, hooks, and JSONL state ([#355](https://github.com/jonathanung/strike/pull/355)).

### Fixed

- Corrected OpenCode-compatible base URL joining and `@ai-sdk/openai` Responses API wire handling ([#357](https://github.com/jonathanung/strike/pull/357)).
- Preserved empty ChatGPT tool outputs so conversations can continue correctly ([#358](https://github.com/jonathanung/strike/pull/358)).

**Contributors:** [@NicholasTamm](https://github.com/NicholasTamm) and [@jonathanung](https://github.com/jonathanung).

**Full changelog:** [v0.0.5...v0.0.6](https://github.com/jonathanung/strike/compare/v0.0.5...v0.0.6)

## [v0.0.5] - 2026-07-26

### Added

- Added a control for hiding completed agents from the agents pane without deleting their sessions ([#350](https://github.com/jonathanung/strike/pull/350)).

### Fixed

- Corrected `providers.jsonc` endpoint overlays and model ID wiring for built-in providers ([#353](https://github.com/jonathanung/strike/pull/353)).

**Contributors:** [@tianyaohu](https://github.com/tianyaohu) and [@jonathanung](https://github.com/jonathanung).

**Full changelog:** [v0.0.4...v0.0.5](https://github.com/jonathanung/strike/compare/v0.0.4...v0.0.5)

## [v0.0.4] - 2026-07-26

### Added

- Added Kimi and DeepSeek as built-in API-key providers ([#338](https://github.com/jonathanung/strike/pull/338)).
- Added a saved default permission mode in the TUI and the `--auto` alias for `--dangerously-skip-permissions` ([#334](https://github.com/jonathanung/strike/pull/334), [#347](https://github.com/jonathanung/strike/pull/347)).
- Added post-plan routing to the build or orchestrator agent and the ability to apply a displayed edit or patch from the diff viewer ([#340](https://github.com/jonathanung/strike/pull/340), [#342](https://github.com/jonathanung/strike/pull/342)).
- Added rich nested model objects to `providers.jsonc` ([#348](https://github.com/jonathanung/strike/pull/348)).

### Fixed

- Wrapped modal body and question-option text to remain readable at narrow terminal widths ([#341](https://github.com/jonathanung/strike/pull/341)).

**Contributors:** [@tianyaohu](https://github.com/tianyaohu) and [@jonathanung](https://github.com/jonathanung).

**Full changelog:** [v0.0.3...v0.0.4](https://github.com/jonathanung/strike/compare/v0.0.3...v0.0.4)

## [v0.0.3] - 2026-07-26

### Added

- Added macOS clipboard screenshot attachment with bounded image conversion ([#320](https://github.com/jonathanung/strike/pull/320)).
- Added Nano as an external-editor fallback and made `/keys` context-aware for the focused pane ([#328](https://github.com/jonathanung/strike/pull/328), [#331](https://github.com/jonathanung/strike/pull/331)).
- Added the issue-creation workflow for turning reports into assigned implementation work ([#329](https://github.com/jonathanung/strike/pull/329)).

### Changed

- `strike --upgrade` now installs the update without automatically relaunching strike ([#321](https://github.com/jonathanung/strike/pull/321)).

### Fixed

- Corrected `providers.jsonc` parsing, environment references, and custom-provider logout cleanup ([#325](https://github.com/jonathanung/strike/pull/325)).
- Corrected bare-LF `ctrl+j` handling so it cycles panes rather than inserting a newline ([#330](https://github.com/jonathanung/strike/pull/330)).

**Contributors:** [@NicholasTamm](https://github.com/NicholasTamm) and [@jonathanung](https://github.com/jonathanung).

**Full changelog:** [v0.0.2...v0.0.3](https://github.com/jonathanung/strike/compare/v0.0.2...v0.0.3)

## [v0.0.2] - 2026-07-26

### Added

- Added agent-scoped lean-code guidance and dynamic tool guidance in base system prompts ([#272](https://github.com/jonathanung/strike/pull/272), [#306](https://github.com/jonathanung/strike/pull/306)).
- Added stacked side-pane groups and system telemetry charts for CPU, memory, and disk usage ([#274](https://github.com/jonathanung/strike/pull/274), [#291](https://github.com/jonathanung/strike/pull/291)).
- Added streamable HTTP MCP transport with retry and disable controls ([#275](https://github.com/jonathanung/strike/pull/275)).
- Expanded the web cockpit with live operations, WebSockets, permissions, and token-guarded LAN access through `strike serve --expose` ([#276](https://github.com/jonathanung/strike/pull/276), [#279](https://github.com/jonathanung/strike/pull/279)).
- Added peer-ecosystem skills and a review-fix workflow ([#277](https://github.com/jonathanung/strike/pull/277)).
- Added provider logout confirmation, a 15-second soft-approve permission mode, and model-facing task status/read/message/interrupt tools ([#281](https://github.com/jonathanung/strike/pull/281), [#308](https://github.com/jonathanung/strike/pull/308), [#311](https://github.com/jonathanung/strike/pull/311)).

### Changed

- Switched the TUI to solid-surface chrome and refined pane focus, selection, and modal presentation ([#280](https://github.com/jonathanung/strike/pull/280), [#283](https://github.com/jonathanung/strike/pull/283), [#290](https://github.com/jonathanung/strike/pull/290)).
- Collapsed subagent results into transcript sections and placed the newest activity first ([#309](https://github.com/jonathanung/strike/pull/309), [#310](https://github.com/jonathanung/strike/pull/310)).

### Fixed

- Made `@file` and `@folder` mentions and indexing reliable ([#273](https://github.com/jonathanung/strike/pull/273)).
- Prevented modal surface bleed and queued blocking prompts behind active user overlays ([#286](https://github.com/jonathanung/strike/pull/286), [#304](https://github.com/jonathanung/strike/pull/304)).
- Fixed telemetry compilation on macOS and removed sleep-poll spam while waiting for subagents ([#293](https://github.com/jonathanung/strike/pull/293), [#307](https://github.com/jonathanung/strike/pull/307)).

**Contributors:** [@NicholasTamm](https://github.com/NicholasTamm) and [@jonathanung](https://github.com/jonathanung).

**Full changelog:** [v0.0.1...v0.0.2](https://github.com/jonathanung/strike/compare/v0.0.1...v0.0.2)

## [v0.0.1] - 2026-07-26

Initial public release.

### Added

- Added the Go/Bubble Tea coding-agent TUI with streamed Markdown, structured tool cells, edit previews, responsive panes, themes, remappable keybindings, mouse support, external editors, and file or image attachments.
- Added Anthropic, OpenAI, ChatGPT, xAI, and custom OpenAI-compatible providers with browser, device, API-key, and stored-credential authentication flows.
- Added the engine turn loop, plan and workflow phases, concurrent and nested subagents, model reasoning controls, retries, history compaction, cancellation, and tool-result feedback.
- Added built-in filesystem, search, patch, shell, process, task, web, question, todo, memory, issue, notebook, skill, and planning tools.
- Added scoped permission rules and modes, child-agent permission ceilings, trusted hooks, file checkpoints, and undo restoration.
- Added persistent JSONL sessions with resume, fork, undo, search, rename, export, concurrent roots, and git-worktree isolation.
- Added project memory and issue stores, built-in agents and shipping skills, stdio MCP support, and `.claude` or `.opencode` agent and skill loading.
- Added `strike exec`, installation archives and checksums, the install and upgrade commands, and an experimental read-only web attach server.

**Contributors:** [@jonathanung](https://github.com/jonathanung).

**Full changelog:** [commits through v0.0.1](https://github.com/jonathanung/strike/commits/v0.0.1)

[Unreleased]: https://github.com/jonathanung/strike/compare/v0.2.2...HEAD
[v0.2.2]: https://github.com/jonathanung/strike/compare/v0.2.0...v0.2.2
[v0.2.0]: https://github.com/jonathanung/strike/compare/v0.1.2...v0.2.0
[v0.1.2]: https://github.com/jonathanung/strike/compare/v0.1.1...v0.1.2
[v0.1.1]: https://github.com/jonathanung/strike/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/jonathanung/strike/compare/v0.0.14...v0.1.0
[v0.0.14]: https://github.com/jonathanung/strike/compare/v0.0.12...v0.0.14
[v0.0.12]: https://github.com/jonathanung/strike/compare/v0.0.11...v0.0.12
[v0.0.11]: https://github.com/jonathanung/strike/compare/v0.0.10...v0.0.11
[v0.0.10]: https://github.com/jonathanung/strike/compare/v0.0.9...v0.0.10
[v0.0.9]: https://github.com/jonathanung/strike/compare/v0.0.8...v0.0.9
[v0.0.8]: https://github.com/jonathanung/strike/compare/v0.0.7...v0.0.8
[v0.0.7]: https://github.com/jonathanung/strike/compare/v0.0.6...v0.0.7
[v0.0.6]: https://github.com/jonathanung/strike/compare/v0.0.5...v0.0.6
[v0.0.5]: https://github.com/jonathanung/strike/compare/v0.0.4...v0.0.5
[v0.0.4]: https://github.com/jonathanung/strike/compare/v0.0.3...v0.0.4
[v0.0.3]: https://github.com/jonathanung/strike/compare/v0.0.2...v0.0.3
[v0.0.2]: https://github.com/jonathanung/strike/compare/v0.0.1...v0.0.2
[v0.0.1]: https://github.com/jonathanung/strike/commits/v0.0.1
