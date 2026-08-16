// Package tools holds Strike product builtins that depend on persist stores
// or integrate/ (memory, issue, plan, artifact, ledger, skill, notebook,
// LSP intel/nav, tui_snapshot, plan mode, phase_done, context_bundle).
//
// Kernel contract and generic builtins stay in internal/tool. cmd/strike
// registers both sets onto one Registry. Wire tool names are unchanged.
package tools
