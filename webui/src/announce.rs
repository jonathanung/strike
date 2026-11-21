//! Global live regions for the cockpit (#1337, #1878).
//!
//! Two visually hidden `aria-live` regions at the shell root — outside any
//! pane a phone layout can hide — so approvals, agent questions, turn
//! endings and session status changes reach screen-reader users whichever
//! view is open. Wording and urgency come
//! from the `a11y-announce` crate; this module owns the DOM and the
//! watchers.

use a11y_announce::{
    live_text, new_asks, new_questions, session_changes, turn_end, Announcement, Politeness,
    SessionState,
};
use leptos::prelude::*;
use wasm_bindgen::JsCast;

use crate::approvals::Ask;
use crate::model::{session_label, RootSummary};
use crate::questions::Pending;
use crate::status::session_kind;
use crate::transcript::{Role, Transcript};

#[derive(Clone, Copy)]
pub struct Announcer {
    polite: RwSignal<String>,
    assertive: RwSignal<String>,
    seq: RwSignal<u32>,
}

impl Announcer {
    pub fn new() -> Self {
        Self {
            polite: RwSignal::new(String::new()),
            assertive: RwSignal::new(String::new()),
            seq: RwSignal::new(0),
        }
    }

    pub fn say(&self, a: Announcement) {
        let n = self.seq.get_untracked().wrapping_add(1);
        self.seq.set(n);
        let text = live_text(&a.text, n);
        match a.politeness {
            Politeness::Polite => self.polite.set(text),
            Politeness::Assertive => self.assertive.set(text),
        }
    }
}

#[component]
pub fn LiveRegions(announcer: Announcer) -> impl IntoView {
    view! {
        <div id="a11y-polite" class="sr-only" aria-live="polite" aria-atomic="true" data-a11y-live="polite">
            {move || announcer.polite.get()}
        </div>
        <div id="a11y-assertive" class="sr-only" aria-live="assertive" aria-atomic="true" data-a11y-live="assertive">
            {move || announcer.assertive.get()}
        </div>
    }
}

/// Announce new permission asks (assertive) and move focus to the new
/// approval card unless the user is typing.
pub fn watch_asks(announcer: Announcer, asks: RwSignal<Vec<Ask>>) {
    let seen = StoredValue::new(Vec::<String>::new());
    Effect::new(move |_| {
        let current: Vec<(String, String)> = asks.with(|list| {
            list.iter()
                .map(|a| (a.request_id.clone(), a.permission.clone()))
                .collect()
        });
        let prev = seen.get_value();
        if let Some(a) = new_asks(&prev, &current) {
            announcer.say(a);
            focus_new_card(".approvals-actions button");
        }
        seen.set_value(current.into_iter().map(|(id, _)| id).collect());
    });
}

/// Announce new agent questions (assertive) and move focus into the new
/// question card unless the user is typing (#1878).
pub fn watch_questions(announcer: Announcer, questions: RwSignal<Vec<Pending>>) {
    let seen = StoredValue::new(Vec::<String>::new());
    Effect::new(move |_| {
        let current: Vec<(String, String)> = questions.with(|list| {
            list.iter()
                .map(|p| {
                    let text = p.prompts.first().map(|q| q.question.clone()).unwrap_or_default();
                    (p.request_id.clone(), text)
                })
                .collect()
        });
        let prev = seen.get_value();
        if let Some(a) = new_questions(&prev, &current) {
            announcer.say(a);
            focus_new_card(
                ".question-card button:not([disabled]), .question-card input:not([disabled]), .question-card textarea:not([disabled])",
            );
        }
        seen.set_value(current.into_iter().map(|(id, _)| id).collect());
    });
}

/// Announce session status changes (polite, #1263 / #1878).
pub fn watch_sessions(announcer: Announcer, roots: RwSignal<Vec<RootSummary>>, active_root: RwSignal<String>) {
    let last = StoredValue::new(Vec::<SessionState>::new());
    Effect::new(move |_| {
        let current: Vec<SessionState> = roots.with(|list| {
            list.iter()
                .map(|row| {
                    let kind = session_kind(&row.status, row.busy, row.permission_pending, row.question_pending);
                    SessionState {
                        id: row.id.clone(),
                        label: session_label(&row.title, &crate::short_id(&row.id)),
                        kind: kind.id().to_string(),
                        word: kind.label().to_string(),
                    }
                })
                .collect()
        });
        let active = active_root.get_untracked();
        let prev = last.get_value();
        if let Some(a) = session_changes(&prev, &current, &active) {
            announcer.say(a);
        }
        last.set_value(current);
    });
}

/// Announce the active session's turn ending (polite).
pub fn watch_turns(
    announcer: Announcer,
    active_root: RwSignal<String>,
    busy: Memo<bool>,
    transcript: RwSignal<Transcript>,
) {
    let last = StoredValue::new((String::new(), false));
    Effect::new(move |_| {
        let root = active_root.get();
        let now = busy.get();
        let (prev_root, was) = last.get_value();
        let notice = transcript.with_untracked(|t| {
            t.messages
                .last()
                .filter(|m| m.role == Role::Notice)
                .map(|m| m.title.clone())
        });
        if let Some(a) = turn_end(was, now, prev_root == root, notice.as_deref()) {
            announcer.say(a);
        }
        last.set_value((root, now));
    });
}

/// Focus the first control matching `selector` (the newest approval or
/// question card), after it renders. Never steals focus from a text field
/// the user is typing in.
fn focus_new_card(selector: &'static str) {
    crate::clipboard::after_ms(0, move || {
        let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
            return;
        };
        if let Some(active) = doc.active_element() {
            let tag = active.tag_name();
            let typing = matches!(tag.as_str(), "INPUT" | "TEXTAREA" | "SELECT")
                || active.get_attribute("contenteditable").is_some();
            if typing {
                return;
            }
        }
        if let Ok(Some(el)) = doc.query_selector(selector) {
            if let Ok(el) = el.dyn_into::<web_sys::HtmlElement>() {
                let _ = el.focus();
            }
        }
    });
}
