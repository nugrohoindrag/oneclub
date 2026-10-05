package banquet

// Customer 360 and Corporate 360 sections of Banquet / Event (PRD P3
// FR-C360-01, FR-C360-04): the events a customer or a company books with
// their status, contract value, money received and outstanding, plus the
// open events a customer registered for. internal/app wires them as
// crm Sections["banquet"] and engagement CorporateSections["banquet"].

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
)

// BanquetC360Event is one booked event of a Customer / Corporate 360.
type BanquetC360Event struct {
	ID            uuid.UUID `json:"id"`
	Number        string    `json:"number"`
	Title         string    `json:"title"`
	Category      string    `json:"category"`
	EventType     string    `json:"eventType"`
	Status        string    `json:"status"`
	Start         time.Time `json:"start"`
	Pax           int       `json:"pax"`
	Currency      string    `json:"currency"`
	ContractTotal string    `json:"contractTotal"`
	Received      string    `json:"received" doc:"Payments and deposits received"`
	Outstanding   string    `json:"outstanding" doc:"Contract (or charges, when higher) − received; 0 for cancelled events"`
	Quotation     *string   `json:"quotationNumber"`
	CompanyName   *string   `json:"companyName"`
	ContactName   *string   `json:"contactName"`
}

// BanquetC360Section is the Banquet / Event section of a 360 view.
type BanquetC360Section struct {
	Events        []BanquetC360Event `json:"events" doc:"Latest booked events (up to 20)"`
	Upcoming      int                `json:"upcoming" doc:"Tentative or definite events still to come"`
	Completed     int                `json:"completed"`
	ContractTotal string             `json:"contractTotal" doc:"Contract value of the listed events that are not cancelled"`
	Outstanding   string             `json:"outstanding" doc:"Outstanding of the listed events"`
	Registrations int                `json:"registrations" doc:"Open events the customer registered for (tickets)"`
}

// CustomerSection is the Banquet / Event section of the Customer 360.
func (m *Module) CustomerSection(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	out, err := m.c360(ctx, q, property, `e.customer_id = $2`, customer)
	if err != nil {
		return out, err
	}
	err = q.QueryRow(ctx, `SELECT count(*)::int FROM banquet.participants p JOIN banquet.events e ON e.id = p.event_id
		WHERE e.property_id = $1 AND p.customer_id = $2 AND p.status <> 'withdrawn'`, property, customer).Scan(&out.Registrations)
	return out, err
}

// CorporateSection is the Banquet / Event section of the Corporate 360.
func (m *Module) CorporateSection(ctx context.Context, q dbtx.Querier, property, corporate uuid.UUID) (any, error) {
	return m.c360(ctx, q, property, `e.corporate_account_id = $2`, corporate)
}

func (m *Module) c360(ctx context.Context, q dbtx.Querier, property uuid.UUID, where string, ref uuid.UUID) (BanquetC360Section, error) {
	out := BanquetC360Section{Events: []BanquetC360Event{}, ContractTotal: "0", Outstanding: "0"}
	list, err := handle.List[BanquetEvent](q.Query(ctx, eventSelect+` WHERE e.property_id = $1 AND `+where+` ORDER BY e.start_at DESC LIMIT 20`, property, ref))
	if err != nil {
		return out, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE e.status IN ('tentative', 'definite') AND e.end_at >= now())::int,
		count(*) FILTER (WHERE e.status = 'completed')::int FROM banquet.events e WHERE e.property_id = $1 AND `+where, property, ref).
		Scan(&out.Upcoming, &out.Completed); err != nil {
		return out, err
	}
	contract, outstanding := decimal.Zero, decimal.Zero
	for _, e := range list {
		_, received, err := m.depositStatus(ctx, q, e)
		if err != nil {
			return out, err
		}
		owed := dec(e.ContractTotal)
		if e.FolioID != nil {
			sum, err := billing.FolioSummary(ctx, q, *e.FolioID)
			if err != nil {
				return out, err
			}
			owed = decimal.Max(owed, dec(sum.Charges))
		}
		left := decimal.Max(owed.Sub(received), decimal.Zero)
		if e.Status == "cancelled" {
			left = decimal.Zero
		} else {
			contract = contract.Add(dec(e.ContractTotal))
			outstanding = outstanding.Add(left)
		}
		pax := e.ExpectedPax
		if e.FinalPax != nil {
			pax = *e.FinalPax
		} else if e.GuaranteedPax != nil {
			pax = *e.GuaranteedPax
		}
		out.Events = append(out.Events, BanquetC360Event{ID: e.ID, Number: e.Number, Title: e.Title, Category: e.Category, EventType: e.EventTypeName,
			Status: e.Status, Start: e.Start, Pax: pax, Currency: e.Currency, ContractTotal: e.ContractTotal, Received: received.String(),
			Outstanding: left.String(), Quotation: e.QuotationNumber, CompanyName: e.CorporateName, ContactName: e.ContactName})
	}
	out.ContractTotal, out.Outstanding = contract.String(), outstanding.String()
	return out, nil
}
