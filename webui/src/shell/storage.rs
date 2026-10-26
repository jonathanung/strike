//! Browser storage for palette recents. Missing storage is an empty list.

use super::rank::push_recent;

const RECENTS_KEY: &str = "strike.paletteRecents";
const RECENT_CAP: usize = 8;

fn storage() -> Option<web_sys::Storage> {
    web_sys::window()?.local_storage().ok().flatten()
}

fn load(key: &str) -> String {
    storage()
        .and_then(|store| store.get_item(key).ok().flatten())
        .unwrap_or_default()
}

fn save(key: &str, value: &str) {
    if let Some(store) = storage() {
        let _ = store.set_item(key, value);
    }
}

pub fn load_recents() -> Vec<String> {
    serde_json::from_str(&load(RECENTS_KEY)).unwrap_or_default()
}

pub fn remember_command(id: &str) {
    let mut list = load_recents();
    push_recent(&mut list, id, RECENT_CAP);
    save(
        RECENTS_KEY,
        &serde_json::to_string(&list).unwrap_or_else(|_| "[]".into()),
    );
}
