//! Memory, knowledge, artifact, and decision-ledger panels. feature:storage

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;

use load_guard::{commit_load, Commit, ErrPolicy, LoadEpoch};

use crate::load_epoch::begin;

use crate::api::{delete_json, get_json, get_text, now_ms, put_json};
use crate::model::{fmt_ago, rfc3339_to_ms};

/// Daemon route for the whole-store memory export (#1534). The daemon
/// already serves this as a file download; the panel only fetches it.
pub const MEMORY_EXPORT_PATH: &str = "/v1/memory/export";
/// Filename the daemon sends in Content-Disposition for the export;
/// the download must use exactly this name.
pub const MEMORY_EXPORT_FILENAME: &str = "strike-memory.json";
/// Honest one-line label for the export (#1534, scope note from #1541):
/// the file is plain unencrypted JSON, and the daemon route ignores
/// the panel's Project/Global scope — it always exports the Project
/// store, even while the Global tab is showing.
pub const MEMORY_EXPORT_HINT: &str = "Export downloads plain, unencrypted JSON of everything the agent remembers in the Project store \u{2014} it always exports Project, even on the Global tab.";

/// What the export action should do with a finished fetch. Split out
/// as a pure decision so tests can prove the two honest states
/// without a browser: success downloads the daemon's bytes untouched,
/// failure downloads nothing and surfaces the daemon's message.
#[derive(Debug, PartialEq, Eq)]
enum MemoryExportDecision {
    Download(String),
    ShowError(String),
}

fn memory_export_decision(result: Result<String, String>) -> MemoryExportDecision {
    match result {
        Ok(text) => MemoryExportDecision::Download(text),
        Err(e) => MemoryExportDecision::ShowError(e),
    }
}

#[derive(Clone, Deserialize, Default)]
struct MemoryEntry {
    key: String,
    value: String,
    #[serde(default)]
    tags: Vec<String>,
    #[serde(default)]
    author: String,
    #[serde(rename = "updatedAt", default)]
    updated_at: String,
}

#[derive(Deserialize, Default)]
struct MemoryList {
    #[serde(default)]
    entries: Vec<MemoryEntry>,
    #[serde(rename = "markdownFile", default)]
    markdown_file: String,
    #[serde(rename = "auditFile", default)]
    audit_file: String,
}

#[derive(Clone, Deserialize, Default)]
struct KnowledgePin {
    path: String,
    #[serde(default)]
    note: String,
    #[serde(default)]
    author: String,
}

#[derive(Deserialize, Default)]
struct KnowledgeList {
    #[serde(default)]
    pins: Vec<KnowledgePin>,
    #[serde(rename = "markdownFile", default)]
    markdown_file: String,
}

#[derive(Clone, Deserialize, Default)]
struct ArtifactMeta {
    id: String,
    #[serde(rename = "type")]
    kind: String,
    #[serde(default)]
    title: String,
    version: i64,
    #[serde(rename = "ownerSession", default)]
    owner_session: String,
}

#[derive(Deserialize, Default)]
struct ArtifactList {
    #[serde(default)]
    artifacts: Vec<ArtifactMeta>,
}

#[derive(Clone, Deserialize, Default)]
struct ArtifactDetail {
    id: String,
    #[serde(rename = "type", default)]
    kind: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    content: String,
    version: i64,
    #[serde(default)]
    versions: Vec<i64>,
}

#[derive(Deserialize, Default)]
struct FileBody {
    #[serde(rename = "Content", alias = "content", default)]
    content: String,
    #[serde(rename = "Notice", alias = "notice", default)]
    notice: String,
    #[serde(rename = "Skip", alias = "skip", default)]
    skip: bool,
}

/// Bootstrap `ledger` capability (#1485): the Decisions palette command
/// and tab exist only when the daemon serves `/v1/ledger`. Shell command
/// predicates are plain `fn() -> bool`, so the flag lives here as well as
/// in the page's signal.
thread_local! {
    static LEDGER_CAPABLE: std::cell::Cell<bool> = const { std::cell::Cell::new(false) };
}

pub fn ledger_capable() -> bool {
    LEDGER_CAPABLE.with(|slot| slot.get())
}

pub fn set_ledger_capable(on: bool) {
    LEDGER_CAPABLE.with(|slot| slot.set(on));
}

/// Ledger kinds and statuses exactly as the daemon stores them
/// (internal/persist/ledger). Filters send these values verbatim.
pub const LEDGER_KINDS: &[&str] = &["decision", "assumption", "constraint"];
pub const LEDGER_STATUSES: &[&str] = &["active", "superseded", "invalidated"];
/// History page size: the daemon caps `/v1/ledger` lists at 200.
pub const LEDGER_HISTORY_LIMIT: u32 = 200;

/// One `/v1/ledger` entry (ledgerEntryDTO in
/// internal/frontend/server/artifacts_ledger.go).
#[derive(Clone, Debug, Deserialize, Default, PartialEq)]
struct LedgerEntry {
    id: String,
    #[serde(default)]
    kind: String,
    #[serde(default)]
    statement: String,
    #[serde(default)]
    confidence: String,
    #[serde(rename = "evidenceRefs", default)]
    evidence_refs: Vec<String>,
    #[serde(default)]
    status: String,
    #[serde(rename = "scopePaths", default)]
    scope_paths: Vec<String>,
    #[serde(rename = "scopeTaskIds", default)]
    scope_task_ids: Vec<String>,
    #[serde(rename = "authorSession", default)]
    author_session: String,
    #[serde(rename = "authorAgent", default)]
    author_agent: String,
    #[serde(rename = "createdAt", default)]
    created_at: String,
    #[serde(rename = "invalidateReason", default)]
    invalidate_reason: String,
    #[serde(rename = "invalidateEvidence", default)]
    invalidate_evidence: Vec<String>,
    #[serde(rename = "supersededBy", default)]
    superseded_by: String,
    #[serde(default)]
    supersedes: String,
}

#[derive(Deserialize, Default)]
struct LedgerList {
    #[serde(default)]
    entries: Vec<LedgerEntry>,
}

/// The `/v1/ledger` request for the current controls (#1485). Filtering
/// is the daemon's: the client only picks query parameters. The default
/// view is the active slice (`active=1`, what sessions load into the
/// prompt); a kind filter on that view asks for `status=active&kind=…`
/// because the active-slice mode takes no kind parameter.
fn ledger_query(history: bool, status: &str, kind: &str) -> String {
    if !history {
        if kind.is_empty() {
            return "/v1/ledger?active=1".to_string();
        }
        return format!("/v1/ledger?status=active&kind={kind}");
    }
    let mut q = format!("/v1/ledger?limit={LEDGER_HISTORY_LIMIT}");
    if !status.is_empty() {
        q.push_str(&format!("&status={status}"));
    }
    if !kind.is_empty() {
        q.push_str(&format!("&kind={kind}"));
    }
    q
}

fn ledger_kind_label(kind: &str) -> &'static str {
    match kind {
        "" => "All kinds",
        "decision" => "Decision",
        "assumption" => "Assumption",
        "constraint" => "Constraint",
        _ => "Entry",
    }
}

fn ledger_status_label(status: &str) -> &'static str {
    match status {
        "" => "All statuses",
        "active" => "Active",
        "superseded" => "Superseded",
        "invalidated" => "Invalidated",
        _ => "Unknown",
    }
}

/// Who recorded an entry, and with what confidence.
fn ledger_meta(e: &LedgerEntry) -> String {
    let mut parts = Vec::new();
    if !e.confidence.is_empty() {
        parts.push(format!("{} confidence", e.confidence));
    }
    let who = match (e.author_agent.is_empty(), e.author_session.is_empty()) {
        (false, false) => format!("{} in {}", e.author_agent, e.author_session),
        (false, true) => e.author_agent.clone(),
        (true, false) => e.author_session.clone(),
        (true, true) => "unknown author".to_string(),
    };
    parts.push(who);
    parts.join(" · ")
}

/// Scope paths and task ids, or an honest "whole project".
fn ledger_scope(e: &LedgerEntry) -> String {
    let mut scope: Vec<String> = e.scope_paths.clone();
    scope.extend(e.scope_task_ids.iter().map(|t| format!("task {t}")));
    if scope.is_empty() {
        "Scope: whole project".to_string()
    } else {
        format!("Scope: {}", scope.join(", "))
    }
}

fn ledger_clip(s: &str, max: usize) -> String {
    let flat = s.split_whitespace().collect::<Vec<_>>().join(" ");
    if flat.chars().count() > max {
        let head: String = flat.chars().take(max).collect();
        format!("{head}…")
    } else {
        flat
    }
}

/// Empty-state headline: distinguishes "nothing recorded" from "the
/// filters hide everything" from "nothing active, history exists maybe".
fn ledger_empty_label(history: bool, status: &str, kind: &str) -> &'static str {
    if !status.is_empty() || !kind.is_empty() {
        "No entries match these filters"
    } else if history {
        "No decisions recorded yet"
    } else {
        "No active decisions"
    }
}

#[component]
pub fn StoragePanel(
    tab: RwSignal<String>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    memory_mode: RwSignal<String>,
    status: RwSignal<String>,
    storage_rev: RwSignal<u32>,
) -> impl IntoView {
    let scope = RwSignal::new("project".to_string());
    let entries = RwSignal::new(Vec::<MemoryEntry>::new());
    let pins = RwSignal::new(Vec::<KnowledgePin>::new());
    let artifacts = RwSignal::new(Vec::<ArtifactMeta>::new());
    let detail = RwSignal::new(None::<ArtifactDetail>);
    let preview = RwSignal::new(String::new());
    let error = RwSignal::new(String::new());
    let markdown = RwSignal::new(String::new());
    let audit = RwSignal::new(String::new());
    let key_in = RwSignal::new(String::new());
    let value_in = RwSignal::new(String::new());
    let tags_in = RwSignal::new(String::new());
    let path_in = RwSignal::new(String::new());
    let note_in = RwSignal::new(String::new());
    let selected = RwSignal::new(String::new());
    let pin_loading = RwSignal::new(false);
    let artifact_loading = RwSignal::new(false);
    let artifact_label = RwSignal::new(String::new());
    let list_loading = RwSignal::new(false);
    let list_epoch = RwSignal::new(LoadEpoch::new());
    let pin_epoch = RwSignal::new(LoadEpoch::new());
    let artifact_epoch = RwSignal::new(LoadEpoch::new());
    let list_key = RwSignal::new(String::new());
    let exporting = RwSignal::new(false);
    let exported = RwSignal::new(String::new());

    let reload = move || {
        let tab_now = tab.get_untracked();
        let (b, t, root_now, scope_now) = (
            base.get_untracked(),
            token.get_untracked(),
            root.get_untracked(),
            scope.get_untracked(),
        );
        let key = format!("{tab_now}|{scope_now}|{root_now}");
        let gen = begin(list_epoch);
        list_loading.set(true);
        if list_key.get_untracked() != key {
            entries.set(Vec::new());
            pins.set(Vec::new());
            artifacts.set(Vec::new());
            markdown.set(String::new());
            audit.set(String::new());
            error.set(String::new());
            preview.set(String::new());
            detail.set(None);
            selected.set(String::new());
            artifact_label.set(String::new());
            pin_loading.set(false);
            artifact_loading.set(false);
            let _ = begin(pin_epoch);
            let _ = begin(artifact_epoch);
            list_key.set(key.clone());
        }
        let list_epoch = list_epoch;
        spawn_local(async move {
            let result = if tab_now == "memory" {
                load_memory(&b, &t, &scope_now).await
            } else if tab_now == "knowledge" {
                load_knowledge(&b, &t, &scope_now).await
            } else if tab_now == "artifacts" {
                load_artifacts(&b, &t, &root_now).await
            } else {
                return;
            };
            if !list_epoch.with_untracked(|epoch| epoch.accept(gen)) {
                return;
            }
            list_loading.set(false);
            match result {
                Ok(loaded) => {
                    error.set(String::new());
                    markdown.set(loaded.markdown);
                    audit.set(loaded.audit);
                    if tab_now == "memory" {
                        entries.set(loaded.entries);
                    } else if tab_now == "knowledge" {
                        pins.set(loaded.pins);
                    } else {
                        artifacts.set(loaded.artifacts);
                    }
                    list_key.set(key);
                }
                Err(e) => {
                    error.set(e);
                    list_key.set(key);
                }
            }
        });
    };

    Effect::new(move |_| {
        let tab = tab.get();
        let _ = (scope.get(), root.get(), storage_rev.get());
        if tab == "memory" || tab == "knowledge" || tab == "artifacts" {
            reload();
        }
    });

    let save_memory = move || {
        let key = key_in.get_untracked().trim().to_string();
        if key.is_empty() {
            error.set("Key is required.".into());
            return;
        }
        let tags: Vec<String> = tags_in
            .get_untracked()
            .split(',')
            .map(|s| s.trim().to_string())
            .filter(|s| !s.is_empty())
            .collect();
        let body = serde_json::json!({
            "value": value_in.get_untracked(),
            "tags": tags,
            "author": "operator",
            "scope": scope.get_untracked(),
        });
        let (b, t, scope) = (
            base.get_untracked(),
            token.get_untracked(),
            scope.get_untracked(),
        );
        let path = format!("/v1/memory/{}?scope={}", crate::api::js_encode(&key), scope);
        spawn_local(async move {
            match put_json(&b, &t, &path, body).await {
                Ok(_) => {
                    status.set(format!("Saved memory {key}"));
                    error.set(String::new());
                    reload();
                }
                Err(e) => error.set(e),
            }
        });
    };

    let delete_memory = move |key: String| {
        let (b, t, scope) = (
            base.get_untracked(),
            token.get_untracked(),
            scope.get_untracked(),
        );
        let path = format!("/v1/memory/{}?scope={}", crate::api::js_encode(&key), scope);
        spawn_local(async move {
            match delete_json(&b, &t, &path, None).await {
                Ok(()) => {
                    status.set(format!("Deleted memory {key}"));
                    reload();
                }
                Err(e) => error.set(e),
            }
        });
    };

    let export_memory = move || {
        if exporting.get_untracked() {
            return;
        }
        exporting.set(true);
        exported.set(String::new());
        error.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            // get_text preserves the daemon's error body, so a failure
            // (including memory-not-configured) surfaces its message.
            // The downloaded bytes are the daemon response untouched —
            // never assembled client-side from the entry list.
            let result = get_text(&b, &t, MEMORY_EXPORT_PATH).await;
            match memory_export_decision(result) {
                MemoryExportDecision::Download(text) => {
                    crate::transcript_view::save_download(
                        MEMORY_EXPORT_FILENAME,
                        "application/json",
                        &text,
                    );
                    let msg =
                        format!("Downloaded {MEMORY_EXPORT_FILENAME} (Project memory export).");
                    exported.set(msg.clone());
                    status.set(msg);
                }
                MemoryExportDecision::ShowError(e) => {
                    // No file is saved on failure.
                    error.set(e);
                }
            }
            exporting.set(false);
        });
    };

    let save_pin = move || {
        let path = path_in.get_untracked().trim().to_string();
        if path.is_empty() {
            error.set("Path is required.".into());
            return;
        }
        let body = serde_json::json!({
            "path": path,
            "note": note_in.get_untracked(),
            "author": "operator",
            "scope": scope.get_untracked(),
        });
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match put_json(&b, &t, "/v1/knowledge", body).await {
                Ok(_) => {
                    status.set(format!("Pinned {path}"));
                    error.set(String::new());
                    reload();
                }
                Err(e) => error.set(e),
            }
        });
    };

    let unpin = move |path: String| {
        let body = serde_json::json!({"path": path, "scope": scope.get_untracked()});
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match delete_json(&b, &t, "/v1/knowledge", Some(body)).await {
                Ok(()) => {
                    status.set(format!("Unpinned {path}"));
                    reload();
                }
                Err(e) => error.set(e),
            }
        });
    };

    let open_pin = move |path: String| {
        let gen = begin(pin_epoch);
        selected.set(path.clone());
        preview.set(String::new());
        pin_loading.set(true);
        error.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let pin_epoch = pin_epoch;
        spawn_local(async move {
            let q = format!("/v1/file?path={}", crate::api::js_encode(&path));
            let fetched = get_json::<FileBody>(&b, &t, &q).await;
            let mapped = match fetched {
                Ok(file) => {
                    if file.skip {
                        Ok(if file.notice.is_empty() {
                            "This path could not be read.".into()
                        } else {
                            file.notice
                        })
                    } else {
                        Ok(file.content)
                    }
                }
                Err(e) => Err(e),
            };
            match pin_epoch.with_untracked(|epoch| {
                commit_load(epoch, gen, mapped, String::new(), ErrPolicy::Clear)
            }) {
                Commit::Drop => return,
                Commit::Apply {
                    value,
                    error: pane_error,
                } => {
                    preview.set(value);
                    error.set(pane_error);
                    pin_loading.set(false);
                }
            }
        });
    };

    let open_artifact = move |id: String, version: i64| {
        let gen = begin(artifact_epoch);
        let label = if version > 0 {
            format!("{id} · v{version}")
        } else {
            id.clone()
        };
        artifact_label.set(label);
        detail.set(None);
        artifact_loading.set(true);
        error.set(String::new());
        let (b, t, root) = (
            base.get_untracked(),
            token.get_untracked(),
            root.get_untracked(),
        );
        let artifact_epoch = artifact_epoch;
        spawn_local(async move {
            let mut q = format!(
                "/v1/artifacts/{}?actorSession={}&actorRoot={}",
                crate::api::js_encode(&id),
                crate::api::js_encode(&root),
                crate::api::js_encode(&root)
            );
            if version > 0 {
                q.push_str(&format!("&version={version}"));
            }
            let fetched = get_json::<ArtifactDetail>(&b, &t, &q).await;
            let mapped = match fetched {
                Ok(art) => Ok(Some(art)),
                Err(e) => Err(e),
            };
            match artifact_epoch
                .with_untracked(|epoch| commit_load(epoch, gen, mapped, None, ErrPolicy::Clear))
            {
                Commit::Drop => return,
                Commit::Apply {
                    value,
                    error: pane_error,
                } => {
                    detail.set(value);
                    error.set(pane_error);
                    artifact_loading.set(false);
                }
            }
        });
    };

    view! {
        <div class="storage"
            tabindex="0"
            on:keydown=move |ev: web_sys::KeyboardEvent| {
                if ev.key() == "Escape" {
                    let _ = begin(pin_epoch);
                    let _ = begin(artifact_epoch);
                    selected.set(String::new());
                    detail.set(None);
                    preview.set(String::new());
                    artifact_label.set(String::new());
                    pin_loading.set(false);
                    artifact_loading.set(false);
                    key_in.set(String::new());
                    value_in.set(String::new());
                }
            }
        >
            <Show when=move || tab.get() != "artifacts" && tab.get() != "decisions">
                <div class="chips" role="group" aria-label="Storage scope">
                    <button type="button" class=move || if scope.get() == "project" { "chip on" } else { "chip" }
                        on:click=move |_| scope.set("project".into())>"Project"</button>
                    <button type="button" class=move || if scope.get() == "global" { "chip on" } else { "chip" }
                        on:click=move |_| scope.set("global".into())>"Global"</button>
                </div>
            </Show>
            <Show when=move || !error.get().is_empty() && tab.get() != "decisions">
                <p class="hint storage-error" role="alert">{move || error.get()}</p>
            </Show>
            <Show when=move || tab.get() == "memory" && memory_mode.get() != "normal" && !memory_mode.get().is_empty()>
                <p class="storage-banner">
                    {move || match memory_mode.get().as_str() {
                        "incognito" => "Incognito: this session still reads memory, and the agent cannot write it. You can.".into(),
                        "temporary" => "Temporary: this session does not load memory. The store below is on disk, not in the prompt.".into(),
                        other => format!("Memory mode {other}"),
                    }}
                </p>
            </Show>
            <Show when=move || tab.get() == "memory">
                <div class="storage-export">
                    <button type="button" class="secondary" data-action="memory-export"
                        disabled=move || exporting.get()
                        on:click=move |_| export_memory()>
                        {move || if exporting.get() { "Exporting\u{2026}" } else { "Export memory" }}
                    </button>
                    <p class="hint">{MEMORY_EXPORT_HINT}</p>
                </div>
                <Show when=move || !exported.get().is_empty()>
                    <p class="hint" role="status">{move || exported.get()}</p>
                </Show>
                <form class="storage-form" on:submit=move |ev| { ev.prevent_default(); save_memory(); }>
                    <label>"Key" <input prop:value=move || key_in.get() on:input=move |ev| key_in.set(event_target_value(&ev)) /></label>
                    <label class="grow">"Value" <textarea prop:value=move || value_in.get() on:input=move |ev| value_in.set(event_target_value(&ev))></textarea></label>
                    <label>"Tags" <input placeholder="preference, instruction" prop:value=move || tags_in.get() on:input=move |ev| tags_in.set(event_target_value(&ev)) /></label>
                    <button class="primary" type="submit">"Save"</button>
                </form>
                <p class="hint">"Tags preference, instruction, and project-convention are what the agent reads at the next turn. Secrets are scrubbed before they are stored. Escape clears the form."</p>
                <Show when=move || !markdown.get().is_empty()>
                    <p class="hint storage-path">"Plain file " {move || markdown.get()}</p>
                </Show>
                <Show when=move || !audit.get().is_empty()>
                    <p class="hint storage-path">"Audit " {move || audit.get()}</p>
                </Show>
                <Show when=move || entries.get().is_empty() && error.get().is_empty() && !list_loading.get()>
                    <div class="empty-state">
                        <strong>"Nothing stored in this scope"</strong>
                        <p>"Save a key here. Tagged entries survive restarts and are read when a session starts."</p>
                    </div>
                </Show>
                <ul class="plain">
                    {move || entries.get().into_iter().map(|entry| {
                        let key = entry.key.clone();
                        let key_fill = key.clone();
                        let value = entry.value.clone();
                        let shown = {
                            let chars: String = value.chars().take(80).collect();
                            if value.chars().count() > 80 {
                                format!("{chars}…")
                            } else {
                                chars
                            }
                        };
                        let tags = entry.tags.join(", ");
                        let author = if entry.author.is_empty() { "unknown".to_string() } else { entry.author.clone() };
                        // Short relative stamp in the UI's existing
                        // vocabulary (fmt_ago, as the session list uses),
                        // inside the link block: the full RFC3339 string
                        // as a flex sibling starved the link block at
                        // panel width. Fall back to the date alone when
                        // the timestamp does not parse.
                        let when_ms = rfc3339_to_ms(&entry.updated_at);
                        let when = if when_ms > 0.0 {
                            let rel = fmt_ago((when_ms / 1000.0) as i64, (now_ms() / 1000.0) as i64);
                            if rel.is_empty() {
                                entry.updated_at.chars().take(10).collect()
                            } else {
                                rel
                            }
                        } else {
                            entry.updated_at.chars().take(10).collect::<String>()
                        };
                        let del = key.clone();
                        view! {
                            <li class="storage-row">
                                <button type="button" class="link" on:click=move |_| {
                                    key_in.set(key_fill.clone());
                                    value_in.set(value.clone());
                                    tags_in.set(tags.clone());
                                }>
                                    <span class="name">{key.clone()}</span>
                                    <span class="path">{shown}</span>
                                    <span class="path">{format!("{author} · {tags}")}</span>
                                    <span class="path">{when}</span>
                                </button>
                                <button type="button" class="secondary" on:click=move |_| delete_memory(del.clone())>"Delete"</button>
                            </li>
                        }
                    }).collect_view()}
                </ul>
            </Show>
            <Show when=move || tab.get() == "knowledge">
                <form class="storage-form" on:submit=move |ev| { ev.prevent_default(); save_pin(); }>
                    <label class="grow">"Path" <input placeholder="docs/theme.md" prop:value=move || path_in.get() on:input=move |ev| path_in.set(event_target_value(&ev)) /></label>
                    <label class="grow">"Note" <input prop:value=move || note_in.get() on:input=move |ev| note_in.set(event_target_value(&ev)) /></label>
                    <button class="primary" type="submit">"Pin"</button>
                </form>
                <p class="hint">"Pins are paths, not copies of the file. The agent sees the list when a session starts. Escape clears the preview."</p>
                <Show when=move || !markdown.get().is_empty()>
                    <p class="hint storage-path">"Plain file " {move || markdown.get()}</p>
                </Show>
                <Show when=move || pins.get().is_empty() && error.get().is_empty() && !list_loading.get()>
                    <div class="empty-state">
                        <strong>"No pinned paths"</strong>
                        <p>"Pin a doc or path the agent should know about across sessions."</p>
                    </div>
                </Show>
                <ul class="plain">
                    {move || pins.get().into_iter().map(|pin| {
                        let path = pin.path.clone();
                        let path_open = path.clone();
                        let path_del = path.clone();
                        let note = pin.note.clone();
                        let author = pin.author.clone();
                        view! {
                            <li class="storage-row">
                                <button type="button" class="link" on:click=move |_| open_pin(path_open.clone())>
                                    <span class="name">{path.clone()}</span>
                                    <span class="path">{if note.is_empty() { author } else { format!("{note} · {author}") }}</span>
                                </button>
                                <button type="button" class="secondary" on:click=move |_| unpin(path_del.clone())>"Unpin"</button>
                            </li>
                        }
                    }).collect_view()}
                </ul>
                <Show when=move || !selected.get().is_empty()>
                    <p class="hint storage-path">{move || selected.get()}</p>
                </Show>
                <Show when=move || pin_loading.get()>
                    <p class="hint">"Loading preview…"</p>
                </Show>
                <Show when=move || !preview.get().is_empty()>
                    <pre class="storage-preview">{move || preview.get()}</pre>
                </Show>
            </Show>
            <Show when=move || tab.get() == "artifacts">
                <p class="hint">"Version is the CAS token. Older bodies stay on the same artifact, up to the last 20. Content is scrubbed before it is stored."</p>
                <Show when=move || artifacts.get().is_empty() && error.get().is_empty() && !list_loading.get()>
                    <div class="empty-state">
                        <strong>"No artifacts for this session"</strong>
                        <p>"When an agent writes a findings, patch, or test report, it shows up here."</p>
                    </div>
                </Show>
                <ul class="plain">
                    {move || artifacts.get().into_iter().map(|art| {
                        let id = art.id.clone();
                        let id_open = id.clone();
                        let title = if art.title.is_empty() { art.kind.clone() } else { art.title.clone() };
                        let meta = format!("v{} · {} · {}", art.version, art.kind, art.owner_session);
                        view! {
                            <li class="storage-row">
                                <button type="button" class="link" on:click=move |_| open_artifact(id_open.clone(), 0)>
                                    <span class="name">{title}</span>
                                    <span class="path">{meta}</span>
                                </button>
                                <span class="hint">{id}</span>
                            </li>
                        }
                    }).collect_view()}
                </ul>
                <Show when=move || artifact_loading.get()>
                    <p class="hint">{move || format!("Loading {}…", artifact_label.get())}</p>
                </Show>
                <Show when=move || !artifact_loading.get() && detail.get().is_none() && !artifact_label.get().is_empty() && !error.get().is_empty()>
                    <p class="hint storage-path">{move || artifact_label.get()}</p>
                </Show>
                {move || detail.get().map(|art| {
                    let versions = if art.versions.is_empty() { vec![art.version] } else { art.versions.clone() };
                    let id = art.id.clone();
                    let body = art.content.clone();
                    let heading = if art.title.is_empty() { art.kind.clone() } else { art.title.clone() };
                    let ident = format!("{} · v{}", art.id, art.version);
                    view! {
                        <div class="storage-detail">
                            <strong>{heading}</strong>
                            <p class="hint storage-path">{ident}</p>
                            <div class="chips">
                                {versions.into_iter().map(|v| {
                                    let id_v = id.clone();
                                    let on = v == art.version;
                                    let klass = if on { "chip on" } else { "chip" };
                                    view! { <button type="button" class=klass on:click=move |_| open_artifact(id_v.clone(), v)>{format!("v{v}")}</button> }
                                }).collect_view()}
                            </div>
                            <pre class="storage-preview">{body}</pre>
                        </div>
                    }
                })}
            </Show>
            <Show when=move || tab.get() == "decisions">
                <DecisionsPanel base=base token=token storage_rev=storage_rev />
            </Show>
        </div>
    }
}

/// Read-only decision ledger (#1485). Writes stay with agents
/// (`ledger_write`); this panel never invalidates or supersedes.
#[component]
fn DecisionsPanel(
    base: RwSignal<String>,
    token: RwSignal<String>,
    storage_rev: RwSignal<u32>,
) -> impl IntoView {
    let history = RwSignal::new(false);
    let status_filter = RwSignal::new(String::new());
    let kind_filter = RwSignal::new(String::new());
    let rows = RwSignal::new(Vec::<LedgerEntry>::new());
    let loading = RwSignal::new(false);
    let loaded = RwSignal::new(false);
    let error = RwSignal::new(String::new());
    let detail = RwSignal::new(None::<LedgerEntry>);
    let detail_error = RwSignal::new(String::new());
    let list_epoch = RwSignal::new(LoadEpoch::new());
    let detail_epoch = RwSignal::new(LoadEpoch::new());

    Effect::new(move |_| {
        let q = ledger_query(history.get(), &status_filter.get(), &kind_filter.get());
        let _ = storage_rev.get();
        let (b, t) = (base.get(), token.get());
        let gen = begin(list_epoch);
        loading.set(true);
        spawn_local(async move {
            let result = get_json::<LedgerList>(&b, &t, &q).await;
            if !list_epoch.with_untracked(|epoch| epoch.accept(gen)) {
                return;
            }
            loading.set(false);
            loaded.set(true);
            match result {
                Ok(list) => {
                    error.set(String::new());
                    rows.set(list.entries);
                }
                Err(e) => {
                    rows.set(Vec::new());
                    error.set(e);
                }
            }
        });
    });

    let open_entry = move |id: String| {
        let gen = begin(detail_epoch);
        detail_error.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/ledger/{}", crate::api::js_encode(&id));
            let fetched = get_json::<LedgerEntry>(&b, &t, &path).await;
            if !detail_epoch.with_untracked(|epoch| epoch.accept(gen)) {
                return;
            }
            match fetched {
                Ok(entry) => detail.set(Some(entry)),
                Err(e) => {
                    detail.set(None);
                    detail_error.set(e);
                }
            }
        });
    };

    view! {
        <div class="ledger" data-ledger="true">
            <p class="hint">"Decisions, assumptions, and constraints agents recorded with ledger_write. Active entries are loaded into every session's prompt. Read-only here."</p>
            <div class="chips" role="group" aria-label="Ledger view">
                <button type="button" data-ledger-view="active"
                    aria-pressed=move || (!history.get()).to_string()
                    class=move || if history.get() { "chip" } else { "chip on" }
                    on:click=move |_| {
                        history.set(false);
                        status_filter.set(String::new());
                    }>"Active"</button>
                <button type="button" data-ledger-view="history"
                    aria-pressed=move || history.get().to_string()
                    class=move || if history.get() { "chip on" } else { "chip" }
                    on:click=move |_| history.set(true)>"Include history"</button>
            </div>
            <div class="chips" role="group" aria-label="Ledger kind">
                {std::iter::once("").chain(LEDGER_KINDS.iter().copied()).map(|k| {
                    view! {
                        <button type="button" data-ledger-kind=k
                            aria-pressed=move || (kind_filter.get() == k).to_string()
                            class=move || if kind_filter.get() == k { "chip on" } else { "chip" }
                            on:click=move |_| kind_filter.set(k.to_string())>{ledger_kind_label(k)}</button>
                    }
                }).collect_view()}
            </div>
            <Show when=move || history.get()>
                <div class="chips" role="group" aria-label="Ledger status">
                    {std::iter::once("").chain(LEDGER_STATUSES.iter().copied()).map(|st| {
                        view! {
                            <button type="button" data-ledger-status-filter=st
                                aria-pressed=move || (status_filter.get() == st).to_string()
                                class=move || if status_filter.get() == st { "chip on" } else { "chip" }
                                on:click=move |_| status_filter.set(st.to_string())>{ledger_status_label(st)}</button>
                        }
                    }).collect_view()}
                </div>
            </Show>
            <Show when=move || !error.get().is_empty()>
                <p class="hint storage-error" role="alert">{move || error.get()}</p>
            </Show>
            <Show when=move || loaded.get() && !loading.get() && error.get().is_empty() && rows.get().is_empty()>
                <div class="empty-state" data-ledger-empty="true">
                    <strong>{move || ledger_empty_label(history.get(), &status_filter.get(), &kind_filter.get())}</strong>
                    <p>"Agents add entries with ledger_write when they settle a decision, assumption, or constraint. Superseded and invalidated entries stay under Include history."</p>
                </div>
            </Show>
            <ul class="plain">
                {move || rows.get().into_iter().map(|e| {
                    let id_attr = e.id.clone();
                    let id_open = e.id.clone();
                    let status_attr = e.status.clone();
                    let title = ledger_clip(&e.statement, 140);
                    let badge = format!("{} · {}", ledger_kind_label(&e.kind), ledger_status_label(&e.status));
                    let meta = ledger_meta(&e);
                    let scope = ledger_scope(&e);
                    view! {
                        <li class="storage-row ledger-row" data-ledger-id=id_attr data-ledger-status=status_attr>
                            <button type="button" class="link" on:click=move |_| open_entry(id_open.clone())>
                                <span class="name">{title}</span>
                                <span class="path ledger-badge">{badge}</span>
                                <span class="path">{meta}</span>
                                <span class="path">{scope}</span>
                            </button>
                        </li>
                    }
                }).collect_view()}
            </ul>
            <Show when=move || !detail_error.get().is_empty()>
                <p class="hint storage-error" role="alert">{move || detail_error.get()}</p>
            </Show>
            {move || detail.get().map(|e| {
                let heading = format!("{} · {}", ledger_kind_label(&e.kind), ledger_status_label(&e.status));
                let ident = format!("{} · recorded {}", e.id, e.created_at.chars().take(10).collect::<String>());
                let detail_attr = e.id.clone();
                let status_attr = e.status.clone();
                let statement = e.statement.clone();
                let meta = ledger_meta(&e);
                let scope = ledger_scope(&e);
                let evidence = e.evidence_refs.join(", ");
                let reason = e.invalidate_reason.clone();
                let inv_evidence = e.invalidate_evidence.join(", ");
                let supersedes = e.supersedes.clone();
                let superseded_by = e.superseded_by.clone();
                let author = e.author_session.clone();
                view! {
                    <div class="storage-detail ledger-detail" data-ledger-detail=detail_attr data-ledger-status=status_attr>
                        <strong>{heading}</strong>
                        <p class="hint storage-path">{ident}</p>
                        <p class="ledger-statement">{statement}</p>
                        <p class="hint">{meta}</p>
                        <p class="hint">{scope}</p>
                        {(!evidence.is_empty()).then(|| view! {
                            <p class="hint">{format!("Evidence: {evidence}")}</p>
                        })}
                        {(!reason.is_empty()).then(|| view! {
                            <p class="hint ledger-invalidated">{format!("Invalidated: {reason}")}</p>
                        })}
                        {(!inv_evidence.is_empty()).then(|| view! {
                            <p class="hint">{format!("Invalidation evidence: {inv_evidence}")}</p>
                        })}
                        {(!superseded_by.is_empty()).then(|| {
                            let id_open = superseded_by.clone();
                            let label = superseded_by.clone();
                            view! {
                                <p class="hint">"Superseded by "
                                    <button type="button" class="ledger-link" data-ledger-chain="superseded-by"
                                        on:click=move |_| open_entry(id_open.clone())>{label}</button>
                                </p>
                            }
                        })}
                        {(!supersedes.is_empty()).then(|| {
                            let id_open = supersedes.clone();
                            let label = supersedes.clone();
                            view! {
                                <p class="hint">"Replaces "
                                    <button type="button" class="ledger-link" data-ledger-chain="supersedes"
                                        on:click=move |_| open_entry(id_open.clone())>{label}</button>
                                </p>
                            }
                        })}
                        {(!author.is_empty()).then(|| {
                            let id_open = author.clone();
                            let label = author.clone();
                            view! {
                                <p class="hint">"Recorded in session "
                                    <button type="button" class="ledger-link" data-ledger-session="true"
                                        on:click=move |_| {
                                            if let Err(e) = crate::shell::open_session(&id_open) {
                                                detail_error.set(e);
                                            }
                                        }>{label}</button>
                                </p>
                            }
                        })}
                    </div>
                }
            })}
        </div>
    }
}

struct Loaded {
    entries: Vec<MemoryEntry>,
    pins: Vec<KnowledgePin>,
    artifacts: Vec<ArtifactMeta>,
    markdown: String,
    audit: String,
}

async fn load_memory(base: &str, token: &str, scope: &str) -> Result<Loaded, String> {
    let path = format!("/v1/memory?scope={}", crate::api::js_encode(scope));
    let list = get_json::<MemoryList>(base, token, &path).await?;
    Ok(Loaded {
        entries: list.entries,
        markdown: list.markdown_file,
        audit: list.audit_file,
        ..Default::default()
    })
}

async fn load_knowledge(base: &str, token: &str, scope: &str) -> Result<Loaded, String> {
    let path = format!("/v1/knowledge?scope={}", crate::api::js_encode(scope));
    let list = get_json::<KnowledgeList>(base, token, &path).await?;
    Ok(Loaded {
        pins: list.pins,
        markdown: list.markdown_file,
        ..Default::default()
    })
}

async fn load_artifacts(base: &str, token: &str, root: &str) -> Result<Loaded, String> {
    if root.is_empty() {
        return Err("No live session yet. Artifacts are listed for the open session.".into());
    }
    let path = format!(
        "/v1/artifacts?actorSession={}&actorRoot={}",
        crate::api::js_encode(root),
        crate::api::js_encode(root)
    );
    let list = get_json::<ArtifactList>(base, token, &path).await?;
    Ok(Loaded {
        artifacts: list.artifacts,
        ..Default::default()
    })
}

impl Default for Loaded {
    fn default() -> Self {
        Self {
            entries: Vec::new(),
            pins: Vec::new(),
            artifacts: Vec::new(),
            markdown: String::new(),
            audit: String::new(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn export_targets_the_daemon_route_and_filename() {
        // The daemon serves GET /v1/memory/export and sends
        // Content-Disposition: attachment; filename="strike-memory.json"
        // (internal/frontend/server/api.go handleMemoryExport).
        assert_eq!(MEMORY_EXPORT_PATH, "/v1/memory/export");
        assert_eq!(MEMORY_EXPORT_FILENAME, "strike-memory.json");
    }

    #[test]
    fn export_success_downloads_the_daemon_bytes_untouched() {
        // Stubbed daemon response: the decision must carry the exact
        // bytes through to the download, never a client-assembled copy.
        let stubbed = r#"{"entries":[{"key":"pref","value":"likes plain files"}]}"#;
        assert_eq!(
            memory_export_decision(Ok(stubbed.to_string())),
            MemoryExportDecision::Download(stubbed.to_string())
        );
    }

    #[test]
    fn export_empty_store_still_downloads_what_the_daemon_returns() {
        // An empty store is not a UI error and no empty file is
        // fabricated client-side: whatever the daemon returns downloads.
        assert_eq!(
            memory_export_decision(Ok("{}".to_string())),
            MemoryExportDecision::Download("{}".to_string())
        );
    }

    #[test]
    fn export_error_saves_no_file_and_surfaces_the_daemon_message() {
        let decision =
            memory_export_decision(Err("HTTP 400 memory is not configured".to_string()));
        assert_eq!(
            decision,
            MemoryExportDecision::ShowError("HTTP 400 memory is not configured".to_string())
        );
        assert!(!matches!(decision, MemoryExportDecision::Download(_)));
    }

    #[test]
    fn ledger_query_defaults_to_the_active_slice() {
        assert_eq!(ledger_query(false, "", ""), "/v1/ledger?active=1");
        // The active slice takes no kind parameter, so a kind filter asks
        // the daemon for status=active with that kind instead.
        assert_eq!(
            ledger_query(false, "", "assumption"),
            "/v1/ledger?status=active&kind=assumption"
        );
        // The status filter only exists with history on; the active view
        // ignores a stale value rather than sending it.
        assert_eq!(ledger_query(false, "superseded", ""), "/v1/ledger?active=1");
    }

    #[test]
    fn ledger_query_history_passes_daemon_filters_verbatim() {
        assert_eq!(ledger_query(true, "", ""), "/v1/ledger?limit=200");
        assert_eq!(
            ledger_query(true, "superseded", ""),
            "/v1/ledger?limit=200&status=superseded"
        );
        assert_eq!(
            ledger_query(true, "invalidated", "constraint"),
            "/v1/ledger?limit=200&status=invalidated&kind=constraint"
        );
        assert_eq!(
            ledger_query(true, "", "decision"),
            "/v1/ledger?limit=200&kind=decision"
        );
    }

    #[test]
    fn ledger_labels_cover_every_daemon_kind_and_status() {
        for k in LEDGER_KINDS {
            assert_ne!(ledger_kind_label(k), "Entry", "{k}");
        }
        for st in LEDGER_STATUSES {
            assert_ne!(ledger_status_label(st), "Unknown", "{st}");
        }
        assert_eq!(ledger_kind_label(""), "All kinds");
        assert_eq!(ledger_status_label(""), "All statuses");
        assert_eq!(ledger_status_label("superseded"), "Superseded");
    }

    #[test]
    fn ledger_entry_parses_the_daemon_dto() {
        let raw = r#"{"id":"led_new","kind":"decision","statement":"Use JSONC","confidence":"high",
            "evidenceRefs":["docs/config.md"],"status":"active","scopePaths":["internal/product/config"],
            "scopeTaskIds":["t1"],"authorSession":"s1","authorAgent":"build","authorRoot":"s1",
            "createdAt":"2026-11-01T10:00:00Z","updatedAt":"2026-11-01T10:00:00Z",
            "supersedes":"led_old"}"#;
        let e: LedgerEntry = serde_json::from_str(raw).expect("parse");
        assert_eq!(e.kind, "decision");
        assert_eq!(e.evidence_refs, vec!["docs/config.md".to_string()]);
        assert_eq!(e.supersedes, "led_old");
        assert!(e.superseded_by.is_empty());
        assert_eq!(ledger_meta(&e), "high confidence · build in s1");
        assert_eq!(ledger_scope(&e), "Scope: internal/product/config, task t1");
        let list: LedgerList =
            serde_json::from_str(r#"{"entries":[],"slice":"active"}"#).expect("list");
        assert!(list.entries.is_empty());
        // An invalidated entry carries its reason; nulls stay tolerated.
        let inv: LedgerEntry = serde_json::from_str(
            r#"{"id":"x","kind":"assumption","statement":"s","status":"invalidated","authorSession":"",
               "invalidateReason":"CI has network","invalidatedAt":"2026-11-02T00:00:00Z"}"#,
        )
        .expect("invalidated");
        assert_eq!(inv.invalidate_reason, "CI has network");
        assert_eq!(ledger_meta(&inv), "unknown author");
        assert_eq!(ledger_scope(&inv), "Scope: whole project");
    }

    #[test]
    fn ledger_clip_and_empty_labels_are_honest() {
        assert_eq!(ledger_clip("a  b\nc", 10), "a b c");
        assert_eq!(ledger_clip("abcdef", 3), "abc…");
        assert_eq!(ledger_empty_label(false, "", ""), "No active decisions");
        assert_eq!(ledger_empty_label(true, "", ""), "No decisions recorded yet");
        assert_eq!(ledger_empty_label(false, "", "decision"), "No entries match these filters");
        assert_eq!(ledger_empty_label(true, "superseded", ""), "No entries match these filters");
    }

    #[test]
    fn ledger_capability_flag_round_trips() {
        assert!(!ledger_capable());
        set_ledger_capable(true);
        assert!(ledger_capable());
        set_ledger_capable(false);
        assert!(!ledger_capable());
    }

    #[test]
    fn export_hint_is_honest_about_encryption_and_scope() {
        assert!(MEMORY_EXPORT_HINT.contains("unencrypted"));
        assert!(MEMORY_EXPORT_HINT.contains("Project"));
        assert!(MEMORY_EXPORT_HINT.contains("Global"));
        // It must never claim the Global store is what gets exported.
        assert!(!MEMORY_EXPORT_HINT.contains("exports Global"));
    }
}
