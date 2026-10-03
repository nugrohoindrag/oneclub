// Package id generates identifiers. All primary keys are UUIDv7 (time-ordered,
// safe to generate on offline clients) per Technical Documentation §5.3.
package id

import "github.com/google/uuid"

// New returns a new UUIDv7.
func New() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// Parse parses a UUID string.
func Parse(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}

// Ptr returns a pointer to u, or nil when u is the zero UUID.
func Ptr(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	return &u
}
