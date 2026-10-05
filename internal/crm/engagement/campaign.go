package engagement

// Campaigns (FR-CMP-01..08): a message to a segment on e-mail, WhatsApp or
// in-app, scheduled, approved above a recipient count, sent in throttled
// batches to the customers who opted in on the channel (suppression list
// and frequency cap applied), with a unique promo code per recipient, a
// tracked link and an unsubscribe link, and tracked to delivery, read,
// click and conversion.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
)

// Notification templates of campaign and reminder messages: the subject and
// body come from the campaign / reminder rule.
const (
	CampaignTemplate = "crm.campaign_message"
	ReminderTemplate = "crm.reminder_message"
)

// CampaignSendDocumentType is the approval of large campaigns (FR-CMP-08).
var CampaignSendDocumentType = provision.DocumentType{Code: "campaign_send", Module: "crm", Name: "Campaign Send",
	Attributes: []provision.DocumentAttribute{{Key: "recipients", Label: "Recipients", Type: "number"}, {Key: "channel", Label: "Channel", Type: "string"}}}

// campaignBeforeWrite validates P3 campaign fields; only drafts change.
func campaignBeforeWrite(_ context.Context, _ pgx.Tx, v, before map[string]any) error {
	if before != nil {
		if st, _ := before["status"].(string); st != "" && st != "draft" {
			return errs.Conflict("campaign_not_draft", "only draft campaigns can be edited")
		}
	}
	if u, ok := v["targetUrl"].(string); ok && u != "" {
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" {
			return handle.Invalid("targetUrl", "invalid_url", "the link must be an http(s) URL")
		}
	}
	mode, _ := merged(v, before, "promoMode").(string)
	code, _ := merged(v, before, "promoCode").(string)
	if (mode == "shared" || mode == "unique") && strings.TrimSpace(code) == "" {
		return handle.Invalid("promoCode", "required", "a promo code (or the prefix of unique codes) is required")
	}
	return nil
}

func merged(v, before map[string]any, k string) any {
	if x, ok := v[k]; ok {
		return x
	}
	if before != nil {
		return before[k]
	}
	return nil
}

// CampaignDetail is a campaign with its P3 fields.
type CampaignDetail struct {
	ID                uuid.UUID      `json:"id" db:"id"`
	Code              string         `json:"code" db:"code"`
	Name              string         `json:"name" db:"name"`
	SegmentID         uuid.UUID      `json:"segmentId" db:"segment_id"`
	SegmentName       string         `json:"segmentName" db:"segment_name"`
	TemplateEvent     string         `json:"templateEvent" db:"template_event"`
	Channel           string         `json:"channel" db:"channel" enum:"email,whatsapp,in_app"`
	Data              map[string]any `json:"data" db:"data"`
	Subject           *string        `json:"subject" db:"subject"`
	Body              *string        `json:"body" db:"body"`
	PromoCode         *string        `json:"promoCode" db:"promo_code"`
	PromoMode         string         `json:"promoMode" db:"promo_mode" enum:"none,shared,unique"`
	TargetURL         *string        `json:"targetUrl" db:"target_url"`
	VoucherTypeRef    *string        `json:"voucherTypeRef" db:"voucher_type_ref"`
	Status            string         `json:"status" db:"status" enum:"draft,pending,scheduled,sent,cancelled"`
	ScheduledAt       *time.Time     `json:"scheduledAt" db:"scheduled_at"`
	SentAt            *time.Time     `json:"sentAt" db:"sent_at"`
	RecipientCount    int            `json:"recipientCount" db:"recipient_count"`
	SentCount         int            `json:"sentCount" db:"sent_count"`
	SkippedCount      int            `json:"skippedCount" db:"skipped_count"`
	ApprovalRequestID *uuid.UUID     `json:"approvalRequestId" db:"approval_request_id"`
	CancelReason      *string        `json:"cancelReason" db:"cancel_reason"`
	PropertyID        uuid.UUID      `json:"-" db:"property_id"`
	AudienceBuiltAt   *time.Time     `json:"-" db:"audience_built_at"`
}

const campaignSelect = `SELECT c.id, c.code, c.name, c.segment_id, s.name AS segment_name, c.template_event, c.channel, c.data, c.subject, c.body,
	c.promo_code, c.promo_mode, c.target_url, c.voucher_type_ref, c.status, c.scheduled_at, c.sent_at, c.recipient_count, c.sent_count, c.skipped_count,
	c.approval_request_id, c.cancel_reason, c.property_id, c.audience_built_at FROM crm.campaigns c JOIN crm.segments s ON s.id = c.segment_id`

func getCampaign(ctx context.Context, q dbtx.Querier, property, cid uuid.UUID, lock bool) (CampaignDetail, error) {
	sql := campaignSelect + ` WHERE c.id = $1 AND c.property_id = $2 AND c.archived_at IS NULL`
	if lock {
		sql += ` FOR UPDATE OF c`
	}
	rows, err := q.Query(ctx, sql, cid, property)
	return handle.One[CampaignDetail](rows, err, "campaign")
}

// CampaignScheduleInput schedules a campaign.
type CampaignScheduleInput struct {
	SendAt *time.Time `json:"sendAt,omitempty" doc:"Default: now (sent by the next dispatch run)"`
}

// CampaignCancelInput cancels a campaign.
type CampaignCancelInput struct {
	Reason string `json:"reason"`
}

// Schedule queues a draft campaign; above the approval threshold of the
// Campaign Policies it waits for approval first (status pending).
func (s *Service) Schedule(ctx context.Context, tx pgx.Tx, property, cid uuid.UUID, in CampaignScheduleInput) (CampaignDetail, error) {
	c, err := getCampaign(ctx, tx, property, cid, true)
	if err != nil {
		return c, err
	}
	if c.Status != "draft" {
		return c, errs.Conflict("campaign_not_draft", "campaign "+c.Code+" is "+c.Status)
	}
	if c.TemplateEvent == CampaignTemplate && strings.TrimSpace(deref(c.Body)) == "" {
		return c, errs.Validation("message_required", "write the message of the campaign", errs.Field("body", "required", "message is required"))
	}
	var segStatus string
	var members int
	if err := tx.QueryRow(ctx, `SELECT status, member_count FROM crm.segments WHERE id = $1`, c.SegmentID).Scan(&segStatus, &members); err != nil {
		return c, err
	}
	if segStatus != "active" {
		return c, errs.Conflict("segment_inactive", "the segment of the campaign is inactive")
	}
	at := clock.Now()
	if in.SendAt != nil && in.SendAt.After(at) {
		at = *in.SendAt
	}
	pol, err := campaignPolicy(ctx, tx, property)
	if err != nil {
		return c, err
	}
	status := "scheduled"
	if pol.ApprovalThreshold > 0 && members > pol.ApprovalThreshold {
		status = "pending"
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.campaigns SET status = $2, scheduled_at = $3, updated_by = $4 WHERE id = $1`, cid, status, at, actor(ctx)); err != nil {
		return c, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "schedule", EntityType: "crm.campaign", EntityID: cid.String(), EntityLabel: c.Code,
		PropertyID: &property, Before: map[string]any{"status": c.Status}, After: map[string]any{"status": status, "scheduledAt": at, "segmentMembers": members}}); err != nil {
		return c, err
	}
	if status == "pending" {
		if s.Approvals == nil {
			return c, errs.Unavailable("approvals are not wired")
		}
		rid, _, err := s.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: CampaignSendDocumentType.Code, DocumentID: cid, DocumentRef: c.Code,
			Title: fmt.Sprintf("Send campaign %s to %d recipients", c.Code, members), PropertyID: property,
			Attributes: map[string]any{"recipients": members, "channel": c.Channel}})
		if err != nil {
			return c, err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.campaigns SET approval_request_id = $2 WHERE id = $1`, cid, rid); err != nil {
			return c, err
		}
	}
	return getCampaign(ctx, tx, property, cid, false)
}

// CampaignDecision applies the approval outcome of a large campaign.
func (s *Service) CampaignDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	status := "draft"
	if d.Status == approval.StatusApproved {
		status = "scheduled"
	}
	tag, err := tx.Exec(ctx, `UPDATE crm.campaigns SET status = $2, cancel_reason = CASE WHEN $2 = 'draft' THEN $3 ELSE cancel_reason END
		WHERE id = $1 AND status = 'pending'`, d.DocumentID, status, "approval "+d.Status+": "+d.Reason)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	pid := d.PropertyID
	return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.campaign", EntityID: d.DocumentID.String(),
		EntityLabel: "campaign approval", PropertyID: &pid, Reason: d.Reason, After: map[string]any{"status": status}})
}

// Cancel stops a campaign that is not sent yet (queued messages are dropped).
func (s *Service) Cancel(ctx context.Context, tx pgx.Tx, property, cid uuid.UUID, in CampaignCancelInput) (CampaignDetail, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return CampaignDetail{}, err
	}
	c, err := getCampaign(ctx, tx, property, cid, true)
	if err != nil {
		return c, err
	}
	if c.Status == "sent" || c.Status == "cancelled" {
		return c, errs.Conflict("campaign_closed", "campaign "+c.Code+" is "+c.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.campaigns SET status = 'cancelled', cancel_reason = $2, updated_by = $3 WHERE id = $1`, cid, in.Reason, actor(ctx)); err != nil {
		return c, err
	}
	if c.ApprovalRequestID != nil && c.Status == "pending" && s.Approvals != nil {
		_ = s.Approvals.Cancel(ctx, tx, *c.ApprovalRequestID, "campaign cancelled")
	}
	after, err := getCampaign(ctx, tx, property, cid, false)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.campaign", EntityID: cid.String(),
		EntityLabel: c.Code, PropertyID: &property, Reason: in.Reason, Before: map[string]any{"status": c.Status}, After: map[string]any{"status": "cancelled"}})
}

// ── audience & dispatch ───────────────────────────────────────────────────

// audience classifies the members of the campaign's segment for its channel.
func (s *Service) audience(ctx context.Context, q dbtx.Querier, c CampaignDetail) ([]recipientInfo, []string, error) {
	recs, err := handle.List[recipientInfo](q.Query(ctx, consentSQL+` JOIN crm.segment_members s ON s.customer_id = c.id
		WHERE s.segment_id = $1 AND c.status = 'active' AND c.erased_at IS NULL ORDER BY c.name, c.id`, c.SegmentID, c.Channel))
	if err != nil {
		return nil, nil, err
	}
	sup, err := suppressedSet(ctx, q, c.PropertyID, c.Channel)
	if err != nil {
		return nil, nil, err
	}
	pol, err := campaignPolicy(ctx, q, c.PropertyID)
	if err != nil {
		return nil, nil, err
	}
	capped := map[uuid.UUID]bool{}
	if pol.FrequencyCapCount > 0 && pol.FrequencyCapDays > 0 {
		rows, err := q.Query(ctx, `SELECT customer_id FROM crm.campaign_recipients WHERE property_id = $1 AND status = 'sent'
			AND sent_at >= now() - make_interval(days => $2) AND campaign_id <> $3 GROUP BY customer_id HAVING count(*) >= $4`,
			c.PropertyID, pol.FrequencyCapDays, c.ID, pol.FrequencyCapCount)
		if err != nil {
			return nil, nil, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return nil, nil, err
		}
		for _, x := range ids {
			capped[x] = true
		}
	}
	status := make([]string, len(recs))
	for i, r := range recs {
		status[i] = classify(r, c.Channel, sup)
		if status[i] == "queued" && capped[r.ID] {
			status[i] = "skipped_frequency_cap"
		}
	}
	return recs, status, nil
}

// buildAudience refreshes a dynamic segment and stores the recipients.
func (s *Service) buildAudience(ctx context.Context, tx pgx.Tx, c CampaignDetail) error {
	var segType string
	if err := tx.QueryRow(ctx, `SELECT segment_type FROM crm.segments WHERE id = $1`, c.SegmentID).Scan(&segType); err != nil {
		return err
	}
	if segType == "dynamic" {
		if _, err := s.Refresh(ctx, tx, c.PropertyID, c.SegmentID); err != nil {
			return err
		}
	}
	recs, status, err := s.audience(ctx, tx, c)
	if err != nil {
		return err
	}
	for i, r := range recs {
		var code *string
		if status[i] == "queued" {
			switch c.PromoMode {
			case "shared":
				code = c.PromoCode
			case "unique":
				u := deref(c.PromoCode) + "-" + shortCode(6)
				code = &u
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm.campaign_recipients (id, campaign_id, property_id, customer_id, channel, address, token, promo_code, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (campaign_id, customer_id) DO NOTHING`, id.New(), c.ID, c.PropertyID, r.ID, c.Channel,
			nullStr(r.address(c.Channel)), token(), code, status[i]); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE crm.campaigns SET audience_built_at = now(), recipient_count = (SELECT count(*) FROM crm.campaign_recipients WHERE campaign_id = $1)
		WHERE id = $1`, c.ID)
	return err
}

type queued struct {
	ID         uuid.UUID  `db:"id"`
	CustomerID uuid.UUID  `db:"customer_id"`
	Address    *string    `db:"address"`
	Token      string     `db:"token"`
	PromoCode  *string    `db:"promo_code"`
	Name       string     `db:"name"`
	UserID     *uuid.UUID `db:"user_id"`
	Locale     *string    `db:"locale"`
}

// messageData is the personalisation of a campaign message.
func (s *Service) messageData(c CampaignDetail, r queued) map[string]any {
	data := map[string]any{}
	for k, v := range c.Data {
		data[k] = v
	}
	data["name"], data["campaign"], data["promoCode"] = r.Name, c.Name, deref(r.PromoCode)
	if c.TargetURL != nil && *c.TargetURL != "" {
		data["link"] = s.website() + "/api/v1/public/campaign-links/" + r.Token
	}
	data["unsubscribeLink"] = s.website() + "/" + linkLang(r.Locale) + "/unsubscribe/" + r.Token
	data["trackingToken"] = r.Token
	return data
}

// send queues the notification of one recipient.
func (s *Service) send(ctx context.Context, tx pgx.Tx, c CampaignDetail, r queued) error {
	if s.Notify == nil {
		return nil
	}
	data := s.messageData(c, r)
	event := c.TemplateEvent
	if strings.TrimSpace(deref(c.Body)) != "" {
		event = CampaignTemplate
		data["subject"] = render(deref(c.Subject), data)
		data["message"] = render(deref(c.Body), data)
	}
	msg := notify.Message{Event: event, Category: "marketing", PropertyID: &c.PropertyID, Data: data, Channels: []string{c.Channel}}
	switch c.Channel {
	case "email":
		msg.Email, msg.Name = deref(r.Address), r.Name
	case "whatsapp":
		msg.Phone, msg.Name = deref(r.Address), r.Name
	case "in_app":
		if r.UserID == nil {
			return nil
		}
		msg.UserIDs = []uuid.UUID{*r.UserID}
		msg.Link = "/c/" + r.Token
	}
	if r.Locale != nil && *r.Locale != "" {
		msg.Locale = *r.Locale
	}
	return s.Notify.Send(ctx, tx, msg)
}

// CampaignDispatchResult reports a dispatch run.
type CampaignDispatchResult struct {
	Campaigns int `json:"campaigns"`
	Sent      int `json:"sent"`
	Completed int `json:"completed"`
}

// DispatchDue sends the due scheduled campaigns of every property, at most
// the batch size of the Campaign Policies per campaign and run (FR-CMP-07).
func (s *Service) DispatchDue(ctx context.Context) (CampaignDispatchResult, error) {
	ctx = dbtx.System(ctx)
	var res CampaignDispatchResult
	var due []struct {
		ID, Property uuid.UUID
	}
	err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, property_id FROM crm.campaigns WHERE status = 'scheduled' AND scheduled_at <= now() AND archived_at IS NULL
			ORDER BY scheduled_at`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x struct{ ID, Property uuid.UUID }
			if err := rows.Scan(&x.ID, &x.Property); err != nil {
				return err
			}
			due = append(due, x)
		}
		return rows.Err()
	})
	if err != nil {
		return res, err
	}
	for _, d := range due {
		err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT true FROM crm.campaigns WHERE id = $1 AND status = 'scheduled' FOR UPDATE SKIP LOCKED`, d.ID).Scan(&ok); err != nil {
				if dbtx.IsNoRows(err) {
					return nil // another worker has it
				}
				return err
			}
			n, done, err := s.dispatchOne(ctx, tx, d.Property, d.ID)
			res.Campaigns++
			res.Sent += n
			if done {
				res.Completed++
			}
			return err
		})
		if err != nil {
			return res, fmt.Errorf("campaign %s: %w", d.ID, err)
		}
	}
	return res, nil
}

func (s *Service) dispatchOne(ctx context.Context, tx pgx.Tx, property, cid uuid.UUID) (int, bool, error) {
	c, err := getCampaign(ctx, tx, property, cid, false)
	if err != nil {
		return 0, false, err
	}
	if c.AudienceBuiltAt == nil {
		if err := s.buildAudience(ctx, tx, c); err != nil {
			return 0, false, err
		}
	}
	pol, err := campaignPolicy(ctx, tx, property)
	if err != nil {
		return 0, false, err
	}
	batch := pol.BatchSize
	if batch <= 0 {
		batch = 500
	}
	list, err := handle.List[queued](tx.Query(ctx, `SELECT r.id, r.customer_id, r.address, r.token, r.promo_code, c.name, c.user_id, c.locale
		FROM crm.campaign_recipients r JOIN crm.customers c ON c.id = r.customer_id WHERE r.campaign_id = $1 AND r.status = 'queued'
		ORDER BY r.created_at, r.id LIMIT $2`, cid, batch))
	if err != nil {
		return 0, false, err
	}
	ids := make([]uuid.UUID, 0, len(list))
	custs := make([]uuid.UUID, 0, len(list))
	batchRecipients := make([]map[string]any, 0, len(list))
	for _, r := range list {
		if err := s.send(ctx, tx, c, r); err != nil {
			return 0, false, err
		}
		ids, custs = append(ids, r.ID), append(custs, r.CustomerID)
		batchRecipients = append(batchRecipients, map[string]any{"recipientId": r.ID, "customerId": r.CustomerID, "promoCode": r.PromoCode})
	}
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE crm.campaign_recipients SET status = 'sent', sent_at = now() WHERE id = ANY($1::uuid[])`, ids); err != nil {
			return 0, false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm.interactions (id, property_id, customer_id, channel, direction, subject, source, ref_type, ref_id)
			SELECT gen_random_uuid(), $1, x, $2, 'outbound', $3, 'campaign', 'crm.campaign', $4 FROM unnest($5::uuid[]) AS x`,
			property, interactionChannelOf(c.Channel), "Campaign "+c.Code, cid, custs); err != nil {
			return 0, false, err
		}
	}
	var left, batchNo int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'queued'), count(DISTINCT sent_at) FILTER (WHERE status = 'sent')
		FROM crm.campaign_recipients WHERE campaign_id = $1`, cid).Scan(&left, &batchNo); err != nil {
		return len(list), false, err
	}
	payload := map[string]any{"campaignId": cid, "code": c.Code, "name": c.Name, "channel": c.Channel, "promoMode": c.PromoMode,
		"promoCode": c.PromoCode, "voucherTypeRef": c.VoucherTypeRef, "batch": batchNo, "final": left == 0, "recipients": batchRecipients}
	if left > 0 {
		if s.Events != nil && len(list) > 0 {
			if _, err := s.Events.Publish(ctx, tx, EventCampaignSent, "crm.campaign", &cid, &property, payload); err != nil {
				return len(list), false, err
			}
		}
		return len(list), false, nil
	}
	var sent, skipped int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'sent'), count(*) FILTER (WHERE status LIKE 'skipped%')
		FROM crm.campaign_recipients WHERE campaign_id = $1`, cid).Scan(&sent, &skipped); err != nil {
		return len(list), false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.campaigns SET status = 'sent', sent_at = now(), sent_count = $2, skipped_count = $3 WHERE id = $1`,
		cid, sent, skipped); err != nil {
		return len(list), false, err
	}
	if s.Events != nil {
		payload["sent"], payload["skipped"] = sent, skipped
		if _, err := s.Events.Publish(ctx, tx, EventCampaignSent, "crm.campaign", &cid, &property, payload); err != nil {
			return len(list), false, err
		}
	}
	return len(list), true, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "send", EntityType: "crm.campaign", EntityID: cid.String(),
		EntityLabel: c.Code, PropertyID: &property, After: map[string]any{"sent": sent, "skipped": skipped}})
}

// linkLang is the website language of a recipient (id unless English).
func linkLang(locale *string) string {
	if locale != nil && strings.HasPrefix(*locale, "en") {
		return "en"
	}
	return "id"
}

// interactionChannelOf maps a campaign channel to an interaction channel.
func interactionChannelOf(ch string) string {
	if ch == "email" || ch == "whatsapp" || ch == "in_app" {
		return ch
	}
	return "other"
}

// ── preview, recipients, statistics ───────────────────────────────────────

// CampaignPreview is the personalised message and the expected audience.
type CampaignPreview struct {
	Channel    string         `json:"channel"`
	Subject    string         `json:"subject"`
	Body       string         `json:"body"`
	Template   string         `json:"template"`
	Customer   string         `json:"customer" doc:"Sample recipient"`
	Recipients int            `json:"recipients" doc:"Members of the segment"`
	Eligible   int            `json:"eligible" doc:"Would receive the message now"`
	Skipped    map[string]int `json:"skipped" doc:"By reason: no consent, suppressed, no contact, frequency cap"`
}

// PreviewCampaign renders the message for a sample customer and classifies
// the audience without sending (FR-CMP-01 preview).
func (s *Service) PreviewCampaign(ctx context.Context, q dbtx.Querier, property, cid uuid.UUID, sample *uuid.UUID) (CampaignPreview, error) {
	c, err := getCampaign(ctx, q, property, cid, false)
	if err != nil {
		return CampaignPreview{}, err
	}
	recs, status, err := s.audience(ctx, q, c)
	if err != nil {
		return CampaignPreview{}, err
	}
	out := CampaignPreview{Channel: c.Channel, Template: c.TemplateEvent, Recipients: len(recs), Skipped: map[string]int{}}
	for _, st := range status {
		if st == "queued" {
			out.Eligible++
		} else {
			out.Skipped[strings.TrimPrefix(st, "skipped_")]++
		}
	}
	r := queued{Token: "preview", Name: "Customer"}
	if sample != nil {
		cust, err := crm.GetCustomer(ctx, q, *sample)
		if err != nil {
			return out, err
		}
		r.Name = cust.Name
	} else if len(recs) > 0 {
		r.Name = recs[0].Name
	}
	out.Customer = r.Name
	if c.PromoMode != "none" {
		code := deref(c.PromoCode)
		if c.PromoMode == "unique" {
			code += "-XXXXXX"
		}
		r.PromoCode = &code
	}
	data := s.messageData(c, r)
	if strings.TrimSpace(deref(c.Body)) != "" {
		out.Template = CampaignTemplate
		out.Subject, out.Body = render(deref(c.Subject), data), render(deref(c.Body), data)
	} else {
		var subj, body string
		err := q.QueryRow(ctx, `SELECT subject, body FROM platform.notification_templates WHERE event_code = $1 AND channel IN ($2, 'email')
			ORDER BY (channel = $2) DESC, (locale = 'en') DESC LIMIT 1`, c.TemplateEvent, c.Channel).Scan(&subj, &body)
		if err != nil && !dbtx.IsNoRows(err) {
			return out, err
		}
		out.Subject, out.Body = render(subj, data), render(body, data)
	}
	return out, nil
}

// CampaignRecipient is one recipient of a campaign with its tracking.
type CampaignRecipient struct {
	ID             uuid.UUID  `json:"id" db:"recipient_id"`
	CustomerID     uuid.UUID  `json:"customerId" db:"customer_id"`
	CustomerName   string     `json:"customerName" db:"customer_name"`
	Channel        string     `json:"channel" db:"channel"`
	Status         string     `json:"status" db:"status" enum:"queued,sent,failed,skipped_no_consent,skipped_suppressed,skipped_frequency_cap,skipped_no_contact"`
	PromoCode      *string    `json:"promoCode" db:"promo_code"`
	SentAt         *time.Time `json:"sentAt" db:"sent_at"`
	DeliveryStatus *string    `json:"deliveryStatus" db:"delivery_status"`
	DeliveredAt    *time.Time `json:"deliveredAt" db:"delivered_at"`
	ReadAt         *time.Time `json:"readAt" db:"read_at"`
	ClickedAt      *time.Time `json:"clickedAt" db:"clicked_at"`
	ClickCount     int        `json:"clickCount" db:"click_count"`
	UnsubscribedAt *time.Time `json:"unsubscribedAt" db:"unsubscribed_at"`
	ConvertedAt    *time.Time `json:"convertedAt" db:"converted_at"`
}

func recipients(ctx context.Context, q dbtx.Querier, cid uuid.UUID, status string, limit int) ([]CampaignRecipient, error) {
	return handle.List[CampaignRecipient](q.Query(ctx, `SELECT v.recipient_id, v.customer_id, c.name AS customer_name, v.channel, v.status, r.promo_code, v.sent_at,
		v.delivery_status, v.delivered_at, v.read_at, v.clicked_at, v.click_count, v.unsubscribed_at, v.converted_at
		FROM reporting.eng_campaign_recipients v JOIN crm.campaign_recipients r ON r.id = v.recipient_id JOIN crm.customers c ON c.id = v.customer_id
		WHERE v.campaign_id = $1 AND ($2 = '' OR v.status = $2) ORDER BY c.name, v.recipient_id LIMIT $3`, cid, status, limit))
}

// CampaignStats is the performance of a campaign (FR-CMP-04).
type CampaignStats struct {
	CampaignID     uuid.UUID      `json:"campaignId"`
	Status         string         `json:"status"`
	Recipients     int            `json:"recipients"`
	Queued         int            `json:"queued"`
	Sent           int            `json:"sent"`
	Delivered      int            `json:"delivered" doc:"Delivered to the device (BSP status)"`
	Read           int            `json:"read" doc:"Read (BSP status) or in-app opened"`
	Clicked        int            `json:"clicked"`
	Converted      int            `json:"converted" doc:"Payment or promo code use within the conversion window"`
	Unsubscribed   int            `json:"unsubscribed"`
	Skipped        map[string]int `json:"skipped"`
	ClickRate      string         `json:"clickRate" doc:"Clicked ÷ sent"`
	ConversionRate string         `json:"conversionRate" doc:"Converted ÷ sent"`
}

// StatsOf computes the funnel of a campaign.
func StatsOf(ctx context.Context, q dbtx.Querier, property, cid uuid.UUID) (CampaignStats, error) {
	c, err := getCampaign(ctx, q, property, cid, false)
	if err != nil {
		return CampaignStats{}, err
	}
	out := CampaignStats{CampaignID: cid, Status: c.Status, Skipped: map[string]int{}}
	rows, err := q.Query(ctx, `SELECT status, count(*)::int, count(*) FILTER (WHERE delivered_at IS NOT NULL OR delivery_status IN ('delivered', 'read'))::int,
		count(*) FILTER (WHERE read_at IS NOT NULL OR delivery_status = 'read')::int, count(*) FILTER (WHERE clicked_at IS NOT NULL)::int,
		count(*) FILTER (WHERE converted_at IS NOT NULL)::int, count(*) FILTER (WHERE unsubscribed_at IS NOT NULL)::int
		FROM reporting.eng_campaign_recipients WHERE campaign_id = $1 GROUP BY status`, cid)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n, del, rd, cl, cv, un int
		if err := rows.Scan(&st, &n, &del, &rd, &cl, &cv, &un); err != nil {
			return out, err
		}
		out.Recipients += n
		switch st {
		case "sent":
			out.Sent += n
		case "queued":
			out.Queued += n
		default:
			out.Skipped[strings.TrimPrefix(st, "skipped_")] += n
		}
		out.Delivered, out.Read, out.Clicked, out.Converted, out.Unsubscribed = out.Delivered+del, out.Read+rd, out.Clicked+cl, out.Converted+cv, out.Unsubscribed+un
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.ClickRate, out.ConversionRate = ratio(out.Clicked, out.Sent), ratio(out.Converted, out.Sent)
	return out, nil
}

func ratio(a, b int) string {
	if b == 0 {
		return "0"
	}
	return fmt.Sprintf("%.4f", float64(a)/float64(b))
}

// ── conversion (FR-CMP-04) ────────────────────────────────────────────────

// OnPaymentSettled marks the campaign messages of the paying customer sent
// within the conversion window as converted.
func (s *Service) OnPaymentSettled(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		PaymentID uuid.UUID `json:"paymentId"`
		Number    string    `json:"number"`
		Purpose   string    `json:"purpose"`
	}
	if err := e.Decode(&p); err != nil || p.Purpose == "account_settlement" {
		return nil // not a customer payment
	}
	var property uuid.UUID
	var customer *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT property_id, customer_id FROM reporting.eng_payments WHERE payment_id = $1`, p.PaymentID).Scan(&property, &customer)
	if dbtx.IsNoRows(err) || (err == nil && customer == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	pol, err := campaignPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE crm.campaign_recipients SET converted_at = now(), conversion_ref = $2 WHERE customer_id = $1 AND status = 'sent'
		AND converted_at IS NULL AND sent_at >= now() - make_interval(days => $3)`, *customer, "payment "+p.Number, max(pol.ConversionDays, 1))
	return err
}

// OnPromotionApplied marks the recipient whose (unique or shared) promo code
// was used as converted (commercial.promotion_applied, contract K3).
func (s *Service) OnPromotionApplied(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		Code       string     `json:"code"`
		CustomerID *uuid.UUID `json:"customerId"`
		SourceType string     `json:"sourceType"`
	}
	if err := e.Decode(&p); err != nil || strings.TrimSpace(p.Code) == "" || e.PropertyID == nil {
		return nil // not a promo code use
	}
	code := strings.ToUpper(strings.TrimSpace(p.Code))
	_, err := tx.Exec(ctx, `UPDATE crm.campaign_recipients r SET converted_at = now(), conversion_ref = $3 FROM crm.campaigns c
		WHERE c.id = r.campaign_id AND r.property_id = $1 AND r.converted_at IS NULL AND r.status = 'sent' AND r.promo_code = $2
		AND (c.promo_mode = 'unique' OR r.customer_id = $4)`, *e.PropertyID, code, "promo "+code+" ("+p.SourceType+")", p.CustomerID)
	return err
}
