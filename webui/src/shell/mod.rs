//! Shared shell chrome: top bar, command palette, shortcut help.
//!
//! Other web UI streams register slots and commands through [`ShellHook`].
//! The contract is `docs/webui-shell.md`.

mod builtin;
mod chrome;
mod host;
mod icon;
mod rank;
mod registry;
mod storage;

use std::cell::Cell;

pub use chrome::Chrome;
#[allow(unused_imports)]
pub use host::{
    bind, dispatch, set_default_model_picker, set_effort_choices, set_effort_default_setter,
    set_effort_setter, set_fast_setter, set_host, set_model_choices, set_model_picker,
    set_show_palette, set_view, show_palette, unbind, HostBridge,
};
pub use icon::icon;
#[allow(unused_imports)]
pub use registry::{
    replace_prefixed, Argument, Choice, Command, CommandRun, ConfirmSpec, Group, PaletteUsage,
    ShellHook, Shortcut, TopRegion, TopSlot,
};

mod contrib {
    include!(concat!(env!("OUT_DIR"), "/shell_contrib.rs"));
}

thread_local! {
    static BOOTED: Cell<bool> = const { Cell::new(false) };
}

/// Install builtins, then every file in `src/shell/contrib/`. Once per page.
pub fn boot() {
    BOOTED.with(|booted| {
        if booted.replace(true) {
            return;
        }
        let mut hook = ShellHook::new();
        builtin::install(&mut hook);
        contrib::install(&mut hook);
        hook.install_global();
    });
}
