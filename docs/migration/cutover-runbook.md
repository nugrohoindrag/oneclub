# Rhapsody cutover runbook — dry runs, parallel run, go-live

PRD P1 EP-18 · FR-MIG-10..14, Exit Criteria #5–6. Commands run on the App Host of the instance (`docker compose exec api`
or the `oneclub` binary with the instance `.env`). File contract: `rhapsody-mapping.md`.

## 0. Prerequisites

- Instance provisioned (`docs/runbooks/instance-provisioning.md`), modules Golf, Membership, Booking, CRM, Billing enabled.
- Course structure, tee sheet templates (identical tee times to Rhapsody), day types, rate card and tax configured.
- Membership programs / types / packages configured with the codes used in `members.csv`.
- Payment methods, approval workflows, Club Policies saved (Golf, Cancellation, Refund, Guest, Member, Caddy, Golf Cart, Weather).
- Backup taken: `deploy/scripts/backup.sh full`.

## 1. Dry run (at least twice on Staging, FR-MIG-12)

| # | Step | Command / action | Evidence |
|---|---|---|---|
| 1 | Restore last production backup to Staging or use a fresh staging instance | `deploy/scripts/restore-drill.sh` | log |
| 2 | Club exports Rhapsody files + control totals | `rhapsody-assessment.md` §3 | export folder (encrypted) |
| 3 | Stage | `oneclub import rhapsody stage -dir /data/rhapsody/<date>` | row counts |
| 4 | Validate | `oneclub import rhapsody validate -property MAIN -out /data/rhapsody/<date>/reports` | `rhapsody-issues.csv` |
| 5 | Fix data in Rhapsody or the files; repeat 3–4 until only accepted issues remain | club + OneClub | issue list signed |
| 6 | Load | `oneclub import rhapsody load -property MAIN` | inserted/updated/skipped per entity |
| 7 | Reconcile | `oneclub import rhapsody reconcile -property MAIN -dir … -out …` | `rhapsody-reconciliation.csv` all MATCH |
| 8 | Spot checks | 20 members: Customer 360, card scan at Check-in, member account balance, future booking on the tee sheet | checklist |
| 9 | Run load again (idempotency) | step 6 → counts updated, nothing inserted twice | report |
| 10 | Time the run | duration of steps 3–7 | used for the cutover window |

Dry run 1 is a rehearsal with a partial export; dry run 2 uses the full production export of the week before cutover.

## 2. Parallel run (FR-MIG-13)

Duration agreed with the club (PRD Open Question). During the parallel run both systems are used:

| Daily | Who | Check |
|---|---|---|
| Bookings of the next 7 days entered in both | Reservation | Daily Tee Sheet Report (OneClub) vs Rhapsody tee sheet: same players and tee times |
| Payments | Cashier | Daily Payment Report vs Rhapsody cashier report: same totals per method |
| Member charges | Finance | Outstanding Member Charge Report vs Rhapsody AR |
| Findings | Delivery lead | Logged with severity; **no critical finding open** to proceed (Exit #6) |

## 3. Cutover (FR-MIG-14)

| Time (T = go-live day 05:00) | Step | Owner |
|---|---|---|
| T−1 18:00 | Announce freeze; no new bookings in Rhapsody after 20:00 | GM |
| T−1 20:00 | **Freeze Rhapsody** (booking and membership entry stopped; POS continues) | Club IT |
| T−1 20:15 | Final export of all files + control totals | Club IT |
| T−1 20:45 | Backup OneClub production: `backup.sh full` | OneClub ops |
| T−1 21:00 | stage → validate → load → reconcile (production, `-property MAIN`) | OneClub ops |
| T−1 22:30 | Final reconciliation signed by Finance Manager and GM (100% active members, member balance, future bookings — Exit #5) | Club |
| T−1 23:00 | **Go / no-go** call (criteria below) | GM + delivery lead |
| T 04:30 | Generate the tee sheet window, attendance for caddies; ops tablets logged in per shift | Golf Admin |
| T 05:00 | First tee time on OneClub; Rhapsody golf reservation stays read-only | Starter |
| T+1 … T+28 | Hypercare (`docs/runbooks/production-readiness.md` §7) | OneClub |

Go criteria: reconciliation all MATCH (or accepted differences documented), no blocking issue in validation, payment gateway
live and webhook test passed, staff trained, backup of the cutover state taken.

## 4. Rollback plan

Decide before T 07:00 (first two hours of play). Rollback means the club returns to Rhapsody, which was only frozen:

1. Announce rollback; staff use Rhapsody again (unfreeze bookings).
2. Bookings made in OneClub between T 05:00 and the rollback are exported from **Golf Booking Report** and re-entered in Rhapsody.
3. Payments taken in OneClub are listed with the **Daily Payment Report** and posted in Rhapsody by Finance.
4. OneClub stays available read-only for investigation; the next attempt repeats §3 after a new dry run.

## 5. After go-live

- Keep the export files encrypted for 30 days, then delete (UU PDP).
- `DROP SCHEMA staging_rhapsody` is done by the migration `platform/00006` down step after the parallel-run sign-off,
  or keep it empty: `oneclub import rhapsody stage` with an empty folder clears nothing, so truncate the tables manually.
- Archive Rhapsody booking history and scoring exports in the project folder (read-only, FR-MIG-08).
