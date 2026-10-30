//! Schedule page. The daemon owns the clock (`/v1/schedules`); this view
//! creates, pauses, runs, edits, and deletes those entries and shows the
//! session each run wrote.

use std::cell::RefCell;
use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::{json, Value};

use load_guard::{commit_load, Commit, ErrPolicy, LoadEpoch};

use crate::load_epoch::begin;

use crate::api::{self, get_json, patch_json, post_json_value};
use crate::outcome;
use crate::shell::Choice;

thread_local! {
    static RUN_CHOICES: RefCell<Vec<Choice>> = RefCell::new(Vec::new());
    static HOLD_CHOICES: RefCell<Vec<Choice>> = RefCell::new(Vec::new());
}

pub fn run_choices() -> Vec<Choice> {
    RUN_CHOICES.with(|rows| rows.borrow().clone())
}

pub fn hold_choices() -> Vec<Choice> {
    HOLD_CHOICES.with(|rows| rows.borrow().clone())
}

fn publish_schedules(items: &[ScheduleItem]) {
    let run = items
        .iter()
        .map(|item| Choice {
            id: item.id.clone(),
            label: if item.name.is_empty() {
                item.id.clone()
            } else {
                item.name.clone()
            },
            detail: item.kind.clone(),
        })
        .collect();
    let hold = items
        .iter()
        .map(|item| {
            let name = if item.name.is_empty() {
                item.id.clone()
            } else {
                item.name.clone()
            };
            let (verb, op) = if item.enabled {
                ("Pause", "pause")
            } else {
                ("Resume", "resume")
            };
            Choice {
                id: format!("{op}\t{}", item.id),
                label: format!("{verb} {name}"),
                detail: item.kind.clone(),
            }
        })
        .collect();
    RUN_CHOICES.with(|rows| *rows.borrow_mut() = run);
    HOLD_CHOICES.with(|rows| *rows.borrow_mut() = hold);
}

#[derive(Clone, Default, Deserialize)]
struct ScheduleItem {
    id: String,
    name: String,
    enabled: bool,
    kind: String,
    #[serde(default)]
    cron: String,
    #[serde(default)]
    every: String,
    #[serde(default)]
    at: String,
    #[serde(default)]
    timezone: String,
    source: String,
    #[serde(default)]
    prompt: String,
    #[serde(default, rename = "goalId")]
    goal_id: String,
    #[serde(default)]
    workflow: String,
    #[serde(default, rename = "workspaceId")]
    workspace_id: String,
    #[serde(default, rename = "permissionMode")]
    permission_mode: String,
    overlap: String,
    missed: String,
    #[serde(default, rename = "nextFire")]
    next_fire: String,
    #[serde(default, rename = "lastStatus")]
    last_status: String,
    #[allow(dead_code)]
    #[serde(default, rename = "lastSessionId")]
    last_session_id: String,
    #[serde(default, rename = "lastError")]
    last_error: String,
}

#[derive(Deserialize)]
struct ScheduleList {
    schedules: Vec<ScheduleItem>,
}

#[derive(Clone, Default, Deserialize)]
struct RunItem {
    #[allow(dead_code)]
    id: String,
    #[serde(default, rename = "firedAt")]
    fired_at: String,
    status: String,
    #[serde(default, rename = "sessionId")]
    session_id: String,
    #[serde(default, rename = "permissionMode")]
    permission_mode: String,
    #[serde(default)]
    error: String,
}

#[derive(Deserialize)]
struct RunList {
    runs: Vec<RunItem>,
}

#[derive(Clone, Default, Deserialize)]
struct Named {
    #[serde(default)]
    id: String,
    #[serde(default)]
    name: String,
    #[serde(default)]
    description: String,
    #[serde(default)]
    valid: bool,
}

#[derive(Deserialize)]
struct GoalList {
    goals: Vec<Named>,
}

#[derive(Deserialize)]
struct WorkflowList {
    workflows: Vec<Named>,
}

/// Name for a duplicated schedule (#1462): append ` (copy)` once so the
/// unsaved copy is distinguishable in the list once saved.
pub fn duplicate_schedule_name(name: &str) -> String {
    let name = name.trim();
    if name.is_empty() {
        return "(copy)".into();
    }
    if name.ends_with("(copy)") {
        return name.to_string();
    }
    format!("{name} (copy)")
}

fn browser_tz() -> String {
    js_sys::eval("Intl.DateTimeFormat().resolvedOptions().timeZone")
        .ok()
        .and_then(|v| v.as_string())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "UTC".into())
}

fn fmt_when(iso: &str) -> String {
    if iso.is_empty() {
        return "not scheduled".into();
    }
    iso.trim_end_matches('Z').replace('T', " ") + " UTC"
}

fn status_label(status: &str) -> &'static str {
    match status {
        "started" => "Running",
        "finished" => "Finished",
        "quiet" => "Quiet",
        "skipped" => "Skipped",
        "missed" => "Missed",
        "error" => "Error",
        "interrupted" => "Interrupted",
        "queued" => "Waiting",
        _ => "New",
    }
}

fn status_badge(status: &str) -> &'static str {
    match status {
        "started" | "queued" => "badge badge-running",
        "finished" | "quiet" => "badge badge-done",
        "error" => "badge badge-failed",
        "skipped" | "missed" | "interrupted" => "badge badge-stalled",
        _ => "badge",
    }
}

fn kind_label(kind: &str) -> &'static str {
    match kind {
        "cron" => "Cron",
        "interval" => "Interval",
        "once" => "One-shot",
        "heartbeat" => "Heartbeat",
        _ => "Schedule",
    }
}

#[component]
pub fn SchedulePage(
    base: RwSignal<String>,
    token: RwSignal<String>,
    mode: RwSignal<String>,
    active_root: RwSignal<String>,
    status: RwSignal<String>,
) -> impl IntoView {
    let items = RwSignal::new(Vec::<ScheduleItem>::new());
    let runs = RwSignal::new(Vec::<RunItem>::new());
    let goals = RwSignal::new(Vec::<Named>::new());
    let workflows = RwSignal::new(Vec::<Named>::new());
    let error = RwSignal::new(String::new());
    let notice = RwSignal::new(String::new());
    let retry_open = RwSignal::new(String::new());
    let editing = RwSignal::new(String::new());
    let confirm_delete = RwSignal::new(String::new());
    let name = RwSignal::new(String::new());
    let kind = RwSignal::new("cron".to_string());
    let cron = RwSignal::new("0 9 * * 1-5".to_string());
    let every = RwSignal::new("1h".to_string());
    let at = RwSignal::new(String::new());
    let timezone = RwSignal::new(browser_tz());
    let source = RwSignal::new("prompt".to_string());
    let prompt = RwSignal::new(String::new());
    let goal_id = RwSignal::new(String::new());
    let workflow = RwSignal::new(String::new());
    let workspace_id = RwSignal::new(String::new());
    let permission_mode = RwSignal::new(String::new());
    let overlap = RwSignal::new("skip".to_string());
    let missed = RwSignal::new("skip".to_string());
    let enabled = RwSignal::new(true);
    // List refresh and history write different signals, so each has its own
    // epoch. One counter would drop a list commit when history starts.
    let list_epoch = RwSignal::new(LoadEpoch::new());
    let history_epoch = RwSignal::new(LoadEpoch::new());
    let history_error = RwSignal::new(String::new());
    let history_loading = RwSignal::new(false);
    let picker_note = RwSignal::new(String::new());

    let load_history = move |id: String| {
        let gen = begin(history_epoch);
        runs.set(Vec::new());
        history_error.set(String::new());
        if id.is_empty() {
            history_loading.set(false);
            return;
        }
        history_loading.set(true);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let history_epoch = history_epoch;
        spawn_local(async move {
            let path = format!("/v1/schedules/{}/history", api::js_encode(&id));
            let fetched = get_json::<RunList>(&b, &t, &path).await;
            let raw = fetched.as_ref().err().cloned();
            let mapped = match fetched {
                Ok(list) => Ok(list.runs),
                Err(_) => Err("History failed to load for this schedule.".into()),
            };
            match history_epoch.with_untracked(|epoch| {
                commit_load(epoch, gen, mapped, Vec::new(), ErrPolicy::Clear)
            }) {
                Commit::Drop => return,
                Commit::Apply { value, error: hist } => {
                    let failed = !hist.is_empty();
                    runs.set(value);
                    history_error.set(hist);
                    history_loading.set(false);
                    if failed {
                        if let Some(raw) = raw {
                            error.set(raw);
                        }
                    }
                }
            }
        });
    };

    let refresh = move || {
        let gen = begin(list_epoch);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let keep = editing.get_untracked();
        let list_epoch = list_epoch;
        let previous = items.get_untracked();
        spawn_local(async move {
            let schedules = get_json::<ScheduleList>(&b, &t, "/v1/schedules").await;
            let mapped = schedules.map(|list| list.schedules);
            match list_epoch
                .with_untracked(|epoch| commit_load(epoch, gen, mapped, previous, ErrPolicy::Keep))
            {
                Commit::Drop => return,
                Commit::Apply {
                    value,
                    error: list_error,
                } => {
                    if list_error.is_empty() {
                        publish_schedules(&value);
                    }
                    items.set(value);
                    error.set(list_error);
                }
            }
            let goals_res = get_json::<GoalList>(&b, &t, "/v1/goals").await;
            if !list_epoch.with_untracked(|epoch| epoch.accept(gen)) {
                return;
            }
            let goals_failed = match goals_res {
                Ok(list) => {
                    goals.set(list.goals);
                    false
                }
                Err(_) => true,
            };
            let workflows_res = get_json::<WorkflowList>(&b, &t, "/v1/workflows").await;
            if !list_epoch.with_untracked(|epoch| epoch.accept(gen)) {
                return;
            }
            let workflows_failed = match workflows_res {
                Ok(list) => {
                    workflows.set(list.workflows);
                    false
                }
                Err(_) => true,
            };
            let mut notes = Vec::new();
            if goals_failed {
                notes.push("Could not load goals — the picker may be incomplete.");
            }
            if workflows_failed {
                notes.push("Could not load workflows — the picker may be incomplete.");
            }
            picker_note.set(notes.join(" "));
        });
        if !keep.is_empty() {
            load_history(keep);
        }
    };

    refresh();

    let run_saved = {
        let base = base;
        let token = token;
        Rc::new(move |id: String| {
            if id.is_empty() {
                error.set("Save the schedule before running it.".into());
                return;
            }
            let (b, t) = (base.get_untracked(), token.get_untracked());
            spawn_local(async move {
                match post_json_value(&b, &t, &format!("/v1/schedules/{id}/run"), json!({})).await {
                    Ok(v) => {
                        error.set(String::new());
                        let sid = v
                            .get("sessionId")
                            .and_then(|s| s.as_str())
                            .unwrap_or("")
                            .to_string();
                        if sid.is_empty() {
                            notice.set("Run recorded.".into());
                        } else {
                            notice.set(format!("Started session {sid}."));
                        }
                        status.set(format!("Schedule {id} fired."));
                        refresh();
                    }
                    Err(e) => {
                        notice.set(String::new());
                        error.set(e);
                    }
                }
            });
        })
    };
    let hold_saved = {
        let base = base;
        let token = token;
        Rc::new(move |args: String| {
            let (op, id) = args.split_once('\t').unwrap_or(("pause", args.as_str()));
            let id = id.to_string();
            if id.is_empty() {
                return;
            }
            let path = format!("/v1/schedules/{id}/{op}");
            let (b, t) = (base.get_untracked(), token.get_untracked());
            let resumed = op == "resume";
            spawn_local(async move {
                match post_json_value(&b, &t, &path, json!({})).await {
                    Ok(_) => {
                        error.set(String::new());
                        notice.set(
                            if resumed {
                                "Resumed. Missed fires stay missed.".into()
                            } else {
                                "Paused.".into()
                            },
                        );
                        enabled.set(resumed);
                        refresh();
                    }
                    Err(e) => error.set(e),
                }
            });
        })
    };
    {
        let run_bind = run_saved.clone();
        let hold_bind = hold_saved.clone();
        crate::shell::bind(
            "schedule.run",
            Rc::new(move |id| {
                run_bind(id.to_string());
                Ok(())
            }),
        );
        crate::shell::bind(
            "schedule.hold",
            Rc::new(move |args| {
                hold_bind(args.to_string());
                Ok(())
            }),
        );
        if let Some(id) = crate::park::take("schedule.run") {
            run_saved(id);
        }
        if let Some(args) = crate::park::take("schedule.hold") {
            hold_saved(args);
        }
    }

    let clear_form = move || {
        editing.set(String::new());
        confirm_delete.set(String::new());
        name.set(String::new());
        kind.set("cron".into());
        cron.set("0 9 * * 1-5".into());
        every.set("1h".into());
        at.set(String::new());
        source.set("prompt".into());
        prompt.set(String::new());
        goal_id.set(String::new());
        workflow.set(String::new());
        workspace_id.set(String::new());
        permission_mode.set(String::new());
        overlap.set("skip".into());
        missed.set("skip".into());
        enabled.set(true);
        let _ = begin(history_epoch);
        runs.set(Vec::new());
        history_error.set(String::new());
        history_loading.set(false);
        notice.set(String::new());
    };

    let fill = move |item: ScheduleItem| {
        editing.set(item.id.clone());
        confirm_delete.set(String::new());
        name.set(item.name);
        kind.set(item.kind);
        cron.set(item.cron);
        every.set(if item.every.is_empty() {
            "1h".into()
        } else {
            item.every
        });
        at.set(item.at);
        timezone.set(if item.timezone.is_empty() {
            "UTC".into()
        } else {
            item.timezone
        });
        source.set(item.source);
        prompt.set(item.prompt);
        goal_id.set(item.goal_id);
        workflow.set(item.workflow);
        workspace_id.set(item.workspace_id);
        permission_mode.set(item.permission_mode);
        overlap.set(item.overlap);
        missed.set(item.missed);
        enabled.set(item.enabled);
        notice.set(String::new());
        load_history(editing.get_untracked());
    };

    // Duplicate (#1462): keep every loaded field, but turn the form into a
    // new unsaved schedule. Clearing `editing` makes Save take the existing
    // POST create path; history is dropped and the copy starts disabled so
    // a copied cron never fires before review. Nothing is written until
    // the operator presses Create.
    let duplicate = move || {
        if editing.get_untracked().is_empty() {
            return;
        }
        name.set(duplicate_schedule_name(&name.get_untracked()));
        editing.set(String::new());
        confirm_delete.set(String::new());
        enabled.set(false);
        let _ = begin(history_epoch);
        runs.set(Vec::new());
        history_error.set(String::new());
        history_loading.set(false);
        error.set(String::new());
        notice.set(
            "Duplicated as a new schedule (copy). It starts paused/disabled — review the fields, then Create to save it. Nothing is saved yet."
                .into(),
        );
    };

    let save = move || {
        let body = json!({
            "name": name.get_untracked(),
            "kind": kind.get_untracked(),
            "cron": cron.get_untracked(),
            "every": every.get_untracked(),
            "at": at.get_untracked(),
            "timezone": timezone.get_untracked(),
            "source": source.get_untracked(),
            "prompt": prompt.get_untracked(),
            "goalId": goal_id.get_untracked(),
            "workflow": workflow.get_untracked(),
            "workspaceId": workspace_id.get_untracked(),
            "permissionMode": permission_mode.get_untracked(),
            "overlap": overlap.get_untracked(),
            "missed": missed.get_untracked(),
            "enabled": enabled.get_untracked(),
        });
        let (b, t, id) = (
            base.get_untracked(),
            token.get_untracked(),
            editing.get_untracked(),
        );
        spawn_local(async move {
            let result = if id.is_empty() {
                post_json_value(&b, &t, "/v1/schedules", body).await
            } else {
                patch_json(&b, &t, &format!("/v1/schedules/{id}"), body)
                    .await
                    .map(|_| Value::Null)
            };
            match result {
                Ok(v) => {
                    error.set(String::new());
                    if id.is_empty() {
                        if let Some(new_id) =
                            v.get("id").and_then(|s| s.as_str()).map(str::to_string)
                        {
                            if !new_id.is_empty() {
                                editing.set(new_id.clone());
                                load_history(new_id);
                            }
                        }
                        notice.set("Schedule saved. It stays quiet until the next fire.".into());
                    } else {
                        notice.set("Schedule updated.".into());
                    }
                    refresh();
                }
                Err(e) => {
                    notice.set(String::new());
                    error.set(e);
                }
            }
        });
    };

    let remove = move || {
        let id = editing.get_untracked();
        if id.is_empty() {
            return;
        }
        if confirm_delete.get_untracked() != id {
            confirm_delete.set(id);
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let resp =
                gloo_net::http::Request::delete(&api::api(&b, &format!("/v1/schedules/{id}")))
                    .header("Authorization", &api::auth_header(&t))
                    .send()
                    .await;
            match resp {
                Ok(resp) if resp.ok() => {
                    clear_form();
                    notice.set("Schedule deleted.".into());
                    refresh();
                }
                Ok(resp) => error.set(format!("HTTP {}", resp.status())),
                Err(e) => error.set(e.to_string()),
            }
        });
    };

    let open_session = move |id: String| {
        if id.is_empty() {
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        error.set(String::new());
        retry_open.set(String::new());
        spawn_local(async move {
            let result = post_json_value(&b, &t, &format!("/v1/roots/{id}/activate"), json!({}))
                .await
                .map(|_| ());
            let decision = outcome::schedule_open(&id, result);
            if decision.opened {
                active_root.set(id);
                mode.set("chat".into());
                status.set(decision.status);
                error.set(String::new());
                retry_open.set(String::new());
            } else {
                error.set(decision.error);
                retry_open.set(id);
            }
        });
    };

    view! {
        <div class="sched">
            <div class="section-head">
                <h2 class="panel-title">"Schedule"</h2>
                <button class="secondary" type="button" on:click=move |_| clear_form()>"New"</button>
            </div>
            <p class="hint">"A clock in the daemon. Unattended runs use the workspace permission mode, or plan when the workspace has none. Plan denies writes."</p>
            <Show when=move || !error.get().is_empty()>
                <p class="sched-error" role="alert">{move || error.get()}</p>
            </Show>
            <Show when=move || !retry_open.get().is_empty()>
                <button type="button" class="secondary" data-sched-retry="true" on:click=move |_| open_session(retry_open.get())>"Retry"</button>
            </Show>
            <Show when=move || !notice.get().is_empty()>
                <p class="sched-note" role="status">{move || notice.get()}</p>
            </Show>
            <Show when=move || !picker_note.get().is_empty()>
                <p class="hint" role="status">{move || picker_note.get()}</p>
            </Show>
            <div class="sched-layout">
                <div class="sched-list">
                    {move || {
                        let rows = items.get();
                        if rows.is_empty() {
                            return view! {
                                <div class="empty-state">
                                    <strong>"Nothing is scheduled"</strong>
                                    <p>"Create a cron, an interval, a one-shot, or a heartbeat. A heartbeat stays quiet until the workspace changes."</p>
                                </div>
                            }.into_any();
                        }
                        let current = editing.get();
                        let rows = rows.into_iter().map(|item| {
                            let id = item.id.clone();
                            let selected = id == current;
                            let label = item.name.clone();
                            let err_bit = if item.last_error.is_empty() {
                                String::new()
                            } else {
                                format!(" · {}", item.last_error)
                            };
                            let meta = format!(
                                "{} · {}{}",
                                kind_label(&item.kind),
                                fmt_when(&item.next_fire),
                                err_bit
                            );
                            let badge = status_badge(&item.last_status).to_string();
                            let state = if !item.enabled { "Paused".to_string() } else { status_label(&item.last_status).to_string() };
                            let item_for_click = item.clone();
                            view! {
                                <button type="button" class=if selected { "sched-row on" } else { "sched-row" } on:click=move |_| fill(item_for_click.clone())>
                                    <span class="name">{label}</span>
                                    <span class="meta">{meta}</span>
                                    <span class=badge>{state}</span>
                                </button>
                            }
                        }).collect_view();
                        view! { <div class="sched-rows">{rows}</div> }.into_any()
                    }}
                </div>
                <form class="sched-form" on:submit=move |ev: web_sys::SubmitEvent| {
                    ev.prevent_default();
                    save();
                }>
                    <label>"Name"
                        <input id="schedule-name" autofocus=true required=true prop:value=move || name.get() on:input=move |ev| name.set(event_target_value(&ev)) />
                    </label>
                    <label>"Kind"
                        <select prop:value=move || kind.get() on:change=move |ev| kind.set(event_target_value(&ev))>
                            <option value="cron">"Cron"</option>
                            <option value="interval">"Interval"</option>
                            <option value="once">"One-shot"</option>
                            <option value="heartbeat">"Heartbeat"</option>
                        </select>
                    </label>
                    <Show when=move || kind.get() == "cron">
                        <label>"Cron"
                            <input prop:value=move || cron.get() on:input=move |ev| cron.set(event_target_value(&ev)) />
                        </label>
                    </Show>
                    <Show when=move || kind.get() == "interval" || kind.get() == "heartbeat">
                        <label>"Every"
                            <input prop:value=move || every.get() on:input=move |ev| every.set(event_target_value(&ev)) placeholder="1h" />
                        </label>
                    </Show>
                    <Show when=move || kind.get() == "once">
                        <label>"At (RFC3339)"
                            <input prop:value=move || at.get() on:input=move |ev| at.set(event_target_value(&ev)) placeholder="2026-10-27T09:00:00Z" />
                        </label>
                    </Show>
                    <label>"Timezone"
                        <input prop:value=move || timezone.get() on:input=move |ev| timezone.set(event_target_value(&ev)) />
                    </label>
                    <label>"Starts from"
                        <select prop:value=move || source.get() on:change=move |ev| source.set(event_target_value(&ev))>
                            <option value="prompt">"Prompt"</option>
                            <option value="goal">"Goal"</option>
                            <option value="workflow">"Workflow"</option>
                        </select>
                    </label>
                    <label class="span-2">"Prompt"
                        <textarea prop:value=move || prompt.get() on:input=move |ev| prompt.set(event_target_value(&ev)) rows="4"></textarea>
                    </label>
                    <Show when=move || source.get() == "goal">
                        <label>"Goal"
                            <input list="sched-goals" prop:value=move || goal_id.get() on:input=move |ev| goal_id.set(event_target_value(&ev)) />
                        </label>
                    </Show>
                    <Show when=move || source.get() == "workflow">
                        <label>"Workflow"
                            <input list="sched-workflows" prop:value=move || workflow.get() on:input=move |ev| workflow.set(event_target_value(&ev)) />
                        </label>
                    </Show>
                    <label>"Workspace id"
                        <input prop:value=move || workspace_id.get() on:input=move |ev| workspace_id.set(event_target_value(&ev)) />
                    </label>
                    <label>"Mode if the workspace has none"
                        <select prop:value=move || permission_mode.get() on:change=move |ev| permission_mode.set(event_target_value(&ev))>
                            <option value="">"Plan (safest)"</option>
                            <option value="plan">"Plan"</option>
                            <option value="default">"Default"</option>
                            <option value="soft-approve">"Soft approve"</option>
                            <option value="accept-edits">"Accept edits"</option>
                            <option value="yolo">"Yolo"</option>
                        </select>
                    </label>
                    <label>"Overlap"
                        <select prop:value=move || overlap.get() on:change=move |ev| overlap.set(event_target_value(&ev))>
                            <option value="skip">"Skip if the last session is busy"</option>
                            <option value="queue">"Wait, then run once"</option>
                            <option value="allow">"Allow another session"</option>
                        </select>
                    </label>
                    <label>"Missed while down"
                        <select prop:value=move || missed.get() on:change=move |ev| missed.set(event_target_value(&ev))>
                            <option value="skip">"Skip"</option>
                            <option value="once">"Run the latest once"</option>
                        </select>
                    </label>
                    <div class="sched-actions">
                        <button type="submit" class="primary">{move || if editing.get().is_empty() { "Create" } else { "Save" }}</button>
                        <button type="button" class="secondary" id="schedule-duplicate" disabled=move || editing.get().is_empty() on:click=move |_| duplicate()>"Duplicate"</button>
                        <button type="button" class="secondary" on:click=move |_| remove()>
                            {move || if confirm_delete.get() == editing.get() && !editing.get().is_empty() { "Confirm delete" } else { "Delete" }}
                        </button>
                    </div>
                    <datalist id="sched-goals">
                        {move || goals.get().into_iter().map(|g| {
                            let value = g.id.clone();
                            let label = if g.description.is_empty() { g.id.clone() } else { g.description };
                            view! { <option value=value>{label}</option> }
                        }).collect_view()}
                    </datalist>
                    <datalist id="sched-workflows">
                        {move || workflows.get().into_iter().filter(|w| w.valid).map(|w| {
                            let value = w.name.clone();
                            view! { <option value=value>{value.clone()}</option> }
                        }).collect_view()}
                    </datalist>
                    <div class="sched-history">
                        <h3>"History"</h3>
                        {move || {
                            let rows = runs.get();
                            if editing.get().is_empty() {
                                return view! { <p class="hint">"History appears after you select a schedule."</p> }.into_any();
                            }
                            if history_loading.get() {
                                return view! { <p class="hint">"Loading history…"</p> }.into_any();
                            }
                            if !history_error.get().is_empty() {
                                return view! { <p class="hint" role="alert">{history_error.get()}</p> }.into_any();
                            }
                            if rows.is_empty() {
                                return view! { <p class="hint">"No runs yet."</p> }.into_any();
                            }
                            let rows = rows.into_iter().map(|run| {
                                let when = fmt_when(&run.fired_at);
                                let badge = status_badge(&run.status).to_string();
                                let label = status_label(&run.status).to_string();
                                let sid = run.session_id.clone();
                                let sid_for_click = sid.clone();
                                let empty = sid.is_empty();
                                let sid_label = if empty { "no session".to_string() } else { sid };
                                let detail = if run.error.is_empty() {
                                    run.permission_mode.clone()
                                } else if run.permission_mode.is_empty() {
                                    run.error.clone()
                                } else {
                                    format!("{} · {}", run.permission_mode, run.error)
                                };
                                view! {
                                    <div class="sched-run">
                                        <span class="meta">{when}</span>
                                        <span class=badge>{label}</span>
                                        <button type="button" class="linkish" disabled=empty on:click=move |_| open_session(sid_for_click.clone())>{sid_label}</button>
                                        <span class="meta">{detail}</span>
                                    </div>
                                }
                            }).collect_view();
                            view! { <div>{rows}</div> }.into_any()
                        }}
                    </div>
                </form>
            </div>
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn duplicate_name_appends_copy_once() {
        assert_eq!(duplicate_schedule_name("Morning"), "Morning (copy)");
        assert_eq!(duplicate_schedule_name("Morning (copy)"), "Morning (copy)");
        assert_eq!(duplicate_schedule_name(""), "(copy)");
        assert_eq!(duplicate_schedule_name("  Standup  "), "Standup (copy)");
    }
}
