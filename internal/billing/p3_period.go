package billing

// Financial period guard (PRD P4 contract K10): once Accounting closes a
// period, billing refuses corrections dated in it — reopening a business
// day, voiding an invoice issued in it or voiding a charge of it. Billing
// sits below accounting (Technical Doc §4.2), so the composition root plugs
// accounting.PeriodStatus in; without a guard every period is open.

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
)

// PeriodGuard reports whether the financial period of a date is closed.
type PeriodGuard func(ctx context.Context, q dbtx.Querier, property uuid.UUID, date time.Time) (closed bool, err error)

var (
	periodMu    sync.RWMutex
	periodGuard PeriodGuard
)

// RegisterPeriodGuard plugs the accounting period status into billing.
func (s *Service) RegisterPeriodGuard(fn PeriodGuard) {
	periodMu.Lock()
	defer periodMu.Unlock()
	periodGuard = fn
}

// ensureOpenPeriod refuses a correction dated in a closed financial period.
func ensureOpenPeriod(ctx context.Context, q dbtx.Querier, property uuid.UUID, date time.Time, what string) error {
	periodMu.RLock()
	fn := periodGuard
	periodMu.RUnlock()
	if fn == nil || date.IsZero() {
		return nil
	}
	closed, err := fn(ctx, q, property, date)
	if err != nil {
		return err
	}
	if closed {
		return errs.Conflict("period_closed", "the financial period of "+date.Format("2006-01-02")+" is closed; "+what)
	}
	return nil
}

// ExportGuard reports whether the Accounting Export of a property is
// stopped: Accounting signed the transition off and posts the books itself
// (PRD P4 FR-TRS-04). Exports made before stay downloadable.
type ExportGuard func(ctx context.Context, q dbtx.Querier, property uuid.UUID) (stopped bool, err error)

var exportGuard ExportGuard

// RegisterExportGuard plugs the accounting cut-over into the export.
func (s *Service) RegisterExportGuard(fn ExportGuard) {
	periodMu.Lock()
	defer periodMu.Unlock()
	exportGuard = fn
}

// exportStopped reports whether the export of a property has stopped;
// without a guard it never stops.
func exportStopped(ctx context.Context, q dbtx.Querier, property uuid.UUID) (bool, error) {
	periodMu.RLock()
	fn := exportGuard
	periodMu.RUnlock()
	if fn == nil {
		return false, nil
	}
	return fn(ctx, q, property)
}
