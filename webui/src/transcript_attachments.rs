//! Sent attachments in the transcript (#1389).
//!
//! `user.message` carries the attachments that went with the turn
//! (`data.images`, refs only once stored). This module parses them into
//! chips under the "you" bubble. Raster images get a small thumbnail
//! fetched from `GET /v1/attachments/{ref}` with the daemon token; other
//! kinds get a name + kind chip with a Download action. A ref that does
//! not parse, or a fetch that fails, renders an honest "unavailable"
//! state instead of a broken image.

use std::cell::RefCell;
use std::collections::HashMap;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde_json::Value;
use wasm_bindgen::JsCast;

const REF_PREFIX: &str = "att:sha256:";

/// Daemon base URL + token for attachment fetches. Provided once by the
/// app root; a missing context renders chips without thumbnails.
#[derive(Clone, Copy)]
pub struct AttachmentAuth {
    pub base: RwSignal<String>,
    pub token: RwSignal<String>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SentAttachment {
    pub name: String,
    pub kind: String,
    pub mime: String,
    pub reference: String,
    pub bytes: i64,
}

fn str_field(v: &Value, key: &str) -> String {
    v.get(key)
        .and_then(|x| x.as_str())
        .unwrap_or("")
        .trim()
        .to_string()
}

/// Attachments from a `user.message` payload. Entries with neither a ref
/// nor a name are dropped (nothing to show or fetch). Inline `data` is
/// never read: the transcript shows refs only.
pub fn parse_images(data: &Value) -> Vec<SentAttachment> {
    let Some(rows) = data.get("images").and_then(|v| v.as_array()) else {
        return Vec::new();
    };
    rows.iter()
        .filter_map(|row| {
            let item = SentAttachment {
                name: str_field(row, "name"),
                kind: str_field(row, "kind"),
                mime: str_field(row, "mime"),
                reference: str_field(row, "ref"),
                bytes: row.get("bytes").and_then(|b| b.as_i64()).unwrap_or(0),
            };
            (!item.reference.is_empty() || !item.name.is_empty()).then_some(item)
        })
        .collect()
}

/// Daemon path for a stored ref, or `None` when the ref is not a valid
/// `att:sha256:<64 lowercase hex>` (the chip then shows unavailable).
pub fn attachment_path(reference: &str) -> Option<String> {
    let hex = reference.trim().strip_prefix(REF_PREFIX)?;
    if hex.len() != 64 || !hex.bytes().all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c)) {
        return None;
    }
    Some(format!("/v1/attachments/att%3Asha256%3A{hex}"))
}

/// Raster images the browser can thumbnail safely. SVG is deliberately
/// excluded: it is offered as a download, never rendered inline.
pub fn is_thumbnail(mime: &str) -> bool {
    matches!(
        mime.trim().to_ascii_lowercase().as_str(),
        "image/png" | "image/jpeg" | "image/jpg" | "image/webp" | "image/gif"
    )
}

/// Visible chip label: the stored name, else a generic noun.
pub fn chip_name(item: &SentAttachment) -> String {
    if item.name.is_empty() {
        "attachment".into()
    } else {
        item.name.clone()
    }
}

/// Short kind tag: the stored kind, else the MIME subtype, else "file".
pub fn kind_label(item: &SentAttachment) -> String {
    if !item.kind.is_empty() {
        return item.kind.to_ascii_lowercase();
    }
    match item.mime.split_once('/') {
        Some((_, sub)) if !sub.trim().is_empty() => sub.trim().to_ascii_lowercase(),
        _ => "file".into(),
    }
}

fn chip_class(missing: bool) -> &'static str {
    if missing {
        "sx-attach-chip sx-attach-missing"
    } else {
        "sx-attach-chip"
    }
}

#[derive(Clone, Debug, PartialEq)]
enum Thumb {
    Loading,
    Ready(String),
    Unavailable,
}

thread_local! {
    // Refs are content-addressed and immutable, so one object URL per ref
    // serves every re-render of the transcript for the page's lifetime.
    static THUMBS: RefCell<HashMap<String, Thumb>> = RefCell::new(HashMap::new());
}

async fn fetch_blob(base: &str, token: &str, path: &str, mime: &str) -> Result<web_sys::Blob, String> {
    let mut req = gloo_net::http::Request::get(&crate::api::api(base, path))
        .credentials(web_sys::RequestCredentials::Include);
    let token = token.trim();
    if !token.is_empty() {
        req = req.header("Authorization", &crate::api::auth_header(token));
    }
    let resp = req.send().await.map_err(|e| e.to_string())?;
    if !(200..300).contains(&resp.status()) {
        return Err(format!("HTTP {}", resp.status()));
    }
    let bytes = resp.binary().await.map_err(|e| e.to_string())?;
    let parts = js_sys::Array::new();
    parts.push(&js_sys::Uint8Array::from(bytes.as_slice()));
    let bag = web_sys::BlobPropertyBag::new();
    bag.set_type(mime);
    web_sys::Blob::new_with_u8_array_sequence_and_options(&parts, &bag).map_err(|_| "blob failed".to_string())
}

fn save_blob(blob: &web_sys::Blob, filename: &str) {
    let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
        return;
    };
    let Ok(url) = web_sys::Url::create_object_url_with_blob(blob) else {
        return;
    };
    if let Ok(a) = doc.create_element("a").map(|el| el.unchecked_into::<web_sys::HtmlAnchorElement>()) {
        a.set_href(&url);
        a.set_download(filename);
        if let Some(body) = doc.body() {
            let _ = body.append_child(&a);
            a.click();
            a.remove();
        }
    }
    let _ = web_sys::Url::revoke_object_url(&url);
}

#[component]
pub fn SentAttachments(items: Vec<SentAttachment>) -> impl IntoView {
    let auth = use_context::<AttachmentAuth>();
    view! {
        <div class="sx-attachments" data-sent-attachments="true">
            {items.into_iter().map(|item| sent_chip(item, auth)).collect_view()}
        </div>
    }
}

fn sent_chip(item: SentAttachment, auth: Option<AttachmentAuth>) -> impl IntoView {
    let name = chip_name(&item);
    let kind = kind_label(&item);
    let path = attachment_path(&item.reference);
    let thumb_wanted = is_thumbnail(&item.mime);
    let mime = item.mime.clone();
    let initial = match (&path, auth) {
        (None, _) | (_, None) => Thumb::Unavailable,
        (Some(_), Some(_)) if !thumb_wanted => Thumb::Loading,
        (Some(_), Some(_)) => THUMBS.with(|t| t.borrow().get(&item.reference).cloned()).unwrap_or(Thumb::Loading),
    };
    let thumb = RwSignal::new(initial.clone());
    if thumb_wanted && initial == Thumb::Loading {
        if let (Some(path), Some(auth)) = (path.clone(), auth) {
            let reference = item.reference.clone();
            let mime = mime.clone();
            let (b, t) = (auth.base.get_untracked(), auth.token.get_untracked());
            spawn_local(async move {
                let next = match fetch_blob(&b, &t, &path, &mime).await {
                    Ok(blob) => web_sys::Url::create_object_url_with_blob(&blob)
                        .map(Thumb::Ready)
                        .unwrap_or(Thumb::Unavailable),
                    Err(_) => Thumb::Unavailable,
                };
                THUMBS.with(|m| m.borrow_mut().insert(reference, next.clone()));
                let _ = thumb.try_set(next);
            });
        }
    }
    let unavailable = move || thumb.get() == Thumb::Unavailable;
    let download_note = RwSignal::new(String::new());
    let can_download = path.is_some() && auth.is_some();
    let dl_name = name.clone();
    let download = move |_: web_sys::MouseEvent| {
        let (Some(path), Some(auth)) = (path.clone(), auth) else {
            return;
        };
        let (b, t) = (auth.base.get_untracked(), auth.token.get_untracked());
        let (mime, file) = (mime.clone(), dl_name.clone());
        download_note.set(String::new());
        spawn_local(async move {
            match fetch_blob(&b, &t, &path, &mime).await {
                Ok(blob) => save_blob(&blob, &file),
                Err(_) => {
                    let _ = download_note.try_set("Unavailable".to_string());
                }
            }
        });
    };
    view! {
        <span class=move || chip_class(unavailable()) data-attach-kind=kind.clone()>
            {move || match thumb.get() {
                Thumb::Ready(url) if thumb_wanted => view! { <img src=url alt="" /> }.into_any(),
                _ => view! { <span></span> }.into_any(),
            }}
            <span class="sx-attach-text">
                <span class="name" title=name.clone()>{name.clone()}</span>
                <span class="kind">{kind}</span>
                <Show when=move || unavailable() && thumb_wanted>
                    <span class="sx-attach-state">"Unavailable"</span>
                </Show>
                <Show when=move || !download_note.get().is_empty()>
                    <span class="sx-attach-state">{move || download_note.get()}</span>
                </Show>
            </span>
            {can_download.then(|| view! {
                <button type="button" class="sx-quiet" aria-label=format!("Download {name}") on:click=download>"Download"</button>
            })}
            {(!can_download && !thumb_wanted).then(|| view! { <span class="sx-attach-state">"Unavailable"</span> })}
        </span>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const HEX: &str = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

    #[test]
    fn parse_images_reads_refs_and_skips_empty() {
        let data = serde_json::json!({
            "text": "see",
            "images": [
                {"ref": format!("att:sha256:{HEX}"), "mime": "image/png", "kind": "image", "name": "a.png", "bytes": 12, "data": "AAAA"},
                {"mime": "application/pdf", "name": "spec.pdf"},
                {"mime": "image/png"},
            ]
        });
        let got = parse_images(&data);
        assert_eq!(got.len(), 2, "{got:?}");
        assert_eq!(got[0].reference, format!("att:sha256:{HEX}"));
        assert_eq!(got[0].bytes, 12);
        assert_eq!(got[1].name, "spec.pdf");
        assert!(got[1].reference.is_empty());
        assert!(parse_images(&serde_json::json!({"text": "x"})).is_empty());
        assert!(parse_images(&serde_json::json!({"images": "nope"})).is_empty());
    }

    #[test]
    fn attachment_path_validates_ref() {
        let cases = [
            (format!("att:sha256:{HEX}"), Some(format!("/v1/attachments/att%3Asha256%3A{HEX}"))),
            (format!(" att:sha256:{HEX} "), Some(format!("/v1/attachments/att%3Asha256%3A{HEX}"))),
            (format!("att:sha256:{}", HEX.to_uppercase()), None),
            ("att:sha256:abc".into(), None),
            (HEX.into(), None),
            ("../../etc/passwd".into(), None),
            (String::new(), None),
        ];
        for (input, want) in cases {
            assert_eq!(attachment_path(&input), want, "input {input:?}");
        }
    }

    #[test]
    fn thumbnail_only_for_raster_images() {
        for (mime, want) in [
            ("image/png", true),
            ("IMAGE/JPEG", true),
            ("image/webp", true),
            ("image/gif", true),
            ("image/svg+xml", false),
            ("application/pdf", false),
            ("", false),
        ] {
            assert_eq!(is_thumbnail(mime), want, "mime {mime:?}");
        }
    }

    #[test]
    fn labels_fall_back_honestly() {
        let named = SentAttachment { name: "a.pdf".into(), kind: "PDF".into(), ..Default::default() };
        assert_eq!(chip_name(&named), "a.pdf");
        assert_eq!(kind_label(&named), "pdf");
        let bare = SentAttachment { mime: "application/zip".into(), ..Default::default() };
        assert_eq!(chip_name(&bare), "attachment");
        assert_eq!(kind_label(&bare), "zip");
        assert_eq!(kind_label(&SentAttachment::default()), "file");
    }
}
