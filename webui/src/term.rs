//! Small UTF-8 terminal screen for the side panel.
//!
//! xterm.js would be a second runtime and a script the page CSP does not
//! allow. This screen handles what an interactive bash actually emits:
//! cursor movement, erase, scroll, and a few SGR colors. It is not a full
//! VT100 (no alternate screen). That is enough for a shell prompt.

#[derive(Clone, Copy)]
struct Cell {
    ch: char,
    fg: u8,
    bold: bool,
}

impl Default for Cell {
    fn default() -> Self {
        Self {
            ch: ' ',
            fg: 0,
            bold: false,
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum State {
    Ground,
    Esc,
    Csi,
    Osc,
}

pub struct Screen {
    cols: usize,
    rows: usize,
    cells: Vec<Cell>,
    cx: usize,
    cy: usize,
    fg: u8,
    bold: bool,
    scrollback: Vec<String>,
    scroll_limit: usize,
    state: State,
    params: Vec<u16>,
    private: bool,
    utf8: Vec<u8>,
}

impl Screen {
    pub fn new(cols: usize, rows: usize) -> Self {
        let cols = cols.max(2);
        let rows = rows.max(2);
        Self {
            cols,
            rows,
            cells: vec![Cell::default(); cols * rows],
            cx: 0,
            cy: 0,
            fg: 0,
            bold: false,
            scrollback: Vec::new(),
            scroll_limit: 400,
            state: State::Ground,
            params: Vec::new(),
            private: false,
            utf8: Vec::new(),
        }
    }

    pub fn resize(&mut self, cols: usize, rows: usize) {
        let cols = cols.max(2);
        let rows = rows.max(2);
        if cols == self.cols && rows == self.rows {
            return;
        }
        let mut next = vec![Cell::default(); cols * rows];
        let copy_rows = self.rows.min(rows);
        let copy_cols = self.cols.min(cols);
        for y in 0..copy_rows {
            for x in 0..copy_cols {
                next[y * cols + x] = self.cells[y * self.cols + x];
            }
        }
        self.cells = next;
        self.cols = cols;
        self.rows = rows;
        if self.cx >= cols {
            self.cx = cols - 1;
        }
        if self.cy >= rows {
            self.cy = rows - 1;
        }
    }

    pub fn feed(&mut self, data: &[u8]) {
        for &b in data {
            self.byte(b);
        }
    }

    // Used by the screen tests. Release builds of the page do not call it.
    #[allow(dead_code)]
    pub fn text(&self) -> String {
        let mut out = String::new();
        for line in &self.scrollback {
            out.push_str(line);
            out.push('\n');
        }
        for y in 0..self.rows {
            let row = &self.cells[y * self.cols..(y + 1) * self.cols];
            let mut end = row.len();
            while end > 0 && row[end - 1].ch == ' ' && row[end - 1].fg == 0 && !row[end - 1].bold
            {
                end -= 1;
            }
            for cell in &row[..end] {
                out.push(cell.ch);
            }
            if y + 1 != self.rows {
                out.push('\n');
            }
        }
        out
    }

    /// HTML for the visible screen and scrollback. Text is escaped.
    pub fn render_html(&self) -> String {
        let mut html = String::new();
        for line in &self.scrollback {
            html.push_str("<div class=\"term-row\">");
            push_escaped(&mut html, line);
            html.push_str("</div>");
        }
        for y in 0..self.rows {
            html.push_str("<div class=\"term-row\">");
            let row = &self.cells[y * self.cols..(y + 1) * self.cols];
            let mut x = 0;
            while x < row.len() {
                let cell = row[x];
                let mut end = x + 1;
                while end < row.len()
                    && row[end].fg == cell.fg
                    && row[end].bold == cell.bold
                    && !(y == self.cy && end == self.cx)
                    && !(y == self.cy && x == self.cx)
                {
                    end += 1;
                }
                let cursor = y == self.cy && (x..end).contains(&self.cx);
                html.push_str("<span class=\"");
                html.push_str(span_class(cell.fg, cell.bold, cursor));
                html.push_str("\">");
                for c in &row[x..end] {
                    push_escaped_char(&mut html, c.ch);
                }
                html.push_str("</span>");
                x = end;
            }
            html.push_str("</div>");
        }
        html
    }

    fn byte(&mut self, b: u8) {
        match self.state {
            State::Ground => self.ground(b),
            State::Esc => self.esc(b),
            State::Csi => self.csi(b),
            State::Osc => self.osc(b),
        }
    }

    fn ground(&mut self, b: u8) {
        match b {
            0x1b => {
                self.utf8.clear();
                self.state = State::Esc;
            }
            b'\n' => {
                self.utf8.clear();
                self.linefeed();
            }
            b'\r' => {
                self.utf8.clear();
                self.cx = 0;
            }
            0x08 | 0x7f => {
                self.utf8.clear();
                self.cx = self.cx.saturating_sub(1);
            }
            b'\t' => {
                self.utf8.clear();
                self.cx = (self.cx / 8 + 1) * 8;
                if self.cx >= self.cols {
                    self.cx = self.cols - 1;
                }
            }
            0x00..=0x1f => {
                self.utf8.clear();
            }
            _ => self.utf8(b),
        }
    }

    fn utf8(&mut self, b: u8) {
        self.utf8.push(b);
        if let Ok(text) = std::str::from_utf8(&self.utf8) {
            let owned = text.to_string();
            self.utf8.clear();
            for ch in owned.chars() {
                self.put(ch);
            }
            return;
        }
        if self.utf8.len() >= 4 {
            self.utf8.clear();
            self.put('\u{fffd}');
        }
    }

    fn esc(&mut self, b: u8) {
        match b {
            b'[' => {
                self.params.clear();
                self.private = false;
                self.state = State::Csi;
            }
            b']' => self.state = State::Osc,
            b'D' => {
                self.linefeed();
                self.state = State::Ground;
            }
            _ => self.state = State::Ground,
        }
    }

    fn osc(&mut self, b: u8) {
        if b == 0x07 {
            self.state = State::Ground;
        } else if b == 0x1b {
            self.state = State::Esc;
        }
    }

    fn csi(&mut self, b: u8) {
        match b {
            b'0'..=b'9' => {
                let n = (b - b'0') as u16;
                if self.params.is_empty() {
                    self.params.push(n);
                } else if let Some(last) = self.params.last_mut() {
                    *last = last.saturating_mul(10).saturating_add(n);
                }
            }
            b';' => self.params.push(0),
            b'?' if self.params.is_empty() => self.private = true,
            0x40..=0x7e => {
                if !self.private {
                    self.dispatch(b);
                }
                self.state = State::Ground;
            }
            _ => {}
        }
    }

    fn param(&self, index: usize, default: usize) -> usize {
        self.params
            .get(index)
            .copied()
            .filter(|n| *n != 0)
            .map(|n| n as usize)
            .unwrap_or(default)
    }

    fn dispatch(&mut self, final_byte: u8) {
        match final_byte {
            b'A' => {
                self.cy = self.cy.saturating_sub(self.param(0, 1));
            }
            b'B' => {
                self.cy = (self.cy + self.param(0, 1)).min(self.rows - 1);
            }
            b'C' => {
                self.cx = (self.cx + self.param(0, 1)).min(self.cols - 1);
            }
            b'D' => {
                self.cx = self.cx.saturating_sub(self.param(0, 1));
            }
            b'G' => {
                let col = self.param(0, 1).saturating_sub(1);
                self.cx = col.min(self.cols - 1);
            }
            b'H' | b'f' => {
                let row = self.param(0, 1).saturating_sub(1);
                let col = self.param(1, 1).saturating_sub(1);
                self.cy = row.min(self.rows - 1);
                self.cx = col.min(self.cols - 1);
            }
            b'J' => self.erase_display(self.params.first().copied().unwrap_or(0)),
            b'K' => self.erase_line(self.params.first().copied().unwrap_or(0)),
            b'm' => self.sgr(),
            _ => {}
        }
    }

    fn erase_display(&mut self, mode: u16) {
        match mode {
            2 => {
                for cell in &mut self.cells {
                    *cell = Cell::default();
                }
            }
            1 => {
                let end = self.cy * self.cols + self.cx;
                let last = self.cells.len() - 1;
                for cell in &mut self.cells[..=end.min(last)] {
                    *cell = Cell::default();
                }
            }
            _ => {
                let start = self.cy * self.cols + self.cx;
                for cell in &mut self.cells[start..] {
                    *cell = Cell::default();
                }
            }
        }
    }

    fn erase_line(&mut self, mode: u16) {
        let start = self.cy * self.cols;
        let end = start + self.cols;
        let from = match mode {
            1 => start,
            2 => start,
            _ => start + self.cx,
        };
        let to = match mode {
            1 => start + self.cx + 1,
            _ => end,
        };
        for cell in &mut self.cells[from.min(end)..to.min(end)] {
            *cell = Cell::default();
        }
    }

    fn sgr(&mut self) {
        if self.params.is_empty() {
            self.fg = 0;
            self.bold = false;
            return;
        }
        for p in &self.params {
            match *p {
                0 => {
                    self.fg = 0;
                    self.bold = false;
                }
                1 => self.bold = true,
                22 => self.bold = false,
                30..=37 => self.fg = (*p - 30 + 1) as u8,
                39 => self.fg = 0,
                90..=97 => self.fg = (*p - 90 + 9) as u8,
                _ => {}
            }
        }
    }

    fn put(&mut self, ch: char) {
        if self.cx >= self.cols {
            self.cx = 0;
            self.linefeed();
        }
        let idx = self.cy * self.cols + self.cx;
        self.cells[idx] = Cell {
            ch,
            fg: self.fg,
            bold: self.bold,
        };
        self.cx += 1;
    }

    fn linefeed(&mut self) {
        if self.cy + 1 < self.rows {
            self.cy += 1;
            return;
        }
        let row = self.cells[..self.cols].to_vec();
        let mut line = String::new();
        for cell in &row {
            line.push(cell.ch);
        }
        let trimmed = line.trim_end().to_string();
        if !trimmed.is_empty() || !self.scrollback.is_empty() {
            self.scrollback.push(trimmed);
            if self.scrollback.len() > self.scroll_limit {
                let drop = self.scrollback.len() - self.scroll_limit;
                self.scrollback.drain(0..drop);
            }
        }
        self.cells.copy_within(self.cols.., 0);
        for cell in &mut self.cells[(self.rows - 1) * self.cols..] {
            *cell = Cell::default();
        }
    }
}

fn span_class(fg: u8, bold: bool, cursor: bool) -> &'static str {
    match (cursor, bold, fg) {
        (true, _, _) => "term-cursor",
        (false, true, 1) => "term-fg-1 term-bold",
        (false, true, 2) => "term-fg-2 term-bold",
        (false, true, 3) => "term-fg-3 term-bold",
        (false, true, 4) => "term-fg-4 term-bold",
        (false, true, 5) => "term-fg-5 term-bold",
        (false, true, 6) => "term-fg-6 term-bold",
        (false, true, 7) => "term-fg-7 term-bold",
        (false, false, 1) => "term-fg-1",
        (false, false, 2) => "term-fg-2",
        (false, false, 3) => "term-fg-3",
        (false, false, 4) => "term-fg-4",
        (false, false, 5) => "term-fg-5",
        (false, false, 6) => "term-fg-6",
        (false, false, 7) => "term-fg-7",
        (false, true, _) => "term-bold",
        _ => "term-fg",
    }
}

fn push_escaped(out: &mut String, text: &str) {
    for ch in text.chars() {
        push_escaped_char(out, ch);
    }
}

fn push_escaped_char(out: &mut String, ch: char) {
    match ch {
        '&' => out.push_str("&amp;"),
        '<' => out.push_str("&lt;"),
        '>' => out.push_str("&gt;"),
        _ => out.push(ch),
    }
}

/// Bytes to write to the PTY for one keydown. Browser shortcuts that use
/// Alt or Meta are left alone. Ctrl+letter becomes the corresponding
/// control byte so Ctrl+C reaches the shell.
pub fn key_bytes(key: &str, ctrl: bool, alt: bool, meta: bool) -> Option<String> {
    if alt || meta {
        return None;
    }
    if ctrl {
        let mut chars = key.chars();
        let c = chars.next()?;
        if chars.next().is_some() {
            return None;
        }
        let c = c.to_ascii_lowercase();
        if c.is_ascii_lowercase() {
            return Some((((c as u8) - b'a' + 1) as char).to_string());
        }
        return None;
    }
    match key {
        "Enter" => Some("\r".into()),
        "Backspace" => Some("\u{7f}".into()),
        "Tab" => Some("\t".into()),
        "ArrowUp" => Some("\u{1b}[A".into()),
        "ArrowDown" => Some("\u{1b}[B".into()),
        "ArrowRight" => Some("\u{1b}[C".into()),
        "ArrowLeft" => Some("\u{1b}[D".into()),
        "Escape" => Some("\u{1b}".into()),
        "Delete" => Some("\u{1b}[3~".into()),
        "Home" => Some("\u{1b}[H".into()),
        "End" => Some("\u{1b}[F".into()),
        _ => {
            let mut chars = key.chars();
            let c = chars.next()?;
            if chars.next().is_some() {
                return None;
            }
            Some(c.to_string())
        }
    }
}

#[cfg(test)]
mod tests {
    use super::Screen;

    #[test]
    fn prints_and_wraps_lines() {
        let mut s = Screen::new(8, 3);
        s.feed(b"hi\r\nthere");
        let text = s.text();
        assert!(text.contains("hi"), "{text}");
        assert!(text.contains("there"), "{text}");
    }

    #[test]
    fn backspace_and_erase_line() {
        let mut s = Screen::new(20, 2);
        s.feed(b"abcdef\x08\x08XX");
        assert!(s.text().contains("abcdXX"), "{}", s.text());
        s.feed(b"\r\x1b[K");
        assert!(!s.text().contains('a'), "{}", s.text());
    }

    #[test]
    fn cursor_address_and_color_do_not_eat_text() {
        let mut s = Screen::new(20, 4);
        s.feed(b"\x1b[2;3Hok\x1b[31m!\x1b[0m");
        let text = s.text();
        assert!(text.contains("ok!"), "{text}");
    }

    #[test]
    fn scrolls_when_the_screen_fills() {
        let mut s = Screen::new(6, 2);
        s.feed(b"one\r\ntwo\r\nthree");
        let text = s.text();
        assert!(text.contains("one"), "{text}");
        assert!(text.contains("three"), "{text}");
    }

    #[test]
    fn ignores_private_modes() {
        let mut s = Screen::new(40, 3);
        s.feed(b"\x1b[?2004hready");
        assert!(s.text().contains("ready"), "{}", s.text());
        assert!(!s.text().contains("2004"), "{}", s.text());
    }

    #[test]
    fn key_bytes_cover_shell_editing() {
        assert_eq!(super::key_bytes("Enter", false, false, false).as_deref(), Some("\r"));
        assert_eq!(super::key_bytes("c", true, false, false).as_deref(), Some("\u{3}"));
        assert_eq!(super::key_bytes("C", true, false, false).as_deref(), Some("\u{3}"));
        assert_eq!(
            super::key_bytes("ArrowUp", false, false, false).as_deref(),
            Some("\u{1b}[A")
        );
        assert_eq!(super::key_bytes("a", false, false, false).as_deref(), Some("a"));
        assert!(super::key_bytes("c", true, true, false).is_none());
        assert!(super::key_bytes("Shift", false, false, false).is_none());
    }

    #[test]
    fn escapes_html() {
        let mut s = Screen::new(20, 2);
        s.feed(b"<b>&");
        let html = s.render_html();
        assert!(html.contains("&lt;b&gt;&amp;"), "{html}");
        assert!(!html.contains("<b>"), "{html}");
    }
}
