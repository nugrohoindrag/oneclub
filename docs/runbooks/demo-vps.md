# Runbook — demo VPS (single VM)

A demo or UAT instance on one small VM (2 vCPU / 2 GB is enough), e.g. `oneclub.web.id`. The app, its database, mail
and file storage run on the same host; client production keeps the database on the DB Host
([instance-provisioning.md](instance-provisioning.md), `deploy/compose/db`).

The VPS only receives the obfuscated images of the `production` branch (Technical Doc §9.4, `vps.yml`); the
repository is never cloned there.

## 1. Host

Ubuntu 24.04, ports 22/80/443 open (cloud security group and `ufw`). As root:

```bash
apt-get install -y docker.io docker-compose-v2
cat > /etc/docker/daemon.json <<'JSON'
{ "log-driver": "json-file", "log-opts": { "max-size": "20m", "max-file": "3" } }
JSON
systemctl restart docker
useradd -m -s /bin/bash deploy && usermod -aG docker deploy
mkdir -p /srv/oneclub/{scripts,compose,instances} && chown -R deploy:deploy /srv/oneclub
```

- Copy `deploy/scripts/*.sh` to `/srv/oneclub/scripts/` and `deploy/compose/app`, `deploy/compose/demo` to
  `/srv/oneclub/compose/`.
- Add the GitHub Actions public key to `/home/deploy/.ssh/authorized_keys` (secret `VPS_SSH_KEY` holds the private
  key).
- Registry: create a **classic** personal access token with only `read:packages`, then on the VPS
  `sudo -u deploy docker login ghcr.io -u <github user>` and paste the token (never share it in chat or commit it).

## 2. DNS

A records to the VPS for `@`, `www`, `dashboard`, `cashier`, `caddy`, `kitchen`, `presence`, `member` (or one
wildcard `*` plus `@`). Caddy issues the certificates on the first deploy.

## 3. Instance directory

`/srv/oneclub/instances/<code>/` (mode 0700, owner `deploy`):

- `compose.override.yaml` — copy of `deploy/compose/demo/compose.override.yaml` (PostgreSQL 18 on the compose
  network only, Mailpit, file storage volume). `deploy.sh` adds it to every compose command.
- `secrets/pg_superuser_password` — `openssl rand -hex 24`; empty `secrets/smtp_password` and
  `secrets/s3_secret_key`.
- `.env.pgbouncer` — `DB_HOST=db`, `DB_USER=postgres`, `DB_PASSWORD=<superuser password>`.
- `.env` — the bundle written by `instance create` (step 4) plus the demo settings:

```dotenv
REGISTRY=ghcr.io/textedoh
ONECLUB_ENV=staging
DOMAIN_DASHBOARD=dashboard.oneclub.web.id
DOMAIN_CASHIER=cashier.oneclub.web.id
DOMAIN_CADDY=caddy.oneclub.web.id
DOMAIN_KITCHEN=kitchen.oneclub.web.id
DOMAIN_PRESENCE=presence.oneclub.web.id
DOMAIN_MEMBER=member.oneclub.web.id
DOMAIN_WEB="oneclub.web.id, www.oneclub.web.id"
ACME_EMAIL=admin@oneclub.web.id
MEMBER_PORTAL_URL=https://member.oneclub.web.id
WEBSITE_URL=https://oneclub.web.id
ALLOWED_ORIGINS=https://dashboard.oneclub.web.id,…,https://oneclub.web.id,https://www.oneclub.web.id
STORAGE_DRIVER=fs
STORAGE_DIR=/data/storage
SMTP_HOST=mailpit
SMTP_PORT=1025
SMTP_FROM="Modern Golf & Country Club <no-reply@oneclub.web.id>"
```

`ONECLUB_ENV=staging` keeps `seed-demo` and the e2e helpers available; values with spaces or `&` stay quoted
because `smoke-test.sh` sources the file. Start the services of the override first:
`docker compose -p oneclub-<code> --env-file .env -f /srv/oneclub/compose/app/compose.yaml -f compose.override.yaml up -d db mailpit storage-init`.

## 4. Provision, demo data, first deploy

With the first `<sha>-vps` images built by `vps.yml`:

1. `instance create` in a container on the compose network (`-admin-url postgres://postgres:<pw>@db:5432/postgres
   -db-host db -bundle-dir /bundle -public-url https://dashboard.oneclub.web.id`), then merge the bundle `.env`
   with the demo settings above (drop the bundle's `ONECLUB_ENV=production` and `STORAGE_DRIVER=s3`) and move the
   bundle secrets into `secrets/`. Compose mounts secret files with their host owner and mode, and the backend runs
   as the distroless `nonroot` user: `chown 65532:65532` and `chmod 400` the app secrets (`database_url`,
   `database_owner_url`, `database_report_url`, `app_secret`, `smtp_password`, `s3_secret_key`), otherwise the
   containers start without `DATABASE_URL`.
2. Enable every module (Platform Administration → Enabled Modules, or `UPDATE platform.modules SET enabled = true`).
3. Demo data with the worker stopped: `seed-demo --trial` (90 days of history, 30 days ahead; demo users with the
   demo password and PIN). The mock payment gateway, WhatsApp and e-mail sandboxes come from `instance create`;
   mock e-Meterai and e-Faktur from the trial dataset.
4. `/srv/oneclub/scripts/deploy.sh <code> <sha>-vps` — pulls, migrates, publishes the static apps, starts Caddy
   (certificates) and runs the smoke test.
5. In GitHub set `VPS_INSTANCE=<code>` and the secrets `VPS_HOST`, `VPS_SSH_KEY`: from then on every merge
   `main → production` deploys automatically. Leave `VPS_RUN_E2E` unset on a public demo (the browser tests create
   accounts with a known password).
