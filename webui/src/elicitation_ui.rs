//! MCP elicitation cards (#1353). Shown above the composer with the other
//! "needs you" cards; posts `elicitation.reply` through the ops route.
//! Parsing and validation live in `elicitation`.

use leptos::prelude::*;
use leptos::task::spawn_local;

use crate::api::post_op;
use crate::elicitation::{
    apply_note, build_content, initial_values, input_type, label, notes_from_envelope,
    openable_url, reply_data, settled_label, Kind, Pending,
};

/// Feed one live envelope into the elicitation list.
pub fn apply_elicitation_notes(raw: &str, list: RwSignal<Vec<Pending>>) {
    let notes = notes_from_envelope(raw);
    if notes.is_empty() {
        return;
    }
    list.update(|rows| {
        for note in notes {
            apply_note(rows, note);
        }
    });
}

#[component]
pub fn ElicitationCards(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    elicitations: RwSignal<Vec<Pending>>,
) -> impl IntoView {
    view! {
        <Show when=move || !elicitations.get().is_empty()>
            <div class="elicit-cards">
                <For
                    each=move || elicitations.get()
                    key=|p| (p.request_id.clone(), p.settled.clone())
                    children=move |pending| view! {
                        <ElicitationCard pending=pending base=base token=token root=root />
                    }
                />
            </div>
        </Show>
    }
}

#[component]
fn ElicitationCard(
    pending: Pending,
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
) -> impl IntoView {
    let server = if pending.server.trim().is_empty() { "An MCP server".to_string() } else { pending.server.clone() };
    if !pending.settled.is_empty() {
        let outcome = settled_label(&pending.settled);
        return view! {
            <article class="elicit-card elicit-settled" data-elicitation-settled=pending.settled.clone()>
                <span class="who">{server}</span>
                <span class="hint">{format!(" asked for input · {outcome}")}</span>
            </article>
        }
        .into_any();
    }

    let values = RwSignal::new(initial_values(&pending.fields));
    let error = RwSignal::new(String::new());
    let sending = RwSignal::new(false);
    let pending = StoredValue::new(pending);

    let post = move |action: &'static str, content: Option<serde_json::Value>| {
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
        let data = reply_data(&id, action, content);
        spawn_local(async move {
            let res = post_op(&b, &t, &r, "elicitation.reply", data).await;
            sending.set(false);
            if let Err(e) = res {
                error.set(format!("Answer not sent: {e}"));
            }
            // On success the daemon's elicitation.resolved settles the card.
        });
    };
    let submit = move || {
        let res = pending.with_value(|p| values.with_untracked(|v| build_content(&p.fields, v)));
        match res {
            Ok(content) => {
                error.set(String::new());
                post("accept", Some(content));
            }
            Err((_, msg)) => error.set(msg),
        }
    };
    let on_key = move |ev: web_sys::KeyboardEvent| {
        if ev.key() == "Escape" {
            ev.prevent_default();
            ev.stop_propagation();
            post("cancel", None);
        }
    };

    let is_url = pending.with_value(|p| p.is_url());
    let message = pending.with_value(|p| p.message.clone());
    let request_id = pending.with_value(|p| p.request_id.clone());
    let header = format!("{server} asks for input");

    let body = if is_url {
        let url = pending.with_value(|p| p.url.clone());
        let can_open = openable_url(&url);
        let open_url = url.clone();
        view! {
            <p class="elicit-url"><code>{url}</code></p>
            <p class="hint">"Open the page yourself, finish there, then choose Done. Strike does not open it for you."</p>
            <div class="elicit-actions">
                <Show when=move || can_open>
                    <button
                        type="button"
                        class="secondary"
                        data-elicitation-open="true"
                        on:click={
                            let open_url = open_url.clone();
                            move |_| {
                                if let Some(w) = web_sys::window() {
                                    let _ = w.open_with_url_and_target_and_features(&open_url, "_blank", "noopener,noreferrer");
                                }
                            }
                        }
                    >"Open"</button>
                </Show>
                <button type="button" data-elicitation-accept="true" disabled=move || sending.get() on:click=move |_| post("accept", None)>"Done"</button>
                <button type="button" class="secondary" data-elicitation-decline="true" disabled=move || sending.get() on:click=move |_| post("decline", None)>"Decline"</button>
                <button type="button" class="danger" data-elicitation-cancel="true" disabled=move || sending.get() on:click=move |_| post("cancel", None)>"Cancel"</button>
            </div>
        }
        .into_any()
    } else {
        let fields = pending.with_value(|p| p.fields.clone());
        view! {
            <form class="elicit-form" on:submit=move |ev: web_sys::SubmitEvent| { ev.prevent_default(); submit(); }>
                {fields.into_iter().enumerate().map(|(i, field)| {
                    let id = format!("{request_id}-f{i}");
                    let text = label(&field);
                    let description = field.description.clone();
                    let has_description = !description.is_empty();
                    let required = field.required;
                    let control = match field.kind.clone() {
                        Kind::Bool => view! {
                            <input
                                type="checkbox"
                                id=id.clone()
                                data-elicitation-field=field.name.clone()
                                prop:checked=move || values.with(|v| v.get(i).map(|s| s == "true").unwrap_or(false))
                                on:change=move |ev| {
                                    let on = event_target_checked(&ev);
                                    values.update(|v| if let Some(slot) = v.get_mut(i) { *slot = on.to_string(); });
                                }
                            />
                        }.into_any(),
                        Kind::Choice { options } => view! {
                            <select
                                id=id.clone()
                                data-elicitation-field=field.name.clone()
                                required=required
                                prop:value=move || values.with(|v| v.get(i).cloned().unwrap_or_default())
                                on:change=move |ev| {
                                    let val = event_target_value(&ev);
                                    values.update(|v| if let Some(slot) = v.get_mut(i) { *slot = val; });
                                }
                            >
                                <option value="">"Choose…"</option>
                                {options.into_iter().map(|(value, text)| view! { <option value=value>{text}</option> }).collect_view()}
                            </select>
                        }.into_any(),
                        Kind::Number { integer, .. } => view! {
                            <input
                                type="number"
                                id=id.clone()
                                step=if integer { "1" } else { "any" }
                                data-elicitation-field=field.name.clone()
                                required=required
                                prop:value=move || values.with(|v| v.get(i).cloned().unwrap_or_default())
                                on:input=move |ev| {
                                    let val = event_target_value(&ev);
                                    values.update(|v| if let Some(slot) = v.get_mut(i) { *slot = val; });
                                }
                            />
                        }.into_any(),
                        Kind::Text { format, .. } => view! {
                            <input
                                type=input_type(&format)
                                id=id.clone()
                                data-elicitation-field=field.name.clone()
                                required=required
                                autocomplete="off"
                                prop:value=move || values.with(|v| v.get(i).cloned().unwrap_or_default())
                                on:input=move |ev| {
                                    let val = event_target_value(&ev);
                                    values.update(|v| if let Some(slot) = v.get_mut(i) { *slot = val; });
                                }
                            />
                        }.into_any(),
                    };
                    view! {
                        <div class="elicit-field">
                            <label for=id>{text}</label>
                            {control}
                            <Show when=move || has_description>
                                <span class="hint">{description.clone()}</span>
                            </Show>
                        </div>
                    }
                }).collect_view()}
                <div class="elicit-actions">
                    <button type="submit" data-elicitation-accept="true" disabled=move || sending.get()>"Submit"</button>
                    <button type="button" class="secondary" data-elicitation-decline="true" disabled=move || sending.get() on:click=move |_| post("decline", None)>"Decline"</button>
                    <button type="button" class="danger" data-elicitation-cancel="true" disabled=move || sending.get() on:click=move |_| post("cancel", None)>"Cancel"</button>
                </div>
            </form>
        }
        .into_any()
    };

    view! {
        <article
            class="elicit-card"
            aria-live="polite"
            aria-label=header.clone()
            data-pending-elicitation=request_id.clone()
            tabindex="-1"
            on:keydown=on_key
        >
            <header>
                <span class="badge badge-blocked">"Needs you"</span>
                <span class="who">{header}</span>
            </header>
            <p class="elicit-message">{message}</p>
            <p class="elicit-safety">"Requested by an MCP server, not by Strike. Servers must not ask for passwords or other secrets here; do not enter them. Your answer goes only to this tool call and grants no permission."</p>
            {body}
            <Show when=move || !error.get().is_empty()>
                <p class="elicit-error" role="alert">{move || error.get()}</p>
            </Show>
        </article>
    }
    .into_any()
}
