package commercial

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/org"
)

// ResolveRequest is the Price Preview / resolution request (FR-PRC-10). Golf
// tee times use date, time and segments; every other line (PRD P2 EP-02,
// the generic resolve) sets serviceType and the P2 fields.
type ResolveRequest struct {
	Date           route.Date `json:"date,omitempty" doc:"Golf: local play date YYYY-MM-DD"`
	Time           string     `json:"time,omitempty" doc:"Golf: local tee time HH:MM"`
	Session        string     `json:"session,omitempty" enum:"morning,afternoon,night"`
	Segments       []string   `json:"segments" doc:"Eligible player segments; the cheapest resolving one wins"`
	ChargeType     string     `json:"chargeType,omitempty" enum:"golf_round,caddy_fee,cart_fee,extra_cart"`
	PlayingRouteID *uuid.UUID `json:"playingRouteId,omitempty"`
	Channel        string     `json:"channel,omitempty" enum:"member_app,website,back_office,walk_in,import"`
	Peak           *bool      `json:"peak,omitempty"`
	Quantity       int        `json:"quantity,omitempty"`
	// P2: any other line
	ServiceType string     `json:"serviceType,omitempty" enum:"golf,sport_court,facility_entry,class_session,class_registration,class_package,bungalow,vip_suite,meeting_room,meeting_package,equipment,driving_range,voucher_sale,membership,pos,locker,other"`
	ItemRef     string     `json:"itemRef,omitempty"`
	Segment     string     `json:"segment,omitempty" doc:"Other lines: the customer segment (default any)"`
	Start       *time.Time `json:"start,omitempty" doc:"Other lines: service start"`
	End         *time.Time `json:"end,omitempty" doc:"Other lines: service end (time-based units)"`
	RatePlan    string     `json:"ratePlan,omitempty"`
	Package     string     `json:"package,omitempty"`
	Persist     bool       `json:"persist,omitempty" doc:"Other lines: store an immutable pricing snapshot"`
}

// Quote is the resolved price: golf fills the P1 fields, other lines also
// return their detail in line.
type Quote struct {
	PriceResult
	Line *LinePrice `json:"line,omitempty" doc:"Other lines (PRD P2 EP-02): units, overtime, tax breakdown, snapshot"`
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
	if req.ServiceType != "" && req.ServiceType != "golf" {
		m.resolveLine(w, r, req)
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
	var out Quote
	err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		loc, err := org.Location(ctx, tx, pid)
		if err != nil {
			return err
		}
		hh, _ := time.Parse("15:04", req.Time)
		at := time.Date(day.Year(), day.Month(), day.Day(), hh.Hour(), hh.Minute(), 0, 0, loc).UTC()
		out.PriceResult, err = Resolve(ctx, tx, PriceQuery{Property: pid, ChargeType: req.ChargeType, Segments: req.Segments, PlayAt: at, PlayDate: day,
			LocalTime: req.Time, Session: req.Session, PlayingRouteID: req.PlayingRouteID, Channel: req.Channel, Peak: req.Peak, Quantity: req.Quantity})
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
		Request: ResolveRequest{}, Response: Quote{}, Status: http.StatusOK,
		NoAudit: "read-only calculation; a persisted snapshot of another line is audited", Handler: m.resolve})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/pricing-snapshots/{id}", Module: "commercial", Tag: "Pricing",
		Summary: "View an immutable pricing snapshot", Permission: "commercial.pricing.view", Scope: route.ScopeProperty,
		Response: SnapshotView{}, Handler: m.snapshot})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/public/rates/{line}", Module: "commercial", Tag: "Public",
		Summary: "Structured public rate table per line (sportclub, stay, meeting, range, vouchers)", Auth: route.AuthPublic,
		Response: PublicRate{}, List: true, Query: []route.Param{{Name: "propertyId", Required: true}}, Handler: m.publicRates})
}

// resolveLine prices a service of another line (PRD P2 EP-02); with persist
// the snapshot is stored and audited.
func (m *Module) resolveLine(w http.ResponseWriter, r *http.Request, req ResolveRequest) {
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	pr := PriceRequest{ServiceType: req.ServiceType, ItemRef: req.ItemRef, Segment: req.Segment, End: req.End, Quantity: req.Quantity,
		RatePlan: req.RatePlan, Package: req.Package, Channel: req.Channel, Persist: req.Persist}
	if req.Start != nil {
		pr.Start = *req.Start
	}
	var out Quote
	fn := func(tx pgx.Tx) error {
		var res LinePrice
		var err error
		if req.Persist {
			if res, err = (Pricer{}).Price(ctx, tx, pid, pr); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionCreate, EntityType: "commercial.pricing_snapshot",
				EntityID: res.SnapshotID.String(), EntityLabel: res.RuleCode, PropertyID: &pid, After: res}); err != nil {
				return err
			}
		} else if res, err = (Pricer{}).Resolve(ctx, tx, pid, pr); err != nil {
			return err
		}
		out = Quote{PriceResult: PriceResult{RuleID: res.RuleID, RuleCode: res.RuleCode, RuleVersion: res.RuleVersion, RuleName: res.RuleName,
			ChargeType: "other", Segment: res.Segment, PricingMode: res.Tax.PricingMode, Currency: res.Tax.Currency, UnitPrice: res.UnitPrice,
			NetAmount: res.Tax.NetAmount, TaxAmount: res.TaxAmount().String(), ServiceAmt: res.ServiceAmount().String(), Total: res.Tax.Total,
			TaxService: res.Tax.Lines, Components: []Component{}, Candidates: []SegmentCost{}}, Line: &res}
		if res.DayType != nil {
			out.DayTypeCode = *res.DayType
		}
		if res.TimeBand != nil {
			out.TimeBandCode = *res.TimeBand
		}
		return nil
	}
	var err error
	if req.Persist {
		err = m.DB.WithTx(ctx, fn)
	} else {
		err = m.DB.WithReadTx(ctx, fn)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// publicRates is the structured rate table of a line for the website
// (FR-WEB-P2-05); golf rates are P1's /api/v1/public/golf/rates.
func (m *Module) publicRates(w http.ResponseWriter, r *http.Request) {
	types, ok := LineServiceTypes[chi.URLParam(r, "line")]
	if !ok {
		httpx.WriteError(w, r, errs.NotFound("rate table"))
		return
	}
	pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
	if err != nil {
		httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
		return
	}
	ctx := dbtx.WithScope(r.Context(), dbtx.Scope{PropertyIDs: []uuid.UUID{pid}})
	var out []PublicRate
	if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		out, err = PublicRates(ctx, tx, pid, types)
		return err
	}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[PublicRate]{Items: out})
}
