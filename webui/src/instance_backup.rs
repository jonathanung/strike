//! Instance backup and restore (#1339). feature:instance-backup
//!
//! Settings controls over the daemon's `GET /v1/backup/export` (one
//! portable `strike-backup.json`: sessions, memory, schedules, non-secret
//! settings) and `POST /v1/backup/restore` (`?dryRun=1` first, so the user
//! sees what will be created, skipped, or restored as a copy before
//! anything is written). The cockpit never parses or rebuilds the archive:
//! the downloaded bytes and the uploaded bytes are exactly the daemon's.

use std::collections::BTreeMap;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;

use crate::api::{get_text, post_raw_json};
use crate::transcript_view::save_download;

pub const BACKUP_EXPORT_PATH: &str = "/v1/backup/export";
pub const BACKUP_RESTORE_PATH: &str = "/v1/backup/restore";
/// Matches the daemon's Content-Disposition name (backup.Filename).
pub const BACKUP_FILENAME: &str = "strike-backup.json";
pub const BACKUP_EXPORT_HINT: &str = "One plain, unencrypted JSON file of this gateway's sessions, memory, schedules and non-secret settings. Provider keys, gateway tokens and secret values are never included; the file lists every left-out category by name.";
pub const BACKUP_RESTORE_HINT: &str = "Restore checks the file's integrity hashes, then shows what it will create, skip, or restore as a copy. Nothing on this gateway is overwritten.";

/// Section order the daemon restores in (backup.KnownSections).
const SECTIONS: [&str; 4] = ["settings", "memory", "schedules", "sessions"];
/// Rows shown per report; the counts above always cover everything.
const MAX_ROWS: usize = 200;

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct RestoreItem {
    #[serde(default)]
    pub section: String,
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub label: String,
    #[serde(default)]
    pub action: String,
    #[serde(default)]
    pub target: String,
    #[serde(default)]
    pub detail: String,
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct Exclusion {
    #[serde(default)]
    pub category: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub reason: String,
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RestoreReport {
    #[serde(default)]
    pub dry_run: bool,
    #[serde(default)]
    pub counts: BTreeMap<String, BTreeMap<String, u64>>,
    #[serde(default)]
    pub items: Vec<RestoreItem>,
    #[serde(default)]
    pub excluded: Vec<Exclusion>,
}

fn restore_path(dry_run: bool) -> String {
    if dry_run {
        format!("{BACKUP_RESTORE_PATH}?dryRun=1")
    } else {
        BACKUP_RESTORE_PATH.to_string()
    }
}

fn parse_report(text: &str) -> Result<RestoreReport, String> {
    serde_json::from_str::<RestoreReport>(text)
        .map_err(|e| format!("The daemon's restore report was not readable: {e}"))
}

/// "memory: 2 create, 1 skip" per section, in restore order; sections
/// with no records are left out.
fn summary_lines(r: &RestoreReport) -> Vec<String> {
    let mut out = Vec::new();
    for section in SECTIONS {
        let Some(counts) = r.counts.get(section) else {
            continue;
        };
        let parts: Vec<String> = ["create", "copy", "skip", "fail"]
            .iter()
            .filter_map(|a| counts.get(*a).filter(|n| **n > 0).map(|n| format!("{n} {a}")))
            .collect();
        if !parts.is_empty() {
            out.push(format!("{section}: {}", parts.join(", ")));
        }
    }
    out
}

/// True when applying the plan would write at least one record.
fn writes_anything(r: &RestoreReport) -> bool {
    r.counts
        .values()
        .any(|c| c.get("create").copied().unwrap_or(0) + c.get("copy").copied().unwrap_or(0) > 0)
}

fn item_line(it: &RestoreItem) -> String {
    let name = if it.label.trim().is_empty() { it.id.as_str() } else { it.label.as_str() };
    let mut line = format!("{} {} \u{2014} {}", it.section, name, it.action);
    if !it.target.is_empty() {
        line.push_str(&format!(" as {}", it.target));
    }
    if !it.detail.is_empty() {
        line.push_str(&format!(" ({})", it.detail));
    }
    line
}

fn exclusion_line(ex: &Exclusion) -> String {
    format!("{} [{}]: {}", ex.category, ex.kind, ex.reason)
}

fn report_rows(r: &RestoreReport) -> (Vec<String>, usize) {
    let rows: Vec<String> = r.items.iter().take(MAX_ROWS).map(item_line).collect();
    (rows, r.items.len().saturating_sub(MAX_ROWS))
}

fn report_view(r: RestoreReport) -> impl IntoView {
    let title = if r.dry_run { "Restore preview" } else { "Restore result" };
    let summary = summary_lines(&r);
    let empty = summary.is_empty();
    let (rows, hidden) = report_rows(&r);
    let excluded: Vec<String> = r.excluded.iter().map(exclusion_line).collect();
    let kind = if r.dry_run { "preview" } else { "result" };
    view! {
        <div class="backup-report" data-backup-report=kind>
            <h3 class="panel-title">{title}</h3>
            <Show when=move || empty>
                <p class="hint">"The archive holds no records."</p>
            </Show>
            <ul class="backup-summary">
                {summary.into_iter().map(|line| view! { <li>{line}</li> }).collect_view()}
            </ul>
            <details>
                <summary>"Every record"</summary>
                <ul class="backup-items">
                    {rows.into_iter().map(|line| view! { <li>{line}</li> }).collect_view()}
                </ul>
                <Show when=move || hidden > 0>
                    <p class="hint">{format!("{hidden} more not shown; the counts above include them.")}</p>
                </Show>
            </details>
            <details>
                <summary>"Not in this backup"</summary>
                <ul class="backup-excluded">
                    {excluded.into_iter().map(|line| view! { <li>{line}</li> }).collect_view()}
                </ul>
            </details>
        </div>
    }
}

#[component]
pub fn InstanceBackup(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
) -> impl IntoView {
    let exporting = RwSignal::new(false);
    let exported = RwSignal::new(String::new());
    let error = RwSignal::new(String::new());
    let file_text = RwSignal::new(String::new());
    let file_name = RwSignal::new(String::new());
    let plan = RwSignal::new(None::<RestoreReport>);
    let result = RwSignal::new(None::<RestoreReport>);
    let busy = RwSignal::new(false);

    let export = move || {
        if exporting.get_untracked() {
            return;
        }
        exporting.set(true);
        exported.set(String::new());
        error.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match get_text(&b, &t, BACKUP_EXPORT_PATH).await {
                Ok(text) => {
                    save_download(BACKUP_FILENAME, "application/json", &text);
                    let msg = format!("Downloaded {BACKUP_FILENAME} (instance backup).");
                    exported.set(msg.clone());
                    status.set(msg);
                }
                Err(e) => error.set(format!("Backup export failed: {e}")),
            }
            exporting.set(false);
        });
    };

    let read_file = move |ev: web_sys::Event| {
        use wasm_bindgen::JsCast;
        let Some(input) = ev
            .target()
            .and_then(|t| t.dyn_into::<web_sys::HtmlInputElement>().ok())
        else {
            return;
        };
        let Some(file) = input.files().and_then(|files| files.get(0)) else {
            return;
        };
        let name = file.name();
        plan.set(None);
        result.set(None);
        error.set(String::new());
        spawn_local(async move {
            match wasm_bindgen_futures::JsFuture::from(file.text()).await {
                Ok(v) => {
                    file_text.set(v.as_string().unwrap_or_default());
                    file_name.set(name);
                }
                Err(_) => {
                    file_text.set(String::new());
                    file_name.set(String::new());
                    error.set(format!("Could not read {name}."));
                }
            }
        });
    };

    let run_restore = move |dry_run: bool| {
        if busy.get_untracked() || file_text.get_untracked().trim().is_empty() {
            return;
        }
        if !dry_run && plan.get_untracked().is_none() {
            return;
        }
        busy.set(true);
        error.set(String::new());
        let (b, t, body) = (
            base.get_untracked(),
            token.get_untracked(),
            file_text.get_untracked(),
        );
        spawn_local(async move {
            match post_raw_json(&b, &t, &restore_path(dry_run), body).await {
                Ok(text) => match parse_report(&text) {
                    Ok(rep) => {
                        if dry_run {
                            plan.set(Some(rep));
                        } else {
                            let lines = summary_lines(&rep).join("; ");
                            status.set(format!("Restored from backup: {lines}"));
                            plan.set(None);
                            result.set(Some(rep));
                        }
                    }
                    Err(e) => error.set(e),
                },
                Err(e) => error.set(format!("Restore refused: {e}")),
            }
            busy.set(false);
        });
    };

    view! {
        <div class="instance-backup">
            <div class="backup-export">
                <button type="button" class="secondary" data-action="backup-export"
                    disabled=move || exporting.get()
                    on:click=move |_| export()>
                    {move || if exporting.get() { "Exporting\u{2026}" } else { "Export instance backup" }}
                </button>
                <p class="hint">{BACKUP_EXPORT_HINT}</p>
                <Show when=move || !exported.get().is_empty()>
                    <p class="hint" role="status" data-backup-export-status="true">{move || exported.get()}</p>
                </Show>
            </div>
            <div class="backup-restore">
                <label>"Restore from backup "
                    <input type="file" accept=".json,application/json" data-action="backup-restore-file" on:change=read_file />
                </label>
                <p class="hint">{BACKUP_RESTORE_HINT}</p>
                <Show when=move || !file_name.get().is_empty()>
                    <p class="hint">{move || format!("File: {}", file_name.get())}</p>
                </Show>
                <button type="button" class="secondary" data-action="backup-restore-preview"
                    disabled=move || busy.get() || file_text.get().trim().is_empty()
                    on:click=move |_| run_restore(true)>
                    {move || if busy.get() { "Checking\u{2026}" } else { "Preview restore" }}
                </button>
                <button type="button" data-action="backup-restore-apply"
                    disabled=move || busy.get() || !plan.get().as_ref().map(writes_anything).unwrap_or(false)
                    on:click=move |_| run_restore(false)>
                    "Restore"
                </button>
            </div>
            <Show when=move || !error.get().is_empty()>
                <p class="banner" role="alert" data-backup-error="true">{move || error.get()}</p>
            </Show>
            {move || plan.get().map(|r| report_view(r).into_any())}
            {move || result.get().map(|r| report_view(r).into_any())}
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const REPORT: &str = r#"{"dryRun":true,"archiveCreatedAt":"2026-11-20T12:00:00Z",
        "counts":{"memory":{"create":2,"skip":1},"sessions":{"copy":1},"schedules":{"fail":1}},
        "items":[{"section":"memory","id":"project:prefs","label":"prefs","action":"create"},
                 {"section":"sessions","id":"s1","label":"Fix","action":"copy","target":"s9","detail":"differs"}],
        "excluded":[{"category":"provider credentials","kind":"secret","reason":"never exported"}]}"#;

    #[test]
    fn parses_daemon_report() {
        let r = parse_report(REPORT).unwrap();
        assert!(r.dry_run);
        assert_eq!(r.items.len(), 2);
        assert_eq!(r.excluded[0].kind, "secret");
        assert!(parse_report("nope").is_err());
    }

    #[test]
    fn summary_follows_restore_order_and_skips_zeroes() {
        let r = parse_report(REPORT).unwrap();
        assert_eq!(
            summary_lines(&r),
            vec![
                "memory: 2 create, 1 skip".to_string(),
                "schedules: 1 fail".to_string(),
                "sessions: 1 copy".to_string(),
            ]
        );
    }

    #[test]
    fn apply_needs_a_write() {
        let r = parse_report(REPORT).unwrap();
        assert!(writes_anything(&r));
        let skips = parse_report(r#"{"dryRun":true,"counts":{"memory":{"skip":3}}}"#).unwrap();
        assert!(!writes_anything(&skips));
        assert!(!writes_anything(&RestoreReport::default()));
    }

    #[test]
    fn item_lines_name_copies_and_reasons() {
        let r = parse_report(REPORT).unwrap();
        assert_eq!(item_line(&r.items[0]), "memory prefs \u{2014} create");
        assert_eq!(item_line(&r.items[1]), "sessions Fix \u{2014} copy as s9 (differs)");
    }

    #[test]
    fn restore_path_marks_dry_runs() {
        assert_eq!(restore_path(true), "/v1/backup/restore?dryRun=1");
        assert_eq!(restore_path(false), "/v1/backup/restore");
    }
}
