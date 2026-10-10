package stay

// Routes of the Stay Front Desk, groups and the rate check of the MGCC
// bungalows (docs/requirement-booking-hotel-mgcc.md Bagian B, C).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// KeysInput records the keys handed back before the check-out.
type KeysInput struct {
	KeysReturned int `json:"keysReturned"`
}

// GroupBookingInput books a group from the back office or the front desk:
// several bungalows with a rooming list, one booker, a combined or
// per-bungalow folio (FR-H64), a corporate account (FR-H67).
type GroupBookingInput struct {
	CartInput
	Name                 string        `json:"name,omitempty" doc:"Group name (default: the booker)"`
	CustomerID           *uuid.UUID    `json:"customerId,omitempty"`
	Guest                *GuestInput   `json:"guest,omitempty"`
	CorporateAccountID   *uuid.UUID    `json:"corporateAccountId,omitempty"`
	FolioMode            string        `json:"folioMode,omitempty" enum:"combined,per_stay"`
	BookingSource        string        `json:"bookingSource,omitempty" enum:"front_desk,phone,walk_in,corporate,website,member_app"`
	ExpectedArrival      string        `json:"expectedArrival,omitempty"`
	SpecialRequests      string        `json:"specialRequests,omitempty"`
	Notes                string        `json:"notes,omitempty"`
	VIP                  bool          `json:"vip,omitempty"`
	OverrideRestrictions bool          `json:"overrideRestrictions,omitempty"`
	SupervisorReason     string        `json:"supervisorReason,omitempty"`
	Payment              *PaymentInput `json:"payment,omitempty" doc:"Deposit taken at the desk for every bungalow"`
}

// GroupDetail is a group with its bungalows.
type GroupDetail struct {
	Group Group  `json:"group"`
	Stays []Stay `json:"stays"`
}

// GroupSettleInput pays every bungalow of a group in one go.
type GroupSettleInput struct {
	MethodType string `json:"methodType" doc:"cash, card, bank_transfer, qris, member_account (city ledger: charge-to-account)"`
	Reference  string `json:"reference,omitempty"`
	Purpose    string `json:"purpose,omitempty" enum:"deposit,settlement"`
}

// GroupResult reports an action on every bungalow of a group.
type GroupResult struct {
	Done   []string `json:"done"`
	Failed []string `json:"failed" doc:"Bungalow: reason"`
	Group  Group    `json:"group"`
}

func (m *Module) registerFrontDesk(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "stay", tagAccommodation, route.ScopeProperty
		reg.Add(rt)
	}
	sid := func(r *http.Request) (uuid.UUID, error) { return handle.ID(r) }
	// registration card and identity (FR-H47, FR-H48)
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}/identity-photo", Summary: "Upload the photo of the identity card / passport (multipart: file)",
		Permission: "stay.stay.check_in", Response: Stay{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			id, err := sid(r)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			out, err := m.saveIdentityPhoto(w, r, id)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		}})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/stays/{id}/identity-photo", Summary: "Photo of the identity (authorised staff only)",
		Permission: "stay.identity.view", RawContent: "image/jpeg", Handler: func(w http.ResponseWriter, r *http.Request) { m.streamFile(w, r, "id_photo_file_id") }})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/stays/{id}/signature", Summary: "Signature of the registration card",
		Permission: "stay.stay.view", RawContent: "image/png", Handler: func(w http.ResponseWriter, r *http.Request) { m.streamFile(w, r, "signature_file_id") }})
	pdfRoute := func(path, summary string, fn func(ctx context.Context, tx pgx.Tx, r *http.Request) ([]byte, string, error)) {
		add(route.Route{Method: http.MethodGet, Path: path, Summary: summary, Permission: "stay.stay.view", RawContent: "application/pdf",
			Query: []route.Param{{Name: "lang", Description: "id | en"}, {Name: "date", Description: "YYYY-MM-DD"}},
			Handler: func(w http.ResponseWriter, r *http.Request) {
				ctx := r.Context()
				var body []byte
				var name string
				err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
					var err error
					body, name, err = fn(ctx, tx, r)
					return err
				})
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				w.Header().Set("Content-Type", "application/pdf")
				w.Header().Set("Content-Disposition", `inline; filename="`+name+`"`)
				_, _ = w.Write(body)
			}})
	}
	pdfRoute("/api/v1/stay/stays/{id}/registration-card.pdf", "Registration card (PDF) with the signature", func(ctx context.Context, tx pgx.Tx, r *http.Request) ([]byte, string, error) {
		id, err := sid(r)
		if err != nil {
			return nil, "", err
		}
		return m.registrationCardPDF(ctx, tx, id, r.URL.Query().Get("lang"))
	})
	pdfRoute("/api/v1/stay/stays/{id}/invoice.pdf", "Folio of the stay: invoice / receipt (PDF)", func(ctx context.Context, tx pgx.Tx, r *http.Request) ([]byte, string, error) {
		id, err := sid(r)
		if err != nil {
			return nil, "", err
		}
		return m.invoicePDF(ctx, tx, id)
	})
	pdfRoute("/api/v1/stay/print/{kind}", "Printed list of the day: arrivals, departures, in-house, foreign-guests (PDF)",
		func(ctx context.Context, tx pgx.Tx, r *http.Request) ([]byte, string, error) {
			loc := calendar.Location(ctx, tx)
			d, err := handle.QueryDate(r, "date", localToday(ctx, tx))
			if err != nil {
				return nil, "", err
			}
			return m.listPDF(ctx, tx, handle.Property(ctx), chi.URLParam(r, "kind"), time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc))
		})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:keys", Summary: "Record the keys handed back", Permission: "stay.stay.update",
		Request: KeysInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in KeysInput) (StayResult, error) {
			id, err := sid(r)
			if err != nil {
				return StayResult{}, err
			}
			s, err := m.lock(ctx, tx, id)
			if err != nil {
				return StayResult{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE stay.stays SET keys_returned = $2 WHERE id = $1`, s.ID, max(in.KeysReturned, 0)); err != nil {
				return StayResult{}, err
			}
			out, err := m.result(ctx, tx, s.ID)
			if err != nil {
				return out, err
			}
			return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "keys_returned", EntityType: "stay.stay", EntityID: s.ID.String(),
				EntityLabel: s.StayNo, PropertyID: &s.PropertyID, After: map[string]any{"keysReturned": in.KeysReturned, "keysIssued": s.KeysIssued}})
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:move", Summary: "Room move of an in-house guest (one folio; the old bungalow Dirty)",
		Permission: "stay.stay.update", Request: MoveInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MoveInput) (StayResult, error) {
			id, err := sid(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.MoveRoom(ctx, tx, id, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:upgrade", Summary: "Upgrade before check-in: free (price kept) or paid (repriced)",
		Permission: "stay.stay.update", Request: UpgradeInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in UpgradeInput) (StayResult, error) {
			id, err := sid(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.Upgrade(ctx, tx, id, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:void", Summary: "Void a reservation keyed in by mistake (supervisor, reason, no fee)",
		Permission: "stay.stay.supervise", Request: ReasonInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (StayResult, error) {
			id, err := sid(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.Void(ctx, tx, id, in.Reason)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/stays/{id}/moves", Summary: "Room moves of a stay", Permission: "stay.stay.view",
		Response: RoomMove{}, List: true, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RoomMove], error) {
			id, err := sid(r)
			if err != nil {
				return httpx.Page[RoomMove]{}, err
			}
			return handle.Page(handle.List[RoomMove](tx.Query(ctx, `SELECT mv.id, f.code AS from_code, t.code AS to_code, mv.reason, mv.upgrade,
				trim_scale(mv.charge)::text AS charge, mv.moved_at, u.full_name AS created_by_name FROM stay.room_moves mv
				JOIN stay.bungalows f ON f.id = mv.from_bungalow_id JOIN stay.bungalows t ON t.id = mv.to_bungalow_id
				LEFT JOIN platform.users u ON u.id = mv.created_by WHERE mv.stay_id = $1 ORDER BY mv.moved_at`, id)))
		})})
	// shift handover (FR-H61)
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/handover-notes", Summary: "Shift handover of the front desk (last days, open first)",
		Permission: "stay.stay.view", Response: HandoverNote{}, List: true, Query: []route.Param{{Name: "days", Type: "integer"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[HandoverNote], error) {
			from := localToday(ctx, tx).AddDate(0, 0, -max(handle.QueryInt(r, "days", 3), 1))
			return handle.Page(handle.List[HandoverNote](tx.Query(ctx, handoverSelect+` WHERE h.property_id = $1 AND (h.status = 'open' OR h.business_date >= $2::date)
				ORDER BY h.status DESC, h.created_at DESC LIMIT 100`, handle.Property(ctx), from.Format(time.DateOnly))))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/handover-notes", Summary: "Write a note for the next shift", Permission: "stay.stay.update",
		Request: HandoverInput{}, Response: HandoverNote{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in HandoverInput) (HandoverNote, error) {
			return m.addHandover(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/handover-notes/{id}:done", Summary: "Mark a handover note done", Permission: "stay.stay.update",
		Response: HandoverNote{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (HandoverNote, error) {
			id, err := sid(r)
			if err != nil {
				return HandoverNote{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE stay.handover_notes SET status = 'done', done_at = now(), done_by = $2 WHERE id = $1`, id, actor(ctx)); err != nil {
				return HandoverNote{}, err
			}
			rows, err := tx.Query(ctx, handoverSelect+` WHERE h.id = $1`, id)
			out, err := handle.One[HandoverNote](rows, err, "handover note")
			if err != nil {
				return out, err
			}
			pid := handle.Property(ctx)
			return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "done", EntityType: "stay.handover_note", EntityID: id.String(),
				PropertyID: &pid, After: out})
		})})
	// incidents (FR-H86)
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/incidents", Summary: "Bungalow incidents (damage, complaint, lost item …)",
		Permission: "stay.incident.view", Response: Incident{}, List: true, Query: []route.Param{{Name: "status"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Incident], error) {
			return handle.Page(handle.List[Incident](tx.Query(ctx, incidentSelect+` WHERE i.property_id = $1 AND ($2 = '' OR i.status = $2)
				ORDER BY i.status DESC, i.occurred_at DESC LIMIT 200`, handle.Property(ctx), r.URL.Query().Get("status"))))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/incidents", Summary: "Report a bungalow incident (shown on Management › Incidents)",
		Permission: "stay.incident.create", Request: IncidentInput{}, Response: Incident{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in IncidentInput) (Incident, error) {
			return m.reportIncident(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/incidents/{id}:close", Summary: "Close an incident with the action taken",
		Permission: "stay.incident.manage", Request: CloseIncidentInput{}, Response: Incident{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CloseIncidentInput) (Incident, error) {
			id, err := sid(r)
			if err != nil {
				return Incident{}, err
			}
			if err := handle.Required("actionTaken", strings.TrimSpace(in.ActionTaken)); err != nil {
				return Incident{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE stay.incidents SET status = 'closed', action_taken = $2, closed_at = now(), updated_by = $3 WHERE id = $1`, id,
				strings.TrimSpace(in.ActionTaken), actor(ctx)); err != nil {
				return Incident{}, err
			}
			rows, err := tx.Query(ctx, incidentSelect+` WHERE i.id = $1`, id)
			out, err := handle.One[Incident](rows, err, "incident")
			if err != nil {
				return out, err
			}
			pid := handle.Property(ctx)
			return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "close", EntityType: "stay.incident", EntityID: id.String(),
				EntityLabel: out.Number, PropertyID: &pid, Reason: in.ActionTaken, After: out})
		})})
	// groups (FR-H64)
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/groups", Summary: "Group reservations", Permission: "stay.stay.view", Response: Group{}, List: true,
		Query: []route.Param{{Name: "q"}}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Group], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Group](tx.Query(ctx, groupSelect+` WHERE g.property_id = $1 AND ($2 = '' OR g.group_no ILIKE '%' || $2 || '%'
				OR g.name ILIKE '%' || $2 || '%') ORDER BY g.created_at DESC LIMIT $3`, handle.Property(ctx), lp.Q, lp.Limit)))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/groups/{id}", Summary: "Group reservation with its rooming list", Permission: "stay.stay.view",
		Response: GroupDetail{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (GroupDetail, error) {
			id, err := sid(r)
			if err != nil {
				return GroupDetail{}, err
			}
			return m.groupDetail(ctx, tx, id)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/groups", Summary: "Book a group: several bungalows, rooming list, combined or per-bungalow folio",
		Permission: "stay.stay.create", Request: GroupBookingInput{}, Response: GroupDetail{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in GroupBookingInput) (GroupDetail, error) {
			if in.BookingSource == "" {
				in.BookingSource = "front_desk"
			}
			b := cartBase{Channel: "back_office", Source: in.BookingSource, CustomerID: in.CustomerID, Guest: in.Guest, CorporateAccountID: in.CorporateAccountID,
				ExpectedArrival: in.ExpectedArrival, SpecialRequests: in.SpecialRequests, Notes: in.Notes, VIP: in.VIP, OverrideRestrictions: in.OverrideRestrictions,
				SupervisorReason: in.SupervisorReason, Payment: in.Payment, GroupName: nonEmpty(strings.TrimSpace(in.Name), "Group"), FolioMode: in.FolioMode}
			if strings.TrimSpace(in.Name) == "" {
				b.GroupName = ""
			}
			res, err := m.bookCart(ctx, tx, handle.Property(ctx), in.CartInput, b, "", r.Header.Get("Idempotency-Key"))
			if err != nil {
				return GroupDetail{}, err
			}
			if res.GroupID == nil {
				// one bungalow is still a group when the desk books it as one
				g, err := m.createGroup(ctx, tx, handle.Property(ctx), groupInput{Name: guestLabel(res.Stays[0]), CustomerID: res.Stays[0].CustomerID,
					CorporateAccountID: in.CorporateAccountID, FolioMode: in.FolioMode, Source: in.BookingSource, Channel: "back_office", Notes: in.Notes})
				if err != nil {
					return GroupDetail{}, err
				}
				if _, err := tx.Exec(ctx, `UPDATE stay.stays SET group_id = $2 WHERE id = $1`, res.Stays[0].ID, g.ID); err != nil {
					return GroupDetail{}, err
				}
				res.GroupID = &g.ID
			}
			return m.groupDetail(ctx, tx, *res.GroupID)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays:quote-cart", Summary: "Price summary of a cart / group (nothing is kept)",
		Permission: "stay.stay.create", Request: GroupBookingInput{}, Response: CartQuote{}, NoAudit: "read-only preview, rolled back",
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in GroupBookingInput) (CartQuote, error) {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return CartQuote{}, err
			}
			defer func() { _ = sp.Rollback(ctx) }()
			return m.QuoteCart(ctx, sp, handle.Property(ctx), in.CartInput, cartBase{Channel: "back_office", Source: nonEmpty(in.BookingSource, "front_desk"),
				CustomerID: in.CustomerID, Guest: in.Guest, CorporateAccountID: in.CorporateAccountID, OverrideRestrictions: in.OverrideRestrictions,
				SupervisorReason: in.SupervisorReason})
		})})
	groupAction := func(op, summary, perm string, req any, fn func(ctx context.Context, tx pgx.Tx, s Stay, r *http.Request, body []byte) error) {
		add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/groups/{id}:" + op, Summary: summary, Permission: perm, Request: req,
			Response: GroupResult{}, Status: http.StatusOK,
			Handler: func(w http.ResponseWriter, r *http.Request) {
				id, err := sid(r)
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				body, err := readBody(r)
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				ctx := r.Context()
				out := GroupResult{Done: []string{}, Failed: []string{}}
				var stays []Stay
				if err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
					var err error
					stays, err = handle.List[Stay](tx.Query(ctx, staySelect+` WHERE s.group_id = $1 ORDER BY s.stay_no`, id))
					return err
				}); err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				// every bungalow in its own transaction: one refusal does not undo the others
				for _, s := range stays {
					err := db.WithTx(ctx, func(tx pgx.Tx) error { return fn(ctx, tx, s, r, body) })
					switch {
					case err == nil:
						out.Done = append(out.Done, s.StayNo)
					case err != errSkip:
						msg := err.Error()
						if de, ok := errs.As(err); ok {
							msg = de.Message
						}
						out.Failed = append(out.Failed, s.StayNo+": "+msg)
					}
				}
				// the group action itself is audited, also when every bungalow was skipped or refused
				pid := handle.Property(ctx)
				if err := db.WithTx(ctx, func(tx pgx.Tx) error {
					return audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "group_" + strings.ReplaceAll(op, "-", "_"), EntityType: "stay.stay_group",
						EntityID: id.String(), PropertyID: &pid, After: map[string]any{"done": out.Done, "failed": out.Failed}})
				}); err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				_ = db.WithReadTx(ctx, func(tx pgx.Tx) error {
					var err error
					out.Group, err = m.group(ctx, tx, id)
					return err
				})
				httpx.JSON(w, http.StatusOK, out)
			}})
	}
	groupAction("check-in", "Check in every reserved bungalow of the group (identity of the booker)", "stay.stay.check_in", CheckInInput{},
		func(ctx context.Context, tx pgx.Tx, s Stay, r *http.Request, body []byte) error {
			if s.Status != "reserved" {
				return errSkip
			}
			var in CheckInInput
			if err := decodeInto(body, &in); err != nil {
				return err
			}
			in.UnitID, in.Signature = nil, ""
			_, err := m.CheckIn(ctx, tx, s.ID, in)
			return err
		})
	groupAction("check-out", "Check out every in-house bungalow of the group (each folio settled)", "stay.stay.check_out", CheckOutInput{},
		func(ctx context.Context, tx pgx.Tx, s Stay, r *http.Request, body []byte) error {
			if s.Status != "checked_in" {
				return errSkip
			}
			var in CheckOutInput
			if err := decodeInto(body, &in); err != nil {
				return err
			}
			_, err := m.CheckOut(ctx, tx, s.ID, in)
			return err
		})
	groupAction("settle", "Pay the balance of every bungalow of the group (combined bill)", "billing.payment.create", GroupSettleInput{},
		func(ctx context.Context, tx pgx.Tx, s Stay, r *http.Request, body []byte) error {
			var in GroupSettleInput
			if err := decodeInto(body, &in); err != nil {
				return err
			}
			if s.FolioID == nil || s.Status == "expired" || s.Status == "void" {
				return errSkip
			}
			due := decOf(s.TotalDue).Sub(decOf(s.Paid))
			if !due.IsPositive() {
				return errSkip
			}
			purpose := nonEmpty(in.Purpose, "settlement")
			if s.Status == "reserved" {
				purpose = "deposit"
			}
			_, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: s.FolioID, MethodType: in.MethodType, Channel: "venue", Purpose: purpose,
				Amount: decimal.Max(due, decimal.Zero), Reference: in.Reference, Description: "Group " + deref(s.GroupNo)})
			return err
		})
	// rate check (FR-H70): the prices of a channel exactly as it shows them
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/rate-check", Summary: "Rate check: room types and rate plans priced as the website / Member App shows them",
		Permission: "stay.stay.view", Response: PublicSearch{}, Query: []route.Param{{Name: "checkin", Required: true}, {Name: "checkout", Required: true},
			{Name: "adults", Type: "integer"}, {Name: "children", Type: "integer"}, {Name: "promo"}, {Name: "source", Description: "website | member_app"},
			{Name: "customerId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PublicSearch, error) {
			in, err := searchQuery(ctx, tx, r)
			if err != nil {
				return PublicSearch{}, err
			}
			in.Source = nonEmpty(r.URL.Query().Get("source"), "website")
			if in.CustomerID, err = handle.QueryUUID(r, "customerId"); err != nil {
				return PublicSearch{}, err
			}
			return m.ChannelSearch(ctx, tx, handle.Property(ctx), in)
		})})
	// POS charge to room (FR-H82)
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/in-house", Summary: "In-house guests for Charge to Room (name or bungalow)",
		Permission: "stay.stay.view", Response: InHouseGuest{}, List: true, Query: []route.Param{{Name: "q"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[InHouseGuest], error) {
			return handle.Page(m.InHouseGuests(ctx, tx, handle.Property(ctx), r.URL.Query().Get("q")))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:charge-order", Summary: "Charge a POS order to the room of an in-house guest",
		Permission: "stay.stay.charge_order", Request: ChargeOrderInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ChargeOrderInput) (StayResult, error) {
			id, err := sid(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.ChargeOrder(ctx, tx, id, in)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/housekeeping/on-duty", Summary: "Room attendants on the roster of a day (fewest open tasks first)",
		Permission: "stay.housekeeping.view", Response: DutyAttendant{}, List: true, Query: []route.Param{{Name: "date"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[DutyAttendant], error) {
			d, err := handle.QueryDate(r, "date", localToday(ctx, tx))
			if err != nil {
				return httpx.Page[DutyAttendant]{}, err
			}
			return handle.Page(m.onDutyHK(ctx, tx, handle.Property(ctx), d.Format(time.DateOnly)))
		})})
}

var errSkip = errs.NotFound("skip")

func (m *Module) groupDetail(ctx context.Context, q pgx.Tx, gid uuid.UUID) (GroupDetail, error) {
	g, err := m.group(ctx, q, gid)
	if err != nil {
		return GroupDetail{}, err
	}
	stays, err := handle.List[Stay](q.Query(ctx, staySelect+` WHERE s.group_id = $1 ORDER BY s.stay_no`, gid))
	return GroupDetail{Group: g, Stays: stays}, err
}

// readBody reads the JSON body of a request (empty = {}).
func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return nil, errs.BadRequest("body_unreadable", "request body could not be read")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	return body, nil
}

// decodeInto decodes a JSON body like the API does (unknown fields refused).
func decodeInto(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errs.BadRequest("invalid_json", "invalid request body: "+err.Error())
	}
	return nil
}
