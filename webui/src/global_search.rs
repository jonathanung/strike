//! Global transcript search (#1355): find text across all sessions.
//!
//! The daemon owns the search (`GET /v1/search?q=…`, an on-demand scan of
//! every root session's user and assistant messages, redacted, newest
//! first, capped at 50 — see `internal/frontend/server/globalsearch.go`).
//! This module is the cockpit side: an overlay opened from the command
//! palette ("Search all sessions…"), a result list with the match
//! highlighted in each snippet, and a handoff — picking a result opens
//! that session and hands the query to find-in-transcript (#1342), which
//! highlights and scrolls to the match.
//!
//! The overlay says plainly when results were capped or the scan stopped
//! at its time limit; a failed request shows the error instead of a
//! spinner.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use wasm_bindgen::JsCast;

use crate::api::{get_json, js_encode};
use crate::model::Workspace;

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct Hit {
    pub id: String,
    #[serde(default)]
    pub title: String,
    #[serde(default, rename = "workspaceId")]
    pub workspace_id: String,
    #[serde(default, rename = "updatedAt")]
    pub updated_at: i64,
    #[serde(default)]
    pub role: String,
    #[serde(default)]
    pub snippet: String,
    #[serde(default, rename = "matchStart")]
    pub match_start: usize,
    #[serde(default, rename = "matchEnd")]
    pub match_end: usize,
    #[serde(default)]
    pub matches: u32,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct Response {
    #[serde(default)]
    pub hits: Vec<Hit>,
    #[serde(default)]
    pub limit: u32,
    #[serde(default)]
    pub capped: bool,
    #[serde(default)]
    pub partial: bool,
    #[serde(default)]
    pub scanned: u32,
    #[serde(default)]
    pub sessions: u32,
    #[serde(default)]
    pub skipped: u32,
}

/// What the overlay shows below the input.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Phase {
    Idle,
    Searching,
    Done(Response),
    Failed(String),
}

pub const MIN_CHARS: usize = 2;

pub fn search_path(q: &str) -> String {
    format!("/v1/search?q={}", js_encode(q.trim()))
}

/// Why a query cannot be sent yet, if it cannot.
pub fn query_problem(q: &str) -> Option<&'static str> {
    let n = q.trim().chars().count();
    if n == 0 {
        Some("Type something to search all sessions.")
    } else if n < MIN_CHARS {
        Some("Type at least 2 characters.")
    } else if n > 200 {
        Some("That query is too long (200 characters max).")
    } else {
        None
    }
}

/// Split a snippet into (before, match, after). Offsets that are out of
/// range or not on char boundaries degrade to "no highlight" rather than
/// panicking.
pub fn split_snippet(snippet: &str, start: usize, end: usize) -> (String, String, String) {
    if start <= end
        && end <= snippet.len()
        && snippet.is_char_boundary(start)
        && snippet.is_char_boundary(end)
    {
        (
            snippet[..start].to_string(),
            snippet[start..end].to_string(),
            snippet[end..].to_string(),
        )
    } else {
        (snippet.to_string(), String::new(), String::new())
    }
}

/// Plain-language result summary, including the cap and time-limit notes.
pub fn summary(resp: &Response) -> String {
    let n = resp.hits.len();
    let mut out = if n == 0 {
        format!("No matches in {} session(s).", resp.scanned)
    } else if n == 1 {
        "1 session matches.".to_string()
    } else {
        format!("{n} sessions match.")
    };
    if resp.capped {
        out.push_str(&format!(
            " Showing the newest {} \u{2014} refine the query to see older ones.",
            resp.limit.max(n as u32)
        ));
    }
    if resp.partial {
        out.push_str(&format!(
            " The search stopped at its time limit after {} of {} sessions; older sessions were not searched.",
            resp.scanned, resp.sessions
        ));
    }
    if resp.skipped > 0 {
        out.push_str(&format!(
            " {} unreadable log line(s) were skipped.",
            resp.skipped
        ));
    }
    out
}

pub fn workspace_name(id: &str, workspaces: &[Workspace]) -> String {
    if id.is_empty() {
        return String::new();
    }
    workspaces
        .iter()
        .find(|w| w.id == id)
        .map(|w| w.name.clone())
        .unwrap_or_default()
}

/// Short date for a result row: "today", "yesterday", "N days ago", or the
/// ISO date beyond a week.
pub fn date_label(ms: i64, now_ms: i64) -> String {
    if ms <= 0 {
        return String::new();
    }
    let day = 24 * 60 * 60 * 1000;
    let days = (now_ms - ms).max(0) / day;
    match days {
        0 => "today".into(),
        1 => "yesterday".into(),
        2..=7 => format!("{days} days ago"),
        _ => {
            #[cfg(target_arch = "wasm32")]
            {
                let d = js_sys::Date::new(&wasm_bindgen::JsValue::from_f64(ms as f64));
                d.to_iso_string()
                    .as_string()
                    .map(|s| s.chars().take(10).collect())
                    .unwrap_or_default()
            }
            #[cfg(not(target_arch = "wasm32"))]
            {
                format!("{days} days ago")
            }
        }
    }
}

pub fn step(current: usize, delta: i32, len: usize) -> usize {
    if len == 0 {
        return 0;
    }
    let next = current as i64 + delta as i64;
    next.rem_euclid(len as i64) as usize
}

fn focus_input_soon() {
    let Some(win) = web_sys::window() else {
        return;
    };
    let cb = wasm_bindgen::closure::Closure::once(move || {
        if let Some(el) = web_sys::window()
            .and_then(|w| w.document())
            .and_then(|d| d.get_element_by_id("gs-input"))
            .and_then(|el| el.dyn_into::<web_sys::HtmlElement>().ok())
        {
            let _ = el.focus();
        }
    });
    let _ = win.set_timeout_with_callback_and_timeout_and_arguments_0(
        cb.as_ref().unchecked_ref(),
        0,
    );
    cb.forget();
}

/// Run one search. Responses from an older query are dropped by `epoch`.
pub fn run(
    base: String,
    token: String,
    query: String,
    phase: RwSignal<Phase>,
    cursor: RwSignal<usize>,
    epoch: RwSignal<u64>,
) {
    if let Some(problem) = query_problem(&query) {
        phase.set(Phase::Failed(problem.into()));
        return;
    }
    let ticket = epoch.get_untracked().wrapping_add(1);
    epoch.set(ticket);
    phase.set(Phase::Searching);
    cursor.set(0);
    spawn_local(async move {
        let result = get_json::<Response>(&base, &token, &search_path(&query)).await;
        if epoch.get_untracked() != ticket {
            return;
        }
        phase.set(match result {
            Ok(resp) => Phase::Done(resp),
            Err(e) => Phase::Failed(format!("Search failed: {e}")),
        });
    });
}

/// The overlay. `on_pick(session_id, query)` opens the session; the page
/// owns opening and the find-in-transcript handoff.
#[component]
pub fn GlobalSearch(
    open: RwSignal<bool>,
    query: RwSignal<String>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    workspaces: RwSignal<Vec<Workspace>>,
    on_pick: Callback<(String, String)>,
) -> impl IntoView {
    let phase = RwSignal::new(Phase::Idle);
    let cursor = RwSignal::new(0usize);
    let epoch = RwSignal::new(0u64);
    let search = move || {
        run(
            base.get_untracked(),
            token.get_untracked(),
            query.get_untracked(),
            phase,
            cursor,
            epoch,
        )
    };
    // Opening (from the palette, possibly with a query) searches at once.
    Effect::new(move |_| {
        if open.get() {
            focus_input_soon();
            if query.get_untracked().trim().is_empty() {
                phase.set(Phase::Idle);
            } else {
                search();
            }
        }
    });
    let hits = move || match phase.get() {
        Phase::Done(resp) => resp.hits,
        _ => Vec::new(),
    };
    let pick = move |index: usize| {
        if let Some(hit) = hits().get(index) {
            open.set(false);
            on_pick.run((hit.id.clone(), query.get_untracked().trim().to_string()));
        }
    };
    let close = move || open.set(false);
    view! {
        <Show when=move || open.get()>
            <div class="gs-backdrop" on:click=move |_| close()>
                <div
                    class="gs-dialog"
                    role="dialog"
                    aria-modal="true"
                    aria-label="Search all sessions"
                    data-global-search="true"
                    on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()
                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                        let len = hits().len();
                        match ev.key().as_str() {
                            "Escape" => {
                                ev.prevent_default();
                                ev.stop_propagation();
                                close();
                            }
                            "ArrowDown" => {
                                ev.prevent_default();
                                cursor.set(step(cursor.get_untracked(), 1, len));
                            }
                            "ArrowUp" => {
                                ev.prevent_default();
                                cursor.set(step(cursor.get_untracked(), -1, len));
                            }
                            _ => {}
                        }
                    }
                >
                    <form class="gs-form" on:submit=move |ev: web_sys::SubmitEvent| {
                        ev.prevent_default();
                        // Enter on a fresh query searches; Enter again on
                        // the same results opens the highlighted one.
                        if matches!(phase.get_untracked(), Phase::Done(ref r) if !r.hits.is_empty()) {
                            pick(cursor.get_untracked());
                        } else {
                            search();
                        }
                    }>
                        <input
                            id="gs-input"
                            class="gs-input"
                            type="search"
                            placeholder="Search all sessions"
                            aria-label="Search all sessions"
                            autocomplete="off"
                            prop:value=move || query.get()
                            on:input=move |ev| {
                                query.set(event_target_value(&ev));
                                // A new query invalidates the shown results:
                                // Enter must search, not open a stale hit.
                                if matches!(phase.get_untracked(), Phase::Done(_)) {
                                    phase.set(Phase::Idle);
                                }
                            }
                        />
                        <button type="submit" class="secondary" data-gs-submit="true">"Search"</button>
                    </form>
                    <p class="gs-status" role="status" data-gs-status="true">{move || match phase.get() {
                        Phase::Idle => "Searches user and assistant messages in every session, newest first.".to_string(),
                        Phase::Searching => "Searching\u{2026}".to_string(),
                        Phase::Done(resp) => summary(&resp),
                        Phase::Failed(msg) => msg,
                    }}</p>
                    <ul class="gs-results" role="listbox" aria-label="Matching sessions">
                        {move || {
                            let ws = workspaces.get();
                            let now = crate::api::now_ms() as i64;
                            let current = cursor.get();
                            hits().into_iter().enumerate().map(|(i, hit)| {
                                let (pre, mark, post) = split_snippet(&hit.snippet, hit.match_start, hit.match_end);
                                let title = if hit.title.trim().is_empty() { "Untitled session".to_string() } else { hit.title.clone() };
                                let mut meta = Vec::new();
                                let wname = workspace_name(&hit.workspace_id, &ws);
                                if !wname.is_empty() { meta.push(wname); }
                                let when = date_label(hit.updated_at, now);
                                if !when.is_empty() { meta.push(when); }
                                meta.push(if hit.role == "assistant" { "assistant".to_string() } else { "you".to_string() });
                                if hit.matches > 1 { meta.push(format!("{} matching messages", hit.matches)); }
                                let class = if i == current { "gs-hit on" } else { "gs-hit" };
                                view! {
                                    <li role="option" aria-selected=(i == current).to_string()>
                                        <button type="button" class=class data-gs-hit=hit.id.clone() on:click=move |_| pick(i)>
                                            <span class="gs-title">{title}</span>
                                            <span class="gs-meta">{meta.join(" \u{b7} ")}</span>
                                            <span class="gs-snippet">{pre}<mark>{mark}</mark>{post}</span>
                                        </button>
                                    </li>
                                }
                            }).collect_view()
                        }}
                    </ul>
                </div>
            </div>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn search_path_encodes_the_query() {
        assert_eq!(search_path(" coral & reef "), "/v1/search?q=coral%20%26%20reef");
    }

    #[test]
    fn short_and_long_queries_are_refused_locally() {
        assert!(query_problem("").is_some());
        assert!(query_problem("a").is_some());
        assert!(query_problem(&"x".repeat(201)).is_some());
        assert_eq!(query_problem("ab"), None);
        assert_eq!(query_problem("界界"), None);
    }

    #[test]
    fn snippet_split_highlights_and_degrades_safely() {
        assert_eq!(
            split_snippet("the coral reef", 4, 9),
            ("the ".into(), "coral".into(), " reef".into())
        );
        // Out of range or mid-char offsets: no highlight, no panic.
        assert_eq!(split_snippet("abc", 2, 9).1, "");
        assert_eq!(split_snippet("界x", 1, 3).1, "");
        assert_eq!(split_snippet("abc", 2, 1).0, "abc");
    }

    fn resp(n: usize) -> Response {
        Response {
            hits: (0..n).map(|i| Hit { id: format!("s{i}"), ..Hit::default() }).collect(),
            limit: 50,
            scanned: 10,
            sessions: 10,
            ..Response::default()
        }
    }

    #[test]
    fn summary_states_caps_time_limits_and_skips() {
        assert_eq!(summary(&resp(0)), "No matches in 10 session(s).");
        assert_eq!(summary(&resp(1)), "1 session matches.");
        let mut r = resp(50);
        r.capped = true;
        assert!(summary(&r).contains("Showing the newest 50"));
        let mut r = resp(3);
        r.partial = true;
        r.scanned = 4;
        assert!(summary(&r).contains("time limit after 4 of 10 sessions"));
        let mut r = resp(2);
        r.skipped = 3;
        assert!(summary(&r).contains("3 unreadable log line(s)"));
    }

    #[test]
    fn workspace_names_resolve_or_stay_blank() {
        let ws = vec![Workspace {
            id: "w1".into(),
            name: "strike".into(),
            ..serde_json::from_str::<Workspace>(r#"{"id":"","name":"","path":""}"#).unwrap()
        }];
        assert_eq!(workspace_name("w1", &ws), "strike");
        assert_eq!(workspace_name("w2", &ws), "");
        assert_eq!(workspace_name("", &ws), "");
    }

    #[test]
    fn dates_and_cursor_steps() {
        let day = 24 * 60 * 60 * 1000;
        let now = 100 * day;
        assert_eq!(date_label(now - 1000, now), "today");
        assert_eq!(date_label(now - day, now), "yesterday");
        assert_eq!(date_label(now - 3 * day, now), "3 days ago");
        assert_eq!(date_label(0, now), "");
        assert_eq!(step(0, -1, 3), 2);
        assert_eq!(step(2, 1, 3), 0);
        assert_eq!(step(0, 1, 0), 0);
    }

    #[test]
    fn response_decodes_the_daemon_payload() {
        let r: Response = serde_json::from_str(
            r#"{"query":"coral","hits":[{"id":"a","title":"T","workspaceId":"w","updatedAt":5,"role":"assistant","snippet":"x coral y","matchStart":2,"matchEnd":7,"matches":2}],"limit":50,"capped":false,"partial":true,"scanned":3,"sessions":9,"skipped":1}"#,
        )
        .unwrap();
        assert_eq!(r.hits[0].match_end, 7);
        assert!(r.partial);
        assert_eq!(r.sessions, 9);
    }
}
