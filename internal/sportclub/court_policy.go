package sportclub

// Court Booking policy of the Sport Club (docs/requirement-booking-sportclub-
// mgcc.md §9, §12; FR-78): booking window, hold, duration, no-show and
// reminder minutes, the online payment methods with the service fee charged
// to the guest, the extras rented at the desk and the terms shown on the
// website. One versioned club policy (Sport Club Policies) edited from
// Sport Club › Settings; the sports override the duration per facility.

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/rules"
)

// OnlineMethod is an online payment method of the website and Member App
// with the service fee of the payment gateway, charged to the guest (FR-30).
type OnlineMethod struct {
	Code       string `json:"code" doc:"qris, virtual_account, ewallet, card"`
	MethodType string `json:"methodType" enum:"qris,virtual_account,card,payment_gateway" doc:"Billing method type of the gateway payment"`
	Label      string `json:"label"`
	Fee        string `json:"fee" doc:"Fixed service fee (Rupiah)"`
	Percent    string `json:"percent" doc:"Service fee in percent of the amount (cards)"`
	Active     bool   `json:"active"`
}

// FeeFor is the service fee of a method for an amount.
func (o OnlineMethod) FeeFor(amount decimal.Decimal) decimal.Decimal {
	fee, _ := decimal.NewFromString(o.Fee)
	pct, _ := decimal.NewFromString(o.Percent)
	return fee.Add(amount.Mul(pct).Div(decimal.NewFromInt(100))).Round(0)
}

// Extra is an item rented or sold at the desk during play (FR-67).
type Extra struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Price     string `json:"price" doc:"Before tax"`
	Component string `json:"component" doc:"Revenue component (sport_rental, fnb …)"`
}

// CourtPolicy is the Court Booking policy.
type CourtPolicy struct {
	WindowDays      int               `json:"windowDays" doc:"Website and Member App booking window (days ahead)"`
	DeskWindowDays  int               `json:"deskWindowDays" doc:"Front desk booking window (tournaments, communities)"`
	HoldMinutes     int               `json:"holdMinutes" doc:"Online payment time limit (the hold)"`
	MinHours        int               `json:"minHours"`
	MaxHours        int               `json:"maxHours" doc:"Consecutive hours of one court in one booking"`
	NoShowMinutes   int               `json:"noShowMinutes" doc:"Minutes after the start without check-in: Not arrived"`
	ReminderMinutes int               `json:"reminderMinutes" doc:"In-app reminder to the court staff on duty before a booking"`
	CheckInMinutes  int               `json:"checkInMinutes" doc:"Check-in opens this many minutes before the start"`
	ProspectVisits  int               `json:"prospectVisits" doc:"Court bookings in 30 days that flag a non-member as a membership prospect (0 = off)"`
	TaxIncluded     bool              `json:"taxIncluded" doc:"false: the brochure prices are before tax"`
	Methods         []OnlineMethod    `json:"methods"`
	Extras          []Extra           `json:"extras"`
	Terms           map[string]string `json:"terms" doc:"Terms & conditions per language (id, en)"`
}

var defaultCourtPolicy = CourtPolicy{
	WindowDays: 30, DeskWindowDays: 180, HoldMinutes: 15, MinHours: 1, MaxHours: 4, NoShowMinutes: 15, ReminderMinutes: 15, CheckInMinutes: 30,
	ProspectVisits: 4,
	Methods: []OnlineMethod{
		{Code: "qris", MethodType: "qris", Label: "QRIS", Fee: "4000", Percent: "0", Active: true},
		{Code: "virtual_account", MethodType: "virtual_account", Label: "Virtual Account", Fee: "6000", Percent: "0", Active: true},
		{Code: "ewallet", MethodType: "payment_gateway", Label: "E-wallet (OVO, DANA, ShopeePay)", Fee: "5000", Percent: "0", Active: true},
		{Code: "card", MethodType: "card", Label: "Kartu Kredit / Debit", Fee: "0", Percent: "3", Active: true},
	},
	Extras: []Extra{
		{Code: "RACKET", Name: "Sewa raket tenis", Price: "50000", Component: "sport_rental"},
		{Code: "BALL-TENNIS", Name: "Bola tenis (3 pcs)", Price: "45000", Component: "sport_rental"},
		{Code: "BALL-FUTSAL", Name: "Sewa bola futsal", Price: "25000", Component: "sport_rental"},
		{Code: "BALL-BASKET", Name: "Sewa bola basket / voli", Price: "25000", Component: "sport_rental"},
		{Code: "VEST", Name: "Sewa rompi (per set)", Price: "30000", Component: "sport_rental"},
		{Code: "TOWEL", Name: "Handuk", Price: "15000", Component: "sport_rental"},
		{Code: "WATER", Name: "Air mineral", Price: "10000", Component: "fnb"},
		{Code: "ISOTONIC", Name: "Minuman isotonik", Price: "15000", Component: "fnb"},
	},
	Terms: map[string]string{
		"id": "Booking tidak dapat dibatalkan dan tidak ada refund. Reschedule hanya oleh petugas klub. Datang 15 menit sebelum jam main dan scan QR di front desk Sport Club. Wajib sepatu dan pakaian olahraga; dilarang membawa makanan dari luar; pembayaran cashless.",
		"en": "Bookings cannot be cancelled and are not refunded. Only the club staff can reschedule. Arrive 15 minutes early and scan the QR at the Sport Club front desk. Sport shoes and sportswear required; no outside food; cashless payment.",
	},
}

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: "sportclub.court_booking", Category: "Sport Club Policies", Name: "Court booking",
		Description: "Booking window, hold, duration, no-show, reminders, online payment methods and service fees, extras and terms of the Sport Club courts",
		Default:     defaultCourtPolicy})
}

func (m *Module) courtPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (CourtPolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, "sportclub.court_booking", property, defaultCourtPolicy)
	if p.WindowDays <= 0 {
		p.WindowDays = 30
	}
	if p.DeskWindowDays < p.WindowDays {
		p.DeskWindowDays = p.WindowDays
	}
	if p.HoldMinutes <= 0 {
		p.HoldMinutes = 15
	}
	if p.MinHours <= 0 {
		p.MinHours = 1
	}
	if p.MaxHours < p.MinHours {
		p.MaxHours = max(p.MinHours, 4)
	}
	if p.NoShowMinutes <= 0 {
		p.NoShowMinutes = 15
	}
	if p.CheckInMinutes <= 0 {
		p.CheckInMinutes = 30
	}
	return p, err
}

// method returns the active online method of a code (or its method type).
func (p CourtPolicy) method(code string) (OnlineMethod, bool) {
	for _, o := range p.Methods {
		if o.Active && (o.Code == code || o.MethodType == code) {
			return o, true
		}
	}
	return OnlineMethod{}, false
}

func (p CourtPolicy) extra(code string) (Extra, bool) {
	for _, x := range p.Extras {
		if strings.EqualFold(x.Code, code) {
			return x, true
		}
	}
	return Extra{}, false
}

// FacilityRules are a sport's overrides of the policy (facility booking_rules).
type FacilityRules struct {
	MinHours    int    `json:"minHours,omitempty"`
	MaxHours    int    `json:"maxHours,omitempty"`
	EveningFrom string `json:"eveningFrom,omitempty" doc:"Start of the evening band (packages: day / evening hours)"`
}

// FacilityContent is the website content of a sport (FR-08, FR-77).
type FacilityContent struct {
	NameEn      string              `json:"nameEn,omitempty"`
	Slug        string              `json:"slug,omitempty" doc:"URL of the sport: /{lang}/book/sport-club/{slug}"`
	Icon        string              `json:"icon,omitempty"`
	Photos      []string            `json:"photos,omitempty"`
	Description map[string]string   `json:"description,omitempty"`
	Rules       map[string][]string `json:"rules,omitempty"`
	Amenities   []string            `json:"amenities,omitempty"`
	FAQ         []map[string]string `json:"faq,omitempty"`
	SEOTitle    map[string]string   `json:"seoTitle,omitempty"`
}
