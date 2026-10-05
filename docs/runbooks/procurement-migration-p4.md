# Procurement migration (PRD P4 EP-29 FR-MIG-P4-04)

Migrates the club's purchasing data into OneClub Procurement at the Release 4
(R4.2 Inventory + Procurement) cut-over: **suppliers** (with contact and bank
account), **supplier items & price lists** and the **open purchase orders**
with the quantities already received before the cut-over. Open AP (vendor
invoices not yet paid) is migrated by Accounting (`FR-MIG-P4-04` open AP,
EP-23); opening stock by Inventory (`POST /api/v1/inventory/opening-stock:import`).

## Tool

`POST /api/v1/procurement/migration:import` (Staff App: **Procurement →
Procurement Migration**), permission `procurement.migration.import`
(Property Admin, Procurement Manager). The property is the active property.

```json
{ "entity": "suppliers | supplier_items | open_purchase_orders", "mode": "preview | commit",
  "filename": "suppliers.csv", "csv": "<file content>" }
```

* `preview` is the **dry run**: the file is validated row by row and applied
  inside a savepoint that is rolled back; the result shows the errors (row,
  field, problem) and the reconciliation totals a commit would produce.
* `commit` writes the file only when every row is valid (otherwise nothing
  is written and `status = failed`).
* Re-running a file is **idempotent**: suppliers are matched on their code
  (updated), price-list rows on supplier × item × UOM × valid-from (updated),
  purchase orders on their legacy number (skipped, counted in `skipped`).
* Every preview and commit is in the audit log (`procurement.migration`).

## File formats (CSV, header row, UTF-8; lists separated by `|`)

| Entity | Required columns | Optional columns |
|---|---|---|
| `suppliers` | `code,name` | `legalName,npwp,email,phone,address,categories,pkp,withholdingType (none/pph23/pph4_2),paymentTermDays,currency,leadTimeDays,contractSupplier,contactName,contactEmail,contactPhone,bankName,bankAccountNumber,bankAccountName` |
| `supplier_items` | `supplierCode,itemCode,unitPrice` | `uom (default: purchase UOM),supplierItemCode,minOrderQuantity,leadTimeDays,validFrom,validTo,preferred,currency` |
| `open_purchase_orders` (one row per line) | `poNumber,supplierCode,orderDate,warehouseCode,itemCode,quantity,unitPrice` | `uom,receivedQuantity,discountPercent,taxPercent,expectedDate,description,orderType (goods/service),paymentTermDays,currency` |

Prerequisites: items, UOM conversions and warehouses exist (Master Data
Import / inventory migration) before supplier items and open POs.

Open purchase orders are created as **Sent** (approved at the source), with
`source = migration`; a line whose received quantity equals the ordered
quantity is refused (only open orders are migrated). The received quantity
is stored as *opening received quantity*: it is not posted to stock (the
stock comes from the cut-over opname) and is not billed again by vendor
invoices recorded from the order (its payable is in the open AP import).

## Steps (dry run at least twice on Staging, FR-MIG-P4-07)

1. **Freeze purchasing** in the legacy files (no new orders) at the agreed
   cut-over time; export suppliers, price lists and the open-PO list with
   received quantities.
2. Import **suppliers**: `preview` until `status = valid`; check the
   reconciliation `suppliers` against the supplier count of the source; `commit`.
3. Import **supplier items**: same; `supplierItems` = rows of the source price list.
4. Import **open purchase orders**: `preview`; reconcile
   * `purchaseOrders` / `orderLines` = open POs / lines of the source,
   * `orderedValue` = Σ quantity × net price (excl. PPN),
   * `receivedValue` = Σ received quantity × net price,
   * `outstandingValue` = ordered − received = the source's outstanding-PO total;

   then `commit`. The **Outstanding PO Report** (Reports → Procurement
   Reports) and the *Outstanding PO* KPI of the Procurement Performance
   dashboard must show the same outstanding value.
5. Sign-off: Procurement Manager and Finance Manager sign the reconciliation
   (supplier count, price-list rows, outstanding PO value).
6. Go / no-go with Inventory (opening stock) and Accounting (open AP).

## Rollback

Before go-live the imported data can be removed by restoring the pre-cut-over
backup (docs/runbooks/backup-restore.md); after go-live, correct individual
records in the Staff App (suppliers can be archived, migrated POs closed or
cancelled with a reason) — never delete migrated documents in the database.
