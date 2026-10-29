# Shell contributions

Add one `name.rs` file here to register top-bar slots or command-palette
commands. `cargo` / `trunk` pick up every `.rs` file. Do not edit
`builtin.rs`, `chrome.rs`, or another stream's file.

The file must expose `pub fn install(reg: &mut crate::shell::ShellHook)`.
The module name is the file name and must be a Rust identifier.

See `docs/webui-shell.md` for the API, slot ids, and the bind/dispatch pattern.
