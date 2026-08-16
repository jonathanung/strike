//go:build ignore

// Delegates flatten to internal/tui/app so `go generate ./internal/tui`
// (CI / older docs) still works after the #1209 move.
package ignore

//go:generate go generate ./app
