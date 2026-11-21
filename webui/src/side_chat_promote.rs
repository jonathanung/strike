//! Side chat promote and stop (#1363).
//!
//! **Add to main chat** is client-side composer insertion only: a settled
//! side answer is appended to the main composer as an attributed, unsent
//! draft. Nothing is posted. The user reviews it and presses Send through
//! the normal input path, so the side chat's read-only contract (#1278:
//! no `user.input`, no `permission.reply`) is unchanged.
//!
//! **Stop** aborts the in-flight `/v1/sidechat/ask` fetch. The daemon runs
//! the side model under the request context, so the abort cancels the
//! provider call and no turn is saved (see `sidechat_cancel_test.go`).

use std::cell::RefCell;

use serde_json::Value;

use crate::api;

pub const ATTRIBUTION: &str = "From side chat:";

/// The answer as a markdown blockquote under an attribution line.
pub fn quote_block(answer: &str) -> String {
    let mut out = format!("> {ATTRIBUTION}\n");
    let body = answer.trim();
    for line in body.lines() {
        if line.trim().is_empty() {
            out.push_str(">\n");
        } else {
            out.push_str("> ");
            out.push_str(line);
            out.push('\n');
        }
    }
    out
}

/// The main draft after inserting `answer`. A non-empty draft is kept and
/// the quote goes below it after a blank line; nothing is overwritten.
/// A blank answer leaves the draft as it is.
pub fn insert_into_draft(draft: &str, answer: &str) -> String {
    if answer.trim().is_empty() {
        return draft.to_string();
    }
    let quote = quote_block(answer);
    if draft.trim().is_empty() {
        format!("{quote}\n")
    } else {
        format!("{}\n\n{quote}\n", draft.trim_end())
    }
}

pub const STOPPED: &str = "Stopped. The side model call was canceled and nothing was saved.";
pub const PROMOTED: &str = "Added to the main composer as a draft. Nothing was sent.";

thread_local! {
    static INFLIGHT: RefCell<Option<(u64, web_sys::AbortController)>> = const { RefCell::new(None) };
    static NEXT: RefCell<u64> = const { RefCell::new(0) };
}

/// Starts tracking one side ask. Returns its generation and abort signal.
/// A previous controller is aborted (the panel allows one ask at a time).
pub fn begin() -> Option<(u64, web_sys::AbortSignal)> {
    let controller = web_sys::AbortController::new().ok()?;
    let signal = controller.signal();
    let gen = NEXT.with(|n| {
        let mut n = n.borrow_mut();
        *n += 1;
        *n
    });
    INFLIGHT.with(|slot| {
        if let Some((_, old)) = slot.borrow_mut().replace((gen, controller)) {
            old.abort();
        }
    });
    Some((gen, signal))
}

/// Aborts the in-flight ask. Returns false when nothing was running.
pub fn stop() -> bool {
    INFLIGHT.with(|slot| match slot.borrow_mut().take() {
        Some((_, controller)) => {
            controller.abort();
            true
        }
        None => false,
    })
}

/// Clears the slot when `gen` is still the in-flight ask.
pub fn finish(gen: u64) {
    INFLIGHT.with(|slot| {
        let mut slot = slot.borrow_mut();
        if slot.as_ref().map(|(g, _)| *g) == Some(gen) {
            *slot = None;
        }
    });
}

/// POST JSON with an abort signal. An aborted request returns
/// `Err(ABORTED)`.
pub const ABORTED: &str = "aborted";

pub async fn post_abortable(
    base: &str,
    token: &str,
    path: &str,
    body: Value,
    signal: &web_sys::AbortSignal,
) -> Result<Value, String> {
    let mut req = gloo_net::http::Request::post(&api::api(base, path))
        .credentials(web_sys::RequestCredentials::Include)
        .abort_signal(Some(signal));
    if !token.trim().is_empty() {
        req = req.header("Authorization", &api::auth_header(token.trim()));
    }
    let sent = req.json(&body).map_err(|e| e.to_string())?.send().await;
    let resp = match sent {
        Ok(resp) => resp,
        Err(_) if signal.aborted() => return Err(ABORTED.into()),
        Err(e) => return Err(e.to_string()),
    };
    let status = resp.status();
    let text = match resp.text().await {
        Ok(text) => text,
        Err(_) if signal.aborted() => return Err(ABORTED.into()),
        Err(e) => return Err(e.to_string()),
    };
    if !(200..300).contains(&status) {
        return Err(format!("HTTP {status} {text}"));
    }
    if text.trim().is_empty() {
        return Ok(Value::Null);
    }
    serde_json::from_str(&text).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn insertion_lands_as_an_attributed_quote() {
        let got = insert_into_draft("", "Use the v2 API.\n\nIt is faster.");
        assert_eq!(got, "> From side chat:\n> Use the v2 API.\n>\n> It is faster.\n\n");
    }

    #[test]
    fn insertion_keeps_an_existing_draft() {
        let draft = "please fix the build\n  and add tests  ";
        let got = insert_into_draft(draft, "The failing test is foo_test.go.");
        assert!(got.starts_with("please fix the build\n  and add tests\n\n> From side chat:\n"), "{got:?}");
        assert!(got.contains("> The failing test is foo_test.go.\n"));
        // Every character of the original draft text survives.
        assert!(got.contains(draft.trim_end()));
        // Inserting twice appends twice; nothing is replaced.
        let twice = insert_into_draft(&got, "second");
        assert!(twice.starts_with(got.trim_end()));
        assert!(twice.ends_with("> second\n\n"));
    }

    #[test]
    fn blank_answer_changes_nothing() {
        assert_eq!(insert_into_draft("keep", "   \n "), "keep");
        assert_eq!(insert_into_draft("", ""), "");
    }
}
