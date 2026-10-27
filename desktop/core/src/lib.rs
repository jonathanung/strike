//! Desktop gateway manager.
//!
//! The Tauri window and the headless binary both use this crate. It stores
//! gateway records without tokens, keeps tokens in the OS keyring, and exposes
//! one loopback reverse proxy. The proxy is the only thing the webview talks
//! to. It injects the bearer on the way out and never opens a public port.

mod banner;
mod desktop;
mod error;
mod keystore;
mod link;
mod probe;
mod proxy;
mod sidecar;
mod sshfwd;
mod store;
mod tlsclient;

pub use banner::parse_banner;
pub use desktop::{Desktop, Notice};
pub use error::GatewayError;
pub use keystore::{KeyStore, MemoryKeyStore, OsKeyStore};
pub use link::{parse_link, ParsedLink};
pub use store::{Gateway, Mode};

/// Wire schema this shell understands. Copied from `pkg/protocol` `Version`.
/// `/health` today reports the product version, not this value. A mismatch is
/// raised only when the daemon adds `protocol` or `protocolVersion` (#1306).
pub const PROTOCOL_VERSION: &str = "1.18.0";

pub(crate) fn install_crypto() {
    let _ = rustls::crypto::ring::default_provider().install_default();
}

/// Drop a secret from a string we are about to log or return.
pub fn redact(text: &str, secret: &str) -> String {
    let mut out = if secret.is_empty() {
        text.to_string()
    } else {
        text.replace(secret, "[redacted]")
    };
    // A pasted URL can still carry token= after a partial failure.
    while let Some(start) = out.find("token=") {
        let value = start + "token=".len();
        let end = out[value..]
            .find(|c: char| c == '&' || c == ' ' || c == '"' || c == '\n' || c == '#')
            .map(|i| value + i)
            .unwrap_or(out.len());
        out.replace_range(value..end, "[redacted]");
        // replace_range leaves "token=[redacted]"; advance past it
        if !out[start..].contains("token=[redacted]") {
            break;
        }
        // one replacement per occurrence; the new text has no raw token
        // but find("token=") would match "token=[redacted]" and loop.
        break;
    }
    // Repeat for every token= that is not already redacted.
    let mut cursor = 0;
    let mut rebuilt = String::new();
    let bytes = out;
    while let Some(rel) = bytes[cursor..].find("token=") {
        let start = cursor + rel;
        rebuilt.push_str(&bytes[cursor..start]);
        rebuilt.push_str("token=");
        let value = start + "token=".len();
        if bytes[value..].starts_with("[redacted]") {
            rebuilt.push_str("[redacted]");
            cursor = value + "[redacted]".len();
            continue;
        }
        let end = bytes[value..]
            .find(|c: char| c == '&' || c == ' ' || c == '"' || c == '\n' || c == '#')
            .map(|i| value + i)
            .unwrap_or(bytes.len());
        rebuilt.push_str("[redacted]");
        cursor = end;
    }
    rebuilt.push_str(&bytes[cursor..]);
    rebuilt
}

pub(crate) fn constant_eq(a: &str, b: &str) -> bool {
    let a = a.as_bytes();
    let b = b.as_bytes();
    if a.len() != b.len() {
        return false;
    }
    let mut diff = 0u8;
    for (x, y) in a.iter().zip(b.iter()) {
        diff |= x ^ y;
    }
    diff == 0
}

#[cfg(test)]
mod redact_tests {
    use super::redact;

    #[test]
    fn redact_strips_secret_and_query_token() {
        let raw = "failed http://host/?token=s3cret&x=1 bearer s3cret";
        let got = redact(raw, "s3cret");
        assert!(!got.contains("s3cret"), "{got}");
        assert!(got.contains("[redacted]"), "{got}");
    }
}
