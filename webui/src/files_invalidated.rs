//! `files.invalidated` in the cockpit (#1380).
//!
//! The daemon emits `files.invalidated` (`pkg/protocol` `FilesInvalidated`:
//! `paths`, `reason`) when files changed underneath the session. The TUI
//! refreshes its file windows and shows "files changed — agent will
//! re-read: <paths>"; this module is the cockpit's half of that parity:
//!
//! - the transcript turns the event into one muted notice line (wording
//!   from the TUI, path list capped), merging back-to-back invalidations
//!   into the same line so a burst never stacks notices;
//! - the shell bumps a `files_rev` signal that the Files/Changes load
//!   `Effect` observes, through [`Debounce`], so a burst of events — a
//!   session replay, or an agent touching many files — costs one refetch
//!   of the open panel, not one per event.

use leptos::prelude::{RwSignal, Update};
use serde_json::Value;
use std::cell::Cell;
use std::rc::Rc;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

/// `Message::key` of the notice line; the view exposes it as `data-kind`
/// so the CSS block can mute it.
pub const KIND: &str = "files.invalidated";

/// TUI wording (`internal/frontend/tui/app/_src/app/apply_event.go`).
pub const NOTICE_PREFIX: &str = "files changed — agent will re-read: ";

/// Paths named in the notice before collapsing to "+N more".
pub const SHOWN_PATHS: usize = 3;

/// Quiet gap (ms) before an invalidation refetches the open panel. A
/// replayed log arrives as one burst well inside this gap.
pub const DEBOUNCE_MS: i32 = 400;

/// True when a raw envelope is a `files.invalidated` event. Matches the
/// quoted type token the same way the shell already spots
/// `compaction.completed`; quoted text inside payload strings is escaped
/// (`\"`) by JSON and cannot match.
pub fn is_event(raw: &str) -> bool {
    raw.contains("\"files.invalidated\"")
}

/// Non-empty, trimmed paths from the event's `data.paths`.
pub fn paths_from(data: &Value) -> Vec<String> {
    let mut out = Vec::new();
    if let Some(items) = data.get("paths").and_then(|p| p.as_array()) {
        let incoming: Vec<String> = items
            .iter()
            .filter_map(|p| p.as_str())
            .map(|p| p.trim().to_string())
            .collect();
        merge_paths(&mut out, &incoming);
    }
    out
}

/// Append `incoming` to `all`, skipping empties and paths already listed;
/// first-seen order is kept.
pub fn merge_paths(all: &mut Vec<String>, incoming: &[String]) {
    for path in incoming {
        if !path.is_empty() && !all.iter().any(|p| p == path) {
            all.push(path.clone());
        }
    }
}

/// The notice text: TUI prefix, first [`SHOWN_PATHS`] paths, then
/// "+N more" for the rest.
pub fn notice_body(paths: &[String]) -> String {
    let shown: Vec<&str> = paths.iter().take(SHOWN_PATHS).map(String::as_str).collect();
    let mut body = format!("{NOTICE_PREFIX}{}", shown.join(", "));
    if paths.len() > SHOWN_PATHS {
        body.push_str(&format!(" +{} more", paths.len() - SHOWN_PATHS));
    }
    body
}

/// Trailing-edge debounce: each `arm` supersedes earlier ones, and only
/// the timer holding the latest generation may fire.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Debounce {
    gen: u32,
}

impl Debounce {
    /// Start a new wait; returns the generation its timer must present.
    pub fn arm(&mut self) -> u32 {
        self.gen = self.gen.wrapping_add(1);
        self.gen
    }

    /// Whether the timer armed with `gen` is still the latest.
    pub fn fires(&self, gen: u32) -> bool {
        self.gen == gen
    }
}

/// Arm the debounce; when the quiet gap passes with no newer arm, bump
/// `rev` once.
pub fn refresh_soon(state: Rc<Cell<Debounce>>, rev: RwSignal<u32>) {
    let mut d = state.get();
    let gen = d.arm();
    state.set(d);
    let Some(win) = web_sys::window() else {
        return;
    };
    let cb = Closure::once(move || {
        if state.get().fires(gen) {
            rev.update(|r| *r = r.wrapping_add(1));
        }
    });
    let _ = win.set_timeout_with_callback_and_timeout_and_arguments_0(
        cb.as_ref().unchecked_ref(),
        DEBOUNCE_MS,
    );
    cb.forget();
}

#[cfg(test)]
mod tests {
    use super::*;

    fn s(items: &[&str]) -> Vec<String> {
        items.iter().map(|p| p.to_string()).collect()
    }

    #[test]
    fn is_event_matches_type_token_only() {
        let cases = [
            (r#"{"type":"files.invalidated","data":{"paths":["a"]}}"#, true),
            (r#"{"type":"text.delta","data":{"text":"files.invalidated"}}"#, false),
            (r#"{"type":"text.delta","data":{"text":"say \"files.invalidated\""}}"#, false),
            (r#"{"type":"files.changed","data":{}}"#, false),
        ];
        for (raw, want) in cases {
            assert_eq!(is_event(raw), want, "{raw}");
        }
    }

    #[test]
    fn paths_from_trims_dedups_and_skips_junk() {
        let cases = [
            (serde_json::json!({"paths": ["a.go", " b.go ", "", "a.go", 7]}), s(&["a.go", "b.go"])),
            (serde_json::json!({"paths": []}), Vec::new()),
            (serde_json::json!({"reason": "external"}), Vec::new()),
            (serde_json::json!({"paths": "a.go"}), Vec::new()),
        ];
        for (data, want) in cases {
            assert_eq!(paths_from(&data), want, "{data}");
        }
    }

    #[test]
    fn merge_paths_keeps_first_seen_order() {
        let mut all = s(&["a", "b"]);
        merge_paths(&mut all, &s(&["b", "c", "", "a", "d"]));
        assert_eq!(all, s(&["a", "b", "c", "d"]));
    }

    #[test]
    fn notice_body_caps_named_paths() {
        let cases = [
            (s(&["a.go"]), format!("{NOTICE_PREFIX}a.go")),
            (s(&["a", "b", "c"]), format!("{NOTICE_PREFIX}a, b, c")),
            (s(&["a", "b", "c", "d"]), format!("{NOTICE_PREFIX}a, b, c +1 more")),
            (s(&["a", "b", "c", "d", "e", "f"]), format!("{NOTICE_PREFIX}a, b, c +3 more")),
        ];
        for (paths, want) in cases {
            assert_eq!(notice_body(&paths), want);
        }
    }

    #[test]
    fn debounce_only_latest_generation_fires() {
        let mut d = Debounce::default();
        let first = d.arm();
        let second = d.arm();
        assert!(!d.fires(first), "superseded timer must not fire");
        assert!(d.fires(second), "latest timer fires");
        let third = d.arm();
        assert!(!d.fires(second));
        assert!(d.fires(third));
    }
}
