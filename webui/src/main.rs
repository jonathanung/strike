//! Strike web client. The session list is live roots plus saved sessions.
//! Activity, Files, Changes, Git, Worktrees, Memory, Knowledge,
//! Artifacts, and Terminal are the side panel. Side chat is a separate
//! read-only drawer. Goals and Autopilot live in `goals`. Apps is the
//! queue surface in `apps`. Triggers are a separate page. Schedule is
//! the clock (`schedule` module). The task runner is the Tasks view in
//! `task_runner`.

mod api;
mod apps;
mod approvals;
mod approvals_ui;
mod chat_mode;
mod composer_triggers;
mod editor;
mod gateway;
mod gateway_qr;
mod git;
mod goals;
mod mobile;
mod schedule;
mod model;
mod notify;
mod model_menu;
mod model_menu_view;
mod session_list;
mod session_panel;
mod storage;
mod task_runner;
mod transcript_find;
mod usage_audit;
mod session_delete;
mod shell;
mod side_chat;
mod status;
mod term;
mod terminal;
mod transcript;
mod transcript_view;
mod triggers;
mod turn_elapsed;
mod workspaces;

use crate::shell::icon;


use std::cell::{Cell, RefCell};
use std::collections::HashMap;
use std::rc::Rc;

use leptos::callback::Callback;
use composer_triggers::{
    file_suggestions, move_index, shell_hint, skill_suggestions,
    slash_suggestions, splice_draft, trigger_at, trigger_status, Kind, Suggestion,
};
use leptos::prelude::*;
use leptos::task::spawn_local;
use model::{
    activity_panel, child_card_from_parts, child_card_from_text, child_keys_match, display_state,
    fmt_ago, fmt_elapsed, is_running, merge_sessions, mode_label, prefer_summary,
    protocol_dump, rfc3339_to_ms, session_label, status_kind, strip_echo_tip, task_headline,
    ActivityRow, Bootstrap, ChangedFilesResponse, ChatLine, ChildCard, ChildrenResponse,
    ComposerCommand, FileSearchResponse, FilesResponse, Health, ModelInfo, ModelList, Remote, RootSummary,
    SessionItem, SessionsResponse, SkillInfo, StatusBoard, TeamSnapshot, Workspace, WorkspacesResponse,
};
use session_panel::{dispatch_session_key, SessionPanel};
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{
    get_json, get_text, idempotency_key, now_ms, patch_json, post_json, post_json_value, post_op,
    put_json,
};
use crate::approvals::{begin_session_mode, ModeChange};
use crate::approvals_ui::{
    apply_perm_notes, commit_session_mode, ApprovalCards, ApprovalHost, ApprovalMenu,
};
use crate::transcript::Transcript;
use crate::transcript_view::{save_download, TranscriptHandlers, TranscriptPane};
use crate::workspaces::WorkspacePicker;
use crate::storage::StoragePanel;
use crate::editor::DocumentPane;
use crate::mobile::PhoneNav;
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

pub(crate) fn load(key: &str) -> String {
    storage()
        .and_then(|s| s.get_item(key).ok().flatten())
        .unwrap_or_default()
}

pub(crate) fn save(key: &str, value: &str) {
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

/// SessionTitled is the daemon's title source (#1262 owns rename). Reflect it
/// on the open root so the list does not keep a raw id until the next poll.
fn textarea_state(target: Option<web_sys::EventTarget>) -> Option<(String, u32)> {
    let el = target?.dyn_into::<web_sys::HtmlTextAreaElement>().ok()?;
    let cursor = el.selection_start().ok().flatten().unwrap_or(0);
    Some((el.value(), cursor))
}

#[derive(Clone, Copy)]
struct TriggerSignals {
    ac_force: RwSignal<String>,
    ac_open: RwSignal<bool>,
    ac_index: RwSignal<usize>,
    ac_items: RwSignal<Vec<Suggestion>>,
    ac_replace_all: RwSignal<bool>,
    trig_start: RwSignal<usize>,
    trig_end: RwSignal<usize>,
    skills: RwSignal<Vec<SkillInfo>>,
    commands: RwSignal<Vec<ComposerCommand>>,
    search_gen: RwSignal<u32>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    draft: RwSignal<String>,
    plus_open: RwSignal<bool>,
    mode_menu: RwSignal<bool>,
    model_menu: RwSignal<bool>,
}

fn composer_caret() -> Option<u32> {
    let el = web_sys::window()?
        .document()?
        .query_selector("[data-feature=\"composer-triggers\"] textarea")
        .ok()
        .flatten()?;
    el.dyn_into::<web_sys::HtmlTextAreaElement>()
        .ok()?
        .selection_start()
        .ok()
        .flatten()
}

fn catalog_fingerprint(commands: &[ComposerCommand], skills: &[SkillInfo]) -> String {
    let mut out = String::new();
    for cmd in commands {
        out.push_str(&cmd.id);
        out.push('\n');
    }
    out.push('\u{1}');
    for skill in skills {
        out.push_str(&skill.name);
        out.push('\n');
        out.push_str(&skill.description);
        out.push('\n');
    }
    out
}

fn sync_composer_trigger(text: String, utf16: u32, ui: &TriggerSignals) {
    ui.plus_open.set(false);
    // Every entry invalidates an in-flight file search, including the path
    // that closes the popup. A late response must not reopen it.
    let gen = ui.search_gen.get_untracked().wrapping_add(1);
    ui.search_gen.set(gen);
    let force = ui.ac_force.get_untracked();
    if force == "skills" || force == "commands" {
        let mut items = if force == "skills" {
            skill_suggestions(&ui.skills.get_untracked(), text.trim())
        } else {
            slash_suggestions(
                &ui.commands.get_untracked(),
                &ui.skills.get_untracked(),
                text.trim(),
            )
        };
        for item in &mut items {
            item.replace_all = true;
        }
        if items.is_empty() {
            items.push(Suggestion {
                id: "empty-catalog".into(),
                label: if force == "skills" {
                    "No skills".into()
                } else {
                    "No commands".into()
                },
                detail: "Nothing in the daemon catalog matches.".into(),
                insert: String::new(),
                selectable: false,
                replace_all: false,
            });
        }
        ui.ac_replace_all.set(true);
        ui.ac_items.set(items);
        ui.ac_index.set(0);
        ui.ac_open.set(true);
        return;
    }
    let Some(trig) = trigger_at(&text, utf16) else {
        ui.ac_open.set(false);
        return;
    };
    ui.trig_start.set(trig.start);
    ui.trig_end.set(trig.end);
    ui.ac_replace_all.set(false);
    match trig.kind {
        Kind::Shell => {
            ui.ac_items.set(shell_hint(&trig.query));
            ui.ac_index.set(0);
            ui.ac_open.set(true);
        }
        Kind::Command => {
            let items = slash_suggestions(
                &ui.commands.get_untracked(),
                &ui.skills.get_untracked(),
                &trig.query,
            );
            ui.ac_open.set(!items.is_empty());
            ui.ac_index.set(0);
            ui.ac_items.set(items);
        }
        Kind::Skill => {
            let items = skill_suggestions(&ui.skills.get_untracked(), &trig.query);
            ui.ac_open.set(!items.is_empty());
            ui.ac_index.set(0);
            ui.ac_items.set(items);
        }
        Kind::File => {
            let q = trig.query.clone();
            let (b, t) = (ui.base.get_untracked(), ui.token.get_untracked());
            let search_gen = ui.search_gen;
            let ac_items = ui.ac_items;
            let ac_index = ui.ac_index;
            let ac_open = ui.ac_open;
            spawn_local(async move {
                let path = format!("/v1/files/search?q={}&limit=12", api::js_encode(&q));
                let items = match get_json::<FileSearchResponse>(&b, &t, &path).await {
                    Ok(found) => file_suggestions(&found.paths),
                    Err(_) => vec![Suggestion {
                        id: "search-failed".into(),
                        label: "File search failed".into(),
                        detail: "The daemon could not list files.".into(),
                        insert: String::new(),
                        selectable: false,
                        replace_all: false,
                    }],
                };
                if search_gen.get_untracked() != gen {
                    return;
                }
                let open = !items.is_empty();
                ac_items.set(items);
                ac_index.set(0);
                ac_open.set(open);
            });
        }
    }
}

fn accept_suggestion(item: Suggestion, ui: &TriggerSignals) {
    if !item.selectable || item.insert.is_empty() {
        return;
    }
    let next = if item.replace_all || ui.ac_replace_all.get_untracked() {
        item.insert
    } else {
        splice_draft(
            &ui.draft.get_untracked(),
            ui.trig_start.get_untracked(),
            ui.trig_end.get_untracked(),
            &item.insert,
        )
    };
    ui.draft.set(next);
    ui.ac_open.set(false);
    ui.plus_open.set(false);
    ui.ac_force.set(String::new());
}

fn open_composer_catalog(kind: &str, ui: &TriggerSignals) {
    ui.ac_force.set(kind.to_string());
    ui.plus_open.set(false);
    ui.mode_menu.set(false);
    ui.model_menu.set(false);
    let skills_menu = kind == "skills";
    let mut items = if skills_menu {
        skill_suggestions(&ui.skills.get_untracked(), "")
    } else {
        slash_suggestions(&ui.commands.get_untracked(), &ui.skills.get_untracked(), "")
    };
    for item in &mut items {
        item.replace_all = true;
    }
    if items.is_empty() {
        items.push(Suggestion {
            id: "empty-catalog".into(),
            label: if skills_menu {
                "No skills loaded".into()
            } else {
                "No commands loaded".into()
            },
            detail: "The daemon bootstrap did not list any.".into(),
            insert: String::new(),
            selectable: false,
            replace_all: false,
        });
    }
    ui.ac_replace_all.set(true);
    ui.ac_items.set(items);
    ui.ac_index.set(0);
    ui.ac_open.set(true);
}

fn mention_composer_file(ui: &TriggerSignals) {
    let current = ui.draft.get_untracked();
    let cursor = web_sys::window()
        .and_then(|w| w.document())
        .and_then(|doc| doc.active_element())
        .and_then(|el| el.dyn_into::<web_sys::HtmlTextAreaElement>().ok())
        .and_then(|el| el.selection_start().ok().flatten())
        .unwrap_or_else(|| current.encode_utf16().count() as u32);
    let byte = composer_triggers::utf16_to_byte(&current, cursor as usize);
    let next = splice_draft(&current, byte, byte, "@");
    ui.draft.set(next);
    ui.plus_open.set(false);
    ui.mode_menu.set(false);
    ui.model_menu.set(false);
    ui.ac_force.set(String::new());
    sync_composer_trigger(ui.draft.get_untracked(), cursor.saturating_add(1), ui);
}

fn prefix_composer_shell(ui: &TriggerSignals) {
    let current = ui.draft.get_untracked().trim().to_string();
    let next = if current.starts_with('!') {
        current
    } else {
        format!("!{current}")
    };
    ui.draft.set(next.clone());
    ui.plus_open.set(false);
    ui.mode_menu.set(false);
    ui.model_menu.set(false);
    ui.ac_force.set(String::new());
    let cursor = next.encode_utf16().count() as u32;
    sync_composer_trigger(next, cursor, ui);
}

fn note_live_title(
    sessions: RwSignal<Vec<SessionItem>>,
    roots: RwSignal<Vec<RootSummary>>,
    id: &str,
    raw: &str,
) {
    if id.is_empty() {
        return;
    }
    let Ok(v) = serde_json::from_str::<serde_json::Value>(raw) else {
        return;
    };
    if v.get("type").and_then(|t| t.as_str()) != Some("session.titled") {
        return;
    }
    let Some(title) = v
        .get("data")
        .and_then(|d| d.get("title"))
        .and_then(|t| t.as_str())
        .map(str::trim)
        .filter(|t| !t.is_empty())
    else {
        return;
    };
    let title = title.to_string();
    sessions.update(|list| {
        if let Some(row) = list.iter_mut().find(|row| row.id == id) {
            row.title = title.clone();
        }
    });
    roots.update(|list| {
        if let Some(row) = list.iter_mut().find(|row| row.id == id) {
            row.title = title;
        }
    });
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

pub(crate) fn load_string_list(key: &str) -> Vec<String> {
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

fn file_stem(id: &str) -> String {
    id.chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || c == '-' || c == '_' {
                c
            } else {
                '-'
            }
        })
        .take(48)
        .collect()
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
    let token = RwSignal::new(gateway::initial_token());
    let status = RwSignal::new("connecting…".to_string());
    let mode = RwSignal::new("chat".to_string());
    // The shell palette preventDefaults its keys. This signal stays false;
    // AppsPane also bails when the event is already default_prevented.
    let apps_palette_open = RwSignal::new(false);
    let workspaces = RwSignal::new(Vec::<Workspace>::new());
    let active_workspace = RwSignal::new(String::new());
    let gateway_open = RwSignal::new(false);
    let phone_open = RwSignal::new(false);
    let gateway_detail = RwSignal::new(String::new());
    let gateway_profiles = RwSignal::new(gateway::load_profiles());
    let gateway_selected = RwSignal::new("this".to_string());
    let workspace_recents = RwSignal::new(Vec::<Workspace>::new());
    let picker_open = RwSignal::new(false);
    let session_cwd = RwSignal::new(String::new());
    let roots = RwSignal::new(Vec::<RootSummary>::new());
    let sessions = RwSignal::new(Vec::<SessionItem>::new());
    let active_root = RwSignal::new(String::new());
    let lines = RwSignal::new(Vec::<ChatLine>::new());
    let transcript = RwSignal::new(Transcript::new());
    let ux_note = RwSignal::new(String::new());
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
    // Kept for the shell host bridge (palette session list / breadcrumb
    // label), which merges with the browser-local pins. The session panel
    // itself uses the server-backed list from /v1/session-list.
    let pins = RwSignal::new(load_string_list("strike.pins"));
    let seen = RwSignal::new(load_seen());
    let children = RwSignal::new(Vec::<model::ChildSession>::new());
    let perm_mode = RwSignal::new("default".to_string());
    let asks = RwSignal::new(Vec::<approvals::Ask>::new());
    let live_audit = RwSignal::new(Vec::<approvals::AuditLine>::new());
    let confirm = RwSignal::new(None::<approvals::PendingConfirm>);
    let panel_open = RwSignal::new(false);
    let reply_n = RwSignal::new(0u32);
    let reply_id = RwSignal::new(String::new());
    let reply_decision = RwSignal::new(String::new());
    let rules_doc = RwSignal::new(approvals::RulesDocument::default());
    let stored_audit = RwSignal::new(approvals::AuditDocument::default());
    let panel_error = RwSignal::new(String::new());
    let auto_seconds = RwSignal::new(0i32);
    let auto_exclude = RwSignal::new(Vec::<String>::new());
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
    let plus_open = RwSignal::new(false);
    let ac_open = RwSignal::new(false);
    let ac_index = RwSignal::new(0usize);
    let ac_items = RwSignal::new(Vec::<Suggestion>::new());
    let ac_force = RwSignal::new(String::new());
    let ac_replace_all = RwSignal::new(false);
    let trig_start = RwSignal::new(0usize);
    let trig_end = RwSignal::new(0usize);
    let skills = RwSignal::new(Vec::<SkillInfo>::new());
    let commands = RwSignal::new(Vec::<ComposerCommand>::new());
    let search_gen = RwSignal::new(0u32);
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
    // Session delete (#1358): the session the confirm dialog is armed
    // for. `None` means no deletion is pending; setting it never issues
    // a request by itself.
    let delete_target = RwSignal::new(None::<session_delete::DeleteTarget>);
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
    let phone_pane = RwSignal::new(mobile::PANE_CHAT.to_string());
    apply_theme(&load("strike.theme"), &load("strike.scheme"));
    apply_layout(&layout.get_untracked());
    Effect::new(move |_| {
        mobile::set_pane(&phone_pane.get());
    });
    // Overlays (#1475, #1476): one dismissal model for the Workspaces
    // dialog and the Gateways popover — changing the view or the phone
    // pane closes both, so a sheet can never be stranded on top of
    // another view. Escape and backdrop/outside tap live with the
    // overlays themselves (and in `dismiss_popups` below).
    Effect::new(move |prev: Option<(String, String)>| {
        let cur = (mode.get(), phone_pane.get());
        if prev.is_some() {
            picker_open.set(false);
            gateway_open.set(false);
        }
        cur
    });
    // Jump to chat when a document opens. A later Files or Activity tap
    // must stick while that document is still open.
    let doc_was_open = Rc::new(Cell::new(false));
    let doc_was = doc_was_open.clone();
    Effect::new(move |_| {
        let open = !doc_path.get().is_empty();
        if open && !doc_was.get() {
            phone_pane.set(mobile::PANE_CHAT.into());
        }
        doc_was.set(open);
    });

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
                    workspace_recents.set(r.recents);
                }
                Err(e) => {
                    if e.contains("HTTP 401") || e.contains("HTTP 403") {
                        gateway.set("unauthorized".into());
                    }
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
                            session_cwd.set(st.cwd);
                            live_busy.set(st.busy);
                            live_busy_root.set(root);
                        }
                        skills.set(boot.skills);
                        commands.set(boot.composer_commands);
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
    let live_cursor: Rc<Cell<u64>> = Rc::new(Cell::new(0));
    let shown_root = RwSignal::new(String::new());
    let wipe_on_replay = RwSignal::new(false);
    let tail_fx = tail.clone();
    let cursor_fx = live_cursor.clone();
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
        // A new session must not keep the previous transcript. A reconnect of
        // the same session keeps the column until the daemon's log replay
        // arrives, then replaces it so lines are not doubled.
        let switching = shown_root.get_untracked() != root;
        shown_root.set(root.clone());
        if switching {
            cursor_fx.set(0);
            lines.set(Vec::new());
            transcript.set(Transcript::new());
            ux_note.set(String::new());
            children.set(Vec::new());
            asks.set(Vec::new());
            live_audit.set(Vec::new());
            confirm.set(None);
            wipe_on_replay.set(false);
            // Find belongs to the session that was open (#1342).
            tf_open.set(false);
            tf_query.set(String::new());
            tf_idx.set(0);
        } else if mobile::resume_after(cursor_fx.get(), false).is_some() {
            // Same session, known SSE cursor: keep the column and ask
            // the daemon for events after that offset.
            wipe_on_replay.set(false);
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
        let mut url = format!("/v1/live/events?root={}", api::js_encode(&root));
        if let Some(after) = mobile::resume_after(cursor_fx.get(), switching) {
            url.push_str("&after=");
            url.push_str(&after.to_string());
        }
        let es = match web_sys::EventSource::new(&url) {
            Ok(es) => es,
            Err(e) => {
                stream_note.set(format!("live events failed: {e:?}"));
                return;
            }
        };
        let lines_cb = lines;
        let transcript_cb = transcript;
        let asks_cb = asks;
        let audit_cb = live_audit;
        let sessions_cb = sessions;
        let roots_cb = roots;
        let titled_root = root.clone();
        let epoch_msg = epoch;
        let epochs_msg = stream_epoch;
        let cursor_msg = cursor_fx.clone();
        let on_msg =
            Closure::<dyn FnMut(web_sys::MessageEvent)>::new(move |ev: web_sys::MessageEvent| {
                if epochs_msg.get_untracked() != epoch_msg {
                    return;
                }
                if let Ok(id) = ev.last_event_id().parse::<u64>() {
                    if id > 0 {
                        cursor_msg.set(id);
                    }
                }
                if let Some(data) = ev.data().as_string() {
                    note_live_title(sessions_cb, roots_cb, &titled_root, &data);
                    transcript_cb.update(|t| t.apply_envelope(&data));
                    apply_perm_notes(&data, asks_cb, perm_mode, audit_cb);
                    // Mirror into the ChatLine shape the side chat and the
                    // activity panel still consume.
                    lines_cb.set(transcript_cb.with_untracked(|t| t.chat_lines()));
                    let artifact_rev = transcript_cb.with_untracked(|t| t.artifact_updates);
                    if artifact_rev != storage_rev.get_untracked() {
                        storage_rev.set(artifact_rev);
                    }
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
                    model_note_after.set(next.note_after);                }
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
        let transcript_open = transcript;
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
                transcript_open.set(Transcript::new());
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

    // Listed title -> transcript (a durable rename or the daemon's title
    // fills the pane header; a pinned rename is not wiped by a replay).
    Effect::new(move |_| {
        let id = active_root.get();
        if id.is_empty() {
            return;
        }
        let from_session = sessions.with(|list| {
            list.iter()
                .find(|s| s.id == id)
                .map(|s| s.title.clone())
                .unwrap_or_default()
        });
        let from_root = roots.with(|list| {
            list.iter()
                .find(|r| r.id == id)
                .map(|r| r.title.clone())
                .unwrap_or_default()
        });
        let listed = if !from_session.is_empty() {
            from_session
        } else {
            from_root
        };
        if !listed.is_empty() {
            transcript.update(|t| t.set_listed_title(&listed));
        }
    });

    // Transcript title -> session/root lists (auto-title and renames show
    // in the session list without waiting for the next poll).
    Effect::new(move |_| {
        let title = transcript.with(|t| t.title.clone());
        let id = active_root.get();
        if title.is_empty() || id.is_empty() {
            return;
        }
        let session_stale = sessions.with_untracked(|list| {
            list.iter()
                .find(|s| s.id == id)
                .map(|s| s.title != title)
                .unwrap_or(false)
        });
        if session_stale {
            sessions.update(|list| {
                if let Some(s) = list.iter_mut().find(|s| s.id == id) {
                    s.title = title.clone();
                }
            });
        }
        let root_stale = roots.with_untracked(|list| {
            list.iter()
                .find(|r| r.id == id)
                .map(|r| r.title.is_empty() || (r.title != title && session_stale))
                .unwrap_or(false)
        });
        if root_stale {
            roots.update(|list| {
                if let Some(r) = list.iter_mut().find(|r| r.id == id && r.title.is_empty()) {
                    r.title = title;
                }
            });
        }
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
        ac_open.set(false);
        plus_open.set(false);
        ac_force.set(String::new());
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
        phone_pane.set(mobile::PANE_CHAT.into());
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
                    refresh_meta();
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
        phone_pane.set(mobile::PANE_CHAT.into());
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
        mode_menu.set(false);
        match begin_session_mode(&perm_mode.get_untracked(), &mode) {
            ModeChange::Same => {}
            ModeChange::Apply(mode) => {
                commit_session_mode(base, token, active_root, perm_mode, status, mode);
            }
            ModeChange::Confirm(pending) => confirm.set(Some(pending)),
        }
    };


    // Session delete (#1358): arm the confirm dialog for one session.
    // Shared by the row button and the palette command — one code path,
    // and arming the dialog is all either surface does.
    let request_delete = move |id: String, label: String| {
        delete_target.set(Some(session_delete::DeleteTarget { id, label }));
    };

    // After the daemon confirms a deletion: drop the session from every
    // local list, forget its pin/seen marks and history entries, and —
    // via `remaining_active` — fall back to the empty/list state when
    // the deleted session was the open one. Then re-read the lists from
    // the daemon so the cockpit shows the daemon's truth, not a guess.
    //
    // One composition is needed for that to hold: the session DELETE
    // removes the durable log, but a session that is also a live root
    // stays registered in the daemon's LiveHub (GET /v1/roots keeps
    // listing it, and a refresh would resurrect its row and reactivate
    // it). So when the deleted session was a live root, close that root
    // too (DELETE /v1/roots/{id} — LiveHub.Remove handles the active
    // root by activating another or none). The daemon gap behind this
    // is filed separately; this PR only composes the two existing
    // endpoints, in delete-then-close order, after the user's force
    // confirmation when one was required.
    let on_deleted = Callback::new(move |id: String| {
        let was_live = roots.get_untracked().iter().any(|row| row.id == id);
        pins.update(|list| list.retain(|p| p != &id));
        save(
            "strike.pins",
            &serde_json::to_string(&pins.get_untracked()).unwrap_or_default(),
        );
        seen.update(|map| {
            map.remove(&id);
        });
        save(
            "strike.seen",
            &serde_json::to_string(&seen.get_untracked()).unwrap_or_default(),
        );
        sessions.update(|rows| rows.retain(|row| row.id != id));
        roots.update(|rows| rows.retain(|row| row.id != id));
        back_stack.update(|stack| stack.retain(|entry| entry != &id));
        forward_stack.update(|stack| stack.retain(|entry| entry != &id));
        let cur = active_root.get_untracked();
        if session_delete::remaining_active(&cur, &id).is_none() && !cur.is_empty() {
            active_root.set(String::new());
        }
        status.set("Session deleted.".into());
        if was_live {
            let (b, t) = (base.get_untracked(), token.get_untracked());
            spawn_local(async move {
                let path = format!(
                    "/v1/roots/{}",
                    session_delete::encode_segment(&id)
                );
                if let Err(e) = crate::api::delete_json(&b, &t, &path, None).await {
                    status.set(format!(
                        "Session deleted, but closing its live root failed: {e}"
                    ));
                }
                refresh_meta();
            });
        } else {
            refresh_meta();
        }
    });

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

    // The shell owns the palette, travel, focus, and Escape chords;
    // this listener only forwards keys to the session panel (j/k/x,
    // Alt+1..9), whose handler guards modifiers and typing targets.
    let on_key =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            dispatch_session_key(&ev);
        });
    if let Some(w) = web_sys::window() {
        let _ = w.add_event_listener_with_callback("keydown", on_key.as_ref().unchecked_ref());
    }
    on_key.forget();

    {
        let tail_resume = tail.clone();
        let refresh = refresh_meta;
        let bump = reconnect_gen;
        mobile::install(move || {
            refresh();
            let needs = tail_resume
                .borrow()
                .as_ref()
                .map(|es| mobile::resume_reconnect(es.ready_state()))
                .unwrap_or(false);
            if needs {
                bump.update(|n| *n = n.wrapping_add(1));
            }
        });
    }

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
            load_model_catalog();        }
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
                || plus_open.get_untracked()
                || ac_open.get_untracked()
                || side_open.get_untracked()
                || phone_open.get_untracked()
                || gateway_open.get_untracked()
                || picker_open.get_untracked();
            mode_menu.set(false);
            model_menu.set(false);
            memory_menu.set(false);
            plus_open.set(false);
            ac_open.set(false);
            ac_force.set(String::new());
            side_open.set(false);
            phone_open.set(false);
            gateway_open.set(false);
            picker_open.set(false);
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
    // Gateway switcher page wiring (#1265). The slot is registered at
    // boot by src/shell/contrib/gateway.rs and renders through this
    // late-bound host, because only the page owns these signals.
    crate::gateway::set_host(crate::gateway::GatewayHost {
        base,
        token,
        gateway,
        detail: gateway_detail,
        open: gateway_open,
        profiles: gateway_profiles,
        selected: gateway_selected,
        refresh: Rc::new(move || refresh_meta()),
    });
    crate::gateway_qr::set_host(crate::gateway_qr::PhoneHost {
        base,
        token,
        status,
        open: phone_open,
        refresh: Rc::new(move || refresh_meta()),
    });
    shell::bind(
        "phone.open",
        Rc::new(move |_args: &str| {
            phone_open.set(true);
            Ok(())
        }),
    );

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
    // Git / Worktrees palette performers (#1270). Registered at boot by
    // src/shell/contrib/git_worktree.rs; only the page owns side_tab.
    shell::bind(
        "panel-git",
        Rc::new(move |_args: &str| {
            side_tab.set("git".into());
            Ok(())
        }),
    );
    shell::bind(
        "panel-worktrees",
        Rc::new(move |_args: &str| {
            side_tab.set("worktrees".into());
            Ok(())
        }),
    );

    // Alt+T opens Worktrees. Like Alt+G for Goals, this stays here
    // because only the page owns the view and side-tab signals; the
    // shortcut listing lives in src/shell/contrib/git_worktree.rs.
    // The PR's original Alt+G chord for Git is NOT ported: Alt+G is
    // Goals on current main (#1300), so Git is reached by its tab or
    // the palette instead of stealing the Goals chord. Its original
    // Alt+W chord for Worktrees is NOT kept either: Alt+W is the
    // gateway switcher on current main (#1308), which landed first,
    // so Worktrees takes the free Alt+T instead of double-binding.
    let on_worktrees_key =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            if ev.alt_key()
                && !ev.meta_key()
                && !ev.ctrl_key()
                && !git::shortcut_blocked(&ev)
                && ev.key().eq_ignore_ascii_case("t")
            {
                ev.prevent_default();
                mode.set("chat".into());
                side_tab.set("worktrees".into());
            }
        });
    if let Some(w) = web_sys::window() {
        let _ = w.add_event_listener_with_callback("keydown", on_worktrees_key.as_ref().unchecked_ref());
    }
    on_worktrees_key.forget();
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

    // Session delete palette performer (#1358). The contrib command has
    // already resolved the argument to exactly one session id via the
    // shell's unique match; this only arms the same confirm dialog the
    // row button opens. No request is issued here.
    shell::bind(
        "session-delete",
        Rc::new(move |id: &str| {
            let id = id.to_string();
            let active = active_root.get_untracked();
            let active_running = activity
                .get_untracked()
                .iter()
                .any(|row| is_running(&row.state));
            let label = merge_sessions(
                &roots.get_untracked(),
                &sessions.get_untracked(),
                &pins.get_untracked(),
                &seen.get_untracked(),
                &active,
                active_running,
            )
            .into_iter()
            .find(|row| row.id == id)
            .map(|row| session_label(&row.title, &short_id(&row.id)))
            .unwrap_or_else(|| short_id(&id));
            mode.set("chat".into());
            request_delete(id, label);
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

    // Alt+C opens Schedule (the clock). The original PR shipped Alt+S,
    // but side chat (#1282) landed first and owns Alt+S on main, so the
    // reland takes the free Alt+C — the same resolution the gateway
    // switcher used when it took Alt+W. Like Alt+G, this stays here
    // because only the page can set `mode`; the shortcut is listed in
    // the overlay via src/shell/contrib/scheduler.rs. Schedule unmounts
    // the document pane, so an edit in progress or typing in a field
    // must keep the key (the original PR's guard).
    let on_schedule_key =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            if ev.alt_key()
                && !ev.meta_key()
                && !ev.ctrl_key()
                && ev.key().eq_ignore_ascii_case("c")
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
                if !typing && !doc_editing.get_untracked() {
                    ev.prevent_default();
                    mode.set("schedule".into());
                }
            }
        });
    if let Some(w) = web_sys::window() {
        let _ = w.add_event_listener_with_callback("keydown", on_schedule_key.as_ref().unchecked_ref());
    }
    on_schedule_key.forget();

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
            plus_open.set(false);
            ac_open.set(false);
            ac_force.set(String::new());
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
                plus_open.set(false);
                ac_open.set(false);
                ac_force.set(String::new());
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

    // Find-in-transcript view state for the readable pane (#1310 union):
    // the matches over the ChatLine mirror — which is transcript.chat_lines(),
    // index-aligned with Transcript::messages — plus the current match,
    // republished whenever the query, bar state, step, or line list moves.
    // TranscriptPane renders the <mark> highlights from this; the bar's
    // count and the scroll effect above keep using `lines` directly.
    let find_view = RwSignal::new(crate::transcript_view::FindView::default());
    Effect::new(move |_| {
        let q = tf_query.get();
        let open = tf_open.get();
        let idx = tf_idx.get();
        let hits = if open && !q.is_empty() {
            transcript_matches(&lines.get(), &q)
        } else {
            Vec::new()
        };
        let current = hits
            .get(transcript_find::clamped_idx(idx, hits.len()))
            .copied();
        find_view.set(crate::transcript_view::FindView { hits, current });
    });

    let apply_title: Callback<(String,)> = (move |title: String| {
        let id = active_root.get_untracked();
        if id.is_empty() {
            ux_note.set("No session to rename.".into());
            return;
        }
        let title = title.trim().to_string();
        if title.is_empty() {
            ux_note.set("A title needs at least one character.".into());
            return;
        }
        transcript.update(|t| t.pin_title(title.clone()));
        sessions.update(|list| {
            if let Some(s) = list.iter_mut().find(|s| s.id == id) {
                s.title = title.clone();
            }
        });
        roots.update(|list| {
            if let Some(r) = list.iter_mut().find(|r| r.id == id) {
                r.title = title.clone();
            }
        });
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/sessions/{}", api::js_encode(&id));
            match patch_json(&b, &t, &path, serde_json::json!({"title": title})).await {
                Ok(()) => ux_note.set(String::new()),
                Err(e) => ux_note.set(format!("Rename failed: {e}")),
            }
        });
    })
    .into();
    let handlers = TranscriptHandlers {
        send: (move |text: String| {
            send_text(text);
        })
        .into(),
        rename: apply_title,
        regenerate: (move || {
            let title = transcript.with_untracked(|t| t.regenerated_title());
            if title.is_empty() {
                ux_note.set("No message to title from yet.".into());
                return;
            }
            apply_title.run((title,));
        })
        .into(),
        export_timeline: (move || {
            let id = active_root.get_untracked();
            if id.is_empty() {
                ux_note.set("No session to export.".into());
                return;
            }
            let (b, t) = (base.get_untracked(), token.get_untracked());
            spawn_local(async move {
                let path = format!(
                    "/v1/sessions/{}/timeline/export?format=json",
                    api::js_encode(&id)
                );
                match get_text(&b, &t, &path).await {
                    Ok(text) => {
                        save_download(
                            &format!("strike-timeline-{}.json", file_stem(&id)),
                            "application/json",
                            &text,
                        );
                        ux_note.set("Exported the redacted timeline.".into());
                    }
                    Err(e) => ux_note.set(format!("Export failed: {e}")),
                }
            });
        })
        .into(),
        export_markdown: (move || {
            let md = transcript.with_untracked(|t| t.to_markdown());
            let id = active_root.get_untracked();
            let name = if id.is_empty() {
                "strike-session.md".into()
            } else {
                format!("strike-session-{}.md", file_stem(&id))
            };
            save_download(&name, "text/markdown;charset=utf-8", &md);
            ux_note.set("Downloaded the visible transcript.".into());
        })
        .into(),
        retry: (move |text: String| {
            send_text(text);
        })
        .into(),
        fork: (move |keep: i32| {
            let root = active_root.get_untracked();
            if root.is_empty() {
                ux_note.set("No session to fork.".into());
                return;
            }
            let (b, t) = (base.get_untracked(), token.get_untracked());
            spawn_local(async move {
                let path = format!("/v1/sessions/{}/fork_at", api::js_encode(&root));
                match post_json_value(&b, &t, &path, serde_json::json!({"keepEvents": keep})).await
                {
                    Ok(created) => {
                        let id = created
                            .get("id")
                            .and_then(|v| v.as_str())
                            .unwrap_or("")
                            .to_string();
                        if id.is_empty() {
                            ux_note.set("Fork did not return a session.".into());
                            return;
                        }
                        ux_note.set("Forked this turn into a new session.".into());
                        resume_session(id);
                    }
                    Err(e) => ux_note.set(format!("Fork failed: {e}")),
                }
            });
        })
        .into(),
        open_file: (move |path: String, line: u32| {
            // The editor stream (#1269) is wired now: a file chip opens the
            // document pane at the line, beside the transcript.
            doc_diff.set(false);
            doc_line.set(line);
            doc_path.set(path);
        })
        .into(),
    };

    let ui = TriggerSignals {
        ac_force,
        ac_open,
        ac_index,
        ac_items,
        ac_replace_all,
        trig_start,
        trig_end,
        skills,
        commands,
        search_gen,
        base,
        token,
        draft,
        plus_open,
        mode_menu,
        model_menu,
    };

    // Bootstrap can land after the first keystroke. Recompute only when the
    // catalog contents change, and only for the trigger at the real caret.
    // refresh_meta writes the same lists every poll; a content match is a no-op.
    let catalog_fp = RwSignal::new(String::new());
    Effect::new(move |_| {
        let cmds = commands.get();
        let sk = skills.get();
        let fp = catalog_fingerprint(&cmds, &sk);
        if fp == catalog_fp.get_untracked() {
            return;
        }
        let Some(cursor) = composer_caret() else {
            return;
        };
        catalog_fp.set(fp);
        let draft_now = draft.get_untracked();
        if !ui.ac_force.get_untracked().is_empty() {
            sync_composer_trigger(draft_now, cursor, &ui);
            return;
        }
        if let Some(trig) = trigger_at(&draft_now, cursor) {
            if matches!(trig.kind, Kind::Command | Kind::Skill) {
                sync_composer_trigger(draft_now, cursor, &ui);
            }
        }
    });

    view! {
        <ApprovalHost
            base=base
            token=token
            root=active_root
            perm_mode=perm_mode
            menu_open=mode_menu
            panel_open=panel_open
            confirm=confirm
            asks=asks
            live_audit=live_audit
            workspaces=workspaces
            active_workspace=active_workspace
            status=status
            auto_seconds=auto_seconds
            auto_exclude=auto_exclude
            rules=rules_doc
            stored_audit=stored_audit
            panel_error=panel_error
            reply_n=reply_n
            reply_id=reply_id
            reply_decision=reply_decision
        />
        <shell::Chrome />
        {gateway_qr::phone_view()}
        <session_delete::SessionDeleteDialog
            target=delete_target
            base=base
            token=token
            on_deleted=on_deleted
        />
        <Show when=move || mode.get() == "daemon" fallback=move || ()>
            <DaemonPane
                base=base
                token=token
                status=status
                gateway=gateway
                gateway_detail=gateway_detail
                gateway_open=gateway_open
                gateway_profiles=gateway_profiles
                gateway_selected=gateway_selected
                refresh=refresh_meta
                open_picker=move || picker_open.set(true)
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
        <Show when=move || mode.get() == "tasks" fallback=move || ()>
            <task_runner::TaskRunner
                base=base
                token=token
                active_root=active_root
                status=status
            />
        </Show>
        <Show when=move || mode.get() == "schedule" fallback=move || ()>
            <schedule::SchedulePage
                base=base
                token=token
                mode=mode
                active_root=active_root
                status=status
            />
        </Show>
        <Show when=move || mode.get() == "triggers" fallback=move || ()>
            <triggers::TriggersPage base=base token=token status=status />
        </Show>
        <Show when=move || mode.get() == "apps" fallback=move || ()>
            <apps::AppsPane
                base=base
                token=token
                status=status
                palette_open=apps_palette_open
                open_session=move |id: String| {
                    mode.set("chat".into());
                    activate_root(id);
                }
            />
        </Show>
        <Show when=move || mode.get() == "chat">
            <div class=move || {
                let mut class = chat_mode::shell_class(
                    focus.get(),
                    &layout.get(),
                    sessions_drawer.get(),
                    panel_drawer.get(),
                );
                class.push_str(" phone-pane-");
                class.push_str(&phone_pane.get());
                class
            }>
                <button type="button" class="chat-scrim" aria-label="Close panel" on:click=move |_| {
                    sessions_drawer.set(false);
                    panel_drawer.set(false);
                }></button>
                <aside class="rail">
<SessionPanel
                        base=base
                        token=token
                        sessions=sessions
                        roots=roots
                        activity=activity
                        active_root=active_root
                        seen=seen
                        workspaces=workspaces
                        on_open=open_session
                        on_new=new_session
                        on_refresh=refresh_meta
                        on_delete=request_delete
                    />
                    <div class="ws-chip">
                        <div class="section-head">
                            <h2 class="panel-title">"Workspace"</h2>
                            <button class="secondary ws-open" on:click=move |_| picker_open.set(true)>"Change workspace"</button>
                        </div>
                        <p class="name">{move || {
                            let id = active_workspace.get();
                            workspaces.get().into_iter().find(|w| w.id == id).map(|w| w.name).unwrap_or_else(|| "No workspace".into())
                        }}</p>
                        <p class="path">{move || {
                            let id = active_workspace.get();
                            if let Some(w) = workspaces.get().into_iter().find(|w| w.id == id) {
                                w.path
                            } else {
                                let cwd = session_cwd.get();
                                if cwd.is_empty() { "New sessions use the daemon directory.".into() } else { cwd }
                            }
                        }}</p>
                        {move || {
                            let id = active_workspace.get();
                            let ws_path = workspaces.get().into_iter().find(|w| w.id == id).map(|w| w.path).unwrap_or_default();
                            let note = crate::workspaces::chip_note(&ws_path, &session_cwd.get());
                            if note.is_empty() {
                                view! { <span></span> }.into_any()
                            } else {
                                view! { <p class="hint">{note}</p> }.into_any()
                            }
                        }}
                        <p class="hint">"This line is the directory. Sessions above are chats, not folders."</p>
                    </div>
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
                    <TranscriptPane
                        transcript=transcript
                        loading=loading
                        load_error=load_error
                        tick=tick
                        note=ux_note
                        handlers=handlers
                        find=find_view
                    />
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
                            && transcript.with(|t| t.messages.last().map(|m| m.role == crate::transcript::Role::Strike).unwrap_or(false))
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
                    <ApprovalCards
                        asks=asks
                        perm_mode=perm_mode
                        auto_seconds=auto_seconds
                        auto_exclude=auto_exclude
                        reply_n=reply_n
                        reply_id=reply_id
                        reply_decision=reply_decision
                    />
                    <div class="composer" data-feature="composer-triggers"
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
                        <ApprovalMenu
                            menu_open=mode_menu
                            perm_mode=perm_mode
                            confirm=confirm
                            panel_open=panel_open
                            base=base
                            token=token
                            root=active_root
                            status=status
                        />
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
                        <Show when=move || plus_open.get()>
                            <div class="menu composer-plus" data-composer-triggers="plus">
                                <button type="button" class="palette-row" on:click=move |_| mention_composer_file(&ui)>
                                    <span class="name">"Mention a file"</span>
                                    <span class="path">"Insert @ and search the working tree"</span>
                                </button>
                                <button type="button" class="palette-row" on:click=move |_| prefix_composer_shell(&ui)>
                                    <span class="name">"Run a shell command"</span>
                                    <span class="path">"Prefix ! and ask before it runs"</span>
                                </button>
                                <button type="button" class="palette-row" on:click=move |_| open_composer_catalog("skills", &ui)>
                                    <span class="name">"Use a skill"</span>
                                    <span class="path">"Skills loaded for this daemon"</span>
                                </button>
                                <button type="button" class="palette-row" on:click=move |_| open_composer_catalog("commands", &ui)>
                                    <span class="name">"Slash command"</span>
                                    <span class="path">"Daemon commands from bootstrap"</span>
                                </button>
                            </div>
                        </Show>
                        <Show when=move || ac_open.get()>
                            <div class="menu composer-suggest" role="listbox" data-composer-triggers="suggestions">
                                {move || {
                                    let selected_at = ac_index.get();
                                    ac_items.get().into_iter().enumerate().map(move |(i, item)| {
                                        let picked = item.clone();
                                        let label = item.label.clone();
                                        let detail = item.detail.clone();
                                        let selectable = item.selectable;
                                        let selected = i == selected_at;
                                        let klass = if selected && selectable { "palette-row on" } else { "palette-row" };
                                        view! {
                                            <button type="button" role="option" class=klass disabled=move || !selectable aria-selected=selected.to_string() on:click=move |_| accept_suggestion(picked.clone(), &ui)>
                                                <span class="name">{label}</span>
                                                <span class="path">{detail}</span>
                                            </button>
                                        }
                                    }).collect_view()
                                }}
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
                                on:input=move |ev| {
                                    if let Some((text, cursor)) = textarea_state(ev.target()) {
                                        draft.set(text.clone());
                                        sync_composer_trigger(text, cursor, &ui);
                                    }
                                }
                                on:click=move |ev: web_sys::MouseEvent| {
                                    if let Some((text, cursor)) = textarea_state(ev.target()) {
                                        sync_composer_trigger(text, cursor, &ui);
                                    }
                                }
                                on:keyup=move |ev: web_sys::KeyboardEvent| {
                                    if matches!(ev.key().as_str(), "ArrowLeft" | "ArrowRight" | "Home" | "End") {
                                        if let Some((text, cursor)) = textarea_state(ev.target()) {
                                            sync_composer_trigger(text, cursor, &ui);
                                        }
                                    }
                                }
                                on:keydown=move |ev: web_sys::KeyboardEvent| {
                                    if ev.key() == "ArrowDown" || ev.key() == "ArrowUp" {
                                        let items = ac_items.get_untracked();
                                        if ac_open.get_untracked() && items.iter().any(|item| item.selectable) {
                                            ev.prevent_default();
                                            let delta = if ev.key() == "ArrowDown" { 1 } else { -1 };
                                            ac_index.set(move_index(&items, ac_index.get_untracked(), delta));
                                        }
                                    } else if ev.key() == "Escape" && (ac_open.get_untracked() || plus_open.get_untracked()) {
                                        ev.prevent_default();
                                        ev.stop_propagation();
                                        ac_open.set(false);
                                        plus_open.set(false);
                                        ac_force.set(String::new());
                                    } else if ev.key() == "Tab" {
                                        if ac_open.get_untracked() {
                                            let items = ac_items.get_untracked();
                                            let idx = ac_index.get_untracked();
                                            if let Some(item) = items.get(idx) {
                                                if item.selectable {
                                                    ev.prevent_default();
                                                    accept_suggestion(item.clone(), &ui);
                                                }
                                            }
                                        }
                                    } else if ev.key() == "Enter" && !ev.shift_key() {
                                        if ac_open.get_untracked() {
                                            ev.prevent_default();
                                            let items = ac_items.get_untracked();
                                            let idx = ac_index.get_untracked();
                                            if let Some(item) = items.get(idx) {
                                                if item.selectable {
                                                    accept_suggestion(item.clone(), &ui);
                                                }
                                            }
                                            return;
                                        }
                                        // Phone keyboards use Enter for a newline. Send is the button.
                                        if !mobile::is_phone() {
                                            ev.prevent_default();
                                            send();
                                        }
                                    }
                                }
                                on:paste=move |ev: web_sys::ClipboardEvent| {
                                    if let Some(data) = ev.clipboard_data() {
                                        if let Some(files) = data.files() {
                                            if files.length() > 0 {
                                                ev.prevent_default();
                                                chat_mode::enqueue_file_list(files, pending_files, status);
                                            }
                                        }
                                    }
                                }
></textarea>
                            <div class="composer-bar">
                                <div class="composer-chips">
                                <button type="button" class="chip plus" aria-label="Composer actions" data-composer-triggers="plus-button" on:click=move |_| {
                                    mode_menu.set(false);
                                    model_menu.set(false);
                                    ac_open.set(false);
                                    ac_force.set(String::new());
                                    plus_open.update(|v| *v = !*v);
                                }>"+"</button>
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
                                <button type="button" class=move || {
                                    if approvals::is_dangerous(&perm_mode.get()) {
                                        "chip approvals-mode-chip danger"
                                    } else {
                                        "chip approvals-mode-chip"
                                    }
                                } aria-haspopup="menu" aria-expanded=move || mode_menu.get() on:click=move |_| {
                                    model_menu.set(false);
                                    memory_menu.set(false);
                                    plus_open.set(false);
                                    ac_open.set(false);
                                    ac_force.set(String::new());
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
                                </div>
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
                        <p class="hint" data-composer-triggers="status">{move || {
                            let note = transcript.with(|t| t.trigger_note.clone());
                            let pending = transcript.with(|t| t.asks.len());
                            let custom = trigger_status(&note, pending);
                            if custom.is_empty() {
                                "Queue waits out the turn. Steer joins a running turn. ! runs a shell after approval. @file, /command, and $skill expand on send.".into()
                            } else {
                                custom
                            }
                        }}</p>
                    </div>
                </section>
                <aside class="side">
                    <div class="side-tabs" role="tablist" aria-label="Side panel">
                        <button class=move || if side_tab.get() == "activity" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("activity".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("activity")></span>"Activity"</button>
                        <button class=move || if side_tab.get() == "files" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("files".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("files")></span>"Files"</button>
                        <button class=move || if side_tab.get() == "changes" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("changes".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("changes")></span>"Changes"</button>
                        <button class=move || if side_tab.get() == "git" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("git".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("git")></span>"Git"</button>
                        <button class=move || if side_tab.get() == "worktrees" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("worktrees".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("worktrees")></span>"Worktrees"</button>
                        <button class=move || if side_tab.get() == "memory" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("memory".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("memory")></span>"Memory"</button>
                        <button class=move || if side_tab.get() == "knowledge" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("knowledge".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("knowledge")></span>"Knowledge"</button>
                        <button class=move || if side_tab.get() == "artifacts" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("artifacts".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("artifacts")></span>"Artifacts"</button>
                        <button class=move || if side_tab.get() == "terminal" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("terminal".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("terminal")></span>"Terminal"</button>
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
                            <p class="hint">"Open reads the file. Click or drag inserts @path. Send attaches the file."</p>
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
                    <Show when=move || side_tab.get() == "git">
                        <git::GitPanel base=base token=token status_line=status />
                    </Show>
                    <Show when=move || side_tab.get() == "worktrees">
                        <git::WorktreePanel base=base token=token status_line=status active_root=active_root />
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
                    <Show when=move || side_tab.get() == "terminal">
                        <terminal::TerminalPanel
                            base=base
                            token=token
                            workspace_id=active_workspace
                            session_id=active_root
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
        <WorkspacePicker
            open=picker_open
            base=base
            token=token
            status=status
            workspaces=workspaces
            active_workspace=active_workspace
            recents=workspace_recents
            refresh=refresh_meta
        />
        <PhoneNav pane=phone_pane side_tab=side_tab mode=mode />
    }
}

#[component]
fn DaemonPane(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    gateway: RwSignal<String>,
    gateway_detail: RwSignal<String>,
    gateway_open: RwSignal<bool>,
    gateway_profiles: RwSignal<Vec<gateway::Profile>>,
    gateway_selected: RwSignal<String>,
    refresh: impl Fn() + Copy + Send + Sync + 'static,
    open_picker: impl Fn() + Copy + 'static,
) -> impl IntoView {
    let _ = status;
    let connect = move |_: web_sys::MouseEvent| {
        save("strike.base", &base.get());
        gateway::persist_token("this", &token.get());
        gateway::forget_persisted_host_token();
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
                <p class="hint">"Same-origin live transcript uses the daemon cookie. strike connect keeps a short-lived grant in a local proxy so the URL has no token."</p>
            </section>
            <gateway::GatewaySwitcher
                inline=true
                primary=false
                base=base
                token=token
                gateway=gateway
                detail=gateway_detail
                open=gateway_open
                profiles=gateway_profiles
                selected=gateway_selected
                refresh=refresh
            />
            <section class="card">
                <h2 class="panel-title">"Workspaces"</h2>
                <p class="hint">"A workspace is a directory. Session lists are not folders."</p>
                <button class="primary ws-open" on:click=move |_| open_picker()>"Change workspace"</button>
            </section>
        </main>
    }
}

fn main() {
    leptos::mount::mount_to_body(App);
    // The cockpit mounted, so the static boot shell from index.html has
    // done its job. mount_to_body appends rather than replacing the body,
    // so remove the placeholder explicitly; if the wasm never runs, the
    // placeholder stays and the user gets the honest shell state instead
    // of a blank page.
    if let Some(doc) = web_sys::window().and_then(|w| w.document()) {
        if let Some(el) = doc.query_selector(".boot-shell").ok().flatten() {
            el.remove();
        }
    }
}
