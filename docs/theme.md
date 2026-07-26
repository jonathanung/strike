# Theme

Strike's TUI look is owned by `internal/tui/theme`. Views compose
`theme.Styles` and `internal/tui/ui` components; they never hardcode colors,
glyphs, or chrome geometry.

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
    "background": { "light": "#ffffff", "dark": "#1c1b22" },
    "surface": { "light": "#f3f1f8", "dark": "#252430" },
    "surfaceFocus": { "light": "#ebe6f8", "dark": "#2f2c3c" },
    "surfaceMuted": { "light": "#f7f6fb", "dark": "#21202a" }
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

See also [ARCHITECTURE.md](ARCHITECTURE.md) (theme tokens recipe) and the
`tui-components` skill catalog.
