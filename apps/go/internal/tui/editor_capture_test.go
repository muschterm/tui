package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

const captureDoc = "package main\n\nimport \"fmt\"\n\n// Greeting 漢字 😀 e\u0301 and a control \x1b[31m here.\nfunc main() {\n\tname := \"world\"\n\tfmt.Println(\"hello, \" + name)\n}\n"

// TestEditorCaptures writes ANSI captures of the editor states when
// TUI_GO_CAPTURE_DIR is set (render with scripts/render-capture.py).
func TestEditorCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("TUI_GO_CAPTURE_DIR not set")
	}
	for _, light := range []bool{false, true} {
		for _, tc := range []struct {
			name          string
			width, height int
			setup         func(h *docHarness)
		}{
			{"editing", 144, 40, func(h *docHarness) {
				h.edit()
				for range 6 {
					h.key(tea.KeyDown, 0)
				}
				h.key(tea.KeyEnd, 0)
				h.key(tea.KeyLeft, tea.ModShift|tea.ModCtrl)
			}},
			{"storing", 144, 40, func(h *docHarness) {
				h.edit()
				h.srv.hold = true
				h.typeText("// draft ")
			}},
			{"retrying", 144, 40, func(h *docHarness) {
				h.edit()
				h.srv.hold = true
				h.typeText("x")
				h.srv.drop()
				h.settle()
			}},
			{"unsaved", 144, 40, func(h *docHarness) {
				h.edit()
				h.srv.setStatus(func(st *protocol.DocumentStatus) { st.State = protocol.DocumentStatePending })
				h.typeText("// more\n")
				h.srv.mu.Lock()
				h.srv.status.SavedRev = 0
				h.srv.mu.Unlock()
				h.srv.setStatus(func(st *protocol.DocumentStatus) {})
				h.settle()
				s := h.session()
				s.status.SavedRev = s.status.DurableRev - 3
			}},
			{"other-editor", 144, 40, func(h *docHarness) {
				h.srv.setStatus(func(st *protocol.DocumentStatus) { st.Editor, st.EditGen = "other-client", st.EditGen+1 })
				h.settle()
				h.m.setFocus("files-text")
			}},
			{"conflict", 144, 40, func(h *docHarness) {
				h.srv.versions = protocol.DocumentVersions{ID: "doc-1", Base: []byte(captureDoc), Document: []byte(captureDoc),
					Disk: []byte(strings.Replace(captureDoc, "hello, ", "goodbye, ", 1)), DiskState: "present", DiskID: "sha256:1"}
				h.edit()
				h.key(tea.KeyHome, tea.ModCtrl)
				h.typeText("// mine\n")
				h.srv.setStatus(func(st *protocol.DocumentStatus) {
					st.State, st.Versions = protocol.DocumentStatePausedConflict, true
				})
				h.settle()
			}},
			{"review", 144, 40, func(h *docHarness) {
				h.srv.versions = protocol.DocumentVersions{ID: "doc-1", Base: []byte(captureDoc), Document: []byte(captureDoc),
					Disk: []byte(strings.Replace(captureDoc, "hello, ", "goodbye, ", 1)), DiskState: "present", DiskID: "sha256:1"}
				h.edit()
				h.key(tea.KeyHome, tea.ModCtrl)
				h.typeText("// mine\n")
				h.srv.setStatus(func(st *protocol.DocumentStatus) {
					st.State, st.Versions = protocol.DocumentStatePausedConflict, true
				})
				h.settle()
				h.click("doc-review")
			}},
			{"review-confirm", 144, 40, func(h *docHarness) {
				h.srv.versions = protocol.DocumentVersions{ID: "doc-1", Base: []byte(captureDoc), Document: []byte(captureDoc),
					Disk: []byte(strings.Replace(captureDoc, "hello, ", "goodbye, ", 1)), DiskState: "present", DiskID: "sha256:1"}
				h.srv.setStatus(func(st *protocol.DocumentStatus) {
					st.State, st.Versions = protocol.DocumentStatePausedConflict, true
				})
				h.settle()
				h.click("doc-review")
				h.click("doc-review-keep")
			}},
			{"lost", 144, 40, func(h *docHarness) {
				h.edit()
				h.srv.hold = true
				h.typeText("mine")
				h.srv.setStatus(func(st *protocol.DocumentStatus) { st.Editor, st.EditGen = "other-client", st.EditGen+1 })
				h.srv.release()
				h.settle()
				h.settle()
			}},
			{"narrow", 44, 24, func(h *docHarness) {
				h.m.activate(action{Kind: "column", Index: int(shell.RightRegion)})
				h.settle()
				h.edit()
				h.key(tea.KeyDown, 0)
				h.key(tea.KeyDown, 0)
				h.key(tea.KeyDown, 0)
				h.key(tea.KeyDown, 0)
			}},
		} {
			h := newDocHarness(t, captureDoc)
			h.m.state.Light = light
			h.do(tea.WindowSizeMsg{Width: tc.width, Height: tc.height})
			tc.setup(h)
			name := fmt.Sprintf("%dx%d-editor-%s-light%t.ansi", tc.width, tc.height, tc.name, light)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(h.m.compose(true).rows, "\n")), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
