//go:build !unix

package usageaudit

import "os"

func lockFile(_ *os.File, _ bool) error { return nil }

func unlockFile(_ *os.File) error { return nil }
