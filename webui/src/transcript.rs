//! Session transcript projection. Events become messages. Protocol JSON
//! stays out of the visible text. The shell renders [`Transcript`]; it does
//! not interpret tool args itself.

use std::collections::HashMap;

use serde_json::Value;

use crate::composer_triggers::{display_user_text, PermAsk};
use crate::model::{
    child_card_from_parts, child_card_from_text, child_keys_match, prefer_summary, protocol_dump,
    status_kind, strip_echo_tip, summary_for_merge, ChatLine, ChildCard,
};

const TITLE_MAX: usize = 32;
const STICK_PX: f64 = 48.0;
pub const MINIMAP_MIN: usize = 8;

const ECHO_TIP: &str = "Tip: start a message with `run <command>` to exercise the bash tool and the permission prompt.";
const ECHO_MARK: &str = "Tip: start a message with";

/// Terminal badge for a tool or child row whose end never arrived (#1506).
/// States the fact — no result was recorded — without claiming the call
/// failed or was cancelled, neither of which the cockpit can know.
pub const NO_RESULT_BADGE: &str = "no result";
const NO_RESULT_TOOL_DETAIL: &str = "Turn ended before this call returned.";
const NO_RESULT_CHILD_DETAIL: &str = "Turn ended before this delegation returned.";

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
    /// Provider whose credential was rejected, on a `provider_auth` notice
    /// (#1546). Drives the Open Providers action; empty everywhere else.
    pub reconnect: String,
    /// Envelope time of the event that created this message, epoch ms.
    /// 0 means unknown (old log line without a parseable `time`).
    pub at_ms: f64,
    /// `turnId` correlation of the event that created this message, when
    /// the envelope carried one. Empty for legacy log lines. Used by the
    /// orphan sweep (#1506) to scope a `turn.completed` to its own turn.
    pub turn_id: String,
    /// Attachments sent with a `user.message` (#1389). Empty elsewhere.
    pub attachments: Vec<crate::transcript_attachments::SentAttachment>,
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

/// The text a quote or a message pin carries: what Copy copies, else the body.
fn quote_text(m: &Message) -> &str {
    if m.copy_text.trim().is_empty() {
        &m.body
    } else {
        &m.copy_text
    }
}

pub fn suggestions() -> &'static [Suggestion] {
    &SUGGESTIONS
}

pub fn empty_headline(tick: u32) -> &'static str {
    match (tick / 4) % 3 {
        0 => "This session is empty.",
        1 => "Send a message to start.",
        _ => "The transcript shows the turn here.",
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

/// Title and pin from before an optimistic rename.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TitleSnap {
    pub title: String,
    pub pin: Option<String>,
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
    /// Per-turn token footers (#1558), keyed like `turn_elapsed` by the
    /// turn's final message id. Only turns with a known input or output
    /// count get an entry.
    pub turn_usage: HashMap<u32, crate::turn_usage::TurnUsage>,
    /// Usage streams of the turn in progress, folded at `turn.completed`.
    usage_acc: crate::turn_usage::TurnUsage,
    /// `sessionId` of the turn in progress (from `turn.started`), so a
    /// child session's streams are not counted on the parent's turn.
    turn_session: String,
    /// Event ordinal that created each message, keyed by message id
    /// (#1362). Deterministic for a given log, so a quote's anchor
    /// survives a reload; jump-back still verifies the text.
    origin: HashMap<u32, usize>,
    /// Quote carried by a user message (#1362), keyed by message id,
    /// parsed from the stored text so it renders after a reload.
    quotes: HashMap<u32, quote_message::Quote>,
    /// Opt-in reasoning (#1372), kept out of `messages` entirely; the
    /// pane renders it only while its per-session toggle is on.
    pub reasoning: crate::reasoning::ReasoningLog,
    /// Per-turn verification badges (#1373), keyed like `turn_elapsed` by
    /// the turn's final message id. Only turns whose `turn.completed`
    /// carried a verification report get an entry.
    pub turn_verify: HashMap<u32, crate::turn_verify::TurnVerify>,
    /// Per-turn file-change summaries (#1365), keyed like `turn_elapsed`
    /// by the turn's final message id. Only turns whose `turn.completed`
    /// listed changed files get an entry.
    pub turn_files: HashMap<u32, crate::turn_files::TurnFiles>,
    /// Steer acknowledgement markers (#1343), keyed by message id.
    pub steer_acks: crate::steer_ack::SteerAcks,
    /// Live `tool.output` tails (#1374), keyed by the tool card's message
    /// id. Removed when the call's `tool.end` arrives; a call that never
    /// ends keeps its last tail as the only record of what it printed.
    pub tool_tails: HashMap<u32, crate::tool_tail::ToolTail>,
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
            turn_usage: HashMap::new(),
            usage_acc: crate::turn_usage::TurnUsage::default(),
            turn_session: String::new(),
            origin: HashMap::new(),
            quotes: HashMap::new(),
            reasoning: crate::reasoning::ReasoningLog::default(),
            turn_verify: HashMap::new(),
            turn_files: HashMap::new(),
            steer_acks: crate::steer_ack::SteerAcks::default(),
            tool_tails: HashMap::new(),
        }
    }

    /// The verification badge stamped on this message, if it is the final
    /// line of a completed turn whose payload carried a report (#1373).
    pub fn verify_for(&self, id: u32) -> Option<&crate::turn_verify::TurnVerify> {
        self.turn_verify.get(&id)
    }

    /// The file-change summary stamped on this message, if it is the final
    /// line of a completed turn that changed files (#1365).
    pub fn files_for(&self, id: u32) -> Option<&crate::turn_files::TurnFiles> {
        self.turn_files.get(&id)
    }

    /// The live output tail of tool card `id`, while it has one (#1374).
    pub fn tail_for(&self, id: u32) -> Option<&crate::tool_tail::ToolTail> {
        self.tool_tails.get(&id).filter(|t| !t.text.is_empty())
    }

    /// The turn elapsed label stamped on this message, if it is the
    /// final line of a completed turn that exceeded a minute (#1364).
    pub fn elapsed_for(&self, id: u32) -> Option<&str> {
        self.turn_elapsed.get(&id).map(String::as_str)
    }

    /// The per-turn usage footer stamped on this message, if it is the
    /// final line of a completed turn with known token counts (#1558).
    pub fn usage_for(&self, id: u32) -> Option<&crate::turn_usage::TurnUsage> {
        self.turn_usage.get(&id)
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

    /// Title plus pin, captured before an optimistic rename.
    pub fn snapshot_title(&self) -> TitleSnap {
        TitleSnap {
            title: self.title.clone(),
            pin: self.title_pin.clone(),
        }
    }

    /// Put back the snapshot from before a rename the daemon rejected.
    /// Restores the previous pin too, so an earlier unconfirmed rename
    /// stays protected and a failed one does not keep blocking listed titles.
    pub fn restore_title(&mut self, snap: TitleSnap) {
        self.title = snap.title;
        self.title_pin = snap.pin;
    }

    pub fn regenerated_title(&self) -> String {
        self.messages
            .iter()
            .find(|m| m.role == Role::You && !m.retry_text.trim().is_empty())
            .map(|m| title_from_text(&quote_message::split(&m.retry_text).1))
            .unwrap_or_default()
    }

    /// The quote a user message carries, if any (#1362).
    pub fn quote_for(&self, id: u32) -> Option<&quote_message::Quote> {
        self.quotes.get(&id)
    }

    /// What quoting message `id` attaches: its role class, anchor
    /// ordinal and full text. `None` for a line that is still running
    /// or streaming (only settled lines can be quoted) or has no text.
    pub fn quote_source(&self, id: u32) -> Option<(String, usize, String)> {
        let idx = self.messages.iter().position(|m| m.id == id)?;
        if !self.quotable_at(idx) {
            return None;
        }
        let m = &self.messages[idx];
        let ordinal = *self.origin.get(&m.id)?;
        Some((m.role.class().to_string(), ordinal, quote_text(m).to_string()))
    }

    /// Cheap per-row check the view runs on every render: the message
    /// at `idx` is settled, has text and a recorded origin.
    pub fn quotable_at(&self, idx: usize) -> bool {
        let Some(m) = self.messages.get(idx) else {
            return false;
        };
        let streaming =
            m.role == Role::Strike && self.assistant_open && idx + 1 == self.messages.len();
        m.badge != "running"
            && !streaming
            && !quote_text(m).trim().is_empty()
            && self.origin.contains_key(&m.id)
    }

    /// The loaded message a quote points at, or `None` when the
    /// original is not in this transcript (#1362).
    pub fn resolve_quote(&self, quote: &quote_message::Quote) -> Option<u32> {
        let candidates: Vec<quote_message::Candidate<'_>> = self
            .messages
            .iter()
            .filter_map(|m| {
                let ordinal = *self.origin.get(&m.id)?;
                Some(quote_message::Candidate {
                    id: m.id,
                    ordinal,
                    role: m.role.class(),
                    text: quote_text(m),
                })
            })
            .collect();
        quote_message::resolve(quote, &candidates)
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
            Ok(protocol_schema::Event::Unknown { .. }) => return,
            Err(_) => {
                // #1359: a compaction/budget payload the schema rejects
                // still leaves a title-only marker where it happened.
                self.apply_malformed_mark(raw);
                return;
            }
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
                self.attach_reasoning_to_last();
                let text = field_str(&data, "text");
                // #1362: a leading quote block renders as a card, not text.
                let (quote, own) = quote_message::split(&text);
                let shown = display_user_text(&strip_echo_progress(&own));
                // #1324: a review-queue turn is labelled as a review, not
                // shown as if the operator had typed it.
                let review = crate::review_queue::is_review_turn(&text);
                // #1827: a /loop firing is badged with its loop id so it
                // does not read as something the operator typed.
                let loop_badge = loop_badge(&field_str(&data, "loopId"));
                let (ack_text, ack_shown) = (text.clone(), shown.clone());
                let id = self.push(Message {
                    id: 0,
                    role: Role::You,
                    title: if review { "Review".into() } else { "You".into() },
                    body: shown.clone(),
                    badge: if review { "review".into() } else { loop_badge },
                    detail: String::new(),
                    key: String::new(),
                    call_id: String::new(),
                    diff: None,
                    chips: chips_in(&shown),
                    keep_events: -1,
                    retry_text: text,
                    copy_text: shown,
                    reconnect: String::new(),
                    at_ms,
                    turn_id: field_str(&data, "turnId"),
                    attachments: crate::transcript_attachments::parse_images(&data),
                });
                if let Some(quote) = quote {
                    self.quotes.insert(id, quote);
                }
                self.steer_acks.on_user_message(id, &ack_text, &ack_shown);
                self.turn_start = Some(id);
                self.absorb();
            }
            "turn.steered" => {
                let text = field_str(&data, "text");
                let mode = field_str(&data, "mode");
                self.steer_acks.on_steered(&self.messages, &text, &mode);
            }
            "turn.steer_ack" => {
                let conflict = data.get("conflict").and_then(|c| c.as_bool()).unwrap_or(false);
                self.steer_acks.on_steer_ack(
                    &field_str(&data, "summary"),
                    conflict,
                    &field_str(&data, "reason"),
                );
            }
            "session.titled" => self.note_titled(&field_str(&data, "title")),
            "session.rewound" => {
                // #1361: the engine dropped the last root user turn
                // (`rewind` op, TUI /rewind, or edit & resend). The
                // transcript drops the same rows so live and replayed
                // views read as if that turn was never sent. Child
                // lineage rewinds do not touch the root transcript.
                let child = !field_str(&data, "parentSessionId").is_empty()
                    || data.get("depth").and_then(|d| d.as_i64()).unwrap_or(0) > 0;
                if !child {
                    self.drop_last_user_turn();
                }
            }
            "turn.started" => {
                self.assistant_open = false;
                // A new turn starting means every earlier turn has ended:
                // any row from an earlier turn still marked running is an
                // orphan (its turn died without a turn.completed reaching
                // this consumer, e.g. a replayed log). Rows carrying the
                // new turn's own id are untouched.
                self.sweep_orphaned_before_turn(&field_str(&data, "turnId"));
                self.usage_acc = crate::turn_usage::TurnUsage::default();
                self.turn_session = field_str(&data, "sessionId");
            }
            "usage.reported" => self.usage_acc.absorb(&data, &self.turn_session),
            "reasoning.delta" => {
                // #1372: recorded off-transcript; another session's
                // stream (a child's) is not this turn's reasoning.
                let session = field_str(&data, "sessionId");
                if !self.turn_session.is_empty() && !session.is_empty() && session != self.turn_session {
                    return;
                }
                self.reasoning.push_delta(&field_str(&data, "text"));
            }
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
                            let id = last.id;
                            self.reasoning.attach_live(id);
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
                    reconnect: String::new(),
                    at_ms,
                    turn_id: String::new(),
                    attachments: Vec::new(),
                });
            }
            "turn.completed" => {
                self.assistant_open = false;
                self.steer_acks.on_turn_completed();
                self.attach_reasoning_to_last();
                self.sweep_orphaned_at_turn_end(&field_str(&data, "turnId"));
                self.absorb();
                let usage = std::mem::take(&mut self.usage_acc);
                let verify = crate::turn_verify::turn_verify(&data);
                let files = crate::turn_files::turn_files(&data);
                self.stamp_turn(at_ms, usage, verify, files);
                // #1371: an abnormal stop gets its own end-of-turn line,
                // pushed after the turn is stamped so the elapsed/usage
                // labels and fork keep-counts stay on the turn's real
                // last line. Another session's stop on this stream (a
                // child's) is the child card's job.
                if !self.foreign_session(&data) {
                    let stop = field_str(&data, "stopReason");
                    if let Some(m) = crate::turn_failure::stop_marker(&stop) {
                        self.push_failure_line(
                            m.title,
                            m.body.to_string(),
                            crate::turn_failure::STOP_BADGE,
                            format!("{}{}", crate::turn_failure::STOP_KEY_PREFIX, m.reason),
                            at_ms,
                            field_str(&data, "turnId"),
                        );
                    }
                }
            }
            "tool.loop_detected" => {
                if self.foreign_session(&data) {
                    return;
                }
                let count = data.get("count").and_then(|c| c.as_i64()).unwrap_or(0);
                let body = crate::turn_failure::loop_body(
                    &field_str(&data, "message"),
                    &field_str(&data, "toolName"),
                    count,
                );
                self.push_failure_line(
                    crate::turn_failure::LOOP_TITLE,
                    body,
                    crate::turn_failure::LOOP_BADGE,
                    String::new(),
                    at_ms,
                    field_str(&data, "turnId"),
                );
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
                    reconnect: String::new(),
                    at_ms,
                    turn_id: field_str(&data, "turnId"),
                    attachments: Vec::new(),
                });
            }
            "tool.output" => {
                // #1374: append to the running card for this callId only.
                // No name or newest-running fallback: a chunk without a
                // matching card is dropped rather than shown on the wrong
                // call, and a straggler after tool.end (or after the row
                // was settled) never resurrects a tail on a finished card.
                let call_id = field_str(&data, "callId");
                if call_id.is_empty() {
                    return;
                }
                let Some(idx) = self.find_tool(&call_id, "") else {
                    return;
                };
                if self.messages[idx].badge != "running" {
                    return;
                }
                let id = self.messages[idx].id;
                self.tool_tails
                    .entry(id)
                    .or_default()
                    .push(&field_str(&data, "data"));
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
                    // The final model-facing output replaces the live tail.
                    self.tool_tails.remove(&self.messages[idx].id);
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
                        reconnect: String::new(),
                        at_ms,
                        turn_id: field_str(&data, "turnId"),
                        attachments: Vec::new(),
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
            "child.escalated" => {
                // #1375: a child tripped a budget/stall/loop. One persistent
                // line names the child, the trip and the action taken; the
                // later child.completed card still carries the outcome.
                let session = field_str(&data, "sessionId");
                let mut name = field_str(&data, "name");
                if name.is_empty() {
                    name = session.chars().take(8).collect();
                }
                if name.is_empty() {
                    name = "A child".into();
                }
                let body = crate::child_signals::escalation_sentence(
                    &field_str(&data, "kind"),
                    &field_str(&data, "reason"),
                    &field_str(&data, "action"),
                );
                self.push(Message {
                    id: 0,
                    role: Role::Notice,
                    title: format!("{name} escalated"),
                    body: body.clone(),
                    badge: "escalated".into(),
                    detail: String::new(),
                    key: String::new(),
                    call_id: String::new(),
                    diff: None,
                    chips: Vec::new(),
                    keep_events: -1,
                    retry_text: String::new(),
                    copy_text: body,
                    reconnect: String::new(),
                    at_ms,
                    attachments: Vec::new(),
                    turn_id: field_str(&data, "turnId"),
                });
            }
            "artifact.updated" => self.artifact_updates = self.artifact_updates.wrapping_add(1),
            "model.fallback" => {
                // #1350: a different model continued this turn. Name both
                // so no reply is attributed to the wrong model.
                let (title, body) = crate::fallback::notice_text(&data);
                self.push(Message {
                    id: 0,
                    role: Role::Notice,
                    title,
                    body,
                    badge: "fallback".into(),
                    detail: field_str(&data, "error"),
                    key: String::new(),
                    call_id: String::new(),
                    diff: None,
                    chips: Vec::new(),
                    keep_events: -1,
                    retry_text: String::new(),
                    copy_text: String::new(),
                    reconnect: String::new(),
                    at_ms,
                    attachments: Vec::new(),
                    turn_id: field_str(&data, "turnId"),
                });
            }
            "files.invalidated" => self.note_files_invalidated(&data, at_ms),
            "compaction.started"
            | "compaction.completed"
            | "context.fit_warning"
            | "session.budget_warning" => {
                if self.foreign_session(&data) {
                    return;
                }
                if let Some(mark) = crate::compaction_marks::mark(typ, &data) {
                    self.assistant_open = false;
                    self.push_mark(mark, at_ms, field_str(&data, "turnId"));
                }
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
                if code == PROVIDER_AUTH_CODE {
                    // #1546: the daemon classified a rejected credential.
                    // Headline says what happened; the vendor text is
                    // secondary detail; Retry re-sends the failed prompt
                    // only when the user presses it.
                    let provider = field_str(&data, "provider");
                    let retry = self.current_retry();
                    self.push(Message {
                        id: 0,
                        role: Role::Notice,
                        title: "Authentication failed".into(),
                        body: provider_auth_headline(&provider),
                        badge: code,
                        detail: message.clone(),
                        key: String::new(),
                        call_id: String::new(),
                        diff: None,
                        chips: Vec::new(),
                        keep_events: -1,
                        retry_text: retry,
                        copy_text: message,
                        reconnect: provider,
                        at_ms,
                        turn_id: String::new(),
                        attachments: Vec::new(),
                    });
                    return;
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
                    reconnect: String::new(),
                    at_ms,
                    turn_id: String::new(),
                    attachments: Vec::new(),
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
                    reconnect: String::new(),
                    at_ms,
                    turn_id: String::new(),
                    attachments: Vec::new(),
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
                    // Name how the ask ended (#1387): Allowed, Denied, or
                    // Expired when it died with no answer.
                    msg.title = crate::approval_outcome::resolved_title(&decision).into();
                    msg.badge = if decision.is_empty() {
                        "done".into()
                    } else {
                        decision
                    };
                }
            }
            "permission.decided" if crate::approval_outcome::is_latched_denial(&data) => {
                // A repeat of an action the user already denied: no card,
                // one line saying why (#1387).
                let permission = field_str(&data, "permission");
                let patterns = pattern_list(&data);
                let body = if patterns.is_empty() {
                    permission.clone()
                } else {
                    format!("{permission} · {patterns}")
                };
                self.push(Message {
                    id: 0,
                    role: Role::Permission,
                    title: "Denied".into(),
                    body,
                    badge: "denied".into(),
                    detail: crate::approval_outcome::LATCHED_DETAIL.into(),
                    key: String::new(),
                    call_id: String::new(),
                    diff: None,
                    chips: Vec::new(),
                    keep_events: -1,
                    retry_text: String::new(),
                    copy_text: permission,
                    reconnect: String::new(),
                    at_ms,
                    attachments: Vec::new(),
                    turn_id: String::new(),
                });
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

    /// Whether an event belongs to a different session than the turn in
    /// progress (a child's stream), by the same rule `TurnUsage::absorb`
    /// uses: only a known, differing `sessionId` is foreign. A child
    /// session opened on its own has its own `turn.started` id, so its
    /// events are not foreign there.
    fn foreign_session(&self, data: &Value) -> bool {
        let session = field_str(data, "sessionId");
        !self.turn_session.is_empty() && !session.is_empty() && session != self.turn_session
    }

    /// One turn-failure notice line (#1371): end-of-turn marker or
    /// loop-guard line. Body is plain text; nothing is retried or forked
    /// from it.
    fn push_failure_line(
        &mut self,
        title: &str,
        body: String,
        badge: &str,
        key: String,
        at_ms: f64,
        turn_id: String,
    ) {
        self.push(Message {
            id: 0,
            role: Role::Notice,
            title: title.into(),
            body: body.clone(),
            badge: badge.into(),
            detail: String::new(),
            key,
            call_id: String::new(),
            diff: None,
            chips: Vec::new(),
            keep_events: -1,
            retry_text: String::new(),
            copy_text: body,
            reconnect: String::new(),
            at_ms,
            turn_id,
            attachments: Vec::new(),
        });
    }

    /// Minimal marker for a compaction/budget envelope whose payload the
    /// generated schema rejected (#1359). Anything else stays dropped.
    fn apply_malformed_mark(&mut self, raw: &str) {
        let Ok(v) = serde_json::from_str::<Value>(raw) else {
            return;
        };
        let typ = v.get("type").and_then(|t| t.as_str()).unwrap_or("");
        let Some(mark) = crate::compaction_marks::minimal(typ) else {
            return;
        };
        let at_ms = v
            .get("time")
            .and_then(|t| t.as_str())
            .map(crate::model::rfc3339_to_ms)
            .unwrap_or(0.0);
        self.assistant_open = false;
        self.push_mark(mark, at_ms, String::new());
    }

    /// One compaction/budget marker line (#1359): a notice whose summary
    /// and residue sit in the collapsed detail fold.
    fn push_mark(&mut self, mark: crate::compaction_marks::Mark, at_ms: f64, turn_id: String) {
        let copy = if mark.body.is_empty() {
            mark.title.clone()
        } else {
            format!("{}\n{}", mark.title, mark.body)
        };
        self.push(Message {
            id: 0,
            role: Role::Notice,
            title: mark.title,
            body: mark.body,
            badge: mark.badge,
            detail: mark.detail,
            key: mark.key,
            call_id: String::new(),
            diff: None,
            chips: Vec::new(),
            keep_events: -1,
            retry_text: String::new(),
            copy_text: copy,
            reconnect: String::new(),
            at_ms,
            turn_id,
            attachments: Vec::new(),
        });
    }

    fn current_retry(&self) -> String {
        self.messages
            .iter()
            .rev()
            .find(|m| m.role == Role::You)
            .map(|m| m.retry_text.clone())
            .unwrap_or_default()
    }

    /// Rows reduced to what message pins (#1528) need.
    pub fn pin_rows(&self) -> Vec<message_pins::Row<'_>> {
        self.messages
            .iter()
            .map(|m| message_pins::Row {
                id: m.id,
                role: m.role.class(),
                keep: m.keep_events,
                text: quote_text(m),
            })
            .collect()
    }

    /// `files.invalidated` (#1380): one muted notice naming the paths
    /// (TUI wording, capped). Back-to-back invalidations extend the
    /// previous notice instead of stacking lines; `copy_text` keeps the
    /// full path list (one per line), which is also the merge state.
    fn note_files_invalidated(&mut self, data: &Value, at_ms: f64) {
        use crate::files_invalidated::{merge_paths, notice_body, paths_from, KIND};
        let paths = paths_from(data);
        if paths.is_empty() {
            return;
        }
        if let Some(last) = self.messages.last_mut() {
            if last.role == Role::Notice && last.key == KIND {
                let mut all: Vec<String> = last.copy_text.lines().map(str::to_string).collect();
                merge_paths(&mut all, &paths);
                last.body = notice_body(&all);
                last.copy_text = all.join("\n");
                return;
            }
        }
        self.push(Message {
            id: 0,
            role: Role::Notice,
            title: "Files changed".into(),
            body: notice_body(&paths),
            badge: String::new(),
            detail: String::new(),
            key: KIND.into(),
            call_id: String::new(),
            diff: None,
            chips: Vec::new(),
            keep_events: -1,
            retry_text: String::new(),
            copy_text: paths.join("\n"),
            reconnect: String::new(),
            at_ms,
            turn_id: String::new(),
            attachments: Vec::new(),
        });
    }

    /// Rows reduced to what edit & resend (#1361) needs.
    pub fn edit_rows(&self) -> Vec<edit_resend::Row> {
        self.messages
            .iter()
            .map(|m| edit_resend::Row {
                id: m.id,
                role: match m.role {
                    Role::You => edit_resend::Role::You,
                    Role::Strike => edit_resend::Role::Strike,
                    Role::Tool => edit_resend::Role::Tool,
                    _ => edit_resend::Role::Other,
                },
                file_change: m.role == Role::Tool && edit_resend::is_file_tool(&m.key),
            })
            .collect()
    }

    /// Durable anchor, stored excerpt and time of the message at `idx`,
    /// when it can be pinned (settled prose).
    pub fn pin_anchor_at(&self, idx: usize) -> Option<(message_pins::Anchor, String, f64)> {
        let m = self.messages.get(idx)?;
        let prev = self.messages[..idx]
            .iter()
            .rev()
            .map(|p| (p.role.class(), p.keep_events));
        let anchor = message_pins::anchor_from_prev(m.role.class(), m.keep_events, prev)?;
        let excerpt = message_pins::excerpt(quote_text(m));
        if excerpt.is_empty() {
            return None;
        }
        Some((anchor, excerpt, m.at_ms))
    }

    /// Pin rail rows against this transcript.
    pub fn pin_rail(&self, pins: &[message_pins::Pin]) -> Vec<message_pins::RailItem> {
        message_pins::rail(pins, &self.pin_rows())
    }

    /// Id of the newest user message (the one edit & resend can edit).
    pub fn last_user_id(&self) -> Option<u32> {
        self.messages
            .iter()
            .rev()
            .find(|m| m.role == Role::You)
            .map(|m| m.id)
    }

    /// Exact text of message `id` as the user sent it.
    pub fn sent_text(&self, id: u32) -> Option<String> {
        self.messages
            .iter()
            .find(|m| m.id == id && m.role == Role::You)
            .map(|m| m.retry_text.clone())
    }

    /// Apply `session.rewound`: drop the newest user message and every
    /// row after it. Message ids are never reused, so a cursor or fold
    /// keyed on a dropped id simply stops matching.
    fn drop_last_user_turn(&mut self) {
        let Some(cut) = edit_resend::rewind_cut(&self.edit_rows()) else {
            return;
        };
        for m in &self.messages[cut..] {
            self.turn_elapsed.remove(&m.id);
            self.turn_usage.remove(&m.id);
        }
        self.messages.truncate(cut);
        self.assistant_open = false;
        self.turn_start = None;
        self.usage_acc = crate::turn_usage::TurnUsage::default();
    }

    fn push(&mut self, mut message: Message) -> u32 {
        self.next_id += 1;
        message.id = self.next_id;
        let id = message.id;
        // #1372: reasoning streamed before this line belongs above it.
        // A user line opens the next turn, so it never takes reasoning.
        if message.role != Role::You {
            self.reasoning.attach_live(id);
        }
        self.messages.push(message);
        self.origin.insert(id, self.event_count);
        id
    }

    /// Settle reasoning still streaming at a turn boundary onto the
    /// turn's last line (#1372).
    fn attach_reasoning_to_last(&mut self) {
        if let Some(id) = self.messages.last().map(|m| m.id) {
            self.reasoning.attach_live(id);
        }
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
        // No identity-free fallback. An unmatched tool.end gets its own
        // card in the caller. Guessing the newest running tool writes one
        // call's output onto another and leaves the real one spinning.
        None
    }

    /// Index of the first message of the turn `turn.completed` is closing:
    /// the live turn's start when one is tracked, else the whole
    /// transcript (a completion for a turn this consumer never saw open,
    /// e.g. a replay that begins mid-turn).
    fn turn_range_start(&self) -> usize {
        match self.turn_start {
            Some(id) => self
                .messages
                .iter()
                .position(|m| m.id == id)
                .unwrap_or(0),
            None => 0,
        }
    }

    /// Give one still-running tool/child row its honest terminal state.
    fn settle_row(msg: &mut Message) {
        let note = if msg.role == Role::Child {
            NO_RESULT_CHILD_DETAIL
        } else {
            NO_RESULT_TOOL_DETAIL
        };
        msg.badge = NO_RESULT_BADGE.into();
        if msg.detail.is_empty() {
            msg.detail = note.into();
        } else if !msg.detail.contains(note) {
            msg.detail = format!("{}\n{note}", msg.detail);
        }
    }

    /// Sweep on `turn.completed`: rows in the completing turn still
    /// marked `running` never got their `tool.end` / `child.end`, so
    /// they settle as `no result`. Rows that received an end keep their
    /// `done`/`failed` badge. A row tagged with the completing turnId
    /// settles wherever it sits — a mid-turn `user.message` (steer,
    /// child notice, mailbox) moves `turn_start` past it. Untagged
    /// (legacy) rows settle only inside the tracked turn range. A row
    /// carrying a *different* turnId belongs to another turn and is
    /// left alone.
    fn sweep_orphaned_at_turn_end(&mut self, completed_turn_id: &str) {
        let start = self.turn_range_start();
        for (idx, msg) in self.messages.iter_mut().enumerate() {
            if msg.badge != "running" || !matches!(msg.role, Role::Tool | Role::Child) {
                continue;
            }
            let ours = if completed_turn_id.is_empty() || msg.turn_id.is_empty() {
                idx >= start
            } else {
                msg.turn_id == completed_turn_id
            };
            if ours {
                Self::settle_row(msg);
            }
        }
    }

    /// Sweep when a new turn starts: rows from earlier turns still
    /// marked `running` are orphans of turns that ended without this
    /// consumer seeing their completion (dead engine, restarted
    /// gateway, replayed log). A row carrying the new turn's own id
    /// (replayed out of order) is left running.
    fn sweep_orphaned_before_turn(&mut self, new_turn_id: &str) {
        for msg in self.messages.iter_mut() {
            if msg.badge != "running" || !matches!(msg.role, Role::Tool | Role::Child) {
                continue;
            }
            if !new_turn_id.is_empty() && msg.turn_id == new_turn_id {
                continue;
            }
            Self::settle_row(msg);
        }
    }

    /// Any tool/child row still marked `running`? Lets callers skip a
    /// signal write when there is nothing to settle.
    pub fn has_running_tools(&self) -> bool {
        self.messages
            .iter()
            .any(|m| m.badge == "running" && matches!(m.role, Role::Tool | Role::Child))
    }

    /// Settle every still-running tool/child row. Load/replay entry
    /// point (#1506): call after a stored transcript has been fully
    /// applied for a session that is NOT in a live turn, so a log that
    /// ends mid-tool does not render a forever-running row in an idle
    /// session. Never call this for a live turn — use the turn-boundary
    /// sweeps, which key off turn state, not age or timers.
    pub fn settle_orphaned_tools(&mut self) {
        for msg in self.messages.iter_mut() {
            if msg.badge == "running" && matches!(msg.role, Role::Tool | Role::Child) {
                Self::settle_row(msg);
            }
        }
    }

    fn stamp_turn(
        &mut self,
        completed_ms: f64,
        usage: crate::turn_usage::TurnUsage,
        verify: Option<crate::turn_verify::TurnVerify>,
        files: Option<crate::turn_files::TurnFiles>,
    ) {
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
        if let (Some(first), Some(last)) = (self.messages.get(start), self.messages.last()) {
            let (first_ms, last_id) = (first.at_ms, last.id);
            let all_timed = self.messages.iter().skip(start).all(|m| m.at_ms > 0.0);
            if let Some(label) =
                crate::turn_elapsed::turn_elapsed_label(first_ms, completed_ms, all_timed)
            {
                self.turn_elapsed.insert(last_id, label);
            }
            // Turn usage (#1558): same final line, only when a stream
            // reported a known input or output count.
            if usage.label().is_some() {
                self.turn_usage.insert(last_id, usage);
            }
            // Verification badge (#1373): same final line, from the
            // payload only; no report, no badge.
            if let Some(v) = verify {
                self.turn_verify.insert(last_id, v);
            }
            // File-change summary (#1365): same final line; a turn with
            // no file changes gets no entry and renders nothing.
            if let Some(f) = files {
                self.turn_files.insert(last_id, f);
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
            let merged = prefer_summary(
                &existing.body,
                &summary_for_merge(&card.summary, &card.status),
            );
            existing.body = merged.clone();
            existing.copy_text = merged;
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
            reconnect: String::new(),
            at_ms,
            turn_id: String::new(),
            attachments: Vec::new(),
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

/// Badge prefix on a `/loop` firing's user line (#1827): "loop l1".
pub const LOOP_BADGE_PREFIX: &str = "loop ";

/// Badge text for a `user.message` `loopId`; empty for ordinary sends.
fn loop_badge(loop_id: &str) -> String {
    let id = loop_id.trim();
    if id.is_empty() {
        String::new()
    } else {
        format!("{LOOP_BADGE_PREFIX}{}", clip(id, 16))
    }
}

/// The loop id a badge names, when it is a `/loop` firing badge.
pub fn loop_badge_id(badge: &str) -> Option<&str> {
    badge.strip_prefix(LOOP_BADGE_PREFIX).filter(|id| !id.is_empty())
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

/// `engine.error` code for a provider rejecting the saved credential
/// mid-session (#1546). Classified daemon-side; never inferred here from
/// message text.
pub const PROVIDER_AUTH_CODE: &str = "provider_auth";

/// Display name for a provider id in the auth-failure headline.
fn provider_display(provider: &str) -> String {
    match provider.trim() {
        "anthropic" => "Anthropic".into(),
        "openai" => "OpenAI".into(),
        "google" => "Google".into(),
        "xai" => "xAI".into(),
        "deepseek" => "DeepSeek".into(),
        "kimi" => "Kimi".into(),
        other => other.to_string(),
    }
}

/// Headline for a `provider_auth` notice: what happened, in the user's
/// terms, without the vendor's raw error text.
pub fn provider_auth_headline(provider: &str) -> String {
    let name = provider_display(provider);
    if name.is_empty() {
        "Provider authentication failed — the saved credential was rejected.".into()
    } else {
        format!("{name} authentication failed — the saved credential was rejected.")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn loop_firing_user_line_carries_loop_badge() {
        // #1827: live and replayed lines are the same envelope shape.
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "check ci", "loopId": "l1"})));
        t.apply_envelope(&env("turn.completed", serde_json::json!({"stopReason": "end_turn"})));
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "hello"})));
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "blank", "loopId": "  "})));
        let you: Vec<(String, String)> = t
            .messages
            .iter()
            .filter(|m| m.role == Role::You)
            .map(|m| (m.body.clone(), m.badge.clone()))
            .collect();
        assert_eq!(
            you,
            vec![
                ("check ci".to_string(), "loop l1".to_string()),
                ("hello".to_string(), String::new()),
                ("blank".to_string(), String::new()),
            ]
        );
        assert_eq!(loop_badge_id("loop l1"), Some("l1"));
        assert_eq!(loop_badge_id("review"), None);
        assert_eq!(loop_badge_id("loop "), None);
    }

    fn notices(t: &Transcript) -> Vec<(String, String)> {
        t.messages
            .iter()
            .filter(|m| m.role == Role::Notice)
            .map(|m| (m.title.clone(), m.key.clone()))
            .collect()
    }

    #[test]
    fn compaction_and_budget_markers_render_inline_in_order() {
        // Real envelope shapes, as the engine emits them mid-session.
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "long task"})));
        t.apply_envelope(&env("turn.started", serde_json::json!({"turnId": "t1", "sessionId": "s1"})));
        t.apply_envelope(&env(
            "context.fit_warning",
            serde_json::json!({"sessionId": "s1", "turnId": "t1", "estimatedTokens": 180000,
                "contextLimit": 200000, "level": "warn", "message": "context is getting full"}),
        ));
        t.apply_envelope(&env(
            "compaction.started",
            serde_json::json!({"sessionId": "s1", "turnId": "t1", "reason": "threshold", "strategy": "summarize"}),
        ));
        t.apply_envelope(&env(
            "compaction.completed",
            serde_json::json!({"sessionId": "s1", "turnId": "t1", "reason": "threshold",
                "strategy": "summarize", "removed": 6, "kept": 2, "summary": "Earlier work summarised.",
                "residue": {"schemaVersion": "1", "facts": [{"id": "f1", "text": "tests pass"}]}}),
        ));
        t.apply_envelope(&env(
            "session.budget_warning",
            serde_json::json!({"sessionId": "s1", "level": "80", "kind": "cost_usd",
                "costUsd": 4.0, "maxCostUsd": 5.0, "ratio": 0.8, "message": "80% of session budget"}),
        ));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "done"})));
        t.apply_envelope(&env("turn.completed", serde_json::json!({"turnId": "t1", "stopReason": "end_turn"})));
        let roles: Vec<Role> = t.messages.iter().map(|m| m.role).collect();
        assert_eq!(
            roles,
            [Role::You, Role::Notice, Role::Notice, Role::Notice, Role::Notice, Role::Strike]
        );
        assert_eq!(
            notices(&t),
            [
                ("Context near limit".to_string(), "compaction:fit".to_string()),
                ("Compacting context".to_string(), "compaction:started".to_string()),
                ("Context compacted".to_string(), "compaction:completed".to_string()),
                ("Budget warning".to_string(), "compaction:budget".to_string()),
            ]
        );
        let done = &t.messages[3];
        assert!(done.body.starts_with("Removed 6 messages, kept 2 messages"), "{}", done.body);
        assert!(done.detail.contains("Summary\nEarlier work summarised."));
        assert!(done.detail.contains("Facts kept\n- tests pass"));
        assert!(!protocol_leak(&visible(&t)), "{}", visible(&t));
    }

    #[test]
    fn malformed_compaction_payload_degrades_to_minimal_marker() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "go"})));
        // `removed` as a string fails the generated schema.
        t.apply_envelope(&env("compaction.completed", serde_json::json!({"removed": "lots"})));
        t.apply_envelope(&env("session.budget_warning", serde_json::json!("not an object")));
        // `null` counts pass the daemon's Go decoder but not the schema.
        t.apply_envelope(&env(
            "compaction.completed",
            serde_json::json!({"reason": "manual", "removed": null}),
        ));
        // Legacy payload: no counts, no strategy — still a marker.
        t.apply_envelope(&env("compaction.completed", serde_json::json!({"reason": "manual"})));
        // A malformed event this module does not own stays dropped.
        t.apply_envelope(&env("tool.end", serde_json::json!({"isError": "nope"})));
        t.apply_envelope(r#"{"type":"compaction.started","data":"#);
        let got = notices(&t);
        assert_eq!(
            got,
            [
                ("Context compacted".to_string(), "compaction:completed".to_string()),
                ("Budget warning".to_string(), "compaction:budget".to_string()),
                ("Context compacted".to_string(), "compaction:completed".to_string()),
                ("Context compacted".to_string(), "compaction:completed".to_string()),
            ]
        );
        assert_eq!(t.messages[1].body, "");
        assert_eq!(t.messages[3].body, "");
        assert_eq!(t.messages[4].body, "Requested manually");
        assert_eq!(t.messages.len(), 5);
    }

    #[test]
    fn foreign_session_compaction_adds_no_marker() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("turn.started", serde_json::json!({"sessionId": "root"})));
        t.apply_envelope(&env(
            "compaction.completed",
            serde_json::json!({"sessionId": "child", "removed": 1, "kept": 1}),
        ));
        assert!(notices(&t).is_empty());
        t.apply_envelope(&env(
            "compaction.completed",
            serde_json::json!({"sessionId": "root", "removed": 1, "kept": 1}),
        ));
        assert_eq!(notices(&t).len(), 1);
    }

    #[test]
    fn markers_replay_identically() {
        let lines = [
            env_at("user.message", serde_json::json!({"text": "a"}), "2026-10-26T06:00:00Z"),
            env_at("compaction.started", serde_json::json!({"reason": "manual", "strategy": "trim"}), "2026-10-26T06:00:01Z"),
            env_at("compaction.completed", serde_json::json!({"reason": "manual", "strategy": "trim", "removed": 4, "kept": 3}), "2026-10-26T06:00:02Z"),
            env_at("context.fit_warning", serde_json::json!({"level": "critical", "estimatedTokens": 9, "contextLimit": 8}), "2026-10-26T06:00:03Z"),
        ];
        let build = || {
            let mut t = Transcript::new();
            for l in &lines {
                t.apply_envelope(l);
            }
            t
        };
        let (a, b) = (build(), build());
        assert_eq!(a.messages, b.messages);
        let stamps: Vec<f64> = a.messages.iter().map(|m| m.at_ms).collect();
        assert!(stamps.windows(2).all(|w| w[0] < w[1]), "{stamps:?}");
        assert_eq!(a.messages[3].title, "Context over limit");
    }

    /// #1371: one turn ending with `stopReason`, after a short answer.
    fn turn_ending(stop: Value) -> Transcript {
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "go"})));
        t.apply_envelope(&env("turn.started", serde_json::json!({"turnId": "t1"})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "partial"})));
        t.apply_envelope(&env("turn.completed", stop));
        t
    }

    #[test]
    fn clean_turn_end_adds_no_failure_line() {
        for stop in [
            serde_json::json!({"stopReason": "end_turn"}),
            serde_json::json!({"stopReason": "interrupted"}),
            serde_json::json!({}),
        ] {
            let t = turn_ending(stop.clone());
            assert_eq!(t.messages.len(), 2, "{stop}");
            assert!(t.messages.iter().all(|m| m.role != Role::Notice), "{stop}");
        }
    }

    #[test]
    fn abnormal_stop_reasons_end_with_a_marker() {
        let cases = [
            ("error", "Turn failed"),
            ("loop_detected", "Turn stopped by loop guard"),
            ("timeout", "Turn timed out"),
            ("budget_exhausted", "Budget exhausted"),
        ];
        for (reason, title) in cases {
            let t = turn_ending(serde_json::json!({"turnId": "t1", "stopReason": reason}));
            let last = t.messages.last().unwrap();
            assert_eq!(last.role, Role::Notice, "{reason}");
            assert_eq!(last.title, title, "{reason}");
            assert_eq!(last.badge, crate::turn_failure::STOP_BADGE, "{reason}");
            assert_eq!(crate::turn_failure::stop_reason_of(&last.key), Some(reason));
            assert_eq!(last.turn_id, "t1");
            // Stamped after the turn: fork keep-counts stay on real lines.
            assert_eq!(last.keep_events, -1, "{reason}");
            assert!(t.messages[1].keep_events > 0, "{reason}");
            assert!(!protocol_leak(&visible(&t)), "{reason}");
        }
    }

    #[test]
    fn foreign_session_stop_adds_no_marker() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "go"})));
        t.apply_envelope(&env(
            "turn.started",
            serde_json::json!({"turnId": "t1", "sessionId": "root"}),
        ));
        t.apply_envelope(&env(
            "tool.loop_detected",
            serde_json::json!({"sessionId": "child", "message": "x"}),
        ));
        t.apply_envelope(&env(
            "turn.completed",
            serde_json::json!({"sessionId": "child", "stopReason": "error"}),
        ));
        assert!(t.messages.iter().all(|m| m.role != Role::Notice));
        // A child session viewed on its own: its events carry the
        // parent link but match its own turn, so the marker shows.
        let mut c = Transcript::new();
        c.apply_envelope(&env("user.message", serde_json::json!({"text": "task"})));
        c.apply_envelope(&env(
            "turn.started",
            serde_json::json!({"sessionId": "child", "parentSessionId": "root"}),
        ));
        c.apply_envelope(&env(
            "turn.completed",
            serde_json::json!({"sessionId": "child", "parentSessionId": "root", "stopReason": "timeout"}),
        ));
        assert_eq!(c.messages.last().unwrap().title, "Turn timed out");
    }

    #[test]
    fn engine_failure_then_marker_replays_in_order() {
        // The engine's real loop-guard sequence (harness/engine failTurn):
        // tool.loop_detected, engine.error, turn.completed loop_detected.
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "go"})));
        t.apply_envelope(&env("turn.started", serde_json::json!({"turnId": "t1"})));
        t.apply_envelope(&env(
            "tool.loop_detected",
            serde_json::json!({
                "turnId": "t1",
                "reason": "identical_calls",
                "toolName": "bash",
                "count": 3,
                "message": "repeated identical failing tool calls"
            }),
        ));
        t.apply_envelope(&env(
            "engine.error",
            serde_json::json!({"message": "tool loop detected: identical_calls", "code": "blocked"}),
        ));
        t.apply_envelope(&env(
            "turn.completed",
            serde_json::json!({"turnId": "t1", "stopReason": "loop_detected"}),
        ));
        let titles: Vec<&str> = t.messages.iter().map(|m| m.title.as_str()).collect();
        assert_eq!(
            titles,
            ["You", "Loop guard", "Notice", "Turn stopped by loop guard"]
        );
        let guard = &t.messages[1];
        assert_eq!(guard.badge, "blocked");
        assert_eq!(guard.body, "Repeated identical failing tool calls · bash, 3 times");
        assert_eq!(crate::turn_failure::stop_reason_of(&guard.key), None);
    }

    #[test]
    fn loop_detected_with_sparse_payload_degrades() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("tool.loop_detected", serde_json::json!({})));
        assert_eq!(t.messages.len(), 1);
        assert_eq!(t.messages[0].body, "Repeated failing tool calls");
    }

    #[test]
    fn failure_marker_does_not_steal_turn_labels() {
        let mut t = Transcript::new();
        t.apply_envelope(&env_at(
            "user.message",
            serde_json::json!({"text": "slow"}),
            "2026-10-26T06:00:00Z",
        ));
        t.apply_envelope(&env_at(
            "text.delta",
            serde_json::json!({"text": "partial"}),
            "2026-10-26T06:01:00Z",
        ));
        t.apply_envelope(&env_at(
            "turn.completed",
            serde_json::json!({"stopReason": "timeout"}),
            "2026-10-26T06:04:12Z",
        ));
        let answer = t.messages.iter().find(|m| m.role == Role::Strike).unwrap();
        let marker = t.messages.last().unwrap();
        assert_eq!(marker.title, "Turn timed out");
        assert_eq!(t.elapsed_for(answer.id), Some("took 4:12"));
        assert_eq!(t.elapsed_for(marker.id), None);
    }

    #[test]
    fn reasoning_attaches_around_tools_and_turn_end() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "fix it"})));
        t.apply_envelope(&env("turn.started", serde_json::json!({"sessionId": "s1"})));
        t.apply_envelope(&env("reasoning.delta", serde_json::json!({"sessionId": "s1", "text": "check the file"})));
        assert_eq!(t.reasoning.live(), "check the file");
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "c1", "name": "read", "args": {"path": "a.rs"}}),
        ));
        let tool_id = t.messages.last().unwrap().id;
        assert_eq!(t.reasoning.for_message(tool_id), Some("check the file"));
        t.apply_envelope(&env("tool.end", serde_json::json!({"callId": "c1", "name": "read", "output": "x"})));
        t.apply_envelope(&env("reasoning.delta", serde_json::json!({"text": "now answer"})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "Fixed."})));
        let answer = t.messages.last().unwrap().clone();
        assert_eq!(answer.role, Role::Strike);
        assert_eq!(t.reasoning.for_message(answer.id), Some("now answer"));
        // Mid-answer reasoning appends to the answer's own entry.
        t.apply_envelope(&env("reasoning.delta", serde_json::json!({"text": " double-check"})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": " Done."})));
        assert_eq!(t.reasoning.for_message(answer.id), Some("now answer double-check"));
        // Trailing reasoning settles on the turn's last line at turn end.
        t.apply_envelope(&env("reasoning.delta", serde_json::json!({"text": " wrap"})));
        t.apply_envelope(&env("turn.completed", serde_json::json!({})));
        assert_eq!(t.reasoning.live(), "");
        assert_eq!(t.reasoning.for_message(answer.id), Some("now answer double-check wrap"));
        // A child's reasoning on this stream is not this turn's.
        t.apply_envelope(&env("reasoning.delta", serde_json::json!({"sessionId": "child", "text": "nope"})));
        assert_eq!(t.reasoning.live(), "");
        assert!(t.messages.iter().all(|m| !m.body.contains("check the file")));
    }

    #[test]
    fn no_reasoning_leaves_log_empty() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "hi"})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "hello"})));
        t.apply_envelope(&env("turn.completed", serde_json::json!({})));
        assert!(t.reasoning.is_empty());
    }

    #[test]
    fn provider_auth_error_renders_actionable_notice() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "fix the bug"})));
        t.apply_envelope(&env(
            "engine.error",
            serde_json::json!({
                "message": "openai: invalid_request_error: Incorrect API key provided",
                "code": "provider_auth",
                "provider": "openai"
            }),
        ));
        let m = t.messages.last().unwrap();
        assert_eq!(m.role, Role::Notice);
        assert_eq!(m.badge, PROVIDER_AUTH_CODE);
        assert_eq!(m.reconnect, "openai");
        assert_eq!(
            m.body,
            "OpenAI authentication failed — the saved credential was rejected."
        );
        assert!(m.detail.contains("Incorrect API key"));
        assert_eq!(m.retry_text, "fix the bug");
    }

    #[test]
    fn generic_engine_error_mentioning_401_stays_plain() {
        // No client-side string matching: only the daemon's code makes
        // a notice actionable.
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "engine.error",
            serde_json::json!({"message": "openai: unexpected status 401: unauthorized"}),
        ));
        let m = t.messages.last().unwrap();
        assert_eq!(m.title, "Notice");
        assert!(m.reconnect.is_empty());
        assert!(m.retry_text.is_empty());
        assert!(m.body.contains("401"));
    }

    #[test]
    fn provider_auth_headline_names_known_and_unknown_providers() {
        assert_eq!(
            provider_auth_headline("anthropic"),
            "Anthropic authentication failed — the saved credential was rejected."
        );
        assert_eq!(
            provider_auth_headline("my-gateway"),
            "my-gateway authentication failed — the saved credential was rejected."
        );
        assert_eq!(
            provider_auth_headline(""),
            "Provider authentication failed — the saved credential was rejected."
        );
    }

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
    fn orphan_tool_swept_at_turn_end() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "user.message",
            serde_json::json!({"text": "hi", "turnId": "t1"}),
        ));
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "a", "name": "bash", "turnId": "t1", "args": {"command": "sleep 9"}}),
        ));
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "b", "name": "read", "turnId": "t1", "args": {"filePath": "a.go"}}),
        ));
        t.apply_envelope(&env(
            "tool.end",
            serde_json::json!({"callId": "b", "name": "read", "output": "contents"}),
        ));
        t.apply_envelope(&env(
            "turn.completed",
            serde_json::json!({"turnId": "t1"}),
        ));
        let orphan = t
            .messages
            .iter()
            .find(|m| m.call_id == "a")
            .expect("orphan row");
        assert_eq!(orphan.badge, NO_RESULT_BADGE);
        assert_eq!(orphan.badge, "no result");
        assert!(
            orphan.detail.contains("Turn ended before this call returned."),
            "{}",
            orphan.detail
        );
        let done = t
            .messages
            .iter()
            .find(|m| m.call_id == "b")
            .expect("done row");
        assert_eq!(done.badge, "done");
    }

    #[test]
    fn orphan_tool_failed_row_untouched_at_turn_end() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "user.message",
            serde_json::json!({"text": "hi", "turnId": "t1"}),
        ));
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "a", "name": "bash", "turnId": "t1", "args": {"command": "false"}}),
        ));
        t.apply_envelope(&env(
            "tool.end",
            serde_json::json!({"callId": "a", "name": "bash", "output": "boom", "isError": true}),
        ));
        t.apply_envelope(&env(
            "turn.completed",
            serde_json::json!({"turnId": "t1"}),
        ));
        let row = t
            .messages
            .iter()
            .find(|m| m.call_id == "a")
            .expect("failed row");
        assert_eq!(row.badge, "failed");
        assert!(!row.detail.contains("Turn ended before this call returned."));
    }

    #[test]
    fn orphan_swept_only_for_the_completing_turn() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "user.message",
            serde_json::json!({"text": "hi", "turnId": "t1"}),
        ));
        // A running row correlated to a different (live) turn must not be
        // swept by t1's completion.
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "a", "name": "bash", "turnId": "t2", "args": {"command": "sleep 9"}}),
        ));
        t.apply_envelope(&env(
            "turn.completed",
            serde_json::json!({"turnId": "t1"}),
        ));
        let row = t
            .messages
            .iter()
            .find(|m| m.call_id == "a")
            .expect("other-turn row");
        assert_eq!(row.badge, "running");
        // Its own turn completing does sweep it.
        t.apply_envelope(&env(
            "turn.completed",
            serde_json::json!({"turnId": "t2"}),
        ));
        let row = t
            .messages
            .iter()
            .find(|m| m.call_id == "a")
            .expect("other-turn row");
        assert_eq!(row.badge, "no result");
    }

    #[test]
    fn replay_ending_mid_tool_settles_on_load() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "user.message",
            serde_json::json!({"text": "hi", "turnId": "t1"}),
        ));
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "a", "name": "bash", "turnId": "t1", "args": {"command": "sleep 9"}}),
        ));
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "b", "name": "read", "turnId": "t1", "args": {"filePath": "a.go"}}),
        ));
        t.apply_envelope(&env(
            "tool.end",
            serde_json::json!({"callId": "b", "name": "read", "output": "contents"}),
        ));
        // The stored log ends here: no tool.end for `a`, no turn.completed.
        let row = t
            .messages
            .iter()
            .find(|m| m.call_id == "a")
            .expect("orphan row");
        assert_eq!(row.badge, "running");
        assert!(t.has_running_tools());
        t.settle_orphaned_tools();
        assert!(!t.has_running_tools());
        let row = t
            .messages
            .iter()
            .find(|m| m.call_id == "a")
            .expect("orphan row");
        assert_eq!(row.badge, "no result");
        assert!(row.detail.contains("Turn ended before this call returned."));
        let done = t
            .messages
            .iter()
            .find(|m| m.call_id == "b")
            .expect("done row");
        assert_eq!(done.badge, "done");
        // A late tool.end (stale idle raced a live call) heals the row.
        let mut healed = t.clone();
        healed.apply_envelope(&env(
            "tool.end",
            serde_json::json!({"callId": "a", "name": "bash", "output": "ok"}),
        ));
        let row = healed.messages.iter().find(|m| m.call_id == "a").unwrap();
        assert_eq!(row.badge, "done");
        assert!(!row.detail.contains("Turn ended before"));
        // Settling is idempotent: the note is not appended twice.
        t.settle_orphaned_tools();
        let row = t
            .messages
            .iter()
            .find(|m| m.call_id == "a")
            .expect("orphan row");
        assert_eq!(
            row.detail
                .matches("Turn ended before this call returned.")
                .count(),
            1
        );
    }

    #[test]
    fn replay_orphan_swept_when_next_turn_starts_and_live_row_survives() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "user.message",
            serde_json::json!({"text": "first", "turnId": "t1"}),
        ));
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "a", "name": "bash", "turnId": "t1", "args": {"command": "sleep 9"}}),
        ));
        // No turn.completed for t1 in the log; the next turn starting is
        // the evidence t1 ended, so its orphan settles here.
        t.apply_envelope(&env(
            "turn.started",
            serde_json::json!({"turnId": "t2"}),
        ));
        let orphan = t
            .messages
            .iter()
            .find(|m| m.call_id == "a")
            .expect("orphan row");
        assert_eq!(orphan.badge, "no result");
        // A tool in the live turn keeps running until its own turn ends.
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "b", "name": "bash", "turnId": "t2", "args": {"command": "sleep 9"}}),
        ));
        let live = t
            .messages
            .iter()
            .find(|m| m.call_id == "b")
            .expect("live row");
        assert_eq!(live.badge, "running");
        t.apply_envelope(&env(
            "turn.completed",
            serde_json::json!({"turnId": "t2"}),
        ));
        let live = t
            .messages
            .iter()
            .find(|m| m.call_id == "b")
            .expect("live row");
        assert_eq!(live.badge, "no result");
    }

    #[test]
    fn orphan_swept_at_turn_end_after_mid_turn_user_message() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "go", "turnId": "t1"})));
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "a", "name": "bash", "turnId": "t1", "args": {"command": "sleep 9"}}),
        ));
        // Steer / child notice / mailbox inject a user.message mid-turn.
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "also this"})));
        t.apply_envelope(&env("turn.completed", serde_json::json!({"turnId": "t1"})));
        let row = t.messages.iter().find(|m| m.call_id == "a").expect("row");
        assert_eq!(row.badge, "no result");
    }

    #[test]
    fn child_escalated_renders_a_line() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "child.escalated",
            serde_json::json!({
                "sessionId": "abcd1234ef",
                "name": "scout",
                "kind": "stall",
                "reason": "no progress for 300s",
                "action": "finalizing",
                "terminalStatus": "blocked"
            }),
        ));
        let line = t
            .messages
            .iter()
            .find(|m| m.badge == "escalated")
            .expect("escalation line");
        assert_eq!(line.role, Role::Notice);
        assert_eq!(line.title, "scout escalated");
        assert_eq!(
            line.body,
            "Stalled: no progress for 300s. Action: finishing with a handoff."
        );

        // No name: fall back to the short session id.
        t.apply_envelope(&env(
            "child.escalated",
            serde_json::json!({"sessionId": "abcd1234ef", "kind": "tokens", "reason": "", "action": "interrupted"}),
        ));
        let last = t.messages.last().unwrap();
        assert_eq!(last.title, "abcd1234 escalated");
        assert_eq!(last.body, "Token budget. Action: stopped.");
    }

    #[test]
    fn orphan_child_row_swept_at_turn_end() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "user.message",
            serde_json::json!({"text": "hi", "turnId": "t1"}),
        ));
        t.apply_envelope(&env(
            "child.completed",
            serde_json::json!({
                "name": "scribe",
                "sessionId": "abcd1234",
                "status": "running",
                "summary": "working on the subtask"
            }),
        ));
        let child = t
            .messages
            .iter()
            .find(|m| m.role == Role::Child)
            .expect("child row");
        assert_eq!(child.badge, "running");
        t.apply_envelope(&env(
            "turn.completed",
            serde_json::json!({"turnId": "t1"}),
        ));
        let child = t
            .messages
            .iter()
            .find(|m| m.role == Role::Child)
            .expect("child row");
        assert_eq!(child.badge, "no result");
        assert!(
            child
                .detail
                .contains("Turn ended before this delegation returned."),
            "{}",
            child.detail
        );
    }

    #[test]
    fn unmatched_tool_end_gets_its_own_card() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "x", "name": "bash", "args": {"command": "ls"}}),
        ));
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": "y", "name": "edit", "args": {"filePath": "a.go"}}),
        ));
        t.apply_envelope(&env(
            "tool.end",
            serde_json::json!({
                "callId": "missing",
                "name": "webfetch",
                "output": "unmatched body",
            }),
        ));
        assert_eq!(t.messages.len(), 3);
        assert_eq!(t.messages[0].badge, "running");
        assert_eq!(t.messages[0].call_id, "x");
        assert_eq!(t.messages[1].badge, "running");
        assert_eq!(t.messages[1].call_id, "y");
        assert_eq!(t.messages[2].call_id, "missing");
        assert_eq!(t.messages[2].key, "webfetch");
        assert_eq!(t.messages[2].badge, "done");
        assert!(
            t.messages[2].body.contains("unmatched")
                || t.messages[2].copy_text.contains("unmatched")
                || t.messages[2].detail.contains("unmatched"),
            "{:?}",
            t.messages[2]
        );
        // Same-name running match still completes that card.
        t.apply_envelope(&env(
            "tool.end",
            serde_json::json!({"callId": "", "name": "bash", "output": "listed"}),
        ));
        assert_eq!(t.messages[0].badge, "done");
        assert_eq!(t.messages[1].badge, "running");
        // Known call id still wins over the name.
        t.apply_envelope(&env(
            "tool.end",
            serde_json::json!({"callId": "y", "name": "edit", "output": "edited"}),
        ));
        assert_eq!(t.messages[1].badge, "done");
        assert_eq!(t.messages[1].call_id, "y");
    }

    #[test]
    fn status_only_child_update_keeps_the_richer_summary() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "child.completed",
            serde_json::json!({
                "name": "scribe",
                "sessionId": "abcd",
                "status": "completed",
                "summary": "wrote the migration and the tests",
            }),
        ));
        t.apply_envelope(&env(
            "child.completed",
            serde_json::json!({
                "name": "scribe-2",
                "sessionId": "efgh",
                "status": "blocked",
                "summary": "scribe-2 is still reading the schema",
            }),
        ));
        assert_eq!(t.messages.len(), 2);
        assert_eq!(t.messages[0].body, "wrote the migration and the tests");
        assert!(t.messages[1].body.contains("still reading"));
        t.apply_envelope(&env(
            "child.completed",
            serde_json::json!({
                "name": "scribe",
                "sessionId": "abcd",
                "status": "completed",
            }),
        ));
        assert_eq!(t.messages[0].body, "wrote the migration and the tests");
        assert_eq!(t.messages[0].copy_text, t.messages[0].body);
        assert_eq!(t.messages[1].body.contains("still reading"), true);
        t.apply_envelope(&env(
            "child.completed",
            serde_json::json!({
                "name": "scribe",
                "status": "completed",
                "summary": "wrote the migration, the tests, and the rollback notes",
            }),
        ));
        assert!(t.messages[0].body.contains("rollback"));
        assert_eq!(t.messages[0].copy_text, t.messages[0].body);
        t.apply_envelope(&env(
            "child.completed",
            serde_json::json!({"name": "lonely", "status": "blocked"}),
        ));
        let lonely = t
            .messages
            .iter()
            .find(|m| m.title == "lonely")
            .expect("status-only card");
        assert_eq!(lonely.badge, "blocked");
        assert_eq!(lonely.body, "blocked");
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
        // #1372: reasoning is opt-in and quarantined — never in a
        // message body, copy text or the markdown export; it sits in the
        // side log, attached to the answer it preceded.
        assert!(t.messages.iter().all(|m| !m.body.contains("secret chain")));
        assert!(t.messages.iter().all(|m| !m.copy_text.contains("secret chain")));
        assert!(!t.to_markdown().contains("secret chain"));
        let strike = t.messages.iter().find(|m| m.role == Role::Strike).unwrap();
        assert_eq!(t.reasoning.for_message(strike.id), Some("secret chain"));
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
    fn failed_rename_rolls_the_pin_back() {
        let mut t = Transcript::new();
        t.set_listed_title("Saved title");
        assert!(t.title_pin.is_none());
        let snap = t.snapshot_title();
        t.pin_title("Unsaved".into());
        assert_eq!(t.title, "Unsaved");
        assert_eq!(t.title_pin.as_deref(), Some("Unsaved"));
        // A listed title that is not the failed pin is ignored while it is held.
        t.set_listed_title("Saved title");
        assert_eq!(t.title, "Unsaved");
        t.restore_title(snap);
        assert_eq!(t.title, "Saved title");
        assert!(t.title_pin.is_none());
        // After rollback the daemon's title is accepted again.
        t.set_listed_title("Saved title");
        assert_eq!(t.title, "Saved title");
        t.set_listed_title("From daemon");
        assert_eq!(t.title, "From daemon");
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
    fn turn_usage_sums_own_streams_onto_the_final_line() {
        let known = |n: i64| serde_json::json!({"n": n, "known": true});
        let mut t = Transcript::new();
        // Turn 1: a tool loop (two streams) plus a child's stream.
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "fix it"})));
        t.apply_envelope(&env("turn.started", serde_json::json!({"sessionId": "root", "turnId": "t1"})));
        t.apply_envelope(&env(
            "usage.reported",
            serde_json::json!({"sessionId": "root", "input": known(1000), "output": known(50)}),
        ));
        t.apply_envelope(&env(
            "usage.reported",
            serde_json::json!({"sessionId": "kid", "input": known(9999), "output": known(9999)}),
        ));
        t.apply_envelope(&env(
            "usage.reported",
            serde_json::json!({"sessionId": "root", "input": known(1500), "output": known(150)}),
        ));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "done"})));
        assert!(t.turn_usage.is_empty(), "nothing until the turn completes");
        t.apply_envelope(&env("turn.completed", serde_json::json!({"turnId": "t1"})));
        let last = t.messages.last().unwrap().id;
        let u = t.usage_for(last).expect("footer on final line");
        assert_eq!(u.label().as_deref(), Some("2.5k in · 200 out"));
        assert_eq!(u.streams, 2);
        assert_eq!(t.turn_usage.len(), 1);
        // Turn 2: usage not reported — no footer, and turn 1's sums do
        // not leak into it.
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "again"})));
        t.apply_envelope(&env("turn.started", serde_json::json!({"sessionId": "root", "turnId": "t2"})));
        t.apply_envelope(&env(
            "usage.reported",
            serde_json::json!({"sessionId": "root", "input": {"known": false}, "output": {"known": false}}),
        ));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "ok"})));
        t.apply_envelope(&env("turn.completed", serde_json::json!({"turnId": "t2"})));
        let last2 = t.messages.last().unwrap().id;
        assert_ne!(last, last2);
        assert!(t.usage_for(last2).is_none());
        assert_eq!(t.turn_usage.len(), 1);
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

    #[test]
    fn quoted_user_message_renders_card_not_block() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "why does it fail"})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "The bug is in parse()."})));
        t.apply_envelope(&env("turn.completed", serde_json::json!({})));
        let strike = t.messages[1].id;
        let (role, anchor, text) = t.quote_source(strike).expect("settled reply is quotable");
        assert_eq!(role, "strike");
        assert_eq!(text, "The bug is in parse().");
        let sent = quote_message::compose(&role, anchor, &text, "fix it");
        t.apply_envelope(&env("user.message", serde_json::json!({"text": sent.clone()})));
        let you = t.messages.last().unwrap();
        assert_eq!(you.body, "fix it");
        assert_eq!(you.retry_text, sent);
        let quote = t.quote_for(you.id).expect("quote parsed").clone();
        assert_eq!(t.resolve_quote(&quote), Some(strike));

        // Reload: the same log replayed into a fresh transcript renders
        // the card from the stored text and still resolves the jump.
        let mut again = Transcript::new();
        for raw in [
            env("user.message", serde_json::json!({"text": "why does it fail"})),
            env("text.delta", serde_json::json!({"text": "The bug is in parse()."})),
            env("turn.completed", serde_json::json!({})),
            env("user.message", serde_json::json!({"text": sent})),
        ] {
            again.apply_envelope(&raw);
        }
        let you = again.messages.last().unwrap();
        let quote = again.quote_for(you.id).expect("quote after reload").clone();
        assert_eq!(again.resolve_quote(&quote), Some(again.messages[1].id));
    }

    #[test]
    fn streaming_reply_is_not_quotable() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "go"})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "partial"})));
        assert!(t.quote_source(t.messages[1].id).is_none());
        assert!(t.quote_source(t.messages[0].id).is_some());
    }

    #[test]
    fn quote_of_a_missing_line_does_not_resolve() {
        let mut t = Transcript::new();
        let sent = quote_message::compose("strike", 99, "gone from this log", "and?");
        t.apply_envelope(&env("user.message", serde_json::json!({"text": sent})));
        let quote = t.quote_for(t.messages[0].id).unwrap().clone();
        assert_eq!(t.resolve_quote(&quote), None);
    }

    #[test]
    fn files_invalidated_adds_one_muted_notice_with_tui_wording() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "edit it"})));
        t.apply_envelope(&env(
            "files.invalidated",
            serde_json::json!({"paths": ["src/a.rs"], "reason": "external"}),
        ));
        assert_eq!(t.messages.len(), 2);
        let notice = &t.messages[1];
        assert_eq!(notice.role, Role::Notice);
        assert_eq!(notice.key, crate::files_invalidated::KIND);
        assert_eq!(notice.body, "files changed — agent will re-read: src/a.rs");
        assert!(notice.badge.is_empty());
    }

    #[test]
    fn files_invalidated_burst_merges_into_one_capped_notice() {
        let mut t = Transcript::new();
        t.apply_envelope(&env(
            "files.invalidated",
            serde_json::json!({"paths": ["a", "b"]}),
        ));
        t.apply_envelope(&env(
            "files.invalidated",
            serde_json::json!({"paths": ["b", "c", "d", "e"]}),
        ));
        assert_eq!(t.messages.len(), 1, "burst must not stack notices");
        assert_eq!(
            t.messages[0].body,
            "files changed — agent will re-read: a, b, c +2 more"
        );
        assert_eq!(t.messages[0].copy_text, "a\nb\nc\nd\ne");
    }

    #[test]
    fn files_invalidated_after_other_lines_starts_a_new_notice() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("files.invalidated", serde_json::json!({"paths": ["a"]})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "done"})));
        t.apply_envelope(&env("files.invalidated", serde_json::json!({"paths": ["b"]})));
        assert_eq!(t.messages.len(), 3);
        assert_eq!(t.messages[2].body, "files changed — agent will re-read: b");
    }

    #[test]
    fn files_invalidated_without_paths_adds_nothing() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("files.invalidated", serde_json::json!({"paths": []})));
        t.apply_envelope(&env("files.invalidated", serde_json::json!({"paths": [""]})));
        assert!(t.messages.is_empty());
    }

    #[test]
    fn session_rewound_drops_the_last_user_turn() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "first"})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "one"})));
        t.apply_envelope(&env("turn.completed", serde_json::json!({})));
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "secnod typo"})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "two"})));
        t.apply_envelope(&env("turn.completed", serde_json::json!({})));
        let last = t.last_user_id().expect("last user");
        assert_eq!(t.sent_text(last).as_deref(), Some("secnod typo"));
        // A child-lineage rewind leaves the root transcript alone.
        t.apply_envelope(&env(
            "session.rewound",
            serde_json::json!({"parentSessionId": "root", "depth": 1}),
        ));
        assert_eq!(t.messages.len(), 4);
        t.apply_envelope(&env("session.rewound", serde_json::json!({"removed": 2})));
        let bodies: Vec<&str> = t.messages.iter().map(|m| m.body.as_str()).collect();
        assert_eq!(bodies, vec!["first", "one"]);
        assert_ne!(t.last_user_id(), Some(last));
        // The resend lands as a fresh turn after the kept history.
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "second fixed"})));
        assert_eq!(t.messages.last().map(|m| m.body.as_str()), Some("second fixed"));
    }

    #[test]
    fn session_rewound_on_empty_transcript_is_a_no_op() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("session.rewound", serde_json::json!({})));
        assert!(t.messages.is_empty());
        assert_eq!(t.last_user_id(), None);
    }

    // #1373: per-turn verification badge.
    fn verify_turn(t: &mut Transcript, prompt: &str, done: Value) -> u32 {
        t.apply_envelope(&env("user.message", serde_json::json!({"text": prompt})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "answer"})));
        let last = t.messages.last().unwrap().id;
        t.apply_envelope(&env("turn.completed", done));
        last
    }

    #[test]
    fn verification_badge_stamps_final_line_from_payload_only() {
        use crate::turn_verify::VerifyState;
        let mut t = Transcript::new();
        let a = verify_turn(
            &mut t,
            "one",
            serde_json::json!({"verification": {"passed": true, "claimed": true, "verified": true}}),
        );
        let b = verify_turn(
            &mut t,
            "two",
            serde_json::json!({"verification": {"passed": false, "claimed": true, "verified": false}}),
        );
        let c = verify_turn(
            &mut t,
            "three",
            serde_json::json!({"verification": {"passed": false, "claimed": false, "verified": true}}),
        );
        let d = verify_turn(&mut t, "four", serde_json::json!({"stopReason": "end_turn"}));
        assert_eq!(t.verify_for(a).unwrap().state, VerifyState::Verified);
        assert_eq!(t.verify_for(b).unwrap().state, VerifyState::Claimed);
        assert_eq!(t.verify_for(c).unwrap().state, VerifyState::Unverified);
        assert!(t.verify_for(d).is_none(), "no payload, no badge");
        assert_eq!(t.turn_verify.len(), 3);
        // Only the final line of each turn carries it.
        for m in &t.messages {
            if ![a, b, c].contains(&m.id) {
                assert!(t.verify_for(m.id).is_none(), "{}", m.id);
            }
        }
    }
    // #1374: live tool.output tail.
    fn begin(t: &mut Transcript, call: &str) -> u32 {
        t.apply_envelope(&env(
            "tool.begin",
            serde_json::json!({"callId": call, "name": "bash", "args": {"command": "make"}}),
        ));
        t.messages.last().unwrap().id
    }

    fn output(t: &mut Transcript, call: &str, data: &str) {
        t.apply_envelope(&env("tool.output", serde_json::json!({"callId": call, "data": data})));
    }

    #[test]
    fn tool_output_appends_to_its_card_until_end() {
        let mut t = Transcript::new();
        let a = begin(&mut t, "a");
        let b = begin(&mut t, "b");
        output(&mut t, "a", "a1\n");
        output(&mut t, "b", "b1\n");
        output(&mut t, "a", "a2\n");
        assert_eq!(t.tail_for(a).unwrap().text, "a1\na2\n");
        assert_eq!(t.tail_for(b).unwrap().text, "b1\n");
        t.apply_envelope(&env(
            "tool.end",
            serde_json::json!({"callId": "a", "name": "bash", "output": "final"}),
        ));
        assert!(t.tail_for(a).is_none(), "tool.end replaces the tail");
        assert!(t.tail_for(b).is_some());
        // A straggler after tool.end does not resurrect a tail.
        output(&mut t, "a", "late\n");
        assert!(t.tail_for(a).is_none());
    }

    #[test]
    fn tool_output_without_matching_card_is_dropped() {
        let cases = [("", "x\n"), ("nope", "x\n")];
        for (call, data) in cases {
            let mut t = Transcript::new();
            let a = begin(&mut t, "a");
            output(&mut t, call, data);
            assert!(t.tail_for(a).is_none(), "call {call:?}");
            assert!(t.tool_tails.is_empty(), "call {call:?}");
        }
    }

    #[test]
    fn tool_output_tail_is_bounded() {
        let mut t = Transcript::new();
        let a = begin(&mut t, "a");
        for i in 0..(crate::tool_tail::TAIL_MAX_LINES + 5) {
            output(&mut t, "a", &format!("{i}\n"));
        }
        let tail = t.tail_for(a).unwrap();
        assert!(tail.truncated);
        assert_eq!(crate::tool_tail::line_count(&tail.text), crate::tool_tail::TAIL_MAX_LINES);
        assert!(tail.text.starts_with("5\n"));
    }

    #[test]
    fn orphaned_call_keeps_its_last_tail() {
        let mut t = Transcript::new();
        let a = begin(&mut t, "a");
        output(&mut t, "a", "last words\n");
        t.settle_orphaned_tools();
        assert_eq!(t.messages[0].badge, NO_RESULT_BADGE);
        assert_eq!(t.tail_for(a).unwrap().text, "last words\n");
    }

    #[test]
    fn pin_anchor_survives_a_replay() {
        let log = [
            env("user.message", serde_json::json!({"text": "why does it fail"})),
            env("text.delta", serde_json::json!({"text": "The bug is in parse()."})),
            env("turn.completed", serde_json::json!({})),
        ];
        let mut t = Transcript::new();
        for raw in &log {
            t.apply_envelope(raw);
        }
        let (anchor, excerpt, _) = t.pin_anchor_at(1).expect("settled reply is pinnable");
        assert_eq!(anchor.role, "strike");
        assert_eq!(excerpt, "The bug is in parse().");
        let pin = message_pins::Pin { anchor, excerpt, at_ms: 0.0 };
        let mut again = Transcript::new();
        for raw in &log {
            again.apply_envelope(raw);
        }
        let rail = again.pin_rail(&[pin]);
        assert_eq!(rail[0].target, Some(again.messages[1].id));
    }

    #[test]
    fn streaming_reply_is_not_pinnable() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "go"})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "partial"})));
        assert!(t.pin_anchor_at(1).is_none());
    }

    // #1365: per-turn file-change summary.
    fn files_turn(t: &mut Transcript, prompt: &str, done: Value) -> u32 {
        t.apply_envelope(&env("user.message", serde_json::json!({"text": prompt})));
        t.apply_envelope(&env("text.delta", serde_json::json!({"text": "answer"})));
        let last = t.messages.last().unwrap().id;
        t.apply_envelope(&env("turn.completed", done));
        last
    }

    #[test]
    fn file_summary_attaches_to_its_own_turn() {
        let mut t = Transcript::new();
        let a = files_turn(
            &mut t,
            "one",
            serde_json::json!({"files": [
                {"path": "a.go", "kind": "update"},
                {"path": "b.go", "kind": "create"}
            ]}),
        );
        let b = files_turn(&mut t, "two", serde_json::json!({"files": []}));
        let c = files_turn(
            &mut t,
            "three",
            serde_json::json!({"files": [{"path": "old.go", "kind": "delete"}]}),
        );
        assert_eq!(t.files_for(a).unwrap().label(), "2 files changed · 1 updated, 1 created");
        assert!(t.files_for(b).is_none(), "empty files renders nothing");
        assert_eq!(t.files_for(c).unwrap().deleted, vec!["old.go"]);
        assert_eq!(t.turn_files.len(), 2);
        for m in &t.messages {
            if m.id != a && m.id != c {
                assert!(t.files_for(m.id).is_none(), "{}", m.id);
            }
        }
    }
}
