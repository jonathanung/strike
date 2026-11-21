//! Schedule page. The daemon owns the clock (`/v1/schedules`); this view
//! creates, pauses, runs, edits, and deletes those entries and shows the
//! session each run wrote.

use std::cell::RefCell;
use std::rc::Rc;

use leptos::prelude::*;
use crate::error_notice::humanize;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::{json, Value};

use load_guard::{commit_load, Commit, ErrPolicy, LoadEpoch};

use crate::load_epoch::begin;

use crate::api::{self, get_json, get_text, patch_json, post_json_value};
use crate::outcome;
use crate::scheduler_presets::SchedulerPresetsSection;
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
    // Script-only fields (#1777). The daemon never sends a script entry's
    // payload for other sources.
    #[serde(default)]
    command: String,
    #[serde(default)]
    dir: String,
    #[serde(default)]
    timeout: String,
    #[serde(default, rename = "secretRefs")]
    secret_refs: Vec<String>,
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
    // Script-run fields (#1777): no session; exit code and a scrubbed tail.
    #[serde(default, rename = "exitCode")]
    exit_code: Option<i64>,
    #[serde(default, rename = "outputPreview")]
    output_preview: String,
    #[serde(default, rename = "outputRef")]
    output_ref: String,
    #[serde(default)]
    decision: String,
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

#[cfg(target_arch = "wasm32")]
fn browser_tz() -> String {
    js_sys::eval("Intl.DateTimeFormat().resolvedOptions().timeZone")
        .ok()
        .and_then(|v| v.as_string())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "UTC".into())
}

#[cfg(not(target_arch = "wasm32"))]
fn browser_tz() -> String {
    "UTC".into()
}

/// The pre-#1465 rendering: the raw instant labelled UTC. Kept as the
/// final fallback and as the exact instant shown in the title tooltip.
fn utc_when(iso: &str) -> String {
    iso.trim_end_matches('Z').replace('T', " ") + " UTC"
}

/// Whose zone wins: the schedule's own `timezone` when non-empty, then
/// the browser's zone, then UTC. Pure so the fallback chain is testable
/// off-wasm.
fn pick_zone<'a>(schedule_tz: &'a str, browser_tz: &'a str) -> &'a str {
    let schedule = schedule_tz.trim();
    if !schedule.is_empty() {
        return schedule;
    }
    let browser = browser_tz.trim();
    if !browser.is_empty() {
        return browser;
    }
    "UTC"
}

/// Format `iso` in `tz` via `Intl.DateTimeFormat`, including the zone
/// abbreviation (e.g. `Oct 28, 2026, 09:00 PDT`). Returns `None` when
/// the timestamp is unparseable or the zone is invalid so the caller
/// can fall back. Off-wasm there is no `Intl`, so it always falls back.
#[cfg(target_arch = "wasm32")]
fn format_in_zone(iso: &str, tz: &str) -> Option<String> {
    let iso_json = serde_json::to_string(iso).ok()?;
    let tz_json = serde_json::to_string(tz).ok()?;
    let script = format!(
        "(function(){{try{{var d=new Date({iso_json});\
         if(isNaN(d.getTime()))return null;\
         var f=new Intl.DateTimeFormat('en-US',{{timeZone:{tz_json},\
         year:'numeric',month:'short',day:'numeric',hour:'2-digit',\
         minute:'2-digit',hourCycle:'h23',timeZoneName:'short'}});\
         return f.format(d);}}catch(e){{return null;}}}})()"
    );
    js_sys::eval(&script)
        .ok()
        .and_then(|v| v.as_string())
        .filter(|s| !s.is_empty())
}

#[cfg(not(target_arch = "wasm32"))]
fn format_in_zone(_iso: &str, _tz: &str) -> Option<String> {
    None
}

fn fmt_when_with_browser(iso: &str, schedule_tz: &str, browser: &str) -> String {
    if iso.is_empty() {
        return "not scheduled".into();
    }
    let zone = pick_zone(schedule_tz, browser);
    if let Some(formatted) = format_in_zone(iso, zone) {
        return formatted;
    }
    // The schedule's zone was invalid (or unformattable): retry in the
    // browser's zone before giving up to the UTC rendering.
    let browser = browser.trim();
    if zone != browser && !browser.is_empty() {
        if let Some(formatted) = format_in_zone(iso, browser) {
            return formatted;
        }
    }
    // Unparseable timestamps keep the old behaviour: print what the
    // server sent, labelled UTC, rather than rendering blank.
    utc_when(iso)
}

/// Single formatter for every schedule time (next fire + run history).
/// Renders in the schedule's own timezone when set, then the browser's
/// zone, then the raw UTC rendering.
fn fmt_when(iso: &str, schedule_tz: &str) -> String {
    if iso.is_empty() {
        return "not scheduled".into();
    }
    fmt_when_with_browser(iso, schedule_tz, &browser_tz())
}

/// Exact UTC instant for the `title` tooltip next to a `fmt_when` time.
fn fmt_when_title(iso: &str) -> String {
    if iso.is_empty() {
        return String::new();
    }
    utc_when(iso)
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
        "failed" => "Failed",
        "denied" => "Denied",
        _ => "New",
    }
}

fn status_badge(status: &str) -> &'static str {
    match status {
        "started" | "queued" => "badge badge-running",
        "finished" | "quiet" => "badge badge-done",
        "error" | "failed" | "denied" => "badge badge-failed",
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

/// Script schedules (#1777) are an explicit opt-in: the source value the
/// daemon validates. Never the form default.
const SOURCE_SCRIPT: &str = "script";

/// The mode a script run will use, as the confirm step states it. The
/// daemon resolves workspace mode first, then the schedule's, then plan.
fn resolved_mode_label(permission_mode: &str) -> String {
    let mode = match permission_mode.trim() {
        "" | "plan" => "Plan",
        "default" => "Default",
        "soft-approve" => "Soft approve",
        "accept-edits" => "Accept edits",
        "yolo" => "Yolo",
        other => other,
    };
    format!("{mode} (a workspace permission mode, when set, wins)")
}

/// The OS sandbox dial a script run will use, from GET /v1/sandbox.
fn sandbox_dial_label(mode: &str) -> String {
    match mode.trim() {
        "off" => "Off: runs on the host".into(),
        "read-only" => "Read-only".into(),
        "workspace-write" => "Workspace write".into(),
        "" => "Unknown (sandbox status unavailable)".into(),
        other => other.to_string(),
    }
}

/// Secret refs typed one per line (or comma-separated), trimmed, blanks
/// dropped. The daemon validates each as secret://env/NAME.
fn parse_secret_refs(text: &str) -> Vec<String> {
    text.split(|c| c == '\n' || c == ',')
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(str::to_string)
        .collect()
}

/// Exit code column for a script run: blank dash when no process started.
fn exit_label(code: Option<i64>) -> String {
    match code {
        Some(n) => format!("exit {n}"),
        None => "—".into(),
    }
}

/// The daemon's run-history cap (#1491): `maxRuns` in
/// `internal/schedule/model.go`. The history response carries no total or
/// dropped count, so the client can only be honest at the cap itself —
/// keep this constant pointed at the daemon's value; if the daemon ever
/// exposes the cap in the response, prefer that over this constant.
const HISTORY_CAP: usize = 50;

/// The honesty label for the History panel (#1491): shown only when the
/// loaded list is at the daemon cap. Below the cap the panel is complete
/// and gets no extra chrome; there is no Show-more, because runs past the
/// cap are discarded daemon-side, not merely unloaded.
fn history_cap_notice(run_count: usize) -> Option<String> {
    if run_count == HISTORY_CAP {
        Some(format!(
            "Showing the newest {HISTORY_CAP} runs — older runs are not kept."
        ))
    } else {
        None
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
    // Script schedule fields (#1777). `confirming` is the arm step: the
    // first submit of a script shows the full command, mode, and sandbox
    // dial; only "Confirm and arm" sends it with confirm: true.
    let command = RwSignal::new(String::new());
    let dir = RwSignal::new(String::new());
    let script_timeout = RwSignal::new(String::new());
    let secret_refs = RwSignal::new(String::new());
    let confirming = RwSignal::new(false);
    let sandbox_mode = RwSignal::new(String::new());
    // Saved source of the schedule being edited: the daemon refuses to
    // change a schedule to or from script, so the picker locks that edge.
    let saved_source = RwSignal::new(String::new());
    // "Open output" for one script run: (run id, sidecar text or error).
    let output_open = RwSignal::new((String::new(), String::new()));
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

    {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Ok(snap) = get_json::<crate::sandbox_explain::SandboxSnapshot>(&b, &t, "/v1/sandbox").await {
                sandbox_mode.set(snap.mode);
            }
        });
    }

    let open_output = move |schedule_id: String, run_id: String| {
        if output_open.get_untracked().0 == run_id {
            output_open.set((String::new(), String::new()));
            return;
        }
        output_open.set((run_id.clone(), "Loading output…".into()));
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!(
                "/v1/schedules/{}/runs/{}/output",
                api::js_encode(&schedule_id),
                api::js_encode(&run_id)
            );
            let text = match get_text(&b, &t, &path).await {
                Ok(text) if text.is_empty() => "No output was kept for this run.".to_string(),
                Ok(text) => text,
                Err(_) => "Output is not available for this run.".to_string(),
            };
            if output_open.get_untracked().0 == run_id {
                output_open.set((run_id, text));
            }
        });
    };

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
        command.set(String::new());
        dir.set(String::new());
        script_timeout.set(String::new());
        secret_refs.set(String::new());
        confirming.set(false);
        saved_source.set(String::new());
        output_open.set((String::new(), String::new()));
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
        saved_source.set(item.source.clone());
        source.set(item.source);
        command.set(item.command);
        dir.set(item.dir);
        script_timeout.set(item.timeout);
        secret_refs.set(item.secret_refs.join("\n"));
        confirming.set(false);
        output_open.set((String::new(), String::new()));
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
        saved_source.set(String::new());
        confirming.set(false);
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
        let script = source.get_untracked() == SOURCE_SCRIPT;
        let mut body = json!({
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
        if script {
            // A script carries a command only, overlap is always skip, and
            // the write is the arming: confirm is sent only from the
            // confirm step.
            body["prompt"] = json!("");
            body["goalId"] = json!("");
            body["workflow"] = json!("");
            body["overlap"] = json!("skip");
            body["command"] = json!(command.get_untracked());
            body["dir"] = json!(dir.get_untracked());
            body["timeout"] = json!(script_timeout.get_untracked());
            body["secretRefs"] = json!(parse_secret_refs(&secret_refs.get_untracked()));
            body["confirm"] = json!(true);
        }
        confirming.set(false);
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
                        notice.set(if script {
                            "Script schedule armed. It runs at the next fire.".into()
                        } else {
                            "Schedule saved. It stays quiet until the next fire.".into()
                        });
                    } else {
                        notice.set(if script {
                            "Script schedule re-armed.".into()
                        } else {
                            "Schedule updated.".into()
                        });
                    }
                    if script {
                        saved_source.set(SOURCE_SCRIPT.into());
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
                active_root.set(id.clone());
                // This path bypasses `activate_root` in main.rs, so it
                // must persist the viewed session itself (#1391).
                crate::session_restore::persist(&id);
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
                <p class="sched-error" role="alert">{move || humanize(&error.get())}</p>
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
                            let when = fmt_when(&item.next_fire, &item.timezone);
                            let when_title = fmt_when_title(&item.next_fire);
                            let meta_prefix = format!("{} · ", kind_label(&item.kind));
                            let is_script = item.source == SOURCE_SCRIPT;
                            let badge = status_badge(&item.last_status).to_string();
                            let state = if !item.enabled { "Paused".to_string() } else { status_label(&item.last_status).to_string() };
                            let item_for_click = item.clone();
                            view! {
                                <button type="button" class=if selected { "sched-row on" } else { "sched-row" } on:click=move |_| fill(item_for_click.clone())>
                                    <span class="name">{label}</span>
                                    <Show when=move || is_script>
                                        <span class="badge sched-script-badge">"script"</span>
                                    </Show>
                                    <span class="meta">{meta_prefix}<span title=when_title>{when}</span>{err_bit}</span>
                                    <span class=badge>{state}</span>
                                </button>
                            }
                        }).collect_view();
                        view! { <div class="sched-rows">{rows}</div> }.into_any()
                    }}
                </div>
                <form class="sched-form" on:submit=move |ev: web_sys::SubmitEvent| {
                    ev.prevent_default();
                    // A script is armed only from the confirm step.
                    if source.get_untracked() == SOURCE_SCRIPT && !confirming.get_untracked() {
                        error.set(String::new());
                        confirming.set(true);
                        return;
                    }
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
                        <select id="schedule-source" prop:value=move || source.get() on:change={move |ev| {
                            let next = event_target_value(&ev);
                            if next == SOURCE_SCRIPT {
                                overlap.set("skip".into());
                            }
                            confirming.set(false);
                            source.set(next);
                        }}>
                            // A saved schedule cannot change to or from a
                            // script (the daemon refuses); lock that edge.
                            <option value="prompt" disabled={move || saved_source.get() == SOURCE_SCRIPT}>"Prompt"</option>
                            <option value="goal" disabled={move || saved_source.get() == SOURCE_SCRIPT}>"Goal"</option>
                            <option value="workflow" disabled={move || saved_source.get() == SOURCE_SCRIPT}>"Workflow"</option>
                            <option value="script" disabled={move || !saved_source.get().is_empty() && saved_source.get() != SOURCE_SCRIPT}>"Script (shell command, no agent)"</option>
                        </select>
                    </label>
                    <Show when={move || source.get() != SOURCE_SCRIPT}>
                        <label class="span-2">"Prompt"
                            <textarea prop:value=move || prompt.get() on:input=move |ev| prompt.set(event_target_value(&ev)) rows="4"></textarea>
                        </label>
                    </Show>
                    <Show when={move || source.get() == SOURCE_SCRIPT}>
                        <p class="hint span-2">"Runs bash -c with your permission rules, hooks, and sandbox dial. No agent session. You confirm the exact command before it is armed."</p>
                        <label class="span-2">"Command"
                            <textarea class="sched-command" id="schedule-command" required=true spellcheck="false" prop:value=move || command.get() on:input={move |ev| { confirming.set(false); command.set(event_target_value(&ev)) }} rows="3" placeholder="make test"></textarea>
                        </label>
                        <label>"Working directory"
                            <input prop:value=move || dir.get() on:input={move |ev| { confirming.set(false); dir.set(event_target_value(&ev)) }} placeholder="Workspace path" />
                        </label>
                        <label>"Timeout"
                            <input prop:value=move || script_timeout.get() on:input=move |ev| script_timeout.set(event_target_value(&ev)) placeholder="30m (10s to 6h)" />
                        </label>
                        <label class="span-2">"Secret refs (optional, one per line)"
                            <textarea prop:value=move || secret_refs.get() on:input=move |ev| secret_refs.set(event_target_value(&ev)) rows="2" placeholder="secret://env/NAME"></textarea>
                        </label>
                    </Show>
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
                            <option value="skip">"Skip if the last run is busy"</option>
                            <option value="queue" disabled={move || source.get() == SOURCE_SCRIPT}>"Wait, then run once"</option>
                            <option value="allow" disabled={move || source.get() == SOURCE_SCRIPT}>"Allow another session"</option>
                        </select>
                    </label>
                    <label>"Missed while down"
                        <select prop:value=move || missed.get() on:change=move |ev| missed.set(event_target_value(&ev))>
                            <option value="skip">"Skip"</option>
                            <option value="once">"Run the latest once"</option>
                        </select>
                    </label>
                    <Show when={move || confirming.get() && source.get() == SOURCE_SCRIPT}>
                        <div class="sched-confirm span-2" role="group" aria-label="Confirm script schedule">
                            <h3>"Confirm the command"</h3>
                            <p class="hint">"This exact command runs unattended on every fire. Deny rules and hooks still apply; an ask is answered by this confirmation."</p>
                            <pre class="sched-output" id="schedule-confirm-command">{move || command.get()}</pre>
                            <p class="meta">"Mode: "{move || resolved_mode_label(&permission_mode.get())}</p>
                            <p class="meta">"Sandbox: "{move || sandbox_dial_label(&sandbox_mode.get())}</p>
                            <p class="meta">"Directory: "{move || if dir.get().trim().is_empty() { "workspace path".to_string() } else { dir.get() }}</p>
                            <div class="sched-actions">
                                <button type="submit" class="primary" id="schedule-confirm">"Confirm and arm"</button>
                                <button type="button" class="secondary" on:click=move |_| confirming.set(false)>"Back"</button>
                            </div>
                        </div>
                    </Show>
                    <div class="sched-actions">
                        <button type="submit" class="primary" hidden={move || confirming.get() && source.get() == SOURCE_SCRIPT}>{move || {
                            if source.get() == SOURCE_SCRIPT {
                                "Review command"
                            } else if editing.get().is_empty() {
                                "Create"
                            } else {
                                "Save"
                            }
                        }}</button>
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
                        {move || history_cap_notice(runs.get().len()).map(|label| view! { <p class="hint">{label}</p> })}
                        {move || {
                            let rows = runs.get();
                            if editing.get().is_empty() {
                                return view! { <p class="hint">"History appears after you select a schedule."</p> }.into_any();
                            }
                            if history_loading.get() {
                                return view! { <p class="hint">"Loading history…"</p> }.into_any();
                            }
                            if !history_error.get().is_empty() {
                                return view! { <p class="hint" role="alert">{humanize(&history_error.get())}</p> }.into_any();
                            }
                            if rows.is_empty() {
                                return view! { <p class="hint">"No runs yet."</p> }.into_any();
                            }
                            // The selected schedule's saved zone, not the
                            // edit field: an unsaved (or half-typed) zone must
                            // not re-label runs that fired under the saved one.
                            let sel = editing.get();
                            let selected = items.get().into_iter().find(|s| s.id == sel);
                            let hist_tz = selected.as_ref().map(|s| s.timezone.clone()).unwrap_or_default();
                            let script_history = selected.map(|s| s.source == SOURCE_SCRIPT).unwrap_or(false);
                            let rows = rows.into_iter().map(|run| {
                                let when = fmt_when(&run.fired_at, &hist_tz);
                                let when_title = fmt_when_title(&run.fired_at);
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
                                if script_history {
                                    // Script runs have no session: exit code,
                                    // scrubbed output tail, and the sidecar.
                                    let exit = exit_label(run.exit_code);
                                    let preview = run.output_preview.clone();
                                    let has_preview = !preview.is_empty();
                                    let run_id = run.id.clone();
                                    let run_for_show = run.id.clone();
                                    let has_output = !run.output_ref.is_empty();
                                    let schedule_id = sel.clone();
                                    let detail = if run.decision.is_empty() {
                                        detail
                                    } else if detail.is_empty() {
                                        run.decision.clone()
                                    } else {
                                        format!("{} · {}", run.decision, detail)
                                    };
                                    return view! {
                                        <div class="sched-run sched-run-script">
                                            <span class="meta" title=when_title>{when}</span>
                                            <span class=badge>{label}</span>
                                            <span class="meta sched-exit">{exit}</span>
                                            <span class="meta">{detail}</span>
                                            <Show when=move || has_output>
                                                <button type="button" class="linkish" on:click={
                                                    let schedule_id = schedule_id.clone();
                                                    let run_id = run_id.clone();
                                                    move |_| open_output(schedule_id.clone(), run_id.clone())
                                                }>{
                                                    let run_id = run_id.clone();
                                                    move || if output_open.get().0 == run_id { "Hide output" } else { "Open output" }
                                                }</button>
                                            </Show>
                                            <Show when=move || has_preview>
                                                <pre class="sched-output">{preview.clone()}</pre>
                                            </Show>
                                            {move || {
                                                let (open, text) = output_open.get();
                                                (open == run_for_show).then(|| view! { <pre class="sched-output sched-output-full">{text}</pre> })
                                            }}
                                        </div>
                                    }.into_any();
                                }
                                view! {
                                    <div class="sched-run">
                                        <span class="meta" title=when_title>{when}</span>
                                        <span class=badge>{label}</span>
                                        <button type="button" class="linkish" disabled=empty on:click=move |_| open_session(sid_for_click.clone())>{sid_label}</button>
                                        <span class="meta">{detail}</span>
                                    </div>
                                }.into_any()
                            }).collect_view();
                            view! { <div>{rows}</div> }.into_any()
                        }}
                    </div>
                </form>
            </div>
            <SchedulerPresetsSection base=base token=token />
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn empty_timestamp_is_not_scheduled_in_any_zone() {
        assert_eq!(fmt_when("", "America/Vancouver"), "not scheduled");
        assert_eq!(fmt_when("", ""), "not scheduled");
        assert_eq!(
            fmt_when_with_browser("", "Bogus/Zone", "America/Vancouver"),
            "not scheduled"
        );
        assert_eq!(fmt_when_title(""), "");
    }

    #[test]
    fn utc_when_keeps_the_old_rendering() {
        assert_eq!(utc_when("2026-10-28T16:00:00Z"), "2026-10-28 16:00:00 UTC");
        assert_eq!(
            fmt_when_title("2026-10-28T16:00:00Z"),
            "2026-10-28 16:00:00 UTC"
        );
    }

    #[test]
    fn pick_zone_prefers_schedule_then_browser_then_utc() {
        assert_eq!(
            pick_zone("America/Vancouver", "Europe/London"),
            "America/Vancouver"
        );
        assert_eq!(pick_zone("", "Europe/London"), "Europe/London");
        assert_eq!(pick_zone("  ", "Europe/London"), "Europe/London");
        assert_eq!(pick_zone("", ""), "UTC");
        assert_eq!(pick_zone(" America/Vancouver ", "UTC"), "America/Vancouver");
    }

    #[test]
    fn unparseable_timestamp_keeps_the_old_rendering() {
        // A garbage instant is invalid in every zone, so both the
        // schedule-zone and browser-zone attempts fail and the raw
        // server string (labelled UTC) is what renders — never blank.
        assert_eq!(
            fmt_when_with_browser("not-a-date", "America/Vancouver", "Europe/London"),
            utc_when("not-a-date")
        );
        assert_eq!(
            fmt_when_with_browser("not-a-date", "Bogus/Zone", "Also/Bogus"),
            utc_when("not-a-date")
        );
    }

    #[test]
    fn duplicate_name_appends_copy_once() {
        assert_eq!(duplicate_schedule_name("Morning"), "Morning (copy)");
        assert_eq!(duplicate_schedule_name("Morning (copy)"), "Morning (copy)");
        assert_eq!(duplicate_schedule_name(""), "(copy)");
        assert_eq!(duplicate_schedule_name("  Standup  "), "Standup (copy)");
    }

    #[test]
    fn script_statuses_use_the_failure_styling() {
        for (status, label) in [("failed", "Failed"), ("denied", "Denied"), ("error", "Error")] {
            assert_eq!(status_label(status), label);
            assert_eq!(status_badge(status), "badge badge-failed");
        }
        assert_eq!(status_badge("finished"), "badge badge-done");
    }

    #[test]
    fn resolved_mode_names_plan_when_unset() {
        assert_eq!(
            resolved_mode_label(""),
            "Plan (a workspace permission mode, when set, wins)"
        );
        assert_eq!(
            resolved_mode_label("accept-edits"),
            "Accept edits (a workspace permission mode, when set, wins)"
        );
        assert_eq!(
            resolved_mode_label("yolo"),
            "Yolo (a workspace permission mode, when set, wins)"
        );
    }

    #[test]
    fn sandbox_dial_label_states_host_runs() {
        assert_eq!(sandbox_dial_label("off"), "Off: runs on the host");
        assert_eq!(sandbox_dial_label("workspace-write"), "Workspace write");
        assert_eq!(sandbox_dial_label("read-only"), "Read-only");
        assert_eq!(sandbox_dial_label(""), "Unknown (sandbox status unavailable)");
    }

    #[test]
    fn secret_refs_split_on_lines_and_commas() {
        assert_eq!(
            parse_secret_refs("secret://env/A\n  secret://env/B , secret://env/C\n\n"),
            vec!["secret://env/A", "secret://env/B", "secret://env/C"]
        );
        assert!(parse_secret_refs("  \n , ").is_empty());
    }

    #[test]
    fn exit_label_dashes_when_no_process_started() {
        assert_eq!(exit_label(Some(0)), "exit 0");
        assert_eq!(exit_label(Some(-1)), "exit -1");
        assert_eq!(exit_label(None), "—");
    }

    #[test]
    fn script_fields_deserialize_from_the_daemon_dto() {
        let item: ScheduleItem = serde_json::from_value(json!({
            "id": "sch_1", "name": "nightly", "enabled": true, "kind": "cron",
            "source": "script", "overlap": "skip", "missed": "skip",
            "command": "make test", "dir": "/srv/app", "timeout": "10m",
            "secretRefs": ["secret://env/TOKEN"]
        }))
        .expect("script schedule");
        assert_eq!(item.source, SOURCE_SCRIPT);
        assert_eq!(item.command, "make test");
        assert_eq!(item.dir, "/srv/app");
        assert_eq!(item.timeout, "10m");
        assert_eq!(item.secret_refs, vec!["secret://env/TOKEN".to_string()]);

        let run: RunItem = serde_json::from_value(json!({
            "id": "run_1", "status": "failed", "exitCode": 2,
            "outputPreview": "FAIL", "outputRef": "run_1", "decision": "armed"
        }))
        .expect("script run");
        assert_eq!(run.exit_code, Some(2));
        assert_eq!(run.output_preview, "FAIL");
        assert_eq!(run.output_ref, "run_1");
        assert_eq!(run.decision, "armed");
        assert!(run.session_id.is_empty());

        // A prompt schedule from an older daemon has none of these fields.
        let old: RunItem = serde_json::from_value(json!({"id": "run_2", "status": "finished", "sessionId": "s1"}))
            .expect("session run");
        assert_eq!(old.exit_code, None);
        assert!(old.output_ref.is_empty());
    }

    #[test]
    fn history_cap_notice_only_at_the_daemon_cap() {
        // #1491: below the cap the panel is complete — no label, no count.
        assert_eq!(history_cap_notice(0), None);
        assert_eq!(history_cap_notice(1), None);
        assert_eq!(history_cap_notice(49), None);
        // At the cap the label states the limit and the consequence,
        // without inventing a total of dropped runs.
        let label = history_cap_notice(50).expect("label at the cap");
        assert_eq!(
            label,
            "Showing the newest 50 runs — older runs are not kept."
        );
        // Past the cap cannot happen (the daemon truncates to 50); if a
        // future daemon raises it, the row-count check stays silent
        // rather than mislabelling a complete list.
        assert_eq!(history_cap_notice(51), None);
    }
}
