//! Command palette and top-bar slot registry.
//!
//! Streams add entries by dropping a file in `src/shell/contrib/` (see
//! `docs/webui-shell.md`). They do not edit this module.

use std::cell::RefCell;
use std::rc::Rc;

use leptos::prelude::AnyView;

use super::rank::command_score;

pub type CommandRun = Rc<dyn Fn(&str) -> Result<(), String>>;

#[derive(Clone, Copy)]
pub struct Argument {
    pub placeholder: &'static str,
    pub required: bool,
}

#[derive(Clone)]
pub struct Command {
    pub id: &'static str,
    pub section: &'static str,
    pub section_order: i32,
    pub label: &'static str,
    pub detail: &'static str,
    pub shortcut: &'static str,
    pub keywords: &'static [&'static str],
    pub argument: Option<Argument>,
    pub order: i32,
    pub run: CommandRun,
}

#[derive(Clone, Copy, PartialEq, Eq)]
pub enum TopRegion {
    /// Sits in the breadcrumb, where the fallback session chip is.
    /// One or more slots here replace that fallback.
    Status,
    /// After the breadcrumb, before the command field.
    Leading,
    /// Right cluster, before the overflow menu.
    Trailing,
}

#[derive(Clone)]
pub struct TopSlot {
    pub id: &'static str,
    pub region: TopRegion,
    pub order: i32,
    pub title: &'static str,
    /// Label used when the slot is inside the overflow menu.
    pub overflow_label: &'static str,
    /// Retained for contributors. The three-zone header keeps non-status
    /// slots in the menu at every width, so this value is not read.
    #[allow(dead_code)]
    pub collapse_below_px: u32,
    pub render: Rc<dyn Fn() -> AnyView>,
}

#[derive(Clone, Copy)]
pub struct Shortcut {
    pub id: &'static str,
    pub keys: &'static str,
    pub group: &'static str,
    pub group_order: i32,
    pub label: &'static str,
    pub order: i32,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PaletteHit {
    pub id: String,
    pub label: String,
    pub detail: String,
    pub shortcut: String,
    pub needs_argument: bool,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PaletteGroup {
    pub section: String,
    pub hits: Vec<PaletteHit>,
}

#[derive(Default)]
struct Registry {
    commands: Vec<Command>,
    slots: Vec<TopSlot>,
    shortcuts: Vec<Shortcut>,
}

impl Registry {
    fn upsert_command(&mut self, cmd: Command) {
        if let Some(index) = self.commands.iter().position(|c| c.id == cmd.id) {
            self.commands[index] = cmd;
        } else {
            self.commands.push(cmd);
        }
    }

    fn upsert_slot(&mut self, slot: TopSlot) {
        if let Some(index) = self.slots.iter().position(|s| s.id == slot.id) {
            self.slots[index] = slot;
        } else {
            self.slots.push(slot);
        }
    }

    fn upsert_shortcut(&mut self, shortcut: Shortcut) {
        if let Some(index) = self.shortcuts.iter().position(|s| s.id == shortcut.id) {
            self.shortcuts[index] = shortcut;
        } else {
            self.shortcuts.push(shortcut);
        }
    }

    fn query(&self, raw: &str, recents: &[String]) -> Vec<PaletteGroup> {
        let query = raw.trim();
        let mut matched: Vec<&Command> = self
            .commands
            .iter()
            .filter(|cmd| {
                command_score(query, cmd.label, cmd.detail, cmd.shortcut, cmd.keywords).is_some()
            })
            .collect();
        if query.is_empty() {
            let recent_ids: Vec<&str> = recents
                .iter()
                .filter(|id| matched.iter().any(|cmd| cmd.id == id.as_str()))
                .take(8)
                .map(|id| id.as_str())
                .collect();
            let mut groups = Vec::new();
            if !recent_ids.is_empty() {
                let hits = recent_ids
                    .iter()
                    .filter_map(|id| self.commands.iter().find(|cmd| cmd.id == *id))
                    .map(hit)
                    .collect();
                groups.push(PaletteGroup {
                    section: "Recent".into(),
                    hits,
                });
            }
            matched.retain(|cmd| !recent_ids.contains(&cmd.id));
            matched.sort_by(|a, b| {
                a.section_order
                    .cmp(&b.section_order)
                    .then(a.order.cmp(&b.order))
                    .then(a.label.cmp(b.label))
            });
            groups.extend(group_contiguous(&matched));
            return groups;
        }
        matched.sort_by(|a, b| {
            let sa = command_score(query, a.label, a.detail, a.shortcut, a.keywords).unwrap_or(0);
            let sb = command_score(query, b.label, b.detail, b.shortcut, b.keywords).unwrap_or(0);
            sb.cmp(&sa)
                .then(a.order.cmp(&b.order))
                .then(a.label.cmp(b.label))
        });
        let mut order = Vec::new();
        for cmd in &matched {
            if !order.iter().any(|section| *section == cmd.section) {
                order.push(cmd.section);
            }
        }
        order
            .into_iter()
            .map(|section| PaletteGroup {
                section: section.to_string(),
                hits: matched
                    .iter()
                    .filter(|cmd| cmd.section == section)
                    .map(|cmd| hit(cmd))
                    .collect(),
            })
            .collect()
    }
}

fn hit(cmd: &Command) -> PaletteHit {
    PaletteHit {
        id: cmd.id.to_string(),
        label: cmd.label.to_string(),
        detail: cmd.detail.to_string(),
        shortcut: cmd.shortcut.to_string(),
        needs_argument: cmd.argument.is_some(),
    }
}

fn group_contiguous(cmds: &[&Command]) -> Vec<PaletteGroup> {
    let mut groups: Vec<PaletteGroup> = Vec::new();
    for cmd in cmds {
        if let Some(last) = groups.last_mut() {
            if last.section == cmd.section {
                last.hits.push(hit(cmd));
                continue;
            }
        }
        groups.push(PaletteGroup {
            section: cmd.section.to_string(),
            hits: vec![hit(cmd)],
        });
    }
    groups
}

/// Mutable handle passed to each `install` function.
pub struct ShellHook {
    registry: Registry,
}

impl ShellHook {
    pub fn new() -> Self {
        Self {
            registry: Registry::default(),
        }
    }

    /// Register a command. The same `id` replaces the previous command,
    /// so a stream can take over a builtin without editing it.
    pub fn command(&mut self, cmd: Command) {
        self.registry.upsert_command(cmd);
    }

    /// Register a top-bar slot. The same `id` replaces the previous slot.
    pub fn slot(&mut self, slot: TopSlot) {
        self.registry.upsert_slot(slot);
    }

    /// Register a row in the shortcut help overlay. The same `id` replaces.
    pub fn shortcut(&mut self, shortcut: Shortcut) {
        self.registry.upsert_shortcut(shortcut);
    }

    #[allow(dead_code)]
    pub fn query(&self, raw: &str, recents: &[String]) -> Vec<PaletteGroup> {
        self.registry.query(raw, recents)
    }

    pub(crate) fn install_global(self) {
        REG.with(|reg| *reg.borrow_mut() = self.registry);
    }
}

thread_local! {
    static REG: RefCell<Registry> = RefCell::new(Registry::default());
}

pub fn palette(raw: &str, recents: &[String]) -> Vec<PaletteGroup> {
    REG.with(|reg| reg.borrow().query(raw, recents))
}

pub fn command(id: &str) -> Option<Command> {
    REG.with(|reg| {
        reg.borrow()
            .commands
            .iter()
            .find(|cmd| cmd.id == id)
            .cloned()
    })
}

pub fn slots(region: TopRegion) -> Vec<TopSlot> {
    REG.with(|reg| {
        let mut found: Vec<TopSlot> = reg
            .borrow()
            .slots
            .iter()
            .filter(|slot| slot.region == region)
            .cloned()
            .collect();
        found.sort_by(|a, b| a.order.cmp(&b.order).then(a.id.cmp(b.id)));
        found
    })
}

pub fn shortcuts() -> Vec<Shortcut> {
    REG.with(|reg| {
        let mut found = reg.borrow().shortcuts.clone();
        found.sort_by(|a, b| {
            a.group_order
                .cmp(&b.group_order)
                .then(a.order.cmp(&b.order))
                .then(a.label.cmp(b.label))
        });
        found
    })
}

/// Width predicate from the old bar. The three-zone header does not
/// consult it; slots other than status and the gateway chip live in
/// the menu at every width.
#[allow(dead_code)]
pub fn slot_inline(slot: &TopSlot, width: u32) -> bool {
    slot.collapse_below_px == 0 || width >= slot.collapse_below_px
}

#[cfg(test)]
mod tests {
    use super::*;
    use leptos::prelude::*;

    fn cmd(
        id: &'static str,
        section: &'static str,
        section_order: i32,
        label: &'static str,
        order: i32,
    ) -> Command {
        Command {
            id,
            section,
            section_order,
            label,
            detail: "detail",
            shortcut: "",
            keywords: &[],
            argument: None,
            order,
            run: Rc::new(|_| Ok(())),
        }
    }

    #[test]
    fn same_id_replaces_command_slot_and_shortcut() {
        let mut hook = ShellHook::new();
        hook.command(cmd("new", "Commands", 0, "New session", 1));
        hook.command(Command {
            label: "New root",
            ..cmd("new", "Commands", 0, "New session", 1)
        });
        hook.slot(TopSlot {
            id: "gateway",
            region: TopRegion::Trailing,
            order: 1,
            title: "Gateway",
            overflow_label: "Gateway",
            collapse_below_px: 0,
            render: Rc::new(|| leptos::prelude::view! { <span>"a"</span> }.into_any()),
        });
        hook.slot(TopSlot {
            id: "gateway",
            region: TopRegion::Trailing,
            order: 2,
            title: "Gateway",
            overflow_label: "Replaced",
            collapse_below_px: 400,
            render: Rc::new(|| leptos::prelude::view! { <span>"b"</span> }.into_any()),
        });
        hook.shortcut(Shortcut {
            id: "palette",
            keys: "Ctrl+K",
            group: "Palette",
            group_order: 1,
            label: "Commands",
            order: 1,
        });
        hook.shortcut(Shortcut {
            id: "palette",
            keys: "Ctrl+K",
            group: "Palette",
            group_order: 1,
            label: "Command palette",
            order: 1,
        });
        let groups = hook.query("", &[]);
        assert_eq!(groups.len(), 1);
        assert_eq!(groups[0].hits[0].label, "New root");
        assert_eq!(hook.registry.slots.len(), 1);
        assert_eq!(hook.registry.slots[0].overflow_label, "Replaced");
        assert_eq!(hook.registry.shortcuts.len(), 1);
        assert_eq!(hook.registry.shortcuts[0].label, "Command palette");
    }

    #[test]
    fn empty_query_lifts_recents_out_of_their_section() {
        let mut hook = ShellHook::new();
        hook.command(cmd("new", "Commands", 0, "New session", 1));
        hook.command(cmd("focus", "Commands", 0, "Focus mode", 2));
        hook.command(cmd("chat", "Apps", 1, "Chat", 1));
        let groups = hook.query("", &["chat".into(), "missing".into()]);
        assert_eq!(groups.len(), 2);
        assert_eq!(groups[0].section, "Recent");
        assert_eq!(groups[0].hits[0].id, "chat");
        assert_eq!(groups[1].section, "Commands");
        let flat: Vec<_> = groups
            .iter()
            .flat_map(|g| g.hits.iter().map(|h| h.id.as_str()))
            .collect();
        assert_eq!(flat.iter().filter(|id| **id == "chat").count(), 1);
    }

    #[test]
    fn fuzzy_query_drops_misses_and_leads_with_the_best_section() {
        let mut hook = ShellHook::new();
        hook.command(cmd("new", "Commands", 0, "New session", 1));
        hook.command(Command {
            detail: "Bundled theme",
            keywords: &["nord"],
            ..cmd("theme-nord", "Theme", 3, "Theme: Nord", 1)
        });
        let groups = hook.query("nord", &[]);
        assert_eq!(groups.len(), 1);
        assert_eq!(groups[0].section, "Theme");
        assert_eq!(groups[0].hits[0].id, "theme-nord");
        let none = hook.query("zzzz-not-a-command", &[]);
        assert!(none.is_empty());
    }

    #[test]
    fn plan_query_leads_with_approval_plan_not_the_task_runner() {
        // Regression for #1429's webui-smoke failure: the task runner
        // registered the exact keyword "plan", which outranked the fuzzy
        // label match for "Approval: Plan" — Enter on a "plan" query
        // opened the Tasks view and every later cockpit assertion failed.
        let mut hook = ShellHook::new();
        crate::shell::builtin::install(&mut hook);
        crate::shell::contrib::install(&mut hook);
        let groups = hook.query("plan", &[]);
        let first = groups
            .first()
            .and_then(|g| g.hits.first())
            .map(|h| h.id.as_str());
        assert_eq!(first, Some("mode-plan"), "groups: {groups:?}");
    }

    #[test]
    fn argument_flag_is_visible_on_the_hit() {
        let mut hook = ShellHook::new();
        hook.command(Command {
            argument: Some(Argument {
                placeholder: "Name",
                required: true,
            }),
            ..cmd("open-session", "Commands", 0, "Open session…", 1)
        });
        let groups = hook.query("open", &[]);
        assert!(groups[0].hits[0].needs_argument);
    }
}
