package hris

// Payroll inputs (PRD P5 EP-09, EP-11, EP-12): amounts that sources outside
// the payroll engine add to an employee's pay of a period — service charge
// distribution (EP-11), sales commission and bonus payout (EP-12), and any
// later source. The payroll engine (EP-09) collects them from every
// registered source when it calculates a run; a source never writes payroll
// tables. Sources register from internal/app (or an init in their own
// hris sub-package) and must be deterministic for a period: calculating the
// same run twice returns the same lines, and a line already consumed by a
// posted run is not returned again (the source keeps its own "paid in run"
// marker, set through MarkPayrollInputsConsumed when the run is posted).

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
)

// PayrollInputKind classifies an input line for the payslip and taxes.
type PayrollInputKind string

// Kinds of payroll input lines.
const (
	PayrollInputEarning   PayrollInputKind = "earning"   // taxable income (PPh 21 gross)
	PayrollInputDeduction PayrollInputKind = "deduction" // after-tax deduction
	PayrollInputNonTax    PayrollInputKind = "non_taxable"
)

// PayrollInputLine is one amount for one employee in a payroll period.
type PayrollInputLine struct {
	EmployeeID    uuid.UUID        `json:"employeeId"`
	Source        string           `json:"source"`        // registered source code, e.g. "service_charge", "commission"
	ComponentCode string           `json:"componentCode"` // payroll component, e.g. SERVICE_CHARGE, COMMISSION, BONUS
	Description   string           `json:"description"`
	Kind          PayrollInputKind `json:"kind"`
	Amount        decimal.Decimal  `json:"amount"`
	SourceType    string           `json:"sourceType"` // e.g. "hris.service_charge_distribution"
	SourceID      uuid.UUID        `json:"sourceId"`   // the source document, for drill-down and the consumed marker
	Irregular     bool             `json:"irregular"`  // irregular income for PPh 21 (bonus, THR-like)
}

// PayrollInputSource returns the input lines of a property's payroll
// period [from, to] (club dates) for the given employees (nil = all).
type PayrollInputSource struct {
	Code  string
	Lines func(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, employees []uuid.UUID) ([]PayrollInputLine, error)
	// Consumed marks the source documents of the given lines as paid by the
	// payroll run (called in the transaction that posts the run).
	Consumed func(ctx context.Context, q dbtx.Querier, property, payrollRunID uuid.UUID, lines []PayrollInputLine) error
}

var (
	payrollInputMu      sync.RWMutex
	payrollInputSources = map[string]PayrollInputSource{}
)

// RegisterPayrollInputSource adds (or replaces) a payroll input source.
func RegisterPayrollInputSource(s PayrollInputSource) {
	payrollInputMu.Lock()
	defer payrollInputMu.Unlock()
	payrollInputSources[s.Code] = s
}

// PayrollInputSources lists the registered sources ordered by code.
func PayrollInputSources() []PayrollInputSource {
	payrollInputMu.RLock()
	defer payrollInputMu.RUnlock()
	out := make([]PayrollInputSource, 0, len(payrollInputSources))
	for _, s := range payrollInputSources {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// CollectPayrollInputs gathers the lines of every registered source.
func CollectPayrollInputs(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, employees []uuid.UUID) ([]PayrollInputLine, error) {
	var out []PayrollInputLine
	for _, s := range PayrollInputSources() {
		lines, err := s.Lines(ctx, q, property, from, to, employees)
		if err != nil {
			return nil, err
		}
		for i := range lines {
			lines[i].Source = s.Code
		}
		out = append(out, lines...)
	}
	return out, nil
}

// MarkPayrollInputsConsumed tells each source which of its lines a posted
// payroll run paid.
func MarkPayrollInputsConsumed(ctx context.Context, q dbtx.Querier, property, payrollRunID uuid.UUID, lines []PayrollInputLine) error {
	by := map[string][]PayrollInputLine{}
	for _, l := range lines {
		by[l.Source] = append(by[l.Source], l)
	}
	for _, s := range PayrollInputSources() {
		if ls := by[s.Code]; len(ls) > 0 && s.Consumed != nil {
			if err := s.Consumed(ctx, q, property, payrollRunID, ls); err != nil {
				return err
			}
		}
	}
	return nil
}
