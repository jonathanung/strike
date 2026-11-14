//! Permissions panel: presets, preset diff, and explain (#1504).
//!
//! Read-only lookups against three daemon routes that already exist:
//! `GET /v1/permissions/presets`, `GET /v1/permissions/diff?left=&right=`,
//! and `GET /v1/permissions/explain?permission=&pattern=&preset=`. Nothing
//! here mutates rules, applies a preset, runs the sampled call, or persists
//! the sample. A host without the Permissions service answers 501
//! (capability unavailable) and the whole section hides; any other fetch
//! error stays inline in the section.
//!
//! The host DTOs carry no json tags, so the wire keys are the Go field
//! names (`ID`, `Name`, `Changes`, …). Every daemon string is rendered as a
//! text node; Leptos escapes it, so rule and pattern strings cannot inject
//! markup.

use leptos::prelude::*;
use leptos::task::spawn_local;
use load_guard::LoadEpoch;
use serde::Deserialize;

use crate::api::get_text;
use crate::load_epoch::begin;

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct PresetInfo {
    #[serde(default, rename = "ID")]
    pub id: String,
    #[serde(default, rename = "Name")]
    pub name: String,
    #[serde(default, rename = "Description")]
    pub description: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct PresetsDocument {
    #[serde(default)]
    pub presets: Vec<PresetInfo>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct RuleRef {
    #[serde(default, rename = "Layer")]
    pub layer: String,
    #[serde(default, rename = "Permission")]
    pub permission: String,
    #[serde(default, rename = "Pattern")]
    pub pattern: String,
    #[serde(default, rename = "Action")]
    pub action: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct RuleDelta {
    #[serde(default, rename = "Kind")]
    pub kind: String,
    #[serde(default, rename = "Layer")]
    pub layer: String,
    #[serde(default, rename = "Before")]
    pub before: Option<RuleRef>,
    #[serde(default, rename = "After")]
    pub after: Option<RuleRef>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct PresetDiff {
    #[serde(default, rename = "LeftLabel")]
    pub left_label: String,
    #[serde(default, rename = "RightLabel")]
    pub right_label: String,
    #[serde(default, rename = "Changes")]
    pub changes: Vec<RuleDelta>,
    #[serde(default, rename = "Summary")]
    pub summary: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct Explanation {
    #[serde(default, rename = "Permission")]
    pub permission: String,
    #[serde(default, rename = "Pattern")]
    pub pattern: String,
    #[serde(default, rename = "Action")]
    pub action: String,
    #[serde(default, rename = "Layer")]
    pub layer: String,
    #[serde(default, rename = "Matched")]
    pub matched: RuleRef,
    #[serde(default, rename = "Trail")]
    pub trail: Vec<RuleRef>,
    #[serde(default, rename = "Summary")]
    pub summary: String,
    #[serde(default, rename = "EvalPath")]
    pub eval_path: String,
    #[serde(default, rename = "FactSummary")]
    pub fact_summary: String,
    #[serde(default, rename = "DryRunPreset")]
    pub dry_run_preset: String,
    #[serde(default, rename = "ManagedBlocks")]
    pub managed_blocks: bool,
    #[serde(default, rename = "ManagedSummary")]
    pub managed_summary: String,
    #[serde(default, rename = "SandboxSummary")]
    pub sandbox_summary: String,
}

/// Percent-encode one query value. Pure so request shapes test off-wasm.
fn encode_query(raw: &str) -> String {
    let mut out = String::with_capacity(raw.len());
    for b in raw.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

pub const PRESETS_PATH: &str = "/v1/permissions/presets";

/// Diff path for two presets. `None` when either side is blank or both
/// sides are the same preset: that is a no-op state, not a request.
pub fn diff_path(left: &str, right: &str) -> Option<String> {
    let (left, right) = (left.trim(), right.trim());
    if left.is_empty() || right.is_empty() || left == right {
        return None;
    }
    Some(format!(
        "/v1/permissions/diff?left={}&right={}",
        encode_query(left),
        encode_query(right)
    ))
}

/// Explain path for a sample call. `None` when the permission is blank
/// (the daemon requires it). Pattern and preset are optional and omitted
/// when blank; a preset makes the daemon dry-run without mutating anything.
pub fn explain_path(permission: &str, pattern: &str, preset: &str) -> Option<String> {
    let permission = permission.trim();
    if permission.is_empty() {
        return None;
    }
    let mut path = format!("/v1/permissions/explain?permission={}", encode_query(permission));
    let pattern = pattern.trim();
    if !pattern.is_empty() {
        path.push_str(&format!("&pattern={}", encode_query(pattern)));
    }
    let preset = preset.trim();
    if !preset.is_empty() {
        path.push_str(&format!("&preset={}", encode_query(preset)));
    }
    Some(path)
}

/// True when the daemon reported the Permissions capability absent (501).
pub fn is_capability_unavailable(err: &str) -> bool {
    err.starts_with("HTTP 501")
}

/// Human error text from a `get_text` failure: the daemon's `error` field
/// when the body is the usual `{"error": "..."}`, otherwise the raw text.
pub fn error_text(err: &str) -> String {
    if let Some(start) = err.find('{') {
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(&err[start..]) {
            if let Some(msg) = v.get("error").and_then(|m| m.as_str()) {
                if !msg.trim().is_empty() {
                    return msg.trim().to_string();
                }
            }
        }
    }
    err.to_string()
}

/// Default diff pair: the first two shipped presets. The presets DTO does
/// not mark which preset (if any) is currently effective, so the issue's
/// preferred "current vs strictest" pair is not derivable client-side.
pub fn default_pair(presets: &[PresetInfo]) -> (String, String) {
    let left = presets.first().map(|p| p.id.clone()).unwrap_or_default();
    let right = presets.get(1).map(|p| p.id.clone()).unwrap_or_default();
    (left, right)
}

/// One rule as `layer · permission · pattern → action`. Blank fields are
/// skipped; an empty pattern is shown as `*` (the daemon's default).
pub fn rule_text(rule: &RuleRef) -> String {
    let pattern = if rule.pattern.is_empty() { "*" } else { rule.pattern.as_str() };
    let mut parts: Vec<&str> = Vec::new();
    if !rule.layer.is_empty() {
        parts.push(&rule.layer);
    }
    if !rule.permission.is_empty() {
        parts.push(&rule.permission);
    }
    parts.push(pattern);
    let head = parts.join(" · ");
    if rule.action.is_empty() {
        head
    } else {
        format!("{head} → {}", rule.action)
    }
}

/// One diff row's text. `changed` shows before ⇒ after.
pub fn delta_text(delta: &RuleDelta) -> String {
    match (&delta.before, &delta.after) {
        (Some(b), Some(a)) => format!("{} ⇒ {}", rule_text(b), rule_text(a)),
        (None, Some(a)) => rule_text(a),
        (Some(b), None) => rule_text(b),
        (None, None) => delta.layer.clone(),
    }
}

/// CSS modifier for a diff row's kind. Unknown kinds get the neutral row.
pub fn delta_class(kind: &str) -> &'static str {
    match kind {
        "added" => "perm-explain-delta add",
        "removed" => "perm-explain-delta del",
        "changed" => "perm-explain-delta changed",
        _ => "perm-explain-delta",
    }
}

/// Explain result heading. A dry-run is always labelled with its preset.
pub fn explain_heading(ex: &Explanation) -> String {
    let decision = if ex.action.is_empty() {
        "no decision".to_string()
    } else {
        ex.action.clone()
    };
    if ex.dry_run_preset.is_empty() {
        format!("Decision: {decision}")
    } else {
        format!("Dry run under {}: {decision}", ex.dry_run_preset)
    }
}

/// Winning-rule line, or `None` when nothing matched (the daemon's default
/// applied).
pub fn winning_rule(ex: &Explanation) -> Option<String> {
    let m = &ex.matched;
    if m.layer.is_empty() && m.permission.is_empty() && m.pattern.is_empty() && m.action.is_empty() {
        None
    } else {
        Some(format!("Winning rule: {}", rule_text(m)))
    }
}

/// Fallback label for a preset whose name is blank.
fn preset_label(p: &PresetInfo) -> String {
    if p.name.is_empty() {
        p.id.clone()
    } else {
        format!("{} ({})", p.name, p.id)
    }
}

/// Presets, preset diff and explain section for the Permissions panel.
/// Fetches presets when the panel opens (the component mounts with it).
#[component]
pub fn PermissionsExplain(base: RwSignal<String>, token: RwSignal<String>) -> impl IntoView {
    let presets = RwSignal::new(Vec::<PresetInfo>::new());
    let unavailable = RwSignal::new(false);
    let presets_error = RwSignal::new(String::new());
    let left = RwSignal::new(String::new());
    let right = RwSignal::new(String::new());
    let diff = RwSignal::new(None::<PresetDiff>);
    let diff_error = RwSignal::new(String::new());
    let diff_epoch = RwSignal::new(LoadEpoch::new());
    let perm = RwSignal::new("bash".to_string());
    let pattern = RwSignal::new(String::new());
    let dry_preset = RwSignal::new(String::new());
    let explained = RwSignal::new(None::<Explanation>);
    let explain_error = RwSignal::new(String::new());
    let explain_epoch = RwSignal::new(LoadEpoch::new());

    {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match get_text(&b, &t, PRESETS_PATH).await {
                Ok(text) => match serde_json::from_str::<PresetsDocument>(&text) {
                    Ok(doc) => {
                        let (l, r) = default_pair(&doc.presets);
                        let _ = presets.try_set(doc.presets);
                        let _ = left.try_set(l);
                        let _ = right.try_set(r);
                    }
                    Err(e) => {
                        let _ = presets_error.try_set(format!("Presets: {e}"));
                    }
                },
                Err(e) if is_capability_unavailable(&e) => {
                    let _ = unavailable.try_set(true);
                }
                Err(e) => {
                    let _ = presets_error.try_set(format!("Presets: {}", error_text(&e)));
                }
            }
        });
    }

    let run_diff = move || {
        let gen = begin(diff_epoch);
        diff.set(None);
        diff_error.set(String::new());
        let Some(path) = diff_path(&left.get_untracked(), &right.get_untracked()) else {
            return;
        };
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let result = get_text(&b, &t, &path).await;
            if !diff_epoch.try_with_untracked(|e| e.accept(gen)).unwrap_or(false) {
                return;
            }
            match result.and_then(|text| {
                serde_json::from_str::<PresetDiff>(&text).map_err(|e| e.to_string())
            }) {
                Ok(d) => diff.set(Some(d)),
                Err(e) => diff_error.set(format!("Diff: {}", error_text(&e))),
            }
        });
    };

    let run_explain = move || {
        let gen = begin(explain_epoch);
        explained.set(None);
        explain_error.set(String::new());
        let Some(path) = explain_path(&perm.get_untracked(), &pattern.get_untracked(), &dry_preset.get_untracked()) else {
            explain_error.set("Enter a permission name, such as bash or edit.".into());
            return;
        };
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let result = get_text(&b, &t, &path).await;
            if !explain_epoch.try_with_untracked(|e| e.accept(gen)).unwrap_or(false) {
                return;
            }
            match result.and_then(|text| {
                serde_json::from_str::<Explanation>(&text).map_err(|e| e.to_string())
            }) {
                Ok(ex) => explained.set(Some(ex)),
                Err(e) => explain_error.set(format!("Explain: {}", error_text(&e))),
            }
        });
    };

    // Options carry a reactive `selected` as well as the select's
    // `prop:value`: presets arrive after the selects mount, and a value set
    // before its option exists would otherwise fall back to the first
    // option while the signal still holds the default pair.
    let preset_options = move |chosen: RwSignal<String>| {
        move || {
            presets
                .get()
                .into_iter()
                .map(|p| {
                    let id = p.id.clone();
                    let value = p.id.clone();
                    let label = preset_label(&p);
                    view! { <option value=value selected=move || chosen.get() == id>{label}</option> }
                })
                .collect_view()
        }
    };

    view! {
        <Show when=move || !unavailable.get()>
            <section class="perm-explain" data-permissions-explain="true">
                <h3>"Presets"</h3>
                <p class="hint">"Shipped rulesets. Read-only here: listing, comparing and dry-running a preset never applies it."</p>
                <Show when=move || !presets_error.get().is_empty()>
                    <p class="notice">{move || presets_error.get()}</p>
                </Show>
                <ul class="plain perm-explain-presets" data-permission-presets="true">
                    {move || presets.get().into_iter().map(|p| {
                        let label = preset_label(&p);
                        let description = p.description.clone();
                        view! {
                            <li class="perm-explain-preset" data-preset-id=p.id.clone()>
                                <strong>{label}</strong>
                                <span class="hint">{description}</span>
                            </li>
                        }
                    }).collect_view()}
                </ul>

                <h3>"Compare presets"</h3>
                <div class="approvals-form">
                    <select aria-label="Left preset" prop:value=move || left.get() on:change=move |ev| left.set(event_target_value(&ev))>
                        {preset_options(left)}
                    </select>
                    <select aria-label="Right preset" prop:value=move || right.get() on:change=move |ev| right.set(event_target_value(&ev))>
                        {preset_options(right)}
                    </select>
                    <button type="button" class="secondary" data-preset-diff-run="true" on:click=move |_| run_diff()>"Compare"</button>
                </div>
                <Show when=move || { let (l, r) = (left.get(), right.get()); !l.is_empty() && l == r }>
                    <p class="hint">"Same preset on both sides. Pick two different presets to compare."</p>
                </Show>
                <Show when=move || !diff_error.get().is_empty()>
                    <p class="notice">{move || diff_error.get()}</p>
                </Show>
                {move || diff.get().map(|d| {
                    let heading = format!("{} → {}", d.left_label, d.right_label);
                    let empty = d.changes.is_empty();
                    view! {
                        <div class="perm-explain-diff" data-preset-diff="true">
                            <p class="hint">{heading}</p>
                            <Show when=move || empty>
                                <p class="hint">"No rule differences."</p>
                            </Show>
                            <ul class="plain">
                                {d.changes.into_iter().map(|delta| {
                                    let class = delta_class(&delta.kind);
                                    let kind = delta.kind.clone();
                                    let text = delta_text(&delta);
                                    view! {
                                        <li class=class>
                                            <span class="perm-explain-kind">{kind}</span>
                                            <span class="perm-explain-rule">{text}</span>
                                        </li>
                                    }
                                }).collect_view()}
                            </ul>
                        </div>
                    }
                })}

                <h3>"Explain a call"</h3>
                <p class="hint">"Asks the daemon which rule decides a sample call. Nothing runs and nothing is saved."</p>
                <div class="approvals-form">
                    <input
                        aria-label="Explain permission"
                        placeholder="permission, such as bash"
                        prop:value=move || perm.get()
                        on:input=move |ev| perm.set(event_target_value(&ev))
                    />
                    <input
                        aria-label="Explain pattern"
                        placeholder="pattern, such as rm -rf build"
                        prop:value=move || pattern.get()
                        on:input=move |ev| pattern.set(event_target_value(&ev))
                    />
                    <select aria-label="Dry-run preset" prop:value=move || dry_preset.get() on:change=move |ev| dry_preset.set(event_target_value(&ev))>
                        <option value="">"Current rules"</option>
                        {preset_options(dry_preset)}
                    </select>
                    <button type="button" class="secondary" data-permission-explain-run="true" on:click=move |_| run_explain()>"Explain"</button>
                </div>
                <Show when=move || !explain_error.get().is_empty()>
                    <p class="notice">{move || explain_error.get()}</p>
                </Show>
                {move || explained.get().map(|ex| {
                    let heading = explain_heading(&ex);
                    let winner = winning_rule(&ex).unwrap_or_else(|| "No rule matched; the default applies.".into());
                    let layer = if ex.layer.is_empty() { String::new() } else { format!("Layer: {}", ex.layer) };
                    let managed = if ex.managed_blocks { ex.managed_summary.clone() } else { String::new() };
                    let lines: Vec<String> = [layer, managed, ex.fact_summary.clone(), ex.sandbox_summary.clone()]
                        .into_iter()
                        .filter(|l| !l.is_empty())
                        .collect();
                    let trail: Vec<String> = ex.trail.iter().map(rule_text).collect();
                    let has_trail = !trail.is_empty();
                    let summary = ex.summary.clone();
                    let dry = !ex.dry_run_preset.is_empty();
                    view! {
                        <div class="perm-explain-result" data-permission-explain-result="true" data-dry-run=if dry { "true" } else { "false" }>
                            <p><strong>{heading}</strong></p>
                            <p class="perm-explain-rule">{winner}</p>
                            {lines.into_iter().map(|l| view! { <p class="hint">{l}</p> }).collect_view()}
                            <Show when=move || has_trail>
                                <p class="hint">"Matching rules, in order (last match wins):"</p>
                            </Show>
                            <ol class="perm-explain-trail">
                                {trail.into_iter().map(|t| view! { <li class="perm-explain-rule">{t}</li> }).collect_view()}
                            </ol>
                            <pre class="perm-explain-summary">{summary}</pre>
                        </div>
                    }
                })}
            </section>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn presets_document_decodes_go_field_names() {
        let doc: PresetsDocument = serde_json::from_str(
            r#"{"presets":[{"ID":"read-only","Name":"Read only","Description":"No writes"},{"ID":"dev"}]}"#,
        )
        .unwrap();
        assert_eq!(doc.presets.len(), 2);
        assert_eq!(doc.presets[0].id, "read-only");
        assert_eq!(doc.presets[0].name, "Read only");
        assert_eq!(doc.presets[1].name, "");
        assert_eq!(default_pair(&doc.presets), ("read-only".into(), "dev".into()));
        assert_eq!(default_pair(&doc.presets[..1]), ("read-only".into(), String::new()));
        assert_eq!(default_pair(&[]), (String::new(), String::new()));
    }

    #[test]
    fn diff_path_skips_same_or_blank_and_encodes() {
        assert_eq!(diff_path("dev", "dev"), None);
        assert_eq!(diff_path(" dev ", "dev"), None);
        assert_eq!(diff_path("", "dev"), None);
        assert_eq!(diff_path("dev", "  "), None);
        assert_eq!(
            diff_path("read-only", "yolo-with-sandbox").as_deref(),
            Some("/v1/permissions/diff?left=read-only&right=yolo-with-sandbox")
        );
        assert_eq!(
            diff_path("a&b", "c d").as_deref(),
            Some("/v1/permissions/diff?left=a%26b&right=c%20d")
        );
    }

    #[test]
    fn explain_path_requires_permission_and_omits_blank_optionals() {
        assert_eq!(explain_path("  ", "x", "dev"), None);
        assert_eq!(
            explain_path("bash", "", "").as_deref(),
            Some("/v1/permissions/explain?permission=bash")
        );
        assert_eq!(
            explain_path("bash", "rm -rf /tmp/x", "").as_deref(),
            Some("/v1/permissions/explain?permission=bash&pattern=rm%20-rf%20%2Ftmp%2Fx")
        );
        assert_eq!(
            explain_path("edit", "*.go", "read-only").as_deref(),
            Some("/v1/permissions/explain?permission=edit&pattern=%2A.go&preset=read-only")
        );
        // A pattern cannot smuggle extra query parameters.
        assert_eq!(
            explain_path("bash", "x&preset=yolo", "").as_deref(),
            Some("/v1/permissions/explain?permission=bash&pattern=x%26preset%3Dyolo")
        );
    }

    #[test]
    fn capability_unavailable_and_error_text() {
        assert!(is_capability_unavailable(
            r#"HTTP 501 {"error":"permissions capability unavailable on this host"}"#
        ));
        assert!(!is_capability_unavailable("HTTP 500"));
        assert!(!is_capability_unavailable("HTTP 400 {}"));
        assert_eq!(
            error_text(r#"HTTP 400 {"error":"unknown preset \"nope\""}"#),
            r#"unknown preset "nope""#
        );
        assert_eq!(error_text("HTTP 502"), "HTTP 502");
        assert_eq!(error_text("HTTP 400 {not json"), "HTTP 400 {not json");
        assert_eq!(error_text(r#"HTTP 400 {"error":"  "}"#), r#"HTTP 400 {"error":"  "}"#);
    }

    #[test]
    fn diff_rows_render_added_removed_changed() {
        let d: PresetDiff = serde_json::from_str(
            r#"{"LeftLabel":"read-only","RightLabel":"dev","Summary":"s","Changes":[
                {"Kind":"added","Layer":"preset","Before":null,"After":{"Layer":"preset","Permission":"edit","Pattern":"","Action":"allow"}},
                {"Kind":"removed","Layer":"preset","Before":{"Layer":"preset","Permission":"bash","Pattern":"rm *","Action":"deny"},"After":null},
                {"Kind":"changed","Layer":"preset","Before":{"Permission":"bash","Pattern":"git *","Action":"ask"},"After":{"Permission":"bash","Pattern":"git *","Action":"allow"}},
                {"Kind":"weird","Layer":"x"}
            ]}"#,
        )
        .unwrap();
        assert_eq!(d.left_label, "read-only");
        assert_eq!(d.changes.len(), 4);
        assert_eq!(delta_text(&d.changes[0]), "preset · edit · * → allow");
        assert_eq!(delta_text(&d.changes[1]), "preset · bash · rm * → deny");
        assert_eq!(delta_text(&d.changes[2]), "bash · git * → ask ⇒ bash · git * → allow");
        assert_eq!(delta_text(&d.changes[3]), "x");
        assert_eq!(delta_class("added"), "perm-explain-delta add");
        assert_eq!(delta_class("removed"), "perm-explain-delta del");
        assert_eq!(delta_class("changed"), "perm-explain-delta changed");
        assert_eq!(delta_class("weird"), "perm-explain-delta");
        let empty: PresetDiff = serde_json::from_str("{}").unwrap();
        assert!(empty.changes.is_empty());
    }

    #[test]
    fn explain_heading_labels_dry_run_and_winner() {
        let ex: Explanation = serde_json::from_str(
            r#"{"Permission":"bash","Pattern":"rm -rf x","Action":"deny","Layer":"config",
                "Matched":{"Layer":"config","Permission":"bash","Pattern":"rm *","Action":"deny"},
                "Trail":[{"Layer":"default","Permission":"*","Pattern":"*","Action":"ask"},
                         {"Layer":"config","Permission":"bash","Pattern":"rm *","Action":"deny"}],
                "Summary":"line1\nline2","ManagedBlocks":false}"#,
        )
        .unwrap();
        assert_eq!(explain_heading(&ex), "Decision: deny");
        assert_eq!(winning_rule(&ex).as_deref(), Some("Winning rule: config · bash · rm * → deny"));
        assert_eq!(ex.trail.len(), 2);

        let dry: Explanation = serde_json::from_str(
            r#"{"Permission":"edit","Action":"allow","DryRunPreset":"dev"}"#,
        )
        .unwrap();
        assert_eq!(explain_heading(&dry), "Dry run under dev: allow");
        assert_eq!(winning_rule(&dry), None);

        // Unknown preset: daemon returns Summary error text and empty Action.
        let bad: Explanation = serde_json::from_str(
            r#"{"Permission":"edit","Summary":"unknown preset","DryRunPreset":"nope"}"#,
        )
        .unwrap();
        assert_eq!(explain_heading(&bad), "Dry run under nope: no decision");
    }

    #[test]
    fn rule_text_keeps_markup_as_plain_text() {
        // Rendering is a text node; the helper must not alter or strip the
        // string either, so what the user sees is exactly the stored rule.
        let r = RuleRef {
            layer: String::new(),
            permission: "bash".into(),
            pattern: "<img src=x onerror=alert(1)>".into(),
            action: String::new(),
        };
        assert_eq!(rule_text(&r), "bash · <img src=x onerror=alert(1)>");
    }
}
