# Runbook — Tournament migration wave 3 (PRD P3 FR-MIG-P3-03, -05, -06)

Tournaments are cut over in two parts: the **history** of the last two years (results and Hall of Fame champions,
§16 #17 Excel) is imported, and the tournaments still **to be played** are re-created in OneClub (they are few —
Monthly Medal, Club Championship, 2–4 corporate events a year, §16 #12) because registration, flighting, scoring and
billing must run end to end in OneClub.

Screen: Back Office → Golf → **Tournaments → Import history** (permission `golf.tournament.manage`: Golf Manager,
tournament director). API: `POST /api/v1/golf/tournaments:import` with `{"mode": "preview"|"commit", "csv": "<text>"}`.
Rows are idempotent per `tournamentRef` + player: importing the same file again updates the same results and
creates no duplicate tournaments or champions.

## Before the dry runs

1. Courses and playing routes exist with their **codes**; members with their member numbers; non-member players as
   customers (customer code) where known.
2. Tournament Policies (formats, handicap allowance, local handicap + PGI index, §16 #12) configured.
3. The club prepares the control figures: number of tournaments of the period, result rows, champions per division.

## History file (CSV export of the spreadsheet)

| Column | Required | Meaning |
|---|---|---|
| `tournamentRef`, `tournamentName` | yes | Stable legacy reference and name |
| `startDate`, `endDate` | yes / | `YYYY-MM-DD` |
| `tournamentType` | | club, club_championship, corporate, invitational, sponsor, charity |
| `format`, `category` | | stroke_play / stableford; gross / net / stableford |
| `courseCode`, `division`, `hallOfFameDivision` | | Course; division; men / ladies / senior / junior / open for champions |
| `position`, `positionLabel` | | Final position (1 = champion of the division) |
| `playerName`, `memberNo`, `customerCode` | yes / | Player and its link to a member or customer |
| `score`, `toPar`, `publicConsent` | | Result; consent to show the name publicly (Hall of Fame on the website) |

## Steps

1. **Dry run 1 (Staging)** — *Preview*: rejected rows show the reason (unknown course, bad date, missing player);
   fix the spreadsheet or the master data and preview again.
2. **Dry run 2 (Staging, full copy)** — *Import*; check the tournament list (source *imported*), the results per
   tournament and the **Hall of Fame** against the club's honours board; preview again (no new tournaments). The Golf
   Manager signs the reconciliation of tournaments, result rows and champions.
3. **Freeze** — from the cutover day no new tournament is entered in Rhapsody / the spreadsheet; registrations for
   upcoming tournaments are taken in OneClub only.
4. **Upcoming tournaments** — create each future tournament in OneClub (Tournaments → *Create*: dates & rounds,
   course & route, format, field, fees, sponsors), open registration and re-enter the players already registered
   (Tournament Desk → *Register*, fee marked paid with the legacy receipt reference when already paid). The tee sheet
   block is created when registration opens.
5. **Import (Production)** — preview, then import the history file.
6. **Delta** — results of tournaments played between the export and the go-live: export again and import (same
   references are updated, new ones added).
7. **Reconcile (FR-MIG-P3-05)** — number of imported tournaments and result rows = control figures; champions per
   division = honours board; upcoming tournaments: registered players and paid fees = legacy registration list.
8. **Go / no-go** — go when the reconciliation is signed and every upcoming tournament is Open in OneClub with its
   players.

## Rollback

Before go: restore the pre-import database backup (`docs/runbooks/backup-restore.md`) — the history import has no
business side effects (no folios, no tee sheet blocks), so restoring is safe; upcoming tournaments created in OneClub
are cancelled with reason "migration rollback" (paid fees refunded through Billing). After go, corrections are made on
the tournament results.
