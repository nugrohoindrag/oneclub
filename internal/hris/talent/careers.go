package talent

// Website careers page (FR-RCT-05, Should): open requisitions marked public
// are listed on the website; a visitor applies with explicit consent to the
// processing of the application (UU PDP, required), an optional talent pool
// consent and a CV. Anonymous writes are rate limited per client IP and
// guarded by a honeypot field; the candidate is de-duplicated per property
// by e-mail / phone.

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/org"
)

// careersLimiter limits website applications per client IP.
var careersLimiter = &handle.Limiter{N: 10, Period: time.Minute}

// careersReadLimiter limits careers page reads per client IP.
var careersReadLimiter = &handle.Limiter{N: 120, Period: time.Minute}

// CareerPosition is an open position on the careers page.
type CareerPosition struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	Number         string     `json:"number" db:"number"`
	Title          string     `json:"title" db:"title"`
	Department     string     `json:"department" db:"department"`
	Location       *string    `json:"location" db:"location"`
	ContractType   string     `json:"contractType" db:"contract_type" enum:"pkwt,pkwtt"`
	WorkerCategory string     `json:"workerCategory" db:"worker_category"`
	Headcount      int        `json:"openings" db:"openings"`
	Description    *string    `json:"description" db:"description"`
	Requirements   *string    `json:"requirements" db:"requirements"`
	PublishUntil   *time.Time `json:"publishUntil" db:"publish_until"`
	PostedAt       *time.Time `json:"postedAt" db:"approved_at"`
}

// CareerCV is a CV sent from the website.
type CareerCV struct {
	Filename      string `json:"filename"`
	ContentBase64 string `json:"contentBase64" doc:"File content, base64 (PDF, DOC/DOCX, JPEG or PNG; size limit of the Recruitment Configuration)"`
}

// CareerApplicationRequest is a website job application.
type CareerApplicationRequest struct {
	PropertyID        uuid.UUID `json:"propertyId"`
	RequisitionID     uuid.UUID `json:"requisitionId"`
	FullName          string    `json:"fullName"`
	Email             string    `json:"email"`
	Phone             string    `json:"phone"`
	City              string    `json:"city,omitempty"`
	Education         string    `json:"education,omitempty" enum:"sd,smp,sma,d1,d3,s1,s2,s3,other"`
	CurrentEmployer   string    `json:"currentEmployer,omitempty"`
	CurrentTitle      string    `json:"currentTitle,omitempty"`
	ExperienceYears   string    `json:"experienceYears,omitempty"`
	CoverLetter       string    `json:"coverLetter,omitempty"`
	CV                *CareerCV `json:"cv,omitempty"`
	Consent           bool      `json:"consent" doc:"Consent to process the application (required, UU PDP); never pre-checked"`
	TalentPoolConsent bool      `json:"talentPoolConsent,omitempty" doc:"Keep my profile for other positions for the retention period"`
	Website           string    `json:"website,omitempty" doc:"Honeypot — must stay empty (bot protection)"`
}

// CareerApplicationResult acknowledges a website application.
type CareerApplicationResult struct {
	Status string `json:"status"`
	Number string `json:"number,omitempty" doc:"Application number"`
}

func (m *Module) registerCareers(reg *route.Registry) {
	tag := "Public Careers"
	pub := func(rt route.Route) {
		rt.Auth = route.AuthPublic
		add(reg, tag, rt)
	}
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/careers", Summary: "Open positions of the careers page (website)",
		Response: CareerPosition{}, List: true, Query: []route.Param{{Name: "propertyId", Required: true}}, Handler: m.publicRead(m.careersHTTP)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/careers/{id}", Summary: "An open position of the careers page",
		Response: CareerPosition{}, Query: []route.Param{{Name: "propertyId", Required: true}}, Handler: m.publicRead(m.careerHTTP)})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/careers/applications",
		Summary: "Apply from the website (consent required, CV optional; rate limited)", Request: CareerApplicationRequest{},
		Response: CareerApplicationResult{}, Status: http.StatusAccepted, Handler: m.applyHTTP})
}

func clientIP(r *http.Request) string {
	if md := reqctx.GetMeta(r.Context()); md != nil && md.IP != "" {
		return md.IP
	}
	h, _, _ := net.SplitHostPort(r.RemoteAddr)
	return h
}

// publicCtx scopes an anonymous request to a property.
func publicCtx(ctx context.Context, pid uuid.UUID) context.Context {
	return reqctx.WithProperty(dbtx.WithScope(ctx, dbtx.Scope{PropertyIDs: []uuid.UUID{pid}}), pid)
}

func (m *Module) publicRead(fn func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !careersReadLimiter.Allow(clientIP(r)) {
			httpx.WriteError(w, r, errs.RateLimited())
			return
		}
		pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
		if err != nil {
			httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
			return
		}
		ctx := publicCtx(r.Context(), pid)
		var out any
		if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
			cfg, _, err := hris.LoadRecruitmentConfiguration(ctx, tx, pid, clock.Now())
			if err != nil {
				return err
			}
			if !cfg.CareersPage {
				return errs.NotFound("careers page")
			}
			out, err = fn(ctx, tx, pid, r)
			return err
		}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

const careerSelect = `SELECT r.id, r.number, r.title, ou.name AS department, r.location, r.contract_type, r.worker_category,
	(r.headcount - r.hired_count) AS openings, r.description, r.requirements, r.publish_until, r.approved_at
	FROM hris.job_requisitions r JOIN hris.org_units ou ON ou.id = r.org_unit_id
	WHERE r.property_id = $1 AND r.status = 'open' AND r.is_public AND r.hired_count < r.headcount
	  AND (r.publish_until IS NULL OR r.publish_until >= $2::date)`

func (m *Module) careersHTTP(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error) {
	return handle.Page(handle.List[CareerPosition](tx.Query(ctx, careerSelect+` ORDER BY r.approved_at DESC NULLS LAST, r.title`, pid,
		ymd(today(ctx, tx, pid)))))
}

func (m *Module) careerHTTP(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return nil, err
	}
	return getOne[CareerPosition]("position")(tx.Query(ctx, careerSelect+` AND r.id = $3`, pid, ymd(today(ctx, tx, pid)), rid))
}

func (m *Module) applyHTTP(w http.ResponseWriter, r *http.Request) {
	if !careersLimiter.Allow(clientIP(r) + r.URL.Path) {
		httpx.WriteError(w, r, errs.RateLimited())
		return
	}
	var in CareerApplicationRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if in.PropertyID == uuid.Nil {
		httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
		return
	}
	ctx := publicCtx(r.Context(), in.PropertyID)
	if in.Website != "" {
		// Bots fill every field: accept silently, keep an audit trace only.
		_ = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "bot_dropped", EntityType: "public.request", EntityID: r.URL.Path,
				EntityLabel: clientIP(r), PropertyID: &in.PropertyID})
		})
		httpx.JSON(w, http.StatusAccepted, CareerApplicationResult{Status: "received"})
		return
	}
	var out CareerApplicationResult
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = m.Apply(ctx, tx, in)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, out)
}

// Apply records a website application: the candidate (new or found by
// e-mail / phone) with consent, the CV and an application in Applied.
func (m *Module) Apply(ctx context.Context, tx pgx.Tx, in CareerApplicationRequest) (CareerApplicationResult, error) {
	pid := in.PropertyID
	cfg, _, err := hris.LoadRecruitmentConfiguration(ctx, tx, pid, clock.Now())
	if err != nil {
		return CareerApplicationResult{}, err
	}
	if !cfg.CareersPage {
		return CareerApplicationResult{}, errs.NotFound("careers page")
	}
	in.FullName, in.Email, in.Phone = strings.TrimSpace(in.FullName), strings.ToLower(strings.TrimSpace(in.Email)), strings.TrimSpace(in.Phone)
	var fields []errs.FieldError
	if in.FullName == "" || len([]rune(in.FullName)) > 120 {
		fields = append(fields, errs.Field("fullName", "required", "your full name (up to 120 characters)"))
	}
	if in.Email == "" || !strings.Contains(in.Email, "@") || strings.ContainsAny(in.Email, " \t") || len(in.Email) > 254 {
		fields = append(fields, errs.Field("email", "invalid_email", "a valid e-mail address"))
	}
	if d := normPhone(in.Phone); len(d) < 8 || len(d) > 15 {
		fields = append(fields, errs.Field("phone", "invalid", "a phone number of 8–15 digits"))
	}
	if !in.Consent {
		fields = append(fields, errs.Field("consent", "required", "agree to the processing of your application data"))
	}
	if in.Education != "" && !oneOf(Education, in.Education) {
		fields = append(fields, errs.Field("education", "invalid", "one of: "+strings.Join(Education, ", ")))
	}
	var exp *string
	if s := strings.TrimSpace(in.ExperienceYears); s != "" {
		d, err := money("experienceYears", s)
		if err != nil || d.GreaterThan(hris.Dec("60")) {
			fields = append(fields, errs.Field("experienceYears", "invalid", "years of experience between 0 and 60"))
		} else {
			exp = decStr(d)
		}
	}
	if len(in.CoverLetter) > 4000 {
		fields = append(fields, errs.Field("coverLetter", "too_long", "up to 4000 characters"))
	}
	if len(fields) > 0 {
		return CareerApplicationResult{}, errs.Validation("invalid_application", "please check your application", fields...)
	}
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (`+careerSelect+` AND r.id = $3)`, pid, ymd(today(ctx, tx, pid)), in.RequisitionID).Scan(&open); err != nil {
		return CareerApplicationResult{}, err
	}
	if !open {
		return CareerApplicationResult{}, errs.NotFound("open position")
	}
	var cv *uuid.UUID
	if in.CV != nil && strings.TrimSpace(in.CV.ContentBase64) != "" {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.CV.ContentBase64))
		if err != nil {
			return CareerApplicationResult{}, handle.Invalid("cv", "invalid", "the CV must be base64 encoded")
		}
		fid, err := m.saveCV(ctx, tx, in.CV.Filename, data, max(cfg.CVMaxMB, 1)<<20)
		if err != nil {
			return CareerApplicationResult{}, err
		}
		cv = &fid
	}
	cid, err := findCandidate(ctx, tx, pid, in.Email, in.Phone)
	if err != nil {
		return CareerApplicationResult{}, err
	}
	created := cid == nil
	if created {
		nid := id.New()
		cid = &nid
		if _, err := tx.Exec(ctx, `INSERT INTO hris.candidates (id, property_id, full_name, email, phone, city, education, current_employer, current_title,
			experience_years, source, cv_file_id, consent_at, talent_pool_consent) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::numeric,'website',$11,now(),$12)`,
			nid, pid, in.FullName, in.Email, in.Phone, nullStr(in.City), nullStr(in.Education), nullStr(in.CurrentEmployer), nullStr(in.CurrentTitle), exp,
			cv, in.TalentPoolConsent); err != nil {
			return CareerApplicationResult{}, err
		}
	} else {
		// The latest application refreshes the profile and the consents (a
		// talent pool consent can be withdrawn by applying without it).
		if _, err := tx.Exec(ctx, `UPDATE hris.candidates SET full_name = $2, email = $3, phone = $4, city = coalesce($5, city),
			education = coalesce($6, education), current_employer = coalesce($7, current_employer), current_title = coalesce($8, current_title),
			experience_years = coalesce($9::numeric, experience_years), cv_file_id = coalesce($10, cv_file_id), consent_at = now(),
			talent_pool_consent = $11 WHERE id = $1`, *cid, in.FullName, in.Email, in.Phone, nullStr(in.City), nullStr(in.Education),
			nullStr(in.CurrentEmployer), nullStr(in.CurrentTitle), exp, cv, in.TalentPoolConsent); err != nil {
			return CareerApplicationResult{}, err
		}
	}
	aid, no, err := m.NewApplication(ctx, tx, pid, in.RequisitionID, *cid, "website", in.CoverLetter)
	if err != nil {
		if e, ok := errs.As(err); ok && e.Code == "application_exists" {
			return CareerApplicationResult{}, errs.Conflict("application_exists", "you already applied for this position")
		}
		return CareerApplicationResult{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "website_application", EntityType: "hris.application", EntityID: aid.String(),
		EntityLabel: no, PropertyID: &pid, After: map[string]any{"candidateId": *cid, "newCandidate": created, "requisitionId": in.RequisitionID,
			"cv": cv != nil, "talentPoolConsent": in.TalentPoolConsent}, ActorName: "Website visitor"}); err != nil {
		return CareerApplicationResult{}, err
	}
	var title string
	if err := tx.QueryRow(ctx, `SELECT title FROM hris.job_requisitions WHERE id = $1`, in.RequisitionID).Scan(&title); err != nil {
		return CareerApplicationResult{}, err
	}
	pname, _ := org.PropertyName(ctx, tx, pid)
	email := in.Email
	if err := m.notifyCandidate(ctx, tx, pid, &email, in.FullName, "hris.candidate_application_received", map[string]any{"candidate": in.FullName,
		"title": title, "number": no, "property": pname}); err != nil {
		return CareerApplicationResult{}, err
	}
	return CareerApplicationResult{Status: "received", Number: no}, nil
}
