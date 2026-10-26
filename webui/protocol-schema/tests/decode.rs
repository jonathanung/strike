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
