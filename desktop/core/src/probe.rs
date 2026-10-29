use std::time::Duration;

use bytes::Bytes;
use http::{header, Request, StatusCode};
use http_body_util::{BodyExt, Full};
use hyper::client::conn::http1;
use hyper_util::rt::TokioIo;
use serde::Deserialize;
use tokio::time::timeout;
use url::Url;

use crate::error::GatewayError;
use crate::tlsclient;
use crate::PROTOCOL_VERSION;

#[derive(Debug, Deserialize)]
struct Health {
    #[serde(default)]
    ok: bool,
    protocol: Option<String>,
    #[serde(rename = "protocolVersion")]
    protocol_version: Option<String>,
}

pub struct ProbeTarget {
    pub base: String,
    pub token: Option<String>,
    pub pin: Option<String>,
}

/// `GET /health`, then `GET /v1/sessions` so a missing or wrong bearer is
/// visible. `/health` itself is not authenticated.
pub async fn probe(target: &ProbeTarget) -> Result<(), GatewayError> {
    let url = Url::parse(&target.base).map_err(|_| GatewayError::InvalidLink {
        detail: "gateway URL did not parse".into(),
    })?;
    let health = exchange(&url, target, "/health", None).await?;
    if !(200..300).contains(&health.status) {
        return Err(GatewayError::Unreachable {
            detail: format!("health returned HTTP {}", health.status),
        });
    }
    let parsed: Health = serde_json::from_slice(&health.body).unwrap_or(Health {
        ok: false,
        protocol: None,
        protocol_version: None,
    });
    if !parsed.ok {
        return Err(GatewayError::Unreachable {
            detail: "health did not report ok".into(),
        });
    }
    if let Some(got) = parsed
        .protocol
        .as_deref()
        .or(parsed.protocol_version.as_deref())
    {
        if got != PROTOCOL_VERSION {
            return Err(GatewayError::VersionMismatch {
                got: got.to_string(),
            });
        }
    }
    let sessions = exchange(&url, target, "/v1/sessions", target.token.as_deref()).await?;
    if sessions.status == StatusCode::UNAUTHORIZED.as_u16() {
        return Err(if target.token.as_deref().unwrap_or("").is_empty() {
            GatewayError::NeedsToken
        } else {
            GatewayError::BadToken
        });
    }
    if !(200..300).contains(&sessions.status) {
        return Err(GatewayError::Unreachable {
            detail: format!("sessions returned HTTP {}", sessions.status),
        });
    }
    Ok(())
}

struct Exchange {
    status: u16,
    body: Bytes,
}

async fn exchange(
    base: &Url,
    target: &ProbeTarget,
    path: &str,
    token: Option<&str>,
) -> Result<Exchange, GatewayError> {
    let host = base.host_str().unwrap_or("");
    let port = base.port_or_known_default().unwrap_or(80);
    let tls = base.scheme() == "https";
    let stream = tlsclient::connect(host, port, tls, target.pin.as_deref()).await?;
    let (mut sender, conn) = timeout(
        Duration::from_secs(5),
        http1::handshake::<_, Full<Bytes>>(TokioIo::new(stream)),
    )
    .await
    .map_err(|_| GatewayError::Unreachable {
        detail: "timed out".into(),
    })?
    .map_err(|_| GatewayError::Unreachable {
        detail: "http handshake failed".into(),
    })?;
    tokio::spawn(async move {
        let _ = conn.await;
    });
    let mut req = Request::builder()
        .method("GET")
        .uri(path)
        .header(header::HOST, host_header(host, port, tls))
        .header(header::ACCEPT, "application/json")
        .header(header::CONNECTION, "close");
    if let Some(token) = token.filter(|t| !t.is_empty()) {
        req = req.header(header::AUTHORIZATION, format!("Bearer {token}"));
    }
    let req = req
        .body(Full::new(Bytes::new()))
        .map_err(|_| GatewayError::Unreachable {
            detail: "could not build the probe request".into(),
        })?;
    let response = timeout(Duration::from_secs(5), sender.send_request(req))
        .await
        .map_err(|_| GatewayError::Unreachable {
            detail: "timed out".into(),
        })?
        .map_err(|_| GatewayError::Unreachable {
            detail: "probe request failed".into(),
        })?;
    let status = response.status().as_u16();
    let body = timeout(Duration::from_secs(5), response.into_body().collect())
        .await
        .map_err(|_| GatewayError::Unreachable {
            detail: "timed out".into(),
        })?
        .map_err(|_| GatewayError::Unreachable {
            detail: "probe body failed".into(),
        })?
        .to_bytes();
    Ok(Exchange { status, body })
}

// Incoming is only used as the proxied body type in proxy.rs.
#[cfg(test)]
mod tls_tests {
    use super::*;
    use std::sync::Arc;

    use hyper::server::conn::http1;
    use hyper::service::service_fn;
    use hyper_util::rt::TokioIo;
    use rustls::pki_types::PrivateKeyDer;
    use rustls::ServerConfig;
    use tokio::net::TcpListener;
    use tokio_rustls::TlsAcceptor;

    use crate::error::GatewayError;
    use crate::install_crypto;
    use crate::tlsclient::fingerprint;

    #[tokio::test]
    async fn self_signed_requires_a_matching_pin() {
        install_crypto();
        let certified = rcgen::generate_simple_self_signed(vec!["localhost".into()]).unwrap();
        let der = certified.cert.der().clone();
        let fp = fingerprint(der.as_ref());
        let key = PrivateKeyDer::Pkcs8(certified.key_pair.serialize_der().into());
        let server = ServerConfig::builder()
            .with_no_client_auth()
            .with_single_cert(vec![der], key)
            .unwrap();
        let acceptor = TlsAcceptor::from(Arc::new(server));
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            loop {
                let Ok((stream, _)) = listener.accept().await else {
                    break;
                };
                let Ok(tls) = acceptor.accept(stream).await else {
                    continue;
                };
                tokio::spawn(async move {
                    let service =
                        service_fn(|req: http::Request<hyper::body::Incoming>| async move {
                            let authed = req
                                .headers()
                                .get(http::header::AUTHORIZATION)
                                .and_then(|v| v.to_str().ok())
                                == Some("Bearer good-token");
                            let (status, body) = if req.uri().path() == "/health" {
                                (200, r#"{"ok":true,"version":"dev"}"#)
                            } else if authed {
                                (200, r#"{"sessions":[]}"#)
                            } else {
                                (401, "no")
                            };
                            Ok::<_, hyper::Error>(
                                http::Response::builder()
                                    .status(status)
                                    .header(http::header::CONTENT_TYPE, "application/json")
                                    .body(Full::new(Bytes::from(body)))
                                    .unwrap(),
                            )
                        });
                    let _ = http1::Builder::new()
                        .serve_connection(TokioIo::new(tls), service)
                        .await;
                });
            }
        });
        let base = format!("https://127.0.0.1:{}", addr.port());
        let unpinned = probe(&ProbeTarget {
            base: base.clone(),
            token: Some("good-token".into()),
            pin: None,
        })
        .await
        .unwrap_err();
        match unpinned {
            GatewayError::BadCert { fingerprint } => assert_eq!(fingerprint, fp),
            other => panic!("expected bad cert, got {other:?}"),
        }
        probe(&ProbeTarget {
            base: base.clone(),
            token: Some("good-token".into()),
            pin: Some(fp.clone()),
        })
        .await
        .expect("pinned cert should be accepted");
        let wrong = "ab".repeat(32);
        let mismatch = probe(&ProbeTarget {
            base,
            token: Some("good-token".into()),
            pin: Some(wrong),
        })
        .await
        .unwrap_err();
        assert!(
            matches!(mismatch, GatewayError::PinMismatch { .. }),
            "{mismatch:?}"
        );
    }
}

pub fn host_header(host: &str, port: u16, tls: bool) -> String {
    let default = if tls { 443 } else { 80 };
    if port == default {
        host.to_string()
    } else if host.contains(':') {
        format!("[{host}]:{port}")
    } else {
        format!("{host}:{port}")
    }
}
