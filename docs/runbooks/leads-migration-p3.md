# Runbook — Leads & opportunities migration wave 3 (PRD P3 FR-MIG-P3-04, -05, -06)

Active leads and open opportunities live in the sales spreadsheets of each line (§16 #17 Excel). They are imported
once with their owner, stage and expected value so the pipelines, follow-ups and forecasts start complete; closed
history stays in the spreadsheets.

Screen: Back Office → CRM → **Leads → Import** (permission `crm.lead.import`: Sales Manager, CRM Admin). API:
`POST /api/v1/crm/leads:import` with `{"mode": "preview"|"commit", "csv": "<text>"}`. Rows are idempotent per
`externalRef`: importing the same file again creates nothing new (`existing`).

## Before the dry runs

1. Sales users exist with their e-mail (the `ownerEmail` column) and belong to the sales teams of their lines; Sales
   Policies (assignment mode, SLA, §16 #3) configured.
2. Pipelines and stages per line as decided in §16.1 (seeded by default; stage codes or names are used in the file).
3. Lead sources configured (Instagram, Facebook, TikTok are manual sources, §16 #6).
4. Each sales lead provides the control figures per owner: number of active leads, open opportunities and their
   total expected value.

## File (CSV export of the sales spreadsheet)

| Column | Required | Meaning |
|---|---|---|
| `externalRef` | yes | Stable reference of the row (sheet + row id) |
| `name`, `companyName`, `phone`, `email` | | Contact (duplicates are flagged against existing customers / leads) |
| `source`, `line`, `eventType`, `eventDate`, `pax`, `budget`, `notes` | | Lead details (`source` default *import*) |
| `ownerEmail` | | Owner; empty = assigned by the Sales Policies (no first-response SLA for imported leads) |
| `status`, `createdAt` | | new / contacted / qualified / unqualified; original creation date |
| `opportunityTitle`, `expectedValue`, `stage`, `expectedCloseDate` | | When set, the lead is converted into an opportunity in that open stage |

## Steps

1. **Dry run 1 (Staging)** — *Preview*: rejected rows show the reason (unknown stage, owner or line, bad date); fix
   and preview again.
2. **Dry run 2 (Staging, full copy)** — *Import*; compare per owner the Leads list and the Pipeline board (open
   count, open value, weighted value = Σ value × stage probability) with the control figures; preview again (all rows
   `existing`). The Sales Manager signs.
3. **Freeze** — from the cutover day new inquiries are entered only in OneClub (website forms, WhatsApp, e-mail
   capture and manual leads); the spreadsheets become read-only.
4. **Import (Production)** — preview, then import.
5. **Delta** — rows added to the spreadsheets between the export and the freeze: export again and import (existing
   references are skipped; changed rows are corrected by hand on the lead / opportunity).
6. **Reconcile (FR-MIG-P3-05)** — per owner: active leads, open opportunities and expected value in OneClub = control
   figures; the Sales Pipeline Report and the CRM Performance dashboard show the imported deals.
7. **Go / no-go** — go when every sales lead has signed the figures of the team.

## Rollback

Before go: disqualify the imported leads (Leads → filter source *import* → *Disqualify*, reason "migration rollback")
and lose their opportunities, or restore the pre-import backup (`docs/runbooks/backup-restore.md`). After go the
spreadsheets are no longer the source; corrections are made on the leads and opportunities.
