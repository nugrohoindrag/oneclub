package crm

// Customer 360 (Basic) and Customer History (FR-CUS-03, FR-CUS-04) and the
// personal data export (FR-CUS-09). Cross-domain data (memberships,
// rounds, payments, account balance, handicap) is read through the
// reporting read models only (Technical Doc §4.2).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/storage"
)

// Profile is the identity block of Customer 360.
type Profile struct {
	ID             uuid.UUID  `json:"id"`
	Code           string     `json:"code"`
	Name           string     `json:"name"`
	CustomerType   string     `json:"customerType" enum:"individual,corporate"`
	Email          *string    `json:"email"`
	Phone          *string    `json:"phone"`
	Gender         *string    `json:"gender"`
	BirthDate      *time.Time `json:"birthDate"`
	Address        *string    `json:"address"`
	City           *string    `json:"city"`
	IDNumber       *string    `json:"idNumber" doc:"Masked unless crm.customer.view_sensitive"`
	PhotoFileID    *uuid.UUID `json:"photoFileId"`
	Resident       bool       `json:"resident"`
	ConsentAt      *time.Time `json:"consentAt"`
	MarketingOptIn bool       `json:"marketingOptIn"`
	Status         string     `json:"status"`
	ErasedAt       *time.Time `json:"erasedAt"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// OverviewMembership is a membership of the customer.
type OverviewMembership struct {
	MembershipID uuid.UUID  `json:"membershipId"`
	MemberID     uuid.UUID  `json:"memberId"`
	MemberNo     string     `json:"memberNo"`
	TypeName     string     `json:"typeName"`
	Category     string     `json:"category"`
	Role         string     `json:"role"`
	Status       string     `json:"status"`
	StartsOn     *time.Time `json:"startsOn"`
	EndsOn       *time.Time `json:"endsOn"`
}

// OverviewAccount is a customer account with its balance.
type OverviewAccount struct {
	AccountID   uuid.UUID `json:"accountId"`
	Number      string    `json:"number"`
	AccountType string    `json:"accountType"`
	Balance     float64   `json:"balance"`
	CreditLimit *float64  `json:"creditLimit"`
	Status      string    `json:"status"`
}

// OverviewRelation is a family relationship.
type OverviewRelation struct {
	CustomerID   uuid.UUID `json:"customerId"`
	Name         string    `json:"name"`
	Relationship string    `json:"relationship"`
}

// OverviewPreference is a stored preference.
type OverviewPreference struct {
	Category string  `json:"category"`
	Key      string  `json:"key"`
	Value    *string `json:"value"`
}

// HistoryItem is one line of the Customer History.
type HistoryItem struct {
	Kind        string    `json:"kind" enum:"round,payment,member_charge"`
	OccurredAt  time.Time `json:"occurredAt"`
	Reference   string    `json:"reference"`
	Description string    `json:"description"`
	Amount      float64   `json:"amount"`
	Status      string    `json:"status"`
}

// OverviewStats summarises the relationship with the club.
type OverviewStats struct {
	Rounds        int64      `json:"rounds"`
	LastVisit     *time.Time `json:"lastVisit"`
	TotalPayments float64    `json:"totalPayments"`
	NoShows       int64      `json:"noShows"`
}

// Overview is Customer 360 (Basic).
type Overview struct {
	Profile       Profile              `json:"profile"`
	HandicapIndex *float64             `json:"handicapIndex"`
	Memberships   []OverviewMembership `json:"memberships"`
	Accounts      []OverviewAccount    `json:"accounts"`
	Relationships []OverviewRelation   `json:"relationships"`
	Preferences   []OverviewPreference `json:"preferences"`
	Stats         OverviewStats        `json:"stats"`
	RecentHistory []HistoryItem        `json:"recentHistory"`
	// Relationship is filled by Customer 360 only (not in the personal data export).
	Relationship *OverviewRelationship `json:"relationship,omitempty"`
}

func loadProfile(ctx context.Context, tx pgx.Tx, cid, pid uuid.UUID) (Profile, error) {
	var p Profile
	err := tx.QueryRow(ctx, `SELECT id, code, name, customer_type, email, phone, gender, birth_date, address, city, id_number, photo_file_id, resident,
		consent_at, marketing_opt_in, status, erased_at, created_at FROM crm.customers WHERE id = $1 AND property_id = $2`, cid, pid).
		Scan(&p.ID, &p.Code, &p.Name, &p.CustomerType, &p.Email, &p.Phone, &p.Gender, &p.BirthDate, &p.Address, &p.City, &p.IDNumber, &p.PhotoFileID,
			&p.Resident, &p.ConsentAt, &p.MarketingOptIn, &p.Status, &p.ErasedAt, &p.CreatedAt)
	if dbtx.IsNoRows(err) {
		return p, errs.NotFound("customer")
	}
	return p, err
}

func history(ctx context.Context, tx pgx.Tx, cid uuid.UUID, kind string, from, to *time.Time, limit int) ([]HistoryItem, error) {
	out := []HistoryItem{}
	rows, err := tx.Query(ctx, `SELECT kind, occurred_at, reference, coalesce(description, ''), coalesce(amount, 0)::float8, coalesce(status, '')
		FROM reporting.customer_history WHERE customer_id = $1 AND ($2 = '' OR kind = $2)
		AND ($3::timestamptz IS NULL OR occurred_at >= $3) AND ($4::timestamptz IS NULL OR occurred_at < $4)
		ORDER BY occurred_at DESC LIMIT $5`, cid, kind, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h HistoryItem
		if err := rows.Scan(&h.Kind, &h.OccurredAt, &h.Reference, &h.Description, &h.Amount, &h.Status); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func buildOverview(ctx context.Context, tx pgx.Tx, cid, pid uuid.UUID, historyLimit int) (Overview, error) {
	o := Overview{Memberships: []OverviewMembership{}, Accounts: []OverviewAccount{}, Relationships: []OverviewRelation{}, Preferences: []OverviewPreference{}}
	var err error
	if o.Profile, err = loadProfile(ctx, tx, cid, pid); err != nil {
		return o, err
	}
	if err := tx.QueryRow(ctx, `SELECT handicap_index::float8 FROM reporting.handicaps WHERE customer_id = $1`, cid).Scan(&o.HandicapIndex); err != nil && !dbtx.IsNoRows(err) {
		return o, err
	}
	rows, err := tx.Query(ctx, `SELECT membership_id, member_id, member_no, type_name, category, role, status, starts_on, ends_on
		FROM reporting.memberships WHERE customer_id = $1 ORDER BY (status = 'active') DESC, starts_on DESC NULLS LAST`, cid)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var m OverviewMembership
		if err := rows.Scan(&m.MembershipID, &m.MemberID, &m.MemberNo, &m.TypeName, &m.Category, &m.Role, &m.Status, &m.StartsOn, &m.EndsOn); err != nil {
			rows.Close()
			return o, err
		}
		o.Memberships = append(o.Memberships, m)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT account_id, number, account_type, balance::float8, credit_limit::float8, status FROM reporting.member_accounts
		WHERE customer_id = $1 ORDER BY number`, cid)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var a OverviewAccount
		if err := rows.Scan(&a.AccountID, &a.Number, &a.AccountType, &a.Balance, &a.CreditLimit, &a.Status); err != nil {
			rows.Close()
			return o, err
		}
		o.Accounts = append(o.Accounts, a)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT c.id, c.name, r.relationship FROM crm.customer_relationships r JOIN crm.customers c ON c.id = r.related_customer_id
		WHERE r.customer_id = $1 AND r.status = 'active'
		UNION ALL
		SELECT c.id, c.name, CASE r.relationship WHEN 'child' THEN 'parent' WHEN 'parent' THEN 'child' ELSE r.relationship END
		FROM crm.customer_relationships r JOIN crm.customers c ON c.id = r.customer_id WHERE r.related_customer_id = $1 AND r.status = 'active'`, cid)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var r OverviewRelation
		if err := rows.Scan(&r.CustomerID, &r.Name, &r.Relationship); err != nil {
			rows.Close()
			return o, err
		}
		o.Relationships = append(o.Relationships, r)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT category, pref_key, pref_value FROM crm.customer_preferences WHERE customer_id = $1 AND status = 'active' ORDER BY category, pref_key`, cid)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var p OverviewPreference
		if err := rows.Scan(&p.Category, &p.Key, &p.Value); err != nil {
			rows.Close()
			return o, err
		}
		o.Preferences = append(o.Preferences, p)
	}
	rows.Close()
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE kind = 'round' AND status IN ('checked_in', 'booked', 'completed')),
		max(occurred_at) FILTER (WHERE kind = 'round' AND occurred_at <= now()),
		coalesce(sum(amount) FILTER (WHERE kind = 'payment' AND status IN ('completed', 'refunded')), 0)::float8,
		count(*) FILTER (WHERE kind = 'round' AND status = 'no_show')
		FROM reporting.customer_history WHERE customer_id = $1`, cid).Scan(&o.Stats.Rounds, &o.Stats.LastVisit, &o.Stats.TotalPayments, &o.Stats.NoShows); err != nil {
		return o, err
	}
	o.RecentHistory, err = history(ctx, tx, cid, "", nil, nil, historyLimit)
	return o, err
}

func (m *Module) overview(w http.ResponseWriter, r *http.Request) {
	cid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	var out Overview
	err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		cid, err = resolveMerged(ctx, tx, cid)
		if err != nil {
			return err
		}
		if out, err = buildOverview(ctx, tx, cid, pid, 20); err != nil {
			return err
		}
		rel, err := loadRelationship(ctx, tx, cid, pid)
		out.Relationship = &rel
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if out.Profile.IDNumber != nil && *out.Profile.IDNumber != "" && !canSensitive(ctx, nil) {
		v := mask.Phone(*out.Profile.IDNumber)
		out.Profile.IDNumber = &v
	}
	hideSensitive(&out, sensitiveFor(ctx)) // health preferences (FR-PRF-05)
	httpx.JSON(w, http.StatusOK, out)
}

func resolveMerged(ctx context.Context, tx pgx.Tx, cid uuid.UUID) (uuid.UUID, error) {
	var merged *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT merged_into_id FROM crm.customers WHERE id = $1`, cid).Scan(&merged); err != nil {
		if dbtx.IsNoRows(err) {
			return cid, errs.NotFound("customer")
		}
		return cid, err
	}
	if merged != nil {
		return *merged, nil
	}
	return cid, nil
}

func (m *Module) customerHistory(w http.ResponseWriter, r *http.Request) {
	cid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	qv := r.URL.Query()
	kind := qv.Get("kind")
	if kind != "" && kind != "round" && kind != "payment" && kind != "member_charge" {
		httpx.WriteError(w, r, errs.BadRequest("invalid_kind", "kind must be round, payment or member_charge"))
		return
	}
	var from, to *time.Time
	for key, dst := range map[string]**time.Time{"from": &from, "to": &to} {
		if v := qv.Get(key); v != "" {
			t, err := time.Parse("2006-01-02", v)
			if err != nil {
				httpx.WriteError(w, r, errs.BadRequest("invalid_date", key+" must be YYYY-MM-DD"))
				return
			}
			if key == "to" {
				t = t.AddDate(0, 0, 1)
			}
			*dst = &t
		}
	}
	limit := 200
	if v, err := strconv.Atoi(qv.Get("limit")); err == nil && v > 0 && v <= 1000 {
		limit = v
	}
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	var out []HistoryItem
	err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		cid, err = resolveMerged(ctx, tx, cid)
		if err != nil {
			return err
		}
		if _, err := loadProfile(ctx, tx, cid, pid); err != nil {
			return err
		}
		out, err = history(ctx, tx, cid, kind, from, to, limit)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[HistoryItem]{Items: out})
}

// PersonalDataExport is the result of a personal data export.
type PersonalDataExport struct {
	Request DataRequest  `json:"request"`
	File    storage.File `json:"file"`
}

// exportPersonalData produces a JSON file with every personal data held on
// the customer (UU PDP right of access, FR-CUS-09).
func (m *Module) exportPersonalData(w http.ResponseWriter, r *http.Request) {
	cid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if m.Files == nil {
		httpx.WriteError(w, r, errs.Unavailable("file storage is not configured"))
		return
	}
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	var out PersonalDataExport
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		o, err := buildOverview(ctx, tx, cid, pid, 10000)
		if err != nil {
			return err
		}
		doc := map[string]any{"exportedAt": time.Now().UTC(), "customer": o.Profile, "handicapIndex": o.HandicapIndex, "memberships": o.Memberships,
			"accounts": o.Accounts, "relationships": o.Relationships, "preferences": o.Preferences, "history": o.RecentHistory}
		body, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		f, err := m.Files.Save(ctx, tx, "personal-data-"+o.Profile.Code+".json", "application/json", "export", false, bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return err
		}
		if err := recordExport(ctx, tx, pid, cid, f.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id, customer_id, request_type, status, reason, file_id, requested_at, completed_at FROM crm.data_requests
			WHERE customer_id = $1 AND file_id = $2`, cid, f.ID).Scan(&out.Request.ID, &out.Request.CustomerID, &out.Request.RequestType, &out.Request.Status,
			&out.Request.Reason, &out.Request.FileID, &out.Request.RequestedAt, &out.Request.CompletedAt); err != nil {
			return err
		}
		out.File = f
		return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "export_personal_data", EntityType: "crm.customer", EntityID: cid.String(),
			EntityLabel: o.Profile.Name, PropertyID: &pid, After: map[string]any{"fileId": f.ID, "requestId": out.Request.ID}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
