//! Scheduler presets section for the Schedule page (#1495). The daemon owns
//! the shipped build-system preset catalog and the global selection
//! (`GET`/`POST /v1/scheduler/presets`); this view lists the catalog, drafts
//! a selection, and applies it with one explicit POST. No optimistic state:
//! what renders as applied is always the daemon's returned `global`.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::json;

use crate::api::{get_json, post_json_value};

/// One command classification rule from a preset (`host.SchedulerPresetRule`).
/// The daemon serialises the host structs without json tags, so keys arrive
/// as Go field names; lower-case aliases keep a future tagged wire working.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct PresetRule {
    #[serde(default, rename = "Pattern", alias = "pattern")]
    pub pattern: String,
    #[serde(default, rename = "Class", alias = "class")]
    pub class: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Preset {
    #[serde(default, rename = "ID", alias = "id")]
    pub id: String,
    #[serde(default, rename = "Name", alias = "name")]
    pub name: String,
    #[serde(default, rename = "Rationale", alias = "rationale")]
    pub rationale: String,
    #[serde(default, rename = "Rules", alias = "rules")]
    pub rules: Option<Vec<PresetRule>>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct GlobalState {
    /// Enabled preset IDs; `null` when none are set.
    #[serde(default, rename = "Presets", alias = "presets")]
    pub presets: Option<Vec<String>>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct PresetsResponse {
    #[serde(default)]
    pub presets: Option<Vec<Preset>>,
    #[serde(default)]
    pub global: GlobalState,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct ApplyResponse {
    #[serde(default)]
    global: GlobalState,
}

/// Just the bootstrap fields this section reads.
#[derive(Clone, Debug, Default, Deserialize)]
struct BootFlags {
    #[serde(default, rename = "attachOnly")]
    attach_only: bool,
}

pub const UNAVAILABLE: &str = "Scheduler presets are not available on this gateway.";
pub const READ_ONLY: &str =
    "This gateway is attach-only (read-only), so presets can be viewed but not changed.";
pub const APPLIED: &str = "Scheduler presets applied.";

/// One rendered row: catalog presets in daemon order, then any globally
/// enabled ID the catalog does not list (read-only, labelled — #1495 Q3).
#[derive(Clone, Debug, PartialEq)]
pub struct PresetRow {
    pub id: String,
    pub name: String,
    pub detail: String,
    pub in_catalog: bool,
}

/// What the preset coordinates: its rationale, then the command patterns.
pub fn preset_detail(p: &Preset) -> String {
    let patterns: Vec<&str> = p
        .rules
        .as_deref()
        .unwrap_or_default()
        .iter()
        .map(|r| r.pattern.as_str())
        .filter(|s| !s.is_empty())
        .collect();
    match (p.rationale.trim().is_empty(), patterns.is_empty()) {
        (true, true) => String::new(),
        (false, true) => p.rationale.trim().to_string(),
        (true, false) => format!("Coordinates: {}", patterns.join(", ")),
        (false, false) => format!(
            "{} Coordinates: {}",
            p.rationale.trim(),
            patterns.join(", ")
        ),
    }
}

pub fn preset_rows(catalog: &[Preset], global: &[String]) -> Vec<PresetRow> {
    let mut rows: Vec<PresetRow> = catalog
        .iter()
        .filter(|p| !p.id.is_empty())
        .map(|p| PresetRow {
            id: p.id.clone(),
            name: if p.name.trim().is_empty() {
                p.id.clone()
            } else {
                p.name.clone()
            },
            detail: preset_detail(p),
            in_catalog: true,
        })
        .collect();
    for id in global {
        if !id.is_empty() && !rows.iter().any(|r| &r.id == id) {
            rows.push(PresetRow {
                id: id.clone(),
                name: id.clone(),
                detail: "Not in this daemon's preset catalog.".into(),
                in_catalog: false,
            });
        }
    }
    rows
}

/// Toggle `id` in the draft, keeping row (catalog) order stable so the POST
/// body is deterministic.
pub fn toggle(draft: &[String], id: &str, on: bool, order: &[PresetRow]) -> Vec<String> {
    order
        .iter()
        .map(|r| r.id.clone())
        .filter(|rid| {
            if rid == id {
                on
            } else {
                draft.iter().any(|d| d == rid)
            }
        })
        .collect()
}

/// Order-insensitive "draft differs from what the daemon has applied".
pub fn is_dirty(draft: &[String], applied: &[String]) -> bool {
    draft.len() != applied.len() || draft.iter().any(|d| !applied.contains(d))
}

/// A GET failure as the section's note: a 501 is the daemon's
/// capability-unavailable answer, everything else is shown as-is.
pub fn load_error_note(err: &str) -> String {
    if err.starts_with("HTTP 501") {
        UNAVAILABLE.to_string()
    } else {
        format!("Could not load scheduler presets: {err}")
    }
}

/// A POST failure (`HTTP <status> <body>`) as the section's note, using the
/// daemon's own `{"error": …}` sentence when present (unknown ID, 403
/// read-only attach, 501 capability unavailable).
pub fn apply_error_note(err: &str) -> String {
    let rest = err.strip_prefix("HTTP ").unwrap_or(err);
    let (code, body) = rest.split_once(' ').unwrap_or((rest, ""));
    if code == "501" {
        return UNAVAILABLE.to_string();
    }
    let msg = serde_json::from_str::<serde_json::Value>(body)
        .ok()
        .and_then(|v| v.get("error").and_then(|e| e.as_str()).map(str::to_string))
        .filter(|m| !m.trim().is_empty());
    match msg {
        Some(m) => format!("Presets not applied: {m}"),
        None => format!("Presets not applied: {err}"),
    }
}

#[component]
pub fn SchedulerPresetsSection(base: RwSignal<String>, token: RwSignal<String>) -> impl IntoView {
    let catalog = RwSignal::new(Vec::<Preset>::new());
    let applied = RwSignal::new(Vec::<String>::new());
    let draft = RwSignal::new(Vec::<String>::new());
    let loading = RwSignal::new(true);
    let unavailable = RwSignal::new(false);
    let read_only = RwSignal::new(false);
    let applying = RwSignal::new(false);
    let note = RwSignal::new(String::new());
    let error = RwSignal::new(String::new());

    let load = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        loading.set(true);
        spawn_local(async move {
            if let Ok(flags) = get_json::<BootFlags>(&b, &t, "/v1/bootstrap").await {
                read_only.set(flags.attach_only);
            }
            match get_json::<PresetsResponse>(&b, &t, "/v1/scheduler/presets").await {
                Ok(resp) => {
                    let global = resp.global.presets.unwrap_or_default();
                    catalog.set(resp.presets.unwrap_or_default());
                    applied.set(global.clone());
                    draft.set(global);
                    unavailable.set(false);
                    error.set(String::new());
                }
                Err(e) => {
                    let msg = load_error_note(&e);
                    unavailable.set(msg == UNAVAILABLE);
                    error.set(if msg == UNAVAILABLE { String::new() } else { msg });
                }
            }
            loading.set(false);
        });
    };
    load();

    let apply = move || {
        if applying.get_untracked() || read_only.get_untracked() {
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let ids = draft.get_untracked();
        applying.set(true);
        note.set(String::new());
        error.set(String::new());
        spawn_local(async move {
            match post_json_value(&b, &t, "/v1/scheduler/presets", json!({ "ids": ids })).await {
                Ok(v) => {
                    // The daemon's returned selection is the new state.
                    let global = serde_json::from_value::<ApplyResponse>(v)
                        .ok()
                        .and_then(|r| r.global.presets)
                        .unwrap_or_default();
                    applied.set(global.clone());
                    draft.set(global);
                    note.set(APPLIED.into());
                }
                Err(e) => {
                    if e.starts_with("HTTP 403") {
                        read_only.set(true);
                    }
                    error.set(apply_error_note(&e));
                }
            }
            applying.set(false);
        });
    };

    let rows = Memo::new(move |_| preset_rows(&catalog.get(), &applied.get()));

    view! {
        <section class="sched-presets" aria-label="Scheduler presets">
            <h3>"Scheduler presets"</h3>
            <p class="hint">"Presets tell the scheduler which build systems to serialise so parallel builds do not starve each other. Apply replaces the global selection."</p>
            {move || {
                if loading.get() && catalog.get().is_empty() {
                    return view! { <p class="hint">"Loading presets…"</p> }.into_any();
                }
                if unavailable.get() {
                    return view! { <p class="hint">{UNAVAILABLE}</p> }.into_any();
                }
                let list = rows.get();
                if list.is_empty() {
                    return view! { <p class="hint">"This daemon ships no scheduler presets."</p> }.into_any();
                }
                let locked = read_only.get();
                list.into_iter().map(|row| {
                    let id = row.id.clone();
                    let id_checked = row.id.clone();
                    let input_id = format!("sched-preset-{}", row.id);
                    let input_for = input_id.clone();
                    let disabled = locked || !row.in_catalog;
                    let name = row.name.clone();
                    let detail = row.detail.clone();
                    view! {
                        <div class="sched-preset">
                            <input
                                type="checkbox"
                                id=input_id
                                data-preset=row.id.clone()
                                disabled=disabled
                                prop:checked=move || draft.get().iter().any(|d| d == &id_checked)
                                on:change=move |ev| {
                                    let on = event_target_checked(&ev);
                                    let order = rows.get_untracked();
                                    draft.update(|d| {
                                        let next = toggle(d.as_slice(), &id, on, &order);
                                        *d = next;
                                    });
                                    note.set(String::new());
                                }
                            />
                            <label for=input_for>
                                <span class="name">{name}</span>
                                <span class="meta">{detail}</span>
                            </label>
                        </div>
                    }
                }).collect_view().into_any()
            }}
            <Show when=move || read_only.get() && !unavailable.get()>
                <p class="hint">{READ_ONLY}</p>
            </Show>
            <Show when=move || !error.get().is_empty()>
                <p class="sched-error" role="alert">{move || error.get()}</p>
            </Show>
            <Show when=move || !note.get().is_empty()>
                <p class="sched-note" role="status">{move || note.get()}</p>
            </Show>
            <Show when=move || !unavailable.get() && !rows.get().is_empty()>
                <div class="sched-actions">
                    <button
                        type="button"
                        class="primary"
                        id="sched-presets-apply"
                        disabled=move || read_only.get() || applying.get() || !is_dirty(&draft.get(), &applied.get())
                        on:click=move |_| apply()
                    >{move || if applying.get() { "Applying…" } else { "Apply presets" }}</button>
                </div>
            </Show>
        </section>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn preset(id: &str, name: &str, rationale: &str, patterns: &[&str]) -> Preset {
        Preset {
            id: id.into(),
            name: name.into(),
            rationale: rationale.into(),
            rules: Some(
                patterns
                    .iter()
                    .map(|p| PresetRule {
                        pattern: (*p).into(),
                        class: "build".into(),
                    })
                    .collect(),
            ),
        }
    }

    #[test]
    fn parses_the_daemon_wire_with_go_field_names() {
        let body = r#"{"presets":[{"ID":"cmake","Version":1,"Name":"CMake","Rationale":"Heavy.","DefaultClass":"build","Limits":{"build":2},"Rules":[{"Pattern":"cmake *","Class":"build"}]}],"global":{"Presets":["cmake"],"Limits":null,"Commands":null}}"#;
        let resp: PresetsResponse = serde_json::from_str(body).unwrap();
        let presets = resp.presets.unwrap();
        assert_eq!(presets[0].id, "cmake");
        assert_eq!(presets[0].name, "CMake");
        assert_eq!(presets[0].rules.as_ref().unwrap()[0].pattern, "cmake *");
        assert_eq!(resp.global.presets.unwrap(), vec!["cmake".to_string()]);
    }

    #[test]
    fn parses_null_global_and_lowercase_keys() {
        let resp: PresetsResponse =
            serde_json::from_str(r#"{"presets":[{"id":"ninja","name":"Ninja"}],"global":{"Presets":null}}"#)
                .unwrap();
        assert_eq!(resp.presets.unwrap()[0].id, "ninja");
        assert_eq!(resp.global.presets, None);
        let apply: ApplyResponse =
            serde_json::from_str(r#"{"ok":true,"global":{"Presets":["ninja"]}}"#).unwrap();
        assert_eq!(apply.global.presets.unwrap(), vec!["ninja".to_string()]);
    }

    #[test]
    fn detail_names_what_the_preset_coordinates() {
        assert_eq!(
            preset_detail(&preset("cmake", "CMake", "Heavy builds.", &["cmake *", "ctest *"])),
            "Heavy builds. Coordinates: cmake *, ctest *"
        );
        assert_eq!(preset_detail(&preset("x", "X", "", &["x *"])), "Coordinates: x *");
        assert_eq!(preset_detail(&preset("x", "X", "Only why.", &[])), "Only why.");
        assert_eq!(preset_detail(&preset("x", "X", "  ", &[])), "");
    }

    #[test]
    fn rows_list_catalog_then_unknown_global_ids_read_only() {
        let catalog = vec![preset("cmake", "CMake", "", &[]), preset("ninja", "", "", &[])];
        let rows = preset_rows(&catalog, &["custom".to_string(), "cmake".to_string()]);
        let ids: Vec<&str> = rows.iter().map(|r| r.id.as_str()).collect();
        assert_eq!(ids, vec!["cmake", "ninja", "custom"]);
        assert_eq!(rows[1].name, "ninja", "empty name falls back to the id");
        assert!(rows[0].in_catalog && rows[1].in_catalog);
        assert!(!rows[2].in_catalog);
    }

    #[test]
    fn toggle_keeps_catalog_order() {
        let rows = preset_rows(
            &[preset("a", "A", "", &[]), preset("b", "B", "", &[]), preset("c", "C", "", &[])],
            &[],
        );
        let d = toggle(&[], "c", true, &rows);
        let d = toggle(&d, "a", true, &rows);
        assert_eq!(d, vec!["a".to_string(), "c".to_string()]);
        assert_eq!(toggle(&d, "a", false, &rows), vec!["c".to_string()]);
    }

    #[test]
    fn dirty_is_order_insensitive() {
        let a = vec!["a".to_string(), "b".to_string()];
        let b = vec!["b".to_string(), "a".to_string()];
        assert!(!is_dirty(&a, &b));
        assert!(is_dirty(&a, &["a".to_string()]));
        assert!(is_dirty(&[], &["a".to_string()]));
        assert!(!is_dirty(&[], &[]));
    }

    #[test]
    fn errors_surface_the_daemon_sentence() {
        assert_eq!(load_error_note("HTTP 501"), UNAVAILABLE);
        assert_eq!(
            load_error_note("HTTP 500"),
            "Could not load scheduler presets: HTTP 500"
        );
        assert_eq!(
            apply_error_note(r#"HTTP 403 {"error":"read-only attach mode does not allow mutations"}"#),
            "Presets not applied: read-only attach mode does not allow mutations"
        );
        assert_eq!(
            apply_error_note(r#"HTTP 400 {"error":"unknown scheduler preset \"nope\""}"#),
            "Presets not applied: unknown scheduler preset \"nope\""
        );
        assert_eq!(
            apply_error_note(r#"HTTP 501 {"error":"scheduler capability unavailable on this host"}"#),
            UNAVAILABLE
        );
        assert_eq!(apply_error_note("network down"), "Presets not applied: network down");
    }
}
