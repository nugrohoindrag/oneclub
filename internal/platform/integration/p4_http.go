package integration

// Settings → Integrations additions of PRD P4 EP-25: the payment routing
// table (method × property → integration, FR-INT-P4-01), the gateway
// settlement per payment method of one payment integration (fees and net,
// the input of the payment / bank reconciliation), the sender domain check
// of an e-mail integration (SPF / DKIM / DMARC, FR-INT-P4-05) and the
// hardware profiles of the bridge agent (FR-INT-P4-04).

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
)

// P4HTTP exposes the P4 integration endpoints.
type P4HTTP struct {
	Svc *Service
}

// PaymentRoutesView is the routing table.
type PaymentRoutesView struct {
	Methods []string              `json:"methods"`
	Routes  []GatewayPaymentRoute `json:"routes"`
}

// HardwareProfilesView lists the device profiles of the bridge agent.
type HardwareProfilesView struct {
	Profiles []HardwareProfile `json:"profiles"`
}

// Register adds the routes.
func (h *P4HTTP) Register(reg *route.Registry) {
	add := func(rt route.Route) { rt.Module, rt.Tag = "platform", "Integrations"; reg.Add(rt) }
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/payment-routes",
		Summary:    "Payment routing: the integration serving each payment method at each property (settings methods / properties / priority)",
		Permission: "platform.integration.view", Response: PaymentRoutesView{}, Handler: h.paymentRoutes})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/integrations/{id}/settlement",
		Summary:    "Gateway settlement per payment method (count, gross, fees, net; settled / pending / failed) for the reconciliation",
		Permission: "platform.integration.view", Response: PaymentSettlementSummary{},
		Query: []route.Param{{Name: "from", Description: "YYYY-MM-DD (default: yesterday)"}, {Name: "to", Description: "YYYY-MM-DD inclusive (default: from)"},
			{Name: "items", Type: "boolean", Description: "Include the transactions"}}, Handler: h.settlement})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/integrations/{id}:check-email-domain",
		Summary:    "Check the sender domain of an e-mail integration: SPF (provider included), DKIM per selector and DMARC",
		Permission: "platform.integration.test", Request: EmailDomainCheckInput{}, Response: EmailDomainReport{}, Status: http.StatusOK, Handler: h.checkEmailDomain})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/hardware-profiles",
		Summary:    "Device profiles of the bridge agent (locker, turnstile, ball dispenser, golf cart GPS): commands, payload and results",
		Permission: "platform.bridge_agent.view", Response: HardwareProfilesView{}, Handler: h.hardwareProfiles})
}

func (h *P4HTTP) paymentRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := h.Svc.PaymentRoutes(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, PaymentRoutesView{Methods: PaymentMethods, Routes: routes})
}

func (h *P4HTTP) hardwareProfiles(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, HardwareProfilesView{Profiles: HardwareProfiles})
}

// loadRecord reads one integration.
func (h *P4HTTP) loadRecord(r *http.Request) (record, error) {
	iid, err := httpx.PathUUID(r, "id")
	if err != nil {
		return record{}, err
	}
	rec, err := h.Svc.load(r.Context(), h.Svc.DB.Primary, `id = $1`, iid)
	if dbtx.IsNoRows(err) {
		return rec, errs.NotFound("integration")
	}
	return rec, err
}

func (h *P4HTTP) settlement(w http.ResponseWriter, r *http.Request) {
	rec, err := h.loadRecord(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if rec.Capability != CapPayment {
		httpx.WriteError(w, r, errs.Validation("not_payment", "not a payment integration", errs.Field("id", "invalid", "choose a payment integration")))
		return
	}
	q := r.URL.Query()
	today := clock.Now().UTC().Truncate(24 * time.Hour)
	from, to := today.AddDate(0, 0, -1), today.AddDate(0, 0, -1)
	if s := q.Get("from"); s != "" {
		if from, err = time.Parse("2006-01-02", s); err != nil {
			httpx.WriteError(w, r, errs.Validation("invalid_date", "invalid date", errs.Field("from", "invalid_date", "YYYY-MM-DD")))
			return
		}
		to = from
	}
	if s := q.Get("to"); s != "" {
		if to, err = time.Parse("2006-01-02", s); err != nil {
			httpx.WriteError(w, r, errs.Validation("invalid_date", "invalid date", errs.Field("to", "invalid_date", "YYYY-MM-DD")))
			return
		}
	}
	if to.Before(from) || to.Sub(from) > 31*24*time.Hour {
		httpx.WriteError(w, r, errs.Validation("invalid_period", "invalid period", errs.Field("to", "invalid", "to on or after from, at most 31 days")))
		return
	}
	adapter, err := h.Svc.instantiate(r.Context(), rec)
	if err != nil {
		httpx.WriteError(w, r, errs.Conflict("integration_unavailable", err.Error()))
		return
	}
	d, ok := adapter.(SettlementDetailer)
	if !ok {
		httpx.WriteError(w, r, errs.Conflict("not_supported", "this payment adapter reports no settlement per method"))
		return
	}
	end := to.AddDate(0, 0, 1)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	items, err := d.SettlementDetails(ctx, from, end)
	if err != nil {
		httpx.WriteError(w, r, errs.Conflict("gateway_error", "the gateway did not answer: "+err.Error()))
		return
	}
	httpx.JSON(w, http.StatusOK, SummarizeSettlement(rec.Code, from, end, items, q.Get("items") == "true"))
}

func (h *P4HTTP) checkEmailDomain(w http.ResponseWriter, r *http.Request) {
	var in EmailDomainCheckInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rec, err := h.loadRecord(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if rec.Capability != CapEmail {
		httpx.WriteError(w, r, errs.Validation("not_email", "not an e-mail integration", errs.Field("id", "invalid", "choose an e-mail integration")))
		return
	}
	creds, err := h.Svc.credentials(rec)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	domain, selectors, include := emailDomainDefaults(rec.Adapter, rec.Settings, creds, in)
	if domain == "" {
		httpx.WriteError(w, r, errs.Validation("domain_required", "sender domain unknown", errs.Field("domain", "required", "set the from address or the domain")))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	start := time.Now()
	checks, err := CheckEmailDomain(ctx, domain, selectors, include)
	rid := rec.ID
	h.Svc.LogCall(r.Context(), &rid, rec.Code, Call{Operation: "check_email_domain", Method: "DNS", URL: "dns:" + strings.ToLower(domain),
		Request: map[string]any{"domain": domain, "dkimSelectors": selectors, "spfInclude": include}, Response: map[string]any{"checks": len(checks)},
		Err: err, Duration: time.Since(start)})
	if err != nil {
		if strings.Contains(err.Error(), "is not a domain name") {
			httpx.WriteError(w, r, errs.Validation("invalid_domain", err.Error(), errs.Field("domain", "invalid", err.Error())))
			return
		}
		httpx.WriteError(w, r, errs.Conflict("dns_error", "the DNS lookup failed: "+err.Error()))
		return
	}
	out := EmailDomainReport{IntegrationCode: rec.Code, Domain: strings.ToLower(domain), OK: true, Checks: checks, CheckedAt: clock.Now()}
	for _, c := range checks {
		if c.Status != "pass" && c.Status != "warning" {
			out.OK = false
		}
	}
	err = h.Svc.DB.WithTx(r.Context(), func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, audit.Entry{Module: "platform", Action: "check_email_domain", EntityType: "platform.integration",
			EntityID: rec.ID.String(), EntityLabel: rec.Name, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
