//! Rule authoring helpers for the Permissions panel (#1509).
//!
//! The rules editor in `approvals_ui.rs` used to read as a config file:
//! glob fields with no examples, no way to test a pattern, and daemon
//! errors reduced to "HTTP 400". This module adds the English layer:
//!
//! - [`rule_sentence`]: one plain sentence per rule (and for the draft).
//! - [`EXAMPLES`]: one-click starter rules that fill the form.
//! - [`scope_explainer`] / [`pattern_hint`]: what a scope means and how
//!   the pattern glob matches.
//! - [`daemon_reason`]: the daemon's own `error` text from a failed call.
//! - [`RuleMatchTester`]: asks `GET /v1/permissions/explain` what the
//!   saved rules decide for a typed command. Read-only; nothing runs.
//!
//! Kept out of `approvals_ui.rs` so the rules editor and the presets /
//! explain section (#1504) stay separate files.

use leptos::prelude::*;
use leptos::task::spawn_local;
use load_guard::LoadEpoch;
use serde::Deserialize;

use crate::api::get_json;
use crate::load_epoch::begin;

/// A starter rule the user can load into the form with one click.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RuleExample {
    pub label: &'static str,
    pub scope: &'static str,
    pub permission: &'static str,
    pub pattern: &'static str,
    pub action: &'static str,
}

/// Four examples, one per common intent. Each loads into the form only;
/// the user still presses "Add rule" (and confirms allow/ask rules).
pub const EXAMPLES: &[RuleExample] = &[
    RuleExample {
        label: "Allow git status here",
        scope: "project",
        permission: "bash",
        pattern: "git status*",
        action: "allow",
    },
    RuleExample {
        label: "Always ask before git push",
        scope: "project",
        permission: "bash",
        pattern: "git push*",
        action: "ask",
    },
    RuleExample {
        label: "Never publish to npm",
        scope: "global",
        permission: "bash",
        pattern: "npm publish*",
        action: "deny",
    },
    RuleExample {
        label: "Never edit .env files",
        scope: "global",
        permission: "edit",
        pattern: "**/.env*",
        action: "deny",
    },
];

/// Verb phrase for an action, capitalised for the start of a sentence.
fn action_verb(action: &str) -> &'static str {
    match action {
        "allow" => "Allow",
        "deny" => "Block",
        "ask" => "Ask before",
        _ => "Decide",
    }
}

/// What a permission's pattern is matched against, in English.
fn subject(permission: &str) -> String {
    match permission {
        "bash" => "bash commands".into(),
        "edit" => "file edits".into(),
        "write" => "file writes".into(),
        "read" => "file reads".into(),
        "webfetch" => "web fetches".into(),
        "websearch" => "web searches".into(),
        "mcp" => "MCP tool calls".into(),
        "task" => "subagent tasks".into(),
        "*" => "every tool call".into(),
        other => format!("{other} calls"),
    }
}

/// Where the rule applies and how long it lasts.
fn scope_tail(scope: &str) -> &'static str {
    match scope {
        "global" => "in every project, until you remove it",
        "project" => "in this project only, until you remove it",
        "session folder" => "for this session only; it ends when the session closes",
        _ => "until you remove it",
    }
}

/// How the pattern part reads. `*` / empty means everything; a pattern
/// ending in a single `*` reads as "starting with", plus the daemon's
/// doublestar rule that `*` never crosses `/`; anything else is quoted.
fn pattern_phrase(pattern: &str) -> String {
    let p = pattern.trim();
    if p.is_empty() || p == "*" {
        return String::new();
    }
    if let Some(prefix) = p.strip_suffix('*') {
        if !prefix.is_empty() && !prefix.contains(['*', '?', '[', '{']) {
            return format!(" starting with \"{}\" (with no / after it)", prefix.trim_end());
        }
    }
    if !p.contains(['*', '?', '[', '{']) {
        return format!(" exactly matching \"{p}\"");
    }
    format!(" matching \"{p}\"")
}

/// One plain-English sentence for a rule, e.g.
/// `Allow bash commands starting with "git status" in this project only,
/// until you remove it.`
pub fn rule_sentence(scope: &str, permission: &str, pattern: &str, action: &str) -> String {
    let what = subject(permission.trim());
    let pat = pattern_phrase(pattern);
    let tail = scope_tail(scope.trim());
    let everything = pat.is_empty();
    match (action, everything) {
        ("ask", true) => format!("Ask before {what} {tail}."),
        ("ask", false) => format!("Ask before {what}{pat} {tail}."),
        (_, true) => format!("{} all {what} {tail}.", action_verb(action)),
        (_, false) => format!("{} {what}{pat} {tail}.", action_verb(action)),
    }
}

/// One line per scope tab, shown next to the scope picker.
pub fn scope_explainer(scope: &str) -> &'static str {
    match scope {
        "global" => "Global: stored in your user config. Applies in every project until you remove it.",
        "project" => "Project: stored in this project's config. Applies only here, and wins over a global rule for the same call.",
        _ => "Rules last until you remove them.",
    }
}

/// Short glob guide for the pattern field.
pub fn pattern_hint(permission: &str) -> &'static str {
    match permission {
        "bash" => "Matched against the whole command. * matches any text except /, so \"git status*\" covers \"git status -s\" but not \"git status src/\". * alone matches every command. Test a command below before you save.",
        "edit" | "write" | "read" => "Matched against the file path. * stays inside one folder; **/ crosses folders, so \"**/.env*\" covers every .env file.",
        _ => "* matches any text except /; ** as a whole path part (like **/x) crosses folders; ? matches one character; * alone matches every call.",
    }
}

/// Pull the daemon's `error` text out of an `api.rs` failure string
/// (`HTTP 400 {"error":"..."}`). Falls back to the raw text, so nothing is
/// ever reduced to an empty message.
pub fn daemon_reason(err: &str) -> String {
    let trimmed = err.trim();
    if let Some(start) = trimmed.find('{') {
        if let Ok(value) = serde_json::from_str::<serde_json::Value>(&trimmed[start..]) {
            if let Some(msg) = value.get("error").and_then(|v| v.as_str()) {
                let msg = msg.trim();
                if !msg.is_empty() {
                    return msg.to_string();
                }
            }
        }
    }
    if trimmed.is_empty() {
        "the daemon gave no reason".into()
    } else {
        trimmed.to_string()
    }
}

/// Feedback line under the add-rule form.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub enum RuleFeedback {
    #[default]
    None,
    /// The rule was saved; the sentence restates what it now does.
    Saved(String),
    Removed(String),
    /// Validation or daemon failure, with the reason shown as-is.
    Failed(String),
}

impl RuleFeedback {
    pub fn text(&self) -> String {
        match self {
            RuleFeedback::None => String::new(),
            RuleFeedback::Saved(s) => format!("Saved. {s}"),
            RuleFeedback::Removed(s) => format!("Removed. No longer: {s}"),
            RuleFeedback::Failed(reason) => format!("Not saved: {reason}"),
        }
    }

    pub fn class(&self) -> &'static str {
        match self {
            RuleFeedback::Failed(_) => "rule-feedback notice",
            _ => "rule-feedback hint",
        }
    }
}

/// One matched rule in the explain DTO. The daemon's host DTO has no
/// json tags, so the wire names are the Go field names.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(rename_all = "PascalCase")]
pub struct ExplainMatch {
    #[serde(default)]
    pub layer: String,
    #[serde(default)]
    pub permission: String,
    #[serde(default)]
    pub pattern: String,
    #[serde(default)]
    pub action: String,
}

/// Subset of `host.PermissionExplanation` the tester shows.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(rename_all = "PascalCase")]
pub struct Explanation {
    #[serde(default)]
    pub permission: String,
    #[serde(default)]
    pub pattern: String,
    #[serde(default)]
    pub action: String,
    #[serde(default)]
    pub layer: String,
    #[serde(default)]
    pub matched: ExplainMatch,
    #[serde(default)]
    pub managed_blocks: bool,
    #[serde(default)]
    pub managed_summary: String,
}

/// `/v1/permissions/explain` path with both values URI-encoded by the
/// caller-supplied encoder (js `encodeURIComponent` in the browser).
pub fn explain_path(permission: &str, sample: &str, encode: impl Fn(&str) -> String) -> String {
    format!(
        "/v1/permissions/explain?permission={}&pattern={}",
        encode(permission.trim()),
        encode(sample.trim())
    )
}

/// English name for an explain layer (`harness/permission/explain.go`).
pub fn layer_label(layer: &str) -> String {
    match layer {
        "defaults" => "built-in default".into(),
        "preset" => "preset".into(),
        "config" => "global".into(),
        "project" => "project".into(),
        "session" => "this session's".into(),
        "scoped-grant" => "session folder".into(),
        "agent" => "agent".into(),
        "phase" => "workflow phase".into(),
        "managed" => "managed policy".into(),
        "dangerous-allow-all" => "allow-all".into(),
        "tool-chain" => "tool-chain".into(),
        other => other.to_string(),
    }
}

/// Headline verdict for a tested command: what happens, then why.
pub fn verdict_line(ex: &Explanation) -> String {
    let what = match ex.action.as_str() {
        "allow" => "Allowed: runs without asking",
        "deny" => "Blocked: never runs",
        "ask" => "Asks you first",
        "" => "No decision returned",
        _ => "Decided",
    };
    let m = &ex.matched;
    let layer = if m.layer.is_empty() { ex.layer.as_str() } else { m.layer.as_str() };
    let why = if matches!(layer, "mode-upgrade" | "mode-late") {
        "The live approval mode decided it, not a rule.".to_string()
    } else if layer == "default" || (m.pattern.is_empty() && m.permission.is_empty()) {
        "No rule matched, so the default applies.".to_string()
    } else {
        let pat = if m.pattern.is_empty() { "*" } else { m.pattern.as_str() };
        if layer.is_empty() {
            format!("Decided by the rule {} \"{}\" → {}.", m.permission, pat, m.action)
        } else {
            format!("Decided by the {} rule {} \"{}\" → {}.", layer_label(layer), m.permission, pat, m.action)
        }
    };
    format!("{what}. {why}")
}

fn js_encode(s: &str) -> String {
    crate::api::js_encode(s)
}

/// Match tester: type a command (or path), see what Strike would decide.
/// The daemon answers from the live session's stack when one is bound
/// (rules plus the approval mode), else from the saved rules alone.
#[component]
pub fn RuleMatchTester(
    base: RwSignal<String>,
    token: RwSignal<String>,
    perm_mode: RwSignal<String>,
    /// Permission names from the rules document (falls back to a short list).
    known: Signal<Vec<String>>,
) -> impl IntoView {
    let perm = RwSignal::new("bash".to_string());
    let sample = RwSignal::new(String::new());
    let result = RwSignal::new(None::<Result<Explanation, String>>);
    let busy = RwSignal::new(false);
    let epoch = RwSignal::new(LoadEpoch::new());

    let run = move || {
        let permission = perm.get_untracked();
        let text = sample.get_untracked();
        if text.trim().is_empty() {
            result.set(Some(Err("Type a command or path to test.".into())));
            return;
        }
        let gen = begin(epoch);
        busy.set(true);
        result.set(None);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let path = explain_path(&permission, &text, js_encode);
        spawn_local(async move {
            let got = get_json::<Explanation>(&b, &t, &path).await;
            if !epoch.with_untracked(|e| e.accept(gen)) {
                return;
            }
            busy.set(false);
            result.set(Some(got.map_err(|e| {
                let reason = daemon_reason(&e);
                if reason.contains("HTTP 501") {
                    "This daemon cannot explain rules (permissions service unavailable).".to_string()
                } else {
                    format!("Could not test: {reason}")
                }
            })));
        });
    };

    view! {
        <div class="rule-tester" data-rule-tester="true">
            <h4>"Test a command"</h4>
            <p class="hint">"Type what the agent might run. Strike checks your saved rules and says what would happen. Nothing runs."</p>
            <div class="approvals-form">
                <select aria-label="Tester permission" prop:value=move || perm.get() on:change=move |ev| perm.set(event_target_value(&ev))>
                    {move || {
                        let mut names = known.get();
                        if names.is_empty() {
                            names = vec!["bash".into(), "edit".into(), "write".into(), "webfetch".into(), "mcp".into()];
                        }
                        names.into_iter().filter(|n| n != "*").map(|name| {
                            let value = name.clone();
                            view! { <option value=value>{name}</option> }
                        }).collect_view()
                    }}
                </select>
                <input
                    aria-label="Command to test"
                    placeholder="git push origin main"
                    prop:value=move || sample.get()
                    on:input=move |ev| sample.set(event_target_value(&ev))
                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                        if ev.key() == "Enter" {
                            ev.prevent_default();
                            run();
                        }
                    }
                />
                <button type="button" class="secondary" disabled=move || busy.get() on:click=move |_| run()>"Test"</button>
            </div>
            {move || match result.get() {
                None => view! { <span></span> }.into_any(),
                Some(Err(msg)) => view! { <p class="notice" data-rule-tester-error="true">{msg}</p> }.into_any(),
                Some(Ok(ex)) => {
                    let verdict = verdict_line(&ex);
                    let action = ex.action.clone();
                    let managed = if ex.managed_blocks && !ex.managed_summary.is_empty() {
                        Some(format!("Managed policy: {}", ex.managed_summary))
                    } else {
                        None
                    };
                    let mode = crate::model::mode_label(&perm_mode.get()).to_string();
                    view! {
                        <div class="rule-tester-result" data-rule-verdict=action>
                            <p class="approvals-copy">{verdict}</p>
                            {managed.map(|m| view! { <p class="hint">{m}</p> })}
                            <p class="hint">{format!("Checked against your saved rules and, when a session is live, its approval mode ({mode}). Save a rule, then test again to see it apply.")}</p>
                        </div>
                    }.into_any()
                }
            }}
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sentence_reads_prefix_globs_as_starting_with() {
        assert_eq!(
            rule_sentence("project", "bash", "git status*", "allow"),
            "Allow bash commands starting with \"git status\" (with no / after it) in this project only, until you remove it."
        );
        assert_eq!(
            rule_sentence("global", "bash", "rm -rf *", "deny"),
            "Block bash commands starting with \"rm -rf\" (with no / after it) in every project, until you remove it."
        );
    }

    #[test]
    fn sentence_covers_everything_exact_and_complex_patterns() {
        assert_eq!(
            rule_sentence("global", "*", "*", "ask"),
            "Ask before every tool call in every project, until you remove it."
        );
        assert_eq!(
            rule_sentence("global", "webfetch", "", "allow"),
            "Allow all web fetches in every project, until you remove it."
        );
        assert_eq!(
            rule_sentence("project", "bash", "make test", "allow"),
            "Allow bash commands exactly matching \"make test\" in this project only, until you remove it."
        );
        assert_eq!(
            rule_sentence("global", "edit", "**/.env*", "deny"),
            "Block file edits matching \"**/.env*\" in every project, until you remove it."
        );
        assert!(rule_sentence("session folder", "edit", "/repo/**", "allow").contains("this session only"));
        assert!(rule_sentence("global", "mytool", "x", "allow").contains("mytool calls"));
    }

    #[test]
    fn examples_are_valid_drafts() {
        assert!(EXAMPLES.len() >= 3 && EXAMPLES.len() <= 4);
        for ex in EXAMPLES {
            assert!(crate::approvals::validate_rule_draft(ex.permission, ex.pattern, ex.action).is_ok(), "{ex:?}");
            assert!(matches!(ex.scope, "global" | "project"), "{ex:?}");
        }
    }

    #[test]
    fn scope_explainers_name_lifetime() {
        assert!(scope_explainer("global").contains("every project"));
        assert!(scope_explainer("project").contains("only here"));
        assert!(scope_explainer("global").contains("until you remove"));
    }

    #[test]
    fn daemon_reason_extracts_error_field() {
        assert_eq!(
            daemon_reason("HTTP 400 {\"error\":\"allow rules require confirm\"}"),
            "allow rules require confirm"
        );
        assert_eq!(daemon_reason("HTTP 502 bad gateway"), "HTTP 502 bad gateway");
        assert_eq!(daemon_reason("HTTP 400 {\"error\":\"\"}"), "HTTP 400 {\"error\":\"\"}");
        assert_eq!(daemon_reason("  "), "the daemon gave no reason");
    }

    #[test]
    fn feedback_restates_and_flags_failures() {
        let saved = RuleFeedback::Saved("Allow x.".into());
        assert_eq!(saved.text(), "Saved. Allow x.");
        assert!(saved.class().contains("hint"));
        let failed = RuleFeedback::Failed("bad pattern".into());
        assert_eq!(failed.text(), "Not saved: bad pattern");
        assert!(failed.class().contains("notice"));
        assert_eq!(RuleFeedback::None.text(), "");
    }

    #[test]
    fn explain_path_encodes_both_values() {
        let enc = |s: &str| s.replace(' ', "%20").replace('&', "%26");
        assert_eq!(
            explain_path("bash", " git push origin main&x=1 ", enc),
            "/v1/permissions/explain?permission=bash&pattern=git%20push%20origin%20main%26x=1"
        );
    }

    #[test]
    fn explanation_decodes_go_field_names() {
        let raw = r#"{"Permission":"bash","Pattern":"git push origin main","Action":"ask","Layer":"project",
            "Matched":{"Layer":"project","Permission":"bash","Pattern":"git push*","Action":"ask"},
            "Trail":[],"Summary":"…","ManagedBlocks":false,"ManagedSummary":""}"#;
        let ex: Explanation = serde_json::from_str(raw).unwrap();
        assert_eq!(ex.action, "ask");
        assert_eq!(ex.matched.pattern, "git push*");
        assert_eq!(
            verdict_line(&ex),
            "Asks you first. Decided by the project rule bash \"git push*\" → ask."
        );
    }

    #[test]
    fn verdict_without_match_names_default() {
        let ex = Explanation { action: "ask".into(), ..Default::default() };
        assert_eq!(verdict_line(&ex), "Asks you first. No rule matched, so the default applies.");
        let ex = Explanation { action: "deny".into(), ..Default::default() };
        assert!(verdict_line(&ex).starts_with("Blocked"));
        let ex = Explanation {
            action: "ask".into(),
            matched: ExplainMatch { layer: "default".into(), permission: "bash".into(), pattern: "*".into(), action: "ask".into() },
            ..Default::default()
        };
        assert!(verdict_line(&ex).ends_with("No rule matched, so the default applies."));
    }

    #[test]
    fn verdict_names_mode_and_layer_in_english() {
        let ex = Explanation {
            action: "allow".into(),
            matched: ExplainMatch { layer: "mode-upgrade".into(), permission: "bash".into(), pattern: "*".into(), action: "allow".into() },
            ..Default::default()
        };
        assert_eq!(verdict_line(&ex), "Allowed: runs without asking. The live approval mode decided it, not a rule.");
        let ex = Explanation {
            action: "deny".into(),
            matched: ExplainMatch { layer: "config".into(), permission: "bash".into(), pattern: "npm publish*".into(), action: "deny".into() },
            ..Default::default()
        };
        assert_eq!(verdict_line(&ex), "Blocked: never runs. Decided by the global rule bash \"npm publish*\" → deny.");
    }
}
