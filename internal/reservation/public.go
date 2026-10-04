package reservation

// Website availability (PRD P2 EP-26 "Select Date / Time → Select
// Availability") and automatic confirmation of held bookings once their
// folio is paid online.

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/outbox"
)

// PublicTypes are the resource types bookable on the website.
var PublicTypes = []string{"sport_court", "class_session", "bungalow", "vip_suite", "meeting_room", "driving_range_bay"}

func (e *Engine) registerPublic(reg *route.Registry) {
	crm.PublicRoute(reg, "reservation", route.Route{Method: http.MethodGet, Path: "/api/v1/public/availability",
		Summary: "Availability with non-member indicative prices (website)", Response: Availability{},
		Query: []route.Param{{Name: "propertyId", Required: true}, {Name: "resourceType", Required: true}, {Name: "date"}, {Name: "days", Type: "integer"},
			{Name: "resourceId"}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
			if err != nil {
				httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
				return
			}
			rt := r.URL.Query().Get("resourceType")
			if !slices.Contains(PublicTypes, rt) {
				httpx.WriteError(w, r, errs.BadRequest("invalid_resource_type", "resource type is not bookable online"))
				return
			}
			ctx := crm.PublicCtx(r.Context(), pid)
			var out Availability
			err = e.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
				d, err := handle.QueryDate(r, "date", time.Now())
				if err != nil {
					return err
				}
				rid, err := handle.QueryUUID(r, "resourceId")
				if err != nil {
					return err
				}
				out, err = e.Availability(ctx, tx, pid, AvailabilityQuery{ResourceType: rt, Date: d, Days: min(max(handle.QueryInt(r, "days", 1), 1), 14),
					ResourceID: rid, Segment: "non_member", WithPrice: true})
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		}})
}

// ConfirmPaid confirms held / pending bookings of a folio after a payment
// (billing.payment_settled subscriber); the deposit policy still applies.
func (e *Engine) ConfirmPaid(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p struct {
		FolioID uuid.UUID `json:"folioId"`
	}
	if err := ev.Decode(&p); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM reservation.reservations WHERE folio_id = $1 AND status IN ('draft', 'pending')`, p.FolioID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, id := range ids {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := e.Confirm(ctx, sp, id, false); err != nil {
			_ = sp.Rollback(ctx)
			if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
				slog.InfoContext(ctx, "paid booking stays unconfirmed", "reservation", id, "reason", de.Message)
				continue // e.g. deposit not yet complete: stays pending
			}
			return err
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
