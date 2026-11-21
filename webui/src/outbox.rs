//! Offline outbox for the composer (#1325).
//!
//! When the gateway is unreachable a text send is queued here instead of
//! being lost. The queue lives in `localStorage` (scoped per gateway), so it
//! survives a tab close or a PWA restart. On reconnect items flush in the
//! order they were written, one at a time, before any new input for the same
//! session. Every send carries a client message id
//! (`X-Strike-Client-Message-Id`); the daemon remembers delivered ids, so a
//! retry after a half-open connection cannot post the same prompt twice.
//!
//! The outbox changes when a message arrives, never what it means: the op
//! (`user.input` or `steer`) is the one chosen at send time (#1268).
//! Approvals never go through here; they must be answered live (#1266).
//! Attachments are not queued (they need an upload while online).

use gloo_net::http::Request;
use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::{Deserialize, Serialize};

use crate::api::{api, auth_header, js_encode};

const OUTBOX_PREFIX: &str = "strike.outbox";
/// Header the daemon dedupes on (see `internal/frontend/server/clientmsg.go`).
pub const CLIENT_MESSAGE_HEADER: &str = "X-Strike-Client-Message-Id";
/// Items older than this are held for confirmation instead of auto-sent.
pub const STALE_MS: f64 = 24.0 * 60.0 * 60.0 * 1000.0;
/// Hard cap so a stuck tab cannot grow storage without bound.
pub const MAX_ITEMS: usize = 50;

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
pub struct OutboxItem {
    /// Client message id; also the dedupe key on the daemon.
    pub id: String,
    pub root: String,
    pub op: String,
    pub text: String,
    #[serde(rename = "createdMs")]
    pub created_ms: f64,
    /// Last delivery error that was not a lost connection. Blocks the
    /// session's later items until the user retries or deletes this one.
    #[serde(default)]
    pub error: String,
    /// User confirmed a stale item should still be sent.
    #[serde(default)]
    pub confirmed: bool,
}

impl OutboxItem {
    pub fn is_stale(&self, now: f64) -> bool {
        !self.confirmed && now - self.created_ms > STALE_MS
    }
}

/// What a failed POST means for the outbox.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum SendFailure {
    /// No HTTP answer (offline, DNS, reset) or a retryable status: keep the
    /// item queued and try again on the next reconnect.
    Transient,
    /// The daemon answered and refused: stop and show it.
    Refused(String),
}

/// Classify an error string from `post_op_with_id`. gloo returns transport
/// failures as plain text; HTTP failures are formatted `HTTP <status> ...`.
pub fn classify_failure(err: &str) -> SendFailure {
    let Some(rest) = err.strip_prefix("HTTP ") else {
        return SendFailure::Transient;
    };
    let code: u16 = rest
        .split_whitespace()
        .next()
        .and_then(|c| c.parse().ok())
        .unwrap_or(0);
    match code {
        // 409 in flight: the same id is mid-delivery on the daemon.
        // 502/503/504: a proxy or the daemon is coming back up.
        // 429: admission rate limit.
        0 | 409 | 429 | 502 | 503 | 504 => SendFailure::Transient,
        _ => SendFailure::Refused(err.to_string()),
    }
}

/// True when a send for `root` must go through the outbox: the gateway is
/// down, or earlier items for the session are still waiting (order).
pub fn should_queue(gateway: &str, items: &[OutboxItem], root: &str) -> bool {
    gateway == "unreachable" || items.iter().any(|i| i.root == root)
}

/// Next item to deliver: the oldest queued item whose session has no
/// earlier stale or failed item ahead of it.
pub fn next_ready(items: &[OutboxItem], now: f64) -> Option<OutboxItem> {
    let mut sorted: Vec<&OutboxItem> = items.iter().collect();
    sorted.sort_by(|a, b| {
        a.created_ms
            .partial_cmp(&b.created_ms)
            .unwrap_or(std::cmp::Ordering::Equal)
            .then(a.id.cmp(&b.id))
    });
    let mut blocked: Vec<&str> = Vec::new();
    for item in sorted {
        if blocked.contains(&item.root.as_str()) {
            continue;
        }
        if !item.error.is_empty() || item.is_stale(now) {
            blocked.push(item.root.as_str());
            continue;
        }
        return Some(item.clone());
    }
    None
}

/// Add an item, keeping the cap. Returns false (and changes nothing) when
/// the outbox is full or the id is already queued.
pub fn push_item(items: &mut Vec<OutboxItem>, item: OutboxItem) -> bool {
    if items.len() >= MAX_ITEMS || items.iter().any(|i| i.id == item.id) {
        return false;
    }
    items.push(item);
    true
}

pub fn remove_item(items: &mut Vec<OutboxItem>, id: &str) {
    items.retain(|i| i.id != id);
}

/// Edit text before it sends. An edited item keeps its id: it has never
/// been delivered, so the daemon has no record of it.
pub fn edit_item(items: &mut [OutboxItem], id: &str, text: &str) -> bool {
    let text = text.trim();
    if text.is_empty() {
        return false;
    }
    match items.iter_mut().find(|i| i.id == id) {
        Some(item) => {
            item.text = text.to_string();
            item.error.clear();
            true
        }
        None => false,
    }
}

/// Clear the hold on a failed or stale item so the next flush sends it.
pub fn retry_item(items: &mut [OutboxItem], id: &str) {
    if let Some(item) = items.iter_mut().find(|i| i.id == id) {
        item.error.clear();
        item.confirmed = true;
    }
}

pub fn items_for<'a>(items: &'a [OutboxItem], root: &str) -> Vec<&'a OutboxItem> {
    let mut out: Vec<&OutboxItem> = items.iter().filter(|i| i.root == root).collect();
    out.sort_by(|a, b| {
        a.created_ms
            .partial_cmp(&b.created_ms)
            .unwrap_or(std::cmp::Ordering::Equal)
    });
    out
}

/// Build a client message id from a timestamp and a random fraction. Only
/// `[a-z0-9-]`, which the daemon's id check accepts. Uniqueness is per
/// browser; it is a dedupe key, not a secret.
pub fn make_client_id(now: f64, random: f64) -> String {
    let r = (random.clamp(0.0, 1.0) * 1e15) as u64;
    format!("cm-{:x}-{:x}", now.max(0.0) as u64, r)
}

pub fn new_client_id() -> String {
    make_client_id(js_sys::Date::now(), js_sys::Math::random())
}

/// Parse stored items, dropping anything malformed.
pub fn parse_stored(raw: &str) -> Vec<OutboxItem> {
    let mut items: Vec<OutboxItem> = serde_json::from_str(raw).unwrap_or_default();
    items.retain(|i| !i.id.is_empty() && !i.root.is_empty() && !i.text.trim().is_empty());
    items.truncate(MAX_ITEMS);
    items
}

fn storage_key(base: &str) -> String {
    crate::notify::scoped_storage_key(OUTBOX_PREFIX, base)
}

fn load_items(base: &str) -> Vec<OutboxItem> {
    let raw = web_sys::window()
        .and_then(|w| w.local_storage().ok().flatten())
        .and_then(|s| s.get_item(&storage_key(base)).ok().flatten())
        .unwrap_or_default();
    parse_stored(&raw)
}

fn save_items(base: &str, items: &[OutboxItem]) {
    if let Some(s) = web_sys::window().and_then(|w| w.local_storage().ok().flatten()) {
        let key = storage_key(base);
        if items.is_empty() {
            let _ = s.remove_item(&key);
        } else {
            let _ = s.set_item(
                &key,
                &serde_json::to_string(items).unwrap_or_else(|_| "[]".into()),
            );
        }
    }
}

/// Submit one op with a client message id. Same shape as `api::post_op`.
pub async fn post_op_with_id(
    base: &str,
    token: &str,
    root: &str,
    typ: &str,
    data: serde_json::Value,
    client_id: &str,
) -> Result<(), String> {
    let path = if root.is_empty() {
        "/v1/ops".to_string()
    } else {
        format!("/v1/ops?root={}", js_encode(root))
    };
    let mut req = Request::post(&api(base, &path))
        .credentials(web_sys::RequestCredentials::Include)
        .header(CLIENT_MESSAGE_HEADER, client_id);
    let token = token.trim();
    if !token.is_empty() {
        req = req.header("Authorization", &auth_header(token));
    }
    let resp = req
        .json(&serde_json::json!({"type": typ, "data": data}))
        .map_err(|e| e.to_string())?
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = resp.status();
    if !(200..300).contains(&status) {
        let text = resp.text().await.unwrap_or_default();
        return Err(format!("HTTP {status} {text}"));
    }
    Ok(())
}

/// Handle to the outbox signals. Copy, so closures can capture it freely.
#[derive(Clone, Copy)]
pub struct Outbox {
    pub items: RwSignal<Vec<OutboxItem>>,
    flushing: RwSignal<bool>,
    base: RwSignal<String>,
    token: RwSignal<String>,
}

impl Outbox {
    pub fn new(base: RwSignal<String>, token: RwSignal<String>) -> Self {
        let items = RwSignal::new(load_items(&base.get_untracked()));
        // Switching gateways swaps in that gateway's queue.
        Effect::new(move |prev: Option<String>| {
            let b = base.get();
            if prev.is_some_and(|p| p != b) {
                items.set(load_items(&b));
            }
            b
        });
        Self {
            items,
            flushing: RwSignal::new(false),
            base,
            token,
        }
    }

    fn update(&self, f: impl FnOnce(&mut Vec<OutboxItem>)) {
        let mut next = self.items.get_untracked();
        f(&mut next);
        save_items(&self.base.get_untracked(), &next);
        self.items.set(next);
    }

    /// Queue a text send. Returns false when the outbox is full.
    pub fn enqueue(&self, id: String, root: String, op: &str, text: String) -> bool {
        let mut ok = false;
        let item = OutboxItem {
            id,
            root,
            op: op.to_string(),
            text,
            created_ms: js_sys::Date::now(),
            error: String::new(),
            confirmed: false,
        };
        self.update(|items| ok = push_item(items, item));
        ok
    }

    pub fn remove(&self, id: &str) {
        self.update(|items| remove_item(items, id));
    }

    pub fn edit(&self, id: &str, text: &str) -> bool {
        let mut ok = false;
        self.update(|items| ok = edit_item(items, id, text));
        ok
    }

    pub fn retry(&self, id: &str) {
        self.update(|items| retry_item(items, id));
        self.kick();
    }

    /// Deliver queued items in order. One flush at a time; stops at the
    /// first failure so later items never overtake an earlier one.
    pub fn kick(&self) {
        if self.flushing.get_untracked() || self.items.get_untracked().is_empty() {
            return;
        }
        self.flushing.set(true);
        let this = *self;
        let started = self.base.get_untracked();
        spawn_local(async move {
            loop {
                if this.base.get_untracked() != started {
                    break;
                }
                let Some(item) = next_ready(&this.items.get_untracked(), js_sys::Date::now())
                else {
                    break;
                };
                let token = this.token.get_untracked();
                let result = post_op_with_id(
                    &started,
                    &token,
                    &item.root,
                    &item.op,
                    serde_json::json!({"text": item.text}),
                    &item.id,
                )
                .await;
                if this.base.get_untracked() != started {
                    break;
                }
                match result {
                    Ok(()) => this.remove(&item.id),
                    Err(e) => {
                        if let SendFailure::Refused(msg) = classify_failure(&e) {
                            this.update(|items| {
                                if let Some(it) = items.iter_mut().find(|i| i.id == item.id) {
                                    it.error = msg;
                                }
                            });
                        }
                        break;
                    }
                }
            }
            this.flushing.set(false);
        });
    }
}

/// Queued-while-offline list for the active session, shown above the
/// composer next to the engine queue.
#[component]
pub fn OutboxList(
    outbox: Outbox,
    #[prop(into)] root: Signal<String>,
    gateway: RwSignal<String>,
) -> impl IntoView {
    let editing = RwSignal::new(String::new());
    let edit_text = RwSignal::new(String::new());
    view! {
        <Show when=move || !items_for(&outbox.items.get(), &root.get()).is_empty()>
            <div class="outbox" role="region" aria-label="Messages waiting for the gateway">
                <p class="outbox-head" role="status">
                    {move || if gateway.get() == "unreachable" {
                        "Queued — will send when reconnected."
                    } else {
                        "Sending queued messages in order…"
                    }}
                </p>
                <ol class="outbox-list">
                    {move || {
                        let now = js_sys::Date::now();
                        let list: Vec<OutboxItem> = items_for(&outbox.items.get(), &root.get())
                            .into_iter()
                            .cloned()
                            .collect();
                        list.into_iter().map(|item| {
                            let id = item.id.clone();
                            let stale = item.is_stale(now);
                            let failed = item.error.clone();
                            let state = if !failed.is_empty() {
                                format!("Not sent: {failed}")
                            } else if stale {
                                "Held: queued over a day ago. Send or delete it.".to_string()
                            } else if item.op == "steer" {
                                "Queued steer".to_string()
                            } else {
                                "Queued".to_string()
                            };
                            let held = stale || !failed.is_empty();
                            let id_edit = id.clone();
                            let id_save = id.clone();
                            let id_save_when = id.clone();
                            let id_key = id.clone();
                            let id_retry = id.clone();
                            let id_delete = id.clone();
                            let id_when = id.clone();
                            let text = item.text.clone();
                            let text_edit = item.text.clone();
                            view! {
                                <li class="outbox-item" data-outbox-id=id.clone()>
                                    <span class="outbox-state">{state}</span>
                                    <Show when=move || editing.get() == id_when fallback=move || view! { <span class="outbox-text">{text.clone()}</span> }>
                                        <input class="outbox-edit" aria-label="Edit queued message"
                                            prop:value=move || edit_text.get()
                                            on:input=move |ev| edit_text.set(event_target_value(&ev))
                                            on:keydown={
                                                let id_key = id_key.clone();
                                                move |ev: web_sys::KeyboardEvent| {
                                                    if ev.key() == "Enter" {
                                                        ev.prevent_default();
                                                        if outbox.edit(&id_key, &edit_text.get_untracked()) {
                                                            editing.set(String::new());
                                                        }
                                                    } else if ev.key() == "Escape" {
                                                        ev.prevent_default();
                                                        ev.stop_propagation();
                                                        editing.set(String::new());
                                                    }
                                                }
                                            } />
                                    </Show>
                                    <span class="outbox-actions">
                                        <Show when=move || editing.get() == id_save_when fallback={
                                            let id_edit = id_edit.clone();
                                            let text_edit = text_edit.clone();
                                            move || {
                                                let id_edit = id_edit.clone();
                                                let text_edit = text_edit.clone();
                                                view! { <button type="button" class="secondary" on:click=move |_| { editing.set(id_edit.clone()); edit_text.set(text_edit.clone()); }>"Edit"</button> }
                                            }
                                        }>
                                            {
                                                let id_save = id_save.clone();
                                                view! { <button type="button" class="secondary" on:click=move |_| {
                                                    if outbox.edit(&id_save, &edit_text.get_untracked()) {
                                                        editing.set(String::new());
                                                    }
                                                }>"Save"</button> }
                                            }
                                        </Show>
                                        <Show when=move || held>
                                            {
                                                let id_retry = id_retry.clone();
                                                view! { <button type="button" class="secondary" on:click=move |_| outbox.retry(&id_retry)>"Send"</button> }
                                            }
                                        </Show>
                                        <button type="button" class="secondary" title="Delete this queued message. It is not sent."
                                            on:click=move |_| outbox.remove(&id_delete)>"Delete"</button>
                                    </span>
                                </li>
                            }
                        }).collect_view()
                    }}
                </ol>
            </div>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn item(id: &str, root: &str, at: f64) -> OutboxItem {
        OutboxItem {
            id: id.into(),
            root: root.into(),
            op: "user.input".into(),
            text: format!("text {id}"),
            created_ms: at,
            error: String::new(),
            confirmed: false,
        }
    }

    #[test]
    fn flush_order_is_creation_order() {
        let items = vec![item("b", "r1", 20.0), item("a", "r1", 10.0), item("c", "r2", 15.0)];
        assert_eq!(next_ready(&items, 30.0).unwrap().id, "a");
        let rest: Vec<OutboxItem> = items.into_iter().filter(|i| i.id != "a").collect();
        assert_eq!(next_ready(&rest, 30.0).unwrap().id, "c");
    }

    #[test]
    fn failed_or_stale_item_blocks_only_its_session() {
        let mut failed = item("a", "r1", 10.0);
        failed.error = "HTTP 400 bad".into();
        let items = vec![failed, item("b", "r1", 20.0), item("c", "r2", 30.0)];
        // r1's later item must not overtake the failed one; r2 still flows.
        assert_eq!(next_ready(&items, 40.0).unwrap().id, "c");

        let old = item("s", "r1", 0.0);
        let now = STALE_MS + 1.0;
        let items = vec![old.clone(), item("t", "r1", now)];
        assert!(next_ready(&items, now).is_none());
        let mut confirmed = old;
        retry_item(std::slice::from_mut(&mut confirmed), "s");
        assert_eq!(next_ready(&[confirmed], now).unwrap().id, "s");
    }

    #[test]
    fn classify_failure_table() {
        let cases = [
            ("TypeError: Failed to fetch", SendFailure::Transient),
            ("HTTP 409 message already in flight", SendFailure::Transient),
            ("HTTP 503 live session unavailable", SendFailure::Transient),
            ("HTTP 429 slow down", SendFailure::Transient),
            ("HTTP 400 invalid op", SendFailure::Refused("HTTP 400 invalid op".into())),
            ("HTTP 403 read only", SendFailure::Refused("HTTP 403 read only".into())),
        ];
        for (err, want) in cases {
            assert_eq!(classify_failure(err), want, "{err}");
        }
    }

    #[test]
    fn should_queue_when_offline_or_behind_earlier_items() {
        let items = vec![item("a", "r1", 1.0)];
        assert!(should_queue("unreachable", &[], "r1"));
        assert!(should_queue("connected", &items, "r1"));
        assert!(!should_queue("connected", &items, "r2"));
        assert!(!should_queue("connected", &[], "r1"));
    }

    #[test]
    fn push_edit_remove_and_cap() {
        let mut items = Vec::new();
        assert!(push_item(&mut items, item("a", "r1", 1.0)));
        assert!(!push_item(&mut items, item("a", "r1", 2.0)), "duplicate id");
        assert!(edit_item(&mut items, "a", "  new text "));
        assert_eq!(items[0].text, "new text");
        assert!(!edit_item(&mut items, "a", "   "), "empty edit refused");
        assert!(!edit_item(&mut items, "zzz", "x"));
        remove_item(&mut items, "a");
        assert!(items.is_empty());
        for i in 0..MAX_ITEMS {
            assert!(push_item(&mut items, item(&format!("i{i}"), "r1", i as f64)));
        }
        assert!(!push_item(&mut items, item("over", "r1", 99.0)));
    }

    #[test]
    fn stored_items_round_trip_and_drop_garbage() {
        let items = vec![item("a", "r1", 1.0)];
        let raw = serde_json::to_string(&items).unwrap();
        assert_eq!(parse_stored(&raw), items);
        assert!(parse_stored("not json").is_empty());
        assert!(parse_stored(r#"[{"id":"","root":"r","op":"user.input","text":"x","createdMs":1}]"#).is_empty());
    }

    #[test]
    fn client_ids_are_daemon_safe() {
        let id = make_client_id(1_700_000_000_000.0, 0.5);
        assert!(id.starts_with("cm-"));
        assert!(id.chars().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '-'));
        assert_ne!(id, make_client_id(1_700_000_000_000.0, 0.25));
        assert!(id.len() <= 128);
    }
}
