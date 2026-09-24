package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

const conversationMarkdownFixture = "# Rendering notes\n\n" +
	"This **important** change is *carefully* formatted with `inline **literal**`, plus a [reference link](https://example.test/docs).\n\n" +
	"- first list item\n" +
	"- second list item\n\n" +
	"```go\n" +
	"fmt.Println(\"fenced **literal**\")\n" +
	"```\n\n" +
	"This final sentence is plain.\n"

func TestConversationRegressionPlacesLegacyQuestionAndRendersMarkdown(t *testing.T) {
	captures := os.Getenv("TUI_GO_CAPTURE_DIR")
	if captures != "" {
		if err := os.MkdirAll(captures, 0700); err != nil {
			t.Fatal(err)
		}
	}

	for _, size := range [][2]int{{60, 32}, {160, 50}} {
		for _, light := range []bool{false, true} {
			name := fmt.Sprintf("%dx%d-light%t", size[0], size[1], light)
			t.Run(name, func(t *testing.T) {
				m := testModel()
				thread := &m.snapshot.Threads[0]
				thread.State = "idle"
				thread.Children = nil
				thread.Plan = nil
				thread.Queue = nil
				thread.Requests = []protocol.Request{{
					ID: "legacy-layout-question", Kind: "question", State: "resolved",
					Delivery: "fixture-confirmed", TurnID: "turn-legacy",
					Questions: []protocol.Question{{
						ID: "layout", Text: "Which layout should guide the report?", Kind: "single", Options: []string{"Compact"},
					}},
					QuestionAnswers: []protocol.Answer{{Choices: []string{"Compact"}}},
				}}
				thread.Activity = []protocol.Activity{
					{ID: "legacy-user", Role: "user", TurnID: "turn-legacy", Text: "Choose a layout for the report."},
					{ID: "legacy-reply", Role: "agent", TurnID: "turn-legacy", Text: "The earlier layout question is now resolved."},
					{ID: "new-user", Role: "user", TurnID: "turn-new", Text: "Format the release notes now."},
					{ID: "new-reply", Role: "agent", TurnID: "turn-new", Text: conversationMarkdownFixture},
				}
				originalMarkdown := thread.Activity[len(thread.Activity)-1].Text

				m.state.Light = light
				m.setFocus("prompt")
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				m.configureInputs()

				frame := m.render()
				lines := m.transcriptLines(*thread, frame.transcript.W)
				visible := make([]string, 0, len(lines))
				for i, line := range lines {
					width, limit := ansi.StringWidth(line.text), frame.transcript.W
					if line.outset {
						limit += 2 // Answered cards span the prompt outline's extent.
					}
					if width > limit {
						t.Errorf("transcript line %d is %d cells wide for a %d-cell viewport: %q", i, width, frame.transcript.W, ansi.Strip(line.text))
					}
					visible = append(visible, ansi.Strip(line.text))
				}
				transcript := strings.Join(visible, "\n")
				// Narrow agent columns may break a phrase across rows; whitespace
				// normalisation keeps the words comparable without joining them.
				flat := strings.Join(strings.Fields(transcript), " ")
				for _, want := range []string{
					"Choose a layout for the report.", "The earlier layout question is now resolved.",
					"Which layout should guide the report?", "ANSWERED", "Format the release notes now.",
					"Rendering notes", "important", "carefully", "inline **literal**", "reference link",
					"first list item", "second list item", `fmt.Println("fenced **literal**")`, "This final sentence is plain.",
				} {
					if !strings.Contains(flat, want) {
						t.Errorf("transcript is missing %q:\n%s", want, transcript)
					}
				}
				questionPosition := strings.Index(flat, "Which layout should guide the report?")
				newPromptPosition := strings.Index(flat, "Format the release notes now.")
				if questionPosition < 0 || newPromptPosition < 0 || questionPosition >= newPromptPosition {
					t.Errorf("legacy question was not inserted between its earlier turn and the newer turn:\n%s", transcript)
				}
				markdownPosition := strings.Index(flat, "Rendering notes")
				if newPromptPosition < 0 || markdownPosition < 0 || newPromptPosition >= markdownPosition {
					t.Errorf("newer assistant reply does not follow its user prompt:\n%s", transcript)
				}

				withoutLiteralCode := strings.ReplaceAll(flat, "inline **literal**", "")
				withoutLiteralCode = strings.ReplaceAll(withoutLiteralCode, `fmt.Println("fenced **literal**")`, "")
				if strings.Contains(withoutLiteralCode, "**") || strings.Contains(withoutLiteralCode, "*") || strings.Contains(withoutLiteralCode, "`") {
					t.Errorf("Markdown delimiters remain outside literal code:\n%s", transcript)
				}
				if strings.Contains(flat, "[reference link](") || strings.Contains(flat, "# Rendering notes") || strings.Contains(flat, "```go") {
					t.Errorf("Markdown syntax was printed as message text:\n%s", transcript)
				}
				if !strings.Contains(transcript, "• first list item") && !strings.Contains(transcript, "- first list item") {
					t.Errorf("list item lost its visible marker:\n%s", transcript)
				}

				m.viewState().Scroll = m.measure().transcriptMax
				view := m.View().Content
				viewRows := strings.Split(view, "\n")
				for i, row := range viewRows {
					if got := ansi.StringWidth(row); got != size[0] {
						t.Errorf("rendered row %d is %d cells wide, want %d", i, got, size[0])
					}
				}
				assertRenderedSGR(t, view, "important", 1, true)
				assertRenderedSGR(t, view, "carefully", 3, true)
				assertRenderedSGR(t, view, "plain.", 1, false)
				assertRenderedSGR(t, view, "plain.", 3, false)
				if thread.Activity[len(thread.Activity)-1].Text != originalMarkdown {
					t.Fatal("rendering changed the stored Markdown source")
				}

				if captures != "" {
					file := filepath.Join(captures, fmt.Sprintf("%dx%d-light%t-conversation-markdown.ansi", size[0], size[1], light))
					if err := os.WriteFile(file, []byte(view), 0600); err != nil {
						t.Fatal(err)
					}
					if size[0] == 60 {
						m.viewState().Scroll = 0
						history := filepath.Join(captures, fmt.Sprintf("%dx%d-light%t-conversation-markdown-history.ansi", size[0], size[1], light))
						if err := os.WriteFile(history, []byte(m.View().Content), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			})
		}
	}
}

// assertRenderedSGR checks that the requested SGR attribute is active when a
// token is painted in the final cell grid, after the normal row renderer runs.
func assertRenderedSGR(t *testing.T, rendered, token string, attribute int, wantActive bool) {
	t.Helper()
	bold, italic := false, false
	for i := 0; i < len(rendered); {
		if rendered[i] == '\x1b' && i+1 < len(rendered) && rendered[i+1] == '[' {
			end := strings.IndexByte(rendered[i+2:], 'm')
			if end < 0 {
				break
			}
			end += i + 2
			codes := strings.Split(rendered[i+2:end], ";")
			for n := 0; n < len(codes); n++ {
				code := codes[n]
				// Extended colors have positional numeric payloads. A red/green
				// channel can equal 1 or 3 and must not toggle bold or italic.
				if code == "38" || code == "48" {
					if n+1 < len(codes) {
						if codes[n+1] == "2" {
							n = min(n+4, len(codes)-1)
						} else if codes[n+1] == "5" {
							n = min(n+2, len(codes)-1)
						}
					}
					continue
				}
				if strings.HasPrefix(code, "38:") || strings.HasPrefix(code, "48:") {
					continue
				}
				switch code {
				case "", "0":
					bold, italic = false, false
				case "1":
					bold = true
				case "3":
					italic = true
				case "22":
					bold = false
				case "23":
					italic = false
				}
			}
			i = end + 1
			continue
		}
		if strings.HasPrefix(rendered[i:], token) {
			active := bold
			if attribute == 3 {
				active = italic
			}
			if active != wantActive {
				t.Errorf("token %q SGR attribute %d active = %t, want %t", token, attribute, active, wantActive)
			}
			return
		}
		i++
	}
	t.Errorf("rendered view did not contain styled token %q", token)
}
