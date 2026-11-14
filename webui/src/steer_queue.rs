//! Steer versus queue for the composer.
//!
//! While a turn is running, Enter follows the saved default (`strike.enterMode`).
//! The other half of the send control does that action once. The queue itself
//! is the engine FIFO (`input.queued` plus edit, cancel, and reorder ops).
//! A context hold uses only numbers the daemon already reported.

use serde_json::Value;

pub const STEER_WAITING: &str = "Waiting to steer this turn.";

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SendWhich {
    Primary,
    Alternate,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct QueueItem {
    pub id: String,
    pub text: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ContextFit {
    pub used: i64,
    pub limit: i64,
    pub estimated: i64,
    pub level: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LiveNote {
    Queue(Vec<QueueItem>),
    Steered { text: String, mode: String },
    TurnStarted,
    TurnFinished,
    Usage { used: i64 },
    Fit { estimated: i64, limit: i64, level: String },
    EngineError { code: String, message: String },
}

pub fn send_op(busy: bool, enter_mode: &str, which: SendWhich) -> &'static str {
    let steer_default = enter_mode == "steer";
    match which {
        SendWhich::Primary if busy && steer_default => "steer",
        SendWhich::Alternate if busy && steer_default => "user.input",
        SendWhich::Alternate => "steer",
        SendWhich::Primary => "user.input",
    }
}

pub fn primary_label(busy: bool, enter_mode: &str) -> &'static str {
    if !busy {
        "Send"
    } else if enter_mode == "steer" {
        "Steer"
    } else {
        "Queue"
    }
}

pub fn alternate_label(busy: bool, enter_mode: &str) -> Option<&'static str> {
    if !busy {
        None
    } else if enter_mode == "steer" {
        Some("Queue")
    } else {
        Some("Steer")
    }
}

pub fn steer_banner(mode: &str) -> &'static str {
    match mode {
        "cancel_restart" => "Steered into this turn. The reply restarted so it could land.",
        "queued_fallback" => "That steer missed the turn and was queued for the next one.",
        _ => "Steered into the running turn.",
    }
}

/// Hold a send when the daemon says the next turn would not fit.
/// Critical is the engine's own fit level. Warn is not a hold.
/// A known window with used or estimated tokens at or over that window is a hold.
/// The client does not invent a limit or a percentage.
pub fn context_blocks_send(fit: &ContextFit) -> bool {
    if fit.level == "critical" {
        return true;
    }
    if fit.limit > 0 && (fit.used >= fit.limit || fit.estimated >= fit.limit) {
        return true;
    }
    false
}

pub fn context_hold_message(fit: &ContextFit) -> String {
    if fit.limit > 0 && fit.estimated >= fit.limit {
        return format!(
            "The daemon says this would not fit ({} estimated tokens, window {}). Trim the message, or send anyway.",
            fit.estimated, fit.limit
        );
    }
    if fit.limit > 0 && fit.used >= fit.limit {
        return format!(
            "The daemon says this would not fit ({} of {} tokens). Trim the message, or send anyway.",
            fit.used, fit.limit
        );
    }
    if fit.level == "critical" && fit.limit > 0 {
        let shown = if fit.estimated > 0 { fit.estimated } else { fit.used };
        return format!(
            "The daemon marked context critical ({} of {} tokens). Trim the message, or send anyway.",
            shown, fit.limit
        );
    }
    if fit.level == "critical" {
        return "The daemon marked context critical. Trim the message, or send anyway.".into();
    }
    String::new()
}

pub fn context_meter(fit: &ContextFit) -> String {
    if fit.limit <= 0 {
        return String::new();
    }
    if fit.used > 0 {
        format!("{} / {} tokens", fit.used, fit.limit)
    } else if fit.estimated > 0 {
        format!("~{} / {} tokens", fit.estimated, fit.limit)
    } else {
        format!("window {} tokens", fit.limit)
    }
}

pub fn apply_usage(fit: &mut ContextFit, used: i64) {
    fit.used = used;
}

pub fn apply_fit_warning(fit: &mut ContextFit, estimated: i64, limit: i64, level: &str) {
    if limit > 0 {
        fit.limit = limit;
    }
    if estimated > 0 {
        fit.estimated = estimated;
    }
    if !level.is_empty() {
        fit.level = level.to_string();
    }
    if fit.used == 0 && estimated > 0 {
        fit.used = estimated;
    }
}

pub fn note_turn_finished(fit: &mut ContextFit) {
    fit.level.clear();
    fit.estimated = 0;
}

pub fn moved_ids(items: &[QueueItem], id: &str, delta: i32) -> Option<Vec<String>> {
    let idx = items.iter().position(|it| it.id == id)?;
    let next = idx as i32 + delta;
    if next < 0 || next >= items.len() as i32 {
        return None;
    }
    let mut ids: Vec<String> = items.iter().map(|it| it.id.clone()).collect();
    ids.swap(idx, next as usize);
    Some(ids)
}

pub fn editing_still_valid(items: &[QueueItem], editing: &str) -> bool {
    !editing.is_empty() && items.iter().any(|it| it.id == editing)
}

pub fn preview(text: &str) -> String {
    let flat = text.split_whitespace().collect::<Vec<_>>().join(" ");
    if flat.chars().count() <= 120 {
        return flat;
    }
    let mut short: String = flat.chars().take(119).collect();
    short.push('…');
    short
}

fn field(data: &Value, key: &str) -> String {
    match data.get(key) {
        Some(Value::String(s)) => s.clone(),
        Some(other) if !other.is_null() => other.to_string(),
        _ => String::new(),
    }
}

fn i64_field(data: &Value, key: &str) -> i64 {
    data.get(key).and_then(|v| v.as_i64()).unwrap_or(0)
}

pub fn notes_from(typ: &str, data: &Value) -> Vec<LiveNote> {
    match typ {
        "input.queued" => {
            let items = data
                .get("items")
                .and_then(|v| v.as_array())
                .map(|rows| {
                    rows.iter()
                        .filter_map(|row| {
                            let id = field(row, "id");
                            if id.is_empty() {
                                None
                            } else {
                                Some(QueueItem {
                                    id,
                                    text: field(row, "text"),
                                })
                            }
                        })
                        .collect()
                })
                .unwrap_or_default();
            vec![LiveNote::Queue(items)]
        }
        "turn.steered" => vec![LiveNote::Steered {
            text: field(data, "text"),
            mode: field(data, "mode"),
        }],
        "turn.started" => vec![LiveNote::TurnStarted],
        "turn.completed" => vec![LiveNote::TurnFinished],
        "usage.reported" => {
            let used = data.get("used").cloned().unwrap_or(Value::Null);
            let known = used.get("known").and_then(|v| v.as_bool()).unwrap_or(false);
            if !known {
                Vec::new()
            } else {
                vec![LiveNote::Usage {
                    used: i64_field(&used, "n"),
                }]
            }
        }
        "context.fit_warning" => vec![LiveNote::Fit {
            estimated: i64_field(data, "estimatedTokens"),
            limit: i64_field(data, "contextLimit"),
            level: field(data, "level"),
        }],
        "engine.error" => vec![LiveNote::EngineError {
            code: field(data, "code"),
            message: field(data, "message"),
        }],
        _ => Vec::new(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn enter_while_busy_follows_the_setting_and_the_other_half_overrides() {
        assert_eq!(send_op(false, "queue", SendWhich::Primary), "user.input");
        assert_eq!(send_op(false, "steer", SendWhich::Primary), "user.input");
        assert_eq!(send_op(false, "queue", SendWhich::Alternate), "steer");
        assert_eq!(send_op(true, "queue", SendWhich::Primary), "user.input");
        assert_eq!(send_op(true, "queue", SendWhich::Alternate), "steer");
        assert_eq!(send_op(true, "steer", SendWhich::Primary), "steer");
        assert_eq!(send_op(true, "steer", SendWhich::Alternate), "user.input");
        assert_eq!(primary_label(false, "steer"), "Send");
        assert_eq!(primary_label(true, "queue"), "Queue");
        assert_eq!(primary_label(true, "steer"), "Steer");
        assert_eq!(alternate_label(false, "queue"), None);
        assert_eq!(alternate_label(true, "queue"), Some("Steer"));
        assert_eq!(alternate_label(true, "steer"), Some("Queue"));
    }

    #[test]
    fn context_hold_uses_daemon_numbers_only() {
        let mut fit = ContextFit::default();
        assert!(!context_blocks_send(&fit));
        assert!(context_hold_message(&fit).is_empty());
        assert!(context_meter(&fit).is_empty());

        apply_usage(&mut fit, 80);
        apply_fit_warning(&mut fit, 80, 100, "warn");
        assert!(!context_blocks_send(&fit));
        assert_eq!(context_meter(&fit), "80 / 100 tokens");

        apply_fit_warning(&mut fit, 96, 100, "critical");
        assert!(context_blocks_send(&fit));
        assert!(context_hold_message(&fit).contains("96"));
        assert!(context_hold_message(&fit).contains("100"));

        note_turn_finished(&mut fit);
        assert!(fit.level.is_empty());
        assert!(!context_blocks_send(&fit));

        fit.used = 100;
        fit.limit = 100;
        assert!(context_blocks_send(&fit));
        assert!(context_hold_message(&fit).contains("100 of 100"));
    }

    #[test]
    fn queue_snapshot_reorder_and_steer_notes() {
        let raw = json!({"items": [{"id": "b", "text": "second"}, {"id": "a", "text": "first"}]});
        let notes = notes_from("input.queued", &raw);
        let LiveNote::Queue(items) = &notes[0] else {
            panic!("queue note");
        };
        assert_eq!(moved_ids(items, "a", -1).unwrap(), vec!["a".to_string(), "b".to_string()]);
        assert!(moved_ids(items, "b", -1).is_none());
        assert!(editing_still_valid(items, "a"));
        assert!(!editing_still_valid(items, "gone"));
        assert!(preview(&"word ".repeat(40)).ends_with('…'));

        let steered = notes_from(
            "turn.steered",
            &json!({"text": "go left", "mode": "queued_fallback"}),
        );
        match &steered[0] {
            LiveNote::Steered { mode, .. } => {
                assert!(steer_banner(mode).contains("queued"));
            }
            _ => panic!("steer"),
        }
        assert!(notes_from("usage.reported", &json!({"used": {"known": false}})).is_empty());
        match &notes_from("usage.reported", &json!({"used": {"n": 12, "known": true}}))[0] {
            LiveNote::Usage { used } => assert_eq!(*used, 12),
            _ => panic!("usage"),
        }
        assert!(matches!(
            notes_from("engine.error", &json!({"code": "queue_full", "message": "full"}))[0],
            LiveNote::EngineError { .. }
        ));
        assert!(notes_from("text.delta", &json!({})).is_empty());
    }
}
