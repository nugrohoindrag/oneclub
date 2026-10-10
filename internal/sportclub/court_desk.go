package sportclub

// Front desk of the Sport Club (docs/requirement-booking-sportclub-mgcc.md
// §5): check-in by QR / code per line with a specific reason when refused
// (FR-127, FR-136), payment at the desk with member charge and its limit
// (FR-63, FR-145), move to another court / hour by a supervisor with the
// higher price charged (FR-56), overtime (FR-68), extras during play
// (FR-67), No-show (FR-60), end of play with the settled bill (FR-69) and
// the void of a booking keyed in by mistake (FR-129). The API enforces the
// rules; the screens only offer what is allowed (FR-126).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/reservation"
)

// CourtBookingDetail is a booking with its bill and history.
type CourtBookingDetail struct {
	Booking  CourtBooking               `json:"booking"`
	Folio    *billing.FolioDetail       `json:"folio"`
	History  []reservation.HistoryEntry `json:"history"`
	Payments []billing.Payment          `json:"payments,omitempty" doc:"Payments taken by this action (receipts)"`
}

func (m *Module) detail(ctx context.Context, q pgx.Tx, rid uuid.UUID) (CourtBookingDetail, error) {
	b, err := m.bookingByID(ctx, q, rid, true)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	out := CourtBookingDetail{Booking: b}
	if b.FolioID != nil {
		f, err := billing.GetFolio(ctx, q, *b.FolioID)
		if err != nil {
			return out, err
		}
		out.Folio = &f
	}
	r, err := m.Res.Get(ctx, q, rid, true)
	if err != nil {
		return out, err
	}
	out.History = r.History
	return out, nil
}

func (m *Module) lockBooking(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID) (CourtBooking, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM reservation.reservations WHERE id = $1 AND property_id = $2 AND source_type = 'sportclub.court_booking'
		FOR UPDATE`, rid, property).Scan(&x); err != nil {
		return CourtBooking{}, errNotFound("court booking")
	}
	return m.bookingByID(ctx, tx, rid, false)
}

// ── check-in (FR-66, FR-127, FR-136) ──────────────────────────────────────────

// ScanInput is a scanned QR, a typed booking code or a booking id.
type ScanInput struct {
	Code    string      `json:"code,omitempty" doc:"Booking code or the QR (public token)"`
	LineIDs []uuid.UUID `json:"lineIds,omitempty" doc:"Lines to check in; default: the lines whose check-in is open now"`
}

// ScanResult tells the front desk what happened and what to do next.
type ScanResult struct {
	Result     string        `json:"result" enum:"checked_in,refused,not_found"`
	Reason     string        `json:"reason,omitempty" enum:"unpaid,too_early,over,expired,void,awaiting_payment,already_in,finished,no_show,unknown"`
	Message    string        `json:"message,omitempty"`
	NextAction string        `json:"nextAction,omitempty" enum:"take_payment,wait,search,none"`
	OpensAt    *time.Time    `json:"opensAt,omitempty"`
	CheckedIn  []uuid.UUID   `json:"checkedIn"`
	Booking    *CourtBooking `json:"booking,omitempty"`
}

// findByCode finds a court booking by its code or public token.
func (m *Module) findByCode(ctx context.Context, tx pgx.Tx, property uuid.UUID, code string) (*CourtBooking, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, nil
	}
	list, err := m.loadBookings(ctx, tx, property, `AND (r.code = upper($2) OR r.attributes ->> 'publicToken' = $2)`, code)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// ScanCheckIn checks in the lines of a booking whose check-in is open; a
// refusal names its reason and the next action.
func (m *Module) ScanCheckIn(ctx context.Context, tx pgx.Tx, property uuid.UUID, b *CourtBooking, lineIDs []uuid.UUID) (ScanResult, error) {
	out := ScanResult{CheckedIn: []uuid.UUID{}}
	if b == nil {
		out.Result, out.Reason, out.NextAction = "not_found", "unknown", "search"
		out.Message = "QR / kode booking tidak dikenal — cari booking lewat nama atau nomor HP."
		return out, nil
	}
	out.Booking = b
	refuse := func(reason, msg, next string) (ScanResult, error) {
		out.Result, out.Reason, out.Message, out.NextAction = "refused", reason, msg, next
		return out, nil
	}
	switch {
	case b.Void:
		return refuse("void", "Booking "+b.Code+" sudah di-void: "+b.VoidReason, "none")
	case b.State == StateExpired || b.Status == reservation.StatusExpired:
		return refuse("expired", "Booking "+b.Code+" kedaluwarsa (tidak dibayar sebelum batas waktu).", "none")
	case b.Status == reservation.StatusDraft:
		return refuse("awaiting_payment", "Booking "+b.Code+" masih menunggu pembayaran online.", "take_payment")
	case b.PayStatus != "paid" && b.PayStatus != "overpaid":
		return refuse("unpaid", fmt.Sprintf("Belum lunas: sisa tagihan Rp %s. Terima pembayaran dulu sebelum check-in.", fmtMoney(b.Balance)), "take_payment")
	}
	now := clock.Now()
	var open []uuid.UUID
	var first *CourtBookingLine
	for i := range b.Lines {
		l := &b.Lines[i]
		if len(lineIDs) > 0 && !containsID(lineIDs, l.ID) {
			continue
		}
		if l.Status == "confirmed" && (first == nil || l.Start.Before(first.Start)) {
			first = l
		}
		if l.CanCheckIn {
			open = append(open, l.ID)
		}
	}
	if len(open) == 0 {
		switch {
		case first != nil && now.Before(first.CheckInFrom):
			t := first.CheckInFrom
			out.OpensAt = &t
			loc := first.Start.Location()
			return refuse("too_early", fmt.Sprintf("Terlalu awal: check-in %s dibuka pukul %s (30 menit sebelum main).", first.CourtName,
				first.CheckInFrom.In(loc).Format("15:04")), "wait")
		case b.State == StatePlaying:
			return refuse("already_in", "Booking "+b.Code+" sudah check-in dan sedang main.", "none")
		case b.State == StateFinished:
			return refuse("finished", "Booking "+b.Code+" sudah selesai.", "none")
		case b.State == StateNoShow:
			return refuse("no_show", "Booking "+b.Code+" ditandai No-show.", "none")
		default:
			return refuse("over", "Jam main booking "+b.Code+" sudah lewat.", "none")
		}
	}
	r, err := m.Res.CheckInLines(ctx, tx, b.ID, open)
	if err != nil {
		return out, err
	}
	// the bill takes the extras and the café orders while in play (FR-67, FR-135)
	if r.FolioID == nil {
		if _, err := m.ensureFolio(ctx, tx, r); err != nil {
			return out, err
		}
	}
	nb, err := m.bookingByID(ctx, tx, r.ID, false)
	if err != nil {
		return out, err
	}
	out.Result, out.CheckedIn, out.Booking = "checked_in", open, &nb
	out.Message = "Check-in berhasil: " + nb.Name
	return out, m.live(ctx, tx, property, "checked_in", r.ID)
}

func containsID(ids []uuid.UUID, x uuid.UUID) bool {
	for _, i := range ids {
		if i == x {
			return true
		}
	}
	return false
}

func fmtMoney(s string) string {
	d, _ := decimal.NewFromString(s)
	n := d.Round(0).IntPart()
	neg := n < 0
	if neg {
		n = -n
	}
	raw := fmt.Sprint(n)
	var b strings.Builder
	for i, c := range raw {
		if i > 0 && (len(raw)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// ── payment at the desk (FR-63, FR-143, FR-145) ─────────────────────────────

// CourtPayInput is one payment of a booking bill (several for a split per
// player).
type CourtPayInput struct {
	MethodType string     `json:"methodType" enum:"cash,card,qris,bank_transfer,member_account"`
	Amount     string     `json:"amount,omitempty" doc:"Default: the balance"`
	Reference  string     `json:"reference,omitempty"`
	CustomerID *uuid.UUID `json:"customerId,omitempty" doc:"Member account to charge (default: the booking's customer)"`
	Payer      string     `json:"payer,omitempty" doc:"Split per player: the name on the receipt"`
}

// PayBooking takes a payment at the desk; a held booking becomes confirmed
// once paid.
func (m *Module) PayBooking(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in CourtPayInput, key string) (CourtBookingDetail, error) {
	b, err := m.lockBooking(ctx, tx, property, rid)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if b.Void || b.Status == reservation.StatusExpired || b.Status == reservation.StatusCancelled {
		return CourtBookingDetail{}, errs.Conflict("booking_closed", "booking "+b.Code+" is "+b.State+"; it cannot be paid")
	}
	if b.FolioID == nil {
		return CourtBookingDetail{}, errs.Conflict("nothing_to_pay", "booking "+b.Code+" has nothing to pay")
	}
	bal, _ := decimal.NewFromString(b.Balance)
	if !bal.IsPositive() {
		return CourtBookingDetail{}, errs.Conflict("nothing_to_pay", "booking "+b.Code+" is paid")
	}
	amt, err := handle.Decimal("amount", in.Amount, bal)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if !amt.IsPositive() || amt.GreaterThan(bal) {
		return CourtBookingDetail{}, handle.Invalid("amount", "invalid_amount", "the amount must be between 1 and the balance "+fmtMoney(b.Balance))
	}
	pi := billing.PaymentInput{FolioID: b.FolioID, MethodType: in.MethodType, Amount: amt, Reference: in.Reference, PayerName: in.Payer, Channel: "venue"}
	if in.MethodType == "member_account" {
		cust := in.CustomerID
		if cust == nil {
			cust = b.CustomerID
		}
		if cust == nil {
			return CourtBookingDetail{}, errs.Conflict("no_member_account", "choose the member to charge")
		}
		a, err := billing.AccountFor(ctx, tx, property, *cust, "member")
		if err != nil {
			return CourtBookingDetail{}, err
		}
		if a == nil {
			return CourtBookingDetail{}, errs.Conflict("no_member_account", "the customer has no member account — choose another payment method")
		}
		if err := billing.CheckCredit(ctx, tx, property, *a, amt); err != nil {
			if de, ok := errs.As(err); ok && de.Code == "credit_limit_exceeded" {
				left, _ := m.creditLeft(ctx, tx, property, *a)
				return CourtBookingDetail{}, errs.Conflict("credit_limit_exceeded", fmt.Sprintf("Limit member charge tidak cukup: sisa limit Rp %s. Pilih metode lain.", fmtMoney(left.String())))
			}
			return CourtBookingDetail{}, err
		}
		pi.AccountID, pi.Channel = &a.ID, "member_account"
	}
	pk := ""
	if key != "" {
		pk = key + "-pay"
	}
	p, err := m.Billing.TakeTender(ctx, tx, billing.TenderPaymentInput{PaymentInput: pi, IdempotencyKey: pk})
	if err != nil {
		return CourtBookingDetail{}, err
	}
	nb, err := m.bookingByID(ctx, tx, rid, false)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if nb.Status == reservation.StatusDraft && nb.PayStatus == "paid" {
		if _, err := m.Res.Confirm(ctx, tx, rid, false); err != nil {
			return CourtBookingDetail{}, err
		}
	}
	out, err := m.detail(ctx, tx, rid)
	out.Payments = []billing.Payment{p}
	if err != nil {
		return out, err
	}
	return out, m.live(ctx, tx, property, "paid", rid)
}

func (m *Module) creditLeft(ctx context.Context, tx pgx.Tx, property uuid.UUID, a billing.Account) (decimal.Decimal, error) {
	pol, _, err := billing.LoadMemberPolicy(ctx, tx, property)
	if err != nil {
		return decimal.Zero, err
	}
	limit, _ := decimal.NewFromString(pol.MemberChargeLimit)
	if a.CreditLimit != nil {
		limit, _ = decimal.NewFromString(*a.CreditLimit)
	}
	bal, _ := decimal.NewFromString(a.Balance)
	return decimal.Max(limit.Sub(bal), decimal.Zero), nil
}

// ── move, overtime, no-show, extras, end of play, void ──────────────────────

// MoveInput moves one line to another court and / or hour (same length).
type MoveInput struct {
	LineID  uuid.UUID  `json:"lineId"`
	CourtID *uuid.UUID `json:"courtId,omitempty" doc:"Another court of the same sport (default: the same court)"`
	Start   time.Time  `json:"start"`
	Reason  string     `json:"reason"`
}

// MoveLine moves a line (supervisor, FR-56): refused on a clash; a higher
// price is charged, a lower one is not refunded.
func (m *Module) MoveLine(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in MoveInput) (CourtBookingDetail, error) {
	b, err := m.lockBooking(ctx, tx, property, rid)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if strings.TrimSpace(in.Reason) == "" {
		return CourtBookingDetail{}, handle.Invalid("reason", "required", "the reason of the move is required")
	}
	var line *CourtBookingLine
	for i := range b.Lines {
		if b.Lines[i].ID == in.LineID {
			line = &b.Lines[i]
		}
	}
	if line == nil {
		return CourtBookingDetail{}, errs.Validation("invalid_line", "line not found in this booking")
	}
	if line.Status != "confirmed" && line.Status != "held" {
		return CourtBookingDetail{}, errs.Conflict("invalid_line_status", "only a scheduled line can be moved (is "+line.State+")")
	}
	courtID := *line.CourtID
	if in.CourtID != nil {
		courtID = *in.CourtID
	}
	pol, err := m.courtPolicy(ctx, tx, property)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	length := line.End.Sub(line.Start)
	slots, err := m.normalizeLines(ctx, tx, property, []CourtLine{{CourtID: courtID, Start: in.Start, End: in.Start.Add(length)}}, pol, slotOptions{})
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if len(slots) != 1 {
		return CourtBookingDetail{}, errs.Validation("invalid_move", "a line moves to consecutive hours of one court on one day")
	}
	s := slots[0]
	if s.Court.FacilityID != *line.FacilityID {
		return CourtBookingDetail{}, errs.Conflict("other_sport", "a line can only move to a court of the same sport")
	}
	if _, err := m.Res.Reschedule(ctx, tx, rid, []reservation.LineChange{{LineID: line.ID, ResourceID: s.Court.ResourceID, Start: s.Start, End: s.End}}, in.Reason); err != nil {
		return CourtBookingDetail{}, err
	}
	if b.PackageCode == "" && b.FolioID != nil {
		lp, err := m.quote(ctx, tx, property, quoteRequest{Slots: []courtSlot{s}, Channel: "ops"}, pol)
		if err != nil {
			return CourtBookingDetail{}, err
		}
		newTotal, _ := decimal.NewFromString(lp.Total)
		old := decimal.Zero
		if line.Amount != nil {
			old, _ = decimal.NewFromString(*line.Amount)
		}
		if diff := newTotal.Sub(old); diff.IsPositive() {
			net, _ := decimal.NewFromString(lp.Net)
			tax, _ := decimal.NewFromString(lp.Tax)
			oldNet := old
			if tax.IsPositive() && newTotal.IsPositive() {
				oldNet = old.Mul(net).Div(newTotal).Round(0)
			}
			dn := net.Sub(oldNet)
			dt := diff.Sub(dn)
			loc := calendar.Location(ctx, tx)
			if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *b.FolioID, ReferenceType: "reservation.line",
				ReferenceID: &line.ID, Description: "Selisih pindah ke " + s.Court.Name + " " + s.Start.In(loc).Format("2 Jan 15:04"), Net: dn, Tax: dt, Total: diff},
				BusinessLine: billing.LineSport, RevenueComponent: "court", CostCenter: s.Court.costCenter()}); err != nil {
				return CourtBookingDetail{}, err
			}
			if err := m.Res.SetLinePrice(ctx, tx, line.ID, nil, newTotal); err != nil {
				return CourtBookingDetail{}, err
			}
		}
	}
	out, err := m.detail(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, m.live(ctx, tx, property, "moved", rid)
}

// ExtendInput adds hours after a line (overtime).
type ExtendInput struct {
	LineID uuid.UUID `json:"lineId"`
	Hours  int       `json:"hours,omitempty" doc:"Default 1"`
}

// ExtendLine adds the next hour(s) of the same court when free, at the rate
// of that hour (FR-68).
func (m *Module) ExtendLine(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in ExtendInput) (CourtBookingDetail, error) {
	b, err := m.lockBooking(ctx, tx, property, rid)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if b.Status != reservation.StatusConfirmed && b.Status != reservation.StatusCheckedIn {
		return CourtBookingDetail{}, errs.Conflict("invalid_status", "only a scheduled or playing booking can be extended")
	}
	var line *CourtBookingLine
	for i := range b.Lines {
		if b.Lines[i].ID == in.LineID {
			line = &b.Lines[i]
		}
	}
	if line == nil || line.CourtID == nil {
		return CourtBookingDetail{}, errs.Validation("invalid_line", "line not found in this booking")
	}
	if line.Status != "confirmed" && line.Status != "checked_in" {
		return CourtBookingDetail{}, errs.Conflict("invalid_line_status", "only a scheduled or playing line can be extended")
	}
	if in.Hours <= 0 {
		in.Hours = 1
	}
	pol, err := m.courtPolicy(ctx, tx, property)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	slots, err := m.normalizeLines(ctx, tx, property, []CourtLine{{CourtID: *line.CourtID, Start: line.End, End: line.End.Add(time.Duration(in.Hours) * time.Hour)}},
		pol, slotOptions{SkipHours: true})
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if taken, err := m.takenSlots(ctx, tx, slots); err != nil {
		return CourtBookingDetail{}, err
	} else if len(taken) > 0 {
		return CourtBookingDetail{}, errs.Conflict("slot_taken", taken[0].Message+" — the court is booked after this hour")
	}
	var reqs []reservation.LineRequest
	for _, s := range slots {
		reqs = append(reqs, reservation.LineRequest{ResourceID: *s.Court.ResourceID, Start: s.Start, End: s.End, Description: s.Court.Name + " (overtime)",
			Attributes: map[string]any{"courtId": s.Court.ID.String(), "facilityId": s.Court.FacilityID.String(), "overtime": true}})
	}
	r, added, err := m.Res.AddLines(ctx, tx, rid, reqs, "overtime")
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if line.Status == "checked_in" {
		if _, err := tx.Exec(ctx, `UPDATE reservation.reservation_lines SET status = 'checked_in' WHERE id = ANY($1)`, added); err != nil {
			return CourtBookingDetail{}, err
		}
	}
	var newLines []reservation.Line
	for _, l := range r.Lines {
		if containsID(added, l.ID) {
			newLines = append(newLines, l)
		}
	}
	if _, _, err := m.chargeLines(ctx, tx, r, slots, newLines, quoteRequest{Slots: slots, Channel: "ops", Customer: r.CustomerID}); err != nil {
		return CourtBookingDetail{}, err
	}
	out, err := m.detail(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, m.live(ctx, tx, property, "extended", rid)
}

// LinesInput selects lines of a booking.
type LinesInput struct {
	LineIDs []uuid.UUID `json:"lineIds,omitempty"`
	Reason  string      `json:"reason,omitempty"`
}

// NoShow marks lines that started without check-in as No-show (FR-60): no
// refund, recorded on the customer.
func (m *Module) NoShow(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in LinesInput) (CourtBookingDetail, error) {
	if _, err := m.lockBooking(ctx, tx, property, rid); err != nil {
		return CourtBookingDetail{}, err
	}
	if _, err := m.Res.NoShowLines(ctx, tx, rid, in.LineIDs, in.Reason); err != nil {
		return CourtBookingDetail{}, err
	}
	out, err := m.detail(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, m.live(ctx, tx, property, "no_show", rid)
}

// ExtraItem is an extra charged to the booking bill.
type ExtraItem struct {
	Code     string `json:"code,omitempty" doc:"Extra of the Court Booking policy"`
	Name     string `json:"name,omitempty" doc:"Free item (with price)"`
	Price    string `json:"price,omitempty"`
	Quantity int    `json:"quantity,omitempty"`
}

// ExtrasInput charges extras.
type ExtrasInput struct {
	Items []ExtraItem `json:"items"`
}

// AddExtras charges rentals and drinks to the bill of a booking in play
// (FR-67): tax as the court rent.
func (m *Module) AddExtras(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in ExtrasInput) (CourtBookingDetail, error) {
	b, err := m.lockBooking(ctx, tx, property, rid)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if b.State != StatePlaying && b.State != StateScheduled && b.State != StateLate {
		return CourtBookingDetail{}, errs.Conflict("invalid_status", "extras are charged to a scheduled or playing booking (is "+b.State+")")
	}
	if len(in.Items) == 0 {
		return CourtBookingDetail{}, handle.Invalid("items", "required", "choose at least one item")
	}
	pol, err := m.courtPolicy(ctx, tx, property)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	r, err := m.Res.Get(ctx, tx, rid, false)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	folioID, err := m.ensureFolio(ctx, tx, r)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	cc := "sportclub"
	if len(b.Lines) > 0 && b.Lines[0].FacilityCode != nil {
		cc = "sportclub:" + *b.Lines[0].FacilityCode
	}
	for _, it := range in.Items {
		name, price, comp := it.Name, it.Price, "sport_rental"
		if it.Code != "" {
			x, ok := pol.extra(it.Code)
			if !ok {
				return CourtBookingDetail{}, handle.Invalid("items", "unknown_item", "unknown extra "+it.Code)
			}
			name, price, comp = x.Name, x.Price, nonEmpty(x.Component, "sport_rental")
		}
		p, err := handle.Decimal("price", price, decimal.Zero)
		if err != nil || !p.IsPositive() || strings.TrimSpace(name) == "" {
			return CourtBookingDetail{}, handle.Invalid("items", "invalid_item", "each item needs a name and a price")
		}
		qty := max(it.Quantity, 1)
		lp, err := m.extraTax(ctx, tx, property, p.Mul(decimal.NewFromInt(int64(qty))))
		if err != nil {
			return CourtBookingDetail{}, err
		}
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: folioID, ReferenceType: "sportclub.extra",
			Description: fmt.Sprintf("%s × %d", name, qty), Quantity: decimal.NewFromInt(int64(qty)), UnitPrice: p, Net: lp[0], Tax: lp[1]},
			BusinessLine: billing.LineSport, RevenueComponent: comp, CostCenter: cc}); err != nil {
			return CourtBookingDetail{}, err
		}
	}
	out, err := m.detail(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, m.live(ctx, tx, property, "extras", rid)
}

// extraTax adds the PPN of the Tax & Service master on an extra (net,
// tax), the tax of the court rent (FR-92).
func (m *Module) extraTax(ctx context.Context, tx pgx.Tx, property uuid.UUID, net decimal.Decimal) ([2]decimal.Decimal, error) {
	now := clock.Now()
	rules, err := commercial.RulesAt(ctx, tx, property, now)
	if err != nil {
		return [2]decimal.Decimal{}, err
	}
	b := commercial.CalculateMode(commercial.WithCodes(rules, []string{"PPN"}), net, "IDR", now, "plus_plus")
	return [2]decimal.Decimal{net, commercial.SumKind(b.Lines, "tax").Add(commercial.SumKind(b.Lines, "service"))}, nil
}

// CompleteInput ends the play of lines (Selesai Main).
type CompleteInput struct {
	LineIDs []uuid.UUID `json:"lineIds,omitempty"`
}

// Complete ends the play (FR-69): the bill must be settled first (split per
// player through several payments); the folio is closed.
func (m *Module) Complete(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in CompleteInput) (CourtBookingDetail, error) {
	b, err := m.lockBooking(ctx, tx, property, rid)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if bal, _ := decimal.NewFromString(b.Balance); bal.IsPositive() {
		return CourtBookingDetail{}, errs.Conflict("unpaid", "settle the bill first: Rp "+fmtMoney(b.Balance)+" still to pay")
	}
	if _, err := m.Res.CompleteLines(ctx, tx, rid, in.LineIDs); err != nil {
		return CourtBookingDetail{}, err
	}
	nb, err := m.bookingByID(ctx, tx, rid, false)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if nb.Status == reservation.StatusCompleted && nb.FolioID != nil {
		if st, err := billing.FolioStatus(ctx, tx, *nb.FolioID); err == nil && st == "open" {
			if err := m.Billing.CloseFolio(ctx, tx, *nb.FolioID); err != nil {
				return CourtBookingDetail{}, err
			}
		}
	}
	out, err := m.detail(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, m.live(ctx, tx, property, "completed", rid)
}

// VoidBooking voids a booking keyed in by mistake (supervisor, reason
// required, audit; FR-129): no refund — a payment already taken stays on
// the folio for Finance.
func (m *Module) VoidBooking(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, reason string) (CourtBookingDetail, error) {
	b, err := m.lockBooking(ctx, tx, property, rid)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return CourtBookingDetail{}, handle.Invalid("reason", "required", "the reason of the void is required")
	}
	for _, l := range b.Lines {
		if l.Status == "checked_in" || l.Status == "completed" {
			return CourtBookingDetail{}, errs.Conflict("already_played", "a booking already played cannot be voided")
		}
	}
	if _, err := m.Res.Void(ctx, tx, rid, reason); err != nil {
		return CourtBookingDetail{}, err
	}
	if b.PackageCode != "" {
		if err := m.restorePackage(ctx, tx, property, b); err != nil {
			return CourtBookingDetail{}, err
		}
	}
	out, err := m.detail(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, m.live(ctx, tx, property, "voided", rid)
}

// restorePackage gives the hours of a voided booking back to its package.
func (m *Module) restorePackage(ctx context.Context, tx pgx.Tx, property uuid.UUID, b CourtBooking) error {
	var vid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM commercial.vouchers WHERE property_id = $1 AND upper(code) = upper($2)`, property, b.PackageCode).Scan(&vid); err != nil {
		return nil //nolint:nilerr // package gone: nothing to restore
	}
	hours := 0
	for _, l := range b.Lines {
		hours += int(l.End.Sub(l.Start) / time.Hour)
	}
	return m.Vouchers.Restore(ctx, tx, vid, decimal.NewFromInt(int64(hours)), "court booking voided", "court-void-"+b.ID.String())
}

// DiscountBooking adds a manual discount of a supervisor (FR-121).
func (m *Module) DiscountBooking(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in DiscountInput) (CourtBookingDetail, error) {
	b, err := m.lockBooking(ctx, tx, property, rid)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if b.FolioID == nil || b.Void {
		return CourtBookingDetail{}, errs.Conflict("nothing_to_discount", "booking "+b.Code+" has no bill to discount")
	}
	bal, _ := decimal.NewFromString(b.Balance)
	amt, err := handle.Decimal("amount", in.Amount, decimal.Zero)
	if err != nil {
		return CourtBookingDetail{}, err
	}
	if amt.GreaterThan(bal) {
		return CourtBookingDetail{}, handle.Invalid("amount", "too_high", "the discount cannot exceed the balance")
	}
	if _, err := m.manualDiscount(ctx, tx, *b.FolioID, in); err != nil {
		return CourtBookingDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: "discount", EntityType: "reservation.reservation", EntityID: rid.String(),
		EntityLabel: b.Code, PropertyID: &property, After: in, Reason: in.Reason}); err != nil {
		return CourtBookingDetail{}, err
	}
	out, err := m.detail(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, m.live(ctx, tx, property, "discount", rid)
}

// ChargeTarget is a court booking in play (or of today) whose bill a café
// order can be charged to (FR-135): the renter, the code and the court.
type ChargeTarget struct {
	ReservationID uuid.UUID `json:"reservationId"`
	BookingCode   string    `json:"bookingCode"`
	Name          string    `json:"name"`
	Courts        string    `json:"courts"`
	State         string    `json:"state"`
	FolioID       uuid.UUID `json:"folioId"`
}

func (m *Module) chargeTargets(ctx context.Context, tx pgx.Tx, property uuid.UUID, q string) ([]ChargeTarget, error) {
	now := clock.Now()
	list, err := m.loadBookings(ctx, tx, property, `AND r.status IN ('confirmed', 'checked_in') AND r.folio_id IS NOT NULL
		AND EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id AND l.period && tstzrange($2, $3, '[)'))
		AND ($4 = '' OR r.code ILIKE '%' || $4 || '%' OR coalesce(c.name, r.guest_name, '') ILIKE '%' || $4 || '%')
		ORDER BY r.checked_in_at DESC NULLS LAST LIMIT 30`, now.Add(-2*time.Hour), now.Add(2*time.Hour), strings.TrimSpace(q))
	out := []ChargeTarget{}
	for _, b := range list {
		var courts []string
		for _, l := range b.Lines {
			courts = append(courts, l.CourtName+" "+l.Start.In(calendar.Location(ctx, tx)).Format("15:04"))
		}
		out = append(out, ChargeTarget{ReservationID: b.ID, BookingCode: b.Code, Name: b.Name, Courts: strings.Join(courts, ", "), State: b.State, FolioID: *b.FolioID})
	}
	return out, err
}
