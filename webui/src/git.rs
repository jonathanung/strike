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

/// Paths of every row whose staged flag equals `staged`, in panel order.
/// Stage all is `paths_where(rows, false)` — Unstaged and Untracked rows
/// both carry `staged: false`, so new agent-created files are included,
/// matching `git add` semantics (#1426 Q3). Unstage all is the staged set.
fn paths_where(rows: &[GitRow], staged: bool) -> Vec<String> {
    rows.iter()
        .filter(|row| row.staged == staged)
        .map(|row| row.path.clone())
        .collect()
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

/// One `git stash list` entry from GET /v1/git/stash (#1471). The hash
/// pins the entry for apply/pop/drop.
#[derive(Clone, Debug, Deserialize, PartialEq)]
struct GitStash {
    index: usize,
    #[serde(default)]
    r#ref: String,
    hash: String,
    #[serde(default)]
    message: String,
    #[serde(default)]
    date: String,
}

#[derive(Clone, Debug, Deserialize)]
struct GitStashes {
    #[serde(default)]
    stashes: Vec<GitStash>,
}

/// "Stash changes" is enabled only when there is something git would
/// stash: tracked changes, or untracked files when opted in (#1471 Q1).
fn stash_enabled(rows: &[GitRow], include_untracked: bool) -> bool {
    rows.iter()
        .any(|row| row.section != "Untracked" || include_untracked)
}

/// Relative age for a stash row ("just now", "5m ago", "3h ago", "2d ago").
fn relative_age(then_ms: f64, now_ms: f64) -> String {
    if !then_ms.is_finite() || !now_ms.is_finite() {
        return String::new();
    }
    let secs = ((now_ms - then_ms) / 1000.0).max(0.0) as u64;
    match secs {
        0..=59 => "just now".into(),
        60..=3599 => format!("{}m ago", secs / 60),
        3600..=86_399 => format!("{}h ago", secs / 3600),
        _ => format!("{}d ago", secs / 86_400),
    }
}

/// Body for apply/pop/drop: index plus the pinning hash.
fn stash_body(stash: &GitStash, confirm: Option<&str>) -> serde_json::Value {
    let mut body = serde_json::json!({"index": stash.index, "hash": stash.hash});
    if let Some(c) = confirm {
        body["confirm"] = serde_json::Value::String(c.to_string());
    }
    body
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

fn enter_on_button(ev: &web_sys::KeyboardEvent) -> bool {
    ev.target()
        .and_then(|t| t.dyn_into::<web_sys::Element>().ok())
        .map(|el| el.tag_name().eq_ignore_ascii_case("button"))
        .unwrap_or(false)
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

/// One changed path on the branch versus its base (#1450).
#[derive(Clone, Debug, Deserialize, PartialEq)]
struct BranchFile {
    path: String,
    #[serde(default)]
    added: i64,
    #[serde(default)]
    deleted: i64,
    #[serde(default)]
    binary: bool,
}

/// GET /v1/git/branch-changes — what this branch contains versus base.
#[derive(Clone, Debug, Deserialize)]
struct BranchChanges {
    #[serde(default)]
    state: String,
    #[serde(default)]
    branch: String,
    #[serde(default)]
    base: String,
    #[serde(default, rename = "baseDetected")]
    base_detected: bool,
    #[serde(default, rename = "mergeBase")]
    merge_base: String,
    #[serde(default)]
    commits: Vec<GitCommit>,
    #[serde(default, rename = "commitsTotal")]
    commits_total: usize,
    #[serde(default)]
    files: Vec<BranchFile>,
}

/// Honest empty-state text for a non-"ok" branch-changes state (#1450).
/// `None` means the lists below are the answer.
fn branch_state_message(state: &str, branch: &str, base: &str) -> Option<String> {
    match state {
        "ok" | "" => None,
        "on-base" => Some(format!("You are on {base} — choose another base to compare {branch} against.")),
        "no-merge-base" => Some(format!("{branch} and {base} share no history — nothing to compare.")),
        "detached" => Some("Detached HEAD — check out a branch to see its changes.".into()),
        "no-commits" => Some("No commits yet.".into()),
        "choose-base" => Some("No default branch found — choose a base branch.".into()),
        other => Some(format!("Unknown state: {other}")),
    }
}

/// Header line: counts are stated even when the commit list is capped.
fn branch_summary(c: &BranchChanges) -> String {
    let commits = if c.commits_total > c.commits.len() {
        format!("{} commits (showing {})", c.commits_total, c.commits.len())
    } else {
        format!("{} commits", c.commits_total)
    };
    let files = c.files.len();
    let mb = short_hash(&c.merge_base);
    format!("{commits} · {files} files vs {} (merge-base {mb})", c.base)
}

/// Base picker choices: local branches other than the current one, plus
/// the base in use when it is not a local branch (e.g. origin/main).
fn base_options(branches: &[GitBranch], current: &str, base: &str) -> Vec<String> {
    let mut out: Vec<String> = branches
        .iter()
        .filter(|b| b.name != current)
        .map(|b| b.name.clone())
        .collect();
    if !base.is_empty() && base != current && !out.iter().any(|n| n == base) {
        out.insert(0, base.to_string());
    }
    out
}

fn branch_file_stat(f: &BranchFile) -> String {
    if f.binary {
        "binary".into()
    } else {
        format!("+{} -{}", f.added, f.deleted)
    }
}

/// One changed path in an inspected commit (#1484).
#[derive(Clone, Debug, Deserialize, PartialEq)]
struct GitShowFile {
    path: String,
    #[serde(default)]
    added: i64,
    #[serde(default)]
    deleted: i64,
    #[serde(default)]
    binary: bool,
}

/// GET /v1/git/show — read-only commit detail (#1484).
#[derive(Clone, Debug, Deserialize)]
struct GitShow {
    hash: String,
    #[serde(default)]
    subject: String,
    #[serde(default)]
    body: String,
    #[serde(default)]
    author: String,
    #[serde(default)]
    date: String,
    #[serde(default)]
    parents: Vec<String>,
    #[serde(default)]
    files: Vec<GitShowFile>,
    #[serde(default, rename = "filesTotal")]
    files_total: usize,
    #[serde(default)]
    patch: String,
    #[serde(default)]
    truncated: bool,
}

/// Render window shared by the working-tree diff (#1428) and a commit
/// patch (#1484 Q2): wasm DOM cost, not data, limits the default view.
const DIFF_RENDER_LINES: usize = 400;

/// Diff/patch lines with add/del/ctx classes, capped at
/// DIFF_RENDER_LINES unless expanded. One renderer for both surfaces.
fn diff_lines_view(text: &str, expanded: bool) -> Vec<AnyView> {
    let limit = if expanded { usize::MAX } else { DIFF_RENDER_LINES };
    text.lines()
        .take(limit)
        .map(|line| {
            let kind = diff_kind(line);
            let text = line.to_string();
            view! { <div class=kind>{text}</div> }.into_any()
        })
        .collect()
}

/// Clicking a log row opens that commit; clicking the open one closes it
/// (#1484). Returns the hash to load, or `None` to close the detail.
fn commit_click_target(open: &str, clicked: &str) -> Option<String> {
    if !open.is_empty() && open == clicked {
        None
    } else {
        Some(clicked.to_string())
    }
}

/// Honest notes under a commit detail: merges are diffed against the
/// first parent, and a daemon-truncated patch says so (#1484).
fn commit_detail_notes(parents: usize, truncated: bool, shown: usize, total: usize) -> Vec<String> {
    let mut notes = Vec::new();
    if parents > 1 {
        notes.push("Merge commit — changes shown against the first parent.".to_string());
    }
    if total > shown {
        notes.push(format!("Showing {shown} of {total} changed files."));
    }
    if truncated {
        notes.push("Patch truncated by the daemon — showing the first 200 KB.".to_string());
    }
    notes
}

/// "+3 -1" for text files, "binary" for binary ones.
fn show_file_stat(file: &GitShowFile) -> String {
    if file.binary {
        "binary".to_string()
    } else {
        format!("+{} -{}", file.added, file.deleted)
    }
}

fn short_hash(hash: &str) -> String {
    hash.chars().take(7).collect()
}

/// Initial Git log page (#1432) and the daemon's hard cap
/// (`gitLogMax` in internal/frontend/server/git.go).
const LOG_LIMIT_INITIAL: usize = 30;
const LOG_LIMIT_MAX: usize = 100;

/// Next log limit to request, or `None` when no More control should
/// show: a short page (`returned < current`) means history is
/// exhausted, and at [`LOG_LIMIT_MAX`] the daemon clamps anyway.
fn next_log_limit(current: usize, returned: usize) -> Option<usize> {
    if returned < current || current >= LOG_LIMIT_MAX {
        return None;
    }
    Some(LOG_LIMIT_MAX)
}

/// Honest end-of-log note shown whenever the More control disappears
/// (#1432 Q2), so a missing button is never ambiguous.
fn log_end_note(current: usize, returned: usize) -> Option<String> {
    if returned == 0 || next_log_limit(current, returned).is_some() {
        return None;
    }
    if returned < current {
        Some(format!("End of history — showing all {returned} commits."))
    } else {
        Some(format!("Showing the last {returned} commits."))
    }
}

/// Case-insensitive substring match on the full path (#1435 Q1).
/// An empty/blank query matches everything.
fn path_matches_query(path: &str, query: &str) -> bool {
    let query = query.trim().to_lowercase();
    query.is_empty() || path.to_lowercase().contains(&query)
}

/// Original indices of the rows visible under `query`, in row order
/// (#1435). Cursor, Enter, Stage/Unstage and Discard keep acting on
/// these original indices, never on visible positions.
fn filtered_row_indices(rows: &[GitRow], query: &str) -> Vec<usize> {
    rows.iter()
        .enumerate()
        .filter(|(_, row)| path_matches_query(&row.path, query))
        .map(|(i, _)| i)
        .collect()
}

/// Step from `cursor` by `delta` through the visible (filtered)
/// original indices. With no query this is the plain clamp over all
/// rows, preserving today's behaviour exactly.
fn filtered_cursor_step(
    rows: &[GitRow],
    query: &str,
    cursor: usize,
    delta: i32,
) -> Option<usize> {
    let visible = filtered_row_indices(rows, query);
    if visible.is_empty() {
        return None;
    }
    // Position of the cursor inside the visible list; if the cursor
    // sits on a hidden row, start from just before the first visible
    // row at/after it so Down lands on that row.
    let pos = visible.iter().position(|&i| i == cursor);
    let next_pos = match pos {
        Some(p) => (p as i32 + delta).clamp(0, visible.len() as i32 - 1) as usize,
        None if delta < 0 => visible
            .iter()
            .rposition(|&i| i < cursor)
            .unwrap_or(0),
        None => visible
            .iter()
            .position(|&i| i > cursor)
            .unwrap_or(visible.len() - 1),
    };
    Some(visible[next_pos])
}

/// Where the cursor belongs after the filter or the row list changed:
/// the cursor itself when visible, else the next visible original index
/// (the last one if none follow). `None` when nothing is visible (#1435).
fn snap_cursor(rows: &[GitRow], query: &str, cursor: usize) -> Option<usize> {
    filtered_cursor_step(rows, query, cursor, 0)
}

/// The row a selected-row action (Stage / Unstage / Commit selected /
/// Discard) may target: only the cursor row, and only while it is
/// visible under the filter — never a hidden file (#1435).
fn selected_row(rows: &[GitRow], query: &str, cursor: usize) -> Option<GitRow> {
    if filtered_row_indices(rows, query).contains(&cursor) {
        rows.get(cursor).cloned()
    } else {
        None
    }
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

/// Plain-language error for the panel: transport errors arrive as
/// `HTTP 400 {"error":"git commit: exit status 1"}`; surface only the
/// daemon's `error` field. Follows the `http_message` idiom in editor.rs.
/// Strings without a JSON error field pass through unchanged.
fn http_message(err: &str) -> String {
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

/// Why a commit attempt must not run, or `None` if it may proceed.
/// Every `Some` outcome leaves the composed message exactly as typed:
/// the caller returns before any request or clearing (#1519).
/// `targeted` commits name their own paths, so the staged-count guard
/// only applies to the plain "Commit staged" path.
fn commit_block(text: &str, targeted: bool, staged_count: usize) -> Option<&'static str> {
    if text.is_empty() {
        return Some("Commit message is required.");
    }
    if !targeted && staged_count == 0 {
        return Some("Nothing is staged. Stage a file first, then commit.");
    }
    None
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
    // #1471: stashes come from the daemon on every load, so terminal-
    // created stashes appear too.
    let stashes = RwSignal::new(Vec::<GitStash>::new());
    let stash_msg = RwSignal::new(String::new());
    let stash_untracked = RwSignal::new(false);
    let stash_open = RwSignal::new(String::new());
    let stash_diff = RwSignal::new(String::new());
    let stash_diff_truncated = RwSignal::new(false);
    let stash_epoch = RwSignal::new(LoadEpoch::new());
    // Stash errors live here, not in `error`: a failed apply/pop reloads
    // the panel (load() rewrites `error`) and git's conflict text must stay.
    let stash_note = RwSignal::new(String::new());
    let stash_diff_loaded = RwSignal::new(false);
    let cursor = RwSignal::new(0usize);
    let open_diff = RwSignal::new(false);
    let diff_text = RwSignal::new(String::new());
    let diff_note = RwSignal::new(String::new());
    let diff_req = RwSignal::new(0u32);
    // #1428: the diff render is capped at 400 lines by default (wasm DOM
    // cost); diff_expanded lifts the cap for the currently open diff only.
    // It resets whenever a diff is (re)loaded — see the diff effect below.
    let diff_expanded = RwSignal::new(false);
    // #1580: whether the daemon truncated the currently open diff, fed
    // from the /v1/git/diff response; copy feedback must call a truncated
    // copy partial rather than complete.
    let diff_truncated = RwSignal::new(false);
    let show_log = RwSignal::new(false);
    // #1450: branch changes vs base. `bc_base` is the user's pick ("" =
    // let the daemon detect the default branch).
    let show_branch = RwSignal::new(false);
    let bc = RwSignal::new(None::<BranchChanges>);
    let bc_base = RwSignal::new(String::new());
    let bc_note = RwSignal::new(String::new());
    let bc_epoch = RwSignal::new(LoadEpoch::new());
    // File diffs have their own epoch so a diff click never cancels a
    // pending base change (and vice versa only via an explicit bump).
    let bc_diff_epoch = RwSignal::new(LoadEpoch::new());
    let bc_file = RwSignal::new(String::new());
    let bc_diff = RwSignal::new(String::new());
    let bc_diff_truncated = RwSignal::new(false);
    let bc_diff_expanded = RwSignal::new(false);
    // #1484: commit detail. `shown_hash` is the row the user opened (set
    // at click so the row highlights while loading); `shown` its payload.
    let shown_hash = RwSignal::new(String::new());
    let shown = RwSignal::new(None::<GitShow>);
    let show_note = RwSignal::new(String::new());
    let show_expanded = RwSignal::new(false);
    let show_epoch = RwSignal::new(LoadEpoch::new());
    let log_limit = RwSignal::new(LOG_LIMIT_INITIAL);
    let copied_hash = RwSignal::new(String::new());
    let file_filter = RwSignal::new(String::new());
    let message = RwSignal::new(String::new());
    let switch_to = RwSignal::new(String::new());
    let load_epoch = RwSignal::new(LoadEpoch::new());
    // Log-only fetches (Show more commits, #1432) race full loads; the
    // newest log request wins.
    let log_epoch = RwSignal::new(LoadEpoch::new());

    // Move the cursor and close a diff that belonged to the old row
    // (#1435): an open diff must always show the row actions target.
    let select_row = move |i: usize| {
        if cursor.get_untracked() == i {
            return;
        }
        cursor.set(i);
        open_diff.set(false);
        diff_expanded.set(false);
        diff_text.set(String::new());
        diff_truncated.set(false);
    };


    // #1450: fetch branch changes for the chosen (or detected) base.
    let load_branch_changes = move || {
        let gen = begin(bc_epoch);
        // Drop the old comparison and any in-flight file diff: nothing
        // from the previous base stays clickable while this loads.
        let _ = begin(bc_diff_epoch);
        bc.set(None);
        bc_note.set(String::new());
        bc_file.set(String::new());
        bc_diff.set(String::new());
        let pick = bc_base.get_untracked();
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = if pick.is_empty() {
                "/v1/git/branch-changes".to_string()
            } else {
                format!("/v1/git/branch-changes?base={}", crate::api::js_encode(&pick))
            };
            let res = get_json::<BranchChanges>(&b, &t, &path).await;
            if !bc_epoch.with_untracked(|epoch| epoch.accept(gen)) {
                return;
            }
            match res {
                Ok(body) => bc.set(Some(body)),
                Err(e) => {
                    bc.set(None);
                    bc_note.set(format!("Branch changes: {}", http_message(&e)));
                }
            }
        });
    };
    // Cumulative merge-base...HEAD diff of one branch file (#1450).
    let open_branch_file = move |path: String, against: String| {
        let gen = begin(bc_diff_epoch);
        if bc_file.get_untracked() == path {
            bc_file.set(String::new());
            bc_diff.set(String::new());
            return;
        }
        bc_file.set(path.clone());
        bc_diff.set(String::new());
        bc_diff_truncated.set(false);
        bc_diff_expanded.set(false);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let url = format!(
                "/v1/git/branch-diff?base={}&path={}",
                crate::api::js_encode(&against),
                crate::api::js_encode(&path)
            );
            let res = get_json::<GitDiff>(&b, &t, &url).await;
            if !bc_diff_epoch.with_untracked(|epoch| epoch.accept(gen)) {
                return;
            }
            match res {
                Ok(body) => {
                    bc_diff_truncated.set(body.truncated);
                    bc_diff.set(body.diff);
                }
                Err(e) => bc_note.set(format!("Branch diff: {}", http_message(&e))),
            }
        });
    };
    let toggle_branch = move || {
        let on = !show_branch.get_untracked();
        show_branch.set(on);
        if on {
            load_branch_changes();
        }
    };

    let load = move || {
        let gen = begin(load_epoch);
        let log_gen = begin(log_epoch);
        // A refresh (commit, checkout, repo switch) may change history;
        // drop any open commit detail and its in-flight request (#1484).
        let _ = begin(show_epoch);
        shown_hash.set(String::new());
        shown.set(None);
        show_note.set(String::new());
        // A refresh (commit, checkout) can change what the branch holds.
        if show_branch.get_untracked() {
            load_branch_changes();
        }
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
                    let snapped =
                        snap_cursor(&list, &file_filter.get_untracked(), cursor.get_untracked());
                    rows.set(list);
                    // Rows reorder after Stage etc.; keep the cursor on a
                    // visible row so actions never hit a hidden file.
                    select_row(snapped.unwrap_or(0));
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
            let log_path = format!("/v1/git/log?limit={}", log_limit.get_untracked());
            if let Some(body) = outcome::take_ok(
                &mut err,
                "Log",
                get_json::<GitLog>(&b, &t, &log_path).await,
            ) {
                if log_epoch.with_untracked(|epoch| epoch.accept(log_gen)) {
                    commits.set(body.commits);
                }
            }
            if !live() {
                return;
            }
            if let Some(body) = outcome::take_ok(
                &mut err,
                "Stashes",
                get_json::<GitStashes>(&b, &t, "/v1/git/stash").await,
            ) {
                // A preview whose stash is gone (popped/dropped here or in
                // a terminal) closes rather than showing a dead diff.
                let open = stash_open.get_untracked();
                if !open.is_empty() && !body.stashes.iter().any(|s| s.hash == open) {
                    let _ = begin(stash_epoch);
                    stash_open.set(String::new());
                    stash_diff.set(String::new());
                }
                stashes.set(body.stashes);
            }
            if live() {
                error.set(err);
            }
        });
    };

    Effect::new(move |prev: Option<(String, String)>| {
        let cur = (base.get(), token.get());
        if prev.as_ref() != Some(&cur) {
            // Per-repo view state resets when the repo changes
            // (#1432 Q3, #1435 Q3), not on panel re-activation.
            log_limit.set(LOG_LIMIT_INITIAL);
            file_filter.set(String::new());
            copied_hash.set(String::new());
        }
        load();
        if active.get() {
            focus_id("git-panel");
        }
        cur
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
        let Some(row) = selected_row(
            &rows.get_untracked(),
            &file_filter.get_untracked(),
            cursor.get_untracked(),
        ) else {
            diff_text.set(String::new());
            diff_truncated.set(false);
            return;
        };
        if !row.inline_diff.is_empty() {
            diff_text.set(row.inline_diff);
            diff_note.set(String::new());
            diff_truncated.set(false);
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
                    diff_truncated.set(body.truncated);
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
                    diff_truncated.set(false);
                    diff_note.set(e);
                }
            }
        });
    });

    // Copy diff (#1580): writes the exact diff_text via the shared
    // clipboard helper, which awaits the write — feedback in diff_note is
    // only "copied" once the browser accepted it, and a truncated diff is
    // called partial in the feedback itself.
    let copy_diff = move |_| {
        let text = diff_text.get_untracked();
        if !crate::clipboard::diff_copyable(&text) {
            return;
        }
        let truncated = diff_truncated.get_untracked();
        spawn_local(async move {
            let ok = crate::clipboard::copy_text(&text).await.is_ok();
            diff_note.set(crate::clipboard::diff_copy_feedback(truncated, ok));
        });
    };

    let move_cursor = move |delta: i32| {
        let all = rows.get_untracked();
        // Under an active filter, step through visible original
        // indices only (#1435); unfiltered this is today's clamp.
        let Some(next) = filtered_cursor_step(
            &all,
            &file_filter.get_untracked(),
            cursor.get_untracked(),
            delta,
        ) else {
            return;
        };
        select_row(next);
    };

    let show_more_log = move |_| {
        let Some(next) =
            next_log_limit(log_limit.get_untracked(), commits.get_untracked().len())
        else {
            return;
        };
        // Fetch only the log: a full load() would blank the changed-file
        // rows and refetch status/branches just for more history.
        let gen = begin(log_epoch);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/git/log?limit={next}");
            let res = get_json::<GitLog>(&b, &t, &path).await;
            if !log_epoch.with_untracked(|epoch| epoch.accept(gen)) {
                return;
            }
            match res {
                // Raise the limit only once the larger page arrived, so a
                // failed fetch never claims "End of history" (#1432).
                Ok(body) => {
                    log_limit.set(next);
                    commits.set(body.commits);
                }
                Err(e) => error.set(format!("Log: {}", http_message(&e))),
            }
        });
    };

    let open_commit = move |hash: String| {
        let gen = begin(show_epoch);
        show_expanded.set(false);
        show_note.set(String::new());
        shown.set(None);
        let Some(target) = commit_click_target(&shown_hash.get_untracked(), &hash) else {
            shown_hash.set(String::new());
            return;
        };
        shown_hash.set(target.clone());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            // Log hashes are hex; no query escaping needed.
            let path = format!("/v1/git/show?ref={target}");
            let res = get_json::<GitShow>(&b, &t, &path).await;
            if !show_epoch.with_untracked(|epoch| epoch.accept(gen)) {
                return;
            }
            match res {
                Ok(body) => shown.set(Some(body)),
                Err(e) => show_note.set(format!("Commit: {}", http_message(&e))),
            }
        });
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
                // A focused button (log row, file row, action) keeps its
                // native click; Enter-to-open is for the panel itself
                // (#1484: Enter on a commit row must open that commit).
                if enter_on_button(&ev) {
                    return;
                }
                ev.prevent_default();
                open_selected();
            }
            "Escape" => {
                ev.prevent_default();
                open_diff.set(false);
                diff_expanded.set(false);
                diff_text.set(String::new());
                diff_truncated.set(false);
            }
            "b" if !ev.meta_key() && !ev.ctrl_key() && !ev.alt_key() => {
                ev.prevent_default();
                toggle_branch();
            }
            "l" if !ev.meta_key() && !ev.ctrl_key() && !ev.alt_key() => {
                ev.prevent_default();
                show_log.update(|v| *v = !*v);
            }
            _ => {}
        }
    };

    // `on_ok` runs only after the POST succeeds (#1519): callers use it
    // to clear text the user composed, so a failed request can never
    // destroy it. Failures surface the daemon's message via http_message.
    let mutate_with =
        move |path: String, body: serde_json::Value, ok: String, on_ok: Rc<dyn Fn()>| {
            let (b, t) = (base.get_untracked(), token.get_untracked());
            spawn_local(async move {
                match post_json_value(&b, &t, &path, body).await {
                    Ok(_) => {
                        on_ok();
                        error.set(String::new());
                        status_line.set(ok);
                        load();
                    }
                    Err(e) => error.set(http_message(&e)),
                }
            });
        };
    let mutate = move |path: String, body: serde_json::Value, ok: String| {
        mutate_with(path, body, ok, Rc::new(|| {}));
    };

    // #1471: stash actions. Pop/apply conflicts come back as the daemon's
    // 409 with git's own words; nothing here resolves or discards.
    // Stash mutations reload on failure too: a conflicting apply/pop has
    // still written markers / restored files, and the panel must show it
    // next to git's own words (#1471).
    let stash_mutate = move |path: String, body: serde_json::Value, ok: String, on_ok: Rc<dyn Fn()>| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match post_json_value(&b, &t, &path, body).await {
                Ok(_) => {
                    on_ok();
                    stash_note.set(String::new());
                    status_line.set(ok);
                }
                Err(e) => stash_note.set(http_message(&e)),
            }
            load();
        });
    };
    let do_stash = move || {
        let msg = stash_msg.get_untracked().trim().to_string();
        let mut body = serde_json::json!({"includeUntracked": stash_untracked.get_untracked()});
        if !msg.is_empty() {
            body["message"] = serde_json::Value::String(msg);
        }
        stash_mutate(
            "/v1/git/stash".into(),
            body,
            "Stashed changes.".into(),
            Rc::new(move || stash_msg.set(String::new())),
        );
    };
    let do_stash_apply = move |stash: GitStash, pop: bool| {
        let (path, ok) = if pop {
            ("/v1/git/stash/pop", format!("Popped {}.", stash.r#ref))
        } else {
            ("/v1/git/stash/apply", format!("Applied {}.", stash.r#ref))
        };
        stash_mutate(path.into(), stash_body(&stash, None), ok, Rc::new(|| {}));
    };
    let do_stash_drop = move |stash: GitStash| {
        let label = stash.r#ref.clone();
        let run = stash.clone();
        let asked = crate::confirm::ask(
            crate::confirm::ConfirmRequest {
                title: format!("Drop {label}?"),
                body: format!("“{}” is deleted from the stash list. This cannot be undone from the panel.", stash.message),
                confirm_label: "Drop stash".into(),
            },
            Rc::new(move || {
                stash_mutate(
                    "/v1/git/stash/drop".into(),
                    stash_body(&run, Some("drop-stash")),
                    format!("Dropped {}.", run.r#ref),
                    Rc::new(|| {}),
                );
            }),
        );
        if !asked {
            stash_note.set(crate::confirm::BUSY.into());
        }
    };
    let toggle_stash_preview = move |stash: GitStash| {
        let gen = begin(stash_epoch);
        if stash_open.get_untracked() == stash.hash {
            stash_open.set(String::new());
            stash_diff.set(String::new());
            return;
        }
        stash_open.set(stash.hash.clone());
        stash_diff.set(String::new());
        stash_diff_truncated.set(false);
        stash_diff_loaded.set(false);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/git/stash/diff?index={}&hash={}", stash.index, stash.hash);
            let res = get_json::<GitDiff>(&b, &t, &path).await;
            if !stash_epoch.with_untracked(|epoch| epoch.accept(gen)) {
                return;
            }
            match res {
                Ok(body) => {
                    stash_diff_truncated.set(body.truncated);
                    stash_diff.set(body.diff);
                    stash_diff_loaded.set(true);
                }
                Err(e) => stash_note.set(http_message(&e)),
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
        // No panel field holds the name (it arrives by value from the
        // palette since #1669), so there is no panel text to clear
        // before the POST resolves; failures surface the daemon's
        // plain-language error via mutate (#1519).
        mutate(
            "/v1/git/checkout".into(),
            serde_json::json!({"branch": branch, "create": true}),
            format!("Created {branch}"),
        );
    };
    let do_commit = move |paths: Vec<String>| {
        let text = message.get_untracked().trim().to_string();
        let staged_count = rows.get_untracked().iter().filter(|row| row.staged).count();
        if let Some(block) = commit_block(&text, !paths.is_empty(), staged_count) {
            // Blocked attempts preserve the message exactly as typed.
            error.set(block.into());
            return;
        }
        let body = if paths.is_empty() {
            serde_json::json!({"message": text})
        } else {
            serde_json::json!({"message": text, "paths": paths})
        };
        // Clear only on success: on_ok fires after the POST resolves Ok,
        // so a failure (nothing staged server-side, hook, lock) leaves
        // the composed message in the box (#1519).
        mutate_with(
            "/v1/git/commit".into(),
            body,
            "Committed.".into(),
            Rc::new(move || message.set(String::new())),
        );
    };
    let commit_staged = move |_| do_commit(vec![]);
    let commit_selected = move || {
        let Some(row) = selected_row(&rows.get(), &file_filter.get(), cursor.get()) else {
            error.set("Select a file first.".into());
            return;
        };
        do_commit(vec![row.path.clone()]);
    };
    let stage_selected = move |_| {
        let Some(row) = selected_row(&rows.get(), &file_filter.get(), cursor.get()) else {
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
        let Some(row) = selected_row(&rows.get(), &file_filter.get(), cursor.get())
            .filter(|r| r.staged)
        else {
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
    // Batch variants (#1426): one POST with the whole path list — the
    // daemon routes already accept `Paths []string` — never N per-file
    // calls looping `stage_selected` / `unstage_selected`.
    let stage_all = move |_| {
        let paths = paths_where(&rows.get(), false);
        if paths.is_empty() {
            error.set("No unstaged or untracked files.".into());
            return;
        }
        mutate(
            "/v1/git/stage".into(),
            serde_json::json!({"paths": paths}),
            "Staged all.".into(),
        );
    };
    let unstage_all = move |_| {
        let paths = paths_where(&rows.get(), true);
        if paths.is_empty() {
            error.set("No staged files.".into());
            return;
        }
        mutate(
            "/v1/git/unstage".into(),
            serde_json::json!({"paths": paths}),
            "Unstaged all.".into(),
        );
    };
    let discard_selected = move || {
        let Some(row) = selected_row(&rows.get(), &file_filter.get(), cursor.get()) else {
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
            let path = selected_row(
                &rows.get_untracked(),
                &file_filter.get_untracked(),
                cursor.get_untracked(),
            )
            .map(|row| row.path)
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
                <button
                    type="button"
                    class="secondary git-branch-toggle"
                    data-git-branch-toggle="true"
                    aria-pressed=move || show_branch.get().to_string()
                    on:click=move |_| toggle_branch()
                >"Branch"</button>
            </div>
            <Show when=move || show_branch.get()>
                <section class="branch-changes" data-git-branch-changes="true" aria-label="Branch changes">
                    {move || {
                        // While loading, fall back to the head branch and
                        // the user's pick so the picker never snaps back.
                        let current = bc.get().map(|c| c.branch).unwrap_or_else(|| branch.get());
                        let in_use = bc.get().map(|c| c.base).unwrap_or_else(|| bc_base.get());
                        let options = base_options(&branches.get(), &current, &in_use);
                        let selected = in_use.clone();
                        view! {
                            <label class="branch-base">
                                <span class="git-meta">"Base"</span>
                                <select
                                    class="git-field"
                                    data-git-branch-base="true"
                                    aria-label="Base branch"
                                    on:change=move |ev| {
                                        bc_base.set(event_target_value(&ev));
                                        load_branch_changes();
                                    }
                                >
                                    <option value="" selected=in_use.is_empty()>"Choose a base branch"</option>
                                    {options.into_iter().map(|name| {
                                        let on = name == selected;
                                        let label = name.clone();
                                        view! { <option value=name selected=on>{label}</option> }
                                    }).collect_view()}
                                </select>
                            </label>
                        }
                    }}
                    <Show when=move || !bc_note.get().is_empty()>
                        <p class="hint" role="alert">{move || bc_note.get()}</p>
                    </Show>
                    {move || bc.get().map(|c| {
                        if let Some(msg) = branch_state_message(&c.state, &c.branch, &c.base) {
                            return view! { <p class="hint" data-git-branch-state=c.state.clone()>{msg}</p> }.into_any();
                        }
                        let summary = branch_summary(&c);
                        let against = c.base.clone();
                        let no_commits = c.commits.is_empty();
                        let no_files = c.files.is_empty();
                        view! {
                            <p class="git-meta" data-git-branch-summary="true">{summary}</p>
                            <h3 class="git-section">"Commits"</h3>
                            {no_commits.then(|| view! { <p class="hint">"No commits on this branch yet."</p> })}
                            <ul class="plain">
                                {c.commits.into_iter().map(|commit| {
                                    let meta = format!("{} · {} · {}", short_hash(&commit.hash), commit.author, commit.date);
                                    let full = commit.hash;
                                    let subject = commit.subject;
                                    view! {
                                        <li class="wt-card">
                                            <span class="name">{subject}</span>
                                            <span class="path" title=full>{meta}</span>
                                        </li>
                                    }
                                }).collect_view()}
                            </ul>
                            <h3 class="git-section">"Files"</h3>
                            {no_files.then(|| view! { <p class="hint">"No file changes versus the merge-base."</p> })}
                            {c.files.into_iter().map(|f| {
                                let stat = branch_file_stat(&f);
                                let path = f.path.clone();
                                let path_on = f.path.clone();
                                let attr = f.path.clone();
                                let label = f.path;
                                let against = against.clone();
                                view! {
                                    <button
                                        type="button"
                                        class="git-file"
                                        class:on=move || bc_file.get() == path_on
                                        data-git-branch-file=attr
                                        on:click=move |_| open_branch_file(path.clone(), against.clone())
                                    >
                                        <span class="name">{label}</span>
                                        <span class="branch-stat">{stat}</span>
                                    </button>
                                }
                            }).collect_view()}
                        }.into_any()
                    })}
                    <Show when=move || !bc_diff.get().is_empty()>
                        <pre class="git-diff" aria-label="Branch diff" data-git-branch-diff="true">{move || diff_lines_view(&bc_diff.get(), bc_diff_expanded.get())}</pre>
                        {move || {
                            let total = bc_diff.get().lines().count();
                            (total > DIFF_RENDER_LINES && !bc_diff_expanded.get()).then(|| view! {
                                <button type="button" class="secondary git-diff-expand" on:click=move |_| bc_diff_expanded.set(true)>
                                    {format!("Show all {total} lines")}
                                </button>
                            })
                        }}
                        <Show when=move || bc_diff_truncated.get()>
                            <p class="hint">"Diff truncated by the daemon — showing the first 200 KB."</p>
                        </Show>
                    </Show>
                </section>
            </Show>
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
                        let full = c.hash.clone();
                        let full_open = c.hash.clone();
                        let full_on = c.hash.clone();
                        let full_attr = c.hash.clone();
                        let full_for_click = c.hash.clone();
                        let full_for_label = c.hash.clone();
                        view! {
                            <li class="wt-card">
                                <button
                                    type="button"
                                    class="git-commit"
                                    data-git-commit=full_attr
                                    aria-pressed=move || (shown_hash.get() == full_on).to_string()
                                    on:click=move |_| open_commit(full_open.clone())
                                >
                                    <span class="name">{subject}</span>
                                    <span class="path" title=full>{hash} " · " {meta}</span>
                                </button>
                                <button type="button" class="secondary git-copy-hash" data-git-copy-hash=full_for_click.clone() on:click=move |_| {
                                    // Shared helper awaits the write: "Copied" only
                                    // once the browser accepted it (#1427).
                                    let full = full_for_click.clone();
                                    spawn_local(async move {
                                        match crate::clipboard::copy_text(&full).await {
                                            Ok(()) => {
                                                copied_hash.set(full.clone());
                                                status_line.set("Copied commit hash.".into());
                                                crate::clipboard::after_ms(2_000, move || {
                                                    if copied_hash.try_get_untracked().as_deref() == Some(full.as_str()) {
                                                        copied_hash.set(String::new());
                                                    }
                                                });
                                            }
                                            Err(e) => status_line.set(e),
                                        }
                                    });
                                }>{move || if copied_hash.get() == full_for_label { "Copied" } else { "Copy" }}</button>
                            </li>
                        }
                    }).collect_view()}
                </ul>
                <Show when=move || commits.get().is_empty()>
                    <p class="hint">"No commits yet."</p>
                </Show>
                <Show when=move || next_log_limit(log_limit.get(), commits.get().len()).is_some()>
                    <div class="git-actions">
                        <button type="button" class="secondary" data-git-log-more on:click=show_more_log>"Show more commits"</button>
                    </div>
                </Show>
                <Show when=move || log_end_note(log_limit.get(), commits.get().len()).is_some()>
                    <p class="hint">{move || log_end_note(log_limit.get(), commits.get().len()).unwrap_or_default()}</p>
                </Show>
                <Show when=move || !show_note.get().is_empty()>
                    <p class="hint" role="alert">{move || show_note.get()}</p>
                </Show>
                {move || shown.get().map(|c| {
                    let notes = commit_detail_notes(c.parents.len(), c.truncated, c.files.len(), c.files_total);
                    let meta = format!("{} · {}", c.author, c.date);
                    let body = c.body.clone();
                    let files = c.files.clone();
                    let no_files = files.is_empty();
                    let patch = c.patch.clone();
                    let total = patch.lines().count();
                    view! {
                        <section class="git-commit-detail" data-git-commit-detail=c.hash.clone() aria-label="Commit detail">
                            <h3 class="git-section">{c.subject.clone()}</h3>
                            <span class="path" title=c.hash.clone()>{c.hash.clone()}</span>
                            <span class="path">{meta}</span>
                            {(!body.is_empty()).then(|| view! { <pre class="git-commit-body">{body}</pre> })}
                            {notes.into_iter().map(|n| view! { <p class="hint">{n}</p> }).collect_view()}
                            <ul class="plain git-commit-files" aria-label="Changed files">
                                {files.into_iter().map(|f| {
                                    let stat = show_file_stat(&f);
                                    view! {
                                        <li class="git-file">
                                            <span class="name">{f.path}</span>
                                            <span class="git-commit-stat">{stat}</span>
                                        </li>
                                    }
                                }).collect_view()}
                            </ul>
                            {no_files.then(|| view! { <p class="hint">"No file changes."</p> })}
                            <pre class="git-diff" aria-label="Commit patch">{move || diff_lines_view(&patch, show_expanded.get())}</pre>
                            {move || (total > DIFF_RENDER_LINES && !show_expanded.get()).then(|| view! {
                                <button type="button" class="secondary git-diff-expand" on:click=move |_| show_expanded.set(true)>
                                    {format!("Show all {total} lines")}
                                </button>
                            })}
                        </section>
                    }
                })}
            </Show>
            <input class="git-field git-filter" data-git-filter type="text" aria-label="Filter changed files by path" placeholder="Filter changed files by path…" prop:value=move || file_filter.get() on:input=move |ev| {
                let query = event_target_value(&ev);
                file_filter.set(query.clone());
                // Keep the cursor on a visible original index (#1435);
                // with no match, selected_row() refuses every action.
                if let Some(i) = snap_cursor(&rows.get_untracked(), &query, cursor.get_untracked()) {
                    select_row(i);
                } else {
                    // Nothing visible: close a diff whose row is hidden.
                    open_diff.set(false);
                    diff_expanded.set(false);
                    diff_text.set(String::new());
                    diff_truncated.set(false);
                }
            }/>
            {move || {
                let rows = rows.get();
                let cur = cursor.get();
                let query = file_filter.get();
                let filtering = !query.trim().is_empty();
                let mut views = Vec::new();
                for section in ["Staged", "Unstaged", "Untracked"] {
                    let total = rows.iter().filter(|row| row.section == section).count();
                    let matched: Vec<_> = rows.iter().enumerate().filter(|(_, row)| row.section == section && path_matches_query(&row.path, &query)).collect();
                    let heading = if filtering {
                        format!("{section} ({} of {total})", matched.len())
                    } else {
                        section.to_string()
                    };
                    views.push(view! { <h3 class="git-section">{heading}</h3> }.into_any());
                    if matched.is_empty() {
                        let empty = if filtering {
                            "No files match."
                        } else {
                            match section {
                                "Staged" => "No staged files.",
                                "Unstaged" => "No unstaged changes.",
                                _ => "No untracked files.",
                            }
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
                    <div class="git-diff-bar">
                        <button type="button" class="secondary git-copy-diff" disabled=move || !crate::clipboard::diff_copyable(&diff_text.get()) on:click=copy_diff>"Copy diff"</button>
                    </div>
                    // #1428: render-only cap. The full text is already
                    // client-side; expansion renders every line with the
                    // same line-kind classes as the capped render.
                    <pre class="git-diff" aria-label="Diff">{move || diff_lines_view(&diff_text.get(), diff_expanded.get())}</pre>
                    {move || {
                        let total = diff_text.get().lines().count();
                        (total > DIFF_RENDER_LINES && !diff_expanded.get()).then(|| view! {
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
                <button type="button" class="secondary" data-git-stage-all=true disabled=move || paths_where(&rows.get(), false).is_empty() on:click=stage_all>"Stage all"</button>
                <button type="button" class="secondary" data-git-unstage-all=true disabled=move || paths_where(&rows.get(), true).is_empty() on:click=unstage_all>"Unstage all"</button>
            </div>
            <section class="git-stash" data-git-stash="true" aria-label="Stashes">
                <h3 class="git-section">"Stashes"</h3>
                <input
                    class="git-field"
                    type="text"
                    data-git-stash-message="true"
                    aria-label="Stash message (optional)"
                    placeholder="Stash message (optional)"
                    prop:value=move || stash_msg.get()
                    on:input=move |ev| stash_msg.set(event_target_value(&ev))
                />
                <div class="git-actions">
                    <label class="git-stash-untracked">
                        <input
                            type="checkbox"
                            data-git-stash-untracked="true"
                            prop:checked=move || stash_untracked.get()
                            on:change=move |ev| stash_untracked.set(event_target_checked(&ev))
                        />
                        "Include untracked"
                    </label>
                    <button
                        type="button"
                        class="secondary"
                        data-git-stash-push="true"
                        disabled=move || !stash_enabled(&rows.get(), stash_untracked.get())
                        on:click=move |_| do_stash()
                    >"Stash changes"</button>
                </div>
                <Show when=move || !stash_note.get().is_empty()>
                    <p class="hint" role="alert" data-git-stash-note="true">{move || stash_note.get()}</p>
                </Show>
                <Show when=move || stashes.get().is_empty()>
                    <p class="hint">"No stashes."</p>
                </Show>
                <ul class="plain">
                    {move || {
                        let now = js_sys::Date::now();
                        stashes.get().into_iter().map(|stash| {
                            let age = relative_age(js_sys::Date::parse(&stash.date), now);
                            let meta = format!("{} · {}", stash.r#ref, age);
                            let message = stash.message.clone();
                            let open_hash = stash.hash.clone();
                            let row_hash = stash.hash.clone();
                            let (s1, s2, s3, s4) = (stash.clone(), stash.clone(), stash.clone(), stash);
                            view! {
                                <li class="wt-card git-stash-row" data-git-stash-row=row_hash>
                                    <span class="name">{message}</span>
                                    <span class="path">{meta}</span>
                                    <div class="git-actions">
                                        <button
                                            type="button"
                                            class="secondary"
                                            aria-pressed=move || (stash_open.get() == open_hash).to_string()
                                            on:click=move |_| toggle_stash_preview(s1.clone())
                                        >"Preview"</button>
                                        <button type="button" class="secondary" data-git-stash-apply="true" on:click=move |_| do_stash_apply(s2.clone(), false)>"Apply"</button>
                                        <button type="button" class="secondary" title="Apply, then drop the stash if it applied cleanly" on:click=move |_| do_stash_apply(s3.clone(), true)>"Pop (apply + drop)"</button>
                                        <button type="button" class="secondary" on:click=move |_| do_stash_drop(s4.clone())>"Drop…"</button>
                                    </div>
                                </li>
                            }
                        }).collect_view()
                    }}
                </ul>
                <Show when=move || !stash_open.get().is_empty() && stash_diff_loaded.get() && stash_diff.get().is_empty()>
                    <p class="hint">"This stash has no changes to preview."</p>
                </Show>
                <Show when=move || !stash_diff.get().is_empty()>
                    <pre class="git-diff" aria-label="Stash diff" data-git-stash-diff="true">{move || diff_lines_view(&stash_diff.get(), false)}</pre>
                    {move || {
                        let total = stash_diff.get().lines().count();
                        (total > DIFF_RENDER_LINES).then(|| view! { <p class="hint">{format!("Showing the first {DIFF_RENDER_LINES} of {total} lines.")}</p> })
                    }}
                    <Show when=move || stash_diff_truncated.get()>
                        <p class="hint">"Diff truncated by the daemon — showing the first 200 KB."</p>
                    </Show>
                </Show>
            </section>
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

#[cfg(test)]
mod tests {
    use super::*;

    fn stash_row(section: &str) -> GitRow {
        GitRow {
            section: section.into(),
            path: "p".into(),
            status: String::new(),
            added: 0,
            deleted: 0,
            staged: section == "Staged",
            inline_diff: String::new(),
        }
    }

    #[test]
    fn stash_enabled_only_when_git_would_stash_something() {
        assert!(!stash_enabled(&[], false));
        assert!(!stash_enabled(&[], true));
        assert!(stash_enabled(&[stash_row("Unstaged")], false));
        assert!(stash_enabled(&[stash_row("Staged")], false));
        // Untracked-only trees need the opt-in (#1471 Q1).
        assert!(!stash_enabled(&[stash_row("Untracked")], false));
        assert!(stash_enabled(&[stash_row("Untracked")], true));
    }

    #[test]
    fn relative_age_buckets() {
        let now = 1_000_000_000_000.0;
        assert_eq!(relative_age(now - 5_000.0, now), "just now");
        assert_eq!(relative_age(now - 5.0 * 60_000.0, now), "5m ago");
        assert_eq!(relative_age(now - 3.0 * 3_600_000.0, now), "3h ago");
        assert_eq!(relative_age(now - 2.0 * 86_400_000.0, now), "2d ago");
        // Clock skew never shows a negative age; unparseable dates are blank.
        assert_eq!(relative_age(now + 60_000.0, now), "just now");
        assert_eq!(relative_age(f64::NAN, now), "");
    }

    #[test]
    fn stash_body_pins_hash_and_confirms_only_drop() {
        let stash = GitStash {
            index: 2,
            r#ref: "stash@{2}".into(),
            hash: "abcdef1234567".into(),
            message: "m".into(),
            date: String::new(),
        };
        let apply = stash_body(&stash, None);
        assert_eq!(apply["index"], 2);
        assert_eq!(apply["hash"], "abcdef1234567");
        assert!(apply.get("confirm").is_none());
        assert_eq!(stash_body(&stash, Some("drop-stash"))["confirm"], "drop-stash");
    }

    #[test]
    fn stash_list_payload_decodes_daemon_shape() {
        let raw = r#"{"stashes":[{"index":0,"ref":"stash@{0}","hash":"h1","message":"WIP on main: x","date":"2026-01-01T00:00:00Z"}]}"#;
        let got: GitStashes = serde_json::from_str(raw).expect("decode");
        assert_eq!(got.stashes[0].r#ref, "stash@{0}");
        assert_eq!(got.stashes[0].message, "WIP on main: x");
    }

    fn branch(name: &str, current: bool) -> GitBranch {
        GitBranch { name: name.into(), current }
    }

    fn changes(total: usize, shown: usize, files: usize) -> BranchChanges {
        BranchChanges {
            state: "ok".into(),
            branch: "feature".into(),
            base: "main".into(),
            base_detected: true,
            merge_base: "0123456789abcdef".into(),
            commits: (0..shown)
                .map(|i| GitCommit {
                    hash: format!("{i:040}"),
                    subject: "s".into(),
                    author: "a".into(),
                    date: "d".into(),
                })
                .collect(),
            commits_total: total,
            files: (0..files)
                .map(|i| BranchFile { path: format!("f{i}"), added: 1, deleted: 0, binary: false })
                .collect(),
        }
    }

    #[test]
    fn branch_state_messages_name_every_empty_state() {
        assert_eq!(branch_state_message("ok", "feature", "main"), None);
        for state in ["on-base", "no-merge-base", "detached", "no-commits", "choose-base", "weird"] {
            let msg = branch_state_message(state, "feature", "main").expect(state);
            assert!(!msg.is_empty(), "{state}");
        }
        assert!(branch_state_message("on-base", "main", "main").unwrap().contains("main"));
        assert!(branch_state_message("choose-base", "x", "").unwrap().contains("choose a base"));
    }

    #[test]
    fn branch_summary_states_counts_even_when_capped() {
        assert_eq!(
            branch_summary(&changes(2, 2, 3)),
            "2 commits · 3 files vs main (merge-base 0123456)"
        );
        assert_eq!(
            branch_summary(&changes(120, 30, 1)),
            "120 commits (showing 30) · 1 files vs main (merge-base 0123456)"
        );
    }

    #[test]
    fn base_options_exclude_current_and_include_remote_base() {
        let branches = vec![branch("main", false), branch("feature", true), branch("dev", false)];
        assert_eq!(base_options(&branches, "feature", "main"), vec!["main", "dev"]);
        assert_eq!(
            base_options(&branches, "feature", "origin/main"),
            vec!["origin/main", "main", "dev"]
        );
        assert_eq!(base_options(&branches, "feature", ""), vec!["main", "dev"]);
    }

    #[test]
    fn branch_changes_payload_decodes_daemon_shape() {
        let raw = r#"{"state":"ok","branch":"feature","base":"main","baseDetected":true,"mergeBase":"abc","commits":[{"hash":"h","subject":"s","author":"a","date":"d"}],"commitsTotal":5,"files":[{"path":"x","added":1,"deleted":2,"binary":false}]}"#;
        let got: BranchChanges = serde_json::from_str(raw).expect("decode");
        assert!(got.base_detected);
        assert_eq!(got.commits_total, 5);
        assert_eq!(got.files[0].deleted, 2);
        assert_eq!(branch_file_stat(&got.files[0]), "+1 -2");
        let bin = BranchFile { path: "p".into(), added: 0, deleted: 0, binary: true };
        assert_eq!(branch_file_stat(&bin), "binary");
    }

    #[test]
    fn commit_click_opens_then_toggles_closed() {
        assert_eq!(commit_click_target("", "abc").as_deref(), Some("abc"));
        assert_eq!(commit_click_target("abc", "abc"), None);
        // Choosing another commit replaces the open one.
        assert_eq!(commit_click_target("abc", "def").as_deref(), Some("def"));
    }

    #[test]
    fn commit_detail_notes_are_honest_about_merges_and_truncation() {
        assert!(commit_detail_notes(1, false, 3, 3).is_empty());
        assert!(commit_detail_notes(0, false, 0, 0).is_empty());
        let merge = commit_detail_notes(2, false, 1, 1);
        assert_eq!(merge.len(), 1);
        assert!(merge[0].contains("first parent"));
        let all = commit_detail_notes(2, true, 500, 1640);
        assert_eq!(all.len(), 3);
        assert_eq!(all[1], "Showing 500 of 1640 changed files.");
        assert!(all[2].contains("truncated"));
    }

    #[test]
    fn show_file_stat_labels_binary_and_text() {
        let text = GitShowFile { path: "a.rs".into(), added: 3, deleted: 1, binary: false };
        assert_eq!(show_file_stat(&text), "+3 -1");
        let bin = GitShowFile { path: "a.png".into(), added: 0, deleted: 0, binary: true };
        assert_eq!(show_file_stat(&bin), "binary");
    }

    #[test]
    fn git_show_payload_decodes_daemon_shape() {
        let raw = r#"{"hash":"0123456789abcdef0123456789abcdef01234567","subject":"s","body":"b","author":"A","date":"2026-01-01T00:00:00Z","parents":["p1","p2"],"files":[{"path":"x.rs","added":1,"deleted":2,"binary":false}],"patch":"+x","truncated":true}"#;
        let got: GitShow = serde_json::from_str(raw).expect("decode");
        assert_eq!(got.parents.len(), 2);
        assert_eq!(got.files[0].path, "x.rs");
        assert!(got.truncated);
        assert_eq!(got.body, "b");
    }

    fn row(section: &str, path: &str, staged: bool) -> GitRow {
        GitRow {
            section: section.into(),
            path: path.into(),
            status: String::new(),
            added: 0,
            deleted: 0,
            staged,
            inline_diff: String::new(),
        }
    }

    fn sample_rows() -> Vec<GitRow> {
        vec![
            row("Unstaged", "webui/src/git.rs", false),
            row("Unstaged", "README.md", false),
            row("Staged", "webui/src/main.rs", true),
            row("Untracked", "docs/guide.md", false),
        ]
    }

    #[test]
    fn next_log_limit_full_page_below_max_steps_to_max() {
        assert_eq!(next_log_limit(30, 30), Some(LOG_LIMIT_MAX));
    }

    #[test]
    fn next_log_limit_at_max_is_none() {
        assert_eq!(next_log_limit(LOG_LIMIT_MAX, LOG_LIMIT_MAX), None);
    }

    #[test]
    fn next_log_limit_short_page_is_none() {
        assert_eq!(next_log_limit(30, 12), None);
        assert_eq!(next_log_limit(LOG_LIMIT_MAX, 87), None);
    }

    #[test]
    fn log_end_note_states_ceiling_and_short_page() {
        assert_eq!(log_end_note(30, 30), None);
        assert_eq!(
            log_end_note(LOG_LIMIT_MAX, LOG_LIMIT_MAX).as_deref(),
            Some("Showing the last 100 commits.")
        );
        assert_eq!(
            log_end_note(30, 12).as_deref(),
            Some("End of history — showing all 12 commits.")
        );
        assert_eq!(log_end_note(30, 0), None);
    }

    #[test]
    fn path_filter_is_case_insensitive_full_path_substring() {
        assert!(path_matches_query("WebUI/src/Git.rs", "webui/"));
        assert!(path_matches_query("webui/src/git.rs", "GIT.RS"));
        assert!(path_matches_query("anything", "  "));
        assert!(!path_matches_query("README.md", "webui/"));
    }

    #[test]
    fn filtered_indices_preserve_original_indices() {
        let rows = sample_rows();
        // The load-bearing #1435 property: staging from the filtered
        // list must act on original index 2, not visible position 0.
        assert_eq!(filtered_row_indices(&rows, "main.rs"), vec![2]);
        assert_eq!(filtered_row_indices(&rows, "webui/"), vec![0, 2]);
        assert_eq!(filtered_row_indices(&rows, ""), vec![0, 1, 2, 3]);
        assert!(filtered_row_indices(&rows, "zz-none").is_empty());
    }

    #[test]
    fn filtered_cursor_steps_through_visible_original_indices() {
        let rows = sample_rows();
        assert_eq!(filtered_cursor_step(&rows, "webui/", 0, 1), Some(2));
        assert_eq!(filtered_cursor_step(&rows, "webui/", 2, 1), Some(2));
        assert_eq!(filtered_cursor_step(&rows, "webui/", 2, -1), Some(0));
        // Cursor on a hidden row: Down lands on the next visible row.
        assert_eq!(filtered_cursor_step(&rows, "webui/", 1, 1), Some(2));
        // Cursor past the last visible row clamps, never wraps to 0.
        assert_eq!(filtered_cursor_step(&rows, "webui/", 3, 1), Some(2));
        assert_eq!(filtered_cursor_step(&rows, "webui/", 3, -1), Some(2));
        assert_eq!(filtered_cursor_step(&rows, "zz-none", 0, 1), None);
        // Unfiltered behaviour is the old clamp.
        assert_eq!(filtered_cursor_step(&rows, "", 0, 1), Some(1));
        assert_eq!(filtered_cursor_step(&rows, "", 3, 1), Some(3));
    }

    #[test]
    fn selected_row_refuses_hidden_and_no_match_cursor() {
        let rows = sample_rows();
        // No match: nothing visible, so no action target.
        assert!(selected_row(&rows, "zz", 1).is_none());
        assert_eq!(snap_cursor(&rows, "zz", 1), None);
        // A cursor on a hidden row is not a target either.
        assert!(selected_row(&rows, "webui/", 1).is_none());
        assert_eq!(
            selected_row(&rows, "webui/", 2).map(|r| r.path).as_deref(),
            Some("webui/src/main.rs")
        );
        assert_eq!(
            selected_row(&rows, "", 1).map(|r| r.path).as_deref(),
            Some("README.md")
        );
    }

    #[test]
    fn snap_cursor_follows_reordered_rows_to_a_visible_row() {
        // After Stage, src/a.rs moves to index 0 and README.md to 1;
        // a stale cursor of 1 must snap off hidden README.md.
        let rows = vec![
            row("Staged", "src/a.rs", true),
            row("Unstaged", "README.md", false),
        ];
        assert_eq!(snap_cursor(&rows, "src", 1), Some(0));
        assert_eq!(snap_cursor(&rows, "src", 0), Some(0));
        assert_eq!(snap_cursor(&rows, "", 1), Some(1));
        // A hidden cursor before a visible row snaps forward.
        assert_eq!(snap_cursor(&sample_rows(), "webui/", 1), Some(2));
    }

    #[test]
    fn short_hash_still_seven_chars_full_hash_untouched() {
        let full = "0123456789abcdef0123456789abcdef01234567";
        assert_eq!(short_hash(full), "0123456");
        assert_eq!(full.len(), 40);
    }

    #[test]
    fn paths_where_splits_mixed_rows_in_panel_order() {
        let rows = vec![
            row("Staged", "a.rs", true),
            row("Unstaged", "b.rs", false),
            row("Staged", "c.rs", true),
            row("Untracked", "new.rs", false),
        ];
        assert_eq!(paths_where(&rows, true), vec!["a.rs", "c.rs"]);
        // Stage all includes Untracked rows (#1426 Q3).
        assert_eq!(paths_where(&rows, false), vec!["b.rs", "new.rs"]);
    }

    #[test]
    fn paths_where_empty_sets_disable_the_batch_buttons() {
        let none: Vec<GitRow> = Vec::new();
        assert!(paths_where(&none, true).is_empty());
        assert!(paths_where(&none, false).is_empty());
        let only_staged = vec![row("Staged", "a.rs", true)];
        assert!(paths_where(&only_staged, false).is_empty());
        let only_unstaged = vec![row("Unstaged", "b.rs", false)];
        assert!(paths_where(&only_unstaged, true).is_empty());
    }

    #[test]
    fn http_message_extracts_the_daemon_error_field() {
        // The exact transport string from #1519.
        let err = r#"HTTP 400 {"error":"git commit: exit status 1"}"#;
        assert_eq!(http_message(err), "git commit: exit status 1");
        // No raw HTTP status or JSON braces survive.
        let shown = http_message(err);
        assert!(!shown.contains("HTTP"));
        assert!(!shown.contains('{'));
    }

    #[test]
    fn http_message_passes_plain_errors_through() {
        assert_eq!(
            http_message("unauthorized: check the daemon token"),
            "unauthorized: check the daemon token"
        );
        assert_eq!(http_message("HTTP 500"), "HTTP 500");
        // An empty error field is not a plain-language message.
        assert_eq!(
            http_message(r#"HTTP 400 {"error":""}"#),
            r#"HTTP 400 {"error":""}"#
        );
    }

    #[test]
    fn commit_block_preserves_the_message_on_every_block() {
        // Empty message: blocked, and there is nothing to lose.
        assert_eq!(
            commit_block("", false, 3),
            Some("Commit message is required.")
        );
        // #1519's common failure, caught client-side: nothing staged.
        assert_eq!(
            commit_block("do not lose me: fix the thing", false, 0),
            Some("Nothing is staged. Stage a file first, then commit.")
        );
        // A targeted (per-file) commit names its paths, so zero staged
        // files in the panel do not block it.
        assert_eq!(commit_block("fix the thing", true, 0), None);
        // The happy path runs; clearing happens only in the success
        // callback, never on a block or a failed POST.
        assert_eq!(commit_block("fix the thing", false, 1), None);
    }
}
