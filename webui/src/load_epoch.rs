//! Signal adapter for [`load_guard::LoadEpoch`].
//!
//! Leptos `update` does not return the closure's value, so callers cannot
//! write `epoch.update(|e| e.begin())`. [`begin`] is that call.

use leptos::prelude::{RwSignal, Update};
use load_guard::{LoadEpoch, LoadToken};

/// Start a load and return its token. Pair this with clearing the stale
/// signals in the same turn, before the first await.
pub fn begin(epoch: RwSignal<LoadEpoch>) -> LoadToken {
    let mut token = LoadToken::default();
    epoch.update(|epoch| token = epoch.begin());
    token
}
