//go:build unix

package server

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// configureGitWriteProcess runs a write in a new session, so it has no
// controlling terminal (pinentry-curses cannot draw on a user's terminal) and
// leads its own process group. Cancellation sends SIGTERM, which Git handles
// by removing the lock files it holds; a SIGKILL would strand index.lock.
func configureGitWriteProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
}

// endGitGroup ends processes left in a finished write's process group (a
// hook's background job): SIGTERM, then SIGKILL after a short grace period.
func endGitGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	if syscall.Kill(-pgid, syscall.SIGTERM) != nil {
		return
	}
	for range 20 {
		time.Sleep(25 * time.Millisecond)
		if syscall.Kill(-pgid, 0) != nil {
			return
		}
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// Worktree access for Git writes. Every path is resolved beneath the
// repository toplevel with openParent (no symlink below the root is
// followed) and the final component is never followed either.

// Worktree tokens: "absent" (the final component does not exist and every
// existing ancestor is a real directory), "blocked" (an ancestor is a file or
// symlink), "unavailable" (cannot be examined safely), or "<kind>:<lstat>"
// where kind is reg, lnk, dir or other. The kind makes every type change a
// different token.
const (
	tokenAbsent      = "absent"
	tokenBlocked     = "blocked"
	tokenUnavailable = "unavailable"
)

func statToken(st *unix.Stat_t) string {
	kind := "other"
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		kind = "reg"
	case unix.S_IFLNK:
		kind = "lnk"
	case unix.S_IFDIR:
		kind = "dir"
	}
	return fmt.Sprintf("%s:%d:%d:%d:%d:%o", kind, st.Size, st.Mtim.Nano(), st.Ctim.Nano(), st.Ino, st.Mode)
}

// tokenKind returns reg, lnk, dir, other, or the token itself for absent,
// blocked and unavailable.
func tokenKind(token string) string {
	kind, _, _ := strings.Cut(token, ":")
	return kind
}

// openWorktreeParent opens rel's parent directory beneath root and returns it
// with rel's final component. The caller closes the descriptor.
func openWorktreeParent(root, rel string) (int, string, error) {
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", err
	}
	dir, name := path.Split(rel)
	if dir == "" {
		return rootFD, name, nil
	}
	parent, err := openParent(rootFD, dir[:len(dir)-1])
	unix.Close(rootFD)
	return parent, name, err
}

// parentToken classifies a failure to open the parent directory.
func parentToken(err error) string {
	switch {
	case errors.Is(err, unix.ENOENT):
		return tokenAbsent
	case errors.Is(err, unix.ENOTDIR), errors.Is(err, unix.ELOOP):
		return tokenBlocked
	}
	return tokenUnavailable
}

// worktreeStat is the worktree token of root/rel.
func worktreeStat(root, rel string) string {
	parent, name, err := openWorktreeParent(root, rel)
	if err != nil {
		return parentToken(err)
	}
	defer unix.Close(parent)
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return tokenAbsent
		}
		return tokenUnavailable
	}
	return statToken(&st)
}

// worktreeContent is what `git add` will read for one path: an open regular
// file or a symlink's target text, matching the token the caller pinned.
type worktreeContent struct {
	file    *os.File
	link    []byte
	symlink bool
}

// openWorktreeContent opens root/rel for hashing when its current token
// equals want. It refuses anything that is not a regular file or symlink.
func openWorktreeContent(root, rel, want string) (worktreeContent, error) {
	var out worktreeContent
	parent, name, err := openWorktreeParent(root, rel)
	if err != nil {
		return out, failure("stale_entry", "the file changed since status was read; refresh and review it again")
	}
	defer unix.Close(parent)
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || statToken(&st) != want {
		return out, failure("stale_entry", "the file changed since status was read; refresh and review it again")
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		buf := make([]byte, 4096)
		n, err := unix.Readlinkat(parent, name, buf)
		if err != nil || n == len(buf) {
			return out, failure("unavailable", "symlink target could not be read")
		}
		return worktreeContent{link: buf[:n], symlink: true}, nil
	case unix.S_IFREG:
	default:
		return out, failure("not_supported", "only regular files and symlinks can be staged here")
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return out, failure("stale_entry", "the file changed since status was read; refresh and review it again")
	}
	var fst unix.Stat_t
	if err := unix.Fstat(fd, &fst); err != nil || statToken(&fst) != want {
		unix.Close(fd)
		return out, failure("stale_entry", "the file changed since status was read; refresh and review it again")
	}
	return worktreeContent{file: os.NewFile(uintptr(fd), name)}, nil
}

// removeUntracked permanently deletes root/rel when it is still the regular
// file or symlink described by want. A symlink is removed, never its target;
// directories (including nested repositories) are refused.
func removeUntracked(root, rel, want string) error {
	parent, name, err := openWorktreeParent(root, rel)
	if err != nil {
		return failure("stale_entry", "the file changed since status was read; refresh and review it again")
	}
	defer unix.Close(parent)
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || statToken(&st) != want {
		return failure("stale_entry", "the file changed since status was read; refresh and review it again")
	}
	if kind := st.Mode & unix.S_IFMT; kind != unix.S_IFREG && kind != unix.S_IFLNK {
		return failure("not_supported", "only untracked regular files and symlinks can be discarded")
	}
	if err := unix.Unlinkat(parent, name, 0); err != nil {
		return failure("git_failed", "the file could not be deleted: "+err.Error())
	}
	return nil
}
