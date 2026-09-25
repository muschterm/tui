package term

import (
	"io"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// clusterLimiter bounds the grapheme clusters the emulator stores.
//
// x/vt composes a cluster from consecutive printable runes and stores it
// whole in one cell of the grid (and later the scrollback), so a child
// printing an endless run of extending characters (combining or spacing
// marks, prepended letters, joiners, variation selectors, tags) would grow
// one cell without bound. The limiter segments the output with the same
// segmenter the emulator uses (ansi.FirstGraphemeCluster) and, only when one
// cluster exceeds MaxClusterBytes, writes its first whole runes up to the cap
// and drops the rest of that cluster. Every other byte reaches the emulator
// unchanged and in order, so clusters within the cap, and all control
// sequences, are exactly what the unlimited emulator would store.
//
// Segmenting raw output rather than printable runs can attach a cluster to
// the final ASCII byte of a preceding escape sequence; that only makes the
// limiter's cluster one byte longer than the emulator's, never shorter, so
// the bound holds.
//
// The emulator flushes a pending cluster at the end of every Write. When a
// read filled the buffer (more output is waiting), the limiter holds back the
// trailing cluster and an incomplete trailing rune and prepends them to the
// next write, so clusters are not split at read boundaries; flush releases
// them when no more output follows.
type clusterLimiter struct {
	pending  []byte // held trailing bytes, at most MaxClusterBytes+utf8.UTFMax
	dropping bool   // continuation of an over-long cluster is being dropped
	ctx      []byte // last rune of that cluster, to recognise continuation
}

// write feeds p to w. more reports that further output is already waiting.
func (l *clusterLimiter) write(w io.Writer, p []byte, more bool) {
	buf := p
	if len(l.pending) > 0 {
		buf = append(l.pending, p...)
		l.pending = nil
	}
	if l.dropping {
		buf = l.skipContinuation(buf)
		if len(buf) == 0 {
			return
		}
	}
	complete := completeRunes(buf)
	start, i := 0, 0
	for i < complete {
		// Two ASCII bytes in a row are always separate clusters (CR LF aside,
		// which is never long); only non-ASCII needs the segmenter.
		if buf[i] < utf8.RuneSelf && i+1 < complete && buf[i+1] < utf8.RuneSelf {
			i++
			continue
		}
		cl, _ := ansi.FirstGraphemeCluster(buf[i:complete], ansi.GraphemeWidth)
		if len(cl) == 0 {
			break
		}
		end := i + len(cl)
		atEnd := end == complete
		if len(cl) <= MaxClusterBytes {
			if atEnd && (more || complete < len(buf)) {
				break // may continue in the next write: hold it
			}
			i = end
			continue
		}
		_, _ = w.Write(buf[start : i+capLen(cl)])
		start, i = end, end
		if atEnd {
			l.dropping = true
			l.ctx = append(l.ctx[:0], lastRune(cl)...)
		}
	}
	if start < i {
		_, _ = w.Write(buf[start:i])
	}
	if i < len(buf) {
		l.pending = append([]byte(nil), buf[i:]...)
	}
}

// flush writes held bytes; the emulator then completes what it can.
func (l *clusterLimiter) flush(w io.Writer) {
	if len(l.pending) == 0 {
		return
	}
	p := l.pending
	l.pending = nil
	l.write(w, p, false)
	if len(l.pending) > 0 { // an incomplete rune: the parser holds it
		_, _ = w.Write(l.pending)
		l.pending = nil
	}
}

// held reports whether bytes are waiting for the next write or flush.
func (l *clusterLimiter) held() bool { return len(l.pending) > 0 }

// skipContinuation drops the leading bytes of buf that continue the
// over-long cluster being dropped.
func (l *clusterLimiter) skipContinuation(buf []byte) []byte {
	complete := completeRunes(buf)
	seg := append(append([]byte(nil), l.ctx...), buf[:complete]...)
	cl, _ := ansi.FirstGraphemeCluster(seg, ansi.GraphemeWidth)
	k := len(cl) - len(l.ctx)
	if k <= 0 {
		l.dropping = false
		return buf
	}
	l.ctx = append(l.ctx[:0], lastRune(cl)...)
	if k < complete {
		l.dropping = false
	}
	return buf[k:]
}

// completeRunes is len(b) minus an incomplete UTF-8 sequence at its end.
func completeRunes(b []byte) int {
	for k := 1; k < utf8.UTFMax && k <= len(b); k++ {
		c := b[len(b)-k]
		if c < utf8.RuneSelf {
			return len(b)
		}
		if utf8.RuneStart(c) {
			if !utf8.FullRune(b[len(b)-k:]) {
				return len(b) - k
			}
			return len(b)
		}
	}
	return len(b)
}

// capLen is the length of the longest prefix of whole runes (invalid bytes
// count as one rune each) within MaxClusterBytes, at least one rune.
func capLen[T string | []byte](s T) int {
	n := 0
	for n < len(s) {
		var size int
		switch v := any(s[n:]).(type) {
		case string:
			_, size = utf8.DecodeRuneInString(v)
		case []byte:
			_, size = utf8.DecodeRune(v)
		}
		if n+size > MaxClusterBytes {
			if n == 0 {
				return size
			}
			break
		}
		n += size
	}
	return n
}

func lastRune(b []byte) []byte {
	_, size := utf8.DecodeLastRune(b)
	return b[len(b)-size:]
}
