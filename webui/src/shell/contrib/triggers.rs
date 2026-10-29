//! Inbound triggers (#1282): a top-bar switch and a palette command.
//! The page lives in `crate::triggers` and is rendered by `main.rs` when
//! the view is `triggers`. This file only registers the shell chrome.

use std::rc::Rc;

use leptos::prelude::*;

use crate::shell::{Argument, Command, ConfirmSpec, Group, ShellHook, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "trigger.delete",
        group: Group::Gateway,
        label: "Delete trigger…",
        detail: "Remove an inbound trigger and its hook secret",
        shortcut: "",
        keywords: &["delete", "webhook", "trigger"],
        argument: Some(Argument {
            placeholder: "Trigger id",
            required: true,
        }),
        order: 70,
        run: Rc::new(|args| crate::shell::dispatch("trigger.delete", args)),
        visible: None,
        enabled: None,
        confirm: Some(ConfirmSpec {
            title: "Delete this trigger?",
            body: "The hook path stops accepting events. A new trigger gets a new secret, so the sender has to be updated.",
            confirm_label: "Delete trigger",
        }),
        choices: None,
    });
    reg.command(Command {
        id: "triggers",
        group: Group::Navigate,
        label: "Triggers",
        detail: "Webhooks, GitHub events, and channel messages",
        shortcut: "",
        keywords: &["webhook", "github", "hook"],
        argument: None,
        order: 35,
        run: Rc::new(|_| crate::shell::host::set_view("triggers")),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.slot(TopSlot {
        id: "triggers",
        region: TopRegion::Trailing,
        order: 86,
        title: "Triggers",
        overflow_label: "Triggers",
        // Stay in the bar. The smoke test clicks this button by its label.
        collapse_below_px: 0,
        render: Rc::new(triggers_switch),
    });
}

fn triggers_switch() -> AnyView {
    view! {
        <div class="segmented" role="tablist" aria-label="Triggers">
            <button
                data-view="triggers"
                class=move || if crate::shell::host::view() == "triggers" { "seg on" } else { "seg" }
                on:click=move |_| {
                    let _ = crate::shell::host::set_view("triggers");
                }
            >
                "Triggers"
            </button>
        </div>
    }
    .into_any()
}
