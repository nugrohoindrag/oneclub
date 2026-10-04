package crm

// Website contact messages about a sales topic become leads of CRM Sales
// (PRD P3 FR-WEB-P3-02). The sales area lives in crm/sales; internal/app
// sets the hook so the P2 contact form keeps working without it.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SalesContactHook handles a contact message (in the contact transaction).
type SalesContactHook func(ctx context.Context, tx pgx.Tx, property uuid.UUID, customer Customer, guest PublicGuest, topic, message string) error

var salesContactHook SalesContactHook

// SetSalesContactHook plugs CRM Sales into the website contact form.
func SetSalesContactHook(fn SalesContactHook) { salesContactHook = fn }
