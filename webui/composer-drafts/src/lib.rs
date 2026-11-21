//! Per-session composer drafts (#1367).
//!
//! The cockpit composer used to hold one global draft string: text typed
//! in session A was still sitting in session B's composer after a switch
//! (one tap from being sent to the wrong session), and a reload lost it.
//! [`DraftBook`] keys the draft by session root and persists it under
//! `strike.draft.<root>` so it survives both a switch and a reload.
//!
//! The book is pure (no `web_sys`) so it is testable off-wasm; the
//! browser binds it to `localStorage` through [`DraftStore`]. Storage is
//! best-effort: a failed or refused write (private mode, quota) leaves
//! the in-memory copy authoritative, so switch isolation still holds and
//! the composer keeps working — only reload persistence is lost.
//!
//! Only the main composer uses this prefix; the side-chat draft must
//! never share it.

use std::collections::HashMap;

/// Storage key prefix, alongside the cockpit's other `strike.*` keys.
pub const KEY_PREFIX: &str = "strike.draft.";

/// Drafts longer than this (in chars) stay in memory only: a forgotten
/// huge paste per session should not eat the origin's storage quota.
pub const MAX_PERSIST_CHARS: usize = 100_000;

/// Browser-local key/value storage. Implementations must not panic or
/// log on failure; the book treats every failure as "not persisted".
pub trait DraftStore {
    fn get(&self, key: &str) -> Option<String>;
    /// Returns false when the write did not land (quota, private mode).
    fn set(&mut self, key: &str, value: &str) -> bool;
    fn remove(&mut self, key: &str);
}

/// The storage key for a session's draft. The empty root (no session
/// open yet) has no durable key: its draft lives in memory only.
pub fn storage_key(root: &str) -> Option<String> {
    if root.is_empty() {
        None
    } else {
        Some(format!("{KEY_PREFIX}{root}"))
    }
}

/// Per-session drafts. `owner` is the session the composer's current
/// text belongs to; `memory` caches every draft seen this page load and
/// is authoritative over storage once a root has been touched.
pub struct DraftBook<S: DraftStore> {
    store: S,
    owner: Option<String>,
    memory: HashMap<String, String>,
}

impl<S: DraftStore> DraftBook<S> {
    pub fn new(store: S) -> Self {
        Self {
            store,
            owner: None,
            memory: HashMap::new(),
        }
    }

    /// Reconcile the composer with the viewed session. Call it whenever
    /// either the active root or the draft text changes.
    ///
    /// Same session: the text is recorded as that session's draft and
    /// `None` is returned. Different session: the text is saved as the
    /// previous owner's draft, the new root becomes the owner, and its
    /// stored draft is returned when it differs from what the composer
    /// shows, so the caller can put it in the composer.
    pub fn sync(&mut self, root: &str, text: &str) -> Option<String> {
        if self.owner.as_deref() == Some(root) {
            self.record(root, text);
            return None;
        }
        if let Some(prev) = self.owner.take() {
            self.record(&prev, text);
        }
        self.owner = Some(root.to_string());
        let restored = self.recall(root);
        if restored == text {
            None
        } else {
            Some(restored)
        }
    }

    /// Drop a session's draft (session deleted). When that session owns
    /// the composer, the owner is cleared too, so the next `sync` does
    /// not write the deleted session's text back.
    pub fn forget(&mut self, root: &str) {
        self.memory.remove(root);
        if let Some(key) = storage_key(root) {
            self.store.remove(&key);
        }
        if self.owner.as_deref() == Some(root) {
            self.owner = None;
        }
    }

    /// The stored draft for `root`, empty when there is none.
    pub fn recall(&mut self, root: &str) -> String {
        if let Some(text) = self.memory.get(root) {
            return text.clone();
        }
        let text = storage_key(root)
            .and_then(|key| self.store.get(&key))
            .unwrap_or_default();
        self.memory.insert(root.to_string(), text.clone());
        text
    }

    /// Session the composer text currently belongs to.
    pub fn owner(&self) -> Option<&str> {
        self.owner.as_deref()
    }

    pub fn store(&self) -> &S {
        &self.store
    }

    fn record(&mut self, root: &str, text: &str) {
        if self.memory.get(root).map(String::as_str) == Some(text) {
            return;
        }
        self.memory.insert(root.to_string(), text.to_string());
        let Some(key) = storage_key(root) else {
            return;
        };
        // An empty draft has nothing to keep; an oversized one or a
        // refused write must not leave an older version behind for the
        // next reload to restore.
        if text.is_empty()
            || text.chars().count() > MAX_PERSIST_CHARS
            || !self.store.set(&key, text)
        {
            self.store.remove(&key);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[derive(Default)]
    struct MemStore {
        items: HashMap<String, String>,
        refuse_writes: bool,
        writes: usize,
    }

    impl DraftStore for MemStore {
        fn get(&self, key: &str) -> Option<String> {
            self.items.get(key).cloned()
        }
        fn set(&mut self, key: &str, value: &str) -> bool {
            self.writes += 1;
            if self.refuse_writes {
                return false;
            }
            self.items.insert(key.to_string(), value.to_string());
            true
        }
        fn remove(&mut self, key: &str) {
            self.items.remove(key);
        }
    }

    fn stored(book: &DraftBook<MemStore>, root: &str) -> Option<String> {
        book.store().items.get(&format!("{KEY_PREFIX}{root}")).cloned()
    }

    /// Drive the book the way the cockpit does: a sync on every change,
    /// applying any restored text to the composer and syncing again.
    fn drive(book: &mut DraftBook<MemStore>, root: &str, composer: &mut String) {
        if let Some(restored) = book.sync(root, composer) {
            *composer = restored;
            assert_eq!(book.sync(root, composer), None, "restore must settle");
        }
    }

    #[test]
    fn storage_key_is_prefixed_and_empty_root_has_none() {
        assert_eq!(storage_key("abc").as_deref(), Some("strike.draft.abc"));
        assert_eq!(storage_key(""), None);
    }

    #[test]
    fn switching_isolates_and_restores_drafts() {
        let mut book = DraftBook::new(MemStore::default());
        let mut composer = String::new();
        drive(&mut book, "a", &mut composer);
        composer = "half-written for A".into();
        drive(&mut book, "a", &mut composer);

        drive(&mut book, "b", &mut composer);
        assert_eq!(composer, "", "B must not inherit A's draft");
        composer = "for B".into();
        drive(&mut book, "b", &mut composer);

        drive(&mut book, "a", &mut composer);
        assert_eq!(composer, "half-written for A");
        drive(&mut book, "b", &mut composer);
        assert_eq!(composer, "for B");
    }

    #[test]
    fn drafts_survive_a_reload() {
        let mut book = DraftBook::new(MemStore::default());
        let mut composer = String::new();
        drive(&mut book, "a", &mut composer);
        composer = "keep me".into();
        drive(&mut book, "a", &mut composer);
        assert_eq!(stored(&book, "a").as_deref(), Some("keep me"));

        // A new page load: fresh book over the same storage, empty
        // composer, the restored session becomes active.
        let DraftBook { store, .. } = book;
        let mut reloaded = DraftBook::new(store);
        let mut composer = String::new();
        drive(&mut reloaded, "", &mut composer);
        assert_eq!(composer, "");
        drive(&mut reloaded, "a", &mut composer);
        assert_eq!(composer, "keep me");
    }

    #[test]
    fn sending_clears_only_the_active_sessions_draft() {
        let mut book = DraftBook::new(MemStore::default());
        let mut composer = String::new();
        drive(&mut book, "b", &mut composer);
        composer = "B waits".into();
        drive(&mut book, "b", &mut composer);
        drive(&mut book, "a", &mut composer);
        composer = "send me".into();
        drive(&mut book, "a", &mut composer);

        // Send clears the composer.
        composer.clear();
        drive(&mut book, "a", &mut composer);
        assert_eq!(stored(&book, "a"), None);
        assert_eq!(stored(&book, "b").as_deref(), Some("B waits"));
        drive(&mut book, "b", &mut composer);
        assert_eq!(composer, "B waits");
    }

    #[test]
    fn deleting_a_session_drops_its_draft() {
        let mut book = DraftBook::new(MemStore::default());
        let mut composer = String::new();
        drive(&mut book, "a", &mut composer);
        composer = "doomed".into();
        drive(&mut book, "a", &mut composer);
        drive(&mut book, "b", &mut composer);
        book.forget("a");
        assert_eq!(stored(&book, "a"), None);
        drive(&mut book, "a", &mut composer);
        assert_eq!(composer, "");
    }

    #[test]
    fn deleting_the_open_session_does_not_write_its_draft_back() {
        let mut book = DraftBook::new(MemStore::default());
        let mut composer = String::new();
        drive(&mut book, "a", &mut composer);
        composer = "doomed".into();
        drive(&mut book, "a", &mut composer);
        // The cockpit forgets the deleted session, then falls back to
        // no session while the composer still shows its text.
        book.forget("a");
        drive(&mut book, "", &mut composer);
        assert_eq!(composer, "", "deleted session's draft leaves the composer");
        assert_eq!(stored(&book, "a"), None);
        assert_eq!(book.recall("a"), "");
    }

    #[test]
    fn storage_unavailable_falls_back_to_memory() {
        let store = MemStore {
            refuse_writes: true,
            ..MemStore::default()
        };
        let mut book = DraftBook::new(store);
        let mut composer = String::new();
        drive(&mut book, "a", &mut composer);
        composer = "only in memory".into();
        drive(&mut book, "a", &mut composer);
        assert_eq!(stored(&book, "a"), None);
        drive(&mut book, "b", &mut composer);
        assert_eq!(composer, "");
        drive(&mut book, "a", &mut composer);
        assert_eq!(composer, "only in memory", "switch isolation holds without storage");
    }

    #[test]
    fn refused_write_removes_an_older_stored_version() {
        let mut book = DraftBook::new(MemStore::default());
        let mut composer = String::new();
        drive(&mut book, "a", &mut composer);
        composer = "old".into();
        drive(&mut book, "a", &mut composer);
        book.store.refuse_writes = true;
        composer = "newer".into();
        drive(&mut book, "a", &mut composer);
        assert_eq!(stored(&book, "a"), None, "a reload must not restore the stale text");
    }

    #[test]
    fn oversized_drafts_stay_in_memory_only() {
        let mut book = DraftBook::new(MemStore::default());
        let mut composer = String::new();
        drive(&mut book, "a", &mut composer);
        composer = "x".repeat(MAX_PERSIST_CHARS);
        drive(&mut book, "a", &mut composer);
        assert!(stored(&book, "a").is_some(), "at the cap still persists");
        composer.push('y');
        drive(&mut book, "a", &mut composer);
        assert_eq!(stored(&book, "a"), None);
        let big = composer.clone();
        drive(&mut book, "b", &mut composer);
        drive(&mut book, "a", &mut composer);
        assert_eq!(composer, big);
    }

    #[test]
    fn unchanged_text_does_not_rewrite_storage() {
        let mut book = DraftBook::new(MemStore::default());
        let mut composer = String::new();
        drive(&mut book, "a", &mut composer);
        composer = "same".into();
        drive(&mut book, "a", &mut composer);
        let writes = book.store().writes;
        drive(&mut book, "a", &mut composer);
        assert_eq!(book.store().writes, writes);
    }

    #[test]
    fn no_session_draft_is_memory_only() {
        let mut book = DraftBook::new(MemStore::default());
        let mut composer = String::new();
        drive(&mut book, "", &mut composer);
        composer = "before any session".into();
        drive(&mut book, "", &mut composer);
        assert!(book.store().items.is_empty());
        drive(&mut book, "a", &mut composer);
        assert_eq!(composer, "");
        assert_eq!(book.owner(), Some("a"));
    }
}
