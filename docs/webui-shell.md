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

use crate::shell::{Command, Group, ShellHook, Shortcut, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "notifications.open",
            Group::Session,
            "Notifications",
            "Approval requests and finished sessions",
            Rc::new(|args| crate::shell::dispatch("notifications.open", args)),
        )
        .keywords(&["bell", "inbox"])
        .order(40),
    );
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

Register with `Command::new(id, group, label, detail, run)` and the builder
methods. `label` is the title the palette shows and searches. The same `id`
replaces a command, including a builtin.

| Field | Meaning |
|---|---|
| `id` | Stable id. Replacing this id replaces the command. |
| `group` | One of `Group::Navigate`, `Session`, `Providers`, `Model`, `Git`, `Settings`, `Customize`, `Gateway`, `Theme`, `Debug`. Empty-query order is that list. |
| `label` / `detail` | Row title and secondary line. |
| `shortcut` | Shown on the row. The shell does not bind it. Bind keys in your own listener, or use a builtin chord. |
| `keywords` | Extra fuzzy terms, weighted with the shortcut above the detail. |
| `order` | Order inside a group when the query is empty. |
| `argument` | `Some` asks for a free-text argument before `run`. `required` rejects an empty string with “This command needs an argument.” |
| `choices` | Second step. A closure returns rows (`id`, `label`, `detail`). The palette lists them, the field filters them, and `run` receives the chosen id. Use this for “Switch model…”, “Switch workspace…”, and “Open session…”. |
| `visible` | `None` lists the command. `Some` hides it when the predicate is false, including from Recent. |
| `enabled` | `None` can run. `Some` keeps the row and refuses to run when the predicate is false. |
| `confirm` | `Some(ConfirmSpec { title, body, confirm_label })` opens the shared dialog in `webui/src/confirm.rs` before `run`. Use it for destructive work. The button that performs the same work should call `confirm::ask` too, and `run` should not ask a second time. |
| `run` | `Fn(&str) -> Result<(), String>`. `Err` stays in the palette as an alert. `Ok` closes it and records a recent and a use count. |

### Moving a control behind the palette

1. Add `reg.command(...)` in `webui/src/shell/contrib/<feature>.rs`. Pick the group above. Do not add a second id for a command that already exists.
2. If the action needs signals from the page, `run` should `dispatch` an id and the page should `bind` that id while the owner is mounted.
3. Delete the visible header or Menu control. If the widget must stay mounted (a bell, a switcher, a dialog host), leave the slot and the shell parks it in `.shell-alive`, which is not part of the visible header.
4. If the command destroys something, set `confirm` and call `crate::confirm::ask` from the in-page button as well.
5. Add a keyword the operator would type. Do not reuse a keyword that is the whole label of another command (`plan` is Approval: Plan).

Fuzzy match is a subsequence score over label (weight 3), keywords and
shortcut (weight 2), and detail (weight 1). A hit has to score at least
eight points per query character, so a scattered match through a long
detail does not appear. An empty query lists Recent (last five), then
Frequent (next five by use count, skipping recents), then the groups.
Storage keys are `strike.paletteRecents` and `strike.paletteFreq`. A
non-empty query drops those two headings and leads with the best match.

The palette opens with Ctrl/Cmd-K, with `/` when focus is not in a field,
from the Commands button, from the Menu row “Run a command”, and from the
phone header button. On a phone it is a full-height sheet pinned to the
visual viewport (`--vv-height`, `--vv-top`) with 44px rows. Esc closes it.
A command with an argument or choices takes a second step; Esc there
returns to the list.

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
primary views (Chat, Daemon), and status, the Commands button, and one Menu.
Secondary actions live in the palette. `collapse_below_px` no longer decides
what sits in the bar. Slots other than status and the gateway chip stay
mounted in a visually hidden tray (`.shell-alive`) so a bell or switcher
still has one live instance.

| Region | Where it renders |
|---|---|
| `Status` | Right zone, with the gateway status chip. |
| `Leading` | Menu. |
| `Trailing` | Menu, except slot id `gateway` (the Connected chip, right zone) and `view` (the centre zone owns Chat / Daemon). |

The Menu stays mounted and hidden (`data-open="0"`) so a `data-view` button
is still in the DOM. Back, Forward, and Focus live in the Menu. Ctrl/Cmd-K
still opens the palette. Phone widths hide the centre zone and use the
bottom tab bar.

On phone widths the overflow sheet (`phone_sheet` in chrome.rs) holds the
Leading and Trailing slots and stays mounted the same way. There,
`gateway-switcher` and `notifications` render only in the sheet — the Menu
skips them — so each has exactly one live instance at any viewport: a
second switcher leaks its probe timer and Alt+W listener per remount, and
a second bell can leave the palette handle pointing at a disposed signal
(#1527). Slot components must release every window registration on
cleanup; the shell remounts slots when the layout crosses the phone
breakpoint.

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
| `/` | Open the palette when focus is not in a field. |
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
gateway chip (`Connected`, `Checking`, or `Unreachable`), then Commands,
then Menu. No active workspace is the literal label “No workspace”. The
full workspace name is the first line of the Menu. The Menu’s only action
is “Run a command”. The header also shows the daemon reachability line (`Connected · version`). The wordmark tooltip repeats it. It hides below 1200px.
Providers, theme, density, layout, notifications, gateway pairing, git,
session rename/fork/export, and the other secondary actions are palette
commands. Customize pages are not in this tree yet; register them under
`Group::Customize` when they land.
