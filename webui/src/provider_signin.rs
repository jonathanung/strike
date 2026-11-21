//! Device-code (#1533) and paste-assisted OAuth (#1538) sign-in on a
//! provider card. A card offers only the methods the daemon lists in its
//! `methods`. The daemon runs each flow and hands the browser only the
//! code, the link, the status, and redacted messages. Flows live in page
//! memory: nothing here touches localStorage, and the OAuth paste goes to
//! the complete endpoint and nowhere else.
//!
//! State and copy live in the `signin-flow` crate, which CI tests; this
//! module is the HTTP glue and the view.

use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde_json::Value;
use signin_flow::{
    complete_path, flow_path, paste_error, poll_error_is_final, safe_link, signin_methods, Answer,
    Flow, Method, Phase, POLL_MS,
};

use crate::api::{delete_json, get_text, post_json_value};
use crate::providers::error_message;

/// Sign-in state for one Providers page, shared by its cards. Keyed by
/// provider name, so a providers reload (which re-renders every card)
/// keeps a pending flow and its poll.
#[derive(Clone)]
pub struct Signin {
    base: RwSignal<String>,
    token: RwSignal<String>,
    notes: RwSignal<HashMap<String, String>>,
    bump: RwSignal<u32>,
    flows: RwSignal<HashMap<String, Flow>>,
    pastes: RwSignal<HashMap<String, String>>,
    /// Providers with a start, cancel, or paste request in flight.
    busy: RwSignal<Vec<String>>,
    /// Cleared when the page unmounts; every queued poll checks it first.
    alive: Arc<AtomicBool>,
}

fn answer_from(value: &Value) -> Answer {
    let field = |key: &str| {
        value
            .get(key)
            .and_then(|v| v.as_str())
            .unwrap_or_default()
            .to_string()
    };
    Answer {
        id: field("id"),
        status: field("status"),
        message: field("message"),
        user_code: field("userCode"),
        verification_uri: field("verificationUri"),
        authorize_url: field("authorizeUrl"),
        expires_at: field("expiresAt"),
    }
}

/// "Expires at 22:41." in the browser's local time, or None when the
/// daemon's timestamp does not parse.
fn expires_label(rfc3339: &str) -> Option<String> {
    if rfc3339.trim().is_empty() {
        return None;
    }
    let date = js_sys::Date::new(&wasm_bindgen::JsValue::from_str(rfc3339));
    if date.get_time().is_nan() {
        return None;
    }
    Some(format!(
        "Expires at {:02}:{:02}.",
        date.get_hours(),
        date.get_minutes()
    ))
}

impl Signin {
    pub fn new(
        base: RwSignal<String>,
        token: RwSignal<String>,
        notes: RwSignal<HashMap<String, String>>,
        bump: RwSignal<u32>,
    ) -> Self {
        let flows = RwSignal::new(HashMap::new());
        let pastes = RwSignal::new(HashMap::new());
        // A flow belongs to the gateway that started it. Switching gateways
        // drops every flow (which also ends its poll) instead of polling the
        // new gateway for an id it never issued.
        Effect::new(move |prev: Option<String>| {
            let now = base.get();
            if prev.is_some_and(|prev| prev != now) {
                flows.set(HashMap::new());
                pastes.set(HashMap::new());
            }
            now
        });
        Signin {
            base,
            token,
            notes,
            bump,
            flows,
            pastes,
            busy: RwSignal::new(Vec::new()),
            alive: Arc::new(AtomicBool::new(true)),
        }
    }

    /// Stop every poll. The page calls this on unmount, so leaving
    /// Providers leaves no browser poll behind. The daemon keeps the flow
    /// until it completes or expires (15 minutes), so an approval the user
    /// already gave still lands, and Providers shows it on return.
    pub fn stop(&self) {
        self.alive.store(false, Ordering::Relaxed);
    }

    fn live(&self) -> bool {
        self.alive.load(Ordering::Relaxed)
    }

    fn endpoint(&self) -> Option<(String, String)> {
        Some((self.base.try_get_untracked()?, self.token.try_get_untracked()?))
    }

    fn note(&self, provider: &str, text: String) {
        if self.live() {
            self.notes.try_update(|map| {
                map.insert(provider.to_string(), text);
            });
        }
    }

    fn reload(&self) {
        if self.live() {
            self.bump.try_update(|n| *n = n.wrapping_add(1));
        }
    }

    fn set_busy(&self, provider: &str, on: bool) {
        if !self.live() {
            return;
        }
        self.busy.try_update(|list| {
            list.retain(|name| name != provider);
            if on {
                list.push(provider.to_string());
            }
        });
    }

    fn is_busy(&self, provider: &str) -> bool {
        self.busy
            .try_with_untracked(|list| list.iter().any(|name| name == provider))
            .unwrap_or(false)
    }

    fn flow(&self, provider: &str) -> Option<Flow> {
        self.flows
            .try_with_untracked(|map| map.get(provider).cloned())
            .flatten()
    }

    /// True while `id` is still this provider's pending flow on a mounted
    /// page. A cancel, a newer flow, a terminal answer, or an unmount all
    /// make it false, which is what ends the poll loop.
    fn current(&self, provider: &str, id: &str) -> bool {
        self.live()
            && self
                .flow(provider)
                .map(|flow| flow.id == id && flow.polls())
                .unwrap_or(false)
    }

    /// Run `step` on the provider's flow if it is still flow `id`. Writes
    /// back (and notifies) only when the step reports a change.
    fn step(&self, provider: &str, id: &str, step: impl FnOnce(&mut Flow) -> bool) -> Option<Flow> {
        if !self.live() {
            return None;
        }
        let mut flow = self.flow(provider)?;
        if flow.id != id || !step(&mut flow) {
            return None;
        }
        let out = flow.clone();
        self.flows.try_update(|map| {
            map.insert(provider.to_string(), flow);
        });
        Some(out)
    }

    pub fn start(&self, method: Method, provider: String) {
        if self.is_busy(&provider) || self.flow(&provider).map(|f| f.polls()).unwrap_or(false) {
            return;
        }
        let Some((base, token)) = self.endpoint() else {
            return;
        };
        self.set_busy(&provider, true);
        let this = self.clone();
        spawn_local(async move {
            let result = post_json_value(
                &base,
                &token,
                method.start_path(),
                serde_json::json!({ "provider": provider }),
            )
            .await;
            this.set_busy(&provider, false);
            if !this.live() {
                return;
            }
            match result {
                Ok(value) => match Flow::start(method, &provider, &answer_from(&value)) {
                    Ok(flow) => {
                        let id = flow.id.clone();
                        let polls = flow.polls();
                        let done = flow.phase == Phase::Completed;
                        this.flows.try_update(|map| {
                            map.insert(provider.clone(), flow);
                        });
                        this.note(&provider, String::new());
                        if polls {
                            this.poll_later(method, provider, id);
                        } else if done {
                            this.reload();
                        }
                    }
                    Err(msg) => this.note(&provider, msg),
                },
                Err(err) => this.note(&provider, error_message(&err)),
            }
        });
    }

    fn poll_later(&self, method: Method, provider: String, id: String) {
        let this = self.clone();
        crate::clipboard::after_ms(POLL_MS, move || {
            if !this.current(&provider, &id) {
                return;
            }
            let Some((base, token)) = this.endpoint() else {
                return;
            };
            spawn_local(async move {
                let result = get_text(&base, &token, &flow_path(method, &id)).await;
                if !this.current(&provider, &id) {
                    return;
                }
                let changed = match result {
                    Ok(body) => {
                        let value = serde_json::from_str::<Value>(&body).unwrap_or(Value::Null);
                        let answer = answer_from(&value);
                        this.step(&provider, &id, |flow| flow.apply(&answer.status, &answer.message))
                    }
                    Err(err) if poll_error_is_final(&err) => {
                        let msg = error_message(&err);
                        this.step(&provider, &id, |flow| flow.lost(&msg))
                    }
                    // Transient: try again on the next tick.
                    Err(_) => None,
                };
                if changed.map(|flow| flow.phase == Phase::Completed).unwrap_or(false) {
                    this.reload();
                }
                if this.current(&provider, &id) {
                    this.poll_later(method, provider, id);
                }
            });
        });
    }

    pub fn cancel(&self, provider: String) {
        let Some(flow) = self.flow(&provider) else {
            return;
        };
        if !flow.can_cancel() || self.is_busy(&provider) {
            return;
        }
        let Some((base, token)) = self.endpoint() else {
            return;
        };
        self.set_busy(&provider, true);
        let this = self.clone();
        spawn_local(async move {
            let result = delete_json(&base, &token, &flow_path(flow.method, &flow.id), None).await;
            this.set_busy(&provider, false);
            match result {
                Ok(()) => {
                    this.step(&provider, &flow.id, |f| f.apply("canceled", ""));
                    // The daemon cancels whatever state the flow was in, so
                    // reload to show what is really stored.
                    this.reload();
                }
                Err(err) if poll_error_is_final(&err) => {
                    let msg = error_message(&err);
                    this.step(&provider, &flow.id, |f| f.lost(&msg));
                }
                Err(err) => this.note(
                    &provider,
                    format!("Could not cancel the sign-in: {}", error_message(&err)),
                ),
            }
        });
    }

    pub fn complete(&self, provider: String) {
        let Some(flow) = self.flow(&provider) else {
            return;
        };
        if !flow.accepts_paste() || self.is_busy(&provider) {
            return;
        }
        let paste = self
            .pastes
            .with_untracked(|map| map.get(&provider).cloned().unwrap_or_default())
            .trim()
            .to_string();
        if paste.is_empty() {
            self.note(
                &provider,
                "Paste the address you landed on, or the code, first.".into(),
            );
            return;
        }
        let Some((base, token)) = self.endpoint() else {
            return;
        };
        self.set_busy(&provider, true);
        let this = self.clone();
        spawn_local(async move {
            let result = post_json_value(
                &base,
                &token,
                &complete_path(&flow.id),
                serde_json::json!({ "paste": paste }),
            )
            .await;
            this.set_busy(&provider, false);
            if !this.live() {
                return;
            }
            match result {
                Ok(value) => {
                    this.pastes.try_update(|map| {
                        map.remove(&provider);
                    });
                    let status = value
                        .get("status")
                        .and_then(|v| v.as_str())
                        .unwrap_or("completed")
                        .to_string();
                    this.step(&provider, &flow.id, |f| f.apply(&status, ""));
                    this.note(&provider, String::new());
                    this.reload();
                }
                // The daemon keeps the flow pending after a refused paste,
                // so the user can paste again.
                Err(err) => this.note(&provider, paste_error(&error_message(&err), &paste)),
            }
        });
    }

    pub fn dismiss(&self, provider: &str) {
        if self.flow(provider).map(|f| f.phase.is_terminal()).unwrap_or(false) {
            self.flows.try_update(|map| {
                map.remove(provider);
            });
        }
    }

    fn copy(&self, provider: String, text: String, what: &'static str) {
        let this = self.clone();
        spawn_local(async move {
            let note = match crate::clipboard::copy_text(&text).await {
                Ok(()) => format!("Copied the {what}."),
                Err(err) => err,
            };
            this.note(&provider, note);
        });
    }
}

/// The sign-in methods and any flow for one provider card. Renders nothing
/// when the daemon advertises no device or OAuth method for the provider.
#[component]
pub fn SigninActions(provider: String, methods: Vec<String>, signin: Signin) -> impl IntoView {
    let offered = signin_methods(&methods);
    if offered.is_empty() {
        return ().into_any();
    }
    let wire = offered
        .iter()
        .map(|method| method.wire())
        .collect::<Vec<_>>()
        .join(" ");
    let flows = signin.flows;
    let busy = signin.busy;
    let mine = {
        let provider = provider.clone();
        Memo::new(move |_| flows.with(|map| map.get(&provider).cloned()))
    };
    let buttons = {
        let signin = signin.clone();
        let provider = provider.clone();
        move || {
            let pending = mine.get().map(|flow| flow.polls()).unwrap_or(false);
            (!pending).then(|| {
                offered
                    .iter()
                    .map(|&method| {
                        let signin = signin.clone();
                        let provider = provider.clone();
                        let waiting = provider.clone();
                        view! {
                            <button
                                type="button"
                                class="secondary"
                                data-signin=method.wire()
                                disabled=move || busy.with(|list| list.contains(&waiting))
                                on:click=move |_| signin.start(method, provider.clone())
                            >
                                {method.button_label()}
                            </button>
                        }
                    })
                    .collect_view()
            })
        }
    };
    let panel = move || {
        mine.get()
            .map(|flow| flow_panel(flow, provider.clone(), signin.clone()))
    };
    view! {
        <div class="providers-block providers-signin" data-signin-methods=wire>
            <div class="providers-actions">{buttons}</div>
            {panel}
        </div>
    }
    .into_any()
}

fn flow_panel(flow: Flow, provider: String, signin: Signin) -> impl IntoView {
    let pending = flow.polls();
    let method = flow.method;
    let link = match method {
        Method::Device => flow.verification_uri.clone(),
        Method::OAuth => flow.authorize_url.clone(),
    };
    let code = (method == Method::Device && pending && !flow.user_code.is_empty())
        .then(|| flow.user_code.clone());
    let expires = if pending {
        expires_label(&flow.expires_at)
    } else {
        None
    };
    let busy = signin.busy;

    let code_row = code.map(|code| {
        let signin = signin.clone();
        let provider = provider.clone();
        let copied = code.clone();
        view! {
            <div class="providers-signin-row">
                <span class="providers-signin-code" data-signin-code="true">{code}</span>
                <button
                    type="button"
                    class="secondary"
                    data-signin-copy-code="true"
                    on:click=move |_| signin.copy(provider.clone(), copied.clone(), "code")
                >
                    "Copy code"
                </button>
            </div>
        }
    });

    let link_row = (pending && !link.is_empty()).then(|| {
        let signin = signin.clone();
        let provider = provider.clone();
        let copied = link.clone();
        let shown = match safe_link(&link) {
            Some(href) => {
                let href = href.to_string();
                let text = href.clone();
                view! {
                    <a
                        class="providers-signin-link"
                        href=href
                        target="_blank"
                        rel="noopener noreferrer"
                        data-signin-url="true"
                    >
                        {text}
                    </a>
                }
                .into_any()
            }
            None => view! {
                <code class="providers-signin-link" data-signin-url="true">{link.clone()}</code>
            }
            .into_any(),
        };
        view! {
            <div class="providers-signin-row">
                {shown}
                <button
                    type="button"
                    class="secondary"
                    data-signin-copy-link="true"
                    on:click=move |_| signin.copy(provider.clone(), copied.clone(), "link")
                >
                    "Copy link"
                </button>
            </div>
        }
    });

    let paste_form = flow.accepts_paste().then(|| {
        let pastes = signin.pastes;
        let read = provider.clone();
        let write = provider.clone();
        let label = provider.clone();
        let waiting = provider.clone();
        let signin = signin.clone();
        let submit = provider.clone();
        view! {
            <label>
                "Address you landed on, or the code"
                <input
                    type="password"
                    autocomplete="off"
                    spellcheck="false"
                    name="strike-oauth-paste"
                    data-signin-paste="true"
                    aria-label=format!("Redirect address or code for {label}")
                    prop:value=move || pastes.with(|map| map.get(&read).cloned().unwrap_or_default())
                    on:input=move |ev| {
                        let value = event_target_value(&ev);
                        pastes.update(|map| {
                            map.insert(write.clone(), value);
                        });
                    }
                />
            </label>
            <div class="providers-actions">
                <button
                    type="button"
                    class="primary"
                    data-signin-complete="true"
                    disabled=move || busy.with(|list| list.contains(&waiting))
                    on:click=move |_| signin.complete(submit.clone())
                >
                    "Finish sign-in"
                </button>
            </div>
        }
    });

    let end_button = if flow.can_cancel() {
        let signin = signin.clone();
        let provider = provider.clone();
        let waiting = provider.clone();
        view! {
            <button
                type="button"
                class="secondary"
                data-signin-cancel="true"
                disabled=move || busy.with(|list| list.contains(&waiting))
                on:click=move |_| signin.cancel(provider.clone())
            >
                "Cancel sign-in"
            </button>
        }
        .into_any()
    } else {
        let signin = signin.clone();
        let provider = provider.clone();
        view! {
            <button
                type="button"
                class="secondary"
                data-signin-dismiss="true"
                on:click=move |_| signin.dismiss(&provider)
            >
                "Dismiss"
            </button>
        }
        .into_any()
    };

    view! {
        <div
            class="providers-signin-flow"
            data-signin-flow=method.wire()
            data-signin-phase=flow.phase.wire()
        >
            <p class="providers-signin-phase" role="status">{flow.phase_label()}</p>
            {code_row}
            {link_row}
            <p class="providers-signin-note" data-signin-note="true">{flow.note()}</p>
            {expires.map(|text| view! { <p class="hint">{text}</p> })}
            {paste_form}
            <div class="providers-actions">{end_button}</div>
        </div>
    }
}
