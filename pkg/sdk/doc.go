// Package sdk is a thin Go client over pkg/protocol for driving Strike
// sessions from Go programs.
//
// # What this is
//
// Frontends talk to the engine only through Ops (client → engine) and Events
// (engine → client). This package wraps that seam with:
//
//   - an in-process [Client] over Go channels (custom composition roots that
//     already construct the engine)
//   - JSONL encode/decode helpers matching session logs and transport envelopes
//   - a one-shot [Client.RunTurn] helper for headless prompts
//   - [ReadSession] for replaying durable JSONL transcripts
//
// # What this is not
//
// The agent engine, tools, auth, and providers stay under internal/. External
// modules cannot import them. To run turns you either wire a custom Strike
// binary that exposes Ops/Events channels into [New], or speak the same
// Op/Event JSON envelopes over a transport (strike serve WebSocket/HTTP today;
// strike rpc when available).
//
// Subprocess task harnesses use a different protocol — see sdk/go/harness,
// not this package.
//
// # Import path
//
//	import (
//		"github.com/jonathanung/strike-cli/pkg/protocol"
//		"github.com/jonathanung/strike-cli/pkg/sdk"
//	)
//
// # Stability
//
// Wire types and envelope shapes are owned by pkg/protocol (semver
// protocol.Version). This package's helpers are additive convenience; they
// do not redefine the wire schema. Breaking changes to public sdk APIs follow
// Go module semver for the strike-cli module.
//
// # Minimal channel client
//
//	client := sdk.New(eng.Ops(), eng.Events())
//	result, err := client.RunTurn(ctx, sdk.Turn{Text: "summarize this repo"})
//
// # Session JSONL
//
//	events, err := sdk.ReadSession(path)
package sdk
