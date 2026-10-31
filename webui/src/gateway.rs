//! Gateway switcher. Profiles are names and URLs only. Short-lived grants
//! live in sessionStorage for this tab, never in the URL and never in
//! localStorage. `strike connect` is what puts a remote loopback daemon on
//! a local port; this component only switches among daemons the browser
//! can already reach.

use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{api, auth_header};
use crate::outcome;

/// Page wiring for the shell slot in `src/shell/contrib/gateway.rs`.
/// The shell boots before App's signals exist, so App installs them here
/// (same late-bind shape as `crate::status::StatusHost`) and the slot
/// render reads them. This replaces the pre-shell header mount; the
/// status aggregate keeps its own `gateway` slot (#1263) untouched.
#[derive(Clone)]
pub struct GatewayHost {
    pub base: RwSignal<String>,
    pub token: RwSignal<String>,
    pub gateway: RwSignal<String>,
    pub detail: RwSignal<String>,
    pub open: RwSignal<bool>,
    pub profiles: RwSignal<Vec<Profile>>,
    pub selected: RwSignal<String>,
    pub refresh: Rc<dyn Fn()>,
}

thread_local! {
    static HOST: RefCell<Option<GatewayHost>> = const { RefCell::new(None) };
}

pub fn set_host(host: GatewayHost) {
    HOST.with(|slot| *slot.borrow_mut() = Some(host));
}

fn host() -> Option<GatewayHost> {
    HOST.with(|slot| slot.borrow().clone())
}

/// Slot render for the `gateway-switcher` slot: the primary switcher.
/// The refresh closure re-reads the host so it stays `Copy` for the
/// component's prop, matching how App passes `refresh_meta`.
pub fn switcher_view() -> AnyView {
    let Some(h0) = host() else {
        return view! { <span></span> }.into_any();
    };
    view! {
        <GatewaySwitcher
            inline=false
            primary=true
            base=h0.base
            token=h0.token
            gateway=h0.gateway
            detail=h0.detail
            open=h0.open
            profiles=h0.profiles
            selected=h0.selected
            refresh=move || {
                if let Some(h) = host() {
                    (*h.refresh)();
                }
            }
        />
    }
    .into_any()
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Profile {
    pub id: String,
    pub name: String,
    pub base: String,
    #[serde(default)]
    pub ssh: String,
    #[serde(default)]
    pub warn: bool,
}

fn local_store() -> Option<web_sys::Storage> {
    web_sys::window()?.local_storage().ok().flatten()
}

fn session_store() -> Option<web_sys::Storage> {
    web_sys::window()?.session_storage().ok().flatten()
}

fn local_get(key: &str) -> String {
    local_store()
        .and_then(|s| s.get_item(key).ok().flatten())
        .unwrap_or_default()
}

fn local_set(key: &str, value: &str) {
    if let Some(s) = local_store() {
        let _ = s.set_item(key, value);
    }
}

fn local_remove(key: &str) {
    if let Some(s) = local_store() {
        let _ = s.remove_item(key);
    }
}

const TOKEN_KEY: &str = "strike.gateway.tokens";

fn token_map() -> HashMap<String, String> {
    session_store()
        .and_then(|s| s.get_item(TOKEN_KEY).ok().flatten())
        .and_then(|raw| serde_json::from_str(&raw).ok())
        .unwrap_or_default()
}

fn write_token_map(map: &HashMap<String, String>) {
    if let Some(s) = session_store() {
        if let Ok(raw) = serde_json::to_string(map) {
            let _ = s.set_item(TOKEN_KEY, &raw);
        }
    }
}

pub fn session_token(id: &str) -> String {
    token_map().get(id).cloned().unwrap_or_default()
}

/// Pure core of [`persist_token`]: an empty token deletes the entry, a
/// non-empty one stores it. Removal paths (#1562) go through the empty
/// case so a removed profile's credential leaves with the profile.
fn updated_token_map(
    mut map: HashMap<String, String>,
    id: &str,
    token: &str,
) -> HashMap<String, String> {
    if token.is_empty() {
        map.remove(id);
    } else {
        map.insert(id.to_string(), token.to_string());
    }
    map
}

pub fn persist_token(id: &str, token: &str) {
    write_token_map(&updated_token_map(token_map(), id, token));
}

/// Moves a legacy localStorage token into this tab's session and deletes it.
pub fn initial_token() -> String {
    let legacy = local_get("strike.token");
    if !legacy.is_empty() {
        if session_token("this").is_empty() {
            persist_token("this", &legacy);
        }
        local_remove("strike.token");
    }
    session_token("this")
}

pub fn forget_persisted_host_token() {
    local_remove("strike.token");
}

/// Records a cross-origin paired session as a gateway profile plus a
/// sessionStorage grant. The daemon token is not written. A public tunnel
/// origin is kept with the switcher's warning rather than dropped.
pub fn remember_paired_gateway(origin: &str, session: &str) {
    let origin = origin.trim().trim_end_matches('/').to_string();
    if origin.is_empty() || session.is_empty() {
        return;
    }
    let mut list = load_profiles();
    let id = if let Some(found) = list.iter().find(|p| p.base.trim_end_matches('/') == origin) {
        found.id.clone()
    } else {
        let warn = classify_base(&origin).unwrap_or(true);
        let id = format!("paired-{}", list.len() + 1);
        list.push(Profile {
            id: id.clone(),
            name: "Paired".into(),
            base: origin.clone(),
            ssh: String::new(),
            warn,
        });
        save_profiles(&list);
        id
    };
    persist_token(&id, session);
    if let Some(h) = host() {
        h.profiles.set(load_profiles());
        h.selected.set(id);
        h.base.set(origin.clone());
        h.token.set(session.to_string());
        local_set("strike.base", &origin);
        forget_persisted_host_token();
    }
}

/// Drops bearer tokens kept for this tab. Settings clear calls this.
/// Profiles live in localStorage and are removed with the other `strike.*` keys.
pub fn clear_session_tokens() {
    if let Some(s) = session_store() {
        let _ = s.remove_item(TOKEN_KEY);
    }
}

pub fn load_profiles() -> Vec<Profile> {
    let raw = local_get("strike.gateways");
    if !raw.is_empty() {
        if let Ok(list) = serde_json::from_str::<Vec<Profile>>(&raw) {
            if raw.contains("\"token\"") {
                save_profiles(&list);
            }
            return list;
        }
    }
    let legacy = local_get("strike.remotes");
    let mut list = Vec::new();
    if let Ok(remotes) = serde_json::from_str::<Vec<crate::model::Remote>>(&legacy) {
        for (i, remote) in remotes.into_iter().enumerate() {
            if remote.url.trim().is_empty() {
                continue;
            }
            let warn = classify_base(&remote.url).unwrap_or(true);
            list.push(Profile {
                id: format!("migrated-{i}"),
                name: if remote.name.trim().is_empty() {
                    format!("Daemon {i}")
                } else {
                    remote.name
                },
                base: remote.url,
                ssh: String::new(),
                warn,
            });
        }
        if !list.is_empty() {
            save_profiles(&list);
        }
    }
    list
}

pub fn save_profiles(list: &[Profile]) {
    if let Ok(raw) = serde_json::to_string(list) {
        local_set("strike.gateways", &raw);
    }
}

/// Ok(true) means a hostname that is not a literal private address.
/// The bearer would leave the machine, so the switcher warns.
pub fn classify_base(raw: &str) -> Result<bool, String> {
    let raw = raw.trim();
    if raw.is_empty() {
        return Err("A saved gateway needs a base URL.".into());
    }
    let lower = raw.to_ascii_lowercase();
    if lower.contains("token=") || lower.contains("bearer ") {
        return Err("The URL must not contain a token.".into());
    }
    if raw.contains('?') || raw.contains('#') || raw.contains('@') {
        return Err("The URL must not contain a query, fragment, or user.".into());
    }
    if !lower.starts_with("http://") && !lower.starts_with("https://") {
        return Err("Use an http or https URL.".into());
    }
    let host = host_of(raw).ok_or_else(|| "That URL has no host.".to_string())?;
    if host.eq_ignore_ascii_case("localhost") {
        return Ok(false);
    }
    if let Some(v4) = parse_v4(&host) {
        if is_loopback_or_private_v4(v4) {
            return Ok(false);
        }
        return Err(
            "Public addresses are refused. Forward the daemon with strike connect or SSH.".into(),
        );
    }
    if host.contains(':') {
        let h = host
            .trim_matches(|c| c == '[' || c == ']')
            .to_ascii_lowercase();
        if h == "::1" || h.starts_with("fc") || h.starts_with("fd") || h.starts_with("fe80:") {
            return Ok(false);
        }
        return Err("That address is not a loopback or private target.".into());
    }
    Ok(true)
}

fn host_of(raw: &str) -> Option<String> {
    let rest = raw.split_once("://")?.1;
    let hostport = rest.split(['/', '?', '#']).next().unwrap_or(rest);
    if hostport.is_empty() || hostport.contains('@') {
        return None;
    }
    if let Some(stripped) = hostport.strip_prefix('[') {
        let end = stripped.find(']')?;
        return Some(stripped[..end].to_string());
    }
    if let Some((host, port)) = hostport.rsplit_once(':') {
        if !port.is_empty() && port.bytes().all(|b| b.is_ascii_digit()) {
            return Some(host.to_string());
        }
    }
    Some(hostport.to_string())
}

fn parse_v4(host: &str) -> Option<[u8; 4]> {
    let parts: Vec<&str> = host.split('.').collect();
    if parts.len() != 4 {
        return None;
    }
    let mut out = [0u8; 4];
    for (i, part) in parts.iter().enumerate() {
        if part.is_empty() || part.len() > 3 {
            return None;
        }
        if part.len() > 1 && part.starts_with('0') {
            return None;
        }
        let n: u16 = part.parse().ok()?;
        if n > 255 {
            return None;
        }
        out[i] = n as u8;
    }
    Some(out)
}

fn is_loopback_or_private_v4(v: [u8; 4]) -> bool {
    if v[0] == 0 {
        return false;
    }
    v[0] == 127
        || v[0] == 10
        || (v[0] == 192 && v[1] == 168)
        || (v[0] == 172 && (16..=31).contains(&v[1]))
        || (v[0] == 100 && (64..=127).contains(&v[1]))
}

fn redact_display(s: &str) -> String {
    let mut out = String::new();
    let bytes = s.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i].is_ascii_hexdigit() {
            let start = i;
            while i < bytes.len() && bytes[i].is_ascii_hexdigit() {
                i += 1;
            }
            if i - start >= 32 {
                out.push_str("[redacted]");
                continue;
            }
            out.push_str(&s[start..i]);
            continue;
        }
        out.push(bytes[i] as char);
        i += 1;
    }
    out.replace("Bearer ", "Bearer ")
}

fn error_text(body: &str) -> String {
    if let Ok(v) = serde_json::from_str::<Value>(body) {
        if let Some(err) = v.get("error").and_then(|e| e.as_str()) {
            return redact_display(err);
        }
    }
    let trimmed = body.trim();
    if trimmed.is_empty() {
        return String::new();
    }
    redact_display(trimmed)
}

fn describe(body: &str) -> String {
    let Ok(v) = serde_json::from_str::<Value>(body) else {
        return "Connected.".into();
    };
    let bind = v.get("bind").and_then(|b| b.as_str()).unwrap_or("");
    let loopback = v.get("loopback").and_then(|b| b.as_bool()).unwrap_or(false);
    let auth = v.get("auth").and_then(|b| b.as_bool()).unwrap_or(false);
    let cred = v.get("credential").and_then(|b| b.as_str()).unwrap_or("");
    let grants = v.get("grants").and_then(|b| b.as_u64()).unwrap_or(0);
    let where_ = if loopback {
        "loopback"
    } else if bind.is_empty() {
        "private bind"
    } else {
        "private or tailnet bind"
    };
    let auth_s = if auth { "auth on" } else { "auth off" };
    let cred_s = match cred {
        "root" => "host token",
        "grant" => "short-lived grant",
        "session" => "paired browser",
        "open" => "open loopback",
        _ => "connected",
    };
    if bind.is_empty() {
        format!("{where_} · {auth_s} · {cred_s} · {grants} grants")
    } else {
        format!("{bind} · {where_} · {auth_s} · {cred_s} · {grants} grants")
    }
}

async fn gateway_fetch(
    method: &str,
    base: &str,
    token: &str,
    path: &str,
    body: Option<Value>,
) -> Result<(u16, String), String> {
    let url = api(base, path);
    let auth = auth_header(token);
    // gloo's json() consumes the builder and returns a Request; send each arm.
    let sent = match method {
        "POST" => {
            gloo_net::http::Request::post(&url)
                .header("Authorization", &auth)
                .json(&body.unwrap_or(Value::Null))
                .map_err(|e| e.to_string())?
                .send()
                .await
        }
        "DELETE" => {
            gloo_net::http::Request::delete(&url)
                .header("Authorization", &auth)
                .send()
                .await
        }
        _ => {
            gloo_net::http::Request::get(&url)
                .header("Authorization", &auth)
                .send()
                .await
        }
    };
    let resp = sent.map_err(|_| "unreachable".to_string())?;
    let status = resp.status();
    let text = resp.text().await.unwrap_or_default();
    Ok((status, text))
}

thread_local! {
    static ACTIVATE: RefCell<Option<Rc<dyn Fn(String, String)>>> = const { RefCell::new(None) };
    static SAVED: RefCell<Vec<Profile>> = RefCell::new(Vec::new());
    static GATEWAY_ROWS: RefCell<String> = RefCell::new(String::new());
    static GATEWAY_SYNCED: std::cell::Cell<bool> = const { std::cell::Cell::new(false) };
}

fn leak_str(text: String) -> &'static str {
    Box::leak(text.into_boxed_str())
}

fn leak_words(fixed: &[&'static str], extra: &str) -> &'static [&'static str] {
    let mut words: Vec<&'static str> = fixed.to_vec();
    if !extra.is_empty() {
        words.push(leak_str(extra.to_string()));
    }
    Box::leak(words.into_boxed_slice())
}

fn activate_gateway(id: &str) -> Result<(), String> {
    let base = if id == "this" {
        String::new()
    } else {
        SAVED.with(|slot| {
            slot.borrow()
                .iter()
                .find(|row| row.id == id)
                .map(|row| row.base.clone())
                .unwrap_or_default()
        })
    };
    let run = ACTIVATE.with(|slot| slot.borrow().clone());
    let Some(run) = run else {
        return Err("Gateways are not ready.".into());
    };
    run(id.to_string(), base);
    Ok(())
}

fn forget_gateway(id: &str) -> Result<(), String> {
    if id == "this" || id.is_empty() {
        return Err("This gateway stays.".into());
    }
    persist_token(id, "");
    let Some(run) = FORGET.with(|slot| slot.borrow().clone()) else {
        return Err("Gateways are not ready.".into());
    };
    run(id.to_string());
    Ok(())
}

thread_local! {
    static FORGET: RefCell<Option<Rc<dyn Fn(String)>>> = const { RefCell::new(None) };
}

fn sync_gateway_rows() {
    let profiles = SAVED.with(|slot| slot.borrow().clone());
    let key = profiles
        .iter()
        .map(|row| format!("{}:{}", row.id, row.name))
        .collect::<Vec<_>>()
        .join("|");
    let changed = GATEWAY_ROWS.with(|slot| {
        if *slot.borrow() == key {
            false
        } else {
            *slot.borrow_mut() = key;
            true
        }
    });
    let first = GATEWAY_SYNCED.with(|slot| {
        let first = !slot.get();
        slot.set(true);
        first
    });
    if !changed && !first {
        return;
    }
    let mut rows = vec![Profile {
        id: "this".into(),
        name: "This gateway".into(),
        base: String::new(),
        ssh: String::new(),
        warn: false,
    }];
    rows.extend(profiles);
    let mut switch = Vec::new();
    let mut forget = Vec::new();
    for row in rows {
        let id = row.id.clone();
        let name = row.name.clone();
        let switch_id = leak_str(format!("gw.switch.{id}"));
        let label = leak_str(format!("Switch to gateway: {name}"));
        let words = leak_words(&["switch", "gateway", "remote"], &name);
        let picked = id.clone();
        switch.push(
            crate::shell::Command::new(
                switch_id,
                crate::shell::Group::Gateway,
                label,
                "Use this gateway in the tab",
                Rc::new(move |_| activate_gateway(&picked)),
            )
            .keywords(words)
            .order(60),
        );
        if id != "this" {
            let forget_id = leak_str(format!("gw.remove.{id}"));
            let label = leak_str(format!("Gateways: Remove {name}"));
            let words = leak_words(&["remove", "delete", "profile", "forget"], &name);
            let picked = id.clone();
            let named = name.clone();
            forget.push(
                crate::shell::Command::new(
                    forget_id,
                    crate::shell::Group::Gateway,
                    label,
                    "Forget this saved gateway",
                    Rc::new(move |_| {
                        let picked = picked.clone();
                        let named = named.clone();
                        if !crate::confirm::ask(
                            crate::confirm::ConfirmRequest {
                                title: format!("Remove {named}?"),
                                body: "This browser forgets the gateway. Its token is dropped from this tab.".into(),
                                confirm_label: "Remove".into(),
                            },
                            Rc::new(move || {
                                let _ = forget_gateway(&picked);
                            }),
                        ) {
                            return Err(crate::confirm::BUSY.into());
                        }
                        Ok(())
                    }),
                )
                .keywords(words)
                .order(70),
            );
        }
    }
    crate::shell::replace_prefixed("gw.switch.", switch);
    crate::shell::replace_prefixed("gw.remove.", forget);
}

#[component]
pub fn GatewaySwitcher(
    inline: bool,
    primary: bool,
    base: RwSignal<String>,
    token: RwSignal<String>,
    gateway: RwSignal<String>,
    detail: RwSignal<String>,
    open: RwSignal<bool>,
    profiles: RwSignal<Vec<Profile>>,
    selected: RwSignal<String>,
    refresh: impl Fn() + Copy + Send + Sync + 'static,
) -> impl IntoView {
    let name = RwSignal::new(String::new());
    let url = RwSignal::new(String::new());
    let ssh = RwSignal::new(String::new());
    let form_note = RwSignal::new(String::new());
    let cursor = RwSignal::new(0usize);
    // Bumps on every probe so a slow response cannot overwrite a newer one.
    let probe_gen = RwSignal::new(0u64);

    let probe = move || {
        let gen = probe_gen.get_untracked().wrapping_add(1);
        probe_gen.set(gen);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let probe_gen = probe_gen;
        spawn_local(async move {
            let current = move || probe_gen.get_untracked() == gen;
            let health = gateway_fetch("GET", &b, &t, "/health", None).await;
            if !current() {
                return;
            }
            match health {
                Err(_) => {
                    gateway.set("unreachable".into());
                    detail.set("Could not reach this daemon. Reconnect tries again.".into());
                    return;
                }
                Ok((code, _)) if code != 200 => {
                    gateway.set("unreachable".into());
                    detail.set(format!("Health returned HTTP {code}."));
                    return;
                }
                Ok(_) => {}
            }
            let info = gateway_fetch("GET", &b, &t, "/v1/gateway", None).await;
            if !current() {
                return;
            }
            match info {
                Err(_) => {
                    gateway.set("unreachable".into());
                    detail.set("Health answered, then the gateway check failed.".into());
                }
                Ok((401, _)) | Ok((403, _)) => {
                    gateway.set("unauthorized".into());
                    detail.set("This browser is not authenticated. Paste a grant, or use strike connect so the token stays out of the URL.".into());
                }
                Ok((200, body)) => {
                    gateway.set("connected".into());
                    detail.set(describe(&body));
                }
                Ok((code, body)) => {
                    gateway.set("unauthorized".into());
                    detail.set(redact_display(&format!(
                        "Gateway check returned HTTP {code} {}",
                        error_text(&body)
                    )));
                }
            }
        });
    };

    if primary {
        // The shell palette command dispatches here (docs/webui-shell.md).
        // The binding captures only the host-owned `open` signal, which
        // outlives this component, so it stays correct across remounts
        // and is deliberately not unbound on cleanup.
        crate::shell::bind(
            "gateways.open",
            Rc::new(move |_args: &str| {
                open.update(|v| *v = !*v);
                Ok(())
            }),
        );
        probe();

        // The shell remounts top-bar slots (a breakpoint flip disposes
        // and recreates this component), so everything registered on
        // the window below — the 5s probe timer and the Alt+W listener
        // — is released on cleanup, the same pattern as notify.rs.
        // Leaked copies are not inert: each leaked timer panics on its
        // disposed signals every tick, and each leaked Alt+W listener
        // toggles the shared `open` signal, so a few remounts leave the
        // shortcut firing an even number of toggles and looking dead
        // (#1527).
        let disposers: Rc<RefCell<Vec<Box<dyn FnOnce()>>>> = Rc::new(RefCell::new(Vec::new()));
        let add_disposer = {
            let disposers = disposers.clone();
            move |f: Box<dyn FnOnce()>| disposers.borrow_mut().push(f)
        };
        // on_cleanup wants Send + Sync; the disposers are window-thread
        // state, so they ride in a SendWrapper (the same shape notify.rs
        // uses) and are only ever run on this thread.
        on_cleanup({
            let disposers = disposers.clone();
            let run = send_wrapper::SendWrapper::new(move || {
                for f in disposers.borrow_mut().drain(..) {
                    f();
                }
            });
            move || run.take()()
        });

        let interval = Closure::<dyn FnMut()>::new(move || probe());
        if let Some(w) = web_sys::window() {
            let registered = w.set_interval_with_callback_and_timeout_and_arguments_0(
                interval.as_ref().unchecked_ref(),
                5000,
            );
            if let Ok(id) = registered {
                let kept = Rc::new(RefCell::new(Some(interval)));
                let drop_cb = kept.clone();
                add_disposer(Box::new(move || {
                    if let Some(w) = web_sys::window() {
                        w.clear_interval_with_handle(id);
                    }
                    drop(drop_cb.borrow_mut().take());
                }));
            }
            // If registration failed the closure drops here and never fires.
        }

        let on_key =
            Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
                if ev.alt_key()
                    && !ev.ctrl_key()
                    && !ev.meta_key()
                    && ev.key().eq_ignore_ascii_case("w")
                {
                    ev.prevent_default();
                    open.update(|v| *v = !*v);
                }
            });
        if let Some(w) = web_sys::window() {
            let _ = w.add_event_listener_with_callback("keydown", on_key.as_ref().unchecked_ref());
        }
        let kept = Rc::new(RefCell::new(Some(on_key)));
        let drop_cb = kept.clone();
        add_disposer(Box::new(move || {
            if let Some(cb) = drop_cb.borrow_mut().take() {
                if let Some(w) = web_sys::window() {
                    let _ = w.remove_event_listener_with_callback(
                        "keydown",
                        cb.as_ref().unchecked_ref(),
                    );
                }
                drop(cb);
            }
        }));
    }

    // (generation, id, base). A shared signal so click and keyboard can both
    // select without moving a closure twice.
    let pick = RwSignal::new((0u64, String::new(), String::new()));
    Effect::new(move |_| {
        let (gen, id, profile_base) = pick.get();
        if gen == 0 || id.is_empty() {
            return;
        }
        selected.set(id.clone());
        base.set(profile_base);
        token.set(session_token(&id));
        if id == "this" {
            let _ = local_store().map(|s| s.set_item("strike.base", "").ok());
        } else {
            let _ = local_store().map(|s| s.set_item("strike.base", &base.get_untracked()).ok());
        }
        forget_persisted_host_token();
        refresh();
        probe();
    });
    let choose = move |id: String, profile_base: String| {
        pick.update(|(gen, cur, dest)| {
            *gen = gen.wrapping_add(1);
            *cur = id;
            *dest = profile_base;
        });
    };

    let reconnect = move || {
        gateway.set("reconnecting".into());
        detail.set("Reconnecting…".into());
        refresh();
        probe();
    };

    let rotate = move || {
        let (b, t, id) = (
            base.get_untracked(),
            token.get_untracked(),
            selected.get_untracked(),
        );
        spawn_local(async move {
            match gateway_fetch("POST", &b, &t, "/v1/gateway/rotate", Some(json!({}))).await {
                Ok((200, body)) => {
                    let tok = serde_json::from_str::<Value>(&body)
                        .ok()
                        .and_then(|v| v.get("token").and_then(|t| t.as_str()).map(|s| s.to_string()))
                        .unwrap_or_default();
                    if tok.is_empty() {
                        detail.set("Rotate succeeded but returned no token.".into());
                        return;
                    }
                    token.set(tok.clone());
                    persist_token(&id, &tok);
                    detail.set("Rotated. The new token is in the password field and is not shown in clear text.".into());
                    probe();
                }
                Ok((403, _)) => detail.set("A short-lived grant cannot rotate the host token. On the host, run strike daemon logout.".into()),
                Ok((409, _)) => detail.set("This daemon was not started with --token-file, so the browser cannot rotate it.".into()),
                Ok((code, body)) => detail.set(redact_display(&format!("Rotate failed: HTTP {code} {}", error_text(&body)))),
                Err(_) => detail.set("Could not reach the daemon to rotate.".into()),
            }
        });
    };

    let revoke = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match gateway_fetch("DELETE", &b, &t, "/v1/gateway/grants", None).await {
                Ok((200, _)) => {
                    detail.set(
                        "Revoked short-lived grants. The host token file was not changed.".into(),
                    );
                    probe();
                }
                Ok((403, _)) => detail.set(
                    "Revoking every grant needs the host token, not a short-lived grant.".into(),
                ),
                Ok((code, body)) => detail.set(redact_display(&format!(
                    "Revoke failed: HTTP {code} {}",
                    error_text(&body)
                ))),
                Err(_) => detail.set("Could not reach the daemon to revoke grants.".into()),
            }
        });
    };
    {
        let rotate = rotate;
        let revoke = revoke;
        crate::shell::bind(
            "gateway.rotate",
            Rc::new(move |_| {
                rotate();
                Ok(())
            }),
        );
        crate::shell::bind(
            "gateway.revoke",
            Rc::new(move |_| {
                revoke();
                Ok(())
            }),
        );
        crate::shell::bind(
            "gateway.reconnect",
            Rc::new(move |_| {
                reconnect();
                Ok(())
            }),
        );
    }

    let logout_browser = move || {
        let (b, t, id) = (base.get(), token.get(), selected.get());
        spawn_local(async move {
            let result = gateway_fetch("POST", &b, &t, "/v1/gateway/logout", Some(json!({}))).await;
            match outcome::logout_outcome(result) {
                Ok(message) => {
                    token.set(String::new());
                    persist_token(&id, "");
                    forget_persisted_host_token();
                    detail.set(message.into());
                    gateway.set("unauthorized".into());
                }
                Err(message) => detail.set(message),
            }
        });
    };
    crate::shell::bind(
        "gateway.logout",
        Rc::new(move |_| {
            logout_browser();
            Ok(())
        }),
    );
    crate::shell::bind(
        "gateway.add",
        Rc::new(move |_| {
            open.set(true);
            Ok(())
        }),
    );

    let add_profile = move |_| {
        let (n, u, s) = (name.get(), url.get(), ssh.get());
        if n.trim().is_empty() {
            form_note.set("Name the gateway.".into());
            return;
        }
        if s.to_ascii_lowercase().contains("token=") {
            form_note.set("The SSH target must not contain a token.".into());
            return;
        }
        match classify_base(&u) {
            Err(e) => form_note.set(e),
            Ok(warn) => {
                let id = format!("g{}", js_sys::Date::now() as u64);
                profiles.update(|list| {
                    list.push(Profile {
                        id,
                        name: n.trim().to_string(),
                        base: u.trim().to_string(),
                        ssh: s.trim().to_string(),
                        warn,
                    });
                });
                save_profiles(&profiles.get());
                name.set(String::new());
                url.set(String::new());
                ssh.set(String::new());
                form_note.set(if warn {
                    "Saved. This hostname is not a literal private address. The bearer is sent there only when you select it.".into()
                } else {
                    "Saved. The token is kept for this tab only.".into()
                });
            }
        }
    };

    let remove_profile = move |id: String| {
        // The token leaves with the profile (#1562). Without this the
        // removed gateway's credential stays in the tab's token map for
        // the tab's lifetime, and a re-added profile whose derived ID
        // matches the old one silently resurrects it instead of prompting.
        persist_token(&id, "");
        profiles.update(|list| list.retain(|p| p.id != id));
        save_profiles(&profiles.get());
        if selected.get_untracked() == id {
            selected.set("this".into());
            base.set(String::new());
        }
    };
    {
        let pick = pick;
        ACTIVATE.with(|slot| {
            *slot.borrow_mut() = Some(Rc::new(move |id, profile_base| {
                pick.update(|(gen, cur, dest)| {
                    *gen = gen.wrapping_add(1);
                    *cur = id;
                    *dest = profile_base;
                });
            }));
        });
        FORGET.with(|slot| *slot.borrow_mut() = Some(Rc::new(remove_profile)));
    }
    Effect::new(move |_| {
        SAVED.with(|slot| *slot.borrow_mut() = profiles.get());
        sync_gateway_rows();
    });

    let on_token = move |ev| {
        let value = event_target_value(&ev);
        token.set(value.clone());
        persist_token(&selected.get_untracked(), &value);
        forget_persisted_host_token();
    };

    let panel = move || {
        let _open = open.get();
        view! {
            <div class="gw-head">
                <h2 class="panel-title">"Gateways"</h2>
                <span class="gw-head-end">
                    {(!inline).then(|| view! {
                        <button class="secondary" on:click=move |_| open.set(false)>"Close"</button>
                    })}
                </span>
            </div>
            <p class="hint gw-detail" aria-live="polite">{move || detail.get()}</p>
            <label>"Token for this gateway"</label>
            <input type="password" autocomplete="off" spellcheck="false" aria-label="Gateway token"
                prop:value=move || token.get()
                on:input=on_token />
            <p class="hint">"The token stays in this tab. strike connect injects a short-lived grant on the machine with the browser, so the address bar never contains it."</p>
            <div class="row">
                <input placeholder="name" aria-label="Gateway name" prop:value=move || name.get()
                    on:input=move |ev| name.set(event_target_value(&ev)) />
                <input placeholder="http://127.0.0.1:8787" aria-label="Gateway base URL" prop:value=move || url.get()
                    on:input=move |ev| url.set(event_target_value(&ev)) />
            </div>
            <div class="row">
                <input placeholder="user@host" aria-label="SSH destination" prop:value=move || ssh.get()
                    on:input=move |ev| ssh.set(event_target_value(&ev)) />
                <button class="secondary" on:click=add_profile>"Save"</button>
            </div>
            <p class="hint">{move || form_note.get()}</p>
        }
    };

    let on_panel_key = move |ev: web_sys::KeyboardEvent| {
        let n = profiles.get_untracked().len() + 1;
        match ev.key().as_str() {
            "Escape" => {
                ev.prevent_default();
                // The popover consumed this Escape; keep the window
                // handlers from also dismissing what sits behind it.
                ev.stop_propagation();
                open.set(false);
            }
            "ArrowDown" => {
                ev.prevent_default();
                cursor.update(|c| *c = (*c + 1).min(n.saturating_sub(1)));
            }
            "ArrowUp" => {
                ev.prevent_default();
                cursor.update(|c| *c = c.saturating_sub(1));
            }
            "Enter" => {
                ev.prevent_default();
                let rows = {
                    let mut rows = vec![("this".to_string(), String::new())];
                    for p in profiles.get_untracked() {
                        rows.push((p.id, p.base));
                    }
                    rows
                };
                let idx = cursor.get_untracked().min(rows.len().saturating_sub(1));
                if let Some((id, b)) = rows.get(idx) {
                    choose(id.clone(), b.clone());
                }
            }
            _ => {}
        }
    };

    let wrap = if inline { "card gw-panel" } else { "gw-anchor" };
    let body_class = if inline { "gw-body" } else { "gw-pop" };
    let body_role = if inline { "region" } else { "dialog" };
    view! {
        <div class=wrap data-gateway-host="true" data-open=move || if open.get() { "1" } else { "0" }>
            <Show when=move || !inline && open.get()>
                <div class="gw-back" aria-hidden="true" on:click=move |_| open.set(false)></div>
            </Show>
            <Show when=move || inline || open.get()>
                <div class=body_class
                    role=body_role
                    aria-label="Gateways"
                    tabindex="0"
                    on:keydown=on_panel_key>
                    {move || panel()}
                </div>
            </Show>
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn empty_token_deletes_only_that_profiles_entry() {
        // The #1562 delete path: removing one profile's token must not
        // disturb any other profile's credential.
        let mut map = HashMap::new();
        map.insert("g1".to_string(), "dummy-token-one".to_string());
        map.insert("g2".to_string(), "dummy-token-two".to_string());
        let map = updated_token_map(map, "g1", "");
        assert!(!map.contains_key("g1"));
        assert_eq!(map.get("g2").map(String::as_str), Some("dummy-token-two"));
    }

    #[test]
    fn non_empty_token_stores_and_overwrites() {
        let map = updated_token_map(HashMap::new(), "g1", "dummy-token");
        assert_eq!(map.get("g1").map(String::as_str), Some("dummy-token"));
        let map = updated_token_map(map, "g1", "dummy-rotated");
        assert_eq!(map.get("g1").map(String::as_str), Some("dummy-rotated"));
    }

    #[test]
    fn deleting_a_token_that_was_never_stored_is_a_no_op() {
        let map = updated_token_map(HashMap::new(), "gone", "");
        assert!(map.is_empty());
    }
}
