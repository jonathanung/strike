//! Session delete (#1358): permanently remove a session from the cockpit.
//!
//! The daemon already owns the semantics (`DELETE /v1/sessions/{id}` in
//! `internal/frontend/server`, host contract in `internal/frontend/host`):
//! 204 on success, and 409 when the session is open/active unless the
//! caller passes `?force=true`. This module is only the cockpit side: a
//! Delete affordance target, a confirm dialog that names the session, and
//! a second, separate force confirmation when the daemon answers 409.
//! Force is never sent silently, a cancelled dialog issues no request, and
//! nothing is removed from the list until the daemon confirms — there is
//! no undo, trash, or soft-delete, because the daemon has none.
//!
//! One daemon quirk shapes the page's success handling (in `App`'s
//! `on_deleted`, not here): the session DELETE removes the durable log
//! but leaves a live root registered in the daemon's LiveHub, so the
//! page closes that root as well, or the deleted session's row would
//! resurrect on the next refresh.

use leptos::prelude::*;
use leptos::task::spawn_local;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{self, DeleteError};

/// One session the user has asked to delete: its id and the label the
/// session list showed for it, so the dialog names what will be removed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DeleteTarget {
    pub id: String,
    pub label: String,
}

/// Percent-encode one path segment. Pure (no `js_sys`) so the request
/// shape is testable off-wasm. Unreserved characters — including the `.`
/// that real session ids contain — pass through; everything else,
/// including `/`, `?`, and `#`, is encoded so an id cannot change the
/// path's meaning.
pub fn encode_segment(id: &str) -> String {
    let mut out = String::with_capacity(id.len());
    for b in id.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

/// The DELETE path for one session. `force` is only ever true after the
/// separate force confirmation, never on the first attempt.
pub fn delete_path(id: &str, force: bool) -> String {
    let path = format!("/v1/sessions/{}", encode_segment(id));
    if force {
        format!("{path}?force=true")
    } else {
        path
    }
}

/// The daemon answers 409 for a session that is open (or active) when
/// force was not given. That is the only status that may advance the
/// dialog to the force confirmation.
pub fn is_conflict(status: u16) -> bool {
    status == 409
}

pub fn confirm_heading(label: &str) -> String {
    format!("Delete session \u{201c}{label}\u{201d}?")
}

pub const CONFIRM_BODY: &str = "This permanently removes the session's durable log and metadata. This cannot be undone.";

pub fn force_heading(label: &str) -> String {
    format!("Session \u{201c}{label}\u{201d} is active")
}

pub const FORCE_BODY: &str = "The daemon refused to delete it without force because the session is open. Force deleting closes it and removes its durable log and metadata. This cannot be undone.";

/// The open session after `deleted` is removed: `None` when the deleted
/// session was the open one (the cockpit falls back to its empty/list
/// state), otherwise the current open session, unchanged.
pub fn remaining_active(active: &str, deleted: &str) -> Option<String> {
    if active.is_empty() || active == deleted {
        None
    } else {
        Some(active.to_string())
    }
}

/// Issue one DELETE for `id`. The error keeps the daemon's status and
/// body so the caller can branch on 409 and surface the rest verbatim.
pub async fn delete_session(
    base: &str,
    token: &str,
    id: &str,
    force: bool,
) -> Result<(), DeleteError> {
    api::delete_with_status(base, token, &delete_path(id, force)).await
}

#[component]
pub fn SessionDeleteDialog(
    target: RwSignal<Option<DeleteTarget>>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    on_deleted: Callback<String>,
) -> impl IntoView {
    // Some(daemon message) once the daemon has answered 409 and the
    // dialog is showing the separate force confirmation.
    let force_msg = RwSignal::new(None::<String>);
    // A failure that is not the 409 force gate, shown verbatim.
    let error = RwSignal::new(String::new());
    let busy = RwSignal::new(false);

    // Every new target starts at the first confirmation, clean.
    Effect::new(move |_| {
        let _ = target.get();
        force_msg.set(None);
        error.set(String::new());
        busy.set(false);
    });

    let cancel = move || {
        if !busy.get_untracked() {
            target.set(None);
        }
    };

    // Escape cancels, like the other cockpit dialogs. The component is
    // mounted once for the page's lifetime, so the listener is too.
    let on_key = Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
        if ev.key() == "Escape" && target.get_untracked().is_some() {
            cancel();
        }
    });
    if let Some(w) = web_sys::window() {
        let _ = w.add_event_listener_with_callback("keydown", on_key.as_ref().unchecked_ref());
    }
    on_key.forget();

    let attempt = move |force: bool| {
        let Some(t) = target.get_untracked() else {
            return;
        };
        let (b, tok) = (base.get_untracked(), token.get_untracked());
        busy.set(true);
        error.set(String::new());
        spawn_local(async move {
            match delete_session(&b, &tok, &t.id, force).await {
                Ok(()) => {
                    busy.set(false);
                    target.set(None);
                    on_deleted.run(t.id.clone());
                }
                Err(e) if !force && is_conflict(e.status) => {
                    busy.set(false);
                    force_msg.set(Some(e.to_string()));
                }
                Err(e) => {
                    busy.set(false);
                    error.set(e.to_string());
                }
            }
        });
    };

    view! {
        <Show when=move || target.get().is_some()>
            <div
                class="session-delete-backdrop"
                on:click=move |_| cancel()
            >
                <div
                    class="session-delete-dialog"
                    role="dialog"
                    aria-modal="true"
                    aria-label="Delete session"
                    data-session-delete-dialog="true"
                    on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()
                >
                    {move || {
                        let Some(t) = target.get() else {
                            return ().into_any();
                        };
                        if let Some(msg) = force_msg.get() {
                            view! {
                                <h2>{force_heading(&t.label)}</h2>
                                <p class="session-delete-daemon" role="alert">{msg}</p>
                                <p>{FORCE_BODY}</p>
                                <Show when=move || !error.get().is_empty()>
                                    <p class="session-delete-error" role="alert">{move || error.get()}</p>
                                </Show>
                                <div class="session-delete-actions">
                                    <button
                                        class="secondary"
                                        data-session-delete-cancel="true"
                                        disabled=move || busy.get()
                                        on:click=move |_| cancel()
                                    >
                                        "Cancel"
                                    </button>
                                    <button
                                        class="danger"
                                        data-session-delete-force="true"
                                        disabled=move || busy.get()
                                        on:click=move |_| attempt(true)
                                    >
                                        "Force delete"
                                    </button>
                                </div>
                            }
                            .into_any()
                        } else {
                            view! {
                                <h2>{confirm_heading(&t.label)}</h2>
                                <p>{CONFIRM_BODY}</p>
                                <Show when=move || !error.get().is_empty()>
                                    <p class="session-delete-error" role="alert">{move || error.get()}</p>
                                </Show>
                                <div class="session-delete-actions">
                                    <button
                                        class="secondary"
                                        data-session-delete-cancel="true"
                                        disabled=move || busy.get()
                                        on:click=move |_| cancel()
                                    >
                                        "Cancel"
                                    </button>
                                    <button
                                        class="danger"
                                        data-session-delete-confirm="true"
                                        disabled=move || busy.get()
                                        on:click=move |_| attempt(false)
                                    >
                                        "Delete"
                                    </button>
                                </div>
                            }
                            .into_any()
                        }
                    }}
                </div>
            </div>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn delete_path_only_forces_when_asked() {
        assert_eq!(delete_path("abc", false), "/v1/sessions/abc");
        assert_eq!(delete_path("abc", true), "/v1/sessions/abc?force=true");
    }

    #[test]
    fn delete_path_encodes_the_id() {
        assert_eq!(delete_path("a b/c", false), "/v1/sessions/a%20b%2Fc");
        assert_eq!(
            delete_path("a b/c", true),
            "/v1/sessions/a%20b%2Fc?force=true"
        );
        assert_eq!(delete_path("a?force=true", false), "/v1/sessions/a%3Fforce%3Dtrue");
    }

    #[test]
    fn encode_segment_keeps_real_dotted_session_ids() {
        // Session ids are minted with dots (see session.NewID); the path
        // must carry them through untouched or the daemon cannot match.
        assert_eq!(encode_segment("2026.10.26-abc123"), "2026.10.26-abc123");
        assert_eq!(encode_segment(""), "");
    }

    #[test]
    fn only_409_is_the_force_gate() {
        assert!(is_conflict(409));
        assert!(!is_conflict(200));
        assert!(!is_conflict(400));
        assert!(!is_conflict(401));
        assert!(!is_conflict(404));
        assert!(!is_conflict(500));
        assert!(!is_conflict(0));
    }

    #[test]
    fn confirm_copy_names_the_session_and_says_permanent() {
        let heading = confirm_heading("Quarterly review");
        assert!(heading.contains("Quarterly review"), "{heading}");
        assert!(CONFIRM_BODY.contains("durable log and metadata"));
        assert!(CONFIRM_BODY.contains("cannot be undone"));
    }

    #[test]
    fn force_copy_is_a_separate_active_warning() {
        let heading = force_heading("Quarterly review");
        assert!(heading.contains("Quarterly review"), "{heading}");
        assert!(heading.contains("active"), "{heading}");
        assert!(FORCE_BODY.contains("without force"));
        assert!(FORCE_BODY.contains("cannot be undone"));
    }

    #[test]
    fn remaining_active_falls_back_when_the_open_session_is_deleted() {
        assert_eq!(remaining_active("a", "a"), None);
        assert_eq!(remaining_active("a", "b"), Some("a".to_string()));
        assert_eq!(remaining_active("", "b"), None);
    }

    #[test]
    fn delete_error_display_keeps_status_and_daemon_body() {
        let e = DeleteError {
            status: 409,
            body: "{\"error\":\"session is open\"}".into(),
        };
        assert_eq!(e.to_string(), "HTTP 409 {\"error\":\"session is open\"}");
        let bare = DeleteError {
            status: 500,
            body: String::new(),
        };
        assert_eq!(bare.to_string(), "HTTP 500");
        let transport = DeleteError {
            status: 0,
            body: "network unreachable".into(),
        };
        assert_eq!(transport.to_string(), "network unreachable");
    }
}
