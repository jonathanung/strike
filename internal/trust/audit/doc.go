// Package audit implements a compact, retention-bounded security audit sink
// for trust-boundary decisions (permission, sandbox, secret_ref_use,
// content_guard, admission, egress, toolchain_match).
//
// Storage is append-only segmented JSONL under ~/.strike/audit/ (schema
// versioned). Events are redacted before persist via pkg/telemetry +
// pkg/redact. This does not replace session JSONL transcripts — only compact
// security decisions are stored by default.
//
// See docs/audit.md and issue #893.
package audit
