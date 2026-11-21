//! Workspace image and binary viewer helpers (#1393).
//!
//! The document viewer used to show "Binary file. Nothing to show." for
//! every non-text file. Images now render from `GET /v1/file/raw` and any
//! other binary offers its name, size, sha and a download. The bytes are
//! fetched with the page's normal auth (bearer header, plus the cookie
//! the browser sends anyway) and shown from a local object URL, so the
//! route never needs a query-string token and works for a remote gateway
//! base too. SVG is shown from a `data:` URL instead of a `blob:` URL: a
//! blob URL carries the cockpit's origin if someone opens it in a tab,
//! while a top-level `data:` navigation is blocked by browsers and an
//! `<img>` never runs SVG script either way.

use serde_json::Value;

/// Must match `rawImageTypes` in `internal/frontend/server/fileraw.go`.
const IMAGE_EXTS: &[&str] = &[
    "png", "jpg", "jpeg", "gif", "webp", "avif", "bmp", "ico", "svg",
];

fn ext_of(path: &str) -> String {
    let name = path.rsplit('/').next().unwrap_or(path);
    match name.rfind('.') {
        Some(i) if i > 0 => name[i + 1..].to_ascii_lowercase(),
        _ => String::new(),
    }
}

/// Whether the viewer renders `path` as an image.
pub fn is_image_path(path: &str) -> bool {
    let ext = ext_of(path);
    !ext.is_empty() && IMAGE_EXTS.contains(&ext.as_str())
}

pub fn is_svg_path(path: &str) -> bool {
    ext_of(path) == "svg"
}

/// Base name of a workspace path.
pub fn file_name(path: &str) -> &str {
    let trimmed = path.trim_end_matches('/');
    trimmed.rsplit('/').next().unwrap_or(trimmed)
}

/// Bytes as a short human size (B, KB, MB, GB; base 1024).
pub fn human_size(bytes: i64) -> String {
    if bytes < 1024 {
        return format!("{} B", bytes.max(0));
    }
    let units = ["KB", "MB", "GB", "TB"];
    let mut value = bytes as f64 / 1024.0;
    let mut unit = 0;
    while value >= 1024.0 && unit + 1 < units.len() {
        value /= 1024.0;
        unit += 1;
    }
    format!("{value:.1} {}", units[unit])
}

/// One metadata line for a binary file: name · size · short sha.
pub fn binary_meta(path: &str, size: i64, sha: &str) -> String {
    let mut parts = vec![file_name(path).to_string(), human_size(size)];
    let sha = sha.trim();
    if !sha.is_empty() {
        let short: String = sha.chars().take(12).collect();
        parts.push(format!("sha256 {short}"));
    }
    parts.join(" · ")
}

/// Sentence for a failed raw fetch, from the status and the JSON error
/// body the route returns (`{"error": …, "code": …}`).
pub fn raw_error_message(status: u16, body: &str) -> String {
    if status == 401 {
        return "unauthorized: check the daemon token".into();
    }
    let parsed: Option<Value> = serde_json::from_str(body).ok();
    let field = |k: &str| {
        parsed
            .as_ref()
            .and_then(|v| v.get(k))
            .and_then(Value::as_str)
            .unwrap_or("")
            .trim()
            .to_string()
    };
    if status == 413 || field("code") == "too_large" {
        return "This file is too large to view here (over 1 MiB).".into();
    }
    let err = field("error");
    if err.is_empty() {
        format!("Could not load the file (HTTP {status}).")
    } else {
        format!("Could not load the file: {err}")
    }
}

const B64: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

/// Standard padded base64.
pub fn base64(bytes: &[u8]) -> String {
    let mut out = String::with_capacity(bytes.len().div_ceil(3) * 4);
    for chunk in bytes.chunks(3) {
        let b = [chunk[0], *chunk.get(1).unwrap_or(&0), *chunk.get(2).unwrap_or(&0)];
        let n = (u32::from(b[0]) << 16) | (u32::from(b[1]) << 8) | u32::from(b[2]);
        out.push(B64[(n >> 18) as usize & 63] as char);
        out.push(B64[(n >> 12) as usize & 63] as char);
        out.push(if chunk.len() > 1 { B64[(n >> 6) as usize & 63] as char } else { '=' });
        out.push(if chunk.len() > 2 { B64[n as usize & 63] as char } else { '=' });
    }
    out
}

/// `data:` URL for an SVG image.
pub fn svg_data_url(bytes: &[u8]) -> String {
    format!("data:image/svg+xml;base64,{}", base64(bytes))
}

/// Object URL for `bytes` typed `mime`. `None` outside a browser.
pub fn blob_url(bytes: &[u8], mime: &str) -> Option<String> {
    let blob = bytes_blob(bytes, mime)?;
    web_sys::Url::create_object_url_with_blob(&blob).ok()
}

pub fn revoke_url(url: &str) {
    if url.starts_with("blob:") {
        let _ = web_sys::Url::revoke_object_url(url);
    }
}

fn bytes_blob(bytes: &[u8], mime: &str) -> Option<web_sys::Blob> {
    let arr = js_sys::Uint8Array::from(bytes);
    let parts = js_sys::Array::new();
    parts.push(&arr);
    let bag = web_sys::BlobPropertyBag::new();
    bag.set_type(mime);
    web_sys::Blob::new_with_u8_array_sequence_and_options(&parts, &bag).ok()
}

/// Save `bytes` as a download named `filename`.
pub fn save_bytes(filename: &str, mime: &str, bytes: &[u8]) -> bool {
    use wasm_bindgen::JsCast;
    let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
        return false;
    };
    let Some(url) = blob_url(bytes, mime) else {
        return false;
    };
    let Some(a) = doc
        .create_element("a")
        .ok()
        .and_then(|el| el.dyn_into::<web_sys::HtmlAnchorElement>().ok())
    else {
        revoke_url(&url);
        return false;
    };
    a.set_href(&url);
    a.set_download(filename);
    if let Some(body) = doc.body() {
        let _ = body.append_child(&a);
        a.click();
        a.remove();
    }
    revoke_url(&url);
    true
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn image_paths_follow_the_server_allowlist() {
        for p in [
            "a.png", "shots/B.PNG", "x.jpg", "x.jpeg", "x.gif", "x.webp", "x.avif", "x.bmp",
            "favicon.ico", "d/logo.svg",
        ] {
            assert!(is_image_path(p), "{p}");
        }
        for p in ["a.bin", "png", ".png", "dir.png/file", "a.pdf", "a.html", ""] {
            assert!(!is_image_path(p), "{p}");
        }
        assert!(is_svg_path("x/Logo.SVG"));
        assert!(!is_svg_path("x.png"));
    }

    #[test]
    fn human_size_and_meta() {
        let cases = [
            (0, "0 B"),
            (1023, "1023 B"),
            (1024, "1.0 KB"),
            (1536, "1.5 KB"),
            (1 << 20, "1.0 MB"),
            (5 * (1i64 << 30), "5.0 GB"),
        ];
        for (n, want) in cases {
            assert_eq!(human_size(n), want, "{n}");
        }
        assert_eq!(
            binary_meta("build/app.bin", 2048, "0123456789abcdef0123"),
            "app.bin · 2.0 KB · sha256 0123456789ab"
        );
        assert_eq!(binary_meta("a.bin", 3, ""), "a.bin · 3 B");
        assert_eq!(file_name("a/b/c.png"), "c.png");
        assert_eq!(file_name("c.png"), "c.png");
    }

    #[test]
    fn raw_error_messages() {
        assert!(raw_error_message(401, "").contains("unauthorized"));
        assert!(raw_error_message(413, "").contains("too large"));
        assert!(raw_error_message(400, r#"{"error":"x","code":"too_large"}"#).contains("too large"));
        assert_eq!(
            raw_error_message(404, r#"{"error":"file not found: a.png","code":"read_failed"}"#),
            "Could not load the file: file not found: a.png"
        );
        assert_eq!(raw_error_message(500, "oops"), "Could not load the file (HTTP 500).");
    }

    #[test]
    fn base64_matches_rfc4648_vectors() {
        let cases: [(&[u8], &str); 7] = [
            (b"", ""),
            (b"f", "Zg=="),
            (b"fo", "Zm8="),
            (b"foo", "Zm9v"),
            (b"foob", "Zm9vYg=="),
            (b"fooba", "Zm9vYmE="),
            (b"foobar", "Zm9vYmFy"),
        ];
        for (input, want) in cases {
            assert_eq!(base64(input), want);
        }
        assert_eq!(base64(&[0xff, 0xfe]), "//4=");
        assert_eq!(svg_data_url(b"<svg/>"), "data:image/svg+xml;base64,PHN2Zy8+");
    }
}
