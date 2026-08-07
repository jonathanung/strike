// Package safefile provides hardened path I/O helpers for tool implementations:
// special-file rejection (FIFO/device/socket), consistent symlink leaf policy,
// path identity normalization for grant matching, and timed reads so FIFOs
// cannot hang a tool forever.
//
// Symlink policy (unix):
//   - Leaf symlinks are refused for mutation helpers (write/replace/remove).
//   - Read helpers may follow symlinks only after the caller has already
//     confined the path (e.g. tool.resolveInWorkspace); OpenRead still rejects
//     when the final target is a special file.
//   - Grant/path matching should use Identity so equivalent paths compare equal.
//
// See docs/safefile.md and issue #896.
package safefile
