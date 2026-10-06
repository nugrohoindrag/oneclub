package sales

// Catalogue contribution of CRM Sales (permissions per object, role
// templates of Product Overview §44: Sales Executive, Banquet Sales, CRM
// Admin, Marketing Staff, General Manager, Finance Manager), notification
// templates (ID/EN, FR-INT-P3-06), the quotation PDF and the commission rows
// of the accounting export (FR-INT-P3-05).

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
)

// Permissions of CRM Sales.
func Permissions() []catalog.Permission {
	var out []catalog.Permission
	for _, g := range [][]catalog.Permission{
		catalog.P("crm", "lead", "view", "create", "update", "assign", "convert", "import", "capture"),
		catalog.P("crm", "follow_up", "view", "manage"),
		catalog.P("crm", "opportunity", "view", "create", "update", "close"),
		catalog.P("crm", "quotation", "view", "create", "update", "send", "accept", "free_item", "stamp"),
		catalog.P("crm", "pipeline", catalog.CRUD...),
		catalog.P("crm", "sales_team", catalog.CRUD...),
		catalog.P("crm", "sales_target", catalog.CRUD...),
		catalog.P("crm", "commission_scheme", catalog.CRUD...),
		catalog.P("crm", "commission", "view", "manage", "export"),
	} {
		out = append(out, g...)
	}
	return out
}

// Contribution adds the CRM Sales permissions and grants them to roles.
func Contribution() catalog.Contribution {
	sell := []string{"crm.lead.view", "crm.lead.create", "crm.lead.update", "crm.lead.convert", "crm.follow_up.view", "crm.follow_up.manage",
		"crm.opportunity.view", "crm.opportunity.create", "crm.opportunity.update", "crm.opportunity.close", "crm.quotation.view", "crm.quotation.create",
		"crm.quotation.update", "crm.quotation.send", "crm.quotation.accept", "crm.pipeline.view", "crm.sales_target.view", "crm.sales_team.view",
		"crm.customer.view", "crm.corporate_account.view"}
	manage := append(append([]string{}, sell...), "crm.quotation.free_item", "crm.quotation.stamp", "crm.lead.assign", "crm.lead.import", "crm.lead.capture", "crm.pipeline.create", "crm.pipeline.update",
		"crm.pipeline.delete", "crm.pipeline.export", "crm.pipeline.import", "crm.sales_team.create", "crm.sales_team.update", "crm.sales_team.delete",
		"crm.sales_team.export", "crm.sales_team.import", "crm.sales_target.create", "crm.sales_target.update", "crm.sales_target.delete",
		"crm.sales_target.export", "crm.sales_target.import", "crm.commission_scheme.view", "crm.commission_scheme.create", "crm.commission_scheme.update",
		"crm.commission_scheme.delete", "crm.commission_scheme.export", "crm.commission_scheme.import", "crm.commission.view", "crm.commission.manage",
		"crm.commission.export")
	finance := []string{"crm.commission.view", "crm.commission.manage", "crm.commission.export", "crm.commission_scheme.view", "crm.sales_target.view",
		"crm.quotation.view", "crm.opportunity.view", "crm.lead.view"}
	return catalog.Contribution{Permissions: Permissions(), RolePermissions: map[string][]string{
		"sales_executive":  sell,
		"banquet_sales":    sell,
		"banquet_manager":  manage,
		"event_manager":    {"crm.lead.view", "crm.opportunity.view", "crm.quotation.view"},
		"crm_admin":        manage,
		"marketing_staff":  {"crm.lead.view", "crm.lead.create", "crm.lead.update", "crm.lead.import", "crm.lead.capture", "crm.follow_up.view"},
		"general_manager":  manage,
		"property_admin":   manage,
		"club_manager":     {"crm.lead.view", "crm.opportunity.view", "crm.quotation.view", "crm.sales_target.view", "crm.commission.view"},
		"finance_manager":  finance,
		"accountant":       {"crm.commission.view", "crm.commission.export"},
		"membership_admin": {"crm.lead.view", "crm.lead.create", "crm.follow_up.view"},
	}}
}

// Templates are the notification templates of CRM Sales (ID/EN).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"crm.sales_quotation": {
			"en": {"Quotation {{.number}} — {{.title}}", "Dear {{.name}},\n\n{{if .message}}{{.message}}\n\n{{end}}Please find our quotation {{.number}} (version {{.version}}) for {{.title}}: {{.currency}} {{.total}}, valid until {{.validUntil}}.\nView and accept online: {{.link}}"},
			"id": {"Penawaran {{.number}} — {{.title}}", "Yth. {{.name}},\n\n{{if .message}}{{.message}}\n\n{{end}}Berikut penawaran kami {{.number}} (versi {{.version}}) untuk {{.title}}: {{.currency}} {{.total}}, berlaku sampai {{.validUntil}}.\nLihat dan setujui secara online: {{.link}}"},
		},
		EventQuotationOtp: {
			"en": {"Verification code for quotation {{.number}}", "{{.otpCode}} is your code to accept quotation {{.number}}. It is valid for {{.expiresInMinutes}} minutes. Never share this code, not even with our staff."},
			"id": {"Kode verifikasi penawaran {{.number}}", "{{.otpCode}} adalah kode Anda untuk menyetujui penawaran {{.number}}. Berlaku {{.expiresInMinutes}} menit. Jangan bagikan kode ini kepada siapa pun, termasuk staf kami."},
		},
		"crm.sales_lead_assigned": {
			"en": {"New lead {{.number}}: {{.name}}", "Lead {{.number}} ({{.name}}, {{.line}}, source {{.source}}) is assigned to you. First response due {{.dueAt}}."},
			"id": {"Lead baru {{.number}}: {{.name}}", "Lead {{.number}} ({{.name}}, {{.line}}, sumber {{.source}}) ditugaskan kepada Anda. Respons pertama paling lambat {{.dueAt}}."},
		},
		"crm.sales_lead_sla_reminder": {
			"en": {"Respond to lead {{.number}}", "Lead {{.number}} ({{.name}}) still waits for a first response, due {{.dueAt}}."},
			"id": {"Respon lead {{.number}}", "Lead {{.number}} ({{.name}}) belum direspons, batas waktu {{.dueAt}}."},
		},
		"crm.sales_lead_sla_escalated": {
			"en": {"Lead {{.number}} missed its first-response SLA", "Lead {{.number}} ({{.name}}, {{.line}}) owned by {{.owner}} had no response by {{.dueAt}}."},
			"id": {"Lead {{.number}} melewati SLA respons pertama", "Lead {{.number}} ({{.name}}, {{.line}}) milik {{.owner}} belum direspons sampai {{.dueAt}}."},
		},
		"crm.sales_follow_up_due": {
			"en": {"Follow-up due: {{.subject}}", "{{.type}} with {{.contact}} is due {{.dueAt}}."},
			"id": {"Follow-up jatuh tempo: {{.subject}}", "{{.type}} dengan {{.contact}} jatuh tempo {{.dueAt}}."},
		},
		"crm.sales_quotation_discount_decided": {
			"en": {"Discount of quotation {{.number}} {{.decision}}", "The discount of {{.discount}}% on quotation {{.number}} was {{.decision}}."},
			"id": {"Diskon penawaran {{.number}} {{.decision}}", "Diskon {{.discount}}% pada penawaran {{.number}} telah {{.decision}}."},
		},
		"crm.sales_quotation_decided": {
			"en": {"Quotation {{.number}} {{.decision}}", "{{.name}} {{.decision}} quotation {{.number}} ({{.title}}, {{.total}})."},
			"id": {"Penawaran {{.number}} {{.decision}}", "{{.name}} {{.decision}} penawaran {{.number}} ({{.title}}, {{.total}})."},
		},
		"crm.sales_commission_earned": {
			"en": {"Commission earned on {{.number}}", "You earned {{.currency}} {{.amount}} commission on {{.number}} (period {{.period}})."},
			"id": {"Komisi dari {{.number}}", "Anda mendapat komisi {{.currency}} {{.amount}} dari {{.number}} (periode {{.period}})."},
		},
		"crm.sales_commission_approved": {
			"en": {"Commission statement {{.number}} approved", "Your commission statement {{.number}} for {{.period}} ({{.currency}} {{.total}}) is approved for payroll."},
			"id": {"Statement komisi {{.number}} disetujui", "Statement komisi {{.number}} periode {{.period}} ({{.currency}} {{.total}}) disetujui untuk payroll."},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			for _, ch := range []string{"email", "in_app", "whatsapp"} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	return out
}

// ExportRow is one line of the accounting export.
type ExportRow struct {
	Section, Code, Description, Amount string
}

// CommissionExport lists the commission earned, clawed back and approved in
// a day for the accounting export until Accounting P4 books it
// (FR-INT-P3-05).
func CommissionExport(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) ([]ExportRow, error) {
	var earned, clawback, adjusted string
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(amount) FILTER (WHERE kind = 'earned'), 0)::text,
		coalesce(sum(amount) FILTER (WHERE kind = 'clawback'), 0)::text, coalesce(sum(amount) FILTER (WHERE kind = 'adjustment'), 0)::text
		FROM crm.sales_commissions WHERE property_id = $1 AND created_at >= $2 AND created_at < $3`, property, from, to).
		Scan(&earned, &clawback, &adjusted); err != nil {
		return nil, err
	}
	return []ExportRow{
		{Section: "commission", Code: "earned", Description: "Sales commission earned", Amount: earned},
		{Section: "commission", Code: "clawback", Description: "Sales commission clawed back", Amount: clawback},
		{Section: "commission", Code: "adjustment", Description: "Sales commission adjustments", Amount: adjusted},
	}, nil
}
