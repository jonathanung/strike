# Web shell

The navbar (top bar, command palette, shortcut overlay) lives in
`webui/src/shell/`. Session list, transcript, composer, and side panel stay
in `webui/src/main.rs`. Streams add chrome by registering into the shell.
They do not edit `chrome.rs` or `builtin.rs`.

## Add a contribution

Create `webui/src/shell/contrib/<name>.rs`. `<name>` is a Rust identifier
(`model_menu.rs`, not `model-menu.rs`). The file exports one function:

```rust
use std::rc::Rc;

use leptos::prelude::*;

use crate::shell::{Argument, Command, ShellHook, Shortcut, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "notifications.open",
        section: "Apps",
        section_order: 10,
        label: "Notifications",
        detail: "Approval requests and finished sessions",
        shortcut: "",
        keywords: &["bell", "inbox"],
        argument: None,
        order: 40,
        run: Rc::new(|args| crate::shell::dispatch("notifications.open", args)),
    });
    reg.slot(TopSlot {
        id: "notifications",
        region: TopRegion::Trailing,
        order: 40,
        title: "Notifications",
        overflow_label: "Notifications",
        collapse_below_px: 900,
        render: Rc::new(|| view! { <crate::notifications::Bell /> }.into_any()),
    });
    reg.shortcut(Shortcut {
        id: "notifications",
        keys: "Ctrl+Shift+N",
        group: "Session",
        group_order: 2,
        label: "Open notifications",
        order: 0,
    });
}
```

`cargo` and `trunk` compile every `*.rs` file in that directory. A later
file does not override an earlier one by filename order. **The same `id`
replaces** a command, slot, or shortcut, including a builtin. Register the
builtin id to take it over. Do not register a second id for the same job.

`install` runs once, when the page boots, before signals exist. A `run`
closure that needs component state should call [`dispatch`](#runtime-actions).
A slot `render` closure runs during the view, so it may read context and signals.

## Commands

| Field | Meaning |
|---|---|
| `id` | Stable id. Replacing this id replaces the command. |
| `section` / `section_order` | Palette heading. Lower `section_order` wins when the query is empty. |
| `label` / `detail` | Row title and secondary line. |
| `shortcut` | Shown on the row. The shell does not bind it. Bind keys in your own listener, or use a builtin chord. |
| `keywords` | Extra fuzzy terms. |
| `order` | Order inside a section when the query is empty. |
| `argument` | `Some` asks for a free-text argument before `run`. `required` rejects an empty string with “This command needs an argument.” |
| `run` | `Fn(&str) -> Result<(), String>`. `Err` stays in the palette as an alert. `Ok` closes it and records a recent. |

Fuzzy match is a subsequence score over label (weight 3), keywords and
shortcut (weight 2), and detail (weight 1). A hit has to score at least
eight points per query character, so a scattered match through a long
detail does not appear. An empty query lists every
command. Up to eight recent ids (`localStorage` key `strike.paletteRecents`)
move into a Recent section and out of their own section so a row is not
listed twice. A non-empty query drops Recent and leads with the best match.

Built-in command ids:

| id | What it does |
|---|---|
| `new` | `POST /v1/roots` |
| `open-session` | Argument. One session whose title or id contains the text. |
| `activate-workspace` | Argument. One workspace whose name or id contains the text. Activating records it. It does not move the live engine. |
| `back` / `forward` | Session history. |
| `focus` | Hide the session list and side panel. |
| `stop-all` | Interrupt running delegated children. |
| `shortcuts` | Shortcut overlay. |
| `chat` / `daemon` | The two views. |
| `steer` / `queue` | Composer enter mode. |
| `mode-default` `mode-plan` `mode-soft` `mode-edits` `mode-yolo` | `set.permission_mode` for the real five modes. |
| `theme-strike` `theme-nord` `theme-dracula` `theme-gruvbox` `theme-monokai` `theme-tokyo-night` `theme-catppuccin` | Bundled TUI themes. |
| `scheme-dark` `scheme-light` | Token pair. |

## Slots

The header is three zones and does not wrap: brand + workspace, at most five
primary views (Chat, Daemon), and status plus one Menu. `collapse_below_px`
no longer decides what sits in the bar.

| Region | Where it renders |
|---|---|
| `Status` | Right zone, with the gateway status chip. |
| `Leading` | Menu. |
| `Trailing` | Menu, except slot id `gateway` (the Connected chip, right zone) and `view` (the centre zone owns Chat / Daemon). |

The Menu stays mounted and hidden (`data-open="0"`) so a `data-view` button
is still in the DOM. Back, Forward, and Focus live in the Menu. Ctrl/Cmd-K
still opens the palette. Phone widths hide the centre zone and use the
bottom tab bar.

Builtin slots:

| id | Region | Collapse | Replace when |
|---|---|---|---|
| `gateway` | Trailing, order 80 | 720 | You own the gateway chip (#1265). |
| `view` | Trailing, order 90 | never | You are replacing Chat / Daemon. Prefer not to. |

The fallback session chip reads the host’s `session_kind`. Today that is
`idle` or `running` from the live `busy` bit. The chip uses the existing
badge classes. `#1263` should register a `Status` slot (suggested id
`session-status`) and the fallback steps aside. Do not paint a second
status chip beside it.

## Runtime actions

```rust
crate::shell::bind("notifications.open", Rc::new(|_args| {
    open.set(true);
    Ok(())
}));
```

`dispatch` returns `Err("Nothing is listening for <id>.")` until `bind`.
Call `bind` from the slot component’s setup. `unbind` on cleanup if the
slot can unmount. Last bind wins.

`set_host` is for the page that owns sessions, workspaces, and focus. Streams
do not replace the host.

## Shortcuts

These are the shell chords. `?` is ignored while typing in a field or while
the palette is open.

| Keys | Action |
|---|---|
| `Ctrl+K` / `Cmd+K` | Open or close the palette. |
| `Up` / `Down` | Move the palette highlight. |
| `Enter` | Run the highlight. On a command with an argument, the first Enter asks for it. The next Enter runs it. |
| `Esc` | Leave argument mode, or close the palette, the shortcut overlay, and open menus. |
| `Alt+Left` / `Alt+Right` | Back / forward through sessions you opened. |
| `Alt+\` | Focus mode. |
| `?` | Shortcut overlay, when focus is not in a field. |

The overlay lists every registered shortcut, so a stream’s `reg.shortcut`
shows up there without a navbar edit. Registering a shortcut does not bind
the keys.

## What the bar shows

`Strike > workspace`, then Chat and Daemon, then the session chip and the
gateway chip (`Connected`, `Checking`, or `Unreachable`), then Menu.
No active workspace is the literal label “No workspace”. The full workspace
name is also the first line of the Menu. The daemon version stays on the
wordmark tooltip. Providers, theme, layout, and the other slots are in
the Menu or the command palette.
