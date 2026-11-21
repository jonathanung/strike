//! Rendered Markdown table block with its actions (#1505).
//!
//! #1344 rendered GFM pipe tables as real `<table>`s but left them a dead
//! end. This module owns the table block of the shared Markdown renderer
//! (`editor::MarkdownView`, used by the transcript and the document
//! viewer alike) and adds a per-table action row: Copy Markdown, Copy CSV,
//! Copy TSV, and a Widen toggle.
//!
//! Serialisation is a pure function of the parsed table (`header`,
//! `aligns`, `rows`), never scraped from the DOM; the serialisers live in
//! the dependency-free `table-export` crate so CI runs their tests on the
//! host. Markdown keeps each cell's inline source; CSV and TSV get the
//! cell's plain text (inline markup stripped to its text). The header row
//! is always included.

use std::sync::Arc;

use leptos::prelude::*;
use leptos::task::spawn_local;

use crate::editor::{align_class, inline_plain_text, Inlines, MdAlign};

/// One copy format offered by the table action row.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TableFormat {
    Markdown,
    Csv,
    Tsv,
}

impl TableFormat {
    pub const ALL: [TableFormat; 3] = [TableFormat::Markdown, TableFormat::Csv, TableFormat::Tsv];

    /// Idle button label: an explicit verb and format, never icon-only.
    pub fn idle_label(self) -> &'static str {
        match self {
            TableFormat::Markdown => "Copy Markdown",
            TableFormat::Csv => "Copy CSV",
            TableFormat::Tsv => "Copy TSV",
        }
    }

    /// Stable hook for tests and the smoke check.
    pub fn key(self) -> &'static str {
        match self {
            TableFormat::Markdown => "markdown",
            TableFormat::Csv => "csv",
            TableFormat::Tsv => "tsv",
        }
    }
}

/// Button label for `fmt` given the last copy attempt `(format, ok)`.
/// Only the button that was pressed changes; success may only be claimed
/// once the clipboard write resolved (same rule as the transcript Copy).
pub fn copy_label(fmt: TableFormat, last: Option<(TableFormat, bool)>) -> &'static str {
    match last {
        Some((f, ok)) if f == fmt => crate::clipboard::transcript_copy_label(Some(ok)),
        _ => fmt.idle_label(),
    }
}

/// Inline failure note for the action row: empty unless the last copy
/// failed, in which case it names the manual fallback.
pub fn copy_note(last: Option<(TableFormat, bool)>) -> &'static str {
    match last {
        Some((_, false)) => crate::clipboard::COPY_FAILED,
        _ => "",
    }
}

pub fn widen_label(wide: bool) -> &'static str {
    if wide {
        "Unwiden"
    } else {
        "Widen"
    }
}

fn export_align(align: MdAlign) -> table_export::Align {
    match align {
        MdAlign::None => table_export::Align::None,
        MdAlign::Left => table_export::Align::Left,
        MdAlign::Center => table_export::Align::Center,
        MdAlign::Right => table_export::Align::Right,
    }
}

fn plain(cells: &[String]) -> Vec<String> {
    cells.iter().map(|c| inline_plain_text(c)).collect()
}

/// Clipboard text for one table in `fmt`.
pub fn serialize(
    fmt: TableFormat,
    header: &[String],
    aligns: &[MdAlign],
    rows: &[Vec<String>],
) -> String {
    match fmt {
        TableFormat::Markdown => {
            let aligns: Vec<_> = aligns.iter().copied().map(export_align).collect();
            table_export::to_markdown(header, &aligns, rows)
        }
        TableFormat::Csv | TableFormat::Tsv => {
            let rows: Vec<Vec<String>> = rows.iter().map(|r| plain(r)).collect();
            let header = plain(header);
            if fmt == TableFormat::Csv {
                table_export::to_csv(&header, &rows)
            } else {
                table_export::to_tsv(&header, &rows)
            }
        }
    }
}

/// One rendered table with its action row. Widen state and copy feedback
/// are per table and ephemeral (never persisted).
#[component]
pub fn MdTable(header: Vec<String>, aligns: Vec<MdAlign>, rows: Vec<Vec<String>>) -> impl IntoView {
    let wide = RwSignal::new(false);
    let last = RwSignal::new(None::<(TableFormat, bool)>);
    // Serialised on click, not per render: transcripts can hold many
    // tables and most are never copied.
    let data = Arc::new((header.clone(), aligns.clone(), rows.clone()));
    let buttons = TableFormat::ALL
        .into_iter()
        .map(|fmt| {
            let data = Arc::clone(&data);
            view! {
                <button
                    type="button"
                    class="sx-quiet md-table-copy"
                    data-md-table-copy=fmt.key()
                    on:click=move |_| {
                        let (h, a, r) = &*data;
                        let text = serialize(fmt, h, a, r);
                        spawn_local(async move {
                            let ok = crate::clipboard::copy_text(&text).await.is_ok();
                            last.set(Some((fmt, ok)));
                            crate::clipboard::after_ms(2_000, move || {
                                if last.get_untracked() == Some((fmt, ok)) {
                                    last.set(None);
                                }
                            });
                        });
                    }
                >{move || copy_label(fmt, last.get())}</button>
            }
        })
        .collect_view();
    let head_aligns = aligns.clone();
    let block_class = move || {
        if wide.get() {
            "md-table-block md-table-wide"
        } else {
            "md-table-block"
        }
    };
    view! {
        <div class=block_class data-md-table=true>
            <div class="md-table-actions" role="toolbar" aria-label="Table actions" data-md-table-actions=true>
                {buttons}
                <button
                    type="button"
                    class="sx-quiet md-table-widen"
                    data-md-table-widen=true
                    aria-pressed=move || if wide.get() { "true" } else { "false" }
                    on:click=move |_| wide.update(|w| *w = !*w)
                >{move || widen_label(wide.get())}</button>
                <span class="md-table-note" role="status" aria-live="polite">{move || copy_note(last.get())}</span>
            </div>
            <div class="md-table-wrap">
                <table class="md-table">
                    <thead>
                        <tr>
                            {header.into_iter().enumerate().map(|(idx, cell)| {
                                let cls = align_class(head_aligns.get(idx).copied().unwrap_or(MdAlign::None));
                                view! { <th scope="col" class=cls><Inlines text=cell /></th> }
                            }).collect_view()}
                        </tr>
                    </thead>
                    <tbody>
                        {rows.into_iter().map(|row| view! {
                            <tr>
                                {row.into_iter().enumerate().map(|(idx, cell)| {
                                    let cls = align_class(aligns.get(idx).copied().unwrap_or(MdAlign::None));
                                    view! { <td class=cls><Inlines text=cell /></td> }
                                }).collect_view()}
                            </tr>
                        }).collect_view()}
                    </tbody>
                </table>
            </div>
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::editor::{markdown_blocks, MdBlock};

    fn parse(src: &str) -> (Vec<String>, Vec<MdAlign>, Vec<Vec<String>>) {
        match markdown_blocks(src).into_iter().next() {
            Some(MdBlock::Table { header, aligns, rows }) => (header, aligns, rows),
            other => panic!("not a table: {other:?}"),
        }
    }

    #[test]
    fn markdown_copy_round_trips_through_the_parser() {
        let cases = [
            "| A | B |\n| :-- | --: |\n| `x \\| y` | **bold** |",
            "| Path | Note |\n| --- | :-: |\n| C:\\dir\\ | a \\| b |\n|  | empty |",
            "| [docs](https://example.com/a) | `code` |\n| - | - |\n| a\\\\b | c |",
        ];
        for src in cases {
            let (h, a, r) = parse(src);
            let again = serialize(TableFormat::Markdown, &h, &a, &r);
            assert_eq!(parse(&again), (h, a, r), "source {src:?} re-emitted as {again:?}");
        }
    }

    #[test]
    fn plain_formats_strip_inline_markup() {
        let (h, a, r) = parse("| **Name** | `cmd` |\n| --- | ---: |\n| [docs](https://example.com) | *em* text |");
        assert_eq!(serialize(TableFormat::Csv, &h, &a, &r), "Name,cmd\r\ndocs,em text");
        assert_eq!(serialize(TableFormat::Tsv, &h, &a, &r), "Name\tcmd\ndocs\tem text");
        assert_eq!(
            serialize(TableFormat::Markdown, &h, &a, &r),
            "| **Name** | `cmd` |\n| --- | ---: |\n| [docs](https://example.com) | *em* text |"
        );
    }

    #[test]
    fn copy_labels_change_only_the_pressed_button() {
        assert_eq!(copy_label(TableFormat::Csv, None), "Copy CSV");
        let ok = Some((TableFormat::Csv, true));
        assert_eq!(copy_label(TableFormat::Csv, ok), "Copied");
        assert_eq!(copy_label(TableFormat::Tsv, ok), "Copy TSV");
        let failed = Some((TableFormat::Markdown, false));
        assert_eq!(copy_label(TableFormat::Markdown, failed), "Copy failed");
        assert_eq!(copy_label(TableFormat::Csv, failed), "Copy CSV");
    }

    #[test]
    fn failure_note_only_on_failed_copy() {
        assert_eq!(copy_note(None), "");
        assert_eq!(copy_note(Some((TableFormat::Csv, true))), "");
        assert_eq!(copy_note(Some((TableFormat::Csv, false))), crate::clipboard::COPY_FAILED);
    }

    #[test]
    fn widen_label_toggles() {
        assert_eq!(widen_label(false), "Widen");
        assert_eq!(widen_label(true), "Unwiden");
    }

    #[test]
    fn idle_labels_are_text_not_icons() {
        for fmt in TableFormat::ALL {
            assert!(fmt.idle_label().starts_with("Copy "), "{fmt:?}");
        }
    }
}
