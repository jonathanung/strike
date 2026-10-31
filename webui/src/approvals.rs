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
    /// Autonomy (#1572): only ever armed for `skip-all`.
    Autonomy { mode: String },
    Global { mode: String },
    Workspace { id: String, mode: String },
    RuleAdd {
        scope: String,
        permission: String,
        pattern: String,
        action: String,
    },
    RuleRemove {
        scope: String,
        index: i32,
        permission: String,
        pattern: String,
        action: String,
    },
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

/// Autonomy (#1572) is a separate dial from permission mode: the four
/// protocol exit-gate values. Unknown ids are ignored, never rendered.
pub fn known_autonomy(mode: &str) -> bool {
    matches!(mode, "supervised" | "agent" | "checks" | "skip-all")
}

/// Only `skip-all` removes a gate entirely, so only it confirms — the
/// other modes still gate every phase exit, just with a different
/// clearer (issue #1572 Q1, recommended default).
pub fn autonomy_needs_confirm(mode: &str) -> bool {
    mode == "skip-all"
}

/// Autonomy changes, mirroring [`begin_session_mode`]: same mode or an
/// unknown id is a no-op, `skip-all` confirms first, everything else
/// applies immediately. Applying only posts the op — the displayed
/// state still waits for the daemon's `autonomy.selected`.
pub fn begin_autonomy_change(current: &str, next: &str) -> ModeChange {
    if !known_autonomy(next) || current == next {
        return ModeChange::Same;
    }
    if autonomy_needs_confirm(next) {
        ModeChange::Confirm(autonomy_confirm(next))
    } else {
        ModeChange::Apply(next.to_string())
    }
}

pub fn autonomy_confirm(mode: &str) -> PendingConfirm {
    PendingConfirm {
        title: "Advance without exit gates?".into(),
        body: "Skip-all advances workflow phases without exit gates: no one approves a phase exit and no check commands run. Tool permissions are unchanged — permission asks and deny rules still apply. Other sessions stay as they are.".into(),
        confirm_label: "Use skip-all".into(),
        kind: ConfirmKind::Autonomy {
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

pub fn rule_remove_confirm(
    scope: &str,
    index: i32,
    permission: &str,
    pattern: &str,
    action: &str,
) -> Option<PendingConfirm> {
    if action != "deny" {
        return None;
    }
    Some(PendingConfirm {
        title: "Remove this deny rule?".into(),
        body: format!(
            "{scope} · {permission} · {pattern} · {action}\n\nRemoving a deny weakens that file. Yolo and Edits still honor every deny that remains, and managed denies are not in this list."
        ),
        confirm_label: "Remove deny".into(),
        kind: ConfirmKind::RuleRemove {
            scope: scope.into(),
            index,
            permission: permission.into(),
            pattern: pattern.into(),
            action: action.into(),
        },
    })
}

/// The four Add-form fields, copied verbatim from an existing rule
/// (#1433). Duplicate/edit is prefill-only: this draft seeds the form and
/// the existing submit path — validation and the dangerous-add confirm
/// included — stays the single way a rule is created. Nothing here
/// submits, and the source rule is untouched until the user removes it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RuleDraft {
    pub scope: String,
    pub permission: String,
    pub pattern: String,
    pub action: String,
}

/// Prefill for the Duplicate affordance: every field copied as-is,
/// including a project scope (changing scope is the user's edit, Q3).
pub fn rule_prefill(rule: &RuleRecord) -> RuleDraft {
    RuleDraft {
        scope: rule.scope.clone(),
        permission: rule.permission.clone(),
        pattern: rule.pattern.clone(),
        action: rule.action.clone(),
    }
}

/// Body for POST /v1/permissions/rules/remove. Identity travels with the
/// index so a shifted list cannot delete a different rule.
pub fn rule_remove_body(rule: &RuleRecord, confirm: bool) -> serde_json::Value {
    serde_json::json!({
        "scope": rule.scope,
        "index": rule.index,
        "permission": rule.permission,
        "pattern": rule.pattern,
        "action": rule.action,
        "confirm": confirm,
    })
}

/// Visible countdown. A positive config value wins. Otherwise soft-approve
/// uses 15 seconds. Excluded permission names never count down.
pub fn countdown_seconds(mode: &str, configured: i32, permission: &str, exclude: &[String]) -> i32 {
    // Negative means the settings load failed or has not confirmed a value.
    // A countdown from that state would invent the daemon's auto-approve.
    if configured < 0 {
        return 0;
    }
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
    /// True when the ask metadata marks the target as a directory (the
    /// delete tool), so the folder scope is the named folder itself
    /// rather than the parent of a file.
    pub target_directory: bool,
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
            "folder" => match self.folder_scope() {
                Some(folder) => format!(
                    "Remembers {} in the folder {} and its subfolders for this session only. Not written to the project config. Remove it any time under Rules and audit.",
                    self.permission, folder
                ),
                None => String::new(),
            },
            "reject" => "Stops this call. Nothing runs and nothing is remembered.".into(),
            _ => String::new(),
        }
    }

    /// The folder a folder grant would cover, as an absolute display path
    /// when the daemon stamped a working directory, else the
    /// workspace-relative folder. Mirrors the daemon's derivation
    /// (permission.DeriveFolderGrant) for display only — the grant itself
    /// is always derived daemon-side from the same patterns. None when
    /// the ask has no folder scope: non-write/edit permissions, glob or
    /// escaping patterns, patterns spanning several folders, and targets
    /// at the workspace root (a bare name is ambiguous between a root
    /// file and the folder itself, so v1 offers no folder scope there).
    pub fn folder_scope(&self) -> Option<String> {
        if self.permission != "write" && self.permission != "edit" {
            return None;
        }
        if self.patterns.is_empty() {
            return None;
        }
        let cleaned: Option<Vec<String>> = self.patterns.iter().map(|p| clean_rel(p)).collect();
        let cleaned = cleaned?;
        let rel = if self.target_directory {
            if cleaned.len() != 1 {
                return None;
            }
            cleaned[0].clone()
        } else {
            let mut folder: Option<String> = None;
            for clean in &cleaned {
                let parent = clean.rsplit_once('/').map(|(head, _)| head.to_string())?;
                match &folder {
                    None => folder = Some(parent),
                    Some(f) if *f != parent => return None,
                    _ => {}
                }
            }
            folder?
        };
        if self.work_dir.is_empty() {
            Some(rel)
        } else {
            Some(format!("{}/{}", self.work_dir.trim_end_matches('/'), rel))
        }
    }
}

/// Cleans a workspace-relative pattern the way the daemon's path.Clean
/// does for the shapes the tools emit; None for absolute, escaping, or
/// glob-shaped patterns (a whole "*" or "**" segment).
fn clean_rel(pat: &str) -> Option<String> {
    if pat.is_empty() || pat.starts_with('/') {
        return None;
    }
    let mut segs: Vec<&str> = Vec::new();
    for seg in pat.split('/') {
        match seg {
            "" | "." => {}
            ".." | "*" | "**" => return None,
            s => segs.push(s),
        }
    }
    if segs.is_empty() {
        return None;
    }
    Some(segs.join("/"))
}

/// The choices for one ask's card: the base decisions, plus "Allow in
/// this folder" (key 4, Deny becomes 5) when the ask has a folder scope.
pub fn decision_choices(ask: &Ask) -> Vec<DecisionChoice> {
    let mut out: Vec<DecisionChoice> = decisions().to_vec();
    if ask.folder_scope().is_some() {
        let mut reject = out.pop().expect("decisions always ends with reject");
        out.push(DecisionChoice {
            id: "folder",
            label: "Allow in this folder",
            key: "4",
            detail: "This folder and its subfolders, this session only.",
        });
        reject.key = "5";
        out.push(reject);
    }
    out
}

/// The card's key hint, e.g. "1 allow once, 2 allow session, 3 allow
/// project, 4 or Escape denies." — the numbering extends honestly when a
/// folder choice is present.
pub fn hint_line(choices: &[DecisionChoice]) -> String {
    let mut parts: Vec<String> = choices[..choices.len().saturating_sub(1)]
        .iter()
        .enumerate()
        .map(|(i, c)| format!("{} {}", i + 1, c.label.to_lowercase()))
        .collect();
    parts.push(format!("{} or Escape denies.", choices.len()));
    parts.join(", ")
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
    let target_directory = data
        .get("metadata")
        .and_then(|m| m.get("directory"))
        .and_then(|v| v.as_bool())
        .unwrap_or(false);
    Some(Ask {
        request_id,
        permission,
        patterns,
        always,
        preview,
        work_dir: text(data, "workDir").unwrap_or_default(),
        project_rule_path: text(data, "projectRulePath").unwrap_or_default(),
        target_directory,
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
        assert!(rule_remove_confirm("global", 1, "bash", "rm *", "allow").is_none());
        let remove = rule_remove_confirm("global", 1, "bash", "rm *", "deny").unwrap();
        assert!(remove.body.contains("global · bash · rm * · deny"), "{}", remove.body);
        match remove.kind {
            ConfirmKind::RuleRemove {
                permission,
                pattern,
                action,
                ..
            } => {
                assert_eq!(permission, "bash");
                assert_eq!(pattern, "rm *");
                assert_eq!(action, "deny");
            }
            other => panic!("{other:?}"),
        }
        let body = rule_remove_body(
            &RuleRecord {
                scope: "global".into(),
                index: 1,
                permission: "bash".into(),
                pattern: "rm *".into(),
                action: "deny".into(),
            },
            true,
        );
        assert_eq!(body["permission"], "bash");
        assert_eq!(body["pattern"], "rm *");
        assert_eq!(body["action"], "deny");
        assert_eq!(body["index"], 1);
        assert_eq!(body["confirm"], true);
    }

    #[test]
    fn autonomy_change_confirms_only_skip_all() {
        assert!(matches!(
            begin_autonomy_change("supervised", "supervised"),
            ModeChange::Same
        ));
        assert!(matches!(
            begin_autonomy_change("supervised", "bogus"),
            ModeChange::Same
        ));
        for mode in ["supervised", "agent", "checks"] {
            assert!(matches!(
                begin_autonomy_change("skip-all", mode),
                ModeChange::Apply(ref m) if m == mode
            ));
        }
        match begin_autonomy_change("supervised", "skip-all") {
            ModeChange::Confirm(c) => {
                assert!(c.title.contains("without exit gates"), "{}", c.title);
                assert!(c.body.contains("Tool permissions are unchanged"));
                assert_eq!(
                    c.kind,
                    ConfirmKind::Autonomy {
                        mode: "skip-all".into()
                    }
                );
            }
            other => panic!("skip-all did not confirm: {other:?}"),
        }
        assert!(known_autonomy("checks"));
        assert!(!known_autonomy("yolo"));
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
        // A failed settings load uses a negative sentinel. Soft-approve must
        // not fall through to the 15s default.
        assert_eq!(countdown_seconds("soft-approve", -1, "bash", &[]), 0);
        assert_eq!(countdown_seconds("default", -1, "bash", &[]), 0);
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
            target_directory: false,
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
            target_directory: false,
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
            target_directory: false,
        };
        let line = ask.consequence("project");
        assert!(line.contains("edit *"), "{line}");
        assert!(line.contains("does not save project rules to a file"), "{line}");
    }

    fn ask_with(permission: &str, patterns: &[&str]) -> Ask {
        Ask {
            request_id: "p1".into(),
            permission: permission.into(),
            patterns: patterns.iter().map(|s| s.to_string()).collect(),
            always: vec!["*".into()],
            preview: Preview {
                heading: String::new(),
                command: String::new(),
                removed: Vec::new(),
                added: Vec::new(),
                truncated: false,
            },
            work_dir: "/work/project".into(),
            project_rule_path: "/work/project/.strike/config".into(),
            target_directory: false,
        }
    }

    #[test]
    fn folder_scope_and_choice_for_write_and_edit_asks() {
        let ask = ask_with("write", &["docs/guide.md"]);
        assert_eq!(ask.folder_scope().as_deref(), Some("/work/project/docs"));
        let choices = decision_choices(&ask);
        let ids: Vec<&str> = choices.iter().map(|c| c.id).collect();
        assert_eq!(ids, ["once", "always", "project", "folder", "reject"]);
        let keys: Vec<&str> = choices.iter().map(|c| c.key).collect();
        assert_eq!(keys, ["1", "2", "3", "4", "5"]);
        assert_eq!(
            hint_line(&choices),
            "1 allow once, 2 allow session, 3 allow project, 4 allow in this folder, 5 or Escape denies."
        );
        let line = ask.consequence("folder");
        assert!(line.contains("/work/project/docs"), "{line}");
        assert!(line.contains("and its subfolders"), "{line}");
        assert!(line.contains("this session only"), "{line}");
        assert!(line.contains("Not written to the project config"), "{line}");
        assert!(line.contains("Rules and audit"), "{line}");

        // Directory targets grant the named folder itself.
        let mut del = ask_with("edit", &["docs/old"]);
        del.target_directory = true;
        assert_eq!(del.folder_scope().as_deref(), Some("/work/project/docs/old"));

        // Without a stamped working directory the folder stays relative.
        let mut bare = ask_with("edit", &["src/api/x.go"]);
        bare.work_dir = String::new();
        assert_eq!(bare.folder_scope().as_deref(), Some("src/api"));
    }

    #[test]
    fn folder_choice_absent_without_a_folder_scope() {
        for ask in [
            ask_with("bash", &["ls docs"]),
            ask_with("write", &["main.go"]),            // workspace root: no v1 folder scope
            ask_with("edit", &["a/x.md", "b/y.md"]),    // spans two folders
            ask_with("write", &["docs/**"]),            // glob, not a file path
            ask_with("write", &["../outside.md"]),     // escapes the workspace
        ] {
            assert_eq!(ask.folder_scope(), None, "{ask:?}");
            let choices = decision_choices(&ask);
            assert_eq!(choices.len(), 4, "{ask:?}");
            assert_eq!(choices[3].id, "reject");
            assert_eq!(choices[3].key, "4");
        }
        assert_eq!(
            hint_line(&decision_choices(&ask_with("bash", &["ls"]))),
            "1 allow once, 2 allow session, 3 allow project, 4 or Escape denies."
        );
    }

    #[test]
    fn rule_prefill_copies_all_four_fields_verbatim() {
        // Distinct values per field so a swapped copy fails loudly.
        let rule = RuleRecord {
            scope: "project".into(),
            index: 3,
            permission: "edit".into(),
            pattern: "src/**/*.go".into(),
            action: "ask".into(),
        };
        let draft = rule_prefill(&rule);
        assert_eq!(draft.scope, "project");
        assert_eq!(draft.permission, "edit");
        assert_eq!(draft.pattern, "src/**/*.go");
        assert_eq!(draft.action, "ask");
        // A prefilled draft is an ordinary draft: it passes the same
        // validation a hand-typed rule goes through, with no shortcut.
        assert!(validate_rule_draft(&draft.permission, &draft.pattern, &draft.action).is_ok());
        // Prefill never mutates the source record.
        assert_eq!(rule.index, 3);
        assert_eq!(rule.pattern, "src/**/*.go");

        let global = RuleRecord {
            scope: "global".into(),
            index: 0,
            permission: "bash".into(),
            pattern: "rm *".into(),
            action: "deny".into(),
        };
        let draft = rule_prefill(&global);
        assert_eq!(draft.scope, "global");
        assert_eq!(draft.permission, "bash");
        assert_eq!(draft.pattern, "rm *");
        assert_eq!(draft.action, "deny");
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
