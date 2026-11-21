//! Read-only cross-gateway peek in the Sessions list (#1539, slice 1).
//!
//! Under the active gateway's sessions (the Local group) the list shows
//! one collapsible group per *saved* gateway profile, each with a
//! reachability badge in the switcher's own vocabulary (connected /
//! reconnecting / unreachable / unauthorized) and, where that gateway's
//! stored token can read it, running + waiting-on-you counts.
//!
//! Strictly read-only: per peer the peek issues at most the switcher's
//! probe (`GET /health`, `GET /v1/gateway`) plus one `GET /v1/sessions`,
//! each with that profile's OWN stored token (never the active
//! gateway's). Nothing polls: groups refresh when the list mounts, when
//! the active gateway changes, when the saved-profile list changes, and
//! on the manual Refresh action. Going
//! to another gateway is the existing guarded switch (`gateway::switch_to`).

use std::collections::HashSet;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde_json::Value;

use crate::api::{api, auth_header};
use crate::gateway::{load_profiles, profiles_signal, session_token, switch_to, Profile};
use crate::status::StatusKind;

/// Every path the peek may request from a non-active gateway, in order.
/// All are GETs; anything else is out of scope for slice 1.
pub(crate) const PEEK_PATHS: [&str; 3] = ["/health", "/v1/gateway", "/v1/sessions"];

/// What one peer gateway looks like from here.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum Peek {
    /// Probe in flight.
    Checking,
    /// Reachable, and its sessions list was readable.
    Counts { running: usize, waiting: usize },
    /// Reachable, but the sessions read was not possible; `reason` says
    /// why. Never shown as a zero count.
    BadgeOnly { reason: String },
    Unreachable,
    Unauthorized,
}

impl Peek {
    /// Badge state id: the switcher's existing vocabulary, no new states.
    pub(crate) fn badge(&self) -> &'static str {
        match self {
            Peek::Checking => "reconnecting",
            Peek::Counts { .. } | Peek::BadgeOnly { .. } => "connected",
            Peek::Unreachable => "unreachable",
            Peek::Unauthorized => "unauthorized",
        }
    }

    pub(crate) fn badge_label(&self) -> &'static str {
        match self {
            Peek::Checking => "Reconnecting",
            Peek::Counts { .. } | Peek::BadgeOnly { .. } => "Connected",
            Peek::Unreachable => "Unreachable",
            Peek::Unauthorized => "Unauthorized",
        }
    }

    /// One-line group summary next to the badge.
    pub(crate) fn summary(&self) -> String {
        match self {
            Peek::Checking => "Checking…".into(),
            Peek::Counts { running, waiting } => {
                format!("{running} running · {waiting} waiting on you")
            }
            Peek::BadgeOnly { .. } => "Counts unavailable".into(),
            Peek::Unreachable => "Not reachable from this browser".into(),
            Peek::Unauthorized => "Needs its own token or pairing".into(),
        }
    }

    /// The expanded group's explanation.
    pub(crate) fn detail(&self) -> String {
        match self {
            Peek::Checking => "Checking this gateway…".into(),
            Peek::Counts { .. } => {
                "Counts come from that gateway's session list. Switch to see the sessions.".into()
            }
            Peek::BadgeOnly { reason } => reason.clone(),
            Peek::Unreachable => {
                "Could not reach it. Check that its daemon is running, or forward it with strike connect, then Refresh.".into()
            }
            Peek::Unauthorized => {
                "It refused this browser. Scan its QR code from the Phone dialog or use strike connect on its host to add a token.".into()
            }
        }
    }
}

/// One reply as the peek sees it: `Err(())` is a network failure.
pub(crate) type Reply = Result<(u16, String), ()>;

/// Next action for a peer given the replies so far.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum Step {
    Fetch(&'static str),
    Done(Peek),
}

/// The peek as a pure state machine over stubbed or real replies, so
/// the request set is testable: it only ever asks for [`PEEK_PATHS`],
/// in order, and stops as soon as the outcome is known.
pub(crate) fn next_step(replies: &[Reply]) -> Step {
    match replies {
        [] => Step::Fetch(PEEK_PATHS[0]),
        [Err(())] => Step::Done(Peek::Unreachable),
        [Ok((code, _))] if *code != 200 => Step::Done(Peek::Unreachable),
        [_] => Step::Fetch(PEEK_PATHS[1]),
        [_, Err(())] => Step::Done(Peek::Unreachable),
        // Same mapping as the switcher's probe: anything but 200 on the
        // gateway check is "unauthorized" there.
        [_, Ok((code, _))] if *code != 200 => Step::Done(Peek::Unauthorized),
        [_, _] => Step::Fetch(PEEK_PATHS[2]),
        [_, _, sessions, ..] => Step::Done(sessions_outcome(sessions)),
    }
}

fn sessions_outcome(reply: &Reply) -> Peek {
    match reply {
        Err(()) => Peek::BadgeOnly {
            reason: "It answered the probe, then its session list could not be read. Refresh to try again.".into(),
        },
        Ok((401, _)) | Ok((403, _)) => Peek::BadgeOnly {
            reason: "This browser's credential for that gateway cannot read its session list (for example a paired-browser grant), so only its reachability is shown.".into(),
        },
        Ok((200, body)) => match count_sessions(body) {
            Some((running, waiting)) => Peek::Counts { running, waiting },
            None => Peek::BadgeOnly {
                reason: "Its session list answered in a shape this cockpit does not read, so no counts are shown.".into(),
            },
        },
        Ok((code, _)) => Peek::BadgeOnly {
            reason: format!("Its session list returned HTTP {code}, so no counts are shown."),
        },
    }
}

/// Running and waiting-on-you counts from a `GET /v1/sessions` body.
/// None when the body is not a sessions list.
pub(crate) fn count_sessions(body: &str) -> Option<(usize, usize)> {
    let v: Value = serde_json::from_str(body).ok()?;
    let rows = v.get("sessions")?.as_array()?;
    let mut running = 0;
    let mut waiting = 0;
    for row in rows {
        let status = row.get("status").and_then(|s| s.as_str()).unwrap_or("");
        match StatusKind::parse(status) {
            StatusKind::Running => running += 1,
            StatusKind::Waiting => waiting += 1,
            _ => {}
        }
    }
    Some((running, waiting))
}

fn norm(base: &str) -> &str {
    base.trim().trim_end_matches('/')
}

/// Saved profiles other than the gateway this tab is using. A blank
/// active base means same-origin, so a profile at the page origin is
/// the active gateway too.
pub(crate) fn peers(profiles: &[Profile], active_base: &str, page_origin: &str) -> Vec<Profile> {
    let active = norm(active_base);
    let origin = norm(page_origin);
    profiles
        .iter()
        .filter(|p| {
            let b = norm(&p.base);
            if b.is_empty() {
                return false;
            }
            if active.is_empty() {
                origin.is_empty() || b != origin
            } else {
                b != active
            }
        })
        .cloned()
        .collect()
}

fn page_origin() -> String {
    web_sys::window()
        .and_then(|w| w.location().origin().ok())
        .unwrap_or_default()
}

async fn peek_get(base: &str, token: &str, path: &str) -> Reply {
    let mut req = gloo_net::http::Request::get(&api(base, path));
    if !token.is_empty() {
        req = req.header("Authorization", &auth_header(token));
    }
    let resp = req.send().await.map_err(|_| ())?;
    let code = resp.status();
    let text = resp.text().await.unwrap_or_default();
    Ok((code, text))
}

async fn run_peek(base: String, token: String) -> Peek {
    let mut replies: Vec<Reply> = Vec::with_capacity(PEEK_PATHS.len());
    loop {
        match next_step(&replies) {
            Step::Done(state) => return state,
            Step::Fetch(path) => replies.push(peek_get(&base, &token, path).await),
        }
    }
}

#[component]
pub(crate) fn GatewayPeek(
    base: RwSignal<String>,
    /// Set to the number of peer groups so the panel can label its own
    /// list as the Local group only when there is something else.
    peer_count: RwSignal<usize>,
) -> impl IntoView {
    let rows = RwSignal::new(Vec::<(Profile, Peek)>::new());
    let expanded = RwSignal::new(HashSet::<String>::new());
    let note = RwSignal::new(String::new());
    let epoch = RwSignal::new(0u64);

    let refresh = move || {
        let gen = epoch.get_untracked().wrapping_add(1);
        epoch.set(gen);
        note.set(String::new());
        let saved = profiles_signal()
            .map(|s| s.get_untracked())
            .unwrap_or_else(load_profiles);
        let list = peers(&saved, &base.get_untracked(), &page_origin());
        peer_count.set(list.len());
        rows.set(list.iter().map(|p| (p.clone(), Peek::Checking)).collect());
        for p in list {
            let token = session_token(&p.id);
            spawn_local(async move {
                let state = run_peek(p.base.clone(), token).await;
                // A newer refresh (or an unmounted list) wins.
                if epoch.try_get_untracked() != Some(gen) {
                    return;
                }
                let _ = rows.try_update(|all| {
                    if let Some(row) = all.iter_mut().find(|(q, _)| q.id == p.id) {
                        row.1 = state;
                    }
                });
            });
        }
    };

    // When the list mounts, whenever the active gateway changes, and
    // when a profile is saved or removed. No timer: a closed list costs
    // zero requests.
    let saved = profiles_signal();
    Effect::new(move |_| {
        base.track();
        if let Some(s) = saved {
            s.track();
        }
        refresh();
    });

    view! {
        <Show when=move || !rows.get().is_empty()>
            <section class="ss-peek" aria-label="Other gateways" data-peek="true">
                <div class="ss-peek-head">
                    <span class="ss-peek-title">"Other gateways"</span>
                    <button type="button" class="secondary" data-action="peek-refresh"
                        on:click=move |_| refresh()>"Refresh"</button>
                </div>
                {move || rows.get().into_iter().map(|(p, state)| {
                    let id = p.id.clone();
                    let toggle_id = id.clone();
                    let switch_id = id.clone();
                    let open_id = id.clone();
                    let is_open = move || expanded.get().contains(&open_id);
                    let is_open_body = is_open.clone();
                    let is_open_aria = is_open.clone();
                    let detail = state.detail();
                    let name = p.name.clone();
                    let switch_label = format!("Switch to {name}");
                    view! {
                        <div class="ss-peek-group" data-peek-gateway=id data-peek-state=state.badge()>
                            <button type="button" class="ss-peek-row"
                                aria-expanded=move || if is_open_aria() { "true" } else { "false" }
                                on:click=move |_| expanded.update(|set| {
                                    if !set.remove(&toggle_id) {
                                        set.insert(toggle_id.clone());
                                    }
                                })>
                                <span class="ss-peek-caret" aria-hidden="true">
                                    {move || if is_open() { "▾" } else { "▸" }}
                                </span>
                                <span class="ss-peek-name">{name}</span>
                                <span class=format!("ss-peek-badge {}", state.badge())>{state.badge_label()}</span>
                                <span class="ss-peek-summary">{state.summary()}</span>
                            </button>
                            <Show when=is_open_body.clone()>
                                <div class="ss-peek-body">
                                    <p class="hint">{detail.clone()}</p>
                                    <button type="button" class="secondary" data-action="peek-switch"
                                        on:click={
                                            let switch_id = switch_id.clone();
                                            move |_| {
                                                if let Err(e) = switch_to(&switch_id) {
                                                    note.set(e);
                                                }
                                            }
                                        }>{switch_label.clone()}</button>
                                </div>
                            </Show>
                        </div>
                    }
                }).collect_view()}
                <Show when=move || !note.get().is_empty()>
                    <p class="hint" role="alert">{move || note.get()}</p>
                </Show>
            </section>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ok(code: u16, body: &str) -> Reply {
        Ok((code, body.to_string()))
    }

    /// Drive the state machine with stubbed replies (keyed by path) and
    /// record every path it asks for.
    fn drive(stub: &dyn Fn(&str) -> Reply) -> (Peek, Vec<&'static str>) {
        let mut replies = Vec::new();
        let mut asked = Vec::new();
        loop {
            match next_step(&replies) {
                Step::Done(state) => return (state, asked),
                Step::Fetch(path) => {
                    asked.push(path);
                    replies.push(stub(path));
                    assert!(asked.len() <= PEEK_PATHS.len(), "peek kept fetching: {asked:?}");
                }
            }
        }
    }

    const SESSIONS: &str = r#"{"sessions":[
        {"id":"a","status":"running"},
        {"id":"b","status":"waiting-for-approval"},
        {"id":"c","status":"question"},
        {"id":"d","status":"idle"},
        {"id":"e"}
    ]}"#;

    #[test]
    fn group_outcomes_from_stubbed_gateways() {
        let cases: Vec<(&str, Box<dyn Fn(&str) -> Reply>, Peek, usize)> = vec![
            (
                "reachable with counts",
                Box::new(|p: &str| match p {
                    "/v1/sessions" => ok(200, SESSIONS),
                    _ => ok(200, "{}"),
                }),
                Peek::Counts { running: 1, waiting: 2 },
                3,
            ),
            (
                "reachable, read not permitted",
                Box::new(|p: &str| match p {
                    "/v1/sessions" => ok(403, ""),
                    _ => ok(200, "{}"),
                }),
                sessions_outcome(&ok(403, "")),
                3,
            ),
            ("unreachable", Box::new(|_: &str| Err(())), Peek::Unreachable, 1),
            (
                "health not ok",
                Box::new(|_: &str| ok(502, "")),
                Peek::Unreachable,
                1,
            ),
            (
                "unauthorized",
                Box::new(|p: &str| match p {
                    "/health" => ok(200, "ok"),
                    _ => ok(401, ""),
                }),
                Peek::Unauthorized,
                2,
            ),
        ];
        for (name, stub, want, calls) in cases {
            let (got, asked) = drive(stub.as_ref());
            assert_eq!(got, want, "{name}");
            assert_eq!(asked.len(), calls, "{name}: {asked:?}");
            assert_eq!(&asked[..], &PEEK_PATHS[..calls], "{name}: only probe + sessions read, in order");
        }
    }

    #[test]
    fn badge_only_is_never_a_zero_count() {
        for reply in [ok(401, ""), ok(403, ""), ok(500, "x"), ok(200, "not json"), Err(())] {
            let state = sessions_outcome(&reply);
            assert!(matches!(state, Peek::BadgeOnly { .. }), "{reply:?} -> {state:?}");
            assert_eq!(state.badge(), "connected");
            assert!(!state.summary().contains('0'), "{}", state.summary());
            assert!(!state.detail().is_empty());
        }
    }

    #[test]
    fn count_sessions_reads_running_and_waiting() {
        assert_eq!(count_sessions(SESSIONS), Some((1, 2)));
        assert_eq!(count_sessions(r#"{"sessions":[]}"#), Some((0, 0)));
        assert_eq!(count_sessions("{}"), None);
        assert_eq!(count_sessions("nope"), None);
    }

    #[test]
    fn badges_reuse_switcher_states() {
        let states = [
            Peek::Checking,
            Peek::Counts { running: 0, waiting: 0 },
            Peek::BadgeOnly { reason: "r".into() },
            Peek::Unreachable,
            Peek::Unauthorized,
        ];
        for s in states {
            assert!(
                matches!(s.badge(), "connected" | "reconnecting" | "unreachable" | "unauthorized"),
                "{s:?}"
            );
        }
    }

    fn profile(id: &str, base: &str) -> Profile {
        Profile {
            id: id.into(),
            name: id.into(),
            base: base.into(),
            ssh: String::new(),
            warn: false,
        }
    }

    #[test]
    fn peers_exclude_the_active_gateway() {
        let list = vec![
            profile("a", "http://127.0.0.1:7777/"),
            profile("b", "http://127.0.0.1:8787"),
            profile("c", ""),
        ];
        let ids = |v: Vec<Profile>| v.into_iter().map(|p| p.id).collect::<Vec<_>>();
        // Same-origin tab: the profile at the page origin is the active one.
        assert_eq!(ids(peers(&list, "", "http://127.0.0.1:7777")), vec!["b"]);
        // Remote base selected: that profile is excluded, the rest stay.
        assert_eq!(ids(peers(&list, "http://127.0.0.1:8787/", "http://127.0.0.1:7777")), vec!["a"]);
        // Unknown origin: nothing to match, every saved base is a peer.
        assert_eq!(ids(peers(&list, "", "")), vec!["a", "b"]);
    }
}
