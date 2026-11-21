//! Debug mode panel rules (#1345).
//!
//! The daemon owns the debug session: the phase gate, hypotheses, evidence
//! and the instrumentation sweep are engine state, reported on every
//! `debug.updated` event. This crate only decides how the cockpit shows
//! that state: how an update merges into what is on screen, the reproduce
//! checklist, which actions are offered, how typed hypotheses become the
//! op's list, and the closing summary.
//!
//! Pure: no Leptos, no serde. The cockpit maps `debug.updated` onto
//! [`State`]; CI runs these tests with
//! `cargo test --manifest-path webui/debug-mode/Cargo.toml`.

/// Instrumentation marker the daemon's sweep searches for. Built by
/// concatenation so this source never contains the marker itself.
pub fn marker() -> String {
    ["STRIKE", "-DEBUG"].concat()
}

pub const PHASE_HYPOTHESIS: &str = "hypothesis";
pub const PHASE_FIX: &str = "fix";
pub const PHASE_CLOSED: &str = "closed";

pub const MIN_HYPOTHESES: usize = 2;
pub const MAX_HYPOTHESES: usize = 5;

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Evidence {
    pub source: String,
    pub text: String,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Hypothesis {
    pub id: String,
    pub rank: i64,
    pub statement: String,
    pub confirms: String,
    pub refutes: String,
    /// open | confirmed | refuted
    pub status: String,
    pub evidence: Vec<Evidence>,
}

/// One tagged instrumentation line on disk.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Hit {
    pub path: String,
    pub line: i64,
    pub text: String,
}

/// One `debug.updated` payload, or the merged state on screen.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct State {
    pub action: String,
    /// hypothesis | fix | closed; empty means no session.
    pub phase: String,
    pub hypotheses: Vec<Hypothesis>,
    pub override_reason: String,
    pub fixed: bool,
    pub instrumentation: Vec<Hit>,
    pub removed: Vec<Hit>,
    pub error: String,
}

impl State {
    /// A session is running (the gate may be holding edits).
    pub fn active(&self) -> bool {
        self.phase == PHASE_HYPOTHESIS || self.phase == PHASE_FIX
    }

    pub fn closed(&self) -> bool {
        self.phase == PHASE_CLOSED
    }

    pub fn has_confirmed(&self) -> bool {
        self.hypotheses.iter().any(|h| h.status == "confirmed")
    }

    pub fn has_evidence(&self) -> bool {
        self.hypotheses.iter().any(|h| !h.evidence.is_empty())
    }
}

/// Actions whose `debug.updated` carries a fresh instrumentation scan.
fn scans(action: &str) -> bool {
    matches!(action, "start" | "scan" | "fixed" | "end")
}

/// Merge one update into what is shown.
///
/// A refusal (`error` set) leaves the session as it was and shows the
/// error; a refused end still reports what the sweep removed and what
/// remains. A success replaces the state, keeping the previous
/// instrumentation list when the action did not rescan.
pub fn apply(prev: Option<State>, next: State) -> Option<State> {
    if !next.error.is_empty() {
        let mut cur = prev.unwrap_or_default();
        if next.phase.is_empty() && !cur.active() {
            // No session on either side: show the error alone.
            cur = State::default();
        }
        cur.action = next.action.clone();
        cur.error = next.error;
        if scans(&next.action) {
            cur.instrumentation = next.instrumentation;
            if next.action == "end" {
                cur.removed = next.removed;
            }
        }
        return Some(cur);
    }
    let mut next = next;
    if !scans(&next.action) {
        if let Some(p) = prev {
            next.instrumentation = p.instrumentation;
        }
    }
    Some(next)
}

/// Phase label for the status chip.
pub fn phase_label(phase: &str) -> &'static str {
    match phase {
        PHASE_HYPOTHESIS => "Hypothesis",
        PHASE_FIX => "Fix",
        PHASE_CLOSED => "Closed",
        _ => "Off",
    }
}

/// Status chip text, e.g. `Debug · Hypothesis · 2 tagged`.
pub fn chip_text(state: &State) -> String {
    let mut out = format!("Debug · {}", phase_label(&state.phase));
    if state.closed() {
        out.push_str(&format!(" · {} swept", state.removed.len()));
    } else {
        out.push_str(&format!(" · {} tagged", state.instrumentation.len()));
    }
    out
}

/// One reproduce-checklist row.
#[derive(Clone, Debug, PartialEq)]
pub struct Step {
    pub label: &'static str,
    pub done: bool,
}

/// The reproduce checklist, in order.
pub fn checklist(state: &State) -> Vec<Step> {
    let unlocked = state.phase == PHASE_FIX || state.closed();
    let settled = state.has_confirmed()
        || (!state.hypotheses.is_empty() && state.hypotheses.iter().all(|h| h.status != "open"));
    vec![
        Step { label: "State 2–5 ranked hypotheses", done: state.hypotheses.len() >= MIN_HYPOTHESES },
        Step { label: "Reproduce the bug and attach the output", done: state.has_evidence() },
        Step { label: "Confirm or refute hypotheses", done: settled },
        Step { label: "Unlock fix (confirmed hypothesis or override)", done: unlocked },
        Step { label: "Mark the bug fixed", done: state.fixed },
        Step { label: "End: sweep instrumentation, zero left", done: state.closed() && state.instrumentation.is_empty() },
    ]
}

/// Panel actions the daemon would accept in this state.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Action {
    Start,
    SetHypotheses,
    Evidence,
    Mark,
    Unlock,
    Override,
    MarkFixed,
    End,
    Scan,
}

/// Whether to offer an action. The daemon re-checks every one.
pub fn can(state: Option<&State>, action: Action) -> bool {
    let Some(s) = state.filter(|s| s.active()) else {
        return action == Action::Start;
    };
    let hyp = s.phase == PHASE_HYPOTHESIS;
    match action {
        Action::Start => false,
        Action::SetHypotheses => hyp,
        Action::Evidence | Action::Mark => !s.hypotheses.is_empty(),
        Action::Unlock => hyp && s.has_confirmed(),
        Action::Override => hyp,
        Action::MarkFixed => s.phase == PHASE_FIX && !s.fixed,
        Action::End => s.phase == PHASE_FIX && s.fixed,
        Action::Scan => true,
    }
}

/// One hypothesis as typed: `statement | confirms | refutes`, one per line.
#[derive(Clone, Debug, PartialEq)]
pub struct Draft {
    pub id: String,
    pub statement: String,
    pub confirms: String,
    pub refutes: String,
}

/// Parse the hypothesis box. Blank lines are skipped; ids are h1..hN in
/// rank order.
pub fn parse_hypotheses(text: &str) -> Result<Vec<Draft>, String> {
    let mut out = Vec::new();
    for line in text.lines() {
        let line = line.trim();
        if line.is_empty() {
            continue;
        }
        let mut parts = line.splitn(3, '|').map(str::trim);
        let statement = parts.next().unwrap_or_default().to_string();
        if statement.is_empty() {
            return Err(format!("Hypothesis {} has no statement.", out.len() + 1));
        }
        out.push(Draft {
            id: format!("h{}", out.len() + 1),
            statement,
            confirms: parts.next().unwrap_or_default().to_string(),
            refutes: parts.next().unwrap_or_default().to_string(),
        });
    }
    if out.len() < MIN_HYPOTHESES || out.len() > MAX_HYPOTHESES {
        return Err(format!(
            "Write {MIN_HYPOTHESES}–{MAX_HYPOTHESES} hypotheses, one per line (got {}).",
            out.len()
        ));
    }
    Ok(out)
}

/// The closing summary that stays in the transcript.
#[derive(Clone, Debug, PartialEq)]
pub struct Summary {
    pub root_cause: Vec<String>,
    pub evidence: usize,
    pub override_reason: String,
    pub swept: usize,
    pub remaining: usize,
    /// Cleanup verified: the session closed with zero tagged lines left.
    pub verified: bool,
}

pub fn summary(state: &State) -> Summary {
    Summary {
        root_cause: state
            .hypotheses
            .iter()
            .filter(|h| h.status == "confirmed")
            .map(|h| h.statement.clone())
            .collect(),
        evidence: state.hypotheses.iter().map(|h| h.evidence.len()).sum(),
        override_reason: state.override_reason.clone(),
        swept: state.removed.len(),
        remaining: state.instrumentation.len(),
        verified: state.closed() && state.instrumentation.is_empty(),
    }
}

/// Transcript line `(title, body)` for an update that should stay in the
/// transcript: start, fix unlock (naming the override reason or the
/// confirmed hypotheses), mark fixed, and the closing summary. Refusals and
/// routine edits stay in the panel.
pub fn transcript_note(s: &State) -> Option<(String, String)> {
    if !s.error.is_empty() {
        return None;
    }
    let confirmed: Vec<&str> = s
        .hypotheses
        .iter()
        .filter(|h| h.status == "confirmed")
        .map(|h| h.statement.as_str())
        .collect();
    match s.action.as_str() {
        "start" => Some((
            "Debug session started".into(),
            format!(
                "Hypothesis phase: only edits tagged {} are allowed until a hypothesis is confirmed with evidence or you override.",
                marker()
            ),
        )),
        "unlock" if !s.override_reason.is_empty() => Some((
            "Fix unlocked by override".into(),
            format!("No hypothesis was confirmed. Override reason: {}", s.override_reason),
        )),
        "unlock" => Some(("Fix unlocked".into(), format!("Confirmed: {}", confirmed.join("; ")))),
        "fixed" => Some((
            "Bug marked fixed".into(),
            "Ending the debug session sweeps tagged instrumentation.".into(),
        )),
        "end" if s.closed() => {
            let sum = summary(s);
            let cause = if sum.root_cause.is_empty() {
                format!("Override: {}", sum.override_reason)
            } else {
                format!("Root cause: {}", sum.root_cause.join("; "))
            };
            Some((
                "Debug session closed".into(),
                format!(
                    "{cause}. Evidence items: {}. Swept {} tagged line(s); {} remain{}.",
                    sum.evidence,
                    sum.swept,
                    sum.remaining,
                    if sum.verified { " (cleanup verified)" } else { "" }
                ),
            ))
        }
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn hyp(id: &str, status: &str, evidence: usize) -> Hypothesis {
        Hypothesis {
            id: id.into(),
            statement: format!("cause {id}"),
            status: status.into(),
            evidence: (0..evidence).map(|i| Evidence { source: "repro".into(), text: format!("out {i}") }).collect(),
            ..Hypothesis::default()
        }
    }

    fn hit(line: i64) -> Hit {
        Hit { path: "a.go".into(), line, text: format!("probe {}", marker()) }
    }

    fn state(phase: &str) -> State {
        State { phase: phase.into(), ..State::default() }
    }

    #[test]
    fn marker_matches_daemon() {
        assert!(marker().starts_with("STRIKE"));
        assert!(marker().ends_with("-DEBUG"));
        assert_eq!(marker().len(), 12);
        assert!(!include_str!("lib.rs").contains(&format!("\"{}\"", marker())));
    }

    #[test]
    fn apply_success_replaces_and_keeps_scan_when_not_rescanned() {
        let start = State { action: "start".into(), instrumentation: vec![hit(2)], ..state(PHASE_HYPOTHESIS) };
        let cur = apply(None, start).unwrap();
        assert_eq!(cur.instrumentation.len(), 1);

        let set = State { action: "hypotheses".into(), hypotheses: vec![hyp("h1", "open", 0), hyp("h2", "open", 0)], ..state(PHASE_HYPOTHESIS) };
        let cur = apply(Some(cur), set).unwrap();
        assert_eq!(cur.hypotheses.len(), 2);
        assert_eq!(cur.instrumentation.len(), 1, "non-scan action keeps the last scan");

        let scan = State { action: "scan".into(), hypotheses: cur.hypotheses.clone(), ..state(PHASE_HYPOTHESIS) };
        let cur = apply(Some(cur), scan).unwrap();
        assert!(cur.instrumentation.is_empty(), "scan replaces the list, even with zero hits");
    }

    #[test]
    fn apply_refusal_keeps_session_and_shows_error() {
        let prev = State { action: "hypotheses".into(), hypotheses: vec![hyp("h1", "open", 0)], instrumentation: vec![hit(3)], ..state(PHASE_HYPOTHESIS) };
        let refused = State { action: "unlock".into(), error: "no confirmed hypothesis".into(), ..state(PHASE_HYPOTHESIS) };
        let cur = apply(Some(prev.clone()), refused).unwrap();
        assert_eq!(cur.phase, PHASE_HYPOTHESIS);
        assert_eq!(cur.hypotheses, prev.hypotheses);
        assert_eq!(cur.instrumentation, prev.instrumentation);
        assert_eq!(cur.error, "no confirmed hypothesis");

        // A success clears the error.
        let ok = State { action: "hypotheses".into(), ..state(PHASE_HYPOTHESIS) };
        assert!(apply(Some(cur), ok).unwrap().error.is_empty());
    }

    #[test]
    fn apply_refused_end_reports_sweep() {
        let prev = State { fixed: true, instrumentation: vec![hit(2), hit(5)], ..state(PHASE_FIX) };
        let refused = State { action: "end".into(), error: "1 tagged line(s) remain after sweep".into(), removed: vec![hit(2)], instrumentation: vec![hit(5)], ..state(PHASE_FIX) };
        let cur = apply(Some(prev), refused).unwrap();
        assert_eq!(cur.removed.len(), 1);
        assert_eq!(cur.instrumentation.len(), 1);
        assert!(cur.active());
    }

    #[test]
    fn apply_error_without_session() {
        let refused = State { action: "scan".into(), error: "no debug session is active".into(), ..State::default() };
        let cur = apply(None, refused).unwrap();
        assert!(!cur.active());
        assert_eq!(cur.error, "no debug session is active");
    }

    #[test]
    fn chip_text_counts() {
        let s = State { instrumentation: vec![hit(1), hit(2)], ..state(PHASE_HYPOTHESIS) };
        assert_eq!(chip_text(&s), "Debug · Hypothesis · 2 tagged");
        let closed = State { removed: vec![hit(1)], ..state(PHASE_CLOSED) };
        assert_eq!(chip_text(&closed), "Debug · Closed · 1 swept");
        assert_eq!(phase_label(""), "Off");
    }

    #[test]
    fn checklist_progresses() {
        let done = |s: &State| checklist(s).iter().filter(|st| st.done).count();
        let mut s = state(PHASE_HYPOTHESIS);
        assert_eq!(done(&s), 0);
        s.hypotheses = vec![hyp("h1", "open", 0), hyp("h2", "open", 0)];
        assert_eq!(done(&s), 1);
        s.hypotheses[0].evidence.push(Evidence { source: "repro".into(), text: "boom".into() });
        assert_eq!(done(&s), 2);
        s.hypotheses[0].status = "confirmed".into();
        assert_eq!(done(&s), 3);
        s.phase = PHASE_FIX.into();
        assert_eq!(done(&s), 4);
        s.fixed = true;
        assert_eq!(done(&s), 5);
        s.phase = PHASE_CLOSED.into();
        assert_eq!(done(&s), 6);
        s.instrumentation = vec![hit(1)];
        assert!(!checklist(&s)[5].done, "end is not done while a tagged line remains");
    }

    #[test]
    fn checklist_all_refuted_counts_as_settled() {
        let s = State { hypotheses: vec![hyp("h1", "refuted", 1), hyp("h2", "refuted", 0)], ..state(PHASE_HYPOTHESIS) };
        assert!(checklist(&s)[2].done);
    }

    #[test]
    fn can_gates_actions_by_phase() {
        assert!(can(None, Action::Start));
        assert!(!can(None, Action::End));
        let closed = state(PHASE_CLOSED);
        assert!(can(Some(&closed), Action::Start), "a closed session can be restarted");

        let mut s = State { hypotheses: vec![hyp("h1", "open", 1), hyp("h2", "open", 0)], ..state(PHASE_HYPOTHESIS) };
        assert!(!can(Some(&s), Action::Start));
        assert!(can(Some(&s), Action::SetHypotheses));
        assert!(can(Some(&s), Action::Evidence));
        assert!(!can(Some(&s), Action::Unlock));
        assert!(can(Some(&s), Action::Override));
        assert!(!can(Some(&s), Action::MarkFixed));
        s.hypotheses[0].status = "confirmed".into();
        assert!(can(Some(&s), Action::Unlock));

        s.phase = PHASE_FIX.into();
        assert!(!can(Some(&s), Action::SetHypotheses));
        assert!(!can(Some(&s), Action::Override));
        assert!(can(Some(&s), Action::MarkFixed));
        assert!(!can(Some(&s), Action::End));
        s.fixed = true;
        assert!(!can(Some(&s), Action::MarkFixed));
        assert!(can(Some(&s), Action::End));
    }

    #[test]
    fn parse_hypotheses_cases() {
        let ok = parse_hypotheses("off by one | index == len | index < len\n\n  stale cache  \n").unwrap();
        assert_eq!(ok.len(), 2);
        assert_eq!(ok[0], Draft { id: "h1".into(), statement: "off by one".into(), confirms: "index == len".into(), refutes: "index < len".into() });
        assert_eq!(ok[1].id, "h2");
        assert_eq!(ok[1].statement, "stale cache");
        assert!(ok[1].confirms.is_empty());

        assert!(parse_hypotheses("only one").unwrap_err().contains("got 1"));
        assert!(parse_hypotheses("a\nb\nc\nd\ne\nf").unwrap_err().contains("got 6"));
        assert!(parse_hypotheses("a\n | confirms").unwrap_err().contains("no statement"));
        assert!(parse_hypotheses("").is_err());
    }

    #[test]
    fn summary_verifies_cleanup() {
        let s = State {
            hypotheses: vec![hyp("h1", "confirmed", 2), hyp("h2", "refuted", 1)],
            removed: vec![hit(2)],
            ..state(PHASE_CLOSED)
        };
        let sum = summary(&s);
        assert_eq!(sum.root_cause, vec!["cause h1".to_string()]);
        assert_eq!(sum.evidence, 3);
        assert_eq!(sum.swept, 1);
        assert_eq!(sum.remaining, 0);
        assert!(sum.verified);

        let open = State { instrumentation: vec![hit(4)], ..state(PHASE_FIX) };
        assert!(!summary(&open).verified);
    }

    #[test]
    fn transcript_note_cases() {
        let start = State { action: "start".into(), ..state(PHASE_HYPOTHESIS) };
        let (title, body) = transcript_note(&start).unwrap();
        assert_eq!(title, "Debug session started");
        assert!(body.contains(&marker()));

        let over = State { action: "unlock".into(), override_reason: "prod down".into(), ..state(PHASE_FIX) };
        assert_eq!(transcript_note(&over).unwrap().1, "No hypothesis was confirmed. Override reason: prod down");

        let ev = State { action: "unlock".into(), hypotheses: vec![hyp("h1", "confirmed", 1)], ..state(PHASE_FIX) };
        assert_eq!(transcript_note(&ev).unwrap(), ("Fix unlocked".to_string(), "Confirmed: cause h1".to_string()));

        let end = State { action: "end".into(), hypotheses: vec![hyp("h1", "confirmed", 2)], removed: vec![hit(2)], ..state(PHASE_CLOSED) };
        assert_eq!(
            transcript_note(&end).unwrap().1,
            "Root cause: cause h1. Evidence items: 2. Swept 1 tagged line(s); 0 remain (cleanup verified)."
        );

        let refused = State { action: "end".into(), error: "1 tagged line(s) remain".into(), ..state(PHASE_FIX) };
        assert!(transcript_note(&refused).is_none());
        let routine = State { action: "evidence".into(), ..state(PHASE_HYPOTHESIS) };
        assert!(transcript_note(&routine).is_none());
    }
}
