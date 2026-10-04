package crm

// PRD P2 CRM (EP-23/24): preferences, interactions, segments, feedback,
// campaigns and the Customer 360 sections of every business line, as P2
// sub-parts of P1's crm module (PRD P2 §5.4.1 crm/preference, crm/segment,
// crm/feedback). P1's Customer, overview and routes are unchanged; P2 keeps
// its own service (Engagement) and reads its customer attributes through
// CustomerProfile. Review: P1 developer.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/resource"
)

// Engagement is the P2 CRM service on top of P1's crm module.
type Engagement struct {
	*Module
	Notify    notify.Sender
	Sections  map[string]SectionFunc  // Customer 360 sections of the business lines (wired in internal/app)
	Behavior  map[string]BehaviorFunc // behaviour facts per business line
	Facts     FactsFunc               // segmentation facts (spend, visits, membership)
	PublicURL string                  // base of the public feedback link (website)
}

// CustomerProfile is a customer with the P2 eligibility and personalisation
// attributes (crm/00003).
type CustomerProfile struct {
	Customer
	CustomerType      string     `json:"customerType" db:"customer_type"`
	Student           bool       `json:"student" db:"student"`
	StudentValidUntil *time.Time `json:"studentValidUntil" db:"student_valid_until"`
	MaritalStatus     string     `json:"maritalStatus" db:"marital_status"`
	Locale            string     `json:"locale" db:"locale"`
	MarketingOptIn    bool       `json:"marketingOptIn" db:"marketing_opt_in"`
	ConsentProfiling  bool       `json:"consentProfiling" db:"consent_profiling"`
}

const profileCols = `id, property_id, code, name, coalesce(email, ''), coalesce(phone, ''), coalesce(gender, ''), birth_date, resident, user_id, status,
	customer_type, student, student_valid_until, coalesce(marital_status, ''), coalesce(locale, ''), marketing_opt_in, consent_profiling`

func scanProfile(row pgx.Row) (CustomerProfile, error) {
	var p CustomerProfile
	c := &p.Customer
	err := row.Scan(&c.ID, &c.PropertyID, &c.Code, &c.Name, &c.Email, &c.Phone, &c.Gender, &c.BirthDate, &c.Resident, &c.UserID, &c.Status,
		&p.CustomerType, &p.Student, &p.StudentValidUntil, &p.MaritalStatus, &p.Locale, &p.MarketingOptIn, &p.ConsentProfiling)
	return p, err
}

// GetCustomerProfile returns a customer with the P2 attributes.
func GetCustomerProfile(ctx context.Context, q dbtx.Querier, id uuid.UUID) (CustomerProfile, error) {
	p, err := scanProfile(q.QueryRow(ctx, `SELECT `+profileCols+` FROM crm.customers WHERE id = $1`, id))
	if dbtx.IsNoRows(err) {
		return p, errs.NotFound("customer")
	}
	return p, err
}

// profiles returns the customers of a property with the P2 attributes.
func profiles(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]CustomerProfile, error) {
	rows, err := q.Query(ctx, `SELECT `+profileCols+` FROM crm.customers WHERE property_id = $1 AND status = 'active'`, property)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CustomerProfile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AgeOn is Age with an explicit "known" flag (eligibility rules need both).
func AgeOn(c Customer, day time.Time) (int, bool) {
	a := c.Age(day)
	return a, a >= 0
}

// Identity identifies a person from a website / app form.
type Identity struct {
	Name  string
	Phone string
	Email string
}

// FindOrCreate returns the customer with the same phone or e-mail (dedup,
// FR-CUS-03 / FR-WEB-08), the customer an upgraded guest became, or a new
// customer (website consent recorded). The bool reports a new profile.
func FindOrCreate(ctx context.Context, tx pgx.Tx, property uuid.UUID, in Identity) (Customer, bool, error) {
	in.Name = strings.TrimSpace(in.Name)
	phone := NormalizePhone(in.Phone)
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if phone == "" && email == "" {
		return Customer{}, false, errs.Validation("identity_required", "phone or e-mail is required", errs.Field("phone", "required", "phone or e-mail is required"))
	}
	dups, err := FindDuplicates(ctx, tx, property, phone, email, "")
	if err != nil {
		return Customer{}, false, err
	}
	if len(dups) > 0 {
		c, err := GetCustomer(ctx, tx, dups[0].ID)
		return c, false, err
	}
	if phone != "" {
		var cid *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT customer_id FROM crm.guests WHERE property_id = $1 AND phone = $2 AND customer_id IS NOT NULL
			AND erased_at IS NULL ORDER BY created_at LIMIT 1`, property, phone).Scan(&cid)
		if err == nil && cid != nil {
			c, err := GetCustomer(ctx, tx, *cid)
			return c, false, err
		}
		if err != nil && !dbtx.IsNoRows(err) {
			return Customer{}, false, err
		}
	}
	if in.Name == "" {
		return Customer{}, false, errs.Validation("name_required", "name is required", errs.Field("name", "required", "name is required"))
	}
	c, err := CreateCustomer(ctx, tx, property, NewCustomer{Name: in.Name, Email: email, Phone: phone, ConsentChannel: "website"})
	return c, true, err
}

// P2 fields and preference categories on P1's resources (additive).
var (
	p2CustomerFields = []resource.Field{
		{Name: "residentRef", Column: "resident_ref", Label: "Resident Reference", Kind: resource.String, Max: 80},
		{Name: "student", Column: "student", Label: "Student", Kind: resource.Bool, Default: false},
		{Name: "studentValidUntil", Column: "student_valid_until", Label: "Student Card Valid Until", Kind: resource.Date},
		{Name: "maritalStatus", Column: "marital_status", Label: "Marital Status", Kind: resource.Enum, Enum: []string{"single", "married"}},
		{Name: "locale", Column: "locale", Label: "Language", Kind: resource.Enum, Enum: []string{"en", "id"}},
		{Name: "consentProfiling", Column: "consent_profiling", Label: "Profiling Consent (UU PDP)", Kind: resource.Bool, Default: false},
	}
	p2PreferenceCategories = []string{"favorite_caddy", "tee_time", "diet", "allergy", "food", "beverage", "facility", "note"}
	p2PreferenceFields     = []resource.Field{
		{Name: "source", Column: "source", Label: "Recorded By", Kind: resource.Enum, Enum: []string{"staff", "caddy", "member", "system"}, ReadOnly: true, Filter: true},
		{Name: "sensitive", Column: "sensitive", Label: "Health Data", Kind: resource.Bool, ReadOnly: true},
	}
)

func init() {
	Customers.Fields = append(Customers.Fields, p2CustomerFields...)
	for i := range Preferences.Fields {
		if Preferences.Fields[i].Name == "category" {
			Preferences.Fields[i].Enum = append(Preferences.Fields[i].Enum, p2PreferenceCategories...)
		}
	}
	Preferences.Fields = append(Preferences.Fields, p2PreferenceFields...)
	if Preferences.Hooks.AfterRead == nil {
		Preferences.Hooks.AfterRead = maskPreference
	}
}

// maskPreference hides health preferences (diet, allergy) from callers
// without crm.preference.view_sensitive (FR-PRF-05).
func maskPreference(ctx context.Context, row map[string]any) {
	if s, _ := row["sensitive"].(bool); !s || sensitiveFor(ctx) {
		return
	}
	row["key"], row["value"], row["notes"] = "restricted", nil, nil
}

// RegisterP2 adds the P2 CRM routes (wired by internal/app).
func (m *Engagement) RegisterP2(reg *route.Registry, eng *resource.Engine) {
	m.registerEngagement(reg, eng)
	m.registerMe(reg)
	m.registerPublic(reg)
}

// EngagementContribution is the P2 part of the CRM catalogue.
func EngagementContribution() catalog.Contribution { return engagementContribution() }
