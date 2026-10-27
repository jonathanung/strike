//! Pure session-list layout. The daemon owns pins, archive, and folders.
//! Unread is [`unread_flag`]: a server bit wins when the status stream
//! (#1263) sends one. Until then the browser seen-map is the shim.
//! [`status_shim`] is the same kind of stand-in for the shared status
//! component that stream will land.

use std::collections::{HashMap, HashSet};

use crate::model::{fmt_ago, session_label};

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SourceSession {
    pub id: String,
    pub title: String,
    pub preview: String,
    pub snippet: String,
    pub mtime: i64,
    pub busy: bool,
    pub live: bool,
    pub pinned: bool,
    pub archived: bool,
    pub unread: bool,
    pub folder_id: String,
    pub team_state: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ChildIn {
    pub id: String,
    pub parent_id: String,
    pub title: String,
    pub state: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FolderIn {
    pub id: String,
    pub name: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LayoutKind {
    Folder,
    Session,
    Child,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LayoutItem {
    pub kind: LayoutKind,
    pub id: String,
    pub open_id: String,
    pub live: bool,
    pub label: String,
    pub subtitle: String,
    pub when: String,
    pub status_kind: String,
    pub pinned: bool,
    pub archived: bool,
    pub selectable: bool,
    pub slot: Option<u8>,
    pub depth: u8,
    pub count: usize,
    pub collapsed: bool,
    pub nav: Option<usize>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Layout {
    pub items: Vec<LayoutItem>,
    pub hidden_older: usize,
}

#[derive(Clone, Copy)]
pub struct AssembleArgs<'a> {
    pub sessions: &'a [SourceSession],
    pub children: &'a [ChildIn],
    pub folders: &'a [FolderIn],
    pub collapsed: &'a HashSet<String>,
    pub content_ids: &'a HashSet<String>,
    pub filter: &'a str,
    pub query: &'a str,
    pub conductor: bool,
    pub active: &'a str,
    pub now_s: i64,
    pub older_days: i64,
    pub show_older: bool,
}

/// Display title. A stored id, or the id's own hex prefix, is not a title.
pub fn human_title(meta: &str, preview: &str, id: &str) -> String {
    let labeled = session_label(meta, "");
    if !labeled.is_empty() && !title_is_raw_id(&labeled, id) {
        return labeled;
    }
    let preview = preview.trim();
    if !preview.is_empty() && !title_is_raw_id(preview, id) {
        return clip_chars(preview, 72);
    }
    "Untitled session".into()
}

pub fn title_is_raw_id(title: &str, id: &str) -> bool {
    let title = title.trim();
    let id = id.trim();
    if title.is_empty() || (!id.is_empty() && title == id) {
        return true;
    }
    if looks_like_session_id(title) {
        return true;
    }
    if !id.is_empty() && id.starts_with(title) && title.len() >= 6 && hexish_with_digit(title) {
        return true;
    }
    title.len() >= 8 && hexish_with_digit(title)
}

fn looks_like_session_id(s: &str) -> bool {
    let b = s.as_bytes();
    if b.len() < 16 || b[8] != b'T' {
        return false;
    }
    b[..8].iter().all(|c| c.is_ascii_digit()) && s.contains('Z')
}

fn hexish_with_digit(s: &str) -> bool {
    let mut digit = false;
    for c in s.chars() {
        match c {
            '0'..='9' => digit = true,
            'a'..='f' | 'A'..='F' | '-' => {}
            _ => return false,
        }
    }
    digit
}

fn clip_chars(s: &str, max: usize) -> String {
    if s.chars().count() <= max {
        return s.to_string();
    }
    let mut out: String = s.chars().take(max - 1).collect();
    out.push('…');
    out
}

/// Server unread wins. `None` keeps the seen-map shim (#1263 swap point).
pub fn unread_flag(server: Option<bool>, id: &str, active: &str, mtime: i64, seen_at: i64) -> bool {
    if id == active {
        return false;
    }
    if let Some(v) = server {
        return v;
    }
    mtime > seen_at && mtime > 0
}

/// Local stand-in for the shared status component. Kinds match the gutter
/// classes the list already paints.
pub fn status_shim(
    busy: bool,
    unread: bool,
    live: bool,
    team_state: &str,
) -> (&'static str, &'static str) {
    match team_state {
        "running" | "stalled" => ("running", "In progress"),
        "blocked" => ("blocked", "Blocked"),
        "failed" => ("failed", "Failed"),
        "done" => ("done", "Done"),
        "stopped" => ("stopped", "Stopped"),
        _ => {
            if busy {
                ("running", "In progress")
            } else if unread {
                ("unread", "Unread")
            } else if live {
                ("idle", "Idle")
            } else {
                ("saved", "Saved")
            }
        }
    }
}

pub fn is_older(mtime: i64, now_s: i64, days: i64) -> bool {
    if days <= 0 || mtime <= 0 || now_s <= 0 {
        return false;
    }
    now_s.saturating_sub(mtime) >= days.saturating_mul(86_400)
}

pub fn step_index(len: usize, cur: usize, delta: i32) -> usize {
    if len == 0 {
        return 0;
    }
    let cur = cur.min(len - 1) as i32;
    let mut next = cur + delta;
    let last = len as i32 - 1;
    if next < 0 {
        next = 0;
    }
    if next > last {
        next = last;
    }
    next as usize
}

pub fn alt_digit(key: &str) -> Option<usize> {
    match key {
        "1" => Some(0),
        "2" => Some(1),
        "3" => Some(2),
        "4" => Some(3),
        "5" => Some(4),
        "6" => Some(5),
        "7" => Some(6),
        "8" => Some(7),
        "9" => Some(8),
        _ => None,
    }
}

pub fn toggle_id(selected: &[String], id: &str) -> Vec<String> {
    if selected.iter().any(|s| s == id) {
        selected
            .iter()
            .filter(|s| s.as_str() != id)
            .cloned()
            .collect()
    } else {
        let mut out = selected.to_vec();
        out.push(id.to_string());
        out
    }
}

pub fn select_range(order: &[String], anchor: &str, to: &str) -> Vec<String> {
    let Some(a) = order.iter().position(|id| id == anchor) else {
        return vec![to.to_string()];
    };
    let Some(b) = order.iter().position(|id| id == to) else {
        return vec![to.to_string()];
    };
    let (lo, hi) = if a <= b { (a, b) } else { (b, a) };
    order[lo..=hi].to_vec()
}

pub fn assemble(args: AssembleArgs) -> Layout {
    let q = args.query.trim().to_ascii_lowercase();
    let searching = !q.is_empty();
    let filtering = !matches!(args.filter, "all" | "");
    let force_open = searching || filtering;

    let mut matched: Vec<&SourceSession> = args
        .sessions
        .iter()
        .filter(|row| chip_ok(row, args.filter))
        .filter(|row| query_ok(row, &args, &q))
        .collect();
    matched.sort_by(|a, b| {
        b.pinned
            .cmp(&a.pinned)
            .then(b.mtime.cmp(&a.mtime))
            .then(a.id.cmp(&b.id))
    });

    let mut hidden_older = 0usize;
    let mut shown = Vec::new();
    for row in matched {
        let keep = args.show_older
            || searching
            || !is_older(row.mtime, args.now_s, args.older_days)
            || row.pinned
            || row.busy
            || row.id == args.active;
        if keep {
            shown.push(row);
        } else {
            hidden_older += 1;
        }
    }

    let kids = index_children(args.children);
    let mut items = Vec::new();
    let mut slot: u8 = 0;
    let mut nav: usize = 0;

    let mut by_folder: HashMap<String, Vec<&SourceSession>> = HashMap::new();
    let mut unfiled = Vec::new();
    for row in shown {
        let known = !row.folder_id.is_empty() && args.folders.iter().any(|f| f.id == row.folder_id);
        if known {
            by_folder
                .entry(row.folder_id.clone())
                .or_default()
                .push(row);
        } else {
            unfiled.push(row);
        }
    }

    for folder in args.folders {
        let rows = by_folder.remove(&folder.id).unwrap_or_default();
        if rows.is_empty() && force_open {
            continue;
        }
        let collapsed = !force_open && args.collapsed.contains(&folder.id);
        items.push(folder_item(&folder.id, &folder.name, rows.len(), collapsed));
        if !collapsed {
            for row in rows {
                push_session(&mut items, &mut slot, &mut nav, row, &kids, args.conductor);
            }
        }
    }
    if !unfiled.is_empty() || (!args.folders.is_empty() && !force_open) {
        if !args.folders.is_empty() {
            items.push(folder_item("", "Ungrouped", unfiled.len(), false));
        }
        for row in unfiled {
            push_session(&mut items, &mut slot, &mut nav, row, &kids, args.conductor);
        }
    }

    Layout {
        items,
        hidden_older,
    }
}

fn chip_ok(row: &SourceSession, filter: &str) -> bool {
    match filter {
        "unread" => !row.archived && row.unread,
        "progress" => {
            !row.archived
                && (row.busy || row.team_state == "running" || row.team_state == "stalled")
        }
        "pinned" => row.pinned,
        "archived" => row.archived,
        _ => !row.archived,
    }
}

fn query_ok(row: &SourceSession, args: &AssembleArgs, q: &str) -> bool {
    if q.is_empty() {
        return true;
    }
    if args.content_ids.contains(&row.id) {
        return true;
    }
    let title = row.title.to_ascii_lowercase();
    let preview = row.preview.to_ascii_lowercase();
    let id = row.id.to_ascii_lowercase();
    if title.contains(q) || preview.contains(q) || id.contains(q) {
        return true;
    }
    args.children.iter().any(|child| {
        if !descendant_of(child, &row.id, args.children) {
            return false;
        }
        args.content_ids.contains(&child.id)
            || child.title.to_ascii_lowercase().contains(q)
            || child.id.to_ascii_lowercase().contains(q)
    })
}

fn descendant_of(child: &ChildIn, root: &str, all: &[ChildIn]) -> bool {
    let mut parent = child.parent_id.as_str();
    if parent == root {
        return true;
    }
    for _ in 0..6 {
        if parent == root {
            return true;
        }
        let Some(next) = all.iter().find(|c| c.id == parent) else {
            return false;
        };
        parent = next.parent_id.as_str();
    }
    false
}

fn index_children(children: &[ChildIn]) -> HashMap<String, Vec<&ChildIn>> {
    let mut sorted: Vec<&ChildIn> = children.iter().collect();
    sorted.sort_by(|a, b| {
        let at = human_title(&a.title, "", &a.id).to_ascii_lowercase();
        let bt = human_title(&b.title, "", &b.id).to_ascii_lowercase();
        at.cmp(&bt).then(a.id.cmp(&b.id))
    });
    let mut out: HashMap<String, Vec<&ChildIn>> = HashMap::new();
    for child in sorted {
        out.entry(child.parent_id.clone()).or_default().push(child);
    }
    out
}

fn folder_item(id: &str, name: &str, count: usize, collapsed: bool) -> LayoutItem {
    LayoutItem {
        kind: LayoutKind::Folder,
        id: id.to_string(),
        open_id: String::new(),
        live: false,
        label: name.to_string(),
        subtitle: String::new(),
        when: String::new(),
        status_kind: String::new(),
        pinned: false,
        archived: false,
        selectable: false,
        slot: None,
        depth: 0,
        count,
        collapsed,
        nav: None,
    }
}

fn push_session(
    items: &mut Vec<LayoutItem>,
    slot: &mut u8,
    nav: &mut usize,
    row: &SourceSession,
    kids: &HashMap<String, Vec<&ChildIn>>,
    conductor: bool,
) {
    let n = *nav;
    *nav += 1;
    let alt = if *slot < 9 {
        *slot += 1;
        Some(*slot)
    } else {
        None
    };
    let (kind, label) = status_shim(row.busy, row.unread, row.live, &row.team_state);
    let subtitle = if !row.snippet.is_empty() {
        row.snippet.clone()
    } else if row.archived {
        format!("Archived · {label}")
    } else {
        label.to_string()
    };
    items.push(LayoutItem {
        kind: LayoutKind::Session,
        id: row.id.clone(),
        open_id: row.id.clone(),
        live: row.live,
        label: row.title.clone(),
        subtitle,
        when: String::new(),
        status_kind: kind.to_string(),
        pinned: row.pinned,
        archived: row.archived,
        selectable: true,
        slot: alt,
        depth: 0,
        count: 0,
        collapsed: false,
        nav: Some(n),
    });
    if conductor {
        push_children(items, nav, &row.id, &row.id, row.live, 1, kids);
    }
}

fn push_children(
    items: &mut Vec<LayoutItem>,
    nav: &mut usize,
    parent: &str,
    root: &str,
    root_live: bool,
    depth: u8,
    kids: &HashMap<String, Vec<&ChildIn>>,
) {
    if depth > 4 {
        return;
    }
    let Some(list) = kids.get(parent) else {
        return;
    };
    for child in list {
        let n = *nav;
        *nav += 1;
        let (sk, sl) = if child.state.is_empty() {
            ("delegated", "Delegated")
        } else {
            let (k, l) = status_shim(false, false, false, &child.state);
            if k == "saved" {
                ("delegated", "Delegated")
            } else {
                (k, l)
            }
        };
        items.push(LayoutItem {
            kind: LayoutKind::Child,
            id: child.id.clone(),
            open_id: root.to_string(),
            live: root_live,
            label: human_title(&child.title, "", &child.id),
            subtitle: sl.to_string(),
            when: String::new(),
            status_kind: sk.to_string(),
            pinned: false,
            archived: false,
            selectable: true,
            slot: None,
            depth,
            count: 0,
            collapsed: false,
            nav: Some(n),
        });
        push_children(items, nav, &child.id, root, root_live, depth + 1, kids);
    }
}

/// Stamp relative times after assemble. Kept out of [`assemble`] so tests
/// can ignore the clock, and so the panel can pass one `now`.
pub fn stamp_times(items: &mut [LayoutItem], mtimes: &HashMap<String, i64>, now_s: i64) {
    for item in items {
        if item.kind != LayoutKind::Session {
            continue;
        }
        let mtime = mtimes.get(&item.id).copied().unwrap_or(0);
        item.when = fmt_ago(mtime, now_s);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn session(id: &str, mtime: i64) -> SourceSession {
        SourceSession {
            id: id.into(),
            title: format!("Title {id}"),
            preview: String::new(),
            snippet: String::new(),
            mtime,
            busy: false,
            live: true,
            pinned: false,
            archived: false,
            unread: false,
            folder_id: String::new(),
            team_state: String::new(),
        }
    }

    fn args<'a>(
        sessions: &'a [SourceSession],
        children: &'a [ChildIn],
        folders: &'a [FolderIn],
        collapsed: &'a HashSet<String>,
        content: &'a HashSet<String>,
    ) -> AssembleArgs<'a> {
        AssembleArgs {
            sessions,
            children,
            folders,
            collapsed,
            content_ids: content,
            filter: "all",
            query: "",
            conductor: false,
            active: "active",
            now_s: 1_700_000_000,
            older_days: 7,
            show_older: false,
        }
    }

    #[test]
    fn human_title_hides_raw_ids() {
        let id = "20060102T150405.000000000Z-ab12cd34";
        assert_eq!(human_title("", "", id), "Untitled session");
        assert_eq!(human_title(id, "", id), "Untitled session");
        assert_eq!(human_title("ab12cd34", "", id), "Untitled session");
        assert_eq!(
            human_title(id, "Reload the session list", id),
            "Reload the session list"
        );
        assert_eq!(human_title("Fix login", "", id), "Fix login");
        assert_eq!(human_title("Reviews", "", id), "Reviews");
    }

    #[test]
    fn unread_shim_prefers_server_bit() {
        assert!(unread_flag(None, "a", "b", 50, 10));
        assert!(!unread_flag(None, "a", "a", 50, 10));
        assert!(!unread_flag(None, "a", "b", 50, 50));
        assert!(!unread_flag(Some(false), "a", "b", 50, 10));
        assert!(unread_flag(Some(true), "a", "b", 0, 0));
        assert!(!unread_flag(Some(true), "a", "a", 50, 0));
    }

    #[test]
    fn status_shim_maps_team_state() {
        assert_eq!(status_shim(false, false, true, "").0, "idle");
        assert_eq!(status_shim(true, false, true, "").0, "running");
        assert_eq!(status_shim(false, true, false, "").1, "Unread");
        assert_eq!(status_shim(false, false, false, "blocked").0, "blocked");
        assert_eq!(status_shim(true, false, true, "failed").0, "failed");
    }

    #[test]
    fn folds_by_age_keeps_pins_and_active() {
        let now = 1_700_000_000i64;
        let mut old = session("old", now - 10 * 86_400);
        let mut pin = session("pin", now - 30 * 86_400);
        pin.pinned = true;
        let active = session("active", now - 20 * 86_400);
        let fresh = session("fresh", now - 3600);
        old.unread = true;
        let sessions = vec![old, pin, active, fresh];
        let folders = Vec::new();
        let collapsed = HashSet::new();
        let content = HashSet::new();
        let mut a = args(&sessions, &[], &folders, &collapsed, &content);
        a.now_s = now;
        let layout = assemble(a);
        let ids: Vec<_> = layout
            .items
            .iter()
            .filter(|i| i.kind == LayoutKind::Session)
            .map(|i| i.id.as_str())
            .collect();
        assert_eq!(layout.hidden_older, 1);
        assert!(ids.contains(&"pin"));
        assert!(ids.contains(&"active"));
        assert!(ids.contains(&"fresh"));
        assert!(!ids.contains(&"old"));
        assert_eq!(ids[0], "pin");

        a.show_older = true;
        let shown = assemble(a);
        assert_eq!(shown.hidden_older, 0);
        assert_eq!(
            shown
                .items
                .iter()
                .filter(|i| i.kind == LayoutKind::Session)
                .count(),
            4
        );
    }

    #[test]
    fn archive_filter_and_folders_and_conductor() {
        let now = 1_700_000_000i64;
        let mut a = session("a", now - 100);
        let mut b = session("b", now - 90);
        b.archived = true;
        b.folder_id = "fld-1".into();
        a.folder_id = "fld-1".into();
        let mut c = session("c", now - 80);
        c.unread = true;
        let sessions = vec![a, b, c];
        let folders = vec![FolderIn {
            id: "fld-1".into(),
            name: "Reviews".into(),
        }];
        let children = vec![
            ChildIn {
                id: "kid".into(),
                parent_id: "a".into(),
                title: "scribe".into(),
                state: "running".into(),
            },
            ChildIn {
                id: "20060102T150405.000000000Z-ab12cd34".into(),
                parent_id: "a".into(),
                title: "20060102T150405.000000000Z-ab12cd34".into(),
                state: String::new(),
            },
        ];
        let collapsed = HashSet::new();
        let content = HashSet::new();
        let mut args = args(&sessions, &children, &folders, &collapsed, &content);
        args.conductor = true;
        let layout = assemble(args);
        let labels: Vec<_> = layout.items.iter().map(|i| i.label.as_str()).collect();
        assert!(labels.contains(&"Reviews"));
        assert!(labels.contains(&"Title a"));
        assert!(labels.contains(&"scribe"));
        assert!(labels.contains(&"Untitled session"));
        assert!(!labels.contains(&"Title b"));
        assert!(labels.contains(&"Ungrouped"));
        let kid = layout.items.iter().find(|i| i.id == "kid").unwrap();
        assert_eq!(kid.kind, LayoutKind::Child);
        assert_eq!(kid.open_id, "a");
        assert_eq!(kid.status_kind, "running");
        assert!(kid.slot.is_none());
        let parent = layout.items.iter().find(|i| i.id == "a").unwrap();
        assert_eq!(parent.slot, Some(1));
        assert_eq!(parent.nav, Some(0));
        assert_eq!(kid.nav, Some(1));

        args.filter = "archived";
        args.conductor = false;
        let archived = assemble(args);
        let ids: Vec<_> = archived
            .items
            .iter()
            .filter(|i| i.kind == LayoutKind::Session)
            .map(|i| i.id.as_str())
            .collect();
        assert_eq!(ids, vec!["b"]);
        assert!(archived
            .items
            .iter()
            .any(|i| i.subtitle.starts_with("Archived")));

        args.filter = "unread";
        let unread = assemble(args);
        let ids: Vec<_> = unread
            .items
            .iter()
            .filter(|i| i.kind == LayoutKind::Session)
            .map(|i| i.id.as_str())
            .collect();
        assert_eq!(ids, vec!["c"]);
    }

    #[test]
    fn search_opens_folders_and_matches_children() {
        let mut a = session("a", 1_700_000_000 - 100);
        a.folder_id = "fld-1".into();
        a.title = "Palette polish".into();
        let sessions = vec![a];
        let folders = vec![FolderIn {
            id: "fld-1".into(),
            name: "Reviews".into(),
        }];
        let mut collapsed = HashSet::new();
        collapsed.insert("fld-1".into());
        let children = vec![ChildIn {
            id: "kid".into(),
            parent_id: "a".into(),
            title: "check the tail token".into(),
            state: String::new(),
        }];
        let content = HashSet::new();
        let mut args = args(&sessions, &children, &folders, &collapsed, &content);
        args.query = "tail token";
        args.conductor = true;
        let layout = assemble(args);
        assert!(layout.items.iter().any(|i| i.id == "a"));
        assert!(layout.items.iter().any(|i| i.id == "kid"));
        assert!(layout
            .items
            .iter()
            .any(|i| i.kind == LayoutKind::Folder && i.id == "fld-1" && !i.collapsed));

        args.query = "nope";
        let none = assemble(args);
        assert!(none.items.iter().all(|i| i.kind != LayoutKind::Session));
    }

    #[test]
    fn keyboard_helpers() {
        assert_eq!(step_index(0, 0, 1), 0);
        assert_eq!(step_index(3, 0, -1), 0);
        assert_eq!(step_index(3, 2, 1), 2);
        assert_eq!(step_index(3, 1, 1), 2);
        assert_eq!(alt_digit("1"), Some(0));
        assert_eq!(alt_digit("9"), Some(8));
        assert_eq!(alt_digit("0"), None);
        assert_eq!(
            toggle_id(&["a".into()], "b"),
            vec!["a".to_string(), "b".into()]
        );
        assert_eq!(
            toggle_id(&["a".into(), "b".into()], "a"),
            vec!["b".to_string()]
        );
        let order = vec!["a".into(), "b".into(), "c".into()];
        assert_eq!(select_range(&order, "c", "a"), order);
    }
}
