//! Memory, knowledge, and artifact panels. feature:storage

use std::rc::Rc;

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
                                <button type="button" class="secondary" data-action="delete-memory" on:click=move |_| {
                                    let del2 = del.clone();
                                    if !crate::confirm::ask(
                                        memory_delete_confirm(&del, &scope.get_untracked()),
                                        Rc::new(move || delete_memory(del2.clone())),
                                    ) {
                                        error.set(crate::confirm::BUSY.into());
                                    }
                                }>"Delete"</button>
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
        </div>
    }
}

/// Confirm copy for Memory Delete (#1554). Deleting a memory entry is
/// permanent with no undo or trash, so the shared confirm dialog
/// (`crate::confirm`) gates it and names the entry and its scope.
/// Knowledge Unpin stays single-click in this slice: a pin is a
/// re-creatable path reference, not stored content.
pub fn memory_delete_confirm(key: &str, scope: &str) -> crate::confirm::ConfirmRequest {
    let scope_label = if scope == "global" { "global" } else { "project" };
    crate::confirm::ConfirmRequest {
        title: format!("Delete memory \u{201c}{key}\u{201d}?"),
        body: format!(
            "This permanently removes the {scope_label} memory entry \u{201c}{key}\u{201d}. It cannot be undone; re-creating it means retyping its value and tags."
        ),
        confirm_label: "Delete memory".into(),
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
    fn memory_delete_confirm_names_key_scope_and_permanence() {
        let req = memory_delete_confirm("smoke-pref", "project");
        assert_eq!(req.confirm_label, "Delete memory");
        assert!(req.title.contains("smoke-pref"));
        assert!(req.body.contains("project"));
        assert!(req.body.contains("cannot be undone"));
        let global = memory_delete_confirm("k", "global");
        assert!(global.body.contains("global"));
        // Unknown scopes fall back to the panel's default (project).
        assert!(memory_delete_confirm("k", "").body.contains("project"));
    }
}
