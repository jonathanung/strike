//! Pure helpers for the cockpit's "Run with N models…" picker and the
//! comparison card (#1346, #1881). The daemon (`/v1/bestofn`) is the source
//! of truth for runs; this crate only parses what the user typed and words
//! what the daemon reports. Kept dependency-free so CI unit-tests it.

/// Fewest and most models one comparison runs (matches the daemon).
pub const MIN_MODELS: usize = 2;
pub const MAX_MODELS: usize = 4;

/// Effort levels the engine accepts on a child.
pub const EFFORTS: &[&str] = &["off", "low", "medium", "high", "xhigh", "max"];

/// One model to run, with an optional effort.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Pick {
    pub model: String,
    pub effort: String,
}

/// Parse the picker's model field: models separated by commas or new
/// lines, each `model` or `model@effort` (e.g. `anthropic/sonnet@high`).
pub fn parse_models(input: &str) -> Result<Vec<Pick>, String> {
    let mut out: Vec<Pick> = Vec::new();
    for raw in input.split(|c: char| c == ',' || c == '\n') {
        let item = raw.trim();
        if item.is_empty() {
            continue;
        }
        let (model, effort) = match item.rsplit_once('@') {
            Some((m, e)) => (m.trim(), e.trim().to_ascii_lowercase()),
            None => (item, String::new()),
        };
        if model.is_empty() {
            return Err(format!("\"{item}\" has no model before @"));
        }
        if !effort.is_empty() && !EFFORTS.contains(&effort.as_str()) {
            return Err(format!(
                "\"{effort}\" is not an effort level; use one of {}",
                EFFORTS.join(", ")
            ));
        }
        let pick = Pick { model: model.to_string(), effort };
        if out.contains(&pick) {
            return Err(format!("{} is listed twice", label(&pick.model, &pick.effort)));
        }
        out.push(pick);
    }
    if out.len() < MIN_MODELS || out.len() > MAX_MODELS {
        return Err(format!(
            "Pick {MIN_MODELS}–{MAX_MODELS} models, got {}.",
            out.len()
        ));
    }
    Ok(out)
}

/// Display name, e.g. `sonnet (high)`.
pub fn label(model: &str, effort: &str) -> String {
    if effort.is_empty() {
        model.to_string()
    } else {
        format!("{model} ({effort})")
    }
}

/// Parse an optional non-negative whole number field ("" means 0).
pub fn parse_count(input: &str, what: &str) -> Result<u32, String> {
    let s = input.trim();
    if s.is_empty() {
        return Ok(0);
    }
    s.parse::<u32>()
        .map_err(|_| format!("{what} must be a whole number of 0 or more"))
}

/// The up-front cost line the picker shows before anything runs.
pub fn cost_line(n: usize, max_turns: u32, timeout_min: u32) -> String {
    let mut out = format!("Runs {n} models: about {n}× the cost of one run.");
    match (max_turns, timeout_min) {
        (0, 0) => out.push_str(" No per-run budget."),
        (t, 0) => out.push_str(&format!(" Each run: up to {t} turns.")),
        (0, m) => out.push_str(&format!(" Each run: up to {m} min.")),
        (t, m) => out.push_str(&format!(" Each run: up to {t} turns, {m} min.")),
    }
    out
}

/// Short status word for a candidate row.
pub fn status_label(status: &str) -> &'static str {
    match status {
        "running" => "Running",
        "done" => "Done",
        "failed" => "Failed",
        "cancelled" => "Cancelled",
        _ => "Unknown",
    }
}

/// `1m 05s`, `42s`, `1h 02m`.
pub fn elapsed_label(ms: i64) -> String {
    let s = ms.max(0) / 1000;
    if s < 60 {
        format!("{s}s")
    } else if s < 3600 {
        format!("{}m {:02}s", s / 60, s % 60)
    } else {
        format!("{}h {:02}m", s / 3600, (s % 3600) / 60)
    }
}

/// `3 files · +10 −2`, or a plain sentence when nothing changed.
pub fn diffstat_label(files: i64, added: i64, removed: i64) -> String {
    if files <= 0 && added <= 0 && removed <= 0 {
        return "No file changes".into();
    }
    let noun = if files == 1 { "file" } else { "files" };
    format!("{files} {noun} · +{added} −{removed}")
}

/// Verification column: nothing when no command was configured.
pub fn verified_label(verified: Option<bool>) -> &'static str {
    match verified {
        Some(true) => "Verified",
        Some(false) => "Verification failed",
        None => "",
    }
}

/// A candidate can be adopted once every run stopped, nothing was decided
/// yet, and it finished successfully.
pub fn can_adopt(status: &str, complete: bool, decided: bool) -> bool {
    complete && !decided && status == "done"
}

/// Card headline for a run.
pub fn headline(complete: bool, keep: Option<&str>, decided: bool) -> String {
    match (decided, keep) {
        (true, Some(k)) if !k.is_empty() => format!("Adopted {k}"),
        (true, _) => "Kept none".into(),
        (false, _) if complete => "Pick a winner".into(),
        _ => "Comparing".into(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn pick(m: &str, e: &str) -> Pick {
        Pick { model: m.into(), effort: e.into() }
    }

    #[test]
    fn parses_models_and_efforts() {
        let got = parse_models("anthropic/sonnet@high, openai/gpt\n  local:qwen ").unwrap();
        assert_eq!(
            got,
            vec![pick("anthropic/sonnet", "high"), pick("openai/gpt", ""), pick("local:qwen", "")]
        );
        assert_eq!(parse_models("a@HIGH,b").unwrap()[0], pick("a", "high"));
    }

    #[test]
    fn rejects_bad_model_lists() {
        let cases = [
            ("", "Pick 2–4"),
            ("a", "Pick 2–4"),
            ("a,b,c,d,e", "Pick 2–4"),
            ("a,a", "listed twice"),
            ("a@high,a@high", "listed twice"),
            ("a@turbo,b", "not an effort"),
            ("@high,b", "no model"),
        ];
        for (input, want) in cases {
            let err = parse_models(input).unwrap_err();
            assert!(err.contains(want), "{input:?}: {err}");
        }
        // Same model at two efforts is two candidates.
        assert_eq!(parse_models("a@low,a@high").unwrap().len(), 2);
    }

    #[test]
    fn counts_and_cost_line() {
        assert_eq!(parse_count(" ", "Turns"), Ok(0));
        assert_eq!(parse_count("12", "Turns"), Ok(12));
        assert!(parse_count("-1", "Turns").is_err());
        assert!(parse_count("x", "Turns").is_err());
        assert_eq!(cost_line(3, 0, 0), "Runs 3 models: about 3× the cost of one run. No per-run budget.");
        assert!(cost_line(2, 20, 0).ends_with("up to 20 turns."));
        assert!(cost_line(2, 0, 10).ends_with("up to 10 min."));
        assert!(cost_line(4, 20, 10).ends_with("up to 20 turns, 10 min."));
    }

    #[test]
    fn words_for_the_card() {
        assert_eq!(label("a", ""), "a");
        assert_eq!(label("a", "high"), "a (high)");
        assert_eq!(status_label("cancelled"), "Cancelled");
        assert_eq!(status_label("weird"), "Unknown");
        assert_eq!(elapsed_label(-5), "0s");
        assert_eq!(elapsed_label(42_000), "42s");
        assert_eq!(elapsed_label(65_000), "1m 05s");
        assert_eq!(elapsed_label(3_720_000), "1h 02m");
        assert_eq!(diffstat_label(0, 0, 0), "No file changes");
        assert_eq!(diffstat_label(1, 3, 0), "1 file · +3 −0");
        assert_eq!(diffstat_label(3, 10, 2), "3 files · +10 −2");
        assert_eq!(verified_label(None), "");
        assert_eq!(verified_label(Some(false)), "Verification failed");
    }

    #[test]
    fn adopt_only_finished_after_everything_stopped() {
        assert!(can_adopt("done", true, false));
        assert!(!can_adopt("done", false, false));
        assert!(!can_adopt("done", true, true));
        assert!(!can_adopt("failed", true, false));
        assert!(!can_adopt("cancelled", true, false));
        assert_eq!(headline(false, None, false), "Comparing");
        assert_eq!(headline(true, None, false), "Pick a winner");
        assert_eq!(headline(true, Some("child-1"), true), "Adopted child-1");
        assert_eq!(headline(true, Some(""), true), "Kept none");
    }
}
