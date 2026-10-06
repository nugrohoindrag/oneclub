# Runbook — Corporate AR migration wave 3 (PRD P3 FR-MIG-P3-02, -05, -06)

The open receivables of companies (and of individual customers on account) are kept in Rhapsody / the Finance
workbook (§16 #17: Excel). They move to OneClub as **issued invoices on the city ledger**: each legacy invoice becomes
invoice `RH-<legacy number>` with its issue and due date, so ageing, reminders, statements and payments continue in
OneClub. Only the **outstanding** amount is migrated (the opening receivable of the company's AR account); the part
already paid stays history in the legacy system.

Screen: Back Office → Billing → **Invoices → Import Corporate AR** (permission `billing.invoice.import`: Finance
Manager, Property Admin). API: `POST /api/v1/billing/invoices:import` with
`{"mode": "preview"|"commit", "xlsx": "<base64 workbook>" | "csv": "<text>", "expectedTotal": "<control total>"}`.
The import is idempotent per legacy number: a row already imported with the same amount is reported as `existing`, a
row whose amount changed is refused (`changed`) — corrections after the import are made on the invoice (credit note,
write-off), never by re-importing.

## Before the dry runs

1. Corporate accounts exist with the **codes** used in the workbook (CRM → Corporate Accounts, Master Data Import P0
   for many); individual debtors as customers with their customer code. Credit limits and terms per Credit Policies
   (§16 #13: 30 days, Rp50 jt).
2. Billing → AR accounts are created automatically per company on import (no manual step).
3. Finance prepares the **control total**: the open corporate AR of the legacy aged-debtor report at the export date,
   and the per-company totals.

## File (Excel first sheet or CSV)

Header row, any column order. Dates `YYYY-MM-DD` or `DD/MM/YYYY`; amounts with `.` as decimal separator (an `Rp`
prefix is accepted).

| Column | Required | Meaning |
|---|---|---|
| `legacyNumber` | yes | Legacy invoice number (unique; becomes `RH-<number>`) |
| `corporateCode` | one of | Corporate account code of the company billed |
| `customerCode` | one of | Customer code (individual on account) when there is no company |
| `issueDate` | | Legacy issue date (default: import day) |
| `dueDate` | | Legacy due date (default: issue date); past due dates are imported **Overdue** |
| `outstanding` | yes | Open amount at the export date (> 0) |
| `originalTotal` | | Original invoice total (kept in the notes; not below the outstanding) |
| `description` | | Invoice line text (default "Rhapsody invoice <number>") |

## Steps

1. **Dry run 1 (Staging)** — *Preview* with the control total: every rejected row shows the row, field and reason
   (unknown company, missing amount, duplicate number, bad date); nothing is saved. Fix the workbook or the master
   data and preview again.
2. **Dry run 2 (Staging, full copy)** — *Import* on Staging, then preview the same file once more: all rows must be
   `existing`, `reconciled = true`. Finance compares the per-company totals of the result with the legacy aged-debtor
   report and the **AR Aging Report** (Reports → Billing) and signs the reconciliation.
3. **Freeze** — from the cutover day no new invoices or receipts are posted in the legacy system; payments received
   are recorded in OneClub after the import.
4. **Import (Production)** — export the workbook at the freeze, *Preview*, then *Import* with the signed control total.
5. **Delta** — invoices issued in the legacy system between the export and the freeze: export them in a second file
   and import (earlier rows are `existing`). Payments received in the legacy system before the freeze but after the
   export are applied in OneClub as payments on the migrated invoice (Invoices → invoice → *Record payment*).
6. **Reconcile (FR-MIG-P3-05)** — the import result shows:
   - `fileTotal` (open amount of the valid rows) = `expectedTotal` (control total) — the file is complete;
   - `migratedOpen` (open balance of all `RH-…` invoices in OneClub) = control total, `difference = 0`;
   - `companies`: invoices and open amount per company, compared with the legacy per-company totals;
   - `reconciled = true` only when no row was rejected and both checks hold.
   Finance and the club sign the reconciliation; a difference blocks the go decision.
7. **Go / no-go** — go when the reconciliation is signed and the AR Aging Report of OneClub matches the legacy aged
   debtors (same buckets ± rounding).

## Rollback

Before go: void the migrated invoices (Invoices → filter `RH-` → *Void*, reason "migration rollback") or restore the
pre-import database backup (`docs/runbooks/backup-restore.md`), then repeat from step 4. After go the legacy AR is no
longer the source: corrections are credit notes / write-offs on the OneClub invoices.
