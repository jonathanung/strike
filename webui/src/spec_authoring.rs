//! Spec authoring (#1348): requirements (EARS) → design → tasks with phase
//! gates, producing the plan the Task runner runs.
//!
//! Everything here is a view over the daemon's spec store (`/v1/specs`,
//! files under the workspace's `.strike/specs/<name>/`). Phase gates,
//! EARS lint and requirement coverage are computed by the daemon; this
//! page renders them and surfaces refusals verbatim. Approving a phase is
//! only ever a button press (keyboard-reachable), never a side effect.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::{json, Value};
use std::collections::HashMap;

use crate::api::{get_json, js_encode, post_json_value, put_json_value};

pub const PHASES: [&str; 3] = ["requirements", "design", "tasks"];

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Requirement {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub story: String,
    #[serde(default)]
    pub criteria: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Task {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub body: String,
    #[serde(default)]
    pub requirements: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Coverage {
    #[serde(default)]
    pub covered: usize,
    #[serde(default)]
    pub total: usize,
    #[serde(default)]
    pub uncovered: Vec<String>,
    #[serde(default, rename = "unknownRefs")]
    pub unknown_refs: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct SpecView {
    pub name: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub requirements: Vec<Requirement>,
    #[serde(default)]
    pub design: String,
    #[serde(default)]
    pub tasks: Vec<Task>,
    #[serde(default)]
    pub status: HashMap<String, String>,
    #[serde(default, rename = "planId")]
    pub plan_id: String,
    #[serde(default)]
    pub version: i64,
    #[serde(default)]
    pub coverage: Coverage,
    #[serde(default)]
    pub lint: HashMap<String, Vec<String>>,
    #[serde(default)]
    pub path: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct SpecSummary {
    pub name: String,
    #[serde(default)]
    pub title: String,
    #[serde(default, rename = "planId")]
    pub plan_id: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct SpecList {
    #[serde(default)]
    specs: Vec<SpecSummary>,
}

pub fn phase_title(phase: &str) -> &'static str {
    match phase {
        "requirements" => "Requirements",
        "design" => "Design",
        "tasks" => "Tasks",
        _ => "Phase",
    }
}

pub fn status_label(status: &str) -> &'static str {
    match status {
        "approved" => "Approved",
        "review" => "In review",
        _ => "Draft",
    }
}

/// "R1, R2 ,R1" → ["R1", "R2"].
pub fn parse_refs(raw: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for part in raw.split(|c: char| c == ',' || c.is_whitespace()) {
        let p = part.trim();
        if !p.is_empty() && !out.iter().any(|x| x == p) {
            out.push(p.to_string());
        }
    }
    out
}

/// One EARS criterion per non-empty line.
pub fn criteria_lines(raw: &str) -> Vec<String> {
    raw.lines()
        .map(str::trim)
        .filter(|l| !l.is_empty())
        .map(str::to_string)
        .collect()
}

/// "7/9 requirements covered by tasks".
pub fn coverage_line(c: &Coverage) -> String {
    format!("{}/{} requirements covered by tasks", c.covered, c.total)
}

/// Lint warnings for one requirement (its own and its criteria's), in order.
pub fn lint_for(lint: &HashMap<String, Vec<String>>, id: &str) -> Vec<String> {
    let mut keys: Vec<&String> = lint
        .keys()
        .filter(|k| k.as_str() == id || k.starts_with(&format!("{id}#")))
        .collect();
    keys.sort();
    keys.into_iter()
        .flat_map(|k| {
            let label = match k.split_once('#') {
                Some((_, n)) => format!("criterion {}", n.parse::<usize>().map(|i| i + 1).unwrap_or(0)),
                None => "requirement".to_string(),
            };
            lint[k].iter().map(move |w| format!("{label}: {w}"))
        })
        .collect()
}

/// `HTTP 422 {"error":"spec: phase gate: approve requirements first"}` → reason.
pub fn spec_error(raw: &str) -> String {
    let body = raw
        .strip_prefix("HTTP ")
        .and_then(|rest| rest.split_once(' ').map(|(_, b)| b))
        .unwrap_or(raw);
    serde_json::from_str::<Value>(body.trim())
        .ok()
        .and_then(|v| v.get("error").and_then(|e| e.as_str()).map(str::to_string))
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| raw.to_string())
}

#[derive(Clone, Copy)]
struct ReqRow {
    key: u64,
    id: RwSignal<String>,
    story: RwSignal<String>,
    criteria: RwSignal<String>,
}

#[derive(Clone, Copy)]
struct TaskRow {
    key: u64,
    id: RwSignal<String>,
    title: RwSignal<String>,
    body: RwSignal<String>,
    refs: RwSignal<String>,
}

#[component]
pub fn SpecAuthoringPage(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    active_root: RwSignal<String>,
    mode: RwSignal<String>,
) -> impl IntoView {
    let specs = RwSignal::new(Vec::<SpecSummary>::new());
    let current = RwSignal::new(None::<SpecView>);
    let tab = RwSignal::new("requirements".to_string());
    let notice = RwSignal::new(String::new());
    let new_name = RwSignal::new(String::new());
    let new_title = RwSignal::new(String::new());
    let reqs = RwSignal::new(Vec::<ReqRow>::new());
    let tasks = RwSignal::new(Vec::<TaskRow>::new());
    let design = RwSignal::new(String::new());
    let next_key = RwSignal::new(1u64);

    let key = move || {
        let k = next_key.get_untracked();
        next_key.set(k + 1);
        k
    };

    // Load a daemon spec view into the page and its editors.
    let fill = move |v: SpecView| {
        reqs.set(
            v.requirements
                .iter()
                .map(|r| ReqRow {
                    key: key(),
                    id: RwSignal::new(r.id.clone()),
                    story: RwSignal::new(r.story.clone()),
                    criteria: RwSignal::new(r.criteria.join("\n")),
                })
                .collect(),
        );
        tasks.set(
            v.tasks
                .iter()
                .map(|t| TaskRow {
                    key: key(),
                    id: RwSignal::new(t.id.clone()),
                    title: RwSignal::new(t.title.clone()),
                    body: RwSignal::new(t.body.clone()),
                    refs: RwSignal::new(t.requirements.join(", ")),
                })
                .collect(),
        );
        design.set(v.design.clone());
        current.set(Some(v));
    };

    let load_list = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match get_json::<SpecList>(&b, &t, "/v1/specs").await {
                Ok(list) => specs.set(list.specs),
                Err(e) => notice.set(format!("Could not load specs: {}", spec_error(&e))),
            }
        });
    };
    load_list();

    let open = move |name: String| {
        notice.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/specs/{}", js_encode(&name));
            match get_json::<SpecView>(&b, &t, &path).await {
                Ok(v) => fill(v),
                Err(e) => notice.set(format!("Could not open {name}: {}", spec_error(&e))),
            }
        });
    };

    // Apply a daemon response (or its refusal) from any spec mutation.
    let landed = move |res: Result<Value, String>, ok_note: &'static str| match res {
        Ok(v) => match serde_json::from_value::<SpecView>(v) {
            Ok(view) => {
                fill(view);
                notice.set(ok_note.into());
                load_list();
            }
            Err(e) => notice.set(format!("Unreadable spec: {e}")),
        },
        Err(e) => notice.set(spec_error(&e)),
    };

    let create = move || {
        let name = new_name.get_untracked().trim().to_string();
        if name.is_empty() {
            notice.set("Name the spec (lowercase letters, digits and dashes).".into());
            return;
        }
        let body = json!({ "name": name, "title": new_title.get_untracked() });
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let res = post_json_value(&b, &t, "/v1/specs", body).await;
            if res.is_ok() {
                new_name.set(String::new());
                new_title.set(String::new());
                tab.set("requirements".into());
            }
            landed(res, "Spec created.");
        });
    };

    let save_phase = move |phase: &'static str| {
        let Some(v) = current.get_untracked() else { return };
        let mut body = json!({ "expectedVersion": v.version });
        match phase {
            "requirements" => {
                body["requirements"] = json!(reqs
                    .get_untracked()
                    .iter()
                    .map(|r| json!({
                        "id": r.id.get_untracked(),
                        "story": r.story.get_untracked(),
                        "criteria": criteria_lines(&r.criteria.get_untracked()),
                    }))
                    .collect::<Vec<_>>());
            }
            "design" => body["design"] = json!(design.get_untracked()),
            _ => {
                body["tasks"] = json!(tasks
                    .get_untracked()
                    .iter()
                    .map(|t| json!({
                        "id": t.id.get_untracked(),
                        "title": t.title.get_untracked(),
                        "body": t.body.get_untracked(),
                        "requirements": parse_refs(&t.refs.get_untracked()),
                    }))
                    .collect::<Vec<_>>());
            }
        }
        let path = format!("/v1/specs/{}/{phase}", js_encode(&v.name));
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            landed(put_json_value(&b, &t, &path, body).await, "Saved. Editing a phase returns it and later phases to draft.");
        });
    };

    let set_status = move |phase: &'static str, next: &'static str| {
        let Some(v) = current.get_untracked() else { return };
        let path = format!("/v1/specs/{}/phases/{phase}", js_encode(&v.name));
        let body = json!({ "status": next, "expectedVersion": v.version });
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            landed(post_json_value(&b, &t, &path, body).await, "Phase updated.");
        });
    };

    let hand_off = move || {
        let Some(v) = current.get_untracked() else { return };
        let owner = active_root.get_untracked();
        if owner.is_empty() {
            notice.set("Open a session first: the plan is owned by that session.".into());
            return;
        }
        let path = format!("/v1/specs/{}/handoff", js_encode(&v.name));
        let body = json!({ "ownerRoot": owner, "expectedVersion": v.version });
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let res = post_json_value(&b, &t, &path, body).await;
            let ok = res.is_ok();
            landed(res, "Handed off. The plan is open in the Task runner; this spec is now read-only.");
            if ok {
                status.set("spec handed off to the task runner".into());
            }
        });
    };

    let read_only = move || current.with(|c| c.as_ref().map(|v| !v.plan_id.is_empty()).unwrap_or(false));
    let phase_status = move |phase: &str| {
        current.with(|c| {
            c.as_ref()
                .and_then(|v| v.status.get(phase).cloned())
                .unwrap_or_else(|| "draft".into())
        })
    };

    let add_req = move || {
        reqs.update(|list| {
            list.push(ReqRow {
                key: key(),
                id: RwSignal::new(String::new()),
                story: RwSignal::new(String::new()),
                criteria: RwSignal::new(String::new()),
            })
        })
    };
    let add_task = move || {
        tasks.update(|list| {
            list.push(TaskRow {
                key: key(),
                id: RwSignal::new(String::new()),
                title: RwSignal::new(String::new()),
                body: RwSignal::new(String::new()),
                refs: RwSignal::new(String::new()),
            })
        })
    };

    view! {
        <main class="spec-authoring" data-spec-root="true">
            <section class="spec-list" aria-label="Specs">
                <div class="section-head">
                    <h2 class="panel-title">"Specs"</h2>
                </div>
                <ul class="plain">
                    {move || specs.get().into_iter().map(|s| {
                        let name = s.name.clone();
                        let row_id = s.name.clone();
                        let label = if s.title.is_empty() { s.name.clone() } else { s.title.clone() };
                        let meta = if s.plan_id.is_empty() { s.name.clone() } else { format!("{} · handed off", s.name) };
                        view! {
                            <li>
                                <button type="button" class="row card" data-spec-row=row_id on:click=move |_| open(name.clone())>
                                    <span class="name">{label}</span>
                                    <span class="path">{meta}</span>
                                </button>
                            </li>
                        }
                    }).collect_view()}
                </ul>
                <form class="spec-create" on:submit=move |ev: web_sys::SubmitEvent| { ev.prevent_default(); create(); }>
                    <label>"New spec name"<input id="spec-new-name" placeholder="data-export" prop:value=move || new_name.get() on:input=move |ev| new_name.set(event_target_value(&ev)) /></label>
                    <label>"Title"<input id="spec-new-title" prop:value=move || new_title.get() on:input=move |ev| new_title.set(event_target_value(&ev)) /></label>
                    <button type="submit" class="secondary" data-action="spec-create">"Create spec"</button>
                </form>
            </section>
            <section class="spec-main" aria-label="Spec">
                <Show when=move || !notice.get().is_empty()>
                    <p class="notice" role="status" data-spec-notice="true">{move || notice.get()}</p>
                </Show>
                <Show when=move || current.get().is_some() fallback=|| view! { <p class="hint">"Open or create a spec. Phases: requirements in EARS, then design, then tasks that cite requirements. Approve each phase to unlock the next."</p> }>
                    <div class="section-head">
                        <h2 class="panel-title">{move || current.with(|c| c.as_ref().map(|v| v.title.clone()).unwrap_or_default())}</h2>
                        <span class="hint">{move || current.with(|c| c.as_ref().map(|v| v.path.clone()).unwrap_or_default())}</span>
                    </div>
                    <Show when=read_only>
                        <p class="hint warn" data-spec-readonly="true">{move || format!("Handed off as plan {}. This spec is read-only history; start a new spec to change direction.", current.with(|c| c.as_ref().map(|v| v.plan_id.clone()).unwrap_or_default()))}</p>
                    </Show>
                    <div class="spec-tabs" role="tablist" aria-label="Spec phases">
                        {PHASES.into_iter().map(|phase: &'static str| {
                            view! {
                                <button type="button" role="tab" data-spec-tab=phase
                                    aria-selected=move || (tab.get() == phase).to_string()
                                    class=move || if tab.get() == phase { "spec-tab on" } else { "spec-tab" }
                                    on:click=move |_| tab.set(phase.into())>
                                    {phase_title(phase)}
                                    <span class=move || format!("spec-status {}", phase_status(phase)) data-spec-status=phase>{move || status_label(&phase_status(phase))}</span>
                                </button>
                            }
                        }).collect_view()}
                    </div>
                    <div class="spec-phase-actions task-actions">
                        <button type="button" class="secondary" data-action="spec-save" disabled=read_only on:click=move |_| {
                            let p: &'static str = match tab.get_untracked().as_str() { "design" => "design", "tasks" => "tasks", _ => "requirements" };
                            save_phase(p);
                        }>"Save phase"</button>
                        <button type="button" class="secondary" data-action="spec-review" disabled=read_only on:click=move |_| {
                            let p: &'static str = match tab.get_untracked().as_str() { "design" => "design", "tasks" => "tasks", _ => "requirements" };
                            set_status(p, "review");
                        }>"Send for review"</button>
                        <button type="button" class="primary" data-action="spec-approve" disabled=read_only on:click=move |_| {
                            let p: &'static str = match tab.get_untracked().as_str() { "design" => "design", "tasks" => "tasks", _ => "requirements" };
                            set_status(p, "approved");
                        }>"Approve phase"</button>
                        <button type="button" class="secondary" data-action="spec-draft" disabled=read_only on:click=move |_| {
                            let p: &'static str = match tab.get_untracked().as_str() { "design" => "design", "tasks" => "tasks", _ => "requirements" };
                            set_status(p, "draft");
                        }>"Back to draft"</button>
                    </div>
                    <Show when=move || tab.get() == "requirements">
                        <p class="hint">"One user story per requirement; one EARS criterion per line, e.g. WHEN ‹condition› THE SYSTEM SHALL ‹behaviour›. EARS warnings never block saving."</p>
                        <ol class="plain spec-reqs">
                            <For each=move || reqs.get() key=|r| r.key children=move |r: ReqRow| {
                                let warnings = move || current.with(|c| c.as_ref().map(|v| lint_for(&v.lint, &r.id.get())).unwrap_or_default());
                                view! {
                                    <li class="spec-req" data-spec-req=move || r.id.get()>
                                        <p class="spec-id">{move || { let id = r.id.get(); if id.is_empty() { "new (id on save)".to_string() } else { id } }}</p>
                                        <label>"User story"<input class="spec-story" prop:disabled=read_only prop:value=move || r.story.get() on:input=move |ev| r.story.set(event_target_value(&ev)) /></label>
                                        <label>"Acceptance criteria (EARS, one per line)"<textarea class="spec-criteria" rows="3" prop:disabled=read_only prop:value=move || r.criteria.get() on:input=move |ev| r.criteria.set(event_target_value(&ev))></textarea></label>
                                        <Show when=move || !warnings().is_empty()>
                                            <ul class="plain spec-lint" data-spec-lint="true">
                                                {move || warnings().into_iter().map(|w| view! { <li>{w}</li> }).collect_view()}
                                            </ul>
                                        </Show>
                                    </li>
                                }
                            } />
                        </ol>
                        <button type="button" class="secondary" data-action="spec-add-req" disabled=read_only on:click=move |_| add_req()>"Add requirement"</button>
                    </Show>
                    <Show when=move || tab.get() == "design">
                        <p class="hint">"Architecture, interfaces, error handling. Saved as design.md in the spec folder."</p>
                        <textarea id="spec-design" class="spec-design" rows="16" prop:disabled=read_only prop:value=move || design.get() on:input=move |ev| design.set(event_target_value(&ev))></textarea>
                    </Show>
                    <Show when=move || tab.get() == "tasks">
                        <p class="spec-coverage" data-spec-coverage="true">{move || current.with(|c| c.as_ref().map(|v| coverage_line(&v.coverage)).unwrap_or_default())}</p>
                        <Show when=move || current.with(|c| c.as_ref().map(|v| !v.coverage.uncovered.is_empty()).unwrap_or(false))>
                            <p class="hint warn" data-spec-uncovered="true">{move || format!("Not covered by any task: {}", current.with(|c| c.as_ref().map(|v| v.coverage.uncovered.join(", ")).unwrap_or_default()))}</p>
                        </Show>
                        <Show when=move || current.with(|c| c.as_ref().map(|v| !v.coverage.unknown_refs.is_empty()).unwrap_or(false))>
                            <p class="hint warn">{move || format!("Tasks cite unknown requirements: {}", current.with(|c| c.as_ref().map(|v| v.coverage.unknown_refs.join(", ")).unwrap_or_default()))}</p>
                        </Show>
                        <ol class="plain spec-tasks">
                            <For each=move || tasks.get() key=|t| t.key children=move |t: TaskRow| {
                                view! {
                                    <li class="spec-task">
                                        <p class="spec-id">{move || { let id = t.id.get(); if id.is_empty() { "new (id on save)".to_string() } else { id } }}</p>
                                        <label>"Task"<input class="spec-task-title" prop:disabled=read_only prop:value=move || t.title.get() on:input=move |ev| t.title.set(event_target_value(&ev)) /></label>
                                        <label>"Details"<textarea class="spec-task-body" rows="2" prop:disabled=read_only prop:value=move || t.body.get() on:input=move |ev| t.body.set(event_target_value(&ev))></textarea></label>
                                        <label>"Implements (requirement ids)"<input class="spec-task-refs" placeholder="R1, R2" prop:disabled=read_only prop:value=move || t.refs.get() on:input=move |ev| t.refs.set(event_target_value(&ev)) /></label>
                                    </li>
                                }
                            } />
                        </ol>
                        <div class="task-actions">
                            <button type="button" class="secondary" data-action="spec-add-task" disabled=read_only on:click=move |_| add_task()>"Add task"</button>
                            <button type="button" class="primary" data-action="spec-handoff" disabled=read_only on:click=move |_| hand_off()>"Send to Task runner"</button>
                            <Show when=read_only>
                                <button type="button" class="secondary" data-action="spec-open-tasks" on:click=move |_| mode.set("tasks".into())>"Open Task runner"</button>
                            </Show>
                        </div>
                    </Show>
                </Show>
            </section>
        </main>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_refs_splits_and_dedupes() {
        assert_eq!(parse_refs("R1, R2 ,R1"), vec!["R1", "R2"]);
        assert_eq!(parse_refs("R3 R4"), vec!["R3", "R4"]);
        assert!(parse_refs(" , ").is_empty());
    }

    #[test]
    fn criteria_lines_drop_blanks() {
        assert_eq!(criteria_lines("a\n\n  b  \n"), vec!["a", "b"]);
    }

    #[test]
    fn coverage_line_reads_n_of_m() {
        let c = Coverage { covered: 7, total: 9, ..Default::default() };
        assert_eq!(coverage_line(&c), "7/9 requirements covered by tasks");
    }

    #[test]
    fn lint_for_collects_requirement_and_criteria_warnings() {
        let mut lint = HashMap::new();
        lint.insert("R2#0".to_string(), vec!["no SHALL".to_string()]);
        lint.insert("R2".to_string(), vec!["no acceptance criteria".to_string()]);
        lint.insert("R20#0".to_string(), vec!["other".to_string()]);
        let got = lint_for(&lint, "R2");
        assert_eq!(got, vec!["requirement: no acceptance criteria".to_string(), "criterion 1: no SHALL".to_string()]);
    }

    #[test]
    fn spec_error_extracts_reason() {
        assert_eq!(spec_error(r#"HTTP 422 {"error":"spec: phase gate: approve requirements first"}"#), "spec: phase gate: approve requirements first");
        assert_eq!(spec_error("offline"), "offline");
    }

    #[test]
    fn view_parses_daemon_shape() {
        let v: SpecView = serde_json::from_str(r#"{"name":"x","title":"X","requirements":[{"id":"R1","story":"s","criteria":["c"]}],"design":"","tasks":[],"status":{"requirements":"approved"},"version":3,"coverage":{"covered":0,"total":1,"uncovered":["R1"],"unknownRefs":[]},"lint":{},"path":"/w/.strike/specs/x"}"#).unwrap();
        assert_eq!(v.status.get("requirements").map(String::as_str), Some("approved"));
        assert_eq!(v.coverage.uncovered, vec!["R1"]);
        assert_eq!(v.version, 3);
    }
}
