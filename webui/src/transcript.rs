//! Session transcript projection. Events become messages. Protocol JSON
//! stays out of the visible text. The shell renders [`Transcript`]; it does
//! not interpret tool args itself.

use std::collections::HashMap;

use serde_json::Value;

use crate::composer_triggers::{display_user_text, PermAsk};
use crate::model::{
    child_card_from_parts, child_card_from_text, child_keys_match, protocol_dump, status_kind,
    strip_echo_tip, ChatLine, ChildCard,
};

const TITLE_MAX: usize = 32;
const STICK_PX: f64 = 48.0;
pub const MINIMAP_MIN: usize = 8;

const ECHO_TIP: &str = "Tip: start a message with `run <command>` to exercise the bash tool and the permission prompt.";
const ECHO_MARK: &str = "Tip: start a message with";

const EXTS: &[&str] = &[
    "rs", "go", "md", "css", "js", "mjs", "ts", "tsx", "json", "toml", "yml", "yaml", "py", "rb",
    "sh", "html", "svg", "txt", "nix",
];

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Role {
    You,
    Strike,
    Tool,
    Child,
    Notice,
    Permission,
}

impl Role {
    pub fn class(self) -> &'static str {
        match self {
            Role::You => "you",
            Role::Strike => "strike",
            Role::Tool => "tool",
            Role::Child => "child",
            Role::Notice => "notice",
            Role::Permission => "permission",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ChipKind {
    File,
    PullRequest,
    Issue,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Chip {
    pub kind: ChipKind,
    pub label: String,
    pub path: String,
    pub line: u32,
    pub href: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DiffStat {
    pub added: u32,
    pub deleted: u32,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Message {
    pub id: u32,
    pub role: Role,
    pub title: String,
    pub body: String,
    pub badge: String,
    pub detail: String,
    pub key: String,
    pub call_id: String,
    pub diff: Option<DiffStat>,
    pub chips: Vec<Chip>,
    /// Keep-count for `POST /v1/sessions/{id}/fork_at` once the turn completes.
    /// Negative until then.
    pub keep_events: i32,
    pub retry_text: String,
    pub copy_text: String,
    /// Envelope time of the event that created this message, epoch ms.
    /// 0 means unknown (old log line without a parseable `time`).
    pub at_ms: f64,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Inline {
    Text(String),
    Code(String),
    Strong(Vec<Inline>),
    Em(Vec<Inline>),
    Link { href: String, label: String },
    Chip(Chip),
}

#[derive(Clone, Debug, PartialEq)]
pub enum Block {
    Heading(u8, Vec<Inline>),
    Paragraph(Vec<Inline>),
    Code {
        lang: String,
        text: String,
    },
    List {
        ordered: bool,
        items: Vec<Vec<Inline>>,
    },
    Quote(Vec<Inline>),
}

#[derive(Clone, Debug)]
pub struct Suggestion {
    pub title: &'static str,
    pub detail: &'static str,
    pub prompt: &'static str,
}

const SUGGESTIONS: [Suggestion; 6] = [
    Suggestion {
        title: "See what is here",
        detail: "Asks for a summary of this directory and says not to edit.",
        prompt: "Summarize the working directory. Do not change files.",
    },
    Suggestion {
        title: "How approval works",
        detail: "Asks how the five composer modes differ. Nothing is edited.",
        prompt: "Explain how the approval modes differ. Do not change files.",
    },
    Suggestion {
        title: "Plan first",
        detail: "Asks for a short plan and waits before any edit.",
        prompt: "Draft a short plan before changing files.",
    },
    Suggestion {
        title: "Review this session",
        detail: "Asks what this transcript has already done.",
        prompt: "Review what this session has done so far.",
    },
    Suggestion {
        title: "What you can do",
        detail: "Asks which tools this session can actually run.",
        prompt: "What can you do in this session?",
    },
    Suggestion {
        title: "What to look at next",
        detail: "Asks for a next step from the files already here.",
        prompt: "What should I look at next in this workspace?",
    },
];

pub fn suggestions() -> &'static [Suggestion] {
    &SUGGESTIONS
}

pub fn empty_headline(tick: u32) -> &'static str {
    match (tick / 4) % 3 {
        0 => "This session is empty.",
        1 => "Send a message, or start from a card.",
        _ => "Each card says what it will ask.",
    }
}

/// Same rules as `session.TitleFromText`: collapse whitespace, drop controls,
/// cut at 32 runes. No ellipsis.
pub fn title_from_text(text: &str) -> String {
    let mut out = String::new();
    let mut prev_space = false;
    for r in text.chars() {
        if r.is_whitespace() {
            if out.is_empty() || prev_space {
                continue;
            }
            out.push(' ');
            prev_space = true;
            continue;
        }
        if r < '\u{20}' || r == '\u{7f}' {
            continue;
        }
        out.push(r);
        prev_space = false;
    }
    let s = out.trim();
    if s.chars().count() <= TITLE_MAX {
        return s.to_string();
    }
    s.chars().take(TITLE_MAX).collect()
}

pub fn stick_to_bottom(scroll_top: f64, scroll_height: f64, client_height: f64) -> bool {
    scroll_height - scroll_top - client_height < STICK_PX
}

/// Scroll offset to use after the scroller's height changes — the phone
/// keyboard opening or closing resizes the shell around the transcript,
/// and a desktop window resize does the same. `prev_*` is the geometry
/// at the last position the reader chose, `new_client` the height after
/// the resize, and `scroll_height` the current content height.
///
/// A reader pinned to the bottom stays at the bottom. A reader scrolled
/// up keeps their offset while it still fits; when growth clamps it away
/// (keyboard closing over a short remaining tail), the offset moves only
/// enough to keep the same distance from the bottom, so the lines on
/// screen stay on screen and the resize never yanks the reader.
pub fn anchored_scroll_top(
    prev_top: f64,
    prev_height: f64,
    prev_client: f64,
    new_client: f64,
    scroll_height: f64,
) -> f64 {
    let distance = prev_height - prev_top - prev_client;
    if distance < STICK_PX {
        return scroll_height.max(0.0);
    }
    let max_top = (scroll_height - new_client).max(0.0);
    if prev_top <= max_top {
        prev_top
    } else {
        (scroll_height - distance - new_client).max(0.0)
    }
}

pub fn show_minimap(count: usize) -> bool {
    count >= MINIMAP_MIN
}

/// Drop the echo-provider tip, including a tip that is still streaming.
pub fn strip_echo_progress(text: &str) -> String {
    let Some(i) = text.find(ECHO_MARK) else {
        return text.to_string();
    };
    let rest = &text[i..];
    if ECHO_TIP.starts_with(rest) || rest.starts_with(ECHO_TIP) {
        let mut out = text[..i].trim_end().to_string();
        if let Some(after) = rest.strip_prefix(ECHO_TIP) {
            let after = after.trim_start();
            if !after.is_empty() {
                if !out.is_empty() {
                    out.push_str("\n\n");
                }
                out.push_str(after);
            }
        }
        return out;
    }
    strip_echo_tip(text)
}

#[derive(Clone, Debug)]
pub struct Transcript {
    pub messages: Vec<Message>,
    pub title: String,
    pub auto_title: String,
    pub title_pin: Option<String>,
    next_id: u32,
    /// Count of `artifact.updated` envelopes seen; the shell mirrors this
    /// into its storage revision signal so the storage panel refreshes.
    pub artifact_updates: u32,
    /// Open permission asks from `permission.asked` envelopes, in arrival
    /// order. The composer ask bar renders these and replies through the
    /// daemon's normal `permission.reply` op.
    pub asks: Vec<PermAsk>,
    /// Latest `engine.error` notice with code `composer_notice` (a skipped
    /// `@file` or similar). Cleared by the next `user.message`.
    pub trigger_note: String,
    assistant_open: bool,
    event_count: usize,
    turn_start: Option<u32>,
    /// Turn elapsed labels (#1364), keyed by the id of the turn's final
    /// message. Filled once per turn by `stamp_turn` at `turn.completed`;
    /// turns under a minute, still in progress, or containing an untimed
    /// line have no entry and render nothing.
    pub turn_elapsed: HashMap<u32, String>,
}

impl Default for Transcript {
    fn default() -> Self {
        Self::new()
    }
}

impl Transcript {
    pub fn new() -> Self {
        Self {
            messages: Vec::new(),
            title: String::new(),
            auto_title: String::new(),
            title_pin: None,
            next_id: 0,
            artifact_updates: 0,
            asks: Vec::new(),
            trigger_note: String::new(),
            assistant_open: false,
            event_count: 0,
            turn_start: None,
            turn_elapsed: HashMap::new(),
        }
    }

    /// The turn elapsed label stamped on this message, if it is the
    /// final line of a completed turn that exceeded a minute (#1364).
    pub fn elapsed_for(&self, id: u32) -> Option<&str> {
        self.turn_elapsed.get(&id).map(String::as_str)
    }

    pub fn set_listed_title(&mut self, listed: &str) {
        let listed = listed.trim();
        if listed.is_empty() {
            return;
        }
        if let Some(pin) = &self.title_pin {
            if listed == pin {
                self.title_pin = None;
            }
            return;
        }
        if self.title.is_empty() || self.title == self.auto_title || listed != self.auto_title {
            // Durable meta wins when it differs from the auto title, and
            // fills an empty title. A later session.titled equal to the
            // previous auto title must not wipe a rename.
            if self.title.is_empty() || listed != self.title {
                if self.auto_title.is_empty()
                    || listed != self.auto_title
                    || self.title == self.auto_title
                {
                    self.title = listed.to_string();
                }
            }
        }
    }

    pub fn pin_title(&mut self, title: String) {
        self.title = title.clone();
        self.title_pin = Some(title);
    }

    pub fn regenerated_title(&self) -> String {
        self.messages
            .iter()
            .find(|m| m.role == Role::You && !m.retry_text.trim().is_empty())
            .map(|m| title_from_text(&m.retry_text))
            .unwrap_or_default()
    }

    /// Every message as a shell `ChatLine` (the side chat and the activity
    /// panel still consume that shape). Role strings match the shell's.
    pub fn chat_lines(&self) -> Vec<ChatLine> {
        self.messages
            .iter()
            .map(|m| ChatLine {
                id: m.id,
                role: m.role.class().into(),
                title: m.title.clone(),
                body: m.body.clone(),
                badge: m.badge.clone(),
                detail: m.detail.clone(),
                key: m.key.clone(),
                at_ms: m.at_ms,
            })
            .collect()
    }

    pub fn to_markdown(&self) -> String {
        let mut out = String::new();
        let title = if self.title.trim().is_empty() {
            "Untitled session"
        } else {
            self.title.trim()
        };
        out.push_str("# ");
        out.push_str(title);
        out.push_str("\n\n");
        for m in &self.messages {
            out.push_str("## ");
            out.push_str(if m.title.is_empty() {
                "Message"
            } else {
                &m.title
            });
            out.push_str("\n\n");
            if !m.body.is_empty() {
                out.push_str(m.body.trim());
                out.push_str("\n\n");
            }
            if let Some(d) = &m.diff {
                out.push_str(&format!("+{}/-{}\n\n", d.added, d.deleted));
            }
            if !m.detail.is_empty() && !protocol_leak(&m.detail) {
                out.push_str("```\n");
                out.push_str(&m.detail);
                if !m.detail.ends_with('\n') {
                    out.push('\n');
                }
                out.push_str("```\n\n");
            }
        }
        out
    }

    pub fn apply_envelope(&mut self, raw: &str) {
        // Generated contract gate (shared with the shell): unknown events
        // are skipped, and a known event whose payload does not match the
        // schema is dropped. Parsing below still reads the raw object.
        match protocol_schema::decode_event(raw) {
            Ok(protocol_schema::Event::Unknown { .. }) | Err(_) => return,
            Ok(_) => {}
        }
        let v: Value = match serde_json::from_str(raw) {
            Ok(v) => v,
            Err(_) => return,
        };
        let typ = v.get("type").and_then(|t| t.as_str()).unwrap_or("");
        if typ.is_empty() || typ == "session.header" {
            return;
        }
        // The envelope's `time` is the event's original time (also what the
        // JSONL log persists), so replayed messages keep real timestamps.
        let at_ms = v
            .get("time")
            .and_then(|t| t.as_str())
            .map(crate::model::rfc3339_to_ms)
            .unwrap_or(0.0);
        self.event_count += 1;
        let data = v.get("data").cloned().unwrap_or(Value::Null);
        match typ {
            "user.message" => {
                self.assistant_open = false;
                self.trigger_note.clear();
                let text = field_str(&data, "text");
                let shown = display_user_text(&strip_echo_progress(&text));
                let id = self.push(Message {
                    id: 0,
                    role: Role::You,
                    title: "You".into(),
                    body: shown.clone(),
                    badge: String::new(),
                    detail: String::new(),
                    key: String::new(),
                    call_id: String::new(),
                    diff: None,
                    chips: chips_in(&shown),
                    keep_events: -1,
                    retry_text: text,
                    copy_text: shown,
                    at_ms,
                });
                self.turn_start = Some(id);
                self.absorb();
            }
            "session.titled" => self.note_titled(&field_str(&data, "title")),
            "turn.started" => self.assistant_open = false,
            "reasoning.delta" => {}
            "text.delta" => {
                let chunk = field_str(&data, "text");
                if chunk.is_empty() {
                    return;
                }
                if self.assistant_open {
                    if let Some(last) = self.messages.last_mut() {
                        if last.role == Role::Strike {
                            last.body.push_str(&chunk);
                            last.body = strip_echo_progress(&last.body);
                            last.copy_text = last.body.clone();
                            last.chips = chips_in(&last.body);
                            return;
                        }
                    }
                }
                self.assistant_open = true;
                let body = strip_echo_progress(&chunk);
                let retry = self.current_retry();
                self.push(Message {
                    id: 0,
                    role: Role::Strike,
                    title: "Strike".into(),
                    body: body.clone(),
                    badge: String::new(),
                    detail: String::new(),
                    key: String::new(),
                    call_id: String::new(),
                    diff: None,
                    chips: chips_in(&body),
                    keep_events: -1,
                    retry_text: retry,
                    copy_text: body,
                    at_ms,
                });
            }
            "turn.completed" => {
                self.assistant_open = false;
                self.absorb();
                self.stamp_turn(at_ms);
            }
            "tool.begin" => {
                self.assistant_open = false;
                let name = field_str(&data, "name");
                let args = args_value(&data);
                let call_id = field_str(&data, "callId");
                let (title, subtitle) = tool_heading(&name, &args);
                let mut chips = path_chips(&args);
                chips.extend(chips_in(&subtitle));
                dedup_chips(&mut chips);
                let diff = diff_from_args(&name, &args);
                let copy = if subtitle.is_empty() {
                    title.clone()
                } else {
                    format!("{title}\n{subtitle}")
                };
                self.push(Message {
                    id: 0,
                    role: Role::Tool,
                    title,
                    body: subtitle,
                    badge: "running".into(),
                    detail: String::new(),
                    key: name,
                    call_id,
                    diff,
                    chips,
                    keep_events: -1,
                    retry_text: String::new(),
                    copy_text: copy,
                    at_ms,
                });
            }
            "tool.end" => {
                let call_id = field_str(&data, "callId");
                let name = field_str(&data, "name");
                let output = field_str(&data, "output");
                let engine_title = field_str(&data, "title");
                let metadata = data.get("metadata").cloned().unwrap_or(Value::Null);
                let err = data
                    .get("isError")
                    .and_then(|v| v.as_bool())
                    .unwrap_or(false);
                let idx = self.find_tool(&call_id, &name);
                if let Some(idx) = idx {
                    let key = if self.messages[idx].key.is_empty() {
                        name.clone()
                    } else {
                        self.messages[idx].key.clone()
                    };
                    let subtitle = self.messages[idx].body.clone();
                    let face = finish_tool(&key, &subtitle, &output, &metadata, &engine_title);
                    let msg = &mut self.messages[idx];
                    msg.badge = if err { "failed" } else { "done" }.into();
                    if !face.body.is_empty() {
                        msg.body = face.body;
                    }
                    msg.detail = face.detail;
                    if face.diff.is_some() {
                        msg.diff = face.diff;
                    }
                    for chip in face.chips {
                        push_chip(&mut msg.chips, chip);
                    }
                    msg.copy_text = face.copy;
                    if msg.title.is_empty() {
                        msg.title = tool_heading(&key, &Value::Null).0;
                    }
                } else {
                    let (title, subtitle) = tool_heading(&name, &Value::Null);
                    let face = finish_tool(&name, &subtitle, &output, &metadata, &engine_title);
                    self.push(Message {
                        id: 0,
                        role: Role::Tool,
                        title,
                        body: face.body,
                        badge: if err { "failed" } else { "done" }.into(),
                        detail: face.detail,
                        key: name,
                        call_id,
                        diff: face.diff,
                        chips: face.chips,
                        keep_events: -1,
                        retry_text: String::new(),
                        copy_text: face.copy,
                        at_ms,
                    });
                }
            }
            "child.completed" => {
                let status = field_str(&data, "status");
                let name = field_str(&data, "name");
                let session = field_str(&data, "sessionId");
                let mut summary = field_str(&data, "summary");
                if summary.is_empty() {
                    summary = data
                        .get("handoff")
                        .and_then(|h| h.get("summary"))
                        .and_then(|s| s.as_str())
                        .unwrap_or("")
                        .to_string();
                }
                let summary = clip(&summary, 160);
                let mut card = child_card_from_parts(
                    &name,
                    &session,
                    &status,
                    &summary,
                    &human_child_detail(&data),
                );
                card.at_ms = at_ms;
                self.upsert_child(card);
            }
            "artifact.updated" => {
                self.artifact_updates = self.artifact_updates.wrapping_add(1)
            }
            "engine.error" => {
                let message = field_str(&data, "message");
                if message.is_empty() {
                    return;
                }
                let code = field_str(&data, "code");
                if code == "composer_notice" {
                    self.trigger_note = message.clone();
                }
                self.push(Message {
                    id: 0,
                    role: Role::Notice,
                    title: "Notice".into(),
                    body: message,
                    badge: code,
                    detail: String::new(),
                    key: String::new(),
                    call_id: String::new(),
                    diff: None,
                    chips: Vec::new(),
                    keep_events: -1,
                    retry_text: String::new(),
                    copy_text: String::new(),
                    at_ms,
                });
            }
            "permission.asked" => {
                let request_id = field_str(&data, "requestId");
                if request_id.is_empty() {
                    return;
                }
                let permission = field_str(&data, "permission");
                let patterns = pattern_list(&data);
                if !self.asks.iter().any(|ask| ask.request_id == request_id) {
                    self.asks.push(PermAsk {
                        request_id: request_id.clone(),
                        permission: permission.clone(),
                        patterns: patterns.clone(),
                    });
                }
                let body = if patterns.is_empty() {
                    permission.clone()
                } else {
                    format!("{permission} · {patterns}")
                };
                self.push(Message {
                    id: 0,
                    role: Role::Permission,
                    title: "Approval".into(),
                    body,
                    badge: "ask".into(),
                    detail: String::new(),
                    key: request_id,
                    call_id: String::new(),
                    diff: None,
                    chips: Vec::new(),
                    keep_events: -1,
                    retry_text: String::new(),
                    copy_text: permission,
                    at_ms,
                });
            }
            "permission.resolved" => {
                let request_id = field_str(&data, "requestId");
                let decision = field_str(&data, "decision");
                self.asks.retain(|ask| ask.request_id != request_id);
                if let Some(msg) = self
                    .messages
                    .iter_mut()
                    .rev()
                    .find(|m| m.key == request_id && m.role == Role::Permission)
                {
                    msg.badge = if decision.is_empty() {
                        "done".into()
                    } else {
                        decision
                    };
                }
            }
            _ => {}
        }
    }

    fn note_titled(&mut self, title: &str) {
        let title = title.trim();
        if title.is_empty() || self.title_pin.is_some() {
            return;
        }
        if self.title.is_empty() || self.title == self.auto_title {
            self.title = title.to_string();
        }
        self.auto_title = title.to_string();
    }

    fn current_retry(&self) -> String {
        self.messages
            .iter()
            .rev()
            .find(|m| m.role == Role::You)
            .map(|m| m.retry_text.clone())
            .unwrap_or_default()
    }

    fn push(&mut self, mut message: Message) -> u32 {
        self.next_id += 1;
        message.id = self.next_id;
        let id = message.id;
        self.messages.push(message);
        id
    }

    fn find_tool(&self, call_id: &str, name: &str) -> Option<usize> {
        if !call_id.is_empty() {
            if let Some(i) = self
                .messages
                .iter()
                .rposition(|m| m.role == Role::Tool && m.call_id == call_id)
            {
                return Some(i);
            }
        }
        if !name.is_empty() {
            if let Some(i) = self
                .messages
                .iter()
                .rposition(|m| m.role == Role::Tool && m.badge == "running" && m.key == name)
            {
                return Some(i);
            }
        }
        self.messages
            .iter()
            .rposition(|m| m.role == Role::Tool && m.badge == "running")
    }

    fn stamp_turn(&mut self, completed_ms: f64) {
        let Some(id) = self.turn_start else {
            return;
        };
        let Some(start) = self.messages.iter().position(|m| m.id == id) else {
            self.turn_start = None;
            return;
        };
        let keep = self.event_count as i32;
        let retry = self.messages[start].retry_text.clone();
        for msg in self.messages.iter_mut().skip(start) {
            if msg.keep_events < 0 {
                msg.keep_events = keep;
            }
            if msg.role == Role::Strike && msg.retry_text.is_empty() {
                msg.retry_text = retry.clone();
            }
        }
        // Turn elapsed (#1364): span from the turn's first line to the
        // `turn.completed` envelope time, labelled on the turn's final
        // line only. Any untimed line in the turn suppresses the label
        // (see turn_elapsed.rs). Computed here, once — a turn still in
        // progress never reaches this point, so nothing ticks live.
        if let (Some(first), Some(last)) = (
            self.messages.get(start),
            self.messages.last(),
        ) {
            let (first_ms, last_id) = (first.at_ms, last.id);
            let all_timed = self
                .messages
                .iter()
                .skip(start)
                .all(|m| m.at_ms > 0.0);
            if let Some(label) =
                crate::turn_elapsed::turn_elapsed_label(first_ms, completed_ms, all_timed)
            {
                self.turn_elapsed.insert(last_id, label);
            }
        }
        self.turn_start = None;
    }

    fn absorb(&mut self) {
        let start_id = self.turn_start;
        let mut cards = Vec::new();
        for line in self.messages.iter_mut() {
            if line.role != Role::You && line.role != Role::Strike {
                continue;
            }
            line.body = strip_echo_progress(&line.body);
            line.copy_text = line.body.clone();
            line.chips = chips_in(&line.body);
            if let Some(mut card) = child_card_from_text(&line.body) {
                card.at_ms = line.at_ms;
                cards.push(card);
                line.body.clear();
                line.copy_text.clear();
            } else if protocol_dump(&line.body).is_some() {
                let summary = safe_dump_summary(&line.body);
                cards.push(ChildCard {
                    key: format!("dump-{}", line.id),
                    name: "Details".into(),
                    status: String::new(),
                    summary,
                    details: String::new(),
                    at_ms: line.at_ms,
                });
                line.body.clear();
                line.copy_text.clear();
            }
        }
        self.messages.retain(|line| match line.role {
            Role::Tool | Role::Child => true,
            _ => !line.body.trim().is_empty(),
        });
        for card in cards {
            self.upsert_child(card);
        }
        if let Some(id) = start_id {
            if self.messages.iter().any(|m| m.id == id) {
                self.turn_start = Some(id);
            } else {
                self.turn_start = None;
            }
        }
    }

    fn upsert_child(&mut self, card: ChildCard) {
        if card.key.is_empty() {
            return;
        }
        let kind = status_kind(&card.status);
        if let Some(existing) = self
            .messages
            .iter_mut()
            .rev()
            .find(|line| line.role == Role::Child && child_keys_match(&line.key, &card.key))
        {
            if !card.name.is_empty() {
                existing.title = card.name;
            }
            if !card.summary.is_empty() {
                existing.body = card.summary.clone();
                existing.copy_text = card.summary;
            }
            if !card.details.is_empty() {
                existing.detail = card.details;
            }
            if !kind.is_empty() {
                existing.badge = kind;
            }
            existing.chips = chips_in(&existing.body);
            return;
        }
        let title = if card.name.is_empty() {
            "Child".into()
        } else {
            card.name
        };
        let at_ms = card.at_ms;
        let summary = card.summary.clone();
        self.push(Message {
            id: 0,
            role: Role::Child,
            title,
            body: summary.clone(),
            badge: kind,
            detail: card.details,
            key: card.key,
            call_id: String::new(),
            diff: None,
            chips: chips_in(&summary),
            keep_events: -1,
            retry_text: String::new(),
            copy_text: summary,
            at_ms,
        });
    }
}

struct ToolFace {
    body: String,
    detail: String,
    diff: Option<DiffStat>,
    chips: Vec<Chip>,
    copy: String,
}

fn pattern_list(data: &Value) -> String {
    data.get("patterns")
        .and_then(|v| v.as_array())
        .map(|items| {
            items
                .iter()
                .filter_map(|v| v.as_str())
                .collect::<Vec<_>>()
                .join(", ")
        })
        .unwrap_or_default()
}

fn finish_tool(
    name: &str,
    subtitle: &str,
    output: &str,
    metadata: &Value,
    engine_title: &str,
) -> ToolFace {
    if name == "git" || looks_like_git(output) {
        if let Some(face) = git_face(output) {
            return face;
        }
    }
    if let Some((diff, path)) = edit_meta(metadata) {
        let body = if plain_sentence(output) {
            clip(output, 160)
        } else if !path.is_empty() {
            format!("Edited {path}")
        } else if !subtitle.is_empty() {
            subtitle.to_string()
        } else {
            "Edited file".into()
        };
        let mut chips = Vec::new();
        if !path.is_empty() {
            push_chip(&mut chips, file_chip(&path, 0));
        }
        return ToolFace {
            copy: body.clone(),
            body,
            detail: String::new(),
            diff: Some(diff),
            chips,
        };
    }
    if let Some(diff) = diff_stat_from_text(output) {
        let body = if !subtitle.is_empty() {
            subtitle.to_string()
        } else if plain_sentence(engine_title) {
            engine_title.to_string()
        } else {
            "Diff".into()
        };
        return ToolFace {
            copy: output.to_string(),
            body,
            detail: output.to_string(),
            diff: Some(diff),
            chips: chips_in(output),
        };
    }
    if protocol_leak(output) {
        let body = if !subtitle.is_empty() {
            subtitle.to_string()
        } else {
            "Structured result".into()
        };
        return ToolFace {
            copy: body.clone(),
            body,
            detail: String::new(),
            diff: None,
            chips: Vec::new(),
        };
    }
    let body = if !subtitle.is_empty() {
        subtitle.to_string()
    } else if plain_sentence(engine_title) {
        clip(engine_title, 160)
    } else if plain_sentence(output) && output.lines().count() == 1 {
        clip(output, 160)
    } else if name == "read" {
        "Read file".into()
    } else {
        String::new()
    };
    let detail = if output.trim().is_empty() || output.trim() == body.trim() {
        String::new()
    } else {
        output.to_string()
    };
    let copy = if detail.is_empty() {
        body.clone()
    } else {
        format!("{body}\n{detail}")
    };
    ToolFace {
        body,
        detail,
        diff: None,
        chips: chips_in(&format!("{subtitle}\n{output}")),
        copy,
    }
}

fn plain_sentence(text: &str) -> bool {
    let t = text.trim();
    !t.is_empty() && !t.starts_with('{') && !t.starts_with('[') && t.chars().count() < 220
}

fn looks_like_git(output: &str) -> bool {
    let t = output.trim();
    if !t.starts_with('{') {
        return false;
    }
    let Ok(v) = serde_json::from_str::<Value>(t) else {
        return false;
    };
    v.get("action").and_then(|a| a.as_str()).is_some()
        && (v.get("files").is_some() || v.get("commits").is_some() || v.get("ok").is_some())
}

fn git_face(output: &str) -> Option<ToolFace> {
    let v: Value = serde_json::from_str(output.trim()).ok()?;
    let action = v.get("action").and_then(|a| a.as_str()).unwrap_or("git");
    let mut chips = Vec::new();
    let mut added = 0u32;
    let mut deleted = 0u32;
    let mut files = 0usize;
    if let Some(list) = v.get("files").and_then(|f| f.as_array()) {
        files = list.len();
        for f in list {
            let path = f.get("path").and_then(|p| p.as_str()).unwrap_or("");
            if !path.is_empty() {
                push_chip(&mut chips, file_chip(path, 0));
            }
            if let Some(hunks) = f.get("hunks").and_then(|h| h.as_array()) {
                for h in hunks {
                    if let Some(lines) = h.get("lines").and_then(|l| l.as_array()) {
                        for line in lines {
                            let s = line.as_str().unwrap_or("");
                            if s.starts_with('+') && !s.starts_with("+++") {
                                added += 1;
                            } else if s.starts_with('-') && !s.starts_with("---") {
                                deleted += 1;
                            }
                        }
                    }
                }
            }
        }
    }
    let body = match action {
        "log" => {
            let subject = v
                .get("commits")
                .and_then(|c| c.as_array())
                .and_then(|c| c.first())
                .and_then(|c| c.get("subject"))
                .and_then(|s| s.as_str())
                .unwrap_or("");
            if subject.is_empty() {
                let n = v
                    .get("commits")
                    .and_then(|c| c.as_array())
                    .map(|c| c.len())
                    .unwrap_or(0);
                format!("Git log · {n} commits")
            } else {
                format!("Git log · {subject}")
            }
        }
        "status" => format!("Git status · {files} files"),
        "diff" => format!("Git diff · {files} files"),
        other => format!("Git {other}"),
    };
    let diff = if added + deleted > 0 {
        Some(DiffStat { added, deleted })
    } else {
        None
    };
    Some(ToolFace {
        copy: body.clone(),
        body,
        detail: String::new(),
        diff,
        chips,
    })
}

fn edit_meta(metadata: &Value) -> Option<(DiffStat, String)> {
    let old = metadata.get("oldString").and_then(|v| v.as_str())?;
    let new = metadata.get("newString").and_then(|v| v.as_str())?;
    let diff = diff_stat_from_replace(old, new)?;
    let path = String::new();
    Some((diff, path))
}

fn human_child_detail(data: &Value) -> String {
    let mut lines = Vec::new();
    if let Some(items) = data
        .get("handoff")
        .and_then(|h| h.get("blockers"))
        .and_then(|b| b.as_array())
    {
        let text: Vec<&str> = items
            .iter()
            .filter_map(|v| v.as_str())
            .filter(|s| !s.is_empty())
            .collect();
        if !text.is_empty() {
            lines.push(format!("Blockers: {}", text.join(", ")));
        }
    }
    if let Some(summary) = data
        .get("verification")
        .and_then(|v| v.get("summary"))
        .and_then(|s| s.as_str())
    {
        let summary = summary.trim();
        if !summary.is_empty() {
            lines.push(format!("Verification: {summary}"));
        }
    }
    lines.join("\n")
}

fn safe_dump_summary(raw: &str) -> String {
    let t = raw.trim();
    if let Ok(v) = serde_json::from_str::<Value>(t) {
        if let Some(s) = v.get("summary").and_then(|s| s.as_str()) {
            let s = s.trim();
            if !s.is_empty() {
                return clip(s, 140);
            }
        }
    }
    if let Some(card) = child_card_from_text(t) {
        if !card.summary.is_empty() && !protocol_leak(&card.summary) {
            return card.summary;
        }
    }
    "Structured result".into()
}

pub fn protocol_leak(text: &str) -> bool {
    let t = text.trim();
    if t.starts_with('{') || t.starts_with('[') {
        return true;
    }
    text.contains("\"sessionId\"")
        || text.contains("\"callId\"")
        || text.contains("\"handoff\"")
        || text.contains("\"schemaVersion\"")
        || text.contains("\"oldString\"")
}

fn tool_heading(name: &str, args: &Value) -> (String, String) {
    let path = first_str(args, &["filePath", "path", "file", "target_file", "target"]);
    let command = first_str(args, &["command", "cmd"]);
    let pattern = first_str(args, &["pattern", "query", "glob", "regex"]);
    let url = first_str(args, &["url", "href"]);
    match name {
        "read" => ("Read file".into(), path),
        "grep" => (
            "Search".into(),
            if pattern.is_empty() { path } else { pattern },
        ),
        "glob" => ("Find files".into(), pattern),
        "edit" => ("Edit file".into(), path),
        "write" => ("Write file".into(), path),
        "apply_patch" => ("Apply patch".into(), path),
        "bash" => ("Run command".into(), clip(&command, 96)),
        "git" => {
            let action = first_str(args, &["action"]);
            let title = if action.is_empty() {
                "Git".into()
            } else {
                format!("Git {action}")
            };
            (title, path)
        }
        "webfetch" => ("Fetch page".into(), url),
        "websearch" => ("Search the web".into(), pattern),
        "delete" => ("Delete file".into(), path),
        "move" => ("Move file".into(), path),
        "todowrite" | "todo_write" => ("Update todos".into(), String::new()),
        "todoread" | "todo_read" => ("Read todos".into(), String::new()),
        "" => ("Tool".into(), String::new()),
        other => {
            let extra = if !path.is_empty() {
                path
            } else if !command.is_empty() {
                clip(&command, 96)
            } else {
                pattern
            };
            (humanize(other), extra)
        }
    }
}

fn humanize(name: &str) -> String {
    let mut out = String::new();
    for (i, word) in name.split(['_', '-']).filter(|w| !w.is_empty()).enumerate() {
        if i > 0 {
            out.push(' ');
        }
        let mut chars = word.chars();
        if let Some(first) = chars.next() {
            out.extend(first.to_uppercase());
            out.extend(chars);
        }
    }
    if out.is_empty() {
        "Tool".into()
    } else {
        out
    }
}

fn diff_from_args(name: &str, args: &Value) -> Option<DiffStat> {
    if name == "edit" {
        let old = first_str(args, &["oldString"]);
        let new = first_str(args, &["newString"]);
        if !old.is_empty() || !new.is_empty() {
            return diff_stat_from_replace(&old, &new);
        }
    }
    if name == "apply_patch" {
        let patch = first_str(args, &["patch"]);
        return diff_stat_from_text(&patch);
    }
    None
}

pub fn diff_stat_from_text(text: &str) -> Option<DiffStat> {
    let mut added = 0u32;
    let mut deleted = 0u32;
    let mut hunks = 0u32;
    for line in text.lines() {
        if line.starts_with("@@") {
            hunks += 1;
            continue;
        }
        if line.starts_with("+++") || line.starts_with("---") || line.starts_with("***") {
            continue;
        }
        if line.starts_with('+') {
            added += 1;
        } else if line.starts_with('-') {
            deleted += 1;
        }
    }
    if added + deleted == 0 {
        return None;
    }
    if hunks == 0 && added + deleted < 2 {
        return None;
    }
    Some(DiffStat { added, deleted })
}

pub fn diff_stat_from_replace(old: &str, new: &str) -> Option<DiffStat> {
    if old == new {
        return None;
    }
    let added = new
        .lines()
        .filter(|l| !old.lines().any(|o| o == *l))
        .count() as u32;
    let deleted = old
        .lines()
        .filter(|l| !new.lines().any(|n| n == *l))
        .count() as u32;
    if added + deleted == 0 {
        return Some(DiffStat {
            added: new.lines().count().max(1) as u32,
            deleted: old.lines().count().max(1) as u32,
        });
    }
    Some(DiffStat { added, deleted })
}

fn path_chips(args: &Value) -> Vec<Chip> {
    let mut chips = Vec::new();
    let path = first_str(args, &["filePath", "path", "file", "target_file"]);
    if !path.is_empty() {
        push_chip(&mut chips, file_chip(&path, 0));
    }
    let patch = first_str(args, &["patch"]);
    for line in patch.lines() {
        let trimmed = line.trim();
        for prefix in [
            "*** Update File:",
            "*** Add File:",
            "*** Delete File:",
            "*** Move to:",
        ] {
            if let Some(rest) = trimmed.strip_prefix(prefix) {
                let p = rest.trim();
                if !p.is_empty() {
                    push_chip(&mut chips, file_chip(p, 0));
                }
            }
        }
    }
    chips
}

pub fn file_chip(path: &str, line: u32) -> Chip {
    let label = if line > 0 {
        format!("{path}:{line}")
    } else {
        path.to_string()
    };
    Chip {
        kind: ChipKind::File,
        label,
        path: path.to_string(),
        line,
        href: String::new(),
    }
}

fn push_chip(chips: &mut Vec<Chip>, chip: Chip) {
    if chips
        .iter()
        .any(|c| c.kind == chip.kind && c.label == chip.label && c.href == chip.href)
    {
        return;
    }
    chips.push(chip);
}

fn dedup_chips(chips: &mut Vec<Chip>) {
    let mut out = Vec::new();
    for chip in chips.drain(..) {
        push_chip(&mut out, chip);
    }
    *chips = out;
}

pub fn chips_in(text: &str) -> Vec<Chip> {
    let mut out = Vec::new();
    collect_chips(&parse_inlines(text), &mut out);
    out
}

fn collect_chips(inlines: &[Inline], out: &mut Vec<Chip>) {
    for inline in inlines {
        match inline {
            Inline::Chip(chip) => push_chip(out, chip.clone()),
            Inline::Strong(inner) | Inline::Em(inner) => collect_chips(inner, out),
            _ => {}
        }
    }
}

pub fn parse_markdown(src: &str) -> Vec<Block> {
    let lines: Vec<&str> = src.split('\n').collect();
    let mut i = 0;
    let mut blocks = Vec::new();
    while i < lines.len() {
        let line = lines[i];
        if line.trim().is_empty() {
            i += 1;
            continue;
        }
        let trimmed = line.trim();
        if let Some(rest) = trimmed.strip_prefix("```") {
            let lang = rest.trim().to_string();
            i += 1;
            let mut buf = Vec::new();
            while i < lines.len() && !lines[i].trim_start().starts_with("```") {
                buf.push(lines[i]);
                i += 1;
            }
            if i < lines.len() {
                i += 1;
            }
            blocks.push(Block::Code {
                lang,
                text: buf.join("\n"),
            });
            continue;
        }
        if let Some(level) = heading_level(trimmed) {
            let text = trimmed[level..].trim();
            blocks.push(Block::Heading(level as u8, parse_inlines(text)));
            i += 1;
            continue;
        }
        if trimmed.starts_with("> ") || trimmed == ">" {
            let mut buf = Vec::new();
            while i < lines.len() {
                let t = lines[i].trim();
                if let Some(rest) = t.strip_prefix('>') {
                    buf.push(rest.trim_start());
                    i += 1;
                } else {
                    break;
                }
            }
            blocks.push(Block::Quote(parse_inlines(&buf.join(" "))));
            continue;
        }
        if list_item(trimmed).is_some() {
            let ordered = trimmed
                .as_bytes()
                .first()
                .is_some_and(|c| c.is_ascii_digit());
            let mut items = Vec::new();
            while i < lines.len() {
                if let Some(item) = list_item(lines[i].trim()) {
                    items.push(parse_inlines(item));
                    i += 1;
                } else {
                    break;
                }
            }
            blocks.push(Block::List { ordered, items });
            continue;
        }
        let mut para = vec![trimmed];
        i += 1;
        while i < lines.len() {
            let t = lines[i].trim();
            if t.is_empty()
                || t.starts_with("```")
                || heading_level(t).is_some()
                || t.starts_with('>')
                || list_item(t).is_some()
            {
                break;
            }
            para.push(t);
            i += 1;
        }
        blocks.push(Block::Paragraph(parse_inlines(&para.join(" "))));
    }
    blocks
}

fn heading_level(line: &str) -> Option<usize> {
    let n = line.chars().take_while(|c| *c == '#').count();
    if (1..=3).contains(&n) && line.chars().nth(n) == Some(' ') {
        Some(n)
    } else {
        None
    }
}

fn list_item(line: &str) -> Option<&str> {
    if let Some(rest) = line.strip_prefix("- ") {
        return Some(rest);
    }
    if let Some(rest) = line.strip_prefix("* ") {
        return Some(rest);
    }
    let bytes = line.as_bytes();
    let mut i = 0;
    while i < bytes.len() && bytes[i].is_ascii_digit() {
        i += 1;
    }
    if i > 0 && i + 1 < bytes.len() && bytes[i] == b'.' && bytes[i + 1] == b' ' {
        return Some(&line[i + 2..]);
    }
    None
}

pub fn parse_inlines(src: &str) -> Vec<Inline> {
    let chars: Vec<char> = src.chars().collect();
    let mut out = Vec::new();
    let mut buf = String::new();
    let mut i = 0;
    while i < chars.len() {
        if chars[i] == '`' {
            if let Some(end) = chars[i + 1..].iter().position(|c| *c == '`') {
                flush_text(&mut buf, &mut out);
                let text: String = chars[i + 1..i + 1 + end].iter().collect();
                if let Some(chip) = chip_from_span(&text) {
                    out.push(Inline::Chip(chip));
                } else {
                    out.push(Inline::Code(text));
                }
                i += end + 2;
                continue;
            }
        }
        if chars[i] == '*' && i + 1 < chars.len() && chars[i + 1] == '*' {
            if let Some(rel) = find_pair(&chars, i + 2, '*') {
                flush_text(&mut buf, &mut out);
                let text: String = chars[i + 2..rel].iter().collect();
                out.push(Inline::Strong(parse_inlines(&text)));
                i = rel + 2;
                continue;
            }
        }
        if chars[i] == '*' {
            if let Some(end) = chars[i + 1..].iter().position(|c| *c == '*') {
                flush_text(&mut buf, &mut out);
                let text: String = chars[i + 1..i + 1 + end].iter().collect();
                out.push(Inline::Em(parse_inlines(&text)));
                i += end + 2;
                continue;
            }
        }
        if chars[i] == '[' {
            if let Some((label, href, next)) = parse_md_link(&chars, i) {
                flush_text(&mut buf, &mut out);
                if let Some(chip) = chip_from_url(&href) {
                    out.push(Inline::Chip(chip));
                } else {
                    out.push(Inline::Link { href, label });
                }
                i = next;
                continue;
            }
        }
        if starts_with_url(&chars, i) {
            flush_text(&mut buf, &mut out);
            let start = i;
            i += 1;
            while i < chars.len() && !chars[i].is_whitespace() && chars[i] != ')' && chars[i] != ']'
            {
                i += 1;
            }
            while i > start && matches!(chars[i - 1], '.' | ',' | ';') {
                i -= 1;
            }
            let href: String = chars[start..i].iter().collect();
            if let Some(chip) = chip_from_url(&href) {
                out.push(Inline::Chip(chip));
            } else {
                out.push(Inline::Link {
                    label: href.clone(),
                    href,
                });
            }
            continue;
        }
        if chars[i] == '#' && (i == 0 || is_boundary(chars[i - 1])) {
            let mut j = i + 1;
            while j < chars.len() && chars[j].is_ascii_digit() {
                j += 1;
            }
            if j > i + 1 {
                flush_text(&mut buf, &mut out);
                let n: String = chars[i + 1..j].iter().collect();
                out.push(Inline::Chip(issue_chip(&n, "")));
                i = j;
                continue;
            }
        }
        if let Some((chip, next)) = parse_repo_issue(&chars, i) {
            flush_text(&mut buf, &mut out);
            out.push(Inline::Chip(chip));
            i = next;
            continue;
        }
        if let Some((chip, next)) = parse_path(&chars, i) {
            flush_text(&mut buf, &mut out);
            out.push(Inline::Chip(chip));
            i = next;
            continue;
        }
        buf.push(chars[i]);
        i += 1;
    }
    flush_text(&mut buf, &mut out);
    out
}

fn flush_text(buf: &mut String, out: &mut Vec<Inline>) {
    if !buf.is_empty() {
        out.push(Inline::Text(std::mem::take(buf)));
    }
}

fn find_pair(chars: &[char], start: usize, mark: char) -> Option<usize> {
    let mut i = start;
    while i + 1 < chars.len() {
        if chars[i] == mark && chars[i + 1] == mark {
            return Some(i);
        }
        i += 1;
    }
    None
}

fn parse_md_link(chars: &[char], i: usize) -> Option<(String, String, usize)> {
    if chars.get(i) != Some(&'[') {
        return None;
    }
    let mut j = i + 1;
    while j < chars.len() && chars[j] != ']' {
        j += 1;
    }
    if j + 1 >= chars.len() || chars[j] != ']' || chars[j + 1] != '(' {
        return None;
    }
    let label: String = chars[i + 1..j].iter().collect();
    let mut k = j + 2;
    while k < chars.len() && chars[k] != ')' {
        k += 1;
    }
    if k >= chars.len() {
        return None;
    }
    let href: String = chars[j + 2..k].iter().collect();
    if !(href.starts_with("https://") || href.starts_with("http://")) {
        return None;
    }
    Some((label, href, k + 1))
}

fn starts_with_url(chars: &[char], i: usize) -> bool {
    let s: String = chars[i..].iter().take(8).collect();
    s.starts_with("https://") || s.starts_with("http://")
}

fn is_boundary(c: char) -> bool {
    c.is_whitespace() || matches!(c, '(' | '[' | '"' | '\'' | '`' | '>' | ':')
}

fn issue_chip(num: &str, href: &str) -> Chip {
    Chip {
        kind: ChipKind::Issue,
        label: format!("Issue #{num}"),
        path: String::new(),
        line: 0,
        href: href.to_string(),
    }
}

fn chip_from_url(url: &str) -> Option<Chip> {
    let rest = url
        .strip_prefix("https://github.com/")
        .or_else(|| url.strip_prefix("http://github.com/"))?;
    let parts: Vec<&str> = rest.split('/').collect();
    if parts.len() < 4 {
        return None;
    }
    let num: String = parts[3]
        .chars()
        .take_while(|c| c.is_ascii_digit())
        .collect();
    if num.is_empty() {
        return None;
    }
    let kind = match parts[2] {
        "pull" | "pulls" => ChipKind::PullRequest,
        "issues" | "issue" => ChipKind::Issue,
        _ => return None,
    };
    let label = match kind {
        ChipKind::PullRequest => format!("PR #{num}"),
        ChipKind::Issue => format!("Issue #{num}"),
        ChipKind::File => format!("#{num}"),
    };
    Some(Chip {
        kind,
        label,
        path: String::new(),
        line: 0,
        href: url.to_string(),
    })
}

fn parse_repo_issue(chars: &[char], i: usize) -> Option<(Chip, usize)> {
    if i > 0 && !is_boundary(chars[i - 1]) {
        return None;
    }
    let rest: String = chars[i..].iter().collect();
    let bytes = rest.as_bytes();
    if bytes.first().is_none_or(|c| !c.is_ascii_alphanumeric()) {
        return None;
    }
    let mut n = 0;
    let mut slash = None;
    while n < bytes.len() {
        let c = bytes[n];
        if c == b'/' && slash.is_none() {
            slash = Some(n);
            n += 1;
            continue;
        }
        if c.is_ascii_alphanumeric() || c == b'_' || c == b'-' || c == b'.' {
            n += 1;
            continue;
        }
        break;
    }
    let slash = slash?;
    if slash == 0 || slash + 1 >= n || n >= bytes.len() || bytes[n] != b'#' {
        return None;
    }
    let mut k = n + 1;
    let mut num = String::new();
    while k < bytes.len() && bytes[k].is_ascii_digit() {
        num.push(bytes[k] as char);
        k += 1;
    }
    if num.is_empty() {
        return None;
    }
    Some((issue_chip(&num, ""), i + k))
}

fn chip_from_span(text: &str) -> Option<Chip> {
    let chars: Vec<char> = text.chars().collect();
    if chars.is_empty() {
        return None;
    }
    let (chip, next) = parse_path(&chars, 0)?;
    if next == chars.len() {
        Some(chip)
    } else {
        None
    }
}

fn parse_path(chars: &[char], i: usize) -> Option<(Chip, usize)> {
    if i > 0 && !is_boundary(chars[i - 1]) {
        return None;
    }
    let rest: String = chars[i..].iter().collect();
    let bytes = rest.as_bytes();
    if bytes
        .first()
        .is_none_or(|c| !(c.is_ascii_alphanumeric() || *c == b'.' || *c == b'_'))
    {
        return None;
    }
    let mut n = 0;
    while n < bytes.len()
        && (bytes[n].is_ascii_alphanumeric() || matches!(bytes[n], b'_' | b'-' | b'.' | b'/'))
    {
        n += 1;
    }
    if n == 0 {
        return None;
    }
    let token = &rest[..n];
    if token.ends_with('.') || token.ends_with('/') || token.contains("://") {
        return None;
    }
    let ext = token.rsplit('.').next().unwrap_or("");
    if !EXTS.contains(&ext) {
        return None;
    }
    let mut end = n;
    let mut line = 0u32;
    if end < bytes.len() && bytes[end] == b':' {
        let mut k = end + 1;
        let mut num = String::new();
        while k < bytes.len() && bytes[k].is_ascii_digit() && num.len() < 6 {
            num.push(bytes[k] as char);
            k += 1;
        }
        if !num.is_empty() {
            line = num.parse().unwrap_or(0);
            end = k;
        }
    }
    Some((file_chip(token, line), i + end))
}

fn first_str(args: &Value, keys: &[&str]) -> String {
    for key in keys {
        if let Some(s) = args.get(*key).and_then(|v| v.as_str()) {
            let s = s.trim();
            if !s.is_empty() {
                return s.to_string();
            }
        }
    }
    String::new()
}

fn args_value(data: &Value) -> Value {
    match data.get("args") {
        Some(Value::String(s)) => serde_json::from_str(s).unwrap_or(Value::Null),
        Some(other) => other.clone(),
        None => Value::Null,
    }
}

fn field_str(data: &Value, key: &str) -> String {
    match data.get(key) {
        Some(Value::String(s)) => s.clone(),
        _ => String::new(),
    }
}

fn clip(text: &str, max: usize) -> String {
    let flat = text.split_whitespace().collect::<Vec<_>>().join(" ");
    if flat.chars().count() <= max {
        return flat;
    }
    let mut short: String = flat.chars().take(max.saturating_sub(1)).collect();
    short.push('…');
    short
}

#[cfg(test)]
mod tests {
    use super::*;

    fn env(typ: &str, data: Value) -> String {
        serde_json::json!({"type": typ, "data": data}).to_string()
    }

    /// Envelope with an explicit `time`, as the JSONL log persists it.
    fn env_at(typ: &str, data: Value, time: &str) -> String {
        serde_json::json!({"type": typ, "data": data, "time": time}).to_string()
    }

    fn visible(t: &Transcript) -> String {
        t.messages
            .iter()
            .map(|m| format!("{}|{}|{}|{}", m.title, m.body, m.detail, m.copy_text))
            .collect::<Vec<_>>()
            .join("\n")
    }

    #[test]
    fn title_matches_session_rules() {
        assert_eq!(title_from_text(""), "");
        assert_eq!(title_from_text("  \n\t  "), "");
        assert_eq!(title_from_text("fix the auth flow"), "fix the auth flow");
        assert_eq!(title_from_text("  hello   \n world  "), "hello world");
        assert_eq!(title_from_text("hi\u{1b}[2J\u{0}there"), "hi[2Jthere");
        assert_eq!(title_from_text("a\u{00a0}b"), "a b");
        let wide = "字".repeat(TITLE_MAX + 10);
        assert_eq!(title_from_text(&wide).chars().count(), TITLE_MAX);
    }

    #[test]
    fn markdown_renders_structure_without_html() {
        let src = "# Plan\n\nSee `webui/src/main.rs:12` and https://github.com/jonathanung/strike/pull/1258.\n\n```go\nfmt.Println(\"hi\")\n```\n\n<script>alert(1)</script>\n\n- one\n- two\n";
        let blocks = parse_markdown(src);
        assert!(matches!(blocks[0], Block::Heading(1, _)));
        assert!(matches!(blocks[1], Block::Paragraph(_)));
        match &blocks[2] {
            Block::Code { lang, text } => {
                assert_eq!(lang, "go");
                assert!(text.contains("Println"));
            }
            other => panic!("fence: {other:?}"),
        }
        let script = match &blocks[3] {
            Block::Paragraph(inlines) => inlines,
            other => panic!("script: {other:?}"),
        };
        assert!(script
            .iter()
            .any(|i| matches!(i, Inline::Text(t) if t.contains("<script>"))));
        assert!(matches!(blocks[4], Block::List { ordered: false, .. }));
        let chips = chips_in(src);
        assert!(chips
            .iter()
            .any(|c| c.kind == ChipKind::File && c.line == 12));
        assert!(chips
            .iter()
            .any(|c| c.kind == ChipKind::PullRequest && c.label == "PR #1258"));
        assert!(
            chips
                .iter()
                .any(|c| c.kind == ChipKind::Issue && c.label == "Issue #1262")
                == false
        );
        let issue = chips_in("Track #1262 and jonathanung/strike#9");
        assert!(issue.iter().any(|c| c.label == "Issue #1262"));
        assert!(issue.iter().any(|c| c.label == "Issue #9"));
    }

    #[test]
    fn tool_card_merges_and_hides_args() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({
                "callId": "c1",
                "name": "edit",
                "args": {"filePath": "webui/src/main.rs", "oldString": "alpha\nbeta", "newString": "alpha\ngamma"}
            }),
        ));
        t.apply_envelope(&env(
            "tool.end",
            serde_json::json!({
                "callId": "c1",
                "name": "edit",
                "title": "webui/src/main.rs",
                "output": "Edited webui/src/main.rs (1 replacement(s))",
                "metadata": {"oldString": "alpha\nbeta", "newString": "alpha\ngamma", "count": 1}
            }),
        ));
        assert_eq!(t.messages.len(), 1);
        let msg = &t.messages[0];
        assert_eq!(msg.title, "Edit file");
        assert_eq!(msg.badge, "done");
        assert!(msg.body.contains("Edited"));
        assert!(!protocol_leak(&visible(&t)));
        let diff = msg.diff.clone().expect("diff");
        assert!(diff.deleted >= 1 && diff.added >= 1);
        assert!(msg.chips.iter().any(|c| c.path == "webui/src/main.rs"));
    }

    #[test]
    fn git_json_becomes_a_summary() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "g", "name": "git", "args": {"action": "diff", "path": "a.go"}}),
        ));
        let raw = r#"{"ok":true,"action":"diff","files":[{"path":"a.go","hunks":[{"lines":["-old","+new","+newer"]}]}]}"#;
        t.apply_envelope(&env(
            "tool.end",
            serde_json::json!({"callId": "g", "name": "git", "title": "1 file", "output": raw}),
        ));
        let msg = &t.messages[0];
        assert_eq!(msg.title, "Git diff");
        assert!(msg.body.contains("Git diff"));
        assert!(!msg.body.contains("\"action\""));
        assert!(!protocol_leak(&visible(&t)));
        assert_eq!(
            msg.diff.as_ref().map(|d| (d.added, d.deleted)),
            Some((2, 1))
        );
        assert!(msg.chips.iter().any(|c| c.path == "a.go"));
    }

    #[test]
    fn child_and_dump_drop_protocol_json() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "child.completed",
            serde_json::json!({
                "name": "scribe",
                "sessionId": "abcd1234",
                "status": "blocked",
                "handoff": {"summary": "verification failed", "blockers": ["schema"]},
                "verification": {"summary": "gate"}
            }),
        ));
        let child = &t.messages[0];
        assert_eq!(child.title, "scribe");
        assert_eq!(child.body, "verification failed");
        assert!(child.detail.contains("Blockers: schema"));
        assert!(!protocol_leak(&visible(&t)));

        let blob = format!("{{\"summary\":\"kept\",\"noise\":\"{}\"}}", "x".repeat(120));
        t.apply_envelope(&env("user.message", serde_json::json!({"text": blob})));
        t.apply_envelope(&env("turn.completed", serde_json::json!({})));
        let shown = visible(&t);
        assert!(shown.contains("kept") || shown.contains("Structured result"));
        assert!(!protocol_leak(&shown), "{shown}");
    }

    #[test]
    fn echo_tip_and_reasoning_stay_out() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "hello"})));
        t.apply_envelope(&env(
            "reasoning.delta",
            serde_json::json!({"text": "secret chain"}),
        ));
        t.apply_envelope(&env(
            "text.delta",
            serde_json::json!({"text": "You said: hello\n\nTip: start a message with `run <command>`"}),
        ));
        t.apply_envelope(&env(
            "text.delta",
            serde_json::json!({"text": " to exercise the bash tool and the permission prompt."}),
        ));
        t.apply_envelope(&env("turn.completed", serde_json::json!({})));
        assert!(t.messages.iter().all(|m| !m.body.contains("secret chain")));
        let strike = t.messages.iter().find(|m| m.role == Role::Strike).unwrap();
        assert!(!strike.body.contains("Tip:"));
        assert!(strike.body.contains("You said: hello"));
        assert_eq!(strike.keep_events, 5);
    }

    #[test]
    fn fork_keep_counts_follow_completed_turns() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("session.header", serde_json::json!({"id": "s"})));
        t.apply_envelope(&env(
            "user.message",
            serde_json::json!({"text": "first prompt"}),
        ));
        t.apply_envelope(&env("turn.started", serde_json::json!({})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "ok"})));
        t.apply_envelope(&env("turn.completed", serde_json::json!({})));
        let first = t.messages.iter().find(|m| m.role == Role::You).unwrap();
        assert_eq!(first.keep_events, 4);
        assert!(first.keep_events > 0);
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "second"})));
        assert_eq!(
            t.messages
                .iter()
                .rev()
                .find(|m| m.role == Role::You)
                .unwrap()
                .keep_events,
            -1
        );
        t.apply_envelope(&env("turn.completed", serde_json::json!({})));
        let keeps: Vec<_> = t
            .messages
            .iter()
            .filter(|m| m.role == Role::You)
            .map(|m| m.keep_events)
            .collect();
        assert_eq!(keeps, vec![4, 6]);
        assert_eq!(t.regenerated_title(), "first prompt");
        assert_eq!(t.title, "");
        t.apply_envelope(&env(
            "session.titled",
            serde_json::json!({"title": "first prompt"}),
        ));
        assert_eq!(t.title, "first prompt");
        t.pin_title("Renamed".into());
        t.set_listed_title("first prompt");
        assert_eq!(t.title, "Renamed");
        t.set_listed_title("Renamed");
        assert!(t.title_pin.is_none());
    }

    #[test]
    fn listed_title_survives_replayed_auto_title() {
        let mut t = Transcript::new();
        t.set_listed_title("Ship the transcript");
        t.apply_envelope(&env(
            "session.titled",
            serde_json::json!({"title": "hello world"}),
        ));
        assert_eq!(t.title, "Ship the transcript");
    }

    #[test]
    fn bash_diff_and_readable_command() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "b", "name": "bash", "args": {"command": "printf diff"}}),
        ));
        t.apply_envelope(&env(
            "tool.end",
            serde_json::json!({
                "callId": "b",
                "title": "printf diff",
                "output": "@@ -1 +1 @@\n-old\n+new\n"
            }),
        ));
        let msg = &t.messages[0];
        assert_eq!(msg.title, "Run command");
        assert!(msg.body.contains("printf"));
        assert_eq!(
            msg.diff.as_ref().map(|d| (d.added, d.deleted)),
            Some((1, 1))
        );
        assert!(!protocol_leak(&msg.body));
        assert!(msg.detail.contains("+new"));
    }

    #[test]
    fn scroll_and_minimap_thresholds() {
        assert!(stick_to_bottom(100.0, 140.0, 40.0));
        assert!(!stick_to_bottom(0.0, 400.0, 40.0));
        assert!(!show_minimap(7));
        assert!(show_minimap(8));
        assert_eq!(suggestions().len(), 6);
        let mut prompts = suggestions().iter().map(|s| s.prompt).collect::<Vec<_>>();
        prompts.sort();
        prompts.dedup();
        assert_eq!(prompts.len(), 6);
        assert!(suggestions()
            .iter()
            .all(|s| !s.detail.is_empty() && !s.title.is_empty()));
    }

    #[test]
    fn anchored_scroll_top_keeps_a_pinned_reader_at_the_bottom() {
        // Keyboard opens: the box shrinks from 1000 to 460 under a reader
        // sitting at the bottom of 2000px of transcript.
        assert_eq!(
            anchored_scroll_top(1000.0, 2000.0, 1000.0, 460.0, 2000.0),
            2000.0
        );
        // Near the bottom (inside STICK_PX) counts as pinned too.
        assert_eq!(
            anchored_scroll_top(970.0, 2000.0, 1000.0, 460.0, 2000.0),
            2000.0
        );
        // Keyboard closes again: still the bottom.
        assert_eq!(
            anchored_scroll_top(1540.0, 2000.0, 460.0, 1000.0, 2000.0),
            2000.0
        );
    }

    #[test]
    fn anchored_scroll_top_keeps_a_scrolled_up_reader_in_place() {
        // Reading 700px above the bottom; the keyboard opening (shrink)
        // and closing (grow) both leave the offset alone while it fits.
        assert_eq!(
            anchored_scroll_top(300.0, 2000.0, 1000.0, 460.0, 2000.0),
            300.0
        );
        assert_eq!(
            anchored_scroll_top(300.0, 2000.0, 460.0, 1000.0, 2000.0),
            300.0
        );
    }

    #[test]
    fn anchored_scroll_top_restores_the_bottom_distance_when_clamped() {
        // 100px above the bottom with the keyboard open (460 box). The
        // keyboard closes to a 1000px box whose max offset is 1000, below
        // the reader's 1440: keep the 100px bottom distance instead of
        // landing on the clamped offset (which reads as the bottom).
        assert_eq!(
            anchored_scroll_top(1440.0, 2000.0, 460.0, 1000.0, 2000.0),
            900.0
        );
    }

    #[test]
    fn turn_elapsed_lands_once_on_the_final_line_of_a_long_turn() {
        let mut t = Transcript::new();
        t.apply_envelope(&env_at(
            "user.message",
            serde_json::json!({"text": "do the slow thing"}),
            "2026-10-26T12:00:00Z",
        ));
        t.apply_envelope(&env_at(
            "tool.begin",
            serde_json::json!({"callId": "c1", "name": "bash", "args": {"command": "sleep 200"}}),
            "2026-10-26T12:00:30Z",
        ));
        t.apply_envelope(&env_at(
            "tool.end",
            serde_json::json!({"callId": "c1", "name": "bash", "output": "done"}),
            "2026-10-26T12:03:30Z",
        ));
        t.apply_envelope(&env_at(
            "text.delta",
            serde_json::json!({"text": "finished"}),
            "2026-10-26T12:04:00Z",
        ));
        // Nothing yet: the turn is still in progress, so no label is
        // stamped (and nothing ticks live).
        assert!(t.turn_elapsed.is_empty());
        t.apply_envelope(&env_at(
            "turn.completed",
            serde_json::json!({}),
            "2026-10-26T12:04:12Z",
        ));
        assert_eq!(t.messages.len(), 3);
        let last = t.messages.last().unwrap();
        assert_eq!(last.role, Role::Strike);
        assert_eq!(t.elapsed_for(last.id), Some("took 4:12"));
        // Exactly one label in the whole transcript, on the final line.
        assert_eq!(t.turn_elapsed.len(), 1);
        for m in &t.messages[..t.messages.len() - 1] {
            assert_eq!(t.elapsed_for(m.id), None);
        }
    }

    #[test]
    fn turn_elapsed_threshold_and_single_line_turns() {
        // Exactly one minute: not shown ("exceeds a minute").
        let mut t = Transcript::new();
        t.apply_envelope(&env_at(
            "user.message",
            serde_json::json!({"text": "quick"}),
            "2026-10-26T12:00:00Z",
        ));
        t.apply_envelope(&env_at(
            "text.delta",
            serde_json::json!({"text": "ok"}),
            "2026-10-26T12:00:59Z",
        ));
        t.apply_envelope(&env_at(
            "turn.completed",
            serde_json::json!({}),
            "2026-10-26T12:01:00Z",
        ));
        assert!(t.turn_elapsed.is_empty());

        // A single-line turn (user line only) over the threshold: the
        // user line is the turn's final line and carries the label.
        let mut t = Transcript::new();
        t.apply_envelope(&env_at(
            "user.message",
            serde_json::json!({"text": "wait for it"}),
            "2026-10-26T12:00:00Z",
        ));
        t.apply_envelope(&env_at(
            "turn.completed",
            serde_json::json!({}),
            "2026-10-26T12:01:30Z",
        ));
        assert_eq!(t.messages.len(), 1);
        assert_eq!(t.elapsed_for(t.messages[0].id), Some("took 1:30"));
    }

    #[test]
    fn turn_elapsed_missing_times_degrade_to_no_label() {
        // One untimed line inside an otherwise long turn (old log):
        // no label for that turn, and the next turn is unaffected.
        let mut t = Transcript::new();
        t.apply_envelope(&env_at(
            "user.message",
            serde_json::json!({"text": "old turn"}),
            "2026-10-26T12:00:00Z",
        ));
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "c1", "name": "bash", "args": {"command": "sleep 200"}}),
        ));
        t.apply_envelope(&env_at(
            "tool.end",
            serde_json::json!({"callId": "c1", "name": "bash", "output": "done"}),
            "2026-10-26T12:05:00Z",
        ));
        t.apply_envelope(&env_at(
            "turn.completed",
            serde_json::json!({}),
            "2026-10-26T12:05:10Z",
        ));
        assert!(t.turn_elapsed.is_empty(), "{:?}", t.turn_elapsed);

        // Untimed completion envelope: also no label.
        let mut t = Transcript::new();
        t.apply_envelope(&env_at(
            "user.message",
            serde_json::json!({"text": "hi"}),
            "2026-10-26T12:00:00Z",
        ));
        t.apply_envelope(&env_at(
            "text.delta",
            serde_json::json!({"text": "slow reply"}),
            "2026-10-26T12:05:00Z",
        ));
        t.apply_envelope(&env("turn.completed", serde_json::json!({})));
        assert!(t.turn_elapsed.is_empty());
    }

    #[test]
    fn turn_elapsed_is_per_turn() {
        let mut t = Transcript::new();
        // Turn 1: short.
        t.apply_envelope(&env_at(
            "user.message",
            serde_json::json!({"text": "one"}),
            "2026-10-26T12:00:00Z",
        ));
        t.apply_envelope(&env_at(
            "text.delta",
            serde_json::json!({"text": "fast"}),
            "2026-10-26T12:00:05Z",
        ));
        t.apply_envelope(&env_at(
            "turn.completed",
            serde_json::json!({}),
            "2026-10-26T12:00:06Z",
        ));
        // Turn 2: long.
        t.apply_envelope(&env_at(
            "user.message",
            serde_json::json!({"text": "two"}),
            "2026-10-26T12:10:00Z",
        ));
        t.apply_envelope(&env_at(
            "text.delta",
            serde_json::json!({"text": "slow"}),
            "2026-10-26T12:12:00Z",
        ));
        t.apply_envelope(&env_at(
            "turn.completed",
            serde_json::json!({}),
            "2026-10-26T12:12:30Z",
        ));
        assert_eq!(t.turn_elapsed.len(), 1);
        let last = t.messages.last().unwrap();
        assert_eq!(t.elapsed_for(last.id), Some("took 2:30"));
    }

    #[test]
    fn streaming_text_is_one_bubble() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "Hello "})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "**world**"})));
        assert_eq!(t.messages.len(), 1);
        assert!(t.messages[0].body.contains("**world**"));
        let blocks = parse_markdown(&t.messages[0].body);
        assert!(matches!(blocks[0], Block::Paragraph(_)));
    }
}
