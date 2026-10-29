//! Side-panel terminal. The PTY lives in the daemon. This view attaches
//! over a same-origin websocket, replays scrollback, and sends keystrokes.
//! Leaving the tab closes the socket and leaves the process running.

use gloo_net::http::Request;
use leptos::html::Pre;
use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::Value;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::{JsCast, JsValue};
use web_sys::{Element, MessageEvent, ResizeObserver, WebSocket};

use crate::api::{api, auth_header, get_json, post_json_value};
use crate::outcome;
use crate::same_origin;
use crate::term::{key_bytes, Screen};
use crate::EmptyState;

#[derive(Clone, Deserialize, Default)]
struct TermInfo {
    id: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    cwd: String,
    #[serde(rename = "workspaceId", default)]
    workspace_id: String,
    #[serde(default)]
    alive: bool,
    #[serde(rename = "exitReason", default)]
    exit_reason: String,
    #[serde(rename = "onSessionEnd", default)]
    on_session_end: String,
}

#[derive(Deserialize)]
struct TermList {
    #[serde(default)]
    terminals: Vec<TermInfo>,
}

#[derive(Deserialize)]
struct TicketBody {
    ticket: String,
}

struct Live {
    ws: WebSocket,
    // Closures stay alive for as long as the socket does.
    _on_message: Closure<dyn FnMut(MessageEvent)>,
    _on_open: Closure<dyn FnMut(JsValue)>,
    _on_error: Closure<dyn FnMut(JsValue)>,
    _on_close: Closure<dyn FnMut(JsValue)>,
}

struct Fit {
    obs: ResizeObserver,
    _cb: Closure<dyn FnMut(js_sys::Array, ResizeObserver)>,
}

#[derive(Copy, Clone)]
struct TermCtl {
    base: RwSignal<String>,
    token: RwSignal<String>,
    workspace_id: RwSignal<String>,
    session_id: RwSignal<String>,
    sessions: RwSignal<Vec<TermInfo>>,
    active: RwSignal<String>,
    note: RwSignal<String>,
    html: RwSignal<String>,
    cwd: RwSignal<String>,
    alive: RwSignal<bool>,
    connected: RwSignal<bool>,
    exit_reason: RwSignal<String>,
    shared: RwSignal<bool>,
    same_dir: RwSignal<bool>,
    agent_busy: RwSignal<bool>,
    policy: RwSignal<String>,
    kill_on_end: RwSignal<bool>,
    epoch: RwSignal<u32>,
    creating: RwSignal<bool>,
    size: RwSignal<(i32, i32)>,
    timer_id: RwSignal<i32>,
    screen_ref: NodeRef<Pre>,
    screen: StoredValue<Screen, LocalStorage>,
    live: StoredValue<Option<Live>, LocalStorage>,
    fit: StoredValue<Option<Fit>, LocalStorage>,
    timer: StoredValue<Option<Closure<dyn FnMut()>>, LocalStorage>,
}

impl TermCtl {
    fn paint(self) {
        let rendered = self.screen.with_value(|screen| screen.render_html());
        self.html.set(rendered);
        if let Some(el) = self.screen_ref.get_untracked() {
            let el: &Element = &el;
            el.set_scroll_top(el.scroll_height());
        }
    }

    fn stop(self) {
        self.live.update_value(|live| {
            if let Some(cur) = live.take() {
                cur.ws.set_onmessage(None);
                cur.ws.set_onopen(None);
                cur.ws.set_onerror(None);
                cur.ws.set_onclose(None);
                let _ = cur.ws.close();
            }
        });
        self.connected.set(false);
    }

    fn send_raw(self, data: String) {
        let Ok(msg) = serde_json::to_string(&serde_json::json!({"type": "input", "data": data}))
        else {
            return;
        };
        self.live.with_value(|live| {
            if let Some(cur) = live.as_ref() {
                if cur.ws.ready_state() == WebSocket::OPEN {
                    let _ = cur.ws.send_with_str(&msg);
                }
            }
        });
    }

    fn send_resize(self, cols: i32, rows: i32, force: bool) {
        let cols = cols.clamp(2, 500);
        let rows = rows.clamp(2, 200);
        let changed = self.size.get_untracked() != (cols, rows);
        if !changed && !force {
            return;
        }
        if changed {
            self.size.set((cols, rows));
        }
        self.screen.update_value(|screen| {
            screen.resize(cols as usize, rows as usize);
        });
        self.paint();
        let Ok(msg) =
            serde_json::to_string(&serde_json::json!({"type": "resize", "cols": cols, "rows": rows}))
        else {
            return;
        };
        self.live.with_value(|live| {
            if let Some(cur) = live.as_ref() {
                if cur.ws.ready_state() == WebSocket::OPEN {
                    let _ = cur.ws.send_with_str(&msg);
                }
            }
        });
    }

    fn reset_screen(self, cols: i32, rows: i32, bytes: &[u8]) {
        let cols = if cols >= 2 { cols } else { 80 };
        let rows = if rows >= 2 { rows } else { 24 };
        let mut next = Screen::new(cols as usize, rows as usize);
        next.feed(bytes);
        self.screen.update_value(|screen| *screen = next);
        self.paint();
    }

    fn on_frame(self, raw: &str) {
        let Ok(v) = serde_json::from_str::<Value>(raw) else {
            return;
        };
        match v.get("type").and_then(|t| t.as_str()).unwrap_or("") {
            "hello" => {
                if let Some(id) = v.get("id").and_then(|x| x.as_str()) {
                    self.active.set(id.to_string());
                }
                self.cwd
                    .set(v.get("cwd").and_then(|x| x.as_str()).unwrap_or("").to_string());
                self.alive
                    .set(v.get("alive").and_then(|x| x.as_bool()).unwrap_or(true));
                self.exit_reason.set(
                    v.get("exitReason")
                        .and_then(|x| x.as_str())
                        .unwrap_or("")
                        .to_string(),
                );
                self.shared.set(flag(&v, "sharedWithAgent"));
                self.same_dir.set(flag(&v, "sameDirectory"));
                self.agent_busy.set(flag(&v, "agentBusy"));
                self.policy.set(
                    v.get("onSessionEnd")
                        .and_then(|x| x.as_str())
                        .unwrap_or("")
                        .to_string(),
                );
                let cols = v.get("cols").and_then(|x| x.as_i64()).unwrap_or(80) as i32;
                let rows = v.get("rows").and_then(|x| x.as_i64()).unwrap_or(24) as i32;
                let bytes = v
                    .get("scrollback")
                    .and_then(|x| x.as_str())
                    .map(decode_b64)
                    .unwrap_or_default();
                self.reset_screen(cols, rows, &bytes);
                self.note.set(String::new());
                let (fit_cols, fit_rows) = self.size.get_untracked();
                if fit_cols >= 2 && fit_rows >= 2 && (fit_cols != cols || fit_rows != rows) {
                    self.send_resize(fit_cols, fit_rows, true);
                }
            }
            "output" => {
                if let Some(data) = v.get("data").and_then(|x| x.as_str()) {
                    let bytes = decode_b64(data);
                    if !bytes.is_empty() {
                        self.screen.update_value(|screen| screen.feed(&bytes));
                        self.paint();
                    }
                }
            }
            "status" => {
                self.shared.set(flag(&v, "sharedWithAgent"));
                self.same_dir.set(flag(&v, "sameDirectory"));
                self.agent_busy.set(flag(&v, "agentBusy"));
                if let Some(dir) = v.get("cwd").and_then(|x| x.as_str()) {
                    if !dir.is_empty() {
                        self.cwd.set(dir.to_string());
                    }
                }
                if let Some(up) = v.get("alive").and_then(|x| x.as_bool()) {
                    self.alive.set(up);
                }
            }
            "exit" => {
                self.alive.set(false);
                let reason = v.get("reason").and_then(|x| x.as_str()).unwrap_or("exited");
                self.exit_reason.set(reason.to_string());
            }
            _ => {}
        }
    }

    fn attach(self, id: String) {
        if !same_origin(&self.base.get_untracked()) {
            self.note
                .set("Open this daemon directly to use the terminal.".into());
            return;
        }
        let url = match stream_url(&id) {
            Ok(url) => url,
            Err(err) => {
                self.note.set(err);
                return;
            }
        };
        let b = self.base.get_untracked();
        let t = self.token.get_untracked();
        self.active.set(id.clone());
        spawn_local(async move {
            let ticket = match outcome::require_ticket(mint_ticket(&b, &t, &id).await) {
                Ok(ticket) => ticket,
                Err(note) => {
                    self.stop();
                    self.note.set(note);
                    return;
                }
            };
            self.stop();
            let sock = match open_socket(&url, &ticket) {
                Ok(sock) => sock,
                Err(err) => {
                    self.note.set(err);
                    return;
                }
            };
            let ctl = self;
            let on_message = Closure::wrap(Box::new(move |ev: MessageEvent| {
                if let Some(text) = ev.data().as_string() {
                    ctl.on_frame(&text);
                }
            }) as Box<dyn FnMut(MessageEvent)>);
            let ctl = self;
            let on_open = Closure::wrap(Box::new(move |_| {
                ctl.connected.set(true);
                let (cols, rows) = ctl.size.get_untracked();
                if cols >= 2 && rows >= 2 {
                    ctl.send_resize(cols, rows, true);
                }
            }) as Box<dyn FnMut(JsValue)>);
            let ctl = self;
            let on_error = Closure::wrap(Box::new(move |_| {
                let failed = outcome::socket_failed();
                ctl.connected.set(failed.connected);
                ctl.note.set(failed.note);
            }) as Box<dyn FnMut(JsValue)>);
            let ctl = self;
            let on_close = Closure::wrap(Box::new(move |_| {
                ctl.connected.set(false);
            }) as Box<dyn FnMut(JsValue)>);
            sock.set_onmessage(Some(on_message.as_ref().unchecked_ref()));
            sock.set_onopen(Some(on_open.as_ref().unchecked_ref()));
            sock.set_onerror(Some(on_error.as_ref().unchecked_ref()));
            sock.set_onclose(Some(on_close.as_ref().unchecked_ref()));
            self.live.update_value(|live| {
                *live = Some(Live {
                    ws: sock,
                    _on_message: on_message,
                    _on_open: on_open,
                    _on_error: on_error,
                    _on_close: on_close,
                });
            });
        });
    }

    fn refresh(self, create_if_empty: bool) {
        let n = self.epoch.get_untracked();
        let b = self.base.get_untracked();
        let t = self.token.get_untracked();
        let workspace = self.workspace_id.get_untracked();
        let session = self.session_id.get_untracked();
        let kill = self.kill_on_end.get_untracked();
        spawn_local(async move {
            if workspace.is_empty() {
                self.sessions.set(Vec::new());
                self.note.set(
                    "Add a workspace in Daemon. The shell starts in that directory.".into(),
                );
                return;
            }
            if !same_origin(&b) {
                self.note
                    .set("Open this daemon directly to use the terminal.".into());
                return;
            }
            let list = match get_json::<TermList>(&b, &t, "/v1/terminals").await {
                Ok(list) => list,
                Err(err) => {
                    if self.epoch.get_untracked() == n {
                        self.note.set(explain(err));
                    }
                    return;
                }
            };
            if self.epoch.get_untracked() != n {
                return;
            }
            let mut items: Vec<TermInfo> = list
                .terminals
                .into_iter()
                .filter(|item| item.workspace_id == workspace)
                .collect();
            items.sort_by(|a, b| a.title.cmp(&b.title).then(a.id.cmp(&b.id)));
            let empty = items.is_empty();
            self.sessions.set(items.clone());
            if empty && create_if_empty {
                if self.creating.get_untracked() {
                    return;
                }
                self.creating.set(true);
                let spec = serde_json::json!({
                    "workspaceId": workspace,
                    "sessionId": session,
                    "onSessionEnd": if kill { "kill" } else { "keep" },
                });
                match post_json_value(&b, &t, "/v1/terminals", spec).await {
                    Ok(v) => {
                        self.creating.set(false);
                        if self.epoch.get_untracked() != n {
                            if let Ok(info) = serde_json::from_value::<TermInfo>(v) {
                                let _ = delete_term(&b, &t, &info.id).await;
                            }
                            return;
                        }
                        match serde_json::from_value::<TermInfo>(v) {
                            Ok(info) => {
                                self.note.set(String::new());
                                let id = info.id.clone();
                                self.sessions.update(|list| list.push(info));
                                self.attach(id);
                            }
                            Err(err) => self.note.set(err.to_string()),
                        }
                    }
                    Err(err) => {
                        self.creating.set(false);
                        if self.epoch.get_untracked() == n {
                            self.note.set(explain(err));
                        }
                    }
                }
                return;
            }
            let active = self.active.get_untracked();
            if let Some(cur) = items.iter().find(|item| item.id == active) {
                self.policy.set(cur.on_session_end.clone());
                if !cur.cwd.is_empty() {
                    self.cwd.set(cur.cwd.clone());
                }
                self.alive.set(cur.alive);
                if !cur.exit_reason.is_empty() {
                    self.exit_reason.set(cur.exit_reason.clone());
                }
            } else if active.is_empty() {
                if let Some(cur) = items.iter().find(|item| item.alive) {
                    self.attach(cur.id.clone());
                }
            }
        });
    }

    fn open_new(self) {
        let workspace = self.workspace_id.get_untracked();
        if workspace.is_empty() {
            self.note
                .set("Add a workspace in Daemon. The shell starts in that directory.".into());
            return;
        }
        self.epoch
            .set(self.epoch.get_untracked().wrapping_add(1));
        let n = self.epoch.get_untracked();
        let b = self.base.get_untracked();
        let t = self.token.get_untracked();
        let session = self.session_id.get_untracked();
        let kill = self.kill_on_end.get_untracked();
        spawn_local(async move {
            let spec = serde_json::json!({
                "workspaceId": workspace,
                "sessionId": session,
                "onSessionEnd": if kill { "kill" } else { "keep" },
            });
            match post_json_value(&b, &t, "/v1/terminals", spec).await {
                Ok(v) => {
                    if self.epoch.get_untracked() != n {
                        if let Ok(info) = serde_json::from_value::<TermInfo>(v.clone()) {
                            let _ = delete_term(&b, &t, &info.id).await;
                        }
                        return;
                    }
                    match serde_json::from_value::<TermInfo>(v) {
                        Ok(info) => {
                            self.note.set(String::new());
                            let id = info.id.clone();
                            self.sessions.update(|list| list.push(info));
                            self.attach(id);
                        }
                        Err(err) => self.note.set(err.to_string()),
                    }
                }
                Err(err) => {
                    if self.epoch.get_untracked() == n {
                        self.note.set(explain(err));
                    }
                }
            }
        });
    }

    fn close_active(self) {
        let id = self.active.get_untracked();
        if id.is_empty() {
            return;
        }
        let b = self.base.get_untracked();
        let t = self.token.get_untracked();
        spawn_local(async move {
            match delete_term(&b, &t, &id).await {
                Ok(()) => {
                    self.sessions.update(|list| {
                        let ids: Vec<String> = list.iter().map(|item| item.id.clone()).collect();
                        let kept = retain_session_ids(&ids, &id);
                        list.retain(|item| kept.iter().any(|keep| keep == &item.id));
                    });
                    let effects = close_ok_effects(close_landing(&id, &self.active.get_untracked()));
                    if effects.stop_live {
                        self.stop();
                    }
                    if effects.clear_active {
                        self.active.set(String::new());
                        self.alive.set(false);
                        self.exit_reason.set(String::new());
                    }
                    if let Some(note) = effects.notice {
                        self.note.set(note);
                    }
                }
                Err(err) => {
                    if let Some(note) =
                        close_err_notice(close_landing(&id, &self.active.get_untracked()), &explain(err))
                    {
                        self.note.set(note);
                    }
                }
            }
        });
    }
}

#[component]
pub fn TerminalPanel(
    base: RwSignal<String>,
    token: RwSignal<String>,
    workspace_id: RwSignal<String>,
    session_id: RwSignal<String>,
) -> impl IntoView {
    let screen = StoredValue::new_local(Screen::new(80, 24));
    let html = RwSignal::new(screen.with_value(|s| s.render_html()));
    let ctl = TermCtl {
        base,
        token,
        workspace_id,
        session_id,
        sessions: RwSignal::new(Vec::new()),
        active: RwSignal::new(String::new()),
        note: RwSignal::new(String::new()),
        html,
        cwd: RwSignal::new(String::new()),
        alive: RwSignal::new(false),
        connected: RwSignal::new(false),
        exit_reason: RwSignal::new(String::new()),
        shared: RwSignal::new(false),
        same_dir: RwSignal::new(false),
        agent_busy: RwSignal::new(false),
        policy: RwSignal::new(String::new()),
        kill_on_end: RwSignal::new(true),
        epoch: RwSignal::new(0),
        creating: RwSignal::new(false),
        size: RwSignal::new((0, 0)),
        timer_id: RwSignal::new(0),
        screen_ref: NodeRef::<Pre>::new(),
        screen,
        live: StoredValue::new_local(None),
        fit: StoredValue::new_local(None),
        timer: StoredValue::new_local(None),
    };

    Effect::new(move |_| {
        let _ = ctl.workspace_id.get();
        ctl.epoch
            .set(ctl.epoch.get_untracked().wrapping_add(1));
        ctl.active.set(String::new());
        ctl.stop();
        ctl.refresh(true);
    });

    let tick = ctl;
    let cb = Closure::wrap(Box::new(move || tick.refresh(false)) as Box<dyn FnMut()>);
    if let Some(win) = web_sys::window() {
        if let Ok(id) = win.set_interval_with_callback_and_timeout_and_arguments_0(
            cb.as_ref().unchecked_ref(),
            2000,
        ) {
            ctl.timer_id.set(id);
        }
    }
    ctl.timer.update_value(|slot| *slot = Some(cb));

    ctl.screen_ref.on_load(move |el| {
        let ctl_fit = ctl;
        let cb = Closure::wrap(Box::new(move |entries: js_sys::Array, _obs: ResizeObserver| {
            let Some(entry) = entries
                .get(0)
                .dyn_into::<web_sys::ResizeObserverEntry>()
                .ok()
            else {
                return;
            };
            let rect = entry.content_rect();
            let cols = (rect.width() / 7.2).floor() as i32;
            let rows = (rect.height() / 16.0).floor() as i32;
            if cols >= 2 && rows >= 2 {
                ctl_fit.send_resize(cols, rows, false);
            }
        }) as Box<dyn FnMut(js_sys::Array, ResizeObserver)>);
        let Ok(obs) = ResizeObserver::new(cb.as_ref().unchecked_ref()) else {
            return;
        };
        let el: &Element = &el;
        obs.observe(el);
        ctl.fit.update_value(|slot| {
            if let Some(old) = slot.take() {
                old.obs.disconnect();
            }
            *slot = Some(Fit { obs, _cb: cb });
        });
    });

    on_cleanup(move || {
        let id = ctl.timer_id.get_untracked();
        if id != 0 {
            if let Some(win) = web_sys::window() {
                win.clear_interval_with_handle(id);
            }
        }
        ctl.timer.update_value(|slot| {
            slot.take();
        });
        ctl.fit.update_value(|slot| {
            if let Some(fit) = slot.take() {
                fit.obs.disconnect();
            }
        });
        ctl.stop();
    });

    let sessions = ctl.sessions;
    let active = ctl.active;
    let note = ctl.note;
    let html = ctl.html;
    let cwd = ctl.cwd;
    let alive = ctl.alive;
    let connected = ctl.connected;
    let exit_reason = ctl.exit_reason;
    let shared = ctl.shared;
    let same_dir = ctl.same_dir;
    let agent_busy = ctl.agent_busy;
    let policy = ctl.policy;
    let kill_on_end = ctl.kill_on_end;
    let workspace_id = ctl.workspace_id;
    let screen_ref = ctl.screen_ref;

    view! {
        <div class="term">
            <div class="term-bar">
                {move || {
                    sessions.get().into_iter().map(|item| {
                        let id = item.id.clone();
                        let id_class = id.clone();
                        let label = if item.title.is_empty() { short_term(&item.id) } else { item.title.clone() };
                        let dead = !item.alive;
                        let ctl_btn = ctl;
                        view! {
                            <button
                                class=move || if active.get() == id_class { "chip on" } else { "chip" }
                                on:click=move |_| ctl_btn.attach(id.clone())
                            >
                                {label}
                                {dead.then_some(" · stopped")}
                            </button>
                        }
                    }).collect_view()
                }}
                <button class="chip" on:click=move |_| ctl.open_new()>"New"</button>
                <button class="chip" disabled=move || active.get().is_empty() on:click=move |_| ctl.close_active()>"Close"</button>
            </div>
            <label class="term-check">
                <input
                    type="checkbox"
                    prop:checked=move || kill_on_end.get()
                    on:change=move |ev| {
                        let Some(target) = ev.target() else { return };
                        let Ok(input) = target.dyn_into::<web_sys::HtmlInputElement>() else { return };
                        kill_on_end.set(input.checked());
                    }
                />
                "Close new shells when the session ends"
            </label>
            <Show when=move || shared.get() && agent_busy.get()>
                <p class="term-banner" role="status">"You and the agent share this directory. This shell is yours — the agent does not type here."</p>
            </Show>
            <Show when=move || same_dir.get() && !(shared.get() && agent_busy.get())>
                <p class="hint" role="status">"Same directory as this session. The agent is idle, and it does not type in this shell."</p>
            </Show>
            <Show when=move || !policy.get().is_empty()>
                <p class="hint">{move || if policy.get() == "keep" {
                    "This shell keeps running after the session ends.".to_string()
                } else {
                    "This shell closes when the session ends.".to_string()
                }}</p>
            </Show>
            <Show when=move || !exit_reason.get().is_empty()>
                <p class="term-banner term-exit" role="alert">{move || format!("Shell stopped: {}", exit_reason.get())}</p>
            </Show>
            <Show when=move || alive.get() && !connected.get()>
                <button class="secondary" on:click=move |_| {
                    let id = active.get();
                    if !id.is_empty() {
                        ctl.attach(id);
                    }
                }>"Reconnect"</button>
            </Show>
            <Show when=move || !note.get().is_empty()>
                <div class="notice" role="alert"><span>{move || note.get()}</span></div>
            </Show>
            <Show when=move || workspace_id.get().is_empty()>
                <EmptyState title="No workspace".into() body="Add a workspace in Daemon. The shell starts in that directory.".into() />
            </Show>
            <Show when=move || !workspace_id.get().is_empty()>
                <pre
                    class="term-screen"
                    tabindex=0
                    node_ref=screen_ref
                    inner_html=move || html.get()
                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                        if let Some(data) = key_bytes(&ev.key(), ev.ctrl_key(), ev.alt_key(), ev.meta_key()) {
                            ev.prevent_default();
                            ctl.send_raw(data);
                        }
                    }
                    on:paste=move |ev: web_sys::ClipboardEvent| {
                        if let Some(data) = ev.clipboard_data().and_then(|d| d.get_data("text/plain").ok()) {
                            if !data.is_empty() {
                                ev.prevent_default();
                                ctl.send_raw(data);
                            }
                        }
                    }
                ></pre>
            </Show>
            <p class="hint">{move || {
                let dir = cwd.get();
                if dir.is_empty() {
                    "Click the screen, then type. Leaving this tab keeps the shell running.".into()
                } else {
                    format!("{dir} · click the screen, then type. Leaving this tab keeps the shell running.")
                }
            }}</p>
        </div>
    }
}

fn flag(v: &Value, key: &str) -> bool {
    v.get(key).and_then(|x| x.as_bool()).unwrap_or(false)
}

fn short_term(id: &str) -> String {
    let count = id.chars().count();
    if count <= 6 {
        return id.to_string();
    }
    id.chars().skip(count - 6).collect()
}

fn explain(err: String) -> String {
    if let Some(i) = err.find('{') {
        if let Ok(v) = serde_json::from_str::<Value>(&err[i..]) {
            if let Some(s) = v.get("error").and_then(|e| e.as_str()) {
                if !s.is_empty() {
                    return s.to_string();
                }
            }
        }
    }
    if err.contains("unauthorized") {
        return "unauthorized: check the daemon token".into();
    }
    err
}

fn decode_b64(data: &str) -> Vec<u8> {
    let Some(win) = web_sys::window() else {
        return Vec::new();
    };
    let Ok(bin) = win.atob(data) else {
        return Vec::new();
    };
    bin.chars().map(|ch| ch as u8).collect()
}

fn stream_url(id: &str) -> Result<String, String> {
    let win = web_sys::window().ok_or_else(|| "no window".to_string())?;
    let loc = win.location();
    let protocol = loc.protocol().map_err(|_| "no protocol".to_string())?;
    let host = loc.host().map_err(|_| "no host".to_string())?;
    let ws = if protocol == "https:" { "wss" } else { "ws" };
    Ok(format!("{ws}://{host}/v1/terminals/{id}/stream"))
}

fn open_socket(url: &str, ticket: &str) -> Result<WebSocket, String> {
    let protos = js_sys::Array::new();
    protos.push(&JsValue::from_str("strike.terminal"));
    if !ticket.is_empty() {
        protos.push(&JsValue::from_str(&format!("ticket.{ticket}")));
    }
    WebSocket::new_with_str_sequence(url, &protos).map_err(|_| "terminal socket failed".to_string())
}

async fn mint_ticket(base: &str, token: &str, id: &str) -> Result<String, String> {
    let path = format!("/v1/terminals/{id}/ticket");
    let v = post_json_value(base, token, &path, serde_json::json!({})).await?;
    let body: TicketBody = serde_json::from_value(v).map_err(|e| e.to_string())?;
    Ok(body.ticket)
}

async fn delete_term(base: &str, token: &str, id: &str) -> Result<(), String> {
    let path = format!("/v1/terminals/{id}");
    let req = Request::delete(&api(base, &path));
    let req = if token.is_empty() {
        req
    } else {
        req.header("Authorization", &auth_header(token))
    };
    let resp = req.send().await.map_err(|e| e.to_string())?;
    if !(200..300).contains(&resp.status()) {
        let text = resp.text().await.unwrap_or_default();
        return Err(format!("HTTP {} {text}", resp.status()));
    }
    Ok(())
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum CloseLanding {
    Active,
    Superseded,
}

struct CloseEffects {
    stop_live: bool,
    clear_active: bool,
    notice: Option<String>,
}

fn close_landing(closed_id: &str, active_now: &str) -> CloseLanding {
    if !closed_id.is_empty() && closed_id == active_now {
        CloseLanding::Active
    } else {
        CloseLanding::Superseded
    }
}

fn close_ok_effects(landing: CloseLanding) -> CloseEffects {
    match landing {
        CloseLanding::Active => CloseEffects {
            stop_live: true,
            clear_active: true,
            notice: Some("Shell closed.".into()),
        },
        CloseLanding::Superseded => CloseEffects {
            stop_live: false,
            clear_active: false,
            notice: None,
        },
    }
}

fn close_err_notice(landing: CloseLanding, message: &str) -> Option<String> {
    match landing {
        CloseLanding::Active => Some(message.to_string()),
        CloseLanding::Superseded => None,
    }
}

fn retain_session_ids(ids: &[String], closed_id: &str) -> Vec<String> {
    ids.iter().filter(|id| id.as_str() != closed_id).cloned().collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn close_of_a_does_not_stop_shell_b() {
        assert_eq!(close_landing("shell-a", "shell-b"), CloseLanding::Superseded);
        let moved = close_ok_effects(CloseLanding::Superseded);
        assert!(!moved.stop_live);
        assert!(!moved.clear_active);
        assert!(moved.notice.is_none());
        assert!(close_err_notice(CloseLanding::Superseded, "HTTP 500").is_none());
        let chips = retain_session_ids(&["shell-a".into(), "shell-b".into()], "shell-a");
        assert_eq!(chips, vec!["shell-b".to_string()]);
        let stayed = close_ok_effects(close_landing("shell-a", "shell-a"));
        assert!(stayed.stop_live);
        assert!(stayed.clear_active);
        assert_eq!(stayed.notice.as_deref(), Some("Shell closed."));
        assert_eq!(
            close_err_notice(CloseLanding::Active, "HTTP 500").as_deref(),
            Some("HTTP 500")
        );
    }
}
