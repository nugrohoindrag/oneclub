package stay

// Accommodation notifications (requirements §27) and the daily job.
// Guests: reservation created / modified / cancelled, no-show, check-in and
// check-out reminders and confirmations, late check-out, request updates
// and waitlist offers — in-app for portal users, else e-mail. Staff: new
// housekeeping tasks, work orders, guest requests and waitlist offers to
// the holders of the permission (in-app). The daily job sends the
// reminders, raises the preventive work orders that are due, plans the
// stayover cleaning and expires the waitlist.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/provision"
)

// stayData are the template variables of a stay.
func (m *Module) stayData(ctx context.Context, q dbtx.Querier, s Stay) map[string]any {
	loc := calendar.Location(ctx, q)
	name := deref(s.CustomerName)
	if name == "" {
		name = deref(s.GuestName)
	}
	pol, _, _ := m.policy(ctx, q, s.PropertyID)
	return map[string]any{"name": name, "stayNo": s.StayNo, "code": s.ReservationCode, "unit": s.UnitName, "type": deref(s.TypeName),
		"arrival": s.Start.In(loc).Format("Mon 02 Jan 2006"), "departure": s.End.In(loc).Format("Mon 02 Jan 2006"), "nights": s.Nights,
		"checkInTime": pol.CheckInTime, "checkOutTime": pol.CheckOutTime, "total": s.TotalDue,
		"lateUntil": func() string {
			if s.LateCheckOutUntil == nil {
				return ""
			}
			return s.LateCheckOutUntil.In(loc).Format("15:04")
		}()}
}

// notifyGuest notifies the guest of a bungalow stay.
func (m *Module) notifyGuest(ctx context.Context, tx pgx.Tx, s Stay, event string) error {
	return m.notifyGuestData(ctx, tx, s, event, nil)
}

func (m *Module) notifyGuestData(ctx context.Context, tx pgx.Tx, s Stay, event string, extra map[string]any) error {
	if m.Notify == nil || s.Kind != "bungalow" {
		return nil
	}
	data := m.stayData(ctx, tx, s)
	for k, v := range extra {
		data[k] = v
	}
	return m.notifyContact(ctx, tx, s.PropertyID, s.CustomerID, deref(s.GuestEmail), deref(s.GuestName), event, data)
}

// notifyContact addresses a customer (portal user or e-mail) or a guest
// e-mail without a customer record.
func (m *Module) notifyContact(ctx context.Context, tx pgx.Tx, property uuid.UUID, customer *uuid.UUID, email, name, event string, data map[string]any) error {
	if m.Notify == nil {
		return nil
	}
	msg := notify.Message{Event: event, Category: "booking", PropertyID: &property, Link: "/activity/stays", Data: data}
	ok := false
	if customer != nil {
		var err error
		if ok, err = crm.Recipient(ctx, tx, *customer, &msg); err != nil {
			return err
		}
	}
	if !ok {
		if email == "" {
			return nil
		}
		msg.Email, msg.Name, msg.Channels = email, name, []string{notify.ChannelEmail}
	}
	return m.Notify.Send(ctx, tx, msg)
}

// notifyStaff sends an in-app alert to the holders of a permission.
func (m *Module) notifyStaff(ctx context.Context, tx pgx.Tx, property uuid.UUID, permission, event string, data map[string]any, link string) error {
	if m.Notify == nil {
		return nil
	}
	users, err := notify.Holders(ctx, tx, property, permission)
	if err != nil || len(users) == 0 {
		return err
	}
	return m.Notify.Send(ctx, tx, notify.Message{Event: event, Category: "general", UserIDs: users, PropertyID: &property, Link: link, Data: data,
		Channels: []string{notify.ChannelInApp}})
}

// Templates are the accommodation notification templates.
func Templates() []provision.Template {
	guest := map[string]map[string][2]string{
		"stay.reservation_created": {
			"en": {"Reservation {{.stayNo}}: {{.type}}", "Hello {{.name}},\n\nThank you for your reservation {{.stayNo}} ({{.code}}): {{.type}}, {{.arrival}} – {{.departure}} ({{.nights}} night(s)). Check-in from {{.checkInTime}}, check-out until {{.checkOutTime}}.\nTotal: {{.total}}."},
			"id": {"Reservasi {{.stayNo}}: {{.type}}", "Halo {{.name}},\n\nTerima kasih atas reservasi {{.stayNo}} ({{.code}}): {{.type}}, {{.arrival}} – {{.departure}} ({{.nights}} malam). Check-in mulai {{.checkInTime}}, check-out sampai {{.checkOutTime}}.\nTotal: {{.total}}."}},
		"stay.reservation_modified": {
			"en": {"Reservation {{.stayNo}} changed", "Hello {{.name}},\n\nYour reservation {{.stayNo}} is now {{.type}} ({{.unit}}), {{.arrival}} – {{.departure}} ({{.nights}} night(s)). Total: {{.total}}."},
			"id": {"Reservasi {{.stayNo}} diubah", "Halo {{.name}},\n\nReservasi {{.stayNo}} Anda sekarang {{.type}} ({{.unit}}), {{.arrival}} – {{.departure}} ({{.nights}} malam). Total: {{.total}}."}},
		"stay.reservation_cancelled": {
			"en": {"Reservation {{.stayNo}} cancelled", "Hello {{.name}},\n\nYour reservation {{.stayNo}} ({{.arrival}} – {{.departure}}) is cancelled. Any refund follows the cancellation policy of your rate."},
			"id": {"Reservasi {{.stayNo}} dibatalkan", "Halo {{.name}},\n\nReservasi {{.stayNo}} ({{.arrival}} – {{.departure}}) telah dibatalkan. Pengembalian dana mengikuti kebijakan pembatalan tarif Anda."}},
		"stay.no_show": {
			"en": {"Reservation {{.stayNo}}: no-show", "Hello {{.name}},\n\nWe did not see you on {{.arrival}}; reservation {{.stayNo}} is marked as no-show and the bungalow released."},
			"id": {"Reservasi {{.stayNo}}: tidak hadir", "Halo {{.name}},\n\nKami tidak menerima kedatangan Anda pada {{.arrival}}; reservasi {{.stayNo}} dicatat tidak hadir dan bungalow dilepas."}},
		"stay.check_in_reminder": {
			"en": {"See you tomorrow: {{.type}}", "Hello {{.name}},\n\nWe look forward to welcoming you tomorrow, {{.arrival}}. Check-in from {{.checkInTime}} (reservation {{.stayNo}}). Please bring your ID."},
			"id": {"Sampai jumpa besok: {{.type}}", "Halo {{.name}},\n\nKami menantikan kedatangan Anda besok, {{.arrival}}. Check-in mulai {{.checkInTime}} (reservasi {{.stayNo}}). Mohon membawa kartu identitas."}},
		"stay.checked_in": {
			"en": {"Welcome to {{.unit}}", "Hello {{.name}},\n\nYou are checked in to {{.unit}} until {{.departure}}, check-out {{.checkOutTime}}. Open My Stay in the app for guest services and your folio."},
			"id": {"Selamat datang di {{.unit}}", "Halo {{.name}},\n\nAnda telah check-in di {{.unit}} sampai {{.departure}}, check-out {{.checkOutTime}}. Buka My Stay di aplikasi untuk layanan tamu dan folio."}},
		"stay.check_out_reminder": {
			"en": {"Check-out today {{.checkOutTime}}", "Hello {{.name}},\n\nA reminder that check-out of {{.unit}} is today at {{.checkOutTime}}. Ask the front office for a late check-out."},
			"id": {"Check-out hari ini {{.checkOutTime}}", "Halo {{.name}},\n\nPengingat: check-out {{.unit}} hari ini pukul {{.checkOutTime}}. Hubungi front office untuk late check-out."}},
		"stay.late_checkout": {
			"en": {"Late check-out until {{.lateUntil}}", "Hello {{.name}},\n\nYour late check-out of {{.unit}} until {{.lateUntil}} is confirmed. A late check-out fee may apply."},
			"id": {"Late check-out sampai {{.lateUntil}}", "Halo {{.name}},\n\nLate check-out {{.unit}} sampai {{.lateUntil}} telah dikonfirmasi. Biaya late check-out dapat berlaku."}},
		"stay.checked_out": {
			"en": {"Thank you for staying with us", "Hello {{.name}},\n\nYou are checked out of {{.unit}}. Thank you for staying with us — we hope to see you again."},
			"id": {"Terima kasih telah menginap", "Halo {{.name}},\n\nAnda telah check-out dari {{.unit}}. Terima kasih telah menginap — sampai jumpa kembali."}},
		"stay.guest_request_update": {
			"en": {"Request {{.requestNo}}: {{.status}}", "Hello {{.name}},\n\nYour request {{.requestNo}} ({{.request}}) is {{.status}}."},
			"id": {"Permintaan {{.requestNo}}: {{.status}}", "Halo {{.name}},\n\nPermintaan {{.requestNo}} ({{.request}}) Anda: {{.status}}."}},
		"stay.waitlist_available": {
			"en": {"A {{.type}} is available", "Hello {{.name}},\n\nGood news: a {{.type}} is now available from {{.arrival}} for {{.nights}} night(s) (waitlist {{.waitlistNo}}). Contact us to confirm your reservation."},
			"id": {"{{.type}} tersedia", "Halo {{.name}},\n\nKabar baik: {{.type}} kini tersedia mulai {{.arrival}} untuk {{.nights}} malam (waitlist {{.waitlistNo}}). Hubungi kami untuk konfirmasi reservasi."}},
	}
	staff := []string{"stay.ops_housekeeping", "stay.ops_work_order", "stay.ops_guest_request", "stay.ops_waitlist", "stay.ops_reservation"}
	var out []provision.Template
	for ev, locs := range guest {
		for loc, c := range locs {
			for _, ch := range []string{"email", "in_app", "whatsapp"} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	for _, ev := range staff {
		for _, loc := range []string{"en", "id"} {
			out = append(out, provision.Template{Event: ev, Channel: "in_app", Locale: loc, Subject: "{{.title}}", Body: "{{.body}}"})
		}
	}
	return out
}

// ── daily job ─────────────────────────────────────────────────────────────

// DailyArgs runs the accommodation day: reminders, preventive maintenance,
// stayover cleaning and the waitlist.
type DailyArgs struct{}

func (DailyArgs) Kind() string { return "stay_accommodation_daily" }
func (DailyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// DailyWorker runs Daily.
type DailyWorker struct {
	river.WorkerDefaults[DailyArgs]
	M *Module
}

func (w *DailyWorker) Work(ctx context.Context, _ *river.Job[DailyArgs]) error {
	_, err := w.M.Daily(ctx)
	return err
}

// ExpireArgs releases the bungalows whose online payment time ran out.
type ExpireArgs struct{}

func (ExpireArgs) Kind() string { return "stay_expire_holds" }
func (ExpireArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// ExpireWorker runs ExpireHolds.
type ExpireWorker struct {
	river.WorkerDefaults[ExpireArgs]
	M *Module
}

func (w *ExpireWorker) Work(ctx context.Context, _ *river.Job[ExpireArgs]) error {
	_, err := w.M.ExpireHolds(ctx)
	return err
}

// RegisterJobs adds the accommodation job (07:00 every day) and the expiry
// of the unpaid holds (every minute, docs/requirement-booking-hotel-mgcc.md FR-H36).
func (m *Module) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &DailyWorker{M: m})
	river.AddWorker(reg.Workers, &ExpireWorker{M: m})
	reg.Periodic = append(reg.Periodic,
		river.NewPeriodicJob(jobs.DailyAt{Hour: 7, Minute: 0, Location: loc}, func() (river.JobArgs, *river.InsertOpts) { return DailyArgs{}, nil }, nil),
		river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) { return ExpireArgs{}, nil }, nil))
}

// DailyResult reports a run of the daily job.
type DailyResult struct {
	CheckInReminders  int `json:"checkInReminders"`
	CheckOutReminders int `json:"checkOutReminders"`
	WorkOrders        int `json:"workOrders"`
	StayoverTasks     int `json:"stayoverTasks"`
}

// Daily runs the accommodation day for every property.
func (m *Module) Daily(ctx context.Context) (DailyResult, error) {
	ctx = dbtx.System(ctx)
	var out DailyResult
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		loc := calendar.Location(ctx, tx)
		now := clock.Now().In(loc)
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		if err := expireWaitlist(ctx, tx, today.Format(time.DateOnly)); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT DISTINCT property_id FROM stay.bungalows WHERE archived_at IS NULL`)
		if err != nil {
			return err
		}
		props, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		for _, p := range props {
			pctx := reqctx.WithProperty(ctx, p)
			r, err := m.dailyFor(pctx, tx, p, today)
			if err != nil {
				return fmt.Errorf("property %s: %w", p, err)
			}
			out.CheckInReminders += r.CheckInReminders
			out.CheckOutReminders += r.CheckOutReminders
			out.WorkOrders += r.WorkOrders
			out.StayoverTasks += r.StayoverTasks
		}
		return nil
	})
	return out, err
}

// DailyFor runs the accommodation day of one property (also by hand).
func (m *Module) dailyFor(ctx context.Context, tx pgx.Tx, property uuid.UUID, today time.Time) (DailyResult, error) {
	var out DailyResult
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return out, err
	}
	tomorrow := today.AddDate(0, 0, 1)
	remind := func(sql, event, column string) (int, error) {
		rows, err := tx.Query(ctx, sql, property, today, tomorrow)
		if err != nil {
			return 0, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return 0, err
		}
		for _, sid := range ids {
			s, err := m.Get(ctx, tx, sid)
			if err != nil {
				return 0, err
			}
			if err := m.notifyGuest(ctx, tx, s, event); err != nil {
				return 0, err
			}
			if _, err := tx.Exec(ctx, `UPDATE stay.stays SET `+column+` = now() WHERE id = $1`, sid); err != nil {
				return 0, err
			}
		}
		return len(ids), nil
	}
	if out.CheckInReminders, err = remind(`SELECT id FROM stay.stays WHERE property_id = $1 AND kind = 'bungalow' AND status = 'reserved'
		AND start_at >= $3 AND start_at < $3 + interval '1 day' AND checkin_reminded_at IS NULL AND $2::timestamptz IS NOT NULL`, "stay.check_in_reminder",
		"checkin_reminded_at"); err != nil {
		return out, err
	}
	if out.CheckOutReminders, err = remind(`SELECT id FROM stay.stays WHERE property_id = $1 AND kind = 'bungalow' AND status = 'checked_in'
		AND end_at >= $2 AND end_at < $3 AND checkout_reminded_at IS NULL`, "stay.check_out_reminder", "checkout_reminded_at"); err != nil {
		return out, err
	}
	wos, err := m.GeneratePreventive(ctx, tx, property, today)
	if err != nil {
		return out, err
	}
	out.WorkOrders = len(wos)
	if pol.StayoverCleaning {
		tasks, err := m.PlanDay(ctx, tx, property, today)
		if err != nil {
			return out, err
		}
		out.StayoverTasks = len(tasks)
	}
	return out, nil
}
