// Package version holds build-time identity stamped via -ldflags.
package version

import "fmt"

// Version and Commit are set at link time:
//
//	-X github.com/jonathanung/strike-cli/internal/version.Version=v0.1.0
//	-X github.com/jonathanung/strike-cli/internal/version.Commit=<sha>
//
// Defaults suit local `go build` / `go test` without ldflags.
var (
	Version = "dev"
	Commit  = "none"
)

// String returns "version (commit)" for CLI display.
func String() string {
	v := Version
	if v == "" {
		v = "dev"
	}
	c := Commit
	if c == "" {
		c = "none"
	}
	if len(c) > 7 {
		c = c[:7]
	}
	return fmt.Sprintf("%s (%s)", v, c)
}
