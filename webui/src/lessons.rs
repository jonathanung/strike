//! Lessons capture, slice 1 (#1376): "Remember this" on the user's own
//! transcript messages opens a lesson draft, pre-filled from the message,
//! that is stored through the existing `/v1/memory` route (#1271, which
//! redacts on write) only when the user confirms it.
//!
//! Slice 2 (#1874): agent-proposed lessons wait, pending, in the Memory
//! panel (`/v1/memory/proposals`) until the operator approves, edits, or
//! dismisses them; and a quiet session-header marker lists exactly the
//! memory entries the daemon recorded as auto-loaded into this session
//! (`/v1/sessions/{id}/lessons-used`). feature:lessons

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;

use crate::api::{delete_json, get_json, patch_json, post_json_value, put_json};

/// Tag every captured lesson carries, so the Memory panel filter finds it.
pub const LESSON_TAG: &str = "lesson";
/// Author recorded for a human-confirmed lesson (same as the Memory form).
pub const LESSON_AUTHOR: &str = "operator";
const KEY_PREFIX: &str = "lesson-";
const KEY_SLUG_MAX: usize = 40;

/// Whether this session may show capture actions. Incognito and
/// temporary sessions show none (#1376 point 4); an unknown mode shows
/// none rather than guessing.
pub fn capture_allowed(mode_known: bool, mode: &str) -> bool {
    mode_known && (mode == "normal" || mode.is_empty())
}

/// Page-provided gate the transcript rows read through context, so a
/// mode change hides or shows "Remember this" on every row at once.
#[derive(Clone, Copy)]
pub struct CaptureGate(pub Signal<bool>);

/// A stable, readable memory key from the message: `lesson-` plus a slug
/// of its first words. The user can edit it before saving.
pub fn lesson_key(text: &str) -> String {
    let mut slug = String::new();
    let mut dash = false;
    for c in text.chars() {
        if slug.len() >= KEY_SLUG_MAX {
            break;
        }
        if c.is_ascii_alphanumeric() {
            slug.push(c.to_ascii_lowercase());
            dash = false;
        } else if !slug.is_empty() && !dash {
            slug.push('-');
            dash = true;
        }
    }
    let slug = slug.trim_end_matches('-');
    if slug.is_empty() {
        format!("{KEY_PREFIX}note")
    } else {
        format!("{KEY_PREFIX}{slug}")
    }
}

/// Memory scope for the draft's choice. Workspace is the default and maps
/// to the daemon's `project` store; Global is opt-in.
pub fn scope_param(global: bool) -> &'static str {
    if global {
        "global"
    } else {
        "project"
    }
}

/// Validated PUT for a confirmed draft: `(path, body)`.
pub fn save_request(
    key: &str,
    value: &str,
    global: bool,
) -> Result<(String, serde_json::Value), String> {
    let key = key.trim();
    if key.is_empty() {
        return Err("A key is required.".into());
    }
    if value.trim().is_empty() {
        return Err("The lesson text is empty.".into());
    }
    let scope = scope_param(global);
    Ok((
        format!("/v1/memory/{}?scope={scope}", crate::api::js_encode(key)),
        serde_json::json!({
            "value": value.trim(),
            "tags": [LESSON_TAG],
            "author": LESSON_AUTHOR,
            "scope": scope,
        }),
    ))
}

/// Honest copy for a failed save (attach-only, untrusted workspace, or
/// the daemon's own message).
pub fn save_error_text(err: &str) -> String {
    let rest = err.strip_prefix("HTTP ").unwrap_or("");
    let (code, body) = rest.split_once(' ').unwrap_or((rest, ""));
    let msg = serde_json::from_str::<serde_json::Value>(body.trim())
        .ok()
        .and_then(|v| v.get("error").and_then(|e| e.as_str()).map(str::to_string))
        .unwrap_or_else(|| body.trim().to_string());
    match code {
        "501" => "Memory is not available on this gateway, so the lesson was not saved.".into(),
        "403" if msg.contains("read-only") => {
            "This gateway is attach-only (read-only), so the lesson was not saved.".into()
        }
        _ if !msg.is_empty() => format!("Not saved: {msg}"),
        _ => format!("Not saved: {err}"),
    }
}

#[derive(Deserialize, Default)]
struct KeyList {
    #[serde(default)]
    entries: Option<Vec<KeyRow>>,
}

#[derive(Deserialize, Default)]
struct KeyRow {
    #[serde(default)]
    key: String,
}

/// Open draft. `None` = closed.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Draft {
    pub key: String,
    pub value: String,
    pub global: bool,
}

pub fn draft_from_message(text: &str) -> Draft {
    Draft {
        key: lesson_key(text),
        value: text.trim().to_string(),
        global: false,
    }
}

/// The lesson draft dialog. Nothing is stored until Save; saving over an
/// existing key needs a second, explicit confirmation.
#[component]
pub fn LessonDraftDialog(
    draft: RwSignal<Option<Draft>>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    storage_rev: RwSignal<u32>,
) -> impl IntoView {
    let error = RwSignal::new(String::new());
    let saving = RwSignal::new(false);
    // Key the user agreed to overwrite; any key/scope edit clears it.
    let confirm_replace = RwSignal::new(String::new());
    let close = move || {
        draft.set(None);
        error.set(String::new());
        confirm_replace.set(String::new());
    };
    let edit = move |f: &dyn Fn(&mut Draft)| {
        draft.update(|d| {
            if let Some(d) = d.as_mut() {
                f(d);
            }
        });
        confirm_replace.set(String::new());
        error.set(String::new());
    };
    let save = move || {
        if saving.get_untracked() {
            return;
        }
        let Some(d) = draft.get_untracked() else { return };
        let (path, body) = match save_request(&d.key, &d.value, d.global) {
            Ok(req) => req,
            Err(e) => {
                error.set(e);
                return;
            }
        };
        saving.set(true);
        error.set(String::new());
        let key = d.key.trim().to_string();
        let scope = scope_param(d.global);
        let agreed = confirm_replace.get_untracked() == key;
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if !agreed {
                let list = get_json::<KeyList>(&b, &t, &format!("/v1/memory?scope={scope}")).await;
                if let Ok(list) = list {
                    if list.entries.unwrap_or_default().iter().any(|e| e.key == key) {
                        confirm_replace.set(key.clone());
                        error.set(format!(
                            "{key} already exists in {} memory. Save again to replace it.",
                            if scope == "global" { "Global" } else { "Workspace" }
                        ));
                        saving.set(false);
                        return;
                    }
                }
            }
            match put_json(&b, &t, &path, body).await {
                Ok(_) => {
                    status.set(format!("Saved lesson {key}"));
                    storage_rev.update(|r| *r = r.wrapping_add(1));
                    draft.set(None);
                    confirm_replace.set(String::new());
                }
                Err(e) => error.set(save_error_text(&e)),
            }
            saving.set(false);
        });
    };
    view! {
        <Show when=move || draft.get().is_some()>
            <div class="confirm-backdrop" data-lesson="backdrop" on:click=move |_| close()>
                <form
                    class="confirm-dialog lesson-draft"
                    role="dialog"
                    aria-modal="true"
                    aria-labelledby="lesson-draft-title"
                    data-lesson="draft"
                    on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()
                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                        if ev.key() == "Escape" {
                            ev.stop_propagation();
                            close();
                        }
                    }
                    on:submit=move |ev| { ev.prevent_default(); save(); }
                >
                    <h2 id="lesson-draft-title">"Remember this"</h2>
                    <p class="hint">"Edit the lesson, then save it to memory. Nothing is stored until you save. Saved lessons load into future sessions."</p>
                    <label class="lesson-field">"Lesson"
                        <textarea rows="4" data-lesson="value"
                            prop:value=move || draft.get().map(|d| d.value).unwrap_or_default()
                            on:input=move |ev| { let v = event_target_value(&ev); edit(&|d| d.value = v.clone()); }></textarea>
                    </label>
                    <label class="lesson-field">"Key"
                        <input type="text" data-lesson="key"
                            prop:value=move || draft.get().map(|d| d.key).unwrap_or_default()
                            on:input=move |ev| { let v = event_target_value(&ev); edit(&|d| d.key = v.clone()); } />
                    </label>
                    <div class="chips" role="radiogroup" aria-label="Lesson scope">
                        <button type="button" data-lesson-scope="project"
                            aria-pressed=move || (!draft.get().map(|d| d.global).unwrap_or(false)).to_string()
                            class=move || if draft.get().map(|d| d.global).unwrap_or(false) { "chip" } else { "chip on" }
                            on:click=move |_| edit(&|d| d.global = false)>"This workspace"</button>
                        <button type="button" data-lesson-scope="global"
                            aria-pressed=move || draft.get().map(|d| d.global).unwrap_or(false).to_string()
                            class=move || if draft.get().map(|d| d.global).unwrap_or(false) { "chip on" } else { "chip" }
                            on:click=move |_| edit(&|d| d.global = true)>"Global"</button>
                    </div>
                    <Show when=move || !error.get().is_empty()>
                        <p class="hint storage-error" role="alert" data-lesson="error">{move || error.get()}</p>
                    </Show>
                    <div class="confirm-actions">
                        <button type="button" class="secondary" data-lesson="cancel" on:click=move |_| close()>"Cancel"</button>
                        <button type="submit" data-lesson="save" disabled=move || saving.get()>
                            {move || if saving.get() {
                                "Saving\u{2026}"
                            } else if !confirm_replace.get().is_empty() {
                                "Replace lesson"
                            } else {
                                "Save lesson"
                            }}
                        </button>
                    </div>
                </form>
            </div>
        </Show>
    }
}

/// One pending proposal from `GET /v1/memory/proposals`.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Proposal {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub key: String,
    #[serde(default)]
    pub value: String,
    #[serde(default)]
    pub scope: String,
    #[serde(default)]
    pub session: String,
}

#[derive(Deserialize, Default)]
struct ProposalList {
    #[serde(default)]
    proposals: Option<Vec<Proposal>>,
}

/// Route for one proposal; `action` is "" (edit/dismiss) or "approve".
pub fn proposal_path(id: &str, action: &str) -> String {
    let base = format!("/v1/memory/proposals/{}", crate::api::js_encode(id));
    if action.is_empty() {
        base
    } else {
        format!("{base}/{action}")
    }
}

/// Validated PATCH body for an edited proposal.
pub fn edit_body(key: &str, value: &str, global: bool) -> Result<serde_json::Value, String> {
    if key.trim().is_empty() {
        return Err("A key is required.".into());
    }
    if value.trim().is_empty() {
        return Err("The lesson text is empty.".into());
    }
    Ok(serde_json::json!({
        "key": key.trim(),
        "value": value.trim(),
        "scope": scope_param(global),
    }))
}

/// Copy for a failed approve/edit/dismiss. A 409 means the key is taken:
/// a second Approve replaces it.
pub fn action_error_text(err: &str) -> String {
    if err.starts_with("HTTP 409") {
        return "A memory entry with this key already exists. Approve again to replace it, or Edit the key.".into();
    }
    if err.starts_with("HTTP 404") {
        return "That proposal is no longer pending.".into();
    }
    save_error_text(err)
}

fn scope_label(scope: &str) -> &'static str {
    if scope == "global" {
        "Global"
    } else {
        "Workspace"
    }
}

/// Pending lesson proposals in the Memory panel: visually distinct from
/// stored entries, each with Approve / Edit / Dismiss. Nothing here is in
/// any prompt until approved.
#[component]
pub fn PendingLessons(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    storage_rev: RwSignal<u32>,
) -> impl IntoView {
    let rows = RwSignal::new(Vec::<Proposal>::new());
    let error = RwSignal::new(String::new());
    let busy = RwSignal::new(String::new());
    let confirm_replace = RwSignal::new(String::new());
    // Edit state is split so typing never re-renders the row.
    let editing_id = RwSignal::new(String::new());
    let edit_draft = RwSignal::new(Draft::default());

    let load = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match get_json::<ProposalList>(&b, &t, "/v1/memory/proposals").await {
                Ok(list) => {
                    rows.set(list.proposals.unwrap_or_default());
                }
                Err(e) if e.starts_with("HTTP 501") => rows.set(Vec::new()),
                Err(e) => error.set(format!("Could not load pending lessons: {e}")),
            }
        });
    };
    Effect::new(move |_| {
        let _ = storage_rev.get();
        load();
    });
    let changed = move |msg: String| {
        status.set(msg);
        error.set(String::new());
        storage_rev.update(|r| *r = r.wrapping_add(1));
    };
    let approve = move |id: String| {
        if !busy.get_untracked().is_empty() {
            return;
        }
        let replace = confirm_replace.get_untracked() == id;
        busy.set(id.clone());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let body = serde_json::json!({ "replace": replace });
            match post_json_value(&b, &t, &proposal_path(&id, "approve"), body).await {
                Ok(_) => {
                    confirm_replace.set(String::new());
                    changed("Lesson approved".into());
                }
                Err(e) => {
                    if e.starts_with("HTTP 409") {
                        confirm_replace.set(id.clone());
                    }
                    error.set(action_error_text(&e));
                }
            }
            busy.set(String::new());
        });
    };
    let dismiss = move |id: String| {
        if !busy.get_untracked().is_empty() {
            return;
        }
        busy.set(id.clone());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match delete_json(&b, &t, &proposal_path(&id, ""), None).await {
                Ok(()) => changed("Lesson proposal dismissed".into()),
                Err(e) => error.set(action_error_text(&e)),
            }
            busy.set(String::new());
        });
    };
    let save_edit = move |id: String| {
        if !busy.get_untracked().is_empty() {
            return;
        }
        let d = edit_draft.get_untracked();
        let body = match edit_body(&d.key, &d.value, d.global) {
            Ok(body) => body,
            Err(e) => {
                error.set(e);
                return;
            }
        };
        busy.set(id.clone());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match patch_json(&b, &t, &proposal_path(&id, ""), body).await {
                Ok(()) => {
                    editing_id.set(String::new());
                    confirm_replace.set(String::new());
                    changed("Lesson proposal updated".into());
                }
                Err(e) => error.set(action_error_text(&e)),
            }
            busy.set(String::new());
        });
    };

    view! {
        <Show when=move || !rows.get().is_empty() || !error.get().is_empty()>
            <section class="lesson-pending" data-lesson-pending="section" aria-label="Pending lessons">
                <h3>{move || format!("Pending lessons ({})", rows.get().len())}</h3>
                <p class="hint">"Proposed by a session. None of these load into a prompt until you approve them."</p>
                <Show when=move || !error.get().is_empty()>
                    <p class="hint storage-error" role="alert" data-lesson-pending="error">{move || error.get()}</p>
                </Show>
                <ul class="lesson-pending-list">
                    {move || rows.get().into_iter().map(|p| {
                        let id = p.id.clone();
                        view! {
                            <li class="lesson-pending-row" data-proposal-id=id.clone()>
                                {move || {
                                    let p = p.clone();
                                    if editing_id.get() == p.id {
                                        let id_save = p.id.clone();
                                        view! {
                                            <div class="lesson-pending-edit">
                                                <label class="lesson-field">"Lesson"
                                                    <textarea rows="3" data-lesson-pending="value"
                                                        prop:value=move || edit_draft.get().value
                                                        on:input=move |ev| {
                                                            let v = event_target_value(&ev);
                                                            edit_draft.update(|d| d.value = v);
                                                        }></textarea>
                                                </label>
                                                <label class="lesson-field">"Key"
                                                    <input type="text" data-lesson-pending="key"
                                                        prop:value=move || edit_draft.get().key
                                                        on:input=move |ev| {
                                                            let v = event_target_value(&ev);
                                                            edit_draft.update(|d| d.key = v);
                                                        } />
                                                </label>
                                                <div class="chips" role="radiogroup" aria-label="Lesson scope">
                                                    <button type="button"
                                                        aria-pressed=move || (!edit_draft.get().global).to_string()
                                                        class=move || if edit_draft.get().global { "chip" } else { "chip on" }
                                                        on:click=move |_| edit_draft.update(|d| d.global = false)>"This workspace"</button>
                                                    <button type="button"
                                                        aria-pressed=move || edit_draft.get().global.to_string()
                                                        class=move || if edit_draft.get().global { "chip on" } else { "chip" }
                                                        on:click=move |_| edit_draft.update(|d| d.global = true)>"Global"</button>
                                                </div>
                                                <div class="confirm-actions">
                                                    <button type="button" class="secondary" data-lesson-pending="cancel-edit"
                                                        on:click=move |_| editing_id.set(String::new())>"Cancel"</button>
                                                    <button type="button" data-lesson-pending="save"
                                                        disabled=move || !busy.get().is_empty()
                                                        on:click=move |_| save_edit(id_save.clone())>"Save"</button>
                                                </div>
                                            </div>
                                        }.into_any()
                                    } else {
                                        let (id_a, id_e, id_d) = (p.id.clone(), p.id.clone(), p.id.clone());
                                        let id_r = p.id.clone();
                                        let draft = Draft {
                                            key: p.key.clone(),
                                            value: p.value.clone(),
                                            global: p.scope == "global",
                                        };
                                        let meta = if p.session.is_empty() {
                                            scope_label(&p.scope).to_string()
                                        } else {
                                            format!("{} \u{b7} from {}", scope_label(&p.scope), p.session.chars().take(8).collect::<String>())
                                        };
                                        view! {
                                            <div class="lesson-pending-view">
                                            <div class="lesson-pending-head">
                                                <span class="lesson-pending-key">{p.key.clone()}</span>
                                                <span class="hint">{meta}</span>
                                            </div>
                                            <p class="lesson-pending-text">{p.value.clone()}</p>
                                            <div class="lesson-pending-actions">
                                                <button type="button" data-lesson-pending="approve"
                                                    disabled=move || !busy.get().is_empty()
                                                    on:click=move |_| approve(id_a.clone())>
                                                    {move || if confirm_replace.get() == id_r { "Approve and replace" } else { "Approve" }}
                                                </button>
                                                <button type="button" class="secondary" data-lesson-pending="edit"
                                                    on:click=move |_| {
                                                        edit_draft.set(draft.clone());
                                                        error.set(String::new());
                                                        editing_id.set(id_e.clone());
                                                    }>"Edit"</button>
                                                <button type="button" class="secondary" data-lesson-pending="dismiss"
                                                    disabled=move || !busy.get().is_empty()
                                                    on:click=move |_| dismiss(id_d.clone())>"Dismiss"</button>
                                            </div>
                                            </div>
                                        }.into_any()
                                    }
                                }}
                            </li>
                        }
                    }).collect_view()}
                </ul>
            </section>
        </Show>
    }
}

/// One entry from `GET /v1/sessions/{id}/lessons-used`.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct UsedEntry {
    #[serde(default)]
    pub scope: String,
    #[serde(default)]
    pub key: String,
    #[serde(default)]
    pub value: String,
}

#[derive(Deserialize, Default)]
struct UsedList {
    #[serde(default)]
    entries: Option<Vec<UsedEntry>>,
}

/// Marker copy: "1 lesson used" / "3 lessons used".
pub fn used_label(n: usize) -> String {
    if n == 1 {
        "1 lesson used".into()
    } else {
        format!("{n} lessons used")
    }
}

/// Quiet session-header marker listing exactly the memory entries the
/// daemon recorded as auto-loaded into this session. Titles first; each
/// expands to its full text. Display-only; hidden when nothing was used.
#[component]
pub fn LessonsUsedMarker(
    base: RwSignal<String>,
    token: RwSignal<String>,
    session: RwSignal<String>,
    busy: RwSignal<bool>,
    storage_rev: RwSignal<u32>,
) -> impl IntoView {
    let used = RwSignal::new(Vec::<UsedEntry>::new());
    let epoch = RwSignal::new(0u32);
    // Refetch on session switch, at each turn edge (the record is written
    // while a turn's prompt is built), and after memory changes.
    Effect::new(move |_| {
        let sid = session.get();
        let _ = (busy.get(), storage_rev.get());
        let gen = epoch.get_untracked().wrapping_add(1);
        epoch.set(gen);
        if sid.is_empty() {
            used.set(Vec::new());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/sessions/{}/lessons-used", crate::api::js_encode(&sid));
            let rows = get_json::<UsedList>(&b, &t, &path)
                .await
                .ok()
                .and_then(|l| l.entries)
                .unwrap_or_default();
            if epoch.get_untracked() == gen {
                used.set(rows);
            }
        });
    });
    view! {
        <Show when=move || !used.get().is_empty()>
            <details class="lessons-used" data-lessons-used="marker">
                <summary data-lessons-used="summary">{move || format!("{} \u{2014} view", used_label(used.get().len()))}</summary>
                <ul class="lessons-used-list">
                    {move || used.get().into_iter().map(|e| {
                        view! {
                            <li data-lesson-used-key=e.key.clone()>
                                <details>
                                    <summary>
                                        <span class="lessons-used-key">{e.key.clone()}</span>
                                        " "
                                        <span class="hint">{scope_label(&e.scope)}</span>
                                    </summary>
                                    <p class="lessons-used-text">{e.value.clone()}</p>
                                </details>
                            </li>
                        }
                    }).collect_view()}
                </ul>
            </details>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn capture_only_in_known_normal_mode() {
        assert!(capture_allowed(true, "normal"));
        assert!(capture_allowed(true, ""));
        assert!(!capture_allowed(true, "incognito"));
        assert!(!capture_allowed(true, "temporary"));
        assert!(!capture_allowed(false, "normal"));
    }

    #[test]
    fn key_is_a_readable_slug_with_fallback() {
        assert_eq!(lesson_key("Use `make test-fast` here!"), "lesson-use-make-test-fast-here");
        assert_eq!(lesson_key("   ***   "), "lesson-note");
        let long = lesson_key(&"word ".repeat(40));
        assert!(long.len() <= KEY_PREFIX.len() + KEY_SLUG_MAX, "{long}");
        assert!(!long.ends_with('-'), "{long}");
    }

    #[test]
    fn save_request_defaults_to_workspace_and_tags_the_lesson() {
        let (path, body) = save_request(" lesson-a ", " Run make test-fast ", false).unwrap();
        assert_eq!(path, "/v1/memory/lesson-a?scope=project");
        assert_eq!(
            body,
            serde_json::json!({"value": "Run make test-fast", "tags": ["lesson"], "author": "operator", "scope": "project"})
        );
        let (gpath, gbody) = save_request("k", "v", true).unwrap();
        assert_eq!(gpath, "/v1/memory/k?scope=global");
        assert_eq!(gbody["scope"], "global");
        assert!(save_request(" ", "v", false).is_err());
        assert!(save_request("k", "  ", false).is_err());
    }

    #[test]
    fn save_errors_are_honest() {
        assert_eq!(
            save_error_text(r#"HTTP 403 {"error":"read-only attach mode does not allow mutations"}"#),
            "This gateway is attach-only (read-only), so the lesson was not saved."
        );
        assert!(save_error_text("HTTP 501 x").starts_with("Memory is not available"));
        assert_eq!(save_error_text(r#"HTTP 403 {"error":"workspace is not trusted"}"#), "Not saved: workspace is not trusted");
        assert_eq!(save_error_text("offline"), "Not saved: offline");
    }

    #[test]
    fn draft_prefills_from_the_message() {
        let d = draft_from_message("  remember: prefer rg over grep \n");
        assert_eq!(d.value, "remember: prefer rg over grep");
        assert_eq!(d.key, "lesson-remember-prefer-rg-over-grep");
        assert!(!d.global);
    }

    #[test]
    fn key_list_tolerates_null_entries() {
        let l: KeyList = serde_json::from_str(r#"{"entries":null}"#).unwrap();
        assert!(l.entries.unwrap_or_default().is_empty());
    }
    #[test]
    fn proposal_routes_and_edit_body() {
        assert_eq!(proposal_path("prop-1", ""), "/v1/memory/proposals/prop-1");
        assert_eq!(proposal_path("prop-1", "approve"), "/v1/memory/proposals/prop-1/approve");
        assert_eq!(
            edit_body(" k ", " v ", true).unwrap(),
            serde_json::json!({"key": "k", "value": "v", "scope": "global"})
        );
        assert_eq!(edit_body("k", "v", false).unwrap()["scope"], "project");
        assert!(edit_body(" ", "v", false).is_err());
        assert!(edit_body("k", " ", false).is_err());
    }

    #[test]
    fn action_errors_explain_conflict_and_gone() {
        assert!(action_error_text(r#"HTTP 409 {"error":"exists"}"#).contains("Approve again to replace"));
        assert_eq!(action_error_text("HTTP 404 {}"), "That proposal is no longer pending.");
        assert_eq!(action_error_text(r#"HTTP 400 {"error":"bad scope"}"#), "Not saved: bad scope");
    }

    #[test]
    fn used_label_counts() {
        assert_eq!(used_label(1), "1 lesson used");
        assert_eq!(used_label(2), "2 lessons used");
    }

    #[test]
    fn lists_tolerate_null() {
        let p: ProposalList = serde_json::from_str(r#"{"proposals":null}"#).unwrap();
        assert!(p.proposals.unwrap_or_default().is_empty());
        let u: UsedList = serde_json::from_str(r#"{"sessionId":"s","entries":[{"scope":"global","key":"k","value":"v","injectedAt":"x"}]}"#).unwrap();
        assert_eq!(u.entries.unwrap()[0], UsedEntry { scope: "global".into(), key: "k".into(), value: "v".into() });
        assert_eq!(scope_label("global"), "Global");
        assert_eq!(scope_label("project"), "Workspace");
    }
}
