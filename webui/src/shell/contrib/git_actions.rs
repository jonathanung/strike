//! Git, file, and worktree verbs that left the panels (#1599).
//! Push, pull, fetch, and stash are reserved here and stay disabled
//! until the daemon grows those routes.

use std::rc::Rc;

use crate::shell::{Argument, Choice, Command, ConfirmSpec, Group, ShellHook};

fn go(id: &'static str) -> Rc<dyn Fn(&str) -> Result<(), String>> {
    Rc::new(move |args| crate::shell::dispatch(id, args))
}

fn reserved(id: &'static str, label: &'static str, detail: &'static str, keywords: &'static [&'static str], order: i32) -> Command {
    Command::new(id, Group::Git, label, detail, Rc::new(|_| Ok(())))
        .keywords(keywords)
        .order(order)
        .enabled(|| false)
}

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "panel-files",
            Group::Navigate,
            "Files",
            "Open the Files tab",
            go("panel-files"),
        )
        .keywords(&["files", "tree", "directory"])
        .order(40),
    );
    reg.command(
        Command::new(
            "git.refresh",
            Group::Git,
            "Git: Refresh status",
            "Reload branch, changes, and the log",
            go("git.refresh"),
        )
        .keywords(&["git", "refresh", "reload", "status"])
        .order(10),
    );
    reg.command(
        Command::new(
            "git.log",
            Group::Git,
            "Git: View log",
            "Show or hide recent commits",
            go("git.log"),
        )
        .keywords(&["git", "log", "history", "commits"])
        .order(11),
    );
    reg.command(
        Command::new(
            "git.switch",
            Group::Git,
            "Git: Switch branch…",
            "Check out another local branch",
            go("git.switch"),
        )
        .keywords(&["git", "branch", "checkout", "switch"])
        .order(12)
        .argument(Argument {
            placeholder: "Filter branches",
            required: false,
        })
        .choices(|| {
            crate::git::branch_names()
                .into_iter()
                .map(|name| Choice {
                    id: name.clone(),
                    label: name,
                    detail: String::new(),
                })
                .collect()
        }),
    );
    reg.command(
        Command::new(
            "git.create",
            Group::Git,
            "Git: Create branch…",
            "Create a branch and check it out",
            go("git.create"),
        )
        .keywords(&["git", "branch", "new", "create"])
        .order(13)
        .argument(Argument {
            placeholder: "New branch name",
            required: true,
        }),
    );
    reg.command(
        Command::new(
            "git.stage-commit",
            Group::Git,
            "Git: Stage and commit file",
            "Stage the selected file and commit it",
            go("git.stage-commit"),
        )
        .keywords(&["git", "stage", "commit", "file", "quick"])
        .order(14),
    );
    reg.command(
        Command::new(
            "git.discard",
            Group::Git,
            "Git: Discard changes in file…",
            "Throw away uncommitted changes in the selected file",
            go("git.discard"),
        )
        .keywords(&["git", "discard", "revert", "checkout", "file"])
        .order(15),
    );
    reg.command(
        Command::new(
            "git.reset",
            Group::Git,
            "Git: Reset hard to HEAD…",
            "Discard every uncommitted change. This does not push.",
            go("git.reset"),
        )
        .keywords(&["git", "reset", "hard", "danger", "undo"])
        .order(16)
        .confirm(ConfirmSpec {
            title: "Reset hard to HEAD?",
            body: "Uncommitted changes in this worktree are discarded. Nothing is pushed.",
            confirm_label: "Reset hard",
        }),
    );
    reg.command(
        Command::new(
            "git.delete-branch",
            Group::Git,
            "Git: Delete branch…",
            "Delete a local branch that is not checked out",
            go("git.delete-branch"),
        )
        .keywords(&["git", "branch", "delete", "remove"])
        .order(17)
        .argument(Argument {
            placeholder: "Filter branches",
            required: false,
        })
        .choices(|| {
            crate::git::deletable_branches()
                .into_iter()
                .map(|name| Choice {
                    id: name.clone(),
                    label: name,
                    detail: String::new(),
                })
                .collect()
        }),
    );
    reg.command(
        Command::new(
            "git.copy-diff",
            Group::Git,
            "Git: Copy diff",
            "Copy the diff of the selected file",
            go("git.copy-diff"),
        )
        .keywords(&["git", "diff", "copy"])
        .order(18),
    );
    reg.command(reserved(
        "git.push",
        "Git: Push",
        "The daemon has no push route yet",
        &["git", "push", "publish", "remote"],
        19,
    ));
    reg.command(reserved(
        "git.pull",
        "Git: Pull",
        "The daemon has no pull route yet",
        &["git", "pull", "update", "remote"],
        20,
    ));
    reg.command(reserved(
        "git.fetch",
        "Git: Fetch",
        "The daemon has no fetch route yet",
        &["git", "fetch", "remote", "refresh"],
        21,
    ));
    reg.command(reserved(
        "git.stash",
        "Git: Stash changes…",
        "Stash lands with its own backend. No panel section.",
        &["git", "stash", "shelve", "wip"],
        22,
    ));
    reg.command(reserved(
        "git.stash-apply",
        "Git: Apply stash…",
        "Apply stash lands with its own backend. No panel section.",
        &["git", "stash", "shelve", "wip"],
        23,
    ));
    reg.command(
        Command::new(
            "worktrees.refresh",
            Group::Git,
            "Worktrees: Refresh",
            "Reload the worktree list",
            go("worktrees.refresh"),
        )
        .keywords(&["worktree", "refresh", "reload"])
        .order(30),
    );
    reg.command(
        Command::new(
            "worktrees.prune",
            Group::Git,
            "Worktrees: Prune stale worktrees…",
            "Remove strike-managed worktrees whose session is gone",
            go("worktrees.prune"),
        )
        .keywords(&["worktree", "prune", "clean", "stale"])
        .order(31)
        .confirm(ConfirmSpec {
            title: "Clean up stale worktrees?",
            body: "Strike-managed worktrees whose session is gone are removed, then missing git entries are pruned. Owned worktrees stay.",
            confirm_label: "Clean up stale",
        }),
    );
    reg.command(
        Command::new(
            "file.preview",
            Group::Git,
            "File: Toggle Markdown preview",
            "Switch a Markdown file between source and preview",
            go("file.preview"),
        )
        .keywords(&["file", "markdown", "preview", "source", "render"])
        .order(40),
    );
    reg.command(
        Command::new(
            "file.agent-diff",
            Group::Git,
            "File: Toggle agent diff",
            "Show or hide the agent diff for the open file",
            go("file.agent-diff"),
        )
        .keywords(&["file", "diff", "agent", "changes", "review"])
        .order(41),
    );
    reg.command(
        Command::new(
            "file.copy-path",
            Group::Git,
            "File: Copy path",
            "Copy the open file's path",
            go("file.copy-path"),
        )
        .keywords(&["file", "copy", "path"])
        .order(42),
    );
    reg.command(
        Command::new(
            "file.copy-contents",
            Group::Git,
            "File: Copy contents",
            "Copy the open file's text",
            go("file.copy-contents"),
        )
        .keywords(&["file", "copy", "contents"])
        .order(43),
    );
}
