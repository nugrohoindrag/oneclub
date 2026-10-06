package inventory

// HTTP API of PRD P4 inventory (§11 API surface): master data through the
// resource engine, stock balance / movements / stock card, store
// requisitions & issues, stock transfers, stock adjustments, stock opname
// (idempotent count sync for offline `ops`), production, waste, valuation,
// theoretical vs actual consumption, replenishment, expiry, consignment,
// opening stock import, assets & maintenance and depreciation. Every write
// runs in one transaction and records its audit entry.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

const (
	tagStock  = "Inventory"
	tagAssets = "Assets & Equipment"
)

// RegisterP4 adds the PRD P4 inventory routes and master data.
func (s *Stock) RegisterP4(reg *route.Registry, eng *resource.Engine) {
	for _, d := range P4Defs {
		eng.Register(reg, d)
	}
	s.registerStock(reg)
	s.registerDocuments(reg)
	s.registerAnalysis(reg)
	s.registerAssets(reg)
}

func add(reg *route.Registry, rt route.Route) {
	rt.Module, rt.Scope = "inventory", route.ScopeProperty
	if rt.Tag == "" {
		rt.Tag = tagStock
	}
	reg.Add(rt)
}

// act runs an action on the document {id} of the request's property.
func act[Req, Res any](db *dbtx.DB, table, what string, fn func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in Req) (Res, error)) http.HandlerFunc {
	return handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in Req) (Res, error) {
		var zero Res
		rid, err := handle.ID(r)
		if err != nil {
			return zero, err
		}
		property := handle.Property(ctx)
		if err := inProperty(ctx, tx, table, rid, property, what); err != nil {
			return zero, err
		}
		return fn(ctx, tx, property, rid, in)
	})
}

// get reads the document {id} of the request's property.
func get[Res any](db *dbtx.DB, table, what string, fn func(ctx context.Context, q dbtx.Querier, id uuid.UUID) (Res, error)) http.HandlerFunc {
	return handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Res, error) {
		var zero Res
		rid, err := handle.ID(r)
		if err != nil {
			return zero, err
		}
		if err := inProperty(ctx, tx, table, rid, handle.Property(ctx), what); err != nil {
			return zero, err
		}
		return fn(ctx, tx, rid)
	})
}

func qDate(r *http.Request, name string) (*time.Time, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return nil, nil
	}
	d, err := parseDate(v)
	if err != nil {
		return nil, errs.Validation("invalid_query", name+" must be a date (YYYY-MM-DD)", errs.Field(name, "invalid_date", "YYYY-MM-DD"))
	}
	return &d, nil
}

func qBool(r *http.Request, name string) bool {
	b, _ := strconv.ParseBool(r.URL.Query().Get(name))
	return b
}

// docFilter reads the common document list parameters.
func docFilter(r *http.Request) (DocFilter, error) {
	lp := httpx.ParseList(r)
	f := DocFilter{Status: lp.Filters["status"], Q: lp.Q, Limit: lp.Limit}
	var err error
	if f.WarehouseID, err = handle.QueryUUID(r, "warehouseId"); err != nil {
		return f, err
	}
	if f.AssetID, err = handle.QueryUUID(r, "assetId"); err != nil {
		return f, err
	}
	if f.From, err = qDate(r, "from"); err != nil {
		return f, err
	}
	f.To, err = qDate(r, "to")
	return f, err
}

var listQuery = []route.Param{{Name: "filter[status]", Description: "comma separated statuses"}, {Name: "warehouseId"}, {Name: "q"}}

// ── stock ─────────────────────────────────────────────────────────────────

func (s *Stock) registerStock(reg *route.Registry) {
	db := s.DB
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/stock-balances", Summary: "Stock Balance per warehouse × item (× batch) with value",
		Permission: "inventory.stock_balance.view", Response: StockBalanceRow{}, List: true,
		Query: []route.Param{{Name: "warehouseId"}, {Name: "itemId"}, {Name: "categoryId"}, {Name: "q"}, {Name: "belowReorder", Type: "boolean"},
			{Name: "byBatch", Type: "boolean"}, {Name: "includeZero", Type: "boolean"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[StockBalanceRow], error) {
			f := BalanceFilter{Q: r.URL.Query().Get("q"), BelowReorder: qBool(r, "belowReorder"), ByBatch: qBool(r, "byBatch"), IncludeZero: qBool(r, "includeZero"),
				Limit: httpx.ParseList(r).Limit}
			var err error
			if f.WarehouseID, err = handle.QueryUUID(r, "warehouseId"); err != nil {
				return httpx.Page[StockBalanceRow]{}, err
			}
			if f.ItemID, err = handle.QueryUUID(r, "itemId"); err != nil {
				return httpx.Page[StockBalanceRow]{}, err
			}
			if f.CategoryID, err = handle.QueryUUID(r, "categoryId"); err != nil {
				return httpx.Page[StockBalanceRow]{}, err
			}
			return handle.Page(StockBalances(ctx, tx, handle.Property(ctx), f))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/barcodes/{code}", Summary: "Scan: item, UOM and stock of an item or carton barcode",
		Permission: "inventory.stock_balance.view", Response: BarcodeLookup{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (BarcodeLookup, error) {
			return LookupBarcode(ctx, tx, handle.Property(ctx), chi.URLParam(r, "code"))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/stock-movements", Summary: "Stock Movement ledger (append-only)",
		Permission: "inventory.stock_movement.view", Response: StockMovement{}, List: true,
		Query: []route.Param{{Name: "warehouseId"}, {Name: "itemId"}, {Name: "filter[movementType]"}, {Name: "filter[sourceType]"}, {Name: "sourceId"},
			{Name: "from"}, {Name: "to"}, {Name: "flagged", Type: "boolean"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[StockMovement], error) {
			lp := httpx.ParseList(r)
			f := MovementFilter{MovementType: lp.Filters["movementType"], SourceType: lp.Filters["sourceType"], Flagged: qBool(r, "flagged"), Limit: lp.Limit}
			var err error
			if f.WarehouseID, err = handle.QueryUUID(r, "warehouseId"); err != nil {
				return httpx.Page[StockMovement]{}, err
			}
			if f.ItemID, err = handle.QueryUUID(r, "itemId"); err != nil {
				return httpx.Page[StockMovement]{}, err
			}
			if f.SourceID, err = handle.QueryUUID(r, "sourceId"); err != nil {
				return httpx.Page[StockMovement]{}, err
			}
			if f.From, err = qDate(r, "from"); err != nil {
				return httpx.Page[StockMovement]{}, err
			}
			if f.To, err = qDate(r, "to"); err != nil {
				return httpx.Page[StockMovement]{}, err
			}
			return handle.Page(ListMovements(ctx, tx, handle.Property(ctx), f))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/stock-movements/{id}", Summary: "Stock movement with lines",
		Permission: "inventory.stock_movement.view", Response: StockMovement{},
		Handler: get(db, "inventory.stock_movements", "stock movement", GetMovement)})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/stock-movements/{id}:reverse",
		Summary: "Reverse a consumption, issue or waste at the same cost (refund restock, FR-CNS-02)", Permission: "inventory.stock_movement.reverse",
		Request: StockReasonInput{}, Response: StockMovement{},
		Handler: act(db, "inventory.stock_movements", "stock movement", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in StockReasonInput) (StockMovement, error) {
			return s.ReverseMovement(ctx, tx, property, id, in.Reason)
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/stock-card", Summary: "Stock card of an item in a warehouse (balance = Σ movements)",
		Permission: "inventory.stock_movement.view", Response: StockCard{},
		Query: []route.Param{{Name: "itemId", Required: true}, {Name: "warehouseId", Required: true}, {Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (StockCard, error) {
			item, err := handle.QueryUUID(r, "itemId")
			if err != nil || item == nil {
				return StockCard{}, firstErr(err, handle.Invalid("itemId", "required", "itemId is required"))
			}
			wh, err := handle.QueryUUID(r, "warehouseId")
			if err != nil || wh == nil {
				return StockCard{}, firstErr(err, handle.Invalid("warehouseId", "required", "warehouseId is required"))
			}
			t := today(ctx, tx)
			from, err := handle.QueryDate(r, "from", time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return StockCard{}, err
			}
			to, err := handle.QueryDate(r, "to", t)
			if err != nil {
				return StockCard{}, err
			}
			return GetStockCard(ctx, tx, handle.Property(ctx), *item, *wh, from, to)
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/pick-suggestions", Summary: "FEFO pick suggestion (first expired, first out)",
		Permission: "inventory.stock_balance.view", Response: FefoPickLine{}, List: true,
		Query: []route.Param{{Name: "warehouseId", Required: true}, {Name: "itemId", Required: true}, {Name: "quantity", Required: true}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[FefoPickLine], error) {
			wh, err := handle.QueryUUID(r, "warehouseId")
			if err != nil || wh == nil {
				return httpx.Page[FefoPickLine]{}, firstErr(err, handle.Invalid("warehouseId", "required", "warehouseId is required"))
			}
			item, err := handle.QueryUUID(r, "itemId")
			if err != nil || item == nil {
				return httpx.Page[FefoPickLine]{}, firstErr(err, handle.Invalid("itemId", "required", "itemId is required"))
			}
			need, err := qty("quantity", r.URL.Query().Get("quantity"), true)
			if err != nil {
				return httpx.Page[FefoPickLine]{}, err
			}
			if err := inProperty(ctx, tx, "inventory.warehouses", *wh, handle.Property(ctx), "warehouse"); err != nil {
				return httpx.Page[FefoPickLine]{}, err
			}
			return handle.Page(PickSuggestion(ctx, tx, *wh, *item, need))
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/issues", Summary: "Issue Stock to a department / cost center with reason (FR-STK-03)",
		Permission: "inventory.issue.create", Request: StockIssueInput{}, Response: StoreRequisition{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in StockIssueInput) (StoreRequisition, error) {
			origin := "manual"
			if in.Origin == "ops" {
				origin = "ops"
			}
			return s.Issue(ctx, tx, handle.Property(ctx), in, origin)
		})})
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// ── documents ─────────────────────────────────────────────────────────────

func (s *Stock) registerDocuments(reg *route.Registry) {
	db := s.DB
	// Store Requisition
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/requisitions", Summary: "Store Requisitions (incl. issues and spare part requests)",
		Permission: "inventory.requisition.view", Response: StoreRequisition{}, List: true,
		Query: append([]route.Param{{Name: "filter[requestType]", Enum: []string{"store", "department", "spare_part", "issue"}}, {Name: "assetId"}}, listQuery...),
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[StoreRequisition], error) {
			f, err := docFilter(r)
			if err != nil {
				return httpx.Page[StoreRequisition]{}, err
			}
			f.Kind = httpx.ParseList(r).Filters["requestType"]
			return handle.Page(ListRequisitions(ctx, tx, handle.Property(ctx), f))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/requisitions/{id}", Summary: "Store Requisition with lines and fulfilments",
		Permission: "inventory.requisition.view", Response: StoreRequisition{}, Handler: get(db, "inventory.requisitions", "requisition", GetRequisition)})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/requisitions", Summary: "Create Store Requisition (outlet / kitchen → store)",
		Permission: "inventory.requisition.create", Request: StoreRequisitionInput{}, Response: StoreRequisition{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in StoreRequisitionInput) (StoreRequisition, error) {
			origin := "manual"
			if in.Origin == "ops" {
				origin = "ops"
			}
			return s.CreateRequisition(ctx, tx, handle.Property(ctx), in, origin)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/requisitions/{id}:submit", Summary: "Submit (approval per Inventory Policies)",
		Permission: "inventory.requisition.submit", Response: StoreRequisition{},
		Handler: act(db, "inventory.requisitions", "requisition", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, _ handle.Empty) (StoreRequisition, error) {
			return s.SubmitRequisition(ctx, tx, property, id)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/requisitions/{id}:fulfill", Summary: "Issue (part of) an approved requisition",
		Permission: "inventory.requisition.fulfill", Request: RequisitionFulfillInput{}, Response: StoreRequisition{},
		Handler: act(db, "inventory.requisitions", "requisition", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in RequisitionFulfillInput) (StoreRequisition, error) {
			return s.FulfillRequisition(ctx, tx, property, id, in)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/requisitions/{id}:cancel", Summary: "Cancel (a partially issued requisition is closed)",
		Permission: "inventory.requisition.cancel", Request: StockReasonInput{}, Response: StoreRequisition{},
		Handler: act(db, "inventory.requisitions", "requisition", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in StockReasonInput) (StoreRequisition, error) {
			return s.CancelRequisition(ctx, tx, property, id, in.Reason)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/requisitions/{id}:request-purchase",
		Summary: "Request a Purchase Requisition for what the store cannot supply (inventory.reorder_needed)", Permission: "inventory.requisition.request_purchase",
		Response: StoreRequisition{},
		Handler: act(db, "inventory.requisitions", "requisition", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, _ handle.Empty) (StoreRequisition, error) {
			return s.RequestPurchase(ctx, tx, property, id)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/spare-part-requests", Tag: tagAssets,
		Summary: "Spare Part Request for an asset (Golf Staff, FR-OPS-P4-03)", Permission: "inventory.spare_part_request.create",
		Request: SparePartRequestInput{}, Response: StoreRequisition{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in SparePartRequestInput) (StoreRequisition, error) {
			return s.RequestSpareParts(ctx, tx, handle.Property(ctx), in)
		})})

	// Stock Transfer
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/transfers", Summary: "Stock Transfers", Permission: "inventory.transfer.view",
		Response: StockTransfer{}, List: true, Query: listQuery,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[StockTransfer], error) {
			f, err := docFilter(r)
			if err != nil {
				return httpx.Page[StockTransfer]{}, err
			}
			return handle.Page(ListTransfers(ctx, tx, handle.Property(ctx), f))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/transfers/{id}", Summary: "Stock Transfer with lines", Permission: "inventory.transfer.view",
		Response: StockTransfer{}, Handler: get(db, "inventory.transfers", "transfer", GetTransfer)})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/transfers", Summary: "Create Stock Transfer", Permission: "inventory.transfer.create",
		Request: StockTransferInput{}, Response: StockTransfer{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in StockTransferInput) (StockTransfer, error) {
			return s.CreateTransfer(ctx, tx, handle.Property(ctx), in)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/transfers/{id}:ship", Summary: "Ship (In Transit)", Permission: "inventory.transfer.ship",
		Response: StockTransfer{},
		Handler: act(db, "inventory.transfers", "transfer", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, _ handle.Empty) (StockTransfer, error) {
			return s.ShipTransfer(ctx, tx, property, id)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/transfers/{id}:receive", Summary: "Receive; shipped − received differences are recorded",
		Permission: "inventory.transfer.receive", Request: TransferReceiveInput{}, Response: StockTransfer{},
		Handler: act(db, "inventory.transfers", "transfer", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in TransferReceiveInput) (StockTransfer, error) {
			return s.ReceiveTransfer(ctx, tx, property, id, in)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/transfers/{id}:cancel", Summary: "Cancel a draft transfer", Permission: "inventory.transfer.cancel",
		Request: StockReasonInput{}, Response: StockTransfer{},
		Handler: act(db, "inventory.transfers", "transfer", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in StockReasonInput) (StockTransfer, error) {
			return s.CancelTransfer(ctx, tx, property, id, in.Reason)
		})})

	// Stock Adjustment
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/adjustments", Summary: "Stock Adjustments", Permission: "inventory.adjustment.view",
		Response: StockAdjustment{}, List: true, Query: append([]route.Param{{Name: "reason"}}, listQuery...),
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[StockAdjustment], error) {
			f, err := docFilter(r)
			if err != nil {
				return httpx.Page[StockAdjustment]{}, err
			}
			f.Kind = r.URL.Query().Get("reason")
			return handle.Page(ListAdjustments(ctx, tx, handle.Property(ctx), f))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/adjustments/{id}", Summary: "Stock Adjustment with lines", Permission: "inventory.adjustment.view",
		Response: StockAdjustment{}, Handler: get(db, "inventory.adjustments", "adjustment", GetAdjustment)})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/adjustments", Summary: "Create Stock Adjustment with reason",
		Permission: "inventory.adjustment.create", Request: StockAdjustmentInput{}, Response: StockAdjustment{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in StockAdjustmentInput) (StockAdjustment, error) {
			return s.CreateAdjustment(ctx, tx, handle.Property(ctx), in)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/adjustments/{id}:submit", Summary: "Submit: posted, through approval above the threshold",
		Permission: "inventory.adjustment.submit", Response: StockAdjustment{},
		Handler: act(db, "inventory.adjustments", "adjustment", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, _ handle.Empty) (StockAdjustment, error) {
			return s.SubmitAdjustment(ctx, tx, property, id)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/adjustments/{id}:cancel", Summary: "Cancel a draft or pending adjustment",
		Permission: "inventory.adjustment.cancel", Request: StockReasonInput{}, Response: StockAdjustment{},
		Handler: act(db, "inventory.adjustments", "adjustment", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in StockReasonInput) (StockAdjustment, error) {
			return s.CancelAdjustment(ctx, tx, property, id, in.Reason)
		})})

	// Stock Opname
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/stock-opnames", Summary: "Stock Opnames", Permission: "inventory.stock_opname.view",
		Response: StockOpname{}, List: true, Query: listQuery,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[StockOpname], error) {
			f, err := docFilter(r)
			if err != nil {
				return httpx.Page[StockOpname]{}, err
			}
			return handle.Page(ListOpnames(ctx, tx, handle.Property(ctx), f))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/stock-opnames/{id}", Summary: "Stock Opname with its count sheet (blind while counting)",
		Permission: "inventory.stock_opname.view", Response: StockOpname{}, Handler: get(db, "inventory.stock_opnames", "stock opname", GetOpname)})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/stock-opnames", Summary: "Start Stock Opname (snapshot; freeze or cut-off)",
		Permission: "inventory.stock_opname.create", Request: StockOpnameInput{}, Response: StockOpname{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in StockOpnameInput) (StockOpname, error) {
			return s.StartOpname(ctx, tx, handle.Property(ctx), in)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/stock-opnames/{id}:count",
		Summary: "Record counts (barcode; offline sync with Idempotency-Key)", Permission: "inventory.stock_opname.count", Request: OpnameCountInput{},
		Response: StockOpname{}, Idempotent: true,
		Handler: act(db, "inventory.stock_opnames", "stock opname", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in OpnameCountInput) (StockOpname, error) {
			return s.CountOpname(ctx, tx, property, id, in)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/stock-opnames/{id}:submit", Summary: "Close the count: variance per line (Counted)",
		Permission: "inventory.stock_opname.submit", Request: SubmitOpnameInput{}, Response: StockOpname{},
		Handler: act(db, "inventory.stock_opnames", "stock opname", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in SubmitOpnameInput) (StockOpname, error) {
			return s.SubmitOpname(ctx, tx, property, id, in)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/stock-opnames/{id}:recount", Summary: "Recount lines above tolerance (back to In Progress)",
		Permission: "inventory.stock_opname.submit", Request: OpnameRecountInput{}, Response: StockOpname{},
		Handler: act(db, "inventory.stock_opnames", "stock opname", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in OpnameRecountInput) (StockOpname, error) {
			return s.RecountOpname(ctx, tx, property, id, in)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/stock-opnames/{id}:post", Summary: "Post the variance (approval above tolerance)",
		Permission: "inventory.stock_opname.post", Response: StockOpname{},
		Handler: act(db, "inventory.stock_opnames", "stock opname", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, _ handle.Empty) (StockOpname, error) {
			return s.PostOpname(ctx, tx, property, id)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/stock-opnames/{id}:cancel", Summary: "Cancel an open stock opname",
		Permission: "inventory.stock_opname.cancel", Request: StockReasonInput{}, Response: StockOpname{},
		Handler: act(db, "inventory.stock_opnames", "stock opname", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in StockReasonInput) (StockOpname, error) {
			return s.CancelOpname(ctx, tx, property, id, in.Reason)
		})})

	// Production
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/production-orders", Summary: "Production Orders (semi-finished items)",
		Permission: "inventory.production.view", Response: ProductionOrder{}, List: true, Query: append([]route.Param{{Name: "from"}, {Name: "to"}}, listQuery...),
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ProductionOrder], error) {
			f, err := docFilter(r)
			if err != nil {
				return httpx.Page[ProductionOrder]{}, err
			}
			return handle.Page(ListProduction(ctx, tx, handle.Property(ctx), f))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/production-orders/{id}", Summary: "Production Order with ingredients",
		Permission: "inventory.production.view", Response: ProductionOrder{}, Handler: get(db, "inventory.production_orders", "production order", GetProduction)})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/production-orders", Summary: "Create Production Order",
		Permission: "inventory.production.create", Request: ProductionOrderInput{}, Response: ProductionOrder{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ProductionOrderInput) (ProductionOrder, error) {
			return s.CreateProduction(ctx, tx, handle.Property(ctx), in)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/production-orders/{id}:complete",
		Summary: "Complete: ingredients out, output in with actual yield at ingredient cost", Permission: "inventory.production.complete",
		Request: CompleteProductionInput{}, Response: ProductionOrder{},
		Handler: act(db, "inventory.production_orders", "production order", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in CompleteProductionInput) (ProductionOrder, error) {
			return s.CompleteProduction(ctx, tx, property, id, in)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/production-orders/{id}:cancel", Summary: "Cancel a draft production order",
		Permission: "inventory.production.cancel", Request: StockReasonInput{}, Response: ProductionOrder{},
		Handler: act(db, "inventory.production_orders", "production order", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in StockReasonInput) (ProductionOrder, error) {
			return s.CancelProduction(ctx, tx, property, id, in.Reason)
		})})

	// Waste
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/waste", Summary: "Waste / Spoilage records", Permission: "inventory.waste.view",
		Response: WasteRecord{}, List: true, Query: append([]route.Param{{Name: "reason"}, {Name: "from"}, {Name: "to"}}, listQuery...),
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[WasteRecord], error) {
			f, err := docFilter(r)
			if err != nil {
				return httpx.Page[WasteRecord]{}, err
			}
			f.Kind = r.URL.Query().Get("reason")
			return handle.Page(ListWaste(ctx, tx, handle.Property(ctx), f))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/waste/{id}", Summary: "Waste record with lines", Permission: "inventory.waste.view",
		Response: WasteRecord{}, Handler: get(db, "inventory.waste_records", "waste record", GetWaste)})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/waste", Summary: "Record Waste / Spoilage with reason", Permission: "inventory.waste.create",
		Request: WasteRecordInput{}, Response: WasteRecord{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in WasteRecordInput) (WasteRecord, error) {
			return s.RecordWaste(ctx, tx, handle.Property(ctx), in)
		})})

	// Consignment
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/consignment-stock", Summary: "Consignment stock per supplier (supplier-owned)",
		Permission: "inventory.consignment.view", Response: ConsignmentStock{}, List: true, Query: []route.Param{{Name: "supplierId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ConsignmentStock], error) {
			sup, err := handle.QueryUUID(r, "supplierId")
			if err != nil {
				return httpx.Page[ConsignmentStock]{}, err
			}
			return handle.Page(ListConsignmentStock(ctx, tx, handle.Property(ctx), sup))
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/consignment-movements", Summary: "Receive or return consignment stock (no value)",
		Permission: "inventory.consignment.create", Request: ConsignmentMovementInput{}, Response: StockMovement{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ConsignmentMovementInput) (StockMovement, error) {
			return s.ConsignmentMovement(ctx, tx, handle.Property(ctx), in)
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/consignment-settlements",
		Summary: "Monthly consignment settlement per supplier (sales, club commission, payable)", Permission: "inventory.consignment.view",
		Response: ConsignmentSettlementPreview{}, List: true, Query: []route.Param{{Name: "period", Description: "YYYY-MM (default this month)"}, {Name: "supplierId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ConsignmentSettlementPreview], error) {
			sup, err := handle.QueryUUID(r, "supplierId")
			if err != nil {
				return httpx.Page[ConsignmentSettlementPreview]{}, err
			}
			period := r.URL.Query().Get("period")
			if period == "" {
				period = today(ctx, tx).Format("2006-01")
			}
			return handle.Page(ConsignmentSettlements(ctx, tx, handle.Property(ctx), period, sup))
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/consignment-settlements", Summary: "Issue the monthly settlement of a supplier (AP basis)",
		Permission: "inventory.consignment.settle", Request: ConsignmentSettlementInput{}, Response: ConsignmentSettlement{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ConsignmentSettlementInput) (ConsignmentSettlement, error) {
			return s.SettleConsignment(ctx, tx, handle.Property(ctx), in)
		})})

	// Opening stock (EP-29)
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/opening-stock:import",
		Summary: "Import opening stock per warehouse with value (preview / commit)", Permission: "inventory.opening_stock.import",
		Request: OpeningStockInput{}, Response: OpeningStockResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in OpeningStockInput) (OpeningStockResult, error) {
			property := handle.Property(ctx)
			res, err := s.ImportOpeningStock(ctx, tx, property, in)
			if err != nil || res.Status == "completed" {
				return res, err
			}
			return res, record(ctx, tx, property, "inventory.opening_stock", uuid.NewSHA1(property, []byte(in.Filename+in.BusinessDate)), in.Filename,
				"import_"+in.Mode, nil, map[string]any{"status": res.Status, "rows": res.TotalRows, "errors": len(res.Errors), "totalValue": res.TotalValue}, "")
		})})
}

// ── analysis & replenishment ──────────────────────────────────────────────

func (s *Stock) registerAnalysis(reg *route.Registry) {
	db := s.DB
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/valuation", Summary: "Stock Valuation as of a date (= GL inventory account)",
		Permission: "inventory.valuation.view", Response: StockValuation{},
		Query: []route.Param{{Name: "asOf"}, {Name: "groupBy", Enum: []string{"item", "category", "warehouse"}}, {Name: "warehouseId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (StockValuation, error) {
			asOf, err := handle.QueryDate(r, "asOf", today(ctx, tx))
			if err != nil {
				return StockValuation{}, err
			}
			wh, err := handle.QueryUUID(r, "warehouseId")
			if err != nil {
				return StockValuation{}, err
			}
			return ValuateStock(ctx, tx, handle.Property(ctx), asOf, r.URL.Query().Get("groupBy"), wh)
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/consumption-variance", Summary: "Theoretical vs Actual consumption per outlet warehouse",
		Permission: "inventory.food_cost.view", Response: ConsumptionVarianceRow{}, List: true,
		Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "warehouseId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ConsumptionVarianceRow], error) {
			t := today(ctx, tx)
			from, err := handle.QueryDate(r, "from", time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return httpx.Page[ConsumptionVarianceRow]{}, err
			}
			to, err := handle.QueryDate(r, "to", t)
			if err != nil {
				return httpx.Page[ConsumptionVarianceRow]{}, err
			}
			wh, err := handle.QueryUUID(r, "warehouseId")
			if err != nil {
				return httpx.Page[ConsumptionVarianceRow]{}, err
			}
			return handle.Page(ConsumptionVariance(ctx, tx, handle.Property(ctx), from, to, wh))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/expiry", Summary: "Batches in stock expiring within N days (expired included)",
		Permission: "inventory.expiry.view", Response: ExpiringBatch{}, List: true, Query: []route.Param{{Name: "days", Type: "integer"}, {Name: "warehouseId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ExpiringBatch], error) {
			property := handle.Property(ctx)
			pol, err := LoadPolicy(ctx, tx, property)
			if err != nil {
				return httpx.Page[ExpiringBatch]{}, err
			}
			wh, err := handle.QueryUUID(r, "warehouseId")
			if err != nil {
				return httpx.Page[ExpiringBatch]{}, err
			}
			return handle.Page(Expiring(ctx, tx, property, handle.QueryInt(r, "days", pol.ExpiryAlertDays), wh))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/slow-moving", Summary: "Items in stock without outbound movement for N days",
		Permission: "inventory.replenishment.view", Response: SlowMovingRow{}, List: true, Query: []route.Param{{Name: "days", Type: "integer"}, {Name: "warehouseId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[SlowMovingRow], error) {
			property := handle.Property(ctx)
			pol, err := LoadPolicy(ctx, tx, property)
			if err != nil {
				return httpx.Page[SlowMovingRow]{}, err
			}
			wh, err := handle.QueryUUID(r, "warehouseId")
			if err != nil {
				return httpx.Page[SlowMovingRow]{}, err
			}
			return handle.Page(SlowMoving(ctx, tx, property, handle.QueryInt(r, "days", pol.SlowMovingDays), wh))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/replenishment", Summary: "Replenishment suggestions (below reorder point / par)",
		Permission: "inventory.replenishment.view", Response: ReplenishmentSuggestion{}, List: true, Query: []route.Param{{Name: "warehouseId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ReplenishmentSuggestion], error) {
			wh, err := handle.QueryUUID(r, "warehouseId")
			if err != nil {
				return httpx.Page[ReplenishmentSuggestion]{}, err
			}
			return handle.Page(Suggestions(ctx, tx, handle.Property(ctx), wh))
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/replenishment:run",
		Summary: "Run replenishment now: automatic PR (once per day) and outlet requisitions", Permission: "inventory.replenishment.run",
		Response: ReplenishmentResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ReplenishmentResult, error) {
			property := handle.Property(ctx)
			res, err := s.Replenish(ctx, tx, property)
			if err != nil {
				return res, err
			}
			return res, record(ctx, tx, property, "inventory.replenishment", property, res.BusinessDate, "run", nil,
				map[string]any{"purchase": len(res.Purchase), "requisitions": res.Requisitions, "skipped": res.Skipped}, "")
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/posting-exceptions", Summary: "Automatic postings that need attention",
		Permission: "inventory.posting_exception.view", Response: InventoryPostingException{}, List: true, Query: []route.Param{{Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[InventoryPostingException], error) {
			lp := httpx.ParseList(r)
			return handle.Page(ListExceptions(ctx, tx, handle.Property(ctx), lp.Filters["status"], lp.Limit))
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/posting-exceptions/{id}:resolve", Summary: "Resolve a posting exception",
		Permission: "inventory.posting_exception.resolve", Request: PostingExceptionResolveInput{}, Response: InventoryPostingException{},
		Handler: act(db, "inventory.posting_exceptions", "posting exception", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in PostingExceptionResolveInput) (InventoryPostingException, error) {
			x, err := s.ResolveException(ctx, tx, property, id, in)
			if err != nil {
				return x, err
			}
			return x, record(ctx, tx, property, "inventory.posting_exception", id, x.SourceType, "resolve", map[string]any{"status": "open"},
				map[string]any{"status": "resolved"}, in.Resolution)
		})})
}

// ── assets & equipment ────────────────────────────────────────────────────

func (s *Stock) registerAssets(reg *route.Registry) {
	db := s.DB
	assetAct := func(path, summary, perm string, req any, fn http.HandlerFunc) {
		add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/assets/{id}" + path, Tag: tagAssets, Summary: summary, Permission: perm,
			Request: req, Response: AssetSummary{}, Handler: fn})
	}
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/assets/{id}/history", Tag: tagAssets,
		Summary: "Usage, maintenance, spare part and depreciation history", Permission: "inventory.asset.view", Response: AssetHistory{},
		Handler: get(db, "inventory.assets", "asset", GetAssetHistory)})
	assetAct(":dispose", "Dispose / write off (approval)", "inventory.asset.dispose", AssetDisposeInput{},
		act(db, "inventory.assets", "asset", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in AssetDisposeInput) (AssetSummary, error) {
			return s.DisposeAsset(ctx, tx, property, id, in)
		}))
	assetAct(":checkout", "Rental equipment out", "inventory.asset.rent", AssetRentalInput{},
		act(db, "inventory.assets", "asset", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in AssetRentalInput) (AssetSummary, error) {
			return s.CheckoutAsset(ctx, tx, property, id, in)
		}))
	assetAct(":return", "Rental equipment back", "inventory.asset.rent", AssetRentalInput{},
		act(db, "inventory.assets", "asset", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in AssetRentalInput) (AssetSummary, error) {
			return s.ReturnAsset(ctx, tx, property, id, in)
		}))
	assetAct(":record-usage", "Record operating hours", "inventory.asset.record_usage", AssetUsageInput{},
		act(db, "inventory.assets", "asset", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in AssetUsageInput) (AssetSummary, error) {
			return s.RecordUsage(ctx, tx, property, id, in)
		}))
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/maintenance-due", Tag: tagAssets, Summary: "Maintenance schedules with their due state",
		Permission: "inventory.maintenance.view", Response: AssetMaintenanceDue{}, List: true,
		Query: []route.Param{{Name: "assetId"}, {Name: "days", Type: "integer"}, {Name: "dueOnly", Type: "boolean"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[AssetMaintenanceDue], error) {
			asset, err := handle.QueryUUID(r, "assetId")
			if err != nil {
				return httpx.Page[AssetMaintenanceDue]{}, err
			}
			list, err := MaintenanceDueList(ctx, tx, handle.Property(ctx), asset, handle.QueryInt(r, "days", 0))
			if err != nil || !qBool(r, "dueOnly") {
				return handle.Page(list, err)
			}
			out := []AssetMaintenanceDue{}
			for _, m := range list {
				if m.Due {
					out = append(out, m)
				}
			}
			return handle.Page(out, nil)
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/maintenance-records", Tag: tagAssets, Summary: "Maintenance work orders",
		Permission: "inventory.maintenance.view", Response: AssetMaintenanceRecord{}, List: true, Query: append([]route.Param{{Name: "assetId"}}, listQuery...),
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[AssetMaintenanceRecord], error) {
			f, err := docFilter(r)
			if err != nil {
				return httpx.Page[AssetMaintenanceRecord]{}, err
			}
			return handle.Page(ListMaintenance(ctx, tx, handle.Property(ctx), f))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/maintenance-records/{id}", Tag: tagAssets, Summary: "Maintenance work order",
		Permission: "inventory.maintenance.view", Response: AssetMaintenanceRecord{},
		Handler: get(db, "inventory.maintenance_records", "maintenance record", GetMaintenance)})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/maintenance-records", Tag: tagAssets, Summary: "Open a maintenance work order",
		Permission: "inventory.maintenance.create", Request: AssetMaintenanceInput{}, Response: AssetMaintenanceRecord{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in AssetMaintenanceInput) (AssetMaintenanceRecord, error) {
			return s.CreateMaintenance(ctx, tx, handle.Property(ctx), in)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/maintenance-records/{id}:start", Tag: tagAssets, Summary: "Start: the asset is in maintenance",
		Permission: "inventory.maintenance.update", Response: AssetMaintenanceRecord{},
		Handler: act(db, "inventory.maintenance_records", "maintenance record", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, _ handle.Empty) (AssetMaintenanceRecord, error) {
			return s.StartMaintenance(ctx, tx, property, id)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/maintenance-records/{id}:complete", Tag: tagAssets,
		Summary: "Complete with costs; spare parts are issued from the Engineering store", Permission: "inventory.maintenance.complete",
		Request: AssetMaintenanceCompleteInput{}, Response: AssetMaintenanceRecord{},
		Handler: act(db, "inventory.maintenance_records", "maintenance record", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in AssetMaintenanceCompleteInput) (AssetMaintenanceRecord, error) {
			return s.CompleteMaintenance(ctx, tx, property, id, in)
		})})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/maintenance-records/{id}:cancel", Tag: tagAssets, Summary: "Cancel a work order",
		Permission: "inventory.maintenance.update", Request: StockReasonInput{}, Response: AssetMaintenanceRecord{},
		Handler: act(db, "inventory.maintenance_records", "maintenance record", func(ctx context.Context, tx pgx.Tx, property, id uuid.UUID, in StockReasonInput) (AssetMaintenanceRecord, error) {
			return s.CancelMaintenance(ctx, tx, property, id, in.Reason)
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/depreciation-runs", Tag: tagAssets, Summary: "Monthly depreciation runs",
		Permission: "inventory.depreciation.view", Response: AssetDepreciationRun{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[AssetDepreciationRun], error) {
			return handle.Page(ListDepreciationRuns(ctx, tx, handle.Property(ctx), httpx.ParseList(r).Limit))
		})})
	add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/depreciation-runs/{id}", Tag: tagAssets, Summary: "Depreciation run with lines",
		Permission: "inventory.depreciation.view", Response: AssetDepreciationRun{},
		Handler: get(db, "inventory.depreciation_runs", "depreciation run", GetDepreciationRun)})
	add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/inventory/depreciation-runs", Tag: tagAssets,
		Summary: "Post the depreciation of a month (inventory.asset_depreciated)", Permission: "inventory.depreciation.run",
		Request: DepreciationRunInput{}, Response: AssetDepreciationRun{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in DepreciationRunInput) (AssetDepreciationRun, error) {
			property := handle.Property(ctx)
			run, err := s.RunDepreciation(ctx, tx, property, in.Period)
			if err != nil {
				return run, err
			}
			return run, record(ctx, tx, property, "inventory.depreciation_run", run.ID, run.Number, "create", nil,
				map[string]any{"period": run.Period, "total": run.Total, "assets": run.Assets}, "")
		})})
}

var _ = decimal.Zero
