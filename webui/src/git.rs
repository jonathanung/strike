//! Git status, diff, commit, and worktree panels.
//! Unstaged diffs come from GET /v1/changed-files (the Changes tab payload).
//! Mutations go through /v1/git, which refuses force-push and requires a
//! confirm token for discard, hard reset, branch delete, and worktree removal.

use std::cell::RefCell;
use std::collections::HashSet;
use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use wasm_bindgen::JsCast;

use load_guard::LoadEpoch;

use crate::load_epoch::begin;

use crate::api::{get_json, post_json_value};
use crate::model::{ChangedFilesResponse, ChildSession, ChildrenResponse};
use crate::outcome;

thread_local! {
    static BRANCHES: RefCell<Vec<(String, bool)>> = RefCell::new(Vec::new());
}

pub fn branch_names() -> Vec<String> {
    BRANCHES.with(|slot| slot.borrow().iter().map(|(name, _)| name.clone()).collect())
}

pub fn deletable_branches() -> Vec<String> {
    BRANCHES.with(|slot| {
        slot.borrow()
            .iter()
            .filter(|(_, current)| !current)
            .map(|(name, _)| name.clone())
            .collect()
    })
}

#[derive(Clone, Debug, Deserialize)]
struct GitFile {
    path: String,
    #[serde(default)]
    status: String,
    #[serde(default)]
    added: i64,
    #[serde(default)]
    deleted: i64,
}

#[derive(Clone, Debug, Deserialize)]
struct GitStatus {
    #[serde(default)]
    branch: String,
    #[serde(default)]
    detached: bool,
    #[serde(default)]
    ahead: i64,
    #[serde(default)]
    behind: i64,
    #[serde(default)]
    staged: Vec<GitFile>,
    #[serde(default)]
    unstaged: Vec<GitFile>,
    #[serde(default)]
    untracked: Vec<GitFile>,
}

#[derive(Clone, Debug, Deserialize)]
struct GitDiff {
    #[serde(default)]
    diff: String,
    #[serde(default)]
    truncated: bool,
}

#[derive(Clone, Debug, Deserialize)]
struct GitBranch {
    name: String,
    #[serde(default)]
    current: bool,
}

#[derive(Clone, Debug, Deserialize)]
struct GitBranches {
    #[serde(default)]
    branches: Vec<GitBranch>,
}

#[derive(Clone, Debug, Deserialize)]
struct GitCommit {
    hash: String,
    #[serde(default)]
    subject: String,
    #[serde(default)]
    author: String,
    #[serde(default)]
    date: String,
}

#[derive(Clone, Debug, Deserialize)]
struct GitLog {
    #[serde(default)]
    commits: Vec<GitCommit>,
}

#[derive(Clone, Debug, Deserialize)]
struct WorktreeInfo {
    path: String,
    #[serde(default)]
    branch: String,
    #[serde(default)]
    head: String,
    #[serde(default, rename = "sessionId")]
    session_id: String,
    #[serde(default)]
    owner: String,
    #[serde(default, rename = "ownerTitle")]
    owner_title: String,
    #[serde(default)]
    stale: bool,
    #[serde(default)]
    primary: bool,
    #[serde(default, rename = "strikeManaged")]
    strike_managed: bool,
}

#[derive(Clone, Debug, Deserialize)]
struct WorktreesBody {
    #[serde(default, rename = "childIsolation")]
    child_isolation: String,
    #[serde(default, rename = "isolatesChildren")]
    isolates_children: bool,
    #[serde(default, rename = "worktreeCleanup")]
    worktree_cleanup: String,
    #[serde(default, rename = "sessionWorktree")]
    session_worktree: String,
    #[serde(default)]
    worktrees: Vec<WorktreeInfo>,
}

#[derive(Clone, Debug)]
struct GitRow {
    section: String,
    path: String,
    status: String,
    added: i64,
    deleted: i64,
    staged: bool,
    inline_diff: String,
}

fn focus_id(id: &str) {
    let Some(el) = web_sys::window()
        .and_then(|w| w.document())
        .and_then(|d| d.get_element_by_id(id))
    else {
        return;
    };
    if let Some(html) = el.dyn_ref::<web_sys::HtmlElement>() {
        let _ = html.focus();
    }
}

pub(crate) fn shortcut_blocked(ev: &web_sys::KeyboardEvent) -> bool {
    let Some(el) = ev
        .target()
        .and_then(|t| t.dyn_into::<web_sys::Element>().ok())
    else {
        return false;
    };
    matches!(
        el.tag_name().to_ascii_lowercase().as_str(),
        "input" | "textarea" | "select"
    )
}

fn diff_kind(line: &str) -> &'static str {
    if line.starts_with('+') && !line.starts_with("+++") {
        "add"
    } else if line.starts_with('-') && !line.starts_with("---") {
        "del"
    } else {
        "ctx"
    }
}

fn short_hash(hash: &str) -> String {
    hash.chars().take(7).collect()
}

fn short_path(path: &str) -> String {
    if let Some(i) = path.find(".strike/worktrees/") {
        return path[i..].to_string();
    }
    path.rsplit('/').next().unwrap_or(path).to_string()
}

fn owner_label(wt: &WorktreeInfo, kids: &[ChildSession], root: &str) -> String {
    if let Some(child) = kids.iter().find(|c| c.id == wt.session_id) {
        if child.title.is_empty() {
            return "Child of this session".into();
        }
        return format!("Child of this session · {}", child.title);
    }
    match wt.owner.as_str() {
        "primary" => "Primary checkout".into(),
        "child" if !wt.owner_title.is_empty() => format!("Child · {}", wt.owner_title),
        "child" => "Child session".into(),
        "session" if wt.session_id == root => "This session".into(),
        "session" if !wt.owner_title.is_empty() => format!("Session · {}", wt.owner_title),
        "session" => "Session".into(),
        "stale" => "Stale · no session owns this".into(),
        _ => "Other worktree".into(),
    }
}

#[component]
pub fn GitPanel(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status_line: RwSignal<String>,
    active: Memo<bool>,
) -> impl IntoView {
    let error = RwSignal::new(String::new());
    let branch = RwSignal::new(String::new());
    let ahead = RwSignal::new(0i64);
    let behind = RwSignal::new(0i64);
    let detached = RwSignal::new(false);
    let rows = RwSignal::new(Vec::<GitRow>::new());
    let branches = RwSignal::new(Vec::<GitBranch>::new());
    let commits = RwSignal::new(Vec::<GitCommit>::new());
    let cursor = RwSignal::new(0usize);
    let open_diff = RwSignal::new(false);
    let diff_text = RwSignal::new(String::new());
    let diff_note = RwSignal::new(String::new());
    let diff_req = RwSignal::new(0u32);
    // #1428: the diff render is capped at 400 lines by default (wasm DOM
    // cost); diff_expanded lifts the cap for the currently open diff only.
    // It resets whenever a diff is (re)loaded — see the diff effect below.
    let diff_expanded = RwSignal::new(false);
    let show_log = RwSignal::new(false);
    let message = RwSignal::new(String::new());
    let switch_to = RwSignal::new(String::new());
    let load_epoch = RwSignal::new(LoadEpoch::new());


    let load = move || {
        let gen = begin(load_epoch);
        // Rows are what Stage / Discard act on. Drop them before the new
        // status arrives so the previous branch's files are not clickable
        // under a checkout that has already moved on.
        rows.set(Vec::new());
        commits.set(Vec::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let load_epoch = load_epoch;
        spawn_local(async move {
            let live = move || load_epoch.with_untracked(|epoch| epoch.accept(gen));
            let mut err = String::new();
            match get_json::<GitStatus>(&b, &t, "/v1/git/status").await {
                Ok(st) => {
                    if !live() {
                        return;
                    }
                    branch.set(st.branch);
                    ahead.set(st.ahead);
                    behind.set(st.behind);
                    detached.set(st.detached);
                    let changes = outcome::take_ok(
                        &mut err,
                        "Changed files",
                        get_json::<ChangedFilesResponse>(&b, &t, "/v1/changed-files").await,
                    )
                    .unwrap_or(ChangedFilesResponse { files: vec![] });
                    if !live() {
                        return;
                    }
                    let mut seen = HashSet::new();
                    let mut list = Vec::new();
                    for file in st.staged {
                        list.push(GitRow {
                            section: "Staged".into(),
                            path: file.path,
                            status: file.status,
                            added: file.added,
                            deleted: file.deleted,
                            staged: true,
                            inline_diff: String::new(),
                        });
                    }
                    for file in st.unstaged {
                        seen.insert(file.path.clone());
                        let hit = changes.files.iter().find(|c| c.path == file.path);
                        list.push(GitRow {
                            section: "Unstaged".into(),
                            added: hit
                                .map(|c| c.added)
                                .filter(|n| *n != 0)
                                .unwrap_or(file.added),
                            deleted: hit
                                .map(|c| c.deleted)
                                .filter(|n| *n != 0)
                                .unwrap_or(file.deleted),
                            inline_diff: hit.map(|c| c.diff.clone()).unwrap_or_default(),
                            path: file.path,
                            status: file.status,
                            staged: false,
                        });
                    }
                    for file in &changes.files {
                        if seen.contains(&file.path) {
                            continue;
                        }
                        list.push(GitRow {
                            section: "Unstaged".into(),
                            path: file.path.clone(),
                            status: "M".into(),
                            added: file.added,
                            deleted: file.deleted,
                            staged: false,
                            inline_diff: file.diff.clone(),
                        });
                    }
                    for file in st.untracked {
                        list.push(GitRow {
                            section: "Untracked".into(),
                            path: file.path,
                            status: "?".into(),
                            added: file.added,
                            deleted: 0,
                            staged: false,
                            inline_diff: String::new(),
                        });
                    }
                    let n = list.len();
                    rows.set(list);
                    if cursor.get_untracked() >= n {
                        cursor.set(0);
                    }
                }
                Err(e) => {
                    if !live() {
                        return;
                    }
                    err = e;
                }
            }
            if !live() {
                return;
            }
            if !live() {
                return;
            }
            if let Some(body) = outcome::take_ok(
                &mut err,
                "Branches",
                get_json::<GitBranches>(&b, &t, "/v1/git/branches").await,
            ) {
                if switch_to.get_untracked().is_empty() {
                    if let Some(cur) = body.branches.iter().find(|b| b.current) {
                        switch_to.set(cur.name.clone());
                    }
                }
                branches.set(body.branches);
            }
            if !live() {
                return;
            }
            if let Some(body) = outcome::take_ok(
                &mut err,
                "Log",
                get_json::<GitLog>(&b, &t, "/v1/git/log?limit=30").await,
            ) {
                commits.set(body.commits);
            }
            if live() {
                error.set(err);
            }
        });
    };

    Effect::new(move |_| {
        let _ = (base.get(), token.get());
        load();
        if active.get() {
            focus_id("git-panel");
        }
    });
    Effect::new(move |_| {
        let rows: Vec<(String, bool)> = branches
            .get()
            .into_iter()
            .map(|branch| (branch.name, branch.current))
            .collect();
        BRANCHES.with(|slot| *slot.borrow_mut() = rows);
    });

    Effect::new(move |_| {
        let _ = diff_req.get();
        if !open_diff.get_untracked() {
            return;
        }
        // Per-diff expansion (#1428): every (re)load starts capped again.
        diff_expanded.set(false);
        let rows = rows.get_untracked();
        let Some(row) = rows.get(cursor.get_untracked()).cloned() else {
            diff_text.set(String::new());
            return;
        };
        if !row.inline_diff.is_empty() {
            diff_text.set(row.inline_diff);
            diff_note.set(String::new());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let staged = if row.staged { "1" } else { "0" };
        let path = format!(
            "/v1/git/diff?path={}&staged={staged}",
            crate::api::js_encode(&row.path)
        );
        spawn_local(async move {
            match get_json::<GitDiff>(&b, &t, &path).await {
                Ok(body) => {
                    diff_note.set(if body.truncated {
                        "Diff truncated.".into()
                    } else {
                        String::new()
                    });
                    diff_text.set(if body.diff.is_empty() {
                        "No diff.".into()
                    } else {
                        body.diff
                    });
                }
                Err(e) => {
                    diff_text.set(String::new());
                    diff_note.set(e);
                }
            }
        });
    });

    let move_cursor = move |delta: i32| {
        let n = rows.get_untracked().len() as i32;
        if n == 0 {
            return;
        }
        let next = (cursor.get_untracked() as i32 + delta).clamp(0, n - 1) as usize;
        cursor.set(next);
        open_diff.set(false);
        diff_expanded.set(false);
        diff_text.set(String::new());
    };

    let open_selected = move || {
        if rows.get_untracked().is_empty() {
            return;
        }
        open_diff.set(true);
        diff_req.update(|n| *n = n.saturating_add(1));
    };

    let on_key = move |ev: web_sys::KeyboardEvent| {
        if shortcut_blocked(&ev) {
            return;
        }
        match ev.key().as_str() {
            "ArrowDown" | "j" => {
                ev.prevent_default();
                move_cursor(1);
            }
            "ArrowUp" | "k" => {
                ev.prevent_default();
                move_cursor(-1);
            }
            "Enter" => {
                ev.prevent_default();
                open_selected();
            }
            "Escape" => {
                ev.prevent_default();
                open_diff.set(false);
                diff_expanded.set(false);
                diff_text.set(String::new());
            }
            "l" if !ev.meta_key() && !ev.ctrl_key() && !ev.alt_key() => {
                ev.prevent_default();
                show_log.update(|v| *v = !*v);
            }
            _ => {}
        }
    };

    let mutate = move |path: String, body: serde_json::Value, ok: String| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match post_json_value(&b, &t, &path, body).await {
                Ok(_) => {
                    error.set(String::new());
                    status_line.set(ok);
                    load();
                }
                Err(e) => error.set(e),
            }
        });
    };

    let do_switch = move |branch: String| {
        if branch.is_empty() {
            error.set("Pick a branch.".into());
            return;
        }
        mutate(
            "/v1/git/checkout".into(),
            serde_json::json!({"branch": branch}),
            format!("On {branch}"),
        );
    };
    let do_create = move |branch: String| {
        let branch = branch.trim().to_string();
        if branch.is_empty() {
            error.set("Branch name is required.".into());
            return;
        }
        mutate(
            "/v1/git/checkout".into(),
            serde_json::json!({"branch": branch, "create": true}),
            format!("Created {branch}"),
        );
    };
    let do_commit = move |paths: Vec<String>| {
        let text = message.get().trim().to_string();
        if text.is_empty() {
            error.set("Commit message is required.".into());
            return;
        }
        message.set(String::new());
        let body = if paths.is_empty() {
            serde_json::json!({"message": text})
        } else {
            serde_json::json!({"message": text, "paths": paths})
        };
        mutate("/v1/git/commit".into(), body, "Committed.".into());
    };
    let commit_staged = move |_| do_commit(vec![]);
    let commit_selected = move || {
        let rows = rows.get();
        let Some(row) = rows.get(cursor.get()) else {
            error.set("Select a file first.".into());
            return;
        };
        do_commit(vec![row.path.clone()]);
    };
    let stage_selected = move |_| {
        let rows = rows.get();
        let Some(row) = rows.get(cursor.get()) else {
            error.set("Select a file first.".into());
            return;
        };
        let path = row.path.clone();
        mutate(
            "/v1/git/stage".into(),
            serde_json::json!({"paths": [path]}),
            "Staged.".into(),
        );
    };
    let unstage_selected = move |_| {
        let rows = rows.get();
        let Some(row) = rows.get(cursor.get()).filter(|r| r.staged) else {
            error.set("Select a staged file.".into());
            return;
        };
        let path = row.path.clone();
        mutate(
            "/v1/git/unstage".into(),
            serde_json::json!({"paths": [path]}),
            "Unstaged.".into(),
        );
    };
    let discard_selected = move || {
        let rows = rows.get();
        let Some(row) = rows.get(cursor.get()) else {
            error.set("Select a file first.".into());
            return;
        };
        let path = row.path.clone();
        let staged = row.staged;
        mutate(
            "/v1/git/discard".into(),
            serde_json::json!({"path": path, "staged": staged, "confirm": "discard"}),
            "Discarded.".into(),
        );
    };
    let do_reset = move || {
        mutate(
            "/v1/git/reset".into(),
            serde_json::json!({"mode": "hard", "confirm": "reset-hard"}),
            "Reset to HEAD.".into(),
        );
    };
    let do_delete_branch = move |name: String| {
        if name.is_empty() {
            error.set("Pick a branch to delete.".into());
            return;
        }
        mutate(
            "/v1/git/branch/delete".into(),
            serde_json::json!({"name": name, "confirm": "delete-branch"}),
            "Branch deleted.".into(),
        );
    };

    crate::shell::bind(
        "git.refresh",
        Rc::new(move |_| {
            load();
            Ok(())
        }),
    );
    crate::shell::bind(
        "git.log",
        Rc::new(move |_| {
            show_log.update(|v| *v = !*v);
            Ok(())
        }),
    );
    crate::shell::bind(
        "git.switch",
        Rc::new(move |name| {
            let name = name.trim().to_string();
            if name.is_empty() {
                return Err("Pick a branch.".into());
            }
            do_switch(name);
            Ok(())
        }),
    );
    crate::shell::bind(
        "git.create",
        Rc::new(move |name| {
            let name = name.trim().to_string();
            if name.is_empty() {
                return Err("Branch name is required.".into());
            }
            do_create(name);
            Ok(())
        }),
    );
    crate::shell::bind(
        "git.stage-commit",
        Rc::new(move |_| {
            commit_selected();
            Ok(())
        }),
    );
    crate::shell::bind(
        "git.discard",
        Rc::new(move |_| {
            let path = rows
                .get_untracked()
                .get(cursor.get_untracked())
                .map(|row| row.path.clone())
                .unwrap_or_default();
            if path.is_empty() {
                return Err("Select a file first.".into());
            }
            let run = discard_selected;
            if !crate::confirm::ask(
                crate::confirm::ConfirmRequest {
                    title: format!("Discard changes in {path}?"),
                    body:
                        "Uncommitted changes in that file are thrown away. This cannot be undone."
                            .into(),
                    confirm_label: "Discard".into(),
                },
                Rc::new(move || run()),
            ) {
                return Err(crate::confirm::BUSY.into());
            }
            Ok(())
        }),
    );
    crate::shell::bind(
        "git.reset",
        Rc::new(move |_| {
            do_reset();
            Ok(())
        }),
    );
    crate::shell::bind(
        "git.delete-branch",
        Rc::new(move |name| {
            let name = name.trim().to_string();
            if name.is_empty() {
                return Err("Pick a branch.".into());
            }
            let run_name = name.clone();
            if !crate::confirm::ask(
                crate::confirm::ConfirmRequest {
                    title: format!("Delete branch {name}?"),
                    body: "The local branch is deleted. This does not push.".into(),
                    confirm_label: "Delete branch".into(),
                },
                Rc::new(move || do_delete_branch(run_name.clone())),
            ) {
                return Err(crate::confirm::BUSY.into());
            }
            Ok(())
        }),
    );
    crate::shell::bind(
        "git.copy-diff",
        Rc::new(move |_| {
            let text = diff_text.get_untracked();
            if text.trim().is_empty() {
                return Err("Open a diff first.".into());
            }
            if let Some(win) = web_sys::window() {
                let _ = win.navigator().clipboard().write_text(&text);
            }
            status_line.set("Copied the diff.".into());
            Ok(())
        }),
    );
    on_cleanup(|| {
        for id in [
            "git.refresh",
            "git.log",
            "git.switch",
            "git.create",
            "git.stage-commit",
            "git.discard",
            "git.reset",
            "git.delete-branch",
            "git.copy-diff",
        ] {
            crate::shell::unbind(id);
        }
    });

    view! {
        <div id="git-panel" class="git-panel" tabindex="0" role="tabpanel" aria-label="Git" on:keydown=on_key>
            <div class="git-head">
                <span class="git-branch">{move || {
                    let name = branch.get();
                    if name.is_empty() { "No branch".into() } else if detached.get() { format!("Detached {name}") } else { name }
                }}</span>
                <span class="git-meta">{move || {
                    let (a, b) = (ahead.get(), behind.get());
                    if a == 0 && b == 0 { String::new() } else { format!("ahead {a} · behind {b}") }
                }}</span>
            </div>
            <Show when=move || !error.get().is_empty()>
                <div class="notice" role="alert">
                    <span>{move || error.get()}</span>
                    <button type="button" class="secondary" on:click=move |_| load()>"Retry"</button>
                </div>
            </Show>
            <Show when=move || show_log.get()>
                <ul class="plain">
                    {move || commits.get().into_iter().map(|c| {
                        let hash = short_hash(&c.hash);
                        let subject = c.subject.clone();
                        let meta = format!("{} · {}", c.author, c.date);
                        view! {
                            <li class="wt-card">
                                <span class="name">{subject}</span>
                                <span class="path">{hash} " · " {meta}</span>
                            </li>
                        }
                    }).collect_view()}
                </ul>
                <Show when=move || commits.get().is_empty()>
                    <p class="hint">"No commits yet."</p>
                </Show>
            </Show>
            {move || {
                let rows = rows.get();
                let cur = cursor.get();
                let mut views = Vec::new();
                for section in ["Staged", "Unstaged", "Untracked"] {
                    let heading = section.to_string();
                    views.push(view! { <h3 class="git-section">{heading}</h3> }.into_any());
                    let matched: Vec<_> = rows.iter().enumerate().filter(|(_, row)| row.section == section).collect();
                    if matched.is_empty() {
                        let empty = match section {
                            "Staged" => "No staged files.",
                            "Unstaged" => "No unstaged changes.",
                            _ => "No untracked files.",
                        };
                        views.push(view! { <p class="hint">{empty}</p> }.into_any());
                        continue;
                    }
                    for (i, row) in matched {
                        let on = i == cur;
                        let klass = if on { "git-file on" } else { "git-file" };
                        let path = row.path.clone();
                        let mark = row.status.clone();
                        let added = row.added;
                        let deleted = row.deleted;
                        views.push(view! {
                            <button type="button" class=klass on:click=move |_| {
                                cursor.set(i);
                                open_diff.set(true);
                                diff_req.update(|n| *n = n.saturating_add(1));
                            }>
                                <span class="mark">{mark}</span>
                                <span class="name">{path}</span>
                                <span class="diff-add">{format!("+{added}")}</span>
                                <span class="diff-del">{format!("-{deleted}")}</span>
                            </button>
                        }.into_any());
                    }
                }
                views.into_iter().collect_view()
            }}
            <Show when=move || open_diff.get() && !diff_text.get().is_empty()>
                <div class="git-diff-wrap">
                    <pre class="git-diff" aria-label="Diff">{move || {
                        let text = diff_text.get();
                        // #1428: render-only cap. The full text is already
                        // client-side; expansion renders every line with the
                        // same line-kind classes as the capped render.
                        let limit = if diff_expanded.get() { usize::MAX } else { 400 };
                        text.lines().take(limit).map(|line| {
                            let kind = diff_kind(line);
                            let text = line.to_string();
                            view! { <div class=kind>{text}</div> }.into_any()
                        }).collect_view()
                    }}</pre>
                    {move || {
                        let total = diff_text.get().lines().count();
                        (total > 400 && !diff_expanded.get()).then(|| view! {
                            <button
                                type="button"
                                class="secondary git-diff-expand"
                                data-git-diff-expand="true"
                                on:click=move |_| diff_expanded.set(true)
                            >
                                {format!("Show all {total} lines")}
                            </button>
                        })
                    }}
                </div>
            </Show>
            <Show when=move || !diff_note.get().is_empty()>
                <p class="hint">{move || diff_note.get()}</p>
            </Show>
            <textarea class="git-field" aria-label="Commit message" placeholder="Commit message" prop:value=move || message.get() on:input=move |ev| message.set(event_target_value(&ev))></textarea>
            <div class="git-actions">
                <button type="button" class="primary" on:click=commit_staged>"Commit staged"</button>
                <button type="button" class="secondary" on:click=stage_selected>"Stage"</button>
                <button type="button" class="secondary" on:click=unstage_selected>"Unstage"</button>
            </div>
        </div>
    }
}

#[component]
pub fn WorktreePanel(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status_line: RwSignal<String>,
    active_root: RwSignal<String>,
    active: Memo<bool>,
) -> impl IntoView {
    let error = RwSignal::new(String::new());
    let body = RwSignal::new(None::<WorktreesBody>);
    let kids = RwSignal::new(Vec::<ChildSession>::new());
    let pending = RwSignal::new(String::new());
    let remove_phrase = RwSignal::new(String::new());
    let prune_phrase = RwSignal::new(String::new());
    let kids_note = RwSignal::new(String::new());
    let load_epoch = RwSignal::new(LoadEpoch::new());

    let load = move || {
        let gen = begin(load_epoch);
        kids.set(vec![]);
        kids_note.set(String::new());
        let (b, t, root) = (
            base.get_untracked(),
            token.get_untracked(),
            active_root.get_untracked(),
        );
        let load_epoch = load_epoch;
        spawn_local(async move {
            let live = move || load_epoch.with_untracked(|epoch| epoch.accept(gen));
            let mut err = String::new();
            let worktrees = outcome::take_ok(
                &mut err,
                "Worktrees",
                get_json::<WorktreesBody>(&b, &t, "/v1/git/worktrees").await,
            );
            if !live() {
                return;
            }
            if let Some(parsed) = worktrees {
                body.set(Some(parsed));
            }
            if root.is_empty() {
                kids.set(vec![]);
                kids_note.set(String::new());
                error.set(err);
                return;
            }
            let path = format!("/v1/sessions/{}/children", crate::api::js_encode(&root));
            let children = outcome::take_ok(
                &mut err,
                "Children",
                get_json::<ChildrenResponse>(&b, &t, &path).await,
            );
            if !live() {
                return;
            }
            match children {
                Some(parsed) => {
                    kids.set(parsed.sessions);
                    kids_note.set(String::new());
                }
                None => {
                    kids.set(vec![]);
                    kids_note.set("Could not load child sessions.".into());
                }
            }
            error.set(err);
        });
    };

    Effect::new(move |_| {
        let _ = (base.get(), token.get(), active_root.get());
        load();
        if active.get() {
            focus_id("worktree-panel");
        }
    });

    let on_key = move |ev: web_sys::KeyboardEvent| {
        if shortcut_blocked(&ev) {
            return;
        }
        if ev.key() == "Escape" {
            ev.prevent_default();
            pending.set(String::new());
        }
    };

    let mutate = move |path: String, payload: serde_json::Value, ok: String| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match post_json_value(&b, &t, &path, payload).await {
                Ok(_) => {
                    error.set(String::new());
                    status_line.set(ok);
                    pending.set(String::new());
                    remove_phrase.set(String::new());
                    prune_phrase.set(String::new());
                    load();
                }
                Err(e) => error.set(e),
            }
        });
    };

    let create = move |_| {
        let id = active_root.get();
        if id.is_empty() {
            error.set("No live session to bind a worktree to.".into());
            return;
        }
        mutate(
            "/v1/git/worktrees".into(),
            serde_json::json!({"sessionId": id}),
            "Worktree created. The live session stays in its current directory.".into(),
        );
    };
    let remove_now = move |path: String| {
        if path.is_empty() {
            error.set("Choose a strike-managed worktree.".into());
            return;
        }
        mutate(
            "/v1/git/worktrees/remove".into(),
            serde_json::json!({"path": path, "confirm": "remove-worktree"}),
            "Worktree removed.".into(),
        );
    };
    let prune = move || {
        mutate(
            "/v1/git/worktrees/prune".into(),
            serde_json::json!({"confirm": "prune-stale"}),
            "Stale worktrees removed.".into(),
        );
    };
    crate::shell::bind(
        "worktrees.refresh",
        Rc::new(move |_| {
            load();
            Ok(())
        }),
    );
    crate::shell::bind(
        "worktrees.prune",
        Rc::new(move |_| {
            prune();
            Ok(())
        }),
    );
    on_cleanup(|| {
        crate::shell::unbind("worktrees.refresh");
        crate::shell::unbind("worktrees.prune");
    });

    view! {
        <div id="worktree-panel" class="worktree-panel" tabindex="0" role="tabpanel" aria-label="Worktrees" on:keydown=on_key>
            <Show when=move || !error.get().is_empty()>
                <div class="notice" role="alert">
                    <span>{move || error.get()}</span>
                    <button type="button" class="secondary" on:click=move |_| load()>"Retry"</button>
                </div>
            </Show>
            <Show when=move || !kids_note.get().is_empty()>
                <p class="hint" role="status">{move || kids_note.get()}</p>
            </Show>
            {move || body.get().map(|info| {
                let isolation = info.child_isolation.clone();
                let cleanup = info.worktree_cleanup.clone();
                let session_mode = info.session_worktree.clone();
                view! {
                    <div class="wt-card">
                        <span class="name">{format!("session.childIsolation: {isolation}")}</span>
                        <span class="path">{format!("cleanup {cleanup} · session.worktree {session_mode}")}</span>
                    </div>
                }
            })}
            <div class="wt-actions">
                <button type="button" class="primary" on:click=create>"Create for this session"</button>
            </div>
            <ul class="plain">
                {move || {
                    let root = active_root.get();
                    let kids = kids.get();
                    let list = body.get().map(|b| b.worktrees).unwrap_or_default();
                    list.into_iter().map(|wt| {
                        let label = owner_label(&wt, &kids, &root);
                        let path = wt.path.clone();
                        let path_rm = path.clone();
                        let shown = short_path(&path);
                        let shown_ask = shown.clone();
                        let branch = if wt.branch.is_empty() { wt.head.chars().take(7).collect::<String>() } else { wt.branch.clone() };
                        let stale = wt.stale;
                        let klass = if stale { "wt-card stale" } else { "wt-card" };
                        let can_remove = wt.strike_managed && !wt.primary;
                        view! {
                            <li class=klass>
                                <span class="name">{label}</span>
                                <span class="path" title=path>{shown} " · " {branch}</span>
                                {can_remove.then(|| view! {
                                    <button type="button" class="secondary" on:click=move |_| {
                                        let path = path_rm.clone();
                                        let label = shown_ask.clone();
                                        let run = remove_now;
                                        if !crate::confirm::ask(
                                            crate::confirm::ConfirmRequest {
                                                title: format!("Remove {label}?"),
                                                body: "This deletes that worktree directory, including uncommitted files there. The primary checkout is never removed.".into(),
                                                confirm_label: "Remove worktree".into(),
                                            },
                                            Rc::new(move || run(path.clone())),
                                        ) {
                                            error.set(crate::confirm::BUSY.into());
                                        }
                                    }>"Remove"</button>
                                })}
                            </li>
                        }
                    }).collect_view()
                }}
            </ul>
            <Show when=move || body.get().map(|b| b.worktrees.is_empty()).unwrap_or(false)>
                <p class="hint">"No worktrees."</p>
            </Show>
        </div>
    }
}
