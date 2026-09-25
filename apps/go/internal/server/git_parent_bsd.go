//go:build unix && !linux

package server

import (
	"strings"

	"golang.org/x/sys/unix"
)

// openParent walks dir one component at a time with O_NOFOLLOW, so no
// symlink is followed. Components were already validated (no "..", ".").
func openParent(rootFD int, dir string) (int, error) {
	fd, err := unix.Dup(rootFD)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(dir, "/") {
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}
