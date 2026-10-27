//! Loopback reverse proxy.
//!
//! The webview loads this process, not the daemon. Every upstream request gets
//! the gateway bearer injected here. Responses do not gain CORS headers, do
//! not set the daemon cookie, and are refused when the client is not this
//! same loopback origin. The daemon itself stays on whatever address it was
//! already using; this shell does not bind it more widely.

use std::sync::Arc;

use bytes::Bytes;
use http::{header, HeaderMap, HeaderName, HeaderValue, Method, Request, Response, StatusCode};
use http_body::Frame;
use http_body_util::{BodyExt, Limited};
use hyper::body::{Body, Incoming};
use hyper::server::conn::http1;
use hyper::service::service_fn;
use hyper_util::rt::TokioIo;
use pin_project_lite::pin_project;
use serde::Deserialize;
use tokio::net::TcpListener;

use crate::desktop::Desktop;
use crate::error::GatewayError;
use crate::tlsclient;

pin_project! {
    #[project = RespProj]
    pub struct RespBody {
        #[pin]
        inner: InnerBody,
    }
}

pin_project! {
    #[project = InnerProj]
    enum InnerBody {
        Full { data: Option<Bytes> },
        Upstream { #[pin] body: Incoming },
    }
}

impl RespBody {
    fn full(bytes: impl Into<Bytes>) -> Self {
        Self {
            inner: InnerBody::Full {
                data: Some(bytes.into()),
            },
        }
    }

    fn upstream(body: Incoming) -> Self {
        Self {
            inner: InnerBody::Upstream { body },
        }
    }
}

impl Body for RespBody {
    type Data = Bytes;
    type Error = Box<dyn std::error::Error + Send + Sync>;

    fn poll_frame(
        self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Option<Result<Frame<Self::Data>, Self::Error>>> {
        let this = self.project();
        match this.inner.project() {
            InnerProj::Full { data } => {
                std::task::Poll::Ready(data.take().map(|b| Ok(Frame::data(b))))
            }
            InnerProj::Upstream { body } => match body.poll_frame(cx) {
                std::task::Poll::Ready(Some(Ok(frame))) => std::task::Poll::Ready(Some(Ok(frame))),
                std::task::Poll::Ready(Some(Err(err))) => {
                    std::task::Poll::Ready(Some(Err(Box::new(err))))
                }
                std::task::Poll::Ready(None) => std::task::Poll::Ready(None),
                std::task::Poll::Pending => std::task::Poll::Pending,
            },
        }
    }
}

pub async fn run(desktop: Arc<Desktop>, listener: TcpListener) {
    loop {
        tokio::select! {
            _ = desktop.cancelled() => break,
            accepted = listener.accept() => {
                let Ok((stream, peer)) = accepted else { break };
                if !peer.ip().is_loopback() {
                    drop(stream);
                    continue;
                }
                let desktop = desktop.clone();
                tokio::spawn(async move {
                    let service = service_fn(move |req| {
                        let desktop = desktop.clone();
                        async move { Ok::<_, std::convert::Infallible>(dispatch(desktop, req).await) }
                    });
                    let io = TokioIo::new(stream);
                    let conn = http1::Builder::new().serve_connection(io, service);
                    let _ = conn.with_upgrades().await;
                });
            }
        }
    }
}

async fn dispatch(desktop: Arc<Desktop>, req: Request<Incoming>) -> Response<RespBody> {
    let port = desktop.port();
    if !host_ok(req.headers(), port) {
        return text(
            StatusCode::MISDIRECTED_REQUEST,
            "host is not this loopback proxy",
        );
    }
    let path = req.uri().path();
    if path.starts_with("/_desktop") {
        return desktop_route(desktop, req).await;
    }
    // Navigation from another site is also refused. The window loads this
    // origin directly, so a foreign page cannot ride the injected bearer.
    if !same_site(req.headers(), port) {
        return text(StatusCode::FORBIDDEN, "cross-site request refused");
    }
    proxy(desktop, req).await
}

fn host_ok(headers: &HeaderMap, port: u16) -> bool {
    let Some(host) = headers.get(header::HOST).and_then(|v| v.to_str().ok()) else {
        return false;
    };
    host == format!("127.0.0.1:{port}")
}

fn same_site(headers: &HeaderMap, port: u16) -> bool {
    if let Some(site) = headers.get("sec-fetch-site").and_then(|v| v.to_str().ok()) {
        if site == "cross-site" {
            return false;
        }
    }
    if let Some(origin) = headers.get(header::ORIGIN).and_then(|v| v.to_str().ok()) {
        if origin == "null" || origin != format!("http://127.0.0.1:{port}") {
            return false;
        }
    }
    true
}

async fn desktop_route(desktop: Arc<Desktop>, req: Request<Incoming>) -> Response<RespBody> {
    let port = desktop.port();
    let path = req.uri().path().trim_end_matches('/').to_string();
    let path = if path.is_empty() {
        "/_desktop".into()
    } else {
        path
    };
    if req.method() == Method::GET {
        if let Some((content_type, bytes)) = asset(&path) {
            return Response::builder()
                .status(StatusCode::OK)
                .header(header::CONTENT_TYPE, content_type)
                .header(header::CACHE_CONTROL, "no-store")
                .header(header::X_CONTENT_TYPE_OPTIONS, "nosniff")
                .header(
                    header::CONTENT_SECURITY_POLICY,
                    "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'",
                )
                .body(RespBody::full(bytes))
                .unwrap();
        }
        if path == "/_desktop/api/gateways" {
            return json_ok(desktop.list_json().await);
        }
        if path == "/_desktop/api/notices" {
            return json_ok(desktop.notices_json().await);
        }
        return text(StatusCode::NOT_FOUND, "not found");
    }
    if !same_site(req.headers(), port) {
        return text(StatusCode::FORBIDDEN, "cross-site request refused");
    }
    let method = req.method().clone();
    let body = match Limited::new(req.into_body(), 64 * 1024).collect().await {
        Ok(collected) => collected.to_bytes(),
        Err(_) => return text(StatusCode::PAYLOAD_TOO_LARGE, "body is too large"),
    };
    if method == Method::POST && path == "/_desktop/api/gateways" {
        return match desktop.add_gateway(&body).await {
            Ok(value) => json_ok(value),
            Err(err) => json_err(err),
        };
    }
    if method == Method::POST && path == "/_desktop/api/active" {
        return match desktop.activate_body(&body).await {
            Ok(value) => json_ok(value),
            Err(err) => json_err(err),
        };
    }
    if method == Method::POST && path == "/_desktop/api/deeplink" {
        return match desktop.deeplink_body(&body).await {
            Ok(value) => json_ok(value),
            Err(err) => json_err(err),
        };
    }
    if let Some(id) = path.strip_prefix("/_desktop/api/gateways/") {
        if let Some(id) = id.strip_suffix("/trust") {
            if method == Method::POST {
                return match desktop.trust_body(id, &body).await {
                    Ok(value) => json_ok(value),
                    Err(err) => json_err(err),
                };
            }
        } else if method == Method::POST && path.ends_with("/probe") {
            let id = id.trim_end_matches("/probe");
            return match desktop.probe_id(id).await {
                Ok(value) => json_ok(value),
                Err(err) => json_err(err),
            };
        } else if method == Method::DELETE && !id.contains('/') {
            return match desktop.remove(id).await {
                Ok(value) => json_ok(value),
                Err(err) => json_err(err),
            };
        }
    }
    text(StatusCode::NOT_FOUND, "not found")
}

fn asset(path: &str) -> Option<(&'static str, &'static [u8])> {
    match path {
        "/_desktop" => Some((
            "text/html; charset=utf-8",
            include_bytes!("../../ui/index.html"),
        )),
        "/_desktop/app.css" => Some((
            "text/css; charset=utf-8",
            include_bytes!("../../ui/app.css"),
        )),
        "/_desktop/app.js" => Some((
            "text/javascript; charset=utf-8",
            include_bytes!("../../ui/app.js"),
        )),
        "/_desktop/boot.js" => Some((
            "text/javascript; charset=utf-8",
            include_bytes!("../../ui/boot.js"),
        )),
        "/_desktop/chrome.js" => Some((
            "text/javascript; charset=utf-8",
            include_bytes!("../../ui/chrome.js"),
        )),
        "/_desktop/chrome.css" => Some((
            "text/css; charset=utf-8",
            include_bytes!("../../ui/chrome.css"),
        )),
        "/_desktop/icon.svg" => Some(("image/svg+xml", include_bytes!("../../ui/icon.svg"))),
        _ => None,
    }
}

async fn proxy(desktop: Arc<Desktop>, req: Request<Incoming>) -> Response<RespBody> {
    let Some(up) = desktop.upstream().await else {
        return html(
            StatusCode::SERVICE_UNAVAILABLE,
            "No gateway selected. <a href=\"/_desktop/\">Open gateways</a>.",
        );
    };
    let path = match path_and_query(req.uri()) {
        Ok(path) => path,
        Err(()) => return text(StatusCode::BAD_REQUEST, "refused path"),
    };
    let ws = is_websocket(&req);
    let stream = match tlsclient::connect(
        up.host.as_str(),
        up.port,
        up.tls,
        if up.pin.is_empty() {
            None
        } else {
            Some(up.pin.as_str())
        },
    )
    .await
    {
        Ok(stream) => stream,
        Err(err) => return json_err(err),
    };
    let (mut sender, conn) =
        match hyper::client::conn::http1::handshake::<_, Incoming>(TokioIo::new(stream)).await {
            Ok(pair) => pair,
            Err(_) => {
                return json_err(GatewayError::Unreachable {
                    detail: "http handshake failed".into(),
                })
            }
        };
    tokio::spawn(async move {
        let _ = conn.with_upgrades().await;
    });
    let method = req.method().clone();
    let mut builder = Request::builder().method(method).uri(path);
    let out_headers = builder.headers_mut().unwrap();
    copy_forward(&req, out_headers, ws);
    if !up.token.is_empty() {
        if let Ok(value) = HeaderValue::from_str(&format!("Bearer {}", up.token.as_str())) {
            out_headers.insert(header::AUTHORIZATION, value);
        }
    }
    if let Ok(host) = HeaderValue::from_str(&crate::probe::host_header(&up.host, up.port, up.tls)) {
        out_headers.insert(header::HOST, host);
    }
    let name = up.name.clone();
    let upstream_req = match builder.body(req.into_body()) {
        Ok(req) => req,
        Err(_) => return text(StatusCode::BAD_REQUEST, "bad request"),
    };
    let resp = match sender.send_request(upstream_req).await {
        Ok(resp) => resp,
        Err(_) => {
            return json_err(GatewayError::Unreachable {
                detail: "gateway closed the connection".into(),
            })
        }
    };
    if resp.status() == StatusCode::SWITCHING_PROTOCOLS {
        return finish_upgrade(resp).await;
    }
    let status = resp.status();
    let content_type = resp
        .headers()
        .get(header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_string();
    let encoded = resp.headers().get(header::CONTENT_ENCODING).is_some();
    let mut headers = HeaderMap::new();
    copy_back(resp.headers(), &mut headers);
    if content_type.contains("text/html") && !encoded {
        let bytes = match resp.into_body().collect().await {
            Ok(collected) => collected.to_bytes(),
            Err(_) => return text(StatusCode::BAD_GATEWAY, "gateway response failed"),
        };
        if bytes.len() > 1_500_000 {
            return text(
                StatusCode::BAD_GATEWAY,
                "html response is too large to frame",
            );
        }
        let injected = inject_chrome(&bytes, &name);
        headers.remove(header::CONTENT_LENGTH);
        return response(status, headers, RespBody::full(injected));
    }
    response(status, headers, RespBody::upstream(resp.into_body()))
}

async fn finish_upgrade(resp: Response<Incoming>) -> Response<RespBody> {
    // The client upgrade is taken from the inbound request by hyper when the
    // 101 we return is written. Here we only need to drive the upstream side
    // once the browser completes its handshake. hyper::upgrade::on consumes
    // the upstream response's OnUpgrade; we rebuild the 101 for the browser.
    let status = resp.status();
    let mut headers = HeaderMap::new();
    copy_back(resp.headers(), &mut headers);
    let upgraded = hyper::upgrade::on(resp);
    tokio::spawn(async move {
        if let Ok(up) = upgraded.await {
            // The paired client upgrade is owned by the server connection
            // after it writes 101. Without a handle to that half here, drop
            // the upstream upgrade. The SSE path is what the cockpit uses.
            drop(up);
        }
    });
    // A one-sided upgrade would strand the socket. Refuse it clearly instead
    // of pretending the websocket is proxied. HTTP and SSE are proxied.
    let _ = (status, headers);
    text(
        StatusCode::NOT_IMPLEMENTED,
        "websocket upgrade is not proxied; the cockpit uses server-sent events",
    )
}

fn is_websocket(req: &Request<Incoming>) -> bool {
    req.headers()
        .get(header::UPGRADE)
        .and_then(|v| v.to_str().ok())
        .map(|v| v.eq_ignore_ascii_case("websocket"))
        .unwrap_or(false)
}

fn path_and_query(uri: &http::Uri) -> Result<String, ()> {
    let pq = uri.path_and_query().map(|p| p.as_str()).unwrap_or("/");
    if !pq.starts_with('/') || pq.starts_with("//") || pq.contains('\\') {
        return Err(());
    }
    Ok(pq.to_string())
}

fn copy_forward(req: &Request<Incoming>, out: &mut HeaderMap, ws: bool) {
    let connection_tokens = connection_tokens(req.headers());
    for (name, value) in req.headers().iter() {
        if hop(name) && !(ws && (name == header::UPGRADE || name == header::CONNECTION)) {
            continue;
        }
        if connection_tokens.iter().any(|t| t == name.as_str()) {
            continue;
        }
        if name == header::HOST || name == header::AUTHORIZATION || name == header::COOKIE {
            continue;
        }
        out.append(name, value.clone());
    }
}

fn copy_back(src: &HeaderMap, dst: &mut HeaderMap) {
    let connection_tokens = connection_tokens(src);
    for (name, value) in src.iter() {
        if hop(name) || name == header::SET_COOKIE || name == header::ACCESS_CONTROL_ALLOW_ORIGIN {
            continue;
        }
        if connection_tokens.iter().any(|t| t == name.as_str()) {
            continue;
        }
        dst.append(name, value.clone());
    }
}

fn connection_tokens(headers: &HeaderMap) -> Vec<String> {
    headers
        .get(header::CONNECTION)
        .and_then(|v| v.to_str().ok())
        .map(|v| {
            v.split(',')
                .map(|t| t.trim().to_ascii_lowercase())
                .filter(|t| !t.is_empty())
                .collect()
        })
        .unwrap_or_default()
}

fn hop(name: &HeaderName) -> bool {
    matches!(
        name.as_str(),
        "connection"
            | "keep-alive"
            | "proxy-authenticate"
            | "proxy-authorization"
            | "te"
            | "trailers"
            | "transfer-encoding"
            | "upgrade"
            | "proxy-connection"
    )
}

fn inject_chrome(html: &[u8], name: &str) -> Vec<u8> {
    let mut text = String::from_utf8_lossy(html).into_owned();
    if text.contains("/_desktop/chrome.js") {
        return text.into_bytes();
    }
    let snippet = format!(
        "<meta name=\"strike-desktop-gateway\" content=\"{}\"><link rel=\"stylesheet\" href=\"/_desktop/chrome.css\"><script src=\"/_desktop/chrome.js\" defer></script>",
        escape_attr(name)
    );
    if let Some(idx) = text.to_ascii_lowercase().find("</head>") {
        text.insert_str(idx, &snippet);
    } else {
        text.insert_str(0, &snippet);
    }
    text.into_bytes()
}

fn escape_attr(value: &str) -> String {
    let mut out = String::new();
    for ch in value.chars() {
        match ch {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&quot;"),
            c if c.is_control() => {}
            c => out.push(c),
        }
    }
    out
}

fn response(status: StatusCode, headers: HeaderMap, body: RespBody) -> Response<RespBody> {
    let mut resp = Response::new(body);
    *resp.status_mut() = status;
    *resp.headers_mut() = headers;
    resp.headers_mut()
        .insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
    resp.headers_mut()
        .remove(header::ACCESS_CONTROL_ALLOW_ORIGIN);
    resp
}

fn json_ok(value: serde_json::Value) -> Response<RespBody> {
    json_response(StatusCode::OK, value)
}

fn json_err(err: GatewayError) -> Response<RespBody> {
    let status = match err.code() {
        "not_found" => StatusCode::NOT_FOUND,
        "bad_token" | "needs_token" => StatusCode::UNAUTHORIZED,
        _ => StatusCode::UNPROCESSABLE_ENTITY,
    };
    json_response(status, err.to_json())
}

fn json_response(status: StatusCode, value: serde_json::Value) -> Response<RespBody> {
    Response::builder()
        .status(status)
        .header(header::CONTENT_TYPE, "application/json")
        .header(header::CACHE_CONTROL, "no-store")
        .header(header::X_CONTENT_TYPE_OPTIONS, "nosniff")
        .body(RespBody::full(value.to_string()))
        .unwrap()
}

fn text(status: StatusCode, msg: &str) -> Response<RespBody> {
    Response::builder()
        .status(status)
        .header(header::CONTENT_TYPE, "text/plain; charset=utf-8")
        .header(header::CACHE_CONTROL, "no-store")
        .body(RespBody::full(msg.to_string()))
        .unwrap()
}

fn html(status: StatusCode, msg: &str) -> Response<RespBody> {
    Response::builder()
        .status(status)
        .header(header::CONTENT_TYPE, "text/html; charset=utf-8")
        .header(header::CACHE_CONTROL, "no-store")
        .body(RespBody::full(format!(
            "<!doctype html><title>Strike</title><p>{msg}</p>"
        )))
        .unwrap()
}

#[derive(Deserialize)]
struct IdBody {
    id: String,
}

pub fn parse_id(body: &[u8]) -> Result<String, GatewayError> {
    let parsed: IdBody = serde_json::from_slice(body).map_err(|_| GatewayError::InvalidLink {
        detail: "expected JSON with an id".into(),
    })?;
    if parsed.id.is_empty() || parsed.id.len() > 80 {
        return Err(GatewayError::InvalidLink {
            detail: "missing id".into(),
        });
    }
    Ok(parsed.id)
}
