package tui

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// git_rebase.go is the interactive rebase of ADR 0026 in the Git surface:
// a plan editor that takes over the surface body (like the resolution job
// form and the conflict viewer, so it scrolls with the body and follows the
// shared maximize control), the commit-row presets that open it preloaded,
// the confirmation that summarises the rewrite, and the actions of an
// application rebase's stops (git_operation_view.go paints them).
//
// The editor lists the plan newest first, like the commit graph; entries
// are kept in execution order (oldest first, what Git runs), so display
// row d is entry len-1-d. A squash or fixup row melds into the row below
// it. The draft is client-local per Git target: it survives hiding the
// surface, switching threads and Back, and ends only on an explicit discard
// (confirmed when edited) or once the server accepted the start. Messages
// are the editor's own: reword messages live on their entry and a chain's
// combined message in chainMsg (keyed by the chain's first commit); build
// places it on the chain's last entry as protocol.ValidateRebasePlan
// requires, prefilled with the combined messages when the user wrote none.

// gitRebaseMsgKey is the focus key of the rebase message editor.
const gitRebaseMsgKey = "git:rb-msg"

// gitRebaseAPI reads plans; tests inject a fake.
type gitRebaseAPI interface {
	GitRebasePlan(ctx context.Context, target client.GitTarget, base, onto string) (protocol.GitRebasePlan, error)
}

var _ gitRebaseAPI = (*client.Client)(nil)

// Presets (commit-row menu) applied once the plan is read.
const (
	gitRebasePresetFrom     = "from"
	gitRebasePresetReword   = "reword"
	gitRebasePresetEdit     = "edit"
	gitRebasePresetSquash   = "squash"
	gitRebasePresetFixup    = "fixup"
	gitRebasePresetDrop     = "drop"
	gitRebasePresetMoveUp   = "move-up"
	gitRebasePresetMoveDown = "move-down"
)

// gitRebaseDraft is one target's plan being arranged.
type gitRebaseDraft struct {
	key    string
	target client.GitTarget
	// base and onto are exactly what the plan was requested with.
	base, onto string
	plan       protocol.GitRebasePlan
	loaded     bool
	// entries run in this order; Message is set only on reword entries.
	entries []protocol.GitRebaseEntry
	// chainMsg is the user's combined message per chain, keyed by the
	// chain's first commit.
	chainMsg map[string]string
	// rewordMsg keeps reword text of entries turned back to another
	// action, restored when they are reworded again.
	rewordMsg                           map[string]string
	updateRefs, ackMerges, ackPublished bool
	// open shows the editor in place of the surface body.
	open    bool
	loading bool
	seq     uint64
	// err is a read failure or the server's refusal of the start; stale
	// marks a plan whose repository moved on; notice summarises a refresh.
	err, notice string
	stale       bool
	// sentID is the start command in flight; the editor hides meanwhile.
	sentID string
	// rev counts changes, so a confirmation acts only on what it showed.
	rev int
	// preset and presetCommit apply once, after the first read; agent
	// entries replace the default arrangement (slice 6).
	preset, presetCommit string
	agent                []protocol.GitRebaseEntry
	// fromAgent is set when the plan was loaded from an agent's proposal.
	fromAgent *gitRebaseAgentInfo
}

// gitRebaseMsgEdit is an open message editor: a reword or a chain of the
// plan, or a stop's Continue or Commit.
type gitRebaseMsgEdit struct {
	key     string
	purpose string // reword, chain, continue, commit
	commit  string // reword: the entry's commit; chain: the chain's first commit
	text    string
	// chain is the chain's key; initial the text shown at open; orig the
	// original (or default combined) message as displayed, exact when
	// exact; warn what an edit cannot preserve. revert restores the entry
	// when a reword chosen just now is left unchanged or cancelled.
	chain, initial, orig string
	from                 string // the commit whose row opened it
	exact                bool
	// stored is set when the entry or chain already carries a message
	// (written, or from an agent plan); unchanged, it is kept byte for
	// byte.
	stored bool
	warn   string
	revert *protocol.GitRebaseEntry
	// state is the stop the continue or commit message is for.
	state protocol.GitOperationState
}

// gitRebaseConfirm is the shown start confirmation, bound to the draft
// revision it summarised.
type gitRebaseConfirm struct {
	key string
	rev int
}

// gitRebaseStopCommit is the shown git.operation_commit confirmation.
type gitRebaseStopCommit struct {
	key     string
	target  client.GitTarget
	state   protocol.GitOperationState
	message string
}

type gitRebaseUI struct {
	drafts map[string]*gitRebaseDraft
	edits  map[string]*gitRebaseMsgEdit
	msg    textarea.Model
	ready  bool
	// msgFor is the edit whose text the shared textarea holds.
	msgFor *gitRebaseMsgEdit
	seq    uint64
	// Drag reordering: the entry index under the pointer.
	dragging bool
	drag     int
	confirm  *gitRebaseConfirm
	commit   *gitRebaseStopCommit
	// replace is an open request waiting for the replace confirmation.
	replace *gitRebaseDraft
	discard string // key whose discard confirmation is shown
	// agentInfo marks the next opened draft as an agent's proposal.
	agentInfo *gitRebaseAgentInfo
}

type gitRebasePlanMsg struct {
	key  string
	seq  uint64
	plan protocol.GitRebasePlan
	err  error
}

// gitRebaseEnabled reports a server with interactive rebase.
func (m *Model) gitRebaseEnabled() bool {
	return m.gitOperationsEnabled() && slices.Contains(m.snapshot.Capabilities, "git-rebase-interactive") && m.gitRebaseClient() != nil
}

func (m *Model) gitRebaseClient() gitRebaseAPI {
	if a, ok := m.gitClient().(gitRebaseAPI); ok {
		return a
	}
	return nil
}

func (m *Model) gitRebaseMsg() *textarea.Model {
	if !m.gitRB.ready {
		m.gitRB.msg = newInput("Commit message")
		m.gitRB.msg.CharLimit = protocol.GitRebaseMessageMax
		m.gitRB.ready = true
	}
	return &m.gitRB.msg
}

// gitRebaseDraftFor is the displayed target's draft, or nil.
func (m *Model) gitRebaseDraftFor(key string) *gitRebaseDraft {
	if m.gitRB.drafts == nil {
		return nil
	}
	return m.gitRB.drafts[key]
}

// ---- Plan model ----

func gitRebaseChainMember(e protocol.GitRebaseEntry) bool {
	return e.Action == protocol.GitRebaseSquash || e.Action == protocol.GitRebaseFixup
}

// gitRebaseChainStart is the first entry of the chain entry i belongs to.
func gitRebaseChainStart(entries []protocol.GitRebaseEntry, i int) int {
	for i > 0 && gitRebaseChainMember(entries[i]) {
		i--
	}
	return i
}

// gitRebaseChainEnd is the last entry of the chain starting at i.
func gitRebaseChainEnd(entries []protocol.GitRebaseEntry, i int) int {
	for i+1 < len(entries) && gitRebaseChainMember(entries[i+1]) {
		i++
	}
	return i
}

// gitRebaseChainNeedsMessage reports a chain (start..end) whose combined
// message the user chooses: it contains a squash or a fixup -c.
func gitRebaseChainNeedsMessage(entries []protocol.GitRebaseEntry, start, end int) bool {
	for _, e := range entries[start+1 : end+1] {
		if e.Action == protocol.GitRebaseSquash || e.Fixup == protocol.GitRebaseFixupEditMessage {
			return true
		}
	}
	return false
}

func (d *gitRebaseDraft) commit(oid string) (protocol.GitRebasePlanCommit, bool) {
	for _, c := range d.plan.Commits {
		if c.Oid == oid {
			return c, true
		}
	}
	return protocol.GitRebasePlanCommit{}, false
}

// gitRebaseRaw is a commit's exact message when the plan carries it whole
// (servers without Message, or a message over 64 KiB, do not).
func gitRebaseRaw(c protocol.GitRebasePlanCommit) (string, bool) {
	if c.Message == "" || c.MessageTruncated {
		return "", false
	}
	return c.Message, true
}

// gitRebaseDisplay is a commit's message for the editor: the raw message
// (else the subject and body the plan read) without control characters or
// escape sequences. The editor also shows tabs as spaces.
func gitRebaseDisplay(c protocol.GitRebasePlanCommit) string {
	if raw, ok := gitRebaseRaw(c); ok {
		return safeKeepTabs(raw)
	}
	msg := safeKeepTabs(c.Subject)
	if b := strings.TrimRight(safeKeepTabs(c.Body), "\n"); b != "" {
		msg += "\n\n" + b
	}
	return msg
}

// gitRebaseChainKey identifies chain start..end by its exact ordered
// members and their actions, so a combined message never carries over to
// a different chain.
func gitRebaseChainKey(entries []protocol.GitRebaseEntry, start, end int) string {
	// The start's own action (pick, edit or reword) is not part of the key;
	// its reword text is kept separately.
	parts := []string{entries[start].Commit}
	for _, e := range entries[start+1 : end+1] {
		parts = append(parts, e.Commit+"/"+e.Action+"/"+e.Fixup)
	}
	return strings.Join(parts, ",")
}

// chainParts are the messages combined for chain start..end: the first
// commit's (or its reword), then each squash's and fixup -C/-c's. raw
// uses the exact messages (ok is false when one is not available);
// otherwise the editor's display forms.
func (d *gitRebaseDraft) chainParts(start, end int, raw bool) ([]string, bool) {
	var parts []string
	ok := true
	for i := start; i <= end; i++ {
		e := d.entries[i]
		if i > start && e.Action == protocol.GitRebaseFixup && e.Fixup == "" {
			continue
		}
		if e.Action == protocol.GitRebaseReword && e.Message != "" {
			if raw {
				parts = append(parts, e.Message)
			} else {
				parts = append(parts, safeKeepTabs(e.Message))
			}
			continue
		}
		c, found := d.commit(e.Commit)
		if !found {
			ok = false
			continue
		}
		if !raw {
			parts = append(parts, gitRebaseDisplay(c))
			continue
		}
		m, has := gitRebaseRaw(c)
		ok = ok && has
		parts = append(parts, m)
	}
	return parts, ok
}

// chainDefault is the combined message of chain start..end: the parts
// with trailing newlines removed, joined by one blank line. ok is false
// when an exact original is missing (the user must write the message).
func (d *gitRebaseDraft) chainDefault(start, end int) (string, bool) {
	parts, ok := d.chainParts(start, end, true)
	for i := range parts {
		parts[i] = strings.TrimRight(parts[i], "\n")
	}
	return strings.Join(parts, "\n\n"), ok
}

// chainDisplay is chainDefault as the editor shows it.
func (d *gitRebaseDraft) chainDisplay(start, end int) string {
	parts, _ := d.chainParts(start, end, false)
	for i := range parts {
		parts[i] = strings.TrimRight(parts[i], "\n")
	}
	return strings.Join(parts, "\n\n")
}

// chainMessage is the combined message a chain carries now: the user's,
// else the exact default ("" when that is not available).
func (d *gitRebaseDraft) chainMessage(start, end int) string {
	if msg, ok := d.chainMsg[gitRebaseChainKey(d.entries, start, end)]; ok {
		return msg
	}
	if msg, ok := d.chainDefault(start, end); ok {
		return msg
	}
	return ""
}

// build is the plan as sent: each chain's message moved to its last entry.
func (d *gitRebaseDraft) build() []protocol.GitRebaseEntry {
	out := slices.Clone(d.entries)
	for i := range out {
		if out[i].Action != protocol.GitRebaseReword {
			out[i].Message = ""
		}
	}
	for i := range out {
		if !gitRebaseChainMember(out[i]) || gitRebaseChainEnd(out, i) != i {
			continue
		}
		start := gitRebaseChainStart(out, i)
		if gitRebaseChainNeedsMessage(out, start, i) {
			out[i].Message = d.chainMessage(start, i)
		}
	}
	return out
}

// problem is why the plan cannot start now ("" when it can).
func (d *gitRebaseDraft) problem() string {
	switch {
	case !d.loaded:
		return "The plan is not read yet"
	case d.plan.Blocked != "":
		if msg := safe(singleLine(d.plan.BlockedMessage)); msg != "" {
			return gitRefCopy(protocol.GitKindRebase, d.plan.Blocked) + " · " + msg
		}
		return gitRefCopy(protocol.GitKindRebase, d.plan.Blocked)
	}
	for i := range d.entries {
		if !gitRebaseChainMember(d.entries[i]) || gitRebaseChainEnd(d.entries, i) != i {
			continue
		}
		start := gitRebaseChainStart(d.entries, i)
		if gitRebaseChainNeedsMessage(d.entries, start, i) && d.chainMessage(start, i) == "" {
			return "Write the combined message of the squash ending at entry " + strconv.Itoa(i+1) + " (m) · the original messages are not fully available here"
		}
	}
	if err := protocol.ValidateRebasePlan(d.plan, d.build()); err != nil {
		return safe(singleLine(err.Message))
	}
	switch {
	case d.plan.MergeCount > 0 && !d.ackMerges:
		return "Acknowledge that merge commits are dropped first"
	case d.plan.Published && !d.ackPublished:
		return "Acknowledge rewriting published commits first"
	case d.updateRefs && len(d.plan.UpdateRefsUnsupported) > 0:
		return "Some branches cannot be moved here · turn Update refs off"
	}
	return ""
}

// gitRebaseMessageApplies reports whether Continue may carry a message at
// this stop: the continue commits a pick, reword or edit step (not inside a
// squash or fixup chain, a break or an empty commit, where the server
// refuses one). Chain ends that carry the plan's message are not offered,
// since the stop does not say whether the plan carries one.
func gitRebaseMessageApplies(in *protocol.GitRebaseProgress) bool {
	switch in.Stop {
	case protocol.GitRebaseStopEditAmend, protocol.GitRebaseStopEditReset:
		if !in.Staged {
			return false
		}
	case protocol.GitRebaseStopMessage, protocol.GitRebaseStopCommitFailed, protocol.GitRebaseStopConflict:
	default:
		return false
	}
	word, _, _ := strings.Cut(strings.TrimSpace(in.Command), " ")
	switch word {
	case "pick", "p", "reword", "r", "edit", "e":
		return true
	}
	return false
}

// gitRebaseStale reports a plan the repository moved away from: refused
// as stale by the server, or read at another branch or HEAD than shown.
func (m *Model) gitRebaseStale(d *gitRebaseDraft) bool {
	if d.stale {
		return true
	}
	g := m.gitViews[d.key]
	return d.loaded && g != nil && g.status != nil && g.status.Operation == "" && (g.status.HeadOid != d.plan.HeadOid || g.status.Branch != d.plan.Branch)
}

// gitRebaseProblem is why the draft cannot start now ("" when it can).
func (m *Model) gitRebaseProblem(d *gitRebaseDraft) string {
	if p := d.problem(); p != "" {
		return p
	}
	if m.gitRebaseStale(d) {
		return "The repository changed since the plan was read · Refresh plan (keeps your edits)"
	}
	return ""
}

// invalidEntry is the entry index a validation error names, or -1.
func (d *gitRebaseDraft) invalidEntry() int {
	if !d.loaded {
		return -1
	}
	err := protocol.ValidateRebasePlan(d.plan, d.build())
	if err == nil {
		return -1
	}
	rest, ok := strings.CutPrefix(err.Message, "entry ")
	if !ok {
		return -1
	}
	n, _, _ := strings.Cut(rest, ":")
	i, convErr := strconv.Atoi(n)
	if convErr != nil {
		return -1
	}
	return i - 1
}

// edited reports changes the user would lose on discard.
func (d *gitRebaseDraft) edited() bool {
	if !d.loaded {
		return false
	}
	def := client.DefaultRebaseEntries(d.plan)
	live := false
	for i := range d.entries {
		if gitRebaseChainMember(d.entries[i]) && gitRebaseChainEnd(d.entries, i) == i {
			_, ok := d.chainMsg[gitRebaseChainKey(d.entries, gitRebaseChainStart(d.entries, i), i)]
			live = live || ok
		}
	}
	return !slices.Equal(def, d.entries) || live || d.updateRefs || d.ackMerges || d.ackPublished
}

// rewordReplaced reports a reword whose text never reaches history: entry
// i starts a chain whose written combined message replaces it. (A default
// combined message is built from the reword, so it is not replaced.)
func (d *gitRebaseDraft) rewordReplaced(i int) bool {
	if i < 0 || i+1 >= len(d.entries) || d.entries[i].Action != protocol.GitRebaseReword || !gitRebaseChainMember(d.entries[i+1]) {
		return false
	}
	end := gitRebaseChainEnd(d.entries, i)
	if !gitRebaseChainNeedsMessage(d.entries, i, end) {
		return false
	}
	_, written := d.chainMsg[gitRebaseChainKey(d.entries, i, end)]
	return written
}

// messageCounts are the messages a start sends: written (rewords that
// reach history and written combined messages), combined by default, and
// rewords a written combined message replaces; firstDefault is the end
// entry of the first default-combined chain (-1 when none).
func (d *gitRebaseDraft) messageCounts() (written, byDefault, replaced, firstDefault int) {
	firstDefault = -1
	for i, e := range d.entries {
		if e.Action == protocol.GitRebaseReword && e.Message != "" {
			if d.rewordReplaced(i) {
				replaced++
			} else {
				written++
			}
		}
		if !gitRebaseChainMember(e) || gitRebaseChainEnd(d.entries, i) != i {
			continue
		}
		start := gitRebaseChainStart(d.entries, i)
		if !gitRebaseChainNeedsMessage(d.entries, start, i) {
			continue
		}
		if _, ok := d.chainMsg[gitRebaseChainKey(d.entries, start, i)]; ok {
			written++
		} else {
			byDefault++
			if firstDefault < 0 {
				firstDefault = i
			}
		}
	}
	return
}

// changed records a change. Written messages are never dropped here: a
// combined message belongs to its exact chain (the key), so it returns
// when a transient move or action change restores the chain, and is only
// discarded with the draft.
func (d *gitRebaseDraft) changed() {
	d.rev++
	d.notice = ""
}

// move moves entry from to position to.
func (d *gitRebaseDraft) move(from, to int) bool {
	if from < 0 || from >= len(d.entries) || to < 0 || to >= len(d.entries) || from == to {
		return false
	}
	e := d.entries[from]
	d.entries = slices.Delete(d.entries, from, from+1)
	d.entries = slices.Insert(d.entries, to, e)
	d.changed()
	return true
}

// entryIndex is the entry naming commit, or -1.
func (d *gitRebaseDraft) entryIndex(commit string) int {
	return slices.IndexFunc(d.entries, func(e protocol.GitRebaseEntry) bool { return e.Commit == commit && commit != "" })
}

// setAction changes entry i's action; variant is the edit mode or fixup
// variant. It reports whether a message editor should open.
func (d *gitRebaseDraft) setAction(i int, act, variant string) (reword bool) {
	if i < 0 || i >= len(d.entries) {
		return false
	}
	e := &d.entries[i]
	if e.Action == protocol.GitRebaseBreak {
		return false
	}
	prev := *e
	e.Action, e.EditMode, e.Fixup = act, "", ""
	switch act {
	case protocol.GitRebaseEdit:
		if variant == protocol.GitRebaseEditAmend {
			e.EditMode = protocol.GitRebaseEditAmend
		}
	case protocol.GitRebaseFixup:
		e.Fixup = variant
	case protocol.GitRebaseReword:
		// The message is set only when the user writes one; text written
		// earlier for this entry returns.
		e.Message = prev.Message
		if e.Message == "" {
			e.Message = d.rewordMsg[e.Commit]
		}
		reword = true
	}
	if act != protocol.GitRebaseReword {
		if prev.Message != "" {
			if d.rewordMsg == nil {
				d.rewordMsg = map[string]string{}
			}
			d.rewordMsg[e.Commit] = prev.Message
		}
		e.Message = ""
	}
	if *e != prev {
		d.changed()
	}
	return reword
}

// insertBreak adds a break that runs right after entry i (-1: before all).
func (d *gitRebaseDraft) insertBreak(i int) int {
	at := min(len(d.entries), max(0, i+1))
	d.entries = slices.Insert(d.entries, at, protocol.GitRebaseEntry{Action: protocol.GitRebaseBreak})
	d.changed()
	return at
}

// applyEntries replaces the arrangement with entries (an agent's plan):
// chain messages move to chainMsg so the editor can show and edit them.
func (d *gitRebaseDraft) applyEntries(entries []protocol.GitRebaseEntry) {
	d.entries = slices.Clone(entries)
	d.chainMsg = map[string]string{}
	for i := range d.entries {
		e := &d.entries[i]
		if gitRebaseChainMember(*e) && e.Message != "" {
			d.chainMsg[gitRebaseChainKey(d.entries, gitRebaseChainStart(d.entries, i), i)] = e.Message
		}
		if e.Action != protocol.GitRebaseReword {
			e.Message = ""
		}
	}
	d.changed()
}

// mergeRefresh rebases the user's arrangement onto a fresh read: entries
// whose commit remains keep their action, message and relative order;
// breaks stay; new commits are picked after their predecessor in the new
// plan. It reports the commits added and those no longer in range.
func (d *gitRebaseDraft) mergeRefresh(plan protocol.GitRebasePlan) (added, removed []string) {
	fresh := map[string]bool{}
	for _, c := range plan.Commits {
		if !c.Merge {
			fresh[c.Oid] = true
		}
	}
	var kept []protocol.GitRebaseEntry
	have := map[string]bool{}
	for _, e := range d.entries {
		if e.Action == protocol.GitRebaseBreak || fresh[e.Commit] {
			kept = append(kept, e)
			have[e.Commit] = true
			continue
		}
		removed = append(removed, gitShort(e.Commit))
	}
	prev := ""
	for _, c := range plan.Commits {
		if c.Merge {
			continue
		}
		if !have[c.Oid] {
			at := 0
			if prev != "" {
				at = slices.IndexFunc(kept, func(e protocol.GitRebaseEntry) bool { return e.Commit == prev }) + 1
			}
			// Never split a squash or fixup chain: insert after its end.
			for at < len(kept) && gitRebaseChainMember(kept[at]) {
				at++
			}
			kept = slices.Insert(kept, at, protocol.GitRebaseEntry{Action: protocol.GitRebasePick, Commit: c.Oid})
			have[c.Oid] = true
			added = append(added, gitShort(c.Oid))
		}
		prev = c.Oid
	}
	d.plan, d.entries = plan, kept
	if len(plan.UpdateRefs) == 0 {
		d.updateRefs = false
	}
	if plan.MergeCount == 0 {
		d.ackMerges = false
	}
	if !plan.Published {
		d.ackPublished = false
	}
	d.changed()
	return added, removed
}

// counts summarises the rewrite for the confirmation.
func (d *gitRebaseDraft) counts() (reordered, reworded, melded, dropped, stops, breaks int) {
	def := client.DefaultRebaseEntries(d.plan)
	pos := map[string]int{}
	for i, e := range def {
		pos[e.Commit] = i
	}
	var seq []int
	for _, e := range d.entries {
		switch e.Action {
		case protocol.GitRebaseBreak:
			breaks++
			continue
		case protocol.GitRebaseReword:
			if !d.rewordReplaced(slices.Index(d.entries, e)) {
				reworded++
			}
		case protocol.GitRebaseSquash, protocol.GitRebaseFixup:
			melded++
		case protocol.GitRebaseDrop:
			dropped++
		case protocol.GitRebaseEdit:
			stops++
		}
		seq = append(seq, pos[e.Commit])
	}
	// Moved: the fewest entries whose moving explains the order (all but a
	// longest increasing subsequence).
	var tails []int
	for _, x := range seq {
		j, _ := slices.BinarySearch(tails, x)
		if j == len(tails) {
			tails = append(tails, x)
		} else {
			tails[j] = x
		}
	}
	reordered = len(seq) - len(tails)
	return
}

// ---- Entry points ----

// openGitRebase opens the editor for the displayed target: base and onto
// as the plan read takes them, a preset applied to presetCommit once the
// plan is read, or an agent's entries (slice 6) instead of the default.
// An edited open plan is replaced only after a confirmation.
func (m *Model) openGitRebase(base, onto, preset, presetCommit string, agent []protocol.GitRebaseEntry) tea.Cmd {
	key, target := m.gitTarget()
	if !m.gitRebaseEnabled() {
		return m.showNoticeAs(noticeUnavailable, "Interactive rebase needs a newer server")
	}
	if key == "" || base == "" {
		return nil
	}
	d := &gitRebaseDraft{key: key, target: target, base: base, onto: onto, preset: preset, presetCommit: presetCommit, agent: agent, open: true}
	if agent != nil {
		d.fromAgent, m.gitRB.agentInfo = m.gitRB.agentInfo, nil
	}
	if old := m.gitRebaseDraftFor(key); old != nil {
		if old.base == base && old.onto == onto && preset == "" && agent == nil {
			old.open = true
			m.markDirty()
			if !old.loaded && !old.loading {
				old.err = ""
				return tea.Batch(m.setFocus("git:rb:back"), m.loadGitRebasePlan(old))
			}
			return m.gitRebaseFocusFirst(old)
		}
		if old.edited() || old.sentID != "" {
			m.gitRB.replace = d
			m.showMenuFor("Replace plan · ", gitHeadLabel(m.gitViews[key].statusOrNil()), []menuItem{
				{Note: "An edited rebase plan is open for this checkout"},
				{Note: "Opening another discards its arrangement and messages"},
				{Label: "Cancel", Action: action{Kind: "noop"}},
				{Label: "Discard it and open the new plan", Action: action{Kind: "git-rb-replace"}},
			})
			m.menuIndex = 2
			return nil
		}
	}
	return m.startGitRebaseDraft(d)
}

func (g *gitView) statusOrNil() *protocol.GitStatus {
	if g == nil {
		return nil
	}
	return g.status
}

func (m *Model) startGitRebaseDraft(d *gitRebaseDraft) tea.Cmd {
	if m.gitRB.drafts == nil {
		m.gitRB.drafts = map[string]*gitRebaseDraft{}
	}
	m.dropGitRebaseEdit(d.key, "reword", "chain")
	m.gitRB.drafts[d.key] = d
	m.viewState().DetailScroll = 0
	m.markDirty()
	return tea.Batch(m.setFocus("git:rb:back"), m.loadGitRebasePlan(d))
}

// OpenGitRebaseUpstream opens the editor rebasing the checked-out branch
// onto its upstream (base = the upstream ref; the plan reports
// BaseIsUpstream). The Pull divergence follow-up uses it.
func (m *Model) openGitRebaseUpstream() tea.Cmd {
	g := m.currentGitView()
	if g == nil || g.status == nil || g.status.Upstream == "" {
		return m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindRebase, "no_upstream"))
	}
	return m.openGitRebase("upstream", "", "", "", nil)
}

// openGitRebaseAgentPlan opens the editor with an agent-proposed
// arrangement for review (slice 6); nothing runs until the user starts it.
func (m *Model) openGitRebaseAgentPlan(base, onto string, entries []protocol.GitRebaseEntry) tea.Cmd {
	if len(entries) == 0 {
		return m.showNoticeAs(noticeUnavailable, "The proposed plan has no entries")
	}
	return m.openGitRebase(base, onto, "", "", slices.Clone(entries))
}

func (m *Model) loadGitRebasePlan(d *gitRebaseDraft) tea.Cmd {
	api := m.gitRebaseClient()
	if api == nil {
		return nil
	}
	m.gitRB.seq++
	d.seq, d.loading = m.gitRB.seq, true
	key, seq, target, base, onto := d.key, d.seq, d.target, d.base, d.onto
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		p, err := api.GitRebasePlan(deadline, target, base, onto)
		return gitRebasePlanMsg{key: key, seq: seq, plan: p, err: err}
	}
}

func (m *Model) acceptGitRebasePlan(msg gitRebasePlanMsg) tea.Cmd {
	d := m.gitRebaseDraftFor(msg.key)
	if d == nil || d.seq != msg.seq {
		return nil
	}
	d.loading = false
	m.markDirty()
	if msg.err != nil {
		var pe *protocol.Error
		if errors.As(msg.err, &pe) {
			d.err = gitRefCopy(protocol.GitKindRebase, pe.Code)
			if s := safe(singleLine(pe.Message)); s != "" && pe.Code != "" {
				d.err += " · " + s
			}
		} else {
			d.err = "Could not read the plan · " + safe(singleLine(msg.err.Error()))
		}
		return nil
	}
	d.err = ""
	if !d.loaded {
		d.plan, d.loaded = msg.plan, true
		d.entries = client.DefaultRebaseEntries(d.plan)
		d.chainMsg = map[string]string{}
		if a := d.fromAgent; a != nil && a.fingerprint != "" && a.fingerprint != d.plan.Fingerprint {
			// Never load a proposal for another branch state.
			d.agent, d.fromAgent = nil, nil
			d.err = "The branch changed since the agent planned · the proposal was not loaded; plan again"
		}
		if d.agent != nil {
			d.applyEntries(d.agent)
			d.agent = nil
			d.notice = "Proposed plan · review it; nothing runs until you start it"
			if a := d.fromAgent; a != nil {
				d.updateRefs = a.updateRefs && len(d.plan.UpdateRefs) > 0
				a.built = d.build()
			}
		}
		if d.preset != "" {
			return m.applyGitRebasePreset(d)
		}
		return m.gitRebaseFocusFirst(d)
	}
	old := d.plan.Fingerprint
	if a := d.fromAgent; a != nil && msg.plan.Fingerprint != a.fingerprint {
		a.outdated = true
	}
	added, removed := d.mergeRefresh(msg.plan)
	d.stale = false
	switch {
	case old == msg.plan.Fingerprint:
		d.notice = "Plan refreshed · unchanged"
	default:
		d.notice = "Plan refreshed · your arrangement is kept"
		if len(added) > 0 {
			d.notice += " · picked new " + strings.Join(added, ", ")
		}
		if len(removed) > 0 {
			d.notice += " · no longer in range " + strings.Join(removed, ", ")
		}
	}
	return nil
}

// gitRebaseFocusFirst focuses the newest entry row.
func (m *Model) gitRebaseFocusFirst(d *gitRebaseDraft) tea.Cmd {
	if len(d.entries) == 0 {
		return m.setFocus("git:rb:back")
	}
	return m.setFocus(gitRebaseRowKey(len(d.entries) - 1))
}

func gitRebaseRowKey(i int) string { return "git:rb:e:" + strconv.Itoa(i) }

// gitRebaseRowIndex parses an entry row key.
func gitRebaseRowIndex(key string) (int, bool) {
	rest, ok := strings.CutPrefix(key, "git:rb:e:")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	return i, err == nil
}

// applyGitRebasePreset applies a commit-menu preset to the fresh plan.
func (m *Model) applyGitRebasePreset(d *gitRebaseDraft) tea.Cmd {
	preset, oid := d.preset, d.presetCommit
	d.preset = ""
	if preset == "" {
		return nil
	}
	i := d.entryIndex(oid)
	if i < 0 {
		return tea.Batch(m.showNoticeAs(noticeUnavailable, "Commit "+gitShort(oid)+" is not in the plan · shown unchanged"), m.gitRebaseFocusFirst(d))
	}
	parent := func(oid string) string {
		if c, ok := d.commit(oid); ok && len(c.Parents) > 0 {
			return c.Parents[0]
		}
		return ""
	}
	switch preset {
	case gitRebasePresetSquash, gitRebasePresetFixup, gitRebasePresetMoveDown:
		if i == 0 || d.entries[i-1].Commit == "" || d.entries[i-1].Commit != parent(oid) {
			return tea.Batch(m.showNoticeAs(noticeUnavailable, "The entry below "+gitShort(oid)+" is not its parent · shown unchanged"), m.setFocus(gitRebaseRowKey(i)))
		}
	case gitRebasePresetMoveUp:
		if i+1 >= len(d.entries) || parent(d.entries[i+1].Commit) != oid {
			return tea.Batch(m.showNoticeAs(noticeUnavailable, "The entry above "+gitShort(oid)+" is not its child · shown unchanged"), m.setFocus(gitRebaseRowKey(i)))
		}
	}
	switch preset {
	case gitRebasePresetReword:
		prev := d.entries[i]
		d.setAction(i, protocol.GitRebaseReword, "")
		return m.openGitRebaseMessage(d, i, &prev)
	case gitRebasePresetEdit:
		d.setAction(i, protocol.GitRebaseEdit, "")
	case gitRebasePresetSquash:
		d.setAction(i, protocol.GitRebaseSquash, "")
		if i > 0 {
			return m.openGitRebaseMessage(d, i, nil)
		}
	case gitRebasePresetFixup:
		d.setAction(i, protocol.GitRebaseFixup, "")
	case gitRebasePresetDrop:
		d.setAction(i, protocol.GitRebaseDrop, "")
	case gitRebasePresetMoveUp:
		if d.move(i, i+1) {
			i++
		}
	case gitRebasePresetMoveDown:
		if d.move(i, i-1) {
			i--
		}
	}
	return m.setFocus(gitRebaseRowKey(i))
}

// gitRebaseFirstParents is the loaded first-parent history of HEAD, newest
// first, as far as the loaded log reaches.
func gitRebaseFirstParents(g *gitView, head string) []protocol.GitCommit {
	if g.log == nil || head == "" {
		return nil
	}
	byHash := map[string]protocol.GitCommit{}
	for _, c := range g.log.Commits {
		byHash[c.Hash] = c
	}
	var out []protocol.GitCommit
	for h := head; h != ""; {
		c, ok := byHash[h]
		if !ok || len(out) > len(g.log.Commits) {
			break
		}
		out = append(out, c)
		h = ""
		if len(c.Parents) > 0 {
			h = c.Parents[0]
		}
	}
	return out
}

// gitRebasePresetBase is the plan base for a commit-menu preset on hash,
// or why it is unavailable.
func (m *Model) gitRebasePresetBase(g *gitView, hash, preset string) (base, reason string) {
	if !m.gitRebaseEnabled() {
		return "", "needs a newer server"
	}
	s := g.status
	switch {
	case s == nil:
		return "", "status not loaded"
	case s.Operation != "":
		return "", "finish the " + safe(singleLine(s.Operation)) + " first"
	case s.Branch == "" || s.Workspace.State == "detached":
		return "", "detached HEAD · switch to a branch first"
	}
	chain := gitRebaseFirstParents(g, s.HeadOid)
	at := slices.IndexFunc(chain, func(c protocol.GitCommit) bool { return c.Hash == hash })
	if at < 0 {
		return "", "not on " + gitHeadLabel(s) + "'s first-parent history (as loaded)"
	}
	c := chain[at]
	if len(c.Parents) > 1 {
		return "", "merge commits are not rewritten here"
	}
	parentOf := func(c protocol.GitCommit) string {
		if len(c.Parents) == 0 {
			return "root"
		}
		return c.Parents[0]
	}
	switch preset {
	case gitRebasePresetMoveUp:
		if at == 0 {
			return "", "already the newest commit"
		}
	case gitRebasePresetSquash, gitRebasePresetFixup, gitRebasePresetMoveDown:
		if len(c.Parents) == 0 {
			return "", "the root commit has no parent"
		}
		if at+1 >= len(chain) {
			return "", "the parent is not loaded · use Interactive rebase from here"
		}
		pc := chain[at+1]
		if len(pc.Parents) > 1 {
			return "", "the parent is a merge commit"
		}
		return parentOf(pc), ""
	}
	return parentOf(c), ""
}

// gitRebaseMenuItems are the commit-row presets; unavailable ones are
// notes naming the reason.
func (m *Model) gitRebaseMenuItems(g *gitView, c protocol.GitCommit) []menuItem {
	if !slices.Contains(m.snapshot.Capabilities, "git-rebase-interactive") || !m.gitOperationsEnabled() {
		return []menuItem{{Note: "Interactive rebase needs a newer server"}}
	}
	short := gitShort(c.Short)
	presets := []struct{ preset, label string }{
		{gitRebasePresetReword, "Edit message of " + short + "…"},
		{gitRebasePresetEdit, "Edit contents of " + short + "…"},
		{gitRebasePresetSquash, "Squash " + short + " into parent…"},
		{gitRebasePresetFixup, "Fixup " + short + " into parent…"},
		{gitRebasePresetDrop, "Drop " + short + "…"},
		{gitRebasePresetMoveUp, "Move " + short + " up…"},
		{gitRebasePresetMoveDown, "Move " + short + " down…"},
		{gitRebasePresetFrom, "Interactive rebase from " + short + "…"},
	}
	var items []menuItem
	for _, p := range presets {
		base, reason := m.gitRebasePresetBase(g, c.Hash, p.preset)
		if reason != "" {
			items = append(items, menuItem{Note: strings.TrimSuffix(p.label, "…") + " · " + reason})
			continue
		}
		items = append(items, menuItem{Label: p.label, Action: action{Kind: "git-rb-open", ID: base, Value: p.preset + ":" + c.Hash}})
		if p.preset == gitRebasePresetFrom && m.gitPlanEnabled() {
			items = append(items, menuItem{Label: "Plan rebase from " + short + " with agent…", Action: action{Kind: "git-plan-open", ID: base}})
		}
	}
	return items
}

// ---- Actions ----

// gitRebaseAction handles every rebase editor and stop control.
func (m *Model) gitRebaseAction(a action) (tea.Cmd, bool) {
	if !strings.HasPrefix(a.Kind, "git-rb-") {
		return nil, false
	}
	key, target := m.gitTarget()
	d := m.gitRebaseDraftFor(key)
	switch a.Kind {
	case "git-rb-open":
		preset, commit, _ := strings.Cut(a.Value, ":")
		return m.openGitRebase(a.ID, "", preset, commit, nil), true
	case "git-rb-onto":
		// Rebase the branch onto a ref or commit, interactively.
		return m.openGitRebase(a.ID, "", "", "", nil), true
	case "git-rb-upstream":
		return m.openGitRebaseUpstream(), true
	case "git-rb-replace":
		r := m.gitRB.replace
		m.gitRB.replace = nil
		if r == nil || r.key != key {
			return nil, true
		}
		return m.startGitRebaseDraft(r), true
	case "git-rb-show":
		if d != nil {
			d.open = true
			m.viewState().DetailScroll = 0
			m.markDirty()
			return m.gitRebaseFocusFirst(d), true
		}
		return nil, true
	case "git-rb-back":
		if d != nil {
			d.open = false
			m.markDirty()
		}
		return m.setFocus("git-refresh"), true
	case "git-rb-discard":
		if d == nil {
			return nil, true
		}
		if !d.edited() {
			return m.discardGitRebase(key), true
		}
		m.gitRB.discard = key
		m.showMenuFor("Discard plan · ", gitHeadLabel(m.gitViews[key].statusOrNil()), []menuItem{
			{Note: "Discard this rebase plan?"},
			{Note: "Its arrangement and messages are lost · nothing in Git changes"},
			{Label: "Cancel", Action: action{Kind: "noop"}},
			{Label: "Discard plan", Action: action{Kind: "git-rb-discard-confirm"}},
		})
		m.menuIndex = 2
		return nil, true
	case "git-rb-discard-confirm":
		k := m.gitRB.discard
		m.gitRB.discard = ""
		if k != key {
			return nil, true
		}
		return m.discardGitRebase(key), true
	case "git-rb-refresh":
		if d == nil || d.loading {
			return nil, true
		}
		d.notice, d.err = "", ""
		return tea.Batch(m.loadGitRebasePlan(d), m.refreshGit()), true
	case "git-rb-row":
		return nil, true
	case "git-rb-grip":
		if d != nil && a.Index >= 0 && a.Index < len(d.entries) {
			m.gitRB.dragging, m.gitRB.drag = true, a.Index
		}
		return nil, true
	case "git-rb-menu":
		return m.openGitRebaseRowMenu(a.Index), true
	case "git-rb-set":
		if d == nil {
			return nil, true
		}
		var prev *protocol.GitRebaseEntry
		if a.Index >= 0 && a.Index < len(d.entries) && d.entries[a.Index].Action != protocol.GitRebaseReword {
			p := d.entries[a.Index]
			prev = &p
		}
		if d.setAction(a.Index, a.ID, a.Value) {
			return m.openGitRebaseMessage(d, a.Index, prev), true
		}
		m.markDirty()
		return m.setFocus(gitRebaseRowKey(a.Index)), true
	case "git-rb-move":
		if d == nil {
			return nil, true
		}
		to := a.Index + 1 // display up: later in the run
		if a.Value == "down" {
			to = a.Index - 1
		}
		if d.move(a.Index, to) {
			m.markDirty()
			return m.gitRebaseScrollTo(to), true
		}
		return nil, true
	case "git-rb-break":
		if d == nil || !d.loaded {
			return nil, true
		}
		at := d.insertBreak(a.Index)
		m.markDirty()
		return m.setFocus(gitRebaseRowKey(at)), true
	case "git-rb-unbreak":
		if d == nil || a.Index < 0 || a.Index >= len(d.entries) || d.entries[a.Index].Action != protocol.GitRebaseBreak {
			return nil, true
		}
		d.entries = slices.Delete(d.entries, a.Index, a.Index+1)
		d.changed()
		m.markDirty()
		return m.setFocus(gitRebaseRowKey(min(a.Index, len(d.entries)-1))), true
	case "git-rb-message":
		if d == nil {
			return nil, true
		}
		return m.openGitRebaseMessage(d, a.Index, nil), true
	case "git-rb-toggle":
		if d == nil {
			return nil, true
		}
		switch a.ID {
		case "update-refs":
			if len(d.plan.UpdateRefs) == 0 {
				return m.showNoticeAs(noticeUnavailable, "No other branch points into the rewritten commits"), true
			}
			d.updateRefs = !d.updateRefs
		case "merges":
			d.ackMerges = !d.ackMerges
		case "published":
			d.ackPublished = !d.ackPublished
		}
		d.changed()
		m.markDirty()
		return nil, true
	case "git-rb-start":
		return m.openGitRebaseConfirm(key), true
	case "git-rb-confirm":
		return m.confirmGitRebase(key), true
	case "git-rb-msg":
		return m.setFocus(gitRebaseMsgKey), true
	case "git-rb-msg-cancel":
		return m.closeGitRebaseMessage(key, false), true
	case "git-rb-msg-save":
		return m.closeGitRebaseMessage(key, true), true
	case "git-rb-msg-reset":
		if e := m.gitRB.edits[key]; e != nil && d != nil {
			e.text = m.gitRebaseDefaultMessage(d, e)
			m.gitRebaseMsg().SetValue(e.text)
			m.markDirty()
		}
		return nil, true
	case "git-rb-stop-continue", "git-rb-stop-commit":
		g := m.gitViews[key]
		if g == nil || g.oper == nil || g.oper.Interactive == nil {
			return tea.Batch(m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindOperationContinue, "no_operation")), m.refreshGit()), true
		}
		purpose := "continue"
		if a.Kind == "git-rb-stop-commit" {
			purpose = "commit"
			if reason := gitRebaseCommitBlock(g.oper); reason != "" {
				return m.showNoticeAs(noticeUnavailable, reason), true
			}
		} else if reason := m.gitOpBlock(key, g, protocol.GitKindOperationContinue); reason != "" {
			return m.showNoticeAs(noticeUnavailable, reason), true
		}
		return m.openGitRebaseStopMessage(key, purpose, *g.oper), true
	case "git-rb-commit-confirm":
		dlg := m.gitRB.commit
		m.gitRB.commit = nil
		g := m.gitViews[key]
		if dlg == nil || dlg.key != key || g == nil {
			return nil, true
		}
		if g.oper == nil || !gitSameStop(*g.oper, dlg.state) {
			return tea.Batch(m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindOperationCommit, "stale_operation")), m.refreshGit()), true
		}
		if reason := gitRebaseCommitBlock(g.oper); reason != "" {
			return m.showNoticeAs(noticeUnavailable, reason), true
		}
		m.clearGitWarning(key)
		cmd := client.GitOperationCommitCommand(identity(), target, dlg.state, dlg.message)
		return m.sendGitWrite(key, cmd, "staged changes"), true
	}
	return nil, true
}

// discardGitRebase drops the target's draft and its message editor.
func (m *Model) discardGitRebase(key string) tea.Cmd {
	delete(m.gitRB.drafts, key)
	m.dropGitRebaseEdit(key, "reword", "chain")
	m.markDirty()
	return tea.Batch(m.setFocus("git-refresh"), m.showNoticeAs(noticeDone, "Rebase plan discarded"))
}

func (m *Model) dropGitRebaseEdit(key string, purposes ...string) {
	if e := m.gitRB.edits[key]; e != nil && slices.Contains(purposes, e.purpose) {
		delete(m.gitRB.edits, key)
		if m.gitRB.msgFor == e {
			m.gitRB.msgFor = nil
		}
	}
}

// gitRebaseScrollTo focuses entry i's row, scrolling it into view.
func (m *Model) gitRebaseScrollTo(i int) tea.Cmd {
	cmd := m.setFocus(gitRebaseRowKey(i))
	m.moveGitFocus(0)
	return cmd
}

// openGitRebaseRowMenu lists the actions of entry i.
func (m *Model) openGitRebaseRowMenu(i int) tea.Cmd {
	key, _ := m.gitTarget()
	d := m.gitRebaseDraftFor(key)
	if d == nil || i < 0 || i >= len(d.entries) || len(m.menu) != 0 {
		return nil
	}
	e := d.entries[i]
	set := func(label, act, variant string) menuItem {
		return menuItem{Label: label, Action: action{Kind: "git-rb-set", ID: act, Value: variant, Index: i}}
	}
	var items []menuItem
	user := "break"
	if e.Action == protocol.GitRebaseBreak {
		items = append(items, menuItem{Label: "Remove break (d)", Action: action{Kind: "git-rb-unbreak", Index: i}})
	} else {
		user = gitShort(e.Commit)
		items = append(items,
			set("Pick (p)", protocol.GitRebasePick, ""),
			set("Reword… (r)", protocol.GitRebaseReword, ""),
			set("Edit contents · stop with it staged (e)", protocol.GitRebaseEdit, protocol.GitRebaseEditReset),
			set("Edit · stop to amend (e twice)", protocol.GitRebaseEdit, protocol.GitRebaseEditAmend),
			set("Squash into the row below (s)", protocol.GitRebaseSquash, ""),
			set("Fixup into the row below · keep its message (f)", protocol.GitRebaseFixup, ""),
			set("Fixup · use this message (fixup -C)", protocol.GitRebaseFixup, protocol.GitRebaseFixupUseMessage),
			set("Fixup · edit the message (fixup -c)", protocol.GitRebaseFixup, protocol.GitRebaseFixupEditMessage),
			set("Drop (d)", protocol.GitRebaseDrop, ""))
		if d.messageFor(i) != "" {
			items = append(items, menuItem{Label: "Edit message… (m)", Action: action{Kind: "git-rb-message", Index: i}})
		}
		items = append(items, menuItem{Label: "Insert break after this commit (b)", Action: action{Kind: "git-rb-break", Index: i}})
	}
	if i+1 < len(d.entries) {
		items = append(items, menuItem{Label: "Move up (Shift+↑ / K)", Action: action{Kind: "git-rb-move", Index: i, Value: "up"}})
	}
	if i > 0 {
		items = append(items, menuItem{Label: "Move down (Shift+↓ / J)", Action: action{Kind: "git-rb-move", Index: i, Value: "down"}})
	}
	m.showMenuFor("Plan entry · ", user, items)
	m.contextMenu = &contextMenuState{returnFocus: gitRebaseRowKey(i)}
	return m.setFocus(gitRebaseRowKey(i))
}

// messageFor names the message entry i offers to edit: "reword" or
// "chain" ("" for none).
func (d *gitRebaseDraft) messageFor(i int) string {
	if i < 0 || i >= len(d.entries) {
		return ""
	}
	if d.entries[i].Action == protocol.GitRebaseReword {
		return "reword"
	}
	if gitRebaseChainMember(d.entries[i]) || i+1 < len(d.entries) && gitRebaseChainMember(d.entries[i+1]) {
		start := gitRebaseChainStart(d.entries, i)
		if end := gitRebaseChainEnd(d.entries, start); end > start && gitRebaseChainNeedsMessage(d.entries, start, end) {
			return "chain"
		}
	}
	return ""
}

// openGitRebaseMessage opens the message editor for entry i: its reword
// message, or its chain's combined message. revert is the entry before a
// reword was chosen just now.
func (m *Model) openGitRebaseMessage(d *gitRebaseDraft, i int, revert *protocol.GitRebaseEntry) tea.Cmd {
	m.gitRebaseRelease()
	kind := d.messageFor(i)
	if kind == "" {
		return m.showNoticeAs(noticeUnavailable, "This entry keeps its message · choose reword, squash or fixup -c to change it")
	}
	e := &gitRebaseMsgEdit{key: d.key, purpose: kind, commit: d.entries[i].Commit, from: d.entries[i].Commit, revert: revert}
	raw := ""
	if kind == "chain" {
		start := gitRebaseChainStart(d.entries, i)
		end := gitRebaseChainEnd(d.entries, start)
		e.commit, e.chain = d.entries[start].Commit, gitRebaseChainKey(d.entries, start, end)
		raw, e.exact = d.chainDefault(start, end)
		e.orig = d.chainDisplay(start, end)
		e.text = e.orig
		if msg, ok := d.chainMsg[e.chain]; ok {
			e.text, e.stored = msg, true
		}
	} else {
		c, _ := d.commit(e.commit)
		raw, e.exact = gitRebaseRaw(c)
		e.orig = gitRebaseDisplay(c)
		e.text = e.orig
		if msg := d.entries[i].Message; msg != "" {
			e.text, e.stored = msg, true
		}
	}
	a := m.gitRebaseMsg()
	storedWarn := ""
	if e.stored {
		stored := e.text
		a.SetValue(safeKeepTabs(stored))
		e.text = a.Value()
		if e.text != stored {
			storedWarn = "This message has characters the editor cannot keep (tabs, carriage returns or control characters) · unchanged it is kept exactly; an edit is sent as shown"
		}
	}
	a.SetValue(safeKeepTabs(e.orig))
	switch {
	case !e.exact:
		// Never prefill a cut or missing original: anything sent is text
		// the user wrote.
		e.warn = "The original message is not available here (longer than 64 KiB, or an older server) and cannot be edited · write the full message"
		if e.text == e.orig {
			e.text = ""
		}
		e.orig = ""
		if storedWarn != "" {
			e.warn = storedWarn
		}
		return m.showGitRebaseEdit(e)
	case storedWarn != "":
		e.warn = storedWarn
	case a.Value() != raw && !e.stored:
		e.warn = "The original has characters the editor cannot keep (tabs, carriage returns or control characters) · unchanged it is kept exactly; an edit is sent as shown"
	}
	e.orig = a.Value()
	if !e.stored {
		e.text = e.orig
	}
	return m.showGitRebaseEdit(e)
}

func (m *Model) showGitRebaseEdit(e *gitRebaseMsgEdit) tea.Cmd {
	if m.gitRB.edits == nil {
		m.gitRB.edits = map[string]*gitRebaseMsgEdit{}
	}
	m.gitRebaseRelease()
	m.gitRB.edits[e.key] = e
	m.gitRB.msgFor = e
	a := m.gitRebaseMsg()
	a.SetValue(e.text)
	e.text = a.Value()
	e.initial = e.text
	m.viewState().DetailScroll = 0
	m.markDirty()
	return m.setFocus(gitRebaseMsgKey)
}

// gitRebaseDefaultMessage is what Reset restores in the editor.
func (m *Model) gitRebaseDefaultMessage(_ *gitRebaseDraft, e *gitRebaseMsgEdit) string {
	return e.orig
}

// closeGitRebaseMessage closes the target's message editor, applying its
// text when save is set.
func (m *Model) closeGitRebaseMessage(key string, save bool) tea.Cmd {
	e := m.gitRB.edits[key]
	if e == nil {
		return nil
	}
	if m.gitRB.msgFor == e {
		e.text = m.gitRebaseMsg().Value()
	}
	d := m.gitRebaseDraftFor(key)
	focus := "git-refresh"
	switch e.purpose {
	case "reword", "chain":
		if d == nil {
			break
		}
		focus = "git:rb:back"
		i := d.entryIndex(e.commit)
		if i < 0 {
			if save {
				m.showNoticeAs(noticeUnavailable, "That entry left the plan · message not applied")
			}
			break
		}
		focus = gitRebaseRowKey(i)
		if j := d.entryIndex(e.from); j >= 0 {
			focus = gitRebaseRowKey(j)
		}
		unchanged := e.text == e.initial
		original := unchanged && !e.stored || !unchanged && e.orig != "" && e.text == e.orig
		switch {
		case !save:
			if e.revert != nil {
				d.entries[i] = *e.revert
				d.changed()
			}
		case unchanged && e.stored:
			// Kept byte for byte, whatever the editor could show.
		case !e.exact && strings.TrimSpace(e.text) == "":
			return m.showNoticeAs(noticeUnavailable, "Write the full message · the original is not available here")
		case !protocol.ValidRebaseMessage(e.text):
			return m.showNoticeAs(noticeUnavailable, "The message must not be blank (up to 64 KiB of text)")
		case e.purpose == "reword" && original:
			// An unchanged reword is a pick: nothing is rewritten.
			prev := protocol.GitRebaseEntry{Action: protocol.GitRebasePick, Commit: e.commit}
			if e.revert != nil {
				prev = *e.revert
			}
			if d.entries[i] != prev {
				d.entries[i] = prev
				d.changed()
			}
			m.showNoticeAs(noticeDone, "Message unchanged · kept as "+prev.Action)
		case e.purpose == "reword":
			if d.entries[i].Message != e.text {
				d.entries[i].Message = e.text
				d.changed()
			}
			if d.rewordReplaced(i) {
				m.showNoticeAs(noticeUnavailable, "The squash's written combined message replaces this reword · edit it with m")
			}
		case original:
			if _, ok := d.chainMsg[e.chain]; ok {
				delete(d.chainMsg, e.chain)
				d.changed()
			}
		default:
			if d.chainMsg[e.chain] != e.text {
				d.chainMsg[e.chain] = e.text
				d.changed()
			}
		}
	case "continue", "commit":
		g := m.gitViews[key]
		if save {
			text, ok := gitRebaseStopText(e)
			if !ok {
				return m.showNoticeAs(noticeUnavailable, "Write the message · the step's message is not fully available here")
			}
			if e.purpose == "commit" && !protocol.ValidRebaseMessage(text) {
				return m.showNoticeAs(noticeUnavailable, "Write a commit message first")
			}
			if text != "" && !protocol.ValidRebaseMessage(text) {
				return m.showNoticeAs(noticeUnavailable, "The message must be text up to 64 KiB, not only whitespace")
			}
			if g == nil || g.oper == nil || !gitSameStop(*g.oper, e.state) {
				delete(m.gitRB.edits, key)
				m.gitRB.msgFor = nil
				return tea.Batch(m.setFocus("git-refresh"), m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindOperationContinue, "stale_operation")), m.refreshGit())
			}
			delete(m.gitRB.edits, key)
			m.gitRB.msgFor = nil
			m.setFocus("git-refresh")
			_, target := m.gitTarget()
			if e.purpose == "continue" {
				m.openGitOpDialog(&gitOpDialog{key: key, target: target, kind: protocol.GitKindOperationContinue, state: e.state, message: text})
				return nil
			}
			m.openGitRebaseCommitConfirm(key, target, e.state, text)
			return nil
		}
	}
	delete(m.gitRB.edits, key)
	if m.gitRB.msgFor == e {
		m.gitRB.msgFor = nil
	}
	m.markDirty()
	return m.setFocus(focus)
}

// openGitRebaseStopMessage opens the message editor for a stop's Continue
// or Commit, prefilled with nothing: an empty Continue keeps the stored or
// original message.
func (m *Model) openGitRebaseStopMessage(key, purpose string, st protocol.GitOperationState) tea.Cmd {
	m.gitRebaseRelease()
	e := &gitRebaseMsgEdit{key: key, purpose: purpose, state: st}
	if in := st.Interactive; in != nil && in.StepMessage != "" {
		raw := in.StepMessage
		e.exact = !in.StepMessageTruncated
		if !e.exact {
			e.warn = "The step's message is longer than 64 KiB and cannot be edited here · write the full message (Continue with an empty message keeps the original)"
			return m.showGitRebaseEdit(e)
		}
		a := m.gitRebaseMsg()
		a.SetValue(safeKeepTabs(raw))
		e.orig, e.text = a.Value(), a.Value()
		switch {
		case !e.exact:
			e.warn = "The step's message is over 64 KiB and shown cut · write the message"
		case e.orig != raw:
			e.warn = "The message has characters the editor cannot keep (tabs, carriage returns or control characters) · unchanged it is kept exactly; an edit is sent as shown"
		}
	}
	return m.showGitRebaseEdit(e)
}

// gitRebaseStopText is the message a stop's Continue or Commit sends: ""
// (the step's own message) or the raw step message when unchanged, the
// editor's text otherwise; ok is false when an unchanged message is not
// exact and must be written.
func gitRebaseStopText(e *gitRebaseMsgEdit) (string, bool) {
	if e.orig == "" || e.text != e.orig {
		return e.text, true
	}
	if !e.exact {
		return "", false
	}
	if e.purpose == "continue" {
		return "", true // Continue commits the step's own message
	}
	return e.state.Interactive.StepMessage, true
}

// gitRebaseCommitBlock is why git.operation_commit is unavailable at st.
func gitRebaseCommitBlock(st *protocol.GitOperationState) string {
	in := st.Interactive
	switch {
	case in == nil || !in.Plan:
		return "Only for a rebase started here"
	case in.Stop != protocol.GitRebaseStopEditAmend && in.Stop != protocol.GitRebaseStopEditReset && in.Stop != protocol.GitRebaseStopBreak:
		return "Commit only while stopped to edit or at a break"
	case !in.Staged:
		return "Stage changes first"
	case len(st.HiddenEntries) > 0:
		return "Hidden index entries · use a terminal"
	}
	return ""
}

// openGitRebaseCommitConfirm confirms committing the staged changes at a
// stop, Cancel first.
func (m *Model) openGitRebaseCommitConfirm(key string, target client.GitTarget, st protocol.GitOperationState, message string) {
	m.gitRB.commit = &gitRebaseStopCommit{key: key, target: target, state: st, message: message}
	branch := safe(singleLine(st.Branch))
	if branch == "" {
		branch = "the rebased branch"
	}
	subject, _, _ := strings.Cut(safe(message), "\n")
	items := []menuItem{
		{Note: "Commit the staged changes on " + branch + " as a new commit?"},
		{Note: truncateCells(subject, 60)},
		{Note: "The rebase stays stopped · Continue when done"},
		{Label: "Cancel", Action: action{Kind: "noop"}},
		{Label: "Commit staged changes", Action: action{Kind: "git-rb-commit-confirm"}},
	}
	m.showMenuFor("Commit at stop · ", branch, items)
	m.menuIndex = len(items) - 2
}

// openGitRebaseConfirm summarises the rewrite before it starts, Cancel
// focused.
func (m *Model) openGitRebaseConfirm(key string) tea.Cmd {
	d := m.gitRebaseDraftFor(key)
	g := m.gitViews[key]
	if d == nil || g == nil || g.status == nil {
		return nil
	}
	if reason := m.gitRebaseProblem(d); reason != "" {
		return m.showNoticeAs(noticeUnavailable, reason)
	}
	if reason := m.gitRefBlock(key, g, protocol.GitKindRebase); reason != "" {
		return m.showNoticeAs(noticeUnavailable, reason)
	}
	if g.status.HeadOid != d.plan.HeadOid || g.status.Branch != d.plan.Branch {
		d.stale = true
		m.markDirty()
		return m.showNoticeAs(noticeUnavailable, "HEAD moved since the plan was read · Refresh plan (keeps your edits)")
	}
	p := d.plan
	branch := safe(singleLine(p.Branch))
	items := []menuItem{
		{Note: "Rewrite " + branch + " at " + gitShort(p.HeadOid) + "?"},
		{Note: "Base " + gitRebaseBaseLabel(p) + " · onto " + gitRebaseOntoLabel(p)},
	}
	if a := d.fromAgent; a != nil && a.outdated {
		items = append(items, menuItem{Note: "Plan proposed by the agent (revision " + strconv.FormatInt(a.revision, 10) + ") for an earlier state of the branch · review the changes"})
	} else if a != nil {
		how := "unchanged"
		if !slices.Equal(a.built, d.build()) || d.updateRefs != (a.updateRefs && len(p.UpdateRefs) > 0) {
			how = "edited by you"
		}
		items = append(items, menuItem{Note: "Plan proposed by the agent (revision " + strconv.FormatInt(a.revision, 10) + ") · " + how})
	}
	reordered, reworded, melded, dropped, stops, breaks := d.counts()
	var parts []string
	for _, c := range []struct {
		n    int
		word string
	}{{reordered, "moved"}, {reworded, "reworded"}, {melded, "squashed or fixed up"}, {dropped, "dropped"}} {
		if c.n > 0 {
			parts = append(parts, strconv.Itoa(c.n)+" "+c.word)
		}
	}
	items = append(items, menuItem{Note: plural(len(client.DefaultRebaseEntries(p)), "commit") + " replayed"})
	for _, part := range parts {
		items = append(items, menuItem{Note: "  " + part})
	}
	if stops+breaks > 0 {
		items = append(items, menuItem{Note: "Stops " + plural(stops+breaks, "time") + " to edit or at breaks"})
	}
	written, byDefault, replaced, firstDefault := d.messageCounts()
	if written+byDefault > 0 {
		items = append(items, menuItem{Note: "Messages: " + strconv.Itoa(written) + " written, " + strconv.Itoa(byDefault) + " combined by default · m to review"})
	}
	if replaced > 0 {
		items = append(items, menuItem{Note: plural(replaced, "reword") + " replaced by a written combined message"})
	}
	if p.Published {
		items = append(items, menuItem{Note: "Rewrites commits already on a remote"},
			menuItem{Note: "Publishing the result needs a force push (never done here)"})
	}
	if p.MergeCount > 0 {
		items = append(items, menuItem{Note: plural(p.MergeCount, "merge commit") + " will be dropped"}, menuItem{Note: "Their side commits are linearised"})
	}
	if len(p.UpdateRefs) > 0 {
		names := gitRebaseRefNames(p.UpdateRefs)
		if d.updateRefs {
			items = append(items, menuItem{Note: "Also moves " + names})
		} else {
			items = append(items, menuItem{Note: "Leaves " + names + " at the old commits (update refs off)"})
		}
	}
	if n := len(p.UpdateRefsUnsupported); n > 0 {
		items = append(items, menuItem{Note: "Left in place (cannot be moved here): " + plural(n, "branch")})
	}
	items = append(items, menuItem{Note: "Nothing is stashed · a clean tracked tree is required"},
		menuItem{Label: "Cancel", Action: action{Kind: "noop"}})
	if firstDefault >= 0 {
		items = append(items, menuItem{Label: "Review combined messages…", Action: action{Kind: "git-rb-message", Index: firstDefault}})
	}
	items = append(items,
		menuItem{Label: "Rewrite " + branch, Action: action{Kind: "git-rb-confirm"}})
	m.gitRB.confirm = &gitRebaseConfirm{key: key, rev: d.rev}
	m.showMenuFor("Interactive rebase · ", branch, items)
	m.menuIndex = slices.IndexFunc(items, func(it menuItem) bool { return it.Label == "Cancel" })
	return nil
}

// confirmGitRebase starts the confirmed plan, re-verified against what the
// confirmation showed.
func (m *Model) confirmGitRebase(key string) tea.Cmd {
	c := m.gitRB.confirm
	m.gitRB.confirm = nil
	d := m.gitRebaseDraftFor(key)
	g := m.gitViews[key]
	if c == nil || c.key != key || d == nil || g == nil || g.status == nil {
		return nil
	}
	if c.rev != d.rev {
		return m.showNoticeAs(noticeUnavailable, "The plan changed since shown · review it again")
	}
	if reason := m.gitRebaseProblem(d); reason != "" {
		return m.showNoticeAs(noticeUnavailable, reason)
	}
	if reason := m.gitRefBlock(key, g, protocol.GitKindRebase); reason != "" {
		return m.showNoticeAs(noticeUnavailable, reason)
	}
	if g.status.HeadOid != d.plan.HeadOid || g.status.Branch != d.plan.Branch {
		d.stale = true
		return tea.Batch(m.showNoticeAs(noticeUnavailable, "HEAD moved since the plan was read · Refresh plan (keeps your edits)"), m.refreshGit())
	}
	p := d.plan
	opts := client.RebaseOptions{UpdateRefs: d.updateRefs && len(p.UpdateRefs) > 0, AcknowledgeMerges: d.ackMerges && p.MergeCount > 0,
		AcknowledgePublished: d.ackPublished && p.Published}
	cmd := client.GitRebaseInteractiveCommand(identity(), d.target, p, d.build(), opts)
	m.clearGitWarning(key)
	send := m.sendGitWrite(key, cmd, gitRebaseOntoLabel(p))
	if st := m.gitW.writes[key]; st == nil || st.cmd.ID != cmd.ID {
		return send // refused locally: another write is pending
	}
	d.sentID, d.err, d.open = cmd.ID, "", false
	m.markDirty()
	return tea.Batch(m.setFocus("git-refresh"), send)
}

// gitRebaseReply records the server's answer to a start on its draft: an
// accepted start ends the draft (the operation panel takes over); a
// refusal reopens the editor with the reason and keeps every edit.
func (m *Model) gitRebaseReply(msg gitWriteMsg) {
	d := m.gitRebaseDraftFor(msg.key)
	if d == nil || d.sentID == "" || d.sentID != msg.cmd.ID {
		return
	}
	var pe *protocol.Error
	switch {
	case errors.As(msg.err, &pe):
		d.sentID, d.open = "", true
		d.err = gitRefCopy(protocol.GitKindRebase, pe.Code)
		if s := safe(singleLine(pe.Message)); s != "" {
			d.err += " · " + s
		}
		d.stale = d.stale || pe.Code == "stale_plan" || pe.Code == "stale_head" || pe.Code == "stale_status"
	case msg.err != nil:
		// No reply: Retry resends the same command; the draft waits.
	case msg.receipt.Git == nil || msg.receipt.Git.State != protocol.GitStateSucceeded && msg.receipt.Git.State != protocol.GitStateFailed:
		d.sentID = ""
		d.err = "The start's result is unknown · refresh; discard this plan if the rebase started"
	case msg.receipt.Git.State == protocol.GitStateSucceeded:
		delete(m.gitRB.drafts, msg.key)
		m.dropGitRebaseEdit(msg.key, "reword", "chain")
	default:
		r := msg.receipt.Git
		d.sentID, d.open = "", true
		d.err = gitRefCopy(protocol.GitKindRebase, r.Code)
		if s := safe(singleLine(r.Message)); s != "" {
			d.err += " · " + s
		}
		d.stale = d.stale || r.Code == "stale_plan"
	}
	m.markDirty()
}

// gitRebaseSettle runs after every update: a draft whose start is no
// longer tracked (dismissed) returns to the editor, the stale marker
// follows the shown status, and confirmation state is dropped once its
// menu is gone.
func (m *Model) gitRebaseSettle(key string) {
	m.gitRebasePrune()
	if len(m.menu) == 0 {
		m.gitRB.confirm, m.gitRB.commit, m.gitRB.replace, m.gitRB.discard = nil, nil, nil, ""
	}
	d := m.gitRebaseDraftFor(key)
	if d == nil {
		return
	}
	if d.sentID != "" {
		if st := m.gitW.writes[key]; st == nil || st.cmd.ID != d.sentID {
			d.sentID = ""
		}
	}
	m.gitRebaseSyncMsg(key)
}

// gitRebasePrune drops drafts and message editors of threads or projects
// that no longer exist.
func (m *Model) gitRebasePrune() {
	if len(m.gitRB.drafts) == 0 && len(m.gitRB.edits) == 0 {
		return
	}
	gone := func(t client.GitTarget) bool {
		if t.ThreadID != "" {
			return !slices.ContainsFunc(m.snapshot.Threads, func(th protocol.Thread) bool { return th.ID == t.ThreadID })
		}
		return t.ProjectID != "" && !slices.ContainsFunc(m.snapshot.Projects, func(p protocol.Project) bool { return p.ID == t.ProjectID })
	}
	for k, d := range m.gitRB.drafts {
		if gone(d.target) {
			delete(m.gitRB.drafts, k)
			m.dropGitRebaseEdit(k, "reword", "chain")
		}
	}
}

// gitRebaseStash stores the shared textarea's text in the edit it holds.
func (m *Model) gitRebaseStash() {
	if e := m.gitRB.msgFor; e != nil && m.gitRB.ready {
		e.text = m.gitRebaseMsg().Value()
	}
}

// gitRebaseRelease stores the shared textarea's text in its owner and
// detaches it, so the textarea may then be written freely (for measuring
// or for a new edit) without touching any edit's text.
func (m *Model) gitRebaseRelease() {
	m.gitRebaseStash()
	m.gitRB.msgFor = nil
}

// gitRebaseSyncMsg loads key's open edit into the shared textarea, so a
// key or paste never edits another target's text.
func (m *Model) gitRebaseSyncMsg(key string) {
	e := m.gitRB.edits[key]
	if e == nil || m.gitRB.msgFor == e {
		return
	}
	m.gitRebaseStash()
	m.gitRB.msgFor = e
	m.gitRebaseMsg().SetValue(e.text)
}

// gitRebaseBaseLabel names a plan's base.
func gitRebaseBaseLabel(p protocol.GitRebasePlan) string {
	switch {
	case p.Root:
		return "the root"
	case p.BaseLabel != "":
		l := safe(singleLine(p.BaseLabel))
		if p.BaseIsUpstream {
			l += " (upstream)"
		}
		return l
	}
	return gitShort(p.BaseOid)
}

// gitRebaseOntoLabel names where the commits are replayed.
func gitRebaseOntoLabel(p protocol.GitRebasePlan) string {
	switch {
	case p.OntoLabel != "":
		return safe(singleLine(p.OntoLabel))
	case p.OntoOid != "":
		return gitShort(p.OntoOid)
	case p.Root:
		return "a new root"
	}
	return gitRebaseBaseLabel(p)
}

func gitRebaseRefNames(refs []protocol.GitRebaseUpdateRef) string {
	var names []string
	for _, r := range refs {
		names = append(names, safe(singleLine(gitRefLabel(r.Ref))))
	}
	if len(names) > 6 {
		names = append(names[:6], "+"+strconv.Itoa(len(refs)-6))
	}
	return strings.Join(names, ", ")
}

// ---- Keys and pointer ----

// gitRebaseKey handles the editor's row keys; ok reports that s was one.
func (m *Model) gitRebaseKey(s string) (tea.Cmd, bool) {
	if !strings.HasPrefix(m.focus, "git:rb") || len(m.menu) != 0 {
		return nil, false
	}
	key, _ := m.gitTarget()
	d := m.gitRebaseDraftFor(key)
	if d == nil || !d.open {
		return nil, false
	}
	if s == "esc" {
		return m.activate(action{Kind: "git-rb-back"}), true
	}
	i, row := gitRebaseRowIndex(m.focus)
	if !row || i >= len(d.entries) {
		return nil, false
	}
	isBreak := d.entries[i].Action == protocol.GitRebaseBreak
	set := func(act, variant string) (tea.Cmd, bool) {
		if isBreak {
			return nil, true
		}
		return m.activate(action{Kind: "git-rb-set", ID: act, Value: variant, Index: i}), true
	}
	switch s {
	case "enter", " ", "space":
		return m.openGitRebaseRowMenu(i), true
	case "p":
		return set(protocol.GitRebasePick, "")
	case "r":
		return set(protocol.GitRebaseReword, "")
	case "e":
		// Edit contents, then the amend stop, then back.
		e := d.entries[i]
		if e.Action == protocol.GitRebaseEdit && e.EditMode == "" {
			return set(protocol.GitRebaseEdit, protocol.GitRebaseEditAmend)
		}
		if e.Action == protocol.GitRebaseEdit {
			return set(protocol.GitRebasePick, "")
		}
		return set(protocol.GitRebaseEdit, protocol.GitRebaseEditReset)
	case "s":
		return set(protocol.GitRebaseSquash, "")
	case "f":
		// fixup, fixup -C, fixup -c, fixup.
		e := d.entries[i]
		next := ""
		if e.Action == protocol.GitRebaseFixup {
			next = map[string]string{"": protocol.GitRebaseFixupUseMessage, protocol.GitRebaseFixupUseMessage: protocol.GitRebaseFixupEditMessage}[e.Fixup]
		}
		return set(protocol.GitRebaseFixup, next)
	case "d":
		if isBreak {
			return m.activate(action{Kind: "git-rb-unbreak", Index: i}), true
		}
		return set(protocol.GitRebaseDrop, "")
	case "b":
		return m.activate(action{Kind: "git-rb-break", Index: i}), true
	case "m":
		return m.activate(action{Kind: "git-rb-message", Index: i}), true
	case "shift+up", "K", "shift+k":
		return m.activate(action{Kind: "git-rb-move", Index: i, Value: "up"}), true
	case "shift+down", "J", "shift+j":
		return m.activate(action{Kind: "git-rb-move", Index: i, Value: "down"}), true
	}
	return nil, false
}

// gitRebaseMsgKeyPress edits the message: Enter saves, Shift+Enter or
// Ctrl+J inserts a newline, Esc cancels.
func (m *Model) gitRebaseMsgKeyPress(k tea.KeyPressMsg) tea.Cmd {
	key, _ := m.gitTarget()
	if m.gitRB.edits[key] == nil {
		return m.setFocus("git-refresh")
	}
	m.gitRebaseSyncMsg(key)
	s := k.String()
	switch s {
	case "enter":
		return m.activate(action{Kind: "git-rb-msg-save"})
	case "esc":
		return m.activate(action{Kind: "git-rb-msg-cancel"})
	case "tab", "shift+tab":
		return nil
	case "shift+enter", "ctrl+j":
		k = tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	c := updateInput(m.gitRebaseMsg(), k)
	if e := m.gitRB.edits[key]; e != nil {
		e.text = m.gitRebaseMsg().Value()
	}
	m.markDirty()
	return c
}

// gitRebaseMsgPaste inserts pasted text (sanitized, newlines kept).
func (m *Model) gitRebaseMsgPaste(content string) tea.Cmd {
	key, _ := m.gitTarget()
	if m.gitRB.edits[key] == nil {
		return nil
	}
	m.gitRebaseSyncMsg(key)
	c := updateInput(m.gitRebaseMsg(), tea.PasteMsg{Content: safe(content)})
	if e := m.gitRB.edits[key]; e != nil {
		e.text = m.gitRebaseMsg().Value()
	}
	m.markDirty()
	return c
}

// gitRebaseMouse moves the dragged entry to the row under the pointer and
// ends the drag on release; ok reports a message it consumed.
func (m *Model) gitRebaseMouse(msg tea.MouseMsg, f frame) (tea.Cmd, bool) {
	if !m.gitRB.dragging {
		return nil, false
	}
	switch msg.(type) {
	case tea.MouseReleaseMsg:
		m.gitRB.dragging = false
		m.markDirty()
		return nil, true
	case tea.MouseMotionMsg:
		key, _ := m.gitTarget()
		d := m.gitRebaseDraftFor(key)
		p := msg.Mouse()
		if d == nil || !d.open || p.Button != tea.MouseLeft {
			// Motion without the left button held: the release was lost.
			m.gitRB.dragging = false
			m.markDirty()
			return nil, false
		}
		// Near the body's edges the body scrolls, so a drag reaches rows
		// out of view.
		if v := m.viewState(); f.detail.H > 2 {
			switch {
			case p.Y <= f.detail.Y:
				v.DetailScroll = max(0, min(v.DetailScroll, f.detailMax)-1)
			case p.Y >= f.detail.Y+f.detail.H-1:
				v.DetailScroll = min(f.detailMax, v.DetailScroll+1)
			}
		}
		for i := len(f.hits) - 1; i >= 0; i-- {
			h := f.hits[i]
			if !h.Rect.Contains(p.X, p.Y) {
				continue
			}
			if j, ok := gitRebaseRowIndex(h.Key); ok && j != m.gitRB.drag {
				if d.move(m.gitRB.drag, j) {
					m.gitRB.drag = j
					m.hover = gitRebaseRowKey(j)
					m.markDirty()
					return m.setFocus(gitRebaseRowKey(j)), true
				}
			}
			break
		}
		return nil, true
	}
	return nil, false
}
