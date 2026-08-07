# Hardened path I/O (`internal/safefile`)

Shared helpers for tool file access so read/write/edit/apply_patch share one
policy for **symlinks**, **special files** (FIFO/device/socket), **path
identity**, and **atomic replace**.

| Helper | Role |
|---|---|
| `CheckLeaf` | Reject FIFO/device/socket; optional refuse symlink leaf |
| `OpenRead` / `ReadFile` | Timed open + regular-file only (no hang on FIFO) |
| `WriteFile` | Refuse symlink leaf; temp + rename atomic replace |
| `Identity` / `SameIdentity` | Normalize paths for grant matching |

## Symlink policy (unix)

| Operation | Leaf symlink | Intermediate symlink |
|---|---|---|
| Read (`OpenRead`) | Follow; target must be regular | Followed via `Stat` after workspace resolve |
| Write / replace | **Refuse** (`symlink_refused`) | Callers must re-resolve workspace root (`resolveInWorkspace`) before write |
| Grant matching | Use `Identity` so alias paths compare equal | `EvalSymlinks` on existing prefix |

Workspace confinement remains the caller’s job (`internal/tool` resolve helpers).
`safefile` hardens the final open/read/write.

## Special files

FIFOs, devices, and sockets return `special_file` and are never read or written.
`OpenRead` also bounds open with `context` or `DefaultReadTimeout` (5s) so a
FIFO cannot block a tool forever even under races.

## Stable error codes

| Code | Meaning |
|---|---|
| `special_file` | FIFO, device, or socket |
| `symlink_refused` | Mutation through a symlink leaf |
| `not_regular` | Directory or other non-regular |
| `timeout` | Open/read exceeded deadline |
| `invalid_path` | Empty/unresolvable path |

Tool layer maps these onto `tool.CodedError` (`precondition_failed` /
`timeout` as appropriate).

## Normal text files

Behavior matches prior atomic write + workspace resolve for ordinary files:
content is unchanged; only unsafe file types and symlink-leaf mutations are
stricter.
