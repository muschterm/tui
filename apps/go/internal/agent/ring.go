package agent

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// ring retains the most recent bytes written to it. Adapter stderr is
// untrusted output from another program: it is bounded here and sanitized
// before it reaches any protocol field.
type ring struct {
	mu    sync.Mutex
	buf   []byte
	limit int
	over  bool
}

func newRing(limit int) *ring { return &ring{limit: limit} }

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	if len(p) > r.limit {
		p, r.over = p[len(p)-r.limit:], true
	}
	if len(r.buf)+len(p) > r.limit {
		drop := len(r.buf) + len(p) - r.limit
		r.buf, r.over = r.buf[drop:], true
	}
	r.buf = append(r.buf, p...)
	return n, nil
}

func (r *ring) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := string(r.buf)
	if r.over {
		out = "… earlier output dropped …\n" + out
	}
	return Sanitize(out)
}

// Sanitize makes agent- or file-supplied text safe to place in a protocol
// field. Escape sequences from a subprocess must never reach the terminal, and
// invalid UTF-8 must not corrupt the JSON snapshot.
func Sanitize(text string) string {
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\t':
			return r
		case '\r':
			return -1
		}
		if unicode.IsControl(r) || r == '​' {
			return -1
		}
		return r
	}, text)
}

// Truncate bounds retained text at a byte limit without splitting a rune and
// says so where it cuts.
func Truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n… truncated at the retention limit …"
}
