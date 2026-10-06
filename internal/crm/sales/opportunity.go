package sales

// EP-02 Sales Pipeline & Opportunity: pipelines per business line with the
// default stages of PRD P3 §16.1 (seeded on first use), opportunities with
// value, probability, expected close and event date, recorded stage moves,
// Won (linked to an accepted quotation) / Lost with reason, the kanban board
// and the weighted forecast (FR-PIPE-01..07).

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
)

type stageSeed struct {
	code, name string
	prob       int
	kind       string
}

type pipelineSeed struct {
	code, name string
	lines      []string
	sort       int
	stages     []stageSeed
}

func won(name string) stageSeed { return stageSeed{"WON", name, 100, "won"} }

var lostStage = stageSeed{"LOST", "Lost", 0, "lost"}

// DefaultPipelines are the default stages and probabilities of PRD P3 §16.1
// (decision #2); General serves the lines without a dedicated pipeline.
var DefaultPipelines = []pipelineSeed{
	{"WEDDING", "Wedding", []string{"wedding"}, 10, []stageSeed{{"NEW_INQUIRY", "New Inquiry", 10, "open"}, {"SITE_VISIT", "Site Visit", 25, "open"},
		{"FOOD_TASTING", "Food Tasting", 40, "open"}, {"QUOTATION", "Quotation", 50, "open"}, {"NEGOTIATION", "Negotiation", 70, "open"},
		won("DP Paid / Won"), lostStage}},
	{"MICE", "MICE / Meeting", []string{"mice", "banquet", "event"}, 20, []stageSeed{{"INQUIRY", "Inquiry", 10, "open"},
		{"REQUIREMENT", "Requirement Gathering", 25, "open"}, {"PROPOSAL", "Proposal / Quotation", 50, "open"}, {"NEGOTIATION", "Negotiation", 70, "open"},
		won("Contract Signed / Won"), lostStage}},
	{"CORP-MEMBERSHIP", "Corporate Membership", []string{"membership"}, 30, []stageSeed{{"LEAD", "Lead", 10, "open"},
		{"PRESENTATION", "Presentation", 30, "open"}, {"PROPOSAL", "Proposal", 50, "open"}, {"NEGOTIATION", "Negotiation", 70, "open"},
		won("Agreement Signed / Won"), lostStage}},
	{"SPORT-MEMBERSHIP", "Sport Membership", []string{"membership"}, 40, []stageSeed{{"INQUIRY", "Inquiry", 10, "open"},
		{"FACILITY_TOUR", "Facility Tour", 30, "open"}, {"TRIAL_OFFER", "Trial / Offer", 60, "open"}, won("Payment / Won"), lostStage}},
	{"CORP-GOLF", "Corporate Golf", []string{"golf"}, 50, []stageSeed{{"INQUIRY", "Inquiry", 10, "open"}, {"PROPOSAL", "Proposal", 40, "open"},
		{"NEGOTIATION", "Negotiation", 70, "open"}, won("Confirmed (DP) / Won"), lostStage}},
	{"TOURNAMENT", "Tournament", []string{"tournament"}, 60, []stageSeed{{"INQUIRY", "Inquiry", 10, "open"}, {"PROPOSAL", "Proposal", 40, "open"},
		{"AGREED", "Sponsor / Format Agreed", 70, "open"}, won("Contract & DP / Won"), lostStage}},
	{"GENERAL", "General", []string{}, 90, []stageSeed{{"INQUIRY", "Inquiry", 10, "open"}, {"PROPOSAL", "Proposal / Quotation", 40, "open"},
		{"NEGOTIATION", "Negotiation", 70, "open"}, won("Won"), lostStage}},
}

// SeedResult reports the default pipelines created.
type SeedResult struct {
	Pipelines int `json:"pipelines"`
	Stages    int `json:"stages"`
}

// SeedPipelines creates the missing default pipelines and stages
// (idempotent by code).
func SeedPipelines(ctx context.Context, tx pgx.Tx, property uuid.UUID) (SeedResult, error) {
	var res SeedResult
	for _, p := range DefaultPipelines {
		pid := id.New()
		err := tx.QueryRow(ctx, `INSERT INTO crm.sales_pipelines (id, property_id, code, name, lines, sort_order) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (property_id, code) DO NOTHING RETURNING id`, pid, property, p.code, p.name, p.lines, p.sort).Scan(&pid)
		if dbtx.IsNoRows(err) {
			continue
		}
		if err != nil {
			return res, err
		}
		res.Pipelines++
		for i, s := range p.stages {
			if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_pipeline_stages (id, property_id, pipeline_id, code, name, probability, sort_order, kind)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (pipeline_id, code) DO NOTHING`, id.New(), property, pid, s.code, s.name, s.prob,
				(i+1)*10, s.kind); err != nil {
				return res, err
			}
			res.Stages++
		}
	}
	return res, nil
}

// ensurePipelines seeds the defaults when the property has no pipeline yet
// (first use).
func ensurePipelines(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.sales_pipelines WHERE property_id = $1)`, property).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	res, err := SeedPipelines(ctx, tx, property)
	if err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "seed_defaults", EntityType: "crm.sales_pipeline", EntityID: property.String(),
		EntityLabel: "Default pipelines", PropertyID: &property, After: res})
}

// defaultPipeline returns the pipeline of a business line.
func defaultPipeline(ctx context.Context, q dbtx.Querier, property uuid.UUID, line string) (uuid.UUID, error) {
	var pid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM crm.sales_pipelines WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
		AND ($2 = ANY (lines) OR cardinality(lines) = 0) ORDER BY ($2 = ANY (lines)) DESC, sort_order, name LIMIT 1`, property, line).Scan(&pid)
	if dbtx.IsNoRows(err) {
		return pid, errs.Validation("no_pipeline", "no active pipeline for line "+line, errs.Field("pipelineId", "required", "choose a pipeline"))
	}
	return pid, err
}

type stageInfo struct {
	ID          uuid.UUID
	PipelineID  uuid.UUID
	Kind        string
	Probability decimal.Decimal
	Name        string
}

func stageOf(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (stageInfo, error) {
	var s stageInfo
	var p string
	err := q.QueryRow(ctx, `SELECT id, pipeline_id, kind, probability::text, name FROM crm.sales_pipeline_stages WHERE id = $1 AND archived_at IS NULL`,
		sid).Scan(&s.ID, &s.PipelineID, &s.Kind, &p, &s.Name)
	if dbtx.IsNoRows(err) {
		return s, handle.Invalid("stageId", "not_found", "stage not found")
	}
	s.Probability = dec(p)
	return s, err
}

// stageOfKind returns the first active stage of a kind in a pipeline.
func stageOfKind(ctx context.Context, q dbtx.Querier, pipeline uuid.UUID, kind string) (stageInfo, error) {
	var sid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM crm.sales_pipeline_stages WHERE pipeline_id = $1 AND kind = $2 AND status = 'active' AND archived_at IS NULL
		ORDER BY sort_order, created_at LIMIT 1`, pipeline, kind).Scan(&sid)
	if dbtx.IsNoRows(err) {
		return stageInfo{}, errs.Conflict("pipeline_incomplete", "the pipeline has no active "+kind+" stage")
	}
	if err != nil {
		return stageInfo{}, err
	}
	return stageOf(ctx, q, sid)
}

// ── opportunity ───────────────────────────────────────────────────────────

// Opportunity is a deal in a pipeline.
type Opportunity struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	Number             string     `json:"number" db:"number"`
	Title              string     `json:"title" db:"title"`
	PipelineID         uuid.UUID  `json:"pipelineId" db:"pipeline_id"`
	PipelineName       string     `json:"pipelineName" db:"pipeline_name"`
	StageID            uuid.UUID  `json:"stageId" db:"stage_id"`
	StageName          string     `json:"stageName" db:"stage_name"`
	StageKind          string     `json:"stageKind" db:"stage_kind" enum:"open,won,lost"`
	Line               string     `json:"line" db:"line"`
	LeadID             *uuid.UUID `json:"leadId" db:"lead_id"`
	CustomerID         *uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerName       *string    `json:"customerName" db:"customer_name"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId" db:"corporate_account_id"`
	CorporateName      *string    `json:"corporateName" db:"corporate_name"`
	OwnerUserID        *uuid.UUID `json:"ownerUserId" db:"owner_user_id"`
	OwnerName          *string    `json:"ownerName" db:"owner_name"`
	ExpectedValue      string     `json:"expectedValue" db:"expected_value"`
	Currency           string     `json:"currency" db:"currency"`
	Probability        string     `json:"probability" db:"probability" doc:"Percent of the stage"`
	WeightedValue      string     `json:"weightedValue" db:"weighted_value" doc:"Expected value × probability"`
	ExpectedCloseDate  *string    `json:"expectedCloseDate" db:"expected_close_date"`
	EventType          *string    `json:"eventType" db:"event_type"`
	EventDate          *string    `json:"eventDate" db:"event_date"`
	EndDate            *string    `json:"endDate" db:"end_date"`
	Pax                *int       `json:"pax" db:"pax"`
	VenueResourceID    *uuid.UUID `json:"venueResourceId" db:"venue_resource_id"`
	PackageRef         *string    `json:"packageRef" db:"package_ref"`
	Status             string     `json:"status" db:"status" enum:"open,won,lost"`
	StageChangedAt     time.Time  `json:"stageChangedAt" db:"stage_changed_at"`
	WonAt              *time.Time `json:"wonAt" db:"won_at"`
	WonQuotationID     *uuid.UUID `json:"wonQuotationId" db:"won_quotation_id"`
	LostAt             *time.Time `json:"lostAt" db:"lost_at"`
	LostReason         *string    `json:"lostReason" db:"lost_reason" enum:"price,date_unavailable,competitor,cancelled,budget,no_response,other"`
	LostNote           *string    `json:"lostNote" db:"lost_note"`
	Notes              *string    `json:"notes" db:"notes"`
	CreatedAt          time.Time  `json:"createdAt" db:"created_at"`
	UpdatedAt          time.Time  `json:"updatedAt" db:"updated_at"`
}

const opportunitySelect = `SELECT o.id, o.number, o.title, o.pipeline_id, p.name AS pipeline_name, o.stage_id, s.name AS stage_name, s.kind AS stage_kind,
	o.line, o.lead_id, o.customer_id, c.name AS customer_name, o.corporate_account_id, ca.name AS corporate_name, o.owner_user_id,
	u.full_name AS owner_name, trim_scale(o.expected_value)::text AS expected_value, o.currency, trim_scale(o.probability)::text AS probability,
	trim_scale(round(o.expected_value * o.probability / 100, 2))::text AS weighted_value, to_char(o.expected_close_date, 'YYYY-MM-DD') AS expected_close_date,
	o.event_type, to_char(o.event_date, 'YYYY-MM-DD') AS event_date, to_char(o.end_date, 'YYYY-MM-DD') AS end_date, o.pax, o.venue_resource_id,
	o.package_ref, o.status, o.stage_changed_at, o.won_at, o.won_quotation_id, o.lost_at, o.lost_reason, o.lost_note, o.notes, o.created_at, o.updated_at
	FROM crm.sales_opportunities o JOIN crm.sales_pipelines p ON p.id = o.pipeline_id JOIN crm.sales_pipeline_stages s ON s.id = o.stage_id
	LEFT JOIN crm.customers c ON c.id = o.customer_id LEFT JOIN crm.corporate_accounts ca ON ca.id = o.corporate_account_id
	LEFT JOIN platform.users u ON u.id = o.owner_user_id`

// GetOpportunity loads an opportunity.
func GetOpportunity(ctx context.Context, q dbtx.Querier, oid uuid.UUID) (Opportunity, error) {
	return getOne[Opportunity]("opportunity")(q.Query(ctx, opportunitySelect+` WHERE o.id = $1`, oid))
}

func lockOpportunity(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID) (Opportunity, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM crm.sales_opportunities WHERE id = $1 AND property_id = $2 FOR UPDATE`, oid, property).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return Opportunity{}, errs.NotFound("opportunity")
		}
		return Opportunity{}, err
	}
	return GetOpportunity(ctx, tx, oid)
}

// StageMove is one recorded stage change.
type StageMove struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	FromStage   *string    `json:"fromStage" db:"from_stage"`
	ToStage     string     `json:"toStage" db:"to_stage"`
	Probability string     `json:"probability" db:"probability"`
	Note        *string    `json:"note" db:"note"`
	ChangedBy   *uuid.UUID `json:"changedBy" db:"changed_by"`
	ChangedName *string    `json:"changedByName" db:"changed_by_name"`
	ChangedAt   time.Time  `json:"changedAt" db:"changed_at"`
}

// OpportunityDetail is an opportunity with its history and quotations.
type OpportunityDetail struct {
	Opportunity
	StageHistory []StageMove      `json:"stageHistory"`
	Activities   []Activity       `json:"activities"`
	Quotations   []QuotationBrief `json:"quotations"`
}

// OpportunityDetailOf loads the detail of an opportunity.
func OpportunityDetailOf(ctx context.Context, q dbtx.Querier, oid uuid.UUID) (OpportunityDetail, error) {
	o, err := GetOpportunity(ctx, q, oid)
	if err != nil {
		return OpportunityDetail{}, err
	}
	d := OpportunityDetail{Opportunity: o}
	if d.StageHistory, err = handle.List[StageMove](q.Query(ctx, `SELECT h.id, fs.name AS from_stage, ts.name AS to_stage,
		trim_scale(h.probability)::text AS probability, h.note, h.changed_by, u.full_name AS changed_by_name, h.changed_at
		FROM crm.sales_stage_history h LEFT JOIN crm.sales_pipeline_stages fs ON fs.id = h.from_stage_id
		JOIN crm.sales_pipeline_stages ts ON ts.id = h.to_stage_id LEFT JOIN platform.users u ON u.id = h.changed_by
		WHERE h.opportunity_id = $1 ORDER BY h.changed_at, h.id`, oid)); err != nil {
		return d, err
	}
	if d.Activities, err = listActivities(ctx, q, `a.opportunity_id = $1`, oid); err != nil {
		return d, err
	}
	d.Quotations, err = handle.List[QuotationBrief](q.Query(ctx, quotationBriefSelect+` WHERE q.opportunity_id = $1 ORDER BY q.number, q.version`, oid))
	return d, err
}

// OpportunityInput creates an opportunity.
type OpportunityInput struct {
	Title              string      `json:"title"`
	PipelineID         *uuid.UUID  `json:"pipelineId,omitempty" doc:"Default: the pipeline of the line"`
	StageID            *uuid.UUID  `json:"stageId,omitempty" doc:"Default: the first open stage"`
	Line               string      `json:"line,omitempty" enum:"wedding,banquet,mice,event,tournament,stay,golf,package,membership,other"`
	LeadID             *uuid.UUID  `json:"leadId,omitempty"`
	CustomerID         *uuid.UUID  `json:"customerId,omitempty"`
	CorporateAccountID *uuid.UUID  `json:"corporateAccountId,omitempty"`
	OwnerUserID        *uuid.UUID  `json:"ownerUserId,omitempty" doc:"Default: me"`
	ExpectedValue      string      `json:"expectedValue,omitempty"`
	ExpectedCloseDate  *route.Date `json:"expectedCloseDate,omitempty"`
	EventType          string      `json:"eventType,omitempty" enum:"wedding,meeting,conference,gathering,birthday,tournament,other"`
	EventDate          *route.Date `json:"eventDate,omitempty"`
	EndDate            *route.Date `json:"endDate,omitempty"`
	Pax                *int        `json:"pax,omitempty"`
	VenueResourceID    *uuid.UUID  `json:"venueResourceId,omitempty"`
	PackageRef         string      `json:"packageRef,omitempty"`
	Notes              string      `json:"notes,omitempty"`
}

func optDate(field string, d *route.Date) (*time.Time, error) {
	if d == nil {
		return nil, nil
	}
	return parseDate(field, string(*d))
}

// CreateOpportunity creates an opportunity directly (customer or company).
func (m *Module) CreateOpportunity(ctx context.Context, tx pgx.Tx, property uuid.UUID, in OpportunityInput) (OpportunityDetail, error) {
	if in.CustomerID == nil && in.CorporateAccountID == nil {
		return OpportunityDetail{}, handle.Invalid("customerId", "required", "a customer or a corporate account is required")
	}
	if in.OwnerUserID == nil {
		in.OwnerUserID = actor(ctx)
	}
	o, err := m.createOpportunity(ctx, tx, property, in)
	if err != nil {
		return OpportunityDetail{}, err
	}
	return OpportunityDetailOf(ctx, tx, o.ID)
}

func (m *Module) createOpportunity(ctx context.Context, tx pgx.Tx, property uuid.UUID, in OpportunityInput) (Opportunity, error) {
	if err := handle.Required("title", in.Title); err != nil {
		return Opportunity{}, err
	}
	if in.Line == "" {
		in.Line = "other"
	}
	if !oneOf(Lines, in.Line) {
		return Opportunity{}, enumErr("line", Lines)
	}
	if in.EventType != "" && !oneOf(EventTypes, in.EventType) {
		return Opportunity{}, enumErr("eventType", EventTypes)
	}
	if in.CustomerID != nil {
		if ok, err := crm.ExistsInProperty(ctx, tx, property, *in.CustomerID); err != nil || !ok {
			if err != nil {
				return Opportunity{}, err
			}
			return Opportunity{}, handle.Invalid("customerId", "not_found", "customer not found in this property")
		}
	}
	if in.CorporateAccountID != nil {
		if _, err := crm.CorporateName(ctx, tx, *in.CorporateAccountID); err != nil {
			return Opportunity{}, err
		}
	}
	if in.OwnerUserID != nil {
		if err := ensureUser(ctx, tx, "ownerUserId", *in.OwnerUserID); err != nil {
			return Opportunity{}, err
		}
	}
	if in.Pax != nil && *in.Pax <= 0 {
		return Opportunity{}, handle.Invalid("pax", "invalid", "a positive number of guests")
	}
	value, err := handle.Decimal("expectedValue", in.ExpectedValue, decimal.Zero)
	if err != nil {
		return Opportunity{}, err
	}
	if value.IsNegative() {
		return Opportunity{}, handle.Invalid("expectedValue", "invalid", "a positive amount")
	}
	closeDate, err := optDate("expectedCloseDate", in.ExpectedCloseDate)
	if err != nil {
		return Opportunity{}, err
	}
	eventDate, err := optDate("eventDate", in.EventDate)
	if err != nil {
		return Opportunity{}, err
	}
	endDate, err := optDate("endDate", in.EndDate)
	if err != nil {
		return Opportunity{}, err
	}
	if err := ensurePipelines(ctx, tx, property); err != nil {
		return Opportunity{}, err
	}
	var pipeline uuid.UUID
	if in.PipelineID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.sales_pipelines WHERE id = $1 AND property_id = $2 AND status = 'active'
			AND archived_at IS NULL)`, *in.PipelineID, property).Scan(&ok); err != nil {
			return Opportunity{}, err
		}
		if !ok {
			return Opportunity{}, handle.Invalid("pipelineId", "not_found", "pipeline not found or inactive")
		}
		pipeline = *in.PipelineID
	} else if pipeline, err = defaultPipeline(ctx, tx, property, in.Line); err != nil {
		return Opportunity{}, err
	}
	var st stageInfo
	if in.StageID != nil {
		if st, err = stageOf(ctx, tx, *in.StageID); err != nil {
			return Opportunity{}, err
		}
		if st.PipelineID != pipeline || st.Kind != "open" {
			return Opportunity{}, handle.Invalid("stageId", "invalid", "an open stage of the pipeline")
		}
	} else if st, err = stageOfKind(ctx, tx, pipeline, "open"); err != nil {
		return Opportunity{}, err
	}
	loc := location(ctx, tx, property)
	number, err := numbering.Next(ctx, tx, property, "OPP", clock.Now().In(loc))
	if err != nil {
		return Opportunity{}, err
	}
	cur := "IDR"
	_ = tx.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&cur)
	oid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_opportunities (id, property_id, number, title, pipeline_id, stage_id, line, lead_id, customer_id,
		corporate_account_id, owner_user_id, expected_value, currency, probability, expected_close_date, event_type, event_date, end_date, pax,
		venue_resource_id, package_ref, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::numeric,$13,$14::numeric,$15,$16,$17,$18,$19,$20,$21,$22,$23,$23)`,
		oid, property, number, strings.TrimSpace(in.Title), pipeline, st.ID, in.Line, in.LeadID, in.CustomerID, in.CorporateAccountID, in.OwnerUserID,
		value.String(), cur, st.Probability.String(), closeDate, nullStr(in.EventType), eventDate, endDate, in.Pax, in.VenueResourceID,
		nullStr(in.PackageRef), nullStr(in.Notes), actor(ctx)); err != nil {
		return Opportunity{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_stage_history (id, property_id, opportunity_id, to_stage_id, probability, note, changed_by)
		VALUES ($1,$2,$3,$4,$5::numeric,'created',$6)`, id.New(), property, oid, st.ID, st.Probability.String(), actor(ctx)); err != nil {
		return Opportunity{}, err
	}
	o, err := GetOpportunity(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	return o, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.opportunity", EntityID: oid.String(),
		EntityLabel: number + " · " + o.Title, PropertyID: &property, After: o})
}

// OpportunityUpdate edits an open opportunity (omitted fields keep their
// value).
type OpportunityUpdate struct {
	Title              *string     `json:"title,omitempty"`
	CustomerID         *uuid.UUID  `json:"customerId,omitempty"`
	CorporateAccountID *uuid.UUID  `json:"corporateAccountId,omitempty"`
	OwnerUserID        *uuid.UUID  `json:"ownerUserId,omitempty"`
	ExpectedValue      *string     `json:"expectedValue,omitempty"`
	ExpectedCloseDate  *route.Date `json:"expectedCloseDate,omitempty"`
	EventType          *string     `json:"eventType,omitempty"`
	EventDate          *route.Date `json:"eventDate,omitempty"`
	EndDate            *route.Date `json:"endDate,omitempty"`
	Pax                *int        `json:"pax,omitempty"`
	VenueResourceID    *uuid.UUID  `json:"venueResourceId,omitempty"`
	PackageRef         *string     `json:"packageRef,omitempty"`
	Notes              *string     `json:"notes,omitempty"`
}

// UpdateOpportunity edits an opportunity; won / lost deals keep their
// commercial data (notes only).
func (m *Module) UpdateOpportunity(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID, in OpportunityUpdate) (OpportunityDetail, error) {
	before, err := lockOpportunity(ctx, tx, property, oid)
	if err != nil {
		return OpportunityDetail{}, err
	}
	if before.Status != "open" && (in.Title != nil || in.ExpectedValue != nil || in.CustomerID != nil || in.CorporateAccountID != nil ||
		in.OwnerUserID != nil || in.EventDate != nil || in.Pax != nil) {
		return OpportunityDetail{}, conflict("opportunity_closed", "a "+before.Status+" opportunity keeps its deal data")
	}
	sets := []string{}
	args := []any{oid}
	add := func(col string, v any, cast string) {
		args = append(args, v)
		sets = append(sets, col+" = $"+itoa(len(args))+cast)
	}
	if in.Title != nil {
		if err := handle.Required("title", *in.Title); err != nil {
			return OpportunityDetail{}, err
		}
		add("title", strings.TrimSpace(*in.Title), "")
	}
	if in.CustomerID != nil {
		if ok, err := crm.ExistsInProperty(ctx, tx, property, *in.CustomerID); err != nil || !ok {
			if err != nil {
				return OpportunityDetail{}, err
			}
			return OpportunityDetail{}, handle.Invalid("customerId", "not_found", "customer not found in this property")
		}
		add("customer_id", *in.CustomerID, "")
	}
	if in.CorporateAccountID != nil {
		if _, err := crm.CorporateName(ctx, tx, *in.CorporateAccountID); err != nil {
			return OpportunityDetail{}, err
		}
		add("corporate_account_id", *in.CorporateAccountID, "")
	}
	if in.OwnerUserID != nil {
		if err := ensureUser(ctx, tx, "ownerUserId", *in.OwnerUserID); err != nil {
			return OpportunityDetail{}, err
		}
		add("owner_user_id", *in.OwnerUserID, "")
	}
	if in.ExpectedValue != nil {
		v, err := handle.Decimal("expectedValue", *in.ExpectedValue, decimal.Zero)
		if err != nil || v.IsNegative() {
			return OpportunityDetail{}, handle.Invalid("expectedValue", "invalid", "a positive amount")
		}
		add("expected_value", v.String(), "::numeric")
	}
	for _, d := range []struct {
		col, field string
		v          *route.Date
	}{{"expected_close_date", "expectedCloseDate", in.ExpectedCloseDate}, {"event_date", "eventDate", in.EventDate}, {"end_date", "endDate", in.EndDate}} {
		if d.v == nil {
			continue
		}
		t, err := optDate(d.field, d.v)
		if err != nil {
			return OpportunityDetail{}, err
		}
		add(d.col, t, "")
	}
	if in.EventType != nil {
		if *in.EventType != "" && !oneOf(EventTypes, *in.EventType) {
			return OpportunityDetail{}, enumErr("eventType", EventTypes)
		}
		add("event_type", nullStr(*in.EventType), "")
	}
	if in.Pax != nil {
		if *in.Pax <= 0 {
			return OpportunityDetail{}, handle.Invalid("pax", "invalid", "a positive number of guests")
		}
		add("pax", *in.Pax, "")
	}
	if in.VenueResourceID != nil {
		add("venue_resource_id", *in.VenueResourceID, "")
	}
	if in.PackageRef != nil {
		add("package_ref", nullStr(*in.PackageRef), "")
	}
	if in.Notes != nil {
		add("notes", nullStr(*in.Notes), "")
	}
	if len(sets) == 0 {
		return OpportunityDetail{}, handle.Invalid("title", "nothing_to_update", "no field to update")
	}
	add("updated_by", actor(ctx), "")
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_opportunities SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...); err != nil {
		return OpportunityDetail{}, err
	}
	after, err := GetOpportunity(ctx, tx, oid)
	if err != nil {
		return OpportunityDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionUpdate, EntityType: "crm.opportunity", EntityID: oid.String(),
		EntityLabel: after.Number + " · " + after.Title, PropertyID: &property, Before: before, After: after}); err != nil {
		return OpportunityDetail{}, err
	}
	return OpportunityDetailOf(ctx, tx, oid)
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// MoveStageInput moves an opportunity to another open stage.
type MoveStageInput struct {
	StageID uuid.UUID `json:"stageId"`
	Note    string    `json:"note,omitempty"`
}

// MoveStage moves an open opportunity; the move is recorded (FR-PIPE-03).
func (m *Module) MoveStage(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID, in MoveStageInput) (OpportunityDetail, error) {
	o, err := lockOpportunity(ctx, tx, property, oid)
	if err != nil {
		return OpportunityDetail{}, err
	}
	if o.Status != "open" {
		return OpportunityDetail{}, conflict("opportunity_closed", "the opportunity is "+o.Status)
	}
	st, err := stageOf(ctx, tx, in.StageID)
	if err != nil {
		return OpportunityDetail{}, err
	}
	if st.PipelineID != o.PipelineID {
		return OpportunityDetail{}, handle.Invalid("stageId", "invalid", "a stage of the opportunity's pipeline")
	}
	if st.Kind != "open" {
		return OpportunityDetail{}, handle.Invalid("stageId", "use_win_or_lose", "use Won / Lost to close the opportunity")
	}
	if st.ID == o.StageID {
		return OpportunityDetail{}, conflict("same_stage", "the opportunity is already in this stage")
	}
	if err := m.changeStage(ctx, tx, property, o, st, in.Note); err != nil {
		return OpportunityDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "move_stage", EntityType: "crm.opportunity", EntityID: oid.String(),
		EntityLabel: o.Number + " · " + o.Title, PropertyID: &property, Reason: in.Note,
		Before: map[string]any{"stage": o.StageName, "probability": o.Probability}, After: map[string]any{"stage": st.Name, "probability": st.Probability}}); err != nil {
		return OpportunityDetail{}, err
	}
	return OpportunityDetailOf(ctx, tx, oid)
}

func (m *Module) changeStage(ctx context.Context, tx pgx.Tx, property uuid.UUID, o Opportunity, st stageInfo, note string) error {
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_opportunities SET stage_id = $2, probability = $3::numeric, stage_changed_at = now(), updated_by = $4
		WHERE id = $1`, o.ID, st.ID, st.Probability.String(), actor(ctx)); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO crm.sales_stage_history (id, property_id, opportunity_id, from_stage_id, to_stage_id, probability, note, changed_by)
		VALUES ($1,$2,$3,$4,$5,$6::numeric,$7,$8)`, id.New(), property, o.ID, o.StageID, st.ID, st.Probability.String(), nullStr(note), actor(ctx))
	return err
}

// WinInput closes an opportunity as won.
type WinInput struct {
	QuotationID *uuid.UUID `json:"quotationId,omitempty" doc:"The accepted quotation (default: the latest accepted one of the opportunity, else of the customer without an opportunity)"`
	Note        string     `json:"note,omitempty"`
}

// WinOpportunity marks an opportunity won; it must be linked to an
// accepted quotation (FR-PIPE-04).
func (m *Module) WinOpportunity(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID, in WinInput) (OpportunityDetail, error) {
	o, err := lockOpportunity(ctx, tx, property, oid)
	if err != nil {
		return OpportunityDetail{}, err
	}
	if o.Status == "won" {
		return OpportunityDetail{}, conflict("already_won", "the opportunity is already won")
	}
	// The accepted quotation of the opportunity, or one accepted directly for
	// the same customer / company without an opportunity (it is linked).
	var qid uuid.UUID
	var linked bool
	err = tx.QueryRow(ctx, `SELECT id, opportunity_id IS NOT NULL FROM crm.sales_quotations WHERE property_id = $1 AND status = 'accepted'
		AND ($3::uuid IS NULL OR id = $3) AND (opportunity_id = $2 OR (opportunity_id IS NULL
		  AND (($4::uuid IS NOT NULL AND customer_id = $4) OR ($5::uuid IS NOT NULL AND corporate_account_id = $5))))
		ORDER BY (opportunity_id = $2) DESC NULLS LAST, accepted_at DESC LIMIT 1`, property, oid, in.QuotationID, o.CustomerID, o.CorporateAccountID).
		Scan(&qid, &linked)
	if dbtx.IsNoRows(err) {
		return OpportunityDetail{}, conflict("accepted_quotation_required", "a won opportunity needs an accepted quotation")
	}
	if err != nil {
		return OpportunityDetail{}, err
	}
	if !linked {
		if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET opportunity_id = $2 WHERE id = $1`, qid, oid); err != nil {
			return OpportunityDetail{}, err
		}
	}
	q, err := GetQuotation(ctx, tx, qid)
	if err != nil {
		return OpportunityDetail{}, err
	}
	if err := m.markWon(ctx, tx, property, o, q, in.Note); err != nil {
		return OpportunityDetail{}, err
	}
	return OpportunityDetailOf(ctx, tx, oid)
}

// markWon closes the opportunity as won with the accepted quotation.
func (m *Module) markWon(ctx context.Context, tx pgx.Tx, property uuid.UUID, o Opportunity, q Quotation, note string) error {
	st, err := stageOfKind(ctx, tx, o.PipelineID, "won")
	if err != nil {
		return err
	}
	if err := m.changeStage(ctx, tx, property, o, st, nonEmpty(note, "won with "+q.Number)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_opportunities SET status = 'won', won_at = now(), won_quotation_id = $2, expected_value = $3::numeric,
		lost_at = NULL, lost_reason = NULL, lost_note = NULL WHERE id = $1`, o.ID, q.ID, q.Total); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "win", EntityType: "crm.opportunity", EntityID: o.ID.String(),
		EntityLabel: o.Number + " · " + o.Title, PropertyID: &property, Reason: note, Before: map[string]any{"status": o.Status},
		After: map[string]any{"status": "won", "quotation": q.Number, "value": q.Total}}); err != nil {
		return err
	}
	var referrer *uuid.UUID
	if o.LeadID != nil {
		_ = tx.QueryRow(ctx, `SELECT referrer_customer_id FROM crm.sales_leads WHERE id = $1`, *o.LeadID).Scan(&referrer)
	}
	_, err = m.Events.Publish(ctx, tx, EventOpportunityWon, "crm.opportunity", &o.ID, &property, map[string]any{"opportunityId": o.ID,
		"number": o.Number, "quotationId": q.ID, "quotationNumber": q.Number, "customerId": q.CustomerID, "corporateAccountId": q.CorporateAccountID,
		"line": o.Line, "value": q.Total, "currency": q.Currency, "ownerUserId": o.OwnerUserID, "leadId": o.LeadID, "referrerCustomerId": referrer})
	return err
}

func nonEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// LoseInput closes an opportunity as lost (FR-PIPE-04).
type LoseInput struct {
	Reason string `json:"reason" enum:"price,date_unavailable,competitor,cancelled,budget,no_response,other"`
	Note   string `json:"note,omitempty"`
}

var lostReasons = []string{"price", "date_unavailable", "competitor", "cancelled", "budget", "no_response", "other"}

// LoseOpportunity marks an opportunity lost with a reason.
func (m *Module) LoseOpportunity(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID, in LoseInput) (OpportunityDetail, error) {
	o, err := lockOpportunity(ctx, tx, property, oid)
	if err != nil {
		return OpportunityDetail{}, err
	}
	if !oneOf(lostReasons, in.Reason) {
		return OpportunityDetail{}, enumErr("reason", lostReasons)
	}
	if o.Status != "open" {
		return OpportunityDetail{}, conflict("opportunity_closed", "the opportunity is "+o.Status)
	}
	st, err := stageOfKind(ctx, tx, o.PipelineID, "lost")
	if err != nil {
		return OpportunityDetail{}, err
	}
	if err := m.changeStage(ctx, tx, property, o, st, in.Reason+" "+in.Note); err != nil {
		return OpportunityDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_opportunities SET status = 'lost', lost_at = now(), lost_reason = $2, lost_note = $3 WHERE id = $1`,
		oid, in.Reason, nullStr(in.Note)); err != nil {
		return OpportunityDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "lose", EntityType: "crm.opportunity", EntityID: oid.String(),
		EntityLabel: o.Number + " · " + o.Title, PropertyID: &property, Reason: in.Reason + " " + in.Note, Before: map[string]any{"status": o.Status},
		After: map[string]any{"status": "lost", "reason": in.Reason}}); err != nil {
		return OpportunityDetail{}, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventOpportunityLost, "crm.opportunity", &oid, &property, map[string]any{"opportunityId": oid,
		"number": o.Number, "line": o.Line, "reason": in.Reason, "customerId": o.CustomerID, "ownerUserId": o.OwnerUserID}); err != nil {
		return OpportunityDetail{}, err
	}
	return OpportunityDetailOf(ctx, tx, oid)
}

// ReopenInput reopens a lost opportunity.
type ReopenInput struct {
	StageID *uuid.UUID `json:"stageId,omitempty" doc:"Default: the first open stage"`
	Note    string     `json:"note,omitempty"`
}

// ReopenOpportunity puts a lost opportunity back into the pipeline.
func (m *Module) ReopenOpportunity(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID, in ReopenInput) (OpportunityDetail, error) {
	o, err := lockOpportunity(ctx, tx, property, oid)
	if err != nil {
		return OpportunityDetail{}, err
	}
	if o.Status != "lost" {
		return OpportunityDetail{}, conflict("not_lost", "only lost opportunities can be reopened")
	}
	var st stageInfo
	if in.StageID != nil {
		if st, err = stageOf(ctx, tx, *in.StageID); err != nil {
			return OpportunityDetail{}, err
		}
		if st.PipelineID != o.PipelineID || st.Kind != "open" {
			return OpportunityDetail{}, handle.Invalid("stageId", "invalid", "an open stage of the pipeline")
		}
	} else if st, err = stageOfKind(ctx, tx, o.PipelineID, "open"); err != nil {
		return OpportunityDetail{}, err
	}
	if err := m.changeStage(ctx, tx, property, o, st, nonEmpty(in.Note, "reopened")); err != nil {
		return OpportunityDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_opportunities SET status = 'open', lost_at = NULL, lost_reason = NULL, lost_note = NULL WHERE id = $1`,
		oid); err != nil {
		return OpportunityDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "reopen", EntityType: "crm.opportunity", EntityID: oid.String(),
		EntityLabel: o.Number + " · " + o.Title, PropertyID: &property, Reason: in.Note, Before: map[string]any{"status": o.Status},
		After: map[string]any{"status": "open", "stage": st.Name}}); err != nil {
		return OpportunityDetail{}, err
	}
	return OpportunityDetailOf(ctx, tx, oid)
}

// listOpportunities lists opportunities with the standard filters.
func listOpportunities(ctx context.Context, q dbtx.Querier, property uuid.UUID, f map[string]string, text string, limit int, me *uuid.UUID) ([]Opportunity, error) {
	owner := f["ownerId"]
	if owner == "me" {
		owner = ""
		if me != nil {
			owner = me.String()
		}
	}
	return handle.List[Opportunity](q.Query(ctx, opportunitySelect+` WHERE o.property_id = $1 AND ($2 = '' OR o.status = $2)
		AND ($3 = '' OR o.pipeline_id::text = $3) AND ($4 = '' OR o.stage_id::text = $4) AND ($5 = '' OR o.line = $5)
		AND ($6 = '' OR o.owner_user_id::text = $6) AND ($7 = '' OR o.customer_id::text = $7)
		AND ($8 = '' OR o.title ILIKE '%' || $8 || '%' OR o.number ILIKE '%' || $8 || '%' OR c.name ILIKE '%' || $8 || '%' OR ca.name ILIKE '%' || $8 || '%')
		AND ($9 = '' OR o.expected_close_date >= NULLIF($9, '')::date) AND ($10 = '' OR o.expected_close_date <= NULLIF($10, '')::date)
		ORDER BY o.created_at DESC LIMIT $11`,
		property, f["status"], f["pipelineId"], f["stageId"], f["line"], owner, f["customerId"], text, f["closeFrom"], f["closeTo"], limit))
}

// ── kanban & forecast (FR-PIPE-03, FR-PIPE-07) ────────────────────────────

// PipelineBoardStage is one kanban column.
type PipelineBoardStage struct {
	StageID       uuid.UUID     `json:"stageId"`
	Code          string        `json:"code"`
	Name          string        `json:"name"`
	Kind          string        `json:"kind" enum:"open,won,lost"`
	Probability   string        `json:"probability"`
	Count         int           `json:"count"`
	Value         string        `json:"value"`
	WeightedValue string        `json:"weightedValue"`
	Opportunities []Opportunity `json:"opportunities"`
}

// PipelineBoard is the kanban of a pipeline.
type PipelineBoard struct {
	PipelineID    uuid.UUID            `json:"pipelineId"`
	PipelineName  string               `json:"pipelineName"`
	Stages        []PipelineBoardStage `json:"stages"`
	OpenCount     int                  `json:"openCount"`
	OpenValue     string               `json:"openValue"`
	WeightedValue string               `json:"weightedValue" doc:"Σ value × stage probability of the open opportunities"`
}

// BoardOf builds the kanban of a pipeline; won / lost columns show the
// deals closed within the filter period (default 90 days).
func BoardOf(ctx context.Context, q dbtx.Querier, property, pipeline uuid.UUID, f map[string]string, me *uuid.UUID) (PipelineBoard, error) {
	var b PipelineBoard
	if err := q.QueryRow(ctx, `SELECT id, name FROM crm.sales_pipelines WHERE id = $1 AND property_id = $2`, pipeline, property).
		Scan(&b.PipelineID, &b.PipelineName); err != nil {
		if dbtx.IsNoRows(err) {
			return b, errs.NotFound("pipeline")
		}
		return b, err
	}
	rows, err := q.Query(ctx, `SELECT id, code, name, kind, trim_scale(probability)::text FROM crm.sales_pipeline_stages WHERE pipeline_id = $1
		AND archived_at IS NULL AND status = 'active' ORDER BY sort_order, created_at`, pipeline)
	if err != nil {
		return b, err
	}
	for rows.Next() {
		var s PipelineBoardStage
		if err := rows.Scan(&s.StageID, &s.Code, &s.Name, &s.Kind, &s.Probability); err != nil {
			rows.Close()
			return b, err
		}
		s.Opportunities = []Opportunity{}
		b.Stages = append(b.Stages, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return b, err
	}
	ff := map[string]string{"pipelineId": pipeline.String(), "ownerId": f["ownerId"], "line": f["line"], "closeFrom": f["closeFrom"], "closeTo": f["closeTo"]}
	opps, err := listOpportunities(ctx, q, property, ff, f["q"], 2000, me)
	if err != nil {
		return b, err
	}
	since := clock.Now().AddDate(0, 0, -90)
	openValue, weighted := decimal.Zero, decimal.Zero
	for _, o := range opps {
		if o.Status != "open" && ((o.WonAt != nil && o.WonAt.Before(since)) || (o.LostAt != nil && o.LostAt.Before(since))) {
			continue
		}
		for i := range b.Stages {
			s := &b.Stages[i]
			if s.StageID != o.StageID {
				continue
			}
			s.Count++
			s.Opportunities = append(s.Opportunities, o)
			s.Value = dec(s.Value).Add(dec(o.ExpectedValue)).String()
			if o.Status == "open" {
				w := dec(o.ExpectedValue).Mul(dec(o.Probability)).Div(hundred)
				s.WeightedValue = dec(s.WeightedValue).Add(w).String()
				openValue = openValue.Add(dec(o.ExpectedValue))
				weighted = weighted.Add(w)
				b.OpenCount++
			}
		}
	}
	for i := range b.Stages {
		b.Stages[i].Value = dec(b.Stages[i].Value).Round(2).String()
		b.Stages[i].WeightedValue = dec(b.Stages[i].WeightedValue).Round(2).String()
	}
	b.OpenValue, b.WeightedValue = openValue.Round(2).String(), weighted.Round(2).String()
	return b, nil
}

// ForecastRow is the weighted forecast of one month and line.
type ForecastRow struct {
	Month         string `json:"month" db:"month" doc:"YYYY-MM of the expected close date"`
	Line          string `json:"line" db:"line"`
	Count         int    `json:"count" db:"count"`
	Value         string `json:"value" db:"value"`
	WeightedValue string `json:"weightedValue" db:"weighted_value"`
}

// Forecast is the weighted revenue forecast per month and line.
type Forecast struct {
	From          string        `json:"from"`
	To            string        `json:"to"`
	Rows          []ForecastRow `json:"rows"`
	Value         string        `json:"value"`
	WeightedValue string        `json:"weightedValue"`
}

// ForecastOf sums the open opportunities expected to close in [from, to].
func ForecastOf(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, line, owner string) (Forecast, error) {
	out := Forecast{From: from.Format("2006-01-02"), To: to.Format("2006-01-02")}
	rows, err := handle.List[ForecastRow](q.Query(ctx, `SELECT to_char(expected_close_date, 'YYYY-MM') AS month, line, count(*)::int AS count,
		trim_scale(sum(expected_value))::text AS value, trim_scale(round(sum(expected_value * probability / 100), 2))::text AS weighted_value
		FROM crm.sales_opportunities WHERE property_id = $1 AND status = 'open' AND expected_close_date BETWEEN $2 AND $3
		AND ($4 = '' OR line = $4) AND ($5 = '' OR owner_user_id::text = $5) GROUP BY 1, 2 ORDER BY 1, 2`, property, from, to, line, owner))
	if err != nil {
		return out, err
	}
	out.Rows = rows
	v, w := decimal.Zero, decimal.Zero
	for _, r := range rows {
		v, w = v.Add(dec(r.Value)), w.Add(dec(r.WeightedValue))
	}
	out.Value, out.WeightedValue = v.String(), w.String()
	return out, nil
}

// ── venue availability (FR-PIPE-06) ───────────────────────────────────────

// VenueBusy is a booked period of a venue.
type VenueBusy struct {
	Start  time.Time `json:"start" db:"start_at"`
	End    time.Time `json:"end" db:"end_at"`
	Status string    `json:"status" db:"status"`
}

// VenueAvailability is the availability of one venue around the event date.
type VenueAvailability struct {
	ResourceID   uuid.UUID   `json:"resourceId" db:"resource_id"`
	Code         string      `json:"code" db:"code"`
	Name         string      `json:"name" db:"name"`
	ResourceType string      `json:"resourceType" db:"resource_type"`
	Capacity     *int        `json:"capacity" db:"capacity"`
	Available    bool        `json:"available"`
	Busy         []VenueBusy `json:"busy"`
}

// VenueAvailabilityOf reads the reservation read models (reporting views)
// for the venue of the opportunity, or the venues of a type / with room for
// the pax, between from and to.
func VenueAvailabilityOf(ctx context.Context, q dbtx.Querier, o Opportunity, resourceType string, from, to time.Time) ([]VenueAvailability, error) {
	var where string
	args := []any{}
	switch {
	case o.VenueResourceID != nil && resourceType == "":
		where, args = `resource_id = $1`, append(args, *o.VenueResourceID)
	case resourceType != "":
		where, args = `resource_type = $1 AND ($2::int IS NULL OR capacity IS NULL OR capacity >= $2)`, append(args, resourceType, o.Pax)
	case o.Pax != nil:
		where, args = `capacity >= $1`, append(args, *o.Pax)
	default:
		return nil, handle.Invalid("resourceType", "required", "set the venue or the pax of the opportunity, or a resource type")
	}
	type venueRow struct {
		ResourceID   uuid.UUID `db:"resource_id"`
		Code         string    `db:"code"`
		Name         string    `db:"name"`
		ResourceType string    `db:"resource_type"`
		Capacity     *int      `db:"capacity"`
	}
	venues, err := handle.List[venueRow](q.Query(ctx, `SELECT resource_id, code, name, resource_type, capacity FROM reporting.sales_venues
		WHERE status = 'active' AND `+where+` ORDER BY name LIMIT 50`, args...))
	if err != nil {
		return nil, err
	}
	out := []VenueAvailability{}
	for _, v := range venues {
		busy, err := handle.List[VenueBusy](q.Query(ctx, `SELECT lower(period) AS start_at, upper(period) AS end_at, status FROM reporting.reservation_lines
			WHERE resource_id = $1 AND status NOT IN ('released', 'cancelled') AND period && tstzrange($2, $3) ORDER BY lower(period)`,
			v.ResourceID, from, to))
		if err != nil {
			return nil, err
		}
		out = append(out, VenueAvailability{ResourceID: v.ResourceID, Code: v.Code, Name: v.Name, ResourceType: v.ResourceType, Capacity: v.Capacity,
			Available: len(busy) == 0, Busy: busy})
	}
	return out, nil
}
