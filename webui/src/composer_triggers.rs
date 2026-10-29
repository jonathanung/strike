//! Composer trigger detection for `/command`, `@file`, `$skill`, and `!shell`.
//!
//! Slash rows come from the daemon bootstrap catalog (`composerCommands`).
//! When the shell command registry lands, replace that slice at the call
//! site. Do not hardcode a second command list here.
//!
//! `trigger_status` is the one-line stand-in for the status component.
//! Call it from that component when it lands.

use crate::model::{ComposerCommand, SkillInfo};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    File,
    Command,
    Skill,
    Shell,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Trigger {
    pub kind: Kind,
    pub start: usize,
    pub end: usize,
    pub query: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Suggestion {
    pub id: String,
    pub label: String,
    pub detail: String,
    pub insert: String,
    pub selectable: bool,
    pub replace_all: bool,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PermAsk {
    pub request_id: String,
    pub permission: String,
    pub patterns: String,
}

/// Map a UTF-16 cursor (textarea `selectionStart`) to a Rust byte index.
pub fn utf16_to_byte(text: &str, utf16_index: usize) -> usize {
    let mut units = 0usize;
    for (byte, ch) in text.char_indices() {
        if units >= utf16_index {
            return byte;
        }
        units += ch.len_utf16();
    }
    text.len()
}

fn floor_char(text: &str, index: usize) -> usize {
    let index = index.min(text.len());
    if text.is_char_boundary(index) {
        return index;
    }
    text.char_indices()
        .map(|(i, _)| i)
        .take_while(|i| *i < index)
        .last()
        .unwrap_or(0)
}

pub fn splice_draft(draft: &str, start: usize, end: usize, insert: &str) -> String {
    let start = floor_char(draft, start);
    let end = floor_char(draft, end.max(start));
    let mut out = String::with_capacity(draft.len() + insert.len());
    out.push_str(&draft[..start]);
    out.push_str(insert);
    out.push_str(&draft[end..]);
    out
}

fn is_path_rune(c: char) -> bool {
    c.is_ascii_alphanumeric() || matches!(c, '/' | '\\' | '.' | '-' | '_' | '+' | '~')
}

/// Active trigger touching the cursor.
///
/// `!`, `/`, and `$` count only when the draft itself starts with that
/// character (whitespace aside). `@file` counts after whitespace, matching
/// the daemon. `user@host` is not a mention.
pub fn trigger_at(draft: &str, utf16_cursor: u32) -> Option<Trigger> {
    let cursor = utf16_to_byte(draft, utf16_cursor as usize);
    if cursor == 0 {
        return None;
    }
    let head = &draft[..cursor];
    let msg_start = draft
        .find(|c: char| !c.is_whitespace())
        .unwrap_or(draft.len());
    let head_start = head
        .find(|c: char| !c.is_whitespace())
        .unwrap_or(head.len());
    if head_start < head.len() && head_start == msg_start {
        let ch = head[head_start..].chars().next()?;
        let after = head_start + ch.len_utf8();
        let kind = match ch {
            '!' => Some(Kind::Shell),
            '/' => Some(Kind::Command),
            '$' => Some(Kind::Skill),
            _ => None,
        };
        if let Some(kind) = kind {
            return Some(Trigger {
                kind,
                start: head_start,
                end: cursor,
                query: draft[after..cursor].to_string(),
            });
        }
    }
    let mut i = head.len();
    while i > 0 {
        let ch = head[..i].chars().next_back()?;
        if is_path_rune(ch) {
            i -= ch.len_utf8();
            continue;
        }
        break;
    }
    if i == 0 || !head[..i].ends_with('@') {
        return None;
    }
    let at = i - '@'.len_utf8();
    if at > 0 {
        let prev = head[..at].chars().next_back()?;
        if !prev.is_whitespace() {
            return None;
        }
    }
    Some(Trigger {
        kind: Kind::File,
        start: at,
        end: cursor,
        query: head[i..].to_string(),
    })
}

pub fn slash_suggestions(
    commands: &[ComposerCommand],
    skills: &[SkillInfo],
    query: &str,
) -> Vec<Suggestion> {
    let q = query.trim().trim_start_matches('/').to_ascii_lowercase();
    let mut out = Vec::new();
    for cmd in commands {
        let label = cmd.label.to_ascii_lowercase();
        let insert = cmd.insert.to_ascii_lowercase();
        if q.is_empty() || label.contains(&q) || insert.contains(&q) {
            out.push(Suggestion {
                id: cmd.id.clone(),
                label: cmd.label.clone(),
                detail: cmd.detail.clone(),
                insert: cmd.insert.clone(),
                selectable: true,
                replace_all: false,
            });
        }
    }
    for skill in skills {
        let name = skill.name.to_ascii_lowercase();
        if name.is_empty() {
            continue;
        }
        if q.is_empty() || name.contains(&q) || format!("/{name}").contains(&q) {
            out.push(Suggestion {
                id: format!("skill-{}", skill.name),
                label: format!("/{}", skill.name),
                detail: if skill.description.is_empty() {
                    "Skill".into()
                } else {
                    skill.description.clone()
                },
                insert: format!("/{}", skill.name),
                selectable: true,
                replace_all: false,
            });
        }
    }
    if out.is_empty() && !q.is_empty() {
        out.push(Suggestion {
            id: "unknown-command".into(),
            label: format!("/{q}"),
            detail: "Unknown command. Enter sends it and the daemon rejects it.".into(),
            insert: String::new(),
            selectable: false,
            replace_all: false,
        });
    }
    out.truncate(40);
    out
}

pub fn skill_suggestions(skills: &[SkillInfo], query: &str) -> Vec<Suggestion> {
    let q = query.trim().trim_start_matches('$').to_ascii_lowercase();
    let mut out = Vec::new();
    for skill in skills {
        let name = skill.name.to_ascii_lowercase();
        if name.is_empty() {
            continue;
        }
        if q.is_empty() || name.contains(&q) {
            out.push(Suggestion {
                id: format!("skill-{}", skill.name),
                label: format!("${}", skill.name),
                detail: if skill.description.is_empty() {
                    "Skill".into()
                } else {
                    skill.description.clone()
                },
                insert: format!("${} ", skill.name),
                selectable: true,
                replace_all: false,
            });
        }
    }
    out.truncate(40);
    out
}

pub fn file_suggestions(paths: &[String]) -> Vec<Suggestion> {
    if paths.is_empty() {
        return vec![Suggestion {
            id: "no-files".into(),
            label: "No files match".into(),
            detail: "The mention stays as text if you send it.".into(),
            insert: String::new(),
            selectable: false,
            replace_all: false,
        }];
    }
    paths
        .iter()
        .take(12)
        .map(|path| Suggestion {
            id: format!("file-{path}"),
            label: path.clone(),
            detail: "Attach this file on send".into(),
            insert: format!("@{path} "),
            selectable: true,
            replace_all: false,
        })
        .collect()
}

pub fn shell_hint(query: &str) -> Vec<Suggestion> {
    let command = query.trim();
    let detail = if command.is_empty() {
        "Type a command. Enter asks for approval, then runs it. No model turn.".into()
    } else {
        "Enter asks for approval, then runs this. No model turn.".into()
    };
    vec![Suggestion {
        id: "shell".into(),
        label: if command.is_empty() {
            "Shell command".into()
        } else {
            command.to_string()
        },
        detail,
        insert: String::new(),
        selectable: false,
        replace_all: false,
    }]
}

/// Drop a daemon file fence the model sees but the transcript should not.
pub fn display_user_text(text: &str) -> String {
    let mut cut = text.len();
    for mark in ["\n\n--- file:", "\n\n--- folder:"] {
        if let Some(i) = text.find(mark) {
            cut = cut.min(i);
        }
    }
    text[..cut].trim_end().to_string()
}

/// One status string until the shared status component replaces this call.
pub fn trigger_status(note: &str, pending_asks: usize) -> String {
    let note = note.trim();
    let asks = match pending_asks {
        0 => String::new(),
        1 => "1 shell approval waiting".into(),
        n => format!("{n} shell approvals waiting"),
    };
    match (note.is_empty(), asks.is_empty()) {
        (true, true) => String::new(),
        (false, true) => note.to_string(),
        (true, false) => asks,
        (false, false) => format!("{note} · {asks}"),
    }
}

pub fn move_index(items: &[Suggestion], index: usize, delta: isize) -> usize {
    if items.is_empty() || !items.iter().any(|item| item.selectable) {
        return index;
    }
    let n = items.len() as isize;
    let mut i = index.min(items.len() - 1) as isize;
    for _ in 0..items.len() {
        i = (i + delta).rem_euclid(n);
        if items[i as usize].selectable {
            return i as usize;
        }
    }
    index
}

#[cfg(test)]
mod tests {
    use super::*;

    fn cmd(id: &str, insert: &str) -> ComposerCommand {
        ComposerCommand {
            id: id.into(),
            label: insert.into(),
            detail: "detail".into(),
            insert: insert.into(),
        }
    }

    fn skill(name: &str) -> SkillInfo {
        SkillInfo {
            name: name.into(),
            description: "desc".into(),
        }
    }

    #[test]
    fn triggers_match_the_daemon_rules() {
        assert!(trigger_at("hello", 5).is_none());
        assert!(trigger_at("user@host", 9).is_none());
        assert!(trigger_at("see ../secret", 14).is_none());
        let file = trigger_at("see @notes", 10).expect("file");
        assert_eq!(file.kind, Kind::File);
        assert_eq!(file.query, "notes");
        assert_eq!(file.start, 4);

        let bang = trigger_at("!echo hi", 8).expect("bang");
        assert_eq!(bang.kind, Kind::Shell);
        assert_eq!(bang.query, "echo hi");
        assert!(trigger_at("say !echo", 9).is_none());

        let slash = trigger_at("  /mode", 7).expect("slash");
        assert_eq!(slash.kind, Kind::Command);
        assert_eq!(slash.query, "mode");

        let dollar = trigger_at("$commit", 7).expect("skill");
        assert_eq!(dollar.kind, Kind::Skill);
        assert_eq!(dollar.query, "commit");
    }

    #[test]
    fn utf16_cursor_and_splice() {
        let text = "see @é";
        let utf16 = text.encode_utf16().count();
        assert_eq!(utf16_to_byte(text, utf16), text.len());
        assert_eq!(
            splice_draft("see @no", 4, 7, "@notes.md "),
            "see @notes.md "
        );
        assert_eq!(splice_draft("draft", 0, 5, "/stop"), "/stop");
    }

    #[test]
    fn catalogs_filter_real_rows_only() {
        let commands = vec![cmd("stop", "/stop"), cmd("mode-plan", "/mode plan")];
        let skills = vec![skill("commit")];
        let slash = slash_suggestions(&commands, &skills, "mode");
        assert_eq!(slash.len(), 1);
        assert_eq!(slash[0].insert, "/mode plan");
        let unknown = slash_suggestions(&commands, &[], "pets");
        assert!(!unknown[0].selectable);
        let found = skill_suggestions(&skills, "com");
        assert_eq!(found[0].insert, "$commit ");
        assert!(skill_suggestions(&skills, "nope").is_empty());
        let files = file_suggestions(&["a.go".into()]);
        assert_eq!(files[0].insert, "@a.go ");
        assert!(!file_suggestions(&[])[0].selectable);
        assert!(!shell_hint("ls")[0].selectable);
    }

    #[test]
    fn display_and_status_shims() {
        let raw = "see @notes\n\n--- file: notes.txt ---\nsecret\n--- end file: notes.txt ---";
        assert_eq!(display_user_text(raw), "see @notes");
        assert_eq!(display_user_text("plain"), "plain");
        assert_eq!(trigger_status("", 0), "");
        assert_eq!(
            trigger_status("composer: skipped", 2),
            "composer: skipped · 2 shell approvals waiting"
        );
        assert_eq!(move_index(&shell_hint("ls"), 0, 1), 0);
        let rows = slash_suggestions(&[cmd("a", "/stop")], &[], "");
        assert_eq!(move_index(&rows, 0, 1), 0);
    }
}
