# P1 traceability — Golf Core MVP (Release 1)

Requirement → implementation → evidence for PRD P1. Status: ✅ implemented and verified by an automated test ·
🟡 implemented, verification needs staging / club data / an external party · ⏳ open.

Tests: Go acceptance tests on real PostgreSQL in `test/e2e/p1_*_test.go` (plus the P0 suite), unit tests next to the code,
browser test `web/e2e/golf.spec.ts`, load tests `test/load/*.js`.

## Epics

| Epic | Implementation | Evidence | Status |
|---|---|---|---|
| EP-01 Customer & Customer 360 | `internal/crm` (profile, guest upgrade, duplicates, merge, relationships, corporate & nominees, preferences, PDP export/erase, `/customers/{id}/overview`, `/history`); Back Office CRM pages | `TestP1CustomerProfiles` | ✅ |
| EP-02 Golf structure & course | `golf.course_sections`, `holes`, `tee_sets`, `playing_routes` (derived holes), `course_assets`, `course_blocks`, `handicaps`; Golf → Course | `TestP1CourseStructure`, `TestP1BookingChanges` (block) | ✅ |
| EP-03 Tee sheet | Templates (versioned), generation job, holidays from the platform calendar, SSE stream | `TestP1TeeSheetTemplates` (weekday / weekend / PH slots of the Modern Golf templates), `TestP1TeeSheetRealtime` (< 2 s) | ✅ |
| EP-04 Booking | Holds on `reservation.allocations` (EXCLUDE), booking types and channels, payment policy, reschedule, cancel (fee), no-show, players, rain check redemption | `TestP1ParallelHoldsAndExpiry` (200 parallel holds → 1), `TestP1BookingChanges`, `TestP1GolfDayOperation` | ✅ |
| EP-05 Flight & player | Flights per slot, split / move players, TBA players, guest of member host rule | `TestP1BookingChanges` | ✅ |
| EP-06 Membership lifecycle | Programs, types (eligibility), packages, application → eligibility → approval → fee → activation, cards, renewal, history, expiry jobs | `TestP1MembershipLifecycle` (child 22 fails), `TestP1MembershipConfiguration`, `internal/membership/eligibility_test.go` | ✅ |
| EP-07 Member entitlement in booking | Member standing at play date, booking window per type, guest limit, Member Rate segment | `TestP1MemberPortal`, `TestP1GolfDayOperation` | ✅ |
| EP-08 Pricing foundation | `internal/commercial/pricing.go`: segments, day types, time bands, routes, channels, all-in components, deterministic resolution, immutable snapshots | `TestP1RateCard` (640k / 995k / 2.96M / 1.96M / 1.05M / 740k), `TestP1PricingConfiguration`, commercial unit tests | ✅ |
| EP-09 Caddy | Attendance, queue / rotation, auto & manual assignment, replace, cancel, caddy fee as liability, tips | `TestP1GolfDayOperation`, `TestP1BookingChanges` | ✅ |
| EP-10 Golf cart | Readiness board, sharing rule, auto assignment of Ready carts without overlap, surcharge, return | `TestP1GolfDayOperation`, `TestP1BookingChanges` | ✅ |
| EP-11 Check-in & starter | Check-in by card / QR / code / name with payment-before-check-in, readiness, starter queue (call, hold, release, skip, tee-off, finish), course status / weather stop, lockers, bag drop & storage; offline check-in & bag drop via sync | `TestP1GolfDayOperation`, `TestP1BookingChanges`, sync idempotency (P0 `TestOfflineSync`) | ✅ |
| EP-12 Billing & payment | Folios, lines, void, close / reopen, payments (cash, card, transfer, member charge, gateway), deposits, refunds with Refund Policy approval, member accounts & statements, reconciliation, accounting export, receipts | `TestP1WebsiteBooking` (webhook ×3 → one payment), `TestP1MemberPortal` (statement), `TestP1BillingAndPayment` | 🟡 `TestP1BillingAndPayment` refund assertion to update (auto-refund ≤ IDR 2,000,000 by policy) |
| EP-13 Member Portal | OTP login, Book Golf, bookings (reschedule / cancel), my flights / caddy / golf cart, membership, digital card QR (offline copy), transactions, statements, profile, application | `TestP1MemberPortal`, `web/e2e/golf.spec.ts` | ✅ / 🟡 browser run on staging |
| EP-14 Website | Golf pages (course, guide, hole-by-hole, handicap, facilities), rates, Book Golf with hold + online payment, manage link (cancel), membership self-activation | `TestP1WebsiteBooking`, `web/e2e/golf.spec.ts` | ✅ / 🟡 browser run on staging |
| EP-15 Club Policies | Golf, Guest, Cancellation, Weather, Caddy, Golf Cart, Payment, Eligibility, Override, Refund, Member policies (versioned, defaults) | used by every golf test; `GET /api/v1/golf/policies` | ✅ |
| EP-16 Reporting & dashboard | Read models `reporting/00003`; 13 P1 reports; `GET /reporting/dashboards/golf-today`; Executive Overview golf KPIs; Management Golf / Membership / Booking Performance | `TestP1GolfDayOperation` (Today's Players = Daily Tee Sheet Report), `TestReporting` | ✅ |
| EP-17 Integrations | Xendit, WhatsApp Cloud API (templates + delivery status), Turnstile CAPTCHA, resident data hook, accounting export | `TestWebhooks`, `TestP1WebsiteBooking`, notification tests | ✅ / 🟡 live vendor keys |
| EP-18 Rhapsody migration | `oneclub import rhapsody stage|validate|load|reconcile` (`internal/app/rhapsody`), staging schema `platform/00006`; docs `docs/migration/*` | `TestP1RhapsodyMigration` (row errors, idempotent re-load, reconciliation MATCH, legacy card at check-in) | ✅ tool / 🟡 dry runs, parallel run and cutover with club data |
| EP-19 Production readiness | `docs/runbooks/production-readiness.md`, `deploy/scripts/healthwatch.sh`, `test/load/*.js`, Playwright `golf.spec.ts`, garble + no source maps build | — | 🟡 executed on production hosting / by external parties |

## Acceptance criteria highlights

| Criterion | Evidence |
|---|---|
| Two bookings can never take the same last seat | `TestP1ParallelHoldsAndExpiry` — 200 concurrent holds, exactly one 201 |
| Expired hold frees the seat | `TestP1ParallelHoldsAndExpiry` |
| Modern Golf templates generate the right slots for weekday / weekend / public holiday | `TestP1TeeSheetTemplates` |
| Rate card resolves to the published prices | `TestP1RateCard` |
| Payment webhook delivered 3× settles one payment and confirms the booking | `TestP1WebsiteBooking` |
| Cancellation inside the free window has no fee; no-show applies the policy | `TestP1BookingChanges` |
| Family application with a 22-year-old child fails eligibility | `TestP1MembershipLifecycle`, `TestEvaluateFamily` |
| Caddy fee recorded as liability, not revenue | `TestP1GolfDayOperation` (Golf Revenue Report) |
| Rain stop issues rain checks with the Weather Policy credit | `TestP1GolfDayOperation` (50% after 9 holes) |
| Today's Players equals the Daily Tee Sheet Report | `TestP1GolfDayOperation` |
| Tee sheet change reaches the screen in < 2 s | `TestP1TeeSheetRealtime` |
| Offline check-in synchronises without duplicates | sync idempotency by item id + check-in of an already checked-in player is a no-op; `test/load/checkin-peak.js` repeats check-ins |
| Every mutating route audited | e2e harness audit check (264 mutating routes) |

## Open items

- `TestP1BillingAndPayment`: change the refund assertion to the Refund Policy (≤ IDR 2,000,000 completes at once, larger
  refunds wait for approval); it covers the last 14 billing routes of the audit-coverage check.
- Dry runs ×2, parallel run, cutover and sign-offs with Modern Golf data (EP-18).
- HA failover and restore drills, external penetration test, load test on staging, hypercare (EP-19).
- PRD Open Questions (club decisions): membership types eligible for the Member Rate, all-in component amounts, parallel-run length.
