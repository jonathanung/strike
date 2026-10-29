//! Usage dashboard and hash-chained audit log (#1286): top-bar switches,
//! palette commands, and the Alt+U / Alt+A shortcut listings. The pages
//! themselves live in `crate::usage_audit` and are rendered by `main.rs`
//! when the view is `usage` or `audit`; this file only registers them
//! with the shell, following docs/webui-shell.md. The Alt+U / Alt+A key
//! bindings themselves stay in `main.rs` next to the host bridge,
//! because only the page owns the view signal.

use std::rc::Rc;

use leptos::prelude::*;

use crate::shell::{Group, Command, ShellHook, Shortcut, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "usage",
        group: Group::Navigate,
        label: "Usage",
        detail: "Tokens and cost from provider usage events",
        shortcut: "Alt+U",
        keywords: &["tokens", "cost", "budget"],
        argument: None,
        order: 40,
        run: Rc::new(|_| crate::shell::host::set_view("usage")),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "audit",
        group: Group::Debug,
        label: "Audit",
        detail: "Hash-chained log of sensitive actions",
        shortcut: "Alt+A",
        keywords: &["chain", "tamper", "log"],
        argument: None,
        order: 50,
        run: Rc::new(|_| crate::shell::host::set_view("audit")),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.slot(TopSlot {
        id: "usage-audit",
        region: TopRegion::Trailing,
        order: 86,
        title: "Usage and Audit",
        overflow_label: "Usage / Audit",
        // 0: the view switches stay in the bar at every width, as they did
        // inside the old segmented control — the smoke test clicks them.
        collapse_below_px: 0,
        render: Rc::new(usage_audit_switch),
    });
    reg.shortcut(Shortcut {
        id: "usage",
        keys: "Alt+U",
        group: "Navigation",
        group_order: 1,
        label: "Open Usage",
        order: 5,
    });
    reg.shortcut(Shortcut {
        id: "audit",
        keys: "Alt+A",
        group: "Navigation",
        group_order: 1,
        label: "Open Audit",
        order: 6,
    });
}

fn usage_audit_switch() -> AnyView {
    view! {
        <div class="segmented" role="tablist" aria-label="Usage and Audit">
            <button
                data-view="usage"
                class=move || if crate::shell::host::view() == "usage" { "seg on" } else { "seg" }
                on:click=move |_| {
                    let _ = crate::shell::host::set_view("usage");
                }
            >
                "Usage"
            </button>
            <button
                data-view="audit"
                class=move || if crate::shell::host::view() == "audit" { "seg on" } else { "seg" }
                on:click=move |_| {
                    let _ = crate::shell::host::set_view("audit");
                }
            >
                "Audit"
            </button>
        </div>
    }
    .into_any()
}
