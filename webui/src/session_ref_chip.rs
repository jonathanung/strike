//! `@session:<id>` chips in the composer (#1550 slice 2, #1906).
//!
//! The daemon expands the token on send (summary + last turns, quoted
//! and attributed) and sizes it at `GET /v1/sessions/{id}/reference`.
//! This module only shows that size before send, so the user sees the
//! cost. It never fetches or expands a transcript itself.

use std::collections::HashMap;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;

use crate::api::{self, get_json};
use session_ref::Size;

/// Body of `GET /v1/sessions/{id}/reference`.
#[derive(Clone, Debug, Deserialize)]
pub struct RefSizeResponse {
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub chars: usize,
    #[serde(default)]
    pub tokens: usize,
    #[serde(default, rename = "self")]
    pub self_ref: bool,
}

impl RefSizeResponse {
    fn size(self) -> Size {
        Size::Ready {
            title: self.title,
            chars: self.chars,
            tokens: self.tokens,
            own: self.self_ref,
        }
    }
}

/// Session chips for every `@session:` token in the draft. Sizes are
/// cached per (root, id) so typing does not refetch.
#[component]
pub fn SessionRefChips(
    draft: RwSignal<String>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    on_open: Callback<String>,
) -> impl IntoView {
    let sizes = RwSignal::new(HashMap::<String, Size>::new());
    let ids = Memo::new(move |_| session_ref::tokens(&draft.get()));

    Effect::new(move |_| {
        let want = ids.get();
        let root_now = root.get();
        for id in want {
            if id == root_now {
                continue;
            }
            let key = format!("{root_now}\u{1}{id}");
            if sizes.with_untracked(|m| m.contains_key(&key)) {
                continue;
            }
            sizes.update(|m| {
                m.insert(key.clone(), Size::Loading);
            });
            let (b, t) = (base.get_untracked(), token.get_untracked());
            let path = format!(
                "/v1/sessions/{}/reference?root={}",
                api::js_encode(&id),
                api::js_encode(&root_now)
            );
            spawn_local(async move {
                let next = match get_json::<RefSizeResponse>(&b, &t, &path).await {
                    Ok(r) => r.size(),
                    Err(e) => Size::from_error(&e),
                };
                sizes.update(|m| {
                    m.insert(key, next);
                });
            });
        }
    });

    view! {
        <Show when=move || !ids.get().is_empty()>
            <div class="attach-row session-ref-row" data-session-ref-chips="true">
                {move || {
                    let root_now = root.get();
                    let map = sizes.get();
                    // The route may not know a title the session list does.
                    let (listed_titles, _) = crate::composer_triggers::session_catalog();
                    ids.get().into_iter().map(|id| {
                        let state = if id == root_now {
                            Size::own()
                        } else {
                            map.get(&format!("{root_now}\u{1}{id}"))
                                .cloned()
                                .unwrap_or(Size::Loading)
                        };
                        let listed = listed_titles
                            .iter()
                            .find(|(sid, _)| *sid == id)
                            .map(|(_, t)| t.as_str())
                            .unwrap_or("");
                        let name = state.name(&id, listed);
                        let detail = state.detail();
                        let kind = state.state();
                        let openable = state.openable();
                        let open_id = id.clone();
                        view! {
                            <span class="attach-chip session-ref-chip" data-session-ref=id.clone() data-session-ref-state=kind>
                                <span>
                                    <span class="name">"@" {name}</span>
                                    <span class="path">{detail}</span>
                                </span>
                                {openable.then(|| view! {
                                    <button type="button" aria-label="Open the referenced session" data-session-ref-open="true" on:click=move |_| on_open.run(open_id.clone())>"Open"</button>
                                })}
                            </span>
                        }
                    }).collect_view()
                }}
            </div>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn response_maps_to_size() {
        let r: RefSizeResponse = serde_json::from_str(
            r#"{"id":"x","title":"T","chars":812,"tokens":203,"turns":3,"self":false}"#,
        )
        .unwrap();
        assert_eq!(r.clone().size().detail(), "812 chars · ~203 tokens");
        let own: RefSizeResponse =
            serde_json::from_str(r#"{"id":"x","chars":0,"tokens":0,"self":true}"#).unwrap();
        assert_eq!(own.size().state(), "self");
    }
}
