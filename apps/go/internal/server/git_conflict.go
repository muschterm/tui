package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Manual conflict resolution (ADR 0023, S3; wire contract in
// protocol/git_write.go): saved copies of conflicted files, GET
// /v1/git/conflict, and git.conflict_choose, git.conflict_resolve and
// git.conflict_restore.
//
// Saved copies are unreferenced commits, like abort backups: a tree with
// s0/<path> (a resolved entry) and s1/, s2/, s3/<path> (the index stages,
// their own modes and objects), w/<path> (the working-tree content) and a
// manifest blob that records, per path, whether the working tree was
// present, absent or could not be saved, and its permission bits. Git
// objects keep the copy next to the objects it refers to and restorable
// with ordinary Git commands; no new storage table is needed.
//
// Every copy is recorded durably (gitRuntime.recordCopy) before the command
// changes anything, so a crash cannot leave a change without its copy.

const (
	gitConflictReadMax     = 1 << 20
	gitConflictMarkMax     = 8 << 20
	gitConflictCopies      = 300
	gitConflictCopyMax     = 2000
	gitConflictPathCopies  = 100
	gitConflictManifest    = "manifest"
	gitRestorePendingState = "tui-restore-pending"
)

func gitConflictKind(kind string) bool {
	switch kind {
	case protocol.GitKindConflictChoose, protocol.GitKindConflictResolve, protocol.GitKindConflictRestore:
		return true
	}
	return false
}

// indexPin digests one path's index records ("mode oid stage\tpath").
func indexPin(records []string) string {
	h := sha256.New()
	h.Write([]byte("conflict-pin-v1\x00"))
	for _, r := range records {
		h.Write([]byte(r + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// pathEntries reads the index entries of exactly path (not of paths below
// it): the pin, whether it is unmerged, and its entries by stage (0-3).
func pathEntries(ctx context.Context, g *gitReader, p string) (pin string, unmerged bool, entries [4]protocol.GitConflictStage, err error) {
	out, truncated, err := g.read(ctx, 64<<10, "ls-files", "--stage", "-z", "--", p)
	if err != nil || truncated {
		return "", false, entries, failure("unavailable", "the index could not be read")
	}
	var records []string
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, name, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || name != p || len(f) != 3 || len(f[2]) != 1 || f[2][0] < '0' || f[2][0] > '3' {
			continue
		}
		records = append(records, rec)
		entries[f[2][0]-'0'] = protocol.GitConflictStage{Present: true, Mode: f[0], Oid: f[1]}
		unmerged = unmerged || f[2] != "0"
	}
	return indexPin(records), unmerged, entries, nil
}

// pathIndex is pathEntries with the conflict stages 1-3 only.
func pathIndex(ctx context.Context, g *gitReader, p string) (string, bool, [3]protocol.GitConflictStage, error) {
	pin, unmerged, e, err := pathEntries(ctx, g, p)
	return pin, unmerged, [3]protocol.GitConflictStage{e[1], e[2], e[3]}, err
}

// attemptID identifies an attempt of the operation by the file its start
// created (inode and modification time), so retrying never reuses an
// earlier attempt's copies.
func attemptID(gitDir, kind string) string {
	var name string
	switch kind {
	case protocol.GitOperationMerge:
		name = "MERGE_HEAD"
	case protocol.GitOperationRebase:
		name = "rebase-merge/head-name"
		if _, err := os.Stat(filepath.Join(gitDir, "rebase-merge")); err != nil {
			name = "rebase-apply/head-name"
		}
	case protocol.GitOperationCherryPick, protocol.GitOperationRevert:
		name = "sequencer/head"
		if _, err := os.Stat(filepath.Join(gitDir, name)); err != nil {
			name = map[string]string{protocol.GitOperationCherryPick: "CHERRY_PICK_HEAD", protocol.GitOperationRevert: "REVERT_HEAD"}[kind]
		}
	default:
		return ""
	}
	fi, err := os.Lstat(filepath.Join(gitDir, name))
	if err != nil {
		return ""
	}
	id := fmt.Sprintf("%d", fi.ModTime().UnixNano())
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		id += fmt.Sprintf(":%d", st.Ino)
	}
	return id
}

// stopKey identifies one stop of one attempt of an operation.
func stopKey(st protocol.GitOperationState) string {
	h := sha256.New()
	cur, target := "", ""
	if st.Current != nil {
		cur = st.Current.Oid
	}
	if st.Target != nil {
		target = st.Target.Oid
	}
	fmt.Fprintf(h, "stop-v2\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s", st.Kind, target, st.OrigHead, cur, st.Step, st.HeadOid, st.Attempt)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// conflictCopiesLocked lists the copies of a stop, oldest first.
func (e *engine) conflictCopiesLocked(top, key string) []protocol.GitConflictCopy {
	k := checkoutKey(top)
	var list []protocol.GitConflictCopy
	for _, c := range e.snap.GitConflictCopies {
		if c.CheckoutKey == k && c.StopKey == key {
			list = append(list, c)
		}
	}
	return list
}

// conflictCopyLocked is the stop's first copy.
func (e *engine) conflictCopyLocked(top, key string) *protocol.GitConflictCopy {
	if list := e.conflictCopiesLocked(top, key); len(list) > 0 {
		return &list[0]
	}
	return nil
}

// putConflictCopy records a copy and returns the copies it evicted.
//
// A stop keeps one at_stop copy and one before_first_change copy per path;
// before_overwrite copies are deduplicated by content (ContentOid) per
// path, so each kept copy holds content no other copy of that path holds,
// and at most gitConflictPathCopies are kept per path (the oldest go
// first). A repository keeps gitConflictCopies and all repositories
// gitConflictCopyMax, evicting copies of other (ended or other
// repositories') stops, oldest first; the stop being written is never
// evicted by those caps.
func putConflictCopy(s *protocol.Snapshot, c protocol.GitConflictCopy) (evicted []protocol.GitConflictCopy) {
	for _, old := range s.GitConflictCopies {
		if old.CheckoutKey != c.CheckoutKey || old.StopKey != c.StopKey || old.Path != c.Path || old.Reason != c.Reason {
			continue
		}
		switch {
		case c.Reason == protocol.GitCopyBeforeJob:
			// Every job start keeps its own pre-job copy.
		case c.Reason != protocol.GitCopyBeforeOverwrite || (c.ContentOid != "" && old.ContentOid == c.ContentOid):
			return nil
		}
	}
	s.GitConflictCopies = append(s.GitConflictCopies, c)
	evict := func(match func(protocol.GitConflictCopy) bool, over int) {
		for i := 0; over > 0 && i < len(s.GitConflictCopies); {
			if match(s.GitConflictCopies[i]) {
				evicted = append(evicted, s.GitConflictCopies[i])
				s.GitConflictCopies = append(s.GitConflictCopies[:i], s.GitConflictCopies[i+1:]...)
				over--
				continue
			}
			i++
		}
	}
	count := func(match func(protocol.GitConflictCopy) bool) int {
		n := 0
		for _, o := range s.GitConflictCopies {
			if match(o) {
				n++
			}
		}
		return n
	}
	samePath := func(o protocol.GitConflictCopy) bool {
		return o.CheckoutKey == c.CheckoutKey && o.StopKey == c.StopKey && o.Reason == protocol.GitCopyBeforeOverwrite && o.Path == c.Path
	}
	if c.Reason == protocol.GitCopyBeforeOverwrite {
		evict(samePath, count(samePath)-gitConflictPathCopies)
	}
	otherStopHere := func(o protocol.GitConflictCopy) bool { return o.CheckoutKey == c.CheckoutKey && o.StopKey != c.StopKey }
	evict(otherStopHere, count(func(o protocol.GitConflictCopy) bool { return o.CheckoutKey == c.CheckoutKey })-gitConflictCopies)
	otherStop := func(o protocol.GitConflictCopy) bool { return o.CheckoutKey != c.CheckoutKey || o.StopKey != c.StopKey }
	evict(otherStop, len(s.GitConflictCopies)-gitConflictCopyMax)
	return evicted
}

// pruneConflictCopies drops copies of repositories no project or thread
// uses any more.
func pruneConflictCopies(s *protocol.Snapshot) {
	kept := s.GitConflictCopies[:0]
	for _, c := range s.GitConflictCopies {
		used := false
		for _, p := range s.Projects {
			used = used || pathsOverlap(p.Path, c.Checkout)
		}
		for _, t := range s.Threads {
			used = used || pathsOverlap(t.Checkout, c.Checkout)
		}
		if used {
			kept = append(kept, c)
		}
	}
	s.GitConflictCopies = kept
	if len(s.GitConflictCopies) == 0 {
		s.GitConflictCopies = nil
	}
}

// copyManifest is the manifest blob of a saved copy: per path (base64 of its
// bytes) "present", "absent" or "missing" (could not be saved), and the
// working-tree permission bits of present regular files.
type copyManifest struct {
	Version int               `json:"version"`
	Paths   map[string]string `json:"paths"`
	Perms   map[string]uint32 `json:"perms,omitempty"`
}

// copyItem is one path of a copy: its index entries by stage (0-3).
type copyItem struct {
	path    string
	entries [4]protocol.GitConflictStage
}

// writeCopy saves items (index entries and working-tree content, at most
// gitBackupBytesMax of content in total) into an unreferenced commit.
// Nothing in the index, working tree or refs changes. Paths whose content
// could not be saved are listed in Missing.
func (w *gitWriter) writeCopy(ctx context.Context, st protocol.GitOperationState, reason, onePath string, items []copyItem) (*protocol.GitConflictCopy, string, error) {
	c := &protocol.GitConflictCopy{Reason: reason, Path: onePath, Checkout: w.top, CheckoutKey: checkoutKey(w.top), StopKey: stopKey(st), CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	var entries bytes.Buffer
	manifest := copyManifest{Version: 2, Paths: map[string]string{}, Perms: map[string]uint32{}}
	var total int64
	lastOid := ""
	for _, it := range items {
		p := it.path
		key := base64.StdEncoding.EncodeToString([]byte(p))
		for i, e := range it.entries {
			if e.Present {
				fmt.Fprintf(&entries, "%s %s\ts%d/%s\x00", e.Mode, e.Oid, i, p)
			}
		}
		tok := worktreeStat(w.top, p)
		switch tokenKind(tok) {
		case tokenAbsent:
			manifest.Paths[key] = "absent"
			continue
		case "reg", "lnk":
		default:
			manifest.Paths[key] = "missing"
			c.Missing = append(c.Missing, p)
			continue
		}
		mode, perm, data, err := readWorktreeFile(w.top, p, tok, gitBackupBytesMax-total)
		if err != nil {
			manifest.Paths[key] = "missing"
			c.Missing = append(c.Missing, p)
			continue
		}
		out, _, err := w.run(ctx, bytes.NewReader(data), false, "hash-object", "-w", "--no-filters", "--stdin")
		oid := strings.TrimSpace(string(out))
		if err != nil || !gitFullHash.MatchString(oid) {
			return nil, "", failure("unavailable", "a conflicted file could not be saved")
		}
		total += int64(len(data))
		manifest.Paths[key] = "present"
		if mode != "120000" {
			manifest.Perms[key] = uint32(perm)
		}
		lastOid = oid
		fmt.Fprintf(&entries, "%s %s\tw/%s\x00", mode, oid, p)
	}
	raw, _ := json.Marshal(manifest)
	out, _, err := w.run(ctx, bytes.NewReader(raw), false, "hash-object", "-w", "--no-filters", "--stdin")
	manifestOid := strings.TrimSpace(string(out))
	if err != nil || !gitFullHash.MatchString(manifestOid) {
		return nil, "", failure("unavailable", "the saved copy could not be written")
	}
	fmt.Fprintf(&entries, "100644 %s\t%s\x00", manifestOid, gitConflictManifest)
	tree, err := w.writeTree(ctx, &entries)
	if err != nil {
		return nil, "", failure("unavailable", "the saved copy could not be written: "+err.Error())
	}
	if c.CopyID, err = w.commitTree(ctx, tree, "Saved conflicted files of the "+st.Kind+" ("+reason+")"); err != nil {
		return nil, "", failure("unavailable", "the saved copy could not be written: "+err.Error())
	}
	if onePath != "" {
		c.ContentOid = lastOid
	}
	return c, lastOid, nil
}

// saveConflictCopy saves every unmerged path of st.
func (w *gitWriter) saveConflictCopy(ctx context.Context, st protocol.GitOperationState) (*protocol.GitConflictCopy, error) {
	if st.ConflictsTruncated {
		return nil, failure("not_supported", "too many conflicted paths to save a copy; use a terminal")
	}
	items := make([]copyItem, 0, len(st.Conflicts))
	for _, c := range st.Conflicts {
		it := copyItem{path: c.Path}
		copy(it.entries[1:], c.Stages[:])
		items = append(items, it)
	}
	c, _, err := w.writeCopy(ctx, st, protocol.GitCopyAtStop, "", items)
	return c, err
}

// savePathCopy saves one path's current index entries and content.
func (w *gitWriter) savePathCopy(ctx context.Context, g *gitReader, st protocol.GitOperationState, p, reason string) (*protocol.GitConflictCopy, string, error) {
	_, _, entries, err := pathEntries(ctx, g, p)
	if err != nil {
		return nil, "", err
	}
	return w.writeCopy(ctx, st, reason, p, []copyItem{{path: p, entries: entries}})
}

// readWorktreeFile reads a regular file or symlink whose token is tok, at
// most limit bytes, with its Git mode and permission bits.
func readWorktreeFile(top, p, tok string, limit int64) (string, fs.FileMode, []byte, error) {
	content, err := openWorktreeContent(top, p, tok)
	if err != nil {
		return "", 0, nil, err
	}
	if content.symlink {
		return "120000", 0, content.link, nil
	}
	defer content.file.Close()
	data, err := io.ReadAll(io.LimitReader(content.file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return "", 0, nil, errors.New("file too large")
	}
	mode, perm := "100644", fs.FileMode(0o644)
	if fi, err := content.file.Stat(); err == nil {
		perm = fi.Mode().Perm()
		if perm&0o111 != 0 {
			mode = "100755"
		}
	}
	return mode, perm, data, nil
}

// copyEntry reads one entry of a saved copy: its mode and object, or
// present false.
func copyEntry(ctx context.Context, g *gitReader, copyID, name string) (mode, oid string, present bool) {
	out, _, err := g.read(ctx, 64<<10, "ls-tree", "-z", "--full-tree", copyID, "--", name)
	if err != nil {
		return "", "", false
	}
	meta, entry, ok := strings.Cut(strings.TrimSuffix(string(out), "\x00"), "\t")
	f := strings.Fields(meta)
	if !ok || entry != name || len(f) != 3 {
		return "", "", false
	}
	return f[0], f[2], true
}

// copyManifestOf reads a copy's manifest.
func copyManifestOf(ctx context.Context, g *gitReader, copyID string) (copyManifest, bool) {
	var m copyManifest
	_, oid, ok := copyEntry(ctx, g, copyID, gitConflictManifest)
	if !ok {
		return m, false
	}
	raw, truncated, err := g.read(ctx, 16<<20, "cat-file", "blob", oid)
	if err != nil || truncated || json.Unmarshal(raw, &m) != nil {
		return m, false
	}
	return m, true
}

// copyWorktreeState reads the manifest's record for p: present, absent,
// missing, or "" when the copy does not hold p.
func copyWorktreeState(ctx context.Context, g *gitReader, copyID, p string) string {
	m, ok := copyManifestOf(ctx, g, copyID)
	if !ok {
		return ""
	}
	return m.Paths[base64.StdEncoding.EncodeToString([]byte(p))]
}

// pathCopies lists the copies of a stop that hold p: the original first
// (at_stop or before_first_change), then before_overwrite copies.
func pathCopies(ctx context.Context, g *gitReader, copies []protocol.GitConflictCopy, p string) (original *protocol.GitConflictCopy, all []protocol.GitConflictCopy) {
	for _, c := range copies {
		if c.Path != "" && c.Path != p {
			continue
		}
		if copyWorktreeState(ctx, g, c.CopyID, p) == "" {
			continue
		}
		all = append(all, c)
		if original == nil && (c.Reason == protocol.GitCopyAtStop || c.Reason == protocol.GitCopyBeforeFirstChange) {
			copied := c
			original = &copied
		}
	}
	return original, all
}

// ---- GET /v1/git/conflict ----

func (e *engine) gitConflict(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	e.gitHandle(w, r, func(ctx context.Context, dir string) (any, error) {
		return readConflictFileCopy(ctx, dir, q.Get("path"), q.Get("version"), q.Get("copy_id"), func(top, key string) []protocol.GitConflictCopy {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.conflictCopiesLocked(top, key)
		})
	})
}

// readConflictFile is readConflictFileCopy for the path's original copy,
// with a lookup of the stop's first copy (tests).
func readConflictFile(ctx context.Context, dir, p, version string, lookup func(top, key string) *protocol.GitConflictCopy) (protocol.GitConflictFile, error) {
	return readConflictFileCopy(ctx, dir, p, version, "", func(top, key string) []protocol.GitConflictCopy {
		if c := lookup(top, key); c != nil {
			return []protocol.GitConflictCopy{*c}
		}
		return nil
	})
}

func readConflictFileCopy(ctx context.Context, dir, p, version, copyID string, copies func(top, key string) []protocol.GitConflictCopy) (protocol.GitConflictFile, error) {
	f := protocol.GitConflictFile{Path: p, Version: version}
	stage := -1
	switch version {
	case protocol.GitConflictVersionBase:
		stage = 1
	case protocol.GitConflictVersionOurs:
		stage = 2
	case protocol.GitConflictVersionTheirs:
		stage = 3
	case protocol.GitConflictVersionWorking, protocol.GitConflictVersionSaved:
	default:
		return f, failure("invalid", "version must be base, ours, theirs, working or saved")
	}
	if !validGitPath(p) || (copyID != "" && (version != protocol.GitConflictVersionSaved || !gitFullHash.MatchString(copyID))) {
		return f, failure("invalid", "path must be a relative checkout path outside .git; copy_id applies to version saved")
	}
	if !gitReadable(inspectWorkspace(ctx, dir)) {
		return f, errGitNotRepository
	}
	g, err := newGitReader(ctx, dir)
	if err != nil {
		return f, err
	}
	gitDir, _, err := g.read(ctx, 4096, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return f, failure("unavailable", "Git directory could not be resolved")
	}
	st, _, err := observeOperationMode(ctx, g, strings.TrimSpace(string(gitDir)), false)
	if err != nil {
		return f, err
	}
	if !managedOperation(st.Kind) {
		return f, failure("no_operation", "no merge, rebase, cherry-pick or revert is in progress")
	}
	pin, unmerged, stages, err := pathIndex(ctx, g, p)
	if err != nil {
		return f, err
	}
	original, all := pathCopies(ctx, g, copies(g.dir, stopKey(st)), p)
	if !unmerged && original == nil {
		return f, failure("not_found", "the path is not conflicted in the operation in progress and has no saved copy")
	}
	if original != nil {
		f.CopyID, f.CopyReason, f.CopyCreatedAt = original.CopyID, original.Reason, original.CreatedAt
	}
	for _, c := range all {
		f.Copies = append(f.Copies, protocol.GitConflictCopyRef{CopyID: c.CopyID, Reason: c.Reason, CreatedAt: c.CreatedAt})
	}
	f.Unmerged, f.ConflictPin = unmerged, pin
	tok := worktreeStat(g.dir, p)
	f.WorktreeToken = tok
	blob := func(mode, oid string) error {
		f.Present, f.Mode, f.Oid, f.Kind = true, mode, oid, "file"
		if mode == "120000" {
			f.Symlink, f.Kind = true, "symlink"
		}
		if mode == "160000" {
			f.Kind = "submodule"
			return nil // a submodule commit has no content here
		}
		size, _, err := g.read(ctx, 64, "cat-file", "-s", oid)
		if err != nil {
			return failure("unavailable", "the object could not be read")
		}
		fmt.Sscan(strings.TrimSpace(string(size)), &f.Size)
		data, truncated, err := g.read(ctx, gitConflictReadMax, "cat-file", "blob", oid)
		if err != nil {
			return failure("unavailable", "the object could not be read")
		}
		f.Content, f.Truncated = data, truncated
		f.Binary = bytes.IndexByte(data[:min(len(data), gitBinarySniff)], 0) >= 0
		return nil
	}
	f.Kind = "absent"
	switch {
	case stage > 0 && unmerged:
		if s := stages[stage-1]; s.Present {
			err = blob(s.Mode, s.Oid)
		}
	case stage > 0:
		if mode, oid, ok := copyEntry(ctx, g, original.CopyID, fmt.Sprintf("s%d/%s", stage, p)); ok {
			err = blob(mode, oid)
		}
	case version == protocol.GitConflictVersionSaved:
		from := original
		if copyID != "" {
			from = nil
			for i := range all {
				if all[i].CopyID == copyID {
					from = &all[i]
				}
			}
		}
		if from == nil {
			return f, failure("not_found", "no saved copy of this path")
		}
		f.CopyID, f.CopyReason, f.CopyCreatedAt = from.CopyID, from.Reason, from.CreatedAt
		switch copyWorktreeState(ctx, g, from.CopyID, p) {
		case "missing":
			return f, failure("not_found", "the saved copy could not hold this file")
		case "present":
			if mode, oid, ok := copyEntry(ctx, g, from.CopyID, "w/"+p); ok {
				err = blob(mode, oid)
			}
		}
	default:
		switch tokenKind(tok) {
		case tokenAbsent:
		case "reg", "lnk":
			f.Present, f.Kind = true, "file"
			mode, _, data, rerr := readWorktreeFile(g.dir, p, tok, gitConflictMarkMax)
			if rerr != nil {
				f.MarkersUnknown, f.Truncated = true, true
				if fi, err := os.Lstat(filepath.Join(g.dir, filepath.FromSlash(p))); err == nil {
					f.Size = fi.Size()
				}
				break
			}
			f.Mode, f.Size, f.Symlink = mode, int64(len(data)), mode == "120000"
			if oid, err := hashContent(ctx, g, data); err == nil {
				f.Oid = oid
			}
			if f.Symlink {
				f.Kind = "symlink"
			}
			f.Content, f.Truncated = data[:min(len(data), gitConflictReadMax)], len(data) > gitConflictReadMax
			f.Binary = bytes.IndexByte(data[:min(len(data), gitBinarySniff)], 0) >= 0
			if !f.Symlink && !f.Binary {
				f.HasMarkers = hasConflictMarker(data, conflictMarkerSizes(ctx, g, []string{p}, func(s string) string { return s })[p])
			}
		case "dir":
			f.Kind = "directory"
		default:
			f.Kind = "other"
		}
	}
	return f, err
}

// ---- Commands ----

func validateGitConflictWrite(kind string, w *protocol.GitWrite) error {
	c := w.Conflict
	if c == nil || w.Integrate != nil || w.Operation != nil || w.Ref != nil || w.Sync != nil || w.Cancel != nil ||
		len(w.Paths) != 0 || w.Message != "" || w.Amend || w.ExpectedHead != "" || w.StagedFingerprint != "" || w.AcknowledgePublished || w.Confirmed {
		return failure("invalid", kind+" carries its payload in Git.Conflict only")
	}
	if !validGitPath(c.Path) || c.ConflictPin == "" || c.WorktreeToken == "" {
		return failure("invalid", kind+" needs a checkout path, its conflict_pin and worktree_token")
	}
	switch kind {
	case protocol.GitKindConflictChoose:
		if c.Side != protocol.GitConflictSideOurs && c.Side != protocol.GitConflictSideTheirs && c.Side != protocol.GitConflictSideBase {
			return failure("invalid", "side must be ours, theirs or base")
		}
		if c.As != "" || c.CopyID != "" || c.AcknowledgeMarkers != "" || c.AcknowledgeBinary != "" {
			return failure("invalid", "choose takes a side (and acknowledge_unsaved) only")
		}
	case protocol.GitKindConflictResolve:
		if c.As != protocol.GitConflictAsContent && c.As != protocol.GitConflictAsDeleted && c.As != protocol.GitConflictAsKeepStaged {
			return failure("invalid", "as must be content, deleted or keep_staged")
		}
		if c.Side != "" || c.CopyID != "" || c.AcknowledgeUnsaved != "" || (c.As != protocol.GitConflictAsContent && (c.AcknowledgeMarkers != "" || c.AcknowledgeBinary != "")) {
			return failure("invalid", "resolve takes as (and marker or binary acknowledgements for content) only")
		}
	case protocol.GitKindConflictRestore:
		if !gitFullHash.MatchString(c.CopyID) || c.Side != "" || c.As != "" || c.AcknowledgeMarkers != "" || c.AcknowledgeBinary != "" {
			return failure("invalid", "restore takes a copy_id (and acknowledge_unsaved) only")
		}
	}
	return nil
}

// stopCopies lists the copies of st's stop through the engine.
func (w *gitWriter) stopCopies(st protocol.GitOperationState) []protocol.GitConflictCopy {
	if w.copyLookup == nil {
		return nil
	}
	return w.copyLookup(stopKey(st))
}

// recordCopy records a copy durably before anything changes, or, without an
// engine runtime, in phase two.
func recordCopy(ctx context.Context, plan *gitPlan, c *protocol.GitConflictCopy, op *protocol.GitOperationResult) error {
	if rt := plan.runtime(ctx); rt.recordCopy != nil {
		evicted, err := rt.recordCopy(*c)
		for _, e := range evicted {
			if e.StopKey == c.StopKey && e.CheckoutKey == c.CheckoutKey {
				op.Evicted = append(op.Evicted, protocol.GitConflictCopyRef{CopyID: e.CopyID, Reason: e.Reason, CreatedAt: e.CreatedAt})
			}
		}
		return err
	}
	plan.copies = append(plan.copies, *c)
	return nil
}

// ensureConflictCopy saves the conflicted files of st unless its stop
// already has a copy.
func (w *gitWriter) ensureConflictCopy(ctx context.Context, plan *gitPlan, st protocol.GitOperationState, op *protocol.GitOperationResult) error {
	if len(st.Conflicts) == 0 || !managedOperation(st.Kind) || len(w.stopCopies(st)) > 0 {
		return nil
	}
	c, err := w.saveConflictCopy(ctx, st)
	if err != nil {
		return err
	}
	if err := recordCopy(ctx, plan, c, op); err != nil {
		return err
	}
	op.ConflictCopy = c.CopyID
	return nil
}

// ensurePathCopy makes sure p has an original copy in this stop: the
// stop's copy, or a before_first_change copy of p.
func (w *gitWriter) ensurePathCopy(ctx context.Context, g *gitReader, plan *gitPlan, st protocol.GitOperationState, p string, op *protocol.GitOperationResult) (*protocol.GitConflictCopy, error) {
	if err := w.ensureConflictCopy(ctx, plan, st, op); err != nil {
		return nil, err
	}
	if original, _ := pathCopies(ctx, g, w.stopCopies(st), p); original != nil {
		return original, nil
	}
	if pc := plan.pendingCopy(p); pc != nil {
		return pc, nil
	}
	c, _, err := w.savePathCopy(ctx, g, st, p, protocol.GitCopyBeforeFirstChange)
	if err != nil {
		return nil, err
	}
	if err := recordCopy(ctx, plan, c, op); err != nil {
		return nil, err
	}
	if op.ConflictCopy == "" {
		op.ConflictCopy = c.CopyID
	}
	return c, nil
}

// pendingCopy finds a copy of p made in this plan but not yet recorded.
func (p *gitPlan) pendingCopy(path string) *protocol.GitConflictCopy {
	for i := range p.copies {
		if p.copies[i].Path == path || p.copies[i].Path == "" {
			return &p.copies[i]
		}
	}
	return nil
}

// copyable reports whether the working-tree file p (token tok) can be
// copied before it is overwritten.
func copyable(top, p, tok string) bool {
	switch tokenKind(tok) {
	case tokenAbsent, "lnk":
		return true
	case "reg":
		fi, err := os.Lstat(filepath.Join(top, filepath.FromSlash(p)))
		return err == nil && fi.Size() <= gitBackupBytesMax
	}
	return false
}

// snapshotBeforeOverwrite copies p's working-tree content when it differs
// from what the original copy holds and from the content about to be
// written (sameAs, a blob object ID, may be empty).
func (w *gitWriter) snapshotBeforeOverwrite(ctx context.Context, g *gitReader, plan *gitPlan, st protocol.GitOperationState, original *protocol.GitConflictCopy, p, sameAs string, op *protocol.GitOperationResult) error {
	tok := worktreeStat(w.top, p)
	if tokenKind(tok) == tokenAbsent {
		return nil
	}
	_, _, data, err := readWorktreeFile(w.top, p, tok, gitBackupBytesMax)
	if err != nil {
		return err
	}
	out, _, err := w.run(ctx, bytes.NewReader(data), false, "hash-object", "--no-filters", "--stdin")
	current := strings.TrimSpace(string(out))
	if err != nil || !gitFullHash.MatchString(current) {
		return failure("unavailable", "the file could not be read before it is replaced")
	}
	// Content that is recoverable elsewhere is not copied again: the
	// content about to be written, any index stage of the path, the
	// original copy, or an earlier copy of the path (then Previous names
	// that copy).
	if current == sameAs {
		return nil
	}
	if _, _, entries, err := pathEntries(ctx, g, p); err == nil {
		for _, e := range entries {
			if e.Present && e.Oid == current {
				return nil
			}
		}
	}
	if original != nil {
		if _, oid, ok := copyEntry(ctx, g, original.CopyID, "w/"+p); ok && oid == current {
			return nil
		}
	}
	for _, c := range w.stopCopies(st) {
		if c.Path == p && c.Reason == protocol.GitCopyBeforeOverwrite && c.ContentOid == current {
			op.Previous = &protocol.GitConflictPrevious{CopyID: c.CopyID, Oid: current}
			return nil
		}
	}
	c, oid, err := w.savePathCopy(ctx, g, st, p, protocol.GitCopyBeforeOverwrite)
	if err != nil {
		return err
	}
	if len(c.Missing) > 0 {
		return failure("unsaved_unacknowledged", "the file could not be copied before it is replaced")
	}
	if err := recordCopy(ctx, plan, c, op); err != nil {
		return err
	}
	op.Previous = &protocol.GitConflictPrevious{CopyID: c.CopyID, Oid: oid}
	return nil
}

// cleanupRestoreTemps removes a temporary file a restore interrupted by a
// crash left behind (its name is kept in the Git directory while it exists).
func (w *gitWriter) cleanupRestoreTemps() {
	pending := filepath.Join(w.gitDir, gitRestorePendingState)
	if name := strings.TrimSpace(readStateFile(w.gitDir, gitRestorePendingState)); name != "" && strings.HasPrefix(filepath.Base(name), ".tui-restore-") && strings.HasPrefix(name, w.top+string(filepath.Separator)) {
		if fi, err := os.Lstat(name); err == nil && !fi.IsDir() {
			_ = os.Remove(name)
		}
	}
	_ = os.Remove(pending)
}

func prepareConflict(ctx context.Context, g *gitReader, w *gitWriter, c protocol.Command) (*gitPlan, error) {
	req := *c.Git.Conflict
	st, _, err := observeOperationMode(ctx, g, w.gitDir, false)
	if err != nil {
		return nil, err
	}
	if !managedOperation(st.Kind) {
		return nil, failure("no_operation", "no merge, rebase, cherry-pick or revert is in progress")
	}
	pin, unmerged, stages, err := pathIndex(ctx, g, req.Path)
	if err != nil {
		return nil, err
	}
	tok := worktreeStat(w.top, req.Path)
	if pin != req.ConflictPin || tok != req.WorktreeToken {
		return nil, failure("stale_entry", "the path changed since it was shown; refresh and review it again")
	}
	for _, s := range stages {
		if s.Mode == "160000" {
			return nil, failure("not_supported", "submodule conflicts are resolved in a terminal")
		}
	}
	switch tokenKind(tok) {
	case "dir":
		return nil, failure("not_supported", "the path is a directory in the working tree; resolve it in a terminal")
	case tokenAbsent, "reg", "lnk":
	default:
		return nil, failure("not_supported", "only regular files and symlinks can be resolved here")
	}
	var restoreFrom *protocol.GitConflictCopy
	switch c.Kind {
	case protocol.GitKindConflictChoose, protocol.GitKindConflictResolve:
		keep := c.Kind == protocol.GitKindConflictResolve && req.As == protocol.GitConflictAsKeepStaged
		if keep && unmerged {
			return nil, failure("conflicted", "the path is still unmerged; there is no staged resolution to keep")
		}
		if !keep && !unmerged {
			return nil, failure("not_conflicted", "the path is not unmerged")
		}
	case protocol.GitKindConflictRestore:
		_, all := pathCopies(ctx, g, w.stopCopies(st), req.Path)
		for i := range all {
			if all[i].CopyID == req.CopyID {
				restoreFrom = &all[i]
			}
		}
		if restoreFrom == nil {
			return nil, failure("no_saved_copy", "that saved copy does not belong to the operation's current stop or does not hold this path; refresh")
		}
		if copyWorktreeState(ctx, g, restoreFrom.CopyID, req.Path) == "missing" {
			return nil, failure("not_supported", "the saved copy could not hold this file; restore it in a terminal")
		}
	}
	if c.Kind != protocol.GitKindConflictResolve && !copyable(w.top, req.Path, tok) && req.AcknowledgeUnsaved != tok {
		return nil, failure("unsaved_unacknowledged", "the file cannot be copied before it is replaced (larger than 64 MiB); confirm replacing it without a copy")
	}
	if c.Kind == protocol.GitKindConflictResolve && req.As == protocol.GitConflictAsContent {
		if err := checkResolveContent(ctx, g, w.top, req, tok); err != nil {
			return nil, err
		}
	}
	if err := w.checkLocks(false); err != nil {
		return nil, err
	}
	p := &gitPlan{paths: []string{req.Path}}
	var decided string // the path's index entries after the command
	p.journal = conflictJournal(w.top, c, func() string { return decided })
	p.run = func(ctx context.Context) (res protocol.GitResult) {
		op := &protocol.GitOperationResult{Kind: st.Kind, HeadBefore: st.HeadOid, Outcome: protocol.GitOutcomeUnchanged}
		defer func() { res.Operation = op }()
		w.cleanupRestoreTemps()
		end, refused := beginWorktreeRewrite(ctx, p.runtime(ctx), w.top)
		defer end()
		if refused != nil {
			res = *refused
			return res
		}
		fail := func(err error) protocol.GitResult {
			var pe *protocol.Error
			code, msg := "unavailable", err.Error()
			if errors.As(err, &pe) {
				code, msg = pe.Code, pe.Message
			}
			return gitResult(protocol.GitStateFailed, code, msg+"; nothing was changed", nil)
		}
		// Last look after open documents were saved: a saved edit is a
		// change the user has not reviewed.
		again, err := w.observeLight(ctx, g)
		if err != nil || stopKey(again) != stopKey(st) {
			res = gitResult(protocol.GitStateFailed, "stale_operation", "the operation changed after review; nothing was changed", nil)
			return res
		}
		stale := func() bool {
			pin2, _, _, err := pathIndex(ctx, g, req.Path)
			return err != nil || pin2 != req.ConflictPin || worktreeStat(w.top, req.Path) != req.WorktreeToken
		}
		if stale() {
			res = gitResult(protocol.GitStateFailed, "stale_entry", "the path changed after review (open documents were saved); nothing was changed; review it again", nil)
			return res
		}
		// Copies first, recorded durably: the stop's conflicts, this path's
		// original state, and whatever is about to be overwritten.
		original, err := w.ensurePathCopy(ctx, g, p, again, req.Path, op)
		if err != nil {
			res = fail(err)
			return res
		}
		if c.Kind != protocol.GitKindConflictResolve && copyable(w.top, req.Path, tok) {
			sameAs := ""
			if c.Kind == protocol.GitKindConflictChoose {
				n := map[string]int{protocol.GitConflictSideBase: 1, protocol.GitConflictSideOurs: 2, protocol.GitConflictSideTheirs: 3}[req.Side]
				sameAs = stages[n-1].Oid
			}
			if err := w.snapshotBeforeOverwrite(ctx, g, p, again, original, req.Path, sameAs, op); err != nil {
				res = fail(err)
				return res
			}
		}
		// The final look immediately before Git writes.
		if stale() {
			res = gitResult(protocol.GitStateFailed, "stale_entry", "the path changed while it was being copied; nothing was changed; review it again", nil)
			return res
		}
		switch c.Kind {
		case protocol.GitKindConflictChoose:
			res = chooseSide(ctx, w, req, stages, tok)
		case protocol.GitKindConflictResolve:
			if req.As == protocol.GitConflictAsKeepStaged {
				res = gitResult(protocol.GitStateSucceeded, "", "Kept the staged resolution of "+req.Path+" as reviewed", nil)
				break
			}
			res = resolvePath(ctx, g, w, req, tok)
		default:
			res = restorePath(ctx, g, w, req, restoreFrom.CopyID, tok)
		}
		vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitRequestBudget)
		defer cancel()
		if after, err := w.observeLight(vctx, g); err == nil && after.Kind != "" {
			copied := after
			op.State, op.HeadAfter = &copied, after.HeadOid
			if res.State == protocol.GitStateSucceeded {
				op.Outcome = protocol.GitOutcomeStopped
				if after.UnmergedFingerprint != "" {
					op.Outcome = protocol.GitOutcomeStoppedConflicts
				}
			}
		}
		if res.State == protocol.GitStateSucceeded {
			if out, truncated, err := g.read(vctx, 64<<10, "ls-files", "--stage", "-z", "--", req.Path); err == nil && !truncated {
				decided = stageMap(out)[req.Path]
			}
		}
		return res
	}
	return p, nil
}

// observeLight observes the operation and its conflicts only.
func (w *gitWriter) observeLight(ctx context.Context, g *gitReader) (protocol.GitOperationState, error) {
	st, _, err := observeOperationMode(ctx, g, w.gitDir, false)
	return st, err
}

// checkResolveContent refuses staging a file that still has conflict
// markers (or is too large to check) unless AcknowledgeMarkers names the
// reviewed working-tree token, and a binary file (markers cannot be
// checked) unless AcknowledgeBinary does.
func checkResolveContent(ctx context.Context, g *gitReader, top string, req protocol.GitConflictWrite, tok string) error {
	switch tokenKind(tok) {
	case tokenAbsent:
		return failure("invalid", "the file does not exist; resolve it as deleted")
	case "lnk":
		return nil
	}
	_, _, data, err := readWorktreeFile(top, req.Path, tok, gitConflictMarkMax)
	if err == nil && bytes.IndexByte(data[:min(len(data), gitBinarySniff)], 0) >= 0 {
		if req.AcknowledgeBinary != tok {
			return failure("binary_unacknowledged", "the file is binary, so it cannot be checked for conflict markers; confirm staging it as it is")
		}
		return nil
	}
	markers := err != nil
	if err == nil {
		markers = hasConflictMarker(data, conflictMarkerSizes(ctx, g, []string{req.Path}, func(s string) string { return s })[req.Path])
	}
	if markers && req.AcknowledgeMarkers != tok {
		return failure("markers_unacknowledged", "the file still has conflict-marker lines (or is too large to check); confirm staging it as it is")
	}
	return nil
}

func chooseSide(ctx context.Context, w *gitWriter, req protocol.GitConflictWrite, stages [3]protocol.GitConflictStage, tok string) protocol.GitResult {
	n := map[string]int{protocol.GitConflictSideBase: 1, protocol.GitConflictSideOurs: 2, protocol.GitConflictSideTheirs: 3}[req.Side]
	if !stages[n-1].Present {
		// That side deleted the file: choosing it removes the file.
		if tokenKind(tok) == tokenAbsent {
			return gitResult(protocol.GitStateSucceeded, "", "Chose "+req.Side+" (deleted) for "+req.Path, nil)
		}
		if err := removeUntracked(w.top, req.Path, tok); err != nil {
			var pe *protocol.Error
			errors.As(err, &pe)
			return gitResult(protocol.GitStateFailed, pe.Code, pe.Message, nil)
		}
		return gitResult(protocol.GitStateSucceeded, "", "Chose "+req.Side+" (deleted) for "+req.Path+"; the file was removed", nil)
	}
	_, output, err := w.run(ctx, nil, true, "checkout-index", "-f", fmt.Sprintf("--stage=%d", n), "--", req.Path)
	if err != nil {
		return failedGit("git_failed", "Git could not write that side", output)
	}
	return gitResult(protocol.GitStateSucceeded, "", "Chose "+req.Side+" for "+req.Path+"; it is still unmerged until resolved", output)
}

func resolvePath(ctx context.Context, g *gitReader, w *gitWriter, req protocol.GitConflictWrite, tok string) protocol.GitResult {
	args := []string{"add", "--", req.Path}
	if req.As == protocol.GitConflictAsDeleted {
		args = []string{"rm", "--cached", "--quiet", "--", req.Path}
	}
	_, output, err := w.run(ctx, nil, true, args...)
	if err != nil {
		return failedGit("git_failed", "Git could not resolve the path", output)
	}
	if _, unmerged, _, err := pathIndex(ctx, g, req.Path); err != nil || unmerged {
		return gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "Git reported success but the path is still unmerged; refresh", output)
	}
	if req.As == protocol.GitConflictAsDeleted {
		return gitResult(protocol.GitStateSucceeded, "", "Resolved "+req.Path+" as deleted; the file stays in the working tree, untracked", output)
	}
	res := gitResult(protocol.GitStateSucceeded, "", "Resolved "+req.Path, output)
	if worktreeStat(w.top, req.Path) != tok {
		res.Code, res.Message = "staged_newer_content", "Resolved "+req.Path+", but the file changed while it was staged; review what was staged"
	}
	return res
}

// restorePath puts a copy's working-tree content back atomically and its
// index entries into the index.
func restorePath(ctx context.Context, g *gitReader, w *gitWriter, req protocol.GitConflictWrite, copyID, tok string) protocol.GitResult {
	var info bytes.Buffer
	zero := ""
	for i := 0; i <= 3; i++ {
		mode, oid, ok := copyEntry(ctx, g, copyID, fmt.Sprintf("s%d/%s", i, req.Path))
		if !ok {
			continue
		}
		if _, _, err := g.read(ctx, 64, "cat-file", "-e", oid); err != nil && mode != "160000" {
			return gitResult(protocol.GitStateFailed, "unavailable", "a saved index entry is no longer in the object database; nothing was changed", nil)
		}
		zero = strings.Repeat("0", len(oid))
		fmt.Fprintf(&info, "%s %s %d\t%s\x00", mode, oid, i, req.Path)
	}
	if zero == "" {
		return gitResult(protocol.GitStateFailed, "no_saved_copy", "the saved copy has no index entries for this path; nothing was changed", nil)
	}
	m, ok := copyManifestOf(ctx, g, copyID)
	if !ok {
		return gitResult(protocol.GitStateFailed, "unavailable", "the saved copy's manifest could not be read; nothing was changed", nil)
	}
	key := base64.StdEncoding.EncodeToString([]byte(req.Path))
	var data []byte
	mode, oid, present := copyEntry(ctx, g, copyID, "w/"+req.Path)
	// The manifest decides whether the saved file existed: a failed read of
	// the entry must never turn into deleting the working file.
	switch state := m.Paths[key]; {
	case state == "present" && !present, state == "absent" && present, state != "present" && state != "absent":
		return gitResult(protocol.GitStateFailed, "unavailable", "the saved copy could not be read consistently; nothing was changed", nil)
	}
	if present {
		out, truncated, err := g.read(ctx, gitBackupBytesMax, "cat-file", "blob", oid)
		if err != nil || truncated {
			return gitResult(protocol.GitStateFailed, "unavailable", "the saved file could not be read; nothing was changed", nil)
		}
		data = out
	}
	perm, ok := m.Perms[key]
	if !ok {
		perm = 0o644
		if mode == "100755" {
			perm = 0o755
		}
	}
	if err := writeWorktreeAtomic(w, req.Path, tok, present, mode, fs.FileMode(perm).Perm(), data); err != nil {
		var pe *protocol.Error
		if errors.As(err, &pe) {
			return gitResult(protocol.GitStateFailed, pe.Code, pe.Message+"; nothing was changed", nil)
		}
		return gitResult(protocol.GitStateFailed, "unavailable", "the file could not be restored: "+err.Error(), nil)
	}
	all := bytes.NewBufferString(fmt.Sprintf("0 %s\t%s\x00", zero, req.Path))
	all.Write(info.Bytes())
	if r := w.runWith(ctx, gitRunOpts{stdin: all, combined: true}, "update-index", "-z", "--index-info"); r.err != nil {
		return gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "the file was restored but its index entries could not be; refresh", r.output)
	}
	return gitResult(protocol.GitStateSucceeded, "", "Restored "+req.Path+" from the saved copy", nil)
}

// writeWorktreeAtomic replaces w.top/p (whose token is tok) with the saved
// content: a temporary file or symlink in the same directory renamed over
// it (its name kept in the Git directory until then, so a crash leaves
// nothing behind unnoticed), or removal when the saved file was absent.
// Parent directories must be real directories (missing ones are created); a
// directory at p is refused.
func writeWorktreeAtomic(w *gitWriter, p, tok string, present bool, mode string, perm fs.FileMode, data []byte) error {
	root := w.top
	if worktreeStat(root, p) != tok {
		return failure("stale_entry", "the file changed after review")
	}
	switch tokenKind(tok) {
	case "dir", tokenBlocked, tokenUnavailable:
		return failure("not_supported", "the path cannot be restored here; use a terminal")
	}
	if !present {
		if tokenKind(tok) == tokenAbsent {
			return nil
		}
		return removeUntracked(root, p, tok)
	}
	dir := root
	for _, part := range strings.Split(path.Dir(p), "/") {
		if part == "." {
			break
		}
		dir = filepath.Join(dir, part)
		fi, err := os.Lstat(dir)
		switch {
		case os.IsNotExist(err):
			if err := os.Mkdir(dir, 0o755); err != nil {
				return err
			}
		case err != nil || !fi.IsDir():
			return failure("not_supported", "a parent of the path is not a directory")
		}
	}
	target := filepath.Join(root, filepath.FromSlash(p))
	tmp := filepath.Join(dir, fmt.Sprintf(".tui-restore-%d", time.Now().UnixNano()))
	pending := filepath.Join(w.gitDir, gitRestorePendingState)
	// Best effort: a restore must still work when the Git directory is not
	// writable (the temporary file is removed on every error path here).
	if os.WriteFile(pending, []byte(tmp+"\n"), 0o600) == nil {
		defer os.Remove(pending)
	}
	if mode == "120000" {
		if err := os.Symlink(string(data), tmp); err != nil {
			return err
		}
	} else {
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, werr := f.Write(data)
		cerr := f.Close()
		if err := errors.Join(werr, cerr, os.Chmod(tmp, perm)); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// conflictJournal records copies made without an engine runtime and moves
// the operation's record to ready once no unmerged path remains.
func conflictJournal(top string, c protocol.Command, decided func() string) func(*protocol.Snapshot, *protocol.GitResult, string) error {
	return func(s *protocol.Snapshot, res *protocol.GitResult, now string) error {
		if res == nil {
			return refuseWhileJobRuns(s, top)
		}
		if res.Operation == nil {
			return nil
		}
		recordJobDecision(s, top, c, res, decided())
		if rec := gitOperationFor(s, top); rec != nil && res.Operation.State != nil && gitOperationActive(rec.State) && recordMatches(*rec, *res.Operation.State) {
			switch rec.State {
			case protocol.GitOperationAgentRunning, protocol.GitOperationAgentReview, protocol.GitOperationAgentInterrupted:
			default:
				rec.State, rec.UpdatedAt = protocol.GitOperationReady, now
				if res.Operation.State.UnmergedFingerprint != "" {
					rec.State = protocol.GitOperationStoppedConflicts
				}
			}
			res.Operation.OperationID = rec.OperationID
			annotateOperation(res.Operation.State, rec)
		}
		return nil
	}
}
