# Rhapsody → OneClub data mapping and file contract

PRD P1 EP-18 · FR-MIG-03 (data mapping per entity), FR-MIG-04..10. The migration tool is `oneclub import rhapsody`
(`internal/app/rhapsody`). Every file is UTF-8 CSV with a header row; column order is free, names are fixed (case
insensitive). Required columns are **bold**. Files that are absent are skipped. All loads are idempotent: they upsert by
the legacy reference, so dry runs can be repeated on the same database.

| Step | Command | What it does |
|---|---|---|
| Stage | `oneclub import rhapsody stage -dir ./export` | Loads each `<entity>.csv` into `staging_rhapsody.<entity>` (replaces the previous run) |
| Validate | `oneclub import rhapsody validate -property MAIN -out ./reports` | Row rules below + the Master Data Import rules; writes `rhapsody-issues.csv` |
| Load | `oneclub import rhapsody load -property MAIN` | Writes rows **without** issues; rows with issues are skipped and listed |
| Reconcile | `oneclub import rhapsody reconcile -property MAIN -dir ./export -out ./reports` | Compares OneClub totals with `control_totals.csv`; exits non-zero on a mismatch |

Load order: customers → corporate accounts → nominees → members (principals before family) → opening balances → caddies,
golf carts, lockers → future bookings.

## customers.csv → `crm.customers`

| Column | OneClub field | Transformation / rule |
|---|---|---|
| **legacy_id** | `legacy_ref`, `code` = `RH-<legacy_id>` | Unique in the file |
| **name** | `name` | Required |
| gender | `gender` | `M/L/Male/Pria` → male, `F/P/Female/Wanita` → female |
| birth_date | `birth_date` | `dd/mm/yyyy` or ISO |
| email, phone | `email`, `phone` | E-mail format checked; duplicates are allowed (flagged `duplicate_acknowledged`) and merged later in CRM |
| address, city | `address`, `city` | |
| id_number | `id_number` | Masked in the UI |
| handicap_index | `golf.handicaps` (source `import`) | −10 … 54 |

Dedup: the import never merges automatically; the CRM duplicate list (`/crm/customers:duplicates`) is reviewed after the
load and duplicates are merged with an audit trail.

## corporate_accounts.csv → `crm.corporate_accounts`

| Column | OneClub field | Rule |
|---|---|---|
| **legacy_id** | `legacy_ref`, `code` = `RHC-<legacy_id>` | Unique |
| **name** | `name` | |
| npwp, contact_name, email, phone, address | same | |

## corporate_nominees.csv → `crm.corporate_nominees`

| Column | Rule |
|---|---|
| **corporate_legacy_id** | Must exist in `corporate_accounts.csv` |
| **customer_legacy_id** | Must exist in `customers.csv` |
| title | Free text |

## members.csv → `membership.members`, `membership.memberships`, `membership.cards`

| Column | OneClub field | Rule |
|---|---|---|
| **member_no** | `members.code` (Member No. kept), `legacy_ref` | Unique |
| **customer_legacy_id** | `members.customer_id` | Must exist in `customers.csv` |
| **type_code** | `memberships.type_id` | OneClub Membership Type code (configure types first; e.g. IND, FAM, CORP) |
| **starts_on** | `starts_on` | Date |
| ends_on | `ends_on` | Required for active memberships; ≥ starts_on |
| **status** | `status` | `A/Active/Aktif` → active, `E/Expired` → expired, `I/Inactive/Suspended` → inactive |
| card_number | `cards.card_number` and `cards.legacy_number` | Physical card keeps working at check-in |
| principal_member_no | `memberships.principal_id`, role family | Empty = principal |
| relationship | `memberships.relationship` | spouse, child, … |
| corporate_legacy_id | `memberships.corporate_account_id`, role nominee | |

Portal access is not created by the import; members activate themselves (`/membership` page of the website) or the
membership office sends the invitation.

## opening_balances.csv → `billing.account_entries` (`opening_balance`)

| Column | Rule |
|---|---|
| **member_no** | Must exist in `members.csv` |
| **balance** | Outstanding signing bill (positive = member owes); `1,250,000` accepted |
| **as_of** | Cutover date |

Re-running replaces the opening balance entry of the account (one entry per account).

## caddies.csv, golf_carts.csv, lockers.csv → golf masters

| File | Columns | Rule |
|---|---|---|
| caddies | **code**, **name**, gender, phone, partnership_status | Code = caddy number |
| golf_carts | **code**, **name**, cart_type, capacity | Default electric, 2 seats |
| lockers | **code**, **name**, **area**, zone | Area male/female (`M`/`F` accepted) |

These go through the generic Master Data Import, so its validation applies (code format, enums).

## future_bookings.csv → `golf.bookings` (channel `import`)

| Column | OneClub field | Rule |
|---|---|---|
| **legacy_ref** | `legacy_ref` | Unique; a booking already imported is skipped |
| **course_code** | `course_id` | OneClub course code (e.g. MGC) |
| **play_date** | `play_date` | From the cutover date only (history is archived) |
| **tee_time** | tee time `HH:MM` | Must exist in the OneClub tee sheet for that day (templates must match Rhapsody) |
| start_tee | `start_tee` | 1 (default) or 10 |
| member_no | first player = member | Others become guests of the member |
| **contact_name**, contact_phone | contact | |
| **players** | number of players | 1–4 |
| player_names | names separated by `;` | Missing names become TBA guests |
| paid_amount | payment `bank_transfer`, reference `RHAPSODY-<legacy_ref>` | Amount already paid in Rhapsody |
| notes | `notes` | |

Prices are re-priced with the OneClub rate card (pricing snapshot); the paid amount is recorded as a payment so the folio
shows any balance still due.

## control_totals.csv (reconcile)

`metric,value` per line, taken from Rhapsody reports on the export day:

| Metric | Meaning |
|---|---|
| customers, corporate_accounts, members, caddies, golf_carts, lockers | Record counts |
| `active_members:<TYPE>` | Active memberships per OneClub type code |
| member_account_balance | Total outstanding of member accounts |
| future_bookings | Open future bookings |

The report `rhapsody-reconciliation.csv` (metric, rhapsody, oneclub, MATCH/MISMATCH) is signed by the club (FR-MIG-11).
