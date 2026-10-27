use url::Url;

use crate::error::GatewayError;
use crate::store::Mode;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ParsedLink {
    pub name: String,
    pub mode: Mode,
    pub url: String,
    pub ssh_target: String,
    pub remote_port: u16,
    pub token: Option<String>,
}

/// Parse a pasted gateway link. Tokens are returned separately so the caller
/// can put them in the keyring. The saved URL never keeps them.
pub fn parse_link(raw: &str, fallback_name: &str) -> Result<ParsedLink, GatewayError> {
    let raw = raw.trim();
    if raw.is_empty() {
        return Err(GatewayError::InvalidLink {
            detail: "link is empty".into(),
        });
    }
    if raw.eq_ignore_ascii_case("sidecar") {
        return Ok(ParsedLink {
            name: name_or(fallback_name, "This computer"),
            mode: Mode::Sidecar,
            url: String::new(),
            ssh_target: String::new(),
            remote_port: 0,
            token: None,
        });
    }
    if raw.contains("://") {
        if let Some(rest) = strip_scheme(raw, "strike") {
            return parse_strike(rest, fallback_name);
        }
        if let Some(rest) = strip_scheme(raw, "ssh") {
            return parse_ssh_url(rest, fallback_name, None);
        }
        return parse_http(raw, fallback_name);
    }
    if let Some((target, port)) = split_ssh_shorthand(raw) {
        return ssh_parsed(&target, port, fallback_name, None, "");
    }
    Err(GatewayError::InvalidLink {
        detail: "use http(s)://, ssh://user@host, user@host, sidecar, or strike://connect".into(),
    })
}

fn parse_strike(rest: &str, fallback_name: &str) -> Result<ParsedLink, GatewayError> {
    // rest is "connect?..." or "pair?..." or "//connect?..."
    let rest = rest.trim_start_matches('/');
    let (kind, query) = rest.split_once('?').unwrap_or((rest, ""));
    let kind = kind.trim_end_matches('/');
    if kind.eq_ignore_ascii_case("pair") {
        return Err(GatewayError::PairingUnsupported);
    }
    if !kind.eq_ignore_ascii_case("connect") {
        return Err(GatewayError::InvalidLink {
            detail: "strike:// links use connect or pair".into(),
        });
    }
    let mut name = String::new();
    let mut url = String::new();
    let mut ssh = String::new();
    let mut port: u16 = 8787;
    let mut token = None;
    for (k, v) in form_pairs(query) {
        match k.as_str() {
            "name" => name = v,
            "url" => url = v,
            "ssh" => ssh = v,
            "port" => {
                port = v.parse().map_err(|_| GatewayError::InvalidLink {
                    detail: "ssh port is not a number".into(),
                })?;
            }
            "token" | "access_token" => {
                if !v.is_empty() {
                    token = Some(v);
                }
            }
            "code" => return Err(GatewayError::PairingUnsupported),
            _ => {}
        }
    }
    if !ssh.is_empty() {
        return ssh_parsed(&ssh, port, &name_or(&name, fallback_name), token, "");
    }
    if url.is_empty() {
        return Err(GatewayError::InvalidLink {
            detail: "strike://connect needs a url or an ssh host".into(),
        });
    }
    let mut parsed = parse_link(&url, &name_or(&name, fallback_name))?;
    if !name.is_empty() {
        parsed.name = name;
    }
    if parsed.token.is_none() {
        parsed.token = token;
    }
    Ok(parsed)
}

fn parse_http(raw: &str, fallback_name: &str) -> Result<ParsedLink, GatewayError> {
    let mut url = Url::parse(raw).map_err(|_| GatewayError::InvalidLink {
        detail: "URL did not parse".into(),
    })?;
    if url.scheme() != "http" && url.scheme() != "https" {
        return Err(GatewayError::InvalidLink {
            detail: "only http and https gateway URLs are accepted".into(),
        });
    }
    if !url.username().is_empty() || url.password().is_some() {
        return Err(GatewayError::InvalidLink {
            detail: "put the token in the token field, not in the URL userinfo".into(),
        });
    }
    let token = take_secret(&mut url);
    url.set_fragment(None);
    let host = url.host_str().unwrap_or("").to_string();
    if host.is_empty() {
        return Err(GatewayError::InvalidLink {
            detail: "URL has no host".into(),
        });
    }
    let name = name_or(fallback_name, &host);
    // Drop trailing slash on the origin path only. Keep a non-root path.
    let mut stored = url.to_string();
    if stored.ends_with('/') && url.path() == "/" {
        stored.pop();
    }
    Ok(ParsedLink {
        name,
        mode: Mode::Remote,
        url: stored,
        ssh_target: String::new(),
        remote_port: url
            .port()
            .unwrap_or(if url.scheme() == "https" { 443 } else { 80 }),
        token,
    })
}

fn parse_ssh_url(
    rest: &str,
    fallback_name: &str,
    token: Option<String>,
) -> Result<ParsedLink, GatewayError> {
    // rest: //user@host:8787 or user@host:8787
    let rest = rest.trim_start_matches('/');
    let (target, port) = split_ssh_shorthand(rest).ok_or_else(|| GatewayError::InvalidLink {
        detail: "ssh host is not usable".into(),
    })?;
    ssh_parsed(&target, port, fallback_name, token, "")
}

fn ssh_parsed(
    target: &str,
    port: u16,
    fallback_name: &str,
    token: Option<String>,
    explicit_name: &str,
) -> Result<ParsedLink, GatewayError> {
    let target = target.trim();
    if !valid_ssh_target(target) || !(1..=65535).contains(&port) {
        return Err(GatewayError::InvalidLink {
            detail: "ssh host must look like user@host with no shell characters".into(),
        });
    }
    let name = if !explicit_name.is_empty() {
        explicit_name.to_string()
    } else {
        name_or(fallback_name, target)
    };
    Ok(ParsedLink {
        name,
        mode: Mode::Ssh,
        url: String::new(),
        ssh_target: target.to_string(),
        remote_port: port,
        token,
    })
}

pub fn valid_ssh_target(target: &str) -> bool {
    if target.is_empty() || target.len() > 200 || target.starts_with('-') {
        return false;
    }
    if target.contains([
        ' ', '\'', '"', ';', '&', '|', '$', '`', '\\', '\n', '\r', '\t', '<', '>',
    ]) {
        return false;
    }
    let (user, host) = match target.split_once('@') {
        Some((user, host)) => (user, host),
        None => ("", target),
    };
    if user.contains('@') || host.is_empty() || host.starts_with('-') || host.contains('@') {
        return false;
    }
    host.chars()
        .all(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '-' | ':' | '[' | ']'))
        && (user.is_empty()
            || user
                .chars()
                .all(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '-' | '_')))
}

fn split_ssh_shorthand(raw: &str) -> Option<(String, u16)> {
    let raw = raw.trim();
    if raw.is_empty() || raw.contains("://") {
        return None;
    }
    // host:port where host may be user@host. A bare host has no colon,
    // or an IPv6 literal in brackets.
    if let Some(host) = raw.strip_prefix('[').and_then(|rest| rest.split_once(']')) {
        let (addr, tail) = host;
        let port = if let Some(p) = tail.strip_prefix(':') {
            p.parse().ok()?
        } else if tail.is_empty() {
            8787
        } else {
            return None;
        };
        return Some((format!("[{addr}]"), port));
    }
    if let Some((left, port)) = raw.rsplit_once(':') {
        if !port.is_empty() && port.chars().all(|c| c.is_ascii_digit()) && valid_ssh_target(left) {
            return Some((left.to_string(), port.parse().ok()?));
        }
    }
    if valid_ssh_target(raw) {
        return Some((raw.to_string(), 8787));
    }
    None
}

fn take_secret(url: &mut Url) -> Option<String> {
    let mut token = None;
    let pairs: Vec<(String, String)> = url
        .query_pairs()
        .map(|(k, v)| (k.into_owned(), v.into_owned()))
        .collect();
    if !pairs.is_empty() {
        let mut kept = Vec::new();
        for (k, v) in pairs {
            if k == "token" || k == "access_token" {
                if !v.is_empty() {
                    token = Some(v);
                }
            } else {
                kept.push((k, v));
            }
        }
        if kept.is_empty() {
            url.set_query(None);
        } else {
            let q: String = kept
                .into_iter()
                .map(|(k, v)| format!("{}={}", urlencoding_lite(&k), urlencoding_lite(&v)))
                .collect::<Vec<_>>()
                .join("&");
            url.set_query(Some(&q));
        }
    }
    if let Some(frag) = url.fragment().map(|s| s.to_string()) {
        for (k, v) in form_pairs(&frag) {
            if (k == "token" || k == "access_token") && !v.is_empty() {
                token = Some(v);
            }
        }
    }
    token
}

fn form_pairs(query: &str) -> Vec<(String, String)> {
    query
        .split('&')
        .filter(|p| !p.is_empty())
        .map(|pair| {
            let (k, v) = pair.split_once('=').unwrap_or((pair, ""));
            (
                percent_decode(k).replace('+', " "),
                percent_decode(v).replace('+', " "),
            )
        })
        .collect()
}

fn percent_decode(input: &str) -> String {
    let bytes = input.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            if let Ok(v) =
                u8::from_str_radix(std::str::from_utf8(&bytes[i + 1..i + 3]).unwrap_or(""), 16)
            {
                out.push(v);
                i += 3;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn urlencoding_lite(input: &str) -> String {
    let mut out = String::new();
    for b in input.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(b as char)
            }
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

fn strip_scheme<'a>(raw: &'a str, scheme: &str) -> Option<&'a str> {
    let prefix = format!("{scheme}://");
    raw.get(..prefix.len())
        .filter(|head| head.eq_ignore_ascii_case(&prefix))
        .map(|_| &raw[prefix.len()..])
}

fn name_or(preferred: &str, fallback: &str) -> String {
    let preferred = preferred.trim();
    if !preferred.is_empty() {
        clip(preferred)
    } else {
        clip(fallback)
    }
}

fn clip(s: &str) -> String {
    let mut out = String::new();
    for (i, ch) in s.chars().enumerate() {
        if i == 80 {
            break;
        }
        if ch.is_control() {
            continue;
        }
        out.push(ch);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn http_token_query_is_stripped() {
        let got = parse_link(
            "http://127.0.0.1:8787/?token=s3cret&x=1#token=other",
            "Desk",
        )
        .unwrap();
        assert_eq!(got.mode, Mode::Remote);
        assert!(!got.url.contains("s3cret"), "{}", got.url);
        assert!(!got.url.contains("token"), "{}", got.url);
        assert!(got.url.contains("x=1"), "{}", got.url);
        assert_eq!(got.token.as_deref(), Some("other"));
        assert_eq!(got.name, "Desk");
    }

    #[test]
    fn strike_connect_and_pair() {
        let got = parse_link(
            "strike://connect?name=Desk&url=https%3A%2F%2F100.64.0.5%3A8787&token=abc",
            "",
        )
        .unwrap();
        assert_eq!(got.name, "Desk");
        assert_eq!(got.url, "https://100.64.0.5:8787");
        assert_eq!(got.token.as_deref(), Some("abc"));
        assert!(matches!(
            parse_link("strike://pair?code=once", ""),
            Err(GatewayError::PairingUnsupported)
        ));
    }

    #[test]
    fn ssh_targets_reject_shell_metacharacters() {
        let got = parse_link("ssh://me@desk.example:2222", "Lab").unwrap();
        assert_eq!(got.mode, Mode::Ssh);
        assert_eq!(got.ssh_target, "me@desk.example");
        assert_eq!(got.remote_port, 2222);
        assert!(parse_link("me@host;rm -rf", "").is_err());
        assert!(parse_link("-oProxyCommand=curl", "").is_err());
        assert!(parse_link("sidecar", "This computer").unwrap().mode == Mode::Sidecar);
    }

    #[test]
    fn userinfo_is_rejected() {
        assert!(matches!(
            parse_link("http://token@127.0.0.1:8787", ""),
            Err(GatewayError::InvalidLink { .. })
        ));
    }
}
