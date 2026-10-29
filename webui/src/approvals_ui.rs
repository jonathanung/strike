//! Approval cards, the mode menu, and the permission settings panel.
//! HTTP stays in this module so the shell only mounts it.

use std::cell::Cell;
use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde_json::json;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{get_json, patch_json, post_json, post_op};
use crate::approvals::{
    begin_session_mode, countdown_seconds, decisions, global_confirm, is_dangerous,
    notes_from_envelope, rule_add_confirm, rule_remove_confirm, session_confirm,
    validate_rule_draft, workspace_confirm, Ask, AuditDocument, AuditLine, ConfirmKind, ModeChange,
    PendingConfirm, PermNote, RuleRecord, RulesDocument, SettingsBits,
};
use crate::model::{mode_label, permission_modes, Workspace};

pub fn commit_session_mode(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    perm_mode: RwSignal<String>,
    status: RwSignal<String>,
    mode: String,
) {
    if root.get_untracked().is_empty() {
        status.set("no live session yet".into());
        return;
    }
    let prev = perm_mode.get_untracked();
    perm_mode.set(mode.clone());
    let (b, t, r) = (
        base.get_untracked(),
        token.get_untracked(),
        root.get_untracked(),
    );
    spawn_local(async move {
        if let Err(e) = post_op(
            &b,
            &t,
            &r,
            "set.permission_mode",
            json!({"mode": mode}),
        )
        .await
        {
            perm_mode.set(prev);
            status.set(format!("approval mode failed: {e}"));
        }
    });
}

pub fn apply_perm_notes(
    raw: &str,
    asks: RwSignal<Vec<Ask>>,
    perm_mode: RwSignal<String>,
    live_audit: RwSignal<Vec<AuditLine>>,
) {
    for note in notes_from_envelope(raw) {
        match note {
            PermNote::Ask(ask) => asks.update(|list| {
                list.retain(|row| row.request_id != ask.request_id);
                list.push(ask);
            }),
            PermNote::Clear(id) => asks.update(|list| list.retain(|row| row.request_id != id)),
            PermNote::Mode(mode) => perm_mode.set(mode),
            PermNote::Decided(row) => live_audit.update(|list| {
                list.insert(0, row);
                list.truncate(40);
            }),
        }
    }
}

#[component]
pub fn ApprovalHost(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    perm_mode: RwSignal<String>,
    menu_open: RwSignal<bool>,
    panel_open: RwSignal<bool>,
    confirm: RwSignal<Option<PendingConfirm>>,
    asks: RwSignal<Vec<Ask>>,
    live_audit: RwSignal<Vec<AuditLine>>,
    workspaces: RwSignal<Vec<Workspace>>,
    active_workspace: RwSignal<String>,
    status: RwSignal<String>,
    auto_seconds: RwSignal<i32>,
    auto_exclude: RwSignal<Vec<String>>,
    rules: RwSignal<RulesDocument>,
    stored_audit: RwSignal<AuditDocument>,
    panel_error: RwSignal<String>,
    reply_n: RwSignal<u32>,
    reply_id: RwSignal<String>,
    reply_decision: RwSignal<String>,
) -> impl IntoView {
    let seen_reply = Rc::new(Cell::new(0u32));
    Effect::new(move |_| {
        let n = reply_n.get();
        if n == 0 || n == seen_reply.get() {
            return;
        }
        seen_reply.set(n);
        let id = reply_id.get_untracked();
        let decision = reply_decision.get_untracked();
        if id.is_empty() {
            return;
        }
        let (b, t, r) = (
            base.get_untracked(),
            token.get_untracked(),
            root.get_untracked(),
        );
        spawn_local(async move {
            match post_op(
                &b,
                &t,
                &r,
                "permission.reply",
                json!({"requestId": id, "decision": decision}),
            )
            .await
            {
                Ok(()) => asks.update(|list| list.retain(|row| row.request_id != id)),
                Err(e) => status.set(format!("approval failed: {e}")),
            }
        });
    });

    Effect::new(move |_| {
        if panel_open.get() {
            reload_panel(base, token, auto_seconds, auto_exclude, rules, stored_audit, panel_error);
        }
    });

    {
        let listener = Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            let key = ev.key();
            if confirm.get_untracked().is_some() {
                if key == "Enter" {
                    ev.prevent_default();
                    ev.stop_propagation();
                    accept_pending(confirm, base, token, root, perm_mode, status, panel_error, rules, stored_audit, auto_seconds, auto_exclude);
                } else if key == "Escape" {
                    ev.prevent_default();
                    ev.stop_propagation();
                    confirm.set(None);
                }
                return;
            }
            if !asks.get_untracked().is_empty() {
                if key == "Escape" {
                    ev.prevent_default();
                    ev.stop_propagation();
                    if let Some(ask) = asks.get_untracked().first() {
                        reply_id.set(ask.request_id.clone());
                        reply_decision.set("reject".into());
                        reply_n.update(|n| *n += 1);
                    }
                    return;
                }
                if !typing_target(&ev) {
                    let decision = match key.as_str() {
                        "1" => Some("once"),
                        "2" => Some("always"),
                        "3" => Some("project"),
                        "4" => Some("reject"),
                        _ => None,
                    };
                    if let Some(decision) = decision {
                        ev.prevent_default();
                        ev.stop_propagation();
                        if let Some(ask) = asks.get_untracked().first() {
                            reply_id.set(ask.request_id.clone());
                            reply_decision.set(decision.into());
                            reply_n.update(|n| *n += 1);
                        }
                    }
                }
                return;
            }
            if key == "Escape" {
                if panel_open.get_untracked() {
                    ev.prevent_default();
                    panel_open.set(false);
                } else if menu_open.get_untracked() {
                    ev.prevent_default();
                    menu_open.set(false);
                }
            }
        });
        if let Some(window) = web_sys::window() {
            let _ = window.add_event_listener_with_callback_and_bool(
                "keydown",
                listener.as_ref().unchecked_ref(),
                true,
            );
        }
        listener.forget();
    }

    let scope = RwSignal::new("global".to_string());
    let perm_name = RwSignal::new("bash".to_string());
    let pattern = RwSignal::new(String::new());
    let action = RwSignal::new("deny".to_string());
    let global_choice = RwSignal::new("default".to_string());
    let workspace_choice = RwSignal::new("default".to_string());
    Effect::new(move |_| {
        let mode = rules.get().global_mode;
        if crate::approvals::known_mode(&mode) {
            global_choice.set(mode);
        }
    });
    Effect::new(move |_| {
        let id = active_workspace.get();
        let list = workspaces.get();
        if let Some(ws) = list.iter().find(|w| w.id == id) {
            if crate::approvals::known_mode(&ws.permission_mode) {
                workspace_choice.set(ws.permission_mode.clone());
            }
        }
    });

    let submit_rule = move || {
        let permission = perm_name.get_untracked();
        let draft = pattern.get_untracked().trim().to_string();
        let action = action.get_untracked();
        let scope = scope.get_untracked();
        if let Err(msg) = validate_rule_draft(&permission, &draft, &action) {
            panel_error.set(msg.into());
            return;
        }
        if let Some(pending) = rule_add_confirm(&scope, &permission, &draft, &action) {
            confirm.set(Some(pending));
            return;
        }
        post_rule(
            base,
            token,
            status,
            panel_error,
            rules,
            json!({
                "scope": scope,
                "permission": permission,
                "pattern": draft,
                "action": action,
                "confirm": false,
            }),
            "/v1/permissions/rules",
        );
        pattern.set(String::new());
    };

    view! {
        {move || confirm.get().map(|pending| {
            let title = pending.title.clone();
            let body = pending.body.clone();
            let label = pending.confirm_label.clone();
            view! {
                <div class="approvals-backdrop" role="presentation">
                    <div class="approvals-dialog" role="dialog" aria-modal="true" aria-labelledby="approvals-confirm-title">
                        <h2 id="approvals-confirm-title">{title}</h2>
                        <p class="approvals-copy">{body}</p>
                        <div class="approvals-actions">
                            <button type="button" class="secondary" on:click=move |_| confirm.set(None)>"Cancel"</button>
                            <button type="button" class="danger" on:click=move |_| accept_pending(confirm, base, token, root, perm_mode, status, panel_error, rules, stored_audit, auto_seconds, auto_exclude)>{label}</button>
                        </div>
                        <p class="hint">"Enter confirms. Escape cancels and leaves permissions unchanged."</p>
                    </div>
                </div>
            }
        })}
        <Show when=move || panel_open.get()>
            <div class="approvals-backdrop" role="presentation" on:click=move |_| panel_open.set(false)>
                <div class="approvals-panel" role="dialog" aria-modal="true" aria-labelledby="approvals-panel-title" on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()>
                    <header class="approvals-panel-head">
                        <h2 id="approvals-panel-title">"Permissions"</h2>
                        <button type="button" class="secondary" on:click=move |_| panel_open.set(false)>"Close"</button>
                    </header>
                    <p class="approvals-copy">"Deny rules still apply in Yolo and Edits. A later allow in the same file can override an earlier deny. Managed denies are not listed and always win."</p>
                    <p class="hint">{move || rules.get().note}</p>
                    <p class="hint">"Strike modes are Default, Plan, Soft, Edits, and Yolo. Plan denies writes. It is stricter than a read-only shell, so it is not a Reads mode. There is no Trust mode. Yolo is this session only."</p>
                    <Show when=move || !panel_error.get().is_empty()>
                        <p class="notice">{move || panel_error.get()}</p>
                    </Show>
                    <section>
                        <h3>"This session"</h3>
                        <p class="hint">{move || format!("Live mode: {}", mode_label(&perm_mode.get()))}</p>
                    </section>
                    <section>
                        <h3>"Global default"</h3>
                        <p class="hint">{move || {
                            let mode = rules.get().global_mode.clone();
                            let settings_mode = if mode.is_empty() { "default".to_string() } else { mode };
                            let effective = rules.get().effective_mode.clone();
                            let project = rules.get().project_mode.clone();
                            if project.is_empty() {
                                format!("New sessions start at {effective}. Stored global mode is {settings_mode}.")
                            } else {
                                format!("A project config sets {project}, so new sessions start there. Stored global mode is {settings_mode}.")
                            }
                        }}</p>
                        <div class="approvals-form">
                            <select aria-label="Global default mode" prop:value=move || global_choice.get() on:change=move |ev| global_choice.set(event_target_value(&ev))>
                                {permission_modes().iter().map(|m| {
                                    let id = m.id.to_string();
                                    let label = m.label.to_string();
                                    view! { <option value=id>{label}</option> }
                                }).collect_view()}
                            </select>
                            <button type="button" class="secondary" on:click=move |_| {
                                let mode = global_choice.get_untracked();
                                if let Some(pending) = global_confirm(&mode) {
                                    confirm.set(Some(pending));
                                } else if crate::approvals::known_mode(&mode) {
                                    let (b, t) = (base.get_untracked(), token.get_untracked());
                                    spawn_local(async move {
                                        if let Err(e) = patch_json(&b, &t, "/v1/settings", json!({"mode": mode})).await {
                                            panel_error.set(format!("global default failed: {e}"));
                                        } else {
                                            reload_panel(base, token, auto_seconds, auto_exclude, rules, stored_audit, panel_error);
                                        }
                                    });
                                }
                            }>"Save global default"</button>
                        </div>
                    </section>
                    <section>
                        <h3>"Workspace default"</h3>
                        <p class="hint">{move || rules.get().workspace_note.clone()}</p>
                        {move || workspace_block(workspaces, active_workspace, workspace_choice, confirm, perm_mode, base, token, root, status)}
                    </section>
                    <section>
                        <h3>"Rules"</h3>
                        <p class="hint">{move || {
                            let dir = rules.get().work_dir.clone();
                            if dir.is_empty() {
                                "Project rules use the daemon working directory.".into()
                            } else {
                                format!("Project rules are stored for {dir}.")
                            }
                        }}</p>
                        <ul class="plain approvals-rules">
                            {move || rules.get().rules.into_iter().map(|rule| {
                                let shown = format!("{} · {} · {} · {}", rule.scope, rule.permission, rule.pattern, rule.action);
                                let remove = rule.clone();
                                view! {
                                    <li class="approvals-rule">
                                        <span>{shown}</span>
                                        <button type="button" class="secondary" on:click=move |_| remove_rule(remove.clone(), confirm, base, token, status, panel_error, rules)>"Remove"</button>
                                    </li>
                                }
                            }).collect_view()}
                        </ul>
                        <div class="approvals-form">
                            <select aria-label="Rule scope" on:change=move |ev| scope.set(event_target_value(&ev))>
                                <option value="global">"Global"</option>
                                <option value="project">"Project"</option>
                            </select>
                            <select aria-label="Permission name" on:change=move |ev| perm_name.set(event_target_value(&ev))>
                                {move || {
                                    let mut names = rules.get().known;
                                    if names.is_empty() {
                                        names = vec!["bash".into(), "edit".into(), "write".into(), "webfetch".into(), "mcp".into(), "*".into()];
                                    }
                                    names.into_iter().map(|name| {
                                        let value = name.clone();
                                        view! { <option value=value>{name}</option> }
                                    }).collect_view()
                                }}
                            </select>
                            <input
                                aria-label="Pattern"
                                placeholder="pattern, such as rm * or *.go"
                                prop:value=move || pattern.get()
                                on:input=move |ev| pattern.set(event_target_value(&ev))
                            />
                            <select aria-label="Rule action" on:change=move |ev| action.set(event_target_value(&ev))>
                                <option value="deny" selected>"Deny"</option>
                                <option value="ask">"Ask"</option>
                                <option value="allow">"Allow"</option>
                            </select>
                            <button type="button" class="primary" on:click=move |_| submit_rule()>"Add rule"</button>
                        </div>
                    </section>
                    <section>
                        <h3>"Audit"</h3>
                        <p class="hint">{move || {
                            let doc = stored_audit.get();
                            if doc.available || doc.note.is_empty() {
                                doc.note
                            } else {
                                format!("Audit log unavailable. {}", doc.note)
                            }
                        }}</p>
                        <ul class="plain approvals-audit">
                            {move || live_audit.get().into_iter().map(|row| audit_row(row.permission, row.action, row.decision, row.detail, "this session".into())).collect_view()}
                            {move || stored_audit.get().records.into_iter().map(|row| {
                                let mut detail = if row.patterns.is_empty() { row.layer.clone() } else { row.patterns.join(" ") };
                                if !row.rule_action.is_empty() {
                                    detail = format!("{detail} rule {}", row.rule_action);
                                }
                                let when = if row.session_id.is_empty() { row.time.clone() } else { format!("{} · {}", row.time, row.session_id) };
                                audit_row(row.permission, row.action, row.decision, detail, when)
                            }).collect_view()}
                        </ul>
                    </section>
                </div>
            </div>
        </Show>
    }
}

fn reload_panel(
    base: RwSignal<String>,
    token: RwSignal<String>,
    auto_seconds: RwSignal<i32>,
    auto_exclude: RwSignal<Vec<String>>,
    rules: RwSignal<RulesDocument>,
    stored_audit: RwSignal<AuditDocument>,
    panel_error: RwSignal<String>,
) {
    let (b, t) = (base.get_untracked(), token.get_untracked());
    spawn_local(async move {
        match get_json::<SettingsBits>(&b, &t, "/v1/settings").await {
            Ok(bits) => {
                auto_seconds.set(bits.auto_seconds);
                auto_exclude.set(bits.auto_exclude);
            }
            Err(e) => panel_error.set(e),
        }
        match get_json::<RulesDocument>(&b, &t, "/v1/permissions/rules").await {
            Ok(doc) => {
                rules.set(doc);
                panel_error.set(String::new());
            }
            Err(e) => panel_error.set(e),
        }
        match get_json::<AuditDocument>(&b, &t, "/v1/permissions/audit?limit=40").await {
            Ok(doc) => stored_audit.set(doc),
            Err(e) => {
                if panel_error.get_untracked().is_empty() {
                    panel_error.set(e);
                }
            }
        }
    });
}

fn accept_pending(
    confirm: RwSignal<Option<PendingConfirm>>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    perm_mode: RwSignal<String>,
    status: RwSignal<String>,
    panel_error: RwSignal<String>,
    rules: RwSignal<RulesDocument>,
    stored_audit: RwSignal<AuditDocument>,
    auto_seconds: RwSignal<i32>,
    auto_exclude: RwSignal<Vec<String>>,
) {
    let Some(pending) = confirm.get_untracked() else {
        return;
    };
    confirm.set(None);
    match pending.kind {
        ConfirmKind::Session { mode } => {
            commit_session_mode(base, token, root, perm_mode, status, mode);
        }
        ConfirmKind::Global { mode } => {
            let (b, t) = (base.get_untracked(), token.get_untracked());
            spawn_local(async move {
                if let Err(e) = patch_json(&b, &t, "/v1/settings", json!({"mode": mode})).await {
                    status.set(format!("global default failed: {e}"));
                } else {
                    reload_panel(base, token, auto_seconds, auto_exclude, rules, stored_audit, panel_error);
                }
            });
        }
        ConfirmKind::Workspace { id, mode } => {
            let (b, t) = (base.get_untracked(), token.get_untracked());
            spawn_local(async move {
                let path = format!("/v1/workspaces/{id}");
                if let Err(e) = patch_json(&b, &t, &path, json!({"permissionMode": mode})).await {
                    status.set(format!("workspace default failed: {e}"));
                } else {
                    reload_panel(base, token, auto_seconds, auto_exclude, rules, stored_audit, panel_error);
                }
            });
        }
        ConfirmKind::RuleAdd {
            scope,
            permission,
            pattern,
            action,
        } => post_rule(
            base,
            token,
            status,
            panel_error,
            rules,
            json!({
                "scope": scope,
                "permission": permission,
                "pattern": pattern,
                "action": action,
                "confirm": true,
            }),
            "/v1/permissions/rules",
        ),
        ConfirmKind::RuleRemove { scope, index } => post_rule(
            base,
            token,
            status,
            panel_error,
            rules,
            json!({"scope": scope, "index": index, "confirm": true}),
            "/v1/permissions/rules/remove",
        ),
    }
}

fn post_rule(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    panel_error: RwSignal<String>,
    rules: RwSignal<RulesDocument>,
    body: serde_json::Value,
    path: &'static str,
) {
    let (b, t) = (base.get_untracked(), token.get_untracked());
    spawn_local(async move {
        match post_json(&b, &t, path, body).await {
            Ok(()) => match get_json::<RulesDocument>(&b, &t, "/v1/permissions/rules").await {
                Ok(doc) => {
                    rules.set(doc);
                    panel_error.set(String::new());
                }
                Err(e) => panel_error.set(e),
            },
            Err(e) => {
                panel_error.set(e.clone());
                status.set(format!("rule change failed: {e}"));
            }
        }
    });
}

fn remove_rule(
    rule: RuleRecord,
    confirm: RwSignal<Option<PendingConfirm>>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    panel_error: RwSignal<String>,
    rules: RwSignal<RulesDocument>,
) {
    if let Some(pending) = rule_remove_confirm(&rule.scope, rule.index, &rule.action) {
        confirm.set(Some(pending));
        return;
    }
    post_rule(
        base,
        token,
        status,
        panel_error,
        rules,
        json!({"scope": rule.scope, "index": rule.index, "confirm": false}),
        "/v1/permissions/rules/remove",
    );
}

fn workspace_block(
    workspaces: RwSignal<Vec<Workspace>>,
    active_workspace: RwSignal<String>,
    workspace_choice: RwSignal<String>,
    confirm: RwSignal<Option<PendingConfirm>>,
    perm_mode: RwSignal<String>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    status: RwSignal<String>,
) -> impl IntoView {
    let id = active_workspace.get();
    let found = workspaces.get().into_iter().find(|w| w.id == id);
    let Some(ws) = found else {
        return view! { <p class="hint">"No workspace is active. Add one from Daemon, then set its stored mode here."</p> }.into_any();
    };
    let stored = if ws.permission_mode.is_empty() {
        "unset".to_string()
    } else {
        mode_label(&ws.permission_mode).to_string()
    };
    let ws_id = ws.id.clone();
    let stored_mode = ws.permission_mode.clone();
    let name = ws.name.clone();
    view! {
        <p class="hint">{format!("{name} stores {stored}.")}</p>
        <div class="approvals-form">
            <select aria-label="Workspace default mode" prop:value=move || workspace_choice.get() on:change=move |ev| workspace_choice.set(event_target_value(&ev))>
                {permission_modes().iter().map(|m| {
                    let id = m.id.to_string();
                    let label = m.label.to_string();
                    view! { <option value=id>{label}</option> }
                }).collect_view()}
            </select>
            <button type="button" class="secondary" on:click=move |_| {
                let mode = workspace_choice.get_untracked();
                let id = ws_id.clone();
                if let Some(pending) = workspace_confirm(&id, &mode) {
                    confirm.set(Some(pending));
                } else if crate::approvals::known_mode(&mode) {
                    let (b, t) = (base.get_untracked(), token.get_untracked());
                    spawn_local(async move {
                        let path = format!("/v1/workspaces/{id}");
                        if let Err(e) = patch_json(&b, &t, &path, json!({"permissionMode": mode})).await {
                            status.set(format!("workspace default failed: {e}"));
                        }
                    });
                }
            }>"Store on workspace"</button>
            <button type="button" class="secondary" on:click=move |_| {
                let mode = if stored_mode.is_empty() { workspace_choice.get_untracked() } else { stored_mode.clone() };
                match begin_session_mode(&perm_mode.get_untracked(), &mode) {
                    ModeChange::Same => {}
                    ModeChange::Apply(mode) => commit_session_mode(base, token, root, perm_mode, status, mode),
                    ModeChange::Confirm(pending) => confirm.set(Some(pending)),
                }
            }>"Apply to this session"</button>
        </div>
    }.into_any()
}

fn audit_row(permission: String, action: String, decision: String, detail: String, when: String) -> impl IntoView {
    let label = if permission.is_empty() { action.clone() } else { format!("{permission} · {action}") };
    let extra = if decision.is_empty() { detail } else { format!("{decision} {detail}") };
    view! {
        <li class="approvals-audit-row">
            <span class="name">{label}</span>
            <span class="path">{extra}</span>
            <span class="elapsed">{when}</span>
        </li>
    }
}

#[component]
pub fn ApprovalMenu(
    menu_open: RwSignal<bool>,
    perm_mode: RwSignal<String>,
    confirm: RwSignal<Option<PendingConfirm>>,
    panel_open: RwSignal<bool>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    status: RwSignal<String>,
) -> impl IntoView {
    view! {
        <Show when=move || menu_open.get()>
            <div class="menu approvals-menu" role="menu" aria-label="Approval mode">
                {permission_modes().iter().map(|m| {
                    let id = m.id.to_string();
                    let id_class = id.clone();
                    let id_click = id.clone();
                    let label = m.label.to_string();
                    let detail = m.detail.to_string();
                    let shown = if is_dangerous(m.id) {
                        format!("{label} · confirms")
                    } else {
                        label
                    };
                    view! {
                        <button type="button" class=move || {
                            if perm_mode.get() == id_class { "palette-row on" } else { "palette-row" }
                        } role="menuitem" on:click=move |_| {
                            menu_open.set(false);
                            match begin_session_mode(&perm_mode.get_untracked(), &id_click) {
                                ModeChange::Same => {}
                                ModeChange::Apply(mode) => commit_session_mode(base, token, root, perm_mode, status, mode),
                                ModeChange::Confirm(_) => confirm.set(Some(session_confirm(&id_click))),
                            }
                        }>
                            <span class="name">{shown}</span>
                            <span class="path">{detail}</span>
                        </button>
                    }
                }).collect_view()}
                <button type="button" class="palette-row" on:click=move |_| {
                    menu_open.set(false);
                    panel_open.set(true);
                }>
                    <span class="name">"Rules and audit"</span>
                    <span class="path">"Allow and deny patterns. Deny rules still apply in Yolo."</span>
                </button>
            </div>
        </Show>
    }
}

#[component]
pub fn ApprovalCards(
    asks: RwSignal<Vec<Ask>>,
    perm_mode: RwSignal<String>,
    auto_seconds: RwSignal<i32>,
    auto_exclude: RwSignal<Vec<String>>,
    reply_n: RwSignal<u32>,
    reply_id: RwSignal<String>,
    reply_decision: RwSignal<String>,
) -> impl IntoView {
    view! {
        <Show when=move || !asks.get().is_empty()>
            <div class="approvals-cards">
                <For
                    each=move || asks.get()
                    key=|ask| ask.request_id.clone()
                    children=move |ask| {
                        let id = ask.request_id.clone();
                        let permission = ask.permission.clone();
                        let seconds = Signal::derive(move || {
                            let oldest = asks.with(|list| {
                                list.first().map(|row| row.request_id.clone()).unwrap_or_default()
                            });
                            if oldest != id {
                                return 0;
                            }
                            countdown_seconds(
                                &perm_mode.get(),
                                auto_seconds.get(),
                                &permission,
                                &auto_exclude.get(),
                            )
                        });
                        view! {
                            <ApprovalCard ask=ask seconds=seconds reply_n=reply_n reply_id=reply_id reply_decision=reply_decision />
                        }
                    }
                />
            </div>
        </Show>
    }
}

#[component]
fn ApprovalCard(
    ask: Ask,
    seconds: Signal<i32>,
    reply_n: RwSignal<u32>,
    reply_id: RwSignal<String>,
    reply_decision: RwSignal<String>,
) -> impl IntoView {
    let left = RwSignal::new(0);
    let stop = RwSignal::new(false);
    let send_id = ask.request_id.clone();
    let request_id = send_id.clone();
    let handle = RwSignal::new(None::<i32>);
    on_cleanup(move || {
        if let (Some(window), Some(id)) = (web_sys::window(), handle.get_untracked()) {
            let _ = window.clear_interval_with_handle(id);
        }
    });
    Effect::new(move |_| {
        if let (Some(window), Some(id)) = (web_sys::window(), handle.get_untracked()) {
            let _ = window.clear_interval_with_handle(id);
            handle.set(None);
        }
        let seconds = seconds.get();
        left.set(seconds);
        if seconds <= 0 {
            return;
        }
        stop.set(false);
        let id = request_id.clone();
        let cb = Closure::<dyn FnMut()>::new(move || {
            if stop.get_untracked() {
                return;
            }
            let n = left.get_untracked() - 1;
            if n <= 0 {
                stop.set(true);
                left.set(0);
                reply_id.set(id.clone());
                reply_decision.set("once".into());
                reply_n.update(|c| *c += 1);
            } else {
                left.set(n);
            }
        });
        if let Some(window) = web_sys::window() {
            if let Ok(id) = window.set_interval_with_callback_and_timeout_and_arguments_0(
                cb.as_ref().unchecked_ref(),
                1000,
            ) {
                handle.set(Some(id));
            }
        }
        cb.forget();
    });

    let send: Rc<dyn Fn(String)> = Rc::new(move |decision: String| {
        if stop.get_untracked() && decision == "once" && left.get_untracked() == 0 {
            return;
        }
        stop.set(true);
        reply_id.set(send_id.clone());
        reply_decision.set(decision);
        reply_n.update(|n| *n += 1);
    });

    let permission = ask.permission.clone();
    let heading = ask.preview.heading.clone();
    let command = ask.preview.command.clone();
    let removed = ask.preview.removed.clone();
    let added = ask.preview.added.clone();
    let truncated = ask.preview.truncated;
    let show_command = !command.is_empty();
    let show_diff = !removed.is_empty() || !added.is_empty();
    // Decision facts (#1507): the daemon-stamped working directory and one
    // consequence line per scope, computed from the ask payload only.
    let work_dir_line = if ask.work_dir.is_empty() {
        String::new()
    } else {
        format!("Working directory: {}", ask.work_dir)
    };
    let show_work_dir = !work_dir_line.is_empty();
    let consequences: Vec<(&'static str, String)> = decisions()
        .iter()
        .map(|choice| (choice.label, ask.consequence(choice.id)))
        .collect();

    view! {
        <article class="approvals-card" aria-live="polite">
            <header>
                <span class="badge badge-blocked">"Needs you"</span>
                <span class="who">{permission.clone()}</span>
            </header>
            <p class="approvals-heading">{heading}</p>
            <Show when=move || show_command>
                <pre class="approvals-command">{command.clone()}</pre>
            </Show>
            <Show when=move || show_diff>
                <pre class="approvals-diff">
                    {removed.iter().map(|line| view! { <span class="del">{"- "}{line.clone()}{"\n"}</span> }).collect_view()}
                    {added.iter().map(|line| view! { <span class="add">{"+ "}{line.clone()}{"\n"}</span> }).collect_view()}
                </pre>
            </Show>
            <Show when=move || truncated>
                <p class="hint">"Diff preview is shortened."</p>
            </Show>
            <Show when=move || show_work_dir>
                <p class="approvals-fact">{work_dir_line.clone()}</p>
            </Show>
            <p class="approvals-fact">"Approving runs this on the gateway host, with the daemon's privileges."</p>
            <Show when=move || { seconds.get() > 0 }>
                <p class="approvals-count">{move || format!("Allows once in {}s unless you choose.", left.get())}</p>
            </Show>
            <p class="hint">"1 allow once, 2 allow session, 3 allow project, 4 or Escape denies."</p>
            <ul class="approvals-scopes">
                {consequences.into_iter().map(|(label, line)| view! {
                    <li><strong>{label}</strong>{" — "}{line}</li>
                }).collect_view()}
            </ul>
            <div class="approvals-actions">
                {decisions().iter().map(|choice| {
                    let id = choice.id.to_string();
                    let label = format!("{} ({})", choice.label, choice.key);
                    let class = if choice.id == "reject" { "danger" } else { "secondary" };
                    let detail = choice.detail;
                    let send = Rc::clone(&send);
                    view! {
                        <button type="button" class=class title=detail on:click=move |_| send(id.clone())>{label}</button>
                    }
                }).collect_view()}
            </div>
        </article>
    }
}

fn typing_target(ev: &web_sys::KeyboardEvent) -> bool {
    let Some(target) = ev.target() else {
        return false;
    };
    let Some(el) = target.dyn_ref::<web_sys::Element>() else {
        return false;
    };
    let tag = el.tag_name().to_ascii_lowercase();
    tag == "textarea" || tag == "input" || tag == "select"
}
