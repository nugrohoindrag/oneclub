package stay

// Stay Front Desk of the MGCC bungalows (docs/requirement-booking-hotel-
// mgcc.md Bagian B): the registration card at check-in — identity with its
// photo (only staff with stay.identity.view open it), nationality, the
// signature drawn on the tablet and the keys handed over (FR-H47–H49) —,
// the room move of an in-house guest (FR-H53), the free or paid upgrade at
// assignment (FR-H46), the void of a reservation keyed in by mistake
// (supervisor, FR-H91), the shift handover notes (FR-H61), the printed
// lists of the day (FR-H62), the invoice of the folio (FR-H57) and the
// bungalow incidents (FR-H86).

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/reservation"
)

// register keeps the registration card of a check-in: nationality, the
// signature and the keys handed over.
func (m *Module) register(ctx context.Context, tx pgx.Tx, s Stay, in CheckInInput) error {
	var sig *uuid.UUID
	if in.Signature != "" {
		raw := in.Signature
		if i := strings.Index(raw, ","); strings.HasPrefix(raw, "data:") && i > 0 {
			raw = raw[i+1:]
		}
		img, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(img) < 8 || !bytes.HasPrefix(img, []byte("\x89PNG")) {
			return handle.Invalid("signature", "invalid", "the signature must be a PNG image")
		}
		if len(img) > 1<<20 {
			return handle.Invalid("signature", "too_large", "the signature image is too large")
		}
		if m.Files == nil {
			return errs.Conflict("no_storage", "file storage is not configured")
		}
		f, err := m.Files.Save(ctx, tx, "signature-"+s.StayNo+".png", "image/png", "attachment", false, bytes.NewReader(img), int64(len(img)))
		if err != nil {
			return err
		}
		sig = &f.ID
	}
	keys := s.KeysIssued
	if in.KeysIssued != nil {
		keys = max(*in.KeysIssued, 0)
	}
	_, err := tx.Exec(ctx, `UPDATE stay.stays SET nationality = coalesce($2, nationality), signature_file_id = coalesce($3, signature_file_id),
		registered_at = CASE WHEN $3::uuid IS NOT NULL THEN now() ELSE registered_at END, keys_issued = $4, key_numbers = coalesce($5, key_numbers)
		WHERE id = $1`, s.ID, nzs(strings.TrimSpace(in.Nationality)), sig, keys, nzs(strings.TrimSpace(in.KeyNumbers)))
	return err
}

// saveIdentityPhoto keeps the photo of the identity card / passport of the
// guest (a private file; OCR is on hold).
func (m *Module) saveIdentityPhoto(w http.ResponseWriter, r *http.Request, sid uuid.UUID) (Stay, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20+64<<10)
	if err := r.ParseMultipartForm(5 << 20); err != nil { //nolint:gosec // G120: body capped by MaxBytesReader above
		return Stay{}, errs.BadRequest("invalid_upload", "upload a photo (JPEG or PNG) up to 5 MB")
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		return Stay{}, errs.Validation("file_required", "choose the photo", errs.Field("file", "required", "choose a file"))
	}
	defer file.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	ctype := http.DetectContentType(head[:n])
	if ctype != "image/jpeg" && ctype != "image/png" {
		return Stay{}, errs.Validation("file_type", "the photo must be JPEG or PNG", errs.Field("file", "type", "JPEG or PNG only"))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Stay{}, err
	}
	if m.Files == nil {
		return Stay{}, errs.Conflict("no_storage", "file storage is not configured")
	}
	ctx := r.Context()
	var out Stay
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		s, err := m.lock(ctx, tx, sid)
		if err != nil {
			return err
		}
		if s.Kind != "bungalow" || (s.Status != "reserved" && s.Status != "checked_in") {
			return errs.Conflict("invalid_status", "the identity is kept for a reserved or in-house bungalow stay")
		}
		f, err := m.Files.Save(ctx, tx, "identity-"+s.StayNo+strings.ToLower(extOf(hdr.Filename)), ctype, "attachment", false, file, hdr.Size)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE stay.stays SET id_photo_file_id = $2 WHERE id = $1`, s.ID, f.ID); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "identity_photo", EntityType: "stay.stay", EntityID: s.ID.String(),
			EntityLabel: s.StayNo, PropertyID: &s.PropertyID, After: map[string]any{"fileId": f.ID}}); err != nil {
			return err
		}
		out, err = m.Get(ctx, tx, sid)
		return err
	})
	return out, err
}

func extOf(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i:]
	}
	return ""
}

// streamFile streams a private file of a stay (identity photo, signature).
func (m *Module) streamFile(w http.ResponseWriter, r *http.Request, column string) {
	sid, err := handle.ID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var fid *uuid.UUID
	if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT `+column+` FROM stay.stays WHERE id = $1`, sid).Scan(&fid)
	}); err != nil || fid == nil {
		httpx.WriteError(w, r, errs.NotFound("file"))
		return
	}
	rc, name, err := m.Files.Open(ctx, *fid)
	if err != nil {
		httpx.WriteError(w, r, errs.NotFound("file"))
		return
	}
	defer rc.Close()
	ctype := "image/jpeg"
	if strings.HasSuffix(strings.ToLower(name), ".png") {
		ctype = "image/png"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = io.Copy(w, rc)
}

// registrationCardPDF renders the registration card of a stay (FR-H48):
// guest, bungalow, dates, rate, deposit, house rules and the signature.
func (m *Module) registrationCardPDF(ctx context.Context, tx pgx.Tx, sid uuid.UUID, lang string) ([]byte, string, error) {
	s, err := m.Get(ctx, tx, sid)
	if err != nil {
		return nil, "", err
	}
	pol, _, err := m.policy(ctx, tx, s.PropertyID)
	if err != nil {
		return nil, "", err
	}
	r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
	if err != nil {
		return nil, "", err
	}
	loc := calendar.Location(ctx, tx)
	c := m.contactOf(ctx, tx, s.PropertyID, pol)
	en := lang == "en"
	t := func(id, e string) string {
		if en {
			return e
		}
		return id
	}
	d := pdf.New()
	d.Row(16, true, t("Kartu Registrasi Tamu", "Guest Registration Card"), c.Name)
	d.Row(9, false, c.Address, strings.Trim(c.Phone+" · "+c.Email, " ·"))
	d.Space(6)
	rows := [][2]string{
		{t("No. reservasi", "Reservation"), s.StayNo + " · " + s.ReservationCode},
		{t("Nama tamu", "Guest"), guestLabel(s)},
		{t("Tamu menginap", "Staying guest"), nonEmpty(deref(s.OccupantName), guestLabel(s))},
		{t("Telepon / e-mail", "Phone / e-mail"), strings.Trim(deref(s.GuestPhone)+" · "+deref(s.GuestEmail), " ·")},
		{t("Identitas", "Identity"), strings.ToUpper(deref(s.IDType)) + " " + deref(s.IDNumberMasked)},
		{t("Kewarganegaraan", "Nationality"), nonEmpty(deref(s.Nationality), "—")},
		{t("Bungalow", "Bungalow"), s.UnitName + " · " + deref(s.TypeName)},
		{"Check-in", s.Start.In(loc).Format("Mon 02 Jan 2006 15:04")},
		{"Check-out", s.End.In(loc).Format("Mon 02 Jan 2006 15:04")},
		{t("Malam / tamu", "Nights / guests"), fmt.Sprintf("%d · %d %s %d %s", s.Nights, s.Adults, t("dewasa", "adults"), s.Children, t("anak", "children"))},
		{t("Rate plan", "Rate plan"), deref(s.RatePlan)},
		{"Total", rupiah(s.TotalDue)},
		{t("Deposit wajib / dibayar", "Deposit required / paid"), rupiah(r.DepositRequired) + " / " + rupiah(s.Paid)},
		{t("Kunci", "Keys"), fmt.Sprintf("%d %s", s.KeysIssued, deref(s.KeyNumbers))},
	}
	for _, x := range rows {
		d.Row(10, false, x[0], x[1])
	}
	d.Space(6)
	d.Row(10, true, t("Aturan rumah", "House rules"))
	rules := pol.HouseRules
	if en && pol.HouseRulesEn != "" {
		rules = pol.HouseRulesEn
	}
	for _, line := range wrapText(rules, 105) {
		d.Row(8, false, line)
	}
	d.Space(8)
	d.Row(9, false, t("Saya menyetujui aturan rumah dan bertanggung jawab atas tagihan selama menginap.",
		"I accept the house rules and am liable for the charges of my stay."))
	var sig *uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT signature_file_id FROM stay.stays WHERE id = $1`, sid).Scan(&sig)
	if sig != nil && m.Files != nil {
		if rc, _, err := m.Files.Open(ctx, *sig); err == nil {
			img, rerr := io.ReadAll(io.LimitReader(rc, 2<<20))
			_ = rc.Close()
			if rerr == nil {
				y := d.Y - 70
				if err := d.Image(img, pdf.Margin, y, 180, 60); err == nil {
					d.Y = y - 6
				}
			}
		}
	}
	signed := t("Belum ditandatangani", "Not signed")
	if s.RegisteredAt != nil {
		signed = t("Ditandatangani ", "Signed ") + s.RegisteredAt.In(loc).Format("02 Jan 2006 15:04")
	}
	d.Row(9, false, t("Tanda tangan tamu", "Guest signature"), signed)
	return d.Bytes(), "registration-" + s.StayNo + ".pdf", nil
}

// invoicePDF renders the folio of a stay (invoice / receipt at check-out).
func (m *Module) invoicePDF(ctx context.Context, tx pgx.Tx, sid uuid.UUID) ([]byte, string, error) {
	s, err := m.Get(ctx, tx, sid)
	if err != nil {
		return nil, "", err
	}
	if s.FolioID == nil {
		return nil, "", errs.NotFound("folio")
	}
	f, err := billing.GetFolio(ctx, tx, *s.FolioID)
	if err != nil {
		return nil, "", err
	}
	pol, _, err := m.policy(ctx, tx, s.PropertyID)
	if err != nil {
		return nil, "", err
	}
	loc := calendar.Location(ctx, tx)
	c := m.contactOf(ctx, tx, s.PropertyID, pol)
	d := pdf.New()
	title := "Tagihan Menginap"
	if s.Status == "checked_out" {
		title = "Invoice / Kuitansi"
	}
	d.Row(16, true, title, c.Name)
	d.Row(9, false, "Folio "+f.Number+" · "+s.StayNo, c.Address)
	d.Row(10, false, guestLabel(s)+" · "+s.UnitName, s.Start.In(loc).Format("02 Jan")+" – "+s.End.In(loc).Format("02 Jan 2006"))
	d.Space(6)
	for _, l := range f.Lines {
		if l.VoidedAt != nil {
			continue
		}
		d.Row(9, false, l.PostedAt.In(loc).Format("02/01")+"  "+l.Description, rupiah(l.Total))
	}
	if s.RoomPosting == "nightly" {
		if rest := decOf(s.TotalDue).Sub(decOf(f.Charges)); rest.IsPositive() {
			d.Row(9, false, "Malam yang belum diposting", rupiah(rest.String()))
		}
	}
	d.Space(4)
	d.Row(10, true, "Total", rupiah(s.TotalDue))
	for _, p := range f.Payments {
		if p.Status == "completed" || p.Status == "refunded" {
			d.Row(9, false, "Pembayaran "+strings.ReplaceAll(p.MethodType, "_", " ")+" "+p.Number, "-"+rupiah(p.Amount))
		}
	}
	d.Row(10, true, "Saldo", rupiah(decOf(s.TotalDue).Sub(decOf(s.Paid)).String()))
	d.Space(6)
	d.Row(8, false, "Dicetak "+clock.Now().In(loc).Format("02 Jan 2006 15:04"))
	return d.Bytes(), "invoice-" + s.StayNo + ".pdf", nil
}

// ── room move (FR-H53) and upgrade (FR-H46) ───────────────────────────────

// MoveInput moves an in-house guest to another bungalow.
type MoveInput struct {
	UnitID           uuid.UUID `json:"unitId"`
	Reason           string    `json:"reason"`
	Upgrade          bool      `json:"upgrade,omitempty" doc:"The guest asked for a better room type"`
	ChargeDifference bool      `json:"chargeDifference,omitempty" doc:"Upgrade: charge the price difference of the nights left"`
}

// RoomMove is a move of a stay.
type RoomMove struct {
	ID        uuid.UUID `json:"id" db:"id"`
	From      string    `json:"from" db:"from_code"`
	To        string    `json:"to" db:"to_code"`
	Reason    string    `json:"reason" db:"reason"`
	Upgrade   bool      `json:"upgrade" db:"upgrade"`
	Charge    string    `json:"charge" db:"charge"`
	MovedAt   time.Time `json:"movedAt" db:"moved_at"`
	CreatedBy *string   `json:"createdBy" db:"created_by_name"`
}

// MoveRoom moves an in-house guest: one folio, the old bungalow Dirty with
// a housekeeping task, the price difference only when the guest asked for
// an upgrade and it is charged.
func (m *Module) MoveRoom(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in MoveInput) (StayResult, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return StayResult{}, err
	}
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Kind != "bungalow" || s.Status != "checked_in" {
		return StayResult{}, errs.Conflict("invalid_status", "a room move is for an in-house guest; before check-in assign another bungalow")
	}
	if in.UnitID == s.UnitID {
		return StayResult{}, handle.Invalid("unitId", "same_unit", "choose another bungalow")
	}
	u, err := m.unit(ctx, tx, "bungalow", in.UnitID)
	if err != nil {
		return StayResult{}, err
	}
	var hk string
	if err := tx.QueryRow(ctx, `SELECT hk_status FROM stay.bungalows WHERE id = $1`, u.ID).Scan(&hk); err != nil {
		return StayResult{}, err
	}
	now := clock.Now()
	if u.Status != "active" || hk != "ready" {
		return StayResult{}, errs.Conflict("unit_not_ready", u.Name+" belum Ready: pilih bungalow yang Ready")
	}
	if blk, err := m.activeBlock(ctx, tx, u.ID, now); err != nil {
		return StayResult{}, err
	} else if blk != "" {
		return StayResult{}, errs.Conflict("unit_blocked", u.Name+" is "+strings.ReplaceAll(blk, "_", " "))
	}
	if s.UnitTypeID != nil && u.TypeID != nil && *s.UnitTypeID != *u.TypeID {
		if err := checkCapacity(ctx, tx, *u.TypeID, s.Adults, s.Children); err != nil {
			return StayResult{}, err
		}
	}
	r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
	if err != nil {
		return StayResult{}, err
	}
	old, err := m.unit(ctx, tx, "bungalow", s.UnitID)
	if err != nil {
		return StayResult{}, err
	}
	cur := currentLine(r, old.ResourceID)
	end := s.End
	if s.LateCheckOutUntil != nil && s.LateCheckOutUntil.After(end) {
		end = *s.LateCheckOutUntil
	}
	if !end.After(now) {
		return StayResult{}, errs.Conflict("past_departure", "the stay is past its departure: check the guest out")
	}
	if _, _, err := m.Res.AddLines(ctx, tx, r.ID, []reservation.LineRequest{{ResourceID: *u.ResourceID, Start: now, End: end, Description: u.Name}},
		"room move: "+in.Reason); err != nil {
		return StayResult{}, err
	}
	if err := m.Res.ShortenLine(ctx, tx, cur.ID, now); err != nil {
		return StayResult{}, err
	}
	charge := decimal.Zero
	if in.Upgrade && in.ChargeDifference && s.UnitTypeID != nil && u.TypeID != nil && *s.UnitTypeID != *u.TypeID && s.FolioID != nil {
		if charge, err = m.upgradeDifference(ctx, tx, s, *u.TypeID, now); err != nil {
			return StayResult{}, err
		}
		if charge.IsPositive() {
			if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *s.FolioID, ReferenceType: "stay.stay",
				ReferenceID: &s.ID, Description: "Upgrade " + old.Name + " → " + u.Name, Quantity: decimal.NewFromInt(1), Net: charge},
				BusinessLine: billing.LineStay, RevenueComponent: "bungalow"}); err != nil {
				return StayResult{}, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.stays SET unit_id = $2, unit_type_id = coalesce($3, unit_type_id), unit_assigned = true, updated_reason = $4,
		updated_by = $5 WHERE id = $1`, s.ID, u.ID, u.TypeID, "room move: "+in.Reason, actor(ctx)); err != nil {
		return StayResult{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO stay.room_moves (id, property_id, stay_id, from_bungalow_id, to_bungalow_id, reason, upgrade, charge, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9)`, id.New(), s.PropertyID, s.ID, old.ID, u.ID, in.Reason, in.Upgrade, charge.String(), actor(ctx)); err != nil {
		return StayResult{}, err
	}
	// the old bungalow is Dirty and housekeeping gets its cleaning
	if err := m.setHKStatus(ctx, tx, old.ID, "dirty", "room move "+s.StayNo); err != nil {
		return StayResult{}, err
	}
	if _, err := m.createHKTask(ctx, tx, s.PropertyID, hkTaskInput{BungalowID: old.ID, StayID: &s.ID, TaskType: "checkout_cleaning", Priority: "high",
		Source: "check_out", Notes: "Room move " + s.StayNo + " → " + u.Name}); err != nil {
		return StayResult{}, err
	}
	out, err := m.result(ctx, tx, s.ID)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "room_move", EntityType: "stay.stay", EntityID: s.ID.String(), EntityLabel: s.StayNo,
		PropertyID: &s.PropertyID, Reason: in.Reason, Before: map[string]any{"unit": old.Name}, After: map[string]any{"unit": u.Name, "upgrade": in.Upgrade,
			"charge": charge.String()}}); err != nil {
		return out, err
	}
	_, err = m.Events.Publish(ctx, tx, "stay.room_moved", "stay.stay", &s.ID, &s.PropertyID, map[string]any{"stayId": s.ID, "from": old.Name, "to": u.Name})
	return out, err
}

// currentLine is the reservation line of the bungalow the guest is in.
func currentLine(r reservation.Reservation, resource *uuid.UUID) reservation.Line {
	line := r.Lines[0]
	if resource == nil {
		return line
	}
	for _, l := range r.Lines {
		if l.ResourceID == *resource && l.Status != "cancelled" && l.Status != "released" {
			line = l
		}
	}
	return line
}

// upgradeDifference is the room price difference of the nights left at the
// rate plan of the stay between the new and the booked room type.
func (m *Module) upgradeDifference(ctx context.Context, tx pgx.Tx, s Stay, newType uuid.UUID, now time.Time) (decimal.Decimal, error) {
	loc := calendar.Location(ctx, tx)
	n := now.In(loc)
	from := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	e := s.End.In(loc)
	to := time.Date(e.Year(), e.Month(), e.Day(), 0, 0, 0, 0, loc)
	if !to.After(from) {
		return decimal.Zero, nil
	}
	price := func(t uuid.UUID) (decimal.Decimal, error) {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return decimal.Zero, err
		}
		defer func() { _ = sp.Rollback(ctx) }()
		q, err := m.quoteRoom(ctx, sp, s.PropertyID, roomRequest{TypeID: t, RatePlan: deref(s.RatePlan), Arrival: from, Departure: to, Adults: 1,
			Segment: "guest", Source: "front_desk", BookedAt: s.CreatedAt})
		if err != nil || q == nil {
			return decimal.Zero, err
		}
		return decOf(q.Gross), nil
	}
	a, err := price(*s.UnitTypeID)
	if err != nil {
		return decimal.Zero, err
	}
	b, err := price(newType)
	if err != nil {
		return decimal.Zero, err
	}
	return decimal.Max(b.Sub(a), decimal.Zero), nil
}

// UpgradeInput assigns a bungalow of another room type before check-in.
type UpgradeInput struct {
	UnitID uuid.UUID `json:"unitId"`
	Paid   bool      `json:"paid,omitempty" doc:"Paid upgrade: the stay is repriced at the new room type; else a free upgrade keeps the price"`
	Reason string    `json:"reason"`
}

// Upgrade assigns a bungalow of another type to a reserved stay (FR-H46):
// a paid upgrade reprices the stay at the new type, a free one keeps the
// price (audited).
func (m *Module) Upgrade(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in UpgradeInput) (StayResult, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return StayResult{}, err
	}
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Status != "reserved" {
		return StayResult{}, errs.Conflict("invalid_status", "upgrade before check-in; an in-house guest is moved")
	}
	u, err := m.unit(ctx, tx, "bungalow", in.UnitID)
	if err != nil {
		return StayResult{}, err
	}
	if in.Paid {
		return m.Reschedule(ctx, tx, sid, RescheduleInput{UnitID: &in.UnitID, Reason: "paid upgrade: " + in.Reason})
	}
	if u.TypeID != nil {
		if err := checkCapacity(ctx, tx, *u.TypeID, s.Adults, s.Children); err != nil {
			return StayResult{}, err
		}
	}
	r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
	if err != nil {
		return StayResult{}, err
	}
	if _, err := m.Res.Reschedule(ctx, tx, r.ID, []reservation.LineChange{{LineID: r.Lines[0].ID, ResourceID: u.ResourceID, Start: s.Start, End: s.End}},
		"free upgrade: "+in.Reason); err != nil {
		return StayResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.stays SET unit_id = $2, unit_type_id = $3, unit_assigned = true, updated_reason = $4 WHERE id = $1`, s.ID, u.ID,
		u.TypeID, "free upgrade: "+in.Reason); err != nil {
		return StayResult{}, err
	}
	out, err := m.result(ctx, tx, s.ID)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "free_upgrade", EntityType: "stay.stay", EntityID: s.ID.String(), EntityLabel: s.StayNo,
		PropertyID: &s.PropertyID, Reason: in.Reason, Before: map[string]any{"unit": s.UnitName, "type": s.TypeName}, After: map[string]any{"unit": u.Name}})
}

// Void cancels a reservation keyed in by mistake (supervisor, reason, no fee).
func (m *Module) Void(ctx context.Context, tx pgx.Tx, sid uuid.UUID, reason string) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if err := supervised(ctx, s.PropertyID, reason, "Voiding a reservation"); err != nil {
		return StayResult{}, err
	}
	if s.Status != "reserved" && s.Status != "requested" {
		return StayResult{}, errs.Conflict("invalid_status", "only a reservation not checked in can be voided")
	}
	if _, err := m.Res.Void(ctx, tx, s.ReservationID, reason); err != nil {
		return StayResult{}, err
	}
	if s.FolioID != nil {
		if err := m.Billing.CancelPending(ctx, tx, *s.FolioID, "void: "+reason); err != nil {
			return StayResult{}, err
		}
		if st, err := billing.FolioStatus(ctx, tx, *s.FolioID); err == nil && st == "open" {
			lines, err := billing.LinesOf(ctx, tx, *s.FolioID)
			if err != nil {
				return StayResult{}, err
			}
			for _, l := range lines {
				if err := m.Billing.VoidCharge(ctx, tx, l.ID, "void: "+reason); err != nil {
					return StayResult{}, err
				}
			}
		}
	}
	if err := m.reverseAccommodation(ctx, tx, s); err != nil {
		return StayResult{}, err
	}
	out, err := m.setStatus(ctx, tx, s, "void", `, voided_at = now(), voided_by = $4, void_reason = $5`, actor(ctx), reason)
	if err != nil {
		return out, err
	}
	if s.UnitTypeID != nil {
		return out, m.offerWaitlist(ctx, tx, s.PropertyID, *s.UnitTypeID)
	}
	return out, nil
}

// ── shift handover (FR-H61) ──────────────────────────────────────────────

// HandoverNote is a note for the next shift of the front desk.
type HandoverNote struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	BusinessDate time.Time  `json:"businessDate" db:"business_date"`
	Category     string     `json:"category" db:"category" enum:"vip,complaint,key,lost_found,payment,general"`
	Body         string     `json:"body" db:"body"`
	StayID       *uuid.UUID `json:"stayId" db:"stay_id"`
	StayNo       *string    `json:"stayNo" db:"stay_no"`
	Status       string     `json:"status" db:"status" enum:"open,done"`
	AuthorName   *string    `json:"authorName" db:"author_name"`
	CreatedAt    time.Time  `json:"createdAt" db:"created_at"`
	DoneAt       *time.Time `json:"doneAt" db:"done_at"`
}

// HandoverInput writes a handover note.
type HandoverInput struct {
	Category string     `json:"category,omitempty" enum:"vip,complaint,key,lost_found,payment,general"`
	Body     string     `json:"body"`
	StayID   *uuid.UUID `json:"stayId,omitempty"`
}

const handoverSelect = `SELECT h.id, h.business_date, h.category, h.body, h.stay_id, s.stay_no, h.status, h.author_name, h.created_at, h.done_at
	FROM stay.handover_notes h LEFT JOIN stay.stays s ON s.id = h.stay_id`

func (m *Module) addHandover(ctx context.Context, tx pgx.Tx, property uuid.UUID, in HandoverInput) (HandoverNote, error) {
	if err := handle.Required("body", strings.TrimSpace(in.Body)); err != nil {
		return HandoverNote{}, err
	}
	if in.Category == "" {
		in.Category = "general"
	}
	hid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO stay.handover_notes (id, property_id, business_date, category, body, stay_id, author_name, created_by)
		VALUES ($1,$2,$3::date,$4,$5,$6,$7,$8)`, hid, property, localToday(ctx, tx).Format(time.DateOnly), in.Category, strings.TrimSpace(in.Body), in.StayID,
		nzs(actorName(ctx, tx)), actor(ctx)); err != nil {
		return HandoverNote{}, err
	}
	rows, err := tx.Query(ctx, handoverSelect+` WHERE h.id = $1`, hid)
	out, err := handle.One[HandoverNote](rows, err, "handover note")
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionCreate, EntityType: "stay.handover_note", EntityID: hid.String(),
		EntityLabel: in.Category, PropertyID: &property, After: out})
}

// ── printed lists of the day (FR-H62) ────────────────────────────────────

// listPDF renders the arrival, departure, in-house or foreign guest list
// of a day for the front desk, security and housekeeping.
func (m *Module) listPDF(ctx context.Context, tx pgx.Tx, property uuid.UUID, kind string, day time.Time) ([]byte, string, error) {
	fo, err := m.FrontOfficeDay(ctx, tx, property, day)
	if err != nil {
		return nil, "", err
	}
	loc := calendar.Location(ctx, tx)
	var list []Stay
	title := ""
	switch kind {
	case "arrivals":
		list, title = fo.Arrivals, "Arrival List"
	case "departures":
		list, title = fo.Departures, "Departure List"
	case "in-house":
		list, title = fo.InHouse, "In-house List"
	case "foreign-guests":
		title = "Daftar Tamu Asing"
		for _, s := range fo.InHouse {
			if n := strings.ToUpper(strings.TrimSpace(deref(s.Nationality))); n != "" && n != "ID" && n != "IDN" && n != "INDONESIA" && n != "WNI" {
				list = append(list, s)
			}
		}
	default:
		return nil, "", errs.NotFound("list")
	}
	pol, _, _ := m.policy(ctx, tx, property)
	c := m.contactOf(ctx, tx, property, pol)
	d := pdf.New()
	d.Row(15, true, title, c.Name)
	d.Row(9, false, day.In(loc).Format("Monday 02 January 2006"), fmt.Sprintf("%d reservasi · dicetak %s", len(list), clock.Now().In(loc).Format("15:04")))
	d.Space(6)
	xs := []float64{pdf.Margin, pdf.Margin + 60, pdf.Margin + 200, pdf.Margin + 300, pdf.Margin + 370, pdf.Margin + 440}
	head := []string{"Bungalow", "Tamu", "Datang", "Keluar", "Tamu (D/A)", "Catatan"}
	if kind == "foreign-guests" {
		head = []string{"Bungalow", "Tamu", "Datang", "Keluar", "Negara", "Identitas"}
	}
	d.Columns(9, true, xs, head)
	for _, s := range list {
		unit := deref(s.UnitCode)
		if !s.UnitAssigned {
			unit = "—"
		}
		note := strings.TrimSpace(strings.Join([]string{map[bool]string{true: "VIP"}[s.VIP], s.PaymentStatus, deref(s.ExpectedArrival)}, " "))
		cells := []string{unit, trunc(guestLabel(s), 26), s.Start.In(loc).Format("02/01 15:04"), s.End.In(loc).Format("02/01 15:04"),
			fmt.Sprintf("%d/%d", s.Adults, s.Children), trunc(note, 22)}
		if kind == "foreign-guests" {
			cells[4], cells[5] = deref(s.Nationality), strings.ToUpper(deref(s.IDType))+" "+deref(s.IDNumberMasked)
		}
		d.Columns(9, false, xs, cells)
	}
	return d.Bytes(), kind + "-" + day.In(loc).Format("2006-01-02") + ".pdf", nil
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ── incidents (FR-H86) ───────────────────────────────────────────────────

// Incident is a damage, complaint or lost item of a bungalow.
type Incident struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	Number       string     `json:"number" db:"number"`
	BungalowID   *uuid.UUID `json:"bungalowId" db:"bungalow_id"`
	BungalowCode *string    `json:"bungalowCode" db:"bungalow_code"`
	StayID       *uuid.UUID `json:"stayId" db:"stay_id"`
	StayNo       *string    `json:"stayNo" db:"stay_no"`
	Category     string     `json:"category" db:"category" enum:"damage,complaint,lost_item,safety,noise,other"`
	Severity     string     `json:"severity" db:"severity" enum:"low,medium,high"`
	Status       string     `json:"status" db:"status" enum:"open,closed"`
	Description  string     `json:"description" db:"description"`
	ActionTaken  *string    `json:"actionTaken" db:"action_taken"`
	DamageAmount *string    `json:"damageAmount" db:"damage_amount"`
	ReportedBy   *string    `json:"reportedBy" db:"reported_by"`
	OccurredAt   time.Time  `json:"occurredAt" db:"occurred_at"`
	ClosedAt     *time.Time `json:"closedAt" db:"closed_at"`
}

// IncidentInput reports an incident.
type IncidentInput struct {
	BungalowID   *uuid.UUID `json:"bungalowId,omitempty"`
	StayID       *uuid.UUID `json:"stayId,omitempty"`
	Category     string     `json:"category" enum:"damage,complaint,lost_item,safety,noise,other"`
	Severity     string     `json:"severity,omitempty" enum:"low,medium,high"`
	Description  string     `json:"description"`
	DamageAmount string     `json:"damageAmount,omitempty"`
	ChargeGuest  bool       `json:"chargeGuest,omitempty" doc:"Damage: charge the amount to the folio of the stay"`
}

// CloseIncidentInput closes an incident with the action taken.
type CloseIncidentInput struct {
	ActionTaken string `json:"actionTaken"`
}

const incidentSelect = `SELECT i.id, i.number, i.bungalow_id, b.code AS bungalow_code, i.stay_id, s.stay_no, i.category, i.severity, i.status, i.description,
	i.action_taken, trim_scale(i.damage_amount)::text AS damage_amount, i.reported_by, i.occurred_at, i.closed_at
	FROM stay.incidents i LEFT JOIN stay.bungalows b ON b.id = i.bungalow_id LEFT JOIN stay.stays s ON s.id = i.stay_id`

func (m *Module) reportIncident(ctx context.Context, tx pgx.Tx, property uuid.UUID, in IncidentInput) (Incident, error) {
	if err := handle.Required("description", strings.TrimSpace(in.Description)); err != nil {
		return Incident{}, err
	}
	if in.Severity == "" {
		in.Severity = "medium"
	}
	if in.Category == "" {
		in.Category = "other"
	}
	var dmg *string
	if in.DamageAmount != "" {
		d, err := handle.Decimal("damageAmount", in.DamageAmount, decimal.Zero)
		if err != nil {
			return Incident{}, err
		}
		v := d.String()
		dmg = &v
	}
	if in.StayID != nil && in.BungalowID == nil {
		var u uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT unit_id FROM stay.stays WHERE id = $1 AND kind = 'bungalow'`, *in.StayID).Scan(&u); err == nil {
			in.BungalowID = &u
		}
	}
	loc := calendar.Location(ctx, tx)
	no, err := numbering.Next(ctx, tx, property, "INC", clock.Now().In(loc))
	if err != nil {
		return Incident{}, err
	}
	iid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO stay.incidents (id, property_id, number, bungalow_id, stay_id, category, severity, description, damage_amount,
		reported_by, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11)`, iid, property, no, in.BungalowID, in.StayID, in.Category, in.Severity,
		strings.TrimSpace(in.Description), dmg, nzs(actorName(ctx, tx)), actor(ctx)); err != nil {
		return Incident{}, err
	}
	if in.ChargeGuest && dmg != nil && in.StayID != nil && decOf(*dmg).IsPositive() {
		if _, err := m.ChargeToRoom(ctx, tx, *in.StayID, ChargeInput{Category: "damage", Description: "Kerusakan · " + no, UnitPrice: *dmg, Quantity: 1}); err != nil {
			return Incident{}, err
		}
	}
	rows, err := tx.Query(ctx, incidentSelect+` WHERE i.id = $1`, iid)
	out, err := handle.One[Incident](rows, err, "incident")
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionCreate, EntityType: "stay.incident", EntityID: iid.String(),
		EntityLabel: no, PropertyID: &property, After: out}); err != nil {
		return out, err
	}
	if in.Severity == "high" {
		return out, m.notifyStaff(ctx, tx, property, "stay.incident.manage", "stay.ops_work_order", map[string]any{"title": "Insiden " + no,
			"body": in.Category + " · " + trunc(in.Description, 80)}, "/accommodation/incidents")
	}
	return out, nil
}

// ── POS charge to room (FR-H82) ──────────────────────────────────────────

// InHouseGuest is an in-house guest a POS cashier can charge.
type InHouseGuest struct {
	StayID      uuid.UUID `json:"stayId"`
	StayNo      string    `json:"stayNo"`
	Guest       string    `json:"guest"`
	Bungalow    string    `json:"bungalow"`
	Departure   time.Time `json:"departure"`
	FolioID     uuid.UUID `json:"folioId"`
	Balance     string    `json:"balance"`
	LimitLeft   *string   `json:"limitLeft" doc:"What can still be charged (Stay Policies roomChargeLimit); null = no limit"`
	CompanyPays bool      `json:"companyPays"`
}

// InHouseGuests lists the in-house guests by name or bungalow.
func (m *Module) InHouseGuests(ctx context.Context, q dbtx.Querier, property uuid.UUID, search string) ([]InHouseGuest, error) {
	pol, _, err := m.policy(ctx, q, property)
	if err != nil {
		return nil, err
	}
	list, err := handle.List[Stay](q.Query(ctx, staySelect+` WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.status = 'checked_in'
		AND ($2 = '' OR b.code ILIKE '%' || $2 || '%' OR b.name ILIKE '%' || $2 || '%' OR c.name ILIKE '%' || $2 || '%' OR s.guest_name ILIKE '%' || $2 || '%'
		  OR s.occupant_name ILIKE '%' || $2 || '%' OR s.stay_no ILIKE '%' || $2 || '%') ORDER BY b.code LIMIT 30`, property, strings.TrimSpace(search)))
	if err != nil {
		return nil, err
	}
	out := []InHouseGuest{}
	limit := decOf(pol.RoomChargeLimit)
	for _, s := range list {
		if s.FolioID == nil {
			continue
		}
		g := InHouseGuest{StayID: s.ID, StayNo: s.StayNo, Guest: nonEmpty(deref(s.OccupantName), guestLabel(s)), Bungalow: deref(s.UnitCode) + " · " + s.UnitName,
			Departure: s.End, FolioID: *s.FolioID, Balance: decOf(s.TotalDue).Sub(decOf(s.Paid)).String(),
			CompanyPays: s.BillingArrangement != nil && *s.BillingArrangement == "company_pays_all"}
		if limit.IsPositive() {
			left := decimal.Max(limit.Sub(decOf(g.Balance)), decimal.Zero).String()
			g.LimitLeft = &left
		}
		out = append(out, g)
	}
	return out, nil
}

// ChargeOrderInput charges a POS order to an in-house guest.
type ChargeOrderInput struct {
	OrderID uuid.UUID `json:"orderId"`
}

// ChargeOrder posts a POS order to the folio of an in-house guest: every
// item carries the guest and the bungalow; refused after check-out or over
// the charge limit (FR-H82).
func (m *Module) ChargeOrder(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in ChargeOrderInput) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Kind != "bungalow" || s.Status != "checked_in" || s.FolioID == nil {
		return StayResult{}, errs.Conflict("not_in_house", s.StayNo+": tamu tidak sedang menginap (sudah check-out atau belum check-in)")
	}
	o, err := m.POS.Order(ctx, tx, in.OrderID)
	if err != nil {
		return StayResult{}, err
	}
	pol, _, err := m.policy(ctx, tx, s.PropertyID)
	if err != nil {
		return StayResult{}, err
	}
	if limit := decOf(pol.RoomChargeLimit); limit.IsPositive() {
		after := decOf(s.TotalDue).Sub(decOf(s.Paid)).Add(decOf(o.Total))
		if after.GreaterThan(limit) {
			return StayResult{}, errs.Conflict("over_limit", fmt.Sprintf("charge to room melewati limit (Rp %s): minta tamu membayar di kasir",
				limit.StringFixed(0)))
		}
	}
	guest := nonEmpty(deref(s.OccupantName), guestLabel(s))
	if _, err := m.POS.ChargeToRoom(ctx, tx, in.OrderID, *s.FolioID, guest, deref(s.UnitCode)); err != nil {
		return StayResult{}, err
	}
	out, err := m.result(ctx, tx, s.ID)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "charge_order", EntityType: "stay.stay", EntityID: s.ID.String(), EntityLabel: s.StayNo,
		PropertyID: &s.PropertyID, After: map[string]any{"orderId": in.OrderID, "orderNo": o.OrderNo, "total": o.Total, "bungalow": s.UnitName}})
}

// ── housekeeping on duty (FR-H84) ────────────────────────────────────────

// DutyAttendant is a room attendant on the roster of a day.
type DutyAttendant struct {
	EmployeeID uuid.UUID  `json:"employeeId" db:"employee_id"`
	Name       string     `json:"name" db:"name"`
	UserID     *uuid.UUID `json:"userId" db:"user_id"`
	Shift      *string    `json:"shift" db:"shift"`
	From       *time.Time `json:"from" db:"starts_at"`
	To         *time.Time `json:"to" db:"ends_at"`
	OpenTasks  int        `json:"openTasks" db:"open_tasks"`
}

// onDutyHK lists the housekeeping staff with a shift on the local day,
// fewest open tasks first.
func (m *Module) onDutyHK(ctx context.Context, q dbtx.Querier, property uuid.UUID, day string) ([]DutyAttendant, error) {
	return handle.List[DutyAttendant](q.Query(ctx, `SELECT e.id AS employee_id, e.full_name AS name, u.id AS user_id, t.name AS shift, a.starts_at, a.ends_at,
		(SELECT count(*) FROM stay.housekeeping_tasks h WHERE h.property_id = $1 AND h.task_date = $2::date AND h.status IN ('open', 'in_progress')
		  AND (h.assignee_user_id = u.id OR h.assigned_to = e.full_name))::int AS open_tasks
		FROM hris.shift_assignments a JOIN hris.employees e ON e.id = a.employee_id LEFT JOIN hris.positions p ON p.id = e.position_id
		LEFT JOIN hris.shift_templates t ON t.id = a.shift_template_id LEFT JOIN platform.users u ON u.employee_id = e.id
		WHERE a.property_id = $1 AND a.work_date = $2::date AND a.kind = 'shift' AND a.status = 'scheduled'
		AND coalesce(a.workforce_role, p.workforce_role, '') = 'housekeeping'
		ORDER BY 7, e.full_name`, property, day))
}

// autoAssignee is the room attendant on duty with the fewest open tasks of
// the day (nil when the roster has nobody).
func (m *Module) autoAssignee(ctx context.Context, q dbtx.Querier, property uuid.UUID, day string) *DutyAttendant {
	list, err := m.onDutyHK(ctx, q, property, day)
	if err != nil || len(list) == 0 {
		return nil
	}
	return &list[0]
}
