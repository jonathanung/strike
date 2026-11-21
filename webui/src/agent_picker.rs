//! Agent picker (#1563): which selectable agent the live session runs.
//!
//! The daemon sends the selectable agents in `GET /v1/bootstrap`
//! (`agents`) and the running agent in the status (`status.agent`). The
//! cockpit switches with the `select.agent` op and shows the new agent only
//! after the daemon's `agent.selected` event confirms it. Clicking never
//! renames the chip by itself.
//!
//! The palette command (`shell/contrib/agent_picker.rs`) reads the agent
//! list and the pick callback from the thread-locals below, which `App`
//! fills, the same late binding `shell::host` uses for the model picker.

use std::cell::RefCell;
use std::rc::Rc;

use serde_json::Value;

use crate::model::AgentInfo;
use crate::model_menu::rfc3339_millis;
use crate::shell::Choice;

/// Label prefix for palette rows. The contrib file and the browser smoke
/// check match on it.
pub const CHOICE_PREFIX: &str = "Switch agent: ";

thread_local! {
    static AGENTS: RefCell<Vec<AgentInfo>> = const { RefCell::new(Vec::new()) };
    static CURRENT: RefCell<String> = const { RefCell::new(String::new()) };
    static PICK: RefCell<Option<Rc<dyn Fn(&str)>>> = RefCell::new(None);
}

pub fn set_agents(agents: Vec<AgentInfo>) {
    AGENTS.with(|slot| *slot.borrow_mut() = agents);
}

pub fn agents() -> Vec<AgentInfo> {
    AGENTS.with(|slot| slot.borrow().clone())
}

/// True when the daemon reported at least one selectable agent. The palette
/// command and the composer chip are hidden otherwise, so an empty list
/// never shows an empty menu that looks broken.
pub fn has_agents() -> bool {
    AGENTS.with(|slot| !slot.borrow().is_empty())
}

/// The daemon-confirmed agent, for marking the current palette row.
pub fn set_current(name: &str) {
    CURRENT.with(|slot| *slot.borrow_mut() = name.to_string());
}

pub fn current() -> String {
    CURRENT.with(|slot| slot.borrow().clone())
}

pub fn set_picker(pick: Rc<dyn Fn(&str)>) {
    PICK.with(|slot| *slot.borrow_mut() = Some(pick));
}

/// Palette rows for the agents the daemon reported, in daemon order
/// (default first). Agents with an empty name are dropped: they cannot be
/// sent as `select.agent`.
pub fn choices(agents: &[AgentInfo], current: &str) -> Vec<Choice> {
    agents
        .iter()
        .filter(|agent| !agent.name.trim().is_empty())
        .map(|agent| {
            let mut detail = agent.description.trim().to_string();
            if agent.name == current {
                detail = if detail.is_empty() {
                    "Current agent".to_string()
                } else {
                    format!("Current agent · {detail}")
                };
            }
            Choice {
                id: agent.name.clone(),
                label: format!("{CHOICE_PREFIX}{}", agent.name),
                detail,
            }
        })
        .collect()
}

/// Check a palette submission against the agent list. The palette passes
/// the typed text when no row matches; an unknown name is refused here
/// with a reason instead of reaching the daemon.
pub fn resolve_pick(raw: &str, agents: &[AgentInfo]) -> Result<String, String> {
    let name = raw.trim();
    let name = name.strip_prefix(CHOICE_PREFIX).unwrap_or(name).trim();
    if name.is_empty() {
        return Err("Pick an agent.".into());
    }
    if agents.is_empty() {
        return Err("No agents are defined for this session.".into());
    }
    if let Some(agent) = agents.iter().find(|a| a.name == name) {
        return Ok(agent.name.clone());
    }
    if let Some(agent) = agents.iter().find(|a| a.name.eq_ignore_ascii_case(name)) {
        return Ok(agent.name.clone());
    }
    Err(format!("\"{name}\" is not a selectable agent. Pick a listed agent."))
}

/// Palette entry point: resolve, then hand the name to the page's picker.
pub fn pick(raw: &str) -> Result<(), String> {
    let name = resolve_pick(raw, &agents())?;
    let pick = PICK
        .with(|slot| slot.borrow().clone())
        .ok_or_else(|| "Agent picker is not ready.".to_string())?;
    pick(&name);
    Ok(())
}

/// Composer chip text. An unknown current agent is shown as unknown, never
/// guessed from the list.
pub fn chip_label(current: &str) -> String {
    let current = current.trim();
    if current.is_empty() {
        "Agent · unknown".into()
    } else {
        format!("Agent · {current}")
    }
}

pub fn chip_aria(current: &str) -> String {
    let current = current.trim();
    if current.is_empty() {
        "Agent: not reported yet. Change agent".into()
    } else {
        format!("Agent: {current}. Change agent")
    }
}

/// What the picker shows: the confirmed agent, plus a note for a refused
/// switch. `note_after` is the unix-ms time the last `select.agent` was
/// sent (0 when nothing is pending), so only an `engine.error` that
/// arrives after our own request turns into a note.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct AgentState {
    pub current: String,
    pub note: String,
    pub note_after: i64,
}

/// Apply one SSE/JSONL envelope. `agent.selected` is the only event that
/// changes `current`, so the chip follows the daemon, including replays.
pub fn reduce_envelope(mut state: AgentState, raw: &str) -> AgentState {
    let v: Value = match serde_json::from_str(raw) {
        Ok(v) => v,
        Err(_) => return state,
    };
    let typ = v.get("type").and_then(|t| t.as_str()).unwrap_or("");
    let data = v.get("data").cloned().unwrap_or(Value::Null);
    let fresh = v
        .get("time")
        .and_then(|t| t.as_str())
        .and_then(rfc3339_millis)
        .is_some_and(|t| state.note_after > 0 && t >= state.note_after);
    match typ {
        "agent.selected" => {
            if let Some(name) = data.get("name").and_then(|n| n.as_str()) {
                if !name.trim().is_empty() {
                    state.current = name.to_string();
                }
            }
            if fresh {
                state.note.clear();
                state.note_after = 0;
            }
        }
        "engine.error" => {
            if fresh {
                let message = data
                    .get("message")
                    .and_then(|m| m.as_str())
                    .unwrap_or_default();
                if message.to_ascii_lowercase().contains("agent") {
                    state.note = message.to_string();
                    state.note_after = 0;
                }
            }
        }
        _ => {}
    }
    state
}

#[cfg(test)]
mod tests {
    use super::*;

    fn agent(name: &str, description: &str) -> AgentInfo {
        AgentInfo {
            name: name.into(),
            description: description.into(),
        }
    }

    #[test]
    fn choices_list_agents_in_order_and_mark_current() {
        let list = vec![agent("build", ""), agent("plan", "Read-only planning"), agent(" ", "")];
        let rows = choices(&list, "plan");
        assert_eq!(rows.len(), 2);
        assert_eq!(rows[0].id, "build");
        assert_eq!(rows[0].label, "Switch agent: build");
        assert_eq!(rows[0].detail, "");
        assert_eq!(rows[1].id, "plan");
        assert_eq!(rows[1].detail, "Current agent · Read-only planning");
        assert_eq!(choices(&list, "build")[0].detail, "Current agent");
        assert!(choices(&[], "build").is_empty());
    }

    #[test]
    fn resolve_pick_accepts_listed_names_only() {
        let list = vec![agent("build", ""), agent("Plan", "")];
        let cases: &[(&str, Result<&str, &str>)] = &[
            ("build", Ok("build")),
            ("  build ", Ok("build")),
            ("Switch agent: build", Ok("build")),
            ("plan", Ok("Plan")),
            ("", Err("Pick an agent.")),
            ("nope", Err("\"nope\" is not a selectable agent. Pick a listed agent.")),
        ];
        for (raw, want) in cases {
            let got = resolve_pick(raw, &list);
            match want {
                Ok(name) => assert_eq!(got.as_deref(), Ok(*name), "raw={raw:?}"),
                Err(msg) => assert_eq!(got.as_deref(), Err(*msg), "raw={raw:?}"),
            }
        }
        assert_eq!(
            resolve_pick("build", &[]),
            Err("No agents are defined for this session.".to_string())
        );
    }

    #[test]
    fn chip_never_guesses_the_current_agent() {
        assert_eq!(chip_label(""), "Agent · unknown");
        assert_eq!(chip_label("build"), "Agent · build");
        assert_eq!(chip_aria(""), "Agent: not reported yet. Change agent");
        assert_eq!(chip_aria("plan"), "Agent: plan. Change agent");
    }

    #[test]
    fn agent_selected_sets_current_and_clears_pending_note() {
        let sent = rfc3339_millis("2026-11-20T12:00:00Z").unwrap();
        let state = AgentState {
            current: "build".into(),
            note: "old".into(),
            note_after: sent,
        };
        let next = reduce_envelope(
            state,
            r#"{"type":"agent.selected","time":"2026-11-20T12:00:01Z","data":{"name":"plan"}}"#,
        );
        assert_eq!(next.current, "plan");
        assert_eq!(next.note, "");
        assert_eq!(next.note_after, 0);
    }

    #[test]
    fn replayed_agent_selected_updates_current_but_keeps_pending() {
        let sent = rfc3339_millis("2026-11-20T12:00:00Z").unwrap();
        let state = AgentState {
            current: String::new(),
            note: String::new(),
            note_after: sent,
        };
        let next = reduce_envelope(
            state,
            r#"{"type":"agent.selected","time":"2026-11-20T11:00:00Z","data":{"name":"build"}}"#,
        );
        assert_eq!(next.current, "build");
        assert_eq!(next.note_after, sent);
    }

    #[test]
    fn engine_error_becomes_a_note_only_when_fresh_and_about_agents() {
        let sent = rfc3339_millis("2026-11-20T12:00:00Z").unwrap();
        let pending = AgentState {
            current: "build".into(),
            note: String::new(),
            note_after: sent,
        };
        let busy = r#"{"type":"engine.error","time":"2026-11-20T12:00:01Z","data":{"message":"cannot switch agents while a turn is running"}}"#;
        let next = reduce_envelope(pending.clone(), busy);
        assert_eq!(next.current, "build");
        assert_eq!(next.note, "cannot switch agents while a turn is running");
        assert_eq!(next.note_after, 0);

        let old = r#"{"type":"engine.error","time":"2026-11-20T11:59:00Z","data":{"message":"unknown agent \"x\""}}"#;
        assert_eq!(reduce_envelope(pending.clone(), old), pending);

        let other = r#"{"type":"engine.error","time":"2026-11-20T12:00:01Z","data":{"message":"unknown provider"}}"#;
        assert_eq!(reduce_envelope(pending.clone(), other), pending);

        let idle = AgentState {
            note_after: 0,
            ..pending
        };
        assert_eq!(reduce_envelope(idle.clone(), busy), idle);
    }

    #[test]
    fn ignores_garbage_and_unrelated_events() {
        let state = AgentState {
            current: "build".into(),
            ..Default::default()
        };
        assert_eq!(reduce_envelope(state.clone(), "not json"), state);
        assert_eq!(
            reduce_envelope(state.clone(), r#"{"type":"model.selected","data":{"model":"x"}}"#),
            state
        );
        assert_eq!(
            reduce_envelope(state.clone(), r#"{"type":"agent.selected","data":{"name":""}}"#),
            state
        );
    }
}
