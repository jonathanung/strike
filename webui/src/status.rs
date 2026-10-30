//! Stable status component. Other web UI streams import this module;
//! do not fork a second status pill, gutter, or id list.
//!
//! Contract (also `docs/webui-status.md` and `schemas/session-status.json`):
//!
//! | Id | Label | Tone | Meaning |
//! |---|---|---|---|
//! | `idle` | Idle | `--muted` (chip uses `--success`) | Ready for input, or a quiet gateway |
//! | `running` | Running | `--accent-alt` | Turn, tool, or thinking |
//! | `waiting` | Waiting | `--warning` | Approval, question, or stall |
//! | `blocked` | Blocked | `--warning` | Handoff blocked |
//! | `failed` | Failed | `--signal` | Turn or child failed |
//! | `done` | Done | `--success` | Finished child or task |
//! | `stopped` | Stopped | `--muted` | Canceled |
//! | `disconnected` | Disconnected | `--signal` | Gateway heartbeat missed |
//!
//! Priority for a live root: waiting, running, blocked, failed, stopped, idle.
//! A root whose turn ended cleanly is `idle`, not `done`. `disconnected` is
//! client-only. Aliases (`thinking`, `completed`, `stalled`, …) normalize
//! through [`StatusKind::parse`]. Unknown strings are not a status;
//! [`StatusKind::parse_known`] returns `None` so tool badges stay tool badges.
//!
//! [`StatusMark`] is the only chrome. `place` selects gutter, dot, badge, chip,
//! or row (`StatusSlot`). The prop is not named `slot`: Leptos reserves that
//! word. [`StatusTabs`] is the open-session strip. Both set `data-status`
//! to the canonical id and `data-unread` to `true` or `false`.

use std::cell::RefCell;
use std::rc::Rc;

use leptos::callback::Callback;
use leptos::prelude::*;

use crate::model::RootSummary;

/// Missed-heartbeat window. Matches `protocol.HeartbeatStaleMs` and the schema.
pub const HEARTBEAT_STALE_MS: f64 = 6000.0;
/// Server heartbeat period. Matches `protocol.HeartbeatIntervalMs`.
/// The shell uses [`HEARTBEAT_STALE_MS`] for the disconnect check.
#[allow(dead_code)]
pub const HEARTBEAT_INTERVAL_MS: f64 = 2000.0;

/// Canonical status. Copy so views can capture it in event closures.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum StatusKind {
    Idle,
    Running,
    Waiting,
    Blocked,
    Failed,
    Done,
    Stopped,
    Disconnected,
}

impl Default for StatusKind {
    fn default() -> Self {
        StatusKind::Idle
    }
}

impl StatusKind {
    pub fn id(self) -> &'static str {
        match self {
            StatusKind::Idle => "idle",
            StatusKind::Running => "running",
            StatusKind::Waiting => "waiting",
            StatusKind::Blocked => "blocked",
            StatusKind::Failed => "failed",
            StatusKind::Done => "done",
            StatusKind::Stopped => "stopped",
            StatusKind::Disconnected => "disconnected",
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            StatusKind::Idle => "Idle",
            StatusKind::Running => "Running",
            StatusKind::Waiting => "Waiting",
            StatusKind::Blocked => "Blocked",
            StatusKind::Failed => "Failed",
            StatusKind::Done => "Done",
            StatusKind::Stopped => "Stopped",
            StatusKind::Disconnected => "Disconnected",
        }
    }

    /// CSS variable for session surfaces. The chip paints idle with success.
    pub fn tone(self) -> &'static str {
        match self {
            StatusKind::Running => "--accent-alt",
            StatusKind::Waiting | StatusKind::Blocked => "--warning",
            StatusKind::Failed | StatusKind::Disconnected => "--signal",
            StatusKind::Done => "--success",
            StatusKind::Idle | StatusKind::Stopped => "--muted",
        }
    }

    pub fn parse(raw: &str) -> Self {
        Self::parse_known(raw).unwrap_or(StatusKind::Idle)
    }

    pub fn parse_known(raw: &str) -> Option<Self> {
        let key = raw.trim().to_ascii_lowercase();
        let kind = match key.as_str() {
            "idle" | "ready" | "saved" => StatusKind::Idle,
            "running" | "thinking" | "working" | "in_progress" | "in-progress" | "busy"
            | "starting" | "active" => StatusKind::Running,
            "waiting" | "waiting-for-approval" | "approval" | "permission" | "question"
            | "stalled" => StatusKind::Waiting,
            "blocked" => StatusKind::Blocked,
            "failed" | "error" => StatusKind::Failed,
            "done" | "completed" | "complete" => StatusKind::Done,
            "stopped" | "canceled" | "cancelled" => StatusKind::Stopped,
            "disconnected" | "unreachable" => StatusKind::Disconnected,
            _ => return None,
        };
        Some(kind)
    }
}

/// Where [`StatusMark`] draws. Navbar, session, and notification streams pick one.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum StatusSlot {
    /// 3px bar plus an unread pip. Session list.
    Gutter,
    /// 8px dot. Tabs and activity.
    Dot,
    /// Pill with the label.
    Badge,
    /// Header chip. Idle uses the success tone (gateway is up).
    Chip,
    /// Badge plus elapsed and last-activity. Other streams use this; the
    /// session list draws the same clocks in its own row.
    #[allow(dead_code)]
    Row,
}

impl Default for StatusSlot {
    fn default() -> Self {
        StatusSlot::Badge
    }
}

impl StatusSlot {
    pub fn id(self) -> &'static str {
        match self {
            StatusSlot::Gutter => "gutter",
            StatusSlot::Dot => "dot",
            StatusSlot::Badge => "badge",
            StatusSlot::Chip => "chip",
            StatusSlot::Row => "row",
        }
    }
}

/// One open session in [`StatusTabs`].
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StatusTab {
    pub id: String,
    pub label: String,
    pub kind: StatusKind,
    pub unread: bool,
    pub elapsed_s: i64,
    pub active: bool,
}

/// Classify a root when the server sent a status id, with a busy/ask fallback
/// for a daemon that has not been upgraded.
pub fn session_kind(explicit: &str, busy: bool, permission: bool, question: bool) -> StatusKind {
    if let Some(kind) = StatusKind::parse_known(explicit) {
        return kind;
    }
    if permission || question {
        return StatusKind::Waiting;
    }
    if busy {
        return StatusKind::Running;
    }
    StatusKind::Idle
}

/// Seconds in the current running or waiting stretch. `since_ms` and `now_ms`
/// are unix milliseconds. Other kinds return 0.
pub fn elapsed_since(kind: StatusKind, since_ms: i64, now_ms: i64) -> i64 {
    if !matches!(kind, StatusKind::Running | StatusKind::Waiting) || since_ms <= 0 || now_ms <= 0 {
        return 0;
    }
    ((now_ms - since_ms) / 1000).max(0)
}

pub fn fmt_elapsed(secs: i64) -> String {
    let secs = secs.max(0);
    let m = secs / 60;
    let s = secs % 60;
    if m >= 60 {
        format!("{}:{:02}:{:02}", m / 60, m % 60, s)
    } else {
        format!("{m}:{s:02}")
    }
}

pub fn fmt_ago(mtime: i64, now_s: i64) -> String {
    if mtime <= 0 || now_s <= 0 {
        return String::new();
    }
    let delta = (now_s - mtime).max(0);
    if delta < 60 {
        return "now".into();
    }
    if delta < 3600 {
        return format!("{}m", delta / 60);
    }
    if delta < 86_400 {
        return format!("{}h", delta / 3600);
    }
    format!("{}d", delta / 86_400)
}

/// What the active session is waiting on, without inventing a count:
/// the client only knows the pending flags, not how many asks are open.
pub fn waiting_cause(permission: bool, question: bool) -> &'static str {
    match (permission, question) {
        (true, true) => "an approval or a question",
        (true, false) => "an approval",
        (false, true) => "a question from the agent",
        (false, false) => "the pending ask",
    }
}

/// Whether the chip has somewhere to go: only Waiting/Blocked (#1543).
/// Idle, Running, Stopped and the rest stay plain status marks.
pub fn is_actionable(kind: StatusKind) -> bool {
    matches!(kind, StatusKind::Waiting | StatusKind::Blocked)
}

/// The button label/title for an actionable chip, or `None` when the
/// chip must stay inert. Never fabricates a pending count.
pub fn chip_action_label(
    kind: StatusKind,
    permission: bool,
    question: bool,
) -> Option<String> {
    if !is_actionable(kind) {
        return None;
    }
    Some(format!(
        "{}: {} — go to it",
        kind.label(),
        waiting_cause(permission, question)
    ))
}

/// Scroll the open chat to the first pending-ask card (`ApprovalCards`
/// renders each with `data-pending-ask`), focus that card and flash its
/// outline, without stealing the composer: focus lands on the card
/// container (`tabindex="-1"`), whose decision buttons are next in tab
/// order. Returns `false` when no pending-ask card is in the DOM, so
/// the caller can say so plainly instead of scrolling nowhere.
pub fn scroll_to_pending_ask_soon() -> bool {
    let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
        return false;
    };
    let Some(el) = doc
        .query_selector("[data-pending-ask=\"true\"]")
        .ok()
        .flatten()
    else {
        return false;
    };
    use wasm_bindgen::closure::Closure;
    use wasm_bindgen::JsCast;
    let flash = el.clone();
    let cb = Closure::once(move || {
        flash.scroll_into_view();
        if let Some(html) = flash.dyn_ref::<web_sys::HtmlElement>() {
            let _ = html.focus();
            let _ = html.class_list().add_1("ask-flash");
            let unflash = flash.clone();
            let off = Closure::once(move || {
                let _ = unflash.class_list().remove_1("ask-flash");
            });
            if let Some(w) = web_sys::window() {
                let _ = w.set_timeout_with_callback_and_timeout_and_arguments_0(
                    off.as_ref().unchecked_ref(),
                    2000,
                );
            }
            off.forget();
        }
    });
    if let Some(w) = web_sys::window() {
        let _ = w.set_timeout_with_callback_and_timeout_and_arguments_0(
            cb.as_ref().unchecked_ref(),
            0,
        );
    }
    cb.forget();
    true
}

/// Page wiring for the shell slots in `src/shell/contrib/status.rs`.
/// The shell boots before App's signals exist, so App installs them here
/// (same late-bind shape as `shell::HostBridge` and the notifications
/// centre's host) and the slot renders read them.
#[derive(Clone)]
pub struct StatusHost {
    pub gateway_kind: RwSignal<StatusKind>,
    pub gateway_label: RwSignal<String>,
    pub roots: RwSignal<Vec<RootSummary>>,
    pub active_root: RwSignal<String>,
    pub refresh: Rc<dyn Fn()>,
    /// Installed by App (#1543): switch to chat and jump to the pending
    /// ask, or report that it already resolved. The chip itself only
    /// renders a button when `chip_action_label` says it has somewhere
    /// to go, so this is never called for an inert chip.
    pub go_to_ask: Rc<dyn Fn()>,
}

thread_local! {
    static HOST: RefCell<Option<StatusHost>> = const { RefCell::new(None) };
}

pub fn set_host(host: StatusHost) {
    HOST.with(|slot| *slot.borrow_mut() = Some(host));
}

fn host() -> Option<StatusHost> {
    HOST.with(|slot| slot.borrow().clone())
}

/// Slot render for the `gateway` slot: the pre-shell header chip, unchanged.
/// A button so Enter/Space re-checks the daemon (`refresh`).
pub fn gateway_view() -> AnyView {
    let Some(host) = host() else {
        return view! { <span></span> }.into_any();
    };
    let kind = host.gateway_kind;
    let label = host.gateway_label;
    let refresh = host.refresh.clone();
    view! {
        <button
            type="button"
            class="status-chip-hit"
            aria-label=move || format!("Gateway {}", label.get())
            title="Gateway status. Enter checks the daemon."
            on:click=move |_| (*refresh)()
        >
            {move || {
                view! {
                    <StatusMark
                        kind=kind.get()
                        place=StatusSlot::Chip
                        label=label.get()
                    />
                }
            }}
        </button>
    }
    .into_any()
}

/// Slot render for the `session-status` slot: the active live root's
/// [`StatusMark`] chip, classified the same way as its [`StatusTabs`] tab.
/// No live root is `idle`, matching the shell fallback it replaces.
pub fn session_view() -> AnyView {
    let Some(host) = host() else {
        return view! { <span></span> }.into_any();
    };
    let roots = host.roots;
    let active_root = host.active_root;
    // `go_to_ask` is fetched from the host at click time, not captured
    // here: reactive closures must be `Send`, and `Rc<dyn Fn()>` is not
    // (the gateway chip's `refresh` works the same way — its `Rc` lives
    // only in the click handler).
    view! {
        {move || {
            let row = roots
                .get()
                .iter()
                .find(|row| row.id == active_root.get())
                .cloned();
            let kind = row
                .as_ref()
                .map(|row| {
                    session_kind(
                        &row.status,
                        row.busy,
                        row.permission_pending,
                        row.question_pending,
                    )
                })
                .unwrap_or(StatusKind::Idle);
            let action = row.as_ref().and_then(|row| {
                chip_action_label(kind, row.permission_pending, row.question_pending)
            });
            match action {
                Some(label) => {
                    view! {
                        <button
                            type="button"
                            class="status-chip-hit status-chip-action"
                            data-session-status-action="true"
                            aria-label=label.clone()
                            title=label
                            on:click=move |_| {
                                // Fully qualified: this function's own
                                // `let Some(host) = host()` binding shadows
                                // the bare `host()` call (E0618).
                                if let Some(h) = crate::status::host() {
                                    (*h.go_to_ask)()
                                }
                            }
                        >
                            <StatusMark kind=kind place=StatusSlot::Chip />
                        </button>
                    }
                    .into_any()
                }
                None => view! { <StatusMark kind=kind place=StatusSlot::Chip /> }.into_any(),
            }
        }}
    }
    .into_any()
}

pub fn chrome_title(kind: StatusKind, unread: usize) -> String {
    match (unread, kind) {
        (0, StatusKind::Idle) => "Strike".into(),
        (0, other) => format!("Strike · {}", other.label()),
        (n, StatusKind::Idle) => format!("({n}) Strike"),
        (n, other) => format!("({n}) Strike · {}", other.label()),
    }
}

/// Window title and favicon. The favicon color is the computed CSS variable
/// for the kind, so a named theme does not need its own icon.
pub fn apply_document_chrome(kind: StatusKind, unread: usize) {
    let Some(window) = web_sys::window() else {
        return;
    };
    let Some(doc) = window.document() else {
        return;
    };
    doc.set_title(&chrome_title(kind, unread));
    let tone = if kind == StatusKind::Idle {
        "--success"
    } else {
        kind.tone()
    };
    let color = css_var(&window, &doc, tone);
    let svg = format!(
        "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'><circle cx='8' cy='8' r='5' fill='{color}'/></svg>"
    );
    let href = format!("data:image/svg+xml,{}", percent_encode(&svg));
    set_favicon(&doc, &href);
}

fn css_var(window: &web_sys::Window, doc: &web_sys::Document, name: &str) -> String {
    let fallback = match name {
        "--accent-alt" => "#22d3ee",
        "--warning" => "#fbbf24",
        "--signal" => "#fb7185",
        "--success" => "#4ade80",
        _ => "#9b99b0",
    };
    let Some(root) = doc.document_element() else {
        return fallback.into();
    };
    let Ok(Some(style)) = window.get_computed_style(&root) else {
        return fallback.into();
    };
    let value = style.get_property_value(name).unwrap_or_default();
    let value = value.trim();
    if value.is_empty() {
        fallback.into()
    } else {
        value.to_string()
    }
}

fn set_favicon(doc: &web_sys::Document, href: &str) {
    let link = match doc.query_selector("link[data-status-icon='1']").ok().flatten() {
        Some(el) => el,
        None => {
            let Ok(el) = doc.create_element("link") else {
                return;
            };
            let _ = el.set_attribute("rel", "icon");
            let _ = el.set_attribute("data-status-icon", "1");
            if let Some(head) = doc.head() {
                let _ = head.append_child(&el);
            }
            el
        }
    };
    let _ = link.set_attribute("type", "image/svg+xml");
    let _ = link.set_attribute("href", href);
}

fn percent_encode(raw: &str) -> String {
    let mut out = String::new();
    for b in raw.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(b as char)
            }
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

fn mark_class(slot: StatusSlot, unread: bool) -> String {
    let mut class = match slot {
        StatusSlot::Gutter => "status-mark status-gutter".to_string(),
        StatusSlot::Dot => "status-mark status-dot".to_string(),
        StatusSlot::Badge => "status-mark status-badge".to_string(),
        StatusSlot::Chip => "status-mark status-chip".to_string(),
        StatusSlot::Row => "status-mark status-row".to_string(),
    };
    if unread {
        class.push_str(" is-unread");
    }
    class
}

fn display_label(kind: StatusKind, label: &str) -> String {
    if label.trim().is_empty() {
        kind.label().to_string()
    } else {
        label.to_string()
    }
}

/// One status mark. `label` overrides the word (activity uses "Stalled") and
/// does not change `data-status`. `elapsed_s` and `last_activity_s` render on
/// the row slot. `last_activity_s` is unix seconds.
#[component]
pub fn StatusMark(
    kind: StatusKind,
    #[prop(optional)] unread: bool,
    /// Where to draw. Defaults to [`StatusSlot::Badge`]. Named `place` because
    /// Leptos treats `slot` as a component slot, which cannot nest in HTML.
    #[prop(optional)] place: StatusSlot,
    #[prop(optional)] elapsed_s: i64,
    #[prop(optional)] last_activity_s: i64,
    #[prop(optional)] now_s: i64,
    #[prop(optional)] label: String,
) -> impl IntoView {
    let id = kind.id().to_string();
    let flag = if unread { "true" } else { "false" };
    let place_id = place.id().to_string();
    let class = mark_class(place, unread);
    let text = display_label(kind, &label);
    let aria = if unread {
        format!("{text}, unread")
    } else {
        text.clone()
    };
    let clock = if matches!(place, StatusSlot::Row) {
        let mut bits = Vec::new();
        if elapsed_s > 0 && matches!(kind, StatusKind::Running | StatusKind::Waiting) {
            bits.push(fmt_elapsed(elapsed_s));
        }
        let ago = fmt_ago(last_activity_s, now_s);
        if !ago.is_empty() {
            bits.push(ago);
        }
        bits.join(" · ")
    } else {
        String::new()
    };
    let show_clock = !clock.is_empty();
    match place {
        StatusSlot::Gutter => view! {
            <span class=class data-status=id.clone() data-unread=flag data-slot=place_id aria-hidden="true">
                <span class="status-gutter-bar"></span>
                {unread.then(|| view! { <span class="status-unread-pip"></span> })}
            </span>
        }
        .into_any(),
        StatusSlot::Dot => view! {
            <span class=class data-status=id data-unread=flag data-slot=place_id title=aria aria-hidden="true"></span>
        }
        .into_any(),
        StatusSlot::Badge | StatusSlot::Chip => view! {
            <span class=class data-status=id data-unread=flag data-slot=place_id>{text}</span>
        }
        .into_any(),
        StatusSlot::Row => view! {
            <span class=class data-status=id data-unread=flag data-slot=place_id>
                <span class="status-badge">{text}</span>
                {show_clock.then(|| view! { <span class="status-clock">{clock}</span> })}
            </span>
        }
        .into_any(),
    }
}

/// Open-session strip. Empty input renders nothing.
#[component]
pub fn StatusTabs(
    tabs: Vec<StatusTab>,
    /// `move |id: String| { ... }`. Leptos stores it as `Callback<(String,)>`.
    #[prop(into)] on_select: Callback<(String,)>,
) -> impl IntoView {
    if tabs.is_empty() {
        return ().into_any();
    }
    let buttons = tabs
        .into_iter()
        .map(|tab| {
            let id = tab.id.clone();
            let select_id = id.clone();
            let label = tab.label;
            let kind = tab.kind;
            let unread = tab.unread;
            let elapsed = tab.elapsed_s;
            let active = tab.active;
            let on_select = on_select.clone();
            let class = if active {
                "status-tab on"
            } else {
                "status-tab"
            };
            view! {
                <button
                    type="button"
                    class=class
                    role="tab"
                    aria-selected=if active { "true" } else { "false" }
                    data-session=id
                    on:click=move |_| on_select.run((select_id.clone(),))
                >
                    <StatusMark kind=kind place=StatusSlot::Dot unread=unread />
                    <span class="status-tab-label">{label}</span>
                    { (elapsed > 0).then(|| view! { <span class="elapsed">{fmt_elapsed(elapsed)}</span> }) }
                </button>
            }
        })
        .collect_view();
    view! {
        <div class="status-tabs" role="tablist" aria-label="Open sessions">
            {buttons}
        </div>
    }
    .into_any()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_matches_schema_aliases() {
        let raw = include_str!("../../schemas/session-status.json");
        let doc: serde_json::Value = serde_json::from_str(raw).unwrap();
        for id in doc["ids"].as_array().unwrap() {
            let id = id.as_str().unwrap();
            let kind = StatusKind::parse_known(id).unwrap();
            assert_eq!(kind.id(), id);
            assert_eq!(kind.label(), doc["labels"][id].as_str().unwrap());
            assert_eq!(kind.tone(), doc["tones"][id].as_str().unwrap());
        }
        let aliases = doc["aliases"].as_object().unwrap();
        for (alias, want) in aliases {
            assert_eq!(StatusKind::parse(alias).id(), want.as_str().unwrap());
        }
        assert!(StatusKind::parse_known("tool").is_none());
        assert_eq!(doc["heartbeatStaleMs"].as_i64().unwrap(), HEARTBEAT_STALE_MS as i64);
        assert_eq!(
            doc["heartbeatIntervalMs"].as_i64().unwrap(),
            HEARTBEAT_INTERVAL_MS as i64
        );
        assert_eq!(doc["chipIdleTone"].as_str().unwrap(), "--success");
    }

    #[test]
    fn session_kind_prefers_server_status() {
        assert_eq!(
            session_kind("blocked", true, true, false),
            StatusKind::Blocked
        );
        assert_eq!(session_kind("", true, true, false), StatusKind::Waiting);
        assert_eq!(session_kind("", true, false, false), StatusKind::Running);
        assert_eq!(session_kind("nope", false, false, false), StatusKind::Idle);
    }

    #[test]
    fn chip_is_a_button_only_when_waiting_or_blocked() {
        assert!(is_actionable(StatusKind::Waiting));
        assert!(is_actionable(StatusKind::Blocked));
        for kind in [
            StatusKind::Idle,
            StatusKind::Running,
            StatusKind::Stopped,
            StatusKind::Done,
            StatusKind::Failed,
            StatusKind::Disconnected,
        ] {
            assert!(!is_actionable(kind), "{kind:?}");
            assert_eq!(chip_action_label(kind, true, false), None);
        }
        assert_eq!(
            chip_action_label(StatusKind::Waiting, true, false).as_deref(),
            Some("Waiting: an approval — go to it")
        );
        assert_eq!(
            chip_action_label(StatusKind::Waiting, false, true).as_deref(),
            Some("Waiting: a question from the agent — go to it")
        );
        assert_eq!(
            chip_action_label(StatusKind::Blocked, true, true).as_deref(),
            Some("Blocked: an approval or a question — go to it")
        );
        // Blocked by explicit daemon status, no pending flags: the label
        // stays honest and names no invented cause or count.
        assert_eq!(
            chip_action_label(StatusKind::Blocked, false, false).as_deref(),
            Some("Blocked: the pending ask — go to it")
        );
    }

    #[test]
    fn elapsed_only_while_active() {
        assert_eq!(elapsed_since(StatusKind::Running, 1_000, 4_500), 3);
        assert_eq!(elapsed_since(StatusKind::Idle, 1_000, 4_500), 0);
        assert_eq!(elapsed_since(StatusKind::Waiting, 0, 4_500), 0);
    }

    #[test]
    fn title_and_clocks() {
        assert_eq!(chrome_title(StatusKind::Idle, 0), "Strike");
        assert_eq!(chrome_title(StatusKind::Running, 0), "Strike · Running");
        assert_eq!(chrome_title(StatusKind::Idle, 2), "(2) Strike");
        assert_eq!(
            chrome_title(StatusKind::Waiting, 1),
            "(1) Strike · Waiting"
        );
        assert_eq!(fmt_elapsed(65), "1:05");
        assert_eq!(fmt_elapsed(3661), "1:01:01");
        assert_eq!(fmt_ago(100, 160), "1m");
        assert_eq!(fmt_ago(0, 10), "");
    }
}
