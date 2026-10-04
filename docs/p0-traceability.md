# P0 traceability — requirement → implementation → evidence

Status: ✅ implemented and verified by an automated test · 🟡 implemented, not executable on the dev machine yet
(needs Docker / a VPS / GitHub) · ⚠ implemented with a deliberate deviation · ⏳ open.

Test locations: `test/e2e/*_test.go` (Go acceptance tests on real PostgreSQL), `web/e2e/shells.spec.ts` and `offline.spec.ts` (Playwright,
Chrome), unit tests next to the code.

## Exit criteria (PRD §12.1)

| # | Criterion | Evidence | Status |
|---|---|---|---|
| 1 | Customer instance can be created and isolated | `oneclub instance create`; `TestInstanceIsolation` (cross-instance DB connection refused, foreign session 401, data separate) | ✅ |
| 2 | Users have roles and access boundaries | `TestRolePermissionMatrix` (46 role templates × every permissioned route: 403 outside, never 403 inside), `TestPropertyAdminBoundaries` | ✅ |
| 3 | Core master data manageable | Back Office pages + `TestOrganizationStructure`, `TestVenueCourseDepartmentEmployee`, `TestPaymentMethods`, `TestTaxServiceEffectiveDate`, `TestFoundationEntities`, `TestImportThousandCustomers` | ✅ |
| 4 | Audit strategy for all transactions | Route middleware + e2e harness: **116/116 mutating routes exercised, 0 succeeded without an audit entry**; append-only proved in `provision_test` | ✅ |
| 5 | API contract available | `api/openapi/openapi.json` (209 operations, validated); `GET /api/v1/openapi.json`; CI drift + oasdiff breaking check | ✅ (⚠ see FR-TEC-02) |
| 6 | Multi-property boundary | RLS on every `property_id` table (`TestRLSOnEveryPropertyTable`), property switcher (Playwright), cross-property tests | ✅ |
| 7 | Every surface logs in and shows role-appropriate menus | Playwright: Staff App areas Back Office (GM, Super Admin+MFA), Management, Platform Administration, Operational (device+PIN), Caddy Tablet; Member App; Website | ✅ |

## Functional requirements

| ID | Implementation | Evidence | Status |
|---|---|---|---|
| FR-INS-01 | `internal/platform/provision/create.go`, `oneclub instance create`, runbook | `provision_test`, e2e harness provisions 2 instances per run | ✅ |
| FR-INS-02 | DB + roles per instance, `CONNECT` revoked from PUBLIC; Compose per instance | `TestInstanceIsolation` | ✅ |
| FR-INS-03 | `platform.instance`, PATCH instance, suspend | `TestSuspendInstance` | ✅ |
| FR-INS-04 | `platform.modules`, gate middleware, navigation filter | `TestDisableModule`, Playwright module toggle | ✅ |
| FR-INS-05 | `platform.feature_flags`, bootstrap exposes client flags | `TestFeatureFlags` | ✅ |
| FR-INS-06 | `platform.domains`, DNS TXT verify, Caddy on-demand TLS ask endpoint | `TestCustomDomain` (resolver stubbed) | ✅ / 🟡 TLS issuance needs VPS |
| FR-INS-07 | `instance.branding` | `TestBrandingAndLocalization` | ✅ |
| FR-INS-08 | Integration configuration per instance | `TestIntegrations` | ✅ |
| FR-ORG-01..06 | `internal/platform/org`, resource engine, property switcher, RLS | `TestOrganizationStructure`, `TestVenueCourseDepartmentEmployee` | ✅ |
| FR-IAM-01 | Users API + page | `TestUserManagement` | ✅ |
| FR-IAM-02 | argon2id, opaque revocable session cookie (HttpOnly, SameSite=Lax, Secure in prod) | `TestSessions`, `TestLockoutAndNoEnumeration` | ✅ |
| FR-IAM-03 | TOTP MFA forced for Platform/Super/Property Admin and finance roles | `TestMFAEnforcedForAdminRoles`, Playwright MFA enrolment | ✅ |
| FR-IAM-04 | Reset e-mail, password policy, lockout after 5 failures (15 min) | `TestPasswordResetFlow`, `TestLockoutAndNoEnumeration` | ✅ |
| FR-IAM-05/06 | `<module>.<object>.<action>` catalogue; 46 role templates from Product Overview §44 | `TestRolePermissionMatrix` | ✅ |
| FR-IAM-07/08 | Per-property assignments, instance vs property scope, escalation guard | `TestPropertyAdminBoundaries` | ✅ |
| FR-IAM-09 | Devices, enrolment token, PIN login confined to device property | `TestDevicePINLogin`, Playwright Operational (device registered at `/login/device`) | ✅ |
| FR-IAM-10 | Session list/revoke (own and admin) | `TestSessions` | ✅ |
| FR-IAM-11 | Single `authz` + route registry; UI only hides | matrix test + `RequirePermission` 403 page (Playwright) | ✅ |
| FR-IAM-12 | Role/permission/assignment changes audited (security category) | audit coverage check | ✅ |
| FR-MD-01/02 | Departments (hierarchy, no cycles), Employees | `TestVenueCourseDepartmentEmployee` | ✅ |
| FR-MD-03 | 8 payment method types, per-property availability | `TestPaymentMethods` | ✅ |
| FR-MD-04/08 | Tax & Service versions by effective date, Nett / Plus-Plus calculator | `TestTaxServiceEffectiveDate`, `internal/commercial/calc_test.go` | ✅ |
| FR-MD-05 | Customer, Guest, Member, Product, Facility, Resource, Outlet, Supplier (schema + CRUD + unique code per property + status) | `TestFoundationEntities` | ✅ |
| FR-MD-06 | CSV import: preview, row errors, idempotent by code, multipart | `TestImportThousandCustomers` (1,000 rows / 10 invalid → 990 + 10 errors; rerun → 990 updated) | ✅ |
| FR-MD-07 | CSV/XLSX export for every master data resource | `TestExport` | ✅ (users list has no export yet) |
| FR-NOT-01 | In-app + e-mail; WhatsApp adapter (mock until BSP) | `TestNotificationsDeliveryAndRetry` | ✅ |
| FR-NOT-02 | Templates per event × channel × language, editable | same | ✅ |
| FR-NOT-03 | River jobs, exponential backoff | same (2 failures → sent on attempt 3) | ✅ |
| FR-NOT-04 | Delivery history Pending/Sent/Failed | `TestFailedJobRetry` | ✅ |
| FR-NOT-05 | Notification center in every app and Staff App area | Playwright | ✅ |
| FR-NOT-06 | Opt-out per non-mandatory category | `TestNotificationsDeliveryAndRetry` | ✅ |
| FR-APR-01..06, 09 | Approval engine, conditions, steps, inbox, notifications, public `Submit` + hook | `TestApprovalTwoStepWorkflow` | ✅ |
| FR-APR-07/08 | Delegation, SLA reminders | `TestApprovalDelegationAndReminder` | ✅ |
| FR-AUD-01/02 | Automatic data + security events with actor, role, property, IP, device | `TestAuditLogs`, coverage check | ✅ |
| FR-AUD-03 | Append-only: grants + trigger (also blocks owner) | `provision_test` | ✅ |
| FR-AUD-04 | Filters + before/after detail | `TestAuditLogs`, Playwright | ✅ |
| FR-AUD-05 | CSV export, itself audited | `TestAuditLogs` | ✅ |
| FR-AUD-06 | Personal data masked without `audit.log.view_sensitive` | `TestAuditLogs` | ✅ |
| FR-AUD-07 | Monthly partitions (`audit.ensure_partitions`, daily job); nothing is ever dropped (≥ 5 years) | migration + job | ✅ (archival policy after 5 y ⏳) |
| FR-INT-01 | Interfaces: Payment, Messaging, Email, Tax Invoice, Resident Data, Hardware | `TestIntegrations` | ✅ |
| FR-INT-02 | Encrypted credentials (AES-GCM), Sandbox/Production, Test Connection | `TestIntegrations` | ✅ |
| FR-INT-03 | Signed, idempotent webhooks | `TestWebhooks` | ✅ |
| FR-INT-04 | Masked integration log, searchable | `TestWebhooks`, `TestIntegrations` | ✅ |
| FR-INT-05 | Mock payment / WhatsApp / e-mail / e-Faktur / resident adapters | `TestIntegrations` | ✅ |
| FR-INT-06 | Scoped, rotatable API keys | `TestAPIKeys` | ✅ |
| FR-INT-07 | Bridge agent registration + heartbeat, `cmd/bridge-agent` | `TestBridgeAgent` | ✅ (commands P2) |
| FR-JOB-01/02 | River + transactional outbox, idempotent subscribers | `TestOutboxSurvivesWorkerRestart`, vertical slice | ✅ |
| FR-JOB-03 | Periodic jobs in instance timezone (`jobs.DailyAt`) | end-of-day, cleanup schedules | ✅ |
| FR-JOB-04 | Background Jobs: Retry, Discard with reason | `TestFailedJobRetry` | ✅ |
| FR-JOB-05 | Alert to Platform Admin (in-app + e-mail) on repeated failures / backlog / outbox lag | `TestFailedJobRetry` | ✅ |
| FR-JOB-06 | Idempotency-Key | `TestIdempotencyKey` | ✅ |
| FR-REP-01 | `reporting` schema, report queries on the replica connection (read-only role, column grants) | `TestVerticalSlice…` (source=replica) | ✅ / 🟡 streaming replica needs DB host |
| FR-REP-02 | Report registry with code, name, module, permission | `TestReporting` | ✅ |
| FR-REP-03 | Async CSV/XLSX export + notification | `TestReporting` | ✅ |
| FR-REP-04 | Management Dashboard → Executive Overview | Playwright | ✅ |
| FR-REP-05 | User Access Report | `TestReporting` | ✅ |
| FR-L10N-01..03 | ID/EN everywhere, instance + property timezone, Intl formatting | `i18n.test.ts`, Playwright language switch, reset e-mail in Indonesian | ✅ |
| FR-BRD-01/02 | Logo, favicon, login photo, colours, names; applied at runtime | `TestBrandingAndLocalization`, `TestUnusedDeletesAndBrandingUpload` | ✅ |
| FR-BRD-03 | 5 presets + custom accent generated server-side with WCAG AA checks | `internal/platform/instance/accent_test.go` | ✅ |
| FR-BRD-04 | Light/Dark per user, instance default | Profile, header toggle | ✅ |
| FR-SH-01 | All apps on Morphic (`packages/ui`) + generated API client | builds | ✅ |
| FR-SH-02/03 | Server-built navigation; property switcher, user menu (with the Staff App area switcher), notifications, language in header | Playwright | ✅ |
| FR-SH-04 | 403 (with links to the user's Staff App areas), 404, error, maintenance pages | Playwright (403) | ✅ |
| FR-SH-05 | Operational area opens offline, action queued, synced when online (one service worker; precache only Operational and Caddy Tablet) | Playwright Operational + `offline.spec.ts` + `offline.test.ts` + `TestOfflineSync` | ✅ |
| FR-SH-06 | Desktop/tablet; member mobile bottom nav | Playwright (390 px) | ✅ |
| FR-SH-07/08/09 | Login per reference, dashboard style, status pills | Playwright + screenshots | ✅ |
| FR-TEC-01 | Modular monolith + arch rules (`internal/archtest`, depguard) | `TestModuleBoundaries` | ✅ |
| FR-TEC-02 | OpenAPI per build, breaking-change check | CI | ⚠ generated from the Go route registry (code-first, single source for authz/audit/spec) instead of oapi-codegen spec-first |
| FR-TEC-03 | Schema per module, goose per module, expand/contract policy | migrations | ✅ |
| FR-TEC-04 | RLS by `property_id` | `TestRLSOnEveryPropertyTable` | ✅ |
| FR-TEC-05 | numeric for money, UUIDv7, timestamptz | schema | ✅ |
| FR-TEC-06 | `reservation.allocations` EXCLUDE constraint | `TestAllocationExcludeConstraint` | ✅ |
| FR-TEC-07 | Dockerfiles + Compose dev / app / db | `deploy/` | 🟡 not run (no Docker on the dev laptop) |
| FR-TEC-08 | Rolling deploy + rollback scripts | `deploy/scripts` | 🟡 needs a VPS |
| FR-TEC-09 | CI/CD workflows | `.github/workflows` | 🟡 runs once pushed to GitHub |
| FR-TEC-10 | JSON logs (PII masked) + health endpoints | — | ⚠ monitoring stack descoped by product decision (2026-10-04) |
| FR-TEC-11 | pgBackRest config, backup + restore-drill scripts | local `pg_dump/pg_restore` drill (docs/runbooks/backup-restore.md) | 🟡 PITR drill on Staging |
| FR-TEC-12 | Secrets via `*_FILE` / Docker secrets; nothing in repo or image | `.gitignore`, bundle | ✅ |

## Other deviations / open items

- **sqlc** (Technical Doc §2) not used: queries are explicit pgx SQL; the generic master data engine builds SQL from typed field definitions.
- **Turborepo** not used: `pnpm -r` is sufficient for now.
- **NFR performance** (p95 < 300 ms) and the **OWASP ASVS L2 security review** are scheduled for the final P0 load/security pass on Staging.
- **Morphic gap #4** (accessibility audit of Morphic Select/overlay/date picker): the apps use native selects and an own focus-trapped dialog; the Morphic components themselves are not audited yet.
- Open Questions in PRD §14 (hosting, e-mail provider, object storage, WhatsApp BSP, member login method, …) remain open; the code keeps them configurable (SMTP or e-mail integration, `STORAGE_DRIVER=fs|s3`, messaging adapter interface).
