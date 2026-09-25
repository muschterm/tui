//go:build unix

package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"golang.org/x/sys/unix"
)

// docsSupported reports whether shared documents work on this platform.
const docsSupported = true

// Document disk access. Every path is resolved beneath the checkout root
// without following symlinks (openParent), the final component is examined
// with AT_SYMLINK_NOFOLLOW, and writes replace the file atomically through a
// temporary file in the same directory.

// openDocParent opens rel's parent directory beneath root and returns it with
// rel's final name. A missing or symlinked parent is reported as the
// observation kind the caller should record.
func openDocParent(root, rel string) (rootFD, parent int, name, kind string, err error) {
	rootFD, err = unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return -1, -1, "", docDiskAbsent, err
		}
		return -1, -1, "", docDiskUnavailable, err
	}
	dir, name := path.Split(rel)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		parent, err = unix.Dup(rootFD)
	} else {
		parent, err = openParent(rootFD, dir)
	}
	if err != nil {
		unix.Close(rootFD)
		switch {
		case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR):
			return -1, -1, "", docDiskAbsent, err
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
			return -1, -1, "", docDiskNotRegular, err
		}
		return -1, -1, "", docDiskUnavailable, err
	}
	return rootFD, parent, name, "", nil
}

// observeDocFile reports the file's kind, token, mode and (for an editable
// size) content. It never follows a symlink.
func observeDocFile(root, rel string) docObservation {
	rootFD, parent, name, kind, err := openDocParent(root, rel)
	if err != nil {
		return docObservation{Kind: kind, Err: err}
	}
	defer unix.Close(rootFD)
	defer unix.Close(parent)
	return observeAt(parent, name)
}

func observeAt(parent int, name string) docObservation {
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return docObservation{Kind: docDiskAbsent}
		}
		return docObservation{Kind: docDiskUnavailable, Err: err}
	}
	obs := docObservation{Token: statToken(&st), Mode: uint32(st.Mode & 0o7777)}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		obs.Kind = docDiskNotRegular
		return obs
	}
	obs.Kind = docDiskPresent
	if st.Nlink > 1 {
		obs.Kind = docDiskLinked
	}
	if st.Size > protocol.DocumentMaxBytes {
		obs.TooLarge = true
		return obs
	}
	// O_NONBLOCK: a path swapped for a FIFO after the lstat never blocks.
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return docObservation{Kind: docDiskAbsent}
		}
		if errors.Is(err, unix.ELOOP) {
			return docObservation{Kind: docDiskNotRegular}
		}
		return docObservation{Kind: docDiskUnavailable, Err: err}
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return docObservation{Kind: docDiskNotRegular}
	}
	obs.Token, obs.Mode = statToken(&st), uint32(st.Mode&0o7777)
	if st.Nlink > 1 {
		obs.Kind = docDiskLinked
	}
	data, err := io.ReadAll(io.LimitReader(f, protocol.DocumentMaxBytes+1))
	if err != nil {
		return docObservation{Kind: docDiskUnavailable, Err: err}
	}
	if len(data) > protocol.DocumentMaxBytes {
		obs.TooLarge = true
		return obs
	}
	obs.Data = data
	return obs
}

// statDocFile returns only the file's token (or its kind when it is not a
// present regular file) for cheap polling.
func statDocFile(root, rel string) (string, string) {
	rootFD, parent, name, kind, err := openDocParent(root, rel)
	if err != nil {
		return kind, ""
	}
	defer unix.Close(rootFD)
	defer unix.Close(parent)
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return docDiskAbsent, ""
		}
		return docDiskUnavailable, ""
	}
	return docDiskPresent, statToken(&st)
}

// docWrite is the outcome of writeDocFile. Renamed reports that the new
// content replaced the file, even when a later step (directory fsync, fstat)
// failed.
type docWrite struct {
	Token   string
	Obs     docObservation
	Renamed bool
}

// writeDocFile replaces rel with data when the file still holds content
// whose SHA-256 is one of expect (or, with expect empty, when it is absent).
// The new file gets mode. commit runs the rename under the caller's save
// guard; it returns errStaleSave to abandon the write. When the file is not
// as expected (including a missing parent directory) the current
// observation is returned with errDocChanged and nothing is written.
func writeDocFile(root, rel string, data []byte, mode uint32, expect []string, commit func(rename func() error) error) (docWrite, error) {
	rootFD, parent, name, kind, err := openDocParent(root, rel)
	if err != nil {
		if kind == docDiskUnavailable {
			return docWrite{Obs: docObservation{Kind: kind, Err: err}}, err
		}
		return docWrite{Obs: docObservation{Kind: kind}}, errDocChanged
	}
	defer unix.Close(rootFD)
	defer unix.Close(parent)
	current := observeAt(parent, name)
	if len(expect) == 0 {
		if current.Kind != docDiskAbsent {
			return docWrite{Obs: current}, errDocChanged
		}
	} else if current.Kind != docDiskPresent || current.TooLarge || !slices.Contains(expect, docSHA(current.Data)) || current.Mode&0o200 == 0 {
		if current.Kind == docDiskUnavailable {
			return docWrite{Obs: current}, current.Err
		}
		return docWrite{Obs: current}, errDocChanged
	}
	tmp := docTempName(name)
	fd, err := unix.Openat(parent, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.ENAMETOOLONG) {
		tmp = docTempName("")
		fd, err = unix.Openat(parent, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	}
	if err != nil {
		return docWrite{}, err
	}
	f := os.NewFile(uintptr(fd), tmp)
	renamed := false
	defer func() {
		f.Close()
		if !renamed {
			_ = unix.Unlinkat(parent, tmp, 0)
		}
	}()
	// Mode is set explicitly so the umask does not change it.
	if err = unix.Fchmod(fd, mode&0o7777); err != nil {
		return docWrite{}, err
	}
	if _, err = f.Write(data); err != nil {
		return docWrite{}, err
	}
	if err = f.Sync(); err != nil {
		return docWrite{}, err
	}
	err = commit(func() error {
		// Revalidate immediately before the rename: a change since the
		// read above (for example a tool writing the file) must not be
		// replaced. The remaining window is the rename itself.
		var st unix.Stat_t
		statErr := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if len(expect) == 0 {
			if statErr == nil || !errors.Is(statErr, unix.ENOENT) {
				return errDocChanged
			}
		} else if statErr != nil || statToken(&st) != current.Token {
			return errDocChanged
		}
		if err := unix.Renameat(parent, tmp, parent, name); err != nil {
			return err
		}
		renamed = true
		return nil
	})
	if errors.Is(err, errDocChanged) {
		return docWrite{Obs: observeAt(parent, name)}, errDocChanged
	}
	if err != nil {
		return docWrite{}, err
	}
	// The new name must survive a crash; the content was synced above.
	if err = unix.Fsync(parent); err != nil {
		return docWrite{Renamed: true}, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return docWrite{Renamed: true}, err
	}
	return docWrite{Token: statToken(&st), Renamed: true}, nil
}

// docTempName is ".<name>.tui-<12 hex>.tmp", or ".tui-<12 hex>.tmp" when
// the name is too long for that.
func docTempName(name string) string {
	if name == "" {
		return ".tui-" + randomSuffix() + ".tmp"
	}
	return "." + name + ".tui-" + randomSuffix() + ".tmp"
}

var docTempPattern = regexp.MustCompile(`^\.(?:.+\.)?tui-[0-9a-f]{12}\.tmp$`)

// cleanupDocTemps removes temporary files a crashed save left beside rel:
// only regular files matching docTempName's patterns for this name (or the
// short form) and last modified before before.
func cleanupDocTemps(root, rel string, before time.Time) {
	rootFD, parent, name, _, err := openDocParent(root, rel)
	if err != nil {
		return
	}
	defer unix.Close(rootFD)
	defer unix.Close(parent)
	dup, err := unix.Dup(parent)
	if err != nil {
		return
	}
	dir := os.NewFile(uintptr(dup), "dir")
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return
	}
	for _, n := range names {
		if !docTempPattern.MatchString(n) || !strings.HasPrefix(n, "."+name+".tui-") && !strings.HasPrefix(n, ".tui-") {
			continue
		}
		var st unix.Stat_t
		if unix.Fstatat(parent, n, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
			continue
		}
		if time.Unix(st.Mtim.Unix()).Before(before) {
			_ = unix.Unlinkat(parent, n, 0)
		}
	}
}

func randomSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
