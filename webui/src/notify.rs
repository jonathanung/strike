//! Notification centre (#1264). The navbar stream hosts this bell; the
//! component is self-contained so that move is one element.
//!
//! Deep link (approvals stream reads the same hash):
//! `/#session=<id>&approval=<requestId>`.
//! Status unread stays on the session list; this badge counts centre rows.

use std::cell::Cell;
use std::rc::Rc;

use gloo_net::http::Request;
use leptos::callback::{Callable, UnsyncCallback};
use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::{Deserialize, Serialize};
use wasm_bindgen::closure::Closure;
use wasm_bindgen::{JsCast, JsValue};

use crate::api::{api, auth_header, get_json};

const SEEN_KEY: &str = "strike.notify.seen";
const HIGH_KEY: &str = "strike.notify.bannerSeq";

thread_local! {
    static OPEN_CENTRE: Cell<Option<RwSignal<bool>>> = Cell::new(None);
}

/// Opens the centre from the command palette without the palette owning state.
pub fn request_open() {
    OPEN_CENTRE.with(|slot| {
        if let Some(open) = slot.get() {
            open.set(true);
        }
    });
}

#[derive(Clone, Debug, PartialEq)]
pub struct FocusTarget {
    pub session_id: String,
    pub approval_id: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq)]
struct Note {
    id: String,
    #[serde(default)]
    seq: i64,
    kind: String,
    #[serde(default, rename = "sessionId")]
    session_id: String,
    #[serde(default, rename = "subjectId")]
    subject_id: String,
    title: String,
    #[serde(default)]
    body: String,
    #[serde(default)]
    href: String,
    #[serde(default, rename = "requestId")]
    request_id: String,
    #[serde(default)]
    banner: bool,
    #[serde(default)]
    at: String,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
struct Quiet {
    enabled: bool,
    start: String,
    end: String,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
struct Prefs {
    enabled: bool,
    quiet: Quiet,
    #[serde(rename = "mutedSessions")]
    muted: Vec<String>,
}

#[derive(Deserialize)]
struct NotesResponse {
    notifications: Vec<Note>,
    prefs: Prefs,
    latest: i64,
}

fn default_prefs() -> Prefs {
    Prefs {
        enabled: true,
        quiet: Quiet {
            enabled: false,
            start: "22:00".into(),
            end: "07:00".into(),
        },
        muted: Vec::new(),
    }
}

fn storage() -> Option<web_sys::Storage> {
    web_sys::window()?.local_storage().ok().flatten()
}

fn load_raw(key: &str) -> String {
    storage()
        .and_then(|s| s.get_item(key).ok().flatten())
        .unwrap_or_default()
}

fn save_raw(key: &str, value: &str) {
    if let Some(s) = storage() {
        let _ = s.set_item(key, value);
    }
}

fn load_seen() -> Vec<String> {
    serde_json::from_str(&load_raw(SEEN_KEY)).unwrap_or_default()
}

fn save_seen(ids: &[String]) {
    let mut ids = ids.to_vec();
    if ids.len() > 500 {
        ids = ids.split_off(ids.len() - 500);
    }
    save_raw(
        SEEN_KEY,
        &serde_json::to_string(&ids).unwrap_or_else(|_| "[]".into()),
    );
}

/// `/#session=<id>&approval=<requestId>`, also a full URL with that hash.
pub fn parse_focus(raw: &str) -> Option<FocusTarget> {
    let hash = match raw.rsplit_once('#') {
        Some((_, hash)) => hash,
        None => raw,
    };
    let hash = hash.trim().trim_start_matches('/');
    if hash.is_empty() {
        return None;
    }
    let mut session = String::new();
    let mut approval = String::new();
    for part in hash.split('&') {
        let Some((key, value)) = part.split_once('=') else {
            continue;
        };
        let value = clean_id(&percent_decode(value));
        match key {
            "session" => session = value,
            "approval" => approval = value,
            _ => {}
        }
    }
    if session.is_empty() && approval.is_empty() {
        return None;
    }
    Some(FocusTarget {
        session_id: session,
        approval_id: approval,
    })
}

fn clean_id(s: &str) -> String {
    let s = s.trim();
    if s.is_empty()
        || s.len() > 128
        || s.contains("..")
        || s.contains('/')
        || s.contains('\\')
        || s.contains(' ')
    {
        return String::new();
    }
    if !s
        .chars()
        .all(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | ':' | '-'))
    {
        return String::new();
    }
    s.to_string()
}

fn percent_decode(s: &str) -> String {
    let bytes = s.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            if let Ok(v) = u8::from_str_radix(&s[i + 1..i + 3], 16) {
                out.push(v);
                i += 3;
                continue;
            }
        }
        out.push(if bytes[i] == b'+' { b' ' } else { bytes[i] });
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn quiet_active(prefs: &Prefs, mins: i32) -> bool {
    if !prefs.quiet.enabled {
        return false;
    }
    let Some(start) = parse_hhmm(&prefs.quiet.start) else {
        return false;
    };
    let Some(end) = parse_hhmm(&prefs.quiet.end) else {
        return false;
    };
    if start == end {
        return true;
    }
    if start < end {
        mins >= start && mins < end
    } else {
        mins >= start || mins < end
    }
}

fn parse_hhmm(s: &str) -> Option<i32> {
    let (h, m) = s.split_once(':')?;
    if h.len() != 2 || m.len() != 2 {
        return None;
    }
    let h: i32 = h.parse().ok()?;
    let m: i32 = m.parse().ok()?;
    if !(0..24).contains(&h) || !(0..60).contains(&m) {
        return None;
    }
    Some(h * 60 + m)
}

fn local_minutes() -> i32 {
    let now = js_sys::Date::new_0();
    (now.get_hours() as i32) * 60 + now.get_minutes() as i32
}

fn merge_notes(existing: &[Note], incoming: &[Note]) -> Vec<Note> {
    let mut locals: Vec<Note> = existing
        .iter()
        .filter(|n| n.id.starts_with("local-"))
        .cloned()
        .collect();
    let mut remote: Vec<Note> = incoming
        .iter()
        .filter(|n| !n.id.starts_with("local-"))
        .cloned()
        .collect();
    remote.sort_by(|a, b| b.seq.cmp(&a.seq).then(a.id.cmp(&b.id)));
    locals.append(&mut remote);
    locals
}

fn prefer_banner<'a>(cur: &'a Note, next: &'a Note) -> &'a Note {
    let rank = |n: &Note| i32::from(n.kind == "approval.needed");
    if rank(next) > rank(cur) || (rank(next) == rank(cur) && next.seq >= cur.seq) {
        next
    } else {
        cur
    }
}

fn one_line(s: &str, max: usize) -> String {
    let flat = s.split_whitespace().collect::<Vec<_>>().join(" ");
    if flat.starts_with('{') || flat.starts_with('[') {
        return "Open Strike to read it.".into();
    }
    let chars: Vec<char> = flat.chars().collect();
    if chars.len() <= max {
        return flat;
    }
    let mut out: String = chars.into_iter().take(max.saturating_sub(1)).collect();
    out.push('…');
    out
}

fn short_when(at: &str) -> String {
    let b = at.as_bytes();
    if b.len() < 16 {
        return String::new();
    }
    for (i, c) in at.bytes().take(16).enumerate() {
        let ok = match i {
            4 | 7 => c == b'-',
            10 => c == b'T',
            13 => c == b':',
            _ => c.is_ascii_digit(),
        };
        if !ok {
            return String::new();
        }
    }
    format!("{} {}", &at[5..10], &at[11..16])
}

fn kind_label(kind: &str) -> &'static str {
    match kind {
        "approval.needed" => "Approval",
        "child.finished" => "Child",
        "child.blocked" => "Blocked",
        "session.finished" => "Session",
        "gateway.disconnected" => "Gateway",
        _ => "Notice",
    }
}

fn bell_svg() -> &'static str {
    r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 2.5a3.5 3.5 0 0 0-3.5 3.5v2.2L3.2 10.5h9.6L11.5 8.2V6A3.5 3.5 0 0 0 8 2.5z"/><path d="M6.5 12.2a1.5 1.5 0 0 0 3 0"/></svg>"#
}

fn page_same_origin(base: &str) -> bool {
    let base = base.trim().trim_end_matches('/');
    if base.is_empty() {
        return true;
    }
    web_sys::window()
        .and_then(|w| w.location().origin().ok())
        .map(|origin| origin.trim_end_matches('/') == base)
        .unwrap_or(false)
}

fn notification_permission() -> String {
    let Some(window) = web_sys::window() else {
        return "unsupported".into();
    };
    let Ok(ctor) = js_sys::Reflect::get(&window, &JsValue::from_str("Notification")) else {
        return "unsupported".into();
    };
    if ctor.is_undefined() || ctor.is_null() {
        return "unsupported".into();
    }
    match web_sys::Notification::permission() {
        web_sys::NotificationPermission::Granted => "granted".into(),
        web_sys::NotificationPermission::Denied => "denied".into(),
        _ => "default".into(),
    }
}

fn request_notification_permission() {
    let Some(window) = web_sys::window() else {
        return;
    };
    let Ok(ctor) = js_sys::Reflect::get(&window, &JsValue::from_str("Notification")) else {
        return;
    };
    if ctor.is_undefined() || ctor.is_null() {
        return;
    }
    let Ok(func) = js_sys::Reflect::get(&ctor, &JsValue::from_str("requestPermission")) else {
        return;
    };
    let Ok(func) = func.dyn_into::<js_sys::Function>() else {
        return;
    };
    let _ = func.apply(&ctor, &js_sys::Array::new());
}

fn show_os(title: &str, body: &str, href: &str, tag: &str) {
    if notification_permission() != "granted" {
        return;
    }
    let title = one_line(title, 80);
    let body = one_line(body, 140);
    if title.is_empty() {
        return;
    }
    let href = if href.starts_with("/#") {
        href.to_string()
    } else {
        "/".into()
    };
    let tag: String = tag
        .chars()
        .filter(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | ':' | '-'))
        .take(64)
        .collect();
    let tag = if tag.is_empty() { "strike".into() } else { tag };
    if let Some(ctrl) = sw_controller() {
        let obj = js_sys::Object::new();
        let _ = js_sys::Reflect::set(
            &obj,
            &JsValue::from_str("type"),
            &JsValue::from_str("notify"),
        );
        let _ = js_sys::Reflect::set(
            &obj,
            &JsValue::from_str("title"),
            &JsValue::from_str(&title),
        );
        let _ = js_sys::Reflect::set(&obj, &JsValue::from_str("body"), &JsValue::from_str(&body));
        let _ = js_sys::Reflect::set(&obj, &JsValue::from_str("href"), &JsValue::from_str(&href));
        let _ = js_sys::Reflect::set(&obj, &JsValue::from_str("tag"), &JsValue::from_str(&tag));
        let _ = ctrl.post_message(&obj);
        return;
    }
    let opts = web_sys::NotificationOptions::new();
    opts.set_body(&body);
    opts.set_tag(&tag);
    let _ = web_sys::Notification::new_with_options(&title, &opts);
}

fn sw_controller() -> Option<web_sys::ServiceWorker> {
    web_sys::window()?.navigator().service_worker().controller()
}

async fn put_prefs(base: &str, token: &str, prefs: &Prefs) -> Result<(), String> {
    let resp = Request::put(&api(base, "/v1/notifications/prefs"))
        .header("Authorization", &auth_header(token))
        .json(prefs)
        .map_err(|e| e.to_string())?
        .send()
        .await
        .map_err(|e| e.to_string())?;
    if (200..300).contains(&resp.status()) {
        Ok(())
    } else {
        Err(format!("HTTP {}", resp.status()))
    }
}

fn event_target(ev: &web_sys::Event) -> Option<web_sys::Element> {
    ev.target()?.dyn_into::<web_sys::Element>().ok()
}

fn event_in_field(ev: &web_sys::Event) -> bool {
    let Some(el) = event_target(ev) else {
        return false;
    };
    matches!(
        el.tag_name().to_ascii_lowercase().as_str(),
        "input" | "textarea" | "select"
    )
}

#[component]
fn NoteRow(
    note: Note,
    active: bool,
    unread: bool,
    muted: bool,
    activate: UnsyncCallback<Note>,
    toggle_mute: UnsyncCallback<String>,
) -> impl IntoView {
    let title = note.title.clone();
    let body = note.body.clone();
    let when = short_when(&note.at);
    let kind = kind_label(&note.kind).to_string();
    let session = note.session_id.clone();
    let show_body = !body.is_empty();
    let show_when = !when.is_empty();
    let mute_label = if muted { "Unmute" } else { "Mute" };
    let row_class = if active {
        "notify-row on"
    } else {
        "notify-row"
    };
    let mute = if session.is_empty() {
        ().into_any()
    } else {
        view! {
            <button class="secondary notify-mute" on:click=move |_| toggle_mute.run(session.clone())>{mute_label}</button>
        }
        .into_any()
    };
    view! {
        <div class=row_class>
            <button class="notify-open" on:click=move |_| activate.run(note.clone())>
                <span class="notify-kicker">
                    <span class="notify-kind">{kind}</span>
                    <Show when=move || unread><span class="notify-dot"></span></Show>
                    <Show when=move || muted><span class="notify-muted">"Muted"</span></Show>
                    <Show when=move || show_when><span class="notify-when">{when.clone()}</span></Show>
                </span>
                <span class="notify-title">{title}</span>
                <Show when=move || show_body><span class="notify-body">{body.clone()}</span></Show>
            </button>
            {mute}
        </div>
    }
}

fn event_in_centre(ev: &web_sys::Event) -> bool {
    let Some(el) = event_target(ev) else {
        return false;
    };
    el.closest(".notify").ok().flatten().is_some()
        || matches!(el.tag_name().to_ascii_lowercase().as_str(), "body" | "html")
}

#[component]
pub fn NotificationBell(
    base: RwSignal<String>,
    token: RwSignal<String>,
    gateway: RwSignal<String>,
    on_focus: Callback<FocusTarget>,
) -> impl IntoView {
    let open = RwSignal::new(false);
    let settings = RwSignal::new(false);
    let items = RwSignal::new(Vec::<Note>::new());
    let prefs = RwSignal::new(default_prefs());
    let seen = RwSignal::new(load_seen());
    let cursor = RwSignal::new(0usize);
    let load_error = RwSignal::new(String::new());
    let form_error = RwSignal::new(String::new());
    let perm = RwSignal::new(notification_permission());
    let linked = RwSignal::new(FocusTarget {
        session_id: String::new(),
        approval_id: String::new(),
    });
    let draft_enabled = RwSignal::new(true);
    let draft_quiet = RwSignal::new(false);
    let draft_start = RwSignal::new("22:00".to_string());
    let draft_end = RwSignal::new("07:00".to_string());
    OPEN_CENTRE.with(|slot| slot.set(Some(open)));

    let (stored_high, stored_primed) = match load_raw(HIGH_KEY).parse::<i64>() {
        Ok(n) if !load_raw(HIGH_KEY).is_empty() => (n, true),
        _ => (0, false),
    };
    let high = Rc::new(Cell::new(stored_high));
    let primed = Rc::new(Cell::new(stored_primed));
    let save_inflight = Rc::new(Cell::new(false));
    let stream_on = Rc::new(Cell::new(false));

    let apply_focus = UnsyncCallback::new(move |target: FocusTarget| {
        if target.session_id.is_empty() && target.approval_id.is_empty() {
            return;
        }
        linked.set(target.clone());
        on_focus.run(target);
    });

    if let Some(target) = web_sys::window()
        .and_then(|w| w.location().hash().ok())
        .and_then(|h| parse_focus(&h))
    {
        apply_focus.run(target);
    }

    let mark = move |id: String| {
        if id.is_empty() || seen.get_untracked().iter().any(|s| s == &id) {
            return;
        }
        seen.update(|list| list.push(id));
        save_seen(&seen.get_untracked());
    };

    let activate = UnsyncCallback::new(move |note: Note| {
        mark(note.id.clone());
        if let Some(target) = parse_focus(&note.href) {
            if let Some(window) = web_sys::window() {
                let hash = note.href.trim_start_matches("/#").trim_start_matches('#');
                if !hash.is_empty() && hash != "/" {
                    let _ = window.location().set_hash(hash);
                }
            }
            apply_focus.run(target);
        }
        open.set(false);
        settings.set(false);
    });

    let consider = UnsyncCallback::new({
        let high = high.clone();
        let primed = primed.clone();
        move |list: Vec<Note>| {
            let max = list.iter().map(|n| n.seq).max().unwrap_or(0);
            if !primed.get() {
                primed.set(true);
                high.set(max);
                save_raw(HIGH_KEY, &max.to_string());
                return;
            }
            let prev = high.get();
            let mut best: Option<&Note> = None;
            for note in &list {
                if note.seq <= prev || !note.banner || note.id.starts_with("local-") {
                    continue;
                }
                best = Some(match best {
                    None => note,
                    Some(cur) => prefer_banner(cur, note),
                });
            }
            if let Some(note) = best {
                show_os(&note.title, &note.body, &note.href, &note.id);
            }
            if max > prev {
                high.set(max);
                save_raw(HIGH_KEY, &max.to_string());
            }
        }
    });

    let refresh = UnsyncCallback::new({
        let save_inflight = save_inflight.clone();
        let stream_on = stream_on.clone();
        move |_: ()| {
            perm.set(notification_permission());
            let (b, t) = (base.get_untracked(), token.get_untracked());
            let save_inflight = save_inflight.clone();
            let stream_on = stream_on.clone();
            spawn_local(async move {
                match get_json::<NotesResponse>(&b, &t, "/v1/notifications").await {
                    Ok(resp) => {
                        if !save_inflight.get() {
                            prefs.set(resp.prefs);
                        }
                        let merged = merge_notes(&items.get_untracked(), &resp.notifications);
                        consider.run(merged.clone());
                        items.set(merged);
                        load_error.set(String::new());
                        if !stream_on.get() && page_same_origin(&b) {
                            stream_on.set(true);
                            start_stream(resp.latest, items, consider);
                        }
                    }
                    Err(e) => load_error.set(format!("Could not load notifications: {e}")),
                }
            });
        }
    });
    refresh.run(());

    let interval = Closure::<dyn FnMut()>::new(move || refresh.run(()));
    if let Some(window) = web_sys::window() {
        let _ = window.set_interval_with_callback_and_timeout_and_arguments_0(
            interval.as_ref().unchecked_ref(),
            2000,
        );
    }
    interval.forget();

    {
        if let Some(window) = web_sys::window() {
            let on_hash = Closure::<dyn FnMut(web_sys::Event)>::new(move |_ev| {
                if let Some(hash) = web_sys::window().and_then(|w| w.location().hash().ok()) {
                    if let Some(target) = parse_focus(&hash) {
                        apply_focus.run(target);
                    }
                }
            });
            let _ = window
                .add_event_listener_with_callback("hashchange", on_hash.as_ref().unchecked_ref());
            on_hash.forget();

            let on_sw = Closure::<dyn FnMut(web_sys::Event)>::new(move |ev: web_sys::Event| {
                let Ok(msg) = ev.dyn_into::<web_sys::MessageEvent>() else {
                    return;
                };
                let data = msg.data();
                let typ = js_sys::Reflect::get(&data, &JsValue::from_str("type"))
                    .ok()
                    .and_then(|v| v.as_string())
                    .unwrap_or_default();
                if typ != "notify-open" {
                    return;
                }
                let href = js_sys::Reflect::get(&data, &JsValue::from_str("href"))
                    .ok()
                    .and_then(|v| v.as_string())
                    .unwrap_or_default();
                if let Some(target) = parse_focus(&href) {
                    open.set(true);
                    apply_focus.run(target);
                }
            });
            if let Ok(container) = window
                .navigator()
                .service_worker()
                .dyn_into::<web_sys::EventTarget>()
            {
                let _ = container
                    .add_event_listener_with_callback("message", on_sw.as_ref().unchecked_ref());
            }
            on_sw.forget();
        }
    }

    let prev_gate = Rc::new(Cell::new(String::new()));
    let gate_down = Rc::new(Cell::new(false));
    Effect::new(move |_| {
        let now = gateway.get();
        let was = prev_gate.replace(now.clone());
        if now == "connected" {
            gate_down.set(false);
            return;
        }
        if now != "unreachable" || was.is_empty() || gate_down.get() {
            return;
        }
        gate_down.set(true);
        let note = Note {
            id: "local-gateway".into(),
            seq: 0,
            kind: "gateway.disconnected".into(),
            session_id: String::new(),
            subject_id: String::new(),
            title: "Gateway disconnected".into(),
            body: "This page lost the daemon. Open Strike again to catch up.".into(),
            href: "/".into(),
            request_id: String::new(),
            banner: false,
            at: String::new(),
        };
        items.update(|list| {
            list.retain(|n| n.id != "local-gateway");
            list.insert(0, note.clone());
        });
        let p = prefs.get_untracked();
        if p.enabled && !quiet_active(&p, local_minutes()) {
            show_os(&note.title, &note.body, &note.href, &note.id);
        }
    });

    let save = UnsyncCallback::new({
        let save_inflight = save_inflight.clone();
        move |next: Prefs| {
            if save_inflight.get() {
                return;
            }
            save_inflight.set(true);
            prefs.set(next.clone());
            let (b, t) = (base.get_untracked(), token.get_untracked());
            let save_inflight = save_inflight.clone();
            spawn_local(async move {
                let result = put_prefs(&b, &t, &next).await;
                save_inflight.set(false);
                match result {
                    Ok(()) => form_error.set(String::new()),
                    Err(e) => form_error.set(format!("Could not save notification settings: {e}")),
                }
            });
        }
    });

    let open_settings = UnsyncCallback::new(move |_: ()| {
        let p = prefs.get_untracked();
        draft_enabled.set(p.enabled);
        draft_quiet.set(p.quiet.enabled);
        draft_start.set(if p.quiet.start.is_empty() {
            "22:00".into()
        } else {
            p.quiet.start
        });
        draft_end.set(if p.quiet.end.is_empty() {
            "07:00".into()
        } else {
            p.quiet.end
        });
        settings.set(true);
        form_error.set(String::new());
    });

    let toggle_mute = UnsyncCallback::new(move |session: String| {
        let session = clean_id(&session);
        if session.is_empty() {
            return;
        }
        let mut next = prefs.get_untracked();
        if let Some(i) = next.muted.iter().position(|id| id == &session) {
            next.muted.remove(i);
        } else {
            next.muted.push(session);
        }
        save.run(next);
    });

    let on_key =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            let key = ev.key();
            if key == "n"
                && ev.alt_key()
                && !ev.meta_key()
                && !ev.ctrl_key()
                && !event_in_field(&ev)
            {
                ev.prevent_default();
                open.update(|v| *v = !*v);
                if open.get_untracked() {
                    settings.set(false);
                }
                return;
            }
            if !open.get_untracked() {
                return;
            }
            if key == "Escape" {
                ev.prevent_default();
                if settings.get_untracked() {
                    settings.set(false);
                } else {
                    open.set(false);
                }
                return;
            }
            if settings.get_untracked() || event_in_field(&ev) || !event_in_centre(&ev) {
                return;
            }
            let list = items.get_untracked();
            if list.is_empty() {
                return;
            }
            if key == "ArrowDown" {
                ev.prevent_default();
                cursor.update(|i| *i = (*i + 1).min(list.len() - 1));
            } else if key == "ArrowUp" {
                ev.prevent_default();
                cursor.update(|i| *i = i.saturating_sub(1));
            } else if key == "Enter" {
                ev.prevent_default();
                let i = cursor.get_untracked().min(list.len() - 1);
                activate.run(list[i].clone());
            }
        });
    if let Some(window) = web_sys::window() {
        let _ = window.add_event_listener_with_callback("keydown", on_key.as_ref().unchecked_ref());
    }
    on_key.forget();

    let unread = move || {
        let seen = seen.get();
        items
            .get()
            .iter()
            .filter(|n| !seen.iter().any(|id| id == &n.id))
            .count()
    };

    view! {
        <div class="notify">
            <button
                class="notify-bell"
                data-feature="notifications"
                title="Notifications (Alt+N)"
                aria-label="Notifications"
                aria-haspopup="dialog"
                aria-expanded=move || if open.get() { "true" } else { "false" }
                on:click=move |_| {
                    open.update(|v| *v = !*v);
                    settings.set(false);
                }
            >
                <span class="icon-slot" aria-hidden="true" inner_html=bell_svg()></span>
                {move || {
                    let n = unread();
                    (n > 0).then(|| {
                        let label = if n > 9 { "9+".into() } else { n.to_string() };
                        view! { <span class="notify-count">{label}</span> }
                    })
                }}
            </button>
            <Show when=move || !linked.get().approval_id.is_empty()>
                <div class="notify-strip" role="status">
                    <span>"Approval waiting in this session."</span>
                    <button class="secondary" on:click=move |_| {
                        linked.update(|t| t.approval_id.clear());
                        if let Some(window) = web_sys::window() {
                            let session = linked.get_untracked().session_id.clone();
                            if session.is_empty() {
                                let _ = window.location().set_hash("");
                            } else {
                                let _ = window.location().set_hash(&format!("session={session}"));
                            }
                        }
                    }>"Dismiss"</button>
                </div>
            </Show>
            <Show when=move || open.get()>
                <div class="notify-backdrop" on:click=move |_| { open.set(false); settings.set(false); }>
                    <div
                        class="notify-panel"
                        role="dialog"
                        aria-label="Notifications"
                        on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()
                    >
                        <Show when=move || settings.get() fallback=move || view! {
                                <div>
                                    <div class="notify-head">
                                        <h2>"Notifications"</h2>
                                        <div class="notify-head-actions">
                                            <button class="secondary" on:click=move |_| {
                                                let ids: Vec<String> = items.get_untracked().iter().map(|n| n.id.clone()).collect();
                                                seen.set(ids.clone());
                                                save_seen(&ids);
                                            }>"Mark all read"</button>
                                            <button class="secondary" on:click=move |_| open_settings.run(())>"Settings"</button>
                                        </div>
                                    </div>
                                    <Show when=move || !load_error.get().is_empty()>
                                        <div class="notify-error" role="alert">
                                            <span>{move || load_error.get()}</span>
                                            <button class="secondary" on:click=move |_| refresh.run(())>"Retry"</button>
                                        </div>
                                    </Show>
                                    <Show when=move || !form_error.get().is_empty()>
                                        <p class="notify-error" role="alert">{move || form_error.get()}</p>
                                    </Show>
                                    <div class="notify-list" aria-live="polite">
                                        {move || {
                                            let list = items.get();
                                            let muted = prefs.get().muted.clone();
                                            let seen_now = seen.get();
                                            if list.is_empty() && load_error.get().is_empty() {
                                                return view! {
                                                    <p class="notify-empty">"Nothing yet. Approvals, finished work, and a dropped gateway show up here."</p>
                                                }.into_any();
                                            }
                                            list.into_iter().enumerate().map(|(i, note)| {
                                                let unread_row = !seen_now.iter().any(|id| id == &note.id);
                                                let is_muted = muted.iter().any(|id| {
                                                    id == &note.session_id || id == &note.subject_id
                                                });
                                                view! {
                                                    <NoteRow
                                                        note=note
                                                        active=cursor.get() == i
                                                        unread=unread_row
                                                        muted=is_muted
                                                        activate=activate
                                                        toggle_mute=toggle_mute
                                                    />
                                                }
                                            }).collect_view().into_any()
                                        }}
                                    </div>
                                    <p class="notify-keys">"Alt+N opens this panel. Up and Down move. Enter opens. Esc closes."</p>
                                </div>
                        }>
                            <div>
                                <div class="notify-head">
                                    <h2>"Notification settings"</h2>
                                    <button class="secondary" on:click=move |_| settings.set(false)>"Back"</button>
                                </div>
                                <Show when=move || !form_error.get().is_empty()>
                                    <p class="notify-error" role="alert">{move || form_error.get()}</p>
                                </Show>
                                <label class="notify-check">
                                    <input type="checkbox" prop:checked=move || draft_enabled.get() on:change=move |_| draft_enabled.update(|v| *v = !*v) />
                                    <span>"Browser banners"</span>
                                </label>
                                <p class="notify-hint">"Banners use this browser only. The centre still keeps the record."</p>
                                <div class="notify-perm">
                                    <button class="secondary" on:click=move |_| {
                                        request_notification_permission();
                                        perm.set(notification_permission());
                                    }>"Allow on this browser"</button>
                                    <span>{move || match perm.get().as_str() {
                                        "granted" => "Allowed".to_string(),
                                        "denied" => "Blocked by the browser".to_string(),
                                        "unsupported" => "This browser has no notification API".to_string(),
                                        _ => "Not asked yet".to_string(),
                                    }}</span>
                                </div>
                                <label class="notify-check">
                                    <input type="checkbox" prop:checked=move || draft_quiet.get() on:change=move |_| draft_quiet.update(|v| *v = !*v) />
                                    <span>"Quiet hours"</span>
                                </label>
                                <div class="notify-times">
                                    <label>"From" <input type="time" prop:value=move || draft_start.get() on:input=move |ev| draft_start.set(event_target_value(&ev)) /></label>
                                    <label>"To" <input type="time" prop:value=move || draft_end.get() on:input=move |ev| draft_end.set(event_target_value(&ev)) /></label>
                                </div>
                                <p class="notify-hint">"Quiet hours use the daemon clock. A window that passes midnight, such as 22:00 to 07:00, wraps."</p>
                                <h3>"Muted sessions"</h3>
                                <Show when=move || prefs.get().muted.is_empty()>
                                    <p class="notify-hint">"No muted sessions. Mute is on each row in the centre."</p>
                                </Show>
                                <ul class="notify-muted-list">
                                    {move || prefs.get().muted.into_iter().map(|id| {
                                        let label = id.clone();
                                        let id_btn = id.clone();
                                        view! {
                                            <li>
                                                <span>{label}</span>
                                                <button class="secondary" on:click=move |_| toggle_mute.run(id_btn.clone())>"Unmute"</button>
                                            </li>
                                        }
                                    }).collect_view()}
                                </ul>
                                <button class="primary" on:click=move |_| {
                                    let mut next = prefs.get_untracked();
                                    next.enabled = draft_enabled.get_untracked();
                                    next.quiet.enabled = draft_quiet.get_untracked();
                                    next.quiet.start = draft_start.get_untracked();
                                    next.quiet.end = draft_end.get_untracked();
                                    save.run(next);
                                }>"Save"</button>
                            </div>
                        </Show>
                    </div>
                </div>
            </Show>
        </div>
    }
}

fn start_stream(since: i64, items: RwSignal<Vec<Note>>, consider: UnsyncCallback<Vec<Note>>) {
    let url = format!("/v1/notifications/stream?since={since}");
    let Ok(es) = web_sys::EventSource::new(&url) else {
        return;
    };
    let es = Rc::new(es);
    let on_msg =
        Closure::<dyn FnMut(web_sys::MessageEvent)>::new(move |ev: web_sys::MessageEvent| {
            let Some(data) = ev.data().as_string() else {
                return;
            };
            let Ok(note) = serde_json::from_str::<Note>(&data) else {
                return;
            };
            let merged = merge_notes(&items.get_untracked(), &[note]);
            consider.run(merged.clone());
            items.set(merged);
        });
    es.set_onmessage(Some(on_msg.as_ref().unchecked_ref()));
    on_msg.forget();
    // The page polls as well. A dead stream must not strand the socket.
    let es_err = es.clone();
    let on_err = Closure::<dyn FnMut(web_sys::Event)>::new(move |_ev| {
        es_err.close();
    });
    es.set_onerror(Some(on_err.as_ref().unchecked_ref()));
    on_err.forget();
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_focus_reads_the_hash_contract() {
        let got = parse_focus("/#session=root1&approval=root1:perm_2").unwrap();
        assert_eq!(got.session_id, "root1");
        assert_eq!(got.approval_id, "root1:perm_2");
        let swapped =
            parse_focus("http://127.0.0.1:8787/#approval=root1%3Aperm_2&session=root1").unwrap();
        assert_eq!(swapped, got);
        assert!(parse_focus("/").is_none());
        assert!(
            parse_focus("/#session=../secret").is_none()
                || parse_focus("/#session=../x").unwrap().session_id.is_empty()
        );
    }

    #[test]
    fn quiet_hours_match_the_daemon_window() {
        let mut prefs = default_prefs();
        prefs.quiet.enabled = true;
        prefs.quiet.start = "22:00".into();
        prefs.quiet.end = "07:00".into();
        assert!(quiet_active(&prefs, 23 * 60 + 30));
        assert!(quiet_active(&prefs, 22 * 60));
        assert!(!quiet_active(&prefs, 7 * 60));
        assert!(!quiet_active(&prefs, 12 * 60));
        prefs.quiet.enabled = false;
        assert!(!quiet_active(&prefs, 23 * 60));
    }

    #[test]
    fn merge_keeps_the_gateway_row_and_sorts_seq() {
        let local = Note {
            id: "local-gateway".into(),
            seq: 0,
            kind: "gateway.disconnected".into(),
            session_id: String::new(),
            subject_id: String::new(),
            title: "Gateway disconnected".into(),
            body: String::new(),
            href: "/".into(),
            request_id: String::new(),
            banner: false,
            at: String::new(),
        };
        let older = Note {
            id: "a".into(),
            seq: 1,
            kind: "session.finished".into(),
            title: "old".into(),
            ..local.clone()
        };
        let newer = Note {
            id: "b".into(),
            seq: 2,
            kind: "approval.needed".into(),
            title: "new".into(),
            ..local.clone()
        };
        let merged = merge_notes(&[local.clone(), older.clone()], &[older, newer]);
        assert_eq!(merged[0].id, "local-gateway");
        assert_eq!(merged[1].id, "b");
        assert_eq!(merged[2].id, "a");
    }

    #[test]
    fn one_line_drops_dumps_and_clamps() {
        assert_eq!(one_line("{\n\"output\":1}", 40), "Open Strike to read it.");
        assert_eq!(one_line("bash is waiting", 80), "bash is waiting");
        assert!(one_line(&"a".repeat(200), 20).chars().count() <= 20);
    }
}
