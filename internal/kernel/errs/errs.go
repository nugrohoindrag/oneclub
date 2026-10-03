// Package errs defines typed domain errors. They are mapped to HTTP status
// codes and RFC 9457 Problem Details in exactly one place (httpx.WriteError).
package errs

import (
	"errors"
	"fmt"
)

// Kind classifies a domain error.
type Kind string

const (
	KindValidation     Kind = "validation"
	KindNotFound       Kind = "not-found"
	KindUnauthorized   Kind = "unauthorized"
	KindForbidden      Kind = "forbidden"
	KindConflict       Kind = "conflict"
	KindModuleDisabled Kind = "module-disabled"
	KindMFARequired    Kind = "mfa-required"
	KindLocked         Kind = "locked"
	KindRateLimited    Kind = "rate-limited"
	KindBadRequest     Kind = "bad-request"
	KindPrecondition   Kind = "precondition-failed"
	KindUnavailable    Kind = "unavailable"
	KindInternal       Kind = "internal"
)

// FieldError describes one invalid input field.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error is a typed domain error.
type Error struct {
	Kind    Kind
	Code    string // stable machine code, e.g. "venue.code_taken"
	Message string // human message (English; frontend translates by Code)
	Fields  []FieldError
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

func newErr(k Kind, code, msg string) *Error { return &Error{Kind: k, Code: code, Message: msg} }

func Validation(code, msg string, fields ...FieldError) *Error {
	e := newErr(KindValidation, code, msg)
	e.Fields = fields
	return e
}
func NotFound(what string) *Error {
	return newErr(KindNotFound, "not_found", what+" not found")
}
func Unauthorized(msg string) *Error   { return newErr(KindUnauthorized, "unauthorized", msg) }
func Forbidden(msg string) *Error      { return newErr(KindForbidden, "forbidden", msg) }
func Conflict(code, msg string) *Error { return newErr(KindConflict, code, msg) }
func BadRequest(code, msg string) *Error {
	return newErr(KindBadRequest, code, msg)
}
func ModuleDisabled(module string) *Error {
	return newErr(KindModuleDisabled, "module_disabled", "module "+module+" is not enabled for this instance")
}
func MFARequired(msg string) *Error { return newErr(KindMFARequired, "mfa_required", msg) }
func Locked(msg string) *Error      { return newErr(KindLocked, "account_locked", msg) }
func RateLimited() *Error {
	return newErr(KindRateLimited, "rate_limited", "too many requests")
}
func Precondition(msg string) *Error { return newErr(KindPrecondition, "precondition_failed", msg) }
func Unavailable(msg string) *Error  { return newErr(KindUnavailable, "unavailable", msg) }
func Internal(err error) *Error {
	return &Error{Kind: KindInternal, Code: "internal", Message: "internal error", Err: err}
}

// Field builds a FieldError.
func Field(field, code, msg string) FieldError {
	return FieldError{Field: field, Code: code, Message: msg}
}

// As extracts *Error from err.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Is reports whether err is a domain error of kind k.
func Is(err error, k Kind) bool {
	e, ok := As(err)
	return ok && e.Kind == k
}
