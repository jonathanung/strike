//! Regenerate a session's title from the session list (#1370).
//!
//! The daemon proposes (`POST /v1/sessions/{id}/title/propose`, which never
//! stores anything); this module shows the proposal in the shared confirm
//! dialog and, only on Confirm, writes it through the existing rename route
//! (`PATCH /v1/sessions/{id}`). Cancel issues no request. There is no other
//! write path, so a title — manual or automatic — cannot change without
//! that confirm.
//!
//! Row state while a proposal is in flight, and any refusal or failure,
//! is reported inline on the row ([`RowState`]); the old title stays.

use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;

use crate::api::{patch_json, post_json_value};
use crate::confirm::ConfirmRequest;
use crate::session_delete::encode_segment;

/// The daemon's proposal. `manual` is true when the current title was
/// typed by the user rather than derived from the conversation.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct Proposal {
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub current: String,
    #[serde(default)]
    pub manual: bool,
    #[serde(default)]
    pub unchanged: bool,
}

/// Per-row regenerate state shown under the row.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum RowState {
    /// A proposal request is in flight: the action is disabled.
    Pending,
    /// The daemon refused (empty / single-message session): the action is
    /// disabled with this hint until the list reloads.
    Disabled(String),
    /// An outcome note (failure, already up to date); the action stays
    /// available.
    Note(String),
}

impl RowState {
    pub fn blocks_action(&self) -> bool {
        matches!(self, RowState::Pending | RowState::Disabled(_))
    }

    pub fn text(&self) -> &str {
        match self {
            RowState::Pending => "Regenerating title\u{2026}",
            RowState::Disabled(s) | RowState::Note(s) => s,
        }
    }
}

pub const LABEL: &str = "Regenerate title";
pub const EMPTY_HINT: &str = "No messages yet \u{2014} nothing to title from.";
pub const SINGLE_HINT: &str =
    "Only one message \u{2014} the title already comes from it.";
pub const UP_TO_DATE: &str = "Title is already up to date.";

pub fn propose_path(id: &str) -> String {
    format!("/v1/sessions/{}/title/propose", encode_segment(id))
}

pub fn rename_path(id: &str) -> String {
    format!("/v1/sessions/{}", encode_segment(id))
}

/// A row whose list label fell back to "Untitled session" has no title
/// and no preview: there is no conversation to title from yet, so the
/// action is disabled up front instead of asking the daemon.
pub fn disabled_up_front(label: &str) -> Option<&'static str> {
    (label.trim() == "Untitled session").then_some(EMPTY_HINT)
}

/// Map a failed proposal (`"HTTP <status> <json body>"` from
/// `post_json_value`, or a transport error) to the row state. The two
/// daemon refusals disable the action; anything else is an inline
/// failure note and the old title stays.
pub fn refusal(err: &str) -> RowState {
    let body = err
        .strip_prefix("HTTP ")
        .and_then(|rest| rest.split_once(' '))
        .map(|(_, body)| body)
        .unwrap_or("");
    let code = serde_json::from_str::<serde_json::Value>(body)
        .ok()
        .and_then(|v| v.get("code").and_then(|c| c.as_str()).map(str::to_string))
        .unwrap_or_default();
    match code.as_str() {
        "title_session_empty" => RowState::Disabled(EMPTY_HINT.into()),
        "title_single_message" => RowState::Disabled(SINGLE_HINT.into()),
        _ => RowState::Note(format!(
            "Couldn\u{2019}t regenerate the title ({}). The old title is kept.",
            short_error(err)
        )),
    }
}

fn short_error(err: &str) -> String {
    let err = err.trim();
    if err.is_empty() {
        return "no response".into();
    }
    // Prefer the daemon's message over the raw JSON body.
    if let Some((head, body)) = err.strip_prefix("HTTP ").and_then(|r| r.split_once(' ')) {
        if let Some(msg) = serde_json::from_str::<serde_json::Value>(body)
            .ok()
            .and_then(|v| v.get("error").and_then(|e| e.as_str()).map(str::to_string))
        {
            return format!("HTTP {head}: {msg}");
        }
        return format!("HTTP {head}");
    }
    err.chars().take(120).collect()
}

/// What to do with a proposal: open the confirm (Ok), or report on the row
/// that nothing would change (Err).
pub fn confirm_for(p: &Proposal) -> Result<ConfirmRequest, RowState> {
    if p.unchanged || p.title.trim().is_empty() {
        return Err(RowState::Note(UP_TO_DATE.into()));
    }
    let current = if p.current.trim().is_empty() {
        "Untitled".to_string()
    } else {
        format!("\u{201c}{}\u{201d}", p.current.trim())
    };
    let mut body = format!(
        "Generated from the conversation as it is now. Current title: {current}."
    );
    if p.manual {
        body.push_str(
            " The current title was set manually \u{2014} renaming replaces it.",
        );
    }
    body.push_str(" Cancel keeps the current title.");
    Ok(ConfirmRequest {
        title: format!("Rename to \u{201c}{}\u{201d}?", p.title.trim()),
        body,
        confirm_label: if p.manual {
            "Replace manual title".into()
        } else {
            "Rename".into()
        },
    })
}

/// Run the regenerate flow for one row: propose → confirm → rename.
/// `on_renamed(id, title)` runs only after the daemon stored a confirmed
/// title.
pub fn start(
    base: String,
    token: String,
    id: String,
    states: RwSignal<std::collections::HashMap<String, RowState>>,
    on_renamed: Rc<dyn Fn(String, String)>,
) {
    if states.with_untracked(|m| m.get(&id).is_some_and(RowState::blocks_action)) {
        return;
    }
    states.update(|m| {
        m.insert(id.clone(), RowState::Pending);
    });
    spawn_local(async move {
        let result = post_json_value(&base, &token, &propose_path(&id), serde_json::json!({}))
            .await
            .and_then(|v| serde_json::from_value::<Proposal>(v).map_err(|e| e.to_string()));
        let proposal = match result {
            Ok(p) => p,
            Err(e) => {
                states.update(|m| {
                    m.insert(id.clone(), refusal(&e));
                });
                return;
            }
        };
        let request = match confirm_for(&proposal) {
            Ok(r) => r,
            Err(state) => {
                states.update(|m| {
                    m.insert(id.clone(), state);
                });
                return;
            }
        };
        states.update(|m| {
            m.remove(&id);
        });
        let title = proposal.title.trim().to_string();
        let on_yes: Rc<dyn Fn()> = {
            let (base, token, id) = (base.clone(), token.clone(), id.clone());
            Rc::new(move || {
                let (base, token, id, title) =
                    (base.clone(), token.clone(), id.clone(), title.clone());
                let on_renamed = on_renamed.clone();
                states.update(|m| {
                    m.insert(id.clone(), RowState::Pending);
                });
                spawn_local(async move {
                    match patch_json(
                        &base,
                        &token,
                        &rename_path(&id),
                        serde_json::json!({ "title": title }),
                    )
                    .await
                    {
                        Ok(()) => {
                            states.update(|m| {
                                m.remove(&id);
                            });
                            on_renamed(id, title);
                        }
                        Err(e) => states.update(|m| {
                            m.insert(
                                id.clone(),
                                RowState::Note(format!(
                                    "Rename failed ({}). The old title is kept.",
                                    short_error(&e)
                                )),
                            );
                        }),
                    }
                });
            })
        };
        if !crate::confirm::ask(request, on_yes) {
            states.update(|m| {
                m.insert(id.clone(), RowState::Note(crate::confirm::BUSY.into()));
            });
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    fn proposal(title: &str, current: &str, manual: bool) -> Proposal {
        Proposal {
            title: title.into(),
            current: current.into(),
            manual,
            unchanged: title == current,
        }
    }

    #[test]
    fn paths_encode_the_session_id() {
        assert_eq!(propose_path("a/b?c"), "/v1/sessions/a%2Fb%3Fc/title/propose");
        assert_eq!(rename_path("s.1"), "/v1/sessions/s.1");
    }

    #[test]
    fn proposal_opens_a_confirm_naming_both_titles() {
        let req = confirm_for(&proposal("split the store", "fix login", false)).unwrap();
        assert_eq!(req.title, "Rename to \u{201c}split the store\u{201d}?");
        assert!(req.body.contains("\u{201c}fix login\u{201d}"));
        assert!(req.body.contains("Cancel keeps the current title"));
        assert!(!req.body.contains("manually"));
        assert_eq!(req.confirm_label, "Rename");
    }

    #[test]
    fn manual_title_gets_the_extra_confirm_wording() {
        let req = confirm_for(&proposal("split the store", "My name", true)).unwrap();
        assert!(req.body.contains("set manually"));
        assert_eq!(req.confirm_label, "Replace manual title");
    }

    #[test]
    fn unchanged_or_empty_proposal_never_opens_a_confirm() {
        assert_eq!(
            confirm_for(&proposal("same", "same", false)),
            Err(RowState::Note(UP_TO_DATE.into()))
        );
        assert_eq!(
            confirm_for(&Proposal::default()),
            Err(RowState::Note(UP_TO_DATE.into()))
        );
    }

    #[test]
    fn refusals_disable_the_action_and_failures_keep_the_old_title() {
        assert_eq!(
            refusal(r#"HTTP 422 {"error":"x","code":"title_session_empty"}"#),
            RowState::Disabled(EMPTY_HINT.into())
        );
        assert_eq!(
            refusal(r#"HTTP 422 {"error":"x","code":"title_single_message"}"#),
            RowState::Disabled(SINGLE_HINT.into())
        );
        let RowState::Note(note) = refusal(r#"HTTP 404 {"error":"session not found"}"#) else {
            panic!("404 should be a note");
        };
        assert!(note.contains("HTTP 404: session not found"), "{note}");
        assert!(note.contains("old title is kept"));
        let RowState::Note(note) = refusal("NetworkError when attempting to fetch") else {
            panic!("transport error should be a note");
        };
        assert!(note.contains("NetworkError"));
        assert!(matches!(refusal(""), RowState::Note(_)));
    }

    #[test]
    fn untitled_rows_are_disabled_up_front() {
        assert_eq!(disabled_up_front("Untitled session"), Some(EMPTY_HINT));
        assert_eq!(disabled_up_front("fix login"), None);
    }

    #[test]
    fn only_pending_and_disabled_block_the_action() {
        assert!(RowState::Pending.blocks_action());
        assert!(RowState::Disabled("x".into()).blocks_action());
        assert!(!RowState::Note("x".into()).blocks_action());
        assert_eq!(RowState::Note("n".into()).text(), "n");
    }

    #[test]
    fn proposal_decodes_the_daemon_payload() {
        let p: Proposal = serde_json::from_str(
            r#"{"title":"t","current":"c","manual":true,"unchanged":false,"messages":4}"#,
        )
        .unwrap();
        assert_eq!(p, proposal("t", "c", true));
    }
}
