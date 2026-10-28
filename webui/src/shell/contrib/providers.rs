//! Providers (#1518). The page lives in `crate::providers`. This file
//! only registers the palette command. The Menu button is chrome, so
//! Providers stays one click from the overflow on every width.

use std::rc::Rc;

use crate::shell::{Command, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "providers",
        section: "Settings",
        section_order: 20,
        label: "Providers",
        detail: "API keys and OpenAI-compatible hosts",
        shortcut: "",
        keywords: &["provider", "api", "key", "openai", "auth", "model"],
        argument: None,
        order: 5,
        run: Rc::new(|_| crate::shell::host::set_view("providers")),
    });
}
