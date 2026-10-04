package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/audit"
)

// Publisher publishes domain events (implemented by outbox.Bus).
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

// HTTP exposes Settings → Integrations, integration logs, webhooks and the
// bridge agent endpoints.
type HTTP struct {
	Svc    *Service
	Events Publisher
}

// Integration is the API view; credentials are never returned (FR-INT-02).
type Integration struct {
	ID               uuid.UUID      `json:"id"`
	Code             string         `json:"code"`
	Adapter          string         `json:"adapter"`
	AdapterName      string         `json:"adapterName"`
	Capability       string         `json:"capability" enum:"payment,messaging,email,tax_invoice,resident_data,hardware,captcha"`
	Name             string         `json:"name"`
	Enabled          bool           `json:"enabled"`
	Mode             string         `json:"mode" enum:"sandbox,production"`
	CredentialKeys   []string       `json:"credentialKeys" doc:"Names of credentials that are set; values are never returned"`
	WebhookSecretSet bool           `json:"webhookSecretSet"`
	WebhookURL       string         `json:"webhookUrl,omitempty"`
	Settings         map[string]any `json:"settings"`
	LastTestedAt     *time.Time     `json:"lastTestedAt"`
	LastTestOK       *bool          `json:"lastTestOk"`
	LastTestMessage  *string        `json:"lastTestMessage"`
	UpdatedAt        time.Time      `json:"updatedAt"`
}

type CreateIntegrationRequest struct {
	Code        string            `json:"code"`
	Adapter     string            `json:"adapter"`
	Name        string            `json:"name"`
	Mode        string            `json:"mode" enum:"sandbox,production"`
	Enabled     bool              `json:"enabled"`
	Credentials map[string]string `json:"credentials,omitempty"`
	Settings    map[string]any    `json:"settings,omitempty"`
}

type UpdateIntegrationRequest struct {
	Name        *string           `json:"name,omitempty"`
	Mode        *string           `json:"mode,omitempty" enum:"sandbox,production"`
	Enabled     *bool             `json:"enabled,omitempty"`
	Credentials map[string]string `json:"credentials,omitempty" doc:"Keys present replace stored values; empty string removes a key"`
	Settings    map[string]any    `json:"settings,omitempty"`
}

type TestResult struct {
	OK      bool      `json:"ok"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

type WebhookSecretResponse struct {
	WebhookSecret string `json:"webhookSecret"`
	WebhookURL    string `json:"webhookUrl"`
}

type IntegrationLog struct {
	ID              uuid.UUID       `json:"id"`
	IntegrationCode string          `json:"integrationCode"`
	Direction       string          `json:"direction" enum:"outbound,inbound"`
	Operation       string          `json:"operation"`
	Method          *string         `json:"method"`
	URL             *string         `json:"url"`
	Request         json.RawMessage `json:"request"`
	Response        json.RawMessage `json:"response"`
	StatusCode      *int            `json:"statusCode"`
	Success         bool            `json:"success"`
	DurationMs      int             `json:"durationMs"`
	Error           *string         `json:"error"`
	CreatedAt       time.Time       `json:"createdAt"`
}

type WebhookAck struct {
	Status  string `json:"status" enum:"processed,duplicate"`
	EventID string `json:"eventId"`
}

const integrationSelect = `SELECT id, code, adapter, capability, name, enabled, mode, credentials_enc, webhook_secret_enc,
	settings, last_tested_at, last_test_ok, last_test_message, updated_at FROM platform.integrations`

func (h *HTTP) scan(row pgx.Row) (Integration, error) {
	var it Integration
	var creds, wh []byte
	err := row.Scan(&it.ID, &it.Code, &it.Adapter, &it.Capability, &it.Name, &it.Enabled, &it.Mode, &creds, &wh,
		&it.Settings, &it.LastTestedAt, &it.LastTestOK, &it.LastTestMessage, &it.UpdatedAt)
	if err != nil {
		return it, err
	}
	it.CredentialKeys = []string{}
	if len(creds) > 0 {
		m, err := h.Svc.credentials(record{Code: it.Code, Credentials: creds})
		if err == nil {
			for k, v := range m {
				if v != "" {
					it.CredentialKeys = append(it.CredentialKeys, k)
				}
			}
		}
	}
	it.WebhookSecretSet = len(wh) > 0
	if info, ok := adapterInfo(it.Adapter); ok {
		it.AdapterName = info.Name
		if info.Webhooks {
			it.WebhookURL = "/api/v1/webhooks/" + it.Code
		}
	}
	if it.Settings == nil {
		it.Settings = map[string]any{}
	}
	return it, nil
}

func (h *HTTP) listAdapters(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, httpx.Page[AdapterInfo]{Items: Adapters()})
}

func (h *HTTP) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []Integration{}
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, integrationSelect+` ORDER BY capability, code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			it, err := h.scan(rows)
			if err != nil {
				return err
			}
			out = append(out, it)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Integration]{Items: out})
}

func (h *HTTP) get(w http.ResponseWriter, r *http.Request) {
	iid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out Integration
	err = h.Svc.DB.WithReadTx(r.Context(), func(tx pgx.Tx) error {
		out, err = h.scan(tx.QueryRow(r.Context(), integrationSelect+` WHERE id = $1`, iid))
		if dbtx.IsNoRows(err) {
			return errs.NotFound("integration")
		}
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

var integrationCodeRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)

func validateRequired(info AdapterInfo, creds map[string]string) error {
	var fields []errs.FieldError
	for _, f := range info.Credentials {
		if f.Required && strings.TrimSpace(creds[f.Key]) == "" {
			fields = append(fields, errs.Field("credentials."+f.Key, "required", f.Label+" is required"))
		}
	}
	if len(fields) > 0 {
		return errs.Validation("credentials_required", "missing credentials", fields...)
	}
	return nil
}

func (h *HTTP) create(w http.ResponseWriter, r *http.Request) {
	var req CreateIntegrationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	info, ok := adapterInfo(req.Adapter)
	var fields []errs.FieldError
	if !integrationCodeRe.MatchString(req.Code) {
		fields = append(fields, errs.Field("code", "invalid", "lower-case letters, digits and dashes"))
	}
	if !ok {
		fields = append(fields, errs.Field("adapter", "invalid", "unknown adapter"))
	}
	if strings.TrimSpace(req.Name) == "" {
		fields = append(fields, errs.Field("name", "required", "name is required"))
	}
	if req.Mode == "" {
		req.Mode = "sandbox"
	}
	if req.Mode != "sandbox" && req.Mode != "production" {
		fields = append(fields, errs.Field("mode", "invalid", "sandbox or production"))
	}
	if len(fields) > 0 {
		httpx.WriteError(w, r, errs.Validation("invalid_integration", "invalid integration", fields...))
		return
	}
	if req.Enabled {
		if err := validateRequired(info, req.Credentials); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	ctx := r.Context()
	var out Integration
	err := h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var sealed []byte
		if len(req.Credentials) > 0 {
			raw, _ := json.Marshal(req.Credentials)
			var err error
			if sealed, err = h.Svc.Box.Seal(raw); err != nil {
				return err
			}
		}
		var whSealed []byte
		if info.Webhooks {
			var err error
			if whSealed, err = h.Svc.Box.Seal([]byte(secret.RandomToken(32))); err != nil {
				return err
			}
		}
		settings := req.Settings
		if settings == nil {
			settings = map[string]any{}
		}
		iid := id.New()
		uid := id.Ptr(authz.From(ctx).UserID)
		if _, err := tx.Exec(ctx, `INSERT INTO platform.integrations (id, code, adapter, capability, name, enabled, mode, credentials_enc,
			webhook_secret_enc, settings, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`,
			iid, req.Code, req.Adapter, info.Capability, req.Name, req.Enabled, req.Mode, sealed, whSealed, settings, uid); err != nil {
			if ok, _ := dbtx.IsUniqueViolation(err); ok {
				return errs.Validation("code_taken", "code already used", errs.Field("code", "taken", "code already used"))
			}
			return err
		}
		var err error
		out, err = h.scan(tx.QueryRow(ctx, integrationSelect+` WHERE id = $1`, iid))
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionCreate, EntityType: "platform.integration",
			EntityID: iid.String(), EntityLabel: req.Name, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *HTTP) update(w http.ResponseWriter, r *http.Request) {
	iid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req UpdateIntegrationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.Mode != nil && *req.Mode != "sandbox" && *req.Mode != "production" {
		httpx.WriteError(w, r, errs.Validation("invalid_mode", "invalid mode", errs.Field("mode", "invalid", "sandbox or production")))
		return
	}
	ctx := r.Context()
	var out Integration
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		rec, err := h.Svc.load(ctx, tx, `id = $1 FOR UPDATE`, iid)
		if dbtx.IsNoRows(err) {
			return errs.NotFound("integration")
		}
		if err != nil {
			return err
		}
		before, err := h.scan(tx.QueryRow(ctx, integrationSelect+` WHERE id = $1`, iid))
		if err != nil {
			return err
		}
		creds, err := h.Svc.credentials(rec)
		if err != nil {
			return err
		}
		credsChanged := false
		for k, v := range req.Credentials {
			credsChanged = true
			if v == "" {
				delete(creds, k)
			} else {
				creds[k] = v
			}
		}
		enabled := rec.Enabled
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		if enabled {
			info, _ := adapterInfo(rec.Adapter)
			if err := validateRequired(info, creds); err != nil {
				return err
			}
		}
		sealed := rec.Credentials
		if credsChanged {
			raw, _ := json.Marshal(creds)
			if sealed, err = h.Svc.Box.Seal(raw); err != nil {
				return err
			}
		}
		settings := rec.Settings
		if req.Settings != nil {
			settings = req.Settings
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.integrations SET name = coalesce($2, name), mode = coalesce($3, mode), enabled = $4,
			credentials_enc = $5, settings = $6, updated_by = $7 WHERE id = $1`,
			iid, req.Name, req.Mode, enabled, sealed, settings, id.Ptr(authz.From(ctx).UserID)); err != nil {
			return err
		}
		out, err = h.scan(tx.QueryRow(ctx, integrationSelect+` WHERE id = $1`, iid))
		if err != nil {
			return err
		}
		md := map[string]any{}
		if credsChanged {
			md["credentialsChanged"] = true
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionUpdate, EntityType: "platform.integration",
			EntityID: iid.String(), EntityLabel: out.Name, Before: before, After: out, Metadata: md})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// test runs Test Connection (FR-INT-02).
func (h *HTTP) test(w http.ResponseWriter, r *http.Request) {
	iid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	rec, err := h.Svc.load(ctx, h.Svc.DB.Primary, `id = $1`, iid)
	if dbtx.IsNoRows(err) {
		httpx.WriteError(w, r, errs.NotFound("integration"))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res := TestResult{At: clock.Now()}
	adapter, err := h.Svc.instantiate(ctx, rec)
	if err != nil {
		res.Message = err.Error()
	} else if t, ok := adapter.(Tester); ok {
		tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		msg, terr := t.TestConnection(tctx)
		cancel()
		if terr != nil {
			res.Message = terr.Error()
		} else {
			res.OK, res.Message = true, msg
		}
	} else {
		res.OK, res.Message = true, "Adapter loaded (no connectivity check available)"
	}
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.integrations SET last_tested_at = $2, last_test_ok = $3, last_test_message = $4 WHERE id = $1`,
			iid, res.At, res.OK, res.Message); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "test_connection", EntityType: "platform.integration",
			EntityID: iid.String(), EntityLabel: rec.Name, After: res})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *HTTP) rotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	iid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	plain := secret.RandomToken(32)
	var out WebhookSecretResponse
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		rec, err := h.Svc.load(ctx, tx, `id = $1 FOR UPDATE`, iid)
		if dbtx.IsNoRows(err) {
			return errs.NotFound("integration")
		}
		if err != nil {
			return err
		}
		if info, _ := adapterInfo(rec.Adapter); !info.Webhooks {
			return errs.Conflict("webhooks_not_supported", "this adapter does not receive webhooks")
		}
		sealed, err := h.Svc.Box.Seal([]byte(plain))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.integrations SET webhook_secret_enc = $2 WHERE id = $1`, iid, sealed); err != nil {
			return err
		}
		out = WebhookSecretResponse{WebhookSecret: plain, WebhookURL: "/api/v1/webhooks/" + rec.Code}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "webhook_secret_rotated", Category: audit.CategorySecurity,
			EntityType: "platform.integration", EntityID: iid.String(), EntityLabel: rec.Name})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *HTTP) logs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	cursor, err := httpx.DecodeCursor(lp.Cursor)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out []IntegrationLog
	err = h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		where := []string{"true"}
		args := []any{}
		add := func(cond string, v any) {
			args = append(args, v)
			where = append(where, strings.ReplaceAll(cond, "?", "$"+itoa(len(args))))
		}
		if v := lp.Filters["integrationCode"]; v != "" {
			add("integration_code = ?", v)
		}
		if v := lp.Filters["direction"]; v != "" {
			add("direction = ?", v)
		}
		if v := lp.Filters["success"]; v != "" {
			add("success = ?", v == "true")
		}
		if lp.Q != "" {
			add("(operation ILIKE ? OR error ILIKE ? OR correlation_id ILIKE ?)", "%"+lp.Q+"%")
		}
		if cursor != "" {
			add("id < ?::uuid", cursor)
		}
		args = append(args, lp.Limit+1)
		rows, err := tx.Query(ctx, `SELECT id, integration_code, direction, operation, method, url, coalesce(request, 'null'),
			coalesce(response, 'null'), status_code, success, duration_ms, error, created_at FROM platform.integration_logs
			WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT $`+itoa(len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l IntegrationLog
			if err := rows.Scan(&l.ID, &l.IntegrationCode, &l.Direction, &l.Operation, &l.Method, &l.URL, &l.Request, &l.Response,
				&l.StatusCode, &l.Success, &l.DurationMs, &l.Error, &l.CreatedAt); err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.BuildPage(out, lp.Limit, func(l IntegrationLog) string { return l.ID.String() }))
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// webhook receives inbound webhooks (FR-INT-03): signature verified by the
// adapter, then stored with a unique (integration, external id) so the same
// webhook delivered twice is processed only once.
func (h *HTTP) webhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := chi.URLParam(r, "integration")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpx.WriteError(w, r, errs.BadRequest("body_unreadable", "unreadable body"))
		return
	}
	start := time.Now()
	rec, err := h.Svc.load(ctx, h.Svc.DB.Primary, `code = $1 AND enabled`, code)
	if dbtx.IsNoRows(err) {
		httpx.WriteError(w, r, errs.NotFound("integration"))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	logIn := func(op string, status int, resp any, e error) {
		var payload any
		_ = json.Unmarshal(body, &payload)
		h.Svc.LogCall(ctx, &rec.ID, rec.Code, Call{Direction: "inbound", Operation: op, Method: r.Method, URL: r.URL.Path,
			Request: payload, Response: resp, StatusCode: status, Err: e, Duration: time.Since(start),
			CorrelationID: reqctx.GetMeta(ctx).RequestID})
	}
	adapter, err := h.Svc.instantiate(ctx, rec)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	verifier, ok := adapter.(WebhookVerifier)
	if !ok {
		httpx.WriteError(w, r, errs.NotFound("webhook endpoint"))
		return
	}
	var whSecret string
	if len(rec.WebhookKey) > 0 {
		plain, err := h.Svc.Box.Open(rec.WebhookKey)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		whSecret = string(plain)
	}
	evt, err := verifier.VerifyWebhook(r.Header, body, whSecret, clock.Now())
	if err != nil {
		logIn("webhook_rejected", http.StatusUnauthorized, nil, err)
		e := errs.Unauthorized("invalid webhook signature")
		e.Code = "invalid_signature"
		httpx.WriteError(w, r, e)
		return
	}
	var ack WebhookAck
	err = h.Svc.DB.WithTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		payload, _ := json.Marshal(evt.Data)
		wid := id.New()
		tag, err := tx.Exec(ctx, `INSERT INTO platform.webhook_events (id, integration_id, external_id, event_type, payload, status, attempts)
			VALUES ($1, $2, $3, $4, $5, 'received', 1) ON CONFLICT (integration_id, external_id) DO NOTHING`,
			wid, rec.ID, evt.ExternalID, evt.Type, payload)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			ack = WebhookAck{Status: "duplicate", EventID: evt.ExternalID}
			if _, err := tx.Exec(ctx, `UPDATE platform.webhook_events SET attempts = attempts + 1 WHERE integration_id = $1 AND external_id = $2`,
				rec.ID, evt.ExternalID); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "webhook_duplicate_ignored", Category: audit.CategorySystem,
				EntityType: "platform.webhook_event", EntityID: evt.ExternalID, EntityLabel: rec.Code + " " + evt.Type,
				ActorName: "webhook:" + rec.Code})
		}
		if h.Events != nil {
			if _, err := h.Events.Publish(ctx, tx, "integration.webhook_received", "platform.webhook_event", &wid, nil, map[string]any{
				"integrationCode": rec.Code, "capability": rec.Capability, "eventType": evt.Type, "externalId": evt.ExternalID, "data": evt.Data,
			}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.webhook_events SET status = 'processed', processed_at = now() WHERE id = $1`, wid); err != nil {
			return err
		}
		ack = WebhookAck{Status: "processed", EventID: evt.ExternalID}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "webhook_received", Category: audit.CategorySystem,
			EntityType: "platform.webhook_event", EntityID: wid.String(), EntityLabel: rec.Code + " " + evt.Type,
			ActorName: "webhook:" + rec.Code, Metadata: map[string]any{"externalId": evt.ExternalID, "type": evt.Type}})
	})
	if err != nil {
		logIn("webhook_failed", http.StatusInternalServerError, nil, err)
		httpx.WriteError(w, r, err)
		return
	}
	logIn("webhook_"+ack.Status, http.StatusOK, ack, nil)
	httpx.JSON(w, http.StatusOK, ack)
}

// webhookChallenge answers a GET handshake for adapters that need one.
func (h *HTTP) webhookChallenge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rec, err := h.Svc.load(ctx, h.Svc.DB.Primary, `code = $1 AND enabled`, chi.URLParam(r, "integration"))
	if err != nil {
		httpx.WriteError(w, r, errs.NotFound("integration"))
		return
	}
	adapter, err := h.Svc.instantiate(ctx, rec)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, ok := adapter.(WebhookChallenger)
	if !ok {
		httpx.WriteError(w, r, errs.NotFound("webhook endpoint"))
		return
	}
	answer, ok := c.Challenge(r.URL.Query())
	if !ok {
		httpx.WriteError(w, r, errs.Forbidden("verification failed"))
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(answer)) //nolint:gosec // G705: handshake echo served as plain text with nosniff, never rendered as HTML
}

// Register adds the integration routes (PRD §10 Integration).
func (h *HTTP) Register(reg *route.Registry) {
	const tag = "Integrations"
	add := func(rt route.Route) { rt.Module = "platform"; rt.Tag = tag; reg.Add(rt) }
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/integrations/adapters", Summary: "Available adapters",
		Permission: "platform.integration.view", Response: AdapterInfo{}, List: true, Handler: h.listAdapters})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/integrations", Summary: "List integrations",
		Permission: "platform.integration.view", Response: Integration{}, List: true, Handler: h.list})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/integrations", Summary: "Add integration",
		Permission: "platform.integration.manage", Request: CreateIntegrationRequest{}, Response: Integration{}, Handler: h.create})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/integrations/{id}", Summary: "View integration",
		Permission: "platform.integration.view", Response: Integration{}, Handler: h.get})
	add(route.Route{Method: http.MethodPatch, Path: "/api/v1/platform/integrations/{id}", Summary: "Edit integration (credentials stored encrypted)",
		Permission: "platform.integration.manage", Request: UpdateIntegrationRequest{}, Response: Integration{}, Handler: h.update})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/integrations/{id}:test", Summary: "Test connection",
		Permission: "platform.integration.test", Response: TestResult{}, Status: http.StatusOK, Handler: h.test})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/integrations/{id}:rotate-webhook-secret", Summary: "Rotate webhook signing secret",
		Permission: "platform.integration.manage", Response: WebhookSecretResponse{}, Status: http.StatusOK, Handler: h.rotateWebhookSecret})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/integration-logs", Summary: "Search integration logs",
		Permission: "platform.integration_log.view", Response: IntegrationLog{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[integrationCode]"}, {Name: "filter[direction]"}, {Name: "filter[success]"}}, Handler: h.logs})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/webhooks/{integration}", Summary: "Inbound webhook (signature verified)",
		Auth: route.AuthSignature, Response: WebhookAck{}, Status: http.StatusOK, Handler: h.webhook})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/webhooks/{integration}", Summary: "Webhook subscription handshake (e.g. WhatsApp Cloud API verify token)",
		Auth: route.AuthSignature, RawContent: "text/plain", Handler: h.webhookChallenge})
	h.registerBridge(reg)
}
