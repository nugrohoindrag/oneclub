# Catatan Hutang — Integrasi P2 di atas P1

Status per 4 Oktober 2026. Branch `feat/p2-on-p1`, commit terakhir `49b73ce`. Semua commit masih lokal dan belum di-push.

## Aturan yang wajib dipatuhi

Sumber aturan: Tech Doc §4.2, §12.3 dan PRD P2 §5.4.

- **File milik P1 tidak diubah isinya.** Pemilik P1 adalah Dian; modulnya golf, billing, crm, membership dan commercial/pricing. Kebutuhan P2 ditaruh di dua tempat:
  - sub-package P2;
  - file kontrak aditif `p2_*.go` / `pricing_p2.go`, yang wajib direview Dian.
- **Tidak ada query ke schema modul lain.** Gunakan salah satu:
  - API publik di package root modul pemilik;
  - read model `reporting.*`;
  - domain event.
- **Modul lain hanya mengimpor package root.** Archtest P1 menegakkan ini. Sub-package seperti `commercial/pos` diakses lewat interface di root (`commercial.POS`, `commercial.Vouchers`).
- **`internal/app`, catalog dan navigasi hanya diubah secara aditif.** Wiring P2 ada di `internal/app/p2.go`.

## Sudah selesai

- **Golf P2** ada di `internal/golf/experience`.
  - Ronde berjalan dari event P1 `golf.flight_teed_off` dan `golf.round_finished`.
  - Data P2 disimpan di tabel P2 (`golf/00003`), tanpa `ALTER` ke tabel P1.
- **Billing:** `p2_api.go` berisi `OpenLineFolio`, `AddLineCharge`, `TakeTender`, `NetPaid`, `ShiftPayments`, `TagPayment`, `ChargedFor` dan `DeferredBalance`.
- **CRM:** `p2_api.go` berisi `Engagement`, `CustomerProfile`, `Recipient`, `FillBirthDate` dan `FindOrCreate`.
- **Membership:** `p2_contract.go` berisi:
  - `RegisterP2`, `RegisterP2Jobs`, `OnActivated` dan `NewApplication`;
  - perluasan field pada resource P1.
- **Commercial:**
  - `pricing_p2.go` (kontrak C4, endpoint `pricing:resolve-line`);
  - `sales_api.go` (interface POS dan Vouchers);
  - sub-package `pos` dan `voucher`.
- **Reporting:**
  - read model `reporting/00004_p2_read_models.sql`;
  - report dan dashboard P2 hanya membaca view.
- **Migrasi gelombang 2** menumpang tool Rhapsody P1: `internal/app/rhapsody/p2.go` dan `platform/00008`.
- **Route member** dipindah ke `/api/v1/member/*`. Path P2 golf mengikuti PRD §11.
- **Inventory** membaca produk lewat interface `ProductCatalog`, yang diimplementasikan oleh `pos`.

## Hutang yang tersisa (urut kerja)

### 1. Sisa query lintas schema

Status: sedang dikerjakan. Fungsi pengganti sudah ada; pemanggilnya belum diganti.

- `internal/stay/stays.go`:
  - `commercial.package_rates` → ganti dengan `commercial.PackageRateMinutes`;
  - pemilihan outlet default lewat `commercial.outlets` → tambahkan `DefaultOutlet` ke interface `commercial.POS` (implementasinya sudah ada di `pos/catalog.go`);
  - `UPDATE reservation.reservations` → `m.Res.SetPolicyRefs` dan `m.Res.SetSource`;
  - `billing.folio_lines ... source_id / status = 'posted'` → `billing.ChargedFor(folio, lineID)`. Ini **bug**: kolom tersebut tidak ada di P1.
- `internal/commercial/voucher/voucher.go`: `liabilityOf` → `billing.DeferredBalance`.
- `internal/commercial/pos/pos.go`:
  - nilai `paid` per bill → `billing.NetPaid(folio, "billId", id)`. Ini **bug**: memakai `p.kind`.
  - `isMember` → `membership.HasMemberRate`. Ini **bug**: memakai tabel interim `membership.membership_members`.
  - `UPDATE billing.payments SET tender_ref` → `billing.TagPayment`.
  - laporan shift → `billing.ShiftPayments`. Ini **bug**: memakai `kind`.
- Cek ulang dengan perintah ini (yang tersisa hanya boleh FK `Ref` resource):

  ```bash
  grep -rnE "(FROM|JOIN|UPDATE|INTO) (crm|billing|membership|commercial|reservation|golf|stay|sportclub|inventory)\." internal/<modul-P2>
  ```

- Pastikan juga `golf/experience` tidak menulis tabel P1, kecuali lewat `golf/p2_contract.go`.

### 2. Dokumen kontrak untuk direview Dian

Buat `docs/p2-contract-review.md` (atau isi deskripsi PR). Daftar isinya:

- **Golf:** `p2_contract.go` (`StartCartAssignment`, `VerifyReciprocalPlayer`).
- **Billing:**
  - `p2_api.go`, `lines.go`, `p2_http.go`, `me.go`, `crm360.go`;
  - migration `billing/00003`: kolom baru di folios, folio_lines dan payments, plus tabel deferred dan payouts.
- **CRM:**
  - `p2_api.go` dan file engagement;
  - mutasi runtime `Customers.Fields` / `Preferences`;
  - migration `crm/00003`.
- **Membership:**
  - `p2_contract.go`, `p2_*.go`, `jobs.go`, `me.go`, `public.go`;
  - migration `membership/00003`: memperluas CHECK status memberships, sehingga status `paused`/`suspended`/`cancelled` juga terbaca oleh kode P1.
- **Commercial:**
  - `pricing_p2.go`, yang melonggarkan `Required` pada ratePlanId/effectiveFrom/pricingMode/session dan membungkus hook rule P1;
  - `sales_api.go`, `line_pricing.go`;
  - migration `commercial/00003`.
- **Catatan untuk P1:**
  - Overview Customer 360 P1 belum menyembunyikan preferensi sensitif (diet/alergi).
  - `CaddyBoard` P1 tidak mengenal clock-out P2.
  - Pemutakhiran readiness cart menjadi Ready tidak wajib melalui inspeksi.

### 3. Test (pekerjaan terbesar)

- Perbaiki `test/e2e/p2_migration_test.go`, yang masih mengimpor paket `internal/migration` yang sudah dihapus. Tulis ulang ke `rhapsody.Stage/Validate/Load` + P2Deps.
- Sesuaikan semua test P2 ke struktur baru:
  - **Path baru:**
    - golf: `/golf/hole-in-ones`, `/golf/rounds/{id}:start|:hole-progress|:complete`, `/golf/my-assignments`, `/golf/golf-cart-inspections`, `/golf/golf-cart-maintenance`, `/golf/range-sessions`, `/golf/caddy-rotation`, `/golf/caddies/{id}/profile`;
    - member: `/member/*` (`/member/golf/*`, `/member/sport-club/*`, `/member/reservations`, `/member/stays`, `/member/preferences`);
    - pricing: `pricing:resolve-line`.
  - **Alur golf:** lewat booking, check-in dan starter P1.
    - Scorecard dan hole progress dibuat oleh subscriber event (asinkron lewat outbox), jadi test perlu men-dispatch outbox.
  - **Pricing:** weekdays `"1,2,3,4"`, includesHolidays, priority (angka lebih kecil menang), golf wajib ratePlanId.
  - **Membership:** packages, application → activate, program_kind `sport_club`.
  - **Billing:** customer-accounts.
  - **Tipe yang pindah package:**
    - tipe POS/voucher sekarang `commercial.Order` dan sejenisnya;
    - `crm.Engagement`;
    - `experience.Module`.
- Gabungkan harness test yang disentuh P1 dan P2: `access_test`, `harness_test`, `services_test`, `p2_zz_coverage`.
- Jalankan:
  - `go build ./... && go vet ./...`;
  - `go test ./...` P0+P1+P2 dengan coverage penuh;
  - `go test ./internal/platform/provision/`, yang menerapkan semua migration ke DB baru. Belum dijalankan sejak migration golf, reporting dan platform/00008 diubah.

### 4. OpenAPI & frontend

- Jalankan `make openapi` untuk generate ulang `api/openapi/openapi.json` dan `web/packages/api-client/src/schema.ts`.
- Frontend:
  - gabungkan `main.tsx` backoffice, member dan ops dengan versi P1;
  - pindahkan halaman P2 ke path API baru;
  - tile ops dan aplikasi `caddy`;
  - member app pakai `/member/*`.
- Jalankan `pnpm -r typecheck` dan `pnpm -r build`.

### 5. Dokumen

- Perbarui `docs/p2-traceability.md`: path, sub-package dan tabel baru.
- Perbarui memory proyek setelah selesai.

## Catatan teknis

- **Kebiasaan tooling:**
  - Heredoc panjang di Bash tool sering gagal parse. Tulis skrip perl atau Go ke folder scratchpad dengan tool Write.
  - Jalankan `goimports -w` setelah edit massal.
- **Referensi kode P2 asli** (sebelum integrasi): worktree `../p2ref` pada commit `e2ef6d5`. Hanya untuk dibaca, jangan diedit.
- **Versi P1 untuk pembanding:** commit `a48e6a3`. Gunakan `git diff a48e6a3 -- <file P1>`; untuk file P1 hasilnya harus kosong, kecuali `internal/app/app.go`, `internal/app/rhapsody/*` dan `cmd/oneclub/main.go`, yang hanya boleh berisi tambahan.
- **Tidak ada state `under_inspection`** untuk golf cart (PRD P2 §6 #11). Cart yang kembali menjadi Not Ready / Charging (aturan P1). Setelah itu inspeksi post-op, lalu inspeksi pre-op mengubahnya menjadi Ready.
