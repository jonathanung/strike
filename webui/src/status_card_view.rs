//! Zero-turn status card view (#1397).
//!
//! Renders `status_card` derivations from data the cockpit already holds:
//! team members from `GET /v1/team` (`objective`, `state`,
//! `terminalSummary`, `blockReason`, `budget.{elapsedS,tokensUsed,
//! toolCalls,costUsdUsed}`), the root row from `GET /v1/roots` / status
//! payloads (`busy`, `permissionPending`, `questionPending`,
//! `lastActivityAt`) and the saved children list. No fetch, no model turn.
//! Derivation rules and tests live in the `webui/status-card` crate.

use leptos::prelude::*;
use status_card::{Bucket, Card, Figure, NextStep, Recap, SessionCard, Source, DASH};

use crate::api::now_ms;
use crate::model::{ActivityRow, ChildSession, RootSummary};

/// Map an activity row onto the card's input record.
pub fn member_of(row: &ActivityRow) -> status_card::Member {
    status_card::Member {
        session_id: row.session_id.clone(),
        name: row.name.clone(),
        state: row.state.clone(),
        objective: row.task.clone(),
        summary: row.summary.clone(),
        block_reason: row.block_reason.clone(),
        budget: row.budget,
    }
}

/// The active root's daemon status, or unknown when it is not listed.
pub fn root_state(roots: &[RootSummary], id: &str) -> status_card::RootState {
    roots
        .iter()
        .find(|r| !id.is_empty() && r.id == id)
        .map(|r| status_card::RootState {
            busy: r.busy,
            permission_pending: r.permission_pending,
            question_pending: r.question_pending,
            last_activity_ms: r.last_activity_at,
        })
        .unwrap_or_default()
}

fn child_titles(children: &[ChildSession], active: &str) -> Vec<String> {
    children
        .iter()
        .filter(|c| !active.is_empty() && c.parent_id == active)
        .map(|c| {
            if c.title.is_empty() {
                c.id.clone()
            } else {
                c.title.clone()
            }
        })
        .collect()
}

const CHIP_TITLE: &str = "Written by the agent, not measured by the engine";

fn chip_view(chip: Option<&'static str>) -> impl IntoView {
    chip.map(|c| view! { <span class="sc-chip" data-provenance="agent" title=CHIP_TITLE>{c}</span> })
}

fn figure_view(f: Figure) -> impl IntoView {
    let chip = f.chip();
    let measured = if f.value.is_some() { "true" } else { "false" };
    let source = match f.source {
        Source::Engine => "engine",
        Source::Agent => "agent",
    };
    let text = f.text();
    let label = f.label;
    view! {
        <div class="sc-fig" data-measured=measured data-source=source>
            <dt>{label}</dt>
            <dd><span class="sc-val">{text}</span>{chip_view(chip)}</dd>
        </div>
    }
}

fn next_view(next: Option<NextStep>) -> impl IntoView {
    next.map(|n| {
        let chip = n.chip();
        let text = n.text;
        view! {
            <p class="sc-next" data-next-step="true">
                <span class="sc-label">"Next step"</span>
                <span class="sc-val">{text}</span>
                {chip_view(chip)}
            </p>
        }
    })
}

fn bucket_view(bucket: Bucket, kind: &'static str, names: Vec<String>) -> impl IntoView {
    let count = names.len().to_string();
    let body = if names.is_empty() {
        DASH.to_string()
    } else {
        names.join(", ")
    };
    view! {
        <div class="sc-bucket" data-bucket=kind>
            <span class="sc-count">{count}</span>
            <span class="sc-label">{bucket.label()}</span>
            <span class="sc-names">{body}</span>
        </div>
    }
}

fn list_view(items: Vec<String>, empty: &'static str) -> AnyView {
    if items.is_empty() {
        return view! { <p class="hint">{empty}</p> }.into_any();
    }
    view! {
        <ul class="plain sc-list">
            {items.into_iter().map(|item| view! { <li>{item}</li> }).collect_view()}
        </ul>
    }
    .into_any()
}

fn live_view(card: SessionCard) -> AnyView {
    view! {
        <section class="status-card sc-live" data-status-card="live" aria-label="Session status">
            <header class="sc-head">
                <strong>"Status"</strong>
                <span class="hint">"From the record, no turn spent"</span>
            </header>
            <div class="sc-buckets">
                {bucket_view(Bucket::Finished, "finished", card.finished)}
                {bucket_view(Bucket::Running, "running", card.running)}
                {bucket_view(Bucket::Waiting, "waiting", card.waiting)}
            </div>
            {next_view(card.next)}
        </section>
    }
    .into_any()
}

fn recap_view(recap: Recap) -> AnyView {
    let quiet = format!("Quiet for {}", status_card::fmt_idle(recap.idle_ms));
    view! {
        <section class="status-card sc-recap" data-status-card="recap" aria-label="Idle recap">
            <header class="sc-head">
                <strong>"While you were away"</strong>
                <span class="hint">{quiet}</span>
            </header>
            <p class="sc-label">"Left open"</p>
            {list_view(recap.open, "Nothing left open.")}
            <p class="sc-label">"Recent child sessions"</p>
            {list_view(recap.recent, "No child sessions recorded.")}
        </section>
    }
    .into_any()
}

/// Session summary card at the top of the Activity tab.
#[component]
pub fn SessionStatusCard(
    activity: RwSignal<Vec<ActivityRow>>,
    roots: RwSignal<Vec<RootSummary>>,
    active_root: RwSignal<String>,
    children: RwSignal<Vec<ChildSession>>,
    tick: RwSignal<u32>,
) -> impl IntoView {
    move || {
        let _ = tick.get();
        let active = active_root.get();
        let members: Vec<status_card::Member> = activity.get().iter().map(member_of).collect();
        let root = root_state(&roots.get(), &active);
        let kids = child_titles(&children.get(), &active);
        match status_card::session_card(&root, &members, &kids, now_ms() as i64) {
            Card::Empty => view! { <span></span> }.into_any(),
            Card::Live(card) => live_view(card),
            Card::Recap(recap) => recap_view(recap),
        }
    }
}

/// One child's card, shown when its activity row is opened.
#[component]
pub fn ChildStatusCard(row: ActivityRow) -> impl IntoView {
    let card = status_card::child_card(&member_of(&row));
    let goal = card.goal.clone().unwrap_or_else(|| DASH.to_string());
    let state = if card.bucket == Bucket::Other {
        if row.state.is_empty() {
            DASH.to_string()
        } else {
            row.state.clone()
        }
    } else {
        card.bucket.label().to_string()
    };
    let figures = card.figures;
    let next = card.next;
    view! {
        <section class="status-card sc-child" data-status-card="child">
            <p class="sc-goal"><span class="sc-label">"Goal"</span><span class="sc-val">{goal}</span></p>
            <p class="sc-state"><span class="sc-label">"Bucket"</span><span class="sc-val">{state}</span></p>
            <dl class="sc-figs">{figures.into_iter().map(figure_view).collect_view()}</dl>
            {next_view(next)}
        </section>
    }
}
