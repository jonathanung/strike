//! Markdown Review (#1366): anchored comments on a rendered document.
//!
//! An anchor is the passage's quoted text plus the nearest heading and an
//! offset hint, never a line number: line numbers rot on the first edit,
//! while a quote either still appears in the document or it does not.
//! When it does not, the comment is [`Status::Stale`] — it is never moved
//! onto some other passage.
//!
//! Everything here is pure so it can be tested off-wasm. The web client
//! feeds it the rendered document's visible text (one entry per block)
//! and the browser selection's text.

/// One rendered block's visible text. `heading` blocks open a section.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Block {
    pub heading: bool,
    pub text: String,
}

impl Block {
    pub fn heading(text: &str) -> Self {
        Block { heading: true, text: text.to_string() }
    }
    pub fn text(text: &str) -> Self {
        Block { heading: false, text: text.to_string() }
    }
}

/// Where a comment points: the quoted passage (whitespace-normalised),
/// the nearest heading above it ("" before the first heading), and the
/// passage's character offset in the normalised document when it was made.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Anchor {
    pub quote: String,
    pub heading: String,
    pub offset: usize,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Status {
    /// The quoted passage is still in the document.
    Attached,
    /// The quoted passage is gone: the comment says so instead of moving.
    Stale,
}

/// One unsent comment.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Comment {
    pub id: String,
    pub anchor: Anchor,
    pub body: String,
}

/// Collapse every whitespace run to one space and trim. Browser selections
/// put newlines between blocks; the rendered text puts none. Normalising
/// both sides makes a quote match regardless of how it was selected.
pub fn normalize(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut space = false;
    for c in s.chars() {
        if c.is_whitespace() {
            space = !out.is_empty();
        } else {
            if space {
                out.push(' ');
                space = false;
            }
            out.push(c);
        }
    }
    out
}

/// The rendered document as one normalised string, with heading starts.
/// Offsets are in characters, not bytes.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct PlainDoc {
    chars: Vec<char>,
    headings: Vec<(usize, String)>,
}

impl PlainDoc {
    pub fn new(blocks: &[Block]) -> Self {
        let mut chars: Vec<char> = Vec::new();
        let mut headings = Vec::new();
        for block in blocks {
            let text = normalize(&block.text);
            if text.is_empty() {
                continue;
            }
            if !chars.is_empty() {
                chars.push(' ');
            }
            if block.heading {
                headings.push((chars.len(), text.clone()));
            }
            chars.extend(text.chars());
        }
        PlainDoc { chars, headings }
    }

    /// Character offsets of every occurrence of `needle` (already
    /// normalised), overlapping occurrences included.
    fn occurrences(&self, needle: &[char]) -> Vec<usize> {
        if needle.is_empty() || needle.len() > self.chars.len() {
            return Vec::new();
        }
        (0..=self.chars.len() - needle.len())
            .filter(|&i| self.chars[i..i + needle.len()] == *needle)
            .collect()
    }

    /// The occurrence of `quote` nearest the offset hint, if any.
    pub fn find(&self, quote: &str, hint: usize) -> Option<usize> {
        let needle: Vec<char> = normalize(quote).chars().collect();
        self.occurrences(&needle)
            .into_iter()
            .min_by_key(|&i| i.abs_diff(hint))
    }

    /// The last heading starting at or before `pos`.
    pub fn heading_at(&self, pos: usize) -> String {
        self.headings
            .iter()
            .take_while(|(start, _)| *start <= pos)
            .last()
            .map(|(_, text)| text.clone())
            .unwrap_or_default()
    }

    /// Anchor a browser selection. `hint` is the expected offset (0 when
    /// unknown); with several equal passages it picks the nearest one.
    /// `None` when the selection is empty or is not text of this document
    /// (it spanned the outline, say).
    pub fn anchor(&self, selection: &str, hint: usize) -> Option<Anchor> {
        let quote = normalize(selection);
        if quote.is_empty() {
            return None;
        }
        let offset = self.find(&quote, hint)?;
        Some(Anchor { heading: self.heading_at(offset), quote, offset })
    }

    /// Whether the anchor's passage is still in the document.
    pub fn status(&self, anchor: &Anchor) -> Status {
        match self.find(&anchor.quote, anchor.offset) {
            Some(_) => Status::Attached,
            None => Status::Stale,
        }
    }
}

/// Longest quote kept in a review turn; the rest is elided.
pub const QUOTE_MAX_CHARS: usize = 600;

/// The header line every Markdown review turn opens with.
pub const TURN_HEADER: &str = "Markdown review of";

fn clip(s: &str, max: usize) -> String {
    let mut out: String = s.chars().take(max).collect();
    if s.chars().count() > max {
        out.push('…');
    }
    out
}

/// Every unsent comment for one file as ONE user turn: the file path, then
/// per comment its section, the quoted passage, and the comment. A stale
/// comment says its passage is no longer in the file.
pub fn review_turn(path: &str, comments: &[(Comment, Status)]) -> String {
    let n = comments.len();
    let mut out = format!(
        "{TURN_HEADER} `{path}` ({n} comment{}). Address each comment in the file, then summarize what you changed.\n",
        if n == 1 { "" } else { "s" }
    );
    for (i, (c, status)) in comments.iter().enumerate() {
        out.push('\n');
        let section = if c.anchor.heading.is_empty() {
            "before the first heading".to_string()
        } else {
            format!("under \"{}\"", c.anchor.heading)
        };
        out.push_str(&format!("{}. {section}", i + 1));
        if *status == Status::Stale {
            out.push_str(" (this passage is no longer in the file; it was edited after the comment)");
        }
        out.push_str(":\n");
        for line in clip(&c.anchor.quote, QUOTE_MAX_CHARS).lines() {
            out.push_str("   > ");
            out.push_str(line);
            out.push('\n');
        }
        let mut first = true;
        for line in c.body.trim().lines() {
            out.push_str(if first { "   Comment: " } else { "            " });
            out.push_str(line);
            out.push('\n');
            first = false;
        }
    }
    out.trim_end().to_string()
}

/// What remains after a send: nothing on success, everything on failure.
/// Comments only leave the queue once the turn was accepted.
pub fn after_send(comments: Vec<Comment>, sent: bool) -> Vec<Comment> {
    if sent {
        Vec::new()
    } else {
        comments
    }
}

/// Browser-storage key for one session's unsent comments on one file.
pub fn storage_key(session: &str, path: &str) -> String {
    let session = if session.is_empty() { "_" } else { session };
    format!("strike.mdReview.v1:{session}:{path}")
}

/// Why sending is refused, or `None` when it may go. Sending mid-turn is
/// refused so the agent never acts on half a review.
pub fn send_block(count: usize, busy: bool, has_session: bool) -> Option<&'static str> {
    if count == 0 {
        Some("No comments to send.")
    } else if !has_session {
        Some("Open a live session to send comments.")
    } else if busy {
        Some("A turn is running. Send the review once the session is idle.")
    } else {
        None
    }
}

/// Comments in display order: grouped by heading in document order of
/// their anchors (stale comments keep their recorded offset).
pub fn grouped(doc: &PlainDoc, comments: &[Comment]) -> Vec<(String, Vec<(Comment, Status)>)> {
    let mut rows: Vec<(usize, Comment, Status)> = comments
        .iter()
        .map(|c| {
            let status = doc.status(&c.anchor);
            let pos = doc.find(&c.anchor.quote, c.anchor.offset).unwrap_or(c.anchor.offset);
            (pos, c.clone(), status)
        })
        .collect();
    rows.sort_by_key(|(pos, _, _)| *pos);
    let mut out: Vec<(String, Vec<(Comment, Status)>)> = Vec::new();
    for (_, c, status) in rows {
        let heading = c.anchor.heading.clone();
        match out.last_mut() {
            Some((h, list)) if *h == heading => list.push((c, status)),
            _ => out.push((heading, vec![(c, status)])),
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn spec() -> Vec<Block> {
        vec![
            Block::heading("Requirements"),
            Block::text("The runner must retry three times."),
            Block::text("Logs go to stderr."),
            Block::heading("Design"),
            Block::text("A queue holds pending work.\nWorkers pull from it."),
        ]
    }

    fn comment(doc: &PlainDoc, quote: &str, body: &str) -> Comment {
        Comment { id: quote.into(), anchor: doc.anchor(quote, 0).expect("anchors"), body: body.into() }
    }

    #[test]
    fn normalize_collapses_whitespace() {
        let cases = [("  a \n\n b\tc  ", "a b c"), ("", ""), ("\n", ""), ("one", "one")];
        for (input, want) in cases {
            assert_eq!(normalize(input), want, "{input:?}");
        }
    }

    #[test]
    fn anchor_records_quote_heading_and_offset() {
        let doc = PlainDoc::new(&spec());
        let a = doc.anchor("retry three\n times", 0).unwrap();
        assert_eq!(a.quote, "retry three times");
        assert_eq!(a.heading, "Requirements");
        let b = doc.anchor("Workers pull", 0).unwrap();
        assert_eq!(b.heading, "Design");
        assert!(b.offset > a.offset);
        assert_eq!(doc.anchor("   ", 0), None);
        assert_eq!(doc.anchor("not in the doc", 0), None);
    }

    #[test]
    fn selection_across_blocks_anchors() {
        let doc = PlainDoc::new(&spec());
        let a = doc.anchor("stderr.\n\nDesign\nA queue", 0).unwrap();
        assert_eq!(a.heading, "Requirements");
    }

    #[test]
    fn before_first_heading_has_empty_heading() {
        let doc = PlainDoc::new(&[Block::text("Intro text."), Block::heading("H"), Block::text("Body.")]);
        assert_eq!(doc.anchor("Intro", 0).unwrap().heading, "");
    }

    #[test]
    fn anchor_survives_an_unrelated_edit_elsewhere() {
        let doc = PlainDoc::new(&spec());
        let c = comment(&doc, "Workers pull from it", "which workers?");
        let mut edited = spec();
        edited[1] = Block::text("The runner must retry five times, with backoff, before failing the job.");
        edited.insert(0, Block::text("A brand new intro paragraph."));
        let doc2 = PlainDoc::new(&edited);
        assert_eq!(doc2.status(&c.anchor), Status::Attached);
    }

    #[test]
    fn anchor_is_stale_not_moved_when_its_passage_is_edited() {
        let doc = PlainDoc::new(&spec());
        let c = comment(&doc, "retry three times", "why three?");
        let mut edited = spec();
        edited[1] = Block::text("The runner must retry five times.");
        let doc2 = PlainDoc::new(&edited);
        assert_eq!(doc2.status(&c.anchor), Status::Stale);
        // Grouping keeps it under its recorded heading, flagged stale.
        let groups = grouped(&doc2, std::slice::from_ref(&c));
        assert_eq!(groups[0].0, "Requirements");
        assert_eq!(groups[0].1[0].1, Status::Stale);
    }

    #[test]
    fn duplicate_passages_pick_the_nearest_to_the_hint() {
        let doc = PlainDoc::new(&[Block::text("same words"), Block::heading("Two"), Block::text("same words")]);
        let first = doc.anchor("same words", 0).unwrap();
        let second = doc.anchor("same words", 1000).unwrap();
        assert_eq!(first.heading, "");
        assert_eq!(second.heading, "Two");
        assert_ne!(first.offset, second.offset);
    }

    #[test]
    fn review_turn_holds_every_comment_in_one_message() {
        let doc = PlainDoc::new(&spec());
        let a = comment(&doc, "retry three times", "why three?");
        let b = comment(&doc, "A queue holds", "which queue?\nRedis or in-memory?");
        let turn = review_turn("specs/x/design.md", &[(a, Status::Attached), (b, Status::Stale)]);
        assert!(turn.starts_with("Markdown review of `specs/x/design.md` (2 comments)."));
        assert_eq!(turn.matches(TURN_HEADER).count(), 1, "exactly one turn header");
        for want in [
            "1. under \"Requirements\":\n   > retry three times\n   Comment: why three?",
            "2. under \"Design\" (this passage is no longer in the file",
            "   > A queue holds\n   Comment: which queue?\n            Redis or in-memory?",
        ] {
            assert!(turn.contains(want), "missing {want:?} in:\n{turn}");
        }
        let one = review_turn("a.md", &[(comment(&doc, "Logs", "x"), Status::Attached)]);
        assert!(one.contains("(1 comment)."));
    }

    #[test]
    fn long_quotes_are_clipped() {
        let long = "word ".repeat(400);
        let doc = PlainDoc::new(&[Block::text(&long)]);
        let c = comment(&doc, &long, "too long");
        let turn = review_turn("a.md", &[(c, Status::Attached)]);
        assert!(turn.contains('…'));
        assert!(turn.chars().count() < long.len());
    }

    #[test]
    fn failed_send_retains_comments_and_success_clears_them() {
        let doc = PlainDoc::new(&spec());
        let list = vec![comment(&doc, "Logs", "a"), comment(&doc, "queue", "b")];
        assert_eq!(after_send(list.clone(), false), list);
        assert!(after_send(list, true).is_empty());
    }

    #[test]
    fn send_block_refuses_mid_turn_and_empty() {
        assert_eq!(send_block(0, false, true), Some("No comments to send."));
        assert!(send_block(2, true, true).unwrap().contains("idle"));
        assert!(send_block(2, false, false).unwrap().contains("live session"));
        assert_eq!(send_block(2, false, true), None);
    }

    #[test]
    fn storage_key_is_per_session_and_file() {
        assert_eq!(storage_key("s1", "docs/a.md"), "strike.mdReview.v1:s1:docs/a.md");
        assert_eq!(storage_key("", "a.md"), "strike.mdReview.v1:_:a.md");
        assert_ne!(storage_key("s1", "a.md"), storage_key("s2", "a.md"));
    }

    #[test]
    fn grouped_orders_by_document_position() {
        let doc = PlainDoc::new(&spec());
        let late = comment(&doc, "Workers", "late");
        let early = comment(&doc, "runner", "early");
        let mid = comment(&doc, "stderr", "mid");
        let groups = grouped(&doc, &[late, early, mid]);
        assert_eq!(groups.len(), 2);
        assert_eq!(groups[0].0, "Requirements");
        assert_eq!(groups[0].1.iter().map(|(c, _)| c.body.as_str()).collect::<Vec<_>>(), ["early", "mid"]);
        assert_eq!(groups[1].1[0].0.body, "late");
    }
}
