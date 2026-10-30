//! Session list and transcript controls that left the reading surface
//! (#1591). Each one dispatches to a binding the page installs while the
//! list or the transcript is mounted. The cursor row (falling back to the
//! open session) and the cursor message (falling back to the last turn)
//! are the targets — the palette does not grow a second picker for them.

use std::rc::Rc;

use crate::shell::{Argument, Choice, Command, Group, ShellHook};

fn go(id: &'static str) -> Rc<dyn Fn(&str) -> Result<(), String>> {
    Rc::new(move |args| crate::shell::dispatch(id, args))
}

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "session.conductor",
            Group::Session,
            "Toggle conductor view",
            "Nest delegated children under their parent",
            go("session.conductor"),
        )
        .keywords(&["conductor", "children", "nest", "delegated"])
        .order(40),
    );
    reg.command(
        Command::new(
            "session.filter.progress",
            Group::Session,
            "Filter sessions: In progress",
            "Show sessions with a live turn",
            go("session.filter.progress"),
        )
        .keywords(&["filter", "running", "busy", "live"])
        .order(41),
    );
    reg.command(
        Command::new(
            "session.filter.pinned",
            Group::Session,
            "Filter sessions: Pinned",
            "Show pinned sessions",
            go("session.filter.pinned"),
        )
        .keywords(&["filter", "pinned", "starred"])
        .order(42),
    );
    reg.command(
        Command::new(
            "session.filter.archived",
            Group::Session,
            "Filter sessions: Archived",
            "Show archived sessions",
            go("session.filter.archived"),
        )
        .keywords(&["filter", "archived", "old"])
        .order(43),
    );
    reg.command(
        Command::new(
            "session.fold",
            Group::Session,
            "Set older-session fold…",
            "Hide sessions older than a threshold",
            go("session.fold"),
        )
        .keywords(&["fold", "older", "threshold", "hide", "age"])
        .order(44)
        .argument(Argument {
            placeholder: "Filter thresholds",
            required: false,
        })
        .choices(|| {
            [
                ("1", "1 day"),
                ("7", "7 days"),
                ("14", "14 days"),
                ("30", "30 days"),
                ("0", "Never"),
            ]
            .into_iter()
            .map(|(id, label)| Choice {
                id: id.into(),
                label: label.into(),
                detail: String::new(),
            })
            .collect()
        }),
    );
    reg.command(
        Command::new(
            "session.folder.new",
            Group::Session,
            "New folder…",
            "Folders group sessions. They are not directories on disk.",
            go("session.folder.new"),
        )
        .keywords(&["folder", "create", "group", "organise"])
        .order(45)
        .argument(Argument {
            placeholder: "Folder name",
            required: true,
        }),
    );
    reg.command(
        Command::new(
            "session.folder.remove",
            Group::Session,
            "Remove folder…",
            "Ungroup the sessions in a folder. Sessions stay.",
            go("session.folder.remove"),
        )
        .keywords(&["folder", "remove", "delete", "ungroup"])
        .order(46)
        .argument(Argument {
            placeholder: "Filter folders",
            required: false,
        })
        .choices(|| {
            crate::session_panel::folder_pairs()
                .into_iter()
                .map(|(id, name)| Choice {
                    id,
                    label: name,
                    detail: String::new(),
                })
                .collect()
        }),
    );
    reg.command(
        Command::new(
            "session.bulk.pin",
            Group::Session,
            "Pin selected sessions",
            "Pin every session in the current selection",
            go("session.bulk.pin"),
        )
        .keywords(&["bulk", "pin", "selected"])
        .order(47),
    );
    reg.command(
        Command::new(
            "session.bulk.unpin",
            Group::Session,
            "Unpin selected sessions",
            "Unpin every session in the current selection",
            go("session.bulk.unpin"),
        )
        .keywords(&["bulk", "unpin", "selected"])
        .order(48),
    );
    reg.command(
        Command::new(
            "session.bulk.archive",
            Group::Session,
            "Archive selected sessions",
            "Archive every session in the current selection",
            go("session.bulk.archive"),
        )
        .keywords(&["bulk", "archive", "selected"])
        .order(49),
    );
    reg.command(
        Command::new(
            "session.bulk.unarchive",
            Group::Session,
            "Unarchive selected sessions",
            "Restore every session in the current selection",
            go("session.bulk.unarchive"),
        )
        .keywords(&["bulk", "unarchive", "restore", "selected"])
        .order(50),
    );
    reg.command(
        Command::new(
            "session.bulk.move",
            Group::Session,
            "Move selected sessions to folder…",
            "File the current selection into a folder",
            go("session.bulk.move"),
        )
        .keywords(&["bulk", "move", "folder", "file"])
        .order(51)
        .argument(Argument {
            placeholder: "Filter folders",
            required: false,
        })
        .choices(|| {
            let mut rows = vec![Choice {
                id: "__unfiled".into(),
                label: "Sessions".into(),
                detail: "Remove them from a folder".into(),
            }];
            for (id, name) in crate::session_panel::folder_pairs() {
                rows.push(Choice {
                    id,
                    label: name,
                    detail: String::new(),
                });
            }
            rows
        }),
    );
    reg.command(
        Command::new(
            "session.pin",
            Group::Session,
            "Pin session",
            "Pin the cursor session, or the open one",
            go("session.pin"),
        )
        .keywords(&["pin", "star"])
        .order(52)
        .visible(|| !crate::session_panel::cursor_session().pinned),
    );
    reg.command(
        Command::new(
            "session.unpin",
            Group::Session,
            "Unpin session",
            "Unpin the cursor session, or the open one",
            go("session.unpin"),
        )
        .keywords(&["unpin", "star"])
        .order(53)
        .visible(|| {
            let cursor = crate::session_panel::cursor_session();
            !cursor.id.is_empty() && cursor.pinned
        }),
    );
    reg.command(
        Command::new(
            "session.copy-id",
            Group::Session,
            "Copy session ID",
            "Copy the cursor session id, or the open one",
            go("session.copy-id"),
        )
        .keywords(&["copy", "id", "session id"])
        .order(54),
    );
    reg.command(
        Command::new(
            "session.unread",
            Group::Session,
            "Mark session as unread",
            "Mark the cursor session unread, or the open one",
            go("session.unread"),
        )
        .keywords(&["unread", "mark", "seen"])
        .order(55)
        // State-honest visibility (#1557): only a read session that is
        // not the open one — an already-unread row has nothing to do,
        // and opening re-marks the open session seen by design.
        .visible(|| {
            let cursor = crate::session_panel::cursor_session();
            !cursor.id.is_empty()
                && crate::session_list::can_mark_unread(cursor.unread, cursor.active)
        }),
    );
    reg.command(
        Command::new(
            "session-rename",
            Group::Session,
            "Rename session…",
            "Set the title of the open session",
            go("session.rename"),
        )
        .keywords(&["rename", "title", "retitle"])
        .order(80)
        .argument(Argument {
            placeholder: "New title",
            required: true,
        }),
    );
    reg.command(
        Command::new(
            "session-fork",
            Group::Session,
            "Fork session…",
            "Branch the cursor session, or the open one, into a new root",
            go("session.fork"),
        )
        .keywords(&["fork", "branch", "copy session"])
        .order(81),
    );
    reg.command(
        Command::new(
            "session-export",
            Group::Session,
            "Export session timeline (JSON)…",
            "Download the redacted timeline JSON",
            go("session.export"),
        )
        .keywords(&["export", "timeline", "json", "download"])
        .order(82),
    );
    reg.command(
        Command::new(
            "session-export-md",
            Group::Session,
            "Export session as Markdown…",
            "Download the visible transcript as markdown",
            go("session.export-md"),
        )
        .keywords(&["export", "markdown", "md", "download"])
        .order(83),
    );
    reg.command(
        Command::new(
            "transcript.regenerate",
            Group::Session,
            "Regenerate last reply",
            "Run the last Strike turn again in this session",
            go("transcript.regenerate"),
        )
        .keywords(&["regenerate", "retry", "redo", "again"])
        .order(86),
    );
    reg.command(
        Command::new(
            "transcript.retry",
            Group::Session,
            "Retry this message",
            "Resend the cursor message, or the last user turn",
            go("transcript.retry"),
        )
        .keywords(&["retry", "resend", "again"])
        .order(87),
    );
    reg.command(
        Command::new(
            "transcript.fork-message",
            Group::Session,
            "Fork from this message",
            "Branch from the cursor message",
            go("transcript.fork-message"),
        )
        .keywords(&["fork", "branch"])
        .order(88),
    );
    reg.command(
        Command::new(
            "transcript.jump",
            Group::Session,
            "Jump to message…",
            "Long-jump to a message in the open transcript",
            go("transcript.jump"),
        )
        .keywords(&["jump", "go to", "navigate", "message"])
        .order(89)
        .argument(Argument {
            placeholder: "Filter messages",
            required: false,
        })
        .choices(crate::transcript_view::message_choices),
    );
}
