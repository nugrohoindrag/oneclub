package hris

// Migration reconciliation of wave 5 (PRD P5 EP-28 FR-MIG-P5-05): the
// control totals of the club's legacy HR system are compared with OneClub
// at the cutover date and signed off by the HR Manager and the Finance
// Manager. Metrics are registered here: Core HR registers headcount and
// leave balances (below); the payroll area registers its payroll totals
// (last period of the legacy system vs the OneClub parallel run) with
// RegisterReconciliationMetric from its own package.

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
)

// ReconciliationTotal is the key of a metric's grand total.
const ReconciliationTotal = "TOTAL"

// ReconciliationMetric computes the OneClub value of a control total.
type ReconciliationMetric struct {
	Code, Label string
	// KeyHelp documents the keys (shown in the import screen).
	KeyHelp string
	// Keys lists the breakdown keys OneClub knows at the cutover (e.g. org
	// unit codes), so keys missing from the legacy file are reported too.
	Keys func(ctx context.Context, q dbtx.Querier, property uuid.UUID, cutover time.Time) ([]string, error)
	// Value is the OneClub value of a key (ReconciliationTotal = all).
	Value func(ctx context.Context, q dbtx.Querier, property uuid.UUID, cutover time.Time, key string) (decimal.Decimal, error)
}

var (
	reconMu      sync.RWMutex
	reconMetrics = map[string]ReconciliationMetric{}
)

// RegisterReconciliationMetric adds or replaces a reconciliation metric.
func RegisterReconciliationMetric(m ReconciliationMetric) {
	reconMu.Lock()
	defer reconMu.Unlock()
	reconMetrics[m.Code] = m
}

// ReconciliationMetrics lists the metrics by code.
func ReconciliationMetrics() []ReconciliationMetric {
	reconMu.RLock()
	defer reconMu.RUnlock()
	out := make([]ReconciliationMetric, 0, len(reconMetrics))
	for _, m := range reconMetrics {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// ReconciliationMetricOf returns a metric by code.
func ReconciliationMetricOf(code string) (ReconciliationMetric, bool) {
	reconMu.RLock()
	defer reconMu.RUnlock()
	m, ok := reconMetrics[code]
	return m, ok
}

// ReconciliationInput is one legacy control total.
type ReconciliationInput struct {
	Metric string `json:"metric"`
	Key    string `json:"key"`
	Legacy string `json:"legacy"`
}

// ReconciliationLine is one compared control total.
type ReconciliationLine struct {
	Metric      string  `json:"metric"`
	MetricLabel string  `json:"metricLabel"`
	Key         string  `json:"key"`
	Legacy      *string `json:"legacy" doc:"null = not in the legacy file (OneClub only)"`
	OneClub     string  `json:"oneclub"`
	Difference  string  `json:"difference" doc:"OneClub − legacy"`
	Match       bool    `json:"match"`
}

// Reconcile computes the lines of a reconciliation: every legacy total, and
// the OneClub keys of a broken-down metric that the legacy file lacks.
func Reconcile(ctx context.Context, q dbtx.Querier, property uuid.UUID, cutover time.Time, in []ReconciliationInput) ([]ReconciliationLine, error) {
	out := []ReconciliationLine{}
	seen := map[string]bool{}
	breakdown := map[string]bool{}
	for _, x := range in {
		m, ok := ReconciliationMetricOf(x.Metric)
		if !ok {
			continue // validated by the caller
		}
		key := normaliseReconKey(x.Key)
		if seen[m.Code+"|"+key] {
			continue
		}
		seen[m.Code+"|"+key] = true
		if key != ReconciliationTotal && !strings.Contains(key, ":") {
			breakdown[m.Code] = true
		}
		legacy, err := decimal.NewFromString(strings.TrimSpace(x.Legacy))
		if err != nil {
			legacy = decimal.Zero
		}
		v, err := m.Value(ctx, q, property, cutover, key)
		if err != nil {
			return nil, err
		}
		ls := legacy.String()
		out = append(out, ReconciliationLine{Metric: m.Code, MetricLabel: m.Label, Key: key, Legacy: &ls, OneClub: v.String(),
			Difference: v.Sub(legacy).String(), Match: v.Equal(legacy)})
	}
	for _, code := range sortedKeys(breakdown) {
		m, _ := ReconciliationMetricOf(code)
		if m.Keys == nil {
			continue
		}
		keys, err := m.Keys(ctx, q, property, cutover)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			k = normaliseReconKey(k)
			if seen[m.Code+"|"+k] {
				continue
			}
			seen[m.Code+"|"+k] = true
			v, err := m.Value(ctx, q, property, cutover, k)
			if err != nil {
				return nil, err
			}
			if v.IsZero() {
				continue
			}
			out = append(out, ReconciliationLine{Metric: m.Code, MetricLabel: m.Label, Key: k, OneClub: v.String(), Difference: v.String()})
		}
	}
	return out, nil
}

func normaliseReconKey(k string) string {
	k = strings.ToUpper(strings.TrimSpace(k))
	if k == "" || k == "ALL" {
		return ReconciliationTotal
	}
	return k
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Core HR metrics: headcount (employed on the cutover date; key TOTAL or an
// org unit code) and leave balances (remaining days of the cutover year;
// key TOTAL, a leave type or EMPLOYEENO:LEAVETYPE).
func init() {
	RegisterReconciliationMetric(ReconciliationMetric{Code: "headcount", Label: "Headcount",
		KeyHelp: "TOTAL or an org unit code (employees employed on the cutover date)",
		Keys: func(ctx context.Context, q dbtx.Querier, property uuid.UUID, cutover time.Time) ([]string, error) {
			rows, err := q.Query(ctx, `SELECT code FROM hris.org_units WHERE property_id = $1 AND archived_at IS NULL ORDER BY code`, property)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			var out []string
			for rows.Next() {
				var c string
				if err := rows.Scan(&c); err != nil {
					return nil, err
				}
				out = append(out, c)
			}
			return out, rows.Err()
		},
		Value: func(ctx context.Context, q dbtx.Querier, property uuid.UUID, cutover time.Time, key string) (decimal.Decimal, error) {
			var n int64
			err := q.QueryRow(ctx, `SELECT count(*) FROM hris.employees e LEFT JOIN hris.org_units u ON u.id = e.org_unit_id
				WHERE e.property_id = $1 AND e.archived_at IS NULL AND (e.join_date IS NULL OR e.join_date <= $2::date)
				  AND (e.termination_date IS NULL OR e.termination_date > $2::date) AND (e.status = 'active' OR e.termination_date IS NOT NULL)
				  AND ($3 = 'TOTAL' OR upper(u.code) = $3)`, property, cutover.Format(time.DateOnly), key).Scan(&n)
			return decimal.NewFromInt(n), err
		}})
	RegisterReconciliationMetric(ReconciliationMetric{Code: "leave_balance", Label: "Leave Balance (days)",
		KeyHelp: "TOTAL, a leave type (e.g. ANNUAL) or EMPLOYEENO:LEAVETYPE (remaining days of the cutover year)",
		Keys: func(ctx context.Context, q dbtx.Querier, property uuid.UUID, cutover time.Time) ([]string, error) {
			rows, err := q.Query(ctx, `SELECT DISTINCT upper(leave_type) FROM hris.leave_balances WHERE property_id = $1 AND year = $2 ORDER BY 1`,
				property, cutover.Year())
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			var out []string
			for rows.Next() {
				var c string
				if err := rows.Scan(&c); err != nil {
					return nil, err
				}
				out = append(out, c)
			}
			return out, rows.Err()
		},
		Value: func(ctx context.Context, q dbtx.Querier, property uuid.UUID, cutover time.Time, key string) (decimal.Decimal, error) {
			emp, typ := "", key
			if i := strings.IndexByte(key, ':'); i >= 0 {
				emp, typ = key[:i], key[i+1:]
			}
			if typ == ReconciliationTotal {
				typ = ""
			}
			var v decimal.Decimal
			err := q.QueryRow(ctx, `SELECT coalesce(sum(b.entitled + b.carried_over - b.carried_expired + b.adjusted - b.used), 0)
				FROM hris.leave_balances b JOIN hris.employees e ON e.id = b.employee_id
				WHERE b.property_id = $1 AND b.year = $2 AND e.archived_at IS NULL AND (e.termination_date IS NULL OR e.termination_date > $3::date)
				  AND ($4 = '' OR upper(b.leave_type) = $4) AND ($5 = '' OR upper(e.employee_no) = $5)`,
				property, cutover.Year(), cutover.Format(time.DateOnly), typ, emp).Scan(&v)
			return v, err
		}})
}
