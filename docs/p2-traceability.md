# P2 traceability — requirement → implementation → evidence

Status: ✅ implemented and verified by an automated test · 🟡 implemented, verification needs an environment the dev
machine does not have (Staging / VPS / CI browsers / club sign-off) · ⚠ implemented with a deliberate deviation · ⏳ open.

Test locations: `test/e2e/p2_*_test.go` (Go acceptance tests on real PostgreSQL, run together with the whole P0 suite),
`web/e2e/p2.spec.ts` (Playwright, run in CI by `.github/scripts/browser-stack.sh`), `test/load/*.js` (k6), unit tests
next to the code.

Every client is web: website (Next.js), Member App, Ops/POS and Caddy Tablet as installable offline PWAs, Back Office
and Platform Admin as SPAs. Native apps are out of scope for the whole roadmap (Tech Doc open decision #8 resolved).

## Audit & route coverage

The e2e harness fails the run when a mutating route succeeds without an audit entry, or when any mutating route is
never exercised successfully. Current run: **426/426 mutating routes exercised, 0 without audit** (P0 + P2).

## Exit criteria (PRD §13.1)

| # | Criterion | Evidence | Status |
|---|---|---|---|
| 1 | Reservation Engine without double booking / overbooking | `TestP2ReservationEngine` (EXCLUDE + capacity CHECK in the database, all-or-nothing multi-line); k6 `court-rush.js` | ✅ / 🟡 load run on Staging |
| 2 | Golf journey up to playing history, incl. offline | `TestP2GolfRound` (offline tablet queue, re-sync duplicates, device handover, correction workflow), `TestP2GolfPaceAndRange`, `TestP2GolfReciprocal`, `TestP2GolfOperationsCoverage` | ✅ |
| 3 | Rhapsody POS switched off | `TestP2POSKitchenBOM`, `TestP2RhapsodyImport` (staging, reconciliation, sign-off), runbook `docs/runbooks/rhapsody-migration-p2.md` | ✅ code / 🟡 club parity sign-off & cutover |
| 4 | Sport Club, Classes, Stay & Venue operate | `TestP2SportClubEntryAccess`, `TestP2Classes`, `TestP2StayAndVenue`, `TestP2PricingRateCards` | ✅ |
| 5 | Voucher & prepaid accurate | `TestP2VoucherPrepaid` (liability = deferred sub-ledger), `TestP2ReportsAndDashboards` (Voucher Liability Report) | ✅ / 🟡 2 weeks of operation |
| 6 | Complete membership lifecycle | `TestP2MembershipLifecycle` (annual fee job, auto-suspension), `TestP2MemberApp` | ✅ / 🟡 job on Staging |
| 7 | P2 KPIs consistent with reports | `TestP2ReportsAndDashboards` | ✅ |
| 8 | No P1 regression | P0 + P2 suites pass together; P1 runs on its own branch (P1 contracts are interim here) | ⚠ re-run after the P1 merge |
| 9 | UAT approved | Scenarios §9 automated above; club sign-off per release wave | 🟡 |

## Epics

| EP | Implementation | Frontend | Evidence | Status |
|---|---|---|---|---|
| EP-01 Reservation Engine | `internal/reservation` (capacity mode, multi-line, availability, `ConfirmPaid`, notifications) | Back Office Booking Calendar, Website availability | `TestP2ReservationEngine`, `TestP2Website` | ✅ |
| EP-02 Pricing (multi-line) | `internal/commercial` pricing rules, day types, time bands, components, rate plans | Back Office Commercial hub | `TestP2PricingRateCards` | ✅ |
| EP-03 Billing extension | `internal/billing` (member statement, online payments + gateway webhook, `Checkout`, caddy tip component, POS shift in accounting export) | Member App Transactions | `TestP2MemberStatement`, `TestP2MemberApp`, `TestP2SelfServiceCoverage` | ✅ |
| EP-04 Membership lifecycle | `internal/membership` (applications, eligibility, family, pause, fees, renewal, cards, policies) | Back Office Membership hub, Member App Membership, Website application | `TestP2MembershipLifecycle`, `TestP2SelfServiceCoverage` | ✅ |
| EP-05 Caddy lifecycle | `internal/golf/caddy.go` (levels, attendance, rotation, promotion approval, incidents, settlements, ratings, favourites) | Ops Caddy Master, Back Office Golf hub | `TestP2GolfRound`, `TestP2GolfOperationsCoverage` | ✅ |
| EP-06 Caddy Tablet | `internal/golf/tablet.go` (assignments, round info, offline sync `golf.round`, handover, on-course F&B, preferences) | `apps/caddy` PWA | `TestP2GolfRound`, `TestP2GolfOperationsCoverage`; Playwright Caddy Tablet | ✅ |
| EP-07 Golf Cart lifecycle | `internal/golf/cart.go` (checklists, inspections, readiness, maintenance, service alerts, GPS ingest, replacement) | Ops Golf Staff | `TestP2GolfRound`, `TestP2GolfOperationsCoverage` | ✅ |
| EP-08 Scorecard & handicap | `internal/golf/scoring.go` (WHS differential, correction audit, official handicap, statistics) | Member App Golf / Scorecard | `TestP2GolfRound`, `TestP2GolfOperationsCoverage` | ✅ |
| EP-09 Playing experience | Pace of play (job every minute, realtime), hole geo + GPS distances | Ops Starter, Caddy Tablet | `TestP2GolfPaceAndRange` | ✅ / 🟡 GPS accuracy field test |
| EP-10 Hole-in-One | `internal/golf/hio.go` (draft from card, manual, verification, claim PDF) | Back Office Golf hub | `TestP2GolfRound`, `TestP2GolfOperationsCoverage` | ✅ |
| EP-11 Hall of Fame | Entries, opt-in consent, curation, kiosk + public API | Website Hall of Fame, Ops Clubhouse Screen, Member App | `TestP2GolfRound`, `TestP2GolfOperationsCoverage` | ✅ |
| EP-12 Driving Range | `internal/golf/range.go` (bays, queue, buckets, prepaid / complimentary, bridge dispenser) | Ops Driving Range | `TestP2GolfPaceAndRange`, `TestP2GolfOperationsCoverage` | ✅ |
| EP-13 Reciprocal Club | `internal/golf/reciprocal.go` (agreements, verification, quota, settlement, introduction letters) | Website Reciprocal Clubs, Member App | `TestP2GolfReciprocal`, `TestP2GolfOperationsCoverage` | ✅ |
| EP-14 Sport Club & Facility | `internal/sportclub` (facilities, courts, entry access, court booking) | Ops Sport Reception, Website, Member App | `TestP2SportClubEntryAccess`, `TestP2Website`, `TestP2SelfServiceCoverage` | ✅ |
| EP-15 Classes & Training | Programs, schedules, sessions, enrollments, packages, instructor | Ops Instructor, Member App | `TestP2Classes`, `TestP2SelfServiceCoverage` | ✅ |
| EP-16 Bungalow | `internal/stay` (types, units, rate plans, stays, check-in/out folio) | Ops Front Desk, Website, Member App | `TestP2StayAndVenue`, `TestP2Website` | ✅ |
| EP-17 VIP Suite | Block + overtime, website request | Ops Front Desk, Website | `TestP2StayAndVenue`, `TestP2Website` | ✅ |
| EP-18 Meeting Room | Layout capacity, packages, equipment, catering on KDS | Website Meeting | `TestP2StayAndVenue` | ✅ |
| EP-19 Voucher & Prepaid | `internal/commercial` vouchers (row lock + idempotency, breakage, liability ledger, policies) | Member App Vouchers | `TestP2VoucherPrepaid`; k6 `voucher-redeem.js` | ✅ / 🟡 load run on Staging |
| EP-20 POS | Orders, bills, shifts, Z report, offline sync `commercial.pos_order` | Ops POS (offline queue) | `TestP2POSKitchenBOM`, `TestP2SelfServiceCoverage`; k6 `pos-peak.js` | ✅ / 🟡 load run on Staging |
| EP-21 F&B experience | KDS realtime, on-course / pre-order, member app order food | Ops Kitchen, Member App Order Food | `TestP2POSKitchenBOM`, `TestP2MemberApp` | ✅ |
| EP-22 BOM / Recipe | `internal/inventory` recipes, UOM conversions, theoretical food cost | Back Office Inventory hub | `TestP2POSKitchenBOM`, `TestP2ResourceDefinitionsCRUD` | ✅ |
| EP-23 Preferences | `internal/crm/foundation.go` (sensitive categories restricted, caddy / staff / member sources) | Member App Preferences, Caddy Tablet | `TestP2CRM`, `TestP2SelfServiceCoverage` | ✅ |
| EP-24 CRM foundation | Customer 360 providers, interactions, segments, feedback, campaigns (opt-in) | Back Office Customer 360 | `TestP2CRM` | ✅ |
| EP-25 Member & Guest App | `/api/v1/me/*` across modules (`crm.MeRoute`) | `apps/member` PWA (offline digital card) | `TestP2MemberApp`, `TestP2SelfServiceCoverage`, `TestP2GolfOperationsCoverage`; Playwright Member App | ✅ |
| EP-26 Website & non-member booking | `/api/v1/public/*` (`crm.PublicWrite`: honeypot, rate limit, dedup) | `apps/web` (Next.js) | `TestP2Website`, `TestP2SelfServiceCoverage`; Playwright Website | ✅ |
| EP-27 Operational interfaces | Navigation trees, SSE with `propertyId` query fallback | `apps/ops` tiles (Starter, Caddy Master, Golf Staff, Range, Sport Reception, Instructor, Front Desk, POS, Kitchen, Clubhouse Screen) | `TestRolePermissionMatrix`; Playwright Ops | ✅ |
| EP-28 Club Policies | `rules.RegisterPolicy`, typed validation, catalogue endpoint | Back Office Club Policies | `TestP2ClubPolicies` | ✅ |
| EP-29 KPI, Dashboard & Reports | `internal/reporting/p2_reports.go` (18 reports), `p2_dashboards.go` (5 dashboards) | Back Office KPI dashboards | `TestP2ReportsAndDashboards`; Playwright Management | ✅ |
| EP-30 Integrations | Payment gateway (mock adapter + webhook), bridge commands (dispenser), GPS ingest, notifications | — | `TestP2MemberApp`, `TestP2GolfOperationsCoverage`, `TestBridgeAgent` | ✅ / 🟡 real vendor adapters |
| EP-31 Migration wave 2 | `internal/migration` staging, `oneclub import rhapsody` / `sign-off` | — | `TestP2RhapsodyImport`; runbook | ✅ / 🟡 production data run |
| EP-32 Production readiness | See below | | | |

## EP-32 — Production readiness

| ID | Evidence | Status |
|---|---|---|
| FR-REL-P2-01 Load tests | `test/load/court-rush.js`, `pos-peak.js`, `voucher-redeem.js` (thresholds encode the targets) | 🟡 run against Staging |
| FR-REL-P2-02 Playwright E2E | `web/e2e/p2.spec.ts`; offline POS and caddy-tablet round are also proved at API level in `TestP2POSKitchenBOM` / `TestP2GolfRound` | 🟡 runs in CI |
| FR-REL-P2-03 P1 regression | P0 + P2 suites green together | ⚠ re-run after the P1 merge |
| FR-REL-P2-04 Pen test | Public endpoints rate-limited + honeypot; voucher codes row-locked; tablet scoped to assigned rounds | 🟡 external pen test |
| FR-REL-P2-05 Field test | — | 🟡 on site |
| FR-REL-P2-06 VPS build | `garble` + no source maps in the VPS release build, `caddy` shell included in `deploy/docker/Dockerfile.web` (`SOURCEMAPS=false`); `deploy/docker/Dockerfile` garbles the API, Caddyfile `DOMAIN_CADDY` | ✅ build config / 🟡 VPS |
| FR-REL-P2-07 Training & hypercare | — | ⏳ club activity |

## Generic master data

Every P2 resource definition (`GET /api/v1/platform/resource-definitions`) can be created, edited and, when unused,
deleted through the generic engine that the Back Office renders its screens from: `TestP2ResourceDefinitionsCRUD`.
