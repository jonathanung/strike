//! Shared clipboard writes for the cockpit.
//!
//! Surfaces before this module wrote with `let _ = …write_text(…)` and
//! discarded the returned Promise, so a rejected write (permission denied,
//! non-secure context, unfocused document) looked exactly like success —
//! and the pairing dialog even claimed "Pairing URL copied." without
//! checking (#1582). [`copy_text`] awaits the Promise and reports failure
//! with a generic, user-facing message — never a raw browser error string
//! (#1535). Every copy surface goes through here instead of adding another
//! fire-and-forget call site: pairing (#1582), transcript (#1582),
//! local-files paths (#1555) and the Git panel diff (#1580).

use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;
use wasm_bindgen_futures::JsFuture;

/// Shown when a clipboard write cannot be completed. Deliberately generic:
/// the browser's rejection value is not user-facing copy.
pub const COPY_FAILED: &str =
    "Copy didn't work in this browser. Select the text and copy it manually.";

/// Pairing-specific failure line: the fallback is the dialog's own URL
/// field, which stays visible and selectable so pairing still works.
pub const PAIRING_COPY_FAILED: &str =
    "Copy didn't work in this browser. Select the pairing URL and copy it manually.";

/// Write `text` to the clipboard, resolving only once the browser has
/// accepted it. `Err` carries [`COPY_FAILED`], not the raw rejection.
pub async fn copy_text(text: &str) -> Result<(), String> {
    let win = web_sys::window().ok_or_else(|| COPY_FAILED.to_string())?;
    let promise = win.navigator().clipboard().write_text(text);
    JsFuture::from(promise)
        .await
        .map(|_| ())
        .map_err(|_| COPY_FAILED.to_string())
}

/// Run `f` after `ms` milliseconds (transient "Copied" labels reverting).
pub fn after_ms(ms: i32, f: impl FnOnce() + 'static) {
    let Some(win) = web_sys::window() else {
        return;
    };
    let cb = Closure::once(f);
    let _ = win.set_timeout_with_callback_and_timeout_and_arguments_0(
        cb.as_ref().unchecked_ref(),
        ms,
    );
    cb.forget();
}

/// Status line for a pairing-URL copy attempt. Success may only be
/// claimed when the write resolved; failure names the manual fallback
/// and never reads as a copy that happened.
pub fn pairing_copy_feedback(ok: bool) -> &'static str {
    if ok {
        "Pairing URL copied."
    } else {
        PAIRING_COPY_FAILED
    }
}

/// Page-note line for a transcript copy attempt (keyboard path).
pub fn transcript_copy_feedback(ok: bool) -> &'static str {
    if ok {
        "Copied."
    } else {
        COPY_FAILED
    }
}

/// Button label for a transcript copy attempt: idle, success, or failure.
pub fn transcript_copy_label(state: Option<bool>) -> &'static str {
    match state {
        None => "Copy",
        Some(true) => "Copied",
        Some(false) => "Copy failed",
    }
}

/// A diff is copyable only when it holds real diff text: not empty and
/// not the "No diff." placeholder the Git panel renders for an empty diff.
pub fn diff_copyable(text: &str) -> bool {
    !text.trim().is_empty() && text != "No diff."
}

/// Feedback line for a diff copy attempt. A truncated diff never claims a
/// complete copy: the partial nature stays in the feedback itself.
pub fn diff_copy_feedback(truncated: bool, ok: bool) -> String {
    match (ok, truncated) {
        (true, true) => "Diff copied. Diff truncated — the copy is partial.".to_string(),
        (true, false) => "Diff copied.".to_string(),
        (false, _) => COPY_FAILED.to_string(),
    }
}

/// Button label for a path copy attempt: idle, success, or failure.
pub fn path_copy_label(ok: Option<bool>) -> &'static str {
    match ok {
        None => "Copy",
        Some(true) => "Copied",
        Some(false) => "Copy failed",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pairing_feedback_claims_copied_only_on_success() {
        assert_eq!(pairing_copy_feedback(true), "Pairing URL copied.");
        let msg = pairing_copy_feedback(false);
        assert_eq!(msg, PAIRING_COPY_FAILED);
        assert!(!msg.contains("copied."), "{msg}");
        assert!(msg.contains("manually"), "{msg}");
    }

    #[test]
    fn failure_feedback_is_generic_not_a_browser_error() {
        for msg in [COPY_FAILED, PAIRING_COPY_FAILED, transcript_copy_feedback(false)] {
            assert!(!msg.contains("NotAllowedError"), "{msg}");
            assert!(!msg.contains("DOMException"), "{msg}");
        }
    }

    #[test]
    fn transcript_feedback_and_labels_cover_success_and_failure() {
        assert_eq!(transcript_copy_feedback(true), "Copied.");
        assert_eq!(transcript_copy_feedback(false), COPY_FAILED);
        assert_eq!(transcript_copy_label(None), "Copy");
        assert_eq!(transcript_copy_label(Some(true)), "Copied");
        assert_eq!(transcript_copy_label(Some(false)), "Copy failed");
    }

    #[test]
    fn diff_copyable_rejects_empty_and_placeholder() {
        assert!(!diff_copyable(""));
        assert!(!diff_copyable("  \n "));
        assert!(!diff_copyable("No diff."));
    }

    #[test]
    fn diff_copyable_accepts_real_diff() {
        assert!(diff_copyable("--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"));
    }

    #[test]
    fn truncated_copy_feedback_never_claims_complete() {
        let msg = diff_copy_feedback(true, true);
        assert!(msg.contains("truncated"), "{msg}");
        assert!(msg.contains("partial"), "{msg}");
        assert_eq!(diff_copy_feedback(false, true), "Diff copied.");
    }

    #[test]
    fn failed_copy_feedback_is_generic_not_a_browser_error() {
        let msg = diff_copy_feedback(false, false);
        assert_eq!(msg, COPY_FAILED);
        assert!(!msg.contains("NotAllowedError"), "{msg}");
        assert!(!msg.contains("DOMException"), "{msg}");
    }

    #[test]
    fn path_copy_labels_cover_idle_success_and_failure() {
        assert_eq!(path_copy_label(None), "Copy");
        assert_eq!(path_copy_label(Some(true)), "Copied");
        assert_eq!(path_copy_label(Some(false)), "Copy failed");
    }
}
