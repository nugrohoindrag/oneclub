package sales

// PRD P3 §16 #18 (FR-QUO-05): acceptance on the public link with a one-time
// code (OTP) sent to the customer contact on file, an audit trail of every
// request, failure and lock, and the acceptance evidence on the quotation
// (IP, user agent, verified code channel and time). Quotations / contracts
// whose total is above the Sales Policies threshold (Rp5 jt) carry an
// e-Meterai, stamped through the integration layer on acceptance; staff
// retries a pending or failed stamp. Certified e-signatures (PSrE,
// FR-QUO-09 / FR-INT-P3-07) stay deferred.
//
// Security: codes are 6 digits from crypto/rand; only a salted, peppered
// HMAC-SHA256 is stored and compared in constant time; a code expires
// (otpTtlMinutes), locks after otpMaxAttempts wrong entries and is consumed
// by the acceptance; a new code needs a 60 s cooldown and at most 10 codes
// a day per quotation; requests and verifications are rate limited per IP
// and per link. The code never appears in a response, the audit trail or
// the integration call log.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/kernel/route"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/notify"
)

// EventQuotationOtp is the notification carrying an acceptance code.
const EventQuotationOtp = "crm.quotation_otp"

const (
	otpDigits         = 6
	otpResendCooldown = 60 * time.Second
	otpRequestsPerDay = 10
)

// In-memory limiters on top of publicLimiter (the durable limits — cooldown,
// codes per day, attempts per code — live in the database).
var (
	otpRequestIPLimiter   = &handle.Limiter{N: 30, Period: 10 * time.Minute}
	otpRequestLinkLimiter = &handle.Limiter{N: 10, Period: 10 * time.Minute}
	otpVerifyIPLimiter    = &handle.Limiter{N: 30, Period: 10 * time.Minute}
)

// ── types ─────────────────────────────────────────────────────────────────

// QuotationOtpRequestResult tells where the acceptance code was sent; it
// never contains the code.
type QuotationOtpRequestResult struct {
	Channel           string    `json:"channel" enum:"email,whatsapp"`
	DestinationMasked string    `json:"destinationMasked" doc:"Masked e-mail / phone the code was sent to"`
	ExpiresAt         time.Time `json:"expiresAt"`
	ResendAfter       time.Time `json:"resendAfter" doc:"A new code can be requested from this time"`
}

// QuotationAcceptanceEvidence is the evidence kept with an acceptance.
type QuotationAcceptanceEvidence struct {
	IP             *string    `json:"ip"`
	UserAgent      *string    `json:"userAgent"`
	OtpVerifiedAt  *time.Time `json:"otpVerifiedAt" doc:"Time the one-time code was verified (public link)"`
	OtpChannel     *string    `json:"otpChannel" enum:"email,whatsapp"`
	OtpDestination *string    `json:"otpDestination" doc:"Masked destination of the verified code"`
}

// QuotationEMeterai is the e-Meterai of a quotation (PRD P3 §16 #18).
type QuotationEMeterai struct {
	Required     bool       `json:"required" doc:"Total above the Sales Policies e-Meterai threshold"`
	Threshold    string     `json:"threshold"`
	Status       string     `json:"status" enum:"not_required,pending,stamped,failed"`
	SerialNumber *string    `json:"serialNumber"`
	StampedAt    *time.Time `json:"stampedAt"`
	Reference    *string    `json:"reference"`
	Provider     *string    `json:"provider" doc:"Integration that stamped"`
	Sandbox      bool       `json:"sandbox" doc:"Stamped by a sandbox adapter (mock / trial, no legal value)"`
	Error        *string    `json:"error" doc:"Last provider error (pending / failed)"`
}

// PublicQuotationEMeterai is the e-Meterai shown on the acceptance page.
type PublicQuotationEMeterai struct {
	Status       string     `json:"status" enum:"not_required,pending,stamped,failed"`
	SerialNumber *string    `json:"serialNumber"`
	StampedAt    *time.Time `json:"stampedAt"`
	Sandbox      bool       `json:"sandbox"`
}

// acceptanceOtp is a verified code waiting to be consumed by the acceptance.
type acceptanceOtp struct {
	id                   uuid.UUID
	channel, destination string
	verifiedAt           time.Time
}

func otpError(kind errs.Kind, code, msg string) error {
	return &errs.Error{Kind: kind, Code: code, Message: msg}
}

// ── policy helpers ────────────────────────────────────────────────────────

// eMeteraiRequired reports whether a total is above the threshold (an empty
// or invalid threshold disables e-Meterai).
func eMeteraiRequired(pol SalesPolicy, total string) bool {
	th, err := decimal.NewFromString(strings.TrimSpace(pol.EMeteraiThreshold))
	if err != nil || th.IsNegative() {
		return false
	}
	return dec(total).GreaterThan(th)
}

func otpTTL(pol SalesPolicy) time.Duration {
	return time.Duration(min(max(pol.OtpTTLMinutes, 1), 60)) * time.Minute
}

func otpMaxAttempts(pol SalesPolicy) int { return min(max(pol.OtpMaxAttempts, 1), 10) }

// ── detail ────────────────────────────────────────────────────────────────

// fillAcceptance adds the acceptance evidence and the e-Meterai to a detail.
func (m *Module) fillAcceptance(ctx context.Context, q dbtx.Querier, d *QuotationDetail) error {
	var property uuid.UUID
	var ev QuotationAcceptanceEvidence
	var e QuotationEMeterai
	if err := q.QueryRow(ctx, `SELECT property_id, accepted_ip, accepted_user_agent, otp_verified_at, otp_channel, otp_destination,
		e_meterai_status, e_meterai_serial, e_meterai_stamped_at, e_meterai_reference, e_meterai_provider, e_meterai_sandbox, e_meterai_error
		FROM crm.sales_quotations WHERE id = $1`, d.ID).Scan(&property, &ev.IP, &ev.UserAgent, &ev.OtpVerifiedAt, &ev.OtpChannel, &ev.OtpDestination,
		&e.Status, &e.SerialNumber, &e.StampedAt, &e.Reference, &e.Provider, &e.Sandbox, &e.Error); err != nil {
		return err
	}
	pol, _, err := LoadPolicy(ctx, q, property)
	if err != nil {
		return err
	}
	e.Threshold = pol.EMeteraiThreshold
	e.Required = e.Status != "not_required" || (d.Status != "accepted" && eMeteraiRequired(pol, d.Total))
	d.EMeterai, d.EMeteraiRequired, d.otpRequired = e, e.Required, pol.RequireAcceptanceOtp
	if d.Status == "accepted" {
		d.AcceptanceEvidence = &ev
	}
	return nil
}

// publicEMeterai is the e-Meterai of the acceptance page.
func publicEMeterai(d QuotationDetail) *PublicQuotationEMeterai {
	if !d.EMeterai.Required {
		return nil
	}
	return &PublicQuotationEMeterai{Status: d.EMeterai.Status, SerialNumber: d.EMeterai.SerialNumber, StampedAt: d.EMeterai.StampedAt,
		Sandbox: d.EMeterai.Sandbox}
}

// eMeteraiPDF prints the e-Meterai block of the quotation PDF: the notice
// before acceptance, the stamp (serial, date, trial marker) after.
func eMeteraiPDF(doc *pdf.Doc, d QuotationDetail) {
	e := d.EMeterai
	if !e.Required {
		return
	}
	doc.Space(6)
	doc.Rule(doc.Y + 10)
	switch {
	case e.Status == "stamped" && e.SerialNumber != nil:
		title := "e-Meterai"
		if e.Sandbox {
			title += " - MOCK / TRIAL (no legal value / tidak berlaku hukum)"
		}
		doc.Row(11, true, title)
		doc.Row(9, false, "Serial No. / No. Seri", *e.SerialNumber)
		if e.StampedAt != nil {
			doc.Row(9, false, "Stamped / Dibubuhkan", e.StampedAt.UTC().Format("2006-01-02 15:04")+" UTC")
		}
	case d.Status == "accepted":
		doc.Row(9, false, "e-Meterai: pending / menunggu pembubuhan")
	default:
		doc.Row(9, false, "e-Meterai will be applied on acceptance / e-Meterai akan dibubuhkan saat persetujuan")
	}
	doc.Rule(doc.Y + 10)
}

// ── codes ─────────────────────────────────────────────────────────────────

func randomDigits(n int) (string, error) {
	var b strings.Builder
	for range n {
		v, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		b.WriteByte(byte('0' + v.Int64()))
	}
	return b.String(), nil
}

// otpHash is hex(HMAC-SHA256(pepper, salt | quotation | code)).
func (m *Module) otpHash(salt string, qid uuid.UUID, code string) string {
	key := sha256.Sum256(append([]byte("oneclub/crm/quotation-otp\x00"), m.OTPKey...))
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(salt + "|" + qid.String() + "|" + code))
	return hex.EncodeToString(mac.Sum(nil))
}

// otpMatches compares a code with the stored hash in constant time.
func (m *Module) otpMatches(salt string, qid uuid.UUID, code, stored string) bool {
	want, err := hex.DecodeString(stored)
	if err != nil {
		return false
	}
	got, _ := hex.DecodeString(m.otpHash(salt, qid, code))
	return hmac.Equal(want, got)
}

// otpContact is where the code of a quotation goes: the customer, the lead
// and the corporate account PIC on file; WhatsApp when a phone is known and
// messaging is configured, else e-mail.
type otpContact struct {
	channel, name, email, phone, masked string
}

func (m *Module) otpContactOf(ctx context.Context, tx pgx.Tx, q Quotation) (otpContact, error) {
	var c otpContact
	add := func(n, e, p string) {
		if c.name == "" {
			c.name = strings.TrimSpace(n)
		}
		if c.email == "" {
			c.email = strings.TrimSpace(e)
		}
		if c.phone == "" && strings.TrimSpace(p) != "" {
			c.phone = crm.NormalizePhone(p)
		}
	}
	if q.CustomerID != nil {
		n, e, p, err := crm.Contact(ctx, tx, q.CustomerID, nil)
		if err != nil {
			return c, err
		}
		add(n, e, p)
	}
	if q.LeadID != nil {
		l, err := GetLead(ctx, tx, *q.LeadID)
		if err != nil {
			return c, err
		}
		add(l.Name, deref(l.Email), deref(l.Phone))
	}
	if q.CorporateAccountID != nil {
		var n, e, p string
		if err := tx.QueryRow(ctx, `SELECT coalesce(contact_name, name), coalesce(email, ''), coalesce(phone, '')
			FROM crm.corporate_accounts WHERE id = $1`, *q.CorporateAccountID).Scan(&n, &e, &p); err != nil && !dbtx.IsNoRows(err) {
			return c, err
		}
		add(n, e, p)
	}
	switch {
	case c.phone != "" && m.whatsAppConfigured(ctx):
		c.channel, c.masked = notify.ChannelWhatsApp, mask.Phone(c.phone)
	case c.email != "":
		c.channel, c.masked = notify.ChannelEmail, mask.Email(c.email)
	}
	return c, nil
}

func (m *Module) whatsAppConfigured(ctx context.Context) bool {
	if m.Integrations == nil {
		return false
	}
	_, err := m.Integrations.Messaging(ctx)
	return err == nil
}

// RequestAcceptanceOtp issues a new acceptance code for a sent quotation and
// sends it to the customer contact on file (earlier codes are superseded).
func (m *Module) RequestAcceptanceOtp(ctx context.Context, tx pgx.Tx, property uuid.UUID, q Quotation, ip, userAgent string) (QuotationOtpRequestResult, error) {
	var out QuotationOtpRequestResult
	if q.Status != "sent" {
		return out, decisionConflict(q)
	}
	valid, _ := time.Parse("2006-01-02", q.ValidUntil)
	if valid.Before(localToday(ctx, tx, property)) {
		return out, conflict("quotation_expired", "the quotation has expired")
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return out, err
	}
	now := clock.Now()
	var recent int
	var resend *time.Time
	if err := tx.QueryRow(ctx, `SELECT count(*), max(resend_after) FROM crm.sales_quotation_otps WHERE quotation_id = $1 AND created_at > $2`,
		q.ID, now.Add(-24*time.Hour)).Scan(&recent, &resend); err != nil {
		return out, err
	}
	if resend != nil && resend.After(now) {
		wait := int(resend.Sub(now).Seconds()) + 1
		return out, otpError(errs.KindRateLimited, "otp_resend_too_soon", "a new code can be requested in "+strconv.Itoa(wait)+" seconds")
	}
	if recent >= otpRequestsPerDay {
		return out, otpError(errs.KindRateLimited, "otp_request_limit", "too many codes were requested for this quotation today; contact your sales")
	}
	c, err := m.otpContactOf(ctx, tx, q)
	if err != nil {
		return out, err
	}
	if c.channel == "" {
		return out, conflict("no_contact", "no e-mail address or WhatsApp number is on file for this quotation; ask your sales to resend the link "+
			"or to record your acceptance")
	}
	if m.Notify == nil {
		return out, errs.Unavailable("notifications are not configured")
	}
	code, err := randomDigits(otpDigits)
	if err != nil {
		return out, err
	}
	salt := secret.RandomToken(16)
	ttl := otpTTL(pol)
	out = QuotationOtpRequestResult{Channel: c.channel, DestinationMasked: c.masked, ExpiresAt: now.Add(ttl), ResendAfter: now.Add(otpResendCooldown)}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotation_otps SET status = 'superseded' WHERE quotation_id = $1 AND status IN ('active', 'verified')`,
		q.ID); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_quotation_otps (id, property_id, quotation_id, code_hash, salt, channel, destination_masked,
		max_attempts, expires_at, resend_after, requested_ip, requested_user_agent, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		id.New(), property, q.ID, m.otpHash(salt, q.ID, code), salt, c.channel, c.masked, otpMaxAttempts(pol), out.ExpiresAt, out.ResendAfter,
		nullStr(ip), nullStr(userAgent), now); err != nil {
		return out, err
	}
	msg := notify.Message{Event: EventQuotationOtp, Category: "crm", Name: c.name, Mandatory: true, PropertyID: &property, Channels: []string{c.channel},
		Data: map[string]any{"name": c.name, "number": q.Number, "title": q.Title, "otpCode": code, "expiresInMinutes": int(ttl.Minutes())}}
	if c.channel == notify.ChannelWhatsApp {
		msg.Phone = c.phone
	} else {
		msg.Email = c.email
	}
	if err := m.Notify.Send(ctx, tx, msg); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "otp_requested", Category: audit.CategorySecurity, EntityType: "crm.quotation",
		EntityID: q.ID.String(), EntityLabel: q.Number + " v" + itoa(q.Version), PropertyID: &property, ActorName: "public link",
		Metadata: map[string]any{"ip": ip, "userAgent": userAgent, "channel": c.channel, "destination": c.masked,
			"expiresAt": out.ExpiresAt.Format(time.RFC3339)}})
}

// verifyAcceptanceOtp checks the code of a quotation in its own transaction,
// so that wrong entries, the lock and their audit entries are kept even
// though the acceptance is refused. A correct code becomes verified; the
// acceptance consumes it.
func (m *Module) verifyAcceptanceOtp(ctx context.Context, property uuid.UUID, q Quotation, code, ip, userAgent string) (*acceptanceOtp, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errs.Validation("otp_required", "enter the code sent to you", errs.Field("otpCode", "required", "enter the code sent to you"))
	}
	if !otpVerifyIPLimiter.Allow(ip) {
		return nil, errs.RateLimited()
	}
	var out *acceptanceOtp
	var outcome error
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var (
			oid                               uuid.UUID
			hash, salt, channel, dest, status string
			attempts, maxAttempts             int
			expires                           time.Time
			verifiedAt                        *time.Time
		)
		err := tx.QueryRow(ctx, `SELECT id, code_hash, salt, channel, destination_masked, status, attempts, max_attempts, expires_at, verified_at
			FROM crm.sales_quotation_otps WHERE quotation_id = $1 AND status IN ('active', 'verified', 'locked')
			ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, q.ID).Scan(&oid, &hash, &salt, &channel, &dest, &status, &attempts, &maxAttempts, &expires, &verifiedAt)
		if dbtx.IsNoRows(err) {
			outcome = errs.Validation("otp_not_requested", "request a code first", errs.Field("otpCode", "not_requested", "request a code first"))
			return nil
		}
		if err != nil {
			return err
		}
		now := clock.Now()
		switch {
		case status == "locked":
			outcome = otpError(errs.KindRateLimited, "otp_locked", "too many wrong codes; request a new code")
			return nil
		case !now.Before(expires):
			outcome = errs.Validation("otp_expired", "the code has expired; request a new code", errs.Field("otpCode", "expired", "the code has expired"))
			return nil
		}
		meta := map[string]any{"ip": ip, "userAgent": userAgent, "channel": channel, "destination": dest}
		entry := audit.Entry{Module: "crm", Category: audit.CategorySecurity, EntityType: "crm.quotation", EntityID: q.ID.String(),
			EntityLabel: q.Number + " v" + itoa(q.Version), PropertyID: &property, ActorName: "public link", Metadata: meta}
		if !m.otpMatches(salt, q.ID, code, hash) {
			attempts++
			meta["attempts"], meta["maxAttempts"] = attempts, maxAttempts
			if attempts >= maxAttempts {
				if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotation_otps SET attempts = $2, status = 'locked', locked_at = $3 WHERE id = $1`,
					oid, attempts, now); err != nil {
					return err
				}
				outcome = otpError(errs.KindRateLimited, "otp_locked", "too many wrong codes; request a new code")
				entry.Action = "otp_locked"
				return audit.Record(ctx, tx, entry)
			}
			if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotation_otps SET attempts = $2 WHERE id = $1`, oid, attempts); err != nil {
				return err
			}
			left := strconv.Itoa(maxAttempts - attempts)
			outcome = errs.Validation("otp_invalid", "the code is not correct ("+left+" attempts left)",
				errs.Field("otpCode", "invalid", "the code is not correct ("+left+" attempts left)"))
			entry.Action = "otp_failed"
			return audit.Record(ctx, tx, entry)
		}
		at := now
		if verifiedAt != nil {
			at = *verifiedAt
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotation_otps SET status = 'verified', verified_at = $2 WHERE id = $1`, oid, at); err != nil {
			return err
		}
		out = &acceptanceOtp{id: oid, channel: channel, destination: dest, verifiedAt: at}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, outcome
}

// recordAcceptance keeps the evidence of an acceptance on the quotation and
// consumes the verified code, in the transaction of the acceptance.
func (m *Module) recordAcceptance(ctx context.Context, tx pgx.Tx, q Quotation, meta decisionMeta, now time.Time) error {
	var verifiedAt *time.Time
	var channel, dest *string
	if o := meta.otp; o != nil {
		tag, err := tx.Exec(ctx, `UPDATE crm.sales_quotation_otps SET status = 'consumed', consumed_at = $3 WHERE id = $1 AND quotation_id = $2
			AND status = 'verified'`, o.id, q.ID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errs.Validation("otp_invalid", "the code was already used; request a new code",
				errs.Field("otpCode", "invalid", "the code was already used"))
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotation_otps SET status = 'superseded' WHERE quotation_id = $1 AND status IN ('active', 'verified')`,
			q.ID); err != nil {
			return err
		}
		verifiedAt, channel, dest = &o.verifiedAt, &o.channel, &o.destination
	}
	_, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET accepted_ip = $2, accepted_user_agent = $3, otp_verified_at = $4, otp_channel = $5,
		otp_destination = $6 WHERE id = $1`, q.ID, nullStr(meta.ip), nullStr(meta.userAgent), verifiedAt, channel, dest)
	return err
}

// otpAudit is the OTP part of the acceptance audit entry.
func (meta decisionMeta) otpAudit() map[string]any {
	out := map[string]any{"userAgent": meta.userAgent}
	if meta.otp != nil {
		out["otpChannel"], out["otpDestination"], out["otpVerifiedAt"] = meta.otp.channel, meta.otp.destination, meta.otp.verifiedAt
	}
	return out
}

// ── e-Meterai ─────────────────────────────────────────────────────────────

// eMeteraiStamp calls the e-Meterai integration for an accepted quotation
// (integration.ErrNotConfigured when none is enabled).
func (m *Module) eMeteraiStamp(ctx context.Context, tx pgx.Tx, d QuotationDetail) (integration.EMeteraiStamp, string, error) {
	if m.Integrations == nil {
		return integration.EMeteraiStamp{}, "", integration.ErrNotConfigured
	}
	svc, code, err := m.Integrations.EMeterai(ctx)
	if err != nil {
		return integration.EMeteraiStamp{}, "", err
	}
	doc, err := QuotationPDF(ctx, tx, d)
	if err != nil {
		return integration.EMeteraiStamp{}, code, err
	}
	s, err := svc.Stamp(ctx, integration.EMeteraiStampRequest{DocumentRef: d.ID.String(), DocumentType: "quotation",
		DocumentNumber: d.Number + " v" + itoa(d.Version), Amount: d.Total, Currency: d.Currency, Signer: deref(d.AcceptedByName), PDF: doc})
	return s, code, err
}

func setEMeterai(ctx context.Context, tx pgx.Tx, qid uuid.UUID, status string, s integration.EMeteraiStamp, provider, errMsg string) error {
	var stampedAt *time.Time
	if status == "stamped" {
		stampedAt = &s.StampedAt
	}
	_, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET e_meterai_status = $2, e_meterai_serial = $3, e_meterai_stamped_at = $4,
		e_meterai_reference = $5, e_meterai_provider = $6, e_meterai_sandbox = $7, e_meterai_error = $8 WHERE id = $1`,
		qid, status, nullStr(s.SerialNumber), stampedAt, nullStr(s.Reference), nullStr(provider), s.Sandbox, nullStr(errMsg))
	return err
}

func eMeteraiAudit(ctx context.Context, tx pgx.Tx, property uuid.UUID, d QuotationDetail, action, status string, s integration.EMeteraiStamp,
	provider, errMsg string) error {
	return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: action, EntityType: "crm.quotation", EntityID: d.ID.String(),
		EntityLabel: d.Number + " v" + itoa(d.Version), PropertyID: &property, Before: map[string]any{"eMeteraiStatus": d.EMeterai.Status},
		After: map[string]any{"eMeteraiStatus": status, "serialNumber": s.SerialNumber, "provider": provider, "sandbox": s.Sandbox, "error": errMsg}})
}

// eMeteraiOnAcceptance stamps an accepted quotation above the threshold.
// The acceptance never fails because of the stamp: without an enabled
// integration, with automatic stamping off or on a provider error the
// e-Meterai stays pending for staff to retry.
func (m *Module) eMeteraiOnAcceptance(ctx context.Context, tx pgx.Tx, property uuid.UUID, d QuotationDetail) (QuotationDetail, error) {
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return d, err
	}
	if !eMeteraiRequired(pol, d.Total) {
		return d, nil
	}
	status, action, provider, errMsg := "pending", "e_meterai_pending", "", "automatic stamping is off (Sales Policies)"
	var s integration.EMeteraiStamp
	if pol.EMeteraiOnAcceptance {
		s, provider, err = m.eMeteraiStamp(ctx, tx, d)
		switch {
		case err == nil:
			status, action, errMsg = "stamped", "e_meterai_stamped", ""
		case errors.Is(err, integration.ErrNotConfigured):
			errMsg = "no e-Meterai integration is enabled"
		default:
			errMsg = truncateMsg(err.Error())
		}
	}
	if err := setEMeterai(ctx, tx, d.ID, status, s, provider, errMsg); err != nil {
		return d, err
	}
	if err := eMeteraiAudit(ctx, tx, property, d, action, status, s, provider, errMsg); err != nil {
		return d, err
	}
	return m.QuotationDetailOf(ctx, tx, d.ID)
}

// StampEMeterai retries the e-Meterai of an accepted quotation whose stamp
// is pending or failed (staff). A provider error is kept as failed.
func (m *Module) StampEMeterai(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID) (QuotationDetail, error) {
	q, err := lockQuotation(ctx, tx, property, qid)
	if err != nil {
		return QuotationDetail{}, err
	}
	d, err := m.QuotationDetailOf(ctx, tx, q.ID)
	if err != nil {
		return d, err
	}
	if q.Status != "accepted" || (d.EMeterai.Status != "pending" && d.EMeterai.Status != "failed") {
		return d, conflict("e_meterai_not_pending", "only an accepted quotation with a pending or failed e-Meterai can be stamped")
	}
	s, provider, err := m.eMeteraiStamp(ctx, tx, d)
	status, action, errMsg := "stamped", "e_meterai_stamped", ""
	switch {
	case errors.Is(err, integration.ErrNotConfigured):
		return d, conflict("e_meterai_not_configured", "no e-Meterai integration is enabled (Settings → Integrations)")
	case err != nil:
		status, action, errMsg = "failed", "e_meterai_failed", truncateMsg(err.Error())
	}
	if err := setEMeterai(ctx, tx, q.ID, status, s, provider, errMsg); err != nil {
		return d, err
	}
	if err := eMeteraiAudit(ctx, tx, property, d, action, status, s, provider, errMsg); err != nil {
		return d, err
	}
	return m.QuotationDetailOf(ctx, tx, q.ID)
}

func truncateMsg(s string) string {
	if r := []rune(s); len(r) > 300 {
		return string(r[:300])
	}
	return s
}

// ── routes ────────────────────────────────────────────────────────────────

// registerAcceptance adds the acceptance code request (public) and the
// e-Meterai retry (staff).
func (m *Module) registerAcceptance(reg *route.Registry) {
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/public/quotations/{token}:request-otp", Module: "crm", Tag: "Public Website",
		Auth: route.AuthPublic, Summary: "Send a one-time acceptance code to the customer contact on file (WhatsApp or e-mail; the code is never returned)",
		Response: QuotationOtpRequestResult{}, Handler: publicLimiter.Wrap(m.publicRequestOtp)})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/quotations/{id}:stamp-e-meterai",
		Summary: "Stamp (retry) the e-Meterai of an accepted quotation above the threshold", Permission: "crm.quotation.stamp",
		Response: QuotationDetail{}, Status: http.StatusOK, Handler: action(m.DB, func(ctx context.Context, tx pgx.Tx, qid uuid.UUID, r *http.Request) (any, error) {
			return m.StampEMeterai(ctx, tx, handle.Property(ctx), qid)
		})})
}

func (m *Module) publicRequestOtp(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	ip := httpx.ClientIP(r)
	if !otpRequestIPLimiter.Allow(ip) || !otpRequestLinkLimiter.Allow(secret.HashToken(token)) {
		httpx.WriteError(w, r, errs.RateLimited())
		return
	}
	ctx := dbtx.System(r.Context())
	var out QuotationOtpRequestResult
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		q, property, err := m.byToken(ctx, tx, token, true)
		if err != nil {
			return err
		}
		out, err = m.RequestAcceptanceOtp(crm.PublicCtx(ctx, property), tx, property, q, ip, r.UserAgent())
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// publicAccept accepts a quotation on its link: name, terms and — when the
// Sales Policies require it — the one-time code, verified (and its failures
// kept) before the acceptance transaction.
func (m *Module) publicAccept(w http.ResponseWriter, r *http.Request) {
	ctx := dbtx.System(r.Context())
	token, ip := chi.URLParam(r, "token"), httpx.ClientIP(r)
	var out PublicQuotation
	err := func() error {
		var in PublicAcceptInput
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
		if err := handle.Required("name", in.Name); err != nil {
			return err
		}
		if !in.TermsAccepted {
			return handle.Invalid("termsAccepted", "required", "accept the terms & conditions")
		}
		var q Quotation
		var property uuid.UUID
		var required bool
		if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
			var err error
			if q, property, err = m.byToken(ctx, tx, token, false); err != nil {
				return err
			}
			pol, _, err := LoadPolicy(crm.PublicCtx(ctx, property), tx, property)
			required = pol.RequireAcceptanceOtp
			return err
		}); err != nil {
			return err
		}
		pctx := crm.PublicCtx(ctx, property)
		var otp *acceptanceOtp
		if q.Status == "sent" && (required || strings.TrimSpace(in.OtpCode) != "") {
			var err error
			if otp, err = m.verifyAcceptanceOtp(pctx, property, q, in.OtpCode, ip, r.UserAgent()); err != nil {
				return err
			}
		}
		return m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			q, property, err := m.byToken(ctx, tx, token, true)
			if err != nil {
				return err
			}
			pctx := crm.PublicCtx(ctx, property)
			if _, err := m.accept(pctx, tx, property, q, decisionMeta{via: "public_link", name: in.Name, ip: ip, userAgent: r.UserAgent(),
				note: in.Note, termsAccepted: true, otp: otp}); err != nil {
				return err
			}
			out, err = m.publicView(pctx, tx, q.ID)
			return err
		})
	}()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
