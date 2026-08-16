// Package tui is the generate entrypoint for the Strike TUI app.
//
// App sources live under app/_src and flatten into app/. CI still runs
// `go generate ./internal/tui`, which forwards here:
//
//	go generate ./app
package tui

//go:generate go generate ./app
