//! Ship to PR (#1340): create or open the current branch's GitHub pull
//! request from the Git tab.
//!
//! `GET /v1/git/pr` previews branch, base, commits, diffstat, uncommitted
//! changes, whether a push is needed, and any existing PR, plus an editable
//! title/body draft. `POST /v1/git/pr` pushes (only with `confirmPush`) and
//! runs the host's `gh pr create`. The daemon refuses detached HEADs, the
//! default branch, non-GitHub remotes, and never force-pushes; this view
//! never handles a token. An existing PR turns the action into "Open pull
//! request" (a link), never a second create.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;

use crate::api::{get_json, post_json_value};

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct ShipCommit {
    #[serde(default)]
    pub hash: String,
    #[serde(default)]
    pub subject: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct ShipExisting {
    #[serde(default)]
    pub url: String,
    #[serde(default)]
    pub number: i64,
    #[serde(default)]
    pub state: String,
    #[serde(default, rename = "isDraft")]
    pub is_draft: bool,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct ShipPreview {
    #[serde(default)]
    pub state: String,
    #[serde(default)]
    pub blockers: Vec<String>,
    #[serde(default)]
    pub warnings: Vec<String>,
    #[serde(default)]
    pub branch: String,
    #[serde(default)]
    pub head: String,
    #[serde(default)]
    pub base: String,
    #[serde(default)]
    pub repo: String,
    #[serde(default)]
    pub commits: Vec<ShipCommit>,
    #[serde(default, rename = "commitsTotal")]
    pub commits_total: i64,
    #[serde(default)]
    pub files: i64,
    #[serde(default)]
    pub added: i64,
    #[serde(default)]
    pub deleted: i64,
    #[serde(default)]
    pub dirty: i64,
    #[serde(default, rename = "needsPush")]
    pub needs_push: bool,
    #[serde(default)]
    pub existing: Option<ShipExisting>,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub body: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct ShipResult {
    #[serde(default)]
    pub url: String,
    #[serde(default)]
    pub number: i64,
    #[serde(default)]
    pub draft: bool,
    #[serde(default)]
    pub existing: bool,
    #[serde(default)]
    pub pushed: bool,
}

/// Header button text: an existing PR is opened, never re-created.
pub fn action_label(p: Option<&ShipPreview>) -> &'static str {
    match p.and_then(|p| p.existing.as_ref()) {
        Some(_) => "Open pull request",
        None => "Create pull request",
    }
}

/// Whether the create button may fire: no blockers, no existing PR, a
/// title, and every required confirmation ticked.
pub fn can_create(p: &ShipPreview, title: &str, push_ok: bool, dirty_ok: bool) -> bool {
    p.blockers.is_empty()
        && p.existing.is_none()
        && !title.trim().is_empty()
        && title.trim().chars().count() <= 256
        && (!p.needs_push || push_ok)
        && (p.dirty == 0 || dirty_ok)
}

/// POST body. The confirmations are sent only when actually required and
/// ticked, so the daemon re-checks the same gates. `head` pins the commit
/// the user reviewed: the daemon refuses if the branch moved since.
pub fn create_body(
    p: &ShipPreview,
    title: &str,
    body: &str,
    draft: bool,
    push_ok: bool,
    dirty_ok: bool,
) -> serde_json::Value {
    serde_json::json!({
        "base": p.base,
        "head": p.head,
        "title": title.trim(),
        "body": body,
        "draft": draft,
        "confirmPush": p.needs_push && push_ok,
        "allowDirty": p.dirty > 0 && dirty_ok,
    })
}

pub fn diffstat_line(p: &ShipPreview) -> String {
    format!(
        "{} commit{} · {} file{} · +{} −{}",
        p.commits_total,
        if p.commits_total == 1 { "" } else { "s" },
        p.files,
        if p.files == 1 { "" } else { "s" },
        p.added,
        p.deleted
    )
}

fn daemon_error(err: &str) -> String {
    if let Some(idx) = err.find('{') {
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(&err[idx..]) {
            if let Some(msg) = v.get("error").and_then(|e| e.as_str()) {
                if !msg.is_empty() {
                    return msg.to_string();
                }
            }
        }
    }
    err.to_string()
}

fn short_hash(h: &str) -> String {
    h.chars().take(8).collect()
}

#[component]
pub fn ShipPr(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status_line: RwSignal<String>,
    active: Memo<bool>,
) -> impl IntoView {
    let open = RwSignal::new(false);
    let loading = RwSignal::new(false);
    let preview = RwSignal::new(None::<ShipPreview>);
    let error = RwSignal::new(String::new());
    let title = RwSignal::new(String::new());
    let body = RwSignal::new(String::new());
    let draft = RwSignal::new(true);
    let push_ok = RwSignal::new(false);
    let dirty_ok = RwSignal::new(false);
    let busy = RwSignal::new(false);
    let created = RwSignal::new(None::<ShipResult>);

    let load = move || {
        loading.set(true);
        error.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match get_json::<ShipPreview>(&b, &t, "/v1/git/pr").await {
                Ok(p) => {
                    title.set(p.title.clone());
                    body.set(p.body.clone());
                    push_ok.set(false);
                    dirty_ok.set(false);
                    preview.set(Some(p));
                }
                Err(e) => {
                    preview.set(None);
                    error.set(format!("Could not load the pull request preview: {}", daemon_error(&e)));
                }
            }
            loading.set(false);
        });
    };

    // Leaving the Git tab closes the panel; nothing is kept half-confirmed.
    Effect::new(move |_| {
        if !active.get() {
            open.set(false);
            push_ok.set(false);
            dirty_ok.set(false);
        }
    });

    let toggle = move |_| {
        if open.get_untracked() {
            open.set(false);
            return;
        }
        open.set(true);
        created.set(None);
        load();
    };

    let submit = move || {
        let Some(p) = preview.get_untracked() else {
            return;
        };
        let (t_title, t_body) = (title.get_untracked(), body.get_untracked());
        if busy.get_untracked() || !can_create(&p, &t_title, push_ok.get_untracked(), dirty_ok.get_untracked()) {
            return;
        }
        busy.set(true);
        error.set(String::new());
        let payload = create_body(&p, &t_title, &t_body, draft.get_untracked(), push_ok.get_untracked(), dirty_ok.get_untracked());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match post_json_value(&b, &t, "/v1/git/pr", payload).await {
                Ok(v) => match serde_json::from_value::<ShipResult>(v) {
                    Ok(r) => {
                        status_line.set(if r.existing {
                            format!("Pull request #{} already exists", r.number)
                        } else {
                            format!("Created pull request #{}", r.number)
                        });
                        created.set(Some(r));
                        push_ok.set(false);
                        dirty_ok.set(false);
                    }
                    Err(e) => error.set(format!("Unexpected daemon reply: {e}")),
                },
                Err(e) => error.set(daemon_error(&e)),
            }
            busy.set(false);
        });
    };

    view! {
        <section class="ship-pr" data-ship-pr="" aria-label="Ship to pull request">
            <div class="ship-pr-head">
                <button
                    type="button"
                    class="secondary"
                    data-ship-pr-open=""
                    aria-expanded=move || open.get().to_string()
                    on:click=toggle
                >{move || action_label(preview.get().as_ref())}</button>
            </div>
            <Show when=move || open.get()>
                <div class="ship-pr-panel" data-ship-pr-panel=""
                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                        if ev.key() == "Escape" {
                            ev.stop_propagation();
                            open.set(false);
                        }
                    }
                >
                    {move || (!error.get().is_empty()).then(|| view! {
                        <p class="ship-pr-error" role="alert" data-ship-pr-error="">{error.get()}</p>
                    })}
                    {move || loading.get().then(|| view! { <p class="ship-pr-meta" role="status">"Loading…"</p> })}
                    {move || created.get().map(|r| {
                        let label = if r.existing {
                            format!("Pull request #{} already exists", r.number)
                        } else if r.draft {
                            format!("Created draft pull request #{}", r.number)
                        } else {
                            format!("Created pull request #{}", r.number)
                        };
                        view! {
                            <p class="ship-pr-done" role="status" data-ship-pr-created="">
                                {label}" · "
                                <a href=r.url.clone() target="_blank" rel="noopener noreferrer">"Open on GitHub"</a>
                            </p>
                        }
                    })}
                    {move || preview.get().filter(|_| created.get().is_none()).map(|p| {
                        let blockers = p.blockers.clone();
                        let warnings = p.warnings.clone();
                        let commits: Vec<ShipCommit> = p.commits.iter().take(10).cloned().collect();
                        let more = (p.commits_total as usize).saturating_sub(commits.len());
                        let needs_push = p.needs_push;
                        let dirty = p.dirty;
                        let branch = p.branch.clone();
                        let stat = diffstat_line(&p);
                        let heading = if p.repo.is_empty() {
                            format!("{} → {}", p.branch, p.base)
                        } else {
                            format!("{} → {} on {}", p.branch, p.base, p.repo)
                        };
                        let existing = p.existing.clone();
                        let gate = p.clone();
                        view! {
                            <div class="ship-pr-summary">
                                <p class="ship-pr-branch" data-ship-pr-branch="">{heading}</p>
                                <p class="ship-pr-meta" data-ship-pr-stat="">{stat}</p>
                                <ul class="ship-pr-commits">
                                    {commits.into_iter().map(|c| view! {
                                        <li><code>{short_hash(&c.hash)}</code>" "{c.subject}</li>
                                    }).collect_view()}
                                    {(more > 0).then(|| view! { <li class="ship-pr-meta">{format!("… and {more} more")}</li> })}
                                </ul>
                            </div>
                            {(!blockers.is_empty()).then(|| view! {
                                <ul class="ship-pr-blockers" data-ship-pr-blockers="">
                                    {blockers.iter().map(|b| view! { <li>{b.clone()}</li> }).collect_view()}
                                </ul>
                            })}
                            {(!warnings.is_empty()).then(|| view! {
                                <ul class="ship-pr-warnings" data-ship-pr-warnings="">
                                    {warnings.iter().map(|w| view! { <li>{w.clone()}</li> }).collect_view()}
                                </ul>
                            })}
                            {match existing {
                                Some(ex) => view! {
                                    <p class="ship-pr-done" data-ship-pr-existing="">
                                        {format!("Pull request #{} is already open{}.", ex.number, if ex.is_draft { " (draft)" } else { "" })}" "
                                        <a class="primary" href=ex.url.clone() target="_blank" rel="noopener noreferrer">"Open pull request"</a>
                                    </p>
                                }.into_any(),
                                None => {
                                    let gate = gate.clone();
                                    view! {
                                        <form class="ship-pr-form" data-ship-pr-form=""
                                            on:submit=move |ev: web_sys::SubmitEvent| {
                                                ev.prevent_default();
                                                submit();
                                            }
                                        >
                                            <label>"Title"
                                                <input type="text" maxlength="256" data-ship-pr-title=""
                                                    prop:value=move || title.get()
                                                    on:input=move |ev| title.set(event_target_value(&ev))
                                                />
                                            </label>
                                            <label>"Description"
                                                <textarea rows="8" data-ship-pr-body=""
                                                    prop:value=move || body.get()
                                                    on:input=move |ev| body.set(event_target_value(&ev))
                                                ></textarea>
                                            </label>
                                            <label class="ship-pr-check">
                                                <input type="checkbox" data-ship-pr-draft=""
                                                    prop:checked=move || draft.get()
                                                    on:change=move |ev| draft.set(event_target_checked(&ev))
                                                />
                                                "Open as draft"
                                            </label>
                                            {needs_push.then(|| view! {
                                                <label class="ship-pr-check">
                                                    <input type="checkbox" data-ship-pr-confirm-push=""
                                                        prop:checked=move || push_ok.get()
                                                        on:change=move |ev| push_ok.set(event_target_checked(&ev))
                                                    />
                                                    {format!("Push {branch} to origin (no force-push)")}
                                                </label>
                                            })}
                                            {(dirty > 0).then(|| view! {
                                                <label class="ship-pr-check">
                                                    <input type="checkbox" data-ship-pr-allow-dirty=""
                                                        prop:checked=move || dirty_ok.get()
                                                        on:change=move |ev| dirty_ok.set(event_target_checked(&ev))
                                                    />
                                                    {format!("Ship without the {dirty} uncommitted change(s)")}
                                                </label>
                                            })}
                                            <div class="ship-pr-actions">
                                                <button type="submit" class="primary" data-ship-pr-create=""
                                                    disabled=move || busy.get() || !can_create(&gate, &title.get(), push_ok.get(), dirty_ok.get())
                                                >{move || if busy.get() { "Creating…" } else if needs_push { "Push and create pull request" } else { "Create pull request" }}</button>
                                                <button type="button" class="secondary" on:click=move |_| open.set(false)>"Cancel"</button>
                                            </div>
                                        </form>
                                    }.into_any()
                                }
                            }}
                        }
                    })}
                </div>
            </Show>
        </section>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ready() -> ShipPreview {
        ShipPreview {
            state: "ok".into(),
            branch: "feature/ship".into(),
            base: "main".into(),
            repo: "acme/widgets".into(),
            commits_total: 2,
            files: 1,
            added: 3,
            deleted: 1,
            needs_push: true,
            head: "abc123".into(),
            title: "Add export".into(),
            ..Default::default()
        }
    }

    #[test]
    fn decodes_preview_payload() {
        let raw = r#"{"state":"blocked","blockers":["You are on the base branch (main)."],"warnings":[],"branch":"main","base":"main","defaultBranch":"main","commits":[{"hash":"abcdef1234","subject":"x","author":"a","date":"d"}],"commitsTotal":1,"files":1,"added":2,"deleted":0,"dirty":3,"needsPush":true,"existing":{"url":"https://github.com/a/b/pull/12","number":12,"state":"open","isDraft":true},"title":"t","body":"b"}"#;
        let p: ShipPreview = serde_json::from_str(raw).unwrap();
        assert_eq!(p.blockers.len(), 1);
        assert_eq!(p.dirty, 3);
        assert!(p.needs_push);
        assert_eq!(p.existing.as_ref().unwrap().number, 12);
        assert!(p.existing.as_ref().unwrap().is_draft);
        assert_eq!(p.commits[0].subject, "x");
    }

    #[test]
    fn existing_pr_turns_the_action_into_open() {
        let mut p = ready();
        assert_eq!(action_label(Some(&p)), "Create pull request");
        assert_eq!(action_label(None), "Create pull request");
        p.existing = Some(ShipExisting { number: 12, ..Default::default() });
        assert_eq!(action_label(Some(&p)), "Open pull request");
        assert!(!can_create(&p, "t", true, true), "never create a duplicate");
    }

    #[test]
    fn create_requires_every_confirmation() {
        let p = ready();
        assert!(!can_create(&p, "Add export", false, false), "push must be confirmed");
        assert!(can_create(&p, "Add export", true, false));
        assert!(!can_create(&p, "   ", true, false), "title required");
        assert!(!can_create(&p, &"t".repeat(257), true, false), "title too long");
        let mut dirty = ready();
        dirty.dirty = 2;
        assert!(!can_create(&dirty, "t", true, false), "dirty must be acknowledged");
        assert!(can_create(&dirty, "t", true, true));
        let mut pushed = ready();
        pushed.needs_push = false;
        assert!(can_create(&pushed, "t", false, false), "no push, no push confirmation");
        let mut blocked = ready();
        blocked.blockers = vec!["default branch".into()];
        assert!(!can_create(&blocked, "t", true, true), "blockers always win");
    }

    #[test]
    fn create_body_only_sends_required_confirmations() {
        let p = ready();
        let v = create_body(&p, "  Add export ", "body", true, true, true);
        assert_eq!(v["title"], "Add export");
        assert_eq!(v["base"], "main");
        assert_eq!(v["head"], "abc123", "the reviewed commit is pinned");
        assert_eq!(v["draft"], true);
        assert_eq!(v["confirmPush"], true);
        assert_eq!(v["allowDirty"], false, "clean tree never sends allowDirty");
        let mut pushed = ready();
        pushed.needs_push = false;
        let v = create_body(&pushed, "t", "b", false, true, false);
        assert_eq!(v["confirmPush"], false);
        assert_eq!(v["draft"], false);
        let v = create_body(&p, "t", "b", true, false, false);
        assert_eq!(v["confirmPush"], false, "unticked push is never confirmed");
    }

    #[test]
    fn diffstat_and_hash_formatting() {
        assert_eq!(diffstat_line(&ready()), "2 commits · 1 file · +3 −1");
        assert_eq!(short_hash("abcdef1234567"), "abcdef12");
        assert_eq!(
            daemon_error(r#"HTTP 409 {"error":"pushing x to origin was not confirmed","code":"push-unconfirmed"}"#),
            "pushing x to origin was not confirmed"
        );
    }
}
