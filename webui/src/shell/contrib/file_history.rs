//! Open-file history (#1395): shortcut-help rows for the document
//! viewer's Back/Forward. The stack lives in `crate::file_history`; the
//! keys are bound in the document pane (`editor.rs`), which owns the
//! open path and the unsaved guard every step must pass through.

use crate::shell::{ShellHook, Shortcut};

pub fn install(reg: &mut ShellHook) {
    reg.shortcut(Shortcut {
        id: "file-history-back",
        keys: "Alt+[",
        group: "Navigation",
        group_order: 1,
        label: "Previous file in the document viewer",
        order: 20,
    });
    reg.shortcut(Shortcut {
        id: "file-history-forward",
        keys: "Alt+]",
        group: "Navigation",
        group_order: 1,
        label: "Next file in the document viewer",
        order: 21,
    });
}
