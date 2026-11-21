//! Focus decisions for modal dialogs (#1878). The webui crate's
//! `focus_trap` module applies them to every `aria-modal="true"` element.

/// What a Tab press inside an open modal should do.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TabMove {
    /// The browser's own Tab order already stays inside the modal.
    Browser,
    /// Prevent the default and focus the focusable at this index.
    Focus(usize),
    /// The modal has nothing to focus: prevent the default so focus
    /// cannot leave it.
    Hold,
}

/// Tab inside a modal with `count` focusable elements, in document order.
/// `current` is the focused element's index in that list, or `None` when
/// focus is outside it. Tab past the last wraps to the first; Shift+Tab
/// before the first wraps to the last; focus that escaped comes back in.
pub fn trap_tab(count: usize, current: Option<usize>, shift: bool) -> TabMove {
    if count == 0 {
        return TabMove::Hold;
    }
    match current {
        None if shift => TabMove::Focus(count - 1),
        None => TabMove::Focus(0),
        Some(i) if i >= count => TabMove::Focus(0),
        Some(i) if !shift && i == count - 1 => TabMove::Focus(0),
        Some(0) if shift => TabMove::Focus(count - 1),
        Some(_) => TabMove::Browser,
    }
}

/// The element to return focus to when a modal closes: the most recent
/// entry of `history` (oldest first) that `usable` accepts — still in the
/// page and outside the modal that just opened.
pub fn pick_opener<T>(history: &[T], usable: impl Fn(&T) -> bool) -> Option<&T> {
    history.iter().rev().find(|el| usable(el))
}

/// Append `el` as the newest history entry, dropping an older copy and
/// keeping at most `cap` entries.
pub fn remember<T: PartialEq>(history: &mut Vec<T>, el: T, cap: usize) {
    history.retain(|e| e != &el);
    history.push(el);
    if history.len() > cap {
        let drop = history.len() - cap;
        history.drain(..drop);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn trap_tab_cases() {
        let cases = [
            (0, None, false, TabMove::Hold),
            (0, Some(0), true, TabMove::Hold),
            (3, None, false, TabMove::Focus(0)),
            (3, None, true, TabMove::Focus(2)),
            (3, Some(0), false, TabMove::Browser),
            (3, Some(1), false, TabMove::Browser),
            (3, Some(2), false, TabMove::Focus(0)),
            (3, Some(0), true, TabMove::Focus(2)),
            (3, Some(1), true, TabMove::Browser),
            (3, Some(2), true, TabMove::Browser),
            (1, Some(0), false, TabMove::Focus(0)),
            (1, Some(0), true, TabMove::Focus(0)),
            (3, Some(7), false, TabMove::Focus(0)),
        ];
        for (count, current, shift, want) in cases {
            assert_eq!(trap_tab(count, current, shift), want, "count={count} current={current:?} shift={shift}");
        }
    }

    #[test]
    fn pick_opener_takes_newest_usable() {
        let history = ["composer", "palette-input", "gone"];
        assert_eq!(pick_opener(&history, |e| *e != "gone" && *e != "palette-input"), Some(&"composer"));
        assert_eq!(pick_opener(&history, |e| *e != "gone"), Some(&"palette-input"));
        assert_eq!(pick_opener(&history, |_| false), None);
        assert_eq!(pick_opener::<&str>(&[], |_| true), None);
    }

    #[test]
    fn remember_dedupes_and_caps() {
        let mut h = vec![1, 2, 3];
        remember(&mut h, 2, 8);
        assert_eq!(h, vec![1, 3, 2]);
        remember(&mut h, 4, 3);
        assert_eq!(h, vec![3, 2, 4]);
        remember(&mut h, 5, 2);
        assert_eq!(h, vec![4, 5]);
    }
}
