package dbtx

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsRetryable(t *testing.T) {
	for code, want := range map[string]bool{"40P01": true, "40001": true, "23505": false} {
		err := fmt.Errorf("sync item: %w", &pgconn.PgError{Code: code})
		if IsRetryable(err) != want {
			t.Errorf("IsRetryable(%s) = %v", code, !want)
		}
	}
	if IsRetryable(errors.New("conn closed")) {
		t.Error("a non-PostgreSQL error is not retryable")
	}
}
