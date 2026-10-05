package app

// PRD P5 EP-01–05, EP-16, EP-24: core HR (organization, employees, contracts, documents, recruitment, training & certification, performance review, Employee Self Service, HR & Workforce Policies). internal/app/p5.go calls these functions; the area fills them.
//
// Core HR (owner: hris/corehr) sits in the back office layer. The
// composition root plugs in IAM (onboarding logins, H7 deactivation and the
// reassignment of a leaver's approvals), the golf caddy and sport club
// instructor directories, and the certification checks golf and sport club
// call before an assignment (FR-TRC-03, H7) — business lines never import
// hris.

import (
	"context"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/platform/calendar"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/golf"
	"oneclub/internal/hris"
	"oneclub/internal/hris/corehr"
	"oneclub/internal/hris/talent"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/iam"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
	"oneclub/internal/reporting"
	"oneclub/internal/sportclub"
)

// p5HR holds the services of the area.
type p5HR struct {
	Module *corehr.Module
	// Recruitment & Performance Review (EP-03, EP-05): p5_hr_talent.go.
	Talent *talent.Module
}

func p5HRContributions() []catalog.Contribution {
	return append([]catalog.Contribution{(&corehr.Module{}).Contribution(), reporting.HRCoreContribution()}, p5HRTalentContributions()...)
}
func p5HRDocumentTypes() []provision.DocumentType { return p5HRTalentDocumentTypes() }
func p5HRTemplates() []provision.Template {
	return append(corehr.Templates(), p5HRTalentTemplates()...)
}

// HRDemoUsers are the HR, employee and department head demo logins (PRD P5
// §4: HR Manager, Employee (self-service), department head).
var HRDemoUsers = []DemoUser{
	{"hr@demo.oneclub.id", "Nadia HR Manager", "hr_manager", "MAIN"},
	{"hr.admin@demo.oneclub.id", "Tari HR Admin", "hr_admin", "MAIN"},
	{"dept.head@demo.oneclub.id", "Hendro F&B Manager", "department_head", "MAIN"},
	{"employee@demo.oneclub.id", "Andi Waiter", "employee_self_service", "MAIN"},
}

func init() {
	Reports = append(Reports, reporting.HRCoreReports()...)
	DemoUsers = append(DemoUsers, HRDemoUsers...)
}

// buildP5HR wires routes, hooks, approval decisions and jobs.
func (a *App) buildP5HR(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	m := &corehr.Module{DB: db, Engine: a.Engine, Events: a.Bus, Notify: a.Notification, Files: files, Accounts: hrAccounts{iam: a.IAM},
		Partners: hrPartners{}, StaffURL: func() string { return cfg.PublicBaseURL }, Location: a.Instance.Location, Logo: brandingLogo(files)}
	m.Register(reg)
	m.RegisterJobs(a.Registrar, a.Instance.Location)
	// FR-TRC-03 / H7: golf and sport club refuse staff without a valid
	// mandatory certification.
	golf.SetCaddyCertificationCheck(func(ctx context.Context, q dbtx.Querier, property uuid.UUID, ids []uuid.UUID, day time.Time) (map[uuid.UUID]string, error) {
		on, err := hrisEnabled(ctx, q)
		if err != nil || !on {
			return nil, err
		}
		gaps, err := hris.PartnersWithGaps(ctx, q, property, hris.HolderCaddy, ids, day)
		if err != nil {
			return nil, err
		}
		out := map[uuid.UUID]string{}
		for cid, c := range gaps {
			if e := c.Err("the caddy"); e != nil {
				if pe, ok := errs.As(e); ok {
					out[cid] = pe.Message
				}
			}
		}
		return out, nil
	})
	sportclub.SetInstructorCertificationCheck(func(ctx context.Context, q dbtx.Querier, property, instructor uuid.UUID, employee *uuid.UUID, name string,
		from, to time.Time) error {
		on, err := hrisEnabled(ctx, q)
		if err != nil || !on {
			return err
		}
		for _, day := range []time.Time{from, to} {
			var chk hris.CertificationCheck
			if employee != nil {
				chk, err = hris.CheckEmployee(ctx, q, *employee, hris.HolderInstructor, day)
			} else {
				chk, err = hris.CheckPartner(ctx, q, property, hris.HolderInstructor, instructor, "", day)
			}
			if err != nil {
				return err
			}
			if e := chk.Err(name); e != nil {
				return e
			}
		}
		return nil
	})
	a.HR.Module = m
	a.buildP5HRTalent(reg, cfg, db, files, m) // EP-03, EP-05 (p5_hr_talent.go)
}

// hrisEnabled reports whether the HRIS module is enabled.
func hrisEnabled(ctx context.Context, q dbtx.Querier) (bool, error) {
	var on bool
	err := q.QueryRow(ctx, `SELECT enabled FROM platform.modules WHERE code = 'hris'`).Scan(&on)
	if dbtx.IsNoRows(err) {
		return false, nil
	}
	return on, err
}

// hrAccounts provisions employee logins through IAM (FR-HR-04).
type hrAccounts struct{ iam *iam.Service }

func (h hrAccounts) ProvisionEmployeeUser(ctx context.Context, tx pgx.Tx, r corehr.AccountRequest) (uuid.UUID, error) {
	cfg, _, err := hris.LoadHRConfiguration(ctx, tx, r.PropertyID, time.Now())
	if err != nil {
		return uuid.Nil, err
	}
	return h.iam.ProvisionEmployeeUser(ctx, tx, iam.EmployeeUserRequest{EmployeeID: r.EmployeeID, PropertyID: r.PropertyID, FullName: r.FullName,
		Email: r.Email, Phone: r.Phone, Locale: r.Locale, RoleCodes: r.RoleCodes, OnboardingRoles: []string{cfg.SelfServiceRole, "department_head"},
		ExistingUserID: r.ExistingUserID})
}

// hrPartners resolves golf caddies and sport club instructors.
type hrPartners struct{}

func partnerTable(kind string) string {
	if kind == hris.HolderCaddy {
		return "golf.caddies"
	}
	return "sportclub.instructors"
}

func (hrPartners) PartnerName(ctx context.Context, q dbtx.Querier, property uuid.UUID, kind string, pid uuid.UUID) (string, error) {
	var n string
	err := q.QueryRow(ctx, `SELECT name FROM `+partnerTable(kind)+` WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, pid, property).Scan(&n)
	if dbtx.IsNoRows(err) {
		return "", nil
	}
	return n, err
}

func (hrPartners) PartnerByCode(ctx context.Context, q dbtx.Querier, property uuid.UUID, kind, code string) (*uuid.UUID, string, error) {
	var pid uuid.UUID
	var n string
	err := q.QueryRow(ctx, `SELECT id, name FROM `+partnerTable(kind)+` WHERE property_id = $1 AND upper(code) = upper($2) AND archived_at IS NULL`,
		property, code).Scan(&pid, &n)
	if dbtx.IsNoRows(err) {
		return nil, "", nil
	}
	return &pid, n, err
}

// subscribeP5HR registers the event subscribers: hris.employee_terminated
// (H7) deactivates the leaver's login and moves the approvals waiting for
// the leaver to the supervisor.
func (a *App) subscribeP5HR() {
	a.Bus.Subscribe(hris.EventEmployeeTerminated, "iam.employee_terminated", func(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
		var p hris.EmployeeTerminated
		if err := e.Decode(&p); err != nil {
			return nil //nolint:nilerr // foreign payload
		}
		if p.UserID == nil {
			return nil
		}
		ctx = reqctx.WithProperty(dbtx.System(ctx), p.PropertyID)
		reason := "Employee " + p.EmployeeNo + " left on " + p.EffectiveDate + " (" + p.TerminationType + ")"
		if err := a.IAM.DeactivateEmployeeUser(ctx, tx, *p.UserID, reason); err != nil {
			return err
		}
		_, err := a.Approvals.ReassignUser(ctx, tx, *p.UserID, p.SupervisorUserID, reason)
		return err
	})
}

// demoP5HR seeds the demo data of the area: the organization, ~40
// employees with contracts, documents, certifications and training (hris),
// the logins of the HR demo users, and caddy certificates (one expiring
// soon, none expired so the golf demo keeps every caddy).
func demoP5HR(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	if err := corehr.SeedDemo(ctx, tx, property); err != nil {
		return err
	}
	for _, link := range [][2]string{{"gm@demo.oneclub.id", "EMP-00001"}, {"golf.manager@demo.oneclub.id", "EMP-00002"},
		{"finance@demo.oneclub.id", "EMP-00008"}, {"hr@demo.oneclub.id", "EMP-00009"}, {"hr.admin@demo.oneclub.id", "EMP-00031"},
		{"dept.head@demo.oneclub.id", "EMP-00005"}, {"employee@demo.oneclub.id", "EMP-00021"}, {"caddy.master@demo.oneclub.id", "EMP-00011"}} {
		if _, err := tx.Exec(ctx, `UPDATE platform.users u SET employee_id = e.id FROM hris.employees e
			WHERE u.email = $1 AND e.property_id = $2 AND e.employee_no = $3 AND u.employee_id IS NULL
			  AND NOT EXISTS (SELECT 1 FROM platform.users x WHERE x.employee_id = e.id)`, link[0], property, link[1]); err != nil {
			return err
		}
	}
	var certType uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM hris.certification_types WHERE code = 'CADDY'`).Scan(&certType); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO hris.certifications (id, property_id, certification_type_id, holder_kind, partner_id, holder_name, certificate_no,
		issuer, issued_on, expires_on)
		SELECT gen_random_uuid(), c.property_id, $2, 'caddy', c.id, c.name, 'CDY-' || c.code, 'Modern Golf Caddy Academy',
		  CASE WHEN c.code = 'C020' THEN $3::date - 700 ELSE $3::date - 200 END,
		  CASE WHEN c.code = 'C020' THEN $3::date + 29 ELSE $3::date + 530 END
		FROM golf.caddies c WHERE c.property_id = $1 AND c.archived_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM hris.certifications x WHERE x.partner_id = c.id AND x.certification_type_id = $2)`, property, certType,
		clock.Now().In(calendar.Location(ctx, tx)).Format(time.DateOnly))
	if err != nil {
		return err
	}
	return demoP5HRTalent(ctx, tx, property) // EP-03, EP-05 (p5_hr_talent.go)
}
