package experience

// Caddy tablet as the golf cart's GPS (demo feedback 9 Oct 2026, OneClub
// replaces Smartscore on MGCC's 120 carts): the tablet stays with the
// caddy and rides in the cart's bracket. While a round is played it sends
// its position; the server puts it on the cart of the caddy's players (the
// caddy and cart assignments of the flight), so no device is registered
// per cart. The Course Monitor shows the real positions.

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// TabletFixMaxAccuracy drops rough fixes (meters).
const TabletFixMaxAccuracy = 150

// TabletFix is the caddy tablet's position during a round.
type TabletFix struct {
	Lat      float64    `json:"lat"`
	Lng      float64    `json:"lng"`
	Accuracy float64    `json:"accuracy,omitempty" doc:"Meters; rougher than 150 m is ignored"`
	Battery  *int       `json:"batteryPercent,omitempty" doc:"Of the tablet"`
	At       *time.Time `json:"at,omitempty"`
}

// TabletFixResult names the cart that got the position.
type TabletFixResult struct {
	GolfCartID *uuid.UUID `json:"golfCartId" doc:"Empty: no golf cart (walking) or the fix was too rough"`
	Code       *string    `json:"code"`
}

// TabletPosition stores the tablet's fix as the position of its cart.
func (m *Module) TabletPosition(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in TabletFix) (TabletFixResult, error) {
	r, err := m.GetRound(ctx, tx, fid)
	if err != nil {
		return TabletFixResult{}, err
	}
	if err := m.canSeeFlight(ctx, tx, r); err != nil {
		return TabletFixResult{}, err
	}
	if (in.Lat == 0 && in.Lng == 0) || in.Lat < -90 || in.Lat > 90 || in.Lng < -180 || in.Lng > 180 {
		return TabletFixResult{}, handle.Invalid("lat", "invalid", "a GPS position")
	}
	var out TabletFixResult
	if in.Accuracy > TabletFixMaxAccuracy {
		return out, nil
	}
	var me *uuid.UUID
	if c, err := m.myCaddy(ctx, tx, r.PropertyID); err == nil {
		me = &c.ID
	}
	// the cart of the caddy's players, else the flight's first cart
	var cart uuid.UUID
	var code string
	err = tx.QueryRow(ctx, `SELECT g.id, g.code FROM golf.golf_cart_assignments a JOIN golf.golf_carts g ON g.id = a.golf_cart_id
		WHERE a.flight_id = $1 AND a.status IN ('assigned', 'in_use')
		ORDER BY (EXISTS (SELECT 1 FROM golf.caddy_assignments c WHERE c.flight_id = a.flight_id AND c.caddy_id = $2 AND c.status <> 'cancelled'
			AND c.player_ids && a.player_ids)) DESC, g.code LIMIT 1`, fid, me).Scan(&cart, &code)
	if dbtx.IsNoRows(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	at := time.Now().UTC()
	if in.At != nil && in.At.Before(at) && at.Sub(*in.At) < FreshFix {
		at = *in.At
	}
	if _, err := m.IngestPositions(ctx, tx, r.PropertyID, []Position{{GolfCartID: cart, Lat: in.Lat, Lng: in.Lng, Battery: in.Battery, At: at}}); err != nil {
		return out, err
	}
	out.GolfCartID, out.Code = &cart, &code
	return out, nil
}

func (m *Module) registerTabletGPS(add func(tag string, rt route.Route)) {
	add("Caddy Tablet", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/rounds/{id}/position",
		Summary:    "Caddy tablet GPS: the position of the cart the caddy rides (sent every 30 s while playing)",
		Permission: "golf.round.operate", Request: TabletFix{}, Response: TabletFixResult{}, Status: http.StatusOK,
		NoAudit: "high-frequency telemetry; stored as the cart's last position",
		Handler: handle.Write(m.DB, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TabletFix) (TabletFixResult, error) {
			fid, err := handle.ID(r)
			if err != nil {
				return TabletFixResult{}, err
			}
			return m.TabletPosition(ctx, tx, fid, in)
		})})
}
