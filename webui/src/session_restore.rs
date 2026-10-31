//! Session restore on reload (#1391): persist the viewed session like
//! theme and layout already are, so a browser reload returns to the
//! session the user was reading instead of the daemon's active session
//! or the first session in the list.
//!
//! Storage only, per-device (localStorage) — no URL routing and no
//! daemon change. The daemon already reports the roots and saved
//! sessions needed to validate the saved id: a saved id that no longer
//! exists is stale, is cleared, and the cockpit falls back to exactly
//! the pre-#1391 behaviour (daemon `activeId`, else the first root) so
//! a deleted session can never strand the user on an empty screen.
//!
//! The decision itself is pure (no `web_sys`) so it is testable
//! off-wasm, like `session_delete`'s helpers; the storage wrappers are
//! thin shells over the `load` / `save` helpers in `main.rs`.

/// The one storage key this feature owns, following the existing
/// `strike.*` naming (`strike.theme`, `strike.layout`, …).
pub const ACTIVE_SESSION_KEY: &str = "strike.activeSession";

pub fn saved_id() -> String {
    crate::load(ACTIVE_SESSION_KEY)
}

/// Record the session the user is now viewing. An empty id clears the
/// key: there is no viewed session to restore.
pub fn persist(id: &str) {
    if id.is_empty() {
        clear();
    } else {
        crate::save(ACTIVE_SESSION_KEY, id);
    }
}

pub fn clear() {
    crate::remove_key(ACTIVE_SESSION_KEY);
}

/// The pre-#1391 fallback, unchanged: the daemon's active id when it
/// sent one, else the first root, else nothing.
pub fn fallback_id(daemon_active: &str, root_ids: &[String]) -> String {
    if !daemon_active.is_empty() {
        daemon_active.to_string()
    } else {
        root_ids.first().cloned().unwrap_or_default()
    }
}

/// What the first load should open.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct InitialPick {
    /// Session to view; empty when there is nothing to open yet.
    pub id: String,
    /// The pick is a saved (dormant) session, not a live root: it must
    /// go through the resume endpoint before it can be viewed.
    pub resume: bool,
    /// The saved id matched nothing the daemon reports — it is stale
    /// and the caller should clear it (the pick is then the fallback).
    pub clear_saved: bool,
}

/// Decide the first session to view.
///
/// Preference order: the saved id when it is still a live root; the
/// saved id when it still exists as a saved session (resume); else the
/// historical fallback exactly. A saved id that matches neither list
/// is stale and is never picked, so a deleted session falls back
/// instead of opening an empty screen.
pub fn pick_initial(
    saved: &str,
    root_ids: &[String],
    session_ids: &[String],
    daemon_active: &str,
) -> InitialPick {
    if !saved.is_empty() {
        if root_ids.iter().any(|id| id == saved) {
            return InitialPick {
                id: saved.to_string(),
                resume: false,
                clear_saved: false,
            };
        }
        if session_ids.iter().any(|id| id == saved) {
            return InitialPick {
                id: saved.to_string(),
                resume: true,
                clear_saved: false,
            };
        }
        return InitialPick {
            id: fallback_id(daemon_active, root_ids),
            resume: false,
            clear_saved: true,
        };
    }
    InitialPick {
        id: fallback_id(daemon_active, root_ids),
        resume: false,
        clear_saved: false,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ids(list: &[&str]) -> Vec<String> {
        list.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn saved_live_root_wins_over_daemon_active() {
        let pick = pick_initial("b", &ids(&["a", "b"]), &ids(&["a", "b"]), "a");
        assert_eq!(pick.id, "b");
        assert!(!pick.resume);
        assert!(!pick.clear_saved);
    }

    #[test]
    fn saved_dormant_session_is_picked_for_resume() {
        // Not a live root, but still in the daemon's saved sessions.
        let pick = pick_initial("b", &ids(&["a"]), &ids(&["a", "b"]), "a");
        assert_eq!(pick.id, "b");
        assert!(pick.resume);
        assert!(!pick.clear_saved);
    }

    #[test]
    fn stale_saved_id_falls_back_to_daemon_active_and_clears() {
        let pick = pick_initial("gone", &ids(&["a", "b"]), &ids(&["a", "b"]), "a");
        assert_eq!(pick.id, "a");
        assert!(!pick.resume);
        assert!(pick.clear_saved);
    }

    #[test]
    fn stale_saved_id_falls_back_to_first_root_without_daemon_active() {
        let pick = pick_initial("gone", &ids(&["a", "b"]), &ids(&["a"]), "");
        assert_eq!(pick.id, "a");
        assert!(pick.clear_saved);
    }

    #[test]
    fn stale_saved_id_with_nothing_else_opens_nothing_and_clears() {
        let pick = pick_initial("gone", &ids(&[]), &ids(&[]), "");
        assert_eq!(pick.id, "");
        assert!(pick.clear_saved);
    }

    #[test]
    fn no_saved_id_is_exactly_the_old_fallback() {
        let pick = pick_initial("", &ids(&["a", "b"]), &ids(&["a", "b"]), "b");
        assert_eq!(pick.id, "b");
        assert!(!pick.resume);
        assert!(!pick.clear_saved);
        let pick = pick_initial("", &ids(&["a", "b"]), &ids(&[]), "");
        assert_eq!(pick.id, "a");
        assert!(!pick.clear_saved);
    }

    #[test]
    fn nothing_anywhere_opens_nothing() {
        let pick = pick_initial("", &ids(&[]), &ids(&[]), "");
        assert_eq!(pick.id, "");
        assert!(!pick.resume);
        assert!(!pick.clear_saved);
    }

    #[test]
    fn saved_id_equal_to_daemon_active_is_a_plain_live_pick() {
        let pick = pick_initial("a", &ids(&["a"]), &ids(&["a"]), "a");
        assert_eq!(pick.id, "a");
        assert!(!pick.resume);
        assert!(!pick.clear_saved);
    }
}
