package stay

// Night audit steps of Stay & Venue (PRD P3 FR-EOD-03, §9.5), plugged into
// the billing night audit by internal/app: the room charge of in-house
// bungalow stays booked under Stay Policies "nightly" is posted for the
// night of the business date (one folio line per night, once — the nights
// left are posted at check-out), and stays that did not arrive by the
// business date are marked No-show when Stay Policies say so (package stays
// follow their package and are left alone).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
)

// roomQuote is the priced room charge of a stay posted per night.
type roomQuote struct {
	LineID           uuid.UUID  `json:"lineId"`
	Net              string     `json:"net"`
	Service          string     `json:"service"`
	Tax              string     `json:"tax"`
	Total            string     `json:"total"`
	Nights           int        `json:"nights"`
	SnapshotID       *uuid.UUID `json:"snapshotId"`
	RevenueComponent string     `json:"revenueComponent"`
	Description      string     `json:"description"`
}

// nightsBetween counts the local nights of a stay (at least one).
func nightsBetween(start, end time.Time, loc *time.Location) int {
	a, b := start.In(loc), end.In(loc)
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	db := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return max(int(db.Sub(da).Hours()/24), 1)
}

// nightShare is the part of an amount posted for night i of n: equal
// shares, the last night takes the rounding.
func nightShare(amount string, i, n int) decimal.Decimal {
	a, _ := decimal.NewFromString(amount)
	share := a.Div(decimal.NewFromInt(int64(n))).Round(2)
	if i < n-1 {
		return share
	}
	return a.Sub(share.Mul(decimal.NewFromInt(int64(n - 1))))
}

// postNights posts the unposted nights of a nightly stay before the local
// date of upTo (the night audit: the business date + 1; the check-out: its
// date, at least the first night). It returns the nights posted.
func (m *Module) postNights(ctx context.Context, tx pgx.Tx, s Stay, upTo time.Time, source string) (int, error) {
	if s.RoomPosting != "nightly" || s.FolioID == nil {
		return 0, nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT room_quote FROM stay.stays WHERE id = $1`, s.ID).Scan(&raw); err != nil || raw == nil {
		return 0, err
	}
	var q roomQuote
	if err := json.Unmarshal(raw, &q); err != nil {
		return 0, err
	}
	n := max(q.Nights, 1)
	loc := calendar.Location(ctx, tx)
	a := s.Start.In(loc)
	arrival := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, loc)
	u := upTo.In(loc)
	cut := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, loc)
	if source == "check_out" && !cut.After(arrival) {
		cut = arrival.AddDate(0, 0, 1)
	}
	bd, err := billing.CurrentBusinessDate(ctx, tx, s.PropertyID)
	if err != nil {
		return 0, err
	}
	posted := 0
	for i := 0; i < n; i++ {
		night := arrival.AddDate(0, 0, i)
		if !night.Before(cut) {
			break
		}
		day := night.Format("2006-01-02")
		var done bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stay.night_postings WHERE stay_id = $1 AND night = $2::date)`, s.ID, day).
			Scan(&done); err != nil {
			return posted, err
		}
		if done {
			continue
		}
		net, svc, tax := nightShare(q.Net, i, n), nightShare(q.Service, i, n), nightShare(q.Tax, i, n)
		total := net.Add(svc).Add(tax)
		lineID := q.LineID
		lid, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *s.FolioID, ReferenceType: "reservation.line",
			ReferenceID: &lineID, Description: q.Description + " · night " + night.Format("02 Jan 2006"), Quantity: decimal.NewFromInt(1), Net: net,
			Service: svc, Tax: tax, SnapshotID: q.SnapshotID}, BusinessLine: billing.LineStay, RevenueComponent: q.RevenueComponent})
		if err != nil {
			return posted, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO stay.night_postings (id, property_id, stay_id, night, business_date, folio_line_id, net, service, tax, total,
			source, created_by) VALUES ($1,$2,$3,$4::date,$5::date,$6,$7::numeric,$8::numeric,$9::numeric,$10::numeric,$11,$12)`, id.New(), s.PropertyID, s.ID,
			day, bd.Format("2006-01-02"), lid, net.String(), svc.String(), tax.String(), total.String(), source, actor(ctx)); err != nil {
			return posted, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "post_night", EntityType: "stay.stay", EntityID: s.ID.String(),
			EntityLabel: s.StayNo + " · " + s.UnitName, PropertyID: &s.PropertyID, ActorName: "system:" + source,
			After: map[string]any{"night": day, "total": total.String(), "folioLineId": lid, "source": source}}); err != nil {
			return posted, err
		}
		posted++
	}
	return posted, nil
}

// NightAudit is the Stay & Venue step of the night audit of a business day
// (billing.NightAuditAction): nightly room charges of in-house stays, then
// the no-shows of the day (Stay Policies).
func (m *Module) NightAudit(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time) ([]billing.AuditFinding, error) {
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	loc := calendar.Location(ctx, tx)
	ds := day.Format("2006-01-02")
	next := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, loc)
	list := func(sql string) ([]uuid.UUID, error) {
		rows, err := tx.Query(ctx, sql, property)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	}
	inHouse, err := list(`SELECT id FROM stay.stays WHERE property_id = $1 AND status = 'checked_in' AND room_posting = 'nightly' ORDER BY stay_no`)
	if err != nil {
		return nil, err
	}
	postings := billing.AuditFinding{Check: "stay_room_postings", Severity: "info", Message: "Bungalow room charges posted for the night (Stay Policies)",
		Items: []string{}}
	for _, sid := range inHouse {
		s, err := m.lock(ctx, tx, sid)
		if err != nil {
			return nil, err
		}
		n, err := m.postNights(ctx, tx, s, next, "night_audit")
		if err != nil {
			return nil, err
		}
		if n > 0 {
			postings.Count += n
			postings.Items = append(postings.Items, fmt.Sprintf("%s · %s · %d night(s)", s.StayNo, s.UnitName, n))
		}
	}
	noShows := billing.AuditFinding{Check: "stay_no_shows", Severity: "info", Message: "Stays that did not arrive marked No-show (Stay Policies)",
		Items: []string{}}
	if pol.AutoNoShow {
		due, err := list(`SELECT id FROM stay.stays WHERE property_id = $1 AND status = 'reserved' AND package_booking_id IS NULL ORDER BY stay_no`)
		if err != nil {
			return nil, err
		}
		for _, sid := range due {
			s, err := m.Get(ctx, tx, sid)
			if err != nil {
				return nil, err
			}
			missed := s.Start.In(loc).Format("2006-01-02") <= ds // a bungalow guest who did not arrive on the day
			if s.Kind != "bungalow" {
				missed = !s.End.After(next) // a VIP suite / meeting room block over by the end of the day
			}
			if !missed {
				continue
			}
			if _, err := m.NoShow(ctx, tx, sid, "night audit "+ds); err != nil {
				if errs.Is(err, errs.KindConflict) {
					continue // the reservation moved on (checked in / cancelled elsewhere)
				}
				return nil, err
			}
			noShows.Count++
			noShows.Items = append(noShows.Items, s.StayNo+" · "+s.UnitName)
		}
	}
	return []billing.AuditFinding{postings, noShows}, nil
}
