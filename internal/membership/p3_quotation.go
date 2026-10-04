package membership

// PRD P3 FR-QUO-06/07: an accepted quotation of the membership line becomes
// a draft Membership Application for the customer, exactly once per
// quotation. The quotation is decoded from crm.quotation_accepted by name
// (docs/p3-p4-contracts.md); membership never imports CRM Sales. The
// membership package is the quotation's package reference or the item
// reference of a package / service line (package code or id). Billing then
// follows the P1 application flow (fee folio at approval, activation on
// payment), so the conversion issues no second payment schedule.

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/outbox"
)

// QuotationAccepted is the CRM Sales event converted here.
const QuotationAccepted = "crm.quotation_accepted"

type acceptedQuotation struct {
	QuotationID        uuid.UUID  `json:"quotationId"`
	Number             string     `json:"number"`
	Version            int        `json:"version"`
	PropertyID         uuid.UUID  `json:"propertyId"`
	Line               string     `json:"line"`
	CustomerID         *uuid.UUID `json:"customerId"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId"`
	PackageRef         *string    `json:"packageRef"`
	Title              string     `json:"title"`
	Lines              []struct {
		ItemType string  `json:"itemType"`
		ItemRef  *string `json:"itemRef"`
	} `json:"lines"`
}

// OnQuotationAccepted creates the draft application of an accepted
// membership quotation (idempotent per quotation).
func (m *Module) OnQuotationAccepted(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p acceptedQuotation
	if err := e.Decode(&p); err != nil {
		return nil //nolint:nilerr // foreign payload
	}
	if p.Line != "membership" || p.CustomerID == nil || p.QuotationID == uuid.Nil {
		return nil
	}
	property := p.PropertyID
	if property == uuid.Nil && e.PropertyID != nil {
		property = *e.PropertyID
	}
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM membership.applications WHERE quotation_id = $1)`, p.QuotationID).Scan(&exists); err != nil || exists {
		return err
	}
	refs := []string{}
	if p.PackageRef != nil && strings.TrimSpace(*p.PackageRef) != "" {
		refs = append(refs, strings.TrimSpace(*p.PackageRef))
	}
	for _, l := range p.Lines {
		if (l.ItemType == "package" || l.ItemType == "service") && l.ItemRef != nil && strings.TrimSpace(*l.ItemRef) != "" {
			refs = append(refs, strings.TrimSpace(*l.ItemRef))
		}
	}
	var pkg, typ uuid.UUID
	found := false
	for _, ref := range refs {
		err := tx.QueryRow(ctx, `SELECT id, type_id FROM membership.packages WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
			AND (upper(code) = upper($2) OR id::text = $2) LIMIT 1`, property, ref).Scan(&pkg, &typ)
		if err == nil {
			found = true
			break
		}
		if !dbtx.IsNoRows(err) {
			return err
		}
	}
	notConverted := func(reason string) error {
		// The sales opens the application by hand.
		return audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "quotation_not_converted", EntityType: "crm.quotation",
			EntityID: p.QuotationID.String(), EntityLabel: p.Number, PropertyID: &property, ActorName: "event:" + QuotationAccepted, Reason: reason})
	}
	if !found {
		return notConverted("no active membership package matches the quotation")
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	a, err := m.NewApplication(ctx, sp, property, "back_office", ApplicationRequest{CustomerID: *p.CustomerID, TypeID: typ, PackageID: pkg,
		CorporateAccountID: p.CorporateAccountID, Notes: "From quotation " + p.Number + " · " + p.Title})
	if err != nil {
		_ = sp.Rollback(ctx)
		if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
			return notConverted(de.Message)
		}
		return err
	}
	if err := sp.Commit(ctx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE membership.applications SET quotation_id = $2, quotation_number = $3 WHERE id = $1`, a.ID, p.QuotationID,
		p.Number); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "membership", Action: "converted", EntityType: "membership.application", EntityID: a.ID.String(),
		EntityLabel: a.Number, PropertyID: &property, ActorName: "event:" + QuotationAccepted,
		Metadata: map[string]any{"quotationId": p.QuotationID, "quotationNumber": p.Number, "version": p.Version}})
}
