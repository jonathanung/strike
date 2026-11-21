//! Agent plan checklist (#1354): the session's live `todowrite` list as a
//! pinned, collapsible Plan card above the composer.
//!
//! The engine already returns the whole list as `tool.end` metadata
//! (`{"todos": [{id, content, status}]}`) from both `todowrite` and
//! `todoread`. `tool.end` carries no tool name, so the card remembers the
//! `callId`s whose `tool.begin` named one of those tools and reads only their
//! ends. The model-facing output text is never scraped.
//!
//! A later write replaces the list (same as the engine's `TodoStore`). The
//! live stream replays the session from the start on load, so a reload
//! rebuilds the last list. Only root-session events count: a delegated
//! child's own list (`parentSessionId` set) is ignored in this slice. An
//! empty list hides the card. A malformed or legacy payload is ignored and
//! the previous list stays.

use std::collections::HashSet;

use leptos::prelude::*;
use serde_json::Value;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Status {
    Pending,
    InProgress,
    Done,
    Cancelled,
}

impl Status {
    pub fn parse(raw: &str) -> Status {
        match raw {
            "in_progress" => Status::InProgress,
            "completed" => Status::Done,
            "cancelled" | "canceled" => Status::Cancelled,
            _ => Status::Pending,
        }
    }

    pub fn class(self) -> &'static str {
        match self {
            Status::Pending => "plan-item pending",
            Status::InProgress => "plan-item in-progress",
            Status::Done => "plan-item done",
            Status::Cancelled => "plan-item cancelled",
        }
    }

    /// Text glyph plus a screen-reader word; no icon font.
    pub fn mark(self) -> (&'static str, &'static str) {
        match self {
            Status::Pending => ("[ ]", "pending"),
            Status::InProgress => ("[>]", "in progress"),
            Status::Done => ("[x]", "done"),
            Status::Cancelled => ("[-]", "cancelled"),
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PlanItem {
    pub id: String,
    pub content: String,
    pub status: Status,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct PlanState {
    pub items: Vec<PlanItem>,
    todo_calls: HashSet<String>,
}

const TODO_TOOLS: [&str; 2] = ["todowrite", "todoread"];

fn s<'a>(v: &'a Value, key: &str) -> &'a str {
    v.get(key).and_then(|x| x.as_str()).unwrap_or("")
}

fn is_child(data: &Value) -> bool {
    !s(data, "parentSessionId").is_empty() || data.get("depth").and_then(|d| d.as_i64()).unwrap_or(0) > 0
}

/// The list in one `tool.end` metadata object, or `None` when it is not a
/// todo payload (missing `todos`, not an array). Rows without text are
/// dropped; an unknown status reads as pending.
pub fn todos_from_metadata(meta: &Value) -> Option<Vec<PlanItem>> {
    let rows = meta.get("todos")?.as_array()?;
    Some(
        rows.iter()
            .filter_map(|row| {
                let content = s(row, "content").trim();
                if content.is_empty() {
                    return None;
                }
                Some(PlanItem {
                    id: s(row, "id").to_string(),
                    content: content.to_string(),
                    status: Status::parse(s(row, "status")),
                })
            })
            .collect(),
    )
}

impl PlanState {
    /// Applies one event envelope. Returns true when the list changed.
    pub fn apply(&mut self, typ: &str, data: &Value) -> bool {
        if is_child(data) {
            return false;
        }
        match typ {
            "tool.begin" => {
                if TODO_TOOLS.contains(&s(data, "name")) {
                    let id = s(data, "callId");
                    if !id.is_empty() {
                        self.todo_calls.insert(id.to_string());
                    }
                }
                false
            }
            "tool.end" => {
                let id = s(data, "callId");
                if id.is_empty() || !self.todo_calls.remove(id) {
                    return false;
                }
                if data.get("isError").and_then(|v| v.as_bool()).unwrap_or(false) {
                    return false;
                }
                let Some(meta) = data.get("metadata") else {
                    return false;
                };
                let Some(items) = todos_from_metadata(meta) else {
                    return false;
                };
                if items == self.items {
                    return false;
                }
                self.items = items;
                true
            }
            _ => false,
        }
    }

    pub fn apply_envelope(&mut self, raw: &str) -> bool {
        let Ok(v) = serde_json::from_str::<Value>(raw) else {
            return false;
        };
        let typ = s(&v, "type").to_string();
        let data = v.get("data").cloned().unwrap_or(Value::Null);
        self.apply(&typ, &data)
    }

    /// Done count over the steps still in play (cancelled ones excluded).
    pub fn summary(&self) -> String {
        let total = self.items.iter().filter(|i| i.status != Status::Cancelled).count();
        let done = self.items.iter().filter(|i| i.status == Status::Done).count();
        format!("{done}/{total} steps")
    }

    pub fn current(&self) -> Option<&PlanItem> {
        self.items.iter().find(|i| i.status == Status::InProgress)
    }
}

#[component]
pub fn PlanCard(plan: RwSignal<PlanState>) -> impl IntoView {
    // Phone widths start collapsed to the one-line summary.
    let open = RwSignal::new(!crate::mobile::is_phone());
    view! {
        <Show when=move || plan.with(|p| !p.items.is_empty())>
            <section class="plan-card" data-feature="todo-checklist" aria-label="Agent plan">
                <button type="button" class="plan-head" aria-expanded=move || open.get().to_string() on:click=move |_| open.update(|o| *o = !*o)>
                    <span class="plan-title">"Plan"</span>
                    <span class="plan-count">{move || plan.with(|p| p.summary())}</span>
                    <Show when=move || !open.get()>
                        <span class="plan-now">{move || plan.with(|p| p.current().map(|i| i.content.clone()).unwrap_or_default())}</span>
                    </Show>
                </button>
                <Show when=move || open.get()>
                    <ol class="plan-list">
                        {move || plan.with(|p| p.items.iter().map(|item| {
                            let (glyph, word) = item.status.mark();
                            view! {
                                <li class=item.status.class()>
                                    <span class="plan-mark" aria-hidden="true">{glyph}</span>
                                    <span class="sr-only">{format!("{word}: ")}</span>
                                    <span class="plan-text">{item.content.clone()}</span>
                                </li>
                            }
                        }).collect_view())}
                    </ol>
                </Show>
            </section>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn begin(st: &mut PlanState, id: &str, name: &str) {
        st.apply("tool.begin", &json!({"callId": id, "name": name}));
    }

    fn end(st: &mut PlanState, id: &str, todos: Value) -> bool {
        st.apply("tool.end", &json!({"callId": id, "metadata": {"todos": todos}}))
    }

    #[test]
    fn status_mapping() {
        let cases = [
            ("pending", Status::Pending),
            ("in_progress", Status::InProgress),
            ("completed", Status::Done),
            ("cancelled", Status::Cancelled),
            ("bogus", Status::Pending),
            ("", Status::Pending),
        ];
        for (raw, want) in cases {
            assert_eq!(Status::parse(raw), want, "{raw}");
        }
        assert_eq!(Status::InProgress.mark().1, "in progress");
        assert!(Status::Done.class().contains("done"));
    }

    #[test]
    fn later_write_replaces_the_list() {
        let mut st = PlanState::default();
        begin(&mut st, "c1", "todowrite");
        assert!(end(&mut st, "c1", json!([
            {"id": "1", "content": "read code", "status": "in_progress"},
            {"id": "2", "content": "write test", "status": "pending"},
            {"id": "3", "content": "ship", "status": "pending"},
        ])));
        assert_eq!(st.items.len(), 3);
        assert_eq!(st.summary(), "0/3 steps");
        assert_eq!(st.current().unwrap().content, "read code");

        begin(&mut st, "c2", "todowrite");
        assert!(end(&mut st, "c2", json!([
            {"id": "1", "content": "read code", "status": "completed"},
            {"id": "4", "content": "new step", "status": "in_progress"},
        ])));
        assert_eq!(st.items.len(), 2, "replace, not merge");
        assert_eq!(st.items[1].content, "new step");
        assert_eq!(st.summary(), "1/2 steps");

        // todoread reports the same list: no change.
        begin(&mut st, "c3", "todoread");
        assert!(!end(&mut st, "c3", json!([
            {"id": "1", "content": "read code", "status": "completed"},
            {"id": "4", "content": "new step", "status": "in_progress"},
        ])));
    }

    #[test]
    fn empty_list_hides_and_cancelled_is_not_counted() {
        let mut st = PlanState::default();
        begin(&mut st, "c1", "todowrite");
        end(&mut st, "c1", json!([
            {"id": "1", "content": "a", "status": "completed"},
            {"id": "2", "content": "b", "status": "cancelled"},
        ]));
        assert_eq!(st.summary(), "1/1 steps");
        begin(&mut st, "c2", "todowrite");
        assert!(end(&mut st, "c2", json!([])));
        assert!(st.items.is_empty(), "an empty write clears the card");
    }

    #[test]
    fn only_todo_calls_from_the_root_session_count() {
        let mut st = PlanState::default();
        // An end with todo-shaped metadata from another tool is ignored.
        begin(&mut st, "x", "bash");
        assert!(!end(&mut st, "x", json!([{"id": "1", "content": "a", "status": "pending"}])));
        // An end with no matching begin is ignored.
        assert!(!end(&mut st, "ghost", json!([{"id": "1", "content": "a", "status": "pending"}])));
        // A child session's list is ignored.
        st.apply("tool.begin", &json!({"callId": "k", "name": "todowrite", "parentSessionId": "root"}));
        assert!(!st.apply(
            "tool.end",
            &json!({"callId": "k", "parentSessionId": "root", "metadata": {"todos": [{"id": "1", "content": "a"}]}})
        ));
        // A failed write changes nothing.
        begin(&mut st, "f", "todowrite");
        assert!(!st.apply("tool.end", &json!({"callId": "f", "isError": true, "metadata": {"todos": [{"id": "1", "content": "a"}]}})));
        assert!(st.items.is_empty());
    }

    #[test]
    fn malformed_payload_keeps_the_previous_list() {
        let mut st = PlanState::default();
        begin(&mut st, "c1", "todowrite");
        end(&mut st, "c1", json!([{"id": "1", "content": "keep me", "status": "pending"}]));
        for bad in [
            json!({"callId": "c2", "metadata": {"todos": "not a list"}}),
            json!({"callId": "c2", "metadata": {"items": []}}),
            json!({"callId": "c2", "metadata": null}),
            json!({"callId": "c2"}),
        ] {
            begin(&mut st, "c2", "todowrite");
            assert!(!st.apply("tool.end", &bad), "{bad}");
            assert_eq!(st.items[0].content, "keep me");
        }
        // Rows without text are dropped; the rest render.
        let items = todos_from_metadata(&json!({"todos": [{"id": "1"}, {"content": "  x  ", "status": 7}]})).unwrap();
        assert_eq!(items, vec![PlanItem { id: String::new(), content: "x".into(), status: Status::Pending }]);
    }

    #[test]
    fn reload_replay_rebuilds_the_last_list() {
        let log = [
            r#"{"type":"turn.started","data":{"sessionId":"s"}}"#,
            r#"{"type":"tool.begin","data":{"callId":"a","name":"todowrite","args":{}}}"#,
            r#"{"type":"tool.end","data":{"callId":"a","title":"2 todos","output":"[]","metadata":{"todos":[{"id":"1","content":"one","status":"in_progress"},{"id":"2","content":"two","status":"pending"}]}}}"#,
            "not json",
            r#"{"type":"tool.begin","data":{"callId":"b","name":"todowrite","args":{}}}"#,
            r#"{"type":"tool.end","data":{"callId":"b","title":"1 todos","output":"[]","metadata":{"todos":[{"id":"1","content":"one","status":"completed"},{"id":"2","content":"two","status":"in_progress"}]}}}"#,
        ];
        let mut st = PlanState::default();
        for line in log {
            st.apply_envelope(line);
        }
        assert_eq!(st.summary(), "1/2 steps");
        assert_eq!(st.current().unwrap().content, "two");
    }
}
