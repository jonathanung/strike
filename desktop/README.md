# Strike desktop

Rust gateway manager and Tauri shell for the Leptos cockpit. Design,
threat model, and the build that CI does not run are in
[docs/desktop.md](../docs/desktop.md).

```sh
cargo run --manifest-path desktop/Cargo.toml -p strike-desktop
make desktop-test
```
