//! Document viewer and editor. Pure helpers are tested on the host; the
//! Leptos pane is the center-column surface the shell opens for a path.

use std::rc::Rc;
use std::sync::Arc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::Value;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{get_json, js_encode, put_json_value};

const MAX_RENDER_LINES: usize = 2000;
const MAX_HITS: usize = 2000;
const MAX_DIFF_CHARS: usize = 20_000;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Hit {
    pub line: u32,
    pub start: usize,
    pub end: usize,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Chunk {
    Text(String),
    Link { path: String, line: u32, label: String },
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Tok {
    pub kind: &'static str,
    pub text: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum MdAlign {
    None,
    Left,
    Center,
    Right,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum MdBlock {
    Heading(u8, String),
    Paragraph(String),
    Code(String),
    List(Vec<String>),
    Quote(String),
    Rule,
    Table {
        header: Vec<String>,
        aligns: Vec<MdAlign>,
        rows: Vec<Vec<String>>,
    },
}

pub fn language_of(path: &str) -> &'static str {
    let Some((_, ext)) = path.rsplit_once('.') else {
        return "text";
    };
    match ext {
        "go" => "go",
        "rs" => "rust",
        "js" | "mjs" | "cjs" | "jsx" => "js",
        "ts" | "tsx" => "ts",
        "py" => "py",
        "sh" | "bash" | "zsh" => "sh",
        "css" => "css",
        "json" => "json",
        "html" | "htm" => "html",
        "md" | "markdown" => "markdown",
        "yaml" | "yml" => "yaml",
        "toml" => "toml",
        "sql" => "sql",
        _ => "text",
    }
}

pub fn lines_of(text: &str) -> Vec<&str> {
    if text.is_empty() {
        return Vec::new();
    }
    let mut lines: Vec<&str> = text.split('\n').collect();
    if text.ends_with('\n') {
        lines.pop();
    }
    lines
}

/// The first rendered window for a source file of `total` lines: the
/// plain render cap, raised in whole chunks so it also covers
/// `target_line` (1-based; 0 means no target) — a find hit or a
/// go-to-line jump past the cap must land on a row that exists.
pub fn initial_render_limit(total: usize, target_line: u32) -> usize {
    let base = MAX_RENDER_LINES.min(total);
    let target = target_line as usize;
    if target <= base {
        return base;
    }
    let chunks = target.div_ceil(MAX_RENDER_LINES);
    chunks.saturating_mul(MAX_RENDER_LINES).min(total).max(base)
}

/// The window after one "show more": one more chunk, never past the
/// end of the file and never smaller than the current window.
pub fn render_limit_after_more(current: usize, total: usize) -> usize {
    let grown = current.saturating_add(MAX_RENDER_LINES).min(total);
    grown.max(current.min(total))
}

/// The visible prefix of a diff under a `max_chars` character cap,
/// extended to the end of the line the cap falls in, so a cut never
/// lands mid-line. Returns `full` unchanged when it fits the cap.
pub fn diff_visible<'a>(full: &'a str, max_chars: usize) -> &'a str {
    let mut seen = 0usize;
    let mut cut = full.len();
    for (idx, _) in full.char_indices() {
        if seen == max_chars {
            cut = idx;
            break;
        }
        seen += 1;
    }
    if cut == full.len() {
        return full;
    }
    if full[..cut].ends_with('\n') {
        return &full[..cut];
    }
    match full[cut..].find('\n') {
        Some(off) => &full[..cut + off],
        None => full,
    }
}

/// How much of `full` a `max_chars` cap hides: (characters, lines).
pub fn diff_hidden(full: &str, max_chars: usize) -> (usize, usize) {
    let visible = diff_visible(full, max_chars);
    let hidden_chars = full.chars().count() - visible.chars().count();
    let hidden_lines = lines_of(full).len() - lines_of(visible).len();
    (hidden_chars, hidden_lines)
}

/// Options for the shared find matcher (#1409 match-case, #1419
/// whole-word). Both default off, which is the historical behaviour:
/// case-insensitive substring matching. One struct threads through the
/// editor and transcript find (`transcript_find` reuses `find_hits`),
/// so the two bars can never drift into separate matchers.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct FindOptions {
    pub case_sensitive: bool,
    pub whole_word: bool,
}

impl FindOptions {
    pub fn new(case_sensitive: bool, whole_word: bool) -> Self {
        Self {
            case_sensitive,
            whole_word,
        }
    }
}

/// A word character for whole-word boundaries (#1419): exactly
/// `[A-Za-z0-9_]`, nothing wider (punctuation, whitespace, and
/// non-ASCII letters all count as boundaries).
pub fn is_word_char(c: char) -> bool {
    matches!(c, 'A'..='Z' | 'a'..='z' | '0'..='9' | '_')
}

/// Pure whole-word predicate: the hit at `start..end` in `line` counts
/// only when the characters immediately before and after it (if any)
/// are not word characters.
pub fn is_whole_word_at(line: &str, start: usize, end: usize) -> bool {
    let before_ok = line[..start.min(line.len())]
        .chars()
        .next_back()
        .map_or(true, |c| !is_word_char(c));
    let after_ok = line[end.min(line.len())..]
        .chars()
        .next()
        .map_or(true, |c| !is_word_char(c));
    before_ok && after_ok
}

pub fn find_hits(text: &str, query: &str, opts: FindOptions) -> Vec<Hit> {
    if query.is_empty() {
        return Vec::new();
    }
    let q_lower = query.to_lowercase();
    let q_bytes = q_lower.as_bytes();
    let query_ascii = query.is_ascii();
    let mut hits = Vec::new();
    for (idx, line) in lines_of(text).into_iter().enumerate() {
        // Case-sensitive matching never lowercases, so it never needs
        // the fold fallback below: `str::find` gives exact byte offsets
        // for ASCII and non-ASCII alike.
        if opts.case_sensitive && (!query_ascii || !line.is_ascii()) {
            for (start, _) in line.match_indices(query) {
                let end = start + query.len();
                if opts.whole_word && !is_whole_word_at(line, start, end) {
                    continue;
                }
                hits.push(Hit {
                    line: (idx as u32) + 1,
                    start,
                    end,
                });
                if hits.len() >= MAX_HITS {
                    return hits;
                }
            }
            continue;
        }
        // Greek and Cyrillic fold without changing the byte length, so a
        // length check never left the ASCII fast path and missed them.
        // The fast path is only for pure ASCII. Anything else is a
        // whole-line hit: one match per line, start and end left at 0
        // (`marked_pieces` already treats that as the whole line).
        if !query_ascii || !line.is_ascii() {
            let folded = line.to_lowercase();
            // Fold fallback: per-hit offsets cannot be trusted, so in
            // whole-word mode the hit's boundaries cannot be verified
            // and it is dropped, not counted (#1419 Q2) — counting it
            // would make the count a lie.
            if !opts.whole_word && folded.contains(&q_lower) {
                hits.push(Hit {
                    line: (idx as u32) + 1,
                    start: 0,
                    end: 0,
                });
            }
        } else {
            // Pure-ASCII fast path. Case-sensitive compares the raw
            // query bytes; case-insensitive compares against the
            // lowercased query, as before.
            let needle: &[u8] = if opts.case_sensitive {
                query.as_bytes()
            } else {
                q_bytes
            };
            let bytes = line.as_bytes();
            if bytes.len() < needle.len() || needle.is_empty() {
                continue;
            }
            let mut i = 0;
            while i + needle.len() <= bytes.len() {
                let same = if opts.case_sensitive {
                    &bytes[i..i + needle.len()] == needle
                } else {
                    bytes[i..i + needle.len()]
                        .iter()
                        .zip(needle)
                        .all(|(a, b)| a.to_ascii_lowercase() == *b)
                };
                if same {
                    if opts.whole_word && !is_whole_word_at(line, i, i + needle.len()) {
                        // Not a whole word: advance one byte so a
                        // later whole-word hit on this line is found.
                        i += 1;
                        continue;
                    }
                    hits.push(Hit {
                        line: (idx as u32) + 1,
                        start: i,
                        end: i + needle.len(),
                    });
                    i += needle.len().max(1);
                    if hits.len() >= MAX_HITS {
                        return hits;
                    }
                } else {
                    i += 1;
                }
            }
        }
        if hits.len() >= MAX_HITS {
            break;
        }
    }
    hits
}

pub fn highlight_line(lang: &str, line: &str) -> Vec<Tok> {
    let marker = comment_marker(lang);
    let keys = keywords(lang);
    let bytes = line.as_bytes();
    let mut out: Vec<Tok> = Vec::new();
    let mut i = 0;
    let mut plain = String::new();
    let flush = |plain: &mut String, out: &mut Vec<Tok>| {
        if !plain.is_empty() {
            out.push(Tok {
                kind: "",
                text: std::mem::take(plain),
            });
        }
    };
    while i < bytes.len() {
        if !marker.is_empty() && line[i..].starts_with(marker) {
            flush(&mut plain, &mut out);
            out.push(Tok {
                kind: "ev-com",
                text: line[i..].to_string(),
            });
            break;
        }
        let ch = line[i..].chars().next().unwrap();
        if ch == '"' || ch == '\'' || ch == '`' {
            flush(&mut plain, &mut out);
            let (tok, n) = scan_string(line, i, ch);
            out.push(tok);
            i += n;
            continue;
        }
        if ch.is_ascii_digit() && (i == 0 || !is_ident(bytes[i - 1] as char)) {
            flush(&mut plain, &mut out);
            let start = i;
            while i < bytes.len() && (bytes[i] as char).is_ascii_digit() {
                i += 1;
            }
            out.push(Tok {
                kind: "ev-num",
                text: line[start..i].to_string(),
            });
            continue;
        }
        if is_ident(ch) {
            let start = i;
            i += ch.len_utf8();
            while i < bytes.len() {
                let c = line[i..].chars().next().unwrap();
                if !is_ident(c) && !c.is_ascii_digit() {
                    break;
                }
                i += c.len_utf8();
            }
            let word = &line[start..i];
            if keys.iter().any(|k| *k == word) {
                flush(&mut plain, &mut out);
                out.push(Tok {
                    kind: "ev-kw",
                    text: word.to_string(),
                });
            } else {
                plain.push_str(word);
            }
            continue;
        }
        plain.push(ch);
        i += ch.len_utf8();
    }
    flush(&mut plain, &mut out);
    out
}

fn scan_string(line: &str, start: usize, quote: char) -> (Tok, usize) {
    let bytes = line.as_bytes();
    let mut i = start + quote.len_utf8();
    while i < bytes.len() {
        let ch = line[i..].chars().next().unwrap();
        if ch == '\\' {
            i += ch.len_utf8();
            if i < bytes.len() {
                let n = line[i..].chars().next().unwrap();
                i += n.len_utf8();
            }
            continue;
        }
        i += ch.len_utf8();
        if ch == quote {
            break;
        }
    }
    (
        Tok {
            kind: "ev-str",
            text: line[start..i].to_string(),
        },
        i - start,
    )
}

fn is_ident(c: char) -> bool {
    c == '_' || c.is_ascii_alphabetic()
}

fn comment_marker(lang: &str) -> &'static str {
    match lang {
        "py" | "sh" | "yaml" | "toml" => "#",
        "sql" => "--",
        "text" | "markdown" | "json" | "html" => "",
        _ => "//",
    }
}

fn keywords(lang: &str) -> &'static [&'static str] {
    match lang {
        "go" => &[
            "func", "package", "import", "return", "if", "else", "for", "range", "var", "const",
            "type", "struct", "interface", "map", "chan", "go", "defer", "select", "switch",
            "case", "break", "continue", "default", "nil", "true", "false",
        ],
        "rust" => &[
            "fn", "let", "mut", "pub", "use", "mod", "struct", "enum", "impl", "trait", "return",
            "if", "else", "for", "while", "loop", "match", "async", "await", "move", "self",
            "true", "false", "where", "type", "const", "static", "crate",
        ],
        "js" | "ts" => &[
            "function", "const", "let", "var", "return", "if", "else", "for", "while", "class",
            "import", "export", "from", "async", "await", "new", "this", "true", "false", "null",
            "undefined",
        ],
        "py" => &[
            "def", "class", "return", "if", "elif", "else", "for", "while", "import", "from", "as",
            "try", "except", "with", "lambda", "True", "False", "None", "and", "or", "not", "in",
            "pass", "yield",
        ],
        "sh" => &[
            "if", "then", "else", "fi", "for", "in", "do", "done", "case", "esac", "function",
            "return",
        ],
        _ => &[],
    }
}

/// Turns `strike-file:path#L12`, `strike-file:path:12`, and `path.ext:12`
/// into links. The label is the matched text. `..`, absolute paths, and URLs
/// are left as text.
pub fn linkify(input: &str) -> Vec<Chunk> {
    let mut out = Vec::new();
    let mut buf = String::new();
    let mut i = 0;
    while i < input.len() {
        if let Some((len, path, line)) = match_strike_file(&input[i..]) {
            push_text(&mut out, &mut buf);
            let label = input[i..i + len].to_string();
            out.push(Chunk::Link { path, line, label });
            i += len;
            continue;
        }
        if let Some((len, path, line)) = match_path_line(input, i) {
            push_text(&mut out, &mut buf);
            let label = input[i..i + len].to_string();
            out.push(Chunk::Link { path, line, label });
            i += len;
            continue;
        }
        let ch = input[i..].chars().next().unwrap();
        buf.push(ch);
        i += ch.len_utf8();
    }
    push_text(&mut out, &mut buf);
    if out.is_empty() {
        out.push(Chunk::Text(String::new()));
    }
    out
}

fn push_text(out: &mut Vec<Chunk>, buf: &mut String) {
    if !buf.is_empty() {
        out.push(Chunk::Text(std::mem::take(buf)));
    }
}

fn match_strike_file(s: &str) -> Option<(usize, String, u32)> {
    const PREFIX: &str = "strike-file:";
    if !s.starts_with(PREFIX) {
        return None;
    }
    let rest = &s[PREFIX.len()..];
    let (path, line, consumed) = strike_target(rest)?;
    Some((PREFIX.len() + consumed, path, line))
}

fn strike_target(s: &str) -> Option<(String, u32, usize)> {
    let bytes = s.as_bytes();
    let mut i = 0;
    while i < bytes.len() && is_path_byte(bytes[i]) {
        i += 1;
    }
    if i == 0 {
        return None;
    }
    let path = s[..i].to_string();
    if !safe_rel_path(&path) {
        return None;
    }
    if s[i..].starts_with("#L") {
        let (line, n) = take_line(&s[i + 2..])?;
        return Some((path, line, i + 2 + n));
    }
    if s[i..].starts_with(':') {
        let (line, n) = take_line(&s[i + 1..])?;
        return Some((path, line, i + 1 + n));
    }
    None
}

fn match_path_line(input: &str, i: usize) -> Option<(usize, String, u32)> {
    if i > 0 {
        let prev = input[..i].chars().next_back().unwrap();
        if is_path_char(prev) {
            return None;
        }
    }
    let look = &input[i.saturating_sub(12)..i];
    if look.contains("://") {
        return None;
    }
    let s = &input[i..];
    let bytes = s.as_bytes();
    let mut j = 0;
    while j < bytes.len() && is_path_byte(bytes[j]) {
        j += 1;
    }
    if j == 0 || !s[..j].contains('.') {
        return None;
    }
    if !s[j..].starts_with(':') {
        return None;
    }
    let (line, n) = take_line(&s[j + 1..])?;
    let mut end = j + 1 + n;
    if s[end..].starts_with(':') {
        if let Some((_, col_n)) = take_line(&s[end + 1..]) {
            end += 1 + col_n;
        }
    }
    let path = s[..j].to_string();
    if !safe_rel_path(&path) || !ext_has_letter(&path) {
        return None;
    }
    Some((end, path, line))
}

fn take_line(s: &str) -> Option<(u32, usize)> {
    let bytes = s.as_bytes();
    let mut n = 0;
    while n < bytes.len() && bytes[n].is_ascii_digit() && n < 7 {
        n += 1;
    }
    if n == 0 {
        return None;
    }
    let line: u32 = s[..n].parse().ok()?;
    if line == 0 || line > 1_000_000 {
        return None;
    }
    Some((line, n))
}

fn is_path_byte(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b == b'_' || b == b'.' || b == b'/' || b == b'-'
}

fn is_path_char(c: char) -> bool {
    c.is_ascii_alphanumeric() || c == '_' || c == '.' || c == '/' || c == '-'
}

fn safe_rel_path(path: &str) -> bool {
    if path.is_empty() || path.starts_with('/') || path.starts_with('\\') {
        return false;
    }
    !path.split('/').any(|p| p.is_empty() || p == "." || p == "..")
}

fn ext_has_letter(path: &str) -> bool {
    let ext = path.rsplit_once('.').map(|(_, e)| e).unwrap_or("");
    ext.chars().any(|c| c.is_ascii_alphabetic()) && ext.len() <= 8
}

/// Splits one pipe-table row into trimmed cells. A pipe escaped as `\|`
/// and a pipe inside a `code span` do not split. A leading or trailing
/// pipe is a border, not an empty cell, so rows may omit either one.
fn split_table_row(line: &str) -> Vec<String> {
    let trimmed = line.trim();
    let mut cells: Vec<String> = Vec::new();
    let mut cell = String::new();
    let mut in_code = false;
    let mut chars = trimmed.chars().peekable();
    while let Some(ch) = chars.next() {
        match ch {
            '\\' => match chars.peek() {
                Some('|') => {
                    cell.push('|');
                    chars.next();
                }
                Some('\\') => {
                    cell.push('\\');
                    chars.next();
                }
                _ => cell.push('\\'),
            },
            '`' => {
                in_code = !in_code;
                cell.push('`');
            }
            '|' if !in_code => cells.push(std::mem::take(&mut cell)),
            _ => cell.push(ch),
        }
    }
    cells.push(cell);
    if trimmed.starts_with('|') && !cells.is_empty() {
        cells.remove(0);
    }
    if trimmed.ends_with('|') && !trimmed.ends_with("\\|") && cells.len() > 1 {
        cells.pop();
    }
    cells.iter().map(|c| c.trim().to_string()).collect()
}

/// Parses a GFM separator row (`---`, `:---`, `---:`, `:---:`) into
/// per-column alignments. Returns `None` when any cell is not a
/// colon/dash marker, so ordinary pipe text never qualifies.
fn parse_separator_row(line: &str) -> Option<Vec<MdAlign>> {
    if !line.contains('|') || !line.contains('-') {
        return None;
    }
    let cells = split_table_row(line);
    if cells.is_empty() {
        return None;
    }
    let mut aligns = Vec::with_capacity(cells.len());
    for cell in &cells {
        let mut core = cell.as_str();
        let mut left = false;
        let mut right = false;
        if let Some(rest) = core.strip_prefix(':') {
            left = true;
            core = rest;
        }
        if let Some(rest) = core.strip_suffix(':') {
            right = true;
            core = rest;
        }
        if core.is_empty() || !core.chars().all(|c| c == '-') {
            return None;
        }
        aligns.push(match (left, right) {
            (true, true) => MdAlign::Center,
            (true, false) => MdAlign::Left,
            (false, true) => MdAlign::Right,
            (false, false) => MdAlign::None,
        });
    }
    Some(aligns)
}

/// Tries to read a GFM pipe table starting at `lines[start]` (header row,
/// separator row, then body rows). Returns the block parts and the number
/// of lines consumed. Any malformation — missing separator, header /
/// separator column mismatch, or a ragged body row — returns `None` so
/// the caller falls back to paragraph rendering and no text is lost.
fn table_at(
    lines: &[&str],
    start: usize,
) -> Option<(Vec<String>, Vec<MdAlign>, Vec<Vec<String>>, usize)> {
    if start + 1 >= lines.len() {
        return None;
    }
    let header_line = lines[start].trim();
    if !header_line.contains('|') || parse_separator_row(header_line).is_some() {
        return None;
    }
    let aligns = parse_separator_row(lines[start + 1])?;
    let header = split_table_row(header_line);
    if header.is_empty() || header.len() != aligns.len() {
        return None;
    }
    let mut rows = Vec::new();
    let mut i = start + 2;
    while i < lines.len() {
        let t = lines[i].trim();
        if t.is_empty() || !t.contains('|') {
            break;
        }
        let cells = split_table_row(t);
        if cells.len() != header.len() {
            return None;
        }
        rows.push(cells);
        i += 1;
    }
    Some((header, aligns, rows, i - start))
}

/// True when `src` contains at least one well-formed markdown table.
/// The transcript uses this to pick the markdown renderer only for
/// table-bearing prose, leaving every other bubble untouched.
pub fn has_markdown_table(src: &str) -> bool {
    markdown_blocks(src)
        .iter()
        .any(|b| matches!(b, MdBlock::Table { .. }))
}

fn align_class(align: MdAlign) -> &'static str {
    match align {
        MdAlign::Left => "md-left",
        MdAlign::Center => "md-center",
        MdAlign::Right => "md-right",
        MdAlign::None => "",
    }
}

pub fn markdown_blocks(src: &str) -> Vec<MdBlock> {
    let mut blocks = Vec::new();
    let lines = lines_of(src);
    let mut i = 0;
    while i < lines.len() {
        let line = lines[i];
        let trimmed = line.trim();
        if trimmed.is_empty() {
            i += 1;
            continue;
        }
        if trimmed.starts_with("```") {
            i += 1;
            let mut body = String::new();
            while i < lines.len() && !lines[i].trim().starts_with("```") {
                if !body.is_empty() {
                    body.push('\n');
                }
                body.push_str(lines[i]);
                i += 1;
            }
            if i < lines.len() {
                i += 1;
            }
            blocks.push(MdBlock::Code(body));
            continue;
        }
        if trimmed == "---" || trimmed == "***" {
            blocks.push(MdBlock::Rule);
            i += 1;
            continue;
        }
        if let Some(level) = heading_level(trimmed) {
            let text = trimmed[level..].trim().to_string();
            blocks.push(MdBlock::Heading(level as u8, text));
            i += 1;
            continue;
        }
        if trimmed.starts_with(">") {
            let mut body = String::new();
            while i < lines.len() && lines[i].trim().starts_with('>') {
                if !body.is_empty() {
                    body.push(' ');
                }
                body.push_str(lines[i].trim().trim_start_matches('>').trim());
                i += 1;
            }
            blocks.push(MdBlock::Quote(body));
            continue;
        }
        if list_item(trimmed).is_some() {
            let mut items = Vec::new();
            while i < lines.len() {
                let Some(item) = list_item(lines[i].trim()) else {
                    break;
                };
                items.push(item);
                i += 1;
            }
            blocks.push(MdBlock::List(items));
            continue;
        }
        if let Some((header, aligns, rows, used)) = table_at(&lines, i) {
            blocks.push(MdBlock::Table {
                header,
                aligns,
                rows,
            });
            i += used;
            continue;
        }
        let mut body = String::new();
        while i < lines.len() {
            let t = lines[i].trim();
            if t.is_empty()
                || t.starts_with("```")
                || heading_level(t).is_some()
                || t.starts_with('>')
                || list_item(t).is_some()
                || table_at(&lines, i).is_some()
            {
                break;
            }
            if !body.is_empty() {
                body.push(' ');
            }
            body.push_str(t);
            i += 1;
        }
        blocks.push(MdBlock::Paragraph(body));
    }
    blocks
}

fn heading_level(line: &str) -> Option<usize> {
    let n = line.chars().take_while(|c| *c == '#').count();
    if (1..=3).contains(&n) && line.as_bytes().get(n) == Some(&b' ') {
        Some(n)
    } else {
        None
    }
}

fn list_item(line: &str) -> Option<String> {
    let rest = if let Some(r) = line.strip_prefix("- ") {
        r
    } else if let Some(r) = line.strip_prefix("* ") {
        r
    } else {
        return None;
    };
    Some(rest.to_string())
}

#[derive(Clone, Debug, PartialEq, Eq)]
enum Inline {
    Text(String),
    Code(String),
    Strong(String),
    Em(String),
    Link { text: String, href: String },
}

fn inline_parse(src: &str) -> Vec<Inline> {
    let mut out = Vec::new();
    let mut buf = String::new();
    let b = src.as_bytes();
    let mut i = 0;
    let flush = |buf: &mut String, out: &mut Vec<Inline>| {
        if !buf.is_empty() {
            out.push(Inline::Text(std::mem::take(buf)));
        }
    };
    while i < b.len() {
        if b[i] == b'`' {
            if let Some(end) = src[i + 1..].find('`') {
                flush(&mut buf, &mut out);
                out.push(Inline::Code(src[i + 1..i + 1 + end].to_string()));
                i += end + 2;
                continue;
            }
        }
        if src[i..].starts_with("**") {
            if let Some(end) = src[i + 2..].find("**") {
                flush(&mut buf, &mut out);
                out.push(Inline::Strong(src[i + 2..i + 2 + end].to_string()));
                i += end + 4;
                continue;
            }
        }
        if b[i] == b'*' {
            if let Some(end) = src[i + 1..].find('*') {
                if end > 0 {
                    flush(&mut buf, &mut out);
                    out.push(Inline::Em(src[i + 1..i + 1 + end].to_string()));
                    i += end + 2;
                    continue;
                }
            }
        }
        if b[i] == b'[' {
            if let Some((text, href, n)) = parse_link(&src[i..]) {
                flush(&mut buf, &mut out);
                out.push(Inline::Link { text, href });
                i += n;
                continue;
            }
        }
        let ch = src[i..].chars().next().unwrap();
        buf.push(ch);
        i += ch.len_utf8();
    }
    flush(&mut buf, &mut out);
    out
}

fn parse_link(s: &str) -> Option<(String, String, usize)> {
    let close = s.find(']')?;
    if close < 2 || !s[close..].starts_with("](") {
        return None;
    }
    let rest = &s[close + 2..];
    let href_end = rest.find(')')?;
    let href = rest[..href_end].trim();
    if !(href.starts_with("https://") || href.starts_with("http://")) {
        return None;
    }
    if href.contains(char::is_whitespace) {
        return None;
    }
    let text = s[1..close].to_string();
    Some((text, href.to_string(), close + 2 + href_end + 1))
}

fn http_message(err: &str) -> String {
    if let Some(idx) = err.find('{') {
        if let Ok(v) = serde_json::from_str::<Value>(&err[idx..]) {
            if let Some(msg) = v.get("error").and_then(|e| e.as_str()) {
                if !msg.is_empty() {
                    return msg.to_string();
                }
            }
        }
    }
    err.to_string()
}

fn conflict_token(err: &str) -> Option<(String, String)> {
    let idx = err.find('{')?;
    let v: Value = serde_json::from_str(&err[idx..]).ok()?;
    if v.get("code").and_then(|c| c.as_str()) != Some("conflict") {
        return None;
    }
    Some((
        v.get("sha256").and_then(|s| s.as_str()).unwrap_or("").to_string(),
        v.get("mtimeUnixNano")
            .and_then(|s| s.as_str())
            .unwrap_or("")
            .to_string(),
    ))
}

fn scroll_line(line: u32) {
    if line == 0 {
        return;
    }
    let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
        return;
    };
    let Some(el) = doc.get_element_by_id(&format!("ev-line-{line}")) else {
        return;
    };
    el.scroll_into_view_with_bool(false);
}

fn scroll_line_soon(line: u32) {
    if line == 0 {
        return;
    }
    let Some(w) = web_sys::window() else {
        return;
    };
    let cb = Closure::<dyn FnMut()>::new(move || scroll_line(line));
    let _ = w.set_timeout_with_callback_and_timeout_and_arguments_0(
        cb.as_ref().unchecked_ref(),
        0,
    );
    cb.forget();
}

fn focus_search() {
    let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
        return;
    };
    let Some(el) = doc.get_element_by_id("ev-search") else {
        return;
    };
    if let Some(html) = el.dyn_ref::<web_sys::HtmlElement>() {
        let _ = html.focus();
    }
}

#[derive(Deserialize)]
struct DocBody {
    path: String,
    #[serde(default)]
    content: String,
    #[serde(default)]
    sha256: String,
    #[serde(default, rename = "mtimeUnixNano")]
    mtime_unix_nano: String,
    #[serde(default)]
    binary: bool,
    #[serde(default)]
    truncated: bool,
    #[serde(default)]
    notice: String,
    #[serde(default)]
    writable: bool,
    #[serde(default, rename = "writeBlock")]
    write_block: String,
}

#[derive(Deserialize)]
struct DiffFile {
    path: String,
    #[serde(default)]
    diff: String,
}

#[derive(Deserialize)]
struct DiffList {
    #[serde(default)]
    files: Vec<DiffFile>,
}

#[component]
pub fn LinkedBody(
    text: String,
    kind: &'static str,
    doc_path: RwSignal<String>,
    doc_line: RwSignal<u32>,
    doc_diff: RwSignal<bool>,
) -> impl IntoView {
    let chunks = linkify(&text);
    let inner = chunks
        .into_iter()
        .map(|chunk| match chunk {
            Chunk::Text(t) => view! { <span class="ev-text">{t}</span> }.into_any(),
            Chunk::Link { path, line, label } => {
                let open_path = path.clone();
                view! {
                    <button
                        type="button"
                        class="ev-pathlink"
                        data-ev-path=path
                        data-ev-line=line.to_string()
                        on:click=move |_| {
                            doc_diff.set(false);
                            doc_line.set(line);
                            doc_path.set(open_path.clone());
                        }
                    >{label}</button>
                }
                .into_any()
            }
        })
        .collect_view();
    match kind {
        "pre" => view! { <pre class="ev-pre">{inner}</pre> }.into_any(),
        "prose" => view! { <p class="prose">{inner}</p> }.into_any(),
        _ => view! { <span class="ev-inline">{inner}</span> }.into_any(),
    }
}

#[component]
fn Inlines(text: String) -> impl IntoView {
    inline_parse(&text)
        .into_iter()
        .map(|part| match part {
            Inline::Text(t) => view! { <span>{t}</span> }.into_any(),
            Inline::Code(t) => view! { <code>{t}</code> }.into_any(),
            Inline::Strong(t) => view! { <strong>{t}</strong> }.into_any(),
            Inline::Em(t) => view! { <em>{t}</em> }.into_any(),
            Inline::Link { text, href } => {
                view! { <a href=href target="_blank" rel="noopener noreferrer">{text}</a> }.into_any()
            }
        })
        .collect_view()
}

#[component]
pub fn MarkdownView(text: String) -> impl IntoView {
    let blocks = markdown_blocks(&text);
    view! {
        <div class="ev-md">
            {blocks.into_iter().map(|block| match block {
                MdBlock::Heading(level, text) => {
                    let inner = view! { <Inlines text=text /> };
                    match level {
                        1 => view! { <h1>{inner}</h1> }.into_any(),
                        2 => view! { <h2>{inner}</h2> }.into_any(),
                        _ => view! { <h3>{inner}</h3> }.into_any(),
                    }
                }
                MdBlock::Paragraph(text) => view! { <p><Inlines text=text /></p> }.into_any(),
                MdBlock::Code(text) => view! { <pre>{text}</pre> }.into_any(),
                MdBlock::Quote(text) => view! { <blockquote><Inlines text=text /></blockquote> }.into_any(),
                MdBlock::List(items) => view! {
                    <ul>
                        {items.into_iter().map(|item| view! { <li><Inlines text=item /></li> }).collect_view()}
                    </ul>
                }.into_any(),
                MdBlock::Rule => view! { <hr /> }.into_any(),
                MdBlock::Table { header, aligns, rows } => {
                    let head_aligns = aligns.clone();
                    view! {
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
                    }.into_any()
                }
            }).collect_view()}
        </div>
    }
}

#[component]
fn SourceView(
    text: String,
    lang: String,
    query: String,
    hits: Vec<Hit>,
    current: usize,
    target_line: u32,
) -> impl IntoView {
    // The whole file is already loaded; only the rendered window is
    // capped (#1431). The window lives in a signal so "Show more" can
    // raise it without a re-fetch, and it starts raised past any find
    // hit / jump target so those rows exist in the DOM. A fresh
    // SourceView is built per file/query/hit change, so the window
    // resets on file switch.
    // Arc, not Rc: Leptos reactive closures in this crate must be Send.
    let lines: Arc<Vec<String>> =
        Arc::new(lines_of(&text).iter().map(|s| s.to_string()).collect());
    let total = lines.len();
    let hits: Arc<Vec<Hit>> = Arc::new(hits);
    let current_hit = hits.get(current).cloned();
    let target = if query.is_empty() {
        target_line
    } else {
        current_hit
            .as_ref()
            .map(|h| h.line)
            .unwrap_or(target_line)
    };
    let limit = RwSignal::new(initial_render_limit(total, target));
    view! {
        <div class="ev-codeview">
            {move || {
                let rows_lines = lines.clone();
                let rows_hits = hits.clone();
                let rows_hit = current_hit.clone();
                let lang = lang.clone();
                let query = query.clone();
                rows_lines
                    .iter()
                    .take(limit.get())
                    .enumerate()
                    .map(|(idx, line)| {
                        let n = (idx as u32) + 1;
                        let line_hits: Vec<&Hit> =
                            rows_hits.iter().filter(|h| h.line == n).collect();
                        let jumped = rows_hit.as_ref().map(|h| h.line) == Some(n);
                        let row_class = if jumped { "ev-line ev-jump" } else { "ev-line" };
                        let body = if query.is_empty() {
                            highlight_line(&lang, line)
                                .into_iter()
                                .map(|tok| {
                                    let class = tok.kind.to_string();
                                    view! { <span class=class>{tok.text}</span> }.into_any()
                                })
                                .collect_view()
                                .into_any()
                        } else {
                            marked_pieces(
                                line,
                                &line_hits,
                                rows_hit.as_ref().filter(|h| h.line == n),
                            )
                                .into_iter()
                                .map(|(class, piece)| {
                                    view! { <span class=class>{piece}</span> }.into_any()
                                })
                                .collect_view()
                                .into_any()
                        };
                        view! {
                            <div class=row_class id=format!("ev-line-{n}")>
                                <span class="ev-gutter">{n.to_string()}</span>
                                <span>{body}</span>
                            </div>
                        }
                    })
                    .collect_view()
            }}
            {move || {
                let hidden = total.saturating_sub(limit.get());
                if hidden == 0 {
                    if total > MAX_RENDER_LINES {
                        return view! {
                            <p class="hint">{format!("Showing all {total} lines.")}</p>
                        }
                        .into_any();
                    }
                    return ().into_any();
                }
                let step = hidden.min(MAX_RENDER_LINES);
                view! {
                    <p class="hint">
                        {format!("{hidden} more lines are not shown. Search still counts the whole file.")}
                    </p>
                    <button
                        type="button"
                        class="secondary"
                        data-ev-show-more=true
                        on:click=move |_| {
                            limit.update(|v| *v = render_limit_after_more(*v, total));
                        }
                    >
                        {format!("Show {step} more lines ({hidden} remaining)")}
                    </button>
                    <button
                        type="button"
                        class="secondary"
                        data-ev-show-all=true
                        on:click=move |_| limit.set(total)
                    >
                        "Show all lines"
                    </button>
                }
                .into_any()
            }}
        </div>
    }
}

fn marked_pieces(line: &str, hits: &[&Hit], current: Option<&Hit>) -> Vec<(String, String)> {
    if hits.is_empty() {
        return vec![("".into(), line.to_string())];
    }
    if hits.iter().any(|h| h.end == 0) {
        let class = if current.is_some() {
            "ev-mark ev-current"
        } else {
            "ev-mark"
        };
        return vec![(class.into(), line.to_string())];
    }
    let mut pieces = Vec::new();
    let mut cursor = 0;
    for hit in hits {
        let start = hit.start.min(line.len());
        let end = hit.end.min(line.len());
        if start < cursor || end < start {
            continue;
        }
        if cursor < start {
            pieces.push(("".into(), line[cursor..start].to_string()));
        }
        let class = if current.is_some_and(|c| c.start == hit.start && c.end == hit.end) {
            "ev-mark ev-current"
        } else {
            "ev-mark"
        };
        pieces.push((class.into(), line[start..end].to_string()));
        cursor = end;
    }
    if cursor < line.len() {
        pieces.push(("".into(), line[cursor..].to_string()));
    }
    pieces
}

#[component]
fn DiffView(text: String) -> impl IntoView {
    if text.is_empty() {
        return view! {
            <div class="ev-diff">
                <p class="hint">"No agent diff for this file. The Changes tab lists git diffs for the working directory."</p>
            </div>
        }
        .into_any();
    }
    // Keep the full diff and render a capped window by default (#1434):
    // expanding renders more of the text already held here, never a
    // re-fetch. The cap counts characters; cuts land on line ends.
    let full: Arc<String> = Arc::new(text);
    let full_footer = full.clone();
    let shown = RwSignal::new(MAX_DIFF_CHARS);
    view! {
        <div class="ev-diff">
            {move || {
                let visible = diff_visible(&full, shown.get());
                let lines: Vec<String> = visible.split('\n').map(|s| s.to_string()).collect();
                view! {
                    <pre>
                        {lines.into_iter().map(|line| {
                            let class = if line.starts_with('+') && !line.starts_with("+++") {
                                "ev-add"
                            } else if line.starts_with('-') && !line.starts_with("---") {
                                "ev-del"
                            } else if line.starts_with("@@") {
                                "ev-hunk"
                            } else {
                                "ev-ctx"
                            };
                            view! { <div class=class>{line}</div> }
                        }).collect_view()}
                    </pre>
                }
            }}
            {move || {
                let (hidden_chars, hidden_lines) = diff_hidden(&full_footer, shown.get());
                let expanded = shown.get() > MAX_DIFF_CHARS;
                if hidden_chars == 0 && !expanded {
                    return ().into_any();
                }
                view! {
                    <Show when=move || hidden_chars != 0>
                        <p class="hint">
                            {format!("{hidden_lines} more diff lines are not shown ({hidden_chars} characters hidden).")}
                        </p>
                        <button
                            type="button"
                            class="secondary"
                            data-agent-diff-expand=true
                            on:click=move |_| {
                                shown.update(|v| *v = v.saturating_add(MAX_DIFF_CHARS));
                            }
                        >
                            "Show 20,000 more characters"
                        </button>
                        <button
                            type="button"
                            class="secondary"
                            data-agent-diff-expand-all=true
                            on:click=move |_| shown.set(usize::MAX)
                        >
                            "Show full diff"
                        </button>
                    </Show>
                    <Show when=move || expanded>
                        <button
                            type="button"
                            class="secondary"
                            data-agent-diff-collapse=true
                            on:click=move |_| shown.set(MAX_DIFF_CHARS)
                        >
                            "Show less"
                        </button>
                    </Show>
                }
                .into_any()
            }}
        </div>
    }
    .into_any()
}

/// Whether an Agent-diff completion may still commit: it was started
/// for the file still open (`started_path` vs `open_path`) at a
/// generation that is still the latest (`started_gen` vs
/// `current_gen`). The generation half also rejects a completion for
/// the *same* path when a newer fetch started in between.
fn diff_current(started_gen: u32, current_gen: u32, open_path: &str, started_path: &str) -> bool {
    started_gen == current_gen && open_path == started_path
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum SaveLanding {
    Commit,
    Ignore,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum SaveOutcome {
    Ok,
    Conflict,
    Error,
}

struct SaveEffects {
    clear_saving: bool,
    write_body: bool,
    conflict: bool,
    report_error: bool,
    status_saved: bool,
}

/// Commit only while this is still the file and generation the save started
/// for. Reopening the same path bumps the generation, so a late completion
/// does not overwrite the reload.
fn save_landing(saved_path: &str, saved_gen: u32, path_now: &str, gen_now: u32) -> SaveLanding {
    if !saved_path.is_empty() && saved_path == path_now && saved_gen == gen_now {
        SaveLanding::Commit
    } else {
        SaveLanding::Ignore
    }
}

fn save_effects(landing: SaveLanding, outcome: SaveOutcome) -> SaveEffects {
    let clear_saving = true;
    match (landing, outcome) {
        (SaveLanding::Ignore, _) => SaveEffects {
            clear_saving,
            write_body: false,
            conflict: false,
            report_error: false,
            status_saved: false,
        },
        (SaveLanding::Commit, SaveOutcome::Ok) => SaveEffects {
            clear_saving,
            write_body: true,
            conflict: false,
            report_error: false,
            status_saved: true,
        },
        (SaveLanding::Commit, SaveOutcome::Conflict) => SaveEffects {
            clear_saving,
            write_body: false,
            conflict: true,
            report_error: false,
            status_saved: false,
        },
        (SaveLanding::Commit, SaveOutcome::Error) => SaveEffects {
            clear_saving,
            write_body: false,
            conflict: false,
            report_error: true,
            status_saved: false,
        },
    }
}

#[component]
pub fn DocumentPane(
    base: RwSignal<String>,
    token: RwSignal<String>,
    path: RwSignal<String>,
    jump_line: RwSignal<u32>,
    want_diff: RwSignal<bool>,
    editing: RwSignal<bool>,
) -> impl IntoView {
    let content = RwSignal::new(String::new());
    let draft = RwSignal::new(String::new());
    let sha = RwSignal::new(String::new());
    let mtime = RwSignal::new(String::new());
    let loaded_path = RwSignal::new(String::new());
    let binary = RwSignal::new(false);
    let truncated = RwSignal::new(false);
    let writable = RwSignal::new(false);
    let write_block = RwSignal::new(String::new());
    let notice = RwSignal::new(String::new());
    let status = RwSignal::new(String::new());
    let loading = RwSignal::new(false);
    let saving = RwSignal::new(false);
    let conflict = RwSignal::new(false);
    let query = RwSignal::new(String::new());
    // Find options (#1409/#1419): per-bar, in-memory only, both off by
    // default (today's case-insensitive substring behaviour).
    let match_case = RwSignal::new(false);
    let whole_word = RwSignal::new(false);
    let hits = RwSignal::new(Vec::<Hit>::new());
    let hit_idx = RwSignal::new(0usize);
    let preview = RwSignal::new(false);
    let show_diff = RwSignal::new(false);
    let agent_diff = RwSignal::new(String::new());
    let diff_note = RwSignal::new(String::new());
    let reload_tick = RwSignal::new(0u32);
    let load_gen = RwSignal::new(0u32);
    let diff_gen = RwSignal::new(0u32);

    Effect::new(move |_| {
        let text = if editing.get() { draft.get() } else { content.get() };
        let q = query.get();
        let opts = FindOptions::new(match_case.get(), whole_word.get());
        hits.set(find_hits(&text, &q, opts));
        hit_idx.set(0);
    });

    Effect::new(move |_| {
        let hs = hits.get();
        let idx = hit_idx.get();
        let line = if query.get().is_empty() {
            jump_line.get()
        } else {
            hs.get(idx).map(|h| h.line).unwrap_or(0)
        };
        scroll_line_soon(line);
    });

    Effect::new(move |_| {
        let _ = reload_tick.get();
        let p = path.get();
        let gen = load_gen.get_untracked() + 1;
        load_gen.set(gen);
        editing.set(false);
        conflict.set(false);
        query.set(String::new());
        // The diff belongs to the file it was fetched for (#1626): a
        // previous file's diff must never render under the new one,
        // not even while the new fetch is in flight or after it fails.
        agent_diff.set(String::new());
        diff_note.set(String::new());
        if p.is_empty() {
            return;
        }
        let diff_on = want_diff.get_untracked();
        let (b, t) = (base.get(), token.get());
        loading.set(true);
        status.set(String::new());
        spawn_local(async move {
            let q = format!("/v1/file/view?path={}", js_encode(&p));
            match get_json::<DocBody>(&b, &t, &q).await {
                Ok(doc) => {
                    if load_gen.get_untracked() != gen {
                        return;
                    }
                    content.set(doc.content.clone());
                    draft.set(doc.content);
                    sha.set(doc.sha256);
                    mtime.set(doc.mtime_unix_nano);
                    loaded_path.set(doc.path.clone());
                    binary.set(doc.binary);
                    truncated.set(doc.truncated);
                    writable.set(doc.writable && !doc.binary && !doc.truncated);
                    write_block.set(doc.write_block);
                    notice.set(doc.notice);
                    preview.set(language_of(&doc.path) == "markdown");
                    show_diff.set(diff_on);
                    loading.set(false);
                }
                Err(e) => {
                    if load_gen.get_untracked() != gen {
                        return;
                    }
                    content.set(String::new());
                    status.set(http_message(&e));
                    loading.set(false);
                    writable.set(false);
                }
            }
        });
    });

    Effect::new(move |_| {
        if !show_diff.get() {
            return;
        }
        let p = loaded_path.get();
        if p.is_empty() {
            return;
        }
        // Generation-guard the completion like the load effect above
        // (#1626): clear up front so nothing stale renders while this
        // fetch is in flight, and drop the completion if a newer diff
        // fetch started or the open file moved on.
        let gen = diff_gen.get_untracked().wrapping_add(1);
        diff_gen.set(gen);
        agent_diff.set(String::new());
        diff_note.set(String::new());
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match get_json::<DiffList>(&b, &t, "/v1/changed-files").await {
                Ok(list) => {
                    if !diff_current(gen, diff_gen.get_untracked(), &loaded_path.get_untracked(), &p) {
                        return;
                    }
                    // Keep the full diff (#1434): DiffView renders the
                    // capped window and expands in place from this text.
                    let diff = list
                        .files
                        .into_iter()
                        .find(|f| f.path == p)
                        .map(|f| f.diff)
                        .unwrap_or_default();
                    agent_diff.set(diff);
                    diff_note.set(String::new());
                }
                Err(e) => {
                    if !diff_current(gen, diff_gen.get_untracked(), &loaded_path.get_untracked(), &p) {
                        return;
                    }
                    // The pane stays empty beside the note — agent_diff
                    // was cleared when this fetch started.
                    diff_note.set(http_message(&e));
                }
            }
        });
    });

    let step = move |delta: i32| {
        let n = hits.get_untracked().len();
        if n == 0 {
            return;
        }
        let cur = hit_idx.get_untracked() as i32;
        let next = (cur + delta).rem_euclid(n as i32) as usize;
        hit_idx.set(next);
    };

    let do_save = move || {
        if saving.get_untracked() || !editing.get_untracked() || !writable.get_untracked() {
            return;
        }
        let p = loaded_path.get_untracked();
        let body = draft.get_untracked();
        let expect = sha.get_untracked();
        let stamp = mtime.get_untracked();
        let saved_gen = load_gen.get_untracked();
        let (b, t) = (base.get_untracked(), token.get_untracked());
        saving.set(true);
        status.set(String::new());
        spawn_local(async move {
            let payload = serde_json::json!({
                "path": p,
                "content": body,
                "sha256": expect,
                "mtimeUnixNano": stamp,
            });
            let outcome = match put_json_value(&b, &t, "/v1/file", payload).await {
                Ok(v) => {
                    let effects = save_effects(
                        save_landing(
                            &p,
                            saved_gen,
                            &loaded_path.get_untracked(),
                            load_gen.get_untracked(),
                        ),
                        SaveOutcome::Ok,
                    );
                    if effects.write_body {
                        sha.set(v.get("sha256").and_then(|x| x.as_str()).unwrap_or("").into());
                        mtime.set(
                            v.get("mtimeUnixNano")
                                .and_then(|x| x.as_str())
                                .unwrap_or("")
                                .into(),
                        );
                        content.set(body.clone());
                        draft.set(body);
                        editing.set(false);
                        conflict.set(false);
                    }
                    if effects.status_saved {
                        status.set("Saved.".into());
                    }
                    effects
                }
                Err(e) => {
                    let kind = if conflict_token(&e).is_some() {
                        SaveOutcome::Conflict
                    } else {
                        SaveOutcome::Error
                    };
                    let effects = save_effects(
                        save_landing(
                            &p,
                            saved_gen,
                            &loaded_path.get_untracked(),
                            load_gen.get_untracked(),
                        ),
                        kind,
                    );
                    if effects.conflict {
                        conflict.set(true);
                        status.set(
                            "This file changed on disk. Reload to see that version. Your edits stay in the box."
                                .into(),
                        );
                    } else if effects.report_error {
                        status.set(http_message(&e));
                    }
                    effects
                }
            };
            if outcome.clear_saving {
                saving.set(false);
            }
        });
    };

    let on_key = move |ev: web_sys::KeyboardEvent| {
        let key = ev.key();
        let cmd = ev.meta_key() || ev.ctrl_key();
        if key == "Escape" {
            ev.stop_propagation();
            if !query.get_untracked().is_empty() {
                query.set(String::new());
                return;
            }
            if editing.get_untracked() {
                draft.set(content.get_untracked());
                editing.set(false);
                conflict.set(false);
                return;
            }
            path.set(String::new());
            return;
        }
        if cmd && key.eq_ignore_ascii_case("f") {
            ev.prevent_default();
            focus_search();
        } else if cmd && key.eq_ignore_ascii_case("s") {
            ev.prevent_default();
            do_save();
        }
    };

    crate::shell::bind(
        "file.preview",
        Rc::new(move |_| {
            if language_of(&loaded_path.get_untracked()) != "markdown" {
                return Err("This file is not Markdown.".into());
            }
            preview.update(|v| *v = !*v);
            Ok(())
        }),
    );
    crate::shell::bind(
        "file.agent-diff",
        Rc::new(move |_| {
            show_diff.update(|v| *v = !*v);
            Ok(())
        }),
    );
    crate::shell::bind(
        "file.copy-path",
        Rc::new(move |_| {
            let path = loaded_path.get_untracked();
            if path.is_empty() {
                return Err("Open a file first.".into());
            }
            if let Some(win) = web_sys::window() {
                let _ = win.navigator().clipboard().write_text(&path);
            }
            notice.set("Copied the path.".into());
            Ok(())
        }),
    );
    crate::shell::bind(
        "file.copy-contents",
        Rc::new(move |_| {
            let text = content.get_untracked();
            if text.is_empty() {
                return Err("Open a file first.".into());
            }
            if let Some(win) = web_sys::window() {
                let _ = win.navigator().clipboard().write_text(&text);
            }
            notice.set("Copied the file.".into());
            Ok(())
        }),
    );
    on_cleanup(|| {
        for id in ["file.preview", "file.agent-diff", "file.copy-path", "file.copy-contents"] {
            crate::shell::unbind(id);
        }
    });

    view! {
        <div
            class="ev-doc"
            tabindex="0"
            data-ev-root="open"
            data-ev-path=move || loaded_path.get()
            data-ev-sha=move || sha.get()
            on:keydown=on_key
        >
            <div class="ev-bar">
                <span class="ev-path">{move || {
                    let p = loaded_path.get();
                    if p.is_empty() { path.get() } else { p }
                }}</span>
                <span class="hint">{move || language_of(&loaded_path.get())}</span>
                <input
                    id="ev-search"
                    class="ev-search"
                    type="search"
                    placeholder="Find in file"
                    aria-label="Find in file"
                    prop:value=move || query.get()
                    on:input=move |ev| {
                        query.set(event_target_value(&ev));
                        if !query.get_untracked().is_empty() {
                            preview.set(false);
                        }
                    }
                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                        if ev.key() == "Enter" {
                            ev.prevent_default();
                            ev.stop_propagation();
                            step(if ev.shift_key() { -1 } else { 1 });
                        }
                    }
                />
                <span class="ev-count" data-ev-count="">{move || {
                    let n = hits.get().len();
                    if query.get().is_empty() {
                        String::new()
                    } else if n == 0 {
                        "0".into()
                    } else {
                        format!("{}/{}", hit_idx.get() + 1, n)
                    }
                }}</span>
                <button
                    type="button"
                    class=move || if match_case.get() { "chip on ev-find-toggle" } else { "chip ev-find-toggle" }
                    data-ev-match-case="true"
                    title="Match case"
                    aria-label="Match case"
                    aria-pressed=move || if match_case.get() { "true" } else { "false" }
                    on:click=move |_| match_case.update(|v| *v = !*v)
                >"Aa"</button>
                <button
                    type="button"
                    class=move || if whole_word.get() { "chip on ev-find-toggle" } else { "chip ev-find-toggle" }
                    data-ev-whole-word="true"
                    title="Whole word"
                    aria-label="Whole word"
                    aria-pressed=move || if whole_word.get() { "true" } else { "false" }
                    on:click=move |_| whole_word.update(|v| *v = !*v)
                >"ab|"</button>
                <button type="button" class="secondary" on:click=move |_| step(-1)>"Prev"</button>
                <button type="button" class="secondary" on:click=move |_| step(1)>"Next"</button>
                <button type="button" class="secondary" on:click=move |_| path.set(String::new())>"Back to chat"</button>
            </div>
            <Show when=move || !status.get().is_empty()>
                <div class="notice" role="alert" data-ev-status=true>
                    <span>{move || status.get()}</span>
                    <Show when=move || conflict.get()>
                        <button type="button" class="secondary" data-ev-reload=true on:click=move |_| {
                            reload_tick.update(|n| *n += 1);
                        }>"Reload file"</button>
                    </Show>
                </div>
            </Show>
            <Show when=move || !notice.get().is_empty()>
                <p class="hint">{move || notice.get()}</p>
            </Show>
            <Show when=move || loading.get()>
                <p class="hint">"Loading…"</p>
            </Show>
            <Show when=move || binary.get()>
                <p class="hint">"Binary file. Nothing to show."</p>
            </Show>
            <div class="ev-body">
                <Show when=move || !binary.get() && !loading.get() && preview.get()>
                    {move || view! { <MarkdownView text=content.get() /> }}
                </Show>
                <Show when=move || !binary.get() && !loading.get() && !preview.get() && !editing.get()>
                    {move || view! {
                        <SourceView
                            text=content.get()
                            lang=language_of(&loaded_path.get()).to_string()
                            query=query.get()
                            hits=hits.get()
                            current=hit_idx.get()
                            target_line=jump_line.get()
                        />
                    }}
                </Show>
                <Show when=move || editing.get()>
                    <textarea
                        class="ev-editor"
                        aria-label="File editor"
                        spellcheck="false"
                        prop:value=move || draft.get()
                        on:input=move |ev| draft.set(event_target_value(&ev))
                    ></textarea>
                </Show>
                <Show when=move || show_diff.get()>
                    <Show when=move || !diff_note.get().is_empty()>
                        <p class="hint">{move || diff_note.get()}</p>
                    </Show>
                    {move || view! { <DiffView text=agent_diff.get() /> }}
                </Show>
            </div>
            <div class="ev-tools">
                <button
                    type="button"
                    class="secondary"
                    data-ev-edit=true
                    disabled=move || !writable.get() || editing.get()
                    title=move || if write_block.get().is_empty() { "Edit this file".into() } else { write_block.get() }
                    on:click=move |_| {
                        draft.set(content.get());
                        conflict.set(false);
                        preview.set(false);
                        editing.set(true);
                    }
                >"Edit"</button>
                <Show when=move || editing.get()>
                    <button type="button" class="secondary" on:click=move |_| {
                        draft.set(content.get());
                        editing.set(false);
                        conflict.set(false);
                    }>"Cancel"</button>
                    <button type="button" class="primary" data-ev-save=true disabled=move || saving.get() on:click=move |_| do_save()>"Save"</button>
                </Show>
            </div>
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn linkify_paths_and_skips_urls() {
        let chunks = linkify("see webui/src/main.rs:12 and `README.md:3` plus http://example.com/a.go:8");
        let links: Vec<_> = chunks
            .iter()
            .filter_map(|c| match c {
                Chunk::Link { path, line, .. } => Some((path.clone(), *line)),
                _ => None,
            })
            .collect();
        assert_eq!(
            links,
            vec![
                ("webui/src/main.rs".into(), 12),
                ("README.md".into(), 3),
            ]
        );
        let explicit = linkify("open strike-file:docs/theme.md#L4 now");
        match &explicit[1] {
            Chunk::Link { path, line, label } => {
                assert_eq!(path, "docs/theme.md");
                assert_eq!(*line, 4);
                assert!(label.contains("theme.md"));
            }
            other => panic!("{other:?}"),
        }
        assert!(linkify("../secret.txt:1").iter().all(|c| matches!(c, Chunk::Text(_))));
        assert!(linkify("/etc/passwd:1").iter().all(|c| matches!(c, Chunk::Text(_))));
        assert!(linkify("12:30").iter().all(|c| matches!(c, Chunk::Text(_))));
        let rebuilt: String = linkify("webui/src/main.rs:12:4").iter().map(|c| match c {
            Chunk::Text(t) | Chunk::Link { label: t, .. } => t.clone(),
        }).collect();
        assert_eq!(rebuilt, "webui/src/main.rs:12:4");
    }

    #[test]
    fn search_is_case_insensitive_and_numbered() {
        let hits = find_hits("Alpha\nbeta alpha\n", "alpha", FindOptions::default());
        assert_eq!(hits.len(), 2);
        assert_eq!(hits[0].line, 1);
        assert_eq!(hits[1].line, 2);
        assert!(hits[1].start > 0);
        assert!(find_hits("abc", "", FindOptions::default()).is_empty());
        assert!(find_hits("abc", "z", FindOptions::default()).is_empty());
        // Byte positions on the ASCII path stay per-occurrence.
        assert_eq!(hits[0].start, 0);
        assert_eq!(hits[0].end, 5);
    }

    #[test]
    fn search_folds_greek_cyrillic_and_length_changing_latin() {
        for (line, query) in [
            ("Σ\n", "σ"),
            ("σ\n", "Σ"),
            ("Α\n", "α"),
            ("α\n", "Α"),
            ("А\n", "а"),
            ("а\n", "А"),
        ] {
            let hits = find_hits(line, query, FindOptions::default());
            assert_eq!(hits.len(), 1, "line {line:?} query {query:?}");
            assert_eq!(hits[0].line, 1);
            assert_eq!(hits[0].start, 0);
            assert_eq!(hits[0].end, 0);
        }
        // Capital sharp S (U+1E9E) folds to ß and changes the byte length.
        for (line, query) in [("ẞ\n", "ß"), ("ß\n", "ẞ")] {
            let hits = find_hits(line, query, FindOptions::default());
            assert_eq!(hits.len(), 1, "line {line:?} query {query:?}");
            assert_eq!(hits[0].end, 0);
        }
        // İ folds to i + combining dot. The query and the line both fold.
        let hits = find_hits("İstanbul\n", "İ", FindOptions::default());
        assert_eq!(hits.len(), 1);
        assert_eq!(hits[0].end, 0);
        assert!(find_hits("plain ascii\n", "Σ", FindOptions::default()).is_empty());
    }

    #[test]
    fn search_match_case_only_hits_exact_case() {
        let opts = FindOptions::new(true, false);
        let hits = find_hits("Token token TOKEN Token\n", "Token", opts);
        assert_eq!(hits.len(), 2);
        assert_eq!((hits[0].start, hits[0].end), (0, 5));
        assert_eq!((hits[1].start, hits[1].end), (18, 23));
        assert!(find_hits("token\n", "Token", opts).is_empty());
        // Default (off) still folds case, as before #1409.
        assert_eq!(
            find_hits("Token token TOKEN\n", "Token", FindOptions::default()).len(),
            3
        );
    }

    #[test]
    fn search_match_case_does_not_regress_unicode_fold_path() {
        let opts = FindOptions::new(true, false);
        // Exact non-ASCII hits get exact offsets (no fold involved).
        let hits = find_hits("İstanbul\n", "İ", opts);
        assert_eq!(hits.len(), 1);
        assert_eq!((hits[0].start, hits[0].end), (0, "İ".len()));
        // A differently-cased fold partner is NOT a case-sensitive hit,
        // while the default matcher still finds it via the fold path.
        assert!(find_hits("Σ\n", "σ", opts).is_empty());
        assert!(find_hits("ẞ\n", "ß", opts).is_empty());
        assert_eq!(find_hits("ẞ\n", "ß", FindOptions::default()).len(), 1);
        assert_eq!(find_hits("Σ sigma\n", "Σ", opts).len(), 1);
    }

    #[test]
    fn whole_word_predicate_boundaries() {
        assert!(is_whole_word_at("token", 0, 5));
        assert!(is_whole_word_at("a token here", 2, 7));
        assert!(is_whole_word_at("(token).", 1, 6));
        assert!(is_whole_word_at("token,", 0, 5));
        // Underscore and digits are word chars: not boundaries.
        assert!(!is_whole_word_at("my_token", 3, 8));
        assert!(!is_whole_word_at("token2", 0, 5));
        assert!(!is_whole_word_at("tokens", 0, 5));
        assert!(!is_whole_word_at("2token", 1, 6));
        assert!(is_word_char('_') && is_word_char('9') && is_word_char('Z'));
        assert!(!is_word_char('-') && !is_word_char(' '));
    }

    #[test]
    fn search_whole_word_only_hits_whole_words() {
        let opts = FindOptions::new(false, true);
        let hits = find_hits("token tokens tokenize mytoken token.\n", "token", opts);
        assert_eq!(hits.len(), 2);
        assert_eq!((hits[0].start, hits[0].end), (0, 5));
        assert_eq!((hits[1].start, hits[1].end), (30, 35));
        // Whole-word composes with match-case.
        let both = FindOptions::new(true, true);
        assert_eq!(
            find_hits("Token token TOKEN tokens\n", "token", both).len(),
            1
        );
        assert_eq!(
            find_hits("Token token TOKEN tokens\n", "token", opts).len(),
            3
        );
    }

    #[test]
    fn search_whole_word_drops_fold_fallback_hits() {
        // The fold fallback (whole-line marker) cannot verify
        // boundaries, so whole-word mode drops it (#1419 Q2) while the
        // default mode keeps counting it.
        let opts = FindOptions::new(false, true);
        assert!(find_hits("ẞ\n", "ß", opts).is_empty());
        assert!(find_hits("İstanbul\n", "İ", opts).is_empty());
        // Case-sensitive mode never uses the fallback: an exact
        // whole-word non-ASCII hit is still found with real offsets.
        let both = FindOptions::new(true, true);
        let hits = find_hits("say İ now\n", "İ", both);
        assert_eq!(hits.len(), 1);
        assert_eq!(hits[0].start, 4);
    }

    #[test]
    fn highlight_go_keywords_strings_and_comments() {
        let toks = highlight_line("go", "func main() { return \"hi\" } // note");
        assert!(toks.iter().any(|t| t.kind == "ev-kw" && t.text == "func"));
        assert!(toks.iter().any(|t| t.kind == "ev-str" && t.text == "\"hi\""));
        assert!(toks.iter().any(|t| t.kind == "ev-com" && t.text.contains("note")));
        let joined: String = toks.iter().map(|t| t.text.as_str()).collect();
        assert_eq!(joined, "func main() { return \"hi\" } // note");
    }

    #[test]
    fn markdown_blocks_skip_raw_html_as_text() {
        let blocks = markdown_blocks("# Title\n\nHello **bold** and `code`.\n\n- one\n- two\n\n```\nfunc\n```\n\n> quoted\n");
        assert!(matches!(blocks[0], MdBlock::Heading(1, ref t) if t == "Title"));
        assert!(matches!(blocks[1], MdBlock::Paragraph(ref t) if t.contains("**bold**")));
        assert!(matches!(blocks[2], MdBlock::List(ref items) if items.len() == 2));
        assert!(matches!(blocks[3], MdBlock::Code(ref t) if t == "func"));
        assert!(matches!(blocks[4], MdBlock::Quote(ref t) if t == "quoted"));
        let inline = inline_parse("**bold** and [x](javascript:alert(1)) [ok](https://example.com)");
        assert!(inline.iter().any(|p| matches!(p, Inline::Strong(t) if t == "bold")));
        assert!(inline.iter().any(|p| matches!(p, Inline::Link { href, .. } if href == "https://example.com")));
        assert!(!inline.iter().any(|p| matches!(p, Inline::Link { href, .. } if href.contains("javascript"))));
    }

    #[test]
    fn markdown_table_simple() {
        let blocks = markdown_blocks("| Name | Score |\n| --- | --- |\n| alpha | 1 |\n| beta | 2 |\n");
        assert_eq!(blocks.len(), 1);
        match &blocks[0] {
            MdBlock::Table {
                header,
                aligns,
                rows,
            } => {
                assert_eq!(header, &vec!["Name".to_string(), "Score".to_string()]);
                assert_eq!(aligns, &vec![MdAlign::None, MdAlign::None]);
                assert_eq!(rows.len(), 2);
                assert_eq!(rows[0], vec!["alpha".to_string(), "1".to_string()]);
                assert_eq!(rows[1], vec!["beta".to_string(), "2".to_string()]);
            }
            other => panic!("expected table, got {other:?}"),
        }
        assert!(has_markdown_table("| A |\n| - |\n| 1 |"));
        assert!(!has_markdown_table("plain | text\nmore text"));
    }

    #[test]
    fn markdown_table_alignments() {
        let blocks = markdown_blocks("| L | C | R | N |\n| :--- | :---: | ---: | --- |\n| a | b | c | d |\n");
        match &blocks[0] {
            MdBlock::Table { aligns, .. } => assert_eq!(
                aligns,
                &vec![
                    MdAlign::Left,
                    MdAlign::Center,
                    MdAlign::Right,
                    MdAlign::None
                ]
            ),
            other => panic!("expected table, got {other:?}"),
        }
    }

    #[test]
    fn markdown_table_missing_trailing_pipes_and_empty_cells() {
        // Rows may omit the leading and/or trailing border pipe.
        let blocks = markdown_blocks("Name | Score\n--- | ---:\nalpha | 1\n| beta | 2\n");
        match &blocks[0] {
            MdBlock::Table {
                header,
                aligns,
                rows,
            } => {
                assert_eq!(header, &vec!["Name".to_string(), "Score".to_string()]);
                assert_eq!(aligns, &vec![MdAlign::None, MdAlign::Right]);
                assert_eq!(rows[0], vec!["alpha".to_string(), "1".to_string()]);
                assert_eq!(rows[1], vec!["beta".to_string(), "2".to_string()]);
            }
            other => panic!("expected table, got {other:?}"),
        }
        let blocks = markdown_blocks("| A | B |\n|---|---|\n|  | x |\n");
        match &blocks[0] {
            MdBlock::Table { rows, .. } => {
                assert_eq!(rows[0], vec!["".to_string(), "x".to_string()]);
            }
            other => panic!("expected table, got {other:?}"),
        }
    }

    #[test]
    fn markdown_table_escaped_and_code_span_pipes() {
        let blocks = markdown_blocks("| Expr | Note |\n| --- | --- |\n| `a|b` | c\\|d |\n");
        match &blocks[0] {
            MdBlock::Table { rows, .. } => {
                assert_eq!(rows[0], vec!["`a|b`".to_string(), "c|d".to_string()]);
            }
            other => panic!("expected table, got {other:?}"),
        }
        // The code-span cell content still parses as inline code.
        let parts = inline_parse("`a|b`");
        assert!(parts.iter().any(|p| matches!(p, Inline::Code(t) if t == "a|b")));
    }

    #[test]
    fn markdown_table_malformed_falls_back_to_paragraph() {
        // Missing separator row.
        let blocks = markdown_blocks("| A | B |\n| 1 | 2 |\n");
        assert!(matches!(blocks[0], MdBlock::Paragraph(ref t) if t.contains("| A | B |") && t.contains("| 1 | 2 |")));
        // Header / separator column mismatch.
        let blocks = markdown_blocks("| A | B | C |\n| --- | --- |\n| 1 | 2 | 3 |\n");
        assert!(matches!(blocks[0], MdBlock::Paragraph(ref t) if t.contains("| A | B | C |")));
        assert!(!has_markdown_table("| A | B | C |\n| --- | --- |\n| 1 | 2 | 3 |"));
        // Ragged body row: the whole table degrades, no cell text is lost.
        let blocks = markdown_blocks("| A | B |\n| --- | --- |\n| 1 | 2 | 3 |\n");
        assert_eq!(blocks.len(), 1);
        assert!(matches!(blocks[0], MdBlock::Paragraph(ref t) if t.contains("| 1 | 2 | 3 |")));
        // A lone separator-looking line is a rule/paragraph, never a table.
        let blocks = markdown_blocks("| --- | --- |\n");
        assert!(!matches!(blocks[0], MdBlock::Table { .. }));
    }

    #[test]
    fn markdown_table_inside_longer_markdown() {
        let src = "# Report\n\nIntro line\n| File | Tests |\n| :--- | ---: |\n| `a.go` | 12 |\n| `b.go` | 3 |\n\nDone **now**.\n";
        let blocks = markdown_blocks(src);
        assert!(matches!(blocks[0], MdBlock::Heading(1, _)));
        assert!(matches!(blocks[1], MdBlock::Paragraph(ref t) if t == "Intro line"));
        match &blocks[2] {
            MdBlock::Table {
                header,
                aligns,
                rows,
            } => {
                assert_eq!(header, &vec!["File".to_string(), "Tests".to_string()]);
                assert_eq!(aligns, &vec![MdAlign::Left, MdAlign::Right]);
                assert_eq!(rows.len(), 2);
                assert_eq!(rows[0][0], "`a.go`");
            }
            other => panic!("expected table, got {other:?}"),
        }
        assert!(matches!(blocks[3], MdBlock::Paragraph(ref t) if t.contains("**now**")));
        assert!(has_markdown_table(src));
    }

    #[test]
    fn diff_current_scopes_to_file_and_generation() {
        // The normal case: same file, no newer fetch — commit.
        assert!(diff_current(3, 3, "a.rs", "a.rs"));
        // File switched while the fetch was in flight (#1626).
        assert!(!diff_current(3, 3, "b.rs", "a.rs"));
        // Same file, but a newer fetch started — stale completion.
        assert!(!diff_current(3, 4, "a.rs", "a.rs"));
        // A newer fetch for the new file supersedes on both axes.
        assert!(!diff_current(3, 4, "b.rs", "a.rs"));
    }

    #[test]
    fn http_conflict_message() {
        let err = r#"HTTP 409 {"error":"conflict: file changed","code":"conflict","sha256":"abc","mtimeUnixNano":"5"}"#;
        assert_eq!(http_message(err), "conflict: file changed");
        let (sha, mt) = conflict_token(err).unwrap();
        assert_eq!(sha, "abc");
        assert_eq!(mt, "5");
        assert!(conflict_token("HTTP 400 {\"error\":\"nope\"}").is_none());
    }

    #[test]
    fn late_save_does_not_paint_the_next_file() {
        assert_eq!(save_landing("a.rs", 1, "a.rs", 1), SaveLanding::Commit);
        assert_eq!(save_landing("a.rs", 1, "b.rs", 2), SaveLanding::Ignore);
        assert_eq!(save_landing("a.rs", 1, "a.rs", 2), SaveLanding::Ignore);
        let ignored = save_effects(SaveLanding::Ignore, SaveOutcome::Ok);
        assert!(ignored.clear_saving);
        assert!(!ignored.write_body);
        assert!(!ignored.status_saved);
        assert!(!ignored.conflict);
        let late_conflict = save_effects(SaveLanding::Ignore, SaveOutcome::Conflict);
        assert!(!late_conflict.conflict);
        assert!(!late_conflict.report_error);
        assert!(late_conflict.clear_saving);
        let committed = save_effects(SaveLanding::Commit, SaveOutcome::Ok);
        assert!(committed.write_body && committed.status_saved && !committed.conflict);
        let conflict = save_effects(SaveLanding::Commit, SaveOutcome::Conflict);
        assert!(conflict.conflict && !conflict.write_body);
    }

    #[test]
    fn render_limit_starts_at_cap_and_covers_targets() {
        assert_eq!(initial_render_limit(0, 0), 0);
        assert_eq!(initial_render_limit(100, 0), 100);
        assert_eq!(initial_render_limit(5000, 0), MAX_RENDER_LINES);
        // A target exactly at the cap needs no raise.
        assert_eq!(initial_render_limit(5000, 2000), MAX_RENDER_LINES);
        // One past the cap raises a whole chunk.
        assert_eq!(initial_render_limit(5000, 2001), 4000);
        // A deep target raises to the chunk covering it, capped at total.
        assert_eq!(initial_render_limit(5000, 4999), 5000);
        assert_eq!(initial_render_limit(3000, 2500), 3000);
        // A target past the end of file still caps at total.
        assert_eq!(initial_render_limit(3000, 9999), 3000);
    }

    #[test]
    fn render_limit_after_more_grows_by_chunks() {
        assert_eq!(render_limit_after_more(2000, 5000), 4000);
        assert_eq!(render_limit_after_more(4000, 5000), 5000);
        assert_eq!(render_limit_after_more(5000, 5000), 5000);
        assert_eq!(render_limit_after_more(0, 100), 100);
        assert_eq!(render_limit_after_more(2000, 2000), 2000);
    }

    #[test]
    fn diff_window_under_cap_is_the_full_text() {
        let diff = "+one\n-two\n@@ hunk @@\n";
        assert_eq!(diff_visible(diff, MAX_DIFF_CHARS), diff);
        assert_eq!(diff_hidden(diff, MAX_DIFF_CHARS), (0, 0));
        assert_eq!(diff_visible(diff, usize::MAX), diff);
        assert_eq!(diff_hidden(diff, usize::MAX), (0, 0));
    }

    #[test]
    fn diff_window_counts_hidden_exactly() {
        // 300 lines of 99 chars + '\n' = 100 chars per line, 30,000 total.
        let line = format!("{}\n", "x".repeat(99));
        let diff = line.repeat(300);
        let visible = diff_visible(&diff, MAX_DIFF_CHARS);
        assert_eq!(visible.chars().count(), MAX_DIFF_CHARS);
        assert_eq!(diff_hidden(&diff, MAX_DIFF_CHARS), (10_000, 100));
    }

    #[test]
    fn diff_window_extends_a_mid_line_cap_to_the_line_end() {
        // 300 lines of 150 chars + '\n' = 151 chars per line, 45,300 total.
        let line = format!("{}\n", "y".repeat(150));
        let diff = line.repeat(300);
        let visible = diff_visible(&diff, MAX_DIFF_CHARS);
        // The 20,000-char cap falls inside line 133; the visible text
        // runs to that line's end: 133 * 151 - 1 (newline excluded).
        assert_eq!(visible.chars().count(), 133 * 151 - 1);
        assert!(!visible.ends_with('\n'));
        assert_eq!(
            diff_hidden(&diff, MAX_DIFF_CHARS),
            (45_300 - (133 * 151 - 1), 300 - 133)
        );
    }

    #[test]
    fn diff_window_counts_unicode_characters_not_bytes() {
        // 'é' is two bytes: a byte-based cap would cut this in half.
        let line = format!("{}\n", "é".repeat(99));
        let diff = line.repeat(300);
        let visible = diff_visible(&diff, MAX_DIFF_CHARS);
        assert_eq!(visible.chars().count(), MAX_DIFF_CHARS);
        assert_eq!(diff_hidden(&diff, MAX_DIFF_CHARS), (10_000, 100));
    }
}
