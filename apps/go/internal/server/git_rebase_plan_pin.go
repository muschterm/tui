package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The checkout pin of a planning job (ADR 0027): everything a turn must
// leave unchanged, so that a proposal is never offered after its agent
// changed the checkout or what a later rebase would run.
//
//   - Branch, HEAD and the raw HEAD file (its symbolic target);
//   - the whole index (`ls-files --stage`), its flags (`ls-files -v`) and,
//     for skip-worktree and assume-unchanged paths, whose edits status
//     hides, their working-tree stat tokens;
//   - the complete status (`--porcelain=v2 --untracked-files=all
//     --ignore-submodules=none`, submodule changes included) with a stat
//     token for every path whose working-tree side differs, so editing a
//     file that was already changed shows;
//   - Repo: the effective configuration of every scope (includes
//     resolved), the effective hooks directory (core.hooksPath or hooks/:
//     names, modes and content hashes, symlinked hooks by their targets'
//     content), exclude, attributes, sparse-checkout and grafts in info/ of
//     the repository and of the worktree, and every ref with its target
//     (stash included) except remote-tracking and prefetch refs.
//
// Every part is complete or the pin fails: a start is refused and a turn's
// comparison taints. Status keeps up to planStatusNamed lines for naming
// what changed; the fingerprint always covers all of it.

const (
	planStatusMax   = 64 << 20
	planIndexMax    = 64 << 20
	planStatusNamed = 2000
	planDirEntries  = 4000
	planDirBytes    = 64 << 20
	planRefsMax     = 64 << 20
)

type planCheckout struct {
	Branch            string            `json:"branch"`
	Head              string            `json:"head"`
	Index             string            `json:"index"`
	IndexFlags        string            `json:"index_flags,omitempty"`
	Status            []string          `json:"status,omitempty"`
	StatusFingerprint string            `json:"status_fingerprint"`
	StatusIncomplete  bool              `json:"status_incomplete,omitempty"`
	Repo              map[string]string `json:"repo,omitempty"`
}

var planRepoParts = map[string]string{
	"head":   "HEAD's target",
	"config": "the Git configuration (any scope)",
	"hooks":  "the hooks directory",
	"info":   "info/ (exclude, attributes, sparse-checkout or grafts)",
	"refs":   "local branches, tags or other refs (the stash included; remote-tracking refs are not compared)",
}

func pinCheckout(ctx context.Context, g *gitReader) (planCheckout, error) {
	head, err := readHead(ctx, g)
	if err != nil {
		return planCheckout{}, err
	}
	pin := planCheckout{Branch: head.branch, Head: head.oid, Repo: map[string]string{}}
	unavailable := func(what string) (planCheckout, error) {
		return planCheckout{}, failure("unavailable", what+" could not be read completely, so the checkout cannot be pinned")
	}
	h := sha256.New()
	if err := g.stream(ctx, h, "ls-files", "--stage", "-z"); err != nil {
		return unavailable("the index")
	}
	pin.Index = hex.EncodeToString(h.Sum(nil))
	if pin.IndexFlags, err = indexFlags(ctx, g); err != nil {
		return unavailable("the index flags")
	}
	if err := pinStatus(ctx, g, &pin); err != nil {
		return unavailable("the working tree status")
	}
	out, truncated, err := g.read(ctx, 64<<10, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir", "--git-path", "hooks")
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if err != nil || truncated || len(lines) != 3 {
		return unavailable("the repository layout")
	}
	gitDir, common, hooks := lines[0], lines[1], lines[2]
	if data, err := os.ReadFile(filepath.Join(gitDir, "HEAD")); err == nil {
		pin.Repo["head"] = digest(data)
	} else {
		return unavailable("HEAD")
	}
	h = sha256.New()
	if err := g.stream(ctx, h, "config", "--list", "--show-scope", "--show-origin", "-z"); err != nil {
		return unavailable("the Git configuration")
	}
	pin.Repo["config"] = hex.EncodeToString(h.Sum(nil))
	refs, truncated, err := g.read(ctx, planRefsMax, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)")
	if err != nil || truncated {
		return unavailable("the refs")
	}
	h = sha256.New()
	for _, line := range strings.Split(string(refs), "\n") {
		// Remote-tracking and prefetch refs move by themselves (fetch,
		// background maintenance) and no rebase runs them.
		if strings.HasPrefix(line, "refs/remotes/") || strings.HasPrefix(line, "refs/prefetch/") {
			continue
		}
		h.Write([]byte(line + "\n"))
	}
	pin.Repo["refs"] = hex.EncodeToString(h.Sum(nil))
	if pin.Repo["hooks"], err = dirDigest(hooks); err != nil {
		return unavailable(planRepoParts["hooks"])
	}
	// Only the info/ files Git reads for a checkout or rebase; git gc
	// writes others (info/refs, info/packs).
	h = sha256.New()
	dirs := []string{filepath.Join(common, "info")}
	if wt := filepath.Join(gitDir, "info"); realPath(wt) != realPath(dirs[0]) {
		dirs = append(dirs, wt)
	}
	for _, dir := range dirs {
		for _, name := range []string{"exclude", "attributes", "sparse-checkout", "grafts"} {
			d, err := fileDigest(filepath.Join(dir, name))
			if err != nil {
				return unavailable("info/" + name)
			}
			fmt.Fprintf(h, "%s\x00%s\x00%s\x00", dir, name, d)
		}
	}
	pin.Repo["info"] = hex.EncodeToString(h.Sum(nil))
	return pin, nil
}

// fileDigest hashes one file (a symlink by its target and what it points
// at; a dangling link by its text), "absent" when it does not exist.
func fileDigest(p string) (string, error) {
	info, err := os.Lstat(p)
	switch {
	case os.IsNotExist(err):
		return "absent", nil
	case err != nil:
		return "", err
	}
	h := sha256.New()
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "link\x00%s\x00", target)
		if info, err = os.Stat(p); err != nil {
			h.Write([]byte("dangling"))
			return hex.EncodeToString(h.Sum(nil)), nil
		}
	}
	if !info.Mode().IsRegular() || info.Size() > planDirBytes {
		fmt.Fprintf(h, "%s\x00%d", info.Mode(), info.Size())
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fmt.Fprintf(h, "%s\x00", info.Mode())
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// indexFlags digests `ls-files -v` (the tag of every entry, so a change of
// skip-worktree or assume-unchanged shows) and the working-tree stat
// token of every flagged path.
func indexFlags(ctx context.Context, g *gitReader) (string, error) {
	out, truncated, err := g.read(ctx, planIndexMax, "ls-files", "-v", "-z")
	if err != nil || truncated {
		return "", fmt.Errorf("index flags unavailable")
	}
	h := sha256.New()
	h.Write(out)
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) < 3 || rec[1] != ' ' {
			continue
		}
		tag := rec[0]
		if tag == 'S' || (tag >= 'a' && tag <= 'z') {
			p := string(rec[2:])
			fmt.Fprintf(h, "\x00flag\x00%s\x00%s", p, worktreeStat(g.dir, p))
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pinStatus digests the complete status with stat tokens for every path
// whose working-tree side differs, keeping up to planStatusNamed lines.
func pinStatus(ctx context.Context, g *gitReader, pin *planCheckout) error {
	out, truncated, err := g.read(ctx, planStatusMax, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none", "--no-renames")
	if err != nil || truncated {
		return fmt.Errorf("status unavailable")
	}
	h := sha256.New()
	var lines []string
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		path, worktree := "", false
		switch rec[0] {
		case '1':
			f := strings.SplitN(rec, " ", 9)
			if len(f) == 9 {
				path, worktree = f[8], f[1][1] != '.'
			}
		case 'u':
			f := strings.SplitN(rec, " ", 11)
			if len(f) == 11 {
				path, worktree = f[10], true
			}
		case '?':
			path, worktree = strings.TrimPrefix(rec, "? "), true
		}
		token := ""
		if worktree && path != "" {
			token = worktreeStat(g.dir, strings.TrimSuffix(path, "/"))
		}
		line := rec + "\x00" + token
		h.Write([]byte(line + "\x00\x00"))
		if len(lines) < planStatusNamed {
			// changedOutside keys lines by their second field.
			lines = append(lines, "s\x00"+cmpOr(path, rec)+"\x00"+digest([]byte(line)))
		} else {
			pin.StatusIncomplete = true
		}
	}
	slices.Sort(lines)
	pin.Status, pin.StatusFingerprint = lines, hex.EncodeToString(h.Sum(nil))
	return nil
}

// dirDigest hashes a directory tree: every entry's path, type and mode, a
// regular file's content and a symlink's target. A missing directory has
// its own digest. Trees over planDirEntries entries or planDirBytes bytes
// cannot be pinned.
func dirDigest(root string) (string, error) {
	h := sha256.New()
	info, err := os.Lstat(root)
	switch {
	case os.IsNotExist(err):
		return digest([]byte("absent")), nil
	case err != nil:
		return "", err
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(root)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "root-link\x00%s\x00", target)
		if root, err = filepath.EvalSymlinks(root); err != nil {
			return digest([]byte("dangling\x00" + target)), nil
		}
	}
	entries, total := 0, int64(0)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entries++; entries > planDirEntries {
			return fmt.Errorf("too many entries")
		}
		rel, _ := filepath.Rel(root, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%s\x00", rel, info.Mode())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			h.Write([]byte(target + "\x00"))
			// What a linked hook runs is its target's content (a dangling
			// link counts by its text).
			if t, err := os.Stat(p); err == nil && t.Mode().IsRegular() {
				if total += t.Size(); total > planDirBytes {
					return fmt.Errorf("too large")
				}
				d, err := fileDigest(p)
				if err != nil {
					return err
				}
				h.Write([]byte(d))
			}
		case info.Mode().IsRegular():
			if total += info.Size(); total > planDirBytes {
				return fmt.Errorf("too large")
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			fh := sha256.New()
			_, err = io.Copy(fh, f)
			f.Close()
			if err != nil {
				return err
			}
			h.Write(fh.Sum(nil))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// changes names what differs from the pinned checkout.
func (a planCheckout) changes(b planCheckout) []string {
	var out []string
	if a.Branch != b.Branch {
		out = append(out, fmt.Sprintf("the checked-out branch changed from %q to %q", cmpOr(a.Branch, "(detached)"), cmpOr(b.Branch, "(detached)")))
	}
	if a.Head != b.Head {
		out = append(out, "HEAD moved from "+shortOid(a.Head)+" to "+shortOid(b.Head))
	}
	if a.Index != b.Index {
		out = append(out, "the index changed")
	}
	if a.IndexFlags != b.IndexFlags {
		out = append(out, "skip-worktree or assume-unchanged flags, or files carrying them, changed")
	}
	if a.StatusFingerprint != b.StatusFingerprint {
		changed := changedOutside(a.Status, b.Status)
		if a.StatusIncomplete || b.StatusIncomplete || len(changed) == 0 {
			out = append(out, "the working tree changed")
		} else {
			out = append(out, "the working tree changed: "+listPaths(changed))
		}
	}
	keys := make([]string, 0, len(planRepoParts))
	for k := range planRepoParts {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if a.Repo[k] != b.Repo[k] {
			out = append(out, planRepoParts[k]+" changed")
		}
	}
	return out
}
