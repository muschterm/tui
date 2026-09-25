package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// files.go serves the read-only Files surface (GET /v1/files/*). Reads are
// confined beneath the selected checkout: intermediate directories are opened
// with openParent (no symlink below the root is followed) and the final
// component is inspected without following it. Nothing here writes.

// filesRequestBudget bounds one Files read.
const filesRequestBudget = 5 * time.Second

// filesScanLimit bounds how many names one directory listing scans.
const filesScanLimit = 20000

// filesBinaryProbe is how much of a file decides text versus binary.
const filesBinaryProbe = 8 << 10

var errFilesFixture = errors.New("fixture checkout")

func filesFailure(w http.ResponseWriter, err error) {
	var pe *protocol.Error
	switch {
	case errors.Is(err, errFilesFixture):
		writeJSON(w, http.StatusConflict, failure("unavailable", "fixture checkouts do not contain local files"))
	case errors.As(err, &pe) && pe.Code == "invalid":
		writeJSON(w, http.StatusBadRequest, pe)
	case errors.As(err, &pe) && pe.Code == "not_found":
		writeJSON(w, http.StatusNotFound, pe)
	case errors.As(err, &pe) && pe.Code == "not_directory":
		writeJSON(w, http.StatusConflict, pe)
	case errors.As(err, &pe):
		writeJSON(w, http.StatusServiceUnavailable, pe)
	case errors.Is(err, context.DeadlineExceeded):
		writeJSON(w, http.StatusServiceUnavailable, failure("unavailable", "file read timed out"))
	default:
		writeJSON(w, http.StatusServiceUnavailable, failure("unavailable", "file read failed"))
	}
}

// filesHandle resolves the target checkout (like the Git reads) and applies
// the per-request deadline.
func (e *engine) filesHandle(w http.ResponseWriter, r *http.Request, read func(ctx context.Context, root string) (any, error)) {
	root, ok := e.gitTarget(w, r)
	if !ok {
		return
	}
	if strings.HasPrefix(root, "fixture://") {
		filesFailure(w, errFilesFixture)
		return
	}
	if root == "" {
		filesFailure(w, failure("unavailable", "checkout directory is unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), filesRequestBudget)
	defer cancel()
	result, err := read(ctx, root)
	if err != nil {
		filesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (e *engine) filesList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hidden := q.Get("hidden") == "1"
	e.filesHandle(w, r, func(ctx context.Context, root string) (any, error) {
		return listFiles(ctx, root, q.Get("dir"), q.Get("cursor"), hidden)
	})
}

// filesReadSlots bounds concurrent file reads (each may hold 16 MiB); a
// read waits for a slot within its request budget.
var filesReadSlots = make(chan struct{}, 4)

func (e *engine) filesRead(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	e.filesHandle(w, r, func(ctx context.Context, root string) (any, error) {
		select {
		case filesReadSlots <- struct{}{}:
			defer func() { <-filesReadSlots }()
		case <-ctx.Done():
			return nil, failure("busy", "the server is reading other files · try again")
		}
		return readFile(ctx, root, p)
	})
}

func (e *engine) filesStat(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	e.filesHandle(w, r, func(ctx context.Context, root string) (any, error) { return statFile(ctx, root, p) })
}

// validFilesDir accepts the checkout root ("") or a valid relative path.
func validFilesDir(dir string) bool { return dir == "" || validFilesPath(dir) }

// validFilesPath is validGitPath with a length bound and no control-free
// requirement: names are untrusted but addressable; clients sanitize them.
func validFilesPath(p string) bool {
	return len(p) <= 4096 && utf8.ValidString(p) && validGitPath(p)
}

// dirEntryName is one scanned name and whether it sorts with directories
// (a directory by d_type; a symlink to one is not).
type dirEntryName struct {
	name string
	dir  bool
}

// fileOrder sorts directories first, then everything else, each in byte
// order. It is the listing's total order, so cursors stay stable.
func fileOrder(a, b dirEntryName) bool {
	if a.dir != b.dir {
		return a.dir
	}
	return a.name < b.name
}

func encodeFilesCursor(e dirEntryName) string {
	prefix := "1"
	if e.dir {
		prefix = "0"
	}
	return base64.RawURLEncoding.EncodeToString([]byte(prefix + e.name))
}

func decodeFilesCursor(s string) (dirEntryName, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) < 2 || (raw[0] != '0' && raw[0] != '1') {
		return dirEntryName{}, false
	}
	return dirEntryName{name: string(raw[1:]), dir: raw[0] == '0'}, true
}

// pageFiles filters, sorts and pages scanned names: .git is never listed and
// dot names only with hidden. It returns the page and the next cursor.
func pageFiles(names []dirEntryName, cursor string, hidden bool) ([]dirEntryName, string, error) {
	var after *dirEntryName
	if cursor != "" {
		c, ok := decodeFilesCursor(cursor)
		if !ok {
			return nil, "", failure("invalid", "invalid listing cursor")
		}
		after = &c
	}
	kept := names[:0:0]
	for _, n := range names {
		if strings.EqualFold(n.name, ".git") || !hidden && strings.HasPrefix(n.name, ".") {
			continue
		}
		if after != nil && !fileOrder(*after, n) {
			continue
		}
		kept = append(kept, n)
	}
	sort.Slice(kept, func(i, j int) bool { return fileOrder(kept[i], kept[j]) })
	if len(kept) <= protocol.FileListPage {
		return kept, "", nil
	}
	page := kept[:protocol.FileListPage]
	return page, encodeFilesCursor(page[len(page)-1]), nil
}

// classifyFile fills a regular file read from its first bytes. data holds at
// most FileTextLimit+1 bytes of the file; size is the file's size.
func classifyFile(out *protocol.FileRead, data []byte, size int64) {
	probe := data[:min(len(data), filesBinaryProbe)]
	if bytes.IndexByte(probe, 0) >= 0 || mostlyInvalid(probe) {
		out.Kind = protocol.FileReadBinary
		return
	}
	out.Kind = protocol.FileReadText
	if len(data) > protocol.FileTextLimit || size > int64(len(data)) {
		data = data[:min(len(data), protocol.FileTextLimit)]
		out.Truncated = true
		// Never split a UTF-8 sequence at the cut.
		data = trimPartialRune(data)
	}
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		out.BOM, data = true, data[3:]
	}
	out.Encoding = "utf-8"
	text := string(data)
	if !utf8.ValidString(text) {
		out.Encoding, text = "invalid", strings.ToValidUTF8(text, "�")
	}
	out.Newline = newlineStyle(text)
	out.Text = text
}

// trimPartialRune drops an incomplete UTF-8 sequence at the end of data.
func trimPartialRune(data []byte) []byte {
	for i := 1; i <= utf8.UTFMax && i <= len(data); i++ {
		b := data[len(data)-i]
		if !utf8.RuneStart(b) {
			continue
		}
		if !utf8.FullRune(data[len(data)-i:]) {
			return data[:len(data)-i]
		}
		return data
	}
	return data
}

// mostlyInvalid reports whether more than a quarter of the probe's bytes
// belong to invalid UTF-8 sequences; such content is treated as binary.
func mostlyInvalid(probe []byte) bool {
	bad := 0
	for i := 0; i < len(probe); {
		r, n := utf8.DecodeRune(probe[i:])
		if r == utf8.RuneError && n == 1 {
			// A sequence cut by the probe's end is not evidence.
			if len(probe)-i < utf8.UTFMax && !utf8.FullRune(probe[i:]) {
				break
			}
			bad++
		}
		i += n
	}
	return bad*4 > len(probe)
}

// newlineStyle reports lf, crlf, mixed or none.
func newlineStyle(text string) string {
	crlf := strings.Count(text, "\r\n")
	lf := strings.Count(text, "\n") - crlf
	switch {
	case crlf == 0 && lf == 0:
		return "none"
	case crlf == 0:
		return "lf"
	case lf == 0:
		return "crlf"
	}
	return "mixed"
}
