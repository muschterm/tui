//go:build unix

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"golang.org/x/sys/unix"
)

// filesSupported gates the files-read capability.
const filesSupported = true

// openFilesRoot opens the checkout root directory.
func openFilesRoot(root string) (int, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, failure("unavailable", "checkout directory is unavailable")
	}
	return fd, nil
}

// openFilesDir opens dir ("" for the root) beneath rootFD without following
// any symlink.
func openFilesDir(rootFD int, dir string) (int, error) {
	if dir == "" {
		return unix.Dup(rootFD)
	}
	fd, err := openParent(rootFD, dir)
	switch {
	case err == nil:
		return fd, nil
	case errors.Is(err, unix.ENOENT):
		return -1, failure("not_found", "directory is no longer present")
	case errors.Is(err, unix.ENOTDIR), errors.Is(err, unix.ELOOP):
		return -1, failure("not_directory", "path is not a directory inside the checkout")
	case errors.Is(err, unix.EXDEV):
		return -1, failure("invalid", "path must remain inside the checkout")
	}
	return -1, failure("unavailable", "directory could not be opened")
}

func fileKind(mode uint32) string {
	switch mode & unix.S_IFMT {
	case unix.S_IFREG:
		return protocol.FileKindFile
	case unix.S_IFDIR:
		return protocol.FileKindDir
	case unix.S_IFLNK:
		return protocol.FileKindSymlink
	}
	return protocol.FileKindOther
}

// listFiles returns one page of dir's entries.
func listFiles(ctx context.Context, root, dir, cursor string, hidden bool) (protocol.FileList, error) {
	out := protocol.FileList{Dir: dir, Entries: []protocol.FileEntry{}}
	if !validFilesDir(dir) {
		return out, failure("invalid", "path must be a relative checkout path outside .git")
	}
	rootFD, err := openFilesRoot(root)
	if err != nil {
		return out, err
	}
	defer unix.Close(rootFD)
	fd, err := openFilesDir(rootFD, dir)
	if err != nil {
		return out, err
	}
	// The name only serves os.File's lstat fallback for unknown d_type.
	f := os.NewFile(uintptr(fd), filepath.Join(root, dir))
	defer f.Close()
	var names []dirEntryName
	for scanned := 0; ; {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		entries, readErr := f.ReadDir(256)
		for _, e := range entries {
			scanned++
			if !utf8.ValidString(e.Name()) {
				out.Skipped++
				continue
			}
			names = append(names, dirEntryName{name: e.Name(), dir: e.Type().IsDir()})
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return out, failure("unavailable", "directory could not be read")
		}
		if scanned >= filesScanLimit {
			out.Truncated = true
			break
		}
	}
	page, next, err := pageFiles(names, cursor, hidden)
	if err != nil {
		return out, err
	}
	out.Next = next
	for _, n := range page {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, n.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue // removed since the scan
		}
		out.Entries = append(out.Entries, protocol.FileEntry{Name: n.name, Kind: fileKind(uint32(st.Mode)), Size: st.Size, Token: statToken(&st)})
	}
	return out, nil
}

// openFileParent opens p's parent directory and returns it with p's name.
func openFileParent(rootFD int, p string) (int, string, error) {
	dir, name := path.Split(p)
	fd, err := openFilesDir(rootFD, strings.TrimSuffix(dir, "/"))
	return fd, name, err
}

// readFile reads one regular file's bounded content; see protocol.FileRead.
func readFile(ctx context.Context, root, p string) (protocol.FileRead, error) {
	out := protocol.FileRead{Path: p}
	if !validFilesPath(p) {
		return out, failure("invalid", "path must be a relative checkout path outside .git")
	}
	rootFD, err := openFilesRoot(root)
	if err != nil {
		return out, err
	}
	defer unix.Close(rootFD)
	parent, name, err := openFileParent(rootFD, p)
	if err != nil {
		var pe *protocol.Error
		if errors.As(err, &pe) && pe.Code == "not_directory" {
			return out, failure("not_found", "file is no longer present")
		}
		return out, err
	}
	defer unix.Close(parent)
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return out, failure("not_found", "file is no longer present")
	}
	out.Size, out.Token = st.Size, statToken(&st)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
	case unix.S_IFLNK:
		out.Kind = protocol.FileReadNotRegular
		buf := make([]byte, 4096)
		if n, err := unix.Readlinkat(parent, name, buf); err == nil {
			out.LinkTarget = strings.ToValidUTF8(string(buf[:n]), "�")
		}
		return out, nil
	default:
		out.Kind = protocol.FileReadNotRegular
		return out, nil
	}
	if st.Size > protocol.FileHashLimit {
		out.Kind = protocol.FileReadTooLarge
		return out, nil
	}
	// O_NONBLOCK: a path swapped for a FIFO after the lstat never blocks.
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return out, failure("not_found", "file is no longer present")
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		out.Kind = protocol.FileReadNotRegular
		return out, nil
	}
	out.Size, out.Token = st.Size, statToken(&st)
	if st.Size > protocol.FileHashLimit {
		out.Kind = protocol.FileReadTooLarge
		return out, nil
	}
	data := make([]byte, 0, st.Size)
	buf := make([]byte, 64<<10)
	limited := io.LimitReader(file, protocol.FileHashLimit+1)
	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		n, readErr := limited.Read(buf)
		data = append(data, buf[:n]...)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return out, failure("unavailable", "file could not be read")
		}
	}
	if len(data) > protocol.FileHashLimit {
		// It grew past the hash limit while being read.
		out.Kind, out.Size = protocol.FileReadTooLarge, int64(len(data))
		return out, nil
	}
	sum := sha256.Sum256(data)
	out.Sha256, out.Size = hex.EncodeToString(sum[:]), int64(len(data))
	classifyFile(&out, data[:min(len(data), protocol.FileTextLimit+1)], out.Size)
	return out, nil
}

// statFile reports p's kind and change token without following it. A
// missing path (or a parent that is no longer a directory) is absent.
func statFile(ctx context.Context, root, p string) (protocol.FileStat, error) {
	out := protocol.FileStat{Path: p}
	if !validFilesPath(p) {
		return out, failure("invalid", "path must be a relative checkout path outside .git")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	rootFD, err := openFilesRoot(root)
	if err != nil {
		return out, err
	}
	defer unix.Close(rootFD)
	parent, name, err := openFileParent(rootFD, p)
	if err != nil {
		var pe *protocol.Error
		if errors.As(err, &pe) && (pe.Code == "not_found" || pe.Code == "not_directory") {
			out.Kind = protocol.FileKindAbsent
			return out, nil
		}
		return out, err
	}
	defer unix.Close(parent)
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			out.Kind = protocol.FileKindAbsent
			return out, nil
		}
		return out, failure("unavailable", "file could not be examined")
	}
	out.Kind, out.Size, out.Token = fileKind(uint32(st.Mode)), st.Size, statToken(&st)
	return out, nil
}
