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
//!
//! Security mode (#1620): the same action with a security persona. The
//! reviewer reports CWE-shaped findings; a second fresh child (verifier)
//! then tries to refute each one, and only confirmed findings are shown as
//! confirmed (refuted ones collapse under "Not verified" with the reason).
//! Dismissing a finding needs a one-line reason and suppresses it on later
//! security reviews of this branch; suppressions are listed and removable.
//! Proposed fixes are drafts only.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{delete_json, get_json, js_encode, post_json_value};

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
    #[serde(default)]
    pub mode: String,
    #[serde(default)]
    pub suppressed: i64,
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
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub cwe: String,
    #[serde(default)]
    pub confidence: String,
    #[serde(default, rename = "attackPath")]
    pub attack_path: String,
    #[serde(default)]
    pub fix: String,
    #[serde(default)]
    pub verification: String,
    #[serde(default, rename = "verifyReason")]
    pub verify_reason: String,
    #[serde(default)]
    pub suppressed: bool,
    #[serde(default, rename = "suppressReason")]
    pub suppress_reason: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct Suppression {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub cwe: String,
    #[serde(default)]
    pub line: u32,
    #[serde(default)]
    pub reason: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct SuppressionList {
    #[serde(default)]
    suppressions: Vec<Suppression>,
}

/// Where a finding renders. General findings are "general".
pub fn security_group(f: &ReviewFinding) -> &'static str {
    if f.cwe.is_empty() {
        return "general";
    }
    if f.suppressed {
        return "suppressed";
    }
    match f.verification.as_str() {
        "confirmed" => "confirmed",
        "refuted" => "refuted",
        _ => "pending",
    }
}

pub fn has_security_findings(r: &ReviewResult) -> bool {
    r.findings.iter().any(|f| !f.cwe.is_empty())
}

/// True while an unsuppressed security finding still awaits the verifier.
pub fn verification_pending(r: &ReviewResult) -> bool {
    r.findings
        .iter()
        .any(|f| !f.cwe.is_empty() && !f.suppressed && f.verification == "pending")
}

pub fn result_path(reviewer: &str, verifier: &str) -> String {
    let mut path = format!("/v1/git/review/{}", js_encode(reviewer));
    if !verifier.is_empty() && verifier != NO_VERIFIER {
        path.push_str("?verifier=");
        path.push_str(&js_encode(verifier));
    }
    path
}

/// A dismissal needs a one-line reason of 1-300 characters.
pub fn can_dismiss(reason: &str) -> bool {
    let n = reason.trim().chars().count();
    n > 0 && n <= 300
}

/// Sentinel: verification could not start (nothing left to verify).
pub const NO_VERIFIER: &str = "-";

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
    if p.mode == "security" {
        return format!("Security review by a read-only child ({agent}) plus an independent verifier child: {turns}.");
    }
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
    mode: RwSignal<String>,
    review_base: RwSignal<String>,
    verifier: RwSignal<String>,
    verify_started: RwSignal<bool>,
}

fn poll_result(p: Poll, child: String, my_gen: u64, polls: u32) {
    if p.gen.try_get_untracked() != Some(my_gen) {
        return;
    }
    let (b, t) = (p.base.get_untracked(), p.token.get_untracked());
    let path = result_path(&child, &p.verifier.get_untracked());
    spawn_local(async move {
        let res = get_json::<ReviewResult>(&b, &t, &path).await;
        if p.gen.try_get_untracked() != Some(my_gen) {
            return;
        }
        let status = match res {
            Ok(r) => {
                let mut s = r.status.clone();
                let finished = s == "done" || s == "unparsed";
                if p.mode.get_untracked() == "security" && finished && has_security_findings(&r) {
                    let verifier = p.verifier.get_untracked();
                    if verifier.is_empty() {
                        if !p.verify_started.get_untracked() {
                            p.verify_started.set(true);
                            start_verifier(p, child.clone(), my_gen);
                        }
                        s = "running".into();
                    } else if verifier != NO_VERIFIER && verification_pending(&r) {
                        s = "running".into();
                    }
                }
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

/// Spawns the independent verifier once the security reviewer finished.
/// The user's single "Run review" confirmation covers both turns (the cost
/// notice says two).
fn start_verifier(p: Poll, reviewer: String, my_gen: u64) {
    let (b, t) = (p.base.get_untracked(), p.token.get_untracked());
    let body = serde_json::json!({ "base": p.review_base.get_untracked(), "confirm": true });
    spawn_local(async move {
        let path = format!("/v1/git/review/{}/verify", js_encode(&reviewer));
        let res = post_json_value(&b, &t, &path, body).await;
        if p.gen.try_get_untracked() != Some(my_gen) {
            return;
        }
        match res {
            Ok(v) => {
                let id = v.get("childSessionId").and_then(|x| x.as_str()).unwrap_or_default().to_string();
                p.verifier.set(if id.is_empty() { NO_VERIFIER.to_string() } else { id });
            }
            Err(e) => {
                p.verifier.set(NO_VERIFIER.to_string());
                p.error.set(format!("Verification did not start: {}", daemon_error(&e)));
            }
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
    let mode = RwSignal::new("general".to_string());
    let review_base = RwSignal::new(String::new());
    let verifier = RwSignal::new(String::new());
    let verify_started = RwSignal::new(false);
    let sups = RwSignal::new(Vec::<Suppression>::new());
    let dismissing = RwSignal::new(String::new());
    let dismiss_reason = RwSignal::new(String::new());
    let sup_note = RwSignal::new(String::new());
    let poller = Poll {
        base,
        token,
        result,
        error,
        gen,
        mode,
        review_base,
        verifier,
        verify_started,
    };

    Effect::new(move |_| {
        if !active.get() {
            open.set(false);
        }
    });

    let load_sups = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match get_json::<SuppressionList>(&b, &t, "/v1/git/review-suppressions").await {
                Ok(list) => sups.set(list.suppressions),
                Err(_) => sups.set(Vec::new()),
            }
        });
    };

    let load = move || {
        error.set(String::new());
        preview.set(None);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let path = if mode.get_untracked() == "security" {
            "/v1/git/review?mode=security"
        } else {
            "/v1/git/review"
        };
        spawn_local(async move {
            match get_json::<ReviewPreview>(&b, &t, path).await {
                Ok(p) => preview.set(Some(p)),
                Err(e) => error.set(format!("Could not size the review: {}", daemon_error(&e))),
            }
        });
        if mode.get_untracked() == "security" {
            load_sups();
        }
    };

    // Re-read the current result once (after a dismissal or removal).
    let repoll = move || {
        let id = child.get_untracked();
        if id.is_empty() {
            return;
        }
        let g = gen.get_untracked().wrapping_add(1);
        gen.set(g);
        poll_result(poller, id, g, 0);
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
        verifier.set(String::new());
        verify_started.set(false);
        review_base.set(p.base.clone());
        let g = gen.get_untracked().wrapping_add(1);
        gen.set(g);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let body = serde_json::json!({ "base": p.base, "confirm": true, "mode": mode.get_untracked() });
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

    let dismiss = move |f: ReviewFinding| {
        let reason = dismiss_reason.get_untracked();
        if !can_dismiss(&reason) {
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let body = serde_json::json!({ "path": f.path, "cwe": f.cwe, "line": f.line, "reason": reason.trim() });
        spawn_local(async move {
            match post_json_value(&b, &t, "/v1/git/review-suppressions", body).await {
                Ok(_) => {
                    dismissing.set(String::new());
                    dismiss_reason.set(String::new());
                    sup_note.set("Dismissed. It will not be raised again on this branch.".into());
                    load_sups();
                    repoll();
                }
                Err(e) => sup_note.set(daemon_error(&e)),
            }
        });
    };

    let remove_sup = move |id: String| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/git/review-suppressions?id={}", js_encode(&id));
            match delete_json(&b, &t, &path, None).await {
                Ok(()) => {
                    sup_note.set("Suppression removed.".into());
                    load_sups();
                    repoll();
                }
                Err(e) => sup_note.set(daemon_error(&e)),
            }
        });
    };

    let finding_row = move |f: ReviewFinding| {
        let anchor = finding_anchor(&f);
        let sev = f.severity.clone();
        let path = f.path.clone();
        let line = f.line;
        let security = !f.cwe.is_empty();
        let key = format!("{}:{}:{}", f.path, f.line, f.cwe);
        let key_open = key.clone();
        let key_show = key.clone();
        let detail = if security {
            format!("{} · {} confidence · {}", f.cwe, f.confidence, f.attack_path)
        } else {
            f.summary.clone()
        };
        let fix = f.fix.clone();
        let verify_reason = f.verify_reason.clone();
        let can_dismiss_row = security && !f.suppressed;
        let fd = f.clone();
        view! {
            <li class=format!("review-finding review-{sev}") data-review-finding=sev.clone() data-review-group=security_group(&f)>
                <span class="review-sev">{sev.clone()}</span>
                <button type="button" class="review-anchor" data-review-anchor=anchor.clone()
                    on:click=move |_| {
                        doc_line.set(line);
                        doc_diff.set(false);
                        doc_path.set(path.clone());
                    }
                >{anchor.clone()}</button>
                <span class="review-summary">{detail}</span>
                {(!fix.is_empty()).then(|| view! { <span class="review-fix" data-review-fix="">{format!("Proposed fix (draft): {fix}")}</span> })}
                {(!verify_reason.is_empty()).then(|| view! { <span class="review-verify-reason">{format!("Verifier: {verify_reason}")}</span> })}
                {can_dismiss_row.then(|| view! {
                    <span class="review-dismiss">
                        <button type="button" class="secondary" data-review-dismiss=""
                            on:click=move |_| {
                                dismiss_reason.set(String::new());
                                dismissing.set(key_open.clone());
                            }
                        >"Dismiss…"</button>
                        <Show when=move || dismissing.get() == key_show>
                            {
                                let fd = fd.clone();
                                view! {
                                    <span class="review-dismiss-form" data-review-dismiss-form="">
                                        <input type="text" maxlength="300" placeholder="Why is this not a real issue?" aria-label="Dismiss reason"
                                            data-review-dismiss-reason=""
                                            prop:value=move || dismiss_reason.get()
                                            on:input=move |ev| dismiss_reason.set(event_target_value(&ev))
                                        />
                                        <button type="button" class="primary" data-review-dismiss-confirm=""
                                            disabled=move || !can_dismiss(&dismiss_reason.get())
                                            on:click=move |_| dismiss(fd.clone())
                                        >"Dismiss with reason"</button>
                                        <button type="button" class="secondary" on:click=move |_| dismissing.set(String::new())>"Cancel"</button>
                                    </span>
                                }
                            }
                        </Show>
                    </span>
                })}
            </li>
        }
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
                    <div class="review-branch-mode" role="radiogroup" aria-label="Review mode" data-review-mode="">
                        <label>
                            <input type="radio" name="review-mode" value="general" data-review-mode-general=""
                                prop:checked=move || mode.get() == "general"
                                on:change=move |_| { mode.set("general".into()); load(); }
                            />
                            "General"
                        </label>
                        <label>
                            <input type="radio" name="review-mode" value="security" data-review-mode-security=""
                                prop:checked=move || mode.get() == "security"
                                on:change=move |_| { mode.set("security".into()); load(); }
                            />
                            "Security"
                        </label>
                    </div>
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
                        let suppressed = p.suppressed;
                        view! {
                            <div class="review-branch-notice" data-review-notice="" data-review-state=state>
                                <p class="review-branch-meta">{cost_line(&p)}</p>
                                <p class="review-branch-meta" data-review-size="">{size_line(&p)}</p>
                                {(suppressed > 0).then(|| view! {
                                    <p class="review-branch-meta">{format!("{suppressed} dismissed finding(s) on this branch will not be raised again.")}</p>
                                })}
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
                        let pick = |g: &str| -> Vec<ReviewFinding> {
                            r.findings.iter().filter(|f| security_group(f) == g).cloned().collect()
                        };
                        let general = pick("general");
                        let confirmed = pick("confirmed");
                        let pending = pick("pending");
                        let refuted = pick("refuted");
                        let suppressed = pick("suppressed");
                        let refuted_n = refuted.len();
                        let suppressed_n = suppressed.len();
                        let ver = verifier.get();
                        view! {
                            <div class="review-branch-result" data-review-result="" data-review-status=status.clone()>
                                <p
                                    class=if blocking { "review-branch-verdict review-blocking" } else { "review-branch-verdict" }
                                    role="status"
                                    data-review-verdict=""
                                    data-review-blocking=blocking.to_string()
                                >{verdict}</p>
                                <p class="review-branch-meta">{format!("Reviewer session: {sid}")}</p>
                                {(!ver.is_empty() && ver != NO_VERIFIER).then(|| view! {
                                    <p class="review-branch-meta" data-review-verifier="">{format!("Verifier session: {ver}")}</p>
                                })}
                                <ul class="review-branch-findings" data-review-findings="">
                                    {general.into_iter().map(finding_row).collect_view()}
                                    {confirmed.into_iter().map(finding_row).collect_view()}
                                    {pending.into_iter().map(finding_row).collect_view()}
                                </ul>
                                {(refuted_n > 0).then(|| view! {
                                    <details class="review-not-verified" data-review-not-verified="">
                                        <summary>{format!("Not verified ({refuted_n})")}</summary>
                                        <ul class="review-branch-findings">
                                            {refuted.clone().into_iter().map(finding_row).collect_view()}
                                        </ul>
                                    </details>
                                })}
                                {(suppressed_n > 0).then(|| view! {
                                    <details class="review-suppressed" data-review-suppressed-findings="">
                                        <summary>{format!("Suppressed on this branch ({suppressed_n})")}</summary>
                                        <ul class="review-branch-findings">
                                            {suppressed.clone().into_iter().map(|f| {
                                                let note = format!("{} {} — dismissed: {}", finding_anchor(&f), f.cwe, f.suppress_reason);
                                                view! { <li class="review-finding">{note}</li> }
                                            }).collect_view()}
                                        </ul>
                                    </details>
                                })}
                            </div>
                        }
                    })}
                    {move || (mode.get() == "security").then(|| {
                        let list = sups.get();
                        view! {
                            <div class="review-suppressions" data-review-suppressions="">
                                <p class="review-branch-meta">{move || {
                                    let n = sups.get().len();
                                    if n == 0 { "No dismissed findings on this branch.".to_string() } else { format!("Dismissed on this branch: {n}") }
                                }}</p>
                                {move || (!sup_note.get().is_empty()).then(|| view! { <p class="review-branch-meta" role="status">{sup_note.get()}</p> })}
                                <ul class="review-branch-findings">
                                    {list.into_iter().map(|s| {
                                        let id = s.id.clone();
                                        let label = format!("{}:{} {} — {}", s.path, s.line, s.cwe, s.reason);
                                        view! {
                                            <li class="review-finding" data-review-suppression=s.id.clone()>
                                                <span class="review-summary">{label}</span>
                                                <button type="button" class="secondary" data-review-unsuppress=""
                                                    on:click=move |_| remove_sup(id.clone())
                                                >"Remove"</button>
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
    fn sec(id: &str, verification: &str, suppressed: bool) -> ReviewFinding {
        ReviewFinding {
            id: id.into(),
            cwe: "CWE-89".into(),
            severity: "high".into(),
            path: "a.go".into(),
            line: 3,
            verification: verification.into(),
            suppressed,
            ..Default::default()
        }
    }

    #[test]
    fn decodes_security_finding() {
        let r: ReviewResult = serde_json::from_str(r#"{"sessionId":"c","status":"done","verdict":"Blocking: S1","blocking":true,"findings":[{"severity":"critical","path":"q.go","line":9,"summary":"p -> s","id":"S1","cwe":"CWE-89","confidence":"high","attackPath":"?q -> Sprintf","fix":"parameterize","verification":"confirmed","verifyReason":"traced","suppressed":false}]}"#).unwrap();
        let f = &r.findings[0];
        assert_eq!(f.cwe, "CWE-89");
        assert_eq!(f.attack_path, "?q -> Sprintf");
        assert_eq!(f.verify_reason, "traced");
        assert_eq!(security_group(f), "confirmed");
        let s: Suppression = serde_json::from_str(r#"{"id":"x","path":"a.go","cwe":"CWE-601","line":4,"reason":"const","at":"2026-11-21T00:00:00Z"}"#).unwrap();
        assert_eq!(s.line, 4);
    }

    #[test]
    fn security_groups_gate_presentation_on_verification() {
        assert_eq!(security_group(&ReviewFinding::default()), "general");
        assert_eq!(security_group(&sec("S1", "confirmed", false)), "confirmed");
        assert_eq!(security_group(&sec("S2", "refuted", false)), "refuted");
        assert_eq!(security_group(&sec("S3", "pending", false)), "pending");
        assert_eq!(security_group(&sec("S4", "", false)), "pending", "never confirmed by default");
        assert_eq!(security_group(&sec("S5", "confirmed", true)), "suppressed");
    }

    #[test]
    fn verification_pending_ignores_suppressed_and_general() {
        let mut r = ReviewResult { status: "done".into(), ..Default::default() };
        assert!(!has_security_findings(&r));
        r.findings = vec![ReviewFinding::default(), sec("S1", "pending", true)];
        assert!(has_security_findings(&r));
        assert!(!verification_pending(&r));
        r.findings.push(sec("S2", "pending", false));
        assert!(verification_pending(&r));
    }

    #[test]
    fn result_path_adds_verifier_only_when_running() {
        assert_eq!(result_path("rev 1", ""), "/v1/git/review/rev%201");
        assert_eq!(result_path("rev", NO_VERIFIER), "/v1/git/review/rev");
        assert_eq!(result_path("rev", "ver"), "/v1/git/review/rev?verifier=ver");
    }

    #[test]
    fn dismiss_needs_a_reason() {
        assert!(!can_dismiss(""));
        assert!(!can_dismiss("   "));
        assert!(can_dismiss("constant target"));
        assert!(!can_dismiss(&"r".repeat(301)));
    }

    #[test]
    fn security_cost_notice_names_both_children() {
        let mut p = preview();
        p.mode = "security".into();
        p.turns = 2;
        assert_eq!(
            cost_line(&p),
            "Security review by a read-only child (branch-reviewer) plus an independent verifier child: 2 model turns."
        );
    }
}

