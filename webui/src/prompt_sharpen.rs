//! Prompt sharpen (#1385): rewrite the composer draft with the session's
//! selected model before sending, and let the user accept or reject it.
//!
//! The rewrite is a real model call through `POST /v1/prompt/sharpen` — a
//! transcript-free one-shot that appends nothing to the session log and
//! starts no run. The result is shown as an original-vs-sharpened line
//! diff; Accept replaces the draft (Undo restores the original until the
//! draft is sent or the session changes), Reject leaves the draft
//! untouched. Sharpening never sends. A failure leaves the draft
//! byte-identical and says so inline.
//!
//! Invoked by Alt+P in the composer, the "+" menu, or the palette command
//! `composer.sharpen` (registered in `shell/contrib/prompt_sharpen.rs`).

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::Value;
use wasm_bindgen::prelude::*;
use wasm_bindgen::JsCast;

/// Command id the palette, the "+" menu and Alt+P dispatch.
pub const COMMAND_ID: &str = "composer.sharpen";

/// Drafts longer than this get a cost warning before the call; they are
/// not blocked (the daemon refuses only beyond its own hard cap).
pub const WARN_CHARS: usize = 4000;

/// Lines beyond this are shown without a line diff (both blocks in full),
/// keeping the LCS table small.
const DIFF_MAX_LINES: usize = 400;

#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SharpenResult {
    pub original: String,
    pub sharpened: String,
    #[serde(default)]
    pub provider: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub input_tokens: u32,
    #[serde(default)]
    pub output_tokens: u32,
    #[serde(default)]
    pub usage_estimated: bool,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Phase {
    Idle,
    Running,
    Preview(SharpenResult),
    Failed(String),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DiffKind {
    Same,
    Removed,
    Added,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DiffLine {
    pub kind: DiffKind,
    pub text: String,
}

/// Whether a sharpen may start. Fresh prompts only in v1: a mid-turn
/// draft becomes a steer, and rewriting a steer is out of scope.
pub fn can_sharpen(draft: &str, busy: bool, root: &str) -> Result<(), &'static str> {
    if draft.trim().is_empty() {
        return Err("Type a draft first, then sharpen it.");
    }
    if root.is_empty() {
        return Err("No live session yet.");
    }
    if busy {
        return Err("Sharpen is for fresh prompts. Wait for the running turn to finish.");
    }
    Ok(())
}

/// Cost warning for long drafts (warn, never block).
pub fn length_warning(draft: &str) -> Option<String> {
    let n = draft.trim().chars().count();
    (n > WARN_CHARS).then(|| format!("Long draft: all {n} characters go to the model for this rewrite."))
}

/// Accept is only safe while the composer still holds the text that was
/// sharpened; otherwise it would overwrite edits made in the meantime.
pub fn accept_allowed(current_draft: &str, original: &str) -> bool {
    current_draft.trim() == original
}

/// One line about what the rewrite cost, with token counts when the
/// daemon reported them.
pub fn cost_note(r: &SharpenResult) -> String {
    let who = match (r.provider.is_empty(), r.model.is_empty()) {
        (_, false) => r.model.clone(),
        (false, true) => r.provider.clone(),
        (true, true) => "the session model".to_string(),
    };
    if r.input_tokens == 0 && r.output_tokens == 0 {
        return format!("One short call to {who}. Nothing was sent to the session.");
    }
    let approx = if r.usage_estimated { "~" } else { "" };
    format!(
        "One short call to {who}: {approx}{} in, {approx}{} out tokens. Nothing was sent to the session.",
        r.input_tokens, r.output_tokens
    )
}

/// Human text for an API error. `post_json_value` reports non-2xx as
/// `HTTP <code> <body>`; the daemon body is `{"error": "..."}`.
pub fn error_text(err: &str) -> String {
    let body = err
        .strip_prefix("HTTP ")
        .and_then(|rest| rest.split_once(' '))
        .map(|(_, body)| body)
        .unwrap_or(err);
    let msg = serde_json::from_str::<Value>(body)
        .ok()
        .and_then(|v| v.get("error").and_then(Value::as_str).map(str::to_string))
        .unwrap_or_else(|| body.trim().to_string());
    let msg = if msg.is_empty() { err.trim().to_string() } else { msg };
    format!("Sharpen failed: {}. Your draft is unchanged.", msg.trim_start_matches("sharpen failed: "))
}

pub fn parse_result(value: Value) -> Result<SharpenResult, String> {
    let r: SharpenResult = serde_json::from_value(value).map_err(|e| e.to_string())?;
    if r.sharpened.trim().is_empty() {
        return Err("Sharpen failed: the model returned no text. Your draft is unchanged.".into());
    }
    Ok(r)
}

/// Line diff (LCS) of original vs sharpened, for the preview.
pub fn diff_lines(original: &str, sharpened: &str) -> Vec<DiffLine> {
    let a: Vec<&str> = original.lines().collect();
    let b: Vec<&str> = sharpened.lines().collect();
    let line = |kind, text: &str| DiffLine { kind, text: text.to_string() };
    if a.len() > DIFF_MAX_LINES || b.len() > DIFF_MAX_LINES {
        return a
            .iter()
            .map(|t| line(DiffKind::Removed, t))
            .chain(b.iter().map(|t| line(DiffKind::Added, t)))
            .collect();
    }
    let (n, m) = (a.len(), b.len());
    // lcs[i][j] = LCS length of a[i..] and b[j..].
    let mut lcs = vec![vec![0u16; m + 1]; n + 1];
    for i in (0..n).rev() {
        for j in (0..m).rev() {
            lcs[i][j] = if a[i] == b[j] {
                lcs[i + 1][j + 1] + 1
            } else {
                lcs[i + 1][j].max(lcs[i][j + 1])
            };
        }
    }
    let (mut i, mut j) = (0, 0);
    let mut out = Vec::with_capacity(n + m);
    while i < n && j < m {
        if a[i] == b[j] {
            out.push(line(DiffKind::Same, a[i]));
            i += 1;
            j += 1;
        } else if lcs[i + 1][j] >= lcs[i][j + 1] {
            out.push(line(DiffKind::Removed, a[i]));
            i += 1;
        } else {
            out.push(line(DiffKind::Added, b[j]));
            j += 1;
        }
    }
    out.extend(a[i..].iter().map(|t| line(DiffKind::Removed, t)));
    out.extend(b[j..].iter().map(|t| line(DiffKind::Added, t)));
    out
}

fn later(f: impl FnOnce() + 'static) {
    let Some(win) = web_sys::window() else {
        return;
    };
    let cb = Closure::once(f);
    let _ = win.set_timeout_with_callback_and_timeout_and_arguments_0(cb.as_ref().unchecked_ref(), 0);
    cb.forget();
}

fn focus_selector(sel: &'static str) {
    later(move || {
        if let Some(el) = web_sys::window()
            .and_then(|w| w.document())
            .and_then(|d| d.query_selector(sel).ok().flatten())
            .and_then(|el| el.dyn_into::<web_sys::HtmlElement>().ok())
        {
            let _ = el.focus();
        }
    });
}

fn focus_composer() {
    focus_selector("#composer-input");
}

/// Sharpen state, owned by the App (not the composer view) so the
/// palette binding and Alt+P keep working when the chat view remounts.
#[derive(Clone, Copy)]
pub struct Sharpen {
    phase: RwSignal<Phase>,
    // (original draft, sharpened text) after Accept; Undo restores the
    // original while the composer still shows the sharpened text.
    undo: RwSignal<Option<(String, String)>>,
    // Bumped per request so a superseded or cancelled call is ignored.
    generation: RwSignal<u32>,
    draft: RwSignal<String>,
    active_root: RwSignal<String>,
    busy: Signal<bool>,
    base: RwSignal<String>,
    token: RwSignal<String>,
}

impl Sharpen {
    /// Create the state, its effects, and the palette binding. Call once
    /// from the App.
    pub fn new(
        draft: RwSignal<String>,
        active_root: RwSignal<String>,
        busy: Signal<bool>,
        base: RwSignal<String>,
        token: RwSignal<String>,
    ) -> Self {
        let s = Self {
            phase: RwSignal::new(Phase::Idle),
            undo: RwSignal::new(None),
            generation: RwSignal::new(0),
            draft,
            active_root,
            busy,
            base,
            token,
        };
        // A session switch abandons any sharpen in flight or on screen.
        Effect::new(move |prev: Option<String>| {
            let root = active_root.get();
            if prev.is_some_and(|p| p != root) {
                s.generation.update(|g| *g = g.wrapping_add(1));
                s.phase.set(Phase::Idle);
                s.undo.set(None);
            }
            root
        });
        // Sending clears the draft; Undo ends there.
        Effect::new(move |_| {
            if draft.get().is_empty() {
                s.undo.set(None);
            }
        });
        crate::shell::bind(COMMAND_ID, std::rc::Rc::new(move |_args: &str| s.start()));
        s
    }

    /// Sharpen the current draft. `Err` carries the reason it did not
    /// start (also shown inline).
    pub fn start(self) -> Result<(), String> {
        let (Some(phase), Some(text), Some(root), Some(busy)) = (
            self.phase.try_get_untracked(),
            self.draft.try_get_untracked(),
            self.active_root.try_get_untracked(),
            self.busy.try_get_untracked(),
        ) else {
            let msg = "Sharpen is unavailable: its state was disposed. Reload the page.";
            crate::console_error(msg);
            return Err(msg.into());
        };
        if phase == Phase::Running {
            return Ok(());
        }
        if let Err(msg) = can_sharpen(&text, busy, &root) {
            self.phase.set(Phase::Failed(msg.to_string()));
            return Err(msg.to_string());
        }
        self.generation.update(|g| *g = g.wrapping_add(1));
        let mine = self.generation.get_untracked();
        self.phase.set(Phase::Running);
        let (b, t) = (self.base.get_untracked(), self.token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/prompt/sharpen?root={}", crate::api::js_encode(&root));
            let result =
                crate::api::post_json_value(&b, &t, &path, serde_json::json!({ "text": text })).await;
            if self.generation.get_untracked() != mine || self.active_root.get_untracked() != root {
                return;
            }
            match result.map_err(|e| error_text(&e)).and_then(parse_result) {
                Ok(r) => {
                    self.phase.set(Phase::Preview(r));
                    focus_selector("[data-sharpen-accept]");
                }
                Err(msg) => self.phase.set(Phase::Failed(msg)),
            }
        });
        Ok(())
    }

    fn accept(self) {
        let Phase::Preview(r) = self.phase.get_untracked() else {
            return;
        };
        let current = self.draft.get_untracked();
        if !accept_allowed(&current, &r.original) {
            self.phase.set(Phase::Failed(
                "The draft changed while sharpening, so the rewrite was not applied. Sharpen again.".into(),
            ));
            return;
        }
        self.undo.set(Some((current, r.sharpened.clone())));
        self.draft.set(r.sharpened);
        self.phase.set(Phase::Idle);
        focus_composer();
    }

    fn reject(self) {
        self.generation.update(|g| *g = g.wrapping_add(1));
        self.phase.set(Phase::Idle);
        focus_composer();
    }
}

/// The sharpen strip above the composer textarea: progress, the
/// original-vs-sharpened preview, inline errors, and Undo. A plain
/// function the composer calls from a reactive closure (`{move ||
/// bar_view(sharpen)}`); it renders nothing when idle with no Undo.
pub fn bar_view(sharpen: Sharpen) -> AnyView {
    let Sharpen { phase, undo, draft, .. } = sharpen;
    let accept = move || sharpen.accept();
    let reject = move || sharpen.reject();
    let current = phase.get();
    let show_undo = undo.get().is_some_and(|(_, sharpened)| draft.get() == sharpened);
    if current == Phase::Idle && !show_undo {
        return ().into_any();
    }
    let state = match current {
        Phase::Idle => "undo",
        Phase::Running => "running",
        Phase::Preview(_) => "preview",
        Phase::Failed(_) => "error",
    };

    view! {
            <div
                class="sharpen"
                data-feature="prompt-sharpen"
                data-sharpen-state=state
                on:keydown=move |ev: web_sys::KeyboardEvent| {
                    if ev.key() == "Escape" {
                        ev.prevent_default();
                        ev.stop_propagation();
                        reject();
                    }
                }
            >
                {match current {
                    Phase::Idle => view! {
                        <div class="sharpen-row">
                            <span class="hint">"Draft sharpened."</span>
                            <button type="button" class="secondary" data-sharpen-undo="true" on:click=move |_| {
                                if let Some((original, _)) = undo.get_untracked() {
                                    draft.set(original);
                                }
                                undo.set(None);
                                focus_composer();
                            }>"Undo sharpen"</button>
                        </div>
                    }.into_any(),
                    Phase::Running => {
                        let warn = length_warning(&draft.get_untracked());
                        view! {
                            <div class="sharpen-row" role="status">
                                <span>"Sharpening with the session model…"</span>
                                <button type="button" class="secondary" data-sharpen-cancel="true" on:click=move |_| reject()>"Cancel"</button>
                            </div>
                            {warn.map(|w| view! { <p class="hint sharpen-warn">{w}</p> })}
                        }.into_any()
                    }
                    Phase::Failed(msg) => view! {
                        <div class="sharpen-row sharpen-error" role="alert">
                            <span>{msg}</span>
                            <button type="button" class="secondary" data-sharpen-dismiss="true" on:click=move |_| reject()>"Dismiss"</button>
                        </div>
                    }.into_any(),
                    Phase::Preview(r) => {
                        let note = cost_note(&r);
                        let rows = diff_lines(&r.original, &r.sharpened);
                        view! {
                            <div class="sharpen-preview" role="group" aria-label="Sharpened draft preview">
                                <div class="sharpen-diff" data-sharpen-diff="true">
                                    {rows.into_iter().map(|row| {
                                        let (klass, mark) = match row.kind {
                                            DiffKind::Same => ("sharpen-line same", "  "),
                                            DiffKind::Removed => ("sharpen-line removed", "- "),
                                            DiffKind::Added => ("sharpen-line added", "+ "),
                                        };
                                        view! { <div class=klass><span class="sharpen-mark" aria-hidden="true">{mark}</span>{row.text}</div> }
                                    }).collect_view()}
                                </div>
                                <p class="hint">{note}</p>
                                <div class="sharpen-row">
                                    <button type="button" data-sharpen-accept="true" on:click=move |_| accept()>"Accept"</button>
                                    <button type="button" class="secondary" data-sharpen-reject="true" on:click=move |_| reject()>"Reject"</button>
                                    <span class="hint">"Enter accepts. Esc rejects. Sharpening never sends."</span>
                                </div>
                            </div>
                        }.into_any()
                    }
                }}
            </div>
    }
    .into_any()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn kinds(rows: &[DiffLine]) -> Vec<(DiffKind, &str)> {
        rows.iter().map(|r| (r.kind, r.text.as_str())).collect()
    }

    #[test]
    fn can_sharpen_needs_a_draft_a_session_and_an_idle_turn() {
        assert!(can_sharpen("fix it", false, "r1").is_ok());
        assert!(can_sharpen("   ", false, "r1").is_err());
        assert!(can_sharpen("fix it", false, "").is_err());
        assert!(can_sharpen("fix it", true, "r1").is_err());
    }

    #[test]
    fn long_drafts_warn_but_short_ones_do_not() {
        assert_eq!(length_warning("short"), None);
        let long = "x".repeat(WARN_CHARS + 1);
        assert!(length_warning(&long).unwrap().contains(&(WARN_CHARS + 1).to_string()));
        assert_eq!(length_warning(&"x".repeat(WARN_CHARS)), None);
    }

    #[test]
    fn accept_requires_the_unchanged_draft() {
        assert!(accept_allowed("  fix the flaky thing \n", "fix the flaky thing"));
        assert!(!accept_allowed("fix the flaky thing now", "fix the flaky thing"));
    }

    #[test]
    fn diff_marks_removed_and_added_lines() {
        let rows = diff_lines("fix the flaky thing\nthanks", "Fix the flaky TestRetry.\nExpected: root cause.\nthanks");
        assert_eq!(
            kinds(&rows),
            vec![
                (DiffKind::Removed, "fix the flaky thing"),
                (DiffKind::Added, "Fix the flaky TestRetry."),
                (DiffKind::Added, "Expected: root cause."),
                (DiffKind::Same, "thanks"),
            ]
        );
    }

    #[test]
    fn identical_text_diffs_to_all_same() {
        let rows = diff_lines("a\nb", "a\nb");
        assert!(rows.iter().all(|r| r.kind == DiffKind::Same));
        assert_eq!(rows.len(), 2);
    }

    #[test]
    fn huge_inputs_skip_the_line_diff() {
        let big = "l\n".repeat(DIFF_MAX_LINES + 1);
        let rows = diff_lines(&big, "short");
        assert_eq!(rows.last().map(|r| r.kind), Some(DiffKind::Added));
        assert!(rows.iter().all(|r| r.kind != DiffKind::Same));
    }

    #[test]
    fn cost_note_reports_tokens_when_known() {
        let mut r = SharpenResult {
            original: "a".into(),
            sharpened: "b".into(),
            model: "claude-x".into(),
            ..Default::default()
        };
        assert_eq!(cost_note(&r), "One short call to claude-x. Nothing was sent to the session.");
        r.input_tokens = 120;
        r.output_tokens = 30;
        assert!(cost_note(&r).contains("120 in, 30 out"));
        r.usage_estimated = true;
        assert!(cost_note(&r).contains("~120 in, ~30 out"));
        r.model.clear();
        r.provider.clear();
        assert!(cost_note(&r).contains("the session model"));
    }

    #[test]
    fn error_text_unwraps_the_daemon_error() {
        assert_eq!(
            error_text(r#"HTTP 502 {"error":"sharpen failed: upstream 500"}"#),
            "Sharpen failed: upstream 500. Your draft is unchanged."
        );
        assert_eq!(
            error_text("HTTP 503 live session unavailable\n"),
            "Sharpen failed: live session unavailable. Your draft is unchanged."
        );
        assert_eq!(error_text("network down"), "Sharpen failed: network down. Your draft is unchanged.");
    }

    #[test]
    fn parse_result_rejects_an_empty_rewrite() {
        let ok = parse_result(serde_json::json!({
            "original": "a", "sharpened": "b", "provider": "echo", "model": "echo",
            "inputTokens": 3, "outputTokens": 4, "usageEstimated": true
        }))
        .unwrap();
        assert_eq!(ok.sharpened, "b");
        assert_eq!(ok.input_tokens, 3);
        assert!(ok.usage_estimated);
        assert!(parse_result(serde_json::json!({"original": "a", "sharpened": "  "})).is_err());
        assert!(parse_result(serde_json::json!({"nope": 1})).is_err());
    }
}
