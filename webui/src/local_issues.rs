//! Local issues panel (#1500): the project-local tracker agents write with
//! `issue_write` and the TUI browses with `/issues`, surfaced in the
//! cockpit. Every action goes to the daemon's existing `/v1/issues*`
//! routes; nothing is shown until the daemon confirms it. feature:local-issues

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;

use load_guard::LoadEpoch;

use crate::api::{get_text, post_json_value, post_raw_json};
use crate::load_epoch::begin;

/// Bootstrap `issues` capability: the palette command and tab exist only
/// when the daemon wires `host.Services.Issues`. Shell command predicates
/// are plain `fn() -> bool`, so the flag lives here as well as in the
/// page's signal.
thread_local! {
    static ISSUES_CAPABLE: std::cell::Cell<bool> = const { std::cell::Cell::new(false) };
}

pub fn issues_capable() -> bool {
    ISSUES_CAPABLE.with(|slot| slot.get())
}

pub fn set_issues_capable(on: bool) {
    ISSUES_CAPABLE.with(|slot| slot.set(on));
}

/// Status filters, sent verbatim as `?status=` ("" = all).
pub const ISSUE_FILTERS: &[&str] = &["open", "closed", ""];
/// Daemon export route and the filename it sends in Content-Disposition.
pub const ISSUES_EXPORT_PATH: &str = "/v1/issues/export";
pub const ISSUES_EXPORT_FILENAME: &str = "strike-issues.json";
pub const ISSUES_IMPORT_PATH: &str = "/v1/issues/import";
/// Format/version the store exports and accepts
/// (internal/persist/issue/store.go exportFormat / exportVersion).
const ISSUES_FORMAT: &str = "strike.issues";
const ISSUES_VERSION: i64 = 1;

/// One `/v1/issues` row. The daemon serialises `host.Issue`, which has no
/// json tags (`ID`, `Title`, …); lower-case aliases accept the store's
/// own export spelling too. The host DTO carries no timestamps or author,
/// so the panel shows none rather than inventing them.
#[derive(Clone, Debug, Deserialize, Default, PartialEq)]
pub struct LocalIssue {
    #[serde(rename = "ID", alias = "id", default)]
    pub id: i64,
    #[serde(rename = "Title", alias = "title", default)]
    pub title: String,
    #[serde(rename = "Body", alias = "body", default)]
    pub body: String,
    #[serde(rename = "Status", alias = "status", default)]
    pub status: String,
}

/// `issues` is `null` when the Go store returns a nil slice.
#[derive(Deserialize, Default)]
struct IssueList {
    #[serde(default)]
    issues: Option<Vec<LocalIssue>>,
}

/// `GET /v1/issues` for a status filter; the daemon does the filtering.
pub fn issues_list_path(status: &str) -> String {
    if status.is_empty() {
        "/v1/issues".to_string()
    } else {
        format!("/v1/issues?status={}", crate::api::js_encode(status))
    }
}

pub fn issue_close_path(id: i64) -> String {
    format!("/v1/issues/{id}/close")
}

pub fn filter_label(status: &str) -> &'static str {
    match status {
        "open" => "Open",
        "closed" => "Closed",
        _ => "All",
    }
}

pub fn empty_label(status: &str) -> &'static str {
    match status {
        "open" => "No open issues",
        "closed" => "No closed issues",
        _ => "No issues recorded yet",
    }
}

/// Client-side guard mirroring the daemon's `title is required` check, so
/// an empty title never round-trips; the daemon still validates.
pub fn create_body(title: &str, body: &str) -> Result<serde_json::Value, String> {
    if title.trim().is_empty() {
        return Err("Title is required.".into());
    }
    Ok(serde_json::json!({ "title": title, "body": body }))
}

/// Turn an `HTTP <status> <body>` error from `crate::api` into honest copy:
/// the daemon's own message, with the attach-only and missing-capability
/// cases named plainly.
pub fn issue_error_text(err: &str) -> String {
    let rest = err.strip_prefix("HTTP ").unwrap_or("");
    let (code, body) = rest.split_once(' ').unwrap_or((rest, ""));
    let msg = serde_json::from_str::<serde_json::Value>(body.trim())
        .ok()
        .and_then(|v| v.get("error").and_then(|e| e.as_str()).map(str::to_string))
        .unwrap_or_else(|| body.trim().to_string());
    match code {
        "501" => "Local issues are not available on this gateway.".into(),
        "403" if msg.contains("read-only") => {
            "This gateway is attach-only (read-only): issues can be read here but not changed.".into()
        }
        _ if !msg.is_empty() => msg,
        _ if !rest.is_empty() => format!("HTTP {rest}"),
        _ => err.to_string(),
    }
}

/// First line of the body, for the collapsed row.
pub fn body_preview(body: &str, max: usize) -> String {
    let line = body.lines().find(|l| !l.trim().is_empty()).unwrap_or("").trim();
    if line.chars().count() > max {
        let mut s: String = line.chars().take(max).collect();
        s.push('…');
        s
    } else {
        line.to_string()
    }
}

/// Check an import file before sending it: valid JSON object with the
/// store's format/version. Returns the issue count. The bytes sent are
/// always the file text itself, never a re-serialisation.
pub fn check_import(raw: &str) -> Result<usize, String> {
    if raw.trim().is_empty() {
        return Err("Choose a strike-issues.json file first.".into());
    }
    let doc: serde_json::Value =
        serde_json::from_str(raw).map_err(|e| format!("Not valid JSON: {e}"))?;
    let obj = doc
        .as_object()
        .ok_or_else(|| "Not a strike-issues.json export: expected a JSON object.".to_string())?;
    let format = obj.get("format").and_then(|v| v.as_str()).unwrap_or("");
    if format != ISSUES_FORMAT {
        return Err(format!(
            "The file's format is {format:?}, not {ISSUES_FORMAT:?}; the daemon would reject it."
        ));
    }
    if obj.get("version").and_then(|v| v.as_i64()) != Some(ISSUES_VERSION) {
        return Err(format!(
            "The file's version is not {ISSUES_VERSION}; the daemon would reject it."
        ));
    }
    Ok(obj
        .get("issues")
        .and_then(|v| v.as_array())
        .map(|a| a.len())
        .unwrap_or(0))
}

/// Merge-import request body: the file text spliced in byte-for-byte.
/// Replace is not offered in this slice (it clears the store).
pub fn import_body(raw: &str) -> String {
    format!("{{\"replace\":false,\"data\":{}}}", raw.trim())
}

#[component]
pub fn LocalIssuesPanel(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    storage_rev: RwSignal<u32>,
) -> impl IntoView {
    let filter = RwSignal::new("open".to_string());
    let rows = RwSignal::new(Vec::<LocalIssue>::new());
    let loading = RwSignal::new(false);
    let loaded = RwSignal::new(false);
    let error = RwSignal::new(String::new());
    let action_error = RwSignal::new(String::new());
    let expanded = RwSignal::new(None::<i64>);
    let title_in = RwSignal::new(String::new());
    let body_in = RwSignal::new(String::new());
    let creating = RwSignal::new(false);
    let closing = RwSignal::new(None::<i64>);
    let rev = RwSignal::new(0u32);
    let busy_io = RwSignal::new(false);
    let list_epoch = RwSignal::new(LoadEpoch::new());

    Effect::new(move |_| {
        let path = issues_list_path(&filter.get());
        let _ = (storage_rev.get(), rev.get());
        let (b, t) = (base.get(), token.get());
        let gen = begin(list_epoch);
        loading.set(true);
        spawn_local(async move {
            let result = get_text(&b, &t, &path).await.and_then(|text| {
                serde_json::from_str::<IssueList>(&text).map_err(|e| e.to_string())
            });
            if !list_epoch.with_untracked(|epoch| epoch.accept(gen)) {
                return;
            }
            loading.set(false);
            loaded.set(true);
            match result {
                Ok(list) => {
                    error.set(String::new());
                    rows.set(list.issues.unwrap_or_default());
                }
                Err(e) => {
                    rows.set(Vec::new());
                    error.set(issue_error_text(&e));
                }
            }
        });
    });

    let create = move || {
        if creating.get_untracked() {
            return;
        }
        let payload = match create_body(&title_in.get_untracked(), &body_in.get_untracked()) {
            Ok(p) => p,
            Err(e) => {
                action_error.set(e);
                return;
            }
        };
        creating.set(true);
        action_error.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match post_json_value(&b, &t, "/v1/issues", payload).await {
                Ok(v) => {
                    let made: LocalIssue = serde_json::from_value(v).unwrap_or_default();
                    title_in.set(String::new());
                    body_in.set(String::new());
                    status.set(format!("Created issue #{}", made.id));
                    rev.update(|r| *r += 1);
                }
                Err(e) => action_error.set(issue_error_text(&e)),
            }
            creating.set(false);
        });
    };

    let close = move |id: i64| {
        if closing.get_untracked().is_some() {
            return;
        }
        closing.set(Some(id));
        action_error.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match post_json_value(&b, &t, &issue_close_path(id), serde_json::json!({})).await {
                Ok(_) => {
                    status.set(format!("Closed issue #{id}"));
                    rev.update(|r| *r += 1);
                }
                Err(e) => action_error.set(issue_error_text(&e)),
            }
            closing.set(None);
        });
    };

    let export = move || {
        if busy_io.get_untracked() {
            return;
        }
        busy_io.set(true);
        action_error.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            // The downloaded bytes are the daemon response untouched; no
            // file is saved on failure.
            match get_text(&b, &t, ISSUES_EXPORT_PATH).await {
                Ok(text) => {
                    crate::transcript_view::save_download(
                        ISSUES_EXPORT_FILENAME,
                        "application/json",
                        &text,
                    );
                    status.set(format!("Downloaded {ISSUES_EXPORT_FILENAME}"));
                }
                Err(e) => action_error.set(issue_error_text(&e)),
            }
            busy_io.set(false);
        });
    };

    let import_file = move |ev: web_sys::Event| {
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
        input.set_value("");
        if busy_io.get_untracked() {
            return;
        }
        busy_io.set(true);
        action_error.set(String::new());
        let name = file.name();
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let raw = match wasm_bindgen_futures::JsFuture::from(file.text()).await {
                Ok(v) => v.as_string().unwrap_or_default(),
                Err(_) => {
                    action_error.set(format!("Could not read {name}."));
                    busy_io.set(false);
                    return;
                }
            };
            if let Err(e) = check_import(&raw) {
                action_error.set(e);
                busy_io.set(false);
                return;
            }
            match post_raw_json(&b, &t, ISSUES_IMPORT_PATH, import_body(&raw)).await {
                Ok(reply) => {
                    let n = serde_json::from_str::<serde_json::Value>(&reply)
                        .ok()
                        .and_then(|v| v.get("imported").and_then(|n| n.as_u64()))
                        .unwrap_or(0);
                    status.set(format!("Imported {n} issues from {name}"));
                    rev.update(|r| *r += 1);
                }
                Err(e) => action_error.set(issue_error_text(&e)),
            }
            busy_io.set(false);
        });
    };

    view! {
        <div class="storage local-issues" data-local-issues="true">
            <p class="hint">"Project-local issues that agents file with issue_write and the TUI shows under /issues. They persist across sessions."</p>
            <div class="chips" role="group" aria-label="Issue status">
                {ISSUE_FILTERS.iter().copied().map(|f| view! {
                    <button type="button" data-issues-filter=f
                        aria-pressed=move || (filter.get() == f).to_string()
                        class=move || if filter.get() == f { "chip on" } else { "chip" }
                        on:click=move |_| { expanded.set(None); filter.set(f.to_string()); }>{filter_label(f)}</button>
                }).collect_view()}
            </div>
            <form class="storage-form" data-issues-form="true"
                on:submit=move |ev| { ev.prevent_default(); create(); }>
                <label class="grow">"Title"
                    <input type="text" data-issues-title="true" maxlength="300"
                        prop:value=move || title_in.get()
                        on:input=move |ev| title_in.set(event_target_value(&ev)) />
                </label>
                <label class="grow">"Body (optional)"
                    <textarea data-issues-body="true" rows="2"
                        prop:value=move || body_in.get()
                        on:input=move |ev| body_in.set(event_target_value(&ev))></textarea>
                </label>
                <button type="submit" data-action="issues-create" disabled=move || creating.get()>
                    {move || if creating.get() { "Creating\u{2026}" } else { "Create issue" }}
                </button>
            </form>
            <Show when=move || !action_error.get().is_empty()>
                <p class="hint storage-error" role="alert" data-issues-action-error="true">{move || action_error.get()}</p>
            </Show>
            <Show when=move || !error.get().is_empty()>
                <p class="hint storage-error" role="alert" data-issues-error="true">{move || error.get()}</p>
            </Show>
            <Show when=move || loaded.get() && !loading.get() && error.get().is_empty() && rows.get().is_empty()>
                <div class="empty-state" data-issues-empty="true">
                    <strong>{move || empty_label(&filter.get())}</strong>
                    <p>"Agents add issues with issue_write; you can add one above."</p>
                </div>
            </Show>
            <ul class="plain">
                {move || rows.get().into_iter().map(|it| {
                    let id = it.id;
                    let open = it.status != "closed";
                    let preview = body_preview(&it.body, 120);
                    let body = it.body.clone();
                    let st = it.status.clone();
                    view! {
                        <li class="storage-row local-issue-row" data-issue-id=id.to_string() data-issue-status=st.clone()>
                            <button type="button" class="link"
                                aria-expanded=move || (expanded.get() == Some(id)).to_string()
                                on:click=move |_| expanded.update(|e| *e = if *e == Some(id) { None } else { Some(id) })>
                                <span class="name">{format!("#{id} {}", it.title)}</span>
                                <span class="path">{if open { "Open" } else { "Closed" }}{(!preview.is_empty()).then(|| format!(" \u{b7} {preview}"))}</span>
                            </button>
                            {open.then(|| view! {
                                <button type="button" class="secondary" data-action="issues-close"
                                    disabled=move || closing.get().is_some()
                                    on:click=move |_| close(id)>
                                    {move || if closing.get() == Some(id) { "Closing\u{2026}" } else { "Close" }}
                                </button>
                            })}
                        </li>
                        <Show when=move || expanded.get() == Some(id)>
                            <li class="local-issue-body" data-issue-body=id.to_string()>
                                <pre class="storage-preview">{if body.is_empty() { "(no body)".to_string() } else { body.clone() }}</pre>
                            </li>
                        </Show>
                    }
                }).collect_view()}
            </ul>
            <div class="storage-export">
                <button type="button" class="secondary" data-action="issues-export"
                    disabled=move || busy_io.get()
                    on:click=move |_| export()>"Export issues"</button>
                <label class="secondary local-issues-import">"Import (merge)"
                    <input type="file" accept=".json,application/json" data-action="issues-import"
                        disabled=move || busy_io.get()
                        on:change=import_file />
                </label>
                <p class="hint">"Export downloads strike-issues.json (plain JSON). Import merges a file by id; imported issues win."</p>
            </div>
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn list_path_sends_status_verbatim_and_all_unfiltered() {
        assert_eq!(issues_list_path("open"), "/v1/issues?status=open");
        assert_eq!(issues_list_path("closed"), "/v1/issues?status=closed");
        assert_eq!(issues_list_path(""), "/v1/issues");
        assert_eq!(issue_close_path(7), "/v1/issues/7/close");
    }

    #[test]
    fn row_parses_host_dto_and_store_spelling() {
        let host: IssueList = serde_json::from_str(
            r#"{"issues":[{"ID":3,"Title":"flaky test","Body":"in X","Status":"open"}]}"#,
        )
        .unwrap();
        assert_eq!(
            host.issues.unwrap_or_default(),
            vec![LocalIssue { id: 3, title: "flaky test".into(), body: "in X".into(), status: "open".into() }]
        );
        let lower: LocalIssue =
            serde_json::from_str(r#"{"id":4,"title":"t","status":"closed"}"#).unwrap();
        assert_eq!(lower.id, 4);
        assert_eq!(lower.status, "closed");
        assert!(lower.body.is_empty());
        let empty: IssueList = serde_json::from_str(r#"{"issues":null}"#).unwrap();
        assert!(empty.issues.unwrap_or_default().is_empty());
    }

    #[test]
    fn create_body_rejects_blank_title_and_keeps_text() {
        assert_eq!(create_body("  ", "b"), Err("Title is required.".to_string()));
        assert_eq!(
            create_body("Migrate Y", "later").unwrap(),
            serde_json::json!({"title": "Migrate Y", "body": "later"})
        );
    }

    #[test]
    fn error_text_names_attach_only_and_missing_capability() {
        let cases = [
            (
                r#"HTTP 403 {"error":"read-only attach mode does not allow mutations"}"#,
                "This gateway is attach-only (read-only): issues can be read here but not changed.",
            ),
            (
                r#"HTTP 501 {"error":"issues capability unavailable on this host"}"#,
                "Local issues are not available on this gateway.",
            ),
            (r#"HTTP 400 {"error":"title is required"}"#, "title is required"),
            (r#"HTTP 404 {"error":"issue 9 not found"}"#, "issue 9 not found"),
            ("HTTP 500", "HTTP 500"),
            ("unauthorized: check the daemon token", "unauthorized: check the daemon token"),
        ];
        for (input, want) in cases {
            assert_eq!(issue_error_text(input), want, "{input}");
        }
    }

    #[test]
    fn labels_cover_every_filter() {
        for f in ISSUE_FILTERS {
            assert!(!filter_label(f).is_empty());
            assert!(!empty_label(f).is_empty());
        }
        assert_eq!(filter_label(""), "All");
        assert_eq!(empty_label("closed"), "No closed issues");
    }

    #[test]
    fn body_preview_takes_first_nonblank_line_and_clips() {
        assert_eq!(body_preview("\n  first line \nsecond", 50), "first line");
        assert_eq!(body_preview("abcdef", 3), "abc…");
        assert_eq!(body_preview("", 10), "");
    }

    #[test]
    fn import_check_matches_store_format_and_body_is_verbatim() {
        let good = r#"{"format":"strike.issues","version":1,"next_id":3,"issues":[{"id":1},{"id":2}]}"#;
        assert_eq!(check_import(good), Ok(2));
        assert!(check_import("").is_err());
        assert!(check_import("[1]").is_err());
        assert!(check_import("{nope").is_err());
        assert!(check_import(r#"{"format":"strike.memory","version":1}"#)
            .unwrap_err()
            .contains("strike.issues"));
        assert!(check_import(r#"{"format":"strike.issues","version":2}"#).is_err());
        assert_eq!(
            import_body(&format!(" {good}\n")),
            format!("{{\"replace\":false,\"data\":{good}}}")
        );
    }
}
