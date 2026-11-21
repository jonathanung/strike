//! Customize: skills, agents, MCP servers, plugins, hooks, rules, and roster.
//! Writes go through preview, then confirm. Secret values stay masked.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::{json, Value};

use crate::api::{get_json, js_encode, post_json_value};

pub const SECRET_KEEP: &str = "••••";

pub fn customize_sections() -> &'static [&'static str] {
    &[
        "skills", "agents", "mcp", "plugins", "hooks", "rules", "roster",
    ]
}

pub fn section_label(id: &str) -> &'static str {
    match id {
        "skills" => "Skills",
        "agents" => "Agents",
        "mcp" => "MCP",
        "plugins" => "Plugins",
        "hooks" => "Hooks",
        "rules" => "Rules",
        "roster" => "Roster",
        _ => "Customize",
    }
}

pub fn section_blurb(id: &str) -> &'static str {
    match id {
        "skills" => "Slash skills the agent can run. Builtins and plugin files are copied into this directory before they can be edited.",
        "agents" => "Personas and crew. Permission lines stay as they are. Handoff is the child schema gate.",
        "mcp" => "Servers from global and workspace mcp files. Env and header values are masked. A project file replaces the global map.",
        "plugins" => "Installed plugins. Enable or disable here. Package files are not edited on this page.",
        "hooks" => "Shell commands and declarative rules from config. A hook cannot widen a hard permission denial.",
        "rules" => "AGENTS.md for this directory and the global file. Other instruction files are shown and not written.",
        "roster" => "Stall threshold and whether a child must hand off before it finishes. Clearing stall removes the key; 0 does not override a higher layer.",
        _ => "",
    }
}

pub fn origin_label(origin: &str) -> &'static str {
    match origin {
        "builtin" => "Built-in",
        "global" => "Global",
        "workspace" => "Workspace",
        "plugin" => "Plugin",
        _ => "Other",
    }
}

pub fn step_section(current: &str, delta: i32) -> &'static str {
    let sections = customize_sections();
    let cur = sections.iter().position(|s| *s == current).unwrap_or(0);
    let len = sections.len() as i32;
    let next = (cur as i32 + delta).rem_euclid(len) as usize;
    sections[next]
}

pub fn step_index(len: usize, index: usize, delta: i32) -> usize {
    if len == 0 {
        return 0;
    }
    let cur = index.min(len - 1) as i32;
    (cur + delta).rem_euclid(len as i32) as usize
}

pub fn plugin_api_scope(scope: &str) -> &'static str {
    if scope == "workspace" { "project" } else { "global" }
}

pub fn format_pairs(pairs: &[Pair]) -> String {
    pairs
        .iter()
        .map(|p| format!("{}={}", p.key, p.value))
        .collect::<Vec<_>>()
        .join("\n")
}

pub fn parse_pairs(text: &str) -> Result<Vec<Pair>, String> {
    let mut out = Vec::new();
    for (n, raw) in text.lines().enumerate() {
        let line = raw.trim();
        if line.is_empty() {
            continue;
        }
        let Some((key, value)) = line.split_once('=') else {
            return Err(format!("line {}: expected KEY=value", n + 1));
        };
        let key = key.trim();
        if key.is_empty() {
            return Err(format!("line {}: key is empty", n + 1));
        }
        out.push(Pair { key: key.to_string(), value: value.to_string() });
    }
    Ok(out)
}

fn focus_is_field() -> bool {
    let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
        return false;
    };
    let Some(el) = doc.active_element() else {
        return false;
    };
    matches!(el.tag_name().to_ascii_lowercase().as_str(), "input" | "textarea" | "select")
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
struct Catalog {
    work_dir: String,
    skills: Vec<SkillItem>,
    agents: Vec<AgentItem>,
    mcp: Vec<McpItem>,
    plugins: Vec<PluginItem>,
    hooks: Vec<HookItem>,
    rules: Vec<RuleItem>,
    roster: Roster,
    hook_events: Vec<String>,
    warnings: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
struct SkillItem {
    name: String,
    description: String,
    origin: String,
    scope: String,
    path: String,
    editable: bool,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
struct AgentItem {
    name: String,
    description: String,
    origin: String,
    scope: String,
    path: String,
    editable: bool,
    handoff: String,
    permissions: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
struct McpItem {
    name: String,
    origin: String,
    scope: String,
    path: String,
    editable: bool,
    active: bool,
    #[serde(rename = "type")]
    typ: String,
    command: String,
    url: String,
    env_keys: Vec<String>,
    state: String,
    error: String,
    tool_count: i32,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
struct PluginItem {
    id: String,
    name: String,
    scope: String,
    origin: String,
    enabled: bool,
    status: String,
    agents: i32,
    skills: i32,
    hooks: i32,
    mcp: i32,
    load_error: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
struct HookItem {
    scope: String,
    origin: String,
    index: i32,
    event: String,
    matcher: String,
    action: String,
    message: String,
    command: String,
    timeout_ms: i32,
    plugin_id: String,
    editable: bool,
    base_hash: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
struct RuleItem {
    name: String,
    origin: String,
    scope: String,
    path: String,
    excerpt: String,
    editable: bool,
    exists: bool,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
struct Roster {
    handoff_required: bool,
    handoff_source: String,
    global_handoff: String,
    workspace_handoff: String,
    stall_after_s: i32,
    stall_source: String,
    global_stall: i32,
    workspace_stall: i32,
    global_base_hash: String,
    workspace_base_hash: String,
    note: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
struct Doc {
    kind: String,
    name: String,
    scope: String,
    origin: String,
    path: String,
    editable: bool,
    base_hash: String,
    description: String,
    body: String,
    model: String,
    provider: String,
    effort: String,
    harness: String,
    handoff: String,
    permissions: Vec<String>,
    secret_redacted: bool,
    mcp: Option<McpDraft>,
    hook: Option<HookDraft>,
    index: Option<i32>,
}

#[derive(Clone, Debug, Default, Deserialize, serde::Serialize)]
#[serde(default, rename_all = "camelCase")]
struct McpDraft {
    #[serde(rename = "type")]
    typ: String,
    command: String,
    args: Vec<String>,
    url: String,
    keep_url: bool,
    keep_command: bool,
    cwd: String,
    env: Vec<Pair>,
    headers: Vec<Pair>,
}

#[derive(Clone, Debug, Default, Deserialize, serde::Serialize)]
#[serde(default)]
pub struct Pair {
    pub key: String,
    pub value: String,
}

#[derive(Clone, Debug, Default, Deserialize, serde::Serialize)]
#[serde(default, rename_all = "camelCase")]
struct HookDraft {
    event: String,
    matcher: String,
    action: String,
    message: String,
    command: String,
    timeout_ms: i32,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(default, rename_all = "camelCase")]
struct DiffResult {
    diff: String,
    diff_hash: String,
    base_hash: String,
    warnings: Vec<String>,
}

impl Default for DiffResult {
    fn default() -> Self {
        Self {
            diff: String::new(),
            diff_hash: String::new(),
            base_hash: String::new(),
            warnings: Vec::new(),
        }
    }
}

#[derive(Clone)]
struct Row {
    title: String,
    meta: String,
    origin: String,
    name: String,
    scope: String,
    kind: String,
    index: Option<i32>,
    plugin_id: String,
    enabled: bool,
    base_hash: String,
}

fn rows_for(section: &str, cat: &Catalog) -> Vec<Row> {
    match section {
        "skills" => cat.skills.iter().map(|s| Row {
            title: s.name.clone(),
            meta: if s.description.is_empty() { s.path.clone() } else { s.description.clone() },
            origin: s.origin.clone(),
            name: s.name.clone(),
            scope: if s.scope.is_empty() { "workspace".into() } else { s.scope.clone() },
            kind: "skill".into(),
            index: None,
            plugin_id: String::new(),
            enabled: false,
            base_hash: String::new(),
        }).collect(),
        "agents" => cat.agents.iter().map(|s| Row {
            title: s.name.clone(),
            meta: {
                let mut m = s.description.clone();
                if !s.handoff.is_empty() {
                    if !m.is_empty() { m.push_str(" · "); }
                    m.push_str("handoff ");
                    m.push_str(&s.handoff);
                }
                m
            },
            origin: s.origin.clone(),
            name: s.name.clone(),
            scope: if s.scope.is_empty() { "workspace".into() } else { s.scope.clone() },
            kind: "agent".into(),
            index: None,
            plugin_id: String::new(),
            enabled: false,
            base_hash: String::new(),
        }).collect(),
        "mcp" => cat.mcp.iter().map(|s| {
            let mut meta = s.typ.clone();
            if !s.state.is_empty() {
                meta.push_str(" · ");
                meta.push_str(&s.state);
            }
            if s.tool_count > 0 {
                meta.push_str(&format!(" · {} tools", s.tool_count));
            }
            if !s.active {
                meta.push_str(" · inactive");
            }
            if !s.command.is_empty() {
                meta.push_str(" · ");
                meta.push_str(&s.command);
            }
            Row {
                title: s.name.clone(),
                meta,
                origin: s.origin.clone(),
                name: s.name.clone(),
                scope: if s.scope.is_empty() { "global".into() } else { s.scope.clone() },
                kind: "mcp".into(),
                index: None,
                plugin_id: String::new(),
                enabled: s.active,
                base_hash: String::new(),
            }
        }).collect(),
        "plugins" => cat.plugins.iter().map(|s| Row {
            title: if s.name.is_empty() { s.id.clone() } else { s.name.clone() },
            meta: format!("{} · {} skills · {} agents · {} hooks · {} mcp", s.status, s.skills, s.agents, s.hooks, s.mcp),
            origin: s.origin.clone(),
            name: s.id.clone(),
            scope: s.scope.clone(),
            kind: "plugin".into(),
            index: None,
            plugin_id: s.id.clone(),
            enabled: s.enabled,
            base_hash: String::new(),
        }).collect(),
        "hooks" => cat.hooks.iter().map(|s| {
            let mut meta = s.event.clone();
            if !s.command.is_empty() {
                meta.push_str(" · ");
                meta.push_str(&s.command);
            } else if !s.action.is_empty() {
                meta.push_str(" · ");
                meta.push_str(&s.action);
            }
            if !s.plugin_id.is_empty() {
                meta.push_str(" · ");
                meta.push_str(&s.plugin_id);
            }
            Row {
                title: if s.event.is_empty() { format!("Hook {}", s.index) } else { s.event.clone() },
                meta,
                origin: s.origin.clone(),
                name: String::new(),
                scope: s.scope.clone(),
                kind: "hook".into(),
                index: Some(s.index),
                plugin_id: s.plugin_id.clone(),
                enabled: false,
                base_hash: s.base_hash.clone(),
            }
        }).collect(),
        "rules" => cat.rules.iter().map(|s| Row {
            title: s.name.clone(),
            meta: if s.excerpt.is_empty() { if s.exists { s.path.clone() } else { "Not written yet".into() } } else { s.excerpt.clone() },
            origin: s.origin.clone(),
            name: s.name.clone(),
            scope: s.scope.clone(),
            kind: "rule".into(),
            index: None,
            plugin_id: String::new(),
            enabled: false,
            base_hash: String::new(),
        }).collect(),
        "roster" => vec![
            Row {
                title: "Global".into(),
                meta: format!("stall {}s · handoff {}", cat.roster.global_stall, cat.roster.global_handoff),
                origin: "global".into(),
                name: "roster".into(),
                scope: "global".into(),
                kind: "roster".into(),
                index: None,
                plugin_id: String::new(),
                enabled: false,
                base_hash: cat.roster.global_base_hash.clone(),
            },
            Row {
                title: "This directory".into(),
                meta: format!("stall {}s · handoff {}", cat.roster.workspace_stall, cat.roster.workspace_handoff),
                origin: "workspace".into(),
                name: "roster".into(),
                scope: "workspace".into(),
                kind: "roster".into(),
                index: None,
                plugin_id: String::new(),
                enabled: false,
                base_hash: cat.roster.workspace_base_hash.clone(),
            },
        ],
        _ => Vec::new(),
    }
}

fn blank_doc(kind: &str) -> Doc {
    Doc {
        kind: kind.to_string(),
        scope: if kind == "mcp" || kind == "hook" { "global".into() } else { "workspace".into() },
        editable: true,
        base_hash: "missing".into(),
        handoff: "inherit".into(),
        mcp: Some(McpDraft { typ: "stdio".into(), ..McpDraft::default() }),
        hook: Some(HookDraft { event: "pre_tool_use".into(), ..HookDraft::default() }),
        ..Doc::default()
    }
}

#[component]
pub fn CustomizePane(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
) -> impl IntoView {
    let section = RwSignal::new("skills".to_string());
    let catalog = RwSignal::new(Catalog::default());
    let tick = RwSignal::new(0u32);
    let err = RwSignal::new(String::new());
    let selected = RwSignal::new(None::<usize>);
    let editing = RwSignal::new(false);
    let copying = RwSignal::new(false);
    let form_on = RwSignal::new(false);
    let doc_kind = RwSignal::new(String::new());
    let name = RwSignal::new(String::new());
    let description = RwSignal::new(String::new());
    let body = RwSignal::new(String::new());
    let scope = RwSignal::new("workspace".to_string());
    let origin = RwSignal::new(String::new());
    let path_label = RwSignal::new(String::new());
    let base_hash = RwSignal::new("missing".to_string());
    let model = RwSignal::new(String::new());
    let provider = RwSignal::new(String::new());
    let effort = RwSignal::new(String::new());
    let harness = RwSignal::new(String::new());
    let handoff = RwSignal::new("inherit".to_string());
    let permissions = RwSignal::new(Vec::<String>::new());
    let secret = RwSignal::new(false);
    let mcp = RwSignal::new(McpDraft::default());
    let env_text = RwSignal::new(String::new());
    let header_text = RwSignal::new(String::new());
    let hook = RwSignal::new(HookDraft::default());
    let hook_mode = RwSignal::new("command".to_string());
    let hook_index = RwSignal::new(None::<i32>);
    let stall_op = RwSignal::new("unchanged".to_string());
    let stall_seconds = RwSignal::new("120".to_string());
    let roster_handoff = RwSignal::new("unchanged".to_string());
    let plugin_enabled = RwSignal::new(false);
    let plugin_id = RwSignal::new(String::new());
    let diff = RwSignal::new(String::new());
    let diff_hash = RwSignal::new(String::new());
    let warnings = RwSignal::new(Vec::<String>::new());
    let busy = RwSignal::new(false);
    let arm_remove = RwSignal::new(false);
    let arm_disable = RwSignal::new(false);

    let clear_diff = move || {
        diff.set(String::new());
        diff_hash.set(String::new());
    };

    Effect::new(move |_| {
        let _ = tick.get();
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match get_json::<Catalog>(&b, &t, "/v1/customize").await {
                Ok(cat) => {
                    catalog.set(cat);
                    err.set(String::new());
                }
                Err(e) => err.set(e),
            }
        });
    });

    let apply_doc = move |d: Doc| {
        doc_kind.set(d.kind.clone());
        name.set(d.name.clone());
        description.set(d.description.clone());
        body.set(d.body.clone());
        scope.set(if d.scope.is_empty() { "workspace".into() } else { d.scope.clone() });
        origin.set(d.origin.clone());
        path_label.set(d.path.clone());
        base_hash.set(if d.base_hash.is_empty() { "missing".into() } else { d.base_hash.clone() });
        model.set(d.model.clone());
        provider.set(d.provider.clone());
        effort.set(d.effort.clone());
        harness.set(d.harness.clone());
        handoff.set(if d.handoff.is_empty() { "inherit".into() } else { d.handoff.clone() });
        permissions.set(d.permissions.clone());
        secret.set(d.secret_redacted);
        form_on.set(d.editable);
        copying.set(false);
        if let Some(draft) = d.mcp.clone() {
            env_text.set(format_pairs(&draft.env));
            header_text.set(format_pairs(&draft.headers));
            mcp.set(draft);
        }
        if let Some(draft) = d.hook.clone() {
            hook_mode.set(if !draft.action.is_empty() { "rule".into() } else { "command".into() });
            hook.set(draft);
        }
        hook_index.set(d.index);
        editing.set(true);
        clear_diff();
    };

    let open_index = move |i: usize| {
        arm_remove.set(false);
        arm_disable.set(false);
        selected.set(Some(i));
        err.set(String::new());
        let sec = section.get_untracked();
        let cat = catalog.get_untracked();
        let rows = rows_for(&sec, &cat);
        let Some(row) = rows.get(i).cloned() else { return };
        selected.set(Some(i));
        editing.set(true);
        doc_kind.set(row.kind.clone());
        name.set(row.title.clone());
        origin.set(row.origin.clone());
        scope.set(if row.scope.is_empty() { "workspace".into() } else { row.scope.clone() });
        if row.kind == "plugin" {
            doc_kind.set("plugin".into());
            name.set(row.title.clone());
            plugin_id.set(row.plugin_id.clone());
            scope.set(row.scope.clone());
            origin.set(row.origin.clone());
            plugin_enabled.set(row.enabled);
            path_label.set(String::new());
            form_on.set(false);
            copying.set(false);
            editing.set(true);
            clear_diff();
            return;
        }
        if row.kind == "roster" {
            doc_kind.set("roster".into());
            name.set(row.title.clone());
            scope.set(row.scope.clone());
            origin.set(row.origin.clone());
            base_hash.set(row.base_hash.clone());
            form_on.set(true);
            copying.set(false);
            stall_op.set("unchanged".into());
            roster_handoff.set("unchanged".into());
            let seconds = if row.scope == "global" { cat.roster.global_stall } else { cat.roster.workspace_stall };
            stall_seconds.set(if seconds > 0 { seconds.to_string() } else { "120".into() });
            editing.set(true);
            clear_diff();
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let mut path = format!(
            "/v1/customize/doc?kind={}&name={}&scope={}",
            js_encode(&row.kind),
            js_encode(&row.name),
            js_encode(&row.scope),
        );
        if let Some(index) = row.index {
            path.push_str(&format!("&index={index}"));
        }
        spawn_local(async move {
            match get_json::<Doc>(&b, &t, &path).await {
                Ok(d) => apply_doc(d),
                Err(e) => err.set(e),
            }
        });
    };

    let start_new = move || {
        arm_remove.set(false);
        arm_disable.set(false);
        let kind = match section.get_untracked().as_str() {
            "agents" => "agent",
            "mcp" => "mcp",
            "hooks" => "hook",
            "rules" => "rule",
            _ => "skill",
        };
        let d = blank_doc(kind);
        selected.set(None);
        apply_doc(d);
        if kind == "hook" {
            hook_index.set(None);
        }
        name.set(String::new());
        description.set(String::new());
        body.set(String::new());
        form_on.set(true);
    };

    let build_change = move |confirm: bool| -> Result<Value, String> {
        let kind = doc_kind.get_untracked();
        let mut v = json!({
            "confirm": confirm,
            "kind": kind,
            "op": "upsert",
            "scope": scope.get_untracked(),
            "name": name.get_untracked(),
            "description": description.get_untracked(),
            "body": body.get_untracked(),
            "baseHash": base_hash.get_untracked(),
            "model": model.get_untracked(),
            "provider": provider.get_untracked(),
            "effort": effort.get_untracked(),
            "harness": harness.get_untracked(),
            "handoff": handoff.get_untracked(),
        });
        if confirm {
            v["diffHash"] = json!(diff_hash.get_untracked());
        }
        if kind == "mcp" {
            let mut draft = mcp.get_untracked();
            draft.env = parse_pairs(&env_text.get_untracked())?;
            draft.headers = parse_pairs(&header_text.get_untracked())?;
            v["mcp"] = serde_json::to_value(&draft).map_err(|e| e.to_string())?;
        }
        if kind == "hook" {
            let mut draft = hook.get_untracked();
            if hook_mode.get_untracked() == "rule" {
                draft.command.clear();
            } else {
                draft.action.clear();
                draft.message.clear();
            }
            v["hook"] = serde_json::to_value(&draft).map_err(|e| e.to_string())?;
            if let Some(index) = hook_index.get_untracked() {
                v["index"] = json!(index);
            }
        }
        if kind == "roster" {
            let seconds: i32 = stall_seconds.get_untracked().trim().parse().unwrap_or(0);
            v["stallOp"] = json!(stall_op.get_untracked());
            v["stallSeconds"] = json!(seconds);
            v["rosterHandoff"] = json!(roster_handoff.get_untracked());
        }
        Ok(v)
    };

    let preview = move || {
        err.set(String::new());
        let ch = match build_change(false) {
            Ok(v) => v,
            Err(e) => { err.set(e); return; }
        };
        let (b, t) = (base.get_untracked(), token.get_untracked());
        busy.set(true);
        spawn_local(async move {
            let result = post_json_value(&b, &t, "/v1/customize/preview", ch).await;
            busy.set(false);
            match result.and_then(|v| serde_json::from_value::<DiffResult>(v).map_err(|e| e.to_string())) {
                Ok(res) => {
                    diff.set(res.diff);
                    diff_hash.set(res.diff_hash);
                    if !res.base_hash.is_empty() {
                        base_hash.set(res.base_hash);
                    }
                    warnings.set(res.warnings);
                }
                Err(e) => {
                    diff.set(String::new());
                    diff_hash.set(String::new());
                    err.set(e);
                }
            }
        });
    };

    let save = move || {
        if diff_hash.get_untracked().is_empty() {
            return;
        }
        err.set(String::new());
        let ch = match build_change(true) {
            Ok(v) => v,
            Err(e) => { err.set(e); return; }
        };
        let (b, t) = (base.get_untracked(), token.get_untracked());
        busy.set(true);
        spawn_local(async move {
            let result = post_json_value(&b, &t, "/v1/customize/apply", ch).await;
            busy.set(false);
            match result {
                Ok(_) => {
                    status.set("Saved".into());
                    editing.set(false);
                    clear_diff();
                    tick.update(|n| *n += 1);
                }
                Err(e) => err.set(e),
            }
        });
    };

    let remove = move || {
        let mut ch = match build_change(false) {
            Ok(v) => v,
            Err(e) => { err.set(e); return; }
        };
        ch["op"] = json!("delete");
        let (b, t) = (base.get_untracked(), token.get_untracked());
        busy.set(true);
        spawn_local(async move {
            let previewed = post_json_value(&b, &t, "/v1/customize/preview", ch.clone()).await;
            let v = match previewed {
                Ok(v) => v,
                Err(e) => {
                    busy.set(false);
                    err.set(e);
                    return;
                }
            };
            let res = match serde_json::from_value::<DiffResult>(v) {
                Ok(res) => res,
                Err(e) => {
                    busy.set(false);
                    err.set(e.to_string());
                    return;
                }
            };
            ch["confirm"] = json!(true);
            ch["diffHash"] = json!(res.diff_hash);
            ch["baseHash"] = json!(res.base_hash);
            let applied = post_json_value(&b, &t, "/v1/customize/apply", ch).await;
            busy.set(false);
            match applied {
                Ok(_) => {
                    status.set("Removed".into());
                    editing.set(false);
                    clear_diff();
                    tick.update(|n| *n += 1);
                }
                Err(e) => err.set(e),
            }
        });
    };

    let toggle_plugin = move || {
        let id = plugin_id.get_untracked();
        let enable = !plugin_enabled.get_untracked();
        let path = if enable { "/v1/plugins/enable" } else { "/v1/plugins/disable" };
        let body = json!({ "id": id, "scope": plugin_api_scope(&scope.get_untracked()) });
        let (b, t) = (base.get_untracked(), token.get_untracked());
        busy.set(true);
        spawn_local(async move {
            let result = post_json_value(&b, &t, path, body).await;
            busy.set(false);
            match result {
                Ok(_) => {
                    status.set(if enable { "Plugin enabled".into() } else { "Plugin disabled".into() });
                    plugin_enabled.set(enable);
                    tick.update(|n| *n += 1);
                }
                Err(e) => err.set(e),
            }
        });
    };

    let on_list_key = move |ev: web_sys::KeyboardEvent| {
        let key = ev.key();
        if key == "Escape" {
            if focus_is_field() || !editing.get_untracked() {
                return;
            }
            ev.prevent_default();
            editing.set(false);
            clear_diff();
            return;
        }
        if focus_is_field() {
            return;
        }
        let delta = match key.as_str() {
            "ArrowDown" => 1,
            "ArrowUp" => -1,
            _ => return,
        };
        ev.prevent_default();
        let rows = rows_for(&section.get_untracked(), &catalog.get_untracked());
        if rows.is_empty() {
            return;
        }
        let cur = selected.get_untracked().unwrap_or(0);
        let next = step_index(rows.len(), cur, delta);
        open_index(next);
    };

    view! {
        <div class="customize" on:keydown=move |ev| on_list_key(ev) tabindex="0">
            <nav class="cz-tabs" role="tablist" aria-label="Customize sections" tabindex="0"
                on:keydown=move |ev: web_sys::KeyboardEvent| {
                    let delta = match ev.key().as_str() {
                        "ArrowRight" | "ArrowDown" => 1,
                        "ArrowLeft" | "ArrowUp" => -1,
                        _ => return,
                    };
                    ev.prevent_default();
                    ev.stop_propagation();
                    let next = step_section(&section.get_untracked(), delta);
                    section.set(next.into());
                    selected.set(None);
                    editing.set(false);
                    clear_diff();
                }>
                {move || customize_sections().iter().map(|id| {
                    let id = *id;
                    let on = move || section.get() == id;
                    view! {
                        <button
                            type="button"
                            role="tab"
                            data-cz-section=id
                            aria-selected=move || if on() { "true" } else { "false" }
                            class=move || if on() { "cz-tab on" } else { "cz-tab" }
                            on:click=move |_| {
                                section.set(id.into());
                                selected.set(None);
                                editing.set(false);
                                clear_diff();
                            }
                        >{section_label(id)}</button>
                    }
                }).collect_view()}
            </nav>
            <section class="cz-list" aria-label="Items">
                <header class="cz-list-head">
                    <h2>{move || section_label(&section.get())}</h2>
                    <Show when=move || matches!(section.get().as_str(), "skills" | "agents" | "mcp" | "hooks" | "rules")>
                        <button type="button" class="secondary" data-cz-new on:click=move |_| start_new()>"New"</button>
                    </Show>
                </header>
                <p class="hint">{move || section_blurb(&section.get())}</p>
                <Show when=move || !catalog.get().work_dir.is_empty()>
                    <p class="hint cz-dir">{move || format!("Directory {}", catalog.get().work_dir)}</p>
                </Show>
                <div class="cz-rows">
                    {move || {
                        let rows = rows_for(&section.get(), &catalog.get());
                        let current = selected.get();
                        if rows.is_empty() {
                            return view! { <p class="hint">"Nothing in this section."</p> }.into_any();
                        }
                        rows.into_iter().enumerate().map(|(i, row)| {
                            let open = open_index;
                            let title = row.title.clone();
                            let meta = row.meta.clone();
                            let origin = row.origin.clone();
                            let item = row.name.clone();
                            let class = if current == Some(i) { "cz-row on" } else { "cz-row" };
                            view! {
                                <button type="button" class=class data-cz-item=item on:click=move |_| open(i)>
                                    <span class="cz-row-title">{title}</span>
                                    <span class="badge">{origin_label(&origin)}</span>
                                    <span class="cz-row-meta">{meta}</span>
                                </button>
                            }
                        }).collect_view().into_any()
                    }}
                </div>
            </section>
            <section class="cz-editor" aria-label="Editor">
                <Show when=move || !err.get().is_empty()>
                    <p class="cz-error" role="alert">{move || err.get()}</p>
                </Show>
                <Show when=move || !catalog.get().warnings.is_empty()>
                    <ul class="cz-warn">
                        {move || catalog.get().warnings.into_iter().map(|w| view! { <li>{w}</li> }).collect_view()}
                    </ul>
                </Show>
                <Show when=move || editing.get() fallback=move || view! {
                    <p class="hint">"Select an item. Arrow keys move the list. Escape closes the editor when focus is not in a field."</p>
                }>
                    <header class="cz-editor-head">
                        <h2>{move || {
                            let n = name.get();
                            if n.is_empty() { format!("New {}", doc_kind.get()) } else { n }
                        }}</h2>
                        <span class="badge">{move || origin_label(&origin.get())}</span>
                    </header>
                    <Show when=move || !path_label.get().is_empty()>
                        <p class="hint cz-path">{move || path_label.get()}</p>
                    </Show>
                    <Show when=move || secret.get()>
                        <p class="cz-error">"This file contains a credential-shaped value. Customize will not edit it."</p>
                    </Show>
                    <Show when=move || doc_kind.get() == "plugin">
                        <p class="hint">"Enable or disable this plugin. Its files stay where they were installed."</p>
                        <Show when=move || plugin_enabled.get() && arm_disable.get() fallback=move || view! {
                            <button type="button" class="primary" data-cz-plugin disabled=move || busy.get()
                                on:click=move |_| {
                                    if plugin_enabled.get_untracked() {
                                        arm_disable.set(true);
                                    } else {
                                        toggle_plugin();
                                    }
                                }>
                                {move || if plugin_enabled.get() { "Disable" } else { "Enable" }}
                            </button>
                        }>
                            <p class="cz-error" role="alert">"Disable this plugin? It stops loading until you enable it again."</p>
                            <div class="cz-actions">
                                <button type="button" class="primary" data-cz-confirm-disable disabled=move || busy.get()
                                    on:click=move |_| { arm_disable.set(false); toggle_plugin(); }>"Confirm disable"</button>
                                <button type="button" class="secondary" on:click=move |_| arm_disable.set(false)>"Cancel"</button>
                            </div>
                        </Show>
                    </Show>
                    <Show when=move || matches!(doc_kind.get().as_str(), "skill" | "agent" | "rule" | "mcp" | "hook" | "roster")>
                        <div class="cz-form">
                            <Show when=move || matches!(doc_kind.get().as_str(), "skill" | "agent" | "mcp")>
                                <label>"Name"
                                    <input data-cz-field="name" prop:value=move || name.get() disabled=move || !form_on.get() || !name.get().is_empty() && base_hash.get() != "missing"
                                        on:input=move |ev| { name.set(event_target_value(&ev)); clear_diff(); } />
                                </label>
                            </Show>
                            <label>"Scope"
                                <select data-cz-field="scope" prop:value=move || scope.get() disabled=move || !form_on.get() && !copying.get()
                                    on:change=move |ev| { scope.set(event_target_value(&ev)); clear_diff(); }>
                                    <option value="workspace">"This directory"</option>
                                    <option value="global">"Global"</option>
                                </select>
                            </label>
                            <Show when=move || matches!(doc_kind.get().as_str(), "skill" | "agent")>
                                <label>"Description"
                                    <input data-cz-field="description" prop:value=move || description.get() disabled=move || !form_on.get() && !copying.get()
                                        on:input=move |ev| { description.set(event_target_value(&ev)); clear_diff(); } />
                                </label>
                            </Show>
                            <Show when=move || doc_kind.get() == "agent">
                                <label>"Model" <input prop:value=move || model.get() disabled=move || !form_on.get() && !copying.get() on:input=move |ev| { model.set(event_target_value(&ev)); clear_diff(); } /></label>
                                <label>"Provider" <input prop:value=move || provider.get() disabled=move || !form_on.get() && !copying.get() on:input=move |ev| { provider.set(event_target_value(&ev)); clear_diff(); } /></label>
                                <label>"Effort" <input prop:value=move || effort.get() disabled=move || !form_on.get() && !copying.get() on:input=move |ev| { effort.set(event_target_value(&ev)); clear_diff(); } /></label>
                                <label>"Harness" <input prop:value=move || harness.get() disabled=move || !form_on.get() && !copying.get() on:input=move |ev| { harness.set(event_target_value(&ev)); clear_diff(); } /></label>
                                <label>"Handoff"
                                    <select prop:value=move || handoff.get() disabled=move || !form_on.get() && !copying.get() on:change=move |ev| { handoff.set(event_target_value(&ev)); clear_diff(); }>
                                        <option value="inherit">"Inherit session"</option>
                                        <option value="required">"Required"</option>
                                        <option value="optional">"Optional"</option>
                                    </select>
                                </label>
                                <Show when=move || !permissions.get().is_empty()>
                                    <p class="hint">"Permission lines are kept. This page does not edit them."</p>
                                    <ul class="cz-perms">{move || permissions.get().into_iter().map(|p| view! { <li>{p}</li> }).collect_view()}</ul>
                                </Show>
                            </Show>
                            <Show when=move || matches!(doc_kind.get().as_str(), "skill" | "agent" | "rule")>
                                <label>{move || if doc_kind.get() == "rule" { "Rules" } else { "Body" }}
                                    <textarea data-cz-field="body" prop:value=move || body.get() disabled=move || !form_on.get() && !copying.get()
                                        on:input=move |ev| { body.set(event_target_value(&ev)); clear_diff(); }></textarea>
                                </label>
                            </Show>
                            <Show when=move || doc_kind.get() == "mcp">
                                <label>"Type"
                                    <select prop:value=move || mcp.get().typ disabled=move || !form_on.get()
                                        on:change=move |ev| { let v = event_target_value(&ev); mcp.update(|m| m.typ = v); clear_diff(); }>
                                        <option value="stdio">"stdio"</option>
                                        <option value="http">"http"</option>
                                    </select>
                                </label>
                                <Show when=move || mcp.get().keep_command fallback=move || view! {
                                    <label>"Command"
                                        <input data-cz-field="command" prop:value=move || mcp.get().command disabled=move || !form_on.get()
                                            on:input=move |ev| { let v = event_target_value(&ev); mcp.update(|m| m.command = v); clear_diff(); } />
                                    </label>
                                }>
                                    <p class="hint">"The command is stored and hidden because it looks like a credential."</p>
                                </Show>
                                <label>"Arguments"
                                    <input prop:value=move || mcp.get().args.join(" ") disabled=move || !form_on.get() || mcp.get().keep_command
                                        on:input=move |ev| {
                                            let v = event_target_value(&ev);
                                            mcp.update(|m| m.args = if v.trim().is_empty() { Vec::new() } else { v.split_whitespace().map(|s| s.to_string()).collect() });
                                            clear_diff();
                                        } />
                                </label>
                                <Show when=move || mcp.get().keep_url fallback=move || view! {
                                    <label>"URL"
                                        <input prop:value=move || mcp.get().url disabled=move || !form_on.get()
                                            on:input=move |ev| { let v = event_target_value(&ev); mcp.update(|m| m.url = v); clear_diff(); } />
                                    </label>
                                }>
                                    <p class="hint">"The URL is stored and hidden because it looks like a credential."</p>
                                </Show>
                                <label>"Environment (KEY=value, one per line). " {SECRET_KEEP} " keeps the stored value."
                                    <textarea data-cz-field="env" prop:value=move || env_text.get() disabled=move || !form_on.get()
                                        on:input=move |ev| { env_text.set(event_target_value(&ev)); clear_diff(); }></textarea>
                                </label>
                                <label>"Headers, same shape."
                                    <textarea prop:value=move || header_text.get() disabled=move || !form_on.get()
                                        on:input=move |ev| { header_text.set(event_target_value(&ev)); clear_diff(); }></textarea>
                                </label>
                            </Show>
                            <Show when=move || doc_kind.get() == "hook">
                                <label>"Event"
                                    <select prop:value=move || hook.get().event disabled=move || !form_on.get()
                                        on:change=move |ev| { let v = event_target_value(&ev); hook.update(|h| h.event = v); clear_diff(); }>
                                        {move || catalog.get().hook_events.into_iter().map(|ev| {
                                            let value = ev.clone();
                                            view! { <option value=value>{ev}</option> }
                                        }).collect_view()}
                                    </select>
                                </label>
                                <label>"Kind"
                                    <select prop:value=move || hook_mode.get() disabled=move || !form_on.get()
                                        on:change=move |ev| { hook_mode.set(event_target_value(&ev)); clear_diff(); }>
                                        <option value="command">"Shell command"</option>
                                        <option value="rule">"Declarative rule"</option>
                                    </select>
                                </label>
                                <label>"Matcher"
                                    <input prop:value=move || hook.get().matcher disabled=move || !form_on.get()
                                        on:input=move |ev| { let v = event_target_value(&ev); hook.update(|h| h.matcher = v); clear_diff(); } />
                                </label>
                                <Show when=move || hook_mode.get() == "command">
                                    <label>"Command"
                                        <input prop:value=move || hook.get().command disabled=move || !form_on.get()
                                            on:input=move |ev| { let v = event_target_value(&ev); hook.update(|h| h.command = v); clear_diff(); } />
                                    </label>
                                </Show>
                                <Show when=move || hook_mode.get() == "rule">
                                    <label>"Action"
                                        <select prop:value=move || hook.get().action disabled=move || !form_on.get()
                                            on:change=move |ev| { let v = event_target_value(&ev); hook.update(|h| h.action = v); clear_diff(); }>
                                            <option value="log">"log"</option>
                                            <option value="notify">"notify"</option>
                                            <option value="block">"block"</option>
                                        </select>
                                    </label>
                                    <label>"Message"
                                        <input prop:value=move || hook.get().message disabled=move || !form_on.get()
                                            on:input=move |ev| { let v = event_target_value(&ev); hook.update(|h| h.message = v); clear_diff(); } />
                                    </label>
                                </Show>
                                <label>"Timeout ms"
                                    <input prop:value=move || hook.get().timeout_ms.to_string() disabled=move || !form_on.get()
                                        on:input=move |ev| {
                                            let v = event_target_value(&ev);
                                            if let Ok(n) = v.trim().parse::<i32>() {
                                                hook.update(|h| h.timeout_ms = n);
                                            }
                                            clear_diff();
                                        } />
                                </label>
                            </Show>
                            <Show when=move || doc_kind.get() == "roster">
                                <p class="hint">{move || catalog.get().roster.note}</p>
                                <p class="hint">{move || format!("Effective stall {}s from {}. Handoff {} from {}.", catalog.get().roster.stall_after_s, catalog.get().roster.stall_source, if catalog.get().roster.handoff_required { "required" } else { "optional" }, catalog.get().roster.handoff_source)}</p>
                                <label>"Stall"
                                    <select data-cz-field="stall" prop:value=move || stall_op.get() on:change=move |ev| { stall_op.set(event_target_value(&ev)); clear_diff(); }>
                                        <option value="unchanged">"Leave as it is"</option>
                                        <option value="set">"Set seconds"</option>
                                        <option value="clear">"Remove the key"</option>
                                    </select>
                                </label>
                                <label>"Seconds"
                                    <input data-cz-field="stall-seconds" prop:value=move || stall_seconds.get() disabled=move || stall_op.get() != "set"
                                        on:input=move |ev| { stall_seconds.set(event_target_value(&ev)); clear_diff(); } />
                                </label>
                                <label>"Handoff"
                                    <select prop:value=move || roster_handoff.get() on:change=move |ev| { roster_handoff.set(event_target_value(&ev)); clear_diff(); }>
                                        <option value="unchanged">"Leave as it is"</option>
                                        <option value="inherit">"Inherit (delete the key)"</option>
                                        <option value="required">"Required"</option>
                                        <option value="optional">"Optional"</option>
                                    </select>
                                </label>
                            </Show>
                            <div class="cz-actions">
                                <Show when=move || !form_on.get() && !secret.get() && matches!(doc_kind.get().as_str(), "skill" | "agent")>
                                    <button type="button" class="secondary" on:click=move |_| { copying.set(true); form_on.set(true); scope.set("workspace".into()); base_hash.set("missing".into()); clear_diff(); }>"Copy into this directory"</button>
                                </Show>
                                <Show when=move || form_on.get() || copying.get()>
                                    <button type="button" class="secondary" data-cz-preview disabled=move || busy.get() on:click=move |_| preview()>"Preview diff"</button>
                                    <button type="button" class="primary" data-cz-save disabled=move || busy.get() || diff_hash.get().is_empty() || diff.get().starts_with("(no changes)") on:click=move |_| save()>"Save"</button>
                                </Show>
                                <Show when=move || form_on.get() && !copying.get() && base_hash.get() != "missing" && doc_kind.get() != "roster">
                                    <Show when=move || arm_remove.get() fallback=move || view! {
                                        <button type="button" class="secondary" data-cz-remove disabled=move || busy.get()
                                            on:click=move |_| arm_remove.set(true)>"Remove"</button>
                                    }>
                                        <button type="button" class="primary" data-cz-confirm-remove disabled=move || busy.get()
                                            on:click=move |_| { arm_remove.set(false); remove(); }>"Confirm remove"</button>
                                        <button type="button" class="secondary" data-cz-cancel-remove
                                            on:click=move |_| arm_remove.set(false)>"Cancel"</button>
                                    </Show>
                                </Show>
                            </div>
                            <Show when=move || !warnings.get().is_empty()>
                                <ul class="cz-warn">{move || warnings.get().into_iter().map(|w| view! { <li>{w}</li> }).collect_view()}</ul>
                            </Show>
                            <Show when=move || !diff.get().is_empty()>
                                <pre class="cz-diff" aria-label="Diff">{move || diff.get()}</pre>
                            </Show>
                        </div>
                    </Show>
                </Show>
            </section>
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sections_step_and_labels() {
        assert_eq!(customize_sections().len(), 7);
        assert_eq!(step_section("skills", 1), "agents");
        assert_eq!(step_section("roster", 1), "skills");
        assert_eq!(step_section("skills", -1), "roster");
        assert_eq!(section_label("mcp"), "MCP");
        assert_eq!(origin_label("plugin"), "Plugin");
        assert_eq!(origin_label("nope"), "Other");
    }

    #[test]
    fn list_index_wraps() {
        assert_eq!(step_index(3, 2, 1), 0);
        assert_eq!(step_index(3, 0, -1), 2);
        assert_eq!(step_index(0, 0, 1), 0);
    }

    #[test]
    fn plugin_scope_and_pairs() {
        assert_eq!(plugin_api_scope("workspace"), "project");
        assert_eq!(plugin_api_scope("global"), "global");
        let text = format!("TOKEN={SECRET_KEEP}\nOTHER=plain\n");
        let pairs = parse_pairs(&text).unwrap();
        assert_eq!(pairs.len(), 2);
        assert_eq!(pairs[0].value, SECRET_KEEP);
        assert_eq!(format_pairs(&pairs), format!("TOKEN={SECRET_KEEP}\nOTHER=plain"));
        assert!(parse_pairs("nope").is_err());
    }
}
