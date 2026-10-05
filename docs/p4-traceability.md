# P4 traceability — requirement → implementation → evidence

PRD P4 *Enterprise Back Office* (Release 4). Verified against the code on `staging` at `28ead3a` (2026-10-05): every
status below was checked against the current code and tests, not copied from the earlier gap audit (its gaps were
fixed since by the ledger, finance UX, stock and website fix packages).

Status: **Done** implemented and verified by an automated test · **Done (mock provider for trial)** implemented
end-to-end against a sandbox adapter of the integration layer; the real provider is a configuration step ·
**Partial** implemented with a remaining gap (named in the row) · **Deferred** postponed by a PRD decision (Should)
or a release activity outside the code · **Not built by decision** · *n/a* process decision without behaviour (not
counted). 🟡 marks verification that needs an environment the dev machine does not have (Staging, VPS, club sign-off,
external pen test).

Test locations: `test/e2e/p4_*_test.go` and `test/e2e/p4fix_*_test.go` (Go acceptance tests on real PostgreSQL, run
together with the P0–P3 suites; `p4_zz_fixledger_test.go` sorts last on purpose), `web/e2e/p4.spec.ts` and
`web/e2e/p4fix-website.spec.ts` (Playwright), `test/load/*.js` (k6), unit tests next to the code
(`internal/accounting/posting_coverage_test.go`, `internal/platform/integration/p4_test.go`, `internal/cms/cms_test.go`,
`internal/reporting/p4fix_pdf_test.go`, `internal/inventory/p4_packs_test.go`). Module contracts with P3 (events,
payloads, renames): [`docs/p3-p4-contracts.md`](p3-p4-contracts.md).

P4 adds the modules `inventory` (P4 files `p4_*.go` on P2's recipes and items), `procurement`, `accounting` and `cms`,
and P4 adapters in `internal/platform/integration/p4_*.go`. Wiring lives in `internal/app/p4_*.go`; Staff App screens
in `web/apps/staff/src/p4/{inventory,procurement,accounting,cms}.tsx`.

## Summary

| Status | Count |
|---|---|
| Done | 239 |
| Done (mock provider for trial) | 2 |
| Partial | 8 |
| Deferred | 2 |
| Not built by decision | 1 |
| **Total requirement rows** | **252** |

Counted over the per-ID tables below (FR, acceptance criteria, EP-30, §12 NFR, §16 decisions with behaviour, §9
flows, §11 events); the exit criteria table is a roll-up and is not counted.

## Audit & route coverage

The e2e harness fails the run when a mutating route succeeds without an audit entry, or when any mutating route is
never exercised successfully. Run at `28ead3a`: **1071/1071 mutating routes exercised, 0 without audit** (P0 + P1 +
P2 + P3 + P4). The posting coverage unit test `TestPostingCoverage` fails when a consumed event has no default
posting rule.

## Exit criteria (PRD §13.1)

| # | Criterion | Evidence | Status |
|---|---|---|---|
| 1 | OneClub is the general ledger | `TestP4AccountingLedgerCore`, `TestP4AccountingTransitionAndPeriodGuard` (sign-off stops the Accounting Export), Excel TB comparison in `TestP4FixFinanceDrillDownAndEFaktur`; runbook [`accounting-migration-p4.md`](runbooks/accounting-migration-p4.md) | ✅ code / 🟡 1-month reconciliation with the club |
| 2 | Automatic posting complete | `TestP4AccountingAutomaticPosting`, `TestP4AccountingBillingAR` (posting reconciliation vs Daily Revenue), `TestP4FixLedgerBillingPostings`, `TestP4FixLedgerCommission`, `TestP4FixLedgerCaddySettlement`; unit `TestPostingCoverage` | ✅ / 🟡 0 exceptions at the end of UAT |
| 3 | Procure-to-pay | `TestP4ProcurementProcureToPay`, `TestP4ProcurementMatching`, `TestP4ProcurementRequisitions` | ✅ |
| 4 | Inventory accurate (cut-over opname, Stock Valuation = GL) | `TestP4InventoryOpname`, `TestP4InventoryValuation`, `TestP4FixLedgerInventoryValuation`, `TestP4FixLedgerOpeningStockCarried`; runbook [`inventory-migration-p4.md`](runbooks/inventory-migration-p4.md) | ✅ / 🟡 cut-over opname on site |
| 5 | Actual food cost | `TestP4InventoryConsumption` (theoretical vs actual per outlet) | ✅ |
| 6 | Liabilities reconciled (deposit, voucher, prepaid, points, annual fee, caddy fee) | `TestP4AccountingBillingAR`, `TestP4FixLedgerBillingPostings`, `TestP4FixLedgerCaddySettlement` | ✅ |
| 7 | Tax integrated (e-Faktur for one tax period) | `TestP4AccountingBillingAR`, `TestP4FixFinanceDrillDownAndEFaktur`; unit `TestCoretaxPJAPSignedCalls` | ✅ on `mock-efaktur` / 🟡 Coretax credentials |
| 8 | Website managed by the club | `TestP4CMSPageWorkflow`, `TestP4CMSNewsGalleryBanners`, `TestP4CMSContentLifecycle`, `TestP4FixWebsiteNavigation`; Playwright `p4fix-website.spec.ts` | ✅ |
| 9 | No Release 1–3 regression | P0–P4 suites pass together on `staging` | ✅ |
| 10 | UAT approved; month-end close signed by the Finance Manager | §9 scenarios automated hop by hop below | 🟡 |

## EP-01 Item Master & Inventory Setup

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-INV-01 | Items: code, category, type, barcode, supplier item, inventory / COGS / expense accounts, status | Done | `internal/inventory/p4_master.go`; accounts read by posting (`internal/accounting/inventory_post.go`) | `TestP4InventoryMaster`, `TestP4FixLedgerInventoryValuation`; unit `TestCategoryCounterAccounts` |
| FR-INV-02 | Hierarchical categories with default accounts and valuation method | Done | `p4_master.go`; category accounts preferred over the posting rules | `TestP4InventoryMaster`, `TestP4FixLedgerInventoryValuation` |
| FR-INV-03 | UOM and conversion buy → stock → use per item | Done | `/inventory/uom-conversions`, pack barcodes | `TestP4InventoryMaster`, `TestP4InventoryConsumption`; unit `TestPacks` |
| FR-INV-04 | Warehouses and stock locations (store, outlet, kitchen, transit, quarantine) | Done | `p4_master.go`; demo `internal/app/p4_inventory.go` (§16 #9 list) | `TestP4InventoryMaster`, `TestP4FixStockSeeds` |
| FR-INV-05 | Retail product 1:1 ↔ item; F&B through recipe | Done | `inventory/p4_consumption.go` | `TestP4InventoryRevaluationSerial`, `TestP4InventoryConsumption` |
| FR-INV-06 | Batch, serial, expiry per item | Done | `p4_stock.go` | `TestP4InventoryRevaluationSerial`, `TestP4ProcurementReceipts`, `TestP4InventoryReplenishment` |
| FR-INV-07 | Pro shop consignment, monthly settlement with commission | Done | `inventory/p4_consignment.go` → accounting | `TestP4InventoryConsumption`, `TestP4AccountingAutomaticPosting` |
| FR-INV-08 | Import items, locations, opening stock (Master Data Import) | Done | `/platform/imports` (`inventory.item`, `inventory.warehouse`, `inventory.asset`), `/inventory/opening-stock:import` (`p4_import.go`) | `TestP4InventoryImport`, `TestP4InventoryAssets` |

## EP-02 Stock Movement & Balance

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-STK-01 | Append-only movements of every type | Done | `inventory/p4_stock.go` (append-only trigger) | `TestP4InventoryValuation` |
| FR-STK-02 | Real-time balance item × location (× batch) with value | Done | `/inventory/stock-balances` | `TestP4InventoryValuation` |
| FR-STK-03 | Issue to department / cost center with reason | Done | `/inventory/issues` | `TestP4InventoryValuation` |
| FR-STK-04 | Negative stock refused unless configured per location | Done | Inventory Policies | `TestP4InventoryValuation`, `TestP4InventoryImport` |
| FR-STK-05 | Movement dated in a closed period refused (K10) | Done | period guard wired in `app/p4_inventory.go` | `TestP4InventoryValuation`, `TestP4AccountingTransitionAndPeriodGuard` |
| FR-STK-06 | Valued movement → event for the journal | Done | `inventory.movement_posted` (PRD name `inventory.stock_moved`, [contracts](p3-p4-contracts.md)) → `accounting/events.go` | `TestP4InventoryValuation`, `TestP4AccountingAutomaticPosting` |
| FR-STK-07 | FEFO pick suggestion (Should) | Done | `/inventory/pick-suggestions` | `TestP4InventoryReplenishment` |
| EP-02 AC | Balance = Σ movements; no edit / delete | Done | — | `TestP4InventoryValuation` |

## EP-03 Store Requisition & Stock Transfer

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-REQ-01 | Store requisition with approval per Inventory Policies | Done | `inventory/p4_documents.go` | `TestP4InventoryRequisitionTransfer`, `TestP4FixStockApprovals` |
| FR-REQ-02 | Partial fulfilment, close the rest, unfulfillable → PR | Done | `p4_documents.go` → `inventory.reorder_needed` → procurement | `TestP4InventoryRequisitionTransfer`, `TestP4ProcurementRequisitions` |
| FR-REQ-03 | Transfer In Transit, received difference | Done | `/inventory/transfers` | `TestP4InventoryRequisitionTransfer` |
| FR-REQ-04 | Requisition from `ops` with barcode | Done | `web/apps/staff/src/p4/inventory.tsx` (`/ops/store`, scanner) | `TestP4InventoryRequisitionTransfer` (`origin: ops`) |

## EP-04 Stock Opname & Adjustment

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-OPN-01 | Opname per location / category, blind count, barcode in `ops`, In Progress → Counted → Posted | Done | `inventory/p4_opname.go`; `/ops/warehouse/opname` | `TestP4InventoryOpname` |
| FR-OPN-02 | Variance qty & value; recount or approval above tolerance | Done | `p4_opname.go` | `TestP4InventoryOpname` |
| FR-OPN-03 | Manual adjustment with reason and approval | Done | `p4_documents.go` | `TestP4InventoryOpname`, `TestP4FixStockApprovals` |
| FR-OPN-04 | Freeze or recorded cut-off | Done | `p4_opname.go` | `TestP4InventoryOpname` |
| FR-OPN-05 | Offline opname, idempotent sync (Should) | Done | offline queue + `Idempotency-Key` | `TestP4InventoryOpname`; k6 `opname.js` 🟡 |
| EP-04 AC | 120 vs 117 → −3 at average cost + variance journal | Done | — | `TestP4InventoryOpname`, `TestP4AccountingAutomaticPosting` |

## EP-05 Stock Valuation, COGS & Inventory Journal

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-VAL-01 | Moving average and FIFO per category / item | Done | `p4_stock.go` | `TestP4InventoryValuation` |
| FR-VAL-02 | COGS at valuation cost | Done | `p4_stock.go` | `TestP4InventoryValuation` |
| FR-VAL-03 | Landed cost allocated to receipt lines (Should) | Done | `internal/procurement/landed_cost.go` (by value or quantity, on-hand and used part) → `procurement.invoice_price_variance` → inventory revaluation | `TestP4FixStockLandedCost` |
| FR-VAL-04 | Inventory journal per valued movement through posting rules | Done | `accounting/events.go`, `inventory_post.go`, rules DEF-INV-* | `TestP4AccountingAutomaticPosting`, `TestP4FixLedgerInventoryValuation` |
| FR-VAL-05 | Stock Valuation Report as of a date = GL inventory | Done | `accounting/revenue.go` control reconciliation per inventory account; period-close check (`accounting/period.go`) | `TestP4FixLedgerInventoryValuation` |
| FR-VAL-06 | Revaluation when the invoice price differs after use | Done | `inventory/p4_revaluation.go`, accounting `onRevaluation` | `TestP4InventoryRevaluationSerial`, `TestP4ProcurementMatching`, `TestP4AccountingAutomaticPosting` |
| EP-05 AC1 | 10 @ 100k + 10 @ 120k → 110k; issue 5 kg = COGS 550k | Done | — | `TestP4InventoryValuation` |
| EP-05 AC2 | Month-end valuation = GL inventory per property | Done | — | `TestP4FixLedgerInventoryValuation` (a deliberate difference fails the reconciliation and blocks the close) |

## EP-06 Automatic Consumption

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-CNS-01 | POS `commercial.sale_completed` (K7) per recipe incl. modifiers, combos, sub-recipes; retail 1:1 | Done | `inventory/p4_consumption.go` | `TestP4InventoryConsumption`, `TestP4FixStockConsumptionModifiersCombos` |
| FR-CNS-02 | Void / refund reverses consumption per configuration | Done | `commercial.sale_voided` / `sale_refunded` (`internal/commercial/pos/pos.go`) → `p4_consumption.go` (retail always, F&B only when not prepared) | `TestP4FixStockRefundRestock` |
| FR-CNS-03 | Banquet `event_completed` (K6) per final pax | Done | `p4_consumption.go` | `TestP4InventoryConsumption` |
| FR-CNS-04 | Package (K6) and service BOM (golf round) | Done | `p4_consumption.go` | `TestP4InventoryConsumption` |
| FR-CNS-05 | Idempotent per event; late events to their business day | Done | `p4_consumption.go` | `TestP4InventoryConsumption`, `TestP4InventoryValuation` |
| FR-CNS-06 | Theoretical vs actual per outlet per period | Done | `/inventory/consumption-variance` | `TestP4InventoryConsumption` |
| FR-CNS-07 | Sold out at POS through K9 (Should) | Done | `reporting.stock_availability` → POS menu (`commercial/pos/pos.go`, POS Policies `markSoldOut`); POS grid `web/apps/staff/src/ops/p2.tsx` | `TestP4FixStockSoldOut` |
| EP-06 AC1 | 100 rounds × 1 bottle = 4 cartons + 4 bottles from Golf Ops | Done | — | `TestP4InventoryConsumption` |
| EP-06 AC2 | Same POS event twice deducts once | Done | — | `TestP4InventoryConsumption` |

## EP-07 Production, Waste, Batch, Serial & Expiry

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-PRD-01 | Production order with actual yield and costed output | Done | `p4_documents.go` | `TestP4InventoryConsumption` |
| FR-PRD-02 | Waste with reason per location | Done | `/inventory/waste` | `TestP4InventoryConsumption` |
| FR-PRD-03 | Expiry per batch, H-N alerts, report | Done | `inventory/p4_replenish.go` | `TestP4InventoryReplenishment` |
| FR-PRD-04 | Serial numbers from receipt to sale | Done | `p4_stock.go` | `TestP4InventoryRevaluationSerial`, `TestP4ProcurementReceipts` |
| FR-PRD-05 | Banquet production schedule from the BEO (Should) | Done | `p4_consumption.go` (`banquet.beo_issued`) | `TestP4InventoryConsumption` |

## EP-08 Replenishment & Automatic PR

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-RPL-01 | Par, minimum, reorder point per item × location | Done | `/inventory/par-stocks`, `/reorder-points` | `TestP4InventoryMaster` |
| FR-RPL-02 | Daily automatic PR = par − stock − on order | Done | `p4_replenish.go` → procurement | `TestP4InventoryReplenishment`, `TestP4ProcurementProcureToPay` |
| FR-RPL-03 | Outlet requisition suggestion below par | Done | `p4_replenish.go` | `TestP4InventoryReplenishment` |
| FR-RPL-04 | Low stock and slow-moving reports | Done | `/inventory/slow-moving`, reports | `TestP4InventoryReplenishment` |
| EP-08 AC | Par 50 / RP 20 / stock 18 → one PR of 32 per day | Done | — | `TestP4InventoryReplenishment` |

## EP-09 Asset & Equipment

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-AST-01 | Asset register (buggy linked to the P2 cart, gym, rental, banquet) | Done | `inventory/p4_assets.go` | `TestP4InventoryAssets` |
| FR-AST-02 | Maintenance schedule by time or usage hours from P2, reminders | Done | `p4_assets.go` (returned golf cart assignments add usage hours) | `TestP4InventoryAssets`, `TestP4FixStockEventsAndCartUsage` |
| FR-AST-03 | Usage and maintenance history; spare parts reduce Engineering stock | Done | `p4_assets.go` | `TestP4InventoryAssets` |
| FR-AST-04 | Rental out / back (Should) | Done | `p4_assets.go` | `TestP4InventoryAssets` |
| FR-AST-05 | Depreciation straight line / declining, monthly journal | Done | `p4_assets.go`, accounting `onDepreciation` (category accounts) | `TestP4InventoryAssets`, `TestP4AccountingAutomaticPosting`, `TestP4FixLedgerAssetDisposal` |
| FR-AST-06 | Disposal / write-off through approval | Done | `p4_assets.go` → `inventory.asset_disposed` → `accounting/inventory_post.go` (cost, accumulated depreciation, proceeds, gain / loss) | `TestP4InventoryAssets`, `TestP4FixLedgerAssetDisposal` |

## EP-10 Supplier Management & Vendor Performance

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-SUP-01 | Suppliers: NPWP, bank (masked), terms, categories, PKP | Done | `internal/procurement/supplier.go` | `TestP4ProcurementRequisitions` |
| FR-SUP-02 | Supplier items: code, last price, lead time, MOQ | Done | `supplier.go` | `TestP4ProcurementRequisitions`, `TestP4InventoryMaster` |
| FR-SUP-03 | Vendor performance | Done | `procurement/performance.go` | `TestP4ProcurementProcureToPay`, `TestP4ProcurementReports` |
| FR-SUP-04 | Blacklist with reason and approval | Done | `supplier.go` | `TestP4ProcurementRequisitions` |

## EP-11 Purchase Requisition & Approval

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-PR-01 | Sources: manual, reorder, banquet (K1), store requisition | Done | `procurement/requisition.go` | `TestP4ProcurementRequisitions`, `TestP4ProcurementProcureToPay` |
| FR-PR-02 | Content and statuses incl. Partially Ordered / Ordered | Done | `requisition.go` | `TestP4ProcurementRequisitions` |
| FR-PR-03 | Approval matrix by value, category, cost center | Done | `procurement/policy.go`, demo workflows `procurement/demo.go` | `TestP4ProcurementRequisitions` |
| FR-PR-04 | Banquet PR follows BEO revisions | Done | `procurement/service.go` (`banquet.beo_revised`) | `TestP4ProcurementRequisitions` |
| FR-PR-05 | Consolidate PRs into one RFQ / PO | Done | `rfq.go`, `order.go` | `TestP4ProcurementRequisitions` |
| EP-11 AC | BEO 300 → 320 pax updates the unordered PR | Done | — | `TestP4ProcurementRequisitions` |

## EP-12 RFQ & Vendor Quotation

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-RFQ-01 | RFQ to several suppliers (PDF / e-mail) with deadline | Done | `procurement/rfq.go`, `pdf.go` (ID / EN), supplier link (website `[lang]/supplier/rfq/[token]`) | `TestP4ProcurementProcureToPay` |
| FR-RFQ-02 | Record vendor quotation | Done | `rfq.go` | `TestP4ProcurementProcureToPay` |
| FR-RFQ-03 | Comparison; non-cheapest needs a reason | Done | `rfq.go` | `TestP4ProcurementProcureToPay` |
| FR-RFQ-04 | Skip RFQ under threshold / contract supplier | Done | `policy.go` | `TestP4ProcurementReports` |

## EP-13 Purchase Order

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-PO-01 | PO from PR / quotation or direct | Done | `procurement/order.go` | `TestP4ProcurementProcureToPay`, `TestP4ProcurementRequisitions` |
| FR-PO-02 | Approve per matrix; sent by PDF / e-mail | Done | `order.go`, `pdf.go` | `TestP4ProcurementProcureToPay`, `TestP4ProcurementReceipts` |
| FR-PO-03 | Versioned revisions; cancel unreceived lines | Done | `order.go` | `TestP4ProcurementReceipts`, `TestP4ProcurementMatching` |
| FR-PO-04 | Partially Received → Received → Closed; outstanding PO | Done | `order.go` | `TestP4ProcurementProcureToPay` |
| FR-PO-05 | Service PO | Done | `order.go` | `TestP4ProcurementMatching` |

## EP-14 Goods Receipt & Purchase Return

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-GR-01 | Receive against PO, batch / expiry / serial, delivery-note photo / document | Done | `procurement/receipt.go`, `receipt_attachments.go`; upload on the Back Office and `ops` receipt forms (`p4/procurement.tsx`) | `TestP4ProcurementProcureToPay`, `TestP4ProcurementReceipts`, `TestP4FixStockDeliveryNote` |
| FR-GR-02 | Over-receipt refused or approved per tolerance | Done | `receipt.go` | `TestP4ProcurementReceipts` |
| FR-GR-03 | Receipt without PO for allowed categories, with approval | Done | `receipt.go` | `TestP4ProcurementReceipts` |
| FR-GR-04 | Purchase return with debit note | Done | `receipt.go` | `TestP4ProcurementReceipts` |
| FR-GR-05 | GR journal (inventory / GRNI) | Done | accounting `onGoodsReceived` | `TestP4AccountingAutomaticPosting` |

## EP-15 Vendor Invoice & 3-Way Matching

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-VIN-01 | Vendor invoice with input tax invoice, lines to PO / GR | Done | `procurement/invoice.go` | `TestP4ProcurementProcureToPay` |
| FR-VIN-02 | 3-way matching with tolerances | Done | `invoice.go` | `TestP4ProcurementMatching` |
| FR-VIN-03 | Matched / Mismatch → On Hold; override | Done | `invoice.go` | `TestP4ProcurementMatching` |
| FR-VIN-04 | 2-way for services | Done | `invoice.go` | `TestP4ProcurementMatching` |
| FR-VIN-05 | Matched → AP, closes GRNI | Done | `procurement.vendor_invoice_approved` → accounting `onVendorInvoice` | `TestP4AccountingAutomaticPosting` |
| EP-15 AC | PO 100, GR 95, invoice 100 → On Hold; 95 → AP Rp950.000 + PPN | Done | — | `TestP4ProcurementMatching` |

## EP-16 General Accounting

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-ACC-01 | Hierarchical CoA with property, business line, cost center | Done | `internal/accounting/coa.go` | `TestP4AccountingLedgerCore` |
| FR-ACC-02 | Manual and automatic journals; approval above threshold | Done | `accounting/journal.go` | `TestP4AccountingLedgerCore`, `TestP4AccountingGolfPerTransaction` |
| FR-ACC-03 | Append-only, reversal, Posted / Reversed | Done | `db/migrations/accounting/` triggers, `ledger.go` | `TestP4AccountingLedgerCore` |
| FR-ACC-04 | Balanced per property, enforced by the database | Done | DB constraint trigger | `TestP4AccountingLedgerCore` |
| FR-ACC-05 | GL and TB per period / property with drill-down to journals and source documents | Done | `accounting/reports.go`, `p4fix_drilldown.go`; Staff App `p4/accounting.tsx` | `TestP4AccountingLedgerCore`, `TestP4FixFinanceDrillDownAndEFaktur`; Playwright *Accounting: the accountant opens the General Ledger, its trial balance and chart of accounts* |
| FR-ACC-06 | Open / Soft Closed / Closed with checklist | Done | `accounting/period.go` (incl. Stock Valuation = GL check) | `TestP4AccountingLedgerCore`, `TestP4FixLedgerInventoryValuation` |
| FR-ACC-07 | Year-end closing to retained earnings | Done | `period.go` | `TestP4AccountingLedgerCore` |
| FR-ACC-08 | Recurring and auto-reversing journals (Should) | Done | `journal.go` | `TestP4AccountingLedgerCore` |
| FR-ACC-09 | Audit trail; auditor read access | Done | creator / approver / source on journals; role **Auditor** (`internal/platform/catalog/p4fix_auditor.go`), time-bound assignment (`internal/platform/iam/p4fix_expiry.go`), logged reads (`internal/platform/httpapi/p4fix_readaudit.go`) | `TestP4FixFinanceAuditor`, `TestRolePermissionMatrix` |
| EP-16 AC1 | Unbalanced refused by the DB; posted only reversible | Done | — | `TestP4AccountingLedgerCore` |
| EP-16 AC2 | Closed period refuses every source; late events to the next open period | Done | — | `TestP4AccountingLedgerCore`, `TestP4AccountingTransitionAndPeriodGuard` |

## EP-17 Automatic Posting

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-PST-01 | Configurable posting rules (event + conditions → accounts, dimensions) | Done | `accounting/rules.go` | `TestP4AccountingLedgerCore`, `TestP4AccountingCashBank` |
| FR-PST-02 | Minimum sources: billing, voucher, loyalty, membership fee, caddy settlement, instructor, commission, POS shift, inventory, procurement, AP, bank | Done | `accounting/events.go`, `billing_post.go`, `cash_post.go`, `inventory_post.go` (commission, disposal); `golf.caddy_settlement_approved` published by `internal/golf/experience/caddy.go` | `TestP4AccountingAutomaticPosting`, `TestP4AccountingBillingAR`, `TestP4FixLedgerCommission`, `TestP4FixLedgerCaddySettlement`, `TestP4FixLedgerBillingPostings`; unit `TestPostingCoverage`, `TestDefaultRuleSources` |
| FR-PST-03 | Posting mode per source (per transaction / daily summary) | Done | Accounting Configuration | `TestP4AccountingGolfPerTransaction`, `TestP4AccountingBillingAR` |
| FR-PST-04 | Suspense and exception queue; no event lost | Done | `accounting/exceptions.go` | `TestP4AccountingAutomaticPosting` |
| FR-PST-05 | Idempotent, replay safe | Done | processed events, posted sources | `TestP4AccountingAutomaticPosting`, `TestP4AccountingGolfPerTransaction` |
| FR-PST-06 | Versioned rules with effective date | Done | `rules.go` | `TestP4AccountingLedgerCore` |
| FR-PST-07 | Posting reconciliation per business day vs Daily Revenue / export | Done | `accounting/revenue.go` | `TestP4AccountingBillingAR` |
| FR-PST-08 | Default rules generated from the Accounting Export components | Done | `POST /accounting/posting-rules:generate-defaults` (idempotent: adds missing defaults) | `TestP4AccountingLedgerCore`; unit `TestDefaultRuleSources` |
| EP-17 AC1 | Golf all-in Rp640.000 → cash Dr; PPN, green fee, buggy, HIO, caddy liability Cr | Done | — | `TestP4AccountingGolfPerTransaction` |
| EP-17 AC2 | `payment_settled` ×3 → one journal | Done | — | `TestP4AccountingGolfPerTransaction` |
| EP-17 AC3 | Journal per component = Daily Revenue Report | Done | — | `TestP4AccountingBillingAR` |

## EP-18 Accounts Receivable

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-AR-01 | AR ledger from P3 invoices, member and corporate billing | Done | `accounting/ar.go` | `TestP4AccountingBillingAR` |
| FR-AR-02 | Receivables per customer with balance, terms, control account | Done | `ar.go` | `TestP4AccountingBillingAR` |
| FR-AR-03 | Payments allocated; unapplied cash / advances | Done | `ar.go` | `TestP4AccountingBillingAR` |
| FR-AR-04 | AR aging = P3 operational aging | Done | `ar.go` | `TestP4AccountingBillingAR` |
| FR-AR-05 | Allowance and write-off through approval (Should) | Done | `ar.go` | `TestP4AccountingBillingAR` |
| FR-AR-06 | AR control = Σ sub-ledger | Done | `revenue.go` control reconciliation | `TestP4AccountingBillingAR` |
| EP-18 AC | AR control = open invoices = AR Aging Report | Done | — | `TestP4AccountingBillingAR` |

## EP-19 Accounts Payable

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-AP-01 | Payables from matched vendor invoices and vendor bills | Done | `accounting/ap.go` | `TestP4AccountingAutomaticPosting` |
| FR-AP-02 | Payment run with approval, partial payment | Done | `ap.go` | `TestP4AccountingAutomaticPosting` |
| FR-AP-03 | AP aging per supplier | Done | `ap.go` | `TestP4AccountingAutomaticPosting` |
| FR-AP-04 | Debit note / return reduces the payable | Done | `ap.go` | `TestP4AccountingAutomaticPosting`, `TestP4ProcurementMatching` |
| FR-AP-05 | PPh withholding (Should) | Done | `ap.go` (PPh 23) | `TestP4AccountingAutomaticPosting`, `TestP4ProcurementMatching` |
| FR-AP-06 | Bank bulk payment file (Should) | Done | `/payment-runs/{id}/bank-file` (generic layout; BCA / Mandiri layouts when the club provides them) | `TestP4AccountingAutomaticPosting` |

## EP-20 Cash & Bank

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-BNK-01 | Cash and bank accounts per property | Done | `accounting/bank.go` | `TestP4AccountingCashBank` |
| FR-BNK-02 | Statement import CSV / MT940 (bank API: see FR-INT-P4-03) | Done | `bank.go` | `TestP4AccountingCashBank` |
| FR-BNK-03 | Reconciliation auto-match; differences by journal | Done | `bank.go` | `TestP4AccountingCashBank` |
| FR-BNK-04 | Shift cash deposits with differences | Done | `bank.go`, `cash_post.go` | `TestP4AccountingCashBank` |
| FR-BNK-05 | Petty cash (Should) | Done | `bank.go` | `TestP4AccountingCashBank` |
| EP-20 AC | Gateway settlement and shift deposits auto-matched | Done | — | `TestP4AccountingCashBank` |

## EP-21 Revenue & Tax

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-REV-01 | Revenue allocation of bundles; total = price | Done | `accounting/revenue.go` | `TestP4AccountingBillingAR`, `TestP4AccountingAutomaticPosting` |
| FR-REV-02 | Deferred revenue: annual fee (monthly), voucher / prepaid, deposit / DP, points | Done | `billing_post.go`, `events.go` (`onAnnualFee`, `onLoyalty`) | `TestP4AccountingBillingAR`, `TestP4FixLedgerBillingPostings` |
| FR-REV-03 | Voucher and points breakage | Done | `events.go` | `TestP4AccountingBillingAR`, `TestP4AccountingAutomaticPosting` |
| FR-REV-04 | Tax configuration to accounts (PPN, PB1 / PBJT) | Done | `coa.go` tax codes, `tax.go` | `TestP4AccountingLedgerCore` |
| FR-REV-05 | Caddy fee liability, released at settlement; = sub-ledger | Done | folio component liability; `golf.caddy_settlement_approved` → `onCaddySettlement` | `TestP4AccountingGolfPerTransaction`, `TestP4FixLedgerCaddySettlement` |
| FR-REV-06 | Service charge pool and basis per department (payout in P5) | Done | `/service-charge-pools` | `TestP4AccountingBillingAR` |
| FR-REV-07 | e-Faktur output / input, upload, cancel / replace | Done (mock provider for trial) | `accounting/tax.go`; Coretax PJAP adapter `internal/platform/integration/p4_tax.go` (`mock-efaktur` in the trial) | `TestP4AccountingBillingAR`, `TestP4AccountingAutomaticPosting`; unit `TestCoretaxPJAPSignedCalls` |
| FR-REV-08 | PPN report per tax period | Done | `/ppn-report`, report `accounting.tax_ppn` | `TestP4AccountingBillingAR` |
| §7.1 / §7.3 | Invoices show the e-Faktur status / number (Back Office, Member App) | Done | `p4fix_drilldown.go`; `web/apps/staff/src/p3/billing.tsx`, `web/apps/member/src/p3.tsx` | `TestP4FixFinanceDrillDownAndEFaktur` |
| EP-21 AC1 | Voucher 5× Rp635.000: Rp127.000 per redemption, breakage Rp254.000 | Done | — | `TestP4AccountingBillingAR` |
| EP-21 AC2 | Wedding DP Rp26,4 jt deposit → revenue at completion | Done | — | `TestP4AccountingBillingAR` |
| EP-21 AC3 | Caddy fee liability GL = settlement sub-ledger daily | Done | — | `TestP4FixLedgerCaddySettlement` |

## EP-22 Financial Reports

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-FIN-01 | P&L, Balance Sheet, Cash Flow, TB, AR / AP aging, Revenue by Business Line | Done | `accounting/reports.go` | `TestP4AccountingLedgerCore` |
| FR-FIN-02 | Per property and period with comparison | Done | `reports.go` | `TestP4AccountingLedgerCore` |
| FR-FIN-03 | Consolidated MAIN + MDR without elimination | Done | `reports.go` | `TestP4AccountingLedgerCore` |
| FR-FIN-04 | Drill-down from report figures to journals and source documents | Done | `p4fix_drilldown.go`; statement rows → GL → journal → source screens (`p4/accounting.tsx`) | `TestP4FixFinanceDrillDownAndEFaktur` |
| FR-FIN-05 | Export XLSX / PDF; reports on the read replica | Done | `internal/reporting/reporting.go`, `p4fix_pdf.go` | `TestP4FixFinancePDFExport`; unit `TestReportPDF`; k6 `period-close.js` 🟡 |
| FR-FIN-06 | Format agreed with the club / auditor | Partial | statements follow the CoA hierarchy of the OneClub template | Layout not yet confirmed with the club's auditor; no configurable report-line mapping |
| EP-22 AC | BS balanced; P&L = change in current earnings | Done | — | `TestP4AccountingLedgerCore` |

## EP-23 Accounting Transition & Opening Balances

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-TRS-01 | Opening GL balances approved by the Finance Manager | Done | `accounting/opening.go` | `TestP4AccountingLedgerCore` |
| FR-TRS-02 | Open items: AR, AP, deposits, vouchers, opening stock, assets | Done | `opening.go`, P3 AR import, `/inventory/opening-stock:import`, asset import | `TestP4AccountingGolfPerTransaction`, `TestP4AccountingLedgerCore`, `TestP4InventoryImport`, `TestP4InventoryAssets`, `TestP4FixLedgerOpeningStockCarried` |
| FR-TRS-03 | 1-month reconciliation vs Excel Finance and Accounting Export | Done | posting reconciliation; Excel TB comparison `POST /accounting/reconciliations:excel-trial-balance` (`p4fix_drilldown.go`) | `TestP4AccountingBillingAR`, `TestP4FixFinanceDrillDownAndEFaktur` 🟡 month with the club |
| FR-TRS-04 | Cut-over stops the Accounting Export; old exports downloadable | Done | `opening.go` (`book:sign-off`), export guard | `TestP4AccountingTransitionAndPeriodGuard` |
| FR-TRS-05 | New CoA from the template; Excel Finance mapping | Done | `coa.go`, `/account-mappings` | `TestP4AccountingLedgerCore` |

## EP-24 Landing Page & CMS

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-CMS-01 | Pages with content and structured data blocks (K5) | Done | `internal/cms/blocks.go` | `TestP4CMSPageWorkflow`, `TestP4CMSSiteData`; unit `TestDataBlocksReferenceOwnerAPIs` |
| FR-CMS-02 | Banners with period and target pages | Done | `cms/content.go` | `TestP4CMSNewsGalleryBanners` |
| FR-CMS-03 | Images with alt text, automatic resize, web format | Done | `cms/media.go` (type sniffed, JPEG / PNG / GIF / WebP accepted, 320 / 960 / 1920 px variants of JPEG and PNG) | `TestP4CMSPageWorkflow`; unit `TestImageVariantsAndWebP` |
| FR-CMS-04 | News / article, gallery, contact information, course guide | Done | `cms/content.go`; website `[lang]/news`, `[lang]/gallery` | `TestP4CMSNewsGalleryBanners`, `TestP4CMSSiteData` |
| FR-CMS-05 | ID / EN with fallback and translation status | Done | `content.go` | `TestP4CMSPageWorkflow`, `TestP4CMSNewsGalleryBanners` |
| FR-CMS-06 | Draft → Review → Scheduled → Published → Unpublished; versions, rollback, preview | Done | `cms/workflow.go`, `preview.go` | `TestP4CMSPageWorkflow`, `TestP4CMSContentLifecycle`; unit `TestPreviewTokens` |
| FR-CMS-07 | Promotions follow the P3 periods automatically | Done | `/public/promotions` | `TestP4CMSPageWorkflow` |
| FR-CMS-08 | SEO: meta, slug, sitemap, Open Graph, structured data | Done | `cms/public.go`; website `web/apps/web/app/sitemap.ts`, `robots.ts`, `seo.ts` | `TestP4CMSSiteData`, `TestP4FixWebsiteNavigation`; Playwright *Website: sitemap.xml and robots.txt*; unit `TestSlugsPathsAndEmbeds` |
| FR-CMS-09 | Public navigation per NC §26, orderable | Done | CMS menus; website header from the CMS menu (`[lang]/nav.tsx`, `nav-model.ts`, keyboard dropdowns, phone layout) | `TestP4CMSSiteData`, `TestP4FixWebsiteNavigation`; Playwright *Website: the header shows the CMS menu with keyboard dropdowns*, *Website: on a phone the header folds into an expandable list* |
| FR-CMS-10 | Audit log; website cache invalidated on publish | Done | `cms.<kind>_published` → website revalidation (`integration/p4_website.go`) | `TestP4CMSPageWorkflow`; unit `TestWebsiteRevalidationSigned` |
| EP-24 AC1 | Rate change shows without a CMS edit | Done | data block → owner API | `TestP4CMSPageWorkflow` |
| EP-24 AC2 | Ended promotion disappears without action | Done | — | `TestP4CMSPageWorkflow`, `TestP4CMSNewsGalleryBanners` |

## EP-25 Integration Layer P4

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-INT-P4-01 | One gateway (Xendit); reconciliation per method / property | Done | `integration/p4_payment.go` (Xendit adapter; trial on `mock-payment`) | `TestP4CMSIntegrations`; unit `TestXenditSettlementDetailsPerMethod` |
| FR-INT-P4-02 | e-Faktur / Coretax adapter or official export | Done (mock provider for trial) | `integration/p4_tax.go` (Coretax PJAP + `mock-efaktur`), `/tax-invoices:export` | `TestP4AccountingBillingAR`; unit `TestCoretaxPJAPSignedCalls` |
| FR-INT-P4-03 | Bank statements: CSV / MT940 import (Must) | Done | `accounting/bank.go` | `TestP4AccountingCashBank` |
| FR-INT-P4-03 (API) | Bank statement API / host-to-host (Should) | Deferred | §16 #11: file upload at go-live; no bank API capability in the integration layer | — |
| FR-INT-P4-04 | Pending hardware through the bridge (Should) | Done | `integration/p4_hardware.go` (GPS buggy deferred, §16 #14) | `TestP4CMSIntegrations`; unit `TestRedactedURLAndHardwareProfiles` |
| FR-INT-P4-05 | Production e-mail (SPF / DKIM) | Done | `integration/p4_email.go` (SendGrid; trial on `mock-email`) | `TestP4CMSIntegrations`; unit `TestSendGridMailAndMaskedLog`, `TestCheckEmailDomain` |
| FR-INT-P4-06 | Export to another accounting system | Not built by decision | §16 #2: the club has no accounting system | — |
| FR-INT-P4-07 | Modernland residence verification (Should) | Done | `integration/p4_resident.go` (trial on `mock-resident`) | unit `TestModernlandResidentLookupLogsNoPersonalData` |
| FR-INT-P4-08 | Every integration call logged, masked | Done | integration call log | `TestP4CMSIntegrations`; unit `TestSendGridMailAndMaskedLog`, `TestRedactions` |

## EP-26 Operational Interfaces P4 (`ops`)

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-OPS-P4-01 | Warehouse: stock, goods receipt, issuing, transfer, opname, requisition with scanner / camera | Done | `web/apps/staff/src/p4/inventory.tsx`, `p4/procurement.tsx` (scan field) | `TestP4ProcurementReports`, `TestP4InventoryOpname`; Playwright *Warehouse: warehouse staff open Goods Receipt on the Operational workstation* |
| FR-OPS-P4-02 | Kitchen / Outlet: requisition, production, waste, outlet stock | Done | `p4/inventory.tsx` (`/ops/store*`) | `TestP4InventoryRequisitionTransfer`, `TestP4InventoryConsumption` |
| FR-OPS-P4-03 | Golf Staff spare part request (Should) | Done | `p4/inventory.tsx` | `TestP4InventoryAssets` |
| FR-OPS-P4-04 | Offline opname and goods receipt, idempotent (Should) | Done | offline queues (delivery-note files kept with the queued receipt) | `TestP4InventoryOpname`, `TestP4ProcurementReceipts` |

## EP-27 Configuration & Policies P4

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-POL-P4-01 | Inventory Configuration / Policies | Done | `inventory/p4_master.go` (incl. refund restock, valuation, tolerances) | `TestP4InventoryMaster`, `TestP4InventoryImport`, `TestP4FixStockRefundRestock` |
| FR-POL-P4-02 | Procurement Configuration / Policies | Done | `procurement/policy.go` | `TestP4ProcurementReports`, `TestP4ProcurementReceipts` |
| FR-POL-P4-03 | Accounting Configuration | Done | `accounting/config.go` | `TestP4AccountingGolfPerTransaction`, `TestP4FixLedgerInventoryValuation` |
| FR-POL-P4-04 | Tax configuration mapped to accounts | Done | `coa.go` tax codes | `TestP4AccountingLedgerCore` |

## EP-28 KPI, Dashboard & Reports

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-RPT-P4-01 | Inventory Performance | Done | `internal/reporting/p4_inventory.go` | `TestP4InventoryImport`, `TestP4InventoryValuation` |
| FR-RPT-P4-02 | Procurement Performance | Done | `reporting/p4_procurement.go` | `TestP4ProcurementReports` |
| FR-RPT-P4-03 | Financial Performance | Done | `reporting/p4_accounting.go` | `TestP4AccountingLedgerCore` |
| FR-RPT-P4-04 | The 29 listed reports | Done | `reporting/p4_{inventory,procurement,accounting,cms}.go` | `TestP4InventoryImport`, `TestP4ProcurementReports`, `TestP4AccountingLedgerCore` |
| FR-RPT-P4-05 | Filters, CSV / XLSX / PDF export, permission per report | Done | `reporting.go`, `p4fix_pdf.go` | `TestP4ProcurementReports`, `TestP4CMSSiteData`, `TestP4FixFinancePDFExport` |
| EP-28 AC | Inventory Value dashboard = Stock Valuation Report = GL inventory | Done | — | `TestP4InventoryValuation`, `TestP4FixLedgerInventoryValuation` |

## EP-29 Migration wave 4

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-MIG-P4-01 | New CoA and Excel Finance mapping | Done | `book:load-template`, `/account-mappings` | `TestP4AccountingLedgerCore` |
| FR-MIG-P4-02 | Opening GL per account per property | Done | `/accounting/opening-balances:import` | `TestP4AccountingLedgerCore` |
| FR-MIG-P4-03 | Items, locations, opening stock with value | Done | `/inventory/opening-stock:import` (Dr inventory / Cr opening balance equity, rule DEF-INV-OPENING; nothing when the GL opening carries the inventory) | `TestP4InventoryImport`, `TestP4FixLedgerInventoryValuation`, `TestP4FixLedgerOpeningStockCarried` |
| FR-MIG-P4-04 | Suppliers, supplier items, open PO, open AP | Done | `/procurement/migration:import` (`procurement/migration.go`), opening AP lines | `TestP4ProcurementMigration`, `TestP4AccountingLedgerCore` |
| FR-MIG-P4-05 | Assets with accumulated depreciation | Done | `/platform/imports` `inventory.asset` | `TestP4InventoryAssets` |
| FR-MIG-P4-06 | Reconciliation signed by the Finance Manager | Done | opening batch reconciliation, import previews | `TestP4AccountingLedgerCore`, `TestP4InventoryImport`, `TestP4ProcurementMigration`, `TestP4FixLedgerInventoryValuation` |
| FR-MIG-P4-07 | Two dry runs on Staging; accounting & warehouse cut-over runbook | Done | [`accounting-migration-p4.md`](runbooks/accounting-migration-p4.md), [`inventory-migration-p4.md`](runbooks/inventory-migration-p4.md), [`procurement-migration-p4.md`](runbooks/procurement-migration-p4.md) | 🟡 dry runs on Staging |
| §11 migration CLI | `oneclub import accounting\|inventory\|procurement` | Partial | Same imports as Back Office / API importers with preview and reconciliation (above); `cmd/oneclub` `import` supports only the Rhapsody pipeline | — |

## EP-30 Production Readiness (Release 4)

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-REL-P4-01 | k6: posting a month, POS stock peak, annual reports on the replica, CMS publish | Partial | `test/load/posting-month.js`, `period-close.js`, `procure-to-pay.js`, `opname.js`, `pos-peak.js` with Inventory; targets in [`production-readiness.md`](runbooks/production-readiness.md) §4 | No `cms-publish.js` (proposal in §4); 🟡 Staging runs |
| FR-REL-P4-02 | Playwright for §9 (procure-to-pay, POS → stock → COGS → journal, opname, period close, CMS publish) | Partial | `web/e2e/p4.spec.ts` (Accounting, Procurement, Inventory, Warehouse screens open), `p4fix-website.spec.ts` (CMS header, sitemap) | The flows run as Go API e2e; no browser run of procure-to-pay, opname, period close or CMS publish |
| FR-REL-P4-03 | P1–P3 regression on the Release 4 build | Done | CI `backend` + `browser` jobs | P0–P4 suites green together |
| FR-REL-P4-04 | Integration test of every posting rule and balance reconciliation | Done | `test/e2e/p4_accounting_test.go`, `p4_zz_fixledger_test.go` | `TestP4FixLedgerBillingPostings`, `TestP4FixLedgerCommission`, `TestP4FixLedgerAssetDisposal`, `TestP4FixLedgerCaddySettlement`, `TestP4FixLedgerInventoryValuation`; unit `TestPostingCoverage` |
| FR-REL-P4-05 | Pen test: CMS, bank / Coretax, financial data access | Done | scope in [`production-readiness.md`](runbooks/production-readiness.md) §6 (Release 4 table); sanitiser, upload checks, masking | `TestP4CMSPageWorkflow`; unit `TestSanitizeHTML`; 🟡 external pen test |
| FR-REL-P4-06 | Retention ≥ 5 years; restore drill includes `accounting` | Done | `deploy/pgbackrest/pgbackrest.conf` (`repo2-retention-full=60`), `deploy/scripts/restore-drill.sh`; [`backup-restore.md`](runbooks/backup-restore.md) | 🟡 bucket lifecycle / object lock ≥ 5 years (ops) and a recorded drill |
| FR-REL-P4-07 | VPS build with `garble`, no source maps | Done | `deploy/docker/Dockerfile`, `Dockerfile.web` | build config |
| FR-REL-P4-08 | Training per role and hypercare through one period close | Deferred | non-code release item; outline in [`production-readiness.md`](runbooks/production-readiness.md) §7 (Release 4 row) | — |

## §12 Non-functional requirements

| Area | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| NFR-Availability | 99,5 %; closing does not disturb operations | Done | period close on the replica, cashier traffic during the close in `period-close.js` | 🟡 Staging run |
| NFR-Performance | p95 targets; posting lag < 5 min; monthly reports < 30 s on the replica | Done | k6 thresholds in `posting-month.js`, `period-close.js`, `procure-to-pay.js`, `opname.js` | 🟡 Staging run |
| NFR-Financial integrity | Append-only, balanced; no event without journal or exception; control = sub-ledger (AR, AP, inventory, deferred, caddy fee, deposit) daily | Done | DB triggers, suspense, `revenue.go` control reconciliation | `TestP4AccountingLedgerCore`, `TestP4AccountingBillingAR`, `TestP4FixLedgerInventoryValuation`, `TestP4FixLedgerCaddySettlement`; unit `TestPostingCoverage` |
| NFR-Inventory integrity | Balance = Σ movements; no double deduction; valuation = GL | Done | `p4_stock.go`, idempotent consumers | `TestP4InventoryValuation`, `TestP4InventoryConsumption`, `TestP4FixLedgerInventoryValuation` |
| NFR-Period control | No posting into Closed periods from any source | Done | K10 guards in billing, inventory, procurement, accounting | `TestP4AccountingTransitionAndPeriodGuard`, `TestP4InventoryValuation`, `TestP4ProcurementReceipts` |
| NFR-Audit | ≥ 5 years retention; auditor read-only; source → journal → report trail | Done | retention config; Auditor role with expiry and read logging; drill-down | `TestP4FixFinanceAuditor`, `TestP4FixFinanceDrillDownAndEFaktur` |
| NFR-Security | ASVS L2; masked bank / NPWP; 2-person review of `accounting` and destructive migrations; CMS sanitising | Partial | masking, sanitiser, upload checks; two-person review is a proposal in [`production-readiness.md`](runbooks/production-readiness.md) §6 | No `.github/CODEOWNERS` / enforced 2-person review (pending product decision) |
| NFR-Offline | Opname and goods receipt with weak connection (Should) | Done | offline queues | `TestP4InventoryOpname`, `TestP4ProcurementReceipts` |
| NFR-SEO & web | Core Web Vitals "Good" on mobile; ID / EN | Partial | ID / EN, sitemap, robots, structured data | No Core Web Vitals measurement recorded |
| NFR-Regression | P1–P3 suites on every P4 merge | Done | CI | P0–P4 suites green |
| NFR-Language | English labels + Indonesian; PO / RFQ ID / EN | Done | `procurement/pdf.go` bilingual | `TestP4ProcurementProcureToPay` |

## §16 Decisions

| # | Decision | Status | Implementation | Evidence |
|---|---|---|---|---|
| §16 #1 | P3 ‖ P4 as wave 2 | n/a | process | — |
| §16 #2 | No legacy accounting system; CoA from the OneClub template | Done | `coa.go` template (FR-INT-P4-06 not built) | `TestP4AccountingLedgerCore` |
| §16 #3 | Go-live at month start; opening = previous month end; 1-month reconciliation | Done | `cutOverDate`, opening batch, Excel TB comparison | `TestP4AccountingLedgerCore`, `TestP4FixFinanceDrillDownAndEFaktur` |
| §16 #4 | Consignment without stock value, AP when sold, monthly settlement | Done | `p4_consignment.go` | `TestP4InventoryConsumption` |
| §16 #5 | Depreciation in OneClub, straight line default, monthly journal | Done | `p4_assets.go` | `TestP4InventoryAssets` |
| §16 #6 | Consolidated MAIN + MDR, no elimination | Done | `reports.go` | `TestP4AccountingLedgerCore` |
| §16 #7 | SC 10 %, PBJT 10 %, PPN for pro shop and membership, PPh 23 2 % / 4(2) 10 % | Done | tax codes `coa.go`; tax & service rules per outlet configurable | `TestP4AccountingLedgerCore`, `TestP4AccountingAutomaticPosting`, `TestP3CommercialGolfRateTaxCodes` — the demo golf rate card (P1) is all-in with PPN 11 % whereas the assumption names PBJT for golf; to verify with the tax consultant |
| §16 #8 | Moving average for all; opname tolerances per category; Finance Manager above | Done | demo seed `app/p4_inventory.go` | `TestP4FixStockSeeds` |
| §16 #9 | Warehouses and outlet sub-stores; monthly opname, weekly spot checks | Done | demo seed; spot-check opname (`itemIds`) | `TestP4FixStockSeeds`, `TestP4InventoryOpname` — the opname calendar is an operating procedure, not scheduled by the system |
| §16 #10 | PR / PO approval tiers; RFQ ≥ 3 vendors above Rp10 jt | Done | `procurement/policy.go`, `procurement/demo.go` | `TestP4ProcurementRequisitions`, `TestP4ProcurementReports` — demo thresholds awaiting confirmation (below) |
| §16 #11 | BCA / Mandiri statement upload; API Should | Done | CSV / MT940 import (API deferred, FR-INT-P4-03) | `TestP4AccountingCashBank` |
| §16 #12 | Wave order R4.1 CMS → R4.2 → R4.3 | n/a | module flags | — |
| §16 #13 | Xendit only | Done | `integration/p4_payment.go` | unit `TestXenditSettlementDetailsPerMethod` |
| §16 #14 | Generic hardware through the bridge; GPS buggy deferred | Done | `integration/p4_hardware.go` | `TestP4CMSIntegrations` |
| §16 #15 | Marketing Staff edits; Marketing Manager approves | Partial | seeded *Website Content Publication* workflow (`internal/cms/demo.go`) | Approver is the General Manager: no Marketing Manager role template |
| §16 #16 | Service charge pool 95 / 5; distribution in P5 | Done | `/service-charge-pools` | `TestP4AccountingBillingAR` |
| §16 #17 | New labels / statuses (Goods Receipt …) | Done | Staff App labels | `TestStaffAppAreas` |
| §16 #18 | Auditor read-only, time-bound, logged | Done | Auditor role, *Access until*, read audit | `TestP4FixFinanceAuditor` |

## §9 End-to-end flows

| Flow | Status | Evidence (hop by hop) |
|---|---|---|
| §9.1 Procure-to-pay | Done | reorder PR, approval, RFQ to 3 suppliers, selection, PO sent, partial and remaining GR, vendor invoice matched `TestP4ProcurementProcureToPay`, `TestP4ProcurementMatching` → AP, payment run, bank file `TestP4AccountingAutomaticPosting` → statement import and reconciliation `TestP4AccountingCashBank` → inventory / GRNI / AP / cash journals `TestP4AccountingAutomaticPosting`; k6 `procure-to-pay.js` 🟡 |
| §9.2 Banquet supply | Partial | BEO 300 pax → PR, revision 320 pax `TestP4ProcurementRequisitions` → event completed, BOM per final pax from the Banquet Kitchen, production from BEO `TestP4InventoryConsumption` → banquet COGS by rule DEF-INV-COGS-BQT (`internal/accounting/rules.go`, movements carry `sourceType: banquet`). Gap: food cost actual vs theoretical is reported per outlet / period (`inventory.food_cost`, `inventory.consumption_variance`), not per event; the banquet COGS rule is not asserted by an e2e test |
| §9.3 POS → stock → journal | Done | sale_completed incl. offline-synced sales, modifiers, combos `TestP4InventoryConsumption`, `TestP4FixStockConsumptionModifiersCombos` → business day close, revenue / tax / service / COGS journals `TestP4AccountingBillingAR`, `TestP4AccountingAutomaticPosting` → opname variance and adjustment `TestP4InventoryOpname` → theoretical vs actual `TestP4InventoryConsumption`; volume (200 trx, 40 offline) 🟡 k6 `pos-peak.js` |
| §9.4 Month-end close | Done | checklist, soft close, adjustment, close, statements `TestP4AccountingLedgerCore`; Stock Valuation = GL check blocks the close `TestP4FixLedgerInventoryValuation`; e-Faktur uploaded `TestP4AccountingBillingAR`; k6 `period-close.js` 🟡 |
| §9.5 Website update | Done | ID / EN promo page + banner scheduled, structured promotion block, published and unpublished on schedule `TestP4CMSPageWorkflow`, `TestP4CMSNewsGalleryBanners` |

## §11 Outbox events

| Event (PRD) | Status | Producer | Evidence |
|---|---|---|---|
| `inventory.stock_moved` | Done | renamed `inventory.movement_posted` (`internal/inventory/p4_stock.go`; [contracts](p3-p4-contracts.md)) | `TestP4InventoryValuation`, `TestP4AccountingAutomaticPosting` |
| `inventory.stock_low` | Done | `inventory/p4_replenish.go` | `TestP4FixStockEventsAndCartUsage` |
| `inventory.opname_posted` | Done | `inventory/p4_opname.go` | `TestP4FixStockEventsAndCartUsage` |
| `inventory.production_completed` | Done | `inventory/p4_documents.go` | `TestP4FixStockEventsAndCartUsage` |
| `inventory.asset_maintenance_due` | Done | `inventory/p4_assets.go` | `TestP4FixStockEventsAndCartUsage` |
| `procurement.requisition_approved` | Done | `internal/procurement/service.go` | `TestP4ProcurementProcureToPay` |
| `procurement.po_approved` | Done | `procurement/service.go` | `TestP4ProcurementProcureToPay` |
| `procurement.goods_received` | Done | `procurement/receipt.go` | `TestP4ProcurementProcureToPay`, `TestP4AccountingAutomaticPosting` |
| `procurement.vendor_invoice_matched` | Done | `procurement/service.go` | `TestP4ProcurementProcureToPay` |
| `accounting.journal_posted` | Done | `internal/accounting/ledger.go` (on every post) | published by every posting test, e.g. `TestP4AccountingLedgerCore` (payload not asserted; no consumer yet) |
| `accounting.posting_exception` | Done | `accounting/posting.go` | `TestP4AccountingAutomaticPosting` |
| `accounting.period_closed` | Done | `accounting/period.go` | `TestP4AccountingLedgerCore` |
| `accounting.payment_run_executed` | Done | `accounting/ap.go` | published by the executed payment run in `TestP4AccountingAutomaticPosting` (payload not asserted; no consumer yet) |
| `accounting.tax_invoice_uploaded` | Done | `accounting/tax.go` | `TestP4AccountingBillingAR` |
| `cms.page_published` | Done | `cms.<kind>_published` (`internal/cms/content.go`) | `TestP4CMSPageWorkflow` |

## Open items

| # | Item | Owner (suggested) |
|---|---|---|
| 1 | FR-REL-P4-02: Playwright runs of procure-to-pay, POS → stock → journal, opname, period close and CMS publish (today 4 P4 smoke tests + 4 website tests; the flows run as Go API e2e) | Engineering (QA) |
| 2 | FR-REL-P4-01: `test/load/cms-publish.js` (publish + cache purge under website traffic) | CMS area |
| 3 | FR-REL-P4-08: training per role (accountant, warehouse, procurement, inventory, marketing CMS) and hypercare through the first period close — outline in [`production-readiness.md`](runbooks/production-readiness.md) §7 | Delivery lead + club Finance |
| 4 | FR-FIN-06: confirm the financial statement layout with the club's auditor; add a report-line mapping if it differs from the CoA hierarchy | Club Finance + accounting area |
| 5 | §16 #15: Marketing Manager role template as approver of website publication (today the General Manager) | Platform / CMS area + club Marketing |
| 6 | §16 #7: golf tax treatment — the demo golf rate card is all-in with PPN 11 % while the assumption names PBJT 10 % for golf; confirm with the tax consultant and configure the rate card | Club Finance + tax consultant |
| 7 | NFR-SEO & web: measure Core Web Vitals of the public pages on mobile | Web area |
| 8 | §11 migration CLI (`oneclub import accounting\|inventory\|procurement`): wrap the API importers or amend the PRD and runbooks | Product + engineering |
| 9 | FR-INT-P4-03 bank statement API / host-to-host (Should, deferred by §16 #11); BCA / Mandiri bulk payment layouts for FR-AP-06 when the club provides them | Accounting area + club Finance |
| 10 | 🟡 Release 4 k6 runs on Staging with results recorded; external pen test (P4 scope); bucket lifecycle ≥ 5 years and a recorded restore drill with `accounting`; two dry runs per cut-over runbook; 1-month reconciliation; month-end close sign-off | OneClub ops + engineering + club Finance |
| 11 | Real providers before production: Coretax PJAP credentials (trial on `mock-efaktur`), Xendit (`mock-payment`), production e-mail (`mock-email`), Modernland resident data (`mock-resident`). For the trial itself `mock-efaktur` is not enabled by provisioning or `seed-demo`: the Platform Admin adds it under Integrations, otherwise e-Faktur upload answers *not_configured* | OneClub ops + club |
| 12 | §9.2: food cost actual vs theoretical per banquet event (today per outlet / period; consumption movements already carry the event as source) and an e2e assertion of the banquet COGS rule DEF-INV-COGS-BQT | Inventory + accounting areas |
| 13 | After deploying this release to an existing instance: run `POST /api/v1/accounting/posting-rules:generate-defaults` once (see [`accounting-migration-p4.md`](runbooks/accounting-migration-p4.md)) | OneClub ops |

## Product decisions pending

Awaiting the product owner; the code ships the values below as demo defaults or proposals.

1. Website menu grouping — now editable in the CMS; the demo seed is a proposal.
2. POS / quotation role discount tiers 5 / 10 / 20 / 100 %.
3. Stay Policies `roomChargePosting` default: `at_booking` (current default) vs `nightly`.
4. Procurement approval thresholds Rp5 jt / Rp25 jt (demo).
5. Public supplier RFQ quote page.
6. OTP code scrubbing from notification deliveries before production.
7. Self-hosting the Material Symbols icon font for offline devices.
8. CODEOWNERS two-person review.
9. Accounting Export resume after sign-off API.
