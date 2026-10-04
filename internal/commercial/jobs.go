package commercial

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/provision"
)

// VoucherExpiryArgs expires vouchers and sends expiry reminders.
type VoucherExpiryArgs struct{}

func (VoucherExpiryArgs) Kind() string { return "commercial_voucher_expiry" }

func (VoucherExpiryArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 5}
}

// VoucherExpiryWorker runs hourly: expiry + breakage, then H-30/H-7 reminders.
type VoucherExpiryWorker struct {
	river.WorkerDefaults[VoucherExpiryArgs]
	M *Module
}

func (w *VoucherExpiryWorker) Work(ctx context.Context, _ *river.Job[VoucherExpiryArgs]) error {
	if _, err := w.M.RunVoucherExpiry(ctx); err != nil {
		return err
	}
	_, err := w.M.SendExpiryReminders(ctx)
	return err
}

// RunVoucherExpiry expires due vouchers (exported for tests and ops).
func (m *Module) RunVoucherExpiry(ctx context.Context) (int, error) {
	var n int
	err := m.DB.WithTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		var err error
		n, err = m.ExpireDue(dbtx.System(ctx), tx)
		return err
	})
	return n, err
}

// SendExpiryReminders notifies customers 30 and 7 days before expiry.
func (m *Module) SendExpiryReminders(ctx context.Context) (int, error) {
	sys := dbtx.System(ctx)
	sent := 0
	err := m.DB.WithTx(sys, func(tx pgx.Tx) error {
		for _, days := range []int{30, 7} {
			rows, err := tx.Query(sys, `INSERT INTO commercial.voucher_reminders (voucher_id, property_id, days_before)
				SELECT v.id, v.property_id, $1 FROM commercial.vouchers v
				WHERE v.status IN ('active', 'partially_redeemed') AND v.customer_id IS NOT NULL
				AND v.expires_at > now() AND v.expires_at <= now() + make_interval(days => $1)
				ON CONFLICT DO NOTHING RETURNING voucher_id, property_id`, days)
			if err != nil {
				return err
			}
			type rem struct{ vid, pid uuid.UUID }
			var list []rem
			for rows.Next() {
				var r rem
				if err := rows.Scan(&r.vid, &r.pid); err != nil {
					rows.Close()
					return err
				}
				list = append(list, r)
			}
			rows.Close()
			for _, r := range list {
				v, err := m.Voucher(sys, tx, r.vid)
				if err != nil {
					return err
				}
				var email, locale *string
				var userID *uuid.UUID
				if err := tx.QueryRow(sys, `SELECT email, user_id, locale FROM crm.customers WHERE id = $1`, *v.CustomerID).Scan(&email, &userID, &locale); err != nil {
					return err
				}
				msg := notify.Message{Event: "voucher.expiring", Category: "voucher", PropertyID: &r.pid,
					Data: map[string]any{"name": deref(v.CustomerName), "code": v.Code, "typeName": v.TypeName, "remaining": v.RemainingQuantity,
						"unit": v.Unit, "expiresAt": v.ExpiresAt.Format("2006-01-02"), "days": days}}
				switch {
				case userID != nil:
					msg.UserIDs = []uuid.UUID{*userID}
				case email != nil && *email != "":
					msg.Email, msg.Locale, msg.Channels = *email, deref(locale), []string{notify.ChannelEmail}
				default:
					continue
				}
				if err := m.Notify.Send(sys, tx, msg); err != nil {
					return err
				}
				sent++
			}
		}
		return nil
	})
	return sent, err
}

// RegisterJobs adds commercial workers and schedules.
func (m *Module) RegisterJobs(reg *jobs.Registrar) {
	river.AddWorker(reg.Workers, &VoucherExpiryWorker{M: m})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return VoucherExpiryArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}))
}

// Templates are the commercial notification templates (FR-INT-P2-04).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"voucher.expiring": {
			"en": {"Your voucher expires in {{.days}} days", "Hello {{.name}},\n\nYour {{.typeName}} voucher {{.code}} has {{.remaining}} {{.unit}} left and expires on {{.expiresAt}}."},
			"id": {"Voucher Anda berakhir dalam {{.days}} hari", "Halo {{.name}},\n\nVoucher {{.typeName}} {{.code}} masih tersisa {{.remaining}} {{.unit}} dan berlaku sampai {{.expiresAt}}."},
		},
		"commercial.order_ready": {
			"en": {"Your order {{.orderNo}} is ready", "Hello {{.name}},\n\nYour order {{.orderNo}} is ready{{if .destination}} for {{.destination}}{{end}}."},
			"id": {"Pesanan {{.orderNo}} siap", "Halo {{.name}},\n\nPesanan {{.orderNo}} sudah siap{{if .destination}} untuk {{.destination}}{{end}}."},
		},
		"commercial.receipt": {
			"en": {"Receipt {{.orderNo}}", "Thank you for your visit to {{.outlet}}.\n\n{{.lines}}\nTotal: {{.total}}\nPaid: {{.payments}}"},
			"id": {"Struk {{.orderNo}}", "Terima kasih atas kunjungan Anda di {{.outlet}}.\n\n{{.lines}}\nTotal: {{.total}}\nPembayaran: {{.payments}}"},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			for _, ch := range []string{"email", "in_app"} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	return out
}
