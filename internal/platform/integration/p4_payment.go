package integration

// PRD P4 FR-INT-P4-01: one payment gateway (Xendit, decision §16 #13) for
// QRIS, Virtual Account and card, reconciled per method and property. P4
// adds (1) routing of a payment to the payment integration serving the
// method and property — with one gateway every route resolves to it, and a
// later provider is "one more adapter + routing settings" without code
// changes in billing — and (2) settlement details with method, channel,
// fee and net amount, summarised per method for the reconciliation.

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
)

// PaymentMethods are the online payment methods.
var PaymentMethods = []string{"qris", "virtual_account", "card"}

// listSetting reads a list setting given as JSON array or comma-separated text.
func listSetting(settings map[string]any, key string) []string {
	var out []string
	switch v := settings[key].(type) {
	case []any:
		for _, x := range v {
			if s := strings.TrimSpace(fmt.Sprint(x)); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

type paymentCandidate struct {
	code       string
	methods    []string
	properties []string
	priority   int
}

func (s *Service) paymentCandidates(ctx context.Context) ([]paymentCandidate, error) {
	rows, err := s.DB.Primary.Query(ctx, `SELECT code, settings FROM platform.integrations WHERE capability = $1 AND enabled ORDER BY code`, CapPayment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []paymentCandidate
	for rows.Next() {
		var c paymentCandidate
		var settings map[string]any
		if err := rows.Scan(&c.code, &settings); err != nil {
			return nil, err
		}
		c.methods = listSetting(settings, "methods")
		c.properties = listSetting(settings, "properties")
		c.priority = 100
		if p, ok := settings["priority"]; ok {
			if n, err := strconv.Atoi(fmt.Sprint(p)); err == nil {
				c.priority = n
			}
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].priority < out[j].priority })
	return out, rows.Err()
}

func (c paymentCandidate) serves(method, propertyCode string, property uuid.UUID) bool {
	if len(c.methods) > 0 && method != "" && !slices.Contains(c.methods, method) {
		return false
	}
	if len(c.properties) > 0 {
		ok := false
		for _, p := range c.properties {
			if strings.EqualFold(p, propertyCode) || p == property.String() {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// PaymentFor resolves the payment integration for a method at a property:
// enabled payment integrations whose settings "methods" / "properties"
// (empty = all) match, lowest settings "priority" first, then by code.
func (s *Service) PaymentFor(ctx context.Context, method string, property uuid.UUID) (PaymentAdapter, string, error) {
	cands, err := s.paymentCandidates(ctx)
	if err != nil {
		return nil, "", err
	}
	var code string
	err = s.DB.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT code FROM platform.properties WHERE id = $1`, property).Scan(&code)
	})
	if err != nil && !dbtx.IsNoRows(err) {
		return nil, "", err
	}
	for _, c := range cands {
		if !c.serves(method, code, property) {
			continue
		}
		a, err := s.ByCode(ctx, c.code)
		if err != nil {
			return nil, "", err
		}
		p, ok := a.(PaymentAdapter)
		if !ok {
			return nil, "", fmt.Errorf("integration %s does not implement payment", c.code)
		}
		return p, c.code, nil
	}
	return nil, "", ErrNotConfigured
}

// GatewayPaymentRoute is the resolved integration of a method at a property.
type GatewayPaymentRoute struct {
	PropertyID      uuid.UUID `json:"propertyId"`
	PropertyCode    string    `json:"propertyCode"`
	Method          string    `json:"method" enum:"qris,virtual_account,card"`
	IntegrationCode *string   `json:"integrationCode" doc:"null: no enabled payment integration serves this method"`
}

// PaymentRoutes lists the routing table (Settings → Integrations).
func (s *Service) PaymentRoutes(ctx context.Context) ([]GatewayPaymentRoute, error) {
	cands, err := s.paymentCandidates(ctx)
	if err != nil {
		return nil, err
	}
	type prop struct {
		id   uuid.UUID
		code string
	}
	var props []prop
	err = s.DB.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, code FROM platform.properties WHERE status = 'active' AND archived_at IS NULL ORDER BY code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p prop
			if err := rows.Scan(&p.id, &p.code); err != nil {
				return err
			}
			props = append(props, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	out := []GatewayPaymentRoute{}
	for _, p := range props {
		for _, m := range PaymentMethods {
			r := GatewayPaymentRoute{PropertyID: p.id, PropertyCode: p.code, Method: m}
			for _, c := range cands {
				if c.serves(m, p.code, p.id) {
					code := c.code
					r.IntegrationCode = &code
					break
				}
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// ── settlement details (reconciliation per method) ───────────────────────

// GatewaySettlementDetail is one gateway transaction with method, channel and fees.
type GatewaySettlementDetail struct {
	ExternalID string     `json:"externalId"`
	Reference  string     `json:"reference" doc:"OneClub payment number"`
	Method     string     `json:"method" enum:"qris,virtual_account,card,ewallet,other"`
	Channel    string     `json:"channel" doc:"e.g. QRIS, BCA, MANDIRI, CREDIT_CARD"`
	Amount     string     `json:"amount"`
	Fee        string     `json:"fee"`
	Net        string     `json:"net"`
	Status     string     `json:"status" enum:"settled,pending,failed"`
	SettledAt  *time.Time `json:"settledAt"`
}

// SettlementDetailer is implemented by payment adapters reporting method,
// channel and fee per transaction.
type SettlementDetailer interface {
	SettlementDetails(ctx context.Context, from, to time.Time) ([]GatewaySettlementDetail, error)
}

func xenditMethod(category string) string {
	switch strings.ToUpper(category) {
	case "QR_CODE", "QRIS":
		return "qris"
	case "VIRTUAL_ACCOUNT":
		return "virtual_account"
	case "CARDS", "CARD", "CREDIT_CARD":
		return "card"
	case "EWALLET":
		return "ewallet"
	}
	return "other"
}

// SettlementDetails reads the Xendit transactions with fees.
func (x *xendit) SettlementDetails(ctx context.Context, from, to time.Time) ([]GatewaySettlementDetail, error) {
	var out []GatewaySettlementDetail
	after := ""
	for page := 0; page < 50; page++ {
		q := url.Values{"types": {"PAYMENT"}, "created[gte]": {from.UTC().Format(time.RFC3339)}, "created[lt]": {to.UTC().Format(time.RFC3339)}, "limit": {"50"}}
		if after != "" {
			q.Set("after_id", after)
		}
		var resp struct {
			HasMore bool `json:"has_more"`
			Data    []struct {
				ID               string  `json:"id"`
				ProductID        string  `json:"product_id"`
				ReferenceID      string  `json:"reference_id"`
				ChannelCategory  string  `json:"channel_category"`
				ChannelCode      string  `json:"channel_code"`
				Amount           float64 `json:"amount"`
				NetAmount        float64 `json:"net_amount"`
				Status           string  `json:"status"`
				SettlementStatus string  `json:"settlement_status"`
				Updated          string  `json:"updated"`
				Fee              struct {
					XenditFee     float64 `json:"xendit_fee"`
					ValueAddedTax float64 `json:"value_added_tax"`
				} `json:"fee"`
			} `json:"data"`
		}
		if _, err := doJSON(ctx, x.env, "settlement_details", http.MethodGet, x.base+"/transactions?"+q.Encode(), x.auth(), nil, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			st := "pending"
			if strings.EqualFold(d.SettlementStatus, "SETTLED") {
				st = "settled"
			}
			if strings.EqualFold(d.Status, "FAILED") || strings.EqualFold(d.Status, "VOIDED") {
				st = "failed"
			}
			var at *time.Time
			if t, err := time.Parse(time.RFC3339, d.Updated); err == nil {
				at = &t
			}
			ext := d.ProductID
			if ext == "" {
				ext = d.ID
			}
			amount := decimal.NewFromFloat(d.Amount)
			fee := decimal.NewFromFloat(d.Fee.XenditFee + d.Fee.ValueAddedTax)
			net := decimal.NewFromFloat(d.NetAmount)
			if d.NetAmount == 0 {
				net = amount.Sub(fee)
			}
			out = append(out, GatewaySettlementDetail{ExternalID: ext, Reference: d.ReferenceID, Method: xenditMethod(d.ChannelCategory), Channel: d.ChannelCode,
				Amount: amount.String(), Fee: fee.String(), Net: net.String(), Status: st, SettledAt: at})
			after = d.ID
		}
		if !resp.HasMore {
			break
		}
	}
	return out, nil
}

// SettlementDetails of the sandbox gateway: three deterministic payments
// (QRIS, BCA virtual account, card) per day, settled up to yesterday. The
// days are calendar days in the location of from (the club's, as the
// settlement endpoint passes local midnights), today included.
func (m *mockPayment) SettlementDetails(ctx context.Context, from, to time.Time) ([]GatewaySettlementDetail, error) {
	var out []GatewaySettlementDetail
	err := timed(m.env, ctx, "settlement_details", map[string]any{"from": from, "to": to}, func() (any, int, error) {
		if to.Sub(from) > 92*24*time.Hour {
			return nil, 400, fmt.Errorf("period longer than 92 days")
		}
		loc := from.Location()
		now := clock.Now().In(loc)
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		for d := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc); d.Before(to); d = d.AddDate(0, 0, 1) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(d.Format("20060102")))
			k := int64(h.Sum32() % 40)
			for i, p := range []struct {
				method, channel string
				amount          int64
			}{{"qris", "QRIS", 250000 + k*5000}, {"virtual_account", "BCA", 1500000 + k*10000}, {"card", "CREDIT_CARD", 850000 + k*2500}} {
				amt := decimal.NewFromInt(p.amount)
				var fee decimal.Decimal
				switch p.method {
				case "qris":
					fee = amt.Mul(decimal.RequireFromString("0.007")).Round(0)
				case "virtual_account":
					fee = decimal.NewFromInt(4440)
				default:
					fee = amt.Mul(decimal.RequireFromString("0.029")).Add(decimal.NewFromInt(2000)).Round(0)
				}
				st := "settled"
				at := d.Add(36 * time.Hour)
				if !d.Before(today) {
					st = "pending"
				}
				out = append(out, GatewaySettlementDetail{ExternalID: fmt.Sprintf("mocktx_%s_%d", d.Format("20060102"), i), Reference: fmt.Sprintf("PAY-%s-%04d", d.Format("20060102"), i+1),
					Method: p.method, Channel: p.channel, Amount: amt.String(), Fee: fee.String(), Net: amt.Sub(fee).String(), Status: st, SettledAt: &at})
			}
		}
		return map[string]any{"count": len(out)}, 200, nil
	})
	return out, err
}

// GatewaySettlementTotal sums transactions of one method.
type GatewaySettlementTotal struct {
	Method  string `json:"method"`
	Count   int    `json:"count"`
	Gross   string `json:"gross"`
	Fees    string `json:"fees"`
	Net     string `json:"net"`
	Settled int    `json:"settled"`
	Pending int    `json:"pending"`
	Failed  int    `json:"failed"`
}

// PaymentSettlementSummary is the gateway settlement per method.
type PaymentSettlementSummary struct {
	IntegrationCode string                    `json:"integrationCode"`
	From            string                    `json:"from"`
	To              string                    `json:"to"`
	Currency        string                    `json:"currency"`
	Methods         []GatewaySettlementTotal  `json:"methods"`
	Total           GatewaySettlementTotal    `json:"total"`
	Items           []GatewaySettlementDetail `json:"items,omitempty"`
}

// SummarizeSettlement totals settlement details per method.
func SummarizeSettlement(code string, from, to time.Time, items []GatewaySettlementDetail, withItems bool) PaymentSettlementSummary {
	out := PaymentSettlementSummary{IntegrationCode: code, From: from.Format("2006-01-02"), To: to.AddDate(0, 0, -1).Format("2006-01-02"), Currency: "IDR",
		Methods: []GatewaySettlementTotal{}}
	type acc struct {
		gross, fees, net decimal.Decimal
		t                GatewaySettlementTotal
	}
	by := map[string]*acc{}
	total := &acc{t: GatewaySettlementTotal{Method: "all"}}
	for _, it := range items {
		a := by[it.Method]
		if a == nil {
			a = &acc{t: GatewaySettlementTotal{Method: it.Method}}
			by[it.Method] = a
		}
		for _, x := range []*acc{a, total} {
			x.t.Count++
			if it.Status == "failed" {
				x.t.Failed++
				continue
			}
			if it.Status == "settled" {
				x.t.Settled++
			} else {
				x.t.Pending++
			}
			x.gross = x.gross.Add(decimal.RequireFromString(orZero(it.Amount)))
			x.fees = x.fees.Add(decimal.RequireFromString(orZero(it.Fee)))
			x.net = x.net.Add(decimal.RequireFromString(orZero(it.Net)))
		}
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fin := func(a *acc) GatewaySettlementTotal {
		a.t.Gross, a.t.Fees, a.t.Net = a.gross.String(), a.fees.String(), a.net.String()
		return a.t
	}
	for _, k := range keys {
		out.Methods = append(out.Methods, fin(by[k]))
	}
	out.Total = fin(total)
	if withItems {
		out.Items = items
	}
	return out
}

func orZero(s string) string {
	if _, err := decimal.NewFromString(s); err != nil {
		return "0"
	}
	return s
}
