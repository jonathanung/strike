//! Pin a transcript message (#1528).
//!
//! A pin marks one prose message (the user's or the agent's) so the pin
//! rail can jump back to it, across reloads and devices: the daemon
//! stores pins per session beside the logs.
//!
//! The cockpit's `Message.id` is a per-build counter and is never a pin
//! anchor. A pin's anchor is:
//! - `keep`: the message's `keep_events`, the event ordinal at the end of
//!   its turn that `fork_at` already trusts to mean "this point in the
//!   log";
//! - `role`: `you` or `strike`;
//! - `seq`: the message's position among same-role prose messages with
//!   that `keep` (a turn can hold several agent replies split by tool
//!   calls).
//!
//! Resolution is strict: a pin resolves only to the message at its anchor
//! whose excerpt still matches. Anything else — the log was compacted,
//! forked or rewound past it — renders as unresolved in the rail with
//! its excerpt, never re-pointed at a neighbour and never dropped.
//!
//! No web dependencies, so the rules run as host unit tests in CI.

/// Stored excerpt length, in characters. Must match the daemon's clip
/// (`messagePinExcerptMax`) so a stored excerpt compares equal.
pub const EXCERPT_MAX: usize = 160;
/// Rail label length, in characters.
pub const LABEL_MAX: usize = 60;

pub const UNRESOLVED: &str = "Original message no longer in this log";

/// Durable pin anchor.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct Anchor {
    pub keep: i64,
    pub role: String,
    pub seq: u32,
}

/// A stored pin.
#[derive(Clone, Debug, PartialEq)]
pub struct Pin {
    pub anchor: Anchor,
    pub excerpt: String,
    /// Message time, epoch ms (0 when unknown). Display only.
    pub at_ms: f64,
}

/// One transcript row, reduced to what pins need.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Row<'a> {
    pub id: u32,
    /// Role class (`you`, `strike`, `tool`, …).
    pub role: &'a str,
    /// `keep_events`; negative until the message's turn completes.
    pub keep: i32,
    pub text: &'a str,
}

/// Slice 1 pins settled prose only.
pub fn pinnable(role: &str, keep: i32) -> bool {
    (role == "you" || role == "strike") && keep >= 0
}

/// Anchor of every pinnable row, in transcript order.
pub fn anchors(rows: &[Row<'_>]) -> Vec<(u32, Anchor)> {
    let mut out: Vec<(u32, Anchor)> = Vec::new();
    for row in rows {
        if !pinnable(row.role, row.keep) {
            continue;
        }
        let keep = i64::from(row.keep);
        let seq = out
            .iter()
            .filter(|(_, a)| a.keep == keep && a.role == row.role)
            .count() as u32;
        out.push((
            row.id,
            Anchor {
                keep,
                role: row.role.to_string(),
                seq,
            },
        ));
    }
    out
}

/// Anchor of the message with `role`/`keep`, given the messages before
/// it newest first as `(role, keep)`. A turn's messages share one
/// `keep` and are contiguous, so the walk stops at the first earlier
/// message from another turn. Lets the view anchor one row without
/// scanning the whole transcript; agrees with [`anchors`].
pub fn anchor_from_prev<'a>(
    role: &str,
    keep: i32,
    prev_newest_first: impl Iterator<Item = (&'a str, i32)>,
) -> Option<Anchor> {
    if !pinnable(role, keep) {
        return None;
    }
    let seq = prev_newest_first
        .take_while(|(_, k)| *k == keep)
        .filter(|(r, _)| *r == role)
        .count() as u32;
    Some(Anchor {
        keep: i64::from(keep),
        role: role.to_string(),
        seq,
    })
}

/// Anchor of row `id`, when it is pinnable.
pub fn anchor_of(rows: &[Row<'_>], id: u32) -> Option<Anchor> {
    anchors(rows)
        .into_iter()
        .find(|(rid, _)| *rid == id)
        .map(|(_, a)| a)
}

/// Stored excerpt: whitespace collapsed, cut at [`EXCERPT_MAX`].
pub fn excerpt(text: &str) -> String {
    let flat = text.split_whitespace().collect::<Vec<_>>().join(" ");
    flat.chars().take(EXCERPT_MAX).collect()
}

/// The row a pin points at, or `None` when its anchor no longer lands
/// on the same text.
pub fn resolve(pin: &Pin, rows: &[Row<'_>]) -> Option<u32> {
    let id = anchors(rows)
        .into_iter()
        .find(|(_, a)| *a == pin.anchor)
        .map(|(id, _)| id)?;
    let row = rows.iter().find(|r| r.id == id)?;
    (excerpt(row.text) == pin.excerpt).then_some(id)
}

/// Is `anchor` pinned?
pub fn is_pinned(pins: &[Pin], anchor: &Anchor) -> bool {
    pins.iter().any(|p| p.anchor == *anchor)
}

/// One pin rail entry.
#[derive(Clone, Debug, PartialEq)]
pub struct RailItem {
    pub anchor: Anchor,
    pub label: String,
    /// Row to jump to; `None` renders the honest unresolved state.
    pub target: Option<u32>,
}

fn role_label(role: &str) -> &'static str {
    if role == "you" {
        "You"
    } else {
        "Strike"
    }
}

/// Rail label: role and a short excerpt.
pub fn label(pin: &Pin) -> String {
    let mut short: String = pin.excerpt.chars().take(LABEL_MAX).collect();
    if pin.excerpt.chars().count() > LABEL_MAX {
        short.push('…');
    }
    format!("{}: {short}", role_label(&pin.anchor.role))
}

/// Rail rows for `pins` against the loaded transcript, in pin order.
/// Empty pins → empty rail (the rail renders nothing at all).
pub fn rail(pins: &[Pin], rows: &[Row<'_>]) -> Vec<RailItem> {
    pins.iter()
        .map(|p| RailItem {
            anchor: p.anchor.clone(),
            label: label(p),
            target: resolve(p, rows),
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn row<'a>(id: u32, role: &'a str, keep: i32, text: &'a str) -> Row<'a> {
        Row { id, role, keep, text }
    }

    fn sample() -> Vec<Row<'static>> {
        vec![
            row(1, "you", 4, "why does it fail"),
            row(2, "strike", 4, "Let me look."),
            row(3, "tool", 4, "Read parse.rs"),
            row(4, "strike", 4, "The bug is in parse()."),
            row(5, "you", 9, "fix it"),
            row(6, "strike", 9, "Done."),
            row(7, "strike", -1, "streaming…"),
            row(8, "notice", 9, "note"),
        ]
    }

    fn anchor(keep: i64, role: &str, seq: u32) -> Anchor {
        Anchor {
            keep,
            role: role.into(),
            seq,
        }
    }

    fn pin_for(rows: &[Row<'_>], id: u32) -> Pin {
        let r = rows.iter().find(|r| r.id == id).unwrap();
        Pin {
            anchor: anchor_of(rows, id).unwrap(),
            excerpt: excerpt(r.text),
            at_ms: 0.0,
        }
    }

    #[test]
    fn only_settled_prose_is_pinnable() {
        let cases = [
            ("you", 0, true),
            ("strike", 3, true),
            ("strike", -1, false),
            ("tool", 3, false),
            ("child", 3, false),
            ("notice", 3, false),
            ("permission", 3, false),
        ];
        for (role, keep, want) in cases {
            assert_eq!(pinnable(role, keep), want, "{role} {keep}");
        }
    }

    #[test]
    fn anchors_number_replies_within_a_turn() {
        let got = anchors(&sample());
        assert_eq!(
            got,
            vec![
                (1, anchor(4, "you", 0)),
                (2, anchor(4, "strike", 0)),
                (4, anchor(4, "strike", 1)),
                (5, anchor(9, "you", 0)),
                (6, anchor(9, "strike", 0)),
            ]
        );
        assert_eq!(anchor_of(&sample(), 3), None);
        assert_eq!(anchor_of(&sample(), 7), None);
    }

    #[test]
    fn per_row_anchor_agrees_with_the_full_scan() {
        let rows = sample();
        let full = anchors(&rows);
        for (idx, r) in rows.iter().enumerate() {
            let prev = rows[..idx].iter().rev().map(|p| (p.role, p.keep));
            let one = anchor_from_prev(r.role, r.keep, prev);
            let want = full.iter().find(|(id, _)| *id == r.id).map(|(_, a)| a.clone());
            assert_eq!(one, want, "row {}", r.id);
        }
    }

    #[test]
    fn pin_resolves_after_replay_with_new_ids() {
        let rows = sample();
        let pin = pin_for(&rows, 4);
        // Same log replayed: every Message.id shifted, anchors did not.
        let replayed: Vec<Row<'static>> = sample()
            .into_iter()
            .map(|r| Row { id: r.id + 100, ..r })
            .collect();
        assert_eq!(resolve(&pin, &replayed), Some(104));
    }

    #[test]
    fn pin_whose_anchor_vanished_is_unresolved_not_repointed() {
        let rows = sample();
        let pin = pin_for(&rows, 4);
        // Rewound past the turn: only the first reply is left.
        let rewound = vec![row(1, "you", 4, "why does it fail"), row(2, "strike", 4, "Let me look.")];
        assert_eq!(resolve(&pin, &rewound), None);
        // A different message now sits at the same anchor: refuse.
        let edited = vec![
            row(1, "you", 4, "why does it fail"),
            row(2, "strike", 4, "Let me look."),
            row(4, "strike", 4, "Something else entirely."),
        ];
        assert_eq!(resolve(&pin, &edited), None);
        assert_eq!(resolve(&pin, &[]), None);
    }

    #[test]
    fn excerpt_matches_the_daemon_clip() {
        assert_eq!(excerpt("  a \n\n b\tc  "), "a b c");
        let long = "é".repeat(EXCERPT_MAX + 5);
        assert_eq!(excerpt(&long).chars().count(), EXCERPT_MAX);
        // Clipping an excerpt again is a no-op (the daemon re-clips).
        let once = excerpt(&long);
        assert_eq!(excerpt(&once), once);
    }

    #[test]
    fn rail_lists_resolved_and_unresolved_pins() {
        let rows = sample();
        assert!(rail(&[], &rows).is_empty());
        let kept = pin_for(&rows, 1);
        let lost = Pin {
            anchor: anchor(99, "strike", 0),
            excerpt: "gone".into(),
            at_ms: 0.0,
        };
        let items = rail(&[kept.clone(), lost.clone()], &rows);
        assert_eq!(items.len(), 2);
        assert_eq!(items[0].target, Some(1));
        assert_eq!(items[0].label, "You: why does it fail");
        assert_eq!(items[1].target, None);
        assert_eq!(items[1].label, "Strike: gone");
        assert!(is_pinned(&[kept.clone()], &kept.anchor));
        assert!(!is_pinned(&[kept], &lost.anchor));
    }

    #[test]
    fn label_is_short() {
        let pin = Pin {
            anchor: anchor(1, "strike", 0),
            excerpt: "x".repeat(LABEL_MAX + 10),
            at_ms: 0.0,
        };
        let l = label(&pin);
        assert!(l.starts_with("Strike: "));
        assert!(l.ends_with('…'));
        assert_eq!(l.chars().count(), "Strike: ".len() + LABEL_MAX + 1);
    }
}
