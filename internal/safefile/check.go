package safefile

import (
	"fmt"
	"io/fs"
	"os"
)

// IsSpecialMode reports whether mode is a FIFO, device, or socket.
func IsSpecialMode(mode fs.FileMode) bool {
	return mode&os.ModeNamedPipe != 0 || mode&os.ModeDevice != 0 || mode&os.ModeSocket != 0
}

// CheckLeaf reports whether path's final component is acceptable.
// refuseSymlink: when true, a symlink leaf is rejected (mutation policy).
// Special files (FIFO/device/socket) are always rejected.
// Directories are rejected with CodeNotRegular (callers that want dirs use Stat).
// Missing paths are OK (returns nil) so create paths can proceed.
func CheckLeaf(path string, refuseSymlink bool) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	mode := fi.Mode()
	if mode&os.ModeSymlink != 0 {
		if refuseSymlink {
			return errf(CodeSymlink, path, "refusing symlink leaf %q", path)
		}
		// Follow once for special-file check on target.
		target, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // dangling symlink — caller decides
			}
			return err
		}
		mode = target.Mode()
		fi = target
	}
	if IsSpecialMode(mode) {
		return errf(CodeSpecialFile, path, "refusing special file %s %q", modeType(mode), path)
	}
	if fi.IsDir() {
		return errf(CodeNotRegular, path, "path is a directory: %q", path)
	}
	if !mode.IsRegular() {
		// Non-regular, non-special (e.g. unexpected mode bits): refuse closed.
		return errf(CodeNotRegular, path, "path is not a regular file: %q", path)
	}
	return nil
}

func modeType(mode fs.FileMode) string {
	switch {
	case mode&os.ModeNamedPipe != 0:
		return "fifo"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeDevice != 0:
		if mode&os.ModeCharDevice != 0 {
			return "char-device"
		}
		return "device"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "dir"
	case mode.IsRegular():
		return "file"
	default:
		return fmt.Sprintf("mode=%s", mode.String())
	}
}
