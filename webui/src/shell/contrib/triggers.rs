//! Inbound triggers (#1282, #1593). Palette-only view: no Menu slot.
//! Delete still confirms. Test, bad signature, and replay are palette
//! commands with a trigger choice.

use std::rc::Rc;

use crate::shell::{Argument, Command, ConfirmSpec, Group, ShellHook};

fn open(action: &'static str) -> crate::shell::CommandRun {
    Rc::new(move |args| {
        crate::shell::host::set_view("triggers")?;
        crate::park::dispatch_or_park(action, args)
    })
}

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "trigger.delete",
            Group::Gateway,
            "Delete trigger…",
            "Remove an inbound trigger and its hook secret",
            Rc::new(|args| crate::shell::dispatch("trigger.delete", args)),
        )
        .keywords(&["delete", "webhook", "trigger"])
        .order(70)
        .argument(Argument {
            placeholder: "Trigger id",
            required: true,
        })
        .confirm(ConfirmSpec {
            title: "Delete this trigger?",
            body: "The hook path stops accepting events. A new trigger gets a new secret, so the sender has to be updated.",
            confirm_label: "Delete trigger",
        }),
    );
    reg.command(
        Command::new(
            "triggers",
            Group::Navigate,
            "Triggers",
            "Webhooks, GitHub events, and channel messages",
            Rc::new(|_| crate::shell::host::set_view("triggers")),
        )
        .keywords(&["webhook", "github", "hook"])
        .order(35),
    );
    reg.command(
        Command::new(
            "trigger.test",
            Group::Gateway,
            "Send test event to trigger…",
            "Deliver a synthetic event to one trigger",
            open("trigger.test"),
        )
        .keywords(&["test", "webhook", "fire"])
        .order(71)
        .argument(Argument {
            placeholder: "Trigger",
            required: true,
        })
        .choices(crate::triggers::trigger_choices),
    );
    reg.command(
        Command::new(
            "trigger.reject",
            Group::Gateway,
            "Test trigger with a bad signature…",
            "Send a delivery the hook should reject",
            open("trigger.reject"),
        )
        .keywords(&["reject", "signature", "hmac"])
        .order(72)
        .argument(Argument {
            placeholder: "Trigger",
            required: true,
        })
        .choices(crate::triggers::trigger_choices),
    );
    reg.command(
        Command::new(
            "trigger.replay",
            Group::Gateway,
            "Replay trigger delivery…",
            "Resend the latest accepted delivery",
            open("trigger.replay"),
        )
        .keywords(&["replay", "delivery", "resend"])
        .order(73)
        .argument(Argument {
            placeholder: "Trigger",
            required: true,
        })
        .choices(crate::triggers::trigger_choices),
    );
}
