package analytics

// Routes of CRM analytics (PRD P5 §11: GET /crm/analytics/{nps|sla|sales|rfm|clv})
// and VIP segmentation, with their catalogue.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
)

// Permissions of CRM analytics and VIP.
func Permissions() []catalog.Permission {
	return append(catalog.P("crm", "analytics", "view", "refresh"), catalog.P("crm", "vip", "view", "manage")...)
}

// RolePermissions grant analytics to CRM, sales and management and the VIP
// marker to the check-in / POS roles.
func RolePermissions() map[string][]string {
	all := []string{"crm.analytics.view", "crm.analytics.refresh", "crm.vip.view", "crm.vip.manage"}
	mgmt := []string{"crm.analytics.view", "crm.vip.view", "crm.vip.manage"}
	desk := []string{"crm.vip.view"}
	return map[string][]string{
		"property_admin": all, "crm_admin": all, "marketing_staff": all,
		"general_manager": mgmt, "club_manager": mgmt, "membership_manager": mgmt,
		"sales_executive": {"crm.analytics.view", "crm.vip.view"}, "finance_manager": {"crm.analytics.view"},
		"front_desk": desk, "sport_club_receptionist": desk, "reservation_staff": desk, "cashier": desk, "pos_staff": desk, "outlet_manager": desk,
	}
}

func period(ctx context.Context, tx pgx.Tx, r *http.Request, days int) (Period, error) {
	return ParsePeriod(r.URL.Query().Get("from"), r.URL.Query().Get("to"), localToday(ctx, tx, handle.Property(ctx)), days)
}

func optDate(r *http.Request, name string) (*time.Time, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	d, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, handle.Invalid(name, "invalid_date", "YYYY-MM-DD")
	}
	return &d, nil
}

// Register adds the analytics and VIP routes (wired by internal/app).
func (s *Service) Register(reg *route.Registry) {
	db := s.DB
	add := func(rt route.Route) {
		rt.Module, rt.Scope = "crm", route.ScopeProperty
		if rt.Tag == "" {
			rt.Tag = "CRM Analytics"
		}
		reg.Add(rt)
	}
	pq := []route.Param{{Name: "from", Description: "YYYY-MM-DD"}, {Name: "to", Description: "YYYY-MM-DD"}}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/dashboard", Tag: "CRM Dashboard", Summary: "CRM Dashboard Overview: members, engagement, membership, value and the action center",
		Permission: "crm.analytics.view", Response: CRMDashboard{},
		Query: []route.Param{{Name: "period", Enum: []string{"month", "quarter", "year"}}, {Name: "type", Description: "Membership type name"},
			{Name: "activeDays", Description: "Active: an activity within N days (default 90)"}, {Name: "occasionalDays", Description: "Occasional: the last activity within N days (default 180)"},
			{Name: "expiringDays", Description: "Expiring: the membership ends within N days (default 30)"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CRMDashboard, error) {
			f, err := dashboardFilter(ctx, tx, r)
			if err != nil {
				return CRMDashboard{}, err
			}
			return Dashboard(ctx, tx, handle.Property(ctx), f)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/dashboard/members", Tag: "CRM Dashboard", Summary: "Members behind a CRM dashboard widget (expiring, at risk, high value)",
		Permission: "crm.analytics.view", Response: DashMember{}, List: true,
		Query: []route.Param{{Name: "list", Enum: []string{"expiring", "at_risk", "high_value"}, Required: true}, {Name: "days", Description: "Expiring within N days (default 90)"},
			{Name: "type", Description: "Membership type name"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[DashMember], error) {
			f, err := dashboardFilter(ctx, tx, r)
			if err != nil {
				return httpx.Page[DashMember]{}, err
			}
			return handle.Page(DashboardMembers(ctx, tx, handle.Property(ctx), f, r.URL.Query().Get("list"), handle.QueryInt(r, "days", 90), handle.QueryInt(r, "limit", 200)))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/analytics/rfm", Summary: "RFM groups and cross-business mix (latest snapshot)",
		Permission: "crm.analytics.view", Response: RFMSummary{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (RFMSummary, error) {
			return RFM(ctx, tx, handle.Property(ctx))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/analytics/rfm/customers", Summary: "RFM scores of customers", Permission: "crm.analytics.view",
		Response: RFMCustomer{}, List: true, Query: []route.Param{{Name: "filter[group]"}, {Name: "order", Enum: []string{"monetary", "clv"}}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RFMCustomer], error) {
			lp := httpx.ParseList(r)
			return handle.Page(RFMCustomers(ctx, tx, handle.Property(ctx), lp.Filters["group"], r.URL.Query().Get("order"), lp.Limit))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/analytics/rfm/movement", Summary: "Movement between RFM groups of two snapshots",
		Permission: "crm.analytics.view", Response: RFMMovement{}, Query: pq,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (RFMMovement, error) {
			from, err := optDate(r, "from")
			if err != nil {
				return RFMMovement{}, err
			}
			to, err := optDate(r, "to")
			if err != nil {
				return RFMMovement{}, err
			}
			return Movement(ctx, tx, handle.Property(ctx), from, to)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/analytics:refresh", Summary: "Refresh RFM, behaviour per line and VIPs now",
		Permission: "crm.analytics.refresh", Response: AnalyticsRefresh{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (AnalyticsRefresh, error) {
			pid := handle.Property(ctx)
			out, err := s.Refresh(ctx, tx, pid)
			if err != nil {
				return out, err
			}
			return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "refresh", EntityType: "crm.analytics", EntityID: pid.String(),
				EntityLabel: "CRM analytics " + out.AsOf, PropertyID: &pid, After: out})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/analytics/clv", Summary: "Customer lifetime value and cohort retention",
		Permission: "crm.analytics.view", Response: CLVAnalytics{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CLVAnalytics, error) {
			return CLVCohorts(ctx, tx, handle.Property(ctx))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/analytics/nps", Summary: "NPS trend, drivers and retention", Permission: "crm.analytics.view",
		Response: NPSAnalytics{}, Query: append(append([]route.Param{}, pq...), route.Param{Name: "businessLine"}),
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (NPSAnalytics, error) {
			p, err := period(ctx, tx, r, 90)
			if err != nil {
				return NPSAnalytics{}, err
			}
			return NPS(ctx, tx, handle.Property(ctx), p, r.URL.Query().Get("businessLine"))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/analytics/sla", Summary: "Complaint SLA compliance, resolution time, recurring categories",
		Permission: "crm.analytics.view", Response: SLAAnalytics{}, Query: pq,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (SLAAnalytics, error) {
			p, err := period(ctx, tx, r, 90)
			if err != nil {
				return SLAAnalytics{}, err
			}
			return SLA(ctx, tx, handle.Property(ctx), p)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/analytics/sales", Summary: "Sales funnel, win / loss, forecast vs actual and commission",
		Permission: "crm.analytics.view", Response: SalesAnalytics{}, Query: pq,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (SalesAnalytics, error) {
			p, err := period(ctx, tx, r, 180)
			if err != nil {
				return SalesAnalytics{}, err
			}
			return Sales(ctx, tx, handle.Property(ctx), p)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/analytics/customers/{id}", Summary: "Cross-business behaviour, RFM history and VIP of a customer",
		Permission: "crm.analytics.view", Response: CustomerBehavior{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CustomerBehavior, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CustomerBehavior{}, err
			}
			pid := handle.Property(ctx)
			if ok, err := crm.ExistsInProperty(ctx, tx, pid, cid); err != nil || !ok {
				if err == nil {
					err = handle.Invalid("id", "not_found", "customer not found in this property")
				}
				return CustomerBehavior{}, err
			}
			return Behavior(ctx, tx, pid, cid)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/analytics/segments", Summary: "Compare segments (members, RFM groups, lines, VIP)",
		Permission: "crm.analytics.view", Response: SegmentComparison{}, List: true,
		Query: []route.Param{{Name: "segmentIds", Required: true, Description: "Comma-separated segment ids"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[SegmentComparison], error) {
			var ids []uuid.UUID
			for _, v := range strings.Split(r.URL.Query().Get("segmentIds"), ",") {
				if v = strings.TrimSpace(v); v == "" {
					continue
				}
				u, err := uuid.Parse(v)
				if err != nil {
					return httpx.Page[SegmentComparison]{}, handle.Invalid("segmentIds", "invalid_uuid", "segment ids must be UUIDs")
				}
				ids = append(ids, u)
			}
			if len(ids) == 0 {
				return httpx.Page[SegmentComparison]{}, handle.Invalid("segmentIds", "required", "choose at least one segment")
			}
			return handle.Page(CompareSegments(ctx, tx, handle.Property(ctx), ids))
		})})
	// VIP (FR-SEG-03).
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/vip", Summary: "VIP customers", Permission: "crm.vip.view", Tag: "VIP", Response: VIPCustomer{},
		List: true, Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[level]"}, {Name: "filter[source]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[VIPCustomer], error) {
			lp := httpx.ParseList(r)
			return handle.Page(VIPs(ctx, tx, handle.Property(ctx), lp.Filters["status"], lp.Filters["level"], lp.Filters["source"], lp.Q, lp.Limit))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/vip", Summary: "Mark a customer as VIP (manual)", Permission: "crm.vip.manage", Tag: "VIP",
		Request: VIPInput{}, Response: VIPCustomer{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in VIPInput) (VIPCustomer, error) {
			pid := handle.Property(ctx)
			v, err := SetVIP(ctx, tx, pid, in)
			if err != nil {
				return v, err
			}
			return v, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "set_vip", EntityType: "crm.vip_customer", EntityID: v.ID.String(),
				EntityLabel: v.CustomerName, PropertyID: &pid, Reason: in.Reason, After: v})
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/vip/{id}:deactivate", Summary: "End a VIP status", Permission: "crm.vip.manage", Tag: "VIP",
		Request: VIPDeactivateInput{}, Response: VIPCustomer{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in VIPDeactivateInput) (VIPCustomer, error) {
			vid, err := handle.ID(r)
			if err != nil {
				return VIPCustomer{}, err
			}
			pid := handle.Property(ctx)
			before, after, err := DeactivateVIP(ctx, tx, pid, vid, in.Reason)
			if err != nil {
				return after, err
			}
			return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.vip_customer",
				EntityID: vid.String(), EntityLabel: after.CustomerName, PropertyID: &pid, Reason: in.Reason, Before: map[string]any{"status": before.Status},
				After: map[string]any{"status": after.Status}})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/vip-flags/{customerId}", Summary: "VIP marker of a customer (check-in / POS)",
		Permission: "crm.vip.view", Tag: "VIP", Response: VIPFlag{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (VIPFlag, error) {
			cid, err := handle.ID(r, "customerId")
			if err != nil {
				return VIPFlag{}, err
			}
			pid := handle.Property(ctx)
			if ok, err := crm.ExistsInProperty(ctx, tx, pid, cid); err != nil || !ok {
				if err == nil {
					err = handle.Invalid("customerId", "not_found", "customer not found in this property")
				}
				return VIPFlag{}, err
			}
			return Flag(ctx, tx, pid, cid)
		})})
}
