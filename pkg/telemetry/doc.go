// Package telemetry defines the versioned security and harness telemetry
// families used by timeline export and the durable audit sink.
//
// This is an export/observability schema, not the Op/Event wire in
// pkg/protocol. Wire compatibility is unchanged: families fold protocol
// events and internal decisions into compact, redaction-annotated records.
//
// Source of truth for field names, types, and redaction policy:
//
//	schemas/telemetry/v1/registry.json
//
// Go structs in this package must stay in lockstep with that registry.
// Run `make telemetry-check` (or `go test ./pkg/telemetry -run TestRegistry`)
// to fail CI on drift. See docs/telemetry.md for extension rules.
//
// v1 does not require an external OpenTelemetry collector; optional OTel
// exporters may map these families later.
package telemetry
