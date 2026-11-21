//! "Run with N models…" picker and comparison card (#1346, #1881).
//!
//! The daemon (`/v1/bestofn`) runs every candidate as a normal team.spawn
//! child and is the source of truth; this card only renders it. The picker
//! opens from the command palette (`best-of-n.open`), shows the N× cost and
//! per-run budget before anything starts, and the card offers cancel-all,
//! adopt (confirmed) and keep none (confirmed). Nothing is applied without
//! that separate, confirmed step. Compare (#1962) asks the parent session
//! for commentary on how the runs differ; it never picks or applies one.

use std::rc::Rc;

use ::best_of_n as bon;
use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::{json, Value};

use crate::api::{get_json, js_encode, post_json_value};

/// Refetch cadence in shell ticks (one tick per second).
const LIVE_TICKS: u32 = 3;
const IDLE_TICKS: u32 = 15;

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Candidate {
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub effort: String,
    #[serde(default)]
    pub label: String,
    #[serde(default, rename = "childId")]
    pub child_id: String,
    #[serde(default)]
    pub status: String,
    #[serde(default, rename = "elapsedMs")]
    pub elapsed_ms: i64,
    #[serde(default)]
    pub summary: String,
    #[serde(default, rename = "filesChanged")]
    pub files_changed: i64,
    #[serde(default)]
    pub added: i64,
    #[serde(default)]
    pub removed: i64,
    #[serde(default)]
    pub verified: Option<bool>,
    #[serde(default)]
    pub error: String,
    /// Absent when the provider reported no usage (never shown as zero).
    #[serde(default)]
    pub tokens: Option<i64>,
    #[serde(default, rename = "costUsd")]
    pub cost_usd: Option<f64>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Decision {
    #[serde(default)]
    pub keep: String,
    #[serde(default)]
    pub archived: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Run {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub prompt: String,
    #[serde(default)]
    pub edits: bool,
    #[serde(default, rename = "costMultiplier")]
    pub cost_multiplier: i64,
    #[serde(default)]
    pub candidates: Vec<Candidate>,
    #[serde(default)]
    pub complete: bool,
    #[serde(default)]
    pub decision: Option<Decision>,
    #[serde(default)]
    pub note: String,
    #[serde(default, rename = "commentaryRequested")]
    pub commentary_requested: bool,
}

#[derive(Clone, Deserialize)]
struct RunList {
    #[serde(default)]
    runs: Vec<Run>,
}

fn parse_run(value: Value) -> Result<Run, String> {
    serde_json::from_value::<Run>(value).map_err(|e| e.to_string())
}

/// The start body the daemon expects.
fn start_body(owner: &str, prompt: &str, picks: &[bon::Pick], edits: bool, turns: u32, minutes: u32) -> Value {
    let candidates: Vec<Value> = picks
        .iter()
        .map(|p| json!({ "model": p.model, "effort": p.effort }))
        .collect();
    json!({
        "ownerRoot": owner,
        "prompt": prompt,
        "candidates": candidates,
        "edits": edits,
        "budget": { "maxTurns": turns, "timeoutSec": minutes * 60 },
    })
}

fn refresh(base: RwSignal<String>, token: RwSignal<String>, session: String, runs: RwSignal<Vec<Run>>, note: RwSignal<String>) {
    if session.trim().is_empty() {
        runs.set(Vec::new());
        return;
    }
    let b = base.get_untracked();
    let t = token.get_untracked();
    let path = format!("/v1/bestofn?root={}", js_encode(&session));
    spawn_local(async move {
        match get_json::<RunList>(&b, &t, &path).await {
            Ok(list) => runs.set(list.runs),
            // Older daemons have no best-of-N route; stay hidden.
            Err(e) if e.contains("404") || e.contains("405") => runs.set(Vec::new()),
            Err(e) => note.set(format!("Best-of-N runs could not be loaded: {e}")),
        }
    });
}

#[component]
pub fn BestOfNCard(
    base: RwSignal<String>,
    token: RwSignal<String>,
    active: RwSignal<String>,
    tick: RwSignal<u32>,
) -> impl IntoView {
    let runs = RwSignal::new(Vec::<Run>::new());
    let note = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let last_session = RwSignal::new(String::new());

    // Picker state.
    let open = RwSignal::new(false);
    let prompt = RwSignal::new(String::new());
    let models = RwSignal::new(String::new());
    let edits = RwSignal::new(true);
    let turns = RwSignal::new(String::new());
    let minutes = RwSignal::new(String::from("15"));
    let form_error = RwSignal::new(String::new());

    crate::shell::bind(
        "best-of-n.open",
        Rc::new(move |_args| {
            open.set(true);
            form_error.set(String::new());
            Ok(())
        }),
    );

    Effect::new(move |_| {
        let session = active.get();
        let n = tick.get();
        let switched = session != last_session.get_untracked();
        if switched {
            last_session.set(session.clone());
            runs.set(Vec::new());
            note.set(String::new());
        }
        let live = runs.with_untracked(|rs| rs.iter().any(|r| !r.complete));
        let every = if live { LIVE_TICKS } else { IDLE_TICKS };
        if switched || n % every == 0 {
            refresh(base, token, session, runs, note);
        }
    });

    let plan = move || -> Result<(Vec<bon::Pick>, u32, u32), String> {
        let picks = bon::parse_models(&models.get())?;
        let t = bon::parse_count(&turns.get(), "Turns per run")?;
        let m = bon::parse_count(&minutes.get(), "Minutes per run")?;
        Ok((picks, t, m))
    };

    let start = move |_| {
        if busy.get_untracked() {
            return;
        }
        let text = prompt.get_untracked();
        if text.trim().is_empty() {
            form_error.set("Write the prompt every model gets.".into());
            return;
        }
        let (picks, t, m) = match plan() {
            Ok(p) => p,
            Err(e) => {
                form_error.set(e);
                return;
            }
        };
        let owner = active.get_untracked();
        if owner.trim().is_empty() {
            form_error.set("Open a session first.".into());
            return;
        }
        let body = start_body(&owner, &text, &picks, edits.get_untracked(), t, m);
        let b = base.get_untracked();
        let tk = token.get_untracked();
        let path = format!("/v1/bestofn?root={}", js_encode(&owner));
        busy.set(true);
        form_error.set(String::new());
        spawn_local(async move {
            let res = post_json_value(&b, &tk, &path, body).await.and_then(parse_run);
            busy.set(false);
            match res {
                Ok(run) => {
                    runs.update(|rs| rs.insert(0, run));
                    open.set(false);
                    prompt.set(String::new());
                }
                Err(e) => form_error.set(e),
            }
        });
    };

    // POST an action for run `id`, replacing that run with the answer.
    let act = move |id: String, action: &'static str, body: Value| {
        let owner = active.get_untracked();
        let b = base.get_untracked();
        let tk = token.get_untracked();
        let path = format!("/v1/bestofn/{}/{}?root={}", id, action, js_encode(&owner));
        busy.set(true);
        note.set(String::new());
        spawn_local(async move {
            let res = post_json_value(&b, &tk, &path, body).await.and_then(parse_run);
            busy.set(false);
            match res {
                Ok(run) => runs.update(|rs| {
                    if let Some(slot) = rs.iter_mut().find(|r| r.id == run.id) {
                        *slot = run;
                    }
                }),
                Err(e) => note.set(e),
            }
        });
    };

    let confirm_then = move |title: String, body: String, label: &'static str, run: Rc<dyn Fn()>| {
        if busy.get_untracked() {
            return;
        }
        let opened = crate::confirm::ask(
            crate::confirm::ConfirmRequest { title, body, confirm_label: label.into() },
            run,
        );
        if !opened {
            note.set(crate::confirm::BUSY.into());
        }
    };

    let picker = move || {
        open.get().then(|| {
            let cost = move || match plan() {
                Ok((picks, t, m)) => bon::cost_line(picks.len(), t, m),
                Err(e) => e,
            };
            view! {
                <section class="best-of-n-card bon-picker" aria-label="Run with N models">
                    <header class="bon-head">
                        <span class="bon-badge">"Run with N models"</span>
                        <button type="button" class="secondary" data-bon-action="close" on:click=move |_| open.set(false)>"Close"</button>
                    </header>
                    <label class="bon-field">
                        <span>"Prompt"</span>
                        <textarea rows="3" data-bon-field="prompt" prop:value=move || prompt.get() on:input=move |ev| prompt.set(event_target_value(&ev)) placeholder="The same prompt goes to every model"></textarea>
                    </label>
                    <label class="bon-field">
                        <span>"Models (2–4, comma separated; model@effort for effort)"</span>
                        <input data-bon-field="models" prop:value=move || models.get() on:input=move |ev| models.set(event_target_value(&ev)) placeholder="anthropic/claude-sonnet@high, openai/gpt-5" />
                    </label>
                    <div class="bon-row">
                        <label class="bon-field bon-small">
                            <span>"Turns per run"</span>
                            <input inputmode="numeric" data-bon-field="turns" prop:value=move || turns.get() on:input=move |ev| turns.set(event_target_value(&ev)) placeholder="no limit" />
                        </label>
                        <label class="bon-field bon-small">
                            <span>"Minutes per run"</span>
                            <input inputmode="numeric" data-bon-field="minutes" prop:value=move || minutes.get() on:input=move |ev| minutes.set(event_target_value(&ev)) />
                        </label>
                        <label class="bon-check">
                            <input type="checkbox" data-bon-field="edits" prop:checked=move || edits.get() on:change=move |ev| edits.set(event_target_checked(&ev)) />
                            <span>"May edit files (one worktree per model)"</span>
                        </label>
                    </div>
                    <p class="bon-cost" data-bon-cost="true" role="status">{cost}</p>
                    <p class="bon-hint">"Each model runs under this session's permission mode. Nothing is applied until you adopt a winner."</p>
                    {move || {
                        let msg = form_error.get();
                        (!msg.is_empty()).then(|| view! { <p class="bon-error" role="alert">{msg}</p> })
                    }}
                    <div class="bon-actions">
                        <button type="button" class="primary" data-bon-action="start" prop:disabled=move || busy.get() on:click=start>"Run"</button>
                    </div>
                </section>
            }
        })
    };

    let cards = move || {
        runs.get()
            .into_iter()
            .map(|run| {
                let decided = run.decision.is_some();
                let keep = run.decision.as_ref().map(|d| d.keep.clone());
                let headline = bon::headline(run.complete, keep.as_deref(), decided);
                let state = if decided { "decided" } else if run.complete { "complete" } else { "running" };
                let complete = run.complete;
                let id = run.id.clone();
                let prompt_line: String = run.prompt.chars().take(160).collect();
                let mode = if run.edits { "worktree per model" } else { "answers only" };
                let multiplier = format!("{}× cost · {}", run.cost_multiplier, mode);
                let run_note = run.note.clone();
                let comparable = bon::can_compare(complete, decided, run.commentary_requested);
                let commentary = run.commentary_requested && !decided;
                let rows = run.candidates.clone().into_iter().map(|c| {
                    let adoptable = bon::can_adopt(&c.status, complete, decided);
                    let kept = keep.as_deref() == Some(c.child_id.as_str()) && !c.child_id.is_empty();
                    let label = if c.label.is_empty() { bon::label(&c.model, &c.effort) } else { c.label.clone() };
                    let status = c.status.clone();
                    let verified = bon::verified_label(c.verified);
                    let diff = bon::diffstat_label(c.files_changed, c.added, c.removed);
                    let elapsed = bon::elapsed_label(c.elapsed_ms);
                    let usage = bon::usage_label(c.tokens, c.cost_usd);
                    let summary = c.summary.clone();
                    let error = c.error.clone();
                    let child = c.child_id.clone();
                    let run_id = id.clone();
                    let who = label.clone();
                    view! {
                        <li data-status=status.clone() data-kept=kept.then_some("true")>
                            <div class="bon-cand-head">
                                <span class="bon-model">{label}</span>
                                <span class="bon-status">{bon::status_label(&status)}</span>
                                <span class="bon-elapsed">{elapsed}</span>
                            </div>
                            <div class="bon-cand-meta">
                                <span class="bon-diff">{diff}</span>
                                {(!verified.is_empty()).then(|| view! { <span class="bon-verify">{verified}</span> })}
                                {(!usage.is_empty()).then(|| view! { <span class="bon-usage" data-bon-usage="true">{usage}</span> })}
                            </div>
                            {(!summary.is_empty()).then(|| view! { <p class="bon-summary">{summary}</p> })}
                            {(!error.is_empty()).then(|| view! { <p class="bon-cand-error">{error}</p> })}
                            {adoptable.then(|| view! {
                                <button type="button" class="primary" data-bon-action="adopt"
                                    prop:disabled=move || busy.get()
                                    on:click=move |_| {
                                        let run_id = run_id.clone();
                                        let child = child.clone();
                                        confirm_then(
                                            format!("Adopt {who}?"),
                                            "Applies this run's changes to your working tree and removes every candidate's worktree. The other runs stay readable in their sessions.".into(),
                                            "Adopt",
                                            Rc::new(move || act(run_id.clone(), "adopt", json!({ "childId": child.clone() }))),
                                        );
                                    }>"Adopt"</button>
                            })}
                        </li>
                    }
                }).collect_view();
                let cancel_id = id.clone();
                let compare_id = id.clone();
                let none_id = id.clone();
                view! {
                    <section class="best-of-n-card" data-bon-run=id.clone() data-state=state aria-label="Best-of-N comparison" aria-live="polite">
                        <header class="bon-head">
                            <span class="bon-badge">{headline}</span>
                            <span class="bon-cost-tag">{multiplier}</span>
                        </header>
                        <p class="bon-prompt">{prompt_line}</p>
                        <ul class="bon-cands">{rows}</ul>
                        {(!run_note.is_empty()).then(|| view! { <p class="bon-note">{run_note}</p> })}
                        {commentary.then(|| view! {
                            <p class="bon-note" data-bon-commentary="requested">"Commentary requested: this session's reply in the transcript compares the runs. It does not pick a winner."</p>
                        })}
                        <div class="bon-actions">
                            {(!complete).then(|| view! {
                                <button type="button" class="secondary" data-bon-action="cancel"
                                    prop:disabled=move || busy.get()
                                    on:click=move |_| act(cancel_id.clone(), "cancel", json!({}))>"Cancel all"</button>
                            })}
                            {comparable.then(|| view! {
                                <button type="button" class="secondary" data-bon-action="compare"
                                    title="Ask this session to comment on how the runs differ (one turn). It never picks or applies a winner."
                                    prop:disabled=move || busy.get()
                                    on:click=move |_| act(compare_id.clone(), "compare", json!({}))>"Compare"</button>
                            })}
                            {(complete && !decided).then(|| view! {
                                <button type="button" class="secondary" data-bon-action="keep-none"
                                    prop:disabled=move || busy.get()
                                    on:click=move |_| {
                                        let none_id = none_id.clone();
                                        confirm_then(
                                            "Keep none?".into(),
                                            "Discards every run's changes and removes their worktrees. Your working tree is not touched.".into(),
                                            "Keep none",
                                            Rc::new(move || act(none_id.clone(), "keep-none", json!({}))),
                                        );
                                    }>"Keep none"</button>
                            })}
                        </div>
                    </section>
                }
            })
            .collect_view()
    };

    view! {
        {picker}
        {cards}
        {move || {
            let msg = note.get();
            (!msg.is_empty()).then(|| view! { <p class="best-of-n-error" role="alert">{msg}</p> })
        }}
    }
}
