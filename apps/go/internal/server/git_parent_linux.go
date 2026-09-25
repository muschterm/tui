package server

import (
	"errors"

	"golang.org/x/sys/unix"
)

// openParent resolves dir beneath rootFD, refusing every symlink and any
// escape, atomically in the kernel. Kernels without openat2 (before 5.6) or
// seccomp filters that deny it fall back to the O_NOFOLLOW component walk.
func openParent(rootFD int, dir string) (int, error) {
	fd, err := unix.Openat2(rootFD, dir, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
		return walkParent(rootFD, dir)
	}
	return fd, err
}
