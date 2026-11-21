//! Agent questions (#1338): the pure half of the question card.
//!
//! The engine's `question` tool emits `question.asked` with one to four
//! prompts and suspends until a `question.reply` op arrives with one answer
//! per prompt, in order. An empty answer list is a dismissal: the question
//! service returns `RejectedError` and the engine ends the turn. The daemon
//! closes every ask with `question.resolved` (answer, dismiss, or cancel).
//!
//! The protocol has no multi-select flag: a prompt with options takes one
//! choice or the user's own text, like the TUI question modal.

use serde_json::{json, Value};

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Choice {
    pub label: String,
    pub description: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Prompt {
    pub id: String,
    pub header: String,
    pub question: String,
    pub options: Vec<Choice>,
}

/// One pending `question.asked` batch.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Pending {
    pub request_id: String,
    pub session_id: String,
    pub prompts: Vec<Prompt>,
}

#[derive(Debug, PartialEq, Eq)]
pub enum QuestionNote {
    Ask(Pending),
    Clear(String),
}

/// Question notes carried by one live event envelope.
pub fn notes_from_envelope(raw: &str) -> Vec<QuestionNote> {
    let v: Value = match serde_json::from_str(raw) {
        Ok(v) => v,
        Err(_) => return Vec::new(),
    };
    let data = v.get("data").cloned().unwrap_or(Value::Null);
    match v.get("type").and_then(Value::as_str).unwrap_or("") {
        "question.asked" => parse_pending(&data).map(QuestionNote::Ask).into_iter().collect(),
        "question.resolved" => text(&data, "requestId")
            .filter(|id| !id.is_empty())
            .map(QuestionNote::Clear)
            .into_iter()
            .collect(),
        _ => Vec::new(),
    }
}

/// Apply one note to the pending list. A re-sent ask replaces its earlier
/// copy so a replayed log does not stack duplicate cards.
pub fn apply_note(list: &mut Vec<Pending>, note: QuestionNote) {
    match note {
        QuestionNote::Ask(pending) => {
            list.retain(|row| row.request_id != pending.request_id);
            list.push(pending);
        }
        QuestionNote::Clear(id) => list.retain(|row| row.request_id != id),
    }
}

fn parse_pending(data: &Value) -> Option<Pending> {
    let request_id = text(data, "requestId").filter(|id| !id.is_empty())?;
    let prompts: Vec<Prompt> = data
        .get("questions")
        .and_then(Value::as_array)
        .map(|rows| rows.iter().filter_map(parse_prompt).collect())
        .unwrap_or_default();
    if prompts.is_empty() {
        return None;
    }
    Some(Pending {
        request_id,
        session_id: text(data, "sessionId").unwrap_or_default(),
        prompts,
    })
}

fn parse_prompt(row: &Value) -> Option<Prompt> {
    let question = text(row, "question").filter(|q| !q.trim().is_empty())?;
    let options = row
        .get("options")
        .and_then(Value::as_array)
        .map(|opts| {
            opts.iter()
                .filter_map(|o| {
                    let label = text(o, "label").filter(|l| !l.trim().is_empty())?;
                    Some(Choice {
                        label,
                        description: text(o, "description").unwrap_or_default(),
                    })
                })
                .collect()
        })
        .unwrap_or_default();
    Some(Prompt {
        id: text(row, "id").unwrap_or_default(),
        header: text(row, "header").unwrap_or_default(),
        question,
        options,
    })
}

fn text(data: &Value, key: &str) -> Option<String> {
    data.get(key).and_then(Value::as_str).map(str::to_string)
}

/// What the user picked for one prompt.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Pick {
    #[default]
    None,
    Option(usize),
    Own,
}

/// In-progress answers for one card.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Draft {
    pub picks: Vec<Pick>,
    pub own: Vec<String>,
}

impl Draft {
    pub fn new(n: usize) -> Self {
        Self {
            picks: vec![Pick::None; n],
            own: vec![String::new(); n],
        }
    }

    pub fn pick(&mut self, i: usize, pick: Pick) {
        if let Some(slot) = self.picks.get_mut(i) {
            *slot = pick;
        }
    }

    /// Typing your own answer selects it.
    pub fn set_own(&mut self, i: usize, text: String) {
        if let Some(slot) = self.own.get_mut(i) {
            *slot = text;
        }
        self.pick(i, Pick::Own);
    }

    pub fn picked(&self, i: usize) -> Pick {
        self.picks.get(i).copied().unwrap_or_default()
    }

    pub fn own_text(&self, i: usize) -> String {
        self.own.get(i).cloned().unwrap_or_default()
    }
}

/// The answer string for prompt `i`, or None while it is unanswered.
/// A free-form prompt (no options) always reads the typed text.
pub fn answer(prompt: &Prompt, draft: &Draft, i: usize) -> Option<String> {
    let own = || {
        let t = draft.own_text(i).trim().to_string();
        (!t.is_empty()).then_some(t)
    };
    if prompt.options.is_empty() {
        return own();
    }
    match draft.picked(i) {
        Pick::Option(j) => prompt.options.get(j).map(|c| c.label.clone()),
        Pick::Own => own(),
        Pick::None => None,
    }
}

/// Every answer in prompt order, or the index of the first unanswered
/// prompt. A reply must carry exactly one answer per prompt.
pub fn collect_answers(pending: &Pending, draft: &Draft) -> Result<Vec<String>, usize> {
    pending
        .prompts
        .iter()
        .enumerate()
        .map(|(i, p)| answer(p, draft, i).ok_or(i))
        .collect()
}

/// `question.reply` op data for a submitted card.
pub fn reply_data(request_id: &str, answers: &[String]) -> Value {
    json!({"requestId": request_id, "answers": answers})
}

/// `question.reply` op data for a dismissal. Empty answers are the
/// engine's dismiss signal: the tool fails as rejected and the turn ends.
pub fn dismiss_data(request_id: &str) -> Value {
    json!({"requestId": request_id, "answers": []})
}

/// Short title for prompt `i` of `n`: its header, else "Question i of n".
pub fn step_label(prompt: &Prompt, i: usize, n: usize) -> String {
    let header = prompt.header.trim();
    if n <= 1 {
        return if header.is_empty() { "Question".into() } else { header.to_string() };
    }
    if header.is_empty() {
        format!("Question {} of {}", i + 1, n)
    } else {
        format!("{header} · {} of {}", i + 1, n)
    }
}

/// Digit key `1`..`9` mapped to an option index on `prompt`; the digit
/// after the last option selects "Your own answer".
pub fn digit_pick(prompt: &Prompt, key: &str) -> Option<Pick> {
    if prompt.options.is_empty() {
        return None;
    }
    let n: usize = key.parse().ok()?;
    let i = n.checked_sub(1)?;
    if i < prompt.options.len() {
        Some(Pick::Option(i))
    } else if i == prompt.options.len() {
        Some(Pick::Own)
    } else {
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const ASKED: &str = r#"{"type":"question.asked","data":{"sessionId":"root1","requestId":"root1:q_1","questions":[
        {"id":"db","header":"Database","question":"Which database?","options":[
            {"label":"Postgres (Recommended)","description":"Matches prod"},
            {"label":"SQLite"}]},
        {"id":"note","question":"Anything else?"}]}}"#;

    fn pending() -> Pending {
        match notes_from_envelope(ASKED).pop() {
            Some(QuestionNote::Ask(p)) => p,
            other => panic!("unexpected {other:?}"),
        }
    }

    #[test]
    fn asked_envelope_parses_prompts_options_and_descriptions() {
        let p = pending();
        assert_eq!(p.request_id, "root1:q_1");
        assert_eq!(p.session_id, "root1");
        assert_eq!(p.prompts.len(), 2);
        assert_eq!(p.prompts[0].header, "Database");
        assert_eq!(p.prompts[0].options[0].label, "Postgres (Recommended)");
        assert_eq!(p.prompts[0].options[0].description, "Matches prod");
        assert_eq!(p.prompts[0].options[1].description, "");
        assert!(p.prompts[1].options.is_empty());
    }

    #[test]
    fn malformed_or_empty_asks_render_nothing() {
        for raw in [
            "not json",
            r#"{"type":"question.asked","data":{"questions":[{"question":"q"}]}}"#,
            r#"{"type":"question.asked","data":{"requestId":"r","questions":[]}}"#,
            r#"{"type":"question.asked","data":{"requestId":"r","questions":[{"question":"  "}]}}"#,
            r#"{"type":"permission.asked","data":{"requestId":"r"}}"#,
            r#"{"type":"question.resolved","data":{"requestId":""}}"#,
        ] {
            assert!(notes_from_envelope(raw).is_empty(), "{raw}");
        }
    }

    #[test]
    fn blank_option_labels_are_dropped() {
        let raw = r#"{"type":"question.asked","data":{"requestId":"r","questions":[{"question":"q","options":[{"label":""},{"label":"A"}]}]}}"#;
        let Some(QuestionNote::Ask(p)) = notes_from_envelope(raw).pop() else {
            panic!("no ask");
        };
        assert_eq!(p.prompts[0].options.len(), 1);
        assert_eq!(p.prompts[0].options[0].label, "A");
    }

    #[test]
    fn resolved_clears_and_replayed_asks_do_not_stack() {
        let mut list = Vec::new();
        for note in notes_from_envelope(ASKED) {
            apply_note(&mut list, note);
        }
        for note in notes_from_envelope(ASKED) {
            apply_note(&mut list, note);
        }
        assert_eq!(list.len(), 1);
        let resolved = r#"{"type":"question.resolved","data":{"requestId":"root1:q_1"}}"#;
        for note in notes_from_envelope(resolved) {
            apply_note(&mut list, note);
        }
        assert!(list.is_empty());
    }

    #[test]
    fn answers_follow_picks_and_own_text() {
        let p = pending();
        let mut d = Draft::new(2);
        assert_eq!(collect_answers(&p, &d), Err(0));
        d.pick(0, Pick::Option(0));
        assert_eq!(collect_answers(&p, &d), Err(1));
        d.set_own(1, "   ".into());
        assert_eq!(collect_answers(&p, &d), Err(1), "blank text is not an answer");
        d.set_own(1, " keep the old API ".into());
        assert_eq!(
            collect_answers(&p, &d),
            Ok(vec!["Postgres (Recommended)".to_string(), "keep the old API".to_string()])
        );
        // Your own answer overrides the listed choice.
        d.set_own(0, "MySQL".into());
        assert_eq!(answer(&p.prompts[0], &d, 0).as_deref(), Some("MySQL"));
        // Picking a listed choice again keeps the typed text but uses the label.
        d.pick(0, Pick::Option(1));
        assert_eq!(answer(&p.prompts[0], &d, 0).as_deref(), Some("SQLite"));
    }

    #[test]
    fn out_of_range_picks_are_unanswered() {
        let p = pending();
        let mut d = Draft::new(2);
        d.pick(0, Pick::Option(9));
        assert_eq!(answer(&p.prompts[0], &d, 0), None);
        d.pick(7, Pick::Own);
        assert_eq!(d.picks.len(), 2);
    }

    #[test]
    fn reply_and_dismiss_bodies_match_the_op_schema() {
        let body = reply_data("r1", &["a".to_string(), "b".to_string()]);
        assert_eq!(body, json!({"requestId": "r1", "answers": ["a", "b"]}));
        let dismiss = dismiss_data("r1");
        assert_eq!(dismiss["requestId"], "r1");
        assert_eq!(dismiss["answers"].as_array().map(Vec::len), Some(0));
    }

    #[test]
    fn step_labels_and_digit_keys() {
        let p = pending();
        assert_eq!(step_label(&p.prompts[0], 0, 2), "Database · 1 of 2");
        assert_eq!(step_label(&p.prompts[1], 1, 2), "Question 2 of 2");
        assert_eq!(step_label(&p.prompts[1], 0, 1), "Question");
        assert_eq!(digit_pick(&p.prompts[0], "1"), Some(Pick::Option(0)));
        assert_eq!(digit_pick(&p.prompts[0], "2"), Some(Pick::Option(1)));
        assert_eq!(digit_pick(&p.prompts[0], "3"), Some(Pick::Own));
        assert_eq!(digit_pick(&p.prompts[0], "4"), None);
        assert_eq!(digit_pick(&p.prompts[0], "0"), None);
        assert_eq!(digit_pick(&p.prompts[1], "1"), None, "free-form prompts take text");
    }
}
