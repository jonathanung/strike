//! Live preview pane (#1341). feature:live-preview
//!
//! Side-panel tab that shows a dev server running on the gateway host
//! through the daemon's loopback proxy (`/v1/preview*`, `/preview/<key>/`).
//! The user picks a detected port or types one. The agent cannot drive
//! this pane. The frame is sandboxed without allow-same-origin, matching
//! the CSP sandbox the daemon puts on every proxied response, and the
//! destination label always comes from the daemon's reply for the port it
//! actually proxies.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;

use crate::api::{api, delete_json, get_json, put_json_value};

/// The iframe sandbox. Must not include allow-same-origin: the proxied
/// app would share the cockpit's origin and could act as the user.
pub const PREVIEW_SANDBOX: &str = "allow-scripts allow-forms allow-popups allow-modals allow-downloads";

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct PreviewPort {
    #[serde(default)]
    pub port: u16,
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct PreviewPorts {
    #[serde(default)]
    pub detection: String,
    #[serde(default)]
    pub ports: Vec<PreviewPort>,
    /// The gateway's own port (never selectable).
    #[serde(default, rename = "self")]
    pub self_port: u16,
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PreviewProbe {
    #[serde(default)]
    pub reachable: bool,
    #[serde(default)]
    pub status: u16,
    #[serde(default)]
    pub error: String,
    #[serde(default)]
    pub framing: String,
    #[serde(default)]
    pub framing_header: String,
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct PreviewTarget {
    #[serde(default)]
    pub selected: bool,
    #[serde(default)]
    pub port: u16,
    #[serde(default)]
    pub origin: String,
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub probe: Option<PreviewProbe>,
}

/// What the pane body shows for a target.
#[derive(Debug, Clone, PartialEq)]
enum PaneState {
    Empty,
    Unreachable(String),
    FramingRefused(String),
    Frame,
}

fn pane_state(t: &PreviewTarget) -> PaneState {
    if !t.selected || t.path.is_empty() {
        return PaneState::Empty;
    }
    match &t.probe {
        Some(p) if !p.reachable => PaneState::Unreachable(if p.error.is_empty() {
            format!("Nothing answered on {}.", destination_label(t))
        } else {
            capitalize(&p.error)
        }),
        Some(p) if p.framing == "refused" => PaneState::FramingRefused(format!(
            "This app refuses to be framed ({}). Open it in the browser instead.",
            p.framing_header
        )),
        _ => PaneState::Frame,
    }
}

fn capitalize(s: &str) -> String {
    let mut c = s.chars();
    match c.next() {
        Some(f) => f.to_uppercase().collect::<String>() + c.as_str(),
        None => String::new(),
    }
}

/// "127.0.0.1:5173" from the daemon's origin for the proxied port.
fn destination_label(t: &PreviewTarget) -> String {
    t.origin
        .trim_start_matches("http://")
        .trim_start_matches("https://")
        .to_string()
}

/// Parses the manual port field. The daemon validates again.
fn parse_port(raw: &str) -> Result<u16, String> {
    match raw.trim().parse::<u32>() {
        Ok(n) if (1..=65535).contains(&n) => Ok(n as u16),
        _ => Err("Enter a port from 1 to 65535.".into()),
    }
}

/// Viewport presets: (id, label, frame class).
const PRESETS: [(&str, &str); 3] = [("phone", "Phone"), ("tablet", "Tablet"), ("desktop", "Desktop")];

fn frame_class(preset: &str) -> String {
    let p = if PRESETS.iter().any(|(id, _)| *id == preset) { preset } else { "desktop" };
    format!("preview-frame preview-{p}")
}

#[component]
pub fn PreviewPanel(base: RwSignal<String>, token: RwSignal<String>) -> impl IntoView {
    let ports = RwSignal::new(PreviewPorts::default());
    let target = RwSignal::new(PreviewTarget::default());
    let error = RwSignal::new(String::new());
    let manual = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let preset = RwSignal::new("desktop".to_string());
    let reloads = RwSignal::new(0u32);

    let refresh_ports = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match get_json::<PreviewPorts>(&b, &t, "/v1/preview/ports").await {
                Ok(p) => ports.set(p),
                Err(e) => error.set(format!("Could not list ports: {e}")),
            }
        });
    };
    let load_current = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Ok(cur) = get_json::<PreviewTarget>(&b, &t, "/v1/preview?probe=1").await {
                target.set(cur);
            }
        });
    };
    refresh_ports();
    load_current();

    let select = move |port: u16| {
        if busy.get_untracked() {
            return;
        }
        busy.set(true);
        error.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match put_json_value(&b, &t, "/v1/preview", serde_json::json!({ "port": port })).await {
                Ok(v) => match serde_json::from_value::<PreviewTarget>(v) {
                    Ok(cur) => {
                        target.set(cur);
                        reloads.update(|n| *n += 1);
                    }
                    Err(e) => error.set(format!("Unexpected preview reply: {e}")),
                },
                Err(e) => error.set(format!("Preview refused: {e}")),
            }
            busy.set(false);
        });
    };
    let select_manual = move || match parse_port(&manual.get_untracked()) {
        Ok(p) => select(p),
        Err(e) => error.set(e),
    };
    let stop = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match delete_json(&b, &t, "/v1/preview", None).await {
                Ok(()) => target.set(PreviewTarget::default()),
                Err(e) => error.set(format!("Could not stop the preview: {e}")),
            }
        });
    };
    let reload = move || {
        let cur = target.get_untracked();
        if cur.selected {
            // Re-probe so a server that went away is reported, not blank.
            select(cur.port);
        }
    };

    view! {
        <div class="preview-panel" data-panel="preview">
            <div class="preview-pick">
                <span class="hint">"Listening on this gateway"</span>
                <div class="chips" role="group" aria-label="Detected ports">
                    {move || {
                        let p = ports.get();
                        if p.ports.is_empty() {
                            let msg = if p.detection == "unsupported" {
                                "Port detection is not available on this host. Type a port."
                            } else {
                                "No loopback servers found. Start one, then refresh."
                            };
                            return view! { <span class="hint" data-preview-ports-empty="true">{msg}</span> }.into_any();
                        }
                        p.ports.into_iter().map(|pp| {
                            let port = pp.port;
                            view! {
                                <button type="button"
                                    class=move || if target.get().port == port { "chip on" } else { "chip" }
                                    data-preview-port=port.to_string()
                                    disabled=move || busy.get()
                                    on:click=move |_| select(port)>
                                    {port.to_string()}
                                </button>
                            }
                        }).collect_view().into_any()
                    }}
                    <button type="button" class="secondary" data-action="preview-refresh-ports" on:click=move |_| refresh_ports()>"Refresh"</button>
                </div>
                <form class="preview-manual" on:submit=move |ev: web_sys::SubmitEvent| { ev.prevent_default(); select_manual(); }>
                    <label>"Port "
                        <input type="text" inputmode="numeric" size="6" data-preview-manual="true"
                            prop:value=move || manual.get()
                            on:input=move |ev| manual.set(event_target_value(&ev)) />
                    </label>
                    <button type="submit" class="secondary" data-action="preview-open" disabled=move || busy.get()>"Preview"</button>
                </form>
            </div>
            <Show when=move || !error.get().is_empty()>
                <p class="banner" role="alert" data-preview-error="true">{move || error.get()}</p>
            </Show>
            {move || {
                let cur = target.get();
                let state = pane_state(&cur);
                if state == PaneState::Empty {
                    return view! {
                        <p class="hint" data-preview-empty="true">"Pick a port to preview a server running on the gateway host. Only loopback servers are proxied, and only the one you pick."</p>
                    }.into_any();
                }
                let label = destination_label(&cur);
                let href = api(&base.get_untracked(), &cur.path);
                let src = href.clone();
                let prefix = cur.path.clone();
                let body = match state {
                    PaneState::Unreachable(msg) => view! {
                        <p class="banner" role="alert" data-preview-unreachable="true">{msg}</p>
                    }.into_any(),
                    PaneState::FramingRefused(msg) => view! {
                        <p class="banner" role="alert" data-preview-framing="refused">{msg}</p>
                    }.into_any(),
                    _ => view! {
                        <div class="preview-stage">
                            {move || {
                                let _ = reloads.get();
                                view! {
                                    <iframe class=move || frame_class(&preset.get())
                                        title=format!("Preview of {}", label.clone())
                                        src=src.clone()
                                        sandbox=PREVIEW_SANDBOX
                                        referrerpolicy="no-referrer"></iframe>
                                }
                            }}
                        </div>
                    }.into_any(),
                };
                view! {
                    <div class="preview-chrome">
                        <span class="preview-dest" data-preview-destination=destination_label(&cur)>
                            {destination_label(&cur)}
                        </span>
                        <div class="chips" role="group" aria-label="Viewport width">
                            {PRESETS.iter().map(|(id, name)| {
                                let id = id.to_string();
                                let on = id.clone();
                                let pick = id.clone();
                                view! {
                                    <button type="button"
                                        class=move || if preset.get() == on { "chip on" } else { "chip" }
                                        data-preview-preset=id.clone()
                                        on:click=move |_| preset.set(pick.clone())>{*name}</button>
                                }
                            }).collect_view()}
                        </div>
                        <span class="row-actions">
                            <button type="button" class="secondary" data-action="preview-reload" on:click=move |_| reload()>"Reload"</button>
                            <a class="button secondary" data-action="preview-open-browser" href=href target="_blank" rel="noopener noreferrer">"Open in browser"</a>
                            <button type="button" class="secondary" data-action="preview-stop" on:click=move |_| stop()>"Stop"</button>
                        </span>
                    </div>
                    <p class="hint">{format!("Served at {prefix}. Apps that load files from absolute paths need this as their base path.")}</p>
                    {body}
                }.into_any()
            }}
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn target(probe: Option<PreviewProbe>) -> PreviewTarget {
        PreviewTarget {
            selected: true,
            port: 5173,
            origin: "http://127.0.0.1:5173".into(),
            path: "/preview/abc/".into(),
            probe,
        }
    }

    #[test]
    fn label_comes_from_the_daemon_origin() {
        assert_eq!(destination_label(&target(None)), "127.0.0.1:5173");
    }

    #[test]
    fn unreachable_and_refused_never_render_a_blank_frame() {
        assert_eq!(pane_state(&PreviewTarget::default()), PaneState::Empty);
        let down = target(Some(PreviewProbe { reachable: false, error: "nothing answered on 127.0.0.1:5173 (connection refused)".into(), ..Default::default() }));
        assert_eq!(
            pane_state(&down),
            PaneState::Unreachable("Nothing answered on 127.0.0.1:5173 (connection refused)".into())
        );
        let deny = target(Some(PreviewProbe { reachable: true, framing: "refused".into(), framing_header: "X-Frame-Options: DENY".into(), ..Default::default() }));
        assert!(matches!(pane_state(&deny), PaneState::FramingRefused(m) if m.contains("DENY")));
        let ok = target(Some(PreviewProbe { reachable: true, status: 200, framing: "ok".into(), ..Default::default() }));
        assert_eq!(pane_state(&ok), PaneState::Frame);
    }

    #[test]
    fn manual_port_bounds() {
        assert_eq!(parse_port(" 3000 "), Ok(3000));
        assert!(parse_port("0").is_err());
        assert!(parse_port("65536").is_err());
        assert!(parse_port("localhost:3000").is_err());
    }

    #[test]
    fn sandbox_never_grants_same_origin() {
        assert!(!PREVIEW_SANDBOX.contains("allow-same-origin"));
        assert!(PREVIEW_SANDBOX.contains("allow-scripts"));
    }

    #[test]
    fn frame_class_falls_back_to_desktop() {
        assert_eq!(frame_class("phone"), "preview-frame preview-phone");
        assert_eq!(frame_class("weird"), "preview-frame preview-desktop");
    }
}
