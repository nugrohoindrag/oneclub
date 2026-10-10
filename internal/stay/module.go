// Package stay is Stay & Venue (PRD P2 EP-16 Bungalow, EP-17 VIP Suite,
// EP-18 Meeting Room): booking inventory on the Reservation Engine with
// rates from Commercial pricing and folios from Billing — not a PMS.
package stay

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/rules"
	"oneclub/internal/platform/storage"
	"oneclub/internal/reservation"
)

// Module is the Stay & Venue module.
type Module struct {
	DB       *dbtx.DB
	Res      *reservation.Engine
	Billing  *billing.Service
	POS      commercial.POS      // restaurant orders charged to the stay
	Vouchers commercial.Vouchers // voucher & prepaid redemption
	Events   *outbox.Bus
	Notify   notify.Sender  // guest and staff notifications (accommodation)
	Files    *storage.Files // identity photos and signatures of the registration card (FR-H47, FR-H48)
}

func codeField(label string) resource.Field { return resource.Code(label) }

var BungalowTypes = &resource.Def{
	Key: "stay.bungalow_type", Module: "stay", Perm: "stay.bungalow_type", Path: "/api/v1/stay/bungalow-types", Table: "stay.bungalow_types",
	Name: "Bungalow Type", Plural: "Bungalow Types", Tag: "Stay & Venue", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "maxAdults", Column: "max_adults", Label: "Maximum Adults", Kind: resource.Int, Default: int64(2), Min: resource.Min(1)},
		{Name: "maxChildren", Column: "max_children", Label: "Maximum Children", Kind: resource.Int, Default: int64(1), Min: resource.Min(0)},
		{Name: "bedrooms", Column: "bedrooms", Label: "Bedrooms", Kind: resource.Int, Default: int64(1), Min: resource.Min(1)},
		{Name: "facilities", Column: "facilities", Label: "Facilities", Kind: resource.StringList, Default: []string{}},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		{Name: "bedConfiguration", Column: "bed_configuration", Label: "Bed Configuration", Kind: resource.String, Max: 120},
		{Name: "sizeSqm", Column: "size_sqm", Label: "Room Size (m²)", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "view", Column: "view", Label: "View", Kind: resource.String, Max: 60},
		{Name: "photos", Column: "photos", Label: "Photos (image URLs)", Kind: resource.StringList, Default: []string{}},
		{Name: "baseRate", Column: "base_rate", Label: "Base Rate per Night", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "weekendRate", Column: "weekend_rate", Label: "Weekend Rate per Night", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "sortOrder", Column: "sort_order", Label: "Sort Order", Kind: resource.Int, Default: int64(0)},
		{Name: "priceItem", Column: "price_item", Label: "Pricing Item (default: code)", Kind: resource.String, Max: 60, Upper: true},
		// website content (docs/requirement-booking-hotel-mgcc.md FR-H68, FR-H73)
		{Name: "nameEn", Column: "name_en", Label: "Name (English)", Kind: resource.String, Max: 120},
		{Name: "descriptionEn", Column: "description_en", Label: "Description (English)", Kind: resource.Text, Max: 2000},
		{Name: "slug", Column: "slug", Label: "Website Page (slug, e.g. eagle)", Kind: resource.String, Max: 60},
		{Name: "amenities", Column: "amenities", Label: "Amenities by group (bathroom, entertainment, internet, kitchen, general)", Kind: resource.JSONList, Default: "[]"},
		{Name: "faq", Column: "faq", Label: "FAQ", Kind: resource.JSONList, Default: "[]"},
		{Name: "houseRules", Column: "house_rules", Label: "House Rules of the type", Kind: resource.Text, Max: 2000},
		{Name: "onWebsite", Column: "on_website", Label: "Bookable on the Website", Kind: resource.Bool, Default: true},
		resource.Status("active", "inactive")},
}

var Bungalows = &resource.Def{
	Key: "stay.bungalow", Module: "stay", Perm: "stay.bungalow", Path: "/api/v1/stay/bungalows", Table: "stay.bungalows",
	Name: "Bungalow", Plural: "Bungalows", Tag: "Stay & Venue", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "code, id",
	Fields: []resource.Field{codeField("Unit No."), resource.Name(),
		{Name: "typeId", Column: "type_id", Label: "Bungalow Type", Kind: resource.UUID, Required: true, Filter: true,
			Ref: &resource.Ref{Table: "stay.bungalow_types", SameProperty: true, Label: "bungalow type"}},
		{Name: "view", Column: "view", Label: "View", Kind: resource.Enum, Enum: []string{"golf", "lake", "pool", "garden", "other"}, Filter: true},
		{Name: "venueId", Column: "venue_id", Label: "Venue", Kind: resource.UUID, Ref: &resource.Ref{Table: "platform.venues", SameProperty: true, Label: "venue"}},
		{Name: "readiness", Column: "readiness", Label: "Readiness", Kind: resource.Enum, Enum: []string{"ready", "not_ready"}, Default: "ready", Filter: true},
		{Name: "location", Column: "location", Label: "Location", Kind: resource.String, Max: 120},
		{Name: "capacity", Column: "capacity", Label: "Capacity (default: room type)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "hkStatus", Column: "hk_status", Label: "Housekeeping Status", Kind: resource.Enum, Enum: HKStatuses, ReadOnly: true, Filter: true},
		{Name: "smoking", Column: "smoking", Label: "Smoking allowed", Kind: resource.Bool, Default: false},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 1000},
		{Name: "resourceId", Column: "resource_id", Label: "Bookable Resource", Kind: resource.UUID, ReadOnly: true},
		resource.Status("active", "inactive")},
}

var VIPSuites = &resource.Def{
	Key: "stay.vip_suite", Module: "stay", Perm: "stay.vip_suite", Path: "/api/v1/stay/vip-suites", Table: "stay.vip_suites",
	Name: "VIP Suite", Plural: "VIP Suites", Tag: "Stay & Venue", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "facilities", Column: "facilities", Label: "Facilities", Kind: resource.StringList, Default: []string{}},
		{Name: "blockHours", Column: "block_hours", Label: "Block (hours)", Kind: resource.Int, Default: int64(8), Min: resource.Min(1)},
		{Name: "priceItem", Column: "price_item", Label: "Pricing Item (default: code)", Kind: resource.String, Max: 60, Upper: true},
		{Name: "resourceId", Column: "resource_id", Label: "Bookable Resource", Kind: resource.UUID, ReadOnly: true},
		resource.Status("active", "inactive")},
}

var MeetingRooms = &resource.Def{
	Key: "stay.meeting_room", Module: "stay", Perm: "stay.meeting_room", Path: "/api/v1/stay/meeting-rooms", Table: "stay.meeting_rooms",
	Name: "Meeting Room", Plural: "Meeting Rooms", Tag: "Stay & Venue", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "sizeSqm", Column: "size_sqm", Label: "Size (m²)", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "facilities", Column: "facilities", Label: "Facilities", Kind: resource.StringList, Default: []string{}},
		{Name: "priceItem", Column: "price_item", Label: "Pricing Item (default: code)", Kind: resource.String, Max: 60, Upper: true},
		{Name: "resourceId", Column: "resource_id", Label: "Bookable Resource", Kind: resource.UUID, ReadOnly: true},
		resource.Status("active", "inactive")},
}

var RoomLayouts = &resource.Def{
	Key: "stay.room_layout", Module: "stay", Perm: "stay.meeting_room", Path: "/api/v1/stay/room-layouts", Table: "stay.room_layouts",
	Name: "Room Layout", Plural: "Room Layouts", Tag: "Stay & Venue", PropertyScoped: true, OrderBy: "meeting_room_id, capacity",
	Fields: []resource.Field{
		{Name: "meetingRoomId", Column: "meeting_room_id", Label: "Meeting Room", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "stay.meeting_rooms", SameProperty: true, Label: "meeting room"}},
		{Name: "layout", Column: "layout", Label: "Layout", Kind: resource.Enum, Required: true, CreateOnly: true,
			Enum: []string{"round_table", "classroom", "u_shape", "theater", "boardroom", "cocktail"}},
		{Name: "capacity", Column: "capacity", Label: "Capacity (pax)", Kind: resource.Int, Required: true, Min: resource.Min(1)}},
}

var Equipment = &resource.Def{
	Key: "stay.equipment", Module: "stay", Perm: "stay.equipment", Path: "/api/v1/stay/equipment", Table: "stay.equipment",
	Name: "Equipment", Plural: "Equipment", Tag: "Stay & Venue", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "quantity", Column: "quantity", Label: "Quantity Available", Kind: resource.Int, Required: true, Min: resource.Min(1)},
		{Name: "priceItem", Column: "price_item", Label: "Pricing Item (default: code)", Kind: resource.String, Max: 60, Upper: true},
		{Name: "resourceId", Column: "resource_id", Label: "Bookable Resource", Kind: resource.UUID, ReadOnly: true},
		resource.Status("active", "inactive")},
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func uuidPtr(v any) *uuid.UUID {
	if u, err := uuid.Parse(str(v)); err == nil {
		return &u
	}
	return nil
}

func intPtr(v any) *int {
	if n, ok := v.(int64); ok {
		x := int(n)
		return &x
	}
	return nil
}

func activeOr(s string) string {
	if s == "active" {
		return "active"
	}
	return "inactive"
}

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

func nzs(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// unitHook keeps the reservation resource of a unit in sync.
func (m *Module) unitHook(table, prefix, rtype string, capacity func(map[string]any) *int, item func(context.Context, pgx.Tx, map[string]any) string) resource.Hooks {
	sync := func(ctx context.Context, tx pgx.Tx, row map[string]any) error {
		pid, _ := reqctx.Property(ctx)
		var c *int
		if capacity != nil {
			c = capacity(row)
		}
		it := str(row["priceItem"])
		if item != nil {
			it = item(ctx, tx, row)
		}
		if it == "" {
			it = str(row["code"])
		}
		rid, err := m.Res.EnsureResource(ctx, tx, uuidPtr(row["resourceId"]), reservation.ResourceRequest{PropertyID: pid, Code: prefix + str(row["code"]),
			Name: str(row["name"]), ResourceType: rtype, VenueID: uuidPtr(row["venueId"]), Capacity: c, Status: activeOr(str(row["status"])),
			Attributes: map[string]any{"priceItem": it, "unitId": str(row["id"])}})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE `+table+` SET resource_id = $2 WHERE id = $1`, row["id"], rid)
		return err
	}
	return resource.Hooks{
		AfterCreate: func(ctx context.Context, tx pgx.Tx, row map[string]any) error { return sync(ctx, tx, row) },
		AfterUpdate: func(ctx context.Context, tx pgx.Tx, _, after map[string]any) error { return sync(ctx, tx, after) },
	}
}

func (m *Module) hooks() {
	Bungalows.Hooks = m.unitHook("stay.bungalows", "BGL-", "bungalow", nil, func(ctx context.Context, tx pgx.Tx, row map[string]any) string {
		var code string
		var item *string
		_ = tx.QueryRow(ctx, `SELECT code, price_item FROM stay.bungalow_types WHERE id = $1`, row["typeId"]).Scan(&code, &item)
		if item != nil && *item != "" {
			return *item
		}
		return code
	})
	VIPSuites.Hooks = m.unitHook("stay.vip_suites", "VIP-", "vip_suite", nil, nil)
	MeetingRooms.Hooks = m.unitHook("stay.meeting_rooms", "MTG-", "meeting_room", nil, nil)
	Equipment.Hooks = m.unitHook("stay.equipment", "EQP-", "equipment", func(row map[string]any) *int { return intPtr(row["quantity"]) }, nil)
}

// Policy is the Stay Policies document (FR-POL-P2-02, label proposed in PRD §7.6).
type Policy struct {
	CheckInTime              string `json:"checkInTime"`
	CheckOutTime             string `json:"checkOutTime"`
	DepositPercent           string `json:"depositPercent"`
	LateCheckoutFeePerHour   string `json:"lateCheckoutFeePerHour"`
	LateCheckoutGraceMinutes int    `json:"lateCheckoutGraceMinutes"`
	RequireReadyUnit         bool   `json:"requireReadyUnit"`
	RoomChargePosting        string `json:"roomChargePosting" enum:"at_booking,nightly" doc:"Bungalow room charge: posted for the whole stay at booking, or one night at a time by the night audit (the nights left at check-out) (PRD P3 FR-EOD-03)"`
	AutoNoShow               bool   `json:"autoNoShow" doc:"The night audit marks Reserved stays that did not arrive by the business date as No-show"`
	// Accommodation (requirements §12, §17, §18, §20, §26)
	WeekendNights        string `json:"weekendNights" doc:"Nights priced at the weekend rate, ISO weekdays of the night (5 = Friday night, 6 = Saturday night)"`
	LateCheckoutMode     string `json:"lateCheckoutMode" enum:"hourly,tiered" doc:"hourly: the fee per started hour after the grace; tiered: one late check-out fee until the cut-off, after it an additional night"`
	LateCheckoutUntil    string `json:"lateCheckoutUntil" doc:"Tiered: the late check-out fee applies until this time (HH:MM)"`
	LateCheckoutFee      string `json:"lateCheckoutFee" doc:"Tiered: the late check-out fee"`
	AfterCutoffCharge    string `json:"afterCutoffCharge" doc:"Tiered: after the cut-off, night charges one more night; an amount charges that amount"`
	EarlyCheckInFrom     string `json:"earlyCheckInFrom" doc:"Earliest early check-in time (HH:MM)"`
	EarlyCheckInFee      string `json:"earlyCheckInFee" doc:"Early check-in fee posted to the folio (0 = free)"`
	InspectionRequired   bool   `json:"inspectionRequired" doc:"A cleaned bungalow must pass an inspection before it is Ready"`
	ReadyAfterInspection bool   `json:"readyAfterInspection" doc:"A passed inspection makes the bungalow Ready at once (otherwise it stays Inspected until released)"`
	StayoverCleaning     bool   `json:"stayoverCleaning" doc:"The daily housekeeping plan adds a stayover cleaning for every occupied bungalow"`
	HouseRules           string `json:"houseRules" doc:"House rules shown to the guest (My Stay)"`
	PropertyInfo         string `json:"propertyInfo" doc:"Property information shown to the guest (My Stay): Wi-Fi, facilities, contacts"`
	// Booking engine of the website and Member App (docs/requirement-booking-hotel-mgcc.md §9)
	WebsiteHoldMinutes   int    `json:"websiteHoldMinutes" doc:"Website / Member App: the bungalows are held this long while the guest pays online (FR-H35)"`
	BookingWindowDays    int    `json:"bookingWindowDays" doc:"How far ahead a guest may book on the website / Member App (days)"`
	OnlineMethods        string `json:"onlineMethods" doc:"Online payment methods offered on the website, comma separated: qris, virtual_account, card (mock gateway while the real one is on hold)"`
	Terms                string `json:"terms" doc:"Terms & Conditions of a bungalow booking (Bahasa Indonesia)"`
	TermsEn              string `json:"termsEn" doc:"Terms & Conditions (English)"`
	HouseRulesEn         string `json:"houseRulesEn" doc:"House rules (English)"`
	ChildPolicy          string `json:"childPolicy" doc:"Children: free age, extra bed"`
	ContactAddress       string `json:"contactAddress" doc:"Property address on the website footer, e-voucher and registration card (FR-H06); default: the property address"`
	ContactPhone         string `json:"contactPhone"`
	ContactEmail         string `json:"contactEmail"`
	ContactWhatsApp      string `json:"contactWhatsApp"`
	MapURL               string `json:"mapUrl" doc:"Map link of the club on the confirmation"`
	KeysPerBungalow      int    `json:"keysPerBungalow" doc:"Keys / key cards handed over at check-in (FR-H49)"`
	DepositBeforeCheckIn bool   `json:"depositBeforeCheckIn" doc:"Check-in needs the deposit of the rate plan paid (a supervisor may accept it later with a reason)"`
	RoomChargeLimit      string `json:"roomChargeLimit" doc:"Charge to room (POS, front desk) is refused when the unpaid folio would exceed this amount (0 = no limit)"`
}

var defaultPolicy = Policy{CheckInTime: "14:00", CheckOutTime: "12:00", DepositPercent: "50", LateCheckoutFeePerHour: "100000",
	LateCheckoutGraceMinutes: 30, RequireReadyUnit: true, RoomChargePosting: "nightly", AutoNoShow: true,
	WeekendNights: "5,6", LateCheckoutMode: "tiered", LateCheckoutUntil: "15:00", LateCheckoutFee: "250000", AfterCutoffCharge: "night",
	EarlyCheckInFrom: "10:00", EarlyCheckInFee: "150000", InspectionRequired: true, ReadyAfterInspection: true, StayoverCleaning: true,
	HouseRules:         "Check-in from 14:00, check-out until 12:00. No smoking inside the bungalow. Quiet hours 22:00-06:00. Pets are not allowed.",
	PropertyInfo:       "Front Office is open 24 hours. Breakfast 06:30-10:00 at the clubhouse restaurant.",
	WebsiteHoldMinutes: 15, BookingWindowDays: 365, OnlineMethods: "qris,virtual_account,card", KeysPerBungalow: 2, RoomChargeLimit: "0",
	Terms: "Check-in mulai pukul 14:00 dan check-out paling lambat pukul 12:00. Early check-in dan late check-out mengikuti ketersediaan dan dikenai biaya. " +
		"Deposit/jaminan diambil saat check-in. Tamu wajib menunjukkan KTP/paspor yang berlaku. Dilarang merokok di dalam bungalow dan membawa hewan peliharaan. " +
		"Pembatalan mengikuti kebijakan rate plan yang dipilih.",
	TermsEn: "Check-in from 14:00, check-out until 12:00. Early check-in and late check-out depend on availability and are charged. A deposit is taken at " +
		"check-in. A valid ID card or passport is required. No smoking inside the bungalow and no pets. Cancellation follows the policy of the chosen rate plan."}

func (m *Module) policy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (Policy, rules.PolicyRef, error) {
	return rules.PolicyAt(ctx, q, "stay.policy", property, defaultPolicy)
}

// Contribution returns catalogue entries.
func Contribution() catalog.Contribution {
	defs := []*resource.Def{BungalowTypes, Bungalows, VIPSuites, MeetingRooms, Equipment}
	all := append(defs, accommodationDefs...)
	perms := resource.Permissions(all...)
	perms = append(perms, catalog.P("stay", "stay", "view", "create", "update", "check_in", "check_out", "cancel")...)
	perms = append(perms, accommodationPermissions()...)
	front := []string{"stay.bungalow_type.view", "stay.bungalow.view", "stay.vip_suite.view", "stay.meeting_room.view", "stay.equipment.view",
		"stay.stay.view", "stay.stay.create", "stay.stay.update", "stay.stay.check_in", "stay.stay.check_out", "stay.stay.cancel"}
	mgr := append(append(resource.AllActions(all...), front...), accommodationManager...)
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"property_admin":        mgr,
			"resort_manager":        mgr,
			"accommodation_manager": mgr,
			"front_desk":            append(append(front, "stay.bungalow.update"), accommodationFrontOffice...),
			"reservation_staff":     append(append(front, "stay.module.access"), accommodationReservations...),
			"housekeeping":          accommodationHousekeeping,
			"maintenance":           accommodationMaintenance,
			"general_manager":       append([]string{"stay.bungalow.view", "stay.vip_suite.view", "stay.meeting_room.view", "stay.stay.view"}, accommodationViewer...),
			"accountant":            {"stay.stay.view", "stay.dashboard.view"},
			"banquet_manager":       {"stay.meeting_room.view", "stay.equipment.view", "stay.stay.view", "stay.stay.create", "stay.stay.update"},
			"banquet_sales":         {"stay.meeting_room.view", "stay.equipment.view", "stay.stay.view", "stay.stay.create"},
			"cashier":               {"stay.stay.view", "stay.stay.charge", "stay.addon.view", "stay.module.access", "stay.stay.charge_order"},
			"pos_staff":             {"stay.stay.view", "stay.stay.charge_order"},
			"outlet_manager":        {"stay.stay.view", "stay.stay.charge_order"},
		},
	}
}
