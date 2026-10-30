//! Providers (#1518, #1597). The page is the key-entry target. The Menu
//! row stays in the shell. Refresh and remove-key are palette commands.
//! Remove provider stays on the card until #1544.

use std::rc::Rc;

use crate::shell::{Argument, Choice, Command, ConfirmSpec, Group, ShellHook};

fn key_choices(prefix: &'static str, detail: &'static str) -> Vec<Choice> {
    crate::providers::key_provider_names()
        .into_iter()
        .map(|name| Choice {
            id: name.clone(),
            label: format!("{prefix}{name}"),
            detail: detail.to_string(),
        })
        .collect()
}

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "providers",
            Group::Providers,
            "Providers · API key",
            "API keys and OpenAI-compatible hosts",
            Rc::new(|_| crate::shell::host::set_view("providers")),
        )
        .keywords(&["provider", "api", "key", "openai", "auth", "model"])
        .order(5),
    );
    reg.command(
        Command::new(
            "providers.add-key",
            Group::Providers,
            "Add an API key…",
            "Open Providers on that provider's key field",
            Rc::new(|args| {
                crate::shell::host::set_view("providers")?;
                crate::park::dispatch_or_park("providers.focus", args)
            }),
        )
        .keywords(&["key", "api", "add", "provider", "paste"])
        .order(6)
        .argument(Argument {
            placeholder: "Provider",
            required: true,
        })
        .choices(|| key_choices("Add an API key: ", "Opens the key field")),
    );
    reg.command(
        Command::new(
            "providers.add-host",
            Group::Providers,
            "Add custom provider…",
            "Open the host form at the bottom of Providers",
            Rc::new(|_| {
                crate::shell::host::set_view("providers")?;
                crate::park::dispatch_or_park("providers.focus", "custom")
            }),
        )
        .keywords(&["add", "custom", "host", "local", "ollama"])
        .order(7),
    );
    reg.command(
        Command::new(
            "providers.refresh",
            Group::Providers,
            "Refresh providers",
            "Reload provider status from the daemon",
            Rc::new(|_| {
                crate::shell::host::set_view("providers")?;
                crate::park::dispatch_or_park("providers.refresh", "")
            }),
        )
        .keywords(&["refresh", "reload", "providers", "status"])
        .order(8),
    );
    reg.command(
        Command::new(
            "providers.remove-key",
            Group::Providers,
            "Remove stored key…",
            "Delete the daemon copy of one provider's API key",
            Rc::new(|args| {
                crate::shell::host::set_view("providers")?;
                crate::park::dispatch_or_park("providers.remove-key", args)
            }),
        )
        .keywords(&["remove", "delete", "revoke", "key"])
        .order(9)
        .argument(Argument {
            placeholder: "Provider",
            required: true,
        })
        .choices(|| key_choices("Remove stored key: ", "Deletes the saved key"))
        .confirm(ConfirmSpec {
            title: "Remove stored key?",
            body: "The daemon deletes the saved API key for this provider. The provider stays in the list.",
            confirm_label: "Remove key",
        }),
    );
}
