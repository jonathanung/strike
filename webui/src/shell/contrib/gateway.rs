//! Gateway switcher (#1265): a top-bar slot, a palette command, and the
//! Alt+W shortcut listing. The switcher component itself lives in
//! `crate::gateway` and is rendered through its late-bound `GatewayHost`,
//! because only the page owns the base/token signals it drives; this file
//! only registers it with the shell, following docs/webui-shell.md.
//!
//! Shortcut: Alt+W, not the Alt+G the PR originally shipped — Goals
//! (#1284, PR #1300) landed first and owns Alt+G on main. Alt+S/M/N/U/A
//! are likewise taken (side chat, model menu, notifications, usage,
//! audit), so the switcher takes the free Alt+W.
//!
//! Slot id is `gateway-switcher`, deliberately NOT `gateway`: the
//! `gateway` slot is the status aggregate chip owned by #1263
//! (`contrib/status.rs`), and registering the same id would replace it.

use std::rc::Rc;

use crate::shell::{Command, ConfirmSpec, Group, ShellHook, Shortcut, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "gateway.rotate",
        group: Group::Gateway,
        label: "Rotate gateway token",
        detail: "Invalidate the host token and store a new one in this browser",
        shortcut: "",
        keywords: &["token", "rotate", "secret"],
        argument: None,
        order: 80,
        run: Rc::new(|_| crate::shell::dispatch("gateway.rotate", "")),
        visible: None,
        enabled: None,
        confirm: Some(ConfirmSpec {
            title: "Rotate gateway token?",
            body: "The current token stops working everywhere it was copied. This browser stores the new one.",
            confirm_label: "Rotate token",
        }),
        choices: None,
    });
    reg.command(Command {
        id: "gateway.revoke",
        group: Group::Gateway,
        label: "Revoke gateway grants",
        detail: "Drop every short-lived grant. The host token file stays",
        shortcut: "",
        keywords: &["revoke", "grants"],
        argument: None,
        order: 81,
        run: Rc::new(|_| crate::shell::dispatch("gateway.revoke", "")),
        visible: None,
        enabled: None,
        confirm: Some(ConfirmSpec {
            title: "Revoke gateway grants?",
            body: "Every short-lived grant stops working. The host token file is not changed.",
            confirm_label: "Revoke grants",
        }),
        choices: None,
    });
    reg.command(Command {
        id: "gateway.reconnect",
        group: Group::Gateway,
        label: "Gateways: Reconnect",
        detail: "Check this gateway again",
        shortcut: "",
        keywords: &["reconnect", "retry", "refresh", "unreachable"],
        argument: None,
        order: 51,
        run: Rc::new(|_| crate::shell::dispatch("gateway.reconnect", "")),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "gateway.logout",
        group: Group::Gateway,
        label: "Gateways: Log out this browser",
        detail: "Drop this browser's token. The host token file stays",
        shortcut: "",
        keywords: &["logout", "sign out", "disconnect", "browser"],
        argument: None,
        order: 82,
        run: Rc::new(|_| crate::shell::dispatch("gateway.logout", "")),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "gateway.add",
        group: Group::Gateway,
        label: "Gateways: Add a gateway",
        detail: "Open the form for a name, base URL, and SSH target",
        shortcut: "",
        keywords: &["add", "new", "remote", "ssh"],
        argument: None,
        order: 52,
        run: Rc::new(|_| crate::shell::dispatch("gateway.add", "")),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "gateways.open",
        group: Group::Gateway,
        label: "Gateways",
        detail: "Switch between this daemon and saved gateways",
        shortcut: "Alt+W",
        keywords: &["gateway", "ssh", "connect", "daemon", "remote"],
        argument: None,
        order: 50,
        run: Rc::new(|args| crate::shell::dispatch("gateways.open", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.slot(TopSlot {
        id: "gateway-switcher",
        region: TopRegion::Trailing,
        order: 82,
        title: "Gateways",
        overflow_label: "Gateways",
        // 0: the switcher button stays in the bar at every width, as it
        // did in the pre-shell header — the smoke test clicks it, and the
        // panel itself is fixed-position at phone widths.
        collapse_below_px: 0,
        render: Rc::new(|| crate::gateway::switcher_view()),
    });
    reg.shortcut(Shortcut {
        id: "gateway-switcher",
        keys: "Alt+W",
        group: "Navigation",
        group_order: 1,
        label: "Open the gateway switcher",
        order: 5,
    });
}
