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


// ---------------------------------------------------------------------------
// Domain commit helpers ported from PR #1663 (`strike-load-guard`) onto the
// shared `LoadEpoch` / `LoadToken` / `accept` API above. Each helper is the
// pure form of one cockpit call site, so native tests can finish responses
// out of order through the same functions the UI calls.
// ---------------------------------------------------------------------------

/// `Some` only when `token` is still the latest `begin`.
fn resolve<T>(epoch: &LoadEpoch, token: LoadToken, value: T) -> Option<T> {
    if epoch.accept(token) {
        Some(value)
    } else {
        None
    }
}

/// Generation check with no extra scope.
pub fn request_current(epoch: &LoadEpoch, token: LoadToken) -> bool {
    epoch.accept(token)
}

/// Generation check plus an equality scope (gateway base, session root).
/// A completion from a previous scope is dropped even if nothing else
/// has bumped the counter yet.
pub fn request_current_for(
    epoch: &LoadEpoch,
    token: LoadToken,
    live_scope: &str,
    request_scope: &str,
) -> bool {
    epoch.accept(token) && live_scope == request_scope
}

/// Session-list search. A stale `Ok` must not replace hits or clear the
/// current query's error. A stale `Err` must not paint an error for a
/// query that is no longer current.
pub fn apply_search<T>(
    epoch: &LoadEpoch,
    token: LoadToken,
    hits: &mut Vec<T>,
    error: &mut String,
    result: Result<Vec<T>, String>,
) -> bool {
    match resolve(epoch, token, result) {
        None => false,
        Some(Ok(next)) => {
            *hits = next;
            error.clear();
            true
        }
        Some(Err(msg)) => {
            *error = msg;
            true
        }
    }
}

/// Workspace browse listing. Only the latest navigation writes the path
/// the picker actions read.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BrowseView<E> {
    pub path: String,
    pub parent: String,
    pub entries: Vec<E>,
    pub error: String,
}

pub enum BrowseOutcome<E> {
    Ready {
        path: String,
        parent: String,
        entries: Vec<E>,
    },
    Failed(String),
}

pub fn apply_browse<E>(
    epoch: &LoadEpoch,
    token: LoadToken,
    view: &mut BrowseView<E>,
    outcome: BrowseOutcome<E>,
) -> bool {
    let Some(outcome) = resolve(epoch, token, outcome) else {
        return false;
    };
    match outcome {
        BrowseOutcome::Ready {
            path,
            parent,
            entries,
        } => {
            view.path = path;
            view.parent = parent;
            view.entries = entries;
            view.error.clear();
        }
        BrowseOutcome::Failed(msg) => view.error = msg,
    }
    true
}

/// Pairing mint. The button stays disabled (`minting`) until the latest
/// mint commits. A slower earlier mint must not paint its code or devices.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PairPaint<D> {
    pub note: String,
    pub url: String,
    pub svg: String,
    pub warning: String,
    pub devices: Vec<D>,
    pub device_note: String,
}

pub fn begin_mint(epoch: &mut LoadEpoch, minting: &mut bool) -> LoadToken {
    *minting = true;
    epoch.begin()
}

pub fn commit_pair<D>(
    epoch: &LoadEpoch,
    token: LoadToken,
    minting: &mut bool,
    paint: PairPaint<D>,
) -> Option<PairPaint<D>> {
    if !epoch.accept(token) {
        return None;
    }
    *minting = false;
    Some(paint)
}

/// Providers page. A different gateway clears rows before the fetch so
/// one gateway's Connected list is never shown under another. A failed
/// reload of the same gateway keeps the rows and labels them stale.
///
/// Port note: current main no longer loads settings/defaults on this page
/// (the declutter program moved that surface behind the palette), so the
/// view carries only the rows and customs the page still paints.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ProviderView<R, C> {
    pub rows: Vec<R>,
    pub customs: Vec<C>,
    pub load_error: String,
    pub stale_note: String,
    pub loading: bool,
    pub loaded_base: String,
    pub loaded_at: String,
}

pub fn prepare_provider_load<R, C>(
    epoch: &mut LoadEpoch,
    view: &mut ProviderView<R, C>,
    base: &str,
) -> LoadToken {
    let token = epoch.begin();
    view.loading = true;
    if view.loaded_base != base {
        view.rows.clear();
        view.customs.clear();
        view.load_error.clear();
        view.stale_note.clear();
        view.loaded_at.clear();
        view.loaded_base = base.to_string();
    }
    token
}

/// Commit a providers load only when `token` is current. A secondary
/// customs failure leaves the previous values, matching the page's
/// existing disclosure. `loading` becomes false only here.
pub fn apply_provider_load<R, C>(
    epoch: &LoadEpoch,
    token: LoadToken,
    view: &mut ProviderView<R, C>,
    rows: Result<Vec<R>, String>,
    customs: Result<Vec<C>, String>,
    now_label: &str,
) -> bool {
    if !epoch.accept(token) {
        return false;
    }
    match rows {
        Ok(rows) => {
            view.rows = rows;
            view.load_error.clear();
            view.stale_note.clear();
            view.loaded_at = now_label.to_string();
        }
        Err(err) => {
            view.load_error = err;
            if !view.rows.is_empty() {
                view.stale_note = if view.loaded_at.is_empty() {
                    "Showing the last successful load.".into()
                } else {
                    format!("Showing the last successful load from {}.", view.loaded_at)
                };
            }
        }
    }
    if let Ok(customs) = customs {
        view.customs = customs;
    }
    view.loading = false;
    true
}

/// Usage budget field. `Ok((budget_set, daily))` writes the budget only
/// when the daemon says a ceiling is set. A superseded fetch does not
/// touch the field, so a save that already painted stays painted.
pub fn commit_usage(
    epoch: &LoadEpoch,
    token: LoadToken,
    budget: &mut String,
    error: &mut String,
    loaded: &mut bool,
    result: Result<(bool, String), String>,
) -> bool {
    match resolve(epoch, token, result) {
        None => false,
        Some(Ok((set, daily))) => {
            if set {
                *budget = daily;
            }
            error.clear();
            *loaded = true;
            true
        }
        Some(Err(msg)) => {
            *error = msg;
            *loaded = true;
            true
        }
    }
}

/// Audit chip label (`Chain intact` / `Chain broken` / …). A slow mount
/// fetch must not replace the verdict a later verify already painted.
pub fn commit_labeled(
    epoch: &LoadEpoch,
    token: LoadToken,
    label: &mut String,
    error: &mut String,
    loaded: &mut bool,
    result: Result<String, String>,
) -> bool {
    match resolve(epoch, token, result) {
        None => false,
        Some(Ok(next)) => {
            *label = next;
            error.clear();
            *loaded = true;
            true
        }
        Some(Err(msg)) => {
            *error = msg;
            *loaded = true;
            true
        }
    }
}

/// True when `refresh_meta` is running for a different gateway than the
/// lists on screen, so those lists should clear immediately.
pub fn gateway_switched(previous_base: &str, next_base: &str) -> bool {
    previous_base != next_base
}

/// What a bootstrap completion may do to the per-session header.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BootstrapGate {
    /// Token is current and the fetched root is still the active session.
    Paint,
    /// A newer refresh started. Drop the rest of this pipeline.
    Superseded,
    /// This refresh is still current, but the user moved to another session.
    /// Skip the per-session paint and keep going.
    SkipRoot,
}

pub fn bootstrap_gate(
    epoch: &LoadEpoch,
    token: LoadToken,
    captured_root: &str,
    live_root: &str,
) -> BootstrapGate {
    if !epoch.accept(token) {
        return BootstrapGate::Superseded;
    }
    if captured_root.is_empty() || captured_root != live_root {
        BootstrapGate::SkipRoot
    } else {
        BootstrapGate::Paint
    }
}

/// Only the current `refresh_meta` may clear the shell loading flag.
pub fn clear_loading(epoch: &LoadEpoch, token: LoadToken) -> bool {
    epoch.accept(token)
}

/// Decision for one `/v1/session-list` success.
///
/// `claim_migration` is true only for the first current snapshot, so a
/// superseded snapshot cannot POST pins or wipe `strike.pins`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SessionReloadOk {
    pub clear_error: bool,
    pub claim_migration: bool,
    pub post_ids: Vec<String>,
}

pub fn session_reload_ok(
    epoch: &LoadEpoch,
    token: LoadToken,
    live_base: &str,
    request_base: &str,
    already_migrated: bool,
    any_pin: bool,
    local_pins: &[String],
    had_error: bool,
) -> Option<SessionReloadOk> {
    if !request_current_for(epoch, token, live_base, request_base) {
        return None;
    }
    let (claim_migration, post_ids) = if already_migrated {
        (false, Vec::new())
    } else if !any_pin && !local_pins.is_empty() {
        (true, local_pins.to_vec())
    } else {
        (true, Vec::new())
    };
    Some(SessionReloadOk {
        clear_error: had_error,
        claim_migration,
        post_ids,
    })
}

/// `Some(message)` when this failure is still the latest reload.
pub fn session_reload_err(
    epoch: &LoadEpoch,
    token: LoadToken,
    live_base: &str,
    request_base: &str,
    message: String,
) -> Option<String> {
    if request_current_for(epoch, token, live_base, request_base) {
        Some(message)
    } else {
        None
    }
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


    fn pair(url: &str, devices: &[&str]) -> PairPaint<String> {
        PairPaint {
            note: format!("note {url}"),
            url: url.into(),
            svg: format!("svg {url}"),
            warning: String::new(),
            devices: devices.iter().map(|d| (*d).to_string()).collect(),
            device_note: String::new(),
        }
    }

    // The branch's wrapping test expected generation 0 after wrap; the
    // shared API reserves 0 so a default token never commits, and
    // `begin_skips_zero_after_wrap` above covers the wrap instead.

    #[test]
    fn session_search_late_ok_does_not_replace_hits_or_clear_error() {
        let mut epoch = LoadEpoch::new();
        let first = epoch.begin();
        let second = epoch.begin();
        let mut hits: Vec<String> = Vec::new();
        let mut error = String::new();

        assert!(apply_search(
            &epoch,
            second,
            &mut hits,
            &mut error,
            Err("query b failed".into())
        ));
        assert_eq!(error, "query b failed");
        assert!(hits.is_empty());

        assert!(!apply_search(
            &epoch,
            first,
            &mut hits,
            &mut error,
            Ok(vec!["session-a".to_string()])
        ));
        assert_eq!(hits, Vec::<String>::new());
        assert_eq!(error, "query b failed");
    }

    #[test]
    fn session_search_late_err_does_not_paint_over_current_hits() {
        let mut epoch = LoadEpoch::new();
        let first = epoch.begin();
        let second = epoch.begin();
        let mut hits: Vec<String> = Vec::new();
        let mut error = String::new();
        assert!(apply_search(
            &epoch,
            second,
            &mut hits,
            &mut error,
            Ok(vec!["session-b".into()])
        ));
        assert!(!apply_search(
            &epoch,
            first,
            &mut hits,
            &mut error,
            Err("query a failed".into())
        ));
        assert_eq!(hits, vec!["session-b".to_string()]);
        assert!(error.is_empty());
    }

    #[test]
    fn browse_late_folder_does_not_replace_path_or_clear_error() {
        let mut epoch = LoadEpoch::new();
        let folder_a = epoch.begin();
        let folder_b = epoch.begin();
        let mut view = BrowseView {
            path: String::new(),
            parent: String::new(),
            entries: Vec::new(),
            error: String::new(),
        };
        assert!(apply_browse(
            &epoch,
            folder_b,
            &mut view,
            BrowseOutcome::Failed("cannot list b".into()),
        ));
        assert!(!apply_browse(
            &epoch,
            folder_a,
            &mut view,
            BrowseOutcome::Ready {
                path: "/a".into(),
                parent: "/".into(),
                entries: vec!["old"],
            },
        ));
        assert_eq!(view.path, "");
        assert_eq!(view.error, "cannot list b");
        assert!(view.entries.is_empty());

        let folder_c = epoch.begin();
        assert!(apply_browse(
            &epoch,
            folder_c,
            &mut view,
            BrowseOutcome::Ready {
                path: "/b".into(),
                parent: "/".into(),
                entries: vec!["keep"],
            },
        ));
        assert!(!apply_browse(
            &epoch,
            folder_b,
            &mut view,
            BrowseOutcome::Ready {
                path: "/a".into(),
                parent: "/".into(),
                entries: vec!["stale"],
            },
        ));
        assert_eq!(view.path, "/b");
        assert_eq!(view.entries, vec!["keep"]);
        assert!(view.error.is_empty());
    }

    #[test]
    fn pairing_late_mint_does_not_paint_code_or_devices() {
        let mut epoch = LoadEpoch::new();
        let mut minting = false;
        let first = begin_mint(&mut epoch, &mut minting);
        let second = begin_mint(&mut epoch, &mut minting);
        assert!(minting);
        let mut shown = pair("code-2", &["browser-2"]);
        assert!(commit_pair(&epoch, first, &mut minting, pair("code-1", &["revoked-browser"])).is_none());
        assert!(minting, "a superseded mint must leave the button disabled");
        assert_eq!(shown.url, "code-2");
        let painted = commit_pair(&epoch, second, &mut minting, pair("code-2", &["browser-2"]))
            .expect("latest mint commits");
        assert!(!minting);
        shown = painted;
        assert_eq!(shown.url, "code-2");
        assert_eq!(shown.devices, vec!["browser-2".to_string()]);
        assert!(commit_pair(&epoch, first, &mut minting, pair("code-1", &["revoked-browser"])).is_none());
        assert!(!minting);
        assert_eq!(shown.devices, vec!["browser-2".to_string()]);
    }

    #[test]
    fn providers_superseded_load_does_not_mix_rows() {
        let mut epoch = LoadEpoch::new();
        let mut view = ProviderView {
            rows: Vec::<String>::new(),
            customs: Vec::<String>::new(),
            load_error: String::new(),
            stale_note: String::new(),
            loading: false,
            loaded_base: String::new(),
            loaded_at: String::new(),
        };
        let gateway_a = prepare_provider_load(&mut epoch, &mut view, "http://a");
        let gateway_b = prepare_provider_load(&mut epoch, &mut view, "http://b");
        assert!(view.rows.is_empty(), "switching gateways clears the previous list");
        assert!(view.loading);
        assert!(!apply_provider_load(
            &epoch,
            gateway_a,
            &mut view,
            Ok(vec!["a-provider".into()]),
            Ok(vec!["a-custom".into()]),
            "12:00:00",
        ));
        assert!(view.rows.is_empty());
        assert!(view.loading, "only the current load clears the spinner");
        assert!(view.customs.is_empty(), "a stale load must not write gateway A's customs");
        assert!(apply_provider_load(
            &epoch,
            gateway_b,
            &mut view,
            Ok(vec!["b-provider".into()]),
            Ok(vec!["b-custom".into()]),
            "12:00:01",
        ));
        assert_eq!(view.rows, vec!["b-provider".to_string()]);
        assert_eq!(view.customs, vec!["b-custom".to_string()]);
        assert!(!view.loading);
    }

    #[test]
    fn providers_same_gateway_failure_keeps_rows_and_labels_them_stale() {
        let mut epoch = LoadEpoch::new();
        let mut view = ProviderView {
            rows: Vec::<String>::new(),
            customs: Vec::<String>::new(),
            load_error: String::new(),
            stale_note: String::new(),
            loading: false,
            loaded_base: String::new(),
            loaded_at: String::new(),
        };
        let first = prepare_provider_load(&mut epoch, &mut view, "http://a");
        assert!(apply_provider_load(
            &epoch,
            first,
            &mut view,
            Ok(vec!["echo".into()]),
            Ok(vec!["local".into()]),
            "09:15:00",
        ));
        let reload = prepare_provider_load(&mut epoch, &mut view, "http://a");
        assert_eq!(view.rows, vec!["echo".to_string()], "same gateway does not clear");
        assert!(apply_provider_load(
            &epoch,
            reload,
            &mut view,
            Err("could not load".into()),
            Err("custom down".into()),
            "09:15:04",
        ));
        assert_eq!(view.rows, vec!["echo".to_string()]);
        assert_eq!(view.customs, vec!["local".to_string()]);
        assert_eq!(view.load_error, "could not load");
        assert_eq!(
            view.stale_note,
            "Showing the last successful load from 09:15:00."
        );
        assert!(!view.loading);
    }

    #[test]
    fn usage_saved_budget_survives_slow_prefetch() {
        let mut epoch = LoadEpoch::new();
        let prefetch = epoch.begin();
        let after_save = epoch.begin();
        let mut budget = String::new();
        let mut error = String::new();
        let mut loaded = false;
        assert!(commit_usage(
            &epoch,
            after_save,
            &mut budget,
            &mut error,
            &mut loaded,
            Ok((true, "12".into()))
        ));
        assert_eq!(budget, "12");
        assert!(!commit_usage(
            &epoch,
            prefetch,
            &mut budget,
            &mut error,
            &mut loaded,
            Ok((true, "1".into()))
        ));
        assert_eq!(budget, "12");
        assert!(error.is_empty());
    }

    #[test]
    fn audit_verdict_survives_slow_mount_fetch() {
        let mut epoch = LoadEpoch::new();
        let mount = epoch.begin();
        let after_verify = epoch.begin();
        let mut label = String::new();
        let mut error = String::new();
        let mut loaded = false;
        assert!(commit_labeled(
            &epoch,
            after_verify,
            &mut label,
            &mut error,
            &mut loaded,
            Ok("Chain broken".into())
        ));
        assert!(!commit_labeled(
            &epoch,
            mount,
            &mut label,
            &mut error,
            &mut loaded,
            Ok("Chain intact".into())
        ));
        assert_eq!(label, "Chain broken");
        assert!(commit_labeled(
            &epoch,
            after_verify,
            &mut label,
            &mut error,
            &mut loaded,
            Err("verify failed".into())
        ));
        assert_eq!(error, "verify failed");
        assert!(!commit_labeled(
            &epoch,
            mount,
            &mut label,
            &mut error,
            &mut loaded,
            Ok("Chain intact".into())
        ));
        assert_eq!(label, "Chain broken");
        assert_eq!(error, "verify failed");
    }

    #[test]
    fn shell_old_gateway_and_old_session_do_not_paint() {
        let mut epoch = LoadEpoch::new();
        let old_gateway = epoch.begin();
        assert!(gateway_switched("http://a", "http://b"));
        assert!(!gateway_switched("http://b", "http://b"));
        let new_gateway = epoch.begin();
        assert!(!request_current(&epoch, old_gateway));
        assert!(request_current(&epoch, new_gateway));
        assert_eq!(
            bootstrap_gate(&epoch, old_gateway, "session-a", "session-a"),
            BootstrapGate::Superseded
        );
        assert!(!clear_loading(&epoch, old_gateway));
        assert!(clear_loading(&epoch, new_gateway));
        assert_eq!(
            bootstrap_gate(&epoch, new_gateway, "session-a", "session-b"),
            BootstrapGate::SkipRoot
        );
        assert_eq!(
            bootstrap_gate(&epoch, new_gateway, "session-b", "session-b"),
            BootstrapGate::Paint
        );
    }

    #[test]
    fn session_reload_stale_snapshot_does_not_migrate_or_wipe_pins() {
        let mut epoch = LoadEpoch::new();
        let stale = epoch.begin();
        let fresh = epoch.begin();
        let local = vec!["pin-a".to_string()];
        let decision = session_reload_ok(
            &epoch,
            fresh,
            "http://gw",
            "http://gw",
            false,
            true,
            &local,
            false,
        )
        .expect("current snapshot commits");
        assert!(decision.claim_migration);
        assert!(decision.post_ids.is_empty(), "server already has pins");
        assert!(session_reload_ok(
            &epoch,
            stale,
            "http://gw",
            "http://gw",
            false,
            false,
            &local,
            false,
        )
        .is_none());
        assert!(session_reload_err(
            &epoch,
            stale,
            "http://gw",
            "http://gw",
            "old failure".into(),
        )
        .is_none());
    }

    #[test]
    fn session_reload_current_snapshot_is_the_one_that_posts_pins() {
        let mut epoch = LoadEpoch::new();
        let stale = epoch.begin();
        let fresh = epoch.begin();
        let local = vec!["pin-a".to_string(), "pin-b".to_string()];
        assert!(session_reload_ok(
            &epoch,
            stale,
            "http://gw",
            "http://gw",
            false,
            false,
            &local,
            false,
        )
        .is_none());
        let decision = session_reload_ok(
            &epoch,
            fresh,
            "http://gw",
            "http://gw",
            false,
            false,
            &local,
            true,
        )
        .expect("newest snapshot drives migration");
        assert!(decision.clear_error);
        assert!(decision.claim_migration);
        assert_eq!(decision.post_ids, local);
        assert!(
            !request_current_for(&epoch, stale, "http://gw", "http://gw"),
            "stale reload must not wipe strike.pins"
        );
        assert!(request_current_for(&epoch, fresh, "http://gw", "http://gw"));
    }

    #[test]
    fn session_reload_drops_previous_gateway_base() {
        let mut epoch = LoadEpoch::new();
        let token = epoch.begin();
        assert!(session_reload_ok(
            &epoch,
            token,
            "http://new",
            "http://old",
            false,
            false,
            &["pin-a".into()],
            false,
        )
        .is_none());
        assert!(session_reload_err(
            &epoch,
            token,
            "http://new",
            "http://old",
            "nope".into(),
        )
        .is_none());
    }

    #[test]
    fn session_reload_late_success_does_not_clear_a_newer_error() {
        let mut epoch = LoadEpoch::new();
        let first = epoch.begin();
        let second = epoch.begin();
        let err = session_reload_err(&epoch, second, "", "", "list failed".into())
            .expect("current failure paints");
        assert_eq!(err, "list failed");
        assert!(session_reload_ok(&epoch, first, "", "", false, false, &[], true).is_none());
    }
}
