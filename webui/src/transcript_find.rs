//! Find in transcript (#1342): search inside the open session only.
//!
//! Pure match logic lives here and is host-tested; the Leptos pieces are
//! the highlighted-text renderer plus small DOM helpers (scroll / focus).
//! The matcher reuses the document editor's `find_hits` (#1269) per line
//! so there is one case-insensitive search implementation in the crate,
//! not a second fork: `find_hits` works on one line at a time here, and
//! this module maps its per-line hits back to byte ranges in the whole
//! field (title or body) it was given.
//!
//! Scope (#1576): the search covers the whole open transcript —
//! titles, bodies (long and JSON-ish tool bodies included), and the
//! collapsed `detail` text the transcript folds into a <details>.
//! A hit inside a fold is returned as `Field::Detail`, so the pane can
//! expand that fold, scroll to it, and highlight it in place through
//! `MarkedText`; the corpus is complete, so the find bar needs no
//! scope note. Case-insensitive only, like the editor; no case toggle
//! in this slice.

use leptos::prelude::*;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::editor::find_hits;

/// Which rendered field of a transcript line a match sits in.
/// `Detail` is the collapsed fold (`Message::detail` / `ChatLine::detail`):
/// a match there is invisible until the fold is expanded, so the UI
/// treats it specially (see `TMatch::in_fold`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Field {
    Title,
    Body,
    Detail,
}

/// One match: `line` indexes the transcript's line list, `start..end`
/// are byte ranges inside that line's title, body, or detail text.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct TMatch {
    pub line: usize,
    pub field: Field,
    pub start: usize,
    pub end: usize,
}

impl TMatch {
    /// True when this match sits inside a collapsed fold, so landing
    /// on it must expand the fold first (#1576) — a count that includes
    /// folded hits is only honest if the user can be shown each one.
    pub fn in_fold(&self) -> bool {
        self.field == Field::Detail
    }
}

/// The searchable text of one transcript line: the whole line, with
/// no class excluded (#1576). `detail` is the collapsed fold body;
/// it is searched exactly like the visible fields.
#[derive(Clone, Copy, Debug)]
pub struct SearchLine<'a> {
    pub title: &'a str,
    pub body: &'a str,
    pub detail: &'a str,
}

/// Byte ranges of every case-insensitive occurrence of `query` in
/// `text`. Ranges are offsets in the whole `text`, across its newlines.
pub fn ranges_in(text: &str, query: &str) -> Vec<(usize, usize)> {
    if query.is_empty() || text.is_empty() {
        return Vec::new();
    }
    let mut out = Vec::new();
    let mut offset = 0usize;
    for line in text.split('\n') {
        for hit in find_hits(line, query) {
            if hit.start == 0 && hit.end == 0 {
                // Editor fallback for case folds that change byte length:
                // the whole line counts as one match.
                out.push((offset, offset + line.len()));
            } else {
                out.push((offset + hit.start, offset + hit.end));
            }
        }
        offset += line.len() + 1;
    }
    out
}

/// All matches in transcript order: line by line, title before body
/// before detail, left to right inside each field.
pub fn matches_for_lines(lines: &[SearchLine<'_>], query: &str) -> Vec<TMatch> {
    if query.is_empty() {
        return Vec::new();
    }
    let mut out = Vec::new();
    for (idx, line) in lines.iter().enumerate() {
        for (start, end) in ranges_in(line.title, query) {
            out.push(TMatch {
                line: idx,
                field: Field::Title,
                start,
                end,
            });
        }
        for (start, end) in ranges_in(line.body, query) {
            out.push(TMatch {
                line: idx,
                field: Field::Body,
                start,
                end,
            });
        }
        for (start, end) in ranges_in(line.detail, query) {
            out.push(TMatch {
                line: idx,
                field: Field::Detail,
                start,
                end,
            });
        }
    }
    out
}

/// Next/previous index with wrap-around. `count == 0` stays at 0.
pub fn step_idx(current: usize, delta: i32, count: usize) -> usize {
    if count == 0 {
        return 0;
    }
    let cur = current.min(count - 1) as i32;
    (cur + delta).rem_euclid(count as i32) as usize
}

/// An index that is always valid for a match list of `count` entries.
pub fn clamped_idx(current: usize, count: usize) -> usize {
    if count == 0 {
        0
    } else {
        current.min(count - 1)
    }
}

/// The find bar's count text. Zero matches is said plainly ("No
/// matches") rather than the ambiguous "0/0" the issue calls out.
pub fn count_label(query: &str, count: usize, current: usize) -> String {
    if query.is_empty() {
        return String::new();
    }
    if count == 0 {
        return "No matches".into();
    }
    format!("{}/{}", clamped_idx(current, count) + 1, count)
}

/// Splits `text` into plain and marked pieces for rendering. `current`
/// is the one range (if any) that gets the stronger current-match class.
pub fn marked_pieces(
    text: &str,
    ranges: &[(usize, usize)],
    current: Option<(usize, usize)>,
) -> Vec<(String, String)> {
    if ranges.is_empty() {
        return vec![(String::new(), text.to_string())];
    }
    let mut pieces = Vec::new();
    let mut cursor = 0usize;
    for &(start, end) in ranges {
        let start = start.min(text.len());
        let end = end.min(text.len());
        if start < cursor || end < start {
            continue;
        }
        if cursor < start {
            pieces.push((String::new(), text[cursor..start].to_string()));
        }
        let class = if current == Some((start, end)) {
            "tf-mark tf-current"
        } else {
            "tf-mark"
        };
        pieces.push((class.into(), text[start..end].to_string()));
        cursor = end;
    }
    if cursor < text.len() {
        pieces.push((String::new(), text[cursor..].to_string()));
    }
    if pieces.is_empty() {
        pieces.push((String::new(), text.to_string()));
    }
    pieces
}

/// Renders one transcript field with its matches wrapped in <mark>,
/// keeping the wrapper the plain path uses for that kind: "prose" is a
/// paragraph, "pre" a pre block, anything else an inline span. While a
/// find is active the bubble's text goes through here instead of the
/// linkifying / markdown renderers, so link chips and tables pause for
/// the duration of the search and come back when it clears.
#[component]
pub fn MarkedText(
    text: String,
    kind: &'static str,
    ranges: Vec<(usize, usize)>,
    current: Option<(usize, usize)>,
) -> impl IntoView {
    let inner = marked_pieces(&text, &ranges, current)
        .into_iter()
        .map(|(class, piece)| {
            if class.is_empty() {
                view! { <span>{piece}</span> }.into_any()
            } else {
                view! { <mark class=class>{piece}</mark> }.into_any()
            }
        })
        .collect_view();
    match kind {
        "pre" => view! { <pre>{inner}</pre> }.into_any(),
        "prose" => view! { <p class="prose">{inner}</p> }.into_any(),
        _ => view! { <span class="ev-inline">{inner}</span> }.into_any(),
    }
}

fn transcript_el() -> Option<web_sys::Element> {
    web_sys::window()
        .and_then(|w| w.document())
        .and_then(|doc| doc.get_element_by_id("tf-transcript"))
}

/// The transcript scroller's current scroll offset (0 when absent).
pub fn transcript_scroll() -> f64 {
    transcript_el().map(|el| el.scroll_top() as f64).unwrap_or(0.0)
}

fn soon(f: impl FnOnce() + 'static) {
    let Some(w) = web_sys::window() else {
        return;
    };
    let cb = Closure::once(f);
    let _ = w.set_timeout_with_callback_and_timeout_and_arguments_0(
        cb.as_ref().unchecked_ref(),
        0,
    );
    cb.forget();
}

/// Restores a saved transcript scroll offset after the find bar's
/// highlights have been torn down (Escape / close).
pub fn restore_scroll_soon(top: f64) {
    soon(move || {
        if let Some(el) = transcript_el() {
            el.set_scroll_top(top as i32);
        }
    });
}

/// Scrolls the transcript so the article for line `idx` is in view.
pub fn scroll_to_line_soon(idx: usize) {
    soon(move || {
        let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
            return;
        };
        if let Some(el) = doc.get_element_by_id(&format!("tf-line-{idx}")) {
            el.scroll_into_view_with_bool(false);
        }
    });
}

/// Focuses the find input and selects its text, after it mounts.
pub fn focus_search_soon() {
    soon(|| {
        let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
            return;
        };
        let Some(el) = doc.get_element_by_id("tf-search") else {
            return;
        };
        if let Some(html) = el.dyn_ref::<web_sys::HtmlElement>() {
            let _ = html.focus();
        }
        if let Some(input) = el.dyn_ref::<web_sys::HtmlInputElement>() {
            input.select();
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    fn lines<'a>(rows: &[(&'a str, &'a str)]) -> Vec<SearchLine<'a>> {
        rows.iter()
            .map(|&(title, body)| SearchLine {
                title,
                body,
                detail: "",
            })
            .collect()
    }

    fn lines_with_detail<'a>(rows: &[(&'a str, &'a str, &'a str)]) -> Vec<SearchLine<'a>> {
        rows.iter()
            .map(|&(title, body, detail)| SearchLine {
                title,
                body,
                detail,
            })
            .collect()
    }

    #[test]
    fn counts_matches_across_bodies_and_titles() {
        let rows = lines(&[
            ("You", "Alpha beta alpha"),
            ("deploy-tool", "nothing here"),
            ("Strike", "ALPHA once more"),
        ]);
        let hits = matches_for_lines(&rows, "alpha");
        // Two in the first body, one in the third line's body; the
        // middle line (a tool card) contributes none.
        assert_eq!(hits.len(), 3);
        assert_eq!(
            hits[0],
            TMatch {
                line: 0,
                field: Field::Body,
                start: 0,
                end: 5
            }
        );
        assert_eq!(hits[1].line, 0);
        assert_eq!(hits[1].start, 11);
        assert_eq!(
            hits[2],
            TMatch {
                line: 2,
                field: Field::Body,
                start: 0,
                end: 5
            }
        );
        let titled = lines(&[("alpha deploy", "body")]);
        let hits = matches_for_lines(&titled, "alpha");
        assert_eq!(hits.len(), 1);
        assert_eq!(hits[0].field, Field::Title);
    }

    #[test]
    fn search_is_case_insensitive_and_empty_is_noop() {
        assert_eq!(ranges_in("Alpha ALPHA alpha", "alpha").len(), 3);
        assert!(ranges_in("abc", "").is_empty());
        assert!(ranges_in("abc", "z").is_empty());
        assert!(matches_for_lines(&lines(&[("t", "b")]), "").is_empty());
    }

    #[test]
    fn ranges_span_newlines_with_whole_text_offsets() {
        let ranges = ranges_in("foo\nbar foo\nfoo", "foo");
        assert_eq!(ranges, vec![(0, 3), (8, 11), (12, 15)]);
    }

    #[test]
    fn step_wraps_around_both_ways() {
        assert_eq!(step_idx(0, 1, 3), 1);
        assert_eq!(step_idx(2, 1, 3), 0);
        assert_eq!(step_idx(0, -1, 3), 2);
        assert_eq!(step_idx(1, -1, 3), 0);
        assert_eq!(step_idx(0, 1, 0), 0);
        assert_eq!(step_idx(9, 1, 3), 0);
    }

    #[test]
    fn count_label_is_plain_about_zero() {
        assert_eq!(count_label("", 0, 0), "");
        assert_eq!(count_label("zz", 0, 0), "No matches");
        assert_eq!(count_label("a", 3, 0), "1/3");
        assert_eq!(count_label("a", 3, 9), "3/3");
    }

    #[test]
    fn marked_pieces_split_and_flag_current() {
        let pieces = marked_pieces("say alpha twice alpha", &[(4, 9), (16, 21)], Some((16, 21)));
        let rebuilt: String = pieces.iter().map(|(_, t)| t.clone()).collect();
        assert_eq!(rebuilt, "say alpha twice alpha");
        assert_eq!(pieces[1], ("tf-mark".into(), "alpha".into()));
        assert_eq!(pieces[3], ("tf-mark tf-current".into(), "alpha".into()));
        let plain = marked_pieces("plain", &[], None);
        assert_eq!(plain, vec![(String::new(), "plain".into())]);
    }

    #[test]
    fn folded_detail_is_searched_with_its_line_identity() {
        // #1576: a string that exists only inside a collapsed fold
        // must be found, and the hit must say which line and which
        // field it is in so the UI can expand that line's fold.
        let rows = lines_with_detail(&[
            ("You", "run the tests", ""),
            (
                "test-tool",
                "tests ran",
                "FAIL needle-test panicked at src/lib.rs",
            ),
            ("Strike", "done", ""),
        ]);
        let hits = matches_for_lines(&rows, "needle-test");
        assert_eq!(hits.len(), 1);
        assert_eq!(
            hits[0],
            TMatch {
                line: 1,
                field: Field::Detail,
                start: 5,
                end: 16
            }
        );
        assert!(hits[0].in_fold());
        // Visible-field hits are not fold hits.
        let visible = matches_for_lines(&rows, "tests ran");
        assert_eq!(visible.len(), 1);
        assert!(!visible[0].in_fold());
    }

    #[test]
    fn detail_hits_come_after_title_and_body_hits_on_the_same_line() {
        let rows = lines_with_detail(&[("alpha title", "alpha body", "alpha detail")]);
        let hits = matches_for_lines(&rows, "alpha");
        assert_eq!(hits.len(), 3);
        assert_eq!(hits[0].field, Field::Title);
        assert_eq!(hits[1].field, Field::Body);
        assert_eq!(hits[2].field, Field::Detail);
    }

    #[test]
    fn long_and_json_detail_bodies_are_searched_in_full() {
        // The old corpus dropped bodies over 240 chars and bodies
        // starting with `{`/`[`; a needle at the very end of either
        // shape must still be hit, with detail-wide offsets.
        let long = format!("{}needle", "x".repeat(300));
        let rows = lines_with_detail(&[("tool", "ran", &long)]);
        let hits = matches_for_lines(&rows, "needle");
        assert_eq!(hits.len(), 1);
        assert_eq!((hits[0].start, hits[0].end), (300, 306));
        assert_eq!(hits[0].field, Field::Detail);

        let rows = lines_with_detail(&[(
            "tool",
            "ran",
            "{\"path\": \"/tmp/needle.json\", \"ok\": false}",
        )]);
        let hits = matches_for_lines(&rows, "needle.json");
        assert_eq!(hits.len(), 1);
        assert_eq!(hits[0].field, Field::Detail);
    }

    #[test]
    fn matching_survives_an_appended_line() {
        // Streaming append: the line list grows; recomputing over the
        // longer list keeps the earlier matches at the same ranges.
        let before = lines(&[("You", "alpha one")]);
        let first = matches_for_lines(&before, "alpha");
        let after = lines(&[("You", "alpha one"), ("Strike", "alpha two alpha")]);
        let second = matches_for_lines(&after, "alpha");
        assert_eq!(second.len(), 3);
        assert_eq!(second[0], first[0]);
        assert_eq!(second[1].line, 1);
        assert_eq!(second[2].line, 1);
    }
}
