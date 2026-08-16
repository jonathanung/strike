//go:build unix

package container

import "os"

func osGetuid() int {
	return os.Getuid()
}
