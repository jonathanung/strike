//! Golden event stream and unknown-type behavior for the generated wire types.
//! The JSONL file is produced by `make protocol-schema` from pkg/protocol.

use protocol_schema::{decode_event, decode_op, Event, Op};

#[test]
fn golden_event_stream_decodes() {
    let raw = include_str!("../../../schemas/protocol/v1/golden-events.jsonl");
    let lines: Vec<&str> = raw.lines().filter(|line| !line.is_empty()).collect();
    assert_eq!(lines.len(), 6, "golden event stream");

    match decode_event(lines[0]).expect("user.message") {
        Event::UserMessage(msg) => assert_eq!(msg.text, "hello"),
        other => panic!("user.message decoded as {other:?}"),
    }
    assert!(
        lines[0].contains("futureField"),
        "unknown field must stay on the wire and still decode"
    );

    match decode_event(lines[1]).expect("text.delta") {
        Event::TextDelta(delta) => assert_eq!(delta.text, "hi"),
        other => panic!("text.delta decoded as {other:?}"),
    }

    match decode_event(lines[2]).expect("tool.begin") {
        Event::ToolCallBegin(call) => {
            assert_eq!(call.name, "bash");
            assert_eq!(call.args["command"], "echo");
        }
        other => panic!("tool.begin decoded as {other:?}"),
    }

    match decode_event(lines[3]).expect("turn.completed") {
        Event::TurnCompleted(done) => assert_eq!(done.stop_reason, "end_turn"),
        other => panic!("turn.completed decoded as {other:?}"),
    }

    match decode_event(lines[4]).expect("child.completed") {
        Event::ChildCompleted(child) => {
            assert_eq!(child.status.0, "completed");
            assert_eq!(child.summary, "done");
            assert!(child.handoff.files_changed.is_empty());
        }
        other => panic!("child.completed decoded as {other:?}"),
    }

    match decode_event(lines[5]).expect("unknown event") {
        Event::Unknown { type_name, data } => {
            assert_eq!(type_name, "future.extension");
            assert_eq!(data["ok"], true);
            assert_eq!(data["span"], "gate");
        }
        other => panic!("unknown event decoded as {other:?}"),
    }
}

#[test]
fn unknown_event_and_bad_known_event() {
    let unknown = decode_event(
        r#"{"type":"future.extension","time":"2026-01-02T03:04:05Z","data":{"span":"gate"}}"#,
    )
    .expect("unknown");
    match unknown {
        Event::Unknown { type_name, .. } => assert_eq!(type_name, "future.extension"),
        other => panic!("{other:?}"),
    }

    let err = decode_event(r#"{"type":"","time":"2026-01-02T03:04:05Z","data":{}}"#).unwrap_err();
    assert!(err.to_string().contains("empty"), "{err}");

    let err = decode_event(
        r#"{"type":"text.delta","time":"2026-01-02T03:04:05Z","v":"1.18.0","data":{"text":1}}"#,
    )
    .unwrap_err();
    assert!(err.to_string().contains("text.delta"), "{err}");
}

#[test]
fn unknown_op_fails_and_interrupt_decodes() {
    let err = decode_op(r#"{"type":"future.op","v":"1.18.0"}"#).unwrap_err();
    assert!(err.to_string().contains("unknown op"), "{err}");

    match decode_op(r#"{"type":"interrupt","v":"1.18.0"}"#).expect("interrupt") {
        Op::Interrupt(_) => {}
        other => panic!("{other:?}"),
    }

    match decode_op(r#"{"type":"user.input","data":{"text":"hi","futureField":true}}"#)
        .expect("user.input")
    {
        Op::UserInput(input) => assert_eq!(input.text, "hi"),
        other => panic!("{other:?}"),
    }
}

// #1827: a /loop firing carries loopId on user.input and user.message; a
// line without it (older logs, ordinary sends) decodes to empty.
#[test]
fn loop_id_decodes_and_defaults_empty() {
    let cases = [
        (r#"{"type":"user.message","v":"1.26.0","data":{"text":"check ci","loopId":"l1"}}"#, "l1"),
        (r#"{"type":"user.message","v":"1.25.0","data":{"text":"hello"}}"#, ""),
    ];
    for (raw, want) in cases {
        match decode_event(raw).expect("user.message") {
            Event::UserMessage(msg) => assert_eq!(msg.loop_id, want, "{raw}"),
            other => panic!("user.message decoded as {other:?}"),
        }
    }
    match decode_op(r#"{"type":"user.input","data":{"text":"check ci","loopId":"l2"}}"#)
        .expect("user.input")
    {
        Op::UserInput(input) => assert_eq!(input.loop_id, "l2"),
        other => panic!("{other:?}"),
    }
}

// #1944: loops.changed carries a live session's whole /loop list on the
// stream. A null list (Go nil slice) is empty, unknown fields are ignored,
// and an older reader sees it as Event::Unknown rather than an error.
#[test]
fn loops_changed_decodes() {
    let raw = r#"{"type":"loops.changed","time":"2026-11-21T10:00:00Z","v":"1.27.0","data":{"sessionId":"s1","reason":"error","loopId":"l2","futureField":1,"loops":[{"id":"l2","interval":"30s","intervalMs":30000,"job":"ping","runs":0,"createdAt":"2026-11-21T09:59:00Z","nextAt":"2026-11-21T10:00:30Z","lastRunAt":"2026-11-21T10:00:00Z","lastError":"input queue full"}]}}"#;
    match decode_event(raw).expect("loops.changed") {
        Event::LoopsChanged(ev) => {
            assert_eq!((ev.session_id.as_str(), ev.reason.as_str(), ev.loop_id.as_str()), ("s1", "error", "l2"));
            assert_eq!(ev.loops.len(), 1);
            let l = &ev.loops[0];
            assert_eq!((l.id.as_str(), l.interval_ms, l.runs), ("l2", 30000, 0));
            assert_eq!(l.last_error, "input queue full");
            assert_eq!(l.next_at, "2026-11-21T10:00:30Z");
        }
        other => panic!("loops.changed decoded as {other:?}"),
    }
    let stop_all = r#"{"type":"loops.changed","time":"2026-11-21T10:00:00Z","data":{"sessionId":"s1","reason":"stop","loops":null}}"#;
    match decode_event(stop_all).expect("stop all") {
        Event::LoopsChanged(ev) => {
            assert!(ev.loops.is_empty());
            assert!(ev.loop_id.is_empty());
        }
        other => panic!("{other:?}"),
    }
}
