package commercial

// Wiring of PRD P3 EP-10 / EP-11 in the commercial module: routes, the
// permission catalogue with its role templates, the approval document type
// and the periodic jobs (package expiry, promotion expiry). internal/app
// builds the P3Module, registers the component allocators and subscribes
// the events.

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// routeSpec is a compact route declaration of the P3 commercial routes.
type routeSpec struct {
	method, path, summary, perm string
	req, res                    any
	status                      int
	list, idempotent            bool
	noAudit                     string
	query                       []queryParam
	h                           http.HandlerFunc
}

type queryParam struct {
	name, typ, doc string
	required       bool
	enum           []string
}

type routeAdder struct{ reg *route.Registry }

func (rs routeSpec) route() route.Route {
	var q []route.Param
	for _, p := range rs.query {
		q = append(q, route.Param{Name: p.name, Type: p.typ, Required: p.required, Enum: p.enum, Description: p.doc})
	}
	return route.Route{Method: rs.method, Path: rs.path, Summary: rs.summary, Permission: rs.perm, Request: rs.req, Response: rs.res,
		List: rs.list, Status: rs.status, Idempotent: rs.idempotent, NoAudit: rs.noAudit, Query: q, Handler: rs.h}
}

func (a routeAdder) add(rs routeSpec, tag string) {
	rt := rs.route()
	rt.Module, rt.Tag, rt.Scope = "commercial", tag, route.ScopeProperty
	a.reg.Add(rt)
}

func (a routeAdder) public(rs routeSpec) { crm.PublicRoute(a.reg, "commercial", rs.route()) }

func (a routeAdder) member(rs routeSpec) {
	crm.MeRoute(a.reg, "commercial", "Member Portal", rs.route())
}

// readPost runs a read-only POST (evaluation, simulation, checks).
func readPost[Req any, Res any](db *dbtx.DB, fn func(ctx context.Context, tx pgx.Tx, r *http.Request, in Req) (Res, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in Req
		if err := httpx.Decode(r, &in); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := r.Context()
		var out Res
		err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
			var err error
			out, err = fn(ctx, tx, r, in)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// dryRead runs a GET whose checks allocate inside savepoints that are
// rolled back (package availability): a read-write transaction that
// commits nothing.
func dryRead[Res any](db *dbtx.DB, fn func(ctx context.Context, tx pgx.Tx, r *http.Request) (Res, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var out Res
		err := db.WithTx(ctx, func(tx pgx.Tx) error {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = sp.Rollback(ctx) }()
			out, err = fn(ctx, sp, r)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

func publicProperty(r *http.Request) (uuid.UUID, error) {
	pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
	if err != nil {
		return uuid.Nil, errs.BadRequest("property_required", "propertyId is required")
	}
	return pid, nil
}

// publicRead runs an anonymous website read scoped to ?propertyId.
func (m *P3Module) publicRead(fn func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid, err := publicProperty(r)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := crm.PublicCtx(r.Context(), pid)
		var out any
		if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
			out, err = fn(ctx, tx, r, pid)
			return err
		}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// publicDry is publicRead with rolled back allocation checks.
func (m *P3Module) publicDry(fn func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid, err := publicProperty(r)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := crm.PublicCtx(r.Context(), pid)
		var out any
		if err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = sp.Rollback(ctx) }()
			out, err = fn(ctx, sp, r, pid)
			return err
		}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// Register adds the P3 commercial routes and resources.
func (m *P3Module) Register(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{Promotions, PromoCodes, Packages, PackageComponents} {
		eng.Register(reg, d)
	}
	a := routeAdder{reg: reg}
	m.registerPromotions(a, eng)
	m.registerPackages(a)
}

// PackageExpiryArgs expires unpaid package holds, unused components and
// promotions past their validity.
type PackageExpiryArgs struct{}

func (PackageExpiryArgs) Kind() string { return "commercial_package_expiry" }

func (PackageExpiryArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 5}
}

// PackageExpiryWorker runs every 15 minutes.
type PackageExpiryWorker struct {
	river.WorkerDefaults[PackageExpiryArgs]
	M *P3Module
}

func (w *PackageExpiryWorker) Work(ctx context.Context, _ *river.Job[PackageExpiryArgs]) error {
	if _, _, err := w.M.RunPackageExpiry(ctx); err != nil {
		return err
	}
	_, err := w.M.ExpirePromotions(ctx)
	return err
}

// ExpirePromotions moves Active promotions past their validity to Expired.
func (m *P3Module) ExpirePromotions(ctx context.Context) (int64, error) {
	sys := dbtx.System(ctx)
	var n int64
	err := m.DB.WithTx(sys, func(tx pgx.Tx) error {
		tag, err := tx.Exec(sys, `UPDATE commercial.promotions p SET status = 'expired' WHERE status = 'active' AND valid_to IS NOT NULL
			AND valid_to < (now() AT TIME ZONE coalesce((SELECT timezone FROM platform.instance), 'UTC'))::date`)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

// RegisterJobs adds the P3 commercial worker and schedule.
func (m *P3Module) RegisterJobs(reg *jobs.Registrar) {
	river.AddWorker(reg.Workers, &PackageExpiryWorker{M: m})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(river.PeriodicInterval(15*time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return PackageExpiryArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}))
}

// P3DocumentTypes are the approval document types of promotions and packages.
var P3DocumentTypes = []provision.DocumentType{PromotionActivationType}

// P3Contribution is the catalogue of promotions and packages: permissions
// and their role templates (Product Overview §44, PRD P3 §4).
func P3Contribution() catalog.Contribution {
	perms := resource.Permissions(Promotions, PromoCodes, Packages)
	perms = append(perms, catalog.P("commercial", "promotion", "activate", "approve")...)
	perms = append(perms, catalog.P("commercial", "package", "publish")...)
	perms = append(perms, catalog.P("commercial", "package_booking", "view", "create", "cancel", "consume")...)
	perms = append(perms, catalog.P("commercial", "pos", "promotion", "promotion_override")...)
	promoAll := append(resource.AllActions(Promotions, PromoCodes), "commercial.promotion.activate")
	promoView := []string{"commercial.promotion.view", "commercial.promo_code.view"}
	pkgAll := append(resource.AllActions(Packages), "commercial.package.publish")
	bookAll := []string{"commercial.package_booking.view", "commercial.package_booking.create", "commercial.package_booking.cancel",
		"commercial.package_booking.consume"}
	bookSell := []string{"commercial.package.view", "commercial.package_booking.view", "commercial.package_booking.create", "commercial.package_booking.cancel"}
	bookUse := []string{"commercial.package.view", "commercial.package_booking.view", "commercial.package_booking.consume"}
	rp := map[string][]string{}
	grant := func(ps []string, roles ...string) {
		for _, r := range roles {
			rp[r] = append(rp[r], ps...)
		}
	}
	grant(append(append(append(append([]string{}, promoAll...), pkgAll...), bookAll...), "commercial.promotion.approve", "commercial.pos.promotion",
		"commercial.pos.promotion_override"), "property_admin")
	grant(append(append(append([]string{}, promoView...), "commercial.promotion.export", "commercial.promotion.approve", "commercial.package.view",
		"commercial.package.export"), "commercial.package_booking.view"), "general_manager", "finance_manager")
	grant(append(append(append([]string{}, promoAll...), pkgAll...), bookAll...), "club_manager", "resort_manager")
	grant(append(append([]string{}, promoAll...), "commercial.pos.promotion", "commercial.pos.promotion_override", "commercial.package.view",
		"commercial.package_booking.view", "commercial.package_booking.consume"), "outlet_manager")
	grant(append(append([]string{}, promoAll...), "commercial.package.view"), "marketing_staff")
	grant(append(append([]string{}, promoView...), "commercial.promo_code.create", "commercial.package.view"), "crm_admin")
	grant(append([]string{"commercial.promotion.view", "commercial.pos.promotion"}, bookUse...), "cashier", "pos_staff")
	grant(append(append([]string{}, bookSell...), "commercial.package_booking.consume", "commercial.promotion.view"), "reservation_staff", "front_desk")
	grant(append(append([]string{}, bookSell...), "commercial.promotion.view"), "banquet_sales", "banquet_manager", "sales_executive")
	grant(bookUse, "golf_admin", "starter_marshal")
	grant([]string{"commercial.promotion.view", "commercial.promotion.export", "commercial.package.view", "commercial.package_booking.view"}, "accountant")
	grant([]string{"commercial.package.view", "commercial.package_booking.view"}, "golf_manager")
	return catalog.Contribution{Permissions: perms, RolePermissions: rp}
}
