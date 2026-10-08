// Package app is the composition root: it wires every module, route,
// worker, periodic job and event subscriber (Technical Doc §5.1).
package app

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/crm"
	"oneclub/internal/golf"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/ratelimit"
	"oneclub/internal/kernel/route"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/membership"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/httpapi"
	"oneclub/internal/platform/iam"
	"oneclub/internal/platform/instance"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/maintenance"
	"oneclub/internal/platform/navigation"
	"oneclub/internal/platform/notification"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/realtime"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/rules"
	"oneclub/internal/platform/storage"
	syncsvc "oneclub/internal/platform/sync"
	"oneclub/internal/procurement"
	"oneclub/internal/reporting"
	"oneclub/internal/reservation"
	"oneclub/internal/sportclub"
)

// Version is set at build time (-ldflags "-X oneclub/internal/app.Version=...").
var Version = "dev"

// Contributions are the module catalogue contributions.
func Contributions() []catalog.Contribution {
	return append([]catalog.Contribution{
		reporting.Contribution(), golf.Contribution(), billing.Contribution(), commercial.Contribution(),
		crm.Contribution(), membership.Contribution(), sportclub.Contribution(), reservation.Contribution(), procurement.Contribution(),
	}, append(p2Contributions(), p3Contributions()...)...)
}

// Flags are the default feature flags (FR-INS-05).
var Flags = []provision.Flag{
	{Key: org.VenueActivationFlag, Description: "New venues require the Venue Activation approval before becoming Active", Type: "boolean", Default: true, ClientVisible: true},
	{Key: "ui.theme_switch", Description: "Users may switch between Light and Dark mode", Type: "boolean", Default: true, ClientVisible: true},
	{Key: "ops.offline_enabled", Description: "Operational Staff app works offline with a sync queue", Type: "boolean", Default: true, ClientVisible: true},
	{Key: "member.self_registration", Description: "Member Portal shows Create an Account (P1)", Type: "boolean", Default: false, ClientVisible: true},
}

// Reports registered in P0, P1, P2, P3 and P4.
var Reports = append(append(append(append([]*reporting.Report{reporting.UserAccessReport, reporting.VenueDirectoryReport}, reporting.P1Reports...),
	reporting.P2Reports...), reporting.P3Reports()...), reporting.P4Reports()...)

// DocumentTypes registered by the modules (approval engine, FR-APR-09).
var DocumentTypes = []provision.DocumentType{approval.TestDocumentType, org.VenueActivationType, billing.RefundDocumentType,
	membership.ApplicationDocumentType, golf.PriceOverrideType, golf.CancellationWaiverType}

func init() {
	DocumentTypes = append(DocumentTypes, p2DocumentTypes...)
	DocumentTypes = append(DocumentTypes, p3DocumentTypes()...) // PRD P3
}

// Seeds builds the catalogue synchronised into instance databases.
func Seeds() (provision.Seeds, error) {
	cat, err := catalog.Build(Contributions()...)
	if err != nil {
		return provision.Seeds{}, err
	}
	rs := &reporting.Service{}
	for _, r := range Reports {
		rs.Add(r)
	}
	return provision.Seeds{Catalog: cat, DocumentTypes: DocumentTypes, Reports: rs.Seeds(), Templates: append(append(notification.DefaultTemplates(), p2Templates()...), p3Templates()...), Flags: Flags}, nil
}

// App holds the wired application.
type App struct {
	Cfg          *config.Config
	DB           *dbtx.DB
	Catalog      *catalog.Catalog
	Registry     *route.Registry
	Server       *httpapi.Server
	Jobs         *jobs.Client
	Registrar    *jobs.Registrar
	Bus          *outbox.Bus
	IAM          *iam.Service
	Instance     *instance.Service
	Notification *notification.Service
	Approvals    *approval.Engine
	Integrations *integration.Service
	Engine       *resource.Engine
	Reporting    *reporting.Service
	Dispatcher   *outbox.Dispatcher
	Navigation   *navigation.Service
	Org          *org.Module
	Box          *secret.Box
	Hub          *realtime.Hub
	Billing      *billing.Service
	Golf         *golf.Module
	Membership   *membership.Module
	Sync         *syncsvc.Service
	P2
	P3
	P4
	P5
}

// Options control process-specific wiring.
type Options struct {
	Worker      bool          // run job queues (oneclub worker)
	TestOnly    bool          // River test mode
	RetryBase   time.Duration // notification retry base (tests use small values)
	Concurrency int
}

// Build wires everything. db may be nil to only build routes (openapi).
func Build(cfg *config.Config, db *dbtx.DB, o Options) (*App, error) {
	seeds, err := Seeds()
	if err != nil {
		return nil, err
	}
	secretVal := cfg.AppSecret
	if len(secretVal) < 32 {
		secretVal = "openapi-generation-only-secret-000000000000"
	}
	box, err := secret.NewBox(secretVal)
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, DB: db, Catalog: seeds.Catalog, Registry: route.NewRegistry(), Registrar: jobs.NewRegistrar(), Bus: outbox.NewBus(), Box: box}

	a.Instance = &instance.Service{DB: db}
	a.Integrations = &integration.Service{DB: db, Box: box, Cfg: cfg}
	a.Notification = &notification.Service{DB: db, Integrations: a.Integrations, Cfg: cfg}
	a.IAM = iam.New(db, cfg, box, a.Notification, a.Catalog)
	a.Approvals = approval.New(db, a.Notification, a.Bus, cfg)
	a.Engine = resource.NewEngine(db)
	a.Org = &org.Module{DB: db, Engine: a.Engine, Approvals: a.Approvals, Events: a.Bus, Flags: a.Instance}
	a.Approvals.RegisterDocumentType(approval.TestDocumentType, nil)
	a.Approvals.RegisterDocumentType(org.VenueActivationType, a.Org.VenueDecision)
	a.Navigation = &navigation.Service{DB: db, Modules: a.Instance}

	var blob storage.Blob
	if db != nil {
		if blob, err = storage.New(cfg.Storage, cfg.InstanceCode); err != nil {
			return nil, fmt.Errorf("storage: %w", err)
		}
	}
	files := &storage.Files{DB: db, Blob: blob}
	// photos of master data records (Image fields): whoever may add or edit the resource
	files.CanUploadImage = func(ctx context.Context, key string) bool {
		d, ok := a.Engine.Def(key)
		if !ok || !slices.ContainsFunc(d.Fields, func(f resource.Field) bool { return f.Kind == resource.Image }) {
			return false
		}
		p := authz.From(ctx)
		return p.Can(d.Perm+".create", nil) || p.Can(d.Perm+".update", nil)
	}
	a.Reporting = &reporting.Service{DB: db, Files: files, Notify: a.Notification, Cfg: cfg, Location: a.Instance.Location}
	for _, r := range Reports {
		a.Reporting.Add(r)
	}

	// Routes.
	reg := a.Registry
	a.IAM.Register(reg)
	a.Instance.Register(reg)
	a.Org.Register(reg)
	(&notification.HTTP{Svc: a.Notification}).Register(reg)
	(&approval.HTTP{E: a.Approvals, Files: files}).Register(reg)
	(&audit.HTTP{DB: db}).Register(reg)
	(&integration.HTTP{Svc: a.Integrations, Events: a.Bus}).Register(reg)
	(&rules.Service{DB: db}).Register(reg)
	files.Register(reg)
	a.Sync = syncsvc.New(db)
	a.Sync.Register(reg)
	a.Navigation.Register(reg)
	a.Reporting.Register(reg)
	a.Engine.RegisterImports(reg)
	(&billing.Module{DB: db}).Register(reg, a.Engine)
	(&commercial.Module{DB: db}).Register(reg, a.Engine)
	for _, d := range []*resource.Def{sportclub.Facilities, reservation.Resources, procurement.Suppliers} {
		a.Engine.Register(reg, d)
	}

	// P1 Golf Core MVP modules (layers: crm → billing/commercial/membership → golf).
	a.Hub = &realtime.Hub{}
	if db != nil {
		a.Hub.Pool = db.Primary
	}
	portalURL := func() string { return cfg.MemberPortalURL }
	(&crm.Module{DB: db, Events: a.Bus, Files: files}).Register(reg, a.Engine)
	a.Billing = &billing.Service{DB: db, Events: a.Bus, Gateways: a.Integrations}
	billingHTTP := &billing.HTTP{Svc: a.Billing, Approvals: a.Approvals, Notify: a.Notification, Files: files, PublicURL: portalURL,
		Holder: func(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (*uuid.UUID, error) {
			mid, err := membership.MemberByUser(ctx, tx, userID)
			if err != nil || mid == nil {
				return nil, err
			}
			return membership.AccountHolder(ctx, tx, *mid)
		}}
	billingHTTP.Register(reg)
	billingHTTP.RegisterMember(reg)
	a.Approvals.RegisterDocumentType(billing.RefundDocumentType, billingHTTP.RefundDecision)
	a.Membership = &membership.Module{DB: db, Events: a.Bus, Approvals: a.Approvals, Billing: a.Billing, Notify: a.Notification, Portal: a.IAM,
		Residents: a.Integrations, PortalURL: portalURL}
	a.Membership.Register(reg, a.Engine)
	a.Approvals.RegisterDocumentType(membership.ApplicationDocumentType, a.Membership.ApplicationDecision)
	a.Golf = &golf.Module{DB: db, Events: a.Bus, Approvals: a.Approvals, Billing: a.Billing, Refunds: billingHTTP, Notify: a.Notification, Hub: a.Hub,
		Integrations: a.Integrations, Cfg: cfg, Limiter: ratelimit.New()}
	a.Golf.Register(reg, a.Engine)
	a.Golf.RegisterSync(a.Sync)
	a.Approvals.RegisterDocumentType(golf.PriceOverrideType, a.Golf.OverrideDecision)
	a.Approvals.RegisterDocumentType(golf.CancellationWaiverType, a.Golf.WaiverDecision)
	a.buildP2(reg, cfg, db, files, billingHTTP)
	a.buildP3(reg, cfg, db, files, billingHTTP)

	// Workers and schedules.
	a.Dispatcher = &outbox.Dispatcher{DB: db, Bus: a.Bus}
	river.AddWorker(a.Registrar.Workers, a.Dispatcher)
	river.AddWorker(a.Registrar.Workers, &notification.Worker{Svc: a.Notification, RetryBase: o.RetryBase})
	river.AddWorker(a.Registrar.Workers, &reporting.ExportWorker{Svc: a.Reporting, LoadAuthz: a.LoadPrincipal})
	a.Registrar.Periodic = append(a.Registrar.Periodic, outbox.PeriodicDispatch())
	maintenance.Register(a.Registrar, &maintenance.Deps{DB: db, Notify: a.Notification, Approvals: a.Approvals, Cfg: cfg, Location: a.Instance.Location})
	reservation.RegisterJobs(a.Registrar, db)
	billing.RegisterJobs(a.Registrar, a.Billing, files, func() billing.StatementDeps {
		return billing.StatementDeps{Files: files, Notify: a.Notification, PortalURL: cfg.MemberPortalURL}
	}, a.Instance.Location)
	a.Membership.RegisterJobs(a.Registrar, a.Instance.Location)
	a.Golf.RegisterJobs(a.Registrar, a.Instance.Location)

	a.subscribe()
	a.subscribeP2()
	a.subscribeP3()

	if db != nil {
		jc, err := jobs.New(db.Primary, a.Registrar, jobs.Options{Process: o.Worker, Concurrency: o.Concurrency, TestOnly: o.TestOnly,
			Logger: slog.Default(), OnFailure: onJobFailure})
		if err != nil {
			return nil, err
		}
		a.Jobs = jc
		a.Bus.Jobs = jc
		a.Notification.Jobs = jc
		a.Reporting.Jobs = jc
		(&jobs.Admin{Client: jc, DB: db}).Register(reg)
	} else {
		(&jobs.Admin{}).Register(reg)
	}

	spec, err := httpapiSpec(reg)
	if err != nil {
		return nil, err
	}
	a.Server = &httpapi.Server{Cfg: cfg, DB: db, Registry: reg, Auth: a.IAM, Gate: a.Instance, OpenAPI: spec}
	return a, nil
}

func onJobFailure(ctx context.Context, job *rivertype.JobRow, err error, final bool) {
	lvl := slog.LevelWarn
	if final {
		lvl = slog.LevelError
	}
	slog.Log(ctx, lvl, "job failed", "kind", job.Kind, "job_id", job.ID, "attempt", job.Attempt, "max_attempts", job.MaxAttempts, "final", final, "err", err)
}

// LoadPrincipal loads a user's principal (for jobs acting on behalf of a user).
func (a *App) LoadPrincipal(ctx context.Context, userID uuid.UUID) (*authz.Principal, error) {
	return a.IAM.PrincipalFor(ctx, userID)
}

// subscribe registers in-process outbox subscribers.
func (a *App) subscribe() {
	// Payment gateway webhooks settle payments (FR-PAY-03); WhatsApp
	// delivery statuses update the notification history (FR-INT-P1-02).
	a.Bus.Subscribe("integration.webhook_received", "billing.settle_payment", a.Billing.OnWebhook)
	a.Bus.Subscribe("integration.webhook_received", "notification.delivery_status", a.Notification.OnDeliveryStatus)
	// Paid folios confirm golf bookings and activate / renew memberships.
	a.Bus.Subscribe(billing.EventPaymentSettled, "golf.confirm_booking", a.Golf.OnPaymentSettled)
	a.Bus.Subscribe(billing.EventPaymentSettled, "membership.activate", a.Membership.OnPaymentSettled)
	// CRM identity changes follow into golf records.
	a.Bus.Subscribe(crm.EventGuestUpgraded, "golf.guest_upgraded", a.Golf.OnGuestUpgraded)
	a.Bus.Subscribe(crm.EventCustomerMerged, "golf.customer_merged", a.Golf.OnCustomerMerged)
	a.Bus.Subscribe(crm.EventCustomerErased, "golf.customer_erased", a.Golf.OnCustomerErased)

	// Vertical slice (PRD §8): an activated venue is announced through the
	// messaging integration (mock adapter in sandbox).
	a.Bus.Subscribe("platform.venue_activated", "integration.announce_venue", func(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
		var p struct {
			VenueCode, VenueName string
		}
		if err := e.Decode(&p); err != nil {
			return err
		}
		m, err := a.Integrations.Messaging(ctx)
		if err == integration.ErrNotConfigured {
			slog.InfoContext(ctx, "venue activated; no messaging integration configured", "venue", p.VenueCode)
			return nil
		}
		if err != nil {
			return err
		}
		_, err = m.SendMessage(ctx, integration.OutboundMessage{To: "+620000000000", Template: "venue_activated", Language: "id",
			Parameters: []string{p.VenueName, p.VenueCode}, Text: "Venue " + p.VenueName + " is now Active"})
		return err
	})
}
