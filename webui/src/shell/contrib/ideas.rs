//! Ideas inbox (#1323): palette entry for the page, and the header slot
//! for its open-count badge (#1926). The page and the badge live in
//! `crate::ideas`.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "ideas",
            Group::Navigate,
            "Ideas",
            "Suggestions for new work you can accept, snooze or dismiss",
            Rc::new(|_| crate::shell::host::set_view("ideas")),
        )
        .keywords(&["suggestions", "inbox", "feed", "proactive"])
        .order(32),
    );
    reg.slot(TopSlot {
        id: "ideas",
        region: TopRegion::Trailing,
        order: 35,
        title: "Ideas",
        overflow_label: "Ideas",
        // 0: the badge stays in the bar at every width (#1926).
        collapse_below_px: 0,
        render: Rc::new(crate::ideas::header_badge),
    });
}
