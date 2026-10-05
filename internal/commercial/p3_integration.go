package commercial

// PRD P3 EP-10 / EP-11 integrations: notification templates (FR-INT-P3-06),
// customer notifications of package bookings, the accounting export rows
// of package revenue allocation and promotion discounts (FR-INT-P3-05),
// the personal promo codes of CRM campaigns (crm.campaign_sent, FR-CMP-05)
// and the demo seed.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
)

// Notification events of promotions and packages.
const (
	NotifyPackageConfirmed = "commercial.package_booking_confirmed"
	NotifyPackageCancelled = "commercial.package_booking_cancelled"
	NotifyPackagePayment   = "commercial.package_payment_pending"
	NotifyPromoCode        = "commercial.promo_code_issued"
)

// P3Templates are the ID / EN templates of package bookings and personal
// promo codes (FR-INT-P3-06).
func P3Templates() []provision.Template {
	t := map[string]map[string][2]string{
		NotifyPackageConfirmed: {
			"en": {"Package {{.number}} confirmed — {{.package}}", "Dear {{.name}},\n\nYour package {{.package}} ({{.number}}) is confirmed from {{.startDate}} for {{.pax}} pax: {{.currency}} {{.total}}.\nIncluded: {{.components}}.\n{{.link}}"},
			"id": {"Paket {{.number}} terkonfirmasi — {{.package}}", "Yth. {{.name}},\n\nPaket {{.package}} ({{.number}}) Anda terkonfirmasi mulai {{.startDate}} untuk {{.pax}} pax: {{.currency}} {{.total}}.\nTermasuk: {{.components}}.\n{{.link}}"},
		},
		NotifyPackagePayment: {
			"en": {"Complete the payment of package {{.number}}", "Dear {{.name}},\n\nPackage {{.package}} ({{.number}}) from {{.startDate}} is held until {{.holdUntil}}. Pay {{.currency}} {{.amount}} to confirm it.\n{{.link}}"},
			"id": {"Selesaikan pembayaran paket {{.number}}", "Yth. {{.name}},\n\nPaket {{.package}} ({{.number}}) mulai {{.startDate}} ditahan sampai {{.holdUntil}}. Bayar {{.currency}} {{.amount}} untuk mengonfirmasi.\n{{.link}}"},
		},
		NotifyPackageCancelled: {
			"en": {"Package {{.number}} cancelled", "Dear {{.name}},\n\nPackage {{.package}} ({{.number}}) from {{.startDate}} is cancelled: {{.reason}}. Cancellation fee {{.currency}} {{.fee}}; any overpayment is refunded."},
			"id": {"Paket {{.number}} dibatalkan", "Yth. {{.name}},\n\nPaket {{.package}} ({{.number}}) mulai {{.startDate}} dibatalkan: {{.reason}}. Biaya pembatalan {{.currency}} {{.fee}}; kelebihan pembayaran dikembalikan."},
		},
		NotifyPromoCode: {
			"en": {"Your promo code {{.code}}", "Dear {{.name}},\n\nUse your personal promo code {{.code}} for {{.promotion}}{{if .expiresAt}} until {{.expiresAt}}{{end}}. It is also shown under Offers in the member app."},
			"id": {"Kode promo Anda {{.code}}", "Yth. {{.name}},\n\nGunakan kode promo pribadi {{.code}} untuk {{.promotion}}{{if .expiresAt}} sampai {{.expiresAt}}{{end}}. Kode juga tampil di menu Offers aplikasi member."},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			for _, ch := range []string{notify.ChannelEmail, notify.ChannelInApp, notify.ChannelWhatsApp} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	return out
}

// notifyCustomer sends a commercial notification to a customer (in-app,
// e-mail and WhatsApp for app users) or to the guest contact.
func (m *P3Module) notifyCustomer(ctx context.Context, tx pgx.Tx, property uuid.UUID, customer *uuid.UUID, guestName, guestEmail, guestPhone, event,
	link string, data map[string]any) error {
	if m.Notify == nil {
		return nil
	}
	msg := notify.Message{Event: event, Category: "general", Data: data, Link: link, PropertyID: &property, Name: guestName}
	if customer != nil {
		c, err := crm.GetCustomer(ctx, tx, *customer)
		if err == nil {
			if c.UserID != nil {
				msg.UserIDs = []uuid.UUID{*c.UserID}
				msg.Channels = []string{notify.ChannelInApp, notify.ChannelEmail, notify.ChannelWhatsApp}
				return m.Notify.Send(ctx, tx, msg)
			}
			_, email, phone, err := crm.Contact(ctx, tx, customer, nil)
			if err == nil {
				guestEmail, guestPhone = nonEmpty(guestEmail, email), nonEmpty(guestPhone, phone)
			}
			if msg.Name == "" {
				msg.Name = c.Name
			}
		}
	}
	if guestEmail != "" {
		msg.Email = guestEmail
		msg.Channels = append(msg.Channels, notify.ChannelEmail)
	}
	if guestPhone != "" {
		msg.Phone = guestPhone
		msg.Channels = append(msg.Channels, notify.ChannelWhatsApp)
	}
	if len(msg.Channels) == 0 {
		return nil
	}
	return m.Notify.Send(ctx, tx, msg)
}

// bookingLink is the page of a booking for its customer.
func (m *P3Module) bookingLink(b PackageBooking) string {
	if m.WebsiteURL == nil {
		return ""
	}
	return strings.TrimRight(m.WebsiteURL(), "/") + "/id/packages/" + strings.ToLower(b.PackageCode)
}

// notifyBooking tells the customer about a package booking.
func (m *P3Module) notifyBooking(ctx context.Context, tx pgx.Tx, b PackageBooking, event string, extra map[string]any) error {
	var property uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM commercial.package_bookings WHERE id = $1`, b.ID).Scan(&property); err != nil {
		return err
	}
	var comps []string
	for _, c := range b.Components {
		if c.Status != "cancelled" {
			comps = append(comps, c.Name)
		}
	}
	name := ""
	if b.GuestName != nil {
		name = *b.GuestName
	} else if b.CustomerName != nil {
		name = *b.CustomerName
	}
	data := map[string]any{"name": name, "number": b.Number, "package": b.PackageName, "startDate": b.StartDate, "pax": b.Pax, "total": b.Total,
		"currency": b.Currency, "components": strings.Join(comps, ", "), "link": m.bookingLink(b)}
	for k, v := range extra {
		data[k] = v
	}
	return m.notifyCustomer(ctx, tx, property, b.CustomerID, name, deref(b.GuestEmail), deref(b.GuestPhone), event, m.bookingLink(b), data)
}

// ── accounting export (FR-INT-P3-05) ──────────────────────────────────────

// ExportRows are the package and promotion rows of the accounting export
// of a business date: revenue allocation per revenue component of the
// packages confirmed that day (liabilities apart), the allocated revenue of
// the components consumed that day, and the promotion discounts redeemed
// (and reversed) that day per business line (K3).
func ExportRows(ctx context.Context, q dbtx.Querier, property uuid.UUID, day, from, to time.Time) ([]billing.ExportRow, error) {
	pl := places(currencyOf(ctx, q))
	var out []billing.ExportRow
	type row struct {
		Code   string
		Amount string
	}
	collect := func(section, label, sql string, args ...any) error {
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.Code, &r.Amount); err != nil {
				return err
			}
			out = append(out, billing.ExportRow{Section: section, Code: r.Code, Description: label + " " + r.Code, Amount: dec(r.Amount).StringFixed(pl)})
		}
		return rows.Err()
	}
	if err := collect("package_allocation", "Package revenue allocated to", `SELECT c.revenue_component || CASE WHEN c.liability THEN ' (liability)' ELSE '' END,
		sum(c.allocated_total)::text FROM commercial.package_booking_components c JOIN commercial.package_bookings b ON b.id = c.booking_id
		WHERE b.property_id = $1 AND b.confirmed_at >= $2 AND b.confirmed_at < $3 AND c.status <> 'cancelled'
		GROUP BY 1 ORDER BY 1`, property, from, to); err != nil {
		return nil, err
	}
	if err := collect("package_consumption", "Package components used:", `SELECT c.revenue_component, sum(pc.total)::text
		FROM commercial.package_consumptions pc JOIN commercial.package_booking_components c ON c.id = pc.booking_component_id
		WHERE pc.property_id = $1 AND pc.business_date = $2::date GROUP BY 1 ORDER BY 1`, property, day.Format("2006-01-02")); err != nil {
		return nil, err
	}
	if err := collect("promotion_discount", "Promotion discounts", `SELECT business_line, sum(discount_amount)::text FROM commercial.promotion_redemptions
		WHERE property_id = $1 AND status IN ('redeemed', 'reversed') AND redeemed_at >= $2 AND redeemed_at < $3 GROUP BY 1 ORDER BY 1`,
		property, from, to); err != nil {
		return nil, err
	}
	if err := collect("promotion_discount_reversed", "Promotion discounts reversed", `SELECT business_line, (-sum(discount_amount))::text
		FROM commercial.promotion_redemptions WHERE property_id = $1 AND status = 'reversed' AND redeemed_at IS NOT NULL AND reversed_at >= $2
		AND reversed_at < $3 GROUP BY 1 ORDER BY 1`, property, from, to); err != nil {
		return nil, err
	}
	return out, nil
}

// ── CRM campaigns: personal promo codes (FR-CMP-05, FR-APP-P3-02) ─────────

// CampaignSentPayload is the part of crm.campaign_sent commercial uses
// (docs/p3-p4-contracts.md).
type CampaignSentPayload struct {
	CampaignID  uuid.UUID  `json:"campaignId"`
	Code        string     `json:"code"`
	PromotionID *uuid.UUID `json:"promotionId"`
	PromoCode   string     `json:"promoCode"`
	PromoMode   string     `json:"promoMode"`
	ExpiresAt   *time.Time `json:"promoExpiresAt"`
	Recipients  []struct {
		CustomerID uuid.UUID `json:"customerId"`
		PromoCode  string    `json:"promoCode"`
	} `json:"recipients"`
}

// OnCampaignSent registers the unique promo codes of a campaign as personal
// codes (one use, the recipient only) of the promotion: promotionId, else
// the promotion of the campaign's shared promo code. A code CRM did not
// choose is generated. Idempotent per campaign and customer
// (crm.campaign_sent subscriber).
func (m *P3Module) OnCampaignSent(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p CampaignSentPayload
	if err := e.Decode(&p); err != nil || e.PropertyID == nil || len(p.Recipients) == 0 {
		return nil //nolint:nilerr // another shape or no personal codes
	}
	if p.PromoMode != "" && p.PromoMode != "unique" {
		return nil
	}
	property := *e.PropertyID
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	promotion := p.PromotionID
	if promotion == nil && p.PromoCode != "" {
		var pid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT promotion_id FROM commercial.promo_codes WHERE property_id = $1 AND code = upper($2)
			UNION SELECT id FROM commercial.promotions WHERE property_id = $1 AND code = upper($2) AND archived_at IS NULL LIMIT 1`, property, p.PromoCode).Scan(&pid)
		if err != nil && !dbtx.IsNoRows(err) {
			return err
		}
		if err == nil {
			promotion = &pid
		}
	}
	if promotion == nil {
		return nil
	}
	var pcode, pname string
	if err := tx.QueryRow(ctx, `SELECT code, name FROM commercial.promotions WHERE id = $1 AND property_id = $2`, *promotion, property).Scan(&pcode, &pname); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	loc := calendar.Location(ctx, tx)
	n := 0

	for _, r := range p.Recipients {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commercial.promo_codes WHERE property_id = $1 AND promotion_id = $2 AND customer_id = $3
			AND campaign_id = $4)`, property, *promotion, r.CustomerID, p.CampaignID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		code := strings.ToUpper(strings.TrimSpace(r.PromoCode))
		if !promoCodeRe.MatchString(code) {
			code = randomCode(strings.ToUpper(pcode)+"-", 6)
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		_, err = sp.Exec(ctx, `INSERT INTO commercial.promo_codes (id, property_id, promotion_id, code, customer_id, campaign_id, campaign_ref, max_uses,
			max_uses_per_customer, expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,1,1,$8)`, id.New(), property, *promotion, code, r.CustomerID, p.CampaignID,
			nullStr(p.Code), p.ExpiresAt)
		if err != nil {
			_ = sp.Rollback(ctx)
			if ok, _ := dbtx.IsUniqueViolation(err); ok {
				continue // the code exists already (resent event)
			}
			if dbtx.IsForeignKeyViolation(err) {
				continue // unknown customer
			}
			return err
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		n++
		exp := ""
		if p.ExpiresAt != nil {
			exp = p.ExpiresAt.In(loc).Format("02 Jan 2006")
		}
		cid := r.CustomerID
		if err := m.notifyCustomer(ctx, tx, property, &cid, "", "", "", NotifyPromoCode, "", map[string]any{"code": code, "promotion": pname,
			"expiresAt": exp}); err != nil {
			return err
		}
	}
	if n == 0 {
		return nil
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "generate_codes", EntityType: "commercial.promotion", EntityID: promotion.String(),
		EntityLabel: pcode, PropertyID: &property, After: map[string]any{"campaignId": p.CampaignID, "campaign": p.Code, "count": n, "personal": true},
		Metadata: map[string]any{"event": e.ID}})
}

// ── demo (idempotent) ─────────────────────────────────────────────────────

// SeedDemo seeds draft promotions (happy hour "Buy 2 Only 200K", member
// discount, a promo code) and a draft Golf & Lunch package with its
// components; nothing is active, so prices do not change until a manager
// activates them.
func SeedDemo(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.promotions (id, property_id, code, name, description, promo_type, buy_quantity, bundle_price, channels,
		business_lines, time_windows, priority, status) VALUES ($1,$2,'HH-BUY2','Happy Hour Buy 2 Only 200K','Two drinks for Rp200.000 on weekday afternoons and weekend lunch',
		'buy_n_price_x',2,200000,'{pos,member_app,website}','{pos}','[{"days":[1,2,3,4,5],"start":"16:00","end":"19:00"},{"days":[6,7],"start":"14:00","end":"17:00"}]'::jsonb,
		10,'draft') ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.promotions (id, property_id, code, name, promo_type, discount_percent, segments, priority, status)
		VALUES ($1,$2,'MEMBER10','Member 10% F&B','member_discount',10,'{member}',50,'draft') ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property); err != nil {
		return err
	}
	pid := id.New()
	tag, err := tx.Exec(ctx, `INSERT INTO commercial.promotions (id, property_id, code, name, promo_type, discount_percent, requires_code, min_purchase, priority, status)
		VALUES ($1,$2,'WELCOME15','Welcome 15%','promo_code',15,true,500000,60,'draft') ON CONFLICT (property_id, code) DO NOTHING`, pid, property)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO commercial.promo_codes (id, property_id, promotion_id, code, max_uses_per_customer) VALUES ($1,$2,$3,'WELCOME15',1)
			ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, pid); err != nil {
			return err
		}
	}
	pkg := id.New()
	tag, err = tx.Exec(ctx, `INSERT INTO commercial.packages (id, property_id, code, name, package_type, description, pricing_mode, price, min_pax, start_time,
		allocation_method, status) VALUES ($1,$2,'GOLF-LUNCH','Golf & Lunch','golf_lunch','18 holes with caddy and lunch at the clubhouse','per_pax',1250000,1,
		'07:00','standalone','draft') ON CONFLICT (property_id, code) DO NOTHING`, pkg, property)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	for i, c := range []struct {
		typ, name, line, rev string
		price                int
		liability            bool
	}{{"tee_time", "Green Fee 18 Holes", "golf", "green_fee", 900000, false}, {"service", "Caddy Fee", "golf", "caddy_fee", 200000, true},
		{"fnb", "Lunch", "pos", "fnb", 250000, false}} {
		if _, err := tx.Exec(ctx, `INSERT INTO commercial.package_components (id, property_id, package_id, seq, component_type, name, quantity, per_pax,
			standalone_price, revenue_component, business_line, liability) VALUES ($1,$2,$3,$4,$5,$6,1,true,$7,$8,$9,$10)`,
			id.New(), property, pkg, i+1, c.typ, c.name, c.price, c.rev, c.line, c.liability); err != nil {
			return err
		}
	}
	return nil
}
