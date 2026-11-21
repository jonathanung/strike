//! Dashboard page rendering rules (#1628).
//!
//! The daemon fills measured blocks and labels every block's provenance
//! (`measured` or `agent`). This crate owns the cockpit-side rules that
//! keep that honest on screen: an unmeasured metric renders as a dash —
//! never `0` — and anything not explicitly `measured` carries the
//! "agent wrote this" marker. Pure; CI runs
//! `cargo test --manifest-path webui/dashboard-view/Cargo.toml`.

/// Marker on every agent-written block.
pub const AGENT_MARK: &str = "agent wrote this";
/// Text for a metric nobody measured.
pub const DASH: &str = "\u{2014}";
/// Templates the cockpit can start a page from: id and label.
pub const TEMPLATES: [(&str, &str); 3] = [
    ("project_report", "Project report"),
    ("standup", "Standup"),
    ("kanban", "Kanban"),
];

/// True unless the daemon said `measured`. Unknown provenance is treated
/// as agent-written: the safe side is to over-label, not under-label.
pub fn is_agent(provenance: &str) -> bool {
    provenance.trim() != "measured"
}

/// Display text for a metric block.
pub fn metric_text(value: Option<f64>, measured: bool, unit: &str) -> String {
    let Some(v) = value.filter(|v| measured && v.is_finite()) else {
        return DASH.to_string();
    };
    if unit == "usd" {
        return format!("${v:.2}");
    }
    if v.fract() == 0.0 && v.abs() < 1e15 {
        format!("{}", v as i64)
    } else {
        format!("{v:.2}")
    }
}

/// Placeholder for an agent block with nothing written yet.
pub fn empty_agent_text() -> &'static str {
    "Not written yet. Ask the agent in chat to fill this in."
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn unmeasured_is_a_dash_never_zero() {
        let cases = [
            (None, false, ""),
            (None, true, ""),
            (Some(0.0), false, ""),
            (Some(5.0), false, "usd"),
            (Some(f64::NAN), true, ""),
        ];
        for (value, measured, unit) in cases {
            assert_eq!(metric_text(value, measured, unit), DASH, "{value:?} {measured} {unit}");
        }
    }

    #[test]
    fn measured_values_format() {
        assert_eq!(metric_text(Some(0.0), true, ""), "0", "a measured zero is a real zero");
        assert_eq!(metric_text(Some(42.0), true, ""), "42");
        assert_eq!(metric_text(Some(2.5), true, ""), "2.50");
        assert_eq!(metric_text(Some(1.234), true, "usd"), "$1.23");
    }

    #[test]
    fn provenance_marker_defaults_to_agent() {
        assert!(!is_agent("measured"));
        assert!(!is_agent(" measured "));
        assert!(is_agent("agent"));
        assert!(is_agent(""));
        assert!(is_agent("Measured"), "only the exact daemon label counts as measured");
    }

    #[test]
    fn templates_are_the_three_v1_pages() {
        let ids: Vec<&str> = TEMPLATES.iter().map(|(id, _)| *id).collect();
        assert_eq!(ids, vec!["project_report", "standup", "kanban"]);
        assert!(!empty_agent_text().is_empty());
        assert!(!AGENT_MARK.is_empty());
    }
}
