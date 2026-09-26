package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"golang.org/x/sys/unix"
)

const browseLimit = 128
const browseScanLimit = 4096

func (e *engine) browse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := protocol.BrowseRequest{Scope: q.Get("scope"), ProjectID: q.Get("project_id"), ThreadID: q.Get("thread_id"), Query: q.Get("query")}
	// Copy only the small target records. Filesystem reads never hold the lock.
	e.mu.Lock()
	root, err := browseRoot(e.snap, req)
	e.mu.Unlock()
	var result protocol.BrowseResult
	if err == nil {
		result, err = browsePaths(r.Context(), root, req)
	}
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		var pe *protocol.Error
		if errors.As(err, &pe) {
			_ = json.NewEncoder(w).Encode(pe)
		} else {
			_ = json.NewEncoder(w).Encode(failure("unavailable", "directory browsing unavailable: "+err.Error()))
		}
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func browseRoot(s protocol.Snapshot, req protocol.BrowseRequest) (string, error) {
	if req.Scope == "projects" {
		if req.ProjectID != "" || req.ThreadID != "" {
			return "", failure("invalid", "project browsing does not take a target")
		}
		if s.AppSettings.ProjectDirectory == "" {
			return "~", nil
		}
		return s.AppSettings.ProjectDirectory, nil
	}
	if req.Scope != "files" {
		return "", failure("invalid", "choose projects or files browsing")
	}
	if (req.ProjectID == "") == (req.ThreadID == "") {
		return "", failure("invalid", "select exactly one project or thread")
	}
	for _, p := range s.Projects {
		if req.ProjectID != "" && p.ID == req.ProjectID {
			return p.Path, nil
		}
	}
	for _, t := range s.Threads {
		if req.ThreadID != "" && t.ID == req.ThreadID {
			return threadCheckout(&s, &t)
		}
	}
	return "", failure("not_found", "workspace target not found")
}

func validRelativePath(path string) bool {
	if filepath.IsAbs(path) || strings.HasPrefix(path, "~") {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return len(path) <= 4096 && utf8.ValidString(path) && !strings.ContainsFunc(path, unicode.IsControl)
}

// insideGit reports whether a root-relative path resolves to the checkout's
// .git entry or below it. The lexical check cannot see case-insensitive
// volumes or in-checkout symlinks; os.Root only confines to the checkout.
func insideGit(root *os.Root, path string) bool {
	git, err := root.Stat(".git")
	if err != nil {
		return false
	}
	path = filepath.Clean(path)
	// A link may target a directory below .git; its resolved, link-free path
	// passes through the .git entry itself.
	if base, err := filepath.EvalSymlinks(root.Name()); err == nil {
		if resolved, err := filepath.EvalSymlinks(filepath.Join(base, path)); err == nil {
			if rel, err := filepath.Rel(base, resolved); err == nil && filepath.IsLocal(rel) {
				path = rel
			}
		}
	}
	prefix := ""
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		prefix = filepath.Join(prefix, part)
		if info, err := root.Stat(prefix); err == nil && os.SameFile(info, git) {
			return true
		}
	}
	return false
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/")), nil
	}
	if strings.HasPrefix(path, "~") {
		return "", failure("invalid", "use ~/ for the server home directory")
	}
	return path, nil
}

func browsePaths(parent context.Context, base string, req protocol.BrowseRequest) (protocol.BrowseResult, error) {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	out := protocol.BrowseResult{Entries: []protocol.PathEntry{}}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if strings.HasPrefix(base, "fixture://") {
		return out, failure("unavailable", "fixture workspaces do not contain local files")
	}
	if len(req.Query) > 4096 || !utf8.ValidString(req.Query) || strings.ContainsFunc(req.Query, unicode.IsControl) {
		return out, failure("invalid", "invalid path query")
	}
	var err error
	base, err = expandHome(base)
	if err != nil {
		return out, err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return out, err
	}
	canonical, baseErr := filepath.EvalSymlinks(base)
	if baseErr == nil {
		base = canonical
	} else {
		// An explicit absolute/home query must let users recover when their
		// saved starting folder has moved or is no longer readable.
		expanded, queryErr := expandHome(req.Query)
		if req.Scope != "projects" || queryErr != nil || !filepath.IsAbs(expanded) {
			return out, baseErr
		}
	}
	out.Root = base
	query := req.Query
	var root *os.Root
	if req.Scope == "projects" {
		query, err = expandHome(query)
		if err != nil {
			return out, err
		}
		if !filepath.IsAbs(query) {
			query = filepath.Join(base, query)
		}
		if req.Query == "" || strings.HasSuffix(req.Query, "/") || req.Query == "~" {
			query += "/"
		}
		root, err = os.OpenRoot("/")
		query = strings.TrimPrefix(query, "/")
	} else {
		if !validRelativePath(query) {
			return out, failure("invalid", "file paths must remain relative to the checkout")
		}
		root, err = os.OpenRoot(base)
	}
	if err != nil {
		return out, err
	}
	defer root.Close()
	directory, prefix := filepath.Split(query)
	if directory == "" {
		directory = "."
	}
	// Project registration may browse any user-readable directory, including
	// absolute symlinks. Context files use os.Root for race-safe confinement.
	var dir *os.File
	if req.Scope == "projects" {
		dir, err = os.OpenFile(filepath.Join("/", directory), os.O_RDONLY|unix.O_NONBLOCK|unix.O_DIRECTORY, 0)
	} else {
		dir, err = root.OpenFile(directory, os.O_RDONLY|unix.O_NONBLOCK|unix.O_DIRECTORY, 0)
	}
	if err != nil {
		return out, err
	}
	defer dir.Close()
	var git os.FileInfo
	if req.Scope != "projects" {
		if insideGit(root, directory) {
			return out, failure("invalid", "file paths must remain outside .git")
		}
		git, _ = root.Stat(".git")
	}
	if req.Scope == "projects" {
		out.Directory, err = filepath.EvalSymlinks(filepath.Join("/", directory))
		if err != nil {
			return out, err
		}
	} else {
		out.Directory = filepath.ToSlash(filepath.Clean(directory))
		if out.Directory == "." {
			out.Directory = ""
		}
	}
	for scanned := 0; scanned < browseScanLimit; {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		entries, readErr := dir.ReadDir(64)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			scanned++
			name := entry.Name()
			if strings.EqualFold(name, ".git") || !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) || !strings.HasPrefix(name, prefix) || strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
				continue
			}
			path := filepath.Join(directory, name)
			var info os.FileInfo
			var statErr error
			if req.Scope == "projects" {
				info, statErr = os.Stat(filepath.Join(out.Directory, name))
			} else {
				info, statErr = root.Stat(path)
			}
			if statErr != nil || git != nil && (os.SameFile(info, git) || entry.Type()&os.ModeSymlink != 0 && insideGit(root, path)) || !info.IsDir() && !info.Mode().IsRegular() || req.Scope == "projects" && !info.IsDir() {
				continue
			}
			if len(out.Entries) == browseLimit {
				out.Truncated = true
				break
			}
			if req.Scope == "projects" {
				path = filepath.Join(out.Directory, name)
			}
			path = filepath.ToSlash(path)
			if info.IsDir() {
				path += "/"
			}
			out.Entries = append(out.Entries, protocol.PathEntry{Name: name, Path: path, IsDir: info.IsDir()})
		}
		if out.Truncated {
			break
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return out, readErr
		}
		if scanned >= browseScanLimit {
			out.Truncated = true
		}
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return a.Name < b.Name
	})
	return out, nil
}
