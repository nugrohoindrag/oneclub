package handle

import (
	"net/http"
	"sync"
	"time"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
)

// Limiter is a fixed-window, per-key request limiter for public endpoints
// (voucher code check, public booking holds) — FR-VCH-10, FR-WEB-P2-08.
type Limiter struct {
	N      int
	Period time.Duration
	mu     sync.Mutex
	hits   map[string]*window
}

type window struct {
	start time.Time
	n     int
}

// Allow records one hit for key and reports whether it is within the limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string]*window{}
	}
	now := time.Now()
	w := l.hits[key]
	if w == nil || now.Sub(w.start) > l.Period {
		if len(l.hits) > 10000 {
			l.hits = map[string]*window{}
		}
		l.hits[key] = &window{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= l.N
}

// Wrap rejects requests over the limit per client IP with 429.
func (l *Limiter) Wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(httpx.ClientIP(r) + " " + r.URL.Path) {
			httpx.WriteError(w, r, errs.RateLimited())
			return
		}
		h(w, r)
	}
}
