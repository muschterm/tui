package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// git_viewer.go opens a status entry's diff or a commit in the centered
// read-only viewer (attachment_viewer.go) as a Git content kind. The patch
// is fetched once per open; the text is untrusted and passes through safe()
// with the rest of the viewer's content, and only this file's inks style it.

// gitViewerContent is the Git part of an open viewer.
type gitViewerContent struct {
	target            client.GitTarget
	commit            bool
	path, group       string
	hash              string
	pairs             [][2]string
	binary, truncated bool
	bytes             int
	loaded            bool
	// key and entry pin what the viewer shows for writes (s/u/d); changed
	// marks a later status showing another Pin for it.
	key             string
	entry           protocol.GitStatusEntry
	pinned, changed bool
	// whole is a whole-group diff (path empty); compare holds the ref
	// compared against HEAD.
	whole   bool
	compare string
}

type gitViewerMsg struct {
	id      uint64
	diff    *protocol.GitDiff
	show    *protocol.GitShow
	compare *protocol.GitCompare
	err     error
}

// openGitViewer resolves a git-open (Value group, ID path) or git-commit (ID
// full hash) action into a loading viewer and its read.
func (m *Model) openGitViewer(a action) tea.Cmd {
	api := m.gitClient()
	if !m.connected || api == nil {
		return m.showNoticeAs(noticeUnavailable, "Connect to the server to read Git changes")
	}
	key, target := m.gitTarget()
	origin := m.focus
	if origin == "" || strings.HasPrefix(origin, "menu:") || strings.HasPrefix(origin, "viewer-") {
		origin = "right-body"
	}
	content := &gitViewerContent{target: target}
	att := protocol.Attachment{Kind: "git-diff"}
	switch a.Kind {
	case "git-whole":
		content.group, content.whole = a.Value, true
		att.Name = "Staged vs HEAD"
		if a.Value == protocol.GitGroupUnstaged {
			att.Name = "Unstaged changes"
		}
	case "git-compare":
		content.compare = a.ID
		att.Name = safe(singleLine(a.Value)) + " vs HEAD"
	case "git-commit":
		content.commit, content.hash = true, a.ID
		att.Name = safe(singleLine(a.Value))
		if g := m.currentGitView(); g != nil && g.log != nil {
			for _, c := range g.log.Commits {
				if c.Hash == a.ID {
					att.Name = safe(singleLine(c.Short + " " + c.Subject))
					content.pairs = gitCommitPairs(c)
				}
			}
		}
	default:
		content.path, content.group, content.key = a.ID, a.Value, key
		if e, ok := m.gitViews[key].entry(a.Value, a.ID); ok && gitEntryWritable(e) {
			content.entry, content.pinned = e, true
		}
		att.Name = safe(singleLine(a.ID)) + " · " + gitGroupLabel(a.Value)
	}
	m.menu = nil
	m.projectMode = ""
	m.contextMenu = nil
	m.selecting, m.selectedText = false, ""
	m.hover = ""
	m.drag = shell.NoDivider
	m.scrollDrag = ""
	release := m.releaseViewerImage()
	m.viewerSeq++
	id := m.viewerSeq
	m.viewer = &attachmentViewer{att: att, origin: origin, git: content, loadID: id, loading: true}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	load := func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if content.commit {
			show, err := api.GitShow(deadline, target, content.hash)
			return gitViewerMsg{id: id, show: &show, err: err}
		}
		if content.compare != "" {
			// Base is the branch, head is HEAD: ahead counts HEAD's commits.
			c, err := api.GitCompare(deadline, target, content.compare, "HEAD")
			return gitViewerMsg{id: id, compare: &c, err: err}
		}
		diff, err := api.GitDiff(deadline, target, content.path, content.group)
		return gitViewerMsg{id: id, diff: &diff, err: err}
	}
	return tea.Batch(release, m.setFocus("viewer-body"), load)
}

// acceptGitViewer applies a read to the viewer open that requested it.
func (m *Model) acceptGitViewer(msg gitViewerMsg) {
	vw := m.viewer
	if vw == nil || vw.git == nil || vw.loadID != msg.id {
		return
	}
	g := vw.git
	vw.loading = false
	vw.clean, vw.cacheKey, vw.cacheLines = nil, "", nil
	if msg.err != nil {
		vw.loadErr = safe(singleLine(msg.err.Error()))
		return
	}
	g.loaded = true
	switch {
	case msg.show != nil:
		s := msg.show
		g.pairs = gitCommitPairs(s.Commit)
		g.binary, g.truncated, g.bytes = s.Binary, s.Truncated, s.Bytes
		if s.Commit.Short != "" {
			vw.att.Name = safe(singleLine(s.Commit.Short + " " + s.Commit.Subject))
		}
		// The subject is already the title; the body follows it.
		text := strings.TrimRight(s.Commit.Body, "\n")
		if first, rest, _ := strings.Cut(text, "\n"); strings.TrimSpace(first) == strings.TrimSpace(s.Commit.Subject) {
			text = strings.TrimLeft(rest, "\n")
		}
		if text != "" && s.Text != "" {
			text += "\n\n"
		}
		vw.att.Content = text + s.Text
	case msg.compare != nil:
		c := msg.compare
		g.pairs = gitComparePairs(c)
		g.binary, g.truncated, g.bytes = c.Binary, c.Truncated, c.Bytes
		vw.att.Content = gitCompareText(c)
	case msg.diff != nil:
		d := msg.diff
		g.binary, g.truncated, g.bytes = d.Binary, d.Truncated, d.Bytes
		if !d.Binary {
			vw.att.Content = d.Text
		}
	}
}

func gitCommitPairs(c protocol.GitCommit) [][2]string {
	var pairs [][2]string
	if c.Author != "" {
		author := safe(singleLine(c.Author))
		if c.Email != "" {
			author += " <" + safe(singleLine(c.Email)) + ">"
		}
		pairs = append(pairs, [2]string{"Author", author})
	}
	if t, err := time.Parse(time.RFC3339, c.Time); err == nil {
		pairs = append(pairs, [2]string{"Date", t.Local().Format("2006-01-02 15:04 MST")})
	}
	pairs = append(pairs, [2]string{"Commit", safe(singleLine(c.Hash))})
	if len(c.Refs) > 0 {
		refs := make([]string, len(c.Refs))
		for i, r := range c.Refs {
			refs[i] = safe(singleLine(gitRefLabel(r)))
		}
		pairs = append(pairs, [2]string{"Refs", strings.Join(refs, ", ")})
	}
	return pairs
}

func (m *Model) gitViewerPairs() [][2]string {
	g := m.viewer.git
	var pairs [][2]string
	switch {
	case g.commit || g.compare != "":
		pairs = append(pairs, g.pairs...)
	case g.whole:
		pairs = append(pairs, [2]string{"Group", gitGroupLabel(g.group)})
	default:
		pairs = append(pairs, [2]string{"Path", safe(singleLine(g.path))}, [2]string{"Group", gitGroupLabel(g.group)})
		switch {
		case g.changed:
			pairs = append(pairs, [2]string{"Status", gitViewerChangedCopy})
		case g.pinned && m.gitWritesEnabled():
			if k := gitViewerKeys(g.group); k != "" {
				pairs = append(pairs, [2]string{"Keys", k})
			}
		}
	}
	if g.loaded {
		pairs = append(pairs, [2]string{"Size", attachmentSize(g.bytes)})
	}
	return pairs
}

// gitViewerState is the honest state shown instead of a patch.
func (m *Model) gitViewerState() []string {
	vw := m.viewer
	g := vw.git
	switch {
	case vw.loading:
		if g.commit {
			return []string{"Loading commit…"}
		}
		if g.compare != "" {
			return []string{"Loading comparison…"}
		}
		return []string{"Loading diff…"}
	case vw.loadErr != "":
		return []string{"Diff unavailable · " + vw.loadErr}
	case !g.commit && g.compare == "" && g.binary:
		return []string{"Binary file; no text diff"}
	case vw.att.Content == "":
		return []string{"No text changes"}
	}
	return nil
}

// gitViewerNotice is the muted row after a truncated patch. It is not part
// of the source text.
func (m *Model) gitViewerNotice(width int) []viewerLine {
	vw := m.viewer
	if vw.git == nil || !vw.git.truncated {
		return nil
	}
	out := []viewerLine{{text: ""}}
	for _, part := range hardWrap("Diff truncated at 512 KiB", max(1, width)) {
		out = append(out, viewerLine{text: part, ink: m.colors().muted})
	}
	return out
}

type gitLineStyle struct {
	ink  string
	bold bool
}

// gitViewerStyles colors sanitized patch lines: file headers muted (the
// "diff --git" line bold), hunk headers accent, additions green and
// removals red. Inside a hunk every line is content, so a removed line
// starting "--" is still a removal. Commit bodies and stats stay default.
func (m *Model) gitViewerStyles(lines []string) []gitLineStyle {
	if m.viewer == nil || m.viewer.git == nil {
		return nil
	}
	p := m.colors()
	out := make([]gitLineStyle, len(lines))
	inDiff, inHunk := false, false
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "diff --cc ") || strings.HasPrefix(line, "diff --combined "):
			inDiff, inHunk = true, false
			out[i] = gitLineStyle{ink: p.muted, bold: true}
		case inDiff && strings.HasPrefix(line, "@@"):
			inHunk = true
			out[i] = gitLineStyle{ink: p.blue}
		case inHunk && strings.HasPrefix(line, "+"):
			out[i] = gitLineStyle{ink: p.green}
		case inHunk && strings.HasPrefix(line, "-"):
			out[i] = gitLineStyle{ink: p.red}
		case inHunk && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\\") || line == ""):
		case inDiff:
			inHunk = false
			out[i] = gitLineStyle{ink: p.muted}
		}
	}
	return out
}

// gitComparePairs are the comparison header pairs. Base is the chosen
// branch and Head is HEAD, so Ahead counts HEAD's own commits.
func gitComparePairs(c *protocol.GitCompare) [][2]string {
	short := func(oid string) string { return safe(singleLine(oid[:min(len(oid), 12)])) }
	pairs := [][2]string{
		{"Base", safe(singleLine(gitRefLabel(c.Base))) + " " + short(c.BaseOid)},
		{"Head", safe(singleLine(gitRefLabel(c.Head))) + " " + short(c.HeadOid)},
	}
	if c.MergeBase != "" {
		pairs = append(pairs, [2]string{"Merge base", short(c.MergeBase)})
	} else {
		pairs = append(pairs, [2]string{"Merge base", "none · unrelated histories"})
	}
	pairs = append(pairs, [2]string{"Ahead", strconv.Itoa(c.Ahead)}, [2]string{"Behind", strconv.Itoa(c.Behind)})
	if strings.HasPrefix(c.Base, "refs/remotes/") || strings.HasPrefix(c.Head, "refs/remotes/") {
		fetched := "never fetched"
		if t, err := time.Parse(time.RFC3339, c.FetchedAt); err == nil {
			fetched = t.Local().Format("2006-01-02 15:04 MST")
		}
		pairs = append(pairs, [2]string{"Upstream as of", fetched})
	}
	return pairs
}

// gitCompareText is the viewer body: HEAD's commits, the base's commits,
// then the merge-base diff. It is untrusted text sanitized by the viewer.
func gitCompareText(c *protocol.GitCompare) string {
	var b strings.Builder
	list := func(title string, n int, commits []protocol.GitCommit) {
		fmt.Fprintf(&b, "%s (%d)\n", title, n)
		for _, cm := range commits {
			fmt.Fprintf(&b, "  %s %s\n", singleLine(cm.Short), singleLine(cm.Subject))
		}
		if len(commits) < n {
			fmt.Fprintf(&b, "  … %d more\n", n-len(commits))
		}
		b.WriteString("\n")
	}
	list("Ahead · only in HEAD", c.Ahead, c.AheadCommits)
	list("Behind · only in base", c.Behind, c.BehindCommits)
	switch {
	case c.MergeBase == "":
		b.WriteString("No merge base; no diff\n")
	case c.Binary && c.Text == "":
		b.WriteString("Binary changes only\n")
	case c.Text == "":
		b.WriteString("No changes since the merge base\n")
	default:
		b.WriteString(c.Text)
	}
	return b.String()
}
