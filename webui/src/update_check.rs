//! Update check (#1583): the Settings autoupdate row and the cockpit's
//! read of `GET /v1/update`. Check-only: the web never installs, even when
//! the dial is `auto`. The daemon's probe cache bounds how often GitHub is
//! contacted; the dial `off` makes the route answer without any network
//! request. The notification centre turns an `available` result into one
//! dismissible row per release (see `notify::update_note`).

use std::cell::RefCell;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::Value;

use crate::api::{get_json, patch_json};

/// One `GET /v1/update` reply (also `settings.updateCheck`).
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct UpdateCheck {
    #[serde(default)]
    pub mode: String,
    /// off|available|current|failed|unknown
    #[serde(default)]
    pub state: String,
    #[serde(default)]
    pub current: String,
    #[serde(default)]
    pub latest: String,
    #[serde(default)]
    pub available: bool,
    #[serde(default)]
    pub message: String,
    #[serde(default)]
    pub error: String,
    #[serde(default, rename = "checkedAt")]
    pub checked_at: String,
    #[serde(default, rename = "canReplace")]
    pub can_replace: bool,
}

thread_local! {
    // Arc signals are reference counted, not owned by whichever component
    // first touched them, so they outlive Settings and the bell remounting.
    static CHECK: ArcRwSignal<Option<UpdateCheck>> = ArcRwSignal::new(None);
    static CAPABLE: ArcRwSignal<bool> = ArcRwSignal::new(false);
    static CHECKED_BASE: RefCell<Option<String>> = const { RefCell::new(None) };
}

/// Latest update-check result for the current gateway (None = not checked).
pub fn signal() -> ArcRwSignal<Option<UpdateCheck>> {
    CHECK.with(|s| s.clone())
}

pub fn capable() -> ArcRwSignal<bool> {
    CAPABLE.with(|s| s.clone())
}

fn publish(check: Option<UpdateCheck>) {
    CHECK.with(|s| s.set(check));
}

pub async fn fetch(base: &str, token: &str) -> Result<UpdateCheck, String> {
    get_json::<UpdateCheck>(base, token, "/v1/update").await
}

/// Called from each bootstrap. Probes once per gateway base: a gateway
/// switch drops the old daemon's result and checks the new one. Repeat
/// bootstraps for the same base do nothing, so page use never nags.
pub fn boot(base: String, token: String, cap: bool) {
    CAPABLE.with(|s| {
        if s.get_untracked() != cap {
            s.set(cap);
        }
    });
    let fresh = CHECKED_BASE.with(|slot| {
        let mut slot = slot.borrow_mut();
        if slot.as_deref() == Some(base.as_str()) {
            false
        } else {
            *slot = Some(base.clone());
            true
        }
    });
    if !fresh {
        return;
    }
    publish(None);
    if !cap {
        return;
    }
    spawn_local(async move {
        let result = fetch(&base, &token).await;
        // A later gateway switch owns the slot now; drop this reply.
        if CHECKED_BASE.with(|slot| slot.borrow().as_deref() != Some(base.as_str())) {
            return;
        }
        publish(Some(match result {
            Ok(check) => check,
            Err(e) => failed(e),
        }));
    });
}

fn failed(error: String) -> UpdateCheck {
    UpdateCheck {
        state: "failed".into(),
        error,
        ..UpdateCheck::default()
    }
}

/// One line for the Settings row. Failures are never "up to date".
pub fn status_line(check: Option<&UpdateCheck>, capable: bool) -> String {
    let Some(c) = check else {
        return if capable {
            "Not checked yet.".into()
        } else {
            "This daemon does not offer update checks.".into()
        };
    };
    let checked = if c.checked_at.is_empty() {
        String::new()
    } else {
        format!(" Checked {}.", c.checked_at)
    };
    match c.state.as_str() {
        "off" => "Checks are off. This daemon makes no update requests.".into(),
        "available" => {
            let running = if c.current.is_empty() {
                String::new()
            } else {
                format!(" (this gateway runs {})", c.current)
            };
            format!(
                "Strike {} is available{running}. Install it on the host with strike upgrade.{checked}",
                if c.latest.is_empty() { "update" } else { &c.latest }
            )
        }
        "current" => {
            let v = if c.current.is_empty() { String::new() } else { format!(" ({})", c.current) };
            format!("Up to date{v}.{checked}")
        }
        "failed" => {
            let why = if c.error.is_empty() { "unknown error" } else { &c.error };
            format!("Update check failed: {why}. Not reported as up to date.")
        }
        _ => "No result yet (the probe skipped this check).".into(),
    }
}

/// What the selected mode means here; the web itself never installs.
pub fn mode_note(mode: &str) -> &'static str {
    match mode {
        "off" => "Off: no update checks run, here or on the host.",
        "auto" => "Auto: the daemon may update itself on the host. This page only reports; it never installs.",
        _ => "Notify: checks at most once a day and reports a newer release. Installing stays on the host.",
    }
}

/// Effective dial from the settings snapshot (`autoupdateEffective`, then
/// the raw dial, then the probe's own default `notify`).
pub fn effective_mode(dials: &Value) -> String {
    for key in ["autoupdateEffective", "autoupdate"] {
        if let Some(m) = dials.get(key).and_then(Value::as_str) {
            let m = m.trim();
            if matches!(m, "off" | "notify" | "auto") {
                return m.to_string();
            }
        }
    }
    "notify".into()
}

const MODES: &[(&str, &str)] = &[("off", "Off"), ("notify", "Notify"), ("auto", "Auto")];

#[component]
pub fn AutoupdateRow(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    dials: RwSignal<Value>,
) -> impl IntoView {
    let check = signal();
    let cap = capable();
    let busy = RwSignal::new(false);
    let error = RwSignal::new(String::new());

    // Settings may open before the boot probe answers; the snapshot's
    // last result fills the row until then.
    let shown = {
        let check = check.clone();
        move || {
            check.get().or_else(|| {
                dials.with(|d| {
                    d.get("updateCheck")
                        .cloned()
                        .and_then(|v| serde_json::from_value::<UpdateCheck>(v).ok())
                })
            })
        }
    };

    let run_check = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        busy.set(true);
        spawn_local(async move {
            let result = fetch(&b, &t).await;
            publish(Some(match result {
                Ok(c) => c,
                Err(e) => failed(e),
            }));
            busy.set(false);
        });
    };

    let pick = {
        let cap = cap.clone();
        move |mode: String| {
            let (b, t) = (base.get_untracked(), token.get_untracked());
            let cap = cap.get_untracked();
            busy.set(true);
            error.set(String::new());
            spawn_local(async move {
                match patch_json(&b, &t, "/v1/settings", serde_json::json!({ "autoupdate": mode })).await {
                    Ok(()) => {
                        if let Ok(v) = get_json::<Value>(&b, &t, "/v1/settings").await {
                            dials.set(v);
                        }
                        status.set(format!("Autoupdate set to {mode}."));
                        if cap {
                            let result = fetch(&b, &t).await;
                            publish(Some(match result {
                                Ok(c) => c,
                                Err(e) => failed(e),
                            }));
                        }
                    }
                    Err(e) => error.set(format!("Autoupdate was not changed: {e}")),
                }
                busy.set(false);
            });
        }
    };

    let line = {
        let cap = cap.clone();
        let shown = shown.clone();
        move || status_line(shown().as_ref(), cap.get())
    };
    let state = {
        let shown = shown.clone();
        move || shown().map(|c| c.state).unwrap_or_default()
    };
    let can_check = {
        let cap = cap.clone();
        move || cap.get()
    };

    view! {
        <section class="card settings-section" id="settings-updates" data-section="updates">
            <h2 class="panel-title">"Updates"</h2>
            <fieldset class="update-mode">
                <legend>"Autoupdate"</legend>
                <div class="segmented" role="radiogroup" aria-label="Autoupdate">
                    {MODES.iter().map(|(id, label)| {
                        let id = *id;
                        let pick = pick.clone();
                        view! {
                            <label class="seg-choice">
                                <input
                                    type="radio"
                                    name="settings-autoupdate"
                                    data-autoupdate=id
                                    prop:checked=move || dials.with(|d| effective_mode(d) == id)
                                    prop:disabled=move || busy.get()
                                    on:change=move |_| pick(id.to_string())
                                />
                                <span>{*label}</span>
                            </label>
                        }
                    }).collect_view()}
                </div>
            </fieldset>
            <p class="hint">{move || mode_note(&dials.with(effective_mode))}</p>
            <p class="update-status" role="status" data-update-state=state>{line}</p>
            <Show when=move || !error.get().is_empty()>
                <p class="banner" role="alert">{move || error.get()}</p>
            </Show>
            <Show when=can_check>
                <button type="button" class="secondary" data-action="update-check" prop:disabled=move || busy.get() on:click=move |_| run_check()>
                    "Check now"
                </button>
            </Show>
        </section>
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn check(state: &str) -> UpdateCheck {
        UpdateCheck {
            state: state.into(),
            current: "v1.2.0".into(),
            latest: "v1.3.0".into(),
            ..UpdateCheck::default()
        }
    }

    #[test]
    fn parses_the_daemon_reply() {
        let c: UpdateCheck = serde_json::from_value(json!({
            "mode": "notify", "state": "available", "current": "v1.2.0", "latest": "v1.3.0",
            "available": true, "checkedAt": "2026-11-20T00:00:00Z", "canReplace": true
        }))
        .unwrap();
        assert_eq!(c.state, "available");
        assert!(c.available && c.can_replace);
        assert_eq!(c.checked_at, "2026-11-20T00:00:00Z");
    }

    #[test]
    fn status_line_per_state() {
        let cases = [
            ("off", "Checks are off"),
            ("available", "Strike v1.3.0 is available (this gateway runs v1.2.0)"),
            ("current", "Up to date (v1.2.0)"),
            ("unknown", "No result yet"),
        ];
        for (state, want) in cases {
            let got = status_line(Some(&check(state)), true);
            assert!(got.contains(want), "{state}: {got}");
        }
        assert_eq!(status_line(None, true), "Not checked yet.");
        assert!(status_line(None, false).contains("does not offer"));
    }

    #[test]
    fn failure_is_never_up_to_date() {
        let mut c = check("failed");
        c.error = "rate limited".into();
        let got = status_line(Some(&c), true);
        assert!(got.contains("rate limited"), "{got}");
        assert!(!got.starts_with("Up to date"), "{got}");
        assert!(status_line(Some(&failed("HTTP 501".into())), true).contains("failed"));
    }

    #[test]
    fn effective_mode_prefers_daemon_effective_value() {
        let cases = [
            (json!({"autoupdateEffective": "off", "autoupdate": "auto"}), "off"),
            (json!({"autoupdate": "auto"}), "auto"),
            (json!({"autoupdate": ""}), "notify"),
            (json!({"autoupdateEffective": "weird"}), "notify"),
            (json!({}), "notify"),
        ];
        for (dials, want) in cases {
            assert_eq!(effective_mode(&dials), want, "{dials}");
        }
    }

    #[test]
    fn auto_mode_copy_says_the_web_never_installs() {
        assert!(mode_note("auto").contains("never installs"));
        assert!(mode_note("off").contains("no update checks"));
    }
}
