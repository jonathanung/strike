# Harness Examples

This directory contains the same choose-best behavior in all supported forms:

- `choose_best.go` is an embedded Go function. Only tests currently import it;
  the stock Strike binary does not include it.
- `go-subprocess` is a Go executable using `sdk/go/harness`.
- `choose-best.mjs` is a Node subprocess using `sdk/typescript`.
- `ChooseBest.lean` is a Lean subprocess using the `sdk/lean` Lake package.
- `config.example.json` configures the three subprocess examples.

`internal/engine/harness_test.go` runs all three through the same task-subagent
integration test.
