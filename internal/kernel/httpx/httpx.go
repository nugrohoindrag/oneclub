// Package httpx contains HTTP helpers shared by all modules: JSON encoding,
// RFC 9457 Problem Details, strict decoding and cursor pagination
// (Technical Doc §8.1).
package httpx

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/reqctx"
)

const ProblemBase = "https://docs.oneclub.id/problems/"

// Problem is an RFC 9457 Problem Details body.
type Problem struct {
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Status    int               `json:"status"`
	Detail    string            `json:"detail,omitempty"`
	Instance  string            `json:"instance,omitempty"`
	Code      string            `json:"code,omitempty"`
	RequestID string            `json:"requestId,omitempty"`
	Errors    []errs.FieldError `json:"errors,omitempty"`
}

var kindStatus = map[errs.Kind]int{
	errs.KindValidation:     http.StatusUnprocessableEntity,
	errs.KindBadRequest:     http.StatusBadRequest,
	errs.KindNotFound:       http.StatusNotFound,
	errs.KindUnauthorized:   http.StatusUnauthorized,
	errs.KindForbidden:      http.StatusForbidden,
	errs.KindModuleDisabled: http.StatusForbidden,
	errs.KindMFARequired:    http.StatusForbidden,
	errs.KindConflict:       http.StatusConflict,
	errs.KindLocked:         http.StatusLocked,
	errs.KindRateLimited:    http.StatusTooManyRequests,
	errs.KindPrecondition:   http.StatusPreconditionFailed,
	errs.KindUnavailable:    http.StatusServiceUnavailable,
	errs.KindInternal:       http.StatusInternalServerError,
}

// StatusOf maps an error to its HTTP status.
func StatusOf(err error) int {
	if e, ok := errs.As(err); ok {
		if s, ok := kindStatus[e.Kind]; ok {
			return s
		}
	}
	return http.StatusInternalServerError
}

// WriteError writes err as Problem Details. Unknown errors become 500 and are
// logged; their message is never leaked to the client.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	meta := reqctx.GetMeta(r.Context())
	e, ok := errs.As(err)
	if !ok {
		e = errs.Internal(err)
	}
	status := kindStatus[e.Kind]
	if status == 0 {
		status = http.StatusInternalServerError
	}
	if status >= 500 {
		slog.ErrorContext(r.Context(), "request failed", "err", err, "request_id", meta.RequestID, "path", r.URL.Path)
	}
	p := Problem{
		Type:      ProblemBase + string(e.Kind),
		Title:     http.StatusText(status),
		Status:    status,
		Detail:    e.Message,
		Instance:  r.URL.Path,
		Code:      e.Code,
		RequestID: meta.RequestID,
		Errors:    e.Fields,
	}
	if status >= 500 {
		p.Detail = "An unexpected error occurred. Quote the request id when contacting support."
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// JSON writes v with status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// NoContent writes 204.
func NoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

const maxBody = 4 << 20

// Decode strictly decodes a JSON body into v (unknown fields rejected).
func Decode(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return errs.BadRequest("body_unreadable", "request body could not be read")
	}
	if len(body) > maxBody {
		return errs.BadRequest("body_too_large", "request body too large")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		var se *json.SyntaxError
		var te *json.UnmarshalTypeError
		switch {
		case errors.As(err, &se):
			return errs.BadRequest("invalid_json", "malformed JSON")
		case errors.As(err, &te):
			return errs.Validation("invalid_type", "invalid field type", errs.Field(te.Field, "invalid_type", "expected "+te.Type.String()))
		case strings.HasPrefix(err.Error(), "json: unknown field"):
			f := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
			return errs.Validation("unknown_field", "unknown field", errs.Field(f, "unknown_field", "field is not allowed"))
		default:
			return errs.BadRequest("invalid_json", err.Error())
		}
	}
	return nil
}

// PathUUID parses a UUID path parameter.
func PathUUID(r *http.Request, name string) (uuid.UUID, error) {
	raw := chi.URLParam(r, name)
	u, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, errs.NotFound("resource")
	}
	return u, nil
}

// ListParams are the standard list query parameters:
// ?cursor=&limit=&q=&filter[status]=active&sort=-createdAt
type ListParams struct {
	Limit   int
	Cursor  string
	Q       string
	Sort    string
	Filters map[string]string
}

// ParseList parses list parameters from the query string.
func ParseList(r *http.Request) ListParams {
	q := r.URL.Query()
	lp := ListParams{Limit: 50, Cursor: q.Get("cursor"), Q: strings.TrimSpace(q.Get("q")), Sort: q.Get("sort"), Filters: map[string]string{}}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		lp.Limit = min(n, 500)
	}
	for k, v := range q {
		if strings.HasPrefix(k, "filter[") && strings.HasSuffix(k, "]") && len(v) > 0 {
			lp.Filters[k[7:len(k)-1]] = v[0]
		}
	}
	return lp
}

// Page is the standard list response envelope.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// EncodeCursor / DecodeCursor wrap an opaque keyset position.
func EncodeCursor(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func DecodeCursor(c string) (string, error) {
	if c == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", errs.BadRequest("invalid_cursor", "invalid cursor")
	}
	return string(b), nil
}

// BuildPage trims items to limit and computes the next cursor from the last
// returned item. Callers fetch limit+1 rows.
func BuildPage[T any](items []T, limit int, key func(T) string) Page[T] {
	if items == nil {
		items = []T{}
	}
	p := Page[T]{Items: items}
	if len(items) > limit {
		p.Items = items[:limit]
		p.NextCursor = EncodeCursor(key(p.Items[limit-1]))
	}
	return p
}

// ClientIP returns the client IP (X-Forwarded-For is trusted only because the
// API is always deployed behind Caddy).
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}
