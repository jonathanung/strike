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
hexes and `web/src/styles.css` `:root` pairs must match that file — Go
(`tokens_test.go`) and web (`web/src/theme.test.ts`) fail on drift.

`theme.Default()` **chrome mode** is still `soft` until [#1234](https://github.com/jonathanung/strike/issues/1234)
applies bordered chrome across TUI views. Do not treat current Default chrome
as the visual north star. Web consumes `radiusWebPx` (2px) and stock hexes from
this file via `web/src/stockTokens.ts`. Bundled named themes (nord, …) keep
their own hexes — only stock Default / `:root` use this map.

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
| `soft` | Surface-filled body + rounded box outline (`╭╮╰╯`). Focus is `BorderFocus` outline + title-edge `SurfaceFocus` (no FocusBar). Degrades to plain text when width &lt; 6. **Still `theme.Default()` until #1234.** |
| `solid` | Filled surfaces with title/footer bars. No box-drawing frame. Focus is title-edge `SurfaceFocus` + thin FocusBar. |
| `bordered` | Classic light/heavy box-drawing borders (outline, minimal surface wash). **Token-file north star.** |

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

## Web cockpit parity

The `strike serve` attach UI (`web/src/styles.css`, embedded under
`internal/frontend/server/static`) mirrors the stock token file via CSS
custom properties (dark defaults; light via `prefers-color-scheme: light`).
Semantic roles map as `--ink`←Text, `--muted`←TextMuted, `--ground`←Background,
`--surface`/`--raised`/`--surface-muted`←Surface*, `--rule`←Border,
`--acid`←Accent, `--accent-alt`←AccentAlt, `--signal`←Error, `--danger`←Danger,
`--user`/`--tool`← transcript labels, `--diff-add`/`--diff-del`←diff roles.
`--radius` is the 2px chrome token; stock role hexes are injected from the
token file (`/* strike-stock:dark|light */`). Parity is guarded by
`web/src/theme.test.ts`. The web settings dialog loads the
host theme catalog (`/v1/themes`), supports preview/apply, and maps portable
semantic roles onto CSS custom properties (`web/src/themeCatalog.ts`, WEBUI.11).

See also [ARCHITECTURE.md](ARCHITECTURE.md) (theme tokens recipe),
[web.md](web.md), and the `tui-components` skill catalog.
