//! Agent question cards (#1338). Rendered above the composer next to the
//! approval cards; posts `question.reply` through the normal ops route.
//! Parsing and answer rules live in `questions`.

use leptos::prelude::*;
use leptos::task::spawn_local;
use wasm_bindgen::JsCast;

use crate::api::post_op;
use crate::questions::{
    apply_note, collect_answers, digit_pick, dismiss_data, notes_from_envelope, reply_data,
    step_label, Draft, Pending, Pick,
};

/// Feed one live envelope into the pending-question list.
pub fn apply_question_notes(raw: &str, questions: RwSignal<Vec<Pending>>) {
    let notes = notes_from_envelope(raw);
    if notes.is_empty() {
        return;
    }
    questions.update(|list| {
        for note in notes {
            apply_note(list, note);
        }
    });
}

#[component]
pub fn QuestionCards(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    questions: RwSignal<Vec<Pending>>,
) -> impl IntoView {
    view! {
        <Show when=move || !questions.get().is_empty()>
            <div class="question-cards">
                <For
                    each=move || questions.get()
                    key=|p| p.request_id.clone()
                    children=move |pending| view! {
                        <QuestionCard pending=pending base=base token=token root=root questions=questions />
                    }
                />
            </div>
        </Show>
    }
}

#[component]
fn QuestionCard(
    pending: Pending,
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    questions: RwSignal<Vec<Pending>>,
) -> impl IntoView {
    let n = pending.prompts.len();
    let step = RwSignal::new(0usize);
    let draft = RwSignal::new(Draft::new(n));
    let error = RwSignal::new(String::new());
    let sending = RwSignal::new(false);
    let pending = StoredValue::new(pending);

    // One post path for submit and dismiss. Success removes the card at
    // once; `question.resolved` would clear it as well.
    let post = move |data: serde_json::Value| {
        if sending.get_untracked() {
            return;
        }
        let id = pending.with_value(|p| p.request_id.clone());
        let (b, t, r) = (base.get_untracked(), token.get_untracked(), root.get_untracked());
        if r.is_empty() {
            error.set("No live session to answer.".into());
            return;
        }
        sending.set(true);
        spawn_local(async move {
            let res = post_op(&b, &t, &r, "question.reply", data).await;
            sending.set(false);
            match res {
                Ok(()) => questions.update(|list| list.retain(|p| p.request_id != id)),
                Err(e) => error.set(format!("Answer not sent: {e}")),
            }
        });
    };
    let dismiss = move || post(pending.with_value(|p| dismiss_data(&p.request_id)));
    let submit = move || {
        let res = pending.with_value(|p| draft.with_untracked(|d| collect_answers(p, d)));
        match res {
            Ok(answers) => {
                error.set(String::new());
                post(pending.with_value(|p| reply_data(&p.request_id, &answers)));
            }
            Err(i) => {
                step.set(i);
                error.set(format!("Answer question {} first.", i + 1));
            }
        }
    };
    // Next moves on when the current prompt is answered; on the last
    // prompt it submits the whole card in one reply.
    let advance = move || {
        let i = step.get_untracked();
        let answered = pending.with_value(|p| {
            draft.with_untracked(|d| p.prompts.get(i).and_then(|q| crate::questions::answer(q, d, i)).is_some())
        });
        if !answered {
            error.set("Pick a choice or write your own answer.".into());
            return;
        }
        error.set(String::new());
        if i + 1 < n {
            step.set(i + 1);
        } else {
            submit();
        }
    };

    let on_key = move |ev: web_sys::KeyboardEvent| {
        let key = ev.key();
        if key == "Escape" {
            ev.prevent_default();
            ev.stop_propagation();
            dismiss();
            return;
        }
        if key == "Enter" && !ev.shift_key() {
            if is_button(&ev) {
                return;
            }
            ev.prevent_default();
            ev.stop_propagation();
            advance();
            return;
        }
        if typing_target(&ev) {
            return;
        }
        let i = step.get_untracked();
        if let Some(pick) = pending.with_value(|p| p.prompts.get(i).and_then(|q| digit_pick(q, &key))) {
            ev.prevent_default();
            ev.stop_propagation();
            draft.update(|d| d.pick(i, pick));
            error.set(String::new());
        }
    };

    let prompts = pending.with_value(|p| p.prompts.clone());
    let request_id = pending.with_value(|p| p.request_id.clone());
    view! {
        <article
            class="question-card"
            aria-live="polite"
            aria-label="Question from the agent"
            data-pending-question=request_id.clone()
            tabindex="-1"
            on:keydown=on_key
        >
            <header>
                <span class="badge badge-blocked">"Needs you"</span>
                <span class="who">"Question from the agent"</span>
            </header>
            {prompts.into_iter().enumerate().map(|(i, prompt)| {
                let label = step_label(&prompt, i, n);
                let group = format!("{request_id}-q{i}");
                let free_form = prompt.options.is_empty();
                let own_index = prompt.options.len();
                let question = prompt.question.clone();
                view! {
                    <section class="question-step" data-question-step=i.to_string() hidden=move || step.get() != i>
                        <p class="question-label">{label}</p>
                        <p class="question-text">{question}</p>
                        <Show when=move || !free_form>
                            <ul class="question-options" role="radiogroup">
                                {prompt.options.iter().enumerate().map(|(j, choice)| {
                                    let label = format!("{}. {}", j + 1, choice.label);
                                    let description = choice.description.clone();
                                    let has_description = !description.is_empty();
                                    let group = group.clone();
                                    view! {
                                        <li>
                                            <label class="question-option">
                                                <input
                                                    type="radio"
                                                    name=group
                                                    data-question-option=j.to_string()
                                                    prop:checked=move || draft.with(|d| d.picked(i) == Pick::Option(j))
                                                    on:change=move |_| {
                                                        draft.update(|d| d.pick(i, Pick::Option(j)));
                                                        error.set(String::new());
                                                    }
                                                />
                                                <span class="question-option-label">{label}</span>
                                                <Show when=move || has_description>
                                                    <span class="question-option-detail">{description.clone()}</span>
                                                </Show>
                                            </label>
                                        </li>
                                    }
                                }).collect_view()}
                                <li>
                                    <label class="question-option">
                                        <input
                                            type="radio"
                                            name=group.clone()
                                            data-question-own="true"
                                            prop:checked=move || draft.with(|d| d.picked(i) == Pick::Own)
                                            on:change=move |_| draft.update(|d| d.pick(i, Pick::Own))
                                        />
                                        <span class="question-option-label">{format!("{}. Your own answer", own_index + 1)}</span>
                                    </label>
                                </li>
                            </ul>
                        </Show>
                        <Show when=move || free_form || draft.with(|d| d.picked(i) == Pick::Own)>
                            <input
                                type="text"
                                class="question-own"
                                data-question-text=i.to_string()
                                aria-label="Your answer"
                                placeholder="Type your answer"
                                prop:value=move || draft.with(|d| d.own_text(i))
                                on:input=move |ev| {
                                    draft.update(|d| d.set_own(i, event_target_value(&ev)));
                                    error.set(String::new());
                                }
                            />
                        </Show>
                    </section>
                }
            }).collect_view()}
            <Show when=move || !error.get().is_empty()>
                <p class="question-error" role="alert">{move || error.get()}</p>
            </Show>
            <p class="hint keys">"Enter goes on, 1-9 pick a choice. Escape dismisses the question and stops the turn."</p>
            <div class="question-actions">
                <button
                    type="button"
                    class="secondary"
                    data-question-back="true"
                    disabled=move || sending.get() || step.get() == 0
                    on:click=move |_| step.update(|s| *s = s.saturating_sub(1))
                >"Back"</button>
                <button
                    type="button"
                    data-question-next="true"
                    disabled=move || sending.get()
                    on:click=move |_| advance()
                >{move || if step.get() + 1 < n { "Next" } else { "Send answers" }}</button>
                <button
                    type="button"
                    class="danger"
                    data-question-dismiss="true"
                    title="Dismiss: the agent gets no answer and the turn stops"
                    disabled=move || sending.get()
                    on:click=move |_| dismiss()
                >"Dismiss"</button>
            </div>
        </article>
    }
}

fn target_tag(ev: &web_sys::KeyboardEvent) -> String {
    ev.target()
        .and_then(|t| t.dyn_ref::<web_sys::Element>().map(|el| el.tag_name().to_ascii_lowercase()))
        .unwrap_or_default()
}

fn is_button(ev: &web_sys::KeyboardEvent) -> bool {
    target_tag(ev) == "button"
}

fn typing_target(ev: &web_sys::KeyboardEvent) -> bool {
    let Some(el) = ev.target().and_then(|t| t.dyn_into::<web_sys::HtmlInputElement>().ok()) else {
        return matches!(target_tag(ev).as_str(), "textarea" | "select");
    };
    el.type_() == "text"
}
