# Runbook — Banquet & Event migration wave 3 (PRD P3 FR-MIG-P3-01, -05, -06)

Future banquets, weddings, MICE and events are kept in the club's **banquet book** (a spreadsheet, not Rhapsody), so
they go through the Master Data Import flow of Banquet & Event: **preview (dry run) → import → reconcile**. The import
is idempotent per banquet book reference (`legacyRef`): rows already imported are reported as `existing` and left
unchanged, so the same file is loaded again for the delta at cutover.

Screen: Back Office → Banquet & Event → **Event Import** (permission `banquet.event.import`, Finance Manager /
Banquet Manager). API: `POST /api/v1/banquet/events:import` (`{"dryRun": true|false, "rows": [...]}`) and
`GET /api/v1/banquet/migration/reconciliation`.

## Before the dry runs

1. Master data in OneClub: venues (codes as in the banquet book), banquet packages (codes), event types, Banquet
   Policies (DP %, final payment H-N, cancellation tiers), corporate accounts (codes) — Master Data Import P0 for
   spreadsheets.
2. Billing payment methods active (`bank_transfer` records the migrated DP).
3. The club prepares the control totals: number of future events, total DP received, total contract value.

## File (CSV / spreadsheet export)

One row per future event. Dates `YYYY-MM-DD`, times `HH:MM`, decimals with `.`.

| Column | Required | Meaning |
|---|---|---|
| `legacyRef` | yes | Banquet book reference (stable between dry runs and cutover) |
| `title`, `customerName` | yes | Event title, customer (found or created by phone / e-mail) |
| `customerPhone`, `customerEmail`, `corporateAccountCode` | | Contact / company billed |
| `date` | yes | Event day (future only) |
| `startTime`, `endTime` | | Default Banquet Policies start time + package hours |
| `eventType` | | WEDDING, MEETING, BIRTHDAY, SOCIAL … (default by package category) |
| `pax`, `packageCode`, `venueCode`, `layout`, `menu` | | Pax, package, venue held, layout, agreed menu (free text) |
| `contractTotal` | yes | Contract value incl. tax & service (posted as one migrated charge) |
| `downPayment`, `downPaymentDate` | | DP received before go-live: recorded as a held deposit, event Definite |
| `balanceDueDate` | | Due date of the balance (default H-N of Banquet Policies) |
| `notes` | | Kept on the event |

## Steps

1. **Dry run 1 (Staging)** — upload with *Preview*: every row shows `imported` or `error` with the reason; nothing is
   saved. Fix the banquet book or the master data; repeat.
2. **Dry run 2 (Staging, full copy)** — import for real on Staging, open *Reconciliation* and compare with the control
   totals; sign-off by Finance and the Banquet Manager.
3. **Freeze** — from the cutover day the banquet book is read-only; new inquiries go into OneClub (CRM Leads).
4. **Import (Production)** — preview, then import. The DP of each row becomes a held deposit on the event folio, the
   payment schedule holds the DP (paid) and the balance with its due date, and the venue is held Definite.
5. **Delta** — bookings or payments received between the export and the go-live: export again, import (existing rows
   are skipped; a changed row is corrected by hand on the event).
6. **Reconcile** — *Reconciliation* shows:
   - `migratedDownPayment` (DP declared in the book) = `depositLiability` (held deposits on the imported event folios):
     `balanced = true`;
   - `importedFutureEvents` / `futureEvents` = number of future events of the control totals;
   - `contractTotal` and `outstandingBalance` (contract − deposits) against the book.
   The club signs the reconciliation; a difference blocks the go decision.
7. **Go / no-go** — go when the reconciliation is signed and the Event Calendar matches the banquet book.

## Rollback

Before go: cancel the imported events (Banquet & Event → event → *Cancel*, reason "migration rollback", fee waived;
the deposits are refunded to the folio and the venues released) or restore the pre-import database backup
(`docs/runbooks/backup-restore.md`). After go the banquet book is no longer the source; corrections are made on the
events.
