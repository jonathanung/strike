//! Session status (#1263): the session-status slot in the breadcrumb and
//! the gateway chip in the trailing cluster, following
//! docs/webui-shell.md. The chips themselves live in `crate::status`
//! (`StatusMark` with `StatusSlot::Chip`); this file only registers them
//! with the shell. Registering `session-status` replaces the shell's
//! fallback session chip, and registering `gateway` replaces the builtin
//! gateway slot with the status aggregate chip (idle paints success,
//! a missed heartbeat paints disconnected / "Unreachable").

use std::rc::Rc;

use crate::shell::{ShellHook, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.slot(TopSlot {
        id: "session-status",
        region: TopRegion::Status,
        order: 10,
        title: "Session status",
        overflow_label: "Session status",
        // 0: the status chip stays in the bar at every width.
        collapse_below_px: 0,
        render: Rc::new(|| crate::status::session_view()),
    });
    reg.slot(TopSlot {
        id: "gateway",
        region: TopRegion::Trailing,
        order: 80,
        title: "Gateway",
        overflow_label: "Gateway",
        collapse_below_px: 720,
        render: Rc::new(|| crate::status::gateway_view()),
    });
}
