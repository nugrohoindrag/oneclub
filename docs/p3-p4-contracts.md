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
| procurement → inventory | Stock is posted by inventory from `procurement.goods_received` / `procurement.purchase_returned` (receipt / return at PO cost); procurement may read inventory through its root package; inventory never imports procurement (no import cycle) |
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

### `crm.quotation_sent` — consumed by `banquet` (tentative venue hold until the option date, FR-QUO-04 / EP-15)
`{ quotationId, number, version, customerId, corporateAccountId?, opportunityId?, line, title, eventType?, eventDate?, endDate?, pax?,
   venueResourceId?, optionDate?, packageRef?, total, currency, validUntil, channels: [email|whatsapp] }`
Sent again after a revision (new quotationId, same number): the hold follows the latest version.

### `crm.quotation_rejected`, `crm.quotation_expired`
`{ quotationId, number, version, customerId, opportunityId? }` — release holds of the quotation.

### `crm.opportunity_won` — consumed by `crm/loyalty` (member referral reward, FR-LEAD-04 / EP-09)
`{ opportunityId, number, quotationId, quotationNumber, customerId, corporateAccountId?, line, value, currency, ownerUserId?, leadId?,
   referrerCustomerId? }`

### `crm.loyalty_points_changed`
`{ accountId, customerId, kind: earned|redeemed|expired|adjusted|reversed, points, balance, sourceType, sourceId }`
Added (crm/loyalty): `entryId` (ledger entry), `value` (signed points × redemption value of the Loyalty Policies, decimal string), `currency`.
One event per ledger entry; redeemed points also reach Billing as the `loyalty_points` tender of the folio payment
(`billing.payment_settled` with `methodType: loyalty_points`, `tender_ref.entryId`).

### `crm.tier_changed`
`{ accountId, customerId, fromTierId?, toTierId?, tier, reason }` — loyalty tier upgrade / downgrade (FR-LOY-07).

### `crm.campaign_sent` — consumed by `commercial` (personal promo codes / vouchers of a campaign, FR-CMP-05)
```json
{ "campaignId": "uuid", "code": "WA-BDAY", "name": "Birthday month", "channel": "email | whatsapp | in_app",
  "promoMode": "none | shared | unique", "promoCode": "BDAY|null", "voucherTypeRef": "text|null",
  "batch": 2, "final": false,
  "recipients": [{ "recipientId": "uuid", "customerId": "uuid", "promoCode": "BDAY-7KQ2MX|null" }],
  "sent": 600, "skipped": 1400 }
```
Published once per dispatch batch (≤ batch size of the Campaign Policies) in the transaction that queues the messages; `sent` /
`skipped` only on the final batch (`final: true`). With `promoMode: unique` every recipient carries a personal code
`<promoCode>-XXXXXX`: Commercial registers it as a single-use code of the promotion `promoCode` for that customer (idempotent per
`recipientId`). With `voucherTypeRef` Commercial issues one voucher of that type per recipient. CRM marks a recipient converted on
`commercial.promotion_applied` with its code, or on a `billing.payment_settled` of the customer within the conversion window.

### `crm.ticket_created`, `crm.ticket_escalated`, `crm.ticket_resolved`
`crm.ticket_created`: `{ ticketId, number, customerId?, businessLine, priority, channel }`;
`crm.ticket_escalated`: `{ ticketId, number, level: 1|2, role, reason, priority, businessLine }`;
`crm.ticket_resolved`: `{ ticketId, number, customerId?, resolutionBreached, firstResponseBreached }`.

### `crm.ticket_compensation_approved` — for `commercial` (voucher) and `billing` (refund)
`{ compensationId, number, ticketId, ticketNumber, customerId?, type: points|voucher|refund|other, amount?, points?, description }`
Points are posted by CRM itself; a voucher / refund compensation is issued by its owner from this event (idempotent per `compensationId`).

### Consumed by CRM engagement & loyalty (decoded by name, wired in `internal/app/p3_engagement.go`)
| Event | Use |
|---|---|
| `billing.payment_settled` | Loyalty earning (idempotent per payment; not for `loyalty_points` / `folio_transfer`), campaign conversion |
| `billing.refund_processed` | Reverses earned points proportionally; gives back points of a refunded `loyalty_points` payment |
| `golf.round_finished` | Activity points (`round_finished` earning rules) for the players of the flight |
| `crm.opportunity_won` | Member referral reward to `referrerCustomerId` (`referral` earning rules), once per opportunity |
| `commercial.promotion_applied` | Campaign conversion of the recipient whose personal / shared code was used |
| `banquet.event_confirmed` | Interaction "Event confirmed" in the Customer 360 and segmentation tag `wedding` / `event_host` |
| `banquet.event_guest_checked_in` *(proposal)* `{ eventId, registrationId?, customerId?, number }` | Activity points (`event_attended`) when Event Operations checks a registered guest in |
| `golf.tournament_registration_confirmed` `{ tournamentId, customerId? }` | Segmentation tag `tournament_participant` |
| `golf.tournament_results_published` | Tags `tournament_participant`, `tournament_champion` of the champions |
| `crm.quotation_accepted` | Segmentation tag `deal_<line>` (e.g. `deal_wedding`) |

### `commercial.promotion_applied` (K3 — discount by component)
`{ promotionId, code, sourceType: pos_order|folio|package_booking, sourceId, customerId?, businessLine, discount, currency }`
Additive fields: `promoCode?` (the promo code entered, upper case; `code` is the promotion code), `promotionVersion`, `redemptionId`,
`channel` (pos | member_app | website | back_office | ops), `lines: [{ key, discount }]`. Published once per promotion when the sale
completes: POS order paid / charged (`pos_order`), booking folio closed (`folio`, priced bookings), package booking confirmed
(`package_booking`). Voids, refunds and cancellations reverse the redemption (no event; the Promotion Performance Report shows them).

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
Additive fields: `consumptionId`, `bookingNumber`, `packageCode`, `businessLine`, `reference?`. Partial uses of a component publish one
event each; the last use takes the rounding of the allocated revenue.

`commercial.package_booked` additive fields: `corporateAccountId?`, `nights`, `channel`, `discount`, `net`, `service`, `tax`, and per component
`name`, `businessLine`, `liability`, `allocationId?`, `resourceId?`, `scheduledStart?`, `scheduledEnd?`. Component types: `reservation`
(Reservation Engine resource, booked by commercial as a confirmed reservation with `sourceType = package_booking`), `tee_time` (golf seats
of the tee times confirmed in the Reservation Engine for the booking component — `allocationDetails.teeTimeIds`; golf books the players
from this event), `voucher` (vouchers issued to the customer), `fnb`, `service`, `banquet`, `other`. Published when the booking is
Confirmed (back office / quotation at once; website and member app when paid).

### `commercial.package_cancelled`
`{ bookingId, number, packageId, packageCode, status: cancelled|expired, reason, fee }` — unused components are released by commercial
(reservations cancelled, tee time seats freed, unused vouchers voided); business lines drop their details of the booking.

### Consumed by `commercial` (packages)
| Event | Effect |
|---|---|
| `crm.quotation_accepted` with `line = package` | Package booking of `packageRef` (or the line `itemType = package` / `itemRef`) on `eventDate` for `pax`, priced at the quotation `total`, with the quotation `paymentTerms` as its payment schedule; once per quotation (FR-QUO-06). `internal/app` marks the line as converted, so no generic quotation schedule is issued |
| `reservation.checked_in` `{ reservationId }` | The package component allocated to that reservation is consumed |
| `commercial.voucher_redeemed` `{ voucherId }` | One unit of the voucher component that issued the voucher is consumed |
| `billing.payment_settled` (folio source `package_booking`) | A pending website / member app booking is confirmed |
| `billing.folio_closed` | The promotions of the booking prices on the folio are redeemed (`commercial.promotion_applied`, sourceType `folio`) |

### `crm.campaign_sent` — consumed by `commercial` (personal promo codes, FR-CMP-05 / FR-APP-P3-02)
`{ campaignId, code, channel, sent, skipped, promoMode?: none|shared|unique, promoCode?, promotionId?, promoExpiresAt?,
   recipients?: [{ customerId, promoCode }] }`
With `promoMode = unique` (or no mode) and recipients, commercial registers each recipient's code as a personal promo code (one use, that
customer only, `campaign_id` kept) of `promotionId`, else of the promotion owning the shared code / promotion code `promoCode`; a code that
is not a valid promo code (3–40 of A–Z, 0–9, - or _) is replaced by a generated one and sent to the customer (`commercial.promo_code_issued`).
Idempotent per campaign and customer. CRM matches conversions on `commercial.promotion_applied.promoCode`.

### `banquet.event_confirmed`, `banquet.event_cancelled`
`{ eventId, number, eventType, title, customerId, startDate, endDate, pax, status }`
(`banquet.event_confirmed` is the "Definite" event of PRD P3 §11, published when the DP is received or the override is approved.)

Banquet consumes (decoded by name): `crm.quotation_sent` (tentative hold until the end of `optionDate` on the venue whose
`venueResourceId` is a `banquet_venue` resource; a revision moves it), `crm.quotation_rejected` / `crm.quotation_expired`
(the event carrying the hold is cancelled), `crm.quotation_accepted` (lines `wedding | banquet | mice | event`: event with the
quoted lines as charges and `paymentTerms` as the payment schedule, once per quotation number; `packageRef` = banquet package
code), `billing.payment_settled` (folio `sourceType = banquet_event`: Definite on the DP, paid registrations),
`billing.invoice_paid` (final invoice settled), `billing.payment_schedule_due` (sales owner reminder) and
`commercial.package_booked` (each `componentType = banquet` component becomes one Definite event, idempotent per
`bookingComponentId`; `allocationRef` may carry the venue's resource id; the package booking keeps the money). Payment schedules
and folios of events use `sourceType = banquet_event` with `sourceId` = event id (registration folios: participant id).

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
Additive fields (inventory): `warehouseCode`, `counterWarehouseId` (the other side of a transfer), `outletId`, `reason`,
`reversalOf` (a reversal repeats the original type with the opposite sign), and per line `itemCode`, `batchId`. A stock opname
variance posts `movementType: opname`; waste `waste`; a shipped − received transfer difference an `adjustment` of the transit
warehouse with `reason: transfer_discrepancy`. Consignment lines (`consignment: true`) carry no cost.

### `inventory.reorder_needed` — consumed by procurement (automatic Purchase Requisition)
`{ warehouseId, items: [{ itemId, onHand, reorderPoint, parLevel, suggestedQuantity, uomId, preferredSupplierId? }] }`
Additive (inventory): `source: reorder | requisition`, `businessDate` (reorder job: one event per warehouse and day, never
duplicated), `requisitionId` / `requisitionNumber` (a store requisition the store cannot supply), per item `itemCode`, `onOrder`.
Goods receipts of the warehouse close the open requests ("on order").

### `inventory.asset_depreciated`
`{ runId, period: "2026-10", lines: [{ assetId, assetCode, category, amount }], total, currency }`
Additive (inventory): `number`, `periodEnd`.

### `inventory.asset_disposed` (additive, inventory) — for accounting (disposal journal)
`{ assetId, assetCode, name, assetClass, category, disposedOn, acquisitionCost, accumulatedDepreciation, bookValue, proceeds, currency, reason }`
Published when the disposal is approved (approval document type `asset_disposal`).

### `inventory.consignment_sold`
`{ supplierId, itemId, quantity, unitCost, totalCost, currency, sourceType, sourceId }`
Additive (inventory): `movementId`, `salesAmount`, `commissionAmount` (club commission of the item), `businessDate`;
`totalCost` is the amount payable to the supplier (sales − commission). Monthly settlements:
`POST /api/v1/inventory/consignment-settlements`.

### Consumed by inventory (EP-06, FR-PRD-05)
| Event | Use |
|---|---|
| `commercial.sale_completed` (K7) | consumption of the outlet warehouse (`inventory.warehouses.outlet_id`) per recipe / retail item 1:1 / combo + modifiers; idempotent per order |
| `commercial.package_consumed`, `banquet.event_completed` (K6) | consumption of `consumption[]` (base UOM) at the outlet warehouse, else the configured package / banquet warehouse |
| `golf.round_finished` | service BOM `golf.round[.<holes>]` × checked-in players (`reporting.golf_rounds`; an optional `players` count is accepted when the flight has none) at the Golf Ops warehouse |
| `banquet.beo_issued`, `banquet.beo_revised` (K1) | draft production orders for the semi-finished items of the menu recipes × pax (revision replaces the drafts) |
| `procurement.goods_received`, `procurement.purchase_returned` | receipt / return_out at the base unit cost |
| `procurement.invoice_price_variance` (proposed, additive) | FR-VAL-06 / FR-VAL-03: revaluation of a receipt whose invoiced (or landed) base cost differs |
Unknown items / warehouses become `inventory.posting_exceptions` (never block the outbox). Late events dated in a closed
accounting period post on the current business day.

### `procurement.goods_received` — consumed by inventory (stock receipt into `warehouseId`) and accounting (GRNI)
`{ goodsReceiptId, number, purchaseOrderId, poNumber, supplierId, warehouseId, receivedDate, currency, total,
   lines: [{ itemId, quantity, uomId, baseQuantity, unitCost, baseUnitCost, totalCost, taxCode?, batchNo?, expiryDate?, serialNos?: [] }] }`
(`quantity`/`unitCost` in the purchase UOM, `baseQuantity`/`baseUnitCost` in the item's base UOM; inventory posts the base figures)

### `procurement.invoice_price_variance` (proposed by inventory, additive) — published by 3-way matching when the invoiced base cost of received goods differs (also landed cost allocated after the receipt)
`{ vendorInvoiceId, number, goodsReceiptId, warehouseId, lines: [{ itemId, invoicedBaseUnitCost }] }`
Inventory revalues the received quantity still in stock (value-only `adjustment` with `sourceType: revaluation`, once per invoice ×
receipt) and publishes `inventory.revaluation_posted`
`{ vendorInvoiceId, number, goodsReceiptId, warehouseId, movementId?, consumedTo: cogs|price_variance, currency,
   lines: [{ itemId, receivedQuantity, inStockQuantity, consumedQuantity, receivedUnitCost, invoicedUnitCost, stockAmount, consumedAmount }] }`
(the consumed part is for accounting: COGS or price variance per Inventory Configuration).

### `procurement.purchase_returned` — consumed by inventory (stock out) and accounting
`{ purchaseReturnId, number, goodsReceiptId, supplierId, warehouseId, currency, total, lines: [{ itemId, baseQuantity, baseUnitCost, totalCost, batchNo? }] }`

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

### `cms.page_published`, `cms.article_published`, `cms.banner_published`, `cms.gallery_published` (and `cms.<kind>_unpublished`), `cms.site_changed`
Published by `cms` (EP-24) when content goes live / comes down (also by the scheduler) and when website master data changes
(navigation, contact, redirects, images, cache purge). Consumed by `internal/app` → website integration (on-demand revalidation, FR-CMS-10).
`{ propertyId, revision, paths: ["/id/golf", "/en/golf"], contentId?, kind?, key?, title?, versionNo?, action: published|unpublished|changed }`
(`paths` empty = the whole site; `revision` is also served by `GET /api/v1/public/cms/site` as the website cache key).

## Read models and public APIs

| Contract | Provider | Shape |
|---|---|---|
| K5 public data for CMS blocks | commercial, banquet, golf/tournament, golf, stay | `GET /api/v1/public/promotions`, `/public/packages`, `/public/packages/{code}`, `/public/events`, `/public/events/{id}`, `/public/tournaments`, `/public/tournaments/{id}`, `/public/tournaments/{id}/leaderboard`, `/public/rates/{line}`, `/public/hall-of-fame` |
| K5 consumer | cms | CMS data blocks store only `{source, filter, limit}`; `GET /api/v1/public/cms/pages/{slug}` returns per block `data: {source, owner, endpoint, query, url}` and the website reads the owner's endpoint above (a 404 / missing endpoint renders an empty block). Golf rates: `/api/v1/public/golf/rates?property=<code>`; other lines `/api/v1/public/rates/{line}?propertyId=` |
| e-Faktur transport (FR-INT-P4-02) | platform/integration | `(*integration.Service).TaxInvoices(ctx) (integration.TaxInvoiceService, code, error)`: `SubmitInvoice`, `InvoiceStatus`, `CancelInvoice`, `UploadBatch(TaxInvoiceBatch{BatchRef, Period, Format: coretax_xml\|efaktur_csv, Filename, Content, InvoiceCount})`, `BatchStatus`; `ErrNotConfigured` → manual upload of the export file |
| K9 stock availability | inventory | view `reporting.stock_availability (property_id, warehouse_id, item_id, product_id, on_hand, available, below_reorder)`; additive column `outlet_id` |
| K10 period status | accounting | root function `accounting.PeriodStatus(ctx, q, property, date) (string, error)` and `accounting.period_closed` |
| K10 in inventory | internal/app | `inventory.Stock.SetPeriodGuard(accounting.PeriodStatus)` (nil = every period open): documents dated in a closed period are refused, automatic postings move to today |
| Procurement → inventory reads | inventory | root functions `inventory.ProcurementItemByID`, `inventory.ProcurementConvert`, `inventory.ProcurementToBase` (internal/inventory/procurement_api.go) |
| Loyalty tender (FR-LOY-05) | crm/loyalty via `internal/app` | `billing.RegisterTender("loyalty_points", …)`: `TenderPaymentInput{MethodType: "loyalty_points", Tender: {accountId? \| customerId?, points?}}` on any folio (POS `tenders[]`, front desk, Member App); default account = the folio's customer; amount = whole points × redemption value |
| Loyalty liability (FR-LOY-08) | crm/loyalty | `billing.RegisterLiability("loyalty_points", …)` (day summary / night audit / export `liability_balance`) and export section `crm.loyalty` (`loyalty` rows: earned, redeemed, expired, adjusted, reversed valued at the redemption value) |
| Customer 360 sections (FR-C360-01) | each area | `a.CRM.Sections["banquet" \| "tournament" \| …] = fn` in the area's wiring; CRM engagement wires `loyalty`, `campaignResponse`, `complaints`, `sales` |
| Recipe explosion (K1/K6) | inventory | root function `inventory.ExplodeRecipe(ctx, q, recipeID, units) ([]inventory.Requirement, error)` |
