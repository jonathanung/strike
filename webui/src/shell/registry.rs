//! Command palette and top-bar slot registry.
//!
//! One [`Command`] type is the list the palette searches. Streams add
//! entries from `src/shell/contrib/` (see `docs/webui-shell.md`). They do
//! not edit this module.
//!
//! Groups are fixed: Navigate, Session, Providers, Model, Git, Settings,
//! Customize, Gateway, Theme, Debug. An empty query shows Recent, then
//! Frequent, then those groups. A typed query fuzzy-matches the title
//! (`label`), keywords, shortcut, and detail.

use std::cell::RefCell;
use std::rc::Rc;

use leptos::prelude::AnyView;

use super::rank::{best_score, command_score};

pub type CommandRun = Rc<dyn Fn(&str) -> Result<(), String>>;
pub type Predicate = Rc<dyn Fn() -> bool>;
pub type ChoiceList = Rc<dyn Fn() -> Vec<Choice>>;

/// Palette heading. Order is the empty-query order after Recent and Frequent.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Group {
    Navigate,
    Session,
    Providers,
    Model,
    Git,
    Settings,
    Customize,
    Gateway,
    Theme,
    Debug,
}

impl Group {
    pub fn title(self) -> &'static str {
        match self {
            Self::Navigate => "Navigate",
            Self::Session => "Session",
            Self::Providers => "Providers",
            Self::Model => "Model",
            Self::Git => "Git",
            Self::Settings => "Settings",
            Self::Customize => "Customize",
            Self::Gateway => "Gateway",
            Self::Theme => "Theme",
            Self::Debug => "Debug",
        }
    }

    pub fn order(self) -> i32 {
        match self {
            Self::Navigate => 0,
            Self::Session => 1,
            Self::Providers => 2,
            Self::Model => 3,
            Self::Git => 4,
            Self::Settings => 5,
            Self::Customize => 6,
            Self::Gateway => 7,
            Self::Theme => 8,
            Self::Debug => 9,
        }
    }
}

/// Copy the shared confirm dialog shows before a destructive command runs.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ConfirmSpec {
    pub title: &'static str,
    pub body: &'static str,
    pub confirm_label: &'static str,
}

/// One row in a command's second step (Switch model, Open session, …).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Choice {
    pub id: String,
    pub label: String,
    pub detail: String,
}

/// Recents are most-recent-first. Frequent ids are highest-count-first.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct PaletteUsage {
    pub recents: Vec<String>,
    pub frequent: Vec<String>,
}

#[derive(Clone, Copy)]
pub struct Argument {
    pub placeholder: &'static str,
    pub required: bool,
}

#[derive(Clone)]
pub struct Command {
    pub id: &'static str,
    pub group: Group,
    /// Row title. Fuzzy search weights this above keywords.
    pub label: &'static str,
    pub detail: &'static str,
    pub shortcut: &'static str,
    pub keywords: &'static [&'static str],
    pub argument: Option<Argument>,
    pub order: i32,
    pub run: CommandRun,
    /// `None` means the command is listed. `Some` hides it when false.
    pub visible: Option<Predicate>,
    /// `None` means the command can run. `Some` keeps the row and blocks it when false.
    pub enabled: Option<Predicate>,
    /// When set, the palette asks with the shared confirm dialog before `run`.
    pub confirm: Option<ConfirmSpec>,
    /// Second-step rows. Selecting one calls `run` with that choice id.
    pub choices: Option<ChoiceList>,
}

impl Command {
    pub fn new(
        id: &'static str,
        group: Group,
        label: &'static str,
        detail: &'static str,
        run: CommandRun,
    ) -> Self {
        Self {
            id,
            group,
            label,
            detail,
            shortcut: "",
            keywords: &[],
            argument: None,
            order: 0,
            run,
            visible: None,
            enabled: None,
            confirm: None,
            choices: None,
        }
    }

    pub fn shortcut(mut self, shortcut: &'static str) -> Self {
        self.shortcut = shortcut;
        self
    }

    pub fn keywords(mut self, keywords: &'static [&'static str]) -> Self {
        self.keywords = keywords;
        self
    }

    pub fn order(mut self, order: i32) -> Self {
        self.order = order;
        self
    }

    pub fn argument(mut self, argument: Argument) -> Self {
        self.argument = Some(argument);
        self
    }

    pub fn visible(mut self, pred: impl Fn() -> bool + 'static) -> Self {
        self.visible = Some(Rc::new(pred));
        self
    }

    pub fn enabled(mut self, pred: impl Fn() -> bool + 'static) -> Self {
        self.enabled = Some(Rc::new(pred));
        self
    }

    pub fn confirm(mut self, spec: ConfirmSpec) -> Self {
        self.confirm = Some(spec);
        self
    }

    pub fn choices(mut self, list: impl Fn() -> Vec<Choice> + 'static) -> Self {
        self.choices = Some(Rc::new(list));
        self
    }

    pub fn is_visible(&self) -> bool {
        self.visible.as_ref().map(|pred| pred()).unwrap_or(true)
    }

    pub fn is_enabled(&self) -> bool {
        self.enabled.as_ref().map(|pred| pred()).unwrap_or(true)
    }

    pub fn needs_prompt(&self) -> bool {
        self.argument.is_some() || self.choices.is_some()
    }
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
    pub enabled: bool,
    /// Choice id when this row is a promoted second-step choice. Empty for
    /// a normal command. Enter runs the command with this argument.
    pub args: String,
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

    fn visible(&self) -> Vec<&Command> {
        self.commands
            .iter()
            .filter(|cmd| cmd.is_visible())
            .collect()
    }

    fn query(&self, raw: &str, usage: &PaletteUsage) -> Vec<PaletteGroup> {
        let query = raw.trim();
        let mut matched: Vec<&Command> = self
            .visible()
            .into_iter()
            .filter(|cmd| {
                command_score(query, cmd.label, cmd.detail, cmd.shortcut, cmd.keywords).is_some()
            })
            .collect();
        if query.is_empty() {
            let recent_ids: Vec<&str> = usage
                .recents
                .iter()
                .filter(|id| matched.iter().any(|cmd| cmd.id == id.as_str()))
                .take(5)
                .map(|id| id.as_str())
                .collect();
            let frequent_ids: Vec<&str> = usage
                .frequent
                .iter()
                .filter(|id| {
                    !recent_ids.contains(&id.as_str())
                        && matched.iter().any(|cmd| cmd.id == id.as_str())
                })
                .take(5)
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
            if !frequent_ids.is_empty() {
                let hits = frequent_ids
                    .iter()
                    .filter_map(|id| self.commands.iter().find(|cmd| cmd.id == *id))
                    .map(hit)
                    .collect();
                groups.push(PaletteGroup {
                    section: "Frequent".into(),
                    hits,
                });
            }
            let pinned: Vec<&str> = recent_ids.into_iter().chain(frequent_ids).collect();
            matched.retain(|cmd| !pinned.contains(&cmd.id));
            matched.sort_by(|a, b| {
                a.group
                    .order()
                    .cmp(&b.group.order())
                    .then(a.order.cmp(&b.order))
                    .then(a.label.cmp(b.label))
            });
            groups.extend(group_contiguous(&matched));
            return groups;
        }
        let mut ranked: Vec<Ranked> = matched
            .into_iter()
            .map(|cmd| Ranked {
                score: command_score(query, cmd.label, cmd.detail, cmd.shortcut, cmd.keywords)
                    .unwrap_or(0),
                order: cmd.order,
                label: cmd.label.to_string(),
                section: cmd.group.title(),
                hit: hit(cmd),
            })
            .collect();
        // A typed query also surfaces matching choices (Switch model: echo/…,
        // Remove stored key: openai) so a known name runs without a second
        // step. An empty query does not list them.
        let mut choice_total = 0;
        for cmd in self.visible() {
            if choice_total >= 12 {
                break;
            }
            let Some(load) = cmd.choices.as_ref() else {
                continue;
            };
            let mut took = 0;
            for choice in filter_choices(query, &load()) {
                if took >= 4 || choice_total >= 12 {
                    break;
                }
                let score = super::rank::best_score(
                    query,
                    &[
                        (choice.label.as_str(), 3),
                        (choice.id.as_str(), 2),
                        (choice.detail.as_str(), 1),
                    ],
                )
                .unwrap_or(0);
                ranked.push(Ranked {
                    score,
                    order: cmd.order,
                    label: choice.label.clone(),
                    section: cmd.group.title(),
                    hit: PaletteHit {
                        id: cmd.id.to_string(),
                        label: choice.label,
                        detail: choice.detail,
                        shortcut: String::new(),
                        needs_argument: false,
                        enabled: cmd.is_enabled(),
                        args: choice.id,
                    },
                });
                took += 1;
                choice_total += 1;
            }
        }
        ranked.sort_by(|a, b| {
            b.score
                .cmp(&a.score)
                .then(a.order.cmp(&b.order))
                .then(a.label.cmp(&b.label))
        });
        let mut groups: Vec<PaletteGroup> = Vec::new();
        for row in ranked {
            if let Some(last) = groups.last_mut() {
                if last.section == row.section {
                    last.hits.push(row.hit);
                    continue;
                }
            }
            if let Some(existing) = groups.iter_mut().find(|group| group.section == row.section) {
                existing.hits.push(row.hit);
                continue;
            }
            groups.push(PaletteGroup {
                section: row.section.to_string(),
                hits: vec![row.hit],
            });
        }
        groups
    }
}

struct Ranked {
    score: i32,
    order: i32,
    label: String,
    section: &'static str,
    hit: PaletteHit,
}

fn hit(cmd: &Command) -> PaletteHit {
    PaletteHit {
        id: cmd.id.to_string(),
        label: cmd.label.to_string(),
        detail: cmd.detail.to_string(),
        shortcut: cmd.shortcut.to_string(),
        needs_argument: cmd.needs_prompt(),
        enabled: cmd.is_enabled(),
        args: String::new(),
    }
}

fn group_contiguous(cmds: &[&Command]) -> Vec<PaletteGroup> {
    let mut groups: Vec<PaletteGroup> = Vec::new();
    for cmd in cmds {
        let section = cmd.group.title();
        if let Some(last) = groups.last_mut() {
            if last.section == section {
                last.hits.push(hit(cmd));
                continue;
            }
        }
        groups.push(PaletteGroup {
            section: section.to_string(),
            hits: vec![hit(cmd)],
        });
    }
    groups
}

/// Filter a second-step list. An empty query keeps the source order.
pub fn filter_choices(query: &str, choices: &[Choice]) -> Vec<Choice> {
    let query = query.trim();
    if query.is_empty() {
        return choices.to_vec();
    }
    let floor = (query.chars().count() as i32).saturating_mul(8);
    let mut scored: Vec<(i32, &Choice)> = choices
        .iter()
        .filter_map(|choice| {
            let score = best_score(
                query,
                &[
                    (choice.label.as_str(), 3),
                    (choice.detail.as_str(), 1),
                    (choice.id.as_str(), 2),
                ],
            )?;
            if score >= floor {
                Some((score, choice))
            } else {
                None
            }
        })
        .collect();
    scored.sort_by(|a, b| b.0.cmp(&a.0).then(a.1.label.cmp(&b.1.label)));
    scored
        .into_iter()
        .map(|(_, choice)| choice.clone())
        .collect()
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
    pub fn query(&self, raw: &str, usage: &PaletteUsage) -> Vec<PaletteGroup> {
        self.registry.query(raw, usage)
    }

    pub(crate) fn install_global(self) {
        REG.with(|reg| *reg.borrow_mut() = self.registry);
    }
}

thread_local! {
    static REG: RefCell<Registry> = RefCell::new(Registry::default());
}

pub fn palette(raw: &str, usage: &PaletteUsage) -> Vec<PaletteGroup> {
    REG.with(|reg| reg.borrow().query(raw, usage))
}

/// Drop every command whose id starts with `prefix`, then insert `cmds`.
/// Gateway names are not known at boot, so the switcher refreshes its rows.
pub fn replace_prefixed(prefix: &str, cmds: Vec<Command>) {
    REG.with(|reg| {
        let mut reg = reg.borrow_mut();
        reg.commands.retain(|cmd| !cmd.id.starts_with(prefix));
        for cmd in cmds {
            reg.upsert_command(cmd);
        }
    });
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

    fn cmd(id: &'static str, group: Group, label: &'static str, order: i32) -> Command {
        Command::new(id, group, label, "detail", Rc::new(|_| Ok(()))).order(order)
    }

    fn usage(recents: &[&str], frequent: &[&str]) -> PaletteUsage {
        PaletteUsage {
            recents: recents.iter().map(|id| (*id).to_string()).collect(),
            frequent: frequent.iter().map(|id| (*id).to_string()).collect(),
        }
    }

    #[test]
    fn same_id_replaces_command_slot_and_shortcut() {
        let mut hook = ShellHook::new();
        hook.command(cmd("new", Group::Session, "New session", 1));
        hook.command(Command {
            label: "New root",
            ..cmd("new", Group::Session, "New session", 1)
        });
        hook.slot(TopSlot {
            id: "gateway",
            region: TopRegion::Trailing,
            order: 1,
            title: "Gateway",
            overflow_label: "Gateway",
            collapse_below_px: 0,
            render: Rc::new(|| view! { <span>"a"</span> }.into_any()),
        });
        hook.slot(TopSlot {
            id: "gateway",
            region: TopRegion::Trailing,
            order: 2,
            title: "Gateway",
            overflow_label: "Replaced",
            collapse_below_px: 400,
            render: Rc::new(|| view! { <span>"b"</span> }.into_any()),
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
        let groups = hook.query("", &PaletteUsage::default());
        assert_eq!(groups.len(), 1);
        assert_eq!(groups[0].hits[0].label, "New root");
        assert_eq!(hook.registry.slots.len(), 1);
        assert_eq!(hook.registry.slots[0].overflow_label, "Replaced");
        assert_eq!(hook.registry.shortcuts.len(), 1);
        assert_eq!(hook.registry.shortcuts[0].label, "Command palette");
    }

    #[test]
    fn empty_query_lifts_recents_and_frequent_out_of_their_group() {
        let mut hook = ShellHook::new();
        hook.command(cmd("new", Group::Session, "New session", 1));
        hook.command(cmd("focus", Group::Navigate, "Focus mode", 2));
        hook.command(cmd("chat", Group::Navigate, "Chat", 1));
        hook.command(cmd("git", Group::Git, "Git", 1));
        let groups = hook.query("", &usage(&["chat", "missing"], &["git", "chat"]));
        assert_eq!(groups[0].section, "Recent");
        assert_eq!(groups[0].hits[0].id, "chat");
        assert_eq!(groups[1].section, "Frequent");
        assert_eq!(groups[1].hits[0].id, "git");
        let flat: Vec<_> = groups
            .iter()
            .flat_map(|g| g.hits.iter().map(|h| h.id.as_str()))
            .collect();
        assert_eq!(flat.iter().filter(|id| **id == "chat").count(), 1);
        assert_eq!(flat.iter().filter(|id| **id == "git").count(), 1);
        assert_eq!(groups.last().unwrap().section, "Session");
    }

    #[test]
    fn hidden_commands_drop_out_and_disabled_commands_stay() {
        let mut hook = ShellHook::new();
        hook.command(cmd("shown", Group::Navigate, "Shown", 1));
        hook.command(cmd("secret", Group::Debug, "Secret", 1).visible(|| false));
        hook.command(cmd("locked", Group::Settings, "Locked", 1).enabled(|| false));
        let groups = hook.query("", &PaletteUsage::default());
        let flat: Vec<_> = groups
            .iter()
            .flat_map(|g| g.hits.iter().map(|h| (h.id.as_str(), h.enabled)))
            .collect();
        assert!(flat.iter().all(|(id, _)| *id != "secret"));
        assert!(flat.contains(&("locked", false)));
        assert!(flat.contains(&("shown", true)));
        assert!(hook.query("secret", &PaletteUsage::default()).is_empty());
    }

    #[test]
    fn fuzzy_query_drops_misses_and_leads_with_the_best_group() {
        let mut hook = ShellHook::new();
        hook.command(cmd("new", Group::Session, "New session", 1));
        hook.command(
            cmd("theme-nord", Group::Theme, "Theme: Nord", 1)
                .keywords(&["nord"])
                .order(1),
        );
        let nord = hook
            .registry
            .commands
            .iter_mut()
            .find(|c| c.id == "theme-nord")
            .unwrap();
        nord.detail = "Bundled theme";
        let groups = hook.query("nord", &PaletteUsage::default());
        assert_eq!(groups.len(), 1);
        assert_eq!(groups[0].section, "Theme");
        assert_eq!(groups[0].hits[0].id, "theme-nord");
        let none = hook.query("zzzz-not-a-command", &PaletteUsage::default());
        assert!(none.is_empty());
    }

    #[test]
    fn plan_query_leads_with_approval_plan_not_the_task_runner() {
        let mut hook = ShellHook::new();
        crate::shell::builtin::install(&mut hook);
        crate::shell::contrib::install(&mut hook);
        let groups = hook.query("plan", &PaletteUsage::default());
        let first = groups
            .first()
            .and_then(|g| g.hits.first())
            .map(|h| h.id.as_str());
        assert_eq!(first, Some("mode-plan"), "groups: {groups:?}");
    }

    #[test]
    fn argument_and_choices_flag_the_hit() {
        let mut hook = ShellHook::new();
        hook.command(
            cmd("open-session", Group::Session, "Open session…", 1).argument(Argument {
                placeholder: "Name",
                required: true,
            }),
        );
        hook.command(
            cmd("switch-model", Group::Model, "Switch model…", 1).choices(|| {
                vec![Choice {
                    id: "echo\thaiku".into(),
                    label: "Haiku".into(),
                    detail: "echo".into(),
                }]
            }),
        );
        let groups = hook.query("open", &PaletteUsage::default());
        assert!(groups[0].hits[0].needs_argument);
        let models = hook.query("model", &PaletteUsage::default());
        assert!(models[0].hits[0].needs_argument);
    }

    #[test]
    fn choice_filter_ranks_the_title_and_drops_misses() {
        let choices = vec![
            Choice {
                id: "a".into(),
                label: "Alpine".into(),
                detail: "workspace".into(),
            },
            Choice {
                id: "b".into(),
                label: "Beta".into(),
                detail: "session".into(),
            },
        ];
        assert_eq!(filter_choices("", &choices).len(), 2);
        let hit = filter_choices("alp", &choices);
        assert_eq!(hit.len(), 1);
        assert_eq!(hit[0].id, "a");
        assert!(filter_choices("zzzz", &choices).is_empty());
    }

    #[test]
    fn typed_query_promotes_a_matching_choice() {
        let mut hook = ShellHook::new();
        hook.command(
            cmd("switch-model", Group::Model, "Switch model…", 1)
                .keywords(&["model", "switch"])
                .argument(Argument {
                    placeholder: "Filter models",
                    required: true,
                })
                .choices(|| {
                    vec![Choice {
                        id: "echo\thaiku".into(),
                        label: "Switch model: echo/haiku".into(),
                        detail: "echo".into(),
                    }]
                }),
        );
        let groups = hook.query("haiku", &PaletteUsage::default());
        let hit = groups
            .iter()
            .flat_map(|group| group.hits.iter())
            .find(|hit| hit.args == "echo\thaiku")
            .expect("choice row");
        assert_eq!(hit.id, "switch-model");
        assert!(!hit.needs_argument);
        let empty = hook.query("", &PaletteUsage::default());
        assert!(empty
            .iter()
            .flat_map(|group| group.hits.iter())
            .all(|hit| hit.args.is_empty()));
    }

    #[test]
    fn palette_only_views_keep_one_command_and_drop_the_menu_slot() {
        let mut hook = ShellHook::new();
        crate::shell::builtin::install(&mut hook);
        crate::shell::contrib::install(&mut hook);
        for id in [
            "schedule",
            "triggers",
            "apps",
            "tasks",
            "goals",
            "usage",
            "audit",
            "panel-terminal",
            "providers",
            "settings",
        ] {
            let n = hook
                .registry
                .commands
                .iter()
                .filter(|cmd| cmd.id == id)
                .count();
            assert_eq!(n, 1, "{id}");
        }
        for id in [
            "schedule",
            "triggers",
            "apps",
            "task-runner",
            "goals",
            "usage-audit",
        ] {
            assert!(
                hook.registry.slots.iter().all(|slot| slot.id != id),
                "{id} still has a TopSlot"
            );
        }
        assert!(hook.registry.slots.iter().any(|slot| slot.id == "settings"));
    }

    #[test]
    fn confirm_spec_round_trips_on_the_command() {
        let cmd = cmd("rotate", Group::Gateway, "Rotate gateway token", 1).confirm(ConfirmSpec {
            title: "Rotate gateway token?",
            body: "The current token stops working.",
            confirm_label: "Rotate token",
        });
        assert_eq!(cmd.confirm.unwrap().confirm_label, "Rotate token");
    }
}
