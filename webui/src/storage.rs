//! Memory, knowledge, and artifact panels. feature:storage

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

/// Explicit page size for the artifacts list (issue #1456, Q1 recommended
/// default): the server's own default, so small lists are unchanged and
/// "Show more" only appears where rows previously vanished silently.
const ARTIFACT_PAGE_SIZE: usize = 100;

/// Registered built-in artifact types offered by the type filter.
/// This must match the store's `ValidType` set exactly: the server
/// rejects an unknown `type` filter with an error, so offering a type
/// that is not registered yet (e.g. `session_report`, mentioned in
/// #1456 as arriving with #1444 but not registered on this main)
/// would break the list instead of filtering it.
const ARTIFACT_TYPES: &[&str] = &["plan", "contract", "findings", "patch", "test_report"];

/// Build the server-filtered artifacts list path. `type` is only sent
/// when a filter is selected; `limit`/`offset` are always explicit so
/// paging is honest rather than relying on the server's silent cap.
fn artifacts_list_path(root: &str, type_filter: &str, limit: usize, offset: usize) -> String {
    let mut path = format!(
        "/v1/artifacts?actorSession={}&actorRoot={}&limit={}&offset={}",
        crate::api::js_encode(root),
        crate::api::js_encode(root),
        limit,
        offset
    );
    if !type_filter.is_empty() {
        path.push_str(&format!("&type={}", crate::api::js_encode(type_filter)));
    }
    path
}

/// A page is possibly truncated only when it came back exactly full;
/// the API returns no total, so nothing stronger may be claimed.
fn artifact_page_has_more(returned: usize, limit: usize) -> bool {
    limit > 0 && returned == limit
}

/// Append a "Show more" page, skipping ids already listed. The list is
/// ordered by updatedAt desc, so an artifact updated between page loads
/// shifts the offset window and would otherwise be listed twice.
fn append_artifact_page(cur: &mut Vec<ArtifactMeta>, page: Vec<ArtifactMeta>) {
    let seen: std::collections::HashSet<String> = cur.iter().map(|a| a.id.clone()).collect();
    cur.extend(page.into_iter().filter(|a| !seen.contains(&a.id)));
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
    let artifact_type_filter = RwSignal::new(String::new());
    let artifact_has_more = RwSignal::new(false);
    let artifact_loading_more = RwSignal::new(false);
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
        let filter_now = artifact_type_filter.get_untracked();
        let key = format!("{tab_now}|{scope_now}|{root_now}|{filter_now}");
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
            artifact_has_more.set(false);
            artifact_loading_more.set(false);
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
                load_artifacts(&b, &t, &root_now, &filter_now, ARTIFACT_PAGE_SIZE, 0).await
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
                        artifact_has_more.set(artifact_page_has_more(
                            loaded.artifacts.len(),
                            ARTIFACT_PAGE_SIZE,
                        ));
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
        let _ = (
            scope.get(),
            root.get(),
            storage_rev.get(),
            artifact_type_filter.get(),
        );
        if tab == "memory" || tab == "knowledge" || tab == "artifacts" {
            reload();
        }
    });

    // Append the next artifacts page. This is a continuation of the
    // current list load, not a new selection: snapshot the list epoch
    // token instead of beginning a new load, so a filter/tab change
    // that begins a real reload drops a stale append.
    let load_more_artifacts = move || {
        if artifact_loading_more.get_untracked()
            || list_loading.get_untracked()
            || !artifact_has_more.get_untracked()
        {
            return;
        }
        let offset = artifacts.get_untracked().len();
        let (b, t, root_now) = (
            base.get_untracked(),
            token.get_untracked(),
            root.get_untracked(),
        );
        let filter_now = artifact_type_filter.get_untracked();
        let gen = list_epoch.with_untracked(|epoch| epoch.token());
        artifact_loading_more.set(true);
        spawn_local(async move {
            let result =
                load_artifacts(&b, &t, &root_now, &filter_now, ARTIFACT_PAGE_SIZE, offset).await;
            if !list_epoch.with_untracked(|epoch| epoch.accept(gen)) {
                artifact_loading_more.set(false);
                return;
            }
            artifact_loading_more.set(false);
            match result {
                Ok(loaded) => {
                    error.set(String::new());
                    artifact_has_more.set(artifact_page_has_more(
                        loaded.artifacts.len(),
                        ARTIFACT_PAGE_SIZE,
                    ));
                    artifacts.update(|cur| append_artifact_page(cur, loaded.artifacts));
                }
                // Keep the rows already shown and the Show more control
                // (has_more stays true) so the user can retry; surface
                // the failure instead of silently dropping the page.
                Err(e) => error.set(e),
            }
        });
    };

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
            <Show when=move || tab.get() != "artifacts">
                <div class="chips" role="group" aria-label="Storage scope">
                    <button type="button" class=move || if scope.get() == "project" { "chip on" } else { "chip" }
                        on:click=move |_| scope.set("project".into())>"Project"</button>
                    <button type="button" class=move || if scope.get() == "global" { "chip on" } else { "chip" }
                        on:click=move |_| scope.set("global".into())>"Global"</button>
                </div>
            </Show>
            <Show when=move || !error.get().is_empty()>
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
                <div class="chips" role="group" aria-label="Artifact type filter">
                    <button type="button" class=move || if artifact_type_filter.get().is_empty() { "chip on" } else { "chip" }
                        on:click=move |_| artifact_type_filter.set(String::new())>"All"</button>
                    {ARTIFACT_TYPES.iter().copied().map(|kind| {
                        view! {
                            <button type="button" class=move || if artifact_type_filter.get() == kind { "chip on" } else { "chip" }
                                on:click=move |_| artifact_type_filter.set(kind.to_string())>{kind}</button>
                        }
                    }).collect_view()}
                </div>
                <Show when=move || !artifacts.get().is_empty()>
                    <p class="hint">{move || {
                        let n = artifacts.get().len();
                        if artifact_has_more.get() {
                            format!("Showing {n} artifacts — more available")
                        } else {
                            format!("Showing {n} artifacts")
                        }
                    }}</p>
                </Show>
                <Show when=move || artifacts.get().is_empty() && artifact_type_filter.get().is_empty() && error.get().is_empty() && !list_loading.get()>
                    <div class="empty-state">
                        <strong>"No artifacts for this session"</strong>
                        <p>"When an agent writes a findings, patch, or test report, it shows up here."</p>
                    </div>
                </Show>
                <Show when=move || artifacts.get().is_empty() && !artifact_type_filter.get().is_empty() && error.get().is_empty() && !list_loading.get()>
                    <div class="empty-state">
                        <strong>"No artifacts of this type — clear filter"</strong>
                        <p>{move || format!("Nothing of type {} for this session.", artifact_type_filter.get())}</p>
                        <button type="button" class="secondary" on:click=move |_| artifact_type_filter.set(String::new())>"Clear filter"</button>
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
                <Show when=move || artifact_has_more.get() && !artifacts.get().is_empty()>
                    <button type="button" class="secondary" prop:disabled=move || artifact_loading_more.get()
                        on:click=move |_| load_more_artifacts()>
                        {move || if artifact_loading_more.get() { "Loading…" } else { "Show more" }}
                    </button>
                </Show>
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

async fn load_artifacts(
    base: &str,
    token: &str,
    root: &str,
    type_filter: &str,
    limit: usize,
    offset: usize,
) -> Result<Loaded, String> {
    if root.is_empty() {
        return Err("No live session yet. Artifacts are listed for the open session.".into());
    }
    let path = artifacts_list_path(root, type_filter, limit, offset);
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
    fn artifacts_path_is_unfiltered_by_default_with_explicit_paging() {
        let path = artifacts_list_path("root 1", "", ARTIFACT_PAGE_SIZE, 0);
        assert!(path.starts_with("/v1/artifacts?"), "{path}");
        assert!(path.contains("limit=100"), "{path}");
        assert!(path.contains("offset=0"), "{path}");
        assert!(!path.contains("type="), "{path}");
    }

    #[test]
    fn artifacts_path_passes_type_to_the_server() {
        let path = artifacts_list_path("root1", "findings", ARTIFACT_PAGE_SIZE, 200);
        assert!(path.contains("type=findings"), "{path}");
        assert!(path.contains("offset=200"), "{path}");
    }

    #[test]
    fn has_more_only_when_page_came_back_exactly_full() {
        assert!(artifact_page_has_more(100, 100));
        assert!(!artifact_page_has_more(99, 100));
        assert!(!artifact_page_has_more(0, 100));
        assert!(!artifact_page_has_more(0, 0));
    }

    #[test]
    fn filter_offers_all_registered_built_in_types() {
        // Exactly the store's ValidType set on this main; the server
        // errors on an unregistered type filter.
        assert_eq!(
            ARTIFACT_TYPES,
            ["plan", "contract", "findings", "patch", "test_report"]
        );
        assert_eq!(ARTIFACT_PAGE_SIZE, 100);
    }

    #[test]
    fn show_more_append_skips_ids_already_listed() {
        let meta = |id: &str| ArtifactMeta {
            id: id.into(),
            ..Default::default()
        };
        let mut cur = vec![meta("a"), meta("b")];
        // "b" shifted into the next window after an update between loads.
        append_artifact_page(&mut cur, vec![meta("b"), meta("c")]);
        let ids: Vec<&str> = cur.iter().map(|a| a.id.as_str()).collect();
        assert_eq!(ids, ["a", "b", "c"]);
    }

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
    fn export_hint_is_honest_about_encryption_and_scope() {
        assert!(MEMORY_EXPORT_HINT.contains("unencrypted"));
        assert!(MEMORY_EXPORT_HINT.contains("Project"));
        assert!(MEMORY_EXPORT_HINT.contains("Global"));
        // It must never claim the Global store is what gets exported.
        assert!(!MEMORY_EXPORT_HINT.contains("exports Global"));
    }
}
