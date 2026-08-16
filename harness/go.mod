module github.com/jonathanung/strike-cli/harness

go 1.26.2

require (
	github.com/bmatcuk/doublestar/v4 v4.10.0
	github.com/charmbracelet/x/ansi v0.11.7
	github.com/jonathanung/strike-cli/pkg/protocol v0.0.0
	github.com/jonathanung/strike-cli/pkg/redact v0.0.0
	golang.org/x/sys v0.47.0
)

require (
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.0 // indirect
	github.com/mattn/go-runewidth v0.0.24 // indirect
)

replace github.com/jonathanung/strike-cli/pkg/protocol => ../pkg/protocol

replace github.com/jonathanung/strike-cli/pkg/redact => ../pkg/redact
