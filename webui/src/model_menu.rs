//! Model menu rules. Pure data: no DOM. The chip view is `model_menu_view`.
//!
//! Capability flags on a catalog row are booleans. False means the daemon
//! did not report the capability (unknown or unsupported — #1054 does not
//! split those yet). This module never renders false as "unsupported".
//!
//! Context numbers come from the daemon. `contextLimit` is set when a fit
//! warning names the window. Catalog `context` is the model's listed window
//! and is used only when the daemon has not named a limit. Neither is invented
//! here. Send is held when the fit level is `critical`, or when a known
//! window is already full.

use serde::Deserialize;
use serde_json::Value;

use crate::model::ModelInfo;

/// Persisted global default from GET /v1/settings. Empty strings are unset.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct ModelDefaults {
    #[serde(default)]
    pub provider: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub effort: String,
}

/// Live model chrome reduced from bootstrap and from the event stream.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct LiveModelState {
    pub provider: String,
    pub model: String,
    pub effort: String,
    pub context_used: i64,
    pub context_limit: i64,
    pub context_fit: String,
    pub note: String,
    /// Epoch millis. Engine errors and note clears apply only to events at
    /// or after this instant, so a session replay cannot surface an old error.
    pub note_after: i64,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ContextView {
    pub used: i64,
    pub limit: i64,
    /// "daemon", "catalog", or "".
    pub limit_source: &'static str,
    /// "", "warn", or "critical".
    pub fit: &'static str,
    pub percent: i64,
    pub held: bool,
    pub warn: bool,
    pub summary: String,
    pub hold_message: String,
    pub warn_message: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct MenuRow {
    pub provider: String,
    pub id: String,
    pub label: String,
    pub detail: String,
    pub caps: String,
    pub current: bool,
    pub pinned: bool,
    pub key: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct MenuSection {
    pub title: String,
    pub rows: Vec<MenuRow>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BuiltMenu {
    pub sections: Vec<MenuSection>,
    pub flat: Vec<MenuRow>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct EffortChoice {
    pub label: String,
    pub detail: String,
    /// Protocol effort to send. Empty means the row is not selectable.
    pub level: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct EffortSection {
    pub shown: bool,
    pub reason: String,
    pub choices: Vec<EffortChoice>,
}

const EFFORTS: &[(&str, &str)] = &[
    (
        "off",
        "least reasoning the provider allows — fastest and cheapest",
    ),
    ("low", "minimal reasoning for short, scoped tasks"),
    ("medium", "balanced reasoning for routine work"),
    ("high", "thorough reasoning — the provider default"),
    (
        "xhigh",
        "deeper reasoning, best for coding and agentic work",
    ),
    ("max", "maximum reasoning when correctness beats cost"),
];

pub fn effort_levels() -> &'static [(&'static str, &'static str)] {
    EFFORTS
}

pub fn parse_effort(value: &str) -> Option<String> {
    let normalized = value.trim().to_ascii_lowercase();
    if normalized.is_empty() {
        return None;
    }
    EFFORTS
        .iter()
        .find(|(level, _)| *level == normalized)
        .map(|(level, _)| (*level).to_string())
}

pub fn format_tokens(n: i64) -> String {
    if n <= 0 {
        return "0".into();
    }
    if n < 1000 {
        return n.to_string();
    }
    if n < 10_000 {
        let whole = n / 1000;
        let tenth = (n % 1000) / 100;
        if tenth == 0 {
            return format!("{whole}k");
        }
        return format!("{whole}.{tenth}k");
    }
    if n < 1_000_000 {
        return format!("{}k", n / 1000);
    }
    let whole = n / 1_000_000;
    let tenth = (n % 1_000_000) / 100_000;
    if tenth == 0 {
        format!("{whole}M")
    } else {
        format!("{whole}.{tenth}M")
    }
}

pub fn format_cost(input: f64, output: f64) -> String {
    format!("{}/{}", format_usd(input), format_usd(output))
}

fn format_usd(v: f64) -> String {
    let mut s = format!("{v:.2}");
    while s.contains('.') && (s.ends_with('0') || s.ends_with('.')) {
        s.pop();
    }
    if s.is_empty() || s == "-" {
        s = "0".into();
    }
    format!("${s}")
}

pub fn model_label(info: &ModelInfo) -> String {
    let name = info.name.trim();
    if name.is_empty() {
        info.id.clone()
    } else {
        name.to_string()
    }
}

pub fn pin_key(provider: &str, id: &str) -> String {
    format!("{}/{}", provider.trim(), id.trim())
}

pub fn toggle_pin(pins: &mut Vec<String>, key: &str) {
    if let Some(i) = pins.iter().position(|p| p == key) {
        pins.remove(i);
    } else {
        pins.insert(0, key.to_string());
    }
}

/// "This session only" when the live pair differs from the saved default.
pub fn scope_label(
    provider: &str,
    model: &str,
    default_provider: &str,
    default_model: &str,
) -> &'static str {
    let dp = default_provider.trim();
    let dm = default_model.trim();
    if dp.is_empty() && dm.is_empty() {
        return "No global default saved";
    }
    if provider.trim() == dp && model.trim() == dm {
        "Matches the global default"
    } else {
        "This session only"
    }
}

pub fn chip_label(model: &str) -> String {
    let model = model.trim();
    if model.is_empty() {
        "Model".into()
    } else {
        format!("Model · {model}")
    }
}

pub fn capability_line(info: &ModelInfo) -> String {
    let mut parts: Vec<String> = Vec::new();
    if info.tool_call {
        parts.push("tools".into());
    }
    if info.reasoning {
        parts.push("reasoning".into());
    }
    if info.attachment {
        parts.push("attachments".into());
    }
    if !info.variant_ids.is_empty() {
        parts.push(format!(
            "{} variant{}",
            info.variant_ids.len(),
            if info.variant_ids.len() == 1 { "" } else { "s" }
        ));
    }
    if parts.is_empty() {
        "Capabilities not reported".into()
    } else {
        parts.join(" · ")
    }
}

fn detail_line(info: &ModelInfo) -> String {
    let mut parts: Vec<String> = Vec::new();
    let label = model_label(info);
    if label != info.id {
        parts.push(info.id.clone());
    }
    if info.context > 0 {
        parts.push(format!("{} context", format_tokens(info.context)));
    }
    if info.output > 0 {
        parts.push(format!("{} out", format_tokens(info.output)));
    }
    if info.has_cost {
        parts.push(format_cost(info.input_cost, info.output_cost));
    }
    parts.join(" · ")
}

pub fn active_context(models: &[ModelInfo], provider: &str, model: &str) -> i64 {
    let provider = provider.trim();
    let model = model.trim();
    if model.is_empty() {
        return 0;
    }
    if let Some(info) = models.iter().find(|m| {
        m.id == model && (m.provider.is_empty() || provider.is_empty() || m.provider == provider)
    }) {
        return info.context.max(0);
    }
    0
}

pub fn active_model<'a>(
    models: &'a [ModelInfo],
    provider: &str,
    model: &str,
) -> Option<&'a ModelInfo> {
    let provider = provider.trim();
    let model = model.trim();
    if model.is_empty() {
        return None;
    }
    models.iter().find(|m| {
        m.id == model && (m.provider.is_empty() || provider.is_empty() || m.provider == provider)
    })
}

fn normalize_fit(fit: &str) -> &'static str {
    match fit.trim() {
        "warn" => "warn",
        "critical" => "critical",
        _ => "",
    }
}

/// Meter and hold. `daemon_limit` wins over `catalog_context`. Hold when the
/// daemon said `critical`, or when used tokens already fill a known window.
pub fn context_view(used: i64, daemon_limit: i64, catalog_context: i64, fit: &str) -> ContextView {
    let used = used.max(0);
    let fit = normalize_fit(fit);
    let (limit, limit_source) = if daemon_limit > 0 {
        (daemon_limit, "daemon")
    } else if catalog_context > 0 {
        (catalog_context, "catalog")
    } else {
        (0, "")
    };
    let percent = if limit > 0 {
        ((used.saturating_mul(100)) / limit).clamp(0, 100)
    } else {
        0
    };
    let full = limit > 0 && used >= limit;
    let held = fit == "critical" || full;
    let warn = !held && (fit == "warn" || (limit > 0 && used > 0 && used * 5 >= limit * 4));
    let summary = if limit == 0 && used == 0 {
        "Context window not reported".into()
    } else if limit == 0 {
        format!(
            "Last turn used {} tokens. Window not reported.",
            format_tokens(used)
        )
    } else {
        let source = if limit_source == "daemon" {
            "daemon limit"
        } else {
            "catalog window"
        };
        format!(
            "{} / {} · {source}",
            format_tokens(used),
            format_tokens(limit)
        )
    };
    let hold_message = if !held {
        String::new()
    } else if fit == "critical" && limit > 0 {
        format!(
            "Send is held. The daemon projected the next turn as a critical fit ({} of {}).",
            format_tokens(used),
            format_tokens(limit)
        )
    } else if fit == "critical" {
        "Send is held. The daemon projected the next turn as a critical fit.".into()
    } else {
        format!(
            "Send is held. Last turn used {} of {} tokens, so the next turn will not fit.",
            format_tokens(used),
            format_tokens(limit)
        )
    };
    let warn_message = if !warn {
        String::new()
    } else if fit == "warn" {
        "Context is close to the window. Send stays available.".into()
    } else {
        "Last turn used 80% or more of the known window. Send stays available until the daemon says the next turn will not fit.".into()
    };
    ContextView {
        used,
        limit,
        limit_source,
        fit,
        percent,
        held,
        warn,
        summary,
        hold_message,
        warn_message,
    }
}

/// Why "Compact now" (#1567) cannot run, or `None` when it can. The
/// daemon rejects compaction while a turn runs, and a second send while
/// one is in flight would race the first — both states disable the
/// action with the reason visible instead of letting the click fail.
pub fn compact_disabled(busy: bool, in_flight: bool) -> Option<&'static str> {
    if in_flight {
        return Some("Compaction is already running.");
    }
    if busy {
        return Some("Unavailable while a turn is running.");
    }
    None
}

pub fn effort_section(info: Option<&ModelInfo>, current: &str) -> EffortSection {
    let current = current.trim();
    let reasoning = info.map(|i| i.reasoning).unwrap_or(false);
    let variants = info.map(|i| i.variant_ids.clone()).unwrap_or_default();
    let reported = reasoning || !variants.is_empty();
    if !reported && current.is_empty() {
        return EffortSection {
            shown: false,
            reason: "Reasoning support is not reported for this model. Effort stays at the provider default.".into(),
            choices: Vec::new(),
        };
    }
    let mut choices = Vec::new();
    // Published variants are the provider's effort set. The protocol ladder
    // is the fallback when the catalog only says "reasoning" (or the live
    // session already has an effort) and does not name variants.
    if variants.is_empty() {
        for (level, detail) in EFFORTS {
            choices.push(EffortChoice {
                label: (*level).into(),
                detail: (*detail).into(),
                level: (*level).into(),
            });
        }
    } else {
        for id in &variants {
            if let Some(level) = parse_effort(id) {
                choices.push(EffortChoice {
                    label: id.clone(),
                    detail: format!("variant → {level}"),
                    level,
                });
            } else {
                choices.push(EffortChoice {
                    label: id.clone(),
                    detail: "This variant does not report an effort level.".into(),
                    level: String::new(),
                });
            }
        }
    }
    let reason = if !reported {
        "The catalog does not report reasoning. This session already has an effort from the daemon.".into()
    } else {
        String::new()
    };
    EffortSection {
        shown: true,
        reason,
        choices,
    }
}

fn row_matches(info: &ModelInfo, query: &str) -> bool {
    if query.is_empty() {
        return true;
    }
    let id = info.id.to_ascii_lowercase();
    let name = info.name.to_ascii_lowercase();
    let provider = info.provider.to_ascii_lowercase();
    id.contains(query)
        || (!name.is_empty() && name.contains(query))
        || (!provider.is_empty() && provider.contains(query))
        || format!("{provider}/{id}").contains(query)
}

fn make_row(info: &ModelInfo, current_provider: &str, current_model: &str, pinned: bool) -> MenuRow {
    let provider = if info.provider.trim().is_empty() {
        current_provider.trim().to_string()
    } else {
        info.provider.clone()
    };
    let current = info.id == current_model
        && (current_provider.is_empty() || provider == current_provider || info.provider.is_empty());
    MenuRow {
        key: pin_key(&provider, &info.id),
        provider,
        id: info.id.clone(),
        label: model_label(info),
        detail: detail_line(info),
        caps: capability_line(info),
        current,
        pinned,
    }
}

pub fn build_menu(
    models: &[ModelInfo],
    filter: &str,
    pins: &[String],
    current_provider: &str,
    current_model: &str,
) -> BuiltMenu {
    let query = filter.trim().to_ascii_lowercase();
    let mut flat = Vec::new();
    let mut pinned_rows = Vec::new();
    for key in pins {
        let key = key.trim();
        if key.is_empty() {
            continue;
        }
        let (provider, id) = key.split_once('/').unwrap_or(("", key));
        if let Some(info) = models.iter().find(|m| {
            m.id == id && (provider.is_empty() || m.provider.is_empty() || m.provider == provider)
        }) {
            if !row_matches(info, &query) {
                continue;
            }
            pinned_rows.push(make_row(info, current_provider, current_model, true));
        } else if query.is_empty()
            || key.to_ascii_lowercase().contains(&query)
            || id.to_ascii_lowercase().contains(&query)
        {
            pinned_rows.push(MenuRow {
                provider: provider.to_string(),
                id: id.to_string(),
                label: key.to_string(),
                detail: String::new(),
                caps: "Not in the current catalog".into(),
                current: id == current_model && (provider.is_empty() || provider == current_provider),
                pinned: true,
                key: key.to_string(),
            });
        }
    }
    if !pinned_rows.is_empty() {
        flat.extend(pinned_rows.iter().cloned());
    }

    let mut sections: Vec<MenuSection> = Vec::new();
    if !pinned_rows.is_empty() {
        sections.push(MenuSection {
            title: "Pinned".into(),
            rows: pinned_rows,
        });
    }
    for info in models {
        if !row_matches(info, &query) {
            continue;
        }
        let provider = if info.provider.trim().is_empty() {
            current_provider.trim().to_string()
        } else {
            info.provider.clone()
        };
        let title = if provider.is_empty() {
            "Models".into()
        } else {
            provider.clone()
        };
        let row = make_row(info, current_provider, current_model, false);
        if let Some(section) = sections.iter_mut().find(|s| s.title == title) {
            section.rows.push(row.clone());
        } else {
            sections.push(MenuSection {
                title,
                rows: vec![row.clone()],
            });
        }
        flat.push(row);
    }
    BuiltMenu { sections, flat }
}

pub fn parse_freeform(raw: &str, fallback_provider: &str) -> (String, String) {
    let raw = raw.trim();
    if let Some((provider, model)) = raw.split_once('/') {
        let provider = provider.trim();
        let model = model.trim();
        if !provider.is_empty() && !model.is_empty() {
            return (provider.to_string(), model.to_string());
        }
    }
    (fallback_provider.trim().to_string(), raw.to_string())
}

fn event_relevant(message: &str) -> bool {
    let m = message.to_ascii_lowercase();
    m.contains("model") || m.contains("effort") || m.contains("provider")
}

fn under_warn_ratio(used: i64, limit: i64) -> bool {
    limit > 0 && used > 0 && used * 5 < limit * 4
}

/// Apply one SSE/JSONL envelope. Model fields follow the log. Notes from
/// `engine.error` apply only when `note_after` says we are waiting on a
/// change we just sent.
pub fn reduce_envelope(mut state: LiveModelState, raw: &str) -> LiveModelState {
    let v: Value = match serde_json::from_str(raw) {
        Ok(v) => v,
        Err(_) => return state,
    };
    let typ = v.get("type").and_then(|t| t.as_str()).unwrap_or("");
    let data = v.get("data").cloned().unwrap_or(Value::Null);
    let when = v
        .get("time")
        .and_then(|t| t.as_str())
        .and_then(rfc3339_millis);
    let fresh = match when {
        Some(t) => state.note_after > 0 && t + 15_000 >= state.note_after,
        None => state.note_after > 0,
    };
    match typ {
        "model.selected" => {
            if let Some(p) = text(&data, "provider") {
                if !p.is_empty() {
                    state.provider = p;
                }
            }
            if let Some(m) = text(&data, "model") {
                state.model = m;
            }
            if fresh {
                state.note.clear();
                state.note_after = 0;
            }
        }
        "effort.selected" => {
            if let Some(level) = text(&data, "level") {
                state.effort = level;
            }
            if fresh {
                state.note.clear();
                state.note_after = 0;
            }
        }
        "usage.reported" => {
            if let Some(n) = known_tokens(data.get("used")) {
                state.context_used = n;
                if under_warn_ratio(state.context_used, state.context_limit) {
                    state.context_fit.clear();
                }
            }
        }
        "context.fit_warning" => {
            if let Some(limit) = data.get("contextLimit").and_then(|v| v.as_i64()) {
                if limit > 0 {
                    state.context_limit = limit;
                }
            }
            if state.context_used == 0 {
                if let Some(est) = data.get("estimatedTokens").and_then(|v| v.as_i64()) {
                    if est > 0 {
                        state.context_used = est;
                    }
                }
            }
            if let Some(level) = text(&data, "level") {
                if level == "warn" || level == "critical" {
                    state.context_fit = level;
                }
            }
        }
        "compaction.completed" => {
            state.context_used = 0;
            state.context_fit.clear();
        }
        "engine.error" => {
            if fresh {
                if let Some(message) = text(&data, "message") {
                    if event_relevant(&message) {
                        state.note = message;
                    }
                }
                state.note_after = 0;
            }
        }
        _ => {}
    }
    state
}

fn text(data: &Value, key: &str) -> Option<String> {
    data.get(key).and_then(|v| v.as_str()).map(|s| s.to_string())
}

fn known_tokens(v: Option<&Value>) -> Option<i64> {
    let obj = v?.as_object()?;
    if obj.get("known").and_then(|k| k.as_bool()) != Some(true) {
        return None;
    }
    Some(obj.get("n").and_then(|n| n.as_i64()).unwrap_or(0))
}

pub fn rfc3339_millis(raw: &str) -> Option<i64> {
    let s = raw.trim();
    if s.len() < 20 {
        return None;
    }
    let bytes = s.as_bytes();
    if bytes[4] != b'-' || bytes[7] != b'-' || bytes[10] != b'T' || bytes[13] != b':' || bytes[16] != b':'
    {
        return None;
    }
    let year: i64 = s[0..4].parse().ok()?;
    let month: i64 = s[5..7].parse().ok()?;
    let day: i64 = s[8..10].parse().ok()?;
    let hour: i64 = s[11..13].parse().ok()?;
    let min: i64 = s[14..16].parse().ok()?;
    let sec: i64 = s[17..19].parse().ok()?;
    if !(0..=23).contains(&hour) || !(0..=59).contains(&min) || !(0..=60).contains(&sec) {
        return None;
    }
    let mut rest = &s[19..];
    let mut frac_ms: i64 = 0;
    if let Some(stripped) = rest.strip_prefix('.') {
        let digits: String = stripped.chars().take_while(|c| c.is_ascii_digit()).collect();
        if digits.is_empty() {
            return None;
        }
        rest = &stripped[digits.len()..];
        let mut padded = digits;
        while padded.len() < 3 {
            padded.push('0');
        }
        frac_ms = padded[..3].parse().ok()?;
    }
    let offset_ms = parse_offset(rest)?;
    let days = days_from_civil(year, month, day)?;
    Some(
        days * 86_400_000
            + hour * 3_600_000
            + min * 60_000
            + sec * 1000
            + frac_ms
            - offset_ms,
    )
}

fn parse_offset(s: &str) -> Option<i64> {
    if s == "Z" {
        return Some(0);
    }
    let (sign, rest) = if let Some(r) = s.strip_prefix('+') {
        (1, r)
    } else if let Some(r) = s.strip_prefix('-') {
        (-1, r)
    } else {
        return None;
    };
    let (hh, mm): (i64, i64) = if rest.len() >= 5 && rest.as_bytes()[2] == b':' {
        (rest[0..2].parse().ok()?, rest[3..5].parse().ok()?)
    } else if rest.len() >= 4 {
        (rest[0..2].parse().ok()?, rest[2..4].parse().ok()?)
    } else {
        return None;
    };
    if !(0..=23).contains(&hh) || !(0..=59).contains(&mm) {
        return None;
    }
    Some(sign * (hh * 3_600_000 + mm * 60_000))
}

fn days_from_civil(mut y: i64, m: i64, d: i64) -> Option<i64> {
    if !(1..=12).contains(&m) || !(1..=31).contains(&d) {
        return None;
    }
    if m <= 2 {
        y -= 1;
    }
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = y - era * 400;
    let mp = if m > 2 { m - 3 } else { m + 9 };
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    Some(era * 146097 + doe - 719468)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn chip_label_names_the_model() {
        assert_eq!(chip_label(""), "Model");
        assert_eq!(chip_label("  "), "Model");
        assert_eq!(chip_label("echo"), "Model · echo");
    }

    fn info(id: &str, provider: &str, name: &str) -> ModelInfo {
        ModelInfo {
            id: id.into(),
            provider: provider.into(),
            name: name.into(),
            context: 0,
            output: 0,
            input_cost: 0.0,
            output_cost: 0.0,
            has_cost: false,
            tool_call: false,
            reasoning: false,
            attachment: false,
            variant_ids: Vec::new(),
            source: String::new(),
        }
    }

    #[test]
    fn filter_groups_pins_and_freeform() {
        let mut echo = info("echo", "echo", "Echo");
        echo.context = 32;
        echo.tool_call = true;
        let mut sonnet = info("claude-sonnet-5", "anthropic", "Sonnet");
        sonnet.reasoning = true;
        sonnet.context = 200_000;
        sonnet.has_cost = true;
        sonnet.input_cost = 2.5;
        sonnet.output_cost = 10.0;
        let models = vec![echo, sonnet];

        let menu = build_menu(&models, "son", &[], "echo", "echo");
        assert_eq!(menu.flat.len(), 1);
        assert_eq!(menu.flat[0].id, "claude-sonnet-5");
        assert!(menu.flat[0].caps.contains("reasoning"));
        assert!(!menu.flat[0].caps.contains("not reported"));
        assert!(menu.flat[0].detail.contains("200k context"));
        assert!(menu.flat[0].detail.contains("$2.5/$10"));

        let mut pins = Vec::new();
        toggle_pin(&mut pins, "echo/echo");
        toggle_pin(&mut pins, "echo/echo");
        assert!(pins.is_empty());
        toggle_pin(&mut pins, "echo/echo");
        let menu = build_menu(&models, "", &pins, "echo", "echo");
        assert_eq!(menu.sections[0].title, "Pinned");
        assert!(menu.flat[0].pinned && menu.flat[0].current);
        assert_eq!(menu.sections[1].title, "echo");
        assert_eq!(menu.sections[2].title, "anthropic");

        let bare = info("echo", "echo", "");
        assert_eq!(capability_line(&bare), "Capabilities not reported");
        assert_eq!(parse_freeform("nope/missing", "echo"), ("nope".into(), "missing".into()));
        assert_eq!(parse_freeform("custom-id", "echo"), ("echo".into(), "custom-id".into()));
        assert_eq!(scope_label("echo", "echo", "", ""), "No global default saved");
        assert_eq!(scope_label("echo", "echo", "echo", "echo"), "Matches the global default");
        assert_eq!(scope_label("echo", "other", "echo", "echo"), "This session only");
    }

    #[test]
    fn compact_now_disabled_only_while_busy_or_in_flight() {
        assert_eq!(compact_disabled(false, false), None);
        assert_eq!(
            compact_disabled(true, false),
            Some("Unavailable while a turn is running.")
        );
        assert_eq!(
            compact_disabled(false, true),
            Some("Compaction is already running.")
        );
        // In flight wins: it is the more specific reason.
        assert_eq!(
            compact_disabled(true, true),
            Some("Compaction is already running.")
        );
    }

    #[test]
    fn effort_hides_until_the_catalog_or_daemon_reports_it() {
        let plain = info("echo", "echo", "Echo");
        let hidden = effort_section(Some(&plain), "");
        assert!(!hidden.shown);
        assert!(hidden.reason.contains("not reported"));

        let shown = effort_section(Some(&plain), "high");
        assert!(shown.shown);
        assert!(shown.reason.contains("already has an effort"));
        assert!(shown.choices.iter().any(|c| c.level == "high"));

        let mut reasoned = plain.clone();
        reasoned.reasoning = true;
        reasoned.variant_ids = vec!["low".into(), "nightly".into()];
        let section = effort_section(Some(&reasoned), "");
        assert!(section.shown);
        assert_eq!(section.choices.len(), 2);
        assert_eq!(section.choices[0].level, "low");
        assert_eq!(section.choices[1].level, "");
        assert!(section.choices[1].detail.contains("does not report"));
        assert!(section.choices.iter().all(|c| c.level != "max"));
        assert!(parse_effort(" Max ").is_some());
        assert!(parse_effort("turbo").is_none());
    }

    #[test]
    fn context_hold_uses_daemon_numbers_only() {
        let unknown = context_view(0, 0, 0, "");
        assert!(!unknown.held);
        assert!(!unknown.warn);
        assert_eq!(unknown.summary, "Context window not reported");

        let catalog = context_view(0, 0, 32, "");
        assert_eq!(catalog.limit_source, "catalog");
        assert!(!catalog.held);
        assert_eq!(catalog.summary, "0 / 32 · catalog window");

        let warn = context_view(26, 0, 32, "");
        assert!(warn.warn && !warn.held);
        assert!(warn.warn_message.contains("80%"));

        let daemon_wins = context_view(1500, 200_000, 32, "warn");
        assert_eq!(daemon_wins.limit_source, "daemon");
        assert_eq!(daemon_wins.limit, 200_000);
        assert!(daemon_wins.warn && !daemon_wins.held);

        let critical = context_view(180_000, 200_000, 32, "critical");
        assert!(critical.held);
        assert!(critical.hold_message.contains("critical"));

        let full = context_view(32, 0, 32, "");
        assert!(full.held);
        assert!(full.hold_message.contains("will not fit"));

        assert_eq!(format_tokens(1500), "1.5k");
        assert_eq!(format_tokens(128_000), "128k");
        assert_eq!(active_context(&[info("echo", "echo", "")], "echo", "other"), 0);
    }

    #[test]
    fn reducer_tracks_fit_and_ignores_replayed_errors() {
        let mut state = LiveModelState::default();
        state = reduce_envelope(
            state,
            r#"{"type":"usage.reported","data":{"used":{"n":1500,"known":true}}}"#,
        );
        assert_eq!(state.context_used, 1500);
        state = reduce_envelope(
            state,
            r#"{"type":"context.fit_warning","data":{"contextLimit":200000,"estimatedTokens":180000,"level":"warn"}}"#,
        );
        assert_eq!(state.context_limit, 200_000);
        assert_eq!(state.context_used, 1500);
        assert_eq!(state.context_fit, "warn");

        state = reduce_envelope(
            state,
            r#"{"type":"usage.reported","data":{"used":{"n":1000,"known":true}}}"#,
        );
        assert_eq!(state.context_fit, "");

        state = reduce_envelope(
            state,
            r#"{"type":"context.fit_warning","data":{"contextLimit":200000,"level":"critical"}}"#,
        );
        state = reduce_envelope(state, r#"{"type":"compaction.completed","data":{}}"#);
        assert_eq!(state.context_used, 0);
        assert_eq!(state.context_fit, "");
        assert_eq!(state.context_limit, 200_000);

        let old = r#"{"type":"engine.error","time":"2020-01-01T00:00:00Z","data":{"message":"unknown provider \"nope\""}}"#;
        state.note_after = rfc3339_millis("2026-10-26T12:00:00Z").unwrap();
        state = reduce_envelope(state, old);
        assert!(state.note.is_empty(), "replayed error must not surface");

        let live = r#"{"type":"engine.error","time":"2026-10-26T12:00:01Z","data":{"message":"unknown provider \"nope\""}}"#;
        state = reduce_envelope(state, live);
        assert!(state.note.contains("unknown provider"));
        assert_eq!(state.note_after, 0);

        state.note_after = rfc3339_millis("2026-10-26T12:00:00Z").unwrap();
        state.note = "keep me".into();
        state = reduce_envelope(
            state,
            r#"{"type":"model.selected","time":"2020-01-01T00:00:00Z","data":{"provider":"echo","model":"echo"}}"#,
        );
        assert_eq!(state.model, "echo");
        assert_eq!(state.note, "keep me");
        state = reduce_envelope(
            state,
            r#"{"type":"model.selected","time":"2026-10-26T12:00:02Z","data":{"provider":"echo","model":"echo-2"}}"#,
        );
        assert_eq!(state.model, "echo-2");
        assert!(state.note.is_empty());
    }

    #[test]
    fn catalog_json_keeps_reported_capabilities() {
        let raw = r#"{"id":"echo","provider":"echo","name":"Echo","context":32,"toolCall":true,"variantIds":["low"]}"#;
        let info: ModelInfo = serde_json::from_str(raw).unwrap();
        assert_eq!(info.context, 32);
        assert!(info.tool_call);
        assert!(!info.reasoning);
        assert_eq!(info.variant_ids, vec!["low".to_string()]);
        assert_eq!(rfc3339_millis("1970-01-01T00:00:00Z"), Some(0));
        let defaults: ModelDefaults =
            serde_json::from_str(r#"{"provider":"echo","model":"echo","effort":"low"}"#).unwrap();
        assert_eq!(defaults.effort, "low");
    }
}
