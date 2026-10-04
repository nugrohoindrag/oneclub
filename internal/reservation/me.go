package reservation

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// registerMe adds "Bookings" of the Member App: my bookings of every line.
func (e *Engine) registerMe(reg *route.Registry) {
	crm.MeRoute(reg, "reservation", "Member Portal", route.Route{Method: http.MethodGet, Path: "/api/v1/member/reservations", Summary: "My Bookings (all business lines)",
		Response: Bookings{}, Handler: handle.Read(e.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Bookings, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return Bookings{}, err
			}
			v, err := e.CustomerSection(ctx, tx, p.PropertyID, p.ID)
			if err != nil {
				return Bookings{}, err
			}
			return v.(Bookings), nil
		})})
}
