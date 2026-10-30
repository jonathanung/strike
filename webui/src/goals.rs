//! Goals view. Talks only to `/v1/goals`. Autopilot is a goal posture:
//! the allowlist may run without an ask, and deny rules still block.
//! It is not a session permission mode (that dial stays on the composer).

use std::sync::Arc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::{json, Value};
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{get_json, post_json, post_json_value};

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct GoalView {
    #[serde(default)]
    id: String,
    #[serde(default)]
    description: String,
    #[serde(default)]
    criteria: Vec<CriterionView>,
    #[serde(default)]
    status: String,
    #[serde(default)]
    max_iterations: i64,
    #[serde(default)]
    max_wall_clock_s: i64,
    #[serde(default)]
    max_tokens: i64,
    #[serde(default)]
    max_verify_failures: i64,
    #[serde(default)]
    max_no_progress_iters: i64,
    #[serde(default)]
    tokens_used: i64,
    #[serde(default)]
    last_iteration: i64,
    #[serde(default)]
    fail_reason: String,
    #[serde(default)]
    autopilot: bool,
    #[serde(default)]
    allowed_tools: Vec<String>,
    #[serde(default)]
    deny_tools: Vec<String>,
    #[serde(default)]
    escalation: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct CriterionView {
    #[serde(default)]
    description: String,
    #[serde(default)]
    check: String,
    #[serde(default)]
    satisfied: bool,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct IterView {
    #[serde(default)]
    n: i64,
    #[serde(default)]
    summary: String,
    #[serde(default)]
    tokens: i64,
    #[serde(default)]
    verified: bool,
}

#[derive(Deserialize)]
struct GoalList {
    #[serde(default)]
    goals: Vec<GoalView>,
}

#[derive(Deserialize)]
struct GoalLog {
    #[serde(default)]
    iterations: Vec<IterView>,
}

#[derive(Clone, Debug, PartialEq)]
pub struct GoalDraft {
    pub description: String,
    pub criteria: Vec<String>,
    pub max_iterations: i64,
    pub max_wall_clock_s: i64,
    pub max_tokens: i64,
    pub max_verify_failures: i64,
    pub max_no_progress_iters: i64,
    pub autopilot: bool,
    pub allowed_tools: Vec<String>,
    pub deny_tools: Vec<String>,
}

pub fn goal_status_label(status: &str) -> &'static str {
    match status {
        "pending" => "Pending",
        "active" => "Running",
        "paused" => "Paused",
        "done" => "Done",
        "failed" => "Stopped",
        "aborted" => "Aborted",
        "blocked" => "Needs you",
        _ => "Unknown",
    }
}

pub fn needs_human(status: &str, escalation: &str) -> bool {
    status == "blocked" || escalation == "human"
}

pub fn split_tools(raw: &str) -> Vec<String> {
    let mut out = Vec::new();
    for part in raw.split(',') {
        let part = part.trim();
        if !part.is_empty() {
            out.push(part.to_string());
        }
    }
    out
}

pub fn tool_denied(tool: &str, deny: &[String]) -> bool {
    deny.iter()
        .any(|d| d == "*" || d.eq_ignore_ascii_case(tool.trim()))
}

fn check_spec(raw: &str) -> String {
    let raw = raw.trim();
    let Some((kind, _)) = raw.split_once(':') else {
        return format!("cmd: {raw}");
    };
    match kind.trim().to_ascii_lowercase().as_str() {
        "cmd" | "predicate" | "judge" => raw.to_string(),
        _ => format!("cmd: {raw}"),
    }
}

fn parse_budget(raw: &str, label: &str, min: i64, max: i64) -> Result<i64, String> {
    let raw = raw.trim();
    let n: i64 = raw
        .parse()
        .map_err(|_| format!("{label} must be a whole number."))?;
    if n < min || n > max {
        return Err(format!("{label} must be between {min} and {max}."));
    }
    Ok(n)
}

/// Builds the create payload. Verification is required. Autopilot without an
/// allowlist is rejected here and again by the daemon.
pub fn draft_goal(
    description: &str,
    verification: &str,
    extra: &str,
    turns: &str,
    minutes: &str,
    tokens: &str,
    stall_after: &str,
    verify_fails: &str,
    autopilot: bool,
    allow: &str,
    deny: &str,
) -> Result<GoalDraft, String> {
    let description = description.trim();
    if description.is_empty() {
        return Err("Describe the goal.".into());
    }
    let verification = verification.trim();
    if verification.is_empty() {
        return Err("A verification command is required. The loop stops when it exits 0.".into());
    }
    if verification.contains('\n') {
        return Err("The verification command has to be one line.".into());
    }
    let mut criteria = vec![check_spec(verification)];
    for (i, line) in extra.lines().enumerate() {
        let line = line.trim();
        if line.is_empty() {
            continue;
        }
        let (kind, _) = line.split_once(':').ok_or_else(|| {
            format!("Extra check {n} must start with cmd:, predicate:, or judge:.", n = i + 1)
        })?;
        match kind.trim().to_ascii_lowercase().as_str() {
            "cmd" | "predicate" | "judge" => criteria.push(line.to_string()),
            _ => {
                return Err(format!(
                    "Extra check {} must start with cmd:, predicate:, or judge:.",
                    i + 1
                ));
            }
        }
    }
    let max_iterations = parse_budget(turns, "Turn budget", 1, 10_000)?;
    let minutes = parse_budget(minutes, "Time budget", 1, 10_080)?;
    let max_tokens = parse_budget(tokens, "Token budget", 1, 10_000_000)?;
    let max_no_progress_iters = parse_budget(stall_after, "Stall window", 1, 100)?;
    let max_verify_failures = parse_budget(verify_fails, "Failed-check limit", 1, 100)?;
    let allowed_tools = if autopilot { split_tools(allow) } else { Vec::new() };
    let deny_tools = if autopilot { split_tools(deny) } else { Vec::new() };
    if autopilot && allowed_tools.is_empty() {
        return Err(
            "Autopilot needs an allow list. It does not run every tool, and deny rules still apply."
                .into(),
        );
    }
    for tool in &allowed_tools {
        if tool_denied(tool, &deny_tools) {
            return Err(format!(
                "Deny rules still apply. Remove {tool} from the allow list or the deny list."
            ));
        }
    }
    Ok(GoalDraft {
        description: description.to_string(),
        criteria,
        max_iterations,
        max_wall_clock_s: minutes.saturating_mul(60),
        max_tokens,
        max_verify_failures,
        max_no_progress_iters,
        autopilot,
        allowed_tools,
        deny_tools,
    })
}

fn later(ms: i32, f: impl FnOnce() + 'static) {
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

fn pull_list(
    base: RwSignal<String>,
    token: RwSignal<String>,
    goals: RwSignal<Vec<GoalView>>,
    note: RwSignal<String>,
) {
    let (b, t) = (base.get_untracked(), token.get_untracked());
    spawn_local(async move {
        match get_json::<GoalList>(&b, &t, "/v1/goals").await {
            Ok(list) => {
                goals.set(list.goals);
                note.set(String::new());
            }
            Err(e) => note.set(e),
        }
    });
}

/// follow polls until the goal leaves pending/active, or a newer follow starts.
/// A slow check (longer than a few seconds) keeps updating the log and the
/// "Needs you" banner. Responses from an older selection are dropped.
fn follow(
    base: RwSignal<String>,
    token: RwSignal<String>,
    goals: RwSignal<Vec<GoalView>>,
    iterations: RwSignal<Vec<IterView>>,
    note: RwSignal<String>,
    selected: RwSignal<String>,
    watch_gen: RwSignal<u32>,
    id: String,
) {
    let gen = watch_gen.get_untracked().wrapping_add(1);
    watch_gen.set(gen);
    poll_goal(base, token, goals, iterations, note, selected, watch_gen, gen, id, 0, 1800);
}

fn poll_goal(
    base: RwSignal<String>,
    token: RwSignal<String>,
    goals: RwSignal<Vec<GoalView>>,
    iterations: RwSignal<Vec<IterView>>,
    note: RwSignal<String>,
    selected: RwSignal<String>,
    watch_gen: RwSignal<u32>,
    gen: u32,
    id: String,
    delay_ms: i32,
    left: i32,
) {
    later(delay_ms, move || {
        if watch_gen.get_untracked() != gen || left <= 0 {
            return;
        }
        let id_for_fetch = id.clone();
        spawn_local(async move {
            if watch_gen.get_untracked() != gen {
                return;
            }
            let (b, t) = (base.get_untracked(), token.get_untracked());
            let list = match get_json::<GoalList>(&b, &t, "/v1/goals").await {
                Ok(list) => list,
                Err(e) => {
                    if watch_gen.get_untracked() == gen {
                        note.set(e);
                        poll_goal(base, token, goals, iterations, note, selected, watch_gen, gen, id_for_fetch, 1000, left - 1);
                    }
                    return;
                }
            };
            if watch_gen.get_untracked() != gen {
                return;
            }
            let status = list
                .goals
                .iter()
                .find(|g| g.id == id_for_fetch)
                .map(|g| g.status.clone())
                .unwrap_or_default();
            goals.set(list.goals);
            note.set(String::new());
            if !id_for_fetch.is_empty() && selected.get_untracked() == id_for_fetch {
                let path = format!("/v1/goals/{}/log", crate::api::js_encode(&id_for_fetch));
                if let Ok(log) = get_json::<GoalLog>(&b, &t, &path).await {
                    if watch_gen.get_untracked() == gen && selected.get_untracked() == id_for_fetch {
                        iterations.set(log.iterations);
                    }
                }
            }
            let live = matches!(status.as_str(), "pending" | "active");
            if live && selected.get_untracked() == id_for_fetch {
                poll_goal(base, token, goals, iterations, note, selected, watch_gen, gen, id_for_fetch, 1000, left - 1);
            }
        });
    });
}

fn draft_body(draft: &GoalDraft) -> Value {
    json!({
        "description": draft.description,
        "criteria": draft.criteria,
        "maxIterations": draft.max_iterations,
        "maxWallClockS": draft.max_wall_clock_s,
        "maxTokens": draft.max_tokens,
        "maxNoProgressIters": draft.max_no_progress_iters,
        "maxVerifyFailures": draft.max_verify_failures,
        "autopilot": draft.autopilot,
        "allowedTools": draft.allowed_tools,
        "denyTools": draft.deny_tools,
    })
}

#[component]
pub fn GoalsPane(base: RwSignal<String>, token: RwSignal<String>) -> impl IntoView {
    let goals = RwSignal::new(Vec::<GoalView>::new());
    let iterations = RwSignal::new(Vec::<IterView>::new());
    let selected = RwSignal::new(String::new());
    let creating = RwSignal::new(true);
    let note = RwSignal::new(String::new());
    let form_error = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let description = RwSignal::new(String::new());
    let verification = RwSignal::new(String::new());
    let extra = RwSignal::new(String::new());
    let turns = RwSignal::new("25".to_string());
    let minutes = RwSignal::new("30".to_string());
    let tokens = RwSignal::new("200000".to_string());
    let stall_after = RwSignal::new("3".to_string());
    let verify_fails = RwSignal::new("3".to_string());
    let autopilot = RwSignal::new(false);
    let allow = RwSignal::new(String::new());
    let deny = RwSignal::new(String::new());
    let watch_gen = RwSignal::new(0u32);

    {
        let base = base;
        let token = token;
        Effect::new(move |_| {
            pull_list(base, token, goals, note);
        });
    }

    let open_goal: Arc<dyn Fn(String) + Send + Sync> = Arc::new(move |id: String| {
        creating.set(false);
        selected.set(id.clone());
        iterations.set(Vec::new());
        follow(base, token, goals, iterations, note, selected, watch_gen, id);
    });

    let start_run: Arc<dyn Fn(String) + Send + Sync> = Arc::new(move |id: String| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let path = format!("/v1/goals/{}/run?background=1", crate::api::js_encode(&id));
        spawn_local(async move {
            if let Err(e) = post_json(&b, &t, &path, json!({})).await {
                note.set(e);
            }
            follow(base, token, goals, iterations, note, selected, watch_gen, id);
        });
    });

    let control: Arc<dyn Fn(String, String) + Send + Sync> = Arc::new(move |id: String, action: String| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let path = format!(
            "/v1/goals/{}/{}",
            crate::api::js_encode(&id),
            action
        );
        spawn_local(async move {
            if let Err(e) = post_json(&b, &t, &path, json!({})).await {
                note.set(e);
            }
            follow(base, token, goals, iterations, note, selected, watch_gen, id);
        });
    });

    let on_submit: Arc<dyn Fn(web_sys::SubmitEvent) + Send + Sync> = Arc::new(move |ev: web_sys::SubmitEvent| {
        ev.prevent_default();
        let draft = match draft_goal(
            &description.get_untracked(),
            &verification.get_untracked(),
            &extra.get_untracked(),
            &turns.get_untracked(),
            &minutes.get_untracked(),
            &tokens.get_untracked(),
            &stall_after.get_untracked(),
            &verify_fails.get_untracked(),
            autopilot.get_untracked(),
            &allow.get_untracked(),
            &deny.get_untracked(),
        ) {
            Ok(draft) => draft,
            Err(e) => {
                form_error.set(e);
                return;
            }
        };
        form_error.set(String::new());
        busy.set(true);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let body = draft_body(&draft);
        spawn_local(async move {
            let created = match post_json_value(&b, &t, "/v1/goals", body).await {
                Ok(v) => v,
                Err(e) => {
                    form_error.set(e);
                    busy.set(false);
                    return;
                }
            };
            let id = created
                .get("id")
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string();
            if id.is_empty() {
                form_error.set("The daemon did not return a goal id.".into());
                busy.set(false);
                return;
            }
            let path = format!("/v1/goals/{}/run?background=1", crate::api::js_encode(&id));
            if let Err(e) = post_json(&b, &t, &path, json!({})).await {
                form_error.set(e);
                busy.set(false);
                selected.set(id.clone());
                creating.set(false);
                follow(base, token, goals, iterations, note, selected, watch_gen, id);
                return;
            }
            busy.set(false);
            creating.set(false);
            selected.set(id.clone());
            description.set(String::new());
            verification.set(String::new());
            extra.set(String::new());
            follow(base, token, goals, iterations, note, selected, watch_gen, id);
        });
    });

    view! {
        <div class="goals-shell" id="goals-root">
            <aside class="goals-list">
                <div class="section-head">
                    <h2 class="panel-title">"Goals"</h2>
                    <button type="button" class="secondary" on:click=move |_| {
                        creating.set(true);
                        selected.set(String::new());
                        form_error.set(String::new());
                        watch_gen.set(watch_gen.get_untracked().wrapping_add(1));
                    }>"New"</button>
                </div>
                <p class="goals-aside-note">"A goal loops until its check passes or a guard stops it."</p>
                {move || {
                    let rows = goals.get();
                    if rows.is_empty() {
                        return view! { <p class="hint">"No goals yet."</p> }.into_any();
                    }
                    let open = open_goal.clone();
                    let current = selected.get();
                    view! {
                        <div>
                            {rows.into_iter().map(|g| {
                        let open = open.clone();
                        let id = g.id.clone();
                        let id_click = id.clone();
                        let title = g.description.clone();
                        let label = goal_status_label(&g.status).to_string();
                        let class = if current == id { "goals-row on" } else { "goals-row" };
                        view! {
                            <button type="button" class=class on:click=move |_| open(id_click.clone())>
                                <span class="goals-row-title">{title}</span>
                                <span class="goals-status">{label}</span>
                            </button>
                        }
                    }).collect_view()}
                        </div>
                    }.into_any()
                }}
            </aside>
            <section class="goals-main">
                {move || {
                    let submit = on_submit.clone();
                    let start = start_run.clone();
                    let ctrl = control.clone();
                    let id = selected.get();
                    let show_form = creating.get() || id.is_empty();
                    if show_form {
                        return view! {
                            <form id="goals-form" on:submit=move |ev| submit(ev)>
                                <h2 class="goals-heading">"New goal"</h2>
                                <p class="goals-lead">"Say what should be true, and the command that proves it. This page runs that check. It does not plan tool calls. Deny rules still block if a planner emits one."</p>
                                <label class="goals-field">
                                    <span>"Goal"</span>
                                    <textarea id="goals-desc" rows="3" prop:value=move || description.get() on:input=move |ev| description.set(event_target_value(&ev)) placeholder="Tests pass on the package you changed"></textarea>
                                </label>
                                <label class="goals-field">
                                    <span>"Verification command"</span>
                                    <input id="goals-verify" prop:value=move || verification.get() on:input=move |ev| verification.set(event_target_value(&ev)) placeholder="go test ./..." />
                                </label>
                                <label class="goals-field">
                                    <span>"Extra checks"</span>
                                    <textarea rows="2" prop:value=move || extra.get() on:input=move |ev| extra.set(event_target_value(&ev)) placeholder="One per line: cmd:, predicate:, or judge:"></textarea>
                                </label>
                                <div class="goals-budgets">
                                    <label class="goals-field">
                                        <span>"Turns"</span>
                                        <input id="goals-turns" inputmode="numeric" prop:value=move || turns.get() on:input=move |ev| turns.set(event_target_value(&ev)) />
                                    </label>
                                    <label class="goals-field">
                                        <span>"Minutes"</span>
                                        <input id="goals-minutes" inputmode="numeric" prop:value=move || minutes.get() on:input=move |ev| minutes.set(event_target_value(&ev)) />
                                    </label>
                                    <label class="goals-field">
                                        <span>"Tokens"</span>
                                        <input id="goals-tokens" inputmode="numeric" prop:value=move || tokens.get() on:input=move |ev| tokens.set(event_target_value(&ev)) />
                                    </label>
                                    <label class="goals-field">
                                        <span>"Stall after"</span>
                                        <input id="goals-stall" inputmode="numeric" prop:value=move || stall_after.get() on:input=move |ev| stall_after.set(event_target_value(&ev)) />
                                    </label>
                                    <label class="goals-field">
                                        <span>"Failed checks"</span>
                                        <input id="goals-fails" inputmode="numeric" prop:value=move || verify_fails.get() on:input=move |ev| verify_fails.set(event_target_value(&ev)) />
                                    </label>
                                </div>
                                <label class="goals-toggle">
                                    <input type="checkbox" prop:checked=move || autopilot.get() on:change=move |ev| autopilot.set(event_target_checked(&ev)) />
                                    <span>
                                        <strong>"Autopilot"</strong>
                                        " stores an allow list for a planner that emits tool calls. Deny rules still apply. This page does not emit those calls. Off is the default."
                                    </span>
                                </label>
                                <Show when=move || autopilot.get()>
                                    <div class="goals-budgets">
                                        <label class="goals-field">
                                            <span>"Allow tools"</span>
                                            <input id="goals-allow" prop:value=move || allow.get() on:input=move |ev| allow.set(event_target_value(&ev)) placeholder="bash, edit" />
                                        </label>
                                        <label class="goals-field">
                                            <span>"Deny tools"</span>
                                            <input id="goals-deny" prop:value=move || deny.get() on:input=move |ev| deny.set(event_target_value(&ev)) placeholder="write, webfetch" />
                                        </label>
                                    </div>
                                </Show>
                                <p class="goals-rel">"A schedule fires on a clock. A task runner walks a plan section by section. This page is neither: one loop, one verification command, and the guards above. A future schedule may start a goal. It cannot skip these stops."</p>
                                <Show when=move || !form_error.get().is_empty()>
                                    <p class="goals-error" role="alert">{move || form_error.get()}</p>
                                </Show>
                                <div class="goals-actions">
                                    <button id="goals-run" class="primary" type="submit" disabled=move || busy.get()>"Run goal"</button>
                                </div>
                                <p class="hint keys">"Tab through the fields. Enter runs the goal. Ctrl+K then Goals, or Alt+G, opens this view."</p>
                            </form>
                        }.into_any();
                    }
                    let list = goals.get();
                    let Some(g) = list.into_iter().find(|g| g.id == id) else {
                        return view! { <p class="hint">"That goal is gone."</p> }.into_any();
                    };
                    let gid = g.id.clone();
                    let run_id = gid.clone();
                    let pause_id = gid.clone();
                    let abort_id = gid.clone();
                    let title = g.description.clone();
                    let status = goal_status_label(&g.status).to_string();
                    let reason = g.fail_reason.clone();
                    let human = needs_human(&g.status, &g.escalation);
                    let can_run = matches!(g.status.as_str(), "pending" | "paused" | "blocked");
                    let can_pause = g.status == "active";
                    let can_abort = !matches!(g.status.as_str(), "done" | "failed" | "aborted");
                    let run_label = if g.status == "pending" { "Run" } else { "Continue" };
                    let autopilot_line = if g.autopilot {
                        format!(
                            "Autopilot on. Allow: {}. Deny: {}. This page's planner does not emit tool calls.",
                            if g.allowed_tools.is_empty() { "none".into() } else { g.allowed_tools.join(", ") },
                            if g.deny_tools.is_empty() { "none".into() } else { g.deny_tools.join(", ") },
                        )
                    } else {
                        "Autopilot off. The check runs. This page does not plan tool calls.".into()
                    };
                    let budget = format!(
                        "Turns {}/{} · Tokens {}/{} · Wall {}m · Stall after {} · Stop after {} failed checks",
                        g.last_iteration,
                        g.max_iterations,
                        g.tokens_used,
                        g.max_tokens,
                        (g.max_wall_clock_s / 60).max(0),
                        g.max_no_progress_iters,
                        g.max_verify_failures,
                    );
                    let criteria = g.criteria.clone();
                    let log_rows = iterations.get();
                    let start = start.clone();
                    let ctrl = ctrl.clone();
                    view! {
                        <div class="goals-detail" id="goals-detail">
                            <header class="goals-detail-head">
                                <h2 class="goals-heading">{title}</h2>
                                <span class="goals-status" id="goals-status">{status}</span>
                            </header>
                            {let reason = reason.clone();
                            move || {
                                let reason = reason.clone();
                                if human {
                                    view! {
                                        <div class="goals-banner" id="goals-escalate" role="status">
                                            <strong>"Needs you"</strong>
                                            <p>{reason}</p>
                                            <p>"The loop has stopped. Continue only after you change the check or the tree. Abort leaves it closed."</p>
                                        </div>
                                    }.into_any()
                                } else if !reason.is_empty() {
                                    view! { <p class="goals-error" role="alert">{reason}</p> }.into_any()
                                } else {
                                    view! { <span></span> }.into_any()
                                }
                            }}
                            <p class="goals-budget">{budget}</p>
                            <p class="goals-autopilot-line">{autopilot_line}</p>
                            <ul class="goals-criteria">
                                {criteria.into_iter().map(|c| {
                                    let mark = if c.satisfied { "Pass" } else { "Open" };
                                    let text = if c.check.is_empty() { c.description } else { c.check };
                                    view! { <li><span class="goals-mark">{mark}</span><span>{text}</span></li> }
                                }).collect_view()}
                            </ul>
                            <h3 class="panel-title">"Log"</h3>
                            <ol class="goals-log" id="goals-log">
                                {if log_rows.is_empty() {
                                    view! { <li class="hint">"No iterations yet."</li> }.into_any()
                                } else {
                                    view! {
                                        <span>
                                            {log_rows.into_iter().map(|row| {
                                                let mark = if row.verified { "Pass" } else { "Fail" };
                                                let line = if row.summary.is_empty() {
                                                    format!("Iteration {}", row.n)
                                                } else {
                                                    row.summary
                                                };
                                                view! { <li><span class="goals-mark">{mark}</span><span>{line}</span><span class="goals-tokens">{row.tokens}" tok"</span></li> }
                                            }).collect_view()}
                                        </span>
                                    }.into_any()
                                }}
                            </ol>
                            <div class="goals-actions">
                                {let start = start.clone();
                                let run_id = run_id.clone();
                                move || {
                                    let start = start.clone();
                                    let run_id = run_id.clone();
                                    if can_run {
                                        view! { <button type="button" class="primary" id="goals-continue" on:click=move |_| start(run_id.clone())>{run_label}</button> }.into_any()
                                    } else {
                                        view! { <span></span> }.into_any()
                                    }
                                }}
                                {let ctrl = ctrl.clone();
                                let pause_id = pause_id.clone();
                                move || {
                                    let ctrl = ctrl.clone();
                                    let pause_id = pause_id.clone();
                                    if can_pause {
                                        view! { <button type="button" class="secondary" on:click=move |_| ctrl(pause_id.clone(), "pause".into())>"Pause"</button> }.into_any()
                                    } else {
                                        view! { <span></span> }.into_any()
                                    }
                                }}
                                {let ctrl = ctrl.clone();
                                let abort_id = abort_id.clone();
                                move || {
                                    let ctrl = ctrl.clone();
                                    let abort_id = abort_id.clone();
                                    if can_abort {
                                        view! { <button type="button" class="secondary" on:click=move |_| ctrl(abort_id.clone(), "abort".into())>"Abort"</button> }.into_any()
                                    } else {
                                        view! { <span></span> }.into_any()
                                    }
                                }}
                            </div>
                        </div>
                    }.into_any()
                }}
                <Show when=move || !note.get().is_empty()>
                    <p class="goals-error" role="alert">{move || note.get()}</p>
                </Show>
            </section>
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ok_draft() -> GoalDraft {
        draft_goal(
            "tests pass",
            "go test ./...",
            "",
            "25",
            "30",
            "200000",
            "3",
            "3",
            false,
            "",
            "",
        )
        .unwrap()
    }

    #[test]
    fn verification_is_required_and_wrapped() {
        let err = draft_goal("x", "  ", "", "25", "30", "200000", "3", "3", false, "", "").unwrap_err();
        assert!(err.contains("verification"));
        let draft = ok_draft();
        assert_eq!(draft.criteria, vec!["cmd: go test ./...".to_string()]);
        assert_eq!(draft.max_wall_clock_s, 1800);
        assert!(!draft.autopilot);
    }

    #[test]
    fn keeps_an_explicit_check_prefix() {
        let draft = draft_goal(
            "x",
            "cmd: true",
            "predicate: built\n",
            "4",
            "1",
            "10",
            "3",
            "2",
            false,
            "",
            "",
        )
        .unwrap();
        assert_eq!(draft.criteria, vec!["cmd: true".to_string(), "predicate: built".to_string()]);
        assert_eq!(draft.max_verify_failures, 2);
        assert_eq!(draft.max_tokens, 10);
    }

    #[test]
    fn autopilot_requires_allow_and_deny_wins() {
        let err = draft_goal("x", "true", "", "8", "5", "100", "3", "3", true, "", "").unwrap_err();
        assert!(err.contains("allow list"));
        let err = draft_goal("x", "true", "", "8", "5", "100", "3", "3", true, "bash, edit", "Bash").unwrap_err();
        assert!(err.contains("Deny rules"));
        let draft = draft_goal("x", "true", "", "8", "5", "100", "3", "3", true, "bash", "write").unwrap();
        assert_eq!(draft.allowed_tools, vec!["bash".to_string()]);
        assert_eq!(draft.deny_tools, vec!["write".to_string()]);
    }

    #[test]
    fn autopilot_off_drops_tool_lists() {
        let draft = draft_goal("x", "true", "", "8", "5", "100", "3", "3", false, "bash", "write").unwrap();
        assert!(draft.allowed_tools.is_empty());
        assert!(draft.deny_tools.is_empty());
    }

    #[test]
    fn budgets_reject_zero_and_junk() {
        assert!(draft_goal("x", "true", "", "0", "30", "10", "3", "3", false, "", "").is_err());
        assert!(draft_goal("x", "true", "", "8", "nope", "10", "3", "3", false, "", "").is_err());
    }

    #[test]
    fn status_and_escalation_labels() {
        assert_eq!(goal_status_label("blocked"), "Needs you");
        assert_eq!(goal_status_label("failed"), "Stopped");
        assert!(needs_human("blocked", ""));
        assert!(needs_human("paused", "human"));
        assert!(!needs_human("failed", ""));
        assert!(tool_denied("bash", &["*".into()]));
        assert!(tool_denied("Bash", &["bash".into()]));
        assert!(!tool_denied("edit", &["bash".into()]));
    }
}
