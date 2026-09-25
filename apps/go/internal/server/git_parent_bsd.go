//go:build unix && !linux

package server

// openParent uses the portable O_NOFOLLOW component walk.
func openParent(rootFD int, dir string) (int, error) { return walkParent(rootFD, dir) }
