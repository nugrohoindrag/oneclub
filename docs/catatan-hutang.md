# Catatan Hutang — Integrasi P2 di atas P1

Status per 4 Oktober 2026 malam. Branch `feat/p2-on-p1`. Semua commit masih lokal dan belum di-push.

- Branch berada di atas `origin/main` (0 commit tertinggal). **P1 belum di-merge ke `main`:** P1 ada di `origin/feat/p1-golf-core-mvp` (masih `a48e6a3`) dan `origin/staging`. PR P2 ke `main` ikut membawa commit P1, kecuali PR P1 di-merge lebih dulu. Bila P1 di-merge dengan squash, branch ini perlu di-rebase ke atas `main` baru.
- Hutang #3 (test) dan #4 (OpenAPI & frontend) selesai; berikutnya hutang #5 (dokumen) dan #6 (penyesuaian P1).
- **Rencana:** semua hutang #3–#6 diselesaikan sekaligus, lalu branch di-push dan dibuka PR ke `main`, supaya bisa lanjut ke P3. Menurut Tech Doc §12.3, P3 dimulai setelah P1 dan P2 merge ke `main`. Push dilakukan setelah suite e2e penuh hijau tanpa `ONECLUB_REQUIRE_FULL_COVERAGE=false`.

## Aturan yang wajib dipatuhi

Sumber aturan: Tech Doc §4.2, §7.5, §12.3 dan PRD P2 §5.4. Cek dokumen ini dulu sebelum mengubah kode supaya tidak perlu refactor ulang.

- **File milik P1 tidak diubah isinya.** Pemilik P1 adalah Dian; modulnya golf, billing, crm, membership dan commercial/pricing. Kebutuhan P2 ditaruh di dua tempat:
  - sub-package P2;
  - file kontrak aditif `p2_*.go` / `pricing_p2.go` / `lines.go` / `sales_api.go`, yang wajib direview Dian.
- **Pengecualian:** perubahan di file P1 hanya untuk dua hal, dikerjakan minimal dan dicatat di `docs/p2-contract-review.md`:
  - requirement PRD yang hanya bisa dipenuhi di file P1. Saat ini: `billing/finance.go` untuk FR-BIL-P2-02 (statement per lini) dan FR-INT-P2-03 (accounting export);
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
| 3 | Test | ✅ Suite e2e penuh hijau dengan coverage wajib; unit, provision, build dan vet hijau |
| 4 | OpenAPI & frontend | ✅ Typecheck dan build hijau; spec Playwright belum dijalankan |
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

### Hutang #3: test (selesai)

Suite e2e penuh hijau **dengan** coverage wajib (`go test ./test/e2e/`, tanpa `ONECLUB_REQUIRE_FULL_COVERAGE=false`). `go build`, `go vet`, `go test ./internal/...` dan provision juga hijau. Ke-21 test P2 lolos.

Cara test P2 kini memakai alur P1:
- **Golf:** course buatan test + tee sheet template P1 (`setupGolfCourse`), booking → bayar → caddy/cart P1 → check-in (`date` hari main) → tee-off lewat starter P1 (tablet `tee_off`, atau `rounds/{id}:start`) → `DispatchPending`. Setiap test golf memakai hari main sendiri (`clubDay` 18/22/26/30) agar antrean caddy tidak bentrok dengan test P1.
- **Waktu ronde:** pembagian fee caddy dan pace dihitung dari timestamp, jadi test memundurkan `tee_off_at`/`started_at`/`out_at` lewat SQL (helper `backdate`), seperti time-travel di test voucher.
- **Billing:** member statement dari charge nyata (folio walk-in golf, booking court, order POS); format uang P1 4 desimal dibandingkan dengan `dec`.
- **Resource CRUD:** hanya resource milik P2; resource P1 (`a48e6a3`) diuji test P1.
- **Bridge agent coverage** dijalankan di properti kedua (MDR), karena `TestBridgeAgent` P0 mengharuskan tepat satu agent di MAIN.
- Helper baru: `golfBooking`, `checkIn`, `dispatch`, `backdate`, `asMaps`, `activeApplication` (aplikasi dengan corporate account).

Bug yang ditemukan lewat test dan sudah diperbaiki:
- **Accounting export (FR-INT-P2-03, Must):** baris P2 diposting tanpa `components`, sehingga F&B (revenue) tergabung dengan penjualan voucher (liability) di satu baris `liability,other`. `AddLineCharge` kini mengisi komponen. Pergerakan deferred, payout caddy/instruktur dan shift POS ditambahkan lewat `appendLineExport` (`lines.go`) dengan satu baris di `finance.go` (dicatat di dokumen review).
- **FR-SCR-10 (Must):** scorecard guest P1 tidak punya customer. Kini profil customer dicari/dibuat dari kontak guest (`crm.FindOrCreate`).
- **FR-HIO-01 (Must):** draft HIO tidak pernah `insured`. Kini diturunkan dari komponen `hio` charge ronde P1 (`billing.LinesOf`).
- **`my-earnings`:** query attendance invalid (error 500), dan fee caddy pengganti tidak memakai pembagian seperti settlement.

### Hutang #4: OpenAPI & frontend (selesai)

- OpenAPI dan `schema.ts` digenerate ulang; lockfile mengikuti app `caddy` dan QR di shell. `pnpm -r typecheck` dan `pnpm -r build` hijau.
- Semua path API di frontend cocok dengan OpenAPI (validator di "Catatan teknis"; sisanya hanya prefix invalidasi cache).
- Halaman P2 ada di file P2 dan dipasang ke router P1 hanya dengan baris tambahan (daftar di `docs/p2-contract-review.md`). Path halaman mengikuti navigasi server.
  - **Backoffice:** hub `golf/operations`, `golf/master`, `sport-club`, `membership/lifecycle`, `booking/all-lines`, `stay-venue`, `crm/engagement`, `commercial/operations`, `commercial/master`, `inventory`; dashboard KPI `dashboards/*` dan `management/sport-club-performance|commercial-performance`. Master data hanya resource P2 (`ResourceIndex only`).
  - **Ops:** `starter/pace`, `caddy/incidents` (clock-in/out + insiden), `golf-staff/inspection`, `stay-desk`, `driving-range`, `sport-reception`, `instructor`, `pos`, `kitchen`, `clubhouse-screen`, plus tile Home. Live update lewat stream golf P1 (topik `golf.*`) dan stream KDS; event SSE bernama sesuai topik.
  - **Member:** `golf/scores`, `golf/scores/:id`, `sport-club`, `stay`, `vouchers`, `membership/services` (fee tahunan, pause, ganti kartu), `order-food`, `preferences`. Tiga item navigasi member ditambahkan.
  - **Caddy tablet:** `my-assignments`, `rounds/{id}`, `course-maps`, `on-course-orders`, `my-earnings`.
  - **Website:** helper P2 di `web/app/lib-p2.ts`; menu Sport Club, Stay & Venue, Hall of Fame.
- Halaman P2 yang dobel dengan P1 dibuang: kartu digital, membership, transaksi dan booking (member); starter tee-off, caddy queue/assignment, golf front desk (ops).
- Spec Playwright `web/e2e/p2.spec.ts` sudah memakai path baru, **tetapi belum dijalankan** (butuh API + preview yang berjalan; lihat `playwright.config.ts`).

Catatan terbuka dari hutang #4:
- **Customer 360 lintas lini** (FR-CRM-01) ada di `crm/customers/:id`, tetapi baru terjangkau bila halaman Customer 360 P1 diberi tautan. Masuk keputusan hutang #6.
- **Rating caddy dari member app:** endpoint `member/golf/caddy-assignments/{id}:rate` ada, tetapi `my-flights` P1 tidak mengembalikan id assignment, sehingga belum ada tombol rating (rating lewat link feedback setelah ronde tetap jalan).

## Hutang yang tersisa (urut kerja)

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
  - Validasi path API di frontend: kumpulkan literal `'/api/v1/...'` dan template string dari `web/apps/**` (ganti `${...}` dengan placeholder, buang `${qs(...)}`), lalu cocokkan dengan path OpenAPI. Typecheck saja tidak cukup karena path berupa string.
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
