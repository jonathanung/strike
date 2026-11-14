//! Window math for the cockpit transcript (#1515).
//!
//! The transcript pane mounts only a tail slice of the message list,
//! `messages[start..]`, instead of every message in the session. A
//! reader following the live tail keeps at most [`WINDOW`] + [`SLACK`]
//! rows in the DOM no matter how long the session runs. Scrolling to
//! the top of the slice (or pressing "Show earlier") adds [`PAGE`] rows
//! above, and jumping to a row above the slice (find, keyboard cursor,
//! palette jump) widens it so the row exists before the scroll.
//!
//! Row indices stay absolute: the rendered row for `messages[i]` keeps
//! index `i`, so find hits and `tf-line-<i>` ids line up with the full
//! list. This crate has no web dependencies so the math runs as host
//! unit tests in CI.

/// Rows mounted when the window snaps to the tail.
pub const WINDOW: usize = 120;
/// Extra rows a following reader can accumulate before the window
/// slides forward. Sliding in steps (not on every new message) keeps
/// the top rows from being unmounted on every append.
pub const SLACK: usize = 60;
/// Rows added above the window per "earlier" step.
pub const PAGE: usize = 80;
/// Rows kept above a revealed target so it does not land flush on the
/// window's first row (which would immediately trigger another page).
pub const MARGIN: usize = 8;
/// Distance from the top of the scroller, in px, at which scrolling up
/// mounts the next page of earlier rows.
pub const TOP_PX: f64 = 240.0;

/// Start sentinel meaning "snap to the tail on the next update". Used
/// for the initial mount and on session switch.
pub const TAIL: usize = usize::MAX;

/// First mounted index when snapping to the tail of `len` rows.
pub fn tail_start(len: usize) -> usize {
    len.saturating_sub(WINDOW)
}

/// Window start after the list or the follow state changes.
///
/// - Never past the tail window: a shrunken list (fork, truncate,
///   session switch) still shows up to [`WINDOW`] rows, and the
///   [`TAIL`] sentinel resolves here.
/// - A following (`pinned`) reader slides forward once the window holds
///   more than [`WINDOW`] + [`SLACK`] rows, so the DOM stays bounded.
/// - A reader scrolled up keeps their start: nothing above them is
///   unmounted while they read.
pub fn follow(start: usize, len: usize, pinned: bool) -> usize {
    let tail = tail_start(len);
    let start = start.min(tail);
    if pinned && len - start > WINDOW + SLACK {
        tail
    } else {
        start
    }
}

/// Window start after mounting one page of earlier rows.
pub fn earlier(start: usize) -> usize {
    start.saturating_sub(PAGE)
}

/// Window start that mounts row `idx`, keeping [`MARGIN`] rows above
/// it. A row already inside the window leaves the start alone.
pub fn reveal(start: usize, idx: usize) -> usize {
    if idx < start {
        idx.saturating_sub(MARGIN)
    } else {
        start
    }
}

/// True when a scroll position is close enough to the top of the
/// mounted slice to mount earlier rows, and there are any.
pub fn wants_earlier(start: usize, scroll_top: f64) -> bool {
    start > 0 && start != TAIL && scroll_top < TOP_PX
}

/// Scroll offset that keeps an anchor row at the same place on screen
/// after rows were mounted above it. `anchor_before` / `anchor_after`
/// are the row's viewport y before and after the mount, and
/// `scroll_top` is the offset after the mount. Engines with native
/// scroll anchoring (`overflow-anchor`) already held the row in place
/// and report no drift, so this is a no-op there instead of a double
/// jump; engines without it get the drift corrected.
pub fn anchor_scroll_top(scroll_top: f64, anchor_before: f64, anchor_after: f64) -> f64 {
    (scroll_top + (anchor_after - anchor_before)).max(0.0)
}

/// Mounted index range for `len` rows starting at `start`. Never fewer
/// than the tail window, so a stale start (or [`TAIL`]) still renders
/// the latest rows before [`follow`] has stored a resolved start.
pub fn mounted(start: usize, len: usize) -> std::ops::Range<usize> {
    start.min(tail_start(len))..len
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tail_start_bounds() {
        assert_eq!(tail_start(0), 0);
        assert_eq!(tail_start(WINDOW), 0);
        assert_eq!(tail_start(WINDOW + 1), 1);
        assert_eq!(tail_start(500), 500 - WINDOW);
    }

    #[test]
    fn sentinel_snaps_to_tail() {
        assert_eq!(follow(TAIL, 0, true), 0);
        assert_eq!(follow(TAIL, 10, false), 0);
        assert_eq!(follow(TAIL, 500, false), 500 - WINDOW);
        assert_eq!(follow(TAIL, 500, true), 500 - WINDOW);
    }

    #[test]
    fn short_sessions_mount_everything() {
        let mut start = TAIL;
        for len in 0..=WINDOW {
            start = follow(start, len, true);
            assert_eq!(mounted(start, len), 0..len);
        }
    }

    #[test]
    fn following_reader_dom_stays_bounded() {
        // Stream 5,000 messages while pinned: the mounted count never
        // exceeds WINDOW + SLACK, and never drops below WINDOW once the
        // session is that long.
        let mut start = TAIL;
        let mut slides = 0;
        for len in 0..5_000 {
            let next = follow(start, len, true);
            if next != start && start != TAIL && len > WINDOW {
                slides += 1;
            }
            start = next;
            let n = mounted(start, len).len();
            assert!(n <= WINDOW + SLACK, "len {len}: {n} rows mounted");
            assert!(n >= len.min(WINDOW), "len {len}: only {n} rows mounted");
        }
        // Slides happen in SLACK-sized steps, not on every append.
        assert!(slides <= 5_000 / SLACK + 1, "{slides} slides");
    }

    #[test]
    fn appends_between_slides_keep_start() {
        let len = 500;
        let start = follow(TAIL, len, true);
        for extra in 1..=SLACK {
            assert_eq!(follow(start, len + extra, true), start);
        }
        assert_eq!(follow(start, len + SLACK + 1, true), tail_start(len + SLACK + 1));
    }

    #[test]
    fn reader_scrolled_up_keeps_start() {
        // Reading history mid-stream: new messages never unmount the
        // rows above the reader.
        let start = 40;
        for len in 300..1_000 {
            assert_eq!(follow(start, len, false), start);
        }
    }

    #[test]
    fn repinning_trims_back_to_tail() {
        assert_eq!(follow(0, 1_000, true), tail_start(1_000));
        // Within the slack, re-pinning keeps the rows already mounted.
        let start = 1_000 - WINDOW - SLACK;
        assert_eq!(follow(start, 1_000, true), start);
    }

    #[test]
    fn shrinking_list_clamps_start() {
        // Fork/truncate down to 50 rows from a window starting at 400.
        assert_eq!(follow(400, 50, false), 0);
        assert_eq!(follow(400, 50, true), 0);
        assert_eq!(follow(400, 300, false), 300 - WINDOW);
        assert_eq!(follow(400, 0, true), 0);
    }

    #[test]
    fn earlier_pages_down_to_zero() {
        assert_eq!(earlier(500), 500 - PAGE);
        assert_eq!(earlier(PAGE), 0);
        assert_eq!(earlier(PAGE - 1), 0);
        assert_eq!(earlier(0), 0);
    }

    #[test]
    fn reveal_mounts_target_with_margin() {
        assert_eq!(reveal(300, 100), 100 - MARGIN);
        assert_eq!(reveal(300, 3), 0);
        assert_eq!(reveal(300, 0), 0);
        // Already mounted: unchanged.
        assert_eq!(reveal(300, 300), 300);
        assert_eq!(reveal(300, 450), 300);
        // A revealed row stays mounted while the reader is scrolled up.
        let start = reveal(300, 100);
        assert!(mounted(follow(start, 600, false), 600).contains(&100));
    }

    #[test]
    fn wants_earlier_only_near_top_with_rows_above() {
        assert!(wants_earlier(10, 0.0));
        assert!(wants_earlier(10, TOP_PX - 1.0));
        assert!(!wants_earlier(10, TOP_PX));
        assert!(!wants_earlier(0, 0.0));
        assert!(!wants_earlier(TAIL, 0.0));
    }

    #[test]
    fn anchor_corrects_drift_only() {
        // No native anchoring: 80 rows (6,000px) landed above a row
        // that sat at y=100, pushing it to y=6,100. Offset follows it.
        assert_eq!(anchor_scroll_top(100.0, 100.0, 6_100.0), 6_100.0);
        // Native anchoring already moved the offset: no drift, no-op.
        assert_eq!(anchor_scroll_top(6_100.0, 100.0, 100.0), 6_100.0);
        // Partial drift is corrected exactly.
        assert_eq!(anchor_scroll_top(500.0, 40.0, 70.0), 530.0);
        // Never negative.
        assert_eq!(anchor_scroll_top(10.0, 500.0, 0.0), 0.0);
    }

    #[test]
    fn mounted_range_is_absolute_and_clamped() {
        assert_eq!(mounted(380, 500), 380..500);
        assert_eq!(mounted(10, 500), 10..500);
        assert_eq!(mounted(600, 500), 380..500);
        assert_eq!(mounted(TAIL, 500), 380..500);
        assert_eq!(mounted(TAIL, 7), 0..7);
        assert_eq!(mounted(0, 0), 0..0);
    }
}
