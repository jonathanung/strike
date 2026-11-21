//! MCP elicitation (#1353): the pure half of the elicitation card.
//!
//! A paused MCP tool call arrives as `elicitation.requested` with the
//! server's name, its message, and either a flat JSON Schema (form mode) or
//! a URL (url mode). The card answers with one `elicitation.reply`:
//! `accept` (form values as a JSON object), `decline`, or `cancel`. The
//! daemon closes every request with `elicitation.resolved`.
//!
//! Answers never become a chat message, and an answer never grants a
//! permission.

use serde_json::{json, Map, Value};

/// What a form field edits, from the MCP restricted schema subset.
#[derive(Clone, Debug, PartialEq)]
pub enum Kind {
    /// Free text; `format` is email | uri | date | date-time | "".
    Text { format: String, min_len: Option<usize>, max_len: Option<usize> },
    Number { integer: bool, min: Option<f64>, max: Option<f64> },
    Bool,
    /// Fixed choices as (value, label).
    Choice { options: Vec<(String, String)> },
}

#[derive(Clone, Debug, PartialEq)]
pub struct Field {
    pub name: String,
    pub title: String,
    pub description: String,
    pub required: bool,
    pub kind: Kind,
    /// Server-provided default, as the string the input starts with.
    pub default: String,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Pending {
    pub request_id: String,
    pub server: String,
    pub message: String,
    /// "form" or "url".
    pub mode: String,
    pub fields: Vec<Field>,
    pub url: String,
    /// Set when the card has been answered: accept | decline | cancel.
    pub settled: String,
}

impl Pending {
    pub fn is_url(&self) -> bool {
        self.mode == "url"
    }
}

#[derive(Debug, PartialEq)]
pub enum Note {
    Ask(Pending),
    Settle { request_id: String, action: String },
}

/// Elicitation notes carried by one live event envelope.
pub fn notes_from_envelope(raw: &str) -> Vec<Note> {
    let v: Value = match serde_json::from_str(raw) {
        Ok(v) => v,
        Err(_) => return Vec::new(),
    };
    let data = v.get("data").cloned().unwrap_or(Value::Null);
    match v.get("type").and_then(Value::as_str).unwrap_or("") {
        "elicitation.requested" => parse_pending(&data).map(Note::Ask).into_iter().collect(),
        "elicitation.resolved" => {
            let id = text(&data, "requestId");
            if id.is_empty() {
                return Vec::new();
            }
            vec![Note::Settle { request_id: id, action: normalize_action(&text(&data, "action")).into() }]
        }
        _ => Vec::new(),
    }
}

/// Settled cards kept on screen (newest last) so the outcome stays visible.
pub const SETTLED_KEEP: usize = 3;

/// Apply one note. A replayed request replaces its earlier copy; a settle
/// marks the card answered and trims old settled cards.
pub fn apply_note(list: &mut Vec<Pending>, note: Note) {
    match note {
        Note::Ask(p) => {
            list.retain(|row| row.request_id != p.request_id);
            list.push(p);
        }
        Note::Settle { request_id, action } => {
            if let Some(row) = list.iter_mut().find(|row| row.request_id == request_id) {
                row.settled = action;
            }
            let settled = list.iter().filter(|row| !row.settled.is_empty()).count();
            if settled > SETTLED_KEEP {
                let mut drop = settled - SETTLED_KEEP;
                list.retain(|row| {
                    if drop > 0 && !row.settled.is_empty() {
                        drop -= 1;
                        false
                    } else {
                        true
                    }
                });
            }
        }
    }
}

pub fn normalize_action(action: &str) -> &'static str {
    match action {
        "accept" => "accept",
        "decline" => "decline",
        _ => "cancel",
    }
}

/// Settled label shown on an answered card.
pub fn settled_label(action: &str) -> &'static str {
    match action {
        "accept" => "Sent",
        "decline" => "Declined",
        _ => "Cancelled",
    }
}

fn parse_pending(data: &Value) -> Option<Pending> {
    let request_id = text(data, "requestId");
    if request_id.is_empty() {
        return None;
    }
    let mode = if text(data, "mode") == "url" { "url" } else { "form" }.to_string();
    let url = if mode == "url" { text(data, "url") } else { String::new() };
    let fields = if mode == "form" {
        data.get("requestedSchema").map(parse_fields).unwrap_or_default()
    } else {
        Vec::new()
    };
    Some(Pending {
        request_id,
        server: text(data, "server"),
        message: text(data, "message"),
        mode,
        fields,
        url,
        settled: String::new(),
    })
}

/// Fields from a flat object schema, ordered by property name (serde_json
/// maps are sorted here, so the server's key order is not kept).
pub fn parse_fields(schema: &Value) -> Vec<Field> {
    let required: Vec<String> = schema
        .get("required")
        .and_then(Value::as_array)
        .map(|r| r.iter().filter_map(Value::as_str).map(str::to_string).collect())
        .unwrap_or_default();
    let Some(props) = schema.get("properties").and_then(Value::as_object) else {
        return Vec::new();
    };
    props
        .iter()
        .map(|(name, prop)| {
            let kind = field_kind(prop);
            let default = match (&kind, prop.get("default")) {
                (_, None) | (_, Some(Value::Null)) => String::new(),
                (Kind::Bool, Some(Value::Bool(b))) => b.to_string(),
                (_, Some(Value::String(s))) => s.clone(),
                (_, Some(Value::Number(n))) => n.to_string(),
                (_, Some(other)) => other.to_string(),
            };
            Field {
                name: name.clone(),
                title: text(prop, "title"),
                description: text(prop, "description"),
                required: required.iter().any(|r| r == name),
                kind,
                default,
            }
        })
        .collect()
}

fn field_kind(prop: &Value) -> Kind {
    if let Some(values) = prop.get("enum").and_then(Value::as_array) {
        let names: Vec<String> = prop
            .get("enumNames")
            .and_then(Value::as_array)
            .map(|n| n.iter().map(|v| v.as_str().unwrap_or_default().to_string()).collect())
            .unwrap_or_default();
        let options = values
            .iter()
            .enumerate()
            .filter_map(|(i, v)| {
                let value = v.as_str()?.to_string();
                let label = names.get(i).filter(|n| !n.is_empty()).cloned().unwrap_or_else(|| value.clone());
                Some((value, label))
            })
            .collect();
        return Kind::Choice { options };
    }
    if let Some(one_of) = prop.get("oneOf").and_then(Value::as_array) {
        let options: Vec<(String, String)> = one_of
            .iter()
            .filter_map(|o| {
                let value = o.get("const")?.as_str()?.to_string();
                let label = o.get("title").and_then(Value::as_str).filter(|t| !t.is_empty()).unwrap_or(&value).to_string();
                Some((value, label))
            })
            .collect();
        if !options.is_empty() {
            return Kind::Choice { options };
        }
    }
    let usize_of = |key: &str| prop.get(key).and_then(Value::as_u64).map(|n| n as usize);
    let f64_of = |key: &str| prop.get(key).and_then(Value::as_f64);
    match prop.get("type").and_then(Value::as_str).unwrap_or("string") {
        "boolean" => Kind::Bool,
        "integer" => Kind::Number { integer: true, min: f64_of("minimum"), max: f64_of("maximum") },
        "number" => Kind::Number { integer: false, min: f64_of("minimum"), max: f64_of("maximum") },
        _ => Kind::Text {
            format: text(prop, "format"),
            min_len: usize_of("minLength"),
            max_len: usize_of("maxLength"),
        },
    }
}

/// HTML input type for a text format hint.
pub fn input_type(format: &str) -> &'static str {
    match format {
        "email" => "email",
        "uri" => "url",
        "date" => "date",
        "date-time" => "datetime-local",
        _ => "text",
    }
}

/// Label for a field: its title, else its property name.
pub fn label(field: &Field) -> String {
    let base = if field.title.trim().is_empty() { field.name.clone() } else { field.title.clone() };
    if field.required {
        format!("{base} *")
    } else {
        base
    }
}

/// Initial input values: server defaults, booleans as "true"/"false".
pub fn initial_values(fields: &[Field]) -> Vec<String> {
    fields
        .iter()
        .map(|f| match f.kind {
            Kind::Bool => if f.default == "true" { "true" } else { "false" }.to_string(),
            _ => f.default.clone(),
        })
        .collect()
}

/// Build the accept content object, or the index and reason of the first
/// field that does not validate. Empty optional fields are omitted.
pub fn build_content(fields: &[Field], values: &[String]) -> Result<Value, (usize, String)> {
    let mut out = Map::new();
    for (i, f) in fields.iter().enumerate() {
        let raw = values.get(i).map(|s| s.trim().to_string()).unwrap_or_default();
        match &f.kind {
            Kind::Bool => {
                out.insert(f.name.clone(), Value::Bool(raw == "true"));
            }
            _ if raw.is_empty() => {
                if f.required {
                    return Err((i, format!("{} is required.", label_plain(f))));
                }
            }
            Kind::Text { format, min_len, max_len } => {
                let n = raw.chars().count();
                if min_len.is_some_and(|m| n < m) || max_len.is_some_and(|m| n > m) {
                    return Err((i, format!("{} has the wrong length.", label_plain(f))));
                }
                if format == "email" && !looks_like_email(&raw) {
                    return Err((i, format!("{} must be an email address.", label_plain(f))));
                }
                out.insert(f.name.clone(), Value::String(raw));
            }
            Kind::Number { integer, min, max } => {
                let Ok(n) = raw.parse::<f64>() else {
                    return Err((i, format!("{} must be a number.", label_plain(f))));
                };
                if *integer && n.fract() != 0.0 {
                    return Err((i, format!("{} must be a whole number.", label_plain(f))));
                }
                if min.is_some_and(|m| n < m) || max.is_some_and(|m| n > m) {
                    return Err((i, format!("{} is out of range.", label_plain(f))));
                }
                let value = if *integer { json!(n as i64) } else { json!(n) };
                out.insert(f.name.clone(), value);
            }
            Kind::Choice { options } => {
                if !options.iter().any(|(v, _)| *v == raw) {
                    return Err((i, format!("Pick one of the choices for {}.", label_plain(f))));
                }
                out.insert(f.name.clone(), Value::String(raw));
            }
        }
    }
    Ok(Value::Object(out))
}

fn label_plain(f: &Field) -> String {
    if f.title.trim().is_empty() { f.name.clone() } else { f.title.clone() }
}

fn looks_like_email(s: &str) -> bool {
    let mut parts = s.splitn(2, '@');
    let local = parts.next().unwrap_or_default();
    let domain = parts.next().unwrap_or_default();
    !local.is_empty() && domain.contains('.') && !domain.starts_with('.') && !s.contains(' ')
}

/// `elicitation.reply` op data. Content rides only on accept.
pub fn reply_data(request_id: &str, action: &str, content: Option<Value>) -> Value {
    let action = normalize_action(action);
    match content {
        Some(c) if action == "accept" => json!({"requestId": request_id, "action": action, "content": c}),
        _ => json!({"requestId": request_id, "action": action}),
    }
}

/// Only http(s) links get an Open button; anything else is shown as text.
pub fn openable_url(url: &str) -> bool {
    let lower = url.trim().to_ascii_lowercase();
    lower.starts_with("https://") || lower.starts_with("http://")
}

fn text(data: &Value, key: &str) -> String {
    data.get(key).and_then(Value::as_str).unwrap_or_default().to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    const FORM: &str = r#"{"type":"elicitation.requested","data":{"sessionId":"s1","requestId":"s1:elicit_1","server":"billing","message":"Who is this for?","mode":"form","requestedSchema":{"type":"object","properties":{
        "name":{"type":"string","title":"Name","default":"world","minLength":1},
        "email":{"type":"string","format":"email"},
        "seats":{"type":"integer","minimum":1,"maximum":10},
        "loud":{"type":"boolean","default":true},
        "color":{"type":"string","enum":["red","blue"],"enumNames":["Red","Blue"]},
        "plan":{"type":"string","oneOf":[{"const":"pro","title":"Pro"},{"const":"free"}]}
    },"required":["name","seats"]}}}"#;

    fn pending() -> Pending {
        match notes_from_envelope(FORM).pop() {
            Some(Note::Ask(p)) => p,
            other => panic!("{other:?}"),
        }
    }

    #[test]
    fn form_request_parses_fields_required_and_defaults() {
        let p = pending();
        assert_eq!(p.server, "billing");
        assert_eq!(p.mode, "form");
        let names: Vec<_> = p.fields.iter().map(|f| f.name.as_str()).collect();
        assert_eq!(names, ["color", "email", "loud", "name", "plan", "seats"]);
        assert!(p.fields[3].required && p.fields[5].required && !p.fields[1].required);
        assert_eq!(p.fields[3].default, "world");
        assert_eq!(label(&p.fields[3]), "Name *");
        assert_eq!(label(&p.fields[1]), "email");
        assert!(matches!(&p.fields[1].kind, Kind::Text { format, .. } if format == "email"));
        assert!(matches!(p.fields[5].kind, Kind::Number { integer: true, min: Some(_), max: Some(_) }));
        assert_eq!(p.fields[2].kind, Kind::Bool);
        assert_eq!(
            p.fields[0].kind,
            Kind::Choice { options: vec![("red".into(), "Red".into()), ("blue".into(), "Blue".into())] }
        );
        assert_eq!(
            p.fields[4].kind,
            Kind::Choice { options: vec![("pro".into(), "Pro".into()), ("free".into(), "free".into())] }
        );
        assert_eq!(initial_values(&p.fields), ["", "", "true", "world", "", ""]);
    }

    #[test]
    fn content_validates_and_types_values() {
        let p = pending();
        // Fields: color0 email1 loud2 name3 plan4 seats5.
        let mut v = initial_values(&p.fields);
        assert_eq!(build_content(&p.fields, &v).unwrap_err().0, 5, "seats is required");
        v[5] = "3.5".into();
        assert!(build_content(&p.fields, &v).unwrap_err().1.contains("whole number"));
        v[5] = "11".into();
        assert!(build_content(&p.fields, &v).unwrap_err().1.contains("out of range"));
        v[5] = "2".into();
        v[1] = "not-an-email".into();
        assert_eq!(build_content(&p.fields, &v).unwrap_err().0, 1);
        v[1] = "ada@example.com".into();
        v[0] = "green".into();
        assert_eq!(build_content(&p.fields, &v).unwrap_err().0, 0);
        v[0] = "blue".into();
        let content = build_content(&p.fields, &v).unwrap();
        assert_eq!(
            content,
            json!({"name": "world", "email": "ada@example.com", "seats": 2, "loud": true, "color": "blue"})
        );
        v[3] = "  ".into();
        assert_eq!(build_content(&p.fields, &v).unwrap_err().0, 3);
    }

    #[test]
    fn url_request_keeps_url_and_drops_schema() {
        let raw = r#"{"type":"elicitation.requested","data":{"requestId":"r2","server":"pay","message":"Confirm","mode":"url","url":"https://pay.test/c","requestedSchema":{"type":"object","properties":{"x":{"type":"string"}}}}}"#;
        let Some(Note::Ask(p)) = notes_from_envelope(raw).pop() else { panic!() };
        assert!(p.is_url());
        assert_eq!(p.url, "https://pay.test/c");
        assert!(p.fields.is_empty());
        assert!(openable_url(&p.url));
        assert!(!openable_url("javascript:alert(1)"));
        assert!(!openable_url("file:///etc/passwd"));
    }

    #[test]
    fn replies_keep_mcp_semantics() {
        assert_eq!(
            reply_data("r", "accept", Some(json!({"a": 1}))),
            json!({"requestId": "r", "action": "accept", "content": {"a": 1}})
        );
        assert_eq!(reply_data("r", "decline", Some(json!({"a": 1}))), json!({"requestId": "r", "action": "decline"}));
        assert_eq!(reply_data("r", "cancel", None), json!({"requestId": "r", "action": "cancel"}));
        assert_eq!(reply_data("r", "grant", None), json!({"requestId": "r", "action": "cancel"}));
    }

    #[test]
    fn settle_marks_card_and_trims_old_settled() {
        let mut list = Vec::new();
        for i in 0..5 {
            let raw = format!(r#"{{"type":"elicitation.requested","data":{{"requestId":"r{i}","server":"s","mode":"form"}}}}"#);
            for n in notes_from_envelope(&raw) {
                apply_note(&mut list, n);
            }
        }
        // A replayed request does not stack.
        for n in notes_from_envelope(r#"{"type":"elicitation.requested","data":{"requestId":"r4","server":"s"}}"#) {
            apply_note(&mut list, n);
        }
        assert_eq!(list.len(), 5);
        for i in 0..5 {
            let raw = format!(r#"{{"type":"elicitation.resolved","data":{{"requestId":"r{i}","action":"decline"}}}}"#);
            for n in notes_from_envelope(&raw) {
                apply_note(&mut list, n);
            }
        }
        assert_eq!(list.len(), SETTLED_KEEP);
        assert!(list.iter().all(|p| p.settled == "decline"));
        assert_eq!(list[0].request_id, "r2");
        assert_eq!(settled_label("decline"), "Declined");
        assert_eq!(settled_label("accept"), "Sent");
        assert_eq!(settled_label("other"), "Cancelled");
    }

    #[test]
    fn malformed_envelopes_are_ignored() {
        for raw in [
            "nope",
            r#"{"type":"elicitation.requested","data":{"server":"s"}}"#,
            r#"{"type":"elicitation.resolved","data":{"action":"accept"}}"#,
            r#"{"type":"question.asked","data":{"requestId":"q"}}"#,
        ] {
            assert!(notes_from_envelope(raw).is_empty(), "{raw}");
        }
        assert_eq!(input_type("email"), "email");
        assert_eq!(input_type("uri"), "url");
        assert_eq!(input_type("date-time"), "datetime-local");
        assert_eq!(input_type("weird"), "text");
    }
}
