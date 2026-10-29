//! Browser storage for palette recents and use counts.
//! Missing storage is an empty list.

use std::collections::BTreeMap;

use super::rank::push_recent;
use super::registry::PaletteUsage;

const RECENTS_KEY: &str = "strike.paletteRecents";
const FREQ_KEY: &str = "strike.paletteFreq";
const RECENT_CAP: usize = 8;
const FREQUENT_CAP: usize = 8;

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

pub fn load_usage() -> PaletteUsage {
    let recents = serde_json::from_str(&load(RECENTS_KEY)).unwrap_or_default();
    let counts: BTreeMap<String, u32> = serde_json::from_str(&load(FREQ_KEY)).unwrap_or_default();
    PaletteUsage {
        recents,
        frequent: rank_counts(&counts, FREQUENT_CAP),
    }
}

pub fn remember_command(id: &str) {
    if id.is_empty() {
        return;
    }
    let mut list = serde_json::from_str(&load(RECENTS_KEY)).unwrap_or_default();
    push_recent(&mut list, id, RECENT_CAP);
    save(
        RECENTS_KEY,
        &serde_json::to_string(&list).unwrap_or_else(|_| "[]".into()),
    );
    let mut counts: BTreeMap<String, u32> =
        serde_json::from_str(&load(FREQ_KEY)).unwrap_or_default();
    let entry = counts.entry(id.to_string()).or_insert(0);
    *entry = entry.saturating_add(1);
    save(
        FREQ_KEY,
        &serde_json::to_string(&counts).unwrap_or_else(|_| "{}".into()),
    );
}

/// Highest count first. Ties break by id so the order is stable.
pub fn rank_counts(counts: &BTreeMap<String, u32>, cap: usize) -> Vec<String> {
    let mut rows: Vec<(&str, u32)> = counts
        .iter()
        .filter(|(_, count)| **count > 0)
        .map(|(id, count)| (id.as_str(), *count))
        .collect();
    rows.sort_by(|a, b| b.1.cmp(&a.1).then(a.0.cmp(b.0)));
    rows.into_iter()
        .take(cap)
        .map(|(id, _)| id.to_string())
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn counts_rank_by_use_then_id() {
        let mut counts = BTreeMap::new();
        counts.insert("git".into(), 2);
        counts.insert("chat".into(), 4);
        counts.insert("daemon".into(), 4);
        counts.insert("idle".into(), 0);
        assert_eq!(
            rank_counts(&counts, 3),
            vec!["chat".to_string(), "daemon".to_string(), "git".to_string()]
        );
    }
}
