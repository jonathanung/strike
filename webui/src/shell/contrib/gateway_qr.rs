//! Open on phone (#1289): a palette command and a top-bar button. The dialog
//! lives in `crate::gateway_qr` and reads a late-bound `PhoneHost`, because
//! only the page owns the base and token signals. Slot id is `phone`, not
//! `gateway` (status chip) or `gateway-switcher` (Alt+W).

use std::rc::Rc;

use crate::shell::{Command, ConfirmSpec, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "pair.revoke-all",
        group: Group::Gateway,
        label: "Revoke paired browsers",
        detail: "Sign out every browser paired with this gateway",
        shortcut: "",
        keywords: &["revoke", "pair", "phone", "browser"],
        argument: None,
        order: 90,
        run: Rc::new(|_| crate::shell::dispatch("pair.revoke-all", "")),
        visible: None,
        enabled: None,
        confirm: Some(ConfirmSpec {
            title: "Revoke paired browsers?",
            body: "Every paired browser loses its grant. Pairing again mints a new code.",
            confirm_label: "Revoke all",
        }),
        choices: None,
    });
    reg.command(Command {
        id: "phone.open",
        group: Group::Gateway,
        label: "Open on phone",
        detail: "QR and a one-time link for another browser",
        shortcut: "",
        keywords: &["phone", "qr", "pair", "pairing"],
        argument: None,
        order: 55,
        run: Rc::new(|args| crate::shell::dispatch("phone.open", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "app.install",
        group: Group::Gateway,
        label: "App: Install Strike",
        detail: "Add Strike to the home screen",
        shortcut: "",
        keywords: &["install", "pwa", "home screen", "app"],
        argument: None,
        order: 56,
        run: Rc::new(|_| crate::shell::dispatch("app.install", "")),
        visible: Some(Rc::new(crate::gateway_qr::install_ready)),
        enabled: None,
        confirm: None,
        choices: None,
    });
}
