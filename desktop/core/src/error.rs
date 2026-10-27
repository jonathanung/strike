use serde_json::{json, Value};

use crate::PROTOCOL_VERSION;

#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum GatewayError {
    #[error("gateway unreachable")]
    Unreachable { detail: String },
    #[error("certificate is not trusted")]
    BadCert { fingerprint: String },
    #[error("certificate does not match the saved pin")]
    PinMismatch { fingerprint: String },
    #[error("gateway rejected the token")]
    BadToken,
    #[error("this gateway requires a token")]
    NeedsToken,
    #[error("protocol version mismatch")]
    VersionMismatch { got: String },
    #[error("cleartext HTTP to a non-loopback host is blocked")]
    CleartextRefused,
    #[error("pairing codes are not exchanged by the desktop shell yet")]
    PairingUnsupported,
    #[error("link is not a gateway URL")]
    InvalidLink { detail: String },
    #[error("ssh forward failed")]
    Ssh { detail: String },
    #[error("local daemon failed to start")]
    Sidecar { detail: String },
    #[error("gateway not found")]
    NotFound,
    #[error("keyring rejected the token")]
    Keyring { detail: String },
    #[error("could not save gateways")]
    Store { detail: String },
}

impl GatewayError {
    pub fn code(&self) -> &'static str {
        match self {
            Self::Unreachable { .. } => "unreachable",
            Self::BadCert { .. } => "bad_cert",
            Self::PinMismatch { .. } => "pin_mismatch",
            Self::BadToken => "bad_token",
            Self::NeedsToken => "needs_token",
            Self::VersionMismatch { .. } => "version_mismatch",
            Self::CleartextRefused => "cleartext_refused",
            Self::PairingUnsupported => "pairing_unsupported",
            Self::InvalidLink { .. } => "invalid_link",
            Self::Ssh { .. } => "ssh",
            Self::Sidecar { .. } => "sidecar",
            Self::NotFound => "not_found",
            Self::Keyring { .. } => "keyring",
            Self::Store { .. } => "store",
        }
    }

    pub fn status_label(&self) -> &'static str {
        match self {
            Self::Unreachable { .. } => "Unreachable",
            Self::BadCert { .. } => "Untrusted certificate",
            Self::PinMismatch { .. } => "Pin mismatch",
            Self::BadToken => "Bad token",
            Self::NeedsToken => "Needs token",
            Self::VersionMismatch { .. } => "Version mismatch",
            Self::CleartextRefused => "Cleartext blocked",
            Self::PairingUnsupported => "Pairing unavailable",
            Self::InvalidLink { .. } => "Invalid link",
            Self::Ssh { .. } => "SSH failed",
            Self::Sidecar { .. } => "Daemon failed",
            Self::NotFound => "Missing",
            Self::Keyring { .. } => "Keyring error",
            Self::Store { .. } => "Save failed",
        }
    }

    pub fn detail(&self) -> String {
        match self {
            Self::Unreachable { detail }
            | Self::InvalidLink { detail }
            | Self::Ssh { detail }
            | Self::Sidecar { detail }
            | Self::Keyring { detail }
            | Self::Store { detail } => detail.clone(),
            Self::BadCert { fingerprint } | Self::PinMismatch { fingerprint } => {
                fingerprint.clone()
            }
            Self::VersionMismatch { got } => {
                format!("daemon speaks {got}; this shell knows {PROTOCOL_VERSION}")
            }
            Self::BadToken => "the gateway returned 401 for this token".into(),
            Self::NeedsToken => "paste the gateway token. It is stored in the OS keyring.".into(),
            Self::CleartextRefused => {
                "check Allow cleartext only for a VPN, Tailscale, or an SSH forward".into()
            }
            Self::PairingUnsupported => {
                "paste the gateway URL and token. QR exchange waits for the pairing routes.".into()
            }
            Self::NotFound => "no saved gateway with that id".into(),
        }
    }

    pub fn fingerprint(&self) -> Option<&str> {
        match self {
            Self::BadCert { fingerprint } | Self::PinMismatch { fingerprint } => Some(fingerprint),
            _ => None,
        }
    }

    pub fn to_json(&self) -> Value {
        let mut body = json!({
            "ok": false,
            "error": self.code(),
            "detail": self.detail(),
            "label": self.status_label(),
        });
        if let Some(fp) = self.fingerprint() {
            body["fingerprint"] = json!(fp);
        }
        if let Self::VersionMismatch { got } = self {
            body["got"] = json!(got);
            body["expected"] = json!(PROTOCOL_VERSION);
        }
        body
    }
}
