use crate::error::GatewayError;
use crate::link::valid_ssh_target;

pub fn ssh_args(
    local_port: u16,
    target: &str,
    remote_port: u16,
) -> Result<Vec<String>, GatewayError> {
    if !valid_ssh_target(target)
        || !(1..=65535).contains(&local_port)
        || !(1..=65535).contains(&remote_port)
    {
        return Err(GatewayError::Ssh {
            detail: "ssh host must look like user@host with no shell characters".into(),
        });
    }
    Ok(vec![
        "-N".into(),
        "-o".into(),
        "ExitOnForwardFailure=yes".into(),
        "-o".into(),
        "BatchMode=yes".into(),
        "-o".into(),
        "StrictHostKeyChecking=accept-new".into(),
        "-L".into(),
        format!("127.0.0.1:{local_port}:127.0.0.1:{remote_port}"),
        target.to_string(),
    ])
}

/// Bind a loopback port, drop it, and return the port. `ssh -L` binds it next.
pub async fn reserve_loopback_port() -> Result<u16, GatewayError> {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .map_err(|e| GatewayError::Ssh {
            detail: format!("could not reserve a local port: {e}"),
        })?;
    let port = listener
        .local_addr()
        .map_err(|e| GatewayError::Ssh {
            detail: e.to_string(),
        })?
        .port();
    drop(listener);
    Ok(port)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn args_are_argv_not_a_shell() {
        let args = ssh_args(2201, "me@host", 8787).unwrap();
        assert_eq!(args[0], "-N");
        assert!(args.iter().any(|a| a == "127.0.0.1:2201:127.0.0.1:8787"));
        assert_eq!(args.last().map(String::as_str), Some("me@host"));
        assert!(ssh_args(1, "-oProxyCommand=curl", 8787).is_err());
        assert!(ssh_args(1, "me@host;id", 8787).is_err());
    }
}
