package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/reservation"
)

// p2 holds shared P2 fixtures of the primary instance (created once).
type p2Fixtures struct {
	SA        *Client
	Loc       *time.Location
	TaxGolf   string
	CourtSet  string
	MonThu    string
	Fri       string
	Sat       string
	SunPH     string
	EntrySet  string
	Weekday   string
	Weekend   string
	Morning   string // 07–16
	Evening   string // 16–21
	CustomerA string // member customer with member account
	CustomerB string // guest customer
	AccountA  string
}

var (
	p2Once sync.Once
	p2     *p2Fixtures
)

// id returns body.id of a response.
func idOf(r Resp) string { return str(r.JSON()["id"]) }

// nextWeekday returns the next local date (at 00:00) with the given weekday,
// at least `minDays` days ahead.
func nextWeekday(loc *time.Location, wd time.Weekday, minDays int) time.Time {
	n := time.Now().In(loc)
	d := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, minDays)
	for d.Weekday() != wd {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

func at(d time.Time, h, m int) time.Time {
	return time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, d.Location())
}

func rfc(t time.Time) string { return t.Format(time.RFC3339) }

func past() string { return time.Now().Add(-72 * time.Hour).Format(time.RFC3339) }

func pastDate() string { return time.Now().AddDate(0, 0, -3).Format("2006-01-02") }

// setupP2 creates tax rules, day types, time bands and two customers.
func setupP2(t *testing.T) *p2Fixtures {
	t.Helper()
	p2Once.Do(func() {
		sa := superAdmin(t, inst)
		f := &p2Fixtures{SA: sa}
		loc, _ := time.LoadLocation("Asia/Jakarta")
		f.Loc = loc
		// Tax & Service: golf 11% VAT; meeting 15.5% = service 5% + PB1 10% on net + service.
		sa.Must(201, "POST", "/api/v1/commercial/tax-service-rules", map[string]any{"code": "P2VAT", "name": "PPN 11%", "kind": "tax", "ratePercent": "11",
			"basis": "net_amount", "pricingMode": "nett", "effectiveFrom": past()})
		sa.Must(201, "POST", "/api/v1/commercial/tax-service-rules", map[string]any{"code": "P2SVC", "name": "Service 5%", "kind": "service", "ratePercent": "5",
			"basis": "net_amount", "pricingMode": "plus_plus", "effectiveFrom": past()})
		sa.Must(201, "POST", "/api/v1/commercial/tax-service-rules", map[string]any{"code": "P2PB1", "name": "PB1 10%", "kind": "tax", "ratePercent": "10",
			"basis": "net_plus_service", "pricingMode": "plus_plus", "effectiveFrom": past()})
		f.CourtSet = idOf(sa.Must(201, "POST", "/api/v1/commercial/day-type-sets", map[string]any{"code": "COURT", "name": "Court days", "serviceType": "sport_court"}))
		// day types of a set: weekdays "1,2,3,4", includesHolidays, lower priority wins
		dt := func(set, code, name, days string, ph bool) string {
			return idOf(sa.Must(201, "POST", "/api/v1/commercial/line-day-types", map[string]any{"code": code, "name": name, "dayTypeSetId": set,
				"weekdays": days, "includesHolidays": ph}))
		}
		f.MonThu = dt(f.CourtSet, "C-MONTHU", "Mon–Thu", "1,2,3,4", false)
		f.Fri = dt(f.CourtSet, "C-FRI", "Friday", "5", false)
		f.Sat = dt(f.CourtSet, "C-SAT", "Saturday", "6", false)
		f.SunPH = dt(f.CourtSet, "C-SUNPH", "Sunday / Public Holiday", "7", true)
		f.EntrySet = idOf(sa.Must(201, "POST", "/api/v1/commercial/day-type-sets", map[string]any{"code": "ENTRY", "name": "Entry days"}))
		f.Weekday = dt(f.EntrySet, "WEEKDAY", "Weekday", "1,2,3,4,5", false)
		f.Weekend = dt(f.EntrySet, "WEEKEND", "Weekend / Public Holiday", "6,7", true)
		f.Morning = idOf(sa.Must(201, "POST", "/api/v1/commercial/time-bands", map[string]any{"code": "07-16", "name": "07.00–16.00", "startTime": "07:00", "endTime": "16:00"}))
		f.Evening = idOf(sa.Must(201, "POST", "/api/v1/commercial/time-bands", map[string]any{"code": "16-21", "name": "16.00–21.00", "startTime": "16:00", "endTime": "21:00"}))
		f.CustomerA = idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "P2-CUST-A", "name": "Hendra Wijaya", "email": "hendra@p2.test"}))
		f.CustomerB = idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "P2-CUST-B", "name": "Rina Tamu", "phone": "+6281111111"}))
		f.AccountA = idOf(sa.Must(201, "POST", "/api/v1/billing/customer-accounts", map[string]any{"customerId": f.CustomerA, "accountType": "member", "creditLimit": "50000000"}))
		p2 = f
	})
	if p2 == nil {
		t.Fatal("P2 fixtures failed")
	}
	p2.SA.t = t
	return p2
}

// rule creates a pricing rule.
func rule(t *testing.T, c *Client, body map[string]any) string {
	t.Helper()
	if _, ok := body["effectiveFrom"]; !ok {
		body["effectiveFrom"] = pastDate()
	}
	return idOf(c.Must(201, "POST", "/api/v1/commercial/pricing-rules", body))
}

// price resolves the price of a non-golf service.
func price(t *testing.T, c *Client, body map[string]any) map[string]any {
	t.Helper()
	return c.Must(200, "POST", "/api/v1/commercial/pricing:resolve-line", body).JSON()
}

func total(p map[string]any) string { return str(p["tax"].(map[string]any)["total"]) }

// resourceOf creates a reservation resource directly via the API.
func resourceOf(t *testing.T, c *Client, code, typ string, capacity *int, attrs map[string]any) string {
	t.Helper()
	b := map[string]any{"code": code, "name": code, "resourceType": typ}
	if capacity != nil {
		b["capacity"] = *capacity
	}
	if attrs != nil {
		b["attributes"] = attrs
	}
	return idOf(c.Must(201, "POST", "/api/v1/reservation/resources", b))
}

// membershipType creates a membership type with a one-year package: P1 keeps
// the joining and period fee on the package; P2 adds the annual fee,
// entitlements and lifecycle fees on the type.
func membershipType(t *testing.T, c *Client, program, code, name string, fields map[string]any) (typeID, packageID string) {
	t.Helper()
	body := map[string]any{"code": code, "name": name, "programId": program, "category": "individual"}
	joining := "0"
	for k, v := range fields {
		if k == "joiningFee" {
			joining = str(v)
			continue
		}
		body[k] = v
	}
	typeID = idOf(c.Must(201, "POST", "/api/v1/membership/types", body))
	packageID = idOf(c.Must(201, "POST", "/api/v1/membership/packages", map[string]any{"typeId": typeID, "code": code + "-1Y",
		"name": name + " 1 year", "periodUnit": "year", "joiningFee": joining}))
	return typeID, packageID
}

// activeMembership runs P1's application flow — application, submit,
// approval by the membership manager, fee payment, activation — and returns
// the membership id.
func activeMembership(t *testing.T, c *Client, customer, typeID, packageID string, dependents []map[string]any) string {
	t.Helper()
	body := map[string]any{"customerId": customer, "typeId": typeID, "packageId": packageID}
	if dependents != nil {
		body["dependents"] = dependents
	}
	aid := idOf(c.Must(201, "POST", "/api/v1/membership/applications", body))
	app := c.Must(200, "POST", "/api/v1/membership/applications/"+aid+":submit", nil).JSON()
	if app["status"] == "pending" && app["approvalRequestId"] != nil {
		mgr := login(t, inst, "membership@demo.oneclub.id", demoPassword)
		mgr.Must(200, "POST", "/api/v1/platform/approvals/"+str(app["approvalRequestId"])+":approve", map[string]any{})
		app = c.Must(200, "GET", "/api/v1/membership/applications/"+aid, nil).JSON()
	}
	if fid := app["feeFolioId"]; fid != nil {
		bal := c.Must(200, "GET", "/api/v1/billing/folios/"+str(fid), nil).JSON()["summary"].(map[string]any)["balance"]
		if dec(bal).IsPositive() {
			c.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": fid, "methodType": "cash", "amount": str(bal)})
			// P1 activates the application once its fee folio is paid (billing.payment_settled)
			if _, err := inst.App.Dispatcher.DispatchPending(t.Context()); err != nil {
				t.Fatal(err)
			}
			app = c.Must(200, "GET", "/api/v1/membership/applications/"+aid, nil).JSON()
		}
	}
	if app["status"] != "completed" {
		app = c.Must(200, "POST", "/api/v1/membership/applications/"+aid+":activate", map[string]any{}).JSON()
	}
	return str(app["membershipId"])
}

func intp(n int) *int { return &n }

func newKey() string { return uuid.NewString() }

func money(s any) string { return fmt.Sprint(s) }

func reservationExpire(in *Instance) (int64, error) {
	return reservation.ExpireHolds(context.Background(), in.DB)
}
