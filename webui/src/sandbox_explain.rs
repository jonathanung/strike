//! Read-only OS sandbox section for the Permissions panel (#1532).
//! Every value comes from GET /v1/sandbox. Nothing here changes the sandbox:
//! the mode dial is a separate, security-reviewed follow-up.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;

use crate::api::get_json;

/// Mirror of the daemon's `SandboxSnapshot` (internal/frontend/server/live.go).
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct SandboxSnapshot {
    #[serde(default)]
    pub mode: String,
    #[serde(default)]
    pub backend: String,
    #[serde(default)]
    pub available: bool,
    #[serde(rename = "networkAllow", default)]
    pub network_allow: Vec<String>,
    #[serde(default)]
    pub explain: String,
    #[serde(rename = "defaultMode", default)]
    pub default_mode: String,
}

/// harness/sandbox BackendUnprobed: status assembled before the first
/// sandboxed command checked the platform backend. Availability is unknown,
/// not false.
pub const BACKEND_UNPROBED: &str = "unprobed";

pub fn is_unprobed(s: &SandboxSnapshot) -> bool {
    s.backend == BACKEND_UNPROBED
}

/// data-sandbox-state for a loaded snapshot.
pub fn snapshot_state(s: &SandboxSnapshot) -> &'static str {
    if s.available {
        "ready"
    } else if is_unprobed(s) {
        "unprobed"
    } else {
        "no-backend"
    }
}

/// What the section renders. `Unavailable` is a first-class state: the
/// daemon has no sandbox capability, so no mode is shown at all.
#[derive(Clone, Debug, PartialEq)]
pub enum SandboxLoad {
    Loading,
    Ready(SandboxSnapshot),
    Unavailable,
    Failed(String),
}

/// Maps the GET result to a render state. The daemon answers 501 when the
/// sandbox capability is not wired on this host (`capabilityUnavailable`).
pub fn classify(res: Result<SandboxSnapshot, String>) -> SandboxLoad {
    match res {
        Ok(snap) => SandboxLoad::Ready(snap),
        Err(e) if e.starts_with("HTTP 501") => SandboxLoad::Unavailable,
        Err(e) => SandboxLoad::Failed(e),
    }
}

pub fn mode_label(mode: &str) -> String {
    match mode {
        "off" => "Off".into(),
        "read-only" => "Read-only".into(),
        "workspace-write" => "Workspace write".into(),
        "" => "Not reported".into(),
        other => other.into(),
    }
}

fn mode_meaning(mode: &str) -> &'static str {
    match mode {
        "off" => "Commands run without OS confinement.",
        "read-only" => "Commands can read files but cannot write anywhere.",
        "workspace-write" => "Commands can write inside the workspace only.",
        _ => "",
    }
}

/// One labelled row of the structured summary.
#[derive(Clone, Debug, PartialEq)]
pub struct Row {
    pub label: &'static str,
    pub value: String,
}

/// Structured summary rows, built only from snapshot fields.
pub fn summary_rows(s: &SandboxSnapshot) -> Vec<Row> {
    let mut rows = Vec::new();
    let mut mode = mode_label(&s.mode);
    let meaning = mode_meaning(&s.mode);
    if !meaning.is_empty() {
        mode = format!("{mode}. {meaning}");
    }
    rows.push(Row {
        label: if s.available {
            "Active sandbox (this session)"
        } else {
            "Configured mode"
        },
        value: mode,
    });
    let unprobed = is_unprobed(s);
    rows.push(Row {
        label: "Backend",
        value: if unprobed {
            "Not checked yet".into()
        } else if s.backend.is_empty() {
            "None reported".into()
        } else {
            s.backend.clone()
        },
    });
    rows.push(Row {
        label: "Backend available",
        value: if s.available {
            "Yes".into()
        } else if unprobed {
            "Unknown (checked on the first sandboxed command)".into()
        } else {
            "No".into()
        },
    });
    if !s.network_allow.is_empty() {
        rows.push(Row {
            label: "Network allowed",
            value: s.network_allow.join(", "),
        });
    }
    rows
}

/// Shown when confinement is not confirmed: the backend was not probed yet,
/// or was probed and reported unavailable. The configured mode is never
/// presented as live confinement. Off needs no warning (no confinement).
pub fn unavailable_backend_copy(s: &SandboxSnapshot) -> Option<String> {
    if s.available || s.mode == "off" {
        return None;
    }
    let mode = mode_label(&s.mode);
    if is_unprobed(s) {
        return Some(format!(
            "The sandbox backend has not been checked yet; it is checked when the first sandboxed command runs. {mode} is the configured mode; confinement is not confirmed yet."
        ));
    }
    Some(format!(
        "The daemon reports no working sandbox backend on this host. {mode} is the configured mode, not confirmed confinement."
    ))
}

/// Shown only when the saved default differs from the active mode. Changing
/// the default never affects the running session (daemon note).
pub fn default_differs_copy(s: &SandboxSnapshot) -> Option<String> {
    if s.default_mode.is_empty() || s.default_mode == s.mode {
        return None;
    }
    Some(format!(
        "Saved default: {}. Only new sessions start with it. This session keeps {}.",
        mode_label(&s.default_mode),
        mode_label(&s.mode)
    ))
}

#[component]
pub fn SandboxSection(base: RwSignal<String>, token: RwSignal<String>) -> impl IntoView {
    let state = RwSignal::new(SandboxLoad::Loading);
    let (b, t) = (base.get_untracked(), token.get_untracked());
    spawn_local(async move {
        let res = get_json::<SandboxSnapshot>(&b, &t, "/v1/sandbox").await;
        // The panel may close before the fetch resolves.
        let _ = state.try_set(classify(res));
    });

    view! {
        <section class="sandbox-explain" data-section="sandbox" aria-labelledby="sandbox-explain-title">
            <h3 id="sandbox-explain-title">"OS sandbox"</h3>
            <p class="hint">"Rules decide which tool calls may run. The OS sandbox limits what a running command can touch."</p>
            {move || match state.get() {
                SandboxLoad::Loading => view! { <p class="hint">"Loading sandbox state…"</p> }.into_any(),
                SandboxLoad::Unavailable => view! {
                    <p class="notice" data-sandbox-state="unavailable">
                        "This daemon does not report sandbox state, so the cockpit cannot show which sandbox mode is active."
                    </p>
                }.into_any(),
                SandboxLoad::Failed(e) => view! {
                    <p class="notice" data-sandbox-state="error">{format!("Could not load sandbox state: {e}")}</p>
                }.into_any(),
                SandboxLoad::Ready(snap) => {
                    let rows = summary_rows(&snap);
                    let backend_warn = unavailable_backend_copy(&snap);
                    let differs = default_differs_copy(&snap);
                    let explain = snap.explain.clone();
                    view! {
                        <dl class="sandbox-summary" data-sandbox-state={snapshot_state(&snap)}>
                            {rows.into_iter().map(|r| view! {
                                <div class="sandbox-row">
                                    <dt>{r.label}</dt>
                                    <dd>{r.value}</dd>
                                </div>
                            }).collect_view()}
                        </dl>
                        {backend_warn.map(|w| view! { <p class="notice">{w}</p> })}
                        {differs.map(|d| view! { <p class="hint" data-sandbox-default="differs">{d}</p> })}
                        {(!explain.is_empty()).then(|| view! {
                            <details class="sandbox-profile">
                                <summary>"Full sandbox profile"</summary>
                                <pre class="output">{explain}</pre>
                            </details>
                        })}
                    }.into_any()
                }
            }}
        </section>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn snap(mode: &str, available: bool) -> SandboxSnapshot {
        SandboxSnapshot {
            mode: mode.into(),
            backend: if available { "bwrap".into() } else { String::new() },
            available,
            explain: "profile line 1\nprofile line 2".into(),
            ..Default::default()
        }
    }

    #[test]
    fn decodes_daemon_snapshot() {
        let raw = r#"{"mode":"read-only","backend":"bwrap","available":true,"networkAllow":["github.com"],"explain":"x","defaultMode":"workspace-write","permissionMode":"default","note":"n","modes":["off","read-only","workspace-write"],"canChangeDefault":true}"#;
        let s: SandboxSnapshot = serde_json::from_str(raw).unwrap();
        assert_eq!(s.mode, "read-only");
        assert_eq!(s.network_allow, vec!["github.com".to_string()]);
        assert_eq!(s.default_mode, "workspace-write");
        assert!(s.available);
    }

    #[test]
    fn classify_maps_501_to_unavailable() {
        assert_eq!(classify(Err("HTTP 501".into())), SandboxLoad::Unavailable);
        assert_eq!(
            classify(Err("HTTP 500".into())),
            SandboxLoad::Failed("HTTP 500".into())
        );
        assert_eq!(
            classify(Ok(snap("off", true))),
            SandboxLoad::Ready(snap("off", true))
        );
    }

    #[test]
    fn each_mode_has_a_label_and_meaning() {
        for (mode, label) in [
            ("off", "Off"),
            ("read-only", "Read-only"),
            ("workspace-write", "Workspace write"),
        ] {
            let rows = summary_rows(&snap(mode, true));
            assert_eq!(rows[0].label, "Active sandbox (this session)");
            assert!(rows[0].value.starts_with(label), "{mode}: {:?}", rows[0]);
            assert!(rows[0].value.len() > label.len(), "{mode} lacks meaning");
        }
        assert_eq!(mode_label("future-mode"), "future-mode");
    }

    #[test]
    fn network_row_only_when_present() {
        let mut s = snap("workspace-write", true);
        assert!(summary_rows(&s).iter().all(|r| r.label != "Network allowed"));
        s.network_allow = vec!["github.com".into(), "proxy.golang.org".into()];
        let rows = summary_rows(&s);
        let net = rows.iter().find(|r| r.label == "Network allowed").unwrap();
        assert_eq!(net.value, "github.com, proxy.golang.org");
    }

    #[test]
    fn default_differs_only_when_different() {
        let mut s = snap("read-only", true);
        assert_eq!(default_differs_copy(&s), None);
        s.default_mode = "read-only".into();
        assert_eq!(default_differs_copy(&s), None);
        s.default_mode = "off".into();
        let copy = default_differs_copy(&s).unwrap();
        assert!(copy.contains("Saved default: Off"));
        assert!(copy.contains("This session keeps Read-only"));
    }

    #[test]
    fn no_backend_is_not_presented_as_live() {
        let s = snap("workspace-write", false);
        let rows = summary_rows(&s);
        assert_eq!(rows[0].label, "Configured mode");
        assert_eq!(rows[1].value, "None reported");
        assert_eq!(rows[2].value, "No");
        let warn = unavailable_backend_copy(&s).unwrap();
        assert!(warn.contains("not confirmed confinement"));
        assert_eq!(unavailable_backend_copy(&snap("off", true)), None);
        assert_eq!(unavailable_backend_copy(&snap("off", false)), None);
        assert_eq!(snapshot_state(&s), "no-backend");
        assert_eq!(snapshot_state(&snap("read-only", true)), "ready");
    }

    #[test]
    fn unprobed_backend_is_unknown_not_unavailable() {
        let mut s = snap("workspace-write", false);
        s.backend = BACKEND_UNPROBED.into();
        assert_eq!(snapshot_state(&s), "unprobed");
        let rows = summary_rows(&s);
        assert_eq!(rows[0].label, "Configured mode");
        assert_eq!(rows[1].value, "Not checked yet");
        assert!(rows[2].value.starts_with("Unknown"));
        let warn = unavailable_backend_copy(&s).unwrap();
        assert!(warn.contains("not been checked yet"));
        assert!(!warn.contains("no working sandbox backend"));
    }
}
