//! Strike web client. The session list is live roots plus saved sessions.
//! Activity, Files, Changes, Memory, Knowledge, and Artifacts are the side
//! panel. Side chat is a separate read-only drawer. Goals and Autopilot live
//! in `goals`. Schedule and the task runner are not drawn here; see
//! docs/webui-planned.md.

mod api;
mod chat_mode;
mod editor;
mod goals;
mod model;
mod notify;
mod model_menu;
mod model_menu_view;
mod storage;
mod transcript_find;
mod usage_audit;
mod shell;
mod side_chat;
mod status;

use crate::shell::icon;


use std::cell::{Cell, RefCell};
use std::collections::HashMap;
use std::rc::Rc;

use leptos::callback::Callback;
use leptos::prelude::*;
use leptos::task::spawn_local;
use model::{
    activity_panel, child_card_from_parts, child_card_from_text, child_keys_match, display_state,
    filter_sessions, fmt_ago, fmt_elapsed, is_running, merge_sessions, mode_label, rfc3339_to_ms,
    permission_modes, prefer_summary, protocol_dump, session_label, status_kind, strip_echo_tip,
    task_headline, visible_sessions, ActivityRow, Bootstrap, ChangedFilesResponse, ChatLine,
    ChildCard, ChildrenResponse, FilesResponse, Health, ModelInfo, ModelList, Remote, RootSummary,
    SessionItem, SessionsResponse, StatusBoard, TeamSnapshot, Workspace, WorkspacesResponse,
};
use protocol_schema::Event;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{
    get_json, idempotency_key, now_ms, patch_json, post_json, post_json_value, post_op, put_json,
};
use crate::storage::StoragePanel;
use crate::editor::{DocumentPane, LinkedBody, MarkdownView, has_markdown_table};
use crate::side_chat::SideChatPanel;

use crate::model::{budget_elapsed, can_retry, RootsResponse};
use crate::model_menu::{active_context, context_view, reduce_envelope, LiveModelState, ModelDefaults};
use crate::model_menu_view::ModelMenu;
use crate::status::{
    apply_document_chrome, elapsed_since, session_kind, StatusKind, StatusMark, StatusSlot,
    StatusTab, StatusTabs, HEARTBEAT_STALE_MS,
};

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

fn known_theme(id: &str) -> bool {
    matches!(
        id,
        "strike" | "nord" | "dracula" | "gruvbox" | "monokai" | "tokyo-night" | "catppuccin"
    )
}

fn theme_color(theme: &str, scheme: &str) -> &'static str {
    match (theme, scheme) {
        ("nord", "light") => "#eceff4",
        ("nord", _) => "#2e3440",
        ("dracula", "light") => "#f8f8f2",
        ("dracula", _) => "#282a36",
        ("gruvbox", "light") => "#fbf1c7",
        ("gruvbox", _) => "#282828",
        ("monokai", "light") => "#fafafa",
        ("monokai", _) => "#272822",
        ("tokyo-night", "light") => "#e1e2e7",
        ("tokyo-night", _) => "#1a1b26",
        ("catppuccin", "light") => "#eff1f5",
        ("catppuccin", _) => "#1e1e2e",
        (_, "light") => "#ffffff",
        _ => "#14131c",
    }
}

/// Applies a bundled TUI theme. Surfaces a theme file omits stay on the
/// Strike ladder for that scheme (see webui/style.css).
fn apply_theme(theme: &str, scheme: &str) {
    let theme = if known_theme(theme) { theme } else { "strike" };
    let scheme = if scheme == "light" { "light" } else { "dark" };
    if let Some(doc) = web_sys::window().and_then(|w| w.document()) {
        if let Some(el) = doc.document_element() {
            let _ = el.set_attribute("data-theme", theme);
            let _ = el.set_attribute("data-scheme", scheme);
        }
        if let Some(meta) = doc
            .query_selector("meta[name=\"theme-color\"]")
            .ok()
            .flatten()
        {
            let _ = meta.set_attribute("content", theme_color(theme, scheme));
        }
    }
    save("strike.theme", theme);
    save("strike.scheme", scheme);
}

fn apply_layout(layout: &str) {
    let layout = chat_mode::normalize_layout(layout);
    if let Some(doc) = web_sys::window().and_then(|w| w.document()) {
        if let Some(el) = doc.document_element() {
            let _ = el.set_attribute("data-layout", &layout);
        }
    }
    save("strike.layout", &layout);
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
    let icon_html = icon("status");
    view! {
        <div class="empty-state">
            <span class="icon-slot" aria-hidden="true" inner_html=icon_html></span>
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

#[allow(clippy::too_many_arguments)]
fn push_line(
    lines: RwSignal<Vec<ChatLine>>,
    seq: &Rc<Cell<u32>>,
    role: &str,
    title: &str,
    body: &str,
    badge: &str,
    at_ms: f64,
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
            detail: String::new(),
            key: String::new(),
            at_ms,
        });
    });
}

fn upsert_child(lines: RwSignal<Vec<ChatLine>>, seq: &Rc<Cell<u32>>, card: ChildCard) {
    if card.key.is_empty() {
        return;
    }
    let kind = status_kind(&card.status);
    let updated = lines
        .try_update(|list| {
            let Some(existing) = list
                .iter_mut()
                .rev()
                .find(|line| line.role == "child" && child_keys_match(&line.key, &card.key))
            else {
                return false;
            };
            if !card.name.is_empty() {
                existing.title = card.name.clone();
            }
            existing.body = prefer_summary(&existing.body, &card.summary);
            if card.details.len() >= existing.detail.len() && !card.details.is_empty() {
                existing.detail = card.details.clone();
            }
            if !kind.is_empty() {
                existing.badge = kind.clone();
            }
            if existing.at_ms <= 0.0 && card.at_ms > 0.0 {
                existing.at_ms = card.at_ms;
            }
            true
        })
        .unwrap_or(false);
    if updated {
        return;
    }
    let id = seq.get() + 1;
    seq.set(id);
    let title = if card.name.is_empty() {
        "Child".into()
    } else {
        card.name
    };
    lines.update(|list| {
        list.push(ChatLine {
            id,
            role: "child".into(),
            title,
            body: card.summary,
            badge: kind,
            detail: card.details,
            key: card.key,
            at_ms: card.at_ms,
        });
    });
}

fn absorb_noise(lines: RwSignal<Vec<ChatLine>>, seq: &Rc<Cell<u32>>) {
    let mut cards = Vec::new();
    lines.update(|list| {
        for line in list.iter_mut() {
            if line.role != "you" && line.role != "strike" {
                continue;
            }
            line.body = strip_echo_tip(&line.body);
            if let Some(mut card) = child_card_from_text(&line.body) {
                card.at_ms = line.at_ms;
                cards.push(card);
                line.body.clear();
            } else if let Some((summary, details)) = protocol_dump(&line.body) {
                cards.push(ChildCard {
                    key: format!("dump-{}", line.id),
                    name: "Details".into(),
                    status: String::new(),
                    summary,
                    details,
                    at_ms: line.at_ms,
                });
                line.body.clear();
            }
        }
        list.retain(|line| line.role == "child" || !line.body.trim().is_empty());
    });
    for card in cards {
        upsert_child(lines, seq, card);
    }
}

/// Header stamp for a transcript line (#1349): relative while the line is
/// under a day old (the session list's `fmt_ago` vocabulary), local clock
/// time after that. Empty when the line's envelope time is unknown.
fn line_stamp(at_ms: f64) -> String {
    if at_ms <= 0.0 {
        return String::new();
    }
    let now = now_ms();
    if now - at_ms < 86_400_000.0 {
        let rel = fmt_ago((at_ms / 1000.0) as i64, (now / 1000.0) as i64);
        if !rel.is_empty() {
            return rel;
        }
    }
    let d = js_sys::Date::new(&wasm_bindgen::JsValue::from_f64(at_ms));
    format!("{:02}:{:02}", d.get_hours() as u32, d.get_minutes() as u32)
}

/// Full local timestamp for the stamp's tooltip. Empty when unknown.
fn line_full_time(at_ms: f64) -> String {
    if at_ms <= 0.0 {
        return String::new();
    }
    js_sys::Date::new(&wasm_bindgen::JsValue::from_f64(at_ms))
        .to_string()
        .as_string()
        .unwrap_or_default()
}

/// Local calendar day of a line, as (year, month 0-based, day of month).
fn line_day(at_ms: f64) -> (u32, u32, u32) {
    let d = js_sys::Date::new(&wasm_bindgen::JsValue::from_f64(at_ms));
    (
        d.get_full_year() as u32,
        d.get_month() as u32,
        d.get_date() as u32,
    )
}

/// Separator label shown when the local day changes between lines.
fn line_day_label(at_ms: f64) -> String {
    const DAYS: [&str; 7] = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
    const MONTHS: [&str; 12] = [
        "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
    ];
    let d = js_sys::Date::new(&wasm_bindgen::JsValue::from_f64(at_ms));
    format!(
        "{} {} {}, {}",
        DAYS[(d.get_day() as usize) % 7],
        MONTHS[(d.get_month() as usize) % 12],
        d.get_date() as u32,
        d.get_full_year() as u32
    )
}

fn apply_envelope(
    lines: RwSignal<Vec<ChatLine>>,
    seq: &Rc<Cell<u32>>,
    open: &Rc<Cell<bool>>,
    storage_rev: RwSignal<u32>,
    raw: &str,
) {
    // Generated contract: unknown events are skipped, and a known event whose
    // payload does not match the schema is dropped. Rendering below still
    // reads the raw object.
    match protocol_schema::decode_event(raw) {
        Ok(Event::Unknown { .. }) | Err(_) => return,
        Ok(_) => {}
    }
    let v: serde_json::Value = match serde_json::from_str(raw) {
        Ok(v) => v,
        Err(_) => return,
    };
    let typ = v.get("type").and_then(|t| t.as_str()).unwrap_or("");
    // The envelope's `time` is the event's original time (also what the
    // JSONL log persists), so replayed lines keep their real timestamps.
    let at_ms = v
        .get("time")
        .and_then(|t| t.as_str())
        .map(rfc3339_to_ms)
        .unwrap_or(0.0);
    let data = v.get("data").cloned().unwrap_or(serde_json::Value::Null);
    let text = |key: &str| match data.get(key) {
        Some(serde_json::Value::String(s)) => s.clone(),
        Some(other) if !other.is_null() => other.to_string(),
        _ => String::new(),
    };
    match typ {
        "user.message" => {
            open.set(false);
            push_line(lines, seq, "you", "You", &text("text"), "", at_ms);
            absorb_noise(lines, seq);
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
            push_line(lines, seq, "strike", "Strike", &chunk, "", at_ms);
        }
        "turn.completed" => {
            open.set(false);
            absorb_noise(lines, seq);
        }
        "tool.begin" => {
            open.set(false);
            let name = text("name");
            push_line(lines, seq, "tool", &name, &text("args"), "tool", at_ms);
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
            push_line(lines, seq, "tool", &heading, &output, badge, at_ms);
        }
        "child.completed" => {
            let status = text("status");
            let name = text("name");
            let session = text("sessionId");
            let mut summary = text("summary");
            if summary.is_empty() {
                summary = data
                    .get("handoff")
                    .and_then(|h| h.get("summary"))
                    .and_then(|s| s.as_str())
                    .unwrap_or("")
                    .to_string();
            }
            let details = serde_json::to_string_pretty(&data).unwrap_or_default();
            let mut card = child_card_from_parts(&name, &session, &status, &summary, &details);
            card.at_ms = at_ms;
            upsert_child(lines, seq, card);
        }
        "artifact.updated" => storage_rev.update(|n| *n = n.wrapping_add(1)),
        _ => {}
    }
}

/// The body text find-in-transcript (#1342) may search for one line:
/// the rendered body, except long / JSON-ish tool bodies the transcript
/// folds into a collapsed <details> — those stay unsearchable in this
/// slice, matching the rendering rule in the transcript view below.
fn transcript_search_body(line: &ChatLine) -> &str {
    let card = line.role == "child";
    let prose = line.role == "you" || line.role == "strike";
    let body = &line.body;
    let tool_dump = !card
        && !prose
        && (body.trim_start().starts_with('{')
            || body.trim_start().starts_with('[')
            || body.chars().count() > 240);
    if tool_dump {
        ""
    } else {
        body
    }
}

/// All find-in-transcript matches over a line list, in transcript order.
fn transcript_matches(
    list: &[ChatLine],
    query: &str,
) -> Vec<transcript_find::TMatch> {
    let rows: Vec<transcript_find::SearchLine<'_>> = list
        .iter()
        .map(|line| transcript_find::SearchLine {
            title: &line.title,
            body: transcript_search_body(line),
        })
        .collect();
    transcript_find::matches_for_lines(&rows, query)
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

fn memory_mode_label(mode: &str) -> &'static str {
    match mode {
        "incognito" => "Incognito",
        "temporary" => "Temporary",
        _ => "Normal",
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

fn apply_status_payload(
    roots: RwSignal<Vec<model::RootSummary>>,
    sessions: RwSignal<Vec<model::SessionItem>>,
    sess: &serde_json::Value,
) {
    let Some(id) = sess.get("sessionId").and_then(|v| v.as_str()) else {
        return;
    };
    if id.is_empty() {
        return;
    }
    let id = id.to_string();
    let state = sess
        .get("status")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    let unread = sess.get("unread").and_then(|v| v.as_bool()).unwrap_or(false);
    let since = sess.get("since").and_then(|v| v.as_i64()).unwrap_or(0);
    let last = sess
        .get("lastActivityAt")
        .and_then(|v| v.as_i64())
        .unwrap_or(0);
    roots.update(|rows| {
        if let Some(row) = rows.iter_mut().find(|row| row.id == id) {
            if !state.is_empty() {
                row.busy = state == "running" || state == "waiting";
                row.status = state.clone();
            }
            row.unread = Some(unread);
            if since > 0 {
                row.status_since = since;
            }
            if last > 0 {
                row.last_activity_at = last;
            }
        }
    });
    sessions.update(|rows| {
        if let Some(row) = rows.iter_mut().find(|row| row.id == id) {
            if !state.is_empty() {
                row.status = state.clone();
            }
            row.unread = Some(unread);
            if last > 0 {
                row.last_activity_at = last;
            }
        }
    });
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
    let gateway_kind = RwSignal::new(StatusKind::Idle);
    let gateway_label = RwSignal::new("Checking".to_string());
    let last_beat = RwSignal::new(0.0f64);
    let boot_ms = now_ms();
    let focus = RwSignal::new(false);
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
    let model_note_after = RwSignal::new(0i64);
    let effort_level = RwSignal::new(String::new());
    let context_used = RwSignal::new(0i64);
    let context_limit = RwSignal::new(0i64);
    let context_fit = RwSignal::new(String::new());
    let model_defaults = RwSignal::new(ModelDefaults::default());
    let catalog = RwSignal::new(Vec::<ModelInfo>::new());
    let catalog_note = RwSignal::new(String::new());
    let catalog_once = RwSignal::new(false);
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
    let memory_mode = RwSignal::new("normal".to_string());
    let memory_menu = RwSignal::new(false);
    let storage_rev = RwSignal::new(0u32);
    // Find in transcript (#1342): bar visibility, query, current match,
    // and the transcript scroll offset saved when the bar opened so
    // Escape / Close can restore it.
    let tf_open = RwSignal::new(false);
    let tf_query = RwSignal::new(String::new());
    let tf_idx = RwSignal::new(0usize);
    let tf_saved_scroll = RwSignal::new(0.0f64);
    let doc_path = RwSignal::new(String::new());
    let doc_line = RwSignal::new(0u32);
    let doc_diff = RwSignal::new(false);
    let doc_editing = RwSignal::new(false);
    let layout = RwSignal::new(chat_mode::normalize_layout(&load("strike.layout")));
    let sessions_drawer = RwSignal::new(false);
    let panel_drawer = RwSignal::new(false);
    let pending_files = RwSignal::new(Vec::<chat_mode::PendingAttachment>::new());
    let sending = RwSignal::new(false);
    let listening = RwSignal::new(false);
    let speech_ok = chat_mode::dictation_available();
    let reconnect_gen = RwSignal::new(0u32);
    let stream_epoch = RwSignal::new(0u32);
    let stream_fails = RwSignal::new(0u32);
    let stream_up = RwSignal::new(false);
    let file_input = NodeRef::<leptos::html::Input>::new();
    let side_open = RwSignal::new(false);
    let side_focus_id = RwSignal::new(String::new());
    let side_focus_name = RwSignal::new(String::new());
    let side_focus_root = RwSignal::new(String::new());
    Effect::new(move |_| {
        let root = active_root.get();
        if side_focus_root.get_untracked() != root {
            side_focus_id.set(String::new());
            side_focus_name.set(String::new());
            side_focus_root.set(root);
        }
    });

    let files_path = RwSignal::new(String::new());
    let files = RwSignal::new(Vec::<model::DirEntry>::new());
    let files_error = RwSignal::new(String::new());
    let changes = RwSignal::new(Vec::<model::ChangedFile>::new());
    let changes_error = RwSignal::new(String::new());
    apply_theme(&load("strike.theme"), &load("strike.scheme"));
    apply_layout(&layout.get_untracked());

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
                    let rows = rows_from_team(&prev, &team, now);
                    let rows = if rows.is_empty() {
                        activity_panel(
                            &[],
                            &children.get_untracked(),
                            &lines.get_untracked(),
                            &root,
                        )
                    } else {
                        rows
                    };
                    activity.set(rows);
                }
                Err(e) => team_note.set(e),
            }
        });
    };

    let load_model_catalog = move || {
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

    let refresh_meta = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let mut err = String::new();
            let mut gateway_up = false;
            match get_json::<Health>(&b, &t, "/health").await {
                Ok(h) if h.ok => {
                    let was = gateway.get_untracked();
                    let root_now = active_root.get_untracked();
                    gateway.set("connected".into());
                    last_beat.set(now_ms());
                    gateway_up = true;
                    // HTTP is up. If the event stream is still down after an
                    // outage, open it again now instead of waiting out backoff.
                    if was == "unreachable"
                        && !stream_up.get_untracked()
                        && !root_now.is_empty()
                        && same_origin(&b)
                    {
                        reconnect_gen.update(|n| *n = n.wrapping_add(1));
                    }
                    status.set(format!("Connected · {}", h.version));
                }
                Ok(_) => {
                    gateway.set("unreachable".into());
                    gateway_kind.set(StatusKind::Disconnected);
                    gateway_label.set("Unreachable".into());
                    status.set("Unreachable".into());
                    err = "The daemon answered, but it is not ok.".into();
                }
                Err(e) => {
                    gateway.set("unreachable".into());
                    gateway_kind.set(StatusKind::Disconnected);
                    gateway_label.set("Unreachable".into());
                    status.set("Unreachable".into());
                    err = format!("Could not reach the daemon: {e}");
                }
            }
            if gateway_up {
                match get_json::<StatusBoard>(&b, &t, "/v1/status/sessions").await {
                    Ok(board) => {
                        let kind = StatusKind::parse(&board.gateway);
                        gateway_kind.set(kind);
                        gateway_label.set(kind.label().into());
                    }
                    Err(e) => {
                        if err.is_empty() {
                            err = format!("Status board failed to load: {e}");
                        }
                    }
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
                            effort_level.set(st.effort);
                            context_used.set(st.context_used);
                            context_limit.set(st.context_limit);
                            context_fit.set(st.context_fit);
                            if !st.permission_mode.is_empty() {
                                perm_mode.set(st.permission_mode);
                            }
                            live_busy.set(st.busy);
                            live_busy_root.set(root);
                        }
                        if !catalog_once.get_untracked() {
                            catalog_once.set(true);
                            load_model_catalog();
                        }
                    }
                    Err(e) => model_note.set(format!("Live status unavailable: {e}")),
                }
            }
            if let Ok(defaults) = get_json::<ModelDefaults>(&b, &t, "/v1/settings").await {
                model_defaults.set(defaults);
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
            map.insert(id.clone(), mtime.max(now));
        });
        save(
            "strike.seen",
            &serde_json::to_string(&seen.get_untracked()).unwrap_or_default(),
        );
        roots.update(|rows| {
            if let Some(row) = rows.iter_mut().find(|row| row.id == id) {
                row.unread = Some(false);
            }
        });
        sessions.update(|rows| {
            if let Some(row) = rows.iter_mut().find(|row| row.id == id) {
                row.unread = Some(false);
            }
        });
        let (b, t, seen_id) = (base.get_untracked(), token.get_untracked(), id);
        spawn_local(async move {
            let path = format!("/v1/sessions/{}/seen", api::js_encode(&seen_id));
            let _ = post_json(&b, &t, &path, serde_json::json!({})).await;
        });
    };

    let tail: Rc<RefCell<Option<web_sys::EventSource>>> = Rc::new(RefCell::new(None));
    let seq = Rc::new(Cell::new(0u32));
    let open = Rc::new(Cell::new(false));
    let shown_root = RwSignal::new(String::new());
    let wipe_on_replay = RwSignal::new(false);
    let tail_fx = tail.clone();
    let seq_fx = seq.clone();
    let open_fx = open.clone();
    Effect::new(move |_| {
        let root = active_root.get();
        let b = base.get();
        let _retry = reconnect_gen.get();
        let epoch = stream_epoch.get_untracked().wrapping_add(1);
        stream_epoch.set(epoch);
        stream_up.set(false);
        if let Some(es) = tail_fx.borrow_mut().take() {
            es.close();
        }
        seq_fx.set(0);
        open_fx.set(false);
        // A new session must not keep the previous transcript. A reconnect of
        // the same session keeps the column until the daemon's log replay
        // arrives, then replaces it so lines are not doubled.
        let switching = shown_root.get_untracked() != root;
        shown_root.set(root.clone());
        if switching {
            lines.set(Vec::new());
            children.set(Vec::new());
            wipe_on_replay.set(false);
            // Find belongs to the session that was open (#1342).
            tf_open.set(false);
            tf_query.set(String::new());
            tf_idx.set(0);
        } else if !root.is_empty() {
            wipe_on_replay.set(true);
        }
        effort_level.set(String::new());
        context_used.set(0);
        context_limit.set(0);
        context_fit.set(String::new());
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
        let epoch_msg = epoch;
        let epochs_msg = stream_epoch;
        let on_msg =
            Closure::<dyn FnMut(web_sys::MessageEvent)>::new(move |ev: web_sys::MessageEvent| {
                if epochs_msg.get_untracked() != epoch_msg {
                    return;
                }
                if let Some(data) = ev.data().as_string() {
                    apply_envelope(lines_cb, &seq_cb, &open_cb, storage_rev, &data);
                    let prev = LiveModelState {
                        provider: provider_name.get_untracked(),
                        model: model_name.get_untracked(),
                        effort: effort_level.get_untracked(),
                        context_used: context_used.get_untracked(),
                        context_limit: context_limit.get_untracked(),
                        context_fit: context_fit.get_untracked(),
                        note: model_note.get_untracked(),
                        note_after: model_note_after.get_untracked(),
                    };
                    let next = reduce_envelope(prev, &data);
                    provider_name.set(next.provider);
                    model_name.set(next.model);
                    effort_level.set(next.effort);
                    context_used.set(next.context_used);
                    context_limit.set(next.context_limit);
                    context_fit.set(next.context_fit);
                    model_note.set(next.note);
                    model_note_after.set(next.note_after);
                }
            });
        es.set_onmessage(Some(on_msg.as_ref().unchecked_ref()));
        on_msg.forget();
        let up = stream_up;
        let fails_ok = stream_fails;
        let note_ok = stream_note;
        let gate_ok = gateway;
        let epoch_open = epoch;
        let epochs_open = stream_epoch;
        let lines_open = lines;
        let wipe_open = wipe_on_replay;
        let on_open = Closure::<dyn FnMut(web_sys::Event)>::new(move |_ev| {
            if epochs_open.get_untracked() != epoch_open {
                return;
            }
            fails_ok.set(0);
            up.set(true);
            note_ok.set(String::new());
            gate_ok.set("connected".into());
            if wipe_open.get_untracked() {
                wipe_open.set(false);
                lines_open.set(Vec::new());
            }
        });
        es.set_onopen(Some(on_open.as_ref().unchecked_ref()));
        on_open.forget();
        let es_err = es.clone();
        let up_err = stream_up;
        let fails_err = stream_fails;
        let epochs = stream_epoch;
        let note_err = stream_note;
        let bump = reconnect_gen;
        let epoch_err = epoch;
        let on_err = Closure::<dyn FnMut(web_sys::Event)>::new(move |_ev| {
            if epochs.get_untracked() != epoch_err {
                return;
            }
            es_err.close();
            up_err.set(false);
            let n = fails_err.get_untracked();
            fails_err.set(n.saturating_add(1));
            if n >= 3 {
                note_err.set("Reconnecting to the live session. If this stays up, open the page once with ?token= so the daemon cookie is set.".into());
            } else {
                note_err.set("Reconnecting to the live session…".into());
            }
            let delay = chat_mode::reconnect_delay_ms(n);
            let epoch_now = epoch;
            let epochs = epochs;
            let bump = bump;
            if let Some(win) = web_sys::window() {
                let tick = Closure::once(move || {
                    if epochs.get_untracked() == epoch_now {
                        bump.update(|g| *g = g.wrapping_add(1));
                    }
                });
                let _ = win.set_timeout_with_callback_and_timeout_and_arguments_0(
                    tick.as_ref().unchecked_ref(),
                    delay,
                );
                tick.forget();
            }
        });
        es.set_onerror(Some(on_err.as_ref().unchecked_ref()));
        on_err.forget();
        *tail_fx.borrow_mut() = Some(es);
    });

    let poll_tick = tick;
    let beat_sig = last_beat;
    let kind_sig = gateway_kind;
    let label_sig = gateway_label;
    let gateway_sig = gateway;
    let status_sig = status;
    let interval = Closure::<dyn FnMut()>::new(move || {
        let n = poll_tick.get_untracked().wrapping_add(1);
        poll_tick.set(n);
        let beat = beat_sig.get_untracked();
        let age = if beat > 0.0 {
            now_ms() - beat
        } else {
            now_ms() - boot_ms
        };
        if age > HEARTBEAT_STALE_MS {
            kind_sig.set(StatusKind::Disconnected);
            label_sig.set("Unreachable".into());
            gateway_sig.set("unreachable".into());
            status_sig.set("Unreachable".into());
        }
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

    let status_tail: Rc<RefCell<Option<web_sys::EventSource>>> = Rc::new(RefCell::new(None));
    let status_tail_fx = status_tail.clone();
    Effect::new(move |_| {
        let b = base.get();
        if let Some(es) = status_tail_fx.borrow_mut().take() {
            es.close();
        }
        if !same_origin(&b) {
            return;
        }
        let es = match web_sys::EventSource::new("/v1/status/events") {
            Ok(es) => es,
            Err(_) => return,
        };
        let beat = last_beat;
        let kind = gateway_kind;
        let label = gateway_label;
        let link = gateway;
        let roots_cb = roots;
        let sessions_cb = sessions;
        let on_msg =
            Closure::<dyn FnMut(web_sys::MessageEvent)>::new(move |ev: web_sys::MessageEvent| {
                // A buffered frame can arrive after /health has already failed.
                // Leave the chip on Unreachable until the next successful poll.
                if link.get_untracked() == "unreachable" {
                    return;
                }
                let Some(data) = ev.data().as_string() else {
                    return;
                };
                let v: serde_json::Value = match serde_json::from_str(&data) {
                    Ok(v) => v,
                    Err(_) => return,
                };
                beat.set(now_ms());
                if let Some(gw) = v.get("gateway").and_then(|g| g.as_str()) {
                    if !gw.is_empty() {
                        let parsed = StatusKind::parse(gw);
                        kind.set(parsed);
                        label.set(parsed.label().into());
                    }
                }
                let typ = v.get("type").and_then(|t| t.as_str()).unwrap_or("");
                if typ == "session.status" {
                    if let Some(sess) = v.get("session") {
                        apply_status_payload(roots_cb, sessions_cb, sess);
                    }
                } else if typ == "snapshot" {
                    if let Some(list) = v.get("sessions").and_then(|s| s.as_array()) {
                        for sess in list {
                            apply_status_payload(roots_cb, sessions_cb, sess);
                        }
                    }
                }
            });
        es.set_onmessage(Some(on_msg.as_ref().unchecked_ref()));
        on_msg.forget();
        *status_tail_fx.borrow_mut() = Some(es);
    });

    Effect::new(move |_| {
        let kind = gateway_kind.get();
        let root_rows = roots.get();
        let saved = sessions.get();
        let active = active_root.get();
        let mut n = 0usize;
        let mut live_ids = std::collections::HashSet::new();
        for row in &root_rows {
            live_ids.insert(row.id.clone());
            if row.unread == Some(true) && row.id != active {
                n += 1;
            }
        }
        for row in &saved {
            if row.unread == Some(true) && !live_ids.contains(&row.id) {
                n += 1;
            }
        }
        apply_document_chrome(kind, n);
    });

    let session_busy = move |root: &str| -> bool {
        roots.get_untracked().iter().any(|r| r.id == root && r.busy)
            || (live_busy.get_untracked() && live_busy_root.get_untracked() == root)
    };

    // Returns true only when the text was actually dispatched, so callers
    // can keep the draft when a context hold (or no live session) blocks it.
    let send_text = move |text: String| -> bool {
        let text = text.trim().to_string();
        if text.is_empty() {
            return false;
        }
        let held = {
            let ctx = active_context(
                &catalog.get_untracked(),
                &provider_name.get_untracked(),
                &model_name.get_untracked(),
            );
            context_view(
                context_used.get_untracked(),
                context_limit.get_untracked(),
                ctx,
                &context_fit.get_untracked(),
            )
        };
        if held.held {
            model_note.set(held.hold_message);
            return false;
        }
        let root = active_root.get_untracked();
        if root.is_empty() {
            status.set("no live session yet".into());
            return false;
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
        true
    };

    let send = move || {
        // Context-hold gate (mirrors send_text): when the context fit is
        // critical/full, do not dispatch and do not clear the draft or
        // pending files, so a held submit never loses typed text. This
        // gates both entry points (form submit and textarea Enter),
        // which call this shared closure unconditionally.
        let held = {
            let ctx = active_context(
                &catalog.get_untracked(),
                &provider_name.get_untracked(),
                &model_name.get_untracked(),
            );
            context_view(
                context_used.get_untracked(),
                context_limit.get_untracked(),
                ctx,
                &context_fit.get_untracked(),
            )
        };
        if held.held {
            model_note.set(held.hold_message);
            return;
        }
        if sending.get_untracked() {
            return;
        }
        let text = draft.get_untracked().trim().to_string();
        let files = pending_files.get_untracked();
        if files.iter().any(|f| f.error.is_empty() && f.data_b64.is_empty() && f.uploaded_ref.is_empty()) {
            status.set("Still reading an attachment.".into());
            return;
        }
        let ready = files.iter().any(|f| f.error.is_empty());
        if text.is_empty() && !ready {
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
        // Take the composer before the first await so a repeated Enter or a
        // second click cannot post the same attachments again.
        sending.set(true);
        pending_files.set(Vec::new());
        draft.set(String::new());
        if listening.get_untracked() {
            let _ = chat_mode::toggle_dictation(|_| {}, |_| {});
            listening.set(false);
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let restore = move |failed: Vec<chat_mode::PendingAttachment>, failed_text: String| {
            draft.update(|d| {
                *d = chat_mode::merge_failed_send(d, &failed_text);
            });
            pending_files.update(|rows| {
                *rows = chat_mode::merge_failed_files(failed.clone(), rows.clone());
            });
            sending.set(false);
        };
        spawn_local(async move {
            let mut updated = files;
            let mut images = Vec::new();
            for file in updated.iter_mut() {
                if !file.error.is_empty() {
                    continue;
                }
                if file.uploaded_ref.is_empty() {
                    match chat_mode::upload_attachment(&b, &t, &root, &file.name, &file.mime, &file.data_b64).await {
                        Ok(up) => {
                            file.uploaded_ref = up.reference.clone();
                            file.uploaded_kind = up.kind;
                            file.uploaded_sha = up.sha256;
                            if !up.name.is_empty() {
                                file.name = up.name;
                            }
                            if !up.mime.is_empty() {
                                file.mime = up.mime;
                            }
                            if up.bytes > 0 {
                                file.bytes = up.bytes as u64;
                            }
                            file.data_b64.clear();
                        }
                        Err(err) => {
                            status.set(format!("attachment failed: {err}"));
                            restore(updated, text);
                            return;
                        }
                    }
                }
                images.push(serde_json::json!({
                    "ref": file.uploaded_ref,
                    "mime": file.mime,
                    "kind": file.uploaded_kind,
                    "name": file.name,
                    "bytes": file.bytes,
                    "sha256": file.uploaded_sha,
                }));
            }
            let body = if images.is_empty() {
                serde_json::json!({"text": text})
            } else {
                serde_json::json!({"text": text, "images": images})
            };
            match post_op(&b, &t, &root, op, body).await {
                Ok(()) => {
                    for file in &updated {
                        chat_mode::revoke_preview(&file.preview);
                    }
                    sending.set(false);
                }
                Err(err) => {
                    status.set(format!("send failed: {err}"));
                    restore(updated, text);
                }
            }
        });
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

    let set_layout = move |next: String| {
        let next = chat_mode::normalize_layout(&next);
        layout.set(next.clone());
        apply_layout(&next);
        if next != "conversation" {
            sessions_drawer.set(false);
            panel_drawer.set(false);
        }
    };

    let set_theme = move |theme: String| {
        apply_theme(&theme, &load("strike.scheme"));
    };
    let set_scheme = move |scheme: String| {
        apply_theme(&load("strike.theme"), &scheme);
    };

    let set_memory = move |mode: String| {
        memory_menu.set(false);
        memory_mode.set(mode.clone());
        let root = active_root.get_untracked();
        if root.is_empty() {
            status.set("no live session yet".into());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/memory/mode?root={}", api::js_encode(&root));
            match put_json(&b, &t, &path, serde_json::json!({"mode": mode})).await {
                Ok(text) => {
                    if let Ok(v) = serde_json::from_str::<serde_json::Value>(&text) {
                        if let Some(saved) = v.get("mode").and_then(|m| m.as_str()) {
                            if !saved.is_empty() {
                                memory_mode.set(saved.to_string());
                            }
                        }
                    }
                }
                Err(e) => status.set(format!("memory mode failed: {e}")),
            }
        });
    };

    let open_side = move |id: String, name: String| {
        side_focus_id.set(id);
        side_focus_name.set(name);
        side_open.set(true);
        mode.set("chat".into());

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

    let stream_up_online = stream_up;
    let reconnect_online = reconnect_gen;
    let on_online = Closure::<dyn FnMut(web_sys::Event)>::new(move |_ev| {
        if !stream_up_online.get_untracked() {
            reconnect_online.update(|n| *n = n.wrapping_add(1));
        }
    });
    if let Some(w) = web_sys::window() {
        let _ = w.add_event_listener_with_callback("online", on_online.as_ref().unchecked_ref());
    }
    on_online.forget();
    let session_busy_now = Memo::new(move |_| {
        let root = active_root.get();
        roots.get().iter().any(|r| r.id == root && r.busy)
            || (live_busy.get() && live_busy_root.get() == root)
    });

    Effect::new(move |_| {
        if model_menu.get() {
            mode_menu.set(false);
            load_model_catalog();
        }
    });

    Effect::new(move |_| {
        let root = active_root.get();
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            let path = format!("/v1/memory/mode?root={}", api::js_encode(&root));
            if let Ok(v) = get_json::<serde_json::Value>(&b, &t, &path).await {
                if let Some(mode) = v.get("mode").and_then(|m| m.as_str()) {
                    if !mode.is_empty() {
                        memory_mode.set(mode.to_string());
                    }
                }
            }
        });
    });

    let on_select_model = Callback::new(move |(provider, id): (String, String)| {
        model_note_after.set(now_ms() as i64);
        model_note.set(String::new());
        let (b, t, root) = (base.get_untracked(), token.get_untracked(), active_root.get_untracked());
        spawn_local(async move {
            if let Err(e) = post_op(
                &b,
                &t,
                &root,
                "select.model",
                serde_json::json!({"provider": provider, "model": id}),
            )
            .await
            {
                model_note.set(format!("model change failed: {e}"));
                model_note_after.set(0);
            }
        });
    });
    let on_default_model = Callback::new(move |(provider, id): (String, String)| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match patch_json(
                &b,
                &t,
                "/v1/settings",
                serde_json::json!({"provider": provider, "model": id}),
            )
            .await
            {
                Ok(()) => {
                    model_defaults.update(|d| {
                        d.provider = provider;
                        d.model = id;
                    });
                    model_note.set("Saved as the global default. This session stays on its current model until you pick one.".into());
                }
                Err(e) => model_note.set(format!("saving the default failed: {e}")),
            }
        });
    });
    let on_effort = Callback::new(move |level: String| {
        model_note_after.set(now_ms() as i64);
        model_note.set(String::new());
        let (b, t, root) = (base.get_untracked(), token.get_untracked(), active_root.get_untracked());
        spawn_local(async move {
            if let Err(e) = post_op(
                &b,
                &t,
                &root,
                "set.effort",
                serde_json::json!({"level": level}),
            )
            .await
            {
                model_note.set(format!("effort change failed: {e}"));
                model_note_after.set(0);
            }
        });
    });
    let on_default_effort = Callback::new(move |level: String| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match patch_json(
                &b,
                &t,
                "/v1/settings",
                serde_json::json!({"effort": level}),
            )
            .await
            {
                Ok(()) => {
                    model_defaults.update(|d| d.effort = level);
                    model_note.set("Saved the effort as the global default.".into());
                }
                Err(e) => model_note.set(format!("saving the effort default failed: {e}")),
            }
        });
    });

    // #1267 navbar hosts this bell. The component owns its panel and settings.
    let on_notify = Callback::new(move |target: notify::FocusTarget| {
        mode.set("chat".into());
        focus.set(false);
        if target.session_id.is_empty() {
            return;
        }
        let live = roots
            .get_untracked()
            .iter()
            .any(|r| r.id == target.session_id);
        open_session(target.session_id, live);
    });

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

    // Conversation layout (#1285): the shell contribution in
    // src/shell/contrib/chat_mode.rs renders the header toggle and the
    // palette commands through this late-bound host.
    chat_mode::set_host(chat_mode::LayoutHost {
        layout,
        set_layout: Rc::new(move |next: &str| set_layout(next.to_string())),
    });
    // The shell slot (src/shell/contrib/notifications.rs) renders the bell
    // with the page's signals; install them before the view reads them.
    notify::set_host(notify::NotifyHost {
        base,
        token,
        gateway,
        on_focus: on_notify,
    });

    shell::boot();
    shell::set_host(shell::HostBridge {
        new_session: Rc::new(move || new_session()),
        stop_all: Rc::new(move || stop_all()),
        set_view: Rc::new(move |next: &str| mode.set(next.to_string())),
        view: Rc::new(move || mode.get()),
        set_enter: Rc::new(move |next: &str| set_enter(next.to_string())),
        set_perm: Rc::new(move |next: &str| set_perm(next.to_string())),
        set_theme: Rc::new(move |next: &str| set_theme(next.to_string())),
        set_scheme: Rc::new(move |next: &str| set_scheme(next.to_string())),
        travel: Rc::new(move |back: bool| travel(back)),
        can_back: Rc::new(move || !back_stack.get().is_empty()),
        can_forward: Rc::new(move || !forward_stack.get().is_empty()),
        toggle_focus: Rc::new(move || focus.update(|value| *value = !*value)),
        focus_on: Rc::new(move || focus.get()),
        workspace_name: Rc::new(move || {
            let id = active_workspace.get();
            if id.is_empty() {
                return "No workspace".into();
            }
            workspaces
                .get()
                .into_iter()
                .find(|workspace| workspace.id == id)
                .map(|workspace| {
                    if workspace.name.is_empty() {
                        workspace.path
                    } else {
                        workspace.name
                    }
                })
                .unwrap_or_else(|| "No workspace".into())
        }),
        session_name: Rc::new(move || {
            let id = active_root.get();
            if id.is_empty() {
                return "No session".into();
            }
            let active_running = activity.get().iter().any(|row| is_running(&row.state));
            let merged = merge_sessions(
                &roots.get(),
                &sessions.get(),
                &pins.get(),
                &seen.get(),
                &id,
                active_running,
            );
            match merged.into_iter().find(|row| row.id == id) {
                Some(row) => session_label(&row.title, &short_id(&row.id)),
                None => short_id(&id),
            }
        }),
        session_kind: Rc::new(move || {
            let id = active_root.get();
            if id.is_empty() || !session_busy(&id) {
                "idle".into()
            } else {
                "running".into()
            }
        }),
        gateway: Rc::new(move || gateway.get()),
        version_label: Rc::new(move || status.get()),
        dismiss_popups: Rc::new(move || {
            // Escape semantics carried over from the pre-shell key handler:
            // close the menus and the side chat drawer; only when none of
            // them was open does Escape also close the open document.
            // The conversation layout's drawers (#1285) close on the same
            // Escape, as they did in this feature's own key handler.
            let menu = mode_menu.get_untracked()
                || model_menu.get_untracked()
                || memory_menu.get_untracked()
                || side_open.get_untracked();
            mode_menu.set(false);
            model_menu.set(false);
            memory_menu.set(false);
            side_open.set(false);
            sessions_drawer.set(false);
            panel_drawer.set(false);
            if !menu && !doc_editing.get_untracked() && !doc_path.get_untracked().is_empty() {
                doc_path.set(String::new());
                doc_editing.set(false);
            }
        }),
        open_side_chat: Rc::new(move || open_side(String::new(), String::new())),
        toggle_side_chat: Rc::new(move || side_open.update(|v| *v = !*v)),
        workspaces: Rc::new(move || {
            workspaces
                .get()
                .into_iter()
                .map(|workspace| {
                    let name = if workspace.name.is_empty() {
                        workspace.path
                    } else {
                        workspace.name
                    };
                    (workspace.id, name)
                })
                .collect()
        }),
        sessions: Rc::new(move || {
            let id = active_root.get();
            let active_running = activity.get().iter().any(|row| is_running(&row.state));
            merge_sessions(
                &roots.get(),
                &sessions.get(),
                &pins.get(),
                &seen.get(),
                &id,
                active_running,
            )
            .into_iter()
            .map(|row| {
                (
                    row.id.clone(),
                    session_label(&row.title, &short_id(&row.id)),
                )
            })
            .collect()
        }),
        open_session: Rc::new(move |id: &str| {
            let id = id.to_string();
            let live = roots.get().iter().any(|row| row.id == id);
            open_session(id, live);
        }),
        activate_workspace: Rc::new(move |id: &str| {
            let id = id.to_string();
            let (b, t) = (base.get(), token.get());
            spawn_local(async move {
                let path = format!("/v1/workspaces/{id}/activate");
                match post_json(&b, &t, &path, serde_json::json!({})).await {
                    Ok(()) => active_workspace.set(id),
                    Err(err) => status.set(format!("activate failed: {err}")),
                }
            });
        }),
    });
    crate::status::set_host(crate::status::StatusHost {
        gateway_kind,
        gateway_label,
        roots,
        active_root,
        refresh: Rc::new(move || refresh_meta()),
    });

    // Storage palette performers (#1271). The commands are registered at
    // boot by src/shell/contrib/storage.rs and dispatch these ids; only
    // the page owns the side-tab and memory-mode signals they drive.
    shell::bind(
        "panel-memory",
        Rc::new(move |_args: &str| {
            side_tab.set("memory".into());
            Ok(())
        }),
    );
    shell::bind(
        "panel-knowledge",
        Rc::new(move |_args: &str| {
            side_tab.set("knowledge".into());
            Ok(())
        }),
    );
    shell::bind(
        "panel-artifacts",
        Rc::new(move |_args: &str| {
            side_tab.set("artifacts".into());
            Ok(())
        }),
    );
    shell::bind(
        "memory-normal",
        Rc::new(move |_args: &str| {
            set_memory("normal".into());
            Ok(())
        }),
    );
    shell::bind(
        "memory-incognito",
        Rc::new(move |_args: &str| {
            set_memory("incognito".into());
            Ok(())
        }),
    );
    shell::bind(
        "memory-temporary",
        Rc::new(move |_args: &str| {
            set_memory("temporary".into());
            Ok(())
        }),
    );

    // Alt+G opens Goals. The shell owns the other chords; this one stays
    // here because only the page can set `mode`. The shortcut is listed in
    // the overlay via src/shell/contrib/goals.rs.
    let on_goals_key =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            if ev.alt_key()
                && !ev.meta_key()
                && !ev.ctrl_key()
                && ev.key().eq_ignore_ascii_case("g")
            {
                let typing = ev
                    .target()
                    .map(|t| {
                        t.dyn_ref::<web_sys::Element>()
                            .map(|el| {
                                matches!(el.tag_name().as_str(), "INPUT" | "TEXTAREA" | "SELECT")
                            })
                            .unwrap_or(false)
                    })
                    .unwrap_or(false);
                if !typing {
                    ev.prevent_default();
                    mode.set("goals".into());
                }
            }
        });
    if let Some(w) = web_sys::window() {
        let _ = w.add_event_listener_with_callback("keydown", on_goals_key.as_ref().unchecked_ref());
    }
    on_goals_key.forget();

    // Alt+U opens Usage and Alt+A opens Audit. Like Alt+G, these stay
    // here because only the page can set `mode`. The shortcuts are listed
    // in the overlay via src/shell/contrib/usage_audit.rs.
    let on_usage_audit_key =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            if ev.alt_key() && !ev.meta_key() && !ev.ctrl_key() {
                let target = if ev.key().eq_ignore_ascii_case("u") {
                    Some("usage")
                } else if ev.key().eq_ignore_ascii_case("a") {
                    Some("audit")
                } else {
                    None
                };
                if let Some(target) = target {
                    let typing = ev
                        .target()
                        .map(|t| {
                            t.dyn_ref::<web_sys::Element>()
                                .map(|el| {
                                    matches!(el.tag_name().as_str(), "INPUT" | "TEXTAREA" | "SELECT")
                                })
                                .unwrap_or(false)
                        })
                        .unwrap_or(false);
                    if !typing {
                        ev.prevent_default();
                        mode.set(target.into());
                    }
                }
            }
        });
    if let Some(w) = web_sys::window() {
        let _ = w.add_event_listener_with_callback("keydown", on_usage_audit_key.as_ref().unchecked_ref());
    }
    on_usage_audit_key.forget();
    // Alt+M opens the model menu. The shell owns the other chords; this
    // one stays here because only the page owns the menu signal, like
    // Alt+G above. The palette command and shortcut listing live in
    // src/shell/contrib/model_menu.rs; the command dispatches to the
    // binding below (the pattern docs/webui-shell.md describes for
    // commands whose state lives in a component).
    shell::bind(
        "model-menu.open",
        Rc::new(move |_args| {
            mode_menu.set(false);
            model_menu.update(|v| *v = !*v);
            Ok(())
        }),
    );
    let on_model_key =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            if ev.alt_key()
                && !ev.meta_key()
                && !ev.ctrl_key()
                && ev.key().eq_ignore_ascii_case("m")
            {
                ev.prevent_default();
                mode_menu.set(false);
                model_menu.update(|v| *v = !*v);
            }
        });
    if let Some(w) = web_sys::window() {
        let _ = w.add_event_listener_with_callback("keydown", on_model_key.as_ref().unchecked_ref());
    }
    on_model_key.forget();

    // Find in transcript (#1342). Opening saves the transcript scroll so
    // closing can restore it; stepping wraps around the match list.
    let open_find = move || {
        if !tf_open.get_untracked() {
            tf_saved_scroll.set(transcript_find::transcript_scroll());
        }
        tf_open.set(true);
        transcript_find::focus_search_soon();
    };
    let close_find = move || {
        if !tf_open.get_untracked() {
            return;
        }
        tf_open.set(false);
        tf_query.set(String::new());
        tf_idx.set(0);
        transcript_find::restore_scroll_soon(tf_saved_scroll.get_untracked());
    };
    let tf_step = move |delta: i32| {
        let list = lines.get_untracked();
        let hits = transcript_matches(&list, &tf_query.get_untracked());
        if hits.is_empty() {
            return;
        }
        tf_idx.set(transcript_find::step_idx(
            tf_idx.get_untracked(),
            delta,
            hits.len(),
        ));
    };
    // Palette command ("Find in transcript", registered in
    // src/shell/contrib/transcript_find.rs) dispatches here.
    shell::bind(
        "transcript-find.open",
        Rc::new(move |_args: &str| {
            if !tf_open.get_untracked() {
                tf_saved_scroll.set(transcript_find::transcript_scroll());
            }
            tf_open.set(true);
            transcript_find::focus_search_soon();
            Ok(())
        }),
    );
    // Ctrl/Cmd+F opens find, cockpit-wide while the chat view is up —
    // the issue's recommended default, matching the editor's "Ctrl+F
    // finds" model. When a document is open the editor pane keeps
    // Ctrl+F for find-in-file, so this handler stands down then.
    // This does collide with the browser's native find while the
    // cockpit is focused; that is the accepted trade-off of the
    // recommended default, and Escape restores the page.
    let on_find_key =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            if (ev.ctrl_key() || ev.meta_key())
                && !ev.alt_key()
                && ev.key().eq_ignore_ascii_case("f")
                && mode.get_untracked() == "chat"
                && doc_path.get_untracked().is_empty()
            {
                ev.prevent_default();
                if !tf_open.get_untracked() {
                    tf_saved_scroll.set(transcript_find::transcript_scroll());
                }
                tf_open.set(true);
                transcript_find::focus_search_soon();
            }
        });
    if let Some(w) = web_sys::window() {
        let _ = w.add_event_listener_with_callback("keydown", on_find_key.as_ref().unchecked_ref());
    }
    on_find_key.forget();
    // Keep the current match scrolled into view as the query or the
    // step position changes. Line appends alone do not retrigger this
    // (the list is read untracked), so streaming does not yank scroll.
    Effect::new(move |_| {
        let _ = tf_idx.get();
        let q = tf_query.get();
        if !tf_open.get_untracked() || q.is_empty() {
            return;
        }
        let list = lines.get_untracked();
        let hits = transcript_matches(&list, &q);
        if let Some(hit) = hits.get(transcript_find::clamped_idx(
            tf_idx.get_untracked(),
            hits.len(),
        )) {
            transcript_find::scroll_to_line_soon(hit.line);
        }
    });

    view! {
        <shell::Chrome />
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
        <Show when=move || mode.get() == "usage" fallback=move || ()>
            <usage_audit::UsagePage base=base token=token status=status />
        </Show>
        <Show when=move || mode.get() == "audit" fallback=move || ()>
            <usage_audit::AuditPage base=base token=token status=status />
        </Show>
        <Show when=move || mode.get() == "goals" fallback=move || ()>
            <goals::GoalsPane base=base token=token />
        </Show>
        <Show when=move || mode.get() == "chat">
            <div class=move || chat_mode::shell_class(focus.get(), &layout.get(), sessions_drawer.get(), panel_drawer.get())>
                <button type="button" class="chat-scrim" aria-label="Close panel" on:click=move |_| {
                    sessions_drawer.set(false);
                    panel_drawer.set(false);
                }></button>
                <aside class="rail">
                    <div class="section-head">
                        <h2 class="panel-title"><span class="icon-slot" aria-hidden="true" inner_html=icon("sessions")></span>"Sessions"</h2>
                        <button class="secondary" on:click=move |_| new_session()>"New"</button>
                    </div>
                    <div class="chips" role="tablist" aria-label="Session filter">
                        <button class=move || if session_filter.get() == "all" { "chip on" } else { "chip" } on:click=move |_| session_filter.set("all".into())>"All"</button>
                        <button class=move || if session_filter.get() == "unread" { "chip on" } else { "chip" } on:click=move |_| session_filter.set("unread".into())>"Unread"</button>
                        <button class=move || if session_filter.get() == "progress" { "chip on" } else { "chip" } on:click=move |_| session_filter.set("progress".into())>"In progress"</button>
                        <button class=move || if session_filter.get() == "pinned" { "chip on" } else { "chip" } on:click=move |_| session_filter.set("pinned".into())>"Pinned"</button>
                    </div>
                    <input class="search" placeholder="Search sessions" prop:value=move || session_query.get() on:input=move |ev| session_query.set(event_target_value(&ev)) />
                    {move || {
                        let _ = tick.get();
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
                        let rows = shown.into_iter().map(|row| {
                            let id = row.id.clone();
                            let id_btn = id.clone();
                            let id_pin = id.clone();
                            let live = row.live;
                            let unread = row.unread;
                            let pinned = row.pinned;
                            let fallback = short_id(&id);
                            let label = session_label(&row.title, &fallback);
                            let now_s = (now_ms() / 1000.0) as i64;
                            let activity_s = if row.last_activity_at > 0 {
                                row.last_activity_at / 1000
                            } else {
                                row.mtime
                            };
                            let when = fmt_ago(activity_s, now_s);
                            let kind = session_kind(&row.status, row.busy, false, false);
                            let elapsed = elapsed_since(kind, row.status_since, now_ms() as i64);
                            let mut subtitle = if unread {
                                format!("{} · Unread", kind.label())
                            } else if !row.live && row.status.is_empty() {
                                "Saved".into()
                            } else {
                                kind.label().to_string()
                            };
                            if !when.is_empty() && elapsed > 0 {
                                subtitle = format!("{subtitle} · {when}");
                            }
                            let clock = if elapsed > 0 {
                                fmt_elapsed(elapsed)
                            } else {
                                when.clone()
                            };
                            let klass = if id == active { "session-row card on" } else { "session-row card" };
                            let pin_label = if pinned { "Unpin" } else { "Pin" };
                            let pin_class = if pinned { "pin on" } else { "pin" };
                            view! {
                                <li class=klass>
                                    <StatusMark kind=kind place=StatusSlot::Gutter unread=unread />
                                    <button class="link session-main" on:click=move |_| open(id_btn.clone(), live)>
                                        <span class="session-copy">
                                            <span class="session-top">
                                                <span class="name">{label}</span>
                                                <span class="time">{clock}</span>
                                            </span>
                                            <span class="path">{subtitle}</span>
                                        </span>
                                    </button>
                                    <button class=pin_class on:click=move |ev: web_sys::MouseEvent| {
                                        ev.stop_propagation();
                                        pin(id_pin.clone());
                                    }>{pin_label}</button>
                                </li>
                            }
                        }).collect_view();
                        let no_rows = rows.is_empty();
                        let has_older = hidden > 0;
                        view! {
                            <div>
                                <Show when=move || no_rows>
                                    <EmptyState title="No sessions match".into() body="New roots show up here. Pins stay in this browser. Unread is stored on the daemon.".into() />
                                </Show>
                                <ul class="plain sessions">{rows}</ul>
                                <Show when=move || has_older>
                                    <button class="secondary" on:click=move |_| older_open.set(true)>{format!("Older sessions ({hidden})")}</button>
                                </Show>
                                <Show when=move || can_fold>
                                    <button class="secondary" on:click=move |_| older_open.set(false)>"Fold older sessions"</button>
                                </Show>
                            </div>
                        }
                    }}
                    <p class="hint keys">"Pins stay in this browser. Alt+Left goes back. Alt+Right goes forward. Alt+\\ hides the side panels."</p>
                    <details class="fold">
                    <summary>"Workspaces"</summary>
                    <p class="hint">"Opening one records it. It does not move the live engine."</p>
                    <Show when=move || workspaces.get().is_empty()>
                        <EmptyState title="No workspaces".into() body="Add a directory from Daemon.".into() />
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
                    </details>
                </aside>
                <section class="chat">
                    {move || {
                        let _ = tick.get();
                        let active = active_root.get();
                        let now = now_ms() as i64;
                        let open = open_session;
                        let tabs: Vec<StatusTab> = roots.get().into_iter().map(|row| {
                            let kind = session_kind(&row.status, row.busy, row.permission_pending, row.question_pending);
                            let fallback = short_id(&row.id);
                            StatusTab {
                                id: row.id.clone(),
                                label: session_label(&row.title, &fallback),
                                unread: row.unread.unwrap_or(false) && row.id != active,
                                elapsed_s: elapsed_since(kind, row.status_since, now),
                                active: row.id == active,
                                kind,
                            }
                        }).collect();
                        view! {
                            <StatusTabs tabs=tabs on_select=move |id: String| open(id, true) />
                        }
                    }}
                    <div class="chat-mode-bar">
                        <button type="button" class="chip" data-chat-sessions="true" on:click=move |_| {
                            panel_drawer.set(false);
                            sessions_drawer.update(|v| *v = !*v);
                        }>"Sessions"</button>
                        <span class="chat-mode-title">{move || {
                            let id = active_root.get();
                            if id.is_empty() {
                                return "No session".to_string();
                            }
                            sessions.get().into_iter().find(|s| s.id == id).map(|s| {
                                let fallback = short_id(&s.id);
                                session_label(&s.title, &fallback)
                            }).unwrap_or_else(|| short_id(&id))
                        }}</span>
                        <button type="button" class="chip" data-chat-panel="true" on:click=move |_| {
                            sessions_drawer.set(false);
                            panel_drawer.update(|v| *v = !*v);
                        }>"Panel"</button>
                        <button type="button" class="chip" data-tf-open="true" title="Find in transcript (Ctrl+F)" on:click=move |_| open_find()>"Find"</button>
                    </div>
                    <Show when=move || !load_error.get().is_empty()>
                        <div class="notice" role="alert">
                            <span>{move || load_error.get()}</span>
                            <button class="secondary" on:click=move |_| refresh_meta()>"Retry"</button>
                        </div>
                    </Show>
                    <Show when=move || !stream_note.get().is_empty()>
                        <p class="hint banner">{move || stream_note.get()}</p>
                    </Show>
                    <Show when=move || !doc_path.get().is_empty()>
                        <DocumentPane
                            base=base
                            token=token
                            path=doc_path
                            jump_line=doc_line
                            want_diff=doc_diff
                            editing=doc_editing
                        />
                    </Show>
                    <Show when=move || tf_open.get() && doc_path.get().is_empty()>
                        <div
                            class="tf-bar"
                            data-tf-bar="true"
                            role="search"
                            aria-label="Find in transcript"
                            on:keydown=move |ev: web_sys::KeyboardEvent| {
                                if ev.key() == "Escape" {
                                    ev.prevent_default();
                                    ev.stop_propagation();
                                    close_find();
                                }
                            }
                        >
                            <input
                                id="tf-search"
                                class="tf-search"
                                type="search"
                                placeholder="Find in transcript"
                                aria-label="Find in transcript"
                                prop:value=move || tf_query.get()
                                on:input=move |ev| {
                                    tf_query.set(event_target_value(&ev));
                                    tf_idx.set(0);
                                }
                                on:keydown=move |ev: web_sys::KeyboardEvent| {
                                    if ev.key() == "Enter" {
                                        ev.prevent_default();
                                        ev.stop_propagation();
                                        tf_step(if ev.shift_key() { -1 } else { 1 });
                                    }
                                }
                            />
                            <span class="tf-count" data-tf-count="">{move || {
                                let q = tf_query.get();
                                let list = lines.get();
                                let hits = transcript_matches(&list, &q);
                                transcript_find::count_label(&q, hits.len(), tf_idx.get())
                            }}</span>
                            <button type="button" class="secondary" data-tf-prev="true" on:click=move |_| tf_step(-1)>"Prev"</button>
                            <button type="button" class="secondary" data-tf-next="true" on:click=move |_| tf_step(1)>"Next"</button>
                            <button type="button" class="secondary" data-tf-close="true" on:click=move |_| close_find()>"Close"</button>
                        </div>
                    </Show>
                    <Show when=move || doc_path.get().is_empty()>
                    <div class="transcript" id="tf-transcript" aria-live="polite">
                        <Show when=move || loading.get()>
                            <Skeleton />
                        </Show>
                        <Show when=move || !loading.get() && lines.get().is_empty() && load_error.get().is_empty()>
                            <div class="empty-hero">
                                <strong>{move || empty_headline(tick.get())}</strong>
                                <p>"Send a message, or start from a suggestion."</p>
                                <div class="suggestions">
                                    {SUGGESTIONS.into_iter().map(|text| {
                                        let label = text.to_string();
                                        let send_label = label.clone();
                                        let send = send_text;
                                        view! {
                                            <button type="button" class="suggestion" on:click=move |_| { send(send_label.clone()); }>{label}</button>
                                        }
                                    }).collect_view()}
                                </div>
                            </div>
                        </Show>
                        {move || {
                            let list = lines.get();
                            // Find in transcript (#1342): matches over the
                            // whole line model (not just visible DOM), so
                            // off-screen matches count and highlight on
                            // render. The current match drives scrolling
                            // via the effect next to the Ctrl+F binding.
                            let tf_q = tf_query.get();
                            let find_active = !tf_q.is_empty();
                            let all_hits = transcript_matches(&list, &tf_q);
                            let cur_hit = all_hits
                                .get(transcript_find::clamped_idx(tf_idx.get(), all_hits.len()))
                                .copied();
                            // A day separator precedes the first timed line of
                            // each local day (#1349). Untimed lines never open
                            // a new day.
                            let mut prev_day: Option<(u32, u32, u32)> = None;
                            let seps: Vec<Option<String>> = list
                                .iter()
                                .map(|line| {
                                    if line.at_ms <= 0.0 {
                                        return None;
                                    }
                                    let day = line_day(line.at_ms);
                                    let label = if prev_day == Some(day) {
                                        None
                                    } else {
                                        Some(line_day_label(line.at_ms))
                                    };
                                    prev_day = Some(day);
                                    label
                                })
                                .collect();
                            list.into_iter().zip(seps).enumerate().map(|(line_idx, (line, sep))| {
                            let badge = line.badge.clone();
                            let badge_kind = badge.clone();
                            let role = line.role.clone();
                            let title = line.title.clone();
                            let body = line.body.clone();
                            let detail = line.detail.clone();
                            let stamp = line_stamp(line.at_ms);
                            let stamp_full = line_full_time(line.at_ms);
                            let has_stamp = !stamp.is_empty();
                            let has_badge = !badge.is_empty();
                            let has_detail = !detail.is_empty();
                            let card = role == "child";
                            let prose = role == "you" || role == "strike";
                            let tool_dump = !card && !prose && (body.trim_start().starts_with('{') || body.trim_start().starts_with('[') || body.chars().count() > 240);
                            let body_ranges: Vec<(usize, usize)> = all_hits
                                .iter()
                                .filter(|h| h.line == line_idx && h.field == transcript_find::Field::Body)
                                .map(|h| (h.start, h.end))
                                .collect();
                            let title_ranges: Vec<(usize, usize)> = all_hits
                                .iter()
                                .filter(|h| h.line == line_idx && h.field == transcript_find::Field::Title)
                                .map(|h| (h.start, h.end))
                                .collect();
                            let cur_body = cur_hit
                                .filter(|h| h.line == line_idx && h.field == transcript_find::Field::Body)
                                .map(|h| (h.start, h.end));
                            let cur_title = cur_hit
                                .filter(|h| h.line == line_idx && h.field == transcript_find::Field::Title)
                                .map(|h| (h.start, h.end));
                            let body_view = if card {
                                let summary = body;
                                let summary_inner = if find_active {
                                    view! { <transcript_find::MarkedText text=summary.clone() kind="inline" ranges=body_ranges current=cur_body /> }.into_any()
                                } else {
                                    view! { <LinkedBody text=summary.clone() kind="inline" doc_path=doc_path doc_line=doc_line doc_diff=doc_diff /> }.into_any()
                                };
                                view! {
                                    <div>
                                        <p class="summary">
                                            {summary_inner}
                                        </p>
                                        {has_detail.then(|| {
                                            let detail = detail.clone();
                                            view! {
                                                <details>
                                                    <summary>"Details"</summary>
                                                    <LinkedBody text=detail kind="pre" doc_path=doc_path doc_line=doc_line doc_diff=doc_diff />
                                                </details>
                                            }
                                        })}
                                    </div>
                                }.into_any()
                            } else if prose {
                                if find_active {
                                    // While finding, prose renders as
                                    // marked plain text so every match is
                                    // visible in place; the markdown /
                                    // linked renderers return when the
                                    // find bar clears.
                                    view! { <transcript_find::MarkedText text=body kind="prose" ranges=body_ranges current=cur_body /> }.into_any()
                                } else if has_markdown_table(&body) {
                                    // Table-bearing prose renders through the
                                    // shared markdown view (#1344); every other
                                    // bubble keeps the plain linked-text path.
                                    view! { <MarkdownView text=body /> }.into_any()
                                } else {
                                    view! { <LinkedBody text=body kind="prose" doc_path=doc_path doc_line=doc_line doc_diff=doc_diff /> }.into_any()
                                }
                            } else if tool_dump {
                                view! {
                                    <details>
                                        <summary>"Details"</summary>
                                        <LinkedBody text=body kind="pre" doc_path=doc_path doc_line=doc_line doc_diff=doc_diff />
                                    </details>
                                }.into_any()
                            } else if find_active {
                                view! { <transcript_find::MarkedText text=body kind="pre" ranges=body_ranges current=cur_body /> }.into_any()
                            } else {
                                view! { <LinkedBody text=body kind="pre" doc_path=doc_path doc_line=doc_line doc_diff=doc_diff /> }.into_any()
                            };
                            let title_view = if find_active && !title_ranges.is_empty() {
                                view! { <transcript_find::MarkedText text=title.clone() kind="inline" ranges=title_ranges current=cur_title /> }.into_any()
                            } else {
                                view! { <span>{title.clone()}</span> }.into_any()
                            };
                            view! {
                                {sep.map(|label| view! { <div class="day-sep" role="separator">{label}</div> })}
                                <article class=format!("bubble card role-{role}") id=format!("tf-line-{line_idx}")>
                                    <header>
                                        <span class="who">{title_view}</span>
                                        <Show when=move || has_badge>
                                            {match StatusKind::parse_known(&badge_kind) {
                                                Some(kind) => view! { <StatusMark kind=kind place=StatusSlot::Badge /> }.into_any(),
                                                None => view! { <Badge kind=badge_kind.clone() label=badge.clone() /> }.into_any(),
                                            }}
                                        </Show>
                                        {has_stamp.then(|| view! { <span class="stamp" title=stamp_full.clone()>{stamp.clone()}</span> })}
                                    </header>
                                    {body_view}
                                </article>
                            }
                            }).collect_view()
                        }}
                    </div>
                    </Show>
                    <Show when=move || layout.get() == "conversation" && {
                        // session_busy reads roots/live_busy untracked, so
                        // subscribe to them here: without this the Show never
                        // re-evaluates once the turn that produced the last
                        // line ends, and the follow-ups stay hidden.
                        let _ = roots.get();
                        let _ = live_busy.get();
                        let _ = live_busy_root.get();
                        let root = active_root.get();
                        !root.is_empty()
                            && !session_busy(&root)
                            && lines.with(|list| list.last().map(|line| line.role == "strike").unwrap_or(false))
                    }>
                        <div class="follow-ups" aria-label="Suggestions">
                            {chat_mode::FOLLOW_UPS.into_iter().map(|text| {
                                let label = text.to_string();
                                let send_label = label.clone();
                                let send = send_text;
                                view! {
                                    <button type="button" class="chip" title="Fixed prompt, not a model suggestion" on:click=move |_| { send(send_label.clone()); }>{label}</button>
                                }
                            }).collect_view()}
                        </div>
                    </Show>
                    <div class="composer"
                        on:dragover=move |ev: web_sys::DragEvent| ev.prevent_default()
                        on:drop=move |ev: web_sys::DragEvent| {
                            ev.prevent_default();
                            if let Some(dt) = ev.data_transfer() {
                                if let Some(files) = dt.files() {
                                    if files.length() > 0 {
                                        chat_mode::enqueue_file_list(files, pending_files, status);
                                        return;
                                    }
                                }
                                if let Ok(text) = dt.get_data("text/plain") {
                                    let text = text.trim().to_string();
                                    if text.starts_with('@') {
                                        insert_mention(text.trim_start_matches('@').to_string());
                                    }
                                }
                            }
                        }
                    >
                        <Show when=move || mode_menu.get()>
                            <div class="menu">
                                {permission_modes().iter().map(|m| {
                                    let id = m.id.to_string();
                                    let label = m.label.to_string();
                                    let detail = m.detail.to_string();
                                    let set = set_perm;
                                    let on = perm_mode.get() == id;
                                    let klass = if on { "palette-row on" } else { "palette-row" };
                                    view! {
                                        <button class=klass on:click=move |_| set(id.clone())>
                                            <span class="name">{label}</span>
                                            <span class="path">{detail}</span>
                                        </button>
                                    }
                                }).collect_view()}
                            </div>
                        </Show>
                        <Show when=move || {
                            let ctx = active_context(&catalog.get(), &provider_name.get(), &model_name.get());
                            context_view(context_used.get(), context_limit.get(), ctx, &context_fit.get()).held
                        }>
                            <p class="model-hold" role="status" data-context-hold="true">{move || {
                                let ctx = active_context(&catalog.get(), &provider_name.get(), &model_name.get());
                                context_view(context_used.get(), context_limit.get(), ctx, &context_fit.get()).hold_message
                            }}</p>
                        </Show>
                        <Show when=move || memory_menu.get()>
                            <div class="menu" role="menu" aria-label="Memory mode">
                                {["normal", "incognito", "temporary"].into_iter().map(|id| {
                                    let label = memory_mode_label(id).to_string();
                                    let detail = match id {
                                        "incognito" => "Reads memory. The agent learns nothing.",
                                        "temporary" => "This session does not load memory.",
                                        _ => "Reads and writes memory.",
                                    };
                                    let set = set_memory;
                                    let on = memory_mode.get() == id;
                                    let klass = if on { "palette-row on" } else { "palette-row" };
                                    let id = id.to_string();
                                    view! {
                                        <button type="button" class=klass on:click=move |_| set(id.clone())>
                                            <span class="name">{label}</span>
                                            <span class="path">{detail}</span>
                                        </button>
                                    }
                                }).collect_view()}
                            </div>
                        </Show>
                        <form on:submit=move |ev| { ev.prevent_default(); send(); }>
                            <Show when=move || !pending_files.get().is_empty()>
                                <div class="attach-row">
                                    {move || pending_files.get().into_iter().map(|file| {
                                        let id = file.id;
                                        let name = file.name.clone();
                                        let preview = file.preview.clone();
                                        let error = file.error.clone();
                                        let show_img = !preview.is_empty();
                                        let show_err = !error.is_empty();
                                        view! {
                                            <span class="attach-chip">
                                                <Show when=move || show_img>
                                                    <img src=preview.clone() alt="" />
                                                </Show>
                                                <span>
                                                    <span class="name">{name}</span>
                                                    <Show when=move || show_err>
                                                        <span class="path">{error.clone()}</span>
                                                    </Show>
                                                </span>
                                                <button type="button" aria-label="Remove attachment" on:click=move |_| {
                                                    pending_files.update(|rows| {
                                                        if let Some(i) = rows.iter().position(|row| row.id == id) {
                                                            chat_mode::revoke_preview(&rows[i].preview);
                                                            rows.remove(i);
                                                        }
                                                    });
                                                }>"Remove"</button>
                                            </span>
                                        }
                                    }).collect_view()}
                                </div>
                            </Show>
                            <input class="file-pick" type="file" data-chat-file="true" node_ref=file_input multiple=true
                                accept="image/png,image/jpeg,image/webp,image/gif,application/pdf,image/svg+xml,text/plain,application/zip,application/gzip"
                                on:change=move |_| {
                                    if let Some(el) = file_input.get() {
                                        if let Some(files) = el.files() {
                                            chat_mode::enqueue_file_list(files, pending_files, status);
                                        }
                                        el.set_value("");
                                    }
                                }
                            />
                            <textarea
                                placeholder="Message this session"
                                prop:value=move || draft.get()
                                on:input=move |ev| draft.set(event_target_value(&ev))
                                on:paste=move |ev: web_sys::Event| {
                                    let Some(ev) = ev.dyn_ref::<web_sys::ClipboardEvent>() else { return };
                                    if let Some(data) = ev.clipboard_data() {
                                        if let Some(files) = data.files() {
                                            if files.length() > 0 {
                                                ev.prevent_default();
                                                chat_mode::enqueue_file_list(files, pending_files, status);
                                            }
                                        }
                                    }
                                }
                                on:keydown=move |ev: web_sys::KeyboardEvent| {
                                    if ev.key() == "Enter" && !ev.shift_key() {
                                        ev.prevent_default();
                                        send();
                                    }
                                }
                            ></textarea>
                            <div class="composer-bar">
                                <button type="button" class="chip" data-chat-attach="true" on:click=move |_| {
                                    if let Some(el) = file_input.get() {
                                        el.click();
                                    }
                                }>"Attach"</button>
                                <Show when=move || speech_ok>
                                    <button type="button" data-chat-voice="true" title="Dictate with the browser speech API. Audio may be sent to the browser vendor." class=move || if listening.get() { "chip listening" } else { "chip" } on:click=move |_| {
                                        let draft_cb = draft;
                                        let status_cb = status;
                                        let listen_cb = listening;
                                        match chat_mode::toggle_dictation(
                                            move |text| {
                                                draft_cb.update(|d| {
                                                    if !d.is_empty() && !d.ends_with(' ') && !d.ends_with('\n') {
                                                        d.push(' ');
                                                    }
                                                    d.push_str(&text);
                                                });
                                            },
                                            move |err| {
                                                listen_cb.set(false);
                                                if !err.is_empty() {
                                                    status_cb.set(err);
                                                }
                                            },
                                        ) {
                                            Ok(on) => listen_cb.set(on),
                                            Err(err) => {
                                                listen_cb.set(false);
                                                status_cb.set(err);
                                            }
                                        }
                                    }>{move || if listening.get() { "Stop" } else { "Voice" }}</button>
                                </Show>
                                <button type="button" class="chip" on:click=move |_| {
                                    model_menu.set(false);
                                    memory_menu.set(false);
                                    mode_menu.update(|v| *v = !*v);
                                }>{move || format!("Approval · {}", mode_label(&perm_mode.get()))}</button>
                                <ModelMenu
                                    open=model_menu
                                    provider=provider_name
                                    model=model_name
                                    effort=effort_level
                                    context_used=context_used
                                    context_limit=context_limit
                                    context_fit=context_fit
                                    catalog=catalog
                                    catalog_note=catalog_note
                                    model_note=model_note
                                    defaults=model_defaults
                                    busy=session_busy_now
                                    on_select=on_select_model
                                    on_default_model=on_default_model
                                    on_effort=on_effort
                                    on_default_effort=on_default_effort
                                />
                                <button type="button" class="chip" aria-label="Memory mode" on:click=move |_| {
                                    mode_menu.set(false);
                                    model_menu.set(false);
                                    memory_menu.update(|v| *v = !*v);
                                }>{move || format!("Memory · {}", memory_mode_label(&memory_mode.get()))}</button>
                                <button type="button" class=move || if side_open.get() { "chip on side-chat-open-composer" } else { "chip side-chat-open-composer" } on:click=move |_| open_side(String::new(), String::new())>"Side chat"</button>
                                <button type="button" class=move || if enter_mode.get() == "steer" { "chip on" } else { "chip" } on:click=move |_| set_enter("steer".into())>"Steer"</button>
                                <button type="button" class=move || if enter_mode.get() == "queue" { "chip on" } else { "chip" } on:click=move |_| set_enter("queue".into())>"Queue"</button>
                                <button class="primary send" type="submit" disabled=move || {
                                    let ctx = active_context(&catalog.get(), &provider_name.get(), &model_name.get());
                                    context_view(context_used.get(), context_limit.get(), ctx, &context_fit.get()).held
                                }>"Send"</button>
                            </div>
                        </form>
                        <Show when=move || !model_note.get().is_empty() && !model_menu.get()>
                            <p class="hint" data-model-note="true">{move || model_note.get()}</p>
                        </Show>
                        <p class="hint">"Queue waits out the turn. Steer joins a running turn. run <command> uses the shell. @path is text."</p>
                    </div>
                </section>
                <aside class="side">
                    <div class="side-tabs" role="tablist" aria-label="Side panel">
                        <button class=move || if side_tab.get() == "activity" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("activity".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("activity")></span>"Activity"</button>
                        <button class=move || if side_tab.get() == "files" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("files".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("files")></span>"Files"</button>
                        <button class=move || if side_tab.get() == "changes" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("changes".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("changes")></span>"Changes"</button>
                        <button class=move || if side_tab.get() == "memory" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("memory".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("memory")></span>"Memory"</button>
                        <button class=move || if side_tab.get() == "knowledge" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("knowledge".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("knowledge")></span>"Knowledge"</button>
                        <button class=move || if side_tab.get() == "artifacts" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("artifacts".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("artifacts")></span>"Artifacts"</button>
                    </div>
                    <Show when=move || side_tab.get() == "activity">
                    <div class="activity">
                    <header class="activity-head">
                        <span class="hint">"Live tasks"</span>
                        <span class="row-actions">
                            <button type="button" class="secondary side-chat-open-activity" on:click=move |_| open_side(String::new(), String::new())>"Side chat"</button>
                            <button class="secondary" disabled=move || running_count() == 0 on:click=move |_| stop_all()>"Stop all"</button>
                        </span>
                    </header>
                        <div class="stats">
                        <StatCard label="Running".into()>{move || running_count().to_string()}</StatCard>
                        <StatCard label="Blocked".into()>{move || blocked_count().to_string()}</StatCard>
                        <StatCard label="Tasks".into()>{move || activity.get().len().to_string()}</StatCard>
                    </div>
                    <Show when=move || !team_note.get().is_empty()>
                        <p class="hint">{move || team_note.get()}</p>
                    </Show>
                    <Show when=move || activity.get().is_empty()>
                        <EmptyState
                            title="No live tasks".into()
                            body="Delegated work for this session shows its status here.".into()
                        />
                    </Show>
                    <ul class="plain">
                        {move || {
                            let _ = tick.get();
                            activity.get().into_iter().map(|row| {
                                let state = row.state.clone();
                                let started_ms = row.started_ms;
                                let server_elapsed = row.elapsed_s;
                                let elapsed = if started_ms <= 0.0 && server_elapsed <= 0 {
                                    String::new()
                                } else {
                                    let local = if started_ms <= 0.0 {
                                        server_elapsed
                                    } else {
                                        ((now_ms() - started_ms) / 1000.0) as i64
                                    };
                                    fmt_elapsed(local.max(server_elapsed))
                                };
                                let show_elapsed = !elapsed.is_empty();
                                let sid = row.session_id.clone();
                                let sid_stop = sid.clone();
                                let task_retry = row.task.clone();
                                let headline = task_headline(&row.task);
                                let show_task = !headline.is_empty();
                                let output = row.output.clone();
                                let expanded = row.expanded;
                                let running = is_running(&state);
                                let retry = can_retry(&state);
                                let name = row.name.clone();
                                let name_chat = name.clone();
                                let sid_chat = sid.clone();
                                let who = name.clone();
                                let agent = row.agent.clone();
                                let agent_label = agent.clone();
                                let primary = if show_task { headline } else { name.clone() };
                                let kind = StatusKind::parse_known(&state);
                                let status_label = if state == "stalled" {
                                    "Stalled".to_string()
                                } else if let Some(kind) = kind {
                                    kind.label().to_string()
                                } else {
                                    state.clone()
                                };
                                let mark_kind = kind.unwrap_or(StatusKind::Idle);
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
                                            <span class="task-line">
                                                <StatusMark kind=mark_kind place=StatusSlot::Dot />
                                                <span class="name">{primary}</span>
                                                <StatusMark kind=mark_kind place=StatusSlot::Badge label=status_label />
                                            </span>
                                            <span class="task-meta">
                                                <Show when=move || show_task>
                                                    <span class="path">{who.clone()}</span>
                                                </Show>
                                                <Show when=move || !agent.is_empty()>
                                                    <span class="path">{agent_label.clone()}</span>
                                                </Show>
                                                <Show when=move || show_elapsed>
                                                    <span class="elapsed">{elapsed.clone()}</span>
                                                </Show>
                                            </span>
                                        </button>
                                        <div class="row-actions">
                                            <button type="button" class="secondary" on:click={
                                                let sid_chat = sid_chat.clone();
                                                let name_chat = name_chat.clone();
                                                move |ev: web_sys::MouseEvent| {
                                                    ev.stop_propagation();
                                                    open_side(sid_chat.clone(), name_chat.clone());
                                                }
                                            }>"Side chat"</button>
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
                            <Show when=move || !doc_path.get().is_empty()>
                                <p class="hint">{move || format!("Open: {}", doc_path.get())}</p>
                            </Show>
                            <ul class="plain">
                                {move || files.get().into_iter().map(|entry| {
                                    let name = entry.name.clone();
                                    let path = join_path(&files_path.get(), &entry.name);
                                    let path_drag = path.clone();
                                    let path_click = path.clone();
                                    let path_open = path.clone();
                                    let is_dir = entry.is_dir;
                                    let label = if is_dir { format!("{name}/") } else { name.clone() };
                                    view! {
                                        <li class="ev-fileline">
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
                                            {(!is_dir).then(|| {
                                                let path_attr = path_open.clone();
                                                let path_set = path_open.clone();
                                                view! {
                                                    <button type="button" class="secondary ev-open" data-ev-open=path_attr on:click=move |_| {
                                                        doc_line.set(0);
                                                        doc_diff.set(false);
                                                        doc_path.set(path_set.clone());
                                                    }>"Open"</button>
                                                }
                                            })}
                                        </li>
                                    }
                                }).collect_view()}
                            </ul>
                            <p class="hint">"Open reads the file. Click or drag still inserts @path."</p>
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
                                    let path_open = path.clone();
                                    let added = file.added;
                                    let deleted = file.deleted;
                                    view! {
                                        <li class="ev-fileline">
                                            <button class="link file-row" draggable="true"
                                                on:dragstart=move |ev: web_sys::DragEvent| set_drag(&ev, &path_drag)
                                                on:click=move |_| insert_mention(path_click.clone())
                                            >
                                                <span class="name">{path.clone()}</span>
                                                <span class="diff-add">{format!("+{added}")}</span>
                                                <span class="diff-del">{format!("-{deleted}")}</span>
                                            </button>
                                            <button type="button" class="secondary ev-open" data-ev-open=path_open.clone() on:click=move |_| {
                                                doc_line.set(0);
                                                doc_diff.set(true);
                                                doc_path.set(path_open.clone());
                                            }>"Open"</button>
                                        </li>
                                    }
                                }).collect_view()}
                            </ul>
                        </div>
                    </Show>
                    <Show when=move || {
                        let tab = side_tab.get();
                        tab == "memory" || tab == "knowledge" || tab == "artifacts"
                    }>
                        <StoragePanel
                            tab=side_tab
                            base=base
                            token=token
                            root=active_root
                            memory_mode=memory_mode
                            status=status
                            storage_rev=storage_rev
                        />
                    </Show>
                </aside>
            </div>
            <SideChatPanel
                open=side_open
                base=base
                token=token
                root=active_root
                lines=lines
                focus_id=side_focus_id
                focus_name=side_focus_name
                live_provider=provider_name
                live_model=model_name
            />
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
                <h2 class="panel-title"><span class="icon-slot" aria-hidden="true" inner_html=icon("status")></span>"Daemon"</h2>
                <label>"Base URL (blank = this daemon)"</label>
                <input placeholder="http://100.64.0.5:8787"
                    prop:value=move || base.get()
                    on:input=move |ev| base.set(event_target_value(&ev)) />
                <label>"Token"</label>
                <input type="password" autocomplete="off"
                    prop:value=move || token.get()
                    on:input=move |ev| token.set(event_target_value(&ev)) />
                <button class="primary" on:click=connect>"Connect"</button>
                <p class="hint">"Same-origin live transcript uses the daemon cookie. Open /?token=… once if the stream closes under --auth."</p>
            </section>
            <section class="card">
                <h2 class="panel-title">"Saved daemons"</h2>
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
                <h2 class="panel-title">"Workspaces"</h2>
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
                    <button class="primary" on:click=add_workspace>"Add"</button>
                </div>
            </section>
            <section class="card">
                <h2 class="panel-title">"Policy"</h2>
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
                <button class="primary" on:click=save_policy>"Save policy"</button>
            </section>
        </main>
    }
}

fn main() {
    leptos::mount::mount_to_body(App);
}
