# Runbook — Rhapsody migration wave 2 & POS cutover (PRD P2 EP-31)

`oneclub import rhapsody` loads the agreed CSV extracts through the module services (Technical Doc §8.2):
**extract → staging (`migration.staging_rows`) → validation → load → reconciliation**. Every legacy id loaded is kept
in `migration.id_map`, so a run can be repeated: rows already loaded are skipped (delta loads at cutover).

## Commands

```bash
# validate everything without writing (transaction rolled back); prints counts, errors and reconciliation
oneclub import rhapsody --scope=all --dir=/data/extract --property=MAIN --dry-run

# load (one scope or all, in dependency order)
oneclub import rhapsody --scope=all --dir=/data/extract --property=MAIN

# club sign-off of the reconciliation (FR-MIG-P2-07); dry runs cannot be signed off
oneclub import sign-off --batch=<batchId> --by="Name, General Manager"
```

Scopes (load order of `all`): `customers`, `pos`, `member_charges`, `vouchers`, `memberships`, `reservations`,
`golf_history`. A missing CSV is skipped; a bad row is reported (`invalid`) without stopping the batch.

## Extract format

UTF-8 CSV (BOM allowed), one file per entity, header row required. Dates `YYYY-MM-DD`, times RFC 3339, decimals with
`.`. `legacy_id` is the Rhapsody / spreadsheet primary key and must be stable between dry runs and the cutover.

| File | Required columns | Optional columns | Loaded as |
|---|---|---|---|
| `customers.csv` | legacy_id, code, name | phone, email, birth_date, gender, customer_type | CRM Customer |
| `outlets.csv` | legacy_id, code, name, outlet_type | | POS Outlet (FR-MIG-P2-01) |
| `products.csv` | legacy_id, code, name, product_type, price | category, member_price | Product |
| `member_charges.csv` | legacy_id, customer_legacy_id, amount | description | Member Account opening balance (FR-MIG-P2-02) |
| `vouchers.csv` | legacy_id, code, voucher_type_code, original_quantity, remaining_quantity, price_paid | customer_legacy_id, expires_on | Voucher with remaining liability (FR-MIG-P2-03) |
| `memberships.csv` | legacy_id, customer_legacy_id, type_code, membership_no, start_date | end_date, card_no, legacy_card_no | Membership + card (FR-MIG-P2-04) |
| `reservations.csv` | legacy_id, resource_code, start_at, end_at | customer_legacy_id, guest_name, business_line, notes | Confirmed future booking (FR-MIG-P2-05) |
| `enrollments.csv` | legacy_id, program_code, customer_legacy_id, valid_until | | Class participant (no fee charged again) |
| `caddies.csv` | legacy_id, code, name | level_code, joined_on, phone, gender | Caddy & level (FR-MIG-P2-06) |
| `hio.csv` | legacy_id, player_name, section_code, hole_number, achieved_on | customer_legacy_id, witnesses (`;`-separated) | Hole-in-One history (Completed) |
| `hall_of_fame.csv` | legacy_id, category, title | year, division, player_name, score, achieved_on, description | Hall of Fame archive (opt-in pending; `club_history` needs none) |

Master data referenced by code (voucher types, membership types, class programs, caddy levels, course sections,
reservation resources) is configured in OneClub **before** the import (Master Data Import P0 for spreadsheets).

## Reconciliation (signed by the club)

| Figure | Source | OneClub |
|---|---|---|
| Outstanding member charge | `member_charges_source` | `member_charges_oneclub` (opening balances) |
| Voucher / prepaid liability | `voucher_liability_source` (remaining × price ÷ original) | `voucher_liability_oneclub` (deferred revenue sub-ledger of migrated vouchers) |
| Active members per program | `active_members_source` | `active_members_oneclub` |
| Future reservations | `future_reservations_source` | `future_reservations_oneclub` |

Any difference blocks the go decision. The batch, its counts and reconciliation stay in `migration.batches`.

## POS cutover (FR-MIG-P2-08)

| Step | When | Owner | Done when |
|---|---|---|---|
| Dry run #1 on Staging | T-14 days | Implementation | Errors fixed at the source; reconciliation explained |
| Dry run #2 on Staging | T-7 days | Implementation + Finance | Zero invalid rows; reconciliation equal |
| Training & device setup (POS terminals, KDS, printers via bridge agent) | T-7 … T-1 | Outlet Manager | Every cashier signed in with PIN |
| **Freeze** Rhapsody POS: no new member charges / vouchers | T-0 22:00 (after last outlet closes) | Finance | Rhapsody read-only |
| Final extract & **delta** load (`--scope=all`, already loaded rows are skipped) | T-0 22:30 | Implementation | Batch `loaded` |
| Reconciliation & **sign-off** (`oneclub import sign-off`) | T-0 23:30 | Finance Manager + GM | Signed batch |
| **Go / No-go** | T-0 23:45 | GM | Decision recorded |
| Outlets open on OneClub POS | T+1 06:00 | Outlet Manager | First shift opened |
| Hypercare (2–4 weeks) | T+1 … | Implementation | Daily review of shift reports & voucher liability |

**Rollback** (No-go or blocking issue on T+1 before noon): keep Rhapsody as the system of record, re-open it,
disable the Commercial POS feature for the outlets (Enabled Modules), and void the OneClub transactions taken since
opening (they are re-entered in Rhapsody from the shift report). The migrated master data stays (idempotent re-run
at the next attempt).
