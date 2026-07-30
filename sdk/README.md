# Harness SDKs

These packages help external programs implement Strike's JSONL harness
protocol:

- `go/harness` provides a Go subprocess runtime.
- `typescript` provides a typed Node runtime.
- `lean` provides the `StrikeHarness` Lake package.

All produce subprocess harnesses. They are not linked into the Go CLI and do
not add language-specific behavior to Strike. Configure the resulting command
under `harnesses` in Strike config.

An external Go executable uses `go/harness`; an embedded Go harness does not.
Embedded functions use `internal/harness` and must be imported, compiled, and
registered in a custom Strike composition root.
