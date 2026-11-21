//! Debug mode panel (#1345). The daemon owns the session (phase gate,
//! hypotheses, evidence, sweep) and reports it on `debug.updated`; this
//! panel shows that state and sends `debug.control` ops. Rules (merge,
//! checklist, gating, summary) live in the `debug-mode` crate.
//!
//! Desktop: an inline card above the composer. Phone width: a status chip
//! that opens the same content as a bottom sheet.

use std::rc::Rc;

use debug_mode::{Action, Evidence, Hit, Hypothesis, State};
use leptos::prelude::*;
use leptos::task::spawn_local;
use serde_json::{json, Value};

use crate::api::post_op;

/// The root session's debug state carried by one live envelope, if any.
pub fn state_from_envelope(raw: &str) -> Option<State> {
    match protocol_schema::decode_event(raw) {
        Ok(protocol_schema::Event::DebugUpdated(d)) if d.parent_session_id.is_empty() && d.depth == 0 => {
            Some(from_wire(&d))
        }
        _ => None,
    }
}

fn from_wire(d: &protocol_schema::DebugUpdated) -> State {
    let hit = |h: &protocol_schema::DebugHit| Hit { path: h.path.clone(), line: h.line, text: h.text.clone() };
    State {
        action: d.action.clone(),
        phase: d.phase.clone(),
        hypotheses: d
            .hypotheses
            .iter()
            .map(|h| Hypothesis {
                id: h.id.clone(),
                rank: h.rank,
                statement: h.statement.clone(),
                confirms: h.confirms.clone(),
                refutes: h.refutes.clone(),
                status: if h.status.is_empty() { "open".into() } else { h.status.clone() },
                evidence: h.evidence.iter().map(|e| Evidence { source: e.source.clone(), text: e.text.clone() }).collect(),
            })
            .collect(),
        override_reason: d.r#override.clone(),
        fixed: d.fixed,
        instrumentation: d.instrumentation.iter().map(hit).collect(),
        removed: d.removed.iter().map(hit).collect(),
        error: d.error.clone(),
    }
}

/// Feed one live envelope into the panel state.
pub fn apply_debug_envelope(raw: &str, debug: RwSignal<Option<State>>) {
    if let Some(next) = state_from_envelope(raw) {
        debug.update(|cur| *cur = debug_mode::apply(cur.take(), next));
    }
}

/// `debug.control` op body for an action with optional fields.
pub fn control(action: &str, extra: Value) -> Value {
    let mut body = json!({ "action": action });
    if let (Some(dst), Value::Object(src)) = (body.as_object_mut(), extra) {
        dst.extend(src);
    }
    body
}

#[component]
pub fn DebugPanel(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    debug: RwSignal<Option<State>>,
) -> impl IntoView {
    let open = RwSignal::new(!crate::mobile::is_phone());
    let sending = RwSignal::new(false);
    let local_error = RwSignal::new(String::new());
    let hyp_text = RwSignal::new(String::new());
    let evidence_text = RwSignal::new(String::new());
    let evidence_for = RwSignal::new(String::new());
    let override_text = RwSignal::new(String::new());

    // Copy (captures only signals) so it can live inside <Show> children,
    // which must be Send + Sync.
    let send = move |action: &'static str, extra: Value| {
        if sending.get_untracked() {
            return;
        }
        let (b, t, r) = (base.get_untracked(), token.get_untracked(), root.get_untracked());
        if r.is_empty() {
            local_error.set("No live session for debug mode.".into());
            return;
        }
        local_error.set(String::new());
        sending.set(true);
        let data = control(action, extra);
        spawn_local(async move {
            let res = post_op(&b, &t, &r, "debug.control", data).await;
            sending.set(false);
            if let Err(e) = res {
                local_error.set(format!("Debug action not sent: {e}"));
            }
            // On success the daemon's debug.updated refreshes the panel.
        });
    };

    // Palette entry (#1267): Debug → Start debug session.
    {
        crate::shell::bind(
            "debug-mode.start",
            Rc::new(move |_args: &str| {
                if root.get_untracked().is_empty() {
                    return Err("No live session to debug.".into());
                }
                if !debug_mode::can(debug.get_untracked().as_ref(), Action::Start) {
                    return Err("A debug session is already running.".into());
                }
                open.set(true);
                send("start", json!({}));
                Ok(())
            }),
        );
        on_cleanup(|| crate::shell::unbind("debug-mode.start"));
    }

    let visible = move || debug.with(|d| d.as_ref().is_some_and(|s| s.active() || s.closed() || !s.error.is_empty()));
    let can = move |a: Action| debug.with(|d| debug_mode::can(d.as_ref(), a)) && !sending.get();
    let phase = move || debug.with(|d| d.as_ref().map(|s| s.phase.clone()).unwrap_or_default());
    let chip = move || debug.with(|d| d.as_ref().map(debug_mode::chip_text).unwrap_or_default());
    let error = move || {
        let local = local_error.get();
        if !local.is_empty() {
            return local;
        }
        debug.with(|d| d.as_ref().map(|s| s.error.clone()).unwrap_or_default())
    };

    let set_hyps = move || match debug_mode::parse_hypotheses(&hyp_text.get_untracked()) {
        Ok(drafts) => {
            let list: Vec<Value> = drafts
                .into_iter()
                .map(|d| json!({"id": d.id, "statement": d.statement, "confirms": d.confirms, "refutes": d.refutes}))
                .collect();
            send("hypotheses", json!({ "hypotheses": list }));
        }
        Err(msg) => local_error.set(msg),
    };
    let attach = move || {
        let text = evidence_text.get_untracked();
        let mut id = evidence_for.get_untracked();
        if id.is_empty() {
            id = debug.with_untracked(|d| d.as_ref().and_then(|s| s.hypotheses.first()).map(|h| h.id.clone()).unwrap_or_default());
        }
        if text.trim().is_empty() || id.is_empty() {
            local_error.set("Paste the reproduced output and pick the hypothesis it bears on.".into());
            return;
        }
        send("evidence", json!({"hypothesisId": id, "source": "repro", "text": text}));
        evidence_text.set(String::new());
    };
    let unlock_override = move || {
        let reason = override_text.get_untracked();
        if reason.trim().is_empty() {
            local_error.set("Say why you are overriding the evidence gate; the reason is recorded in the transcript.".into());
            return;
        }
        send("unlock", json!({ "text": reason }));
    };

    view! {
        <Show when=visible>
            <section class="debug-panel" data-debug-phase=phase aria-label="Debug session">
                <button
                    type="button"
                    class="debug-chip"
                    data-debug-chip="true"
                    aria-expanded=move || open.get().to_string()
                    on:click=move |_| open.update(|o| *o = !*o)
                >{chip}</button>
                <div class="debug-sheet" class:open=move || open.get() data-debug-sheet="true">
                    <header class="debug-head">
                        <span class="debug-title">"Debug session"</span>
                        <span class="hint">{move || format!("Phase: {}", debug_mode::phase_label(&phase()))}</span>
                        <button type="button" class="secondary debug-close" aria-label="Collapse debug panel" on:click=move |_| open.set(false)>"Collapse"</button>
                    </header>
                    <Show when=move || !error().is_empty()>
                        <p class="debug-error" role="alert" data-debug-error="true">{error}</p>
                    </Show>

                    <Show when=move || can(Action::SetHypotheses)>
                        <div class="debug-block debug-hyp-input">
                            <label for="debug-hyps">"Hypotheses, ranked, one per line: statement | confirms | refutes"</label>
                            <textarea
                                id="debug-hyps"
                                rows="4"
                                data-debug-hypotheses-input="true"
                                prop:value=move || hyp_text.get()
                                on:input=move |ev| hyp_text.set(event_target_value(&ev))
                            ></textarea>
                            <button type="button" data-debug-set-hypotheses="true" disabled=move || sending.get() on:click=move |_| set_hyps()>"Set hypotheses"</button>
                        </div>
                    </Show>

                    <ol class="debug-hyps" data-debug-hypotheses="true">
                        <For
                            each=move || debug.with(|d| d.as_ref().map(|s| s.hypotheses.clone()).unwrap_or_default())
                            key=|h| (h.id.clone(), h.status.clone(), h.evidence.len())
                            children=move |h: Hypothesis| {
                                    let id_c = h.id.clone();
                                    let id_r = h.id.clone();
                                    let (mark_c, mark_r) = (send, send);
                                    let status = h.status.clone();
                                    let badge = match status.as_str() {
                                        "confirmed" => "badge badge-done",
                                        "refuted" => "badge badge-stopped",
                                        _ => "badge badge-running",
                                    };
                                    let has_confirms = !h.confirms.is_empty();
                                    let has_refutes = !h.refutes.is_empty();
                                    let (confirms, refutes) = (h.confirms.clone(), h.refutes.clone());
                                    view! {
                                        <li class="debug-hyp" data-debug-hypothesis=h.id.clone() data-status=status.clone()>
                                            <div class="debug-hyp-head">
                                                <span class="debug-rank">{format!("#{}", h.rank)}</span>
                                                <span class="debug-statement">{h.statement.clone()}</span>
                                                <span class=badge>{status.clone()}</span>
                                            </div>
                                            <Show when=move || has_confirms>
                                                <p class="hint">{format!("Confirms: {confirms}")}</p>
                                            </Show>
                                            <Show when=move || has_refutes>
                                                <p class="hint">{format!("Refutes: {refutes}")}</p>
                                            </Show>
                                            <ul class="debug-evidence">
                                                {h.evidence.iter().map(|e| view! {
                                                    <li><span class="hint">{e.source.clone()}</span><pre>{e.text.clone()}</pre></li>
                                                }).collect_view()}
                                            </ul>
                                            <div class="debug-actions">
                                                <button type="button" class="secondary" data-debug-confirm=h.id.clone()
                                                    disabled=move || !can(Action::Mark)
                                                    on:click=move |_| mark_c("mark", json!({"hypothesisId": id_c, "status": "confirmed"}))
                                                >"Confirm"</button>
                                                <button type="button" class="secondary" data-debug-refute=h.id.clone()
                                                    disabled=move || !can(Action::Mark)
                                                    on:click=move |_| mark_r("mark", json!({"hypothesisId": id_r, "status": "refuted"}))
                                                >"Refute"</button>
                                            </div>
                                        </li>
                                    }
                            }
                        />
                    </ol>

                    <div class="debug-block">
                        <p class="debug-subtitle">"Reproduce"</p>
                        <ul class="debug-checklist" data-debug-checklist="true">
                            {move || debug.with(|d| d.as_ref().map(debug_mode::checklist).unwrap_or_default()).into_iter().map(|st| view! {
                                <li class:done=st.done data-done=st.done.to_string()>
                                    <span class="debug-check" aria-hidden="true">{if st.done { "✓" } else { "○" }}</span>
                                    {st.label}
                                </li>
                            }).collect_view()}
                        </ul>
                        <Show when=move || can(Action::Evidence)>
                            <div class="debug-evidence-form">
                                <label for="debug-evidence-for">"Bears on"</label>
                                <select id="debug-evidence-for" data-debug-evidence-for="true" on:change=move |ev| evidence_for.set(event_target_value(&ev))>
                                    {move || debug.with(|d| d.as_ref().map(|s| s.hypotheses.clone()).unwrap_or_default()).into_iter().map(|h| view! {
                                        <option value=h.id.clone()>{format!("#{} {}", h.rank, h.statement)}</option>
                                    }).collect_view()}
                                </select>
                                <textarea
                                    rows="3"
                                    aria-label="Reproduced output"
                                    placeholder="Paste the output or log captured while reproducing the bug"
                                    data-debug-evidence-input="true"
                                    prop:value=move || evidence_text.get()
                                    on:input=move |ev| evidence_text.set(event_target_value(&ev))
                                ></textarea>
                                <button type="button" data-debug-attach="true" disabled=move || sending.get() on:click=move |_| attach()>"Attach evidence"</button>
                            </div>
                        </Show>
                    </div>

                    <div class="debug-block" data-debug-instrumentation="true">
                        <p class="debug-subtitle">
                            {move || {
                                let n = debug.with(|d| d.as_ref().map(|s| s.instrumentation.len()).unwrap_or(0));
                                format!("Instrumentation: {n} tagged line(s) remaining")
                            }}
                        </p>
                        <p class="hint">{format!("Probes must carry {} on the same line; ending the session removes those lines.", debug_mode::marker())}</p>
                        <ul class="debug-hits">
                            {move || debug.with(|d| d.as_ref().map(|s| s.instrumentation.clone()).unwrap_or_default()).into_iter().map(|h| view! {
                                <li data-debug-hit=format!("{}:{}", h.path, h.line)><code>{format!("{}:{}", h.path, h.line)}</code>" "<span class="hint">{h.text.trim().to_string()}</span></li>
                            }).collect_view()}
                        </ul>
                        <Show when=move || can(Action::Scan)>
                            <button type="button" class="secondary" data-debug-scan="true" on:click=move |_| send("scan", json!({}))>"Rescan"</button>
                        </Show>
                    </div>

                    <div class="debug-actions debug-phase-actions">
                        <Show when=move || can(Action::Unlock)>
                            <button type="button" data-debug-unlock="true" on:click=move |_| send("unlock", json!({}))>"Unlock fix"</button>
                        </Show>
                        <Show when=move || can(Action::Override)>
                            <div class="debug-override">
                                <input
                                    type="text"
                                    aria-label="Override reason"
                                    placeholder="Override reason (recorded)"
                                    data-debug-override-input="true"
                                    prop:value=move || override_text.get()
                                    on:input=move |ev| override_text.set(event_target_value(&ev))
                                />
                                <button type="button" class="secondary" data-debug-override="true" on:click=move |_| unlock_override()>"Override and unlock"</button>
                            </div>
                        </Show>
                        <Show when=move || can(Action::MarkFixed)>
                            <button type="button" data-debug-fixed="true" on:click=move |_| send("fixed", json!({}))>"Mark bug fixed"</button>
                        </Show>
                        <Show when=move || can(Action::End)>
                            <button type="button" class="danger" data-debug-end="true" on:click=move |_| send("end", json!({}))>"End debug session"</button>
                        </Show>
                        <Show when=move || can(Action::Start)>
                            <button type="button" class="secondary" data-debug-start="true" on:click=move |_| send("start", json!({}))>"Start a new debug session"</button>
                        </Show>
                    </div>

                    <Show when=move || debug.with(|d| d.as_ref().is_some_and(|s| s.closed()))>
                        <SummaryCard debug=debug />
                    </Show>
                </div>
            </section>
        </Show>
    }
}

#[component]
fn SummaryCard(debug: RwSignal<Option<State>>) -> impl IntoView {
    let sum = move || debug.with(|d| d.as_ref().map(debug_mode::summary));
    view! {
        <article class="debug-summary" data-debug-summary="true">
            {move || sum().map(|s| {
                let cause = if s.root_cause.is_empty() {
                    format!("No confirmed hypothesis. Override: {}", s.override_reason)
                } else {
                    s.root_cause.join("; ")
                };
                view! {
                    <p class="debug-subtitle">"Summary"</p>
                    <dl>
                        <dt>"Root cause"</dt><dd>{cause}</dd>
                        <dt>"Evidence"</dt><dd>{format!("{} item(s)", s.evidence)}</dd>
                        <dt>"Cleanup"</dt>
                        <dd data-debug-remaining=s.remaining.to_string()>{format!(
                            "{} tagged line(s) swept, {} remaining{}",
                            s.swept,
                            s.remaining,
                            if s.verified { " (verified)" } else { "" }
                        )}</dd>
                    </dl>
                }
            })}
        </article>
    }
}
