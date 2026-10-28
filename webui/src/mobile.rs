//! Phone cockpit. One pane at a time, a bottom nav, and the visual
//! viewport so the composer stays above the keyboard. Desktop layout is
//! unchanged when `data-phone` is not `1`.
//!
//! feature:mobile-first

use std::cell::Cell;
use std::rc::Rc;

use leptos::prelude::*;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

/// Width and short-landscape query. 1024×594 (the review desktop) is
/// taller than 500px, so it stays on the three-column shell.
pub const PHONE_MQ: &str = "(max-width: 720px), (max-height: 500px) and (max-width: 1024px)";

pub const PANE_SESSIONS: &str = "sessions";
pub const PANE_CHAT: &str = "chat";
pub const PANE_ACTIVITY: &str = "activity";
pub const PANE_FILES: &str = "files";
pub const PANE_DAEMON: &str = "daemon";

/// Visible viewport in CSS pixels. `top` is `visualViewport.offsetTop`.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct ViewportBox {
    pub top: f64,
    pub height: f64,
}

pub fn known_pane(id: &str) -> bool {
    matches!(
        id,
        PANE_SESSIONS | PANE_CHAT | PANE_ACTIVITY | PANE_FILES | PANE_DAEMON
    )
}

/// `vv_height` / `vv_offset_top` come from `visualViewport`. A missing or
/// non-finite visual viewport falls back to the layout height and a top of 0.
pub fn viewport_box(inner_height: f64, vv_height: f64, vv_offset_top: f64) -> ViewportBox {
    let inner = if inner_height.is_finite() && inner_height > 0.0 {
        inner_height
    } else {
        0.0
    };
    let height = if vv_height.is_finite() && vv_height > 0.0 {
        vv_height
    } else {
        inner
    };
    let mut top = if vv_offset_top.is_finite() && vv_offset_top > 0.0 {
        vv_offset_top
    } else {
        0.0
    };
    if inner > 0.0 {
        top = top.min(inner);
    }
    ViewportBox { top, height }
}

/// EventSource.readyState: 0 connecting, 1 open, 2 closed.
/// CONNECTING is the browser's own `Last-Event-ID` retry. Leave that
/// socket alone. OPEN may be a zombie after radio sleep, and CLOSED
/// needs a new stream. Both recreate from the stored cursor.
pub fn resume_reconnect(ready_state: u16) -> bool {
    ready_state != 0
}

/// Byte offset for `?after=` when a live stream is recreated.
/// A root switch, or a cursor that never advanced, starts at the beginning.
pub fn resume_after(cursor: u64, switching: bool) -> Option<u64> {
    if switching || cursor == 0 {
        None
    } else {
        Some(cursor)
    }
}

pub fn is_phone() -> bool {
    body_attr("data-phone").as_deref() == Some("1")
}

pub fn set_pane(pane: &str) {
    let pane = if known_pane(pane) { pane } else { PANE_CHAT };
    set_body_attr("data-phone-pane", pane);
}

/// Wires the viewport, the phone flag, pull-to-refresh, and resume.
/// `on_resume` refreshes daemon state. The caller decides whether the
/// live stream has to be recreated.
pub fn install(on_resume: impl Fn() + 'static) {
    let on_resume: Rc<dyn Fn()> = Rc::new(on_resume);
    install_viewport();
    install_resume(Rc::clone(&on_resume));
    install_pull(on_resume);
    sync_phone_flag();
}

fn install_viewport() {
    let Some(win) = web_sys::window() else {
        return;
    };
    let listen = |target: &web_sys::EventTarget, name: &str| {
        let cb = Closure::<dyn FnMut()>::new(|| sync_phone_flag());
        let _ = target.add_event_listener_with_callback(name, cb.as_ref().unchecked_ref());
        cb.forget();
    };

    listen(win.as_ref(), "resize");
    if let Ok(Some(mql)) = win.match_media(PHONE_MQ) {
        listen(mql.as_ref(), "change");
    }
    if let Some(vv) = win.visual_viewport() {
        let track = |name: &str, vv: &web_sys::VisualViewport| {
            let cb = Closure::<dyn FnMut()>::new(|| {
                if is_phone() {
                    let (h, top) = read_visual();
                    apply_viewport(h, top);
                }
            });
            let _ = vv.add_event_listener_with_callback(name, cb.as_ref().unchecked_ref());
            cb.forget();
        };
        track("resize", &vv);
        track("scroll", &vv);
    }

    let force = Closure::<dyn FnMut(f64, f64)>::new(|height: f64, top: f64| {
        apply_viewport(height, top);
    });
    let _ = js_sys::Reflect::set(
        &win,
        &wasm_bindgen::JsValue::from_str("__strikeSetViewport"),
        force.as_ref().unchecked_ref(),
    );
    force.forget();
}

fn install_resume(on_resume: Rc<dyn Fn()>) {
    let Some(win) = web_sys::window() else {
        return;
    };
    let run = Rc::clone(&on_resume);
    let on_vis = Closure::<dyn FnMut()>::new(move || {
        if visibility_visible() {
            note_resume(&run);
        }
    });
    if let Some(doc) = win.document() {
        let _ = doc
            .add_event_listener_with_callback("visibilitychange", on_vis.as_ref().unchecked_ref());
    }
    on_vis.forget();

    let run = Rc::clone(&on_resume);
    let on_show = Closure::<dyn FnMut(web_sys::Event)>::new(move |ev: web_sys::Event| {
        let persisted = js_sys::Reflect::get(&ev, &wasm_bindgen::JsValue::from_str("persisted"))
            .ok()
            .and_then(|v| v.as_bool())
            .unwrap_or(false);
        if persisted {
            note_resume(&run);
        }
    });
    let _ = win.add_event_listener_with_callback("pageshow", on_show.as_ref().unchecked_ref());
    on_show.forget();

    let run = on_resume;
    let on_evt = Closure::<dyn FnMut()>::new(move || note_resume(&run));
    let _ = win.add_event_listener_with_callback("strike-resume", on_evt.as_ref().unchecked_ref());
    on_evt.forget();
}

fn install_pull(on_resume: Rc<dyn Fn()>) {
    let Some(win) = web_sys::window() else {
        return;
    };
    let Some(doc) = win.document() else {
        return;
    };
    let start_y = Rc::new(Cell::new(f64::NAN));
    let armed = Rc::new(Cell::new(false));

    let y0 = Rc::clone(&start_y);
    let arm = Rc::clone(&armed);
    let on_start = Closure::<dyn FnMut(web_sys::Event)>::new(move |ev: web_sys::Event| {
        arm.set(false);
        y0.set(f64::NAN);
        if !is_phone() {
            return;
        }
        if !at_scroll_top(&ev) {
            return;
        }
        if let Some(y) = touch_client_y(&ev, "touches") {
            y0.set(y);
        }
    });
    let _ = doc.add_event_listener_with_callback("touchstart", on_start.as_ref().unchecked_ref());
    on_start.forget();

    let y0 = Rc::clone(&start_y);
    let arm = Rc::clone(&armed);
    let on_move = Closure::<dyn FnMut(web_sys::Event)>::new(move |ev: web_sys::Event| {
        let start = y0.get();
        if !start.is_finite() {
            return;
        }
        let Some(y) = touch_client_y(&ev, "touches") else {
            return;
        };
        let pulling = y - start > 64.0;
        arm.set(pulling);
        set_body_attr("data-pull", if pulling { "1" } else { "0" });
    });
    let _ = doc.add_event_listener_with_callback("touchmove", on_move.as_ref().unchecked_ref());
    on_move.forget();

    let on_end = Closure::<dyn FnMut(web_sys::Event)>::new(move |_ev: web_sys::Event| {
        let fire = armed.get();
        armed.set(false);
        start_y.set(f64::NAN);
        set_body_attr("data-pull", "0");
        if fire {
            note_resume(&on_resume);
        }
    });
    let _ = doc.add_event_listener_with_callback("touchend", on_end.as_ref().unchecked_ref());
    on_end.forget();
}

fn note_resume(cb: &Rc<dyn Fn()>) {
    if let Some(el) = document_element() {
        let prev = el
            .get_attribute("data-resume")
            .unwrap_or_else(|| "0".into());
        let n = prev.parse::<u32>().unwrap_or(0).wrapping_add(1);
        let _ = el.set_attribute("data-resume", &n.to_string());
    }
    cb();
}

fn sync_phone_flag() {
    let Some(win) = web_sys::window() else {
        return;
    };
    let matches = win
        .match_media(PHONE_MQ)
        .ok()
        .flatten()
        .is_some_and(|m| m.matches());
    set_body_attr("data-phone", if matches { "1" } else { "0" });
    if matches {
        let (h, top) = read_visual();
        apply_viewport(h, top);
    } else {
        clear_viewport();
    }
}

fn read_visual() -> (f64, f64) {
    let Some(win) = web_sys::window() else {
        return (0.0, 0.0);
    };
    if let Some(vv) = win.visual_viewport() {
        return (vv.height(), vv.offset_top());
    }
    let h = win
        .inner_height()
        .ok()
        .and_then(|v| v.as_f64())
        .unwrap_or(0.0);
    (h, 0.0)
}

fn apply_viewport(height: f64, top: f64) {
    let Some(win) = web_sys::window() else {
        return;
    };
    let inner = win
        .inner_height()
        .ok()
        .and_then(|v| v.as_f64())
        .unwrap_or(height);
    let box_ = viewport_box(inner, height, top);
    let Some(html) = document_element() else {
        return;
    };
    let Ok(html) = html.dyn_into::<web_sys::HtmlElement>() else {
        return;
    };
    let style = html.style();
    let _ = style.set_property("--vv-height", &format!("{}px", box_.height));
    let _ = style.set_property("--vv-top", &format!("{}px", box_.top));
}

fn clear_viewport() {
    let Some(html) = document_element() else {
        return;
    };
    let Ok(html) = html.dyn_into::<web_sys::HtmlElement>() else {
        return;
    };
    let style = html.style();
    let _ = style.remove_property("--vv-height");
    let _ = style.remove_property("--vv-top");
}

fn visibility_visible() -> bool {
    let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
        return false;
    };
    js_sys::Reflect::get(&doc, &wasm_bindgen::JsValue::from_str("visibilityState"))
        .ok()
        .and_then(|v| v.as_string())
        .is_none_or(|s| s == "visible")
}

fn touch_client_y(ev: &web_sys::Event, list: &str) -> Option<f64> {
    let touches = js_sys::Reflect::get(ev, &wasm_bindgen::JsValue::from_str(list)).ok()?;
    let item = js_sys::Reflect::get(&touches, &wasm_bindgen::JsValue::from_str("0")).ok()?;
    if item.is_null() || item.is_undefined() {
        return None;
    }
    js_sys::Reflect::get(&item, &wasm_bindgen::JsValue::from_str("clientY"))
        .ok()?
        .as_f64()
}

fn at_scroll_top(ev: &web_sys::Event) -> bool {
    let Some(mut node) = ev.target() else {
        return true;
    };
    for _ in 0..8 {
        let top = js_sys::Reflect::get(&node, &wasm_bindgen::JsValue::from_str("scrollTop"))
            .ok()
            .and_then(|v| v.as_f64())
            .unwrap_or(0.0);
        if top > 1.0 {
            return false;
        }
        let Ok(parent) =
            js_sys::Reflect::get(&node, &wasm_bindgen::JsValue::from_str("parentElement"))
        else {
            break;
        };
        if parent.is_null() || parent.is_undefined() {
            break;
        }
        let Ok(parent) = parent.dyn_into::<web_sys::EventTarget>() else {
            break;
        };
        node = parent;
    }
    true
}

fn document_element() -> Option<web_sys::Element> {
    web_sys::window()?.document()?.document_element()
}

fn body_attr(name: &str) -> Option<String> {
    web_sys::window()?.document()?.body()?.get_attribute(name)
}

fn set_body_attr(name: &str, value: &str) {
    if let Some(body) = web_sys::window()
        .and_then(|w| w.document())
        .and_then(|d| d.body())
    {
        let _ = body.set_attribute(name, value);
    }
}

#[component]
pub fn PhoneNav(
    pane: RwSignal<String>,
    side_tab: RwSignal<String>,
    mode: RwSignal<String>,
) -> impl IntoView {
    let go = move |id: &'static str| {
        pane.set(id.to_string());
        match id {
            PANE_DAEMON => mode.set("daemon".into()),
            PANE_ACTIVITY => {
                mode.set("chat".into());
                side_tab.set("activity".into());
            }
            PANE_FILES => {
                mode.set("chat".into());
                side_tab.set("files".into());
            }
            _ => mode.set("chat".into()),
        }
    };
    let class_for = move |id: &'static str| {
        let current = if mode.get() == PANE_DAEMON {
            PANE_DAEMON.to_string()
        } else {
            pane.get()
        };
        if current == id {
            "on"
        } else {
            ""
        }
    };
    let sessions = move || go(PANE_SESSIONS);
    let chat = move || go(PANE_CHAT);
    let activity = move || go(PANE_ACTIVITY);
    let files = move || go(PANE_FILES);
    let daemon = move || go(PANE_DAEMON);
    view! {
        <nav class="phone-nav" aria-label="Sections">
            <button type="button" class=move || class_for(PANE_SESSIONS) on:click=move |_| sessions()>"Sessions"</button>
            <button type="button" class=move || class_for(PANE_CHAT) on:click=move |_| chat()>"Chat"</button>
            <button type="button" class=move || class_for(PANE_ACTIVITY) on:click=move |_| activity()>"Activity"</button>
            <button type="button" class=move || class_for(PANE_FILES) on:click=move |_| files()>"Files"</button>
            <button type="button" class=move || class_for(PANE_DAEMON) on:click=move |_| daemon()>"Daemon"</button>
        </nav>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn viewport_box_tracks_the_visual_viewport() {
        let b = viewport_box(844.0, 420.0, 0.0);
        assert_eq!(
            b,
            ViewportBox {
                top: 0.0,
                height: 420.0
            }
        );
        let b = viewport_box(844.0, 380.0, 300.0);
        assert_eq!(b.top, 300.0);
        assert_eq!(b.height, 380.0);
    }

    #[test]
    fn viewport_box_ignores_a_missing_visual_viewport() {
        let b = viewport_box(844.0, -1.0, f64::NAN);
        assert_eq!(
            b,
            ViewportBox {
                top: 0.0,
                height: 844.0
            }
        );
        let b = viewport_box(100.0, 80.0, 500.0);
        assert_eq!(b.top, 100.0);
        assert_eq!(b.height, 80.0);
    }

    #[test]
    fn resume_leaves_a_connecting_stream_alone() {
        assert!(!resume_reconnect(0));
        assert!(resume_reconnect(1));
        assert!(resume_reconnect(2));
    }

    fn resume_after_keeps_a_cursor_on_the_same_root() {
        assert_eq!(resume_after(120, false), Some(120));
        assert_eq!(resume_after(0, false), None);
        assert_eq!(resume_after(120, true), None);
    }

    #[test]
    fn panes_are_the_screens_the_daemon_can_draw() {
        assert!(known_pane("sessions"));
        assert!(known_pane("chat"));
        assert!(known_pane("activity"));
        assert!(known_pane("files"));
        assert!(known_pane("daemon"));
        assert!(!known_pane("memory"));
        assert!(!known_pane(""));
    }
}
