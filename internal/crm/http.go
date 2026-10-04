package crm

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/storage"
)

// Publisher publishes domain events (outbox.Bus).
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

// Domain events (other modules re-point their references).
const (
	EventCustomerMerged = "crm.customer_merged"
	EventGuestUpgraded  = "crm.guest_upgraded"
	EventCustomerErased = "crm.customer_erased"
)

// MergedPayload is the payload of crm.customer_merged.
type MergedPayload struct {
	SourceID uuid.UUID `json:"sourceId"`
	TargetID uuid.UUID `json:"targetId"`
}

// UpgradedPayload is the payload of crm.guest_upgraded.
type UpgradedPayload struct {
	GuestID    uuid.UUID `json:"guestId"`
	CustomerID uuid.UUID `json:"customerId"`
}

// ErasedPayload is the payload of crm.customer_erased.
type ErasedPayload struct {
	CustomerID uuid.UUID `json:"customerId"`
}

// Module serves CRM endpoints.
type Module struct {
	DB     *dbtx.DB
	Events Publisher
	Files  *storage.Files
}

type MergeRequest struct {
	SourceID uuid.UUID `json:"sourceId" doc:"Profile that is merged away (becomes Merged)"`
	TargetID uuid.UUID `json:"targetId" doc:"Profile that is kept"`
	Reason   string    `json:"reason"`
}

type MergeResult struct {
	SourceID uuid.UUID `json:"sourceId"`
	TargetID uuid.UUID `json:"targetId"`
	MergedAt time.Time `json:"mergedAt"`
}

type UpgradeGuestRequest struct {
	Code      string     `json:"code,omitempty"`
	Name      string     `json:"name,omitempty"`
	Email     string     `json:"email,omitempty"`
	Gender    string     `json:"gender,omitempty" enum:"male,female"`
	BirthDate *time.Time `json:"birthDate,omitempty"`
	Consent   bool       `json:"consent,omitempty"`
}

type EraseRequest struct {
	Reason string `json:"reason"`
}

type DataRequest struct {
	ID          uuid.UUID  `json:"id"`
	CustomerID  uuid.UUID  `json:"customerId"`
	RequestType string     `json:"requestType" enum:"export,erase"`
	Status      string     `json:"status" enum:"pending,completed,rejected"`
	Reason      *string    `json:"reason"`
	FileID      *uuid.UUID `json:"fileId"`
	RequestedAt time.Time  `json:"requestedAt"`
	CompletedAt *time.Time `json:"completedAt"`
}

func (m *Module) duplicates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	q := r.URL.Query()
	var out []Duplicate
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = FindDuplicates(ctx, tx, pid, q.Get("phone"), q.Get("email"), q.Get("excludeId"))
		if err != nil {
			return err
		}
		phone := NormalizePhone(q.Get("phone"))
		if phone == "" {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT id, code, name FROM crm.guests WHERE property_id = $1 AND phone = $2 AND customer_id IS NULL
			AND erased_at IS NULL LIMIT 5`, pid, phone)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d := Duplicate{Kind: "guest", MatchedOn: "phone"}
			if err := rows.Scan(&d.ID, &d.Code, &d.Name); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if out == nil {
		out = []Duplicate{}
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Duplicate]{Items: out})
}

// merge folds the source profile into the target (FR-CUS-03). CRM-owned
// references move inside this transaction; other modules move theirs when
// they receive crm.customer_merged.
func (m *Module) merge(w http.ResponseWriter, r *http.Request) {
	var req MergeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		httpx.WriteError(w, r, errs.Validation("reason_required", "a reason is required", errs.Field("reason", "required", "reason is required")))
		return
	}
	if req.SourceID == req.TargetID || req.SourceID == uuid.Nil || req.TargetID == uuid.Nil {
		httpx.WriteError(w, r, errs.Validation("invalid_merge", "choose two different customers"))
		return
	}
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	var out MergeResult
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var srcStatus, tgtStatus, srcName, tgtName string
		if err := tx.QueryRow(ctx, `SELECT status, name FROM crm.customers WHERE id = $1 AND property_id = $2 FOR UPDATE`, req.SourceID, pid).Scan(&srcStatus, &srcName); err != nil {
			if dbtx.IsNoRows(err) {
				return errs.NotFound("source customer")
			}
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT status, name FROM crm.customers WHERE id = $1 AND property_id = $2 FOR UPDATE`, req.TargetID, pid).Scan(&tgtStatus, &tgtName); err != nil {
			if dbtx.IsNoRows(err) {
				return errs.NotFound("target customer")
			}
			return err
		}
		if srcStatus == "merged" || tgtStatus == "merged" {
			return errs.Conflict("already_merged", "one of the profiles is already merged")
		}
		stmts := []string{
			`UPDATE crm.guests SET customer_id = $2 WHERE customer_id = $1`,
			`UPDATE crm.customer_relationships SET customer_id = $2 WHERE customer_id = $1 AND related_customer_id <> $2
			   AND NOT EXISTS (SELECT 1 FROM crm.customer_relationships x WHERE x.customer_id = $2 AND x.related_customer_id = crm.customer_relationships.related_customer_id AND x.relationship = crm.customer_relationships.relationship)`,
			`UPDATE crm.customer_relationships SET related_customer_id = $2 WHERE related_customer_id = $1 AND customer_id <> $2
			   AND NOT EXISTS (SELECT 1 FROM crm.customer_relationships x WHERE x.related_customer_id = $2 AND x.customer_id = crm.customer_relationships.customer_id AND x.relationship = crm.customer_relationships.relationship)`,
			`UPDATE crm.customer_relationships SET status = 'inactive' WHERE (customer_id = $1 OR related_customer_id = $1) AND $2::uuid IS NOT NULL`,
			`UPDATE crm.corporate_nominees SET customer_id = $2 WHERE customer_id = $1
			   AND NOT EXISTS (SELECT 1 FROM crm.corporate_nominees x WHERE x.customer_id = $2 AND x.corporate_account_id = crm.corporate_nominees.corporate_account_id)`,
			`UPDATE crm.customer_preferences SET customer_id = $2 WHERE customer_id = $1
			   AND NOT EXISTS (SELECT 1 FROM crm.customer_preferences x WHERE x.customer_id = $2 AND x.category = crm.customer_preferences.category AND x.pref_key = crm.customer_preferences.pref_key)`,
		}
		for _, s := range stmts {
			if _, err := tx.Exec(ctx, s, req.SourceID, req.TargetID); err != nil {
				return err
			}
		}
		// The portal login moves to the kept profile when it has none.
		var srcUser *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT user_id FROM crm.customers WHERE id = $1`, req.SourceID).Scan(&srcUser); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.customers SET status = 'merged', merged_into_id = $2, user_id = NULL, updated_by = $3 WHERE id = $1`,
			req.SourceID, req.TargetID, id.Ptr(actorID(ctx))); err != nil {
			return err
		}
		if srcUser != nil {
			if _, err := tx.Exec(ctx, `UPDATE crm.customers SET user_id = $2 WHERE id = $1 AND user_id IS NULL`, req.TargetID, *srcUser); err != nil {
				return err
			}
		}
		out = MergeResult{SourceID: req.SourceID, TargetID: req.TargetID, MergedAt: time.Now().UTC()}
		if _, err := tx.Exec(ctx, `INSERT INTO crm.customer_merges (id, property_id, source_id, target_id, reason, merged_by) VALUES ($1,$2,$3,$4,$5,$6)`,
			id.New(), pid, req.SourceID, req.TargetID, req.Reason, id.Ptr(actorID(ctx))); err != nil {
			return err
		}
		if _, err := m.Events.Publish(ctx, tx, EventCustomerMerged, "crm.customer", &req.TargetID, &pid, MergedPayload{SourceID: req.SourceID, TargetID: req.TargetID}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "merge", EntityType: "crm.customer", EntityID: req.TargetID.String(),
			EntityLabel: tgtName, PropertyID: &pid, Reason: req.Reason,
			Before: map[string]any{"sourceId": req.SourceID, "sourceName": srcName}, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// upgradeGuest turns a Guest into a Customer without losing history
// (FR-CUS-02): bookings keep pointing at the guest, which now carries the
// customer id; golf re-points players on crm.guest_upgraded.
func (m *Module) upgradeGuest(w http.ResponseWriter, r *http.Request) {
	gid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req UpgradeGuestRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	var out Customer
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var name, code string
		var phone, email, idNumber *string
		var cust *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT code, name, phone, email, id_number, customer_id FROM crm.guests WHERE id = $1 AND property_id = $2 FOR UPDATE`, gid, pid).
			Scan(&code, &name, &phone, &email, &idNumber, &cust)
		if dbtx.IsNoRows(err) {
			return errs.NotFound("guest")
		}
		if err != nil {
			return err
		}
		if cust != nil {
			return errs.Conflict("already_upgraded", "this guest is already a customer")
		}
		n := NewCustomer{Code: req.Code, Name: name, Gender: req.Gender, BirthDate: req.BirthDate}
		if req.Name != "" {
			n.Name = req.Name
		}
		if phone != nil {
			n.Phone = *phone
		}
		if email != nil {
			n.Email = *email
		}
		if req.Email != "" {
			n.Email = req.Email
		}
		if idNumber != nil {
			n.IDNumber = *idNumber
		}
		if req.Consent {
			n.ConsentChannel = "back_office"
		}
		if n.Code == "" {
			n.Code = "C-" + code
		}
		out, err = CreateCustomer(ctx, tx, pid, n)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.guests SET customer_id = $2, upgraded_at = now(), updated_by = $3 WHERE id = $1`, gid, out.ID, id.Ptr(actorID(ctx))); err != nil {
			return err
		}
		if _, err := m.Events.Publish(ctx, tx, EventGuestUpgraded, "crm.guest", &gid, &pid, UpgradedPayload{GuestID: gid, CustomerID: out.ID}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "upgrade", EntityType: "crm.guest", EntityID: gid.String(), EntityLabel: name,
			PropertyID: &pid, After: map[string]any{"customerId": out.ID, "customerCode": out.Code}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// erase anonymises personal data on request of the data subject (UU PDP,
// FR-CUS-09). Financial records keep amounts; identifiers are removed.
func (m *Module) erase(w http.ResponseWriter, r *http.Request) {
	cid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req EraseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		httpx.WriteError(w, r, errs.Validation("reason_required", "a reason is required", errs.Field("reason", "required", "reason is required")))
		return
	}
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	var out DataRequest
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var name string
		var erased *time.Time
		if err := tx.QueryRow(ctx, `SELECT name, erased_at FROM crm.customers WHERE id = $1 AND property_id = $2 FOR UPDATE`, cid, pid).Scan(&name, &erased); err != nil {
			if dbtx.IsNoRows(err) {
				return errs.NotFound("customer")
			}
			return err
		}
		if erased != nil {
			return errs.Conflict("already_erased", "personal data of this customer is already erased")
		}
		anon := "Erased Customer " + strings.ToUpper(cid.String()[:8])
		if _, err := tx.Exec(ctx, `UPDATE crm.customers SET name = $2, email = NULL, phone = NULL, birth_date = NULL, address = NULL, city = NULL,
			id_number = NULL, photo_file_id = NULL, notes = NULL, attributes = '{}'::jsonb, marketing_opt_in = false, erased_at = now(),
			status = CASE WHEN status = 'merged' THEN status ELSE 'inactive' END, updated_by = $3 WHERE id = $1`, cid, anon, id.Ptr(actorID(ctx))); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.guests SET name = $2, email = NULL, phone = NULL, id_number = NULL, erased_at = now() WHERE customer_id = $1`, cid, anon); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.customer_preferences SET pref_value = NULL, notes = NULL, status = 'inactive' WHERE customer_id = $1`, cid); err != nil {
			return err
		}
		did := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO crm.data_requests (id, property_id, customer_id, request_type, status, reason, requested_by, completed_at)
			VALUES ($1,$2,$3,'erase','completed',$4,$5,now()) RETURNING id, customer_id, request_type, status, reason, file_id, requested_at, completed_at`,
			did, pid, cid, req.Reason, id.Ptr(actorID(ctx))).Scan(&out.ID, &out.CustomerID, &out.RequestType, &out.Status, &out.Reason, &out.FileID,
			&out.RequestedAt, &out.CompletedAt); err != nil {
			return err
		}
		if _, err := m.Events.Publish(ctx, tx, EventCustomerErased, "crm.customer", &cid, &pid, ErasedPayload{CustomerID: cid}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "erase_personal_data", EntityType: "crm.customer", EntityID: cid.String(),
			EntityLabel: anon, PropertyID: &pid, Reason: req.Reason, Before: map[string]any{"name": "[redacted]"}, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (m *Module) listDataRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	out := []DataRequest{}
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, customer_id, request_type, status, reason, file_id, requested_at, completed_at
			FROM crm.data_requests WHERE property_id = $1 ORDER BY requested_at DESC LIMIT 200`, pid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d DataRequest
			if err := rows.Scan(&d.ID, &d.CustomerID, &d.RequestType, &d.Status, &d.Reason, &d.FileID, &d.RequestedAt, &d.CompletedAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[DataRequest]{Items: out})
}

// recordExport stores a completed personal data export request.
func recordExport(ctx context.Context, tx pgx.Tx, property, customerID, fileID uuid.UUID) error {
	_, err := tx.Exec(ctx, `INSERT INTO crm.data_requests (id, property_id, customer_id, request_type, status, file_id, requested_by, completed_at)
		VALUES ($1,$2,$3,'export','completed',$4,$5,now())`, id.New(), property, customerID, fileID, id.Ptr(actorID(ctx)))
	return err
}

// Register adds the CRM routes.
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{Customers, Guests, CorporateAccounts, CorporateNominees, Relationships, Preferences} {
		eng.Register(reg, d)
	}
	add := func(rt route.Route) {
		rt.Module = "crm"
		rt.Scope = route.ScopeProperty
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/customers:duplicates", Tag: "Customers", Summary: "Find possible duplicates by phone / e-mail",
		Permission: "crm.customer.view", Response: Duplicate{}, List: true,
		Query: []route.Param{{Name: "phone"}, {Name: "email"}, {Name: "excludeId"}}, Handler: m.duplicates})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/customers:merge", Tag: "Customers", Summary: "Merge two customer profiles",
		Permission: "crm.customer.merge", Request: MergeRequest{}, Response: MergeResult{}, Status: http.StatusOK, Handler: m.merge})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/guests/{id}:upgrade", Tag: "Customers", Summary: "Upgrade a Guest to a Customer (history kept)",
		Permission: "crm.customer.create", Request: UpgradeGuestRequest{}, Response: Customer{}, Handler: m.upgradeGuest})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/customers/{id}:erase", Tag: "Customers", Summary: "Erase personal data (UU PDP data subject request)",
		Permission: "crm.customer.erase", Request: EraseRequest{}, Response: DataRequest{}, Status: http.StatusOK, Handler: m.erase})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/data-requests", Tag: "Customers", Summary: "Personal data requests (export / erase)",
		Permission: "crm.customer.export_personal_data", Response: DataRequest{}, List: true, Handler: m.listDataRequests})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/customers/{id}/overview", Tag: "Customers", Summary: "Customer 360 (Basic)",
		Permission: "crm.customer_overview.view", Response: Overview{}, Handler: m.overview})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/customers/{id}/history", Tag: "Customers", Summary: "Customer History (rounds, payments, member charges)",
		Permission: "crm.customer_overview.view", Response: HistoryItem{}, List: true,
		Query: []route.Param{{Name: "kind"}, {Name: "from"}, {Name: "to"}}, Handler: m.customerHistory})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/customers/{id}:export-personal-data", Tag: "Customers", Summary: "Export personal data (UU PDP right of access)",
		Permission: "crm.customer.export_personal_data", Response: PersonalDataExport{}, Status: http.StatusOK, Handler: m.exportPersonalData})
}
