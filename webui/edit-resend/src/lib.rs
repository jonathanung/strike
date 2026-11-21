//! Edit the last message and resend (#1361).
//!
//! With an empty composer, Cmd/Ctrl+Up (or the "Edit & resend" action on
//! the newest user message) loads that message's exact text into the
//! composer. Nothing has changed yet: Esc or clearing the draft abandons
//! the edit with no side effects. Sending the edited text first asks the
//! engine to discard the last turn — the existing `rewind` op, with
//! `restoreFiles` so the workspace agrees with the transcript — and only
//! once the engine confirms with `session.rewound` is the edited text
//! sent as a normal `user.input`. If the rewind is refused, nothing is
//! sent and the draft stays in the composer.
//!
//! The rules live here, with no web dependencies, so they run as host
//! unit tests in CI; the shell maps its transcript rows onto [`Row`].

/// Transcript row roles this feature distinguishes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Role {
    /// A user message (`user.message`).
    You,
    /// An agent reply.
    Strike,
    /// A tool call.
    Tool,
    /// Anything else (child cards, notices, permission rows).
    Other,
}

/// One transcript row, reduced to what the edit rules need.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Row {
    pub id: u32,
    pub role: Role,
    /// The row is a tool call that changes files (edit, write, patch,
    /// move, delete). Only used to word the discard confirm.
    pub file_change: bool,
}

/// Tool names whose calls change workspace files.
pub fn is_file_tool(name: &str) -> bool {
    matches!(
        name,
        "edit" | "write" | "apply_patch" | "move" | "delete" | "notebook_edit"
    )
}

/// Index of the newest user row. `session.rewound` drops it and every
/// row after it, exactly like the engine drops the last user turn from
/// model-facing history.
pub fn rewind_cut(rows: &[Row]) -> Option<usize> {
    rows.iter().rposition(|r| r.role == Role::You)
}

/// Id of the newest user row — the only message slice 1 can edit.
pub fn last_user(rows: &[Row]) -> Option<u32> {
    rewind_cut(rows).map(|i| rows[i].id)
}

/// What resending will discard: the rows after the edited message.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Discard {
    pub replies: usize,
    pub tools: usize,
    pub file_tools: usize,
}

/// Count what follows `target`. `None` unless `target` is the newest
/// user row: an older message is never editable in slice 1.
pub fn discard_after(rows: &[Row], target: u32) -> Option<Discard> {
    let cut = rewind_cut(rows)?;
    if rows[cut].id != target {
        return None;
    }
    let mut d = Discard::default();
    for row in &rows[cut + 1..] {
        match row.role {
            Role::Strike => d.replies += 1,
            Role::Tool => {
                d.tools += 1;
                if row.file_change {
                    d.file_tools += 1;
                }
            }
            Role::You | Role::Other => {}
        }
    }
    Some(d)
}

fn plural(n: usize, one: &str, many: &str) -> String {
    format!("{n} {}", if n == 1 { one } else { many })
}

/// Body of the discard confirm. Names what will be dropped so the
/// operator is never surprised by a revert.
pub fn confirm_body(d: &Discard) -> String {
    let mut parts = Vec::new();
    if d.replies > 0 {
        parts.push(plural(d.replies, "agent reply", "agent replies"));
    }
    if d.tools > 0 {
        parts.push(plural(d.tools, "tool call", "tool calls"));
    }
    let mut out = String::from("Resending replaces your last message. ");
    if parts.is_empty() {
        out.push_str("Nothing has answered it yet; it is removed");
    } else {
        out.push_str("This discards ");
        out.push_str(&parts.join(" and "));
        out.push_str(" that followed it");
    }
    if d.file_tools > 0 {
        out.push_str(&format!(
            ", and reverts the file changes from {}",
            plural(d.file_tools, "file-changing call", "file-changing calls")
        ));
    }
    out.push_str(". Then the edited text is sent.");
    out
}

/// Composer facts that decide whether Cmd/Ctrl+Up recalls.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Gate {
    pub draft_empty: bool,
    /// A turn is running in this session.
    pub busy: bool,
    /// Messages are waiting in the steer/queue.
    pub queued: bool,
    /// An autocomplete or attach picker is open.
    pub picker_open: bool,
    /// An IME composition is in progress.
    pub composing: bool,
}

/// Outcome of a recall request.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Recall {
    /// Load this message into the composer.
    Edit(u32),
    /// Refused, with the reason to show the operator.
    Refused(&'static str),
    /// The key is not ours here; let the composer handle it.
    Inert,
}

pub const BUSY: &str = "Wait for the turn to finish before editing your last message.";
pub const QUEUED: &str = "Clear the queued messages before editing your last message.";
pub const NOTHING: &str = "There is no message of yours to edit yet.";

/// Keyboard recall (Cmd/Ctrl+Up). Inert while a picker is open, during
/// IME composition, or with text in the composer (Up keeps its draft
/// history / caret meaning there).
pub fn recall(gate: Gate, last: Option<u32>) -> Recall {
    if gate.picker_open || gate.composing || !gate.draft_empty {
        return Recall::Inert;
    }
    edit_request(gate.busy, gate.queued, last)
}

/// The hover "Edit & resend" action on message `id`: same guards as the
/// keyboard path, but only for the newest user message.
pub fn action(busy: bool, queued: bool, last: Option<u32>, id: u32) -> Recall {
    if last != Some(id) {
        return Recall::Refused(NOTHING);
    }
    edit_request(busy, queued, last)
}

fn edit_request(busy: bool, queued: bool, last: Option<u32>) -> Recall {
    if busy {
        return Recall::Refused(BUSY);
    }
    if queued {
        return Recall::Refused(QUEUED);
    }
    match last {
        Some(id) => Recall::Edit(id),
        None => Recall::Refused(NOTHING),
    }
}

/// Where an edit stands.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub enum State {
    #[default]
    Idle,
    /// The message `target` is loaded in the composer; nothing changed.
    Editing { target: u32 },
    /// The rewind op is in flight; `text` is sent once it is confirmed.
    Reverting { target: u32, text: String },
}

impl State {
    pub fn editing(&self) -> bool {
        !matches!(self, State::Idle)
    }
}

/// Submit while editing. On `Ok` the caller posts the `rewind` op and
/// keeps the draft in the composer until the engine answers.
pub fn begin(
    state: &State,
    text: &str,
    busy: bool,
    queued: bool,
    last: Option<u32>,
) -> Result<State, &'static str> {
    let State::Editing { target } = state else {
        return Err("Not editing a message.");
    };
    if text.trim().is_empty() {
        return Err("Type the edited message first.");
    }
    if busy {
        return Err(BUSY);
    }
    if queued {
        return Err(QUEUED);
    }
    if last != Some(*target) {
        return Err("Your last message changed; the edit was not sent.");
    }
    Ok(State::Reverting {
        target: *target,
        text: text.trim().to_string(),
    })
}

/// `session.rewound` arrived. Returns the text to send now, only when
/// this edit asked for the rewind.
pub fn on_rewound(state: State) -> (State, Option<String>) {
    match state {
        State::Reverting { text, .. } => (State::Idle, Some(text)),
        other => (other, None),
    }
}

/// Engine errors that answer a `rewind` op (see `handleRewind`).
pub fn is_rewind_error(message: &str) -> bool {
    message.starts_with("cannot rewind")
        || message.starts_with("nothing to rewind")
        || message.starts_with("rewind ")
}

/// The rewind was refused, failed partway, or its post failed: nothing
/// is sent, the draft stays, and the edit returns to `Editing` so the
/// operator can retry or abandon. Returns the note to show.
pub fn on_rewind_failed(state: State, message: &str) -> (State, Option<String>) {
    match state {
        State::Reverting { target, .. } => (
            State::Editing { target },
            Some(format!("Edit not sent: {message}. Your draft is kept.")),
        ),
        other => (other, None),
    }
}

/// Keep an `Editing` state honest against the transcript: when the
/// newest user message is no longer the one being edited (another
/// device sent a turn, the session switched) the edit quietly ends. The
/// draft is left alone. `Reverting` is untouched — the rewind itself
/// removes the target.
pub fn reconcile(state: State, last: Option<u32>) -> State {
    match state {
        State::Editing { target } if last != Some(target) => State::Idle,
        other => other,
    }
}

/// Composer banner while an edit is open. A note (refusal, failure)
/// takes precedence over the state line.
pub fn banner(state: &State, note: &str) -> String {
    if !note.is_empty() {
        return note.to_string();
    }
    match state {
        State::Idle => String::new(),
        State::Editing { .. } => {
            "Editing your last message. Enter resends it; Esc cancels.".to_string()
        }
        State::Reverting { .. } => "Discarding the last turn before resending…".to_string(),
    }
}

/// Esc while editing. Before the confirm it abandons with no side
/// effects and clears the recalled draft. While the rewind is in flight
/// it only stops waiting: the draft is kept, and if the discard lands
/// later nothing is sent. Returns the next state and whether to clear
/// the draft.
pub fn on_escape(state: &State) -> (State, bool) {
    match state {
        State::Editing { .. } => (State::Idle, true),
        State::Reverting { .. } | State::Idle => (State::Idle, false),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn row(id: u32, role: Role) -> Row {
        Row {
            id,
            role,
            file_change: false,
        }
    }

    fn file_tool(id: u32) -> Row {
        Row {
            id,
            role: Role::Tool,
            file_change: true,
        }
    }

    fn turn() -> Vec<Row> {
        vec![
            row(1, Role::You),
            row(2, Role::Strike),
            row(3, Role::You),
            row(4, Role::Strike),
            file_tool(5),
            row(6, Role::Tool),
            row(7, Role::Strike),
            row(8, Role::Other),
        ]
    }

    #[test]
    fn rewind_cut_is_the_newest_user_row() {
        let rows = turn();
        assert_eq!(rewind_cut(&rows), Some(2));
        assert_eq!(last_user(&rows), Some(3));
        assert_eq!(rewind_cut(&[row(1, Role::Strike)]), None);
        assert_eq!(last_user(&[]), None);
    }

    #[test]
    fn discard_counts_exactly_the_rows_after_the_message() {
        let d = discard_after(&turn(), 3).unwrap();
        assert_eq!(
            d,
            Discard {
                replies: 2,
                tools: 2,
                file_tools: 1
            }
        );
        // Older messages are not editable in slice 1.
        assert_eq!(discard_after(&turn(), 1), None);
        assert_eq!(discard_after(&turn(), 99), None);
    }

    #[test]
    fn confirm_body_names_what_is_discarded() {
        let cases = [
            (
                Discard {
                    replies: 1,
                    tools: 0,
                    file_tools: 0,
                },
                "Resending replaces your last message. This discards 1 agent reply that followed it. Then the edited text is sent.",
            ),
            (
                Discard {
                    replies: 2,
                    tools: 3,
                    file_tools: 1,
                },
                "Resending replaces your last message. This discards 2 agent replies and 3 tool calls that followed it, and reverts the file changes from 1 file-changing call. Then the edited text is sent.",
            ),
            (
                Discard::default(),
                "Resending replaces your last message. Nothing has answered it yet; it is removed. Then the edited text is sent.",
            ),
        ];
        for (d, want) in cases {
            assert_eq!(confirm_body(&d), want);
        }
    }

    #[test]
    fn file_tools_are_the_mutating_ones() {
        for name in ["edit", "write", "apply_patch", "move", "delete", "notebook_edit"] {
            assert!(is_file_tool(name), "{name}");
        }
        for name in ["read", "bash", "grep", ""] {
            assert!(!is_file_tool(name), "{name}");
        }
    }

    #[test]
    fn keyboard_recall_gate() {
        let idle = Gate {
            draft_empty: true,
            ..Gate::default()
        };
        let cases = [
            ("idle empty composer", idle, Some(3), Recall::Edit(3)),
            ("no user message", idle, None, Recall::Refused(NOTHING)),
            (
                "busy session is refused",
                Gate { busy: true, ..idle },
                Some(3),
                Recall::Refused(BUSY),
            ),
            (
                "queued steer is refused",
                Gate {
                    queued: true,
                    ..idle
                },
                Some(3),
                Recall::Refused(QUEUED),
            ),
            (
                "draft present keeps Up for history",
                Gate {
                    draft_empty: false,
                    ..idle
                },
                Some(3),
                Recall::Inert,
            ),
            (
                "picker open",
                Gate {
                    picker_open: true,
                    ..idle
                },
                Some(3),
                Recall::Inert,
            ),
            (
                "ime composing",
                Gate {
                    composing: true,
                    ..idle
                },
                Some(3),
                Recall::Inert,
            ),
        ];
        for (name, gate, last, want) in cases {
            assert_eq!(recall(gate, last), want, "{name}");
        }
    }

    #[test]
    fn hover_action_only_on_the_newest_message() {
        assert_eq!(action(false, false, Some(3), 3), Recall::Edit(3));
        assert_eq!(action(false, false, Some(3), 1), Recall::Refused(NOTHING));
        assert_eq!(action(true, false, Some(3), 3), Recall::Refused(BUSY));
        assert_eq!(action(false, true, Some(3), 3), Recall::Refused(QUEUED));
    }

    #[test]
    fn begin_requires_an_idle_session_and_the_same_target() {
        let editing = State::Editing { target: 3 };
        assert_eq!(
            begin(&editing, "  fixed text ", false, false, Some(3)),
            Ok(State::Reverting {
                target: 3,
                text: "fixed text".into()
            })
        );
        assert_eq!(begin(&editing, "x", true, false, Some(3)), Err(BUSY));
        assert_eq!(begin(&editing, "x", false, true, Some(3)), Err(QUEUED));
        assert!(begin(&editing, "   ", false, false, Some(3)).is_err());
        assert!(begin(&editing, "x", false, false, Some(4)).is_err());
        assert!(begin(&State::Idle, "x", false, false, Some(3)).is_err());
    }

    #[test]
    fn rewound_sends_only_for_our_rewind() {
        let reverting = State::Reverting {
            target: 3,
            text: "fixed".into(),
        };
        assert_eq!(on_rewound(reverting), (State::Idle, Some("fixed".into())));
        // A rewind from elsewhere (TUI /rewind, another browser) sends nothing.
        assert_eq!(on_rewound(State::Idle), (State::Idle, None));
        assert_eq!(
            on_rewound(State::Editing { target: 3 }),
            (State::Editing { target: 3 }, None)
        );
    }

    #[test]
    fn failed_rewind_sends_nothing_and_keeps_editing() {
        let reverting = State::Reverting {
            target: 3,
            text: "fixed".into(),
        };
        let (next, note) = on_rewind_failed(reverting, "cannot rewind while a turn is running");
        assert_eq!(next, State::Editing { target: 3 });
        assert_eq!(
            note.as_deref(),
            Some("Edit not sent: cannot rewind while a turn is running. Your draft is kept.")
        );
        // A later rewound (file-restore error is emitted before it) finds
        // no pending text, so nothing is sent.
        assert_eq!(on_rewound(next).1, None);
        assert_eq!(on_rewind_failed(State::Idle, "x"), (State::Idle, None));
    }

    #[test]
    fn rewind_errors_are_recognised() {
        for msg in [
            "cannot rewind while a turn is running",
            "nothing to rewind",
            "rewind file restore: permission denied",
        ] {
            assert!(is_rewind_error(msg), "{msg}");
        }
        for msg in ["no active turn", "rewinding is fun", ""] {
            assert!(!is_rewind_error(msg), "{msg}");
        }
    }

    #[test]
    fn reconcile_ends_a_stale_edit_but_not_a_rewind() {
        assert_eq!(reconcile(State::Editing { target: 3 }, Some(3)), State::Editing { target: 3 });
        assert_eq!(reconcile(State::Editing { target: 3 }, Some(9)), State::Idle);
        assert_eq!(reconcile(State::Editing { target: 3 }, None), State::Idle);
        let reverting = State::Reverting {
            target: 3,
            text: "t".into(),
        };
        assert_eq!(reconcile(reverting.clone(), None), reverting);
        assert_eq!(reconcile(State::Idle, Some(3)), State::Idle);
    }

    #[test]
    fn banner_text() {
        assert_eq!(banner(&State::Idle, ""), "");
        assert!(banner(&State::Editing { target: 1 }, "").starts_with("Editing your last message"));
        let reverting = State::Reverting {
            target: 1,
            text: "t".into(),
        };
        assert!(banner(&reverting, "").starts_with("Discarding"));
        assert_eq!(banner(&State::Idle, BUSY), BUSY);
    }

    #[test]
    fn escape_abandons_without_side_effects() {
        assert_eq!(on_escape(&State::Editing { target: 1 }), (State::Idle, true));
        let reverting = State::Reverting {
            target: 1,
            text: "t".into(),
        };
        let (next, clear) = on_escape(&reverting);
        assert_eq!((next.clone(), clear), (State::Idle, false));
        // After stopping the wait, a late rewound sends nothing.
        assert_eq!(on_rewound(next).1, None);
    }

    #[test]
    fn editing_flag() {
        assert!(!State::Idle.editing());
        assert!(State::Editing { target: 1 }.editing());
        assert!(State::Reverting {
            target: 1,
            text: String::new()
        }
        .editing());
    }
}
