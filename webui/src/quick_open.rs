//! Quick-open a workspace file by name (#1396).
//!
//! The palette's "Open file…" command (registered by
//! `src/shell/contrib/quick_open.rs`) asks this module for its rows on
//! every keystroke. Rows come from the daemon's existing, root-confined
//! `GET /v1/files/search`; selecting one opens it through the page's
//! own document-open path (the same one the Files pane's Open uses), so
//! the unsaved-edit guard (#1394) applies unchanged. Only the page owns
//! base, token and the document signals, so `main.rs` installs a
//! late-bound [`QuickOpenHost`], like `gateway_qr::PhoneHost`.

use std::cell::RefCell;
use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;

use crate::shell::Choice;

/// Rows asked of the daemon per query.
pub const SEARCH_LIMIT: usize = 20;

pub struct QuickOpenHost {
    pub base: RwSignal<String>,
    pub token: RwSignal<String>,
    /// Open `path` in the document viewer.
    pub open: Rc<dyn Fn(&str)>,
}

/// The latest answered search: the query it answered, its paths, and a
/// failure sentence (empty on success).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Results {
    pub query: String,
    pub paths: Vec<String>,
    pub error: String,
}

struct State {
    host: QuickOpenHost,
    results: RwSignal<Results>,
    /// Query of the newest request; a response for any other is stale.
    asked: String,
}

thread_local! {
    static STATE: RefCell<Option<State>> = const { RefCell::new(None) };
}

/// Install the page bridge. Call from the mounted page (it creates the
/// results signal under the page's owner).
pub fn set_host(host: QuickOpenHost) {
    let results = RwSignal::new(Results::default());
    STATE.with(|st| {
        *st.borrow_mut() = Some(State {
            host,
            results,
            asked: String::new(),
        })
    });
}

/// One palette row for a search path: file name as the title, its
/// directory as the detail, the full path as the id `run` receives.
pub fn file_choice(path: &str) -> Choice {
    let trimmed = path.trim_end_matches('/');
    let (dir, name) = match trimmed.rfind('/') {
        Some(i) => (&trimmed[..i], &trimmed[i + 1..]),
        None => ("", trimmed),
    };
    Choice {
        id: path.to_string(),
        label: name.to_string(),
        detail: dir.to_string(),
    }
}

/// The path to open for the palette's argument, or the sentence to
/// show. `arg` is a row id (a search path) when a row was picked, or the
/// raw typed text when Enter found no row.
pub fn resolve_pick(arg: &str, results: &Results) -> Result<String, String> {
    let arg = arg.trim();
    if arg.is_empty() {
        return Err("Type part of a file name.".into());
    }
    if results.paths.iter().any(|p| p == arg) {
        return Ok(arg.to_string());
    }
    if !results.error.is_empty() {
        return Err(results.error.clone());
    }
    Err(format!("No file matches \u{201c}{arg}\u{201d}. Keep typing, then pick a result."))
}

/// Rows for the step text `query`. Reads the results signal (so the
/// palette re-renders when an answer lands) and starts a search when
/// the text differs from the newest request. Empty text lists nothing
/// and forgets the last request, so reopening the step searches fresh.
pub fn rows(query: &str) -> Vec<Choice> {
    let q = query.trim().to_string();
    let found = STATE.with(|st| {
        let mut st = st.borrow_mut();
        let st = st.as_mut()?;
        let fetch = if q.is_empty() {
            st.asked.clear();
            None
        } else if st.asked != q {
            st.asked = q.clone();
            Some((st.host.base.get_untracked(), st.host.token.get_untracked()))
        } else {
            None
        };
        Some((st.results, fetch))
    });
    let Some((results, fetch)) = found else {
        return Vec::new();
    };
    if let Some((base, token)) = fetch {
        search(base, token, q.clone(), results);
    }
    let current = results.get();
    if q.is_empty() {
        return Vec::new();
    }
    current.paths.iter().map(|p| file_choice(p)).collect()
}

fn search(base: String, token: String, q: String, results: RwSignal<Results>) {
    spawn_local(async move {
        let path = format!(
            "/v1/files/search?q={}&limit={SEARCH_LIMIT}",
            crate::api::js_encode(&q)
        );
        let answer = crate::api::get_json::<crate::model::FileSearchResponse>(&base, &token, &path).await;
        let fresh = STATE.with(|st| st.borrow().as_ref().is_some_and(|s| s.asked == q));
        if !fresh {
            return;
        }
        results.set(match answer {
            Ok(found) => Results {
                query: q,
                paths: found.paths,
                error: String::new(),
            },
            Err(e) => Results {
                query: q,
                paths: Vec::new(),
                error: format!("File search failed: {e}"),
            },
        });
    });
}

/// The palette command's `run`: open the picked path in the viewer.
pub fn open(arg: &str) -> Result<(), String> {
    let picked = STATE.with(|st| {
        let st = st.borrow();
        let st = st.as_ref().ok_or_else(|| "File search is not ready yet.".to_string())?;
        let path = resolve_pick(arg, &st.results.get_untracked())?;
        Ok::<_, String>((path, st.host.open.clone()))
    });
    let (path, open) = picked?;
    open(&path);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn file_choice_splits_name_and_directory() {
        let cases = [
            ("src/deep/nested/widget.rs", "widget.rs", "src/deep/nested"),
            ("README.md", "README.md", ""),
            ("a/b/", "b", "a"),
        ];
        for (path, label, detail) in cases {
            let c = file_choice(path);
            assert_eq!(c.id, path);
            assert_eq!((c.label.as_str(), c.detail.as_str()), (label, detail), "{path}");
        }
    }

    #[test]
    fn resolve_pick_opens_only_search_results() {
        let ok = Results {
            query: "widg".into(),
            paths: vec!["src/deep/nested/widget.rs".into()],
            error: String::new(),
        };
        assert_eq!(
            resolve_pick(" src/deep/nested/widget.rs ", &ok),
            Ok("src/deep/nested/widget.rs".to_string())
        );
        assert!(resolve_pick("", &ok).unwrap_err().contains("Type part"));
        assert!(resolve_pick("widg", &ok).unwrap_err().contains("No file matches"));
        let failed = Results {
            query: "x".into(),
            paths: Vec::new(),
            error: "File search failed: 500".into(),
        };
        assert_eq!(resolve_pick("x", &failed), Err("File search failed: 500".into()));
    }
}
