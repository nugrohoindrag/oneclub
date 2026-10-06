package commercial

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/org"
)

// ResolveRequest is the Price Preview / resolution request (FR-PRC-10).
type ResolveRequest struct {
	Date           route.Date `json:"date" doc:"Local play date YYYY-MM-DD"`
	Time           string     `json:"time" doc:"Local tee time HH:MM"`
	Session        string     `json:"session,omitempty" enum:"morning,afternoon,night"`
	Segments       []string   `json:"segments" doc:"Eligible player segments; the cheapest resolving one wins"`
	ChargeType     string     `json:"chargeType,omitempty" enum:"golf_round,caddy_fee,cart_fee,extra_cart"`
	PlayingRouteID *uuid.UUID `json:"playingRouteId,omitempty"`
	Channel        string     `json:"channel,omitempty" enum:"member_app,website,back_office,walk_in,import"`
	Peak           *bool      `json:"peak,omitempty"`
	Quantity       int        `json:"quantity,omitempty"`
	// PRD P3 (additive)
	CorporateAccountID *uuid.UUID `json:"corporateAccountId,omitempty" doc:"Corporate Rate (contract rate) of a corporate account"`
	CustomerID         *uuid.UUID `json:"customerId,omitempty" doc:"Customer for promotion eligibility"`
	PromoCodes         []string   `json:"promoCodes,omitempty"`
}

func sessionOf(hhmm string) string {
	switch {
	case hhmm >= "16:00":
		return "night"
	case hhmm >= "11:00":
		return "afternoon"
	default:
		return "morning"
	}
}

func (m *Module) resolve(w http.ResponseWriter, r *http.Request) {
	var req ResolveRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	day, err := time.Parse("2006-01-02", string(req.Date))
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("invalid_date", "invalid date", errs.Field("date", "invalid", "YYYY-MM-DD")))
		return
	}
	if !timeRe.MatchString(req.Time) {
		httpx.WriteError(w, r, errs.Validation("invalid_time", "invalid time", errs.Field("time", "invalid", "HH:MM")))
		return
	}
	for _, s := range req.Segments {
		ok := false
		for _, x := range Segments {
			ok = ok || x == s
		}
		if !ok {
			httpx.WriteError(w, r, errs.Validation("invalid_segment", "unknown segment "+s, errs.Field("segments", "invalid", "unknown segment")))
			return
		}
	}
	if req.Session == "" {
		req.Session = sessionOf(req.Time)
	}
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	var out PriceResult
	err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		loc, err := org.Location(ctx, tx, pid)
		if err != nil {
			return err
		}
		hh, _ := time.Parse("15:04", req.Time)
		at := time.Date(day.Year(), day.Month(), day.Day(), hh.Hour(), hh.Minute(), 0, 0, loc).UTC()
		out, err = Resolve(ctx, tx, PriceQuery{Property: pid, ChargeType: req.ChargeType, Segments: req.Segments, PlayAt: at, PlayDate: day,
			LocalTime: req.Time, Session: req.Session, PlayingRouteID: req.PlayingRouteID, Channel: req.Channel, Peak: req.Peak, Quantity: req.Quantity,
			CorporateAccountID: req.CorporateAccountID, CustomerID: req.CustomerID, PromoCodes: req.PromoCodes})
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (m *Module) snapshot(w http.ResponseWriter, r *http.Request) {
	sid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out SnapshotView
	err = m.DB.WithReadTx(r.Context(), func(tx pgx.Tx) error {
		out, err = GetSnapshot(r.Context(), tx, sid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (m *Module) registerPricing(reg *route.Registry) {
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/pricing:resolve", Module: "commercial", Tag: "Pricing",
		Summary: "Resolve a price (Price Preview) without saving", Permission: "commercial.pricing.view", Scope: route.ScopeProperty,
		Request: ResolveRequest{}, Response: PriceResult{}, Status: http.StatusOK, NoAudit: "read-only calculation", Handler: m.resolve})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/pricing-snapshots/{id}", Module: "commercial", Tag: "Pricing",
		Summary: "View an immutable pricing snapshot", Permission: "commercial.pricing.view", Scope: route.ScopeProperty,
		Response: SnapshotView{}, Handler: m.snapshot})
}
