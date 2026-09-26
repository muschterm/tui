package server

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Pure hunk/line selection for partial staging (ADR 0025): parse one path's
// unified diff and apply a selection of its changes to the base blob's bytes.
// Nothing here runs Git; git_partial.go supplies the pinned inputs.

// diffLine is one hunk line. kind is ' ', '-' or '+'; text excludes the diff
// prefix and the "\n"; noNL is Git's "\ No newline at end of file".
type diffLine struct {
	kind byte
	text []byte
	noNL bool
}

// diffHunk is one parsed hunk; first is the global index of lines[0].
type diffHunk struct {
	header                               string
	oldStart, oldLines, newStart, newLen int
	first                                int
	lines                                []diffLine
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

var errDiffBinary = errors.New("binary diff")

// parseHunks parses the hunks of a single-file unified diff produced with
// the fixed options of git_partial.go. Anything unexpected is an error: the
// caller refuses partial staging rather than guessing.
func parseHunks(diff []byte) ([]diffHunk, error) {
	lines := bytes.Split(diff, []byte("\n"))
	if n := len(lines); n > 0 && len(lines[n-1]) == 0 {
		lines = lines[:n-1]
	} else if len(diff) > 0 {
		return nil, errors.New("diff does not end with a newline")
	}
	i := 0
	// Extended header lines up to the first hunk.
	for ; i < len(lines) && !bytes.HasPrefix(lines[i], []byte("@@ ")); i++ {
		if bytes.HasPrefix(lines[i], []byte("Binary files ")) || bytes.HasPrefix(lines[i], []byte("GIT binary patch")) {
			return nil, errDiffBinary
		}
		if i > 0 && bytes.HasPrefix(lines[i], []byte("diff ")) {
			return nil, errors.New("diff covers more than one file")
		}
	}
	var hunks []diffHunk
	global := 0
	for i < len(lines) {
		m := hunkHeader.FindSubmatch(lines[i])
		if m == nil {
			return nil, fmt.Errorf("unexpected diff line %d", i)
		}
		num := func(b []byte, def int) (int, error) {
			if b == nil {
				return def, nil
			}
			return strconv.Atoi(string(b))
		}
		h := diffHunk{header: string(lines[i]), first: global}
		var err error
		if h.oldStart, err = num(m[1], 0); err != nil {
			return nil, err
		}
		if h.oldLines, err = num(m[2], 1); err != nil {
			return nil, err
		}
		if h.newStart, err = num(m[3], 0); err != nil {
			return nil, err
		}
		if h.newLen, err = num(m[4], 1); err != nil {
			return nil, err
		}
		i++
		oldLeft, newLeft := h.oldLines, h.newLen
		for i < len(lines) {
			l := lines[i]
			if len(l) > 0 && l[0] == '\\' {
				if len(h.lines) == 0 || h.lines[len(h.lines)-1].noNL {
					return nil, errors.New("misplaced no-newline marker")
				}
				h.lines[len(h.lines)-1].noNL = true
				i++
				continue
			}
			if oldLeft == 0 && newLeft == 0 {
				break
			}
			if len(l) == 0 {
				return nil, errors.New("empty diff line")
			}
			switch l[0] {
			case ' ':
				oldLeft--
				newLeft--
			case '-':
				oldLeft--
			case '+':
				newLeft--
			default:
				return nil, fmt.Errorf("unexpected diff line %d", i)
			}
			if oldLeft < 0 || newLeft < 0 {
				return nil, errors.New("hunk longer than its header")
			}
			h.lines = append(h.lines, diffLine{kind: l[0], text: l[1:]})
			i++
		}
		if oldLeft != 0 || newLeft != 0 || len(h.lines) == 0 {
			return nil, errors.New("hunk shorter than its header")
		}
		if n := len(hunks); n > 0 {
			prev := hunks[n-1]
			if hunkStart(h.oldStart, h.oldLines) < hunkStart(prev.oldStart, prev.oldLines)+prev.oldLines {
				return nil, errors.New("hunks overlap")
			}
		}
		global += len(h.lines)
		hunks = append(hunks, h)
	}
	return hunks, nil
}

// hunkStart is the 0-based base index where a hunk's pre-image begins; an
// empty pre-image "-n,0" inserts after line n.
func hunkStart(start, count int) int {
	if count == 0 {
		return start
	}
	return start - 1
}

// baseLine is one line of a blob; noNL marks a final line without "\n".
type baseLine struct {
	text []byte
	noNL bool
}

func splitBlob(b []byte) []baseLine {
	if len(b) == 0 {
		return nil
	}
	parts := bytes.Split(b, []byte("\n"))
	noNL := len(parts[len(parts)-1]) > 0
	if !noNL {
		parts = parts[:len(parts)-1]
	}
	out := make([]baseLine, len(parts))
	for i, p := range parts {
		out[i] = baseLine{text: p}
	}
	out[len(out)-1].noNL = noNL
	return out
}

// joinBlob ends every line with "\n" except a last line marked noNL: a
// missing final newline is a property of whichever line ends up last.
func joinBlob(lines []baseLine) []byte {
	var b bytes.Buffer
	for i, l := range lines {
		b.Write(l.text)
		if i < len(lines)-1 || !l.noNL {
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

var errBaseMismatch = errors.New("the diff does not match the base content")

// applySelection returns base with exactly the selected change lines of
// hunks (whose pre-image is base) applied: a selected '-' line is removed, a
// selected '+' line is inserted, unselected '-' lines are kept and unselected
// '+' lines are dropped. Within one change block (a maximal run of '-' and
// '+' lines) the selected '+' lines take the place of the first selected '-'
// line: kept '-' lines before it stay in front of them and kept '-' lines
// after it follow them (research A2: "-l4 -l5 +L4 +L5" with "-l4 +L4"
// selected gives "L4 l5", with "-l5 +L5" selected "l4 L5"). A block with no
// selected '-' line keeps all its old lines first and appends the selected
// '+' lines, as `git add -p` does. Each group keeps its original order.
// Every context and '-' line is
// checked against base, so a diff that does not describe base is refused
// (errBaseMismatch) instead of producing unrelated content.
func applySelection(base []baseLine, hunks []diffHunk, selected func(int) bool) ([]baseLine, error) {
	out := make([]baseLine, 0, len(base))
	pos := 0
	same := func(l diffLine) bool {
		return pos < len(base) && bytes.Equal(base[pos].text, l.text) && base[pos].noNL == l.noNL
	}
	for _, h := range hunks {
		start := hunkStart(h.oldStart, h.oldLines)
		if start < pos || start > len(base) {
			return nil, errBaseMismatch
		}
		out = append(out, base[pos:start]...)
		pos = start
		for i := 0; i < len(h.lines); {
			if l := h.lines[i]; l.kind == ' ' {
				if !same(l) {
					return nil, errBaseMismatch
				}
				out = append(out, base[pos])
				pos++
				i++
				continue
			}
			var before, adds, after []baseLine
			removed := false
			for ; i < len(h.lines) && h.lines[i].kind != ' '; i++ {
				l := h.lines[i]
				switch {
				case l.kind == '-':
					if !same(l) {
						return nil, errBaseMismatch
					}
					switch {
					case selected(h.first + i):
						removed = true
					case removed:
						after = append(after, base[pos])
					default:
						before = append(before, base[pos])
					}
					pos++
				case selected(h.first + i):
					adds = append(adds, baseLine{text: l.text, noNL: l.noNL})
				}
			}
			out = append(append(append(out, before...), adds...), after...)
		}
		if pos-start != h.oldLines {
			return nil, errBaseMismatch
		}
	}
	return append(out, base[pos:]...), nil
}

// hunksView converts parsed hunks into the wire form.
func hunksView(hunks []diffHunk) ([]protocol.GitHunk, int) {
	out := make([]protocol.GitHunk, 0, len(hunks))
	count := 0
	for i, h := range hunks {
		v := protocol.GitHunk{Index: i, Header: h.header, OldStart: h.oldStart, OldLines: h.oldLines, NewStart: h.newStart, NewLines: h.newLen,
			Lines: make([]protocol.GitHunkLine, len(h.lines))}
		oldNo, newNo := hunkStart(h.oldStart, h.oldLines)+1, hunkStart(h.newStart, h.newLen)+1
		for j, l := range h.lines {
			line := protocol.GitHunkLine{Index: h.first + j, Text: string(l.text), NoNewline: l.noNL}
			switch l.kind {
			case ' ':
				line.Kind, line.OldLine, line.NewLine = protocol.GitHunkLineContext, oldNo, newNo
				oldNo++
				newNo++
			case '-':
				line.Kind, line.OldLine = protocol.GitHunkLineDelete, oldNo
				oldNo++
			case '+':
				line.Kind, line.NewLine = protocol.GitHunkLineAdd, newNo
				newNo++
			}
			v.Lines[j] = line
		}
		count += len(h.lines)
		out = append(out, v)
	}
	return out, count
}

// selectionSet validates a GitPartial selection against hunks and returns
// the selected global line indices.
func selectionSet(hunks []diffHunk, sel protocol.GitPartial) (map[int]bool, error) {
	kind := map[int]byte{}
	for _, h := range hunks {
		for j, l := range h.lines {
			kind[h.first+j] = l.kind
		}
	}
	set := map[int]bool{}
	seenHunk := map[int]bool{}
	for _, hi := range sel.Hunks {
		if hi < 0 || hi >= len(hunks) || seenHunk[hi] {
			return nil, failure("invalid", "the selection names a hunk that does not exist or names it twice")
		}
		seenHunk[hi] = true
		h := hunks[hi]
		for j, l := range h.lines {
			if l.kind != ' ' {
				set[h.first+j] = true
			}
		}
	}
	seenLine := map[int]bool{}
	for _, li := range sel.Lines {
		k, ok := kind[li]
		if !ok || seenLine[li] {
			return nil, failure("invalid", "the selection names a line that does not exist or names it twice")
		}
		if k == ' ' {
			return nil, failure("invalid", "context lines cannot be selected")
		}
		seenLine[li] = true
		set[li] = true
	}
	if len(set) == 0 {
		return nil, failure("invalid", "select at least one added or removed line")
	}
	return set, nil
}
