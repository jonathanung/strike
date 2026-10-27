use std::collections::VecDeque;
use std::path::PathBuf;
use std::sync::atomic::{AtomicU16, Ordering};
use std::sync::Arc;
use std::time::Duration;

use serde::Deserialize;
use serde_json::{json, Value};
use tokio::io::{AsyncBufReadExt, BufReader};
use tokio::net::TcpStream;
use tokio::process::{Child, Command};
use tokio::sync::{Mutex, Notify};
use tokio::time::{sleep, timeout};
use url::Url;
use uuid::Uuid;
use zeroize::Zeroizing;

use crate::error::GatewayError;
use crate::keystore::KeyStore;
use crate::link::parse_link;
use crate::probe::{self, ProbeTarget};
use crate::proxy;
use crate::redact;
use crate::sidecar;
use crate::sshfwd;
use crate::store::{Gateway, Mode, Store};
use crate::tlsclient::normalize_fp;

pub struct Notice {
    pub id: u64,
    pub title: String,
    pub body: String,
}

#[allow(dead_code)]
struct Running {
    id: String,
    child: Child,
    base: String,
    token: Zeroizing<String>,
    pin: String,
}

pub struct Upstream {
    pub host: String,
    pub port: u16,
    pub tls: bool,
    pub pin: String,
    pub token: Zeroizing<String>,
    pub name: String,
}

struct Inner {
    store: Store,
    upstream: Option<Upstream>,
    running: Option<Running>,
    notices: VecDeque<Notice>,
    notice_seq: u64,
}

pub struct Desktop {
    inner: Mutex<Inner>,
    keys: Arc<dyn KeyStore>,
    strike_bin: PathBuf,
    cancel: Notify,
    port: AtomicU16,
}

impl Desktop {
    pub fn open(
        dir: PathBuf,
        keys: Arc<dyn KeyStore>,
        strike_bin: Option<PathBuf>,
    ) -> Result<Arc<Self>, GatewayError> {
        let store = Store::open(&dir)?;
        Ok(Arc::new(Self {
            inner: Mutex::new(Inner {
                store,
                upstream: None,
                running: None,
                notices: VecDeque::new(),
                notice_seq: 0,
            }),
            keys,
            strike_bin: sidecar::resolve_bin(strike_bin),
            cancel: Notify::new(),
            port: AtomicU16::new(0),
        }))
    }

    pub async fn listen(self: &Arc<Self>) -> Result<std::net::SocketAddr, GatewayError> {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
            .await
            .map_err(|err| GatewayError::Store {
                detail: format!("could not bind the loopback proxy: {err}"),
            })?;
        let addr = listener.local_addr().map_err(|err| GatewayError::Store {
            detail: err.to_string(),
        })?;
        if !addr.ip().is_loopback() {
            return Err(GatewayError::Store {
                detail: "proxy refused a non-loopback bind".into(),
            });
        }
        self.port.store(addr.port(), Ordering::Relaxed);
        let desktop = Arc::clone(self);
        tokio::spawn(async move { proxy::run(desktop, listener).await });
        Ok(addr)
    }

    pub fn port(&self) -> u16 {
        self.port.load(Ordering::Relaxed)
    }

    pub async fn cancelled(&self) {
        self.cancel.notified().await;
    }

    pub async fn shutdown(&self) {
        self.stop_running().await;
        self.cancel.notify_waiters();
    }

    pub async fn list_json(&self) -> Value {
        let guard = self.inner.lock().await;
        let active = guard.store.active.clone();
        let gateways: Vec<_> = guard
            .store
            .gateways
            .iter()
            .map(|gw| gw.public_json(active.as_deref() == Some(gw.id.as_str())))
            .collect();
        json!({"gateways": gateways, "active": active})
    }

    pub async fn notices_json(&self) -> Value {
        let guard = self.inner.lock().await;
        let notices: Vec<_> = guard
            .notices
            .iter()
            .map(|n| json!({"id": n.id, "title": n.title, "body": n.body}))
            .collect();
        json!({"notices": notices})
    }

    pub async fn upstream(&self) -> Option<Upstream> {
        let guard = self.inner.lock().await;
        guard.upstream.as_ref().map(|up| Upstream {
            host: up.host.clone(),
            port: up.port,
            tls: up.tls,
            pin: up.pin.clone(),
            token: up.token.clone(),
            name: up.name.clone(),
        })
    }

    pub async fn add_gateway(&self, body: &[u8]) -> Result<Value, GatewayError> {
        let parsed: AddBody =
            serde_json::from_slice(body).map_err(|_| GatewayError::InvalidLink {
                detail: "expected a JSON gateway".into(),
            })?;
        let link = parse_link(&parsed.link, parsed.name.as_deref().unwrap_or(""))?;
        let mut token = link.token;
        if token.is_none() {
            token = parsed.token.filter(|t| !t.is_empty());
        }
        if let Some(secret) = token.as_deref() {
            reject_token(secret)?;
        }
        let id = Uuid::new_v4().to_string();
        if let Some(secret) = &token {
            self.keys
                .set(&id, secret)
                .map_err(|detail| GatewayError::Keyring { detail })?;
        }
        let gw = Gateway {
            id: id.clone(),
            name: link.name,
            mode: link.mode,
            url: link.url,
            ssh_target: link.ssh_target,
            remote_port: link.remote_port,
            cleartext: parsed.cleartext.unwrap_or(false),
            pin: String::new(),
            provider: parsed.provider.unwrap_or_default(),
            status: "idle".into(),
            detail: String::new(),
        };
        let mut guard = self.inner.lock().await;
        guard.store.gateways.push(gw);
        guard.store.save()?;
        drop(guard);
        let _ = token;
        Ok(json!({"ok": true, "id": id}))
    }

    pub async fn remove(&self, id: &str) -> Result<Value, GatewayError> {
        if self.inner.lock().await.store.active.as_deref() == Some(id) {
            self.stop_running().await;
            let mut guard = self.inner.lock().await;
            guard.upstream = None;
            guard.store.active = None;
        }
        let mut guard = self.inner.lock().await;
        let before = guard.store.gateways.len();
        guard.store.gateways.retain(|gw| gw.id != id);
        if guard.store.gateways.len() == before {
            return Err(GatewayError::NotFound);
        }
        guard.store.save()?;
        drop(guard);
        let _ = self.keys.delete(id);
        Ok(json!({"ok": true}))
    }

    pub async fn probe_id(&self, id: &str) -> Result<Value, GatewayError> {
        self.activate(id).await
    }

    pub async fn activate_body(&self, body: &[u8]) -> Result<Value, GatewayError> {
        let id = proxy::parse_id(body)?;
        self.activate(&id).await
    }

    pub async fn trust_body(&self, id: &str, body: &[u8]) -> Result<Value, GatewayError> {
        #[derive(Deserialize)]
        struct Trust {
            fingerprint: String,
        }
        let trust: Trust = serde_json::from_slice(body).map_err(|_| GatewayError::InvalidLink {
            detail: "expected a fingerprint".into(),
        })?;
        let fp = normalize_fp(&trust.fingerprint);
        if fp.len() != 64 {
            return Err(GatewayError::InvalidLink {
                detail: "fingerprint must be a SHA-256 hex string".into(),
            });
        }
        {
            let mut guard = self.inner.lock().await;
            let gw = guard
                .store
                .gateways
                .iter_mut()
                .find(|gw| gw.id == id)
                .ok_or(GatewayError::NotFound)?;
            gw.pin = fp;
            guard.store.save()?;
        }
        self.activate(id).await
    }

    pub async fn deeplink_body(&self, body: &[u8]) -> Result<Value, GatewayError> {
        #[derive(Deserialize)]
        struct Deep {
            url: String,
        }
        let deep: Deep = serde_json::from_slice(body).map_err(|_| GatewayError::InvalidLink {
            detail: "expected a url".into(),
        })?;
        let added = self
            .add_gateway(&serde_json::to_vec(&json!({"link": deep.url})).unwrap_or_default())
            .await?;
        let id = added["id"].as_str().unwrap_or("").to_string();
        // Activate failure still leaves the gateway saved. The error is the
        // connect result; the row stays in the list for a retry.
        self.activate(&id).await
    }

    pub async fn activate(&self, id: &str) -> Result<Value, GatewayError> {
        let gw = {
            let guard = self.inner.lock().await;
            guard
                .store
                .gateways
                .iter()
                .find(|gw| gw.id == id)
                .cloned()
                .ok_or(GatewayError::NotFound)?
        };
        self.stop_running().await;
        let prepared = match self.prepare(&gw).await {
            Ok(prepared) => prepared,
            Err(err) => {
                self.mark(&gw.id, Some(&err)).await;
                self.note(&gw.name, &err.status_label(), &err.detail());
                return Err(err);
            }
        };
        let probe_target = ProbeTarget {
            base: prepared.base.clone(),
            token: Some(prepared.token.to_string()).filter(|t| !t.is_empty()),
            pin: if prepared.pin.is_empty() {
                None
            } else {
                Some(prepared.pin.clone())
            },
        };
        if let Err(err) = probe::probe(&probe_target).await {
            self.mark(&gw.id, Some(&err)).await;
            self.note(
                &gw.name,
                &err.status_label(),
                &redact(&err.detail(), prepared.token.as_str()),
            );
            self.stop_running().await;
            return Err(err);
        }
        let name = gw.name.clone();
        self.install(gw.id.clone(), name.clone(), prepared).await?;
        self.note(&name, "Connected", "gateway is ready");
        Ok(json!({"ok": true, "id": id, "name": name, "cockpit": "/"}))
    }

    async fn prepare(&self, gw: &Gateway) -> Result<Prepared, GatewayError> {
        match gw.mode {
            Mode::Sidecar => self.prepare_sidecar(gw).await,
            Mode::Remote => self.prepare_remote(gw).await,
            Mode::Ssh => self.prepare_ssh(gw).await,
        }
    }

    async fn prepare_sidecar(&self, gw: &Gateway) -> Result<Prepared, GatewayError> {
        let started = sidecar::spawn(&self.strike_bin, &gw.provider).await?;
        let token = started.banner.token.unwrap_or_default();
        if token.is_empty() {
            let _ = started.child;
            return Err(GatewayError::Sidecar {
                detail: "daemon did not print a minted token".into(),
            });
        }
        self.keys
            .set(&gw.id, &token)
            .map_err(|detail| GatewayError::Keyring {
                detail: redact(&detail, &token),
            })?;
        let base = started.banner.listen;
        let mut guard = self.inner.lock().await;
        guard.running = Some(Running {
            id: gw.id.clone(),
            child: started.child,
            base: base.clone(),
            token: Zeroizing::new(token.clone()),
            pin: String::new(),
        });
        Ok(Prepared {
            base,
            token: Zeroizing::new(token),
            pin: String::new(),
        })
    }

    async fn prepare_remote(&self, gw: &Gateway) -> Result<Prepared, GatewayError> {
        let url = Url::parse(&gw.url).map_err(|_| GatewayError::InvalidLink {
            detail: "saved URL did not parse".into(),
        })?;
        if url.scheme() == "http"
            && !host_is_loopback(url.host_str().unwrap_or(""))
            && !gw.cleartext
        {
            return Err(GatewayError::CleartextRefused);
        }
        if url.scheme() != "http" && url.scheme() != "https" {
            return Err(GatewayError::InvalidLink {
                detail: "saved URL is not http(s)".into(),
            });
        }
        let token = self
            .keys
            .get(&gw.id)
            .map_err(|detail| GatewayError::Keyring { detail })?
            .unwrap_or_else(|| Zeroizing::new(String::new()));
        Ok(Prepared {
            base: gw.url.trim_end_matches('/').to_string(),
            token,
            pin: gw.pin.clone(),
        })
    }

    async fn prepare_ssh(&self, gw: &Gateway) -> Result<Prepared, GatewayError> {
        let local = sshfwd::reserve_loopback_port().await?;
        let args = sshfwd::ssh_args(local, &gw.ssh_target, gw.remote_port.max(1))?;
        let mut child = Command::new("ssh");
        child
            .args(&args)
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::piped())
            .kill_on_drop(true);
        let mut child = child.spawn().map_err(|err| GatewayError::Ssh {
            detail: format!("could not start ssh: {err}"),
        })?;
        let stderr = child.stderr.take();
        let ready = timeout(Duration::from_secs(8), async {
            loop {
                if let Ok(Some(status)) = child.try_wait() {
                    return Err(format!("ssh exited {status}"));
                }
                if TcpStream::connect(("127.0.0.1", local)).await.is_ok() {
                    return Ok(());
                }
                sleep(Duration::from_millis(80)).await;
            }
        })
        .await;
        if ready.is_err() || matches!(ready, Ok(Err(_))) {
            let mut detail = match ready {
                Err(_) => "ssh forward did not open".to_string(),
                Ok(Err(detail)) => detail,
                Ok(Ok(())) => String::new(),
            };
            if let Some(stderr) = stderr {
                let mut lines = BufReader::new(stderr).lines();
                if let Ok(Some(line)) = lines.next_line().await {
                    if detail.is_empty() {
                        detail = line;
                    }
                }
            }
            let _ = child.start_kill();
            return Err(GatewayError::Ssh { detail });
        }
        let token = self
            .keys
            .get(&gw.id)
            .map_err(|detail| GatewayError::Keyring { detail })?
            .unwrap_or_else(|| Zeroizing::new(String::new()));
        let base = format!("http://127.0.0.1:{local}");
        let mut guard = self.inner.lock().await;
        guard.running = Some(Running {
            id: gw.id.clone(),
            child,
            base: base.clone(),
            token: token.clone(),
            pin: String::new(),
        });
        Ok(Prepared {
            base,
            token,
            pin: String::new(),
        })
    }

    async fn install(
        &self,
        id: String,
        name: String,
        prepared: Prepared,
    ) -> Result<(), GatewayError> {
        let url = Url::parse(&prepared.base).map_err(|_| GatewayError::InvalidLink {
            detail: "upstream URL did not parse".into(),
        })?;
        let host = url.host_str().unwrap_or("").to_string();
        let port = url.port_or_known_default().unwrap_or(80);
        let upstream = Upstream {
            host,
            port,
            tls: url.scheme() == "https",
            pin: prepared.pin,
            token: prepared.token,
            name,
        };
        let mut guard = self.inner.lock().await;
        if let Some(gw) = guard.store.gateways.iter_mut().find(|gw| gw.id == id) {
            gw.status = "connected".into();
            gw.detail.clear();
        }
        guard.store.active = Some(id);
        guard.upstream = Some(upstream);
        guard.store.save()?;
        Ok(())
    }

    async fn mark(&self, id: &str, err: Option<&GatewayError>) {
        let mut guard = self.inner.lock().await;
        if let Some(gw) = guard.store.gateways.iter_mut().find(|gw| gw.id == id) {
            match err {
                None => {
                    gw.status = "connected".into();
                    gw.detail.clear();
                }
                Some(err) => {
                    gw.status = err.code().into();
                    gw.detail = err.detail();
                }
            }
        }
    }

    fn note(&self, name: &str, title: &str, body: &str) {
        let Ok(mut guard) = self.inner.try_lock() else {
            return;
        };
        guard.notice_seq += 1;
        let id = guard.notice_seq;
        guard.notices.push_back(Notice {
            id,
            title: format!("{title}: {name}"),
            body: body.chars().take(240).collect(),
        });
        while guard.notices.len() > 20 {
            guard.notices.pop_front();
        }
    }

    async fn stop_running(&self) {
        let running = self.inner.lock().await.running.take();
        if let Some(mut running) = running {
            let _ = running.child.start_kill();
            let _ = timeout(Duration::from_secs(2), running.child.wait()).await;
        }
        if let Ok(mut guard) = self.inner.try_lock() {
            guard.upstream = None;
        }
    }
}

struct Prepared {
    base: String,
    token: Zeroizing<String>,
    pin: String,
}

#[derive(Deserialize)]
struct AddBody {
    #[serde(default)]
    name: Option<String>,
    link: String,
    #[serde(default)]
    token: Option<String>,
    #[serde(default)]
    cleartext: Option<bool>,
    #[serde(default)]
    provider: Option<String>,
}

fn reject_token(secret: &str) -> Result<(), GatewayError> {
    let header = format!("Bearer {secret}");
    if secret.is_empty() || secret.len() > 512 || http::HeaderValue::from_str(&header).is_err() {
        return Err(GatewayError::InvalidLink {
            detail: "token is empty or not a single line".into(),
        });
    }
    Ok(())
}

fn host_is_loopback(host: &str) -> bool {
    let host = host.trim_matches(['[', ']']);
    host.eq_ignore_ascii_case("localhost") || host == "127.0.0.1" || host == "::1"
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::keystore::MemoryKeyStore;
    use bytes::Bytes;
    use http::{header, Request, Response, StatusCode};
    use http_body_util::{BodyExt, Full};
    use hyper::body::Incoming;
    use hyper::client::conn::http1 as client_http;
    use hyper::server::conn::http1 as server_http;
    use hyper::service::service_fn;
    use hyper_util::rt::TokioIo;
    use std::sync::atomic::{AtomicUsize, Ordering};

    async fn boot() -> (Arc<Desktop>, std::net::SocketAddr) {
        let dir = std::env::temp_dir().join(format!(
            "strike-desktop-{}-{}",
            std::process::id(),
            Uuid::new_v4()
        ));
        let desktop = Desktop::open(dir, Arc::new(MemoryKeyStore::default()), None).unwrap();
        let addr = desktop.listen().await.unwrap();
        (desktop, addr)
    }

    async fn http(addr: std::net::SocketAddr, req: Request<Full<Bytes>>) -> Response<Incoming> {
        let stream = TcpStream::connect(addr).await.unwrap();
        let (mut sender, conn) = client_http::handshake::<_, Full<Bytes>>(TokioIo::new(stream))
            .await
            .unwrap();
        tokio::spawn(async move {
            let _ = conn.await;
        });
        sender.send_request(req).await.unwrap()
    }

    fn get(addr: std::net::SocketAddr, path: &str) -> Request<Full<Bytes>> {
        Request::builder()
            .uri(path)
            .header(header::HOST, format!("127.0.0.1:{}", addr.port()))
            .body(Full::new(Bytes::new()))
            .unwrap()
    }

    async fn mock_gateway(sessions: u16, health: &'static str) -> std::net::SocketAddr {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            loop {
                let Ok((stream, _)) = listener.accept().await else {
                    break;
                };
                tokio::spawn(async move {
                    let service = service_fn(move |req: Request<Incoming>| async move {
                        let path = req.uri().path().to_string();
                        let auth = req
                            .headers()
                            .get(header::AUTHORIZATION)
                            .and_then(|v| v.to_str().ok())
                            .unwrap_or("")
                            .to_string();
                        let (status, body) = if path == "/health" {
                            (200, health.to_string())
                        } else if path == "/v1/sessions" {
                            if sessions == 401
                                || (sessions == 200 && auth != "Bearer good-token")
                                    && sessions != 200
                            {
                                (401, "no".into())
                            } else if auth == "Bearer good-token"
                                || sessions == 200 && auth == "Bearer good-token"
                            {
                                (200, "{\"sessions\":[]}".into())
                            } else if sessions == 200 {
                                (401, "no".into())
                            } else {
                                (sessions, "no".into())
                            }
                        } else if path == "/" {
                            let page =
                                format!("<html><head></head><body>cockpit {auth}</body></html>");
                            return Ok::<_, hyper::Error>(
                                Response::builder()
                                    .status(200)
                                    .header(header::CONTENT_TYPE, "text/html")
                                    .body(Full::new(Bytes::from(page)))
                                    .unwrap(),
                            );
                        } else {
                            (404, "no".into())
                        };
                        let _ = auth;
                        Ok::<_, hyper::Error>(
                            Response::builder()
                                .status(status)
                                .header(header::CONTENT_TYPE, "application/json")
                                .body(Full::new(Bytes::from(body)))
                                .unwrap(),
                        )
                    });
                    let _ = server_http::Builder::new()
                        .serve_connection(TokioIo::new(stream), service)
                        .await;
                });
            }
        });
        addr
    }

    #[tokio::test]
    async fn switches_gateways_without_leaking_the_token() {
        let (desktop, addr) = boot().await;
        let a = mock_gateway(200, r#"{"ok":true,"version":"dev"}"#).await;
        let b = mock_gateway(401, r#"{"ok":true,"version":"dev"}"#).await;
        let add = http(
            addr,
            Request::builder()
                .method("POST")
                .uri("/_desktop/api/gateways")
                .header(header::HOST, format!("127.0.0.1:{}", addr.port()))
                .header(header::CONTENT_TYPE, "application/json")
                .body(Full::new(Bytes::from(format!(
                    "{{\"name\":\"Alpha\",\"link\":\"http://127.0.0.1:{}/?token=good-token\",\"token\":\"should-not-win\"}}",
                    a.port()
                ))))
                .unwrap(),
        )
        .await;
        let body = add.into_body().collect().await.unwrap().to_bytes();
        let added: Value = serde_json::from_slice(&body).unwrap();
        let id = added["id"].as_str().unwrap().to_string();
        let raw = std::fs::read_to_string(desktop.inner.lock().await.store.path()).unwrap();
        assert!(!raw.contains("good-token"), "{raw}");

        let active = http(
            addr,
            Request::builder()
                .method("POST")
                .uri("/_desktop/api/active")
                .header(header::HOST, format!("127.0.0.1:{}", addr.port()))
                .header(header::CONTENT_TYPE, "application/json")
                .body(Full::new(Bytes::from(format!("{{\"id\":\"{id}\"}}"))))
                .unwrap(),
        )
        .await;
        assert_eq!(active.status(), StatusCode::OK);
        let page = http(addr, get(addr, "/")).await;
        let html = String::from_utf8(
            page.into_body()
                .collect()
                .await
                .unwrap()
                .to_bytes()
                .to_vec(),
        )
        .unwrap();
        assert!(html.contains("content=\"Alpha\""), "{html}");
        assert!(html.contains("/_desktop/chrome.js"), "{html}");
        assert!(!html.contains("content=\"good-token\""), "{html}");
        // The mock writes the Authorization header it received into the page,
        // which is how this test sees the injected bearer.
        assert!(html.contains("cockpit Bearer good-token"), "{html}");
        let leaked = http(
            addr,
            Request::builder()
                .uri("/")
                .header(header::HOST, format!("127.0.0.1:{}", addr.port()))
                .header("sec-fetch-site", "cross-site")
                .body(Full::new(Bytes::new()))
                .unwrap(),
        )
        .await;
        assert_eq!(leaked.status(), StatusCode::FORBIDDEN);

        let bad = http(
            addr,
            Request::builder()
                .method("POST")
                .uri("/_desktop/api/gateways")
                .header(header::HOST, format!("127.0.0.1:{}", addr.port()))
                .header(header::CONTENT_TYPE, "application/json")
                .body(Full::new(Bytes::from(format!(
                    "{{\"name\":\"Beta\",\"link\":\"http://127.0.0.1:{}\",\"token\":\"nope\"}}",
                    b.port()
                ))))
                .unwrap(),
        )
        .await;
        let bad_id =
            serde_json::from_slice::<Value>(&bad.into_body().collect().await.unwrap().to_bytes())
                .unwrap()["id"]
                .as_str()
                .unwrap()
                .to_string();
        let failed = http(
            addr,
            Request::builder()
                .method("POST")
                .uri("/_desktop/api/active")
                .header(header::HOST, format!("127.0.0.1:{}", addr.port()))
                .header(header::CONTENT_TYPE, "application/json")
                .body(Full::new(Bytes::from(format!("{{\"id\":\"{bad_id}\"}}"))))
                .unwrap(),
        )
        .await;
        assert_eq!(failed.status(), StatusCode::UNAUTHORIZED);
        let err = String::from_utf8(
            failed
                .into_body()
                .collect()
                .await
                .unwrap()
                .to_bytes()
                .to_vec(),
        )
        .unwrap();
        assert!(err.contains("bad_token"), "{err}");
        assert!(!err.contains("nope"), "{err}");
        assert!(!err.contains("good-token"), "{err}");

        let listed = http(addr, get(addr, "/_desktop/api/gateways")).await;
        let listed = String::from_utf8(
            listed
                .into_body()
                .collect()
                .await
                .unwrap()
                .to_bytes()
                .to_vec(),
        )
        .unwrap();
        assert!(!listed.contains("good-token"), "{listed}");
        assert!(!listed.contains("nope"), "{listed}");
        assert!(listed.contains("bad_token"), "{listed}");
    }

    #[tokio::test]
    async fn classifies_unreachable_version_cleartext_and_cross_site() {
        let (desktop, addr) = boot().await;
        let _ = desktop;
        let closed = http(
            addr,
            Request::builder()
                .method("POST")
                .uri("/_desktop/api/gateways")
                .header(header::HOST, format!("127.0.0.1:{}", addr.port()))
                .header(header::CONTENT_TYPE, "application/json")
                .body(Full::new(Bytes::from(
                    r#"{"name":"Gone","link":"http://127.0.0.1:1","token":"x"}"#,
                )))
                .unwrap(),
        )
        .await;
        let id = serde_json::from_slice::<Value>(
            &closed.into_body().collect().await.unwrap().to_bytes(),
        )
        .unwrap()["id"]
            .as_str()
            .unwrap()
            .to_string();
        let failed = http(
            addr,
            Request::builder()
                .method("POST")
                .uri("/_desktop/api/active")
                .header(header::HOST, format!("127.0.0.1:{}", addr.port()))
                .header(header::CONTENT_TYPE, "application/json")
                .body(Full::new(Bytes::from(format!("{{\"id\":\"{id}\"}}"))))
                .unwrap(),
        )
        .await;
        let err = String::from_utf8(
            failed
                .into_body()
                .collect()
                .await
                .unwrap()
                .to_bytes()
                .to_vec(),
        )
        .unwrap();
        assert!(err.contains("unreachable"), "{err}");

        let weird = mock_gateway(200, r#"{"ok":true,"version":"dev","protocol":"9.9.9"}"#).await;
        let added = http(
            addr,
            Request::builder()
                .method("POST")
                .uri("/_desktop/api/gateways")
                .header(header::HOST, format!("127.0.0.1:{}", addr.port()))
                .header(header::CONTENT_TYPE, "application/json")
                .body(Full::new(Bytes::from(format!(
                    "{{\"name\":\"Old\",\"link\":\"http://127.0.0.1:{}\",\"token\":\"good-token\"}}",
                    weird.port()
                ))))
                .unwrap(),
        )
        .await;
        let id =
            serde_json::from_slice::<Value>(&added.into_body().collect().await.unwrap().to_bytes())
                .unwrap()["id"]
                .as_str()
                .unwrap()
                .to_string();
        let failed = http(
            addr,
            Request::builder()
                .method("POST")
                .uri("/_desktop/api/active")
                .header(header::HOST, format!("127.0.0.1:{}", addr.port()))
                .header(header::CONTENT_TYPE, "application/json")
                .body(Full::new(Bytes::from(format!("{{\"id\":\"{id}\"}}"))))
                .unwrap(),
        )
        .await;
        let err = String::from_utf8(
            failed
                .into_body()
                .collect()
                .await
                .unwrap()
                .to_bytes()
                .to_vec(),
        )
        .unwrap();
        assert!(err.contains("version_mismatch"), "{err}");
        assert!(err.contains("9.9.9"), "{err}");

        let clear = http(
            addr,
            Request::builder()
                .method("POST")
                .uri("/_desktop/api/gateways")
                .header(header::HOST, format!("127.0.0.1:{}", addr.port()))
                .header(header::CONTENT_TYPE, "application/json")
                .body(Full::new(Bytes::from(
                    r#"{"name":"Tail","link":"http://100.64.0.5:8787","token":"t"}"#,
                )))
                .unwrap(),
        )
        .await;
        let id =
            serde_json::from_slice::<Value>(&clear.into_body().collect().await.unwrap().to_bytes())
                .unwrap()["id"]
                .as_str()
                .unwrap()
                .to_string();
        let failed = http(
            addr,
            Request::builder()
                .method("POST")
                .uri("/_desktop/api/active")
                .header(header::HOST, format!("127.0.0.1:{}", addr.port()))
                .header(header::CONTENT_TYPE, "application/json")
                .body(Full::new(Bytes::from(format!("{{\"id\":\"{id}\"}}"))))
                .unwrap(),
        )
        .await;
        let err = String::from_utf8(
            failed
                .into_body()
                .collect()
                .await
                .unwrap()
                .to_bytes()
                .to_vec(),
        )
        .unwrap();
        assert!(err.contains("cleartext_refused"), "{err}");

        let crossed = http(
            addr,
            Request::builder()
                .method("POST")
                .uri("/_desktop/api/active")
                .header(header::HOST, format!("127.0.0.1:{}", addr.port()))
                .header(header::ORIGIN, "https://evil.example")
                .header("sec-fetch-site", "cross-site")
                .header(header::CONTENT_TYPE, "application/json")
                .body(Full::new(Bytes::from(format!("{{\"id\":\"{id}\"}}"))))
                .unwrap(),
        )
        .await;
        assert_eq!(crossed.status(), StatusCode::FORBIDDEN);

        let wrong_host = http(
            addr,
            Request::builder()
                .uri("/_desktop/api/gateways")
                .header(header::HOST, "evil.example")
                .body(Full::new(Bytes::new()))
                .unwrap(),
        )
        .await;
        assert_eq!(wrong_host.status(), StatusCode::MISDIRECTED_REQUEST);
        let _ = AtomicUsize::new(0).load(Ordering::Relaxed);
    }
}
