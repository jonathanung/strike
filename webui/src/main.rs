//! Strike web client. The session list is live roots plus saved sessions.
//! Activity, Files, and Changes are the side panel. Schedule, memory,
//! knowledge, artifacts, and the task runner are not drawn here; see
//! docs/webui-planned.md.

mod api;
mod editor;
mod model;

use std::cell::{Cell, RefCell};
use std::collections::HashMap;
use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use model::{
    activity_panel, child_card_from_parts, child_card_from_text, child_keys_match, display_state,
    filter_sessions, fmt_ago, fmt_elapsed, is_running, merge_sessions, mode_label, task_headline,
    permission_modes, prefer_summary, protocol_dump, session_label, status_kind, strip_echo_tip,
    visible_sessions, ActivityRow, Bootstrap, ChangedFilesResponse, ChatLine, ChildCard,
    ChildrenResponse, FilesResponse, Health, ModelInfo, ModelList, Remote, RootSummary,
    SessionItem, SessionsResponse, TeamSnapshot, Workspace, WorkspacesResponse,
};
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{
    get_json, idempotency_key, now_ms, patch_json, post_json, post_json_value, post_op,
};
use crate::editor::{DocumentPane, LinkedBody};
use protocol_schema::Event;
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
            detail: String::new(),
            key: String::new(),
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
            if let Some(card) = child_card_from_text(&line.body) {
                cards.push(card);
                line.body.clear();
            } else if let Some((summary, details)) = protocol_dump(&line.body) {
                cards.push(ChildCard {
                    key: format!("dump-{}", line.id),
                    name: "Details".into(),
                    status: String::new(),
                    summary,
                    details,
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

fn apply_envelope(
    lines: RwSignal<Vec<ChatLine>>,
    seq: &Rc<Cell<u32>>,
    open: &Rc<Cell<bool>>,
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
            push_line(lines, seq, "strike", "Strike", &chunk, "");
        }
        "turn.completed" => {
            open.set(false);
            absorb_noise(lines, seq);
        }
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
            upsert_child(
                lines,
                seq,
                child_card_from_parts(&name, &session, &status, &summary, &details),
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
        PaletteCmd {
            section: "Theme",
            id: "theme-strike",
            label: "Theme: Strike",
            detail: "Stock TUI palette. Royal purple, cyan working state",
        },
        PaletteCmd {
            section: "Theme",
            id: "theme-nord",
            label: "Theme: Nord",
            detail: "Bundled TUI theme. Missing surfaces stay Strike",
        },
        PaletteCmd {
            section: "Theme",
            id: "theme-dracula",
            label: "Theme: Dracula",
            detail: "Bundled TUI theme. Missing surfaces stay Strike",
        },
        PaletteCmd {
            section: "Theme",
            id: "theme-gruvbox",
            label: "Theme: Gruvbox",
            detail: "Bundled TUI theme. Missing surfaces stay Strike",
        },
        PaletteCmd {
            section: "Theme",
            id: "theme-monokai",
            label: "Theme: Monokai",
            detail: "Bundled TUI theme. Missing surfaces stay Strike",
        },
        PaletteCmd {
            section: "Theme",
            id: "theme-tokyo-night",
            label: "Theme: Tokyo Night",
            detail: "Bundled TUI theme. Missing surfaces stay Strike",
        },
        PaletteCmd {
            section: "Theme",
            id: "theme-catppuccin",
            label: "Theme: Catppuccin",
            detail: "Bundled TUI theme. Missing surfaces stay Strike",
        },
        PaletteCmd {
            section: "Theme",
            id: "scheme-dark",
            label: "Scheme: Dark",
            detail: "Dark hexes from the TUI token pair",
        },
        PaletteCmd {
            section: "Theme",
            id: "scheme-light",
            label: "Scheme: Light",
            detail: "Light hexes from the TUI token pair",
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

fn icon(name: &str) -> &'static str {
    match name {
        "sessions" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 4.5h11M2.5 8h11M2.5 11.5h7"/></svg>"#
        }
        "activity" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M1.5 8h2.5l1.8-3.5L9 12l1.7-4H14.5"/></svg>"#
        }
        "files" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 2.5h5l3 3V13.5H4z"/><path d="M9 2.5V5.5h3"/></svg>"#
        }
        "changes" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 4.5h10M3 8h6M3 11.5h8"/></svg>"#
        }
        "command" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 3.5h11v9h-11z"/><path d="M5 7l2 1.5L5 10M8.5 10.5h2.5"/></svg>"#
        }
        "status" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5"><circle cx="8" cy="8" r="2.5"/></svg>"#
        }
        _ => "",
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
    let doc_path = RwSignal::new(String::new());
    let doc_line = RwSignal::new(0u32);
    let doc_diff = RwSignal::new(false);
    let doc_editing = RwSignal::new(false);
    let files_path = RwSignal::new(String::new());
    let files = RwSignal::new(Vec::<model::DirEntry>::new());
    let files_error = RwSignal::new(String::new());
    let changes = RwSignal::new(Vec::<model::ChangedFile>::new());
    let changes_error = RwSignal::new(String::new());
    apply_theme(&load("strike.theme"), &load("strike.scheme"));

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

    let set_theme = move |theme: String| {
        apply_theme(&theme, &load("strike.scheme"));
    };
    let set_scheme = move |scheme: String| {
        apply_theme(&load("strike.theme"), &scheme);
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
            "theme-strike" => set_theme("strike".into()),
            "theme-nord" => set_theme("nord".into()),
            "theme-dracula" => set_theme("dracula".into()),
            "theme-gruvbox" => set_theme("gruvbox".into()),
            "theme-monokai" => set_theme("monokai".into()),
            "theme-tokyo-night" => set_theme("tokyo-night".into()),
            "theme-catppuccin" => set_theme("catppuccin".into()),
            "scheme-dark" => set_scheme("dark".into()),
            "scheme-light" => set_scheme("light".into()),
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
            } else if ev.alt_key() && !ev.meta_key() && !ev.ctrl_key() && ev.key() == "ArrowLeft" {
                ev.prevent_default();
                travel(true);
            } else if ev.alt_key() && !ev.meta_key() && !ev.ctrl_key() && ev.key() == "ArrowRight" {
                ev.prevent_default();
                travel(false);
            } else if ev.alt_key() && !ev.meta_key() && !ev.ctrl_key() && ev.key() == "\\" {
                ev.prevent_default();
                focus.update(|v| *v = !*v);
            } else if ev.key() == "Escape" {
                let menu = palette_open.get_untracked()
                    || mode_menu.get_untracked()
                    || model_menu.get_untracked();
                palette_open.set(false);
                mode_menu.set(false);
                model_menu.set(false);
                if !menu
                    && !doc_editing.get_untracked()
                    && !doc_path.get_untracked().is_empty()
                {
                    doc_path.set(String::new());
                    doc_editing.set(false);
                }
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
            <div class="brand" title=move || status.get()>
                "Strike"
                <span class="brand-status">{move || status.get()}</span>
            </div>
            <button class="command-pill" title="Ctrl+K commands. Alt+Left and Alt+Right switch sessions. Alt+\\ focuses the transcript." on:click=move |_| {
                palette_query.set(String::new());
                palette_open.set(true);
            }>
                <span class="pill-label">
                    <span class="icon-slot" aria-hidden="true" inner_html=icon("command")></span>
                    <span>"Run a command"</span>
                </span>
                <kbd>"Ctrl+K"</kbd>
            </button>
            <div class="header-actions">
                <span class=move || match gateway.get().as_str() {
                    "connected" => "chip ok",
                    "unreachable" => "chip bad",
                    _ => "chip",
                }>{move || match gateway.get().as_str() {
                    "connected" => "Connected",
                    "unreachable" => "Unreachable",
                    _ => "Checking",
                }}</span>
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
                    <div class="palette-list">
                    {move || {
                        let q = palette_query.get().to_ascii_lowercase();
                        let run = run_command;
                        let mut blocks: Vec<_> = Vec::new();
                        for section in ["Commands", "Apps", "Settings", "Theme"] {
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
                    </div>
                    <p class="hint keys">"Enter runs the first match. Alt+Left and Alt+Right switch sessions. Alt+\\ focuses the transcript. Esc closes."</p>
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
                            let busy = row.busy;
                            let unread = row.unread;
                            let pinned = row.pinned;
                            let fallback = short_id(&id);
                            let label = session_label(&row.title, &fallback);
                            let when = fmt_ago(row.mtime, (now_ms() / 1000.0) as i64);
                            let subtitle = if busy {
                                "In progress"
                            } else if unread {
                                "Unread"
                            } else if live {
                                "Idle"
                            } else {
                                "Saved"
                            };
                            let dot = if busy {
                                "dot dot-running"
                            } else if unread {
                                "dot dot-unread"
                            } else {
                                "dot"
                            };
                            let klass = if id == active { "session-row card on" } else { "session-row card" };
                            let pin_label = if pinned { "Unpin" } else { "Pin" };
                            let pin_class = if pinned { "pin on" } else { "pin" };
                            view! {
                                <li class=klass>
                                    <button class="link session-main" on:click=move |_| open(id_btn.clone(), live)>
                                        <span class=dot></span>
                                        <span class="session-copy">
                                            <span class="session-top">
                                                <span class="name">{label}</span>
                                                <span class="time">{when}</span>
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
                                    <EmptyState title="No sessions match".into() body="New roots show up here. Pins and unread marks stay in this browser.".into() />
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
                    <p class="hint keys">"Pins stay in this browser. Alt+Left and Alt+Right switch sessions. Alt+\\ focuses the transcript."</p>
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
                    <Show when=move || doc_path.get().is_empty()>
                    <div class="transcript" aria-live="polite">
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
                                            <button type="button" class="suggestion" on:click=move |_| send(send_label.clone())>{label}</button>
                                        }
                                    }).collect_view()}
                                </div>
                            </div>
                        </Show>
                        {move || lines.get().into_iter().map(|line| {
                            let badge = line.badge.clone();
                            let badge_kind = badge.clone();
                            let role = line.role.clone();
                            let title = line.title.clone();
                            let body = line.body.clone();
                            let detail = line.detail.clone();
                            let has_badge = !badge.is_empty();
                            let has_detail = !detail.is_empty();
                            let card = role == "child";
                            let prose = role == "you" || role == "strike";
                            let tool_dump = !card && !prose && (body.trim_start().starts_with('{') || body.trim_start().starts_with('[') || body.chars().count() > 240);
                            let body_view = if card {
                                let summary = body;
                                view! {
                                    <div>
                                        <p class="summary">
                                            <LinkedBody text=summary kind="inline" doc_path=doc_path doc_line=doc_line doc_diff=doc_diff />
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
                                view! { <LinkedBody text=body kind="prose" doc_path=doc_path doc_line=doc_line doc_diff=doc_diff /> }.into_any()
                            } else if tool_dump {
                                view! {
                                    <details>
                                        <summary>"Details"</summary>
                                        <LinkedBody text=body kind="pre" doc_path=doc_path doc_line=doc_line doc_diff=doc_diff />
                                    </details>
                                }.into_any()
                            } else {
                                view! { <LinkedBody text=body kind="pre" doc_path=doc_path doc_line=doc_line doc_diff=doc_diff /> }.into_any()
                            };
                            view! {
                                <article class=format!("bubble card role-{role}")>
                                    <header>
                                        <span class="who">{title}</span>
                                        <Show when=move || has_badge>
                                            <Badge kind=badge_kind.clone() label=badge.clone() />
                                        </Show>
                                    </header>
                                    {body_view}
                                </article>
                            }
                        }).collect_view()}
                    </div>
                    </Show>
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
                            <div class="composer-bar">
                                <button type="button" class="chip" on:click=move |_| {
                                    model_menu.set(false);
                                    mode_menu.update(|v| *v = !*v);
                                }>{move || format!("Approval · {}", mode_label(&perm_mode.get()))}</button>
                                <button type="button" class="chip" on:click=open_models>{move || {
                                    let model = model_name.get();
                                    if model.is_empty() { "Model".into() } else { model }
                                }}</button>
                                <button type="button" class=move || if enter_mode.get() == "steer" { "chip on" } else { "chip" } on:click=move |_| set_enter("steer".into())>"Steer"</button>
                                <button type="button" class=move || if enter_mode.get() == "queue" { "chip on" } else { "chip" } on:click=move |_| set_enter("queue".into())>"Queue"</button>
                                <button class="primary send" type="submit">"Send"</button>
                            </div>
                        </form>
                        <p class="hint">"Queue waits out the turn. Steer joins a running turn. run <command> uses the shell. @path is text."</p>
                    </div>
                </section>
                <aside class="side">
                    <div class="side-tabs" role="tablist" aria-label="Side panel">
                        <button class=move || if side_tab.get() == "activity" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("activity".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("activity")></span>"Activity"</button>
                        <button class=move || if side_tab.get() == "files" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("files".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("files")></span>"Files"</button>
                        <button class=move || if side_tab.get() == "changes" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("changes".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("changes")></span>"Changes"</button>
                    </div>
                    <Show when=move || side_tab.get() == "activity">
                    <div class="activity">
                    <header class="activity-head">
                        <span class="hint">"Live tasks"</span>
                        <button class="secondary" disabled=move || running_count() == 0 on:click=move |_| stop_all()>"Stop all"</button>
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
                                let who = name.clone();
                                let agent = row.agent.clone();
                                let agent_label = agent.clone();
                                let primary = if show_task { headline } else { name.clone() };
                                let dot = match state.as_str() {
                                    "running" | "working" => "dot dot-running",
                                    "blocked" | "stalled" => "dot dot-blocked",
                                    "done" | "completed" => "dot dot-done",
                                    "failed" => "dot dot-failed",
                                    _ => "dot",
                                };
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
                                                <span class=dot></span>
                                                <span class="name">{primary}</span>
                                                <Badge kind=state.clone() label=state.clone() />
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
