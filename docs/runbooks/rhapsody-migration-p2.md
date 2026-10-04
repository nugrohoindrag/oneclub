# Runbook — Rhapsody migration wave 2 & POS cutover (PRD P2 EP-31)

Wave 2 runs on P1's Rhapsody pipeline (`docs/migration/cutover-runbook.md`, file contract
`docs/migration/rhapsody-mapping.md`): the P2 export files sit in the same export folder and go through the same
steps — **stage → validate → load → reconcile**. Loads write through the P2 modules' public API;
`staging_rhapsody.id_map` remembers what each legacy row became, so every step can be repeated (rows already loaded
are skipped — delta loads at cutover).

## Commands

Run on the App Host of the instance (`docker compose exec api` or the `oneclub` binary with the instance `.env`).

```bash
# 1. stage the CSV files of the export folder (P1 and wave-2 files together)
oneclub import rhapsody stage -dir /data/rhapsody/<date>

# 2. validate: every issue goes to rhapsody-issues.csv, nothing is written
oneclub import rhapsody validate -property MAIN -out /data/rhapsody/<date>/reports

# 3. load in dependency order (P1 entities first, then wave 2)
oneclub import rhapsody load -property MAIN

# 4. reconcile against the club's control totals (metric,value per line)
oneclub import rhapsody reconcile -property MAIN -dir /data/rhapsody/<date> -out /data/rhapsody/<date>/reports
```

`reconcile` reads `<dir>/control_totals.csv` (or `-totals FILE`) and writes `rhapsody-reconciliation.csv` with one
line per metric and its result (MATCH / DIFF). The club signs the report; a DIFF blocks the go decision.

## Wave-2 files

UTF-8 CSV (BOM allowed), one file per entity, header row required. Dates `YYYY-MM-DD`, times RFC 3339, decimals with
`.`. Keys must be stable between dry runs and the cutover. Customers, members, caddies, golf carts, lockers and future
golf bookings come from P1's files (`rhapsody-mapping.md`).

| File | Key | Required columns | Optional columns | Loaded as |
|---|---|---|---|---|
| `outlets.csv` | code | code, name, outlet_type | | POS Outlet (FR-MIG-P2-01) |
| `products.csv` | code | code, name, product_type, price | category, member_price | Product |
| `member_charges.csv` | legacy_id | legacy_id, member_no, amount | description | Outstanding F&B member charge as an opening-balance entry of the member account (FR-MIG-P2-02) |
| `vouchers.csv` | code | code, voucher_type_code, original_quantity, remaining_quantity, price_paid | customer_legacy_id, expires_on | Voucher with its remaining liability (FR-MIG-P2-03) |
| `reservations.csv` | legacy_id | legacy_id, resource_code, start_at, end_at | business_line, customer_legacy_id, guest_name, notes | Confirmed future booking of the Reservation Engine (FR-MIG-P2-05) |
| `enrollments.csv` | legacy_id | legacy_id, program_code, customer_legacy_id, valid_until | | Class participant (no fee charged again) |
| `caddy_profiles.csv` | caddy_code | caddy_code | level_code, joined_on | Level and joined date of a P1 caddy (FR-MIG-P2-06) |
| `hio.csv` | legacy_id | legacy_id, player_name, section_code, hole_number, achieved_on | customer_legacy_id, witnesses (`;`-separated) | Hole-in-One history (Completed) |
| `hall_of_fame.csv` | legacy_id | legacy_id, category, title | division, player_name, achieved_on, description, year, score | Hall of Fame archive (opt-in pending; `club_history` needs none) |

A missing file is skipped; a bad row is reported by `validate` and skipped by `load` without stopping the run.

Master data referenced by code (voucher types, class programs, caddy levels, course sections, reservation resources)
is configured in OneClub **before** the import (Master Data Import P0 for spreadsheets).

## Reconciliation metrics (wave 2)

Added to P1's metrics in `rhapsody-reconciliation.csv`; the club puts the Rhapsody figure of each in
`control_totals.csv`.

| Metric | OneClub figure |
|---|---|
| `outlets`, `products` | Rows loaded |
| `voucher_liability` | Deferred revenue sub-ledger of the migrated vouchers (remaining liability) |
| `future_reservations` | Confirmed imported reservations of the Reservation Engine |
| `enrollments`, `hio`, `hall_of_fame` | Rows loaded |

## POS cutover (FR-MIG-P2-08)

| Step | When | Owner | Done when |
|---|---|---|---|
| Dry run #1 on Staging (stage → reconcile) | T-14 days | Implementation | Issues fixed at the source; reconciliation explained |
| Dry run #2 on Staging | T-7 days | Implementation + Finance | No open issue; every metric MATCH |
| Training & device setup (POS terminals, KDS, printers via bridge agent) | T-7 … T-1 | Outlet Manager | Every cashier signed in with PIN |
| **Freeze** Rhapsody POS: no new member charges / vouchers | T-0 22:00 (after last outlet closes) | Finance | Rhapsody read-only |
| Final extract, `stage` and **delta** `load` (loaded rows are skipped) | T-0 22:30 | Implementation | Load report without failed rows |
| `reconcile` and **sign-off** of `rhapsody-reconciliation.csv` | T-0 23:30 | Finance Manager + GM | Signed report |
| **Go / No-go** | T-0 23:45 | GM | Decision recorded |
| Outlets open on OneClub POS | T+1 06:00 | Outlet Manager | First shift opened |
| Hypercare (2–4 weeks) | T+1 … | Implementation | Daily review of shift reports & voucher liability |

**Rollback** (No-go or blocking issue on T+1 before noon): keep Rhapsody as the system of record, re-open it,
disable the Commercial POS feature for the outlets (Enabled Modules), and void the OneClub transactions taken since
opening (they are re-entered in Rhapsody from the shift report). The migrated master data stays (idempotent re-run
at the next attempt).
