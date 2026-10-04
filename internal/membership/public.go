package membership

// Website Membership page and online application (PRD P2 EP-26
// FR-WEB-P2-01/07, FR-MBL-15) on P1's types, packages and applications.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// PublicPackage is a package of a membership type on the website.
type PublicPackage struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	PeriodUnit  string    `json:"periodUnit" enum:"year,month"`
	PeriodCount int       `json:"periodCount"`
	JoiningFee  string    `json:"joiningFee"`
	PeriodFee   string    `json:"periodFee"`
	Currency    string    `json:"currency"`
}

// PublicType is a membership type on the website Membership page.
type PublicType struct {
	ID           uuid.UUID       `json:"id" db:"id"`
	Program      string          `json:"program" db:"program"`
	ProgramKind  string          `json:"programKind" db:"program_kind"`
	Name         string          `json:"name" db:"name"`
	Category     string          `json:"category" db:"category"`
	AnnualFee    string          `json:"annualFee" db:"annual_fee"`
	Eligibility  map[string]any  `json:"eligibility" db:"eligibility"`
	Entitlements map[string]any  `json:"benefits" db:"entitlements"`
	RawPackages  []byte          `json:"-" db:"packages"`
	Packages     []PublicPackage `json:"packages" db:"-"`
}

// PublicApplication is an online membership application.
type PublicApplication struct {
	PropertyID uuid.UUID       `json:"propertyId"`
	Guest      crm.PublicGuest `json:"guest"`
	TypeID     uuid.UUID       `json:"typeId"`
	PackageID  *uuid.UUID      `json:"packageId,omitempty" doc:"Default: the first active package of the type"`
	BirthDate  string          `json:"birthDate,omitempty"`
	Resident   bool            `json:"resident,omitempty" doc:"Claims Kota Modern residence (verified by staff)"`
	Documents  []Document      `json:"documents,omitempty"`
	Notes      string          `json:"notes,omitempty"`
}

func (p PublicApplication) Property() uuid.UUID      { return p.PropertyID }
func (p PublicApplication) Visitor() crm.PublicGuest { return p.Guest }

// PublicApplicationResult confirms the application.
type PublicApplicationResult struct {
	ApplicationNo string `json:"applicationNo"`
	Status        string `json:"status"`
}

func (m *Module) registerPublic(reg *route.Registry) {
	crm.PublicRoute(reg, "membership", route.Route{Method: http.MethodGet, Path: "/api/v1/public/membership-types", Summary: "Membership page: programs, types, packages, benefits",
		Response: PublicType{}, List: true, Query: []route.Param{{Name: "propertyId", Required: true}, {Name: "programKind"}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
			if err != nil {
				httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
				return
			}
			ctx := crm.PublicCtx(r.Context(), pid)
			var out []PublicType
			err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
				var err error
				out, err = handle.List[PublicType](tx.Query(ctx, `SELECT t.id, p.name AS program, p.program_kind, t.name, t.category,
					t.annual_fee::text AS annual_fee, t.eligibility, t.entitlements,
					coalesce((SELECT json_agg(json_build_object('id', k.id, 'name', k.name, 'periodUnit', k.period_unit, 'periodCount', k.period_count,
					  'joiningFee', k.joining_fee::text, 'periodFee', k.period_fee::text, 'currency', k.currency) ORDER BY k.created_at)
					  FROM membership.packages k WHERE k.type_id = t.id AND k.status = 'active' AND k.archived_at IS NULL), '[]')::text::bytea AS packages
					FROM membership.types t JOIN membership.programs p ON p.id = t.program_id WHERE t.property_id = $1 AND t.status = 'active'
					AND t.archived_at IS NULL AND p.status = 'active' AND ($2 = '' OR p.program_kind = $2) ORDER BY p.name, t.rank, t.name`,
					pid, programKind(r.URL.Query().Get("programKind"))))
				for i := range out {
					out[i].Packages = []PublicPackage{}
					_ = json.Unmarshal(out[i].RawPackages, &out[i].Packages)
				}
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, httpx.Page[PublicType]{Items: out})
		}})
	crm.PublicRoute(reg, "membership", route.Route{Method: http.MethodPost, Path: "/api/v1/public/membership-applications",
		Summary: "Apply for a membership online (eligibility checked; submitted by the Membership Admin)", Request: PublicApplication{}, Response: PublicApplicationResult{},
		Handler: crm.PublicWrite(m.DB, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID, c crm.Customer, in PublicApplication) (PublicApplicationResult, error) {
			if in.BirthDate != "" {
				if _, err := tx.Exec(ctx, `UPDATE crm.customers SET birth_date = coalesce(birth_date, $2::date) WHERE id = $1`, c.ID, in.BirthDate); err != nil {
					return PublicApplicationResult{}, handle.Invalid("birthDate", "invalid_date", "birthDate must be YYYY-MM-DD")
				}
			}
			pkg := in.PackageID
			if pkg == nil {
				var x uuid.UUID
				if err := tx.QueryRow(ctx, `SELECT id FROM membership.packages WHERE type_id = $1 AND status = 'active' ORDER BY created_at LIMIT 1`, in.TypeID).Scan(&x); err != nil {
					return PublicApplicationResult{}, handle.Invalid("typeId", "no_package", "this membership type has no package to apply for")
				}
				pkg = &x
			}
			notes := in.Notes
			if in.Resident {
				notes = "Claims Kota Modern residence. " + notes
			}
			a, err := m.NewApplication(withProperty(ctx, pid), tx, pid, "website", ApplicationRequest{CustomerID: c.ID, TypeID: in.TypeID, PackageID: *pkg,
				Documents: in.Documents, Notes: notes})
			if err != nil {
				return PublicApplicationResult{}, err
			}
			return PublicApplicationResult{ApplicationNo: a.Number, Status: a.Status}, nil
		})})
}
