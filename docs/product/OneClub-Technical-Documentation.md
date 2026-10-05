# OneClub
## Technical Documentation — Architecture, Stack & Deployment

**Version:** 1.0  
**Based on:** Product Overview v3 (§45–48), Product Roadmap (P0 — Platform Foundation)  
**Date:** 3 October 2026

> Dokumen ini adalah acuan teknis untuk tim engineering OneClub. Stack: **Go, React, PostgreSQL, Docker**. Deployment memakai **Docker Compose tanpa Kubernetes**. Bagian yang ditandai *(usulan)* adalah rekomendasi teknis yang belum diputuskan; lihat [bagian 15](#15-keputusan-terbuka).

---

# 1. Ringkasan Teknis

OneClub dibangun sebagai **modular monolith Go** dengan frontend **React + TypeScript** dan **PostgreSQL** sebagai source of truth. Semua komponen dikemas sebagai **Docker image** dan dijalankan dengan **Docker Compose** di VM biasa.

Prinsip:

1. **Satu codebase backend, banyak module.** Domain dipisah tegas di level kode dan schema database, bukan di level network.
2. **PostgreSQL sebagai source of truth.** Transaksi lintas module (booking → folio → payment) berjalan dalam satu database transaction bila memungkinkan.
3. **Operasional sederhana.** Tidak ada orchestrator, service mesh, atau control plane. Deploy = pull image baru + `docker compose up -d`.
4. **Dedicated Customer Instance.** Setiap klien mendapat stack dan database sendiri (sesuai Roadmap P0 §4.1).
5. **Siap dipecah bila perlu.** Module boundary dan event internal dirancang agar module berat (mis. Reporting) bisa diekstrak menjadi service tanpa rewrite.
6. **Offline-first untuk operational surface.** POS dan operational apps tetap jalan saat koneksi putus, lalu sync.
7. **GolfOne sebagai golf core.** Domain golf mengikuti baseline GolfOne; domain lain menjadi shared capability di sekitarnya.

---

# 2. Tech Stack

| Layer | Teknologi | Catatan |
|---|---|---|
| Backend language | Go (versi stable terbaru, dipin di `go.mod`) | Satu binary, beberapa subcommand (`api`, `worker`, `migrate`, `import`) |
| HTTP router | `chi` *(usulan)* | Kompatibel dengan `net/http` standar |
| API contract | REST + OpenAPI 3, spec-first dengan `oapi-codegen` *(usulan)* | Spec menjadi sumber untuk server stub dan client TypeScript |
| Data access | `pgx` + `sqlc` | SQL ditulis eksplisit, type-safe, tanpa ORM |
| Migration | `goose` *(usulan)* | File migration per module |
| Background job | River (queue berbasis PostgreSQL) | Tanpa Redis/RabbitMQ; job ikut transaksi DB |
| Uang | `numeric` di DB, `shopspring/decimal` di Go *(usulan)* | Tidak pernah memakai float |
| Logging | `log/slog` (JSON) | Structured log ke stdout |
| Telemetry | OpenTelemetry SDK | Trace + metric |
| Database | PostgreSQL (major version terbaru yang didukung) | Schema per domain |
| Connection pooling | PgBouncer (transaction mode) | Wajib bila replica `api` > 1 |
| Frontend language | TypeScript | Strict mode |
| Public website | Next.js (React, SSR) | Butuh SEO, ID/EN |
| Staff App (back office, dashboard, operational, caddy tablet, kitchen display, platform admin) | React PWA + Vite | Satu build untuk semua staf, disajikan di domain `dashboard`, `cashier`, `caddy`, `kitchen` (§6.1); offline untuk area POS/operational dan caddy tablet |
| Member app | React PWA + Vite | Read-only cache |
| Frontend data | TanStack Query + client hasil generate dari OpenAPI | |
| UI / Design system | **Morphic Design System** (internal, Material 3-based) + Motion (`motion/react`) | Satu design system untuk semua app; lihat bagian 6.5 |
| Icon & font | Material Symbols Rounded; Roboto Flex / Inter | Mengikuti token Morphic |
| Form & validasi | React Hook Form + Zod *(usulan)* | |
| i18n | i18next *(usulan)* | Bahasa Indonesia & English |
| Offline storage | IndexedDB (Dexie) + Workbox service worker *(usulan)* | |
| Frontend monorepo | pnpm workspaces + Turborepo *(usulan)* | |
| Container | Docker, multi-stage build | Image Go berbasis distroless |
| Runtime orchestration | Docker Compose | **Tanpa Kubernetes** |
| Reverse proxy / TLS | Caddy *(usulan)* | TLS otomatis, load balancing ke replica `api` |
| Object storage | S3-compatible (managed, atau self-hosted) | Dokumen, foto, lampiran, export |
| Observability | Prometheus, Grafana, Loki, Tempo *(usulan)* | Jalan di monitoring host, juga via Compose |
| Backup | pgBackRest ke object storage *(usulan)* | PITR |

---

# 3. Arsitektur Sistem

## 3.1 Gambaran Komponen

```text
┌───────────────────────────────────────────────────────────────────────┐
│                         CLIENT APPLICATIONS                           │
│  Website (Next.js) │ Member App (PWA) │ Staff App (PWA)               │
│  Staff App (satu build, area per role, domain per perangkat):         │
│   dashboard: Back Office, Management, Platform Admin, Clubhouse Screen│
│   cashier: Operational/POS (offline) · caddy: Caddy Tablet (offline)  │
│   kitchen: Kitchen Display                                            │
└───────────────────────────────┬───────────────────────────────────────┘
                                │ HTTPS (REST + SSE)
                                ▼
                    ┌───────────────────────┐
                    │   Caddy (TLS, LB)     │
                    └───────────┬───────────┘
                                │
           ┌────────────────────┼────────────────────┐
           ▼                    ▼                    ▼
   ┌──────────────┐     ┌──────────────┐     ┌──────────────┐
   │ oneclub api  │ ... │ oneclub api  │     │oneclub worker│
   │ (replica 1)  │     │ (replica N)  │     │ (River jobs) │
   └──────┬───────┘     └──────┬───────┘     └──────┬───────┘
          └─────────────┬──────┴────────────────────┘
                        ▼
                 ┌─────────────┐        ┌──────────────────────┐
                 │  PgBouncer  │        │ S3-compatible storage │
                 └──────┬──────┘        └──────────────────────┘
                        ▼
          ┌────────────────────────────┐
          │ PostgreSQL primary         │──streaming──▶ PostgreSQL replica
          │ (schema per domain)        │               (reporting, failover)
          └────────────────────────────┘
                        │
                        ▼ WAL + full backup
                 pgBackRest → object storage

External: Payment Gateway │ WhatsApp Business API │ e-Faktur (Coretax)
          Data residen Modernland │ Hardware (locker, turnstile, ball dispenser)
```

## 3.2 Process Roles

Backend dibangun sebagai **satu binary** `oneclub` dengan beberapa role:

| Role | Command | Tugas | Replica |
|---|---|---|---|
| API | `oneclub api` | REST API, SSE real-time, webhook masuk | 1–N, stateless |
| Worker | `oneclub worker` | River jobs: notifikasi, end-of-day, renewal, reminder pembayaran, outbox dispatch, integrasi keluar | 1–N |
| Migrate | `oneclub migrate up` | Menjalankan migration, lalu exit | One-shot per deploy |
| Import | `oneclub import <source>` | Migrasi data dari Rhapsody | One-shot |

Periodic job (end-of-day, renewal) dijadwalkan lewat River periodic jobs, jadi tidak ada cron terpisah.

## 3.3 Kenapa Modular Monolith, Bukan Microservices

- **Transaksi lintas domain sangat rapat.** Satu tee time melibatkan Reservation, Pricing, Membership, Billing, dan Caddy. Di monolith ini cukup satu DB transaction, tanpa saga.
- **Tim dan ops lebih kecil.** Satu pipeline, satu image, satu set dashboard.
- **Tanpa Kubernetes**, microservices menambah beban service discovery, retry, dan deployment yang tidak sebanding manfaatnya.
- **Jalan keluar tetap ada.** Module berkomunikasi lewat interface publik dan event, sehingga bisa diekstrak bila beban atau tim bertambah.

## 3.4 Real-time

Tee sheet, starter board, caddy assignment, dan kitchen display butuh update real-time.

- Transport: **Server-Sent Events (SSE)** dari `api` ke client.
- Fan-out antar replica: **PostgreSQL `LISTEN/NOTIFY`**. Perubahan di replica mana pun di-broadcast ke semua replica tanpa Redis.
- Payload NOTIFY hanya berisi ID dan tipe event; client mengambil data terbaru lewat REST.

---

# 4. Module Boundaries

## 4.1 Mapping Domain → Module Go → Schema

Mengikuti 15 sistem dalam 5 lapis (Product Overview §3).

| Lapis | Sistem | Module Go | Schema PostgreSQL |
|---|---|---|---|
| Lini bisnis | Golf Operations (GolfOne) | `golf` | `golf` |
| Lini bisnis | Sport Club & Facility | `sportclub` | `sportclub` |
| Lini bisnis | Stay & Venue | `stay` | `stay` |
| Lini bisnis | Banquet, MICE & Wedding | `banquet` | `banquet` |
| Core bersama | Membership | `membership` | `membership` |
| Core bersama | Reservation Engine | `reservation` | `reservation` |
| Core bersama | Commercial (POS + BOM, Pricing & Promotion, Voucher & Prepaid) | `commercial` | `commercial` |
| Core bersama | Billing & Payment | `billing` | `billing` |
| Customer | CRM & Sales (Loyalty, Top Spender) | `crm` | `crm` |
| Back office | Inventory & BOM | `inventory` | `inventory` |
| Back office | Procurement | `procurement` | `procurement` |
| Back office | Accounting | `accounting` | `accounting` |
| Back office | HRIS & Payroll | `hris` | `hris` |
| Fondasi | Management & BI | `reporting` | `reporting` |
| Fondasi | Platform (IAM, master data, notifikasi, integrasi, approval, audit) | `platform` | `platform`, `audit` |
| Kanal publik | Landing Page / CMS | `cms` | `cms` |

## 4.2 Aturan Antar Module

1. **Module lain hanya boleh memanggil interface publik** sebuah module (file `api.go` di root module). Package `internal` milik module tidak boleh di-import module lain.
2. **Tidak ada query lintas schema** dari kode module lain. Kebutuhan baca lintas domain melalui interface publik atau read model di `reporting`.
3. **Dependensi satu arah** mengikuti lapis: Lini bisnis → Core bersama → Back office → Platform. Arah sebaliknya hanya lewat **domain event**.
4. Aturan 1–3 dicek otomatis di CI dengan linter arsitektur (mis. `depguard` / `go-arch-lint`) *(usulan)*.

## 4.3 Domain Event & Outbox

```text
Module A (dalam DB transaction)
  ├── ubah data domain
  └── INSERT ke platform.outbox  ← commit bersamaan
           │
           ▼
River job "outbox-dispatch"
  └── panggil subscriber terdaftar (in-process)
         ├── accounting: buat jurnal
         ├── crm: update Member 360 / loyalty
         ├── reporting: update read model
         └── notification: kirim WhatsApp / email
```

- Event bersifat **at-least-once**; setiap subscriber wajib **idempotent** (dedupe berdasarkan `event_id`).
- Contoh event: `reservation.confirmed`, `billing.payment_settled`, `billing.folio_closed`, `membership.renewed`, `commercial.sale_completed`, `inventory.stock_moved`.
- Bila module diekstrak menjadi service, dispatcher cukup diganti ke message broker tanpa mengubah publisher.

## 4.4 Flow Kritis: Tee Time sampai Jurnal

```text
Booking tee time (reservation)
  → harga dihitung & di-snapshot (commercial.pricing)
  → hak & eligibility dicek (membership)
  → slot dikunci di DB (EXCLUDE constraint)
  → charge masuk folio (billing)
  → pembayaran (billing ↔ payment gateway)
  → event billing.payment_settled
  → jurnal (accounting), loyalty (crm), KPI (reporting)
```

Sesuai prinsip roadmap: **financial evidence harus traceable** dari pricing snapshot sampai payment, refund, adjustment, folio, dan accounting.

---

# 5. Backend (Go)

## 5.1 Struktur Repository

```text
oneclub/
├── cmd/
│   └── oneclub/                 # main.go: subcommand api | worker | migrate | import
├── internal/
│   ├── golf/
│   ├── sportclub/
│   ├── stay/
│   ├── banquet/
│   ├── membership/
│   ├── reservation/
│   ├── commercial/
│   ├── billing/
│   ├── crm/
│   ├── inventory/
│   ├── procurement/
│   ├── accounting/
│   ├── hris/
│   ├── reporting/
│   ├── platform/                # iam, instance, property, masterdata, notification,
│   │                            # approval, audit, integration, outbox
│   ├── cms/
│   └── kernel/                  # shared kernel: money, id, clock, errs, dbtx, httpx, authz
├── api/
│   └── openapi/                 # spec per module, digabung saat build
├── db/
│   └── migrations/              # <module>/NNNN_<nama>.sql
├── deploy/
│   ├── docker/                  # Dockerfile
│   └── compose/                 # compose.yaml per environment
├── web/                         # monorepo frontend (bagian 6)
└── Makefile
```

## 5.2 Struktur di Dalam Module

```text
internal/reservation/
├── api.go            # interface publik untuk module lain
├── events.go         # definisi event yang dipublish
├── module.go         # wiring: register route, job, subscriber
├── http/             # handler hasil generate oapi-codegen + mapping DTO
├── service/          # use case & aturan bisnis
├── store/            # kode hasil generate sqlc
├── queries/          # *.sql untuk sqlc
└── jobs/             # River job milik module
```

Layering: `http` → `service` → `store`. Aturan bisnis hanya di `service`; handler hanya validasi input, authz, dan mapping.

## 5.3 Konvensi

| Topik | Konvensi |
|---|---|
| ID | UUIDv7 (urut waktu, aman di-generate di client offline) |
| Waktu | `timestamptz` di DB, UTC di Go; konversi ke timezone instance hanya di presentasi |
| Uang | `numeric(19,4)` + kolom `currency`; tipe `kernel/money.Money` di Go |
| Error | Error domain bertipe (`kernel/errs`), dipetakan ke HTTP status di satu tempat |
| Context | `property_id`, `user_id`, `request_id` dibawa lewat `context.Context` |
| Transaksi | Helper `dbtx.WithTx(ctx, fn)`; service tidak membuka transaksi sendiri secara manual |
| Konfigurasi | Environment variable, divalidasi saat startup (fail fast) |
| Idempotency | Endpoint yang membuat transaksi keuangan menerima header `Idempotency-Key` |
| Logging | `slog` JSON; data pribadi (NIK, nomor HP, email) di-mask |

## 5.4 Dockerfile Backend (ringkas)

```dockerfile
FROM golang:<versi> AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/oneclub ./cmd/oneclub

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/oneclub /oneclub
USER nonroot
ENTRYPOINT ["/oneclub"]
```

---

# 6. Frontend (React)

## 6.1 Aplikasi

Mengikuti application surfaces di Product Overview §45: **satu platform**, aplikasi dipisah menurut siapa penggunanya, dan akses di dalamnya ditentukan oleh role (ACL).

| App | Pengguna | Teknologi | Offline |
|---|---|---|---|
| `web` — Website + Online Booking | Publik, non-member | Next.js (SSR) | Tidak |
| `member` — Member & Guest App | Member, keluarga, tamu | React PWA | Read-only cache (kartu digital, booking saya) |
| `staff` — Staff App | Semua staf club dan Platform Admin | React PWA | **Ya** untuk area Operational dan Caddy Tablet |

**Area di Staff App.** Satu login, satu sesi, satu build. Area yang terbuka dihitung dari permission user
(`GET /api/v1/auth/me` → `shells`, FR-SH-02); otorisasi tetap di backend.

| Area | Path | Permission | Layout | Offline |
|---|---|---|---|---|
| Back Office | `/` (path module, mis. `/golf/tee-sheet`) | `platform.backoffice.access` | Sidebar | Tidak |
| Management Dashboard | `/management` | `reporting.dashboard.view` | Top pill navigation | Tidak |
| Operational Interface | `/ops` | `platform.ops.access` | Touch, control 44px | **Ya** (POS wajib) |
| Caddy Tablet | `/tablet` | `golf.tablet.use` | Touch, control 44px | **Ya** |
| Platform Administration | `/platform` | `platform.platform_admin.access` | Sidebar | Tidak |
| Clubhouse Screen | `/screen` | `platform.screen.access` (role **Screen**) | Layar penuh, tanpa menu | Tidak |
| Kitchen Display | `/kitchen` | `commercial.kitchen.update` (kasir yang hanya melihat pesanan dapur tidak mendapat area ini) | Lebar penuh, tanpa menu Operational | Tidak |

**Domain Staff App** *(diputuskan 5 Oktober 2026)*. Staff App tetap **satu build**, tetapi disajikan di beberapa
domain. Domain menentukan area yang terbuka dan cara login; permission tetap menentukan apa yang boleh dibuka di dalamnya
(*host-based routing* di atas satu SPA, bukan micro-frontend).

| Domain (contoh) | Surface | Area | Login | Setelah login |
|---|---|---|---|---|
| `dashboard.<club>` | `dashboard` | Back Office, Management Dashboard, Platform Administration, Clubhouse Screen | Email + password (+ MFA) | Management → Back Office → Platform Administration (area pertama yang dimiliki); role **Screen** langsung ke Clubhouse Screen |
| `cashier.<club>` | `cashier` | Operational: POS, Front Desk, Starter, Caddy Master, Golf Staff, Driving Range, Sport Reception, Stay Desk | PIN staf di perangkat terdaftar (password tetap tersedia) | Operational |
| `caddy.<club>` | `caddy` | Caddy Tablet | PIN caddy di tablet terdaftar | Caddy Tablet |
| `kitchen.<club>` | `kitchen` | Kitchen Display (KDS), layar penuh | PIN di perangkat terdaftar | KDS |

Aturan:

- **Domain mengunci area.** Di setiap domain hanya area domain itu yang terbuka; di `cashier`, `caddy` dan `kitchen`
  tidak ada area switcher. Path area domain lain menampilkan 403 dengan tautan ke domain yang benar.
  Di `dashboard`, user dengan lebih dari satu area mendapat **area switcher** di user menu; Management dibatasi lewat menu
  dan permission (`reporting.dashboard.view`), bukan domain sendiri.
- **Landing** di `dashboard` tidak pernah ke area perangkat (Operational, Caddy Tablet, KDS): urutannya Management,
  Back Office, Platform Administration, Clubhouse Screen. Membuka area tanpa permission menampilkan 403 dengan tautan ke
  area yang boleh dibuka.
- **Clubhouse Screen** pindah dari Operational ke `dashboard`: role template **Screen** hanya berhak melihat layar itu dan
  langsung membukanya dalam tampilan penuh tanpa menu (TV/kiosk di clubhouse).
- **Surface diberikan oleh server**, bukan ditebak dari nama domain: Caddy menyajikan `/surface.json` per domain
  (`{"surface":"cashier","domains":{…}}`, berisi juga keempat domain Staff App untuk tautan 403) dan manifest PWA sesuai
  surface (nama aplikasi saat di-install). App membacanya sebelum render pertama dan menyimpannya per domain untuk
  offline. **Custom domain** (FR-INS-06): `GET /api/v1/public/domains/allowed` yang dipakai Caddy untuk TLS on-demand
  juga mengembalikan header `X-Surface`; Caddy (`forward_auth`) menyajikan Staff App dengan surface itu, Member App, atau
  website. Surface lama `backoffice`/`platform-admin` disajikan sebagai `dashboard`, `ops` sebagai `cashier`.
  Di development tanpa `surface.json` semua area dibuka lewat path di satu port; `<surface>.localhost:5173` mencoba
  tiap domain.
- **CSRF.** Setiap domain mem-proxy `/api`, jadi request app selalu same-origin: API menerima mutasi bila `Origin`
  sama dengan host request (termasuk custom domain) atau ada di `ALLOWED_ORIGINS`.
- **Mode perangkat.** Perangkat bersama yang diregistrasi (FR-IAM-09) di `cashier`, `caddy`, `kitchen` memakai login PIN
  staf per shift. Tiap domain punya penyimpanan, registrasi perangkat dan service worker sendiri.
- **Code splitting per area.** Setiap area di-load lazy. Service worker hanya melakukan precache untuk area offline
  (Operational, Caddy Tablet).
- **Path back office tetap di root** domain `dashboard`, sehingga tautan di email dan notifikasi (`PUBLIC_BASE_URL` + path
  module) tidak berubah; `PUBLIC_BASE_URL` menunjuk ke `dashboard`.
- **Build & deploy.** CI membangun Staff App sekali (image `oneclub-static`); Caddy menyajikannya untuk keempat domain
  (`DOMAIN_DASHBOARD`, `DOMAIN_CASHIER`, `DOMAIN_CADDY`, `DOMAIN_KITCHEN`). Tidak ada container tambahan.

## 6.2 Struktur Monorepo

```text
web/
├── apps/
│   ├── web/
│   ├── member/
│   └── staff/           # area: backoffice, management, ops, tablet, platform, screen; domain dashboard/cashier/caddy/kitchen
└── packages/
    ├── api-client/      # hasil generate dari OpenAPI + hooks TanStack Query
    ├── ui/              # Morphic Design System (tokens, components, patterns, templates)
    ├── ui-oneclub/      # pattern & template khusus OneClub di atas Morphic (Tee Sheet, Folio, dst.)
    ├── auth/            # session, guard, RBAC helper
    ├── i18n/            # resource ID/EN
    ├── offline/         # IndexedDB store, sync queue, conflict handling
    └── config/          # tsconfig, eslint
```

## 6.3 Konvensi Frontend

- **Label menu, module, dan action mengikuti `OneClub — Naming Convention.md`.**
- API client **selalu di-generate** dari OpenAPI; tidak ada `fetch` manual ke endpoint.
- Server state di TanStack Query; state lokal UI di komponen. Global store hanya bila benar-benar perlu.
- Routing berbasis module, mis. `/golf/tee-sheet` dan `/billing/folio/:id`.
- Permission UI hanya untuk tampilan; **otorisasi tetap di backend**.
- Komponen UI **hanya dari Morphic** (`packages/ui`) atau pattern OneClub (`packages/ui-oneclub`); warna, spacing, radius, dan typography selalu lewat token, tidak ada nilai mentah.

## 6.4 Offline & Sync (area POS/Operational dan Caddy Tablet di Staff App)

```text
Aksi user (offline)
  → tulis ke IndexedDB + antrean sync (UUIDv7 + Idempotency-Key)
  → UI langsung terupdate (optimistic)
Koneksi kembali
  → kirim antrean berurutan ke /sync endpoint
  → server proses idempotent, kembalikan hasil / konflik
  → client rekonsiliasi & tampilkan konflik yang perlu tindakan
```

Aturan konflik:

| Data | Aturan |
|---|---|
| Penjualan POS | Selalu diterima. Harga memakai price list cache saat transaksi, disimpan sebagai snapshot |
| Stok | Server authoritative. Stok minus dari penjualan offline dicatat sebagai exception, bukan ditolak |
| Assignment caddy / cart | Server menang; client diberi notifikasi bila assignment berubah |
| Skor (caddy tablet) | Last-write-wins per hole, dengan audit log |
| Master data | Read-only di client, refresh berkala |

Data yang di-cache offline dibatasi per property dan per shift. Cache dihapus saat logout.

## 6.5 Design System — Morphic

OneClub memakai **Morphic Design System**, design system internal dari project sebelumnya (sumber: folder `Design System/` di root project).

| Aspek | Morphic |
|---|---|
| Basis | Material 3, gaya "soft, premium, information-dense" |
| Teknologi | React + TypeScript, animasi dengan Motion (`motion/react`), styling lewat CSS custom properties (tanpa Tailwind) |
| Lapisan | Foundation (tokens) → Primitive → Component → Pattern → Template → Application |
| Token | Color, spacing (grid 8px), radius, elevation, typography, density, motion, z-index (`tokens/*.css`, `tokens.ts`) |
| Tema | `data-theme` = light / dark; `data-accent` = lime / blue / violet / orange / rose |
| Icon & font | Material Symbols Rounded; Roboto Flex / Inter dengan tabular numerals untuk angka |
| Aksesibilitas | Target WCAG AA, touch target min. 44px, focus ring, `prefers-reduced-motion` |

Pemakaian di OneClub:

- **Core Morphic tetap generik** di `packages/ui`. Komponen khusus golf club (Tee Sheet, Flight Card, Folio, Caddy Board, dst.) dibangun sebagai pattern/template di `packages/ui-oneclub` di atas token Morphic.
- **Examples industri lain** (manufacturing, logistics) dan template yang tidak relevan (e-commerce) tidak ikut dibawa.
- **Branding per customer instance** (PRD P0 FR-BRD-01) dipetakan ke token: mode light/dark + accent. Instance yang butuh warna di luar 5 preset memerlukan **custom accent** yang di-generate dari satu warna brand dan divalidasi kontrasnya (gap, lihat di bawah).
- **Density** mengikuti konteks: area Back Office dan Platform Administration memakai density standar (control 36px); area Operational dan Caddy Tablet (tablet, layar sentuh) memakai control besar (44px) dan touch target minimal 44px.
- **Next.js (`web`)**: komponen Morphic yang memakai Motion dirender sebagai client component (`'use client'`); halaman publik tetap SSR untuk SEO.

### Referensi Visual

| Referensi | File | Diterapkan ke |
|---|---|---|
| Dashboard | `dashboard-ui.webp` | Back Office, Management Dashboard, Member Portal |
| Login | `login-reference.webp` | Halaman login Staff App dan Member App |

**Dashboard** (`dashboard-ui.webp`):

- Kanvas abu-abu terang dengan **card putih berradius besar**; satu **card gelap (inverse surface)** sebagai sorotan per halaman.
- **Metric card**: judul + ikon dalam lingkaran, angka besar dengan tabular numerals, indikator perubahan berupa pill hijau/merah, filter periode berbentuk pill di pojok kanan atas.
- **Navigasi atas berbentuk pill** (item aktif terisi gelap) + notifikasi dan user menu di kanan.
- **Tabel** dengan search pill, tombol filter, dan **status sebagai pill berwarna** (Complete / Canceled).
- **Chart** dengan satu warna dominan + satu warna pembeda, tooltip gelap.
- Tombol aksi: primary terisi warna accent, secondary gelap, tertiary netral.

Penerapan di OneClub:

| Area | Navigasi | Catatan |
|---|---|---|
| Management Dashboard | Top pill navigation seperti referensi | Executive Overview, Golf Performance, dst. (Naming Convention §23) |
| Back Office | **Sidebar** (collapsed 68px / expanded 196px) + top bar | Module terlalu banyak (Golf s.d. Settings) untuk top navigation; gaya card, tabel, dan status pill tetap mengikuti referensi |
| Member Portal | Top pill navigation di desktop, bottom navigation di mobile | |
| Ops / Caddy | Navigasi sederhana per peran, layout touch-first | Tidak memakai layout dashboard |

Status pill memakai label dan warna semantik yang konsisten dengan Naming Convention §31 (mis. Confirmed / Completed = success, Pending = warning, Cancelled / Rejected = error).

**Login** (`login-reference.webp`):

- **Split layout**: panel foto/visual di kiri (radius besar), form di kanan dengan banyak ruang kosong.
- Judul besar "Log in", **input berbentuk pill** dengan label di atas, toggle tampil/sembunyikan password, link "Forgot Password?".
- **Primary button hitam full-width**.

Penerapan di OneClub:

- Panel kiri menampilkan **foto dan logo club dari Branding instance** (bukan gambar statis).
- **Staff & admin:** email + password → langkah MFA (TOTP) pada halaman yang sama. Tidak ada "Create an Account" maupun social login; akun staff dibuat oleh admin.
- **Member Portal:** boleh menampilkan "Create an Account" / aktivasi member dan checkbox Terms & Condition. Social login (Google) baru dipertimbangkan setelah P1.
- **Ops / Caddy:** layar login device + PIN staff (PRD FR-IAM-09), memakai gaya visual yang sama.
- Di mobile, panel foto disembunyikan atau diperkecil menjadi header.

### Gap yang Perlu Ditutup di P0

| # | Gap | Tindakan |
|---|---|---|
| 1 | Folder `Design System/` berisi source code saja, belum ada `package.json` dan konfigurasi build | Pulihkan menjadi package `packages/ui` di monorepo, pasang build, lint, dan Storybook/showcase |
| 2 | Accent hanya 5 preset | Tambah generator custom accent dari warna brand + validasi kontras WCAG AA |
| 3 | Pattern domain OneClub belum ada | Bangun bertahap di `packages/ui-oneclub` mulai P1 (Tee Sheet, Starter Board, Folio) |
| 4 | Komponen Select, overlay, dan date picker dibuat sendiri | Audit aksesibilitas (keyboard, screen reader, focus trap) sebelum dipakai production |
| 5 | Mode offline untuk komponen form/data | Pastikan komponen tidak bergantung pada request jaringan saat render (area Operational dan Caddy Tablet) |

---

# 7. Data Layer (PostgreSQL)

## 7.1 Isolasi Customer & Property

```text
Customer Instance (klien)  → database terpisah, stack Compose terpisah
  └── Property             → kolom property_id + Row Level Security
        └── Venue / Course / Outlet
```

- **Antar customer:** isolasi fisik (database dan stack sendiri). Tidak ada risiko data bocor lintas klien lewat query.
- **Antar property dalam satu customer:** setiap tabel transaksional memiliki `property_id`. Aplikasi memfilter berdasarkan konteks, dan **Row Level Security** menjadi lapisan pertahanan kedua. Setiap transaksi menjalankan `SET LOCAL app.property_id = '...'`.

## 7.2 Konvensi Schema

| Topik | Aturan |
|---|---|
| Schema | Satu schema per module (bagian 4.1) |
| Primary key | `id uuid` (UUIDv7) |
| Kolom wajib tabel transaksional | `property_id`, `created_at`, `created_by`, `updated_at`, `updated_by` |
| Soft delete | Hanya untuk master data (`archived_at`); transaksi tidak dihapus, tetapi di-void / di-reverse |
| Uang | `numeric(19,4)` + `currency char(3)` |
| Enum | Lookup table bila dikonfigurasi club; `text` + `CHECK` bila tetap |
| Penamaan | `snake_case`, tabel bentuk jamak (`tee_times`, `folios`) |
| Foreign key lintas schema | Diperbolehkan untuk integritas, tetapi kode module tetap tidak meng-query schema lain |

## 7.3 Anti Double-Booking

Semua resource yang dibooking (tee time, court, bungalow, VIP suite, meeting room, venue) dikunci di level database:

```sql
CREATE TABLE reservation.allocations (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL,
  resource_id   uuid NOT NULL,
  reservation_id uuid NOT NULL,
  period        tstzrange NOT NULL,
  status        text NOT NULL,
  EXCLUDE USING gist (
    resource_id WITH =,
    period WITH &&
  ) WHERE (status IN ('held', 'confirmed'))
);
```

Hold sementara (keranjang online booking) memakai status `held` + `expires_at`; River job melepas hold yang kedaluwarsa.

## 7.4 Data Append-Only

- **`accounting.journal_entries` / `journal_lines`**: tidak ada `UPDATE`/`DELETE`. Koreksi dilakukan dengan reversal entry. Ditegakkan dengan trigger dan grant.
- **`audit.audit_log`**: mencatat actor, aksi, entity, before/after (JSONB), IP, dan waktu. Dipartisi per bulan.
- **Pricing snapshot**: setiap charge menyimpan rate rule, segmen, day type, time band, tax & service yang dipakai.

## 7.5 Migration

- Migration per module di `db/migrations/<module>/`, dijalankan oleh `oneclub migrate up` sebagai container one-shot sebelum `api` baru dinyalakan.
- Pola **expand → migrate → contract** supaya versi lama dan baru bisa jalan bersamaan saat deploy:
  1. Tambah kolom/tabel baru (nullable / default)
  2. Deploy kode yang menulis ke dua tempat dan membaca yang baru
  3. Backfill dengan job
  4. Hapus kolom lama di rilis berikutnya
- Migration destruktif wajib di-review dua orang.

## 7.6 Reporting

- Query dashboard dan laporan berat diarahkan ke **read replica**.
- Schema `reporting` berisi read model / materialized view yang di-refresh oleh event dan job.
- Bila volume tumbuh, `reporting` adalah kandidat pertama untuk diekstrak (mis. ke database analitik terpisah).

## 7.7 Backup & Recovery

| Item | Rencana *(usulan)* |
|---|---|
| Full backup | Harian, pgBackRest ke object storage |
| WAL archive | Kontinu, untuk Point-in-Time Recovery |
| Retensi | 30 hari PITR + backup bulanan disimpan 12 bulan |
| Uji restore | Bulanan ke environment staging, hasil dicatat |
| Lokasi | Object storage di lokasi berbeda dari server database |

---

# 8. API & Integrasi

## 8.1 Konvensi REST

| Topik | Konvensi |
|---|---|
| Base path | `/api/v1/<module>/<resource>`, mis. `/api/v1/golf/tee-times` |
| Format | JSON, `camelCase` di payload |
| Versioning | Major version di path; perubahan breaking hanya di versi baru |
| Pagination | Cursor-based (`?cursor=&limit=`) |
| Filter & sort | `?filter[status]=confirmed&sort=-startAt` |
| Error | RFC 9457 Problem Details (`type`, `title`, `status`, `detail`, `errors[]`) |
| Idempotency | Header `Idempotency-Key` untuk POST transaksi keuangan & sync offline |
| Concurrency | `ETag` / `If-Match` untuk update dokumen penting (folio, BEO, PO) |
| Waktu | ISO 8601 dengan offset |
| Dokumentasi | OpenAPI di-publish otomatis per build |

## 8.2 Integrasi Eksternal

Semua integrasi lewat module `platform/integration` dengan pola **adapter**: satu interface per kapabilitas, implementasi per vendor, konfigurasi per customer instance.

| Integrasi | Pola | Catatan |
|---|---|---|
| Payment gateway (QRIS, Virtual Account, kartu) | Adapter + webhook | Verifikasi signature webhook, idempotent, rekonsiliasi harian. Data kartu tidak pernah disimpan (hosted page / tokenisasi) |
| WhatsApp Business API | Adapter via River job | Template message, retry dengan backoff, status delivery disimpan |
| e-Faktur (Coretax) | Adapter / export | Dari module `accounting` |
| Data residen Modernland | Adapter (pull/sync) | Untuk verifikasi tarif residence |
| Hardware (locker, turnstile, ball dispenser) | **Local bridge agent** di jaringan club | Agent kecil (Go) yang terhubung keluar ke API; hardware tidak diekspos ke internet |
| Migrasi Rhapsody | `oneclub import rhapsody` | Extract → staging schema → validasi → load; bisa diulang (idempotent) |

Setiap panggilan keluar dicatat (request, response yang di-mask, durasi, status) untuk troubleshooting dan audit.

---

# 9. Security

## 9.1 Autentikasi

| Pengguna | Metode *(usulan)* |
|---|---|
| Staff & admin (web) | Session token opaque di cookie `HttpOnly`, `Secure`, `SameSite=Lax`; disimpan di DB sehingga bisa dicabut |
| Admin & finance | Wajib MFA (TOTP) |
| Member app (PWA) | Session token + refresh; login dengan nomor HP (OTP WhatsApp) atau email |
| Operational device (`cashier`, `caddy`, `kitchen`: POS, tablet ops, caddy tablet, KDS) | Device registration + PIN staff per shift |
| Staf dengan perangkat pribadi (`dashboard`) | Session token di atas + MFA sesuai role |
| Integrasi server-to-server | API key per integrasi, scope terbatas, bisa di-rotate |
| Enterprise SSO (P6) | OIDC / SAML |

Password di-hash dengan **argon2id**. Login dilindungi rate limit dan lockout.

## 9.2 Otorisasi

- **RBAC** dengan permission granular per module dan action (mis. `golf.tee_time.create`, `billing.folio.void`), mengikuti User Roles di Product Overview §44.
- **Scope property**: role berlaku per property. Super Admin dan Property Admin dibedakan.
- Aksi sensitif (void, refund, diskon di atas batas, adjustment stok) melewati **approval workflow** dan tercatat di audit log.

## 9.3 Perlindungan Data

- TLS di semua koneksi publik; koneksi ke PostgreSQL memakai TLS antar host.
- Data pribadi (NIK, nomor HP, tanggal lahir) mengikuti **UU PDP (UU No. 27/2022)**: akses dibatasi, di-mask di log, dan ada mekanisme ekspor/hapus atas permintaan subjek data.
- Secret tidak disimpan di image maupun repo; dikirim lewat Docker secrets / file env terenkripsi (mis. `sops` + `age`) *(usulan)*.
- Image di-scan (mis. Trivy) di CI; dependency diperbarui terjadwal.
- Security header (CSP, HSTS) di Caddy; CORS hanya untuk origin app OneClub.

## 9.4 Perlindungan Source Code & Kekayaan Intelektual

Tujuan: source code OneClub tidak dapat diambil atau diduplikasi dari artefak yang di-deploy ke customer instance.

| Lapisan | Tindakan | Catatan |
|---|---|---|
| Distribusi | Klien hanya menerima akses aplikasi, **bukan** source code, binary, atau image | Paling efektif bila instance di-hosting OneClub (lihat Keputusan Terbuka #1). Untuk on-premise, image ditarik dari registry privat dengan kredensial per instance yang bisa dicabut |
| Backend (Go) | Binary dikompilasi (bukan source), `-trimpath -ldflags="-s -w"`, lalu **obfuscation dengan `garble`** (`-literals -tiny`) untuk build Staging/Production | Nama package, fungsi, dan string literal diacak; stack trace production di-*reverse* lewat `garble reverse` dengan seed build yang disimpan privat |
| Frontend (React) | Build production di-minify dan di-mangle; **source map tidak ikut di-deploy** (di-upload privat ke error tracker saja) | Kode JavaScript tetap bisa dibaca browser, jadi **logika bisnis dan otorisasi wajib di backend** (sudah menjadi prinsip §6.3) |
| Image | Base image distroless, tanpa shell; image ditandatangani (cosign) dan hanya registry privat | Mencegah penggantian image dan memperkecil permukaan ekstraksi |
| Lisensi | Instance memvalidasi **license key** bertanda tangan (kode instance, masa berlaku, module yang dibeli) saat startup dan berkala | Selaras dengan Enabled Modules (PRD FR-INS-04) dan subscription/feature tier (P6) |
| Hukum | Klausul HKI, larangan reverse engineering, dan kerahasiaan di kontrak klien | Obfuscation hanya menghambat, bukan mencegah; perlindungan hukum tetap diperlukan |

Waktu penerapan (keputusan: **obfuscation hanya saat deploy ke VPS**):

- **Development, CI (PR), dan Dev:** build biasa tanpa obfuscation, agar debugging, stack trace, dan test tetap mudah.
- **Deploy ke VPS (Staging/Production):** pipeline release membangun image terpisah dengan `garble` + frontend tanpa source map. Smoke test dan E2E dijalankan terhadap image hasil obfuscation sebelum dipromosikan. Seed `garble` per build disimpan privat untuk `garble reverse`.
- Kode sejak awal menghindari pola yang rusak oleh obfuscation (mis. bergantung pada nama fungsi/package lewat refleksi).
- **P6:** license key penuh bersama subscription & feature tier.

---

# 10. Infrastruktur & Deployment (Docker, tanpa Kubernetes)

## 10.1 Topologi per Customer Instance

**Baseline** (satu klien, mis. Modern Golf & Country Club):

```text
                 Internet
                    │
            ┌───────▼────────┐
            │  App Host (VM) │  Docker Compose:
            │                │   caddy, api ×2, worker, web (Next.js),
            │                │   static frontend (via caddy), pgbouncer
            └───────┬────────┘
                    │ private network
            ┌───────▼────────┐        ┌────────────────┐
            │  DB Host (VM)  │──────▶ │ DB Replica (VM)│
            │  PostgreSQL    │ stream │ PostgreSQL     │
            │  pgBackRest    │        │ (reporting)    │
            └───────┬────────┘        └────────────────┘
                    │
              Object Storage (backup, file)

Monitoring Host (shared antar customer):
  Prometheus, Grafana, Loki, Tempo, Alertmanager
```

Untuk pilot, App Host dan DB Host boleh digabung di satu VM. Database tetap dipisah sebelum go-live production.

## 10.2 Docker Compose (ringkas)

```yaml
name: oneclub-${INSTANCE}

services:
  caddy:
    image: caddy:<versi>
    ports: ["80:80", "443:443"]
    environment:                    # satu build Staff App, empat domain (§6.1)
      DOMAIN_DASHBOARD: ${DOMAIN_DASHBOARD}
      DOMAIN_CASHIER: ${DOMAIN_CASHIER}
      DOMAIN_CADDY: ${DOMAIN_CADDY}
      DOMAIN_KITCHEN: ${DOMAIN_KITCHEN}
      DOMAIN_MEMBER: ${DOMAIN_MEMBER}
      DOMAIN_WEB: ${DOMAIN_WEB}
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - ./static:/srv:ro            # build SPA/PWA
      - caddy_data:/data
    depends_on: [api]

  api:
    image: registry.example.com/oneclub:${VERSION}
    command: ["api"]
    env_file: .env
    secrets: [db_password, app_secret]
    deploy:
      replicas: 2
    healthcheck:
      test: ["CMD", "/oneclub", "healthcheck"]
      interval: 10s

  worker:
    image: registry.example.com/oneclub:${VERSION}
    command: ["worker"]
    env_file: .env
    secrets: [db_password, app_secret]

  migrate:
    image: registry.example.com/oneclub:${VERSION}
    command: ["migrate", "up"]
    env_file: .env
    secrets: [db_password]
    restart: "no"
    profiles: ["migrate"]

  web:
    image: registry.example.com/oneclub-web:${VERSION}
    env_file: .env.web

  pgbouncer:
    image: <pgbouncer-image>
    env_file: .env.pgbouncer

secrets:
  db_password: { file: ./secrets/db_password }
  app_secret:  { file: ./secrets/app_secret }

volumes:
  caddy_data:
```

PostgreSQL di DB Host dijalankan dengan Compose terpisah, memakai volume di disk khusus dan `shm_size` yang cukup.

## 10.3 Alur Deploy

```text
1. CI build & push image  oneclub:<git-sha>, oneclub-web:<git-sha>
2. SSH ke App Host (dari pipeline)
3. docker compose pull
4. docker compose --profile migrate run --rm migrate      ← migration expand-only
5. Rolling update api:
     naikkan replica baru → tunggu healthcheck → Caddy alihkan traffic
     → matikan replica lama (graceful shutdown, drain koneksi SSE)
6. Restart worker (River menyelesaikan job aktif sebelum berhenti)
7. Smoke test otomatis → selesai / rollback
```

- **Zero-downtime** dicapai dengan rolling update `api` di belakang Caddy (health check aktif + `lb_try_duration`), misalnya memakai `docker rollout` atau skrip deploy sendiri *(usulan)*.
- **Rollback** = deploy ulang tag image sebelumnya. Ini aman karena migration mengikuti pola expand/contract.
- Semua langkah ada di satu skrip `deploy.sh` yang dipanggil pipeline, bukan langkah manual.

## 10.4 Scaling Tanpa Kubernetes

| Tahap | Cara | Kapan |
|---|---|---|
| 1. Vertical | Naikkan CPU/RAM VM | Default awal |
| 2. Replica `api` di host yang sama | `deploy.replicas` di Compose | CPU API tinggi, DB masih longgar |
| 3. Worker terpisah | `worker` dipindah ke VM sendiri | Job berat (end-of-day, report) mengganggu API |
| 4. Multi App Host | 2+ VM menjalankan Compose yang sama; Caddy / load balancer di depan | Butuh redundansi host atau kapasitas lebih |
| 5. Read replica | Report & dashboard ke replica | Query berat mengganggu transaksi |
| 6. Ekstrak module | Mis. `reporting` jadi service sendiri | Hanya bila tahap 1–5 tidak cukup |

Karena `api` dan `worker` stateless (state di PostgreSQL dan object storage), menambah host hanya berarti menjalankan Compose yang sama di VM baru.

## 10.5 High Availability Database

| Opsi | Kelebihan | Kekurangan |
|---|---|---|
| **A. Self-hosted** primary + streaming replica, failover semi-manual dengan runbook | Biaya rendah, kontrol penuh, konsisten dengan Docker | Failover butuh tindakan operator (target < 30 menit) |
| **B. Self-hosted + Patroni** (etcd 3 node) | Failover otomatis | Komponen tambahan yang harus dirawat |
| **C. Managed PostgreSQL** di cloud provider | Backup, failover, dan patching dikelola | Biaya lebih tinggi; perlu cek lokasi data di Indonesia |

Rekomendasi awal *(usulan)*: **Opsi A** untuk pilot, lalu evaluasi B atau C sebelum klien kedua.

---

# 11. Observability

> **Keputusan 4 Oktober 2026:** stack monitoring (Prometheus, Grafana, Loki, Tempo, Alertmanager, OpenTelemetry) **tidak dibangun di P0**. Yang tetap ada: log JSON terstruktur ke stdout (PII di-mask), `/healthz` dan `/readyz` untuk health check deploy, serta alert job gagal berulang lewat notifikasi in-app + email ke Platform Admin (PRD FR-JOB-05). Tabel di bawah menjadi acuan bila monitoring diaktifkan di fase berikutnya.

| Sinyal | Implementasi | Isi utama |
|---|---|---|
| Log | `slog` JSON → stdout → Loki (via Promtail/Alloy) | `request_id`, `property_id`, `user_id`, module, durasi |
| Metric | Endpoint `/metrics` (Prometheus) | Latency & error rate per endpoint, River queue depth & job failure, pool DB, outbox lag |
| Trace | OpenTelemetry → Tempo | Request → service → query → panggilan integrasi |
| Uptime | Probe eksternal ke `/healthz` dan website | Per customer instance |
| Business metric | Dashboard Grafana | Booking/jam, payment gagal, sync offline tertunda, webhook gagal |

Alert minimal *(usulan)*:

- API error rate 5xx > 2% selama 5 menit
- p95 latency booking > 1 detik selama 10 menit
- River job gagal berulang / queue menumpuk
- Outbox lag > 5 menit
- Replication lag replica > 60 detik
- Backup harian atau WAL archive gagal
- Disk DB > 80%
- Sertifikat TLS < 14 hari dari kedaluwarsa

---

# 12. CI/CD & Environment

## 12.1 Environment

| Environment | Tujuan | Data |
|---|---|---|
| Local | Development (`docker compose up` untuk PostgreSQL + tools) | Seed data |
| Dev | Integrasi harian, deploy otomatis dari `main` | Seed data |
| Staging | UAT dengan club, uji migrasi Rhapsody, uji restore | Salinan anonim / data migrasi uji |
| Production | Per customer instance | Data asli |

## 12.2 Pipeline

```text
Pull Request
  ├── Go: lint (golangci-lint), arch-lint, unit test, integration test (PostgreSQL di container)
  ├── sqlc & oapi-codegen: generate ulang → gagal bila ada diff
  ├── OpenAPI: cek breaking change
  ├── Frontend: typecheck, lint, unit test, build
  └── Migration: dijalankan di DB kosong + DB snapshot staging

Merge ke main
  ├── Build image (tag git SHA) → scan → push registry
  └── Deploy otomatis ke Dev

Release (tag vX.Y.Z)
  ├── Deploy ke Staging + E2E test
  └── Deploy ke Production per instance — approval manual
```

Rilis memakai **semantic versioning**. Release note dihasilkan dari commit / PR.

**Implementasi (P0, GitHub Actions — repo `github.com/textedoh/oneclub`):**

| Workflow | Trigger | Isi |
|---|---|---|
| `ci` | Setiap PR, push ke `main` | `lint` (golangci-lint + depguard aturan arsitektur) · `backend` (unit + race, migration di DB kosong, acceptance test API di PostgreSQL 18, OpenAPI drift + breaking change) · `frontend` (drift API client, typecheck, lint, unit test, build) · `browser` (stack lengkap di runner + Playwright untuk semua shell) |
| `ci` (khusus `main`) | Setelah semua check hijau | `images`: build → Trivy scan → push ke GitHub Container Registry (tag SHA) · `deploy-dev` |
| `staging` | Push ke branch `staging` | Image VPS dibangun sekali: backend di-obfuscate (garble), frontend tanpa source map (§9.4), tag `<sha>-vps` → deploy ke Staging + browser test |
| `release` | Tag `vX.Y.Z` pada commit `staging` | Image `<sha>-vps` yang sudah diuji di Staging dipromosikan menjadi `vX.Y.Z` tanpa build ulang → GitHub Release → Production per instance dengan approval manual |

**Alur branch:**

```text
develop ──PR──▶ main ──PR──▶ staging ──tag vX.Y.Z──▶ Production
(kerja)         (Dev)        (Staging/UAT)           (approval manual)
```

Hanya ada tiga branch tetap:

- `develop` → tempat semua commit. Saat sekumpulan perubahan siap, buka PR `develop → main`.
- `main` → Dev otomatis. Hanya menerima PR dari `develop` dengan **merge commit** (bukan squash, agar riwayat
  `develop` tetap sama dengan `main` dan PR berikutnya tidak conflict). Setelah merge, `develop` disamakan lagi
  dengan `git merge --ff-only origin/main`.
- `staging` → Staging otomatis. Hanya menerima PR `main → staging` saat sekumpulan fitur siap UAT; tidak ada commit
  langsung. Perbaikan dari temuan UAT dibuat di `develop` → `main` → dipromosikan lagi ke `staging`.
- Tag `vX.Y.Z` hanya boleh pada commit di `staging` (dicek otomatis). Production menjalankan image yang persis sama
  dengan yang diuji di Staging.

Penyesuaian dari rencana: OpenAPI di-generate dari kode (code-first) dan sqlc tidak dipakai, sehingga cek drift
dilakukan pada `openapi.json` dan API client TypeScript. Job deploy otomatis dilewati sampai server Dev/Staging/
Production dikonfigurasi (Keputusan Terbuka #1 — hosting). Daftar secret/variable dan langkah konfigurasi ada di
runbook `docs/runbooks/ci-cd.md`.

## 12.3 Urutan Pengembangan P1–P6

Gelombang 1 (P1 Golf Core MVP dan P2 Complete Golf Experience & Shared Core) dikerjakan paralel oleh dua developer.
Sejak Oktober 2026 pengembangan dilanjutkan oleh **satu developer**, sehingga fase berikutnya dikerjakan berurutan:

| Urutan | Fase | Syarat mulai |
|---|---|---|
| 1 | P1 — Golf Core MVP · P2 — Complete Golf Experience & Shared Core | P0 selesai (Release 0) |
| 2 | P3 — Commercial & Business Expansion | P1 dan P2 sudah merge ke `main` |
| 3 | P4 — Enterprise Back Office | P3 sudah merge ke `main` |
| 4 | P5 — People & Advanced Enterprise | P4 sudah merge ke `main` |
| 5 | P6 — SaaS Commercialization | P5 sudah merge ke `main` |

**Aturan kerja:**

1. **Tiga branch tetap** (§12.2). Semua commit di `develop`; merge ke `main` hanya lewat Pull Request yang semua
   check CI-nya hijau (branch protection: wajib PR, wajib lulus `lint`, `backend`, `frontend`, `browser`, tidak
   boleh force push). Tidak ada approval wajib karena tidak ada developer kedua; CI menjadi gerbang utamanya.
2. **Batas module.** Tiap module memegang kode Go dan schema-nya sendiri (§4.1). Kebutuhan lintas module dipenuhi
   lewat interface/API publik module atau domain event + outbox (§4.2–4.3), bukan dengan mengakses tabel module lain.
3. **File yang di-generate tidak diedit manual.** `api/openapi/openapi.json` dan
   `web/packages/api-client/src/schema.ts` dibuat ulang dengan `make openapi`. CI gagal bila basi.
4. **Migration.** Urutan goose terpisah per module (`db/migrations/<module>/`) dan selalu expand-only (§7.5) agar rilis
   tetap zero-downtime.
5. **Titik sentuh bersama** — wiring module (`internal/app`), katalog permission & role
   (`internal/platform/catalog`), navigasi shell — diubah secara aditif agar fase sebelumnya tidak rusak.
6. **Rilis.** Satu tag `vX.Y.Z` dari `staging` dapat berisi pekerjaan beberapa fase. Fitur yang belum siap dirilis
   disembunyikan dengan feature flag/module flag instance (§9, FR-INS), bukan dengan menahan merge.

---

# 13. Testing & Quality

| Level | Tools *(usulan)* | Fokus |
|---|---|---|
| Unit | `go test`, Vitest | Aturan bisnis di `service`: pricing, eligibility, kalkulasi folio, handicap |
| Integration | `go test` + testcontainers-go (PostgreSQL asli) | Query sqlc, constraint (EXCLUDE, RLS), transaksi, outbox |
| Contract | Validasi response terhadap OpenAPI | API tidak menyimpang dari spec |
| E2E | Playwright | Flow kritis: booking → check-in → tee-off → folio → payment; POS offline → sync |
| Load | k6 | Rebutan tee time saat jadwal dibuka, puncak POS saat event |
| Migrasi data | Skrip rekonsiliasi | Total saldo member, voucher, piutang Rhapsody vs OneClub |

Quality gate:

- Tidak ada merge tanpa review minimal 1 orang (2 orang untuk `billing`, `accounting`, dan migration destruktif).
- Flow keuangan wajib memiliki integration test.
- Bug produksi ditutup dengan test yang mereproduksinya.

---

# 14. Non-Functional Requirements

Target awal *(usulan, perlu divalidasi dengan club dan hasil load test)*:

| Area | Target |
|---|---|
| Availability (jam operasional club) | 99.5% per bulan |
| Latency API | p95 < 300 ms untuk read, < 800 ms untuk transaksi booking/payment |
| Real-time update tee sheet | < 2 detik dari perubahan sampai tampil di layar lain |
| POS offline | Bisa bertransaksi minimal satu shift penuh tanpa koneksi |
| RPO (data hilang maksimum) | ≤ 5 menit (WAL archive) |
| RTO (waktu pulih) | ≤ 4 jam untuk kegagalan total host |
| Retensi audit log | Minimal 5 tahun (menyesuaikan kebutuhan pajak & akuntansi) |
| Browser | Chrome/Edge/Safari versi terbaru; tablet Android & iPad |

---

# 15. Keputusan Terbuka

| # | Keputusan | Opsi | Dibutuhkan sebelum |
|---|---|---|---|
| 1 | Hosting | Cloud (region Indonesia) vs server on-premise club | P0 |
| 2 | HA database | Opsi A / B / C (bagian 10.5) | Go-live P1 |
| 3 | Vendor payment gateway | Ditentukan bersama club | P1 (payment) |
| 4 | Provider WhatsApp Business API (BSP) | Ditentukan bersama club | P1 (notifikasi) |
| 5 | Object storage | Managed S3-compatible vs self-hosted | P0 |
| 6 | HRIS & Payroll | Full payroll vs operational HR + payroll pihak ketiga (Product Overview §39) | P5 |
| 7 | Integrasi hardware | Vendor & protokol locker, turnstile, ball dispenser | P2 |
| 8 | Native mobile app | Tetap PWA vs native | Evaluasi setelah P2 |
| 9 | Library yang ditandai *(usulan)* di dokumen ini | Dikonfirmasi tim engineering | P0 |
| 10 | Accent default OneClub | Preset Morphic vs custom accent dari warna brand | P0 (M0) |
| 11 | Perlindungan source code (§9.4) | **Diputuskan:** obfuscation `garble` + tanpa source map hanya pada build deploy VPS. Terbuka: license key sejak P0 atau P6 | Deploy VPS pertama |