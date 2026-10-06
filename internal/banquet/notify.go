package banquet

// Notifications of Banquet & Event: event confirmed (customer), hold
// expired and waitlist promoted (sales owner, EP-15 AC), BEO issued /
// revised (departments, FR-BEO-03), checklist overdue (PIC, FR-EVT-07),
// guaranteed pax due (sales owner), registration confirmed / promoted from
// the waitlist (guest).

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/provision"
)

func eventData(ctx context.Context, tx pgx.Tx, e BanquetEvent, extra map[string]any) map[string]any {
	loc := calendar.Location(ctx, tx)
	d := map[string]any{"number": e.Number, "title": e.Title, "date": e.Start.In(loc).Format("02 Jan 2006 15:04"), "status": e.Status}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

// notifyOwner tells the sales owner of an event.
func (m *Module) notifyOwner(ctx context.Context, tx pgx.Tx, e BanquetEvent, event string, data map[string]any) error {
	if m.Notify == nil || e.SalesOwnerID == nil {
		return nil
	}
	return m.Notify.Send(ctx, tx, notify.Message{Event: event, Category: "general", UserIDs: []uuid.UUID{*e.SalesOwnerID}, PropertyID: &e.PropertyID,
		Link: "/banquet-event/events/" + e.ID.String(), Data: eventData(ctx, tx, e, data)})
}

// notifyCustomer tells the customer (portal user, e-mail) of an event.
func (m *Module) notifyCustomer(ctx context.Context, tx pgx.Tx, e BanquetEvent, event string, data map[string]any) error {
	if m.Notify == nil {
		return nil
	}
	msg := notify.Message{Event: event, Category: "general", PropertyID: &e.PropertyID, Data: eventData(ctx, tx, e, data)}
	ok := false
	if e.CustomerID != nil {
		var err error
		if ok, err = crm.Recipient(ctx, tx, *e.CustomerID, &msg); err != nil {
			return err
		}
	}
	if !ok && e.ContactEmail != nil && *e.ContactEmail != "" {
		msg.Email, msg.Name, msg.Channels, ok = *e.ContactEmail, deref(e.ContactName), []string{notify.ChannelEmail}, true
	}
	if !ok {
		return nil
	}
	return m.Notify.Send(ctx, tx, msg)
}

// notifyParticipant sends the registration (or promotion) with the ticket.
func (m *Module) notifyParticipant(ctx context.Context, tx pgx.Tx, e BanquetEvent, p EventParticipant, event string) error {
	if m.Notify == nil {
		return nil
	}
	rank := 0
	if p.WaitlistRank != nil {
		rank = *p.WaitlistRank
	}
	msg := notify.Message{Event: event, Category: "general", PropertyID: &e.PropertyID, Link: "/events/my-events",
		Data: eventData(ctx, tx, e, map[string]any{"name": p.Name, "code": p.TicketCode, "registration": p.Status, "waitlistRank": rank, "seats": p.PartySize})}
	ok := false
	if p.CustomerID != nil {
		var err error
		if ok, err = crm.Recipient(ctx, tx, *p.CustomerID, &msg); err != nil {
			return err
		}
	}
	if !ok && p.Email != nil && *p.Email != "" {
		msg.Email, msg.Name, msg.Channels, ok = *p.Email, p.Name, []string{notify.ChannelEmail}, true
	}
	if !ok {
		return nil
	}
	return m.Notify.Send(ctx, tx, msg)
}

// Templates are the notification templates of the module (EN / ID).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"banquet.event_confirmed": {
			"en": {"Your event is confirmed: {{.title}}", "Hello {{.recipientName}},\n\nYour event {{.title}} ({{.number}}) on {{.date}} is now Definite. Our banquet team will contact you for the technical meeting."},
			"id": {"Acara Anda terkonfirmasi: {{.title}}", "Halo {{.recipientName}},\n\nAcara {{.title}} ({{.number}}) pada {{.date}} sudah Definite. Tim banquet kami akan menghubungi Anda untuk technical meeting."},
		},
		"banquet.hold_expired": {
			"en": {"Tentative hold expired: {{.number}}", "The option date of {{.title}} ({{.number}}) passed; the venue hold on {{.venues}} was released and the event is back to Inquiry."},
			"id": {"Hold tentative kedaluwarsa: {{.number}}", "Option date {{.title}} ({{.number}}) terlewati; hold venue {{.venues}} dilepas dan acara kembali menjadi Inquiry."},
		},
		"banquet.waitlist_promoted": {
			"en": {"Venue available: {{.venue}}", "The waitlisted hold of {{.title}} ({{.number}}) on {{.venue}} for {{.date}} is now Tentative. Confirm the down payment before the option date."},
			"id": {"Venue tersedia: {{.venue}}", "Hold waitlist {{.title}} ({{.number}}) di {{.venue}} untuk {{.date}} kini Tentative. Pastikan DP dibayar sebelum option date."},
		},
		"banquet.beo_distributed": {
			"en": {"BEO {{.number}} v{{.version}} {{.action}}", "Banquet Event Order {{.number}} version {{.version}} for {{.event}} on {{.date}} was {{.action}} ({{.changes}} change(s)). Please read and confirm for your department."},
			"id": {"BEO {{.number}} v{{.version}} {{.action}}", "Banquet Event Order {{.number}} versi {{.version}} untuk {{.event}} pada {{.date}} telah {{.action}} ({{.changes}} perubahan). Mohon baca dan konfirmasi untuk departemen Anda."},
		},
		"banquet.checklist_overdue": {
			"en": {"Checklist task overdue: {{.task}}", "The task \"{{.task}}\" of {{.title}} ({{.number}}, {{.date}}) was due on {{.dueDate}}."},
			"id": {"Tugas checklist terlambat: {{.task}}", "Tugas \"{{.task}}\" untuk {{.title}} ({{.number}}, {{.date}}) jatuh tempo pada {{.dueDate}}."},
		},
		"banquet.guaranteed_pax_due": {
			"en": {"Final pax due: {{.number}}", "The guaranteed (final) pax of {{.title}} ({{.number}}, {{.date}}) is due on {{.deadline}}."},
			"id": {"Final pax jatuh tempo: {{.number}}", "Jumlah pax final (guaranteed) {{.title}} ({{.number}}, {{.date}}) jatuh tempo pada {{.deadline}}."},
		},
		"banquet.registration_confirmed": {
			"en": {"Registration {{.registration}}: {{.title}}", "Hello {{.name}},\n\nYour registration for {{.title}} on {{.date}} is {{.registration}} ({{.seats}} seat(s)). Ticket code: {{.code}}{{if .waitlistRank}} — waitlist #{{.waitlistRank}}{{end}}."},
			"id": {"Registrasi {{.registration}}: {{.title}}", "Halo {{.name}},\n\nRegistrasi Anda untuk {{.title}} pada {{.date}} berstatus {{.registration}} ({{.seats}} kursi). Kode tiket: {{.code}}{{if .waitlistRank}} — waitlist #{{.waitlistRank}}{{end}}."},
		},
		"banquet.payment_due": {
			"en": {"{{if .overdue}}Overdue{{else}}Due {{.tag}}{{end}}: {{.label}} · {{.number}}", "{{.label}} of {{.title}} ({{.number}}, {{.date}}) of {{.amount}} is {{if .overdue}}overdue since{{else}}due on{{end}} {{.dueDate}}. Please follow up with the customer."},
			"id": {"{{if .overdue}}Terlambat{{else}}Jatuh tempo {{.tag}}{{end}}: {{.label}} · {{.number}}", "{{.label}} untuk {{.title}} ({{.number}}, {{.date}}) sebesar {{.amount}} {{if .overdue}}terlambat sejak{{else}}jatuh tempo pada{{end}} {{.dueDate}}. Mohon tindak lanjuti dengan pelanggan."},
		},
		"banquet.registration_promoted": {
			"en": {"You are in: {{.title}}", "Hello {{.name}},\n\nA seat became available: your registration for {{.title}} on {{.date}} is now confirmed. Ticket code: {{.code}}."},
			"id": {"Anda terdaftar: {{.title}}", "Halo {{.name}},\n\nKursi tersedia: registrasi Anda untuk {{.title}} pada {{.date}} kini terkonfirmasi. Kode tiket: {{.code}}."},
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
