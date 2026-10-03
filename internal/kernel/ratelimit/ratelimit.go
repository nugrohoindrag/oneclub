// Package ratelimit is a small in-process fixed-window limiter used to
// protect public endpoints (website booking hold and payment, FR-WEB-07)
// and one-time login codes. It is per replica; the effective limit scales
// with the number of API replicas, which is acceptable for bot protection.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter counts hits per key in fixed windows.
type Limiter struct {
	mu   sync.Mutex
	hits map[string]*window
	// Disabled turns every check into a no-op (test environment).
	Disabled bool
}

type window struct {
	start time.Time
	n     int
}

// New returns an empty limiter.
func New() *Limiter { return &Limiter{hits: map[string]*window{}} }

// Allow reports whether key may proceed (max n per period).
func (l *Limiter) Allow(key string, n int, period time.Duration) bool {
	if l == nil || l.Disabled {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) > period {
		if len(l.hits) > 100000 {
			l.hits = map[string]*window{}
		}
		l.hits[key] = &window{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= n
}
