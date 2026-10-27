//! Discover `src/shell/contrib/*.rs` so each stream can register shell
//! entries by adding a file, not by editing the navbar.

use std::fs;
use std::path::PathBuf;

fn main() {
    let manifest = PathBuf::from(std::env::var("CARGO_MANIFEST_DIR").unwrap());
    let dir = manifest.join("src/shell/contrib");
    println!("cargo:rerun-if-changed={}", dir.display());
    let mut files = Vec::new();
    if let Ok(read) = fs::read_dir(&dir) {
        for entry in read.flatten() {
            let path = entry.path();
            if path.extension().and_then(|ext| ext.to_str()) != Some("rs") {
                continue;
            }
            let stem = path
                .file_stem()
                .and_then(|stem| stem.to_str())
                .unwrap_or_default()
                .to_string();
            let ident = stem.chars().next().is_some_and(|c| c.is_ascii_alphabetic())
                && stem.chars().all(|c| c.is_ascii_alphanumeric() || c == '_');
            if !ident {
                panic!("shell contrib file must be a rust identifier: {stem}.rs");
            }
            println!("cargo:rerun-if-changed={}", path.display());
            files.push((stem, path));
        }
    }
    files.sort_by(|a, b| a.0.cmp(&b.0));
    let mut body = String::new();
    for (stem, path) in &files {
        let path = path.display().to_string().replace('\\', "/");
        body.push_str(&format!("#[path = {path:?}]\nmod {stem};\n"));
    }
    let arg = if files.is_empty() { "_reg" } else { "reg" };
    body.push_str(&format!(
        "pub fn install({arg}: &mut crate::shell::ShellHook) {{\n"
    ));
    for (stem, _) in &files {
        body.push_str(&format!("    {stem}::install(reg);\n"));
    }
    body.push_str("}\n");
    let out = PathBuf::from(std::env::var("OUT_DIR").unwrap()).join("shell_contrib.rs");
    fs::write(out, body).unwrap();
}
