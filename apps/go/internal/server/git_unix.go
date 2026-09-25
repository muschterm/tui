//go:build unix

package server

import (
	"io"
	"os"
	"os/exec"
	"path"
	"syscall"

	"golang.org/x/sys/unix"
)

// configureGitProcess puts git in its own process group and kills the whole
// group on cancellation, so filter or other child processes are not orphaned.
func configureGitProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// readUntracked opens root/rel without following any symlink below root:
// intermediate directories are resolved by openParent (openat2 with
// RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS on Linux, an O_NOFOLLOW component walk
// elsewhere) and the final component is inspected with AT_SYMLINK_NOFOLLOW.
func readUntracked(root, rel string, limit int) (untrackedFile, error) {
	var out untrackedFile
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return out, failure("unavailable", "checkout directory is unavailable")
	}
	defer unix.Close(rootFD)
	dir, name := path.Split(rel)
	parent := rootFD
	if dir != "" {
		if parent, err = openParent(rootFD, dir[:len(dir)-1]); err != nil {
			return out, failure("not_diffable", "path is not a regular file inside the checkout")
		}
		defer unix.Close(parent)
	}
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return out, failure("not_found", "file is no longer present")
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		buf := make([]byte, 4096)
		n, err := unix.Readlinkat(parent, name, buf)
		if err != nil {
			return out, failure("not_found", "file is no longer present")
		}
		return untrackedFile{mode: "120000", data: buf[:n], truncated: n == len(buf)}, nil
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return out, failure("not_diffable", "path is not a regular file inside the checkout")
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return out, failure("not_diffable", "path is not a regular file inside the checkout")
	}
	out.mode = "100644"
	if st.Mode&0o111 != 0 {
		out.mode = "100755"
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return out, failure("unavailable", "file could not be read")
	}
	if len(data) > limit {
		data, out.truncated = data[:limit], true
	}
	out.data = data
	return out, nil
}
