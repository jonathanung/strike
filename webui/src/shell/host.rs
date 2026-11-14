//! Callbacks the shell needs from the page, plus a late-bound action map.
//!
//! Palette commands installed at boot cannot close over signals that do not
//! exist yet. Builtins call these functions, which read the bridge the page
//! installs from `App`. Other streams bind an id at runtime with [`bind`]
//! and invoke it from a command via [`dispatch`].

use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Rc;

use super::registry::{Choice, CommandRun};

thread_local! {
    static MODEL_CHOICES: RefCell<Vec<Choice>> = RefCell::new(Vec::new());
    static EFFORT_CHOICES: RefCell<Vec<Choice>> = RefCell::new(Vec::new());
    static PICK_MODEL: RefCell<Option<Rc<dyn Fn(&str, &str)>>> = RefCell::new(None);
    static SET_EFFORT: RefCell<Option<Rc<dyn Fn(&str)>>> = RefCell::new(None);
    static SET_FAST: RefCell<Option<Rc<dyn Fn(bool)>>> = RefCell::new(None);
    static SAVE_EFFORT: RefCell<Option<Rc<dyn Fn()>>> = RefCell::new(None);
    static SET_DEFAULT_MODEL: RefCell<Option<Rc<dyn Fn(&str, &str)>>> = RefCell::new(None);
}

pub fn set_model_choices(rows: Vec<Choice>) {
    MODEL_CHOICES.with(|choices| *choices.borrow_mut() = rows);
}

pub fn model_choices() -> Vec<Choice> {
    MODEL_CHOICES.with(|choices| choices.borrow().clone())
}

pub fn set_model_picker(pick: Rc<dyn Fn(&str, &str)>) {
    PICK_MODEL.with(|slot| *slot.borrow_mut() = Some(pick));
}

/// `encoded` is `provider` + tab + model id, the choice id the palette stores.
pub fn pick_model(encoded: &str) -> Result<(), String> {
    let (provider, id) = encoded.split_once('\t').unwrap_or(("", encoded));
    if id.is_empty() {
        return Err("Pick a model.".into());
    }
    let pick = PICK_MODEL
        .with(|slot| slot.borrow().clone())
        .ok_or_else(|| "Model picker is not ready.".to_string())?;
    pick(provider, id);
    Ok(())
}

pub fn set_effort_choices(rows: Vec<Choice>) {
    EFFORT_CHOICES.with(|choices| *choices.borrow_mut() = rows);
}

pub fn effort_choices() -> Vec<Choice> {
    EFFORT_CHOICES.with(|choices| choices.borrow().clone())
}

pub fn set_effort_setter(set: Rc<dyn Fn(&str)>) {
    SET_EFFORT.with(|slot| *slot.borrow_mut() = Some(set));
}

pub fn set_effort(level: &str) -> Result<(), String> {
    let level = level.trim();
    if level.is_empty() {
        return Err("Pick an effort.".into());
    }
    let set = SET_EFFORT
        .with(|slot| slot.borrow().clone())
        .ok_or_else(|| "Effort control is not ready.".to_string())?;
    set(level);
    Ok(())
}

pub fn set_fast_setter(set: Rc<dyn Fn(bool)>) {
    SET_FAST.with(|slot| *slot.borrow_mut() = Some(set));
}

/// The palette's Fast choices are the strings "on" / "off".
pub fn set_fast(value: &str) -> Result<(), String> {
    let enabled = match value.trim() {
        "on" | "true" => true,
        "off" | "false" => false,
        _ => return Err("Pick on or off.".into()),
    };
    let set = SET_FAST
        .with(|slot| slot.borrow().clone())
        .ok_or_else(|| "Fast control is not ready.".to_string())?;
    set(enabled);
    Ok(())
}

pub fn set_effort_default_setter(save: Rc<dyn Fn()>) {
    SAVE_EFFORT.with(|slot| *slot.borrow_mut() = Some(save));
}

pub fn save_effort_default() -> Result<(), String> {
    let save = SAVE_EFFORT
        .with(|slot| slot.borrow().clone())
        .ok_or_else(|| "Effort control is not ready.".to_string())?;
    save();
    Ok(())
}

pub fn set_default_model_picker(pick: Rc<dyn Fn(&str, &str)>) {
    SET_DEFAULT_MODEL.with(|slot| *slot.borrow_mut() = Some(pick));
}

/// `encoded` is `provider` + tab + model id, same as [`pick_model`].
pub fn set_default_model(encoded: &str) -> Result<(), String> {
    let (provider, id) = encoded.split_once('\t').unwrap_or(("", encoded));
    if id.is_empty() {
        return Err("Pick a model.".into());
    }
    let pick = SET_DEFAULT_MODEL
        .with(|slot| slot.borrow().clone())
        .ok_or_else(|| "Model picker is not ready.".to_string())?;
    pick(provider, id);
    Ok(())
}

pub struct HostBridge {
    pub new_session: Rc<dyn Fn()>,
    pub stop_all: Rc<dyn Fn()>,
    pub set_view: Rc<dyn Fn(&str)>,
    pub view: Rc<dyn Fn() -> String>,
    pub set_enter: Rc<dyn Fn(&str)>,
    pub set_perm: Rc<dyn Fn(&str)>,
    pub set_autonomy: Rc<dyn Fn(&str)>,
    pub set_theme: Rc<dyn Fn(&str)>,
    pub set_scheme: Rc<dyn Fn(&str)>,
    pub travel: Rc<dyn Fn(bool)>,
    pub can_back: Rc<dyn Fn() -> bool>,
    pub can_forward: Rc<dyn Fn() -> bool>,
    pub toggle_focus: Rc<dyn Fn()>,
    pub focus_on: Rc<dyn Fn() -> bool>,
    pub workspace_name: Rc<dyn Fn() -> String>,
    pub session_name: Rc<dyn Fn() -> String>,
    /// `idle` or `running` from the live session, or a later status id
    /// (`thinking`, `waiting`, `blocked`, `failed`, `done`, `stopped`,
    /// `disconnected`, `checking`) if the page already knows it.
    pub session_kind: Rc<dyn Fn() -> String>,
    pub gateway: Rc<dyn Fn() -> String>,
    pub version_label: Rc<dyn Fn() -> String>,
    pub dismiss_popups: Rc<dyn Fn()>,
    pub open_side_chat: Rc<dyn Fn()>,
    pub toggle_side_chat: Rc<dyn Fn()>,
    pub sessions: Rc<dyn Fn() -> Vec<(String, String)>>,
    pub open_session: Rc<dyn Fn(&str)>,
    pub workspaces: Rc<dyn Fn() -> Vec<(String, String)>>,
    pub activate_workspace: Rc<dyn Fn(&str)>,
}

thread_local! {
    static HOST: RefCell<Option<HostBridge>> = const { RefCell::new(None) };
    static ACTIONS: RefCell<HashMap<String, CommandRun>> = RefCell::new(HashMap::new());
}

pub fn set_host(bridge: HostBridge) {
    HOST.with(|host| *host.borrow_mut() = Some(bridge));
}

macro_rules! host_rc {
    ($field:ident) => {
        HOST.with(|host| {
            host.borrow()
                .as_ref()
                .map(|bridge| bridge.$field.clone())
                .ok_or_else(|| "Shell host is not ready.".to_string())
        })?
    };
}

pub fn new_session() -> Result<(), String> {
    let run = host_rc!(new_session);
    (*run)();
    Ok(())
}

pub fn stop_all() -> Result<(), String> {
    let run = host_rc!(stop_all);
    (*run)();
    Ok(())
}

pub fn set_view(mode: &str) -> Result<(), String> {
    let run = host_rc!(set_view);
    (*run)(mode);
    Ok(())
}

pub fn view() -> String {
    HOST.with(|host| {
        host.borrow()
            .as_ref()
            .map(|bridge| (bridge.view)())
            .unwrap_or_default()
    })
}

pub fn set_enter(mode: &str) -> Result<(), String> {
    let run = host_rc!(set_enter);
    (*run)(mode);
    Ok(())
}

pub fn set_perm(mode: &str) -> Result<(), String> {
    let run = host_rc!(set_perm);
    (*run)(mode);
    Ok(())
}

pub fn set_autonomy(mode: &str) -> Result<(), String> {
    let run = host_rc!(set_autonomy);
    (*run)(mode);
    Ok(())
}

pub fn set_theme(theme: &str) -> Result<(), String> {
    let run = host_rc!(set_theme);
    (*run)(theme);
    Ok(())
}

pub fn set_scheme(scheme: &str) -> Result<(), String> {
    let run = host_rc!(set_scheme);
    (*run)(scheme);
    Ok(())
}

pub fn travel(back: bool) -> Result<(), String> {
    let run = host_rc!(travel);
    (*run)(back);
    Ok(())
}

pub fn can_back() -> bool {
    HOST.with(|host| {
        host.borrow()
            .as_ref()
            .map(|bridge| (bridge.can_back)())
            .unwrap_or(false)
    })
}

pub fn can_forward() -> bool {
    HOST.with(|host| {
        host.borrow()
            .as_ref()
            .map(|bridge| (bridge.can_forward)())
            .unwrap_or(false)
    })
}

pub fn toggle_focus() -> Result<(), String> {
    let run = host_rc!(toggle_focus);
    (*run)();
    Ok(())
}

pub fn focus_on() -> bool {
    HOST.with(|host| {
        host.borrow()
            .as_ref()
            .map(|bridge| (bridge.focus_on)())
            .unwrap_or(false)
    })
}

pub fn workspace_name() -> String {
    HOST.with(|host| {
        host.borrow()
            .as_ref()
            .map(|bridge| (bridge.workspace_name)())
            .unwrap_or_else(|| "No workspace".into())
    })
}

pub fn session_name() -> String {
    HOST.with(|host| {
        host.borrow()
            .as_ref()
            .map(|bridge| (bridge.session_name)())
            .unwrap_or_else(|| "No session".into())
    })
}

pub fn session_kind() -> String {
    HOST.with(|host| {
        host.borrow()
            .as_ref()
            .map(|bridge| (bridge.session_kind)())
            .unwrap_or_else(|| "idle".into())
    })
}

pub fn gateway() -> String {
    HOST.with(|host| {
        host.borrow()
            .as_ref()
            .map(|bridge| (bridge.gateway)())
            .unwrap_or_else(|| "checking".into())
    })
}

pub fn version_label() -> String {
    HOST.with(|host| {
        host.borrow()
            .as_ref()
            .map(|bridge| (bridge.version_label)())
            .unwrap_or_default()
    })
}

pub fn dismiss_popups() {
    HOST.with(|host| {
        if let Some(bridge) = host.borrow().as_ref() {
            (bridge.dismiss_popups)();
        }
    });
}

pub fn open_side_chat() -> Result<(), String> {
    let run = host_rc!(open_side_chat);
    (*run)();
    Ok(())
}

pub fn toggle_side_chat() -> Result<(), String> {
    let run = host_rc!(toggle_side_chat);
    (*run)();
    Ok(())
}

pub fn sessions() -> Result<Vec<(String, String)>, String> {
    let run = host_rc!(sessions);
    Ok((*run)())
}

pub fn open_session(id: &str) -> Result<(), String> {
    let run = host_rc!(open_session);
    (*run)(id);
    Ok(())
}

pub fn workspaces() -> Result<Vec<(String, String)>, String> {
    let run = host_rc!(workspaces);
    Ok((*run)())
}

pub fn activate_workspace(id: &str) -> Result<(), String> {
    let run = host_rc!(activate_workspace);
    (*run)(id);
    Ok(())
}

thread_local! {
    static OPEN_HELP: RefCell<Option<Rc<dyn Fn()>>> = const { RefCell::new(None) };
}

pub fn set_open_help(open: Rc<dyn Fn()>) {
    OPEN_HELP.with(|slot| *slot.borrow_mut() = Some(open));
}

thread_local! {
    static OPEN_PALETTE: RefCell<Option<Rc<dyn Fn()>>> = const { RefCell::new(None) };
}

pub fn set_show_palette(open: Rc<dyn Fn()>) {
    OPEN_PALETTE.with(|slot| *slot.borrow_mut() = Some(open));
}

/// Open the command palette. Chrome installs the performer at mount.
pub fn show_palette() {
    OPEN_PALETTE.with(|slot| {
        if let Some(open) = slot.borrow().clone() {
            open();
        }
    });
}

pub fn open_help() {
    OPEN_HELP.with(|slot| {
        if let Some(open) = slot.borrow().clone() {
            open();
        }
    });
}

/// Attach a performer to an id. Last bind wins. Call from a mounted
/// component; the command registered at boot can [`dispatch`] this id.
/// Streams call this. The navbar crate does not.
#[allow(dead_code)]
pub fn bind(id: &str, run: CommandRun) {
    ACTIONS.with(|actions| {
        actions.borrow_mut().insert(id.to_string(), run);
    });
}

#[allow(dead_code)]
pub fn unbind(id: &str) {
    ACTIONS.with(|actions| {
        actions.borrow_mut().remove(id);
    });
}

#[allow(dead_code)]
pub fn dispatch(id: &str, args: &str) -> Result<(), String> {
    let run = ACTIONS.with(|actions| actions.borrow().get(id).cloned());
    match run {
        Some(run) => run(args),
        None => Err(format!("Nothing is listening for {id}.")),
    }
}

/// Sentence for the fallback session chip. Keeps the existing badge
/// vocabulary (`Running` is the live `busy` bit). Streams that own a
/// richer status replace the chip instead of extending this table.
pub fn status_label(kind: &str) -> &'static str {
    match kind {
        "running" => "Running",
        "thinking" => "Thinking",
        "waiting" => "Waiting",
        "blocked" => "Blocked",
        "failed" => "Failed",
        "done" => "Done",
        "stopped" => "Stopped",
        "disconnected" => "Disconnected",
        "checking" => "Checking",
        _ => "Idle",
    }
}

pub fn status_badge_class(kind: &str) -> String {
    let painted = match kind {
        "thinking" => "running",
        "waiting" => "blocked",
        "disconnected" => "failed",
        other => other,
    };
    format!("badge badge-{painted}")
}
