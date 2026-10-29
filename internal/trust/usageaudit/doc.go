// Package usageaudit is the web cockpit's usage ledger and tamper-evident
// audit log.
//
// Usage totals are summed from session JSONL usage.reported events. Dollars
// come only from a caller-supplied catalog price. A model with no price is
// reported as tokens with cost unknown — this package never invents a rate.
//
// The audit log is an append-only hash chain under ~/.strike/usage-audit/.
// Payloads are scrubbed with pkg/redact before they are hashed, so the chain
// commits to the redacted text. It is separate from the retention-bounded
// security sink in internal/trust/audit: pruning that sink would break a
// hash chain.
package usageaudit
