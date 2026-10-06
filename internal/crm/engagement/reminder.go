package engagement

// Renewal & Birthday Reminders and follow-ups (FR-CMP-06): scheduled
// messages per reminder rule — a birthday greeting with an offer, a
// membership renewal offer (complementing P1's H-30 / H-7 renewal notices)
// and a win-back follow-up N days after the last visit — sent once per
// customer and occurrence, with consent and suppression applied.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/resource"
)

// ReminderRules is the reminder rule master.
var ReminderRules = &resource.Def{
	Key: "crm.reminder_rule", Module: "crm", Perm: "crm.reminder", Path: "/api/v1/crm/reminder-rules", Table: "crm.reminder_rules",
	Name: "Reminder Rule", Plural: "Reminder Rules", Tag: "Campaigns", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "kind, code, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "kind", Column: "kind", Label: "Reminder", Kind: resource.Enum, Enum: []string{"birthday", "renewal", "follow_up"}, Required: true, Filter: true},
		{Name: "daysOffset", Column: "days_offset", Label: "Days (birthday / renewal: before; follow-up: after the last visit)", Kind: resource.Int,
			Default: int64(0), Min: resource.Min(0), MaxN: resource.Max(365)},
		{Name: "channel", Column: "channel", Label: "Channel", Kind: resource.Enum, Enum: []string{"email", "whatsapp", "in_app"}, Default: "email"},
		{Name: "templateEvent", Column: "template_event", Label: "Notification Template", Kind: resource.String, Max: 80, Default: ReminderTemplate},
		{Name: "subject", Column: "subject", Label: "Subject", Kind: resource.String, Max: 200},
		{Name: "body", Column: "body", Label: "Message ({{.name}}, {{.promoCode}}, {{.date}})", Kind: resource.Text, Max: 4000},
		{Name: "promoCode", Column: "promo_code", Label: "Promo Code (Commercial)", Kind: resource.String, Max: 40, Upper: true},
		resource.Status("active", "inactive")},
}

func init() {
	ReminderRules.Hooks.BeforeWrite = func(_ context.Context, _ pgx.Tx, v, before map[string]any) error {
		kind, _ := merged(v, before, "kind").(string)
		var days int64
		switch d := merged(v, before, "daysOffset").(type) {
		case int64:
			days = d
		case int:
			days = int64(d)
		}
		if kind == "renewal" && (days == 30 || days == 7) {
			return handle.Invalid("daysOffset", "duplicate_reminder", "membership renewal reminders H-30 and H-7 are already sent by Membership; choose another day")
		}
		if kind == "follow_up" && days == 0 {
			return handle.Invalid("daysOffset", "invalid_days", "a follow-up needs the number of days after the last visit")
		}
		return nil
	}
}

// defaultTemplates are used when a rule has no own message.
var defaultTemplates = map[string]string{"birthday": "crm.birthday_greeting", "renewal": "crm.renewal_offer", "follow_up": "crm.follow_up_message"}

type reminderRule struct {
	ID       uuid.UUID `db:"id"`
	Property uuid.UUID `db:"property_id"`
	Code     string    `db:"code"`
	Name     string    `db:"name"`
	Kind     string    `db:"kind"`
	Days     int       `db:"days_offset"`
	Channel  string    `db:"channel"`
	Template string    `db:"template_event"`
	Subject  *string   `db:"subject"`
	Body     *string   `db:"body"`
	Promo    *string   `db:"promo_code"`
}

// dueCustomer is a customer a reminder is due for.
type dueCustomer struct {
	Customer   uuid.UUID `db:"customer_id"`
	Occurrence string    `db:"occurrence"`
	Date       string    `db:"date"`
	Detail     string    `db:"detail"`
}

// due finds the customers a rule applies to today.
func (s *Service) due(ctx context.Context, q dbtx.Querier, r reminderRule, today time.Time) ([]dueCustomer, error) {
	switch r.Kind {
	case "birthday":
		target := today.AddDate(0, 0, r.Days)
		feb29 := target.Month() == time.February && target.Day() == 28 && !leap(target.Year())
		return handle.List[dueCustomer](q.Query(ctx, `SELECT id AS customer_id, $3::text AS occurrence, to_char($2::date, 'YYYY-MM-DD') AS date, '' AS detail
			FROM crm.customers WHERE property_id = $1 AND status = 'active' AND erased_at IS NULL AND birth_date IS NOT NULL
			AND (to_char(birth_date, 'MM-DD') = to_char($2::date, 'MM-DD') OR ($4 AND to_char(birth_date, 'MM-DD') = '02-29'))`,
			r.Property, target, fmt.Sprint(target.Year()), feb29))
	case "renewal":
		target := today.AddDate(0, 0, r.Days)
		return handle.List[dueCustomer](q.Query(ctx, `SELECT DISTINCT ON (customer_id) customer_id, membership_id::text || ':' || to_char(ends_on, 'YYYY-MM-DD') AS occurrence,
			to_char(ends_on, 'YYYY-MM-DD') AS date, type_name AS detail FROM reporting.membership_lifecycle
			WHERE property_id = $1 AND role = 'principal' AND status = 'active' AND ends_on = $2::date AND customer_id IS NOT NULL ORDER BY customer_id`,
			r.Property, target))
	case "follow_up":
		target := today.AddDate(0, 0, -r.Days)
		tz := location(ctx, q, r.Property).String()
		return handle.List[dueCustomer](q.Query(ctx, `SELECT customer_id, to_char(last_day, 'YYYY-MM-DD') AS occurrence, to_char(last_day, 'YYYY-MM-DD') AS date,
			'' AS detail FROM (SELECT customer_id, max((posted_at AT TIME ZONE $3)::date) AS last_day FROM reporting.eng_folio_lines
			WHERE property_id = $1 AND customer_id IS NOT NULL GROUP BY customer_id) x WHERE last_day = $2::date`, r.Property, target, tz))
	}
	return nil, nil
}

func leap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

// ReminderPreview lists who a rule reaches today.
type ReminderPreview struct {
	Rule      string           `json:"rule"`
	Kind      string           `json:"kind"`
	Date      string           `json:"date"`
	Customers []ReminderTarget `json:"customers"`
}

// ReminderTarget is one customer of a reminder.
type ReminderTarget struct {
	CustomerID uuid.UUID `json:"customerId"`
	Name       string    `json:"name"`
	Occurrence string    `json:"occurrence"`
	Status     string    `json:"status" enum:"queued,skipped_no_consent,skipped_suppressed,skipped_no_contact,already_sent"`
}

func loadRule(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (reminderRule, error) {
	rows, err := q.Query(ctx, `SELECT id, property_id, code, name, kind, days_offset, channel, template_event, subject, body, promo_code
		FROM crm.reminder_rules WHERE id = $1 AND archived_at IS NULL`, rid)
	return handle.One[reminderRule](rows, err, "reminder rule")
}

// targets classifies the customers due for a rule.
func (s *Service) targets(ctx context.Context, q dbtx.Querier, r reminderRule, today time.Time) ([]ReminderTarget, map[uuid.UUID]recipientInfo, []dueCustomer, error) {
	due, err := s.due(ctx, q, r, today)
	if err != nil || len(due) == 0 {
		return nil, nil, due, err
	}
	ids := make([]uuid.UUID, len(due))
	for i, d := range due {
		ids[i] = d.Customer
	}
	recs, err := handle.List[recipientInfo](q.Query(ctx, consentSQL+` WHERE c.id = ANY($1::uuid[])`, ids, r.Channel))
	if err != nil {
		return nil, nil, due, err
	}
	byID := map[uuid.UUID]recipientInfo{}
	for _, x := range recs {
		byID[x.ID] = x
	}
	sup, err := suppressedSet(ctx, q, r.Property, r.Channel)
	if err != nil {
		return nil, nil, due, err
	}
	sent, err := idSet(ctx, q, `SELECT customer_id FROM crm.reminder_log WHERE rule_id = $1 AND customer_id = ANY($2::uuid[]) AND
		occurrence = ANY($3::text[])`, r.ID, ids, occurrences(due))
	if err != nil {
		return nil, nil, due, err
	}
	out := make([]ReminderTarget, 0, len(due))
	for _, d := range due {
		ri, ok := byID[d.Customer]
		if !ok {
			continue
		}
		st := classify(ri, r.Channel, sup)
		if sent[d.Customer] {
			st = "already_sent"
		}
		out = append(out, ReminderTarget{CustomerID: d.Customer, Name: ri.Name, Occurrence: d.Occurrence, Status: st})
	}
	return out, byID, due, nil
}

func occurrences(d []dueCustomer) []string {
	out := make([]string, len(d))
	for i, x := range d {
		out[i] = x.Occurrence
	}
	return out
}

// PreviewReminder lists the customers a rule reaches today (no sending).
func (s *Service) PreviewReminder(ctx context.Context, q dbtx.Querier, property, rid uuid.UUID) (ReminderPreview, error) {
	r, err := loadRule(ctx, q, rid)
	if err != nil {
		return ReminderPreview{}, err
	}
	if r.Property != property {
		return ReminderPreview{}, handle.Invalid("id", "not_found", "reminder rule not found")
	}
	today := localToday(ctx, q, property)
	ts, _, _, err := s.targets(ctx, q, r, today)
	if ts == nil {
		ts = []ReminderTarget{}
	}
	return ReminderPreview{Rule: r.Code, Kind: r.Kind, Date: today.Format("2006-01-02"), Customers: ts}, err
}

// runRule sends the reminders of one rule for today.
func (s *Service) runRule(ctx context.Context, tx pgx.Tx, r reminderRule, today time.Time) (int, error) {
	ts, recs, due, err := s.targets(ctx, tx, r, today)
	if err != nil {
		return 0, err
	}
	dates := map[uuid.UUID]dueCustomer{}
	for _, d := range due {
		dates[d.Customer] = d
	}
	sent := 0
	for _, t := range ts {
		if t.Status == "already_sent" {
			continue
		}
		status := t.Status
		if status == "queued" {
			status = "sent"
		}
		tag, err := tx.Exec(ctx, `INSERT INTO crm.reminder_log (id, property_id, rule_id, customer_id, occurrence, channel, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (rule_id, customer_id, occurrence) DO NOTHING`, id.New(), r.Property, r.ID, t.CustomerID, t.Occurrence,
			r.Channel, status)
		if err != nil {
			return sent, err
		}
		if tag.RowsAffected() == 0 || status != "sent" || s.Notify == nil {
			continue
		}
		ri := recs[t.CustomerID]
		d := dates[t.CustomerID]
		data := map[string]any{"name": ri.Name, "promoCode": deref(r.Promo), "date": d.Date, "detail": d.Detail,
			"link": s.portal() + "/profile/communication-preferences"}
		event := r.Template
		if strings.TrimSpace(deref(r.Body)) != "" {
			event = ReminderTemplate
			data["subject"], data["message"] = render(deref(r.Subject), data), render(deref(r.Body), data)
		} else if event == "" || event == ReminderTemplate {
			event = defaultTemplates[r.Kind]
		}
		msg := notify.Message{Event: event, Category: "marketing", PropertyID: &r.Property, Data: data, Channels: []string{r.Channel}, Locale: ri.Locale}
		switch r.Channel {
		case "email":
			msg.Email, msg.Name = ri.address("email"), ri.Name
		case "whatsapp":
			msg.Phone, msg.Name = ri.address("whatsapp"), ri.Name
		case "in_app":
			msg.UserIDs = []uuid.UUID{*ri.UserID}
		}
		if err := s.Notify.Send(ctx, tx, msg); err != nil {
			return sent, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm.interactions (id, property_id, customer_id, channel, direction, subject, source, ref_type, ref_id)
			VALUES ($1,$2,$3,$4,'outbound',$5,'reminder','crm.reminder_rule',$6)`, id.New(), r.Property, t.CustomerID, r.Channel, "Reminder "+r.Name, r.ID); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}

// RunReminders sends today's reminders of every active rule.
func (s *Service) RunReminders(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	var rules []reminderRule
	if err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		rules, err = handle.List[reminderRule](tx.Query(ctx, `SELECT id, property_id, code, name, kind, days_offset, channel, template_event, subject, body,
			promo_code FROM crm.reminder_rules WHERE status = 'active' AND archived_at IS NULL ORDER BY property_id, kind, code`))
		return err
	}); err != nil {
		return 0, err
	}
	total := 0
	for _, r := range rules {
		err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
			n, err := s.runRule(ctx, tx, r, localToday(ctx, tx, r.Property))
			total += n
			return err
		})
		if err != nil {
			return total, fmt.Errorf("reminder %s: %w", r.Code, err)
		}
	}
	return total, nil
}

// ReminderLog is one reminder sent (or skipped).
type ReminderLog struct {
	ID           uuid.UUID `json:"id" db:"id"`
	RuleID       uuid.UUID `json:"ruleId" db:"rule_id"`
	RuleCode     string    `json:"ruleCode" db:"rule_code"`
	Kind         string    `json:"kind" db:"kind"`
	CustomerID   uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerName string    `json:"customerName" db:"customer_name"`
	Occurrence   string    `json:"occurrence" db:"occurrence"`
	Channel      string    `json:"channel" db:"channel"`
	Status       string    `json:"status" db:"status" enum:"sent,skipped_no_consent,skipped_suppressed,skipped_no_contact"`
	CreatedAt    time.Time `json:"createdAt" db:"created_at"`
}
