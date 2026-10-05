package commercial

// Package component allocation (FR-PKG-04): commercial sits below the
// business lines (Technical Doc §4.2) and the Reservation Engine imports
// commercial for pricing, so the composition root registers one allocator
// per component type — Reservation Engine resources (bungalow, meeting
// room, banquet venue …), golf tee times and vouchers. A booking calls the
// allocators of all components inside its transaction: when one fails the
// whole transaction rolls back and nothing stays allocated (all-or-nothing).

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// AllocationRequest asks a business line to allocate one package component.
type AllocationRequest struct {
	Property           uuid.UUID
	BookingID          uuid.UUID
	BookingNumber      string
	PackageCode        string
	PackageName        string
	BookingComponentID uuid.UUID
	ComponentType      string
	ComponentName      string
	ResourceTypeCode   string
	ResourceID         *uuid.UUID
	RefCode            string    // golf course code, voucher type code …
	ServiceDate        time.Time // local service date
	Start, End         time.Time // scheduled period (the allocator may adjust it, e.g. check-in / check-out times)
	Nights             int
	Quantity           decimal.Decimal
	Pax                int
	CustomerID         *uuid.UUID
	CorporateAccountID *uuid.UUID
	GuestName          string
	GuestPhone         string
	GuestEmail         string
	Channel            string
	Value              decimal.Decimal // allocated total of the component (e.g. voucher value)
	FolioID            *uuid.UUID
	ExpiresAt          *time.Time
	DryRun             bool // availability check inside a savepoint that is rolled back
}

// Allocation is what a business line reserved for a component.
type Allocation struct {
	Ref        string         // reservation code, golf booking code, voucher codes …
	ID         *uuid.UUID     // reservation / golf booking / first voucher
	ResourceID *uuid.UUID     // the resource chosen
	Start, End *time.Time     // the period actually allocated
	Details    map[string]any // allocator data needed to release it
}

// ReleaseRequest frees the allocation of a component (cancellation, expiry).
type ReleaseRequest struct {
	Property           uuid.UUID
	BookingID          uuid.UUID
	BookingComponentID uuid.UUID
	ComponentType      string
	AllocationID       *uuid.UUID
	AllocationRef      string
	Details            map[string]any
	Reason             string
}

// ComponentAllocator allocates and releases one component type.
type ComponentAllocator struct {
	Allocate func(ctx context.Context, tx pgx.Tx, r AllocationRequest) (Allocation, error)
	Release  func(ctx context.Context, tx pgx.Tx, r ReleaseRequest) error
}

var (
	allocMu    sync.RWMutex
	allocators = map[string]ComponentAllocator{}
)

// RegisterComponentAllocator registers the allocator of a component type
// ("reservation", "tee_time", "voucher", …); wired by internal/app.
func RegisterComponentAllocator(componentType string, a ComponentAllocator) {
	allocMu.Lock()
	defer allocMu.Unlock()
	allocators[componentType] = a
}

// allocatorFor returns the allocator of a component: its own type, else the
// Reservation Engine for components with a resource (banquet venue).
func allocatorFor(componentType string, hasResource bool) (ComponentAllocator, bool) {
	allocMu.RLock()
	defer allocMu.RUnlock()
	if a, ok := allocators[componentType]; ok {
		return a, true
	}
	if hasResource {
		a, ok := allocators["reservation"]
		return a, ok
	}
	return ComponentAllocator{}, false
}

// needsAllocator tells whether a component type must be allocated.
func needsAllocator(componentType string, hasResource bool) bool {
	switch componentType {
	case "reservation", "tee_time", "voucher":
		return true
	case "banquet":
		return hasResource
	}
	return false
}
