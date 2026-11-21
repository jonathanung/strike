//! "While you were away" briefing (#1488).
//!
//! A deterministic roll-up of daemon records newer than the shared
//! watermark (`GET /v1/briefing/seen`). No model call, no scheduler, no new
//! record type: every row is a notification, a root status flag or a
//! schedule's last run, and its label is the record's own text.
//!
//! Groups shipped in slice 1 (only sources with a reliable timestamp):
//! 1. Finished — `session.finished` / `child.finished` notifications.
//! 2. Failed or canceled — `child.finished` whose title says failed/canceled.
//! 3. Waiting on you — roots with `permissionPending` / `questionPending`
//!    now, plus `child.blocked` notifications since the watermark.
//! 4. Schedule runs — schedules whose `lastFire` is after the watermark.
//!
//! Dropped (Q4: drop a group rather than guess): expired approvals (no
//! daemon record) and goal transitions (goals carry no transition time).
//!
//! Pure: no Leptos, no serde. CI runs
//! `cargo test --manifest-path webui/away-briefing/Cargo.toml`.

/// Away gap before a briefing is offered: 30 minutes.
pub const THRESHOLD_MS: i64 = 30 * 60 * 1000;
/// Rows shown per group before "+N more".
pub const ROWS_PER_GROUP: usize = 5;

/// One notification centre record (`GET /v1/notifications`).
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Note {
    pub id: String,
    pub kind: String,
    pub session_id: String,
    /// The child (or session) the note is about; used to drop a blocked
    /// row that a newer finish already resolved.
    pub subject_id: String,
    pub title: String,
    pub body: String,
    pub href: String,
    /// Unix ms; `0` = unparseable (never counted).
    pub at_ms: i64,
}

/// A root session's live wait flags (`GET /v1/roots`).
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Waiting {
    pub session_id: String,
    pub title: String,
    pub permission: bool,
    pub question: bool,
}

/// A schedule's last run (`GET /v1/schedules`).
#[derive(Clone, Debug, Default, PartialEq)]
pub struct ScheduleRun {
    pub id: String,
    pub name: String,
    /// Unix ms of `lastFire`; `0` = never fired or unparseable.
    pub last_fire_ms: i64,
    pub last_status: String,
    pub last_session_id: String,
    pub last_error: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum GroupKind {
    Finished,
    Failed,
    Waiting,
    Schedules,
}

impl GroupKind {
    pub fn id(self) -> &'static str {
        match self {
            GroupKind::Finished => "finished",
            GroupKind::Failed => "failed",
            GroupKind::Waiting => "waiting",
            GroupKind::Schedules => "schedules",
        }
    }

    pub fn title(self) -> &'static str {
        match self {
            GroupKind::Finished => "Finished",
            GroupKind::Failed => "Failed or canceled",
            GroupKind::Waiting => "Waiting on you",
            GroupKind::Schedules => "Schedule runs",
        }
    }

    fn noun(self, n: usize) -> String {
        match self {
            GroupKind::Finished => format!("{n} finished"),
            GroupKind::Failed => format!("{n} failed"),
            GroupKind::Waiting => format!("{n} waiting"),
            GroupKind::Schedules => {
                if n == 1 {
                    "1 schedule run".to_string()
                } else {
                    format!("{n} schedule runs")
                }
            }
        }
    }
}

/// One record-derived row. `session_id` is the deep-link target (empty =
/// no link).
#[derive(Clone, Debug, PartialEq)]
pub struct Row {
    pub label: String,
    pub detail: String,
    pub session_id: String,
    /// Request id for an approval deep link; usually empty.
    pub approval_id: String,
    pub at_ms: i64,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Group {
    pub kind: GroupKind,
    pub rows: Vec<Row>,
}

impl Group {
    pub fn shown(&self) -> &[Row] {
        let n = self.rows.len().min(ROWS_PER_GROUP);
        &self.rows[..n]
    }

    pub fn hidden(&self) -> usize {
        self.rows.len().saturating_sub(ROWS_PER_GROUP)
    }
}

#[derive(Clone, Debug, PartialEq)]
pub struct Briefing {
    /// Non-empty groups in display order.
    pub groups: Vec<Group>,
    /// The watermark the briefing was computed against.
    pub since_ms: i64,
    /// Daemon now at compute time; "Mark caught up" advances to this.
    pub now_ms: i64,
}

impl Briefing {
    pub fn total(&self) -> usize {
        self.groups.iter().map(|g| g.rows.len()).sum()
    }

    /// One-line roll-up for the phone chip, e.g. `2 finished · 1 waiting`.
    pub fn chip(&self) -> String {
        self.groups
            .iter()
            .map(|g| g.kind.noun(g.rows.len()))
            .collect::<Vec<_>>()
            .join(" \u{b7} ")
    }
}

/// Whether the away gap is long enough to brief. A zero watermark means
/// never seen: no briefing (the cockpit initialises it instead).
pub fn should_brief(seen_at: i64, now: i64) -> bool {
    seen_at > 0 && now > seen_at && now - seen_at >= THRESHOLD_MS
}

fn lower_ends(text: &str, suffix: &str) -> bool {
    text.trim().to_ascii_lowercase().ends_with(suffix)
}

fn note_row(n: &Note) -> Row {
    Row {
        label: n.title.trim().to_string(),
        detail: n.body.trim().to_string(),
        session_id: n.session_id.clone(),
        approval_id: String::new(),
        at_ms: n.at_ms,
    }
}

fn schedule_failed(status: &str) -> bool {
    matches!(
        status.trim().to_ascii_lowercase().as_str(),
        "failed" | "error" | "errored" | "timeout" | "timed_out" | "skipped"
    )
}

/// Compute the briefing, or `None` when the gap is under the threshold or
/// nothing happened (empty delta = no panel, ever).
pub fn compute(
    seen_at: i64,
    now: i64,
    notes: &[Note],
    waiting: &[Waiting],
    schedules: &[ScheduleRun],
) -> Option<Briefing> {
    if !should_brief(seen_at, now) {
        return None;
    }
    let mut finished = Vec::new();
    let mut failed = Vec::new();
    let mut wait = Vec::new();
    let mut seen_ids: Vec<&str> = Vec::new();
    let mut fresh: Vec<&Note> = notes
        .iter()
        .filter(|n| n.at_ms > seen_at && n.at_ms <= now)
        .collect();
    fresh.sort_by(|a, b| b.at_ms.cmp(&a.at_ms).then_with(|| a.id.cmp(&b.id)));
    // A blocked child that later finished is no longer waiting on anyone.
    let resolved = |blocked: &Note| {
        !blocked.subject_id.is_empty()
            && notes.iter().any(|n| {
                n.kind == "child.finished" && n.subject_id == blocked.subject_id && n.at_ms >= blocked.at_ms
            })
    };
    for n in fresh {
        if !n.id.is_empty() {
            if seen_ids.contains(&n.id.as_str()) {
                continue;
            }
            seen_ids.push(n.id.as_str());
        }
        match n.kind.as_str() {
            "session.finished" => finished.push(note_row(n)),
            "child.finished" => {
                if lower_ends(&n.title, " failed") || lower_ends(&n.title, " canceled") {
                    failed.push(note_row(n));
                } else {
                    finished.push(note_row(n));
                }
            }
            "child.blocked" => {
                if !resolved(n) {
                    wait.push(note_row(n));
                }
            }
            _ => {}
        }
    }
    // Live waits come first: they are true now, not just since the mark.
    let mut live = Vec::new();
    for w in waiting {
        if !(w.permission || w.question) || w.session_id.is_empty() {
            continue;
        }
        let what = match (w.permission, w.question) {
            (true, true) => "Approval and question pending",
            (true, false) => "Approval pending",
            _ => "Question pending",
        };
        let label = if w.title.trim().is_empty() {
            w.session_id.clone()
        } else {
            w.title.trim().to_string()
        };
        live.push(Row {
            label,
            detail: what.to_string(),
            session_id: w.session_id.clone(),
            approval_id: String::new(),
            at_ms: 0,
        });
    }
    live.extend(wait);
    let mut runs: Vec<&ScheduleRun> = schedules
        .iter()
        .filter(|s| s.last_fire_ms > seen_at && s.last_fire_ms <= now)
        .collect();
    runs.sort_by(|a, b| b.last_fire_ms.cmp(&a.last_fire_ms).then_with(|| a.id.cmp(&b.id)));
    let runs: Vec<Row> = runs
        .into_iter()
        .map(|s| {
            let name = if s.name.trim().is_empty() { s.id.clone() } else { s.name.trim().to_string() };
            let status = if s.last_status.trim().is_empty() {
                "fired".to_string()
            } else {
                s.last_status.trim().to_string()
            };
            let detail = if schedule_failed(&s.last_status) && !s.last_error.trim().is_empty() {
                format!("{status}: {}", s.last_error.trim())
            } else {
                status
            };
            Row {
                label: name,
                detail,
                session_id: s.last_session_id.clone(),
                approval_id: String::new(),
                at_ms: s.last_fire_ms,
            }
        })
        .collect();
    let groups: Vec<Group> = [
        (GroupKind::Finished, finished),
        (GroupKind::Failed, failed),
        (GroupKind::Waiting, live),
        (GroupKind::Schedules, runs),
    ]
    .into_iter()
    .filter(|(_, rows)| !rows.is_empty())
    .map(|(kind, rows)| Group { kind, rows })
    .collect();
    if groups.is_empty() {
        return None;
    }
    Some(Briefing { groups, since_ms: seen_at, now_ms: now })
}

/// Away gap label, e.g. `45m`, `3h`, `2d`.
pub fn fmt_gap(ms: i64) -> String {
    let m = ms.max(0) / 60_000;
    if m < 60 {
        format!("{m}m")
    } else if m < 48 * 60 {
        format!("{}h", m / 60)
    } else {
        format!("{}d", m / (24 * 60))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const NOW: i64 = 1_800_000_000_000;
    const SEEN: i64 = NOW - 2 * 60 * 60 * 1000;

    fn note(id: &str, kind: &str, title: &str, at: i64) -> Note {
        Note {
            id: id.into(),
            kind: kind.into(),
            session_id: format!("s-{id}"),
            subject_id: format!("c-{id}"),
            title: title.into(),
            body: "body".into(),
            href: format!("/#session=s-{id}"),
            at_ms: at,
        }
    }

    #[test]
    fn threshold_and_never_seen() {
        let cases = [
            ("never seen", 0, NOW, false),
            ("just under", NOW - THRESHOLD_MS + 1, NOW, false),
            ("exactly", NOW - THRESHOLD_MS, NOW, true),
            ("long away", SEEN, NOW, true),
            ("future mark", NOW + 5, NOW, false),
        ];
        for (name, seen, now, want) in cases {
            assert_eq!(should_brief(seen, now), want, "{name}");
        }
    }

    #[test]
    fn empty_delta_is_no_panel() {
        assert_eq!(compute(SEEN, NOW, &[], &[], &[]), None);
        let old = [note("a", "session.finished", "Session finished", SEEN - 1)];
        assert_eq!(compute(SEEN, NOW, &old, &[], &[]), None, "records at or before the mark are old");
        let at_mark = [note("a", "session.finished", "Session finished", SEEN)];
        assert_eq!(compute(SEEN, NOW, &at_mark, &[], &[]), None);
        let fresh = [note("a", "session.finished", "Session finished", NOW - 1)];
        assert_eq!(compute(NOW - 1000, NOW, &fresh, &[], &[]), None, "under threshold");
    }

    #[test]
    fn groups_all_four_sources() {
        let notes = vec![
            note("1", "session.finished", "Session finished", SEEN + 10),
            note("2", "child.finished", "scribe finished", SEEN + 20),
            note("3", "child.finished", "tester failed", SEEN + 30),
            note("4", "child.finished", "linter canceled", SEEN + 40),
            note("5", "child.blocked", "reviewer blocked", SEEN + 50),
            note("6", "approval.needed", "Approval needed", SEEN + 60),
        ];
        let waiting = vec![
            Waiting { session_id: "r1".into(), title: "Root one".into(), permission: true, question: false },
            Waiting { session_id: "r2".into(), title: String::new(), permission: false, question: false },
        ];
        let schedules = vec![
            ScheduleRun {
                id: "sch1".into(),
                name: "nightly".into(),
                last_fire_ms: SEEN + 70,
                last_status: "failed".into(),
                last_session_id: "run-sess".into(),
                last_error: "exit 1".into(),
            },
            ScheduleRun { id: "sch2".into(), name: "stale".into(), last_fire_ms: SEEN - 5, ..ScheduleRun::default() },
        ];
        let b = compute(SEEN, NOW, &notes, &waiting, &schedules).expect("briefing");
        let kinds: Vec<GroupKind> = b.groups.iter().map(|g| g.kind).collect();
        assert_eq!(kinds, vec![GroupKind::Finished, GroupKind::Failed, GroupKind::Waiting, GroupKind::Schedules]);
        let labels = |k: GroupKind| -> Vec<String> {
            b.groups.iter().find(|g| g.kind == k).unwrap().rows.iter().map(|r| r.label.clone()).collect()
        };
        assert_eq!(labels(GroupKind::Finished), vec!["scribe finished", "Session finished"], "newest first");
        assert_eq!(labels(GroupKind::Failed), vec!["linter canceled", "tester failed"]);
        assert_eq!(labels(GroupKind::Waiting), vec!["Root one", "reviewer blocked"], "live waits first");
        let run = &b.groups[3].rows[0];
        assert_eq!(run.label, "nightly");
        assert_eq!(run.detail, "failed: exit 1");
        assert_eq!(run.session_id, "run-sess");
        assert_eq!(b.total(), 7, "approval.needed history is not a live wait");
        assert_eq!(b.chip(), "2 finished \u{b7} 2 failed \u{b7} 2 waiting \u{b7} 1 schedule run");
        assert_eq!(b.since_ms, SEEN);
        assert_eq!(b.now_ms, NOW);
    }

    #[test]
    fn live_wait_alone_briefs_and_labels_kind() {
        let waiting = [Waiting { session_id: "r1".into(), title: " ".into(), permission: true, question: true }];
        let b = compute(SEEN, NOW, &[], &waiting, &[]).expect("briefing");
        let row = &b.groups[0].rows[0];
        assert_eq!(row.label, "r1", "blank title falls back to id");
        assert_eq!(row.detail, "Approval and question pending");
        let q = [Waiting { session_id: "r1".into(), title: "T".into(), permission: false, question: true }];
        assert_eq!(compute(SEEN, NOW, &[], &q, &[]).unwrap().groups[0].rows[0].detail, "Question pending");
    }

    #[test]
    fn duplicate_ids_and_future_or_unparsed_times_are_skipped() {
        let notes = vec![
            note("1", "session.finished", "Session finished", SEEN + 10),
            note("1", "session.finished", "Session finished", SEEN + 10),
            note("2", "session.finished", "From the future", NOW + 10),
            note("3", "session.finished", "No time", 0),
        ];
        let b = compute(SEEN, NOW, &notes, &[], &[]).expect("briefing");
        assert_eq!(b.total(), 1);
    }

    #[test]
    fn blocked_child_that_later_finished_is_not_waiting() {
        let mut blocked = note("b", "child.blocked", "scribe blocked", SEEN + 10);
        blocked.subject_id = "child-1".into();
        let mut done = note("d", "child.finished", "scribe finished", SEEN + 20);
        done.subject_id = "child-1".into();
        let mut other = note("o", "child.blocked", "tester blocked", SEEN + 30);
        other.subject_id = "child-2".into();
        let b = compute(SEEN, NOW, &[blocked.clone(), done.clone(), other], &[], &[]).expect("briefing");
        let waiting = b.groups.iter().find(|g| g.kind == GroupKind::Waiting).expect("waiting group");
        let labels: Vec<&str> = waiting.rows.iter().map(|r| r.label.as_str()).collect();
        assert_eq!(labels, vec!["tester blocked"]);
        // A finish older than the block does not resolve it.
        let mut early = done;
        early.at_ms = SEEN + 5;
        let b = compute(SEEN, NOW, &[blocked, early], &[], &[]).expect("briefing");
        assert!(b.groups.iter().any(|g| g.kind == GroupKind::Waiting));
    }

    #[test]
    fn group_caps_rows() {
        let notes: Vec<Note> = (0..8)
            .map(|i| note(&format!("n{i}"), "session.finished", &format!("S{i}"), SEEN + 1 + i as i64))
            .collect();
        let b = compute(SEEN, NOW, &notes, &[], &[]).expect("briefing");
        let g = &b.groups[0];
        assert_eq!(g.shown().len(), ROWS_PER_GROUP);
        assert_eq!(g.hidden(), 3);
        assert_eq!(g.shown()[0].label, "S7");
    }

    #[test]
    fn schedule_without_status_reads_fired() {
        let s = [ScheduleRun { id: "x".into(), last_fire_ms: SEEN + 1, ..ScheduleRun::default() }];
        let b = compute(SEEN, NOW, &[], &[], &s).expect("briefing");
        assert_eq!(b.groups[0].rows[0].label, "x");
        assert_eq!(b.groups[0].rows[0].detail, "fired");
        assert_eq!(b.chip(), "1 schedule run");
    }

    #[test]
    fn gap_labels() {
        assert_eq!(fmt_gap(45 * 60_000), "45m");
        assert_eq!(fmt_gap(3 * 3_600_000), "3h");
        assert_eq!(fmt_gap(72 * 3_600_000), "3d");
    }
}
