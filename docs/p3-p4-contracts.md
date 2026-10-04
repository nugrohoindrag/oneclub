# P3 ↔ P4 Module Contracts

PRD P3 §5.4.2 (K1–K6) and PRD P4 §5.4.2 (K1–K10) let the P3 and P4 modules be
built in parallel. This page fixes the event names and payloads (JSON, money
as decimal strings, dates `YYYY-MM-DD`, timestamps RFC 3339) so publishers and
subscribers agree without importing each other (Technical Doc §4.2 rule 3:
upward communication only through domain events). Contracts are additive:
fields may be added, never renamed or removed.

Every event is published through the outbox in the business transaction
(`Bus.Publish(ctx, tx, type, aggregateType, &aggregateID, &propertyID, payload)`),
so `propertyId` is also on the event envelope.

## Ownership and direction

| From → To | Mechanism |
|---|---|
| business line (golf, sportclub, stay, banquet, cms) → core (membership, reservation, commercial, billing) → customer (crm) → back office (inventory, procurement, accounting) | Direct call of the target module's root package (public API) |
| reverse direction | Domain event (outbox) or a hook registered by `internal/app` (e.g. `billing.RegisterTender`, `billing.RegisterNightAuditCheck`, `commercial.RegisterComponentAllocator`) |
| procurement → inventory | Direct call (goods receipt posts stock); inventory never imports procurement (no import cycle) |
| accounting → procurement / inventory / billing | Read through root packages or `reporting.*` views; accounting is never called by them |

## P3 events

### `crm.lead_converted`
`{ leadId, number, customerId, corporateAccountId?, opportunityId?, convertedAt }`

### `crm.quotation_accepted` — consumed by `banquet` (event creation) and `golf/tournament` (corporate tournament)
```json
{
  "quotationId": "uuid", "number": "QUO-2026-00012", "version": 2,
  "propertyId": "uuid", "opportunityId": "uuid|null", "leadId": "uuid|null",
  "customerId": "uuid", "corporateAccountId": "uuid|null",
  "line": "wedding | banquet | mice | event | tournament | stay | golf | package | membership | other",
  "eventType": "wedding | meeting | conference | gathering | birthday | tournament | other | null",
  "eventDate": "2026-12-12|null", "endDate": "2026-12-12|null", "pax": 300,
  "venueResourceId": "uuid|null", "packageRef": "text|null",
  "title": "Wedding Andi & Sari", "currency": "IDR",
  "subtotal": "0", "discount": "0", "service": "0", "tax": "0", "total": "0",
  "lines": [{ "itemType": "banquet_package | venue | product | service | package | other", "itemRef": "text|null",
               "description": "text", "quantity": "300", "unitPrice": "350000", "discount": "0", "total": "105000000" }],
  "paymentTerms": [{ "label": "DP 30%", "percent": "30", "amount": "31500000", "dueDate": "2026-10-20" }],
  "acceptedAt": "RFC3339", "acceptedVia": "staff | public_link"
}
```

### `crm.quotation_rejected`, `crm.quotation_expired`
`{ quotationId, number, version, customerId, opportunityId? }`

### `crm.loyalty_points_changed`
`{ accountId, customerId, kind: earned|redeemed|expired|adjusted|reversed, points, balance, sourceType, sourceId }`

### `commercial.promotion_applied` (K3 — discount by component)
`{ promotionId, code, sourceType: pos_order|folio|package_booking, sourceId, customerId?, businessLine, discount, currency }`

### `commercial.package_booked`
```json
{ "bookingId": "uuid", "number": "PKB-20261012-0001", "packageId": "uuid", "packageCode": "WED-GOLD", "version": 3,
  "customerId": "uuid", "folioId": "uuid", "startDate": "2026-12-12", "endDate": "2026-12-13", "pax": 2,
  "total": "0", "currency": "IDR",
  "components": [{ "bookingComponentId": "uuid", "componentId": "uuid", "componentType": "reservation | tee_time | voucher | fnb | service | banquet",
                   "resourceTypeCode": "bungalow|null", "allocationRef": "text|null", "serviceDate": "2026-12-12", "quantity": "1",
                   "allocatedNet": "0", "allocatedService": "0", "allocatedTax": "0", "allocatedTotal": "0", "revenueComponent": "package" }] }
```

### `commercial.package_consumed` (K6)
```json
{ "bookingId": "uuid", "bookingComponentId": "uuid", "packageId": "uuid", "componentType": "fnb", "consumedAt": "RFC3339",
  "businessDate": "2026-12-12", "quantity": "2", "outletId": "uuid|null", "recipeId": "uuid|null", "productId": "uuid|null",
  "consumption": [{ "itemId": "uuid", "quantity": "1.5", "uomId": "uuid" }],
  "revenue": { "revenueComponent": "package", "net": "0", "service": "0", "tax": "0", "total": "0", "currency": "IDR" } }
```
`consumption` holds base-UOM quantities from the recipe (BOM) of the component; empty when the component has no recipe.

### `banquet.event_confirmed`, `banquet.event_cancelled`
`{ eventId, number, eventType, title, customerId, startDate, endDate, pax, status }`

### `banquet.beo_issued`, `banquet.beo_revised` (K1 — procurement requirement)
```json
{ "beoId": "uuid", "beoNumber": "BEO-20261012-0001", "version": 2, "eventId": "uuid", "eventNumber": "EVT-20261012-0001",
  "eventDate": "2026-12-12", "pax": 300, "outletId": "uuid|null",
  "requirements": [{ "itemId": "uuid", "itemCode": "BEEF", "itemName": "Beef tenderloin", "quantity": "45.5", "uomId": "uuid", "uom": "kg",
                     "neededBy": "2026-12-11", "source": "menu | extra", "recipeId": "uuid|null" }] }
```
`banquet.beo_revised` carries the full requirement list of the new version (consumers replace, not add).

### `banquet.event_completed` (K6)
```json
{ "eventId": "uuid", "number": "EVT-20261012-0001", "completedAt": "RFC3339", "businessDate": "2026-12-12", "finalPax": 310,
  "outletId": "uuid|null",
  "consumption": [{ "itemId": "uuid", "quantity": "47.0", "uomId": "uuid", "recipeId": "uuid|null" }] }
```

### `golf.tournament_results_published`
`{ tournamentId, code, name, endDate, champions: [{ division, category: gross|net|stableford, customerId?, playerName, score }] }`

### Billing (owner: billing, P3) — K2, K4
| Event | Payload |
|---|---|
| `billing.invoice_issued`, `billing.invoice_paid` | `{ invoiceId, number, kind: folio\|account\|schedule, accountId?, customerId?, corporateAccountId?, billToName, billToNpwp?, issueDate, dueDate, currency, subtotal, serviceAmount, taxAmount, total, paidAmount, outstanding, status, lines: [{ businessLine, revenueComponent, net, service, tax, total, folioLineId? }] }` |
| `billing.invoice_overdue` | `{ invoiceId, number, accountId, dueDate, outstanding, daysOverdue }` |
| `billing.invoice_voided` | `{ invoiceId, number, accountId, total, reason }` |
| `billing.credit_note_issued` | `{ creditNoteId, number, invoiceId, invoiceNumber, accountId, amount, currency, reason }` |
| `billing.invoice_written_off` | `{ invoiceId, number, accountId, amount, currency, reason }` |
| `billing.payment_schedule_due` | `{ scheduleId, lineId, label, dueDate, amount, outstanding }` |
| `billing.business_day_closed` | `DailyRevenue`: `{ businessDate, status, frozen, revenue: [{ businessLine, revenueComponent, liability, net, service, tax, total }], charges, net, service, tax, payments: [{ methodType, purpose, count, amount }], paymentTotal, refunds, shiftTotal, liabilities: { name: amount }, invoicesIssued, invoiceTotal, generatedAt }` |

P1–P2 financial events reused by P4 (K8): `billing.payment_settled`, `billing.refund_processed`, `billing.folio_closed`,
`commercial.sale_completed` (K7), `commercial.voucher_sold`, `commercial.voucher_redeemed`, `commercial.voucher_expired`,
`commercial.shift_closed`, `golf.round_finished`, `sportclub.instructor_fee_approved`, `membership.annual_fee_due`, `membership.fee_paid`.

## P4 events

### `inventory.movement_posted` — consumed by accounting (inventory journal, COGS)
```json
{ "movementId": "uuid", "number": "SMV-20261012-0001", "movementType": "receipt | issue | transfer_out | transfer_in | adjustment | opname | waste | production_in | production_out | consumption | return_out",
  "businessDate": "2026-10-12", "warehouseId": "uuid", "costCenter": "kitchen|null", "sourceType": "goods_receipt | requisition | transfer | adjustment | opname | waste | production | sale | package | banquet | purchase_return | consignment",
  "sourceId": "uuid|null", "currency": "IDR", "totalCost": "0",
  "lines": [{ "itemId": "uuid", "quantity": "-1.5", "unitCost": "0", "totalCost": "0", "consignment": false }] }
```
Quantities are signed (in > 0, out < 0) in base UOM; cost uses the valuation method of the item (average or FIFO).

### `inventory.reorder_needed` — consumed by procurement (automatic Purchase Requisition)
`{ warehouseId, items: [{ itemId, onHand, reorderPoint, parLevel, suggestedQuantity, uomId, preferredSupplierId? }] }`

### `inventory.asset_depreciated`
`{ runId, period: "2026-10", lines: [{ assetId, assetCode, category, amount }], total, currency }`

### `inventory.consignment_sold`
`{ supplierId, itemId, quantity, unitCost, totalCost, currency, sourceType, sourceId }`

### `procurement.goods_received`
`{ goodsReceiptId, number, purchaseOrderId, poNumber, supplierId, warehouseId, receivedDate, currency, total,
   lines: [{ itemId, quantity, uomId, unitCost, totalCost, taxCode? }] }`

### `procurement.purchase_returned`
`{ purchaseReturnId, number, goodsReceiptId, supplierId, currency, total, lines: [{ itemId, quantity, unitCost, totalCost }] }`

### `procurement.vendor_invoice_approved` — consumed by accounting (AP)
`{ vendorInvoiceId, number, supplierInvoiceNo, supplierId, invoiceDate, dueDate, currency, subtotal, taxAmount, total,
   withholding: "0", taxInvoiceNo?, goodsReceiptIds: [], lines: [{ itemId?, description, quantity, unitPrice, total, accountHint: inventory|expense|asset }] }`

### `procurement.debit_note_issued`
`{ debitNoteId, number, supplierId, vendorInvoiceId?, amount, currency, reason }`

### `accounting.vendor_payment_made` — consumed by procurement (vendor invoice paid status)
`{ paymentId, number, supplierId, paidDate, currency, allocations: [{ vendorInvoiceId, amount }] }`

### `accounting.period_closed` (K10), `accounting.period_reopened`
`{ periodId, year, month, status: soft_closed|closed|open, closedAt }`

### `accounting.journal_posted`
`{ journalId, number, journalDate, sourceType, sourceId, total, currency }`

## Read models and public APIs

| Contract | Provider | Shape |
|---|---|---|
| K5 public data for CMS blocks | commercial, banquet, golf/tournament, golf, stay | `GET /api/v1/public/promotions`, `/public/packages`, `/public/packages/{code}`, `/public/events`, `/public/events/{id}`, `/public/tournaments`, `/public/tournaments/{id}`, `/public/tournaments/{id}/leaderboard`, `/public/rates/{line}`, `/public/hall-of-fame` |
| K9 stock availability | inventory | view `reporting.stock_availability (property_id, warehouse_id, item_id, product_id, on_hand, available, below_reorder)` |
| K10 period status | accounting | root function `accounting.PeriodStatus(ctx, q, property, date) (string, error)` and `accounting.period_closed` |
| Recipe explosion (K1/K6) | inventory | root function `inventory.ExplodeRecipe(ctx, q, recipeID, units) ([]inventory.Requirement, error)` |
