//! Session list panel. Daemon routes under `/v1/session-list` own pins,
//! archive, and folders. Folders group sessions; they are not directories.
//!
//! The row Delete action (#1358) only arms the confirm dialog owned by
//! `crate::session_delete` through the `on_delete` prop; the panel never
//! issues a DELETE itself.
//!
//! The gutter is [`status_shim`] until #1263 lands the shared status
//! component. Unread uses the seen-map unless a server bit arrives.

use std::cell::RefCell;
use std::collections::{HashMap, HashSet};
use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde::Serialize;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{delete_json, get_json, js_encode, now_ms, post_json_value};
use crate::model::{is_running, merge_sessions, ActivityRow, RootSummary, SessionItem, Workspace};
use crate::session_list::{
    alt_digit, assemble, empty_folder_ids, human_title, select_range, stamp_times, step_index,
    still_empty_folder_ids, title_is_raw_id, toggle_id, unread_flag, AssembleArgs, ChildIn,
    FolderIn, Layout, LayoutKind, SourceSession,
};
use crate::shell::icon;
use crate::{load, load_string_list, save};

thread_local! {
    static KEY_HANDLER: RefCell<Option<Box<dyn FnMut(&web_sys::KeyboardEvent) -> bool>>> =
        RefCell::new(None);
}

#[derive(Clone, Debug)]
pub(crate) struct CursorSession {
    pub id: String,
    pub label: String,
    pub pinned: bool,
}

impl Default for CursorSession {
    fn default() -> Self {
        Self {
            id: String::new(),
            label: String::new(),
            pinned: false,
        }
    }
}

thread_local! {
    static CURSOR: RefCell<CursorSession> = RefCell::new(CursorSession::default());
    static FOLDERS: RefCell<Vec<(String, String)>> = RefCell::new(Vec::new());
    static SELECTED: RefCell<Vec<String>> = RefCell::new(Vec::new());
    static CLEANUP_ASK: RefCell<Option<Rc<dyn Fn()>>> = RefCell::new(None);
}

/// Run the mounted panel's empty-folder cleanup ask (#1566). The
/// callback lives in a thread-local because the panel's view closures
/// must be `Send` and an `Rc` callback is not; the FOLDERS/SELECTED
/// slots above are the same pattern.
fn ask_empty_folder_cleanup() {
    let ask = CLEANUP_ASK.with(|slot| slot.borrow().clone());
    if let Some(ask) = ask {
        ask();
    }
}

pub(crate) fn cursor_session() -> CursorSession {
    CURSOR.with(|slot| slot.borrow().clone())
}

pub(crate) fn folder_pairs() -> Vec<(String, String)> {
    FOLDERS.with(|slot| slot.borrow().clone())
}

pub(crate) fn selected_ids() -> Vec<String> {
    SELECTED.with(|slot| slot.borrow().clone())
}

pub(crate) fn dispatch_session_key(ev: &web_sys::KeyboardEvent) -> bool {
    KEY_HANDLER.with(|slot| {
        let mut guard = slot.borrow_mut();
        if let Some(handler) = guard.as_mut() {
            handler(ev)
        } else {
            false
        }
    })
}

fn set_key_handler(handler: Option<Box<dyn FnMut(&web_sys::KeyboardEvent) -> bool>>) {
    KEY_HANDLER.with(|slot| *slot.borrow_mut() = handler);
}

#[derive(Clone, Serialize, Deserialize)]
struct ViewPrefs {
    #[serde(default = "filter_all")]
    filter: String,
    #[serde(default = "days_seven")]
    older_days: i64,
    #[serde(default)]
    conductor: bool,
    #[serde(default)]
    collapsed: Vec<String>,
    #[serde(default)]
    show_older: bool,
}

fn filter_all() -> String {
    "all".into()
}
fn days_seven() -> i64 {
    7
}

impl Default for ViewPrefs {
    fn default() -> Self {
        Self {
            filter: filter_all(),
            older_days: days_seven(),
            conductor: false,
            collapsed: Vec::new(),
            show_older: false,
        }
    }
}

fn load_prefs() -> ViewPrefs {
    let mut prefs: ViewPrefs =
        serde_json::from_str(&load("strike.sessionList.view")).unwrap_or_default();
    if !matches!(
        prefs.filter.as_str(),
        "all" | "unread" | "needs-you" | "progress" | "pinned" | "archived"
    ) {
        prefs.filter = filter_all();
    }
    if !matches!(prefs.older_days, 0 | 1 | 7 | 14 | 30) {
        prefs.older_days = 7;
    }
    prefs
}

#[derive(Clone, Debug, Default, Deserialize)]
struct OrgWire {
    #[serde(default)]
    pinned: bool,
    #[serde(default)]
    archived: bool,
    #[serde(default, rename = "folderId")]
    folder_id: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct FolderWire {
    id: String,
    name: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct ChildWire {
    id: String,
    #[serde(default, rename = "parentId")]
    parent_id: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    preview: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct Snapshot {
    #[serde(default)]
    folders: Vec<FolderWire>,
    #[serde(default)]
    sessions: HashMap<String, OrgWire>,
    #[serde(default)]
    previews: HashMap<String, String>,
    #[serde(default)]
    titles: HashMap<String, String>,
    #[serde(default)]
    children: Vec<ChildWire>,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct SearchHit {
    id: String,
    #[serde(default, rename = "parentId")]
    parent_id: String,
    #[serde(default)]
    snippet: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct SearchResponse {
    #[serde(default)]
    hits: Vec<SearchHit>,
}

fn friendly(err: &str) -> String {
    if let Some(i) = err.find('{') {
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(&err[i..]) {
            if let Some(msg) = v.get("error").and_then(|e| e.as_str()) {
                if !msg.is_empty() {
                    return msg.to_string();
                }
            }
        }
    }
    err.to_string()
}

fn event_element(ev: &web_sys::KeyboardEvent) -> Option<web_sys::Element> {
    ev.target()?.dyn_into().ok()
}

fn class_has(el: &web_sys::Element, class: &str) -> bool {
    el.get_attribute("class")
        .unwrap_or_default()
        .split_whitespace()
        .any(|c| c == class)
}

fn inside_panel(el: &web_sys::Element) -> bool {
    let mut cur = Some(el.clone());
    while let Some(node) = cur {
        if class_has(&node, "ss-panel") {
            return true;
        }
        cur = node.parent_element();
    }
    false
}

fn is_typing(el: &web_sys::Element) -> bool {
    let tag = el.tag_name().to_ascii_lowercase();
    if tag == "textarea" || tag == "select" {
        return true;
    }
    if tag == "input" {
        let kind = el
            .get_attribute("type")
            .unwrap_or_default()
            .to_ascii_lowercase();
        return kind.is_empty() || kind == "text" || kind == "search";
    }
    false
}

/// True while the shell's command palette dialog is mounted.
fn palette_is_open() -> bool {
    web_sys::window()
        .and_then(|w| w.document())
        .and_then(|d| {
            d.query_selector("[data-nav=\"palette-dialog\"]")
                .ok()
                .flatten()
        })
        .is_some()
}

#[component]
pub(crate) fn SessionPanel(
    base: RwSignal<String>,
    token: RwSignal<String>,
    sessions: RwSignal<Vec<SessionItem>>,
    roots: RwSignal<Vec<RootSummary>>,
    activity: RwSignal<Vec<ActivityRow>>,
    active_root: RwSignal<String>,
    seen: RwSignal<HashMap<String, i64>>,
    workspaces: RwSignal<Vec<Workspace>>,
    on_open: impl Fn(String, bool) + Copy + Send + 'static,
    on_new: impl Fn() + Copy + Send + 'static,
    on_refresh: impl Fn() + Copy + Send + 'static,
    nav_order: RwSignal<Vec<(String, bool)>>,
) -> impl IntoView {
    let prefs = load_prefs();
    let filter = RwSignal::new(prefs.filter);
    let older_days = RwSignal::new(prefs.older_days);
    let conductor = RwSignal::new(prefs.conductor);
    let collapsed = RwSignal::new(prefs.collapsed);
    let show_older = RwSignal::new(prefs.show_older);
    let query = RwSignal::new(String::new());
    let snapshot = RwSignal::new(Snapshot::default());
    let hits = RwSignal::new(Vec::<SearchHit>::new());
    let list_error = RwSignal::new(String::new());
    let load_failed = RwSignal::new(false);
    let search_error = RwSignal::new(String::new());
    let cursor = RwSignal::new(0usize);
    let selected = RwSignal::new(Vec::<String>::new());
    let anchor = RwSignal::new(String::new());
    let action_note = RwSignal::new(String::new());
    let migrated = RwSignal::new(false);
    let search_gen = RwSignal::new(0u32);
    let reload_gen = RwSignal::new(0u32);
    let cleanup_busy = RwSignal::new(false);

    let reload = move || {
        // Reloads commit in start order, not arrival order (#1641): a
        // superseded snapshot must never paint over a newer list, and
        // only the newest snapshot may drive the one-time pin
        // migration — a stale one would POST its own pin verdict and
        // then wipe the local pin store for good.
        let n = reload_gen.get_untracked().wrapping_add(1);
        reload_gen.set(n);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            // Still current only while no newer reload has started and
            // the panel still points at the gateway this reload was
            // issued against (a gateway switch must not be painted with
            // the old gateway's sessions).
            let b_check = b.clone();
            let current =
                move || reload_gen.get_untracked() == n && base.get_untracked() == b_check;
            match get_json::<Snapshot>(&b, &t, "/v1/session-list").await {
                Ok(snap) => {
                    if !current() {
                        return;
                    }
                    let local = load_string_list("strike.pins");
                    let any_pin = snap.sessions.values().any(|org| org.pinned);
                    snapshot.set(snap);
                    if load_failed.get_untracked() {
                        load_failed.set(false);
                        list_error.set(String::new());
                    }
                    if !migrated.get_untracked() {
                        migrated.set(true);
                        if !any_pin && !local.is_empty() {
                            let body = serde_json::json!({
                                "ids": local,
                                "op": "pin",
                                "folderId": "",
                            });
                            if post_json_value(&b, &t, "/v1/session-list/bulk", body)
                                .await
                                .is_ok()
                            {
                                // A newer reload may have started while
                                // the POST was in flight; only a still
                                // current one may wipe the local pins.
                                if !current() {
                                    return;
                                }
                                save("strike.pins", "[]");
                                if let Ok(again) =
                                    get_json::<Snapshot>(&b, &t, "/v1/session-list").await
                                {
                                    if current() {
                                        snapshot.set(again);
                                    }
                                }
                            }
                        }
                    }
                }
                Err(e) => {
                    if !current() {
                        return;
                    }
                    load_failed.set(true);
                    list_error.set(friendly(&e));
                }
            }
        });
    };
    reload();

    Effect::new(move |_| {
        let prefs = ViewPrefs {
            filter: filter.get(),
            older_days: older_days.get(),
            conductor: conductor.get(),
            collapsed: collapsed.get(),
            show_older: show_older.get(),
        };
        if let Ok(raw) = serde_json::to_string(&prefs) {
            save("strike.sessionList.view", &raw);
        }
    });

    Effect::new(move |_| {
        let q = query.get();
        let n = search_gen.get_untracked().wrapping_add(1);
        search_gen.set(n);
        let trimmed = q.trim().to_string();
        if trimmed.is_empty() {
            hits.set(Vec::new());
            search_error.set(String::new());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let fire = Closure::once(move || {
            if search_gen.get_untracked() != n {
                return;
            }
            spawn_local(async move {
                let path = format!("/v1/session-list/search?q={}", js_encode(&trimmed));
                match get_json::<SearchResponse>(&b, &t, &path).await {
                    // Re-check the generation after the await, like the
                    // op_gen completions below (#1624): a slower earlier
                    // search must not overwrite the current query's hits
                    // or clear its error (nor paint its own stale error).
                    Ok(found) => {
                        if search_gen.get_untracked() != n {
                            return;
                        }
                        hits.set(found.hits);
                        search_error.set(String::new());
                    }
                    Err(e) => {
                        if search_gen.get_untracked() != n {
                            return;
                        }
                        search_error.set(friendly(&e));
                    }
                }
            });
        });
        if let Some(window) = web_sys::window() {
            let _ = window.set_timeout_with_callback_and_timeout_and_arguments_0(
                fire.as_ref().unchecked_ref(),
                250,
            );
        }
        fire.forget();
    });

    if let Some(window) = web_sys::window() {
        let tick = Closure::<dyn FnMut()>::new(move || reload());
        let handle = window
            .set_interval_with_callback_and_timeout_and_arguments_0(
                tick.as_ref().unchecked_ref(),
                4000,
            )
            .ok();
        tick.forget();
        on_cleanup(move || {
            if let Some(id) = handle {
                if let Some(window) = web_sys::window() {
                    window.clear_interval_with_handle(id);
                }
            }
        });
    }

    let layout = Memo::new(move |_| {
        let snap = snapshot.get();
        let sessions_now = sessions.get();
        let roots_now = roots.get();
        let activity_now = activity.get();
        let seen_now = seen.get();
        let hits_now = hits.get();
        let filter_now = filter.get();
        let query_now = query.get();
        let collapsed_now = collapsed.get();
        let active = active_root.get();
        let workspaces_now = workspaces.get();
        build_layout(
            &snap,
            &sessions_now,
            &roots_now,
            &activity_now,
            &seen_now,
            &hits_now,
            &workspaces_now,
            &filter_now,
            &query_now,
            &collapsed_now,
            &active,
            conductor.get(),
            older_days.get(),
            show_older.get(),
        )
    });

    // Publish the panel's flattened session order for edge-swipe
    // navigation (#1490 slice 5): session rows only, in display order,
    // as (open_id, live) pairs — the same rows j/k + Enter walk.
    Effect::new(move |_| {
        let order: Vec<(String, bool)> = layout
            .get()
            .items
            .iter()
            .filter(|item| item.nav.is_some() && item.kind == LayoutKind::Session)
            .map(|item| (item.open_id.clone(), item.live))
            .collect();
        if nav_order.get_untracked() != order {
            nav_order.set(order);
        }
    });

    let report = move |err: String| {
        if err.is_empty() {
            list_error.set(String::new());
        } else {
            list_error.set(friendly(&err));
        }
    };
    // A slower success must not clear an error from a later click.
    let op_gen = RwSignal::new(0u32);
    let start_op = move || {
        let n = op_gen.get_untracked().wrapping_add(1);
        op_gen.set(n);
        n
    };
    let finish_op = move |n: u32, err: String| {
        if op_gen.get_untracked() != n {
            return;
        }
        report(err);
    };

    let run_bulk = move |ids: Vec<String>, op: &str, folder_id: &str| {
        if ids.is_empty() {
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let op = op.to_string();
        let folder_id = folder_id.to_string();
        let n = start_op();
        spawn_local(async move {
            let body = serde_json::json!({
                "ids": ids,
                "op": op,
                "folderId": folder_id,
            });
            match post_json_value(&b, &t, "/v1/session-list/bulk", body).await {
                Ok(_) => finish_op(n, String::new()),
                Err(e) => finish_op(n, e),
            }
            reload();
        });
    };

    let patch_session = move |id: String, body: serde_json::Value| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let n = start_op();
        spawn_local(async move {
            let path = format!("/v1/session-list/sessions/{}", js_encode(&id));
            match crate::api::patch_json(&b, &t, &path, body).await {
                Ok(()) => finish_op(n, String::new()),
                Err(e) => finish_op(n, e),
            }
            reload();
        });
    };

    let add_folder = move |name: String| {
        let name = name.trim().to_string();
        if name.is_empty() {
            list_error.set("Folder name is empty".into());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let n = start_op();
        spawn_local(async move {
            match post_json_value(
                &b,
                &t,
                "/v1/session-list/folders",
                serde_json::json!({ "name": name }),
            )
            .await
            {
                Ok(_) => finish_op(n, String::new()),
                Err(e) => finish_op(n, e),
            }
            reload();
        });
    };

    let remove_folder = move |id: String| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let n = start_op();
        spawn_local(async move {
            let path = format!("/v1/session-list/folders/{}", js_encode(&id));
            match delete_json(&b, &t, &path, None).await {
                Ok(()) => finish_op(n, String::new()),
                Err(e) => finish_op(n, e),
            }
            reload();
        });
    };

    // Bulk cleanup of empty folders (#1566). The candidate set is
    // recomputed against a fresh snapshot inside the run: a folder
    // that gained a session since the confirm was armed is skipped,
    // never removed from under its sessions. Deletes reuse the
    // existing per-folder DELETE in a loop; the panel reloads once
    // at the end, and a partial failure says how many folders could
    // not be removed instead of reporting a clean sweep.
    let cleanup_empty: Rc<dyn Fn(Vec<String>)> = Rc::new(move |candidates: Vec<String>| {
        if candidates.is_empty() || cleanup_busy.get_untracked() {
            return;
        }
        cleanup_busy.set(true);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let n = start_op();
        let finish = finish_op;
        let reload = reload;
        let note = action_note;
        spawn_local(async move {
            let mut targets = candidates.clone();
            let mut skipped = 0usize;
            if let Ok(fresh) = get_json::<Snapshot>(&b, &t, "/v1/session-list").await {
                let folder_ids: Vec<String> =
                    fresh.folders.iter().map(|f| f.id.clone()).collect();
                let filed: Vec<String> =
                    fresh.sessions.values().map(|o| o.folder_id.clone()).collect();
                let still = still_empty_folder_ids(&candidates, &folder_ids, &filed);
                skipped = candidates.len() - still.len();
                targets = still;
            }
            let total = targets.len();
            let mut failed = 0usize;
            for id in &targets {
                let path = format!("/v1/session-list/folders/{}", js_encode(id));
                if delete_json(&b, &t, &path, None).await.is_err() {
                    failed += 1;
                }
            }
            cleanup_busy.set(false);
            if failed > 0 {
                finish(n, format!("Could not remove {failed} of {total} empty folders."));
            } else {
                finish(n, String::new());
                if total > 0 && skipped > 0 {
                    note.set(format!(
                        "Removed {total} empty folders. {skipped} skipped — no longer empty."
                    ));
                } else if total > 0 {
                    note.set(format!("Removed {total} empty folders."));
                } else if skipped > 0 {
                    note.set("Nothing removed — every folder gained a session.".into());
                }
            }
            reload();
        });
    });

    // Panel-level discovery + one shared confirm for the cleanup
    // (#1566). The count comes from the snapshot's folder assignments,
    // so it never disagrees with what the batch will actually remove.
    let ask_cleanup: Rc<dyn Fn()> = Rc::new(move || {
        let snap = snapshot.get_untracked();
        let folder_ids: Vec<String> = snap.folders.iter().map(|f| f.id.clone()).collect();
        let filed: Vec<String> = snap
            .sessions
            .values()
            .map(|o| o.folder_id.clone())
            .collect();
        let candidates = empty_folder_ids(&folder_ids, &filed);
        let count = candidates.len();
        if count == 0 {
            return;
        }
        let run = cleanup_empty.clone();
        if !crate::confirm::ask(
            crate::confirm::ConfirmRequest {
                title: format!("Remove {count} empty folders?"),
                body: "Sessions are not affected — these folders contain none.".into(),
                confirm_label: "Remove empty folders".into(),
            },
            Rc::new(move || run(candidates.clone())),
        ) {
            list_error.set(crate::confirm::BUSY.into());
        }
    });
    CLEANUP_ASK.with(|slot| *slot.borrow_mut() = Some(ask_cleanup));
    on_cleanup(|| CLEANUP_ASK.with(|slot| *slot.borrow_mut() = None));

    let open_at = move |index: usize| {
        let layout = layout.get_untracked();
        let Some(item) = layout
            .items
            .iter()
            .filter(|item| item.nav.is_some())
            .nth(index)
        else {
            return;
        };
        cursor.set(index);
        if item.kind == LayoutKind::Folder {
            return;
        }
        on_open(item.open_id.clone(), item.live);
    };

    {
        let layout = layout;
        set_key_handler(Some(Box::new(move |ev| {
            // The shell owns the command palette now and keeps its open
            // state internal; the dialog's presence in the DOM is the
            // signal that panel keys must stand down.
            if palette_is_open() || ev.meta_key() || ev.ctrl_key() {
                return false;
            }
            let key = ev.key();
            if ev.alt_key() {
                let Some(index) = alt_digit(&key) else {
                    return false;
                };
                let layout = layout.get_untracked();
                let Some(item) = layout
                    .items
                    .iter()
                    .find(|item| item.slot == Some(index as u8 + 1))
                else {
                    return false;
                };
                ev.prevent_default();
                if let Some(nav) = item.nav {
                    cursor.set(nav);
                }
                on_open(item.open_id.clone(), item.live);
                return true;
            }
            let target = event_element(ev);
            let in_panel = target.as_ref().is_some_and(inside_panel);
            if !in_panel {
                return false;
            }
            let search = target.as_ref().is_some_and(|el| class_has(el, "ss-search"));
            let typing = target.as_ref().is_some_and(is_typing);
            if key == "Escape" {
                if !selected.get_untracked().is_empty() {
                    ev.prevent_default();
                    selected.set(Vec::new());
                    return true;
                }
                return false;
            }
            if search && matches!(key.as_str(), "j" | "k" | "x") {
                return false;
            }
            if typing && !search {
                return false;
            }
            let nav_len = layout
                .get_untracked()
                .items
                .iter()
                .filter(|item| item.nav.is_some())
                .count();
            let delta = match key.as_str() {
                "j" | "ArrowDown" => 1,
                "k" | "ArrowUp" => -1,
                _ => 0,
            };
            if delta != 0 {
                if nav_len == 0 {
                    return false;
                }
                ev.prevent_default();
                cursor.set(step_index(nav_len, cursor.get_untracked(), delta));
                return true;
            }
            if key == "Enter" {
                if nav_len == 0 {
                    return false;
                }
                ev.prevent_default();
                open_at(cursor.get_untracked().min(nav_len - 1));
                return true;
            }
            if key == "x" || key == "X" {
                let layout = layout.get_untracked();
                let index = cursor.get_untracked();
                let Some(item) = layout
                    .items
                    .iter()
                    .filter(|item| item.nav.is_some())
                    .nth(index)
                else {
                    return false;
                };
                if item.kind != LayoutKind::Session {
                    return false;
                }
                ev.prevent_default();
                let id = item.id.clone();
                selected.set(toggle_id(&selected.get_untracked(), &id));
                anchor.set(id);
                return true;
            }
            false
        })));
        on_cleanup(|| set_key_handler(None));
    }

    Effect::new(move |_| {
        let layout = layout.get();
        let index = cursor.get();
        let active = active_root.get();
        let at_cursor = layout
            .items
            .iter()
            .filter(|item| item.nav.is_some())
            .nth(index);
        let picked = at_cursor
            .filter(|item| item.kind == LayoutKind::Session)
            .or_else(|| {
                layout.items.iter().find(|item| {
                    item.kind == LayoutKind::Session
                        && (item.id == active || item.open_id == active)
                })
            });
        let next = match picked {
            Some(item) => CursorSession {
                id: item.id.clone(),
                label: item.label.clone(),
                pinned: item.pinned,
            },
            None => CursorSession::default(),
        };
        CURSOR.with(|slot| *slot.borrow_mut() = next);
        let folders: Vec<(String, String)> = snapshot
            .get()
            .folders
            .iter()
            .map(|folder| (folder.id.clone(), folder.name.clone()))
            .collect();
        FOLDERS.with(|slot| *slot.borrow_mut() = folders);
        SELECTED.with(|slot| *slot.borrow_mut() = selected.get());
    });

    let target_id = move || {
        let cursor = cursor_session();
        if !cursor.id.is_empty() {
            cursor.id
        } else {
            active_root.get_untracked()
        }
    };
    let note = action_note;
    crate::shell::bind(
        "session.conductor",
        Rc::new(move |_| {
            conductor.update(|v| *v = !*v);
            Ok(())
        }),
    );
    crate::shell::bind(
        "session.filter.needs-you",
        Rc::new(move |_| {
            filter.set("needs-you".into());
            Ok(())
        }),
    );
    crate::shell::bind(
        "session.filter.progress",
        Rc::new(move |_| {
            filter.set("progress".into());
            Ok(())
        }),
    );
    crate::shell::bind(
        "session.filter.pinned",
        Rc::new(move |_| {
            filter.set("pinned".into());
            Ok(())
        }),
    );
    crate::shell::bind(
        "session.filter.archived",
        Rc::new(move |_| {
            filter.set("archived".into());
            Ok(())
        }),
    );
    crate::shell::bind(
        "session.fold",
        Rc::new(move |args| {
            let days: i64 = args.trim().parse().unwrap_or(7);
            if !matches!(days, 0 | 1 | 7 | 14 | 30) {
                return Err("Fold is 1, 7, 14, 30 days, or Never.".into());
            }
            older_days.set(days);
            show_older.set(false);
            Ok(())
        }),
    );
    crate::shell::bind(
        "session.folder.new",
        Rc::new(move |args| {
            let name = args.trim().to_string();
            if name.is_empty() {
                return Err("Folder name is empty.".into());
            }
            add_folder(name);
            Ok(())
        }),
    );
    crate::shell::bind(
        "session.folder.remove",
        Rc::new(move |id| {
            let id = id.trim().to_string();
            if id.is_empty() {
                return Err("Pick a folder.".into());
            }
            let name = folder_pairs()
                .into_iter()
                .find(|(fid, _)| fid == &id)
                .map(|(_, name)| name)
                .unwrap_or_else(|| id.clone());
            let remove = remove_folder;
            if !crate::confirm::ask(
                crate::confirm::ConfirmRequest {
                    title: format!("Remove folder {name}?"),
                    body: "Sessions in the folder stay. A folder only groups them. It is not a directory on disk.".into(),
                    confirm_label: "Remove folder".into(),
                },
                Rc::new(move || remove(id.clone())),
            ) {
                return Err(crate::confirm::BUSY.into());
            }
            Ok(())
        }),
    );
    let bulk = Rc::new(move |op: &'static str| -> Result<(), String> {
        let ids = selected_ids();
        if ids.is_empty() {
            return Err("Select one or more sessions first.".into());
        }
        run_bulk(ids, op, "");
        Ok(())
    });
    let bulk_pin = bulk.clone();
    let bulk_unpin = bulk.clone();
    let bulk_archive = bulk.clone();
    let bulk_unarchive = bulk.clone();
    crate::shell::bind("session.bulk.pin", Rc::new(move |_| bulk_pin("pin")));
    crate::shell::bind("session.bulk.unpin", Rc::new(move |_| bulk_unpin("unpin")));
    crate::shell::bind(
        "session.bulk.archive",
        Rc::new(move |_| bulk_archive("archive")),
    );
    crate::shell::bind(
        "session.bulk.unarchive",
        Rc::new(move |_| bulk_unarchive("unarchive")),
    );
    crate::shell::bind(
        "session.bulk.move",
        Rc::new(move |folder| {
            let ids = selected_ids();
            if ids.is_empty() {
                return Err("Select one or more sessions first.".into());
            }
            let folder = folder.trim();
            if folder.is_empty() {
                return Err("Pick a folder.".into());
            }
            let folder_id = if folder == "__unfiled" { "" } else { folder };
            run_bulk(ids, "move", folder_id);
            selected.set(Vec::new());
            Ok(())
        }),
    );
    crate::shell::bind(
        "session.pin",
        Rc::new(move |_| {
            let id = target_id();
            if id.is_empty() {
                return Err("No session to pin.".into());
            }
            patch_session(id, serde_json::json!({"pinned": true}));
            Ok(())
        }),
    );
    crate::shell::bind(
        "session.unpin",
        Rc::new(move |_| {
            let id = target_id();
            if id.is_empty() {
                return Err("No session to unpin.".into());
            }
            patch_session(id, serde_json::json!({"pinned": false}));
            Ok(())
        }),
    );
    crate::shell::bind(
        "session.copy-id",
        Rc::new(move |_| {
            let id = target_id();
            if id.is_empty() {
                return Err("No session to copy.".into());
            }
            if let Some(win) = web_sys::window() {
                let _ = win.navigator().clipboard().write_text(&id);
            }
            note.set(format!("Copied {id}"));
            Ok(())
        }),
    );
    crate::shell::bind(
        "session.unread",
        Rc::new(move |_| {
            let id = target_id();
            if id.is_empty() {
                return Err("No session to mark unread.".into());
            }
            let (b, t) = (base.get_untracked(), token.get_untracked());
            let note = note;
            spawn_local(async move {
                let path = format!("/v1/sessions/{}/seen", js_encode(&id));
                match delete_json(&b, &t, &path, None).await {
                    Ok(()) => {
                        note.set("Marked unread.".into());
                        reload();
                        on_refresh();
                    }
                    Err(e) => list_error.set(e),
                }
            });
            Ok(())
        }),
    );
    on_cleanup(|| {
        for id in [
            "session.conductor",
            "session.filter.needs-you",
            "session.filter.progress",
            "session.filter.pinned",
            "session.filter.archived",
            "session.fold",
            "session.folder.new",
            "session.folder.remove",
            "session.bulk.pin",
            "session.bulk.unpin",
            "session.bulk.archive",
            "session.bulk.unarchive",
            "session.bulk.move",
            "session.pin",
            "session.unpin",
            "session.copy-id",
            "session.unread",
        ] {
            crate::shell::unbind(id);
        }
    });

    let chip = |id: &'static str, label: &'static str| {
        view! {
            <button type="button" class=move || if filter.get() == id { "chip on" } else { "chip" } on:click=move |_| filter.set(id.into())>{label}</button>
        }
    };

    view! {
        <div class="ss-panel" tabindex="0" data-selected-count=move || selected.get().len().to_string()>
            <div class="section-head">
                <h2 class="panel-title">
                    <span class="icon-slot" aria-hidden="true" inner_html=icon("sessions")></span>
                    "Sessions"
                </h2>
                <button type="button" class="secondary" on:click=move |_| on_new()>"New"</button>
            </div>
            <Show when=move || conductor.get()>
                <p class="hint">"Conductor view"</p>
            </Show>
            <div class="ss-chips" role="tablist" aria-label="Session filter">
                {chip("all", "All")}
                {chip("unread", "Unread")}
                {chip("needs-you", "Needs you")}
            </div>
            <Show when=move || !matches!(filter.get().as_str(), "all" | "unread" | "needs-you")>
                <p class="hint">{move || match filter.get().as_str() {
                    "needs-you" => "Showing sessions that need you".into(),
                    "progress" => "Showing in progress".into(),
                    "pinned" => "Showing pinned".into(),
                    "archived" => "Showing archived".into(),
                    other => format!("Showing {other}"),
                }}</p>
            </Show>
            <input
                class="search ss-search"
                placeholder="Search titles and recent messages"
                aria-label="Search titles and recent messages"
                prop:value=move || query.get()
                on:input=move |ev| query.set(event_target_value(&ev))
            />
            <Show when=move || !action_note.get().is_empty()>
                <p class="hint">{move || action_note.get()}</p>
            </Show>
            <Show when=move || !list_error.get().is_empty()>
                <div class="ss-notice" role="alert">
                    <span>{move || list_error.get()}</span>
                    <button type="button" class="secondary" on:click=move |_| reload()>"Reload"</button>
                </div>
            </Show>
            <Show when=move || !search_error.get().is_empty()>
                <p class="hint ss-search-error" role="alert">{move || search_error.get()}</p>
            </Show>
            <Show when=move || !selected.get().is_empty()>
                <div class="ss-bulk" aria-label="Bulk actions">
                    <span class="ss-bulk-count">{move || format!("{} selected", selected.get().len())}</span>
                    <button type="button" class="secondary" on:click=move |_| selected.set(Vec::new())>"Clear"</button>
                </div>
            </Show>
            {move || {
                // Empty-folder cleanup (#1566): shown only while the
                // snapshot says at least one folder holds no session.
                let snap = snapshot.get();
                let folder_ids: Vec<String> = snap.folders.iter().map(|f| f.id.clone()).collect();
                let filed: Vec<String> = snap
                    .sessions
                    .values()
                    .map(|o| o.folder_id.clone())
                    .collect();
                let empty = empty_folder_ids(&folder_ids, &filed);
                if empty.is_empty() {
                    ().into_any()
                } else {
                    let label = format!("Clean up empty folders ({})", empty.len());
                    view! {
                        <div class="ss-folder-cleanup">
                            <button
                                type="button"
                                class="secondary"
                                data-action="cleanup-empty-folders"
                                disabled=move || cleanup_busy.get()
                                on:click=move |_| ask_empty_folder_cleanup()
                            >
                                {label}
                            </button>
                        </div>
                    }
                    .into_any()
                }
            }}
            {move || render_rows(
                layout.get(),
                cursor.get(),
                selected.get(),
                active_root.get(),
                on_open,
                move |id| {
                    collapsed.update(|list| {
                        if let Some(i) = list.iter().position(|item| item == &id) {
                            list.remove(i);
                        } else {
                            list.push(id);
                        }
                    });
                },
                move |id, shift| {
                    let order: Vec<String> = layout
                        .get_untracked()
                        .items
                        .iter()
                        .filter(|item| item.kind == LayoutKind::Session)
                        .map(|item| item.id.clone())
                        .collect();
                    if shift && !anchor.get_untracked().is_empty() {
                        selected.set(select_range(&order, &anchor.get_untracked(), &id));
                    } else {
                        selected.set(toggle_id(&selected.get_untracked(), &id));
                        anchor.set(id);
                    }
                },
                move |index| cursor.set(index),
            )}
            {move || {
                let hidden = layout.get().hidden_older;
                let open = show_older.get();
                view! {
                    <div class="ss-older">
                        <Show when=move || hidden != 0>
                            <button type="button" class="secondary" on:click=move |_| show_older.set(true)>{format!("Older sessions ({hidden})")}</button>
                        </Show>
                        <Show when=move || open && older_days.get() != 0>
                            <button type="button" class="secondary" on:click=move |_| show_older.set(false)>"Fold older sessions"</button>
                        </Show>
                    </div>
                }
            }}
        </div>
    }
}

fn build_layout(
    snap: &Snapshot,
    sessions: &[SessionItem],
    roots: &[RootSummary],
    activity: &[ActivityRow],
    seen: &HashMap<String, i64>,
    hits: &[SearchHit],
    workspaces: &[Workspace],
    filter: &str,
    query: &str,
    collapsed: &[String],
    active: &str,
    conductor: bool,
    older_days: i64,
    show_older: bool,
) -> Layout {
    let active_running = activity.iter().any(|row| is_running(&row.state));
    let pins: Vec<String> = snap
        .sessions
        .iter()
        .filter(|(_, org)| org.pinned)
        .map(|(id, _)| id.clone())
        .collect();
    let merged = merge_sessions(roots, sessions, &pins, seen, active, active_running);
    let mut mtimes = HashMap::new();
    let sources: Vec<SourceSession> = merged
        .into_iter()
        .map(|row| {
            mtimes.insert(row.id.clone(), row.mtime);
            let org = snap.sessions.get(&row.id);
            let preview = snap.previews.get(&row.id).cloned().unwrap_or_default();
            let server_title = snap.titles.get(&row.id).cloned().unwrap_or_default();
            let meta = if !server_title.is_empty() && !title_is_raw_id(&server_title, &row.id) {
                server_title
            } else {
                row.title
            };
            let snippet = hits
                .iter()
                .find(|hit| hit.id == row.id || hit.parent_id == row.id)
                .map(|hit| hit.snippet.clone())
                .unwrap_or_default();
            SourceSession {
                title: human_title(&meta, &preview, &row.id),
                preview,
                snippet,
                mtime: row.mtime,
                busy: row.busy,
                live: row.live,
                pinned: org.map(|org| org.pinned).unwrap_or(row.pinned),
                archived: org.map(|org| org.archived).unwrap_or(false),
                unread: unread_flag(
                    None,
                    &row.id,
                    active,
                    row.mtime,
                    seen.get(&row.id).copied().unwrap_or(0),
                ),
                folder_id: org.map(|org| org.folder_id.clone()).unwrap_or_default(),
                team_state: String::new(),
                status: row.status.clone(),
                permission_pending: row.permission_pending,
                question_pending: row.question_pending,
                workspace: workspaces
                    .iter()
                    .find(|w| w.id == row.workspace_id)
                    .map(|w| {
                        if w.name.is_empty() {
                            w.path.clone()
                        } else {
                            w.name.clone()
                        }
                    })
                    .unwrap_or_default(),
                id: row.id,
            }
        })
        .collect();

    let mut state_by = HashMap::new();
    for row in activity {
        if !row.session_id.is_empty() {
            state_by.insert(row.session_id.clone(), row.state.clone());
        }
    }
    let mut children: Vec<ChildIn> = snap
        .children
        .iter()
        .map(|child| {
            let state = state_by.get(&child.id).cloned().unwrap_or_default();
            ChildIn {
                id: child.id.clone(),
                parent_id: child.parent_id.clone(),
                title: human_title(&child.title, &child.preview, &child.id),
                state,
            }
        })
        .collect();
    for row in activity {
        if row.session_id.is_empty() || row.session_id == active {
            continue;
        }
        if children.iter().any(|child| child.id == row.session_id) {
            continue;
        }
        let raw_name = title_is_raw_id(&row.name, &row.session_id);
        let title = if raw_name && !row.task.is_empty() {
            row.task.clone()
        } else {
            row.name.clone()
        };
        children.push(ChildIn {
            id: row.session_id.clone(),
            parent_id: active.to_string(),
            title: human_title(&title, "", &row.session_id),
            state: row.state.clone(),
        });
    }
    let folders: Vec<FolderIn> = snap
        .folders
        .iter()
        .map(|folder| FolderIn {
            id: folder.id.clone(),
            name: folder.name.clone(),
        })
        .collect();
    let collapsed: HashSet<String> = collapsed.iter().cloned().collect();
    let mut content_ids = HashSet::new();
    for hit in hits {
        content_ids.insert(hit.id.clone());
        if !hit.parent_id.is_empty() {
            content_ids.insert(hit.parent_id.clone());
        }
    }
    let now_s = (now_ms() / 1000.0) as i64;
    let mut layout = assemble(AssembleArgs {
        sessions: &sources,
        children: &children,
        folders: &folders,
        collapsed: &collapsed,
        content_ids: &content_ids,
        filter,
        query,
        conductor,
        active,
        now_s,
        older_days,
        show_older,
    });
    stamp_times(&mut layout.items, &mtimes, now_s);
    layout
}

fn render_rows(
    layout: Layout,
    cursor: usize,
    selected: Vec<String>,
    active: String,
    on_open: impl Fn(String, bool) + Copy + 'static,
    on_toggle_folder: impl Fn(String) + Copy + 'static,
    on_check: impl Fn(String, bool) + Copy + 'static,
    on_cursor: impl Fn(usize) + Copy + 'static,
) -> impl IntoView {
    let nav_len = layout
        .items
        .iter()
        .filter(|item| item.nav.is_some())
        .count();
    let cursor = if nav_len == 0 {
        0
    } else {
        cursor.min(nav_len - 1)
    };
    let any = layout
        .items
        .iter()
        .any(|item| item.kind != LayoutKind::Folder);
    let rows = layout.items.into_iter().map(|item| {
        match item.kind {
            LayoutKind::Folder => {
                let id = item.id.clone();
                let label = item.label.clone();
                let count = item.count;
                let collapsed = item.collapsed;
                view! {
                    <div class="ss-folder-row">
                        <button type="button" class="ss-folder" aria-expanded=(!collapsed).to_string() on:click=move |_| {
                            if !id.is_empty() {
                                on_toggle_folder(id.clone());
                            }
                        }>
                            <span class="ss-folder-name">{label}</span>
                            <span class="ss-count">{count}</span>
                        </button>
                    </div>
                }
                .into_any()
            }
            LayoutKind::Session | LayoutKind::Child => {
                let id = item.id.clone();
                let open_id = item.open_id.clone();
                let live = item.live;
                let label = item.label.clone();
                let subtitle = item.subtitle.clone();
                let when = item.when.clone();
                let kind = item.status_kind.clone();
                let is_session = item.kind == LayoutKind::Session;
                let checked = selected.iter().any(|sid| sid == &id);
                let current = active == id || (item.kind == LayoutKind::Child && active == open_id && false);
                let on = active == open_id && item.kind == LayoutKind::Session || active == id;
                let nav = item.nav.unwrap_or(0);
                let is_cursor = item.nav == Some(cursor);
                let depth = item.depth;
                let slot = item.slot;
                let mut class = format!("ss-row ss-depth-{depth}");
                if is_session {
                    class.push_str(" session-row");
                }
                if on || current {
                    class.push_str(" on");
                }
                if is_cursor {
                    class.push_str(" ss-cursor");
                }
                let gutter = format!("ss-gutter ss-gutter-{kind}");
                let tip = format!("{label} ({id})");
                let open_main = open_id.clone();
                let id_attr = id.clone();
                view! {
                    <div class=class role="option" aria-selected=on.to_string() data-session-id=id_attr>
                        {is_session.then(|| {
                            let id = id.clone();
                            view! {
                                <input
                                    type="checkbox"
                                    class="ss-check"
                                    aria-label="Select session"
                                    prop:checked=checked
                                    on:click=move |ev: web_sys::MouseEvent| {
                                        ev.prevent_default();
                                        ev.stop_propagation();
                                        on_check(id.clone(), ev.shift_key());
                                    }
                                />
                            }
                        })}
                        <button type="button" class="ss-main session-main" title=tip on:click=move |_| {
                            on_cursor(nav);
                            on_open(open_main.clone(), live);
                        }>
                            <span class=gutter aria-hidden="true"></span>
                            <span class="ss-copy">
                                <span class="ss-top">
                                    {slot.map(|n| view! { <span class="ss-slot">{n.to_string()}</span> })}
                                    <span class="ss-name name">{label}</span>
                                    <span class="ss-time">{when}</span>
                                </span>
                                <span class="ss-sub">{subtitle}</span>
                            </span>
                        </button>
                    </div>
                }
                .into_any()
            }
        }
    }).collect_view();
    view! {
        <div class="ss-list" role="listbox" aria-label="Sessions">
            <Show when=move || !any>
                <p class="hint">"No sessions match. Clear the filter, or start a new one."</p>
            </Show>
            {rows}
        </div>
    }
}
