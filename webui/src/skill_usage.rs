//! Skill usage in Customize → Skills (#1442). The daemon counts runs from
//! recorded session logs (`GET /v1/customize/skill-usage`). This module only
//! turns one row's count into text. It never invents a number: with no
//! report, or a skill the report does not list, there is no usage line.

use std::collections::HashMap;

use serde::Deserialize;

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
#[serde(default, rename_all = "camelCase")]
pub struct Usage {
    pub count: u32,
    pub last_used: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
#[serde(default)]
pub struct Report {
    pub skills: HashMap<String, Usage>,
}

/// `YYYY-MM-DD` from an RFC 3339 timestamp, or empty when it is not one.
/// Go's zero time (`0001-01-01…`) also reads as empty.
fn day(stamp: &str) -> &str {
    let s = stamp.trim();
    if s.len() < 10 || s.starts_with("0001-") {
        return "";
    }
    let d = &s[..10];
    let b = d.as_bytes();
    let digits = |r: std::ops::Range<usize>| b[r].iter().all(u8::is_ascii_digit);
    if b[4] == b'-' && b[7] == b'-' && digits(0..4) && digits(5..7) && digits(8..10) {
        d
    } else {
        ""
    }
}

/// Row text: "Used 3 times · last 2026-11-05", "Used 1 time", or
/// "Never used". `None` when usage is unknown for this skill.
pub fn usage_line(report: Option<&Report>, name: &str) -> Option<String> {
    let usage = report?.skills.get(name)?;
    if usage.count == 0 {
        return Some("Never used".into());
    }
    let times = if usage.count == 1 {
        "1 time".to_string()
    } else {
        format!("{} times", usage.count)
    };
    let last = day(&usage.last_used);
    Some(if last.is_empty() {
        format!("Used {times}")
    } else {
        format!("Used {times} · last {last}")
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn report(raw: &str) -> Report {
        serde_json::from_str(raw).unwrap()
    }

    #[test]
    fn lines_for_used_never_and_unknown() {
        let r = report(
            r#"{"skills":{"review":{"count":3,"lastUsed":"2026-11-05T09:00:00Z"},"once":{"count":1,"lastUsed":"2026-11-01T00:00:00Z"},"idle":{"count":0,"lastUsed":"0001-01-01T00:00:00Z"},"odd":{"count":2,"lastUsed":"yesterday"}},"sessions":4}"#,
        );
        let cases = [
            ("review", Some("Used 3 times · last 2026-11-05")),
            ("once", Some("Used 1 time · last 2026-11-01")),
            ("idle", Some("Never used")),
            ("odd", Some("Used 2 times")),
            ("missing", None),
        ];
        for (name, want) in cases {
            assert_eq!(usage_line(Some(&r), name).as_deref(), want, "{name}");
        }
        assert_eq!(usage_line(None, "review"), None);
    }

    #[test]
    fn empty_history_reads_never_used() {
        let r = report(r#"{"skills":{"a":{"count":0}},"sessions":0}"#);
        assert_eq!(usage_line(Some(&r), "a").as_deref(), Some("Never used"));
    }
}
