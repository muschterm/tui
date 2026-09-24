//go:build !unix

package term

import "context"

// Session is unavailable on this platform.
type Session struct{}

// Start reports ErrUnavailable.
func Start(context.Context, Config) (*Session, error) { return nil, ErrUnavailable }

// Write reports ErrUnavailable.
func (*Session) Write([]byte) error { return ErrUnavailable }

// Resize reports ErrUnavailable.
func (*Session) Resize(int, int) error { return ErrUnavailable }

// Snapshot returns an empty screen.
func (*Session) Snapshot() Screen { return Screen{} }

// ScrollbackLen returns 0.
func (*Session) ScrollbackLen() int { return 0 }

// ScrollbackLines returns nil.
func (*Session) ScrollbackLines(int, int) [][]Cell { return nil }

// Changed returns nil.
func (*Session) Changed() <-chan struct{} { return nil }

// Done returns a closed channel.
func (*Session) Done() <-chan struct{} { c := make(chan struct{}); close(c); return c }

// ExitStatus reports no status.
func (*Session) ExitStatus() (ExitStatus, bool) { return ExitStatus{}, false }

// Close reports ErrUnavailable.
func (*Session) Close(context.Context) error { return ErrUnavailable }
