//! Open on phone (#1289): a palette command and a top-bar button. The dialog
//! lives in `crate::gateway_qr` and reads a late-bound `PhoneHost`, because
//! only the page owns the base and token signals. Slot id is `phone`, not
//! `gateway` (status chip) or `gateway-switcher` (Alt+W).

use std::rc::Rc;

use leptos::prelude::*;

use crate::shell::{Command, ShellHook, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "phone.open",
        section: "Apps",
        section_order: 10,
        label: "Open on phone",
        detail: "QR and a one-time link for another browser",
        shortcut: "",
        keywords: &["phone", "qr", "pair", "pairing"],
        argument: None,
        order: 55,
        run: Rc::new(|args| crate::shell::dispatch("phone.open", args)),
    });
    reg.slot(TopSlot {
        id: "phone",
        region: TopRegion::Trailing,
        order: 88,
        title: "Open on phone",
        overflow_label: "Phone",
        collapse_below_px: 0,
        render: Rc::new(phone_button),
    });
}

fn phone_button() -> AnyView {
    view! {
        <button
            type="button"
            class="secondary gq-open"
            title="Open on phone"
            on:click=move |_| {
                let _ = crate::shell::dispatch("phone.open", "");
            }
        >
            "Phone"
        </button>
    }
    .into_any()
}
