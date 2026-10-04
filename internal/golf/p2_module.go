package golf

// PRD P2 Complete Golf Experience on P1's Golf Core: master data, policies,
// approval document types and the catalogue of EP-05 … EP-13.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/docno"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/realtime"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/rules"
)

func p2code(label string) resource.Field {
	return resource.Field{Name: "code", Column: "code", Label: label, Kind: resource.String, Required: true, Max: 20, Pattern: numRe,
		PatternMsg: "1–20 characters", Search: true}
}

// CaddyLevels (FR-CDL-01): fee per round and promotion thresholds.
var CaddyLevels = &resource.Def{
	Key: "golf.caddy_level", Module: "golf", Perm: "golf.caddy_level", Path: "/api/v1/golf/caddy-levels", Table: "golf.caddy_levels",
	Name: "Caddy Level", Plural: "Caddy Levels", Tag: "Caddies", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "rank, id",
	Fields: []resource.Field{p2code("Code"), resource.Name(),
		{Name: "rank", Column: "rank", Label: "Rank (1 = entry)", Kind: resource.Int, Default: int64(1), Min: resource.Min(1)},
		{Name: "feeAmount", Column: "fee_amount", Label: "Caddy Fee per Round", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "minRounds", Column: "min_rounds", Label: "Promotion: Minimum Rounds", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "minRating", Column: "min_rating", Label: "Promotion: Minimum Rating", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "maxIncidents", Column: "max_incidents", Label: "Promotion: Maximum Incidents (12 months)", Kind: resource.Int, Min: resource.Min(0)},
		resource.Status("active", "inactive")},
}

// CartChecklists (FR-CTL-01): pre-op, post-op and release inspections.
var CartChecklists = &resource.Def{
	Key: "golf.cart_checklist", Module: "golf", Perm: "golf.golf_cart", Path: "/api/v1/golf/golf-cart-checklists", Table: "golf.cart_checklists",
	Name: "Golf Cart Inspection Checklist", Plural: "Golf Cart Inspection Checklists", Tag: "Golf Carts", PropertyScoped: true, Archive: true,
	CodeField: "code", OrderBy: "code, id",
	Fields: []resource.Field{p2code("Code"), resource.Name(),
		{Name: "cartType", Column: "cart_type", Label: "Cart Type", Kind: resource.Enum, Enum: []string{"electric", "gasoline", "other"}, Default: "electric", Filter: true},
		{Name: "inspectionKind", Column: "inspection_kind", Label: "Inspection", Kind: resource.Enum, Enum: []string{"pre_op", "post_op", "release"}, Required: true, Filter: true},
		{Name: "items", Column: "items", Label: "Checklist Items", Kind: resource.StringList, Required: true},
		resource.Status("active", "inactive")},
}

// RangeBays (FR-RNG-01).
var RangeBays = &resource.Def{
	Key: "golf.range_bay", Module: "golf", Perm: "golf.range_bay", Path: "/api/v1/golf/range-bays", Table: "golf.range_bays",
	Name: "Driving Range Bay", Plural: "Driving Range Bays", Tag: "Driving Range", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "code, id",
	Fields: []resource.Field{p2code("Bay No."), resource.Name(),
		{Name: "area", Column: "area", Label: "Area", Kind: resource.Enum, Enum: []string{"indoor", "outdoor"}, Default: "outdoor", Filter: true},
		{Name: "bayType", Column: "bay_type", Label: "Bay Type", Kind: resource.String, Max: 40, Default: "standard"},
		{Name: "tier", Column: "tier", Label: "Tier / Floor", Kind: resource.Int, Default: int64(1), Min: resource.Min(1)},
		{Name: "readiness", Column: "readiness", Label: "Bay Status", Kind: resource.Enum, Enum: []string{"available", "occupied", "maintenance"}, Default: "available", Filter: true},
		{Name: "resourceId", Column: "resource_id", Label: "Bookable Resource", Kind: resource.UUID, ReadOnly: true},
		resource.Status("active", "inactive")},
}

// ReciprocalClubs (FR-RCP-01): agreement, quota and settlement.
var ReciprocalClubs = &resource.Def{
	Key: "golf.reciprocal_club", Module: "golf", Perm: "golf.reciprocal_club", Path: "/api/v1/golf/reciprocal-clubs", Table: "golf.reciprocal_clubs",
	Name: "Reciprocal Club", Plural: "Reciprocal Clubs", Tag: "Reciprocal Club", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "country, name, id",
	Fields: []resource.Field{p2code("Code"), resource.Name(),
		{Name: "country", Column: "country", Label: "Country", Kind: resource.String, Required: true, Max: 60, Filter: true},
		{Name: "city", Column: "city", Label: "City", Kind: resource.String, Max: 80},
		{Name: "contactName", Column: "contact_name", Label: "Contact", Kind: resource.String, Max: 120},
		{Name: "contactEmail", Column: "contact_email", Label: "Contact E-mail", Kind: resource.Email, Max: 254},
		{Name: "contactPhone", Column: "contact_phone", Label: "Contact Phone", Kind: resource.String, Max: 40},
		{Name: "agreementFrom", Column: "agreement_from", Label: "Agreement From", Kind: resource.Date},
		{Name: "agreementTo", Column: "agreement_to", Label: "Agreement Until", Kind: resource.Date},
		{Name: "visitQuota", Column: "visit_quota", Label: "Visit Quota per Period", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "quotaPeriod", Column: "quota_period", Label: "Quota Period", Kind: resource.Enum, Enum: []string{"month", "year"}, Default: "year"},
		{Name: "settlementMode", Column: "settlement_mode", Label: "Club Settlement", Kind: resource.Enum, Enum: []string{"none", "periodic"}, Default: "none"},
		resource.Status("active", "inactive")},
}

// HallOfFame entries (FR-HOF-01/02); automatic entries come from verified
// Hole-in-Ones and finalised scorecards.
var HallOfFame = &resource.Def{
	Key: "golf.hall_of_fame", Module: "golf", Perm: "golf.hall_of_fame", Path: "/api/v1/golf/hall-of-fame", Table: "golf.hall_of_fame",
	Name: "Hall of Fame Entry", Plural: "Hall of Fame", Tag: "Hall of Fame", PropertyScoped: true, Archive: true, OrderBy: "achieved_on DESC NULLS LAST, id",
	Fields: []resource.Field{
		{Name: "category", Column: "category", Label: "Category", Kind: resource.Enum, Required: true, Filter: true,
			Enum: []string{"hole_in_one", "club_champion", "course_record", "albatross", "eagle", "tournament_champion", "club_history"}},
		{Name: "title", Column: "title", Label: "Title", Kind: resource.String, Required: true, Max: 200, Search: true},
		{Name: "year", Column: "year", Label: "Year", Kind: resource.Int, Min: resource.Min(1900), Filter: true},
		{Name: "division", Column: "division", Label: "Division", Kind: resource.Enum, Enum: []string{"men", "ladies", "senior", "junior", "open"}},
		{Name: "customerId", Column: "customer_id", Label: "Player (customer)", Kind: resource.UUID, Ref: &resource.Ref{Table: "crm.customers", SameProperty: true, Label: "customer"}},
		{Name: "playerName", Column: "player_name", Label: "Player Name", Kind: resource.String, Max: 120, Search: true},
		{Name: "teeSetId", Column: "tee_set_id", Label: "Tee Set", Kind: resource.UUID, Ref: &resource.Ref{Table: "golf.tee_sets", SameProperty: true, Label: "tee set"}},
		{Name: "holeId", Column: "hole_id", Label: "Hole", Kind: resource.UUID, Ref: &resource.Ref{Table: "golf.holes", SameProperty: true, Label: "hole"}},
		{Name: "score", Column: "score", Label: "Score", Kind: resource.Int},
		{Name: "achievedOn", Column: "achieved_on", Label: "Date", Kind: resource.Date},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		{Name: "photoUrl", Column: "photo_url", Label: "Photo", Kind: resource.String, Max: 500},
		{Name: "sourceType", Column: "source_type", Label: "Source", Kind: resource.String, ReadOnly: true, Default: "manual"},
		{Name: "needsVerification", Column: "needs_verification", Label: "Needs Verification", Kind: resource.Bool, ReadOnly: true},
		{Name: "consent", Column: "consent", Label: "Player Consent", Kind: resource.Enum, Enum: []string{"pending", "granted", "withdrawn", "not_required"},
			Default: "pending", Filter: true},
		{Name: "published", Column: "published", Label: "Published", Kind: resource.Bool, ReadOnly: true, Filter: true},
		resource.Status("active", "inactive")},
}

// P2Defs are the P2 golf master data resources.
var P2Defs = []*resource.Def{CaddyLevels, CartChecklists, RangeBays, ReciprocalClubs, HallOfFame}

// ── policies (FR-POL-P2-04/05/07) ─────────────────────────────────────────

// ReciprocalPolicy is "Reciprocal Policies" (code golf.reciprocal).
type ReciprocalPolicy struct {
	RequireLetter      bool `json:"requireLetter"`
	RequireHomeCard    bool `json:"requireHomeCard"`
	LetterValidityDays int  `json:"letterValidityDays"`
	EnforceQuota       bool `json:"enforceQuota"`
}

var defaultReciprocalPolicy = ReciprocalPolicy{RequireLetter: true, RequireHomeCard: true, LetterValidityDays: 30, EnforceQuota: true}

// HallOfFamePolicy is "Hall of Fame Policies" (code golf.hall_of_fame).
type HallOfFamePolicy struct {
	AutoPublish          bool `json:"autoPublish" doc:"false: automatic entries wait for a Golf Admin to publish (FR-HOF-06)"`
	KioskRotateSecs      int  `json:"kioskRotateSeconds"`
	CourseRecordMinHoles int  `json:"courseRecordMinHoles"`
}

var defaultHOFPolicy = HallOfFamePolicy{AutoPublish: false, KioskRotateSecs: 12, CourseRecordMinHoles: 18}

func init() {
	// Every golf policy appears in the Club Policies catalogue (P1 policies
	// with their P2 extensions, plus the P2 ones).
	for code, def := range map[string]any{PolicyGolf: DefaultGolf, PolicyGuest: DefaultGuest, PolicyCancellation: DefaultCancellation,
		PolicyWeather: DefaultWeather, PolicyCaddy: DefaultCaddy, PolicyCart: DefaultCart, PolicyPayment: DefaultPayment, PolicyEligibility: DefaultEligibility} {
		rules.RegisterPolicy(rules.PolicyDef{Code: code, Category: PolicyCategory[code], Name: PolicyCategory[code], Default: def})
	}
	rules.RegisterPolicy(rules.PolicyDef{Code: "golf.reciprocal", Category: "Reciprocal Policies", Name: "Reciprocal verification",
		Description: "Introduction letter and home club card requirements, letter validity, visit quota", Default: defaultReciprocalPolicy})
	rules.RegisterPolicy(rules.PolicyDef{Code: "golf.hall_of_fame", Category: "Hall of Fame Policies", Name: "Hall of Fame curation",
		Description: "Auto-publish of automatic entries, kiosk rotation, course record minimum holes", Default: defaultHOFPolicy})
}

func (m *Module) caddyPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (CaddyPolicy, error) {
	p, err := LoadPolicies(ctx, q, property, clock.Now())
	return p.Caddy, err
}

func (m *Module) cartPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (CartPolicy, error) {
	p, err := LoadPolicies(ctx, q, property, clock.Now())
	return p.Cart, err
}

func (m *Module) reciprocalPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (ReciprocalPolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, "golf.reciprocal", property, defaultReciprocalPolicy)
	return p, err
}

func (m *Module) hofPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (HallOfFamePolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, "golf.hall_of_fame", property, defaultHOFPolicy)
	return p, err
}

// ── approval document types ───────────────────────────────────────────────

var (
	PromotionType          = provision.DocumentType{Code: "golf_caddy_promotion", Module: "golf", Name: "Caddy Promotion"}
	SettlementType         = provision.DocumentType{Code: "golf_caddy_settlement", Module: "golf", Name: "Caddy Fee Settlement", Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Amount", Type: "number"}}}
	HIOType                = provision.DocumentType{Code: "golf_hio_verification", Module: "golf", Name: "Hole-in-One Verification"}
	DamageChargeType       = provision.DocumentType{Code: "golf_damage_charge", Module: "golf", Name: "Golf Cart Damage Charge", Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Amount", Type: "number"}}}
	IntroductionLetterType = provision.DocumentType{Code: "golf_introduction_letter", Module: "golf", Name: "Introduction Letter"}
)

// P2DocumentTypes lists the P2 golf approval document types.
func P2DocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{PromotionType, SettlementType, HIOType, DamageChargeType, IntroductionLetterType}
}

// P2Decision routes approval decisions of the P2 golf documents.
func (m *Module) P2Decision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	switch d.DocumentType {
	case PromotionType.Code:
		return m.promotionDecision(ctx, tx, d)
	case SettlementType.Code:
		return m.settlementDecision(ctx, tx, d)
	case HIOType.Code:
		return m.hioDecision(ctx, tx, d)
	case DamageChargeType.Code:
		return m.damageDecision(ctx, tx, d)
	case IntroductionLetterType.Code:
		return m.letterDecision(ctx, tx, d)
	}
	return nil
}

// ── helpers of the P2 golf code ───────────────────────────────────────────

func actorPtr(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil && p.UserID != uuid.Nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func localNow(ctx context.Context, q dbtx.Querier) time.Time {
	return clock.Now().In(calendar.Location(ctx, q))
}

func dayBounds(ctx context.Context, q dbtx.Querier, d time.Time) (time.Time, time.Time) {
	loc := calendar.Location(ctx, q)
	from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	return from, from.AddDate(0, 0, 1)
}

func eventTime(at *time.Time) time.Time {
	if at != nil && !at.IsZero() {
		return at.UTC()
	}
	return clock.Now()
}

func record(ctx context.Context, tx pgx.Tx, entity string, eid uuid.UUID, label, action string, property uuid.UUID, before, after any, reason string) error {
	return audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: action, EntityType: entity, EntityID: eid.String(), EntityLabel: label,
		PropertyID: &property, Before: before, After: after, Reason: reason})
}

func (m *Module) publish(ctx context.Context, tx pgx.Tx, event, aggregate string, aid, property uuid.UUID, payload any) error {
	if m.Events == nil {
		return nil
	}
	_, err := m.Events.Publish(ctx, tx, event, aggregate, &aid, &property, payload)
	return err
}

// live notifies real-time screens (pace of play, golf cart readiness,
// driving range, caddy board) through P1's realtime hub.
func (m *Module) live(ctx context.Context, tx pgx.Tx, topic string, property uuid.UUID, kind, eid string, data map[string]any) error {
	payload := map[string]any{"id": eid}
	for k, v := range data {
		payload[k] = v
	}
	return realtime.Publish(ctx, tx, topic, kind, &property, payload)
}

func collectIDs(rows pgx.Rows, err error) ([]uuid.UUID, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

func can(ctx context.Context, perm string, property uuid.UUID) bool {
	p := authz.From(ctx)
	return p != nil && p.Can(perm, &property)
}

// p2Contribution is the P2 part of the golf catalogue.
func p2Contribution() catalog.Contribution {
	perms := resource.Permissions(CaddyLevels, RangeBays, ReciprocalClubs, HallOfFame)
	add := func(obj string, actions ...string) { perms = append(perms, catalog.P("golf", obj, actions...)...) }
	add("caddy_assignment", "accept")
	add("caddy_promotion", "view", "request")
	add("caddy_incident", "view", "create", "manage")
	add("caddy_rating", "view", "create")
	add("caddy_settlement", "view", "manage", "pay")
	add("cart_inspection", "view", "create")
	add("cart_maintenance", "view", "manage")
	add("cart_incident", "view", "create", "manage")
	add("scorecard", "view", "enter", "finalize", "correct", "view_all")
	add("handicap", "view", "manage")
	add("hio", "view", "manage", "claim")
	add("hall_of_fame", "publish")
	add("range", "view", "operate")
	add("reciprocal_visit", "view", "verify")
	add("introduction_letter", "view", "request", "issue")
	add("pace", "view")
	add("round", "view", "operate")
	add("tablet", "use")
	view := []string{"golf.caddy_level.view", "golf.range_bay.view", "golf.reciprocal_club.view", "golf.hall_of_fame.view", "golf.caddy_promotion.view",
		"golf.caddy_incident.view", "golf.caddy_rating.view", "golf.caddy_settlement.view", "golf.cart_inspection.view", "golf.cart_maintenance.view",
		"golf.cart_incident.view", "golf.scorecard.view", "golf.handicap.view", "golf.hio.view", "golf.range.view", "golf.reciprocal_visit.view",
		"golf.introduction_letter.view", "golf.pace.view", "golf.round.view"}
	all := make([]string, 0, len(perms))
	for _, x := range perms {
		all = append(all, x.Code)
	}
	with := func(base []string, extra ...string) []string { return append(append([]string{}, base...), extra...) }
	manager := with(view, "golf.caddy_level.create", "golf.caddy_level.update", "golf.range_bay.create", "golf.range_bay.update",
		"golf.reciprocal_club.create", "golf.reciprocal_club.update", "golf.hall_of_fame.create", "golf.hall_of_fame.update", "golf.hall_of_fame.publish",
		"golf.caddy_promotion.request", "golf.caddy_incident.create", "golf.caddy_incident.manage", "golf.caddy_settlement.manage",
		"golf.cart_inspection.create", "golf.cart_maintenance.manage", "golf.cart_incident.create", "golf.cart_incident.manage", "golf.scorecard.enter",
		"golf.scorecard.finalize", "golf.scorecard.correct", "golf.scorecard.view_all", "golf.handicap.manage", "golf.hio.manage", "golf.hio.claim",
		"golf.range.operate", "golf.reciprocal_visit.verify", "golf.introduction_letter.request", "golf.introduction_letter.issue", "golf.round.operate")
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"property_admin": all,
			"golf_manager":   manager,
			"golf_admin": with(view, "golf.range_bay.create", "golf.range_bay.update", "golf.reciprocal_club.create", "golf.reciprocal_club.update",
				"golf.hall_of_fame.create", "golf.hall_of_fame.update", "golf.hall_of_fame.publish", "golf.scorecard.enter", "golf.scorecard.finalize",
				"golf.scorecard.view_all", "golf.reciprocal_visit.verify", "golf.introduction_letter.request", "golf.introduction_letter.issue", "golf.hio.manage"),
			"starter_marshal": with(view, "golf.scorecard.enter", "golf.round.operate"),
			"caddy_manager": with(view, "golf.caddy_promotion.request", "golf.caddy_incident.create", "golf.caddy_incident.manage",
				"golf.caddy_settlement.manage"),
			"caddy": {"golf.tablet.use", "golf.caddy_assignment.accept", "golf.round.view", "golf.round.operate", "golf.scorecard.view", "golf.scorecard.enter", "golf.caddy_incident.create",
				"golf.cart_incident.create", "golf.pace.view"},
			"golf_staff": {"golf.cart_inspection.view", "golf.cart_inspection.create", "golf.cart_maintenance.view", "golf.cart_maintenance.manage",
				"golf.cart_incident.view", "golf.cart_incident.create"},
			"driving_range_staff": {"golf.range_bay.view", "golf.range.view", "golf.range.operate", "golf.range_bay.update"},
			"general_manager":     view,
			"club_manager":        view,
			"finance_manager":     {"golf.caddy_settlement.view", "golf.caddy_settlement.manage", "golf.caddy_settlement.pay", "golf.cart_incident.view", "golf.hio.view"},
			"accountant":          {"golf.caddy_settlement.view", "golf.caddy_settlement.pay"},
			"membership_admin":    {"golf.reciprocal_club.view", "golf.introduction_letter.view", "golf.introduction_letter.request", "golf.introduction_letter.issue", "golf.reciprocal_visit.view"},
			"front_desk":          {"golf.reciprocal_visit.view", "golf.reciprocal_visit.verify", "golf.reciprocal_club.view"},
		},
	}
}

// number issues a P2 golf document number (incident, settlement, HIO …) from
// P1's golf sequences.
func number(ctx context.Context, tx pgx.Tx, property uuid.UUID, prefix string) (string, error) {
	return docno.Daily(ctx, tx, "golf.sequences", property, prefix, localNow(ctx, tx))
}

func jsonOf(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
