package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/muschterm/tui/apps/go/internal/shell"
)

func TestComposerControlsFitAndMeasureExactly(t *testing.T) {
	for _, plain := range []bool{false, true} {
		for _, width := range []int{24, 44, 48, 72, 120} {
			m := testModel()
			m.plainIcons = plain
			f := frame{}
			next := m.renderComposerControls(&f, shell.Rect{X: 3, W: width}, 4)
			if next != 4+m.composerControlsHeight(width) {
				t.Fatal("height mismatch")
			}
			for i, h := range f.hits {
				if h.Rect.X < 5 || h.Rect.X+h.Rect.W > 3+width-2 || h.Rect.Y < 4 || h.Rect.Y >= next {
					t.Fatalf("width %d: %#v", width, h)
				}
				for _, other := range f.hits[:i] {
					if h.Rect.Y == other.Rect.Y && h.Rect.X < other.Rect.X+other.Rect.W && other.Rect.X < h.Rect.X+h.Rect.W {
						t.Fatalf("overlap: %#v %#v", h, other)
					}
				}
				if h.Key == "send" && h.slot().X+h.slot().W != 3+width-2 {
					t.Fatal("send not aligned with inset right edge")
				}
				if h.Key == "usage" && h.Label != "Open usage details" {
					t.Fatal("usage action lost its accessible description", h)
				}
			}
		}
	}
}

func TestComposerSettingsAndActionsShareInsetRow(t *testing.T) {
	for _, width := range []int{44, 48, 72, 120} {
		m := testModel()
		f := frame{}
		m.renderComposerControls(&f, shell.Rect{X: 3, W: width}, 4)
		agent := controlHit(t, f, "settings:agent")
		model := controlHit(t, f, "settings:model")
		send := controlHit(t, f, "send")
		usage := controlHit(t, f, "usage")
		if agent.Rect.X != 5 || model.Rect.Y != send.Rect.Y || agent.Rect.Y != send.Rect.Y || model.Rect.X+model.Rect.W >= usage.Rect.X {
			t.Fatalf("width %d: settings and actions do not share the row with a gap", width)
		}
		if width >= 72 && m.composerControlsHeight(width) != 1 {
			t.Fatal("ordinary footer still uses two rows")
		}
	}
}

func TestStopAndEffectiveSettingsOnlyForActiveTurn(t *testing.T) {
	for _, state := range []string{"running", "waiting", "idle", "completed"} {
		for _, resume := range []bool{false, true} {
			m := testModel()
			m.snapshot.Threads[0].State = state
			m.snapshot.Threads[0].NeedsResume = resume
			m.viewState().Settings.Effort = "high"
			controls, hidden := m.composerLayout(48)
			controls = append(controls, hidden...)
			foundStop := false
			for _, c := range controls {
				foundStop = foundStop || c.action.Kind == "interrupt"
			}
			want := !resume && (state == "running" || state == "waiting")
			if foundStop != want || m.configurationLocked() != want {
				t.Fatalf("%s resume %v: stop %v locked %v", state, resume, foundStop, m.configurationLocked())
			}
			wantSettings := m.viewState().Settings
			if want {
				wantSettings = m.thread().Effective
			}
			if m.composerSelection() != wantSettings {
				t.Fatal("row does not show the current editable/effective settings")
			}
		}
	}
}

func TestComposerRecoveryControlsRemainReachable(t *testing.T) {
	m := testModel()
	m.state.Edit = &editState{}
	m.snapshot.Threads[0].NeedsResume = true
	controls, hidden := m.composerLayout(44)
	controls = append(controls, hidden...)
	for _, key := range []string{"save-edit", "cancel-edit", "resume", "send", "attach", "usage"} {
		found := false
		for _, c := range controls {
			found = found || c.key == key
		}
		if !found {
			t.Fatal("missing", key)
		}
	}
}

func TestComposerProgressivelyOverflowsWithoutLosingActions(t *testing.T) {
	for _, plain := range []bool{false, true} {
		m := testModel()
		m.plainIcons = plain
		m.viewState().Settings.Context = "1M"
		m.viewState().Settings.Speed = "fast"
		m.snapshot.Threads[0].Effective = m.viewState().Settings
		previous := map[string]bool{}
		all, _ := m.composerLayout(240)
		for width := 240; width >= 24; width-- {
			visible, hidden := m.composerLayout(width)
			seen, collapsed := map[string]bool{}, map[string]bool{}
			for _, c := range visible {
				if c.y != 0 || c.x < composerInset(width) || c.x+c.width > width-composerInset(width) {
					t.Fatalf("width %d: control out of the single inset row: %+v", width, c)
				}
				seen[c.key] = true
			}
			for _, c := range hidden {
				if seen[c.key] {
					t.Fatalf("width %d: control duplicated: %s", width, c.key)
				}
				seen[c.key], collapsed[c.key] = true, true
			}
			if seen["composer-more"] != (len(hidden) > 0) {
				t.Fatalf("width %d: overflow visibility disagrees with hidden controls", width)
			}
			for _, c := range all {
				if !seen[c.key] {
					t.Fatalf("width %d: lost %s", width, c.key)
				}
			}
			for key := range previous {
				if !collapsed[key] {
					t.Fatalf("width %d: %s unexpectedly reappeared while shrinking", width, key)
				}
			}
			if collapsed["send"] || collapsed["interrupt"] || width >= 28 && collapsed["settings:model"] {
				t.Fatalf("width %d: primary controls collapsed too soon", width)
			}
			previous = collapsed
		}
	}
}

func TestComposerLongUnicodeModelTruncatesBeforeHiding(t *testing.T) {
	m := testModel()
	m.viewState().Settings.Model = strings.Repeat("模型é", 20)
	m.snapshot.Threads[0].Effective = m.viewState().Settings
	controls, _ := m.composerLayout(28)
	for _, c := range controls {
		if c.key == "settings:model" {
			if !strings.HasSuffix(c.label, "…") || ansi.StringWidth(c.label) > c.width-2 || !strings.Contains(c.help, m.viewState().Settings.Model) {
				t.Fatalf("model lost its full accessible name or exceeded cells: %+v", c)
			}
			return
		}
	}
	t.Fatal("model hidden instead of truncated")
}

func TestComposerOverflowPointerKeyboardResizeAndDraft(t *testing.T) {
	for _, pointer := range []bool{false, true} {
		m := testModel()
		m.snapshot.Threads[0].State = "idle"
		m.Update(tea.WindowSizeMsg{Width: 48, Height: 24})
		m.setFocus("prompt")
		m.Update(tea.PasteMsg{Content: "keep this unsent draft"})
		more := controlHit(t, m.measure(), "composer-more")
		if pointer {
			m.Update(tea.MouseClickMsg{X: more.Rect.X, Y: more.Rect.Y, Button: tea.MouseLeft})
		} else {
			m.setFocus("composer-more")
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
		if m.menuTitle != "More settings" || len(m.menu) == 0 {
			t.Fatal("ellipsis did not open the composer menu")
		}
		index := -1
		for i, item := range m.menu {
			if item.Action == (action{Kind: "settings", Value: "effort"}) {
				index = i
			}
		}
		if index < 0 {
			t.Fatal("hidden effort not reachable")
		}
		m.menuIndex = index
		item := m.menu[index]
		m.Update(tea.WindowSizeMsg{Width: 180, Height: 50})
		snapshot := m.snapshot
		snapshot.Revision++
		m.Update(snapshotMsg(snapshot))
		if m.menuIndex != index || m.menu[index] != item {
			t.Fatal("resize or background activity moved the selected menu action")
		}
		if pointer {
			h := controlHit(t, m.measure(), fmt.Sprintf("menu:%d", index))
			m.Update(tea.MouseClickMsg{X: h.Rect.X + 1, Y: h.Rect.Y, Button: tea.MouseLeft})
		} else {
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
		if m.menuTitle != "Settings · effort" || len(m.menu) != 3 {
			t.Fatal("overflow did not route to the existing effort picker")
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if m.prompt.Value() != "keep this unsent draft" || m.busy != nil || m.viewState().Settings.Effort != "medium" {
			t.Fatal("overflow navigation changed draft, settings or submitted work")
		}
	}
}

func TestComposerFocusFollowsCollapsedControl(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 180, Height: 50})
	m.setFocus("settings:permissions")
	m.Update(tea.WindowSizeMsg{Width: 48, Height: 24})
	if m.focus != "composer-more" {
		t.Fatalf("collapsed control lost keyboard access: focus = %q", m.focus)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.HasPrefix(m.menuTitle, "More settings") {
		t.Fatal("Enter did not follow collapsed control into overflow")
	}
}

func TestComposerOverflowStaysWithItsOwnGroup(t *testing.T) {
	for _, plain := range []bool{false, true} {
		m := testModel()
		m.plainIcons = plain
		for width := 34; width <= 160; width++ {
			controls, hidden := m.composerLayout(width)
			byKey := map[string]composerControl{}
			for _, c := range controls {
				byKey[c.key] = c
			}
			usage, attach, stop, send := byKey["usage"], byKey["attach"], byKey["interrupt"], byKey["send"]
			if usage.x+usage.width != attach.x || attach.x+attach.width != stop.x || stop.x+stop.width != send.x || send.x+send.width != width-composerInset(width) {
				t.Fatalf("width %d: right-hand controls lost their order or alignment", width)
			}
			for _, c := range hidden {
				if c.key == "usage" || c.key == "attach" || c.key == "interrupt" || c.key == "send" {
					t.Fatalf("width %d: action leaked into settings overflow: %s", width, c.key)
				}
			}
			if more, ok := byKey["composer-more"]; ok {
				end := composerInset(width)
				for _, c := range controls {
					if c.x < more.x {
						end = c.x + c.width
					}
				}
				if more.x != end || more.x+more.width >= usage.x {
					t.Fatalf("width %d: settings ellipsis is not adjacent to its fields", width)
				}
			}
			if !strings.Contains(usage.label, "Ctx") && !strings.HasPrefix(strings.TrimSpace(usage.label), m.icon("more-vertical")) {
				t.Fatalf("width %d: hidden usage detail lacks its own ellipsis", width)
			}
		}
	}
}

func TestUsageSummaryHasIndependentMouseAndKeyboardAccess(t *testing.T) {
	for _, pointer := range []bool{false, true} {
		m := testModel()
		m.Update(tea.WindowSizeMsg{Width: 48, Height: 24})
		m.setFocus("prompt")
		m.Update(tea.PasteMsg{Content: "unsent usage review"})
		usage := controlHit(t, m.measure(), "usage")
		if pointer {
			m.Update(tea.MouseClickMsg{X: usage.Rect.X + 1, Y: usage.Rect.Y, Button: tea.MouseLeft})
		} else {
			m.setFocus("usage")
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
		if m.menuTitle != "Usage" {
			t.Fatal("usage opened the settings menu or a surface instead of its summary")
		}
		for _, label := range []string{"Context used: unavailable", "Context capacity: unavailable", "Context percentage: unavailable", "Billing mode: unknown", "Subscription limits: unavailable", "API cost: unavailable"} {
			found := false
			for _, item := range m.menu {
				found = found || item.Label == label && item.Action.Kind == "noop"
			}
			if !found {
				t.Fatal("missing honest, read-only measurement", label)
			}
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if m.prompt.Value() != "unsent usage review" || m.busy != nil {
			t.Fatal("reading usage changed or submitted the draft")
		}
	}
}

func TestUsageGaugeDistinguishesUnknownZeroAndClamps(t *testing.T) {
	capacity := int64(100)
	if got := usageGauge(nil, &capacity); got != "Ctx ?" {
		t.Fatal(got)
	}
	for _, tt := range []struct {
		used int64
		want string
	}{{0, "Ctx 0%"}, {50, "Ctx 50%"}, {100, "Ctx 100%"}, {150, "Ctx 100%"}, {-1, "Ctx ?"}} {
		if got := usageGauge(&tt.used, &capacity); got != tt.want {
			t.Errorf("%d: %q", tt.used, got)
		}
	}
	zero := int64(0)
	if got := usageGauge(&zero, &zero); got != "Ctx ?" {
		t.Fatal(got)
	}
}
