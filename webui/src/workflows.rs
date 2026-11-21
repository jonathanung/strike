//! Workflows view (#1501): catalog, per-workflow document, and a raw
//! draft editor wired to the daemon's authoring routes.
//!
//! - Catalog: `GET /v1/workflows`; document: `GET /v1/workflows/{name}/document`.
//! - "Edit as draft" formats the document through `POST /v1/workflows/format`
//!   so the editor holds the same canonical JSON a saved file has.
//! - Review: `POST /v1/workflow-drafts/review` (checks + permission widening).
//! - Validate: `POST /v1/workflows/validate` (structural + known-agent checks).
//! - Save draft: `POST /v1/workflow-drafts/save`, behind a confirm that states
//!   the draft is saved inactive. Saving never activates (the daemon answers
//!   `activated: false`); there are no start/stop controls in this view.
//!
//! Save is offered only for the exact text the last Review accepted (Q2
//! default): Review errors block save, and a widening needs a confirm
//! that names every widened grant.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::{json, Map, Value};

use crate::api::{get_json, js_encode, post_json_value};
use crate::confirm::ConfirmRequest;

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Permission {
    #[serde(default)]
    pub permission: String,
    #[serde(default)]
    pub pattern: String,
    #[serde(default)]
    pub action: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct PhaseDoc {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub description: String,
    #[serde(default)]
    pub agent: String,
    #[serde(default)]
    pub context: String,
    #[serde(default)]
    pub gate: String,
    #[serde(default, rename = "gateCommand")]
    pub gate_command: String,
    #[serde(default)]
    pub permissions: Vec<Permission>,
    /// Review only: effective grant delta vs the host baseline.
    #[serde(default)]
    pub widening: Vec<Permission>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct CatalogRow {
    pub name: String,
    #[serde(default)]
    pub description: String,
    #[serde(default)]
    pub source: String,
    #[serde(default)]
    pub valid: bool,
    #[serde(default, rename = "validationError")]
    pub validation_error: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct CatalogList {
    #[serde(default)]
    workflows: Vec<CatalogRow>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Document {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub description: String,
    #[serde(default)]
    pub phases: Vec<PhaseDoc>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct DraftReview {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub valid: bool,
    #[serde(default, rename = "validationError")]
    pub validation_error: String,
    #[serde(default, rename = "hasChecks")]
    pub has_checks: bool,
    #[serde(default, rename = "hasWidening")]
    pub has_widening: bool,
    #[serde(default)]
    pub phases: Vec<PhaseDoc>,
}

/// `allow bash *`, `deny write` …
pub fn perm_line(p: &Permission) -> String {
    let mut out = format!("{} {}", p.action.trim(), p.permission.trim());
    if !p.pattern.trim().is_empty() {
        out.push(' ');
        out.push_str(p.pattern.trim());
    }
    out
}

/// Human gate label for a phase exit.
pub fn gate_line(gate: &str, command: &str) -> String {
    match gate.trim() {
        "user" => "Exit: you approve".into(),
        "agent" => "Exit: the agent calls phase_done".into(),
        "check" if !command.trim().is_empty() => format!("Exit: check `{}` passes", command.trim()),
        "check" => "Exit: check command (none set)".into(),
        "skip" => "Exit: no gate".into(),
        "" => "Exit: not set".into(),
        other => format!("Exit: {other}"),
    }
}

/// One line per widened grant, prefixed by its phase.
pub fn widening_lines(review: &DraftReview) -> Vec<String> {
    review
        .phases
        .iter()
        .flat_map(|phase| {
            phase
                .widening
                .iter()
                .map(move |w| format!("{}: {}", phase.name, perm_line(w)))
        })
        .collect()
}

/// Convert the editor's canonical workflow JSON (on-disk shape, with
/// `exit: {type, command}`) into the host document shape that
/// `/v1/workflows/validate` accepts (`gate` / `gateCommand`).
pub fn draft_to_document(raw: &str) -> Result<Value, String> {
    let mut v: Value =
        serde_json::from_str(raw).map_err(|e| format!("The draft is not valid JSON: {e}"))?;
    let Some(obj) = v.as_object_mut() else {
        return Err("The draft must be a JSON object.".into());
    };
    if let Some(Value::Array(phases)) = obj.get_mut("phases") {
        for phase in phases.iter_mut() {
            let Some(p) = phase.as_object_mut() else {
                continue;
            };
            if let Some(Value::Object(exit)) = p.remove("exit") {
                if let Some(t) = exit.get("type").and_then(|t| t.as_str()) {
                    p.entry("gate").or_insert_with(|| Value::String(t.to_string()));
                }
                if let Some(c) = exit.get("command").and_then(|c| c.as_str()) {
                    p.entry("gateCommand")
                        .or_insert_with(|| Value::String(c.to_string()));
                }
            }
        }
    } else {
        obj.entry("phases").or_insert_with(|| Value::Array(Vec::new()));
    }
    Ok(v)
}

/// Pull the daemon's `{"error": "..."}` reason out of an `HTTP <code> <body>`
/// error string; otherwise return the text unchanged.
pub fn daemon_error(raw: &str) -> String {
    let body = raw
        .strip_prefix("HTTP ")
        .and_then(|rest| rest.split_once(' ').map(|(_, body)| body))
        .unwrap_or(raw);
    serde_json::from_str::<Map<String, Value>>(body.trim())
        .ok()
        .and_then(|m| m.get("error").and_then(|e| e.as_str()).map(str::to_string))
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| raw.to_string())
}

/// Save gate: only the exact text the last Review accepted.
pub fn save_block(review: Option<&DraftReview>, reviewed: &str, current: &str) -> Option<&'static str> {
    let Some(review) = review else {
        return Some("Review the draft before saving.");
    };
    if reviewed != current {
        return Some("The draft changed since the last Review. Review it again before saving.");
    }
    if !review.valid {
        return Some("Review reported errors. Fix them before saving.");
    }
    None
}

/// Confirm before Save draft: always states the draft is saved inactive;
/// a widening is named grant by grant.
pub fn save_confirm(review: &DraftReview, scope: &str, force: bool) -> ConfirmRequest {
    let mut body = format!(
        "Saves \"{}\" to the {scope} workflow catalog as an inactive definition. Saving never starts it; a session runs it only when someone starts it.",
        review.name
    );
    if force {
        body.push_str(" An existing file with this name is overwritten.");
    }
    let widened = widening_lines(review);
    if !widened.is_empty() {
        body.push_str(" When started, it widens permissions beyond your current rules: ");
        body.push_str(&widened.join("; "));
        body.push('.');
    }
    ConfirmRequest {
        title: if widened.is_empty() {
            format!("Save \"{}\" inactive?", review.name)
        } else {
            format!("Save \"{}\" inactive — it widens permissions?", review.name)
        },
        body,
        confirm_label: if widened.is_empty() {
            "Save inactive".into()
        } else {
            "Save inactive with widening".into()
        },
    }
}

fn phase_view(phase: PhaseDoc, review_mode: bool) -> impl IntoView {
    let name = phase.name.clone();
    let meta = {
        let mut parts = Vec::new();
        if !phase.agent.is_empty() {
            parts.push(format!("Agent: {}", phase.agent));
        }
        parts.push(gate_line(&phase.gate, &phase.gate_command));
        parts.join(" · ")
    };
    let desc = phase.description.clone();
    let context = phase.context.clone();
    let perms: Vec<String> = phase.permissions.iter().map(perm_line).collect();
    let widening: Vec<String> = phase.widening.iter().map(perm_line).collect();
    let phase_id = name.clone();
    view! {
        <li class="wf-phase" data-wf-phase=phase_id>
            <p class="wf-phase-name">{name}</p>
            <p class="hint">{meta}</p>
            <Show when={let d = desc.clone(); move || !d.is_empty()}>
                <p>{desc.clone()}</p>
            </Show>
            <Show when={let c = context.clone(); move || !c.is_empty()}>
                <pre class="wf-context">{context.clone()}</pre>
            </Show>
            <Show when={let p = perms.clone(); move || !p.is_empty()}>
                <ul class="plain wf-perms" aria-label="Phase permissions">
                    {perms.clone().into_iter().map(|p| view! { <li><code>{p}</code></li> }).collect_view()}
                </ul>
            </Show>
            <Show when={let w = widening.clone(); move || review_mode && !w.is_empty()}>
                <ul class="plain wf-widening" data-wf-widening="true" aria-label="Permission widening">
                    {widening.clone().into_iter().map(|w| view! { <li><strong>"Widens: "</strong><code>{w}</code></li> }).collect_view()}
                </ul>
            </Show>
        </li>
    }
}

#[component]
pub fn WorkflowsPage(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
) -> impl IntoView {
    let rows = RwSignal::new(Vec::<CatalogRow>::new());
    let selected = RwSignal::new(String::new());
    let doc = RwSignal::new(None::<Document>);
    let notice = RwSignal::new(String::new());
    let draft = RwSignal::new(String::new());
    let review = RwSignal::new(None::<DraftReview>);
    let reviewed_text = RwSignal::new(String::new());
    let validate_note = RwSignal::new(String::new());
    let scope = RwSignal::new("project".to_string());
    let force = RwSignal::new(false);
    let saved_note = RwSignal::new(String::new());

    let load_catalog = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match get_json::<CatalogList>(&b, &t, "/v1/workflows").await {
                Ok(list) => rows.set(list.workflows),
                Err(e) => notice.set(format!("Could not load workflows: {}", daemon_error(&e))),
            }
        });
    };
    load_catalog();

    let open_doc = move |name: String| {
        selected.set(name.clone());
        doc.set(None);
        notice.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/workflows/{}/document", js_encode(&name));
            match get_json::<Value>(&b, &t, &path).await {
                Ok(v) => {
                    if selected.get_untracked() != name {
                        return;
                    }
                    match serde_json::from_value::<Document>(v) {
                        Ok(d) => doc.set(Some(d)),
                        Err(e) => notice.set(format!("Unreadable workflow document: {e}")),
                    }
                }
                Err(e) => notice.set(format!("Could not open {name}: {}", daemon_error(&e))),
            }
        });
    };

    let edit_as_draft = move || {
        let name = selected.get_untracked();
        if name.is_empty() {
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/workflows/{}/document", js_encode(&name));
            let document = match get_json::<Value>(&b, &t, &path).await {
                Ok(v) => v,
                Err(e) => {
                    notice.set(format!("Could not open {name}: {}", daemon_error(&e)));
                    return;
                }
            };
            match post_json_value(&b, &t, "/v1/workflows/format", json!({ "document": document })).await {
                Ok(v) => {
                    let text = v.get("json").and_then(|j| j.as_str()).unwrap_or("").to_string();
                    draft.set(text);
                    review.set(None);
                    validate_note.set(String::new());
                    saved_note.set(String::new());
                }
                Err(e) => saved_note.set(format!("Format failed: {}", daemon_error(&e))),
            }
        });
    };

    let run_review = move || {
        let text = draft.get_untracked();
        let (b, t) = (base.get_untracked(), token.get_untracked());
        saved_note.set(String::new());
        spawn_local(async move {
            match post_json_value(&b, &t, "/v1/workflow-drafts/review", json!({ "json": text.clone() })).await {
                Ok(v) => match serde_json::from_value::<DraftReview>(v) {
                    Ok(r) => {
                        review.set(Some(r));
                        reviewed_text.set(text);
                    }
                    Err(e) => saved_note.set(format!("Unreadable review: {e}")),
                },
                Err(e) => {
                    review.set(None);
                    saved_note.set(format!("Review failed: {}", daemon_error(&e)));
                }
            }
        });
    };

    let run_validate = move || {
        let document = match draft_to_document(&draft.get_untracked()) {
            Ok(d) => d,
            Err(e) => {
                validate_note.set(e);
                return;
            }
        };
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match post_json_value(&b, &t, "/v1/workflows/validate", json!({ "document": document })).await {
                Ok(v) => {
                    let ok = v.get("ok").and_then(|o| o.as_bool()).unwrap_or(false);
                    let err = v.get("error").and_then(|e| e.as_str()).unwrap_or("").to_string();
                    validate_note.set(if ok {
                        "Validate: no problems found.".into()
                    } else {
                        format!("Validate: {err}")
                    });
                }
                Err(e) => validate_note.set(format!("Validate failed: {}", daemon_error(&e))),
            }
        });
    };

    let do_save = move || {
        let text = draft.get_untracked();
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let (sc, fo) = (scope.get_untracked(), force.get_untracked());
        spawn_local(async move {
            let body = json!({ "json": text, "scope": sc, "confirm": true, "force": fo });
            match post_json_value(&b, &t, "/v1/workflow-drafts/save", body).await {
                Ok(v) => {
                    let path = v.get("path").and_then(|p| p.as_str()).unwrap_or("").to_string();
                    let activated = v.get("activated").and_then(|a| a.as_bool()).unwrap_or(false);
                    saved_note.set(if activated {
                        format!("Saved to {path}, but the daemon reported it active.")
                    } else {
                        format!("Saved inactive to {path}. It runs only when someone starts it.")
                    });
                    status.set("workflow draft saved (inactive)".into());
                    load_catalog();
                }
                Err(e) => saved_note.set(format!("Save refused: {}", daemon_error(&e))),
            }
        });
    };

    let request_save = move || {
        let current = draft.get_untracked();
        let r = review.get_untracked();
        if let Some(reason) = save_block(r.as_ref(), &reviewed_text.get_untracked(), &current) {
            saved_note.set(reason.into());
            return;
        }
        let Some(r) = r else { return };
        let req = save_confirm(&r, &scope.get_untracked(), force.get_untracked());
        if !crate::confirm::ask(req, std::rc::Rc::new(move || do_save())) {
            saved_note.set(crate::confirm::BUSY.into());
        }
    };

    view! {
        <main class="workflows" data-workflows-root="true">
            <section class="wf-list" aria-label="Workflows catalog">
                <div class="section-head">
                    <h2 class="panel-title">"Workflows"</h2>
                    <button type="button" class="secondary" on:click=move |_| load_catalog()>"Refresh"</button>
                </div>
                <Show when=move || !notice.get().is_empty()>
                    <p class="notice" role="alert">{move || notice.get()}</p>
                </Show>
                <Show when=move || rows.get().is_empty()>
                    <p class="hint">"No workflows loaded."</p>
                </Show>
                <ul class="plain">
                    {move || rows.get().into_iter().map(|row| {
                        let name = row.name.clone();
                        let row_id = row.name.clone();
                        let label = row.name.clone();
                        let pick = row.name.clone();
                        let meta = if row.valid {
                            if row.source.is_empty() { row.description.clone() } else { format!("{} · {}", row.source, row.description) }
                        } else {
                            format!("Invalid: {}", row.validation_error)
                        };
                        view! {
                            <li>
                                <button type="button" data-wf-row=row_id class=move || if selected.get() == name { "row card on" } else { "row card" } on:click=move |_| open_doc(pick.clone())>
                                    <span class="name">{label}</span>
                                    <span class="path">{meta}</span>
                                </button>
                            </li>
                        }
                    }).collect_view()}
                </ul>
            </section>
            <section class="wf-doc" aria-label="Workflow document" data-wf-doc="true">
                {move || match doc.get() {
                    None => view! { <p class="hint">"Pick a workflow to read its phases, gates and permissions."</p> }.into_any(),
                    Some(d) => {
                        let Document { name, description, phases } = d;
                        view! {
                        <div class="section-head">
                            <h2 class="panel-title">{name}</h2>
                            <button type="button" class="secondary" data-action="wf-edit-draft" on:click=move |_| edit_as_draft()>"Edit as draft"</button>
                        </div>
                        <p class="hint">{description}</p>
                        <ol class="plain wf-phases">
                            {phases.into_iter().map(|p| phase_view(p, false)).collect_view()}
                        </ol>
                        }.into_any()
                    }
                }}
            </section>
            <section class="wf-draft" aria-label="Workflow draft">
                <h2 class="panel-title">"Draft"</h2>
                <p class="hint">"Raw workflow JSON. Review and Validate run on the daemon; saving stores it inactive and never starts it."</p>
                <textarea id="wf-draft" class="wf-editor" rows="14" spellcheck="false" aria-label="Workflow draft JSON" prop:value=move || draft.get() on:input=move |ev| draft.set(event_target_value(&ev))></textarea>
                <div class="row wf-actions">
                    <button type="button" class="primary" data-action="wf-review" on:click=move |_| run_review()>"Review"</button>
                    <button type="button" class="secondary" data-action="wf-validate" on:click=move |_| run_validate()>"Validate"</button>
                    <label class="wf-scope">"Scope"
                        <select prop:value=move || scope.get() on:change=move |ev| scope.set(event_target_value(&ev))>
                            <option value="project">"Project"</option>
                            <option value="global">"Global"</option>
                        </select>
                    </label>
                    <label class="wf-force"><input type="checkbox" prop:checked=move || force.get() on:change=move |ev| force.set(event_target_checked(&ev)) />"Overwrite existing"</label>
                    <button type="button" class="secondary" data-action="wf-save" disabled=move || save_block(review.get().as_ref(), &reviewed_text.get(), &draft.get()).is_some() on:click=move |_| request_save()>"Save draft…"</button>
                </div>
                <Show when=move || !validate_note.get().is_empty()>
                    <p class="hint" data-wf-validate="true" role="status">{move || validate_note.get()}</p>
                </Show>
                <Show when=move || !saved_note.get().is_empty()>
                    <p class="hint" data-wf-saved="true" role="status">{move || saved_note.get()}</p>
                </Show>
                {move || review.get().map(|r| {
                    let stale = reviewed_text.get() != draft.get();
                    let verdict = if r.valid { "Review: valid".to_string() } else { format!("Review: invalid — {}", r.validation_error) };
                    let widened = widening_lines(&r);
                    let valid = r.valid;
                    let has_checks = r.has_checks;
                    let phases = r.phases.clone();
                    let widened_text = format!("Widens permissions: {}", widened.join("; "));
                    view! {
                        <div class="wf-review" data-wf-review={if valid { "valid" } else { "invalid" }}>
                            <p class={if valid { "wf-verdict ok" } else { "wf-verdict bad" }} role="status">{verdict}</p>
                            <Show when=move || stale>
                                <p class="hint warn">"The draft changed since this Review."</p>
                            </Show>
                            <Show when=move || has_checks>
                                <p class="hint">"Has check gates: their commands run when a phase exits."</p>
                            </Show>
                            <Show when={let w = widened.clone(); move || !w.is_empty()}>
                                <p class="hint warn" data-wf-widening-summary="true">{widened_text.clone()}</p>
                            </Show>
                            <ol class="plain wf-phases">
                                {phases.into_iter().map(|p| phase_view(p, true)).collect_view()}
                            </ol>
                        </div>
                    }
                })}
            </section>
        </main>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn perm(a: &str, p: &str, pat: &str) -> Permission {
        Permission {
            permission: p.into(),
            pattern: pat.into(),
            action: a.into(),
        }
    }

    #[test]
    fn perm_and_gate_lines() {
        assert_eq!(perm_line(&perm("allow", "bash", "*")), "allow bash *");
        assert_eq!(perm_line(&perm("deny", "write", "")), "deny write");
        let cases = [
            ("user", "", "Exit: you approve"),
            ("agent", "", "Exit: the agent calls phase_done"),
            ("check", "make test", "Exit: check `make test` passes"),
            ("check", "", "Exit: check command (none set)"),
            ("", "", "Exit: not set"),
        ];
        for (g, c, want) in cases {
            assert_eq!(gate_line(g, c), want);
        }
    }

    #[test]
    fn draft_to_document_maps_exit_to_gate() {
        let raw = r#"{"name":"w","phases":[{"name":"a","exit":{"type":"check","command":"make test"}},{"name":"b","exit":{"type":"user"}}]}"#;
        let v = draft_to_document(raw).unwrap();
        assert_eq!(v["phases"][0]["gate"], "check");
        assert_eq!(v["phases"][0]["gateCommand"], "make test");
        assert!(v["phases"][0].get("exit").is_none());
        assert_eq!(v["phases"][1]["gate"], "user");
        assert!(draft_to_document("{").is_err());
        assert!(draft_to_document("[]").is_err());
        assert_eq!(draft_to_document(r#"{"name":"x"}"#).unwrap()["phases"], json!([]));
    }

    #[test]
    fn daemon_error_extracts_reason() {
        assert_eq!(daemon_error(r#"HTTP 409 {"error":"workflow file already exists"}"#), "workflow file already exists");
        assert_eq!(daemon_error("HTTP 500 boom"), "HTTP 500 boom");
        assert_eq!(daemon_error("network down"), "network down");
    }

    #[test]
    fn save_block_requires_fresh_valid_review() {
        let ok = DraftReview { valid: true, ..Default::default() };
        let bad = DraftReview { valid: false, ..Default::default() };
        assert!(save_block(None, "", "x").is_some());
        assert!(save_block(Some(&ok), "a", "b").is_some());
        assert!(save_block(Some(&bad), "a", "a").is_some());
        assert!(save_block(Some(&ok), "a", "a").is_none());
    }

    #[test]
    fn save_confirm_states_inactive_and_names_widening() {
        let mut r = DraftReview {
            name: "w".into(),
            valid: true,
            ..Default::default()
        };
        let plain = save_confirm(&r, "project", false);
        assert!(plain.body.contains("inactive"));
        assert!(plain.body.contains("never starts"));
        assert!(!plain.body.contains("widens"));
        r.phases = vec![PhaseDoc {
            name: "build".into(),
            widening: vec![perm("allow", "bash", "*")],
            ..Default::default()
        }];
        let wide = save_confirm(&r, "global", true);
        assert!(wide.body.contains("build: allow bash *"));
        assert!(wide.body.contains("overwritten"));
        assert_eq!(wide.confirm_label, "Save inactive with widening");
        assert_eq!(widening_lines(&r), vec!["build: allow bash *".to_string()]);
    }

    #[test]
    fn review_json_parses() {
        let r: DraftReview = serde_json::from_str(
            r#"{"name":"w","valid":true,"hasWidening":true,"phases":[{"name":"p","gate":"agent","widening":[{"permission":"bash","pattern":"*","action":"allow"}]}]}"#,
        )
        .unwrap();
        assert!(r.has_widening);
        assert_eq!(r.phases[0].widening.len(), 1);
    }
}
