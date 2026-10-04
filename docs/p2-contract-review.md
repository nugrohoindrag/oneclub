# Review Kontrak P2 di Module P1

Untuk: Dian (pemilik P1). Branch `feat/p2-on-p1`, pembanding P1 commit `a48e6a3`.

Dasar aturan: Tech Doc §4.2, §7.5, §12.3 dan PRD P2 §5.4. Semua kebutuhan P2 di module P1 ada di file baru (aditif) dan migration `00003` per module, dengan **satu pengecualian**: `billing/finance.go` (lihat bagian berikut). Mohon direview per bagian di bawah.

Cek cepat file P1 yang berubah (hasilnya harus hanya `internal/billing/finance.go`):

```bash
git diff --diff-filter=MD --stat a48e6a3 -- internal/golf internal/billing internal/crm internal/membership internal/commercial/*.go
```

## Perubahan di file P1: Member Statement per lini (kontrak C2)

PRD P2 EP-03 menetapkan billing diimplementasikan P1 lewat kontrak C1–C3. **FR-BIL-P2-02 (Must)** mewajibkan Member Statement bulanan menampilkan rincian per lini. Statement P1 (`GenerateStatements`) belum punya rincian ini, dan tidak ada titik ekstensi. Karena itu perubahannya dibuat di `finance.go` seminimal mungkin (+8/−3 baris):

- `stmtLine` mendapat field `Line` (JSON `businessLine`).
- Query entri membaca `coalesce(e.business_line, f.business_line, '')` lewat `LEFT JOIN billing.folios`. Member charge golf P1 terbaca sebagai `golf`, karena default kolom `folios.business_line`.
- PDF menambah baris "Charges · <lini>" sebelum "Total charges". Pengelompokan dan labelnya ada di file P2 `lines.go` (`chargesByLine`).

Perilaku P1 lain tidak berubah: saldo, total, notifikasi, dan format baris yang sudah ada.

## Ringkasan risiko untuk kode P1

Hal yang perlu diperhatikan saat review, urut dari yang paling berdampak ke kode P1:

1. **Status membership bertambah** (`membership/00003`). CHECK `memberships.status` dan `members.status` sekarang juga menerima `paused`, `suspended` dan `cancelled`. Kode P1 yang memakai `switch status` atau mengecek `status = 'active'` perlu dipastikan memperlakukan status baru itu sebagai tidak aktif.
2. **`pricing_rules.rate_plan_id` boleh NULL** (`commercial/00003`). Rule non-golf tidak wajib punya rate plan. Resolve P1 memakai `JOIN rate_plans`, jadi rule P2 tanpa rate plan otomatis tidak ikut ke golf. Rule golf tetap wajib `ratePlanId`; ini ditegakkan oleh `ruleP2BeforeWrite`.
3. **Resource P1 diperluas saat runtime** (`init()` di file kontrak): field, enum dan hook tambahan pada resource P1. Perilaku P1 dipertahankan, karena hook P1 tetap dipanggil di dalam hook P2.
4. **Kolom baru di tabel billing P1** semuanya nullable atau punya default, jadi `AddCharge` dan `TakePayment` P1 tetap jalan tanpa perubahan.

Semua CHECK yang diganti sudah dicek hanya *melebar*: setiap nilai P1 masih diterima. Daftarnya: `folios.source_type`, `payments.method_type`, `customer_preferences.category`, `membership.types.category`, `memberships.status`, `members.status`, `cards.status`, `applications.channel`, `rate_plans.business_line`, serta `pricing_rules.charge_type`, `segment` dan `channel`.

## Golf

**File:** `internal/golf/p2_contract.go` (kontrak C8)

- `StartCartAssignment`: cart pengganti langsung `in_use` untuk flight yang sedang bermain (FR-CTL-05). Prosesnya sama dengan yang dilakukan tee-off P1.
- `VerifyReciprocalPlayer`: menandai reciprocal player sudah terverifikasi. Ini menggantikan cek teks bebas (FR-RCP-03).

**Migration** `golf/00003_p2_golf.sql` hanya berisi tabel baru, tanpa `ALTER` ke tabel P1.

- Handicap Index lokal (WHS) dan handicap federasi disimpan di tabel P2 `golf.handicap_indexes` (PRD P2 FR-SCR-08).
- `golf.handicaps` P1 (`manual`/`import`) tidak ditulis oleh P2.

Sub-package `golf/experience` **membaca** tabel golf P1 (booking, flight, caddy, cart) di schema yang sama. Perubahan status P1 hanya lewat event P1 (`golf.flight_teed_off`, `golf.round_finished`) dan fungsi di atas.

## Billing

**File:**

| File | Isi |
|---|---|
| `p2_api.go` | `OpenLineFolio`, `AddLineCharge`, `TakeTender`, `NetPaid`, `ShiftPayments`, `TagPayment`, `ChargedFor`, `DeferredBalance`, `TenderOf` |
| `lines.go` | Charge lintas lini (C1), member charge termasuk offline (C2), tender hook Voucher & Prepaid (C3), deferred revenue, payout caddy/instruktur |
| `p2_http.go` | Endpoint P2 billing |
| `me.go` | Pembayaran online folio dari member app / payment link |
| `crm360.go` | Bagian billing di Customer 360 |

**Migration** `billing/00003_p2_billing.sql`:

- `folios`: tambah `business_line` (default `golf`), `reservation_id`, `corporate_name`; `source_type` diperluas.
- `folio_lines`: tambah `business_line`, `revenue_component` (nullable; data lama diisi dari `charge_type`), `tax_lines`, `beneficiary_type`/`beneficiary_id`.
- `payments`: tambah `tender_ref`, `outlet_id`, `shift_id`, `offline`, `needs_review`, `idempotency_key` (unique per property); `method_type` diperluas.
- `customer_accounts.status_reason`; `account_entries` tambah `business_line`, `source_type`, `source_id`, `offline`.
- Tabel baru `deferred_revenue_entries` (append-only lewat trigger) dan `payouts`.

## CRM

**File:**

| File | Isi |
|---|---|
| `p2_api.go` | `Engagement`, `CustomerProfile`, `Recipient`, `FillBirthDate`, `FindOrCreate` |
| `engagement.go`, `foundation.go` | Preferensi terstruktur, interaksi, segmen, feedback, campaign, Customer 360 lintas lini (lewat provider hook) |
| `public.go` | Endpoint publik: rate limit, honeypot, dedup customer |
| `me.go` | Profil, preferensi dan consent milik user portal |

**Mutasi runtime** (`init()` di `p2_api.go`):

- field P2 ditambahkan ke `Customers.Fields`;
- kategori dan field P2 ditambahkan ke `Preferences`;
- `Preferences.Hooks.AfterRead = maskPreference` dipasang **hanya bila** P1 belum punya hook.

**Migration** `crm/00003_crm_foundation.sql`:

- `customers`: tambah `resident_ref`, `student`, `student_valid_until`, `marital_status`, `locale`, `consent_profiling`, `consent_updated_at`.
- `customer_preferences`: kategori diperluas; tambah `ref_type`, `ref_id`, `sensitive`, `source`.
- Tabel baru: `interactions`, `segments`, `segment_members`, `feedback_requests`, `feedback`, `campaigns`, `campaign_deliveries`.

## Membership

**File:**

| File | Isi |
|---|---|
| `p2_contract.go` | `RegisterP2`, `RegisterP2Jobs`, `OnActivated`, `NewApplication`; perluasan field resource P1 |
| `p2_api.go` | `ActiveFor`, `HasMemberRate` (kontrak C5) dan query entitlement |
| `p2_lifecycle.go`, `p2_http.go` | Pause, suspend, cancel, upgrade/downgrade, ganti nominee, annual fee (via approval) |
| `jobs.go` | Job harian: annual fee, reminder, suspensi otomatis, akhir pause, batas umur anak |
| `me.go`, `public.go` | Self-service member dan aplikasi membership dari website |
| `policies.go` | Policy P2 |

**Mutasi runtime:** enum `Types.category` dan `Members.status` diperluas, dan field P2 ditambahkan ke `Types`.

**Migration** `membership/00003_p2_lifecycle.sql`:

- `types`: tambah `annual_fee`, `grace_days`, `rank`, `entitlements`, fee kartu/reaktivasi/nominee; `category` diperluas.
- `memberships`: status diperluas (lihat risiko #1); tambah kolom pause, suspensi dan cancel, serta `next_fee_due`.
- `cards`: status `blocked`/`replaced`; tambah `blocked_at`, `block_reason`, `replaced_by`.
- `applications.channel`: tambah `website`.
- Tabel baru `fees`, `fee_reminders`, `requests`.
- `programs.operational` default menjadi `true`.

## Commercial (pricing)

**File:**

- **`pricing_p2.go`** (kontrak C4). Endpoint `pricing:resolve-line` berdiri di samping `pricing:resolve` P1.
  - `init()` memperluas enum `segment`, `channel`, `chargeType` dan `businessLine`.
  - `Required` dilonggarkan untuk `ratePlanId`, `effectiveFrom`, `pricingMode` dan `session` (default: hari ini, `nett`, `other`).
  - Field P2 ditambahkan ke `TimeBands`, `RatePlans` dan `PricingRules`. Resource `DayTypes` P1 tidak disentuh.
  - Hook `BeforeWrite` P1 dibungkus: hook P1 tetap dipanggil, dan rule golf tetap wajib rate plan.
- **`line_pricing.go`**: resolver multi-lini. Aturannya sama dengan P1: rule paling spesifik menang, lalu angka priority terkecil, lalu effective date terbaru. Golf tetap memakai `Resolve` P1.
- **`sales_api.go`**: interface `commercial.POS` dan `commercial.Vouchers` untuk module lain. Implementasinya ada di sub-package `pos` dan `voucher` milik P2.

**Migration** `commercial/00003_p2_pricing.sql`:

- Tabel baru `day_type_sets`, `line_day_types` dan `package_rates`. Day type lini lain disimpan di `line_day_types` (kode unik per set), sehingga `commercial.day_types` tetap khusus golf: tee sheet, harga golf dan override kalender P1 tidak pernah melihat day type lini lain.
- Kolom P2 di `time_bands`, `rate_plans`, `pricing_rules` (termasuk `line_day_type_id` untuk rule non-golf) dan `pricing_snapshots`. Rule golf hanya boleh memakai `day_type_id`, rule lini lain hanya `line_day_type_id`; ini ditegakkan oleh `ruleP2BeforeWrite`.
- `pricing_rules.rate_plan_id` menjadi nullable (lihat risiko #2).

Migration `commercial/00004_vouchers.sql` dan `00005_pos.sql` hanya berisi tabel milik P2.

## Catatan untuk P1 (bukan perubahan P2)

Temuan di kode P1 berikut **akan dikerjakan oleh tim P2 di PR yang sama**, dengan perubahan minimal. Setelah dikerjakan, tiap perubahan dipindah ke bagian "Perubahan di file P1" untuk direview. Selain itu akan dicek juga dampak status membership baru dan `rate_plan_id` NULL terhadap kode P1 (lihat "Ringkasan risiko").

- Overview Customer 360 P1 belum menyembunyikan preferensi sensitif (diet/alergi). P2 hanya memasang mask di resource `Preferences`.
- `CaddyBoard` P1 belum mengenal clock-out caddy dari P2.
- Readiness cart bisa diubah menjadi Ready tanpa inspeksi. Menurut PRD P2 §6 #11, cart yang kembali menjadi Not Ready / Charging, lalu inspeksi pre-op mengubahnya menjadi Ready.
- Kode P1 masih membaca schema module lain langsung, yang melanggar Tech Doc §4.2 #2:
  - `golf/booking.go` membaca `reservation.allocations`;
  - `golf/modify.go` membaca `billing.folio_lines`;
  - `golf/portal.go` membaca `membership.members`;
  - `golf/teesheet.go` membaca `membership.types`.
