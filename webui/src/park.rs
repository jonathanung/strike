//! One-shot palette actions for a page that is not mounted yet.
//!
//! A command can `set_view` and then [`dispatch_or_park`]. If the page is
//! already listening, [`crate::shell::dispatch`] runs it. Otherwise the
//! args wait until that page's setup calls [`take`].

use std::cell::RefCell;
use std::collections::HashMap;

thread_local! {
    static PARKED: RefCell<HashMap<String, String>> = RefCell::new(HashMap::new());
}

pub fn put(id: &str, args: &str) {
    PARKED.with(|park| {
        park.borrow_mut().insert(id.to_string(), args.to_string());
    });
}

pub fn take(id: &str) -> Option<String> {
    PARKED.with(|park| park.borrow_mut().remove(id))
}

/// Run `id` when a page is listening. Otherwise remember `args`.
pub fn dispatch_or_park(id: &str, args: &str) -> Result<(), String> {
    match crate::shell::dispatch(id, args) {
        Ok(()) => Ok(()),
        Err(_) => {
            put(id, args);
            Ok(())
        }
    }
}
