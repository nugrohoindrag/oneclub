// Package clock provides the current time. Always UTC; conversion to the
// instance timezone happens only at presentation (Technical Doc §5.3).
package clock

import (
	"sync/atomic"
	"time"
)

var offset atomic.Int64

// Now returns the current UTC time.
func Now() time.Time {
	return time.Now().UTC().Add(time.Duration(offset.Load()))
}

// SetOffsetForTest shifts Now by d. Tests only.
func SetOffsetForTest(d time.Duration) { offset.Store(int64(d)) }
