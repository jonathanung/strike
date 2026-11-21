//! Browser binding for per-session composer drafts (#1367): the pure
//! [`composer_drafts::DraftBook`] over `localStorage`. Every failure is
//! silent — private mode or a full quota leaves the book's in-memory
//! copy authoritative and the composer working.

use composer_drafts::{DraftBook, DraftStore};

pub struct LocalDrafts;

fn storage() -> Option<web_sys::Storage> {
    web_sys::window()?.local_storage().ok().flatten()
}

impl DraftStore for LocalDrafts {
    fn get(&self, key: &str) -> Option<String> {
        storage()?.get_item(key).ok().flatten()
    }

    fn set(&mut self, key: &str, value: &str) -> bool {
        storage().is_some_and(|s| s.set_item(key, value).is_ok())
    }

    fn remove(&mut self, key: &str) {
        if let Some(s) = storage() {
            let _ = s.remove_item(key);
        }
    }
}

pub fn book() -> DraftBook<LocalDrafts> {
    DraftBook::new(LocalDrafts)
}
