//! Pre-push branch review (#1487): "Review this branch" in the Git tab.
//!
//! `GET /v1/git/review` shows the cost before anything runs (one model
//! turn, diff size, warn/refuse caps). Only an explicit "Run review" posts
//! `POST /v1/git/review {confirm:true}`, which spawns a fresh-context child
//! with the read-only `branch-reviewer` agent profile (engine-enforced
//! denies: no edits, commands, spawns, or network). Findings and the verdict
//! are parsed by the daemon from that child's transcript
//! (`GET /v1/git/review/{id}`); each finding opens its file at the line.
//! No autofix, no store, no merge gate.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{get_json, js_encode, post_json_value};

pub const POLL_MS: u32 = 3_000;
pub const POLL_MAX: u32 = 400; // ~20 minutes

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct ReviewPreview {
    #[serde(default)]
    pub state: String,
    #[serde(default)]
    pub reason: String,
    #[serde(default)]
    pub branch: String,
    #[serde(default)]
    pub base: String,
    #[serde(default)]
    pub files: i64,
    #[serde(default)]
    pub added: i64,
    #[serde(default)]
    pub deleted: i64,
    #[serde(default)]
    pub uncommitted: i64,
    #[serde(default)]
    pub untracked: Vec<String>,
    #[serde(default, rename = "diffBytes")]
    pub diff_bytes: i64,
    #[serde(default, rename = "diffLines")]
    pub diff_lines: i64,
    #[serde(default)]
    pub warn: bool,
    #[serde(default, rename = "warnBytes")]
    pub warn_bytes: i64,
    #[serde(default, rename = "maxBytes")]
    pub max_bytes: i64,
    #[serde(default)]
    pub agent: String,
    #[serde(default)]
    pub turns: i64,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct ReviewFinding {
    #[serde(default)]
    pub severity: String,
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub line: u32,
    #[serde(default)]
    pub summary: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct ReviewResult {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub verdict: String,
    #[serde(default)]
    pub blocking: bool,
    #[serde(default)]
    pub findings: Vec<ReviewFinding>,
}

fn kb(bytes: i64) -> String {
    if bytes < 1024 {
        format!("{bytes} B")
    } else {
        format!("{} KB", (bytes + 512) / 1024)
    }
}

/// The cost notice shown before the user confirms.
pub fn cost_line(p: &ReviewPreview) -> String {
    let turns = if p.turns == 1 {
        "one model turn".to_string()
    } else {
        format!("{} model turns", p.turns.max(1))
    };
    let agent = if p.agent.is_empty() { "branch-reviewer" } else { p.agent.as_str() };
    format!("Fresh-context review by a read-only child ({agent}): {turns}.")
}

pub fn size_line(p: &ReviewPreview) -> String {
    format!(
        "Diff: {} · {} lines · {} file(s) +{} −{} · {} uncommitted · {} untracked",
        kb(p.diff_bytes),
        p.diff_lines,
        p.files,
        p.added,
        p.deleted,
        p.uncommitted,
        p.untracked.len()
    )
}

pub fn can_run(p: &ReviewPreview) -> bool {
    p.state == "ok"
}

/// Next poll delay for a running review, or None to stop.
pub fn next_poll(status: &str, polls: u32) -> Option<u32> {
    if status != "running" && !status.is_empty() {
        return None;
    }
    if polls >= POLL_MAX {
        return None;
    }
    Some(POLL_MS)
}

pub fn verdict_text(r: &ReviewResult) -> String {
    match r.status.as_str() {
        "done" if !r.verdict.is_empty() => r.verdict.clone(),
        "unparsed" => "The reviewer finished without a parseable verdict. Read its transcript.".into(),
        _ => "Reviewing…".into(),
    }
}

pub fn finding_anchor(f: &ReviewFinding) -> String {
    if f.line > 0 {
        format!("{}:{}", f.path, f.line)
    } else {
        f.path.clone()
    }
}

fn daemon_error(err: &str) -> String {
    if let Some(idx) = err.find('{') {
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(&err[idx..]) {
            if let Some(msg) = v.get("error").and_then(|e| e.as_str()) {
                if !msg.is_empty() {
                    return msg.to_string();
                }
            }
        }
    }
    err.to_string()
}

fn later(ms: u32, f: impl FnOnce() + 'static) {
    let Some(win) = web_sys::window() else {
        return;
    };
    let cb = Closure::once(f);
    let _ = win.set_timeout_with_callback_and_timeout_and_arguments_0(
        cb.as_ref().unchecked_ref(),
        ms.min(i32::MAX as u32) as i32,
    );
    cb.forget();
}

#[derive(Clone, Copy)]
struct Poll {
    base: RwSignal<String>,
    token: RwSignal<String>,
    result: RwSignal<Option<ReviewResult>>,
    error: RwSignal<String>,
    gen: RwSignal<u64>,
}

fn poll_result(p: Poll, child: String, my_gen: u64, polls: u32) {
    if p.gen.try_get_untracked() != Some(my_gen) {
        return;
    }
    let (b, t) = (p.base.get_untracked(), p.token.get_untracked());
    spawn_local(async move {
        let path = format!("/v1/git/review/{}", js_encode(&child));
        let res = get_json::<ReviewResult>(&b, &t, &path).await;
        if p.gen.try_get_untracked() != Some(my_gen) {
            return;
        }
        let status = match res {
            Ok(r) => {
                let s = r.status.clone();
                p.result.set(Some(r));
                s
            }
            // The child log may not exist for a moment after spawn.
            Err(_) => "running".to_string(),
        };
        match next_poll(&status, polls) {
            Some(ms) => later(ms, move || poll_result(p, child, my_gen, polls + 1)),
            None if status == "running" => p.error.set("Stopped waiting for the reviewer; its transcript keeps the result.".into()),
            None => {}
        }
    });
}

#[component]
pub fn ReviewBranch(
    base: RwSignal<String>,
    token: RwSignal<String>,
    active: Memo<bool>,
    /// Document viewer signals: a finding opens its file at the line.
    doc_path: RwSignal<String>,
    doc_line: RwSignal<u32>,
    doc_diff: RwSignal<bool>,
) -> impl IntoView {
    let open = RwSignal::new(false);
    let preview = RwSignal::new(None::<ReviewPreview>);
    let error = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let child = RwSignal::new(String::new());
    let result = RwSignal::new(None::<ReviewResult>);
    let gen = RwSignal::new(0u64);
    let poller = Poll {
        base,
        token,
        result,
        error,
        gen,
    };

    Effect::new(move |_| {
        if !active.get() {
            open.set(false);
        }
    });

    let load = move || {
        error.set(String::new());
        preview.set(None);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match get_json::<ReviewPreview>(&b, &t, "/v1/git/review").await {
                Ok(p) => preview.set(Some(p)),
                Err(e) => error.set(format!("Could not size the review: {}", daemon_error(&e))),
            }
        });
    };

    let run = move || {
        let Some(p) = preview.get_untracked() else {
            return;
        };
        if busy.get_untracked() || !can_run(&p) {
            return;
        }
        busy.set(true);
        error.set(String::new());
        result.set(None);
        let g = gen.get_untracked().wrapping_add(1);
        gen.set(g);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let body = serde_json::json!({ "base": p.base, "confirm": true });
        spawn_local(async move {
            match post_json_value(&b, &t, "/v1/git/review", body).await {
                Ok(v) => {
                    let id = v
                        .get("childSessionId")
                        .and_then(|x| x.as_str())
                        .unwrap_or_default()
                        .to_string();
                    if id.is_empty() {
                        error.set("The daemon did not return a reviewer session.".into());
                    } else {
                        child.set(id.clone());
                        result.set(Some(ReviewResult {
                            session_id: id.clone(),
                            status: "running".into(),
                            ..Default::default()
                        }));
                        poll_result(poller, id, g, 0);
                    }
                }
                Err(e) => error.set(daemon_error(&e)),
            }
            busy.set(false);
        });
    };

    view! {
        <section class="review-branch" data-review-branch="" aria-label="Review this branch">
            <div class="review-branch-head">
                <button
                    type="button"
                    class="secondary"
                    data-review-open=""
                    aria-expanded=move || open.get().to_string()
                    on:click=move |_| {
                        if open.get_untracked() {
                            open.set(false);
                        } else {
                            open.set(true);
                            load();
                        }
                    }
                >"Review this branch"</button>
            </div>
            <Show when=move || open.get()>
                <div class="review-branch-panel" data-review-panel=""
                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                        if ev.key() == "Escape" {
                            ev.stop_propagation();
                            open.set(false);
                        }
                    }
                >
                    {move || (!error.get().is_empty()).then(|| view! {
                        <p class="review-branch-error" role="alert" data-review-error="">{error.get()}</p>
                    })}
                    {move || preview.get().map(|p| {
                        let ok = can_run(&p);
                        let state = p.state.clone();
                        let reason = p.reason.clone();
                        let warn = p.warn;
                        let warn_kb = kb(p.warn_bytes);
                        let max_kb = kb(p.max_bytes);
                        view! {
                            <div class="review-branch-notice" data-review-notice="" data-review-state=state>
                                <p class="review-branch-meta">{cost_line(&p)}</p>
                                <p class="review-branch-meta" data-review-size="">{size_line(&p)}</p>
                                {warn.then(|| view! {
                                    <p class="review-branch-warn" role="status" data-review-warn="">
                                        {format!("Large diff: over {warn_kb}. The review may be slower and less precise. Hard cap {max_kb}.")}
                                    </p>
                                })}
                                {(!ok).then(|| view! { <p class="review-branch-warn" role="status">{reason.clone()}</p> })}
                                <div class="review-branch-actions">
                                    <button type="button" class="primary" data-review-run=""
                                        disabled=move || busy.get() || !ok
                                        on:click=move |_| run()
                                    >{move || if busy.get() { "Starting…" } else { "Run review" }}</button>
                                    <button type="button" class="secondary" on:click=move |_| open.set(false)>"Close"</button>
                                </div>
                            </div>
                        }
                    })}
                    {move || result.get().map(|r| {
                        let verdict = verdict_text(&r);
                        let blocking = r.blocking;
                        let status = r.status.clone();
                        let sid = r.session_id.clone();
                        let findings = r.findings.clone();
                        view! {
                            <div class="review-branch-result" data-review-result="" data-review-status=status.clone()>
                                <p
                                    class=if blocking { "review-branch-verdict review-blocking" } else { "review-branch-verdict" }
                                    role="status"
                                    data-review-verdict=""
                                    data-review-blocking=blocking.to_string()
                                >{verdict}</p>
                                <p class="review-branch-meta">{format!("Reviewer session: {sid}")}</p>
                                <ul class="review-branch-findings" data-review-findings="">
                                    {findings.into_iter().map(|f| {
                                        let anchor = finding_anchor(&f);
                                        let sev = f.severity.clone();
                                        let path = f.path.clone();
                                        let line = f.line;
                                        view! {
                                            <li class=format!("review-finding review-{sev}") data-review-finding=sev.clone()>
                                                <span class="review-sev">{sev.clone()}</span>
                                                <button type="button" class="review-anchor" data-review-anchor=anchor.clone()
                                                    on:click=move |_| {
                                                        doc_line.set(line);
                                                        doc_diff.set(false);
                                                        doc_path.set(path.clone());
                                                    }
                                                >{anchor.clone()}</button>
                                                <span class="review-summary">{f.summary.clone()}</span>
                                            </li>
                                        }
                                    }).collect_view()}
                                </ul>
                            </div>
                        }
                    })}
                </div>
            </Show>
        </section>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn preview() -> ReviewPreview {
        ReviewPreview {
            state: "ok".into(),
            branch: "feature/x".into(),
            base: "main".into(),
            files: 2,
            added: 10,
            deleted: 3,
            uncommitted: 1,
            untracked: vec!["n.txt".into()],
            diff_bytes: 4096,
            diff_lines: 80,
            warn_bytes: 60_000,
            max_bytes: 200_000,
            agent: "branch-reviewer".into(),
            turns: 1,
            ..Default::default()
        }
    }

    #[test]
    fn decodes_preview_and_result() {
        let p: ReviewPreview = serde_json::from_str(r#"{"state":"refuse","reason":"too big","branch":"b","base":"main","files":1,"added":1,"deleted":0,"uncommitted":0,"untracked":[],"diffBytes":300000,"diffLines":9,"warn":false,"warnBytes":60000,"maxBytes":200000,"agent":"branch-reviewer","turns":1}"#).unwrap();
        assert_eq!(p.state, "refuse");
        assert!(!can_run(&p));
        let r: ReviewResult = serde_json::from_str(r#"{"sessionId":"c1","status":"done","verdict":"Blocking: x","blocking":true,"findings":[{"severity":"blocking","path":"calc.go","line":3,"summary":"panics"}]}"#).unwrap();
        assert!(r.blocking);
        assert_eq!(finding_anchor(&r.findings[0]), "calc.go:3");
        assert_eq!(verdict_text(&r), "Blocking: x");
    }

    #[test]
    fn cost_notice_names_turns_agent_and_size() {
        let p = preview();
        assert_eq!(cost_line(&p), "Fresh-context review by a read-only child (branch-reviewer): one model turn.");
        assert_eq!(size_line(&p), "Diff: 4 KB · 80 lines · 2 file(s) +10 −3 · 1 uncommitted · 1 untracked");
        assert!(can_run(&p));
        let mut empty = preview();
        empty.state = "empty".into();
        assert!(!can_run(&empty));
        assert_eq!(kb(512), "512 B");
    }

    #[test]
    fn polling_stops_when_done_or_capped() {
        assert_eq!(next_poll("running", 0), Some(POLL_MS));
        assert_eq!(next_poll("", 3), Some(POLL_MS));
        assert_eq!(next_poll("done", 0), None);
        assert_eq!(next_poll("unparsed", 0), None);
        assert_eq!(next_poll("running", POLL_MAX), None);
    }

    #[test]
    fn verdict_states() {
        let mut r = ReviewResult { status: "running".into(), ..Default::default() };
        assert_eq!(verdict_text(&r), "Reviewing…");
        r.status = "unparsed".into();
        assert!(verdict_text(&r).contains("parseable"));
        r.status = "done".into();
        r.verdict = "No blocking issues".into();
        assert_eq!(verdict_text(&r), "No blocking issues");
        let f = ReviewFinding { path: "a.go".into(), line: 0, ..Default::default() };
        assert_eq!(finding_anchor(&f), "a.go");
    }

    #[test]
    fn daemon_error_reads_error_field() {
        assert_eq!(daemon_error(r#"HTTP 409 {"error":"Nothing to review","code":"empty"}"#), "Nothing to review");
    }
}
