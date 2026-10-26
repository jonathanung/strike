//go:build linux

package terminal

import (
	"os"
	"strconv"
)

func processCWD(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	p, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	if err != nil || p == "" {
		return "", false
	}
	return p, true
}
