//! Permission posture for the web client.
//!
//! Modes are Strike's five wire values. The UI never invents a Reads or Trust
//! mode, and it never posts a mode or a rule that the daemon did not confirm.

use serde::Deserialize;
use serde_json::Value;

pub const SOFT_APPROVE_SECONDS: i32 = 15;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DecisionChoice {
    pub id: &'static str,
    pub label: &'static str,
    pub key: &'static str,
    pub detail: &'static str,
}

pub fn decisions() -> &'static [DecisionChoice] {
    &[
        DecisionChoice {
            id: "once",
            label: "Allow once",
            key: "1",
            detail: "This call only.",
        },
        DecisionChoice {
            id: "always",
            label: "Allow session",
            key: "2",
            detail: "Remembered for this session only.",
        },
        DecisionChoice {
            id: "project",
            label: "Allow project",
            key: "3",
            detail: "Remembered for future sessions in this workspace.",
        },
        DecisionChoice {
            id: "reject",
            label: "Deny",
            key: "4",
            detail: "Stops this call. Escape does the same.",
        },
    ]
}

pub fn is_dangerous(mode: &str) -> bool {
    matches!(mode, "soft-approve" | "accept-edits" | "yolo")
}

pub fn known_mode(mode: &str) -> bool {
    matches!(
        mode,
        "default" | "plan" | "soft-approve" | "accept-edits" | "yolo"
    )
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ConfirmKind {
    Session { mode: String },
    Global { mode: String },
    Workspace { id: String, mode: String },
    RuleAdd {
        scope: String,
        permission: String,
        pattern: String,
        action: String,
    },
    RuleRemove { scope: String, index: i32 },
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PendingConfirm {
    pub title: String,
    pub body: String,
    pub confirm_label: String,
    pub kind: ConfirmKind,
}

#[derive(Debug)]
pub enum ModeChange {
    Same,
    Apply(String),
    Confirm(PendingConfirm),
}

/// Session mode changes. Dangerous targets always confirm. Tightening to
/// Default or Plan applies immediately. Unknown ids are ignored.
pub fn begin_session_mode(current: &str, next: &str) -> ModeChange {
    if !known_mode(next) || current == next {
        return ModeChange::Same;
    }
    if is_dangerous(next) {
        ModeChange::Confirm(session_confirm(next))
    } else {
        ModeChange::Apply(next.to_string())
    }
}

pub fn session_confirm(mode: &str) -> PendingConfirm {
    let (title, body, label) = match mode {
        "yolo" => (
            "Use Yolo for this session?",
            "This session skips asks that are not hard-denied. Deny rules still apply, including managed denies. Other sessions stay as they are.",
            "Use Yolo",
        ),
        "accept-edits" => (
            "Allow edits without asking?",
            "File edits and writes are allowed in this session. Shell and network still ask. Deny rules still apply.",
            "Allow edits",
        ),
        _ => (
            "Arm the soft-approve countdown?",
            "Asks stay on screen, then allow once when the countdown finishes if you do nothing. Deny rules still apply. You can deny before it finishes.",
            "Arm countdown",
        ),
    };
    PendingConfirm {
        title: title.into(),
        body: body.into(),
        confirm_label: label.into(),
        kind: ConfirmKind::Session {
            mode: mode.into(),
        },
    }
}

pub fn global_confirm(mode: &str) -> Option<PendingConfirm> {
    if !known_mode(mode) || !is_dangerous(mode) {
        return None;
    }
    let mut confirm = session_confirm(mode);
    confirm.title = format!("Save {} as the global default?", crate::model::mode_label(mode));
    confirm.body = format!(
        "{}\n\nNew sessions start here unless a project config sets its own mode. This session does not change.",
        confirm.body
    );
    confirm.confirm_label = "Save global default".into();
    confirm.kind = ConfirmKind::Global {
        mode: mode.into(),
    };
    Some(confirm)
}

pub fn workspace_confirm(id: &str, mode: &str) -> Option<PendingConfirm> {
    if id.is_empty() || !known_mode(mode) || !is_dangerous(mode) {
        return None;
    }
    let mut confirm = session_confirm(mode);
    confirm.title = format!(
        "Store {} on this workspace?",
        crate::model::mode_label(mode)
    );
    confirm.body = format!(
        "{}\n\nThis only stores the mode; it does not change the live session now. Opening the workspace applies its stored mode to the live session.",
        confirm.body
    );
    confirm.confirm_label = "Store on workspace".into();
    confirm.kind = ConfirmKind::Workspace {
        id: id.into(),
        mode: mode.into(),
    };
    Some(confirm)
}

pub fn rule_add_confirm(scope: &str, permission: &str, pattern: &str, action: &str) -> Option<PendingConfirm> {
    if !matches!(action, "allow" | "ask") {
        return None;
    }
    Some(PendingConfirm {
        title: format!("Add {action} rule?"),
        body: format!(
            "{scope} · {permission} · {pattern}\n\nInside one file the last matching rule wins, so this can override an earlier deny for the same call. Managed denies still win. Yolo does not turn a deny into an allow."
        ),
        confirm_label: "Add rule".into(),
        kind: ConfirmKind::RuleAdd {
            scope: scope.into(),
            permission: permission.into(),
            pattern: pattern.into(),
            action: action.into(),
        },
    })
}

pub fn rule_remove_confirm(scope: &str, index: i32, action: &str) -> Option<PendingConfirm> {
    if action != "deny" {
        return None;
    }
    Some(PendingConfirm {
        title: "Remove this deny rule?".into(),
        body: "Removing a deny weakens that file. Yolo and Edits still honor every deny that remains, and managed denies are not in this list.".into(),
        confirm_label: "Remove deny".into(),
        kind: ConfirmKind::RuleRemove {
            scope: scope.into(),
            index,
        },
    })
}

/// Visible countdown. A positive config value wins. Otherwise soft-approve
/// uses 15 seconds. Excluded permission names never count down.
pub fn countdown_seconds(mode: &str, configured: i32, permission: &str, exclude: &[String]) -> i32 {
    if exclude
        .iter()
        .any(|name| name.eq_ignore_ascii_case(permission))
    {
        return 0;
    }
    if configured > 0 {
        return configured.clamp(1, 60);
    }
    if mode == "soft-approve" {
        SOFT_APPROVE_SECONDS
    } else {
        0
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Preview {
    pub heading: String,
    pub command: String,
    pub removed: Vec<String>,
    pub added: Vec<String>,
    pub truncated: bool,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Ask {
    pub request_id: String,
    pub permission: String,
    pub patterns: Vec<String>,
    pub always: Vec<String>,
    pub preview: Preview,
    /// Working directory the tool call runs in, stamped by the daemon on
    /// the ask payload (#1507). Empty when the daemon did not send it —
    /// the card then hides the line instead of guessing.
    pub work_dir: String,
    /// File a project grant appends its rule to, stamped by the daemon
    /// (#1507). Empty when project grants are not persisted to disk.
    pub project_rule_path: String,
}

impl Ask {
    /// The pattern set a session or project grant will remember. Matches
    /// the permission service: Always when non-empty, otherwise Patterns.
    pub fn remembered_patterns(&self) -> Vec<String> {
        if self.always.is_empty() {
            self.patterns.clone()
        } else {
            self.always.clone()
        }
    }

    /// The remembered rules in the shape they are stored: one
    /// "permission pattern" entry per remembered pattern.
    pub fn remembered_rules(&self) -> String {
        self.remembered_patterns()
            .iter()
            .map(|pattern| format!("{} {}", self.permission, pattern))
            .collect::<Vec<_>>()
            .join(", ")
    }

    /// One-line plain-language consequence for a decision id, built only
    /// from facts the daemon stamped on this ask (#1507). Shown on the
    /// card next to the scope buttons, before the person chooses.
    pub fn consequence(&self, decision: &str) -> String {
        let rules = self.remembered_rules();
        match decision {
            "once" => "Runs this one call now. Nothing is remembered.".into(),
            "always" => format!(
                "Remembers {rules} for this session only. Not written to the project config."
            ),
            "project" => {
                if self.project_rule_path.is_empty() {
                    format!(
                        "Remembers {rules} for future sessions in this workspace. This gateway does not save project rules to a file."
                    )
                } else {
                    format!(
                        "Remembers {rules} for future sessions in this workspace. Stored in {} — remove it any time under Rules and audit.",
                        self.project_rule_path
                    )
                }
            }
            "reject" => "Stops this call. Nothing runs and nothing is remembered.".into(),
            _ => String::new(),
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AuditLine {
    pub time: String,
    pub session_id: String,
    pub permission: String,
    pub action: String,
    pub decision: String,
    pub detail: String,
}

#[derive(Debug)]
pub enum PermNote {
    Ask(Ask),
    Clear(String),
    Mode(String),
    Decided(AuditLine),
}

pub fn notes_from_envelope(raw: &str) -> Vec<PermNote> {
    let v: Value = match serde_json::from_str(raw) {
        Ok(v) => v,
        Err(_) => return Vec::new(),
    };
    let typ = v.get("type").and_then(|t| t.as_str()).unwrap_or("");
    let data = v.get("data").cloned().unwrap_or(Value::Null);
    match typ {
        "permission.asked" => parse_ask(&data).into_iter().map(PermNote::Ask).collect(),
        "permission.resolved" => text(&data, "requestId")
            .filter(|id| !id.is_empty())
            .map(PermNote::Clear)
            .into_iter()
            .collect(),
        "permission.mode" => text(&data, "mode")
            .filter(|mode| known_mode(mode))
            .map(PermNote::Mode)
            .into_iter()
            .collect(),
        "permission.decided" => {
            // action "ask" is the audit of a still-open prompt. Clearing it
            // here would drop the card before the person answers.
            let row = audit_line(&data);
            let resolved = !row.decision.is_empty() || row.action == "allow" || row.action == "deny";
            let mut notes = Vec::new();
            if resolved {
                if let Some(id) = text(&data, "requestId").filter(|id| !id.is_empty()) {
                    notes.push(PermNote::Clear(id));
                }
            }
            notes.push(PermNote::Decided(row));
            notes
        }
        _ => Vec::new(),
    }
}

fn parse_ask(data: &Value) -> Option<Ask> {
    let request_id = text(data, "requestId")?;
    if request_id.is_empty() {
        return None;
    }
    let permission = text(data, "permission").unwrap_or_else(|| "tool".into());
    let patterns = string_list(data, "patterns");
    let always = string_list(data, "always");
    let preview = preview_of(&permission, &patterns, data.get("metadata").unwrap_or(&Value::Null));
    Some(Ask {
        request_id,
        permission,
        patterns,
        always,
        preview,
        work_dir: text(data, "workDir").unwrap_or_default(),
        project_rule_path: text(data, "projectRulePath").unwrap_or_default(),
    })
}

pub fn preview_of(permission: &str, patterns: &[String], metadata: &Value) -> Preview {
    if let Some((old, new)) = diff_strings(metadata) {
        let (removed, added, truncated) = diff_lines(&old, &new, 8);
        return Preview {
            heading: patterns.first().cloned().unwrap_or_else(|| permission.into()),
            command: String::new(),
            removed,
            added,
            truncated,
        };
    }
    let body = patterns.join("\n");
    if permission == "bash" || permission == "git" {
        Preview {
            heading: permission.into(),
            command: body,
            removed: Vec::new(),
            added: Vec::new(),
            truncated: false,
        }
    } else {
        Preview {
            heading: if body.is_empty() {
                permission.into()
            } else {
                body
            },
            command: String::new(),
            removed: Vec::new(),
            added: Vec::new(),
            truncated: false,
        }
    }
}

fn diff_strings(metadata: &Value) -> Option<(String, String)> {
    let old = metadata.get("oldString")?.as_str()?;
    let new = metadata.get("newString")?.as_str()?;
    Some((old.to_string(), new.to_string()))
}

fn diff_lines(old: &str, new: &str, cap: usize) -> (Vec<String>, Vec<String>, bool) {
    let mut removed: Vec<String> = old.split('\n').map(|l| l.to_string()).collect();
    let mut added: Vec<String> = new.split('\n').map(|l| l.to_string()).collect();
    let truncated = removed.len() > cap || added.len() > cap;
    if removed.len() > cap {
        removed.truncate(cap);
    }
    if added.len() > cap {
        added.truncate(cap);
    }
    (removed, added, truncated)
}

fn audit_line(data: &Value) -> AuditLine {
    let patterns = string_list(data, "patterns");
    AuditLine {
        time: String::new(),
        session_id: text(data, "sessionId").unwrap_or_default(),
        permission: text(data, "permission").unwrap_or_default(),
        action: text(data, "action").unwrap_or_default(),
        decision: text(data, "decision").unwrap_or_default(),
        detail: patterns.join(" "),
    }
}

fn text(data: &Value, key: &str) -> Option<String> {
    match data.get(key) {
        Some(Value::String(s)) => Some(s.clone()),
        Some(other) if !other.is_null() => Some(other.to_string()),
        _ => None,
    }
}

fn string_list(data: &Value, key: &str) -> Vec<String> {
    data.get(key)
        .and_then(|v| v.as_array())
        .map(|items| {
            items
                .iter()
                .filter_map(|v| v.as_str().map(|s| s.to_string()))
                .collect()
        })
        .unwrap_or_default()
}

#[derive(Clone, Debug, Deserialize)]
pub struct RuleRecord {
    pub scope: String,
    pub index: i32,
    pub permission: String,
    pub pattern: String,
    pub action: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct RulesDocument {
    #[serde(default, rename = "workDir")]
    pub work_dir: String,
    #[serde(default, rename = "globalMode")]
    pub global_mode: String,
    #[serde(default, rename = "projectMode")]
    pub project_mode: String,
    #[serde(default, rename = "effectiveMode")]
    pub effective_mode: String,
    #[serde(default)]
    pub rules: Vec<RuleRecord>,
    #[serde(default, rename = "knownPermissions")]
    pub known: Vec<String>,
    #[serde(default)]
    pub note: String,
    #[serde(default, rename = "workspaceNote")]
    pub workspace_note: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct AuditDocument {
    #[serde(default)]
    pub available: bool,
    #[serde(default)]
    pub records: Vec<StoredAudit>,
    #[serde(default)]
    pub note: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct StoredAudit {
    #[serde(default)]
    pub time: String,
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default)]
    pub permission: String,
    #[serde(default)]
    pub action: String,
    #[serde(default)]
    pub decision: String,
    #[serde(default)]
    pub patterns: Vec<String>,
    #[serde(default)]
    pub layer: String,
    #[serde(default, rename = "ruleAction")]
    pub rule_action: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct SettingsBits {
    #[serde(default, rename = "permissionAutoApproveSeconds")]
    pub auto_seconds: i32,
    #[serde(default, rename = "permissionAutoApproveExclude")]
    pub auto_exclude: Vec<String>,
}

pub fn validate_rule_draft(permission: &str, pattern: &str, action: &str) -> Result<(), &'static str> {
    if permission.trim().is_empty() {
        return Err("Pick a permission.");
    }
    if pattern.trim().is_empty() {
        return Err("Pattern is required. Use * only if you mean every call.");
    }
    if pattern.contains('\n') || pattern.contains('\r') {
        return Err("Pattern must be a single line.");
    }
    if !matches!(action, "allow" | "ask" | "deny") {
        return Err("Action must be allow, ask, or deny.");
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dangerous_modes_confirm_and_tightening_does_not() {
        assert!(matches!(begin_session_mode("default", "default"), ModeChange::Same));
        assert!(matches!(begin_session_mode("yolo", "trust"), ModeChange::Same));
        assert!(matches!(
            begin_session_mode("yolo", "plan"),
            ModeChange::Apply(ref mode) if mode == "plan"
        ));
        assert!(matches!(
            begin_session_mode("accept-edits", "default"),
            ModeChange::Apply(ref mode) if mode == "default"
        ));
        for mode in ["soft-approve", "accept-edits", "yolo"] {
            match begin_session_mode("default", mode) {
                ModeChange::Confirm(c) => {
                    assert!(c.body.contains("Deny rules still apply"), "{}", c.body);
                    match &c.kind {
                        ConfirmKind::Session { mode: got } => assert_eq!(got, mode),
                        other => panic!("{other:?}"),
                    }
                }
                other => panic!("{mode} did not confirm: {other:?}"),
            }
        }
        let yolo = session_confirm("yolo");
        assert!(yolo.body.contains("this session") || yolo.body.contains("This session"));
        assert!(yolo.body.contains("Other sessions"));
        assert!(global_confirm("plan").is_none());
        assert!(global_confirm("yolo").unwrap().body.contains("New sessions"));
        assert!(workspace_confirm("", "yolo").is_none());
        assert!(rule_add_confirm("global", "bash", "rm *", "deny").is_none());
        let add = rule_add_confirm("global", "bash", "*", "allow").unwrap();
        assert!(add.body.contains("last matching rule"));
        assert!(rule_remove_confirm("global", 1, "allow").is_none());
        assert!(rule_remove_confirm("global", 1, "deny").is_some());
    }

    #[test]
    fn countdown_matches_the_tui_dial() {
        assert_eq!(countdown_seconds("default", 0, "bash", &[]), 0);
        assert_eq!(countdown_seconds("soft-approve", 0, "bash", &[]), 15);
        assert_eq!(countdown_seconds("default", 7, "edit", &[]), 7);
        assert_eq!(countdown_seconds("soft-approve", 90, "edit", &[]), 60);
        assert_eq!(
            countdown_seconds("soft-approve", 15, "bash", &["Bash".into()]),
            0
        );
    }

    #[test]
    fn ask_card_previews_command_and_diff() {
        let raw = r#"{"type":"permission.asked","data":{"requestId":"p1","permission":"bash","patterns":["echo hi"],"always":["echo *"]}}"#;
        let notes = notes_from_envelope(raw);
        let PermNote::Ask(ask) = &notes[0] else {
            panic!("notes = {notes:?}");
        };
        assert_eq!(ask.preview.command, "echo hi");
        assert_eq!(ask.always, vec!["echo *".to_string()]);

        let diff = r#"{"type":"permission.asked","data":{"requestId":"p2","permission":"edit","patterns":["a.go"],"metadata":{"oldString":"a\nb\nc\nd\ne\nf\ng\nh\ni","newString":"z"}}}"#;
        let notes = notes_from_envelope(diff);
        let PermNote::Ask(ask) = &notes[0] else {
            panic!("diff notes");
        };
        assert_eq!(ask.preview.heading, "a.go");
        assert!(ask.preview.truncated);
        assert_eq!(ask.preview.removed.len(), 8);
        assert_eq!(ask.preview.added, vec!["z".to_string()]);

        let pending = r#"{"type":"permission.decided","data":{"requestId":"p1","permission":"bash","action":"ask","patterns":["echo hi"]}}"#;
        let notes = notes_from_envelope(pending);
        assert!(matches!(notes.as_slice(), [PermNote::Decided(row)] if row.action == "ask"));

        let decided = r#"{"type":"permission.decided","data":{"requestId":"p1","permission":"bash","action":"deny","decision":"reject","patterns":["echo hi"]}}"#;
        let notes = notes_from_envelope(decided);
        assert!(matches!(&notes[0], PermNote::Clear(id) if id == "p1"));
        assert!(matches!(&notes[1], PermNote::Decided(row) if row.action == "deny" && row.decision == "reject"));

        let mode = r#"{"type":"permission.mode","data":{"mode":"yolo"}}"#;
        assert!(matches!(notes_from_envelope(mode).as_slice(), [PermNote::Mode(m)] if m == "yolo"));
        assert!(notes_from_envelope(r#"{"type":"permission.mode","data":{"mode":"trust"}}"#).is_empty());
        assert!(notes_from_envelope("not-json").is_empty());
    }

    #[test]
    fn ask_facts_parse_from_the_payload() {
        let raw = r#"{"type":"permission.asked","data":{"requestId":"p9","permission":"bash","patterns":["ls -la"],"always":["ls *"],"workDir":"/work/project","projectRulePath":"/work/project/.strike/config"}}"#;
        let notes = notes_from_envelope(raw);
        let PermNote::Ask(ask) = &notes[0] else {
            panic!("notes = {notes:?}");
        };
        assert_eq!(ask.work_dir, "/work/project");
        assert_eq!(ask.project_rule_path, "/work/project/.strike/config");

        // Older daemons send neither field: both stay empty and the card
        // hides the lines rather than guessing.
        let bare = r#"{"type":"permission.asked","data":{"requestId":"p10","permission":"bash","patterns":["ls"]}}"#;
        let notes = notes_from_envelope(bare);
        let PermNote::Ask(ask) = &notes[0] else {
            panic!("notes = {notes:?}");
        };
        assert_eq!(ask.work_dir, "");
        assert_eq!(ask.project_rule_path, "");
    }

    #[test]
    fn remembered_patterns_prefer_always_then_patterns() {
        let mut ask = Ask {
            request_id: "p1".into(),
            permission: "bash".into(),
            patterns: vec!["ls -la".into()],
            always: vec!["ls *".into()],
            preview: Preview {
                heading: String::new(),
                command: String::new(),
                removed: Vec::new(),
                added: Vec::new(),
                truncated: false,
            },
            work_dir: "/work/project".into(),
            project_rule_path: "/work/project/.strike/config".into(),
        };
        assert_eq!(ask.remembered_patterns(), vec!["ls *".to_string()]);
        assert_eq!(ask.remembered_rules(), "bash ls *");
        ask.always.clear();
        assert_eq!(ask.remembered_patterns(), vec!["ls -la".to_string()]);
        assert_eq!(ask.remembered_rules(), "bash ls -la");
    }

    #[test]
    fn consequence_lines_state_scope_pattern_and_store() {
        let ask = Ask {
            request_id: "p1".into(),
            permission: "bash".into(),
            patterns: vec!["ls -la".into()],
            always: vec!["ls *".into()],
            preview: Preview {
                heading: String::new(),
                command: String::new(),
                removed: Vec::new(),
                added: Vec::new(),
                truncated: false,
            },
            work_dir: "/work/project".into(),
            project_rule_path: "/work/project/.strike/config".into(),
        };
        assert_eq!(
            ask.consequence("once"),
            "Runs this one call now. Nothing is remembered."
        );
        assert_eq!(
            ask.consequence("always"),
            "Remembers bash ls * for this session only. Not written to the project config."
        );
        assert_eq!(
            ask.consequence("project"),
            "Remembers bash ls * for future sessions in this workspace. Stored in /work/project/.strike/config — remove it any time under Rules and audit."
        );
        assert_eq!(
            ask.consequence("reject"),
            "Stops this call. Nothing runs and nothing is remembered."
        );
        assert_eq!(ask.consequence("bogus"), "");
    }

    #[test]
    fn project_consequence_without_a_store_path_says_so() {
        let ask = Ask {
            request_id: "p1".into(),
            permission: "edit".into(),
            patterns: vec!["a.go".into()],
            always: vec!["*".into()],
            preview: Preview {
                heading: String::new(),
                command: String::new(),
                removed: Vec::new(),
                added: Vec::new(),
                truncated: false,
            },
            work_dir: "/work/project".into(),
            project_rule_path: String::new(),
        };
        let line = ask.consequence("project");
        assert!(line.contains("edit *"), "{line}");
        assert!(line.contains("does not save project rules to a file"), "{line}");
    }

    #[test]
    fn rule_draft_rejects_a_blank_pattern() {
        assert!(validate_rule_draft("bash", "rm *", "deny").is_ok());
        assert!(validate_rule_draft("bash", "", "deny").is_err());
        assert!(validate_rule_draft("bash", "rm\n-rf", "deny").is_err());
        assert!(validate_rule_draft("bash", "*", "yolo").is_err());
        assert_eq!(decisions().len(), 4);
        assert_eq!(decisions()[1].id, "always");
        assert_eq!(decisions()[3].id, "reject");
    }
}
