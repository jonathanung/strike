//! Session `/loop` in the cockpit (#1588).
//!
//! A loop re-injects a prompt into this live session on an interval until it
//! is stopped or the session ends. The daemon owns the timers
//! (`/v1/sessions/{id}/loops`), so a loop keeps firing after the phone tab
//! closes. A firing is a normal `user.input` on the daemon side: it rides the
//! same input queue as a send and shows up in the transcript as the prompt.
//!
//! The composer grammar matches the TUI: `/loop <interval> <job>`,
//! `/loop` or `/loop list`, `/loop stop [id]` (no id stops every loop). The
//! daemon validates the interval and the job (shared `pkg/loopspec`), so the
//! client only splits the line and shows the daemon's own error sentence.
//!
//! Loop state rides the live event stream as `loops.changed` (#1944): the
//! daemon sends the whole list after every start, stop, firing, and failed
//! firing, and the rows replace theirs. The list route is read once when the
//! session opens and again each time the stream (re)connects, since the event
//! is not persisted and a reconnect may have missed one. No polling.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api;
use crate::model::ComposerCommand;

pub const USAGE: &str = "usage: /loop <interval> <job>  |  /loop list  |  /loop stop [id]. Interval: 30s, 15m, 2h, 1h30m (1s to 168h). Loops end with the session.";

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LoopCmd {
    Start { interval: String, job: String },
    List,
    Help,
    Stop(Option<String>),
    /// The line is a `/loop` command the client can reject without a round
    /// trip (missing job). The string is the user-facing reason.
    Invalid(String),
}

/// Parses a composer line. `None` means the line is not a `/loop` command
/// and goes through the normal send path.
pub fn parse(text: &str) -> Option<LoopCmd> {
    let raw = text.trim();
    let rest = raw.strip_prefix("/loop")?;
    if !rest.is_empty() && !rest.starts_with(char::is_whitespace) {
        return None; // `/loopy` is not `/loop`
    }
    let rest = rest.trim();
    let mut fields = rest.split_whitespace();
    let Some(first) = fields.next() else {
        return Some(LoopCmd::List);
    };
    match first.to_ascii_lowercase().as_str() {
        "list" | "ls" => Some(LoopCmd::List),
        "help" | "?" => Some(LoopCmd::Help),
        "stop" | "cancel" | "rm" | "remove" => {
            Some(LoopCmd::Stop(fields.next().map(|id| id.to_string())))
        }
        _ => {
            let job = rest[first.len()..].trim();
            if job.is_empty() {
                return Some(LoopCmd::Invalid(
                    "loop: need <interval> <job>, e.g. /loop 15m check the pipeline".into(),
                ));
            }
            Some(LoopCmd::Start {
                interval: first.to_string(),
                job: job.to_string(),
            })
        }
    }
}

/// One loop as `GET /v1/sessions/{id}/loops` reports it.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct SessionLoop {
    pub id: String,
    #[serde(default)]
    pub interval: String,
    #[serde(default)]
    pub job: String,
    #[serde(default)]
    pub runs: i64,
    #[serde(default, rename = "nextAt")]
    pub next_at: String,
    #[serde(default, rename = "lastError")]
    pub last_error: String,
}

impl From<protocol_schema::LoopState> for SessionLoop {
    fn from(l: protocol_schema::LoopState) -> Self {
        Self {
            id: l.id,
            interval: l.interval,
            job: l.job,
            runs: l.runs,
            next_at: l.next_at,
            last_error: l.last_error,
        }
    }
}

/// The loop list a live `loops.changed` envelope carries, or `None` for
/// any other envelope. The live stream is per root, so the envelope is for
/// the root whose stream delivered it (its `sessionId` is the durable
/// session id, which need not equal the root id).
pub fn loops_from_envelope(raw: &str) -> Option<Vec<SessionLoop>> {
    if !raw.contains("\"loops.changed\"") {
        return None;
    }
    match protocol_schema::decode_event(raw) {
        Ok(protocol_schema::Event::LoopsChanged(ev)) => Some(ev.loops.into_iter().map(SessionLoop::from).collect()),
        _ => None,
    }
}

/// Replaces the rows from one envelope of `stream_root`'s live stream when
/// it is `loops.changed` and that root is still the one shown.
pub fn apply_loops_envelope(raw: &str, stream_root: &str, active: RwSignal<String>, loops: RwSignal<Vec<SessionLoop>>) {
    if active.get_untracked() != stream_root {
        return;
    }
    if let Some(next) = loops_from_envelope(raw) {
        loops.set(next);
    }
}

/// The rows after a successful stop, without a refetch: `Some(id)` drops
/// that loop, `None` (stop all) clears the list.
pub fn without_stopped(mut loops: Vec<SessionLoop>, id: Option<&str>) -> Vec<SessionLoop> {
    match id {
        Some(id) => loops.retain(|l| l.id != id),
        None => loops.clear(),
    }
    loops
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct LoopsResponse {
    #[serde(default)]
    pub loops: Vec<SessionLoop>,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct StartResponse {
    #[serde(default)]
    r#loop: SessionLoop,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct StopResponse {
    #[serde(default)]
    stopped: i64,
}

/// The composer catalog rows for `/loop`, appended when the daemon reports
/// the `loops` capability.
pub fn catalog_commands() -> Vec<ComposerCommand> {
    vec![
        ComposerCommand {
            id: "loop-start".into(),
            label: "/loop".into(),
            detail: "Repeat a prompt in this session on an interval".into(),
            insert: "/loop ".into(),
        },
        ComposerCommand {
            id: "loop-list".into(),
            label: "/loop list".into(),
            detail: "Show this session's loops".into(),
            insert: "/loop list".into(),
        },
        ComposerCommand {
            id: "loop-stop".into(),
            label: "/loop stop".into(),
            detail: "Stop every loop in this session (or /loop stop <id>)".into(),
            insert: "/loop stop".into(),
        },
    ]
}

/// Adds the loop rows to a catalog once (bootstrap can run again).
pub fn with_loop_commands(mut commands: Vec<ComposerCommand>) -> Vec<ComposerCommand> {
    if !commands.iter().any(|c| c.id.starts_with("loop-")) {
        commands.extend(catalog_commands());
    }
    commands
}

/// "in 4m 10s" style countdown. `next_ms` 0 means unknown.
pub fn next_label(next_ms: f64, now_ms: f64) -> String {
    if next_ms <= 0.0 {
        return String::new();
    }
    let secs = ((next_ms - now_ms) / 1000.0).ceil() as i64;
    if secs <= 0 {
        return "due now".into();
    }
    let (h, m, s) = (secs / 3600, (secs % 3600) / 60, secs % 60);
    if h > 0 {
        format!("next in {h}h {m}m")
    } else if m > 0 {
        format!("next in {m}m {s}s")
    } else {
        format!("next in {s}s")
    }
}

pub fn runs_label(runs: i64) -> String {
    match runs {
        0 => "not run yet".into(),
        1 => "ran once".into(),
        n => format!("ran {n} times"),
    }
}

/// The daemon's error sentence out of an api helper error
/// (`HTTP 400 {"error":"…"}`), or the raw text when it is not JSON.
pub fn daemon_error(raw: &str) -> String {
    let body = raw
        .strip_prefix("HTTP ")
        .and_then(|rest| rest.split_once(' ').map(|(_, b)| b))
        .unwrap_or(raw);
    serde_json::from_str::<serde_json::Value>(body)
        .ok()
        .and_then(|v| v.get("error").and_then(|e| e.as_str()).map(str::to_string))
        .filter(|s| !s.trim().is_empty())
        .unwrap_or_else(|| raw.to_string())
}

/// True when the list call failed only because the session is not live
/// (attach-only or closed). That is "no loops", not an error to show.
pub fn not_live(raw: &str) -> bool {
    raw.starts_with("HTTP 404") || raw.starts_with("HTTP 410")
}

pub fn list_summary(loops: &[SessionLoop]) -> String {
    if loops.is_empty() {
        return format!("No loops in this session. {USAGE}");
    }
    let rows: Vec<String> = loops
        .iter()
        .map(|l| format!("{} every {}", l.id, l.interval))
        .collect();
    format!("{} active: {}", loops.len(), rows.join(", "))
}

pub fn started_note(l: &SessionLoop) -> String {
    format!(
        "loop: started {} every {}: {} (/loop stop {})",
        l.id,
        l.interval,
        crate::steer_queue::preview(&l.job),
        l.id
    )
}

pub fn stopped_note(id: Option<&str>, stopped: i64) -> String {
    match id {
        Some(id) => format!("loop: stopped {id}"),
        None if stopped == 0 => "loop: no active loops".into(),
        None => format!("loop: stopped all ({stopped})"),
    }
}

fn loops_path(root: &str) -> String {
    format!("/v1/sessions/{}/loops", api::js_encode(root))
}

fn loop_path(root: &str, id: &str) -> String {
    format!("{}/{}", loops_path(root), api::js_encode(id))
}

fn parse_time_ms(rfc3339: &str) -> f64 {
    if rfc3339.is_empty() {
        return 0.0;
    }
    let ms = js_sys::Date::parse(rfc3339);
    if ms.is_nan() {
        0.0
    } else {
        ms
    }
}

/// Fetches the list into `loops`. Not-live clears it quietly. A reply for
/// a session that is no longer `active` is dropped.
pub async fn refresh(
    base: String,
    token: String,
    root: String,
    active: RwSignal<String>,
    loops: RwSignal<Vec<SessionLoop>>,
) {
    let got = api::get_json::<LoopsResponse>(&base, &token, &loops_path(&root)).await;
    if active.get_untracked() != root {
        return;
    }
    match got {
        Ok(resp) => loops.set(resp.loops),
        Err(e) if not_live(&e) => loops.set(Vec::new()),
        Err(_) => {} // keep the last list through a transient failure
    }
}

/// Runs one parsed composer command against the daemon and reports the
/// outcome in `note`, unless the user has switched sessions meanwhile.
pub async fn run(
    base: String,
    token: String,
    root: String,
    cmd: LoopCmd,
    active: RwSignal<String>,
    loops: RwSignal<Vec<SessionLoop>>,
    note: RwSignal<String>,
) {
    let note_for = root.clone();
    let note = move |msg: String| {
        if active.get_untracked() == note_for {
            note.set(msg);
        }
    };
    match cmd {
        LoopCmd::Invalid(msg) => note(msg),
        LoopCmd::Help => note(USAGE.into()),
        LoopCmd::List => {
            match api::get_json::<LoopsResponse>(&base, &token, &loops_path(&root)).await {
                Ok(resp) => {
                    note(list_summary(&resp.loops));
                    if active.get_untracked() == root {
                        loops.set(resp.loops);
                    }
                }
                Err(e) if not_live(&e) => {
                    if active.get_untracked() == root {
                        loops.set(Vec::new());
                    }
                    note("loop: this session is not live here, so it has no loops.".into());
                }
                Err(e) => note(format!("loop: {}", daemon_error(&e))),
            }
        }
        LoopCmd::Start { interval, job } => {
            let body = serde_json::json!({"interval": interval, "job": job});
            match api::post_json_value(&base, &token, &loops_path(&root), body).await {
                Ok(v) => {
                    let started = serde_json::from_value::<StartResponse>(v).unwrap_or_default();
                    note(started_note(&started.r#loop));
                    refresh(base, token, root, active, loops).await;
                }
                Err(e) => note(format!("loop: {}", daemon_error(&e))),
            }
        }
        LoopCmd::Stop(id) => {
            let path = match &id {
                Some(id) => loop_path(&root, id),
                None => loops_path(&root),
            };
            match delete_value(&base, &token, &path).await {
                Ok(v) => {
                    let stopped = serde_json::from_value::<StopResponse>(v).unwrap_or_default();
                    note(stopped_note(id.as_deref(), stopped.stopped));
                    // The daemon also sends loops.changed; dropping the row
                    // here keeps the panel right without a refetch.
                    if active.get_untracked() == root {
                        loops.update(|ls| *ls = without_stopped(std::mem::take(ls), id.as_deref()));
                    }
                }
                Err(e) => note(format!("loop: {}", daemon_error(&e))),
            }
        }
    }
}

/// DELETE returning the JSON body (the stop routes report a count).
async fn delete_value(base: &str, token: &str, path: &str) -> Result<serde_json::Value, String> {
    let mut req = gloo_net::http::Request::delete(&api::api(base, path))
        .credentials(web_sys::RequestCredentials::Include);
    if !token.trim().is_empty() {
        req = req.header("Authorization", &api::auth_header(token.trim()));
    }
    let resp = req.send().await.map_err(|e| e.to_string())?;
    let status = resp.status();
    let text = resp.text().await.unwrap_or_default();
    if !(200..300).contains(&status) {
        return Err(format!("HTTP {status} {text}"));
    }
    if text.trim().is_empty() {
        return Ok(serde_json::Value::Null);
    }
    serde_json::from_str(&text).map_err(|e| e.to_string())
}

/// What a change of (root, capable, stream up) asks of the rows: clear
/// them when the session or capability changed, and fetch the list when it
/// can be shown and the session changed or the stream just came up.
pub fn on_key_change(prev: Option<&(String, bool, bool)>, key: &(String, bool, bool)) -> (bool, bool) {
    let session_changed = prev.map(|p| (&p.0, p.1) != (&key.0, key.1)).unwrap_or(true);
    let stream_came_up = key.2 && prev.map(|p| !p.2).unwrap_or(true);
    let fetch = key.1 && !key.0.is_empty() && (session_changed || stream_came_up);
    (session_changed, fetch)
}

/// Loop rows for the queue pane: interval, job, run count, next fire, Stop.
/// Kept current by `loops.changed` on the live stream (see
/// [`apply_loops_envelope`]); the list route is read when the session
/// changes and whenever `stream_up` turns true.
#[component]
pub fn LoopRows(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    capable: RwSignal<bool>,
    stream_up: RwSignal<bool>,
    loops: RwSignal<Vec<SessionLoop>>,
    note: RwSignal<String>,
) -> impl IntoView {
    let now = RwSignal::new(api::now_ms());
    // Bootstrap re-sets the capability on every refresh, so act only on a
    // real change of the key.
    Effect::new(move |prev: Option<(String, bool, bool)>| {
        let key = (root.get(), capable.get(), stream_up.get());
        let (reset, fetch) = on_key_change(prev.as_ref(), &key);
        if reset {
            loops.set(Vec::new());
            note.set(String::new());
        }
        if fetch {
            spawn_local(refresh(
                base.get_untracked(),
                token.get_untracked(),
                key.0.clone(),
                root,
                loops,
            ));
        }
        key
    });
    // A 1s clock for the "next in" countdown only; it never fetches.
    let handle = StoredValue::new(None::<i32>);
    if let Some(window) = web_sys::window() {
        let cb = Closure::<dyn FnMut()>::new(move || {
            now.set(api::now_ms());
        });
        if let Ok(id) = window
            .set_interval_with_callback_and_timeout_and_arguments_0(cb.as_ref().unchecked_ref(), 1000)
        {
            handle.set_value(Some(id));
        }
        cb.forget();
    }
    on_cleanup(move || {
        if let (Some(window), Some(id)) = (web_sys::window(), handle.get_value()) {
            window.clear_interval_with_handle(id);
        }
    });

    let stop_one = move |id: String| {
        let (b, t, r) = (base.get_untracked(), token.get_untracked(), root.get_untracked());
        spawn_local(run(b, t, r, LoopCmd::Stop(Some(id)), root, loops, note));
    };

    view! {
        <Show when=move || !note.get().is_empty()>
            <p class="loop-note" role="status">{move || note.get()}</p>
        </Show>
        <Show when=move || !loops.get().is_empty()>
            <ol class="loop-list" aria-label="Session loops">
                {move || {
                    let at = now.get();
                    loops.get().into_iter().map(|l| {
                        let id = l.id.clone();
                        let label = format!("Stop loop {}", l.id);
                        let next = next_label(parse_time_ms(&l.next_at), at);
                        let runs = runs_label(l.runs);
                        let err = l.last_error.clone();
                        let has_err = !err.is_empty();
                        view! {
                            <li data-loop-id=l.id.clone()>
                                <span class="loop-badge">{format!("{} · every {}", l.id, l.interval)}</span>
                                <span class="loop-job" title=l.job.clone()>{crate::steer_queue::preview(&l.job)}</span>
                                <span class="loop-meta">{format!("{runs} · {next}")}</span>
                                <Show when=move || has_err>
                                    <span class="loop-error" role="alert">{format!("last firing failed: {}", err.clone())}</span>
                                </Show>
                                <span class="queue-actions">
                                    <button type="button" class="secondary" aria-label=label on:click=move |_| stop_one(id.clone())>"Stop"</button>
                                </span>
                            </li>
                        }
                    }).collect_view()
                }}
            </ol>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_matches_the_tui_grammar() {
        let cases: &[(&str, Option<LoopCmd>)] = &[
            ("hello", None),
            ("/loopy 5m x", None),
            ("/looping", None),
            ("/loop", Some(LoopCmd::List)),
            ("  /loop  ", Some(LoopCmd::List)),
            ("/loop list", Some(LoopCmd::List)),
            ("/loop LS", Some(LoopCmd::List)),
            ("/loop help", Some(LoopCmd::Help)),
            ("/loop stop", Some(LoopCmd::Stop(None))),
            ("/loop stop l2", Some(LoopCmd::Stop(Some("l2".into())))),
            ("/loop cancel l3", Some(LoopCmd::Stop(Some("l3".into())))),
            (
                "/loop 15m check the pipeline",
                Some(LoopCmd::Start {
                    interval: "15m".into(),
                    job: "check the pipeline".into(),
                }),
            ),
            (
                "/loop 1h30m  look\n for  issues ",
                Some(LoopCmd::Start {
                    interval: "1h30m".into(),
                    job: "look\n for  issues".into(),
                }),
            ),
            // Bad intervals still go to the daemon, which owns the grammar.
            (
                "/loop 500ms x",
                Some(LoopCmd::Start {
                    interval: "500ms".into(),
                    job: "x".into(),
                }),
            ),
        ];
        for (input, want) in cases {
            assert_eq!(&parse(input), want, "input {input:?}");
        }
        assert!(matches!(parse("/loop 15m"), Some(LoopCmd::Invalid(_))));
        assert!(matches!(parse("/loop 15m   "), Some(LoopCmd::Invalid(_))));
    }

    #[test]
    fn next_and_runs_labels() {
        let now = 1_000_000.0;
        assert_eq!(next_label(0.0, now), "");
        assert_eq!(next_label(now - 5.0, now), "due now");
        assert_eq!(next_label(now + 9_200.0, now), "next in 10s");
        assert_eq!(next_label(now + 250_000.0, now), "next in 4m 10s");
        assert_eq!(next_label(now + 2.0 * 3_600_000.0 + 60_000.0, now), "next in 2h 1m");
        assert_eq!(runs_label(0), "not run yet");
        assert_eq!(runs_label(1), "ran once");
        assert_eq!(runs_label(7), "ran 7 times");
    }

    #[test]
    fn daemon_error_reads_the_json_sentence() {
        assert_eq!(
            daemon_error(r#"HTTP 400 {"error":"interval 500ms too short (min 1s)"}"#),
            "interval 500ms too short (min 1s)"
        );
        assert_eq!(
            daemon_error(r#"HTTP 409 {"error":"at most 16 concurrent loops"}"#),
            "at most 16 concurrent loops"
        );
        assert_eq!(daemon_error("HTTP 500 boom"), "HTTP 500 boom");
        assert_eq!(daemon_error("network down"), "network down");
        assert!(not_live(r#"HTTP 404 {"code":"session_not_live"}"#));
        assert!(not_live("HTTP 410 {}"));
        assert!(!not_live("HTTP 400 {}"));
    }

    #[test]
    fn decodes_the_daemon_list() {
        let raw = r#"{"loops":[{"id":"l1","interval":"15m","intervalMs":900000,"job":"check ci","runs":2,"createdAt":"2026-11-20T10:00:00Z","nextAt":"2026-11-20T10:45:00Z","lastError":"queue full"}],"max":16,"minIntervalMs":1000,"maxIntervalMs":604800000}"#;
        let resp: LoopsResponse = serde_json::from_str(raw).unwrap();
        assert_eq!(resp.loops.len(), 1);
        let l = &resp.loops[0];
        assert_eq!((l.id.as_str(), l.interval.as_str(), l.runs), ("l1", "15m", 2));
        assert_eq!(l.next_at, "2026-11-20T10:45:00Z");
        assert_eq!(l.last_error, "queue full");
        let empty: LoopsResponse = serde_json::from_str(r#"{"loops":[]}"#).unwrap();
        assert!(empty.loops.is_empty());
        assert!(list_summary(&empty.loops).contains("usage: /loop"));
        assert_eq!(list_summary(&resp.loops), "1 active: l1 every 15m");
    }

    #[test]
    fn loops_changed_envelope_replaces_the_list() {
        let raw = r#"{"type":"loops.changed","time":"2026-11-21T10:00:00Z","v":"1.27.0","data":{"sessionId":"s1","reason":"fire","loopId":"l1","loops":[{"id":"l1","interval":"15m","intervalMs":900000,"job":"check ci","runs":3,"createdAt":"2026-11-21T09:00:00Z","nextAt":"2026-11-21T10:15:00Z","lastRunAt":"2026-11-21T10:00:00Z"}]}}"#;
        let got = loops_from_envelope(raw).expect("loops.changed");
        assert_eq!(got.len(), 1);
        assert_eq!((got[0].id.as_str(), got[0].interval.as_str(), got[0].runs), ("l1", "15m", 3));
        assert_eq!(got[0].next_at, "2026-11-21T10:15:00Z");
        assert!(got[0].last_error.is_empty());

        // Other events and junk are not loop state.
        assert_eq!(
            loops_from_envelope(r#"{"type":"text.delta","time":"2026-11-21T10:00:00Z","tag":"loops.changed","data":{"text":"hi"}}"#),
            None
        );
        assert_eq!(loops_from_envelope("not json \"loops.changed\""), None);

        // A stop that empties the session sends an empty (or null) list.
        let empty = r#"{"type":"loops.changed","time":"2026-11-21T10:00:00Z","data":{"sessionId":"s1","reason":"stop","loops":[]}}"#;
        assert_eq!(loops_from_envelope(empty), Some(Vec::new()));
        let null = r#"{"type":"loops.changed","time":"2026-11-21T10:00:00Z","data":{"sessionId":"s1","reason":"close","loops":null}}"#;
        assert_eq!(loops_from_envelope(null), Some(Vec::new()));
    }

    #[test]
    fn stop_drops_rows_without_a_refetch() {
        let row = |id: &str| SessionLoop { id: id.into(), ..Default::default() };
        let cases: &[(Option<&str>, &[&str])] = &[(Some("l1"), &["l2"]), (Some("l9"), &["l1", "l2"]), (None, &[])];
        for (id, want) in cases {
            let got = without_stopped(vec![row("l1"), row("l2")], *id);
            let ids: Vec<&str> = got.iter().map(|l| l.id.as_str()).collect();
            assert_eq!(ids, want.to_vec(), "stop {id:?}");
        }
    }

    #[test]
    fn key_change_fetches_on_session_change_and_stream_up() {
        let k = |root: &str, capable: bool, up: bool| (root.to_string(), capable, up);
        // First run: reset, fetch when capable with a root.
        assert_eq!(on_key_change(None, &k("s1", true, false)), (true, true));
        assert_eq!(on_key_change(None, &k("", true, false)), (true, false));
        assert_eq!(on_key_change(None, &k("s1", false, true)), (true, false));
        // Same key (bootstrap re-set): nothing.
        assert_eq!(on_key_change(Some(&k("s1", true, true)), &k("s1", true, true)), (false, false));
        // Stream (re)connects: refetch, keep rows.
        assert_eq!(on_key_change(Some(&k("s1", true, false)), &k("s1", true, true)), (false, true));
        // Stream drops: nothing to do until it is back.
        assert_eq!(on_key_change(Some(&k("s1", true, true)), &k("s1", true, false)), (false, false));
        // Session switch: reset and fetch.
        assert_eq!(on_key_change(Some(&k("s1", true, true)), &k("s2", true, true)), (true, true));
    }

    #[test]
    fn notes_and_catalog() {
        let l = SessionLoop {
            id: "l1".into(),
            interval: "10s".into(),
            job: "ping".into(),
            ..Default::default()
        };
        assert_eq!(started_note(&l), "loop: started l1 every 10s: ping (/loop stop l1)");
        assert_eq!(stopped_note(Some("l1"), 1), "loop: stopped l1");
        assert_eq!(stopped_note(None, 0), "loop: no active loops");
        assert_eq!(stopped_note(None, 3), "loop: stopped all (3)");

        let once = with_loop_commands(Vec::new());
        assert_eq!(once.len(), 3);
        assert_eq!(with_loop_commands(once.clone()).len(), 3, "no duplicate rows");
        assert!(once.iter().all(|c| parse(&c.insert).is_some()));
    }
}
