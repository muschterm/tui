package term

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

const diffWidth = 200

type emuState struct {
	cells  []string
	cursor string
}

func emuDump(e *vt.Emulator) emuState {
	var st emuState
	for y := range e.Height() {
		for x := range e.Width() {
			c := e.CellAt(x, y)
			if c == nil {
				st.cells = append(st.cells, "<nil>")
				continue
			}
			st.cells = append(st.cells, fmt.Sprintf("%q/%d", c.Content, c.Width))
		}
	}
	st.cursor = fmt.Sprint(e.CursorPosition())
	return st
}

func unlimited(p string) emuState {
	e := vt.NewEmulator(diffWidth, 3)
	_, _ = e.Write([]byte(p))
	return emuDump(e)
}

// limited feeds p in the given chunks, marking every chunk but the last as
// followed by more output, then flushes.
func limited(t *testing.T, w, h int, chunks ...[]byte) *vt.Emulator {
	t.Helper()
	e := vt.NewEmulator(w, h)
	var l clusterLimiter
	for i, c := range chunks {
		l.write(e, c, i < len(chunks)-1)
	}
	l.flush(e)
	return e
}

var clusterCorpus = map[string]string{
	"thai":          "ที่นี่มีน้ำ ภาษาไทย|",
	"hindi":         "क्षत्रिय हिन्दी नमस्ते|",
	"hangul-nfd":    "한글|",
	"family-skin":   "👨🏻‍👩🏻‍👧🏻‍👦🏻|",
	"kiss-skin":     "👩🏻‍❤️‍💋‍👨🏼|",
	"flags":         "🇺🇸🇬🇧🇯🇵🇩🇪🇫🇷|",
	"tag-flag":      "🏴\U000e0067\U000e0062\U000e0065\U000e006e\U000e0067\U000e007f|",
	"latin-marks":   "éạ̀ȫ ё|",
	"zwj-chain":     "🧑‍🤝‍🧑 🏳️‍🌈 👁️‍🗨️|",
	"with-escapes":  "a\x1b[31ḿb\x1b[0m̂ ok|",
	"cjk-and-ascii": "漢字かなカナ abc 한국어|",
}

// Text whose clusters fit the cap is stored exactly as by the unlimited
// emulator, whole or split across reads at any byte.
func TestClusterLimiterMatchesUnlimitedEmulator(t *testing.T) {
	for name, text := range clusterCorpus {
		want := unlimited(text)
		if got := emuDump(limited(t, diffWidth, 3, []byte(text))); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s: limited differs\n got %v\nwant %v", name, got, want)
		}
		for k := 1; k < len(text); k++ {
			got := emuDump(limited(t, diffWidth, 3, []byte(text[:k]), []byte(text[k:])))
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("%s split at %d: limited differs\n got %v\nwant %v", name, k, got, want)
			}
		}
	}
}

func chunked(b []byte, n int) [][]byte {
	var out [][]byte
	for len(b) > n {
		out = append(out, b[:n])
		b = b[n:]
	}
	return append(out, b)
}

// Floods of every extending class stay within the cap in the grid and the
// scrollback, fed whole or in read-sized chunks, and later text is intact.
func TestClusterLimiterBoundsFloods(t *testing.T) {
	floods := map[string]string{
		"thai-sara-am":    "ก" + strings.Repeat("ำ", 1500),
		"lao-am":          "ກ" + strings.Repeat("ຳ", 1500),
		"prepend-0d4e":    strings.Repeat("ൎ", 1500) + "ക",
		"halfwidth-ff9e":  "ｶ" + strings.Repeat("ﾞ", 1500),
		"halfwidth-ff9f":  "ﾊ" + strings.Repeat("ﾟ", 1500),
		"mn":              "a" + strings.Repeat("́", 3000),
		"variation-sel":   "❤" + strings.Repeat("️", 1500),
		"regional-ind":    strings.Repeat("🇺", 1500),
		"tags":            "🏴" + strings.Repeat("\U000e0067", 1500),
		"invalid+marks":   "\xff" + strings.Repeat("́", 3000),
		"invalid-bytes":   strings.Repeat("\xff\xfe", 3000),
		"zwj-chain":       strings.Repeat("👨‍", 800),
		"hangul-jamo":     strings.Repeat("ᄀ", 1500) + "ᅡ",
		"marks-only-line": strings.Repeat("́", 3000),
	}
	for name, flood := range floods {
		var b strings.Builder
		for range 4 {
			b.WriteString(flood + "X\r\n")
		}
		b.WriteString("END")
		input := []byte(b.String())
		for _, size := range []int{len(input), readChunk, 1000, 7} {
			e := limited(t, 40, 2, chunked(input, size)...)
			longest := 0
			for y := range e.Height() {
				for x := range e.Width() {
					if c := e.CellAt(x, y); c != nil {
						longest = max(longest, len(c.Content))
					}
				}
			}
			sb := e.Scrollback()
			for i := range sb.Len() {
				for _, c := range sb.Line(i) {
					longest = max(longest, len(c.Content))
				}
			}
			if longest > MaxClusterBytes {
				t.Fatalf("%s in %d-byte chunks stored a %d-byte cluster", name, size, longest)
			}
			var last strings.Builder
			for x := range 3 {
				if c := e.CellAt(x, 1); c != nil {
					last.WriteString(c.Content)
				}
			}
			if last.String() != "END" {
				t.Fatalf("%s in %d-byte chunks: text after the flood is %q", name, size, last.String())
			}
			for _, row := range convertAll(e) {
				for _, c := range row {
					if len(c.Text) > MaxClusterBytes {
						t.Fatalf("%s: converted cell of %d bytes", name, len(c.Text))
					}
				}
			}
		}
	}
}

func convertAll(e *vt.Emulator) [][]Cell {
	var out [][]Cell
	for y := range e.Height() {
		out = append(out, convertLine(e.Width(), func(x int) *uv.Cell { return e.CellAt(x, y) }))
	}
	return out
}

func TestCapLenKeepsWholeRunes(t *testing.T) {
	s := "a" + strings.Repeat("́", 100)
	if n := capLen(s); n != 127 {
		t.Fatalf("capLen = %d", n)
	}
	if n := capLen(strings.Repeat("\xff", 200)); n != MaxClusterBytes {
		t.Fatalf("capLen over invalid bytes = %d", n)
	}
	if got := capCluster(strings.Repeat("😀", 40)); len(got) != 128 {
		t.Fatalf("capCluster kept %d bytes", len(got))
	}
}
