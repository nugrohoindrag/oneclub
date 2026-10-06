package httpapi

// Read logging for auditors (PRD P4 §16 #18 "seluruh akses tercatat di
// audit log", NFR Audit): every permitted read of a read-audited module
// (Accounting, Reports, Audit) by a principal holding a read-audited role
// (Auditor) is written to the audit log as a security "view" event with
// the route and its query. The entry is written before the handler runs
// and a failure to write it refuses the read (fail closed).

import (
	"context"
	"log/slog"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
)

// ActionView is the audit action of a logged read.
const ActionView = "view"

// readAudited reports whether a read of rt by p must be logged.
func readAudited(rt *route.Route, p *authz.Principal) bool {
	if rt.Method != http.MethodGet || p == nil || !slices.Contains(catalog.ReadAuditedModules, rt.Module) {
		return false
	}
	for _, code := range catalog.ReadAuditedRoles {
		if p.HasRole(code) {
			return true
		}
	}
	return false
}

// logRead writes the audit entry of a read.
func (s *Server) logRead(ctx context.Context, r *http.Request, rt *route.Route) error {
	md := map[string]any{"method": r.Method, "path": r.URL.Path}
	if q := r.URL.RawQuery; q != "" {
		md["query"] = q
	}
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{Module: rt.Module, Action: ActionView, Category: audit.CategorySecurity, EntityType: "route",
			EntityID: rt.ID(), EntityLabel: rt.Summary, Metadata: md})
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit read failed", "route", rt.ID(), "error", err)
	}
	return err
}
