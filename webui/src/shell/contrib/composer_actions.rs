//! Composer controls that left the chip row (#1592). Send, Stop, the
//! textarea, and the + menu stay. Approval mode and the model stay as
//! passive status. These commands are how they are changed.

use std::rc::Rc;

use crate::model_menu::effort_levels;
use crate::shell::{Argument, Choice, Command, Group, ShellHook};

fn go(id: &'static str) -> Rc<dyn Fn(&str) -> Result<(), String>> {
    Rc::new(move |args| crate::shell::dispatch(id, args))
}

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "composer.attach",
            Group::Session,
            "Attach files",
            "Pick images or files. Drag, drop, and paste still attach.",
            go("composer.attach"),
        )
        .keywords(&["attach", "upload", "image", "pdf", "file"])
        .order(60),
    );
    reg.command(
        Command::new(
            "composer.voice",
            Group::Session,
            "Dictate (voice input)",
            "Dictate into the composer with the browser speech API",
            go("composer.voice"),
        )
        .keywords(&["voice", "dictate", "mic", "speech"])
        .order(61)
        .visible(crate::chat_mode::dictation_available),
    );
    reg.command(
        Command::new(
            "composer.mention",
            Group::Session,
            "Mention a file",
            "Insert @ and search the working tree",
            go("composer.mention"),
        )
        .keywords(&["mention", "@", "file", "reference"])
        .order(62),
    );
    reg.command(
        Command::new(
            "composer.shell",
            Group::Session,
            "Run a shell command",
            "Prefix ! and ask before it runs",
            go("composer.shell"),
        )
        .keywords(&["shell", "run", "!", "terminal", "command"])
        .order(63),
    );
    reg.command(
        Command::new(
            "composer.skill",
            Group::Session,
            "Use a skill",
            "Insert $ and pick a skill",
            go("composer.skill"),
        )
        .keywords(&["skill", "$", "workflow"])
        .order(64),
    );
    reg.command(
        Command::new(
            "enter.toggle",
            Group::Settings,
            "Toggle Enter behaviour: Steer / Queue",
            "While a turn runs, Enter either steers it or waits",
            go("enter.toggle"),
        )
        .keywords(&["steer", "queue", "enter", "send mode"])
        .order(12),
    );
    reg.command(
        Command::new(
            "mode-default",
            Group::Settings,
            "Set approval mode: Default",
            "Ask before tools that change things",
            Rc::new(|_| crate::shell::host::set_perm("default")),
        )
        .keywords(&["approval", "mode", "permission"])
        .order(30),
    );
    reg.command(
        Command::new(
            "mode-plan",
            Group::Settings,
            "Set approval mode: Plan",
            "Discuss the change before any tool runs",
            Rc::new(|_| crate::shell::host::set_perm("plan")),
        )
        .keywords(&["approval", "mode", "permission", "plan"])
        .order(31),
    );
    reg.command(
        Command::new(
            "mode-soft",
            Group::Settings,
            "Set approval mode: Soft",
            "Ask less often for routine tools",
            Rc::new(|_| crate::shell::host::set_perm("soft-approve")),
        )
        .keywords(&["approval", "mode", "permission"])
        .order(32),
    );
    reg.command(
        Command::new(
            "mode-edits",
            Group::Settings,
            "Set approval mode: Edits",
            "Apply file edits without asking",
            Rc::new(|_| crate::shell::host::set_perm("accept-edits")),
        )
        .keywords(&["approval", "mode", "permission", "edits"])
        .order(33),
    );
    reg.command(
        Command::new(
            "mode-yolo",
            Group::Settings,
            "Set approval mode: Yolo",
            "Apply every tool call without asking",
            Rc::new(|_| crate::shell::host::set_perm("yolo")),
        )
        .keywords(&["approval", "mode", "permission", "yolo"])
        .order(34),
    );
    reg.command(
        Command::new(
            "approvals.rules",
            Group::Settings,
            "Approval rules and audit",
            "Allow and deny patterns. Deny rules still apply in Yolo.",
            go("approvals.rules"),
        )
        .keywords(&["rules", "audit", "allow", "deny", "permissions"])
        .order(35),
    );
    reg.command(
        Command::new(
            "model.effort",
            Group::Model,
            "Set reasoning effort",
            "How much the model thinks before it answers",
            go("model.effort"),
        )
        .keywords(&["effort", "reasoning", "thinking", "fast"])
            .order(20)
        .argument(Argument {
            placeholder: "Filter effort",
            required: false,
        })
        .choices(|| {
            effort_levels()
                .iter()
                .map(|(id, detail)| Choice {
                    id: (*id).into(),
                    label: (*id).into(),
                    detail: (*detail).into(),
                })
                .collect()
        }),
    );
    reg.command(
        Command::new(
            "model.effort-default",
            Group::Model,
            "Set default reasoning effort",
            "Save the effort as the global default",
            go("model.effort-default"),
        )
        .keywords(&["effort", "reasoning", "default", "thinking"])
        .order(24)
        .argument(Argument {
            placeholder: "Filter effort",
            required: false,
        })
        .choices(|| {
            effort_levels()
                .iter()
                .map(|(id, detail)| Choice {
                    id: (*id).into(),
                    label: (*id).into(),
                    detail: (*detail).into(),
                })
                .collect()
        }),
    );
    reg.command(
        Command::new(
            "model.pin",
            Group::Model,
            "Pin model",
            "Pin or unpin the session model in the picker",
            go("model.pin"),
        )
        .keywords(&["pin", "favourite", "model"])
        .order(21),
    );
    reg.command(
        Command::new(
            "model.default",
            Group::Model,
            "Set default model",
            "Save a model as the global default",
            go("model.default"),
        )
        .keywords(&["default", "model", "save"])
        .order(22)
        .argument(Argument {
            placeholder: "Filter models",
            required: false,
        })
        .choices(crate::shell::host::model_choices),
    );
    reg.command(
        Command::new(
            "providers.add",
            Group::Providers,
            "Add provider",
            "Open Providers to connect an API host",
            Rc::new(|_| crate::shell::host::set_view("providers")),
        )
        .keywords(&["provider", "api key", "connect"])
        .order(6),
    );
    reg.command(
        Command::new(
            "composer.context",
            Group::Model,
            "View context usage",
            "Show how full the context window is",
            go("composer.context"),
        )
        .keywords(&["context", "tokens", "window", "compact"])
        .order(23),
    );
    reg.command(
        Command::new(
            "side-chat",
            Group::Session,
            "Open side chat",
            "Ask about this session without steering it or approving a tool",
            Rc::new(|_| crate::shell::host::open_side_chat()),
        )
        .shortcut("Alt+S")
        .keywords(&["side", "chat", "split"])
        .order(75),
    );
    reg.command(
        Command::new(
            "queue.move-up",
            Group::Session,
            "Move queued message up",
            "Move the hovered queued message earlier",
            go("queue.move-up"),
        )
        .keywords(&["reorder", "queue", "move", "priority"])
        .order(64),
    );
    reg.command(
        Command::new(
            "queue.move-down",
            Group::Session,
            "Move queued message down",
            "Move the hovered queued message later",
            go("queue.move-down"),
        )
        .keywords(&["reorder", "queue", "move", "priority"])
        .order(65),
    );
    reg.command(
        Command::new(
            "workspaces.change",
            Group::Navigate,
            "Workspaces: Change workspace",
            "Pick a directory for this daemon",
            go("workspaces.change"),
        )
        .keywords(&["workspace", "folder", "directory", "switch"])
        .order(31),
    );
}
