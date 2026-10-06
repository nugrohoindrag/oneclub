package app

// Trial dataset: outlets, menus and stock. At go-live the F&B and pro
// shop catalogue is set up (tax & service, outlets, products, recipes,
// items, the outlet warehouses) and the opening stock of the cut-over
// opname is imported. Every simulated day the outlets open their POS
// shifts and sell (dine-in, golfers at the halfway house, the bar, the
// sport café and the pro shop; cash, card, QRIS and member charge); the
// sales consume the recipes' stock through inventory. Twice a week the
// outlets are replenished from their stores (stock transfers).

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "pos", Order: 30, Setup: trialPOSSetup, Day: trialPOSDay})
}

const trialOutletManager = "outlet.manager@demo.oneclub.id"

type trialItem struct {
	code, name, category, uom string
	cost                      int64
	product                   string // retail product sold 1:1
	store                     string // receiving store
}

var trialItems = []trialItem{
	{"RICE", "Beras Premium", "FNB", "KG", 15000, "", "MAIN-STORE"},
	{"EGG", "Telur Ayam", "FNB", "PCS", 2500, "", "COLD-STORE"},
	{"CHICKEN", "Daging Ayam Fillet", "FNB", "KG", 48000, "", "COLD-STORE"},
	{"BEEF", "Daging Sapi Has Dalam", "FNB", "KG", 145000, "", "COLD-STORE"},
	{"OXTAIL", "Buntut Sapi", "FNB", "KG", 160000, "", "COLD-STORE"},
	{"FISH", "Ikan Dori Fillet", "FNB", "KG", 75000, "", "COLD-STORE"},
	{"NOODLE", "Mie Telur", "FNB", "KG", 22000, "", "MAIN-STORE"},
	{"BREAD", "Roti Tawar (slice)", "FNB", "PCS", 1500, "", "MAIN-STORE"},
	{"POTATO", "Kentang", "FNB", "KG", 18000, "", "MAIN-STORE"},
	{"VEG", "Sayuran Campur", "FNB", "KG", 20000, "", "COLD-STORE"},
	{"SPICE", "Bumbu Dasar", "FNB", "KG", 45000, "", "MAIN-STORE"},
	{"OIL", "Minyak Goreng", "FNB", "L", 18000, "", "MAIN-STORE"},
	{"SUGAR", "Gula Pasir", "FNB", "KG", 16000, "", "MAIN-STORE"},
	{"ORANGE", "Jeruk Peras", "FNB", "KG", 25000, "", "COLD-STORE"},
	{"COFFEE", "Biji Kopi Arabika", "BEV", "KG", 220000, "", "BEV-STORE"},
	{"TEA", "Teh Celup", "BEV", "PCS", 500, "", "BEV-STORE"},
	{"MILK", "Susu Segar", "BEV", "L", 22000, "", "COLD-STORE"},
	{"WATER-600", "Air Mineral 600 ml", "BEV", "BTL", 2500, "AIR-600", "BEV-STORE"},
	{"COLA-330", "Cola 330 ml", "BEV", "BTL", 5500, "COLA", "BEV-STORE"},
	{"ISO-500", "Minuman Isotonik 500 ml", "BEV", "BTL", 6500, "ISOTONIK", "BEV-STORE"},
	{"BEER-330", "Bir 330 ml", "BEV", "BTL", 19000, "BIR", "BEV-STORE"},
	{"BALL-DZ", "Bola Golf (lusin)", "PROSHOP", "BOX", 420000, "BALL-DZ", "PRO-SHOP"},
	{"GLOVE", "Sarung Tangan Golf", "PROSHOP", "PCS", 165000, "GLOVE", "PRO-SHOP"},
	{"CAP", "Topi Golf Modern Golf", "PROSHOP", "PCS", 110000, "CAP", "PRO-SHOP"},
	{"TEE-PACK", "Tee Kayu (isi 50)", "PROSHOP", "PACK", 14000, "TEE-PACK", "PRO-SHOP"},
	{"POLO", "Kaos Polo Modern Golf", "PROSHOP", "PCS", 320000, "POLO", "PRO-SHOP"},
	{"UMBRELLA", "Payung Golf", "PROSHOP", "PCS", 230000, "UMBRELLA", "PRO-SHOP"},
}

type trialProduct struct {
	code, name, category, ptype, station string
	price, member                        int64
	recipe                               [][2]string // item code, quantity (stock UOM)
}

var trialProducts = []trialProduct{
	{"NASGOR", "Nasi Goreng Spesial", "Makanan", "food", "kitchen", 85000, 76500,
		[][2]string{{"RICE", "0.2"}, {"EGG", "1"}, {"CHICKEN", "0.08"}, {"SPICE", "0.02"}, {"OIL", "0.03"}}},
	{"MIEGOR", "Mie Goreng Ayam", "Makanan", "food", "kitchen", 80000, 72000,
		[][2]string{{"NOODLE", "0.15"}, {"CHICKEN", "0.08"}, {"EGG", "1"}, {"VEG", "0.05"}, {"OIL", "0.03"}, {"SPICE", "0.02"}}},
	{"SOTO", "Soto Ayam", "Makanan", "food", "kitchen", 65000, 58500,
		[][2]string{{"CHICKEN", "0.12"}, {"RICE", "0.15"}, {"VEG", "0.05"}, {"SPICE", "0.03"}, {"EGG", "1"}}},
	{"SOPBUNTUT", "Sop Buntut", "Makanan", "food", "kitchen", 145000, 130500,
		[][2]string{{"OXTAIL", "0.35"}, {"POTATO", "0.1"}, {"VEG", "0.08"}, {"SPICE", "0.03"}, {"RICE", "0.15"}}},
	{"SANDWICH", "Club Sandwich", "Makanan", "food", "kitchen", 95000, 85500,
		[][2]string{{"BREAD", "3"}, {"CHICKEN", "0.08"}, {"EGG", "1"}, {"VEG", "0.04"}, {"POTATO", "0.15"}, {"OIL", "0.05"}}},
	{"BURGER", "Beef Burger", "Makanan", "food", "kitchen", 125000, 112500,
		[][2]string{{"BEEF", "0.15"}, {"BREAD", "2"}, {"VEG", "0.04"}, {"POTATO", "0.15"}, {"OIL", "0.05"}}},
	{"FISHCHIPS", "Fish & Chips", "Makanan", "food", "kitchen", 115000, 103500, [][2]string{{"FISH", "0.2"}, {"POTATO", "0.2"}, {"OIL", "0.08"}}},
	{"FRIES", "French Fries", "Snack", "food", "kitchen", 45000, 40500, [][2]string{{"POTATO", "0.25"}, {"OIL", "0.06"}}},
	{"KOPI", "Kopi Hitam", "Minuman", "beverage", "bar", 35000, 31500, [][2]string{{"COFFEE", "0.018"}, {"SUGAR", "0.01"}}},
	{"LATTE", "Cafe Latte", "Minuman", "beverage", "bar", 45000, 40500, [][2]string{{"COFFEE", "0.018"}, {"MILK", "0.2"}, {"SUGAR", "0.01"}}},
	{"TEH", "Teh Manis", "Minuman", "beverage", "bar", 25000, 22500, [][2]string{{"TEA", "1"}, {"SUGAR", "0.02"}}},
	{"ESJERUK", "Es Jeruk", "Minuman", "beverage", "bar", 35000, 31500, [][2]string{{"ORANGE", "0.25"}, {"SUGAR", "0.02"}}},
	{"AIR-600", "Air Mineral 600 ml", "Minuman", "beverage", "bar", 15000, 15000, nil},
	{"COLA", "Cola", "Minuman", "beverage", "bar", 25000, 25000, nil},
	{"ISOTONIK", "Minuman Isotonik", "Minuman", "beverage", "bar", 28000, 28000, nil},
	{"BIR", "Bir Dingin", "Minuman", "beverage", "bar", 55000, 55000, nil},
	{"BALL-DZ", "Bola Golf (lusin)", "Pro Shop", "retail", "", 650000, 600000, nil},
	{"GLOVE", "Sarung Tangan Golf", "Pro Shop", "retail", "", 295000, 270000, nil},
	{"CAP", "Topi Modern Golf", "Pro Shop", "retail", "", 250000, 225000, nil},
	{"TEE-PACK", "Tee Kayu (isi 50)", "Pro Shop", "retail", "", 35000, 35000, nil},
	{"POLO", "Kaos Polo Modern Golf", "Pro Shop", "retail", "", 650000, 585000, nil},
	{"UMBRELLA", "Payung Golf", "Pro Shop", "retail", "", 450000, 405000, nil},
}

type trialOutlet struct {
	code, name, kind, warehouse, user string
	products                          []string
	weekday, weekend                  int // orders per day
}

var trialOutlets = []trialOutlet{
	{"RESTO", "Clubhouse Restaurant", "restaurant", "CLUBHOUSE", "pos@demo.oneclub.id",
		[]string{"NASGOR", "MIEGOR", "SOTO", "SOPBUNTUT", "SANDWICH", "BURGER", "FISHCHIPS", "FRIES", "KOPI", "LATTE", "TEH", "ESJERUK", "AIR-600", "COLA"}, 4, 8},
	{"BAR", "Spike Bar", "bar", "BAR", "pos.bar@demo.oneclub.id", []string{"BIR", "COLA", "AIR-600", "ISOTONIK", "KOPI", "FRIES"}, 2, 4},
	{"HALFWAY", "Halfway House", "restaurant", "HALFWAY", "pos.halfway@demo.oneclub.id",
		[]string{"AIR-600", "ISOTONIK", "COLA", "SANDWICH", "FRIES", "KOPI", "TEH", "MIEGOR"}, 3, 5},
	{"SPORTCAFE", "Sport Club Café", "cafe", "SPORT-CAFE", "pos.sport@demo.oneclub.id",
		[]string{"AIR-600", "ISOTONIK", "COLA", "NASGOR", "FRIES", "ESJERUK", "TEH", "LATTE"}, 1, 3},
	{"PROSHOP", "Pro Shop", "retail", "PRO-SHOP", "pos.proshop@demo.oneclub.id",
		[]string{"BALL-DZ", "GLOVE", "CAP", "TEE-PACK", "POLO", "UMBRELLA"}, 1, 2},
}

// trialUsage is the expected daily use of each item per warehouse (stock
// UOM) from the outlets' sales mix (2 dishes per order) and the golf
// rounds (one bottle of water per player at Golf Ops).
func trialUsage() map[string]map[string]float64 {
	recipes := map[string]trialProduct{}
	for _, p := range trialProducts {
		recipes[p.code] = p
	}
	retail := map[string]string{}
	for _, it := range trialItems {
		if it.product != "" {
			retail[it.product] = it.code
		}
	}
	out := map[string]map[string]float64{}
	add := func(wh, item string, q float64) {
		if out[wh] == nil {
			out[wh] = map[string]float64{}
		}
		out[wh][item] += q
	}
	for _, o := range trialOutlets {
		perProduct := float64(o.weekday*5+o.weekend*2) / 7 * 2 / float64(len(o.products))
		for _, pc := range o.products {
			if it := retail[pc]; it != "" {
				add(o.warehouse, it, perProduct)
				continue
			}
			for _, l := range recipes[pc].recipe {
				q, _ := decimal.NewFromString(l[1])
				add(o.warehouse, l[0], perProduct*q.InexactFloat64())
			}
		}
	}
	add("GOLF-OPS", "WATER-600", 40)
	return out
}

// trialPar is the stock level a warehouse is replenished to: ten days of
// use for the outlets, three weeks for the stores (whole units).
func trialPar(daily float64, days int) float64 { return math.Ceil(daily*float64(days) + 1) }

// trialStores maps the outlet warehouses to the store that replenishes them.
func trialStoreOf(item string) string {
	for _, it := range trialItems {
		if it.code == item {
			return it.store
		}
	}
	return "MAIN-STORE"
}

func trialPOSSetup(ctx context.Context, t *Trial) error {
	admin := t.Admin()
	om := admin // the catalogue is club configuration
	eff := t.Start.AddDate(0, 0, -30).Format(time.RFC3339)
	// F&B: service charge 10 % on the net, PB1 10 % on net + service (plus plus)
	admin.Post("/api/v1/commercial/tax-service-rules", J{"code": "SVC", "name": "Service Charge 10%", "kind": "service", "ratePercent": "10",
		"basis": "net_amount", "pricingMode": "plus_plus", "effectiveFrom": eff})
	admin.Post("/api/v1/commercial/tax-service-rules", J{"code": "PB1", "name": "PB1 Restoran 10%", "kind": "tax", "ratePercent": "10",
		"basis": "net_plus_service", "pricingMode": "plus_plus", "effectiveFrom": eff})
	inv := t.As("inventory@demo.oneclub.id")
	uoms := map[string]string{}
	for _, u := range [][3]string{{"KG", "Kilogram", "mass"}, {"G", "Gram", "mass"}, {"L", "Liter", "volume"}, {"PCS", "Piece", "count"},
		{"BTL", "Bottle", "count"}, {"BOX", "Box", "count"}, {"PACK", "Pack", "count"}, {"CTN", "Carton", "count"}, {"PORTION", "Portion", "portion"}} {
		uoms[u[0]] = inv.Post("/api/v1/inventory/uoms", J{"code": u[0], "name": u[1], "kind": u[2]}).S("id")
	}
	inv.Post("/api/v1/inventory/uom-conversions", J{"fromUomId": uoms["KG"], "toUomId": uoms["G"], "factor": "1000"})
	cats := map[string]string{}
	for _, c := range inv.Items("/api/v1/inventory/categories?limit=100") {
		cats[c.S("code")] = c.S("id")
	}
	whs := map[string]string{}
	for _, w := range inv.Items("/api/v1/inventory/warehouses?limit=100") {
		whs[w.S("code")] = w.S("id")
	}
	// outlets and their warehouses
	outlets := map[string]string{}
	for _, o := range trialOutlets {
		body := J{"code": o.code, "name": o.name, "outletType": o.kind, "openingTime": "06:00", "closingTime": "22:00"}
		if o.code == "PROSHOP" {
			body["taxCodes"], body["pricingMode"], body["revenueComponent"] = []string{"PPN"}, "nett", "pro_shop"
		} else {
			body["taxCodes"], body["pricingMode"], body["kdsStations"] = []string{"SVC", "PB1"}, "plus_plus", []string{"kitchen", "bar"}
		}
		outlets[o.code] = om.Post("/api/v1/commercial/outlets", body).S("id")
		inv.Patch("/api/v1/inventory/warehouses/"+whs[o.warehouse], J{"outletId": outlets[o.code]})
	}
	// products
	products := map[string]string{}
	for _, p := range trialProducts {
		body := J{"code": p.code, "name": p.name, "category": p.category, "productType": p.ptype, "price": fmt.Sprint(p.price),
			"memberPrice": fmt.Sprint(p.member)}
		if p.station != "" {
			body["kitchenStation"] = p.station
		}
		if p.ptype == "retail" {
			body["revenueComponent"] = "pro_shop"
		}
		products[p.code] = om.Post("/api/v1/commercial/products", body).S("id")
	}
	for _, o := range trialOutlets {
		var ids []string
		for _, pc := range o.products {
			ids = append(ids, products[pc])
		}
		om.Post("/api/v1/commercial/menus", J{"code": o.code + "-MENU", "name": o.name + " — All Day", "outletId": outlets[o.code], "productIds": ids,
			"channels": []string{"pos", "member_app", "caddy_tablet"}})
	}
	// items (retail items are sold 1:1 through their product)
	items := map[string]string{}
	for _, it := range trialItems {
		body := J{"code": it.code, "name": it.name, "baseUomId": uoms[it.uom], "categoryId": cats[it.category], "category": it.category,
			"itemType": "raw", "standardCost": fmt.Sprint(it.cost)}
		if it.product != "" {
			body["productId"] = products[it.product]
		}
		items[it.code] = inv.Post("/api/v1/inventory/items", body).S("id")
	}
	// recipes of the menu items
	for _, p := range trialProducts {
		if len(p.recipe) == 0 {
			continue
		}
		rid := inv.Post("/api/v1/inventory/recipes", J{"code": "R-" + p.code, "name": p.name, "recipeType": "menu", "productId": products[p.code],
			"yieldQuantity": "1", "yieldUomId": uoms["PORTION"]}).S("id")
		for _, l := range p.recipe {
			inv.Post("/api/v1/inventory/recipe-lines", J{"recipeId": rid, "itemId": items[l[0]], "quantity": l[1], "uomId": trialUOMOf(uoms, l[0])})
		}
	}
	// the golf round service BOM: one bottle of water per player (rate card)
	svc := inv.Post("/api/v1/inventory/recipes", J{"code": "R-GOLF-ROUND", "name": "Golf round amenities", "recipeType": "service",
		"serviceRef": "golf.round", "yieldQuantity": "1"}).S("id")
	inv.Post("/api/v1/inventory/recipe-lines", J{"recipeId": svc, "itemId": items["WATER-600"], "quantity": "1", "uomId": uoms["BTL"]})

	// opening stock of the cut-over opname: outlets at par, stores at three weeks
	var csv strings.Builder
	csv.WriteString("warehouseCode,itemCode,quantity,unitCost\n")
	opening := trialOpeningStock()
	for _, wh := range slices.Sorted(maps.Keys(opening)) {
		for _, item := range slices.Sorted(maps.Keys(opening[wh])) {
			q := opening[wh][item]
			fmt.Fprintf(&csv, "%s,%s,%v,%d\n", wh, item, q, trialCost(item))
		}
	}
	res := inv.Post("/api/v1/inventory/opening-stock:import", J{"mode": "commit", "filename": "opname-cutover.csv",
		"businessDate": t.Start.Format(time.DateOnly), "csv": csv.String()})
	if res.S("status") != "completed" {
		return fmt.Errorf("opening stock: %v", res["errors"])
	}
	return nil
}

// trialOpeningStock is the cut-over stock: the outlets at par (ten days),
// the stores with three weeks of the outlets' use (a store that is also an
// outlet, the pro shop, carries both).
func trialOpeningStock() map[string]map[string]float64 {
	out := map[string]map[string]float64{}
	add := func(wh, item string, q float64) {
		if out[wh] == nil {
			out[wh] = map[string]float64{}
		}
		out[wh][item] += q
	}
	for wh, m := range trialUsage() {
		for item, daily := range m {
			add(wh, item, trialPar(daily, 10))
			if st := trialStoreOf(item); st != wh {
				add(st, item, trialPar(daily, 21))
			} else {
				add(st, item, trialPar(daily, 11))
			}
		}
	}
	return out
}

func trialUOMOf(uoms map[string]string, item string) string {
	for _, it := range trialItems {
		if it.code == item {
			return uoms[it.uom]
		}
	}
	return ""
}

func trialCost(item string) int64 {
	for _, it := range trialItems {
		if it.code == item {
			return it.cost
		}
	}
	return 0
}

// ids resolves the codes of a resource list (cached in refs), read as the
// Property Admin or the given user.
func (t *Trial) ids(kind, path string, as ...string) func(code string) string {
	return func(code string) string {
		k := kind + ":" + code
		if v := t.Ref(k); v != "" {
			return v
		}
		c := t.Admin()
		if len(as) > 0 {
			c = t.As(as[0])
		}
		for _, x := range c.Items(path) {
			t.SetRef(kind+":"+x.S("code"), x.S("id"))
		}
		return t.Ref(k)
	}
}

// trialSale is one POS order of a simulated day (choices drawn first).
type trialSale struct {
	body   J
	tender J
}

func trialPOSDay(ctx context.Context, t *Trial, day time.Time) error {
	r := t.Rand("pos:" + day.Format(time.DateOnly))
	if wd := day.Weekday(); wd == time.Monday || wd == time.Thursday {
		t.At(day, "07:00")
		trialReplenishOutlets(t, day)
	}
	outlet := t.ids("outlet", "/api/v1/commercial/outlets?limit=100")
	product := t.ids("product", "/api/v1/commercial/products?limit=200")
	weekend := day.Weekday() == time.Saturday || day.Weekday() == time.Sunday
	members := t.MembersOn(day)
	// the day's orders per outlet, in two blocks: lunch and the afternoon /
	// evening (happy hour)
	sales := make([][2][]trialSale, len(trialOutlets))
	for oi, o := range trialOutlets {
		n := o.weekday + r.IntN(2)
		if weekend {
			n = o.weekend + r.IntN(3)
		}
		for i := range n {
			var lines []J
			for range 1 + r.IntN(3) {
				q := 1
				if r.IntN(4) == 0 {
					q = 2
				}
				lines = append(lines, J{"productId": product(o.products[r.IntN(len(o.products))]), "quantity": fmt.Sprint(q)})
			}
			body := J{"outletId": outlet(o.code), "lines": lines, "send": o.code != "PROSHOP"}
			if o.code == "PROSHOP" {
				body["orderType"] = "retail"
			} else {
				body["tableNo"], body["guestCount"] = fmt.Sprint(1+r.IntN(20)), 1+r.IntN(4)
			}
			var tender J
			switch x := r.IntN(10); {
			case x < 3 && len(members) > 0: // member: member price, charged to the member account
				body["customerId"] = members[r.IntN(len(members))].CustomerID
				tender = J{"methodType": "member_account"}
			case x < 6:
				tender = J{"methodType": "card", "reference": fmt.Sprintf("EDC-%06d", r.IntN(1000000))}
			case x < 8:
				tender = J{"methodType": "qris", "reference": fmt.Sprintf("QR-%08d", r.IntN(100000000))}
			default:
				tender = J{"methodType": "cash"}
			}
			b := 0
			if i >= n/2 {
				b = 1
			}
			sales[oi][b] = append(sales[oi][b], trialSale{body: body, tender: tender})
		}
	}
	shifts := make([]string, len(trialOutlets))
	t.At(day, "07:30")
	t.Parallel(len(trialOutlets), trialWorkers, func(oi int) {
		o := trialOutlets[oi]
		shifts[oi] = t.As(o.user).Post("/api/v1/commercial/shifts:open", J{"outletId": outlet(o.code), "openingCash": "500000"}).S("id")
	})
	for b, at := range []string{"12:15", "17:15"} {
		t.At(day, at)
		// the orders of the block in every outlet, several terminals at once
		type sale struct {
			oi int
			s  trialSale
		}
		var block []sale
		for oi := range trialOutlets {
			for _, s := range sales[oi][b] {
				block = append(block, sale{oi, s})
			}
		}
		t.Parallel(len(block), trialWorkers, func(i int) {
			oi, s := block[i].oi, block[i].s
			o, c := trialOutlets[oi], t.As(trialOutlets[oi].user)
			s.body["shiftId"] = shifts[oi]
			ord := c.Post("/api/v1/commercial/orders", s.body)
			st, _, raw := c.Call("POST", "/api/v1/commercial/orders/"+ord.S("id")+":pay", J{"shiftId": shifts[oi], "tenders": []J{s.tender}})
			if st == 409 && (strings.Contains(string(raw), "limit") || strings.Contains(string(raw), "no_member_account")) {
				c.Post("/api/v1/commercial/orders/"+ord.S("id")+":pay", J{"shiftId": shifts[oi], "tenders": []J{{"methodType": "card", "reference": "EDC-LIMIT"}}})
			} else if st < 200 || st > 299 {
				t.fail("pay order at %s: %d %s", o.code, st, raw)
			}
		})
	}
	t.At(day, "22:00")
	t.Parallel(len(trialOutlets), trialWorkers, func(oi int) {
		c := t.As(trialOutlets[oi].user)
		rep := c.Get("/api/v1/commercial/shifts/" + shifts[oi] + "/report")
		c.Post("/api/v1/commercial/shifts/"+shifts[oi]+":close", J{"countedCash": rep.S("expectedCash")})
	})
	return nil
}

// trialReplenishOutlets brings the outlet warehouses back to their par
// level with stock transfers from the stores (shipped and received).
func trialReplenishOutlets(t *Trial, day time.Time) {
	ws := t.As("warehouse@demo.oneclub.id")
	wh := t.ids("warehouse", "/api/v1/inventory/warehouses?limit=100")
	item := t.ids("item", "/api/v1/inventory/items?limit=200")
	balances := map[string]map[string]float64{}
	stock := func(w string) map[string]float64 {
		if balances[w] == nil {
			balances[w] = map[string]float64{}
			for _, b := range ws.Items("/api/v1/inventory/stock-balances?warehouseId=" + wh(w) + "&limit=500") {
				q, _ := decimal.NewFromString(b.S("quantity"))
				balances[w][b.S("itemId")] += q.InexactFloat64()
			}
		}
		return balances[w]
	}
	for _, w := range slices.Sorted(maps.Keys(trialUsage())) {
		m := trialUsage()[w]
		onHand := stock(w)
		bySource := map[string][]J{}
		for _, code := range slices.Sorted(maps.Keys(m)) {
			daily := m[code]
			need := trialPar(daily, 10) - onHand[item(code)]
			if need < math.Max(1, daily*2) {
				continue
			}
			src := trialStoreOf(code)
			if src == w {
				continue // a store itself: replenished by purchasing
			}
			// what the store has (a stock-out waits for the next delivery)
			q := math.Min(math.Ceil(need), math.Floor(stock(src)[item(code)]))
			if q < 1 {
				continue
			}
			stock(src)[item(code)] -= q
			bySource[src] = append(bySource[src], J{"itemId": item(code), "quantity": fmt.Sprint(q)})
		}
		for _, src := range slices.Sorted(maps.Keys(bySource)) {
			lines := bySource[src]
			tr := ws.Post("/api/v1/inventory/transfers", J{"fromWarehouseId": wh(src), "toWarehouseId": wh(w), "ship": true,
				"notes": "Replenishment to par " + day.Format("Mon 2 Jan"), "lines": lines})
			ws.Post("/api/v1/inventory/transfers/"+tr.S("id")+":receive", J{})
		}
	}
}
