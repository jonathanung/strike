//! Phone pairing dialog.
//!
//! The pairing code travels in the URL fragment (`#pair=`), then this page
//! strips that fragment and POSTs the code to `/v1/pair/redeem`. The daemon
//! token is never placed in a URL. Same-origin redeem stores an HttpOnly
//! cookie. A cross-origin scan (another daemon) asks for `delivery: token`
//! because a third-party cookie would not stick. See docs/gateway.md.

use std::cell::RefCell;
use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;
use wasm_bindgen::JsValue;

use crate::api::{self, delete_json, get_json, post_json_value};

/// Page wiring for the shell command in `src/shell/contrib/gateway_qr.rs`.
/// The shell boots before App's signals exist, so App installs them here.
#[derive(Clone)]
pub struct PhoneHost {
    pub base: RwSignal<String>,
    pub token: RwSignal<String>,
    pub status: RwSignal<String>,
    pub open: RwSignal<bool>,
    pub refresh: Rc<dyn Fn()>,
}

thread_local! {
    static HOST: RefCell<Option<PhoneHost>> = const { RefCell::new(None) };
}

pub fn set_host(host: PhoneHost) {
    HOST.with(|slot| *slot.borrow_mut() = Some(host));
}

fn phone_host() -> Option<PhoneHost> {
    HOST.with(|slot| slot.borrow().clone())
}

/// Mounted beside the shell so a `#pair=` landing is redeemed even when the
/// dialog is closed. The refresh closure re-reads the host so it stays `Copy`.
pub fn phone_view() -> AnyView {
    let Some(h0) = phone_host() else {
        return view! { <span></span> }.into_any();
    };
    view! {
        <GatewayQR
            open=h0.open
            base=h0.base
            token=h0.token
            status=h0.status
            refresh=move || {
                if let Some(h) = phone_host() {
                    (*h.refresh)();
                }
            }
        />
    }
    .into_any()
}

#[derive(Clone, Debug, Deserialize, Default)]
struct GatewayURL {
    #[serde(default)]
    kind: String,
    #[serde(default)]
    url: String,
}

#[derive(Clone, Debug, Deserialize, Default)]
struct GatewayInfo {
    #[serde(rename = "phoneReachable", default)]
    phone_reachable: bool,
    #[serde(default)]
    urls: Vec<GatewayURL>,
    #[serde(rename = "optIn", default)]
    opt_in: String,
    #[serde(rename = "lanWarning", default)]
    lan_warning: String,
    #[serde(rename = "advertiseWarning", default)]
    advertise_warning: String,
    #[serde(default)]
    unbound: Vec<String>,
}

#[derive(Clone, Debug, Deserialize, Default)]
struct PairMint {
    #[serde(default)]
    url: String,
    #[serde(rename = "ttlSeconds", default)]
    ttl_seconds: i64,
    #[serde(default)]
    gateway: GatewayInfo,
}

#[derive(Clone, Debug, Deserialize)]
struct DeviceRow {
    id: String,
    #[serde(default)]
    label: String,
    #[serde(default)]
    revoked: bool,
}

#[derive(Deserialize, Default)]
struct DeviceList {
    #[serde(default)]
    devices: Vec<DeviceRow>,
}

struct PairPaint {
    note: String,
    url: String,
    svg: String,
    warning: String,
    devices: Vec<DeviceRow>,
    device_note: String,
    /// True when a pairing URL exists but a phone can never open it
    /// (loopback target, #1522): the dialog must show no QR and no
    /// copyable URL, only the reachable-path instructions.
    blocked: bool,
    /// Copyable restart command for the blocked case; empty otherwise.
    restart: String,
}

struct ScanSlot {
    interval: i32,
    stream: Option<web_sys::MediaStream>,
    closure: Option<Closure<dyn FnMut()>>,
    prompt: Option<JsValue>,
}

thread_local! {
    static SCAN: RefCell<ScanSlot> = RefCell::new(ScanSlot {
        interval: -1,
        stream: None,
        closure: None,
        prompt: None,
    });
}

fn halt_scan() {
    SCAN.with(|slot| {
        let mut slot = slot.borrow_mut();
        if let Some(win) = web_sys::window() {
            if slot.interval >= 0 {
                win.clear_interval_with_handle(slot.interval);
                slot.interval = -1;
            }
        }
        if let Some(stream) = slot.stream.take() {
            let tracks = stream.get_tracks();
            for i in 0..tracks.length() {
                if let Ok(track) = tracks.get(i).dyn_into::<web_sys::MediaStreamTrack>() {
                    track.stop();
                }
            }
        }
        if let Some(video) = camera_element() {
            let _ = video.set_src_object(None);
        }
        // Leak the callback. Dropping it from inside the interval is unsafe.
        if let Some(cb) = slot.closure.take() {
            cb.forget();
        }
    });
}

fn store_prompt(ev: JsValue) {
    SCAN.with(|slot| slot.borrow_mut().prompt = Some(ev));
}

thread_local! {
    static INSTALL_READY: std::cell::Cell<bool> = const { std::cell::Cell::new(false) };
}

pub fn install_ready() -> bool {
    INSTALL_READY.with(|slot| slot.get())
}

fn set_install_ready(ready: bool) {
    INSTALL_READY.with(|slot| slot.set(ready));
}

fn call_install_prompt() {
    let ev = SCAN.with(|slot| slot.borrow().prompt.clone());
    let Some(ev) = ev else {
        return;
    };
    if let Ok(fun) = js_sys::Reflect::get(&ev, &JsValue::from_str("prompt")) {
        if let Ok(fun) = fun.dyn_into::<js_sys::Function>() {
            let _ = fun.call0(&ev);
        }
    }
}

#[component]
pub(crate) fn GatewayQR(
    open: RwSignal<bool>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    refresh: impl Fn() + Copy + Send + Sync + 'static,
) -> impl IntoView {
    let note = RwSignal::new(String::new());
    let url = RwSignal::new(String::new());
    let svg = RwSignal::new(String::new());
    let warning = RwSignal::new(String::new());
    let devices = RwSignal::new(Vec::<DeviceRow>::new());
    let device_note = RwSignal::new(String::new());
    let blocked = RwSignal::new(false);
    let restart = RwSignal::new(String::new());
    crate::shell::bind(
        "app.install",
        Rc::new(|_| {
            if !install_ready() {
                return Err("This browser has not offered to install Strike.".into());
            }
            call_install_prompt();
            Ok(())
        }),
    );
    crate::shell::bind(
        "pair.revoke-all",
        Rc::new(move |_| {
            revoke_all(base, token, devices, device_note);
            Ok(())
        }),
    );
    let scan_note = RwSignal::new(String::new());
    let landed = RwSignal::new(false);
    let install_ready = RwSignal::new(false);

    if let Some(win) = web_sys::window() {
        let cb = Closure::<dyn FnMut(web_sys::Event)>::new(move |ev: web_sys::Event| {
            ev.prevent_default();
            store_prompt(ev.into());
            install_ready.set(true);
            set_install_ready(true);
        });
        let _ = win
            .add_event_listener_with_callback("beforeinstallprompt", cb.as_ref().unchecked_ref());
        cb.forget();
    }

    kick_pair(base, token, status, landed, refresh);
    if let Some(win) = web_sys::window() {
        let cb = Closure::<dyn FnMut(web_sys::Event)>::new(move |_ev| {
            kick_pair(base, token, status, landed, refresh);
        });
        let _ = win.add_event_listener_with_callback("hashchange", cb.as_ref().unchecked_ref());
        cb.forget();
    }

    Effect::new(move |_| {
        if !open.get() {
            halt_scan();
            return;
        }
        queue_pair(
            base,
            token,
            note,
            url,
            svg,
            warning,
            devices,
            device_note,
            blocked,
            restart,
        );
    });
    on_cleanup(halt_scan);

    // Set when a Copy URL attempt was rejected by the browser (#1582):
    // the dialog then shows the manual-copy fallback instead of the old
    // unconditional "Pairing URL copied." claim.
    let copy_failed = RwSignal::new(false);
    let copy_url = move |_| {
        let text = url.get_untracked();
        if text.is_empty() {
            return;
        }
        spawn_local(async move {
            let ok = crate::clipboard::copy_text(&text).await.is_ok();
            copy_failed.set(!ok);
            status.set(crate::clipboard::pairing_copy_feedback(ok).into());
            if !ok {
                select_pairing_url();
            }
        });
    };

    let copy_restart = move |_| {
        let text = restart.get_untracked();
        if text.is_empty() {
            return;
        }
        spawn_local(async move {
            let ok = crate::clipboard::copy_text(&text).await.is_ok();
            let msg = if ok {
                "Restart command copied."
            } else {
                "Copy didn't work in this browser. Select the restart command and copy it manually."
            };
            status.set(msg.into());
        });
    };

    view! {
        <Show when=move || landed.get()>
            <div class="gq-landing" role="status">
                <p>"Paired with this gateway. Add Strike to the home screen to open it again."</p>
                <div class="gq-actions">
                    <button class="secondary gq-revoke-self" type="button" on:click=move |_| {
                        if !crate::confirm::ask(
                            crate::confirm::ConfirmRequest {
                                title: "Revoke this browser?".into(),
                                body: "This browser's grant stops working. Pair again to come back.".into(),
                                confirm_label: "Revoke this browser".into(),
                            },
                            Rc::new(move || revoke_self(base, token, status, landed, refresh)),
                        ) {
                            status.set(crate::confirm::BUSY.into());
                        }
                    }>"Revoke this browser"</button>
                </div>
            </div>
        </Show>
        <Show when=move || open.get()>
            <div class="gq-backdrop" on:click=move |_| open.set(false)>
                <div
                    class="gq-dialog"
                    role="dialog"
                    aria-modal="true"
                    aria-labelledby="gq-title"
                    on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()
                >
                    <div class="gq-head">
                        <h2 id="gq-title">"Open on phone"</h2>
                        <button class="secondary gq-close" type="button" on:click=move |_| open.set(false)>"Close"</button>
                    </div>
                    <p class="gq-note">{move || note.get()}</p>
                    <Show when=move || blocked.get()>
                        <div class="gq-blocked" role="alert">
                            <p class="gq-blocked-title">"No QR to scan — a phone cannot open this gateway"</p>
                            <p>"Restart the gateway on an address your phone can reach, then reopen this dialog:"</p>
                            <code class="gq-cmd">{move || restart.get()}</code>
                            <div class="gq-actions">
                                <button class="secondary" type="button" on:click=copy_restart>"Copy restart command"</button>
                                <button class="secondary gq-new" type="button" on:click=move |_| { copy_failed.set(false); queue_pair(base, token, note, url, svg, warning, devices, device_note, blocked, restart); }>"Check again"</button>
                            </div>
                        </div>
                    </Show>
                    <Show when=move || !blocked.get()>
                        <div class="gq-qr" inner_html=move || svg.get()></div>
                        <input id="gq-url" readonly aria-label="Pairing URL" prop:value=move || url.get() />
                        <div class="gq-actions">
                            <button class="secondary" type="button" on:click=copy_url>"Copy URL"</button>
                            <button class="secondary gq-new" type="button" on:click=move |_| { copy_failed.set(false); queue_pair(base, token, note, url, svg, warning, devices, device_note, blocked, restart); }>"New code"</button>
                        </div>
                        <Show when=move || copy_failed.get()>
                            <p class="gq-copy-fallback" role="alert">
                                "Copy didn't work in this browser. The pairing URL above is selected — copy it manually, then open it on your phone to pair."
                            </p>
                        </Show>
                    </Show>
                    <p class="gq-warn">{move || warning.get()}</p>
                    <h3 class="gq-sub">"Paired browsers"</h3>
                    <p class="hint">{move || device_note.get()}</p>
                    <ul class="plain">
                        {move || devices.get().into_iter().map(|d| {
                            let id = d.id.clone();
                            let label = if d.label.is_empty() { "browser".to_string() } else { d.label };
                            let ask_label = label.clone();
                            let revoked = d.revoked;
                            view! {
                                <li class="row gq-device">
                                    <span>{label}{if revoked { " · revoked" } else { "" }}</span>
                                    <button
                                        class="secondary"
                                        type="button"
                                        disabled=revoked
                                        on:click=move |_| {
                                            let id = id.clone();
                                            if !crate::confirm::ask(
                                                crate::confirm::ConfirmRequest {
                                                    title: format!("Revoke {ask_label}?"),
                                                    body: format!("{ask_label} loses its grant and has to pair again to come back."),
                                                    confirm_label: "Revoke".into(),
                                                },
                                                Rc::new(move || revoke_device(base, token, id.clone(), devices, device_note)),
                                            ) {
                                                device_note.set(crate::confirm::BUSY.into());
                                            }
                                        }
                                    >"Revoke"</button>
                                </li>
                            }
                        }).collect_view()}
                    </ul>
                    <button class="secondary" type="button" on:click=move |_| {
                        let live = devices.get_untracked().iter().filter(|d| !d.revoked).count();
                        let body = match live {
                            0 => "Every paired browser loses its grant. Pairing again mints a new code.".to_string(),
                            1 => "The one paired browser loses its grant. Pairing again mints a new code.".to_string(),
                            n => format!("All {n} paired browsers lose their grants. Pairing again mints a new code."),
                        };
                        if !crate::confirm::ask(
                            crate::confirm::ConfirmRequest {
                                title: "Revoke paired browsers?".into(),
                                body,
                                confirm_label: "Revoke all".into(),
                            },
                            Rc::new(move || revoke_all(base, token, devices, device_note)),
                        ) {
                            device_note.set(crate::confirm::BUSY.into());
                        }
                    }>"Revoke all"</button>
                    <h3 class="gq-sub">"Scan a gateway QR"</h3>
                    <p class="hint">"Add another daemon. The phone camera can open a Strike QR in the browser."</p>
                    <video id="gq-cam" class="gq-video" autoplay=true muted=true playsinline=true></video>
                    <div class="gq-actions">
                        <button class="secondary gq-scan" type="button" on:click=move |_| begin_scan(base, token, status, scan_note, landed, refresh)>"Scan"</button>
                    </div>
                    <p class="hint">{move || scan_note.get()}</p>
                </div>
            </div>
        </Show>
    }
}

/// Select the pairing URL field so a user whose clipboard write was
/// rejected can copy it manually with one keystroke (#1582).
fn select_pairing_url() {
    let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
        return;
    };
    let Some(el) = doc.get_element_by_id("gq-url") else {
        return;
    };
    if let Ok(input) = el.dyn_into::<web_sys::HtmlInputElement>() {
        input.select();
    }
}

#[allow(clippy::too_many_arguments)]
fn queue_pair(
    base: RwSignal<String>,
    token: RwSignal<String>,
    note: RwSignal<String>,
    url: RwSignal<String>,
    svg: RwSignal<String>,
    warning: RwSignal<String>,
    devices: RwSignal<Vec<DeviceRow>>,
    device_note: RwSignal<String>,
    blocked: RwSignal<bool>,
    restart: RwSignal<String>,
) {
    let (b, t) = (base.get_untracked(), token.get_untracked());
    spawn_local(async move {
        let paint = fetch_pair(&b, &t).await;
        note.set(paint.note);
        url.set(paint.url);
        svg.set(paint.svg);
        warning.set(paint.warning);
        devices.set(paint.devices);
        device_note.set(paint.device_note);
        blocked.set(paint.blocked);
        restart.set(paint.restart);
    });
}

fn kick_pair(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    landed: RwSignal<bool>,
    refresh: impl Fn() + Copy + 'static,
) {
    let Some(code) = consume_pair_fragment() else {
        return;
    };
    let (b, t) = (base.get_untracked(), token.get_untracked());
    spawn_local(async move {
        apply_cookie_redeem(&b, &t, &code, status, landed, refresh).await;
    });
}

fn revoke_device(
    base: RwSignal<String>,
    token: RwSignal<String>,
    id: String,
    devices: RwSignal<Vec<DeviceRow>>,
    device_note: RwSignal<String>,
) {
    let (b, t) = (base.get_untracked(), token.get_untracked());
    spawn_local(async move {
        let path = format!("/v1/pair/devices/{}", api::js_encode(&id));
        match delete_json(&b, &t, &path, None).await {
            Ok(()) => match get_json::<DeviceList>(&b, &t, "/v1/pair/devices").await {
                Ok(list) => {
                    devices.set(list.devices);
                    device_note.set(String::new());
                }
                Err(_) => device_note
                    .set("Revoked. The device list needs the daemon token to refresh.".into()),
            },
            Err(_) => device_note.set("Could not revoke that browser.".into()),
        }
    });
}

fn revoke_all(
    base: RwSignal<String>,
    token: RwSignal<String>,
    devices: RwSignal<Vec<DeviceRow>>,
    device_note: RwSignal<String>,
) {
    let (b, t) = (base.get_untracked(), token.get_untracked());
    spawn_local(async move {
        match post_json_value(&b, &t, "/v1/pair/revoke-all", serde_json::json!({})).await {
            Ok(_) => {
                devices.set(Vec::new());
                device_note
                    .set("Paired browsers were revoked. Outstanding codes were dropped.".into());
            }
            Err(_) => device_note.set("Revoke all needs the daemon token.".into()),
        }
    });
}

fn revoke_self(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    landed: RwSignal<bool>,
    refresh: impl Fn() + Copy + 'static,
) {
    let (b, t) = (base.get_untracked(), token.get_untracked());
    spawn_local(async move {
        match post_json_value(&b, &t, "/v1/pair/revoke-self", serde_json::json!({})).await {
            Ok(_) => {
                landed.set(false);
                status.set("This browser was revoked.".into());
                refresh();
            }
            Err(_) => status.set("Could not revoke this browser.".into()),
        }
    });
}

fn begin_scan(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    scan_note: RwSignal<String>,
    landed: RwSignal<bool>,
    refresh: impl Fn() + Copy + Send + Sync + 'static,
) {
    halt_scan();
    if !barcode_available() {
        scan_note.set(
            "The phone camera can still open a Strike QR. This browser has no BarcodeDetector."
                .into(),
        );
        return;
    }
    spawn_local(async move {
        let Some(window) = web_sys::window() else {
            return;
        };
        let media = match window.navigator().media_devices() {
            Ok(media) => media,
            Err(_) => {
                scan_note.set(
                    "This browser has no camera API. The phone camera can still open a Strike QR."
                        .into(),
                );
                return;
            }
        };
        let constraints = web_sys::MediaStreamConstraints::new();
        constraints.set_video(&JsValue::TRUE);
        let promise = match media.get_user_media_with_constraints(&constraints) {
            Ok(p) => p,
            Err(_) => {
                scan_note.set(
                    "Camera permission was denied. The phone camera can still open a Strike QR."
                        .into(),
                );
                return;
            }
        };
        let stream_val = match wasm_bindgen_futures::JsFuture::from(promise).await {
            Ok(v) => v,
            Err(_) => {
                scan_note.set(
                    "Camera permission was denied. The phone camera can still open a Strike QR."
                        .into(),
                );
                return;
            }
        };
        let Ok(stream) = stream_val.dyn_into::<web_sys::MediaStream>() else {
            return;
        };
        if let Some(video) = camera_element() {
            let _ = video.set_src_object(Some(&stream));
            let _ = video.play();
        }
        SCAN.with(|slot| slot.borrow_mut().stream = Some(stream));
        let Some(detector) = new_detector() else {
            halt_scan();
            scan_note.set(
                "The phone camera can still open a Strike QR. This browser has no BarcodeDetector."
                    .into(),
            );
            return;
        };
        scan_note.set("Point the camera at a Strike pairing QR.".into());
        let detector = std::rc::Rc::new(detector);
        let busy = std::rc::Rc::new(std::cell::Cell::new(false));
        let found = std::rc::Rc::new(std::cell::Cell::new(false));
        let cb = Closure::<dyn FnMut()>::new(move || {
            if busy.get() || found.get() {
                return;
            }
            let Some(video) = camera_element() else {
                return;
            };
            let detector = detector.clone();
            let busy = busy.clone();
            let found = found.clone();
            busy.set(true);
            spawn_local(async move {
                let text = detect_once(&detector, &video).await;
                busy.set(false);
                let Some(text) = text else {
                    return;
                };
                if found.get() {
                    return;
                }
                found.set(true);
                halt_scan();
                match scan_target(&text) {
                    Ok((origin, code)) => {
                        scan_note.set("QR read. Exchanging the pairing code.".into());
                        redeem_scanned(
                            origin, code, base, token, status, scan_note, landed, refresh,
                        )
                        .await;
                    }
                    Err("secret-query") => scan_note.set(
                        "That QR puts a secret in the query string. Pairing links do not. Ask for a fresh code."
                            .into(),
                    ),
                    Err(_) => scan_note.set(
                        "That QR is not a Strike pairing link. It needs a one-time fragment code."
                            .into(),
                    ),
                }
            });
        });
        if let Some(handle) = window
            .set_interval_with_callback_and_timeout_and_arguments_0(
                cb.as_ref().unchecked_ref(),
                500,
            )
            .ok()
        {
            SCAN.with(|slot| {
                let mut slot = slot.borrow_mut();
                slot.interval = handle;
                slot.closure = Some(cb);
            });
        }
    });
}

fn camera_element() -> Option<web_sys::HtmlVideoElement> {
    web_sys::window()?
        .document()?
        .get_element_by_id("gq-cam")?
        .dyn_into::<web_sys::HtmlVideoElement>()
        .ok()
}

fn barcode_available() -> bool {
    js_sys::Reflect::has(&js_sys::global(), &JsValue::from_str("BarcodeDetector")).unwrap_or(false)
}

fn new_detector() -> Option<JsValue> {
    let ctor =
        js_sys::Reflect::get(&js_sys::global(), &JsValue::from_str("BarcodeDetector")).ok()?;
    if ctor.is_undefined() || ctor.is_null() {
        return None;
    }
    let ctor = ctor.dyn_into::<js_sys::Function>().ok()?;
    let opts = js_sys::Object::new();
    let formats = js_sys::Array::of1(&JsValue::from_str("qr_code"));
    js_sys::Reflect::set(&opts, &JsValue::from_str("formats"), &formats).ok()?;
    js_sys::Reflect::construct(&ctor, &js_sys::Array::of1(&opts)).ok()
}

async fn detect_once(det: &JsValue, video: &web_sys::HtmlVideoElement) -> Option<String> {
    let fun = js_sys::Reflect::get(det, &JsValue::from_str("detect")).ok()?;
    let fun = fun.dyn_into::<js_sys::Function>().ok()?;
    let promise = fun
        .call1(det, video)
        .ok()?
        .dyn_into::<js_sys::Promise>()
        .ok()?;
    let value = wasm_bindgen_futures::JsFuture::from(promise).await.ok()?;
    let arr = value.dyn_into::<js_sys::Array>().ok()?;
    if arr.length() == 0 {
        return None;
    }
    js_sys::Reflect::get(&arr.get(0), &JsValue::from_str("rawValue"))
        .ok()?
        .as_string()
}

async fn fetch_pair(base: &str, token: &str) -> PairPaint {
    let page = page_origin();
    match post_json_value(
        base,
        token,
        "/v1/pair",
        serde_json::json!({"label": "browser"}),
    )
    .await
    {
        Ok(v) => match serde_json::from_value::<PairMint>(v) {
            Ok(mint) => {
                let (devices, device_note) = load_devices(base, token).await;
                let ttl = if mint.ttl_seconds > 0 {
                    mint.ttl_seconds / 60
                } else {
                    5
                };
                let note = format!(
                    "This link works once and expires in about {ttl} minutes. The daemon token is not in the URL."
                );
                pair_paint(
                    &mint.gateway,
                    &mint.url,
                    &page,
                    note,
                    "",
                    devices,
                    device_note,
                )
            }
            Err(_) => PairPaint {
                note: "Could not mint a pairing code.".into(),
                url: String::new(),
                svg: String::new(),
                warning: String::new(),
                devices: Vec::new(),
                device_note: String::new(),
                blocked: false,
                restart: String::new(),
            },
        },
        Err(err) => {
            let (extra, gateway) = public_pair_error(&err);
            let gateway = gateway.unwrap_or_default();
            let plain = plain_gateway_url(&gateway);
            let note = compose_note(&gateway.opt_in, &extra);
            pair_paint(
                &gateway,
                &plain,
                &page,
                note,
                &extra,
                Vec::new(),
                String::new(),
            )
        }
    }
}

/// Compose the dialog paint from a gateway snapshot and the URL the daemon
/// proposes. This is the one place that decides whether a QR may be shown
/// (#1522). A URL whose host is loopback can never be opened by a phone, so
/// when the effective target is unreachable the paint carries no QR and no
/// copyable URL — in both the minted (`--auth`) and the public (no-auth)
/// variants — and leads with the daemon's reachable-path instructions plus
/// a copyable restart command instead. When the gateway is reachable the
/// paint is exactly what the dialog showed before.
fn pair_paint(
    info: &GatewayInfo,
    server_url: &str,
    page_origin: &str,
    note: String,
    blocked_lead: &str,
    devices: Vec<DeviceRow>,
    device_note: String,
) -> PairPaint {
    let shown = if server_url.is_empty() {
        String::new()
    } else {
        shown_pair_url(server_url, info.phone_reachable, page_origin)
    };
    if shown.is_empty() {
        return PairPaint {
            note,
            url: String::new(),
            svg: String::new(),
            warning: warnings(info),
            devices,
            device_note,
            blocked: false,
            restart: String::new(),
        };
    }
    if pair_url_scannable(&shown, info.phone_reachable, page_origin) {
        return PairPaint {
            note,
            svg: qr_markup(&shown),
            url: shown,
            warning: warnings(info),
            devices,
            device_note,
            blocked: false,
            restart: String::new(),
        };
    }
    PairPaint {
        note: unreachable_note(info, &shown, blocked_lead),
        url: String::new(),
        svg: String::new(),
        // The "other addresses are not the QR target" warning is folded
        // into the restart command below; as a standalone warning next to
        // no QR at all it only reads as a riddle.
        warning: String::new(),
        devices,
        device_note,
        blocked: true,
        restart: restart_command(&shown, &info.unbound),
    }
}

/// Whether the dialog may present `shown` as a scannable QR (and a copyable
/// pairing URL). A loopback host is never scannable: on a phone it resolves
/// to the phone itself (#1522). When the daemon reports the gateway as not
/// phone-reachable, the one scannable case left is the tunnel rewrite in
/// `shown_pair_url`, where `shown` was rebuilt from the non-loopback origin
/// the operator used to open this page — that origin is a reachable path
/// the daemon cannot see.
pub(crate) fn pair_url_scannable(shown: &str, phone_reachable: bool, page_origin: &str) -> bool {
    if shown.is_empty() || origin_is_loopback(shown) {
        return false;
    }
    if phone_reachable {
        return true;
    }
    let page = page_origin.trim_end_matches('/');
    !page.is_empty() && !origin_is_loopback(page) && shown.starts_with(page)
}

/// The lead text for the unreachable case: the daemon's own reachable-path
/// instructions (`optIn`), preceded by any mint error lead, and followed by
/// the plain statement the old dialog never made — no QR is shown, and why.
fn unreachable_note(info: &GatewayInfo, shown: &str, lead: &str) -> String {
    let mut parts: Vec<String> = Vec::new();
    if !lead.is_empty() {
        parts.push(lead.to_string());
    }
    if info.opt_in.is_empty() {
        parts.push(
            "A phone cannot open this gateway. It is listening on loopback only.".to_string(),
        );
    } else {
        parts.push(info.opt_in.clone());
    }
    parts.push(format!(
        "No QR is shown on purpose: it would point at {shown}, and a phone cannot open a loopback address — on the phone, that address is the phone itself. A pairing QR only works if the address in it is one your phone can reach."
    ));
    parts.join("\n\n")
}

/// The restart command offered when the gateway is loopback-only. It
/// mirrors the daemon's own `LoopbackOptIn` instruction (pair.go): bind a
/// private or Tailscale address the phone can reach. The first address the
/// daemon reported as not bound is used when there is one; the port
/// follows the gateway's own URL.
pub(crate) fn restart_command(shown: &str, unbound: &[String]) -> String {
    let ip = unbound
        .iter()
        .map(|s| s.trim())
        .find(|s| !s.is_empty())
        .unwrap_or("<tailnet-or-lan-ip>");
    let port = url_port(shown).unwrap_or_else(|| "8787".to_string());
    format!("strike daemon install --addr {ip}:{port} --allow-private-bind")
}

fn url_port(url: &str) -> Option<String> {
    let authority = url.split("://").nth(1)?.split(['/', '?', '#']).next()?;
    let port = if let Some(after) = authority.strip_prefix('[') {
        after.split_once(']')?.1.strip_prefix(':')?
    } else {
        authority.rsplit_once(':')?.1
    };
    if !port.is_empty() && port.chars().all(|c| c.is_ascii_digit()) {
        Some(port.to_string())
    } else {
        None
    }
}

async fn load_devices(base: &str, token: &str) -> (Vec<DeviceRow>, String) {
    match get_json::<DeviceList>(base, token, "/v1/pair/devices").await {
        Ok(list) => {
            if list.devices.is_empty() {
                (Vec::new(), "No paired browsers yet.".into())
            } else {
                (list.devices, String::new())
            }
        }
        Err(_) => (
            Vec::new(),
            "The device list needs the daemon token. A paired browser cannot list or revoke other devices.".into(),
        ),
    }
}

fn warnings(info: &GatewayInfo) -> String {
    let mut parts = Vec::new();
    if !info.lan_warning.is_empty() {
        parts.push(info.lan_warning.clone());
    }
    if !info.advertise_warning.is_empty() {
        parts.push(info.advertise_warning.clone());
    }
    if !info.unbound.is_empty() {
        parts.push(format!(
            "Other addresses on this machine are not the QR target: {}.",
            info.unbound.join(", ")
        ));
    }
    parts.join("\n\n")
}

fn plain_gateway_url(info: &GatewayInfo) -> String {
    info.urls
        .iter()
        .find(|u| u.kind == "tunnel")
        .or_else(|| info.urls.iter().find(|u| u.kind == "bind"))
        .map(|u| u.url.clone())
        .unwrap_or_default()
}

fn public_pair_error(err: &str) -> (String, Option<GatewayInfo>) {
    let code = json_error_code(err);
    let gateway = json_gateway(err);
    let note = match code.as_str() {
        "auth_disabled" => {
            "No pairing code was minted. This gateway is not running with --auth.".to_string()
        }
        "daemon_token_required" => {
            "A daemon token is required to mint a pairing code. A paired browser cannot mint one. Paste the daemon token in the Daemon view, or run strike gateway url --qr on the host.".to_string()
        }
        "too_many_codes" => {
            "Too many live pairing codes. Wait for one to expire, then try New code.".to_string()
        }
        "rate_limited" => "Too many pairing attempts. Wait a minute and try again.".to_string(),
        _ if err.contains("HTTP 401") || err.contains("unauthorized") => {
            "This page is not signed in to the daemon. Paste the daemon token in the Daemon view, or run strike gateway url --qr on the host.".to_string()
        }
        _ => "Could not mint a pairing code.".to_string(),
    };
    (note, gateway)
}

fn json_error_code(err: &str) -> String {
    json_body(err)
        .and_then(|v| v.get("error").and_then(|e| e.as_str()).map(str::to_string))
        .unwrap_or_default()
}

fn json_gateway(err: &str) -> Option<GatewayInfo> {
    json_body(err).and_then(|v| serde_json::from_value(v.get("gateway")?.clone()).ok())
}

fn json_body(err: &str) -> Option<serde_json::Value> {
    let i = err.find('{')?;
    serde_json::from_str(&err[i..]).ok()
}

fn compose_note(prefix: &str, extra: &str) -> String {
    match (prefix.is_empty(), extra.is_empty()) {
        (true, _) => extra.to_string(),
        (_, true) => prefix.to_string(),
        _ => format!("{prefix}\n\n{extra}"),
    }
}

async fn apply_cookie_redeem(
    base: &str,
    token: &str,
    code: &str,
    status: RwSignal<String>,
    landed: RwSignal<bool>,
    refresh: impl Fn() + Copy + 'static,
) {
    match redeem_cookie(base, token, code).await {
        Ok(()) => {
            landed.set(true);
            status.set("Paired with this gateway.".into());
            refresh();
        }
        Err(msg) => status.set(msg),
    }
}

async fn redeem_cookie(base: &str, token: &str, code: &str) -> Result<(), String> {
    let body = serde_json::json!({"code": code, "label": "browser", "delivery": "cookie"});
    match post_json_value(base, token, "/v1/pair/redeem", body).await {
        Ok(v) => {
            if v.get("sessionToken").and_then(|s| s.as_str()).is_some() {
                Err("Pairing did not stay in a cookie. This browser was not signed in.".into())
            } else {
                Ok(())
            }
        }
        Err(err) => Err(redeem_failure(&err)),
    }
}

async fn redeem_scanned(
    origin: String,
    code: String,
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    scan_note: RwSignal<String>,
    landed: RwSignal<bool>,
    refresh: impl Fn() + Copy + 'static,
) {
    let page = page_origin();
    let same = !page.is_empty() && origin_key(&page) == origin_key(&origin);
    if same {
        apply_cookie_redeem(
            &base.get_untracked(),
            &token.get_untracked(),
            &code,
            status,
            landed,
            refresh,
        )
        .await;
        if status.get_untracked() == "Paired with this gateway." {
            scan_note.set("Paired with this gateway.".into());
        } else {
            scan_note.set(status.get_untracked());
        }
        return;
    }
    let endpoint = format!("{origin}/v1/pair/redeem");
    let payload = serde_json::json!({"code": code, "label": "browser", "delivery": "token"});
    let sent = gloo_net::http::Request::post(&endpoint)
        .credentials(web_sys::RequestCredentials::Omit)
        .json(&payload);
    let req = match sent {
        Ok(req) => req,
        Err(_) => {
            scan_note.set("Could not exchange that pairing code.".into());
            return;
        }
    };
    let resp = match req.send().await {
        Ok(resp) => resp,
        Err(_) => {
            scan_note.set("Could not reach that gateway.".into());
            return;
        }
    };
    let status_code = resp.status();
    let text = resp.text().await.unwrap_or_default();
    if !(200..300).contains(&status_code) {
        scan_note.set(redeem_failure(&format!("HTTP {status_code} {text}")));
        return;
    }
    let Ok(v) = serde_json::from_str::<serde_json::Value>(&text) else {
        scan_note.set("That gateway did not return a session.".into());
        return;
    };
    let Some(session) = v.get("sessionToken").and_then(|s| s.as_str()) else {
        scan_note.set("That gateway did not return a session token for a native delivery.".into());
        return;
    };
    if session.is_empty() || text.contains("token=") {
        scan_note.set("That gateway returned an unsafe session.".into());
        return;
    }
    let session = session.to_string();
    crate::gateway::remember_paired_gateway(&origin, &session);
    base.set(origin.clone());
    token.set(session);
    status.set("Paired with another gateway.".into());
    scan_note.set("Paired with another gateway.".into());
    refresh();
}

fn redeem_failure(err: &str) -> String {
    match json_error_code(err).as_str() {
        "code_used" => "That pairing code was already used.".into(),
        "code_expired" => "That pairing code expired.".into(),
        "rate_limited" => "Too many pairing attempts. Wait a minute and try again.".into(),
        "query_secret" => {
            "A secret was sent in the query string. Pairing does not accept that.".into()
        }
        "auth_disabled" => {
            "No pairing code was minted. This gateway is not running with --auth.".into()
        }
        _ => "This pairing link was not accepted.".into(),
    }
}

fn page_origin() -> String {
    web_sys::window()
        .and_then(|w| w.location().origin().ok())
        .unwrap_or_default()
}

fn origin_key(origin: &str) -> String {
    origin.trim_end_matches('/').to_ascii_lowercase()
}

/// Pick the URL to draw. A loopback bind is not rewritten from the Host
/// header. When the operator already opened this page through a tunnel the
/// daemon does not know, and the bind is still loopback, the QR uses the
/// page origin so the phone hits the tunnel the human used.
pub(crate) fn shown_pair_url(server_url: &str, phone_reachable: bool, page_origin: &str) -> String {
    if server_url.is_empty() || server_url.contains("token=") || server_url.contains("?code=") {
        return String::new();
    }
    if phone_reachable || page_origin.is_empty() || origin_is_loopback(page_origin) {
        return server_url.to_string();
    }
    match server_url.split_once('#') {
        Some((_, frag))
            if frag.starts_with("pair=") && !frag.contains('&') && !frag.contains("token=") =>
        {
            format!("{}/#{frag}", page_origin.trim_end_matches('/'))
        }
        None => format!("{}/", page_origin.trim_end_matches('/')),
        _ => server_url.to_string(),
    }
}

pub(crate) fn origin_is_loopback(origin: &str) -> bool {
    let host = origin.split("://").nth(1).unwrap_or(origin);
    let host = host.split(['/', '?', '#']).next().unwrap_or(host);
    let host = if let Some(stripped) = host.strip_prefix('[') {
        stripped.split(']').next().unwrap_or(stripped)
    } else {
        host.split(':').next().unwrap_or(host)
    };
    host.eq_ignore_ascii_case("localhost") || host == "127.0.0.1" || host == "::1"
}

/// Read a scanned QR. Query secrets are rejected. The code stays in the
/// fragment and is not returned when the link is malformed.
pub(crate) fn scan_target(text: &str) -> Result<(String, String), &'static str> {
    let text = text.trim();
    if text.is_empty() {
        return Err("empty");
    }
    let head = text.split('#').next().unwrap_or(text);
    if query_has_secret(head) {
        return Err("secret-query");
    }
    let (before, hash) = text.split_once('#').ok_or("no-fragment")?;
    if query_has_secret(before) {
        return Err("secret-query");
    }
    let code = hash.strip_prefix("pair=").ok_or("no-pair")?;
    if code.is_empty()
        || code.contains('&')
        || code.contains('#')
        || code.contains(' ')
        || code.contains("token=")
    {
        return Err("bad-code");
    }
    let origin = http_origin(before).ok_or("origin")?;
    Ok((origin, code.to_string()))
}

fn query_has_secret(before_hash: &str) -> bool {
    let Some((_, query)) = before_hash.split_once('?') else {
        return false;
    };
    search_has_secret(&format!("?{query}"))
}

pub(crate) fn search_has_secret(search: &str) -> bool {
    let raw = search.trim_start_matches('?');
    if raw.is_empty() {
        return false;
    }
    raw.split('&').any(|part| {
        let key = part.split('=').next().unwrap_or("");
        matches!(key, "token" | "code" | "pair")
    })
}

pub(crate) fn strip_secret_search(search: &str) -> String {
    let raw = search.trim_start_matches('?');
    if raw.is_empty() {
        return String::new();
    }
    let kept: Vec<&str> = raw
        .split('&')
        .filter(|part| {
            let key = part.split('=').next().unwrap_or("");
            !matches!(key, "token" | "code" | "pair")
        })
        .collect();
    if kept.is_empty() {
        String::new()
    } else {
        format!("?{}", kept.join("&"))
    }
}

fn http_origin(before: &str) -> Option<String> {
    let (scheme, rest) = before.split_once("://")?;
    if scheme != "http" && scheme != "https" {
        return None;
    }
    let host = rest.split(['/', '?', '#']).next().unwrap_or("");
    if host.is_empty() || host.contains('@') {
        return None;
    }
    Some(format!("{scheme}://{host}"))
}

pub(crate) fn qr_markup(text: &str) -> String {
    if text.is_empty() || text.contains("token=") || text.contains("?code=") {
        return String::new();
    }
    let Ok(code) = qrcode::QrCode::new(text.as_bytes()) else {
        return String::new();
    };
    let svg = code
        .render::<qrcode::render::svg::Color>()
        .min_dimensions(220, 220)
        .dark_color(qrcode::render::svg::Color("#14131c"))
        .light_color(qrcode::render::svg::Color("#ffffff"))
        .quiet_zone(true)
        .build();
    if !svg_is_safe(&svg) {
        return String::new();
    }
    match svg.to_ascii_lowercase().find("<svg") {
        Some(at) => svg[at..].to_string(),
        None => String::new(),
    }
}

fn svg_is_safe(svg: &str) -> bool {
    let lower = svg.to_ascii_lowercase();
    let Some(at) = lower.find("<svg") else {
        return false;
    };
    at < 80
        && !lower.contains("<script")
        && !lower.contains("javascript:")
        && !lower.contains("<foreignobject")
}

fn consume_pair_fragment() -> Option<String> {
    let win = web_sys::window()?;
    let loc = win.location();
    let hash = loc.hash().unwrap_or_default();
    let search = loc.search().unwrap_or_default();
    let path = loc.pathname().unwrap_or_else(|_| "/".into());
    let cleaned = strip_secret_search(&search);
    if hash.starts_with("#pair=") || search_has_secret(&search) {
        if let Ok(history) = win.history() {
            let next = if cleaned.is_empty() {
                path
            } else {
                format!("{path}{cleaned}")
            };
            let _ = history.replace_state_with_url(&JsValue::NULL, "", Some(&next));
        }
    }
    if search_has_secret(&search) {
        return None;
    }
    let code = hash.strip_prefix("#pair=")?.trim();
    if code.is_empty() || code.contains('&') || code.contains("token=") {
        return None;
    }
    Some(code.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn shown_url_keeps_loopback_and_rewrites_tunnel_page() {
        let server = "http://127.0.0.1:8787/#pair=p1.abc";
        assert_eq!(
            shown_pair_url(server, false, "http://127.0.0.1:8787"),
            server
        );
        assert_eq!(
            shown_pair_url(server, false, "https://lab.example"),
            "https://lab.example/#pair=p1.abc"
        );
        assert_eq!(shown_pair_url(server, true, "https://lab.example"), server);
        assert_eq!(
            shown_pair_url(
                "http://127.0.0.1:8787/?token=secret",
                false,
                "http://127.0.0.1:8787"
            ),
            ""
        );
    }

    #[test]
    fn scan_rejects_query_secrets_and_reads_fragment() {
        let (origin, code) = scan_target("http://127.0.0.1:8787/#pair=p1.abc").unwrap();
        assert_eq!(origin, "http://127.0.0.1:8787");
        assert_eq!(code, "p1.abc");
        assert_eq!(
            scan_target("http://127.0.0.1:8787/?token=secret#pair=p1.abc"),
            Err("secret-query")
        );
        assert_eq!(
            scan_target("http://127.0.0.1:8787/?code=p1.abc"),
            Err("secret-query")
        );
        assert!(scan_target("http://127.0.0.1:8787/").is_err());
    }

    #[test]
    fn strip_drops_secret_keys_only() {
        assert_eq!(
            strip_secret_search("?tab=chat&token=abc&x=1"),
            "?tab=chat&x=1"
        );
        assert_eq!(strip_secret_search("?pair=1"), "");
        assert!(search_has_secret("?code=1"));
        assert!(!search_has_secret("?tab=chat"));
    }

    #[test]
    fn qr_svg_is_geometry_without_script() {
        let svg = qr_markup("http://127.0.0.1:8787/");
        assert!(svg.starts_with("<svg"), "len {}", svg.len());
        assert!(!svg.to_ascii_lowercase().contains("<script"));
        assert!(qr_markup("http://127.0.0.1:8787/?token=secret").is_empty());
    }

    #[test]
    fn loopback_hosts() {
        assert!(origin_is_loopback("http://127.0.0.1:8787"));
        assert!(origin_is_loopback("http://[::1]:8787"));
        assert!(origin_is_loopback("http://localhost"));
        assert!(!origin_is_loopback("https://lab.example"));
    }

    #[test]
    fn scannable_requires_a_phone_reachable_target() {
        let page = "http://127.0.0.1:8787";
        // A loopback target is never scannable, whatever the flags say:
        // on a phone it resolves to the phone itself (#1522).
        assert!(!pair_url_scannable(
            "http://127.0.0.1:8787/#pair=p1.abc",
            false,
            page
        ));
        assert!(!pair_url_scannable(
            "http://127.0.0.1:8787/#pair=p1.abc",
            true,
            page
        ));
        assert!(!pair_url_scannable("http://localhost:8787/", false, page));
        assert!(!pair_url_scannable("", true, page));
        // A reachable bind keeps its QR.
        assert!(pair_url_scannable(
            "http://192.168.1.20:8787/#pair=p1.abc",
            true,
            page
        ));
        assert!(pair_url_scannable(
            "https://lab.example/#pair=p1.abc",
            true,
            page
        ));
        // Daemon says unreachable, but the URL was rewritten to the tunnel
        // origin the operator opened this page through: scannable.
        assert!(pair_url_scannable(
            "https://lab.example/#pair=p1.abc",
            false,
            "https://lab.example"
        ));
        // Daemon says unreachable and the URL is some other non-loopback
        // host the page did not come from: not scannable.
        assert!(!pair_url_scannable(
            "http://192.168.1.20:8787/",
            false,
            page
        ));
    }

    #[test]
    fn restart_command_uses_unbound_ip_and_gateway_port() {
        assert_eq!(
            restart_command(
                "http://127.0.0.1:8787/#pair=p1.abc",
                &["198.19.0.2".to_string()]
            ),
            "strike daemon install --addr 198.19.0.2:8787 --allow-private-bind"
        );
        assert_eq!(
            restart_command("http://127.0.0.1:9999/", &[]),
            "strike daemon install --addr <tailnet-or-lan-ip>:9999 --allow-private-bind"
        );
        assert_eq!(
            restart_command("http://[::1]:8787/", &["  ".to_string()]),
            "strike daemon install --addr <tailnet-or-lan-ip>:8787 --allow-private-bind"
        );
    }

    fn loopback_info() -> GatewayInfo {
        GatewayInfo {
            phone_reachable: false,
            urls: vec![GatewayURL {
                kind: "bind".into(),
                url: "http://127.0.0.1:8787/".into(),
            }],
            opt_in: "A phone cannot open this gateway. It is listening on loopback only.".into(),
            unbound: vec!["198.19.0.2".into()],
            ..Default::default()
        }
    }

    #[test]
    fn loopback_mint_paint_hides_the_qr() {
        // The --auth variant was the worse half of #1522: it showed the
        // loopback QR with no warning at all.
        let paint = pair_paint(
            &loopback_info(),
            "http://127.0.0.1:8787/#pair=p1.abc",
            "http://127.0.0.1:8787",
            "ttl note".into(),
            "",
            Vec::new(),
            String::new(),
        );
        assert!(paint.blocked);
        assert!(
            paint.svg.is_empty(),
            "no scannable QR for a loopback target"
        );
        assert!(paint.url.is_empty(), "no copyable loopback URL");
        assert!(
            paint.note.contains("cannot open this gateway"),
            "{}",
            paint.note
        );
        assert!(
            paint
                .note
                .contains("only works if the address in it is one your phone can reach"),
            "{}",
            paint.note
        );
        assert_eq!(
            paint.restart,
            "strike daemon install --addr 198.19.0.2:8787 --allow-private-bind"
        );
        assert!(
            paint.warning.is_empty(),
            "the unbound-address note must not survive as a cryptic warning: {}",
            paint.warning
        );
    }

    #[test]
    fn loopback_public_paint_hides_the_qr() {
        // The no-auth variant already said "cannot open" — and then showed
        // the QR anyway.
        let paint = pair_paint(
            &loopback_info(),
            "http://127.0.0.1:8787/",
            "http://127.0.0.1:8787",
            "opt-in plus error".into(),
            "No pairing code was minted. This gateway is not running with --auth.",
            Vec::new(),
            String::new(),
        );
        assert!(paint.blocked);
        assert!(paint.svg.is_empty());
        assert!(paint.url.is_empty());
        assert!(
            paint.note.starts_with("No pairing code was minted."),
            "{}",
            paint.note
        );
        assert!(
            paint.note.contains("cannot open this gateway"),
            "{}",
            paint.note
        );
    }

    #[test]
    fn reachable_paint_keeps_the_qr() {
        let info = GatewayInfo {
            phone_reachable: true,
            urls: vec![GatewayURL {
                kind: "bind".into(),
                url: "http://192.168.1.20:8787/".into(),
            }],
            lan_warning: "lan warning".into(),
            ..Default::default()
        };
        let paint = pair_paint(
            &info,
            "http://192.168.1.20:8787/#pair=p1.abc",
            "http://192.168.1.20:8787",
            "ttl note".into(),
            "",
            Vec::new(),
            String::new(),
        );
        assert!(!paint.blocked);
        assert!(paint.svg.starts_with("<svg"), "reachable QR must render");
        assert_eq!(paint.url, "http://192.168.1.20:8787/#pair=p1.abc");
        assert_eq!(paint.note, "ttl note");
        assert_eq!(paint.warning, "lan warning");
        assert!(paint.restart.is_empty());
    }
}
