//! Strike web client. Chat tabs are live daemon roots. The Activity panel is
//! the team snapshot: delegated children, including blocked handoffs.
//! Schedule, memory, knowledge, artifacts, and the task runner are not drawn
//! here; see docs/webui-planned.md.

mod api;
mod model;

use std::cell::{Cell, RefCell};
use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use model::{
    display_state, fmt_elapsed, is_running, ActivityRow, ChatLine, Health, Remote, RootSummary,
    SessionItem, SessionsResponse, TeamSnapshot, Workspace, WorkspacesResponse,
};
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{get_json, idempotency_key, now_ms, post_json, post_json_value, post_op};
use crate::model::{budget_elapsed, can_retry, RootsResponse};

fn storage() -> Option<web_sys::Storage> {
    web_sys::window()?.local_storage().ok().flatten()
}

fn load(key: &str) -> String {
    storage()
        .and_then(|s| s.get_item(key).ok().flatten())
        .unwrap_or_default()
}

fn save(key: &str, value: &str) {
    if let Some(s) = storage() {
        let _ = s.set_item(key, value);
    }
}

fn same_origin(base: &str) -> bool {
    let base = base.trim().trim_end_matches('/');
    if base.is_empty() {
        return true;
    }
    web_sys::window()
        .and_then(|w| w.location().origin().ok())
        .map(|origin| origin.trim_end_matches('/') == base)
        .unwrap_or(false)
}

fn load_remotes() -> Vec<Remote> {
    serde_json::from_str(&load("strike.remotes")).unwrap_or_default()
}

#[component]
fn Badge(kind: String, label: String) -> impl IntoView {
    let class = format!("badge badge-{kind}");
    view! { <span class=class>{label}</span> }
}

#[component]
fn StatCard(label: String, children: Children) -> impl IntoView {
    view! {
        <div class="stat card">
            <div class="stat-value">{children()}</div>
            <div class="stat-label">{label}</div>
        </div>
    }
}

#[component]
fn EmptyState(title: String, body: String) -> impl IntoView {
    view! {
        <div class="empty-state">
            <strong>{title}</strong>
            <p>{body}</p>
        </div>
    }
}

#[component]
fn Skeleton() -> impl IntoView {
    view! {
        <div class="skeleton" aria-hidden="true">
            <div class="skeleton-line"></div>
            <div class="skeleton-line short"></div>
        </div>
    }
}

#[component]
fn PageHeader(status: RwSignal<String>, mode: RwSignal<String>) -> impl IntoView {
    view! {
        <header class="page-header">
            <div>
                <div class="brand">"Strike"</div>
                <div class="status-line">{move || status.get()}</div>
            </div>
            <div class="segmented" role="tablist" aria-label="View">
                <button
                    class=move || if mode.get() == "chat" { "seg on" } else { "seg" }
                    on:click=move |_| mode.set("chat".into())
                >"Chat"</button>
                <button
                    class=move || if mode.get() == "daemon" { "seg on" } else { "seg" }
                    on:click=move |_| mode.set("daemon".into())
                >"Daemon"</button>
            </div>
        </header>
    }
}

fn push_line(lines: RwSignal<Vec<ChatLine>>, seq: &Rc<Cell<u32>>, role: &str, title: &str, body: &str, badge: &str) {
    let id = seq.get() + 1;
    seq.set(id);
    lines.update(|list| {
        list.push(ChatLine {
            id,
            role: role.into(),
            title: title.into(),
            body: body.into(),
            badge: badge.into(),
        });
    });
}

fn apply_envelope(lines: RwSignal<Vec<ChatLine>>, seq: &Rc<Cell<u32>>, open: &Rc<Cell<bool>>, raw: &str) {
    let v: serde_json::Value = match serde_json::from_str(raw) {
        Ok(v) => v,
        Err(_) => return,
    };
    let typ = v.get("type").and_then(|t| t.as_str()).unwrap_or("");
    let data = v.get("data").cloned().unwrap_or(serde_json::Value::Null);
    let text = |key: &str| match data.get(key) {
        Some(serde_json::Value::String(s)) => s.clone(),
        Some(other) if !other.is_null() => other.to_string(),
        _ => String::new(),
    };
    match typ {
        "user.message" => {
            open.set(false);
            push_line(lines, seq, "you", "You", &text("text"), "");
        }
        "turn.started" => open.set(false),
        "text.delta" => {
            let chunk = text("text");
            if chunk.is_empty() {
                return;
            }
            if open.get() {
                lines.update(|list| {
                    if let Some(last) = list.last_mut() {
                        if last.role == "strike" {
                            last.body.push_str(&chunk);
                            return;
                        }
                    }
                });
                // If the last row was not the assistant, fall through by checking.
                let appended = lines.with(|list| list.last().map(|l| l.role == "strike").unwrap_or(false));
                if appended {
                    return;
                }
            }
            open.set(true);
            push_line(lines, seq, "strike", "Strike", &chunk, "");
        }
        "turn.completed" => open.set(false),
        "tool.begin" => {
            open.set(false);
            let name = text("name");
            push_line(lines, seq, "tool", &name, &text("args"), "tool");
        }
        "tool.end" => {
            let title = text("title");
            let output = text("output");
            let err = data.get("isError").and_then(|v| v.as_bool()).unwrap_or(false);
            let badge = if err { "failed" } else { "done" };
            let heading = if title.is_empty() { text("name") } else { title };
            push_line(lines, seq, "tool", &heading, &output, badge);
        }
        "child.completed" => {
            let status = text("status");
            let name = text("name");
            let mut summary = text("summary");
            if summary.is_empty() {
                summary = data
                    .get("handoff")
                    .and_then(|h| h.get("summary"))
                    .and_then(|s| s.as_str())
                    .unwrap_or("")
                    .to_string();
            }
            let who = if name.is_empty() { text("sessionId") } else { name };
            let title = format!("Child {who}");
            push_line(lines, seq, "child", &title, &summary, &status_badge(&status));
        }
        _ => {}
    }
}

fn short_id(id: &str) -> String {
    let tail = id.rsplit('-').next().unwrap_or(id);
    let count = tail.chars().count();
    if count <= 6 {
        return tail.to_string();
    }
    tail.chars().skip(count - 6).collect()
}

fn status_badge(status: &str) -> String {
    match status.to_ascii_lowercase().as_str() {
        "completed" => "done".into(),
        "canceled" | "cancelled" => "stopped".into(),
        "failed" => "failed".into(),
        "blocked" => "blocked".into(),
        other => other.to_string(),
    }
}

fn rows_from_team(prev: &[ActivityRow], team: &TeamSnapshot, now: f64) -> Vec<ActivityRow> {
    let mut rows = Vec::new();
    for (key, member) in &team.members {
        let session_id = if member.session_id.is_empty() {
            key.clone()
        } else {
            member.session_id.clone()
        };
        if !team.lead_id.is_empty() && session_id == team.lead_id {
            continue;
        }
        if member.role.eq_ignore_ascii_case("lead") {
            continue;
        }
        let state = display_state(member);
        let mut output = member.terminal_summary.clone();
        if !member.block_reason.is_empty() {
            if !output.is_empty() {
                output.push_str("\n\n");
            }
            output.push_str(&member.block_reason);
        }
        let started_ms = prev
            .iter()
            .find(|p| p.session_id == session_id)
            .map(|p| p.started_ms)
            .unwrap_or(now);
        let expanded = prev
            .iter()
            .find(|p| p.session_id == session_id)
            .map(|p| p.expanded)
            .unwrap_or(false);
        let server_elapsed = budget_elapsed(member);
        let elapsed_s = if server_elapsed > 0 {
            server_elapsed
        } else {
            ((now - started_ms) / 1000.0).max(0.0) as i64
        };
        let name = if member.name.is_empty() {
            session_id.chars().take(8).collect()
        } else {
            member.name.clone()
        };
        rows.push(ActivityRow {
            session_id,
            name,
            agent: member.agent.clone(),
            task: member.objective.clone(),
            state,
            output,
            elapsed_s,
            started_ms,
            expanded,
        });
    }
    rows.sort_by(|a, b| a.name.cmp(&b.name).then(a.session_id.cmp(&b.session_id)));
    rows
}

#[component]
fn App() -> impl IntoView {
    let base = RwSignal::new(load("strike.base"));
    let token = RwSignal::new(load("strike.token"));
    let status = RwSignal::new("connecting…".to_string());
    let mode = RwSignal::new(if load("strike.token").is_empty() && load("strike.base").is_empty() {
        "chat".to_string()
    } else {
        "chat".to_string()
    });
    let workspaces = RwSignal::new(Vec::<Workspace>::new());
    let active_workspace = RwSignal::new(String::new());
    let new_path = RwSignal::new(String::new());
    let remotes = RwSignal::new(load_remotes());
    let remote_name = RwSignal::new(String::new());
    let remote_url = RwSignal::new(String::new());
    let roots = RwSignal::new(Vec::<RootSummary>::new());
    let sessions = RwSignal::new(Vec::<SessionItem>::new());
    let active_root = RwSignal::new(String::new());
    let lines = RwSignal::new(Vec::<ChatLine>::new());
    let activity = RwSignal::new(Vec::<ActivityRow>::new());
    let team_note = RwSignal::new(String::new());
    let draft = RwSignal::new(String::new());
    let loading = RwSignal::new(true);
    let tick = RwSignal::new(0u32);
    let stream_note = RwSignal::new(String::new());

    let poll_team = move || {
        let (b, t, root) = (base.get_untracked(), token.get_untracked(), active_root.get_untracked());
        if root.is_empty() {
            return;
        }
        let path = format!("/v1/team?root={}", api::js_encode(&root));
        spawn_local(async move {
            match get_json::<TeamSnapshot>(&b, &t, &path).await {
                Ok(team) => {
                    if !team.available && !team.unavailable_reason.is_empty() {
                        team_note.set(team.unavailable_reason.clone());
                    } else {
                        team_note.set(String::new());
                    }
                    let now = now_ms();
                    let prev = activity.get_untracked();
                    activity.set(rows_from_team(&prev, &team, now));
                }
                Err(e) => team_note.set(e),
            }
        });
    };

    let refresh_meta = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match get_json::<Health>(&b, &t, "/health").await {
                Ok(h) if h.ok => status.set(format!("daemon ok · strike {}", h.version)),
                Ok(_) => status.set("daemon answered but not ok".into()),
                Err(e) => status.set(format!("health failed: {e}")),
            }
            match get_json::<WorkspacesResponse>(&b, &t, "/v1/workspaces").await {
                Ok(r) => {
                    active_workspace.set(r.active_id);
                    workspaces.set(r.workspaces);
                }
                Err(e) => status.set(format!("workspaces failed: {e}")),
            }
            if let Ok(r) = get_json::<RootsResponse>(&b, &t, "/v1/roots").await {
                if active_root.get_untracked().is_empty() {
                    let id = if !r.active_id.is_empty() {
                        r.active_id.clone()
                    } else {
                        r.roots.first().map(|x| x.id.clone()).unwrap_or_default()
                    };
                    active_root.set(id);
                }
                roots.set(r.roots);
            }
            if let Ok(r) = get_json::<SessionsResponse>(&b, &t, "/v1/sessions").await {
                sessions.set(r.sessions);
            }
            loading.set(false);
            poll_team();
        });
    };

    refresh_meta();

    let tail: Rc<RefCell<Option<web_sys::EventSource>>> = Rc::new(RefCell::new(None));
    let seq = Rc::new(Cell::new(0u32));
    let open = Rc::new(Cell::new(false));
    let tail_fx = tail.clone();
    let seq_fx = seq.clone();
    let open_fx = open.clone();
    Effect::new(move |_| {
        let root = active_root.get();
        let b = base.get();
        if let Some(es) = tail_fx.borrow_mut().take() {
            es.close();
        }
        seq_fx.set(0);
        open_fx.set(false);
        lines.set(Vec::new());
        if root.is_empty() {
            stream_note.set(String::new());
            return;
        }
        poll_team();
        if !same_origin(&b) {
            stream_note.set("Live transcript follows the daemon that served this page. Activity still polls the selected base URL.".into());
            return;
        }
        stream_note.set(String::new());
        let url = format!("/v1/live/events?root={}", api::js_encode(&root));
        let es = match web_sys::EventSource::new(&url) {
            Ok(es) => es,
            Err(e) => {
                stream_note.set(format!("live events failed: {e:?}"));
                return;
            }
        };
        let lines_cb = lines;
        let seq_cb = seq_fx.clone();
        let open_cb = open_fx.clone();
        let on_msg = Closure::<dyn FnMut(web_sys::MessageEvent)>::new(move |ev: web_sys::MessageEvent| {
            if let Some(data) = ev.data().as_string() {
                apply_envelope(lines_cb, &seq_cb, &open_cb, &data);
            }
        });
        es.set_onmessage(Some(on_msg.as_ref().unchecked_ref()));
        on_msg.forget();
        let note = stream_note;
        let on_err = Closure::<dyn FnMut(web_sys::Event)>::new(move |_ev| {
            note.set("live event stream closed. Reload after the daemon cookie handoff if /v1 requires a token.".into());
        });
        es.set_onerror(Some(on_err.as_ref().unchecked_ref()));
        on_err.forget();
        *tail_fx.borrow_mut() = Some(es);
    });

    let poll_tick = tick;
    let interval = Closure::<dyn FnMut()>::new(move || {
        let n = poll_tick.get_untracked().wrapping_add(1);
        poll_tick.set(n);
        if n % 2 == 0 {
            poll_team();
            refresh_meta();
        }
    });
    if let Some(w) = web_sys::window() {
        let _ = w.set_interval_with_callback_and_timeout_and_arguments_0(interval.as_ref().unchecked_ref(), 1000);
    }
    interval.forget();

    let send = move || {
        let text = draft.get_untracked();
        let text = text.trim().to_string();
        if text.is_empty() {
            return;
        }
        let root = active_root.get_untracked();
        if root.is_empty() {
            status.set("no live session yet".into());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        draft.set(String::new());
        spawn_local(async move {
            if let Err(e) = post_op(&b, &t, &root, "user.input", serde_json::json!({"text": text})).await {
                status.set(format!("send failed: {e}"));
            }
        });
    };

    let new_session = move |_| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match post_json_value(&b, &t, "/v1/roots", serde_json::json!({})).await {
                Ok(created) => {
                    let id = created.get("id").and_then(|v| v.as_str()).unwrap_or("").to_string();
                    if !id.is_empty() {
                        let _ = post_json(&b, &t, &format!("/v1/roots/{id}/activate"), serde_json::json!({})).await;
                        active_root.set(id);
                    }
                    if let Ok(r) = get_json::<RootsResponse>(&b, &t, "/v1/roots").await {
                        roots.set(r.roots);
                    }
                }
                Err(e) => status.set(format!("new session failed: {e}")),
            }
        });
    };

    let activate_root = move |id: String| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        active_root.set(id.clone());
        spawn_local(async move {
            if let Err(e) = post_json(&b, &t, &format!("/v1/roots/{id}/activate"), serde_json::json!({})).await {
                status.set(format!("switch session failed: {e}"));
            }
        });
    };

    let resume_session = move |id: String| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/roots/{id}/resume");
            match post_json(&b, &t, &path, serde_json::json!({})).await {
                Ok(()) => active_root.set(id),
                Err(e) => status.set(format!("resume failed: {e}")),
            }
        });
    };

    let stop_child = move |id: String| {
        let (b, t, root) = (base.get_untracked(), token.get_untracked(), active_root.get_untracked());
        spawn_local(async move {
            let data = serde_json::json!({
                "childSessionId": id,
                "reason": "stopped from activity panel",
                "idempotencyKey": idempotency_key("stop"),
            });
            if let Err(e) = post_op(&b, &t, &root, "team.child_interrupt", data).await {
                status.set(format!("stop failed: {e}"));
            }
        });
    };

    let retry_child = move |task: String| {
        let (b, t, root) = (base.get_untracked(), token.get_untracked(), active_root.get_untracked());
        spawn_local(async move {
            let data = serde_json::json!({
                "objective": task,
                "idempotencyKey": idempotency_key("retry"),
            });
            if let Err(e) = post_op(&b, &t, &root, "team.spawn", data).await {
                status.set(format!("retry failed: {e}"));
            }
        });
    };

    let stop_all = move |_| {
        let rows = activity.get_untracked();
        for row in rows {
            if is_running(&row.state) {
                stop_child(row.session_id);
            }
        }
    };

    let running_count = move || activity.get().iter().filter(|r| is_running(&r.state)).count();
    let blocked_count = move || activity.get().iter().filter(|r| r.state == "blocked").count();

    view! {
        <PageHeader status=status mode=mode />
        <Show when=move || mode.get() == "daemon" fallback=move || ()>
            <DaemonPane
                base=base
                token=token
                status=status
                workspaces=workspaces
                active_workspace=active_workspace
                new_path=new_path
                remotes=remotes
                remote_name=remote_name
                remote_url=remote_url
                refresh=refresh_meta
            />
        </Show>
        <Show when=move || mode.get() == "chat">
            <div class="shell">
                <aside class="rail">
                    <h2>"Projects"</h2>
                    <p class="hint">"Workspaces recorded on this daemon. The live engine still runs in the directory serve was started in."</p>
                    <Show when=move || workspaces.get().is_empty()>
                        <EmptyState title="No workspaces".into() body="Add a directory from the Daemon view.".into() />
                    </Show>
                    <ul class="plain">
                        {move || workspaces.get().into_iter().map(|w| {
                            let id = w.id.clone();
                            let on = move || active_workspace.get() == id;
                            let activate_id = w.id.clone();
                            view! {
                                <li class=move || if on() { "row card on" } else { "row card" }>
                                    <button class="link" on:click=move |_| {
                                        let (b, t, id) = (base.get(), token.get(), activate_id.clone());
                                        spawn_local(async move {
                                            let path = format!("/v1/workspaces/{id}/activate");
                                            match post_json(&b, &t, &path, serde_json::json!({})).await {
                                                Ok(()) => active_workspace.set(id),
                                                Err(e) => status.set(format!("activate failed: {e}")),
                                            }
                                        });
                                    }>
                                        <span class="name">{w.name}</span>
                                        <span class="path">{w.path}</span>
                                    </button>
                                </li>
                            }
                        }).collect_view()}
                    </ul>
                    <h2>"Sessions"</h2>
                    <Show when=move || sessions.get().is_empty()>
                        <EmptyState title="No saved sessions".into() body="A new live root appears here after the first turn.".into() />
                    </Show>
                    <ul class="plain">
                        {move || sessions.get().into_iter().map(|s| {
                            let id = s.id.clone();
                            let label = if s.title.is_empty() { short_id(&s.id) } else { s.title.clone() };
                            view! {
                                <li class="row card">
                                    <button class="link" on:click=move |_| resume_session(id.clone())>
                                        <span class="name">{label}</span>
                                        <span class="path">{s.id.clone()}</span>
                                    </button>
                                </li>
                            }
                        }).collect_view()}
                    </ul>
                    <p class="hint">"Schedule, memory, knowledge, artifacts, and the task runner are not in this client yet."</p>
                </aside>
                <section class="chat">
                    <div class="tabs" role="tablist" aria-label="Sessions">
                        {move || roots.get().into_iter().map(|r| {
                            let id = r.id.clone();
                            let id_class = id.clone();
                            let label = if r.title.is_empty() { short_id(&r.id) } else { r.title.clone() };
                            let busy = r.busy;
                            view! {
                                <button
                                    class=move || if active_root.get() == id_class { "tab on" } else { "tab" }
                                    on:click=move |_| activate_root(id.clone())
                                >
                                    {label}
                                    <Show when=move || busy><span class="dot" aria-label="busy"></span></Show>
                                </button>
                            }
                        }).collect_view()}
                        <button class="tab add" on:click=new_session>"+"</button>
                    </div>
                    <Show when=move || !stream_note.get().is_empty()>
                        <p class="hint banner">{move || stream_note.get()}</p>
                    </Show>
                    <div class="transcript" aria-live="polite">
                        <Show when=move || loading.get()>
                            <Skeleton />
                        </Show>
                        <Show when=move || !loading.get() && lines.get().is_empty()>
                            <EmptyState
                                title="This session is empty".into()
                                body="Send a message. Delegated children show up in Activity, and their results land here when they finish.".into()
                            />
                        </Show>
                        {move || lines.get().into_iter().map(|line| {
                            let badge = line.badge.clone();
                            let badge_kind = badge.clone();
                            view! {
                                <article class=format!("bubble card role-{}", line.role)>
                                    <header>
                                        <span class="who">{line.title.clone()}</span>
                                        <Show when=move || !badge.is_empty()>
                                            <Badge kind=badge_kind.clone() label=badge_kind.clone() />
                                        </Show>
                                    </header>
                                    <pre>{line.body.clone()}</pre>
                                </article>
                            }
                        }).collect_view()}
                    </div>
                    <form class="composer" on:submit=move |ev| { ev.prevent_default(); send(); }>
                        <textarea
                            placeholder="Message this session"
                            prop:value=move || draft.get()
                            on:input=move |ev| draft.set(event_target_value(&ev))
                            on:keydown=move |ev: web_sys::KeyboardEvent| {
                                if ev.key() == "Enter" && !ev.shift_key() {
                                    ev.prevent_default();
                                    send();
                                }
                            }
                        ></textarea>
                        <button type="submit">"Send"</button>
                    </form>
                </section>
                <aside class="activity">
                    <header class="activity-head">
                        <h2>"Activity"</h2>
                        <button class="secondary" disabled=move || running_count() == 0 on:click=stop_all>"Stop all"</button>
                    </header>
                        <div class="stats">
                        <StatCard label="Running".into()>{move || running_count().to_string()}</StatCard>
                        <StatCard label="Blocked".into()>{move || blocked_count().to_string()}</StatCard>
                        <StatCard label="Sessions".into()>{move || { let _ = tick.get(); roots.get().len().to_string() }}</StatCard>
                    </div>
                    <Show when=move || !team_note.get().is_empty()>
                        <p class="hint">{move || team_note.get()}</p>
                    </Show>
                    <Show when=move || activity.get().is_empty()>
                        <EmptyState
                            title="No delegated children".into()
                            body="task and delegate runs appear here with elapsed time and status: running, done, failed, stalled, blocked, or stopped.".into()
                        />
                    </Show>
                    <ul class="plain">
                        {move || {
                            let _ = tick.get();
                            activity.get().into_iter().map(|row| {
                                let state = row.state.clone();
                                let started_ms = row.started_ms;
                                let server_elapsed = row.elapsed_s;
                                let elapsed = {
                                    let local = ((now_ms() - started_ms) / 1000.0) as i64;
                                    fmt_elapsed(local.max(server_elapsed))
                                };
                                let sid = row.session_id.clone();
                                let sid_stop = sid.clone();
                                let task = if row.task.is_empty() { "(no task text)".to_string() } else { row.task.clone() };
                                let task_retry = task.clone();
                                let output = row.output.clone();
                                let expanded = row.expanded;
                                let running = is_running(&state);
                                let retry = can_retry(&state);
                                let name = row.name.clone();
                                let agent = row.agent.clone();
                                let agent_label = agent.clone();
                                view! {
                                    <li class="card activity-row">
                                        <button class="link activity-main" on:click=move |_| {
                                            let sid = sid.clone();
                                            activity.update(|rows| {
                                                if let Some(r) = rows.iter_mut().find(|r| r.session_id == sid) {
                                                    r.expanded = !r.expanded;
                                                }
                                            });
                                        }>
                                            <span class="name">{name}</span>
                                            <span class="path">{task.clone()}</span>
                                            <span class="meta">
                                                <Badge kind=state.clone() label=state.clone() />
                                                <span class="elapsed">{elapsed}</span>
                                                <Show when=move || !agent.is_empty()>
                                                    <span class="path">{agent_label.clone()}</span>
                                                </Show>
                                            </span>
                                        </button>
                                        <div class="row-actions">
                                            <button class="secondary" disabled=move || !running on:click={
                                                let sid_stop = sid_stop.clone();
                                                move |ev: web_sys::MouseEvent| {
                                                    ev.stop_propagation();
                                                    stop_child(sid_stop.clone());
                                                }
                                            }>"Stop"</button>
                                            <Show when=move || retry>
                                                <button class="secondary" on:click={
                                                    let task_retry = task_retry.clone();
                                                    move |ev: web_sys::MouseEvent| {
                                                        ev.stop_propagation();
                                                        retry_child(task_retry.clone());
                                                    }
                                                }>"Retry"</button>
                                            </Show>
                                        </div>
                                        <Show when=move || expanded>
                                            <pre class="output">{if output.is_empty() { "No output yet.".to_string() } else { output.clone() }}</pre>
                                        </Show>
                                    </li>
                                }
                            }).collect_view()
                        }}
                    </ul>
                </aside>
            </div>
        </Show>
    }
}

#[component]
fn DaemonPane(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    workspaces: RwSignal<Vec<Workspace>>,
    active_workspace: RwSignal<String>,
    new_path: RwSignal<String>,
    remotes: RwSignal<Vec<Remote>>,
    remote_name: RwSignal<String>,
    remote_url: RwSignal<String>,
    refresh: impl Fn() + Copy + Send + 'static,
) -> impl IntoView {
    let add_workspace = move |_| {
        let path = new_path.get();
        if path.trim().is_empty() {
            return;
        }
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match post_json(&b, &t, "/v1/workspaces", serde_json::json!({ "path": path })).await {
                Ok(()) => {
                    new_path.set(String::new());
                    if let Ok(r) = get_json::<WorkspacesResponse>(&b, &t, "/v1/workspaces").await {
                        active_workspace.set(r.active_id);
                        workspaces.set(r.workspaces);
                    }
                }
                Err(e) => status.set(format!("add workspace failed: {e}")),
            }
        });
    };
    let add_remote = move |_| {
        let (name, url) = (remote_name.get(), remote_url.get());
        if name.trim().is_empty() || url.trim().is_empty() {
            return;
        }
        remotes.update(|list| list.push(Remote { name, url }));
        save("strike.remotes", &serde_json::to_string(&remotes.get()).unwrap_or_default());
        remote_name.set(String::new());
        remote_url.set(String::new());
    };
    let connect = move |_: web_sys::MouseEvent| {
        save("strike.base", &base.get());
        save("strike.token", &token.get());
        refresh();
    };

    view! {
        <main class="daemon">
            <section class="card">
                <h2>"Daemon"</h2>
                <label>"Base URL (blank = this daemon)"</label>
                <input placeholder="http://100.64.0.5:8787"
                    prop:value=move || base.get()
                    on:input=move |ev| base.set(event_target_value(&ev)) />
                <label>"Token"</label>
                <input type="password" autocomplete="off"
                    prop:value=move || token.get()
                    on:input=move |ev| token.set(event_target_value(&ev)) />
                <button on:click=connect>"Connect"</button>
                <p class="hint">"Same-origin live transcript uses the daemon cookie. Open /?token=… once if the stream closes under --auth."</p>
            </section>
            <section class="card">
                <h2>"Saved daemons"</h2>
                <ul class="plain">
                    {move || remotes.get().into_iter().map(|r| {
                        let url = r.url.clone();
                        view! {
                            <li class="row">
                                <span><span class="name">{r.name}</span><span class="path">{r.url}</span></span>
                                <button class="secondary" on:click=move |_| {
                                    base.set(url.clone());
                                    save("strike.base", &base.get());
                                    save("strike.token", &token.get());
                                    refresh();
                                }>"Use"</button>
                            </li>
                        }
                    }).collect_view()}
                </ul>
                <div class="row">
                    <input placeholder="name" prop:value=move || remote_name.get()
                        on:input=move |ev| remote_name.set(event_target_value(&ev)) />
                    <input placeholder="http://host:8787" prop:value=move || remote_url.get()
                        on:input=move |ev| remote_url.set(event_target_value(&ev)) />
                    <button class="secondary" on:click=add_remote>"Save"</button>
                </div>
            </section>
            <section class="card">
                <h2>"Workspaces"</h2>
                <Show when=move || workspaces.get().is_empty()>
                    <EmptyState title="No workspaces".into() body="Add a directory that exists on the daemon host.".into() />
                </Show>
                <ul class="plain">
                    {move || workspaces.get().into_iter().map(|w| {
                        let id = w.id.clone();
                        let is_active = move || active_workspace.get() == id;
                        let activate_id = w.id.clone();
                        view! {
                            <li class="row">
                                <span>
                                    {w.name}
                                    <Show when=is_active>" "<Badge kind="done".into() label="active".into() /></Show>
                                    <span class="path">{w.path}</span>
                                </span>
                                <button class="secondary" on:click=move |_| {
                                    let (b, t, id) = (base.get(), token.get(), activate_id.clone());
                                    spawn_local(async move {
                                        let path = format!("/v1/workspaces/{id}/activate");
                                        match post_json(&b, &t, &path, serde_json::json!({})).await {
                                            Ok(()) => active_workspace.set(id),
                                            Err(e) => status.set(format!("activate failed: {e}")),
                                        }
                                    });
                                }>"Open"</button>
                            </li>
                        }
                    }).collect_view()}
                </ul>
                <div class="row">
                    <input placeholder="/home/you/Projects/my-repo"
                        prop:value=move || new_path.get()
                        on:input=move |ev| new_path.set(event_target_value(&ev)) />
                    <button on:click=add_workspace>"Add"</button>
                </div>
            </section>
        </main>
    }
}

fn main() {
    leptos::mount::mount_to_body(App);
}
