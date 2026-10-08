package accounting

// Collections (AR) and Vendor Follow-up (AP): a worklist of the customers or
// suppliers with open items — outstanding, overdue, days late — with the
// collection work recorded on them (reminders, calls, promises to pay,
// disputes), the person responsible and the next follow-up date. The open
// amounts come from the AR / AP open items; accounting.follow_ups only holds
// the follow-up log.

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// arOpenItems are the open billing invoices of a property at a date with
// their party (billing account, else corporate account, else customer).
const arOpenItems = `WITH open AS (` + arOpenAsOf + `)
	SELECT id, number, coalesce(account_id, corporate_account_id, customer_id) AS party_id, bill_to_name AS party_name,
	  coalesce(due_date, issue_date) AS due_date, trim_scale(open_amount)::text AS amount
	FROM open WHERE open_amount > 0 AND coalesce(account_id, corporate_account_id, customer_id) IS NOT NULL`

// apOpenItems are the open supplier items of a property at a date.
const apOpenItems = `WITH open AS (SELECT i.id, i.number, i.supplier_id, i.supplier_name, i.due_date, i.amount - coalesce((SELECT sum((a->>'amount')::numeric)
	  FROM accounting.vendor_payments p CROSS JOIN LATERAL jsonb_array_elements(p.allocations) a
	  WHERE (a->>'apItemId')::uuid = i.id AND p.paid_date <= $2::date), 0) AS open_amount
	  FROM accounting.ap_items i WHERE i.property_id = $1 AND i.invoice_date <= $2::date AND i.status <> 'cancelled')
	SELECT id, number, supplier_id AS party_id, supplier_name AS party_name, due_date, trim_scale(open_amount)::text AS amount
	FROM open WHERE open_amount > 0`

type partyItem struct {
	ID        uuid.UUID `db:"id"`
	Number    string    `db:"number"`
	PartyID   uuid.UUID `db:"party_id"`
	PartyName string    `db:"party_name"`
	Due       time.Time `db:"due_date"`
	Amount    string    `db:"amount"`
}

// FollowUp is one entry of the follow-up log.
type FollowUp struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	PartyType       string     `json:"partyType" db:"party_type" enum:"customer,supplier"`
	PartyID         uuid.UUID  `json:"partyId" db:"party_id"`
	PartyName       string     `json:"partyName" db:"party_name"`
	InvoiceID       *uuid.UUID `json:"invoiceId" db:"invoice_id"`
	InvoiceNumber   *string    `json:"invoiceNumber" db:"invoice_number"`
	Action          string     `json:"action" db:"action" enum:"reminder,call,email,meeting,promise_to_pay,dispute,note"`
	Notes           *string    `json:"notes" db:"notes"`
	PromisedDate    *string    `json:"promisedDate" db:"promised_date"`
	PromisedAmount  *string    `json:"promisedAmount" db:"promised_amount"`
	NextFollowUp    *string    `json:"nextFollowUp" db:"next_follow_up"`
	ResponsibleID   *uuid.UUID `json:"responsibleId" db:"responsible_id"`
	ResponsibleName *string    `json:"responsibleName" db:"responsible_name"`
	CreatedAt       time.Time  `json:"createdAt" db:"created_at"`
	CreatedByName   *string    `json:"createdByName" db:"created_by_name"`
}

// FollowUpInput records a follow-up on a customer or supplier.
type FollowUpInput struct {
	PartyName      string     `json:"partyName" doc:"Name shown in the log (the bill-to or supplier name)"`
	Action         string     `json:"action" enum:"reminder,call,email,meeting,promise_to_pay,dispute,note"`
	Notes          string     `json:"notes,omitempty"`
	InvoiceID      *uuid.UUID `json:"invoiceId,omitempty" doc:"Invoice or supplier item the follow-up is about"`
	InvoiceNumber  string     `json:"invoiceNumber,omitempty"`
	PromisedDate   string     `json:"promisedDate,omitempty" doc:"promise_to_pay: date the customer promised"`
	PromisedAmount string     `json:"promisedAmount,omitempty"`
	NextFollowUp   string     `json:"nextFollowUp,omitempty" doc:"YYYY-MM-DD"`
	ResponsibleID  *uuid.UUID `json:"responsibleId,omitempty" doc:"Default: the current user"`
}

// FollowUpRow is a customer or supplier of the worklist.
type FollowUpRow struct {
	PartyID         uuid.UUID  `json:"partyId"`
	PartyName       string     `json:"partyName"`
	OpenItems       int        `json:"openItems"`
	Outstanding     string     `json:"outstanding"`
	Overdue         string     `json:"overdue"`
	DaysOverdue     int        `json:"daysOverdue" doc:"Days past due of the oldest overdue item (0 = nothing overdue)"`
	NextDue         *string    `json:"nextDue" doc:"Earliest due date of the items not yet due"`
	Status          string     `json:"status" enum:"overdue,due_soon,current"`
	LastAction      *string    `json:"lastAction"`
	LastActionAt    *time.Time `json:"lastActionAt"`
	LastReminderAt  *time.Time `json:"lastReminderAt"`
	NextFollowUp    *string    `json:"nextFollowUp"`
	FollowUpDue     bool       `json:"followUpDue" doc:"The next follow-up date is today or past"`
	ResponsibleID   *uuid.UUID `json:"responsibleId"`
	ResponsibleName *string    `json:"responsibleName"`
	PromisedDate    *string    `json:"promisedDate"`
	PromisedAmount  *string    `json:"promisedAmount"`
}

// FollowUpWorklist is the Collections or Vendor Follow-up worklist.
type FollowUpWorklist struct {
	AsOf        string        `json:"asOf"`
	PartyType   string        `json:"partyType" enum:"customer,supplier"`
	Rows        []FollowUpRow `json:"rows"`
	Outstanding string        `json:"outstanding"`
	Overdue     string        `json:"overdue"`
	DueToday    int           `json:"dueToday" doc:"Parties whose next follow-up is today or past"`
}

// FollowUpOpenItem is an open invoice or supplier item of a party.
type FollowUpOpenItem struct {
	ID          uuid.UUID `json:"id"`
	Number      string    `json:"number"`
	DueDate     string    `json:"dueDate"`
	Amount      string    `json:"amount"`
	DaysOverdue int       `json:"daysOverdue"`
	Status      string    `json:"status" enum:"overdue,due_today,due_soon,current"`
}

// FollowUpParty is a party with its open items and follow-up log.
type FollowUpParty struct {
	PartyType string             `json:"partyType" enum:"customer,supplier"`
	PartyID   uuid.UUID          `json:"partyId"`
	PartyName string             `json:"partyName"`
	OpenItems []FollowUpOpenItem `json:"openItems"`
	FollowUps []FollowUp         `json:"followUps"`
}

var followUpActions = []string{"reminder", "call", "email", "meeting", "promise_to_pay", "dispute", "note"}

// RegisterFollowUps adds the Collections and Vendor Follow-up routes.
func (m *Module) RegisterFollowUps(reg *route.Registry) {
	for _, k := range []struct{ party, path, tag, view, manage, summary string }{
		{"customer", "/collections", "Accounts Receivable", "accounting.receivable.view", "accounting.receivable.manage", "Collections"},
		{"supplier", "/vendor-follow-ups", "Accounts Payable", "accounting.payable.view", "accounting.payable.manage", "Vendor Follow-up"},
	} {
		party := k.party
		m.propertyRoute(reg, k.tag, route.Route{Method: http.MethodGet, Path: base + k.path,
			Summary:    k.summary + " worklist: parties with open items, overdue, last follow-up, next follow-up and the person responsible",
			Permission: k.view, Response: FollowUpWorklist{}, Query: []route.Param{{Name: "asOf"}},
			Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (FollowUpWorklist, error) {
				p := handle.Property(ctx)
				today := localToday(ctx, tx, p)
				d, err := handle.QueryDate(r, "asOf", today)
				if err != nil {
					return FollowUpWorklist{}, err
				}
				return Worklist(ctx, tx, p, party, d, today)
			})})
		m.propertyRoute(reg, k.tag, route.Route{Method: http.MethodGet, Path: base + k.path + "/{id}",
			Summary: k.summary + ": open items and follow-up log of a party", Permission: k.view, Response: FollowUpParty{},
			Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (FollowUpParty, error) {
				pid, err := handle.ID(r)
				if err != nil {
					return FollowUpParty{}, err
				}
				p := handle.Property(ctx)
				return Party(ctx, tx, p, party, pid, localToday(ctx, tx, p))
			})})
		m.propertyRoute(reg, k.tag, route.Route{Method: http.MethodPost, Path: base + k.path + "/{id}/follow-ups",
			Summary: k.summary + ": record a follow-up (reminder, call, promise to pay, dispute, note)", Permission: k.manage,
			Request: FollowUpInput{}, Response: FollowUp{}, Status: http.StatusCreated,
			Handler: handle.Write(m.DB, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in FollowUpInput) (FollowUp, error) {
				pid, err := handle.ID(r)
				if err != nil {
					return FollowUp{}, err
				}
				return RecordFollowUp(ctx, tx, handle.Property(ctx), party, pid, in)
			})})
	}
}

func openItems(ctx context.Context, q dbtx.Querier, property uuid.UUID, party string, asOf time.Time) ([]partyItem, error) {
	sql := arOpenItems
	if party == "supplier" {
		sql = apOpenItems
	}
	return handle.List[partyItem](q.Query(ctx, sql+` ORDER BY 5, 2`, property, ymd(asOf)))
}

const followUpCols = `f.id, f.party_type, f.party_id, f.party_name, f.invoice_id, f.invoice_number, f.action, f.notes, f.promised_date::text AS promised_date,
	trim_scale(f.promised_amount)::text AS promised_amount, f.next_follow_up::text AS next_follow_up, f.responsible_id, ru.full_name AS responsible_name,
	f.created_at, cu.full_name AS created_by_name
	FROM accounting.follow_ups f LEFT JOIN platform.users ru ON ru.id = f.responsible_id LEFT JOIN platform.users cu ON cu.id = f.created_by`

// Worklist lists the parties with open items, the most overdue first.
func Worklist(ctx context.Context, q dbtx.Querier, property uuid.UUID, party string, asOf, today time.Time) (FollowUpWorklist, error) {
	out := FollowUpWorklist{AsOf: ymd(asOf), PartyType: party, Rows: []FollowUpRow{}}
	items, err := openItems(ctx, q, property, party, asOf)
	if err != nil {
		return out, err
	}
	latest, err := handle.List[FollowUp](q.Query(ctx, `SELECT DISTINCT ON (f.party_id) `+followUpCols+`
		WHERE f.property_id = $1 AND f.party_type = $2 ORDER BY f.party_id, f.created_at DESC`, property, party))
	if err != nil {
		return out, err
	}
	type reminder struct {
		PartyID uuid.UUID `db:"party_id"`
		At      time.Time `db:"at"`
	}
	reminders, err := handle.List[reminder](q.Query(ctx, `SELECT party_id, max(created_at) AS at FROM accounting.follow_ups
		WHERE property_id = $1 AND party_type = $2 AND action = 'reminder' GROUP BY party_id`, property, party))
	if err != nil {
		return out, err
	}
	last := map[uuid.UUID]FollowUp{}
	for _, f := range latest {
		last[f.PartyID] = f
	}
	lastReminder := map[uuid.UUID]time.Time{}
	for _, r := range reminders {
		lastReminder[r.PartyID] = r.At
	}
	rows := map[uuid.UUID]*FollowUpRow{}
	sums := map[uuid.UUID][2]decimal.Decimal{}
	var order []uuid.UUID
	var total, overdue decimal.Decimal
	for _, it := range items {
		row, ok := rows[it.PartyID]
		if !ok {
			row = &FollowUpRow{PartyID: it.PartyID, PartyName: it.PartyName, Status: "current"}
			rows[it.PartyID] = row
			order = append(order, it.PartyID)
		}
		v := dec(it.Amount)
		s := sums[it.PartyID]
		s[0] = s[0].Add(v)
		total = total.Add(v)
		row.OpenItems++
		if it.Due.Before(asOf) {
			s[1] = s[1].Add(v)
			overdue = overdue.Add(v)
			if late := int(asOf.Sub(it.Due).Hours() / 24); late > row.DaysOverdue {
				row.DaysOverdue = late
			}
			row.Status = "overdue"
		} else {
			if row.NextDue == nil || ymd(it.Due) < *row.NextDue {
				d := ymd(it.Due)
				row.NextDue = &d
			}
			if row.Status == "current" && !it.Due.After(asOf.AddDate(0, 0, 7)) {
				row.Status = "due_soon"
			}
		}
		sums[it.PartyID] = s
	}
	for _, pid := range order {
		row := rows[pid]
		row.Outstanding, row.Overdue = sums[pid][0].String(), sums[pid][1].String()
		if f, ok := last[pid]; ok {
			row.LastAction, row.LastActionAt = &f.Action, &f.CreatedAt
			row.NextFollowUp, row.ResponsibleID, row.ResponsibleName = f.NextFollowUp, f.ResponsibleID, f.ResponsibleName
			row.PromisedDate, row.PromisedAmount = f.PromisedDate, f.PromisedAmount
			if f.NextFollowUp != nil && *f.NextFollowUp <= ymd(today) {
				row.FollowUpDue = true
				out.DueToday++
			}
		}
		if at, ok := lastReminder[pid]; ok {
			row.LastReminderAt = &at
		}
		out.Rows = append(out.Rows, *row)
	}
	sort.SliceStable(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if a.FollowUpDue != b.FollowUpDue {
			return a.FollowUpDue
		}
		if a.DaysOverdue != b.DaysOverdue {
			return a.DaysOverdue > b.DaysOverdue
		}
		return dec(a.Outstanding).GreaterThan(dec(b.Outstanding))
	})
	out.Outstanding, out.Overdue = total.String(), overdue.String()
	return out, nil
}

// Party returns the open items and the follow-up log of a party.
func Party(ctx context.Context, q dbtx.Querier, property uuid.UUID, party string, pid uuid.UUID, today time.Time) (FollowUpParty, error) {
	out := FollowUpParty{PartyType: party, PartyID: pid, OpenItems: []FollowUpOpenItem{}, FollowUps: []FollowUp{}}
	items, err := openItems(ctx, q, property, party, today)
	if err != nil {
		return out, err
	}
	for _, it := range items {
		if it.PartyID != pid {
			continue
		}
		out.PartyName = it.PartyName
		late := 0
		if it.Due.Before(today) {
			late = int(today.Sub(it.Due).Hours() / 24)
		}
		out.OpenItems = append(out.OpenItems, FollowUpOpenItem{ID: it.ID, Number: it.Number, DueDate: ymd(it.Due), Amount: it.Amount,
			DaysOverdue: late, Status: dueStatus(it.Due, today)})
	}
	if out.FollowUps, err = handle.List[FollowUp](q.Query(ctx, `SELECT `+followUpCols+`
		WHERE f.property_id = $1 AND f.party_type = $2 AND f.party_id = $3 ORDER BY f.created_at DESC LIMIT 200`, property, party, pid)); err != nil {
		return out, err
	}
	if out.PartyName == "" && len(out.FollowUps) > 0 {
		out.PartyName = out.FollowUps[0].PartyName
	}
	if out.PartyName == "" {
		return out, errs.NotFound("party")
	}
	return out, nil
}

// RecordFollowUp appends a follow-up to the log of a party.
func RecordFollowUp(ctx context.Context, tx pgx.Tx, property uuid.UUID, party string, pid uuid.UUID, in FollowUpInput) (FollowUp, error) {
	in.Action = strings.TrimSpace(in.Action)
	ok := false
	for _, a := range followUpActions {
		ok = ok || a == in.Action
	}
	if !ok {
		return FollowUp{}, handle.Invalid("action", "invalid", strings.Join(followUpActions, ", "))
	}
	if strings.TrimSpace(in.PartyName) == "" {
		return FollowUp{}, handle.Invalid("partyName", "required", "name of the customer or supplier")
	}
	date := func(field, v string) (any, error) {
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		t, err := parseDate(field, v)
		if err != nil {
			return nil, err
		}
		return t, nil
	}
	promised, err := date("promisedDate", in.PromisedDate)
	if err != nil {
		return FollowUp{}, err
	}
	next, err := date("nextFollowUp", in.NextFollowUp)
	if err != nil {
		return FollowUp{}, err
	}
	var amount any
	if strings.TrimSpace(in.PromisedAmount) != "" {
		a, err := handle.Decimal("promisedAmount", in.PromisedAmount, decimal.Zero)
		if err != nil {
			return FollowUp{}, err
		}
		amount = a.String()
	}
	if in.Action == "promise_to_pay" && promised == nil {
		return FollowUp{}, handle.Invalid("promisedDate", "required", "date the customer promised to pay")
	}
	responsible := in.ResponsibleID
	if responsible == nil {
		responsible = actorPtr(ctx)
	}
	fid := id.New()
	nullable := func(s string) any {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return strings.TrimSpace(s)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.follow_ups (id, property_id, party_type, party_id, party_name, invoice_id, invoice_number, action,
		notes, promised_date, promised_amount, next_follow_up, responsible_id, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12,$13,$14,$14)`, fid, property, party, pid, strings.TrimSpace(in.PartyName), in.InvoiceID,
		nullable(in.InvoiceNumber), in.Action, nullable(in.Notes), promised, amount, next, responsible, actorPtr(ctx)); err != nil {
		return FollowUp{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionCreate, EntityType: "accounting.follow_up", EntityID: fid.String(),
		EntityLabel: strings.TrimSpace(in.PartyName), PropertyID: &property, After: map[string]any{"partyType": party, "action": in.Action, "nextFollowUp": in.NextFollowUp}}); err != nil {
		return FollowUp{}, err
	}
	return getOne[FollowUp]("follow-up")(tx.Query(ctx, `SELECT `+followUpCols+` WHERE f.id = $1`, fid))
}
