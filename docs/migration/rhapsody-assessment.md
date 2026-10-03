# Rhapsody → OneClub: inventory and export assessment

PRD P1 EP-18 · FR-MIG-01 (source data and module inventory) · FR-MIG-02 (data export assessment).
Owner: OneClub delivery lead with the Modern Golf IT / Finance contact. Status columns are filled during milestone M1
(PRD §12) and signed off before the first dry run.

## 1. Rhapsody module inventory

Modern Golf runs **Rhapsody Golf** (PT Realta Chakradarma) for golf front office, POS, membership and back office
(PRD P1 §1). The table lists what P1 replaces, what stays in Rhapsody until a later phase, and what is archived.

| Rhapsody module / area | Used by the club | P1 decision | Data migrated | Owner at the club | Status |
|---|---|---|---|---|---|
| Golf reservation / tee sheet | Reservation, Starter | Replaced by OneClub (EP-03/04) | Future bookings from the cutover date | Golf Admin | ☐ confirmed |
| Golf front office (check-in, caddy, buggy) | Front Desk, Caddy Master | Replaced (EP-09/10/11) | Caddy, golf cart, locker masters | Golf Manager | ☐ |
| Membership | Membership office | Replaced (EP-06/07) | Customers, members, family, corporate, cards, validity | Membership Manager | ☐ |
| Member account (signing bill) | Finance | Replaced (EP-12) | Opening balance per member at cutover | Finance Manager | ☐ |
| Golf POS / pro shop / F&B POS | Outlets | **Stays in Rhapsody** (POS is P2) | — | Outlet Manager | ☐ |
| General ledger / AR / AP | Accounting | **Stays** (accounting export FR-INT-P1-04 feeds it) | — | Finance Manager | ☐ |
| Tournament / scoring | Golf Admin | Archive read-only if available (FR-MIG-08) | Export kept as files | Golf Admin | ☐ |
| Online booking (public site) | Website | Replaced by OneClub website (EP-14) — the Rhapsody page showed a database error on 2 Oct 2026 | — | Marketing | ☐ |

## 2. Source data inventory

| Entity | Rhapsody source (table / report) | Approx. volume | Quality notes | OneClub file (`rhapsody-mapping.md`) |
|---|---|---|---|---|
| Customers / guests | Guest profile master | to be counted | Duplicate phone numbers expected; birth date often missing | `customers.csv` |
| Corporate accounts | Company master | | NPWP format varies | `corporate_accounts.csv` |
| Corporate nominees | Company member list | | | `corporate_nominees.csv` |
| Members & family | Member master + family table | | Family linked by principal member no. | `members.csv` |
| Membership types | Member type setup | | Mapped by hand to OneClub type codes | (configured, not imported) |
| Member cards | Card numbers on member master | | Old card numbers stay valid (kept as legacy number) | `members.csv` (`card_number`) |
| Member account balance | Outstanding signing bill report | | Must equal the Finance AR report at cutover | `opening_balances.csv` |
| Future bookings | Tee sheet / reservation list | | Only from cutover date; paid amounts per booking | `future_bookings.csv` |
| Caddies | Caddy master | | | `caddies.csv` |
| Golf carts | Buggy master | | | `golf_carts.csv` |
| Lockers | Locker master | | Area must be male / female | `lockers.csv` |
| Handicap | Member profile field | | Optional | `customers.csv` (`handicap_index`) |
| Booking history, scoring | Reports | | Archive only | files kept in the project folder |

## 3. Export assessment

| Question | Finding | Decision |
|---|---|---|
| Extraction method | Rhapsody report export to Excel/CSV, or read-only DB access granted by Realta | Prefer **CSV report exports** saved as UTF-8; DB access only if Realta agrees in writing |
| Who runs the export | Club IT with Rhapsody admin rights | Same person for dry runs and cutover |
| Format | Excel → save as CSV UTF-8, one file per entity, header row as in `rhapsody-mapping.md` | Column names are fixed; extra columns are rejected by `oneclub import rhapsody stage` |
| Dates | Rhapsody reports print `dd/mm/yyyy` | Accepted as is (ISO also accepted) |
| Amounts | Thousand separators `1,250,000` | Accepted; commas are removed |
| Control totals | From Rhapsody reports on the same day as the export | `control_totals.csv` (`metric,value`) for `reconcile` |
| Gaps | Fields OneClub needs that Rhapsody lacks (consent date, ID number) | Collected later at the counter / in the Member Portal |
| Personal data | Exports contain PII (UU PDP) | Transferred only over the encrypted channel agreed with the club, deleted after sign-off |

## 4. Sign-off

| Role | Name | Date | Signature |
|---|---|---|---|
| Club General Manager | | | |
| Finance Manager | | | |
| OneClub delivery lead | | | |
