//! Memory, knowledge, and artifact panels. feature:storage

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;

use load_guard::{commit_load, Commit, ErrPolicy, LoadEpoch};

use crate::load_epoch::begin;

use crate::api::{delete_json, get_json, now_ms, put_json};
use crate::model::{fmt_ago, rfc3339_to_ms};

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

/// Lower-cased, trimmed query shared by the storage list filters
/// (#1461/#1469). An empty query matches everything.
fn norm_query(raw: &str) -> String {
    raw.trim().to_lowercase()
}

/// The author a memory row displays: the stored value, or "unknown"
/// when the store left it blank (same fallback the row rendering uses).
fn entry_author(entry: &MemoryEntry) -> &str {
    if entry.author.is_empty() {
        "unknown"
    } else {
        &entry.author
    }
}

/// Memory filter (#1461): case-insensitive substring over key, value
/// and tags, plus an exact author filter ("all" = no author filter).
/// Pure over the already-loaded list — no server query is implied.
fn memory_matches(entry: &MemoryEntry, query: &str, author: &str) -> bool {
    if author != "all" && entry_author(entry) != author {
        return false;
    }
    if query.is_empty() {
        return true;
    }
    entry.key.to_lowercase().contains(query)
        || entry.value.to_lowercase().contains(query)
        || entry.tags.iter().any(|t| t.to_lowercase().contains(query))
}

fn filtered_memory(
    entries: &[MemoryEntry],
    raw_query: &str,
    author: &str,
) -> Vec<MemoryEntry> {
    let query = norm_query(raw_query);
    entries
        .iter()
        .filter(|e| memory_matches(e, &query, author))
        .cloned()
        .collect()
}

/// Knowledge filter (#1469): case-insensitive substring over the fields
/// each pin row already renders — path, note and author.
fn pin_matches(pin: &KnowledgePin, query: &str) -> bool {
    query.is_empty()
        || pin.path.to_lowercase().contains(query)
        || pin.note.to_lowercase().contains(query)
        || pin.author.to_lowercase().contains(query)
}

fn filtered_pins(pins: &[KnowledgePin], raw_query: &str) -> Vec<KnowledgePin> {
    let query = norm_query(raw_query);
    pins.iter()
        .filter(|p| pin_matches(p, &query))
        .cloned()
        .collect()
}

/// Download extension + MIME for an artifact kind (#1453): Markdown
/// kinds get `.md`, patches get `.patch`, everything else `.txt`.
fn artifact_extension(kind: &str) -> (&'static str, &'static str) {
    match kind.trim().to_lowercase().as_str() {
        "markdown" | "md" | "plan" | "findings" | "test_report" | "contract" | "report"
        | "session_report" => ("md", "text/markdown"),
        "patch" | "diff" => ("patch", "text/plain"),
        _ => ("txt", "text/plain"),
    }
}

/// Filename stem from free text: keep alphanumerics, `-` and `_`,
/// collapse every other run to one `-`, trim edge dashes, cap length.
fn sanitize_filename_stem(raw: &str) -> String {
    let mut stem = String::new();
    for ch in raw.chars() {
        if ch.is_alphanumeric() || ch == '-' || ch == '_' {
            stem.push(ch);
        } else if !stem.is_empty() && !stem.ends_with('-') {
            stem.push('-');
        }
    }
    let stem = stem.trim_matches('-');
    stem.chars().take(80).collect::<String>().trim_matches('-').to_string()
}

/// Download filename for the displayed artifact version (#1453):
/// sanitised title, falling back to kind + id when the title yields
/// nothing usable — never a bare artifact id alone.
fn artifact_download_name(title: &str, kind: &str, id: &str) -> String {
    let mut stem = sanitize_filename_stem(title);
    if stem.is_empty() {
        stem = sanitize_filename_stem(&format!("{kind}-{id}"));
    }
    if stem.is_empty() {
        stem = "artifact".to_string();
    }
    let (ext, _) = artifact_extension(kind);
    format!("{stem}.{ext}")
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
    // Client-only list filters (#1461/#1469): never persisted, so a
    // returning visit never starts with entries silently hidden.
    let memory_query = RwSignal::new(String::new());
    let memory_author = RwSignal::new("all".to_string());
    let knowledge_query = RwSignal::new(String::new());
    // Copy feedback for the artifact detail actions (#1453), using the
    // transcript copy-label idiom: None = "Copy", Some(true) = "Copied",
    // Some(false) = "Copy failed".
    let artifact_copy_state = RwSignal::new(None::<bool>);

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
        artifact_copy_state.set(None);
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
                    artifact_copy_state.set(None);
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
                <div class="storage-filter">
                    <input
                        class="search storage-search storage-search-memory"
                        type="search"
                        placeholder="Search key, value, or tags"
                        aria-label="Search memory entries"
                        prop:value=move || memory_query.get()
                        on:input=move |ev| memory_query.set(event_target_value(&ev))
                    />
                    <p class="hint storage-count" aria-live="polite">
                        {move || {
                            let all = entries.get();
                            let visible = filtered_memory(&all, &memory_query.get(), &memory_author.get()).len();
                            format!("{visible} of {} entries", all.len())
                        }}
                    </p>
                </div>
                {move || {
                    let mut authors: Vec<String> = entries.get().iter().map(|e| entry_author(e).to_string()).collect();
                    authors.sort();
                    authors.dedup();
                    if authors.is_empty() {
                        // No rows yet: no author filter to offer (the
                        // "Nothing stored" empty state below covers it).
                        return view! { <span></span> }.into_any();
                    }
                    view! {
                        <div class="chips" role="group" aria-label="Memory author filter">
                            <button type="button" class=move || if memory_author.get() == "all" { "chip on" } else { "chip" }
                                on:click=move |_| memory_author.set("all".into())>"All"</button>
                            {authors.into_iter().map(|author| {
                                let click_author = author.clone();
                                let class_author = author.clone();
                                view! {
                                    <button type="button" class=move || if memory_author.get() == class_author { "chip on" } else { "chip" }
                                        on:click=move |_| memory_author.set(click_author.clone())>{author.clone()}</button>
                                }
                            }).collect_view()}
                        </div>
                    }.into_any()
                }}
                <Show when=move || entries.get().is_empty() && error.get().is_empty() && !list_loading.get()>
                    <div class="empty-state">
                        <strong>"Nothing stored in this scope"</strong>
                        <p>"Save a key here. Tagged entries survive restarts and are read when a session starts."</p>
                    </div>
                </Show>
                <Show when=move || !entries.get().is_empty() && filtered_memory(&entries.get(), &memory_query.get(), &memory_author.get()).is_empty()>
                    <div class="empty-state" data-memory-filtered-empty="true">
                        <strong>"No memory entries match — clear search"</strong>
                        <p>"No stored entry matches the current search and author filter. The Save form above still works."</p>
                        <button type="button" class="secondary" on:click=move |_| { memory_query.set(String::new()); memory_author.set("all".into()); }>"Clear search"</button>
                    </div>
                </Show>
                <ul class="plain">
                    {move || filtered_memory(&entries.get(), &memory_query.get(), &memory_author.get()).into_iter().map(|entry| {
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
                <div class="storage-filter">
                    <input
                        class="search storage-search storage-search-knowledge"
                        type="search"
                        placeholder="Search path, note, or author"
                        aria-label="Search knowledge pins"
                        prop:value=move || knowledge_query.get()
                        on:input=move |ev| knowledge_query.set(event_target_value(&ev))
                    />
                    <p class="hint storage-count" aria-live="polite">
                        {move || {
                            let all = pins.get();
                            let visible = filtered_pins(&all, &knowledge_query.get()).len();
                            format!("{visible} of {} pins", all.len())
                        }}
                    </p>
                </div>
                <Show when=move || pins.get().is_empty() && error.get().is_empty() && !list_loading.get()>
                    <div class="empty-state">
                        <strong>"No pinned paths"</strong>
                        <p>"Pin a doc or path the agent should know about across sessions."</p>
                    </div>
                </Show>
                <Show when=move || !pins.get().is_empty() && filtered_pins(&pins.get(), &knowledge_query.get()).is_empty()>
                    <div class="empty-state" data-knowledge-filtered-empty="true">
                        <strong>"No pins match — clear search"</strong>
                        <p>"No pinned path matches the current search. The Pin form above still works."</p>
                        <button type="button" class="secondary" on:click=move |_| knowledge_query.set(String::new())>"Clear search"</button>
                    </div>
                </Show>
                <ul class="plain">
                    {move || filtered_pins(&pins.get(), &knowledge_query.get()).into_iter().map(|pin| {
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
                    let copy_body = body.clone();
                    let dl_body = body.clone();
                    let dl_name = artifact_download_name(&art.title, &art.kind, &art.id);
                    let dl_mime = artifact_extension(&art.kind).1;
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
                            <div class="storage-detail-actions">
                                <button type="button" class="secondary" title="Copy the displayed version's content"
                                    on:click=move |_| {
                                        let text = copy_body.clone();
                                        spawn_local(async move {
                                            let ok = crate::clipboard::copy_text(&text).await.is_ok();
                                            artifact_copy_state.set(Some(ok));
                                            crate::clipboard::after_ms(2_000, move || artifact_copy_state.set(None));
                                        });
                                    }>
                                    {move || crate::clipboard::transcript_copy_label(artifact_copy_state.get())}
                                </button>
                                <button type="button" class="secondary" title="Download the displayed version's content"
                                    on:click=move |_| {
                                        crate::transcript_view::save_download(&dl_name, dl_mime, &dl_body);
                                    }>
                                    "Download"
                                </button>
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

    fn entry(key: &str, value: &str, tags: &[&str], author: &str) -> MemoryEntry {
        MemoryEntry {
            key: key.into(),
            value: value.into(),
            tags: tags.iter().map(|t| t.to_string()).collect(),
            author: author.into(),
            updated_at: String::new(),
        }
    }

    fn pin(path: &str, note: &str, author: &str) -> KnowledgePin {
        KnowledgePin {
            path: path.into(),
            note: note.into(),
            author: author.into(),
        }
    }

    #[test]
    fn memory_filter_matches_key_value_and_tags_case_insensitively() {
        let entries = vec![
            entry("theme", "Uses plain files", &["preference"], "operator"),
            entry("deploy", "Ship on Fridays", &["project-convention"], "agent"),
        ];
        assert_eq!(filtered_memory(&entries, "THE", "all").len(), 1);
        assert_eq!(filtered_memory(&entries, "fridays", "all").len(), 1);
        assert_eq!(filtered_memory(&entries, "Preference", "all").len(), 1);
        assert_eq!(filtered_memory(&entries, "  theme  ", "all").len(), 1);
        assert_eq!(filtered_memory(&entries, "", "all").len(), 2);
        assert!(filtered_memory(&entries, "nope", "all").is_empty());
    }

    #[test]
    fn memory_filter_author_composes_with_search_and_uses_row_values() {
        let entries = vec![
            entry("a", "same value", &[], "operator"),
            entry("b", "same value", &[], "agent"),
            entry("c", "same value", &[], ""),
        ];
        assert_eq!(filtered_memory(&entries, "same", "agent").len(), 1);
        assert_eq!(filtered_memory(&entries, "same", "operator").len(), 1);
        // Blank stored authors filter under the displayed "unknown".
        assert_eq!(filtered_memory(&entries, "same", "unknown").len(), 1);
        assert_eq!(filtered_memory(&entries, "", "all").len(), 3);
    }

    #[test]
    fn pin_filter_matches_path_note_and_author() {
        let pins = vec![
            pin("docs/theme.md", "Design source", "operator"),
            pin("docs/api.md", "", "agent"),
        ];
        assert_eq!(filtered_pins(&pins, "THEME").len(), 1);
        assert_eq!(filtered_pins(&pins, "design").len(), 1);
        assert_eq!(filtered_pins(&pins, "agent").len(), 1);
        assert_eq!(filtered_pins(&pins, "").len(), 2);
        assert!(filtered_pins(&pins, "nope").is_empty());
    }

    #[test]
    fn artifact_download_name_sanitises_title_and_picks_extension() {
        assert_eq!(
            artifact_download_name("My Plan: v2 / draft!", "plan", "a1"),
            "My-Plan-v2-draft.md"
        );
        assert_eq!(
            artifact_download_name("Fix thing", "patch", "a2"),
            "Fix-thing.patch"
        );
        assert_eq!(
            artifact_download_name("Notes", "findings", "a3"),
            "Notes.md"
        );
        assert_eq!(
            artifact_download_name("Blob", "mystery", "a4"),
            "Blob.txt"
        );
        // Empty / unusable titles fall back to kind + id, never a bare id.
        assert_eq!(artifact_download_name("", "findings", "a5"), "findings-a5.md");
        assert_eq!(artifact_download_name("///", "plan", "a6"), "plan-a6.md");
        assert_eq!(artifact_extension("test_report"), ("md", "text/markdown"));
        assert_eq!(artifact_extension("patch"), ("patch", "text/plain"));
        assert_eq!(artifact_extension("other"), ("txt", "text/plain"));
    }
}
