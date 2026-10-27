use crate::error::GatewayError;
use crate::link::valid_ssh_target;

/// Arguments for `strike connect`. The grant is minted by that command and
/// stays in its process. This shell does not pass a token, `--open`, or
/// its own `StrictHostKeyChecking` override.
pub fn connect_args(target: &str, remote_port: u16) -> Result<Vec<String>, GatewayError> {
    let remote_port = if remote_port == 0 { 8787 } else { remote_port };
    if !valid_ssh_target(target) || !target.contains('@') || !(1..=65535).contains(&remote_port) {
        return Err(GatewayError::Ssh {
            detail: "ssh host must look like user@host with no shell characters".into(),
        });
    }
    Ok(vec![
        "--remote".into(),
        format!("127.0.0.1:{remote_port}"),
        "--local".into(),
        "127.0.0.1:0".into(),
        "--ttl".into(),
        "15m".into(),
        target.to_string(),
    ])
}

/// `strike connect` prints `Open http://127.0.0.1:<port>/`. Anything else
/// is refused so a noisy line cannot retarget the proxy.
pub fn parse_open_url(text: &str) -> Option<String> {
    for line in text.lines() {
        let Some(rest) = line.trim().strip_prefix("Open ") else {
            continue;
        };
        let url = rest.trim().trim_end_matches('/');
        if url.contains(['?', '#', '@', ' ']) {
            continue;
        }
        if url.starts_with("http://127.0.0.1:") || url.starts_with("http://[::1]:") {
            return Some(url.to_string());
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn args_are_argv_not_a_shell() {
        let args = connect_args("me@host", 8787).unwrap();
        assert_eq!(
            args,
            vec![
                "--remote",
                "127.0.0.1:8787",
                "--local",
                "127.0.0.1:0",
                "--ttl",
                "15m",
                "me@host",
            ]
        );
        assert!(!args.iter().any(|a| a == "--open" || a.contains("token")));
        assert!(connect_args("-oProxyCommand=curl", 8787).is_err());
        assert!(connect_args("me@host;id", 8787).is_err());
        assert!(connect_args("host-only", 8787).is_err());
    }

    #[test]
    fn open_url_must_be_loopback() {
        let banner =
            "Forwarding me@host\nOpen http://127.0.0.1:4311/\nThe grant stays in this process.\n";
        assert_eq!(
            parse_open_url(banner).as_deref(),
            Some("http://127.0.0.1:4311")
        );
        assert!(parse_open_url("Open http://100.64.0.5:8787/\n").is_none());
        assert!(parse_open_url("Open http://127.0.0.1:4311/?token=secret\n").is_none());
    }
}
