//! Settings → Guardrails (#1577): the session guardrail dials that were
//! config-file-only. Every control PATCHes the real `/v1/settings` field
//! and re-reads the daemon-reported effective value; a rejected value
//! renders the daemon's reason. Loosening changes (raising or removing a
//! cap, delegation off, handoff optional, children on the host) go through
//! the shared confirm dialog; tightening applies at once.

use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde_json::{Map, Value};

use crate::api::{get_json, patch_json};
use crate::confirm::{ask, ConfirmRequest, BUSY};

/// Product default for `session.turnTimeoutS` when the config omits it.
pub const DEFAULT_TURN_TIMEOUT_S: i64 = 1800;

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Kind {
    /// Seconds; -1 = off. PATCH: default|off|seconds|duration.
    Timeout,
    /// USD; 0 = unlimited.
    Cost,
    /// Integer cap; 0 = unlimited.
    Cap,
    /// Delegation threshold; 0 = engine default, -1 = off.
    Threshold,
    /// Options ordered loosest → strictest; `value` is the PATCH token.
    Choice(&'static [(&'static str, &'static str)]),
}

#[derive(Debug)]
pub struct DialSpec {
    /// Wire path on `/v1/settings`; `.` nests (`agentBudget.maxCostUsd`).
    pub key: &'static str,
    pub label: &'static str,
    pub help: &'static str,
    pub kind: Kind,
}

const DELEGATION_MODES: &[(&str, &str)] = &[("off", "Off"), ("advise", "Advise"), ("enforce", "Enforce")];
const HANDOFF: &[(&str, &str)] = &[("off", "Optional"), ("on", "Required")];
// Host mode first: it is the default and a first-class choice.
const ISOLATION: &[(&str, &str)] = &[
    ("shared", "On this host (shared workdir)"),
    ("worktree", "Git worktree per child"),
];

pub const SESSION_DIALS: &[DialSpec] = &[
    DialSpec {
        key: "turnTimeoutS",
        label: "Turn timeout",
        help: "Wall-clock deadline for one root turn. Seconds, a duration like 30m, off, or default (1800s).",
        kind: Kind::Timeout,
    },
    DialSpec {
        key: "maxSessionCostUSD",
        label: "Max session cost (USD)",
        help: "Stops a session once its spend reaches this amount. off = unlimited.",
        kind: Kind::Cost,
    },
    DialSpec {
        key: "maxTurnTokens",
        label: "Max turn tokens",
        help: "Token ceiling for a single turn. off = unlimited.",
        kind: Kind::Cap,
    },
];

pub const AGENT_BUDGET_DIALS: &[DialSpec] = &[
    DialSpec {
        key: "agentBudget.maxCostUsd",
        label: "Cost (USD)",
        help: "Spend allowed per child. off = unlimited.",
        kind: Kind::Cost,
    },
    DialSpec {
        key: "agentBudget.maxTokens",
        label: "Tokens",
        help: "Tokens allowed per child. off = unlimited.",
        kind: Kind::Cap,
    },
    DialSpec {
        key: "agentBudget.maxWallClockS",
        label: "Wall clock (s)",
        help: "Seconds a child may run. off = unlimited.",
        kind: Kind::Cap,
    },
    DialSpec {
        key: "agentBudget.maxToolCalls",
        label: "Tool calls",
        help: "Tool calls allowed per child. off = unlimited.",
        kind: Kind::Cap,
    },
    DialSpec {
        key: "agentBudget.maxDangerousTools",
        label: "Dangerous tool calls",
        help: "Write/exec tool calls allowed per child. off = unlimited.",
        kind: Kind::Cap,
    },
    DialSpec {
        key: "agentBudget.stallAfterS",
        label: "Stall after (s)",
        help: "Stop a child with no progress for this long. off = never.",
        kind: Kind::Cap,
    },
    DialSpec {
        key: "agentBudget.loopDetectN",
        label: "Loop detect",
        help: "Stop a child repeating the same call this many times. off = never.",
        kind: Kind::Cap,
    },
];

pub const DELEGATION_DIALS: &[DialSpec] = &[
    DialSpec {
        key: "delegationPolicy.mode",
        label: "Delegation policy",
        help: "Whether undersized delegations are blocked (enforce), flagged (advise), or allowed (off).",
        kind: Kind::Choice(DELEGATION_MODES),
    },
    DialSpec {
        key: "delegationPolicy.maxLiveChildren",
        label: "Max live children",
        help: "Children running at once. off = unlimited.",
        kind: Kind::Cap,
    },
    DialSpec {
        key: "delegationPolicy.tinyPromptRunes",
        label: "Tiny prompt (runes)",
        help: "Prompts shorter than this count as too small to delegate. default = engine default, off = rule disabled.",
        kind: Kind::Threshold,
    },
    DialSpec {
        key: "delegationPolicy.maxPathsLocal",
        label: "Max paths to keep local",
        help: "Tasks touching at most this many paths stay with the parent. default = engine default, off = rule disabled.",
        kind: Kind::Threshold,
    },
    DialSpec {
        key: "childHandoffRequired",
        label: "Child handoff",
        help: "Whether a child must return a structured handoff before it finishes.",
        kind: Kind::Choice(HANDOFF),
    },
    DialSpec {
        key: "childIsolation",
        label: "Child isolation",
        help: "Where children run: directly on this host in the parent's workdir (default), or each in its own git worktree.",
        kind: Kind::Choice(ISOLATION),
    },
];

/// What the group says about when changes apply (read from the config
/// layer: dials load when a strike process assembles).
pub const APPLY_NOTE: &str = "Gateway defaults, saved to ~/.strike/config. Strike reads them when a process starts, so restart strike serve to apply them. CLI --turn-timeout / --max-cost and a project .strike/config that sets the same key take precedence.";

/// Read a dotted wire path from the settings snapshot.
pub fn lookup<'a>(snapshot: &'a Value, key: &str) -> Option<&'a Value> {
    key.split('.').try_fold(snapshot, |v, part| v.get(part))
}

/// PATCH body for one dial: `agentBudget.maxCostUsd` nests.
pub fn patch_body(key: &str, value: &str) -> Value {
    let mut out = Value::String(value.to_string());
    for part in key.rsplit('.') {
        let mut map = Map::new();
        map.insert(part.to_string(), out);
        out = Value::Object(map);
    }
    out
}

/// The current PATCH token for a choice dial (`childHandoffRequired` is a
/// bool on GET).
pub fn choice_value(v: Option<&Value>) -> String {
    match v {
        Some(Value::Bool(true)) => "on".into(),
        Some(Value::Bool(false)) => "off".into(),
        Some(Value::String(s)) => s.trim().to_string(),
        _ => String::new(),
    }
}

fn trim_float(v: f64) -> String {
    let s = format!("{v:.4}");
    s.trim_end_matches('0').trim_end_matches('.').to_string()
}

/// Seconds as `1800s (30m)` / `90s`.
pub fn format_secs(secs: i64) -> String {
    if secs >= 3600 && secs % 3600 == 0 {
        format!("{secs}s ({}h)", secs / 3600)
    } else if secs >= 60 && secs % 60 == 0 {
        format!("{secs}s ({}m)", secs / 60)
    } else {
        format!("{secs}s")
    }
}

/// Human form of the daemon-reported effective value.
pub fn display(kind: Kind, v: Option<&Value>) -> String {
    let num = v.and_then(Value::as_f64);
    match kind {
        Kind::Timeout => match num {
            Some(n) if n < 0.0 => "off".into(),
            Some(n) => format_secs(n as i64),
            None => "—".into(),
        },
        Kind::Cost => match num {
            Some(n) if n <= 0.0 => "unlimited".into(),
            Some(n) => format!("${}", trim_float(n)),
            None => "—".into(),
        },
        Kind::Cap => match num {
            Some(n) if n <= 0.0 => "unlimited".into(),
            Some(n) => format!("{}", n as i64),
            None => "—".into(),
        },
        Kind::Threshold => match num {
            Some(n) if n < 0.0 => "off".into(),
            Some(n) if n == 0.0 => "engine default".into(),
            Some(n) => format!("{}", n as i64),
            None => "—".into(),
        },
        Kind::Choice(options) => {
            let cur = choice_value(v);
            options
                .iter()
                .find(|(id, _)| *id == cur)
                .map(|(_, label)| label.to_string())
                .unwrap_or_else(|| if cur.is_empty() { "—".into() } else { cur })
        }
    }
}

/// Go `time.ParseDuration` subset the turn-timeout dial accepts, in whole
/// seconds (sub-second rounds down, min 1 — matching the config layer).
pub fn parse_duration_secs(raw: &str) -> Option<i64> {
    let s = raw.trim();
    if s.is_empty() {
        return None;
    }
    let mut total = 0f64;
    let mut rest = s;
    while !rest.is_empty() {
        let num_len = rest
            .find(|c: char| !(c.is_ascii_digit() || c == '.'))
            .unwrap_or(rest.len());
        if num_len == 0 {
            return None;
        }
        let n: f64 = rest[..num_len].parse().ok()?;
        rest = &rest[num_len..];
        let unit_len = rest
            .find(|c: char| c.is_ascii_digit() || c == '.')
            .unwrap_or(rest.len());
        let scale = match &rest[..unit_len] {
            "h" => 3600.0,
            "m" => 60.0,
            "s" => 1.0,
            "ms" => 0.001,
            "us" | "µs" => 0.000_001,
            "ns" => 0.000_000_001,
            _ => return None,
        };
        rest = &rest[unit_len..];
        total += n * scale;
    }
    if total <= 0.0 {
        return Some(-1);
    }
    Some((total as i64).max(1))
}

/// Turn-timeout token → seconds (-1 = off), as the daemon resolves it.
pub fn parse_timeout(raw: &str) -> Option<i64> {
    let v = raw.trim().to_ascii_lowercase();
    match v.as_str() {
        "default" | "unset" => return Some(DEFAULT_TURN_TIMEOUT_S),
        "off" | "none" | "disable" | "disabled" | "0" => return Some(-1),
        _ => {}
    }
    if let Ok(n) = v.parse::<i64>() {
        return Some(if n <= 0 { -1 } else { n });
    }
    parse_duration_secs(&v)
}

/// Cap token → number (0 = unlimited), as the daemon resolves it.
pub fn parse_cap(raw: &str) -> Option<f64> {
    let v = raw.trim().to_ascii_lowercase();
    match v.as_str() {
        "off" | "0" | "none" | "unlimited" | "default" | "unset" => return Some(0.0),
        _ => {}
    }
    let n: f64 = v.trim_start_matches('$').parse().ok()?;
    (n.is_finite() && n >= 0.0).then_some(n)
}

/// True when applying `input` relaxes the guardrail from its current
/// effective value. Unparseable input is not "loosening": the daemon
/// rejects it and its reason is shown instead.
pub fn loosens(kind: Kind, current: Option<&Value>, input: &str) -> bool {
    let cur = current.and_then(Value::as_f64);
    match kind {
        Kind::Timeout => {
            let (Some(cur), Some(next)) = (cur, parse_timeout(input)) else {
                return false;
            };
            if cur < 0.0 {
                return false;
            }
            next < 0 || next as f64 > cur
        }
        Kind::Cost | Kind::Cap => {
            let (Some(cur), Some(next)) = (cur, parse_cap(input)) else {
                return false;
            };
            if cur <= 0.0 {
                return false;
            }
            next == 0.0 || next > cur
        }
        Kind::Threshold => false,
        Kind::Choice(options) => {
            let rank = |id: &str| options.iter().position(|(o, _)| *o == id);
            let next = input.trim().to_ascii_lowercase();
            // `default` resolves to the strictest option for handoff and
            // delegation, and to the host mode for isolation.
            let next = if next == "default" {
                if options == ISOLATION {
                    "shared".to_string()
                } else {
                    options.last().map(|(id, _)| id.to_string()).unwrap_or_default()
                }
            } else {
                next
            };
            match (rank(choice_value(current).as_str()), rank(next.as_str())) {
                (Some(c), Some(n)) => n < c,
                _ => false,
            }
        }
    }
}

/// Confirm copy for a loosening change. The host-mode copy is neutral:
/// running children on the gateway host is a supported default.
pub fn confirm_for(spec: &DialSpec, current: Option<&Value>, input: &str) -> ConfirmRequest {
    if spec.key == "childIsolation" {
        return ConfirmRequest {
            title: "Run children on this host?".into(),
            body: "New children will run directly in the parent's working directory on the gateway host instead of their own git worktree. This is the default mode.".into(),
            confirm_label: "Use host mode".into(),
        };
    }
    let next = match spec.kind {
        Kind::Choice(_) => display(spec.kind, Some(&Value::String(input.trim().to_string()))),
        _ => input.trim().to_string(),
    };
    ConfirmRequest {
        title: format!("Loosen {}?", spec.label.to_lowercase()),
        body: format!(
            "{}: {} → {}. New sessions on this gateway get the looser limit.",
            spec.label,
            display(spec.kind, current),
            next
        ),
        confirm_label: "Apply".into(),
    }
}

/// The daemon's reason from a `patch_json` error (`HTTP 400 {"error":…}`).
pub fn daemon_reason(err: &str) -> String {
    if let Some(start) = err.find('{') {
        if let Ok(v) = serde_json::from_str::<Value>(&err[start..]) {
            if let Some(msg) = v.get("error").and_then(Value::as_str) {
                if !msg.trim().is_empty() {
                    return msg.trim().to_string();
                }
            }
        }
    }
    err.trim().to_string()
}

fn dom_id(key: &str) -> String {
    format!("guard-{}", key.replace('.', "-"))
}

#[component]
pub fn Guardrails(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    dials: RwSignal<Value>,
) -> impl IntoView {
    view! {
        <section class="card settings-section guardrails" id="settings-guardrails" data-section="guardrails">
            <h2 class="panel-title">"Guardrails"</h2>
            <p class="hint" data-guard-note="">{APPLY_NOTE}</p>
            <Show
                when=move || dials.with(|d| d.get("turnTimeoutS").is_some())
                fallback=|| view! { <p class="hint">"Reading the daemon's guardrail dials…"</p> }
            >
                <div class="guard-group">
                    {SESSION_DIALS.iter().map(|spec| view! { <DialRow spec=spec base=base token=token status=status dials=dials /> }).collect_view()}
                </div>
                <h3 class="guard-subtitle">"Agent budget"</h3>
                <p class="hint">"Default budget each delegated child gets."</p>
                <div class="guard-group">
                    {AGENT_BUDGET_DIALS.iter().map(|spec| view! { <DialRow spec=spec base=base token=token status=status dials=dials /> }).collect_view()}
                </div>
                <h3 class="guard-subtitle">"Delegation"</h3>
                <div class="guard-group">
                    {DELEGATION_DIALS.iter().map(|spec| view! { <DialRow spec=spec base=base token=token status=status dials=dials /> }).collect_view()}
                </div>
            </Show>
        </section>
    }
}

#[component]
fn DialRow(
    spec: &'static DialSpec,
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    dials: RwSignal<Value>,
) -> impl IntoView {
    let draft = RwSignal::new(String::new());
    let error = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let id = dom_id(spec.key);

    let apply = move |value: String| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        busy.set(true);
        error.set(String::new());
        spawn_local(async move {
            match patch_json(&b, &t, "/v1/settings", patch_body(spec.key, &value)).await {
                Ok(()) => {
                    draft.set(String::new());
                    match get_json::<Value>(&b, &t, "/v1/settings").await {
                        Ok(v) => {
                            let shown = display(spec.kind, lookup(&v, spec.key));
                            dials.set(v);
                            status.set(format!("Saved {}: {shown}. Applies after strike serve restarts.", spec.label));
                        }
                        Err(e) => error.set(format!("Saved, but could not re-read the daemon value: {e}")),
                    }
                }
                Err(e) => error.set(daemon_reason(&e)),
            }
            busy.set(false);
        });
    };
    let submit = move |value: String| {
        let value = value.trim().to_string();
        if value.is_empty() {
            error.set("Enter a value, off, or default.".into());
            return;
        }
        let current = dials.with_untracked(|d| lookup(d, spec.key).cloned());
        if loosens(spec.kind, current.as_ref(), &value) {
            let request = confirm_for(spec, current.as_ref(), &value);
            let v = value.clone();
            if !ask(request, Rc::new(move || apply(v.clone()))) {
                status.set(BUSY.into());
            }
        } else {
            apply(value);
        }
    };

    let effective = move || dials.with(|d| display(spec.kind, lookup(d, spec.key)));
    let control = match spec.kind {
        Kind::Choice(options) => view! {
            <div class="segmented" role="radiogroup" aria-label=spec.label>
                {options.iter().map(|(opt, label)| {
                    let opt = *opt;
                    view! {
                        <label class="seg-choice">
                            <input
                                type="radio"
                                name=dom_id(spec.key)
                                data-choice=opt
                                prop:checked=move || dials.with(|d| choice_value(lookup(d, spec.key)) == opt)
                                prop:disabled=move || busy.get()
                                on:change=move |_| submit(opt.to_string())
                            />
                            <span>{*label}</span>
                        </label>
                    }
                }).collect_view()}
            </div>
        }
        .into_any(),
        _ => {
            let input_id = id.clone();
            view! {
                <form class="guard-edit" on:submit=move |ev| {
                    ev.prevent_default();
                    submit(draft.get_untracked());
                }>
                    <input
                        id=input_id
                        type="text"
                        autocomplete="off"
                        spellcheck="false"
                        placeholder=move || effective()
                        prop:value=move || draft.get()
                        on:input=move |ev| draft.set(event_target_value(&ev))
                    />
                    <button type="submit" class="secondary" data-action="guard-save" prop:disabled=move || busy.get()>"Save"</button>
                </form>
            }
            .into_any()
        }
    };

    view! {
        <div class="guard-dial" data-dial=spec.key>
            <div class="guard-head">
                <label class="guard-label" for=id.clone()>{spec.label}</label>
                <span class="guard-value" data-effective=spec.key>{effective}</span>
            </div>
            <p class="hint">{spec.help}</p>
            {control}
            <Show when=move || !error.get().is_empty()>
                <p class="banner" role="alert" data-guard-error=spec.key>{move || error.get()}</p>
            </Show>
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn spec(key: &str) -> &'static DialSpec {
        SESSION_DIALS
            .iter()
            .chain(AGENT_BUDGET_DIALS)
            .chain(DELEGATION_DIALS)
            .find(|s| s.key == key)
            .unwrap()
    }

    #[test]
    fn covers_every_guardrail_wire_field() {
        let keys: Vec<&str> = SESSION_DIALS
            .iter()
            .chain(AGENT_BUDGET_DIALS)
            .chain(DELEGATION_DIALS)
            .map(|s| s.key)
            .collect();
        for want in [
            "turnTimeoutS",
            "maxSessionCostUSD",
            "maxTurnTokens",
            "agentBudget.maxWallClockS",
            "agentBudget.maxTokens",
            "agentBudget.maxCostUsd",
            "agentBudget.maxToolCalls",
            "agentBudget.maxDangerousTools",
            "agentBudget.stallAfterS",
            "agentBudget.loopDetectN",
            "delegationPolicy.mode",
            "delegationPolicy.tinyPromptRunes",
            "delegationPolicy.maxPathsLocal",
            "delegationPolicy.maxLiveChildren",
            "childHandoffRequired",
            "childIsolation",
        ] {
            assert!(keys.contains(&want), "missing {want}");
        }
    }

    #[test]
    fn patch_body_nests_dotted_keys() {
        let cases = [
            ("turnTimeoutS", "30m", json!({"turnTimeoutS": "30m"})),
            ("agentBudget.maxCostUsd", "2", json!({"agentBudget": {"maxCostUsd": "2"}})),
            ("delegationPolicy.mode", "advise", json!({"delegationPolicy": {"mode": "advise"}})),
        ];
        for (key, value, want) in cases {
            assert_eq!(patch_body(key, value), want, "{key}");
        }
    }

    #[test]
    fn display_reports_daemon_values() {
        let snap = json!({
            "turnTimeoutS": 1800,
            "maxSessionCostUSD": 0,
            "maxTurnTokens": 50000,
            "agentBudget": {"maxCostUsd": 1.5},
            "delegationPolicy": {"mode": "enforce", "tinyPromptRunes": 0, "maxPathsLocal": -1},
            "childHandoffRequired": true,
            "childIsolation": "shared"
        });
        let cases = [
            ("turnTimeoutS", "1800s (30m)"),
            ("maxSessionCostUSD", "unlimited"),
            ("maxTurnTokens", "50000"),
            ("agentBudget.maxCostUsd", "$1.5"),
            ("delegationPolicy.mode", "Enforce"),
            ("delegationPolicy.tinyPromptRunes", "engine default"),
            ("delegationPolicy.maxPathsLocal", "off"),
            ("childHandoffRequired", "Required"),
            ("childIsolation", "On this host (shared workdir)"),
        ];
        for (key, want) in cases {
            assert_eq!(display(spec(key).kind, lookup(&snap, key)), want, "{key}");
        }
        assert_eq!(display(Kind::Timeout, Some(&json!(-1))), "off");
    }

    #[test]
    fn timeout_parse_matches_config_layer() {
        let cases = [
            ("default", Some(1800)),
            ("off", Some(-1)),
            ("0", Some(-1)),
            ("-5", Some(-1)),
            ("900", Some(900)),
            ("30m", Some(1800)),
            ("1h30m", Some(5400)),
            ("1.5h", Some(5400)),
            ("500ms", Some(1)),
            ("soon", None),
            ("10x", None),
        ];
        for (raw, want) in cases {
            assert_eq!(parse_timeout(raw), want, "{raw}");
        }
    }

    #[test]
    fn cap_parse_matches_config_layer() {
        let cases = [
            ("off", Some(0.0)),
            ("unlimited", Some(0.0)),
            ("default", Some(0.0)),
            ("$2.50", Some(2.5)),
            ("10", Some(10.0)),
            ("-1", None),
            ("lots", None),
        ];
        for (raw, want) in cases {
            assert_eq!(parse_cap(raw), want, "{raw}");
        }
    }

    #[test]
    fn loosening_needs_confirm_tightening_does_not() {
        let cases: &[(&str, Value, &str, bool)] = &[
            ("turnTimeoutS", json!(1800), "off", true),
            ("turnTimeoutS", json!(1800), "1h", true),
            ("turnTimeoutS", json!(1800), "10m", false),
            ("turnTimeoutS", json!(-1), "30m", false),
            ("turnTimeoutS", json!(600), "default", true),
            ("maxSessionCostUSD", json!(5.0), "off", true),
            ("maxSessionCostUSD", json!(5.0), "10", true),
            ("maxSessionCostUSD", json!(5.0), "2", false),
            ("maxSessionCostUSD", json!(0), "2", false),
            ("maxSessionCostUSD", json!(5.0), "bogus", false),
            ("maxTurnTokens", json!(1000), "unlimited", true),
            ("delegationPolicy.tinyPromptRunes", json!(10), "off", false),
            ("delegationPolicy.mode", json!("enforce"), "advise", true),
            ("delegationPolicy.mode", json!("off"), "enforce", false),
            ("childHandoffRequired", json!(true), "off", true),
            ("childHandoffRequired", json!(false), "on", false),
            ("childIsolation", json!("worktree"), "shared", true),
            ("childIsolation", json!("worktree"), "default", true),
            ("childIsolation", json!("shared"), "worktree", false),
        ];
        for (key, cur, input, want) in cases {
            assert_eq!(loosens(spec(key).kind, Some(cur), input), *want, "{key} {cur} -> {input}");
        }
    }

    #[test]
    fn host_mode_confirm_is_neutral() {
        let req = confirm_for(spec("childIsolation"), Some(&json!("worktree")), "shared");
        assert!(req.body.contains("default mode"));
        assert!(!req.body.to_lowercase().contains("unsafe"));
    }

    #[test]
    fn daemon_reason_extracts_error_field() {
        let cases = [
            (r#"HTTP 400 {"error":"invalid maxSessionCostUSD \"x\""}"#, r#"invalid maxSessionCostUSD "x""#),
            ("HTTP 500 ", "HTTP 500"),
            ("network down", "network down"),
        ];
        for (raw, want) in cases {
            assert_eq!(daemon_reason(raw), want, "{raw}");
        }
    }
}
