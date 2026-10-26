//! Strike web client. The session list is live roots plus saved sessions.
//! Activity, Files, and Changes are the side panel. Schedule, memory,
//! knowledge, artifacts, and the task runner are not drawn here; see
//! docs/webui-planned.md.

mod api;
mod model;

use std::cell::{Cell, RefCell};
use std::collections::HashMap;
use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use model::{
    display_state, filter_sessions, fmt_elapsed, is_running, merge_sessions, mode_label,
    permission_modes, visible_sessions, ActivityRow, Bootstrap, ChangedFilesResponse, ChatLine,
    ChildrenResponse, FilesResponse, Health, ModelInfo, ModelList, Remote, RootSummary,
    SessionItem, SessionsResponse, TeamSnapshot, Workspace, WorkspacesResponse,
};
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{
    get_json, idempotency_key, now_ms, patch_json, post_json, post_json_value, post_op,
};
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

fn push_line(
    lines: RwSignal<Vec<ChatLine>>,
    seq: &Rc<Cell<u32>>,
    role: &str,
    title: &str,
    body: &str,
    badge: &str,
) {
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

fn apply_envelope(
    lines: RwSignal<Vec<ChatLine>>,
    seq: &Rc<Cell<u32>>,
    open: &Rc<Cell<bool>>,
    raw: &str,
) {
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
                let appended =
                    lines.with(|list| list.last().map(|l| l.role == "strike").unwrap_or(false));
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
            let err = data
                .get("isError")
                .and_then(|v| v.as_bool())
                .unwrap_or(false);
            let badge = if err { "failed" } else { "done" };
            let heading = if title.is_empty() {
                text("name")
            } else {
                title
            };
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
            let who = if name.is_empty() {
                text("sessionId")
            } else {
                name
            };
            let title = format!("Child {who}");
            push_line(
                lines,
                seq,
                "child",
                &title,
                &summary,
                &status_badge(&status),
            );
        }
        _ => {}
    }
}

fn join_path(dir: &str, name: &str) -> String {
    if dir.is_empty() {
        name.to_string()
    } else {
        format!("{dir}/{name}")
    }
}

fn parent_path(dir: &str) -> String {
    match dir.rsplit_once('/') {
        Some((parent, _)) => parent.to_string(),
        None => String::new(),
    }
}

fn empty_headline(tick: u32) -> &'static str {
    match (tick / 4) % 3 {
        0 => "What should this session do?",
        1 => "This session is idle.",
        _ => "Delegated results land in the transcript.",
    }
}

const SUGGESTIONS: [&str; 6] = [
    "What can you do in this session?",
    "Summarize the working directory",
    "Explain how the approval modes differ",
    "Draft a short plan before changing files",
    "Review what this session has done",
    "What should I look at next?",
];

#[derive(Clone, Copy)]
struct PaletteCmd {
    section: &'static str,
    id: &'static str,
    label: &'static str,
    detail: &'static str,
}

fn palette_cmds() -> &'static [PaletteCmd] {
    &[
        PaletteCmd {
            section: "Commands",
            id: "new",
            label: "New session",
            detail: "Open another live root",
        },
        PaletteCmd {
            section: "Commands",
            id: "stop-all",
            label: "Stop all",
            detail: "Interrupt running delegated children in this session",
        },
        PaletteCmd {
            section: "Apps",
            id: "chat",
            label: "Chat",
            detail: "Sessions and the transcript",
        },
        PaletteCmd {
            section: "Apps",
            id: "daemon",
            label: "Daemon",
            detail: "This gateway, its token, and workspaces",
        },
        PaletteCmd {
            section: "Settings",
            id: "steer",
            label: "Steer",
            detail: "Enter adds your message to the current turn without stopping it",
        },
        PaletteCmd {
            section: "Settings",
            id: "queue",
            label: "Queue",
            detail: "Enter waits until the turn finishes",
        },
        PaletteCmd {
            section: "Settings",
            id: "mode-default",
            label: "Approval: Default",
            detail: "Ask on the normal rules",
        },
        PaletteCmd {
            section: "Settings",
            id: "mode-plan",
            label: "Approval: Plan",
            detail: "Deny writes and edits",
        },
        PaletteCmd {
            section: "Settings",
            id: "mode-soft",
            label: "Approval: Soft",
            detail: "Count down 15 seconds, then allow once",
        },
        PaletteCmd {
            section: "Settings",
            id: "mode-edits",
            label: "Approval: Edits",
            detail: "Allow file edits; still ask for shell and network",
        },
        PaletteCmd {
            section: "Settings",
            id: "mode-yolo",
            label: "Approval: Yolo",
            detail: "This session skips asks that are not hard-denied",
        },
    ]
}

fn palette_match(cmd: &PaletteCmd, query: &str) -> bool {
    query.is_empty()
        || cmd.label.to_ascii_lowercase().contains(query)
        || cmd.detail.to_ascii_lowercase().contains(query)
}

fn load_string_list(key: &str) -> Vec<String> {
    serde_json::from_str(&load(key)).unwrap_or_default()
}

fn load_seen() -> HashMap<String, i64> {
    serde_json::from_str(&load("strike.seen")).unwrap_or_default()
}

fn set_drag(ev: &web_sys::DragEvent, path: &str) {
    if let Some(dt) = ev.data_transfer() {
        let _ = dt.set_data("text/plain", &format!("@{path}"));
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
    let mode = RwSignal::new(
        if load("strike.token").is_empty() && load("strike.base").is_empty() {
            "chat".to_string()
        } else {
            "chat".to_string()
        },
    );
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
    let load_error = RwSignal::new(String::new());
    let gateway = RwSignal::new("checking".to_string());
    let focus = RwSignal::new(false);
    let palette_open = RwSignal::new(false);
    let palette_query = RwSignal::new(String::new());
    let back_stack = RwSignal::new(Vec::<String>::new());
    let forward_stack = RwSignal::new(Vec::<String>::new());
    let pins = RwSignal::new(load_string_list("strike.pins"));
    let seen = RwSignal::new(load_seen());
    let session_filter = RwSignal::new("all".to_string());
    let session_query = RwSignal::new(String::new());
    let older_open = RwSignal::new(false);
    let children = RwSignal::new(Vec::<model::ChildSession>::new());
    let perm_mode = RwSignal::new("default".to_string());
    let provider_name = RwSignal::new(String::new());
    let model_name = RwSignal::new(String::new());
    let model_note = RwSignal::new(String::new());
    let catalog = RwSignal::new(Vec::<ModelInfo>::new());
    let catalog_note = RwSignal::new(String::new());
    let mode_menu = RwSignal::new(false);
    let model_menu = RwSignal::new(false);
    let enter_mode = RwSignal::new(if load("strike.enterMode") == "steer" {
        "steer".to_string()
    } else {
        "queue".to_string()
    });
    let live_busy = RwSignal::new(false);
    let live_busy_root = RwSignal::new(String::new());
    let side_tab = RwSignal::new("activity".to_string());
    let files_path = RwSignal::new(String::new());
    let files = RwSignal::new(Vec::<model::DirEntry>::new());
    let files_error = RwSignal::new(String::new());
    let changes = RwSignal::new(Vec::<model::ChangedFile>::new());
    let changes_error = RwSignal::new(String::new());

    let poll_team = move || {
        let (b, t, root) = (
            base.get_untracked(),
            token.get_untracked(),
            active_root.get_untracked(),
        );
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
            let mut err = String::new();
            match get_json::<Health>(&b, &t, "/health").await {
                Ok(h) if h.ok => {
                    gateway.set("connected".into());
                    status.set(format!("strike {}", h.version));
                }
                Ok(_) => {
                    gateway.set("unreachable".into());
                    err = "The daemon answered, but it is not ok.".into();
                }
                Err(e) => {
                    gateway.set("unreachable".into());
                    err = format!("Could not reach the daemon: {e}");
                }
            }
            match get_json::<WorkspacesResponse>(&b, &t, "/v1/workspaces").await {
                Ok(r) => {
                    active_workspace.set(r.active_id);
                    workspaces.set(r.workspaces);
                }
                Err(e) => {
                    if err.is_empty() {
                        err = format!("Workspaces failed to load: {e}");
                    }
                }
            }
            match get_json::<RootsResponse>(&b, &t, "/v1/roots").await {
                Ok(r) => {
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
                Err(e) => {
                    if err.is_empty() {
                        err = format!("Sessions failed to load: {e}");
                    }
                }
            }
            match get_json::<SessionsResponse>(&b, &t, "/v1/sessions").await {
                Ok(r) => sessions.set(r.sessions),
                Err(e) => {
                    if err.is_empty() {
                        err = format!("Saved sessions failed to load: {e}");
                    }
                }
            }
            let root = active_root.get_untracked();
            if !root.is_empty() {
                let path = format!("/v1/bootstrap?root={}", api::js_encode(&root));
                match get_json::<Bootstrap>(&b, &t, &path).await {
                    Ok(boot) => {
                        if let Some(st) = boot.status {
                            provider_name.set(st.provider);
                            model_name.set(st.model);
                            if !st.permission_mode.is_empty() {
                                perm_mode.set(st.permission_mode);
                            }
                            live_busy.set(st.busy);
                            live_busy_root.set(root);
                        }
                        model_note.set(String::new());
                    }
                    Err(e) => model_note.set(format!("Live status unavailable: {e}")),
                }
            }
            load_error.set(err);
            loading.set(false);
            poll_team();
        });
    };

    refresh_meta();

    let mark_seen = move |id: String| {
        let mtime = sessions
            .get_untracked()
            .iter()
            .find(|s| s.id == id)
            .map(|s| s.mtime)
            .unwrap_or(0);
        let now = (now_ms() / 1000.0) as i64;
        seen.update(|map| {
            map.insert(id, mtime.max(now));
        });
        save(
            "strike.seen",
            &serde_json::to_string(&seen.get_untracked()).unwrap_or_default(),
        );
    };

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
        children.set(Vec::new());
        if root.is_empty() {
            stream_note.set(String::new());
            return;
        }
        mark_seen(root.clone());
        let (cb, ct, child_root) = (b.clone(), token.get_untracked(), root.clone());
        spawn_local(async move {
            let path = format!("/v1/sessions/{}/children", api::js_encode(&child_root));
            if let Ok(found) = get_json::<ChildrenResponse>(&cb, &ct, &path).await {
                children.set(found.sessions);
            }
        });
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
        let on_msg =
            Closure::<dyn FnMut(web_sys::MessageEvent)>::new(move |ev: web_sys::MessageEvent| {
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
        let _ = w.set_interval_with_callback_and_timeout_and_arguments_0(
            interval.as_ref().unchecked_ref(),
            1000,
        );
    }
    interval.forget();

    let session_busy = move |root: &str| -> bool {
        roots.get_untracked().iter().any(|r| r.id == root && r.busy)
            || (live_busy.get_untracked() && live_busy_root.get_untracked() == root)
    };

    let send_text = move |text: String| {
        let text = text.trim().to_string();
        if text.is_empty() {
            return;
        }
        let root = active_root.get_untracked();
        if root.is_empty() {
            status.set("no live session yet".into());
            return;
        }
        let op = if session_busy(&root) && enter_mode.get_untracked() == "steer" {
            "steer"
        } else {
            "user.input"
        };
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Err(e) = post_op(&b, &t, &root, op, serde_json::json!({"text": text})).await {
                status.set(format!("send failed: {e}"));
            }
        });
    };

    let send = move || {
        let text = draft.get_untracked();
        draft.set(String::new());
        send_text(text);
    };

    let insert_mention = move |path: String| {
        draft.update(|d| {
            if !d.is_empty() && !d.ends_with(' ') && !d.ends_with('\n') {
                d.push(' ');
            }
            d.push('@');
            d.push_str(&path);
            d.push(' ');
        });
    };

    let remember = move |id: &str| {
        let cur = active_root.get_untracked();
        if !cur.is_empty() && cur != id {
            back_stack.update(|s| s.push(cur));
            forward_stack.set(Vec::new());
        }
    };

    let new_session = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let cur = active_root.get_untracked();
        spawn_local(async move {
            match post_json_value(&b, &t, "/v1/roots", serde_json::json!({})).await {
                Ok(created) => {
                    let id = created
                        .get("id")
                        .and_then(|v| v.as_str())
                        .unwrap_or("")
                        .to_string();
                    if !id.is_empty() {
                        if !cur.is_empty() && cur != id {
                            back_stack.update(|s| s.push(cur));
                            forward_stack.set(Vec::new());
                        }
                        let _ = post_json(
                            &b,
                            &t,
                            &format!("/v1/roots/{id}/activate"),
                            serde_json::json!({}),
                        )
                        .await;
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
            if let Err(e) = post_json(
                &b,
                &t,
                &format!("/v1/roots/{id}/activate"),
                serde_json::json!({}),
            )
            .await
            {
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
        let (b, t, root) = (
            base.get_untracked(),
            token.get_untracked(),
            active_root.get_untracked(),
        );
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
        let (b, t, root) = (
            base.get_untracked(),
            token.get_untracked(),
            active_root.get_untracked(),
        );
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

    let stop_all = move || {
        let rows = activity.get_untracked();
        for row in rows {
            if is_running(&row.state) {
                stop_child(row.session_id);
            }
        }
    };

    let open_session = move |id: String, live: bool| {
        remember(&id);
        if live {
            activate_root(id);
        } else {
            resume_session(id);
        }
    };

    let travel = move |back: bool| {
        let id = {
            let mut popped = None;
            if back {
                back_stack.update(|s| popped = s.pop());
            } else {
                forward_stack.update(|s| popped = s.pop());
            }
            popped
        };
        let Some(id) = id else { return };
        let cur = active_root.get_untracked();
        if !cur.is_empty() {
            if back {
                forward_stack.update(|s| s.push(cur));
            } else {
                back_stack.update(|s| s.push(cur));
            }
        }
        let live = roots.get_untracked().iter().any(|r| r.id == id);
        if live {
            activate_root(id);
        } else {
            resume_session(id);
        }
    };

    let set_enter = move |mode: String| {
        enter_mode.set(mode.clone());
        save("strike.enterMode", &mode);
    };

    let set_perm = move |mode: String| {
        perm_mode.set(mode.clone());
        mode_menu.set(false);
        let root = active_root.get_untracked();
        if root.is_empty() {
            status.set("no live session yet".into());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Err(e) = post_op(
                &b,
                &t,
                &root,
                "set.permission_mode",
                serde_json::json!({"mode": mode}),
            )
            .await
            {
                status.set(format!("approval mode failed: {e}"));
            }
        });
    };

    let toggle_pin = move |id: String| {
        pins.update(|list| {
            if let Some(i) = list.iter().position(|p| p == &id) {
                list.remove(i);
            } else {
                list.push(id);
            }
        });
        save(
            "strike.pins",
            &serde_json::to_string(&pins.get_untracked()).unwrap_or_default(),
        );
    };

    let run_command = move |id: String| {
        palette_open.set(false);
        match id.as_str() {
            "new" => new_session(),
            "stop-all" => stop_all(),
            "chat" => mode.set("chat".into()),
            "daemon" => mode.set("daemon".into()),
            "steer" => set_enter("steer".into()),
            "queue" => set_enter("queue".into()),
            "mode-default" => set_perm("default".into()),
            "mode-plan" => set_perm("plan".into()),
            "mode-soft" => set_perm("soft-approve".into()),
            "mode-edits" => set_perm("accept-edits".into()),
            "mode-yolo" => set_perm("yolo".into()),
            _ => {}
        }
    };

    let run_picked = move || {
        let q = palette_query.get_untracked().to_ascii_lowercase();
        let items: Vec<_> = palette_cmds()
            .iter()
            .copied()
            .filter(|c| palette_match(c, &q))
            .collect();
        if let Some(cmd) = items.first() {
            run_command(cmd.id.to_string());
        }
    };

    let load_files = move || {
        let (b, t, path) = (
            base.get_untracked(),
            token.get_untracked(),
            files_path.get_untracked(),
        );
        spawn_local(async move {
            let q = format!("/v1/files?path={}", api::js_encode(&path));
            match get_json::<FilesResponse>(&b, &t, &q).await {
                Ok(r) => {
                    let mut entries = r.entries;
                    entries.sort_by(|a, b| b.is_dir.cmp(&a.is_dir).then(a.name.cmp(&b.name)));
                    files.set(entries);
                    files_error.set(String::new());
                }
                Err(e) => files_error.set(e),
            }
        });
    };

    let load_changes = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match get_json::<ChangedFilesResponse>(&b, &t, "/v1/changed-files").await {
                Ok(r) => {
                    changes.set(r.files);
                    changes_error.set(String::new());
                }
                Err(e) => changes_error.set(e),
            }
        });
    };

    Effect::new(move |_| {
        let tab = side_tab.get();
        let _ = files_path.get();
        if tab == "files" {
            load_files();
        } else if tab == "changes" {
            load_changes();
        }
    });

    let on_key =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            if (ev.meta_key() || ev.ctrl_key()) && ev.key().eq_ignore_ascii_case("k") {
                ev.prevent_default();
                palette_open.set(!palette_open.get_untracked());
                if palette_open.get_untracked() {
                    palette_query.set(String::new());
                }
            } else if ev.key() == "Escape" {
                palette_open.set(false);
                mode_menu.set(false);
                model_menu.set(false);
            }
        });
    if let Some(w) = web_sys::window() {
        let _ = w.add_event_listener_with_callback("keydown", on_key.as_ref().unchecked_ref());
    }
    on_key.forget();

    let open_models = move |_| {
        let open = !model_menu.get_untracked();
        mode_menu.set(false);
        model_menu.set(open);
        if !open {
            return;
        }
        let (b, t, provider) = (
            base.get_untracked(),
            token.get_untracked(),
            provider_name.get_untracked(),
        );
        spawn_local(async move {
            let path = if provider.is_empty() {
                "/v1/models".to_string()
            } else {
                format!("/v1/models?provider={}", api::js_encode(&provider))
            };
            match get_json::<ModelList>(&b, &t, &path).await {
                Ok(list) => {
                    catalog.set(list.models);
                    catalog_note.set(String::new());
                }
                Err(_) => {
                    catalog.set(Vec::new());
                    catalog_note
                        .set("Catalog unavailable. The chip still shows the live model.".into());
                }
            }
        });
    };

    let running_count = move || {
        activity
            .get()
            .iter()
            .filter(|r| is_running(&r.state))
            .count()
    };
    let blocked_count = move || {
        activity
            .get()
            .iter()
            .filter(|r| r.state == "blocked")
            .count()
    };

    view! {
        <header class="page-header">
            <div class="brand">"Strike" <small>{move || status.get()}</small></div>
            <span class=move || match gateway.get().as_str() {
                "connected" => "chip ok",
                "unreachable" => "chip bad",
                _ => "chip",
            }>{move || match gateway.get().as_str() {
                "connected" => "Gateway connected",
                "unreachable" => "Gateway unreachable",
                _ => "Gateway checking",
            }}</span>
            <button class="command-pill" on:click=move |_| {
                palette_query.set(String::new());
                palette_open.set(true);
            }>
                <span>"Run a command"</span>
                <kbd>"Ctrl+K"</kbd>
            </button>
            <div class="header-actions">
                <button class="secondary" disabled=move || back_stack.get().is_empty() on:click=move |_| travel(true)>"Back"</button>
                <button class="secondary" disabled=move || forward_stack.get().is_empty() on:click=move |_| travel(false)>"Forward"</button>
                <button class="secondary" on:click=move |_| focus.update(|v| *v = !*v)>{move || if focus.get() { "Exit focus" } else { "Focus" }}</button>
                <div class="segmented" role="tablist" aria-label="View">
                    <button class=move || if mode.get() == "chat" { "seg on" } else { "seg" } on:click=move |_| mode.set("chat".into())>"Chat"</button>
                    <button class=move || if mode.get() == "daemon" { "seg on" } else { "seg" } on:click=move |_| mode.set("daemon".into())>"Daemon"</button>
                </div>
            </div>
        </header>
        <Show when=move || palette_open.get()>
            <div class="palette-backdrop" on:click=move |_| palette_open.set(false)>
                <div class="palette" on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()>
                    <input
                        placeholder="Type a command"
                        prop:value=move || palette_query.get()
                        on:input=move |ev| palette_query.set(event_target_value(&ev))
                        on:keydown=move |ev: web_sys::KeyboardEvent| {
                            if ev.key() == "Enter" {
                                ev.prevent_default();
                                run_picked();
                            }
                        }
                    />
                    {move || {
                        let q = palette_query.get().to_ascii_lowercase();
                        let run = run_command;
                        let mut blocks: Vec<_> = Vec::new();
                        for section in ["Commands", "Apps", "Settings"] {
                            let items: Vec<_> = palette_cmds().iter().copied().filter(|c| c.section == section && palette_match(c, &q)).collect();
                            if items.is_empty() {
                                continue;
                            }
                            let heading = section.to_string();
                            blocks.push(view! { <h3 class="palette-section">{heading}</h3> }.into_any());
                            for cmd in items {
                                let id = cmd.id.to_string();
                                let label = cmd.label.to_string();
                                let detail = cmd.detail.to_string();
                                let run = run;
                                blocks.push(view! {
                                    <button class="palette-row" on:click=move |_| run(id.clone())>
                                        <span class="name">{label}</span>
                                        <span class="path">{detail}</span>
                                    </button>
                                }.into_any());
                            }
                        }
                        blocks
                    }}
                    <button on:click=move |_| run_picked()>"Run"</button>
                </div>
            </div>
        </Show>
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
            <div class=move || if focus.get() { "shell focus" } else { "shell" }>
                <aside class="rail">
                    <h2>"Workspaces"</h2>
                    <p class="hint">"Registered directories, not session folders. Opening one does not move the live engine."</p>
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
                    <div class="chips" role="tablist" aria-label="Session filter">
                        <button class=move || if session_filter.get() == "all" { "chip on" } else { "chip" } on:click=move |_| session_filter.set("all".into())>"All"</button>
                        <button class=move || if session_filter.get() == "unread" { "chip on" } else { "chip" } on:click=move |_| session_filter.set("unread".into())>"Unread"</button>
                        <button class=move || if session_filter.get() == "progress" { "chip on" } else { "chip" } on:click=move |_| session_filter.set("progress".into())>"In progress"</button>
                        <button class=move || if session_filter.get() == "pinned" { "chip on" } else { "chip" } on:click=move |_| session_filter.set("pinned".into())>"Pinned"</button>
                    </div>
                    <input class="search" placeholder="Search sessions" prop:value=move || session_query.get() on:input=move |ev| session_query.set(event_target_value(&ev)) />
                    <button class="secondary" on:click=move |_| new_session()>"New session"</button>
                    {move || {
                        let active = active_root.get();
                        let active_running = activity.get().iter().any(|r| is_running(&r.state));
                        let merged = merge_sessions(&roots.get(), &sessions.get(), &pins.get(), &seen.get(), &active, active_running);
                        let kids = children.get();
                        let titles: Vec<String> = kids.iter().map(|c| if c.title.is_empty() { short_id(&c.id) } else { c.title.clone() }).collect();
                        let query = session_query.get();
                        let filtered = filter_sessions(&merged, &session_filter.get(), &query, &active, &titles);
                        let show_older = older_open.get();
                        let (shown, hidden) = visible_sessions(&filtered, &query, &active, 6, show_older);
                        let can_fold = show_older && query.trim().is_empty() && filtered.len() > 6;
                        let open = open_session;
                        let pin = toggle_pin;
                        let acts = activity.get();
                        let rows = shown.into_iter().map(|row| {
                            let id = row.id.clone();
                            let id_btn = id.clone();
                            let id_pin = id.clone();
                            let live = row.live;
                            let busy = row.busy;
                            let unread = row.unread;
                            let pinned = row.pinned;
                            let label = if row.title.is_empty() { short_id(&id) } else { row.title.clone() };
                            let gutter = if busy { "gutter gutter-running" } else if unread { "gutter gutter-unread" } else { "gutter" };
                            let klass = if id == active { "session-row card on" } else { "session-row card" };
                            let row_kids: Vec<_> = if id == active { kids.clone() } else { Vec::new() };
                            let acts = acts.clone();
                            let pin_label = if pinned { "Unpin" } else { "Pin" };
                            view! {
                                <li class=klass>
                                    <span class=gutter></span>
                                    <button class="link" on:click=move |_| open(id_btn.clone(), live)>
                                        <span class="name">{label}</span>
                                        <span class="path">
                                            {if busy { "In progress" } else if unread { "Unread" } else if live { "Idle" } else { "Saved" }}
                                        </span>
                                    </button>
                                    <button class="secondary pin" on:click=move |ev: web_sys::MouseEvent| {
                                        ev.stop_propagation();
                                        pin(id_pin.clone());
                                    }>{pin_label}</button>
                                    <ul class="plain children">
                                        {row_kids.into_iter().map(|c| {
                                            let state = acts.iter().find(|r| r.session_id == c.id).map(|r| r.state.clone()).unwrap_or_default();
                                            let state_kind = state.clone();
                                            let state_label = state.clone();
                                            let child_label = if c.title.is_empty() { short_id(&c.id) } else { c.title };
                                            view! {
                                                <li class="child-row">
                                                    <span class="name">{child_label}</span>
                                                    <Show when=move || !state.is_empty()>
                                                        <Badge kind=state_kind.clone() label=state_label.clone() />
                                                    </Show>
                                                </li>
                                            }
                                        }).collect_view()}
                                    </ul>
                                </li>
                            }
                        }).collect_view();
                        let no_rows = rows.is_empty();
                        let has_older = hidden > 0;
                        view! {
                            <div>
                                <Show when=move || no_rows>
                                    <EmptyState title="No sessions match".into() body="New roots show up here. Pins and unread marks stay in this browser.".into() />
                                </Show>
                                <ul class="plain">{rows}</ul>
                                <Show when=move || has_older>
                                    <button class="secondary" on:click=move |_| older_open.set(true)>{format!("Older sessions ({hidden})")}</button>
                                </Show>
                                <Show when=move || can_fold>
                                    <button class="secondary" on:click=move |_| older_open.set(false)>"Fold older sessions"</button>
                                </Show>
                            </div>
                        }
                    }}
                    <p class="hint">"Unread and pinned are kept in this browser. Schedule, memory, knowledge, artifacts, and the task runner are not in this client."</p>
                </aside>
                <section class="chat">
                    <Show when=move || !load_error.get().is_empty()>
                        <div class="notice" role="alert">
                            <span>{move || load_error.get()}</span>
                            <button class="secondary" on:click=move |_| refresh_meta()>"Retry"</button>
                        </div>
                    </Show>
                    <Show when=move || !stream_note.get().is_empty()>
                        <p class="hint banner">{move || stream_note.get()}</p>
                    </Show>
                    <div class="transcript" aria-live="polite">
                        <Show when=move || loading.get()>
                            <Skeleton />
                        </Show>
                        <Show when=move || !loading.get() && lines.get().is_empty() && load_error.get().is_empty()>
                            <div class="empty-state">
                                <strong>{move || empty_headline(tick.get())}</strong>
                                <p>"Send a message, or start from a suggestion. Delegated children show up under the session and in Activity."</p>
                                <div class="suggestions">
                                    {SUGGESTIONS.into_iter().map(|text| {
                                        let label = text.to_string();
                                        let send_label = label.clone();
                                        let send = send_text;
                                        view! {
                                            <button type="button" class="suggestion" on:click=move |_| send(send_label.clone())>{label}</button>
                                        }
                                    }).collect_view()}
                                </div>
                            </div>
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
                    <div class="composer"
                        on:dragover=move |ev: web_sys::DragEvent| ev.prevent_default()
                        on:drop=move |ev: web_sys::DragEvent| {
                            ev.prevent_default();
                            if let Some(dt) = ev.data_transfer() {
                                if let Ok(text) = dt.get_data("text/plain") {
                                    let text = text.trim().to_string();
                                    if text.starts_with('@') {
                                        insert_mention(text.trim_start_matches('@').to_string());
                                    }
                                }
                            }
                        }
                    >
                        <div class="composer-tools">
                            <button type="button" class="chip" on:click=move |_| {
                                model_menu.set(false);
                                mode_menu.update(|v| *v = !*v);
                            }>{move || format!("Approval: {}", mode_label(&perm_mode.get()))}</button>
                            <button type="button" class="chip" on:click=open_models>{move || {
                                let model = model_name.get();
                                if model.is_empty() { "Model".into() } else { model }
                            }}</button>
                            <div class="segmented" role="group" aria-label="Enter while the session is working">
                                <button type="button" class=move || if enter_mode.get() == "steer" { "seg on" } else { "seg" } on:click=move |_| set_enter("steer".into())>"Steer"</button>
                                <button type="button" class=move || if enter_mode.get() == "queue" { "seg on" } else { "seg" } on:click=move |_| set_enter("queue".into())>"Queue"</button>
                            </div>
                        </div>
                        <Show when=move || mode_menu.get()>
                            <div class="menu">
                                {permission_modes().iter().map(|m| {
                                    let id = m.id.to_string();
                                    let label = m.label.to_string();
                                    let detail = m.detail.to_string();
                                    let set = set_perm;
                                    view! {
                                        <button class="palette-row" on:click=move |_| set(id.clone())>
                                            <span class="name">{label}</span>
                                            <span class="path">{detail}</span>
                                        </button>
                                    }
                                }).collect_view()}
                            </div>
                        </Show>
                        <Show when=move || model_menu.get()>
                            <div class="menu">
                                <p class="hint">{move || {
                                    let p = provider_name.get();
                                    let m = model_name.get();
                                    if m.is_empty() { "No live model reported yet.".into() } else if p.is_empty() { m } else { format!("{p} · {m}") }
                                }}</p>
                                <Show when=move || !model_note.get().is_empty()><p class="hint">{move || model_note.get()}</p></Show>
                                <Show when=move || !catalog_note.get().is_empty()><p class="hint">{move || catalog_note.get()}</p></Show>
                                {move || catalog.get().into_iter().map(|m| {
                                    let id = m.id.clone();
                                    let provider = if m.provider.is_empty() { provider_name.get() } else { m.provider.clone() };
                                    let label = if m.name.is_empty() { m.id.clone() } else { m.name.clone() };
                                    view! {
                                        <button class="palette-row" on:click=move |_| {
                                            model_menu.set(false);
                                            let (b, t, root, provider, id) = (base.get(), token.get(), active_root.get(), provider.clone(), id.clone());
                                            spawn_local(async move {
                                                if let Err(e) = post_op(&b, &t, &root, "select.model", serde_json::json!({"provider": provider, "model": id})).await {
                                                    status.set(format!("model change failed: {e}"));
                                                }
                                            });
                                        }>{label}</button>
                                    }
                                }).collect_view()}
                            </div>
                        </Show>
                        <form on:submit=move |ev| { ev.prevent_default(); send(); }>
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
                        <p class="hint">"While a turn is running, Steer adds your message to that turn. Queue waits until it finishes. Idle sessions send either way. File names from the Files tab are inserted as @path text."</p>
                    </div>
                </section>
                <aside class="side">
                    <div class="side-tabs" role="tablist" aria-label="Side panel">
                        <button class=move || if side_tab.get() == "activity" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("activity".into())>"Activity"</button>
                        <button class=move || if side_tab.get() == "files" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("files".into())>"Files"</button>
                        <button class=move || if side_tab.get() == "changes" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("changes".into())>"Changes"</button>
                    </div>
                    <Show when=move || side_tab.get() == "activity">
                    <div class="activity">
                    <header class="activity-head">
                        <h2>"Activity"</h2>
                        <button class="secondary" disabled=move || running_count() == 0 on:click=move |_| stop_all()>"Stop all"</button>
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
                    </div>
                    </Show>
                    <Show when=move || side_tab.get() == "files">
                        <div class="files">
                            <p class="hint">{move || if files_path.get().is_empty() { "Working directory".into() } else { files_path.get() }}</p>
                            <Show when=move || !files_error.get().is_empty()>
                                <div class="notice" role="alert">
                                    <span>{move || files_error.get()}</span>
                                    <button class="secondary" on:click=move |_| load_files()>"Retry"</button>
                                </div>
                            </Show>
                            <Show when=move || !files_path.get().is_empty()>
                                <button class="secondary" on:click=move |_| files_path.set(parent_path(&files_path.get()))>"Up"</button>
                            </Show>
                            <ul class="plain">
                                {move || files.get().into_iter().map(|entry| {
                                    let name = entry.name.clone();
                                    let path = join_path(&files_path.get(), &entry.name);
                                    let path_drag = path.clone();
                                    let path_click = path.clone();
                                    let is_dir = entry.is_dir;
                                    let label = if is_dir { format!("{name}/") } else { name };
                                    view! {
                                        <li>
                                            <button class="link file-row" draggable="true"
                                                on:dragstart=move |ev: web_sys::DragEvent| set_drag(&ev, &path_drag)
                                                on:click=move |_| {
                                                    if is_dir {
                                                        files_path.set(path_click.clone());
                                                    } else {
                                                        insert_mention(path_click.clone());
                                                    }
                                                }
                                            >{label}</button>
                                        </li>
                                    }
                                }).collect_view()}
                            </ul>
                            <p class="hint">"Click or drag a file to insert @path as text. This client does not expand that token into file contents."</p>
                        </div>
                    </Show>
                    <Show when=move || side_tab.get() == "changes">
                        <div class="changes">
                            <Show when=move || !changes_error.get().is_empty()>
                                <div class="notice" role="alert">
                                    <span>{move || changes_error.get()}</span>
                                    <button class="secondary" on:click=move |_| load_changes()>"Retry"</button>
                                </div>
                            </Show>
                            <Show when=move || changes.get().is_empty() && changes_error.get().is_empty()>
                                <EmptyState title="No changes".into() body="Git diffs for the daemon working directory show up here.".into() />
                            </Show>
                            <ul class="plain">
                                {move || changes.get().into_iter().map(|file| {
                                    let path = file.path.clone();
                                    let path_drag = path.clone();
                                    let path_click = path.clone();
                                    let label = format!("{path}  +{} -{}", file.added, file.deleted);
                                    view! {
                                        <li>
                                            <button class="link file-row" draggable="true"
                                                on:dragstart=move |ev: web_sys::DragEvent| set_drag(&ev, &path_drag)
                                                on:click=move |_| insert_mention(path_click.clone())
                                            >{label}</button>
                                        </li>
                                    }
                                }).collect_view()}
                            </ul>
                        </div>
                    </Show>
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
    let policy_rules = RwSignal::new(String::new());
    let policy_mode = RwSignal::new(String::new());
    let policy_for = RwSignal::new(String::new());
    Effect::new(move |_| {
        let id = active_workspace.get();
        let list = workspaces.get();
        if policy_for.get_untracked() != id {
            if let Some(w) = list.iter().find(|w| w.id == id) {
                policy_rules.set(w.rules.clone());
                policy_mode.set(w.permission_mode.clone());
            } else {
                policy_rules.set(String::new());
                policy_mode.set(String::new());
            }
            policy_for.set(id);
        }
    });
    let save_policy = move |_| {
        let id = active_workspace.get();
        if id.is_empty() {
            status.set("no active workspace".into());
            return;
        }
        let rules = policy_rules.get();
        let mode = policy_mode.get();
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match patch_json(
                &b,
                &t,
                &format!("/v1/workspaces/{id}"),
                serde_json::json!({
                    "rules": rules,
                    "permissionMode": mode,
                }),
            )
            .await
            {
                Ok(()) => {
                    policy_for.set(String::new());
                    refresh();
                }
                Err(e) => status.set(format!("workspace policy failed: {e}")),
            }
        });
    };
    let add_workspace = move |_| {
        let path = new_path.get();
        if path.trim().is_empty() {
            return;
        }
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match post_json(
                &b,
                &t,
                "/v1/workspaces",
                serde_json::json!({ "path": path }),
            )
            .await
            {
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
        save(
            "strike.remotes",
            &serde_json::to_string(&remotes.get()).unwrap_or_default(),
        );
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
                                    <span class="path">{w.path.clone()}</span>
                                    {(!w.rules.is_empty()).then(|| {
                                        let rules = w.rules.clone();
                                        view! { <span class="path">{format!("rules {rules}")}</span> }
                                    })}
                                    {(!w.permission_mode.is_empty()).then(|| {
                                        let mode = w.permission_mode.clone();
                                        view! { <span class="path">{format!("mode {mode}")}</span> }
                                    })}
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
                <h2>"Policy"</h2>
                <p class="hint">"Rules must be a file inside the active workspace. The mode is stored here and is not applied to the live session."</p>
                <label>"Rules file"</label>
                <input placeholder="AGENTS.md"
                    prop:value=move || policy_rules.get()
                    on:input=move |ev| policy_rules.set(event_target_value(&ev)) />
                <label>"Permission mode"</label>
                <select class="mode" prop:value=move || policy_mode.get() on:change=move |ev| policy_mode.set(event_target_value(&ev))>
                    <option value="">"Unset"</option>
                    {permission_modes().iter().map(|m| {
                        let id = m.id.to_string();
                        let label = m.label.to_string();
                        view! { <option value=id>{label}</option> }
                    }).collect_view()}
                </select>
                <button on:click=save_policy>"Save policy"</button>
            </section>
        </main>
    }
}

fn main() {
    leptos::mount::mount_to_body(App);
}
