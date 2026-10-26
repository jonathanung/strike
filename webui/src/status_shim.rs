//! Stand-in for the shared status badge (#1263).
//! Call sites pass `kind` and `label`. Swap this component for the status
//! stream's badge when that module lands; do not grow a second status system here.

use leptos::prelude::*;

#[component]
pub fn Badge(kind: String, label: String) -> impl IntoView {
    let class = format!("sx-status sx-status-{kind}");
    view! { <span class=class data-status=kind>{label}</span> }
}
