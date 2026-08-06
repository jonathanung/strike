package protocol

// Version is the semantic version of the Op/Event wire schema published by
// this package. See the package doc for stability guarantees.
//
// Bump rules (keep in sync with package doc):
//   - major: breaking JSON tags, type strings, or field meaning
//   - minor: additive ops/events/optional fields
//   - patch: non-wire changes
const Version = "1.4.0"

// LegacyVersion is the schema version assumed when an envelope omits "v"
// (sessions written before pkg/protocol published Version on the wire).
const LegacyVersion = "1.0.0"
