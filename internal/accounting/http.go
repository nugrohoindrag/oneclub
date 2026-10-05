package accounting

// HTTP API of Accounting (PRD P4 §11 API surface: /api/v1/accounting/...).
// Ledger, trial balance and financial reports are instance-scoped routes
// filtered by property (consolidated over every property the user may see
// when no property is given, FR-FIN-03); everything else works on the
// active property.

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

const base = "/api/v1/accounting"

func (m *Module) add(reg *route.Registry, rt route.Route, tag string) {
	rt.Module, rt.Tag = "accounting", tag
	reg.Add(rt)
}

// propertyRoute declares a route of the active property.
func (m *Module) propertyRoute(reg *route.Registry, tag string, rt route.Route) {
	rt.Scope = route.ScopeProperty
	m.add(reg, rt, tag)
}

// globalRoute declares an instance route (consolidated reports).
func (m *Module) globalRoute(reg *route.Registry, tag string, rt route.Route) {
	rt.Scope = route.ScopeGlobal
	m.add(reg, rt, tag)
}

// idWrite runs a write use case on the {id} of the path.
func idWrite[Req any, Res any](db *dbtx.DB, fn func(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in Req) (Res, error)) http.HandlerFunc {
	return handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in Req) (Res, error) {
		rid, err := handle.ID(r)
		if err != nil {
			var zero Res
			return zero, err
		}
		return fn(ctx, tx, handle.Property(ctx), rid, in)
	})
}

// idRead runs a read use case on the {id} of the path.
func idRead[Res any](db *dbtx.DB, fn func(ctx context.Context, q dbtx.Querier, property, rid uuid.UUID) (Res, error)) http.HandlerFunc {
	return handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Res, error) {
		rid, err := handle.ID(r)
		if err != nil {
			var zero Res
			return zero, err
		}
		return fn(ctx, tx, handle.Property(ctx), rid)
	})
}

func pageOf[T any](items []T, err error) (httpx.Page[T], error) { return handle.Page(items, err) }

// dateRange reads from / to (default: the month to date).
func dateRange(ctx context.Context, q dbtx.Querier, r *http.Request) (time.Time, time.Time, error) {
	today := dateOnly(time.Now().In(calendar.Location(ctx, q)))
	if p := handle.Property(ctx); p != uuid.Nil {
		today = localToday(ctx, q, p)
	}
	to, err := handle.QueryDate(r, "to", today)
	if err != nil {
		return to, to, err
	}
	from, err := handle.QueryDate(r, "from", monthStart(to))
	if err != nil {
		return from, to, err
	}
	if from.After(to) {
		return from, to, errs.BadRequest("invalid_range", "from must not be after to")
	}
	return from, to, nil
}

// propertyFilter reads the optional propertyId of instance routes.
func propertyFilter(r *http.Request) (*uuid.UUID, error) { return handle.QueryUUID(r, "propertyId") }

// AccountingSetupStatus is the accounting setup of the active property.
type AccountingSetupStatus struct {
	Book           *AccountingBook         `json:"book"`
	Configuration  AccountingConfiguration `json:"configuration"`
	Roles          []string                `json:"roles" doc:"Default-account roles of the Accounting Configuration"`
	Accounts       int                     `json:"accounts"`
	PostingRules   int                     `json:"postingRules"`
	TaxCodes       int                     `json:"taxCodes"`
	OpenExceptions int                     `json:"openExceptions"`
}

// RunPostingInput posts the pending source documents up to a date.
type RunPostingInput struct {
	UpTo string `json:"upTo,omitempty" doc:"Business date (default: today)"`
}

// PostingRunResult reports a posting run.
type PostingRunResult struct {
	Journals   []uuid.UUID `json:"journals"`
	Exceptions int         `json:"exceptions"`
}

// Register adds the Accounting routes and master data.
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	for _, d := range Defs() {
		eng.Register(reg, d)
	}
	m.registerSetup(reg)
	m.registerJournals(reg)
	m.registerPeriods(reg)
	m.registerPosting(reg)
	m.registerARAP(reg)
	m.registerBank(reg)
	m.registerRevenueTax(reg)
	m.registerReports(reg)
	m.registerOpening(reg)
}

func (m *Module) registerSetup(reg *route.Registry) {
	db := m.DB
	const tag = "Accounting Setup"
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/book", Summary: "Accounting setup of the property: book, configuration, counts",
		Permission: "accounting.setup.view", Response: AccountingSetupStatus{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (AccountingSetupStatus, error) {
			p := handle.Property(ctx)
			out := AccountingSetupStatus{Roles: Roles()}
			if b, err := GetBook(ctx, tx, p); err == nil {
				out.Book = &b
			}
			cfg, err := LoadConfiguration(ctx, tx, p)
			if err != nil {
				return out, err
			}
			out.Configuration = cfg
			err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM accounting.accounts)::int, (SELECT count(*) FROM accounting.posting_rules WHERE status = 'active')::int,
				(SELECT count(*) FROM accounting.tax_codes)::int, (SELECT count(*) FROM accounting.posting_exceptions WHERE property_id = $1 AND status = 'open')::int`,
				p).Scan(&out.Accounts, &out.PostingRules, &out.TaxCodes, &out.OpenExceptions)
			return out, err
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/book:load-template",
		Summary:    "Load the OneClub club & hospitality chart of accounts, tax codes and default posting rules and open the book (cut-over date)",
		Permission: "accounting.setup.manage", Request: COATemplateInput{}, Response: COALoadResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in COATemplateInput) (COALoadResult, error) {
			return m.SetupBook(ctx, tx, handle.Property(ctx), in)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/book:sign-off",
		Summary: "Sign the transition off: the book goes live and the Accounting Export stops (FR-TRS-04)", Permission: "accounting.setup.sign_off",
		Request: AccountingSignOffInput{}, Response: AccountingBook{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in AccountingSignOffInput) (AccountingBook, error) {
			return m.SignOff(ctx, tx, handle.Property(ctx), in)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/postings:run",
		Summary:    "Post the pending operational documents up to a business date (catch-up of the daily summary, FR-PST-04)",
		Permission: "accounting.posting.manage", Request: RunPostingInput{}, Response: PostingRunResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RunPostingInput) (PostingRunResult, error) {
			p := handle.Property(ctx)
			d := localToday(ctx, tx, p)
			if in.UpTo != "" {
				var err error
				if d, err = parseDate("upTo", in.UpTo); err != nil {
					return PostingRunResult{}, err
				}
			}
			res, err := m.RunPostings(ctx, tx, p, d)
			if err != nil {
				return res, err
			}
			return res, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "run_postings", EntityType: "accounting.posting_run",
				EntityID: ymd(d), EntityLabel: "Postings up to " + ymd(d), PropertyID: &p, After: res})
		})})
}

func (m *Module) registerJournals(reg *route.Registry) {
	db := m.DB
	const tag = "General Ledger"
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/journals", Summary: "Journals (filter by date, type, source, account, text)",
		Permission: "accounting.journal.view", Response: AccountingJournal{}, List: true,
		Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "filter[journalType]"}, {Name: "filter[sourceType]"}, {Name: "filter[accountId]"},
			{Name: "filter[sourceId]"}, {Name: "q"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[AccountingJournal], error) {
			lp := httpx.ParseList(r)
			from, to, err := dateRange(ctx, tx, r)
			if err != nil {
				return httpx.Page[AccountingJournal]{}, err
			}
			// a document or text search spans every date unless a range is given
			// (journals of a business date ahead of the calendar included)
			if lp.Filters["sourceId"] != "" || lp.Q != "" {
				if r.URL.Query().Get("from") == "" {
					from = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
				}
				if r.URL.Query().Get("to") == "" {
					to = time.Date(2200, 12, 31, 0, 0, 0, 0, time.UTC)
				}
			}
			return pageOf(handle.List[AccountingJournal](tx.Query(ctx, journalSelect+` WHERE j.property_id = $1 AND j.journal_date BETWEEN $2 AND $3
				AND ($4 = '' OR j.journal_type = $4) AND ($5 = '' OR j.source_type = $5)
				AND ($6 = '' OR EXISTS (SELECT 1 FROM accounting.journal_lines l WHERE l.journal_id = j.id AND l.account_id::text = $6))
				AND ($7 = '' OR j.source_id = $7 OR EXISTS (SELECT 1 FROM accounting.posted_sources s WHERE s.journal_id = j.id AND s.source_id::text = $7))
				AND ($8 = '' OR j.number ILIKE '%' || $8 || '%' OR j.description ILIKE '%' || $8 || '%' OR j.source_ref ILIKE '%' || $8 || '%')
				ORDER BY j.journal_date DESC, j.posted_at DESC LIMIT $9`, handle.Property(ctx), from, to, lp.Filters["journalType"], lp.Filters["sourceType"],
				lp.Filters["accountId"], lp.Filters["sourceId"], lp.Q, lp.Limit)))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/journals/{id}",
		Summary: "Journal with its lines and source documents (drill-down, FR-ACC-05)", Permission: "accounting.journal.view", Response: AccountingJournal{},
		Handler: idRead(db, func(ctx context.Context, tx dbtx.Querier, p, jid uuid.UUID) (AccountingJournal, error) {
			j, err := GetJournal(ctx, tx, jid)
			if err == nil && j.PropertyID != p {
				return AccountingJournal{}, errs.NotFound("journal")
			}
			return j, err
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/journals/{id}:reverse",
		Summary: "Reverse a posted journal (append-only correction, FR-ACC-03)", Permission: "accounting.journal.reverse", Request: JournalReverseInput{},
		Response: AccountingJournal{}, Status: http.StatusOK, Handler: idWrite(db, m.ReverseJournal)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/manual-journals", Summary: "Manual journals (requests and posted)",
		Permission: "accounting.journal.view", Response: ManualJournal{}, List: true, Query: []route.Param{{Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ManualJournal], error) {
			return pageOf(ListManualJournals(ctx, tx, handle.Property(ctx), httpx.ParseList(r).Filters["status"]))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/manual-journals/{id}", Summary: "Manual journal",
		Permission: "accounting.journal.view", Response: ManualJournal{}, Handler: idRead(db, GetManualJournal)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/manual-journals",
		Summary: "Draft a manual or adjustment journal (submit: post, or approval above the threshold, FR-ACC-02)", Permission: "accounting.journal.create",
		Request: ManualJournalInput{}, Response: ManualJournal{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ManualJournalInput) (ManualJournal, error) {
			return m.CreateManualJournal(ctx, tx, handle.Property(ctx), in)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPatch, Path: base + "/manual-journals/{id}", Summary: "Edit a draft manual journal",
		Permission: "accounting.journal.create", Request: ManualJournalInput{}, Response: ManualJournal{}, Handler: idWrite(db, m.UpdateManualJournal)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/manual-journals/{id}:submit",
		Summary: "Submit a manual journal: posted below the approval threshold, else sent for approval", Permission: "accounting.journal.create",
		Request: handle.Empty{}, Response: ManualJournal{}, Status: http.StatusOK,
		Handler: idWrite(db, func(ctx context.Context, tx pgx.Tx, p, mid uuid.UUID, _ handle.Empty) (ManualJournal, error) {
			return m.SubmitManualJournal(ctx, tx, p, mid)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/manual-journals/{id}:cancel", Summary: "Cancel a draft or pending manual journal",
		Permission: "accounting.journal.create", Request: ExceptionResolveInput{}, Response: ManualJournal{}, Status: http.StatusOK,
		Handler: idWrite(db, m.CancelManualJournal)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/recurring-journals:run",
		Summary: "Post the recurring journals due by a date (also run daily)", Permission: "accounting.journal.create", Request: RunRecurringInput{},
		Response: RecurringRunResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RunRecurringInput) (RecurringRunResult, error) {
			p := handle.Property(ctx)
			d := localToday(ctx, tx, p)
			if in.AsOf != "" {
				var err error
				if d, err = parseDate("asOf", in.AsOf); err != nil {
					return RecurringRunResult{}, err
				}
			}
			res, err := m.RunRecurring(ctx, tx, p, d)
			if err != nil {
				return res, err
			}
			return res, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "run", EntityType: "accounting.recurring_journal", EntityID: ymd(d),
				EntityLabel: "Recurring journals " + ymd(d), PropertyID: &p, After: res})
		})})
	m.globalRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/general-ledger",
		Summary: "General Ledger of an account (per property or consolidated, filter by dimension; FR-ACC-05)", Permission: "accounting.ledger.view",
		Response: GeneralLedger{}, Query: []route.Param{{Name: "accountId", Required: true}, {Name: "from"}, {Name: "to"}, {Name: "propertyId"},
			{Name: "businessLine"}, {Name: "costCenter"}, {Name: "departmentId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (GeneralLedger, error) {
			aid, err := handle.QueryUUID(r, "accountId")
			if err != nil {
				return GeneralLedger{}, err
			}
			if aid == nil {
				return GeneralLedger{}, errs.BadRequest("account_required", "accountId is required")
			}
			from, to, err := dateRange(ctx, tx, r)
			if err != nil {
				return GeneralLedger{}, err
			}
			prop, err := propertyFilter(r)
			if err != nil {
				return GeneralLedger{}, err
			}
			dep, err := handle.QueryUUID(r, "departmentId")
			if err != nil {
				return GeneralLedger{}, err
			}
			q := r.URL.Query()
			return GeneralLedgerOf(ctx, tx, *aid, LedgerFilter{Property: prop, From: from, To: to, BusinessLine: q.Get("businessLine"),
				CostCenter: q.Get("costCenter"), DepartmentID: dep})
		})})
	m.globalRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/trial-balance",
		Summary: "Trial Balance of a period (per property or consolidated MAIN + MDR)", Permission: "accounting.ledger.view", Response: TrialBalance{},
		Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "propertyId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TrialBalance, error) {
			from, to, err := dateRange(ctx, tx, r)
			if err != nil {
				return TrialBalance{}, err
			}
			prop, err := propertyFilter(r)
			if err != nil {
				return TrialBalance{}, err
			}
			return TrialBalanceOf(ctx, tx, prop, from, to)
		})})
}

func (m *Module) registerPeriods(reg *route.Registry) {
	db := m.DB
	const tag = "Financial Periods"
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/periods", Summary: "Financial periods", Permission: "accounting.period.view",
		Response: FinancialPeriod{}, List: true, Query: []route.Param{{Name: "year", Type: "integer"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[FinancialPeriod], error) {
			return pageOf(ListPeriods(ctx, tx, handle.Property(ctx), handle.QueryInt(r, "year", 0)))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/periods/{id}", Summary: "Financial period with its closing checklist",
		Permission: "accounting.period.view", Response: FinancialPeriod{}, Handler: idRead(db, GetPeriod)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/periods:generate", Summary: "Create the twelve periods of a year",
		Permission: "accounting.period.close", Request: GeneratePeriodsInput{}, Response: FinancialPeriod{}, List: true, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in GeneratePeriodsInput) (httpx.Page[FinancialPeriod], error) {
			return pageOf(m.GeneratePeriods(ctx, tx, handle.Property(ctx), in))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/periods/{id}:soft-close",
		Summary: "Soft-close a period (only finance adjustments afterwards)", Permission: "accounting.period.close", Request: handle.Empty{}, Response: FinancialPeriod{},
		Status: http.StatusOK, Handler: idWrite(db, func(ctx context.Context, tx pgx.Tx, p, pid uuid.UUID, _ handle.Empty) (FinancialPeriod, error) {
			return m.SoftClosePeriod(ctx, tx, p, pid)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/periods/{id}:close",
		Summary: "Close a period after the closing checklist (publishes accounting.period_closed)", Permission: "accounting.period.close",
		Request: handle.Empty{}, Response: FinancialPeriod{}, Status: http.StatusOK,
		Handler: idWrite(db, func(ctx context.Context, tx pgx.Tx, p, pid uuid.UUID, _ handle.Empty) (FinancialPeriod, error) {
			return m.ClosePeriod(ctx, tx, p, pid)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/periods/{id}:reopen", Summary: "Request reopening a period (approval)",
		Permission: "accounting.period.reopen", Request: PeriodReopenInput{}, Response: FinancialPeriod{}, Status: http.StatusOK, Handler: idWrite(db, m.ReopenPeriod)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/fiscal-years", Summary: "Closed fiscal years",
		Permission: "accounting.period.view", Response: FiscalYear{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[FiscalYear], error) {
			return pageOf(ListFiscalYears(ctx, tx, handle.Property(ctx)))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/fiscal-years:close",
		Summary: "Year-end closing of revenue and expenses to retained earnings (FR-ACC-07)", Permission: "accounting.period.close", Request: YearEndInput{},
		Response: FiscalYear{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in YearEndInput) (FiscalYear, error) {
			return m.CloseYear(ctx, tx, handle.Property(ctx), in)
		})})
}

// GenerateRulesResult reports generated default rules.
type GenerateRulesResult struct {
	Created int `json:"created"`
}

func (m *Module) registerPosting(reg *route.Registry) {
	db := m.DB
	const tag = "Posting"
	m.globalRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/posting-rules:generate-defaults",
		Summary: "Generate the missing default posting rules from the Accounting Export components (FR-PST-08)", Permission: "accounting.posting_rule.create",
		Request: handle.Empty{}, Response: GenerateRulesResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (GenerateRulesResult, error) {
			n, err := GenerateDefaultRules(ctx, tx)
			if err != nil {
				return GenerateRulesResult{}, err
			}
			return GenerateRulesResult{Created: n}, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "generate_defaults",
				EntityType: "accounting.posting_rule", EntityID: "defaults", EntityLabel: "Default posting rules", After: map[string]any{"created": n}})
		})})
	m.globalRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/posting-rules/{id}:new-version",
		Summary: "New version of a posting rule from an effective date (FR-PST-06)", Permission: "accounting.posting_rule.update", Request: PostingRuleVersionInput{},
		Response: PostingRuleVersion{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PostingRuleVersionInput) (PostingRuleVersion, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return PostingRuleVersion{}, err
			}
			return m.NewRuleVersion(ctx, tx, rid, in)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/posting-exceptions", Summary: "Posting Exception queue (FR-PST-04)",
		Permission: "accounting.posting.view", Response: AccountingPostingException{}, List: true,
		Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[eventType]"}, {Name: "filter[reason]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[AccountingPostingException], error) {
			lp := httpx.ParseList(r)
			return pageOf(ListExceptions(ctx, tx, handle.Property(ctx), lp.Filters["status"], lp.Filters["eventType"], lp.Filters["reason"], lp.Limit))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/posting-exceptions/{id}", Summary: "Posting exception with its stored event",
		Permission: "accounting.posting.view", Response: AccountingPostingException{},
		Handler: idRead(db, func(ctx context.Context, tx dbtx.Querier, p, xid uuid.UUID) (AccountingPostingException, error) {
			x, err := getException(ctx, tx, xid)
			if err == nil && x.PropertyID != p {
				return AccountingPostingException{}, errs.NotFound("posting exception")
			}
			return x, err
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/posting-exceptions/{id}:repost",
		Summary: "Repost the event of an exception with the rules in force (the suspense journal is reversed)", Permission: "accounting.posting.manage",
		Request: handle.Empty{}, Response: AccountingPostingException{}, Status: http.StatusOK,
		Handler: idWrite(db, func(ctx context.Context, tx pgx.Tx, p, xid uuid.UUID, _ handle.Empty) (AccountingPostingException, error) {
			return m.RepostException(ctx, tx, p, xid)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/posting-exceptions/{id}:ignore", Summary: "Ignore an exception with a reason",
		Permission: "accounting.posting.manage", Request: ExceptionResolveInput{}, Response: AccountingPostingException{}, Status: http.StatusOK,
		Handler: idWrite(db, m.IgnoreException)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/processed-events",
		Summary: "Consumed events and the journals they posted (idempotency trace, FR-PST-05)", Permission: "accounting.posting.view",
		Response: AccountingProcessedEvent{}, List: true, Query: []route.Param{{Name: "filter[eventType]"}, {Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[AccountingProcessedEvent], error) {
			lp := httpx.ParseList(r)
			return pageOf(ListProcessedEvents(ctx, tx, handle.Property(ctx), lp.Filters["eventType"], lp.Filters["status"], lp.Limit))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/posting-reconciliation",
		Summary: "Posting reconciliation of a business day: journals per component = Daily Revenue Report (FR-PST-07)", Permission: "accounting.posting.view",
		Response: PostingReconciliation{}, Query: []route.Param{{Name: "date"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PostingReconciliation, error) {
			p := handle.Property(ctx)
			d, err := handle.QueryDate(r, "date", localToday(ctx, tx, p))
			if err != nil {
				return PostingReconciliation{}, err
			}
			return ReconcilePosting(ctx, tx, p, d)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/reconciliation",
		Summary: "Control accounts against their sub-ledgers (AR, AP, deferred revenue, deposits, caddy fees)", Permission: "accounting.ledger.view",
		Response: ControlReconciliation{}, Query: []route.Param{{Name: "asOf"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (ControlReconciliation, error) {
			p := handle.Property(ctx)
			d, err := handle.QueryDate(r, "asOf", localToday(ctx, tx, p))
			if err != nil {
				return ControlReconciliation{}, err
			}
			return ReconcileControls(ctx, tx, p, d)
		})})
}

func (m *Module) registerARAP(reg *route.Registry) {
	db := m.DB
	const ar, ap = "Accounts Receivable", "Accounts Payable"
	m.propertyRoute(reg, ar, route.Route{Method: http.MethodGet, Path: base + "/receivables", Summary: "Receivables per customer / corporate (AR ledger)",
		Permission: "accounting.receivable.view", Response: ARReceivable{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ARReceivable], error) {
			return pageOf(ListReceivables(ctx, tx, handle.Property(ctx)))
		})})
	m.propertyRoute(reg, ar, route.Route{Method: http.MethodGet, Path: base + "/ar-entries", Summary: "AR ledger movements of an invoice or account",
		Permission: "accounting.receivable.view", Response: AREntry{}, List: true, Query: []route.Param{{Name: "invoiceId"}, {Name: "accountId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[AREntry], error) {
			inv, err := handle.QueryUUID(r, "invoiceId")
			if err != nil {
				return httpx.Page[AREntry]{}, err
			}
			acc, err := handle.QueryUUID(r, "accountId")
			if err != nil {
				return httpx.Page[AREntry]{}, err
			}
			return pageOf(ListAREntries(ctx, tx, handle.Property(ctx), inv, acc))
		})})
	m.propertyRoute(reg, ar, route.Route{Method: http.MethodGet, Path: base + "/ar-aging",
		Summary: "AR Aging (0–30, 31–60, 61–90, > 90) = operational ageing of billing, with the AR control balance", Permission: "accounting.receivable.view",
		Response: ARAging{}, Query: []route.Param{{Name: "asOf"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (ARAging, error) {
			p := handle.Property(ctx)
			d, err := handle.QueryDate(r, "asOf", localToday(ctx, tx, p))
			if err != nil {
				return ARAging{}, err
			}
			return ARAgingAsOf(ctx, tx, p, d)
		})})
	m.propertyRoute(reg, ar, route.Route{Method: http.MethodGet, Path: base + "/allowances", Summary: "Allowance for doubtful accounts runs",
		Permission: "accounting.receivable.view", Response: ARAllowanceRun{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ARAllowanceRun], error) {
			return pageOf(ListAllowanceRuns(ctx, tx, handle.Property(ctx)))
		})})
	m.propertyRoute(reg, ar, route.Route{Method: http.MethodPost, Path: base + "/allowances",
		Summary: "Compute the allowance for doubtful accounts from the AR aging (posted after approval, FR-AR-05)", Permission: "accounting.receivable.manage",
		Request: ARAllowanceInput{}, Response: ARAllowanceRun{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ARAllowanceInput) (ARAllowanceRun, error) {
			return m.CreateAllowanceRun(ctx, tx, handle.Property(ctx), in)
		})})
	m.propertyRoute(reg, ap, route.Route{Method: http.MethodGet, Path: base + "/payables", Summary: "Payables (AP open items)",
		Permission: "accounting.payable.view", Response: APItem{}, List: true,
		Query: []route.Param{{Name: "filter[supplierId]"}, {Name: "filter[status]", Description: "open, partially_paid, paid, unpaid"}, {Name: "dueBy"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[APItem], error) {
			lp := httpx.ParseList(r)
			var sup *uuid.UUID
			if v := lp.Filters["supplierId"]; v != "" {
				u, err := uuid.Parse(v)
				if err != nil {
					return httpx.Page[APItem]{}, errs.BadRequest("invalid_supplierId", "supplierId must be a UUID")
				}
				sup = &u
			}
			return pageOf(ListAPItems(ctx, tx, handle.Property(ctx), sup, lp.Filters["status"], r.URL.Query().Get("dueBy"), lp.Limit))
		})})
	m.propertyRoute(reg, ap, route.Route{Method: http.MethodPost, Path: base + "/vendor-bills",
		Summary: "Record a service bill without purchase order (expense, VAT input, withholding tax) to AP", Permission: "accounting.payable.manage",
		Request: VendorBillInput{}, Response: APItem{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in VendorBillInput) (APItem, error) {
			return m.CreateVendorBill(ctx, tx, handle.Property(ctx), in)
		})})
	m.propertyRoute(reg, ap, route.Route{Method: http.MethodGet, Path: base + "/ap-aging", Summary: "AP Aging per supplier (days past due)",
		Permission: "accounting.payable.view", Response: APAging{}, Query: []route.Param{{Name: "asOf"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (APAging, error) {
			p := handle.Property(ctx)
			d, err := handle.QueryDate(r, "asOf", localToday(ctx, tx, p))
			if err != nil {
				return APAging{}, err
			}
			return APAgingAsOf(ctx, tx, &p, d)
		})})
	m.propertyRoute(reg, ap, route.Route{Method: http.MethodGet, Path: base + "/payment-runs", Summary: "Payment runs", Permission: "accounting.payment_run.view",
		Response: APPaymentRun{}, List: true, Query: []route.Param{{Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[APPaymentRun], error) {
			return pageOf(ListPaymentRuns(ctx, tx, handle.Property(ctx), httpx.ParseList(r).Filters["status"]))
		})})
	m.propertyRoute(reg, ap, route.Route{Method: http.MethodGet, Path: base + "/payment-runs/{id}", Summary: "Payment run with its payables",
		Permission: "accounting.payment_run.view", Response: APPaymentRun{},
		Handler: idRead(db, func(ctx context.Context, tx dbtx.Querier, p, rid uuid.UUID) (APPaymentRun, error) {
			var owner uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT property_id FROM accounting.payment_runs WHERE id = $1`, rid).Scan(&owner); err != nil || owner != p {
				return APPaymentRun{}, errs.NotFound("payment run")
			}
			return GetPaymentRun(ctx, tx, rid)
		})})
	m.propertyRoute(reg, ap, route.Route{Method: http.MethodPost, Path: base + "/payment-runs",
		Summary: "Draft a payment run: chosen payables or everything due by a date (partial payments allowed)", Permission: "accounting.payment_run.create",
		Request: APPaymentRunInput{}, Response: APPaymentRun{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in APPaymentRunInput) (APPaymentRun, error) {
			return m.CreatePaymentRun(ctx, tx, handle.Property(ctx), in)
		})})
	m.propertyRoute(reg, ap, route.Route{Method: http.MethodPost, Path: base + "/payment-runs/{id}:submit", Summary: "Submit a payment run for approval",
		Permission: "accounting.payment_run.create", Request: handle.Empty{}, Response: APPaymentRun{}, Status: http.StatusOK,
		Handler: idWrite(db, func(ctx context.Context, tx pgx.Tx, p, rid uuid.UUID, _ handle.Empty) (APPaymentRun, error) {
			return m.SubmitPaymentRun(ctx, tx, p, rid)
		})})
	m.propertyRoute(reg, ap, route.Route{Method: http.MethodPost, Path: base + "/payment-runs/{id}:cancel", Summary: "Cancel a payment run",
		Permission: "accounting.payment_run.create", Request: ExceptionResolveInput{}, Response: APPaymentRun{}, Status: http.StatusOK, Handler: idWrite(db, m.CancelPaymentRun)})
	m.propertyRoute(reg, ap, route.Route{Method: http.MethodPost, Path: base + "/payment-runs/{id}:execute",
		Summary: "Execute an approved run: vendor payments, payment journal, accounting.vendor_payment_made", Permission: "accounting.payment_run.execute",
		Request: handle.Empty{}, Response: APPaymentRun{}, Status: http.StatusOK,
		Handler: idWrite(db, func(ctx context.Context, tx pgx.Tx, p, rid uuid.UUID, _ handle.Empty) (APPaymentRun, error) {
			return m.ExecutePaymentRun(ctx, tx, p, rid)
		})})
	m.propertyRoute(reg, ap, route.Route{Method: http.MethodGet, Path: base + "/payment-runs/{id}/bank-file", Summary: "Bulk transfer file of a run (CSV)",
		Permission: "accounting.payment_run.view", RawContent: "text/csv",
		Handler: func(w http.ResponseWriter, r *http.Request) {
			rid, err := handle.ID(r)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			var out string
			err = db.WithReadTx(r.Context(), func(tx pgx.Tx) error {
				var err error
				out, err = PaymentRunFile(r.Context(), tx, handle.Property(r.Context()), rid)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="payment-run.csv"`)
			_, _ = w.Write([]byte(out))
		}})
	m.propertyRoute(reg, ap, route.Route{Method: http.MethodGet, Path: base + "/vendor-payments", Summary: "Vendor payments",
		Permission: "accounting.payable.view", Response: APVendorPayment{}, List: true, Query: []route.Param{{Name: "supplierId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[APVendorPayment], error) {
			sup, err := handle.QueryUUID(r, "supplierId")
			if err != nil {
				return httpx.Page[APVendorPayment]{}, err
			}
			return pageOf(ListVendorPayments(ctx, tx, handle.Property(ctx), sup))
		})})
}

func (m *Module) registerBank(reg *route.Registry) {
	db := m.DB
	const tag = "Cash & Bank"
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/bank-transactions", Summary: "Bank transactions (imported statement lines)",
		Permission: "accounting.bank_transaction.view", Response: BankTransaction{}, List: true,
		Query: []route.Param{{Name: "filter[bankAccountId]"}, {Name: "filter[status]"}, {Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[BankTransaction], error) {
			lp := httpx.ParseList(r)
			var bank *uuid.UUID
			if v := lp.Filters["bankAccountId"]; v != "" {
				u, err := uuid.Parse(v)
				if err != nil {
					return httpx.Page[BankTransaction]{}, errs.BadRequest("invalid_bankAccountId", "bankAccountId must be a UUID")
				}
				bank = &u
			}
			q := r.URL.Query()
			return pageOf(ListBankTransactions(ctx, tx, handle.Property(ctx), bank, lp.Filters["status"], q.Get("from"), q.Get("to"), lp.Limit))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/bank-transactions:import",
		Summary: "Import a bank statement (CSV generic / BCA / Mandiri or MT940); duplicates are skipped (FR-BNK-02)", Permission: "accounting.bank_transaction.import",
		Request: BankStatementImportInput{}, Response: BankStatementImportResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in BankStatementImportInput) (BankStatementImportResult, error) {
			return m.ImportStatement(ctx, tx, handle.Property(ctx), in)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/bank-reconciliations", Summary: "Bank reconciliations",
		Permission: "accounting.bank_transaction.view", Response: BankReconciliation{}, List: true, Query: []route.Param{{Name: "bankAccountId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[BankReconciliation], error) {
			bank, err := handle.QueryUUID(r, "bankAccountId")
			if err != nil {
				return httpx.Page[BankReconciliation]{}, err
			}
			return pageOf(ListReconciliations(ctx, tx, handle.Property(ctx), bank))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/bank-reconciliations/{id}",
		Summary: "Bank reconciliation with the unmatched statement lines (exceptions) and open book lines", Permission: "accounting.bank_transaction.view",
		Response: BankReconciliation{}, Handler: idRead(db, GetReconciliation)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/bank-reconciliations", Summary: "Start the reconciliation of a statement",
		Permission: "accounting.bank_transaction.reconcile", Request: BankReconciliationInput{}, Response: BankReconciliation{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in BankReconciliationInput) (BankReconciliation, error) {
			return m.CreateReconciliation(ctx, tx, handle.Property(ctx), in)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/bank-reconciliations/{id}:auto-match",
		Summary: "Auto-match statement lines with gateway settlements, shift deposits, payments (FR-BNK-03)", Permission: "accounting.bank_transaction.reconcile",
		Request: handle.Empty{}, Response: BankAutoMatchResult{}, Status: http.StatusOK,
		Handler: idWrite(db, func(ctx context.Context, tx pgx.Tx, p, rid uuid.UUID, _ handle.Empty) (BankAutoMatchResult, error) {
			return m.AutoMatch(ctx, tx, p, rid)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/bank-reconciliations/{id}:match", Summary: "Match a statement line with book lines",
		Permission: "accounting.bank_transaction.reconcile", Request: BankMatchInput{}, Response: BankReconciliation{}, Status: http.StatusOK, Handler: idWrite(db, m.Match)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/bank-reconciliations/{id}:unmatch", Summary: "Release a matched statement line",
		Permission: "accounting.bank_transaction.reconcile", Request: BankUnmatchInput{}, Response: BankReconciliation{}, Status: http.StatusOK,
		Handler: idWrite(db, m.Unmatch)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/bank-reconciliations/{id}:resolve",
		Summary: "Settle a statement line with a journal (bank charges, interest) and match it", Permission: "accounting.bank_transaction.reconcile",
		Request: BankResolveLineInput{}, Response: BankReconciliation{}, Status: http.StatusOK, Handler: idWrite(db, m.ResolveLine)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/bank-reconciliations/{id}:ignore", Summary: "Ignore a statement line",
		Permission: "accounting.bank_transaction.reconcile", Request: BankIgnoreLineInput{}, Response: BankReconciliation{}, Status: http.StatusOK,
		Handler: idWrite(db, m.IgnoreLine)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/bank-reconciliations/{id}:complete",
		Summary: "Complete a reconciliation without difference", Permission: "accounting.bank_transaction.reconcile", Request: handle.Empty{},
		Response: BankReconciliation{}, Status: http.StatusOK,
		Handler: idWrite(db, func(ctx context.Context, tx pgx.Tx, p, rid uuid.UUID, _ handle.Empty) (BankReconciliation, error) {
			return m.CompleteReconciliation(ctx, tx, p, rid)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/cash-transactions",
		Summary: "Cash documents: shift deposits, petty cash, transfers, counts", Permission: "accounting.cash.view", Response: CashTransaction{}, List: true,
		Query: []route.Param{{Name: "filter[kind]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CashTransaction], error) {
			return pageOf(ListCashTransactions(ctx, tx, handle.Property(ctx), httpx.ParseList(r).Filters["kind"]))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/cash-transactions",
		Summary:    "Post a cash document (deposit of shift cash with variance, petty cash expense / replenishment, transfer, count)",
		Permission: "accounting.cash.manage", Request: CashTransactionInput{}, Response: CashTransaction{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CashTransactionInput) (CashTransaction, error) {
			return m.CreateCashTransaction(ctx, tx, handle.Property(ctx), in)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/cash-shifts",
		Summary: "Closed cashier and POS shifts with the cash to deposit", Permission: "accounting.cash.view", Response: AccountingCashShift{}, List: true,
		Query: []route.Param{{Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[AccountingCashShift], error) {
			from, to, err := dateRange(ctx, tx, r)
			if err != nil {
				return httpx.Page[AccountingCashShift]{}, err
			}
			return pageOf(ListCashShifts(ctx, tx, handle.Property(ctx), from, to))
		})})
}

func (m *Module) registerRevenueTax(reg *route.Registry) {
	db := m.DB
	const tag = "Revenue & Tax"
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/revenue-allocations",
		Summary: "Revenue allocations received (packages, promotions; K3)", Permission: "accounting.revenue.view", Response: AccountingRevenueAllocation{}, List: true,
		Query: []route.Param{{Name: "filter[sourceType]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[AccountingRevenueAllocation], error) {
			return pageOf(ListRevenueAllocations(ctx, tx, handle.Property(ctx), httpx.ParseList(r).Filters["sourceType"]))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/deferred-revenue",
		Summary: "Deferred revenue & liabilities: GL vs sub-ledger, deferrals, recognition, breakage (FR-REV-02/03/05)", Permission: "accounting.revenue.view",
		Response: DeferredRevenueReport{}, Query: []route.Param{{Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (DeferredRevenueReport, error) {
			from, to, err := dateRange(ctx, tx, r)
			if err != nil {
				return DeferredRevenueReport{}, err
			}
			return DeferredRevenue(ctx, tx, handle.Property(ctx), from, to)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/service-charge-pools", Summary: "Service charge pools per month",
		Permission: "accounting.revenue.view", Response: ServiceChargePool{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ServiceChargePool], error) {
			return pageOf(ListServiceChargePools(ctx, tx, handle.Property(ctx)))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/service-charge-pools",
		Summary: "Compute the service charge pool of a month with the department basis (FR-REV-06)", Permission: "accounting.revenue.manage",
		Request: ServiceChargePoolInput{}, Response: ServiceChargePool{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ServiceChargePoolInput) (ServiceChargePool, error) {
			return m.ComputeServiceChargePool(ctx, tx, handle.Property(ctx), in)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/service-charge-pools/{id}:approve", Summary: "Approve a service charge pool",
		Permission: "accounting.revenue.manage", Request: handle.Empty{}, Response: ServiceChargePool{}, Status: http.StatusOK,
		Handler: idWrite(db, func(ctx context.Context, tx pgx.Tx, p, pid uuid.UUID, _ handle.Empty) (ServiceChargePool, error) {
			return m.ApproveServiceChargePool(ctx, tx, p, pid)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/tax-invoices", Summary: "Tax invoices (e-Faktur output and input)",
		Permission: "accounting.tax_invoice.view", Response: TaxInvoiceDoc{}, List: true,
		Query: []route.Param{{Name: "filter[direction]"}, {Name: "filter[taxPeriod]"}, {Name: "filter[status]"}, {Name: "filter[sourceId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TaxInvoiceDoc], error) {
			f := httpx.ParseList(r).Filters
			return pageOf(ListTaxInvoices(ctx, tx, handle.Property(ctx), f["direction"], f["taxPeriod"], f["status"], f["sourceId"]))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/tax-invoices/{id}", Summary: "Tax invoice",
		Permission: "accounting.tax_invoice.view", Response: TaxInvoiceDoc{}, Handler: idRead(db, GetTaxInvoice)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/tax-invoices/{id}:upload",
		Summary: "Upload an output tax invoice to Coretax (e-Faktur integration)", Permission: "accounting.tax_invoice.manage", Request: handle.Empty{},
		Response: TaxInvoiceDoc{}, Status: http.StatusOK,
		Handler: idWrite(db, func(ctx context.Context, tx pgx.Tx, p, tid uuid.UUID, _ handle.Empty) (TaxInvoiceDoc, error) {
			return m.UploadTaxInvoice(ctx, tx, p, tid)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/tax-invoices/{id}:cancel",
		Summary: "Cancel a tax invoice (optionally with a replacement draft)", Permission: "accounting.tax_invoice.manage", Request: TaxInvoiceCancelInput{},
		Response: TaxInvoiceDoc{}, Status: http.StatusOK, Handler: idWrite(db, m.CancelTaxInvoice)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/tax-invoices:export",
		Summary: "Export the tax invoices of a period (e-Faktur CSV)", Permission: "accounting.tax_invoice.manage", Request: EFakturExportInput{},
		Response: EFakturExport{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in EFakturExportInput) (EFakturExport, error) {
			return m.ExportTaxInvoices(ctx, tx, handle.Property(ctx), in)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/ppn-report", Summary: "PPN report of a tax period (FR-REV-08)",
		Permission: "accounting.tax_invoice.view", Response: PPNReport{}, Query: []route.Param{{Name: "period", Description: "YYYY-MM"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PPNReport, error) {
			p := handle.Property(ctx)
			period := r.URL.Query().Get("period")
			if period == "" {
				period = localToday(ctx, tx, p).Format("2006-01")
			}
			return PPNReportOf(ctx, tx, p, period)
		})})
}

func (m *Module) registerReports(reg *route.Registry) {
	db := m.DB
	m.globalRoute(reg, "Financial Reports", route.Route{Method: http.MethodGet, Path: base + "/reports/{kind}",
		Summary:    "Financial report: profit-loss, balance-sheet, cash-flow, revenue-by-business-line (per property or consolidated, with comparison)",
		Permission: "accounting.report.view", Response: FinancialStatement{},
		Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "propertyId"}, {Name: "compare", Enum: []string{"previous_period", "last_year"}}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (FinancialStatement, error) {
			from, to, err := dateRange(ctx, tx, r)
			if err != nil {
				return FinancialStatement{}, err
			}
			prop, err := propertyFilter(r)
			if err != nil {
				return FinancialStatement{}, err
			}
			cmp := r.URL.Query().Get("compare")
			switch chi.URLParam(r, "kind") {
			case "profit-loss":
				return ProfitLoss(ctx, tx, prop, from, to, cmp)
			case "balance-sheet":
				return BalanceSheet(ctx, tx, prop, to, cmp)
			case "cash-flow":
				return CashFlow(ctx, tx, prop, from, to, cmp)
			case "revenue-by-business-line":
				return RevenueByBusinessLine(ctx, tx, prop, from, to, cmp)
			}
			return FinancialStatement{}, errs.NotFound("financial report")
		})})
}

func (m *Module) registerOpening(reg *route.Registry) {
	db := m.DB
	const tag = "Accounting Transition"
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/opening-balances", Summary: "Opening balance batches",
		Permission: "accounting.opening_balance.view", Response: OpeningBalance{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[OpeningBalance], error) {
			return pageOf(ListOpeningBalances(ctx, tx, handle.Property(ctx)))
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/opening-balances/{id}", Summary: "Opening balance batch with its lines",
		Permission: "accounting.opening_balance.view", Response: OpeningBalance{}, Handler: idRead(db, GetOpeningBalance)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/opening-balances/{id}/reconciliation",
		Summary: "Reconciliation of a batch with the trial balance and the AR / AP sub-ledgers (FR-MIG-P4-06)", Permission: "accounting.opening_balance.view",
		Response: OpeningReconciliation{}, Handler: idRead(db, ReconcileOpening)})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/opening-balances",
		Summary: "Draft opening balances (lines and / or the operational sub-ledgers at the balance date)", Permission: "accounting.opening_balance.manage",
		Request: OpeningBalanceInput{}, Response: OpeningBalance{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in OpeningBalanceInput) (OpeningBalance, error) {
			return m.CreateOpeningBalance(ctx, tx, handle.Property(ctx), in)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/opening-balances:import",
		Summary: "Import opening balances incl. open AR / AP from a CSV (preview or draft)", Permission: "accounting.opening_balance.manage",
		Request: OpeningImportInput{}, Response: OpeningImportResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in OpeningImportInput) (OpeningImportResult, error) {
			res, err := m.ImportOpeningBalances(ctx, tx, handle.Property(ctx), in)
			if err == nil && in.Preview {
				p := handle.Property(ctx)
				err = audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "import_preview", EntityType: "accounting.opening_balance",
					EntityID: "preview", EntityLabel: "Opening balance import preview", PropertyID: &p, After: map[string]any{"rows": res.Rows, "issues": len(res.Issues)}})
			}
			return res, err
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodDelete, Path: base + "/opening-balances/{id}", Summary: "Delete a draft batch",
		Permission: "accounting.opening_balance.manage",
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (struct{}, error) {
			bid, err := handle.ID(r)
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, m.DeleteOpeningBalance(ctx, tx, handle.Property(ctx), bid)
		})})
	m.propertyRoute(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/opening-balances/{id}:post",
		Summary: "Post a balanced batch as the opening journal with its AR / AP open items (Finance Manager)", Permission: "accounting.opening_balance.post",
		Request: handle.Empty{}, Response: OpeningBalance{}, Status: http.StatusOK,
		Handler: idWrite(db, func(ctx context.Context, tx pgx.Tx, p, bid uuid.UUID, _ handle.Empty) (OpeningBalance, error) {
			return m.PostOpeningBalance(ctx, tx, p, bid)
		})})
}

// RunPostings posts the pending documents of the daily summary up to a
// date and catches up documents whose events found no book yet.
func (m *Module) RunPostings(ctx context.Context, tx pgx.Tx, property uuid.UUID, upTo time.Time) (PostingRunResult, error) {
	if _, err := GetBook(ctx, tx, property); err != nil {
		return PostingRunResult{}, err
	}
	if err := lockProperty(ctx, tx, property); err != nil {
		return PostingRunResult{}, err
	}
	src := "accounting.posting_run"
	out, err := m.sweepBilling(ctx, tx, property, sweepFilter{UpTo: &upTo, Revenue: true, Deferred: true, Payouts: true, AR: true},
		sweepHeader{Date: upTo, SourceType: src, SourceID: ymd(upTo), SourceRef: "Posting run " + ymd(upTo), Description: "Posting run up to " + ymd(upTo)})
	if err != nil {
		return PostingRunResult{}, err
	}
	res := PostingRunResult{Journals: out.Journals}
	if res.Journals == nil {
		res.Journals = []uuid.UUID{}
	}
	if len(out.Missing) > 0 {
		ev := Ev{ID: uuid.New(), Type: src, Property: property, OccurredAt: time.Now()}
		reason := out.Missing[0].Reason
		if err := m.recordEvent(ctx, tx, ev, outcome{Status: "exception", Journals: out.Journals, Missing: out.Missing}, reason); err != nil {
			return res, err
		}
		res.Exceptions = len(out.Missing)
	}
	return res, nil
}
