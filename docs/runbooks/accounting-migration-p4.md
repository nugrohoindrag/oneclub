# Accounting cut-over (PRD P4 EP-23 / EP-29, FR-MIG-P4-01/02/04/07, FR-TRS-01…05, §16 #3)

Moves the club's books from **Excel Finance + the P1–P3 Accounting Export** to the
OneClub General Ledger at Release 4 (R4.3 Accounting). Decisions that apply
(PRD P4 §16): go-live on the **first day of a month**; opening balances are the
**closing balances of the previous month** from Excel Finance; one month of
**parallel run** in which OneClub, Excel Finance and the Accounting Export are
compared and every difference is explained (§16 #3, FR-TRS-03); the Accounting
Export stops only at the **sign-off** (FR-TRS-04).

Related runbooks: `procurement-migration-p4.md` (suppliers, price lists, open
POs), `inventory-migration-p4.md` (cut-over opname and opening stock),
`backup-restore.md`, `production-readiness.md` (Release 4).

## Roles

| Role | Who | Does |
|---|---|---|
| Finance Manager (club) | Club Finance | approves the CoA mapping, posts the opening balances, signs off |
| Accountant (club) | Club Finance | prepares the Excel files, runs the reconciliations, explains differences |
| Auditor (optional) | External auditor | read-only access for the parallel-run month (role **Auditor**, *Access until* = end of the audit period; every read is in the audit log) |
| OneClub delivery lead | OneClub | runs the dry runs, checks posting exceptions, go/no-go chair |

## Tools (Staff App → Accounting)

| Step | Screen | API |
|---|---|---|
| Chart of accounts & book | Setup → Book & Sign-off | `POST /api/v1/accounting/book:load-template` `{cutOverDate}` |
| Excel Finance mapping | Setup → Excel Finance Mapping | `/api/v1/accounting/account-mappings` (source code → account) |
| Opening balances (GL + open AR/AP items) | Setup → Opening Balances → Import | `POST /api/v1/accounting/opening-balances:import` `{content, description, balanceDate?, preview}` then `:post` |
| Sub-ledger openings (open invoices, deferred revenue, deposits, caddy fees) | Setup → Opening Balances | `POST /api/v1/accounting/opening-balances` `{includeSubledgers: true}` |
| Opening reconciliation | Opening batch → *Reconciliation with the sub-ledgers* | `GET /api/v1/accounting/opening-balances/{id}/reconciliation` |
| Daily posting check | Closing → Reconciliations | `GET /posting-reconciliation?date=`, `GET /reconciliation?asOf=` |
| Parallel-run TB comparison | Closing → **Excel TB Comparison** | `POST /api/v1/accounting/reconciliations:excel-trial-balance` |
| Sign-off (stops the Accounting Export) | Setup → Book & Sign-off | `POST /api/v1/accounting/book:sign-off` `{note}` |

Opening balance CSV: `account,debit,credit,partner_type,partner,document_no,document_date,due_date,description`
(`account` = OneClub code or the Excel Finance code of the mapping; AR / AP open items carry the partner and the
document). Excel TB CSV: `account,debit,credit` or `account,balance` (closing balances of the month).

## Existing environments: default posting rules after an upgrade

Instances whose book was opened before this release (Staging, demo and trial instances) do not have the posting
rules added since. After deploying the release, run once per instance:

```
POST /api/v1/accounting/posting-rules:generate-defaults      (permission accounting.posting_rule.create)
```

or Staff App → Accounting → Posting Rules → *Generate default rules*. The call is idempotent: it inserts only the
default rules whose code is missing — e.g. `DEF-INV-OPENING` (opening stock → opening balance equity instead of
the P&L), `DEF-COMMISSION` (sales commission accrual), `DEF-ASSET-DISP-*` (asset disposal) — and never changes an
existing or edited rule; the response reports how many were created. Then check the Posting Exceptions tab:
events that went to suspense for lack of these rules can be re-posted (*Repost*).

## Timeline (go-live on day **D** = 1st of month M)

| When | Step | Evidence |
|---|---|---|
| D − 6 weeks | CoA template loaded on Staging, Excel Finance mapping agreed with the auditor's format (FR-FIN-06) | mapping list signed by the Finance Manager |
| D − 4 weeks | **Dry run 1** on Staging with the closing balances of M − 2 (all steps below, no sign-off) | dry-run log, issues list |
| D − 2 weeks | **Dry run 2** with the closing balances of M − 1 (preliminary) — must pass the go/no-go criteria | dry-run log; zero open blocker |
| D − 1 day | Production: book opened with `cutOverDate = D` (documents before D are not posted: they are in the opening balances); posting rules generated; posting exceptions empty | Setup screen |
| D | Go-live: OneClub posts every billing / inventory / procurement event from D (K1–K10); the Accounting Export still runs every day | Processed Events |
| D + 3…5 working days | Excel Finance closes M − 1; import the **final opening balances** dated D − 1 (`preview` until no issue and balanced; then *Approve & post*); post the sub-ledger openings | opening reconciliation all ✔ |
| Daily during M | Posting reconciliation of the business day (journals = Daily Revenue Report) and control accounts = sub-ledgers; posting exceptions resolved within 1 working day | Closing → Reconciliations |
| End of M | Period M soft-closed; Excel Finance TB of M uploaded to **Excel TB Comparison**; P&L of M compared with Excel; every difference explained in the reconciliation log | comparison result *Trial balances agree* or each difference explained |
| M + 1, week 1 | **Go / no-go for sign-off** (below) | minutes |
| M + 1 | **Sign-off**: Accounting Export stops (old exports stay downloadable); Excel Finance archived read-only | audit log `accounting.sign_off` |

## Go / no-go criteria (sign-off)

1. Opening balances posted, balanced, and the opening reconciliation shows GL = sub-ledgers for AR, AP, deposits,
   deferred revenue and caddy fees.
2. Every business day of M: posting reconciliation OK (or explained), no open posting exception older than 1 day.
3. Excel TB Comparison for M: no unmapped Excel item; every account difference explained (timing, reclassification,
   Excel error) and, where OneClub is wrong, corrected by an adjustment journal in M.
4. P&L of M and Balance Sheet at end of M signed by the Finance Manager (and the auditor when engaged).
5. e-Faktur of the month exported / uploaded (Revenue & Tax → Tax Invoices) and the PPN report equals Excel's.
6. AR Aging = billing operational aging; AP Aging = open vendor invoices.

Any criterion failing → **no-go**: the parallel run continues one more month (the Accounting Export is still
running), the cause is fixed, and the go/no-go is repeated.

## Rollback (to Accounting Export)

* **Before sign-off** (the whole parallel-run month) rollback needs no data change: Excel Finance with the daily
  Accounting Export stays the system of record. Stop using OneClub figures and keep the book open (postings
  continue in the background and are harmless; disabling the Accounting module only hides its screens and API —
  event posting is not gated by the module switch). The opening batch can be deleted only while it is a draft; a
  posted opening is corrected by reversal — never delete journals.
* **After sign-off** the Accounting Export is stopped by design (FR-TRS-04). There is no API to resume it; a rollback
  then is a change request to the Platform Admin (decision: *proposal* — add a `book:resume-export` action with
  approval and audit, see the report of the P4 gap fixes). Until that exists, sign off only after the go/no-go.
* Data restore (wrong opening import on Production before go-live): restore the pre-cut-over backup
  (`backup-restore.md`) — only before D.

## Checks after go-live (hypercare, one period close)

* Daily: posting exceptions, processed events lag, the reconciliations above.
* Month-end of M + 1: period close checklist (Financial Periods), soft-close → adjustments → close; the Auditor role,
  if used, expires automatically at its *Access until* date.
