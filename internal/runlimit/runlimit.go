// Package runlimit caps how many long, memory-holding runs (the anonymise
// tool and ingest) a server holds at once, so concurrent uploads cannot
// exhaust its memory together. A nil Limiter imposes no cap.
package runlimit

import "sync"

// Limiter counts concurrent holders of a slot. The zero value, like nil,
// imposes no cap; New builds one that does.
type Limiter struct {
	mu   sync.Mutex
	used int
	max  int
}

// New returns a Limiter of n concurrent holders; n <= 0 means no limit (nil),
// like the server's other size knobs.
func New(n int) *Limiter {
	if n <= 0 {
		return nil
	}
	return &Limiter{max: n}
}

// Acquire takes a slot and reports whether one was free.
func (l *Limiter) Acquire() bool {
	if l == nil || l.max <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used >= l.max {
		return false
	}
	l.used++
	return true
}

// Release gives a slot back. Releasing without a successful Acquire is a
// programming error; it is guarded anyway so one mistake cannot push the
// count negative and block every later run.
func (l *Limiter) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used > 0 {
		l.used--
	}
}
