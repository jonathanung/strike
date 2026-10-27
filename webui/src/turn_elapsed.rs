//! Turn elapsed time in the transcript — the deferred §3 of #1349,
//! tracked as #1364.
//!
//! A turn's elapsed time is the span from its first line to its last:
//! the user message that opened the turn through the turn's completion.
//! It is shown once, on the turn's final line, and only when the span
//! exceeds one minute. Below the threshold nothing renders, so short
//! turns stay visually identical to before.
//!
//! The computation is pure over the envelope times #1351 already plumbed
//! onto every message (`Message::at_ms`, epoch ms; `0.0` means unknown).
//! No new data, no daemon/protocol change, and no live ticking: a turn
//! still in progress has not completed, so it carries no label — the
//! label is stamped once, at `turn.completed` (see
//! `Transcript::stamp_turn`), from that envelope's own time. Replayed
//! JSONL lines keep their original envelope times, so the span is
//! correct across a gateway restart.
//!
//! Degradation rule (the #1364 error path): if ANY line of the turn has
//! an unknown time — an old log line whose envelope `time` is missing or
//! unparseable — the turn gets no label at all, never a partial or
//! absurd duration, and the rest of the transcript is unaffected.

use crate::status::fmt_elapsed;

/// A turn must exceed this span (ms) before its elapsed time is shown.
/// Exactly one minute is NOT shown: the spec is "exceeds a minute".
pub const TURN_ELAPSED_THRESHOLD_MS: f64 = 60_000.0;

/// The label for one turn, or `None` when nothing should render.
///
/// `first_ms` is the opening (user) line's envelope time, `last_ms` the
/// turn's final envelope time (the `turn.completed` time), and
/// `all_timed` reports whether every line of the turn carried a known
/// time. The wording reuses the UI's existing duration vocabulary —
/// [`fmt_elapsed`], the same formatter the status model uses for
/// session elapsed — prefixed with "took", as in #1364's example
/// ("took 4m 12s" in that vocabulary is `took 4:12`). No second format.
pub fn turn_elapsed_label(first_ms: f64, last_ms: f64, all_timed: bool) -> Option<String> {
    if !all_timed || first_ms <= 0.0 || last_ms <= 0.0 {
        return None;
    }
    let span_ms = last_ms - first_ms;
    if span_ms <= TURN_ELAPSED_THRESHOLD_MS {
        return None;
    }
    let secs = (span_ms / 1000.0) as i64;
    Some(format!("took {}", fmt_elapsed(secs)))
}

#[cfg(test)]
mod tests {
    use super::*;

    const T0: f64 = 1_761_000_000_000.0; // arbitrary epoch-ms anchor

    #[test]
    fn over_a_minute_shows_once_with_shared_vocabulary() {
        // 4m 12s of span, in fmt_elapsed's vocabulary.
        assert_eq!(
            turn_elapsed_label(T0, T0 + 252_000.0, true),
            Some("took 4:12".to_string())
        );
        // Hours roll into h:mm:ss, exactly as fmt_elapsed renders them.
        assert_eq!(
            turn_elapsed_label(T0, T0 + 3_661_000.0, true),
            Some("took 1:01:01".to_string())
        );
    }

    #[test]
    fn exactly_one_minute_is_under_the_threshold() {
        assert_eq!(turn_elapsed_label(T0, T0 + 60_000.0, true), None);
        // Just over the line shows, floored to whole seconds.
        assert_eq!(
            turn_elapsed_label(T0, T0 + 61_000.0, true),
            Some("took 1:01".to_string())
        );
        assert_eq!(turn_elapsed_label(T0, T0 + 59_999.0, true), None);
        assert_eq!(turn_elapsed_label(T0, T0, true), None);
    }

    #[test]
    fn missing_times_degrade_to_no_label() {
        // Unknown first or last time (old log lines): no label.
        assert_eq!(turn_elapsed_label(0.0, T0 + 300_000.0, true), None);
        assert_eq!(turn_elapsed_label(T0, 0.0, true), None);
        // Any untimed line inside the turn suppresses the label, even
        // when the endpoints are known and the span is long.
        assert_eq!(turn_elapsed_label(T0, T0 + 300_000.0, false), None);
    }

    #[test]
    fn backwards_or_equal_times_never_show() {
        // Clock skew / replay disorder must not produce a duration.
        assert_eq!(turn_elapsed_label(T0 + 300_000.0, T0, true), None);
    }
}
