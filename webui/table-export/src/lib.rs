//! Table copy serialisers for the cockpit's rendered Markdown tables
//! (#1505).
//!
//! Input is the table the renderer already parsed — header cells,
//! per-column alignment, body rows — never text scraped from the DOM.
//! This crate has no web dependencies so the serialisers run as host
//! unit tests in CI.
//!
//! - [`to_markdown`] re-emits a GFM pipe table with the parsed
//!   alignments. Cells are written as given (the caller passes the
//!   cells' inline Markdown source, so code spans and links survive);
//!   `|` and any backslash that would pair with the next character are
//!   escaped, so the output splits back into the same cells.
//! - [`to_csv`] follows RFC 4180: CRLF between records, a field holding
//!   `,` `"` CR or LF is quoted with `"` doubled.
//! - [`to_tsv`] writes tab, LF and CR inside a cell as the two-character
//!   escapes `\t`, `\n`, `\r`, so one cell stays one cell.
//!
//! CSV and TSV callers pass plain-text cells (inline markup stripped).
//! The header row is always first. Every row is fitted to the header's
//! width — shorter rows padded with empty cells, longer rows cut — so all
//! three formats stay rectangular.

/// Column alignment from a GFM separator row.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Align {
    None,
    Left,
    Center,
    Right,
}

fn fit(row: &[String], width: usize) -> impl Iterator<Item = &str> {
    (0..width).map(move |i| row.get(i).map(String::as_str).unwrap_or(""))
}

fn align_marker(align: Align) -> &'static str {
    match align {
        Align::None => "---",
        Align::Left => ":---",
        Align::Center => ":---:",
        Align::Right => "---:",
    }
}

/// Escape one cell for a pipe table: `|` becomes `\|`, and a backslash
/// followed by `|`, `\` or the end of the cell is doubled so a table
/// splitter that unescapes `\|` and `\\` reads back exactly `cell`.
pub fn markdown_cell(cell: &str) -> String {
    let mut out = String::with_capacity(cell.len());
    let mut chars = cell.chars().peekable();
    while let Some(ch) = chars.next() {
        match ch {
            '|' => out.push_str("\\|"),
            '\\' if matches!(chars.peek(), Some('|') | Some('\\') | None) => out.push_str("\\\\"),
            _ => out.push(ch),
        }
    }
    out
}

fn markdown_row<'a>(cells: impl Iterator<Item = &'a str>, escape: bool) -> String {
    let mut line = String::from("|");
    for cell in cells {
        line.push(' ');
        if escape {
            line.push_str(&markdown_cell(cell));
        } else {
            line.push_str(cell);
        }
        line.push_str(" |");
    }
    line
}

/// GFM table text, header and separator first, `\n` line ends.
pub fn to_markdown(header: &[String], aligns: &[Align], rows: &[Vec<String>]) -> String {
    let width = header.len();
    let delim = (0..width).map(|i| align_marker(aligns.get(i).copied().unwrap_or(Align::None)));
    let mut lines = vec![
        markdown_row(header.iter().map(String::as_str), true),
        markdown_row(delim, false),
    ];
    lines.extend(rows.iter().map(|row| markdown_row(fit(row, width), true)));
    lines.join("\n")
}

/// One RFC 4180 field.
pub fn csv_field(cell: &str) -> String {
    if cell.contains([',', '"', '\r', '\n']) {
        format!("\"{}\"", cell.replace('"', "\"\""))
    } else {
        cell.to_string()
    }
}

fn records(header: &[String], rows: &[Vec<String>], field: fn(&str) -> String, sep: &str) -> Vec<String> {
    let width = header.len();
    std::iter::once(header.iter().map(|c| field(c)).collect::<Vec<_>>().join(sep))
        .chain(rows.iter().map(|row| fit(row, width).map(field).collect::<Vec<_>>().join(sep)))
        .collect()
}

/// RFC 4180 CSV: header record first, CRLF between records, no trailing
/// CRLF.
pub fn to_csv(header: &[String], rows: &[Vec<String>]) -> String {
    records(header, rows, csv_field, ",").join("\r\n")
}

/// One TSV field with tab/LF/CR escaped.
pub fn tsv_field(cell: &str) -> String {
    cell.replace('\t', "\\t")
        .replace('\n', "\\n")
        .replace('\r', "\\r")
}

/// Tab-separated values: header line first, `\n` line ends, no trailing
/// newline.
pub fn to_tsv(header: &[String], rows: &[Vec<String>]) -> String {
    records(header, rows, tsv_field, "\t").join("\n")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn s(v: &[&str]) -> Vec<String> {
        v.iter().map(|c| c.to_string()).collect()
    }

    #[test]
    fn markdown_preserves_alignments() {
        let md = to_markdown(
            &s(&["a", "b", "c", "d"]),
            &[Align::None, Align::Left, Align::Center, Align::Right],
            &[s(&["1", "2", "3", "4"])],
        );
        assert_eq!(md, "| a | b | c | d |\n| --- | :--- | :---: | ---: |\n| 1 | 2 | 3 | 4 |");
    }

    #[test]
    fn markdown_missing_aligns_default_to_none() {
        let md = to_markdown(&s(&["a", "b"]), &[Align::Right], &[]);
        assert_eq!(md, "| a | b |\n| ---: | --- |");
    }

    #[test]
    fn markdown_cell_escapes() {
        let cases = [
            ("plain", "plain"),
            ("a|b", "a\\|b"),
            ("`x | y`", "`x \\| y`"),
            ("tail\\", "tail\\\\"),
            ("x\\\\y", "x\\\\\\y"),
            ("C:\\dir", "C:\\dir"),
            ("\\|", "\\\\\\|"),
            ("", ""),
        ];
        for (cell, want) in cases {
            assert_eq!(markdown_cell(cell), want, "cell {cell:?}");
        }
    }

    #[test]
    fn markdown_keeps_inline_source() {
        let md = to_markdown(&s(&["**Name**"]), &[], &[s(&["[docs](https://example.com)"])]);
        assert_eq!(md, "| **Name** |\n| --- |\n| [docs](https://example.com) |");
    }

    #[test]
    fn csv_quotes_per_rfc4180() {
        let cases = [
            ("plain", "plain"),
            ("has, comma", "\"has, comma\""),
            ("say \"hi\"", "\"say \"\"hi\"\"\""),
            ("line\nbreak", "\"line\nbreak\""),
            ("cr\rhere", "\"cr\rhere\""),
            ("", ""),
            (" padded ", " padded "),
        ];
        for (cell, want) in cases {
            assert_eq!(csv_field(cell), want, "cell {cell:?}");
        }
    }

    #[test]
    fn csv_uses_crlf_between_records() {
        let csv = to_csv(&s(&["name", "note"]), &[s(&["a", "b, c"]), s(&["", "d"])]);
        assert_eq!(csv, "name,note\r\na,\"b, c\"\r\n,d");
    }

    #[test]
    fn tsv_escapes_tabs_and_newlines() {
        let cases = [
            ("plain", "plain"),
            ("x\ty", "x\\ty"),
            ("l1\nl2", "l1\\nl2"),
            ("cr\r", "cr\\r"),
            ("", ""),
        ];
        for (cell, want) in cases {
            assert_eq!(tsv_field(cell), want, "cell {cell:?}");
        }
        assert_eq!(to_tsv(&s(&["a", "b"]), &[s(&["x\ty", "z"])]), "a\tb\nx\\ty\tz");
    }

    #[test]
    fn empty_cells_and_ragged_rows_stay_rectangular() {
        let header = s(&["a", "b", "c"]);
        let rows = vec![s(&["1"]), s(&["1", "2", "3", "4"]), s(&["", "", ""])];
        assert_eq!(to_csv(&header, &rows), "a,b,c\r\n1,,\r\n1,2,3\r\n,,");
        assert_eq!(to_tsv(&header, &rows), "a\tb\tc\n1\t\t\n1\t2\t3\n\t\t");
        assert_eq!(
            to_markdown(&header, &[], &rows),
            "| a | b | c |\n| --- | --- | --- |\n| 1 |  |  |\n| 1 | 2 | 3 |\n|  |  |  |"
        );
    }

    #[test]
    fn header_only_table_keeps_its_header() {
        let header = s(&["A", "B"]);
        assert_eq!(to_csv(&header, &[]), "A,B");
        assert_eq!(to_tsv(&header, &[]), "A\tB");
        assert_eq!(to_markdown(&header, &[], &[]), "| A | B |\n| --- | --- |");
    }
}
