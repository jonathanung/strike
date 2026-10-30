//! Task runner board. A plan is a checklist. Run spawns one child per ready
//! section through the daemon (`POST /v1/plans/{id}/run`). The checkpoint is
//! that child's handoff and verification report.
//!
//! Status pills use the cockpit badge classes, as authored in #1318. The
//! shared status component from #1263 has since landed on main; swapping
//! `status_class` for it is a follow-on, not part of this re-land.

use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use wasm_bindgen::JsCast;

use load_guard::LoadEpoch;

use crate::load_epoch::begin;

use crate::api::{get_json, post_json_value};

#[derive(Clone, Deserialize)]
struct PlanMeta {
    #[serde(rename = "ID", alias = "id")]
    id: String,
    #[serde(rename = "Title", alias = "title")]
    title: String,
    #[serde(rename = "Status", alias = "status")]
    status: String,
    #[serde(rename = "OwnerRoot", alias = "ownerRoot", default)]
    owner_root: String,
}

#[derive(Clone, Deserialize)]
struct PlanList {
    plans: Vec<PlanMeta>,
}

#[derive(Clone, Deserialize)]
struct TaskCheck {
    #[serde(default)]
    name: String,
    #[serde(default)]
    kind: String,
    passed: bool,
    #[serde(default)]
    error: String,
}

#[derive(Clone, Deserialize)]
struct Checkpoint {
    #[serde(default, rename = "childStatus")]
    child_status: String,
    #[serde(default)]
    summary: String,
    #[serde(default, rename = "filesChanged")]
    files_changed: Vec<String>,
    #[serde(default, rename = "handoffText")]
    handoff_text: String,
    #[serde(default)]
    findings: Vec<String>,
    #[serde(default)]
    blockers: Vec<String>,
    #[serde(default)]
    incomplete: bool,
    #[serde(default, rename = "hasVerify")]
    has_verify: bool,
    #[serde(default, rename = "verifyPassed")]
    verify_passed: bool,
    #[serde(default, rename = "verifySummary")]
    verify_summary: String,
    #[serde(default)]
    checks: Vec<TaskCheck>,
}

#[derive(Clone, Deserialize)]
struct TaskSection {
    id: String,
    title: String,
    #[serde(default)]
    body: String,
    #[serde(default, rename = "dependsOn")]
    depends_on: Vec<String>,
    status: String,
    #[serde(default, rename = "childId")]
    child_id: String,
    #[serde(default)]
    detail: String,
    #[serde(default)]
    checkpoint: Option<Checkpoint>,
}

#[derive(Clone, Deserialize)]
struct TaskRun {
    #[serde(rename = "planId")]
    plan_id: String,
    #[serde(rename = "ownerRoot")]
    owner_root: String,
    #[serde(rename = "planTitle")]
    plan_title: String,
    #[serde(rename = "planStatus")]
    plan_status: String,
    version: i64,
    sections: Vec<TaskSection>,
}

#[derive(Clone, Deserialize)]
struct RunBody {
    run: TaskRun,
    #[serde(default)]
    note: String,
}

#[derive(Clone)]
struct DraftSection {
    title: String,
    body: String,
}

fn status_label(status: &str) -> &'static str {
    match status {
        "running" => "Running",
        "checkpoint" => "Review",
        "approved" => "Approved",
        "failed" => "Failed",
        "skipped" => "Skipped",
        "blocked" => "Blocked",
        _ => "Pending",
    }
}

/// Badge classes already in the cockpit. #1263 owns a shared status
/// component (now on main); this mapping is the authored stand-in.
fn status_class(status: &str) -> &'static str {
    match status {
        "running" => "badge badge-running",
        "checkpoint" => "badge badge-tool",
        "approved" => "badge badge-completed",
        "failed" => "badge badge-failed",
        "skipped" => "badge badge-stopped",
        "blocked" => "badge badge-blocked",
        _ => "badge",
    }
}

fn typing_target(ev: &web_sys::KeyboardEvent) -> bool {
    let Some(target) = ev.target() else {
        return false;
    };
    let Ok(el) = target.dyn_into::<web_sys::Element>() else {
        return false;
    };
    matches!(el.tag_name().as_str(), "INPUT" | "TEXTAREA" | "SELECT")
}

async fn fetch_plans(base: &str, token: &str) -> Result<Vec<PlanMeta>, String> {
    let list = get_json::<PlanList>(base, token, "/v1/plans").await?;
    Ok(list.plans)
}

async fn post_run(
    base: &str,
    token: &str,
    id: &str,
    body: serde_json::Value,
) -> Result<RunBody, String> {
    let path = format!("/v1/plans/{}/run", crate::api::js_encode(id));
    let value = post_json_value(base, token, &path, body).await?;
    serde_json::from_value(value).map_err(|e| e.to_string())
}

async fn load_run(base: &str, token: &str, plan: &PlanMeta) -> Result<RunBody, String> {
    let path = format!("/v1/plans/{}/run", crate::api::js_encode(&plan.id));
    match get_json::<RunBody>(base, token, &path).await {
        Ok(body) => Ok(body),
        Err(err) if err.contains("404") => {
            post_run(
                base,
                token,
                &plan.id,
                serde_json::json!({
                    "action": "open",
                    "ownerRoot": plan.owner_root,
                }),
            )
            .await
        }
        Err(err) => Err(err),
    }
}

#[component]
pub fn TaskRunner(
    base: RwSignal<String>,
    token: RwSignal<String>,
    active_root: RwSignal<String>,
    status: RwSignal<String>,
) -> impl IntoView {
    let plans = RwSignal::new(Vec::<PlanMeta>::new());
    let run = RwSignal::new(None::<TaskRun>);
    let selected = RwSignal::new(String::new());
    let row = RwSignal::new(String::new());
    let note = RwSignal::new(String::new());
    let show_new = RwSignal::new(false);
    let draft_title = RwSignal::new(String::new());
    let draft_section = RwSignal::new(String::new());
    let draft_body = RwSignal::new(String::new());
    let drafts = RwSignal::new(Vec::<DraftSection>::new());
    // Set before refresh so a plan created in this view is selected after the
    // list round-trip. Setting `selected` first races the list and never opens.
    let pending_select = RwSignal::new(String::new());
    let run_epoch = RwSignal::new(LoadEpoch::new());
    let run_loading = RwSignal::new(false);

    let refresh_plans = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match fetch_plans(&b, &t).await {
                Ok(list) => {
                    let want = pending_select.get_untracked();
                    pending_select.set(String::new());
                    let pick = if !want.is_empty() && list.iter().any(|p| p.id == want) {
                        want
                    } else if selected.get_untracked().is_empty() {
                        list.first().map(|p| p.id.clone()).unwrap_or_default()
                    } else {
                        String::new()
                    };
                    plans.set(list);
                    if !pick.is_empty() {
                        selected.set(pick);
                    }
                }
                Err(err) => note.set(err),
            }
        });
    };
    crate::shell::bind(
        "tasks.refresh",
        Rc::new(move |_| {
            refresh_plans();
            Ok(())
        }),
    );
    if let Some(_) = crate::park::take("tasks.refresh") {
        refresh_plans();
    }

    let open_selected = move || {
        let gen = begin(run_epoch);
        run.set(None);
        note.set(String::new());
        let id = selected.get_untracked();
        if id.is_empty() {
            run_loading.set(false);
            return;
        }
        let plan = plans.get_untracked().into_iter().find(|p| p.id == id);
        let Some(plan) = plan else {
            run_loading.set(false);
            note.set("That plan is not in the list.".into());
            return;
        };
        run_loading.set(true);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let run_epoch = run_epoch;
        spawn_local(async move {
            let fetched = load_run(&b, &t, &plan).await;
            if !run_epoch.with_untracked(|epoch| epoch.accept(gen)) {
                return;
            }
            if selected.get_untracked() != id {
                return;
            }
            run_loading.set(false);
            match fetched {
                Ok(body) => {
                    if row.get_untracked().is_empty() {
                        if let Some(sec) = body.run.sections.first() {
                            row.set(sec.id.clone());
                        }
                    }
                    if !body.note.is_empty() {
                        note.set(body.note);
                    }
                    run.set(Some(body.run));
                }
                Err(err) => {
                    run.set(None);
                    note.set(err.clone());
                    status.set(err);
                }
            }
        });
    };

    Effect::new(move |_| {
        let _ = (base.get(), token.get());
        refresh_plans();
    });
    Effect::new(move |_| {
        let _ = selected.get();
        row.set(String::new());
        open_selected();
    });

    let act = move |action: &'static str, section_id: String, section_ids: Vec<String>| {
        let Some(current) = run.get_untracked() else {
            return;
        };
        if current.plan_id != selected.get_untracked() {
            note.set("That run belongs to a different plan.".into());
            return;
        }
        let gen = run_epoch.with_untracked(|epoch| epoch.token());
        let selected_id = current.plan_id.clone();
        let owner_root = current.owner_root.clone();
        let plan_title = current.plan_title.clone();
        let plan_status = current.plan_status.clone();
        let version = current.version;
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let run_epoch = run_epoch;
        spawn_local(async move {
            let body = serde_json::json!({
                "action": action,
                "ownerRoot": owner_root.clone(),
                "sectionId": section_id,
                "sectionIds": section_ids,
                "expectedVersion": version,
            });
            match post_run(&b, &t, &selected_id, body).await {
                Ok(body) => {
                    if !run_epoch.with_untracked(|epoch| epoch.accept(gen))
                        || selected.get_untracked() != selected_id
                    {
                        return;
                    }
                    note.set(body.note);
                    run.set(Some(body.run));
                    status.set(String::new());
                }
                Err(err) => {
                    if !run_epoch.with_untracked(|epoch| epoch.accept(gen))
                        || selected.get_untracked() != selected_id
                    {
                        return;
                    }
                    note.set(err.clone());
                    status.set(err);
                    if let Ok(again) = load_run(
                        &b,
                        &t,
                        &PlanMeta {
                            id: selected_id.clone(),
                            title: plan_title.clone(),
                            status: plan_status.clone(),
                            owner_root: owner_root.clone(),
                        },
                    )
                    .await
                    {
                        if !run_epoch.with_untracked(|epoch| epoch.accept(gen))
                            || selected.get_untracked() != selected_id
                        {
                            return;
                        }
                        run.set(Some(again.run));
                    }
                }
            }
        });
    };

    let create_plan = move || {
        let title = draft_title.get_untracked().trim().to_string();
        let mut sections = drafts.get_untracked();
        let pending_title = draft_section.get_untracked().trim().to_string();
        if !pending_title.is_empty() {
            sections.push(DraftSection {
                title: pending_title,
                body: draft_body.get_untracked(),
            });
        }
        let owner = active_root.get_untracked();
        if title.is_empty() || sections.is_empty() {
            note.set("A plan needs a title and at least one section.".into());
            return;
        }
        if owner.is_empty() {
            note.set("Open a session first. The plan is owned by that session.".into());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let payload_sections: Vec<_> = sections
            .iter()
            .map(|s| serde_json::json!({"title": s.title, "body": s.body}))
            .collect();
        spawn_local(async move {
            let value = match post_json_value(
                &b,
                &t,
                "/v1/plans",
                serde_json::json!({
                    "ownerRoot": owner,
                    "title": title,
                    "sections": payload_sections,
                }),
            )
            .await
            {
                Ok(v) => v,
                Err(err) => {
                    note.set(err);
                    return;
                }
            };
            let id = value
                .get("ID")
                .or_else(|| value.get("id"))
                .and_then(|v| v.as_str())
                .unwrap_or_default()
                .to_string();
            draft_title.set(String::new());
            draft_section.set(String::new());
            draft_body.set(String::new());
            drafts.set(Vec::new());
            show_new.set(false);
            if !id.is_empty() {
                pending_select.set(id);
            }
            refresh_plans();
        });
    };

    let add_draft = move || {
        let title = draft_section.get_untracked().trim().to_string();
        if title.is_empty() {
            return;
        }
        drafts.update(|list| {
            list.push(DraftSection {
                title,
                body: draft_body.get_untracked(),
            });
        });
        draft_section.set(String::new());
        draft_body.set(String::new());
    };

    let move_row = move |delta: isize| {
        let Some(current) = run.get_untracked() else {
            return;
        };
        let ids: Vec<String> = current.sections.iter().map(|s| s.id.clone()).collect();
        let Some(pos) = ids.iter().position(|id| id == &row.get_untracked()) else {
            return;
        };
        let next = pos as isize + delta;
        if next < 0 || next >= ids.len() as isize {
            return;
        }
        let mut order = ids;
        order.swap(pos, next as usize);
        let moved = order[next as usize].clone();
        row.set(moved);
        act("reorder", String::new(), order);
    };

    let on_key = move |ev: web_sys::KeyboardEvent| {
        if typing_target(&ev) || ev.meta_key() || ev.ctrl_key() || ev.alt_key() {
            return;
        }
        let Some(current) = run.get_untracked() else {
            return;
        };
        let ids: Vec<String> = current.sections.iter().map(|s| s.id.clone()).collect();
        if ids.is_empty() {
            return;
        }
        let pos = ids
            .iter()
            .position(|id| id == &row.get_untracked())
            .unwrap_or(0);
        let current_id = ids[pos].clone();
        match ev.key().as_str() {
            "j" | "ArrowDown" => {
                let n = (pos + 1).min(ids.len() - 1);
                row.set(ids[n].clone());
            }
            "k" | "ArrowUp" => {
                let n = pos.saturating_sub(1);
                row.set(ids[n].clone());
            }
            "r" => act("start", String::new(), Vec::new()),
            "a" => act("approve", current_id, Vec::new()),
            "s" => act("skip", current_id, Vec::new()),
            "t" => act("retry", current_id, Vec::new()),
            "[" => move_row(-1),
            "]" => move_row(1),
            _ => return,
        }
        ev.prevent_default();
    };

    arm_poll(base, token, plans, selected, run, run_epoch);

    view! {
        <div class="task-shell">
        <div class="task-runner" id="task-runner" tabindex="0" on:keydown=on_key>
            <aside class="task-col">
                <div class="section-head">
                    <h2 class="panel-title">"Task runner"</h2>
                </div>
                <div class="task-plan-list" role="listbox" aria-label="Plans">
                    {move || {
                        let list = plans.get();
                        let current = selected.get();
                        let empty = list.is_empty();
                        let buttons = list.into_iter().map(|plan| {
                            let id = plan.id.clone();
                            let class = if plan.id == current { "task-plan on" } else { "task-plan" };
                            view! {
                                <button class=class on:click=move |_| selected.set(id.clone())>
                                    <span class="name">{plan.title.clone()}</span>
                                    <span class="path">{plan.status.clone()}</span>
                                </button>
                            }
                        }).collect_view();
                        view! {
                            <div>
                                <Show when=move || empty>
                                    <p class="hint">"No plans yet. Create one, then run it section by section."</p>
                                </Show>
                                {buttons}
                            </div>
                        }
                    }}
                </div>
                <button class="secondary" on:click=move |_| show_new.update(|v| *v = !*v)>"New plan"</button>
                <Show when=move || show_new.get()>
                    <form class="task-create" on:submit=move |ev: web_sys::SubmitEvent| {
                        ev.prevent_default();
                        create_plan();
                    }>
                        <label>"Title"<input prop:value=move || draft_title.get() on:input=move |ev| draft_title.set(event_target_value(&ev)) /></label>
                        <label>"Section"<input prop:value=move || draft_section.get() on:input=move |ev| draft_section.set(event_target_value(&ev)) /></label>
                        <label>"Body"<textarea prop:value=move || draft_body.get() on:input=move |ev| draft_body.set(event_target_value(&ev))></textarea></label>
                        <div class="task-actions">
                            <button type="button" class="secondary" on:click=move |_| add_draft()>"Add section"</button>
                            <button type="submit" class="primary">"Create"</button>
                        </div>
                        <ul class="task-drafts">
                            {move || drafts.get().into_iter().map(|d| view! { <li>{d.title}</li> }).collect_view()}
                        </ul>
                    </form>
                </Show>
            </aside>
            <section class="task-col task-board" aria-label="Checklist">
                {move || {
                    let current = run.get();
                    let Some(current) = current else {
                        if run_loading.get() {
                            return view! { <p class="hint">"Loading plan…"</p> }.into_any();
                        }
                        return view! { <p class="hint">"Open a plan to see its sections."</p> }.into_any();
                    };
                    let title = current.plan_title.clone();
                    let plan_status = current.plan_status.clone();
                    let picked = row.get();
                    let names: std::collections::HashMap<String, String> = current.sections.iter().map(|s| (s.id.clone(), s.title.clone())).collect();
                    let rows = current.sections.clone();
                    view! {
                        <div class="section-head">
                            <h2 class="panel-title">{title}</h2>
                            <span class="hint">{plan_status}</span>
                            <button class="primary" on:click=move |_| act("start", String::new(), Vec::new())>"Run"</button>
                        </div>
                        <ol class="task-list">
                            {rows.into_iter().map(|sec| {
                                let id = sec.id.clone();
                                let class = if sec.id == picked { "task-row on" } else { "task-row" };
                                let dep = sec.depends_on.first().and_then(|dep| names.get(dep)).cloned().unwrap_or_default();
                                let dep_shown = !dep.is_empty();
                                let dep_label = dep;
                                let badge = status_class(&sec.status);
                                let label = status_label(&sec.status);
                                let detail = sec.detail.clone();
                                let detail_shown = !detail.is_empty();
                                let title = sec.title.clone();
                                view! {
                                    <li>
                                        <button class=class on:click=move |_| row.set(id.clone())>
                                            <span class=badge>{label}</span>
                                            <span class="name">{title}</span>
                                            <Show when=move || dep_shown>
                                                <span class="path">"after "{dep_label.clone()}</span>
                                            </Show>
                                            <Show when=move || detail_shown>
                                                <span class="task-detail">{detail.clone()}</span>
                                            </Show>
                                        </button>
                                    </li>
                                }
                            }).collect_view()}
                        </ol>
                        <p class="hint keys">"j/k move  r run  a approve  s skip  t retry  [ ] reorder"</p>
                    }.into_any()
                }}
            </section>
            <aside class="task-col task-read" aria-label="Checkpoint">
                {move || checkpoint_view(run.get(), row.get(), act)}
            </aside>
        </div>
        <p class="task-note" aria-live="polite">{move || note.get()}</p>
        </div>
    }
}

fn checkpoint_view(
    run: Option<TaskRun>,
    row: String,
    act: impl Fn(&'static str, String, Vec<String>) + Copy + 'static,
) -> impl IntoView {
    let Some(run) = run else {
        return view! { <p class="hint">"The checkpoint is the section body, the child handoff, and the verification report."</p> }.into_any();
    };
    let Some(sec) = run
        .sections
        .iter()
        .find(|s| s.id == row)
        .cloned()
        .or_else(|| run.sections.first().cloned())
    else {
        return view! { <p class="hint">"This plan has no sections."</p> }.into_any();
    };
    let id = sec.id.clone();
    let status = sec.status.clone();
    let can_approve = status == "checkpoint";
    let can_retry = status == "failed" || status == "checkpoint";
    let can_skip = matches!(
        status.as_str(),
        "pending" | "checkpoint" | "failed" | "blocked"
    );
    let cp = sec.checkpoint.clone();
    let body = sec.body.clone();
    let title = sec.title.clone();
    let child = sec.child_id.clone();
    let child_shown = !child.is_empty();
    let approve_id = id.clone();
    let retry_id = id.clone();
    let skip_id = id.clone();
    view! {
        <div class="section-head">
            <h2 class="panel-title">{title}</h2>
            <span class=status_class(&status)>{status_label(&status)}</span>
        </div>
        <div class="task-actions">
            <button class="primary" disabled=move || !can_approve on:click=move |_| act("approve", approve_id.clone(), Vec::new())>"Approve"</button>
            <button class="secondary" disabled=move || !can_retry on:click=move |_| act("retry", retry_id.clone(), Vec::new())>"Retry"</button>
            <button class="secondary" disabled=move || !can_skip on:click=move |_| act("skip", skip_id.clone(), Vec::new())>"Skip"</button>
        </div>
        <h3>"Section"</h3>
        <pre class="task-check">{if body.is_empty() { "(no body)".into() } else { body }}</pre>
        {match cp {
            None => view! { <p class="hint">"Run spawns a child for a ready section. When it finishes, the handoff and verification report land here. A failed section blocks the ones that depend on it."</p> }.into_any(),
            Some(cp) => {
                let verify = if !cp.has_verify {
                    "No verification report".to_string()
                } else if cp.verify_passed {
                    "Verification passed".to_string()
                } else {
                    "Verification failed".to_string()
                };
                let verify_class = if cp.has_verify && cp.verify_passed { "badge badge-completed" } else if cp.has_verify { "badge badge-failed" } else { "badge" };
                let summary = cp.summary.clone();
                let summary_shown = !summary.is_empty();
                let handoff = cp.handoff_text.clone();
                let handoff_shown = !handoff.is_empty();
                let vsum = cp.verify_summary.clone();
                let vsum_shown = !vsum.is_empty();
                let files = cp.files_changed.clone();
                let files_shown = !files.is_empty();
                let findings = cp.findings.clone();
                let findings_shown = !findings.is_empty();
                let blockers = cp.blockers.clone();
                let blockers_shown = !blockers.is_empty();
                let checks = cp.checks.clone();
                let checks_shown = !checks.is_empty();
                let incomplete = cp.incomplete;
                let child_status = cp.child_status.clone();
                view! {
                    <h3>"Checkpoint"</h3>
                    <p>
                        <span class=verify_class>{verify}</span>
                        <span class="hint">" "{child_status}</span>
                    </p>
                    <Show when=move || incomplete>
                        <p class="hint">"schema:handoff did not accept the handoff. Dependants stay blocked until this section is retried or skipped."</p>
                    </Show>
                    <Show when=move || summary_shown>
                        <p>{summary.clone()}</p>
                    </Show>
                    <Show when=move || vsum_shown>
                        <p class="hint">{vsum.clone()}</p>
                    </Show>
                    <Show when=move || handoff_shown>
                        <h3>"Handoff note"</h3>
                        <pre class="task-check">{handoff.clone()}</pre>
                    </Show>
                    <Show when=move || files_shown>
                        <h3>"Files"</h3>
                        <ul>{files.clone().into_iter().map(|f| view! { <li>{f}</li> }).collect_view()}</ul>
                    </Show>
                    <Show when=move || findings_shown>
                        <h3>"Findings"</h3>
                        <ul>{findings.clone().into_iter().map(|f| view! { <li>{f}</li> }).collect_view()}</ul>
                    </Show>
                    <Show when=move || blockers_shown>
                        <h3>"Blockers"</h3>
                        <ul>{blockers.clone().into_iter().map(|f| view! { <li>{f}</li> }).collect_view()}</ul>
                    </Show>
                    <Show when=move || checks_shown>
                        <h3>"Gates"</h3>
                        <ul>
                            {checks.clone().into_iter().map(|c| {
                                let mark = if c.passed { "passed" } else { "failed" };
                                let name = if c.name.is_empty() { c.kind.clone() } else { c.name.clone() };
                                view! { <li>{name}" "{mark}" "{c.error}</li> }
                            }).collect_view()}
                        </ul>
                    </Show>
                }.into_any()
            }
        }}
        <Show when=move || child_shown>
            <p class="hint">"Child "{child.clone()}</p>
        </Show>
    }.into_any()
}

/// Polls the open run while a section is running so the checkpoint shows up
/// without a manual refresh. The closure stays alive until this view unmounts.
fn arm_poll(
    base: RwSignal<String>,
    token: RwSignal<String>,
    plans: RwSignal<Vec<PlanMeta>>,
    selected: RwSignal<String>,
    run: RwSignal<Option<TaskRun>>,
    run_epoch: RwSignal<LoadEpoch>,
) {
    let cb = wasm_bindgen::closure::Closure::wrap(Box::new(move || {
        let running = run
            .get_untracked()
            .map(|r| r.sections.iter().any(|s| s.status == "running"))
            .unwrap_or(false);
        if !running {
            return;
        }
        let id = selected.get_untracked();
        let Some(plan) = plans.get_untracked().into_iter().find(|p| p.id == id) else {
            return;
        };
        let gen = run_epoch.with_untracked(|epoch| epoch.token());
        let plan_id = plan.id.clone();
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Ok(body) = get_json::<RunBody>(
                &b,
                &t,
                &format!("/v1/plans/{}/run", crate::api::js_encode(&plan.id)),
            )
            .await
            {
                if !run_epoch.with_untracked(|epoch| epoch.accept(gen))
                    || selected.get_untracked() != plan_id
                {
                    return;
                }
                run.set(Some(body.run));
            }
        });
    }) as Box<dyn FnMut()>);
    let handle = web_sys::window().and_then(|window| {
        window
            .set_interval_with_callback_and_timeout_and_arguments_0(
                cb.as_ref().unchecked_ref(),
                1500,
            )
            .ok()
    });
    // The callback is not Send. Leak it for the page lifetime and only clear
    // the interval id (an i32) when this view unmounts.
    cb.forget();
    on_cleanup(move || {
        if let Some(id) = handle {
            if let Some(window) = web_sys::window() {
                window.clear_interval_with_handle(id);
            }
        }
    });
}
