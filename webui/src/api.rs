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

pub async fn get_json<T: DeserializeOwned>(
    base: &str,
    token: &str,
    path: &str,
) -> Result<T, String> {
    let resp = Request::get(&api(base, path))
        .header("Authorization", &auth_header(token))
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
    let resp = Request::post(&api(base, path))
        .header("Authorization", &auth_header(token))
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
    let resp = Request::put(&api(base, path))
        .header("Authorization", &auth_header(token))
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

pub async fn delete_json(base: &str, token: &str, path: &str, body: Option<Value>) -> Result<(), String> {
    let req = Request::delete(&api(base, path)).header("Authorization", &auth_header(token));
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

pub async fn patch_json(base: &str, token: &str, path: &str, body: Value) -> Result<(), String> {
    let resp = Request::patch(&api(base, path))
        .header("Authorization", &auth_header(token))
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
    js_sys::encode_uri_component(s)
        .as_string()
        .unwrap_or_else(|| s.to_string())
}

pub fn now_ms() -> f64 {
    js_sys::Date::now()
}

pub fn idempotency_key(prefix: &str) -> String {
    format!("{prefix}-{}", now_ms() as u64)
}
