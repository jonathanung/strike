//! Web Push toggle for the notification settings (#1564).
//!
//! The browser half lives in `register.js` (`window.strikePush`): permission,
//! `PushManager.subscribe`, and unsubscribe. This component asks it for the
//! state, then registers or removes the subscription on the daemon
//! (`/v1/notifications/push-subscriptions`). Pushes carry only a kind and a
//! deep link; `sw.js` turns the kind into fixed banner text.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use wasm_bindgen::{JsCast, JsValue};
use wasm_bindgen_futures::JsFuture;

use crate::api::{delete_json, get_json, post_json};

/// What the toggle can honestly say.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum PushState {
    Checking,
    Unsupported,
    IosNotInstalled,
    Denied,
    OtherGateway,
    Off,
    On,
    Failed(String),
}

/// Map `strikePush.support()` (plus whether this page talks to its own
/// gateway) to a state. Push needs the page's own service worker, so a
/// cockpit pointed at another gateway cannot subscribe for it.
pub fn state_from_support(support: &str, same_origin: bool) -> PushState {
    match support {
        "ok" if !same_origin => PushState::OtherGateway,
        "ok" => PushState::Off,
        "denied" => PushState::Denied,
        "ios-not-installed" => PushState::IosNotInstalled,
        _ => PushState::Unsupported,
    }
}

pub fn state_label(state: &PushState) -> String {
    match state {
        PushState::Checking => "Checking…".into(),
        PushState::Unsupported => "This browser cannot receive push notifications.".into(),
        PushState::IosNotInstalled => {
            "On iPhone and iPad, add Strike to the Home Screen first, then turn this on from there.".into()
        }
        PushState::Denied => "Notifications are blocked for this site in the browser settings.".into(),
        PushState::OtherGateway => "Push works only for the gateway that served this page.".into(),
        PushState::Off => "Off".into(),
        PushState::On => "On for this browser".into(),
        PushState::Failed(msg) => format!("Could not change push: {msg}"),
    }
}

/// The delivery line under the toggle, from the daemon's push status.
pub fn delivery_label(last_result: &str) -> &'static str {
    match last_result {
        "failed" => "Last push failed to deliver.",
        "ok" => "Last push delivered to the push service.",
        _ => "",
    }
}

#[derive(Clone, Debug, Default, Deserialize)]
struct PushStatus {
    #[serde(default, rename = "lastResult")]
    last_result: String,
}

#[derive(Deserialize)]
struct StatusOnly {
    #[serde(default)]
    push: PushStatus,
}

#[derive(Deserialize)]
struct KeyResponse {
    #[serde(rename = "publicKey")]
    public_key: String,
}

fn js_message(e: JsValue) -> String {
    if let Some(s) = e.as_string() {
        return s;
    }
    js_sys::Reflect::get(&e, &JsValue::from_str("message"))
        .ok()
        .and_then(|m| m.as_string())
        .unwrap_or_else(|| "browser error".into())
}

fn helper_call(method: &str, arg: Option<&str>) -> Result<JsValue, String> {
    let window = web_sys::window().ok_or("no window")?;
    let api = js_sys::Reflect::get(&window, &JsValue::from_str("strikePush"))
        .map_err(js_message)?;
    if !api.is_object() {
        return Err("unsupported".into());
    }
    let func = js_sys::Reflect::get(&api, &JsValue::from_str(method))
        .map_err(js_message)?
        .dyn_into::<js_sys::Function>()
        .map_err(|_| "unsupported".to_string())?;
    match arg {
        Some(a) => func.call1(&api, &JsValue::from_str(a)),
        None => func.call0(&api),
    }
    .map_err(js_message)
}

async fn helper_await(method: &str, arg: Option<&str>) -> Result<String, String> {
    let value = helper_call(method, arg)?;
    let promise = value
        .dyn_into::<js_sys::Promise>()
        .map_err(|_| "unsupported".to_string())?;
    let out = JsFuture::from(promise).await.map_err(js_message)?;
    Ok(out.as_string().unwrap_or_default())
}

fn page_same_origin(base: &str) -> bool {
    let base = base.trim().trim_end_matches('/');
    if base.is_empty() {
        return true;
    }
    web_sys::window()
        .and_then(|w| w.location().origin().ok())
        .is_some_and(|origin| origin == base)
}

#[component]
pub fn PushToggle(base: RwSignal<String>, token: RwSignal<String>) -> impl IntoView {
    let state = RwSignal::new(PushState::Checking);
    let delivery = RwSignal::new(String::new());
    let busy = RwSignal::new(false);

    let refresh = move || {
        let support = helper_call("support", None)
            .ok()
            .and_then(|v| v.as_string())
            .unwrap_or_default();
        let initial = state_from_support(&support, page_same_origin(&base.get_untracked()));
        state.set(initial.clone());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if initial == PushState::Off {
                if let Ok(endpoint) = helper_await("current", None).await {
                    if !endpoint.is_empty() {
                        state.set(PushState::On);
                    }
                }
            }
            if let Ok(st) = get_json::<StatusOnly>(&b, &t, "/v1/notifications?since=9223372036854775807").await {
                delivery.set(delivery_label(&st.push.last_result).to_string());
            }
        });
    };
    refresh();

    let toggle = move |_| {
        if busy.get_untracked() {
            return;
        }
        let current = state.get_untracked();
        if current != PushState::On && current != PushState::Off && !matches!(current, PushState::Failed(_)) {
            return;
        }
        busy.set(true);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let outcome: Result<PushState, String> = if current == PushState::On {
                async {
                    let endpoint = helper_await("disable", None).await?;
                    if !endpoint.is_empty() {
                        let body = serde_json::json!({"endpoint": endpoint});
                        // Already gone on the daemon is fine: the browser is off.
                        let _ = delete_json(&b, &t, "/v1/notifications/push-subscriptions", Some(body)).await;
                    }
                    Ok::<PushState, String>(PushState::Off)
                }
                .await
            } else {
                async {
                    let key = get_json::<KeyResponse>(&b, &t, "/v1/notifications/push-key").await?;
                    let sub = helper_await("enable", Some(&key.public_key)).await?;
                    let body: serde_json::Value =
                        serde_json::from_str(&sub).map_err(|e| e.to_string())?;
                    post_json(&b, &t, "/v1/notifications/push-subscriptions", body).await?;
                    Ok::<PushState, String>(PushState::On)
                }
                .await
            };
            match outcome {
                Ok(next) => state.set(next),
                Err(e) if e == "denied" => state.set(PushState::Denied),
                Err(e) => state.set(PushState::Failed(e)),
            }
            busy.set(false);
        });
    };

    view! {
        <div class="notify-push" data-push-state=move || format!("{:?}", state.get())>
            <div class="notify-perm">
                <button class="secondary"
                    disabled=move || busy.get() || !matches!(state.get(), PushState::On | PushState::Off | PushState::Failed(_))
                    on:click=toggle
                >{move || if state.get() == PushState::On { "Turn off push" } else { "Turn on push" }}</button>
                <span role="status">{move || state_label(&state.get())}</span>
            </div>
            <p class="notify-hint">"Push reaches this browser even when Strike is closed. Only approvals and blocked runs push. The message says what kind of thing happened and links to it; session names and output stay here."</p>
            <Show when=move || !delivery.get().is_empty()>
                <p class="notify-hint">{move || delivery.get()}</p>
            </Show>
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn support_maps_to_honest_states() {
        let cases = [
            ("ok", true, PushState::Off),
            ("ok", false, PushState::OtherGateway),
            ("denied", true, PushState::Denied),
            ("ios-not-installed", true, PushState::IosNotInstalled),
            ("unsupported", true, PushState::Unsupported),
            ("", true, PushState::Unsupported),
        ];
        for (support, same, want) in cases {
            assert_eq!(state_from_support(support, same), want, "{support}/{same}");
        }
    }

    #[test]
    fn labels_never_pretend() {
        assert!(state_label(&PushState::Unsupported).contains("cannot"));
        assert!(state_label(&PushState::IosNotInstalled).contains("Home Screen"));
        assert!(state_label(&PushState::Denied).contains("blocked"));
        assert_eq!(delivery_label("failed"), "Last push failed to deliver.");
        assert_eq!(delivery_label(""), "");
    }
}
