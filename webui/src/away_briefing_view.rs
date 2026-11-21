//! "While you were away" panel (#1488).
//!
//! On cockpit open the panel reads the daemon watermark
//! (`GET /v1/briefing/seen`), and when the away gap passes the threshold it
//! fetches the records it groups: `GET /v1/notifications` (finished /
//! failed / blocked children, finished sessions) and `GET /v1/schedules`
//! (`lastFire`, `lastStatus`). Live waits come from the roots signal the
//! cockpit already polls (`permissionPending`, `questionPending`). Grouping
//! rules and tests live in the `webui/away-briefing` crate.
//!
//! Watermark rules:
//! - never seen (`seenAt == 0`): set it to now silently, show nothing;
//! - under the threshold, or nothing to report: advance it to now;
//! - while no panel is showing and the page is visible, advance it every
//!   few minutes so time spent in the cockpit is not briefed later;
//! - "Mark caught up" advances it to the daemon time the briefing was
//!   computed at, for every device. "Hide" only hides this page view.
//!
//! A source that fails to load is dropped, never guessed (issue Q4).

use leptos::callback::{Callable, Callback};
use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{get_json, now_ms, post_json};
use crate::model::RootSummary;
use crate::notify::FocusTarget;
use away_briefing::{Briefing, Group, Note, ScheduleRun, Waiting};

/// Heartbeat that keeps the watermark current while the user is here.
const HEARTBEAT_MS: i32 = 5 * 60 * 1000;

#[derive(Clone, Debug, Default, Deserialize)]
struct SeenBody {
    #[serde(default, rename = "seenAt")]
    seen_at: i64,
    #[serde(default)]
    now: i64,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct NoteDto {
    #[serde(default)]
    id: String,
    #[serde(default)]
    kind: String,
    #[serde(default, rename = "sessionId")]
    session_id: String,
    #[serde(default, rename = "subjectId")]
    subject_id: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    body: String,
    #[serde(default)]
    href: String,
    #[serde(default)]
    at: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct NotesBody {
    #[serde(default)]
    notifications: Vec<NoteDto>,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct ScheduleDto {
    #[serde(default)]
    id: String,
    #[serde(default)]
    name: String,
    #[serde(default, rename = "lastFire")]
    last_fire: String,
    #[serde(default, rename = "lastStatus")]
    last_status: String,
    #[serde(default, rename = "lastSessionId")]
    last_session_id: String,
    #[serde(default, rename = "lastError")]
    last_error: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct SchedulesBody {
    #[serde(default)]
    schedules: Vec<ScheduleDto>,
}

/// Records fetched at open, before the live root waits are folded in.
#[derive(Clone, Debug, Default, PartialEq)]
struct Fetched {
    seen_at: i64,
    now: i64,
    notes: Vec<Note>,
    schedules: Vec<ScheduleRun>,
}

/// RFC 3339 to unix ms; `0` when empty or unparseable (never counted).
fn parse_ms(raw: &str) -> i64 {
    let raw = raw.trim();
    if raw.is_empty() {
        return 0;
    }
    let ms = js_sys::Date::parse(raw);
    if ms.is_finite() && ms > 0.0 {
        ms as i64
    } else {
        0
    }
}

fn waiting_from(roots: &[RootSummary]) -> Vec<Waiting> {
    roots
        .iter()
        .map(|r| Waiting {
            session_id: r.id.clone(),
            title: r.title.clone(),
            permission: r.permission_pending,
            question: r.question_pending,
        })
        .collect()
}

fn page_hidden() -> bool {
    web_sys::window()
        .and_then(|w| w.document())
        .map(|d| d.hidden())
        .unwrap_or(true)
}

fn advance(base: String, token: String, at: i64) {
    if at <= 0 {
        return;
    }
    spawn_local(async move {
        let _ = post_json(&base, &token, "/v1/briefing/seen", serde_json::json!({ "seenAt": at })).await;
    });
}

fn group_view(group: Group, on_focus: Callback<FocusTarget>) -> impl IntoView {
    let kind = group.kind;
    let count = group.rows.len().to_string();
    let hidden = group.hidden();
    let rows = group.shown().to_vec();
    view! {
        <section class="away-group" data-away-group={kind.id()}>
            <h3><span class="away-count">{count}</span>" "{kind.title()}</h3>
            <ul class="plain">
                {rows.into_iter().map(|row| {
                    let label = row.label.clone();
                    let detail = row.detail.clone();
                    if row.session_id.is_empty() {
                        view! {
                            <li><span class="away-label">{label}</span><span class="away-detail">{detail}</span></li>
                        }.into_any()
                    } else {
                        let target = FocusTarget { session_id: row.session_id.clone(), approval_id: row.approval_id.clone() };
                        view! {
                            <li>
                                <button type="button" class="link away-row" on:click=move |_| on_focus.run(target.clone())>
                                    <span class="away-label">{label}</span>
                                    <span class="away-detail">{detail}</span>
                                </button>
                            </li>
                        }.into_any()
                    }
                }).collect_view()}
            </ul>
            <Show when=move || { hidden > 0 }>
                <p class="hint">{format!("+{hidden} more")}</p>
            </Show>
        </section>
    }
}

/// Non-modal catch-up panel at the top of the chat pane.
#[component]
pub fn AwayBriefing(
    base: RwSignal<String>,
    token: RwSignal<String>,
    roots: RwSignal<Vec<RootSummary>>,
    on_focus: Callback<FocusTarget>,
) -> impl IntoView {
    let fetched = RwSignal::new(None::<Fetched>);
    let hidden = RwSignal::new(false);
    let expanded = RwSignal::new(false);
    let generation = RwSignal::new(0u32);

    let briefing = Memo::new(move |_| -> Option<Briefing> {
        let f = fetched.get()?;
        away_briefing::compute(f.seen_at, f.now, &f.notes, &waiting_from(&roots.get()), &f.schedules)
    });

    // Load on open and again when the gateway or token changes.
    Effect::new(move |_| {
        let (b, t) = (base.get(), token.get());
        let gen = generation.get_untracked().wrapping_add(1);
        generation.set(gen);
        fetched.set(None);
        hidden.set(false);
        spawn_local(async move {
            // Older daemons have no watermark route: no panel.
            let Ok(seen) = get_json::<SeenBody>(&b, &t, "/v1/briefing/seen").await else {
                return;
            };
            if generation.get_untracked() != gen {
                return;
            }
            if seen.seen_at <= 0 || !away_briefing::should_brief(seen.seen_at, seen.now) {
                advance(b, t, seen.now);
                return;
            }
            let notes = match get_json::<NotesBody>(&b, &t, "/v1/notifications").await {
                Ok(body) => body
                    .notifications
                    .into_iter()
                    .map(|n| Note {
                        at_ms: parse_ms(&n.at),
                        id: n.id,
                        kind: n.kind,
                        session_id: n.session_id,
                        subject_id: n.subject_id,
                        title: n.title,
                        body: n.body,
                        href: n.href,
                    })
                    .collect(),
                Err(_) => Vec::new(),
            };
            let schedules = match get_json::<SchedulesBody>(&b, &t, "/v1/schedules").await {
                Ok(body) => body
                    .schedules
                    .into_iter()
                    .map(|s| ScheduleRun {
                        last_fire_ms: parse_ms(&s.last_fire),
                        id: s.id,
                        name: s.name,
                        last_status: s.last_status,
                        last_session_id: s.last_session_id,
                        last_error: s.last_error,
                    })
                    .collect(),
                Err(_) => Vec::new(),
            };
            if generation.get_untracked() != gen {
                return;
            }
            let data = Fetched { seen_at: seen.seen_at, now: seen.now, notes, schedules };
            let waiting = waiting_from(&roots.get_untracked());
            if away_briefing::compute(data.seen_at, data.now, &data.notes, &waiting, &data.schedules).is_none() {
                // Empty delta: no panel, and the mark catches up.
                advance(b, t, data.now);
                return;
            }
            fetched.set(Some(data));
        });
    });

    // Heartbeat: keep the mark current while the user is here and no
    // briefing is waiting to be read. The App lives for the page, so the
    // closure is leaked with it.
    let beat = Closure::<dyn FnMut()>::new(move || {
        if page_hidden() || briefing.get_untracked().is_some() {
            return;
        }
        advance(base.get_untracked(), token.get_untracked(), now_ms() as i64);
    });
    if let Some(window) = web_sys::window() {
        let _ = window.set_interval_with_callback_and_timeout_and_arguments_0(
            beat.as_ref().unchecked_ref(),
            HEARTBEAT_MS,
        );
    }
    beat.forget();

    let mark_caught_up = move || {
        if let Some(b) = briefing.get_untracked() {
            advance(base.get_untracked(), token.get_untracked(), b.now_ms);
        }
        fetched.set(None);
    };

    move || {
        let Some(b) = briefing.get() else {
            return view! { <span></span> }.into_any();
        };
        if hidden.get() {
            return view! { <span></span> }.into_any();
        }
        let chip = b.chip();
        let gap = format!("Away {}", away_briefing::fmt_gap(b.now_ms - b.since_ms));
        let groups = b.groups.clone();
        view! {
            <section
                class=move || if expanded.get() { "away-briefing card away-open" } else { "away-briefing card" }
                data-briefing="true"
                aria-label="While you were away"
            >
                <button type="button" class="away-chip" aria-expanded=move || expanded.get().to_string() on:click=move |_| expanded.update(|v| *v = !*v)>
                    <strong>"While you were away"</strong>
                    <span class="away-chip-text">{chip.clone()}</span>
                </button>
                <div class="away-body">
                    <header class="away-head">
                        <strong>"While you were away"</strong>
                        <span class="hint">{gap}" · from daemon records"</span>
                    </header>
                    <div class="away-groups">
                        {groups.into_iter().map(|g| group_view(g, on_focus)).collect_view()}
                    </div>
                    <div class="row-actions">
                        <button type="button" class="primary away-caught-up" on:click=move |_| mark_caught_up()>"Mark caught up"</button>
                        <button type="button" class="secondary away-hide" on:click=move |_| hidden.set(true)>"Hide"</button>
                    </div>
                </div>
            </section>
        }
        .into_any()
    }
}
