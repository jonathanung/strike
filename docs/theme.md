# Theme

Strike's TUI look is owned by `internal/frontend/tui/theme`. Views compose
`theme.Styles` and `internal/frontend/tui/ui` components; they never hardcode colors,
glyphs, or chrome geometry.

## North star: sharp + royal purple

Stock tokens are a **sharp operational chrome** system: royal-purple primary,
square corners, bordered tiles, and bento as a **tiling** layout (not soft
rounding). [VeTool lobbies](https://vetool.jonathanung.ca/lobbies) is a look
reference for operational board chrome — not a clone. Terminal constraints
apply; strike layout/keymaps stay; no web-only effects (real drop shadows,
blur) and **no new idle animation**.

Family soft-rounded cards are no longer the documented default. They remain a
valid `chrome: "soft"` option for named/custom themes.

The machine-readable source of truth is [`schemas/ui-tokens.json`](../schemas/ui-tokens.json):
every semantic role has a light+dark hex, plus chrome defaults
(`mode: bordered`, `corners: square`, `radiusWebPx: 2`). `theme.Default()`
hexes must match that file — `tokens_test.go` fails on drift. The old
`web/src/styles.css` parity check left with the JavaScript UI. The Rust
client stylesheet is `webui/style.css`.

`theme.Default()` chrome is **bordered + square corners**, matching this file.
Bundled named themes (nord, …) keep their own hexes. Theme files may set
`chrome: "soft"` for Family rounded cards.

### Token → hex (light + dark)

| Token | Light | Dark |
|---|---|---|
| Text | `#1a1528` | `#f3f1fa` |
| TextMuted | `#5c586e` | `#9b99b0` |
| Accent | `#5b21b6` | `#7c3aed` |
| AccentAlt | `#0e7490` | `#22d3ee` |
| Highlight | `#4c1d95` | `#ddd6fe` |
| Success | `#15803d` | `#4ade80` |
| Warning | `#b45309` | `#fbbf24` |
| Error | `#e11d48` | `#fb7185` |
| Danger | `#ea580c` | `#fb923c` |
| Background | `#ffffff` | `#14131c` |
| Surface | `#f3eef9` | `#232230` |
| SurfaceFocus | `#e9e0f7` | `#2e2c3e` |
| SurfaceMuted | `#f8f5fc` | `#1a1924` |
| Border | `#c4bfd4` | `#4f4d63` |
| BorderFocus | `#5b21b6` | `#7c3aed` |
| BorderMuted | `#ddd8ea` | `#2c2a3a` |
| UserLabel | `#0e7490` | `#22d3ee` |
| ToolLabel | `#2563eb` | `#7dd3fc` |
| DiffAdded | `#15803d` | `#4ade80` |
| DiffRemoved | `#e11d48` | `#fb7185` |
| OverlayScrim | `#a8a3b8` | `#7c7a90` |

Accent is royal (`#5b21b6` / `#7c3aed`), not pastel `#c4b5fd`. Spacing defaults
are unchanged (`XS=1`, `SM=2`, …). Left|right pane gutter stays `XS` so the
canonical 93-col split (`60+gutter+32`) remains intact; breathing room comes
from bento `SM` gaps between tiles, not rounded card chrome. Stock badges are
delimiter-free pills on `SurfaceMuted`.

### Role semantics

| Role | Intent |
|---|---|
| **Accent** | Royal-purple primary emphasis (titles, assistant, focus border) |
| **AccentAlt** / **UserLabel** | Cyan secondary / "you" transcript label |
| **ToolLabel** | Sky blue tool-call label |
| **Success** / **DiffAdded** | Mint positive / added |
| **Warning** | Amber caution / needs-you |
| **Error** / **DiffRemoved** | Coral failure / removed |
| **Danger** | Orange destructive actions — **distinct from Error** |
| **Highlight** | Selected / active item foreground (distinct from Accent) |

### Information colors (TUI + web)

Same *role* on both surfaces — never one purple for working, needs-you, and
error. Hexes stay in the token file; this table is the meaning contract.

| Role | Meaning | TUI | Web CSS |
|---|---|---|---|
| Ready / idle | Awaiting input | `AgentStateReady` → `Success` | gateway `.chip.ok` → `--success` |
| Working | Turn or tool in flight | `AgentStateWorking` → `AccentAlt` | `.badge-running`, `.gutter-running` → `--accent-alt` |
| Needs you | Permission, question, or gate | `AgentStateAttention` → `Warning` | `.badge-blocked`, `.badge-stalled` → `--warning` |
| Error | Failed turn, tool, or provider | `AgentStateError` → `Error` | `.badge-failed` → `--signal` |
| Success | Complete / ok | `Success` | `.badge-done`, `.badge-completed` → `--success` |
| Danger | Destructive action | `Danger` | `--danger`, distinct from `--signal` |
| User | "You" transcript label | `UserLabel` | `.role-you` → `--user` |
| Tool | Tool-call transcript label | `ToolLabel` | `.role-tool`, `.badge-tool` → `--tool` |
| Selected | Cursor / active item | `Highlight` foreground | `.session-row.on` uses `--raised` and `--border-focus` |
| Focus | Focused pane / control | `BorderFocus` + title-edge `SurfaceFocus` | `:focus-visible` outline `--border-focus` |
| Stopped | Canceled or unknown | `TextMuted` | `.badge-stopped`, `.badge-canceled` → `--muted` |

Permission/question wins over busy (TUI `agentState`, web `liveStatusKind`).
Canceled / unknown stay muted (`TextMuted` / `--muted`).

### Surface ladder

`background` < `surfaceMuted` < `surface` < `surfaceFocus` — enough step that
tiles remain distinct under 256-color quantization, not only in truecolor.

### SSH / tmux acceptance

- Judge contrast and role separation on `TERM=tmux-256color` and
  `screen-256color` over SSH, not only local GPU truecolor terminals.
- Lip Gloss degrades hex adaptive pairs for non-truecolor profiles; every
  critical role must remain distinguishable after 256-color quantization
  (prefer hues that land on distinct xterm-256 buckets).
- Spot-check truecolor local still looks good (more colorful is OK).
- No idle full-frame animation or rainbow noise on idle redraw.
- Pure string UI; no full-tree restyle per frame.

### Motion budget

Region-scoped animation only (header spinner, focus via outline / title edge
+ FocusBar, badge/meter updates, short copied-flash on transcript cells) —
invalidate cached regions, never recompose the full transcript every tick.
Existing `paint_budget` (~6 FPS soft coalesce) and `frame_cache` patterns are
the model. Correctness over delight on low-FPS remote. **No new animation**
for this token-contract pass.

### Chrome density (sharp tiling)

Hierarchy comes from surface step, royal Accent, and bordered outlines — not
soft rounding. Header drops lowest-priority badges under width pressure
(think → effort → phase → health-dot first). Composer and right-pane footers
use `KeyHints`; welcome empty state is a bento of tiled `Panel` cards (no
outer welcome frame). Dialogs stay elevated (`SurfaceFocus`) with optional
tone chrome for warning/danger.

## Chrome mode

Panels (transcript, composer, side panes, dialogs, bento cards) paint through
`ui.Panel`. The theme `chrome` field selects how that chrome looks:

| Value | Behavior |
|---|---|
| `soft` | Surface-filled body + rounded box outline (`╭╮╰╯`). Focus is `BorderFocus` outline + title-edge `SurfaceFocus` (no FocusBar). Degrades to plain text when width &lt; 6. Opt-in via `chrome: "soft"`. |
| `solid` | Filled surfaces with title/footer bars. No box-drawing frame. Focus is title-edge `SurfaceFocus` + thin FocusBar. |
| `bordered` | Classic light/heavy box-drawing borders (outline, square `┌┐└┘` light corners, minimal surface wash). **`theme.Default()` / token-file north star.** |

JSON theme files:

```json
{
  "id": "my-theme",
  "chrome": "bordered",
  "border": "light",
  "colors": {
    "background": { "light": "#ffffff", "dark": "#14131c" },
    "surface": { "light": "#f3eef9", "dark": "#232230" },
    "surfaceFocus": { "light": "#e9e0f7", "dark": "#2e2c3e" },
    "surfaceMuted": { "light": "#f8f5fc", "dark": "#1a1924" }
  }
}
```

`border` (`light` | `heavy`) affects glyph choice when `chrome` is `soft` or
`bordered`.

## Surfaces and canvas

- `background` — application fill, painted last by `ui.Canvas`
- `surface` / `surfaceFocus` / `surfaceMuted` — panel fills
- Nested surface backgrounds survive the canvas pass; canvas restores its
  background only after SGR clears (reset / default background)
- Modals still scrim the frame with `overlayScrim` via `ui.Scrim` /
  `ui.OverlayCenter`

## Soft pills (badges)

`ui.Badge` paints a tone-colored label on `SurfaceMuted` with XS horizontal
pad. Stock `Icons.BadgeLeft` / `BadgeRight` are empty so chips read as pills
without heavy `[` `]` weight. Themes may restore bracket delimiters via
JSON icons.

## Loading themes

Merge order (later wins on the same theme id — [plugins.md](plugins.md) §4.1):

1. Bundled JSON under `internal/frontend/tui/theme/themes/` (`builtin`)
2. `~/.strike/themes` (`user`)
3. Global plugin contributions under `~/.strike/plugins/*/…` (`plugin:<id>`)
4. `./.strike/themes` (`project`)
5. Project plugin contributions under `./.strike/plugins/*/…` (`plugin:<id>`)

Invalid theme JSON, malformed plugins, disabled lockfile entries, and install
staging dirs are **skipped** — they must not break startup or silently shadow a
winner. Collisions surface in `/theme` as `over <previous-provenance>`.

Pick with `/theme` or `config.theme`. In the picker: cursor movement **previews**
without writing config; **enter** applies the session theme; **ctrl+d** saves
the default; **esc** always reverts to the theme that was active when the
picker opened. Install/update theme *plugins* through the generic plugin
lifecycle (`/plugin` or `strike plugin`) — same catalog integrity and lockfile
path as other contributions; there is no separate theme marketplace.

## Web client

The JavaScript cockpit is gone. `webui/style.css` mirrors
[`schemas/ui-tokens.json`](../schemas/ui-tokens.json). `theme.Default()` is the
same hexes (`tokens_test.go`). `TestWebUIStyleMirrorsUITokens` fails if a role's
light hex, dark hex, or `cssVar` drops out of the stylesheet.

Dark Strike is `:root`. Light Strike is `[data-scheme="light"]`. The command
palette writes `strike.theme` and `strike.scheme` (`strike`, `nord`, `dracula`,
`gruvbox`, `monokai`, `tokyo-night`, `catppuccin`, plus dark or light). An
inline boot script sets the attributes before paint. Bundled theme JSON lives
in `internal/frontend/tui/theme/themes/`. Roles a file omits — surface,
surfaceFocus, surfaceMuted, overlayScrim — stay on the Strike ladder for the
active scheme, which is what `theme.Resolve()` does. `--on-accent` is not a TUI
role. It is the label color on a filled accent control.

The TUI north star stays square (`radiusWebPx: 2`, no drop shadows). The
cockpit uses a 10px panel radius and a pill command field so panels read as
separate surfaces. That radius is a web layout exception. Colors are not.

| TUI token | Light | Dark | CSS variable |
|---|---|---|---|
| Text | `#1a1528` | `#f3f1fa` | `--ink` |
| TextMuted | `#5c586e` | `#9b99b0` | `--muted` |
| Accent | `#5b21b6` | `#7c3aed` | `--acid` |
| AccentAlt | `#0e7490` | `#22d3ee` | `--accent-alt` |
| Highlight | `#4c1d95` | `#ddd6fe` | `--highlight` |
| Success | `#15803d` | `#4ade80` | `--success` |
| Warning | `#b45309` | `#fbbf24` | `--warning` |
| Error | `#e11d48` | `#fb7185` | `--signal` |
| Danger | `#ea580c` | `#fb923c` | `--danger` |
| Background | `#ffffff` | `#14131c` | `--ground` |
| Surface | `#f3eef9` | `#232230` | `--surface` |
| SurfaceFocus | `#e9e0f7` | `#2e2c3e` | `--raised` |
| SurfaceMuted | `#f8f5fc` | `#1a1924` | `--surface-muted` |
| Border | `#c4bfd4` | `#4f4d63` | `--rule` |
| BorderFocus | `#5b21b6` | `#7c3aed` | `--border-focus` |
| BorderMuted | `#ddd8ea` | `#2c2a3a` | `--border-muted` |
| UserLabel | `#0e7490` | `#22d3ee` | `--user` |
| ToolLabel | `#2563eb` | `#7dd3fc` | `--tool` |
| DiffAdded | `#15803d` | `#4ade80` | `--diff-add` |
| DiffRemoved | `#e11d48` | `#fb7185` | `--diff-del` |
| OverlayScrim | `#a8a3b8` | `#7c7a90` | `--overlay` |

Working status is AccentAlt, not Success. A blocked handoff is a gate, so it
uses Warning. Failed uses Error. Stopped uses TextMuted.

See also [ARCHITECTURE.md](ARCHITECTURE.md) (theme tokens recipe) and
[webui-daemon.md](webui-daemon.md).
