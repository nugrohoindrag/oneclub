package crm

// Public interface of the CRM module (Technical Doc §4.2): other modules
// (membership, billing, golf) call these functions inside their own
// transaction; they never query the crm schema directly.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
)

// Customer is the profile other modules need.
type Customer struct {
	ID         uuid.UUID  `json:"id"`
	PropertyID uuid.UUID  `json:"propertyId"`
	Code       string     `json:"code"`
	Name       string     `json:"name"`
	Email      string     `json:"email"`
	Phone      string     `json:"phone"`
	Gender     string     `json:"gender"`
	BirthDate  *time.Time `json:"birthDate"`
	Resident   bool       `json:"resident"`
	UserID     *uuid.UUID `json:"userId"`
	Status     string     `json:"status"`
}

// Age returns the age in whole years on day (-1 when the birth date is
// unknown).
func (c Customer) Age(day time.Time) int {
	if c.BirthDate == nil {
		return -1
	}
	b := *c.BirthDate
	age := day.Year() - b.Year()
	if day.Month() < b.Month() || (day.Month() == b.Month() && day.Day() < b.Day()) {
		age--
	}
	return age
}

const customerCols = `id, property_id, code, name, coalesce(email, ''), coalesce(phone, ''), coalesce(gender, ''), birth_date, resident, user_id, status`

func scanCustomer(row pgx.Row) (Customer, error) {
	var c Customer
	err := row.Scan(&c.ID, &c.PropertyID, &c.Code, &c.Name, &c.Email, &c.Phone, &c.Gender, &c.BirthDate, &c.Resident, &c.UserID, &c.Status)
	return c, err
}

// GetCustomer loads a customer (a merged profile resolves to its target).
func GetCustomer(ctx context.Context, q dbtx.Querier, cid uuid.UUID) (Customer, error) {
	var merged *uuid.UUID
	if err := q.QueryRow(ctx, `SELECT merged_into_id FROM crm.customers WHERE id = $1`, cid).Scan(&merged); err != nil {
		if dbtx.IsNoRows(err) {
			return Customer{}, errs.NotFound("customer")
		}
		return Customer{}, err
	}
	if merged != nil {
		cid = *merged
	}
	c, err := scanCustomer(q.QueryRow(ctx, `SELECT `+customerCols+` FROM crm.customers WHERE id = $1`, cid))
	if dbtx.IsNoRows(err) {
		return c, errs.NotFound("customer")
	}
	return c, err
}

// CustomerByUser returns the customer profile linked to a portal user.
func CustomerByUser(ctx context.Context, q dbtx.Querier, userID uuid.UUID) (Customer, error) {
	c, err := scanCustomer(q.QueryRow(ctx, `SELECT `+customerCols+` FROM crm.customers WHERE user_id = $1 AND status <> 'merged'`, userID))
	if dbtx.IsNoRows(err) {
		return c, errs.Forbidden("this account is not linked to a customer profile")
	}
	return c, err
}

// LinkUser links a portal user to the customer.
func LinkUser(ctx context.Context, tx pgx.Tx, customerID, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE crm.customers SET user_id = $2 WHERE id = $1 AND (user_id IS NULL OR user_id = $2)`, customerID, userID)
	return err
}

// Duplicate is a possible duplicate profile.
type Duplicate struct {
	ID        uuid.UUID `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind" enum:"customer,guest"`
	MatchedOn string    `json:"matchedOn" enum:"phone,email"`
}

// FindDuplicates looks for active customers with the same phone or e-mail
// at the property (FR-CUS-03).
func FindDuplicates(ctx context.Context, q dbtx.Querier, property uuid.UUID, phone, email, excludeID string) ([]Duplicate, error) {
	phone = NormalizePhone(phone)
	email = strings.ToLower(strings.TrimSpace(email))
	if phone == "" && email == "" {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id, code, name, CASE WHEN $2 <> '' AND phone = $2 THEN 'phone' ELSE 'email' END
		FROM crm.customers
		WHERE property_id = $1 AND status <> 'merged' AND archived_at IS NULL AND erased_at IS NULL
		  AND (($2 <> '' AND phone = $2) OR ($3 <> '' AND lower(email) = $3))
		  AND ($4 = '' OR id <> $4::uuid)
		ORDER BY created_at LIMIT 5`, property, phone, email, excludeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Duplicate
	for rows.Next() {
		d := Duplicate{Kind: "customer"}
		if err := rows.Scan(&d.ID, &d.Code, &d.Name, &d.MatchedOn); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// NewCustomer creates a customer profile (website guests who give full
// details, membership applicants, migration).
type NewCustomer struct {
	Code           string
	Name           string
	Email          string
	Phone          string
	Gender         string
	BirthDate      *time.Time
	Resident       bool
	IDNumber       string
	ConsentChannel string // set when consent was given (UU PDP)
	LegacyRef      string
}

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// CreateCustomer inserts a customer and records audit.
func CreateCustomer(ctx context.Context, tx pgx.Tx, property uuid.UUID, n NewCustomer) (Customer, error) {
	cid := id.New()
	if n.Code == "" {
		n.Code = "C" + strings.ToUpper(cid.String()[24:])
	}
	var consentAt *time.Time
	if n.ConsentChannel != "" {
		t := time.Now().UTC()
		consentAt = &t
	}
	var gender *string
	if n.Gender == "male" || n.Gender == "female" {
		gender = &n.Gender
	}
	uid := actorID(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO crm.customers (id, property_id, code, name, email, phone, gender, birth_date, resident, id_number,
		consent_at, consent_channel, legacy_ref, duplicate_acknowledged, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,true,$14,$14)`,
		cid, property, strings.ToUpper(n.Code), n.Name, nullStr(strings.ToLower(n.Email)), nullStr(NormalizePhone(n.Phone)), gender, n.BirthDate,
		n.Resident, nullStr(n.IDNumber), consentAt, nullStr(n.ConsentChannel), nullStr(n.LegacyRef), id.Ptr(uid)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return Customer{}, errs.Conflict("duplicate", "a customer with the same code already exists")
		}
		return Customer{}, err
	}
	c, err := GetCustomer(ctx, tx, cid)
	if err != nil {
		return c, err
	}
	return c, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.customer", EntityID: cid.String(),
		EntityLabel: c.Code + " · " + c.Name, PropertyID: &property, After: c})
}

// GuestRef is the identity used for a booking player or booker.
type GuestRef struct {
	CustomerID *uuid.UUID `json:"customerId"`
	GuestID    *uuid.UUID `json:"guestId"`
	Name       string     `json:"name"`
	Phone      string     `json:"phone"`
	Email      string     `json:"email"`
}

// GuestInput is a minimal identity (name + phone, optional e-mail).
type GuestInput struct {
	Name    string
	Phone   string
	Email   string
	Consent bool
}

// ResolveGuest returns the existing customer or guest with the same phone /
// e-mail, or creates a Guest (dedup for website bookings, FR-WEB-08).
func ResolveGuest(ctx context.Context, tx pgx.Tx, property uuid.UUID, in GuestInput) (GuestRef, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return GuestRef{}, errs.Validation("name_required", "guest name is required", errs.Field("name", "required", "name is required"))
	}
	phone := NormalizePhone(in.Phone)
	email := strings.ToLower(strings.TrimSpace(in.Email))
	dups, err := FindDuplicates(ctx, tx, property, phone, email, "")
	if err != nil {
		return GuestRef{}, err
	}
	if len(dups) > 0 {
		cid := dups[0].ID
		if in.Consent {
			if _, err := tx.Exec(ctx, `UPDATE crm.customers SET consent_at = coalesce(consent_at, now()), consent_channel = coalesce(consent_channel, 'website') WHERE id = $1`, cid); err != nil {
				return GuestRef{}, err
			}
		}
		return GuestRef{CustomerID: &cid, Name: dups[0].Name, Phone: phone, Email: email}, nil
	}
	if phone != "" {
		var gid uuid.UUID
		var cust *uuid.UUID
		var name string
		err := tx.QueryRow(ctx, `SELECT id, customer_id, name FROM crm.guests WHERE property_id = $1 AND phone = $2 AND erased_at IS NULL
			ORDER BY created_at LIMIT 1`, property, phone).Scan(&gid, &cust, &name)
		if err == nil {
			if email != "" {
				if _, err := tx.Exec(ctx, `UPDATE crm.guests SET email = coalesce(email, $2) WHERE id = $1`, gid, email); err != nil {
					return GuestRef{}, err
				}
			}
			return GuestRef{CustomerID: cust, GuestID: &gid, Name: name, Phone: phone, Email: email}, nil
		}
		if !dbtx.IsNoRows(err) {
			return GuestRef{}, err
		}
	}
	gid := id.New()
	var consentAt *time.Time
	if in.Consent {
		t := time.Now().UTC()
		consentAt = &t
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.guests (id, property_id, code, name, phone, email, consent_at, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, gid, property, "G"+strings.ToUpper(gid.String()[24:]), in.Name, nullStr(phone), nullStr(email),
		consentAt, id.Ptr(actorID(ctx))); err != nil {
		return GuestRef{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.guest", EntityID: gid.String(),
		EntityLabel: in.Name, PropertyID: &property, After: map[string]any{"name": in.Name, "phone": phone, "email": email}}); err != nil {
		return GuestRef{}, err
	}
	return GuestRef{GuestID: &gid, Name: in.Name, Phone: phone, Email: email}, nil
}

// FamilyOf returns active family relationships (both directions).
type Relative struct {
	CustomerID   uuid.UUID `json:"customerId"`
	Name         string    `json:"name"`
	Relationship string    `json:"relationship"`
}

func FamilyOf(ctx context.Context, q dbtx.Querier, customerID uuid.UUID) ([]Relative, error) {
	rows, err := q.Query(ctx, `
		SELECT c.id, c.name, r.relationship FROM crm.customer_relationships r JOIN crm.customers c ON c.id = r.related_customer_id
		WHERE r.customer_id = $1 AND r.status = 'active'
		UNION
		SELECT c.id, c.name, CASE r.relationship WHEN 'child' THEN 'parent' WHEN 'parent' THEN 'child' ELSE r.relationship END
		FROM crm.customer_relationships r JOIN crm.customers c ON c.id = r.customer_id
		WHERE r.related_customer_id = $1 AND r.status = 'active'`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Relative
	for rows.Next() {
		var r Relative
		if err := rows.Scan(&r.CustomerID, &r.Name, &r.Relationship); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// IsNominee reports whether the customer is an active nominee of the
// corporate account.
func IsNominee(ctx context.Context, q dbtx.Querier, corporateID, customerID uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.corporate_nominees WHERE corporate_account_id = $1 AND customer_id = $2
		AND status = 'active' AND (ends_on IS NULL OR ends_on >= billing.local_date(property_id)))`, corporateID, customerID).Scan(&ok)
	return ok, err
}

// CorporateName returns a corporate account name.
func CorporateName(ctx context.Context, q dbtx.Querier, corporateID uuid.UUID) (string, error) {
	var name string
	err := q.QueryRow(ctx, `SELECT name FROM crm.corporate_accounts WHERE id = $1 AND status = 'active'`, corporateID).Scan(&name)
	if dbtx.IsNoRows(err) {
		return "", errs.Validation("invalid_corporate_account", "corporate account not found or inactive",
			errs.Field("corporateAccountId", "not_found", "corporate account not found"))
	}
	return name, err
}

func actorID(ctx context.Context) uuid.UUID {
	if p := authz.From(ctx); p != nil {
		return p.UserID
	}
	return uuid.Nil
}

// Names returns display names of customers by id.
func Names(ctx context.Context, q dbtx.Querier, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id, name FROM crm.customers WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid uuid.UUID
		var n string
		if err := rows.Scan(&cid, &n); err != nil {
			return nil, err
		}
		out[cid] = n
	}
	return out, rows.Err()
}

// SearchCustomerIDs returns customers whose name, code, phone or e-mail
// match text.
func SearchCustomerIDs(ctx context.Context, q dbtx.Querier, property uuid.UUID, text string, limit int) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT id FROM crm.customers WHERE property_id = $1 AND status <> 'merged'
		AND (name ILIKE $2 OR code ILIKE $2 OR phone ILIKE $2 OR email ILIKE $2) ORDER BY name LIMIT $3`, property, "%"+text+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var cid uuid.UUID
		if err := rows.Scan(&cid); err != nil {
			return nil, err
		}
		out = append(out, cid)
	}
	return out, rows.Err()
}

// Contact returns the name, e-mail and phone of a customer or guest.
func Contact(ctx context.Context, q dbtx.Querier, customerID, guestID *uuid.UUID) (name, email, phone string, err error) {
	var e, p *string
	switch {
	case customerID != nil:
		err = q.QueryRow(ctx, `SELECT name, email, phone FROM crm.customers WHERE id = $1`, *customerID).Scan(&name, &e, &p)
	case guestID != nil:
		err = q.QueryRow(ctx, `SELECT name, email, phone FROM crm.guests WHERE id = $1`, *guestID).Scan(&name, &e, &p)
	default:
		return "", "", "", nil
	}
	if dbtx.IsNoRows(err) {
		return "", "", "", nil
	}
	if e != nil {
		email = *e
	}
	if p != nil {
		phone = *p
	}
	return name, email, phone, err
}

// ExistsInProperty reports whether a customer exists at the property.
func ExistsInProperty(ctx context.Context, q dbtx.Querier, property, customerID uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.customers WHERE id = $1 AND property_id = $2)`, customerID, property).Scan(&ok)
	return ok, err
}

// EnsureRelationship records a family relationship (idempotent).
func EnsureRelationship(ctx context.Context, tx pgx.Tx, property, customerID, relatedID uuid.UUID, relationship string) error {
	if customerID == relatedID {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO crm.customer_relationships (id, property_id, customer_id, related_customer_id, relationship, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$6) ON CONFLICT (customer_id, related_customer_id, relationship) DO UPDATE SET status = 'active'`,
		id.New(), property, customerID, relatedID, relationship, id.Ptr(actorID(ctx)))
	return err
}

// Consent returns the consent time and marketing preference.
func Consent(ctx context.Context, q dbtx.Querier, customerID uuid.UUID, consentAt **time.Time, marketing *bool) error {
	return q.QueryRow(ctx, `SELECT consent_at, marketing_opt_in FROM crm.customers WHERE id = $1`, customerID).Scan(consentAt, marketing)
}

// SelfUpdate are the fields a member may change in the Member Portal.
type SelfUpdate struct {
	Phone          *string
	MarketingOptIn *bool
	Consent        *bool
}

// UpdateSelf applies a Member Portal profile change (audited).
func UpdateSelf(ctx context.Context, tx pgx.Tx, customerID uuid.UUID, u SelfUpdate) error {
	var property uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM crm.customers WHERE id = $1 FOR UPDATE`, customerID).Scan(&property); err != nil {
		return err
	}
	if u.Phone != nil {
		p := strings.TrimSpace(*u.Phone)
		if p != "" && !phoneRe.MatchString(p) {
			return errs.Validation("invalid_phone", "invalid phone number", errs.Field("phone", "invalid", "digits, spaces, + ( ) - only"))
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.customers SET phone = $2 WHERE id = $1`, customerID, nullStr(NormalizePhone(p))); err != nil {
			return err
		}
	}
	if u.MarketingOptIn != nil {
		if _, err := tx.Exec(ctx, `UPDATE crm.customers SET marketing_opt_in = $2 WHERE id = $1`, customerID, *u.MarketingOptIn); err != nil {
			return err
		}
	}
	if u.Consent != nil {
		if *u.Consent {
			if _, err := tx.Exec(ctx, `UPDATE crm.customers SET consent_at = coalesce(consent_at, now()), consent_channel = coalesce(consent_channel, 'member_portal') WHERE id = $1`, customerID); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE crm.customers SET consent_at = NULL, marketing_opt_in = false WHERE id = $1`, customerID); err != nil {
			return err
		}
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionUpdate, EntityType: "crm.customer", EntityID: customerID.String(),
		PropertyID: &property, After: map[string]any{"phoneChanged": u.Phone != nil, "marketingOptIn": u.MarketingOptIn, "consent": u.Consent}})
}

// ByLegacyRef resolves a migrated customer or corporate account by its
// Rhapsody reference (EP-18). table is "customers" or "corporate_accounts".
func ByLegacyRef(ctx context.Context, q dbtx.Querier, property uuid.UUID, table, ref string) (*uuid.UUID, error) {
	if table != "customers" && table != "corporate_accounts" {
		return nil, errs.BadRequest("invalid_table", "unknown CRM table")
	}
	var out uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM crm.`+table+` WHERE property_id = $1 AND legacy_ref = $2 AND archived_at IS NULL LIMIT 1`, property, ref).Scan(&out)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// LinkNominee records a customer as nominee of a corporate account
// (idempotent; Rhapsody migration and membership activation).
func LinkNominee(ctx context.Context, tx pgx.Tx, property, corporateID, customerID uuid.UUID, title string) error {
	_, err := tx.Exec(ctx, `INSERT INTO crm.corporate_nominees (id, property_id, corporate_account_id, customer_id, title)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT (corporate_account_id, customer_id) DO UPDATE SET status = 'active'`,
		id.New(), property, corporateID, customerID, nullIfEmpty(title))
	return err
}

func nullIfEmpty(s string) *string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	return &s
}
