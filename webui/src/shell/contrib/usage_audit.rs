//! Usage and audit (#1286, #1593). One Usage view; Audit is its second
//! segment. No Menu slot. Alt+U and Alt+A stay, bound in `main.rs`.
//! Export is a palette command. Verify stays on the audit segment.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook, Shortcut};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "usage",
            Group::Navigate,
            "Usage",
            "Tokens and cost from provider usage events",
            Rc::new(|_| crate::shell::host::set_view("usage")),
        )
        .shortcut("Alt+U")
        .keywords(&["tokens", "cost", "budget"])
        .order(40),
    );
    reg.command(
        Command::new(
            "audit",
            Group::Debug,
            "Audit",
            "Hash-chained log inside Usage",
            Rc::new(|_| crate::shell::host::set_view("audit")),
        )
        .shortcut("Alt+A")
        .keywords(&["chain", "tamper", "log", "usage"])
        .order(50),
    );
    reg.command(
        Command::new(
            "audit.export",
            Group::Debug,
            "Export audit chain…",
            "Download the redacted hash chain",
            Rc::new(|_| {
                crate::shell::host::set_view("audit")?;
                crate::park::dispatch_or_park("audit.export", "")
            }),
        )
        .keywords(&["export", "audit", "download", "chain"])
        .order(51),
    );
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
