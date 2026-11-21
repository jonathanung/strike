//! Focus trap and focus return for every modal dialog (#1878).
//!
//! One document-level helper instead of per-dialog code: any element with
//! `aria-modal="true"` — the command palette, keyboard help, confirm
//! dialogs, approvals, share, rewind, workspaces, the model menu and the
//! rest — gets the same behaviour without changing its component:
//!
//! - **Trap**: Tab and Shift+Tab cycle inside the newest open modal. A
//!   dialog that already handles Tab itself (confirm) calls
//!   `preventDefault` / `stopPropagation`, and this helper stays out of
//!   the way.
//! - **Initial focus**: if the dialog did not focus something inside
//!   itself, its first focusable gets focus.
//! - **Return**: when the modal leaves the page and focus was lost with it,
//!   focus goes back to the element that had it before the modal opened.
//!   Each dialog keeps its own Escape/close handling.
//!
//! The decisions are pure functions in `a11y_announce::focus`.

use std::cell::RefCell;
use std::rc::Rc;

use a11y_announce::focus::{pick_opener, remember, trap_tab, TabMove};
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;
use web_sys::{Document, Element, HtmlElement, Node};

const MODAL: &str = "[aria-modal=\"true\"]";
const FOCUSABLE: &str = "a[href], button:not([disabled]), input:not([disabled]):not([type=\"hidden\"]), \
     select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex=\"-1\"]), \
     [contenteditable=\"true\"]";
const HISTORY: usize = 8;

#[derive(Default)]
struct State {
    /// Recent focus targets, oldest first.
    history: Vec<Element>,
    /// Open modals in opening order, each with the element to return to.
    stack: Vec<(Element, Option<Element>)>,
}

/// Install the document listeners once, at app start.
pub fn install() {
    let Some(doc) = document() else {
        return;
    };
    let state = Rc::new(RefCell::new(State::default()));

    let on_focus = {
        let state = Rc::clone(&state);
        Closure::<dyn FnMut(web_sys::Event)>::new(move |ev: web_sys::Event| {
            if let Some(el) = ev.target().and_then(|t| t.dyn_into::<Element>().ok()) {
                remember(&mut state.borrow_mut().history, el, HISTORY);
            }
        })
    };
    let _ = doc.add_event_listener_with_callback("focusin", on_focus.as_ref().unchecked_ref());
    on_focus.forget();

    let on_key = {
        let state = Rc::clone(&state);
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            if ev.key() != "Tab" || ev.default_prevented() || ev.alt_key() || ev.ctrl_key() || ev.meta_key() {
                return;
            }
            let Some(doc) = document() else {
                return;
            };
            let Some(modal) = top_modal(&doc, &state.borrow()) else {
                return;
            };
            let items = focusables(&modal);
            let active = doc.active_element();
            let current = active.and_then(|a| items.iter().position(|el| el == &a));
            match trap_tab(items.len(), current, ev.shift_key()) {
                TabMove::Browser => {}
                TabMove::Focus(i) => {
                    ev.prevent_default();
                    focus(&items[i]);
                }
                TabMove::Hold => ev.prevent_default(),
            }
        })
    };
    // Window, bubble phase, registered after the first render: the last
    // listener a keydown reaches, after Leptos's delegated handlers and the
    // shell's window shortcuts, so a dialog that handles Tab itself
    // (preventDefault / stopPropagation) is never moved twice.
    crate::clipboard::after_ms(0, move || {
        if let Some(w) = web_sys::window() {
            let _ = w.add_event_listener_with_callback("keydown", on_key.as_ref().unchecked_ref());
        }
        on_key.forget();
    });

    let on_mutate = {
        let state = Rc::clone(&state);
        Closure::<dyn FnMut()>::new(move || sync(&state))
    };
    if let (Ok(observer), Some(body)) = (
        web_sys::MutationObserver::new(on_mutate.as_ref().unchecked_ref()),
        doc.body(),
    ) {
        let opts = web_sys::MutationObserverInit::new();
        opts.set_child_list(true);
        opts.set_subtree(true);
        let _ = observer.observe_with_options(&body, &opts);
        // Lives for the page, like the closures.
        std::mem::forget(observer);
    }
    on_mutate.forget();
}

/// Reconcile the modal stack with the page after DOM changes.
fn sync(state: &Rc<RefCell<State>>) {
    let Some(doc) = document() else {
        return;
    };
    let open = query_all(&doc, MODAL);
    let mut opened = Vec::new();
    let mut openers = Vec::new();
    {
        let mut s = state.borrow_mut();
        let mut closed = Vec::new();
        s.stack.retain(|(m, opener)| {
            let keep = open.contains(m);
            if !keep {
                closed.push(opener.clone());
            }
            keep
        });
        // Outermost first: when nested modals close together, the outer
        // opener is the one still in the page.
        openers.extend(closed.into_iter().flatten());
        for m in open {
            if s.stack.iter().any(|(e, _)| e == &m) {
                continue;
            }
            let opener = pick_opener(&s.history, |e| e.is_connected() && !inside(&m, e)).cloned();
            s.stack.push((m.clone(), opener));
            opened.push(m);
        }
    }
    // A modal replaced by another (palette -> confirm) hands focus to the
    // new one; its opener is returned when that one closes.
    if !openers.is_empty() && opened.is_empty() && focus_lost(&doc) {
        if let Some(el) = openers.into_iter().find(|e| e.is_connected()) {
            focus(&el);
        }
    }
    for m in opened {
        // Let the dialog's own autofocus run first.
        crate::clipboard::after_ms(30, move || {
            if !m.is_connected() {
                return;
            }
            let Some(doc) = document() else {
                return;
            };
            if doc.active_element().is_some_and(|a| inside(&m, &a)) {
                return;
            }
            if let Some(first) = focusables(&m).first() {
                focus(first);
            }
        });
    }
}

/// The newest open modal still in the page and visible.
fn top_modal(doc: &Document, state: &State) -> Option<Element> {
    state
        .stack
        .iter()
        .rev()
        .map(|(m, _)| m)
        .find(|m| m.is_connected() && visible(m))
        .cloned()
        .or_else(|| query_all(doc, MODAL).into_iter().rev().find(visible))
}

fn focusables(modal: &Element) -> Vec<Element> {
    let Ok(list) = modal.query_selector_all(FOCUSABLE) else {
        return Vec::new();
    };
    (0..list.length())
        .filter_map(|i| list.item(i))
        .filter_map(|n| n.dyn_into::<Element>().ok())
        .filter(visible)
        .collect()
}

fn query_all(doc: &Document, selector: &str) -> Vec<Element> {
    let Ok(list) = doc.query_selector_all(selector) else {
        return Vec::new();
    };
    (0..list.length())
        .filter_map(|i| list.item(i))
        .filter_map(|n| n.dyn_into::<Element>().ok())
        .collect()
}

fn visible(el: &Element) -> bool {
    let r = el.get_bounding_client_rect();
    r.width() > 0.0 && r.height() > 0.0
}

fn inside(container: &Element, el: &Element) -> bool {
    let node: &Node = el;
    container.contains(Some(node))
}

fn focus_lost(doc: &Document) -> bool {
    match doc.active_element() {
        None => true,
        Some(a) => !a.is_connected() || a.tag_name().eq_ignore_ascii_case("body"),
    }
}

fn focus(el: &Element) {
    if let Some(el) = el.dyn_ref::<HtmlElement>() {
        let _ = el.focus();
    }
}

fn document() -> Option<Document> {
    web_sys::window().and_then(|w| w.document())
}
