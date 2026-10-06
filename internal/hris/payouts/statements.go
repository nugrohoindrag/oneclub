package payouts

// Statements (FR-CDY-03, FR-INS-HR-04, FR-OPS-P5-03/04): the statement of
// one partner in a run — sources (settlements with rounds and non-cash tips
// / instructor fees with sessions), gross, PPh 21 non-employee, BPJS BPU,
// deductions and net — as JSON for the Caddy App "Payout History" /
// "Statement" and the instructor "Honor Statement", and as PDF. Partners see
// their own approved and paid statements only (hris.payout.own).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// PayoutStatement is one partner's statement.
type PayoutStatement struct {
	RunID       uuid.UUID     `json:"runId"`
	Number      string        `json:"number"`
	Kind        string        `json:"kind" enum:"caddy,instructor"`
	Title       string        `json:"title" doc:"Payout Statement (caddy) / Honor Statement (instructor)"`
	PeriodStart time.Time     `json:"periodStart"`
	PeriodEnd   time.Time     `json:"periodEnd"`
	PayDate     time.Time     `json:"payDate"`
	RunStatus   string        `json:"runStatus" enum:"draft,calculated,pending_approval,approved,paid,cancelled"`
	PaidOn      *time.Time    `json:"paidOn"`
	TaxNote     *string       `json:"taxNote"`
	Line        PayoutRunLine `json:"line"`
}

// PayoutHistoryItem is one statement in the partner's payout history.
type PayoutHistoryItem struct {
	LineID      uuid.UUID  `json:"lineId" db:"line_id"`
	RunID       uuid.UUID  `json:"runId" db:"run_id"`
	Number      string     `json:"number" db:"number"`
	Kind        string     `json:"kind" db:"kind" enum:"caddy,instructor"`
	PeriodStart time.Time  `json:"periodStart" db:"period_start"`
	PeriodEnd   time.Time  `json:"periodEnd" db:"period_end"`
	PayDate     time.Time  `json:"payDate" db:"pay_date"`
	RunStatus   string     `json:"runStatus" db:"run_status" enum:"approved,paid"`
	PaidOn      *time.Time `json:"paidOn" db:"paid_on"`
	Units       int        `json:"units" db:"units"`
	Gross       string     `json:"gross" db:"gross"`
	PPh21       string     `json:"pph21" db:"pph21"`
	BPU         string     `json:"bpu" db:"bpu"`
	Deductions  string     `json:"deductions" db:"deductions"`
	Net         string     `json:"net" db:"net"`
}

func (m *Module) registerStatements(reg *route.Registry) {
	tag := "HRIS Payouts"
	base := "/api/v1/hris/my-payouts"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base, Summary: "My payout history (Caddy App / instructor Honor Statement)",
		Permission: PermOwnPayout, Response: PayoutHistoryItem{}, List: true, Query: []route.Param{{Name: "kind", Enum: hris.PayoutKinds}},
		Handler: listRead(m.DB, m.myPayoutsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{lineId}", Summary: "My statement", Permission: PermOwnPayout,
		Response: PayoutStatement{}, Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PayoutStatement, error) {
			lid, err := handle.ID(r, "lineId")
			if err != nil {
				return PayoutStatement{}, err
			}
			return m.ownStatement(ctx, tx, lid)
		})})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{lineId}/pdf", Summary: "My statement (PDF)", Permission: PermOwnPayout,
		RawContent: "application/pdf", Handler: m.statementHTTP(true)})
}

// myPartners returns the caddy and instructor records of the signed-in user.
func (m *Module) myPartners(ctx context.Context, tx pgx.Tx, kind string) ([]Partner, error) {
	if m.Directory == nil {
		return nil, errs.Unavailable("partner directory")
	}
	var out []Partner
	for _, k := range hris.PayoutKinds {
		if kind != "" && kind != k {
			continue
		}
		p, err := m.Directory.PartnerOfUser(ctx, tx, handle.Property(ctx), k, handle.UserID(ctx))
		if err != nil {
			return nil, err
		}
		if p != nil {
			out = append(out, *p)
		}
	}
	if len(out) == 0 {
		return nil, errs.NotFound("caddy or instructor linked to your account")
	}
	return out, nil
}

func (m *Module) myPayoutsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PayoutHistoryItem, error) {
	kind := filterParam(r, "kind")
	if kind != "" && !oneOf(hris.PayoutKinds, kind) {
		return nil, enumErr("kind", hris.PayoutKinds)
	}
	ps, err := m.myPartners(ctx, tx, kind)
	if err != nil {
		return nil, err
	}
	var out []PayoutHistoryItem
	for _, p := range ps {
		items, err := handle.List[PayoutHistoryItem](tx.Query(ctx, `SELECT l.id AS line_id, r.id AS run_id, r.number, r.kind, r.period_start, r.period_end,
			r.pay_date, r.status AS run_status, r.paid_on, l.units, trim_scale(l.gross)::text AS gross, trim_scale(l.pph21)::text AS pph21,
			trim_scale(l.bpu_jkk + l.bpu_jkm)::text AS bpu, trim_scale(l.source_deductions + l.other_deductions)::text AS deductions,
			trim_scale(l.net)::text AS net
			FROM hris.payout_lines l JOIN hris.payout_runs r ON r.id = l.run_id
			WHERE r.property_id = $1 AND r.kind = $2 AND l.partner_id = $3 AND r.status IN ('approved', 'paid')
			ORDER BY r.period_end DESC, r.number DESC LIMIT 120`, handle.Property(ctx), p.Kind, p.ID))
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	if out == nil {
		out = []PayoutHistoryItem{}
	}
	return out, nil
}

// statement loads the statement of a line of the request's property.
func (m *Module) statement(ctx context.Context, tx pgx.Tx, runID *uuid.UUID, lid uuid.UUID) (PayoutStatement, error) {
	l, err := getOne[PayoutRunLine]("statement")(tx.Query(ctx, lineSelect+` WHERE id = $1 AND property_id = $2 AND ($3::uuid IS NULL OR run_id = $3)`,
		lid, handle.Property(ctx), runID))
	if err != nil {
		return PayoutStatement{}, err
	}
	run, err := getOne[PayoutRun]("payout run")(tx.Query(ctx, runSelect+` WHERE id = $1`, l.RunID))
	if err != nil {
		return PayoutStatement{}, err
	}
	maskLine(ctx, run.PropertyID, &l)
	title := "Payout Statement"
	if run.Kind == hris.PayoutKindInstructor {
		title = "Honor Statement"
	}
	return PayoutStatement{RunID: run.ID, Number: run.Number, Kind: run.Kind, Title: title, PeriodStart: run.PeriodStart, PeriodEnd: run.PeriodEnd,
		PayDate: run.PayDate, RunStatus: run.Status, PaidOn: run.PaidOn, TaxNote: run.TaxNote, Line: l}, nil
}

// ownStatement is a statement of the signed-in partner (approved / paid runs).
func (m *Module) ownStatement(ctx context.Context, tx pgx.Tx, lid uuid.UUID) (PayoutStatement, error) {
	s, err := m.statement(ctx, tx, nil, lid)
	if err != nil {
		return s, err
	}
	ps, err := m.myPartners(ctx, tx, s.Kind)
	if err != nil {
		return s, err
	}
	if len(ps) == 0 || ps[0].ID != s.Line.PartnerID || (s.RunStatus != "approved" && s.RunStatus != "paid") {
		return PayoutStatement{}, errs.NotFound("statement")
	}
	return s, nil
}

func (m *Module) statementHTTP(own bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lid, err := handle.ID(r, "lineId")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := r.Context()
		var out []byte
		var name string
		err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var s PayoutStatement
			var err error
			if own {
				s, err = m.ownStatement(ctx, tx, lid)
			} else {
				var rid uuid.UUID
				if rid, err = uuid.Parse(chi.URLParam(r, "id")); err != nil {
					return handle.Invalid("id", "invalid", "must be a uuid")
				}
				s, err = m.statement(ctx, tx, &rid, lid)
			}
			if err != nil {
				return err
			}
			out = StatementPDF(s)
			name = strings.ToLower(s.Number) + "-" + strings.ToLower(strings.ReplaceAll(deref(s.Line.PartnerCode), " ", "")) + ".pdf"
			pid := handle.Property(ctx)
			return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "export", EntityType: "hris.payout_run", EntityID: s.RunID.String(),
				EntityLabel: s.Number + " · " + s.Line.PartnerName, PropertyID: &pid, Metadata: map[string]any{"file": "statement", "lineId": lid.String()}})
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `inline; filename="`+name+`"`)
		_, _ = w.Write(out)
	}
}

// StatementPDF renders a statement.
func StatementPDF(s PayoutStatement) []byte {
	d := pdf.New()
	l := s.Line
	d.Row(16, true, s.Title)
	d.Row(10, false, s.Number+" · "+ymd(s.PeriodStart)+" - "+ymd(s.PeriodEnd), "Pay date "+ymd(s.PayDate))
	d.Space(6)
	d.Row(11, true, l.PartnerName+strings.TrimSpace(" "+deref(l.PartnerCode)))
	method := "Bank transfer"
	if l.PaymentMethod == "cash" {
		method = "Cash"
	}
	d.Row(9, false, "Payment: "+method+strings.TrimSpace(" "+deref(l.BankName)+" "+deref(l.BankAccountNo)), "Status: "+s.RunStatus)
	d.Rule(d.Y + 4)
	d.Space(8)
	d.Row(10, true, "Source", "Gross", "Deductions")
	for _, src := range l.Sources {
		d.Row(9, false, src.Number, money(dec(src.Gross)), money(dec(src.Deductions)))
	}
	d.Space(6)
	unitLabel := "Rounds"
	if s.Kind == hris.PayoutKindInstructor {
		unitLabel = "Sessions"
	}
	rows := [][2]string{{unitLabel, fmt.Sprint(l.Units)}}
	if s.Kind == hris.PayoutKindCaddy {
		rows = append(rows, [2]string{"Caddy fee", money(dec(l.Fee))}, [2]string{"Non-cash tips", money(dec(l.Tips))})
	} else {
		rows = append(rows, [2]string{"Honorarium", money(dec(l.Fee))})
	}
	rows = append(rows, [2]string{"Gross", money(dec(l.Gross))}, [2]string{"Settlement deductions", money(dec(l.SourceDeductions))},
		[2]string{"PPh 21 (taxable base " + money(dec(l.TaxBase)) + ", " + l.PPh21Rate + "%)", money(dec(l.PPh21))},
		[2]string{"BPJS Ketenagakerjaan BPU - JKK", money(dec(l.BPUJKK))}, [2]string{"BPJS Ketenagakerjaan BPU - JKM", money(dec(l.BPUJKM))})
	for _, x := range l.Deductions {
		rows = append(rows, [2]string{x.Label, money(dec(x.Amount))})
	}
	for _, r := range rows {
		d.Row(10, false, r[0], r[1])
	}
	d.Rule(d.Y + 4)
	d.Space(8)
	d.Row(12, true, "Net paid", money(dec(l.Net)))
	if s.TaxNote != nil {
		d.Space(12)
		for _, line := range wrap(*s.TaxNote, 110) {
			d.Row(7, false, line)
		}
	}
	return d.Bytes()
}

// wrap splits a text into lines of at most n characters (PDF notes).
func wrap(s string, n int) []string {
	var out []string
	line := ""
	for _, w := range strings.Fields(s) {
		if line != "" && len(line)+1+len(w) > n {
			out = append(out, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}
