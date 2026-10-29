//go:build !linux

package terminal

func processCWD(int) (string, bool) { return "", false }

func cwdUnseenIsEscape() bool { return false }

func foregroundInside(string, int) (bool, bool) { return false, false }
