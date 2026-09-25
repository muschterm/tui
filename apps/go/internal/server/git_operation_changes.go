package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// What the operation commands touch beyond the operation's own work (ADR
// 0023, second review): the exact files abort and skip overwrite that are
// not the current step's own result, untracked and ignored files in their
// way, staged conflict markers, and the backup written before an abort or
// skip changes anything.

const (
	gitMarkerScanFiles = 5000
	gitMarkerBlobMax   = 8 << 20
	gitMarkerTotalMax  = 32 << 20
	gitBinarySniff     = 8 << 10
	gitBackupFilesMax  = 1000
	gitBackupBytesMax  = 64 << 20
	gitBackupReadMax   = 1 << 20
	gitTodoMax         = 1000
	gitAbsentEntry     = "000000"
)

// opLists is what abort and skip back up: every tracked path with staged,
// unstaged or unmerged changes plus the untracked files each would
// overwrite.
type opLists struct {
	abort, skip []string
	// written lists the tracked paths abort rewrites, to verify them after.
	written []string
}

// rawChange is one `diff-index --raw` or `diff-tree --raw` record: the old
// and new sides as "mode oid" (mode 000000 when absent).
type rawChange struct {
	path, old, new string
	unmerged       bool
}

func parseRaw(out []byte) []rawChange {
	var changes []rawChange
	tokens := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(tokens); i += 2 {
		f := strings.Fields(strings.TrimPrefix(tokens[i], ":"))
		if len(f) != 5 {
			break
		}
		changes = append(changes, rawChange{path: tokens[i+1], old: f[0] + " " + f[2], new: f[1] + " " + f[3], unmerged: f[4] == "U"})
	}
	return changes
}

func absentSide(side string) bool { return strings.HasPrefix(side, gitAbsentEntry+" ") }

func sortedUnique(list []string) []string {
	out := slices.Clone(list)
	slices.Sort(out)
	return slices.Compact(out)
}

// observeChanges fills the pins, discard lists, marker scan and backup
// completeness of st, and returns what abort and skip back up.
func observeChanges(ctx context.Context, g *gitReader, gitDir string, st *protocol.GitOperationState) (*opLists, error) {
	out, truncated, err := g.read(ctx, gitStatusMaxBytes, append(statusArgs("no"), "--branch")...)
	if err != nil {
		return nil, gitError(err)
	}
	var tracked protocol.GitStatus
	parseGitStatus(out, truncated, 0, &tracked)
	annotateGitStatus(g.dir, &tracked)
	st.WorktreeFingerprint, st.StagedFingerprint = protocol.GitWorktreeFingerprint(tracked), tracked.StagedFingerprint
	incomplete := truncated || st.ConflictsTruncated || st.HeadOid == ""
	var staged []rawChange
	if st.HeadOid != "" {
		raw, rawTruncated, err := g.read(ctx, gitStatusMaxBytes, "diff-index", "--cached", "--raw", "-z", "--no-renames", "--no-abbrev", st.HeadOid)
		if err != nil {
			return nil, gitError(err)
		}
		incomplete = incomplete || rawTruncated
		staged = parseRaw(raw)
	}
	unmerged := map[string]bool{}
	for _, c := range st.Conflicts {
		unmerged[c.Path] = true
	}
	// Submodules are never recursed into, so a gitlink's worktree is not
	// touched and is not backed up.
	gitlinks := map[string]bool{}
	for _, c := range staged {
		if strings.HasPrefix(c.old, "160000 ") || strings.HasPrefix(c.new, "160000 ") {
			gitlinks[c.path] = true
		}
	}
	for _, c := range st.Conflicts {
		if c.Submodule {
			gitlinks[c.Path] = true
		}
	}
	var unstaged []string
	for _, e := range tracked.Entries {
		if e.Submodule {
			gitlinks[e.Path] = true
		}
		if e.Group == protocol.GitGroupUnstaged {
			unstaged = append(unstaged, e.Path)
		}
	}
	own, ownKnown := stepResults(ctx, g, gitDir, *st)
	var listed, stagedDeleted, dirty []string
	for _, c := range staged {
		dirty = append(dirty, c.path)
		if c.unmerged || unmerged[c.path] {
			continue
		}
		if absentSide(c.new) && !absentSide(c.old) {
			stagedDeleted = append(stagedDeleted, c.path)
		}
		// Only an entry that is exactly the step's own one-sided result is
		// the operation's; anything else, or anything in doubt, is listed.
		if o, ok := own[c.path]; !ownKnown || !ok || o.old != c.old || o.new != c.new {
			listed = append(listed, c.path)
		}
	}
	dirty = append(dirty, unstaged...)
	for p := range unmerged {
		dirty = append(dirty, p)
	}
	dirty = slices.DeleteFunc(dirty, func(p string) bool { return gitlinks[p] })
	rebase := st.Kind == protocol.GitOperationRebase
	if rebase {
		for _, p := range unstaged {
			if !unmerged[p] {
				listed = append(listed, p)
			}
		}
	}
	// Untracked or ignored files each command would write over: at paths
	// it restores (the original branch of a rebase, the start of a
	// cherry-pick or revert sequence), at staged deletions and, for a
	// rebase's hard reset, inside a tracked path that became a directory.
	abortCandidates := slices.Clone(stagedDeleted)
	skipCandidates := slices.Clone(stagedDeleted)
	// Staged and unmerged paths are rewritten too; one that is now a
	// directory (a file/directory conflict) may hold untracked files.
	for _, c := range staged {
		if !gitlinks[c.path] {
			abortCandidates = append(abortCandidates, c.path)
			skipCandidates = append(skipCandidates, c.path)
		}
	}
	var written []string
	if rebase {
		for _, p := range unstaged {
			if !unmerged[p] && !gitlinks[p] {
				abortCandidates = append(abortCandidates, p)
				skipCandidates = append(skipCandidates, p)
			}
		}
	}
	var hiddenCandidates []string
	abortInc, skipInc := incomplete, incomplete
	if st.OrigHead != "" && st.OrigHead != st.HeadOid {
		out, trunc, err := g.read(ctx, gitStatusMaxBytes, "diff-tree", "-r", "-z", "--name-only", "--no-renames", st.HeadOid, st.OrigHead)
		if err != nil || trunc {
			abortInc = true
		}
		for _, p := range strings.Split(string(out), "\x00") {
			if p != "" {
				abortCandidates = append(abortCandidates, p)
			}
		}
	}
	written = slices.Clone(abortCandidates)
	abortRisk, ok := untrackedAt(ctx, g, abortCandidates)
	abortRisk = dropGitlinks(ctx, g, *st, abortRisk)
	abortInc = abortInc || !ok
	hiddenCandidates = append(append(hiddenCandidates, abortCandidates...), unstaged...)
	for _, c := range staged {
		hiddenCandidates = append(hiddenCandidates, c.path)
	}
	lists := &opLists{abort: sortedUnique(append(slices.Clone(dirty), withoutNested(abortRisk)...)), written: sortedUnique(written)}
	st.DiscardsOnAbort = sortedUnique(append(slices.Clone(listed), abortRisk...))
	st.NestedOnAbort = nestedEntries(abortRisk)
	pending, updateRefs, pendingOK := pendingAdds(ctx, g, gitDir, *st)
	continueRisk, ok := untrackedAt(ctx, g, pending)
	continueRisk = dropGitlinks(ctx, g, *st, continueRisk)
	st.NestedOnContinue = nestedEntries(continueRisk)
	st.ContinueInTheWay = sortedUnique(withoutNested(continueRisk))
	if len(st.ContinueInTheWay) == 0 {
		st.ContinueInTheWay = nil
	}
	st.ContinueUnchecked = !pendingOK || !ok
	st.ContinueUpdatesRefs = updateRefs
	hiddenCandidates = append(hiddenCandidates, pending...)
	hidden, ok := hiddenEntries(ctx, g, hiddenCandidates)
	st.HiddenEntries = hidden
	abortInc, skipInc = abortInc || !ok, skipInc || !ok
	if rebase && st.Current != nil && st.StopReason == "" {
		skipRisk, ok := untrackedAt(ctx, g, skipCandidates)
		skipRisk = dropGitlinks(ctx, g, *st, skipRisk)
		skipInc = skipInc || !ok
		st.DiscardsOnSkip = sortedUnique(append(slices.Clone(listed), skipRisk...))
		st.NestedOnSkip = nestedEntries(skipRisk)
		lists.skip = sortedUnique(append(slices.Clone(dirty), withoutNested(skipRisk)...))
	}
	st.NestedInTheWay = nil
	if nested := sortedUnique(slices.Concat(st.NestedOnAbort, st.NestedOnSkip, st.NestedOnContinue)); len(nested) > 0 {
		st.NestedInTheWay = nested
	}
	if len(st.DiscardsOnAbort) == 0 {
		st.DiscardsOnAbort = nil
	}
	if len(st.DiscardsOnSkip) == 0 {
		st.DiscardsOnSkip = nil
	}
	st.DiscardsOnAbortFingerprint, st.DiscardsOnSkipFingerprint = pathsFingerprint(st.DiscardsOnAbort), pathsFingerprint(st.DiscardsOnSkip)
	st.BackupMissingOnAbort, st.BackupMissingOnSkip = predictBackupMissing(g.dir, lists.abort), predictBackupMissing(g.dir, lists.skip)
	st.BackupMissingOnAbortFingerprint, st.BackupMissingOnSkipFingerprint = pathsFingerprint(st.BackupMissingOnAbort), pathsFingerprint(st.BackupMissingOnSkip)
	st.BackupIncomplete = len(st.BackupMissingOnAbort)+len(st.BackupMissingOnSkip) > 0
	if st.UnmergedFingerprint == "" && len(staged) > 0 {
		st.MarkerPaths, st.MarkersIncomplete = scanMarkers(ctx, g, staged)
		st.MarkersFingerprint = pathsFingerprint(st.MarkerPaths)
	}
	if (st.Kind == protocol.GitOperationCherryPick || st.Kind == protocol.GitOperationRevert) && st.OrigHead != "" && st.OrigHead != st.HeadOid {
		st.AbortDropsCommits, st.AbortDropsCount, st.AbortDropsIncomplete = droppedCommits(ctx, g, st.OrigHead, st.HeadOid)
		st.AbortDropsFingerprint = droppedFingerprint(st.AbortDropsCommits, st.AbortDropsCount)
		if st.AbortDropsIncomplete && st.AbortDropsCount == 0 {
			abortInc = true // the dropped commits could not be counted
		}
	}
	st.DiscardsOnAbortIncomplete = abortInc
	st.DiscardsOnSkipIncomplete = skipInc && rebase && st.Current != nil && st.StopReason == ""
	st.DiscardsIncomplete = st.DiscardsOnAbortIncomplete || st.DiscardsOnSkipIncomplete
	return lists, nil
}

// dropGitlinks removes nested-repository entries (trailing slash) at
// paths that are submodules (gitlinks) in HEAD, the operation's original
// head or the index: Git never removes a submodule's directory here (no
// recursion), so its repository is not in the way.
func dropGitlinks(ctx context.Context, g *gitReader, st protocol.GitOperationState, list []string) []string {
	return slices.DeleteFunc(list, func(entry string) bool {
		p, nested := strings.CutSuffix(entry, "/")
		if !nested || !validGitPath(p) {
			return false
		}
		for _, commit := range []string{st.HeadOid, st.OrigHead} {
			if gitFullHash.MatchString(commit) {
				if out, _, err := g.read(ctx, 4096, "ls-tree", "-z", "--full-tree", commit, "--", p); err == nil && strings.HasPrefix(string(out), "160000 ") {
					return true
				}
			}
		}
		out, _, err := g.read(ctx, 4096, "ls-files", "--stage", "-z", "--", p)
		return err == nil && strings.HasPrefix(string(out), "160000 ")
	})
}

// predictBackupMissing lists what makeBackup could not copy from paths:
// files beyond gitBackupFilesMax or gitBackupBytesMax, in makeBackup's
// order, and anything other than a regular file, symlink or directory.
// makeBackup reports what actually could not be copied; a command's
// acknowledgement must match that exactly.
func predictBackupMissing(top string, paths []string) []string {
	var missing []string
	var total int64
	files := 0
	for _, p := range sortedUnique(paths) {
		fi, err := os.Lstat(filepath.Join(top, filepath.FromSlash(p)))
		switch {
		case err != nil && os.IsNotExist(err):
			continue
		case err == nil && fi.IsDir():
			continue
		}
		files++
		if err != nil || files > gitBackupFilesMax || (!fi.Mode().IsRegular() && fi.Mode()&os.ModeSymlink == 0) {
			missing = append(missing, p)
			continue
		}
		size := fi.Size() // a symlink's size is its target's length
		if total+size > gitBackupBytesMax {
			missing = append(missing, p)
			continue
		}
		total += size
	}
	return missing
}

// nestedEntries picks the directory entries of an untracked listing:
// `ls-files --others` names a nested repository (or a directory it does not
// expand) with a trailing slash. Their content cannot be listed or backed
// up here, so no command may remove them.
func nestedEntries(list []string) []string {
	var nested []string
	for _, p := range list {
		if strings.HasSuffix(p, "/") {
			nested = append(nested, p)
		}
	}
	return nested
}

func withoutNested(list []string) []string {
	return slices.DeleteFunc(slices.Clone(list), func(p string) bool { return strings.HasSuffix(p, "/") })
}

// droppedFingerprint digests the dropped commits and their exact count, so
// an acknowledgement covers exactly what an abort removes.
func droppedFingerprint(commits []protocol.GitOperationCommit, count int) string {
	if count == 0 {
		return ""
	}
	oids := make([]string, 0, len(commits)+1)
	oids = append(oids, "count:"+strconv.Itoa(count))
	for _, c := range commits {
		oids = append(oids, c.Oid)
	}
	return pathsFingerprint(oids)
}

// pathsFingerprint digests a path list exactly as the server holds it, so
// an acknowledgement works for names JSON cannot carry (not valid UTF-8).
// It is empty for an empty list.
func pathsFingerprint(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	h := sha256.New()
	h.Write([]byte("paths-v1\x00"))
	for _, p := range paths {
		h.Write([]byte(p + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hiddenEntries lists index entries marked assume-unchanged or
// skip-worktree among paths: a local change there is invisible to status,
// so the command cannot know what it would overwrite.
func hiddenEntries(ctx context.Context, g *gitReader, paths []string) ([]string, bool) {
	var list []string
	for _, p := range sortedUnique(paths) {
		if validGitPath(p) {
			list = append(list, p)
		}
	}
	if len(list) > gitWrittenPathsMax {
		return nil, false
	}
	records, ok := lsFilesChunk(ctx, g, []string{"-v"}, list)
	if !ok {
		return nil, false
	}
	var hidden []string
	for _, r := range records {
		tag, p, found := strings.Cut(r, " ")
		if found && len(tag) == 1 && (unicode.IsLower(rune(tag[0])) || tag == "S") {
			hidden = append(hidden, p)
		}
	}
	return sortedUnique(hidden), true
}

// droppedCommits lists the commits an abort of a cherry-pick or revert
// sequence removes from the branch (from..to): at most 200 with bounded
// subjects, their exact count, and whether the list is incomplete (more
// than listed, or a read failed or was cut short).
func droppedCommits(ctx context.Context, g *gitReader, from, to string) (commits []protocol.GitOperationCommit, count int, incomplete bool) {
	out, _, err := g.read(ctx, 4096, "rev-list", "--count", from+".."+to, "--")
	count, convErr := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || convErr != nil {
		return nil, 0, true
	}
	out, truncated, err := g.read(ctx, gitStatusMaxBytes, "log", "-z", "--no-show-signature", "-n", "200", "--format=%H%x00%s", from+".."+to, "--")
	if err != nil {
		return nil, count, true
	}
	f := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		if truncated && i+2 >= len(f) {
			break // the last record may be cut short
		}
		oid := strings.TrimPrefix(f[i], "\n")
		if gitFullHash.MatchString(oid) {
			commits = append(commits, protocol.GitOperationCommit{Oid: oid, Label: shortOid(oid), Subject: boundedSubject(f[i+1])})
		}
	}
	return commits, count, len(commits) != count
}

// stepResults maps each path the current step changes to its old and new
// sides: merge base to MERGE_HEAD for a merge, parent to commit for a pick
// or a replayed commit, commit to parent for a revert. A staged entry is
// the operation's own exactly when HEAD still has the old side (only this
// step changed the path) and the index holds the new side. known is false
// when the step cannot be determined (several merge bases, a merge commit
// picked without a known mainline, an interactive stop), so nothing is
// treated as own.
func stepResults(ctx context.Context, g *gitReader, gitDir string, st protocol.GitOperationState) (map[string]rawChange, bool) {
	if st.StopReason != "" || st.Current == nil || !gitFullHash.MatchString(st.Current.Oid) || st.HeadOid == "" {
		return nil, false
	}
	var from, to string
	switch st.Kind {
	case protocol.GitOperationMerge:
		out, _, err := g.read(ctx, 4096, "merge-base", "--all", st.HeadOid, st.Current.Oid)
		bases := strings.Fields(string(out))
		if err != nil || len(bases) != 1 {
			return nil, false
		}
		from, to = bases[0], st.Current.Oid
	default:
		out, _, err := g.read(ctx, 64<<10, "rev-list", "--parents", "-n", "1", st.Current.Oid)
		f := strings.Fields(string(out))
		if err != nil || len(f) < 2 {
			return nil, false
		}
		parent := f[1]
		if len(f) > 2 {
			m := sequencerMainline(gitDir)
			if m < 1 || m >= len(f) {
				return nil, false
			}
			parent = f[m]
		}
		from, to = parent, st.Current.Oid
		if st.Kind == protocol.GitOperationRevert {
			from, to = to, from
		}
	}
	out, truncated, err := g.read(ctx, gitStatusMaxBytes, "diff-tree", "-r", "-z", "--raw", "--no-renames", "--no-abbrev", from, to)
	if err != nil || truncated {
		return nil, false
	}
	own := map[string]rawChange{}
	for _, c := range parseRaw(out) {
		own[c.path] = c
	}
	return own, true
}

// sequencerMainline reads cherry-pick/revert -m from the sequencer options;
// 0 when absent.
func sequencerMainline(gitDir string) int {
	for _, line := range strings.Split(readStateFile(filepath.Join(gitDir, "sequencer"), "opts"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "mainline") {
			n, _ := strconv.Atoi(strings.TrimSpace(value))
			return n
		}
	}
	return 0
}

var todoOid = func(s string) bool { return gitHashInput.MatchString(s) }

// pendingAdds lists files the commits still to be applied add, modify or
// retype (a rebase's todo list, or a cherry-pick or revert sequence), which
// continue and skip would write; a revert writes what the commit deleted or
// modified. Each commit is compared with its parent explicitly (the
// mainline parent from the sequencer options for a merge commit, the empty
// tree for a root commit), so neither merges nor log configuration hide
// anything. updateRefs lists the refs an external `--update-refs` rebase
// would move. ok is false when this could not be determined (an
// apply-backend rebase, a merge commit without a known mainline, a todo
// command other than pick-like, revert or update-ref, or too many entries).
func pendingAdds(ctx context.Context, g *gitReader, gitDir string, st protocol.GitOperationState) (paths, updateRefs []string, ok bool) {
	var todo string
	sequencer := false
	switch st.Kind {
	case protocol.GitOperationRebase:
		if _, err := os.Stat(filepath.Join(gitDir, "rebase-merge")); err != nil {
			return nil, nil, false
		}
		todo = readStateFile(filepath.Join(gitDir, "rebase-merge"), "git-rebase-todo")
	case protocol.GitOperationCherryPick, protocol.GitOperationRevert:
		todo, sequencer = readStateFile(filepath.Join(gitDir, "sequencer"), "todo"), true
	default:
		return nil, nil, true
	}
	var adds, reverts []string
	for _, line := range strings.Split(todo, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		switch f[0] {
		case "pick", "p", "reword", "r", "edit", "e", "fixup", "f", "squash", "s":
			if len(f) < 2 || !todoOid(f[1]) {
				return nil, nil, false
			}
			adds = append(adds, f[1])
		case "revert":
			if len(f) < 2 || !todoOid(f[1]) {
				return nil, nil, false
			}
			reverts = append(reverts, f[1])
		case "update-ref", "u":
			if len(f) >= 2 {
				updateRefs = append(updateRefs, f[1])
			}
		case "drop", "d", "noop", "break", "b", "exec", "x":
		default:
			return nil, nil, false
		}
	}
	if len(adds)+len(reverts) > gitTodoMax {
		return nil, nil, false
	}
	mainline := 0
	if sequencer {
		mainline = sequencerMainline(gitDir)
	}
	collect := func(oids []string, filter string) bool {
		if len(oids) == 0 {
			return true
		}
		out, _, err := g.read(ctx, 1<<20, append([]string{"rev-parse"}, oids...)...)
		full := strings.Fields(string(out))
		if err != nil || len(full) != len(oids) {
			return false
		}
		out, truncated, err := g.read(ctx, 1<<20, append([]string{"rev-list", "--no-walk=unsorted", "--parents"}, append(full, "--")...)...)
		if err != nil || truncated {
			return false
		}
		parents := map[string][]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				parents[f[0]] = f[1:]
			}
		}
		var input strings.Builder
		for _, oid := range full {
			ps, found := parents[oid]
			switch {
			case !found || !gitFullHash.MatchString(oid):
				return false
			case len(ps) == 0:
				input.WriteString(oid + "\n")
			case len(ps) == 1:
				input.WriteString(oid + " " + ps[0] + "\n")
			case mainline >= 1 && mainline <= len(ps):
				input.WriteString(oid + " " + ps[mainline-1] + "\n")
			default:
				return false // a merge commit without a known mainline
			}
		}
		out, truncated, err = g.readInput(ctx, gitStatusMaxBytes, strings.NewReader(input.String()),
			"diff-tree", "--stdin", "-r", "-z", "--root", "--no-commit-id", "--name-only", "--no-renames", "--diff-filter="+filter)
		if err != nil || truncated {
			return false
		}
		for _, p := range strings.Split(string(out), "\x00") {
			if p = strings.TrimPrefix(p, "\n"); p != "" {
				paths = append(paths, p)
			}
		}
		return true
	}
	// Additions, and modifications or type changes, which a
	// modify/delete conflict writes even where the path is absent.
	if !collect(adds, "AMT") || !collect(reverts, "DMT") {
		return nil, updateRefs, false
	}
	return paths, updateRefs, true
}

// untrackedAt lists untracked (including ignored) files a command would
// overwrite or remove to write each of paths: a file at the path, an
// untracked file or symlink blocking it as a parent, or untracked files in
// a directory that the path turns into a file. A tracked blocker or
// directory content belongs to the operation. ok is false when this could
// not be checked completely.
func untrackedAt(ctx context.Context, g *gitReader, paths []string) (risk []string, ok bool) {
	var candidates, dirs []string
	for _, p := range sortedUnique(paths) {
		if !validGitPath(p) {
			continue
		}
		tok := worktreeStat(g.dir, p)
		switch tokenKind(tok) {
		case tokenAbsent:
		case "dir":
			dirs = append(dirs, p)
		case tokenBlocked:
			if a := blockingAncestor(g.dir, p); a != "" {
				candidates = append(candidates, a)
			}
		default:
			candidates = append(candidates, p)
		}
	}
	// Existing tracked files are the common case (a large upstream change)
	// and are filtered by the index below; only directories are expanded.
	if len(candidates) > gitWrittenPathsMax || len(dirs) > gitOverwriteCandidates {
		return nil, false
	}
	candidates = sortedUnique(candidates)
	if len(candidates) > 0 {
		records, ok := lsFilesChunk(ctx, g, nil, candidates)
		if !ok {
			return nil, false
		}
		tracked := map[string]bool{}
		for _, r := range records {
			tracked[r] = true
		}
		for _, c := range candidates {
			if !tracked[c] {
				risk = append(risk, c)
			}
		}
	}
	if len(dirs) > 0 {
		// Every untracked file inside, ignored ones included.
		records, ok := lsFilesChunk(ctx, g, []string{"--others"}, dirs)
		if !ok || len(records) > gitOverwriteCandidates {
			return nil, false
		}
		risk = append(risk, records...)
	}
	return risk, true
}

// ---- Conflict markers ----

// scanMarkers reads staged blobs (regular files) directly and reports those
// with a conflict-marker line. incomplete when a blob could not be scanned.
func scanMarkers(ctx context.Context, g *gitReader, staged []rawChange) (found []string, incomplete bool) {
	type blob struct{ path, oid string }
	var blobs []blob
	for _, c := range staged {
		mode, oid, _ := strings.Cut(c.new, " ")
		if c.unmerged || (mode != "100644" && mode != "100755") || !gitFullHash.MatchString(oid) {
			continue
		}
		blobs = append(blobs, blob{c.path, oid})
	}
	if len(blobs) > gitMarkerScanFiles {
		blobs, incomplete = blobs[:gitMarkerScanFiles], true
	}
	if len(blobs) == 0 {
		return nil, incomplete
	}
	var ids strings.Builder
	for _, b := range blobs {
		ids.WriteString(b.oid + "\n")
	}
	out, truncated, err := g.readInput(ctx, 1<<20, strings.NewReader(ids.String()), "cat-file", "--batch-check=%(objectname) %(objectsize)")
	if err != nil || truncated {
		return nil, true
	}
	sizes := map[string]int64{}
	for _, line := range strings.Split(string(out), "\n") {
		if oid, size, ok := strings.Cut(line, " "); ok {
			n, err := strconv.ParseInt(size, 10, 64)
			if err == nil {
				sizes[oid] = n
			}
		}
	}
	var selected []blob
	var total int64
	for _, b := range blobs {
		size, ok := sizes[b.oid]
		if !ok || size > gitMarkerBlobMax || total+size > gitMarkerTotalMax {
			incomplete = true
			continue
		}
		total += size
		selected = append(selected, b)
	}
	markerSize := conflictMarkerSizes(ctx, g, selected, func(b blob) string { return b.path })
	ids.Reset()
	seen := map[string]bool{}
	for _, b := range selected {
		if !seen[b.oid] {
			seen[b.oid] = true
			ids.WriteString(b.oid + "\n")
		}
	}
	out, truncated, err = g.readInput(ctx, int(total)+len(seen)*200+1, strings.NewReader(ids.String()), "cat-file", "--batch")
	if err != nil || truncated {
		return nil, true
	}
	contents := map[string][]byte{}
	r := bufio.NewReader(bytes.NewReader(out))
	for {
		header, err := r.ReadString('\n')
		if err != nil {
			break
		}
		f := strings.Fields(header)
		if len(f) != 3 {
			return nil, true
		}
		n, err := strconv.Atoi(f[2])
		if err != nil {
			return nil, true
		}
		data := make([]byte, n+1)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, true
		}
		contents[f[0]] = data[:n]
	}
	for _, b := range selected {
		data, ok := contents[b.oid]
		if !ok {
			incomplete = true
			continue
		}
		if hasConflictMarker(data, markerSize[b.path]) {
			found = append(found, b.path)
		}
	}
	return sortedUnique(found), incomplete
}

// conflictMarkerSizes reads the conflict-marker-size attribute (default 7).
func conflictMarkerSizes[T any](ctx context.Context, g *gitReader, items []T, path func(T) string) map[string]int {
	sizes := map[string]int{}
	var paths []string
	for _, it := range items {
		sizes[path(it)] = 7
		paths = append(paths, path(it))
	}
	for len(paths) > 0 {
		n := min(len(paths), 500)
		out, _, err := g.read(ctx, 1<<20, append([]string{"check-attr", "-z", "conflict-marker-size", "--"}, paths[:n]...)...)
		if err == nil {
			f := strings.Split(string(out), "\x00")
			for j := 0; j+2 < len(f); j += 3 {
				if v, err := strconv.Atoi(f[j+2]); err == nil && v > 0 && v < 100 {
					sizes[f[j]] = v
				}
			}
		}
		paths = paths[n:]
	}
	return sizes
}

// hasConflictMarker reports a line that starts with size '<', '|' or '>'
// followed by a space or the line end (CRLF allowed). Content with a NUL in
// its first 8 KiB is binary, as Git decides, and has none.
func hasConflictMarker(data []byte, size int) bool {
	if size <= 0 {
		size = 7
	}
	if bytes.IndexByte(data[:min(len(data), gitBinarySniff)], 0) >= 0 {
		return false
	}
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		for _, c := range []byte("<|>") {
			if len(line) >= size && bytes.Count(line[:size], []byte{c}) == size && (len(line) == size || line[size] == ' ') {
				return true
			}
		}
	}
	return false
}

// ---- Backup ----

// makeBackup copies paths (working-tree content, and stage-0 index entries)
// into two unreferenced commits in the repository's object database, before
// an abort or skip overwrites them. Nothing in the index, working tree or
// refs changes. missing lists paths that could not be copied.
func (w *gitWriter) makeBackup(ctx context.Context, g *gitReader, paths []string, what string) (*protocol.GitOperationBackup, error) {
	b := &protocol.GitOperationBackup{CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	var worktree, index bytes.Buffer
	var total int64
	files := 0
	var nestedErr error
	add := func(p string) {
		tok := worktreeStat(w.top, p)
		kind := tokenKind(tok)
		if kind == tokenAbsent {
			return
		}
		if kind == "dir" {
			// A directory's untracked files are backed up one by one (they
			// are listed separately); tracked content is in the index. A
			// nested repository or unexpanded directory inside cannot be.
			if records, ok := lsFilesChunk(ctx, g, []string{"--others"}, []string{p}); !ok || len(nestedEntries(records)) > 0 {
				nestedErr = failure("not_supported", "a nested repository or untracked directory is in the way at "+p+"; resolve it in a terminal")
			}
			return
		}
		files++
		if files > gitBackupFilesMax || (kind != "reg" && kind != "lnk") {
			b.Missing = append(b.Missing, p)
			return
		}
		content, err := openWorktreeContent(w.top, p, tok)
		if err != nil {
			b.Missing = append(b.Missing, p)
			return
		}
		var data []byte
		mode := "120000"
		if content.symlink {
			data = content.link
		} else {
			data, err = io.ReadAll(io.LimitReader(content.file, gitBackupBytesMax-total+1))
			fi, statErr := content.file.Stat()
			content.file.Close()
			mode = "100644"
			if statErr == nil && fi.Mode()&0o111 != 0 {
				mode = "100755"
			}
			if err != nil || total+int64(len(data)) > gitBackupBytesMax {
				b.Missing = append(b.Missing, p)
				return
			}
		}
		out, _, err := w.run(ctx, bytes.NewReader(data), false, "hash-object", "-w", "--no-filters", "--stdin")
		oid := strings.TrimSpace(string(out))
		if err != nil || !gitFullHash.MatchString(oid) {
			b.Missing = append(b.Missing, p)
			return
		}
		total += int64(len(data))
		b.Paths = append(b.Paths, p)
		fmt.Fprintf(&worktree, "%s %s\t%s\x00", mode, oid, p)
	}
	for _, p := range sortedUnique(paths) {
		add(p)
	}
	if nestedErr != nil {
		return nil, nestedErr
	}
	if len(paths) > 0 {
		records, ok := lsFilesChunk(ctx, g, []string{"--stage"}, sortedUnique(paths))
		if !ok {
			b.Missing = append(b.Missing, "(index)")
		}
		for _, r := range records {
			meta, p, found := strings.Cut(r, "\t")
			f := strings.Fields(meta)
			if found && len(f) == 3 && f[2] == "0" {
				fmt.Fprintf(&index, "%s %s\t%s\x00", f[0], f[1], p)
			}
		}
	}
	tmp, err := os.MkdirTemp("", "tui-backup-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	tree := func(name string, entries *bytes.Buffer) (string, error) {
		env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, name)}
		if r := w.runWith(ctx, gitRunOpts{stdin: entries, env: env}, "update-index", "-z", "--add", "--index-info"); r.err != nil {
			return "", fmt.Errorf("backup index: %w", r.err)
		}
		r := w.runWith(ctx, gitRunOpts{env: env}, "write-tree")
		if r.err != nil {
			return "", fmt.Errorf("backup tree: %w", r.err)
		}
		return strings.TrimSpace(string(r.stdout)), nil
	}
	ident := []string{"GIT_AUTHOR_NAME=Operation backup", "GIT_AUTHOR_EMAIL=backup@localhost", "GIT_COMMITTER_NAME=Operation backup", "GIT_COMMITTER_EMAIL=backup@localhost"}
	commit := func(treeOid, message string, parents ...string) (string, error) {
		args := []string{"commit-tree", "--no-gpg-sign", "-m", message}
		for _, p := range parents {
			args = append(args, "-p", p)
		}
		r := w.runWith(ctx, gitRunOpts{env: ident}, append(args, treeOid)...)
		oid := strings.TrimSpace(string(r.stdout))
		if r.err != nil || !gitFullHash.MatchString(oid) {
			return "", fmt.Errorf("backup commit: %v", r.err)
		}
		return oid, nil
	}
	indexTree, err := tree("index", &index)
	if err != nil {
		return nil, err
	}
	worktreeTree, err := tree("worktree", &worktree)
	if err != nil {
		return nil, err
	}
	if b.IndexOid, err = commit(indexTree, "Operation backup (staged) before "+what); err != nil {
		return nil, err
	}
	if b.Oid, err = commit(worktreeTree, "Operation backup (working tree) before "+what, b.IndexOid); err != nil {
		return nil, err
	}
	b.Incomplete = len(b.Missing) > 0
	return b, nil
}

// gitOperationBackup is GET /v1/git/operation/backup: one file of a backup
// commit, read-only.
func (e *engine) gitOperationBackup(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	e.gitHandle(w, r, func(ctx context.Context, dir string) (any, error) {
		return readBackupFile(ctx, dir, q.Get("oid"), q.Get("path"), func(top, oid string) bool {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.backupRecordedLocked(top, oid)
		})
	})
}

// readBackupFile reads one file of a backup that recorded confirms was made
// for this repository.
func readBackupFile(ctx context.Context, dir, oid, p string, recorded func(top, oid string) bool) (protocol.GitBackupFile, error) {
	f := protocol.GitBackupFile{Oid: oid, Path: p}
	if !gitFullHash.MatchString(oid) || !validGitPath(p) {
		return f, failure("invalid", "oid must be a full backup commit hash and path a checkout path")
	}
	if !gitReadable(inspectWorkspace(ctx, dir)) {
		return f, errGitNotRepository
	}
	g, err := newGitReader(ctx, dir)
	if err != nil {
		return f, err
	}
	if !recorded(g.dir, oid) {
		return f, failure("not_found", "no such backup for this repository")
	}
	out, _, err := g.read(ctx, 64<<10, "ls-tree", "-z", "--full-tree", oid, "--", p)
	if err != nil {
		return f, failure("not_found", "no such backup")
	}
	meta, name, ok := strings.Cut(strings.TrimSuffix(string(out), "\x00"), "\t")
	fields := strings.Fields(meta)
	if !ok || name != p || len(fields) != 3 || fields[1] != "blob" {
		return f, failure("not_found", "the backup has no file at this path")
	}
	f.Mode = fields[0]
	size, _, err := g.read(ctx, 64, "cat-file", "-s", fields[2])
	if err != nil {
		return f, failure("unavailable", "the backup could not be read")
	}
	f.Size, _ = strconv.ParseInt(strings.TrimSpace(string(size)), 10, 64)
	data, truncated, err := g.read(ctx, gitBackupReadMax, "cat-file", "blob", fields[2])
	if err != nil {
		return f, failure("unavailable", "the backup could not be read")
	}
	f.Content, f.Truncated = data, truncated
	return f, nil
}
