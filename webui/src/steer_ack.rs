//! Steer acknowledgement in the transcript (#1343).
//!
//! A mid-turn steer reaches the transcript only when the engine applies it:
//! it emits the steer's `user.message` and then a durable `turn.steered`
//! with how it landed. This module turns that event into a persistent
//! marker on the steered message, so it survives reload and replay:
//!
//! - `boundary`: acknowledged. The steer was put into history before the
//!   agent's next model call, so its next step sees it.
//! - `cancel_restart`: acknowledged. The in-flight reply was restarted with
//!   the steer appended.
//! - `queued_fallback`: the turn ended before a boundary. The engine queued
//!   the text as the next message, so the marker lands on that later
//!   `user.message` instead.
//!
//! The pending state (sent, not yet applied) is the composer's steer note:
//! the steer has no transcript line until the engine applies it. Nothing
//! here is inferred from timing. A "what changed" summary or a conflict
//! needs an agent-emitted event that does not exist yet (see
//! docs/webui-planned.md), so the marker never invents one. Ordinary
//! queued sends never get a marker: only `turn.steered` creates one.

use std::collections::HashMap;

use crate::transcript::{Message, Role};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AckState {
    Applied,
    Restarted,
    MissedTurn,
}

impl AckState {
    pub fn key(self) -> &'static str {
        match self {
            AckState::Applied | AckState::Restarted => "acknowledged",
            AckState::MissedTurn => "missed",
        }
    }

    pub fn text(self) -> &'static str {
        match self {
            AckState::Applied => "Steer acknowledged. The agent has it from its next step.",
            AckState::Restarted => "Steer acknowledged. The reply restarted with it.",
            AckState::MissedTurn => {
                "This steer missed the running turn and was sent as the next message."
            }
        }
    }
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct SteerAcks {
    by_msg: HashMap<u32, AckState>,
    /// Texts of steers that fell back to the next-turn queue, waiting for
    /// their `user.message`.
    fallback: Vec<String>,
}

impl SteerAcks {
    pub fn get(&self, id: u32) -> Option<AckState> {
        self.by_msg.get(&id).copied()
    }

    /// A `turn.steered` event. `messages` is the transcript so far.
    pub fn on_steered(&mut self, messages: &[Message], text: &str, mode: &str) {
        if text.is_empty() {
            return;
        }
        let state = match mode {
            "queued_fallback" => {
                self.fallback.push(text.to_string());
                return;
            }
            "cancel_restart" => AckState::Restarted,
            _ => AckState::Applied,
        };
        // The engine emits the steer's user.message right before this
        // event, so the newest matching line without a marker is it.
        if let Some(msg) = messages.iter().rev().find(|m| {
            m.role == Role::You
                && (m.retry_text == text || m.body == text)
                && !self.by_msg.contains_key(&m.id)
        }) {
            self.by_msg.insert(msg.id, state);
        }
    }

    /// A `user.message` was pushed as message `id`. Marks it when it is a
    /// steer that fell back to the queue.
    pub fn on_user_message(&mut self, id: u32, text: &str, shown: &str) {
        if let Some(pos) = self.fallback.iter().position(|t| t == text || t == shown) {
            self.fallback.remove(pos);
            self.by_msg.insert(id, AckState::MissedTurn);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::transcript::Transcript;

    fn env(typ: &str, data: serde_json::Value) -> String {
        serde_json::json!({"type": typ, "data": data, "time": "2026-11-20T10:00:00Z"}).to_string()
    }

    fn last_you(t: &Transcript) -> u32 {
        t.messages.iter().rev().find(|m| m.role == Role::You).unwrap().id
    }

    #[test]
    fn boundary_and_restart_steers_are_acknowledged_in_order() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("turn.started", serde_json::json!({"turnId": "t1"})));
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "fix the bug", "turnId": "t1"})));
        let first = last_you(&t);
        assert_eq!(t.steer_acks.get(first), None, "an ordinary send has no marker");

        // Steer lands at a boundary: user.message, then turn.steered.
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "use the v2 api", "turnId": "t1"})));
        let steer = last_you(&t);
        assert_eq!(t.steer_acks.get(steer), None, "pending until turn.steered");
        t.apply_envelope(&env("turn.steered", serde_json::json!({"text": "use the v2 api", "mode": "boundary", "turnId": "t1", "targetTurnId": "t1"})));
        assert_eq!(t.steer_acks.get(steer), Some(AckState::Applied));
        assert_eq!(t.steer_acks.get(first), None);

        t.apply_envelope(&env("user.message", serde_json::json!({"text": "stop, add tests", "turnId": "t1"})));
        let restart = last_you(&t);
        t.apply_envelope(&env("turn.steered", serde_json::json!({"text": "stop, add tests", "mode": "cancel_restart", "turnId": "t1"})));
        assert_eq!(t.steer_acks.get(restart), Some(AckState::Restarted));
        assert_eq!(AckState::Restarted.key(), "acknowledged");
    }

    #[test]
    fn queued_messages_get_no_marker_and_fallback_marks_the_next_message() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("turn.started", serde_json::json!({"turnId": "t1"})));
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "first", "turnId": "t1"})));
        // Turn ends before a boundary: the steer falls back to the queue.
        t.apply_envelope(&env("turn.steered", serde_json::json!({"text": "go left", "mode": "queued_fallback", "targetTurnId": "t1"})));
        t.apply_envelope(&env("turn.completed", serde_json::json!({"turnId": "t1"})));
        // An ordinary queued send runs first and is not marked.
        t.apply_envelope(&env("turn.started", serde_json::json!({"turnId": "t2"})));
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "queued thing", "turnId": "t2"})));
        assert_eq!(t.steer_acks.get(last_you(&t)), None);
        t.apply_envelope(&env("turn.completed", serde_json::json!({"turnId": "t2"})));
        t.apply_envelope(&env("turn.started", serde_json::json!({"turnId": "t3"})));
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "go left", "turnId": "t3"})));
        assert_eq!(t.steer_acks.get(last_you(&t)), Some(AckState::MissedTurn));
        // Only once: a later identical send is ordinary.
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "go left", "turnId": "t3"})));
        assert_eq!(t.steer_acks.get(last_you(&t)), None);
    }

    #[test]
    fn marker_survives_replay() {
        let log = vec![
            env("turn.started", serde_json::json!({"turnId": "t1"})),
            env("user.message", serde_json::json!({"text": "build it", "turnId": "t1"})),
            env("user.message", serde_json::json!({"text": "smaller please", "turnId": "t1"})),
            env("turn.steered", serde_json::json!({"text": "smaller please", "mode": "boundary", "turnId": "t1"})),
            env("turn.completed", serde_json::json!({"turnId": "t1"})),
        ];
        let mut live = Transcript::new();
        for line in &log {
            live.apply_envelope(line);
        }
        let mut replayed = Transcript::new();
        for line in &log {
            replayed.apply_envelope(line);
        }
        let id = last_you(&replayed);
        assert_eq!(replayed.steer_acks.get(id), Some(AckState::Applied));
        assert_eq!(live.steer_acks, replayed.steer_acks);
    }

    #[test]
    fn unmatched_or_empty_steer_marks_nothing() {
        let mut acks = SteerAcks::default();
        acks.on_steered(&[], "", "boundary");
        acks.on_steered(&[], "nobody", "boundary");
        assert_eq!(acks, SteerAcks::default());
        assert!(AckState::MissedTurn.text().contains("next message"));
    }
}
