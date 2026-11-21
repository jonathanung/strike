//! Send-time attachment honesty (#1561). Pure rules: no DOM.
//!
//! The composer warns only when the daemon is *known* to drop a file
//! before it reaches the model. These rules mirror the engine's own
//! attachment policy (`harness/engine/attachment_policy.go` and
//! `providerAcceptsImages` in `harness/engine/turn.go`), so the warning
//! states what the engine will actually do, not a guess:
//!
//! - The echo provider never receives attachments.
//! - Only PNG, JPEG, WebP and GIF images are sent to a provider. Other
//!   kinds (PDF, SVG, text, archives) are stored but never sent.
//!
//! Catalog `attachment == false` is NOT used: it means unknown or
//! unsupported (#1054), so treating it as unsupported would warn on
//! capable models. An unknown provider or MIME yields no warning.

/// Provider image MIME types the engine forwards (`providerImageMIME`).
const PROVIDER_IMAGE_MIME: [&str; 4] = ["image/png", "image/jpeg", "image/webp", "image/gif"];

/// Lowercase, drop parameters, and fold `image/jpg` into `image/jpeg`,
/// matching the engine's `normalizeAttachmentMIME`.
pub fn normalize_mime(mime: &str) -> String {
    let lower = mime.trim().to_ascii_lowercase();
    let base = match lower.find(';') {
        Some(i) => lower[..i].trim().to_string(),
        None => lower,
    };
    if base == "image/jpg" {
        "image/jpeg".into()
    } else {
        base
    }
}

/// True when the engine is known to send nothing from attachments to
/// this provider. Empty means the status has not loaded: unknown.
pub fn provider_drops_attachments(provider: &str) -> bool {
    provider.trim().eq_ignore_ascii_case("echo")
}

/// Why one attachment will not reach the model, or `None` when the
/// engine would forward it (or the outcome is not known client-side).
pub fn drop_reason(provider: &str, mime: &str) -> Option<String> {
    let provider = provider.trim();
    if provider_drops_attachments(provider) {
        return Some(format!(
            "The {provider} provider does not accept attachments. Only your text reaches the model."
        ));
    }
    let mime = normalize_mime(mime);
    if mime.is_empty() || PROVIDER_IMAGE_MIME.contains(&mime.as_str()) {
        return None;
    }
    Some(format!(
        "{mime} files are stored but not sent to the model. Only PNG, JPEG, WebP and GIF images are."
    ))
}

/// One composer warning line per distinct reason, each naming the files
/// it applies to. Empty when every file is expected to reach the model.
/// `files` is `(name, mime)` for attachments without a read error.
pub fn composer_warnings(provider: &str, files: &[(String, String)]) -> Vec<String> {
    let mut groups: Vec<(String, Vec<String>)> = Vec::new();
    for (name, mime) in files {
        let Some(reason) = drop_reason(provider, mime) else {
            continue;
        };
        match groups.iter_mut().find(|(r, _)| *r == reason) {
            Some((_, names)) => names.push(name.clone()),
            None => groups.push((reason, vec![name.clone()])),
        }
    }
    groups
        .into_iter()
        .map(|(reason, names)| format!("Will not reach the model: {}. {reason}", names.join(", ")))
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn files(rows: &[(&str, &str)]) -> Vec<(String, String)> {
        rows.iter().map(|(n, m)| (n.to_string(), m.to_string())).collect()
    }

    #[test]
    fn normalize_mime_matches_engine() {
        let cases = [
            ("image/PNG", "image/png"),
            (" image/jpg ", "image/jpeg"),
            ("image/jpeg; q=1", "image/jpeg"),
            ("", ""),
            ("application/pdf", "application/pdf"),
        ];
        for (input, want) in cases {
            assert_eq!(normalize_mime(input), want, "input {input:?}");
        }
    }

    #[test]
    fn drop_reason_cases() {
        let cases: [(&str, &str, bool); 9] = [
            ("echo", "image/png", true),
            ("ECHO", "image/png", true),
            (" echo ", "", true),
            ("anthropic", "image/png", false),
            ("anthropic", "image/jpg", false),
            ("openai", "application/pdf", true),
            ("openai", "image/svg+xml", true),
            // Unknown MIME or provider not loaded yet: no claim either way.
            ("openai", "", false),
            ("", "image/webp", false),
        ];
        for (provider, mime, dropped) in cases {
            assert_eq!(
                drop_reason(provider, mime).is_some(),
                dropped,
                "provider {provider:?} mime {mime:?}"
            );
        }
    }

    #[test]
    fn never_claims_unsupported_from_catalog_flag() {
        // A real provider with an image never warns, whatever the catalog
        // says: the catalog's false is unknown under #1054.
        assert!(drop_reason("xai", "image/gif").is_none());
    }

    #[test]
    fn composer_warnings_group_by_reason() {
        let rows = files(&[
            ("a.png", "image/png"),
            ("spec.pdf", "application/pdf"),
            ("b.pdf", "application/pdf"),
            ("d.svg", "image/svg+xml"),
        ]);
        let got = composer_warnings("anthropic", &rows);
        assert_eq!(got.len(), 2, "{got:?}");
        assert!(got[0].starts_with("Will not reach the model: spec.pdf, b.pdf."), "{got:?}");
        assert!(got[1].contains("d.svg"), "{got:?}");
        assert!(!got.iter().any(|w| w.contains("a.png")), "{got:?}");
    }

    #[test]
    fn composer_warnings_echo_names_every_file() {
        let rows = files(&[("a.png", "image/png"), ("b.pdf", "application/pdf")]);
        let got = composer_warnings("echo", &rows);
        assert_eq!(got.len(), 1, "{got:?}");
        assert!(got[0].contains("a.png, b.pdf"), "{got:?}");
        assert!(got[0].contains("echo provider does not accept attachments"), "{got:?}");
    }

    #[test]
    fn composer_warnings_empty_when_all_forwarded() {
        let rows = files(&[("a.png", "image/png"), ("b.webp", "image/webp")]);
        assert!(composer_warnings("anthropic", &rows).is_empty());
        assert!(composer_warnings("anthropic", &[]).is_empty());
    }
}
