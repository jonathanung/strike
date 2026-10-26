//go:build !linux

package terminal

func processCWD(int) (string, bool) { return "", false }
