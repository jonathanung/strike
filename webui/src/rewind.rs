//! Session rewind (#1336): restore the conversation and/or code to before
//! an earlier turn, from the Leptos transcript.
//!
//! The daemon describes the rewind (`GET /v1/sessions/{id}/rewind_preview`
//! — turns undone, files the harness changed in them, checkpoint-skipped
//! counts, uncovered mutations such as bash) from the session record; this
//! module shows that preview and applies the rewind through the engine's
//! existing `rewind` protocol op. The op drops the last completed turn, so
//! rewinding to before turn N sends it once per undone turn, in order. No
//! checkpointing is reimplemented here.
//!
//! Modes follow what the checkpoint data supports: conversation + code
//! (`restoreFiles: true`) or conversation only (`restoreFiles: false`).
//! Code only is listed but disabled with its reason — the engine restores
//! files only together with the turn. After a conversation restore the
//! picked turn's prompt is put back in the composer, never sent.
//!
//! A code restore over turns with uncovered mutations needs an explicit
//! acknowledgement first: the warning blocks a silent restore.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{get_json, js_encode, post_op};

/// One rewind request from the transcript: the session and the picked
/// turn's fork keep-count (`Message::keep_events`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RewindTarget {
    pub session: String,
    pub keep: i32,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct PreviewFile {
    pub path: String,
    #[serde(default)]
    pub kind: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct Preview {
    #[serde(default)]
    pub turn: u32,
    #[serde(default)]
    pub turns: u32,
    #[serde(default, rename = "totalTurns")]
    pub total_turns: u32,
    #[serde(default)]
    pub prompt: String,
    #[serde(default)]
    pub files: Vec<PreviewFile>,
    #[serde(default, rename = "checkpointSkipped")]
    pub checkpoint_skipped: u32,
    #[serde(default)]
    pub uncovered: Vec<String>,
    #[serde(default, rename = "beyondCheckpoints")]
    pub beyond_checkpoints: bool,
    #[serde(default)]
    pub disabled: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Mode {
    Both,
    Conversation,
    Code,
}

impl Mode {
    pub fn label(self) -> &'static str {
        match self {
            Mode::Both => "Restore code and conversation",
            Mode::Conversation => "Restore conversation only (keep current code)",
            Mode::Code => "Restore code only (keep the conversation)",
        }
    }

    /// `None` when the mode can run; otherwise why not.
    pub fn unavailable(self, p: &Preview) -> Option<&'static str> {
        match self {
            Mode::Code => Some(CODE_ONLY_REASON),
            Mode::Both if p.files.is_empty() => {
                Some("These turns changed no files through Strike's tools, so there is no code to restore.")
            }
            _ => None,
        }
    }

    pub fn restores_files(self) -> bool {
        matches!(self, Mode::Both | Mode::Code)
    }
}

pub const CODE_ONLY_REASON: &str = "Not supported yet: Strike's checkpoints restore files only together with the turn they belong to.";

pub fn preview_path(t: &RewindTarget) -> String {
    format!(
        "/v1/sessions/{}/rewind_preview?keep={}",
        js_encode(&t.session),
        t.keep
    )
}

/// The mode a fresh preview opens on: code and conversation when there are
/// files to restore, otherwise conversation only.
pub fn default_mode(p: &Preview) -> Mode {
    if Mode::Both.unavailable(p).is_none() {
        Mode::Both
    } else {
        Mode::Conversation
    }
}

pub fn heading(p: &Preview) -> String {
    format!("Rewind to before turn {}?", p.turn)
}

pub fn scope_line(p: &Preview) -> String {
    if p.turns <= 1 {
        format!("Undoes turn {} (the latest).", p.turn)
    } else {
        format!(
            "Undoes {} turns: turn {} through the latest (turn {}).",
            p.turns, p.turn, p.total_turns
        )
    }
}

/// Warnings to show for a mode. Code-restoring modes surface what will NOT
/// be restored; conversation-only states that disk stays as it is.
pub fn warnings(p: &Preview, mode: Mode) -> Vec<String> {
    let mut out = Vec::new();
    if !mode.restores_files() {
        if !p.files.is_empty() {
            out.push(format!(
                "Files changed in these turns stay as they are now ({} path(s)).",
                p.files.len()
            ));
        }
        return out;
    }
    if !p.uncovered.is_empty() {
        out.push(format!(
            "These turns made changes Strike cannot undo ({}): those disk changes stay even after the restore.",
            p.uncovered.join(", ")
        ));
    }
    if p.checkpoint_skipped > 0 {
        out.push(format!(
            "{} file(s) were too large or unreadable to checkpoint and will not be restored.",
            p.checkpoint_skipped
        ));
    }
    if p.beyond_checkpoints {
        out.push(
            "Strike keeps file checkpoints for the newest 50 turns only; older turns' files will not be restored."
                .into(),
        );
    }
    out
}

/// A code restore over uncovered mutations must be acknowledged first.
pub fn needs_ack(p: &Preview, mode: Mode) -> bool {
    mode.restores_files() && !p.uncovered.is_empty()
}

/// Why Confirm is blocked right now, if it is.
pub fn blocked(p: &Preview, mode: Mode, acked: bool, busy_turn: bool) -> Option<String> {
    if !p.disabled.is_empty() {
        return Some(p.disabled.clone());
    }
    if busy_turn {
        return Some("A turn is running; rewind once it finishes.".into());
    }
    if let Some(reason) = mode.unavailable(p) {
        return Some(reason.into());
    }
    if needs_ack(p, mode) && !acked {
        return Some("Acknowledge the warning above to restore code.".into());
    }
    None
}

/// Map a preview failure ("HTTP <status> <body>") to a sentence.
pub fn preview_error(err: &str) -> String {
    if err.contains("HTTP 409") {
        "That turn is no longer in the session history (already rewound, or not completed).".into()
    } else if err.contains("HTTP 404") {
        "This session is not on the daemon.".into()
    } else {
        format!("Could not load the rewind preview: {err}")
    }
}

fn kind_label(kind: &str) -> &'static str {
    match kind {
        "create" => "created",
        "delete" => "deleted",
        _ => "changed",
    }
}

#[derive(Clone, Debug, PartialEq)]
enum Phase {
    Loading,
    Ready(Preview),
    Failed(String),
}

/// The preview + confirm dialog. Mounted once; `target` opens it.
/// `on_done(prompt, mode)` runs after every rewind op was accepted.
#[component]
pub fn RewindDialog(
    target: RwSignal<Option<RewindTarget>>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    busy_turn: Signal<bool>,
    on_done: Callback<(String, Mode, u32)>,
) -> impl IntoView {
    let phase = RwSignal::new(Phase::Loading);
    let mode = RwSignal::new(Mode::Both);
    let acked = RwSignal::new(false);
    let applying = RwSignal::new(false);
    let error = RwSignal::new(String::new());

    Effect::new(move |_| {
        let Some(t) = target.get() else {
            return;
        };
        phase.set(Phase::Loading);
        acked.set(false);
        applying.set(false);
        error.set(String::new());
        let (b, tok) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let result = get_json::<Preview>(&b, &tok, &preview_path(&t)).await;
            if target.get_untracked().as_ref() != Some(&t) {
                return;
            }
            match result {
                Ok(p) => {
                    mode.set(default_mode(&p));
                    phase.set(Phase::Ready(p));
                }
                Err(e) => phase.set(Phase::Failed(preview_error(&e))),
            }
        });
    });

    let cancel = move || {
        if !applying.get_untracked() {
            target.set(None);
        }
    };
    let on_key =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            if ev.key() == "Escape" && target.get_untracked().is_some() {
                cancel();
            }
        });
    if let Some(w) = web_sys::window() {
        let _ = w.add_event_listener_with_callback("keydown", on_key.as_ref().unchecked_ref());
    }
    on_key.forget();

    let apply = move || {
        let (Some(t), Phase::Ready(p)) = (target.get_untracked(), phase.get_untracked()) else {
            return;
        };
        let m = mode.get_untracked();
        if blocked(&p, m, acked.get_untracked(), busy_turn.get_untracked()).is_some() {
            return;
        }
        applying.set(true);
        error.set(String::new());
        let (b, tok) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let data = serde_json::json!({ "restoreFiles": m.restores_files() });
            for done in 0..p.turns {
                if let Err(e) = post_op(&b, &tok, &t.session, "rewind", data.clone()).await {
                    applying.set(false);
                    error.set(format!(
                        "Rewind stopped after {done} of {} turn(s): {e}",
                        p.turns
                    ));
                    return;
                }
            }
            applying.set(false);
            target.set(None);
            on_done.run((p.prompt.clone(), m, p.turns));
        });
    };

    view! {
        <Show when=move || target.get().is_some()>
            <div class="rewind-backdrop" on:click=move |_| cancel()>
                <div
                    class="rewind-dialog"
                    role="dialog"
                    aria-modal="true"
                    aria-labelledby="rewind-heading"
                    data-rewind="dialog"
                    on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()
                >
                    {move || match phase.get() {
                        Phase::Loading => view! {
                            <h2 id="rewind-heading">"Rewind"</h2>
                            <p class="hint" role="status">"Loading what this rewind would undo\u{2026}"</p>
                            <div class="rewind-actions">
                                <button type="button" class="secondary" data-rewind="cancel" on:click=move |_| cancel()>"Cancel"</button>
                            </div>
                        }.into_any(),
                        Phase::Failed(msg) => view! {
                            <h2 id="rewind-heading">"Rewind"</h2>
                            <p class="rewind-error" role="alert">{msg}</p>
                            <div class="rewind-actions">
                                <button type="button" class="secondary" data-rewind="cancel" on:click=move |_| cancel()>"Close"</button>
                            </div>
                        }.into_any(),
                        Phase::Ready(p) => {
                            let shown: Vec<PreviewFile> = p.files.iter().take(20).cloned().collect();
                            let more = p.files.len().saturating_sub(shown.len());
                            let prompt = p.prompt.clone();
                            let p_modes = p.clone();
                            let p_warn = p.clone();
                            let p_block = p.clone();
                            view! {
                                <h2 id="rewind-heading">{heading(&p)}</h2>
                                <p class="rewind-scope">{scope_line(&p)}</p>
                                {(!prompt.trim().is_empty()).then(|| view! {
                                    <blockquote class="rewind-prompt" title="Returned to the composer after a conversation restore">{prompt.clone()}</blockquote>
                                })}
                                <fieldset class="rewind-modes">
                                    <legend>"Restore"</legend>
                                    {[Mode::Both, Mode::Conversation, Mode::Code].into_iter().map(|m| {
                                        let reason = m.unavailable(&p_modes);
                                        let disabled = reason.is_some();
                                        view! {
                                            <label class="rewind-mode" data-rewind-mode=format!("{m:?}").to_lowercase()>
                                                <input
                                                    type="radio"
                                                    name="rewind-mode"
                                                    prop:checked=move || mode.get() == m
                                                    disabled=disabled
                                                    on:change=move |_| { mode.set(m); acked.set(false); }
                                                />
                                                <span>{m.label()}</span>
                                                {reason.map(|r| view! { <span class="rewind-reason">{r}</span> })}
                                            </label>
                                        }
                                    }).collect_view()}
                                </fieldset>
                                {(!shown.is_empty()).then(|| view! {
                                    <details class="rewind-files" open=true>
                                        <summary>{format!("Files these turns changed ({})", p.files.len())}</summary>
                                        <ul>
                                            {shown.into_iter().map(|f| view! {
                                                <li><code>{f.path.clone()}</code>" "<span class="rewind-kind">{kind_label(&f.kind)}</span></li>
                                            }).collect_view()}
                                            {(more > 0).then(|| view! { <li class="hint">{format!("+{more} more")}</li> })}
                                        </ul>
                                    </details>
                                })}
                                {move || {
                                    let list = warnings(&p_warn, mode.get());
                                    (!list.is_empty()).then(|| view! {
                                        <ul class="rewind-warnings" role="alert" data-rewind="warnings">
                                            {list.into_iter().map(|w| view! { <li>{w}</li> }).collect_view()}
                                        </ul>
                                    })
                                }}
                                {
                                    let p_ack = p.clone();
                                    move || needs_ack(&p_ack, mode.get()).then(|| view! {
                                        <label class="rewind-ack">
                                            <input
                                                type="checkbox"
                                                data-rewind="ack"
                                                prop:checked=move || acked.get()
                                                on:change=move |ev| acked.set(event_target_checked(&ev))
                                            />
                                            " I understand these changes will not be undone."
                                        </label>
                                    })
                                }
                                {move || {
                                    let reason = blocked(&p_block, mode.get(), acked.get(), busy_turn.get());
                                    reason.map(|r| view! { <p class="hint" data-rewind="blocked">{r}</p> })
                                }}
                                <Show when=move || !error.get().is_empty()>
                                    <p class="rewind-error" role="alert">{move || error.get()}</p>
                                </Show>
                                <div class="rewind-actions">
                                    <button type="button" class="secondary" data-rewind="cancel" on:click=move |_| cancel()>"Cancel"</button>
                                    {
                                        let p_btn = p.clone();
                                        view! {
                                            <button
                                                type="button"
                                                class="primary"
                                                data-rewind="confirm"
                                                disabled=move || applying.get() || blocked(&p_btn, mode.get(), acked.get(), busy_turn.get()).is_some()
                                                on:click=move |_| apply()
                                            >{move || if applying.get() { "Rewinding\u{2026}" } else { "Rewind" }}</button>
                                        }
                                    }
                                </div>
                            }.into_any()
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

    fn preview() -> Preview {
        Preview {
            turn: 2,
            turns: 2,
            total_turns: 3,
            prompt: "add tests".into(),
            files: vec![PreviewFile { path: "a.go".into(), kind: "update".into() }],
            checkpoint_skipped: 0,
            uncovered: vec![],
            beyond_checkpoints: false,
            disabled: String::new(),
        }
    }

    #[test]
    fn preview_path_encodes_the_session() {
        let t = RewindTarget { session: "a/b".into(), keep: 7 };
        assert_eq!(preview_path(&t), "/v1/sessions/a%2Fb/rewind_preview?keep=7");
    }

    #[test]
    fn code_only_is_always_unavailable_with_a_reason() {
        let p = preview();
        assert_eq!(Mode::Code.unavailable(&p), Some(CODE_ONLY_REASON));
        assert_eq!(Mode::Conversation.unavailable(&p), None);
        assert_eq!(Mode::Both.unavailable(&p), None);
    }

    #[test]
    fn no_files_means_conversation_only_by_default() {
        let mut p = preview();
        assert_eq!(default_mode(&p), Mode::Both);
        p.files.clear();
        assert!(Mode::Both.unavailable(&p).is_some());
        assert_eq!(default_mode(&p), Mode::Conversation);
    }

    #[test]
    fn uncovered_mutations_block_a_silent_code_restore() {
        let mut p = preview();
        p.uncovered = vec!["bash".into()];
        assert!(needs_ack(&p, Mode::Both));
        assert!(!needs_ack(&p, Mode::Conversation));
        assert!(blocked(&p, Mode::Both, false, false).unwrap().contains("Acknowledge"));
        assert_eq!(blocked(&p, Mode::Both, true, false), None);
        assert_eq!(blocked(&p, Mode::Conversation, false, false), None);
        assert!(warnings(&p, Mode::Both).iter().any(|w| w.contains("bash")));
    }

    #[test]
    fn disabled_running_and_unsupported_states_block_with_reasons() {
        let mut p = preview();
        assert!(blocked(&p, Mode::Both, false, true).unwrap().contains("turn is running"));
        assert_eq!(blocked(&p, Mode::Code, true, false).as_deref(), Some(CODE_ONLY_REASON));
        p.disabled = "This cockpit is read-only".into();
        assert_eq!(blocked(&p, Mode::Conversation, true, false).as_deref(), Some("This cockpit is read-only"));
    }

    #[test]
    fn warnings_state_what_is_not_restored() {
        let mut p = preview();
        p.checkpoint_skipped = 2;
        p.beyond_checkpoints = true;
        let both = warnings(&p, Mode::Both);
        assert!(both.iter().any(|w| w.contains("2 file(s)")));
        assert!(both.iter().any(|w| w.contains("newest 50 turns")));
        let convo = warnings(&p, Mode::Conversation);
        assert_eq!(convo.len(), 1);
        assert!(convo[0].contains("stay as they are now"));
        assert!(Mode::Both.restores_files());
        assert!(!Mode::Conversation.restores_files());
    }

    #[test]
    fn copy_reads_naturally() {
        let mut p = preview();
        assert_eq!(heading(&p), "Rewind to before turn 2?");
        assert_eq!(scope_line(&p), "Undoes 2 turns: turn 2 through the latest (turn 3).");
        p.turns = 1;
        p.turn = 3;
        assert_eq!(scope_line(&p), "Undoes turn 3 (the latest).");
        assert!(preview_error("HTTP 409 {}").contains("no longer"));
        assert!(preview_error("HTTP 404 {}").contains("not on the daemon"));
        assert!(preview_error("boom").contains("boom"));
        assert_eq!(kind_label("create"), "created");
        assert_eq!(kind_label("update"), "changed");
    }

    #[test]
    fn preview_decodes_the_daemon_payload() {
        let p: Preview = serde_json::from_str(
            r#"{"turn":2,"turns":2,"totalTurns":3,"prompt":"add tests","files":[{"path":"a.go","kind":"update"}],"checkpointSkipped":0,"uncovered":[],"beyondCheckpoints":false}"#,
        )
        .unwrap();
        assert_eq!(p, preview());
    }
}
