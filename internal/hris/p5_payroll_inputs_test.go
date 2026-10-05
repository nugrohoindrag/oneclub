package hris

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
)

func TestCollectPayrollInputs(t *testing.T) {
	emp := uuid.New()
	var consumed []PayrollInputLine
	RegisterPayrollInputSource(PayrollInputSource{Code: "test_source",
		Lines: func(context.Context, dbtx.Querier, uuid.UUID, time.Time, time.Time, []uuid.UUID) ([]PayrollInputLine, error) {
			return []PayrollInputLine{{EmployeeID: emp, ComponentCode: "BONUS", Kind: PayrollInputEarning, Amount: decimal.NewFromInt(100)}}, nil
		},
		Consumed: func(_ context.Context, _ dbtx.Querier, _, _ uuid.UUID, l []PayrollInputLine) error {
			consumed = l
			return nil
		}})
	t.Cleanup(func() { payrollInputMu.Lock(); delete(payrollInputSources, "test_source"); payrollInputMu.Unlock() })
	lines, err := CollectPayrollInputs(context.Background(), nil, uuid.New(), time.Now(), time.Now(), nil)
	if err != nil || len(lines) != 1 || lines[0].Source != "test_source" {
		t.Fatalf("collect: %v %v", lines, err)
	}
	if err := MarkPayrollInputsConsumed(context.Background(), nil, uuid.New(), uuid.New(), lines); err != nil || len(consumed) != 1 {
		t.Fatalf("consumed: %v %v", consumed, err)
	}
}
