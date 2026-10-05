package banquet

// Resource bundling of banquet packages (PRD P3 FR-BQT-06): a package lists
// its inclusions — a bungalow suite night, the family room, golf carts, F&B
// vouchers (P2 EP-19), a food tasting … When the package prices an event,
// the inclusions are copied to the event: resources are held on the
// Reservation Engine with the event (pending while tentative, confirmed with
// it), vouchers are issued once the event is Definite and services are
// listed on the BEO.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/reservation"
)

// Inclusion is one item bundled with a banquet package.
type Inclusion struct {
	Kind            string `json:"kind" enum:"resource,voucher,service"`
	Label           string `json:"label"`
	ResourceType    string `json:"resourceType,omitempty" doc:"resource: Reservation Engine resource type (bungalow, meeting_room, vip_suite …)"`
	Quantity        int    `json:"quantity,omitempty" doc:"Default 1"`
	Nights          int    `json:"nights,omitempty" doc:"resource: nights from the event day (bungalow)"`
	Hours           int    `json:"hours,omitempty" doc:"resource: hours from the event start (default: the event period)"`
	VoucherTypeCode string `json:"voucherTypeCode,omitempty" doc:"voucher: Commercial voucher type code"`
}

// parseInclusions validates the inclusions of a package (JSON list).
func parseInclusions(raw any) ([]Inclusion, error) {
	var b []byte
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case []byte:
		b = v
	case string:
		b = []byte(v)
	case json.RawMessage:
		b = v
	default:
		var err error
		if b, err = json.Marshal(v); err != nil {
			return nil, err
		}
	}
	if len(b) == 0 || string(b) == "null" {
		return nil, nil
	}
	var list []Inclusion
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("inclusions must be a list of {kind, label, resourceType, quantity, nights, hours, voucherTypeCode}")
	}
	for i := range list {
		in := &list[i]
		in.Label = strings.TrimSpace(in.Label)
		if in.Quantity <= 0 {
			in.Quantity = 1
		}
		if in.Nights < 0 || in.Hours < 0 {
			return nil, fmt.Errorf("inclusion %d: nights and hours cannot be negative", i+1)
		}
		switch in.Kind {
		case "resource":
			if strings.TrimSpace(in.ResourceType) == "" {
				return nil, fmt.Errorf("inclusion %d: resourceType is required for a resource", i+1)
			}
			if in.ResourceType == "banquet_venue" {
				return nil, fmt.Errorf("inclusion %d: venues are held through the venue holds", i+1)
			}
		case "voucher":
			if strings.TrimSpace(in.VoucherTypeCode) == "" {
				return nil, fmt.Errorf("inclusion %d: voucherTypeCode is required for a voucher", i+1)
			}
		case "service":
		default:
			return nil, fmt.Errorf("inclusion %d: kind must be resource, voucher or service", i+1)
		}
		if in.Label == "" {
			in.Label = strings.ReplaceAll(in.ResourceType+in.VoucherTypeCode, "_", " ")
			if in.Label == "" {
				return nil, fmt.Errorf("inclusion %d: label is required", i+1)
			}
		}
	}
	return list, nil
}

// EventInclusion is a package inclusion of an event.
type EventInclusion struct {
	ID              uuid.UUID `json:"id" db:"id"`
	PackageID       uuid.UUID `json:"packageId" db:"package_id"`
	Seq             int       `json:"seq" db:"seq"`
	Kind            string    `json:"kind" db:"kind" enum:"resource,voucher,service"`
	Label           string    `json:"label" db:"label"`
	ResourceType    *string   `json:"resourceType" db:"resource_type"`
	Quantity        int       `json:"quantity" db:"quantity"`
	Nights          int       `json:"nights" db:"nights"`
	Hours           int       `json:"hours" db:"hours"`
	VoucherTypeCode *string   `json:"voucherTypeCode" db:"voucher_type_code"`
	Status          string    `json:"status" db:"status" enum:"pending,held,issued,unavailable,cancelled"`
	VoucherCodes    []string  `json:"voucherCodes" db:"voucher_codes"`
	Note            *string   `json:"note" db:"note"`
}

func (m *Module) inclusions(ctx context.Context, q dbtx.Querier, eid uuid.UUID) ([]EventInclusion, error) {
	return handle.List[EventInclusion](q.Query(ctx, `SELECT id, package_id, seq, kind, label, resource_type, quantity, nights, hours, voucher_type_code,
		status, voucher_codes, note FROM banquet.event_inclusions WHERE event_id = $1 ORDER BY status = 'cancelled', seq`, eid))
}

// inclusionPeriod is the period a resource inclusion is held for: nights
// from the event day (check-in / check-out times of the resource type),
// hours from the event start, or the event period.
func inclusionPeriod(ctx context.Context, q dbtx.Querier, e BanquetEvent, in Inclusion, rt reservation.ResourceType) (time.Time, time.Time) {
	loc := calendar.Location(ctx, q)
	if in.Nights > 0 {
		day := localDay(e.Start, loc)
		at := func(d time.Time, hhmm *string, def int) time.Time {
			h, mi := def, 0
			if hhmm != nil {
				if t, err := time.Parse("15:04", *hhmm); err == nil {
					h, mi = t.Hour(), t.Minute()
				}
			}
			return time.Date(d.Year(), d.Month(), d.Day(), h, mi, 0, 0, loc)
		}
		return at(day, rt.CheckInTime, 14), at(day.AddDate(0, 0, in.Nights), rt.CheckOutTime, 12)
	}
	if in.Hours > 0 {
		return e.Start, e.Start.Add(time.Duration(in.Hours) * time.Hour)
	}
	return e.Start, e.End
}

// holdInclusion books free resources of the inclusion's type (all units or
// none) and records them as event resources without a charge.
func (m *Module) holdInclusion(ctx context.Context, tx pgx.Tx, e BanquetEvent, incID uuid.UUID, in Inclusion) (bool, error) {
	rt, err := m.Res.Type(ctx, tx, in.ResourceType)
	if err != nil {
		if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
			return false, nil // unknown type: the inclusion stays unavailable
		}
		return false, err
	}
	start, end := inclusionPeriod(ctx, tx, e, in, rt)
	rows, err := tx.Query(ctx, `SELECT id FROM reservation.resources WHERE property_id = $1 AND resource_type = $2 AND status = 'active'
		AND archived_at IS NULL ORDER BY code`, e.PropertyID, in.ResourceType)
	if err != nil {
		return false, err
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return false, err
	}
	units, lineQty := in.Quantity, 1
	if rt.AllocationMode != "exclusive" {
		units, lineQty = 1, in.Quantity // pooled resources: one line with the quantity
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return false, err
	}
	booked := 0
	for _, rid := range candidates {
		if booked == units {
			break
		}
		xid := id.New()
		usp, err := sp.Begin(ctx)
		if err != nil {
			_ = sp.Rollback(ctx)
			return false, err
		}
		r, err := m.Res.Book(ctx, usp, e.PropertyID, reservation.BookRequest{Kind: "block", BusinessLine: "banquet", Channel: e.channel(),
			CustomerID: e.CustomerID, GuestName: deref(e.ContactName), CorporateName: deref(e.CorporateName), Confirm: e.Status == StatusDefinite,
			SourceType: "banquet.event_resource", SourceID: &xid, Notes: e.Number + " · " + in.Label,
			Lines:      []reservation.LineRequest{{ResourceID: rid, Start: start, End: end, Quantity: lineQty, Description: in.Label}},
			Attributes: map[string]any{"eventId": e.ID.String(), "eventNumber": e.Number, "inclusion": in.Label}})
		if err != nil {
			_ = usp.Rollback(ctx)
			if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
				continue // taken or not bookable: try the next resource
			}
			_ = sp.Rollback(ctx)
			return false, err
		}
		if err := usp.Commit(ctx); err != nil {
			_ = sp.Rollback(ctx)
			return false, err
		}
		status := "held"
		if e.Status == StatusDefinite {
			status = "confirmed"
		}
		if _, err := sp.Exec(ctx, `INSERT INTO banquet.event_resources (id, property_id, event_id, resource_id, reservation_id, inclusion_id, description,
			quantity, start_at, end_at, status, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)`, xid, e.PropertyID, e.ID, rid,
			r.ID, incID, in.Label, lineQty, start, end, status, actor(ctx)); err != nil {
			_ = sp.Rollback(ctx)
			return false, err
		}
		booked++
	}
	if booked < units {
		return false, sp.Rollback(ctx) // all or nothing
	}
	return true, sp.Commit(ctx)
}

// applyInclusions bundles the inclusions of the event's package: kept when
// the package is re-priced, replaced when another package is chosen.
func (m *Module) applyInclusions(ctx context.Context, tx pgx.Tx, eid, packageID uuid.UUID) error {
	var same int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM banquet.event_inclusions WHERE event_id = $1 AND package_id = $2 AND status <> 'cancelled'`,
		eid, packageID).Scan(&same); err != nil {
		return err
	}
	if same > 0 {
		return nil
	}
	if err := m.cancelInclusions(ctx, tx, eid, "package changed"); err != nil {
		return err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT inclusions FROM banquet.packages WHERE id = $1`, packageID).Scan(&raw); err != nil {
		return err
	}
	list, err := parseInclusions(raw)
	if err != nil {
		return err
	}
	e, err := m.Get(ctx, tx, eid)
	if err != nil {
		return err
	}
	for i, in := range list {
		incID := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO banquet.event_inclusions (id, property_id, event_id, package_id, seq, kind, label, resource_type, quantity,
			nights, hours, voucher_type_code, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)`, incID, e.PropertyID, eid,
			packageID, i+1, in.Kind, in.Label, nzs(in.ResourceType), in.Quantity, in.Nights, in.Hours, nzs(in.VoucherTypeCode), actor(ctx)); err != nil {
			return err
		}
		if in.Kind != "resource" {
			continue
		}
		ok, err := m.holdInclusion(ctx, tx, e, incID, in)
		if err != nil {
			return err
		}
		status, note := "held", ""
		if !ok {
			status, note = "unavailable", "no free "+strings.ReplaceAll(in.ResourceType, "_", " ")+" for the period"
		}
		if _, err := tx.Exec(ctx, `UPDATE banquet.event_inclusions SET status = $2, note = $3 WHERE id = $1`, incID, status, nzs(note)); err != nil {
			return err
		}
	}
	if e.Status == StatusDefinite {
		return m.issueVouchers(ctx, tx, e)
	}
	return nil
}

// issueVouchers issues the F&B vouchers of a Definite event (once).
func (m *Module) issueVouchers(ctx context.Context, tx pgx.Tx, e BanquetEvent) error {
	if m.Vouchers == nil {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id, label, voucher_type_code, quantity FROM banquet.event_inclusions WHERE event_id = $1 AND kind = 'voucher'
		AND status = 'pending' ORDER BY seq FOR UPDATE`, e.ID)
	if err != nil {
		return err
	}
	type v struct {
		id          uuid.UUID
		label, code string
		qty         int
	}
	var list []v
	for rows.Next() {
		var x v
		if err := rows.Scan(&x.id, &x.label, &x.code, &x.qty); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	if len(list) == 0 {
		return nil
	}
	if e.CustomerID == nil {
		_, err := tx.Exec(ctx, `UPDATE banquet.event_inclusions SET note = 'vouchers need a customer on the event' WHERE event_id = $1 AND kind = 'voucher'
			AND status = 'pending'`, e.ID)
		return err
	}
	for _, x := range list {
		codes, err := m.Vouchers(ctx, tx, e.PropertyID, x.code, x.qty, *e.CustomerID, e.ID, e.Number+" · "+x.label)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE banquet.event_inclusions SET status = 'issued', voucher_codes = $2, note = NULL WHERE id = $1`, x.id, codes); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "issue_voucher", EntityType: "banquet.event_inclusion", EntityID: x.id.String(),
			EntityLabel: e.Number + " · " + x.label, PropertyID: &e.PropertyID, After: map[string]any{"codes": codes}, ActorName: sysActor(ctx)}); err != nil {
			return err
		}
	}
	return nil
}

// cancelInclusions releases the held resources of the inclusions and
// cancels the ones not delivered yet.
func (m *Module) cancelInclusions(ctx context.Context, tx pgx.Tx, eid uuid.UUID, reason string) error {
	list, err := m.resources(ctx, tx, eid)
	if err != nil {
		return err
	}
	for _, x := range list {
		if x.InclusionID != nil && (x.Status == "held" || x.Status == "confirmed") {
			if err := m.releaseResource(ctx, tx, x, reason); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE banquet.event_inclusions SET status = 'cancelled', note = $2, updated_by = $3 WHERE event_id = $1
		AND status IN ('pending', 'held', 'unavailable')`, eid, nzs(reason), actor(ctx))
	return err
}
