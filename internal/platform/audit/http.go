package audit

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/route"
)

// Log is an audit log entry (FR-AUD-04).
type Log struct {
	ID          uuid.UUID      `json:"id"`
	OccurredAt  time.Time      `json:"occurredAt"`
	ActorType   string         `json:"actorType" enum:"user,api_key,device,system,anonymous"`
	ActorID     *uuid.UUID     `json:"actorId"`
	ActorName   *string        `json:"actorName"`
	ActorRoles  []string       `json:"actorRoles"`
	PropertyID  *uuid.UUID     `json:"propertyId"`
	Module      string         `json:"module"`
	Action      string         `json:"action"`
	Category    string         `json:"category" enum:"data,security,system"`
	EntityType  string         `json:"entityType"`
	EntityID    *string        `json:"entityId"`
	EntityLabel *string        `json:"entityLabel"`
	Before      map[string]any `json:"before"`
	After       map[string]any `json:"after"`
	Changed     []string       `json:"changed"`
	Reason      *string        `json:"reason"`
	IP          *string        `json:"ip"`
	UserAgent   *string        `json:"userAgent"`
	DeviceID    *uuid.UUID     `json:"deviceId"`
	RequestID   *string        `json:"requestId"`
	Metadata    map[string]any `json:"metadata"`
	Masked      bool           `json:"masked" doc:"Personal data masked because the viewer lacks audit.log.view_sensitive"`
}

// ExportRequest filters an audit export.
type ExportRequest struct {
	Filters map[string]string `json:"filters,omitempty"`
	From    *time.Time        `json:"from,omitempty"`
	To      *time.Time        `json:"to,omitempty"`
	Q       string            `json:"q,omitempty"`
}

// HTTP serves Settings → Audit Logs.
type HTTP struct{ DB *dbtx.DB }

const logSelect = `SELECT id, occurred_at, actor_type, actor_id, actor_name, actor_roles, property_id, module, action, category,
	entity_type, entity_id, entity_label, before, after, reason, ip, user_agent, device_id, request_id, metadata FROM audit.audit_log`

var filterCols = map[string]string{
	"module": "module", "action": "action", "category": "category", "entityType": "entity_type", "entityId": "entity_id",
	"actorId": "actor_id::text", "actorType": "actor_type", "propertyId": "property_id::text", "requestId": "request_id",
}

func buildWhere(filters map[string]string, from, to *time.Time, q string) ([]string, []any, error) {
	where := []string{"true"}
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	for k, v := range filters {
		col, ok := filterCols[k]
		if !ok {
			return nil, nil, errs.BadRequest("invalid_filter", "unsupported filter "+k)
		}
		add(col+" = ANY(?)", strings.Split(v, ","))
	}
	if from != nil {
		add("occurred_at >= ?", *from)
	}
	if to != nil {
		add("occurred_at < ?", *to)
	}
	if q != "" {
		add("(entity_label ILIKE ? OR actor_name ILIKE ? OR entity_id ILIKE ? OR reason ILIKE ?)", "%"+q+"%")
	}
	return where, args, nil
}

func parseTime(r *http.Request, key string) (*time.Time, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		if d, derr := time.Parse("2006-01-02", v); derr == nil {
			return &d, nil
		}
		return nil, errs.BadRequest("invalid_time", key+" must be RFC 3339 or YYYY-MM-DD")
	}
	return &t, nil
}

func scanLog(row pgx.Row, sensitive bool) (Log, error) {
	var l Log
	var before, after []byte
	err := row.Scan(&l.ID, &l.OccurredAt, &l.ActorType, &l.ActorID, &l.ActorName, &l.ActorRoles, &l.PropertyID, &l.Module, &l.Action,
		&l.Category, &l.EntityType, &l.EntityID, &l.EntityLabel, &before, &after, &l.Reason, &l.IP, &l.UserAgent, &l.DeviceID, &l.RequestID, &l.Metadata)
	if err != nil {
		return l, err
	}
	if len(before) > 0 {
		_ = json.Unmarshal(before, &l.Before)
	}
	if len(after) > 0 {
		_ = json.Unmarshal(after, &l.After)
	}
	l.Changed = []string{}
	if l.Before != nil && l.After != nil {
		l.Changed = Diff(l.Before, l.After)
	}
	if !sensitive {
		// FR-AUD-06: mask personal data for viewers without the permission.
		l.Before, l.After = mask.Map(l.Before, true), mask.Map(l.After, true)
		if l.IP != nil {
			m := mask.Text(*l.IP)
			l.IP = &m
		}
		l.Masked = true
	}
	if l.ActorRoles == nil {
		l.ActorRoles = []string{}
	}
	return l, nil
}

func (h *HTTP) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	from, err := parseTime(r, "from")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	to, err := parseTime(r, "to")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	where, args, err := buildWhere(lp.Filters, from, to, lp.Q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if lp.Cursor != "" {
		c, err := httpx.DecodeCursor(lp.Cursor)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ts, idPart, _ := strings.Cut(c, "|")
		t, err1 := time.Parse(time.RFC3339Nano, ts)
		u, err2 := uuid.Parse(idPart)
		if err1 != nil || err2 != nil {
			httpx.WriteError(w, r, errs.BadRequest("invalid_cursor", "invalid cursor"))
			return
		}
		args = append(args, t, u)
		where = append(where, "(occurred_at, id) < ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
	}
	sensitive := authz.From(ctx).Can("audit.log.view_sensitive", nil)
	var out []Log
	err = h.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		args = append(args, lp.Limit+1)
		rows, err := tx.Query(ctx, logSelect+" WHERE "+strings.Join(where, " AND ")+" ORDER BY occurred_at DESC, id DESC LIMIT $"+strconv.Itoa(len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanLog(rows, sensitive)
			if err != nil {
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
	httpx.JSON(w, http.StatusOK, httpx.BuildPage(out, lp.Limit, func(l Log) string {
		return l.OccurredAt.UTC().Format(time.RFC3339Nano) + "|" + l.ID.String()
	}))
}

func (h *HTTP) get(w http.ResponseWriter, r *http.Request) {
	lid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	sensitive := authz.From(ctx).Can("audit.log.view_sensitive", nil)
	var out Log
	err = h.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		out, err = scanLog(tx.QueryRow(ctx, logSelect+" WHERE id = $1", lid), sensitive)
		if dbtx.IsNoRows(err) {
			return errs.NotFound("audit log entry")
		}
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// export streams a CSV of matching entries; the export itself is audited
// (FR-AUD-05).
func (h *HTTP) export(w http.ResponseWriter, r *http.Request) {
	var req ExportRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	where, args, err := buildWhere(req.Filters, req.From, req.To, req.Q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	sensitive := authz.From(ctx).Can("audit.log.view_sensitive", nil)
	var logs []Log
	err = h.DB.WithTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, logSelect+" WHERE "+strings.Join(where, " AND ")+" ORDER BY occurred_at DESC, id DESC LIMIT 100000", args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			l, err := scanLog(rows, sensitive)
			if err != nil {
				rows.Close()
				return err
			}
			logs = append(logs, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		return Record(ctx, tx, Entry{Module: "audit", Action: ActionExport, Category: CategorySecurity, EntityType: "audit.audit_log",
			EntityLabel: "Audit Logs", Metadata: map[string]any{"rows": len(logs), "filters": req.Filters, "from": req.From, "to": req.To, "q": req.Q, "masked": !sensitive}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit-logs-`+time.Now().UTC().Format("20060102-150405")+`.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"occurredAt", "actorType", "actorName", "actorRoles", "propertyId", "module", "action", "category", "entityType",
		"entityId", "entityLabel", "changed", "before", "after", "reason", "ip", "requestId"})
	s := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for _, l := range logs {
		b, _ := json.Marshal(l.Before)
		a, _ := json.Marshal(l.After)
		pid := ""
		if l.PropertyID != nil {
			pid = l.PropertyID.String()
		}
		_ = cw.Write([]string{l.OccurredAt.UTC().Format(time.RFC3339), l.ActorType, s(l.ActorName), strings.Join(l.ActorRoles, " "), pid,
			l.Module, l.Action, l.Category, l.EntityType, s(l.EntityID), s(l.EntityLabel), strings.Join(l.Changed, " "), string(b), string(a),
			s(l.Reason), s(l.IP), s(l.RequestID)})
	}
	cw.Flush()
}

// Register adds the audit routes (PRD §10 Audit).
func (h *HTTP) Register(reg *route.Registry) {
	q := []route.Param{{Name: "q"}, {Name: "from", Description: "RFC 3339 or YYYY-MM-DD"}, {Name: "to"}}
	keys := make([]string, 0, len(filterCols))
	for k := range filterCols {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic OpenAPI output (CI drift check)
	for _, k := range keys {
		q = append(q, route.Param{Name: "filter[" + k + "]"})
	}
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/audit/logs", Module: "audit", Tag: "Audit Logs", Summary: "Search audit logs",
		Permission: "audit.log.view", Response: Log{}, List: true, Query: q, Handler: h.list})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/audit/logs/{id}", Module: "audit", Tag: "Audit Logs", Summary: "Audit log entry with before/after",
		Permission: "audit.log.view", Response: Log{}, Handler: h.get})
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/audit/exports", Module: "audit", Tag: "Audit Logs", Summary: "Export audit logs (CSV)",
		Permission: "audit.log.export", Request: ExportRequest{}, RawContent: "text/csv", Status: http.StatusOK, Handler: h.export})
}
