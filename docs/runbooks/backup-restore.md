# Runbook — backup and restore (FR-TEC-11, RPO ≤ 5 min, RTO ≤ 4 h)

## Backups (DB host)

- **WAL archive**: continuous via `archive_command = pgbackrest archive-push`, `archive_timeout = 60` → RPO ≤ 5 minutes.
- **Full backup**: daily (`backup.sh full`), differential at midday, monthly copy to repo 2 kept 12 months.
- **Retention**: 30 days PITR (repo 1), 12 monthly full backups (repo 2). Repositories are encrypted (AES-256) and
  live in object storage at a different location from the database server.
- Config: `deploy/pgbackrest/pgbackrest.conf`, image `deploy/docker/Dockerfile.postgres`, schedule in `deploy/scripts/backup.sh`.

## Restore drill (monthly on Staging)

```bash
deploy/scripts/restore-drill.sh mgcc                                  # latest backup
deploy/scripts/restore-drill.sh mgcc --target-time "2026-10-04 10:00:00+07"   # point in time
```

The script restores into a scratch container, verifies instance code, users, properties, audit entries and the
migration version, and appends the duration to `restore-drills.log`.

## Drill log

| Date | Environment | Method | Data | Duration | Result |
|---|---|---|---|---|---|
| 2026-10-04 | Local dev (Windows, PostgreSQL 18.6) | `pg_dump -Fc` → `pg_restore` into a new database | 17 users, 2 properties, 4 venues, 81 audit entries, migrations platform v4 — identical after restore | dump 0.37 s, restore 1.23 s | ✅ pass |
| _pending_ | Staging | pgBackRest full + WAL, PITR | — | — | to run before P0 exit (needs the DB host) |

The local drill proves the schema, roles, partitions and data restore cleanly; the pgBackRest/PITR drill must be run
on Staging once the DB host exists (Open Question #1 — hosting).
