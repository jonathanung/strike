//! Strike web client. The session list is live roots plus saved sessions.
//! Activity, Files, Changes, Git, Worktrees, Memory, Knowledge,
//! Artifacts, and Terminal are the side panel. Side chat is a separate
//! read-only drawer. Goals and Autopilot live in `goals`. Apps is the
//! queue surface in `apps`. Triggers are a separate page. Schedule is
//! the clock (`schedule` module). The task runner is the Tasks view in
//! `task_runner`. Customize (skills, agents, MCP, hooks, rules) lives
//! in `customize`.

mod agent_picker;
mod annotate;
mod announce;
mod api;
mod appearance;
mod attachment_honesty;
mod apps;
mod approval_outcome;
mod approvals;
mod approvals_ui;
mod away_briefing_view;
mod chat_mode;
mod child_signals;
mod clipboard;
mod compaction_marks;
mod confirm;
mod container_panel;
mod context_doctor;
mod composer_triggers;
mod config_sources;
mod debug_mode_ui;
mod diag;
mod diagnostics;
mod draft_size;
mod drafts;
mod editor;
mod error_notice;
mod fallback;
mod files_invalidated;
mod lessons;
mod elicitation;
mod elicitation_ui;
mod link_patterns;
mod load_epoch;
mod load_warnings;
mod markdown_review;
mod local_issues;
mod mermaid;
mod localfiles;
mod md_table;
mod customize;
mod dashboards;
mod delegation_board;
mod deploy_watch;
mod gateway;
mod gateway_peek;
mod gateway_qr;
mod git;
mod global_search;
mod guardrails;
mod goals;
mod ideas;
mod identity;
mod instance_backup;
mod mobile;
mod provider_signin;
mod providers;
mod review_queue;
mod push;
mod reasoning;
mod questions;
mod questions_ui;
mod review_branch;
mod rule_author;
mod sandbox_explain;
mod schedule;
mod search_in_files;
mod scheduler_presets;
mod model;
mod notify;
mod outcome;
mod project_init;
mod prompt_library;
mod prompt_sharpen;
mod onboarding;
mod outbox;
mod park;
mod preview;
mod permissions_explain;
mod plugin_flow;
mod plugin_lifecycle;
mod plan_card;
mod pr_watch;
mod steer_queue;
mod steer_ack;
mod model_menu;
mod model_menu_view;
mod session_list;
mod session_loops;
mod settings;
mod ship_pr;
mod session_panel;
mod storage;
mod tab_title;
mod themes;
mod task_runner;
mod team_message;
mod file_history;
mod quick_open;
mod transcript_find;
mod transcript_pins;
mod usage_audit;
mod regenerate_title;
mod rewind;
mod session_delete;
mod session_ref_chip;
mod stale_resume;
mod session_restore;
mod share;
mod shell;
mod skill_proposals;
mod skill_usage;
mod side_chat;
mod side_chat_promote;
mod status;
mod status_card_view;
mod term;
mod tool_tail;
mod terminal;
mod transcript;
mod transcript_attachments;
mod transcript_view;
mod tts;
mod update_check;
mod triggers;
mod turn_elapsed;
mod turn_files;
mod turn_failure;
mod turn_usage;
mod turn_verify;
mod verbosity;
mod workspace_image;
mod workspace_trust;
mod workspaces;
mod spec_authoring;
mod workflows;
mod workflow_run;
use crate::shell::icon;
use crate::announce::LiveRegions;
use crate::team_message::TeamMessageComposer;
use crate::identity::IdentityDisc;
use crate::delegation_board::DelegationBoard;
use crate::child_signals::TeamMessagesFeed;


use std::cell::{Cell, RefCell};
use std::collections::HashMap;
use std::rc::Rc;

use leptos::callback::Callback;
use composer_triggers::{
    file_suggestions, first_selectable, move_index, session_catalog, session_suggestions,
    shell_hint, skill_suggestions, slash_suggestions, splice_draft, trigger_at,
    trigger_status, Kind, Suggestion,
};
use leptos::prelude::*;
use leptos::callback::UnsyncCallback;
use crate::error_notice::{humanize, ErrorNotice};
use leptos::task::spawn_local;
use model::{
    activity_panel, display_state, fmt_elapsed, is_running, merge_sessions, mode_label,
    session_label, task_headline, ActivityRow, Bootstrap, ChangedFilesResponse, ChatLine,
    ChildrenResponse, ComposerCommand, FileSearchResponse, FilesResponse, Health, ModelInfo,
    ModelList, RootSummary, SessionItem, SessionsResponse, SkillInfo, StatusBoard, TeamSnapshot,
    Workspace, WorkspacesResponse,
};
use session_panel::{dispatch_session_key, SessionPanel};
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use load_guard::{
    bootstrap_gate, clear_loading, commit_load, gateway_switched, key_matches, BootstrapGate,
    Commit, ErrPolicy, LoadEpoch,
};

use crate::api::{
    get_json, get_text, idempotency_key, now_ms, patch_json, post_json, post_json_value, post_op,
    put_json,
};
use crate::load_epoch::begin;
use crate::approvals::{begin_autonomy_change, begin_session_mode, ModeChange};
use crate::approvals_ui::{
    apply_perm_notes, commit_autonomy, commit_session_mode, ApprovalCards, ApprovalHost,
    ApprovalMenu, AutonomyMenu,
};
use crate::away_briefing_view::AwayBriefing;
use crate::status_card_view::{ChildStatusCard, SessionStatusCard};
use crate::transcript::Transcript;
use crate::transcript_view::{save_download, TranscriptHandlers, TranscriptPane};
use crate::workspaces::WorkspacePicker;
use crate::storage::StoragePanel;
use crate::editor::DocumentPane;
use crate::mobile::PhoneNav;
use crate::customize::CustomizePane;
use crate::side_chat::SideChatPanel;
use crate::share::{ShareDialog, ShareView};
use crate::outbox::OutboxList;

use crate::model::{budget_elapsed, can_retry, RootsResponse};
use crate::model_menu::{active_context, context_view, reduce_envelope, LiveModelState, ModelDefaults};
use crate::status::{
    apply_document_favicon, elapsed_since, session_kind, StatusKind, StatusMark, StatusSlot,
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

pub(crate) fn remove_key(key: &str) {
    if let Some(s) = storage() {
        let _ = s.remove_item(key);
    }
}

fn event_in_field(ev: &web_sys::KeyboardEvent) -> bool {
    let Some(target) = ev.target() else {
        return false;
    };
    let Some(el) = target.dyn_ref::<web_sys::Element>() else {
        return false;
    };
    let tag = el.tag_name();
    tag.eq_ignore_ascii_case("input")
        || tag.eq_ignore_ascii_case("textarea")
        || tag.eq_ignore_ascii_case("select")
        || el.get_attribute("contenteditable").is_some()
}

pub(crate) fn theme_color(theme: &str, scheme: &str) -> &'static str {
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

/// True when a touch starting on `target` belongs to a horizontally
/// scrollable element (code block, table wrap, code view) or a text
/// field: those own horizontal drags, so edge-swipe session navigation
/// must not claim the gesture (#1490 slice 5).
fn touch_in_scroller(target: Option<web_sys::EventTarget>) -> bool {
    let Some(el) = target.and_then(|t| t.dyn_into::<web_sys::Element>().ok()) else {
        return false;
    };
    let mut cur = Some(el);
    for _ in 0..12 {
        let Some(el) = cur else { break };
        let tag = el.tag_name();
        if tag == "TEXTAREA" || tag == "INPUT" || tag == "SELECT" {
            return true;
        }
        if el.scroll_width() > el.client_width() + 1 {
            return true;
        }
        cur = el.parent_element();
    }
    false
}

/// SessionTitled is the daemon's title source (#1262 owns rename). Reflect it
/// on the open root so the list does not keep a raw id until the next poll.
fn textarea_state(target: Option<web_sys::EventTarget>) -> Option<(String, u32)> {
    let el = target?.dyn_into::<web_sys::HtmlTextAreaElement>().ok()?;
    let cursor = el.selection_start().ok().flatten().unwrap_or(0);
    Some((el.value(), cursor))
}

// --- Composer auto-grow (#1411) + prompt history (#1402) -----------------
// One sizing function for every draft mutation: typing calls it from the
// textarea's input handler, and an Effect tracking the draft signal calls
// it (deferred one tick, after Leptos has written `prop:value`) for every
// programmatic restore — per-session draft restore, queue recall, dictation,
// suggestions, and prompt-history recall. The 44px/160px clamps are the
// pre-existing CSS clamps, kept for parity across surfaces (#1411 Q1).

pub(crate) const COMPOSER_MIN_HEIGHT_PX: i32 = 44;
pub(crate) const COMPOSER_MAX_HEIGHT_PX: i32 = 160;
pub(crate) const PROMPT_HISTORY_CAP: usize = 100;

fn composer_height_px(scroll_height: i32) -> i32 {
    scroll_height.clamp(COMPOSER_MIN_HEIGHT_PX, COMPOSER_MAX_HEIGHT_PX)
}

fn sync_composer_height_for(el: &web_sys::HtmlTextAreaElement) {
    let Some(html) = el.dyn_ref::<web_sys::HtmlElement>() else {
        return;
    };
    // Reset first: `scrollHeight` never shrinks below the current box, so
    // measuring without the reset would grow but never shrink.
    let _ = html.style().set_property("height", "auto");
    let height = composer_height_px(el.scroll_height());
    let _ = html
        .style()
        .set_property("height", &format!("{height}px"));
}

fn sync_composer_height_target(target: Option<web_sys::EventTarget>) {
    if let Some(el) = target.and_then(|t| t.dyn_into::<web_sys::HtmlTextAreaElement>().ok()) {
        sync_composer_height_for(&el);
    }
}

fn sync_composer_height() {
    let Some(el) = web_sys::window()
        .and_then(|w| w.document())
        .and_then(|doc| doc.query_selector("#composer-input").ok().flatten())
        .and_then(|el| el.dyn_into::<web_sys::HtmlTextAreaElement>().ok())
    else {
        return;
    };
    sync_composer_height_for(&el);
}

/// Defer the DOM sync one tick so a programmatic `draft.set(..)` has been
/// written into the textarea's value before `scrollHeight` is measured.
/// Also places the caret at the end of the restored text, which is where
/// history recall / draft restore leaves the user ready to edit or send.
fn sync_composer_height_soon() {
    let Some(win) = web_sys::window() else {
        return;
    };
    let cb = Closure::once(move || {
        sync_composer_height();
    });
    let _ = win.set_timeout_with_callback_and_timeout_and_arguments_0(
        cb.as_ref().unchecked_ref(),
        0,
    );
    cb.forget();
}

/// Focus the composer and put the caret at the end (#1361 recall).
fn focus_composer_end_soon() {
    let Some(win) = web_sys::window() else {
        return;
    };
    let cb = Closure::once(move || {
        if let Some(el) = web_sys::window()
            .and_then(|w| w.document())
            .and_then(|doc| doc.query_selector("#composer-input").ok().flatten())
            .and_then(|el| el.dyn_into::<web_sys::HtmlTextAreaElement>().ok())
        {
            let _ = el.focus();
            let end = el.value().encode_utf16().count() as u32;
            let _ = el.set_selection_range(end, end);
        }
        sync_composer_height();
    });
    let _ = win.set_timeout_with_callback_and_timeout_and_arguments_0(
        cb.as_ref().unchecked_ref(),
        0,
    );
    cb.forget();
}

/// #1361: route the engine's answer to an in-flight edit & resend
/// rewind. `session.rewound` releases the edited text for sending; a
/// rewind error keeps the draft and sends nothing.
fn note_edit_envelope(
    data: &str,
    state: RwSignal<edit_resend::State>,
    resend_now: RwSignal<Option<String>>,
    note: RwSignal<String>,
) {
    let Ok(v) = serde_json::from_str::<serde_json::Value>(data) else {
        return;
    };
    let typ = v.get("type").and_then(|t| t.as_str()).unwrap_or("");
    let d = v.get("data").cloned().unwrap_or(serde_json::Value::Null);
    let child = d
        .get("parentSessionId")
        .and_then(|p| p.as_str())
        .is_some_and(|p| !p.is_empty())
        || d.get("depth").and_then(|x| x.as_i64()).unwrap_or(0) > 0;
    if child {
        return;
    }
    match typ {
        "session.rewound" => {
            let (next, send) = edit_resend::on_rewound(state.get_untracked());
            state.set(next);
            if let Some(text) = send {
                note.set(String::new());
                resend_now.set(Some(text));
            }
        }
        "engine.error" => {
            let msg = d.get("message").and_then(|m| m.as_str()).unwrap_or("");
            if edit_resend::is_rewind_error(msg) {
                let (next, n) = edit_resend::on_rewind_failed(state.get_untracked(), msg);
                state.set(next);
                if let Some(n) = n {
                    note.set(n);
                }
            }
        }
        _ => {}
    }
}

fn place_composer_caret_end_soon() {
    let Some(win) = web_sys::window() else {
        return;
    };
    let cb = Closure::once(move || {
        if let Some(el) = web_sys::window()
            .and_then(|w| w.document())
            .and_then(|doc| doc.query_selector("#composer-input").ok().flatten())
            .and_then(|el| el.dyn_into::<web_sys::HtmlTextAreaElement>().ok())
        {
            let end = el.value().encode_utf16().count() as u32;
            let _ = el.set_selection_range(end, end);
        }
        sync_composer_height();
    });
    let _ = win.set_timeout_with_callback_and_timeout_and_arguments_0(
        cb.as_ref().unchecked_ref(),
        0,
    );
    cb.forget();
}

/// TUI rule (docs/keybinds.md:99): ↑/↓ navigate prompt history only when
/// they would not move the caret within a multiline draft. Caret offsets
/// from the DOM are UTF-16 units, matching `selection_start`.
fn caret_can_move_up(text: &str, caret_utf16: u32) -> bool {
    let byte = composer_triggers::utf16_to_byte(text, caret_utf16 as usize);
    text[..byte.min(text.len())].contains('\n')
}

fn caret_can_move_down(text: &str, caret_utf16: u32) -> bool {
    let byte = composer_triggers::utf16_to_byte(text, caret_utf16 as usize);
    text[byte.min(text.len())..].contains('\n')
}

/// In-memory sent-prompt history (#1402 slice 1): per loaded page, no
/// persistence and no daemon change. `cursor == None` means the user is
/// on their own draft, not cycling. Cycling stashes that draft and hands
/// it back when the user cycles down past the newest entry.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
struct PromptHistory {
    entries: Vec<String>,
    cursor: Option<usize>,
    stash: String,
    /// Last value handed out by `prev`/`next`; if the live draft no
    /// longer matches it, the draft was edited or replaced since the
    /// recall, so navigation restarts / abandons instead of clobbering.
    recalled: Option<String>,
}

impl PromptHistory {
    fn push(&mut self, raw: &str) {
        let text = raw.trim();
        if text.is_empty() {
            return;
        }
        if self.entries.last().map(String::as_str) != Some(text) {
            self.entries.push(text.to_string());
            if self.entries.len() > PROMPT_HISTORY_CAP {
                let excess = self.entries.len() - PROMPT_HISTORY_CAP;
                self.entries.drain(..excess);
            }
        }
        self.cursor = None;
        self.stash.clear();
        self.recalled = None;
    }

    fn prev(&mut self, current_draft: &str) -> Option<String> {
        if self.entries.is_empty() {
            return None;
        }
        let last = self.entries.len() - 1;
        let idx = match self.cursor {
            None => {
                self.stash = current_draft.to_string();
                last
            }
            Some(i) if self.recalled.as_deref() != Some(current_draft) => {
                // Draft changed since the last recall (edited, or set
                // programmatically): restart from the newest entry and
                // stash the live draft rather than losing it.
                self.stash = current_draft.to_string();
                last
            }
            Some(0) => 0,
            Some(i) => i - 1,
        };
        self.cursor = Some(idx);
        let value = self.entries[idx].clone();
        self.recalled = Some(value.clone());
        Some(value)
    }

    fn next(&mut self, current_draft: &str) -> Option<String> {
        let idx = self.cursor?;
        if self.recalled.as_deref() != Some(current_draft) {
            // Edited since recall: abandon cycling, keep the edit.
            self.cursor = None;
            self.stash.clear();
            self.recalled = None;
            return None;
        }
        if idx + 1 < self.entries.len() {
            self.cursor = Some(idx + 1);
            let value = self.entries[idx + 1].clone();
            self.recalled = Some(value.clone());
            Some(value)
        } else {
            self.cursor = None;
            let value = std::mem::take(&mut self.stash);
            self.recalled = Some(value.clone());
            Some(value)
        }
    }
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
        Kind::Session => {
            let (rows, self_id) = session_catalog();
            let mut items = session_suggestions(&rows, &self_id, &trig.query);
            if items.is_empty() {
                items.push(Suggestion {
                    id: "no-sessions".into(),
                    label: "No sessions match".into(),
                    detail: "An unknown id attaches nothing; the daemon says so on send.".into(),
                    insert: String::new(),
                    selectable: false,
                    replace_all: false,
                });
            }
            ui.ac_index.set(first_selectable(&items));
            ui.ac_items.set(items);
            ui.ac_open.set(true);
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
                // #1550: the @ menu also lists recent sessions by title.
                let (rows, self_id) = session_catalog();
                let mut items = items;
                items.extend(session_suggestions(&rows, &self_id, &q));
                let open = !items.is_empty();
                ac_index.set(first_selectable(&items));
                ac_items.set(items);
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
/// All find-in-transcript matches over a line list, in transcript order.
///
/// The corpus is the whole line (#1576): the title, the full body —
/// long and JSON-ish tool bodies included; the >240-char / `{`/`[`
/// exclusion this projection used to apply is gone — and the collapsed
/// `detail` the transcript folds into a <details>. Detail hits carry
/// `Field::Detail` so the pane can expand the fold they land in and
/// highlight them there. Nothing in the open transcript is excluded,
/// so the find bar needs no scope note: "No matches" now means the
/// string is not in the transcript.
fn transcript_matches(
    list: &[ChatLine],
    query: &str,
    opts: crate::editor::FindOptions,
) -> Vec<transcript_find::TMatch> {
    let rows: Vec<transcript_find::SearchLine<'_>> = list
        .iter()
        .map(|line| transcript_find::SearchLine {
            title: &line.title,
            body: &line.body,
            detail: &line.detail,
        })
        .collect();
    transcript_find::matches_for_lines(&rows, query, opts)
}

#[cfg(test)]
mod transcript_matches_tests {
    use super::*;

    fn line(role: &str, title: &str, body: &str, detail: &str) -> ChatLine {
        ChatLine {
            id: 0,
            role: role.into(),
            title: title.into(),
            body: body.into(),
            badge: String::new(),
            detail: detail.into(),
            key: String::new(),
            at_ms: 0.0,
        }
    }

    #[test]
    fn long_tool_body_is_searched() {
        // >240 chars: the old projection returned "" for this body.
        let body = format!("{}needle", "log line\n".repeat(40));
        assert!(body.chars().count() > 240);
        let list = vec![line("tool", "bash", &body, "")];
        let hits = transcript_matches(
            &list,
            "needle",
            crate::editor::FindOptions::default(),
        );
        assert_eq!(hits.len(), 1);
        assert_eq!(hits[0].line, 0);
        assert_eq!(hits[0].field, transcript_find::Field::Body);
    }

    #[test]
    fn json_tool_body_is_searched() {
        // Starts with `{`: the old projection returned "" for this body.
        let list = vec![line(
            "tool",
            "read",
            "{\"path\": \"/tmp/needle.json\"}",
            "",
        )];
        let hits = transcript_matches(
            &list,
            "needle.json",
            crate::editor::FindOptions::default(),
        );
        assert_eq!(hits.len(), 1);
        assert_eq!(hits[0].field, transcript_find::Field::Body);
    }

    #[test]
    fn folded_detail_hit_carries_its_line_identity() {
        let list = vec![
            line("you", "You", "run the tests", ""),
            line("tool", "bash", "tests ran", "FAIL needle-test panicked"),
            line("strike", "Strike", "done", ""),
        ];
        let hits = transcript_matches(
            &list,
            "needle-test",
            crate::editor::FindOptions::default(),
        );
        assert_eq!(hits.len(), 1);
        assert_eq!(hits[0].line, 1);
        assert_eq!(hits[0].field, transcript_find::Field::Detail);
        assert!(hits[0].in_fold());
    }

    #[test]
    fn absent_string_is_an_honest_zero_over_the_full_corpus() {
        let list = vec![
            line("tool", "bash", "{\"a\": 1}", "long folded output"),
            line("you", "You", "hello", ""),
        ];
        assert!(
            transcript_matches(&list, "not-present", crate::editor::FindOptions::default())
                .is_empty()
        );
        assert_eq!(
            transcript_find::count_label("not-present", 0, 0),
            "No matches"
        );
    }
}

fn note_steer(
    transcript: RwSignal<Transcript>,
    raw: &str,
    notes: &impl Fn(steer_queue::LiveNote),
) {
    let Ok(v) = serde_json::from_str::<serde_json::Value>(raw) else {
        return;
    };
    let typ = v.get("type").and_then(|t| t.as_str()).unwrap_or("");
    let data = v.get("data").cloned().unwrap_or(serde_json::Value::Null);
    for note in steer_queue::notes_from(typ, &data) {
        if let steer_queue::LiveNote::Steered { text, mode } = &note {
            if mode != "queued_fallback" && !text.is_empty() {
                let text = text.clone();
                transcript.update(|t| {
                    if let Some(msg) = t.messages.iter_mut().rev().find(|m| {
                        m.role == crate::transcript::Role::You && (m.body == text || m.retry_text == text)
                    }) {
                        msg.badge = "steered".into();
                    }
                });
            }
        }
        notes(note);
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

fn memory_mode_label(mode: &str) -> &'static str {
    match mode {
        "incognito" => "Incognito",
        "temporary" => "Temporary",
        _ => "Normal",
    }
}

/// Chip text for the memory mode (#1585). The chip is a privacy control:
/// it may only name a mode the daemon has confirmed. Before a successful
/// load (or after a failed one with nothing confirmed) it asserts
/// "Unknown" instead of the invented default "Normal".
fn memory_chip_text(known: bool, mode: &str) -> String {
    if known {
        format!("Memory · {}", memory_mode_label(mode))
    } else {
        "Memory · Unknown".to_string()
    }
}

/// The mode to show after a rejected change (#1585): the last confirmed
/// mode, or unknown when nothing was ever confirmed.
fn revert_memory_mode(previous: Option<String>) -> (bool, String) {
    match previous {
        Some(mode) => (true, mode),
        None => (false, String::new()),
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

/// Member names by session id and escalation sentences by session id,
/// from one team snapshot (#1375).
fn team_signal_maps(team: &TeamSnapshot) -> (HashMap<String, String>, HashMap<String, String>) {
    let mut names = HashMap::new();
    let mut escalations = HashMap::new();
    for (key, member) in &team.members {
        let sid = if member.session_id.is_empty() {
            key.clone()
        } else {
            member.session_id.clone()
        };
        if !member.name.is_empty() {
            names.insert(sid.clone(), member.name.clone());
        }
        if let Some(esc) = &member.escalation {
            escalations.insert(sid, child_signals::escalation_line(esc));
        }
    }
    (names, escalations)
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
            elapsed_s,
            started_ms,
            expanded,
            summary: member.terminal_summary.clone(),
            block_reason: member.block_reason.clone(),
            budget: model::card_budget(member),
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
    provide_context(transcript_attachments::AttachmentAuth { base, token });
    let status = RwSignal::new("connecting…".to_string());
    // Bootstrap capability `container` (#1584): gates Settings → Isolation.
    let container_capable = RwSignal::new(false);
    // Bootstrap capability `workspaceTrust` (#1781): gates the untrusted
    // workspace badge and trust card.
    let workspace_trust_capable = RwSignal::new(false);
    // Bootstrap capability `diag` (#1489): gates Download diagnostics.
    let diag_capable = RwSignal::new(false);
    // Bootstrap capability `ledger` (#1485): gates the Decisions tab.
    let ledger_capable = RwSignal::new(false);
    // Bootstrap capability `issues` (#1500): gates the Issues tab.
    let issues_capable = RwSignal::new(false);
    // Bootstrap capability `projectInit` (#1581): gates Create AGENTS.md.
    let project_init_capable = RwSignal::new(false);
    // #1573: bootstrap `teamControl` plus a live team with members gate the
    // activity panel's Message action; absent otherwise, never disabled.
    let team_control_capable = RwSignal::new(false);
    let team_live = RwSignal::new(false);
    let message_target = RwSignal::new(None::<team_message::Target>);
    // #1381: delegation board rows from the /v1/team poll.
    let delegation_rows = RwSignal::new(Vec::<delegation_board::BoardRow>::new());
    // Bootstrap capability `plugins` (#1570): gates plugin lifecycle.
    let plugins_capable = RwSignal::new(false);
    // Typed config-source list (#1493), from bootstrap `configFiles`.
    let config_files_capable = RwSignal::new(false);
    // #1375: daemon-aggregated team signals from the /v1/team poll — the
    // bounded inter-agent mail, member names for its From/To, the lead id,
    // and each child's latest escalation sentence by session id.
    let team_messages = RwSignal::new(Vec::<crate::model::TeamMessage>::new());
    let team_names = RwSignal::new(HashMap::<String, String>::new());
    let team_lead = RwSignal::new(String::new());
    let team_escalations = RwSignal::new(HashMap::<String, String>::new());
    // Bootstrap capability `loops` (#1588): session /loop jobs.
    let loops_capable = RwSignal::new(false);
    let loop_rows = RwSignal::new(Vec::<session_loops::SessionLoop>::new());
    let loop_note = RwSignal::new(String::new());
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
    // Session restore (#1391): false until the first refresh that got a
    // roots response has run the one-time saved-session decision, so
    // later polls that find `active_root` empty again (e.g. the open
    // session was deleted) use the plain fallback, not a re-restore.
    let restore_attempted = RwSignal::new(false);
    // Flattened session order published by SessionPanel for edge-swipe
    // navigation (#1490 slice 5): (open_id, live) pairs in display order.
    let session_nav = RwSignal::new(Vec::<(String, bool)>::new());
    // Start point of a candidate edge swipe on the chat section.
    let swipe_start = RwSignal::new(None::<(i32, i32)>);
    let lines = RwSignal::new(Vec::<ChatLine>::new());
    let transcript = RwSignal::new(Transcript::new());
    let ux_note = RwSignal::new(String::new());
    let activity = RwSignal::new(Vec::<ActivityRow>::new());
    let team_note = RwSignal::new(String::new());
    let children_note = RwSignal::new(String::new());
    let draft = RwSignal::new(String::new());
    // Sent-prompt history (#1402): in-memory only, per loaded page.
    let prompt_history = RwSignal::new(PromptHistory::default());
    // Auto-grow (#1411): every programmatic draft change (per-session
    // restore, queue recall, send clearing the draft, history recall,
    // suggestions, dictation) resizes the composer once Leptos has
    // written the new value into the textarea. Typing resizes directly
    // from the input handler, where the DOM value is already current.
    Effect::new(move |_| {
        let _ = draft.get();
        sync_composer_height_soon();
    });
    // Per-session drafts (#1367): the composer text belongs to the
    // viewed session. Every switch — whichever of the active_root
    // setters made it — saves the outgoing session's draft and restores
    // the incoming one; every edit (typing, recall, send clearing it)
    // is persisted under `strike.draft.<root>`.
    let draft_book = StoredValue::new_local(drafts::book());
    Effect::new(move |_| {
        let root = active_root.get();
        let text = draft.get();
        if let Some(restored) = draft_book
            .try_update_value(|book| book.sync(&root, &text))
            .flatten()
        {
            draft.set(restored);
        }
    });
    let loading = RwSignal::new(true);
    let meta_epoch = RwSignal::new(LoadEpoch::new());
    let meta_base = RwSignal::new(load("strike.base"));
    let tick = RwSignal::new(0u32);
    let stream_note = RwSignal::new(String::new());
    // #1386: one notice per session start for skill/agent load diagnostics
    // this browser has not announced yet. Cleared only by the user.
    let load_notice = RwSignal::new(String::new());
    // Keys announced this page load, merged with the stored list so a
    // browser that refuses localStorage writes still never re-nags.
    let warn_announced = RwSignal::new(Vec::<String>::new());
    let load_error = RwSignal::new(String::new());
    let gateway = RwSignal::new("checking".to_string());
    // Offline outbox (#1325): text sends queued while the gateway is down.
    let outbox = outbox::Outbox::new(base, token);
    let gateway_kind = RwSignal::new(StatusKind::Idle);
    let gateway_label = RwSignal::new("Checking".to_string());
    let last_beat = RwSignal::new(0.0f64);
    let boot_ms = now_ms();
    let focus = RwSignal::new(false);
    let back_stack = RwSignal::new(Vec::<String>::new());
    let stale_warning = RwSignal::new(None::<stale_resume::StaleWarning>);
    // Open-file history and recents (#1395): in-memory, global across
    // sessions like the Files pane, mirroring the session stacks here.
    let file_history = RwSignal::new(file_history::FileHistory::default());
    let forward_stack = RwSignal::new(Vec::<String>::new());
    // Kept for the shell host bridge (palette session list / breadcrumb
    // label), which merges with the browser-local pins. The session panel
    // itself uses the server-backed list from /v1/session-list.
    let pins = RwSignal::new(load_string_list("strike.pins"));
    let seen = RwSignal::new(load_seen());
    let children = RwSignal::new(Vec::<model::ChildSession>::new());
    let perm_mode = RwSignal::new("default".to_string());
    // Session dials (#1569 Fast, #1572 autonomy). Both signals hold only
    // daemon-reported state: autonomy is seeded from bootstrap/status and
    // afterwards changes solely on `autonomy.selected`; fast changes
    // solely on `fast.selected`. Neither is set optimistically on click.
    let autonomy = RwSignal::new("supervised".to_string());
    let autonomy_menu = RwSignal::new(false);
    // Live workflow run (#1574): daemon-reported only — seeded from
    // bootstrap status, then moved solely by `phase.changed`.
    let wf_run = RwSignal::new(workflow_run::WorkflowRun::default());
    Effect::new(move |_| {
        workflow_run::set_active(wf_run.with(|w| w.is_active()));
    });
    let fast_on = RwSignal::new(false);
    let asks = RwSignal::new(Vec::<approvals::Ask>::new());
    let elicitations = RwSignal::new(Vec::<elicitation::Pending>::new());
    let debug_state = RwSignal::new(None::<debug_mode::State>);
    let questions = RwSignal::new(Vec::<questions::Pending>::new());
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
    // Agent picker (#1563): the selectable agents from bootstrap and the
    // daemon-confirmed running agent. `agent_name` changes only from the
    // bootstrap status or an `agent.selected` event, never on click.
    let agent_list = RwSignal::new(Vec::<crate::model::AgentInfo>::new());
    let agent_name = RwSignal::new(String::new());
    let agent_note = RwSignal::new(String::new());
    let agent_note_after = RwSignal::new(0i64);
    let effort_level = RwSignal::new(String::new());
    let context_used = RwSignal::new(0i64);
    let context_limit = RwSignal::new(0i64);
    let context_fit = RwSignal::new(String::new());
    // Context doctor (#1537): inspect.prompt breakdown dialog.
    let doctor = context_doctor::DoctorState::new();
    // Compact now (#1567): true from sending the `compact` op until the
    // daemon's own `compaction.completed` event lands — the meter only
    // ever moves from daemon events, never optimistically.
    let compacting = RwSignal::new(false);
    // Daemon refusal text carried from the engine.error arm to the end
    // of the same envelope handler (#1567).
    let compact_refused = RwSignal::new(String::new());
    let model_defaults = RwSignal::new(ModelDefaults::default());
    // False until GET /v1/settings succeeds. Empty defaults are not "none saved".
    let defaults_known = RwSignal::new(false);
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
    // The requested theme id. It may name a daemon catalog theme (#1529);
    // themes::paint decides how it is drawn and falls back to Strike with
    // a note when the catalog does not know it.
    let theme_id = RwSignal::new({
        let t = load("strike.theme");
        if t.trim().is_empty() {
            "strike".to_string()
        } else {
            t
        }
    });
    let scheme_id: RwSignal<String> = RwSignal::new(if load("strike.scheme") == "light" {
        "light".to_string()
    } else {
        "dark".to_string()
    });
    let themes_state = RwSignal::new(theme_catalog::CatalogState::Loading);
    let theme_note = RwSignal::new(String::new());
    let repaint_theme = move || {
        let paint = themes::paint(
            &theme_id.get_untracked(),
            &scheme_id.get_untracked(),
            &themes_state.get_untracked(),
        );
        theme_note.set(paint.note().to_string());
        if let theme_catalog::Paint::Fallback { keep: false, .. } = paint {
            theme_id.set(theme_catalog::DEFAULT_ID.to_string());
        }
    };
    let font_id = RwSignal::new(appearance::normalize_font(&load("strike.font")));
    let density_id = RwSignal::new(appearance::normalize_density(&load("strike.density")));
    let onboarded = RwSignal::new(load("strike.onboarded") == "1");
    // Daemon-side onboarding acknowledgement (#1552): localStorage is only
    // a per-browser fast path. Until GET /v1/onboarding has answered for
    // the current base, the first-run card stays hidden, so a browser
    // against an already-onboarded daemon never flashes it. A failed
    // check (old or attach-only daemon) counts as checked and falls back
    // to the localStorage behaviour.
    let onboarding_daemon_checked = RwSignal::new(load("strike.onboarded") == "1");
    Effect::new(move |_| {
        let b = base.get();
        if onboarded.get_untracked() {
            onboarding_daemon_checked.set(true);
            return;
        }
        let t = token.get_untracked();
        spawn_local(async move {
            let ack = match get_json::<serde_json::Value>(&b, &t, "/v1/onboarding").await {
                Ok(v) => v
                    .get("acknowledged")
                    .and_then(|x| x.as_bool())
                    .unwrap_or(false),
                Err(_) => false,
            };
            // Stale-response guard: the gateway changed while the check
            // was in flight; the effect re-runs for the new base.
            if base.get_untracked() != b {
                return;
            }
            if ack {
                save("strike.onboarded", "1");
                onboarded.set(true);
            }
            onboarding_daemon_checked.set(true);
        });
    });
    let queue_items = RwSignal::new(Vec::<steer_queue::QueueItem>::new());
    // Agent plan checklist (#1354): the live todowrite list.
    let plan = RwSignal::new(plan_card::PlanState::default());
    let queue_focus = RwSignal::new(String::new());
    let editing_id = RwSignal::new(String::new());
    let edit_draft = RwSignal::new(String::new());
    let steer_note = RwSignal::new(String::new());
    let steer_mode = RwSignal::new(String::new());
    // Edit last message & resend (#1361): where the edit stands, the
    // edited text released once the engine confirms the rewind, and the
    // composer banner note (refusals, failures).
    let last_edit = RwSignal::new(edit_resend::State::Idle);
    let resend_now = RwSignal::new(None::<String>);
    let edit_note = RwSignal::new(String::new());
    let queue_note = RwSignal::new(String::new());
    let hold_bypass = RwSignal::new(false);
    // Draft-size second-press arm (#1542): the exact draft text whose
    // first Send press was held at projected-critical. Empty = unarmed.
    let draft_armed = RwSignal::new(String::new());
    // Cancel and Stop drop work. The first click arms the button; the
    // second click is the confirm. Escape still stops immediately.
    let confirm_cancel = RwSignal::new(String::new());
    let confirm_stop = RwSignal::new(false);
    let live_busy = RwSignal::new(false);
    let live_busy_root = RwSignal::new(String::new());
    let side_tab = RwSignal::new("activity".to_string());
    let memory_mode = RwSignal::new("normal".to_string());
    // False until the daemon confirms a mode (#1585): the chip must not
    // assert the invented "normal" default before a successful load.
    let memory_mode_known = RwSignal::new(false);
    let memory_menu = RwSignal::new(false);
    let storage_rev = RwSignal::new(0u32);
    // files.invalidated (#1380): bumped (debounced) when the daemon says
    // files changed underneath the session; the Files/Changes load Effect
    // observes it so the open panel refetches.
    let files_rev = RwSignal::new(0u32);
    let files_debounce: Rc<Cell<files_invalidated::Debounce>> = Rc::new(Cell::new(Default::default()));
    // Lessons capture (#1376): rows show "Remember this" only while the
    // confirmed memory mode allows storing; the draft is page-owned.
    let lesson_gate = Signal::derive(move || {
        lessons::capture_allowed(memory_mode_known.get(), &memory_mode.get())
    });
    provide_context(lessons::CaptureGate(lesson_gate));
    let lesson_draft = RwSignal::new(None::<lessons::Draft>);
    // Find in transcript (#1342): bar visibility, query, current match,
    // and the transcript scroll offset saved when the bar opened so
    // Escape / Close can restore it.
    let tf_open = RwSignal::new(false);
    let tf_query = RwSignal::new(String::new());
    // Find options (#1409/#1419): per-bar, in-memory only, both off by
    // default — independent from the editor bar's toggles.
    let tf_match_case = RwSignal::new(false);
    let tf_whole_word = RwSignal::new(false);
    let tf_idx = RwSignal::new(0usize);
    // Global transcript search (#1355): overlay state and the pending
    // jump (session id, query) handed to find-in-transcript once that
    // session's lines have loaded.
    let gs_open = RwSignal::new(false);
    let gs_query = RwSignal::new(String::new());
    let gs_jump = RwSignal::new(None::<(String, String)>);
    let tf_saved_scroll = RwSignal::new(0.0f64);
    let doc_path = RwSignal::new(String::new());
    let doc_line = RwSignal::new(0u32);
    let doc_diff = RwSignal::new(false);
    // LSP diagnostics (#1404): last /v1/diagnostics result, a refresh
    // counter, and a generation so a slow older fetch cannot overwrite a
    // newer one.
    let diag_state = RwSignal::new(diagnostics::DiagState::Idle);
    let diag_rev = RwSignal::new(0u32);
    let diag_gen = RwSignal::new(0u32);
    // Search in files (#1406): the Files-pane box's query and a run
    // counter the palette command bumps to start a search.
    let sif_query = RwSignal::new(String::new());
    let sif_run = RwSignal::new(0u32);
    let doc_editing = RwSignal::new(false);
    // Open a file in the document viewer: the one path the Files pane's
    // Open and quick-open (#1396) share.
    let open_doc = move |p: &str| {
        doc_line.set(0);
        doc_diff.set(false);
        doc_path.set(p.to_string());
    };
    let layout = RwSignal::new(chat_mode::normalize_layout(&load("strike.layout")));
    let sessions_drawer = RwSignal::new(false);
    let panel_drawer = RwSignal::new(false);
    // Session delete (#1358): the session the confirm dialog is armed
    // for. `None` means no deletion is pending; setting it never issues
    // a request by itself.
    let delete_target = RwSignal::new(None::<session_delete::DeleteTarget>);
    // Session rewind (#1336): the preview dialog's open request.
    let rewind_target = RwSignal::new(None::<rewind::RewindTarget>);
    let pending_files = RwSignal::new(Vec::<chat_mode::PendingAttachment>::new());
    // Quote a message (#1362): the line attached to the next send. One
    // at a time; quoting another line replaces it.
    let quote_draft = RwSignal::new(None::<quote_message::Quote>);
    // Visual annotate (#1545): marks per pending image id, and the image
    // open in the overlay. Marks live and die with the composer session.
    let annotate_marks: annotate::MarkStore = RwSignal::new(HashMap::new());
    let annotate_target = RwSignal::new(None::<annotate::Target>);
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
    // Sentinel until a listing commits, so the first paint cannot treat an
    // empty vec as this folder or as the previous folder's rows.
    let files_for = RwSignal::new("\u{0}".to_string());
    let files_loading = RwSignal::new(false);
    let files_epoch = RwSignal::new(LoadEpoch::new());
    let changes = RwSignal::new(Vec::<model::ChangedFile>::new());
    let changes_error = RwSignal::new(String::new());
    let phone_pane = RwSignal::new(mobile::PANE_CHAT.to_string());
    repaint_theme();
    Effect::new(move |_| {
        themes_state.track();
        repaint_theme();
    });
    Effect::new(move |_| {
        let (b, t) = (base.get(), token.get());
        themes_state.set(theme_catalog::CatalogState::Loading);
        spawn_local(async move {
            let state = themes::fetch(&b, &t).await;
            if base.get_untracked() == b && token.get_untracked() == t {
                themes_state.set(state);
            }
        });
    });
    appearance::apply_type(&font_id.get_untracked(), &density_id.get_untracked());
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
                    team_live.set(team.available && !rows.is_empty());
                    let board = delegation_board::board_rows(&team);
                    if delegation_rows.get_untracked() != board {
                        delegation_rows.set(board);
                    }
                    let (names, escalations) = team_signal_maps(&team);
                    if team_names.get_untracked() != names {
                        team_names.set(names);
                    }
                    if team_escalations.get_untracked() != escalations {
                        team_escalations.set(escalations);
                    }
                    if team_lead.get_untracked() != team.lead_id {
                        team_lead.set(team.lead_id.clone());
                    }
                    if team_messages.get_untracked() != team.messages {
                        team_messages.set(team.messages.clone());
                    }
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
                Err(e) => {
                    team_live.set(false);
                    team_note.set(e);
                }
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

    // new_session parks an activation failure here so the meta refresh,
    // which rewrites the connection status, does not erase it.
    let activation_hold = RwSignal::new(None::<String>);

    let refresh_meta = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let ticket = begin(meta_epoch);
        // Another machine's sessions are not a loading state. Clear them
        // before the new pipeline returns; a superseded pipeline cannot
        // write them back.
        if gateway_switched(&meta_base.get_untracked(), &b) {
            sessions.set(Vec::new());
            workspaces.set(Vec::new());
            workspace_recents.set(Vec::new());
            roots.set(Vec::new());
            active_workspace.set(String::new());
            meta_base.set(b.clone());
        }
        spawn_local(async move {
            let live = move || meta_epoch.get_untracked().accept(ticket);
            let mut err = String::new();
            let mut gateway_up = false;
            match get_json::<Health>(&b, &t, "/health").await {
                Ok(h) if h.ok => {
                    if !live() {
                        poll_team();
                        return;
                    }
                    let was = gateway.get_untracked();
                    let root_now = active_root.get_untracked();
                    gateway.set("connected".into());
                    last_beat.set(now_ms());
                    gateway_up = true;
                    // Deliver anything queued while offline (#1325).
                    outbox.kick();
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
                    if !live() {
                        poll_team();
                        return;
                    }
                    gateway.set("unreachable".into());
                    gateway_kind.set(StatusKind::Disconnected);
                    gateway_label.set("Unreachable".into());
                    status.set("Unreachable".into());
                    err = "The daemon answered, but it is not ok.".into();
                }
                Err(e) => {
                    if !live() {
                        poll_team();
                        return;
                    }
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
                        if !live() {
                            poll_team();
                            return;
                        }
                        let kind = StatusKind::parse(&board.gateway);
                        gateway_kind.set(kind);
                        gateway_label.set(kind.label().into());
                    }
                    Err(e) => {
                        if !live() {
                            poll_team();
                            return;
                        }
                        if err.is_empty() {
                            err = format!("Status board failed to load: {e}");
                        }
                    }
                }
            }
            match get_json::<WorkspacesResponse>(&b, &t, "/v1/workspaces").await {
                Ok(r) => {
                    if !live() {
                        poll_team();
                        return;
                    }
                    active_workspace.set(r.active_id);
                    workspaces.set(r.workspaces);
                    workspace_recents.set(r.recents);
                }
                Err(e) => {
                    if !live() {
                        poll_team();
                        return;
                    }
                    if e.contains("HTTP 401") || e.contains("HTTP 403") {
                        gateway.set("unauthorized".into());
                    }
                    if err.is_empty() {
                        err = format!("Workspaces failed to load: {e}");
                    }
                }
            }
            // Captured (not applied inline) so the open-session decision
            // below can validate the saved session (#1391) against both
            // the live roots and the saved sessions before picking one.
            let mut roots_resp: Option<RootsResponse> = None;
            match get_json::<RootsResponse>(&b, &t, "/v1/roots").await {
                Ok(r) => {
                    if !live() {
                        poll_team();
                        return;
                    }
                    roots.set(r.roots.clone());
                    roots_resp = Some(r);
                }
                Err(e) => {
                    if !live() {
                        poll_team();
                        return;
                    }
                    if err.is_empty() {
                        err = format!("Sessions failed to load: {e}");
                    }
                }
            }
            let mut session_ids: Vec<String> = Vec::new();
            let mut sessions_ok = false;
            match get_json::<SessionsResponse>(&b, &t, "/v1/sessions").await {
                Ok(r) => {
                    if !live() {
                        poll_team();
                        return;
                    }
                    session_ids = r.sessions.iter().map(|s| s.id.clone()).collect();
                    sessions_ok = true;
                    sessions.set(r.sessions);
                }
                Err(e) => {
                    if !live() {
                        poll_team();
                        return;
                    }
                    if err.is_empty() {
                        err = format!("Saved sessions failed to load: {e}");
                    }
                }
            }
            if active_root.get_untracked().is_empty() {
                if let Some(r) = &roots_resp {
                    let root_ids: Vec<String> =
                        r.roots.iter().map(|x| x.id.clone()).collect();
                    // Only the first decision consults storage; after
                    // that, an empty `active_root` means the open
                    // session went away and the plain fallback applies.
                    let saved = if restore_attempted.get_untracked() {
                        String::new()
                    } else {
                        session_restore::saved_id()
                    };
                    let pick = session_restore::pick_initial(
                        &saved,
                        &root_ids,
                        &session_ids,
                        &r.active_id,
                    );
                    restore_attempted.set(true);
                    // A stale id is only declared (and cleared) when
                    // the saved-session list actually loaded — a failed
                    // list fetch must not erase a dormant session's id.
                    if pick.clear_saved && sessions_ok {
                        session_restore::clear();
                    }
                    if !pick.id.is_empty() {
                        if pick.resume {
                            // Dormant saved session: set the view
                            // optimistically (like `activate_root`) and
                            // resume it; a failed resume clears both the
                            // view and the saved id so the next poll
                            // lands on the fallback, never an empty
                            // screen for a session that cannot resume.
                            let id = pick.id.clone();
                            active_root.set(id.clone());
                            session_restore::persist(&id);
                            let (rb, rt) = (b.clone(), t.clone());
                            spawn_local(async move {
                                let path = format!("/v1/roots/{id}/resume");
                                if let Err(e) =
                                    post_json(&rb, &rt, &path, serde_json::json!({})).await
                                {
                                    status.set(format!("resume failed: {e}"));
                                    if active_root.get_untracked() == id {
                                        active_root.set(String::new());
                                    }
                                    // Only forget the id that failed —
                                    // a newer switch may already have
                                    // persisted a different session.
                                    if session_restore::saved_id() == id {
                                        session_restore::clear();
                                    }
                                }
                            });
                        } else {
                            // View-local restore: no activate POST, so
                            // two devices never fight over the daemon's
                            // active id — each keeps its own place.
                            active_root.set(pick.id.clone());
                            session_restore::persist(&pick.id);
                        }
                    }
                }
            }
            let root = active_root.get_untracked();
            if !root.is_empty() {
                let path = format!("/v1/bootstrap?root={}", api::js_encode(&root));
                match get_json::<Bootstrap>(&b, &t, &path).await {
                    Ok(boot) => {
                        match bootstrap_gate(
                            &meta_epoch.get_untracked(),
                            ticket,
                            &root,
                            &active_root.get_untracked(),
                        ) {
                            BootstrapGate::Superseded => {
                                poll_team();
                                return;
                            }
                            BootstrapGate::SkipRoot => {}
                            BootstrapGate::Paint => {
                                if let Some(st) = boot.status {
                                    provider_name.set(st.provider);
                                    model_name.set(st.model);
                                    agent_name.set(st.agent);
                                    effort_level.set(st.effort);
                                    context_used.set(st.context_used);
                                    context_limit.set(st.context_limit);
                                    context_fit.set(st.context_fit);
                                    if !st.permission_mode.is_empty() {
                                        perm_mode.set(st.permission_mode);
                                    }
                                    if !st.autonomy.is_empty() {
                                        autonomy.set(
                                            crate::model::autonomy_or_default(&st.autonomy)
                                                .to_string(),
                                        );
                                    }
                                    crate::fallback::seed(&st.fallback_provider, &st.fallback_model);
                                    crate::verbosity::seed(&st.verbosity, &st.verbosity_delivery);
                                    wf_run.set(workflow_run::WorkflowRun::from_status(
                                        &st.workflow,
                                        &st.phase,
                                    ));
                                    session_cwd.set(st.cwd);
                                    live_busy.set(st.busy);
                                    live_busy_root.set(root);
                                }
                                // #1574: refresh the Start workflow… choices
                                // from the daemon catalog for this session.
                                {
                                    let (cb, ct) = (b.clone(), t.clone());
                                    spawn_local(async move {
                                        if let Ok(list) = get_json::<workflow_run::CatalogList>(
                                            &cb,
                                            &ct,
                                            "/v1/workflows",
                                        )
                                        .await
                                        {
                                            workflow_run::set_catalog(
                                                workflow_run::catalog_choices(&list.workflows),
                                            );
                                        }
                                    });
                                }
                                container_capable.set(boot.capabilities.container);
                                workspace_trust_capable.set(boot.capabilities.workspace_trust);
                                diag_capable.set(boot.capabilities.diag);
                                crate::storage::set_ledger_capable(boot.capabilities.ledger);
                                ledger_capable.set(boot.capabilities.ledger);
                                crate::local_issues::set_issues_capable(boot.capabilities.issues);
                                issues_capable.set(boot.capabilities.issues);
                                project_init_capable.set(boot.capabilities.project_init);
                                team_control_capable.set(boot.capabilities.team_control);
                                plugins_capable.set(boot.capabilities.plugins);
                                config_files_capable.set(boot.capabilities.config_files);
                                crate::update_check::boot(b.clone(), t.clone(), boot.capabilities.update_check);
                                skills.set(boot.skills);
                                {
                                    let mut announced = crate::load_warnings::parse_seen(&load(crate::load_warnings::SEEN_KEY));
                                    announced.extend(warn_announced.get_untracked());
                                    let fresh = crate::load_warnings::unseen(&boot.load_warnings, &announced);
                                    if !fresh.is_empty() {
                                        load_notice.set(crate::load_warnings::notice_text(&fresh));
                                        let kept = crate::load_warnings::remember(&announced, &fresh);
                                        warn_announced.set(kept.clone());
                                        if let Ok(raw) = serde_json::to_string(&kept) {
                                            save(crate::load_warnings::SEEN_KEY, &raw);
                                        }
                                    }
                                }
                                agent_list.set(boot.agents);
                                loops_capable.set(boot.capabilities.loops);
                                commands.set(if boot.capabilities.loops {
                                    session_loops::with_loop_commands(boot.composer_commands)
                                } else {
                                    boot.composer_commands
                                });
                                if !catalog_once.get_untracked() {
                                    catalog_once.set(true);
                                    load_model_catalog();
                                }
                            }
                        }
                    }
                    Err(e) => {
                        match bootstrap_gate(
                            &meta_epoch.get_untracked(),
                            ticket,
                            &root,
                            &active_root.get_untracked(),
                        ) {
                            BootstrapGate::Superseded => {
                                poll_team();
                                return;
                            }
                            BootstrapGate::SkipRoot => {}
                            BootstrapGate::Paint => {
                                model_note.set(format!("Live status unavailable: {e}"));
                            }
                        }
                    }
                }
            }
            let settings = get_json::<ModelDefaults>(&b, &t, "/v1/settings").await;
            if !live() {
                poll_team();
                return;
            }
            match settings {
                Ok(defaults) => {
                    model_defaults.set(defaults);
                    defaults_known.set(true);
                    model_note.set(outcome::merge_defaults_note(
                        &model_note.get_untracked(),
                        None,
                    ));
                }
                Err(e) => {
                    defaults_known.set(false);
                    model_note.set(outcome::merge_defaults_note(
                        &model_note.get_untracked(),
                        Some(&e),
                    ));
                }
            }
            if clear_loading(&meta_epoch.get_untracked(), ticket) {
                load_error.set(err);
                loading.set(false);
                if let Some(msg) = activation_hold.get_untracked() {
                    activation_hold.set(None);
                    status.set(msg.clone());
                    ux_note.set(msg);
                }
            }
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
    let debounce_fx = files_debounce.clone();
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
            children_note.set(String::new());
            asks.set(Vec::new());
            elicitations.set(Vec::new());
            debug_state.set(None);
            questions.set(Vec::new());
            live_audit.set(Vec::new());
            confirm.set(None);
            wipe_on_replay.set(false);
            // Find belongs to the session that was open (#1342).
            tf_open.set(false);
            tf_query.set(String::new());
            tf_idx.set(0);
            queue_items.set(Vec::new());
            plan.set(plan_card::PlanState::default());
            editing_id.set(String::new());
            edit_draft.set(String::new());
            steer_note.set(String::new());
            steer_mode.set(String::new());
            queue_note.set(String::new());
            hold_bypass.set(false);
            draft_armed.set(String::new());
            confirm_cancel.set(String::new());
            confirm_stop.set(false);
        } else if mobile::resume_after(cursor_fx.get(), false).is_some() {
            // Same session, known SSE cursor: keep the column and ask
            // the daemon for events after that offset.
            wipe_on_replay.set(false);
        } else if !root.is_empty() {
            wipe_on_replay.set(true);
        }
        effort_level.set(String::new());
        // A new session's dials are unknown until its bootstrap/status or
        // its replayed confirmations land — never carry the previous
        // session's Fast/autonomy across the switch.
        fast_on.set(false);
        autonomy.set("supervised".to_string());
        crate::fallback::reset();
        crate::verbosity::reset();
        autonomy_menu.set(false);
        wf_run.set(workflow_run::WorkflowRun::default());
        context_used.set(0);
        context_limit.set(0);
        context_fit.set(String::new());
        compacting.set(false);
        if root.is_empty() {
            stream_note.set(String::new());
            return;
        }
        mark_seen(root.clone());
        let (cb, ct, child_root) = (b.clone(), token.get_untracked(), root.clone());
        spawn_local(async move {
            let path = format!("/v1/sessions/{}/children", api::js_encode(&child_root));
            match get_json::<ChildrenResponse>(&cb, &ct, &path).await {
                Ok(found) => {
                    children.set(found.sessions);
                    children_note.set(String::new());
                }
                Err(e) => children_note.set(outcome::children_failure_note(&e)),
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
        let debounce_msg = debounce_fx.clone();
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
                    if matches!(last_edit.get_untracked(), edit_resend::State::Reverting { .. }) {
                        note_edit_envelope(&data, last_edit, resend_now, edit_note);
                    }
                    doctor.on_envelope(&data, &titled_root);
                    let mut plan_changed = false;
                    plan.update_untracked(|p| plan_changed = p.apply_envelope(&data));
                    if plan_changed {
                        plan.notify();
                    }
                    let on_note = |note: steer_queue::LiveNote| match note {
                        steer_queue::LiveNote::Queue(items) => {
                            let editing = editing_id.get_untracked();
                            if !steer_queue::editing_still_valid(&items, &editing) {
                                editing_id.set(String::new());
                            }
                            let armed = confirm_cancel.get_untracked();
                            if !armed.is_empty() && !items.iter().any(|it| it.id == armed) {
                                confirm_cancel.set(String::new());
                            }
                            queue_items.set(items);
                            queue_note.set(String::new());
                        }
                        steer_queue::LiveNote::Steered { mode, .. } => {
                            steer_mode.set(mode.clone());
                            steer_note.set(steer_queue::steer_banner(&mode).to_string());
                            queue_note.set(String::new());
                        }
                        steer_queue::LiveNote::TurnStarted => {
                            live_busy.set(true);
                            doctor.on_turn_started();
                            live_busy_root.set(active_root.get_untracked());
                            if steer_mode.get_untracked() == "queued_fallback" {
                                steer_note.set(String::new());
                                steer_mode.set(String::new());
                            }
                        }
                        steer_queue::LiveNote::TurnFinished => {
                            live_busy.set(false);
                            doctor.on_turn_finished(
                                base.get_untracked(),
                                token.get_untracked(),
                                active_root.get_untracked(),
                            );
                            confirm_stop.set(false);
                            if context_fit.get_untracked() == "critical" {
                                context_fit.set(String::new());
                            }
                            if steer_mode.get_untracked() != "queued_fallback" {
                                steer_note.set(String::new());
                                steer_mode.set(String::new());
                            }
                        }
                        steer_queue::LiveNote::Usage { .. } | steer_queue::LiveNote::Fit { .. } => {}
                        steer_queue::LiveNote::EngineError { message, .. } => {
                            queue_note.set(message.clone());
                            if compacting.get_untracked()
                                && crate::model_menu::is_compact_refusal(&message)
                            {
                                compacting.set(false);
                                compact_refused.set(message.clone());
                            }
                            if message.contains("no active turn") || message.contains("already waiting") {
                                steer_note.set(String::new());
                                steer_mode.set(String::new());
                            }
                        }
                    };
                    note_steer(transcript_cb, &data, &on_note);
                    apply_perm_notes(&data, asks_cb, perm_mode, audit_cb);
                    elicitation_ui::apply_elicitation_notes(&data, elicitations);
                    debug_mode_ui::apply_debug_envelope(&data, debug_state);
                    questions_ui::apply_question_notes(&data, questions);
                    // Mirror into the ChatLine shape the side chat and the
                    // activity panel still consume.
                    lines_cb.set(transcript_cb.with_untracked(|t| t.chat_lines()));
                    let artifact_rev = transcript_cb.with_untracked(|t| t.artifact_updates);
                    if artifact_rev != storage_rev.get_untracked() {
                        storage_rev.set(artifact_rev);
                    }
                    if files_invalidated::is_event(&data) {
                        files_invalidated::refresh_soon(debounce_msg.clone(), files_rev);
                    }
                    let prev = LiveModelState {
                        provider: provider_name.get_untracked(),
                        model: model_name.get_untracked(),
                        effort: effort_level.get_untracked(),
                        fast: fast_on.get_untracked(),
                        autonomy: autonomy.get_untracked(),
                        context_used: context_used.get_untracked(),
                        context_limit: context_limit.get_untracked(),
                        context_fit: context_fit.get_untracked(),
                        note: model_note.get_untracked(),
                        note_after: model_note_after.get_untracked(),
                    };
                    let agent_next = crate::agent_picker::reduce_envelope(
                        crate::agent_picker::AgentState {
                            current: agent_name.get_untracked(),
                            note: agent_note.get_untracked(),
                            note_after: agent_note_after.get_untracked(),
                        },
                        &data,
                    );
                    agent_name.set(agent_next.current);
                    agent_note.set(agent_next.note);
                    agent_note_after.set(agent_next.note_after);
                    let wf_next = workflow_run::reduce(wf_run.get_untracked(), &data);
                    if wf_next != wf_run.get_untracked() {
                        wf_run.set(wf_next);
                    }
                    let next = reduce_envelope(prev, &data);
                    crate::fallback::observe(&data);
                    crate::verbosity::observe(&data);
                    provider_name.set(next.provider);
                    model_name.set(next.model);
                    effort_level.set(next.effort);
                    fast_on.set(next.fast);
                    if !next.autonomy.is_empty() {
                        autonomy.set(next.autonomy);
                    }
                    context_used.set(next.context_used);
                    context_limit.set(next.context_limit);
                    context_fit.set(next.context_fit);
                    model_note.set(next.note);
                    model_note_after.set(next.note_after);
                    if data.contains("\"compaction.completed\"") {
                        compacting.set(false);
                    }
                    // Set after reduce_envelope so its note does not
                    // overwrite the daemon's refusal (#1567).
                    let refused = compact_refused.get_untracked();
                    if !refused.is_empty() {
                        compact_refused.set(String::new());
                        model_note.set(format!("compact refused: {refused}"));
                    }
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

    // Orphan settle on load (#1506): a replayed log that ends mid-tool
    // leaves rows `running` with no turn boundary to sweep them. Once the
    // daemon reports the open session idle, those calls cannot still be
    // in flight, so settle them. Tracks roots only (refreshed by polls
    // and the status stream); a stale idle that races a live call is
    // healed by that call's `tool.end`, which matches by callId.
    Effect::new(move |_| {
        let root = active_root.get();
        let idle = roots.with(|rows| model::root_reported_idle(rows, &root));
        if !idle || !transcript.with_untracked(|t| t.has_running_tools()) {
            return;
        }
        transcript.update(|t| t.settle_orphaned_tools());
        lines.set(transcript.with_untracked(|t| t.chat_lines()));
    });

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

    // #1405 tab title: the notification bell publishes its unread count
    // into this signal (see notify::set_unread_host below); the chrome
    // effect combines it with unread sessions.
    let notify_unread = RwSignal::new(0usize);
    Effect::new(move |_| {
        let kind = gateway_kind.get();
        let root_rows = roots.get();
        let saved = sessions.get();
        let active = active_root.get();
        let blocking = tab_title::blocking_count(&root_rows);
        let unread = tab_title::unread_session_count(&root_rows, &saved, &active)
            + notify_unread.get();
        // Favicon behaviour is unchanged (status module owns it); only
        // the title text follows the #1405 waiting/unread precedence.
        apply_document_favicon(kind);
        tab_title::apply_title(blocking, unread);
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
        if held.held && !hold_bypass.get_untracked() {
            model_note.set(held.hold_message);
            return false;
        }
        hold_bypass.set(false);
        let root = active_root.get_untracked();
        if root.is_empty() {
            status.set("no live session yet".into());
            return false;
        }
        let op = steer_queue::send_op(
            session_busy(&root),
            &enter_mode.get_untracked(),
            steer_queue::SendWhich::Primary,
        );
        queue_note.set(String::new());
        if op == "steer" {
            steer_note.set(steer_queue::STEER_WAITING.into());
            steer_mode.set(String::new());
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Err(e) = post_op(&b, &t, &root, op, serde_json::json!({"text": text})).await {
                if op == "steer" && steer_note.get_untracked() == steer_queue::STEER_WAITING {
                    steer_note.set(String::new());
                }
                queue_note.set(format!("Send failed: {e}"));
                status.set(format!("send failed: {e}"));
            }
        });
        true
    };

    // Edit last message & resend (#1361). An edit ends quietly when its
    // target is no longer the newest user message (another device sent
    // a turn) or the viewed session changes; the draft is left alone.
    Effect::new(move |_| {
        let last = transcript.with(|t| t.last_user_id());
        let state = last_edit.get_untracked();
        let next = edit_resend::reconcile(state.clone(), last);
        if next != state {
            last_edit.set(next);
        }
    });
    Effect::new(move |prev: Option<String>| {
        let root = active_root.get();
        if prev.is_some_and(|p| p != root) && last_edit.get_untracked().editing() {
            last_edit.set(edit_resend::State::Idle);
            edit_note.set(String::new());
        }
        root
    });
    // The engine confirmed the rewind: send the edited text as a normal
    // user.input. A held send (context full) keeps the draft.
    Effect::new(move |_| {
        let Some(text) = resend_now.get() else {
            return;
        };
        resend_now.set(None);
        if send_text(text.clone()) {
            prompt_history.update(|h| h.push(&text));
            if draft.get_untracked().trim() == text {
                draft.set(String::new());
            }
            edit_note.set(String::new());
        } else {
            edit_note.set(
                "The last turn was discarded, but the edited text was not sent. It is still in the composer."
                    .into(),
            );
        }
    });
    let start_edit = move |id: u32| {
        let Some(text) = transcript.with_untracked(|t| t.sent_text(id)) else {
            edit_note.set(edit_resend::NOTHING.into());
            return;
        };
        last_edit.set(edit_resend::State::Editing { target: id });
        edit_note.set(String::new());
        draft.set(text);
        focus_composer_end_soon();
    };
    let request_edit = move |r: edit_resend::Recall| match r {
        edit_resend::Recall::Edit(id) => start_edit(id),
        edit_resend::Recall::Refused(msg) => edit_note.set(msg.into()),
        edit_resend::Recall::Inert => {}
    };
    // Submit while editing: confirm what will be discarded, post the
    // engine's `rewind` op (conversation + that turn's file changes),
    // and keep the draft until `session.rewound` releases it.
    let resend_edit = move || {
        let root = active_root.get_untracked();
        if root.is_empty() {
            status.set("no live session yet".into());
            return;
        }
        if matches!(last_edit.get_untracked(), edit_resend::State::Reverting { .. }) {
            return;
        }
        if !pending_files.get_untracked().is_empty() {
            edit_note.set("Edit & resend sends text only. Remove the attachments first.".into());
            return;
        }
        let check = move || -> Result<(edit_resend::State, edit_resend::Discard), &'static str> {
            let root = active_root.get_untracked();
            let (rows, last) = transcript.with_untracked(|t| (t.edit_rows(), t.last_user_id()));
            let next = edit_resend::begin(
                &last_edit.get_untracked(),
                &draft.get_untracked(),
                session_busy(&root),
                !queue_items.get_untracked().is_empty(),
                last,
            )?;
            let edit_resend::State::Reverting { target, .. } = &next else {
                return Err(edit_resend::NOTHING);
            };
            let discard = edit_resend::discard_after(&rows, *target).ok_or(edit_resend::NOTHING)?;
            Ok((next, discard))
        };
        let discard = match check() {
            Ok((_, d)) => d,
            Err(msg) => {
                edit_note.set(msg.into());
                return;
            }
        };
        let on_yes: Rc<dyn Fn()> = Rc::new(move || {
            // Re-checked at confirm time: the session may have moved on
            // while the dialog was open.
            let next = match check() {
                Ok((next, _)) => next,
                Err(msg) => {
                    edit_note.set(msg.into());
                    return;
                }
            };
            last_edit.set(next);
            edit_note.set(String::new());
            let root = active_root.get_untracked();
            let (b, t) = (base.get_untracked(), token.get_untracked());
            spawn_local(async move {
                if let Err(e) =
                    post_op(&b, &t, &root, "rewind", serde_json::json!({"restoreFiles": true})).await
                {
                    let (n, note) = edit_resend::on_rewind_failed(last_edit.get_untracked(), &e);
                    last_edit.set(n);
                    if let Some(note) = note {
                        edit_note.set(note);
                    }
                }
            });
        });
        if !crate::confirm::ask(
            crate::confirm::ConfirmRequest {
                title: "Edit and resend?".into(),
                body: edit_resend::confirm_body(&discard),
                confirm_label: "Discard and resend".into(),
            },
            on_yes,
        ) {
            edit_note.set(crate::confirm::BUSY.into());
        }
    };

    let send = move |which: steer_queue::SendWhich| {
        // Session /loop (#1588): handled against the loops routes, never
        // sent as a prompt, and ahead of the context hold so a full window
        // never blocks /loop stop. Without the capability the draft is kept.
        let loop_text = draft.get_untracked().trim().to_string();
        if let Some(cmd) = session_loops::parse(&loop_text) {
            ac_open.set(false);
            let root = active_root.get_untracked();
            if root.is_empty() {
                status.set("no live session yet".into());
                return;
            }
            if !loops_capable.get_untracked() {
                loop_note.set("loop: this gateway cannot run loops for this session (attach-only or read-only).".into());
                return;
            }
            if pending_files.get_untracked().iter().any(|f| f.error.is_empty()) {
                loop_note.set("loop: a loop job is text only. Remove the attachments first.".into());
                return;
            }
            prompt_history.update(|h| h.push(&loop_text));
            draft.set(String::new());
            let (b, t) = (base.get_untracked(), token.get_untracked());
            spawn_local(session_loops::run(b, t, root, cmd, active_root, loop_rows, loop_note));
            return;
        }
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
        if held.held && !hold_bypass.get_untracked() {
            model_note.set(held.hold_message);
            return;
        }
        hold_bypass.set(false);
        ac_open.set(false);
        plus_open.set(false);
        ac_force.set(String::new());
        if sending.get_untracked() {
            return;
        }
        if last_edit.get_untracked().editing() {
            resend_edit();
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
        // #1362: a quote rides on the user's own words; it never sends alone.
        let quoted = quote_draft.get_untracked();
        if quoted.is_some() && text.is_empty() {
            queue_note.set("Add a message to send with the quote.".into());
            return;
        }
        let root = active_root.get_untracked();
        if root.is_empty() {
            status.set("no live session yet".into());
            return;
        }
        // Draft-size gate (#1542): when the draft itself projects the
        // session to critical, the first press arms the warning —
        // nothing is dispatched and the draft is kept. A second press
        // on the same draft sends anyway. Below critical this never
        // blocks, and an active session hold defers (no double gate).
        {
            let ctx = active_context(
                &catalog.get_untracked(),
                &provider_name.get_untracked(),
                &model_name.get_untracked(),
            );
            let base_view = context_view(
                context_used.get_untracked(),
                context_limit.get_untracked(),
                ctx,
                &context_fit.get_untracked(),
            );
            let dview = draft_size::draft_size_view(
                base_view.used,
                base_view.limit,
                base_view.held,
                &text,
            );
            if draft_size::send_held(&dview, &draft_armed.get_untracked(), &text) {
                draft_armed.set(text.clone());
                return;
            }
            draft_armed.set(String::new());
        }
        let op = steer_queue::send_op(session_busy(&root), &enter_mode.get_untracked(), which);
        // One id per send. The daemon dedupes on it, so the outbox may
        // retry a send whose answer was lost without posting it twice.
        let client_id = outbox::new_client_id();
        // Offline outbox (#1325): with the gateway down, or earlier queued
        // items for this session still waiting, a text send is queued in
        // order instead of posted. Attachments need an upload, so they keep
        // the normal path (and its failure restore).
        if !ready
            && outbox::should_queue(&gateway.get_untracked(), &outbox.items.get_untracked(), &root)
        {
            if !outbox.enqueue(client_id, root.clone(), op, text.clone()) {
                status.set("Outbox is full. Delete a queued message first.".into());
                return;
            }
            queue_note.set(String::new());
            prompt_history.update(|h| h.push(&text));
            draft.set(String::new());
            outbox.kick();
            return;
        }
        queue_note.set(String::new());
        if op == "steer" {
            steer_note.set(steer_queue::STEER_WAITING.into());
            steer_mode.set(String::new());
        }
        // Prompt history (#1402): record the trimmed prompt before the
        // draft is cleared, deduping consecutive repeats. Attachments-only
        // sends have no text and record nothing.
        prompt_history.update(|h| h.push(&text));
        // #1545: snapshot marks for the files being sent. The composite
        // replaces the original image in the turn; the text block rides
        // in the same message. Marks are cleared only after success.
        let marks_now = annotate_marks.get_untracked();
        let annotated: HashMap<u32, Vec<annotate::Mark>> = files
            .iter()
            .filter(|f| f.error.is_empty())
            .filter_map(|f| marks_now.get(&f.id).filter(|m| !m.is_empty()).map(|m| (f.id, m.clone())))
            .collect();
        let blocks: Vec<String> = files
            .iter()
            .filter_map(|f| annotated.get(&f.id).map(|m| annotate::text_block(&f.name, m)))
            .collect();
        let text_out = annotate::compose_message(&text, &blocks);
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
                let (merged, dropped) = chat_mode::merge_failed_files(failed.clone(), rows.clone());
                *rows = merged;
                if let Some(msg) = chat_mode::restore_cap_status(dropped) {
                    status.set(msg);
                }
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
                if let Some(marks) = annotated.get(&file.id) {
                    // Composite from the real attached image; the original
                    // pending file is left untouched for a failed-send restore.
                    let composed = match annotate::composite_png(&file.preview, marks).await {
                        Ok(b64) => b64,
                        Err(err) => {
                            status.set(format!("annotate failed: {err}"));
                            restore(updated, text);
                            return;
                        }
                    };
                    let name = annotate::annotated_name(&file.name);
                    match chat_mode::upload_attachment(&b, &t, &root, &name, "image/png", &composed).await {
                        Ok(up) => {
                            let mime = if up.mime.is_empty() { "image/png".to_string() } else { up.mime };
                            let shown = if up.name.is_empty() { name } else { up.name };
                            images.push(serde_json::json!({
                                "ref": up.reference,
                                "mime": mime,
                                "kind": up.kind,
                                "name": shown,
                                "bytes": up.bytes,
                                "sha256": up.sha256,
                            }));
                        }
                        Err(err) => {
                            status.set(format!("attachment failed: {err}"));
                            restore(updated, text);
                            return;
                        }
                    }
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
            // #1362: the quote travels as a delimited block ahead of the
            // user's text, so the agent gets the exact quoted text and the
            // stored log keeps the reference. It wraps the annotated text
            // (#1545); `text` stays the user's own words for the restore.
            let wire = match &quoted {
                Some(q) => quote_message::compose(&q.role, q.anchor, &q.text, &text_out),
                None => text_out.clone(),
            };
            let body = if images.is_empty() {
                serde_json::json!({"text": wire})
            } else {
                serde_json::json!({"text": wire, "images": images})
            };
            match outbox::post_op_with_id(&b, &t, &root, op, body, &client_id).await {
                Ok(()) => {
                    if quoted.is_some() && quote_draft.get_untracked() == quoted {
                        quote_draft.set(None);
                    }
                    for file in &updated {
                        chat_mode::revoke_preview(&file.preview);
                    }
                    annotate_marks.update(|m| {
                        for id in annotated.keys() {
                            m.remove(id);
                        }
                    });
                    sending.set(false);
                }
                Err(err) => {
                    if op == "steer" && steer_note.get_untracked() == steer_queue::STEER_WAITING {
                        steer_note.set(String::new());
                    }
                    // Lost connection on a text send: queue it under the
                    // same id. If the daemon did get it, the retry is a
                    // no-op there instead of a second prompt (#1325).
                    if images.is_empty()
                        && outbox::classify_failure(&err) == outbox::SendFailure::Transient
                        && outbox.enqueue(client_id.clone(), root.clone(), op, text.clone())
                    {
                        sending.set(false);
                        return;
                    }
                    queue_note.set(format!("Send failed: {err}"));
                    status.set(format!("send failed: {err}"));
                    restore(updated, text);
                }
            }
        });
    };

    let stop_turn = move || {
        let root = active_root.get_untracked();
        if root.is_empty() || !session_busy(&root) {
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Err(e) = post_op(&b, &t, &root, "interrupt", serde_json::json!({})).await {
                queue_note.set(format!("Stop failed: {e}"));
            }
        });
    };

    let post_queue = move |typ: &'static str, data: serde_json::Value| {
        let root = active_root.get_untracked();
        if root.is_empty() {
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Err(e) = post_op(&b, &t, &root, typ, data).await {
                queue_note.set(format!("Queue update failed: {e}"));
            }
        });
    };

    let stop_for_palette = stop_turn.clone();
    shell::bind(
        "stop-turn",
        Rc::new(move |_args: &str| {
            stop_for_palette();
            Ok(())
        }),
    );
    shell::bind(
        "queue.clear",
        Rc::new(move |_args: &str| {
            let ids: Vec<String> = queue_items
                .get_untracked()
                .into_iter()
                .map(|item| item.id)
                .collect();
            if ids.is_empty() {
                return Err("The queue is empty.".into());
            }
            for id in ids {
                post_queue("input.queue_cancel", serde_json::json!({"id": id}));
            }
            Ok(())
        }),
    );

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
                        let activated = post_json(
                            &b,
                            &t,
                            &format!("/v1/roots/{id}/activate"),
                            serde_json::json!({}),
                        )
                        .await;
                        if let Some(msg) = outcome::new_session_activation(activated) {
                            activation_hold.set(Some(msg));
                        }
                        active_root.set(id.clone());
                        session_restore::persist(&id);
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
        session_restore::persist(&id);
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
                Ok(()) => {
                    session_restore::persist(&id);
                    active_root.set(id);
                }
                Err(e) => status.set(format!("resume failed: {e}")),
            }
        });
    };

    // #1550: a session chip opens the session it references.
    let on_open_session_ref = Callback::new(move |id: String| resume_session(id));

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
        // Stale/large resume warning (#1502): decided from the list row
        // before switching; any older card is cleared by the new open.
        let viewing = active_root.get_untracked();
        let limit = context_limit.get_untracked();
        let warn = sessions.with_untracked(|list| {
            list.iter().find(|s| s.id == id).and_then(|row| {
                stale_resume::check(
                    row,
                    now_ms() as i64,
                    limit,
                    &viewing,
                    stale_resume::dismissed_tokens(&row.id),
                )
            })
        });
        stale_warning.set(warn);
        remember(&id);
        if live {
            activate_root(id);
        } else {
            resume_session(id);
        }
    };

    // Page-level session links (Audit #1530, Permissions #1540): switch
    // to chat and open through the shared live/resume path.
    let open_session_page = move |id: String| {
        mode.set("chat".into());
        let live = roots.get_untracked().iter().any(|r| r.id == id);
        open_session(id, live);
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
        let theme = theme.trim().to_string();
        theme_id.set(if theme.is_empty() {
            theme_catalog::DEFAULT_ID.to_string()
        } else {
            theme
        });
        repaint_theme();
    };
    let set_scheme = move |scheme: String| {
        let scheme: String = if scheme == "light" {
            "light".to_string()
        } else {
            "dark".to_string()
        };
        scheme_id.set(scheme);
        repaint_theme();
    };
    let set_font = move |font: String| {
        font_id.set(appearance::normalize_font(&font));
        appearance::apply_type(&font_id.get_untracked(), &density_id.get_untracked());
    };
    let set_density = move |density: String| {
        density_id.set(appearance::normalize_density(&density));
        appearance::apply_type(&font_id.get_untracked(), &density_id.get_untracked());
    };
    let finish_onboarding = move || {
        save("strike.onboarded", "1");
        onboarded.set(true);
        // Record the acknowledgement on the daemon too (#1552), for both
        // Continue and "Not now" (both reach this via on_done), so other
        // browsers and devices against this daemon skip the card. The
        // local ack above stands either way; a failed daemon write is
        // surfaced, not swallowed, so the repeat is not a surprise.
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Err(e) =
                post_json(&b, &t, "/v1/onboarding/acknowledge", serde_json::json!({})).await
            {
                status.set(format!(
                    "Onboarding is saved in this browser only. The daemon record was not written: {e}"
                ));
            }
        });
    };
    let on_cleared = move || {
        set_theme("strike".into());
        set_scheme("dark".into());
        set_font("md".into());
        set_density("comfortable".into());
        onboarded.set(false);
        onboarding_daemon_checked.set(false);
        set_layout("cockpit".into());
        base.set(String::new());
        token.set(String::new());
        gateway::clear_session_tokens();
        gateway_profiles.set(Vec::new());
        gateway_selected.set("this".into());
        pins.set(Vec::new());
        seen.set(HashMap::new());
        enter_mode.set("queue".into());
        save("strike.enterMode", "queue");
    };
    shell::bind(
        "settings.font",
        Rc::new(move |value| {
            set_font(value.to_string());
            Ok(())
        }),
    );
    shell::bind(
        "settings.density",
        Rc::new(move |value| {
            set_density(value.to_string());
            Ok(())
        }),
    );
    shell::bind(
        "settings.export-browser",
        Rc::new(move |_| {
            appearance::download("strike-browser.json", &appearance::export_browser_json());
            status.set("Downloaded browser settings. The token was left out.".into());
            Ok(())
        }),
    );
    shell::bind(
        "settings.export-daemon",
        Rc::new(move |_| {
            let (b, t) = (base.get_untracked(), token.get_untracked());
            spawn_local(async move {
                match get_json::<serde_json::Value>(&b, &t, "/v1/settings").await {
                    Ok(v) => {
                        let body = serde_json::to_string_pretty(&v).unwrap_or_else(|_| "{}".into());
                        appearance::download("strike-daemon-settings.json", &body);
                        status.set("Downloaded the daemon settings dials.".into());
                    }
                    Err(e) => status.set(format!("Could not export daemon settings: {e}")),
                }
            });
            Ok(())
        }),
    );
    shell::bind(
        "settings.clear",
        Rc::new(move |_| {
            appearance::clear_strike_keys();
            on_cleared();
            status.set("Cleared Strike keys in this browser. Daemon files were left in place.".into());
            Ok(())
        }),
    );

    let set_memory = move |mode: String| {
        memory_menu.set(false);
        // Snapshot the confirmed mode before any optimistic update so a
        // rejected change can revert the chip (#1585). The optimistic set
        // happens only after the live-session check passes.
        let previous = if memory_mode_known.get_untracked() {
            Some(memory_mode.get_untracked())
        } else {
            None
        };
        let root = active_root.get_untracked();
        if root.is_empty() {
            status.set("no live session yet".into());
            return;
        }
        memory_mode.set(mode.clone());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/memory/mode?root={}", api::js_encode(&root));
            match put_json(&b, &t, &path, serde_json::json!({"mode": mode})).await {
                Ok(text) => {
                    let saved = serde_json::from_str::<serde_json::Value>(&text)
                        .ok()
                        .and_then(|v| {
                            v.get("mode")
                                .and_then(|m| m.as_str())
                                .map(str::to_string)
                        })
                        .filter(|saved| !saved.is_empty());
                    match saved {
                        Some(saved) => {
                            memory_mode.set(saved);
                            memory_mode_known.set(true);
                        }
                        None => {
                            let (known, mode) = revert_memory_mode(previous);
                            memory_mode_known.set(known);
                            if known {
                                memory_mode.set(mode);
                            }
                            status.set("memory mode was not confirmed by the daemon".into());
                        }
                    }
                }
                Err(e) => {
                    let (known, mode) = revert_memory_mode(previous);
                    memory_mode_known.set(known);
                    if known {
                        memory_mode.set(mode);
                    }
                    status.set(format!("memory mode failed: {e}"));
                }
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

    // Autonomy (#1572), cloned from set_perm with two deliberate
    // differences: a running turn refuses up front with the reason on
    // the status line (the daemon rejects the op mid-turn), and applying
    // never touches the autonomy signal — only the daemon's
    // `autonomy.selected` confirmation does.
    let set_autonomy = move |mode: String| {
        autonomy_menu.set(false);
        let root = active_root.get_untracked();
        if !root.is_empty() && session_busy(&root) {
            status.set(
                "This turn is still running. Autonomy changes wait until it finishes.".into(),
            );
            return;
        }
        match begin_autonomy_change(&autonomy.get_untracked(), &mode) {
            ModeChange::Same => {}
            ModeChange::Apply(mode) => {
                commit_autonomy(base, token, active_root, status, mode);
            }
            ModeChange::Confirm(pending) => confirm.set(Some(pending)),
        }
    };


    // Session delete (#1358): arm the confirm dialog for one session.
    // Shared by the row button and the palette command — one code path,
    // and arming the dialog is all either surface does.
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
    // Stale/large resume card actions (#1502). Start fresh opens a new
    // session and only pre-fills the composer with a reference to the old
    // one; nothing is sent or copied. Cancel returns to the session open
    // before (the resumed one sends nothing until a turn is started).
    let on_stale_fresh = Callback::new(move |w: stale_resume::StaleWarning| {
        new_session();
        draft.set(stale_resume::fresh_prompt(&w.id, &w.title));
        sync_composer_height_soon();
    });
    let on_stale_cancel = Callback::new(move |w: stale_resume::StaleWarning| {
        if w.previous.is_empty() || w.previous == w.id {
            return;
        }
        let live = roots.get_untracked().iter().any(|r| r.id == w.previous);
        open_session(w.previous, live);
    });
    // Rewind applied (#1336): after a conversation restore the picked
    // turn's prompt returns to the composer for edit-and-resend; it is
    // never sent automatically.
    let rewind_busy = Signal::derive(move || {
        let active = active_root.get();
        roots.with(|list| list.iter().any(|r| r.id == active && r.busy))
    });
    let on_rewound = Callback::new(move |(prompt, mode, turns): (String, rewind::Mode, u32)| {
        let what = if mode.restores_files() { "conversation and code" } else { "conversation" };
        // The ops were accepted; the engine reports each undone turn (or a
        // refusal) in the transcript, so this note does not claim more.
        ux_note.set(format!(
            "Requested a rewind of {turns} turn(s) ({what}); the transcript shows each undone turn. The prompt is back in the composer."
        ));
        if !prompt.trim().is_empty() {
            draft.set(prompt);
            sync_composer_height_soon();
        }
    });
    // Global search pick (#1355): open the session through the shared
    // live/resume path and queue the find-in-transcript handoff.
    let on_gs_pick = Callback::new(move |(id, q): (String, String)| {
        gs_jump.set(Some((id.clone(), q)));
        open_session_page(id);
    });
    let on_deleted = Callback::new(move |id: String| {
        let was_live = roots.get_untracked().iter().any(|row| row.id == id);
        // Drop the deleted session's draft (#1367) before the fallback
        // switch below, so it is not saved back as the outgoing draft.
        draft_book.update_value(|book| book.forget(&id));
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
            // The viewed session is gone (#1391): forget it now so a
            // reload before the fallback lands cannot restore a ghost;
            // the next refresh persists the fallback it picks.
            session_restore::clear();
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
        let gen = begin(files_epoch);
        let (b, t, path) = (
            base.get_untracked(),
            token.get_untracked(),
            files_path.get_untracked(),
        );
        files.set(Vec::new());
        files_error.set(String::new());
        files_loading.set(true);
        let files_epoch = files_epoch;
        spawn_local(async move {
            let q = format!("/v1/files?path={}", api::js_encode(&path));
            let fetched = get_json::<FilesResponse>(&b, &t, &q).await;
            if !key_matches(&path, &files_path.get_untracked()) {
                return;
            }
            let mapped = match fetched {
                Ok(r) => {
                    let mut entries = r.entries;
                    entries.sort_by(|a, b| b.is_dir.cmp(&a.is_dir).then(a.name.cmp(&b.name)));
                    Ok(entries)
                }
                Err(e) => Err(e),
            };
            match files_epoch.with_untracked(|epoch| {
                commit_load(epoch, gen, mapped, Vec::new(), ErrPolicy::Clear)
            }) {
                Commit::Drop => return,
                Commit::Apply { value, error } => {
                    files.set(value);
                    files_error.set(error);
                    files_for.set(path);
                    files_loading.set(false);
                }
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

    // Diagnostics (#1404) load while the pane is shown or a document is
    // open (for the editor badge), and again on every Refresh / palette
    // open (diag_rev) and every debounced files.invalidated burst
    // (files_rev, #1894). The previous result stays visible while reloading.
    Effect::new(move |_| {
        let _ = diag_rev.get();
        let _ = files_rev.get();
        let shown = side_tab.get() == "diagnostics";
        let doc_open = !doc_path.get().is_empty();
        if !shown && !doc_open {
            return;
        }
        let gen = diag_gen.get_untracked().wrapping_add(1);
        diag_gen.set(gen);
        if !matches!(diag_state.get_untracked(), diagnostics::DiagState::Ready(_)) {
            diag_state.set(diagnostics::DiagState::Loading);
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let res = api::get_text(&b, &t, diagnostics::PATH).await;
            if diag_gen.get_untracked() != gen {
                return;
            }
            diag_state.set(diagnostics::state_from(res));
        });
    });

    Effect::new(move |_| {
        let tab = side_tab.get();
        let _ = files_path.get();
        let _ = files_rev.get();
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
            // Ctrl/Cmd+, opens Settings. The shell owns Ctrl+K, Alt arrows,
            // Escape, and ?. ? stays the shortcut overlay.
            if (ev.meta_key() || ev.ctrl_key())
                && !ev.alt_key()
                && ev.key() == ","
                && !event_in_field(&ev)
            {
                ev.prevent_default();
                mode.set("settings".into());
            }
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
    // #1337: approvals and turn endings reach screen readers.
    let announcer = announce::Announcer::new();
    announce::watch_asks(announcer, asks);
    announce::watch_turns(announcer, active_root, session_busy_now, transcript);
    // Prompt sharpen (#1385): App-owned so the binding outlives the
    // chat view's remounts.
    let sharpen = prompt_sharpen::Sharpen::new(
        draft,
        active_root,
        Signal::derive(move || session_busy_now.get()),
        base,
        token,
    );

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
            // A failed or empty load must not leave the chip asserting the
            // invented default (#1585): mark the mode unknown and say so.
            let loaded = match get_json::<serde_json::Value>(&b, &t, &path).await {
                Ok(v) => v
                    .get("mode")
                    .and_then(|m| m.as_str())
                    .filter(|mode| !mode.is_empty())
                    .map(str::to_string),
                Err(_) => None,
            };
            match loaded {
                Some(mode) => {
                    memory_mode.set(mode);
                    memory_mode_known.set(true);
                }
                None => {
                    memory_mode_known.set(false);
                    if !root.is_empty() {
                        status.set("memory mode could not be loaded".into());
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
    let on_select_agent = Callback::new(move |name: String| {
        agent_note_after.set(now_ms() as i64);
        agent_note.set(String::new());
        let (b, t, root) = (base.get_untracked(), token.get_untracked(), active_root.get_untracked());
        spawn_local(async move {
            if let Err(e) = post_op(&b, &t, &root, "select.agent", serde_json::json!({"name": name})).await {
                agent_note.set(format!("agent change failed: {e}"));
                agent_note_after.set(0);
            }
        });
    });
    crate::agent_picker::set_picker(Rc::new(move |name: &str| {
        on_select_agent.run(name.to_string());
    }));
    Effect::new(move |_| crate::agent_picker::set_agents(agent_list.get()));
    Effect::new(move |_| crate::agent_picker::set_current(&agent_name.get()));
    {
        let pick = on_select_model.clone();
        shell::set_model_picker(Rc::new(move |provider, id| {
            pick.run((provider.to_string(), id.to_string()));
        }));
    }
    Effect::new(move |_| {
        let rows = catalog
            .get()
            .into_iter()
            .map(|model| shell::Choice {
                id: format!("{}\t{}", model.provider, model.id),
                label: {
                    let provider = if model.provider.is_empty() {
                        "model".to_string()
                    } else {
                        model.provider.clone()
                    };
                    let id = if model.name.is_empty() {
                        model.id.clone()
                    } else {
                        model.name.clone()
                    };
                    format!("Switch model: {provider}/{id}")
                },
                detail: model.provider.clone(),
            })
            .collect();
        shell::set_model_choices(rows);
    });
    Effect::new(move |_| {
        let rows = skills
            .get()
            .into_iter()
            .map(|skill| (skill.name, skill.description))
            .collect();
        composer_triggers::set_palette_skills(rows);
    });
    // #1550: recent sessions for the @ menu's Sessions section.
    Effect::new(move |_| {
        let mut rows = sessions.get();
        rows.sort_by_key(|s| std::cmp::Reverse(s.last_activity_at.max(s.mtime)));
        let mut rows: Vec<(String, String)> = rows.into_iter().map(|s| (s.id, s.title)).collect();
        let own = active_root.get();
        // A fresh session may not be in the list yet; still show it so
        // the self row is honest instead of missing.
        if !own.is_empty() && !rows.iter().any(|(id, _)| *id == own) {
            rows.insert(0, (own.clone(), String::new()));
        }
        composer_triggers::set_session_catalog(rows, own);
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
                    defaults_known.set(true);
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
    // Fast (#1569), cloned from on_effort: post the real `set.fast` op
    // and nothing else. `fast_on` is never set here — it flips only when
    // the daemon's `fast.selected` confirmation reduces in the SSE path
    // above. A running turn refuses up front with the reason surfaced,
    // matching the model menu's disabled state.
    let on_fast = Callback::new(move |enabled: bool| {
        let root = active_root.get_untracked();
        if !root.is_empty() && session_busy(&root) {
            model_note.set(
                "This turn is still running. Fast changes wait until it finishes.".into(),
            );
            return;
        }
        model_note_after.set(now_ms() as i64);
        model_note.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Err(e) = post_op(
                &b,
                &t,
                &root,
                "set.fast",
                serde_json::json!({"enabled": enabled}),
            )
            .await
            {
                model_note.set(format!("fast change failed: {e}"));
                model_note_after.set(0);
            }
        });
    });
    // Compact now (#1567): send the daemon-certified `compact` op on
    // the same path as effort. No optimistic meter change — the UI
    // waits for `compaction.completed`; a refusal surfaces as-is.
    let on_compact = Callback::new(move |_: ()| {
        let root = active_root.get_untracked();
        if root.is_empty() {
            return;
        }
        let busy = roots
            .get_untracked()
            .iter()
            .any(|row| row.id == root && row.busy)
            || (live_busy.get_untracked() && live_busy_root.get_untracked() == root);
        if let Some(reason) =
            crate::model_menu::compact_disabled(busy, compacting.get_untracked())
        {
            model_note.set(reason.into());
            return;
        }
        compacting.set(true);
        model_note.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Err(e) = post_op(&b, &t, &root, "compact", serde_json::json!({})).await {
                compacting.set(false);
                model_note.set(format!("compact failed: {e}"));
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
                    defaults_known.set(true);
                    model_note.set("Saved the effort as the global default.".into());
                }
                Err(e) => model_note.set(format!("saving the effort default failed: {e}")),
            }
        });
    });
    {
        let pick = on_default_model.clone();
        shell::set_default_model_picker(Rc::new(move |provider, id| {
            pick.run((provider.to_string(), id.to_string()));
        }));
        let effort = on_effort.clone();
        shell::set_effort_setter(Rc::new(move |level| {
            effort.run(level.to_string());
        }));
        shell::set_effort_choices(
            crate::model_menu::effort_levels()
                .iter()
                .map(|(id, label)| shell::Choice {
                    id: (*id).to_string(),
                    label: format!("Set reasoning effort: {label}"),
                    detail: String::new(),
                })
                .collect(),
        );
        let save = on_default_effort.clone();
        shell::set_effort_default_setter(Rc::new(move || {
            let level = effort_level.get_untracked();
            if !level.trim().is_empty() {
                save.run(level);
            }
        }));
        let fast = on_fast.clone();
        shell::set_fast_setter(Rc::new(move |enabled| {
            fast.run(enabled);
        }));
    }

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
    notify::set_unread_host(notify_unread);
    // #1926: the Ideas badge is a header slot; give it the daemon address.
    ideas::set_host(base, token);

    shell::boot();
    shell::set_host(shell::HostBridge {
        new_session: Rc::new(move || new_session()),
        stop_all: Rc::new(move || stop_all()),
        set_view: Rc::new(move |next: &str| mode.set(next.to_string())),
        view: Rc::new(move || mode.get()),
        set_enter: Rc::new(move |next: &str| set_enter(next.to_string())),
        set_perm: Rc::new(move |next: &str| set_perm(next.to_string())),
        set_autonomy: Rc::new(move |next: &str| set_autonomy(next.to_string())),
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
        // Waiting chip (#1543): jump to the pending ask card, or say
        // plainly in the status line when it already resolved — the
        // flags and the ask list are re-read at click time, not from
        // the render that painted the chip.
        go_to_ask: Rc::new(move || {
            mode.set("chat".into());
            let root = active_root.get_untracked();
            let flagged = roots.get_untracked().iter().any(|row| {
                row.id == root && (row.permission_pending || row.question_pending)
            });
            if asks.get_untracked().is_empty() && !flagged {
                status.set("That ask is already resolved — nothing is waiting.".into());
                return;
            }
            crate::status::scroll_to_pending_ask_soon();
        }),
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
    // Quick-open (#1396): the palette's Open file… opens through the
    // same path as the Files pane's Open (`open_doc`), so the unsaved
    // guard sees it like any other path change.
    crate::quick_open::set_host(crate::quick_open::QuickOpenHost {
        base,
        token,
        open: Rc::new(move |p: &str| {
            mode.set("chat".into());
            open_doc(p);
        }),
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
    shell::bind(
        "panel-decisions",
        Rc::new(move |_args: &str| {
            if !ledger_capable.get_untracked() {
                return Err("This daemon does not serve the decision ledger.".into());
            }
            side_tab.set("decisions".into());
            Ok(())
        }),
    );
    // Context doctor (#1537): open the breakdown and ask the live root.
    shell::bind(
        "context-doctor",
        Rc::new(move |_args: &str| {
            doctor.open.set(true);
            doctor.request(
                base.get_untracked(),
                token.get_untracked(),
                active_root.get_untracked(),
            );
            Ok(())
        }),
    );
    // A different session's breakdown never lingers in the dialog.
    Effect::new(move |prev: Option<String>| {
        let root = active_root.get();
        if prev.as_ref().is_some_and(|p| p != &root) {
            doctor.reset();
            if doctor.open.get_untracked() {
                doctor.request(base.get_untracked(), token.get_untracked(), root.clone());
            }
        }
        root
    });
    shell::bind(
        "lesson-remember",
        Rc::new(move |text: &str| {
            if !lesson_gate.get_untracked() {
                return Err("This session's memory mode does not store lessons.".into());
            }
            lesson_draft.set(Some(lessons::draft_from_message(text)));
            Ok(())
        }),
    );
    shell::bind(
        "panel-issues",
        Rc::new(move |_args: &str| {
            if !issues_capable.get_untracked() {
                return Err("Local issues are not available on this gateway.".into());
            }
            side_tab.set("issues".into());
            Ok(())
        }),
    );
    shell::bind(
        "panel-terminal",
        Rc::new(move |_args: &str| {
            side_tab.set("terminal".into());
            Ok(())
        }),
    );
    shell::bind(
        "permissions.rules",
        Rc::new(move |_args: &str| {
            mode.set("chat".into());
            panel_open.set(true);
            Ok(())
        }),
    );
    shell::bind(
        "skill.use",
        Rc::new(move |name| {
            let name = name.trim().trim_start_matches('$');
            if name.is_empty() {
                return Err("Pick a skill.".into());
            }
            let mut text = draft.get_untracked();
            if !text.is_empty() && !text.ends_with(' ') {
                text.push(' ');
            }
            text.push('$');
            text.push_str(name);
            text.push(' ');
            draft.set(text);
            mode.set("chat".into());
            Ok(())
        }),
    );
    if let Some(name) = park::take("skill.use") {
        let _ = shell::dispatch("skill.use", &name);
    }
    if let Some(_) = park::take("permissions.rules") {
        mode.set("chat".into());
        panel_open.set(true);
    }
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

    // Workflow run performers (#1574). Both post the browser-certified
    // ops and never touch `wf_run`: the chip moves only on the
    // daemon's `phase.changed`. A refusal is an `engine.error` that the
    // model-note reducer shows verbatim (it arms on `model_note_after`).
    let post_workflow_op = move |typ: &'static str, data: serde_json::Value| {
        let root = active_root.get_untracked();
        if root.is_empty() {
            model_note.set("No live session yet — open or start one first.".into());
            return;
        }
        model_note_after.set(now_ms() as i64);
        model_note.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Err(e) = post_op(&b, &t, &root, typ, data).await {
                model_note.set(format!("{typ} failed: {e}"));
                model_note_after.set(0);
            }
        });
    };
    let request_workflow_stop = move || -> Result<(), String> {
        let active = wf_run.get_untracked();
        if !active.is_active() {
            return Err("No workflow is active on this session.".into());
        }
        if !crate::confirm::ask(
            workflow_run::stop_confirm(&active),
            Rc::new(move || post_workflow_op("workflow.stop", serde_json::json!({}))),
        ) {
            return Err(crate::confirm::BUSY.into());
        }
        Ok(())
    };
    shell::bind(
        "workflow-start",
        Rc::new(move |args: &str| {
            let name = workflow_run::start_target(args)?;
            let active = wf_run.get_untracked();
            let data = serde_json::json!({ "name": name.clone() });
            match workflow_run::replace_confirm(&active, &name) {
                None => post_workflow_op("workflow.start", data),
                Some(request) => {
                    if !crate::confirm::ask(
                        request,
                        Rc::new(move || post_workflow_op("workflow.start", data.clone())),
                    ) {
                        return Err(crate::confirm::BUSY.into());
                    }
                }
            }
            Ok(())
        }),
    );
    shell::bind(
        "workflow-stop",
        Rc::new(move |_args: &str| request_workflow_stop()),
    );

    // Session delete palette performer (#1358). The contrib command has
    // already resolved the argument to exactly one session id via the
    // shell's unique match; this only arms the same confirm dialog the
    // row button opens. No request is issued here.
    shell::bind(
        "session-delete",
        Rc::new(move |args: &str| {
            let cursor = session_panel::cursor_session();
            let active = active_root.get_untracked();
            let selected = session_panel::selected_ids();
            let marked = web_sys::window()
                .and_then(|w| w.document())
                .and_then(|doc| doc.query_selector("[data-delete-target='1']").ok().flatten())
                .and_then(|el| el.get_attribute("data-session-id"))
                .unwrap_or_default();
            let chosen = if !marked.is_empty() {
                marked
            } else if selected.len() == 1 {
                selected[0].clone()
            } else {
                session_delete::resolve_session_target(args, &cursor.id, &active)?.to_string()
            };
            let id = chosen;
            let active_running = activity
                .get_untracked()
                .iter()
                .any(|row| is_running(&row.state));
            let label = if cursor.id == id && !cursor.label.is_empty() {
                cursor.label
            } else {
                merge_sessions(
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
                .unwrap_or_else(|| short_id(&id))
            };
            mode.set("chat".into());
            let on_deleted = on_deleted;
            let status_note = status;
            if !session_delete::prompt_delete(
                session_delete::DeleteTarget {
                    id,
                    label,
                },
                base.get_untracked(),
                token.get_untracked(),
                Rc::new(move |id| on_deleted.run(id)),
                Rc::new(move |err| status_note.set(err)),
            ) {
                return Err(crate::confirm::BUSY.into());
            }
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
            shell::show_palette();
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
                shell::show_palette();
            }
        });
    if let Some(w) = web_sys::window() {
        let _ = w.add_event_listener_with_callback("keydown", on_model_key.as_ref().unchecked_ref());
    }
    on_model_key.forget();

    // Find in transcript (#1342). Opening saves the transcript scroll so
    // closing can restore it; stepping wraps around the match list.
    let _open_find = move || {
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
        let hits = transcript_matches(
            &list,
            &tf_query.get_untracked(),
            crate::editor::FindOptions::new(
                tf_match_case.get_untracked(),
                tf_whole_word.get_untracked(),
            ),
        );
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
    // Ctrl/Cmd+P opens the palette on Open file… (#1396), cockpit-wide —
    // the quick-open convention every comparator uses. It replaces the
    // browser's print shortcut while the cockpit is focused.
    let on_quick_open_key =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            if (ev.ctrl_key() || ev.meta_key())
                && !ev.alt_key()
                && !ev.shift_key()
                && ev.key().eq_ignore_ascii_case("p")
            {
                ev.prevent_default();
                shell::show_palette_at("open-file");
            }
        });
    if let Some(w) = web_sys::window() {
        let _ = w.add_event_listener_with_callback("keydown", on_quick_open_key.as_ref().unchecked_ref());
    }
    on_quick_open_key.forget();
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
        let hits = transcript_matches(
            &list,
            &q,
            crate::editor::FindOptions::new(tf_match_case.get(), tf_whole_word.get()),
        );
        if let Some(hit) = hits.get(transcript_find::clamped_idx(
            tf_idx.get_untracked(),
            hits.len(),
        )) {
            transcript_find::scroll_to_line_soon(hit.line);
        }
    });

    // Global search handoff (#1355): once the picked session is open and
    // its lines contain the query, open the find bar on it; the scroll
    // effect above then brings the first match into view. A session whose
    // loaded lines do not contain the query (e.g. the match is only in an
    // older page) leaves the jump pending until the next pick.
    Effect::new(move |_| {
        let list = lines.get();
        let active = active_root.get();
        let Some((id, q)) = gs_jump.get() else {
            return;
        };
        if active != id || list.is_empty() {
            return;
        }
        let hits = transcript_matches(&list, &q, crate::editor::FindOptions::new(false, false));
        if hits.is_empty() {
            return;
        }
        gs_jump.set(None);
        if !tf_open.get_untracked() {
            tf_saved_scroll.set(transcript_find::transcript_scroll());
        }
        tf_match_case.set(false);
        tf_whole_word.set(false);
        tf_query.set(q);
        tf_idx.set(0);
        tf_open.set(true);
    });
    shell::bind(
        "search.global",
        Rc::new(move |args: &str| {
            gs_query.set(args.trim().to_string());
            gs_open.set(true);
            Ok(())
        }),
    );

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
            transcript_matches(
                &lines.get(),
                &q,
                crate::editor::FindOptions::new(tf_match_case.get(), tf_whole_word.get()),
            )
        } else {
            Vec::new()
        };
        let current = hits
            .get(transcript_find::clamped_idx(idx, hits.len()))
            .copied();
        find_view.set(crate::transcript_view::FindView { hits, current });
    });

    let apply_title: Callback<(String, String)> = (move |session_id: String, title: String| {
        let id = active_root.get_untracked();
        if id.is_empty() || id != session_id {
            ux_note.set("That rename was for another session.".into());
            return;
        }
        let title = title.trim().to_string();
        if title.is_empty() {
            ux_note.set("A title needs at least one character.".into());
            return;
        }
        let prev_title = transcript.with_untracked(|t| t.snapshot_title());
        let prev_session = sessions.with_untracked(|list| {
            list.iter().find(|s| s.id == id).map(|s| s.title.clone())
        });
        let prev_root = roots.with_untracked(|list| {
            list.iter().find(|r| r.id == id).map(|r| r.title.clone())
        });
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
                Err(e) => {
                    transcript.update(|doc| doc.restore_title(prev_title));
                    if let Some(old) = prev_session {
                        sessions.update(|list| {
                            if let Some(s) = list.iter_mut().find(|s| s.id == id) {
                                s.title = old;
                            }
                        });
                    }
                    if let Some(old) = prev_root {
                        roots.update(|list| {
                            if let Some(r) = list.iter_mut().find(|r| r.id == id) {
                                r.title = old;
                            }
                        });
                    }
                    ux_note.set(format!("Rename failed: {e}"));
                }
            }
        });
    })
    .into();
    let rename_for_palette = apply_title.clone();
    // Message pins (#1528): daemon-backed, reloaded on session switch.
    let pin_store = transcript_pins::PinStore::new(base, token, active_root, ux_note);
    // A quote's anchor is per session: switching sessions drops it.
    Effect::new(move |prev: Option<String>| {
        let root = active_root.get();
        if prev.is_some_and(|p| p != root) {
            quote_draft.set(None);
        }
        root
    });
    let handlers = TranscriptHandlers {
        quote: (move |id: u32| {
            match transcript.with_untracked(|t| t.quote_source(id)) {
                Some((role, anchor, text)) => {
                    quote_draft.set(Some(quote_message::Quote { role, anchor, text }));
                    if let Some(el) = web_sys::window()
                        .and_then(|w| w.document())
                        .and_then(|doc| doc.query_selector("#composer-input").ok().flatten())
                        .and_then(|el| el.dyn_into::<web_sys::HtmlElement>().ok())
                    {
                        let _ = el.focus();
                    }
                }
                None => ux_note.set("That line cannot be quoted until it finishes.".into()),
            }
        })
        .into(),
        edit_last: (move |id: u32| {
            let root = active_root.get_untracked();
            let last = transcript.with_untracked(|t| t.last_user_id());
            request_edit(edit_resend::action(
                session_busy(&root),
                !queue_items.get_untracked().is_empty(),
                last,
                id,
            ));
        })
        .into(),
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
            apply_title.run((active_root.get_untracked(), title));
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
        rewind: (move |keep: i32| {
            let root = active_root.get_untracked();
            if root.is_empty() || keep <= 0 {
                ux_note.set("No completed turn to rewind to.".into());
                return;
            }
            rewind_target.set(Some(rewind::RewindTarget { session: root, keep }));
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
    shell::bind(
        "session.rename",
        Rc::new(move |title| {
            let title = title.trim().to_string();
            if title.is_empty() {
                return Err("A title needs at least one character.".into());
            }
            rename_for_palette.run((active_root.get_untracked(), title));
            Ok(())
        }),
    );
    shell::bind(
        "session.export",
        Rc::new(move |_| {
            let id = active_root.get_untracked();
            if id.is_empty() {
                return Err("No session to export.".into());
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
            Ok(())
        }),
    );
    shell::bind(
        "session.export-md",
        Rc::new(move |_| {
            let md = transcript.with_untracked(|t| t.to_markdown());
            let id = active_root.get_untracked();
            let name = if id.is_empty() {
                "strike-session.md".into()
            } else {
                format!("strike-session-{}.md", file_stem(&id))
            };
            save_download(&name, "text/markdown;charset=utf-8", &md);
            ux_note.set("Downloaded the visible transcript.".into());
            Ok(())
        }),
    );
    shell::bind(
        "session.fork",
        Rc::new(move |_| {
            let cursor = session_panel::cursor_session();
            let id = if cursor.id.is_empty() {
                active_root.get_untracked()
            } else {
                cursor.id
            };
            if id.is_empty() {
                return Err("No session to fork.".into());
            }
            let (b, t) = (base.get_untracked(), token.get_untracked());
            spawn_local(async move {
                let path = format!("/v1/sessions/{}/fork", api::js_encode(&id));
                match post_json_value(&b, &t, &path, serde_json::json!({})).await {
                    Ok(created) => {
                        let new_id = created
                            .get("id")
                            .and_then(|v| v.as_str())
                            .unwrap_or("")
                            .to_string();
                        if new_id.is_empty() {
                            ux_note.set("Fork did not return a session.".into());
                            return;
                        }
                        ux_note.set("Forked this session.".into());
                        resume_session(new_id);
                    }
                    Err(e) => ux_note.set(format!("Fork failed: {e}")),
                }
            });
            Ok(())
        }),
    );

    let queue_step = {
        let queue_items = queue_items;
        let queue_focus = queue_focus;
        let post_queue = post_queue;
        move |delta: i32| -> Result<(), String> {
            let items = queue_items.get_untracked();
            if items.is_empty() {
                return Err("The queue is empty.".into());
            }
            let focus = queue_focus.get_untracked();
            let id = if items.iter().any(|item| item.id == focus) {
                focus
            } else {
                items.last().map(|item| item.id.clone()).unwrap_or_default()
            };
            let Some(ids) = steer_queue::moved_ids(&items, &id, delta) else {
                return Err("That message cannot move further.".into());
            };
            post_queue("input.queue_reorder", serde_json::json!({"ids": ids}));
            Ok(())
        }
    };
    let queue_step = Rc::new(queue_step);
    let queue_up = queue_step.clone();
    let queue_down = queue_step;
    shell::bind(
        "queue.move-up",
        Rc::new(move |_| queue_up(-1)),
    );
    shell::bind(
        "queue.move-down",
        Rc::new(move |_| queue_down(1)),
    );
    shell::bind(
        "panel-files",
        Rc::new(move |_| {
            side_tab.set("files".into());
            mode.set("chat".into());
            Ok(())
        }),
    );
    shell::bind(
        "lsp-diagnostics.open",
        Rc::new(move |_| {
            side_tab.set("diagnostics".into());
            mode.set("chat".into());
            diag_rev.update(|n| *n = n.wrapping_add(1));
            Ok(())
        }),
    );
    shell::bind(
        "search-in-files.run",
        Rc::new(move |args: &str| {
            let q = args.trim();
            if q.is_empty() {
                return Err("Type the text to find.".into());
            }
            side_tab.set("files".into());
            mode.set("chat".into());
            sif_query.set(q.to_string());
            sif_run.update(|n| *n = n.wrapping_add(1));
            Ok(())
        }),
    );
    shell::bind(
        "workspaces.change",
        Rc::new(move |_| {
            picker_open.set(true);
            Ok(())
        }),
    );
    shell::bind(
        "approvals.rules",
        Rc::new(move |_| {
            panel_open.set(true);
            mode.set("chat".into());
            Ok(())
        }),
    );
    shell::bind(
        "enter.toggle",
        Rc::new(move |_| {
            let next = if enter_mode.get_untracked() == "steer" {
                "queue"
            } else {
                "steer"
            };
            set_enter(next.into());
            Ok(())
        }),
    );
    shell::bind(
        "composer.attach",
        Rc::new(move |_| {
            if let Some(el) = file_input.get_untracked() {
                el.click();
            }
            Ok(())
        }),
    );
    shell::bind(
        "composer.voice",
        Rc::new(move |_| {
            if !chat_mode::dictation_available() {
                return Err("This browser has no speech API.".into());
            }
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
                    return Err(err);
                }
            }
            Ok(())
        }),
    );
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

    {
        let ui_mention = TriggerSignals {
            ac_force: ui.ac_force,
            ac_open: ui.ac_open,
            ac_index: ui.ac_index,
            ac_items: ui.ac_items,
            ac_replace_all: ui.ac_replace_all,
            trig_start: ui.trig_start,
            trig_end: ui.trig_end,
            skills: ui.skills,
            commands: ui.commands,
            search_gen: ui.search_gen,
            base: ui.base,
            token: ui.token,
            draft: ui.draft,
            plus_open: ui.plus_open,
            mode_menu: ui.mode_menu,
            model_menu: ui.model_menu,
        };
        let ui_shell = TriggerSignals {
            ac_force: ui.ac_force,
            ac_open: ui.ac_open,
            ac_index: ui.ac_index,
            ac_items: ui.ac_items,
            ac_replace_all: ui.ac_replace_all,
            trig_start: ui.trig_start,
            trig_end: ui.trig_end,
            skills: ui.skills,
            commands: ui.commands,
            search_gen: ui.search_gen,
            base: ui.base,
            token: ui.token,
            draft: ui.draft,
            plus_open: ui.plus_open,
            mode_menu: ui.mode_menu,
            model_menu: ui.model_menu,
        };
        let ui_skill = TriggerSignals {
            ac_force: ui.ac_force,
            ac_open: ui.ac_open,
            ac_index: ui.ac_index,
            ac_items: ui.ac_items,
            ac_replace_all: ui.ac_replace_all,
            trig_start: ui.trig_start,
            trig_end: ui.trig_end,
            skills: ui.skills,
            commands: ui.commands,
            search_gen: ui.search_gen,
            base: ui.base,
            token: ui.token,
            draft: ui.draft,
            plus_open: ui.plus_open,
            mode_menu: ui.mode_menu,
            model_menu: ui.model_menu,
        };
        shell::bind(
            "composer.mention",
            Rc::new(move |_| {
                mention_composer_file(&ui_mention);
                Ok(())
            }),
        );
        shell::bind(
            "composer.shell",
            Rc::new(move |_| {
                prefix_composer_shell(&ui_shell);
                Ok(())
            }),
        );
        shell::bind(
            "composer.skill",
            Rc::new(move |_| {
                open_composer_catalog("skills", &ui_skill);
                Ok(())
            }),
        );
    }
    shell::bind(
        "model.effort",
        Rc::new(move |level| {
            let level = level.trim().to_string();
            if level.is_empty() {
                return Err("Pick an effort.".into());
            }
            on_effort.run(level);
            Ok(())
        }),
    );
    shell::bind(
        "model.compact",
        Rc::new(move |_| {
            on_compact.run(());
            Ok(())
        }),
    );
    shell::bind(
        "model.effort-default",
        Rc::new(move |level| {
            let level = level.trim().to_string();
            if level.is_empty() {
                return Err("Pick an effort.".into());
            }
            on_default_effort.run(level);
            Ok(())
        }),
    );
    shell::bind(
        "model.default",
        Rc::new(move |args| {
            let Some((provider, id)) = args.split_once('\t') else {
                return Err("Pick a model.".into());
            };
            on_default_model.run((provider.to_string(), id.to_string()));
            Ok(())
        }),
    );
    shell::bind(
        "model.pin",
        Rc::new(move |_| {
            let provider = provider_name.get_untracked();
            let model = model_name.get_untracked();
            if provider.is_empty() || model.is_empty() {
                return Err("No model is selected.".into());
            }
            crate::model_menu_view::toggle_stored_pin(&provider, &model);
            model_note.set("Updated the model pin.".into());
            Ok(())
        }),
    );
    shell::bind(
        "composer.context",
        Rc::new(move |_| {
            let ctx = active_context(
                &catalog.get_untracked(),
                &provider_name.get_untracked(),
                &model_name.get_untracked(),
            );
            let view = context_view(
                context_used.get_untracked(),
                context_limit.get_untracked(),
                ctx,
                &context_fit.get_untracked(),
            );
            model_note.set(if view.summary.is_empty() {
                "No context window reported.".into()
            } else {
                view.summary
            });
            Ok(())
        }),
    );


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

    let git_active = Memo::new(move |_| side_tab.get() == "git");
    let worktree_active = Memo::new(move |_| side_tab.get() == "worktrees");
    let review_active = Memo::new(move |_| side_tab.get() == "review");
    // Names the open session for the chat-mode bar and the chat view's h1.
    let chat_title = Memo::new(move |_| {
        let id = active_root.get();
        if id.is_empty() {
            return "No session".to_string();
        }
        sessions
            .get()
            .into_iter()
            .find(|s| s.id == id)
            .map(|s| {
                let fallback = short_id(&s.id);
                session_label(&s.title, &fallback)
            })
            .unwrap_or_else(|| short_id(&id))
    });

    view! {
        <LiveRegions announcer=announcer />
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
            open_session=open_session_page
        />
        <shell::Chrome />
        {gateway_qr::phone_view()}
        <prompt_library::PromptLibrary draft=draft />
        <session_delete::SessionDeleteDialog
            target=delete_target
            base=base
            token=token
            on_deleted=on_deleted
        />
        <rewind::RewindDialog
            target=rewind_target
            base=base
            token=token
            busy_turn=rewind_busy
            on_done=on_rewound
        />
        <global_search::GlobalSearch
            open=gs_open
            query=gs_query
            base=base
            token=token
            workspaces=workspaces
            on_pick=on_gs_pick
        />
        <Show when=move || mode.get() == "providers" fallback=move || ()>
            <providers::ProvidersPage base=base token=token />
        </Show>
        <Show when=move || !onboarded.get() && onboarding_daemon_checked.get()>
            <onboarding::OnboardingCard
                base=base
                token=token
                status=status
                theme=theme_id
                theme_catalog=themes_state
                on_theme=set_theme
                on_done=finish_onboarding
            />
        </Show>
        <Show when=move || mode.get() == "settings">
            <settings::SettingsPage
                base=base
                token=token
                gateway=gateway
                mode=mode
                status=status
                diag_capable=diag_capable
                project_init_capable=project_init_capable
                container_capable=container_capable
                config_files_capable=config_files_capable
                root=active_root
                theme=theme_id
                theme_catalog=themes_state
                theme_note=theme_note
                scheme=scheme_id
                font=font_id
                density=density_id
                on_theme=set_theme
                on_scheme=set_scheme
                on_font=set_font
                on_density=set_density
                on_cleared=on_cleared
            />
        </Show>
        <Show when=move || mode.get() == "daemon" fallback=move || ()>
            <DaemonPane gateway_detail=gateway_detail status=status />
        </Show>
        <Show when=move || mode.get() == "usage" || mode.get() == "audit" fallback=move || ()>
            <usage_audit::UsageAuditPages
                mode=mode
                base=base
                token=token
                status=status
                open_session=open_session_page
            />
        </Show>
        <Show when=move || mode.get() == "goals" fallback=move || ()>
            <goals::GoalsPane base=base token=token />
        </Show>
        <Show when=move || mode.get() == "dashboards" fallback=move || ()>
            <dashboards::DashboardsPane base=base token=token active_root=active_root />
        </Show>
        <Show when=move || mode.get() == "ideas" fallback=move || ()>
            <ideas::IdeasPage base=base token=token draft=draft active_root=active_root mode=mode />
        </Show>
        <Show when=move || mode.get() == "tasks" fallback=move || ()>
            <task_runner::TaskRunner
                base=base
                token=token
                active_root=active_root
                status=status
                mode=mode
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
        <Show when=move || mode.get() == "specs" fallback=move || ()>
            <spec_authoring::SpecAuthoringPage base=base token=token status=status active_root=active_root mode=mode />
        </Show>
        <Show when=move || mode.get() == "workflows" fallback=move || ()>
            <workflows::WorkflowsPage base=base token=token status=status />
        </Show>
        <Show when=move || mode.get() == "triggers" fallback=move || ()>
            <triggers::TriggersPage
                base=base
                token=token
                status=status
                open_session=move |id: String| {
                    mode.set("chat".into());
                    let live = roots.get_untracked().iter().any(|r| r.id == id);
                    open_session(id, live);
                }
            />
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
        <Show when=move || mode.get() == "customize" fallback=move || ()>
            <CustomizePane base=base token=token status=status plugins_capable=plugins_capable />
        </Show>
        <Show when=move || mode.get() == "chat">
            <div
                class=move || {
                    let mut class = chat_mode::shell_class(
                        focus.get(),
                        &layout.get(),
                        sessions_drawer.get(),
                        panel_drawer.get(),
                    );
                    class.push_str(" phone-pane-");
                    class.push_str(&phone_pane.get());
                    class
                }
                on:touchstart=move |ev: web_sys::TouchEvent| {
                    // Edge-swipe session navigation (#1490 slice 5): only
                    // gestures that begin within 24px of the left/right
                    // screen edge are candidates, and never ones that
                    // begin on a horizontal scroller or a text field —
                    // code blocks own their drags. The handlers live on
                    // the shell so a swipe that starts on the outermost
                    // pixels (outside the chat section's inset) counts.
                    if sessions_drawer.get_untracked() || panel_drawer.get_untracked() {
                        swipe_start.set(None);
                        return;
                    }
                    if touch_in_scroller(ev.target()) {
                        swipe_start.set(None);
                        return;
                    }
                    if let Some(t) = ev.changed_touches().item(0) {
                        swipe_start.set(Some((t.client_x(), t.client_y())));
                    }
                }
                on:touchend=move |ev: web_sys::TouchEvent| {
                    let Some((x0, y0)) = swipe_start.get_untracked() else {
                        return;
                    };
                    swipe_start.set(None);
                    if sessions_drawer.get_untracked() || panel_drawer.get_untracked() {
                        return;
                    }
                    let Some(t) = ev.changed_touches().item(0) else {
                        return;
                    };
                    let dx = t.client_x() - x0;
                    let dy = t.client_y() - y0;
                    if dx.abs() < 48 || dx.abs() < dy.abs() * 2 {
                        return;
                    }
                    let iw = web_sys::window()
                        .and_then(|w| w.inner_width().ok())
                        .and_then(|v| v.as_f64())
                        .unwrap_or(0.0) as i32;
                    let delta = if x0 <= 24 && dx > 0 {
                        -1
                    } else if iw > 0 && x0 >= iw - 24 && dx < 0 {
                        1
                    } else {
                        return;
                    };
                    if let Some((id, live)) = session_list::neighbour(
                        &session_nav.get_untracked(),
                        &active_root.get_untracked(),
                        delta,
                    ) {
                        ev.prevent_default();
                        open_session(id, live);
                    }
                }
            >
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
                        nav_order=session_nav
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
                <main class="chat" aria-labelledby="chat-heading">
                    // #1516: landmark and heading so assistive tech can jump
                    // to the conversation. Visually hidden; the status tabs
                    // and chat-mode bar already show the session name.
                    <h1 id="chat-heading" class="sr-only">{move || chat_title.get()}</h1>
                    <AwayBriefing base=base token=token roots=roots on_focus=on_notify />
                    // #1781: persistent untrusted-workspace badge + trust card.
                    <workspace_trust::WorkspaceTrust
                        base=base
                        token=token
                        root=active_root
                        status=status
                        capable=workspace_trust_capable
                    />
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
                        <span class="chat-mode-title">{move || chat_title.get()}</span>
                        <button type="button" class="chip" data-chat-panel="true" on:click=move |_| {
                            sessions_drawer.set(false);
                            panel_drawer.update(|v| *v = !*v);
                        }>"Panel"</button>
                    </div>
                    // #1874: "N lessons used — view" for the active session.
                    <lessons::LessonsUsedMarker
                        base=base
                        token=token
                        session=active_root
                        busy=live_busy
                        storage_rev=storage_rev
                    />
                    <ErrorNotice message=load_error on_retry=UnsyncCallback::new(move |_: ()| refresh_meta()) />
                    <Show when=move || !stream_note.get().is_empty()>
                        <p class="hint banner">{move || stream_note.get()}</p>
                    </Show>
                    <stale_resume::StaleResumeCard
                        warning=stale_warning
                        on_fresh=on_stale_fresh
                        on_cancel=on_stale_cancel
                    />
                    <Show when=move || !load_notice.get().is_empty()>
                        <div class="notice" role="status" data-load-warning-notice="true">
                            <span>{move || load_notice.get()}</span>
                            <span class="load-warning-actions">
                                <button class="secondary" data-load-warning-open="true" on:click=move |_| {
                                    load_notice.set(String::new());
                                    let _ = shell::set_view("customize");
                                }>"Open Customize"</button>
                                <button class="secondary" data-load-warning-dismiss="true" on:click=move |_| load_notice.set(String::new())>"Dismiss"</button>
                            </span>
                        </div>
                    </Show>
                    // #1785: a deploy watch session shows its verdict card.
                    <deploy_watch::DeployWatchCard base=base token=token active=active_root tick=tick />
                    <Show when=move || !doc_path.get().is_empty()>
                        <diagnostics::DocBadge
                            state=diag_state
                            doc_path=doc_path
                            on_click=Callback::new(move |_| {
                                side_tab.set("diagnostics".into());
                                diag_rev.update(|n| *n = n.wrapping_add(1));
                            })
                        />
                        <DocumentPane
                            base=base
                            token=token
                            path=doc_path
                            jump_line=doc_line
                            want_diff=doc_diff
                            editing=doc_editing
                            root=active_root
                            busy=session_busy_now
                            history=file_history
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
                                let hits = transcript_matches(&list, &q, crate::editor::FindOptions::new(tf_match_case.get(), tf_whole_word.get()));
                                transcript_find::count_label(&q, hits.len(), tf_idx.get())
                            }}</span>
                            <button
                                type="button"
                                class=move || if tf_match_case.get() { "chip on tf-find-toggle" } else { "chip tf-find-toggle" }
                                data-tf-match-case="true"
                                title="Match case"
                                aria-label="Match case"
                                aria-pressed=move || if tf_match_case.get() { "true" } else { "false" }
                                on:click=move |_| { tf_match_case.update(|v| *v = !*v); tf_idx.set(0); }
                            >"Aa"</button>
                            <button
                                type="button"
                                class=move || if tf_whole_word.get() { "chip on tf-find-toggle" } else { "chip tf-find-toggle" }
                                data-tf-whole-word="true"
                                title="Whole word"
                                aria-label="Whole word"
                                aria-pressed=move || if tf_whole_word.get() { "true" } else { "false" }
                                on:click=move |_| { tf_whole_word.update(|v| *v = !*v); tf_idx.set(0); }
                            >"ab|"</button>
                            <button type="button" class="secondary" data-tf-prev="true" on:click=move |_| tf_step(-1)>"Prev"</button>
                            <button type="button" class="secondary" data-tf-next="true" on:click=move |_| tf_step(1)>"Next"</button>
                            <button type="button" class="secondary" data-tf-close="true" on:click=move |_| close_find()>"Close"</button>
                        </div>
                    </Show>
                    <Show when=move || doc_path.get().is_empty()>
                    <ShareDialog base=base token=token session=active_root />
                    <TranscriptPane
                        transcript=transcript
                        session=active_root
                        loading=loading
                        load_error=load_error
                        tick=tick
                        note=ux_note
                        handlers=handlers
                        find=find_view
                        pins=pin_store
                    />
                    </Show>
                    <debug_mode_ui::DebugPanel base=base token=token root=active_root debug=debug_state />
                    <elicitation_ui::ElicitationCards base=base token=token root=active_root elicitations=elicitations />
                    <questions_ui::QuestionCards base=base token=token root=active_root questions=questions />
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
                        <AutonomyMenu
                            menu_open=autonomy_menu
                            autonomy=autonomy
                            confirm=confirm
                            busy=session_busy_now
                            base=base
                            token=token
                            root=active_root
                            status=status
                        />
                        <Show when=move || {
                            let ctx = active_context(&catalog.get(), &provider_name.get(), &model_name.get());
                            context_view(context_used.get(), context_limit.get(), ctx, &context_fit.get()).held
                        }>
                            <div class="steer-hold" role="status" data-context-hold="true">
                                <p class="model-hold">{move || {
                                    let ctx = active_context(&catalog.get(), &provider_name.get(), &model_name.get());
                                    context_view(context_used.get(), context_limit.get(), ctx, &context_fit.get()).hold_message
                                }}</p>
                                <button type="button" class="secondary" on:click=move |_| {
                                    hold_bypass.set(true);
                                    send(steer_queue::SendWhich::Primary);
                                }>"Send anyway"</button>
                            </div>
                        </Show>
                        <Show when=move || {
                            let ctx = active_context(&catalog.get(), &provider_name.get(), &model_name.get());
                            let base_view = context_view(context_used.get(), context_limit.get(), ctx, &context_fit.get());
                            draft_size::draft_size_view(base_view.used, base_view.limit, base_view.held, &draft.get()).level != draft_size::DraftSizeLevel::None
                        }>
                            <p class=move || {
                                let ctx = active_context(&catalog.get(), &provider_name.get(), &model_name.get());
                                let base_view = context_view(context_used.get(), context_limit.get(), ctx, &context_fit.get());
                                let view = draft_size::draft_size_view(base_view.used, base_view.limit, base_view.held, &draft.get());
                                if view.level == draft_size::DraftSizeLevel::Critical { "hint draft-size-warn critical" } else { "hint draft-size-warn" }
                            } role="status" data-draft-size=move || {
                                let ctx = active_context(&catalog.get(), &provider_name.get(), &model_name.get());
                                let base_view = context_view(context_used.get(), context_limit.get(), ctx, &context_fit.get());
                                let view = draft_size::draft_size_view(base_view.used, base_view.limit, base_view.held, &draft.get());
                                match view.level { draft_size::DraftSizeLevel::Critical => "critical", draft_size::DraftSizeLevel::Warn => "warn", draft_size::DraftSizeLevel::None => "none" }
                            }>{move || {
                                let ctx = active_context(&catalog.get(), &provider_name.get(), &model_name.get());
                                let base_view = context_view(context_used.get(), context_limit.get(), ctx, &context_fit.get());
                                let text = draft.get();
                                let view = draft_size::draft_size_view(base_view.used, base_view.limit, base_view.held, &text);
                                if view.level == draft_size::DraftSizeLevel::Critical && draft_armed.get() == text.trim() && !text.trim().is_empty() {
                                    draft_size::armed_message(&view)
                                } else {
                                    view.message
                                }
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
                                    let on = memory_mode_known.get() && memory_mode.get() == id;
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
                                    <span class="name">"Mention a file or session"</span>
                                    <span class="path">"Insert @ and search the working tree and recent sessions"</span>
                                </button>
                                <button type="button" class="palette-row" on:click=move |_| prefix_composer_shell(&ui)>
                                    <span class="name">"Run a shell command"</span>
                                    <span class="path">"Prefix ! and ask before it runs"</span>
                                </button>
                                <button type="button" class="palette-row" on:click=move |_| open_composer_catalog("skills", &ui)>
                                    <span class="name">"Use a skill"</span>
                                    <span class="path">"Skills loaded for this daemon"</span>
                                </button>
                                <button type="button" class="palette-row" data-plus-approval="true" on:click=move |_| {
                                    plus_open.set(false);
                                    autonomy_menu.set(false);
                                    mode_menu.set(true);
                                }>
                                    <span class="name">{move || format!("Approval · {}", mode_label(&perm_mode.get()))}</span>
                                    <span class="path">"Who answers tool asks in this session"</span>
                                </button>
                                <button type="button" class="palette-row" data-plus-verbosity="true" on:click=move |_| {
                                    plus_open.set(false);
                                    crate::shell::show_palette_at(crate::verbosity::COMMAND);
                                }>
                                    <span class="name">"Response style"</span>
                                    <span class="path">"Concise, Default, or Detailed for this session"</span>
                                </button>
                                <button type="button" class="palette-row" data-plus-autonomy="true" on:click=move |_| {
                                    plus_open.set(false);
                                    mode_menu.set(false);
                                    autonomy_menu.set(true);
                                }>
                                    <span class="name">{move || format!("Autonomy · {}", crate::model::autonomy_label(&autonomy.get()))}</span>
                                    <span class="path">"Who clears a workflow phase exit"</span>
                                </button>
                                <button type="button" class="palette-row" data-prompts-open-menu="true" on:click=move |_| {
                                    plus_open.set(false);
                                    let _ = crate::shell::dispatch(prompt_library::INSERT_ID, "");
                                }>
                                    <span class="name">"Insert saved prompt"</span>
                                    <span class="path">"Search your prompt library"</span>
                                </button>
                                <button type="button" class="palette-row" data-prompts-save-menu="true" on:click=move |_| {
                                    plus_open.set(false);
                                    let _ = crate::shell::dispatch(prompt_library::SAVE_ID, "");
                                }>
                                    <span class="name">"Save draft as prompt"</span>
                                    <span class="path">"Keep this draft in the prompt library"</span>
                                </button>
                                <button type="button" class="palette-row" data-sharpen-menu="true" on:click=move |_| {
                                    plus_open.set(false);
                                    let _ = sharpen.start();
                                }>
                                    <span class="name">"Sharpen prompt"</span>
                                    <span class="path">"Rewrite the draft with the session model (Alt+P)"</span>
                                </button>
                                <button type="button" class="palette-row" data-chat-attach="menu" on:click=move |_| {
                                    plus_open.set(false);
                                    if let Some(el) = file_input.get() {
                                        el.click();
                                    }
                                }>
                                    <span class="name">"Attach files"</span>
                                    <span class="path">"Images and documents. Drag and paste still work."</span>
                                </button>
                                <Show when=move || speech_ok>
                                    <button type="button" class="palette-row" data-chat-voice="true" on:click=move |_| {
                                        plus_open.set(false);
                                        let _ = crate::shell::dispatch("composer.voice", "");
                                    }>
                                        <span class="name">"Dictate"</span>
                                        <span class="path">"Voice input from this browser"</span>
                                    </button>
                                </Show>
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
                                        if item.id.starts_with("section-") {
                                            return view! {
                                                <p class="hint palette-section" role="presentation" data-composer-section=item.id.clone()>{label}</p>
                                            }.into_any();
                                        }
                                        view! {
                                            <button type="button" role="option" class=klass data-suggest-id=item.id.clone() disabled=move || !selectable aria-selected=selected.to_string() on:click=move |_| accept_suggestion(picked.clone(), &ui)>
                                                <span class="name">{label}</span>
                                                <span class="path">{detail}</span>
                                            </button>
                                        }.into_any()
                                    }).collect_view()
                                }}
                            </div>
                        </Show>
                        <plan_card::PlanCard plan=plan />
                        <div class="steer-queue" id="steer-queue">
                            <Show when=move || last_edit.get().editing() || !edit_note.get().is_empty()>
                                <p class="steer-note edit-resend-note" role="status">
                                    {move || edit_resend::banner(&last_edit.get(), &edit_note.get())}
                                </p>
                            </Show>
                            <OutboxList outbox=outbox root=active_root gateway=gateway />
                            <session_loops::LoopRows base=base token=token root=active_root capable=loops_capable loops=loop_rows note=loop_note />
                            <Show when=move || !steer_note.get().is_empty()>
                                <p class="steer-note" role="status">{move || steer_note.get()}</p>
                            </Show>
                            <Show when=move || quote_draft.with(|q| q.is_some())>
                                <div class="quote-chip" role="group" aria-label="Quoted message">
                                    <span class="quote-chip-text">
                                        {move || quote_draft.with(|q| q.as_ref().map(|q| quote_message::label(&q.role, &q.text)).unwrap_or_default())}
                                    </span>
                                    <button
                                        type="button"
                                        class="quote-chip-remove"
                                        aria-label="Remove quote"
                                        title="Remove quote (Backspace in an empty composer)"
                                        on:click=move |_| quote_draft.set(None)
                                    >"×"</button>
                                </div>
                            </Show>
                            <Show when=move || !queue_note.get().is_empty()>
                                <p class="steer-error" role="alert">{move || queue_note.get()}</p>
                            </Show>
                            <Show when=move || !queue_items.get().is_empty()>
                                <ol class="queue-list" aria-label="Queued messages">
                                    {move || {
                                        let busy_now = session_busy_now.get();
                                        queue_items.get().into_iter().enumerate().map(|(i, item)| {
                                        let id = item.id.clone();
                                        let id_for_when = id.clone();
                                        let id_for_fallback = id.clone();
                                        let id_cancel = id.clone();
                                        let id_focus = id.clone();
                                        let id_send_now = id.clone();
                                        let row_busy = busy_now;
                                        let text_for_edit = item.text.clone();
                                        let preview = steer_queue::preview(&text_for_edit);
                                        view! {
                                            <li on:mouseenter=move |_| queue_focus.set(id_focus.clone())>
                                                <span class="queue-index">{(i + 1).to_string()}</span>
                                                <Show when=move || editing_id.get() == id.clone() fallback=move || view! { <span class="queue-text">{preview.clone()}</span> }>
                                                    <input class="queue-edit" prop:value=move || edit_draft.get() on:input=move |ev| edit_draft.set(event_target_value(&ev)) on:keydown=move |ev: web_sys::KeyboardEvent| {
                                                        if ev.key() == "Enter" {
                                                            ev.prevent_default();
                                                            let id = editing_id.get_untracked();
                                                            let text = edit_draft.get_untracked();
                                                            editing_id.set(String::new());
                                                            post_queue("input.queue_edit", serde_json::json!({"id": id, "text": text}));
                                                        } else if ev.key() == "Escape" {
                                                            ev.prevent_default();
                                                            ev.stop_propagation();
                                                            editing_id.set(String::new());
                                                        }
                                                    } />
                                                </Show>
                                                <span class="queue-actions">
                                                    <Show when=move || row_busy>
                                                        {let id_click = id_send_now.clone();
                                                        view! {
                                                            <button type="button" class="secondary" title="Send this message into the running turn now. The messages it jumps stay queued, in order." on:click=move |_| {
                                                                steer_note.set(steer_queue::STEER_WAITING.into());
                                                                steer_mode.set(String::new());
                                                                post_queue("input.queue_send_now", serde_json::json!({"id": id_click.clone()}));
                                                            }>"Send now"</button>
                                                        }}
                                                    </Show>
                                                    <Show when=move || editing_id.get() == id_for_when.clone() fallback=move || {
                                                        let id_edit = id_for_fallback.clone();
                                                        let text = text_for_edit.clone();
                                                        view! { <button type="button" class="secondary" on:click=move |_| { editing_id.set(id_edit.clone()); edit_draft.set(text.clone()); }>"Edit"</button> }
                                                    }>
                                                        <button type="button" class="secondary" on:click=move |_| {
                                                            let id = editing_id.get_untracked();
                                                            let text = edit_draft.get_untracked();
                                                            editing_id.set(String::new());
                                                            post_queue("input.queue_edit", serde_json::json!({"id": id, "text": text}));
                                                        }>"Save"</button>
                                                    </Show>
                                                    <button type="button" class="secondary" title="Removes this queued message" on:click=move |_| {
                                                        let id = id_cancel.clone();
                                                        if !crate::confirm::ask(
                                                            crate::confirm::ConfirmRequest {
                                                                title: "Remove queued message?".into(),
                                                                body: "This drops the message from the queue. It is not sent.".into(),
                                                                confirm_label: "Remove".into(),
                                                            },
                                                            Rc::new(move || {
                                                                post_queue(
                                                                    "input.queue_cancel",
                                                                    serde_json::json!({"id": id.clone()}),
                                                                );
                                                            }),
                                                        ) {
                                                            steer_note.set(crate::confirm::BUSY.into());
                                                        }
                                                    }>"Cancel"</button>
                                                </span>
                                            </li>
                                        }
                                        }).collect_view()
                                    }}
                                </ol>
                            </Show>
                            <Show when=move || !queue_items.get().is_empty() && !session_busy_now.get()>
                                <p class="queue-note">"No turn is running — queued messages send on their own, in order."</p>
                            </Show>
                        </div>
                        <form on:submit=move |ev| { ev.prevent_default(); send(steer_queue::SendWhich::Primary); }>
                            <Show when=move || !pending_files.get().is_empty()>
                                <div class="attach-row">
                                    {move || pending_files.get().into_iter().map(|file| {
                                        let id = file.id;
                                        let name = file.name.clone();
                                        let preview = file.preview.clone();
                                        let error = file.error.clone();
                                        let show_err = !error.is_empty();
                                        // #1390: audio/video chips get native controls over
                                        // the same blob URL images use.
                                        let preview_view = match (preview.is_empty(), chat_mode::preview_kind(&file.mime)) {
                                            (false, chat_mode::PreviewKind::Image) => view! { <img src=preview.clone() alt="" /> }.into_any(),
                                            (false, chat_mode::PreviewKind::Audio) => view! { <audio class="attach-media" controls=true preload="metadata" src=preview.clone()></audio> }.into_any(),
                                            (false, chat_mode::PreviewKind::Video) => view! { <video class="attach-media" controls=true preload="metadata" playsinline=true src=preview.clone()></video> }.into_any(),
                                            _ => view! { <span></span> }.into_any(),
                                        };
                                        let show_img = !preview.is_empty();
                                        // #1545: raster images can be annotated; the overlay
                                        // reads the same blob URL as the thumbnail.
                                        let can_annotate = show_img && !show_err && annotate::can_annotate(&file.mime);
                                        let annotate_for = annotate::Target { id, name: name.clone(), preview: preview.clone() };
                                        let mark_count = move || annotate_marks.with(|m| m.get(&id).map(|v| v.len()).unwrap_or(0));
                                        let remove_now = move || {
                                            pending_files.update(|rows| {
                                                if let Some(i) = rows.iter().position(|row| row.id == id) {
                                                    chat_mode::revoke_preview(&rows[i].preview);
                                                    rows.remove(i);
                                                }
                                            });
                                            annotate_marks.update(|m| {
                                                m.remove(&id);
                                            });
                                        };
                                        view! {
                                            <span class="attach-chip">
                                                {preview_view}
                                                <span>
                                                    <span class="name">{name}</span>
                                                    <Show when=move || show_err>
                                                        <span class="path">{error.clone()}</span>
                                                    </Show>
                                                    <Show when=move || { mark_count() > 0 }>
                                                        <span class="attach-marks" data-attach-marks="true">{move || {
                                                            let n = mark_count();
                                                            if n == 1 { "1 mark".to_string() } else { format!("{n} marks") }
                                                        }}</span>
                                                    </Show>
                                                </span>
                                                {can_annotate.then(|| view! {
                                                    <button type="button" data-annotate-open="true" aria-label="Annotate image" on:click=move |_| {
                                                        annotate_target.set(Some(annotate_for.clone()));
                                                    }>"Annotate"</button>
                                                })}
                                                <button type="button" aria-label="Remove attachment" on:click=move |_| {
                                                    // Unsent marks are listed before they go: removing
                                                    // the image discards them only on an explicit yes.
                                                    match annotate::discard_body(mark_count()) {
                                                        None => remove_now(),
                                                        Some(body) => {
                                                            let opened = crate::confirm::ask(
                                                                crate::confirm::ConfirmRequest {
                                                                    title: "Remove image and its marks?".into(),
                                                                    body,
                                                                    confirm_label: "Remove".into(),
                                                                },
                                                                Rc::new(remove_now),
                                                            );
                                                            if !opened {
                                                                status.set(crate::confirm::BUSY.into());
                                                            }
                                                        }
                                                    }
                                                }>"Remove"</button>
                                            </span>
                                        }
                                    }).collect_view()}
                                </div>
                                {move || {
                                    // #1561: say when the engine is known to drop a file
                                    // before the model sees it. Warn, never block.
                                    let rows: Vec<(String, String)> = pending_files
                                        .get()
                                        .into_iter()
                                        .filter(|f| f.error.is_empty())
                                        .map(|f| (f.name, f.mime))
                                        .collect();
                                    let lines = attachment_honesty::composer_warnings(&provider_name.get(), &rows);
                                    (!lines.is_empty()).then(|| view! {
                                        <div class="attach-warning" role="status" data-attach-warning="true">
                                            {lines.into_iter().map(|line| view! { <p>{line}</p> }).collect_view()}
                                        </div>
                                    })
                                }}
                            </Show>
                            <input class="file-pick" type="file" data-chat-file="true" data-chat-attach="true" node_ref=file_input multiple=true
                                accept=chat_mode::ACCEPT_TYPES
                                on:change=move |_| {
                                    if let Some(el) = file_input.get() {
                                        if let Some(files) = el.files() {
                                            chat_mode::enqueue_file_list(files, pending_files, status);
                                        }
                                        el.set_value("");
                                    }
                                }
                            />
                            {move || prompt_sharpen::bar_view(sharpen)}
                            <session_ref_chip::SessionRefChips draft=draft base=base token=token root=active_root on_open=on_open_session_ref />
                            <textarea
                                id="composer-input"
                                aria-label="Message this session"
                                placeholder="Message this session"
                                prop:value=move || draft.get()
                                on:input=move |ev| {
                                    if let Some((text, cursor)) = textarea_state(ev.target()) {
                                        draft.set(text.clone());
                                        // #1361: clearing the recalled text
                                        // abandons the edit.
                                        if text.trim().is_empty()
                                            && matches!(last_edit.get_untracked(), edit_resend::State::Editing { .. })
                                        {
                                            last_edit.set(edit_resend::State::Idle);
                                            edit_note.set(String::new());
                                        } else if !last_edit.get_untracked().editing()
                                            && !edit_note.get_untracked().is_empty()
                                        {
                                            // A refusal note goes once the user types on.
                                            edit_note.set(String::new());
                                        }
                                        sync_composer_trigger(text, cursor, &ui);
                                    }
                                    // Auto-grow (#1411): the DOM value is
                                    // already current in the input handler.
                                    sync_composer_height_target(ev.target());
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
                                    // Prompt sharpen (#1385): Alt+P rewrites the
                                    // draft; `code` also matches macOS, where
                                    // Option+P types a symbol.
                                    if ev.alt_key() && !ev.ctrl_key() && !ev.meta_key()
                                        && (ev.key().eq_ignore_ascii_case("p") || ev.code() == "KeyP")
                                    {
                                        // The chord is consumed here: it must not
                                        // bubble on to page-level key handlers.
                                        ev.prevent_default();
                                        ev.stop_propagation();
                                        let _ = sharpen.start();
                                        return;
                                    }
                                    if ev.key() == "ArrowDown" || ev.key() == "ArrowUp" {
                                        let items = ac_items.get_untracked();
                                        if ac_open.get_untracked() && items.iter().any(|item| item.selectable) {
                                            ev.prevent_default();
                                            let delta = if ev.key() == "ArrowDown" { 1 } else { -1 };
                                            ac_index.set(move_index(&items, ac_index.get_untracked(), delta));
                                        } else if ev.key() == "ArrowUp"
                                            && (ev.ctrl_key() || ev.meta_key())
                                            && !ev.alt_key()
                                            && !ev.shift_key()
                                        {
                                            // Edit last message (#1361): Cmd/Ctrl+Up
                                            // with an empty composer.
                                            let root = active_root.get_untracked();
                                            let gate = edit_resend::Gate {
                                                draft_empty: draft.get_untracked().trim().is_empty(),
                                                busy: session_busy(&root),
                                                queued: !queue_items.get_untracked().is_empty(),
                                                picker_open: ac_open.get_untracked() || plus_open.get_untracked(),
                                                composing: ev.is_composing(),
                                            };
                                            let r = edit_resend::recall(gate, transcript.with_untracked(|t| t.last_user_id()));
                                            if r != edit_resend::Recall::Inert {
                                                ev.prevent_default();
                                            }
                                            request_edit(r);
                                        } else if !ev.ctrl_key() && !ev.meta_key() && !ev.alt_key() {
                                            // Prompt history (#1402), after the
                                            // autocomplete popup above has had
                                            // first claim on ↑/↓. TUI rule
                                            // (docs/keybinds.md:99): cycle only
                                            // when the key would not move the
                                            // caret within a multiline draft.
                                            if let Some((text, cursor)) = textarea_state(ev.target()) {
                                                let up = ev.key() == "ArrowUp";
                                                let can_move = if up {
                                                    caret_can_move_up(&text, cursor)
                                                } else {
                                                    caret_can_move_down(&text, cursor)
                                                };
                                                if !can_move {
                                                    let mut hist = prompt_history.get_untracked();
                                                    let recalled = if up {
                                                        hist.prev(&text)
                                                    } else {
                                                        hist.next(&text)
                                                    };
                                                    if let Some(value) = recalled {
                                                        ev.prevent_default();
                                                        prompt_history.set(hist);
                                                        draft.set(value);
                                                        place_composer_caret_end_soon();
                                                    }
                                                }
                                            }
                                        }
                                    } else if ev.key() == "Backspace"
                                        && draft.get_untracked().is_empty()
                                        && quote_draft.with_untracked(|q| q.is_some())
                                    {
                                        // #1362: Backspace on an empty composer removes the quote.
                                        ev.prevent_default();
                                        quote_draft.set(None);
                                    } else if ev.key() == "Escape" && (ac_open.get_untracked() || plus_open.get_untracked()) {
                                        ev.prevent_default();
                                        ev.stop_propagation();
                                        ac_open.set(false);
                                        plus_open.set(false);
                                        ac_force.set(String::new());
                                    } else if ev.key() == "Escape" && last_edit.get_untracked().editing() {
                                        // #1361: abandon the edit. Before the
                                        // confirm this has no side effects.
                                        ev.prevent_default();
                                        ev.stop_propagation();
                                        let (next, clear) = edit_resend::on_escape(&last_edit.get_untracked());
                                        last_edit.set(next);
                                        edit_note.set(String::new());
                                        if clear {
                                            draft.set(String::new());
                                            place_composer_caret_end_soon();
                                        }
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
                                            if ev.alt_key() && !ev.ctrl_key() && !ev.meta_key() {
                                                hold_bypass.set(true);
                                                send(steer_queue::SendWhich::Primary);
                                            } else if ev.ctrl_key() || ev.meta_key() {
                                                send(steer_queue::SendWhich::Alternate);
                                            } else {
                                                send(steer_queue::SendWhich::Primary);
                                            }
                                        }
                                    } else if ev.key() == "Escape"
                                        && draft.get_untracked().trim().is_empty()
                                        && editing_id.get_untracked().is_empty()
                                    {
                                        let root = active_root.get_untracked();
                                        if session_busy(&root)
                                            && !mode_menu.get_untracked()
                                            && !model_menu.get_untracked()
                                            && !memory_menu.get_untracked()
                                        {
                                            ev.prevent_default();
                                            ev.stop_propagation();
                                            stop_turn();
                                        }
                                    } else if ev.key() == "Backspace" && draft.get_untracked().is_empty() {
                                        if let Some(item) = queue_items.get_untracked().last().cloned() {
                                            ev.prevent_default();
                                            draft.set(item.text);
                                            post_queue("input.queue_cancel", serde_json::json!({"id": item.id}));
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
                                // #1512: the model chip is a labelled control,
                                // first after "+" so a 390px phone shows it
                                // without sideways-scrolling the chip strip.
                                // It opens Switch model, which only accepts
                                // catalog rows.
                                <button type="button" class="composer-glance model-glance" data-model-status="true" title="Change the session model (Alt+M opens the palette)" aria-label=move || {
                                    let model = model_name.get();
                                    let name = if model.is_empty() { provider_name.get() } else { model };
                                    if name.is_empty() { "Model: none. Change model".to_string() } else { format!("Model: {name}. Change model") }
                                } on:click=move |_| {
                                    mode_menu.set(false);
                                    model_menu.set(false);
                                    autonomy_menu.set(false);
                                    plus_open.set(false);
                                    ac_open.set(false);
                                    ac_force.set(String::new());
                                    shell::show_palette_at("switch-model");
                                }>{move || {
                                    let model = model_name.get();
                                    let name = if model.is_empty() { provider_name.get() } else { model };
                                    if name.is_empty() { "Model · none".into() } else { format!("Model · {name}") }
                                }}</button>
                                // #1563: the agent chip sits beside the model
                                // chip — agent and model are the two session
                                // identity controls. Hidden when the daemon
                                // reported no selectable agents. It opens
                                // Switch agent, which only accepts listed
                                // agents; the label follows agent.selected.
                                <Show when=move || !agent_list.get().is_empty()>
                                    <button type="button" class="composer-glance agent-glance" data-agent-status="true" data-agent-current=move || agent_name.get() title="Change the agent this session runs" aria-label=move || crate::agent_picker::chip_aria(&agent_name.get()) on:click=move |_| {
                                        mode_menu.set(false);
                                        model_menu.set(false);
                                        autonomy_menu.set(false);
                                        plus_open.set(false);
                                        ac_open.set(false);
                                        ac_force.set(String::new());
                                        shell::show_palette_at("switch-agent");
                                    }>{move || crate::agent_picker::chip_label(&agent_name.get())}</button>
                                </Show>
                                <span class=move || {
                                    if approvals::is_dangerous(&perm_mode.get()) {
                                        "composer-glance approvals-mode-chip danger"
                                    } else {
                                        "composer-glance approvals-mode-chip"
                                    }
                                } data-approval-status="true">{move || format!("Approval · {}", mode_label(&perm_mode.get()))}</span>
                                <button type="button" class=move || {
                                    // Non-default autonomy is safety-relevant
                                    // state (#1572): a session not on the
                                    // supervised default must read that way at
                                    // a glance, phone included.
                                    if crate::model::autonomy_or_default(&autonomy.get()) != "supervised" {
                                        "composer-glance approvals-mode-chip danger"
                                    } else {
                                        "composer-glance approvals-mode-chip"
                                    }
                                } data-autonomy-status="true" title="Autonomy — who clears a workflow phase exit. Opens the autonomy picker." on:click=move |_| {
                                    mode_menu.set(false);
                                    model_menu.set(false);
                                    autonomy_menu.update(|v| *v = !*v);
                                }>{move || format!("Autonomy · {}", crate::model::autonomy_label(&autonomy.get()))}</button>
                                // #1924: phones show one safety chip in place of
                                // the Approval and Autonomy chips so the row
                                // fits one 44px line at 360px. Non-default
                                // values stay named and flagged; tapping it
                                // opens "+" where both pickers live.
                                <button type="button" class=move || {
                                    if approvals::safety_summary(&perm_mode.get(), &autonomy.get()).danger {
                                        "composer-glance approvals-mode-chip safety-glance danger"
                                    } else {
                                        "composer-glance approvals-mode-chip safety-glance"
                                    }
                                } data-safety-status="true" title="Approval mode and autonomy. Opens + to change either." aria-label=move || approvals::safety_summary(&perm_mode.get(), &autonomy.get()).aria on:click=move |_| {
                                    mode_menu.set(false);
                                    model_menu.set(false);
                                    autonomy_menu.set(false);
                                    ac_open.set(false);
                                    ac_force.set(String::new());
                                    plus_open.update(|v| *v = !*v);
                                }>{move || approvals::safety_summary(&perm_mode.get(), &autonomy.get()).label}</button>
                                <crate::fallback::FallbackChip base=base token=token active_root=active_root status=status busy=Signal::derive(move || session_busy_now.get()) />
                                <crate::verbosity::VerbosityChip base=base token=token active_root=active_root status=status busy=Signal::derive(move || session_busy_now.get()) />
                                // #1574: the live workflow run, only while the
                                // daemon reports one. Stop asks first.
                                <Show when=move || wf_run.with(|w| w.is_active())>
                                    <span class="composer-glance workflow-glance" data-workflow-status="true" title="Active workflow and phase, as reported by the daemon. Start another from the palette: Start workflow…">{move || wf_run.with(|w| w.label())}</span>
                                    <button type="button" class="composer-glance workflow-stop" data-workflow-stop="true" title="Stop the active workflow (asks first)" aria-label=move || format!("Stop workflow {}", wf_run.with(|w| w.workflow.clone())) on:click=move |_| {
                                        if let Err(e) = request_workflow_stop() {
                                            model_note.set(e);
                                        }
                                    }>"Stop"</button>
                                </Show>
                                <Show when=move || fast_on.get()>
                                    <span class="composer-glance" data-fast-status="true" title="Fast (priority service tier) is on for this session, as confirmed by the daemon. The current provider may not support priority tier.">"Fast · on"</span>
                                </Show>
                                </div>
                                <div class="steer-actions">
                                    <Show when=move || session_busy_now.get()>
                                        <button type="button" id="stop-turn" class=move || if confirm_stop.get() { "secondary confirming" } else { "secondary" } title="Stops the running turn. Click again to confirm. Escape still stops immediately." on:click=move |_| {
                                            if confirm_stop.get_untracked() {
                                                confirm_stop.set(false);
                                                stop_turn();
                                            } else {
                                                confirm_cancel.set(String::new());
                                                confirm_stop.set(true);
                                            }
                                        }>{move || if confirm_stop.get() { "Confirm" } else { "Stop" }}</button>
                                    </Show>
                                    <button id="send-primary" class="primary send" type="submit" disabled=move || {
                                        let ctx = active_context(&catalog.get(), &provider_name.get(), &model_name.get());
                                        context_view(context_used.get(), context_limit.get(), ctx, &context_fit.get()).held
                                    }>{move || steer_queue::primary_label(session_busy_now.get(), &enter_mode.get())}</button>
                                </div>
                            </div>
                        </form>
                        <annotate::AnnotateOverlay target=annotate_target store=annotate_marks />
                        <Show when=move || !model_note.get().is_empty() && !model_menu.get()>
                            <p class="hint" data-model-note="true">{move || humanize(&model_note.get())}</p>
                        </Show>
                        <Show when=move || !agent_note.get().is_empty()>
                            <p class="hint" data-agent-note="true">{move || agent_note.get()}</p>
                        </Show>
                        <p class="hint composer-statusline" data-composer-triggers="status">{
                            let memory_mode = memory_mode;
                            let memory_mode_known = memory_mode_known;
                            move || {
                            let note = transcript.with(|t| t.trigger_note.clone());
                            let pending = transcript.with(|t| t.asks.len());
                            let custom = trigger_status(&note, pending);
                            let mode = memory_mode.get();
                            let known = memory_mode_known.get();
                            let memory = if known && (mode == "normal" || mode.is_empty()) {
                                String::new()
                            } else {
                                memory_chip_text(known, &mode)
                            };
                            let ctx = active_context(&catalog.get(), &provider_name.get(), &model_name.get());
                            let meter = {
                                let view = context_view(context_used.get(), context_limit.get(), ctx, &context_fit.get());
                                if view.limit <= 0 { String::new() } else { view.summary }
                            };
                            view! {
                                <span>{if custom.is_empty() { memory } else { custom }}</span>
                                // Context doctor (#1537): the meter opens the breakdown.
                                <button type="button" class="context-meter context-doctor-open" data-context-doctor="open"
                                    title="Where is my context going? Opens the context doctor."
                                    on:click=move |_| { let _ = shell::dispatch("context-doctor", ""); }
                                >{if meter.is_empty() { "Context".to_string() } else { meter }}</button>
                            }
                        }}</p>
                    </div>
                </main>
                <aside class="side">
                    <div class="side-tabs" role="tablist" aria-label="Side panel">
                        <button class=move || if side_tab.get() == "activity" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("activity".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("activity")></span>"Activity"</button>
                        <button class=move || if side_tab.get() == "files" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("files".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("files")></span>"Files"</button>
                        <button class=move || if side_tab.get() == "changes" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("changes".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("changes")></span>"Changes"</button>
                        <button class=move || if side_tab.get() == "git" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("git".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("git")></span>"Git"</button>
                        <button class=move || if side_tab.get() == "review" { "chip on" } else { "chip" } data-side-tab="review" on:click=move |_| side_tab.set("review".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("changes")></span>"Review"</button>
                        <button class=move || if side_tab.get() == "terminal" { "chip on" } else { "chip" } on:click=move |_| side_tab.set("terminal".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("terminal")></span>"Terminal"</button>
                        <button class=move || if side_tab.get() == "preview" { "chip on" } else { "chip" } data-side-tab="preview" on:click=move |_| side_tab.set("preview".into())><span class="icon-slot" aria-hidden="true" inner_html=icon("preview")></span>"Preview"</button>
                    </div>
                    <Show when=move || side_tab.get() == "activity">
                    <div class="activity">
                    <header class="activity-head">
                        <span class="hint">"Live tasks"</span>
                        <span class="row-actions">
                            <button type="button" class="secondary side-chat-open-activity" on:click=move |_| open_side(String::new(), String::new())>"Side chat"</button>
                            <Show when=move || team_control_capable.get() && team_live.get()>
                                <button type="button" class="secondary" data-team-message-all="true" on:click=move |_| message_target.set(Some(team_message::Target::All))>"Message all"</button>
                            </Show>
                            <button class="secondary" disabled=move || running_count() == 0 on:click=move |_| stop_all()>"Stop all"</button>
                        </span>
                    </header>
                        <div class="stats">
                        <StatCard label="Running".into()>{move || running_count().to_string()}</StatCard>
                        <StatCard label="Blocked".into()>{move || blocked_count().to_string()}</StatCard>
                        <StatCard label="Tasks".into()>{move || activity.get().len().to_string()}</StatCard>
                    </div>
                    <SessionStatusCard activity=activity roots=roots active_root=active_root children=children tick=tick />
                    <Show when=move || !team_note.get().is_empty()>
                        <p class="hint" data-team-note="true">{move || team_note.get()}</p>
                    </Show>
                    <Show when=move || !children_note.get().is_empty()>
                        <p class="hint" data-children-note="true">{move || children_note.get()}</p>
                    </Show>
                    <TeamMessageComposer base=base token=token root=active_root activity=activity target=message_target />
                    <DelegationBoard rows=delegation_rows />
                    <Show when=move || activity.get().is_empty()>
                        {move || {
                            let failed = !children_note.get().is_empty();
                            let title = if failed {
                                "Delegation history unavailable".to_string()
                            } else {
                                "No live tasks".to_string()
                            };
                            let body = if failed {
                                "Saved children did not load, so this panel is not a complete delegation history.".to_string()
                            } else {
                                "Delegated work for this session shows its status here.".to_string()
                            };
                            view! { <EmptyState title=title body=body /> }
                        }}
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
                                let row_card = row.clone();
                                let expanded = row.expanded;
                                let running = is_running(&state);
                                let retry = can_retry(&state);
                                let name = row.name.clone();
                                let name_chat = name.clone();
                                let sid_chat = sid.clone();
                                let sid_msg = sid.clone();
                                let name_msg = name.clone();
                                let escalation = team_escalations.with(|m| m.get(&sid).cloned());
                                let who = name.clone();
                                let disc_key = identity::child_key(&name, &sid);
                                let disc_name = name.clone();
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
                                                <IdentityDisc ident=disc_key name=disc_name />
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
                                            <Show when=move || team_control_capable.get() && team_live.get()>
                                                <button type="button" class="secondary" data-team-message-open=sid_msg.clone() on:click={
                                                    let sid_msg = sid_msg.clone();
                                                    let name_msg = name_msg.clone();
                                                    move |ev: web_sys::MouseEvent| {
                                                        ev.stop_propagation();
                                                        message_target.set(Some(team_message::Target::Child {
                                                            session_id: sid_msg.clone(),
                                                            name: name_msg.clone(),
                                                        }));
                                                    }
                                                }>"Message"</button>
                                            </Show>
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
                                        {escalation.map(|line| view! {
                                            <p class="child-escalation" data-child-escalation="true">{line}</p>
                                        })}
                                        <Show when=move || expanded>
                                            <ChildStatusCard row=row_card.clone() />
                                        </Show>
                                    </li>
                                }
                            }).collect_view()
                        }}
                    </ul>
                    <TeamMessagesFeed messages=team_messages names=team_names lead_id=team_lead />
                    </div>
                    </Show>
                    <Show when=move || side_tab.get() == "files">
                        <div class="files">
                            <div class="files-head">
                                <p class="hint">{move || if files_path.get().is_empty() { "Working directory".into() } else { files_path.get() }}</p>
                                <button class="secondary" on:click=move |_| load_files()>"Refresh"</button>
                            </div>
                            <search_in_files::SearchInFiles
                                base=base
                                token=token
                                query=sif_query
                                run=sif_run
                                on_open=Callback::new(move |(path, line): (String, u32)| {
                                    doc_diff.set(false);
                                    doc_line.set(line);
                                    doc_path.set(path);
                                })
                            />
                            <p class="hint">"Click a file to open it. @ mentions it in chat; drag also inserts @path."</p>
                            <Show when=move || file_history.with(|h| !h.recent(1).is_empty())>
                                <div class="files-recent" data-ev-recent=true>
                                    <p class="hint">"Recent"</p>
                                    <ul class="plain">
                                        {move || file_history.with(|h| h.recent(file_history::RECENT_SHOWN)).into_iter().map(|p| {
                                            let label = p.clone();
                                            let attr = p.clone();
                                            let title = p.clone();
                                            view! {
                                                <li class="ev-fileline">
                                                    <button class="link file-row" data-ev-recent-open=attr title=title
                                                        on:click=move |_| open_doc(&p)
                                                    >{label}</button>
                                                </li>
                                            }
                                        }).collect_view()}
                                    </ul>
                                </div>
                            </Show>
                            <ErrorNotice message=files_error on_retry=UnsyncCallback::new(move |_: ()| load_files()) />
                            <Show when=move || !files_path.get().is_empty()>
                                <button class="secondary" on:click=move |_| files_path.set(parent_path(&files_path.get()))>"Up"</button>
                            </Show>
                            <ul class="plain">
                                {move || {
                                    let pending = files_loading.get() || files_for.get() != files_path.get();
                                    if pending {
                                        return view! { <li class="hint">"Loading…"</li> }.into_any();
                                    }
                                    files.get().into_iter().map(|entry| {
                                    let name = entry.name.clone();
                                    let path = join_path(&files_path.get(), &entry.name);
                                    let path_drag = path.clone();
                                    let path_click = path.clone();
                                    let mention_attr = path.clone();
                                    let mention_click = path.clone();
                                    let mention_label = format!("Mention {path} in chat");
                                    let is_dir = entry.is_dir;
                                    let label = if is_dir { format!("{name}/") } else { name.clone() };
                                    let open_attr = if is_dir { String::new() } else { path.clone() };
                                    view! {
                                        <li class="ev-fileline">
                                            <button class="link file-row" draggable="true" data-ev-open=open_attr
                                                on:dragstart=move |ev: web_sys::DragEvent| set_drag(&ev, &path_drag)
                                                on:click=move |_| {
                                                    if is_dir {
                                                        files_path.set(path_click.clone());
                                                    } else {
                                                        open_doc(&path_click);
                                                    }
                                                }
                                            >{label}</button>
                                            {(!is_dir).then(move || view! {
                                                <button type="button" class="secondary ev-mention" data-ev-mention=mention_attr.clone()
                                                    aria-label=mention_label.clone() title="Mention in chat"
                                                    on:click=move |_| insert_mention(mention_click.clone())
                                                >"@"</button>
                                            })}
                                        </li>
                                    }
                                }).collect_view().into_any()
                                }}
                            </ul>
                        </div>
                    </Show>
                    <Show when=move || side_tab.get() == "changes">
                        <div class="changes">
                            <div class="changes-head">
                                <p class="hint">"Changed files"</p>
                                <button class="secondary" on:click=move |_| load_changes()>"Refresh"</button>
                            </div>
                            <p class="hint">"Click a change to open its diff. @ mentions it in chat; drag also inserts @path."</p>
                            <ErrorNotice message=changes_error on_retry=UnsyncCallback::new(move |_: ()| load_changes()) />
                            <Show when=move || changes.get().is_empty() && changes_error.get().is_empty()>
                                <EmptyState title="No changes".into() body="Git diffs for the daemon working directory show up here.".into() />
                            </Show>
                            <ul class="plain">
                                {move || changes.get().into_iter().map(|file| {
                                    let path = file.path.clone();
                                    let path_drag = path.clone();
                                    let path_click = path.clone();
                                    let mention_attr = path.clone();
                                    let mention_click = path.clone();
                                    let mention_label = format!("Mention {path} in chat");
                                    let added = file.added;
                                    let deleted = file.deleted;
                                    view! {
                                        <li class="ev-fileline">
                                            <button class="link file-row" draggable="true" data-ev-open=path.clone()
                                                on:dragstart=move |ev: web_sys::DragEvent| set_drag(&ev, &path_drag)
                                                on:click=move |_| {
                                                    doc_line.set(0);
                                                    doc_diff.set(true);
                                                    doc_path.set(path_click.clone());
                                                }
                                            >
                                                <span class="name">{path.clone()}</span>
                                                <span class="diff-add">{format!("+{added}")}</span>
                                                <span class="diff-del">{format!("-{deleted}")}</span>
                                            </button>
                                            <button type="button" class="secondary ev-mention" data-ev-mention=mention_attr.clone()
                                                aria-label=mention_label.clone() title="Mention in chat"
                                                on:click=move |_| insert_mention(mention_click.clone())
                                            >"@"</button>
                                        </li>
                                    }
                                }).collect_view()}
                            </ul>
                        </div>
                    </Show>
                    <div hidden=move || !git_active.get()>
                        <ship_pr::ShipPr base=base token=token status_line=status active=git_active />
                        <review_branch::ReviewBranch base=base token=token active=git_active doc_path=doc_path doc_line=doc_line doc_diff=doc_diff />
                        <pr_watch::PrWatch base=base token=token session=active_root active=git_active />
                        <git::GitPanel base=base token=token status_line=status active=git_active />
                    </div>
                    <div hidden=move || !review_active.get()>
                        <review_queue::ReviewPanel base=base token=token root=active_root busy=session_busy_now active=review_active />
                    </div>
                    <div hidden=move || !worktree_active.get()>
                        <git::WorktreePanel base=base token=token status_line=status active_root=active_root active=worktree_active />
                    </div>
                    <Show when=move || {
                        let tab = side_tab.get();
                        tab == "memory"
                            || tab == "knowledge"
                            || tab == "artifacts"
                            || (tab == "decisions" && ledger_capable.get())
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
                    <Show when=move || side_tab.get() == "diagnostics">
                        <diagnostics::DiagnosticsPane
                            state=diag_state
                            on_refresh=Callback::new(move |_| diag_rev.update(|n| *n = n.wrapping_add(1)))
                            on_open=Callback::new(move |(path, line): (String, u32)| {
                                doc_diff.set(false);
                                doc_line.set(line);
                                doc_path.set(path);
                            })
                        />
                    </Show>
                    <Show when=move || side_tab.get() == "issues" && issues_capable.get()>
                        <local_issues::LocalIssuesPanel
                            base=base
                            token=token
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
                    <Show when=move || side_tab.get() == "preview">
                        <preview::PreviewPanel base=base token=token />
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
                on_promote=Callback::new(move |(text,): (String,)| {
                    draft.update(|d| *d = side_chat_promote::insert_into_draft(d, &text));
                })
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
        <context_doctor::ContextDoctorDialog
            state=doctor
            limit=Signal::derive(move || {
                let ctx = active_context(&catalog.get(), &provider_name.get(), &model_name.get());
                context_view(context_used.get(), context_limit.get(), ctx, &context_fit.get()).limit
            })
            on_refresh=Callback::new(move |_| {
                doctor.request(base.get_untracked(), token.get_untracked(), active_root.get_untracked())
            })
        />
        <lessons::LessonDraftDialog
            draft=lesson_draft
            base=base
            token=token
            status=status
            storage_rev=storage_rev
        />
    }
}

#[component]
fn DaemonPane(gateway_detail: RwSignal<String>, status: RwSignal<String>) -> impl IntoView {
    view! {
        <main class="daemon">
            <section class="card">
                <h2 class="panel-title"><span class="icon-slot" aria-hidden="true" inner_html=icon("status")></span>"Daemon"</h2>
                // Connection state and version ("Connected · v…" /
                // "Unreachable"): this is their on-screen home now that the
                // header prints neither (#1590 A6/A7); the brand mark only
                // carries the same text as a tooltip.
                <p class="hint" data-daemon-link="true">{move || status.get()}</p>
                <p class="hint gw-detail">{move || {
                    let detail = gateway_detail.get();
                    if detail.is_empty() {
                        "Gateway actions are in the command palette.".into()
                    } else {
                        detail
                    }
                }}</p>
            </section>
        </main>
    }
}

#[cfg(test)]
mod composer_grow_history_tests {
    use super::*;

    #[test]
    fn height_clamps_to_existing_bounds() {
        assert_eq!(composer_height_px(0), COMPOSER_MIN_HEIGHT_PX);
        assert_eq!(composer_height_px(43), COMPOSER_MIN_HEIGHT_PX);
        assert_eq!(composer_height_px(44), 44);
        assert_eq!(composer_height_px(100), 100);
        assert_eq!(composer_height_px(160), 160);
        assert_eq!(composer_height_px(900), COMPOSER_MAX_HEIGHT_PX);
    }

    #[test]
    fn caret_rules_follow_multiline_motion() {
        // Single line: neither arrow can move the caret within the draft.
        assert!(!caret_can_move_up("hello", 5));
        assert!(!caret_can_move_down("hello", 0));
        // Caret on the second line: Up moves, Down does not.
        assert!(caret_can_move_up("a\nb", 3));
        assert!(!caret_can_move_down("a\nb", 3));
        // Caret on the first line of two: Down moves, Up does not.
        assert!(!caret_can_move_up("a\nb", 1));
        assert!(caret_can_move_down("a\nb", 1));
        // Caret in the middle line of three: both move.
        assert!(caret_can_move_up("a\nb\nc", 3));
        assert!(caret_can_move_down("a\nb\nc", 3));
    }

    #[test]
    fn history_cycles_and_restores_the_stash() {
        let mut h = PromptHistory::default();
        assert_eq!(h.prev("wip"), None);
        h.push("first prompt");
        h.push("second prompt");
        assert_eq!(h.prev("wip draft").as_deref(), Some("second prompt"));
        assert_eq!(h.prev("second prompt").as_deref(), Some("first prompt"));
        // Oldest entry sticks instead of wrapping.
        assert_eq!(h.prev("first prompt").as_deref(), Some("first prompt"));
        assert_eq!(h.next("first prompt").as_deref(), Some("second prompt"));
        // Down past the newest entry restores the stashed draft.
        assert_eq!(h.next("second prompt").as_deref(), Some("wip draft"));
        // And cycling is over: a further Down has nothing to do.
        assert_eq!(h.next("wip draft"), None);
    }

    #[test]
    fn history_dedupes_consecutive_and_caps() {
        let mut h = PromptHistory::default();
        h.push("  same  ");
        h.push("same");
        h.push("");
        h.push("   ");
        assert_eq!(h.entries, vec!["same".to_string()]);
        h.push("other");
        h.push("same");
        assert_eq!(h.entries.len(), 3, "non-consecutive repeats are kept");
        for i in 0..(PROMPT_HISTORY_CAP + 5) {
            h.push(&format!("prompt {i}"));
        }
        assert_eq!(h.entries.len(), PROMPT_HISTORY_CAP);
        assert_eq!(h.entries.first().map(String::as_str), Some("prompt 5"));
    }

    #[test]
    fn history_edit_since_recall_is_not_clobbered() {
        let mut h = PromptHistory::default();
        h.push("one");
        h.push("two");
        assert_eq!(h.prev("").as_deref(), Some("two"));
        // User edited the recalled prompt, then presses Down: the edit
        // stays and cycling is abandoned.
        assert_eq!(h.next("two, edited"), None);
        // Pressing Up with the edited draft stashes the edit and recalls
        // the newest entry again.
        assert_eq!(h.prev("two, edited").as_deref(), Some("two"));
        assert_eq!(h.next("two").as_deref(), Some("two, edited"));
    }

    #[test]
    fn push_resets_navigation() {
        let mut h = PromptHistory::default();
        h.push("one");
        assert_eq!(h.prev("wip").as_deref(), Some("one"));
        h.push("two");
        assert_eq!(h.cursor, None);
        assert_eq!(h.prev("new wip").as_deref(), Some("two"));
    }
}

#[wasm_bindgen::prelude::wasm_bindgen]
extern "C" {
    #[wasm_bindgen(js_namespace = console, js_name = error)]
    fn console_error(s: &str);
}

fn main() {
    // A wasm panic otherwise surfaces only as "RuntimeError: unreachable";
    // log the message and location so the console (and the smoke's
    // console dump) says what broke.
    std::panic::set_hook(Box::new(|info| console_error(&format!("strike-webui panic: {info}"))));
    // A share link (#1565) gets only the read-only transcript shell: no
    // cockpit, no session list, no gateway credential.
    if let Some(token) = share::share_token_from_location() {
        leptos::mount::mount_to_body(move || view! { <ShareView token=token /> });
    } else {
        leptos::mount::mount_to_body(App);
    }
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

#[cfg(test)]
mod memory_mode_tests {
    use super::*;

    #[test]
    fn chip_asserts_only_confirmed_modes() {
        assert_eq!(memory_chip_text(true, "incognito"), "Memory · Incognito");
        assert_eq!(memory_chip_text(true, "temporary"), "Memory · Temporary");
        assert_eq!(memory_chip_text(true, "normal"), "Memory · Normal");
        assert_eq!(memory_chip_text(false, "normal"), "Memory · Unknown");
        assert_eq!(memory_chip_text(false, "incognito"), "Memory · Unknown");
    }

    #[test]
    fn revert_restores_the_confirmed_mode_or_unknown() {
        assert_eq!(
            revert_memory_mode(Some("incognito".to_string())),
            (true, "incognito".to_string())
        );
        assert_eq!(revert_memory_mode(None), (false, String::new()));
    }
}

#[cfg(test)]
mod team_signal_maps_tests {
    use super::*;

    #[test]
    fn maps_names_and_escalations_by_session() {
        let team: TeamSnapshot = serde_json::from_str(
            r#"{"available":true,"members":{
                "k1":{"sessionId":"c1","name":"scout","escalation":{"kind":"loop","reason":"same call x5","action":"interrupted"}},
                "c2":{"name":""},
                "c3":{"sessionId":"c3","name":"fixer"}
            }}"#,
        )
        .unwrap();
        let (names, escalations) = team_signal_maps(&team);
        assert_eq!(names.get("c1").map(String::as_str), Some("scout"));
        assert_eq!(names.get("c3").map(String::as_str), Some("fixer"));
        assert!(!names.contains_key("c2"));
        assert_eq!(escalations.len(), 1);
        assert_eq!(
            escalations.get("c1").map(String::as_str),
            Some("Loop detected: same call x5. Action: stopped.")
        );
    }
}
