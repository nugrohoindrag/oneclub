# OneClub
## Product Requirements Document — P4 Enterprise Back Office

**Version:** 1.1 (Draft): keputusan & asumsi Open Questions diterapkan  
**Phase:** P4 — Enterprise Back Office (Release 4)  
**Based on:** `product-roadmap.md` (§3, §50–54, §60, §65–75, §77), `OneClub — Product Overview v3.md` (§30, §33–38, §43, §49–53), `OneClub — Naming Convention.md` (§13, §19–21, §23–26, §29–33), `OneClub — Technical Documentation.md` (§3–4, §7.4–7.6, §8, §9, §13–15), `OneClub — PRD P0 Platform Foundation.md`, `OneClub — PRD P1 Golf Core MVP.md`, `OneClub — PRD P2 Complete Golf Experience & Shared Core.md`, `OneClub — PRD P3 Commercial & Business Expansion.md`  
**Date:** 4 October 2026

> Requirement yang ditandai *(usulan)* adalah turunan PRD ini dan belum tercantum eksplisit di roadmap atau Product Overview. Ketidaksesuaian antar dokumen sumber dan keputusan PRD ini atas masing-masing ada di [bagian 6](#6-hasil-pengecekan-dokumen-sumber). Keputusan dan asumsi atas open question ada di [bagian 16](#16-open-questions).

> **Pembaruan arsitektur aplikasi (5 Oktober 2026).** Staff App tetap **satu aplikasi** (satu build) dengan area per role, dan dibuka di empat domain (Product Overview §45, Technical Documentation §6.1): `dashboard.<club>` untuk kantor (Back Office, Management Dashboard yang dibatasi menu/permission, Platform Administration, dan Clubhouse Screen untuk role **Screen**; login email + password + MFA), `cashier.<club>` untuk perangkat kasir dan konter (Operational Interface, PIN per shift), `caddy.<club>` untuk Caddy Tablet, dan `kitchen.<club>` untuk Kitchen Display. Domain mengunci area; perangkat bersama tidak membuka area kantor. Untuk P4: inventory, procurement, accounting dan laporan keuangan ada di `dashboard`; pekerjaan gudang di perangkat bersama (penerimaan barang, stock opname, transfer) masuk `cashier` *(usulan)*; pemotongan stok dari penjualan POS dan BOM tetap dari `cashier`/`kitchen`.

---

# 1. Ringkasan

P4 menjadikan OneClub **financial & supply backbone** club: persediaan tercatat dan terpotong otomatis dari penjualan, pembelian berjalan dari permintaan sampai pembayaran pemasok, dan setiap transaksi operasional sejak P1 menjadi jurnal di buku besar OneClub sendiri. Targetnya adalah **Release 4 — Enterprise Back Office** (roadmap §75): *inventory, procurement, accounting, digital channel* untuk **Modern Golf & Country Club**, Tangerang.

P4 dikerjakan **paralel dengan P3** sebagai gelombang kedua (PRD P2 §5.4.1, PRD P3 §5.4). P4 adalah **konsumen** kontrak K1–K6 milik P3 dan data keuangan P1–P2; aturan kerjanya ada di [bagian 5.4](#54-pengembangan-paralel-dengan-p3).

P4 menghasilkan:

```text
Enterprise Back Office
├── INVENTORY
│   ├── Item Master, Category, UOM & Conversion, Barcode (melanjutkan foundation P2)
│   ├── Warehouse & Stock Location (Main Store → Kitchen, Bar, Pro Shop, Golf Ops, Sport Club,
│   │   Bungalow, Engineering, General)
│   ├── Stock Movement, Balance, Store Requisition, Transfer, Adjustment, Stock Opname
│   ├── Valuation (Average / FIFO), COGS, Inventory Journal
│   ├── Potong stok otomatis dari POS, BOM, banquet & paket; theoretical vs actual
│   ├── Production, Waste / Spoilage, Batch, Serial, Expiry
│   ├── Par Stock, Minimum Stock, Reorder Point → PR otomatis
│   └── Asset & Equipment (buggy fleet, alat gym, peralatan sewa, maintenance)
├── PROCUREMENT
│   ├── Supplier Master & Vendor Performance (melanjutkan foundation P0)
│   ├── Purchase Requisition (manual, reorder, banquet) → Approval
│   ├── RFQ → Vendor Quotation → Purchase Order
│   ├── Goods Receipt, Purchase Return
│   └── Vendor Invoice → 3-Way Matching → Accounts Payable
├── ACCOUNTING
│   ├── Chart of Accounts, Journal (append-only), General Ledger, Trial Balance
│   ├── Posting otomatis dari semua module (menggantikan Accounting Export)
│   ├── Accounts Receivable (invoice P3, member & corporate billing, aging)
│   ├── Accounts Payable (vendor invoice, payment run, aging)
│   ├── Cash & Bank (bank transaction, bank reconciliation)
│   ├── Revenue & Tax (revenue allocation, deferred revenue, breakage, caddy fee liability,
│   │   service charge, pajak, e-Faktur / Coretax)
│   ├── Financial Period & Closing
│   └── Financial Reports (P&L, Balance Sheet, Cash Flow, TB, Aging, Revenue by Business Line)
├── DIGITAL CHANNEL
│   └── Landing Page & CMS (pages, banner, news, gallery, promosi, tarif terstruktur, ID/EN)
└── INTEGRATION & OPERATIONS
    ├── Integration Layer (multi payment provider, Coretax, bank, hardware, email)
    ├── Warehouse interface (`ops`)
    ├── Inventory, Procurement, Financial KPI
    └── Migrasi gelombang 4 (stok awal, saldo awal GL, AP/AR terbuka, aset) & Release 4
```

Alur inti baru yang harus berjalan end-to-end di production:

```text
Supply:     Reorder Point / Banquet BEO / Manual → Purchase Requisition → Approval → RFQ
            → Vendor Quotation → Purchase Order → Goods Receipt → Stock Balance
            → Vendor Invoice → 3-Way Matching → Accounts Payable → Payment → Bank
Consume:    POS Sale / Banquet Event / Package / Golf Round → BOM → potong stok outlet
            → COGS → Inventory Journal; Stock Opname → Variance → Adjustment
Finance:    Billing / Commercial / Inventory / Procurement event → Posting Rule → Journal
            → General Ledger → Period Closing → Financial Reports; Invoice → e-Faktur
Digital:    CMS (draft → review → publish) + data terstruktur (tarif, paket, promosi, event)
            → Website ID/EN → Booking / Inquiry
```

---

# 2. Latar Belakang & Problem

- **Jurnal masih diketik ulang.** Sejak P1, OneClub mengirim **Accounting Export** harian ke sistem akuntansi club (PRD P1 FR-INT-P1-04, diperluas P2 FR-INT-P2-03 dan P3 FR-INT-P3-05). Roadmap §52 *Transition* menyatakan ini sementara: begitu Accounting live, transaksi operasional langsung menjadi jurnal dan laporan keuangan.
- **Food cost hanya teoretis.** P2 membangun recipe dan consumption ledger tanpa stok (PRD P2 Risiko #6). Club belum bisa melihat selisih pemakaian aktual vs resep, persediaan per outlet, maupun HPP yang benar.
- **Pembelian di luar sistem.** Permintaan dapur, kebutuhan bahan banquet dari BEO (PRD P3 K1), dan pembelian pro shop berjalan lewat kertas atau chat, sehingga tidak ada kontrol anggaran, approval, maupun pencocokan PO, penerimaan, dan tagihan pemasok (roadmap §51, 3-way matching).
- **Kewajiban keuangan tersebar.** Deposit/DP banquet, voucher & prepaid, poin loyalty, annual fee dibayar di muka, titipan caddy fee, dan service charge tercatat sebagai sub-ledger operasional (P2 FR-BIL-P2-06, P3 FR-LOY-08) tetapi belum dibukukan sebagai kewajiban di neraca.
- **Pajak keluaran manual.** Faktur pajak (e-Faktur / Coretax) untuk tagihan corporate dibuat terpisah dari invoice P3.
- **Website tidak bisa dikelola club.** Halaman P1–P3 diisi dari data terstruktur; berita, galeri, banner, dan konten bilingual masih memerlukan developer. Website Modern Golf lama menampilkan tarif sebagai gambar flyer usang (Product Overview §38).

---

# 3. Goals & Non-Goals

## 3.1 Goals

| # | Goal | Ukuran keberhasilan |
|---|---|---|
| G1 | OneClub menjadi buku besar club | Setelah 1 bulan rekonsiliasi dengan Excel Finance, laporan keuangan bulanan dihasilkan dari OneClub; Accounting Export dimatikan; Trial Balance seimbang setiap hari |
| G2 | Semua transaksi operasional terjurnal otomatis dan tertelusur | 100% charge, payment, refund, deposit, voucher, poin, settlement, dan stok yang terjadi menghasilkan jurnal; setiap baris jurnal tertelusur ke dokumen sumber dan pricing snapshot |
| G3 | Persediaan akurat per lokasi | Selisih stock opname bulanan ≤ toleransi yang disepakati club *(usulan: ≤ 2% nilai per outlet)*; stok terpotong otomatis dari penjualan, banquet, dan paket |
| G4 | Food cost aktual | Food Cost % aktual vs teoretis per outlet tersedia setiap periode (roadmap §60 *Theoretical vs Actual Variance*) |
| G5 | Procure-to-pay terkendali | 100% pembelian di atas ambang melalui PR → PO; vendor invoice hanya dibayar setelah 3-way matching |
| G6 | Kewajiban dan pendapatan ditangguhkan tercatat benar | Saldo GL untuk deposit, voucher, prepaid, poin, annual fee, dan caddy fee liability = saldo sub-ledger operasional setiap akhir hari |
| G7 | Pajak terintegrasi | Faktur pajak keluaran untuk invoice berpajak dibuat dari invoice OneClub dan tersinkron ke Coretax |
| G8 | Website dikelola club sendiri | Marketing menerbitkan halaman, berita, galeri, dan promosi ID/EN tanpa developer; tarif selalu dari pricing engine |
| G9 | Tanpa regresi Release 1–3 | Seluruh test E2E dan load test P1–P3 tetap lulus |

G1–G8 diturunkan dari scope roadmap §50–54, §60, §69. G9 mengikuti pola PRD P2–P3.

## 3.2 Non-Goals (P4)

- HRIS, attendance, dan payroll, termasuk pembayaran **service charge** ke karyawan dan payroll caddy/instruktur/komisi sales: **P5** (roadmap §55–58). P4 membukukan pool service charge dan kewajiban, lalu mengekspor dasar pembagian
- Advanced BI dan dashboard lintas domain lanjutan: **P5** (roadmap §60). P4 menyediakan KPI Inventory, Procurement, dan Finance dasar
- Budgeting & forecasting keuangan *(tidak tercantum di roadmap)*
- Multi-currency dan konsolidasi multi-entitas hukum (IDR, satu instance per customer). Konsolidasi antar **property** dalam satu instance sebagai Should
- Full hotel accounting, housekeeping, laundry, resort engineering di luar aset buggy & peralatan: Out of Scope (roadmap §73)
- Payment gateway tambahan untuk SaaS multi-customer: P6
- Menu engineering lanjutan: Out of Scope (roadmap §73)

---

# 4. Users & Personas

Role mengikuti Product Overview §44. Role template sudah di-seed di P0 (FR-IAM-06); P4 mengisi permission domain baru dan mengaktifkan role yang mulai login.

| Persona | Label role (UI) | Kebutuhan di P4 |
|---|---|---|
| Manajemen | General Manager, Club Manager | Financial Performance, Inventory & Procurement Performance, approval pembelian besar |
| Keuangan | Finance Manager, **Accountant** | CoA, jurnal, posting rule, AR/AP, bank reconciliation, pajak, period closing, laporan keuangan |
| **Gudang** | **Warehouse Staff** | **Mulai login di P4**: goods receipt, issuing, transfer, store requisition, stock opname |
| **Persediaan** | **Inventory Manager** | Item, lokasi, par stock, reorder point, valuasi, variance, waste |
| **Pembelian** | **Procurement Staff, Procurement Manager** | PR, RFQ, vendor quotation, PO, vendor performance |
| Approver | Approver | Menyetujui PR/PO sesuai matriks |
| Outlet & dapur | Outlet Manager, Kitchen Staff | Store requisition, production, waste, food cost aktual |
| Golf & aset | Golf Manager, Golf Staff | Asset register buggy, spare part, jadwal maintenance |
| Marketing | Marketing Staff | CMS: halaman, banner, berita, galeri, promosi, konten ID/EN |
| Auditor *(usulan)* | — (permission read-only) | Akses baca jurnal, GL, dokumen sumber, audit log |
| Pemasok | — (email/PDF) | Menerima RFQ dan PO; portal pemasok di luar scope |
| Tim internal OneClub | Platform Admin | Integrasi Coretax/bank, migrasi saldo awal, Release 4 |

---

# 5. Scope

## 5.1 In Scope

| Area | Referensi roadmap | Epic |
|---|---|---|
| Item Master & Inventory Setup | §50 | EP-01 |
| Stock Movement & Balance | §50 | EP-02 |
| Store Requisition & Stock Transfer | §50 | EP-03 |
| Stock Opname & Adjustment | §50 | EP-04 |
| Stock Valuation, COGS & Inventory Journal | §50 | EP-05 |
| Consumption Otomatis (POS, BOM, Banquet, Package) | §50, §35 (lanjutan P2) | EP-06 |
| Production, Waste, Batch, Serial & Expiry | §50 | EP-07 |
| Replenishment & Automatic PR | §50, §51 | EP-08 |
| Asset & Equipment | §50 (Asset & Equipment) | EP-09 |
| Supplier Management & Vendor Performance | §51 | EP-10 |
| Purchase Requisition & Approval | §51 | EP-11 |
| RFQ & Vendor Quotation | §51 | EP-12 |
| Purchase Order | §51 | EP-13 |
| Goods Receipt & Purchase Return | §51 | EP-14 |
| Vendor Invoice & 3-Way Matching | §51 | EP-15 |
| General Accounting | §52 | EP-16 |
| Posting Otomatis | §52 (Transition), Tech Doc §4.3–4.4 | EP-17 |
| Accounts Receivable | §52 | EP-18 |
| Accounts Payable | §52 | EP-19 |
| Cash & Bank | §52 | EP-20 |
| Revenue & Tax | §52 | EP-21 |
| Financial Reports | §52, §60 (Finance) | EP-22 |
| Accounting Transition & Opening Balances | §52 (Transition) | EP-23 |
| Landing Page & CMS | §53, §66.1 | EP-24 |
| Integration Layer P4 | §54 | EP-25 |
| Operational Interfaces P4 (Warehouse) | §66.4 | EP-26 |
| Configuration & Policies P4 | §65 | EP-27 |
| KPI, Dashboard & Reports | §60, §69 | EP-28 |
| Migrasi gelombang 4 | §72 | EP-29 |
| Production Readiness (Release 4) | §75 | EP-30 |

## 5.2 Batas P4 vs P5+ per Area

| Area | Dibangun di P4 | Lanjut di P5+ |
|---|---|---|
| Inventory | Item, lokasi, movement, requisition, transfer, opname, valuasi average/FIFO, potong stok otomatis, production, waste, batch/serial/expiry, par & reorder, PR otomatis | Optimasi stok berbasis prediksi (P7) |
| Asset | Register aset operasional (buggy, gym, sewa, raket, stik), maintenance schedule, usage history, spare part | Intelligence maintenance (P7). Penyusutan aset tetap dibangun di P4 (keputusan §16 #5) |
| Procurement | PR → approval → RFQ → quotation → PO → GR → return → vendor invoice → 3-way matching, vendor performance | Portal pemasok, e-procurement lanjutan *(belum di roadmap)* |
| Accounting | CoA, jurnal, GL, TB, posting otomatis, AR, AP, Cash & Bank, revenue & tax, e-Faktur, period closing, laporan keuangan per property | Budgeting, BI keuangan lanjutan (P5); konsolidasi multi-entitas *(belum di roadmap)* |
| Service charge | Pool dan kewajiban service charge di GL, dasar pembagian per departemen | Pembagian ke karyawan lewat payroll (P5) |
| Website | CMS penuh, news, gallery, banner, SEO, bilingual, tarif terstruktur | Personalisasi konten berbasis segmen (P5/P7) |
| Integration | Multi payment provider, Coretax, mutasi bank, adapter hardware yang tertunda di P2, email provider produksi | Integrasi HRIS/payroll pihak ketiga (P5); provisioning multi-customer (P6) |

## 5.3 Dependensi ke P0–P3

| Kapabilitas sebelumnya | Dipakai P4 untuk |
|---|---|
| Supplier foundation (P0 FR-MD-05; `procurement.suppliers` di repo) | Supplier Master penuh (EP-10) |
| Product & Outlet foundation (P0), POS (P2 EP-20), event `commercial.sale_completed` | Potong stok per outlet, COGS |
| Item, UOM, Recipe, Consumption Ledger (P2 EP-22) | Item master, konversi, consumption aktual (EP-01, EP-06) |
| Golf Cart lifecycle (P2 EP-07) | Asset register buggy, spare part (EP-09) |
| Revenue component & Accounting Export (P1 FR-INT-P1-04, P2 FR-BIL-P2-05, P3 FR-INT-P3-05) | Posting rule awal = mapping komponen export ke akun (EP-17) |
| Deferred revenue sub-ledger voucher & annual fee (P2 FR-BIL-P2-06), caddy fee settlement (P2 EP-05), honor instruktur (P2 EP-15) | Jurnal kewajiban & pengakuan (EP-21) |
| Kontrak P3 K1–K6 (PRD P3 §5.4.2) | PR banquet (K1), AR (K2), revenue allocation (K3), jurnal harian (K4), CMS (K5), potong stok paket/banquet (K6) |
| Invoice, payment schedule, corporate billing, business day (P3 EP-17–18) | AR ledger & aging (EP-18), cut-off jurnal harian |
| Loyalty liability (P3 FR-LOY-08), komisi sales (P3 EP-04) | Jurnal liability poin, beban komisi terutang |
| Approval (P0 EP-06), Audit log append-only (P0), Outbox (P0 EP-09) | Approval PR/PO/jurnal manual/closing; jejak audit keuangan; posting event-driven |
| Integration Layer + bridge agent (P0 FR-INT-07), payment gateway (P1) | EP-25 |
| Website `web` (P1–P3) | CMS (EP-24) |
| Reporting Foundation (P0 EP-10), read replica (Tech Doc §7.6) | Laporan keuangan & KPI |

## 5.4 Pengembangan Paralel dengan P3

P3 dan P4 adalah gelombang kedua yang berjalan bersamaan setelah Release 2 live (PRD P3 §5.4). Aturan kepemilikan per sub-package, `CODEOWNERS`, kontrak aditif, dan contract test berlaku. Tech Doc belum memuat §12.3 yang dirujuk PRD P2 ([bagian 6](#6-hasil-pengecekan-dokumen-sumber) #1).

### 5.4.1 Kepemilikan Module *(usulan)*

| Module (Tech Doc §4.1) | Pemilik gelombang 2 | Bagian P4 | Aturan |
|---|---|---|---|
| `inventory` | **P4** | EP-01–09 | Melanjutkan foundation P2 (item, UOM, recipe, consumption ledger); struktur recipe P2 tidak diubah, hanya diperluas |
| `procurement` | **P4** | EP-10–15 | Melanjutkan `procurement.suppliers` P0 |
| `accounting` (baru) | **P4** | EP-16–23 | Module & schema baru; review 2 orang (Tech Doc §13) |
| `cms` (baru) | **P4** | EP-24 | Module & schema baru; website `web` memakai API CMS + API publik P1–P3 |
| `platform/integration` | Bersama | Adapter baru EP-25 | Aditif; adapter per vendor |
| `billing`, `commercial`, `crm`, `banquet`, `golf` | P1–P3 | — | P4 **tidak mengubah**; hanya subscribe event dan memanggil interface publik |
| `reporting`, navigasi shell, `internal/app` | Bersama | Read model & widget P4 | Aditif saja |
| `web/apps/ops` (Warehouse), `web/apps/backoffice` (Inventory, Procurement, Accounting, CMS), `web/apps/web` | Bersama | Route folder per domain | Aditif per folder route |

**Arah dependensi.** Tech Doc §4.2 #3: lini bisnis → core → customer → back office → platform; arah sebaliknya hanya lewat domain event. Akibatnya:

- `accounting` **tidak dipanggil** oleh `billing`, `commercial`, atau `inventory`. Accounting men-subscribe event (Tech Doc §4.3: *accounting: buat jurnal*) dan membaca dokumen sumber lewat interface publik.
- `inventory` memotong stok dari event `commercial.sale_completed`, `commercial.package_consumed`, `banquet.event_completed`, dan `golf.round_finished` (BOM jasa), bukan dipanggil POS. Saldo stok yang perlu ditampilkan POS dibaca lewat read model.
- `procurement` → `inventory` (sesama back office) boleh memanggil interface publik untuk goods receipt.
- `cms` (kanal publik) hanya membaca API publik module lain; tidak menulis data domain.

### 5.4.2 Kontrak yang Dikonsumsi dan Disediakan

| # | Kontrak | Pemilik | Dipakai P4 untuk |
|---|---|---|---|
| K1 | Procurement requirement dari BEO & paket | P3 | Banquet material PR (EP-11) |
| K2 | Invoice & receivable (`billing.invoice_*`) | P3 | AR ledger & jurnal piutang (EP-18) |
| K3 | Revenue allocation per komponen | P3 (paket, banquet, promosi, poin), P1–P2 (all-in golf) | Jurnal pendapatan per komponen (EP-21) |
| K4 | `billing.business_day_closed` | P3 | Cut-off jurnal harian (EP-17) |
| K5 | Data terstruktur untuk CMS | P3 (event, promosi, paket), P1–P2 (tarif, course) | Halaman CMS memuat blok data (EP-24) |
| K6 | Consumption paket & banquet | P3 | Potong stok aktual (EP-06) |
| K7 *(usulan)* | `commercial.sale_completed` dengan baris produk & modifier | P2 | Potong stok POS (EP-06) |
| K8 *(usulan)* | Event keuangan P1–P2: `billing.payment_settled`, `billing.refund_processed`, `billing.folio_closed`, `commercial.voucher_sold/redeemed/expired`, `golf.caddy_settlement_approved`, `sportclub.instructor_fee_approved`, `membership.annual_fee_due` | P1–P2 | Posting otomatis (EP-17, EP-21) |
| K9 *(usulan, disediakan P4)* | **Stock availability read model** per outlet & produk | P4 | POS/paket P2–P3 dapat menandai produk habis tanpa memanggil `inventory` langsung |
| K10 *(usulan, disediakan P4)* | **Financial period status** (`accounting.period_closed`) | P4 | Billing P3 menolak koreksi bertanggal di periode tertutup |

Kontrak yang belum tersedia saat P4 mulai dikerjakan memakai mock, lalu diganti implementasi nyata setelah merge (urutan §5.4.3).

### 5.4.3 Urutan Kerja P4

```text
Tidak bergantung P3        → Item, lokasi, movement, requisition, transfer, opname, valuasi, supplier,
                             PR manual & reorder, RFQ, PO, GR, vendor invoice, 3-way matching,
                             CoA, jurnal, GL, AP, Cash & Bank, CMS halaman/berita/galeri
Butuh data P1–P2           → Posting otomatis billing/voucher/settlement (K8), potong stok POS (K7)
Butuh kontrak P3 (mock)    → PR banquet (K1), AR dari invoice (K2), revenue allocation paket (K3),
                             cut-off business day (K4), potong stok paket/banquet (K6), blok CMS P3 (K5)
```

---

# 6. Hasil Pengecekan Dokumen Sumber

Pengecekan silang roadmap, Product Overview, Naming Convention, Technical Documentation, PRD P0–P3, dan kode `oneclub` (branch `staging`, commit `a48e6a3`) menemukan hal berikut. Keputusan di kolom kanan berlaku untuk PRD ini sampai dikonfirmasi di [Open Questions](#16-open-questions).

| # | Temuan | Sumber | Keputusan PRD P4 |
|---|---|---|---|
| 1 | **Tech Doc §12.3 tidak ada**, padahal PRD P2 dan P3 merujuk aturan gelombang di sana | PRD P2 §5.4; PRD P3 §6 #1; Tech Doc | Aturan gelombang memakai PRD P3 §5.4 dan §5.4 PRD ini. Tech Doc perlu menambahkan §12.3. **Disetujui** (§16 #1) |
| 2 | **Fase Product Overview berbeda dengan roadmap.** PO §49 menaruh Inventory, Procurement, Accounting di Fase 3 bersama SaaS; roadmap memecahnya ke P4 (back office) dan P6 (SaaS) | PO §49; roadmap §3, §63 | Mengikuti **roadmap**: P4 = back office + digital channel + integration layer |
| 3 | **Phase Map roadmap §3 tidak menyebut CMS/Landing Page di P4**, tetapi §53, §66.1, dan §67 menaruh Full CMS di P4 | Roadmap §3 vs §53, §66.1, §67 | **CMS di P4** (EP-24) |
| 4 | **Integration Layer §54 sebagian sudah dibangun**: payment gateway (Xendit), WhatsApp Cloud API, email, data residen (Should), migrasi Rhapsody di P1; hardware bridge di P2 (Should) | Roadmap §54; PRD P1 EP-17, EP-18; PRD P2 EP-30; repo | P4 = **penyelesaian & produksi**: multi payment provider, Coretax, mutasi bank, adapter hardware yang tertunda, email provider produksi. Yang sudah ada tidak dibangun ulang |
| 5 | **Accounts Receivable** di P3 (invoice & aging operasional, PRD P3 §6 #6) dan di P4 Accounting | Roadmap §49 vs §52; PRD P3 §5.2 | **Satu dokumen invoice** milik `billing` (P3). P4 membangun AR **ledger**, jurnal, kontrol akun, penyisihan, dan e-Faktur dari invoice tersebut (K2). Aging operasional P3 dan AR Aging P4 harus sama angkanya |
| 6 | **Distribusi service charge ke karyawan** (PO §37) butuh HRIS/payroll P5 | PO §37; roadmap §52, §58 | P4: pool service charge & kewajiban di GL, dasar pembagian per departemen; pembayaran ke karyawan di P5 |
| 7 | **Penyusutan aset tetap** tidak tercantum di Accounting roadmap §52 maupun Inventory §50 (Asset & Equipment hanya operasional) | Roadmap §50, §52; PO §35, §37 | Asset register operasional dan penyusutan aset tetap **Must** (keputusan §16 #5) |
| 8 | **NC §29 Warehouse memakai "Receiving"**, padahal NC §30 meminta "Goods Receipt" dan melarang "Receiving" | NC §29 vs §30 | Label **Goods Receipt** di Back Office dan `ops`; usulan perbaikan NC §29 di §7.6 |
| 9 | **Batas BOM P2/P4**: P2 membangun recipe & theoretical cost; potong stok, actual consumption, production, requisition di P4 | PRD P2 §6 #1, EP-22 | Dipertahankan. P4 memperluas item P2 menjadi item master penuh tanpa migrasi destruktif |
| 10 | **Konsinyasi pro shop** belum diputuskan (PO §35, roadmap §74 #9; PRD P2 OQ #11) | PO §35; roadmap §74 | Item konsinyasi **Must** *(asumsi §16 #4: konsinyasi ada)*: stok milik pemasok, AP timbul saat terjual, settlement bulanan |
| 11 | **Payment Reconciliation (P1)** adalah rekonsiliasi gateway vs settlement, berbeda dengan **Bank Reconciliation** (P4) | PRD P1 EP-12; roadmap §52 | Keduanya ada: gateway settlement (P1) mengalir ke mutasi bank yang dicocokkan di P4 |
| 12 | **Jurnal append-only** sudah ditetapkan Tech Doc §7.4 (trigger & grant, koreksi via reversal) | Tech Doc §7.4 | Wajib (FR-ACC-03) |
| 13 | **Pajak**: P0 test memakai PB1 (pajak restoran) di samping PPN; PO menyebut 11% golf dan 15,5% tax & service banquet/meeting | Repo `TestTaxServiceEffectiveDate`; PO §31, §37 | Tax Configuration memetakan setiap aturan tax & service P0 ke akun pajak/service; jenis pajak (PPN, PB1) dikonfirmasi (OQ #7) |
| 14 | **Multi-property** (MAIN, MDR di demo) belum diatur untuk akuntansi | P0 org; repo demo | Jurnal per property; laporan **gabungan** MAIN + MDR **Must** (keputusan §16 #6), tanpa eliminasi karena diasumsikan satu badan hukum |
| 15 | **Kode saat ini**: `procurement.suppliers` (P0) dan `billing.accounting_exports` (P1, CSV harian) ada; module `inventory`, `accounting`, `cms` belum ada; P2 dan P3 masih PRD | Repo `oneclub` | P4 dimulai setelah Release 2; posting rule awal memakai komponen Accounting Export P1–P3 agar hasil parallel run dapat dibandingkan |
| 16 | **Data Entity Rollout §70 (P4)** memuat "Quotation" (vendor) dan "Invoice", yang juga nama dokumen P3 (sales quotation, customer invoice) | Roadmap §70 | Nama entitas P4: **Vendor Quotation**, **Vendor Invoice**; label UI sesuai NC §20 |
| 17 | **Production** dan **Store Requisition** ada di Inventory NC §19, tetapi P2 menyebut Kitchen interface tanpa requisition | NC §19; PRD P2 §7.2 | Requisition dan production dibuat dari Back Office dan `ops` Warehouse/Kitchen (EP-26) |
| 18 | **Status P4 belum ada di NC §31** (requisition, PO, GR, matching, jurnal, periode) | NC §31 | Usulan status baru di §7.6 |
| 19 | **Inventory Configuration / Procurement & Accounting policy** belum punya label di NC §25/§33 | NC §25, §33 | Memakai pola NC §33 (*[Object] Configuration*, *[Object] Policy*); usulan di §7.6 |

---

# 7. Information Architecture

Label mengikuti `OneClub — Naming Convention.md`. Module dan menu P4 muncul hanya bila module aktif (P0 FR-INS-04) dan user berizin.

## 7.1 Back Office — Module & Menu Baru/Aktif di P4

| Module | Menu baru di P4 | Catatan |
|---|---|---|
| Inventory | **Items, Categories, UOM, Warehouses, Stock Locations, Stock Balance, Stock Movement, Store Requisition, Stock Transfer, Stock Adjustment, Stock Opname, Stock Valuation, Par Stock, Reorder Point, BOM & Recipes, Production, Assets & Equipment, Inventory Reports** | NC §19; melengkapi foundation P2 |
| Procurement | **Suppliers, Purchase Requisitions, Approvals, RFQ, Vendor Quotations, Purchase Orders, Goods Receipts, Purchase Returns, Vendor Invoices, Vendor Performance, Procurement Reports** | NC §20 |
| Accounting | **General Ledger, Accounts Receivable, Accounts Payable, Cash & Bank, Revenue & Tax, Financial Periods, Closing, Financial Reports** | NC §21; module baru aktif |
| Billing & Payment | Tidak ada menu baru; Invoices menampilkan status e-Faktur | |
| Reports | **Inventory Reports, Procurement Reports, Financial Reports** | NC §23 |
| Dashboard | **Inventory Performance, Procurement Performance, Financial Performance** | NC §23 |
| Settings | Tax & Service dipetakan ke akun; **Inventory Configuration, Procurement Configuration, Accounting Configuration** *(usulan)* | NC §33 |
| CMS *(usulan posisi menu)* | **Pages, Banners, Images, Packages, Promotions, Pricing, Events, News, Gallery, Course Guide, Contact Information, Languages** | NC §26 CMS; module `cms` di sidebar Back Office (usulan §7.6) |

## 7.2 Operational Staff (`ops`)

| Interface | Menu (NC §29 + usulan) | Pengguna |
|---|---|---|
| Warehouse | Stock Balance, **Goods Receipt** *(NC §29: Receiving)*, Issuing, Transfer, Stock Opname, Store Requisition | Warehouse Staff |
| Kitchen | + Store Requisition, Production, Waste *(usulan)* | Kitchen Staff |
| Outlet (POS) | + Store Requisition, Stock Balance outlet, Waste *(usulan)* | Outlet Manager, POS Staff |
| Golf Staff | + Spare Part Request *(usulan)* | Golf Staff |

## 7.3 Member & Guest Portal (`member`) dan Caddy Application (`caddy`)

Tidak ada menu baru. Invoice yang memiliki faktur pajak menampilkan nomor faktur.

## 7.4 Website (`web`)

Public navigation lengkap NC §26: **Home, Golf, Sport Club, Bungalow, VIP Suite, Meeting & MICE, Wedding & Banquet, Events, Membership, Packages, Promotions, Hall of Fame, News, Gallery, Contact, Location**. Halaman dapat dikelola lewat CMS; blok tarif, paket, promosi, event, dan hall of fame tetap diambil dari data terstruktur.

## 7.5 Struktur Inventory (contoh Modern Golf)

```text
Main Store
├── Kitchen (Spike Bar, pool, banquet)
├── Bar & Beverage
├── Pro Shop
├── Golf Ops (bola range, air mineral, tee, scorecard)
├── Sport Club (shuttlecock, bola, kimia kolam, amenities spa)
├── Bungalow (amenities, linen)
├── Engineering / Maintenance (sparepart buggy)
└── General
```

## 7.6 Usulan Tambahan ke Naming Convention

| Usulan label | Lokasi | Alasan |
|---|---|---|
| Goods Receipt (ganti "Receiving") | NC §29 Warehouse | Konsisten dengan NC §30 ([bagian 6](#6-hasil-pengecekan-dokumen-sumber) #8) |
| CMS (module di sidebar Back Office) | Admin Dashboard NC §5 | NC §26 punya menu CMS tetapi tidak ada module induk di Back Office |
| Inventory Configuration, Procurement Configuration, Accounting Configuration | Settings | Pola NC §33 |
| Procurement Policies, Inventory Policies | Settings → Club Policies | Ambang approval, toleransi matching, toleransi opname |
| Posting Rules | Accounting → Revenue & Tax | Pemetaan event operasional ke akun |
| Spare Part Request, Waste | `ops` | Interface baru (§7.2) |
| Status **Submitted**, **Partially Ordered**, **Ordered** | NC §31 | Purchase Requisition |
| Status **Sent**, **Partially Received**, **Received**, **Closed** | NC §31 | Purchase Order |
| Status **Matched**, **Mismatch**, **On Hold**, **Partially Paid**, **Paid** | NC §31 | Vendor Invoice & 3-way matching |
| Status **Posted**, **Reversed** | NC §31 | Journal |
| Status **Open**, **Soft Closed**, **Closed** | NC §31 | Financial Period |
| Status **In Progress**, **Counted**, **Posted** | NC §31 | Stock Opname |
| Status **Published**, **Scheduled**, **Unpublished** | NC §31 | Konten CMS |
| Status **In Transit** | NC §31 | Stock Transfer |

---

# 8. Functional Requirements

Format sama dengan PRD P0–P3. Prioritas: **Must** = wajib untuk go-live Release 4, **Should** = sebaiknya ada di P4, boleh bergeser ke awal P5 bila club setuju. Prefix ID baru dipakai agar tidak bentrok; area yang memperluas fase sebelumnya memakai akhiran `-P4-`.

## EP-01 — Item Master & Inventory Setup

**User story:** Sebagai Inventory Manager, saya ingin satu master barang untuk dapur, bar, pro shop, golf ops, sport club, bungalow, dan engineering, dengan satuan beli dan pakai yang benar.

Cakupan (roadmap §50): F&B, Golf, Pro Shop, Sport Club, Bungalow, Event / Banquet, Maintenance, General Supplies.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-INV-01 | **Items** (melanjutkan item bahan P2): kode, nama, kategori, tipe (stock, non-stock, service, asset, consumable), barcode, item pemasok, akun persediaan/HPP/beban per kategori, status | Must |
| FR-INV-02 | **Categories** bertingkat dengan default akun dan metode valuasi | Must |
| FR-INV-03 | **UOM & Conversion** beli → stok → pakai (karton → botol; kg → gram; porsi) melanjutkan P2; konversi per item | Must |
| FR-INV-04 | **Warehouses & Stock Locations** sesuai struktur §7.5; lokasi bertipe store, outlet, dapur, transit, karantina | Must |
| FR-INV-05 | **Produk jual ↔ item stok**: produk POS retail (pro shop) memetakan 1:1 ke item; menu F&B lewat recipe P2 | Must |
| FR-INV-06 | Tracking per item: **batch**, **serial number**, **expiry** (opsional per item) | Must |
| FR-INV-07 | **Item konsinyasi** pro shop (milik pemasok sampai terjual), settlement bulanan dengan komisi club per vendor *(asumsi §16 #4)* | Must |
| FR-INV-08 | Import item, lokasi, dan saldo awal lewat Master Data Import P0 | Must |

## EP-02 — Stock Movement & Balance

| ID | Requirement | Prioritas |
|---|---|---|
| FR-STK-01 | **Stock Movement** append-only dengan tipe: Goods Receipt, Issue, Transfer Out/In, Adjustment, Consumption (penjualan/BOM), Production In/Out, Waste, Return to Supplier, Opname Adjustment | Must |
| FR-STK-02 | **Stock Balance** per item × lokasi (× batch) real-time, dengan nilai | Must |
| FR-STK-03 | **Issue Stock** ke departemen/cost center (mis. engineering, housekeeping bungalow) dengan alasan | Must |
| FR-STK-04 | Stok negatif dilarang per lokasi kecuali dikonfigurasi untuk outlet tertentu dengan penandaan untuk review *(usulan)* | Must |
| FR-STK-05 | Movement bertanggal di periode akuntansi tertutup ditolak (K10) | Must |
| FR-STK-06 | Setiap movement bernilai menghasilkan event `inventory.stock_moved` untuk jurnal (Tech Doc §4.3) | Must |
| FR-STK-07 | FEFO (first expired first out) saran pengambilan untuk item ber-expiry | Should |

**Acceptance criteria:**

- Saldo stok per lokasi = Σ movement lokasi tersebut; tidak ada movement yang dapat diubah atau dihapus (koreksi lewat movement baru).

## EP-03 — Store Requisition & Stock Transfer

| ID | Requirement | Prioritas |
|---|---|---|
| FR-REQ-01 | **Store Requisition** outlet/dapur → main store: item, jumlah, tanggal butuh; approval sesuai Inventory Policies | Must |
| FR-REQ-02 | Pemenuhan parsial; sisa tetap terbuka atau dibatalkan; requisition yang tidak dapat dipenuhi dapat menjadi PR (EP-11) | Must |
| FR-REQ-03 | **Stock Transfer** antar lokasi dengan status **In Transit** sampai diterima; selisih kirim-terima dicatat | Must |
| FR-REQ-04 | Requisition dari `ops` Kitchen/Outlet dengan pemindaian barcode | Must |

## EP-04 — Stock Opname & Adjustment

| ID | Requirement | Prioritas |
|---|---|---|
| FR-OPN-01 | **Stock Opname** per lokasi/kategori: snapshot sistem, lembar hitung (blind count opsional), input di `ops` dengan barcode, status In Progress → Counted → Posted | Must |
| FR-OPN-02 | **Variance** jumlah & nilai per item; variance di atas toleransi memerlukan hitung ulang atau approval | Must |
| FR-OPN-03 | **Stock Adjustment** manual dengan alasan & approval | Must |
| FR-OPN-04 | Pembekuan movement lokasi selama opname, atau cut-off waktu yang dicatat | Must |
| FR-OPN-05 | Opname dapat dilakukan offline di `ops` dan tersinkron idempotent *(usulan)* | Should |

**Acceptance criteria:**

- Sistem 120 botol, hitung 117: setelah approval terbentuk adjustment −3 dengan nilai sesuai average cost dan jurnal selisih persediaan.

## EP-05 — Stock Valuation, COGS & Inventory Journal

| ID | Requirement | Prioritas |
|---|---|---|
| FR-VAL-01 | Metode valuasi **Moving Average** dan **FIFO** per kategori/item | Must |
| FR-VAL-02 | **COGS** dihitung saat consumption/issue memakai biaya valuasi, bukan standard cost P2; standard cost tetap dipakai untuk food cost teoretis | Must |
| FR-VAL-03 | **Landed cost** sederhana (ongkir, bea) dialokasikan ke item GR *(usulan)* | Should |
| FR-VAL-04 | **Inventory Journal** otomatis per movement bernilai (persediaan, HPP, selisih, waste) lewat posting rule EP-17 | Must |
| FR-VAL-05 | **Stock Valuation Report** per tanggal (as-of) yang sama dengan saldo akun persediaan di GL | Must |
| FR-VAL-06 | Revaluasi bila harga vendor invoice berbeda dari PO setelah barang dipakai: selisih ke HPP/selisih harga sesuai konfigurasi | Must |

**Acceptance criteria:**

- Terima 10 kg @ Rp100.000 lalu 10 kg @ Rp120.000: average Rp110.000/kg; issue 5 kg mencatat COGS Rp550.000.
- Nilai Stock Valuation Report akhir bulan = saldo akun persediaan GL per property.

## EP-06 — Consumption Otomatis (POS, BOM, Banquet, Package)

**User story:** Sebagai Finance Manager, saya ingin stok terpotong otomatis setiap kali menu terjual, event selesai, atau paket dipakai, agar food cost aktual dan persediaan selalu benar.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-CNS-01 | **POS**: `commercial.sale_completed` (K7) memotong stok lokasi outlet sesuai recipe P2 termasuk modifier, combo, dan sub-recipe; retail pro shop 1:1 | Must |
| FR-CNS-02 | **Void / refund POS** membalik consumption bila barang belum dibuat/dikembalikan sesuai konfigurasi | Must |
| FR-CNS-03 | **Banquet**: `banquet.event_completed` (K6) memotong stok dapur banquet dari BOM per pax final | Must |
| FR-CNS-04 | **Package & jasa**: `commercial.package_consumed` (K6) dan BOM jasa P2 (mis. air mineral per round golf, coffee break meeting) | Must |
| FR-CNS-05 | Proses idempotent per event (`event_id`); event terlambat (POS offline) diposting ke business day transaksinya bila periode masih terbuka | Must |
| FR-CNS-06 | **Theoretical vs Actual**: consumption teoretis (P2 ledger) vs pemakaian aktual (opening + receipt − closing − transfer) per outlet per periode | Must |
| FR-CNS-07 | Produk habis ditandai di POS lewat stock availability read model (K9) | Should |

**Acceptance criteria:**

- 100 round golf dengan BOM 1 botol air mineral memotong 100 botol di lokasi Golf Ops (dari stok karton 24 botol = 4 karton + 4 botol).
- Event POS yang sama diterima dua kali hanya memotong stok sekali.

## EP-07 — Production, Waste, Batch, Serial & Expiry

| ID | Requirement | Prioritas |
|---|---|---|
| FR-PRD-01 | **Production Order** untuk semi-finished (sambal, kaldu, saus): bahan keluar, hasil masuk dengan yield aktual; biaya hasil dari biaya bahan | Must |
| FR-PRD-02 | **Record Waste / Spoilage** dengan alasan (kedaluwarsa, rusak, salah masak) per lokasi | Must |
| FR-PRD-03 | **Expiry** per batch dengan peringatan H-N dan laporan barang mendekati kedaluwarsa | Must |
| FR-PRD-04 | **Serial number** untuk barang bernilai (stik, raket, elektronik pro shop) dari GR sampai terjual | Must |
| FR-PRD-05 | Banquet production schedule dari BEO (P3 FR-BEO-04) membuat production order terjadwal *(usulan)* | Should |

## EP-08 — Replenishment & Automatic PR

| ID | Requirement | Prioritas |
|---|---|---|
| FR-RPL-01 | **Par Stock**, **Minimum Stock**, **Reorder Point** per item × lokasi | Must |
| FR-RPL-02 | Job harian membuat **PR otomatis** untuk item di bawah reorder point sebesar (par − stok − on order) | Must |
| FR-RPL-03 | Saran **requisition outlet** ke main store saat stok outlet di bawah par | Must |
| FR-RPL-04 | Laporan **Low Stock** dan item tanpa pergerakan (slow moving) | Must |

**Acceptance criteria:**

- Par 50, reorder point 20, stok 18, on order 0: PR otomatis 32 unit terbentuk sekali per hari dan tidak terduplikasi bila job berjalan ulang.

## EP-09 — Asset & Equipment

Boundary: aset operasional dan equipment; resort engineering di luar buggy & peralatan Out of Scope (roadmap §73).

| ID | Requirement | Prioritas |
|---|---|---|
| FR-AST-01 | **Asset Register**: buggy fleet (terhubung golf cart P2), alat gym, peralatan sewa (stik, raket), equipment meeting/banquet; kode, lokasi, nilai perolehan, tanggal, pemasok, garansi, status | Must |
| FR-AST-02 | **Maintenance Schedule** per aset (berbasis waktu atau jam pakai dari usage P2) dengan pengingat | Must |
| FR-AST-03 | **Usage History** dan riwayat maintenance; spare part yang dipakai mengurangi stok Engineering | Must |
| FR-AST-04 | **Rental equipment** (stik, raket) sebagai aset yang dapat dipinjam/disewa dengan status keluar/kembali *(usulan)* | Should |
| FR-AST-05 | **Penyusutan** aset tetap di OneClub (default garis lurus, saldo menurun configurable) dengan jurnal otomatis bulanan (keputusan §16 #5) | Must |
| FR-AST-06 | Disposal/write-off aset lewat approval | Must |

## EP-10 — Supplier Management & Vendor Performance

| ID | Requirement | Prioritas |
|---|---|---|
| FR-SUP-01 | **Suppliers** (melanjutkan P0): NPWP, alamat, kontak, rekening bank (masked, permission), termin pembayaran, kategori pasokan, status PKP | Must |
| FR-SUP-02 | **Item pemasok**: kode pemasok, harga terakhir, lead time, minimum order | Must |
| FR-SUP-03 | **Vendor Performance**: ketepatan waktu, kesesuaian jumlah/kualitas GR, selisih harga, return rate | Must |
| FR-SUP-04 | Blacklist / non-aktif pemasok dengan alasan dan approval | Must |

## EP-11 — Purchase Requisition & Approval

| ID | Requirement | Prioritas |
|---|---|---|
| FR-PR-01 | **Purchase Requisition** sumber: **Manual**, **Reorder-based** (EP-08), **Banquet material** dari procurement requirement BEO (K1), requisition outlet yang tidak terpenuhi (EP-03) | Must |
| FR-PR-02 | PR berisi item/jasa, jumlah, tanggal butuh, lokasi tujuan, cost center, estimasi harga; status Draft → Submitted → Approved / Rejected → Partially Ordered → Ordered | Must |
| FR-PR-03 | **Approval matrix** berdasarkan nilai, kategori, dan cost center memakai approval engine P0 | Must |
| FR-PR-04 | PR banquet tertaut ke event; revisi BEO (`banquet.beo_revised`) memperbarui PR yang belum dipesan dan menandai PR yang sudah dipesan | Must |
| FR-PR-05 | Konsolidasi beberapa PR menjadi satu RFQ/PO | Must |

**Acceptance criteria:**

- BEO wedding 300 pax menghasilkan PR bahan sesuai BOM per pax; revisi menjadi 320 pax memperbarui jumlah PR yang belum menjadi PO.

## EP-12 — RFQ & Vendor Quotation

| ID | Requirement | Prioritas |
|---|---|---|
| FR-RFQ-01 | **RFQ** ke beberapa pemasok dari PR (PDF/email) dengan batas waktu | Must |
| FR-RFQ-02 | **Record Vendor Quotation**: harga, diskon, PPN, lead time, termin, masa berlaku | Must |
| FR-RFQ-03 | **Perbandingan** quotation per item dan pemilihan pemasok dengan alasan bila bukan termurah | Must |
| FR-RFQ-04 | RFQ dapat dilewati untuk pembelian di bawah ambang atau pemasok kontrak (Procurement Policies) | Must |

## EP-13 — Purchase Order

| ID | Requirement | Prioritas |
|---|---|---|
| FR-PO-01 | **Purchase Order** dari PR/quotation atau langsung (sesuai policy): pemasok, item, harga, PPN, termin, tanggal kirim, lokasi terima | Must |
| FR-PO-02 | **Approve Purchase Order** sesuai matrix; PO terkirim (PDF/email) dengan status Sent | Must |
| FR-PO-03 | Revisi PO berversi; pembatalan baris yang belum diterima | Must |
| FR-PO-04 | Status Partially Received → Received → Closed; **Outstanding PO** per pemasok | Must |
| FR-PO-05 | PO jasa (tanpa stok) untuk vendor event, maintenance, dan jasa lain | Must |

## EP-14 — Goods Receipt & Purchase Return

| ID | Requirement | Prioritas |
|---|---|---|
| FR-GR-01 | **Receive Goods** terhadap PO (parsial/penuh), batch/expiry/serial, foto/dokumen surat jalan; barang masuk ke lokasi tujuan | Must |
| FR-GR-02 | Penerimaan di atas PO ditolak atau memerlukan approval sesuai toleransi | Must |
| FR-GR-03 | Penerimaan barang tanpa PO hanya untuk kategori yang diizinkan, dengan approval | Must |
| FR-GR-04 | **Purchase Return** ke pemasok dengan alasan; mengurangi stok dan membuat debit note | Must |
| FR-GR-05 | GR menghasilkan jurnal persediaan / barang diterima belum ditagih (GRNI) | Must |

## EP-15 — Vendor Invoice & 3-Way Matching

| ID | Requirement | Prioritas |
|---|---|---|
| FR-VIN-01 | **Record Vendor Invoice**: nomor, tanggal, jatuh tempo, faktur pajak masukan, baris tertaut PO/GR | Must |
| FR-VIN-02 | **Perform 3-Way Matching** PO + GR + Vendor Invoice per baris (jumlah & harga) dengan toleransi (Procurement Policies) | Must |
| FR-VIN-03 | Status **Matched** → siap bayar; **Mismatch** → **On Hold** sampai diselesaikan (koreksi, debit note, atau approval override) | Must |
| FR-VIN-04 | Invoice jasa tanpa GR memakai 2-way matching (PO + invoice) dengan konfirmasi penerima jasa | Must |
| FR-VIN-05 | Matched invoice masuk Accounts Payable (EP-19) dan menutup GRNI | Must |

**Acceptance criteria:**

- PO 100 unit @ Rp10.000, GR 95 unit, invoice 100 unit: invoice berstatus Mismatch/On Hold; invoice 95 unit menjadi Matched dan AP Rp950.000 + PPN.

## EP-16 — General Accounting

**User story:** Sebagai Accountant, saya ingin buku besar OneClub dengan bagan akun club, jurnal yang tidak bisa diubah, dan penutupan periode yang terkendali.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-ACC-01 | **Chart of Accounts** bertingkat (aset, kewajiban, ekuitas, pendapatan, beban) dengan dimensi **property**, **business line**, **department / cost center** *(usulan dimensi)* | Must |
| FR-ACC-02 | **Journal** manual dan otomatis; jurnal manual memerlukan approval di atas ambang | Must |
| FR-ACC-03 | Jurnal **append-only**: tidak ada UPDATE/DELETE; koreksi lewat **reversal entry** (Tech Doc §7.4); status Posted, Reversed | Must |
| FR-ACC-04 | Setiap jurnal seimbang (debit = kredit) per property; ditegakkan di database | Must |
| FR-ACC-05 | **General Ledger** dan **Trial Balance** per periode/tanggal, per property, dengan drill-down ke jurnal dan dokumen sumber | Must |
| FR-ACC-06 | **Financial Period**: Open, Soft Closed (hanya jurnal penyesuaian finance), Closed; **Period Closing** dengan checklist (business day ditutup, opname diposting, rekonsiliasi bank, matching) | Must |
| FR-ACC-07 | **Year-end closing** ke laba ditahan | Must |
| FR-ACC-08 | Jurnal berulang (accrual bulanan) dan jurnal pembalik otomatis awal periode *(usulan)* | Should |
| FR-ACC-09 | Audit trail: pembuat, approver, sumber, waktu; akses baca untuk auditor | Must |

**Acceptance criteria:**

- Jurnal yang tidak seimbang ditolak database; jurnal Posted tidak dapat diubah, hanya dibalik.
- Jurnal bertanggal di periode Closed ditolak untuk semua sumber, termasuk event operasional terlambat (masuk periode terbuka berikutnya sesuai Accounting Configuration).

## EP-17 — Posting Otomatis

**User story:** Sebagai Finance Manager, saya ingin semua transaksi operasional langsung menjadi jurnal dengan aturan yang bisa saya atur, agar Accounting Export dan input ulang tidak diperlukan.

Pola Tech Doc §4.3: accounting men-subscribe event outbox (at-least-once, idempotent per `event_id`).

| ID | Requirement | Prioritas |
|---|---|---|
| FR-PST-01 | **Posting Rules** configurable: event + kondisi (lini, revenue component, metode bayar, property) → akun debit/kredit dan dimensi | Must |
| FR-PST-02 | Sumber posting minimal: billing (charge, payment, refund, deposit/DP, adjustment, invoice, credit note, write-off), voucher & prepaid (jual, redeem, breakage), loyalty (earn, redeem, expire), membership fee, caddy fee settlement, honor instruktur, komisi sales, POS shift, inventory (receipt, consumption, transfer, adjustment, waste, production), procurement (GR, vendor invoice, return), AP payment, bank | Must |
| FR-PST-03 | **Mode posting** per sumber: per transaksi atau ringkasan per business day (K4) *(usulan default: ringkasan harian untuk POS & charge, per dokumen untuk invoice/AP)* | Must |
| FR-PST-04 | Event tanpa rule yang cocok masuk **suspense** dan antrean exception; tidak ada event yang hilang | Must |
| FR-PST-05 | Idempotent: event yang sama tidak menghasilkan jurnal ganda; replay aman | Must |
| FR-PST-06 | Rule berversi dengan effective date; perubahan rule tidak mengubah jurnal lama | Must |
| FR-PST-07 | **Rekonsiliasi posting**: per business day, total jurnal per komponen = total Accounting Export/Daily Revenue Report P3 | Must |
| FR-PST-08 | Default posting rule awal di-generate dari komponen Accounting Export P1–P3 untuk rekonsiliasi transisi (EP-23) | Must |

**Acceptance criteria:**

- Pembayaran member rate golf all-in Rp640.000 menghasilkan jurnal: debit kas/bank Rp640.000; kredit PPN keluaran, pendapatan green fee, pendapatan buggy, HIO, dan **kewajiban caddy fee** sesuai komponen snapshot, dengan total kredit Rp640.000.
- Event `billing.payment_settled` yang diterima tiga kali menghasilkan satu jurnal.
- Total jurnal per komponen untuk satu business day sama dengan Daily Revenue Report P3 hari yang sama.

## EP-18 — Accounts Receivable

| ID | Requirement | Prioritas |
|---|---|---|
| FR-AR-01 | **AR ledger** dari invoice P3 (K2): Customer Invoices, **Member Billing** (member account/signing bill & statement P1–P2), **Corporate Billing** | Must |
| FR-AR-02 | **Receivables** per customer/corporate dengan saldo, termin, dan akun kontrol | Must |
| FR-AR-03 | **Payments** dialokasikan ke invoice (dari billing P3) tercermin di AR; uang muka/unapplied cash tercatat | Must |
| FR-AR-04 | **AR Aging** (0–30, 31–60, 61–90, > 90) yang angkanya sama dengan aging operasional P3 | Must |
| FR-AR-05 | Penyisihan piutang tak tertagih dan write-off lewat approval dengan jurnal | Should |
| FR-AR-06 | Rekonsiliasi saldo akun kontrol AR = Σ saldo sub-ledger | Must |

**Acceptance criteria:**

- Saldo akun kontrol AR akhir bulan = Σ invoice terbuka P3 = total AR Aging Report.

## EP-19 — Accounts Payable

| ID | Requirement | Prioritas |
|---|---|---|
| FR-AP-01 | **Payables** dari vendor invoice Matched (EP-15), vendor jasa, komisi/settlement yang dibayar lewat AP (caddy fee bila dibayar sebagai vendor, sesuai OQ P2) | Must |
| FR-AP-02 | **Payment run**: pilih invoice jatuh tempo, approval, pembayaran (transfer/cek/kas), bukti; pembayaran parsial | Must |
| FR-AP-03 | **AP Aging** per pemasok | Must |
| FR-AP-04 | Debit note/purchase return mengurangi utang | Must |
| FR-AP-05 | **PPh potong/pungut** pada pembayaran vendor jasa *(usulan, OQ #7)* | Should |
| FR-AP-06 | File pembayaran bank massal (format bank) *(usulan)* | Should |

## EP-20 — Cash & Bank

| ID | Requirement | Prioritas |
|---|---|---|
| FR-BNK-01 | **Cash Accounts** (kas kecil per outlet/kasir) dan **Bank Accounts** per property | Must |
| FR-BNK-02 | **Bank Transactions**: impor mutasi (CSV/MT940) atau tarik lewat API bank bila tersedia (EP-25) | Must |
| FR-BNK-03 | **Bank Reconciliation**: auto-match mutasi dengan pembayaran, settlement gateway (P1 payment reconciliation), setoran kas shift (P2–P3), pembayaran AP; selisih diselesaikan dengan jurnal | Must |
| FR-BNK-04 | **Setoran kas** dari shift kasir (P2 POS, P3 cashier shift) ke bank dengan selisih tercatat | Must |
| FR-BNK-05 | Kas kecil (petty cash) dengan pengeluaran dan pengisian ulang *(usulan)* | Should |

**Acceptance criteria:**

- Settlement gateway harian (P1) dan setoran kas shift ter-match otomatis dengan mutasi bank pada tanggal yang sama; sisa tidak ter-match tampil sebagai exception.

## EP-21 — Revenue & Tax

| ID | Requirement | Prioritas |
|---|---|---|
| FR-REV-01 | **Revenue Allocation** dari harga bundling (green fee, caddy fee, buggy fee, HIO, pajak) melanjutkan komponen P1, paket & banquet P3 (K3); total alokasi = harga | Must |
| FR-REV-02 | **Deferred Revenue**: membership fee/annual fee dibayar di muka (diakui bulanan), voucher & prepaid (diakui saat dipakai), deposit/DP (diakui saat event/layanan), poin loyalty | Must |
| FR-REV-03 | **Voucher Breakage** saat kedaluwarsa (P2 FR-VCH-08) dan poin kedaluwarsa (P3) | Must |
| FR-REV-04 | **Tax Configuration**: aturan tax & service P0 dipetakan ke akun pajak keluaran/masukan dan service charge; jenis pajak PPN & PB1 sesuai konfigurasi | Must |
| FR-REV-05 | **Caddy Fee Liability**: titipan caddy dari setiap round, pengurangan saat settlement P2 dibayar; saldo = sub-ledger settlement | Must |
| FR-REV-06 | **Service Charge**: pool service charge per periode dan dasar pembagian per departemen; pembayaran ke karyawan di P5 | Must |
| FR-REV-07 | **e-Faktur / Coretax**: faktur pajak keluaran dari invoice berpajak (P3) untuk customer ber-NPWP, nomor seri, status upload, pembatalan/pengganti; faktur pajak masukan dari vendor invoice | Must |
| FR-REV-08 | Laporan PPN keluaran/masukan per masa pajak | Must |

**Acceptance criteria:**

- Voucher 5x entry Rp635.000 (P2): jurnal penjualan ke kewajiban; setiap redemption mengakui Rp127.000; kedaluwarsa dengan sisa 2 kuota mengakui breakage Rp254.000.
- DP wedding Rp26.400.000 (P3) tercatat sebagai kewajiban deposit dan dipindah ke pendapatan saat event Completed.
- Saldo GL caddy fee liability = saldo sub-ledger caddy settlement P2 setiap akhir hari.

## EP-22 — Financial Reports

| ID | Requirement | Prioritas |
|---|---|---|
| FR-FIN-01 | **Profit & Loss**, **Balance Sheet**, **Cash Flow** (metode tidak langsung *(usulan)*), **Trial Balance**, **AR Aging**, **AP Aging**, **Revenue by Business Line** | Must |
| FR-FIN-02 | Per property dan periode; perbandingan periode sebelumnya/tahun lalu | Must |
| FR-FIN-03 | **Laporan gabungan** MAIN + MDR dalam satu instance, tetap dapat difilter per property; tanpa eliminasi antar perusahaan (satu badan hukum, asumsi §16 #6) | Must |
| FR-FIN-04 | Drill-down dari angka laporan ke jurnal dan dokumen sumber | Must |
| FR-FIN-05 | Export XLSX/PDF; laporan berjalan di read replica (Tech Doc §7.6) | Must |
| FR-FIN-06 | Format laporan mengikuti struktur yang disepakati club/auditor | Must |

**Acceptance criteria:**

- Balance Sheet seimbang di setiap tanggal; laba bersih P&L periode = perubahan laba berjalan di Balance Sheet.

## EP-23 — Accounting Transition & Opening Balances

Roadmap §52 *Transition*: Operational Platform → Accounting Export → Existing Accounting System, lalu berpindah ke Operational Platform → Accounting → Financial Reports.

Club **belum memiliki sistem akuntansi** (keputusan §16 #2); pembukuan saat ini di Excel Finance. Transisi P4 karena itu berupa input saldo awal dan rekonsiliasi, bukan parallel run dengan sistem lama.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-TRS-01 | **Opening Balances** GL per akun per property per akhir bulan sebelum go-live, dari laporan keuangan / Excel Finance yang disetujui Finance Manager | Must |
| FR-TRS-02 | **Open items**: AR terbuka (melanjutkan P3), AP terbuka, deposit/DP, voucher, persediaan awal per lokasi, aset | Must |
| FR-TRS-03 | **Rekonsiliasi 1 bulan** setelah go-live awal bulan *(asumsi §16 #3)*: TB dan P&L OneClub dibandingkan dengan pembukuan Excel Finance dan Accounting Export; selisih dijelaskan | Must |
| FR-TRS-04 | **Cutover** Accounting Export: export dihentikan per property setelah sign-off; export lama tetap dapat diunduh | Must |
| FR-TRS-05 | **CoA baru** dari template OneClub untuk club/hospitality; mapping pos Excel Finance → CoA terdokumentasi | Must |

## EP-24 — Landing Page & CMS

**User story:** Sebagai Marketing Staff, saya ingin mengubah halaman, banner, berita, dan galeri dalam Bahasa Indonesia dan Inggris tanpa developer, dengan tarif dan paket yang selalu sama dengan sistem.

Halaman publik (roadmap §53): Home, Golf (Course, Hole by Hole, Handicap Index, Facilities, Reciprocal), Sport Club, Bungalow, VIP Suite, Meeting Room, MICE, Wedding, Banquet, Event, Membership, Package, Promotion / What's On, Hall of Fame, News, Article, Gallery, Contact, Location.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-CMS-01 | **Pages** dengan blok konten (teks, gambar, galeri, CTA, embed peta) dan **blok data terstruktur**: tarif (Pricing Engine), paket, promosi, event, hall of fame, course guide, availability widget (K5) | Must |
| FR-CMS-02 | **Banners** dengan periode tayang dan target halaman | Must |
| FR-CMS-03 | **Images** library (P0 storage) dengan alt text, resize otomatis, format web | Must |
| FR-CMS-04 | **News / Article**, **Gallery**, **Contact Information**, **Course Guide** (teks per hole melengkapi data course P1) | Must |
| FR-CMS-05 | **Languages**: konten ID/EN per halaman dengan fallback dan status terjemahan | Must |
| FR-CMS-06 | **Workflow**: Draft → Review (approval opsional) → **Scheduled** → **Published** → **Unpublished**; versi & rollback; preview | Must |
| FR-CMS-07 | **Promotion / What's On** otomatis tayang dan turun mengikuti periode promosi P3 | Must |
| FR-CMS-08 | **SEO**: meta title/description, slug, sitemap, Open Graph, structured data dasar | Must |
| FR-CMS-09 | Navigasi publik mengikuti NC §26 dan dapat diatur urutannya | Must |
| FR-CMS-10 | Perubahan konten tercatat di audit log; cache website di-invalidasi saat publish | Must |

**Acceptance criteria:**

- Perubahan tarif di Pricing Engine tampil di halaman tarif website tanpa mengedit konten CMS.
- Promosi dengan periode berakhir hilang dari halaman Promotions pada waktunya tanpa tindakan manual.

## EP-25 — Integration Layer P4

| ID | Requirement | Prioritas |
|---|---|---|
| FR-INT-P4-01 | **Satu payment gateway: Xendit** (provider P1) untuk QRIS, VA, dan kartu; rekonsiliasi per metode/property. Provider kedua (mis. Espay) tidak di P4; adapter P0 memungkinkan penambahan nanti (keputusan §16 #13) | Must |
| FR-INT-P4-02 | **e-Faktur / Coretax** adapter atau export resmi (Tech Doc §8.2: dari module `accounting`) | Must |
| FR-INT-P4-03 | **Bank**: impor mutasi (CSV/MT940) Must; API mutasi/host-to-host per bank bila tersedia | Must / Should (API) |
| FR-INT-P4-04 | **Hardware** yang tertunda dari P2 (locker, access/turnstile, ball dispenser, golf cart GPS) lewat bridge agent, sesuai vendor terpilih (Tech Doc keputusan #7) | Should |
| FR-INT-P4-05 | **Email provider produksi** (domain club, SPF/DKIM) untuk notifikasi, campaign P3, RFQ/PO | Must |
| FR-INT-P4-06 | ~~Accounting integration export ke sistem akuntansi lain~~: **tidak dibangun**, club belum memiliki sistem akuntansi (keputusan §16 #2) | — |
| FR-INT-P4-07 | **Modernland residence verification** produksi bila sumber tersedia (melanjutkan P1–P2) | Should |
| FR-INT-P4-08 | Semua integrasi dicatat (request/response masked, durasi, status) sesuai Tech Doc §8.2 | Must |

## EP-26 — Operational Interfaces P4 (`ops`)

| ID | Requirement | Prioritas |
|---|---|---|
| FR-OPS-P4-01 | **Warehouse**: Stock Balance, Goods Receipt, Issuing, Transfer, Stock Opname, Store Requisition dengan pemindai barcode/kamera | Must |
| FR-OPS-P4-02 | **Kitchen/Outlet**: Store Requisition, Production, Waste, stok outlet | Must |
| FR-OPS-P4-03 | **Golf Staff**: Spare Part Request untuk maintenance buggy | Should |
| FR-OPS-P4-04 | Stock opname dan goods receipt tetap dapat diinput saat koneksi gudang lemah; sync idempotent *(usulan)* | Should |

## EP-27 — Configuration & Policies P4

Memakai framework policy P0 (berversi + effective date) dan pola NC §33.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-POL-P4-01 | **Inventory Configuration** *(usulan label)*: metode valuasi, stok negatif per lokasi, toleransi opname, approval adjustment | Must |
| FR-POL-P4-02 | **Procurement Configuration / Policies** *(usulan label)*: ambang RFQ, approval matrix PR/PO, toleransi 3-way matching, penerimaan tanpa PO | Must |
| FR-POL-P4-03 | **Accounting Configuration** *(usulan label)*: CoA default, dimensi, mode posting, akun suspense, cut-off event terlambat, ambang approval jurnal manual | Must |
| FR-POL-P4-04 | **Tax Configuration** (NC §33) dipetakan ke akun | Must |

## EP-28 — KPI, Dashboard & Reports

KPI P4 mengikuti roadmap §60 (Inventory, Procurement, Finance) dan §69 dengan label NC §23.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-RPT-P4-01 | **Inventory Performance**: Inventory Value, Stock Balance, Low Stock, Stock Movement, Stock Opname, **Theoretical vs Actual Variance**, Food Cost % aktual | Must |
| FR-RPT-P4-02 | **Procurement Performance**: PR, PO, Outstanding PO, Purchase Value, Vendor Performance, waktu PR → PO | Must |
| FR-RPT-P4-03 | **Financial Performance**: Revenue by Business Line, AR, AP, Cash, Outstanding Payment, Deferred Revenue, Profit & Loss | Must |
| FR-RPT-P4-04 | **Reports** *(usulan daftar)*: Stock Balance Report, Stock Movement Report, Stock Valuation Report, Stock Opname Variance Report, Waste Report, Expiry Report, Theoretical vs Actual Consumption Report, Food Cost Report (aktual), Asset Register Report, Maintenance Schedule Report, Purchase Requisition Report, Purchase Order Report, Outstanding PO Report, Goods Receipt Report, Vendor Invoice Matching Report, Vendor Performance Report, Procurement Performance Report, General Ledger Report, Trial Balance, Profit & Loss, Balance Sheet, Cash Flow, Accounts Receivable Aging Report, Accounts Payable Aging Report, Bank Reconciliation Report, Deferred Revenue Report, Tax Report (PPN), Revenue by Business Line Report, Posting Exception Report | Must |
| FR-RPT-P4-05 | Filter tanggal/property/lokasi/pemasok/akun, export CSV/XLSX/PDF, permission per report (P0 FR-REP-03) | Must |

**Acceptance criteria:**

- Inventory Value di dashboard = Stock Valuation Report = saldo akun persediaan GL untuk tanggal yang sama.

## EP-29 — Migrasi Gelombang 4

Sumber utama: sistem akuntansi club yang ada (menerima Accounting Export sejak P1), Rhapsody back office, dan Excel gudang/pembelian. Pipeline memakai `oneclub import` dan Master Data Import P0.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-MIG-P4-01 | **Chart of Accounts** baru dan mapping pos Excel Finance → CoA | Must |
| FR-MIG-P4-02 | **Saldo awal GL** per akun per property (EP-23) | Must |
| FR-MIG-P4-03 | **Item, lokasi, saldo stok awal** dengan nilai (hasil stock opname cutover) | Must |
| FR-MIG-P4-04 | **Supplier**, item pemasok, **PO terbuka**, **AP terbuka** | Must |
| FR-MIG-P4-05 | **Aset** (register, nilai, akumulasi penyusutan bila dipakai) | Must |
| FR-MIG-P4-06 | **Reconciliation**: TB pembuka = neraca saldo awal yang disetujui Finance Manager; nilai persediaan awal = hasil opname; AP & AR terbuka = sub-ledger; ditandatangani Finance Manager | Must |
| FR-MIG-P4-07 | **Dry run** minimal 2 kali di Staging dan runbook cutover akuntansi & gudang (opname cutover, freeze pembelian, go/no-go, rollback ke Accounting Export) | Must |

## EP-30 — Production Readiness (Release 4)

| ID | Requirement | Referensi |
|---|---|---|
| FR-REL-P4-01 | **Load test** k6: posting 1 bulan volume transaksi, potong stok pada puncak POS, laporan keuangan tahunan di read replica, publikasi CMS | Tech Doc §13 |
| FR-REL-P4-02 | **E2E** Playwright untuk flow §9 (procure-to-pay, POS → stok → COGS → jurnal, opname, period closing, CMS publish) | Tech Doc §13 |
| FR-REL-P4-03 | **Regression suite P1–P3** lulus pada build Release 4 | G9 |
| FR-REL-P4-04 | **Integration test** seluruh posting rule dan rekonsiliasi saldo (Tech Doc §13: flow keuangan wajib integration test) | Tech Doc §13 |
| FR-REL-P4-05 | **Pen test** untuk CMS (upload, XSS konten), integrasi bank/Coretax, akses data keuangan | Tech Doc §9 |
| FR-REL-P4-06 | Retensi audit log & dokumen keuangan ≥ 5 tahun (Tech Doc §14); uji restore mencakup schema `accounting` | Tech Doc §7.7, §14 |
| FR-REL-P4-07 | Build VPS dengan `garble` dan tanpa source map | Tech Doc §9.4 |
| FR-REL-P4-08 | Training per peran (accountant, warehouse, procurement, inventory, marketing CMS) dan **hypercare** satu periode tutup buku *(usulan)* | — |

---

# 9. End-to-End Flows (Demo Exit P4)

Skenario UAT *(usulan)*.

## 9.1 Procure-to-Pay

```text
Stok sabun bungalow di bawah reorder point → PR otomatis (EP-08)
  → approval Procurement Manager → RFQ 3 pemasok → pilih quotation (EP-11, EP-12)
  → PO (approved, sent) → GR parsial 80% → GR sisa (EP-13, EP-14)
  → Vendor Invoice → 3-Way Matching Matched → AP (EP-15, EP-19)
  → payment run → mutasi bank → bank reconciliation (EP-20)
  → jurnal: persediaan, GRNI, utang, kas (EP-17)
```

## 9.2 Banquet Supply

```text
BEO wedding 300 pax issued (P3) → procurement requirement (K1) → PR banquet (EP-11)
  → revisi BEO 320 pax → PR diperbarui → PO → GR ke Kitchen Banquet
  → event Completed (K6) → potong stok BOM per pax final (EP-06)
  → COGS banquet & food cost aktual event vs teoretis (EP-05, EP-28)
```

## 9.3 POS → Stok → Jurnal

```text
Restoran: 200 transaksi termasuk 40 offline (P2) → sale_completed (K7)
  → potong stok outlet (recipe, modifier, sub-recipe) (EP-06)
  → business day closed (K4) → jurnal pendapatan, PPN/PB1, service, HPP (EP-17, EP-21)
  → stock opname bulanan → variance → adjustment (EP-04)
  → Theoretical vs Actual Variance per outlet (EP-28)
```

## 9.4 Month-End Close

```text
Semua business day ditutup (P3) → posting exception = 0 → opname diposting
  → bank reconciliation selesai → 3-way matching selesai → accrual & penyusutan (Should)
  → Soft Close → jurnal penyesuaian → Close (EP-16)
  → P&L, Balance Sheet, Cash Flow, Revenue by Business Line (EP-22)
  → e-Faktur keluaran masa ini terunggah (EP-21)
```

## 9.5 Website Update

```text
Marketing membuat halaman promo Ramadan ID/EN + banner (Scheduled) (EP-24)
  → promo P3 aktif → blok promosi & tarif otomatis dari data terstruktur
  → Published sesuai jadwal → periode berakhir → Unpublished otomatis
```

---

# 10. Data Entities P4

Sesuai Data Entity Rollout roadmap §70 (P4), ditambah entity pendukung. Schema mengikuti Tech Doc §4.1.

| Entity | Schema | Catatan |
|---|---|---|
| Item (perluasan P2), Item Category, UOM Conversion (perluasan), Barcode, Supplier Item | `inventory` | Product Category (§70) |
| Warehouse, Stock Location | `inventory` | Warehouse (§70) |
| Stock Movement, Stock Balance, Batch, Serial, Cost Layer (FIFO) | `inventory` | Inventory, Stock Movement (§70) |
| Store Requisition, Stock Transfer, Stock Adjustment, Stock Opname, Opname Line | `inventory` | Store Requisition (§70) |
| Production Order, Waste Record, Consumption (aktual), Par Stock / Reorder Rule | `inventory` | Melengkapi consumption ledger P2 |
| Asset, Asset Category, Maintenance Schedule, Maintenance Record, Depreciation Schedule (Should) | `inventory` | Asset (§70) |
| Supplier (perluasan P0), Vendor Performance | `procurement` | Supplier (§70) |
| Purchase Requisition, PR Line, RFQ, Vendor Quotation, Purchase Order, PO Line, Goods Receipt, Purchase Return, Debit Note | `procurement` | Purchase Requisition, RFQ, Quotation, Purchase Order, Goods Receipt (§70) |
| Vendor Invoice, Vendor Invoice Line, Matching Result | `procurement` | Vendor Invoice (§70) |
| Chart of Account, Account Dimension, Journal Entry, Journal Line, Financial Period, Closing Run | `accounting` | Chart of Account, Journal, Ledger (§70); append-only (Tech Doc §7.4) |
| Posting Rule, Posting Rule Version, Posting Exception, Processed Event | `accounting` | |
| AR Ledger Entry, Allowance, AP Ledger Entry, Payment Run, AP Payment | `accounting` | AR, AP (§70); invoice tetap di `billing` (P3) |
| Bank Account, Cash Account, Bank Transaction, Bank Reconciliation, Reconciliation Match | `accounting` | Bank Account (§70) |
| Revenue Allocation Rule, Deferred Revenue Schedule, Tax Code, Tax Invoice (e-Faktur), Service Charge Pool | `accounting` | Deferred Revenue, Revenue Allocation Rule (§70) |
| Opening Balance Batch, Account Mapping | `accounting` | Transisi |
| Page, Page Version, Content Block, Banner, Media, Article, Gallery, Navigation, Translation | `cms` | Module baru |
| Read model KPI P4 | `reporting` | |

---

# 11. API Surface P4

Konvensi Tech Doc §8.1 (`/api/v1/<module>/<resource>`, cursor pagination, RFC 9457, `Idempotency-Key`, `ETag`/`If-Match`).

| Area | Endpoint utama |
|---|---|
| Inventory master | `/api/v1/inventory/items`, `/inventory/categories`, `/inventory/uoms`, `/inventory/warehouses`, `/inventory/locations`, `/inventory/par-stocks`, `/inventory/reorder-points` |
| Stock | `GET /api/v1/inventory/stock-balances`, `GET /inventory/stock-movements`, `/inventory/issues`, `/inventory/requisitions` (+ `:submit`, `:approve`, `:fulfill`), `/inventory/transfers` (+ `:ship`, `:receive`), `/inventory/adjustments` (+ `:approve`) |
| Opname | `/api/v1/inventory/stock-opnames` (+ `:start`, `:count`, `:submit`, `:post`), `/inventory/stock-opnames/{id}/lines` |
| Valuation & production | `GET /api/v1/inventory/valuation`, `/inventory/production-orders` (+ `:complete`), `/inventory/waste`, `GET /inventory/consumption-variance` |
| Asset | `/api/v1/inventory/assets` (+ `:dispose`), `/inventory/maintenance-schedules`, `/inventory/maintenance-records` |
| Procurement | `/api/v1/procurement/suppliers`, `/procurement/requisitions` (+ `:submit`, `:approve`, `:reject`), `/procurement/rfqs` (+ `:send`), `/procurement/vendor-quotations` (+ `:select`), `/procurement/purchase-orders` (+ `:approve`, `:send`, `:revise`, `:close`), `/procurement/goods-receipts`, `/procurement/purchase-returns`, `/procurement/vendor-invoices` (+ `:match`, `:release-hold`), `GET /procurement/vendor-performance` |
| Accounting | `/api/v1/accounting/accounts`, `/accounting/journals` (+ `:approve`, `:post`, `:reverse`), `GET /accounting/general-ledger`, `GET /accounting/trial-balance`, `/accounting/periods` (+ `:soft-close`, `:close`, `:reopen`), `/accounting/posting-rules`, `/accounting/posting-exceptions` (+ `:repost`) |
| AR / AP | `GET /api/v1/accounting/receivables`, `GET /accounting/ar-aging`, `/accounting/allowances`, `GET /accounting/payables`, `GET /accounting/ap-aging`, `/accounting/payment-runs` (+ `:approve`, `:execute`) |
| Cash & Bank | `/api/v1/accounting/bank-accounts`, `/accounting/bank-transactions` (+ `:import`), `/accounting/bank-reconciliations` (+ `:auto-match`, `:match`, `:complete`) |
| Revenue & Tax | `/api/v1/accounting/revenue-allocation-rules`, `GET /accounting/deferred-revenue`, `/accounting/tax-codes`, `/accounting/tax-invoices` (+ `:upload`, `:cancel`), `/accounting/service-charge-pools` |
| Reports | `GET /api/v1/accounting/reports/{profit-loss|balance-sheet|cash-flow|revenue-by-business-line}`, `GET /api/v1/reporting/dashboards/{inventory|procurement|financial}` |
| Transition | `/api/v1/accounting/opening-balances` (+ `:post`), `/accounting/account-mappings` |
| CMS | `/api/v1/cms/pages` (+ `:submit`, `:publish`, `:schedule`, `:unpublish`, `:rollback`), `/cms/banners`, `/cms/media`, `/cms/articles`, `/cms/galleries`, `/cms/navigation`, `GET /api/v1/public/cms/pages/{slug}` |
| Migration | CLI `oneclub import accounting --coa|--opening-balances|--open-ap`, `oneclub import inventory --items|--opening-stock`, `oneclub import procurement --suppliers|--open-po` |

Event outbox P4: `inventory.stock_moved`, `inventory.stock_low`, `inventory.opname_posted`, `inventory.production_completed`, `inventory.asset_maintenance_due`, `procurement.requisition_approved`, `procurement.po_approved`, `procurement.goods_received`, `procurement.vendor_invoice_matched`, `accounting.journal_posted`, `accounting.posting_exception`, `accounting.period_closed`, `accounting.payment_run_executed`, `accounting.tax_invoice_uploaded`, `cms.page_published`.

---

# 12. Non-Functional Requirements

Mengacu Tech Doc §14 dan PRD P3 §12. Semua target P1–P3 tetap berlaku.

| Area | Requirement P4 |
|---|---|
| Availability | 99.5% per bulan pada jam operasional; proses period closing tidak mengganggu transaksi operasional |
| Performance | p95 < 300 ms read; p95 < 800 ms transaksi gudang/pembelian; posting event ke jurnal < 5 menit (lag outbox, Tech Doc §11); laporan keuangan bulanan < 30 detik di read replica *(usulan)* |
| Financial integrity | Jurnal append-only dan seimbang (DB-enforced); 0 event keuangan tanpa jurnal atau exception; saldo akun kontrol = sub-ledger (AR, AP, persediaan, deferred, caddy fee liability, deposit) setiap akhir hari |
| Inventory integrity | Saldo = Σ movement; 0 potong stok ganda; valuasi konsisten dengan GL |
| Period control | Tidak ada posting ke periode Closed dari sumber mana pun |
| Audit | Retensi audit log & dokumen keuangan ≥ 5 tahun; akses auditor read-only; jejak dokumen sumber → jurnal → laporan |
| Security | OWASP ASVS Level 2; data rekening bank & NPWP dimasking per permission; review 2 orang untuk `accounting` dan migration destruktif (Tech Doc §13); CMS mensanitasi konten (XSS) dan memvalidasi upload |
| Offline | Opname dan goods receipt `ops` dapat diinput dengan koneksi lemah (Should) |
| SEO & web | Halaman publik Core Web Vitals "Good" pada mobile *(usulan)*; ID/EN |
| Regresi | Seluruh suite P1–P3 lulus di setiap merge P4 |
| Bahasa | English (label) + Bahasa Indonesia (helper text, pesan, dokumen PO/RFQ ID/EN) |

---

# 13. Exit Criteria & Definition of Done

## 13.1 Exit Criteria Release 4 *(usulan)*

Roadmap tidak mencantumkan exit criteria eksplisit untuk P4. Kriteria berikut diturunkan dari target Release 4 (roadmap §75: *inventory, procurement, accounting, digital channel*), roadmap §52 *Transition*, dan Rhapsody Parity §72 (*Back Office: operational reporting, master data, transaction history, financial handoff*).

| # | Exit criteria | Bukti |
|---|---|---|
| 1 | OneClub menjadi buku besar | Rekonsiliasi 1 bulan lulus (TB & P&L sama dengan pembukuan Excel Finance per akun atau selisih terjelaskan); Accounting Export dimatikan (EP-23) |
| 2 | Posting otomatis lengkap | Posting exception = 0 pada akhir periode UAT; rekonsiliasi posting vs Daily Revenue Report 100% (EP-17) |
| 3 | Procure-to-pay berjalan | Skenario §9.1–9.2 lulus; vendor invoice hanya dibayar setelah Matched (EP-11–15, EP-19) |
| 4 | Persediaan akurat | Stock opname cutover dan satu opname bulanan dalam toleransi; Stock Valuation = GL (EP-02–06) |
| 5 | Food cost aktual tersedia | Theoretical vs Actual Variance per outlet untuk satu periode (EP-06, EP-28) |
| 6 | Kewajiban terekonsiliasi | Deposit, voucher, prepaid, poin, annual fee, caddy fee liability di GL = sub-ledger (EP-21) |
| 7 | Pajak terintegrasi | e-Faktur keluaran satu masa pajak dibuat dari invoice OneClub dan terunggah (EP-21, EP-25) |
| 8 | Website dikelola club | Marketing menerbitkan halaman, berita, galeri, dan promosi ID/EN tanpa developer (EP-24) |
| 9 | Tanpa regresi Release 1–3 | Suite P1–P3 lulus pada build Release 4 (G9) |
| 10 | UAT disetujui | Skenario §9 lulus di Staging; month-end close §9.4 di-sign-off Finance Manager |

## 13.2 Definition of Done per Story

Sama dengan PRD P3 §13.2, ditambah:

- Setiap posting rule baru memiliki integration test yang memverifikasi jurnal seimbang, akun benar, dan idempotency.
- Setiap sub-ledger memiliki test rekonsiliasi terhadap akun kontrol GL.
- Movement stok memiliki test saldo = Σ movement dan valuasi.
- Perubahan pada module `accounting` dan migration destruktif direview 2 orang (Tech Doc §13).
- Kontrak dengan P3 (§5.4.2) memiliki contract test.

---

# 14. Milestones & Gelombang Rilis

## 14.1 Milestones *(usulan)*

Urutan mengikuti §5.4.3: bagian yang tidak bergantung pada P3 lebih dulu.

| Milestone | Isi | Bergantung pada | Hasil yang bisa didemo |
|---|---|---|---|
| **M1 — Ledger Core** | EP-16 (CoA, jurnal append-only, GL, TB, period), EP-27 (Accounting Configuration), kontrak K7–K10 | Release 2 | Jurnal manual seimbang, reversal, period close |
| **M2 — Posting P1–P2** | EP-17 untuk billing, voucher, settlement, membership fee, POS shift; EP-21 (revenue allocation golf all-in, deferred, caddy liability, pajak) | M1; K8 | Jurnal otomatis harian = Accounting Export (parallel run internal) |
| **M3 — Inventory** | EP-01–08, Warehouse interface | M1 | POS → potong stok → COGS; opname; PR otomatis |
| **M4 — Procure-to-Pay** | EP-10–15, EP-19, EP-20 | M1, M3 | Skenario §9.1 |
| **M5 — P3 Integration** | AR dari invoice P3 (K2), PR banquet (K1), paket/banquet consumption (K6), business day cut-off (K4), EP-18 | M2–M4; P3 R3.1/R3.4 | Skenario §9.2; AR aging = aging P3 |
| **M6 — Reports, Tax & Asset** | EP-22, EP-21 (e-Faktur), EP-09, EP-25, EP-28 | M2–M5 | Laporan keuangan; e-Faktur; asset maintenance |
| **M7 — CMS** | EP-24 | Website P1–P3; K5 | Skenario §9.5 |
| **M8 — Transition, Migrasi & Release 4** | EP-23, EP-29, EP-30 | M1–M7 | Rekonsiliasi 1 bulan, dry run ×2, demo exit §9 |

M3 dan M7 dapat berjalan bersamaan dengan M2.

## 14.2 Gelombang Rilis Release 4 *(usulan)*

Production P4 hanya setelah Release 2 live. Rilis bertahap dengan module flag:

| Gelombang | Isi | Alasan urutan |
|---|---|---|
| R4.1 | CMS | Tidak bergantung pada data keuangan; dampak cepat ke marketing |
| R4.2 | Inventory + Procurement (dengan opname cutover) | Membutuhkan perubahan proses gudang dan pembelian; jurnal stok masih lewat export sampai R4.3 |
| R4.3 | Accounting: go-live awal bulan dengan saldo awal, rekonsiliasi 1 bulan, lalu Accounting Export dimatikan | Risiko tertinggi; dimulai di awal periode tutup buku yang disepakati Finance |

---

# 15. Risiko & Mitigasi *(usulan)*

| # | Risiko | Dampak | Mitigasi |
|---|---|---|---|
| 1 | Scope P4 besar dan paralel dengan P3 (30 epic, 3 module baru) | Release 4 terlambat | Gelombang §14.2; Must dulu; potongan scope di OQ #12 |
| 2 | Posting rule salah atau tidak lengkap | Laporan keuangan salah | Rule awal dari komponen Accounting Export, suspense + exception queue, rekonsiliasi 1 bulan dengan Excel Finance, rekonsiliasi harian |
| 3 | Event terlambat (POS offline, retry) setelah business day/periode ditutup | Jurnal di periode salah | Aturan cut-off eksplisit (Accounting Configuration), periode Soft Closed, exception report |
| 4 | Data stok awal tidak akurat | Variance besar sejak hari pertama | Stock opname cutover wajib per lokasi; freeze pembelian saat cutover |
| 5 | Disiplin gudang rendah (barang keluar tanpa requisition) | Variance tinggi, food cost tidak berarti | Requisition di `ops` dengan barcode, opname bulanan, laporan variance per outlet ke manajemen |
| 6 | Dua sumber kebenaran AR (billing P3 vs accounting P4) | Angka tidak cocok | Satu dokumen invoice (P3), AR ledger diturunkan darinya, test rekonsiliasi |
| 7 | Ketentuan Coretax berubah atau API belum tersedia | e-Faktur tertunda | Adapter + export resmi sebagai fallback; pantau regulasi DJP |
| 8 | Ketergantungan P3 (invoice, BEO, paket, business day) belum siap | Fitur P4 terhambat | Kontrak dengan mock (§5.4.3); M1–M4 tidak bergantung P3 |
| 9 | Akuntan club terbiasa dengan pembukuan Excel | Adopsi lambat, rekonsiliasi berlarut | Training, mapping akun jelas, laporan format auditor, hypercare satu periode |
| 10 | CMS dipakai untuk konten yang seharusnya data terstruktur (tarif sebagai gambar) | Website kembali usang | Blok data terstruktur wajib untuk tarif/paket/promo; review konten |

---

# 16. Open Questions

Status per 4 Oktober 2026 (sumber: `OneClub — Open Questions P3–P5.md`):

- ✅ **Diputuskan**: dijawab langsung oleh club / product, dan sudah diterapkan ke requirement terkait.
- 🅰 **Asumsi**: belum dijawab. Nilai di kolom kanan dipakai sebagai dasar pengembangan sampai dikonfirmasi pemiliknya. Semua angka, tarif, dan aturan configurable di Settings.

| # | Pertanyaan | Dampak | Pemilik | Status | Keputusan / Asumsi |
|---|---|---|---|---|---|
| 1 | Tech Doc §12.3 (gelombang & kepemilikan) perlu ditulis; setuju P3 dan P4 sebagai gelombang 2 dengan aturan §5.4? | Seluruh P4 | Engineering | ✅ | **Setuju.** P3 ‖ P4 = gelombang 2; §12.3 ditulis di Tech Doc |
| 2 | Sistem akuntansi yang dipakai club saat ini dan format CoA; apakah OneClub menggantikannya penuh atau club tetap memakai sistem lama (FR-INT-P4-06)? | EP-16, EP-23 | Club Finance | ✅ | **Belum ada sistem akuntansi**; OneClub menjadi sistem akuntansi pertama. CoA disusun baru dari template OneClub untuk club/hospitality. Integrasi ke sistem akuntansi lama (FR-INT-P4-06) tidak diperlukan |
| 3 | Lama parallel run dan periode cutover akuntansi (awal bulan / awal tahun buku)? | EP-23 | Club Finance | 🅰 | Go-live **awal bulan**; saldo awal per akhir bulan sebelumnya, diambil dari laporan keuangan / Excel Finance. Karena tidak ada sistem lama, parallel run diganti **rekonsiliasi 1 bulan** dengan pembukuan Excel Finance |
| 4 | Ada konsinyasi pro shop? Bagaimana perlakuan stok dan utangnya? (roadmap §74 #9) | FR-INV-07 | Club | 🅰 | **Ada.** Stok konsinyasi dicatat terpisah tanpa nilai persediaan. Utang ke vendor timbul saat barang terjual; settlement bulanan dengan komisi club per vendor |
| 5 | Penyusutan aset tetap dikelola di OneClub atau tetap di sistem lain? | FR-AST-05 | Club Finance | ✅ | **Di OneClub.** 🅰 Default metode garis lurus (saldo menurun configurable); jurnal penyusutan otomatis bulanan |
| 6 | Laporan keuangan per property saja, atau perlu gabungan antar property (MAIN, MDR)? Apakah setiap property badan hukum berbeda? | FR-FIN-03 | Club Finance | ✅ / 🅰 | ✅ **Gabungan** (konsolidasi MAIN + MDR), tetap bisa difilter per property. 🅰 MAIN dan MDR **satu badan hukum**, sehingga gabungan cukup dijumlahkan tanpa eliminasi antar perusahaan |
| 7 | Jenis pajak yang berlaku per outlet (PPN, PB1), perlakuan service charge, dan kewajiban PPh atas pembayaran vendor jasa | EP-21, FR-AP-05 | Club Finance + Konsultan pajak | 🅰 | **Service charge 10%** di outlet F&B, banquet, dan bungalow. **Pajak daerah PBJT (dulu PB1) 10%:** F&B & banquet (makanan/minuman), bungalow (perhotelan), golf & fasilitas sport (jasa hiburan – olahraga permainan). **PPN** untuk penjualan barang pro shop dan membership fee. **PPh vendor:** PPh 23 2% atas jasa, PPh 4(2) 10% atas sewa. Tarif per outlet configurable. **Wajib diverifikasi konsultan pajak** (dibahas bersama P5 #4) |
| 8 | Metode valuasi per kategori (average vs FIFO) dan toleransi variance opname | EP-04, EP-05 | Club Finance | 🅰 | **Moving average** untuk semua kategori. Toleransi variance: bahan F&B 2%, minuman/alkohol 0,5%, pro shop 0,5%, general supplies 1% dari nilai. Di atas toleransi → approval Finance Manager |
| 9 | Struktur gudang & lokasi final, siapa pemegang stok outlet, dan frekuensi opname | EP-01, EP-04 | Club Operasional | 🅰 | **Gudang:** Main Store, Cold Store, Beverage Store, Pro Shop Store, Engineering Store. **Sub-store outlet:** Clubhouse Restaurant, Bar, Halfway House, Banquet Kitchen, Sport Club Café. Pemegang stok outlet: Outlet Manager / Chef. **Opname:** bulanan untuk semua outlet & gudang; spot check mingguan untuk beverage & pro shop; full opname tahunan |
| 10 | Matriks approval PR/PO (nilai, kategori, cost center) dan ambang RFQ | EP-11–13 | Club | 🅰 | ≤ Rp5 jt: kepala departemen · ≤ Rp25 jt: + Finance Manager · ≤ Rp100 jt: + GM · > Rp100 jt: + Direksi/Owner. Capex selalu sampai GM. **RFQ minimal 3 vendor** untuk pembelian > Rp10 jt, kecuali vendor kontrak |
| 11 | Bank yang dipakai club dan ketersediaan API mutasi/host-to-host | EP-20, EP-25 | Club Finance | 🅰 | Club meminta data dibuat sendiri. Asumsi: **BCA** (operasional & payroll) dan **Mandiri** (penerimaan & virtual account). Mutasi lewat **upload file statement** (CSV) saat go-live; API mutasi / host-to-host jadi Should |
| 12 | Prioritas jika kapasitas tidak cukup: urutan gelombang R4.1–R4.3 disetujui? Epic Should mana yang boleh bergeser ke P5? | Milestones | Management + Club | 🅰 | **Disetujui:** R4.1 CMS → R4.2 Inventory + Procurement → R4.3 Accounting. Epic Should boleh bergeser ke P5 |
| 13 | Payment provider tambahan yang diinginkan selain provider P1 | FR-INT-P4-01 | Club Finance | ✅ | **Satu payment gateway saja, tetap Xendit** (sudah diimplementasi di P1). Tidak ada provider tambahan di P4. Espay sempat disebut; penggantian ke Espay bisa dievaluasi nanti lewat adapter baru |
| 14 | Vendor hardware (locker, turnstile, ball dispenser, GPS buggy) sudah dipilih? (Tech Doc keputusan #7) | FR-INT-P4-04 | Club + Engineering | 🅰 | Belum dipilih. Integrasi generik lewat bridge agent: locker elektronik RFID, turnstile dan device absensi satu vendor (mis. ZKTeco, selaras P5 #7), ball dispenser lewat relay/API vendor. **GPS buggy ditunda** (Should) |
| 15 | Siapa pengelola konten website dan apakah perlu approval sebelum publish? | EP-24 | Club Marketing | 🅰 | Dikelola **Marketing Staff**; publish perlu approval **Marketing Manager**. Harga di website tetap mengikuti rate card, bukan CMS |
| 16 | Dasar pembagian service charge per departemen (untuk P5) | FR-REV-06 | Club Finance + HR | 🅰 | **95%** pool dibagi ke karyawan, **5%** cadangan breakage & loss. Dibagi **rata per karyawan eligible**, dikalikan faktor kehadiran (hari hadir ÷ hari kerja). **Eligible:** karyawan tetap & kontrak yang lulus probation. **Tidak eligible:** probation, pekerja harian, karyawan dengan SP2 ke atas, cuti tanpa upah sepanjang periode. Caddy mitra tidak menerima service charge (sudah ada caddy fee). Dibayar bulanan bersama payroll. Sama dengan P5 #5 |
| 17 | Persetujuan usulan label dan status baru (§7.6), termasuk perbaikan "Receiving" → "Goods Receipt" di NC §29 | IA | Product | ✅ | **Setuju** |
| 18 | Apakah auditor eksternal memerlukan akses langsung (read-only) ke OneClub? | FR-ACC-09 | Club Finance | 🅰 | **Ya.** Role **Auditor** read-only, hanya Accounting & Reports, berlaku selama periode audit, seluruh akses tercatat di audit log |
