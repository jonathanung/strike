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

/// Scrollback retained per terminal screen, in lines (#1520).
///
/// Was 400, which lost the first — usually causal — error of any long
/// build or test run. 5,000 lines of plain strings is in line with real
/// terminals (VS Code defaults to 1,000; xterm.js examples commonly use
/// 5,000–10,000) and costs one short `String` per line; `render_html`
/// walks the buffer once per frame, so the cost is linear in lines kept.
pub const SCROLLBACK_LIMIT: usize = 5_000;

/// Distance from the bottom, in CSS pixels, that still counts as "at the
/// bottom" for autoscroll purposes. Exact equality is too strict: wheel
/// and touch scrolling rarely land on the precise final pixel.
pub const AT_BOTTOM_THRESHOLD_PX: i32 = 32;

/// True when a scroll position is at (or within the threshold of) the
/// bottom of a scrollable element.
pub fn is_at_bottom(scroll_top: i32, scroll_height: i32, client_height: i32) -> bool {
    scroll_height - client_height - scroll_top <= AT_BOTTOM_THRESHOLD_PX
}

/// Whether the terminal view should follow new output (#1520).
///
/// Starts following. A scroll event away from the bottom pins the view:
/// `paint_target` then returns the saved reading position instead of the
/// tail, so repaint frames cannot yank the user back down. Scrolling back
/// within [`AT_BOTTOM_THRESHOLD_PX`] of the bottom — or an explicit
/// `jump_to_bottom` — resumes following. The state is plain panel state:
/// nothing in attach/replay resets it, so a re-attach mid-investigation
/// keeps the reading position instead of silently snapping to the tail.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ScrollFollow {
    following: bool,
    saved_top: i32,
}

impl Default for ScrollFollow {
    fn default() -> Self {
        Self::new()
    }
}

impl ScrollFollow {
    pub fn new() -> Self {
        Self {
            following: true,
            saved_top: 0,
        }
    }

    pub fn following(&self) -> bool {
        self.following
    }

    /// Record a user scroll: following iff the new position is at the
    /// bottom; otherwise remember the position as the reading position.
    pub fn on_scroll(&mut self, scroll_top: i32, scroll_height: i32, client_height: i32) {
        if is_at_bottom(scroll_top, scroll_height, client_height) {
            self.following = true;
        } else {
            self.following = false;
            self.saved_top = scroll_top.max(0);
        }
    }

    /// The `scrollTop` a paint should apply, if any: the tail while
    /// following, otherwise the saved reading position (restoring it
    /// undoes any reset the DOM update itself caused).
    pub fn paint_target(&self, scroll_height: i32) -> i32 {
        if self.following {
            scroll_height
        } else {
            self.saved_top
        }
    }

    /// Jump-to-bottom control: resume following; the next paint lands on
    /// the tail.
    pub fn jump_to_bottom(&mut self) {
        self.following = true;
    }
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
            scroll_limit: SCROLLBACK_LIMIT,
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
    pub fn scrollback_len(&self) -> usize {
        self.scrollback.len()
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
    fn scrollback_keeps_far_more_than_the_old_400_lines() {
        // Regression for #1520: a `seq 1 500`-scale burst used to evict
        // its own first lines at the old 400-line cap.
        let mut s = Screen::new(20, 2);
        let mut data = String::new();
        for i in 1..=1_000 {
            data.push_str(&format!("line{i}\r\n"));
        }
        s.feed(data.as_bytes());
        assert_eq!(s.scrollback_len(), 999);
        let text = s.text();
        assert!(text.contains("line1\n"), "first line kept: {text:.80}");
        assert!(text.contains("line999"), "{text:.80}");
    }

    #[test]
    fn scrollback_is_capped_at_the_named_limit() {
        let mut s = Screen::new(20, 2);
        let mut data = String::new();
        for i in 1..=(super::SCROLLBACK_LIMIT + 100) {
            data.push_str(&format!("line{i}\r\n"));
        }
        s.feed(data.as_bytes());
        assert_eq!(s.scrollback_len(), super::SCROLLBACK_LIMIT);
        let text = s.text();
        assert!(!text.contains("line50\n"), "oldest lines evicted");
        assert!(text.contains(&format!("line{}", super::SCROLLBACK_LIMIT + 99)));
    }

    #[test]
    fn at_bottom_threshold_is_forgiving_but_not_generous() {
        // scroll_height 1000, viewport 200: bottom scrollTop is 800.
        assert!(super::is_at_bottom(800, 1000, 200));
        assert!(super::is_at_bottom(800 - super::AT_BOTTOM_THRESHOLD_PX, 1000, 200));
        assert!(!super::is_at_bottom(800 - super::AT_BOTTOM_THRESHOLD_PX - 1, 1000, 200));
        assert!(!super::is_at_bottom(0, 1000, 200));
        // Content shorter than the viewport is always at the bottom.
        assert!(super::is_at_bottom(0, 100, 200));
    }

    #[test]
    fn scroll_follow_pins_on_scroll_up_and_resumes_at_bottom_or_on_jump() {
        let mut follow = super::ScrollFollow::new();
        assert!(follow.following());
        // Paint while following targets the tail.
        assert_eq!(follow.paint_target(1000), 1000);
        // User scrolls up to read: paint must restore the reading
        // position, never the tail (#1520 defect 1).
        follow.on_scroll(120, 1000, 200);
        assert!(!follow.following());
        assert_eq!(follow.paint_target(1400), 120);
        // More output arrives while pinned: still the reading position.
        follow.on_scroll(120, 1400, 200);
        assert_eq!(follow.paint_target(1800), 120);
        // Scrolling back near the bottom resumes following.
        follow.on_scroll(1600 - super::AT_BOTTOM_THRESHOLD_PX, 1800, 200);
        assert!(follow.following());
        assert_eq!(follow.paint_target(1800), 1800);
        // Pin again, then the jump-to-bottom control resumes following.
        follow.on_scroll(40, 1800, 200);
        assert!(!follow.following());
        follow.jump_to_bottom();
        assert!(follow.following());
        assert_eq!(follow.paint_target(1800), 1800);
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
