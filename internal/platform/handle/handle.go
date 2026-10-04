// Package handle removes the boilerplate of hand-written endpoints: decode
// the JSON body, run the use case in one database transaction (with the RLS
// scope of the request), and write the result or RFC 9457 problem. Use cases
// still record their own audit entries and events inside the transaction.
package handle

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
)

// Empty is the request type of endpoints without a body.
type Empty struct{}

// Write decodes Req, runs fn in a read-write transaction and writes Res.
func Write[Req any, Res any](db *dbtx.DB, status int, fn func(ctx context.Context, tx pgx.Tx, r *http.Request, req Req) (Res, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req Req
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := r.Context()
		var res Res
		err := db.WithTx(ctx, func(tx pgx.Tx) error {
			var err error
			res, err = fn(ctx, tx, r, req)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if status == http.StatusNoContent {
			httpx.NoContent(w)
			return
		}
		httpx.JSON(w, status, res)
	}
}

// Read runs fn in a read-only transaction and writes Res with 200.
func Read[Res any](db *dbtx.DB, fn func(ctx context.Context, tx pgx.Tx, r *http.Request) (Res, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var res Res
		err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
			var err error
			res, err = fn(ctx, tx, r)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, res)
	}
}

// List collects rows into []T using `db` struct tags (missing columns are
// left zero). A nil result is returned as an empty slice.
func List[T any](rows pgx.Rows, err error) ([]T, error) {
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[T])
	if out == nil {
		out = []T{}
	}
	return out, err
}

// One returns exactly one row as T; no row maps to a not-found error.
func One[T any](rows pgx.Rows, err error, what string) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByNameLax[T])
	if dbtx.IsNoRows(err) {
		return zero, errs.NotFound(what)
	}
	return v, err
}

// Get is One for direct use with Query results: handle.Get[T](tx.Query(...)).
func Get[T any](rows pgx.Rows, err error) (T, error) { return One[T](rows, err, "record") }

// Page wraps items in the standard list envelope.
func Page[T any](items []T, err error) (httpx.Page[T], error) {
	if items == nil {
		items = []T{}
	}
	return httpx.Page[T]{Items: items}, err
}

// Property returns the active property of a property-scoped route.
func Property(ctx context.Context) uuid.UUID {
	pid, _ := reqctx.Property(ctx)
	return pid
}

// ID parses the {id} path parameter (or another name).
func ID(r *http.Request, name ...string) (uuid.UUID, error) {
	n := "id"
	if len(name) > 0 {
		n = name[0]
	}
	return httpx.PathUUID(r, n)
}

// QueryUUID parses an optional UUID query parameter.
func QueryUUID(r *http.Request, name string) (*uuid.UUID, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return nil, nil
	}
	u, err := uuid.Parse(v)
	if err != nil {
		return nil, errs.BadRequest("invalid_"+name, name+" must be a UUID")
	}
	return &u, nil
}

// QueryDate parses a YYYY-MM-DD query parameter; def when absent.
func QueryDate(r *http.Request, name string, def time.Time) (time.Time, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return def, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return t, errs.BadRequest("invalid_"+name, name+" must be a date (YYYY-MM-DD)")
	}
	return t, nil
}

// QueryTime parses an RFC 3339 query parameter; def when absent.
func QueryTime(r *http.Request, name string, def time.Time) (time.Time, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return def, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return t, errs.BadRequest("invalid_"+name, name+" must be an RFC 3339 date-time")
	}
	return t.UTC(), nil
}

// QueryInt parses an integer query parameter; def when absent.
func QueryInt(r *http.Request, name string, def int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get(name)); err == nil {
		return n
	}
	return def
}

// Decimal parses a decimal input field; "" yields def.
func Decimal(field, v string, def decimal.Decimal) (decimal.Decimal, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return def, nil
	}
	d, err := decimal.NewFromString(v)
	if err != nil {
		return d, errs.Validation("invalid_amount", "invalid amount", errs.Field(field, "invalid", "must be a decimal number"))
	}
	return d, nil
}

// Required returns a validation error when v is empty.
func Required(field, v string) error {
	if strings.TrimSpace(v) == "" {
		return errs.Validation("required", field+" is required", errs.Field(field, "required", "is required"))
	}
	return nil
}

// Invalid builds a single-field validation error.
func Invalid(field, code, msg string) error {
	return errs.Validation(code, msg, errs.Field(field, code, msg))
}

// UserID returns the authenticated user of the request (uuid.Nil for
// anonymous and system callers).
func UserID(ctx context.Context) uuid.UUID {
	if p := authz.From(ctx); p != nil {
		return p.UserID
	}
	return uuid.Nil
}
