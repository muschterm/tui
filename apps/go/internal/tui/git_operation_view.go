package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// git_operation_view.go paints the merge/rebase operation panel and results
// (git_operation.go).

// gitOperationBlocks is the operation panel shown while a merge, rebase,
// cherry-pick or revert is in progress; it replaces the passive banner.
func (m *Model) gitOperationBlocks(g *gitView) []surfaceBlock {
	p := m.colors()
	o := g.oper
	gap := surfaceBlock{kind: surfaceGapBlock}
	text := func(s, ink string) surfaceBlock { return surfaceBlock{kind: surfaceTextBlock, value: s, ink: ink} }
	pair := func(label, value string) surfaceBlock {
		return surfaceBlock{kind: surfacePairBlock, label: label, value: value}
	}
	title := gitOperationTitle(o.Kind)
	state := "here"
	if o.Source == protocol.GitOperationSourceExternal {
		state = "in a terminal"
	}
	step := ""
	if o.Steps > 0 {
		step = "step " + strconv.Itoa(o.Step) + "/" + strconv.Itoa(o.Steps)
	}
	glyph, ink := panelStatusMark(m, "blocked")
	// The title stays short so it fits beside its state; the source is a
	// pair of its own.
	b := []surfaceBlock{{kind: surfaceStatusBlock, label: title, value: step, glyph: glyph, ink: ink, bold: true},
		{kind: surfacePairBlock, label: "Started", value: state}}
	commit := func(c *protocol.GitOperationCommit) string {
		if c == nil {
			return ""
		}
		s := gitShort(c.Oid)
		if l := safe(singleLine(c.Label)); l != "" {
			s = l + " " + s
		}
		if sub := safe(singleLine(c.Subject)); sub != "" {
			s += " " + sub
		}
		return s
	}
	if o.Branch != "" {
		b = append(b, pair("Branch", safe(singleLine(o.Branch))))
	}
	if t := commit(o.Target); t != "" {
		label := "Merging"
		if o.Kind == protocol.GitOperationRebase {
			label = "Onto"
		}
		b = append(b, pair(label, t))
	}
	if c := commit(o.Current); c != "" && o.Kind != protocol.GitOperationMerge {
		b = append(b, pair("Current", c))
	}
	if o.Sides.Ours != "" || o.Sides.Theirs != "" {
		b = append(b, pair("Ours", safe(singleLine(o.Sides.Ours))), pair("Theirs", safe(singleLine(o.Sides.Theirs))))
	}
	if in := o.Interactive; in != nil && in.Plan {
		b = append(b, m.gitRebaseStopBlocks(o)...)
	} else if r := o.StopReason; r != "" {
		what := map[string]string{protocol.GitStopEdit: "an edit", protocol.GitStopExec: "an exec", protocol.GitStopBreak: "a break"}[r]
		if what == "" {
			what = "an interactive step"
		}
		b = append(b, text("Stopped for "+what+" · continue in a terminal", p.gold))
	}
	if g.operErr != "" {
		b = append(b, text("Showing the last result · "+g.operErr, p.muted))
	}
	if n := len(o.Conflicts); n > 0 {
		count := strconv.Itoa(n)
		if o.ConflictsTruncated {
			count += "+"
		}
		b = append(b, gap, surfaceBlock{kind: surfaceHeadingBlock, label: "Unmerged", value: count})
		for _, c := range o.Conflicts {
			path := safe(singleLine(c.Path))
			row := &gitRow{kind: "conflict", conflict: c, text: path, key: "git:conflict:" + c.Path,
				action: action{Kind: "git-open", Value: protocol.GitGroupConflicted, ID: c.Path},
				help:   "Open " + path + " · " + gitConflictLabel(c.Kind)}
			row.controls = m.gitConflictControls(c)
			if m.gitConflictsEnabled() {
				row.action = action{Kind: "git-conflict-view", ID: c.Path}
				row.help = "View versions · " + path + " · " + gitConflictLabel(c.Kind) + " · o/t choose · m resolved · e edit"
			}
			b = append(b, surfaceBlock{kind: surfaceGitBlock, git: row})
		}
		if o.ConflictsTruncated {
			b = append(b, text("Showing the first "+count[:len(count)-1]+" · more are unmerged", p.muted))
		}
	}
	for _, list := range []struct {
		title string
		paths []string
	}{{"In the way of the remaining commits:", o.ContinueInTheWay}, {"Hidden index entries (use a terminal):", o.HiddenEntries}, {"Nested repositories in the way:", o.NestedInTheWay}} {
		if len(list.paths) > 0 {
			b = append(b, text(list.title, p.gold))
			for _, path := range list.paths {
				b = append(b, text("  "+safe(singleLine(path)), p.text))
			}
		}
	}
	b = append(b, m.gitJobBlocks(o)...)
	b = append(b, gap)
	type act struct {
		kind, label, glyph string
		can                protocol.GitOperationAction
	}
	acts := []act{{"git-op-continue", "Continue…", "check", o.Can.Continue}}
	if o.Kind == protocol.GitOperationRebase {
		acts = append(acts, act{"git-op-skip", "Skip commit…", "next", o.Can.Skip})
	}
	if in := o.Interactive; in != nil && in.Plan && in.Stop == protocol.GitRebaseStopEmpty && !gitRebaseEmptyCommitted(o) {
		// Continue keeps the empty commit; Skip drops it.
		acts[0].label, acts[1].label = "Keep empty commit…", "Skip (drop) commit…"
	}
	acts = append(acts, act{"git-op-abort", "Abort " + safe(singleLine(o.Kind)) + "…", "close", o.Can.Abort})
	for _, a := range acts {
		if a.can.Allowed {
			b = append(b, surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: a.label, mark: m.icon(a.glyph),
				key: "git:" + strings.TrimPrefix(a.kind, "git-"), action: action{Kind: a.kind}, help: a.label}})
			if a.kind == "git-op-continue" && o.Interactive != nil && o.Interactive.Plan {
				b = append(b, m.gitRebaseStopActions(o)...)
			}
			continue
		}
		reason := safe(singleLine(a.can.Reason))
		if reason == "" {
			reason = "not available now"
		}
		b = append(b, text(strings.TrimSuffix(a.label, "…")+" unavailable · "+reason, p.muted))
		if a.kind == "git-op-continue" && o.Interactive != nil && o.Interactive.Plan {
			b = append(b, m.gitRebaseStopActions(o)...)
		}
	}
	return b
}

// gitConflictLabel names a conflict kind for help.
func gitConflictLabel(kind string) string {
	switch kind {
	case protocol.GitConflictBothModified:
		return "both modified"
	case protocol.GitConflictBothAdded:
		return "both added"
	case protocol.GitConflictDeletedByThem:
		return "deleted by theirs"
	case protocol.GitConflictDeletedByUs:
		return "deleted by ours"
	case protocol.GitConflictAddedByUs:
		return "added by ours"
	case protocol.GitConflictAddedByThem:
		return "added by theirs"
	case protocol.GitConflictBothDeleted:
		return "both deleted"
	}
	return "unmerged"
}

// gitConflictFlags are a conflict row's muted flags.
func gitConflictFlags(c protocol.GitConflict) string {
	var f []string
	if c.Binary {
		f = append(f, "binary")
	}
	if c.Submodule {
		f = append(f, "submodule")
	}
	if c.Symlink {
		f = append(f, "symlink")
	}
	return strings.Join(f, " ")
}

// paintGitConflictRow paints a conflict row: kind badge, path, flags and
// reserved S3 slots.
func (m *Model) paintGitConflictRow(f *frame, x, y, width int, r *gitRow, v componentVisual) {
	p := m.colors()
	bg := v.background
	cx, end := x+1, x+width-1
	kind := gitConflictBadge(r.conflict.Kind)
	f.text(cx, y, 2, kind, p.red, bg)
	cx += 3
	end -= 2 * gitSlotWidth
	m.paintGitControls(f, end+1, y, r, bg)
	end--
	if flags := gitConflictFlags(r.conflict); flags != "" {
		if fw := ansi.StringWidth(flags); fw+8 < end-cx {
			f.text(end-fw, y, fw, flags, p.muted, bg)
			end -= fw + 2
		}
	}
	room := end - cx
	f.componentText(cx, y, max(0, room), truncatePathLeft(r.text, room), v)
}

// gitOperationDoneCopy is the success notice of an operation command.
func gitOperationDoneCopy(st *gitWriteState, r *protocol.GitResult) string {
	o := r.Operation
	label := safe(singleLine(st.label))
	if o == nil {
		return "Done"
	}
	if st.cmd.Kind == protocol.GitKindOperationCommit && r.State == protocol.GitStateSucceeded {
		return "Committed " + gitShort(r.Commit) + " · the rebase is still stopped"
	}
	switch o.Outcome {
	case protocol.GitOutcomeStoppedConflicts:
		return gitRefCopy(st.cmd.Kind, "stopped_conflicts")
	case protocol.GitOutcomeStopped:
		return gitRefCopy(st.cmd.Kind, "stopped")
	case protocol.GitOutcomeAborted:
		if ref := safe(singleLine(o.BackupRef)); ref != "" {
			return "Aborted the " + safe(singleLine(o.Kind)) + " · commits made at stops kept at " + ref
		}
		return "Aborted the " + safe(singleLine(o.Kind))
	}
	switch st.cmd.Kind {
	case protocol.GitKindMerge:
		if o.Outcome == protocol.GitOutcomeCompleted {
			return "Merged " + label
		}
	case protocol.GitKindRebase:
		if o.Outcome == protocol.GitOutcomeCompleted {
			return "Rebased onto " + label
		}
	case protocol.GitKindOperationSkip:
		if o.Skipped != nil {
			return "Skipped " + gitShort(o.Skipped.Oid)
		}
	case protocol.GitKindOperationContinue:
		if o.Outcome == protocol.GitOutcomeCompleted {
			return "Finished the " + safe(singleLine(o.Kind))
		}
		return "Continued the " + safe(singleLine(o.Kind))
	}
	return "Done"
}

// gitOperationResultBlocks is an operation command's outcome at the source,
// with the backup's object IDs and recovery instructions.
func (m *Model) gitOperationResultBlocks(st *gitWriteState) []surfaceBlock {
	p := m.colors()
	text := func(s, ink string) surfaceBlock { return surfaceBlock{kind: surfaceTextBlock, value: s, ink: ink} }
	button := func(label, glyph, k string, a action) surfaceBlock {
		return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: label, mark: glyph, key: k, action: a, help: label}}
	}
	r := st.result
	var b []surfaceBlock
	switch {
	case r != nil && r.State == protocol.GitStateSucceeded:
		glyph, ink := panelStatusMark(m, "succeeded")
		if r.Code == "stopped_conflicts" || r.Code == "stopped" || r.Operation != nil && (r.Operation.Outcome == protocol.GitOutcomeStoppedConflicts || r.Operation.Outcome == protocol.GitOutcomeStopped) {
			glyph, ink = panelStatusMark(m, "blocked")
		}
		b = append(b, surfaceBlock{kind: surfaceStatusBlock, label: gitOperationDoneCopy(st, r), glyph: glyph, ink: ink})
		if r.Code == "abort_incomplete" || r.Code == "hook_failed" {
			b = append(b, text(gitRefCopy(st.cmd.Kind, r.Code), p.gold))
			for _, path := range r.Paths {
				b = append(b, text("  "+safe(singleLine(path)), p.text))
			}
		}
	default:
		failure := st.failure
		if failure == "" {
			failure = "Git refused the change"
		}
		b = append(b, text(failure, p.red))
		if r != nil && r.Code == "would_overwrite" {
			b = append(b, m.gitPathListBlocks(r)...)
		}
		if r != nil && r.Code == "nothing_to_commit" && st.cmd.Git.Operation != nil && st.cmd.Git.Operation.Kind == protocol.GitOperationRebase {
			b = append(b, button("Skip commit…", m.icon("next"), "git:result-skip", action{Kind: "git-op-skip"}))
		}
	}
	if r != nil && r.Operation != nil {
		o := r.Operation
		if len(o.RerereResolved) > 0 {
			b = append(b, text("Rewritten from recorded resolutions (still unmerged, review):", p.gold))
			for _, path := range o.RerereResolved {
				b = append(b, text("  "+safe(singleLine(path)), p.text))
			}
		}
		if len(o.StopCommits) > 0 {
			b = append(b, text("Committed at this stop:", p.muted))
			for _, c := range o.StopCommits {
				b = append(b, text("  "+gitShort(c.Oid)+" "+safe(singleLine(c.Subject)), p.text))
			}
		}
		if ref := safe(singleLine(o.BackupRef)); ref != "" {
			b = append(b, text("Backup ref "+ref+" keeps the commits made at stops", p.muted),
				text("Inspect: git log "+ref+" · delete: git update-ref -d "+ref, p.muted))
		}
		if n := len(o.BackupRefs); n > 0 {
			b = append(b, text("Further backup refs (commits HEAD did not reach):", p.muted))
			for i, r := range o.BackupRefs {
				if i == 5 {
					b = append(b, text("  and "+strconv.Itoa(n-5)+" more under refs/tui-go/rebase-backup/", p.muted))
					break
				}
				b = append(b, text("  "+safe(singleLine(r)), p.text))
			}
		}
		if bk := o.Backup; bk != nil && bk.Oid != "" {
			oid := safe(singleLine(bk.Oid))
			b = append(b, text("Backup "+gitShort(bk.Oid)+" · "+plural(len(bk.Paths), "file")+" copied before overwriting", p.muted),
				text("Recover a file: git show "+oid+":<path> > <path>", p.muted),
				text("Staged copy: git checkout "+safe(singleLine(bk.IndexOid))+" -- <path>", p.muted),
				text("Git may prune the backup after gc.pruneExpire (two weeks by default)", p.muted))
			if bk.Incomplete {
				b = append(b, text("Not backed up: "+plural(len(bk.Missing), "file"), p.gold))
			}
		}
	}
	if st.output != "" {
		b = append(b, button("View output", m.icon("terminal"), "git:output", action{Kind: "git-output"}))
	}
	if st.unknown {
		b = append(b, button("Refresh", m.icon("refresh"), "git:unknown-refresh", action{Kind: "git-refresh"}))
	}
	return b
}

// gitConflictBadge is a conflict kind's two-cell badge, sanitized.
func gitConflictBadge(kind string) string {
	kind = safe(singleLine(kind))
	if ansi.StringWidth(kind) != 2 {
		return "U?"
	}
	return kind
}
