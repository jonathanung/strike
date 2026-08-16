//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package history

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func secureOpenHistory(globalRoot, name string) (*os.File, error) {
	createdRoot := false
	if err := os.Mkdir(globalRoot, 0o700); err == nil {
		createdRoot = true
	} else if !os.IsExist(err) {
		return nil, fmt.Errorf("create global directory: %w", err)
	}

	rootFD, err := unix.Open(globalRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open global directory: %w", err)
	}
	defer unix.Close(rootFD)
	var rootStat unix.Stat_t
	if err := unix.Fstat(rootFD, &rootStat); err != nil {
		return nil, fmt.Errorf("inspect global directory: %w", err)
	}
	if rootStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, fmt.Errorf("open global directory: not a directory")
	}
	if createdRoot {
		if err := unix.Fchmod(rootFD, 0o700); err != nil {
			return nil, fmt.Errorf("secure global directory: %w", err)
		}
	}

	if err := unix.Mkdirat(rootFD, "history", 0o700); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("create history directory: %w", err)
	}

	dirFD, err := unix.Openat(rootFD, "history", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open history directory: %w", err)
	}
	defer unix.Close(dirFD)
	var dirStat unix.Stat_t
	if err := unix.Fstat(dirFD, &dirStat); err != nil {
		return nil, fmt.Errorf("inspect history directory: %w", err)
	}
	if dirStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, fmt.Errorf("open history directory: not a directory")
	}
	if err := unix.Fchmod(dirFD, 0o700); err != nil {
		return nil, fmt.Errorf("secure history directory: %w", err)
	}

	fd, err := unix.Openat(dirFD, name, unix.O_CREAT|unix.O_APPEND|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open history: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("inspect history: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, fmt.Errorf("open history: not a regular file")
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("secure history file: %w", err)
	}
	return os.NewFile(uintptr(fd), filepath.Join(globalRoot, "history", name)), nil
}
