package golf

// The front desk bill of a booking (demo feedback 9 Oct 2026): the balance
// is paid at the start, at the end or in part, by one payer for everyone,
// split per player (each player pays their share, one payment each) or
// merged with other bookings into one bill paid with one tender. Payments
// stay on the booking folio; a player's payment carries the reference
// "player:<id>" so the bill knows whose share it settled.

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
)

const playerRef = "player:"

// PlayerShare is what one player owes on the booking bill.
type PlayerShare struct {
	PlayerID uuid.UUID `json:"playerId"`
	Name     string    `json:"name"`
	Share    string    `json:"share" doc:"Own charges (round, caddy) plus an equal part of the shared ones (golf carts, F&B)"`
	Paid     string    `json:"paid"`
	Due      string    `json:"due"`
}

// BookingBill is the bill of a booking at the front desk.
type BookingBill struct {
	BookingID   uuid.UUID           `json:"bookingId"`
	Code        string              `json:"code"`
	LocalTime   string              `json:"localTime"`
	ContactName string              `json:"contactName"`
	Status      string              `json:"status"`
	FolioID     *uuid.UUID          `json:"folioId"`
	Charges     string              `json:"charges"`
	Paid        string              `json:"paid"`
	Balance     string              `json:"balance"`
	Players     []PlayerShare       `json:"players"`
	Lines       []billing.FolioLine `json:"lines"`
	Payments    []billing.Payment   `json:"payments"`
}

// Bill builds the bill of a booking with the share of every player.
func (m *Module) Bill(ctx context.Context, q dbtx.Querier, property, bid uuid.UUID) (BookingBill, error) {
	b, err := GetBooking(ctx, q, bid)
	if err != nil {
		return BookingBill{}, err
	}
	var p uuid.UUID
	if err := q.QueryRow(ctx, `SELECT property_id FROM golf.bookings WHERE id = $1`, bid).Scan(&p); err != nil || p != property {
		return BookingBill{}, errs.NotFound("booking")
	}
	out := BookingBill{BookingID: b.ID, Code: b.Code, LocalTime: b.LocalTime, ContactName: b.ContactName, Status: b.Status, FolioID: b.FolioID,
		Charges: "0", Paid: "0", Balance: "0", Players: []PlayerShare{}, Lines: []billing.FolioLine{}, Payments: []billing.Payment{}}
	if b.FolioID == nil {
		return out, nil
	}
	f, err := billing.GetFolio(ctx, q, *b.FolioID)
	if err != nil {
		return out, err
	}
	out.Charges, out.Paid, out.Balance, out.Lines, out.Payments = f.Summary.Charges, f.Summary.Payments, f.Summary.Balance, f.Lines, f.Payments
	var players []Player
	for _, pl := range b.Players {
		if pl.Status != "removed" && pl.Status != "cancelled" {
			players = append(players, pl)
		}
	}
	if len(players) == 0 {
		return out, nil
	}
	own := map[uuid.UUID]decimal.Decimal{}
	common := decimal.Zero
	for _, l := range f.Lines {
		if l.VoidedAt != nil {
			continue
		}
		t := dec(l.Total)
		if l.ReferenceType != nil && *l.ReferenceType == "golf_player" && l.ReferenceID != nil {
			if _, ok := own[*l.ReferenceID]; ok || isPlayer(players, *l.ReferenceID) {
				own[*l.ReferenceID] = own[*l.ReferenceID].Add(t)
				continue
			}
		}
		common = common.Add(t)
	}
	// payments tagged per player; the rest (and applied deposits) pays for everyone
	tagged := map[uuid.UUID]decimal.Decimal{}
	taggedSum := decimal.Zero
	for _, pay := range f.Payments {
		if pay.Status != "completed" || pay.Reference == nil {
			continue
		}
		if pid, ok := refPlayer(*pay.Reference); ok {
			amt := dec(pay.Amount)
			tagged[pid] = tagged[pid].Add(amt)
			taggedSum = taggedSum.Add(amt)
		}
	}
	untagged := dec(f.Summary.Payments).Sub(taggedSum)
	n := decimal.NewFromInt(int64(len(players)))
	part := common.Div(n).Floor()
	rest := common.Sub(part.Mul(n))
	for i, pl := range players {
		share := own[pl.ID].Add(part)
		if i == 0 {
			share = share.Add(rest)
		}
		paid := tagged[pl.ID]
		due := share.Sub(paid)
		if untagged.IsPositive() && due.IsPositive() {
			use := decimal.Min(untagged, due)
			paid, due, untagged = paid.Add(use), due.Sub(use), untagged.Sub(use)
		}
		if due.IsNegative() {
			due = decimal.Zero
		}
		out.Players = append(out.Players, PlayerShare{PlayerID: pl.ID, Name: pl.Name, Share: share.StringFixed(0), Paid: paid.StringFixed(0), Due: due.StringFixed(0)})
	}
	return out, nil
}

func isPlayer(ps []Player, id uuid.UUID) bool {
	for _, p := range ps {
		if p.ID == id {
			return true
		}
	}
	return false
}

func refPlayer(ref string) (uuid.UUID, bool) {
	if !strings.HasPrefix(ref, playerRef) {
		return uuid.Nil, false
	}
	s := strings.TrimPrefix(ref, playerRef)
	if len(s) > 36 {
		s = s[:36]
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

var deskMethods = map[string]bool{"cash": true, "card": true, "qris": true, "bank_transfer": true, "member_account": true}

// BillPayRequest pays a booking bill at the front desk.
type BillPayRequest struct {
	PlayerIDs  []uuid.UUID `json:"playerIds,omitempty" doc:"Split bill: pay the share of these players (one payment per player)"`
	Amount     string      `json:"amount,omitempty" doc:"Without players: any amount up to the balance (default the whole balance)"`
	MethodType string      `json:"methodType" enum:"cash,card,qris,bank_transfer,member_account"`
	Reference  string      `json:"reference,omitempty" doc:"EDC approval / transfer reference"`
	PayerName  string      `json:"payerName,omitempty"`
	CustomerID *uuid.UUID  `json:"customerId,omitempty" doc:"Member Account: the member whose account is charged (default the booker)"`
}

// PayBill takes a payment for the whole bill, part of it or the share of
// players.
func (m *Module) PayBill(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID, req BillPayRequest) (BookingBill, error) {
	if !deskMethods[req.MethodType] {
		return BookingBill{}, errs.Validation("invalid_method", "choose cash, card, QRIS, bank transfer or member account",
			errs.Field("methodType", "invalid", "cash, card, qris, bank_transfer, member_account"))
	}
	b, err := lockBooking(ctx, tx, property, bid)
	if err != nil {
		return BookingBill{}, err
	}
	// Member Account: charged to the member's account (on the statement)
	var account *uuid.UUID
	if req.MethodType == "member_account" {
		cust := req.CustomerID
		if cust == nil {
			cust = b.CustomerID
		}
		if cust == nil {
			return BookingBill{}, errs.Validation("member_required", "choose the member whose account is charged", errs.Field("customerId", "required", "a member"))
		}
		a, err := billing.AccountFor(ctx, tx, property, *cust, "member")
		if err != nil {
			return BookingBill{}, err
		}
		if a == nil || a.Status != "active" {
			return BookingBill{}, errs.Conflict("no_member_account", "this customer has no active member account")
		}
		account = &a.ID
	}
	bill, err := m.Bill(ctx, tx, property, bid)
	if err != nil {
		return bill, err
	}
	if bill.FolioID == nil || !dec(bill.Balance).IsPositive() {
		return bill, errs.Conflict("nothing_due", "the bill has no balance to pay")
	}
	pay := func(amount decimal.Decimal, ref, payer string) error {
		if r := strings.TrimSpace(req.Reference); r != "" {
			ref = strings.TrimSpace(ref + " " + r)
		}
		if payer == "" {
			payer = req.PayerName
		}
		channel := "venue"
		if account != nil {
			channel = "member_account"
		}
		_, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: bill.FolioID, AccountID: account, MethodType: req.MethodType, Channel: channel, Amount: amount,
			Reference: ref, PayerName: payer, Description: "Golf booking " + bill.Code})
		return err
	}
	if len(req.PlayerIDs) > 0 {
		paid := 0
		for _, pid := range req.PlayerIDs {
			for _, s := range bill.Players {
				if s.PlayerID != pid {
					continue
				}
				due := dec(s.Due)
				if !due.IsPositive() {
					continue
				}
				if err := pay(due, playerRef+pid.String(), s.Name); err != nil {
					return bill, err
				}
				paid++
			}
		}
		if paid == 0 {
			return bill, errs.Conflict("nothing_due", "these players have nothing left to pay")
		}
	} else {
		amount := dec(bill.Balance)
		if strings.TrimSpace(req.Amount) != "" {
			amount = dec(req.Amount)
			if !amount.IsPositive() || amount.GreaterThan(dec(bill.Balance)) {
				return bill, errs.Validation("invalid_amount", "the amount must be above zero and at most the balance", errs.Field("amount", "invalid", "1 – "+bill.Balance))
			}
		}
		if err := pay(amount, "", ""); err != nil {
			return bill, err
		}
	}
	return m.Bill(ctx, tx, property, bid)
}

// CombinedPayRequest pays several bookings as one bill.
type CombinedPayRequest struct {
	BookingIDs []uuid.UUID `json:"bookingIds" doc:"The bookings merged into one bill"`
	MethodType string      `json:"methodType" enum:"cash,card,qris,bank_transfer,member_account"`
	Reference  string      `json:"reference,omitempty"`
	PayerName  string      `json:"payerName,omitempty" doc:"Who pays the merged bill"`
	CustomerID *uuid.UUID  `json:"customerId,omitempty" doc:"Member Account: the member whose account is charged"`
}

// PayCombined settles the balance of every booking with one tender.
func (m *Module) PayCombined(ctx context.Context, tx pgx.Tx, property uuid.UUID, req CombinedPayRequest) ([]BookingBill, error) {
	if len(req.BookingIDs) < 2 {
		return nil, errs.Validation("bookings_required", "choose at least two bookings to merge", errs.Field("bookingIds", "required", "≥ 2 bookings"))
	}
	var codes []string
	var bills []BookingBill
	for _, bid := range req.BookingIDs {
		if _, err := lockBooking(ctx, tx, property, bid); err != nil {
			return nil, err
		}
		b, err := m.Bill(ctx, tx, property, bid)
		if err != nil {
			return nil, err
		}
		codes = append(codes, b.Code)
		bills = append(bills, b)
	}
	ref := strings.TrimSpace("merged:" + strings.Join(codes, ",") + " " + req.Reference)
	out := make([]BookingBill, 0, len(bills))
	for _, b := range bills {
		if b.FolioID != nil && dec(b.Balance).IsPositive() {
			if _, err := m.PayBill(ctx, tx, property, b.BookingID, BillPayRequest{MethodType: req.MethodType, Reference: ref, PayerName: req.PayerName, CustomerID: req.CustomerID}); err != nil {
				return nil, err
			}
		}
		nb, err := m.Bill(ctx, tx, property, b.BookingID)
		if err != nil {
			return nil, err
		}
		out = append(out, nb)
	}
	return out, nil
}

func (m *Module) billHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) { return m.Bill(ctx, tx, prop(ctx), bid) })
}

func (m *Module) payBillHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[BillPayRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return m.PayBill(ctx, tx, prop(ctx), bid, req) })
}

func (m *Module) payCombinedHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[CombinedPayRequest](w, r)
	if !ok {
		return
	}
	m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		out, err := m.PayCombined(ctx, tx, prop(ctx), req)
		return httpx.Page[BookingBill]{Items: out}, err
	})
}
