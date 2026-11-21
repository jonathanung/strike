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
//! here is inferred from timing. Ordinary queued sends never get a marker:
//! only `turn.steered` creates one.
//!
//! #1860: when the agent calls its `steer_ack` tool, the engine emits
//! `turn.steer_ack {summary, conflict, reason}` answering the oldest applied
//! steer of the turn it has not answered yet. The marker then adds the
//! agent's own "Now: …" line, or a distinct conflict state with the reason.
//! The stop and queue controls stay the actions. With no such event the
//! marker stays at plain "acknowledged": the summary is never invented.
//! Engine and cockpit pair acks with steers the same way (FIFO per turn,
//! dropped at `turn.completed`), so replay reproduces the live view.

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

/// Placeholder in the ack FIFO for an applied steer with no marked line.
const NO_LINE: u32 = u32::MAX;

/// The agent's own words on a steer (`turn.steer_ack`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct AgentAck {
    pub summary: String,
    pub conflict: bool,
    pub reason: String,
}

/// Everything the marker on one steered message shows.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SteerMark {
    pub state: AckState,
    pub agent: Option<AgentAck>,
}

impl SteerMark {
    pub fn key(&self) -> &'static str {
        match &self.agent {
            Some(a) if a.conflict => "conflict",
            _ => self.state.key(),
        }
    }

    pub fn text(&self) -> String {
        match &self.agent {
            Some(a) if a.conflict => {
                let mut out = format!("Steer conflicts with the agent's plan: {}", a.reason);
                if !a.summary.is_empty() {
                    out.push_str(&format!(" Now: {}", a.summary));
                }
                out.push_str(" Stop or queue to change course.");
                out
            }
            Some(a) if !a.summary.is_empty() => format!("{} Now: {}", self.state.text(), a.summary),
            _ => self.state.text().to_string(),
        }
    }
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct SteerAcks {
    by_msg: HashMap<u32, AckState>,
    /// Texts of steers that fell back to the next-turn queue, waiting for
    /// their `user.message`.
    fallback: Vec<String>,
    /// Agent acknowledgements, keyed by the steered message id.
    agent: HashMap<u32, AgentAck>,
    /// Applied steers of the running turn the agent has not answered yet,
    /// oldest first (mirrors the engine's FIFO).
    awaiting: Vec<u32>,
}

impl SteerAcks {
    pub fn get(&self, id: u32) -> Option<AckState> {
        self.by_msg.get(&id).copied()
    }

    /// The full marker for message `id`, if it is a steer.
    pub fn mark(&self, id: u32) -> Option<SteerMark> {
        self.get(id).map(|state| SteerMark {
            state,
            agent: self.agent.get(&id).cloned(),
        })
    }

    /// A `turn.steer_ack` event: answers the oldest unanswered steer.
    pub fn on_steer_ack(&mut self, summary: &str, conflict: bool, reason: &str) {
        let summary = summary.trim();
        let reason = reason.trim();
        if summary.is_empty() && !conflict {
            return;
        }
        if self.awaiting.is_empty() {
            return;
        }
        let id = self.awaiting.remove(0);
        if id == NO_LINE {
            return;
        }
        self.agent.insert(
            id,
            AgentAck {
                summary: summary.to_string(),
                conflict,
                reason: if conflict { reason.to_string() } else { String::new() },
            },
        );
    }

    /// The turn ended: unanswered steers keep their plain marker.
    pub fn on_turn_completed(&mut self) {
        self.awaiting.clear();
    }

    /// A `turn.steered` event. `messages` is the transcript so far.
    pub fn on_steered(&mut self, messages: &[Message], text: &str, mode: &str) {
        let state = match mode {
            "queued_fallback" => {
                if !text.is_empty() {
                    self.fallback.push(text.to_string());
                }
                return;
            }
            "cancel_restart" => AckState::Restarted,
            _ => AckState::Applied,
        };
        // The engine queues every applied steer for an agent ack. Keep the
        // FIFO aligned even when no line can carry the marker (image-only
        // steer, unmatched text): its ack is then consumed and dropped
        // instead of landing on a later steer.
        if text.is_empty() {
            self.awaiting.push(NO_LINE);
            return;
        }
        // The engine emits the steer's user.message right before this
        // event, so the newest matching line without a marker is it.
        if let Some(msg) = messages.iter().rev().find(|m| {
            m.role == Role::You
                && (m.retry_text == text || m.body == text)
                && !self.by_msg.contains_key(&m.id)
        }) {
            self.by_msg.insert(msg.id, state);
            self.awaiting.push(msg.id);
        } else {
            self.awaiting.push(NO_LINE);
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

    fn steer(t: &mut Transcript, text: &str, mode: &str) -> u32 {
        t.apply_envelope(&env("user.message", serde_json::json!({"text": text, "turnId": "t1"})));
        let id = last_you(t);
        t.apply_envelope(&env("turn.steered", serde_json::json!({"text": text, "mode": mode, "turnId": "t1", "targetTurnId": "t1"})));
        id
    }

    #[test]
    fn agent_summary_renders_only_when_emitted() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("turn.started", serde_json::json!({"turnId": "t1"})));
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "build it", "turnId": "t1"})));
        let id = steer(&mut t, "use the v2 api", "boundary");
        let plain = t.steer_acks.mark(id).unwrap();
        assert_eq!(plain.key(), "acknowledged");
        assert_eq!(plain.text(), AckState::Applied.text(), "no event, no summary");

        t.apply_envelope(&env("turn.steer_ack", serde_json::json!({"turnId": "t1", "targetTurnId": "t1", "summary": "switching to v2"})));
        let mark = t.steer_acks.mark(id).unwrap();
        assert_eq!(mark.key(), "acknowledged");
        assert!(mark.text().ends_with("Now: switching to v2"), "{}", mark.text());
    }

    #[test]
    fn conflict_is_distinct_and_acks_pair_in_order() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("turn.started", serde_json::json!({"turnId": "t1"})));
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "refactor auth", "turnId": "t1"})));
        let first = steer(&mut t, "keep the old api", "boundary");
        let second = steer(&mut t, "also add tests", "cancel_restart");
        t.apply_envelope(&env("turn.steer_ack", serde_json::json!({"turnId": "t1", "summary": "keeping v1", "conflict": true, "reason": "the plan removes v1"})));
        t.apply_envelope(&env("turn.steer_ack", serde_json::json!({"turnId": "t1", "summary": "adding tests"})));
        let a = t.steer_acks.mark(first).unwrap();
        assert_eq!(a.key(), "conflict");
        assert!(a.text().contains("the plan removes v1"), "{}", a.text());
        assert!(a.text().contains("Stop or queue"), "{}", a.text());
        let b = t.steer_acks.mark(second).unwrap();
        assert_eq!(b.key(), "acknowledged");
        assert!(b.text().ends_with("Now: adding tests"));
        // A third ack has no steer to answer and changes nothing.
        let before = t.steer_acks.clone();
        t.apply_envelope(&env("turn.steer_ack", serde_json::json!({"turnId": "t1", "summary": "extra"})));
        assert_eq!(t.steer_acks, before);
    }

    #[test]
    fn ack_never_crosses_a_turn_or_marks_ordinary_messages() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("turn.started", serde_json::json!({"turnId": "t1"})));
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "first", "turnId": "t1"})));
        let id = steer(&mut t, "go left", "boundary");
        t.apply_envelope(&env("turn.completed", serde_json::json!({"turnId": "t1"})));
        t.apply_envelope(&env("turn.started", serde_json::json!({"turnId": "t2"})));
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "next", "turnId": "t2"})));
        let ordinary = last_you(&t);
        t.apply_envelope(&env("turn.steer_ack", serde_json::json!({"turnId": "t2", "summary": "late"})));
        assert_eq!(t.steer_acks.mark(id).unwrap().agent, None);
        assert_eq!(t.steer_acks.mark(ordinary), None);
    }

    #[test]
    fn conflict_survives_replay() {
        let log = vec![
            env("turn.started", serde_json::json!({"turnId": "t1"})),
            env("user.message", serde_json::json!({"text": "build it", "turnId": "t1"})),
            env("user.message", serde_json::json!({"text": "use sqlite", "turnId": "t1"})),
            env("turn.steered", serde_json::json!({"text": "use sqlite", "mode": "boundary", "turnId": "t1"})),
            env("turn.steer_ack", serde_json::json!({"turnId": "t1", "summary": "staying on postgres", "conflict": true, "reason": "schema needs jsonb"})),
            env("turn.completed", serde_json::json!({"turnId": "t1"})),
        ];
        let mut replayed = Transcript::new();
        for line in &log {
            replayed.apply_envelope(line);
        }
        let id = last_you(&replayed);
        let mark = replayed.steer_acks.mark(id).unwrap();
        assert_eq!(mark.key(), "conflict");
        assert!(mark.text().contains("schema needs jsonb"));
    }

    #[test]
    fn unmatched_or_empty_steer_marks_nothing() {
        let mut acks = SteerAcks::default();
        acks.on_steered(&[], "", "boundary");
        acks.on_steered(&[], "nobody", "boundary");
        assert_eq!(acks.mark(NO_LINE), None);
        assert!(acks.by_msg.is_empty() && acks.agent.is_empty());
        assert!(AckState::MissedTurn.text().contains("next message"));
    }

    #[test]
    fn image_only_steer_keeps_acks_aligned() {
        let mut t = Transcript::new();
        t.apply_envelope(&env("turn.started", serde_json::json!({"turnId": "t1"})));
        t.apply_envelope(&env("user.message", serde_json::json!({"text": "build it", "turnId": "t1"})));
        // An image-only steer: no text to match a line.
        t.apply_envelope(&env("turn.steered", serde_json::json!({"text": "", "mode": "boundary", "turnId": "t1"})));
        let text_steer = steer(&mut t, "smaller", "boundary");
        t.apply_envelope(&env("turn.steer_ack", serde_json::json!({"turnId": "t1", "summary": "looking at the screenshot"})));
        assert_eq!(t.steer_acks.mark(text_steer).unwrap().agent, None, "first ack answers the image steer");
        t.apply_envelope(&env("turn.steer_ack", serde_json::json!({"turnId": "t1", "summary": "shrinking it"})));
        assert!(t.steer_acks.mark(text_steer).unwrap().text().ends_with("Now: shrinking it"));
    }
}
