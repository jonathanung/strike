//! Signal wrapper around [`strike_load_guard`].
//!
//! Pure commit rules live in that crate so native tests can finish
//! responses out of order. Call sites bump with [`issue`] and commit
//! only through the crate's functions.

pub use strike_load_guard::*;

use leptos::prelude::*;

pub fn issue(epoch: RwSignal<LoadEpoch>) -> Token {
    let mut current = epoch.get_untracked();
    let token = current.begin();
    epoch.set(current);
    token
}
