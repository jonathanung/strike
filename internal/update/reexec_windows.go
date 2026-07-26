//go:build windows

package update

import "fmt"

func reexec(exe string, args []string) error {
	return fmt.Errorf("%w", ErrUnsupportedPlatform)
}
