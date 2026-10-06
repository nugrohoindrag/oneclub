# Inventory cut-over (PRD P4 EP-29 FR-MIG-P4-03 / FR-MIG-P4-06 / FR-MIG-P4-07)

Brings the club's stock into OneClub Inventory at Release 4 (R4.2 Inventory + Procurement): **master data**
(items, UOM conversions, categories, warehouses and stock locations, assets), the **cut-over stock opname** and the
**opening stock** per warehouse with value. Purchasing data (suppliers, price lists, open POs) follows
`procurement-migration-p4.md`; the GL side (inventory accounts in the opening balances) follows
`accounting-migration-p4.md`.

## Tools

| Step | Screen (Staff App) | API |
|---|---|---|
| Items, UOM, categories, warehouses, locations, assets | Settings → Master Data Import | `inventory.item`, `inventory.uom`, `inventory.warehouse`, … resources |
| Opening stock (preview / commit) | Inventory → Stock Balance → *Import Opening Stock* | `POST /api/v1/inventory/opening-stock:import` `{mode, filename, businessDate, csv}` |
| Stock check after import | Inventory → Stock Balance, Valuation | `GET /api/v1/inventory/valuation` |
| Spot-check opname | Inventory → Stock Opname / Ops **Warehouse → Opname** | `POST /api/v1/inventory/stock-opnames` `{warehouseId, freeze, blind}`, `:count`, `:submit`, `:post` |

Opening stock CSV: `warehouseCode,itemCode,quantity,unitCost[,uom,batchNo,expiryDate,serialNos]` (serial numbers
separated by `|`). `preview` validates every row and returns per warehouse the quantity and value a commit would
post; `commit` posts one opening adjustment per warehouse. An item that already has opening stock in a warehouse
is refused, so a committed file cannot be posted twice.

## Dating rule (opening stock vs GL cut-over)

* The opening stock is dated the **day before the GL cut-over date** of the property's accounting book
  (`businessDate = D − 1`, D = `cutOverDate` of Accounting → Setup). Accounting does not post documents dated before
  the cut-over: the stock value is in the GL through the **opening balances** of the inventory accounts imported
  from Excel Finance.
* Check before posting the GL opening balances: Inventory Valuation at D − 1 per inventory account = the inventory
  lines of the opening balance file. A difference is either a count / cost issue (fix the opening stock with an
  adjustment dated D − 1 before the GL opening is posted) or an Excel error (fix the opening balance file).
* If Inventory goes live **before** the accounting book (R4.2 before R4.3), every movement between the inventory
  go-live and D is also before the GL cut-over: the GL inventory opening is then Excel's balance at D − 1, which
  must equal the Valuation report at D − 1 (same check).

## Steps (dry run at least twice on Staging, FR-MIG-P4-07)

| When | Step | Evidence |
|---|---|---|
| D − 4 weeks / D − 2 weeks | **Dry runs 1 and 2** on Staging with the last full count: master data import, opening stock `preview` + `commit`, valuation check, spot-check opname | dry-run log, reconciliation sheet |
| D − 3 days | Master data frozen in the legacy files; final master data import on Production (items, UOM conversions, warehouses, locations, assets) | import results without errors |
| D − 1, 12:00 | **Freeze purchasing**: no new purchase orders in the legacy process; open POs exported for `procurement-migration-p4.md`; suppliers told that deliveries after the freeze are received in OneClub from D | freeze notice |
| D − 1, close of business | **Freeze stock movements**: no receipts, issues or transfers; outlets keep buffer stock for the evening; late deliveries quarantined and received on D | store keepers' sign-off |
| D − 1 evening | **Cut-over opname**: physical count per warehouse / location (two counters per area, blind recount of variances > tolerance, count sheets signed); unit cost = legacy average cost | signed count sheets |
| D − 1 night | Opening stock `preview` per file; reconcile per warehouse: Σ quantity and Σ value = count sheets × cost; `commit` with `businessDate = D − 1` | preview / commit results |
| D, 06:00 | Valuation report = count value per warehouse; spot-check: blind OneClub opname of 20 high-value items on the main store → no variance | valuation print, opname |
| D, 07:00 | **Go / no-go** (below); unfreeze: GRs in OneClub, issues / requisitions, POS consumption (K6) live | minutes |

## Go / no-go

1. Master data imported without errors; every stocked item has a base UOM and a category with accounts.
2. Opening stock committed for every warehouse; per warehouse quantity and value = signed count sheets.
3. Valuation at D − 1 per inventory account = the inventory lines of the GL opening balances (or the difference
   explained and scheduled for correction before the GL opening is posted).
4. Spot-check opname without variance; warehouse devices signed in (Ops → Warehouse).
5. Open POs imported (procurement runbook) — receiving can start.

No-go → the legacy process continues on D; the freeze is lifted in the legacy files and a new cut-over date is set
(the Production data is restored from the pre-cut-over backup before the next attempt, `backup-restore.md`).

## Rollback

* **Before go-live** (opening stock not committed or no movement after it): restore the pre-cut-over backup
  (`backup-restore.md`) and repeat the cut-over on a new date.
* **After go-live**: never delete stock movements. Wrong quantities are corrected by a stock opname or a stock
  adjustment with a reason (approval per Inventory Policies); a wrong unit cost by an adjustment out and in at the right cost. A full return to
  the legacy stock files is only possible before the first period close of Accounting: export the Stock Balance /
  Valuation as of the rollback day and load it into the legacy files.
