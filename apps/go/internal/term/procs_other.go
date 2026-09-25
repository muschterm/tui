//go:build unix && !linux

package term

// sessionMembers is not implemented here: without a portable session listing
// only the shell's own process group is signalled, and ExitStatus reports
// DescendantsUnknown.
func sessionMembers(int) (map[int]int, bool) { return nil, false }
