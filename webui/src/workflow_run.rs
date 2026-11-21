//! Workflow run controls (#1574): show the session's live workflow and
//! phase, start a catalog workflow, stop the active one.
//!
//! State discipline: the displayed workflow/phase comes only from what
//! the daemon reports — the bootstrap `status.workflow` / `status.phase`
//! fields and `phase.changed` events. Start and Stop post the
//! browser-certified `workflow.start` / `workflow.stop` ops and never
//! touch the displayed state themselves; a rejection arrives as an
//! `engine.error` and is shown verbatim through the composer note.
//!
//! Semantics mirrored from the engine (`harness/engine/phase.go`):
//! exactly one workflow is active per root session; starting validates
//! the target and then replaces any active one; stopping clears the
//! active phase, its phase context, its phase permission profile and
//! any pending phase-grant approval. Both are rejected while a turn runs.

use std::cell::RefCell;

use serde::Deserialize;
use serde_json::Value;

use crate::confirm::ConfirmRequest;
use crate::shell::Choice;

/// The session's live workflow as last reported by the daemon. An empty
/// `workflow` means none is active.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct WorkflowRun {
    pub workflow: String,
    pub phase: String,
    /// Fail-closed resume recovery (`missing` | `mismatch`), empty while
    /// the phase is healthy and enforced.
    pub status: String,
}

impl WorkflowRun {
    pub fn from_status(workflow: &str, phase: &str) -> Self {
        Self {
            workflow: workflow.trim().to_string(),
            phase: phase.trim().to_string(),
            status: String::new(),
        }
    }

    pub fn is_active(&self) -> bool {
        !self.workflow.is_empty()
    }

    /// Chip text, e.g. `Workflow · plan-implement · plan`.
    pub fn label(&self) -> String {
        let mut out = format!("Workflow · {}", self.workflow);
        if !self.phase.is_empty() {
            out.push_str(" · ");
            out.push_str(&self.phase);
        }
        match self.status.as_str() {
            "missing" => out.push_str(" (recovery: not in catalog)"),
            "mismatch" => out.push_str(" (recovery: definition changed)"),
            "" => {}
            other => {
                out.push_str(" (recovery: ");
                out.push_str(other);
                out.push(')');
            }
        }
        out
    }

    /// `"name" (phase "p")` or `"name"` when the phase is unknown.
    fn named(&self) -> String {
        if self.phase.is_empty() {
            format!("\"{}\"", self.workflow)
        } else {
            format!("\"{}\" (phase \"{}\")", self.workflow, self.phase)
        }
    }
}

/// Apply one SSE/JSONL envelope. Only `phase.changed` moves the state;
/// an empty `phase.changed` (no workflow, no phase) is the daemon's
/// "no workflow active".
pub fn reduce(state: WorkflowRun, raw: &str) -> WorkflowRun {
    let Ok(v) = serde_json::from_str::<Value>(raw) else {
        return state;
    };
    if v.get("type").and_then(|t| t.as_str()) != Some("phase.changed") {
        return state;
    }
    let data = v.get("data").cloned().unwrap_or(Value::Null);
    let field = |k: &str| {
        data.get(k)
            .and_then(|s| s.as_str())
            .unwrap_or("")
            .trim()
            .to_string()
    };
    let workflow = field("workflow");
    if workflow.is_empty() {
        return WorkflowRun::default();
    }
    WorkflowRun {
        workflow,
        phase: field("phase"),
        status: field("status"),
    }
}

/// Confirm shown before a start that would replace the active workflow.
/// `None` when nothing is active (the start posts directly).
pub fn replace_confirm(active: &WorkflowRun, target: &str) -> Option<ConfirmRequest> {
    if !active.is_active() {
        return None;
    }
    let target = target.trim();
    Some(ConfirmRequest {
        title: format!("Replace \"{}\" with \"{target}\"?", active.workflow),
        body: format!(
            "Only one workflow runs per session. Starting \"{target}\" stops {} and starts \"{target}\" at its first phase. The daemon checks \"{target}\" first: if it is rejected, \"{}\" stays active.",
            active.named(),
            active.workflow
        ),
        confirm_label: format!("Replace with {target}"),
    })
}

/// Confirm shown before every stop (#1574 Q3: always, phase named).
pub fn stop_confirm(active: &WorkflowRun) -> ConfirmRequest {
    ConfirmRequest {
        title: format!("Stop workflow \"{}\"?", active.workflow),
        body: format!(
            "Stops {}. The phase's context, its phase permission rules and any pending phase approval are dropped; the session continues with no workflow. The daemon refuses this while a turn is running.",
            active.named()
        ),
        confirm_label: "Stop workflow".into(),
    }
}

/// Normalize a palette argument into a workflow name to start.
pub fn start_target(args: &str) -> Result<String, String> {
    let name = args.trim();
    if name.is_empty() {
        return Err("Pick a workflow.".into());
    }
    Ok(name.to_string())
}

#[derive(Clone, Debug, Deserialize)]
pub struct CatalogRow {
    pub name: String,
    #[serde(default)]
    pub description: String,
    #[serde(default)]
    pub valid: bool,
    #[serde(default, rename = "validationError")]
    pub validation_error: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct CatalogList {
    #[serde(default)]
    pub workflows: Vec<CatalogRow>,
}

/// Palette rows for `GET /v1/workflows`. Invalid workflows stay listed
/// with their reason: the daemon is the one that refuses them.
pub fn catalog_choices(rows: &[CatalogRow]) -> Vec<Choice> {
    rows.iter()
        .filter(|row| !row.name.trim().is_empty())
        .map(|row| Choice {
            id: row.name.clone(),
            label: format!("Start workflow: {}", row.name),
            detail: if row.valid {
                row.description.clone()
            } else if row.validation_error.is_empty() {
                "Invalid — the daemon will refuse it".into()
            } else {
                format!("Invalid: {}", row.validation_error)
            },
        })
        .collect()
}

thread_local! {
    static CATALOG: RefCell<Vec<Choice>> = const { RefCell::new(Vec::new()) };
    static ACTIVE: RefCell<bool> = const { RefCell::new(false) };
}

/// Mirror of "a workflow is active on the open session" for the palette's
/// Stop row visibility. Written only from daemon-reported state.
pub fn set_active(active: bool) {
    ACTIVE.with(|a| *a.borrow_mut() = active);
}

pub fn active() -> bool {
    ACTIVE.with(|a| *a.borrow())
}

pub fn set_catalog(rows: Vec<Choice>) {
    CATALOG.with(|c| *c.borrow_mut() = rows);
}

/// Palette choices for "Start workflow…" (last loaded catalog).
pub fn choices() -> Vec<Choice> {
    CATALOG.with(|c| c.borrow().clone())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn env(typ: &str, data: &str) -> String {
        format!(r#"{{"type":"{typ}","data":{data}}}"#)
    }

    #[test]
    fn reduce_follows_phase_changed_only() {
        let cases = [
            (
                "start",
                WorkflowRun::default(),
                env("phase.changed", r#"{"workflow":"plan-implement","phase":"plan","index":0}"#),
                WorkflowRun::from_status("plan-implement", "plan"),
            ),
            (
                "advance",
                WorkflowRun::from_status("plan-implement", "plan"),
                env("phase.changed", r#"{"workflow":"plan-implement","phase":"implement","index":1}"#),
                WorkflowRun::from_status("plan-implement", "implement"),
            ),
            (
                "stop is an empty phase.changed",
                WorkflowRun::from_status("plan-implement", "plan"),
                env("phase.changed", "{}"),
                WorkflowRun::default(),
            ),
            (
                "recovery status kept",
                WorkflowRun::default(),
                env("phase.changed", r#"{"workflow":"gone","status":"missing"}"#),
                WorkflowRun {
                    workflow: "gone".into(),
                    phase: String::new(),
                    status: "missing".into(),
                },
            ),
            (
                "other events ignored",
                WorkflowRun::from_status("a", "b"),
                env("engine.error", r#"{"message":"unknown workflow \"x\""}"#),
                WorkflowRun::from_status("a", "b"),
            ),
            (
                "garbage ignored",
                WorkflowRun::from_status("a", "b"),
                "not json".to_string(),
                WorkflowRun::from_status("a", "b"),
            ),
        ];
        for (name, prev, raw, want) in cases {
            assert_eq!(reduce(prev, &raw), want, "{name}");
        }
    }

    #[test]
    fn label_names_workflow_phase_and_recovery() {
        let cases = [
            (WorkflowRun::from_status("w", "p"), "Workflow · w · p"),
            (WorkflowRun::from_status("w", ""), "Workflow · w"),
            (
                WorkflowRun {
                    workflow: "w".into(),
                    phase: "p".into(),
                    status: "mismatch".into(),
                },
                "Workflow · w · p (recovery: definition changed)",
            ),
        ];
        for (run, want) in cases {
            assert_eq!(run.label(), want);
        }
    }

    #[test]
    fn replace_confirm_only_when_active_and_names_both() {
        assert!(replace_confirm(&WorkflowRun::default(), "review-fix").is_none());
        let req = replace_confirm(&WorkflowRun::from_status("plan-implement", "plan"), "review-fix")
            .expect("confirm");
        assert!(req.title.contains("plan-implement") && req.title.contains("review-fix"));
        assert!(req.body.contains("phase \"plan\""));
        assert!(req.body.contains("stays active"));
    }

    #[test]
    fn stop_confirm_names_workflow_and_phase() {
        let req = stop_confirm(&WorkflowRun::from_status("plan-implement", "plan"));
        assert!(req.title.contains("plan-implement"));
        assert!(req.body.contains("phase \"plan\""));
        assert!(req.body.contains("permission"));
    }

    #[test]
    fn start_target_requires_a_name() {
        assert!(start_target("  ").is_err());
        assert_eq!(start_target(" review-fix ").unwrap(), "review-fix");
    }

    #[test]
    fn catalog_choices_keep_invalid_rows_with_reason() {
        let rows: CatalogList = serde_json::from_str(
            r#"{"workflows":[{"name":"ok","description":"d","valid":true},{"name":"bad","valid":false,"validationError":"no phases"},{"name":" ","valid":true}]}"#,
        )
        .unwrap();
        let got = catalog_choices(&rows.workflows);
        assert_eq!(got.len(), 2);
        assert_eq!(got[0].id, "ok");
        assert_eq!(got[0].detail, "d");
        assert_eq!(got[1].detail, "Invalid: no phases");
    }
}
