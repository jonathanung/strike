//! Generation guard for async UI loads.
//!
//! Overlapping fetches commit in **start** order, not arrival order. Each
//! independent loader owns one [`LoadEpoch`]. Call [`LoadEpoch::begin`] when
//! a load starts. After every `.await`, [`LoadEpoch::accept`] (or
//! [`commit_load`]) decides whether that completion may write.
//!
//! ## Selection change (detail panes)
//!
//! Clear the pane in the same synchronous turn as `begin`, before the first
//! await, so the previous item cannot render under the new selection and
//! cannot stay behind if the new load fails. [`start_load`] is that pair for
//! a plain value. Signals do the same two steps:
//!
//! ```ignore
//! let token = epoch.update(|e| e.begin());
//! value.set(Default::default());
//! error.set(String::new());
//! ```
//!
//! On failure, publish with [`ErrPolicy::Clear`] so the previous body is not
//! kept. On a list refresh that should survive a failed refetch, use
//! [`ErrPolicy::Keep`] and do **not** clear at `begin`.
//!
//! ## Several awaits
//!
//! Check `accept` after **each** await and return before any write. Guarding
//! only the last commit is how a new header lands on the previous load's rows.
//!
//! ## Polls
//!
//! Snapshot [`LoadEpoch::token`] when the poll starts. Do not `begin` — a
//! poll is not a new selection. `accept` then fails if a real load has begun
//! since the snapshot.
//!
//! ## One epoch per loader
//!
//! A page with a list refresh and a detail fetch uses two epochs. A shared
//! counter would drop a list commit when the detail load bumps, or the
//! reverse. The helper is shared; the counters are not.
//!
//! Batch 2 loaders should depend on this crate. Do not add another counter
//! type.

/// Monotonic id of the load that is allowed to commit.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct LoadEpoch {
    current: u64,
}

/// Handle from [`LoadEpoch::begin`] or [`LoadEpoch::token`].
///
/// Generation 0 is never current, so a default token cannot commit.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
#[must_use = "drop the completion unless LoadEpoch::accept says it is current"]
pub struct LoadToken {
    generation: u64,
}

impl LoadEpoch {
    pub fn new() -> Self {
        Self { current: 0 }
    }

    /// Start a load. Only this token commits until the next `begin`.
    pub fn begin(&mut self) -> LoadToken {
        self.current = self.current.wrapping_add(1);
        if self.current == 0 {
            // 0 is reserved so LoadToken::default never accepts.
            self.current = 1;
        }
        LoadToken {
            generation: self.current,
        }
    }

    /// Token of the load currently allowed to commit, without starting one.
    ///
    /// Polls snapshot this. It is rejected once a later [`begin`] runs.
    pub fn token(&self) -> LoadToken {
        LoadToken {
            generation: self.current,
        }
    }

    /// True when `token` is the latest [`begin`] and may write state.
    pub fn accept(&self, token: LoadToken) -> bool {
        token.generation != 0 && token.generation == self.current
    }
}

/// What a failed load does with the value already on screen.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ErrPolicy {
    /// Detail pane: drop the previous item. An error must not leave it up.
    Clear,
    /// List refresh: keep the last successful list and surface the error.
    Keep,
}

/// Result of [`commit_load`]. A superseded completion is [`Commit::Drop`]
/// and must not write signals.
#[derive(Clone, Debug, PartialEq, Eq)]
#[must_use = "publish Apply and ignore Drop; a superseded completion must not write signals"]
pub enum Commit<T> {
    Apply { value: T, error: String },
    Drop,
}

/// Apply `result` only when `token` is still current.
///
/// `previous` is returned unchanged on [`ErrPolicy::Keep`]. [`ErrPolicy::Clear`]
/// substitutes `T::default()` so the caller cannot republish the old item.
pub fn commit_load<T: Default>(
    epoch: &LoadEpoch,
    token: LoadToken,
    result: Result<T, String>,
    previous: T,
    on_err: ErrPolicy,
) -> Commit<T> {
    if !epoch.accept(token) {
        return Commit::Drop;
    }
    match result {
        Ok(value) => Commit::Apply {
            value,
            error: String::new(),
        },
        Err(error) => Commit::Apply {
            value: match on_err {
                ErrPolicy::Clear => T::default(),
                ErrPolicy::Keep => previous,
            },
            error,
        },
    }
}

/// Begin a load and clear `value` and `error` before the request is awaited.
///
/// This is the plain-value form of a selection change. Signal code calls
/// [`LoadEpoch::begin`] and clears the signals in the same turn.
pub fn start_load<T: Default>(
    epoch: &mut LoadEpoch,
    value: &mut T,
    error: &mut String,
) -> LoadToken {
    *value = T::default();
    error.clear();
    epoch.begin()
}

/// Path-keyed loaders (a folder listing) also require the fetched key to
/// still be the key on screen. The epoch rejects a superseded *load*; this
/// rejects a superseded *path* if a caller forgot to begin.
pub fn key_matches(fetched: &str, current: &str) -> bool {
    fetched == current
}

#[cfg(test)]
mod tests {
    use super::*;

    fn apply<T: Default + Clone>(
        epoch: &LoadEpoch,
        token: LoadToken,
        result: Result<T, String>,
        previous: T,
        on_err: ErrPolicy,
    ) -> Option<(T, String)> {
        match commit_load(epoch, token, result, previous, on_err) {
            Commit::Drop => None,
            Commit::Apply { value, error } => Some((value, error)),
        }
    }

    #[test]
    fn default_token_never_commits() {
        let epoch = LoadEpoch::new();
        assert!(!epoch.accept(LoadToken::default()));
        assert!(apply(
            &epoch,
            LoadToken::default(),
            Ok("late".to_string()),
            String::new(),
            ErrPolicy::Clear
        )
        .is_none());
    }

    #[test]
    fn begin_skips_zero_after_wrap() {
        let mut epoch = LoadEpoch::new();
        epoch.current = u64::MAX;
        let token = epoch.begin();
        assert_eq!(token.generation, 1);
        assert!(epoch.accept(token));
    }

    #[test]
    fn start_load_clears_before_the_request() {
        let mut epoch = LoadEpoch::new();
        let mut value = vec!["previous".to_string()];
        let mut error = "old".to_string();
        let token = start_load(&mut epoch, &mut value, &mut error);
        assert!(
            value.is_empty(),
            "selection change clears the previous item"
        );
        assert!(error.is_empty());
        assert!(epoch.accept(token));
    }

    #[test]
    fn failed_load_does_not_keep_the_previous_item() {
        let mut epoch = LoadEpoch::new();
        let mut value = vec!["plan-a".to_string()];
        let mut error = String::new();
        let token = start_load(&mut epoch, &mut value, &mut error);
        let committed = apply(
            &epoch,
            token,
            Err("HTTP 500".into()),
            value.clone(),
            ErrPolicy::Clear,
        )
        .expect("current");
        assert!(committed.0.is_empty());
        assert_eq!(committed.1, "HTTP 500");
    }

    #[test]
    fn keep_policy_retains_the_list_when_the_current_refresh_fails() {
        let mut epoch = LoadEpoch::new();
        let token = epoch.begin();
        let previous = vec!["still-here".to_string()];
        let committed = apply(
            &epoch,
            token,
            Err("HTTP 500".into()),
            previous.clone(),
            ErrPolicy::Keep,
        )
        .expect("current");
        assert_eq!(committed.0, previous);
        assert_eq!(committed.1, "HTTP 500");
    }

    /// #1619 / #1623 / #1626 / #1627 — select B while A's body is showing.
    /// B fails (or is still in flight). A's late success must not paint A
    /// under B, and B's failure must not leave A's body in the slot.
    #[test]
    fn detail_switch_ignores_late_success_and_clears_on_failure() {
        let mut epoch = LoadEpoch::new();
        let mut shown = vec!["A-run".to_string()];
        let mut error = String::new();
        let load_a = start_load(&mut epoch, &mut shown, &mut error);
        assert!(apply(
            &epoch,
            load_a,
            Ok(vec!["A-run".into()]),
            shown.clone(),
            ErrPolicy::Clear
        )
        .is_some());
        shown = vec!["A-run".into()];

        let load_b = start_load(&mut epoch, &mut shown, &mut error);
        assert!(
            shown.is_empty(),
            "B must not render A's rows while its load is in flight"
        );

        // A arrives after B was selected.
        assert!(apply(
            &epoch,
            load_a,
            Ok(vec!["A-run".into()]),
            shown.clone(),
            ErrPolicy::Clear
        )
        .is_none());
        assert!(shown.is_empty());

        let failed = apply(
            &epoch,
            load_b,
            Err("HTTP 500".into()),
            shown.clone(),
            ErrPolicy::Clear,
        )
        .expect("B is current");
        assert!(failed.0.is_empty(), "failure keeps the pane empty");
        assert_eq!(failed.1, "HTTP 500");

        // A second late copy of A still loses.
        assert!(apply(
            &epoch,
            load_a,
            Ok(vec!["A-run".into()]),
            failed.0.clone(),
            ErrPolicy::Clear
        )
        .is_none());
    }

    /// #1634 — a slow refresh started before a delete must not write the
    /// pre-delete list over the delete's own refresh.
    #[test]
    fn slow_refresh_does_not_resurrect_deleted_row() {
        let mut epoch = LoadEpoch::new();
        let slow = epoch.begin();
        let fresh = epoch.begin();
        let kept = apply(
            &epoch,
            fresh,
            Ok(vec!["keep".to_string()]),
            vec!["keep".into(), "deleted".into()],
            ErrPolicy::Keep,
        )
        .expect("post-delete refresh");
        assert_eq!(kept.0, vec!["keep".to_string()]);
        assert!(
            apply(
                &epoch,
                slow,
                Ok(vec!["keep".into(), "deleted".into()]),
                kept.0.clone(),
                ErrPolicy::Keep,
            )
            .is_none(),
            "pre-delete response is superseded"
        );
    }

    /// #1635 — each stage of a pipeline checks the same token. A late status
    /// must not put the old branch back, and a late file list must not land
    /// under the branch the newer load already published.
    #[test]
    fn pipeline_drops_every_stage_of_a_superseded_load() {
        let mut epoch = LoadEpoch::new();
        let slow = epoch.begin();
        let mut branch = "A".to_string();
        let mut rows = vec!["a-file".to_string()];
        rows.clear();
        let fresh = epoch.begin();

        let branch_now = apply(
            &epoch,
            fresh,
            Ok("B".to_string()),
            branch.clone(),
            ErrPolicy::Keep,
        )
        .expect("new status");
        branch = branch_now.0;
        let rows_now = apply(
            &epoch,
            fresh,
            Ok(vec!["b-file".to_string()]),
            rows.clone(),
            ErrPolicy::Keep,
        )
        .expect("new rows");
        rows = rows_now.0;

        assert!(apply(
            &epoch,
            slow,
            Ok("A".into()),
            branch.clone(),
            ErrPolicy::Keep
        )
        .is_none());
        assert!(apply(
            &epoch,
            slow,
            Ok(vec!["a-file".into()]),
            rows.clone(),
            ErrPolicy::Keep
        )
        .is_none());
        assert_eq!(branch, "B");
        assert_eq!(rows, vec!["b-file".to_string()]);
        assert!(!epoch.accept(slow));
    }

    /// #1630 — a checkpoint poll snapshots the generation; selecting another
    /// plan begins a new one. The poll must not restore the previous run,
    /// including when the new load then fails.
    #[test]
    fn poll_snapshot_does_not_restore_the_previous_plan() {
        let mut epoch = LoadEpoch::new();
        let _opened = epoch.begin();
        let poll = epoch.token();
        assert!(epoch.accept(poll));

        let mut run = Some("plan-a".to_string());
        assert_eq!(run.as_deref(), Some("plan-a"));
        run = None;
        let load_b = epoch.begin();
        assert!(!epoch.accept(poll));
        assert!(apply(
            &epoch,
            poll,
            Ok(Some("plan-a".into())),
            run.clone(),
            ErrPolicy::Clear
        )
        .is_none());
        assert!(run.is_none());

        let failed = apply(
            &epoch,
            load_b,
            Err("HTTP 500".into()),
            run.clone(),
            ErrPolicy::Clear,
        )
        .expect("B");
        assert!(failed.0.is_none());
        assert_eq!(failed.1, "HTTP 500");
    }

    /// #1636 — folder B is selected while folder A's listing is still in
    /// flight. A must not commit, and the rows must not be readable as B's
    /// children just because the header changed.
    #[test]
    fn folder_switch_drops_the_previous_listing() {
        let mut epoch = LoadEpoch::new();
        let mut files = vec!["a.txt".to_string()];
        let mut error = String::new();
        let folder_a = "src".to_string();
        let folder_b = "docs".to_string();
        let load_a = start_load(&mut epoch, &mut files, &mut error);
        let _ = apply(
            &epoch,
            load_a,
            Ok(vec!["a.txt".into()]),
            files.clone(),
            ErrPolicy::Clear,
        );
        files = vec!["a.txt".into()];

        let load_b = start_load(&mut epoch, &mut files, &mut error);
        assert!(files.is_empty());
        assert!(!key_matches(&folder_a, &folder_b));
        assert!(apply(
            &epoch,
            load_a,
            Ok(vec!["a.txt".into()]),
            files.clone(),
            ErrPolicy::Clear
        )
        .is_none());

        let failed = apply(
            &epoch,
            load_b,
            Err("HTTP 500".into()),
            files.clone(),
            ErrPolicy::Clear,
        )
        .expect("B");
        assert!(failed.0.is_empty());
        // A retried completion still loses, even if someone also forgot the
        // path check: the epoch alone is enough.
        assert!(!epoch.accept(load_a));
    }

    #[test]
    fn out_of_order_success_keeps_the_later_selection() {
        let mut epoch = LoadEpoch::new();
        let first = epoch.begin();
        let second = epoch.begin();
        // First request finishes last.
        assert!(apply(
            &epoch,
            first,
            Ok("first".to_string()),
            String::new(),
            ErrPolicy::Clear
        )
        .is_none());
        let won = apply(
            &epoch,
            second,
            Ok("second".into()),
            String::new(),
            ErrPolicy::Clear,
        )
        .expect("second");
        assert_eq!(won.0, "second");
        assert!(won.1.is_empty());
    }
}
