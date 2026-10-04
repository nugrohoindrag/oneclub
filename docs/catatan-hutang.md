# Catatan Hutang — Integrasi P2 di atas P1

Status per 4 Oktober 2026 sore. Branch `feat/p2-on-p1`, commit terakhir `73d020e`. Semua commit masih lokal dan belum di-push.

- Branch berada tepat di atas `origin/main` (0 commit tertinggal), jadi belum ada conflict.
- Pekerjaan sedang **di-hold** di tengah hutang #3 (test P2).

## Aturan yang wajib dipatuhi

Sumber aturan: Tech Doc §4.2, §7.5, §12.3 dan PRD P2 §5.4. Cek dokumen ini dulu sebelum mengubah kode supaya tidak perlu refactor ulang.

- **File milik P1 tidak diubah isinya.** Pemilik P1 adalah Dian; modulnya golf, billing, crm, membership dan commercial/pricing. Kebutuhan P2 ditaruh di dua tempat:
  - sub-package P2;
  - file kontrak aditif `p2_*.go` / `pricing_p2.go` / `lines.go` / `sales_api.go`, yang wajib direview Dian.
- **Pengecualian:** perubahan di file P1 hanya untuk dua hal, dikerjakan minimal dan dicatat di `docs/p2-contract-review.md`:
  - requirement PRD yang hanya bisa dipenuhi di file P1. Saat ini: `billing/finance.go` untuk FR-BIL-P2-02, statement per lini;
  - penyesuaian P1 di hutang #6.
- **Utamakan tabel/file milik P2** bila itu memberi solusi lengkap. Contoh: `commercial.line_day_types`, bukan memperluas `day_types` P1.
- **Tidak ada query ke schema modul lain.** Gunakan API publik package root, read model `reporting.*`, atau domain event. Pengecualian: `internal/app/rhapsody` (composition root), mengikuti preseden P1.
- **Migration harus expand-only** (Tech Doc §7.5). Tidak boleh `SET NOT NULL` atau mempersempit CHECK di tabel P1.
- **`internal/app`, catalog dan navigasi hanya diubah secara aditif.** Wiring P2 ada di `internal/app/p2.go`. Baris P1 di `app.go` tidak boleh diubah.
- **Target akhir:** PR ke `main` tanpa conflict, dead code dan catatan yang tidak terpakai sudah dibuang.

Cek cepat file P1 (hasilnya harus hanya `internal/billing/finance.go`):

```bash
git diff --diff-filter=MD --stat a48e6a3 -- internal/golf internal/billing internal/crm internal/membership internal/commercial/*.go
```

## Status hutang

| # | Hutang | Status |
|---|---|---|
| 1 | Query lintas schema | ✅ Selesai (`d72cffa`) |
| 2 | Dokumen kontrak untuk Dian | ✅ `docs/p2-contract-review.md` (diperbarui setiap ada perubahan kontrak) |
| 3 | Test | 🔄 P0/P1 hijau, provision hijau, unit hijau. Test P2: 13 lolos, 7 gagal (lihat sisa pekerjaan) |
| 4 | OpenAPI & frontend | ⏳ Belum dimulai |
| 5 | Dokumen | ⏳ Belum dimulai |
| 6 | Penyesuaian P1 (kita kerjakan, direview Dian) | ⏳ Belum dimulai |

## Sudah selesai sejak catatan sebelumnya

### Hutang #1: query lintas schema

Semua pemanggil sudah memakai API publik atau read model:

- `stay/stays.go`: package rate, default outlet (`commercial.POS.DefaultOutlet`), `SetPolicyRefs`/`SetSource`, `billing.ChargedFor`, ketersediaan bungalow (`reservation.BusyResources`), kode reservasi dari `reporting.reservations`.
- `commercial/pos/pos.go`: `paid` per bill (`billing.NetPaid`), `membership.HasMemberRate`, `billing.TagPayment`, laporan shift (`billing.ShiftPayments`), struk (`billing.PaymentsOf`).
- `commercial/voucher`: `billing.DeferredBalance`.
- `membership/public.go`: `crm.FillBirthDate` (sebelumnya tidak tercatat).
- `golf/experience`: handicap WHS/federasi ditulis ke tabel P2 `golf.handicap_indexes`. Sebelumnya ditulis ke `golf.handicaps` P1 dengan nilai yang ditolak CHECK-nya.
- Sisa query lintas schema hanya ada di file P1 milik Dian (`golf/booking.go`, `modify.go`, `portal.go`, `teesheet.go`). Ini sudah dicatat di dokumen review.

### Bug yang ditemukan dan sudah diperbaikan

Tidak satu pun tercatat di catatan lama, dan sebagian besar akan rusak di production.

**Aplikasi tidak bisa start:**
- permission P1 didefinisikan ulang oleh P2 (`golf.handicap.*`, commercial tax/product/outlet/pricing);
- route POS didaftarkan dua kali.

**Migration:**
- `billing/00003`: `revenue_component SET NOT NULL` membuat setiap charge golf P1 gagal. Sekarang nullable.
- `reporting/00004`: 4 view salah kolom (`golf_caddies`, `golf_caddy_duty`, `golf_cart_maintenance`, `golf_reciprocal_visits`), sehingga instance baru gagal migrate.
- `platform/00008`: `id_map` belum diberi RLS.
- Kolom migrasi gelombang 2 `start`/`end` diganti `start_at`/`end_at`, karena `end` kata kunci SQL.

**Wiring dan `app.go`:**
- Route P0 `reservation/resources` dan registrasi `Facilities` dikembalikan.
- Baris `crmModule` dikembalikan ke bentuk P1, sehingga `app.go` kembali murni aditif.
- `RegisterMeta` (endpoint `resource-definitions`, dipakai layar settings shell) dipasang lagi.

**Golf experience:** 5 query salah kolom (error 500):
- attendance memakai `WHERE` ganda;
- jam dinas dan kehadiran tablet kini dari `caddy_shifts`;
- charge reciprocal kini lewat `booking_player_id`;
- target pace kini dari `hole_pace_targets`.

**Pricing:**
- Day type lini lain dipindah ke tabel P2 `commercial.line_day_types` dengan kolom `pricing_rules.line_day_type_id`. Sebelumnya day type court/stay bisa mengambil alih harga golf dan tee sheet P1.
- Rule lini lain kini mengikuti versioning dan imutabilitas P1 (FR-PRC-04).
- Default `effectiveFrom` yang tidak pernah tercapai dibuang.

**Billing:**
- Kanal pembayaran P1 (`online`/`venue`/`member_account`) dipisah dari kanal penjualan. Sebelumnya `vouchers:sell` error 500.
- Kanal penjualan voucher kini memilih harga per kanal.

**Reservasi:** deposit yang sudah dibayar kini mengonfirmasi reservasi. P1 menyimpan deposit di `HeldDeposits`, sehingga sebelumnya semua booking ber-deposit tetap pending (engine + stay).

**Membership:**
- Detail keanggotaan kini menampilkan state lifecycle P2 (`MembershipDetail`).
- `change-preview` tidak lagi mengunci baris di transaksi read-only.

**Statement per lini (FR-BIL-P2-02, Must):** perubahan minimal di `billing/finance.go` (+8/−3 baris), wajib direview Dian.

### Hutang #3 (sebagian)

- `test/e2e/p2_migration_test.go` ditulis ulang ke `rhapsody.Stage/Validate/Load/Reconcile` + `P2Deps`. Sudah lolos.
- Helper test baru di `p2_helpers_test.go`:
  - `membershipType` (tipe + package P1);
  - `activeMembership` (alur aplikasi P1: submit → approval manajer → bayar → aktivasi otomatis);
  - `pastDate`;
  - `price()` kini memanggil `pricing:resolve-line`.
- `setupGolfCourse` memakai skema P1: section, hole dengan nomor 1–18 per course, course asset GeoJSON, `PUT pace-target`/`pace-tolerance`, route `sectionCodes`.
- `customer()` mengisi `crm.customers.user_id` lewat SQL; tautan portal P1 diisi saat aktivasi.
- Test P2 yang sudah lolos (13):
  - PricingRateCards, ReservationEngine, RhapsodyImport, MembershipLifecycle, MemberApp, StayAndVenue;
  - CRM, ClubPolicies, ReportsAndDashboards, SportClubEntryAccess, Classes;
  - Website dan coverage (GolfOperationsCoverage, SelfServiceCoverage).
  - Run penuh terakhir berhenti karena panic di `TestP2VoucherPrepaid`. Website dan coverage terakhir terverifikasi lolos di run sebelumnya; ulangi run penuh setelah VoucherPrepaid diperbaiki.

## Hutang yang tersisa (urut kerja)

### 3. Test (lanjutan)

Jalankan test P2:

```bash
ONECLUB_TEST_ADMIN_URL="postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable" \
ONECLUB_REQUIRE_FULL_COVERAGE=false go test -count=1 -run 'TestP2' ./test/e2e/
```

Test yang masih gagal:

1. **`TestP2VoucherPrepaid`** (`p2_voucher_test.go`):
   - Tender voucher (`voucher_prepaid`) tidak bisa lewat `POST /billing/payments` P1. Tulis ulang lewat checkout POS (`commercial/orders/{id}:pay` dengan tender voucher) atau `vouchers:redeem`.
   - `accounting-export` sekarang `POST /billing/accounting-exports` lalu `GET .../{id}/file`.
2. **`TestP2MemberStatement`** (`p2_voucher_test.go`) perlu ditulis ulang:
   - Charge golf/futsal/restoran dibuat lewat alur nyata: booking golf P1 atau folio walk-in (lini `golf`), booking court dengan member charge, order POS dengan member charge.
   - Lalu `POST /billing/member-statements:generate` dan cek baris `businessLine` serta saldo akhir sama dengan saldo akun.
   - Batas kredit diperbarui dengan `POST /billing/customer-accounts` ulang (tidak ada PATCH).
   - Pembayaran akun memakai `POST /billing/payments` dengan `accountId`.
   - Refund memakai `POST /billing/refunds`; void baris memakai `POST /billing/folios/{id}/lines/{lineId}:void`.
3. **`TestP2POSKitchenBOM`** (`p2_pos_test.go:141`): `member-accounts/{id}/statement` diganti `GET /billing/customer-accounts/{id}` (ledger) atau member statement.
4. **`TestP2ResourceDefinitionsCRUD`**:
   - Generator nilai otomatis gagal di 5 resource P1: customer relationship ke diri sendiri, `par` hole ≥ 3, `sectionCodes`, `intervalMinutes` ≥ 4, delete class schedule yang sudah dipakai.
   - Opsi: batasi ke resource milik P2 (resource P1 sudah diuji test P1), atau beri override nilai per resource.
5. **`TestP2GolfRound`, `TestP2GolfPaceAndRange`, `TestP2GolfReciprocal`**, plus bagian golf di `p2_zz_coverage_test.go`. Alur golf harus lewat P1:
   - `POST /golf/bookings`, lalu `POST /golf/check-ins`;
   - caddy lewat `POST /golf/caddy-assignments`, cart lewat `POST /golf/golf-cart-assignments`;
   - `starter-queue/{flightId}:tee-off`, lalu `DispatchPending` (event `golf.flight_teed_off` membuat ronde dan scorecard);
   - `rounds/{id}:hole-progress` → `rounds/{id}:complete`.
   - Pakai helper P1 di `p1_helpers_test.go` (`teeTimes`, `slotsOf`, `payFolio`, `firstFlight`, `presentCaddies`).
   - "route A+B scorecard template: []" (`p2_golf_test.go:77`): cek respons `GET /golf/playing-routes/{id}/holes` dengan skema hole P1.
   - `reciprocal-clubs` tidak lagi punya field `rateItem`.
6. **Path API lama yang masih tersisa: 25.** Daftar lengkap bisa dibuat dengan validator di bagian "Catatan teknis". Pemetaan utamanya:

| Lama | Baru |
|---|---|
| `flights/{id}:check-in` | `POST /golf/check-ins` |
| `flights/{id}/caddies` | `POST /golf/caddy-assignments` |
| `flights/{id}/golf-carts` | `POST /golf/golf-cart-assignments` |
| `flights/{id}:cancel` | `golf/bookings/{id}:cancel` |
| `GET flights/{id}` | `GET /golf/rounds/{id}` atau `tee-sheet` |
| `rounds/{id}:tee-off` | `starter-queue/{flightId}:tee-off` |
| `caddy-assignments/{id}:tip` | `POST /golf/caddy-tips` |
| `caddy-assignments/{id}:rate` | `POST /golf/caddy-ratings` |
| `golf-carts/{id}/inspections` | `POST /golf/golf-cart-inspections` |
| `golf-carts/{id}:status` | `golf-carts/{id}:set-readiness` |
| `hall-of-fame/{id}:consent` | `POST /golf/hall-of-fame/consents` |
| `hole-in-ones/{id}:submit` | `hole-in-ones/{id}:verify` |
| `reciprocal-visits:verify` | `POST /golf/reciprocal-visits` |
| `scorecards/{id}:submit` | `scorecards/{id}:validate` |
| `scorecards/{id}:correct` | `POST scorecards/{id}/corrections` |
| `scorecards/{id}/audit` | `GET scorecards/{id}/corrections` |
| `holes/{id}/distances` | `GET /golf/course-maps/{holeId}` atau `/member/golf/holes/{id}/distances` |
| `member/memberships/{id}:pay-fee` | `/member/membership-fees/{id}:pay-online` |

7. **Pemeriksaan akhir:**
   - Gabungkan harness test yang disentuh P1 dan P2 (`access_test`, `harness_test`, `services_test`, `p2_zz_coverage`).
   - Jalankan suite penuh **tanpa** `ONECLUB_REQUIRE_FULL_COVERAGE=false`, karena harness mewajibkan setiap route mutasi teruji dan teraudit.
   - Jalankan `go build ./... && go vet ./...`, `go test ./internal/...`, dan `go test ./internal/platform/provision/`.

### 4. OpenAPI & frontend

- Jalankan `make openapi` untuk generate ulang `api/openapi/openapi.json` dan `web/packages/api-client/src/schema.ts`.
- Frontend:
  - gabungkan `main.tsx` backoffice, member dan ops dengan versi P1;
  - pindahkan halaman P2 ke path API baru (`/member/*`, `golf/hole-in-ones`, `range-sessions`, `customer-accounts`, `line-day-types` untuk day type lini lain, dll.);
  - tile ops dan aplikasi `caddy`;
  - member app pakai `/member/*`;
  - shell settings membaca `resource-definitions` (sudah terpasang lagi).
- Jalankan `pnpm -r typecheck` dan `pnpm -r build`.

### 5. Dokumen dan bersih-bersih

- Perbarui `docs/p2-traceability.md`: path, sub-package dan tabel baru (`line_day_types`, `handicap_indexes`, `MembershipDetail`).
- Perbarui `docs/runbooks/rhapsody-migration-p2.md`. Isinya masih menjelaskan tool P2 lama (scope, `migration.Run`); sesuaikan ke `oneclub import rhapsody --stage|--validate|--load|--reconcile` dan entitas gelombang 2.
- Buang dead code dan catatan yang tidak terpakai. Kandidat:
  - kode P2 yang tidak lagi dipanggil setelah integrasi;
  - komentar yang merujuk tool/route lama;
  - file ini setelah semua hutang selesai.
- Perbarui memory proyek.

### 6. Penyesuaian P1 (kita kerjakan, direview Dian)

Temuan di kode P1 yang kita kerjakan sendiri dalam PR yang sama. Aturannya:
- perubahan seminimal mungkin dan tidak mengubah perilaku P1 lain;
- setiap perubahan ditambahkan ke bagian "Perubahan di file P1" di `docs/p2-contract-review.md`;
- test P1 harus tetap hijau, dan ditambah test bila perilakunya berubah.

**Perlu dicek karena data P2:**

1. **Status membership baru** (`paused`, `suspended`, `cancelled`). Telusuri kode P1 yang memakai status membership, misalnya `switch status`, enum `Membership.Status`, eligibility booking golf, member rate, member charge, kartu, dan portal. Pastikan status baru diperlakukan sebagai tidak aktif. Enum dokumentasi `Membership.Status` di `membership/http.go` sebaiknya ikut diperluas.
2. **`pricing_rules.rate_plan_id` NULL** untuk rule non-golf. Cek semua query P1 yang membaca `pricing_rules` (list, export, resolve, snapshot). Pastikan rule tanpa rate plan tidak menimbulkan error scan atau ikut ke golf.

**Temuan di kode P1 sendiri:**

3. **Customer 360 P1** (`crm/overview.go`) belum menyembunyikan preferensi sensitif (diet/alergi) bagi pengguna tanpa `crm.preference.view_sensitive`, padahal P2 sudah memasang mask di resource `Preferences`.
4. **`CaddyBoard` P1** belum mengenal clock-out caddy dari P2 (`golf.caddy_shifts.clocked_out_at`). Caddy yang sudah pulang masih tampil tersedia.
5. **Readiness golf cart** bisa diset Ready tanpa inspeksi (PRD P2 §6 #11). Cart yang kembali menjadi Not Ready / Charging; setelah inspeksi post-op, inspeksi pre-op mengubahnya menjadi Ready. Perlu aturan di `golf-carts/{id}:set-readiness`, atau hook kontrak dari P2.
6. **Query lintas schema di golf P1** (Tech Doc §4.2 #2). Ganti dengan API publik atau read model:
   - `golf/booking.go` membaca `reservation.allocations`;
   - `golf/modify.go` membaca `billing.folio_lines`;
   - `golf/portal.go` membaca `membership.members`;
   - `golf/teesheet.go` membaca `membership.types`.

**Kecil (opsional):**

7. `POST /billing/customer-accounts` untuk akun yang sudah ada tanpa perubahan mengembalikan 201 tanpa entri audit, sehingga harness audit menandainya. Pilihannya: kembalikan 200 untuk no-op, atau catat audit. Diskusikan dengan Dian.

## Catatan teknis

- **Tooling:**
  - Heredoc panjang di Bash tool sering gagal parse. Tulis skrip perl/Go/JS ke folder scratchpad dengan tool Write.
  - Jalankan `goimports -w` setelah edit massal.
  - Perl dengan delimiter `{}` gagal bila pola berisi kurung kurawal; pakai Edit tool.
- **PostgreSQL 18 lokal** jalan sebagai service Windows di `localhost:5432` (`postgres/postgres`); `psql` ada di `C:\Program Files\PostgreSQL\18\bin`.
- **Alat verifikasi yang terbukti berguna** (perlu dibuat ulang; scratchpad tidak permanen):
  - Validasi semua view/migration: terapkan bagian Up semua migration ke DB scratch, lalu jalankan statement file target satu per satu dan laporkan yang error.
  - Validasi kolom SQL di kode: kumpulkan literal SQL lengkap dari file `.go` P2, lalu `PREPARE` ke DB scratch. Postgres memeriksa tabel/kolom tanpa mengeksekusi.
  - Validasi path API di test: `go run ./cmd/oneclub openapi -o <file>`, lalu cocokkan setiap panggilan `"METHOD", "/api/v1/..."` di test dengan path OpenAPI. Test P1 = 0 path basi, jadi metodenya akurat.
  - Daftar permission duplikat: test sementara yang memanggil `app.Contributions()` dan melapor lewat `t.Errorf`. Tanpa `-v`, output `fmt` tidak tampil.
- **Perbedaan semantik P1 yang sering menjebak:**
  - Respons uang P1 berformat 4 desimal (`"350000.0000"`); bandingkan dengan `eqAmount`/`dec`.
  - `qris` adalah metode online (pending sampai webhook). Untuk pembayaran langsung pakai `card` + `reference` atau `cash`.
  - Status aplikasi aktif adalah `completed`; P1 mengaktifkan otomatis setelah folio iuran lunas (outbox).
  - Konfirmasi reservasi dan aktivasi berjalan lewat outbox. Di test panggil `inst.App.Dispatcher.DispatchPending`.
  - `Summary.Payments` hanya settlement; deposit ada di `HeldDeposits`.
  - Validasi `Required` resource engine berjalan **sebelum** hook `BeforeWrite`.
- **Referensi kode P2 asli** (sebelum integrasi): worktree `../p2ref` pada commit `e2ef6d5`. Hanya untuk dibaca.
- **Versi P1 untuk pembanding:** commit `a48e6a3`.
- **Tidak ada state `under_inspection`** untuk golf cart (PRD P2 §6 #11).
