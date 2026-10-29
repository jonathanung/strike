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
/// A second [`ConfirmAction::Open`] leaves the first request in place.
pub fn step(phase: ConfirmPhase, action: ConfirmAction) -> (ConfirmPhase, bool) {
    match action {
        ConfirmAction::Open(request) => match phase {
            ConfirmPhase::Open(_) => (phase, false),
            ConfirmPhase::Idle => (ConfirmPhase::Open(request), false),
        },
        ConfirmAction::Cancel => (ConfirmPhase::Idle, false),
        ConfirmAction::Accept => match phase {
            ConfirmPhase::Open(_) => (ConfirmPhase::Idle, true),
            ConfirmPhase::Idle => (ConfirmPhase::Idle, false),
        },
    }
}

/// Callers show this when [`ask`] does not open (busy or not mounted).
pub const BUSY: &str = "Another confirmation is open.";

/// What the dialog does with a key. `stop` keeps the shell's window
/// listener from also dismissing the view underneath.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ConfirmKey {
    pub stop: bool,
    pub prevent_default: bool,
    pub dismiss: bool,
    pub trap_tab: bool,
}

pub fn confirm_key(key: &str) -> ConfirmKey {
    match key {
        "Escape" => ConfirmKey {
            stop: true,
            prevent_default: true,
            dismiss: true,
            trap_tab: false,
        },
        "Tab" => ConfirmKey {
            stop: true,
            prevent_default: true,
            dismiss: false,
            trap_tab: true,
        },
        _ => ConfirmKey {
            stop: false,
            prevent_default: false,
            dismiss: false,
            trap_tab: false,
        },
    }
}

/// Reject-and-keep. An open prompt stays authoritative, and an uninstalled
/// dialog does not drop the request without a `false` result.
pub fn admit_prompt<T>(
    installed: bool,
    current: Option<(ConfirmRequest, T)>,
    incoming: (ConfirmRequest, T),
) -> (Option<(ConfirmRequest, T)>, bool) {
    if !installed || current.is_some() {
        return (current, false);
    }
    (Some(incoming), true)
}

/// Accept runs the callback stored with the open prompt. Cancel runs none.
pub fn settle_prompt<T>(
    current: Option<(ConfirmRequest, T)>,
    accept: bool,
) -> (Option<(ConfirmRequest, T)>, Option<T>) {
    let Some((_, callback)) = current else {
        return (None, None);
    };
    if accept {
        (None, Some(callback))
    } else {
        (None, None)
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

#[must_use = "false means the prompt was not opened"]
pub fn ask(request: ConfirmRequest, on_yes: Rc<dyn Fn()>) -> bool {
    GATE.with(|gate| {
        let mut gate = gate.borrow_mut();
        let installed = gate.is_some();
        let current = gate
            .as_ref()
            .and_then(|g| g.request.get_untracked().map(|open| (open, ())));
        let already = current.is_some();
        let (_kept, accepted) = admit_prompt(installed, current, (request.clone(), ()));
        if !accepted || already {
            return false;
        }
        let (phase, _) = step(ConfirmPhase::Idle, ConfirmAction::Open(request));
        let ConfirmPhase::Open(request) = phase else {
            return false;
        };
        let Some(gate) = gate.as_mut() else {
            return false;
        };
        gate.on_yes = on_yes;
        gate.request.set(Some(request));
        true
    })
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
        let (next, accepted) = step(phase.clone(), action);
        let current = match phase {
            ConfirmPhase::Open(request) => Some((request, gate.on_yes.clone())),
            ConfirmPhase::Idle => None,
        };
        let (_settled, callback) = settle_prompt(current, accept && accepted);
        gate.request.set(match next {
            ConfirmPhase::Open(request) => Some(request),
            ConfirmPhase::Idle => None,
        });
        if !accepted {
            return None;
        }
        gate.on_yes = placeholder();
        callback
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
                        let plan = confirm_key(&ev.key());
                        if plan.stop {
                            ev.stop_propagation();
                        }
                        if plan.prevent_default {
                            ev.prevent_default();
                        }
                        if plan.dismiss {
                            dismiss(false);
                            return;
                        }
                        if plan.trap_tab {
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
    fn a_second_prompt_keeps_the_open_one() {
        let (first, _) = step(ConfirmPhase::Idle, ConfirmAction::Open(req("Rotate")));
        let (second, accepted) = step(first.clone(), ConfirmAction::Open(req("Revoke")));
        assert!(!accepted);
        assert_eq!(second, first);
    }

    #[test]
    fn escape_stops_so_the_view_underneath_stays() {
        let escape = confirm_key("Escape");
        assert!(escape.stop);
        assert!(escape.prevent_default);
        assert!(escape.dismiss);
        let tab = confirm_key("Tab");
        assert!(tab.stop);
        assert!(tab.trap_tab);
        assert!(!tab.dismiss);
        assert!(!confirm_key("Enter").stop);
    }

    #[test]
    fn a_second_ask_keeps_the_first_callback() {
        let first = (req("Rotate"), 1u8);
        let (open, ok) = admit_prompt(true, None, first.clone());
        assert!(ok);
        let (kept, ok) = admit_prompt(true, open.clone(), (req("Revoke"), 2));
        assert!(!ok);
        assert_eq!(kept.as_ref().map(|(_, id)| *id), Some(1));
        let (after, ran) = settle_prompt(kept, true);
        assert!(after.is_none());
        assert_eq!(ran, Some(1));
        let (_idle, ran) = settle_prompt(open, false);
        assert_eq!(ran, None);
    }

    #[test]
    fn ask_when_uninstalled_does_not_open() {
        let (slot, ok) = admit_prompt(false, None, (req("Rotate"), 1u8));
        assert!(!ok);
        assert!(slot.is_none());
        let open = Some((req("Rotate"), 1u8));
        let (kept, ok) = admit_prompt(false, open.clone(), (req("Revoke"), 2));
        assert!(!ok);
        assert_eq!(kept, open);
    }
}
