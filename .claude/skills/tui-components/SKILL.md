---
name: tui-components
description: Use when building or restyling strike's TUI — views, panels, modals, pickers, badges, dashboards, status rows, or any layout work in internal/tui. Covers the internal/tui/ui component catalog and internal/tui/theme tokens. Do not use for backend/engine work (internal/engine, internal/provider, internal/tool, internal/permission, internal/session, internal/auth, internal/host) — those packages have no UI surface.
---

# TUI components (strike-cli)

`internal/tui/ui` is the component library; `internal/tui/theme` is the token
source. Views compose these instead of building raw lipgloss boxes, lists, or
hardcoded ANSI glyphs. This skill's job is to keep that true — if you add or
change a component, update this file in the same change so the catalog below
never drifts from `internal/tui/ui/*.go`.

## Boundary rules

Enforced by `internal/tui/boundary_test.go` (`TestArchitectureBoundaries`,
walks the module with `go/parser`) — a violation fails loudly with the
offending file and import, so don't try to route around it:

- `internal/tui/...` (including `ui` and `theme`) may import only
  `internal/protocol`, `internal/host`, other `internal/tui/...` packages,
  stdlib, and third-party (lipgloss, bubbles, bubbletea, charmbracelet/x/ansi).
  No `internal/auth`, `internal/config`, `internal/models`, `internal/engine`,
  etc. — those go through `host.Services`, not direct imports.
- `internal/tui/ui` specifically imports only stdlib, lipgloss, bubbles,
  `charmbracelet/x/ansi`, and `internal/tui/theme` (see the package doc in
  `internal/tui/ui/doc.go`). No `internal/tui` (the app/model package) import
  — components never reach back into app state.
- Colors and glyphs always come from theme tokens — `theme.Theme` fields
  (`th.Accent`, `th.Border`, …), `th.Icons` (or `theme.DefaultIcons()`), and
  `th.S()` for precomputed styles. Never a literal hex string or a hardcoded
  `"✓"`/`"❯"` in a view or component.
- Components are pure renderers: every exported `ui` function takes a
  `theme.Theme` (+ opts) and returns a `string`. None of them see a
  `tea.Msg`. Key handling and state (cursor position, filter text, which
  modal is open) live in the `internal/tui` model files — `modal.go`,
  `palette.go`, `provider_modal.go`, `model_modal.go`, `auth.go` — which call
  a component's `view()` method to render and keep their own `update()` for
  input.

## Component catalog

All signatures are copy-paste exact from `internal/tui/ui/*.go`. Shared enum
types used across several components: `Tone` (`ToneDefault`, `ToneAccent`,
`ToneAccentAlt`, `ToneSuccess`, `ToneWarning`, `ToneError`, `ToneMuted` —
`tone.go`) and `Level` (`LevelInfo`, `LevelSuccess`, `LevelWarning`,
`LevelError` — `notice.go`, used only by `Notice`).

| Component | Signature | For | Key opts |
|---|---|---|---|
| Panel | `Panel(th theme.Theme, opts PanelOpts, body string) string` | Rounded-border tile with the title woven into the top border and an optional footer hint in the bottom border. The bento-tile primitive; `Dialog` and `Card` are built on it. | `Title`, `Footer`, `Width` (mandatory; output is exactly this wide), `Height` (0 fits content), `Focused` (BorderFocus), `Dim` (BorderMuted), `Tone` (override; see precedence below) |
| InnerWidth | `InnerWidth(width int) int` | Content width inside a Panel of the given outer width, after border + padding. Wrap body text to this before calling Panel/Dialog. | — |
| Dialog | `Dialog(th theme.Theme, opts DialogOpts, body string) string` | Standard modal frame: a focused Panel plus a muted hint line at the foot of the body. Every modal in the app routes through this so they look identical. | `Title`, `Hint` (bottom line), `Width`, `Height`, `Tone` |
| Badge | `Badge(th theme.Theme, tone Tone, text string) string` | Compact bracketed chip, e.g. `[ anthropic/claude-sonnet-5 ]`, tone-colored. Sizes to its text — truncate the text yourself first if it must fit a budget. | tone, text |
| KeyHints | `KeyHints(th theme.Theme, width int, hints []KeyHint) string` | Footer row of keybinding hints joined by `·`: `enter send · ctrl+k palette`. Keys accented, labels muted. Drops whole hints that don't fit rather than cutting one mid-hint. | `hints []KeyHint{Key, Label}` |
| StatusBar | `StatusBar(th theme.Theme, width int, left, right string) string` | One row, `left` flush-left and `right` flush-right with space between, exactly `width` cells. Truncates `right` first, then drops it, before ever overflowing. | left/right pre-styled strings |
| List | `List(th theme.Theme, opts ListOpts) string` | Picker body: optional filter header, a scrolling cursor window, an empty state. No border of its own — wrap it in a Panel or Dialog. Powers the provider/model pickers and the command palette. | `Items []ListItem{Label, Detail, Current, Disabled}`, `Cursor`, `Width`, `Visible` (window size, 0 = show all), `ShowFilter`/`Filter`/`Total`, `Empty` message |
| Notice | `Notice(th theme.Theme, level Level, text string, width int) string` | One-line status message with a level glyph (`◦`/`✓`/`✗`) and matching color. The reserved notice row and the style for error/info transcript cells. | `level`, `text`, `width` (whole line truncates to it) |
| Card / Bento | `Bento(th theme.Theme, width, gap int, cards []Card) string` | `Card{Title, Footer, Body, Width, Tone}` is a Panel with a preferred width; `Bento` packs cards left-to-right into rows that fit `width`, wrapping to a new row on overflow (single column when narrow). The welcome-dashboard layout. Card auto-wraps `Body` to `InnerWidth(Width)` before rendering — unlike Panel/Dialog, you don't pre-wrap it yourself. | `cards []Card`, `gap` |
| OverlayCenter / ModalWidth | `OverlayCenter(bg, fg string, width, height int) string`, `ModalWidth(screenWidth int) int` | Composite a rendered dialog (`fg`) centered over the base view (`bg`) on a `width`×`height` screen, ANSI-aware (escape sequences stay balanced across the cut). `ModalWidth` is the standard outer dialog width: `min(72, screenWidth-4)`. | — |
| Logo / LogoCompact | `Logo(th theme.Theme) string`, `LogoCompact(th theme.Theme) string` | The 3-line "strike" wordmark (accent-gradient rules around the bolt+letters) and its one-line `⚡ strike` fallback for narrow spaces. | — |

### Panel border color precedence

`Tone` (when not `ToneDefault`) beats `Focused`, which beats `Dim`, which
falls back to the plain `Border` color — in that order. So a warning-tone
permission dialog stays warning-colored even though `Dialog` always sets
`Focused: true` underneath.

### Pre-wrapping vs. truncating

Panel and Dialog **truncate** each line of `body` to fit — they do not wrap
multi-line text for you. Wrap first with `lipgloss.NewStyle().Width(w).Render(...)`
sized to `ui.InnerWidth(width)` (see `wrapToWidth` in `internal/tui/modal.go`).
`Card` is the one exception: it wraps its own `Body` internally before handing
it to `Panel`.

## Recipes

Real snippets from the current views — follow these shapes rather than
inventing new ones.

**Dialog wrapping a List (picker modal)** — `internal/tui/provider_modal.go`
(same shape in `model_modal.go`, `auth.go`; the filterable command palette in
`palette.go` adds `ShowFilter`/`Filter`/`Total`):

```go
body := ui.List(th, ui.ListOpts{
    Items:   items,
    Cursor:  m.cursor,
    Width:   max(1, ui.InnerWidth(width)),
    Visible: providerModalVisible,
    Empty:   "no providers configured",
})
return ui.Dialog(th, ui.DialogOpts{
    Title: "Select provider",
    Hint:  "↑/↓ move · enter select or log in · ctrl+d set default · esc close",
    Width: width,
}, body)
```

**Titled panel region** — `internal/tui/view.go` (`transcriptView`,
`composerView`):

```go
return ui.Panel(m.th, ui.PanelOpts{
    Title:  title, // "welcome" or "session"
    Footer: m.transcriptFooter(),
    Width:  m.width,
    Height: height,
}, body)

// composer: focused border marks it as the active input
return ui.Panel(m.th, ui.PanelOpts{
    Title:   "prompt " + m.themeIcons().Prompt,
    Width:   m.width,
    Focused: true,
}, m.composer.View())
```

**Keyhints footer** — `internal/tui/view.go` (`hintsView`):

```go
return ui.KeyHints(m.th, max(1, width), []ui.KeyHint{
    {Key: "enter", Label: "send"},
    {Key: "alt+enter", Label: "newline"},
    {Key: "ctrl+k", Label: "palette"},
    {Key: "tab", Label: "agent"},
    {Key: "ctrl+d", Label: "save defaults"},
    {Key: "esc", Label: "interrupt"},
    {Key: "pgup/pgdn", Label: "scroll"},
})
```

**Bento dashboard card row** — `internal/tui/welcome.go` (`welcomeView`):

```go
cards := []ui.Card{
    {Title: "strike", Body: ui.Logo(m.th), Width: 24, Tone: ui.ToneAccent},
    {Title: "get started", Body: m.welcomeProviders(), Width: 38},
    {Title: "keys", Body: m.welcomeKeys(), Width: 26},
    {Title: "agents & skills", Body: m.welcomeAgentsSkills(), Width: 30},
}
if len(m.entries) > 0 {
    cards = append(cards, ui.Card{Title: "recent prompts", Body: m.welcomeRecent(), Width: 38})
}
return ui.Bento(m.th, width, 2, cards)
```

**Badges in a status row** — `internal/tui/view.go` (`headerView`):

```go
left := ui.LogoCompact(m.th)
left += "  " + ui.Badge(m.th, ui.ToneAccent, m.providerName+"/"+model)
if m.agentName != "" {
    left += " " + ui.Badge(m.th, ui.ToneAccentAlt, ic.Agent+" "+m.agentName)
}
right := ""
if m.turnRunning {
    right = m.spin.View() + " " + st.Warning.Render("working — esc interrupts")
}
return ui.StatusBar(m.th, max(1, width), left, right)
```

## Theme tokens

`internal/tui/theme`. Use `theme.Default()` for the stock palette,
`theme.DefaultIcons()` for the stock glyphs, and `(Theme).S()` for
precomputed styles — never re-`lipgloss.NewStyle().Foreground(...)` a role
you can get from `th.S()`.

### `theme.Theme` fields (all `lipgloss.AdaptiveColor`, light+dark)

| Field | Role |
|---|---|
| `Text` | primary foreground |
| `TextMuted` | secondary/de-emphasized foreground |
| `Accent` | primary emphasis (titles, assistant) |
| `AccentAlt` | secondary emphasis (user label, info) |
| `Highlight` | foreground of the selected/active item |
| `Success` | positive state (ok, added) |
| `Warning` | caution state (permission prompts) |
| `Error` | failure state (errors, removed) |
| `Border` | standard panel border |
| `BorderFocus` | border of the focused/active panel |
| `BorderMuted` | dim chrome (inactive tiles, gutters) |
| `UserLabel` | "you" transcript label |
| `ToolLabel` | tool-call transcript label |
| `DiffAdded` | added lines in diffs |
| `DiffRemoved` | removed lines in diffs |
| `Icons` | the glyph set, see below |

Roles carry meaning, not appearance — `Accent` is "the primary emphasis
color," not "violet," so treat it as the token even if you know the stock hex.

### `theme.Icons` (via `th.Icons`, or `theme.DefaultIcons()`)

| Field | Glyph | Meaning |
|---|---|---|
| `Prompt` | `❯` | user prompt / input marker |
| `Assistant` | `●` | assistant label bullet |
| `Tool` | `⚙` | tool-call label |
| `OK` | `✓` | success |
| `Err` | `✗` | error |
| `Info` | `◦` | informational |
| `Agent` | `◆` | agent marker |
| `Bolt` | `⚡` | brand motif |
| `Dot` | `·` | inline separator |
| `Cursor` | `▸` | selection cursor |

A zero-value `theme.Theme{}` still renders: components fall back to
`theme.DefaultIcons()` whenever `th.Icons.Cursor == ""`.

### `(Theme).S() Styles`

Precomputed once per render from the color roles — `Text`, `Muted`,
`Accent`, `AccentAlt`, `Title` (accent + bold), `Success`, `Warning`,
`Error`. Call `st := th.S()` once at the top of a `view()` and reuse `st`
rather than building fresh `lipgloss.Style` values per line.

## Extending the kit

When the catalog above doesn't cover what a view needs:

1. Add the component to `internal/tui/ui` (its own file, `package ui`),
   respecting the import boundary above. Give it a doc comment with a short
   usage snippet in the style of the existing files (see `Panel`'s doc
   comment in `panel.go` for the shape: one-line description, a runnable
   snippet, and — where it clarifies behavior — a rendered-output comment).
2. Make it width-safe and zero-value tolerant like its neighbors, and add a
   test proving it: assert `lipgloss.Width(out) == width` across a range of
   widths including tiny ones (0, 1, 2) and one with wide runes (e.g. `界`),
   the way `TestPanelRendersExactlyRequestedWidth`,
   `TestPanelNeverExceedsWidthWithWideRunesAndLongTitle`, and
   `TestPanelDegradesAtTinyWidthsWithoutPanic` do in `panel_test.go`.
3. Update the catalog table (and recipes, if it changes how a view is built)
   in **this file**, in the same change. The catalog is only useful if it
   matches `internal/tui/ui/*.go` exactly — don't let it drift.

## Verification

```sh
go test ./internal/tui/... -count=1
make test   # full battery — see the test-and-validate skill
```

To eyeball a rendered view instead of just asserting on it, run the gallery
test (skipped by default, so it never affects the normal suite):

```sh
STRIKE_GALLERY=1 go test ./internal/tui/ -run Gallery -count=1 -v
```

It logs full `View()` renders — welcome dashboard, a busy transcript with
tool cells, the permission modal, the provider picker, the command palette —
at 100×30 for human/orchestrator review.
