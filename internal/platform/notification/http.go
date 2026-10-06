package notification

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/notify"
)

type Notification struct {
	ID        uuid.UUID  `json:"id"`
	EventCode string     `json:"eventCode"`
	Category  string     `json:"category"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	Link      *string    `json:"link"`
	ReadAt    *time.Time `json:"readAt"`
	CreatedAt time.Time  `json:"createdAt"`
}

type UnreadCount struct {
	Unread int `json:"unread"`
}

type Template struct {
	ID        uuid.UUID `json:"id"`
	EventCode string    `json:"eventCode"`
	Channel   string    `json:"channel" enum:"in_app,email,whatsapp,push"`
	Locale    string    `json:"locale" enum:"en,id"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	IsActive  bool      `json:"isActive"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type TemplateUpdate struct {
	Subject  *string `json:"subject,omitempty"`
	Body     *string `json:"body,omitempty"`
	IsActive *bool   `json:"isActive,omitempty"`
}

type Delivery struct {
	ID        uuid.UUID  `json:"id"`
	UserID    *uuid.UUID `json:"userId"`
	EventCode string     `json:"eventCode"`
	Category  string     `json:"category"`
	Channel   string     `json:"channel" enum:"in_app,email,whatsapp,push"`
	Locale    string     `json:"locale"`
	Recipient string     `json:"recipient"`
	Subject   string     `json:"subject"`
	Status    string     `json:"status" enum:"pending,sent,failed,skipped"`
	Attempts  int        `json:"attempts"`
	LastError *string    `json:"lastError"`
	JobID     *int64     `json:"jobId"`
	CreatedAt time.Time  `json:"createdAt"`
	SentAt    *time.Time `json:"sentAt"`
	FailedAt  *time.Time `json:"failedAt"`
}

type Preference struct {
	Category  string `json:"category"`
	Label     string `json:"label"`
	Mandatory bool   `json:"mandatory"`
	InApp     bool   `json:"inApp"`
	Email     bool   `json:"email"`
	WhatsApp  bool   `json:"whatsapp"`
	// Push to subscribed devices (PRD P5 FR-INT-P5-05); omitted in an
	// update = unchanged.
	Push *bool `json:"push,omitempty"`
}

type PreferencesUpdate struct {
	Preferences []Preference `json:"preferences"`
}

type SendTestRequest struct {
	UserID   *uuid.UUID `json:"userId,omitempty" doc:"Defaults to the current user"`
	Channels []string   `json:"channels,omitempty"`
}

// HTTP exposes notification endpoints.
type HTTP struct{ Svc *Service }

func (h *HTTP) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authz.From(ctx)
	lp := httpx.ParseList(r)
	cursor, err := httpx.DecodeCursor(lp.Cursor)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out []Notification
	err = h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		args := []any{p.UserID, lp.Limit + 1}
		where := "user_id = $1"
		if lp.Filters["unread"] == "true" {
			where += " AND read_at IS NULL"
		}
		if cursor != "" {
			args = append(args, cursor)
			where += " AND id < $3::uuid"
		}
		rows, err := tx.Query(ctx, `SELECT id, event_code, category, title, body, link, read_at, created_at
			FROM platform.notifications WHERE `+where+` ORDER BY id DESC LIMIT $2`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n Notification
			if err := rows.Scan(&n.ID, &n.EventCode, &n.Category, &n.Title, &n.Body, &n.Link, &n.ReadAt, &n.CreatedAt); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.BuildPage(out, lp.Limit, func(n Notification) string { return n.ID.String() }))
}

func (h *HTTP) unread(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var c UnreadCount
	err := h.Svc.DB.Primary.QueryRow(ctx, `SELECT count(*) FROM platform.notifications WHERE user_id = $1 AND read_at IS NULL`,
		authz.From(ctx).UserID).Scan(&c.Unread)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (h *HTTP) markRead(all bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		p := authz.From(ctx)
		var nid uuid.UUID
		if !all {
			var err error
			if nid, err = httpx.PathUUID(r, "id"); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
		}
		err := h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var n int64
			if all {
				tag, err := tx.Exec(ctx, `UPDATE platform.notifications SET read_at = now() WHERE user_id = $1 AND read_at IS NULL`, p.UserID)
				if err != nil {
					return err
				}
				n = tag.RowsAffected()
			} else {
				tag, err := tx.Exec(ctx, `UPDATE platform.notifications SET read_at = coalesce(read_at, now()) WHERE id = $1 AND user_id = $2`, nid, p.UserID)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					return errs.NotFound("notification")
				}
				n = 1
			}
			entity := ""
			if !all {
				entity = nid.String()
			}
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "mark_read", EntityType: "platform.notification",
				EntityID: entity, EntityLabel: p.Name, Metadata: map[string]any{"count": n, "all": all}})
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.NoContent(w)
	}
}

func (h *HTTP) templates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	out := []Template{}
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		where, args := "true", []any{}
		if v := lp.Filters["eventCode"]; v != "" {
			args = append(args, v)
			where += " AND event_code = $1"
		}
		rows, err := tx.Query(ctx, `SELECT id, event_code, channel, locale, subject, body, is_active, updated_at
			FROM platform.notification_templates WHERE `+where+` ORDER BY event_code, channel, locale`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Template
			if err := rows.Scan(&t.ID, &t.EventCode, &t.Channel, &t.Locale, &t.Subject, &t.Body, &t.IsActive, &t.UpdatedAt); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Template]{Items: out})
}

func (h *HTTP) updateTemplate(w http.ResponseWriter, r *http.Request) {
	tid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req TemplateUpdate
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	for field, v := range map[string]*string{"subject": req.Subject, "body": req.Body} {
		if v != nil {
			if err := ValidateTemplate(*v); err != nil {
				httpx.WriteError(w, r, errs.Validation("template_invalid", "template syntax error", errs.Field(field, "syntax", err.Error())))
				return
			}
		}
	}
	if req.Body != nil && strings.TrimSpace(*req.Body) == "" {
		httpx.WriteError(w, r, errs.Validation("template_invalid", "body is required", errs.Field("body", "required", "body is required")))
		return
	}
	ctx := r.Context()
	var out Template
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var before Template
		sel := `SELECT id, event_code, channel, locale, subject, body, is_active, updated_at FROM platform.notification_templates WHERE id = $1`
		err := tx.QueryRow(ctx, sel+` FOR UPDATE`, tid).Scan(&before.ID, &before.EventCode, &before.Channel, &before.Locale, &before.Subject, &before.Body, &before.IsActive, &before.UpdatedAt)
		if dbtx.IsNoRows(err) {
			return errs.NotFound("template")
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.notification_templates SET subject = coalesce($2, subject), body = coalesce($3, body),
			is_active = coalesce($4, is_active), updated_by = $5 WHERE id = $1`, tid, req.Subject, req.Body, req.IsActive, id.Ptr(authz.From(ctx).UserID)); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, sel, tid).Scan(&out.ID, &out.EventCode, &out.Channel, &out.Locale, &out.Subject, &out.Body, &out.IsActive, &out.UpdatedAt); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionUpdate, EntityType: "platform.notification_template",
			EntityID: tid.String(), EntityLabel: out.EventCode + " " + out.Channel + " " + out.Locale, Before: before, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *HTTP) deliveries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	cursor, err := httpx.DecodeCursor(lp.Cursor)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out []Delivery
	err = h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		where := []string{"true"}
		args := []any{}
		add := func(cond string, v any) {
			args = append(args, v)
			where = append(where, strings.ReplaceAll(cond, "?", "$"+itoa(len(args))))
		}
		for _, f := range []string{"status", "channel", "eventCode"} {
			if v := lp.Filters[f]; v != "" {
				col := map[string]string{"status": "status", "channel": "channel", "eventCode": "event_code"}[f]
				add(col+" = ?", v)
			}
		}
		if lp.Q != "" {
			add("(recipient ILIKE ? OR subject ILIKE ?)", "%"+lp.Q+"%")
		}
		if cursor != "" {
			add("id < ?::uuid", cursor)
		}
		args = append(args, lp.Limit+1)
		rows, err := tx.Query(ctx, `SELECT id, user_id, event_code, category, channel, locale, recipient, subject, status, attempts,
			last_error, job_id, created_at, sent_at, failed_at FROM platform.notification_deliveries WHERE `+strings.Join(where, " AND ")+
			` ORDER BY id DESC LIMIT $`+itoa(len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Delivery
			if err := rows.Scan(&d.ID, &d.UserID, &d.EventCode, &d.Category, &d.Channel, &d.Locale, &d.Recipient, &d.Subject, &d.Status,
				&d.Attempts, &d.LastError, &d.JobID, &d.CreatedAt, &d.SentAt, &d.FailedAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.BuildPage(out, lp.Limit, func(d Delivery) string { return d.ID.String() }))
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

func (h *HTTP) preferences(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authz.From(ctx)
	prefs, err := h.loadPrefs(ctx, p.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Preference]{Items: prefs})
}

func (h *HTTP) loadPrefs(ctx context.Context, uid uuid.UUID) ([]Preference, error) {
	set := map[string]bool{}
	rows, err := h.Svc.DB.Primary.Query(ctx, `SELECT category, channel, enabled FROM platform.notification_preferences WHERE user_id = $1`, uid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c, ch string
		var en bool
		if err := rows.Scan(&c, &ch, &en); err != nil {
			rows.Close()
			return nil, err
		}
		set[c+"/"+ch] = en
	}
	rows.Close()
	get := func(c, ch string) bool {
		if v, ok := set[c+"/"+ch]; ok {
			return v
		}
		return true
	}
	out := []Preference{}
	for _, c := range Categories {
		push := c.Mandatory || get(c.Code, "push")
		out = append(out, Preference{Category: c.Code, Label: c.Label, Mandatory: c.Mandatory,
			InApp: c.Mandatory || get(c.Code, "in_app"), Email: c.Mandatory || get(c.Code, "email"), WhatsApp: c.Mandatory || get(c.Code, "whatsapp"),
			Push: &push})
	}
	return out, nil
}

func (h *HTTP) updatePreferences(w http.ResponseWriter, r *http.Request) {
	var req PreferencesUpdate
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	p := authz.From(ctx)
	err := h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		before, err := h.loadPrefs(ctx, p.UserID)
		if err != nil {
			return err
		}
		for _, pr := range req.Preferences {
			if categoryMandatory(pr.Category) {
				continue // mandatory categories cannot be opted out
			}
			known := false
			for _, c := range Categories {
				known = known || c.Code == pr.Category
			}
			if !known {
				return errs.Validation("unknown_category", "unknown category", errs.Field("preferences", "unknown", pr.Category))
			}
			chans := map[string]bool{"in_app": pr.InApp, "email": pr.Email, "whatsapp": pr.WhatsApp}
			if pr.Push != nil {
				chans["push"] = *pr.Push
			}
			for ch, en := range chans {
				if _, err := tx.Exec(ctx, `INSERT INTO platform.notification_preferences (user_id, category, channel, enabled)
					VALUES ($1, $2, $3, $4) ON CONFLICT (user_id, category, channel) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`,
					p.UserID, pr.Category, ch, en); err != nil {
					return err
				}
			}
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionUpdate, EntityType: "platform.notification_preferences",
			EntityID: p.UserID.String(), EntityLabel: p.Name, Before: map[string]any{"preferences": before}, After: req})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	prefs, err := h.loadPrefs(ctx, p.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Preference]{Items: prefs})
}

// sendTest sends the "system.test" event (EP-05 AC: e-mail + in-app in the
// recipient's language).
func (h *HTTP) sendTest(w http.ResponseWriter, r *http.Request) {
	var req SendTestRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	p := authz.From(ctx)
	target := p.UserID
	if req.UserID != nil {
		target = *req.UserID
	}
	for _, c := range req.Channels {
		if c != notify.ChannelInApp && c != notify.ChannelEmail && c != notify.ChannelWhatsApp && c != notify.ChannelPush {
			httpx.WriteError(w, r, errs.Validation("invalid_channel", "invalid channel", errs.Field("channels", "invalid", c)))
			return
		}
	}
	err := h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if err := h.Svc.Send(ctx, tx, notify.Message{Event: "system.test", Category: "system", UserIDs: []uuid.UUID{target},
			Channels: req.Channels, Data: map[string]any{"senderName": p.Name, "sentAt": time.Now().UTC().Format(time.RFC3339)}}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "send_test_notification", EntityType: "platform.notification",
			EntityID: target.String(), EntityLabel: p.Name, Metadata: map[string]any{"channels": req.Channels}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

// Register adds the notification routes (PRD §10 Notification).
func (h *HTTP) Register(reg *route.Registry) {
	const tag = "Notifications"
	add := func(rt route.Route) { rt.Module = "platform"; rt.Tag = tag; reg.Add(rt) }
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/notifications", Summary: "My notifications (notification center)",
		Response: Notification{}, List: true, Query: []route.Param{{Name: "filter[unread]", Type: "boolean"}}, Handler: h.list})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/notifications/unread-count", Summary: "Unread badge count",
		Response: UnreadCount{}, Handler: h.unread})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/notifications/{id}:read", Summary: "Mark as read", Handler: h.markRead(false)})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/notifications:read-all", Summary: "Mark all as read", Handler: h.markRead(true)})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/notifications:send-test", Summary: "Send a test notification",
		Permission: "platform.notification.send_test", Request: SendTestRequest{}, Response: map[string]string{}, Status: http.StatusAccepted, Handler: h.sendTest})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/notification-templates", Summary: "List notification templates",
		Permission: "platform.notification_template.view", Response: Template{}, List: true, Query: []route.Param{{Name: "filter[eventCode]"}}, Handler: h.templates})
	add(route.Route{Method: http.MethodPatch, Path: "/api/v1/platform/notification-templates/{id}", Summary: "Edit notification template",
		Permission: "platform.notification_template.update", Request: TemplateUpdate{}, Response: Template{}, Handler: h.updateTemplate})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/notification-deliveries", Summary: "Delivery history",
		Permission: "platform.notification_delivery.view", Response: Delivery{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[channel]"}, {Name: "filter[eventCode]"}}, Handler: h.deliveries})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/notification-preferences", Summary: "My notification preferences",
		Response: Preference{}, List: true, Handler: h.preferences})
	add(route.Route{Method: http.MethodPut, Path: "/api/v1/platform/notification-preferences", Summary: "Update my notification preferences",
		Request: PreferencesUpdate{}, Response: Preference{}, List: true, Handler: h.updatePreferences})
	h.registerPush(add) // PRD P5 FR-INT-P5-05
}
