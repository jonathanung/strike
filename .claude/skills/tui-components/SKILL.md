---
name: tui-components
description: Use when building or restyling strike's TUI — views, panels, modals, pickers, badges, dashboards, status rows, or any layout work in internal/tui. Covers the internal/tui/ui component catalog and internal/tui/theme tokens. Do not use for backend/engine work (internal/engine, internal/provider, internal/tool, internal/permission, internal/session, internal/auth, internal/host) — those packages have no UI surface.
---

# TUI components (strike-cli)

`internal/tui/theme` is the resolved token source and `internal/tui/ui` is the
component library. Root views compose completed theme styles and `ui`
components, then perform only structural operations (joining, allocation,
selection, and layout). Keep this catalog synchronized with `internal/tui/ui`.

## Boundaries

- `internal/tui/...` may import only `internal/protocol`, `internal/host`,
  `internal/tui/...`, stdlib, and third-party packages. The boundary test
  enforces this.
- `internal/tui/ui` may import only stdlib, lipgloss, bubbles,
  `charmbracelet/x/ansi`, and `internal/tui/theme`; components do not import
  the app model or handle `tea.Msg`.
- Resolve a supplied theme before reading tokens: `th = th.Resolve()`. Colors,
  glyphs, borders, spacing, and emphasis come only from that resolved theme.
  Never put literal colors, visual glyphs, border/spacing values, or visual
  modifiers in a root view. A `ui` visual-modifier argument must trace to a
  resolved theme value; manually review unknown or interprocedural origins.
- Components are pure renderers: exported rendering functions take a
  `theme.Theme` and options and return a string. State and key handling remain
  in `internal/tui` model files.

## Component catalog

`Tone` is `ToneDefault`, `ToneAccent`, `ToneAccentAlt`, `ToneSuccess`,
`ToneWarning`, `ToneError`, or `ToneMuted`. `Level` is `LevelInfo`,
`LevelSuccess`, `LevelWarning`, or `LevelError`.

| Component | Exact signature | Use |
|---|---|---|
| Panel | `Panel(th theme.Theme, opts PanelOpts, body string) string` | Width-safe framed tile. `PanelOpts` has `Title`, `Footer`, mandatory `Width`, optional `Height`, `Focused`, `Dim`, `Tone`, and `Borderless`. Default chrome is **solid**. Focused solid panes: body stays `Surface`, title edge uses `SurfaceFocus`, leading `BorderFocus` bar in the left pad column — never a full-panel wash. `TextSelection` is a separate role. Tone dialogs keep elevated `SurfaceFocus` body. `chrome: bordered` uses box-drawing + `BorderFocus`. `Borderless` omits chrome. |
| Panel geometry | `InnerWidth(width int) int`; `PanelInnerWidth(th theme.Theme, width int) int`; `PanelInnerHeight(width, height int) int`; `PanelContentOrigin(th theme.Theme, width int) (x, y int)` | Body dimensions and content-cell origin under chrome. Themed callers must use `PanelInnerWidth`; `InnerWidth` is default-theme compatibility only. `PanelInnerHeight` clamps nonpositive dimensions and removes two rows only when chrome fits; narrow panels and fixed heights below two retain full height. |
| Dialog | `Dialog(th theme.Theme, opts DialogOpts, body string) string` | Focused Panel with a muted final hint. `DialogOpts`: `Title`, `Hint`, `Width`, `Height`, `Tone`. |
| Badge | `Badge(th theme.Theme, tone Tone, text string) string` | Token-sized bracketed status chip. |
| KeyHints | `KeyHints(th theme.Theme, width int, hints []KeyHint) string` | Width-safe footer hints. `KeyHint` has `Key`, `Label`. |
| StatusBar | `StatusBar(th theme.Theme, width int, left, right string) string` | One exactly-width row with left/right content. |
| List | `List(th theme.Theme, opts ListOpts) string` | Unframed picker body. `ListOpts`: `Items []ListItem`, `Cursor`, `Width`, `Visible`, `ShowFilter`, `Filter`, `Total`, `Empty`; `ListItem`: `Label`, `Detail`, `Current`, `Disabled`. |
| Tree | `Tree(th theme.Theme, opts TreeOpts) string` | Unframed expand/collapse tree body. `TreeOpts`: `Nodes []TreeNode`, `Cursor`, `Width`, `Visible`, `Empty`; `TreeNode`: `ID`, `Label`, `Detail`, `Children`, `Expanded`, `Lazy`, `Leaf`, `Disabled`, `Current`, `Tone`. Helpers: `FlattenTree`, `TreeNodeAt`, `TreeToggleExpanded`. Indent uses `Spacing.SM`; expand glyphs are `Icons.TreeExpanded` / `TreeCollapsed`. |
| Notice | `Notice(th theme.Theme, level Level, text string, width int) string` | One-line, level-colored feedback. |
| Bento | `Bento(th theme.Theme, width int, cards []Card) string` | Card packer. `Card` has `Title`, `Footer`, `Body`, `Width`, `Tone`; its body wraps to `PanelInnerWidth(th, Width)`. Bento derives its inter-card gap from resolved `th.Spacing.SM`. |
| Overlay | `OverlayCenter(th theme.Theme, bg, fg string, width, height int) string`; `Scrim(th theme.Theme, s string) string`; `ModalWidth(screenWidth int) int` | ANSI-aware centered overlay: scrims bg via `OverlayScrim`, pads every bg row to exact width (no bright spill strip), keeps fg sharp. Standalone `Scrim`; dialog width `min(72, screenWidth-4)`. |
| Canvas | `Canvas(th theme.Theme, width, height int, body string) string` | Final full-screen fit operation and owner of the application background. |
| Logo | `Logo(th theme.Theme) string`; `LogoCompact(th theme.Theme) string` | Full and compact Strike wordmarks. |
| DiffPreview | `DiffPreview(th theme.Theme, opts DiffPreviewOpts) string` | Unified +/-/context diff block. `DiffPreviewOpts`: `Path`, `Old`, `New`, `MaxLines` (hunk body; ≤0 → 12), mandatory `Width` (≤0 → ""), `ShowStats`, optional `MoreHint` (appended on overflow, e.g. expand key). Header (path and/or +N/−M) does not consume `MaxLines`; overflow ends with a muted ellipsis more-lines row. Helpers: `DiffBodyLen`, `DiffExceeds`. |
| Meter | `Meter(th theme.Theme, width int, ratio float64) string` | Fixed-width ratio bar. Negative ratio = unknown hollow (MeterEmpty). |
| Sparkline | `Sparkline(th theme.Theme, width int, samples []float64) string` | Fixed-width activity chart from non-negative samples. Empty/unknown series draws hollow MeterEmpty row; glyphs from `Icons.Sparkline`. |
| FormatTokens | `FormatTokens(n int) string` | Compact token count for chrome (`1.5k`, `2M`). |

Panel and Dialog truncate long body lines; pre-wrap them to
`ui.PanelInnerWidth(th, width)`. `Card` is the exception: it wraps its own
body. Size panel-backed child windows with `ui.PanelInnerHeight(width, height)`
rather than unconditionally subtracting border rows, so unbordered narrow
panels retain their full height.

## Theme tokens

`Theme.Resolve` fills every unset role from `theme.Default()`. It preserves
only `theme.NoBackground()` as the explicit transparent-background choice;
otherwise `Background` resolves to a solid `lipgloss.TerminalColor`.

| Token | Role |
|---|---|
| `Text`, `TextMuted` | primary and secondary foreground |
| `OverlayScrim` | de-emphasized modal background (scrim) fill |
| `Accent`, `AccentAlt`, `Highlight` | primary, secondary, and selected emphasis |
| `Success`, `Warning`, `Error`, `Danger` | semantic state colors |
| `Background` | application background (`lipgloss.TerminalColor`) |
| `Surface`, `SurfaceFocus`, `SurfaceMuted` | solid panel fills: body default / title-edge focus (and tone dialogs) / dim |
| `Border`, `BorderFocus`, `BorderMuted` | bordered-chrome frame colors |
| `UserLabel`, `ToolLabel`, `DiffAdded`, `DiffRemoved` | transcript and diff roles |
| `Chrome` | `ChromeSolid` (default) or `ChromeBordered` |
| `BorderStyle` | bordered-chrome panel border weight and six glyphs |
| `Spacing` | `None`, `XS`, `SM`, `MD`, `LG` layout gaps; `Label` is the gap between a numbered permission-choice shortcut (for example, `1)`) and its label, defaulting to `1` when resolved |
| `Icons` | glyph set below |
| `AgentState` | runtime status coloring via `Theme.AgentStateColor` / `AgentStateStyle` (not a palette field) |

`Icons` fields are `Prompt`, `Assistant`, `Tool`, `OK`, `Err`, `Info`,
`Agent`, `Bolt`, `Dot`, `Cursor`, `InputCursor`, `FilterCursor`, `ToolGuide`,
`FocusBar`,
`BadgeLeft`, `BadgeRight`, `DetailSeparator`, `Ellipsis`, `LogoTopRule`,
`LogoBottomRule`, `MeterFill`, `MeterEmpty`, `TreeExpanded`,
`TreeCollapsed`, and `Sparkline` (low→high bar runes). Use `th.Icons`, never
the literal glyph.

`theme.AgentState` is the live session/agent status vocabulary for dynamic
coloring: `Ready` → `Success`, `Working` → `AccentAlt`, `Attention` →
`Warning`, `Error` → `Error`, and reserved `Dead` → `TextMuted` (unmapped by
reducers until dead-session lifecycle exists). Use
`th.AgentStateColor`/`AgentStateStyle`/`AgentStateStrongStyle`; never hardcode
state colors in views. `Spinner` uses the working token (`AccentAlt`).

`th.S()` returns semantic styles: `Text`, `Muted`, `Accent`, `AccentAlt`,
`Title`, `Success`, `Warning`, `Error`, `Danger`; their `*Strong` variants;
`Selected`, `SelectedUnderline`; `UserLabel`, `AssistantLabel`, `ToolLabel`;
`Input`, `InputPrompt`, `InputPlaceholder`, `InputCursor`, `Spinner`;
`Border`, `BorderFocus`, `BorderMuted`; and `DiffAdded`, `DiffRemoved`. Call
it once per render and reuse it.

`BorderStyle` selects `BorderWeightUnset`, `BorderWeightLight`, or
`BorderWeightHeavy`; resolving selects the matching preset and fills invalid
or missing one-cell glyphs. `NewSpacing(xs, sm, md, lg) Spacing` initializes
`XS`, `SM`, `MD`, and `LG`, including explicit zero values. It does not
initialize `Label`, so `Theme.Resolve` supplies the default `Label` value of
`1`. `WithXS(v int) Spacing`, `WithSM(v int) Spacing`, `WithMD(v int) Spacing`,
`WithLG(v int) Spacing`, and `WithLabel(v int) Spacing` set their respective
token and preserve an explicit zero. Literal nonzero fields, including
`Label`, are inferred as set; use the constructor or corresponding `With`
method when an explicit zero must be preserved.

Configure Bubble widgets from the resolved foreground/cursor/spinner styles
and glyphs, without setting widget backgrounds. `ui.Canvas` owns background
painting after root views and overlays compose, filling every cell unless the
theme explicitly uses `theme.NoBackground()`.

## Recipes

```go
body := ui.List(th, ui.ListOpts{
    Items: items, Cursor: m.cursor,
    Width: max(1, ui.PanelInnerWidth(th, width)),
    Visible: providerModalVisible, Empty: "no providers configured",
})
return ui.Dialog(th, ui.DialogOpts{Title: "Select provider", Width: width}, body)
```

The empty-transcript dashboard is app composition, not a component API. Its
header owns the compact brand; the dashboard directly allocates fixed-height
`Panel` cards, with one or two columns according to available width. It has no
outer welcome panel or logo card. The `keys` card is always present; `get
started` is present only when no provider is selected or the selected provider
is unauthenticated; `agents & skills` requires at least one valid configured
agent or skill; and `recent prompts` requires history. Preserve those
conditions and the short-view fallback when changing the dashboard, without
exposing app-private helpers as component recipes.

## Extending and verification

Add a component under `internal/tui/ui`, make it width-safe and zero-value
tolerant, document it, add its production-appropriate coverage, and update
this catalog in the same change. New colors, glyphs, borders, spacing, and
emphasis belong in `theme`.

```sh
go test ./internal/tui/... -count=1
make test
```

For visual review, the opt-in gallery renders parameterized scenarios rather
than every view at both 80×24 and 120×40. Its current matrix is: 80×24 left
dashboard and right context; 93×40 canonical split; 92×60 left-only; 120×40
split; 120×80 cycle, permission modal, provider picker, command palette, and
busy transcript; and 160×45 long-data/status and danger-modal cases:

```sh
STRIKE_GALLERY=1 go test ./internal/tui/ -run Gallery -count=1 -v
```

Review the constrained 80×24 dashboard, split and modal scenarios, and the
long-data cases appropriate to the change.
