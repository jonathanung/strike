# Approval modes in the web client

The composer chip, the command palette, and the permissions panel all use
Strike's five wire modes. The labels are the Strike names. They are not
renamed to another product's Normal / Reads / Trust / YOLO set.

| Wire mode | Chip | What it does | Nearby label it is not |
| --- | --- | --- | --- |
| `default` | Default | Ask on the normal rules. | Closest to a normal ask-first posture. |
| `plan` | Plan | Writes and edits are denied. Shell still asks. | Stricter than a read-only shell. There is no Reads mode. |
| `soft-approve` | Soft | The ask stays on screen. If you do nothing, it allows once when the countdown ends. A positive `permissionAutoApproveSeconds` (clamped 1–60) wins; otherwise this mode uses 15 seconds. Names in `permissionAutoApproveExclude` never count down. | No analogue in a four-mode menu. |
| `accept-edits` | Edits | File edits and writes are allowed. Shell and network still ask. | File-only. It is not a trust-everything mode. |
| `yolo` | Yolo | This session skips asks that are not hard-denied. Other sessions stay as they are. | Session-only. Deny rules still apply. |

Switching the live session to Soft, Edits, or Yolo asks first. Switching to
Default or Plan applies immediately. The same gate covers the command
palette. Unknown mode ids are ignored.

## Rules

`GET /v1/permissions/rules` lists the global file (`~/.strike/config`) and
the project file (`<workdir>/.strike/config`). Inside one file the last
matching rule wins, so a later allow can override an earlier deny in that
file. Adding an allow or ask, and deleting a deny, require `confirm: true`.
Adding a deny does not. Managed denies are not in this list and cannot be
overridden here. Yolo and Edits do not turn an evaluated deny into an allow.

## Workspace and global defaults

`PATCH /v1/settings` with `mode` stores the global default for new sessions.
`PATCH /v1/workspaces/{id}` with `permissionMode` stores a mode on that
workspace only; storing does not by itself change the live session.
Opening a workspace applies its stored mode to the live session (the
workspace picker's behaviour). The panel's Apply button posts the stored
mode through the same confirmation as the chip. A project config mode, when set, is what a new
session starts with; otherwise the global mode is.

## Asks and audit

`permission.asked` becomes a card: Allow once (`once`), Allow session
(`always`, this session only), Allow project (`project`, appends an allow
rule), Deny (`reject`). Escape denies the oldest ask. Keys 1–4 do the same
when focus is not in a text field. The soft-approve countdown is visible
and replies `once`.

`permission.decided` is listed for this session. `GET /v1/permissions/audit`
reads the daemon audit sink when one is attached. Patterns in that log are
redacted.
