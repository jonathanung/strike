/// Fields read from `strike serve`'s stdout banner. The token is only present
/// when the process minted one. A `--token` / `--token-file` banner does not
/// print the secret, and this parser does not invent one.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ServeBanner {
    pub listen: String,
    pub token: Option<String>,
}

pub fn parse_banner(text: &str) -> Option<ServeBanner> {
    let mut listen = None;
    let mut token = None;
    for line in text.lines() {
        let line = line.trim();
        if let Some(rest) = line.strip_prefix("strike serve listening on ") {
            listen = Some(rest.trim().trim_end_matches('/').to_string());
        }
        if let Some(rest) = line.strip_prefix("token:") {
            let first = rest.split_whitespace().next().unwrap_or("");
            if !first.is_empty() && !first.starts_with('(') {
                token = Some(first.to_string());
            }
        }
    }
    listen.map(|listen| ServeBanner { listen, token })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_minted_token_and_ignores_hidden_token() {
        let minted = "\
strike serve listening on http://127.0.0.1:4321\n\
  health:  http://127.0.0.1:4321/health\n\
  cockpit: http://127.0.0.1:4321/attach?token=super-secret\n\
  token:   super-secret  (auto-minted; pass --token to set)\n";
        let got = parse_banner(minted).unwrap();
        assert_eq!(got.listen, "http://127.0.0.1:4321");
        assert_eq!(got.token.as_deref(), Some("super-secret"));

        let hidden = "\
strike serve listening on http://127.0.0.1:8787\n\
  token:   (from --token)\n";
        let got = parse_banner(hidden).unwrap();
        assert_eq!(got.listen, "http://127.0.0.1:8787");
        assert!(got.token.is_none());
    }

    #[test]
    fn empty_banner_is_none() {
        assert!(parse_banner("still starting\n").is_none());
    }
}
