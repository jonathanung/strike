//! Text Link Patterns (#1383): user regex → URL rules that linkify
//! transcript mentions at display time.
//!
//! Matching, validation and URL building live in the dependency-light
//! `link-patterns` crate (host-tested in CI). This module owns:
//! - persistence (#1879): the rule list lives on the gateway as
//!   `webui.linkPatterns` (`GET`/`PATCH /v1/settings`), so every device
//!   paired to it shares one list. The gateway validates the list with the
//!   same rules and refuses an invalid one. A list this browser kept in
//!   localStorage (`strike.linkPatterns`, #1383 slice 1) is moved to the
//!   gateway once and the local key is then deleted. A gateway too old to
//!   report `linkPatterns` keeps the per-browser list;
//! - the live rule set: one reactive signal, so saving in Settings
//!   re-renders already-mounted transcript text;
//! - render hooks for plain-text chunks ([`linked_text`]) and inline code
//!   ([`linked_code`]) used by the transcript and the shared Markdown view.
//!   Built-in links (URLs, file paths, chips) are rendered by their own
//!   paths and never reach these hooks;
//! - the Settings section.
//!
//! With no rules the hooks render the same text they always did; the
//! stored transcript is never touched.

use std::cell::RefCell;
use std::sync::{Arc, OnceLock};

use leptos::prelude::*;
use leptos::task::spawn_local;
use ::link_patterns::{validate, validate_all, LinkRules, Piece, Rule, MAX_NAME_LEN, MAX_RULES};
use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::api::{get_json, patch_json};

const STORAGE_KEY: &str = "strike.linkPatterns";

#[derive(Serialize, Deserialize, Default)]
struct StoredRule {
    #[serde(default)]
    name: String,
    #[serde(default)]
    pattern: String,
    #[serde(default)]
    template: String,
}

fn storage() -> Option<web_sys::Storage> {
    web_sys::window()?.local_storage().ok().flatten()
}

/// Parse the stored JSON. Anything malformed reads as no rules; invalid
/// individual rules are kept for editing but never applied
/// ([`LinkRules::new`] drops them).
pub fn parse_stored(raw: &str) -> Vec<Rule> {
    serde_json::from_str::<Vec<StoredRule>>(raw)
        .unwrap_or_default()
        .into_iter()
        .map(|r| Rule {
            name: r.name,
            pattern: r.pattern,
            template: r.template,
        })
        .collect()
}

pub fn encode_stored(rules: &[Rule]) -> String {
    let stored: Vec<StoredRule> = rules
        .iter()
        .map(|r| StoredRule {
            name: r.name.clone(),
            pattern: r.pattern.clone(),
            template: r.template.clone(),
        })
        .collect();
    serde_json::to_string(&stored).unwrap_or_else(|_| "[]".into())
}

fn load_stored() -> Vec<Rule> {
    storage()
        .and_then(|s| s.get_item(STORAGE_KEY).ok().flatten())
        .map(|raw| parse_stored(&raw))
        .unwrap_or_default()
}

fn clear_stored() {
    if let Some(s) = storage() {
        let _ = s.remove_item(STORAGE_KEY);
    }
}

/// `linkPatterns` from a `GET /v1/settings` snapshot. `None` means the
/// gateway does not store rules (older than #1879): keep the local list.
pub fn gateway_rules(snapshot: &Value) -> Option<Vec<Rule>> {
    let list = snapshot.get("linkPatterns")?;
    if list.is_null() {
        return Some(Vec::new());
    }
    Some(parse_stored(&list.to_string()))
}

/// The `PATCH /v1/settings` body that replaces the gateway list.
pub fn patch_body(rules: &[Rule]) -> Value {
    let stored: Value = serde_json::from_str(&encode_stored(rules)).unwrap_or(Value::Array(Vec::new()));
    serde_json::json!({ "linkPatterns": stored })
}

/// The one-time move of this browser's list to the gateway: the gateway's
/// rules first, then each valid local rule it does not already have, up
/// to [`MAX_RULES`]. `None` when the gateway list already covers them
/// (nothing to send). Invalid local rules were never applied and are
/// dropped.
pub fn migration_plan(local: &[Rule], remote: &[Rule]) -> Option<Vec<Rule>> {
    let mut merged: Vec<Rule> = remote.to_vec();
    for rule in local.iter().map(normalise) {
        if merged.len() >= MAX_RULES {
            break;
        }
        if validate(&rule).is_ok() && !merged.contains(&rule) {
            merged.push(rule);
        }
    }
    (merged.len() != remote.len()).then_some(merged)
}

/// Where the saved list lives for the connected gateway.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Store {
    /// Not read from the gateway yet.
    Unknown,
    /// `webui.linkPatterns` on the gateway, shared by paired devices.
    Gateway,
    /// This browser's localStorage (gateway older than #1879).
    Browser,
}

struct Saved {
    rules: ArcRwSignal<Vec<Rule>>,
    compiled: ArcRwSignal<Arc<LinkRules>>,
    store: ArcRwSignal<Store>,
}

fn saved() -> &'static Saved {
    static SAVED: OnceLock<Saved> = OnceLock::new();
    SAVED.get_or_init(|| {
        // Until the gateway answers, render with this browser's list (the
        // pre-#1879 behaviour) so a page load never flashes unlinked text
        // for users who have not migrated yet.
        let rules = load_stored();
        Saved {
            compiled: ArcRwSignal::new(Arc::new(LinkRules::new(&rules))),
            rules: ArcRwSignal::new(rules),
            store: ArcRwSignal::new(Store::Unknown),
        }
    })
}

/// The live, compiled rule set every render hook reads.
fn live() -> ArcRwSignal<Arc<LinkRules>> {
    saved().compiled.clone()
}

fn set_saved(rules: Vec<Rule>, store: Store) {
    let s = saved();
    s.compiled.set(Arc::new(LinkRules::new(&rules)));
    s.rules.set(rules);
    s.store.set(store);
}

/// Read the gateway's rules, moving this browser's legacy list there once.
/// Returns the saved list now in effect.
pub async fn sync(base: &str, token: &str) -> Result<Vec<Rule>, String> {
    let snapshot = get_json::<Value>(base, token, "/v1/settings").await?;
    let Some(remote) = gateway_rules(&snapshot) else {
        let local = load_stored();
        set_saved(local.clone(), Store::Browser);
        return Ok(local);
    };
    let local = load_stored();
    let mut rules = remote;
    if !local.is_empty() {
        if let Some(merged) = migration_plan(&local, &rules) {
            // Keep the local key until the gateway accepted the list so a
            // failed move is retried on the next load, not lost.
            patch_json(base, token, "/v1/settings", patch_body(&merged)).await?;
            rules = merged;
        }
        clear_stored();
    }
    set_saved(rules.clone(), Store::Gateway);
    Ok(rules)
}

thread_local! {
    static SYNCED_BASE: RefCell<Option<String>> = const { RefCell::new(None) };
}

/// Sync once per gateway address (page load, or a switch to another
/// gateway). Errors leave the current rules in place.
pub fn sync_once(base: &str, token: &str) {
    let fresh = SYNCED_BASE.with(|cell| {
        let mut cell = cell.borrow_mut();
        if cell.as_deref() == Some(base) {
            false
        } else {
            *cell = Some(base.to_string());
            true
        }
    });
    if !fresh {
        return;
    }
    let (b, t) = (base.to_string(), token.to_string());
    spawn_local(async move {
        if sync(&b, &t).await.is_err() {
            // Let the next bootstrap retry.
            SYNCED_BASE.with(|cell| *cell.borrow_mut() = None);
        }
    });
}

/// Persist a validated list where the connected gateway keeps it.
async fn persist(base: &str, token: &str, rules: &[Rule]) -> Result<(), String> {
    let store = saved().store.get_untracked();
    if store == Store::Browser {
        if let Some(s) = storage() {
            s.set_item(STORAGE_KEY, &encode_stored(rules))
                .map_err(|_| "Could not write browser storage.".to_string())?;
        }
        set_saved(rules.to_vec(), Store::Browser);
        return Ok(());
    }
    patch_json(base, token, "/v1/settings", patch_body(rules))
        .await
        .map_err(|e| crate::guardrails::daemon_reason(&e))?;
    set_saved(rules.to_vec(), Store::Gateway);
    Ok(())
}

/// The Settings blurb's storage sentence.
pub fn store_note(store: Store) -> &'static str {
    match store {
        Store::Gateway => "Rules are saved on the gateway and shared by every device paired to it.",
        Store::Browser => "This gateway is too old to store rules, so they are saved in this browser.",
        Store::Unknown => "Reading the saved rules from the gateway…",
    }
}

/// Normalise a draft for saving: the name is trimmed and so is the
/// template (a URL never has meaningful edge whitespace). The pattern is
/// kept verbatim — whitespace is significant in a regex.
pub fn normalise(rule: &Rule) -> Rule {
    Rule {
        name: rule.name.trim().to_string(),
        pattern: rule.pattern.clone(),
        template: rule.template.trim().to_string(),
    }
}

/// Outcome line for a save attempt.
pub fn save_outcome(rules: &[Rule]) -> Result<String, String> {
    match validate_all(rules) {
        Ok(()) => Ok(match rules.len() {
            0 => "Saved. No rules: the transcript renders as before.".to_string(),
            1 => "Saved 1 rule.".to_string(),
            n => format!("Saved {n} rules."),
        }),
        Err((Some(i), e)) => Err(format!("Rule {}: {} Nothing was saved.", i + 1, e.message())),
        Err((None, e)) => Err(format!("{} Nothing was saved.", e.message())),
    }
}

fn user_link(text: String, href: String) -> AnyView {
    view! {
        <a class="sx-userlink" href=href target="_blank" rel="noopener noreferrer" data-link-pattern="true">{text}</a>
    }
    .into_any()
}

fn pieces_view(pieces: Vec<Piece>) -> AnyView {
    pieces
        .into_iter()
        .map(|p| match p {
            Piece::Text(t) => t.into_any(),
            Piece::Link { text, href } => user_link(text, href),
        })
        .collect_view()
        .into_any()
}

/// A plain-text chunk with user patterns applied (reactive to Settings).
pub fn linked_text(text: String) -> impl IntoView {
    let rules = live();
    move || {
        let r = rules.get();
        if r.is_empty() {
            return text.clone().into_any();
        }
        pieces_view(r.apply(&text))
    }
}

/// An inline-code span: a link only when a rule matches its whole text.
pub fn linked_code(text: String, class: Option<&'static str>) -> impl IntoView {
    let rules = live();
    move || {
        let code = view! { <code class=class>{text.clone()}</code> };
        match rules.get().whole(&text) {
            Some(href) => view! {
                <a class="sx-userlink" href=href target="_blank" rel="noopener noreferrer" data-link-pattern="true">{code}</a>
            }
            .into_any(),
            None => code.into_any(),
        }
    }
}

/// Inline error for one draft row: shown once the row has any content or
/// a save was attempted, so a fresh empty row is not born red.
pub fn row_error(rule: &Rule, tried: bool) -> String {
    let touched = !(rule.name.is_empty() && rule.pattern.is_empty() && rule.template.is_empty());
    if !(touched || tried) {
        return String::new();
    }
    validate(&normalise(rule)).err().map(|e| e.message()).unwrap_or_default()
}

/// Swap `idx` with its neighbour `idx + delta` when both exist.
pub fn moved<T>(mut items: Vec<T>, idx: usize, up: bool) -> Vec<T> {
    let other = if up { idx.checked_sub(1) } else { Some(idx + 1) };
    if let Some(j) = other.filter(|j| *j < items.len()) {
        if idx < items.len() {
            items.swap(idx, j);
        }
    }
    items
}

#[derive(Clone, Copy)]
struct Row {
    id: u64,
    name: RwSignal<String>,
    pattern: RwSignal<String>,
    template: RwSignal<String>,
}

impl Row {
    fn rule(&self) -> Rule {
        Rule {
            name: self.name.get(),
            pattern: self.pattern.get(),
            template: self.template.get(),
        }
    }

    fn rule_untracked(&self) -> Rule {
        Rule {
            name: self.name.get_untracked(),
            pattern: self.pattern.get_untracked(),
            template: self.template.get_untracked(),
        }
    }
}

#[component]
fn RuleInput(
    label: &'static str,
    field: &'static str,
    value: RwSignal<String>,
    #[prop(optional)] placeholder: &'static str,
    #[prop(optional)] maxlength: Option<usize>,
) -> impl IntoView {
    view! {
        <label class="lp-field">
            <span>{label}</span>
            <input
                type="text"
                spellcheck="false"
                autocomplete="off"
                data-lp-field=field
                placeholder=placeholder
                maxlength=maxlength.map(|n| n.to_string())
                prop:value=move || value.get()
                on:input=move |ev| value.set(event_target_value(&ev))
            />
        </label>
    }
}

/// Settings → Text Link Patterns.
#[component]
pub fn LinkPatternsSettings(base: RwSignal<String>, token: RwSignal<String>) -> impl IntoView {
    let next_id = StoredValue::new(0u64);
    let make_row = move |rule: Rule| {
        let id = next_id.get_value();
        next_id.set_value(id + 1);
        Row {
            id,
            name: RwSignal::new(rule.name),
            pattern: RwSignal::new(rule.pattern),
            template: RwSignal::new(rule.template),
        }
    };
    let saved_rules = saved().rules.clone();
    let store = saved().store.clone();
    let rows = RwSignal::new(saved_rules.get_untracked().into_iter().map(make_row).collect::<Vec<_>>());
    let tried = RwSignal::new(false);
    let busy = RwSignal::new(false);
    let note = RwSignal::new(String::new());
    let sample = RwSignal::new(String::new());
    let draft_rules = move || rows.with_untracked(|v| v.iter().map(|r| r.rule_untracked()).collect::<Vec<_>>());

    // Re-read the gateway list on open (another device may have saved).
    // Replace the rows only if nothing was edited while it loaded.
    {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let before = draft_rules();
        spawn_local(async move {
            let result = sync(&b, &t).await;
            // The section may have closed while the read was in flight.
            let Some(now) = rows.try_with_untracked(|v| v.iter().map(|r| r.rule_untracked()).collect::<Vec<_>>()) else {
                return;
            };
            match result {
                Ok(rules) if now == before => rows.set(rules.into_iter().map(make_row).collect()),
                Ok(_) => {}
                Err(e) => {
                    let _ = note.try_set(format!("Could not read the saved rules from the gateway: {e}"));
                }
            }
        });
    }

    let add = move |_| {
        rows.update(|v| {
            if v.len() < MAX_RULES {
                v.push(make_row(Rule::default()));
            }
        });
        note.set(String::new());
    };
    let save = move |_| {
        let rules: Vec<Rule> = draft_rules().iter().map(normalise).collect();
        match save_outcome(&rules) {
            Ok(msg) => {
                let (b, t) = (base.get_untracked(), token.get_untracked());
                busy.set(true);
                note.set("Saving…".into());
                spawn_local(async move {
                    let outcome = persist(&b, &t, &rules).await;
                    match outcome {
                        Ok(()) => {
                            let _ = tried.try_set(false);
                            let _ = note.try_set(msg);
                        }
                        Err(e) => {
                            let _ = note.try_set(format!("{e} Nothing was saved."));
                        }
                    }
                    let _ = busy.try_set(false);
                });
            }
            Err(msg) => {
                tried.set(true);
                note.set(msg);
            }
        }
    };
    let revert = {
        let saved_rules = saved_rules.clone();
        move |_| {
            rows.set(saved_rules.get_untracked().into_iter().map(make_row).collect());
            tried.set(false);
            note.set("Reverted to the saved rules.".into());
        }
    };
    let preview = move || {
        let text = sample.get();
        if text.is_empty() {
            return view! { <span class="hint">"Type a sample to see which parts would link."</span> }.into_any();
        }
        let rules: Vec<Rule> = rows.with(|v| v.iter().map(|r| normalise(&r.rule())).collect());
        let compiled = LinkRules::new(&rules);
        let pieces = compiled.apply(&text);
        if !pieces.iter().any(|p| matches!(p, Piece::Link { .. })) {
            return view! { <span class="hint">"No rule matches this sample."</span> }.into_any();
        }
        pieces_view(pieces)
    };

    view! {
        <section class="card settings-section" id="settings-link-patterns" data-link-patterns=true>
            <h2 class="panel-title">"Text Link Patterns"</h2>
            <p class="hint">
                "Turn references in the transcript (ticket keys, build IDs) into links when they are shown. "
                "Each rule is a regular expression and an https:// or http:// URL; "
                <code>"{match}"</code>
                " inserts the matched text, percent-encoded. The first matching rule wins. "
                "Stored messages never change. "
                <span data-lp-store=true>{move || store_note(store.get())}</span>
            </p>
            <For each=move || rows.get() key=|row| row.id let:row>
                {
                    let id = row.id;
                    let index = move || rows.with(|v| v.iter().position(|r| r.id == id).unwrap_or(0));
                    let shift = move |up: bool| {
                        rows.update(|v| {
                            if let Some(i) = v.iter().position(|r| r.id == id) {
                                *v = moved(std::mem::take(v), i, up);
                            }
                        })
                    };
                    view! {
                        <fieldset class="lp-rule" data-lp-rule=true>
                            <legend>{move || format!("Rule {}", index() + 1)}</legend>
                            <RuleInput label="Name" field="name" value=row.name maxlength=MAX_NAME_LEN placeholder="Tracker" />
                            <RuleInput label="Pattern" field="pattern" value=row.pattern placeholder="\\b[A-Z]{2,}-\\d+\\b" />
                            <RuleInput label="URL template" field="template" value=row.template placeholder="https://tracker.example.com/browse/{match}" />
                            <div class="lp-row-actions">
                                <button
                                    type="button"
                                    class="secondary"
                                    aria-label=move || format!("Move rule {} up", index() + 1)
                                    disabled=move || index() == 0
                                    on:click=move |_| shift(true)
                                >"Up"</button>
                                <button
                                    type="button"
                                    class="secondary"
                                    aria-label=move || format!("Move rule {} down", index() + 1)
                                    disabled=move || index() + 1 >= rows.with(|v| v.len())
                                    on:click=move |_| shift(false)
                                >"Down"</button>
                                <button
                                    type="button"
                                    class="secondary"
                                    data-lp-remove=true
                                    aria-label=move || format!("Remove rule {}", index() + 1)
                                    on:click=move |_| rows.update(|v| v.retain(|r| r.id != id))
                                >"Remove"</button>
                            </div>
                            <p class="lp-error" role="alert" data-lp-error=true>{move || row_error(&row.rule(), tried.get())}</p>
                        </fieldset>
                    }
                }
            </For>
            <div class="lp-actions">
                <button
                    type="button"
                    class="secondary"
                    data-lp-add=true
                    disabled=move || rows.with(|v| v.len() >= MAX_RULES)
                    on:click=add
                >"Add rule"</button>
                <button type="button" data-lp-save=true disabled=move || busy.get() on:click=save>"Save rules"</button>
                <button type="button" class="secondary" data-lp-revert=true on:click=revert>"Revert"</button>
            </div>
            <p class="hint lp-note" role="status" aria-live="polite" data-lp-note=true>{move || note.get()}</p>
            <label class="lp-field">
                <span>"Try a sample"</span>
                <input
                    type="text"
                    data-lp-sample=true
                    placeholder="Fixed in ABC-123"
                    prop:value=move || sample.get()
                    on:input=move |ev| sample.set(event_target_value(&ev))
                />
            </label>
            <p class="lp-preview" data-lp-preview=true>{preview}</p>
        </section>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rule(name: &str, pattern: &str, template: &str) -> Rule {
        Rule {
            name: name.into(),
            pattern: pattern.into(),
            template: template.into(),
        }
    }

    #[test]
    fn stored_rules_round_trip_and_malformed_reads_empty() {
        let rules = vec![rule("Jira", r"\b[A-Z]+-\d+\b", "https://t.example/{match}")];
        assert_eq!(parse_stored(&encode_stored(&rules)), rules);
        assert!(parse_stored("not json").is_empty());
        assert!(parse_stored("{}").is_empty());
        assert_eq!(parse_stored(r#"[{"name":"x"}]"#), vec![rule("x", "", "")]);
    }

    #[test]
    fn normalise_trims_name_and_template_not_pattern() {
        let n = normalise(&rule("  A ", " x ", " https://t.example/{match} "));
        assert_eq!(n, rule("A", " x ", "https://t.example/{match}"));
    }

    #[test]
    fn save_outcome_names_the_bad_rule_and_saves_nothing() {
        let good = rule("A", "x", "https://t.example/{match}");
        assert_eq!(save_outcome(&[]).unwrap(), "Saved. No rules: the transcript renders as before.");
        assert_eq!(save_outcome(&[good.clone()]).unwrap(), "Saved 1 rule.");
        assert_eq!(save_outcome(&[good.clone(), good.clone()]).unwrap(), "Saved 2 rules.");
        let err = save_outcome(&[good.clone(), rule("B", "(", "https://t.example/{match}")]).unwrap_err();
        assert!(err.starts_with("Rule 2: Invalid regular expression"), "{err}");
        assert!(err.ends_with("Nothing was saved."), "{err}");
        let js = save_outcome(&[rule("C", "x", "javascript:{match}")]).unwrap_err();
        assert!(js.contains("https://"), "{js}");
    }

    #[test]
    fn row_error_waits_for_content_or_a_save_attempt() {
        let empty = Rule::default();
        assert_eq!(row_error(&empty, false), "");
        assert!(!row_error(&empty, true).is_empty());
        assert!(row_error(&rule("A", "(", ""), false).contains("Invalid regular expression"));
        assert_eq!(row_error(&rule("A", "x", "https://t.example/{match}"), false), "");
    }

    #[test]
    fn gateway_rules_reads_settings_and_tells_old_gateways_apart() {
        let snap = serde_json::json!({
            "theme": "dracula",
            "linkPatterns": [{"name": "Jira", "pattern": "x", "template": "https://t.example/{match}"}],
        });
        assert_eq!(gateway_rules(&snap), Some(vec![rule("Jira", "x", "https://t.example/{match}")]));
        assert_eq!(gateway_rules(&serde_json::json!({"linkPatterns": []})), Some(vec![]));
        assert_eq!(gateway_rules(&serde_json::json!({"linkPatterns": null})), Some(vec![]));
        // An older gateway omits the field: keep the browser list.
        assert_eq!(gateway_rules(&serde_json::json!({"theme": "dracula"})), None);
    }

    #[test]
    fn patch_body_carries_the_whole_list() {
        let rules = vec![rule("A", r"\d+", "https://t.example/{match}")];
        assert_eq!(
            patch_body(&rules),
            serde_json::json!({"linkPatterns": [{"name": "A", "pattern": r"\d+", "template": "https://t.example/{match}"}]})
        );
        assert_eq!(patch_body(&[]), serde_json::json!({"linkPatterns": []}));
    }

    #[test]
    fn migration_plan_appends_new_valid_local_rules_once() {
        let a = rule("A", "a", "https://t.example/{match}");
        let b = rule("B", "b", "https://t.example/{match}");
        // Empty gateway: the local list moves as is (normalised).
        assert_eq!(
            migration_plan(&[rule(" A ", "a", " https://t.example/{match} ")], &[]),
            Some(vec![a.clone()])
        );
        // Gateway already has them: nothing to send.
        assert_eq!(migration_plan(&[a.clone()], &[a.clone()]), None);
        // Gateway rules stay first; only missing local rules are added.
        assert_eq!(migration_plan(&[a.clone(), b.clone()], &[b.clone()]), Some(vec![b.clone(), a.clone()]));
        // Invalid local rules were never applied and are dropped.
        assert_eq!(migration_plan(&[rule("Bad", "a*", "https://t.example/{match}")], &[]), None);
        // The rule cap holds.
        let full: Vec<Rule> = (0..MAX_RULES).map(|i| rule(&format!("R{i}"), &format!("r{i}"), "https://t.example/{match}")).collect();
        assert_eq!(migration_plan(&[a.clone()], &full), None);
    }

    #[test]
    fn store_note_names_where_rules_live() {
        assert!(store_note(Store::Gateway).contains("gateway"));
        assert!(store_note(Store::Browser).contains("this browser"));
        assert!(!store_note(Store::Unknown).is_empty());
    }

    #[test]
    fn moved_swaps_with_neighbours_within_bounds() {
        assert_eq!(moved(vec![1, 2, 3], 1, true), vec![2, 1, 3]);
        assert_eq!(moved(vec![1, 2, 3], 1, false), vec![1, 3, 2]);
        assert_eq!(moved(vec![1, 2, 3], 0, true), vec![1, 2, 3]);
        assert_eq!(moved(vec![1, 2, 3], 2, false), vec![1, 2, 3]);
        assert_eq!(moved(vec![1, 2, 3], 9, true), vec![1, 2, 3]);
    }
}
