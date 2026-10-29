//! Monotonic request tokens for async cockpit loads.
//!
//! A load calls [`LoadEpoch::begin`] before it awaits and commits only
//! when that token is still current. A slower response for an earlier
//! request must not overwrite the state the later request already
//! published — including one-time side effects such as pin migration.

/// Generation counter. `begin` advances it; only the latest token commits.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct LoadEpoch {
    n: u64,
}

/// Token captured for one in-flight request.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Token {
    n: u64,
}

impl LoadEpoch {
    /// Start a request. The returned token is the only one that may commit
    /// until the next `begin`.
    pub fn begin(&mut self) -> Token {
        self.n = self.n.wrapping_add(1);
        Token { n: self.n }
    }

    pub fn is_current(&self, token: Token) -> bool {
        self.n == token.n
    }

    /// `Some` only when `token` is still the latest `begin`.
    pub fn resolve<T>(&self, token: Token, value: T) -> Option<T> {
        if self.is_current(token) {
            Some(value)
        } else {
            None
        }
    }
}

/// Generation check with no extra scope.
pub fn request_current(epoch: &LoadEpoch, token: Token) -> bool {
    epoch.is_current(token)
}

/// Generation check plus an equality scope (gateway base, session root).
/// A completion from a previous scope is dropped even if nothing else
/// has bumped the counter yet.
pub fn request_current_for(
    epoch: &LoadEpoch,
    token: Token,
    live_scope: &str,
    request_scope: &str,
) -> bool {
    epoch.is_current(token) && live_scope == request_scope
}

/// Session-list search. A stale `Ok` must not replace hits or clear the
/// current query's error. A stale `Err` must not paint an error for a
/// query that is no longer current.
pub fn apply_search<T>(
    epoch: &LoadEpoch,
    token: Token,
    hits: &mut Vec<T>,
    error: &mut String,
    result: Result<Vec<T>, String>,
) -> bool {
    match epoch.resolve(token, result) {
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
    token: Token,
    view: &mut BrowseView<E>,
    outcome: BrowseOutcome<E>,
) -> bool {
    let Some(outcome) = epoch.resolve(token, outcome) else {
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

pub fn begin_mint(epoch: &mut LoadEpoch, minting: &mut bool) -> Token {
    *minting = true;
    epoch.begin()
}

pub fn commit_pair<D>(
    epoch: &LoadEpoch,
    token: Token,
    minting: &mut bool,
    paint: PairPaint<D>,
) -> Option<PairPaint<D>> {
    if !epoch.is_current(token) {
        return None;
    }
    *minting = false;
    Some(paint)
}

/// Providers page. A different gateway clears rows before the fetch so
/// one gateway's Connected list is never shown under another. A failed
/// reload of the same gateway keeps the rows and labels them stale.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ProviderView<R, C, D> {
    pub rows: Vec<R>,
    pub customs: Vec<C>,
    pub defaults: D,
    pub load_error: String,
    pub stale_note: String,
    pub loading: bool,
    pub loaded_base: String,
    pub loaded_at: String,
}

pub fn prepare_provider_load<R, C, D: Default>(
    epoch: &mut LoadEpoch,
    view: &mut ProviderView<R, C, D>,
    base: &str,
) -> Token {
    let token = epoch.begin();
    view.loading = true;
    if view.loaded_base != base {
        view.rows.clear();
        view.customs.clear();
        view.defaults = D::default();
        view.load_error.clear();
        view.stale_note.clear();
        view.loaded_at.clear();
        view.loaded_base = base.to_string();
    }
    token
}

/// Commit a providers load only when `token` is current. Secondary
/// failures (`Err` customs / defaults) leave the previous values, matching
/// the page's existing disclosure. `loading` becomes false only here.
pub fn apply_provider_load<R, C, D>(
    epoch: &LoadEpoch,
    token: Token,
    view: &mut ProviderView<R, C, D>,
    rows: Result<Vec<R>, String>,
    customs: Result<Vec<C>, String>,
    defaults: Result<D, String>,
    now_label: &str,
) -> bool {
    if !epoch.is_current(token) {
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
    if let Ok(defaults) = defaults {
        view.defaults = defaults;
    }
    view.loading = false;
    true
}

/// Usage budget field. `Ok((budget_set, daily))` writes the budget only
/// when the daemon says a ceiling is set. A superseded fetch does not
/// touch the field, so a save that already painted stays painted.
pub fn commit_usage(
    epoch: &LoadEpoch,
    token: Token,
    budget: &mut String,
    error: &mut String,
    loaded: &mut bool,
    result: Result<(bool, String), String>,
) -> bool {
    match epoch.resolve(token, result) {
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
    token: Token,
    label: &mut String,
    error: &mut String,
    loaded: &mut bool,
    result: Result<String, String>,
) -> bool {
    match epoch.resolve(token, result) {
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
    token: Token,
    captured_root: &str,
    live_root: &str,
) -> BootstrapGate {
    if !epoch.is_current(token) {
        return BootstrapGate::Superseded;
    }
    if captured_root.is_empty() || captured_root != live_root {
        BootstrapGate::SkipRoot
    } else {
        BootstrapGate::Paint
    }
}

/// Only the current `refresh_meta` may clear the shell loading flag.
pub fn clear_loading(epoch: &LoadEpoch, token: Token) -> bool {
    epoch.is_current(token)
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
    token: Token,
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
    token: Token,
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

    #[test]
    fn wrapping_begin_stays_ordered() {
        let mut epoch = LoadEpoch { n: u64::MAX };
        let first = epoch.begin();
        assert_eq!(first.n, 0);
        assert!(epoch.is_current(first));
        let second = epoch.begin();
        assert!(!epoch.is_current(first));
        assert!(epoch.is_current(second));
    }

    #[test]
    fn session_search_late_ok_does_not_replace_hits_or_clear_error() {
        let mut epoch = LoadEpoch::default();
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
        let mut epoch = LoadEpoch::default();
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
        let mut epoch = LoadEpoch::default();
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
        let mut epoch = LoadEpoch::default();
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
        let mut epoch = LoadEpoch::default();
        let mut view = ProviderView {
            rows: Vec::<String>::new(),
            customs: Vec::<String>::new(),
            defaults: "none".to_string(),
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
            Ok("a-default".into()),
            "12:00:00",
        ));
        assert!(view.rows.is_empty());
        assert!(view.loading, "only the current load clears the spinner");
        assert_eq!(view.defaults, "", "a stale load must not write gateway A's default");
        assert!(apply_provider_load(
            &epoch,
            gateway_b,
            &mut view,
            Ok(vec!["b-provider".into()]),
            Ok(vec!["b-custom".into()]),
            Ok("b-default".into()),
            "12:00:01",
        ));
        assert_eq!(view.rows, vec!["b-provider".to_string()]);
        assert_eq!(view.customs, vec!["b-custom".to_string()]);
        assert_eq!(view.defaults, "b-default");
        assert!(!view.loading);
    }

    #[test]
    fn providers_same_gateway_failure_keeps_rows_and_labels_them_stale() {
        let mut epoch = LoadEpoch::default();
        let mut view = ProviderView {
            rows: Vec::<String>::new(),
            customs: Vec::<String>::new(),
            defaults: String::new(),
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
            Ok("echo/default".into()),
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
            Err("settings down".into()),
            "09:15:04",
        ));
        assert_eq!(view.rows, vec!["echo".to_string()]);
        assert_eq!(view.customs, vec!["local".to_string()]);
        assert_eq!(view.defaults, "echo/default");
        assert_eq!(view.load_error, "could not load");
        assert_eq!(
            view.stale_note,
            "Showing the last successful load from 09:15:00."
        );
        assert!(!view.loading);
    }

    #[test]
    fn usage_saved_budget_survives_slow_prefetch() {
        let mut epoch = LoadEpoch::default();
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
        let mut epoch = LoadEpoch::default();
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
        let mut epoch = LoadEpoch::default();
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
        let mut epoch = LoadEpoch::default();
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
        let mut epoch = LoadEpoch::default();
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
        let mut epoch = LoadEpoch::default();
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
        let mut epoch = LoadEpoch::default();
        let first = epoch.begin();
        let second = epoch.begin();
        let err = session_reload_err(&epoch, second, "", "", "list failed".into())
            .expect("current failure paints");
        assert_eq!(err, "list failed");
        assert!(session_reload_ok(&epoch, first, "", "", false, false, &[], true).is_none());
    }
}
