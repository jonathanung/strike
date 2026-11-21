//! Open-file history and recents for the document viewer (#1395).
//!
//! A per-browser, in-memory visited-files stack (like the session
//! back/forward stacks) plus a most-recent-first Recent list. The page
//! owns one [`FileHistory`] signal; the document pane records each file
//! that actually loads and steps through the stack with Back/Forward.
//! A step only *requests* a path (it still passes through the unsaved
//! guard, #1394); the stack position moves when that file loads, so a
//! Stay on the guard leaves history where it was.

/// Most entries kept in the stack and in Recent.
pub const CAP: usize = 50;
/// Rows shown in the Files pane's Recent section.
pub const RECENT_SHOWN: usize = 8;

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct FileHistory {
    entries: Vec<String>,
    pos: usize,
    /// Stack index a Back/Forward step asked for, applied when that
    /// path loads.
    pending: Option<usize>,
    recent: Vec<String>,
}

impl FileHistory {
    /// Record that `path` loaded in the viewer. A load answering a
    /// pending step moves the position there; reopening the current
    /// file is not a new entry; anything else drops the forward branch
    /// and pushes (browser-style), capped at [`CAP`].
    pub fn visit(&mut self, path: &str) {
        if path.is_empty() {
            return;
        }
        let pending = self.pending.take();
        self.touch_recent(path);
        if let Some(i) = pending {
            if self.entries.get(i).map(String::as_str) == Some(path) {
                self.pos = i;
                return;
            }
        }
        if self.current() == Some(path) {
            return;
        }
        if !self.entries.is_empty() {
            self.entries.truncate(self.pos + 1);
        }
        self.entries.push(path.to_string());
        if self.entries.len() > CAP {
            let drop = self.entries.len() - CAP;
            self.entries.drain(..drop);
        }
        self.pos = self.entries.len() - 1;
    }

    pub fn current(&self) -> Option<&str> {
        self.entries.get(self.pos).map(String::as_str)
    }

    pub fn can_back(&self) -> bool {
        self.pos > 0 && !self.entries.is_empty()
    }

    pub fn can_forward(&self) -> bool {
        self.pos + 1 < self.entries.len()
    }

    /// Ask to step `delta` entries (-1 back, +1 forward). Returns the
    /// path to open and remembers the target index, or `None` at an end.
    pub fn begin_step(&mut self, delta: i32) -> Option<String> {
        let target = self.pos as i64 + i64::from(delta);
        if target < 0 || target as usize >= self.entries.len() {
            return None;
        }
        let i = target as usize;
        self.pending = Some(i);
        Some(self.entries[i].clone())
    }

    /// Up to `n` recent files, most recent first (deduped).
    pub fn recent(&self, n: usize) -> Vec<String> {
        self.recent.iter().take(n).cloned().collect()
    }

    fn touch_recent(&mut self, path: &str) {
        self.recent.retain(|p| p != path);
        self.recent.insert(0, path.to_string());
        self.recent.truncate(CAP);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn visited(paths: &[&str]) -> FileHistory {
        let mut h = FileHistory::default();
        for p in paths {
            h.visit(p);
        }
        h
    }

    #[test]
    fn empty_history_has_no_steps() {
        let mut h = FileHistory::default();
        assert_eq!(h.current(), None);
        assert!(!h.can_back());
        assert!(!h.can_forward());
        assert_eq!(h.begin_step(-1), None);
        assert_eq!(h.begin_step(1), None);
        h.visit("");
        assert_eq!(h, FileHistory::default());
    }

    #[test]
    fn back_and_forward_move_only_when_the_file_loads() {
        let mut h = visited(&["a", "b", "c"]);
        assert_eq!(h.current(), Some("c"));
        assert!(h.can_back() && !h.can_forward());
        assert_eq!(h.begin_step(-1).as_deref(), Some("b"));
        // Not loaded yet (the guard may still hold it): no move.
        assert_eq!(h.current(), Some("c"));
        h.visit("b");
        assert_eq!(h.current(), Some("b"));
        assert_eq!(h.begin_step(-1).as_deref(), Some("a"));
        h.visit("a");
        assert!(!h.can_back() && h.can_forward());
        assert_eq!(h.begin_step(1).as_deref(), Some("b"));
        h.visit("b");
        assert_eq!(h.current(), Some("b"));
        assert!(h.can_back() && h.can_forward());
    }

    #[test]
    fn a_new_open_after_back_drops_the_forward_branch() {
        let mut h = visited(&["a", "b", "c"]);
        h.begin_step(-1);
        h.visit("b");
        h.visit("d");
        assert_eq!(h.current(), Some("d"));
        assert!(!h.can_forward());
        assert_eq!(h.begin_step(-1).as_deref(), Some("b"));
    }

    #[test]
    fn a_held_step_that_never_loads_does_not_move_history() {
        let mut h = visited(&["a", "b"]);
        assert_eq!(h.begin_step(-1).as_deref(), Some("a"));
        // Guard Stay: the next load is some other file.
        h.visit("c");
        assert_eq!(h.current(), Some("c"));
        assert_eq!(h.begin_step(-1).as_deref(), Some("b"));
    }

    #[test]
    fn consecutive_repeats_are_one_entry() {
        let h = visited(&["a", "a", "b", "b"]);
        let mut h2 = h.clone();
        assert_eq!(h2.begin_step(-1).as_deref(), Some("a"));
        h2.visit("a");
        assert!(!h2.can_back());
        assert_eq!(h.recent(10), vec!["b".to_string(), "a".to_string()]);
    }

    #[test]
    fn recent_moves_revisits_to_the_top_and_caps() {
        let h = visited(&["a", "b", "a", "c"]);
        assert_eq!(h.recent(10), vec!["c", "a", "b"]);
        assert_eq!(h.recent(2), vec!["c", "a"]);
        let many: Vec<String> = (0..CAP + 5).map(|i| format!("f{i}")).collect();
        let refs: Vec<&str> = many.iter().map(String::as_str).collect();
        let mut h = visited(&refs);
        assert_eq!(h.recent(usize::MAX).len(), CAP);
        assert_eq!(h.recent(1), vec![format!("f{}", CAP + 4)]);
        // The stack is capped too, oldest dropped.
        let mut steps = 0;
        while let Some(p) = h.begin_step(-1) {
            h.visit(&p);
            steps += 1;
        }
        assert_eq!(steps, CAP - 1);
        assert_eq!(h.current(), Some("f5"));
    }
}
