//! Zero-turn status card (#1397).
//!
//! Answers "what is this child doing, what has it finished, what does it
//! need from me" from the record the daemon already sends (`GET /v1/team`
//! members plus the root's status row). No model call, no new store.
//!
//! Honesty rules:
//! - Every figure names its source. Engine-measured values (budget counters
//!   from the team snapshot) render plain; values whose source is agent text
//!   (terminal summary, block reason) carry the [`AGENT_REPORTED`] chip.
//! - A figure nobody measured renders [`DASH`], never `0`. An absent budget
//!   leaves every counter unmeasured; a zero dollar cost is treated as
//!   unpriced (the daemon sends `0` when no price is known).
//! - A next step appears only when one is recorded (pending approval or
//!   question, blocked child). None is ever invented.
//! - The idle recap replaces the live card only when nothing is in flight
//!   and the session has been quiet for [`IDLE_RECAP_MS`].
//!
//! Pure: no Leptos, no serde. The cockpit maps daemon JSON onto [`Member`]
//! and [`RootState`]; CI runs these tests with
//! `cargo test --manifest-path webui/status-card/Cargo.toml`.

/// Quiet time before the card switches to the idle recap: 6 h, matching
/// the Kiro Crew 0.9.0 reference.
pub const IDLE_RECAP_MS: i64 = 6 * 60 * 60 * 1000;
/// Chip text for a value whose source is agent-written text.
pub const AGENT_REPORTED: &str = "agent-reported";
/// Text for a figure nobody measured.
pub const DASH: &str = "\u{2014}";
/// Child sessions listed in the idle recap (tail of the daemon's list).
pub const RECAP_RECENT_MAX: usize = 5;

/// Where a value comes from.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Source {
    /// Measured or recorded by the engine/daemon.
    Engine,
    /// Text an agent wrote.
    Agent,
}

/// One labelled value on the card.
#[derive(Clone, Debug, PartialEq)]
pub struct Figure {
    pub label: &'static str,
    /// `None` = nobody measured it.
    pub value: Option<String>,
    pub source: Source,
}

impl Figure {
    /// Display text: the value, or [`DASH`] when unmeasured.
    pub fn text(&self) -> String {
        match &self.value {
            Some(v) => v.clone(),
            None => DASH.to_string(),
        }
    }

    /// Provenance chip, only for an agent-sourced value that exists.
    pub fn chip(&self) -> Option<&'static str> {
        if self.source == Source::Agent && self.value.is_some() {
            Some(AGENT_REPORTED)
        } else {
            None
        }
    }
}

/// Engine budget counters from a team member. `None` per field = not sent.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct Budget {
    pub elapsed_s: Option<i64>,
    pub tokens: Option<i64>,
    pub tool_calls: Option<i64>,
    pub cost_usd: Option<f64>,
}

/// One delegated child, as the cockpit already holds it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Member {
    pub session_id: String,
    pub name: String,
    /// Display state (`running`, `stalled`, `blocked`, `done`, `failed`,
    /// `stopped`, …) as normalised by the cockpit.
    pub state: String,
    /// Delegation objective (the goal line).
    pub objective: String,
    /// Agent-written terminal summary.
    pub summary: String,
    /// Agent-written block reason.
    pub block_reason: String,
    /// Engine counters; `None` when the daemon sent no budget.
    pub budget: Option<Budget>,
}

/// The root session's daemon status fields.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct RootState {
    pub busy: bool,
    pub permission_pending: bool,
    pub question_pending: bool,
    /// Unix ms of last activity; `0` = unknown.
    pub last_activity_ms: i64,
}

/// Which card bucket a child state belongs to.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Bucket {
    Finished,
    Running,
    Waiting,
    Other,
}

impl Bucket {
    pub fn label(self) -> &'static str {
        match self {
            Bucket::Finished => "Finished",
            Bucket::Running => "Running",
            Bucket::Waiting => "Waiting on you",
            Bucket::Other => "Other",
        }
    }
}

/// Bucket for a display state.
pub fn bucket(state: &str) -> Bucket {
    match state.trim().to_ascii_lowercase().as_str() {
        "done" | "completed" | "failed" | "stopped" | "canceled" | "cancelled" => Bucket::Finished,
        "running" | "stalled" | "working" | "active" | "starting" => Bucket::Running,
        "blocked" | "waiting" => Bucket::Waiting,
        _ => Bucket::Other,
    }
}

/// A recorded next step.
#[derive(Clone, Debug, PartialEq)]
pub struct NextStep {
    pub text: String,
    pub source: Source,
}

impl NextStep {
    pub fn chip(&self) -> Option<&'static str> {
        if self.source == Source::Agent {
            Some(AGENT_REPORTED)
        } else {
            None
        }
    }
}

fn label_of(m: &Member) -> String {
    let name = m.name.trim();
    if !name.is_empty() {
        return name.to_string();
    }
    m.session_id.chars().take(8).collect()
}

fn one_line(text: &str) -> String {
    text.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// The one recorded next step for a session, or `None`. Engine-recorded
/// waits come first (approval, question), then the first blocked child.
pub fn next_step(root: &RootState, members: &[Member]) -> Option<NextStep> {
    if root.permission_pending {
        return Some(NextStep {
            text: "Answer the pending approval".to_string(),
            source: Source::Engine,
        });
    }
    if root.question_pending {
        return Some(NextStep {
            text: "Answer the pending question".to_string(),
            source: Source::Engine,
        });
    }
    let blocked = members.iter().find(|m| bucket(&m.state) == Bucket::Waiting)?;
    let name = label_of(blocked);
    Some(match child_next(blocked) {
        Some(step) => NextStep {
            text: format!("Unblock {name}: {}", step.text),
            source: Source::Agent,
        },
        None => NextStep {
            text: format!("Unblock {name}"),
            source: Source::Engine,
        },
    })
}

/// A child's own recorded next step: its block reason while blocked.
fn child_next(m: &Member) -> Option<NextStep> {
    if bucket(&m.state) != Bucket::Waiting {
        return None;
    }
    let reason = one_line(&m.block_reason);
    if reason.is_empty() {
        return None;
    }
    Some(NextStep {
        text: reason,
        source: Source::Agent,
    })
}

/// Live session card: names per bucket plus the recorded next step.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct SessionCard {
    pub finished: Vec<String>,
    pub running: Vec<String>,
    pub waiting: Vec<String>,
    pub next: Option<NextStep>,
}

/// Idle recap: what the session left open and its recent children.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Recap {
    /// `name (state)` for every child that did not finish cleanly.
    pub open: Vec<String>,
    /// The last [`RECAP_RECENT_MAX`] child session titles in the order the
    /// daemon listed them (no recency claim: the list is not time-sorted).
    pub recent: Vec<String>,
    /// Quiet time in ms.
    pub idle_ms: i64,
}

/// What the session summary shows.
#[derive(Clone, Debug, PartialEq)]
pub enum Card {
    Live(SessionCard),
    Recap(Recap),
    /// Nothing recorded: render no card.
    Empty,
}

/// True when something is still moving or waiting on the user.
pub fn in_flight(root: &RootState, members: &[Member]) -> bool {
    root.busy
        || root.permission_pending
        || root.question_pending
        || members
            .iter()
            .any(|m| matches!(bucket(&m.state), Bucket::Running | Bucket::Waiting))
}

/// Build the session summary card. `recent_children` are child session
/// titles in list order; `now_ms` is unix ms.
pub fn session_card(
    root: &RootState,
    members: &[Member],
    recent_children: &[String],
    now_ms: i64,
) -> Card {
    let quiet_ms = if root.last_activity_ms > 0 && now_ms > root.last_activity_ms {
        now_ms - root.last_activity_ms
    } else {
        0
    };
    if !in_flight(root, members) && root.last_activity_ms > 0 && quiet_ms >= IDLE_RECAP_MS {
        let open = members
            .iter()
            .filter(|m| {
                let s = m.state.trim().to_ascii_lowercase();
                s != "done" && s != "completed"
            })
            .map(|m| format!("{} ({})", label_of(m), m.state.trim()))
            .collect();
        let skip = recent_children.len().saturating_sub(RECAP_RECENT_MAX);
        let recent = recent_children
            .iter()
            .skip(skip)
            .map(|t| t.trim().to_string())
            .filter(|t| !t.is_empty())
            .collect();
        return Card::Recap(Recap {
            open,
            recent,
            idle_ms: quiet_ms,
        });
    }
    let mut card = SessionCard::default();
    if root.permission_pending {
        card.waiting.push("Approval".to_string());
    }
    if root.question_pending {
        card.waiting.push("Question".to_string());
    }
    for m in members {
        let name = label_of(m);
        match bucket(&m.state) {
            Bucket::Finished => card.finished.push(name),
            Bucket::Running => card.running.push(name),
            Bucket::Waiting => card.waiting.push(name),
            Bucket::Other => {}
        }
    }
    card.next = next_step(root, members);
    if card.finished.is_empty() && card.running.is_empty() && card.waiting.is_empty() {
        return Card::Empty;
    }
    Card::Live(card)
}

/// One child's card, shown when its row is opened.
#[derive(Clone, Debug, PartialEq)]
pub struct ChildCard {
    /// Delegation objective; `None` when the record has none.
    pub goal: Option<String>,
    pub bucket: Bucket,
    pub figures: Vec<Figure>,
    pub next: Option<NextStep>,
}

fn non_empty(text: &str) -> Option<String> {
    let t = text.trim();
    if t.is_empty() {
        None
    } else {
        Some(t.to_string())
    }
}

/// Build a child's card from its record.
pub fn child_card(m: &Member) -> ChildCard {
    let b = m.budget;
    let elapsed = b.and_then(|b| b.elapsed_s).filter(|n| *n >= 0).map(fmt_elapsed);
    let tokens = b.and_then(|b| b.tokens).filter(|n| *n >= 0).map(fmt_count);
    let tools = b.and_then(|b| b.tool_calls).filter(|n| *n >= 0).map(fmt_count);
    // The daemon sends 0 when no price is known: unpriced, not free.
    let cost = b
        .and_then(|b| b.cost_usd)
        .filter(|c| c.is_finite() && *c > 0.0)
        .map(fmt_usd);
    let figures = vec![
        Figure { label: "Elapsed", value: elapsed, source: Source::Engine },
        Figure { label: "Tokens", value: tokens, source: Source::Engine },
        Figure { label: "Tool calls", value: tools, source: Source::Engine },
        Figure { label: "Cost", value: cost, source: Source::Engine },
        Figure { label: "Result", value: non_empty(&m.summary), source: Source::Agent },
    ];
    ChildCard {
        goal: non_empty(&m.objective).map(|g| one_line(&g)),
        bucket: bucket(&m.state),
        figures,
        next: child_next(m),
    }
}

/// `m:ss` or `h:mm:ss`.
pub fn fmt_elapsed(secs: i64) -> String {
    let secs = secs.max(0);
    let m = secs / 60;
    let s = secs % 60;
    if m >= 60 {
        format!("{}:{:02}:{:02}", m / 60, m % 60, s)
    } else {
        format!("{m}:{s:02}")
    }
}

/// Integer with thousands separators.
pub fn fmt_count(n: i64) -> String {
    let neg = n < 0;
    let digits = n.unsigned_abs().to_string();
    let mut out = String::new();
    for (i, c) in digits.chars().enumerate() {
        if i > 0 && (digits.len() - i) % 3 == 0 {
            out.push(',');
        }
        out.push(c);
    }
    if neg {
        format!("-{out}")
    } else {
        out
    }
}

/// Dollars: four decimals under a dollar, two above.
pub fn fmt_usd(c: f64) -> String {
    if c < 1.0 {
        format!("${c:.4}")
    } else {
        format!("${c:.2}")
    }
}

/// Quiet time label for the recap, e.g. `7h` or `2d`.
pub fn fmt_idle(ms: i64) -> String {
    let h = ms.max(0) / 3_600_000;
    if h >= 48 {
        format!("{}d", h / 24)
    } else {
        format!("{h}h")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn member(name: &str, state: &str) -> Member {
        Member {
            session_id: format!("sess-{name}"),
            name: name.to_string(),
            state: state.to_string(),
            ..Member::default()
        }
    }

    const NOW: i64 = 1_800_000_000_000;

    #[test]
    fn bucket_derivation() {
        let cases = [
            ("done", Bucket::Finished),
            ("failed", Bucket::Finished),
            ("stopped", Bucket::Finished),
            ("Completed", Bucket::Finished),
            ("running", Bucket::Running),
            ("stalled", Bucket::Running),
            ("blocked", Bucket::Waiting),
            ("waiting", Bucket::Waiting),
            ("", Bucket::Other),
            ("mystery", Bucket::Other),
        ];
        for (state, want) in cases {
            assert_eq!(bucket(state), want, "state {state:?}");
        }
    }

    #[test]
    fn live_card_has_all_three_buckets() {
        let root = RootState { busy: true, last_activity_ms: NOW, ..RootState::default() };
        let members = vec![member("a", "done"), member("b", "running"), member("c", "blocked")];
        let Card::Live(card) = session_card(&root, &members, &[], NOW) else {
            panic!("want live card");
        };
        assert_eq!(card.finished, vec!["a"]);
        assert_eq!(card.running, vec!["b"]);
        assert_eq!(card.waiting, vec!["c"]);
        assert_eq!(
            card.next,
            Some(NextStep { text: "Unblock c".into(), source: Source::Engine }),
            "blocked without a reason: engine-recorded state only"
        );
    }

    #[test]
    fn pending_approval_is_waiting_and_first_next_step() {
        let root = RootState { permission_pending: true, question_pending: true, ..RootState::default() };
        let mut c = member("c", "blocked");
        c.block_reason = "need creds".into();
        let Card::Live(card) = session_card(&root, &[c], &[], NOW) else {
            panic!("want live card");
        };
        assert_eq!(card.waiting, vec!["Approval", "Question", "c"]);
        let next = card.next.expect("recorded next step");
        assert_eq!(next.text, "Answer the pending approval");
        assert_eq!(next.chip(), None);
    }

    #[test]
    fn blocked_reason_next_step_is_agent_reported() {
        let mut c = member("c", "blocked");
        c.block_reason = "  need\nthe API key ".into();
        let next = next_step(&RootState::default(), &[member("a", "running"), c]).expect("step");
        assert_eq!(next.text, "Unblock c: need the API key");
        assert_eq!(next.source, Source::Agent);
        assert_eq!(next.chip(), Some(AGENT_REPORTED));
    }

    #[test]
    fn no_recorded_next_step_renders_none() {
        let members = vec![member("a", "done"), member("b", "running")];
        assert_eq!(next_step(&RootState::default(), &members), None);
        let Card::Live(card) = session_card(&RootState::default(), &members, &[], NOW) else {
            panic!("want live card");
        };
        assert_eq!(card.next, None);
        assert_eq!(child_card(&members[1]).next, None);
    }

    #[test]
    fn empty_record_renders_no_card() {
        assert_eq!(session_card(&RootState::default(), &[], &[], NOW), Card::Empty);
        assert_eq!(
            session_card(&RootState::default(), &[member("x", "mystery")], &[], NOW),
            Card::Empty
        );
    }

    #[test]
    fn idle_recap_after_threshold() {
        let root = RootState { last_activity_ms: NOW - IDLE_RECAP_MS - 1, ..RootState::default() };
        let members = vec![member("a", "done"), member("b", "failed"), member("c", "stopped")];
        let kids: Vec<String> = (1..=7).map(|i| format!("child {i}")).collect();
        let Card::Recap(r) = session_card(&root, &members, &kids, NOW) else {
            panic!("want recap");
        };
        assert_eq!(r.open, vec!["b (failed)", "c (stopped)"]);
        assert_eq!(r.recent, vec!["child 3", "child 4", "child 5", "child 6", "child 7"]);
        assert_eq!(fmt_idle(r.idle_ms), "6h");
    }

    #[test]
    fn no_recap_while_in_flight_or_recent_or_unknown() {
        let old = NOW - IDLE_RECAP_MS * 2;
        let cases = [
            ("busy root", RootState { busy: true, last_activity_ms: old, ..RootState::default() }, "done"),
            ("running child", RootState { last_activity_ms: old, ..RootState::default() }, "running"),
            ("blocked child", RootState { last_activity_ms: old, ..RootState::default() }, "blocked"),
            ("recent", RootState { last_activity_ms: NOW - IDLE_RECAP_MS + 1000, ..RootState::default() }, "done"),
            ("unknown activity", RootState::default(), "done"),
        ];
        for (name, root, state) in cases {
            let card = session_card(&root, &[member("a", state)], &[], NOW);
            assert!(matches!(card, Card::Live(_)), "{name}: {card:?}");
        }
    }

    #[test]
    fn unmeasured_figures_render_dash_not_zero() {
        let card = child_card(&member("a", "running"));
        for f in &card.figures {
            assert_eq!(f.value, None, "{}", f.label);
            assert_eq!(f.text(), DASH, "{}", f.label);
            assert_eq!(f.chip(), None, "dash carries no chip: {}", f.label);
        }
        assert_eq!(card.goal, None);
    }

    #[test]
    fn engine_figures_plain_agent_figures_chipped() {
        let mut m = member("a", "done");
        m.objective = "Fix\n the flaky test".into();
        m.summary = "tests: 42 passed".into();
        m.budget = Some(Budget {
            elapsed_s: Some(3725),
            tokens: Some(12345),
            tool_calls: Some(0),
            cost_usd: Some(0.0),
        });
        let card = child_card(&m);
        assert_eq!(card.goal.as_deref(), Some("Fix the flaky test"));
        assert_eq!(card.bucket, Bucket::Finished);
        let get = |label: &str| card.figures.iter().find(|f| f.label == label).unwrap().clone();
        assert_eq!(get("Elapsed").text(), "1:02:05");
        assert_eq!(get("Elapsed").chip(), None);
        assert_eq!(get("Tokens").text(), "12,345");
        assert_eq!(get("Tool calls").text(), "0", "a measured zero is a real zero");
        assert_eq!(get("Cost").text(), DASH, "unpriced cost is a dash, never $0");
        let result = get("Result");
        assert_eq!(result.text(), "tests: 42 passed");
        assert_eq!(result.chip(), Some(AGENT_REPORTED));
        assert_eq!(card.next, None, "a finished child records no next step");
    }

    #[test]
    fn blocked_child_next_step_is_its_reason() {
        let mut m = member("a", "blocked");
        m.block_reason = "waiting on review".into();
        let card = child_card(&m);
        assert_eq!(card.bucket, Bucket::Waiting);
        let next = card.next.expect("recorded reason");
        assert_eq!(next.text, "waiting on review");
        assert_eq!(next.chip(), Some(AGENT_REPORTED));
        assert_eq!(child_card(&member("b", "blocked")).next, None, "no reason, no invented step");
    }

    #[test]
    fn priced_cost_formats() {
        assert_eq!(fmt_usd(0.01234), "$0.0123");
        assert_eq!(fmt_usd(2.5), "$2.50");
        let mut m = member("a", "running");
        m.budget = Some(Budget { cost_usd: Some(0.5), ..Budget::default() });
        let card = child_card(&m);
        let cost = card.figures.iter().find(|f| f.label == "Cost").unwrap();
        assert_eq!(cost.text(), "$0.5000");
    }

    #[test]
    fn formatting_helpers() {
        assert_eq!(fmt_count(0), "0");
        assert_eq!(fmt_count(999), "999");
        assert_eq!(fmt_count(1000), "1,000");
        assert_eq!(fmt_count(1234567), "1,234,567");
        assert_eq!(fmt_count(-1234), "-1,234");
        assert_eq!(fmt_elapsed(65), "1:05");
        assert_eq!(fmt_idle(50 * 3_600_000), "2d");
    }

    #[test]
    fn nameless_member_uses_session_prefix() {
        let mut m = member("", "running");
        m.session_id = "abcdef123456".into();
        let Card::Live(card) = session_card(&RootState::default(), &[m], &[], NOW) else {
            panic!("want live");
        };
        assert_eq!(card.running, vec!["abcdef12"]);
    }
}
