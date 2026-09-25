package tui

import (
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Server-supplied Git strings may carry terminal controls; none may reach the
// rendered frame in the surface rows or the viewer title and pairs.
func TestGitSurfaceAndViewerSanitizeGitStrings(t *testing.T) {
	const osc, csi = "\x1b]2;PWNED\x07", "\x1b[31mREDX"
	m, api := gitModel(t, 144, 40)
	api.status.Upstream = "origin/" + osc + "up"
	api.status.Entries = []protocol.GitStatusEntry{
		{Path: "p" + osc + "ath" + csi, OrigPath: "o" + csi, Index: "R", Worktree: ".", Group: protocol.GitGroupStaged},
		{Path: "u\x9b31mC1" + "\r\bx", Index: "?", Worktree: "?", Group: protocol.GitGroupUntracked},
	}
	c := api.log.Commits[0]
	c.Subject, c.Refs, c.Author = "sub"+osc+csi, []string{"refs/heads/r" + osc}, "au"+csi
	api.log.Commits = []protocol.GitCommit{c}
	gitSettle(t, m, nil)
	check := func(stage string) {
		t.Helper()
		raw := strings.Join(m.render().rows, "\n")
		for _, bad := range []string{"PWNED", "\x07", "\x1b]", "\x1b[31mREDX", "\x9b", "\r", "\b"} {
			if strings.Contains(raw, bad) {
				t.Fatalf("%s: %q reached the frame", stage, bad)
			}
		}
	}
	check("surface")
	gitSettle(t, m, m.activate(action{Kind: "git-open", Value: protocol.GitGroupStaged, ID: api.status.Entries[0].Path}))
	check("diff viewer")
	api.show = protocol.GitShow{Commit: c, Text: "diff " + osc + "\n"}
	gitSettle(t, m, m.activate(action{Kind: "git-commit", ID: c.Hash, Value: c.Short + osc}))
	check("commit viewer")
}
