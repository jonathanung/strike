//! Daemon HTTP helpers. Every call sends the bearer token. The live event
//! stream is same-origin EventSource (cookie handoff); JSON calls work for a
//! remote base URL too.

use gloo_net::http::Request;
use serde::de::DeserializeOwned;
use serde_json::Value;

pub fn api(base: &str, path: &str) -> String {
    format!("{}{}", base.trim_end_matches('/'), path)
}

pub fn auth_header(token: &str) -> String {
    format!("Bearer {token}")
}

fn with_auth(
    builder: gloo_net::http::RequestBuilder,
    token: &str,
) -> gloo_net::http::RequestBuilder {
    let builder = builder.credentials(web_sys::RequestCredentials::Include);
    let token = token.trim();
    if token.is_empty() {
        builder
    } else {
        builder.header("Authorization", &auth_header(token))
    }
}

pub async fn get_json<T: DeserializeOwned>(
    base: &str,
    token: &str,
    path: &str,
) -> Result<T, String> {
    let resp = with_auth(Request::get(&api(base, path)), token)
        .send()
        .await
        .map_err(|e| e.to_string())?;
    if resp.status() == 401 {
        return Err("unauthorized: check the daemon token".into());
    }
    if !resp.ok() {
        return Err(format!("HTTP {}", resp.status()));
    }
    resp.json::<T>().await.map_err(|e| e.to_string())
}

pub async fn post_json_value(
    base: &str,
    token: &str,
    path: &str,
    body: Value,
) -> Result<Value, String> {
    let resp = with_auth(Request::post(&api(base, path)), token)
        .json(&body)
        .map_err(|e| e.to_string())?
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = resp.status();
    let text = resp.text().await.unwrap_or_default();
    if !(200..300).contains(&status) {
        return Err(format!("HTTP {status} {text}"));
    }
    if text.trim().is_empty() {
        return Ok(Value::Null);
    }
    serde_json::from_str(&text).map_err(|e| e.to_string())
}

pub async fn put_json_value(
    base: &str,
    token: &str,
    path: &str,
    body: Value,
) -> Result<Value, String> {
    let resp = with_auth(Request::put(&api(base, path)), token)
        .json(&body)
        .map_err(|e| e.to_string())?
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = resp.status();
    let text = resp.text().await.unwrap_or_default();
    if !(200..300).contains(&status) {
        return Err(format!("HTTP {status} {text}"));
    }
    if text.trim().is_empty() {
        return Ok(Value::Null);
    }
    serde_json::from_str(&text).map_err(|e| e.to_string())
}

pub async fn post_json(base: &str, token: &str, path: &str, body: Value) -> Result<(), String> {
    post_json_value(base, token, path, body).await.map(|_| ())
}

pub async fn put_json(base: &str, token: &str, path: &str, body: Value) -> Result<String, String> {
    let resp = with_auth(Request::put(&api(base, path)), token)
        .header("Content-Type", "application/json")
        .body(body.to_string())
        .map_err(|e| e.to_string())?
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = resp.status();
    let text = resp.text().await.unwrap_or_default();
    if !(200..300).contains(&status) {
        return Err(format!("HTTP {status} {text}"));
    }
    Ok(text)
}

pub async fn delete_json(
    base: &str,
    token: &str,
    path: &str,
    body: Option<Value>,
) -> Result<(), String> {
    let req = with_auth(Request::delete(&api(base, path)), token);
    let resp = if let Some(body) = body {
        req.header("Content-Type", "application/json")
            .body(body.to_string())
            .map_err(|e| e.to_string())?
            .send()
            .await
    } else {
        req.send().await
    }
    .map_err(|e| e.to_string())?;
    let status = resp.status();
    if !(200..300).contains(&status) {
        let text = resp.text().await.unwrap_or_default();
        return Err(format!("HTTP {status} {text}"));
    }
    Ok(())
}

/// A failed DELETE, keeping the HTTP status beside the daemon's response
/// body so callers can tell a conflict (409) from other failures without
/// parsing the message. `status` is 0 when the request never got a
/// response (transport error); `body` then holds the transport message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DeleteError {
    pub status: u16,
    pub body: String,
}

impl std::fmt::Display for DeleteError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        if self.status == 0 {
            write!(f, "{}", self.body)
        } else if self.body.is_empty() {
            write!(f, "HTTP {}", self.status)
        } else {
            write!(f, "HTTP {} {}", self.status, self.body)
        }
    }
}

/// DELETE with no body, reporting failures as [`DeleteError`] so the
/// status survives. Success is any 2xx (the daemon answers 204).
pub async fn delete_with_status(base: &str, token: &str, path: &str) -> Result<(), DeleteError> {
    let resp = with_auth(Request::delete(&api(base, path)), token)
        .send()
        .await
        .map_err(|e| DeleteError {
            status: 0,
            body: e.to_string(),
        })?;
    let status = resp.status();
    if (200..300).contains(&status) {
        return Ok(());
    }
    let text = resp.text().await.unwrap_or_default();
    Err(DeleteError { status, body: text })
}

pub async fn get_text(base: &str, token: &str, path: &str) -> Result<String, String> {
    let resp = with_auth(Request::get(&api(base, path)), token)
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = resp.status();
    let text = resp.text().await.unwrap_or_default();
    if !(200..300).contains(&status) {
        let detail = if text.trim().is_empty() {
            String::new()
        } else {
            format!(" {text}")
        };
        return Err(format!("HTTP {status}{detail}"));
    }
    Ok(text)
}

pub async fn patch_json(base: &str, token: &str, path: &str, body: Value) -> Result<(), String> {
    let resp = with_auth(Request::patch(&api(base, path)), token)
        .json(&body)
        .map_err(|e| e.to_string())?
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = resp.status();
    if !(200..300).contains(&status) {
        let text = resp.text().await.unwrap_or_default();
        return Err(format!("HTTP {status} {text}"));
    }
    Ok(())
}

/// Submit one protocol op to the live root (`?root=`).
pub async fn post_op(
    base: &str,
    token: &str,
    root: &str,
    typ: &str,
    data: Value,
) -> Result<(), String> {
    let path = if root.is_empty() {
        "/v1/ops".to_string()
    } else {
        format!("/v1/ops?root={}", js_encode(root))
    };
    post_json(
        base,
        token,
        &path,
        serde_json::json!({"type": typ, "data": data}),
    )
    .await
}

pub fn js_encode(s: &str) -> String {
    #[cfg(target_arch = "wasm32")]
    {
        js_sys::encode_uri_component(s)
            .as_string()
            .unwrap_or_else(|| s.to_string())
    }
    #[cfg(not(target_arch = "wasm32"))]
    {
        // Host fallback (unit tests): js_sys panics off-wasm, so percent-
        // encode exactly like encodeURIComponent (unreserved set below).
        let mut out = String::with_capacity(s.len());
        for b in s.bytes() {
            match b {
                b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'!'
                | b'~' | b'*' | b'\'' | b'(' | b')' => out.push(b as char),
                _ => out.push_str(&format!("%{b:02X}")),
            }
        }
        out
    }
}

pub fn now_ms() -> f64 {
    js_sys::Date::now()
}

pub fn idempotency_key(prefix: &str) -> String {
    format!("{prefix}-{}", now_ms() as u64)
}
