//! Search in files (#1406): find text across the workspace's files.
//!
//! A search box at the top of the Files pane calls the daemon's
//! `GET /v1/files/grep` (plain-text, case-insensitive substring; root
//! scoping, `.gitignore`, binary skipping and walk caps are the daemon's).
//! Results are grouped by file with the line number and a snippet whose
//! matches are marked with the transcript find bar's `MarkedText`.
//! Selecting a result opens the file in the document viewer at that line
//! through the page's `open_file` callback — the same landing file chips
//! use (`jump_line`). A capped search says so instead of looking complete.
//!
//! The palette command "Search in files…" (`shell/contrib/search_in_files.rs`)
//! takes the query as its argument; the page binds it to open the Files
//! tab, fill the box and run the search.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;

use crate::api::{get_json, js_encode};
use crate::editor::FindOptions;
use crate::transcript_find::{ranges_in, MarkedText};

/// Matches requested per search (the daemon clamps to its own maximum).
pub const RESULT_LIMIT: u32 = 200;

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct GrepMatch {
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub line: u32,
    #[serde(default)]
    pub snippet: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct GrepResponse {
    #[serde(default)]
    pub matches: Vec<GrepMatch>,
    #[serde(default)]
    pub capped: bool,
    #[serde(default, rename = "filesScanned")]
    pub files_scanned: u32,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum SearchState {
    Idle,
    Loading,
    Done(GrepResponse),
    Failed(String),
}

/// Request path for one query.
pub fn grep_path(query: &str) -> String {
    format!(
        "/v1/files/grep?q={}&limit={RESULT_LIMIT}",
        js_encode(query)
    )
}

/// Matches grouped by file, files in first-seen (daemon) order.
pub fn group_by_file(matches: &[GrepMatch]) -> Vec<(String, Vec<GrepMatch>)> {
    let mut out: Vec<(String, Vec<GrepMatch>)> = Vec::new();
    for m in matches {
        match out.iter_mut().find(|(p, _)| p == &m.path) {
            Some((_, items)) => items.push(m.clone()),
            None => out.push((m.path.clone(), vec![m.clone()])),
        }
    }
    out
}

fn plural(n: usize, one: &str, many: &str) -> String {
    format!("{n} {}", if n == 1 { one } else { many })
}

/// Status line above the results. The capped wording is the honesty rule
/// from the issue: a partial list must say it is partial.
pub fn summary(query: &str, resp: &GrepResponse) -> String {
    let files = group_by_file(&resp.matches).len();
    let scanned = plural(resp.files_scanned as usize, "file", "files");
    let base = if resp.matches.is_empty() {
        format!("No matches for \"{query}\" in {scanned}.")
    } else {
        format!(
            "{} in {} ({scanned} searched).",
            plural(resp.matches.len(), "match", "matches"),
            plural(files, "file", "files"),
        )
    };
    if resp.capped {
        format!("{base} Search capped — results are partial; narrow the query.")
    } else {
        base
    }
}

/// Case-insensitive highlight ranges for `query` in one snippet.
pub fn snippet_ranges(snippet: &str, query: &str) -> Vec<(usize, usize)> {
    ranges_in(snippet, query, FindOptions::new(false, false))
}

/// The Files-pane search box and its results. `query` and `run` are owned
/// by the page so the palette command can fill the box and start a search
/// (bump `run`); `on_open` lands the document viewer on a match.
#[component]
pub fn SearchInFiles(
    base: RwSignal<String>,
    token: RwSignal<String>,
    query: RwSignal<String>,
    run: RwSignal<u32>,
    on_open: Callback<(String, u32)>,
) -> impl IntoView {
    let state = RwSignal::new(SearchState::Idle);
    let searched = RwSignal::new(String::new());
    // Latest request wins: a slow response for an older query is dropped.
    let latest = RwSignal::new(0u32);
    Effect::new(move |_| {
        let gen = run.get();
        if gen == 0 {
            return;
        }
        let q = query.get_untracked();
        if q.trim().is_empty() {
            state.set(SearchState::Idle);
            return;
        }
        latest.set(gen);
        state.set(SearchState::Loading);
        searched.set(q.clone());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let res = get_json::<GrepResponse>(&b, &t, &grep_path(&q)).await;
            if latest.get_untracked() != gen {
                return;
            }
            state.set(match res {
                Ok(r) => SearchState::Done(r),
                Err(e) => SearchState::Failed(format!("Search in files failed: {e}")),
            });
        });
    });
    let start = move || run.update(|n| *n = n.wrapping_add(1));
    let clear = move || {
        query.set(String::new());
        state.set(SearchState::Idle);
    };
    view! {
        <div class="sif" data-search-in-files-pane="">
            <div class="sif-bar">
                <input
                    type="search"
                    class="sif-input"
                    data-search-in-files=""
                    placeholder="Search in files"
                    aria-label="Search in files"
                    prop:value=move || query.get()
                    on:input=move |ev| query.set(event_target_value(&ev))
                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                        match ev.key().as_str() {
                            "Enter" => {
                                ev.prevent_default();
                                start();
                            }
                            "Escape" if !query.get_untracked().is_empty() => {
                                ev.prevent_default();
                                ev.stop_propagation();
                                clear();
                            }
                            _ => {}
                        }
                    }
                />
                <button class="secondary" on:click=move |_| start()>"Search"</button>
            </div>
            {move || match state.get() {
                SearchState::Idle => ().into_any(),
                SearchState::Loading => view! { <p class="hint" role="status">"Searching…"</p> }.into_any(),
                SearchState::Failed(e) => view! { <div class="notice" role="alert"><span>{e}</span></div> }.into_any(),
                SearchState::Done(resp) => {
                    let q = searched.get_untracked();
                    let line = summary(&q, &resp);
                    let capped = resp.capped;
                    let groups = group_by_file(&resp.matches);
                    view! {
                        <p class=if capped { "hint sif-summary sif-capped" } else { "hint sif-summary" } role="status" data-sif-capped=capped.to_string()>{line}</p>
                        <div class="sif-results">
                            {groups.into_iter().map(|(path, items)| {
                                let count = items.len();
                                let q = q.clone();
                                view! {
                                    <section class="sif-file" data-sif-file=path.clone()>
                                        <h4 class="sif-path"><span>{path.clone()}</span><span class="sif-count">{count}</span></h4>
                                        <ul class="plain">
                                            {items.into_iter().map(|m| {
                                                let ranges = snippet_ranges(&m.snippet, &q);
                                                let (open_path, open_line) = (m.path.clone(), m.line);
                                                let label = format!("Open {}:{}", m.path, m.line);
                                                view! {
                                                    <li>
                                                        <button class="sif-hit" data-sif-path=m.path.clone() data-sif-line=m.line.to_string() title=label.clone() aria-label=label
                                                            on:click=move |_| on_open.run((open_path.clone(), open_line))>
                                                            <span class="sif-line">{m.line}</span>
                                                            <MarkedText text=m.snippet.clone() kind="inline" ranges=ranges current=None />
                                                        </button>
                                                    </li>
                                                }
                                            }).collect_view()}
                                        </ul>
                                    </section>
                                }
                            }).collect_view()}
                        </div>
                    }.into_any()
                }
            }}
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn m(path: &str, line: u32, snippet: &str) -> GrepMatch {
        GrepMatch {
            path: path.into(),
            line,
            snippet: snippet.into(),
        }
    }

    #[test]
    fn group_by_file_keeps_daemon_order() {
        let groups = group_by_file(&[m("b.go", 3, "x"), m("a.go", 1, "y"), m("b.go", 9, "z")]);
        let shape: Vec<(String, Vec<u32>)> = groups
            .into_iter()
            .map(|(p, items)| (p, items.into_iter().map(|i| i.line).collect()))
            .collect();
        assert_eq!(
            shape,
            vec![("b.go".to_string(), vec![3, 9]), ("a.go".to_string(), vec![1])]
        );
    }

    #[test]
    fn summary_states() {
        let cases = [
            (
                GrepResponse { matches: vec![], capped: false, files_scanned: 12 },
                "No matches for \"q\" in 12 files.",
            ),
            (
                GrepResponse { matches: vec![m("a", 1, "q")], capped: false, files_scanned: 1 },
                "1 match in 1 file (1 file searched).",
            ),
            (
                GrepResponse {
                    matches: vec![m("a", 1, "q"), m("b", 2, "q")],
                    capped: true,
                    files_scanned: 40,
                },
                "2 matches in 2 files (40 files searched). Search capped — results are partial; narrow the query.",
            ),
            (
                GrepResponse { matches: vec![], capped: true, files_scanned: 5000 },
                "No matches for \"q\" in 5000 files. Search capped — results are partial; narrow the query.",
            ),
        ];
        for (resp, want) in cases {
            assert_eq!(summary("q", &resp), want);
        }
    }

    #[test]
    fn snippet_ranges_are_case_insensitive() {
        assert_eq!(snippet_ranges("Flag and FLAG", "flag"), vec![(0, 4), (9, 13)]);
        assert!(snippet_ranges("nothing", "flag").is_empty());
    }

    #[test]
    fn response_decodes_daemon_shape() {
        let raw = r#"{"query":"x","limit":200,"matches":[{"path":"a.go","line":2,"snippet":"// x"}],"capped":true,"filesScanned":7}"#;
        let resp: GrepResponse = serde_json::from_str(raw).unwrap();
        assert_eq!(resp.matches, vec![m("a.go", 2, "// x")]);
        assert!(resp.capped);
        assert_eq!(resp.files_scanned, 7);
    }
}
