package tui

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

type checkoutMsg struct {
	key  string
	info protocol.WorkspaceInfo
	err  error
}

func (m *Model) checkoutTarget() (key, projectID, threadID string) {
	if !m.hasComposer() {
		return "", "", ""
	}
	if m.creatingThread() {
		return "project:" + m.state.DraftProjectID + ":" + m.thread().Checkout, m.state.DraftProjectID, ""
	}
	return "thread:" + m.state.Active + ":" + m.thread().Checkout, "", m.state.Active
}

func (m *Model) nextCheckoutInspection() tea.Cmd {
	key, projectID, threadID := m.checkoutTarget()
	if key == "" || key == m.checkoutKey || !m.connected || m.client == nil || !m.hasCapability("workspace-info") {
		return nil
	}
	m.checkoutKey, m.checkoutLoading = key, true
	m.checkoutInfo = protocol.WorkspaceInfo{}
	connection, ctx := m.client, m.ctx
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		info, err := connection.Workspace(deadline, projectID, threadID)
		return checkoutMsg{key: key, info: info, err: err}
	}
}

func (m *Model) displayedCheckout() protocol.WorkspaceInfo {
	key, _, _ := m.checkoutTarget()
	if key == m.checkoutKey && !m.checkoutLoading && m.checkoutInfo.Path != "" {
		return m.checkoutInfo
	}
	path := m.thread().Checkout
	kind := "checkout"
	if strings.HasPrefix(path, "fixture://") {
		kind = "fixture"
	}
	return protocol.WorkspaceInfo{Path: path, Kind: kind, State: "unavailable"}
}

func (m *Model) checkoutLabels() (string, string) {
	info := m.displayedCheckout()
	left := "Local checkout"
	if info.Kind == "worktree" {
		left = "Worktree"
	} else if info.Kind == "fixture" {
		left = "Demo checkout"
	}
	if info.Path != "" {
		left += " · " + filepath.Base(info.Path)
	}
	right := "Branch unavailable"
	if m.checkoutLoading {
		right = "Reading branch…"
	} else {
		switch info.State {
		case "branch":
			right = info.Branch
		case "unborn":
			right = info.Branch + " (unborn)"
		case "detached":
			right = "Detached · " + info.Revision
		case "non-git":
			right = "Not a Git checkout"
		}
	}
	return left, right
}

func (m *Model) renderCheckoutContext(f *frame, r shell.Rect) {
	p := m.colors()
	x, width := r.X+composerInset(r.W), max(1, r.W-2*composerInset(r.W))
	left, right := m.checkoutLabels()
	rightWidth := min(ansi.StringWidth(right), max(1, width/2))
	leftWidth := max(1, width-rightWidth-2)
	f.button(m, x, r.Y, leftWidth, left, "checkout-info", action{Kind: "checkout-info"}, p.muted, p.canvas)
	f.hits[len(f.hits)-1].Label = m.displayedCheckout().Path + " · Checkout details / refresh"
	f.button(m, x+width-rightWidth, r.Y, rightWidth, right, "checkout-branch", action{Kind: "checkout-info"}, p.muted, p.canvas)
	f.hits[len(f.hits)-1].Label = right + " · Observed on selection; open to refresh"
}

func (m *Model) openCheckoutInfo() {
	info := m.displayedCheckout()
	left, right := m.checkoutLabels()
	items := []menuItem{{Label: left, Action: action{Kind: "noop"}}, {Label: info.Path, Action: action{Kind: "noop"}}, {Label: right, Action: action{Kind: "noop"}}}
	if info.Error != "" {
		items = append(items, menuItem{Label: info.Error, Action: action{Kind: "noop"}})
	}
	if m.hasCapability("workspace-info") {
		items = append(items, menuItem{Label: "Refresh checkout / branch", Action: action{Kind: "checkout-refresh"}})
	} else {
		items = append(items, menuItem{Label: "Update this server to inspect the branch", Action: action{Kind: "noop"}})
	}
	m.showMenu("Checkout", items)
}

func (m *Model) closedBannerHeight(width int) int {
	if !m.thread().Closed {
		return 0
	}
	if width < 72 {
		return 2
	}
	return 1
}

func (m *Model) renderClosedBanner(f *frame, r shell.Rect) int {
	height := m.closedBannerHeight(r.W)
	if height == 0 {
		return r.Y
	}
	p := m.colors()
	x, width := r.X+composerInset(r.W), r.W-2*composerInset(r.W)
	label := "This thread is closed · Send a message to reopen"
	if height == 2 {
		f.text(x, r.Y, width, "This thread is closed", p.text, p.canvas)
		label = "Send to reopen"
	}
	y := r.Y + height - 1
	f.text(x, y, max(1, width-10), label, p.muted, p.canvas)
	f.button(m, x+width-8, y, 8, "Reopen", "thread-reopen", action{Kind: "thread-reopen", ID: m.state.Active}, p.blue, p.input)
	return r.Y + height
}
