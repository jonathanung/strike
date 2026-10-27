use std::path::PathBuf;
use std::process::Stdio;
use std::time::Duration;

use tokio::io::{AsyncBufReadExt, BufReader};
use tokio::process::{Child, Command};
use tokio::time::timeout;

use crate::banner::{self, ServeBanner};
use crate::error::GatewayError;
use crate::redact;

pub struct Sidecar {
    pub child: Child,
    pub banner: ServeBanner,
}

/// Spawn `strike serve --auth` on an ephemeral loopback port. The token is
/// read from the process pipe when strike mints it. It is not passed on argv
/// and it is not written to a file by this shell.
pub async fn spawn(bin: &PathBuf, provider: &str) -> Result<Sidecar, GatewayError> {
    let mut cmd = Command::new(bin);
    cmd.arg("serve")
        .arg("--auth")
        .arg("--addr")
        .arg("127.0.0.1:0")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    if let Ok(home) = std::env::var("STRIKE_DESKTOP_HOME") {
        if !home.is_empty() {
            cmd.env("HOME", home);
        }
    }
    if !provider.is_empty() {
        cmd.arg("--provider").arg(provider);
    }
    let mut child = cmd.spawn().map_err(|err| GatewayError::Sidecar {
        detail: format!("could not start {}: {err}", bin.display()),
    })?;
    let stdout = child.stdout.take().ok_or_else(|| GatewayError::Sidecar {
        detail: "daemon stdout was not piped".into(),
    })?;
    let stderr = child.stderr.take().ok_or_else(|| GatewayError::Sidecar {
        detail: "daemon stderr was not piped".into(),
    })?;
    let mut out_lines = BufReader::new(stdout).lines();
    let mut err_lines = BufReader::new(stderr).lines();
    let mut collected = String::new();
    let mut errors = String::new();
    let deadline = Duration::from_secs(8);
    let banner = timeout(deadline, async {
        loop {
            tokio::select! {
                line = out_lines.next_line() => {
                    match line {
                        Ok(Some(line)) => {
                            collected.push_str(&line);
                            collected.push('\n');
                            if let Some(parsed) = banner::parse_banner(&collected) {
                                if parsed.token.is_some() || collected.contains("token:") {
                                    return Ok(parsed);
                                }
                            }
                        }
                        Ok(None) => break,
                        Err(err) => return Err(err.to_string()),
                    }
                }
                line = err_lines.next_line() => {
                    if let Ok(Some(line)) = line {
                        if errors.len() < 800 {
                            errors.push_str(&line);
                            errors.push('\n');
                        }
                    }
                }
                status = child.wait() => {
                    let code = status.map(|s| s.to_string()).unwrap_or_else(|e| e.to_string());
                    return Err(format!("exited {code}"));
                }
            }
        }
        Err("daemon exited before printing its listen address".into())
    })
    .await;

    let parsed = match banner {
        Ok(Ok(parsed)) => parsed,
        other => {
            let secret = banner::parse_banner(&collected)
                .and_then(|b| b.token)
                .unwrap_or_default();
            let detail = if !errors.is_empty() {
                errors
            } else {
                match other {
                    Err(_) => "timed out waiting for the daemon banner".into(),
                    Ok(Err(detail)) => detail,
                    Ok(Ok(_)) => "daemon did not become ready".into(),
                }
            };
            let _ = child.start_kill();
            return Err(GatewayError::Sidecar {
                detail: redact(&detail, &secret),
            });
        }
    };
    // Drain the pipes so a later banner line cannot fill them. Do not log them.
    tokio::spawn(async move { while let Ok(Some(_)) = out_lines.next_line().await {} });
    tokio::spawn(async move { while let Ok(Some(_)) = err_lines.next_line().await {} });
    Ok(Sidecar {
        child,
        banner: parsed,
    })
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;

    use crate::desktop::Desktop;
    use crate::keystore::MemoryKeyStore;

    #[tokio::test]
    async fn sidecar_serves_through_the_proxy_when_strike_bin_is_set() {
        let Some(bin) = std::env::var_os("STRIKE_BIN") else {
            return;
        };
        let home = std::env::temp_dir().join(format!("strike-desktop-side-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&home);
        std::fs::create_dir_all(&home).unwrap();
        // SAFETY: only this test writes STRIKE_DESKTOP_HOME, and only when
        // STRIKE_BIN is set (CI leaves it unset and returns above).
        unsafe { std::env::set_var("STRIKE_DESKTOP_HOME", &home) };
        let dir = home.join("state");
        let desktop =
            Desktop::open(dir, Arc::new(MemoryKeyStore::default()), Some(bin.into())).unwrap();
        let addr = desktop.listen().await.unwrap();
        let added = desktop
            .add_gateway(br#"{"name":"Local","link":"sidecar","provider":"echo"}"#)
            .await
            .unwrap();
        let id = added["id"].as_str().unwrap();
        desktop.activate(id).await.expect("sidecar should come up");
        // Stay on the test runtime. A blocking read would stall the proxy,
        // which is spawned on this same current-thread runtime.
        let mut client = tokio::net::TcpStream::connect(addr).await.unwrap();
        let req = format!(
            "GET /health HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nConnection: close\r\n\r\n",
            addr.port()
        );
        use tokio::io::{AsyncReadExt, AsyncWriteExt};
        client.write_all(req.as_bytes()).await.unwrap();
        let mut buf = Vec::new();
        let _ = tokio::time::timeout(
            std::time::Duration::from_secs(5),
            client.read_to_end(&mut buf),
        )
        .await;
        let buf = String::from_utf8_lossy(&buf);
        assert!(buf.contains("200"), "{buf}");
        assert!(
            buf.contains("\"ok\":true") || buf.contains("\"ok\": true"),
            "{buf}"
        );
        desktop.shutdown().await;
        unsafe { std::env::remove_var("STRIKE_DESKTOP_HOME") };
        let _ = std::fs::remove_dir_all(&home);
    }
}

pub fn resolve_bin(explicit: Option<PathBuf>) -> PathBuf {
    if let Some(path) = explicit {
        return path;
    }
    if let Some(path) = std::env::var_os("STRIKE_BIN") {
        return PathBuf::from(path);
    }
    PathBuf::from("strike")
}
