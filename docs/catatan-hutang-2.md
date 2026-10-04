# Catatan Hutang 2 — Domain Staff App

Status per 5 Oktober 2026. Branch `work/staff-app` (worktree `../oneclub-staff`), belum di-push. File ini dibuang setelah
semua butir selesai dan PR `develop → main` di-merge.

## Keputusan (5 Oktober 2026)

Staff App tetap **satu aplikasi dan satu build**, tetapi dibuka di empat domain. Domain mengunci area dan cara login;
role (ACL) tetap menentukan apa yang boleh dibuka. Polanya *host-based routing* di atas satu SPA, bukan micro-frontend.
Sudah dicatat di Tech Doc §6.1 (juga §2, §3.1, §6.2, §9.1, §10.2), roadmap §4.6 dan §66.4, Product Overview §45,
Naming Convention dan header PRD P3–P5. Dokumen itu ada di `docs/product/` dan PRD di root `docs/`; keduanya
diabaikan git (lihat tugas 3.2).

| Domain | Surface | Area | Login | Setelah login |
|---|---|---|---|---|
| `dashboard.<club>` | `dashboard` | Back Office, Management Dashboard, Platform Administration, Clubhouse Screen | Email + password (+ MFA) | Management → Back Office → Platform Administration; role **Screen** langsung ke Clubhouse Screen |
| `cashier.<club>` | `cashier` | Operational (POS, Front Desk, Starter, Caddy Master, Golf Staff, Driving Range, Sport Reception, Stay Desk) | PIN di perangkat terdaftar (password tetap ada) | Operational |
| `caddy.<club>` | `caddy` | Caddy Tablet | PIN di tablet terdaftar | Caddy Tablet |
| `kitchen.<club>` | `kitchen` | Kitchen Display (KDS), layar penuh | PIN di perangkat terdaftar | KDS |

Alasan: landing tidak boleh langsung ke tablet. Dengan urutan landing lama (Tablet → Ops → Management → Back Office →
Platform), role dengan semua permission (Super Admin, Platform Admin, Property Admin) mendarat di `/tablet` dan Golf Manager
di `/ops`. Management cukup dibatasi lewat menu/permission, tidak perlu domain sendiri.

## Yang harus dikerjakan

### A. Frontend (`web/apps/staff`, `@oneclub/shell`)

- [ ] **Surface dari server.** Satu build dipakai banyak instance club, jadi surface tidak boleh ditebak dari nama domain.
  Caddy menyajikan `/surface.json` per domain (`{"surface":"cashier"}`); app membacanya saat start dan menyimpannya
  untuk offline (precache / runtime cache service worker). Tanpa file itu (development, test) app berperilaku seperti
  sekarang: semua area lewat path.
- [ ] **Registry area** (`area-list.ts`): tambah kolom surface per area; tambah area **Clubhouse Screen** (`/screen`) dan
  area **Kitchen Display** (KDS sekarang halaman `/ops/kitchen` di `ops/p2.tsx`).
- [ ] **Landing per surface**: `dashboard` → Management, Back Office, Platform Administration, Screen; `cashier` → Operational;
  `caddy` → Caddy Tablet; `kitchen` → KDS. Area perangkat tidak pernah jadi landing di `dashboard`.
- [ ] **Kunci area**: di `cashier`/`caddy`/`kitchen` hanya area surface itu yang terbuka, tanpa area switcher; path area lain
  menampilkan 403 dengan tautan ke domain yang benar bila diketahui.
- [ ] **Login per surface**: `dashboard` email + password (+ MFA); surface perangkat membuka PIN bila perangkat terdaftar
  (`/login/device` tetap ada). Device token tersimpan per domain.
- [ ] **Clubhouse Screen** pindah dari Operational ke `/screen`: layar penuh tanpa header/menu, auto-refresh.
- [ ] **KDS layar penuh** di `kitchen`: tanpa bottom nav Operational.
- [ ] **Manifest PWA** per domain (nama dan `start_url`): `start_url: "/"` cukup bila landing per surface benar; cek nama
  aplikasi yang tampil saat di-install di tablet.

### B. Backend

- [ ] **Role template Screen** (catalog) dengan permission baru untuk Clubhouse Screen (nama permission mengikuti Naming
  Convention, mis. `golf.hall_of_fame.display`) dan shell code baru untuk `GET /auth/me` → `shells`. Permission ini tidak
  memberi akses Back Office.
- [ ] **Surface custom domain** (`platform.domains`): migration `platform/00009_staff_surface.sql` belum dirilis, jadi
  ganti nilai `staff` dengan `dashboard`, `cashier`, `caddy`, `kitchen` (nilai lama tetap diterima, expand-only).
  `make openapi`, cek oasdiff.
- [ ] **Custom domain untuk surface staf**: blok `https://` on-demand di Caddyfile sekarang selalu ke `web:3000`. Perlu
  cara menyajikan Staff App + `surface.json` yang tepat untuk custom domain staf (mis. endpoint publik yang memberi surface
  per hostname, dipakai app saat `surface.json` tidak ada). Putuskan saat mengerjakan.
- [ ] **Navigasi server**: Clubhouse Screen keluar dari tree `ops`; tree untuk area Screen dan Kitchen bila perlu.
- [ ] **Default `ALLOWED_ORIGINS`** tetap port dev (5173, 5174, 3000); di server diisi keempat domain staf + member + web.

### C. Deploy & CI

- [ ] `deploy/compose/app/Caddyfile`: snippet `(staff)` dengan argumen surface (root `/srv/staff`, `sw.js` no-cache,
  `respond /surface.json`); empat blok `DOMAIN_DASHBOARD`, `DOMAIN_CASHIER`, `DOMAIN_CADDY`, `DOMAIN_KITCHEN`.
  `geolocation=(self)` cukup di `caddy`.
- [ ] `compose.yaml`: ganti `DOMAIN_STAFF` dengan empat variabel di atas. `smoke-test.sh`: `DOMAIN_DASHBOARD`.
- [ ] `PUBLIC_BASE_URL` menunjuk ke `dashboard` (tautan email tetap path Back Office di root).
- [ ] Image tetap satu (`oneclub-static`, build Staff App sekali). Tidak ada container baru.
- [ ] `staging.yml`: `STAGING_STAFF_URL` → `STAGING_DASHBOARD_URL` (+ `STAGING_CASHIER_URL`, `STAGING_CADDY_URL`,
  `STAGING_KITCHEN_URL` bila test Staging menguji penguncian domain).
- [ ] Browser test lokal/CI: tetap satu port untuk `dashboard`; penguncian surface diuji dengan preview kedua yang
  menyajikan `surface.json` lain (mis. middleware kecil di `vite.config.ts` yang membaca env `SURFACE`), atau dengan
  routing di test. Putuskan saat mengerjakan, jaga `browser-stack.sh` tetap sederhana.

### D. Test

- [ ] `dashboard`: GM mendarat di Management; Super Admin/Platform Admin/Property Admin tidak pernah ke `/tablet` atau `/ops`;
  role Screen langsung ke `/screen` tanpa menu.
- [ ] `cashier`: starter/cashier PIN → Operational; path Back Office ditolak.
- [ ] `caddy`: caddy PIN → Caddy Tablet; ronde 18 hole offline tetap lulus (`offline.spec.ts`).
- [ ] `kitchen`: kitchen staff → KDS layar penuh; area lain ditolak.
- [ ] Go e2e: role template Screen, surface custom domain baru, `TestCustomDomain`.
- [ ] Semua yang ada sekarang tetap hijau (Go e2e dengan coverage wajib, 20 test Playwright, oasdiff).

### E. Dokumen

- [ ] Runbook: `local-development.md` (surface di dev), `instance-provisioning.md` (empat domain + DNS),
  `deploy-and-rollback.md` (catatan upgrade: `DOMAIN_BACKOFFICE` → `DOMAIN_DASHBOARD`, perangkat registrasi ulang di
  domain perangkatnya), `ci-cd.md` (variabel Staging), `production-readiness.md` (uptime probe per domain).
- [ ] README, `docs/p0-traceability.md`, `p2-traceability.md` (Clubhouse Screen, KDS, Caddy Tablet per domain).
- [ ] `docs/tugas.md` diperbarui; file ini dihapus setelah selesai.

## Sisa di luar kode

- **1.1** branch protection `main` (matikan *Require approvals*) butuh akun admin `textedoh`; GitHub CLI di mesin ini
  login sebagai `dianprasetyo1290-ai` (push, bukan admin).
- **1.2 / 1.4 / 1.5** merge PR #1, #2 dan `develop → main` dengan merge commit, setelah 1.1. Lalu 1.6: `develop` disamakan
  dengan `main`, `work/staff-app` di-rebase (`git rebase --onto develop sim-base work/staff-app`) dan dibuka sebagai satu PR.
- Variabel/secret GitHub Actions masih kosong: job deploy Dev, Staging, Production dilewati sampai server ada.
- Database uji lokal `oneclub_stafftest` (Postgres Docker) boleh dihapus kapan saja.
