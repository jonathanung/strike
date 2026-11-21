//! Plugin lifecycle wire types and the pending-action state machine
//! (#1570). Pure: no browser calls, so the trust gate is unit-tested.
//!
//! Posture mirrors TUI `/plugin` and docs/web.md: the trust preview is
//! rendered before trust is offered; remove and update need an explicit
//! second step and then send `confirm:true`; env and header **keys** only
//! ever appear (the daemon never sends values). Trust state is always read
//! back from `GET /v1/plugins` after an action, never assumed.

use serde::Deserialize;

pub const TRUST_NONE: &str = "none";
pub const TRUST_TRUSTED: &str = "trusted";
pub const TRUST_STALE: &str = "stale";
pub const TRUST_PASSIVE: &str = "n/a-passive-only";

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq, Hash)]
#[serde(default, rename_all = "camelCase")]
pub struct PluginMcp {
    pub name: String,
    pub transport: String,
    pub command: String,
    pub args: Vec<String>,
    pub env_keys: Vec<String>,
    pub url: String,
    pub header_keys: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq, Hash)]
#[serde(default, rename_all = "camelCase")]
pub struct PluginHarness {
    pub name: String,
    pub command: String,
    pub args: Vec<String>,
}

/// One `GET /v1/plugins` row (the fields the lifecycle section reads).
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq, Hash)]
#[serde(default, rename_all = "camelCase")]
pub struct Plugin {
    pub id: String,
    pub version: String,
    pub name: String,
    pub scope: String,
    pub enabled: bool,
    pub status: String,
    pub source_label: String,
    pub trust_state: String,
    pub load_error: String,
    pub has_executable: bool,
    pub update_available: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct PluginList {
    pub plugins: Vec<Plugin>,
}

/// `GET /v1/plugins/{id}/trust-preview`.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
#[serde(default, rename_all = "camelCase")]
pub struct TrustPreview {
    pub id: String,
    pub scope: String,
    pub digest: String,
    pub capabilities: Vec<String>,
    pub mcp: Vec<PluginMcp>,
    pub harnesses: Vec<PluginHarness>,
    pub hooks: u32,
    pub review_lines: Vec<String>,
}

/// `POST /v1/plugins/preview-update`.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
#[serde(default, rename_all = "camelCase")]
pub struct UpdateReview {
    pub id: String,
    pub old_version: String,
    pub new_version: String,
    pub source_label: String,
    pub capability_added: Vec<String>,
    pub capability_removed: Vec<String>,
    pub contrib_added: Vec<String>,
    pub contrib_removed: Vec<String>,
    pub executable_changed: bool,
    pub executable_diffs: Vec<String>,
    pub trust_invalidated: bool,
    pub had_trust: bool,
    pub summary: String,
}

impl Plugin {
    pub fn title(&self) -> &str {
        if self.name.trim().is_empty() {
            &self.id
        } else {
            self.name.trim()
        }
    }

    /// Trust can be granted: executable content without a valid grant.
    pub fn can_trust(&self) -> bool {
        matches!(self.trust_state.as_str(), TRUST_NONE | TRUST_STALE)
    }

    /// A grant exists to revoke (TUI: not for none / passive-only).
    pub fn can_untrust(&self) -> bool {
        matches!(self.trust_state.as_str(), TRUST_TRUSTED | TRUST_STALE)
    }

    pub fn trust_label(&self) -> String {
        match self.trust_state.as_str() {
            TRUST_TRUSTED => "Trusted".into(),
            TRUST_STALE => "Trust stale: contents changed, review again".into(),
            TRUST_NONE => "Not trusted: executable parts stay off".into(),
            TRUST_PASSIVE => "Passive only: no trust needed".into(),
            "" => "Trust state not reported".into(),
            other => format!("Trust: {other}"),
        }
    }
}

/// Ids the daemon reports as outdated, from `GET /v1/plugins/outdated`
/// plus any row that already carries `updateAvailable`.
pub fn outdated(list: &[Plugin], outdated: &[Plugin]) -> Vec<(String, String)> {
    let mut out: Vec<(String, String)> = Vec::new();
    for p in outdated.iter().chain(list.iter()) {
        if p.update_available.is_empty() && !outdated.iter().any(|o| o.id == p.id) {
            continue;
        }
        if out.iter().any(|(id, _)| id == &p.id) {
            continue;
        }
        out.push((p.id.clone(), p.update_available.clone()));
    }
    out
}

/// The single pending action in the section. Only one plugin can be mid-
/// flow at a time, so a confirm can never land on a different row.
#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub enum Flow {
    #[default]
    Idle,
    /// Trust preview requested; waiting for the daemon.
    TrustLoading { id: String, scope: String },
    /// Preview rendered: only now is Trust offered.
    TrustReview { id: String, scope: String, preview: TrustPreview },
    /// Untrust armed.
    UntrustArmed { id: String, scope: String },
    /// Remove armed: the next click sends `confirm:true`.
    RemoveArmed { id: String, scope: String },
    UpdateLoading { id: String, scope: String },
    /// Update review rendered: the next click sends `confirm:true`.
    UpdateReview { id: String, scope: String, review: UpdateReview },
}

pub enum Event {
    AskTrust { id: String, scope: String },
    PreviewLoaded(TrustPreview),
    ArmUntrust { id: String, scope: String },
    ArmRemove { id: String, scope: String },
    AskUpdate { id: String, scope: String },
    ReviewLoaded(UpdateReview),
    /// Cancel, Escape, a failed load, or a finished action.
    Reset,
}

/// Advance the flow. A late preview/review for a different id (or after a
/// cancel) is dropped, so a stale response can never arm another row.
pub fn step(flow: Flow, event: Event) -> Flow {
    match event {
        Event::AskTrust { id, scope } => Flow::TrustLoading { id, scope },
        Event::PreviewLoaded(preview) => match flow {
            Flow::TrustLoading { id, scope } if preview.id.is_empty() || preview.id == id => {
                Flow::TrustReview { id, scope, preview }
            }
            other => other,
        },
        Event::ArmUntrust { id, scope } => Flow::UntrustArmed { id, scope },
        Event::ArmRemove { id, scope } => Flow::RemoveArmed { id, scope },
        Event::AskUpdate { id, scope } => Flow::UpdateLoading { id, scope },
        Event::ReviewLoaded(review) => match flow {
            Flow::UpdateLoading { id, scope } if review.id.is_empty() || review.id == id => {
                Flow::UpdateReview { id, scope, review }
            }
            other => other,
        },
        Event::Reset => Flow::Idle,
    }
}

/// What a confirm click may POST right now, if anything.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Post {
    Trust { id: String, scope: String },
    Untrust { id: String, scope: String },
    Remove { id: String, scope: String },
    Update { id: String, scope: String },
}

impl Post {
    pub fn path(&self) -> &'static str {
        match self {
            Post::Trust { .. } => "/v1/plugins/trust",
            Post::Untrust { .. } => "/v1/plugins/untrust",
            Post::Remove { .. } => "/v1/plugins/remove",
            Post::Update { .. } => "/v1/plugins/update",
        }
    }

    /// Request body. Remove and update carry the daemon's `confirm:true`.
    pub fn body(&self) -> serde_json::Value {
        match self {
            Post::Trust { id, scope } | Post::Untrust { id, scope } => {
                serde_json::json!({"id": id, "scope": scope})
            }
            Post::Remove { id, scope } | Post::Update { id, scope } => {
                serde_json::json!({"id": id, "scope": scope, "confirm": true})
            }
        }
    }

    pub fn done(&self) -> String {
        match self {
            Post::Trust { id, .. } => format!("Trust granted for {id}."),
            Post::Untrust { id, .. } => format!("Trust revoked for {id}."),
            Post::Remove { id, .. } => format!("Removed {id}."),
            Post::Update { id, .. } => format!("Updated {id}."),
        }
    }
}

/// The confirm action for the current flow. Trust is only possible from a
/// rendered preview; loading states confirm nothing.
pub fn confirm(flow: &Flow) -> Option<Post> {
    match flow {
        Flow::TrustReview { id, scope, .. } => Some(Post::Trust { id: id.clone(), scope: scope.clone() }),
        Flow::UntrustArmed { id, scope } => Some(Post::Untrust { id: id.clone(), scope: scope.clone() }),
        Flow::RemoveArmed { id, scope } => Some(Post::Remove { id: id.clone(), scope: scope.clone() }),
        Flow::UpdateReview { id, scope, .. } => Some(Post::Update { id: id.clone(), scope: scope.clone() }),
        Flow::Idle | Flow::TrustLoading { .. } | Flow::UpdateLoading { .. } => None,
    }
}

/// The plugin id the flow is about, if any.
pub fn flow_id(flow: &Flow) -> Option<&str> {
    match flow {
        Flow::Idle => None,
        Flow::TrustLoading { id, .. }
        | Flow::TrustReview { id, .. }
        | Flow::UntrustArmed { id, .. }
        | Flow::RemoveArmed { id, .. }
        | Flow::UpdateLoading { id, .. }
        | Flow::UpdateReview { id, .. } => Some(id),
    }
}

/// MCP server lines for the trust preview. Keys only: values never reach
/// the browser, and nothing here would print one.
pub fn mcp_lines(mcp: &[PluginMcp]) -> Vec<String> {
    mcp.iter()
        .map(|m| {
            let mut line = format!("MCP {}", m.name);
            if !m.transport.is_empty() {
                line.push_str(&format!(" ({})", m.transport));
            }
            if !m.command.is_empty() {
                line.push_str(&format!(": runs {}", m.command));
                if !m.args.is_empty() {
                    line.push(' ');
                    line.push_str(&m.args.join(" "));
                }
            }
            if !m.url.is_empty() {
                line.push_str(&format!(": connects to {}", m.url));
            }
            if !m.env_keys.is_empty() {
                line.push_str(&format!("; env keys {}", m.env_keys.join(", ")));
            }
            if !m.header_keys.is_empty() {
                line.push_str(&format!("; header keys {}", m.header_keys.join(", ")));
            }
            line
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn plugin(id: &str, trust: &str) -> Plugin {
        Plugin { id: id.into(), trust_state: trust.into(), ..Plugin::default() }
    }

    #[test]
    fn decode_list_ignores_unknown_fields() {
        let list: PluginList = serde_json::from_str(
            r#"{"plugins":[{"id":"acme.tools","version":"1.2.0","scope":"global","enabled":true,
            "trustState":"none","hasExecutable":true,"agents":0,"mcp":[{"name":"docs","envKeys":["TOKEN"]}],
            "updateAvailable":"1.3.0","future":true}]}"#,
        )
        .unwrap();
        let p = &list.plugins[0];
        assert_eq!(p.id, "acme.tools");
        assert!(p.can_trust() && !p.can_untrust());
        assert_eq!(p.update_available, "1.3.0");
        let empty: PluginList = serde_json::from_str("{}").unwrap();
        assert!(empty.plugins.is_empty());
    }

    #[test]
    fn trust_gates_by_state() {
        let cases = [
            (TRUST_NONE, true, false),
            (TRUST_STALE, true, true),
            (TRUST_TRUSTED, false, true),
            (TRUST_PASSIVE, false, false),
            ("", false, false),
        ];
        for (state, trust, untrust) in cases {
            let p = plugin("x", state);
            assert_eq!(p.can_trust(), trust, "{state}");
            assert_eq!(p.can_untrust(), untrust, "{state}");
            assert!(!p.trust_label().is_empty());
        }
    }

    #[test]
    fn trust_only_after_preview_renders() {
        let f = step(Flow::Idle, Event::AskTrust { id: "a".into(), scope: "global".into() });
        assert_eq!(confirm(&f), None, "no trust while the preview loads");
        let f = step(f, Event::PreviewLoaded(TrustPreview { id: "a".into(), ..TrustPreview::default() }));
        assert_eq!(confirm(&f), Some(Post::Trust { id: "a".into(), scope: "global".into() }));
        assert_eq!(confirm(&step(f, Event::Reset)), None);
    }

    #[test]
    fn stale_preview_for_another_row_is_dropped() {
        let f = step(Flow::Idle, Event::AskTrust { id: "a".into(), scope: String::new() });
        let f = step(f, Event::PreviewLoaded(TrustPreview { id: "b".into(), ..TrustPreview::default() }));
        assert!(matches!(f, Flow::TrustLoading { .. }));
        assert_eq!(confirm(&f), None);
        // Cancelled before the preview arrived: it must not re-arm.
        let f = step(step(Flow::Idle, Event::AskTrust { id: "a".into(), scope: String::new() }), Event::Reset);
        let f = step(f, Event::PreviewLoaded(TrustPreview { id: "a".into(), ..TrustPreview::default() }));
        assert_eq!(f, Flow::Idle);
        // Same for an update review.
        let f = step(Flow::Idle, Event::ReviewLoaded(UpdateReview { id: "a".into(), ..UpdateReview::default() }));
        assert_eq!(f, Flow::Idle);
    }

    #[test]
    fn remove_and_update_send_confirm_true() {
        let f = step(Flow::Idle, Event::ArmRemove { id: "a".into(), scope: "project".into() });
        let post = confirm(&f).unwrap();
        assert_eq!(post.path(), "/v1/plugins/remove");
        assert_eq!(post.body(), serde_json::json!({"id": "a", "scope": "project", "confirm": true}));

        let f = step(Flow::Idle, Event::AskUpdate { id: "a".into(), scope: String::new() });
        assert_eq!(confirm(&f), None, "no update before the review renders");
        let f = step(f, Event::ReviewLoaded(UpdateReview { id: "a".into(), ..UpdateReview::default() }));
        let post = confirm(&f).unwrap();
        assert_eq!(post.path(), "/v1/plugins/update");
        assert_eq!(post.body()["confirm"], serde_json::json!(true));

        let untrust = confirm(&step(Flow::Idle, Event::ArmUntrust { id: "a".into(), scope: String::new() })).unwrap();
        assert_eq!(untrust.path(), "/v1/plugins/untrust");
        assert!(untrust.body().get("confirm").is_none());
    }

    #[test]
    fn outdated_merges_route_and_rows() {
        let mut a = plugin("a", TRUST_PASSIVE);
        a.update_available = "2.0.0".into();
        let b = plugin("b", TRUST_PASSIVE);
        let c = plugin("c", TRUST_PASSIVE);
        let got = outdated(&[a.clone(), b.clone(), c], &[b, a]);
        let ids: Vec<&str> = got.iter().map(|(id, _)| id.as_str()).collect();
        assert_eq!(ids, ["b", "a"]);
    }

    #[test]
    fn mcp_lines_show_keys_only() {
        let lines = mcp_lines(&[PluginMcp {
            name: "docs".into(),
            transport: "stdio".into(),
            command: "node".into(),
            args: vec!["server.js".into()],
            env_keys: vec!["TOKEN".into()],
            header_keys: vec!["Authorization".into()],
            ..PluginMcp::default()
        }]);
        assert_eq!(
            lines,
            ["MCP docs (stdio): runs node server.js; env keys TOKEN; header keys Authorization"]
        );
    }
}
