//! Transcript message pins (#1528): the per-session pin list, loaded
//! from and written to the daemon (`/v1/sessions/{id}/message-pins`) so
//! pins follow the session across browsers and devices, plus the pin
//! rail above the transcript. Anchors and resolution rules live in the
//! `message-pins` crate.

use leptos::prelude::*;
use leptos::task::spawn_local;
use message_pins::{Anchor, Pin};
use serde_json::{json, Value};

use crate::api;
use crate::transcript::Transcript;

/// Pins for the viewed session, plus what is needed to change them.
#[derive(Clone, Copy)]
pub struct PinStore {
    pub pins: RwSignal<Vec<Pin>>,
    session: RwSignal<String>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    note: RwSignal<String>,
}

fn pins_path(session: &str) -> String {
    format!("/v1/sessions/{}/message-pins", api::js_encode(session))
}

/// Parse the daemon's `{"pins": [...]}` body. Malformed rows are skipped.
pub fn parse_pins(v: &Value) -> Vec<Pin> {
    v.get("pins")
        .and_then(|p| p.as_array())
        .map(|rows| {
            rows.iter()
                .filter_map(|row| {
                    let keep = row.get("keep")?.as_i64()?;
                    let role = row.get("role")?.as_str()?.to_string();
                    let seq = u32::try_from(row.get("seq")?.as_u64()?).ok()?;
                    let excerpt = row.get("excerpt")?.as_str()?.to_string();
                    let at_ms = row.get("atMs").and_then(|t| t.as_f64()).unwrap_or(0.0);
                    Some(Pin {
                        anchor: Anchor { keep, role, seq },
                        excerpt,
                        at_ms,
                    })
                })
                .collect()
        })
        .unwrap_or_default()
}

impl PinStore {
    /// Creates the store and reloads pins whenever the viewed session
    /// changes. A response for a session the user already left is
    /// dropped.
    pub fn new(
        base: RwSignal<String>,
        token: RwSignal<String>,
        session: RwSignal<String>,
        note: RwSignal<String>,
    ) -> Self {
        let store = Self {
            pins: RwSignal::new(Vec::new()),
            session,
            base,
            token,
            note,
        };
        Effect::new(move |_| {
            let id = session.get();
            store.pins.set(Vec::new());
            if id.is_empty() {
                return;
            }
            let (b, t) = (base.get_untracked(), token.get_untracked());
            spawn_local(async move {
                let got = api::get_json::<Value>(&b, &t, &pins_path(&id)).await;
                if session.get_untracked() != id {
                    return;
                }
                match got {
                    Ok(v) => store.pins.set(parse_pins(&v)),
                    // Older daemons have no pin route: no rail, no noise.
                    Err(_) => store.pins.set(Vec::new()),
                }
            });
        });
        store
    }

    pub fn is_pinned(&self, anchor: &Anchor) -> bool {
        self.pins.with(|p| message_pins::is_pinned(p, anchor))
    }

    /// Pin or unpin one message on the daemon; the list updates from the
    /// daemon's answer, never optimistically.
    pub fn toggle(&self, anchor: Anchor, excerpt: String, at_ms: f64) {
        let id = self.session.get_untracked();
        if id.is_empty() {
            return;
        }
        let pinned = self.is_pinned(&anchor);
        let (b, t) = (self.base.get_untracked(), self.token.get_untracked());
        let store = *self;
        spawn_local(async move {
            let path = pins_path(&id);
            if pinned {
                let q = format!(
                    "{path}?keep={}&role={}&seq={}",
                    anchor.keep,
                    api::js_encode(&anchor.role),
                    anchor.seq
                );
                match api::delete_json(&b, &t, &q, None).await {
                    Ok(()) => {
                        if store.session.get_untracked() == id {
                            store.pins.update(|p| p.retain(|x| x.anchor != anchor));
                        }
                    }
                    Err(e) => store.note.set(format!("Unpin failed: {e}")),
                }
            } else {
                let body = json!({
                    "keep": anchor.keep,
                    "role": anchor.role,
                    "seq": anchor.seq,
                    "excerpt": excerpt,
                    "atMs": at_ms,
                });
                match api::post_json_value(&b, &t, &path, body).await {
                    Ok(v) => {
                        if store.session.get_untracked() == id {
                            store.pins.set(parse_pins(&v));
                        }
                    }
                    Err(e) => store.note.set(format!("Pin failed: {e}")),
                }
            }
        });
    }
}

/// Compact strip of the open session's pinned messages. Renders nothing
/// at all until the first pin exists. A resolved pin jumps through the
/// palette's `transcript.jump` (which mounts a windowed-out row and puts
/// the cursor highlight on it); an unresolved one says so, with its
/// excerpt.
#[component]
pub fn PinRail(store: PinStore, transcript: RwSignal<Transcript>) -> impl IntoView {
    let items = Memo::new(move |_| {
        let pins = store.pins.get();
        if pins.is_empty() {
            return Vec::new();
        }
        transcript.with(|t| t.pin_rail(&pins))
    });
    view! {
        <Show when=move || items.with(|i| !i.is_empty())>
            <nav class="pin-rail" aria-label="Pinned messages">
                <span class="pin-rail-title">"Pinned"</span>
                <div class="pin-rail-items">
                    {move || {
                        items
                            .get()
                            .into_iter()
                            .map(|item| match item.target {
                                Some(id) => view! {
                                    <button
                                        type="button"
                                        class="pin-rail-item"
                                        data-pin-target=id.to_string()
                                        title="Jump to this pinned message"
                                        on:click=move |_| {
                                            let _ = crate::shell::dispatch("transcript.jump", &id.to_string());
                                        }
                                    >{item.label}</button>
                                }
                                .into_any(),
                                None => view! {
                                    <span class="pin-rail-item pin-rail-lost" data-pin-target="" title=message_pins::UNRESOLVED>
                                        {item.label}
                                        <span class="pin-rail-note">{message_pins::UNRESOLVED}</span>
                                    </span>
                                }
                                .into_any(),
                            })
                            .collect_view()
                    }}
                </div>
            </nav>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_pins_reads_the_daemon_body() {
        let v = json!({"pins": [
            {"keep": 4, "role": "you", "seq": 0, "excerpt": "why", "atMs": 17.0, "pinnedAt": "x"},
            {"keep": 9, "role": "strike", "seq": 1, "excerpt": "done"},
            {"keep": "bad", "role": "you", "seq": 0, "excerpt": "skipped"},
        ]});
        let pins = parse_pins(&v);
        assert_eq!(pins.len(), 2);
        assert_eq!(pins[0].anchor, Anchor { keep: 4, role: "you".into(), seq: 0 });
        assert_eq!(pins[0].at_ms, 17.0);
        assert_eq!(pins[1].anchor.seq, 1);
        assert!(parse_pins(&json!({})).is_empty());
    }
}
