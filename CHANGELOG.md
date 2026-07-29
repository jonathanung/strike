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

### Changed

- System telemetry (CPU/RAM/disk pane) is on by default again; disable with `/telemetry off` ([#485](https://github.com/jonathanung/strike/issues/485)).

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

[Unreleased]: https://github.com/jonathanung/strike/compare/v0.0.11...HEAD
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
