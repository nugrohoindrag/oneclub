# P2 traceability — requirement → implementation → evidence

Status: ✅ implemented and verified by an automated test · 🟡 implemented, verification needs an environment the dev
machine does not have (Staging / VPS / CI browsers / club sign-off) · ⚠ implemented with a deliberate deviation · ⏳ open.

Test locations: `test/e2e/p2_*_test.go` (Go acceptance tests on real PostgreSQL, run together with the P0 and P1
suites), `web/e2e/p2.spec.ts` (Playwright, run in CI by `.github/scripts/browser-stack.sh`), `test/load/*.js` (k6),
unit tests next to the code.

P2 runs on top of P1 (branch `feat/p2-on-p1`): P1's modules keep their files; P2 lives in sub-packages and additive
contract files (`p2_*.go`, `lines.go`, `pricing_p2.go`, `sales_api.go`). What P2 needed from P1 files is listed in
`docs/p2-contract-review.md`.

Every client is web: website (Next.js), Member App, Ops/POS and Caddy Tablet as installable offline PWAs, Back Office
and Platform Admin as SPAs. Native apps are out of scope for the whole roadmap (Tech Doc open decision #8 resolved).

## Audit & route coverage

The e2e harness fails the run when a mutating route succeeds without an audit entry, or when any mutating route is
never exercised successfully. Current run: **AUDIT_COVERAGE mutating routes exercised, 0 without audit** (P0 + P1 + P2).

## Exit criteria (PRD §13.1)

| # | Criterion | Evidence | Status |
|---|---|---|---|
| 1 | Reservation Engine without double booking / overbooking | `TestP2ReservationEngine` (EXCLUDE + capacity CHECK in the database, all-or-nothing multi-line); k6 `court-rush.js` | ✅ / 🟡 load run on Staging |
| 2 | Golf journey up to playing history, incl. offline | `TestP2GolfRound` on P1's booking, check-in and starter (offline tablet queue, re-sync duplicates, device handover, caddy replacement and fee split, correction workflow), `TestP2GolfPaceAndRange`, `TestP2GolfReciprocal`, `TestP2GolfOperationsCoverage` | ✅ |
| 3 | Rhapsody POS switched off | `TestP2POSKitchenBOM`, `TestP2RhapsodyImport` (stage, validate, load, reconcile), runbook `docs/runbooks/rhapsody-migration-p2.md` | ✅ code / 🟡 club parity sign-off & cutover |
| 4 | Sport Club, Classes, Stay & Venue operate | `TestP2SportClubEntryAccess`, `TestP2Classes`, `TestP2StayAndVenue`, `TestP2PricingRateCards` | ✅ |
| 5 | Voucher & prepaid accurate | `TestP2VoucherPrepaid` (liability = deferred sub-ledger, accounting export), `TestP2ReportsAndDashboards` (Voucher Liability Report) | ✅ / 🟡 2 weeks of operation |
| 6 | Complete membership lifecycle | `TestP2MembershipLifecycle` (annual fee job, auto-suspension), `TestP2MemberApp` | ✅ / 🟡 job on Staging |
| 7 | P2 KPIs consistent with reports | `TestP2ReportsAndDashboards` | ✅ |
| 8 | No P1 regression | P0, P1 and P2 suites pass together on the integrated branch | ✅ |
| 9 | UAT approved | Scenarios §9 automated above; club sign-off per release wave | 🟡 |

## Epics

| EP | Implementation | Frontend | Evidence | Status |
|---|---|---|---|---|
| EP-01 Reservation Engine | `internal/reservation` (capacity mode, multi-line, availability, paid deposit confirms, notifications) | Back Office `booking/all-lines`, Website availability | `TestP2ReservationEngine`, `TestP2Website` | ✅ |
| EP-02 Pricing (multi-line) | `internal/commercial/line_pricing.go`, `pricing_p2.go`: rules of other lines on P1's pricing rules with P1 versioning; their day types in P2's `commercial.line_day_types` (`pricing_rules.line_day_type_id`) | Back Office `commercial/master` | `TestP2PricingRateCards` | ✅ |
| EP-03 Billing extension | `internal/billing/lines.go`, `p2_api.go` (contracts C1–C3: line folios and charges with revenue components, member charge across lines, tenders, deferred revenue, payouts); statement per line (FR-BIL-P2-02) and accounting export (FR-INT-P2-03) in `finance.go` | Member App Fees & Requests | `TestP2MemberStatement`, `TestP2VoucherPrepaid`, `TestP2MemberApp`, `TestP2SelfServiceCoverage` | ✅ |
| EP-04 Membership lifecycle | `internal/membership/p2_lifecycle.go`, `p2_api.go` on P1's applications (pause, suspension, cancel, upgrade/downgrade, family & nominees, annual fees, cards; `MembershipDetail` shows the lifecycle state) | Back Office `membership/lifecycle`, Member App Fees & Requests, Website application | `TestP2MembershipLifecycle`, `TestP2SelfServiceCoverage` | ✅ |
| EP-05 Caddy lifecycle | `internal/golf/experience/caddy.go` on P1's caddies and assignments (levels, clock-in/out, rotation, promotion approval, incidents, settlements with replacement shares, ratings, favourites) | Ops `caddy/incidents`, Back Office `golf/operations` | `TestP2GolfRound`, `TestP2GolfOperationsCoverage` | ✅ |
| EP-06 Caddy Tablet | `internal/golf/experience/tablet.go`, `rounds.go` (round = P1 flight; tee-off and finish through P1's starter; offline sync `golf.round`, handover, on-course F&B, preferences, earnings) | `apps/caddy` PWA | `TestP2GolfRound`, `TestP2GolfOperationsCoverage`; Playwright Caddy Tablet | ✅ |
| EP-07 Golf Cart lifecycle | `internal/golf/experience/cart.go` (checklists, inspections, readiness through P1, maintenance, service alerts, GPS ingest, replacement); manual Ready only after a passed inspection (`golf.ReadyGuard`, contract C8) | Ops `golf-staff/inspection` | `TestP2GolfRound`, `TestP2GolfOperationsCoverage`, `TestP1GolfDayOperation` | ✅ |
| EP-08 Scorecard & handicap | `internal/golf/experience/scoring.go` (WHS differential into P2's `golf.handicap_indexes`, correction audit, official handicap, statistics; guest rounds on a customer profile, FR-SCR-10) | Member App `golf/scores` | `TestP2GolfRound`, `TestP2GolfOperationsCoverage` | ✅ |
| EP-09 Playing experience | `internal/golf/experience/tablet.go` pace of play (job every minute, realtime on P1's golf stream), hole assets + GPS distances (`course-maps`) | Ops `starter/pace`, Caddy Tablet | `TestP2GolfPaceAndRange` | ✅ / 🟡 GPS accuracy field test |
| EP-10 Hole-in-One | `internal/golf/experience/hio.go` (draft from card, insured from P1's all-in `hio` component, manual, verification, claim PDF) | Back Office `golf/operations` | `TestP2GolfRound`, `TestP2GolfOperationsCoverage` | ✅ |
| EP-11 Hall of Fame | Entries, opt-in consent, curation, kiosk + public API | Website Hall of Fame, Ops Clubhouse Screen, Member App | `TestP2GolfRound`, `TestP2GolfOperationsCoverage` | ✅ |
| EP-12 Driving Range | `internal/golf/experience/range.go` (bays, queue, buckets, prepaid / complimentary, bridge dispenser) | Ops `driving-range` | `TestP2GolfPaceAndRange`, `TestP2GolfOperationsCoverage` | ✅ |
| EP-13 Reciprocal Club | `internal/golf/experience/reciprocal.go` (agreements, verification, quota, visit linked to P1's reciprocal player, settlement, introduction letters) | Website Reciprocal Clubs, Member App | `TestP2GolfReciprocal`, `TestP2GolfOperationsCoverage` | ✅ |
| EP-14 Sport Club & Facility | `internal/sportclub` (facilities, courts, entry access, court booking) | Back Office `sport-club`, Ops `sport-reception`, Website, Member App | `TestP2SportClubEntryAccess`, `TestP2Website`, `TestP2SelfServiceCoverage` | ✅ |
| EP-15 Classes & Training | Programs, schedules, sessions, enrollments, packages, instructor fees | Ops `instructor`, Member App | `TestP2Classes`, `TestP2SelfServiceCoverage` | ✅ |
| EP-16 Bungalow | `internal/stay` (types, units, rate plans, stays, check-in/out folio) | Ops `stay-desk`, Website, Member App | `TestP2StayAndVenue`, `TestP2Website` | ✅ |
| EP-17 VIP Suite | Block + overtime, website request | Ops `stay-desk`, Website | `TestP2StayAndVenue`, `TestP2Website` | ✅ |
| EP-18 Meeting Room | Layout capacity, packages, equipment, catering on KDS | Website Meeting | `TestP2StayAndVenue` | ✅ |
| EP-19 Voucher & Prepaid | `internal/commercial/voucher` (row lock + idempotency, breakage, liability ledger, policies) | Back Office `commercial/operations`, Member App Vouchers | `TestP2VoucherPrepaid`; k6 `voucher-redeem.js` | ✅ / 🟡 load run on Staging |
| EP-20 POS | `internal/commercial/pos` (orders, bills, shifts, Z report, offline sync `commercial.pos_order`) | Ops `pos` (offline queue) | `TestP2POSKitchenBOM`, `TestP2SelfServiceCoverage`; k6 `pos-peak.js` | ✅ / 🟡 load run on Staging |
| EP-21 F&B experience | KDS realtime, on-course / pre-order, member app order food | Ops `kitchen`, Member App `order-food` | `TestP2POSKitchenBOM`, `TestP2MemberApp` | ✅ |
| EP-22 BOM / Recipe | `internal/inventory` recipes, UOM conversions, theoretical food cost | Back Office `inventory` | `TestP2POSKitchenBOM`, `TestP2ResourceDefinitionsCRUD` | ✅ |
| EP-23 Preferences | `internal/crm/foundation.go`, `p2_api.go` (health categories restricted, also in P1's Customer 360; caddy / staff / member sources) | Member App `preferences`, Caddy Tablet | `TestP2CRM`, `TestP2SelfServiceCoverage` | ✅ |
| EP-24 CRM foundation | Customer 360 across lines, interactions, segments, feedback, campaigns (opt-in) | Back Office `crm/engagement`, `crm/customers/:id` | `TestP2CRM` | ✅ / ⏳ link from P1's Customer 360 (contract review) |
| EP-25 Member & Guest App | `/api/v1/member/*` across modules | `apps/member` PWA on P1's portal (scores, sport club, stay, vouchers, fees & requests, order food, preferences) | `TestP2MemberApp`, `TestP2SelfServiceCoverage`, `TestP2GolfOperationsCoverage`; Playwright Member App | ✅ |
| EP-26 Website & non-member booking | `/api/v1/public/*` (`crm.PublicWrite`: honeypot, rate limit, dedup) | `apps/web` (Next.js; P2 helpers in `app/lib-p2.ts`) | `TestP2Website`, `TestP2SelfServiceCoverage`; Playwright Website | ✅ |
| EP-27 Operational interfaces | Navigation trees (additive), SSE with `propertyId` query fallback; events named after their topic | `apps/ops` P2 tiles (Driving Range, Sport Reception, Instructor, Stay Front Desk, POS, Kitchen) next to P1's golf tiles | `TestRolePermissionMatrix`; Playwright Ops | ✅ |
| EP-28 Club Policies | `rules.RegisterPolicy`, typed validation, catalogue endpoint | Back Office Club Policies | `TestP2ClubPolicies` | ✅ |
| EP-29 KPI, Dashboard & Reports | `internal/reporting/p2_reports.go`, `p2_dashboards.go` | Back Office `dashboards/*`, Management Sport Club / Commercial Performance | `TestP2ReportsAndDashboards`; Playwright Management | ✅ |
| EP-30 Integrations | Payment gateway (mock adapter + webhook), bridge commands (dispenser), GPS ingest, notifications | — | `TestP2MemberApp`, `TestP2GolfOperationsCoverage`, `TestBridgeAgent` | ✅ / 🟡 real vendor adapters |
| EP-31 Migration wave 2 | `internal/app/rhapsody` (`p2.go`: wave-2 entities on P1's stage / validate / load / reconcile), `oneclub import rhapsody` | — | `TestP2RhapsodyImport`; runbook | ✅ / 🟡 production data run |
| EP-32 Production readiness | See below | | | |

## EP-32 — Production readiness

| ID | Evidence | Status |
|---|---|---|
| FR-REL-P2-01 Load tests | `test/load/court-rush.js`, `pos-peak.js`, `voucher-redeem.js` (thresholds encode the targets) | 🟡 run against Staging |
| FR-REL-P2-02 Playwright E2E | `web/e2e/p2.spec.ts`; offline POS and caddy-tablet round are also proved at API level in `TestP2POSKitchenBOM` / `TestP2GolfRound` | 🟡 runs in CI |
| FR-REL-P2-03 P1 regression | P0, P1 and P2 suites green together on the integrated branch | ✅ |
| FR-REL-P2-04 Pen test | Public endpoints rate-limited + honeypot; voucher codes row-locked; tablet scoped to assigned rounds | 🟡 external pen test |
| FR-REL-P2-05 Field test | — | 🟡 on site |
| FR-REL-P2-06 VPS build | `garble` + no source maps in the VPS release build, `caddy` shell included in `deploy/docker/Dockerfile.web` (`SOURCEMAPS=false`); `deploy/docker/Dockerfile` garbles the API, Caddyfile `DOMAIN_CADDY` | ✅ build config / 🟡 VPS |
| FR-REL-P2-07 Training & hypercare | — | ⏳ club activity |

## Generic master data

Every P2 resource definition (`GET /api/v1/platform/resource-definitions`) can be created, edited and, when unused,
deleted through the generic engine that the Back Office renders its screens from: `TestP2ResourceDefinitionsCRUD`
(P1's resources are covered by the P1 tests).
