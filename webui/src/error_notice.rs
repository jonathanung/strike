//! The cockpit's one designed error state (#1535).
//!
//! Panels store failures as strings — often a label plus whatever the fetch
//! layer returned (`TypeError: Failed to fetch`, a serde message, an HTTP
//! status and body). [`ErrorNotice`] renders such a message as a square,
//! bordered block: a plain headline, what the user can do, Retry, and a
//! deliberate "Copy details" action for the internal string. The internal
//! string is logged to the console and never rendered. Mapping lives in the
//! `error-copy` crate so every surface uses the same words.
//!
//! Inline notes that mix status and error text (forms, toasts) call
//! [`humanize`] instead of rendering the block.

use leptos::callback::{Callable, UnsyncCallback};
use leptos::prelude::*;
use leptos::task::spawn_local;

pub use error_copy::humanize;

#[component]
pub fn ErrorNotice(
    /// The stored error message; the block renders nothing while empty.
    #[prop(into)]
    message: Signal<String>,
    /// Re-run the failed load. No Retry button when absent.
    #[prop(optional)]
    on_retry: Option<UnsyncCallback<()>>,
) -> impl IntoView {
    let info = Memo::new(move |_| error_copy::describe(&message.get()));
    let copy_note = RwSignal::new(String::new());
    // Internal strings go to the console for debugging, never to the DOM.
    Effect::new(move |_| {
        let raw = message.get();
        copy_note.set(String::new());
        if info.get_untracked().internal {
            web_sys::console::warn_1(&wasm_bindgen::JsValue::from_str(&format!("cockpit error: {raw}")));
        }
    });
    let copy_details = move |_: web_sys::MouseEvent| {
        let raw = message.get_untracked();
        spawn_local(async move {
            let note = match crate::clipboard::copy_text(&raw).await {
                Ok(()) => "Details copied.".to_string(),
                Err(msg) => msg,
            };
            copy_note.set(note);
        });
    };
    view! {
        <Show when=move || !message.get().trim().is_empty()>
            <div class="error-block" role="alert" data-error-class=move || info.get().class.slug()>
                <p class="error-block-title">{move || info.get().message}</p>
                <Show when=move || !info.get().action.is_empty()>
                    <p class="error-block-action">{move || info.get().action}</p>
                </Show>
                <div class="error-block-actions">
                    {on_retry.map(|retry| view! {
                        <button type="button" class="secondary" on:click=move |_| retry.run(())>"Retry"</button>
                    })}
                    <Show when=move || info.get().internal>
                        <button type="button" class="ghost" data-error-copy="true" on:click=copy_details>"Copy details"</button>
                    </Show>
                    <span class="hint" role="status">{move || copy_note.get()}</span>
                </div>
            </div>
        </Show>
    }
}
