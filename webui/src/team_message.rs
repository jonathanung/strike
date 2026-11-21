//! Team message composer (#1573): the human messages a running child, or
//! broadcasts to every member, through the daemon's certified `team.message`
//! / `team.broadcast` ops (docs/human-orchestration-ops.md §7.2-7.3).
//!
//! The confirmation is the daemon's `TeamOpOutcome`, never a local echo:
//! success names the returned message id, and a coded refusal (`not_lead`,
//! `cross_root_denied`, `team_unavailable`, …) renders as that code's
//! meaning. The action is mounted only while bootstrap reports
//! `teamControl` and a live team exists; a child that has gone terminal
//! explains it cannot receive mail instead of sending into the void.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::{json, Value};

use crate::api::{api, auth_header, idempotency_key, js_encode};
use crate::model::ActivityRow;

pub const OP_MESSAGE: &str = "team.message";
pub const OP_BROADCAST: &str = "team.broadcast";

/// Protocol vocabulary for `team.message` `kind`, with display labels.
pub const KINDS: &[(&str, &str)] = &[("message", "Message"), ("request", "Request")];
/// Protocol vocabulary for `urgency`, with display labels.
pub const URGENCIES: &[(&str, &str)] =
    &[("normal", "Normal"), ("high", "High"), ("blocker", "Blocker")];

/// Who the composer sends to. One form, two targets.
#[derive(Clone, Debug, PartialEq)]
pub enum Target {
    Child { session_id: String, name: String },
    All,
}

/// True when a child in this display state can still take mail.
/// Display states come from `model::display_state`; `blocked` is the
/// terminal ChildStatus the daemon reports as a closed mailbox.
pub fn can_receive(state: &str) -> bool {
    !matches!(state, "done" | "failed" | "stopped" | "blocked")
}

/// Why a terminal child cannot be messaged.
pub fn terminal_note(name: &str, state: &str) -> String {
    let how = match state {
        "done" => "has finished",
        "failed" => "has failed",
        "stopped" => "was stopped",
        "blocked" => "ended blocked",
        _ => "is no longer running",
    };
    format!("{name} {how} and cannot receive messages. Retry it to start a new run.")
}

/// Builds the op type and payload for one send. `state` is the child's
/// current display state when known; `key` is the idempotency key.
pub fn build_request(
    target: &Target,
    state: Option<&str>,
    body: &str,
    kind: &str,
    urgency: &str,
    root: &str,
    key: &str,
) -> Result<(&'static str, Value), String> {
    let body = body.trim();
    if body.is_empty() {
        return Err("Write a message first.".to_string());
    }
    let (op, mut data) = match target {
        Target::Child { session_id, name } => {
            if let Some(state) = state {
                if !can_receive(state) {
                    return Err(terminal_note(name, state));
                }
            }
            (
                OP_MESSAGE,
                json!({
                    "to": session_id,
                    "body": body,
                    "kind": kind,
                    "urgency": urgency,
                    "idempotencyKey": key,
                }),
            )
        }
        Target::All => (
            OP_BROADCAST,
            json!({
                "body": body,
                "urgency": urgency,
                "idempotencyKey": key,
            }),
        ),
    };
    if !root.is_empty() {
        data["rootSessionId"] = Value::String(root.to_string());
    }
    Ok((op, data))
}

/// The daemon's meaning for a stable team-control error code.
pub fn code_meaning(code: &str) -> Option<&'static str> {
    Some(match code {
        "not_lead" => "Only the team lead can send this, and this session is not the lead.",
        "cross_root_denied" => {
            "That team belongs to a different root session. Switch to its root to message it."
        }
        "team_unavailable" => "No team is running in this session, so there is no one to message.",
        "idempotency_conflict" => "This send collided with an earlier one. Send it again.",
        "conflict" => "The team changed while sending. Try again.",
        "capability_unavailable: teamControl" => "This daemon does not accept team controls.",
        "attach_only" | "read_only" => "This cockpit is read-only here and cannot send team messages.",
        "permission_denied" => "Permission policy denied this message.",
        _ => return None,
    })
}

#[derive(Debug, Default, Deserialize)]
struct OpReply {
    #[serde(default)]
    error: String,
    #[serde(default)]
    code: String,
    #[serde(default, rename = "messageId")]
    message_id: String,
}

/// What the daemon said about one send.
#[derive(Clone, Debug, PartialEq)]
pub enum SendOutcome {
    Delivered(String),
    Refused { code: String, sentence: String },
}

/// Reads one `/v1/ops` reply for a team message or broadcast.
pub fn read_outcome(status: u16, body: &str, target: &Target) -> SendOutcome {
    let reply: OpReply = serde_json::from_str(body).unwrap_or_default();
    if (200..300).contains(&status) {
        let sentence = match target {
            Target::Child { name, .. } if !reply.message_id.is_empty() => {
                format!("Delivered to {name} (message {}).", reply.message_id)
            }
            Target::Child { name, .. } => format!("Delivered to {name}."),
            Target::All => "Broadcast delivered to the team.".to_string(),
        };
        return SendOutcome::Delivered(sentence);
    }
    let code = reply.code.trim().to_string();
    let error = reply.error.trim();
    let sentence = if let Some(meaning) = code_meaning(&code) {
        meaning.to_string()
    } else if code == "validation" && !error.is_empty() {
        format!("The daemon rejected the message: {error}")
    } else if status == 504 {
        "The daemon did not confirm the message in time. It may still arrive.".to_string()
    } else if !error.is_empty() {
        error.to_string()
    } else {
        format!("HTTP {status}")
    };
    SendOutcome::Refused { code, sentence }
}

fn ops_path(root: &str) -> String {
    if root.is_empty() {
        "/v1/ops".to_string()
    } else {
        format!("/v1/ops?root={}", js_encode(root))
    }
}

/// Posts one op and returns the HTTP status and body unchanged, so coded
/// refusals reach `read_outcome` instead of a flattened error string.
async fn post_team_op(
    base: &str,
    token: &str,
    root: &str,
    op: &str,
    data: Value,
) -> Result<(u16, String), String> {
    let url = api(base, &ops_path(root));
    let mut builder = gloo_net::http::Request::post(&url)
        .credentials(web_sys::RequestCredentials::Include)
        .header("Content-Type", "application/json");
    let token = token.trim();
    if !token.is_empty() {
        builder = builder.header("Authorization", &auth_header(token));
    }
    let resp = builder
        .body(json!({"type": op, "data": data}).to_string())
        .map_err(|e| e.to_string())?
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = resp.status();
    let text = resp.text().await.unwrap_or_default();
    Ok((status, text))
}

fn chip_class(on: bool) -> &'static str {
    if on {
        "chip on"
    } else {
        "chip"
    }
}

#[component]
pub fn TeamMessageComposer(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    activity: RwSignal<Vec<ActivityRow>>,
    target: RwSignal<Option<Target>>,
) -> impl IntoView {
    let body = RwSignal::new(String::new());
    let kind = RwSignal::new(KINDS[0].0.to_string());
    let urgency = RwSignal::new(URGENCIES[0].0.to_string());
    let pending = RwSignal::new(false);
    let outcome = RwSignal::new(None::<SendOutcome>);
    // The child the composer was opened on, so the form can switch between
    // that child and "all members" without losing it.
    let opened_child = RwSignal::new(None::<Target>);

    Effect::new(move |_| {
        let t = target.get();
        outcome.set(None);
        if matches!(t, Some(Target::Child { .. })) {
            opened_child.set(t);
        } else if t.is_none() {
            opened_child.set(None);
            body.set(String::new());
        }
    });
    // A different root is a different team: close rather than aim the
    // draft at members of another session.
    Effect::new(move |prev: Option<String>| {
        let r = root.get();
        if prev.is_some_and(|p| p != r) {
            target.set(None);
        }
        r
    });

    // Live display state of the targeted child, from the polled rows.
    let child_state = move || -> Option<String> {
        let Some(Target::Child { session_id, .. }) = target.get() else {
            return None;
        };
        activity.with(|rows| {
            rows.iter()
                .find(|r| r.session_id == session_id)
                .map(|r| r.state.clone())
        })
    };
    // Memo so the 2s team poll does not rebuild the form (and drop focus)
    // unless the child actually crossed into a terminal state.
    let blocked_note = Memo::new(move |_| -> Option<String> {
        let Some(Target::Child { name, .. }) = target.get() else {
            return None;
        };
        child_state()
            .filter(|s| !can_receive(s))
            .map(|s| terminal_note(&name, &s))
    });

    let send = move || {
        if pending.get_untracked() {
            return;
        }
        let Some(t) = target.get_untracked() else {
            return;
        };
        let state = child_state();
        let r = root.get_untracked();
        let built = build_request(
            &t,
            state.as_deref(),
            &body.get_untracked(),
            &kind.get_untracked(),
            &urgency.get_untracked(),
            &r,
            &idempotency_key("msg"),
        );
        let (op, data) = match built {
            Ok(v) => v,
            Err(sentence) => {
                outcome.set(Some(SendOutcome::Refused {
                    code: String::new(),
                    sentence,
                }));
                return;
            }
        };
        pending.set(true);
        outcome.set(None);
        let (b, tk) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let out = match post_team_op(&b, &tk, &r, op, data).await {
                Ok((status, text)) => read_outcome(status, &text, &t),
                Err(e) => SendOutcome::Refused {
                    code: String::new(),
                    sentence: format!("Could not reach the daemon: {e}"),
                },
            };
            pending.set(false);
            // The human may have switched target or closed meanwhile; an
            // outcome belongs only to the form it was sent from.
            if target.get_untracked().as_ref() != Some(&t) {
                return;
            }
            if matches!(out, SendOutcome::Delivered(_)) {
                body.set(String::new());
            }
            outcome.set(Some(out));
        });
    };

    let close = move || target.set(None);
    let is_all = move || matches!(target.get(), Some(Target::All));
    let title = move || match target.get() {
        Some(Target::Child { name, .. }) => format!("Message {name}"),
        _ => "Message all members".to_string(),
    };

    view! {
        <Show when=move || target.with(|t| t.is_some())>
            <section
                class="team-message"
                data-team-message="open"
                role="group"
                aria-label=title
                on:keydown=move |ev: web_sys::KeyboardEvent| {
                    if ev.key() == "Escape" {
                        ev.prevent_default();
                        close();
                    }
                }
            >
                <header class="team-message-head">
                    <h3>{title}</h3>
                    <button type="button" class="secondary" on:click=move |_| close()>"Close"</button>
                </header>
                <Show when=move || opened_child.with(|c| c.is_some())>
                    <div class="team-message-chips" role="group" aria-label="Send to">
                        <button
                            type="button"
                            data-team-message-target="child"
                            aria-pressed=move || (!is_all()).to_string()
                            class=move || chip_class(!is_all())
                            on:click=move |_| target.set(opened_child.get_untracked())
                        >
                            {move || match opened_child.get() {
                                Some(Target::Child { name, .. }) => name,
                                _ => String::new(),
                            }}
                        </button>
                        <button
                            type="button"
                            data-team-message-target="all"
                            aria-pressed=move || is_all().to_string()
                            class=move || chip_class(is_all())
                            on:click=move |_| target.set(Some(Target::All))
                        >
                            "All members"
                        </button>
                    </div>
                </Show>
                {move || match blocked_note.get() {
                    Some(note) => view! {
                        <p class="team-message-note" data-team-message-terminal="true">{note}</p>
                    }
                    .into_any(),
                    None => view! {
                        <form on:submit=move |ev: web_sys::SubmitEvent| {
                            ev.prevent_default();
                            send();
                        }>
                            <textarea
                                class="team-message-body"
                                aria-label="Message"
                                placeholder=move || if is_all() {
                                    "Tell every member"
                                } else {
                                    "Correction, missing fact, or next step"
                                }
                                prop:value=move || body.get()
                                disabled=move || pending.get()
                                on:input=move |ev| body.set(event_target_value(&ev))
                                on:keydown=move |ev: web_sys::KeyboardEvent| {
                                    if ev.key() == "Enter" && !ev.shift_key() {
                                        ev.prevent_default();
                                        send();
                                    }
                                }
                            ></textarea>
                            <Show when=move || !is_all()>
                                <div class="team-message-chips" role="group" aria-label="Kind">
                                    {KINDS.iter().map(|&(value, label)| view! {
                                        <button
                                            type="button"
                                            data-team-message-kind=value
                                            aria-pressed=move || (kind.get() == value).to_string()
                                            class=move || chip_class(kind.get() == value)
                                            on:click=move |_| kind.set(value.to_string())
                                        >
                                            {label}
                                        </button>
                                    }).collect_view()}
                                </div>
                            </Show>
                            <div class="team-message-chips" role="group" aria-label="Urgency">
                                {URGENCIES.iter().map(|&(value, label)| view! {
                                    <button
                                        type="button"
                                        data-team-message-urgency=value
                                        aria-pressed=move || (urgency.get() == value).to_string()
                                        class=move || chip_class(urgency.get() == value)
                                        on:click=move |_| urgency.set(value.to_string())
                                    >
                                        {label}
                                    </button>
                                }).collect_view()}
                            </div>
                            <div class="team-message-bar">
                                <span class="hint">"Enter sends. Shift+Enter adds a line. Esc closes."</span>
                                <button
                                    class="primary"
                                    type="submit"
                                    data-team-message-send="true"
                                    disabled=move || pending.get()
                                >
                                    {move || if pending.get() { "Sending" } else { "Send" }}
                                </button>
                            </div>
                        </form>
                    }
                    .into_any(),
                }}
                {move || outcome.get().map(|o| match o {
                    SendOutcome::Delivered(sentence) => view! {
                        <p class="team-message-ok" role="status" data-team-message-outcome="delivered">{sentence}</p>
                    }
                    .into_any(),
                    SendOutcome::Refused { code, sentence } => view! {
                        <p class="team-message-error" role="alert" data-team-message-outcome="refused" data-code=code>{sentence}</p>
                    }
                    .into_any(),
                })}
            </section>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn child(name: &str) -> Target {
        Target::Child {
            session_id: format!("{name}-sid"),
            name: name.to_string(),
        }
    }

    #[test]
    fn message_carries_target_root_and_key() {
        let (op, data) = build_request(
            &child("scout"),
            Some("running"),
            "  stop editing main.go ",
            "request",
            "blocker",
            "root-1",
            "msg-1",
        )
        .unwrap();
        assert_eq!(op, OP_MESSAGE);
        assert_eq!(data["to"], "scout-sid");
        assert_eq!(data["body"], "stop editing main.go");
        assert_eq!(data["kind"], "request");
        assert_eq!(data["urgency"], "blocker");
        assert_eq!(data["rootSessionId"], "root-1");
        assert_eq!(data["idempotencyKey"], "msg-1");
    }

    #[test]
    fn broadcast_has_no_recipient_or_kind() {
        let (op, data) =
            build_request(&Target::All, None, "wrap up", "request", "high", "", "msg-2").unwrap();
        assert_eq!(op, OP_BROADCAST);
        assert!(data.get("to").is_none());
        assert!(data.get("kind").is_none());
        assert!(data.get("rootSessionId").is_none());
        assert_eq!(data["urgency"], "high");
        assert_eq!(data["idempotencyKey"], "msg-2");
    }

    #[test]
    fn build_refuses_empty_and_terminal() {
        let cases = [
            (child("a"), Some("running"), "   ", "Write a message first."),
            (
                child("a"),
                Some("done"),
                "hi",
                "a has finished and cannot receive messages. Retry it to start a new run.",
            ),
            (
                child("b"),
                Some("failed"),
                "hi",
                "b has failed and cannot receive messages. Retry it to start a new run.",
            ),
            (
                child("c"),
                Some("stopped"),
                "hi",
                "c was stopped and cannot receive messages. Retry it to start a new run.",
            ),
            (
                child("d"),
                Some("blocked"),
                "hi",
                "d ended blocked and cannot receive messages. Retry it to start a new run.",
            ),
        ];
        for (target, state, body, want) in cases {
            let err = build_request(&target, state, body, "message", "normal", "r", "k")
                .unwrap_err();
            assert_eq!(err, want);
        }
    }

    #[test]
    fn unknown_child_state_is_left_to_the_daemon() {
        assert!(build_request(&child("x"), None, "hi", "message", "normal", "", "k").is_ok());
    }

    #[test]
    fn receivable_states() {
        for s in ["running", "stalled", "idle", "waiting"] {
            assert!(can_receive(s), "{s}");
        }
        for s in ["done", "failed", "stopped", "blocked"] {
            assert!(!can_receive(s), "{s}");
        }
    }

    #[test]
    fn delivered_outcome_names_message_id() {
        assert_eq!(
            read_outcome(200, r#"{"ok":true,"messageId":"m-7"}"#, &child("scout")),
            SendOutcome::Delivered("Delivered to scout (message m-7).".into())
        );
        assert_eq!(
            read_outcome(200, r#"{"ok":true}"#, &child("scout")),
            SendOutcome::Delivered("Delivered to scout.".into())
        );
        assert_eq!(
            read_outcome(200, r#"{"ok":true}"#, &Target::All),
            SendOutcome::Delivered("Broadcast delivered to the team.".into())
        );
    }

    #[test]
    fn coded_refusals_render_their_meaning() {
        let cases = [
            (403, r#"{"error":"not_lead","code":"not_lead"}"#, "not_lead"),
            (
                403,
                r#"{"error":"cross_root_denied","code":"cross_root_denied"}"#,
                "cross_root_denied",
            ),
            (
                409,
                r#"{"error":"no team","code":"team_unavailable"}"#,
                "team_unavailable",
            ),
        ];
        for (status, body, code) in cases {
            let got = read_outcome(status, body, &child("a"));
            assert_eq!(
                got,
                SendOutcome::Refused {
                    code: code.into(),
                    sentence: code_meaning(code).unwrap().into(),
                }
            );
        }
    }

    #[test]
    fn uncoded_refusals_fall_back_honestly() {
        let validation = read_outcome(
            400,
            r#"{"error":"unknown recipient","code":"validation"}"#,
            &child("a"),
        );
        assert_eq!(
            validation,
            SendOutcome::Refused {
                code: "validation".into(),
                sentence: "The daemon rejected the message: unknown recipient".into(),
            }
        );
        let timeout = read_outcome(504, r#"{"error":"team control op timed out"}"#, &Target::All);
        assert!(matches!(timeout, SendOutcome::Refused { ref sentence, .. } if sentence.contains("in time")));
        let raw = read_outcome(500, "", &Target::All);
        assert_eq!(
            raw,
            SendOutcome::Refused {
                code: String::new(),
                sentence: "HTTP 500".into(),
            }
        );
    }

    #[test]
    fn ops_path_scopes_to_root() {
        assert_eq!(ops_path(""), "/v1/ops");
        assert_eq!(ops_path("a b"), "/v1/ops?root=a%20b");
    }
}
