//! Per-turn file-change summary (#1365).
//!
//! `turn.completed` carries `files: [{path, kind}]` (`TurnFileChange`,
//! kind `create | update | delete`), filled by the engine's per-turn diff
//! tracker. The transcript attaches one collapsed summary to the turn's
//! final line — "3 files changed · 2 updated, 1 created" — expandable to
//! the path list grouped by kind. A turn with no file changes renders
//! nothing. Line counts are not in the payload, so slice 1 shows none.
//!
//! This is the per-turn roll-up, not the per-tool-call diff chips (those
//! annotate single calls) and not the workspace Changes tab (cumulative
//! git state with no turn attribution).

use std::collections::HashSet;

use serde_json::Value;

/// One turn's changed paths, grouped by kind, in payload order.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct TurnFiles {
    pub updated: Vec<String>,
    pub created: Vec<String>,
    pub deleted: Vec<String>,
}

impl TurnFiles {
    pub fn total(&self) -> usize {
        self.updated.len() + self.created.len() + self.deleted.len()
    }

    /// "3 files changed · 2 updated, 1 created". Kinds with no paths are
    /// left out; order is updated, created, deleted.
    pub fn label(&self) -> String {
        let n = self.total();
        let noun = if n == 1 { "file" } else { "files" };
        let parts: Vec<String> = [
            (self.updated.len(), "updated"),
            (self.created.len(), "created"),
            (self.deleted.len(), "deleted"),
        ]
        .iter()
        .filter(|(c, _)| *c > 0)
        .map(|(c, k)| format!("{c} {k}"))
        .collect();
        format!("{n} {noun} changed · {}", parts.join(", "))
    }

    /// Non-empty groups as (heading, paths), in label order.
    pub fn groups(&self) -> Vec<(&'static str, &[String])> {
        [
            ("Updated", self.updated.as_slice()),
            ("Created", self.created.as_slice()),
            ("Deleted", self.deleted.as_slice()),
        ]
        .into_iter()
        .filter(|(_, p)| !p.is_empty())
        .collect()
    }
}

/// The summary for a `turn.completed` data object, or `None` when it
/// carries no file changes. Entries without a path are skipped; a path
/// listed twice keeps its first entry. An unknown kind counts as updated
/// (a change of unspecified kind) rather than being dropped.
pub fn turn_files(data: &Value) -> Option<TurnFiles> {
    let list = data.get("files")?.as_array()?;
    let mut out = TurnFiles::default();
    let mut seen = HashSet::new();
    for entry in list {
        let path = entry
            .get("path")
            .and_then(Value::as_str)
            .map(str::trim)
            .unwrap_or("");
        if path.is_empty() || !seen.insert(path.to_string()) {
            continue;
        }
        let kind = entry.get("kind").and_then(Value::as_str).unwrap_or("");
        let group = match kind {
            "create" => &mut out.created,
            "delete" => &mut out.deleted,
            _ => &mut out.updated,
        };
        group.push(path.to_string());
    }
    (out.total() > 0).then_some(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn counts_by_kind() {
        let data = json!({"files": [
            {"path": "a.go", "kind": "update"},
            {"path": "b.go", "kind": "create"},
            {"path": "c.go", "kind": "update"},
            {"path": "d.go", "kind": "delete"},
        ]});
        let f = turn_files(&data).unwrap();
        assert_eq!(f.updated, vec!["a.go", "c.go"]);
        assert_eq!(f.created, vec!["b.go"]);
        assert_eq!(f.deleted, vec!["d.go"]);
        assert_eq!(f.label(), "4 files changed · 2 updated, 1 created, 1 deleted");
    }

    #[test]
    fn label_cases() {
        let cases = [
            (vec![("x", "update")], "1 file changed · 1 updated"),
            (vec![("x", "create"), ("y", "create")], "2 files changed · 2 created"),
            (vec![("x", "delete"), ("y", "update")], "2 files changed · 1 updated, 1 deleted"),
        ];
        for (files, want) in cases {
            let list: Vec<Value> = files
                .iter()
                .map(|(p, k)| json!({"path": p, "kind": k}))
                .collect();
            let f = turn_files(&json!({ "files": list })).unwrap();
            assert_eq!(f.label(), want);
        }
    }

    #[test]
    fn empty_or_absent_renders_nothing() {
        for data in [
            json!({}),
            json!({"files": []}),
            json!({"files": null}),
            json!({"files": [{"path": "", "kind": "update"}]}),
        ] {
            assert_eq!(turn_files(&data), None, "{data}");
        }
    }

    #[test]
    fn duplicate_path_keeps_first_and_unknown_kind_is_updated() {
        let data = json!({"files": [
            {"path": "a.go", "kind": "create"},
            {"path": "a.go", "kind": "update"},
            {"path": "b.go", "kind": "rename"},
        ]});
        let f = turn_files(&data).unwrap();
        assert_eq!(f.created, vec!["a.go"]);
        assert_eq!(f.updated, vec!["b.go"]);
        assert_eq!(f.total(), 2);
    }

    #[test]
    fn groups_skip_empty_kinds_in_label_order() {
        let data = json!({"files": [
            {"path": "z", "kind": "delete"},
            {"path": "y", "kind": "update"},
        ]});
        let f = turn_files(&data).unwrap();
        let heads: Vec<&str> = f.groups().iter().map(|(h, _)| *h).collect();
        assert_eq!(heads, vec!["Updated", "Deleted"]);
    }
}
