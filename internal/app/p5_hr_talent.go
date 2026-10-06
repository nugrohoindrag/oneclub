package app

// PRD P5 EP-03 Recruitment and EP-05 Performance Review (owner:
// hris/talent), called from p5_hr.go. The composition root plugs in Core HR
// as the hris.Onboarding of the hire (employee through the employee
// resource — which publishes hris.employee_hired —, the active contract and
// the Employee Self Service login) and of the promotion from a review (Core
// HR :promote), the approval document types of requisitions and offers, and
// the operational inputs of reviews (FR-PRF-HR-03) from CRM sales targets.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm/sales"
	"oneclub/internal/hris"
	"oneclub/internal/hris/corehr"
	"oneclub/internal/hris/talent"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

func p5HRTalentContributions() []catalog.Contribution {
	return []catalog.Contribution{(&talent.Module{}).Contribution()}
}
func p5HRTalentDocumentTypes() []provision.DocumentType { return talent.DocumentTypes() }
func p5HRTalentTemplates() []provision.Template         { return talent.Templates() }

func init() {
	hris.RegisterReviewInput("sales_target", salesTargetInput)
}

// buildP5HRTalent wires recruitment and performance review (core is the
// Core HR module built just before).
func (a *App) buildP5HRTalent(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files, core *corehr.Module) {
	m := &talent.Module{DB: db, Engine: a.Engine, Events: a.Bus, Notify: a.Notification, Approvals: a.Approvals, Files: files,
		Onboarding: talentOnboarding{core: core, accounts: hrAccounts{iam: a.IAM}}, StaffURL: func() string { return cfg.PublicBaseURL },
		WebsiteURL: func() string { return cfg.WebsiteURL }, Logo: brandingLogo(files)}
	m.Register(reg)
	a.Approvals.RegisterDocumentType(talent.RequisitionDocumentType, m.RequisitionDecision)
	a.Approvals.RegisterDocumentType(talent.OfferDocumentType, m.OfferDecision)
	m.RegisterJobs(a.Registrar, a.Instance.Location)
	a.HR.Talent = m
}

// demoP5HRTalent seeds two open requisitions with candidates in different
// stages and the F&B annual review cycle in progress.
func demoP5HRTalent(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	return talent.SeedDemo(ctx, tx, property)
}

// talentOnboarding is Core HR as the hris.Onboarding of recruitment.
type talentOnboarding struct {
	core     *corehr.Module
	accounts hrAccounts
}

func (o talentOnboarding) Hire(ctx context.Context, tx pgx.Tx, r hris.HireRequest) (hris.HireResult, error) {
	def, ok := o.core.Engine.Def(corehr.KeyEmployee)
	if !ok {
		return hris.HireResult{}, errs.Unavailable("employee resource is not registered")
	}
	status := hris.StatusPermanent
	switch {
	case r.ContractType == "pkwt":
		status = hris.StatusContract
	case r.ProbationMonths > 0:
		status = hris.StatusProbation
	}
	in := map[string]any{"fullName": r.FullName, "employmentStatus": status, "joinDate": r.JoinDate.Format("2006-01-02")}
	set := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			in[k] = v
		}
	}
	set("employeeNo", r.EmployeeNo)
	set("gender", r.Gender)
	set("phone", r.Phone)
	set("personalEmail", r.PersonalEmail)
	set("email", r.WorkEmail)
	set("address", r.Address)
	set("city", r.City)
	set("jobTitle", r.JobTitle)
	set("workerCategory", r.WorkerCategory)
	if r.BirthDate != nil {
		in["birthDate"] = r.BirthDate.Format("2006-01-02")
	}
	if r.PositionID != nil {
		in["positionId"] = r.PositionID.String()
	} else if r.OrgUnitID != nil {
		in["orgUnitId"] = r.OrgUnitID.String()
	}
	if r.GradeID != nil {
		in["gradeId"] = r.GradeID.String()
	}
	if r.SupervisorID != nil {
		in["supervisorId"] = r.SupervisorID.String()
	}
	row, err := o.core.Engine.CreateRow(ctx, tx, def, in)
	if err != nil {
		return hris.HireResult{}, err
	}
	eid, err := uuid.Parse(row["id"].(string))
	if err != nil {
		return hris.HireResult{}, err
	}
	no, _ := row["employeeNo"].(string)
	req := corehr.ContractRequest{EmployeeID: eid, ContractType: r.ContractType, StartDate: r.JoinDate.Format("2006-01-02"), ProbationMonths: r.ProbationMonths,
		GradeID: r.GradeID, JobTitle: r.JobTitle, BaseSalary: r.BaseSalary, Allowances: r.Allowances, WorkWeekDays: r.WorkWeekDays,
		Notes: "Hired through recruitment " + r.Reference, Activate: true}
	if r.EndDate != nil {
		req.EndDate = r.EndDate.Format("2006-01-02")
	}
	c, err := o.core.CreateContract(ctx, tx, req)
	if err != nil {
		return hris.HireResult{}, err
	}
	out := hris.HireResult{EmployeeID: eid, EmployeeNo: no, ContractID: c.ID, ContractNumber: c.Number}
	if !r.CreateLogin {
		return out, nil
	}
	roles := r.RoleCodes
	if len(roles) == 0 {
		cfg, _, err := hris.LoadHRConfiguration(ctx, tx, r.PropertyID, clock.Now())
		if err != nil {
			return out, err
		}
		roles = []string{cfg.SelfServiceRole}
	}
	var phone *string
	if p := strings.TrimSpace(r.Phone); p != "" {
		phone = &p
	}
	uid, err := o.accounts.ProvisionEmployeeUser(ctx, tx, corehr.AccountRequest{EmployeeID: eid, PropertyID: r.PropertyID, FullName: r.FullName,
		Email: r.LoginEmail, Phone: phone, RoleCodes: roles})
	if err != nil {
		return out, err
	}
	out.UserID = &uid
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "create_account", EntityType: corehr.KeyEmployee, EntityID: eid.String(),
		EntityLabel: no + " · " + r.FullName, PropertyID: &r.PropertyID, After: map[string]any{"userId": uid, "roles": roles, "hire": r.Reference}})
}

func (o talentOnboarding) ChangeEmployment(ctx context.Context, tx pgx.Tx, employeeID uuid.UUID, r hris.EmploymentChangeRequest) (uuid.UUID, error) {
	kind := r.Kind
	if kind == "" {
		kind = "promotion"
	}
	return o.core.ChangeEmployment(ctx, tx, employeeID, kind, corehr.EmploymentChangeRequest{Kind: kind, EffectiveDate: r.EffectiveDate,
		OrgUnitID: r.OrgUnitID, PositionID: r.PositionID, GradeID: r.GradeID, Reason: r.Reason})
}

// salesTargetInput is the sales target achievement of an employee's login in
// the review period (CRM Sales, P3 EP-04).
func salesTargetInput(ctx context.Context, q dbtx.Querier, e hris.Employee, from, to time.Time) ([]hris.ReviewInput, error) {
	if e.UserID == nil {
		return nil, nil
	}
	list, err := sales.TargetAchievements(ctx, q, e.PropertyID, from, to, e.UserID.String())
	if err != nil || len(list) == 0 {
		return nil, err
	}
	target, achieved := decimal.Zero, decimal.Zero
	for _, t := range list {
		target = target.Add(hris.Dec(t.TargetRevenue))
		achieved = achieved.Add(hris.Dec(t.AchievedRevenue))
	}
	if !target.IsPositive() {
		return nil, nil
	}
	pct := achieved.Mul(decimal.NewFromInt(100)).Div(target).Round(1)
	return []hris.ReviewInput{{Key: "sales_target", Label: "Sales target achievement", Value: pct.String(), Unit: "percent",
		Note: "Achieved " + achieved.StringFixed(0) + " of " + target.StringFixed(0) + " (paid deals, " + itoaApp(len(list)) + " target(s))"}}, nil
}

func itoaApp(n int) string { return decimal.NewFromInt(int64(n)).String() }
