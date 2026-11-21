//! Live tool output tail (#1374).
//!
//! The engine streams `tool.output` chunks (`ToolCallOutput{callId, data}`)
//! while a call runs; the protocol says frontends append `data` in order
//! for a live tail, and `tool.end` still carries the final model-facing
//! output. The TUI does exactly that (`apply_event.go`); this is the
//! cockpit's copy.
//!
//! The tail is display-only and bounded so a chatty command cannot grow
//! a long session without limit: it keeps the last [`TAIL_MAX_LINES`]
//! lines or [`TAIL_MAX_BYTES`] bytes, whichever is smaller, and records
//! that earlier output was dropped so the card can say so. Nothing here
//! changes what the model receives — `tool.end` replaces the tail with
//! the final output exactly as before.

/// Most lines the tail keeps.
pub const TAIL_MAX_LINES: usize = 200;
/// Most bytes the tail keeps (32 KiB).
pub const TAIL_MAX_BYTES: usize = 32 * 1024;
/// Shown above a tail that has dropped earlier output.
pub const TRUNCATED_HINT: &str = "Earlier output truncated in this view";

/// Retained tail of one running tool call.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ToolTail {
    pub text: String,
    /// Earlier output was dropped to stay within the bounds.
    pub truncated: bool,
}

impl ToolTail {
    /// Append one `tool.output` chunk in arrival order, then re-apply the
    /// bounds. CRLF is folded to LF so Windows-style output does not
    /// double-space in the `<pre>`.
    pub fn push(&mut self, data: &str) {
        if data.is_empty() {
            return;
        }
        if data.contains("\r\n") {
            self.text.push_str(&data.replace("\r\n", "\n"));
        } else {
            self.text.push_str(data);
        }
        self.bound();
    }

    fn bound(&mut self) {
        if self.text.len() > TAIL_MAX_BYTES {
            let mut cut = self.text.len() - TAIL_MAX_BYTES;
            while !self.text.is_char_boundary(cut) {
                cut += 1;
            }
            self.text.drain(..cut);
            // Drop the partial first line the byte cut left behind, unless
            // it is the only line (one huge line keeps its tail).
            if let Some(nl) = self.text.find('\n') {
                if nl + 1 < self.text.len() {
                    self.text.drain(..=nl);
                }
            }
            self.truncated = true;
        }
        let lines = line_count(&self.text);
        if lines > TAIL_MAX_LINES {
            // The first `drop` lines all end in '\n' (only the last line
            // may be unterminated), so cut just after the drop-th newline.
            let drop = lines - TAIL_MAX_LINES;
            if let Some((i, _)) = self.text.match_indices('\n').nth(drop - 1) {
                self.text.drain(..=i);
            }
            self.truncated = true;
        }
    }
}

/// Lines in `s`; an unterminated last line counts.
pub fn line_count(s: &str) -> usize {
    if s.is_empty() {
        return 0;
    }
    let n = s.matches('\n').count();
    if s.ends_with('\n') {
        n
    } else {
        n + 1
    }
}

/// The muted tail block inside a tool card. The scroller is
/// `column-reverse` (see `tool-tail.css`), so it stays pinned to the
/// newest line as chunks arrive without any scroll code.
pub fn tail_view(tail: ToolTail) -> impl leptos::prelude::IntoView {
    use leptos::prelude::*;
    let lines = line_count(&tail.text);
    view! {
        <div class="tool-tail" data-tool-tail=lines.to_string()>
            {tail.truncated.then(|| view! {
                <p class="tool-tail-note" data-tool-tail-truncated="true">{TRUNCATED_HINT}</p>
            })}
            <div class="tool-tail-scroll" tabindex="0" aria-label="Live tool output">
                <pre>{tail.text}</pre>
            </div>
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn appends_chunks_in_order() {
        let mut t = ToolTail::default();
        t.push("one\n");
        t.push("tw");
        t.push("o\nthree");
        assert_eq!(t.text, "one\ntwo\nthree");
        assert!(!t.truncated);
    }

    #[test]
    fn empty_chunk_is_ignored() {
        let mut t = ToolTail::default();
        t.push("");
        assert_eq!(t, ToolTail::default());
    }

    #[test]
    fn crlf_is_folded() {
        let mut t = ToolTail::default();
        t.push("a\r\nb\r\n");
        assert_eq!(t.text, "a\nb\n");
    }

    #[test]
    fn line_count_cases() {
        let cases = [("", 0), ("a", 1), ("a\n", 1), ("a\nb", 2), ("a\nb\n", 2), ("\n\n", 2)];
        for (s, want) in cases {
            assert_eq!(line_count(s), want, "{s:?}");
        }
    }

    #[test]
    fn keeps_last_lines_within_line_cap() {
        let mut t = ToolTail::default();
        for i in 0..(TAIL_MAX_LINES + 50) {
            t.push(&format!("line {i}\n"));
        }
        assert!(t.truncated);
        assert_eq!(line_count(&t.text), TAIL_MAX_LINES);
        assert!(t.text.starts_with("line 50\n"), "{}", &t.text[..20]);
        assert!(t.text.ends_with(&format!("line {}\n", TAIL_MAX_LINES + 49)));
    }

    #[test]
    fn exactly_at_line_cap_is_not_truncated() {
        let mut t = ToolTail::default();
        for i in 0..TAIL_MAX_LINES {
            t.push(&format!("{i}\n"));
        }
        assert!(!t.truncated);
        assert_eq!(line_count(&t.text), TAIL_MAX_LINES);
    }

    #[test]
    fn unterminated_last_line_counts_toward_cap() {
        let mut t = ToolTail::default();
        let mut s = String::new();
        for i in 0..TAIL_MAX_LINES {
            s.push_str(&format!("{i}\n"));
        }
        s.push_str("partial");
        t.push(&s);
        assert!(t.truncated);
        assert_eq!(line_count(&t.text), TAIL_MAX_LINES);
        assert!(t.text.starts_with("1\n"));
        assert!(t.text.ends_with("partial"));
    }

    #[test]
    fn byte_cap_drops_partial_first_line() {
        let mut t = ToolTail::default();
        // 100 lines of ~1 KiB: well under the line cap, over the byte cap.
        let row = "x".repeat(1023);
        for i in 0..100 {
            t.push(&format!("{i:03}{row}\n"));
        }
        assert!(t.truncated);
        assert!(t.text.len() <= TAIL_MAX_BYTES);
        // Every kept line is whole: starts with its 3-digit index.
        for line in t.text.lines() {
            assert_eq!(line.len(), 3 + row.len(), "partial line kept");
        }
        assert!(t.text.ends_with(&format!("099{row}\n")));
    }

    #[test]
    fn single_huge_line_keeps_its_tail() {
        let mut t = ToolTail::default();
        t.push(&"y".repeat(TAIL_MAX_BYTES + 10));
        assert!(t.truncated);
        assert_eq!(t.text.len(), TAIL_MAX_BYTES);
    }

    #[test]
    fn byte_cut_respects_char_boundaries() {
        let mut t = ToolTail::default();
        // 3-byte chars, no newline: the cut point must land on a boundary.
        t.push(&"€".repeat(TAIL_MAX_BYTES / 3 + 7));
        assert!(t.truncated);
        assert!(t.text.len() <= TAIL_MAX_BYTES);
        assert!(t.text.chars().all(|c| c == '€'));
    }
}
