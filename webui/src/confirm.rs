//! One confirm dialog for destructive cockpit actions.
//!
//! Rotate token, revoke paired browsers, delete a trigger, close an
//! issue, and clear the queue all call [`ask`]. The pure [`step`]
//! function is what the tests pin; the view only renders the open phase.

use std::cell::RefCell;
use std::rc::Rc;

use leptos::prelude::*;
use wasm_bindgen::JsCast;

use crate::shell::ConfirmSpec;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ConfirmRequest {
    pub title: String,
    pub body: String,
    pub confirm_label: String,
}

impl From<ConfirmSpec> for ConfirmRequest {
    fn from(spec: ConfirmSpec) -> Self {
        Self {
            title: spec.title.to_string(),
            body: spec.body.to_string(),
            confirm_label: spec.confirm_label.to_string(),
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ConfirmPhase {
    Idle,
    Open(ConfirmRequest),
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ConfirmAction {
    Open(ConfirmRequest),
    Cancel,
    Accept,
}

/// Advance the dialog. `true` means the operator confirmed an open prompt.
pub fn step(phase: ConfirmPhase, action: ConfirmAction) -> (ConfirmPhase, bool) {
    match action {
        ConfirmAction::Open(request) => (ConfirmPhase::Open(request), false),
        ConfirmAction::Cancel => (ConfirmPhase::Idle, false),
        ConfirmAction::Accept => match phase {
            ConfirmPhase::Open(_) => (ConfirmPhase::Idle, true),
            ConfirmPhase::Idle => (ConfirmPhase::Idle, false),
        },
    }
}

struct Gate {
    request: RwSignal<Option<ConfirmRequest>>,
    on_yes: Rc<dyn Fn()>,
}

thread_local! {
    static GATE: RefCell<Option<Gate>> = const { RefCell::new(None) };
}

fn placeholder() -> Rc<dyn Fn()> {
    Rc::new(|| {})
}

/// Mount once from the shell. Later [`ask`] calls open this dialog.
pub fn install() -> RwSignal<Option<ConfirmRequest>> {
    let request = RwSignal::new(None::<ConfirmRequest>);
    GATE.with(|gate| {
        *gate.borrow_mut() = Some(Gate {
            request,
            on_yes: placeholder(),
        });
    });
    request
}

pub fn ask(request: ConfirmRequest, on_yes: Rc<dyn Fn()>) {
    let (phase, accepted) = step(ConfirmPhase::Idle, ConfirmAction::Open(request.clone()));
    if accepted {
        return;
    }
    let ConfirmPhase::Open(request) = phase else {
        return;
    };
    GATE.with(|gate| {
        let mut gate = gate.borrow_mut();
        let Some(gate) = gate.as_mut() else {
            return;
        };
        gate.on_yes = on_yes;
        gate.request.set(Some(request));
    });
}

fn dismiss(accept: bool) {
    let run = GATE.with(|gate| {
        let mut gate = gate.borrow_mut();
        let Some(gate) = gate.as_mut() else {
            return None;
        };
        let phase = match gate.request.get_untracked() {
            Some(request) => ConfirmPhase::Open(request),
            None => ConfirmPhase::Idle,
        };
        let action = if accept {
            ConfirmAction::Accept
        } else {
            ConfirmAction::Cancel
        };
        let (next, accepted) = step(phase, action);
        gate.request.set(match next {
            ConfirmPhase::Open(request) => Some(request),
            ConfirmPhase::Idle => None,
        });
        if !accepted {
            return None;
        }
        let run = gate.on_yes.clone();
        gate.on_yes = placeholder();
        Some(run)
    });
    if let Some(run) = run {
        run();
    }
}

#[component]
pub fn ConfirmDialog() -> impl IntoView {
    let request = install();
    // Focus Cancel whenever a prompt opens. The Escape and Tab handling
    // below is keyed on the dialog subtree, so without this the keyboard
    // contract of the wired surfaces (Escape cancels, the safe choice is
    // the default) never engages: focus would stay on the button behind
    // the backdrop that opened the prompt.
    Effect::new(move |_| {
        if request.get().is_some() {
            let cancel = web_sys::window()
                .and_then(|w| w.document())
                .and_then(|doc| doc.query_selector("[data-confirm='cancel']").ok().flatten());
            if let Some(el) = cancel.and_then(|el| el.dyn_into::<web_sys::HtmlElement>().ok()) {
                let _ = el.focus();
            }
        }
    });
    view! {
        <Show when=move || request.get().is_some()>
            <div
                class="confirm-backdrop"
                data-confirm="backdrop"
                on:click=move |_| dismiss(false)
            >
                <div
                    class="confirm-dialog"
                    role="alertdialog"
                    aria-modal="true"
                    aria-labelledby="confirm-title"
                    aria-describedby="confirm-body"
                    data-confirm="dialog"
                    on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()
                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                        let key = ev.key();
                        if key == "Escape" {
                            ev.prevent_default();
                            dismiss(false);
                            return;
                        }
                        if key == "Tab" {
                            ev.prevent_default();
                            let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
                                return;
                            };
                            let Some(dialog) = doc.query_selector("[data-confirm='dialog']").ok().flatten() else {
                                return;
                            };
                            let Some(list) = dialog.query_selector_all("button").ok() else {
                                return;
                            };
                            let count = list.length();
                            if count == 0 {
                                return;
                            }
                            let active = doc.active_element();
                            let mut index = 0i32;
                            for i in 0..count {
                                if let Some(node) = list.item(i) {
                                    if active.as_ref().map(|el| el.is_same_node(Some(&node))).unwrap_or(false) {
                                        index = i as i32;
                                        break;
                                    }
                                }
                            }
                            let next = if ev.shift_key() {
                                if index == 0 { count - 1 } else { (index as u32) - 1 }
                            } else {
                                ((index as u32) + 1) % count
                            };
                            if let Some(node) = list.item(next) {
                                if let Some(el) = node.dyn_ref::<web_sys::HtmlElement>() {
                                    let _ = el.focus();
                                }
                            }
                        }
                    }
                >
                    <h2 id="confirm-title">{move || request.get().map(|r| r.title).unwrap_or_default()}</h2>
                    <p id="confirm-body">{move || request.get().map(|r| r.body).unwrap_or_default()}</p>
                    <div class="confirm-actions">
                        <button type="button" class="secondary" data-confirm="cancel" on:click=move |_| dismiss(false)>
                            "Cancel"
                        </button>
                        <button type="button" class="primary" data-confirm="accept" on:click=move |_| dismiss(true)>
                            {move || request.get().map(|r| r.confirm_label).unwrap_or_else(|| "Confirm".into())}
                        </button>
                    </div>
                </div>
            </div>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn req(title: &str) -> ConfirmRequest {
        ConfirmRequest {
            title: title.into(),
            body: "This cannot be undone.".into(),
            confirm_label: "Do it".into(),
        }
    }

    #[test]
    fn confirm_flow_accepts_only_an_open_prompt() {
        let (opened, accepted) = step(ConfirmPhase::Idle, ConfirmAction::Open(req("Rotate")));
        assert!(!accepted);
        assert!(matches!(opened, ConfirmPhase::Open(_)));
        let (cancelled, accepted) = step(opened.clone(), ConfirmAction::Cancel);
        assert!(!accepted);
        assert_eq!(cancelled, ConfirmPhase::Idle);
        let (done, accepted) = step(opened, ConfirmAction::Accept);
        assert!(accepted);
        assert_eq!(done, ConfirmPhase::Idle);
        let (still, accepted) = step(ConfirmPhase::Idle, ConfirmAction::Accept);
        assert!(!accepted);
        assert_eq!(still, ConfirmPhase::Idle);
    }

    #[test]
    fn a_second_prompt_replaces_the_open_one() {
        let (first, _) = step(ConfirmPhase::Idle, ConfirmAction::Open(req("Rotate")));
        let (second, accepted) = step(first, ConfirmAction::Open(req("Revoke")));
        assert!(!accepted);
        match second {
            ConfirmPhase::Open(request) => assert_eq!(request.title, "Revoke"),
            ConfirmPhase::Idle => panic!("prompt closed"),
        }
    }
}
