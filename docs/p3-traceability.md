# P3 traceability — requirement → implementation → evidence

PRD P3 *Commercial & Business Expansion* (Release 3). Verified against the code on `staging` at `28ead3a`
(2026-10-05): every status below was checked against the current code and tests, not copied from the earlier gap
audit (many of its gaps were fixed since).

Status: **Done** implemented and verified by an automated test · **Done (mock provider for trial)** implemented
end-to-end against a sandbox adapter of the integration layer; the real provider is a configuration step ·
**Partial** implemented with a remaining gap (named in the row) · **Deferred** postponed by a PRD decision or a
release activity outside the code · **Not built by decision** · *n/a* process decision without behaviour (not
counted). 🟡 marks verification that needs an environment the dev machine does not have (Staging, VPS, club sign-off,
external pen test).

Test locations: `test/e2e/p3_*_test.go` and `test/e2e/p3fix_*_test.go` (Go acceptance tests on real PostgreSQL, run
together with the P0, P1, P2 and P4 suites), `web/e2e/p3.spec.ts`, `web/e2e/p34-flows.spec.ts` and `web/e2e/p4fix-website.spec.ts` (Playwright),
`test/load/*.js` (k6), unit tests next to the code. Module contracts with P4 (events, payloads, renames):
[`docs/p3-p4-contracts.md`](p3-p4-contracts.md).

Every client is web (Website, Member App, Staff App areas on the `dashboard`, `cashier`, `caddy` and `kitchen`
domains); P3 adds the module `banquet`, the CRM sub-packages `crm/sales`, `crm/engagement`, `crm/loyalty`,
`crm/topspender`, `golf/tournament`, and P3 files in `commercial` (`promotion*.go`, `package*.go`) and `billing`
(`p3_*.go`). Wiring between modules lives in `internal/app/p3_*.go`.

## Summary

| Status | Count |
|---|---|
| Done | 289 |
| Done (mock provider for trial) | 2 |
| Partial | 2 |
| Deferred | 4 |
| Not built by decision | 0 |
| **Total requirement rows** | **297** |

Counted over the per-ID tables below (FR, acceptance criteria, EP-26, §12 NFR, §16 decisions with behaviour, §9
flows, §11 events); the exit criteria table is a roll-up and is not counted.

## Audit & route coverage

The e2e harness fails the run when a mutating route succeeds without an audit entry, or when any mutating route is
never exercised successfully. Run at `28ead3a`: **1071/1071 mutating routes exercised, 0 without audit** (P0 + P1 + P2 + P3 + P4).

## Exit criteria (PRD §13.1)

| # | Criterion | Evidence | Status |
|---|---|---|---|
| 1 | Every inquiry reaches the pipeline | `TestP3SalesLeads` (WhatsApp webhook, website inquiry, e-mail, manual), `TestP3SalesJobs` (SLA), `TestP3FixChannelsWebsiteConsent`; Lead Source and first-response reports in `TestP3SalesQuotationToCommission` | ✅ / 🟡 UAT |
| 2 | Wedding & corporate event end-to-end | `TestP3BanquetQuotation`, `TestP3BanquetWedding`, `TestP3BanquetCorporate`, `TestP3FixMoneyQuotation`, `TestP3FixChannelsSchedulePayOnline`, `TestP3FixFulfilmentPackages`; Playwright Kitchen Display BEO and Member App DP | ✅ / 🟡 chained UAT run |
| 3 | Tournament on OneClub, Rhapsody tournaments switched off | `TestP3TournamentClub` (72 players, shotgun, offline scores), `TestP3TournamentHistoryImport`; runbook [`tournament-migration-p3.md`](runbooks/tournament-migration-p3.md) | ✅ code / 🟡 parity sign-off |
| 4 | Promotions & loyalty accurate | `TestP3CommercialPromotions` (offline = online), `TestP3EngagementLoyalty` (liability = ledger), `TestP3FixChannelsPOSPoints` | ✅ |
| 5 | Cross-line packages | `TestP3CommercialPackages` (all-or-nothing, allocation = price), `TestP3FixFulfilmentPackages` | ✅ |
| 6 | Unified folio, corporate billing, night audit | `TestP3BillingCorporateInvoices`, `TestP3BillingCashierShiftsNightAudit`, `TestP3FixFulfilmentNightAudit`, `TestP3FixMoneyBilling` | ✅ / 🟡 2 weeks on Staging |
| 7 | P3 KPIs consistent | `TestP3BanquetWedding`, `TestP3EngagementTopSpender`, `TestP3BillingCorporateInvoices` (dashboard = report, AR aging = open invoices) | ✅ |
| 8 | No Release 1–2 regression | P0–P4 suites pass together on `staging` | ✅ |
| 9 | P4 contracts K1–K6 | `TestP4ProcurementRequisitions` (K1), `TestP4AccountingBillingAR` (K2, K4), `TestP4AccountingAutomaticPosting` (K3), `TestP4CMSPageWorkflow` (K5), `TestP4InventoryConsumption` (K6) | ✅ |
| 10 | UAT approved | §9 scenarios automated hop by hop below; club sign-off per wave R3.1–R3.4 | 🟡 |

## EP-01 Lead Management

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-LEAD-01 | Lead with source, channel, interest, event date, pax, budget | Done | `internal/crm/sales/lead.go` | `TestP3SalesLeads` |
| FR-LEAD-02 | Automatic capture from website & WhatsApp; manual with source | Done | `crm/sales/capture.go` (WhatsApp webhook, e-mail, website hook), `internal/crm/public.go` | `TestP3SalesLeads`, `TestP3FixChannelsWebsiteConsent` |
| FR-LEAD-03 | Deduplication against customers and leads, link to Customer 360 | Done | `lead.go` (`duplicate_lead`) | `TestP3SalesLeads` |
| FR-LEAD-04 | Member referral visible for the loyalty reward | Done | `lead.go` referrer, `crm/loyalty/activity.go` (`crm.opportunity_won`) | `TestP3EngagementLoyalty` |
| FR-LEAD-05 | Assignment manual / round-robin / fixed per line | Done | `lead.go`, Sales Policies `assignmentMode` | `TestP3SalesLeads` |
| FR-LEAD-06 | First-response SLA with reminder and escalation | Done | `crm/sales/jobs.go` | `TestP3SalesJobs` |
| FR-LEAD-07 | Qualify / disqualify; convert to opportunity and customer | Done | `lead.go`, `crm/sales/conversion.go` | `TestP3SalesLeads` |
| FR-LEAD-08 | Scheduled follow-ups with reminders, interaction history | Done | `crm/sales/activity.go`, `jobs.go` | `TestP3SalesLeads`, `TestP3SalesJobs` |
| FR-LEAD-09 | Consent from the lead source; no consent ⇒ no campaign | Done | lead `marketingConsent`, website forms (`web/apps/web/app/[lang]/events/forms.tsx`, `booking.tsx`, `contact/page.tsx`), campaign eligibility | `TestP3EngagementCampaigns`, `TestP3FixChannelsWebsiteConsent`; Playwright *Website: inquiry with an unticked marketing consent…* |
| FR-LEAD-10 | Transfer leads with history | Done | `lead.go` | `TestP3SalesLeads` |
| EP-01 AC1 | Website wedding inquiry → lead of the wedding sales ≤ 1 min | Done | inquiry hook → `CreateLead` + assignment | `TestP3SalesLeads` |
| EP-01 AC2 | Duplicate phone warns and links to Customer 360 | Done | — | `TestP3SalesLeads` |
| EP-01 AC3 | Missed SLA → reminder and escalation to the manager | Done | `jobs.go` | `TestP3SalesJobs` |

## EP-02 Sales Pipeline & Opportunity

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-PIPE-01 | Pipelines per line, configurable stages (§16.1 defaults) | Done | `crm/sales/opportunity.go` (default pipelines) | `TestP3SalesQuotationToCommission` |
| FR-PIPE-02 | Opportunity fields | Done | `opportunity.go` | `TestP3SalesQuotationToCommission` |
| FR-PIPE-03 | Kanban & list, stage history | Done | `opportunity.go`; Staff App `web/apps/staff/src/p3/sales.tsx` | `TestP3SalesQuotationToCommission` |
| FR-PIPE-04 | Won / Lost with reason; Won needs an accepted quotation | Done | `opportunity.go` | `TestP3SalesConversions` |
| FR-PIPE-05 | Activities & follow-ups on the opportunity | Done | `/crm/opportunities/{id}/activities` | `TestP3SalesQuotationToCommission` |
| FR-PIPE-06 | Venue availability from the opportunity | Done | `opportunity.go` venue availability | `TestP3SalesQuotationToCommission` |
| FR-PIPE-07 | Weighted forecast per month and line (Should) | Done | `opportunity.go` forecast | `TestP3SalesQuotationToCommission`, `TestP3FixMoneyCommission` |
| EP-02 AC | Weighted value = Σ value × stage probability | Done | board / forecast | `TestP3FixMoneyCommission` |

## EP-03 Quotation & Conversion

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-QUO-01 | Lines from packages, banquet packages & menus, rates, free items; pricing engine + snapshot | Done | `crm/sales/quotation.go`, `internal/app/p3_sales.go` (catalogue pricing: Commercial package, banquet package per pax with minimum pax, banquet menu) | `TestP3FixMoneyQuotation`, `TestP3SalesConversions` |
| FR-QUO-02 | Discount above the Sales Policies limit needs approval | Done | `quotation.go`, role limits `crm/sales/module.go` | `TestP3SalesQuotationToCommission`, `TestP3FixMoneyQuotation` |
| FR-QUO-03 | Versions (Revised), one active | Done | `quotation.go` | `TestP3SalesQuotationToCommission` |
| FR-QUO-04 | Validity and option date → Tentative hold | Done | `quotation.go`, `internal/banquet/quotation.go` | `TestP3SalesJobs`, `TestP3BanquetQuotation` |
| FR-QUO-05 | PDF and secure link; accept with name and terms; IP & time recorded | Done (mock provider for trial) | `quotation.go`, `crm/sales/p3_acceptance.go` (one-time code by WhatsApp / e-mail — `mock-whatsapp` / `mock-email` in the trial), website `[lang]/quotation/[token]` (`otp.tsx`, `accept.tsx`) | `TestP3SalesQuotationToCommission`, `TestP3QuotationOtpAndEMeterai`; unit `TestAcceptanceOtpHash` |
| FR-QUO-06 | Conversion to event / package / membership / tournament + DP schedule | Done | `banquet/quotation.go`, `commercial/package_booking.go`, `internal/membership/p3_quotation.go`, `golf/tournament/integration.go`, schedule in `app/p3_sales.go` | `TestP3BanquetQuotation`, `TestP3CommercialPackages`, `TestP3SalesConversions`, `TestP3TournamentCorporate` |
| FR-QUO-07 | Idempotent conversion | Done | per-quotation keys | `TestP3BanquetQuotation`, `TestP3TournamentCorporate` (redelivery) |
| FR-QUO-08 | Template per line with instance branding, T&C from Banquet Policies | Done | `crm/sales/quotation_pdf.go`, images in `internal/kernel/pdf/image.go` | `TestP3FixMoneyQuotation`; unit `TestImages` |
| FR-QUO-09 | Third-party (certified PSrE) e-signature (Should) | Deferred | §16 #18: acceptance by link + one-time code with audit trail instead | — |
| EP-03 AC1 | Accepted twice → one event, venue locked, DP once | Done | — | `TestP3BanquetQuotation`, `TestP3SalesQuotationToCommission` |
| EP-03 AC2 | 15 % above a 10 % limit not sendable before approval | Done | — | `TestP3SalesQuotationToCommission` |

## EP-04 Sales Target & Commission

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-COM-01 | Targets per sales, line, period | Done | `crm/sales/defs.go`, `commission.go` | `TestP3SalesQuotationToCommission` |
| FR-COM-02 | Achievement and commission when the deal is Won and paid | Done | `crm/sales/commission.go` | `TestP3SalesQuotationToCommission`, `TestP3SalesConversions` |
| FR-COM-03 | Schemes: percent per line, tiered, flat; effective date | Done | `commission.go` | `TestP3FixMoneyCommission` |
| FR-COM-04 | Statements, Finance approval, Approved → Paid, payroll export | Done | `commission.go` | `TestP3SalesQuotationToCommission` |
| FR-COM-05 | Clawback on cancellation / refund with audit | Done | `commission.go` (refund, invoice void) | `TestP3FixMoneyCommission` |
| EP-04 AC | Wedding Rp88 jt @ 1 % = Rp880.000; refund → −Rp880.000 next period | Done | — | `TestP3FixMoneyCommission` |

## EP-05 Advanced Customer 360 & Segmentation

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-C360-01 | Customer 360 sections Banquet/Event, Lead & Opportunity, Quotation, Campaign, Loyalty, Complaint, Tournament | Done | `internal/crm/foundation.go` sections; `internal/banquet/crm360.go`; wiring `app/p3_banquet.go`, `p3_engagement.go`, `p3_tournament.go` | `TestP3FixChannelsCustomer360Banquet`, `TestP3EngagementLoyalty`, `TestP3TournamentClub` |
| FR-C360-02 | Segmentation dimensions incl. outlet | Done | `crm/engagement/segment.go` | `TestP3EngagementCampaigns` |
| FR-C360-03 | Dynamic (scheduled) and static segments, count, export | Done | `segment.go` (refresh job, `members.csv`) | `TestP3EngagementCampaigns`, `TestP34LeftoversTestGaps` (CSV export: header, members, permission) |
| FR-C360-04 | Corporate 360 (nominees, events, billing, AR, golf) | Done | `crm/engagement/c360.go`, `banquet/crm360.go`, `golf/tournament/corporate360.go` | `TestP3FixChannelsCustomer360Banquet`, `TestP3EngagementCampaigns` |
| FR-C360-05 | Automatic interactions from campaign, quotation, ticket, event | Done | `campaign.go`, `ticket.go`, `quotation.go`, `app/p3_engagement.go` | `TestP3EngagementCampaigns` |
| FR-C360-06 | Masking and permissions of sensitive data | Done | `crm/foundation.go` | `TestP3EngagementCampaigns` |

## EP-06 Campaign & Reminder

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-CMP-01 | WhatsApp / e-mail campaigns to segments, schedule, variables, preview | Done | `crm/engagement/campaign.go` | `TestP3EngagementCampaigns` |
| FR-CMP-02 | Consent & opt-out per channel, suppression | Done | `crm/engagement/consent.go` | `TestP3EngagementCampaigns` |
| FR-CMP-03 | Frequency cap (Should) | Done | `campaign.go` (Campaign Policies) | `TestP3FixMoneyCampaignTracking` |
| FR-CMP-04 | Tracking sent / delivered / read / click / conversion | Done | `campaign.go` statistics, BSP status webhook | `TestP3EngagementCampaigns`, `TestP3FixMoneyCampaignTracking` |
| FR-CMP-05 | Unique promo code or P2 voucher as campaign content | Done | `commercial/p3_integration.go` (codes), `internal/commercial/voucher/p3_issue.go` (one voucher per recipient) | `TestP3CommercialPromotions`, `TestP3FixMoneyVouchers` |
| FR-CMP-06 | Renewal and birthday reminders | Done | `crm/engagement/reminder.go` | `TestP3EngagementCampaigns` |
| FR-CMP-07 | Throttling to BSP limits, queue with retry | Done | batches + River | `TestP3EngagementCampaigns`; k6 `campaign-10k.js` 🟡 |
| FR-CMP-08 | Approval above N recipients (Should) | Done | `campaign.go` | `TestP3EngagementCampaigns` |
| EP-06 AC | 2.000 recipients → only opt-ins; unsubscribed skipped next | Done | — | `TestP3EngagementCampaigns` |

## EP-07 Feedback, NPS & Complaint Ticket

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-TKT-01 | Tickets from staff, Member App, website, low feedback; attachments | Done | `crm/engagement/ticket.go`, `/public/complaints`, website `[lang]/complaint` (footer link) | `TestP3EngagementComplaints`; Playwright *Website: inquiry with an unticked marketing consent, corporate golf and the complaint link in the footer* |
| FR-TKT-02 | SLA per category / priority in business hours | Done | `ticket.go` | `TestP3EngagementComplaints`; unit `TestAddBusiness` |
| FR-TKT-03 | Tiered escalation | Done | `ticket.go` | `TestP3EngagementComplaints` |
| FR-TKT-04 | Open → In Progress → Resolved → Closed, reopen, customer comms | Done | `ticket.go` | `TestP3EngagementComplaints` |
| FR-TKT-05 | Compensation (voucher, points, refund) through approval (Should) | Done | `ticket.go`; voucher `commercial/voucher/p3_issue.go`; refund `internal/billing/p3_compensation.go` | `TestP3EngagementComplaints`, `TestP3FixMoneyVouchers` |
| FR-TKT-06 | NPS per line and period | Done | `crm/engagement/nps.go` | `TestP3EngagementComplaints` |
| EP-07 AC | High ticket past SLA escalates to the line manager | Done | — | `TestP3EngagementComplaints` |

## EP-08 Top Spender & Leaderboard

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-TOP-01 | Ranking month / quarter / year from folio billing | Done | `internal/crm/topspender/topspender.go` | `TestP3EngagementTopSpender` |
| FR-TOP-02 | Spend per line | Done | `topspender.go` | `TestP3EngagementTopSpender` |
| FR-TOP-03 | Filters member, corporate, outlet, segment | Done | `topspender.go` | `TestP3EngagementTopSpender` |
| FR-TOP-04 | Most Rounds, Most Active Member | Done | `topspender.go` | `TestP3EngagementTopSpender` |
| FR-TOP-05 | Add to segment, VIP invitation, tier note | Done | `topspender.go` | `TestP3EngagementTopSpender` |
| FR-TOP-06 | Internal only | Done | permissions | `TestP3EngagementTopSpender`, `TestP3EngagementMemberLoyalty` |
| FR-TOP-07 | Refund / void / credit note reduce spend; points / voucher rule | Done | Top Spender policy | `TestP3EngagementTopSpender` |
| EP-08 AC | #1 spend = net charges − refunds | Done | — | `TestP3EngagementTopSpender` |

## EP-09 Loyalty Foundation

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-LOY-01 | Loyalty account with opt-in | Done | `internal/crm/loyalty/loyalty.go` | `TestP3EngagementLoyalty` |
| FR-LOY-02 | Earning rules per line / outlet / product, segment / tier multiplier, activity points | Done | `crm/loyalty/earn.go`, `activity.go` (segment multiplier) | `TestP3EngagementLoyalty`, `TestP3FixMoneyLoyalty` |
| FR-LOY-03 | Append-only points ledger | Done | `loyalty.go` | `TestP3EngagementLoyalty` |
| FR-LOY-04 | Earn on `billing.payment_settled`, idempotent; refund reverses | Done | `earn.go` | `TestP3EngagementLoyalty` |
| FR-LOY-05 | Redeem as tender at POS, front desk, app; rewards (P2 voucher, merchandise, service) | Done | `crm/loyalty/redeem.go`; POS customer picker and points tender `web/apps/staff/src/ops/p2.tsx`, `p3/commercial.tsx`; reward vouchers `commercial/voucher/p3_issue.go` | `TestP3EngagementLoyalty`, `TestP3EngagementMemberLoyalty`, `TestP3FixChannelsPOSPoints`, `TestP3FixMoneyVouchers`; Playwright *POS: the cashier picks the customer and pays part of the bill with loyalty points* |
| FR-LOY-06 | Expiry with notice | Done | `redeem.go`, daily job | `TestP3EngagementLoyalty` |
| FR-LOY-07 | Tier foundation | Done | `earn.go` | `TestP3EngagementLoyalty` |
| FR-LOY-08 | Daily liability, export | Done | `redeem.go`, report `crm.loyalty_liability` | `TestP3EngagementLoyalty` |
| FR-LOY-09 | Adjust points through approval | Done | `loyalty.go` | `TestP3EngagementLoyalty` |
| FR-LOY-10 | Anti-fraud, daily redemption limit (Should) | Done | `redeem.go` (Loyalty Policies) | `TestP3FixMoneyLoyalty` |
| EP-09 AC1 | Rp1.250.000 → 125 points once ×3 webhooks; refund reverses | Done | — | `TestP3EngagementLoyalty` |
| EP-09 AC2 | Balance × value = Loyalty Liability Report | Done | — | `TestP3EngagementLoyalty` |

## EP-10 Pricing & Promotion Engine

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-PRC-P3-01 | Peak / off-peak, holiday, package, corporate rates | Done | `internal/commercial/p3_pricing.go`, `pricing_p2.go`, `promotion_engine.go` | `TestP3CommercialPromotions`, `TestP3CommercialGolfRateTaxCodes` |
| FR-PRM-01 | Promotion types incl. Happy Hour, Buy N Get X, Buy N Price X, Bundle | Done | `commercial/promotion_engine.go` | `TestP3CommercialPromotions`, `TestP3FixMoneyPromotionTypes` |
| FR-PRM-02 | Scope: line, outlet, product, resource, segment, channel, time | Done | `commercial/promotion.go` | `TestP3CommercialPromotions` |
| FR-PRM-03 | Stacking and deterministic priority | Done | `promotion_engine.go` | `TestP3CommercialPromotions` |
| FR-PRM-04 | Promo codes, limits, rate-limited check | Done | `commercial/promotion_http.go` | `TestP3CommercialPromotions` |
| FR-PRM-05 | Budget / quota (Should) | Done | `promotion.go` | `TestP3CommercialPromotions` |
| FR-PRM-06 | Auto-apply in POS, booking, quotation, website, app; cashier removal | Done | POS / booking / public evaluate; quotation lines in `app/p3_sales.go` | `TestP3CommercialPromotions`, `TestP3FixMoneyQuotation` |
| FR-PRM-07 | Immutable pricing snapshot of promotions | Done | `commercial/promotion_ledger.go` | `TestP3CommercialPromotions` |
| FR-PRM-08 | Simulation (Should) | Done | `:simulate` | `TestP3CommercialPromotions` |
| FR-PRM-09 | POS offline cache, server validation at sync | Done | `commercial/pos` promotions, Staff App offline evaluation | `TestP3CommercialPromotions` |
| FR-PRM-10 | Approval before active | Done | `promotion.go` | `TestP3CommercialPromotions` |
| EP-10 AC1 | Buy 2 Only 200K in its hours; offline same price | Done | — | `TestP3CommercialPromotions` |
| EP-10 AC2 | Non-stackable → higher priority in every channel | Done | — | `TestP3CommercialPromotions` |

## EP-11 Package Management

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-PKG-01 | Package types and components | Done | `internal/commercial/package.go` | `TestP3CommercialPackages` |
| FR-PKG-02 | Pricing incl. per pax, per night, nett / ++, add-ons | Done | `package.go` | `TestP3CommercialPackages` |
| FR-PKG-03 | Availability, quota, segment, channel | Done | `package_booking.go` | `TestP3CommercialPackages` |
| FR-PKG-04 | All-or-nothing booking; line details from `commercial.package_booked` | Done | `package_booking.go`; golf booking `internal/golf/package.go`; stay `internal/stay/package.go`; banquet event `banquet/quotation.go` | `TestP3CommercialPackages`, `TestP3FixFulfilmentPackages`, `TestP3BanquetQuotation` |
| FR-PKG-05 | Revenue allocation = package price | Done | `commercial/package_alloc.go` | `TestP3CommercialPackages` |
| FR-PKG-06 | Consumption per component in its line | Done | golf check-in, stay check-in, voucher redemption → `ConsumeAllocation` (`app/p3_commercial.go`); Staff App *Package Use* (`p3/commercial.tsx`) | `TestP3CommercialPackages`, `TestP3FixFulfilmentPackages` |
| FR-PKG-07 | BOM link per component (K6) | Done | `commercial.package_consumed` → inventory | `TestP3CommercialPackages`, `TestP4InventoryConsumption` |
| FR-PKG-08 | Change / cancel per component, partial refund | Done | `package_booking.go`; golf / stay cancel; banquet `internal/banquet/package_cancel.go` | `TestP3CommercialPackages`, `TestP3FixFulfilmentPackages`, `TestP3FixFulfilmentBanquetPackageCancel` |
| FR-PKG-09 | History and versions | Done | `package.go` | `TestP3CommercialPackages` |
| EP-11 AC1 | Tee time full → whole Stay & Golf refused, no bungalow held | Done | — | `TestP3CommercialPackages` |
| EP-11 AC2 | Rp3.500.000 allocated exactly | Done | — | `TestP3CommercialPackages` |

## EP-12 Event Management

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-EVT-01 | Event creation and statuses | Done | `internal/banquet/events.go` | `TestP3BanquetVenueHolds` |
| FR-EVT-02 | Schedule (rundown) | Done | `banquet/ops.go` | `TestP3BanquetWedding` |
| FR-EVT-03 | Venues + bungalow, golf block, meeting room, cart, equipment | Done | `ops.go`; golf course block `banquet/golf_blocks.go` → `internal/golf/event_blocks.go` | `TestP3BanquetWedding`, `TestP3FixFulfilmentEventGolfBlock` |
| FR-EVT-04 | Participants, import, public registration, QR check-in | Done | `ops.go`; website `[lang]/events/[id]` | `TestP3BanquetRegistration` |
| FR-EVT-05 | Package, catering, equipment | Done | `banquet/charges.go` | `TestP3BanquetWedding` |
| FR-EVT-06 | Vendors | Done | `ops.go` | `TestP3BanquetWedding` |
| FR-EVT-07 | Checklist templates with reminders | Done | `ops.go`, `banquet/consumers.go` | `TestP3BanquetWedding` |
| FR-EVT-08 | Payment via schedule & folio | Done | `charges.go` | `TestP3BanquetWedding` |
| FR-EVT-09 | Event report | Done | `internal/reporting/p3_banquet.go` | `TestP3BanquetWedding` |
| FR-EVT-10 | Event calendar across venues | Done | `ops.go` | `TestP3BanquetVenueHolds` |

## EP-13 Banquet, MICE & Wedding

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-BQT-01 | Banquet packages per pax ++ / nett with minimum pax | Done | `banquet/charges.go`, demo `app/p3_banquet.go` | `TestP3BanquetWedding`, `TestP3BanquetCancellation` |
| FR-BQT-02 | Menu quotas per category | Done | `charges.go` | `TestP3BanquetWedding` |
| FR-BQT-03 | Extra buffet per pax, food stall | Done | `charges.go` | `TestP3BanquetWedding` |
| FR-BQT-04 | Corkage | Done | `charges.go` | `TestP3BanquetWedding` |
| FR-BQT-05 | Outdoor add-on with minimum pax | Done | `charges.go` | `TestP3BanquetVenueHolds` |
| FR-BQT-06 | Resource bundling, F&B vouchers | Done | `banquet/inclusions.go`, `app/p3_banquet.go` | `TestP3BanquetWedding` |
| FR-BQT-07 | Electricity quota (Should) | Done | `charges.go` | `TestP3BanquetWedding` |
| FR-BQT-08 | Payment schedule DP / H-7, reminders | Done | `charges.go`, `consumers.go` | `TestP3BanquetWedding` |
| FR-BQT-09 | Food tasting & technical meeting → BEO | Done | `ops.go` | `TestP3BanquetWedding` |
| FR-BQT-10 | Final pax cut-off, guarantee | Done | `charges.go` | `TestP3BanquetWedding` |
| FR-BQT-11 | BOM per pax → procurement requirement (K1) | Done | `banquet/beo.go` | `TestP3BanquetWedding`, `TestP3BanquetCorporate`, `TestP4ProcurementRequisitions` |
| FR-BQT-12 | Final billing with DP and terms applied | Done | `charges.go` | `TestP3BanquetWedding`, `TestP3BanquetCorporate` |
| FR-BQT-13 | MICE multi-day, multi-room, accommodation | Done | per pax per day, several venues / resources | `TestP3BanquetCorporate` |
| FR-BQT-14 | CRM pipeline and venue calendar in sync with the event | Done | `internal/crm/sales/banquet_sync.go` (`banquet.event_confirmed` / `event_cancelled`) | `TestP3FixFulfilmentCRMBanquetSync`, `TestP3BanquetVenueHolds` |
| EP-13 AC1 | DP Rp26,4 jt; Rp61,6 jt at H-7 with reminders | Done | — | `TestP3BanquetWedding` |
| EP-13 AC2 | 20 pax × Rp190.000++ = Rp4.389.000 | Done | — | `TestP3BanquetWedding` |
| EP-13 AC3 | Social Event 25 → minimum 30 pax = Rp6.583.500 | Done | — | `TestP3BanquetCancellation` |
| EP-13 AC4 | Garden add-on refused for 80 pax | Done | — | `TestP3BanquetVenueHolds` |

## EP-14 Banquet Event Order (BEO)

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-BEO-01 | BEO content | Done | `banquet/beo.go` | `TestP3BanquetWedding` |
| FR-BEO-02 | Issue / revise with versions, diff, `ETag` / `If-Match` | Done | `beo.go` | `TestP3BanquetWedding` |
| FR-BEO-03 | Distribution with read confirmation per department | Done | `beo.go` | `TestP3BanquetWedding` |
| FR-BEO-04 | BEO items on the KDS at production time | Done | `beo.go` production; Kitchen Display tab `web/apps/staff/src/areas/kitchen.tsx` | `TestP3BanquetWedding`; Playwright *Kitchen Display: kitchen staff switch to the BEO production of the day* |
| FR-BEO-05 | PDF | Done | `beo.go` | `TestP3BanquetWedding` |
| FR-BEO-06 | Locked after completion | Done | `events.go` | `TestP3BanquetWedding` |
| EP-14 AC | Revision raises the version, marks changes, kitchen pending | Done | — | `TestP3BanquetWedding` |

## EP-15 Banquet Venue & Event Resource

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-VEN-01 | Venues and layout capacities | Done | `banquet/module.go`, demo seed | `TestP3BanquetVenueHolds` |
| FR-VEN-02 | Resource type Banquet Venue with setup / teardown buffers | Done | `banquet/module.go` on the Reservation Engine | `TestP3BanquetVenueHolds` |
| FR-VEN-03 | Tentative hold with option date, Definite after DP, waitlist | Done | `banquet/events.go` | `TestP3BanquetVenueHolds`, `TestP3BanquetCancellation` |
| FR-VEN-04 | Parent / child venues (Should) | Done | — | `TestP3BanquetVenueHolds` |
| FR-VEN-05 | Venue calendar ↔ booking calendar | Done | `ops.go` | `TestP3BanquetVenueHolds` |
| EP-15 AC | Waitlist hold promoted and notified | Done | — | `TestP3BanquetVenueHolds` |

## EP-16 Tournament Management

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-TRN-01 | Creation: formats, divisions, eligibility | Done | `internal/golf/tournament/setup.go` | `TestP3TournamentClub` |
| FR-TRN-02 | Tee sheet block | Done | `setup.go`, `internal/golf/tournament_teesheet.go` | `TestP3TournamentClub` |
| FR-TRN-03 | Registration Back Office / app / website, fee and package | Done | `registration.go`, `self.go`; website `[lang]/tournaments` | `TestP3TournamentClub` |
| FR-TRN-04 | Registered / Waitlisted / Withdrawn, refunds per policy | Done | `registration.go`, `policy.go` | `TestP3TournamentClub`, `TestP3TournamentRoundsAndCancel` |
| FR-TRN-05 | Flighting auto / manual, sponsor pairing | Done | `draw.go` | `TestP3TournamentClub`; unit `TestBuildFlights`, `TestHandicaps` |
| FR-TRN-06 | Sequential / shotgun with A/B, start sheet | Done | `draw.go` | `TestP3TournamentClub`; unit `TestShotgunStarts` |
| FR-TRN-07 | Scoring tablet / app / desk, attestation, correction | Done | `scoring.go` | `TestP3TournamentClub` |
| FR-TRN-08 | Live leaderboard gross / net, countback; app, website, screen | Done | `leaderboard.go`, `public.go` (consent masking on website, Leaderboard Screen and Member App) | `TestP3TournamentClub` |
| FR-TRN-09 | Nearest to Pin, Longest Drive, Hole-in-One | Done | `sponsors.go` | `TestP3TournamentClub` |
| FR-TRN-10 | Sponsors and prizes | Done | `sponsors.go` | `TestP3TournamentClub` |
| FR-TRN-11 | Finalize → Hall of Fame | Done | `results.go` | `TestP3TournamentClub`, `TestP3TournamentRoundsAndCancel` |
| FR-TRN-12 | Tournament report | Done | `internal/reporting/p3_tournament.go` | `TestP3TournamentClub` |
| FR-TRN-13 | Event integration (venue, catering, sponsor billing) | Done | `integration.go` | `TestP3TournamentCorporate` |
| EP-16 AC1 | Shotgun 72 = 18 flights × 4, start sheet | Done | — | `TestP3TournamentClub` |
| EP-16 AC2 | Offline caddy-tablet scores → leaderboard ≤ 1 min | Done | — | `TestP3TournamentClub` |
| EP-16 AC3 | Finalize → Hall of Fame champion public by consent | Done | — | `TestP3TournamentClub` |

## EP-17 Unified Folio & Billing Expansion

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-BIL-P3-01 | Customer folio across lines, merge | Done | `internal/billing/p3_folio.go` | `TestP3BillingCorporateInvoices` |
| FR-BIL-P3-02 | Cross-line split bill | Done | `p3_folio.go` | `TestP3BillingCorporateInvoices` |
| FR-BIL-P3-03 | Generic payment schedule | Done | `billing/p3_schedule.go` | `TestP3BillingPaymentSchedules` |
| FR-BIL-P3-04 | Invoice, gap-free numbering per property, statuses, credit note | Done | `billing/p3_invoice.go` | `TestP3BillingCorporateInvoices` |
| FR-BIL-P3-05 | Corporate billing, credit limit, statement, aging | Done | `p3_invoice.go`, `p3_policy.go` | `TestP3BillingCorporateInvoices` |
| FR-BIL-P3-06 | Payment allocation, DP applied at final billing | Done | `p3_invoice.go` | `TestP3BillingCorporateInvoices`, `TestP3BanquetCorporate` |
| FR-BIL-P3-07 | Installments (DP ≥ 30 %, ≤ 12×) | Done | `p3_schedule.go` | `TestP3BillingPaymentSchedules` |
| FR-BIL-P3-08 | Write-off and refund through approval | Done | `p3_invoice.go` | `TestP3BillingCorporateInvoices` |
| FR-BIL-P3-09 | Financial evidence traceable from the invoice | Done | invoice detail (allocations, credit notes, write-offs, snapshots) | `TestP3BillingCorporateInvoices` |
| FR-BIL-P3-10 | `billing.invoice_issued` / `invoice_paid` / `invoice_voided` (K2) | Done | `p3_invoice.go` | `TestP3BillingCorporateInvoices`, `TestP3FixMoneyBilling`, `TestP4AccountingBillingAR` |
| EP-17 AC1 | One corporate invoice, DP deducted | Done | — | `TestP3BanquetCorporate`, `TestP3BillingCorporateInvoices` |
| EP-17 AC2 | Overdue → aging and reminder; credit limit needs approval | Done | `p3_invoice.go` reminders | `TestP3BillingCorporateInvoices`, `TestP3FixMoneyBilling` |

## EP-18 Cashier Shift, End-of-Day & Night Audit

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-EOD-01 | Billing cashier shift | Done | `billing/p3_dayclose.go`; Staff App `/ops/front-desk/cashier` | `TestP3BillingCashierShiftsNightAudit` |
| FR-EOD-02 | End-of-day per outlet / cashier, offline synced | Done | `internal/app/p3_dayclose.go` checks | `TestP3BillingCashierShiftsNightAudit` |
| FR-EOD-03 | Night audit: checks, no-shows, automatic postings (bungalow per night), frozen | Done | `p3_dayclose.go`, `internal/stay/night_audit.go` (Stay Policies `roomChargePosting`, `autoNoShow`) | `TestP3BillingCashierShiftsNightAudit`, `TestP3FixFulfilmentNightAudit` |
| FR-EOD-04 | Daily Revenue Report | Done | `p3_dayclose.go` | `TestP3BillingCashierShiftsNightAudit` |
| FR-EOD-05 | Next business day; reopen through approval | Done | `p3_dayclose.go` | `TestP3BillingCashierShiftsNightAudit` |
| FR-EOD-06 | `billing.business_day_closed` (K4) | Done | `p3_dayclose.go` | `TestP3BillingCashierShiftsNightAudit` |
| EP-18 AC | Blocked by an open shift; totals reconcile | Done | — | `TestP3BillingCashierShiftsNightAudit` |

## EP-19 Member & Guest App P3

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-APP-P3-01 | Loyalty | Done | `web/apps/member/src/areas/engagement.tsx` | `TestP3EngagementMemberLoyalty` |
| FR-APP-P3-02 | Offers incl. personal promo codes | Done | `web/apps/member/src/areas/commercial.tsx` | `TestP3CommercialPromotions` |
| FR-APP-P3-03 | Events, QR ticket, My Events | Done | `web/apps/member/src/areas/banquet.tsx` | `TestP3BanquetRegistration` |
| FR-APP-P3-04 | Tournaments | Done | `web/apps/member/src/areas/tournament.tsx` | `TestP3TournamentClub` |
| FR-APP-P3-05 | Packages | Done | `areas/commercial.tsx` | `TestP3CommercialPackages` |
| FR-APP-P3-06 | Support (feedback, complaints, ticket status) | Done | `areas/engagement.tsx` | `TestP3EngagementComplaints` |
| FR-APP-P3-07 | Invoices and payment schedule with online payment | Done | `web/apps/member/src/p3.tsx`; `billing/p3_schedule_link.go` | `TestP3BillingCorporateInvoices`, `TestP3FixChannelsSchedulePayOnline`; Playwright *Member App: the member pays the down payment of a payment schedule online* |
| FR-APP-P3-08 | Communication preferences | Done | `areas/engagement.tsx` | `TestP3EngagementCampaigns` |

## EP-20 Website P3

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-WEB-P3-01 | Wedding & Banquet, Events, Promotions, Packages pages from structured data | Done | `web/apps/web/app/[lang]/{wedding-banquet,events,promotions,packages}`; header from the CMS menu (`[lang]/nav.tsx`, `nav-model.ts`) | `TestP3BanquetRegistration`, `TestP3CommercialPromotions`, `TestP3CommercialPackages`, `TestP4FixWebsiteNavigation`; Playwright *Website: VIP Suite and Meeting & MICE are reachable from the header (demo menu)* |
| FR-WEB-P3-02 | Inquiry forms (wedding, banquet, MICE, corporate golf, tournament, membership) → lead with consent, anti-bot | Done | `[lang]/events/forms.tsx`, `booking.tsx`, `contact/page.tsx`; `crm.PublicWrite` | `TestP3SalesLeads`, `TestP3FixChannelsWebsiteConsent`; Playwright *Website: inquiry with an unticked marketing consent, corporate golf…* |
| FR-WEB-P3-03 | Book event / public tournament with payment | Done | `[lang]/events/[id]`, `[lang]/tournaments/[id]` | `TestP3BanquetRegistration`, `TestP3TournamentClub` |
| FR-WEB-P3-04 | Book package | Done | `[lang]/packages/[code]` | `TestP3CommercialPackages` |
| FR-WEB-P3-05 | Quotation acceptance and DP / invoice payment through a secure link | Done | `[lang]/quotation/[token]` (Pay down payment step), `[lang]/payment/[token]`, `[lang]/invoice/[token]` | `TestP3SalesQuotationToCommission`, `TestP3BillingCorporateInvoices`, `TestP3FixChannelsSchedulePayOnline` |
| FR-WEB-P3-06 | Public leaderboard by consent (Should) | Done | `[lang]/tournaments/[id]` leaderboard | `TestP3TournamentClub` |
| FR-WEB-P3-07 | Promo code at public checkout, rate limited | Done | package booking, `/public/promo-codes:check` | `TestP3CommercialPromotions` |

## EP-21 Operational Interfaces P3 (`ops`)

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-OPS-P3-01 | Event Operations | Done | `web/apps/staff/src/p3/banquet.tsx` (`/ops/events`, check-in) | `TestP3BanquetRegistration` |
| FR-OPS-P3-02 | Tournament Desk | Done | `web/apps/staff/src/p3/tournament.tsx` | `TestP3TournamentClub` |
| FR-OPS-P3-03 | POS & Front Desk: Apply Promotion, Redeem Points, cashier shift | Done | `ops/p2.tsx` POS (customer, points tender, promotions), `/ops/front-desk/redeem-points`, `/ops/front-desk/cashier` | `TestP3FixChannelsPOSPoints`, `TestP3CommercialPromotions`, `TestP3BillingCashierShiftsNightAudit`; Playwright POS points |
| FR-OPS-P3-04 | Leaderboard Screen kiosk (Should) | Done | `web/apps/staff/src/areas/screen.tsx` (masked by consent) | `TestP3TournamentClub` |
| FR-OPS-P3-05 | Offline event check-in and scoring desk, idempotent sync | Done | offline queue in `p3/banquet.tsx`, `p3/tournament.tsx` | `TestP3BanquetRegistration`, `TestP3TournamentClub` |

## EP-22 Club Policies P3

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-POL-P3-01 | Banquet Policies | Done | `internal/banquet/policies.go` (DP 30 %, 40 % at H-60, H-7) | `TestP3BanquetCancellation`, `TestP3FixMoneyBilling` |
| FR-POL-P3-02 | Pricing & Promotion Policies (stacking, manual discount per role, approval) | Done | `commercial/promotion.go` (default manual discount tiers) | `TestP3CommercialPromotions`, `TestP4FixStockDiscountTiers` |
| FR-POL-P3-03 | Sales Policies | Done | `crm/sales/module.go` | `TestP3SalesQuotationToCommission`, `TestP3FixMoneyQuotation` |
| FR-POL-P3-04 | Loyalty Policies | Done | `crm/loyalty/loyalty.go` | `TestP3EngagementLoyalty`, `TestP3FixMoneyLoyalty` |
| FR-POL-P3-05 | Event & Tournament Policies | Done | `banquet/policies.go`, `golf/tournament/policy.go` | `TestP3TournamentRoundsAndCancel`, `TestP3BanquetRegistration` |
| FR-POL-P3-06 | Credit Policies | Done | `billing/p3_policy.go` | `TestP3BillingCorporateInvoices` |

## EP-23 KPI, Dashboard & Reports

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-RPT-P3-01 | Banquet Performance | Done | `internal/reporting/p3_banquet.go`; Staff App `areas/management.tsx` | `TestP3BanquetWedding` |
| FR-RPT-P3-02 | CRM Performance | Done | `reporting/p3_sales.go`, `p3_engagement.go` | `TestP3SalesQuotationToCommission`, `TestP3EngagementTopSpender` |
| FR-RPT-P3-03 | Commercial Performance extended | Done | `reporting/p3_commercial.go` | `TestP3CommercialPromotions` |
| FR-RPT-P3-04 | Golf Performance + tournament | Done | `reporting/p3_tournament.go` | `TestP3TournamentClub` |
| FR-RPT-P3-05 | The 22 listed reports | Done | `reporting/p3_*.go` | exercised across the P3 tests |
| FR-RPT-P3-06 | Filters, CSV / XLSX, permission per report | Done | `internal/reporting/reporting.go` | P0 report tests, `TestP3BillingCorporateInvoices` |
| EP-23 AC | Dashboard = report; AR aging = open invoices | Done | — | `TestP3BanquetWedding`, `TestP3BillingCorporateInvoices` |

## EP-24 Integrations P3

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-INT-P3-01 | WhatsApp inbound → lead + interaction | Done | `crm/sales/capture.go` on the messaging capability (adapter `whatsapp-cloud`; the trial runs on `mock-whatsapp`) | `TestP3SalesLeads` |
| FR-INT-P3-02 | E-mail inbound → lead (Should) | Done | `capture.go` (the department mailbox forwards to the API) | `TestP3SalesLeads` |
| FR-INT-P3-03 | Meta / TikTok lead form adapter | Deferred | §16 #6: social leads entered manually with a mandatory source | `TestP3SalesLeads` (manual source) |
| FR-INT-P3-04 | Payment link for DP, terms and invoices | Done | `billing/p3_schedule_link.go`, `/public/invoices/{token}:pay`; gateway per method & property through the integration layer (`internal/billing/api.go`) | `TestP3BillingCorporateInvoices`, `TestP3FixChannelsSchedulePayOnline`, `TestP3FixMoneyBilling` |
| FR-INT-P3-05 | Accounting Export extended (invoices, DP, package allocation, promotions, points, commission, business day) | Done | `billing/p3_export.go` + sections (`app/p3_sales.go` commission) | `TestP3BillingCorporateInvoices`, `TestP3CommercialPackages`, `TestP3EngagementLoyalty`, `TestP34LeftoversTestGaps` (commission section: earned / clawback / adjustment amounts) |
| FR-INT-P3-06 | P3 notification templates ID / EN | Done | `Templates()` of banquet, crm/sales, crm/engagement, golf/tournament, billing (seeded per instance) | deliveries asserted in `TestP3QuotationOtpAndEMeterai` (quotation code), `TestP3FixMoneyBilling` (overdue reminder), `TestP3EngagementCampaigns` |
| FR-INT-P3-07 | Third-party e-signature (Should) | Deferred | §16 #18: certified PSrE e-signature postponed | — |

## EP-25 Migration wave 3

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-MIG-P3-01 | Future banquets and events with DP received | Done | `internal/banquet/import.go`; runbook [`banquet-migration-p3.md`](runbooks/banquet-migration-p3.md) | `TestP3BanquetImport` |
| FR-MIG-P3-02 | Open corporate AR with due dates | Done | `billing/p3_import.go`, `p3_import_http.go` (`POST /billing/invoices:import`); Staff App Invoices → *Import Corporate AR* (`p3/ar_import.tsx`) | `TestP3FixMoneyARImport` |
| FR-MIG-P3-03 | Tournament history and Hall of Fame champions (Should) | Done | `golf/tournament/import.go` | `TestP3TournamentHistoryImport` |
| FR-MIG-P3-04 | Active leads & opportunities from the sales spreadsheet | Done | `crm/sales/import.go` (`/crm/leads:import`); runbook [`leads-migration-p3.md`](runbooks/leads-migration-p3.md) | `TestP3SalesLeads` |
| FR-MIG-P3-05 | Reconciliation: DP = deposit liability, corporate AR, future events; signed | Done | `banquet/import.go`, `billing/p3_import.go` | `TestP3BanquetImport`, `TestP3FixMoneyARImport` |
| FR-MIG-P3-06 | Two dry runs on Staging, tournament & banquet cut-over runbook | Done | runbooks [`banquet-migration-p3.md`](runbooks/banquet-migration-p3.md), [`tournament-migration-p3.md`](runbooks/tournament-migration-p3.md), [`corporate-ar-migration-p3.md`](runbooks/corporate-ar-migration-p3.md), [`leads-migration-p3.md`](runbooks/leads-migration-p3.md) | 🟡 dry runs on Staging |
| §11 migration CLI | `oneclub import rhapsody --scope=banquet\|corporate-ar\|tournament-history`, `oneclub import sales` | Done | `oneclub import <scope>` (also `import rhapsody --scope=…`) in `cmd/oneclub`, scopes `sales`, `banquet`, `tournament-history`, `corporate-ar` in `internal/app/dataimport`: the API importer functions run as the system actor; dry run by default (rolled back), `-commit`, idempotent re-run, rejected rows with their line, reconciliation (banquet DP = deposit liability, AR control total) printed and written as a JSON report | `TestP34LeftoversImportCLI` (CSV fixtures `test/e2e/testdata/p34import`) |

## EP-26 Production Readiness (Release 3)

| ID | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| FR-REL-P3-01 | k6: tournament registration, campaign 10.000, promotions at POS peak, night audit on a month | Done | `test/load/tournament-registration.js`, `campaign-10k.js`, `promotion-pos-peak.js`, `night-audit.js`; targets in [`production-readiness.md`](runbooks/production-readiness.md) §4 | 🟡 run on Staging, results to record |
| FR-REL-P3-02 | Playwright for the §9 flows (wedding, corporate event, tournament, package, promotion offline, night audit) | Partial | `web/e2e/p3.spec.ts` (POS points, Kitchen Display BEO, website consent, Member App DP), `p34-flows.spec.ts` (§9.1 wedding chained in the browser: website inquiry → quotation accepted on the public link with the one-time code → DP → Definite → BEO issued by the Banquet Manager), `p4fix-website.spec.ts`; the other flows run as Go API e2e | Browser runs of the corporate event, tournament, package, promotion offline and night audit flows not written |
| FR-REL-P3-03 | P1–P2 regression on the Release 3 build | Done | CI `backend` + `browser` jobs | P0–P4 suites green together |
| FR-REL-P3-04 | Pen test of the new public endpoints | Done | scope in [`production-readiness.md`](runbooks/production-readiness.md) §6 (Release 3 table); rate limits, random tokens, single-use acceptance | 🟡 external pen test |
| FR-REL-P3-05 | VPS build with `garble`, no source maps | Done | `deploy/docker/Dockerfile`, `Dockerfile.web` (`SOURCEMAPS=false`) | build config |
| FR-REL-P3-06 | Training per role and hypercare 2–4 weeks per wave | Deferred | non-code release item: no Release 3 training / hypercare plan yet ([`production-readiness.md`](runbooks/production-readiness.md) §7 covers Release 1 and Release 4) | — |

## §12 Non-functional requirements

| Area | Requirement | Status | Implementation | Evidence |
|---|---|---|---|---|
| NFR-Availability | 99,5 %; event / tournament days monitored specially | Partial | P1 healthwatch and uptime probes ([`production-readiness.md`](runbooks/production-readiness.md) §3) | No event-day / tournament-day monitoring plan |
| NFR-Performance | p95 < 300 ms read, < 800 ms quotation / invoice / payment / redemption / promo; promo resolve < 100 ms local | Done | k6 scripts with these thresholds | 🟡 Staging run |
| NFR-Real-time | Leaderboard ≤ 1 min (< 2 s online); BEO and venue calendar < 2 s | Done | SSE `/golf/tournaments/stream`, live hooks | `TestP3TournamentClub` |
| NFR-Concurrency | 0 double booking of venue / package, 0 double redemption, tournament quota | Done | exclusion constraints, row locks, idempotency keys | `TestP3BanquetVenueHolds`, `TestP3EngagementLoyalty`, `TestP3CommercialPromotions`, `TestP3TournamentClub` |
| NFR-Campaign | 10.000 messages within BSP limits, delivery status recorded | Done | throttled batches, River retry | `TestP3EngagementCampaigns`, `TestP3FixMoneyCampaignTracking`; k6 `campaign-10k.js` 🟡 |
| NFR-Offline | POS promotions, event check-in, scoring desk offline; idempotent sync | Done | Staff App offline queues | `TestP3CommercialPromotions`, `TestP3BanquetRegistration`, `TestP3TournamentClub`, `TestP3FixChannelsPOSPoints` |
| NFR-Financial integrity | Immutable snapshots; points and DP ledgers = daily liability; business day closed before journal export | Done | snapshots, append-only ledgers | `TestP3EngagementLoyalty`, `TestP3BanquetImport`, `TestP3CommercialPackages`, `TestP3BillingCashierShiftsNightAudit` |
| NFR-Document integrity | Versioned quotation / BEO / invoice; gap-free invoice numbers; `ETag` for BEO and folio | Done | `billing/p3_invoice.go`, `banquet/beo.go`, `billing/http.go` | `TestP3BanquetWedding`, `TestP3BillingCorporateInvoices` |
| NFR-Privacy | Consent per channel, opt-out, lead retention then anonymise; Top Spender internal | Done | consent on every website form, leaderboard masking on every surface, `crm/sales/jobs.go` (anonymise after 2 years) | `TestP3FixChannelsWebsiteConsent`, `TestP3TournamentClub`, `TestP3SalesJobs`, `TestP3EngagementTopSpender` |
| NFR-Security | ASVS L2; random, expiring, single-use acceptance tokens; rate limits on promo codes and inquiries | Done | public limiter, one-time code with cooldown and lock (`crm/sales/p3_acceptance.go`) | `TestP3SalesQuotationToCommission`, `TestP3QuotationOtpAndEMeterai`, `TestP3CommercialPromotions` |
| NFR-Regression | P1–P2 suites pass on every P3 merge | Done | CI | P0–P4 suites green |
| NFR-Language | English labels; ID / EN messages and documents | Done | ID / EN templates, bilingual PDFs | `TestP3FixMoneyQuotation`, `TestP3BanquetWedding` (BEO PDF) |

## §16 Decisions

| # | Decision | Status | Implementation | Evidence |
|---|---|---|---|---|
| §16 #1 | P3 ‖ P4 as wave 2 | n/a | process (Technical Doc §12.3) | — |
| §16 #2 | Default pipeline stages and probabilities (§16.1) | Done | `crm/sales/opportunity.go` | `TestP3SalesQuotationToCommission` |
| §16 #3 | Round-robin, first response ≤ 1 h 08–20, 09:00 next day, escalation | Done | Sales Policies defaults `crm/sales/module.go` | `TestP3SalesLeads`, `TestP3SalesJobs` |
| §16 #4 | Commission on paid; flat rates per line; clawback ≤ 90 days | Done | `module.go` defaults, `commission.go` | `TestP3SalesConversions`, `TestP3FixMoneyCommission` |
| §16 #5 | Discount limits per role (5 / 10 / 20 %, above → GM) for quotation and POS | Done | `sales.DefaultRoleDiscountLimits`, `commercial.DefaultManualDiscountLimits` (GM 100 %) | `TestP3FixMoneyQuotation`, `TestP4FixStockDiscountTiers` — tiers awaiting product confirmation (see below) |
| §16 #6 | Social leads manual; Meta / TikTok postponed | Done | manual source | `TestP3SalesLeads` |
| §16 #7 | Loyalty: 1 point / Rp10.000 net, 1 point = Rp100, 12 months, participating lines | Done | `crm/loyalty/loyalty.go` defaults | `TestP3EngagementLoyalty` |
| §16 #8 | No stacking, best price for the customer | Done | `DefaultPromotionPolicy.Selection = "best_price"` (`commercial/promotion.go`) | `TestP3CommercialPromotions`, `TestP3FixMoneyPromotionTypes` |
| §16 #9 | Allocation proportional to standalone price; caddy fee as liability first | Done | `package_alloc.go` | `TestP3CommercialPackages` |
| §16 #10 | Installments: DP ≥ 30 %, ≤ 12×; packages ≥ Rp50 jt like banquet | Done | `p3_schedule.go` | `TestP3BillingPaymentSchedules` |
| §16 #11 | Banquet terms: DP 30 %, 40 % H-60, H-7 settlement, pax H-7 −10 %, cancellation tiers, corkage, 10 kW | Done | `banquet/policies.go` | `TestP3BanquetWedding`, `TestP3BanquetCancellation`, `TestP3FixMoneyBilling` |
| §16 #12 | Formats stroke / stableford; local handicap + PGI input | Done | `golf/tournament/policy.go` | `TestP3TournamentRoundsAndCancel` |
| §16 #13 | 30 days, Rp50 jt credit limit, statement on the 1st | Done | `billing/p3_policy.go` | `TestP3BillingCorporateInvoices` |
| §16 #14 | Automatic night audit at 02:00; Duty Manager resolves exceptions | Done | `billing/p3_dayclose.go` (auto night audit job) | `TestP3FixFulfilmentNightAudit` |
| §16 #15 | Wave order R3.1–R3.4; Should epics may slip | n/a | process | — |
| §16 #16 | New labels / statuses (§7.6) | Done | Staff App labels | `TestStaffAppAreas` |
| §16 #17 | Migration sources: Rhapsody for customers, Excel for events / DP / AR / leads / tournaments | Done | Excel / CSV importers above | `TestP3BanquetImport`, `TestP3FixMoneyARImport`, `TestP3SalesLeads`, `TestP3TournamentHistoryImport` |
| §16 #18 | Acceptance by link + OTP with audit trail; e-Meterai above Rp5 jt | Done (mock provider for trial) | `crm/sales/p3_acceptance.go`; e-Meterai capability `internal/platform/integration/p4_emeterai.go` (`mock-emeterai`); OTP delivered through `mock-whatsapp` / `mock-email`; certified PSrE deferred (FR-QUO-09) | `TestP3QuotationOtpAndEMeterai`; unit `TestEMeteraiThreshold`, `TestMockEMeterai`, `TestRedactions` |
| §16 #19 | Lead retention 2 years then anonymise | Done | `crm/sales/jobs.go` | `TestP3SalesJobs` |
| §16 #20 | Phased release per wave | n/a | module flags per instance | — |

## §9 End-to-end flows

| Flow | Status | Evidence (hop by hop) |
|---|---|---|
| §9.1 Wedding | Done | WhatsApp lead `TestP3SalesLeads` → quotation with discount and link `TestP3SalesQuotationToCommission`, `TestP3FixMoneyQuotation` → accepted with one-time code `TestP3QuotationOtpAndEMeterai` → event Definite, venue, schedule `TestP3BanquetQuotation` → DP paid online `TestP3FixChannelsSchedulePayOnline` → tasting, BEO v1/v2, final billing `TestP3BanquetWedding` → production on the KDS (Playwright) → commission `TestP3FixMoneyCommission` → procurement requirement `TestP4ProcurementRequisitions` → business day close `TestP3BillingCashierShiftsNightAudit`. 🟡 one chained UAT run |
| §9.2 Corporate event | Done | corporate golf lead `TestP3FixChannelsWebsiteConsent` → quotation → Corporate Package all-or-nothing `TestP3CommercialPackages` → golf bookings and stays from the package, nominee check-in consumes `TestP3FixFulfilmentPackages` → corporate invoice − DP, aging `TestP3BanquetCorporate`, `TestP3BillingCorporateInvoices` → AR `TestP4AccountingBillingAR`. 🟡 one chained UAT run |
| §9.3 Club tournament | Done | `TestP3TournamentClub` (stableford, 72 players, waitlist, flighting, shotgun, offline tablet scores, leaderboard, NTP / LD, finalize, Hall of Fame, report) |
| §9.4 Promotion & loyalty | Done | segment campaign with unique codes `TestP3EngagementCampaigns`, `TestP3CommercialPromotions` → happy hour, POS offline `TestP3CommercialPromotions`, `TestP3FixMoneyPromotionTypes` → customer + personal code + points at POS `TestP3FixChannelsPOSPoints` → earn, tier, redeem `TestP3EngagementLoyalty` → Top Spender `TestP3EngagementTopSpender` → campaign conversion `TestP3FixMoneyCampaignTracking`. 🟡 one chained UAT run |
| §9.5 Night audit | Done | shifts and checks `TestP3BillingCashierShiftsNightAudit`; bungalow per night, no-show, automatic run after 02:00 `TestP3FixFulfilmentNightAudit`; liability in the Daily Revenue Report; 00:30 transaction to the next business day `TestP3BillingCashierShiftsNightAudit` |

## §11 Outbox events

| Event (PRD) | Status | Producer | Evidence |
|---|---|---|---|
| `crm.lead_created` | Done | `internal/crm/sales/module.go` | `TestP3SalesLeads` |
| `crm.lead_assigned` | Done | `crm/sales/module.go` | `TestP3SalesLeads` |
| `crm.lead_converted` | Done | `crm/sales/module.go` | `TestP3SalesLeads` |
| `crm.opportunity_won` | Done | `crm/sales/module.go` | `TestP3SalesConversions`, `TestP3EngagementLoyalty` |
| `crm.opportunity_lost` | Done | `crm/sales/module.go` | `TestP3SalesConversions`, `TestP3FixFulfilmentCRMBanquetSync` |
| `crm.quotation_sent` | Done | `crm/sales/module.go` | `TestP3BanquetQuotation` |
| `crm.quotation_accepted` | Done | `crm/sales/module.go` | `TestP3BanquetQuotation`, `TestP3SalesConversions`, `TestP3TournamentCorporate` |
| `crm.campaign_sent` | Done | `internal/crm/engagement/engagement.go` | `TestP3EngagementCampaigns`, `TestP3FixMoneyVouchers` |
| `crm.ticket_escalated` | Done | `crm/engagement/engagement.go` | `TestP3EngagementComplaints` |
| `crm.ticket_resolved` | Done | `crm/engagement/engagement.go` | `TestP3EngagementComplaints` |
| `crm.points_earned` / `points_redeemed` / `points_expired` | Done | renamed `crm.loyalty_points_changed` `{kind}` (`internal/crm/loyalty/loyalty.go`; [contracts](p3-p4-contracts.md)) | `TestP3EngagementLoyalty`, `TestP4AccountingAutomaticPosting` |
| `crm.tier_changed` | Done | `crm/loyalty/loyalty.go` | `TestP3EngagementLoyalty` |
| `crm.commission_approved` | Done | `crm/sales/module.go` | `TestP3SalesQuotationToCommission`, `TestP4FixLedgerCommission` |
| `commercial.promotion_applied` | Done | `internal/commercial/promotion_ledger.go` | `TestP3CommercialPromotions`, `TestP4AccountingAutomaticPosting` |
| `commercial.package_booked` | Done | `commercial/package_booking.go` | `TestP3CommercialPackages`, `TestP3FixFulfilmentPackages` |
| `commercial.package_consumed` | Done | `package_booking.go` | `TestP3CommercialPackages`, `TestP4InventoryConsumption` |
| `banquet.event_definite` | Done | renamed `banquet.event_confirmed` (`internal/banquet/module.go`; [contracts](p3-p4-contracts.md)) | `TestP3FixFulfilmentCRMBanquetSync`, `TestP3TournamentCorporate` |
| `banquet.event_completed` | Done | `banquet/module.go` | `TestP3BanquetWedding`, `TestP4InventoryConsumption` |
| `banquet.event_cancelled` | Done | `banquet/module.go` | `TestP3BanquetCancellation`, `TestP3FixFulfilmentCRMBanquetSync` |
| `banquet.beo_issued` | Done | `banquet/module.go` | `TestP3BanquetWedding`, `TestP4ProcurementRequisitions` |
| `banquet.beo_revised` | Done | `banquet/module.go` | `TestP4ProcurementRequisitions`, `TestP4InventoryConsumption` |
| `golf.tournament_registration_confirmed` | Done | `internal/golf/tournament/module.go` | `TestP3TournamentClub` |
| `golf.tournament_started` | Done | `golf/tournament/module.go` | `TestP3TournamentClub` |
| `golf.tournament_finalized` | Done | `golf/tournament/module.go` (+ `golf.tournament_results_published` for the Hall of Fame) | `TestP3TournamentClub` |
| `billing.invoice_issued` | Done | `internal/billing/p3_invoice.go` | `TestP3BillingCorporateInvoices` |
| `billing.invoice_paid` | Done | `p3_invoice.go` | `TestP3BillingCorporateInvoices`, `TestP3BanquetCorporate` |
| `billing.invoice_overdue` | Done | `p3_invoice.go` | `TestP3FixMoneyBilling` |
| `billing.invoice_voided` | Done | `p3_invoice.go` | `TestP3FixMoneyBilling`, `TestP4FixLedgerBillingPostings` |
| `billing.payment_schedule_due` | Done | `billing/p3_schedule.go` | `TestP3BillingPaymentSchedules`, `TestP3BanquetWedding` (reminders) |
| `billing.business_day_closed` | Done | `billing/p3_dayclose.go` | `TestP3BillingCashierShiftsNightAudit` |

## Open items

| # | Item | Owner (suggested) |
|---|---|---|
| 1 | FR-REL-P3-02: Playwright runs of the remaining §9 flows (corporate event, tournament, package, promotion offline, night audit) — the wedding flow runs in `web/e2e/p34-flows.spec.ts` | Engineering (QA) |
| 2 | FR-REL-P3-06: Release 3 training per role (sales, banquet, event, marketing, finance AR, tournament desk) and 2–4 weeks hypercare per wave; add a Release 3 row to [`production-readiness.md`](runbooks/production-readiness.md) §7 | Delivery lead + club |
| 3 | NFR-Availability: event-day / tournament-day monitoring plan (on-call, probes on public registration and leaderboard) | OneClub ops |
| 4 | 🟡 k6 Release 3 scenarios on Staging with results recorded; external pen test of the P3 public endpoints; two migration dry runs per runbook; chained UAT of §9.1–§9.5 with club sign-off per wave | Engineering + OneClub ops + club |
| 5 | Real providers before production: configure WhatsApp (`whatsapp-cloud`), e-mail (`sendgrid` / `smtp`) and Xendit per instance (the trial runs on `mock-whatsapp`, `mock-email`, `mock-payment`); **e-Meterai has only the sandbox adapter `mock-emeterai`** — a production adapter for the club's e-Meterai distributor must be built (capability `e_meterai` is ready) | Platform / integration area + OneClub ops + club |
| 6 | FR-QUO-09 / FR-INT-P3-07 certified PSrE e-signature and FR-INT-P3-03 Meta / TikTok lead adapter — deferred by §16 #6 / #18, to be re-planned | Product |

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
