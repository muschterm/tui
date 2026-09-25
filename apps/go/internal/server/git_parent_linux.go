package server

import "golang.org/x/sys/unix"

// openParent resolves dir beneath rootFD, refusing every symlink and any
// escape, atomically in the kernel.
func openParent(rootFD int, dir string) (int, error) {
	return unix.Openat2(rootFD, dir, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
}
