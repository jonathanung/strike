//! Apps (#1283): a top-bar switch and a palette command. The pane lives in
//! `crate::apps` and is rendered by `main.rs` when the view is `apps`.

use std::rc::Rc;

use leptos::prelude::*;

use crate::shell::{Command, ShellHook, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "apps",
        section: "Apps",
        section_order: 10,
        label: "Apps",
        detail: "Queues such as GitHub triage",
        shortcut: "",
        keywords: &["github", "triage", "issues"],
        argument: None,
        order: 20,
        run: Rc::new(|_| crate::shell::host::set_view("apps")),
    });
    reg.slot(TopSlot {
        id: "apps",
        region: TopRegion::Trailing,
        order: 84,
        title: "Apps",
        overflow_label: "Apps",
        // 0: the view switch stays in the bar at every width. Smoke clicks it.
        collapse_below_px: 0,
        render: Rc::new(apps_switch),
    });
}

fn apps_switch() -> AnyView {
    view! {
        <div class="segmented" role="tablist" aria-label="Apps">
            <button
                data-view="apps"
                class=move || if crate::shell::host::view() == "apps" { "seg on" } else { "seg" }
                on:click=move |_| {
                    let _ = crate::shell::host::set_view("apps");
                }
            >
                "Apps"
            </button>
        </div>
    }
    .into_any()
}
