# Theme

Strike's TUI look is owned by `internal/tui/theme`. Views compose
`theme.Styles` and `internal/tui/ui` components; they never hardcode colors,
glyphs, or chrome geometry.

## North star palette (E13.8)

Stock `theme.Default()` is a **soft-bento multi-accent** system: dark-first
ground, raised soft cards, and semantic accents that stay a bit more colorful
than the visual refs while remaining legible over SSH + tmux.

### Visual refs

- https://cdn.dribbble.com/userupload/45035686/file/14b3c8318ecd928a77915bd6a629c11e.png?format=webp&resize=400x300&vertical=center
- https://miro.medium.com/1*pzGevugpNDXXUOCRtAfhsA.png

Take from refs: dark ground, raised soft cards, multi-accent semantic colors
(purple / blue / coral / green / yellow family), calm typography, separation by
surface step more than heavy boxes. Push slightly more colorful accents than
the refs. Not a clone — terminal constraints; strike layout/keymaps stay; no
web-only effects (real drop shadows, blur).

### Token → hex (light + dark)

| Token | Light | Dark |
|---|---|---|
| Text | `#1a1528` | `#f3f1fa` |
| TextMuted | `#5c586e` | `#9b99b0` |
| Accent | `#6d28d9` | `#c4b5fd` |
| AccentAlt | `#0e7490` | `#22d3ee` |
| Highlight | `#5b21b6` | `#f5f3ff` |
| Success | `#15803d` | `#4ade80` |
| Warning | `#b45309` | `#fbbf24` |
| Error | `#e11d48` | `#fb7185` |
| Danger | `#ea580c` | `#fb923c` |
| Background | `#ffffff` | `#14131c` |
| Surface | `#f3eef9` | `#232230` |
| SurfaceFocus | `#e9e0f7` | `#2e2c3e` |
| SurfaceMuted | `#f8f5fc` | `#1a1924` |
| Border | `#c4bfd4` | `#4f4d63` |
| BorderFocus | `#6d28d9` | `#c4b5fd` |
| BorderMuted | `#ddd8ea` | `#2c2a3a` |
| UserLabel | `#0e7490` | `#22d3ee` |
| ToolLabel | `#2563eb` | `#7dd3fc` |
| DiffAdded | `#15803d` | `#4ade80` |
| DiffRemoved | `#e11d48` | `#fb7185` |
| OverlayScrim | `#a8a3b8` | `#7c7a90` |

Chrome mode defaults to `solid`; spacing and `DefaultIcons` are unchanged.
Bundled named themes (nord, …) keep their own hexes — only `Default()` uses
this map.

### Role semantics

| Role | Intent |
|---|---|
| **Accent** | Violet primary emphasis (titles, assistant, focus border) |
| **AccentAlt** / **UserLabel** | Cyan secondary / "you" transcript label |
| **ToolLabel** | Sky blue tool-call label |
| **Success** / **DiffAdded** | Mint positive / added |
| **Warning** | Amber caution / needs-you |
| **Error** / **DiffRemoved** | Coral failure / removed |
| **Danger** | Orange destructive actions — **distinct from Error** |

### Surface ladder

`background` < `surfaceMuted` < `surface` < `surfaceFocus` — enough step that
solid panels read as soft tiles under 256-color quantization, not only in
truecolor.

### SSH / tmux acceptance

- Judge contrast and role separation on `TERM=tmux-256color` and
  `screen-256color` over SSH, not only local GPU truecolor terminals.
- Lip Gloss degrades hex adaptive pairs for non-truecolor profiles; every
  critical role must remain distinguishable after 256-color quantization
  (prefer hues that land on distinct xterm-256 buckets).
- Spot-check truecolor local still looks good (more colorful is OK).
- No idle full-frame animation or rainbow noise on idle redraw.

### Motion budget

Region-scoped animation only (header spinner, focus pulse via solid title edge
+ FocusBar, badge/meter updates, short copied-flash on transcript cells) —
invalidate cached regions, never recompose the full transcript every tick.
Existing `paint_budget` (~6 FPS soft coalesce) and `frame_cache` patterns are
the model. Correctness over delight on low-FPS remote.

### Chrome density (soft-bento hierarchy)

Solid surfaces + multi-accent badges/labels carry hierarchy more than heavy
boxes. Header clusters status badges by semantic tone; composer and right-pane
footers use `KeyHints`; welcome empty state is a bento of solid `Panel` cards
(no outer welcome frame). Dialogs stay elevated (`SurfaceFocus`) with optional
tone chrome for warning/danger.

## Chrome mode

Panels (transcript, composer, side panes, dialogs, bento cards) paint through
`ui.Panel`. The theme `chrome` field selects how that chrome looks:

| Value | Behavior |
|---|---|
| `solid` (default) | Filled surfaces (`surface` / `surfaceFocus` / `surfaceMuted`) with title and footer bars. No box-drawing frame. Focus is a surface/emphasis change. |
| `bordered` | Classic light/heavy box-drawing borders (`border` glyph weight). |

JSON theme files:

```json
{
  "id": "my-theme",
  "chrome": "solid",
  "border": "light",
  "colors": {
    "background": { "light": "#ffffff", "dark": "#14131c" },
    "surface": { "light": "#f3eef9", "dark": "#232230" },
    "surfaceFocus": { "light": "#e9e0f7", "dark": "#2e2c3e" },
    "surfaceMuted": { "light": "#f8f5fc", "dark": "#1a1924" }
  }
}
```

`border` (`light` | `heavy`) only affects glyph choice when `chrome` is
`bordered`.

## Surfaces and canvas

- `background` — application fill, painted last by `ui.Canvas`
- `surface` / `surfaceFocus` / `surfaceMuted` — solid panel fills
- Nested surface backgrounds survive the canvas pass; canvas restores its
  background only after SGR clears (reset / default background)
- Modals still scrim the frame with `overlayScrim` via `ui.Scrim` /
  `ui.OverlayCenter`

## Loading themes

Bundled JSON under `internal/tui/theme/themes/`, then `~/.strike/themes`, then
`./.strike/themes`. Pick with `/theme` or `config.theme`.

## Web cockpit parity

The `strike serve` attach UI (`web/src/styles.css`, embedded under
`internal/server/static`) mirrors the stock `theme.Default()` palette via CSS
custom properties (dark defaults; light via `prefers-color-scheme: light`).
Semantic roles map as `--ink`←Text, `--muted`←TextMuted, `--ground`←Background,
`--surface`/`--raised`/`--surface-muted`←Surface*, `--rule`←Border,
`--acid`←Accent, `--accent-alt`←AccentAlt, `--signal`←Error, `--danger`←Danger,
`--user`/`--tool`← transcript labels, `--diff-add`/`--diff-del`←diff roles.
Parity is guarded by `web/src/theme.test.ts`. User-selected TUI JSON themes are
not yet applied to the web UI.

See also [ARCHITECTURE.md](ARCHITECTURE.md) (theme tokens recipe),
[web.md](web.md), and the `tui-components` skill catalog.
