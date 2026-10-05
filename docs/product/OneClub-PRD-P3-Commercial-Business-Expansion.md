# OneClub
## Product Requirements Document — P3 Commercial & Business Expansion

**Version:** 1.1 (Draft): keputusan & asumsi Open Questions diterapkan  
**Phase:** P3 — Commercial & Business Expansion (Release 3 — Commercial Platform)  
**Based on:** `product-roadmap.md` (§3, §40–49, §59, §61–62, §65–75, §77), `OneClub — Product Overview v3.md` (§15, §26–33, §40–43, §49–53), `OneClub — Naming Convention.md` (§6, §9, §11–18, §23–27, §29–33), `OneClub — Technical Documentation.md` (§4, §6.1, §7.3–7.6, §8, §9.3, §13–15), `OneClub — PRD P0 Platform Foundation.md`, `OneClub — PRD P1 Golf Core MVP.md`, `OneClub — PRD P2 Complete Golf Experience & Shared Core.md`  
**Date:** 4 October 2026

> Requirement yang ditandai *(usulan)* adalah turunan PRD ini dan belum tercantum eksplisit di roadmap atau Product Overview. Ketidaksesuaian antar dokumen sumber dan keputusan PRD ini atas masing-masing ada di [bagian 6](#6-hasil-pengecekan-dokumen-sumber). Keputusan dan asumsi atas open question ada di [bagian 16](#16-open-questions).

> **Pembaruan arsitektur aplikasi (5 Oktober 2026).** Staff App tetap **satu aplikasi** (satu build) dengan area per role, dan dibuka di empat domain (Product Overview §45, Technical Documentation §6.1): `dashboard.<club>` untuk kantor (Back Office, Management Dashboard yang dibatasi menu/permission, Platform Administration, dan Clubhouse Screen untuk role **Screen**; login email + password + MFA), `cashier.<club>` untuk perangkat kasir dan konter (Operational Interface, PIN per shift), `caddy.<club>` untuk Caddy Tablet, dan `kitchen.<club>` untuk Kitchen Display. Domain mengunci area; perangkat bersama tidak membuka area kantor. Untuk P3: layar Back Office (CRM pipeline, package, banquet/wedding/MICE, promotion, loyalty, consolidated billing) ada di `dashboard`; Event Operations dan Tournament Desk di `cashier`; produksi banquet dari BEO di `kitchen`; **Leaderboard Screen** mengikuti pola Clubhouse Screen (`dashboard`, role Screen); promosi dan redeem poin di POS memakai `cashier`, termasuk saat offline.

---

# 1. Ringkasan

P3 mengubah OneClub dari sistem operasional menjadi **platform komersial**: club bisa menjaring dan mengonversi calon pelanggan, menjual paket lintas lini, menjalankan banquet, wedding, MICE, event, dan tournament, menjalankan promosi dan loyalty, serta menagih semua transaksi customer dari **satu folio**. Targetnya adalah **Release 3 — Commercial Platform** (roadmap §75): *customer, sales, commercial, event, banquet, tournament* untuk **Modern Golf & Country Club**, Tangerang.

P3 dibangun di atas Release 2 (P0 + P1 + P2) dan dikerjakan **paralel dengan P4** (Enterprise Back Office) sebagai gelombang kedua. Karena itu PRD ini juga mengatur kepemilikan module dan kontrak dengan P4 ([bagian 5.4](#54-pengembangan-paralel-dengan-p4)).

P3 menghasilkan:

```text
Commercial & Business Expansion
├── CUSTOMER & SALES
│   ├── Lead Management (WhatsApp, media sosial, website, walk-in, referral member)
│   ├── Sales Pipeline & Opportunity (Wedding, MICE, Corporate Membership, Sport Membership,
│   │   Corporate Golf, Tournament)
│   ├── Quotation → konversi ke Booking / Banquet / Membership / Tournament
│   ├── Sales Target & Commission (perhitungan; payroll di P5)
│   ├── Advanced Customer 360 & Segmentation
│   ├── Customer Engagement: Campaign, Renewal & Birthday Reminder, Follow-up
│   ├── Feedback, NPS & Complaint Ticket (SLA, eskalasi)
│   ├── Top Spender & Leaderboard (internal)
│   └── Loyalty Foundation (poin lintas outlet, redemption, tier & reward foundation)
├── COMMERCIAL
│   ├── Pricing & Promotion Engine (happy hour, Buy N, bundle, member discount, promo code)
│   └── Package Management lintas lini (Golf Day, Stay & Golf, Corporate, Wedding)
├── EVENT & BANQUET
│   ├── Event Management (multi-resource, peserta, checklist, vendor)
│   ├── Banquet, MICE & Wedding (inquiry → quotation → menu → BEO → DP → H-7 → final billing)
│   ├── Banquet Event Order (BEO) & produksi dapur
│   └── Banquet Venue sebagai resource Reservation Engine
├── GOLF
│   └── Tournament Management (registrasi, flighting, shotgun start, scoring, leaderboard,
│       sponsor, prize, Hall of Fame)
├── BILLING EXPANSION
│   ├── Unified Folio per customer & cross-line billing
│   ├── Invoice, Payment Schedule (DP, termin), Corporate Billing (AR operasional)
│   └── Cashier Shift (front office), End-of-Day & Night Audit
└── CHANNELS & OPERATIONS
    ├── Member App P3 (Loyalty, Offers, Events, Tournaments, Support)
    ├── Website P3 (Wedding & Banquet, Events, Promotions, Packages, inquiry → CRM)
    ├── Operational Interfaces (Event Operations, Tournament Desk, Leaderboard Screen)
    ├── Sales, CRM, Banquet, Commercial (package & promotion), Loyalty KPI
    └── Migrasi gelombang 3 & Release 3
```

Alur inti baru yang harus berjalan end-to-end di production:

```text
Sales:      Lead (WhatsApp/Web/Social/Walk-in) → Assignment → Opportunity → Quotation
            → Accepted → Booking / Banquet / Membership / Tournament → Won
Banquet:    Inquiry → Quotation & Site Visit → Package & Menu → DP 30% → Food Tasting
            & Technical Meeting → BEO → Pelunasan H-7 → Event → Final Billing → Accounting
Package:    Select Package → Availability semua komponen → Hold all-or-nothing → Payment
            → Allocation per lini → Consumption → Revenue Allocation per komponen
Tournament: Create → Registration & Fee → Flighting / Shotgun Draw → Scoring (scorecard P2)
            → Live Leaderboard → Finalize → Prize & Hall of Fame → Tournament Report
Promotion:  Rule aktif (jam, hari, segmen, kode) → harga POS/booking/website → snapshot
Loyalty:    Payment Settled → Earn Points → Tier → Redeem (tender) → Liability
Billing:    Charges semua lini → Customer Folio → Invoice / Payment Schedule
            → Payment → Cashier Shift → Night Audit → Accounting Export / Accounting P4
```

---

# 2. Latar Belakang & Problem

- **Penjualan bergantung pada WhatsApp pribadi sales.** Modern Golf menerima inquiry wedding, MICE, dan corporate golf lewat beberapa nomor WhatsApp sales, email per departemen (reservation@, marketing@, banquet@), Instagram, Facebook, dan TikTok (Product Overview §53). Tidak ada pipeline bersama, sehingga lead hilang saat sales cuti atau keluar, dan manajemen tidak tahu konversi per lini.
- **Banquet adalah lini pendapatan besar tanpa sistem.** Paket wedding Rp88 jt nett, intimate wedding Rp28 jt, birthday Rp18 jt, kids birthday Rp14,5 jt, social event Rp190rb++/pax (Product Overview §28) dikelola dengan quotation Word/Excel, BEO tertulis, dan pengingat DP 30% serta pelunasan H-7 yang manual. Fit-Gap §71 menandai *Banquet & Wedding* sebagai gap P3.
- **Tournament dan scoring masih di Rhapsody.** P1 dan P2 memindahkan golf front office, scoring, dan Hall of Fame, tetapi tournament (registrasi, flighting, shotgun start, leaderboard) masih menjadi alasan Rhapsody belum bisa dimatikan sepenuhnya (Rhapsody Parity §72: *Tournament, Scoring*).
- **Promosi tidak bisa dijalankan sistem.** Promo seperti "Buy 2 Only 200K" dengan jam berbeda weekday/weekend, happy hour, dan bundling (Product Overview §31) dihitung kasir secara manual. P2 sengaja hanya memperluas dimensi tarif (PRD P2 §3.2).
- **Tagihan customer masih terpecah per reservasi.** P2 menyatukan member account dan statement, tetapi customer non-member dan perusahaan masih menerima tagihan terpisah per lini. Corporate billing ke perusahaan masih invoice manual (PRD P2 FR-MTG-08). Kontrol kas front office hanya Daily Payment Summary (PRD P1 FR-PAY-12) dan shift POS per outlet (PRD P2 FR-POS-10). Belum ada tutup hari bisnis lintas kasir.
- **Tidak ada program retensi.** Top Spender, loyalty lintas outlet, campaign tersegmentasi, dan complaint ticket dengan SLA belum ada, padahal datanya sudah terkumpul sejak P1–P2.

---

# 3. Goals & Non-Goals

## 3.1 Goals

| # | Goal | Ukuran keberhasilan |
|---|---|---|
| G1 | Semua inquiry masuk ke satu pipeline | ≥ 95% inquiry wedding, MICE, corporate golf, dan membership selama UAT tercatat sebagai Lead dengan sumber dan sales yang ditugaskan *(usulan target)*; first response time terukur |
| G2 | Banquet & wedding berjalan end-to-end di OneClub | Skenario wedding §9.1 lulus: quotation → BEO → DP 30% → pelunasan H-7 → final billing, tanpa dokumen di luar sistem |
| G3 | Tournament dan scoring Rhapsody dapat dimatikan | Satu tournament club (shotgun start, ≥ 72 pemain) berjalan penuh di OneClub dengan live leaderboard; checklist parity tournament ditandatangani club |
| G4 | Promosi dijalankan sistem, bukan kasir | Promo dengan jam/hari/segmen/kode menghasilkan harga benar di POS (termasuk offline), booking, dan website; tercatat di pricing snapshot |
| G5 | Paket lintas lini dapat dijual | Golf Day, Stay & Golf, Corporate, dan Wedding package dibooking dalam satu transaksi dengan alokasi all-or-nothing dan revenue allocation per komponen yang jumlahnya sama dengan harga paket |
| G6 | Satu folio per customer dan corporate billing | Customer non-member dan corporate account menerima satu folio/invoice lintas lini; aging piutang corporate tersedia; night audit menutup hari bisnis dengan total yang terekonsiliasi |
| G7 | Retensi berbasis data | Top Spender, segmentasi lanjutan, campaign WhatsApp/email, dan loyalty points aktif; saldo poin = liability loyalty di setiap akhir hari |
| G8 | KPI P3 tersedia | Sales, CRM, Banquet, Commercial (package & promotion), dan Loyalty KPI tampil di Management Dashboard (roadmap §69) |
| G9 | Tanpa regresi Release 1–2 | Seluruh test E2E dan load test P1–P2 tetap lulus setelah pricing, billing, reservation, dan CRM diperluas |

G1–G8 diturunkan dari scope roadmap §40–49, §69, §71–72. G9 mengikuti pola PRD P2 G9.

## 3.2 Non-Goals (P3)

- Inventory penuh (warehouse, stock balance, potong stok, stock opname), Procurement, dan Accounting (GL, AR/AP ledger, e-Faktur): **P4** (roadmap §50–52). P3 menyerahkan kebutuhan bahan banquet sebagai *procurement requirement* dan invoice sebagai dokumen sumber AR (kontrak K1–K2, [§5.4.2](#542-kontrak-dengan-p4))
- CMS website penuh (halaman bebas, banner, news, gallery, konten bilingual): **P4** (roadmap §53). P3 menampilkan event, promosi, dan paket dari data terstruktur
- Integration Layer lanjutan (multiple payment provider, tax/Coretax, hardware tambahan): **P4** (roadmap §54)
- Payroll sales commission dan service charge: **P5** (roadmap §58–59). P3 menghitung, menyetujui, dan mengekspor komisi
- Advanced CRM & Loyalty: loyalty tier otomatis berbasis aturan kompleks, campaign automation multi-langkah, NPS/complaint SLA analytics, sales performance analytics: **P5** (roadmap §59)
- Advanced Package: multi-business package dengan optimasi kapasitas, package profitability: **P5** (roadmap §61)
- Advanced Tournament: multi-round, format tim lanjutan, historical tournament analytics, integrasi federasi: **P5** (roadmap §62)
- Advanced BI: **P5** (roadmap §60)
- Dynamic pricing berbasis permintaan: **P7** (roadmap §64)
- Restaurant table reservation dan full hotel PMS: Out of Scope (roadmap §73)

---

# 4. Users & Personas

Role mengikuti Product Overview §44. Role template sudah di-seed di P0 (FR-IAM-06); P3 mengisi permission domain baru dan mengaktifkan role yang mulai login.

| Persona | Label role (UI) | Kebutuhan di P3 |
|---|---|---|
| Manajemen | General Manager, Club Manager, Resort Manager | Sales pipeline, konversi, banquet revenue, Top Spender, Commercial & Loyalty KPI |
| **Sales** | Sales Executive | **Mulai aktif penuh di P3**: lead, opportunity, quotation, follow-up, target & komisi |
| **Banquet** | Banquet Manager, Banquet Sales | Inquiry, quotation, menu, BEO, payment schedule, venue calendar |
| **Event** | Event Manager, Event Staff | Event schedule, checklist, vendor, peserta, registrasi tamu di hari H |
| CRM & marketing | CRM Admin, Marketing Staff | Segmentasi lanjutan, campaign, promosi, loyalty, complaint ticket |
| Keuangan | Finance Manager, Accountant | Unified folio, invoice, corporate billing & aging, payment schedule, night audit, komisi, liability loyalty |
| Kasir & front office | Cashier, Front Desk, Reservation Staff | Cashier shift lintas lini, split bill lintas lini, promosi & redemption poin di titik bayar |
| Pengelola golf | Golf Manager, Golf Admin | Tournament: setup, registrasi, draw, scoring, leaderboard, prize |
| Starter & marshal | Starter / Marshal | Shotgun start, kontrol flight tournament |
| Commercial | Outlet Manager, POS Staff, Kitchen Staff | Promosi otomatis di POS, produksi banquet dari BEO di KDS |
| Member / tamu | — (Member Portal) | Poin & reward, penawaran, registrasi event & tournament, leaderboard, complaint |
| Non-member / calon klien | — (Website, link quotation) | Inquiry wedding/banquet/event, terima quotation online, bayar DP |
| Contact corporate | — (link invoice/quotation) | Menerima quotation, invoice, dan statement perusahaan |
| Tim internal OneClub | Platform Admin | Migrasi gelombang 3, Release 3 |

---

# 5. Scope

## 5.1 In Scope

| Area | Referensi roadmap | Epic |
|---|---|---|
| Lead Management | §40.1 | EP-01 |
| Sales Pipeline & Opportunity | §40.2 | EP-02 |
| Quotation & Conversion | §40 (Features), §47 | EP-03 |
| Sales Target & Commission | §40 (Features) | EP-04 |
| Advanced Customer 360 & Segmentation | §41 | EP-05 |
| Customer Engagement: Campaign & Reminder | §40–41 | EP-06 |
| Feedback, NPS & Complaint Ticket | §41 (Interaction) | EP-07 |
| Top Spender & Leaderboard | §42 | EP-08 |
| Loyalty Foundation | §43 | EP-09 |
| Pricing & Promotion Engine | §44, §65 Pricing Policy | EP-10 |
| Package Management | §45 | EP-11 |
| Event Management | §46 | EP-12 |
| Banquet, MICE & Wedding | §47, §65 Banquet Policy | EP-13 |
| Banquet Event Order (BEO) | §47 | EP-14 |
| Banquet Venue & Event Resource | §32 (lanjutan PRD P2 FR-RSV-01) | EP-15 |
| Tournament Management | §48 | EP-16 |
| Unified Folio & Billing Expansion | §49 | EP-17 |
| Cashier Shift, End-of-Day & Night Audit | §49 | EP-18 |
| Member & Guest App P3 | §66.2 | EP-19 |
| Website P3 | §66.1 | EP-20 |
| Operational Interfaces P3 | §66.4 | EP-21 |
| Club Policies P3 | §65 | EP-22 |
| KPI, Dashboard & Reports | §69, §66.5 | EP-23 |
| Integrasi P3 | §40.1, §49, §54 (sebagian) | EP-24 |
| Migrasi gelombang 3 | §72 | EP-25 |
| Production Readiness (Release 3) | §75 | EP-26 |

## 5.2 Batas P3 vs P4/P5 per Area

| Area | Dibangun di P3 | Lanjut di P4/P5+ |
|---|---|---|
| CRM & Sales | Lead, pipeline, quotation, konversi, target & komisi (hitung + approve + export), campaign, reminder, complaint ticket + SLA | Campaign automation multi-langkah, NPS & SLA analytics, sales performance analytics (P5) |
| Loyalty | Loyalty account, earning rule per lini, ledger poin, redemption sebagai tender, expiry, tier & reward **foundation** (tier manual/threshold sederhana) | Tier otomatis kompleks, reward eligibility engine, VIP segmentation (P5) |
| Top Spender | Ranking lintas lini dari folio, filter, leaderboard rounds & aktivitas, dasar undangan VIP | Engagement otomatis berbasis Top Spender (P5) |
| Pricing & Promotion | Promotion rules (happy hour, Buy N, bundle, member discount, promo code, period), stacking & prioritas, peak/off-peak/holiday | Dynamic pricing (P7) |
| Package | Package engine lintas lini: komponen, harga, availability, booking all-or-nothing, consumption, revenue allocation, BOM link | Package profitability, optimasi kapasitas/time block lanjutan (P5) |
| Event & Banquet | Event, banquet, MICE, wedding, BEO, payment schedule, BOM per pax → *procurement requirement* | Purchase requisition/PO dari requirement, potong stok aktual (P4) |
| Tournament | Single/multi-day stroke play & stableford, gross/net, shotgun start, registrasi, draw, live leaderboard, sponsor, prize, Hall of Fame otomatis | Format tim lanjutan, multi-round series/order of merit, historical analytics, integrasi federasi (P5) |
| Billing | Unified folio, invoice, payment schedule, corporate billing & aging operasional, cashier shift, night audit | GL, AR control account, jurnal otomatis, e-Faktur (P4); service charge distribution (P4–P5) |
| Website | Halaman Wedding & Banquet, Events, Promotions, Packages lintas lini; inquiry → Lead; registrasi event/tournament | CMS penuh, News, Gallery, konten bilingual dikelola (P4) |
| Member App | Loyalty, Offers, Events, Tournaments, Support | Personalized offers berbasis rekomendasi, campaign automation (P5/P7) |

## 5.3 Dependensi ke P0–P2

| Kapabilitas sebelumnya | Dipakai P3 untuk |
|---|---|
| Enabled Modules & Feature Flags (P0 FR-INS-04/05) | Mengaktifkan Banquet & Event, CRM Sales, Loyalty, Promotions per gelombang rilis |
| Approval (P0 EP-06) | Diskon quotation di atas batas, komisi, adjust poin, write-off invoice, reopen business day, override promo |
| Notification + WhatsApp BSP (P0 EP-05, P1 FR-INT-P1-02) | Campaign, reminder DP/pelunasan, quotation link, renewal & birthday, ticket update |
| Outbox & Background Job (P0 EP-09) | Earning poin, ranking Top Spender, SLA timer, payment reminder, night audit job |
| Reporting Foundation (P0 EP-10) + read model P1–P2 | KPI dan laporan P3 |
| Customer, Corporate Account & Nominee (P1 EP-01) | Lead → customer, corporate billing, nominee pada event perusahaan |
| Pricing Foundation + snapshot (P1 EP-08, P2 EP-02) | Promotion engine, package pricing, banquet per pax ++ |
| Billing P1 + Billing Extension P2 (EP-03) | Unified folio, invoice, deposit/DP, refund, member account |
| Scorecard, Handicap, Hall of Fame (P2 EP-08, EP-11) | Tournament scoring, net score, champion otomatis |
| Reservation Engine exclusive + capacity (P2 EP-01) | Banquet venue, event, paket lintas lini |
| Meeting Room, Bungalow, VIP Suite (P2 EP-16–18) | Komponen paket corporate, wedding, stay & golf |
| POS, KDS, BOM/Recipe foundation (P2 EP-20–22) | Promosi di POS, produksi banquet di KDS, BOM per pax |
| Voucher & Prepaid (P2 EP-19) | Voucher F&B di paket pre-wedding, promo voucher, reward loyalty |
| CRM Foundation (P2 EP-24) | Customer 360, segmentasi, interaction, feedback, campaign foundation |

## 5.4 Pengembangan Paralel dengan P4

PRD P2 §5.4.1 menyebut P4 sebagai **gelombang 2** yang melanjutkan module `inventory`. PRD ini mengikuti pembagian itu: **P3 dan P4 berjalan bersamaan** setelah Release 2 live. Aturan kerja PRD P2 §5.4 (kepemilikan per sub-package, `CODEOWNERS`, kontrak aditif, contract test) berlaku juga untuk P3 ↔ P4.

> Catatan: PRD P2 merujuk **Tech Doc §12.3** untuk aturan gelombang, tetapi `OneClub — Technical Documentation.md` saat ini tidak memuat §12.3 ([bagian 6](#6-hasil-pengecekan-dokumen-sumber) #1). Bagian ini menuliskan aturan yang dipakai sampai Tech Doc diperbarui.

### 5.4.1 Kepemilikan Module *(usulan)*

| Module (Tech Doc §4.1) | Pemilik gelombang 2 | Bagian P3 | Aturan |
|---|---|---|---|
| `banquet` (baru) | **P3** | EP-12–15: event, banquet, BEO, venue | Module & schema baru. Mencakup Event umum karena Tech Doc §4.1 tidak punya module `event` |
| `crm` | **P3** | `crm/sales` (lead, opportunity, quotation, target, komisi), `crm/engagement` (campaign, reminder, ticket), `crm/loyalty`, `crm/topspender` | Melanjutkan module P1–P2; perubahan pada `crm` milik P1–P2 hanya aditif |
| `commercial` | **P3** (promotion, package) | `commercial/promotion`, `commercial/package` | Pricing resolve P1–P2 diperluas lewat titik promosi; POS P2 memanggil promotion engine |
| `golf` | **P3** (tournament) | `golf/tournament` | Memakai scorecard P2 lewat interface publik; tidak menulis tabel scoring |
| `reservation` | P2 → **P3** | Resource type Banquet Venue & Event, hold Tentative/Definite | Hanya aditif; contract test P1–P2 tetap lulus |
| `billing` | **P3** | Unified folio, invoice, payment schedule, corporate billing, cashier shift, business day | Review 2 orang (Tech Doc §13 quality gate) |
| `inventory`, `procurement`, `accounting`, `cms` | **P4** | — | P3 hanya memanggil kontrak K1–K6 |
| `reporting`, navigasi shell, `internal/app` | Bersama | Read model & widget P3 | Aditif saja |
| `web/apps/{backoffice,member,ops,web}` | Bersama | Route folder per domain | Aditif per folder route |

**Arah dependensi.** Tech Doc §4.2 #3 mengizinkan lini bisnis → core → customer → back office, dan arah sebaliknya hanya lewat domain event. Akibatnya:

- `crm` (lapis customer) **tidak boleh memanggil** `banquet` atau `golf` (lapis lini bisnis). Konversi quotation menjadi event, booking, atau registrasi tournament terjadi lewat event `crm.quotation_accepted` yang di-subscribe module lini bisnis.
- `commercial/package` (core) mengalokasikan resource lewat `reservation` (core). Detail per lini (stay, golf, banquet) dibuat oleh module lini bisnis dari event `commercial.package_booked`.
- Earning loyalty dipicu event `billing.payment_settled`. Redemption poin sebagai tender dipanggil `billing` lewat interface publik `crm/loyalty` (billing → customer diizinkan).

### 5.4.2 Kontrak dengan P4

| # | Kontrak | Pemilik | Dibutuhkan oleh | Isi minimal |
|---|---|---|---|---|
| K1 | **Procurement requirement** dari BEO dan paket | P3 publish, P4 consume | P4 Purchase Requisition | Event `banquet.beo_issued` / `banquet.beo_revised` berisi item, jumlah, UOM, tanggal butuh, outlet/dapur (dari BOM per pax P2) |
| K2 | **Invoice & receivable** sebagai dokumen sumber AR | P3 | P4 Accounts Receivable | Invoice, payment allocation, credit note, write-off; event `billing.invoice_issued`, `billing.invoice_paid`, `billing.invoice_voided` |
| K3 | **Revenue allocation** per komponen | P3 | P4 Revenue & Tax | Paket, banquet, promosi, poin loyalty: komponen pendapatan, diskon, liability |
| K4 | **Business day closed** | P3 | P4 jurnal harian | Event `billing.business_day_closed` dengan ringkasan pendapatan, pembayaran, deposit, liability per hari |
| K5 | **Data terstruktur untuk CMS** | P3 | P4 CMS | API publik event, promosi, paket, tarif yang dirujuk halaman CMS |
| K6 | **Consumption** dari paket & banquet | P3 publish | P4 potong stok | Event `commercial.package_consumed`, `banquet.event_completed` dengan baris BOM aktual (pax final) |

Sebelum Accounting P4 live, K2–K4 tetap mengalir ke **Accounting Export** P1–P2 (diperluas di EP-24).

### 5.4.3 Urutan Kerja P3

```text
Tidak bergantung P4         → Lead, pipeline, quotation, campaign, ticket, Top Spender, loyalty,
                              promotion engine, package engine, event & banquet, BEO, tournament
Butuh kontrak P4 (mock)     → Procurement requirement (K1), invoice → AR (K2), business day → jurnal (K4)
Butuh P4 merge              → Potong stok dari banquet/paket (K6), halaman CMS memuat data P3 (K5)
```

---

# 6. Hasil Pengecekan Dokumen Sumber

Pengecekan silang roadmap, Product Overview, Naming Convention, Technical Documentation, PRD P0–P2, dan kode `oneclub` (branch `staging`, commit `a48e6a3`) menemukan hal berikut. Keputusan di kolom kanan berlaku untuk PRD ini sampai dikonfirmasi di [Open Questions](#16-open-questions).

| # | Temuan | Sumber | Keputusan PRD P3 |
|---|---|---|---|
| 1 | **Tech Doc §12.3 tidak ada.** PRD P2 merujuk Tech Doc §12.3 (gelombang paralel, aturan kepemilikan, "POS/BOM di P3"), tetapi versi Tech Doc yang ada berakhir di §15 tanpa §12.3 | PRD P2 §5.4, §6 #1; Tech Doc | Aturan gelombang dituliskan di §5.4 PRD ini. Tech Doc perlu menambahkan §12.3. **Disetujui** (§16 #1) |
| 2 | **Fase 3 Product Overview berbeda dengan P3 roadmap.** PO §49 Fase 3 berisi SaaS, POS/Accounting integration, Inventory, Procurement, Accounting, Tournament, Banquet; roadmap memecahnya menjadi P3 (commercial), P4 (back office), dan P6 (SaaS) | PO §49; roadmap §3, §63 | Mengikuti **roadmap**: P3 = commercial & business expansion. SaaS di P6, back office di P4 |
| 3 | **Tournament muncul di P3 dan P5** dengan isi tumpang tindih (shotgun start, scoring, leaderboard, sponsor, prize ada di keduanya) | Roadmap §48 vs §62; PO §49 Fase 3 vs Fase 4 | **P3**: tournament club lengkap (single/multi-day, stroke play & stableford, gross/net, shotgun, live leaderboard, sponsor, prize, Hall of Fame). **P5**: format tim lanjutan, series/order of merit, historical analytics, integrasi federasi |
| 4 | **Package muncul di P3 dan P5** (resource bundle, BOM, reservation allocation, payment schedule, revenue allocation ada di keduanya) | Roadmap §45 vs §61 | **P3**: package engine lintas lini lengkap termasuk revenue allocation (wajib untuk pembukuan). **P5**: profitability dan optimasi kapasitas |
| 5 | **Loyalty P3 vs Advanced Loyalty P5**; Member App P3–P5 memuat Loyalty | Roadmap §43 vs §59; §66.2 | **P3**: foundation (account, earning, ledger, redemption, tier & reward sederhana). **P5**: tier & reward engine lanjutan. Menu Loyalty di Member App tampil mulai P3 |
| 6 | **Corporate AR billing di P3** (roadmap §49) tetapi **Accounts Receivable** (customer invoice, corporate billing, aging) di P4 Accounting | Roadmap §49 vs §52 | **P3**: invoice, payment allocation, statement, aging **operasional** di `billing`. **P4**: AR ledger, jurnal, kontrol akun, e-Faktur memakai invoice P3 sebagai sumber (K2). Tidak ada dua sistem invoice |
| 7 | **Inquiry wedding/banquet → CRM Lead** ada di Website P3 (§66.1) dan di Landing Page P4 (§53) | Roadmap §66.1 vs §53 | **P3**: formulir inquiry dan halaman Wedding & Banquet, Events, Promotions, Packages dari data terstruktur. **P4**: CMS dan konten bebas |
| 8 | **Sales Commission "terhubung ke HRIS"** (PO §29), HRIS/payroll di P5 | PO §29; roadmap §58 | P3 menghitung, menyetujui, dan mengekspor komisi; pembayaran lewat payroll P5 |
| 9 | **Banquet "Procurement requirement"** (roadmap §47) dan **BOM per pax → procurement** (PO §28), Procurement di P4 | Roadmap §47 vs §51 | P3 menghasilkan daftar kebutuhan bahan per event (K1); P4 mengubahnya menjadi purchase requisition |
| 10 | **Shotgun start** ditunda P1 ke P3 (PRD P1 FR-TEE-04), tetapi juga tercantum di Advanced Tournament P5 | PRD P1 FR-TEE-04; roadmap §48, §62 | **P3** (bagian dari tournament club) |
| 11 | **Lead dari Instagram/Facebook/TikTok** di P3, tetapi Integration Layer komunikasi di P4 | Roadmap §40.1 vs §54 | P3: lead dari WhatsApp (BSP P1), formulir website, email, dan **input manual dengan sumber** untuk media sosial. Integrasi Meta/TikTok **ditunda** (keputusan §16 #6) |
| 12 | **Night Audit** di P3, sementara full hotel PMS dan full hotel accounting Out of Scope | Roadmap §49 vs §73 | Night audit = **tutup hari bisnis club** (semua shift kasir tertutup, folio terbuka ditinjau, pendapatan hari itu dibekukan), bukan proses PMS hotel |
| 13 | **Status P3 belum ada di NC §31** (lead, opportunity, quotation, BEO, tentative/definite, ticket, invoice, registrasi, poin) | NC §31 | Usulan status baru di §7.6 |
| 14 | **Management Dashboard NC §23 tidak memuat CRM/Sales**, padahal PO §43 dan roadmap §60 punya CRM KPI dan roadmap §69 menaruh Sales & CRM KPI di P3 | NC §23; PO §43; roadmap §60, §69 | Usulan label **CRM Performance** (mencakup sales) di §7.6 |
| 15 | **Club Policies NC §25** belum punya label untuk Loyalty, Promotion, Event, Tournament, Sales, dan kredit corporate | NC §25 | Usulan label baru di §7.6 |
| 16 | **Event vs Banquet** dibedakan di PO (§27 vs §28) dan NC §11, tetapi Tech Doc §4.1 hanya punya module `banquet` | PO §27–28; NC §11; Tech Doc §4.1 | Satu module `banquet` (label UI **Banquet & Event**); Event adalah entitas induk, Banquet adalah event dengan alur penjualan & BEO |
| 17 | **Tournament adalah tipe Event** (PO §15, roadmap §48), tetapi scoring dan flighting milik `golf` | PO §15; roadmap §48 | Tournament di `golf/tournament`, terhubung ke Event `banquet` lewat `event_id` (venue, catering, sponsor billing). Tidak ada import lintas lini; sinkronisasi lewat event |
| 18 | **Restaurant reservation** masih tercantum di F&B Experience PO §30 | PO §30 vs §50; PRD P2 §6 #2 | Tetap Out of Scope di P3 |
| 19 | **Promosi di POS offline** belum diatur: P2 POS mengandalkan price list cache | PRD P2 FR-POS-11; roadmap §44 | Rule promosi aktif ikut di-cache per shift dan dievaluasi lokal; server memvalidasi saat sync (FR-PRM-09) |
| 20 | **Kondisi kode:** `oneclub` (staging `a48e6a3`) berisi P0 + P1. Module `banquet`, `stay`, `inventory`, `cms` belum ada; P2 baru berupa PRD. `billing.customer_accounts` sudah punya tipe `corporate` dan `crm` sudah punya corporate account & nominee | Repo `oneclub` | P3 dimulai **setelah Release 2**. Corporate account P1 dipakai ulang untuk corporate billing (EP-17) |
| 21 | **"Package" di Booking Common Features** (NC §9) dan menu Package di Commercial (NC §13, §16) | NC §9, §13, §16 | Package dikelola di **Commercial → Packages**; Booking menampilkan pemilihan paket saat membuat booking |

---

# 7. Information Architecture

Label mengikuti `OneClub — Naming Convention.md`. Module dan menu P3 muncul hanya bila module aktif (P0 FR-INS-04) dan user berizin.

## 7.1 Back Office — Module & Menu Baru/Aktif di P3

| Module | Menu baru di P3 | Catatan |
|---|---|---|
| Golf | **Tournaments** (Tournament Schedule, Registration, Participants, Flighting, Tee Assignment, Scoring, Leaderboard, Tournament Packages, Tournament Fees, Sponsors, Prizes, Tournament Reports) | NC §6 |
| Banquet & Event | **Events, Banquet, MICE, Weddings, Packages, Venues, BEO, Event Schedule, Event Checklist, Event Billing, Event Reports** | Module baru aktif (NC §11) |
| Booking | **Banquet & Event**; pemilihan Package di Create Booking | NC §9 |
| CRM | **Leads, Opportunities, Sales Pipeline, Quotations, Campaigns, Promotions, Loyalty, Top Spender, Complaints, Follow-ups, CRM Reports** | Melengkapi Customers, Customer 360, Corporate Accounts, Interactions, Feedback (P1–P2) |
| Commercial | **Promotions, Packages**; **Pricing** (+ Discounts, Promo Codes) | NC §13, §15, §16 |
| Billing & Payment | **Invoices, Corporate Billing, Cashier, Night Audit, Billing Reports**; Folios menjadi folio per customer | NC §18 |
| Reports | **Banquet & Event Reports, CRM Reports**; tambahan Golf, Commercial, Booking Reports | NC §23 |
| Dashboard | **Banquet Performance, CRM Performance** *(usulan label)*; Commercial Performance diperluas | NC §23 + §7.6 |
| Settings | Club Policies: **Banquet Policies, Pricing Policies** + usulan §7.6 | NC §25 |

## 7.2 Operational Staff (`ops`)

| Interface | Menu (NC §29 + usulan) | Pengguna |
|---|---|---|
| Event Operations *(usulan)* | Today's Events, BEO, Event Checklist, Guest Registration, Event Check-in | Event Manager, Event Staff, Banquet Manager |
| Tournament Desk *(usulan)* | Registration Check-in, Draw, Scoring, Leaderboard | Golf Admin, Starter / Marshal |
| Starter | + Shotgun Start *(usulan)* | Starter / Marshal |
| POS | + Apply Promotion, Redeem Points | Cashier, POS Staff |
| Front Desk | + Cashier (shift lintas lini), Folios per customer | Front Desk, Cashier |
| Kitchen | + Banquet Production (dari BEO) | Kitchen Staff |
| Leaderboard Screen *(usulan)* | Live leaderboard tournament (kiosk, read-only) | — (device terdaftar) |

## 7.3 Caddy Application (`caddy`)

Tidak ada menu baru. Saat round tournament, **Scorecard** menampilkan format tournament (stroke play/stableford) dan skor tersinkron ke leaderboard.

## 7.4 Member & Guest Portal (`member`)

Mengikuti Naming Convention §27; item baru P3 ditandai `+`:

```text
Home            + Offers (promosi & paket untuk saya)
Golf            ... (P1–P2), + Tournaments: Register, My Tournaments, Leaderboard
Sport Club      ... (P2)
Stay & Venue    ... (P2), + Packages
+ Events        Upcoming Events, Event Registration, My Events
Bookings
Membership
Voucher & Prepaid
Scores
+ Loyalty       My Points, Tier, Rewards, Points History
Transactions    My Transactions, Payments, Member Charges, Statements, + Invoices
Profile         + Communication Preferences
+ Support       Feedback, Complaints (usulan label)
```

## 7.5 Website (`web`)

Public navigation P3 (NC §26): **Home, Golf, Sport Club, Bungalow, VIP Suite, Meeting & MICE, Wedding & Banquet, Events, Membership, Packages, Promotions, Hall of Fame, Contact, Location**. Booking: **Book Golf, Book Sport Club, Book Bungalow, Book Meeting Room, Book Event**. News dan Gallery menyusul P4 (CMS).

## 7.6 Usulan Tambahan ke Naming Convention

| Usulan label | Lokasi | Alasan |
|---|---|---|
| CRM Performance | Management Dashboard | NC §23 belum punya dashboard CRM/Sales ([bagian 6](#6-hasil-pengecekan-dokumen-sumber) #14) |
| Loyalty Policies, Promotion Policies, Event Policies, Tournament Policies, Sales Policies, Credit Policies | Settings → Club Policies | NC §25 belum punya label ([bagian 6](#6-hasil-pengecekan-dokumen-sumber) #15) |
| Event Operations, Tournament Desk, Leaderboard Screen | `ops` | Interface baru (§7.2) |
| Shotgun Start | Golf → Tournaments, `ops` Starter | PRD P1 FR-TEE-04 |
| Offers, Support | Member Portal | Penawaran personal dan feedback/complaint |
| Communication Preferences | Member Portal → Profile | Opt-in campaign per kanal (UU PDP) |
| Business Day | Billing & Payment → Night Audit | Tanggal bisnis yang ditutup night audit |
| Payment Schedule | Banquet & Event, Billing & Payment | Termin DP & pelunasan (NC §11 Banquet sudah menyebut "Payment Schedule" sebagai fitur) |
| Status **New, Contacted, Qualified, Unqualified, Converted** | NC §31 | Lead |
| Status **Open, Won, Lost** | NC §31 | Opportunity |
| Status **Sent, Accepted, Revised** | NC §31 | Quotation (Draft, Expired, Rejected sudah ada / dipakai) |
| Status **Tentative, Definite** | NC §31 | Event & venue hold (istilah standar banquet) |
| Status **Issued** | NC §31 | BEO, Invoice |
| Status **In Progress, Escalated, Resolved, Closed** | NC §31 | Complaint Ticket |
| Status **Registered, Waitlisted, Withdrawn** | NC §31 | Registrasi event & tournament |
| Status **Partially Paid, Paid, Overdue, Void** | NC §31 | Invoice & payment schedule |
| Status **Earned, Redeemed** | NC §31 | Transaksi poin loyalty |
| Status **Scheduled** | NC §31 | Campaign |

---

# 8. Functional Requirements

Format sama dengan PRD P0–P2. Prioritas: **Must** = wajib untuk go-live Release 3, **Should** = sebaiknya ada di P3, boleh bergeser ke awal P4/P5 bila club setuju. Prefix ID baru dipakai agar tidak bentrok; area yang memperluas fase sebelumnya memakai akhiran `-P3-`.

## EP-01 — Lead Management

**User story:** Sebagai Sales Executive, saya ingin semua inquiry dari WhatsApp, media sosial, website, walk-in, dan referral member masuk ke satu daftar lead yang ditugaskan ke saya, agar tidak ada calon pelanggan yang terlewat.

Sumber lead (roadmap §40.1): WhatsApp, Instagram, Facebook, TikTok, Website Form, Walk-in, Member Referral; ditambah Email dan Phone *(usulan)*.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-LEAD-01 | **Lead** dengan nama, kontak, sumber, kanal, minat (Wedding, MICE / Meeting, Corporate Membership, Sport Membership, Corporate Golf, Tournament, Birthday, Social Event, Golf Membership), tanggal acara, perkiraan pax, budget, catatan | Must |
| FR-LEAD-02 | **Capture otomatis** dari formulir website (EP-20) dan pesan WhatsApp masuk ke nomor bisnis club (BSP P1); **input manual** cepat untuk DM media sosial dan walk-in dengan sumber wajib | Must |
| FR-LEAD-03 | **Deduplication** terhadap customer dan lead lain (nomor HP, email) memakai aturan P1 FR-CUS-03; lead yang cocok dengan customer tertaut ke Customer 360 | Must |
| FR-LEAD-04 | **Member Referral**: member perujuk tercatat; referral yang menjadi deal terlihat untuk reward loyalty (EP-09) | Must |
| FR-LEAD-05 | **Lead Assignment** manual atau otomatis per minat/lini (round-robin atau sales tetap per lini) *(usulan rule)* | Must |
| FR-LEAD-06 | **First response SLA** per lini (mis. 1 jam pada jam kerja) dengan pengingat ke sales dan eskalasi ke manager bila terlewat *(usulan target)* | Must |
| FR-LEAD-07 | **Qualify / Disqualify** dengan alasan; lead Qualified dikonversi menjadi **Opportunity** (EP-02) dan customer (bila belum ada) | Must |
| FR-LEAD-08 | **Follow-up** terjadwal (telepon, WhatsApp, site visit) dengan pengingat; histori interaksi masuk Interaction History P2 | Must |
| FR-LEAD-09 | **Consent** komunikasi pemasaran dicatat dari sumber lead (UU PDP); lead tanpa consent tidak masuk audience campaign | Must |
| FR-LEAD-10 | Pemindahan lead saat sales cuti/keluar dengan histori utuh | Must |

**Acceptance criteria:**

- Inquiry wedding dari formulir website tampil sebagai Lead dengan sumber Website Form dan ditugaskan ke sales wedding dalam ≤ 1 menit.
- Lead dengan nomor HP yang sudah terdaftar sebagai customer menampilkan peringatan duplikat dan tertaut ke Customer 360 yang ada.
- Lead yang tidak direspons melewati SLA memunculkan pengingat ke sales dan eskalasi ke Banquet Manager.

## EP-02 — Sales Pipeline & Opportunity

**User story:** Sebagai General Manager, saya ingin melihat pipeline penjualan per lini dan peluang yang akan closing bulan ini, agar target pendapatan dapat dipantau.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-PIPE-01 | **Pipeline per lini** (roadmap §40.2): Wedding, MICE / Meeting, Corporate Membership, Sport Membership, Corporate Golf, Tournament; stage & probabilitas configurable per pipeline; default sesuai [§16.1](#161-default-tahap-pipeline-keputusan-2) | Must |
| FR-PIPE-02 | **Opportunity**: customer/corporate, lini, nilai perkiraan, probabilitas per stage, tanggal acara, tanggal perkiraan closing, sales pemilik | Must |
| FR-PIPE-03 | **Kanban & daftar** pipeline dengan filter sales, lini, periode; perpindahan stage tercatat | Must |
| FR-PIPE-04 | **Won / Lost** dengan alasan kalah (harga, tanggal penuh, kompetitor, batal); Won wajib terhubung ke quotation yang diterima | Must |
| FR-PIPE-05 | **Activity** (call, meeting, site visit, food tasting) dan **Follow-up** pada opportunity | Must |
| FR-PIPE-06 | **Ketersediaan tanggal** venue/ballroom terlihat dari opportunity tanpa pindah layar (EP-15) | Must |
| FR-PIPE-07 | **Forecast** pendapatan tertimbang per bulan dan per lini | Should |

**Acceptance criteria:**

- Pipeline Wedding menampilkan total nilai tertimbang yang sama dengan Σ (nilai × probabilitas stage) seluruh opportunity terbuka.

## EP-03 — Quotation & Conversion

**User story:** Sebagai Banquet Sales, saya ingin membuat quotation dari paket dan menu yang berlaku, mengirimnya lewat link, dan saat diterima langsung menjadi event dan tagihan DP, agar tidak ada pengetikan ulang.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-QUO-01 | **Quotation** dari opportunity atau langsung dari customer: item dari paket (EP-11), banquet package & menu (EP-13), tarif (P1–P2), item bebas dengan permission; harga dari pricing engine dengan snapshot | Must |
| FR-QUO-02 | **Diskon** per item atau total; di atas batas Sales Policies memerlukan approval (P0 EP-06) | Must |
| FR-QUO-03 | **Versi quotation** (Revised): versi lama tetap tersimpan; hanya satu versi aktif | Must |
| FR-QUO-04 | **Masa berlaku** quotation dan **option date** untuk hold venue Tentative (EP-15) | Must |
| FR-QUO-05 | **Kirim** sebagai PDF dan **link aman** (WhatsApp/email); customer dapat **menerima** quotation lewat link dengan nama dan persetujuan syarat; IP & waktu tercatat | Must |
| FR-QUO-06 | **Conversion** saat Accepted: event `crm.quotation_accepted` → membuat Event/Banquet (EP-13), Package Booking (EP-11), Membership Application (P1–P2), atau Tournament Registration (EP-16) sesuai lini; payment schedule DP terbit (EP-17) | Must |
| FR-QUO-07 | Konversi idempotent: quotation yang sama tidak menghasilkan event atau tagihan ganda | Must |
| FR-QUO-08 | Template dokumen quotation per lini dengan branding instance (P0) dan syarat & ketentuan dari Banquet Policies | Must |
| FR-QUO-09 | Tanda tangan elektronik pihak ketiga *(usulan)* | Should |

**Acceptance criteria:**

- Quotation Intimate Wedding Rp28.000.000 nett diterima lewat link: event wedding Tentative→Definite terbentuk, ballroom/Jade Room terkunci, dan tagihan DP 30% = Rp8.400.000 terbit sekali saja meskipun link dibuka dan diterima dua kali.
- Diskon 15% dengan batas Sales Policies 10% tidak dapat dikirim sebelum disetujui Banquet Manager.

## EP-04 — Sales Target & Commission

| ID | Requirement | Prioritas |
|---|---|---|
| FR-COM-01 | **Sales Target** per sales, per lini, per periode (bulan/kuartal) dalam nilai dan jumlah deal | Must |
| FR-COM-02 | **Achievement** dan komisi diakui saat deal Won **lunas** (keputusan §16 #4) | Must |
| FR-COM-03 | **Commission Scheme** configurable: persen per lini, tiered per pencapaian, flat per deal; berlaku efektif per tanggal | Must |
| FR-COM-04 | **Commission Statement** per periode per sales, approval Finance, status Approved → Paid; export untuk payroll (P5) | Must |
| FR-COM-05 | Penyesuaian bila deal dibatalkan/refund setelah komisi dihitung (clawback) dengan audit | Must |

**Acceptance criteria:**

- Sales dengan deal wedding Rp88.000.000 dan skema 1% menerima komisi Rp880.000 pada periode pengakuan; pembatalan setelahnya menghasilkan clawback −Rp880.000 di periode berikutnya.

## EP-05 — Advanced Customer 360 & Segmentation

Melanjutkan CRM Foundation P2 (EP-24).

| ID | Requirement | Prioritas |
|---|---|---|
| FR-C360-01 | Customer 360 menampilkan **Banquet / Event, Lead & Opportunity, Quotation, Campaign, Loyalty, Complaint, Tournament** (placeholder P2 diisi) | Must |
| FR-C360-02 | **Segmentasi** dengan dimensi roadmap §41: Member Type, Frequency, Spend Value, Age, Residence, Customer Type, Corporate, **Outlet**, segmen configurable lain (mis. peserta tournament, pernah wedding) | Must |
| FR-C360-03 | **Segmen dinamis** (dihitung ulang terjadwal) dan **statis** (snapshot); jumlah anggota dan export | Must |
| FR-C360-04 | **Corporate 360**: ringkasan perusahaan (nominee, event, tagihan, piutang, aktivitas golf nominee) | Must |
| FR-C360-05 | **Interaction** otomatis dari WhatsApp/email campaign, quotation, ticket, dan event (melengkapi P2 FR-CRM-02) | Must |
| FR-C360-06 | Masking dan permission data sensitif mengikuti P1 FR-CUS-09 dan P2 FR-PRF-05 | Must |

## EP-06 — Customer Engagement: Campaign & Reminder

| ID | Requirement | Prioritas |
|---|---|---|
| FR-CMP-01 | **Campaign** WhatsApp (template BSP) dan email ke segmen, dengan jadwal kirim, personalisasi variabel, dan preview | Must |
| FR-CMP-02 | **Consent & opt-out** per kanal: hanya penerima dengan opt-in; tautan berhenti berlangganan; daftar suppression | Must |
| FR-CMP-03 | **Frequency cap** per customer per periode *(usulan)* | Should |
| FR-CMP-04 | **Tracking**: terkirim, terkirim ke device, dibaca (status BSP), klik link, konversi (booking/redeem promo dalam N hari) | Must |
| FR-CMP-05 | **Promotion & Voucher** sebagai isi campaign: promo code unik per penerima atau voucher P2 | Must |
| FR-CMP-06 | **Renewal Reminder** membership (melengkapi P1–P2) dan **Birthday Reminder** (ucapan + penawaran) terjadwal; aturan otomatis lanjutan di P5 | Must |
| FR-CMP-07 | Throttling sesuai batas BSP dan antrean pengiriman dengan retry (River) | Must |
| FR-CMP-08 | Persetujuan campaign sebelum kirim ke segmen > N penerima *(usulan)* | Should |

**Acceptance criteria:**

- Campaign ke segmen 2.000 penerima hanya mengirim ke yang opt-in WhatsApp; penerima yang berhenti berlangganan tidak menerima campaign berikutnya.

## EP-07 — Feedback, NPS & Complaint Ticket

| ID | Requirement | Prioritas |
|---|---|---|
| FR-TKT-01 | **Complaint Ticket** dari staff, Member App (Support), website, atau feedback skor rendah (P2 FR-CRM-04): kategori, lini, prioritas, deskripsi, lampiran | Must |
| FR-TKT-02 | **SLA** per kategori/prioritas (waktu respons pertama dan penyelesaian) dengan timer jam kerja | Must |
| FR-TKT-03 | **Escalation** bertingkat bila SLA terlewat (petugas → manager lini → GM) dengan notifikasi | Must |
| FR-TKT-04 | Status **Open → In Progress → Resolved → Closed**; reopen oleh customer dalam N hari; komunikasi ke customer tercatat | Must |
| FR-TKT-05 | **Kompensasi** (voucher, poin, refund) dari ticket lewat approval, tercatat di ticket | Should |
| FR-TKT-06 | **NPS** per lini dan periode dari survei P2; tren dan komentar | Must |

**Acceptance criteria:**

- Ticket prioritas High yang belum direspons dalam SLA otomatis tereskalasi ke manager lini dan tercatat di histori ticket.

## EP-08 — Top Spender & Leaderboard

| ID | Requirement | Prioritas |
|---|---|---|
| FR-TOP-01 | **Ranking** bulanan, kuartalan, tahunan berdasarkan nilai belanja dari folio/billing (bukan dari POS terpisah) | Must |
| FR-TOP-02 | **Spend per lini**: Golf, F&B, Sport, Bungalow, Banquet (dan lini lain P2) | Must |
| FR-TOP-03 | **Filter**: member, corporate, outlet, segmen | Must |
| FR-TOP-04 | **Leaderboard** lain: Most Rounds, Most Active Member | Must |
| FR-TOP-05 | Aksi dari daftar: tambah ke segmen, undangan VIP (campaign), catatan untuk tier loyalty | Must |
| FR-TOP-06 | **Akses internal saja** (sales & manajemen) dengan permission khusus; tidak tampil di kanal publik/member | Must |
| FR-TOP-07 | Refund, void, dan credit note mengurangi nilai belanja; pembayaran dengan poin/voucher dihitung sesuai aturan yang dikonfigurasi *(usulan)* | Must |

**Acceptance criteria:**

- Total spend customer #1 bulan September sama dengan Σ charge net folio customer tersebut di bulan itu dikurangi refund.

## EP-09 — Loyalty Foundation

**User story:** Sebagai member, saya ingin mendapat poin dari setiap transaksi di semua outlet dan bisa menukarnya, agar ada alasan untuk kembali.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-LOY-01 | **Loyalty Account** per customer (member dan non-member terdaftar) dengan opt-in | Must |
| FR-LOY-02 | **Earning Rule** per lini/outlet/produk: poin per nominal (mis. 1 poin per Rp10.000 net) *(usulan contoh)*, pengali per segmen/tier, poin aktivitas (round selesai, event, referral) | Must |
| FR-LOY-03 | **Points Ledger** append-only: Earned, Redeemed, Expired, Adjusted, Reversed; saldo dihitung dari ledger | Must |
| FR-LOY-04 | Earning dipicu `billing.payment_settled` (bukan saat order) dan idempotent per pembayaran; refund membalik poin | Must |
| FR-LOY-05 | **Redemption**: poin sebagai tender di POS, front desk, member app (nilai tukar configurable) dan penukaran **Reward** (voucher P2, merchandise, layanan) | Must |
| FR-LOY-06 | **Expiry** poin (mis. 12 bulan bergulir) dengan notifikasi H-30 *(usulan)* | Must |
| FR-LOY-07 | **Tier foundation**: tier (mis. Silver, Gold, Platinum) dengan ambang poin/spend per periode; evaluasi berkala; benefit dasar (pengali poin) | Must |
| FR-LOY-08 | **Liability** poin dinilai sebesar nilai tukar; saldo liability harian dapat direkonsiliasi; masuk accounting export | Must |
| FR-LOY-09 | **Adjust Points** manual lewat approval dengan alasan | Must |
| FR-LOY-10 | Anti-fraud: poin tidak bisa ditukar pada transaksi yang sama sebelum settle; batas redemption per hari *(usulan)* | Should |

**Acceptance criteria:**

- Pembayaran settle Rp1.250.000 dengan aturan 1 poin/Rp10.000 menghasilkan 125 poin sekali saja meskipun webhook diterima tiga kali; refund penuh membalik 125 poin.
- Total saldo poin × nilai tukar = Loyalty Liability Report di akhir hari.

## EP-10 — Pricing & Promotion Engine

**User story:** Sebagai Outlet Manager, saya ingin promosi seperti happy hour dan "Buy 2 Only 200K" berjalan otomatis sesuai jam dan hari, agar kasir tidak menghitung manual.

Melengkapi pricing P1–P2. Dimensi tarif roadmap §44 yang belum lengkap di P2 ditutup di sini.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-PRC-P3-01 | Melengkapi rate rule: **Peak / Off-Peak Rate**, **Holiday Rate**, **Package Rate**, **Corporate Rate** per corporate account (contract rate) | Must |
| FR-PRM-01 | **Promotion** dengan tipe: Percentage / Amount Discount, **Happy Hour** (jam & hari), **Buy N Get X**, **Buy N Price X**, **Bundle** (kombinasi produk/layanan dengan harga tetap), **Member Discount**, **Promo Code**, **Period-based Promotion** | Must |
| FR-PRM-02 | **Scope**: lini, outlet, produk/kategori, resource type, segmen, kanal (POS, member app, website, back office), hari, jam, tanggal berlaku | Must |
| FR-PRM-03 | **Stacking & priority**: aturan apakah promosi dapat digabung; urutan prioritas deterministik; maksimal satu promosi per item kecuali dinyatakan stackable | Must |
| FR-PRM-04 | **Promo Code**: kode umum atau unik per penerima, batas pakai total & per customer, minimum pembelian; rate limit pengecekan kode | Must |
| FR-PRM-05 | **Budget / quota** promosi (jumlah redemption atau nilai diskon maksimum) | Should |
| FR-PRM-06 | Promosi diterapkan otomatis di **POS, booking, quotation, website, member app**; kasir dapat melihat dan, dengan permission, menolak promosi | Must |
| FR-PRM-07 | **Pricing Snapshot** mencatat promosi, versi rule, dan nilai diskon per baris (immutable) | Must |
| FR-PRM-08 | **Simulasi** promosi sebelum aktif (contoh harga per skenario) | Should |
| FR-PRM-09 | **POS offline**: rule promosi aktif di-cache per shift dan dievaluasi lokal; server memvalidasi saat sync dan menandai selisih untuk review *(usulan)* | Must |
| FR-PRM-10 | **Approval** untuk promosi baru/berubah sebelum aktif | Must |

**Acceptance criteria:**

- Promo "Buy 2 Only 200K" untuk produk tertentu berlaku 16.00–19.00 weekday dan 14.00–17.00 weekend *(contoh jam)*: dua item pada jam promo dibayar Rp200.000; di luar jam memakai harga normal; terminal POS offline menghasilkan harga yang sama.
- Dua promosi non-stackable pada item yang sama: hanya yang prioritasnya lebih tinggi diterapkan, dan hasilnya sama di POS, website, dan member app.

## EP-11 — Package Management

**User story:** Sebagai Reservation Staff, saya ingin menjual paket Stay & Golf dalam satu transaksi yang mengunci bungalow, tee time, dan caddy sekaligus, dengan pendapatan terbagi ke lini yang benar.

Contoh paket (roadmap §45): Golf Package (Tee Time, Caddy, Golf Cart, Lunch), Stay & Golf (Bungalow, Tee Time, Caddy, Breakfast), Corporate Package (Meeting Room, Bungalow, Golf, Catering), Wedding Package (Ballroom, Buffet, VIP Family, Food Stall, Family Room, Bungalow, Food Tasting, Technical Meeting).

| ID | Requirement | Prioritas |
|---|---|---|
| FR-PKG-01 | **Package Types** dan **Package Components**: layanan (tee time, court, kelas), resource (bungalow, meeting room, venue, VIP suite, golf cart), produk/menu (lunch, breakfast, buffet per pax), voucher, layanan non-stok (food tasting, technical meeting) | Must |
| FR-PKG-02 | **Package Pricing**: harga tetap, per pax (minimum pax), per malam/durasi, nett atau ++; tambahan opsional (add-on) dengan harga | Must |
| FR-PKG-03 | **Package Availability**: tanggal jual, hari berlaku, kuota per hari, segmen yang boleh membeli, kanal | Must |
| FR-PKG-04 | **Package Booking** dengan alokasi **all-or-nothing** semua resource lewat Reservation Engine (P2 FR-RSV-05); detail per lini dibuat dari event `commercial.package_booked` | Must |
| FR-PKG-05 | **Revenue Allocation** per komponen (nilai tetap, persen, atau proporsional harga standalone) dengan komponen sisa menampung pembulatan; total alokasi = harga paket | Must |
| FR-PKG-06 | **Package Consumption**: status per komponen (Unused, Consumed, Expired); komponen dipakai di lini masing-masing (check-in golf, check-in bungalow, order lunch) | Must |
| FR-PKG-07 | **BOM link** per komponen (P2 FR-BOM-06) untuk kebutuhan bahan dan potong stok P4 (K6) | Must |
| FR-PKG-08 | Perubahan dan pembatalan paket mengikuti policy per komponen; refund parsial per komponen yang belum dipakai | Must |
| FR-PKG-09 | **Package History** dan versi paket; booking menyimpan versi yang dibeli | Must |

**Acceptance criteria:**

- Stay & Golf untuk tanggal saat tee time penuh ditolak seluruhnya; tidak ada bungalow yang tertahan.
- Paket dengan harga Rp3.500.000 *(contoh)* dialokasikan ke bungalow, green fee, caddy fee (liability), dan breakfast dengan total alokasi tepat Rp3.500.000.

## EP-12 — Event Management

Event types (roadmap §46): Corporate Gathering, Golf Tournament, Wedding, Birthday, Kids Birthday, Family Gathering, Company Outing, Seminar, Meeting, Community Event, Social Event.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-EVT-01 | **Event Creation**: tipe, nama, customer/corporate, sales, tanggal & jam, pax, status (Tentative, Definite, Completed, Cancelled) | Must |
| FR-EVT-02 | **Event Schedule** (rundown) per sesi dengan venue dan penanggung jawab | Must |
| FR-EVT-03 | **Venue** satu atau lebih (EP-15) plus resource tambahan: bungalow, golf (tee time/blok tee sheet), meeting room, golf cart, equipment | Must |
| FR-EVT-04 | **Participants** dan **Guest Registration** (daftar tamu, impor, link registrasi publik untuk event terbuka), check-in tamu di hari H (QR) | Must |
| FR-EVT-05 | **Package, Catering, Equipment** dari EP-11, EP-13, P2 | Must |
| FR-EVT-06 | **Vendors** (dekorasi, MC, band, fotografer) dengan kontak, layanan, dan catatan; pembayaran vendor di P4 | Must |
| FR-EVT-07 | **Event Checklist** per tipe event (template) dengan PIC dan tenggat; item terlambat diingatkan | Must |
| FR-EVT-08 | **Event Payment** lewat payment schedule & folio event (EP-17) | Must |
| FR-EVT-09 | **Event Report**: pax rencana vs aktual, pendapatan per komponen, pembayaran, outstanding | Must |
| FR-EVT-10 | **Event calendar** lintas venue untuk sales, banquet, dan operasional | Must |

## EP-13 — Banquet, MICE & Wedding

**User story:** Sebagai Banquet Manager, saya ingin alur inquiry sampai final billing berjalan di satu tempat dengan menu, pax, dan pembayaran yang selalu sinkron, agar dapur, venue, dan keuangan bekerja dari data yang sama.

Alur (roadmap §47): Inquiry → Quotation → Package & Menu → BEO → DP → Food Tasting & Technical Meeting → Event → Final Billing.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-BQT-01 | **Banquet Package** per pax ++ dengan **minimum pax** atau harga paket nett: Wedding (Rp88 jt nett: buffet 300 pax + VIP family 20 pax, 2 food stall, ballroom 5 jam, 1 malam bungalow suite), Intimate Wedding (Rp28 jt nett), Pre-wedding (Rp3,8 jt nett), Birthday (Rp18 jt nett), Kids Birthday (Rp14,5 jt nett), Social Event (Rp190rb++/pax, minimal 30 pax) | Must |
| FR-BQT-02 | **Menu Selection** dengan **kuota per kategori** (mis. 2 appetizer, rice, soup, noodle/pasta, chicken, fish, vegetable, 2 dessert); pilihan di luar kuota ditolak atau dikenai tambahan | Must |
| FR-BQT-03 | **Tambahan buffet per pax** (mis. Rp190.000++/pax) dan **food stall** tambahan | Must |
| FR-BQT-04 | **Corkage fee** untuk makanan/minuman dari luar per policy | Must |
| FR-BQT-05 | **Outdoor venue add-on** dengan minimum pax (garden & lake view Rp15 jt, pool side Rp10 jt, minimal 100 pax) | Must |
| FR-BQT-06 | **Resource bundling**: ballroom, family room, bungalow, golf cart, voucher F&B (P2 EP-19), sesuai paket | Must |
| FR-BQT-07 | **Electricity quota** (watt) per paket dan biaya kelebihan *(usulan)* | Should |
| FR-BQT-08 | **Payment Schedule**: DP minimum (mis. 30%), pelunasan (mis. H-7), termin tambahan; **payment reminder** otomatis; tanggal jatuh tempo ditegakkan di BEO (Definite) | Must |
| FR-BQT-09 | **Food Tasting** dan **Technical Meeting** sebagai jadwal dengan peserta, catatan hasil, dan perubahan yang diteruskan ke BEO | Must |
| FR-BQT-10 | **Final pax** dikunci pada tanggal cut-off (mis. H-3) *(usulan)*; penambahan setelahnya ditagih; penurunan tidak di bawah guaranteed pax | Must |
| FR-BQT-11 | **BOM per pax** dari recipe P2 → daftar kebutuhan bahan per event (procurement requirement, K1) | Must |
| FR-BQT-12 | **Final Billing**: folio event menggabungkan paket, tambahan, corkage, F&B tambahan, kerusakan; DP dan termin diaplikasikan; sisa ditagih atau direfund | Must |
| FR-BQT-13 | **MICE**: paket meeting P2 diperluas dengan multi-hari, multi-ruang, breakout room, dan akomodasi peserta | Must |
| FR-BQT-14 | Pipeline banquet di CRM (EP-02) dan kalender venue (EP-15) selalu sinkron dengan status event | Must |

**Acceptance criteria:**

- Wedding Rp88.000.000 nett: DP 30% = Rp26.400.000 ditagih saat Definite; pelunasan Rp61.600.000 jatuh tempo H-7 dengan pengingat H-14 dan H-8 *(usulan jadwal)*.
- Tambahan buffet 20 pax × Rp190.000++ = Rp3.800.000 + 15,5% tax & service = Rp4.389.000 masuk final billing.
- Social Event 25 pax ditagih minimum 30 pax: 30 × Rp190.000 = Rp5.700.000 ++ (Rp6.583.500 setelah 15,5%).
- Outdoor add-on garden & lake view tidak dapat dipilih untuk event 80 pax (minimum 100 pax).

## EP-14 — Banquet Event Order (BEO)

Label sesuai NC §11: **Banquet Event Order** (bukan "Banquet Event Ordering").

| ID | Requirement | Prioritas |
|---|---|---|
| FR-BEO-01 | **BEO / function sheet** dibuat dari event: rundown, venue & setup/layout, pax, menu final, jadwal saji, F&B per station, equipment, dekorasi, vendor, catatan khusus (alergi, VIP), kebutuhan listrik | Must |
| FR-BEO-02 | **Issue** dan **Revise** dengan versi; perubahan antar versi ditandai; `ETag`/`If-Match` mencegah timpa (Tech Doc §8.1) | Must |
| FR-BEO-03 | **Distribusi** ke departemen (kitchen, F&B service, venue, engineering, golf, front desk) dengan konfirmasi baca per departemen | Must |
| FR-BEO-04 | **Kitchen production**: item BEO muncul di KDS pada jadwal produksi (P2 EP-21) | Must |
| FR-BEO-05 | Cetak/PDF BEO dengan format standar banquet | Must |
| FR-BEO-06 | BEO terkunci setelah event Completed; perubahan sesudahnya hanya lewat adjustment di final billing | Must |

**Acceptance criteria:**

- Revisi BEO menaikkan versi, menandai item yang berubah, dan dapur yang belum mengonfirmasi versi terbaru terlihat di status distribusi.

## EP-15 — Banquet Venue & Event Resource

Melanjutkan PRD P2 FR-RSV-01 (Banquet Venue dan Event disiapkan untuk P3).

| ID | Requirement | Prioritas |
|---|---|---|
| FR-VEN-01 | **Venues**: Ballroom, function room (Sapphire, Emerald, Jade, Ruby), outdoor (garden & lake view, pool side), VIP Suite; kapasitas per layout (Round Table, Classroom, U-Shape, Theater, Boardroom, Standing, Banquet) | Must |
| FR-VEN-02 | Resource type **Banquet Venue** dan **Event** di Reservation Engine (mode exclusive) dengan **buffer setup/teardown** | Must |
| FR-VEN-03 | **Tentative hold** dengan option date: kedaluwarsa otomatis bila tidak menjadi Definite; **Definite** setelah DP; hold kedua (waitlist) pada tanggal yang sama *(usulan)* | Must |
| FR-VEN-04 | Venue yang dibagi/digabung (mis. ballroom terbagi dua) sebagai resource induk-anak yang saling mengunci | Should |
| FR-VEN-05 | **Venue Calendar** terintegrasi Booking Calendar P2 | Must |

**Acceptance criteria:**

- Dua sales memegang Tentative pada ballroom tanggal yang sama hanya bila hold kedua ditandai waitlist; saat hold pertama kedaluwarsa, waitlist naik dan sales diberi notifikasi.

## EP-16 — Tournament Management

**User story:** Sebagai Golf Manager, saya ingin menjalankan tournament club dari registrasi sampai pemenang dengan leaderboard live, agar Rhapsody tidak lagi dibutuhkan untuk tournament.

Tournament adalah tipe Event (roadmap §48) dengan modul scoring.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-TRN-01 | **Tournament Creation**: nama, tanggal (satu atau beberapa hari), course & playing route, format (**Stroke Play**, **Stableford**; gross/net), divisi/flight handicap, batas peserta, eligibility (member/tamu/undangan) | Must |
| FR-TRN-02 | **Tournament Schedule**: blok tee sheet (P1) dikunci untuk tournament; tee time umum hari itu tertutup/terbatas | Must |
| FR-TRN-03 | **Registration**: Back Office, Member App, website; data handicap (P2 Handicap Index atau input resmi), ukuran kaos/preferensi; **Tournament Fee** dan **Tournament Package** (green fee, caddy, golf cart, makan malam, goodie bag) lewat billing | Must |
| FR-TRN-04 | **Participants**: status Registered, Waitlisted, Withdrawn; waitlist otomatis naik; refund sesuai Tournament Policies | Must |
| FR-TRN-05 | **Flighting**: pembagian flight otomatis berdasarkan handicap/divisi atau manual dengan drag; pairing tamu sponsor | Must |
| FR-TRN-06 | **Tee Assignment**: sequential tee times atau **Shotgun Start** (setiap flight start di hole berbeda; A/B group untuk >18 flight) dengan start sheet & cetak | Must |
| FR-TRN-07 | **Scoring** memakai scorecard P2 (caddy tablet, member app, staff scoring desk) dengan validasi per hole dan attestation; skor Finalized mengikuti aturan correction P2 | Must |
| FR-TRN-08 | **Leaderboard** live gross & net per divisi, tiebreak (countback 9/6/3/1 hole terakhir) *(usulan)*, tampil di member app, website, dan Leaderboard Screen < 1 menit setelah skor masuk | Must |
| FR-TRN-09 | **Special awards**: Nearest to Pin, Longest Drive, Hole-in-One (P2 EP-10) | Must |
| FR-TRN-10 | **Sponsor** (nama, paket sponsor, logo pada leaderboard/start sheet, tagihan sponsor) dan **Prize** (per posisi/award, penerima, serah terima) | Must |
| FR-TRN-11 | **Finalize**: hasil dikunci; **Hall of Fame** (Tournament Champion, Club Champion) dibuat otomatis (melanjutkan P2 EP-11) | Must |
| FR-TRN-12 | **Tournament Report**: peserta, hasil, pendapatan (fee, sponsor, paket), biaya prize | Must |
| FR-TRN-13 | Integrasi Event: venue, catering, dan sponsor billing melalui Event `banquet` (EP-12) | Must |

**Acceptance criteria:**

- Shotgun start 72 pemain (18 flight × 4) menempatkan satu flight per hole; start sheet menampilkan hole awal setiap pemain.
- Skor dari caddy tablet offline tersinkron dan leaderboard net diperbarui ≤ 1 menit setelah koneksi kembali.
- Finalize tournament membuat entri Hall of Fame Tournament Champion yang hanya tampil publik sesuai consent P2.

## EP-17 — Unified Folio & Billing Expansion

**User story:** Sebagai Finance Manager, saya ingin semua transaksi seorang customer atau perusahaan tertagih dari satu folio dan invoice, dengan termin dan piutang yang terpantau.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-BIL-P3-01 | **Customer Folio** lintas lini (roadmap §49): golf, sport club, kelas, bungalow, VIP suite, meeting, banquet, caddy, golf cart, restoran, pro shop, driving range, membership, event, paket, voucher; folio reservasi P1–P2 tetap ada dan dapat **digabung** ke folio customer | Must |
| FR-BIL-P3-02 | **Cross-line Split Bill**: memecah folio per item, per orang, per persentase, atau per pihak pembayar (customer vs perusahaan) lintas lini | Must |
| FR-BIL-P3-03 | **Payment Schedule** generik: DP, termin, pelunasan dengan tanggal jatuh tempo, reminder, status Partially Paid / Paid / Overdue | Must |
| FR-BIL-P3-04 | **Invoice**: Generate Invoice dari folio/payment schedule; nomor berurutan per property; Issued, Partially Paid, Paid, Overdue, Void; credit note | Must |
| FR-BIL-P3-05 | **Corporate Billing**: tagihan nominee/event perusahaan ke Corporate Account (P1) dengan **credit limit**, termin (mis. 30 hari), statement perusahaan, **aging** operasional (0–30, 31–60, 61–90, > 90 hari) | Must |
| FR-BIL-P3-06 | **Payment allocation** ke invoice (penuh/parsial, satu pembayaran ke banyak invoice); **deposit/DP** diaplikasikan saat final billing | Must |
| FR-BIL-P3-07 | **Installment / Payment Term** untuk membership fee (DP ≥ 30%, maks 12×) dan paket/wedding ≥ Rp50 jt *(asumsi §16 #10)* | Must |
| FR-BIL-P3-08 | **Write-off** piutang dan **refund** lewat approval | Must |
| FR-BIL-P3-09 | **Financial Evidence** (roadmap §49): pricing snapshot, payment, refund, adjustment, membership charge, folio, final settlement tetap tertelusur dari invoice | Must |
| FR-BIL-P3-10 | Event `billing.invoice_issued`, `billing.invoice_paid`, `billing.invoice_voided` untuk AR P4 (K2) | Must |

**Acceptance criteria:**

- Corporate event dengan meeting room, bungalow, golf, dan catering menghasilkan satu invoice ke perusahaan; DP dari payment schedule berkurang dari invoice final.
- Invoice yang lewat jatuh tempo berpindah ke kolom aging yang benar dan memicu reminder; corporate account yang melewati credit limit tidak bisa menambah charge tanpa approval.

## EP-18 — Cashier Shift, End-of-Day & Night Audit

| ID | Requirement | Prioritas |
|---|---|---|
| FR-EOD-01 | **Cashier Shift** tingkat billing untuk front desk, sport reception, banquet, dan kasir non-POS: modal awal, penerimaan per metode, cash in/out, hitung kas, selisih; laporan per kasir (melanjutkan P1 FR-PAY-12 dan P2 shift POS) | Must |
| FR-EOD-02 | **End-of-Day** per outlet/kasir: semua shift ditutup, transaksi offline tersinkron | Must |
| FR-EOD-03 | **Night Audit** = tutup **Business Day** club: cek shift terbuka, folio terbuka/belum settle, pembayaran pending, no-show, posting biaya otomatis (mis. bungalow per malam); hasil dibekukan | Must |
| FR-EOD-04 | **Daily Revenue Report** per lini & komponen, pembayaran per metode, deposit & liability (voucher, poin, DP) per business day | Must |
| FR-EOD-05 | Transaksi setelah night audit masuk business day berikutnya; **reopen** business day lewat approval dengan audit | Must |
| FR-EOD-06 | Event `billing.business_day_closed` (K4) untuk accounting export/P4 | Must |

**Acceptance criteria:**

- Night audit tidak dapat ditutup selama ada shift terbuka; setelah ditutup, total Daily Revenue Report = Σ charge business day tersebut dan total pembayaran = Σ shift.

## EP-19 — Member & Guest App P3

| ID | Requirement | Prioritas |
|---|---|---|
| FR-APP-P3-01 | **Loyalty**: saldo poin, tier, rewards, Points History, tukar reward | Must |
| FR-APP-P3-02 | **Offers**: promosi dan paket yang berlaku untuk segmen member; promo code personal dari campaign | Must |
| FR-APP-P3-03 | **Events**: daftar event, registrasi, tiket QR, My Events | Must |
| FR-APP-P3-04 | **Tournaments**: registrasi & bayar fee, start sheet, leaderboard live, hasil | Must |
| FR-APP-P3-05 | **Packages**: beli paket lintas lini (EP-11) | Must |
| FR-APP-P3-06 | **Support**: kirim feedback dan complaint, lihat status ticket | Must |
| FR-APP-P3-07 | **Invoices** dan **payment schedule** (mis. termin banquet keluarga) dengan pembayaran online | Must |
| FR-APP-P3-08 | **Communication Preferences** (opt-in per kanal) | Must |

## EP-20 — Website P3

| ID | Requirement | Prioritas |
|---|---|---|
| FR-WEB-P3-01 | Halaman **Wedding & Banquet, Events, Promotions, Packages** dari data terstruktur (bukan CMS) | Must |
| FR-WEB-P3-02 | **Inquiry form** wedding, banquet, MICE, corporate golf, tournament, membership → Lead (EP-01) dengan consent; anti-bot seperti P1 | Must |
| FR-WEB-P3-03 | **Book Event** / registrasi event terbuka dan tournament publik dengan pembayaran | Must |
| FR-WEB-P3-04 | **Book Package** lintas lini (EP-11) dengan alur non-member P2 | Must |
| FR-WEB-P3-05 | **Quotation acceptance** dan **pembayaran DP/invoice** lewat link aman (EP-03, EP-17) | Must |
| FR-WEB-P3-06 | **Leaderboard** tournament publik (opt-in sesuai consent) | Should |
| FR-WEB-P3-07 | Kode promo di checkout publik dengan rate limit | Must |

## EP-21 — Operational Interfaces P3 (`ops`)

| ID | Requirement | Prioritas |
|---|---|---|
| FR-OPS-P3-01 | **Event Operations**: event hari ini, BEO terbaru, checklist, registrasi & check-in tamu (QR), catatan insiden | Must |
| FR-OPS-P3-02 | **Tournament Desk**: check-in peserta, start sheet, shotgun start, scoring desk (input dari kartu kertas), leaderboard | Must |
| FR-OPS-P3-03 | **POS & Front Desk**: Apply Promotion, Redeem Points, Cashier shift lintas lini | Must |
| FR-OPS-P3-04 | **Leaderboard Screen** kiosk (device terdaftar, read-only) | Should |
| FR-OPS-P3-05 | Check-in tamu event dan scoring desk tetap berfungsi offline dengan sync idempotent | Must |

## EP-22 — Club Policies P3

Memakai framework policy P0 (berversi + effective date). Booking, quotation, dan transaksi menyimpan versi policy yang berlaku.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-POL-P3-01 | **Banquet Policies** (roadmap §65): DP minimum, jatuh tempo pelunasan, corkage fee, minimum pax per venue/paket, final pax cut-off, pembatalan & forfeiture DP | Must |
| FR-POL-P3-02 | **Pricing Policies** dan **Promotion Policies** *(usulan label)*: stacking, batas diskon manual per role, approval promosi | Must |
| FR-POL-P3-03 | **Sales Policies** *(usulan label)*: batas diskon quotation per role, masa berlaku quotation, option date, SLA lead | Must |
| FR-POL-P3-04 | **Loyalty Policies** *(usulan label)*: earning, nilai tukar, expiry, tier | Must |
| FR-POL-P3-05 | **Event Policies, Tournament Policies** *(usulan label)*: pembatalan registrasi, waitlist, refund fee, handicap maksimum | Must |
| FR-POL-P3-06 | **Credit Policies** *(usulan label)*: credit limit default corporate, termin, reminder, eskalasi overdue | Must |

## EP-23 — KPI, Dashboard & Reports

KPI P3 mengikuti roadmap §60 (Banquet, Commercial, CRM) dan §69 dengan label NC §23.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-RPT-P3-01 | **Banquet Performance**: Inquiry, Quotation, Deal (konversi per tahap), Event Count, Pax, Banquet Revenue, Outstanding DP, Outstanding Settlement | Must |
| FR-RPT-P3-02 | **CRM Performance** *(usulan label)*: Leads per sumber, Conversion, First Response Time, Pipeline Value, Active Customers, Member Activity, Campaign Performance, Loyalty, Top Spender, NPS, Complaint SLA | Must |
| FR-RPT-P3-03 | **Commercial Performance** diperluas: Total Sales, Package Sales, Promotion Usage & Discount Cost, Voucher Sold vs Redeemed, Average Transaction | Must |
| FR-RPT-P3-04 | **Golf Performance** + Tournament: jumlah tournament, peserta, pendapatan tournament & sponsor | Must |
| FR-RPT-P3-05 | **Reports** *(usulan daftar)*: Lead Source Report, Sales Pipeline Report, Quotation Report, Sales Commission Report, Campaign Performance Report, Complaint Report, NPS Report, Top Spender Report, Loyalty Points Report, Loyalty Liability Report, Promotion Performance Report, Package Sales Report, Event Report, Banquet Revenue Report, BEO Report, Tournament Report, Invoice Report, Accounts Receivable Aging Report, Payment Schedule Report, Daily Revenue Report, Night Audit Report, Cashier Shift Report | Must |
| FR-RPT-P3-06 | Filter tanggal/property/lini/sales, export CSV/XLSX, permission per report (P0 FR-REP-03) | Must |

**Acceptance criteria:**

- Banquet Revenue di dashboard sama dengan Banquet Revenue Report untuk periode yang sama; Accounts Receivable Aging Report sama dengan saldo invoice terbuka.

## EP-24 — Integrasi P3

| ID | Requirement | Prioritas |
|---|---|---|
| FR-INT-P3-01 | **WhatsApp inbound** lewat BSP (P1): pesan baru dari nomor tak dikenal menjadi Lead; percakapan tercatat di Interaction | Must |
| FR-INT-P3-02 | **Email inbound** dari alamat departemen (banquet@, marketing@) menjadi Lead *(usulan)* | Should |
| FR-INT-P3-03 | ~~Meta (Instagram/Facebook) & TikTok lead form adapter~~: **ditunda**. Lead media sosial diinput manual dengan sumber wajib (keputusan §16 #6) | — |
| FR-INT-P3-04 | **Payment link** untuk DP, termin, dan invoice lewat payment gateway P1 | Must |
| FR-INT-P3-05 | **Accounting Export** diperluas: invoice & piutang, DP/deposit banquet, revenue allocation paket, diskon promosi, liability & redemption poin, komisi sales, business day close (sampai Accounting P4 live) | Must |
| FR-INT-P3-06 | **Template notifikasi P3** (ID/EN): quotation, DP & pelunasan reminder, BEO update, registrasi event/tournament, leaderboard final, poin & reward, ticket update, invoice & overdue | Must |
| FR-INT-P3-07 | **E-signature** pihak ketiga untuk quotation/kontrak *(usulan)* | Should |

## EP-25 — Migrasi Gelombang 3

Pipeline sama dengan P1–P2 (`oneclub import rhapsody`, Tech Doc §8.2). Sumber di luar Rhapsody (Excel sales, buku banquet) memakai Master Data Import P0.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-MIG-P3-01 | **Future banquet & events** (tanggal, paket, pax, menu bila ada) beserta **DP yang sudah diterima** dan sisa tagihan | Must |
| FR-MIG-P3-02 | **Corporate AR terbuka** (invoice outstanding per perusahaan) dengan tanggal jatuh tempo | Must |
| FR-MIG-P3-03 | **Tournament history & hasil** dari Rhapsody (arsip read-only) dan Hall of Fame champion | Should |
| FR-MIG-P3-04 | **Leads & opportunity aktif** dari spreadsheet sales | Must |
| FR-MIG-P3-05 | **Reconciliation**: total DP banquet = liability deposit, total AR corporate, jumlah future events; ditandatangani club | Must |
| FR-MIG-P3-06 | **Dry run** minimal 2 kali di Staging dan runbook cutover tournament & banquet (freeze, delta, rekonsiliasi, go/no-go, rollback) | Must |

## EP-26 — Production Readiness (Release 3)

| ID | Requirement | Referensi |
|---|---|---|
| FR-REL-P3-01 | **Load test** k6: registrasi tournament saat dibuka, campaign 10.000 penerima, promosi pada puncak POS, night audit pada volume 1 bulan | Tech Doc §13 |
| FR-REL-P3-02 | **E2E** Playwright untuk flow §9 (wedding, corporate event, tournament, package, promosi offline, night audit) | Tech Doc §13 |
| FR-REL-P3-03 | **Regression suite P1–P2** lulus pada build Release 3 | G9 |
| FR-REL-P3-04 | **Pen test** untuk endpoint publik baru: inquiry, quotation acceptance link, payment link, promo code, registrasi publik | Tech Doc §9 |
| FR-REL-P3-05 | Build VPS dengan `garble` dan tanpa source map | Tech Doc §9.4 |
| FR-REL-P3-06 | Training per peran (sales, banquet, event, marketing, finance AR, tournament desk) dan **hypercare** 2–4 minggu per gelombang *(usulan)* | — |

---

# 9. End-to-End Flows (Demo Exit P3)

Skenario UAT *(usulan)*.

## 9.1 Wedding (roadmap §68.5)

```text
Inquiry WhatsApp ke nomor bisnis → Lead (sumber WhatsApp) → assign sales wedding (EP-01, EP-24)
  → Opportunity Wedding → site visit → Quotation Wedding Rp88 jt nett + 20 pax tambahan
  → diskon 5% (dalam batas Sales Policies) → link quotation → Accepted (EP-02, EP-03)
  → Event Definite: ballroom terkunci, bungalow suite 1 malam, payment schedule DP 30% & H-7 (EP-13, EP-15, EP-17)
  → bayar DP via payment link → food tasting & technical meeting → menu final (EP-13)
  → BEO v1 → revisi v2 (alergi) → kitchen & venue konfirmasi (EP-14)
  → pelunasan H-7 (reminder otomatis) → hari H: guest registration & check-in, produksi di KDS
  → final billing: corkage + F&B tambahan → settle → opportunity Won → komisi sales (EP-04)
  → procurement requirement terkirim ke P4 (K1); business day close (EP-18)
```

## 9.2 Corporate Event (roadmap §68.4)

```text
Lead Corporate Golf dari website → Opportunity → Quotation Corporate Package
  (Jade Room full day 60 pax + 10 bungalow + golf 40 pemain + catering) (EP-11, EP-03)
  → Accepted → alokasi all-or-nothing (meeting room, bungalow, tee sheet block) (EP-11)
  → invoice ke Corporate Account dengan termin 30 hari (EP-17)
  → hari H → nominee check-in golf & bungalow; F&B tambahan ke folio perusahaan
  → final invoice − DP → aging → pembayaran → AR P4 / accounting export
```

## 9.3 Club Tournament

```text
Create tournament (stableford net, 72 pemain, shotgun) → registrasi Member App & website + fee (EP-16)
  → waitlist saat penuh → flighting otomatis per handicap → shotgun start sheet
  → hari H: tournament desk check-in → shotgun → skor caddy tablet (hole 7–12 offline)
  → leaderboard live di Leaderboard Screen & website → Nearest to Pin, Longest Drive
  → finalize → winners, prize → Hall of Fame Tournament Champion (consent) → Tournament Report
```

## 9.4 Promotion & Loyalty

```text
Campaign WhatsApp ke segmen "member aktif, belanja F&B > Rp2 jt/bulan" dengan promo code unik (EP-05, EP-06)
  → member pakai kode di restoran saat happy hour; POS offline menerapkan promo (EP-10)
  → payment settle → poin earned → tier naik → redeem poin di kunjungan berikutnya (EP-09)
  → Top Spender bulan ini memperbarui ranking (EP-08) → Campaign Performance mencatat konversi
```

## 9.5 Night Audit

```text
Akhir hari: kasir front desk, sport reception, banquet, dan POS menutup shift (EP-18)
  → night audit: cek folio terbuka, posting bungalow per malam, no-show
  → Daily Revenue Report, liability (voucher, poin, DP) → business day closed (K4)
  → transaksi 00.30 masuk business day berikutnya
```

---

# 10. Data Entities P3

Sesuai Data Entity Rollout roadmap §70 (P3), ditambah entity pendukung. Schema mengikuti Tech Doc §4.1.

| Entity | Schema | Catatan |
|---|---|---|
| Lead, Lead Source, Lead Assignment, Follow-up | `crm` | Lead (§70) |
| Pipeline, Pipeline Stage, Opportunity, Opportunity Activity | `crm` | Opportunity / Pipeline (§70) |
| Sales Quotation, Quotation Version, Quotation Line, Quotation Acceptance | `crm` | Sales Quotation (§70) |
| Sales Target, Commission Scheme, Commission Statement, Commission Line | `crm` | |
| Segment (perluasan), Campaign, Campaign Audience, Campaign Delivery, Communication Consent | `crm` | Campaign (§70) |
| Customer Interaction (perluasan), Feedback (perluasan), Complaint Ticket, Ticket SLA, Ticket Escalation | `crm` | Customer Interaction, Feedback, Complaint Ticket (§70) |
| Loyalty Account, Loyalty Tier, Earning Rule, Points Ledger, Reward, Reward Redemption | `crm` | Loyalty (§70) |
| Top Spender Snapshot (read model) | `reporting` | Sumber folio/billing |
| Promotion, Promotion Rule, Promo Code, Promotion Redemption | `commercial` | Promotion (§70) |
| Package, Package Type, Package Component, Package Version, Package Booking, Package Component Consumption, Revenue Allocation | `commercial` | Package (§70) |
| Event, Event Type, Event Schedule, Event Participant, Guest Registration, Event Checklist, Vendor | `banquet` | Event, Event Participant (§70) |
| Event Package, Banquet Booking, Banquet Package, Menu Category Quota, Menu Selection, Corkage, Food Tasting, Technical Meeting | `banquet` | Event Package, Banquet Booking, Banquet Package, Menu Selection (§70) |
| BEO, BEO Version, BEO Distribution, Procurement Requirement | `banquet` | BEO (§70); requirement → P4 (K1) |
| Venue, Venue Layout Capacity, Venue Hold (Tentative/Definite) | `banquet`, `reservation` | Resource type Banquet Venue & Event |
| Tournament, Tournament Division, Tournament Registration, Tournament Flight, Start Assignment, Tournament Leaderboard, Sponsor, Prize, Award | `golf` | Tournament (§70) |
| Customer Folio, Payment Schedule, Payment Schedule Line, Invoice, Invoice Line, Credit Note, Payment Allocation, Corporate Credit Limit | `billing` | Payment Schedule (§70) |
| Cashier Shift (billing), Cash Movement, Business Day, Night Audit Run | `billing` | |
| Read model KPI P3 | `reporting` | |

---

# 11. API Surface P3

Konvensi Tech Doc §8.1 (`/api/v1/<module>/<resource>`, cursor pagination, RFC 9457, `Idempotency-Key`, `ETag`/`If-Match`).

| Area | Endpoint utama |
|---|---|
| Leads | `/api/v1/crm/leads` (+ `:assign`, `:qualify`, `:disqualify`, `:convert`), `/crm/follow-ups` |
| Pipeline | `/api/v1/crm/pipelines`, `/crm/opportunities` (+ `:move-stage`, `:win`, `:lose`), `/crm/opportunities/{id}/activities` |
| Quotation | `/api/v1/crm/quotations` (+ `:revise`, `:submit-approval`, `:send`, `:convert`), `GET /crm/quotations/{id}/pdf` |
| Commission | `/api/v1/crm/sales-targets`, `/crm/commission-schemes`, `/crm/commission-statements` (+ `:approve`, `:mark-paid`) |
| Engagement | `/api/v1/crm/segments` (+ `:refresh`, `:export`), `/crm/campaigns` (+ `:schedule`, `:approve`, `:cancel`), `/crm/consents`, `/crm/tickets` (+ `:assign`, `:escalate`, `:resolve`, `:close`, `:reopen`), `GET /crm/nps` |
| Loyalty & Top Spender | `/api/v1/crm/loyalty/accounts`, `/crm/loyalty/earning-rules`, `/crm/loyalty/tiers`, `/crm/loyalty/rewards`, `/crm/loyalty/accounts/{id}/ledger` (+ `:adjust`, `:redeem`), `GET /crm/top-spenders` |
| Promotion | `/api/v1/commercial/promotions` (+ `:approve`, `:activate`, `:deactivate`, `:simulate`), `/commercial/promo-codes` (+ `:generate`), `POST /commercial/promo-codes:check` |
| Package | `/api/v1/commercial/packages` (+ `:publish`), `/commercial/packages/{id}/availability`, `/commercial/package-bookings` (+ `:cancel`), `/commercial/package-bookings/{id}/consumption` |
| Event & Banquet | `/api/v1/banquet/events` (+ `:make-definite`, `:complete`, `:cancel`), `/banquet/events/{id}/schedule`, `/banquet/events/{id}/participants`, `/banquet/events/{id}/checklist`, `/banquet/vendors`, `/banquet/packages`, `/banquet/menus`, `/banquet/events/{id}/menu-selection`, `/banquet/venues`, `GET /banquet/venue-calendar` |
| BEO | `/api/v1/banquet/beos` (+ `:issue`, `:revise`, `:acknowledge`), `GET /banquet/beos/{id}/pdf`, `GET /banquet/events/{id}/procurement-requirement` |
| Tournament | `/api/v1/golf/tournaments` (+ `:open-registration`, `:close-registration`, `:publish-draw`, `:start`, `:finalize`), `/golf/tournaments/{id}/registrations` (+ `:withdraw`), `/golf/tournaments/{id}/flights`, `GET /golf/tournaments/{id}/start-sheet`, `GET /golf/tournaments/{id}/leaderboard` (+ `/stream` SSE), `/golf/tournaments/{id}/sponsors`, `/golf/tournaments/{id}/prizes` |
| Billing | `/api/v1/billing/customer-folios` (+ `:merge`, `:split`), `/billing/payment-schedules`, `/billing/invoices` (+ `:issue`, `:send`, `:void`, `:write-off`), `/billing/credit-notes`, `/billing/payment-allocations`, `GET /billing/corporate-accounts/{id}/statement`, `GET /billing/aging` |
| Cashier & Night Audit | `/api/v1/billing/cashier-shifts` (+ `:open`, `:close`), `/billing/business-days` (+ `:night-audit`, `:reopen`), `GET /billing/daily-revenue` |
| Public (web) | `POST /api/v1/public/inquiries`, `GET /public/events`, `POST /public/events/{id}/registrations`, `GET /public/promotions`, `GET /public/packages`, `POST /public/package-bookings`, `GET /public/quotations/{token}` (+ `:accept`), `GET /public/invoices/{token}`, `GET /public/tournaments/{id}/leaderboard` |
| Reporting | `GET /api/v1/reporting/dashboards/{banquet|crm|commercial|golf}`, report registry P0 |
| Migration | CLI `oneclub import rhapsody --scope=banquet|corporate-ar|tournament-history`, `oneclub import sales --file=leads.xlsx` |

Event outbox P3: `crm.lead_created`, `crm.lead_assigned`, `crm.lead_converted`, `crm.opportunity_won`, `crm.opportunity_lost`, `crm.quotation_sent`, `crm.quotation_accepted`, `crm.campaign_sent`, `crm.ticket_escalated`, `crm.ticket_resolved`, `crm.points_earned`, `crm.points_redeemed`, `crm.points_expired`, `crm.tier_changed`, `crm.commission_approved`, `commercial.promotion_applied`, `commercial.package_booked`, `commercial.package_consumed`, `banquet.event_definite`, `banquet.event_completed`, `banquet.event_cancelled`, `banquet.beo_issued`, `banquet.beo_revised`, `golf.tournament_registration_confirmed`, `golf.tournament_started`, `golf.tournament_finalized`, `billing.invoice_issued`, `billing.invoice_paid`, `billing.invoice_overdue`, `billing.invoice_voided`, `billing.payment_schedule_due`, `billing.business_day_closed`.

---

# 12. Non-Functional Requirements

Mengacu Tech Doc §14 dan PRD P2 §12. Semua target P1–P2 tetap berlaku.

| Area | Requirement P3 |
|---|---|
| Availability | 99.5% per bulan pada jam operasional club; hari event/tournament dipantau khusus |
| Performance | p95 < 300 ms read; p95 < 800 ms untuk quotation, invoice, payment, redemption poin, apply promo; resolve promo di POS < 100 ms lokal |
| Real-time | Leaderboard tournament ≤ 1 menit dari skor masuk (target < 2 detik saat online); BEO update dan venue calendar < 2 detik |
| Konkurensi | 0 double booking venue/paket; 0 double redemption poin & promo code; registrasi tournament tidak melebihi kuota |
| Campaign | 10.000 pesan per campaign tanpa melampaui batas BSP; retry dan status pengiriman tercatat |
| Offline | Promosi POS dan check-in tamu event/scoring desk berjalan offline; sync idempotent |
| Financial integrity | Snapshot immutable termasuk promosi & alokasi paket; ledger poin dan deposit/DP seimbang dengan liability harian; business day ter-close sebelum ekspor jurnal |
| Document integrity | Quotation, BEO, invoice berversi; nomor invoice berurutan tanpa celah per property; `ETag`/`If-Match` untuk BEO dan folio |
| Privacy | UU PDP: consent pemasaran per kanal, opt-out, data lead dihapus/anonimkan setelah periode retensi bila tidak menjadi customer *(usulan)*; Top Spender hanya internal |
| Security | OWASP ASVS Level 2; link quotation/invoice bertoken acak, kedaluwarsa, sekali pakai untuk acceptance; rate limit promo code dan inquiry |
| Regresi | Seluruh suite P1–P2 (unit, integration, E2E, load) lulus di setiap merge P3 |
| Bahasa | English (label) + Bahasa Indonesia (helper text, pesan, notifikasi, dokumen quotation/BEO/invoice ID/EN) |

---

# 13. Exit Criteria & Definition of Done

## 13.1 Exit Criteria Release 3 *(usulan)*

Roadmap tidak mencantumkan exit criteria eksplisit untuk P3. Kriteria berikut diturunkan dari target Release 3 (roadmap §75: *customer, sales, commercial, event, banquet, tournament*), Fit-Gap §71, dan Rhapsody Parity §72.

| # | Exit criteria | Bukti |
|---|---|---|
| 1 | Semua inquiry masuk pipeline | Lead dari WhatsApp, website, dan input manual tercatat selama UAT; laporan sumber & first response tersedia (EP-01–02) |
| 2 | Wedding & corporate event end-to-end | Skenario §9.1–9.2 lulus termasuk DP, H-7 reminder, BEO revisi, final billing (EP-03, EP-11–15, EP-17) |
| 3 | Tournament Rhapsody dimatikan | Skenario §9.3 lulus dengan ≥ 72 pemain; checklist parity tournament & scoring ditandatangani club (EP-16, EP-25) |
| 4 | Promosi & loyalty akurat | Skenario §9.4 lulus; liability poin = saldo ledger; promo offline = online (EP-09–10) |
| 5 | Paket lintas lini | Golf Day, Stay & Golf, Corporate, Wedding package terjual dengan alokasi all-or-nothing; revenue allocation = harga paket (EP-11) |
| 6 | Unified folio, corporate billing, night audit | Invoice corporate, aging, dan night audit berjalan 2 minggu di Staging dengan rekonsiliasi harian (EP-17–18) |
| 7 | KPI P3 konsisten | Angka dashboard = report sumber untuk tanggal yang sama (EP-23) |
| 8 | Tanpa regresi Release 1–2 | Suite P1–P2 lulus pada build Release 3 (G9) |
| 9 | Kontrak P4 terpenuhi | Contract test K1–K6 lulus; accounting export memuat data P3 sampai Accounting P4 live |
| 10 | UAT disetujui | Skenario §9 lulus di Staging dan di-sign-off club per gelombang rilis |

## 13.2 Definition of Done per Story

Sama dengan PRD P2 §13.2, ditambah:

- Dokumen keuangan (quotation, invoice, BEO) memiliki test versi, penomoran, dan konkurensi (`If-Match`).
- Alur yang menulis ledger (poin, deposit/DP, invoice) memiliki integration test rekonsiliasi saldo.
- Konversi lintas module lewat domain event memiliki test idempotency (event diterima dua kali).
- Kontrak dengan P4 (§5.4.2) memiliki contract test.
- Fitur yang belum siap rilis tersembunyi di balik module/feature flag instance.

---

# 14. Milestones & Gelombang Rilis

## 14.1 Milestones *(usulan)*

Urutan mengikuti §5.4.3: bagian yang tidak bergantung pada P4 lebih dulu. Tanggal mengikuti kapasitas tim dan progres P4.

| Milestone | Isi | Bergantung pada | Hasil yang bisa didemo |
|---|---|---|---|
| **M1 — Kontrak & Billing Core** | Kontrak K1–K6 di-merge; EP-17 (customer folio, payment schedule, invoice), EP-15 (venue resource), module `banquet` skeleton | Release 2 | Payment schedule DP/H-7 dengan reminder; invoice corporate; hold venue Tentative/Definite |
| **M2 — CRM & Sales** | EP-01–04, EP-24 (WhatsApp inbound, payment link) | M1 | Lead → opportunity → quotation → accepted → DP terbit |
| **M3 — Event & Banquet** | EP-12–14, Banquet Policies | M1–M2 | Skenario wedding §9.1 sampai BEO & final billing |
| **M4 — Commercial** | EP-10 (promotion, termasuk POS offline), EP-11 (package) | M1 | Happy hour di POS offline; Stay & Golf all-or-nothing |
| **M5 — Tournament** | EP-16, Tournament Desk, Leaderboard Screen | Release 2 (scorecard, caddy tablet) | Skenario tournament §9.3 |
| **M6 — Engagement, Loyalty & KPI** | EP-05–09, EP-23 | M2, M4 | Campaign → promo → poin → Top Spender; dashboard P3 |
| **M7 — Night Audit, Channels, Migrasi & Release 3** | EP-18–22, EP-25–26 | M1–M6 | Night audit, Member App & Website P3, dry run ×2, demo exit §9 |

M2, M4, dan M5 dapat berjalan bersamaan setelah M1.

## 14.2 Gelombang Rilis Release 3 *(usulan)*

Production P3 hanya setelah Release 2 live. Rilis bertahap dengan module flag:

| Gelombang | Isi | Alasan urutan |
|---|---|---|
| R3.1 | CRM & Sales, Event & Banquet, BEO, venue, payment schedule & invoice | Lini pendapatan terbesar tanpa sistem; menggantikan WhatsApp/Excel sales |
| R3.2 | Promotion, Package, Loyalty, Top Spender, Campaign, Complaint Ticket | Butuh data transaksi R2 + R3.1 dan campaign consent |
| R3.3 | Tournament + cutover tournament Rhapsody | Butuh scorecard & caddy tablet R2 stabil; jadwal mengikuti kalender tournament club |
| R3.4 | Unified folio penuh, corporate billing & aging, cashier shift, night audit | Koordinasi dengan Accounting P4 (K2, K4) |

---

# 15. Risiko & Mitigasi *(usulan)*

| # | Risiko | Dampak | Mitigasi |
|---|---|---|---|
| 1 | Scope P3 besar dan paralel dengan P4 (26 epic, module baru `banquet`) | Release 3 terlambat | Gelombang §14.2; prioritaskan Must; potongan scope diputuskan di OQ #15 sebelum M2 |
| 2 | Perubahan billing (unified folio, invoice) merusak alur P1–P2 | Regresi tagihan & statement | Folio P1–P2 tetap ada; unified folio sebagai lapisan penggabung; suite P1–P2 wajib lulus; review 2 orang |
| 3 | Adopsi sales rendah; sales tetap memakai WhatsApp pribadi | Pipeline kosong, konversi tak terukur | WhatsApp bisnis terpusat lewat BSP, input manual cepat, komisi hanya untuk deal yang tercatat |
| 4 | Promosi bertumpuk/salah konfigurasi di POS offline | Kerugian margin, selisih kas | Approval promosi, simulasi, prioritas deterministik, validasi server saat sync |
| 5 | Liability poin dan DP tidak tercatat benar sebelum Accounting P4 | Laporan keuangan salah | Ledger append-only, rekonsiliasi harian, accounting export diperluas |
| 6 | Kalender tournament club bertabrakan dengan jadwal rilis | Cutover tournament tertunda | Cutover pada tournament kecil lebih dulu; Rhapsody tournament tetap read-only sampai sign-off |
| 7 | Batas & biaya WhatsApp BSP untuk campaign besar | Campaign gagal atau mahal | Throttling, frequency cap, email sebagai kanal alternatif, persetujuan campaign besar |
| 8 | UU PDP: data lead dan campaign tanpa consent | Keluhan & sanksi | Consent wajib per kanal, opt-out mudah, retensi lead, audit pengiriman |
| 9 | Ketergantungan P4 (procurement, CMS, AR) belum siap | Fitur P3 tidak lengkap di production | Kontrak K1–K6 dengan mock; accounting export sebagai fallback |
| 10 | Venue Tentative menumpuk tanpa option date | Tanggal populer terkunci tanpa DP | Option date wajib, kedaluwarsa otomatis, waitlist |

---

# 16. Open Questions

Status per 4 Oktober 2026 (sumber: `OneClub — Open Questions P3–P5.md`):

- ✅ **Diputuskan**: dijawab langsung oleh club / product, dan sudah diterapkan ke requirement terkait.
- 🅰 **Asumsi**: belum dijawab. Nilai di kolom kanan dipakai sebagai dasar pengembangan sampai dikonfirmasi pemiliknya. Semua angka, tarif, dan aturan configurable di Settings.

| # | Pertanyaan | Dampak | Pemilik | Status | Keputusan / Asumsi |
|---|---|---|---|---|---|
| 1 | Tech Doc §12.3 (gelombang & kepemilikan) perlu ditulis ulang; setuju P3 dan P4 sebagai gelombang 2 dengan aturan §5.4? | Seluruh P3 | Engineering | ✅ | **Setuju.** P3 ‖ P4 = gelombang 2; §12.3 ditulis di Tech Doc |
| 2 | Pipeline stage per lini dan probabilitasnya (wedding, MICE, corporate membership, sport membership, corporate golf, tournament)? | EP-02 | Club (Sales) | ✅ | **Sesuai default** tahap & probabilitas di [§16.1](#161-default-tahap-pipeline-keputusan-2) |
| 3 | Aturan lead assignment: round-robin, sales tetap per lini, atau manual oleh manager? Target first response? | EP-01 | Club | 🅰 | Club menekankan **first response**. Asumsi: lead dibagi **round-robin** ke sales di tim lini terkait, dan Sales Manager boleh reassign. Target first response **≤ 1 jam** di jam kerja (08.00–20.00); lead di luar jam kerja dibalas paling lambat 09.00 hari berikutnya. Lewat target → eskalasi ke Sales Manager |
| 4 | Skema komisi sales: persen per lini, tier, kapan diakui (DP diterima, event selesai, lunas)? | EP-04 | Club + Finance | ✅ / 🅰 | ✅ Komisi **diakui saat lunas**. 🅰 Persentase flat dari nilai net (sebelum pajak & service charge), tanpa tier: Wedding 1%, MICE 1%, Corporate Membership 3%, Sport Membership 3%, Corporate Golf 2%, Tournament 2%. Clawback bila ada refund ≤ 90 hari setelah lunas |
| 5 | Batas diskon per role untuk quotation dan POS; siapa approver? | EP-03, EP-10 | Club | 🅰 | Staff (sales, kasir) ≤ 5% · Supervisor ≤ 10% · Outlet Manager / Sales Manager ≤ 20% · > 20% atau complimentary → General Manager. Diproses lewat approval engine P0 |
| 6 | Apakah lead media sosial perlu integrasi Meta/TikTok, atau cukup input manual + WhatsApp bisnis? | EP-24 | Club + Marketing | ✅ | **Manual dulu** (input manual + WhatsApp bisnis). Integrasi Meta/TikTok ditunda ke fase berikutnya |
| 7 | Aturan loyalty: nilai earning, nilai tukar, expiry, tier & benefit, lini yang ikut, apakah member dan non-member sama? | EP-09 | Club + Finance | 🅰 | **Earning:** 1 poin per Rp10.000 net spend (tanpa pajak & service charge). **Nilai tukar:** 1 poin = Rp100 (setara 1%). **Expiry:** 12 bulan sejak poin diperoleh. **Lini peserta:** golf (green fee, cart; caddy fee tidak), F&B, sport club, bungalow, pro shop. **Tidak ikut:** membership fee, banquet/wedding, tournament fee. **Member & non-member terdaftar** sama-sama earning; perbedaan lewat tier (P5 #14) |
| 8 | Daftar promosi berjalan dan aturan stacking yang diinginkan (mis. member discount + happy hour)? | EP-10 | Club (F&B, Marketing) | 🅰 | **Default tidak stack**: sistem memilih harga terbaik untuk customer. Pengecualian: voucher/poin loyalty boleh dipakai bersama satu promosi. Member discount dan happy hour tidak stack. Contoh promosi awal: Happy Hour bar 16.00–18.00 (30% minuman), Weekday Twilight Golf, Birthday Month |
| 9 | Daftar paket lintas lini yang akan dijual beserta alokasi pendapatan per komponen | EP-11 | Club + Finance | 🅰 | Paket awal: **Golf & Lunch**, **Golf & Stay** (golf + bungalow 1 malam + sarapan), **Corporate Golf Day** (golf + meeting room + dinner), **Family Weekend** (kolam + court + F&B), **Wedding Package**. **Alokasi:** proporsional terhadap harga jual standalone tiap komponen (PSAK 72). Caddy fee dikeluarkan lebih dulu sebagai titipan (bukan pendapatan) |
| 10 | Apakah installment membership fee / paket besar dibutuhkan di P3? | FR-BIL-P3-07 | Club + Finance | 🅰 | **Ya.** Membership fee: DP minimal 30%, cicilan maks 12× tanpa bunga. Paket/wedding ≥ Rp50 jt mengikuti termin banquet (#11) |
| 11 | Kebijakan banquet: DP minimum, H-7 pelunasan, final pax cut-off, forfeiture DP saat batal, nilai corkage, kuota listrik | EP-13, EP-22 | Club (Banquet) | 🅰 | **DP 30%** saat konfirmasi; tanggal baru di-block sebagai Definite setelah DP. Termin 2: 40% di H-60; **pelunasan H-7**. **Final pax H-7**; boleh turun maks 10% dari guarantee. **Pembatalan:** > 90 hari → 50% DP dikembalikan; 30–90 hari → DP hangus; < 30 hari → seluruh pembayaran hangus. **Corkage** Rp150.000/botol wine/liquor; makanan luar hanya dari vendor rekanan. **Listrik** termasuk 10.000 W per ballroom; tambahan sesuai rate card |
| 12 | Kalender tournament dan format yang dipakai (stroke play, stableford, tim?); siapa tournament director; handicap resmi atau lokal? (roadmap §74 #8) | EP-16 | Club (Golf) | 🅰 | **Kalender:** Monthly Medal (Sabtu minggu ke-2 tiap bulan), Club Championship tahunan (Agustus, 2 hari), 2–4 corporate/sponsor tournament per tahun. **Format P3:** stroke play (gross/net) dan stableford. **Tournament director:** Golf Manager. **Handicap:** handicap lokal OneClub, dengan handicap index PGI diinput manual bila pemain punya |
| 13 | Termin dan credit limit default corporate account; apakah perlu statement bulanan perusahaan? | EP-17 | Finance | 🅰 | Termin **30 hari**; credit limit default **Rp50 jt**, perubahan disetujui Finance Manager; **statement bulanan** perusahaan dikirim tanggal 1 |
| 14 | Jam cut-off night audit dan siapa yang menjalankannya (front desk malam atau finance)? | EP-18 | Finance + Operasional | 🅰 | Cut-off **02.00 WIB**. Night audit dijalankan **otomatis oleh sistem**; Duty Manager malam menyelesaikan exception (folio terbuka, selisih kas); Finance mereview laporan night audit pagi harinya |
| 15 | Prioritas jika kapasitas tidak cukup: urutan gelombang R3.1–R3.4 disetujui? Epic Should mana yang boleh bergeser ke P4/P5? | Milestones | Management + Club | 🅰 | Urutan **sesuai PRD P3 §14.2**. Epic Should boleh bergeser ke gelombang berikutnya bila kapasitas kurang; diputuskan Product + GM per milestone |
| 16 | Persetujuan usulan label dan status baru (§7.6) dan pembaruan NC §23, §25, §31 | IA | Product | 🅰 | **Disetujui** sesuai usulan PRD P3 §7.6; Naming Convention diperbarui |
| 17 | Data sumber migrasi: daftar event mendatang & DP, piutang corporate, histori tournament, lead aktif (Rhapsody, Excel, atau lainnya)? | EP-25 | Club | 🅰 | **Rhapsody** untuk data customer/member yang sudah ada. **Excel** untuk event mendatang & DP banquet, piutang corporate, lead aktif, dan histori tournament 2 tahun terakhir. Data yang lebih lama tidak dimigrasi |
| 18 | Apakah quotation/kontrak memerlukan tanda tangan elektronik bersertifikat? | FR-QUO-09 | Club + Legal | 🅰 | **Tidak wajib di P3.** Persetujuan quotation lewat link + OTP dengan audit trail. Kontrak bernilai > Rp5 jt memakai **e-Meterai**. Tanda tangan elektronik bersertifikat (PSrE) ditunda |
| 19 | Retensi data lead yang tidak menjadi customer (UU PDP)? | EP-01, §12 | Club + Legal | 🅰 | Disimpan **2 tahun** sejak interaksi terakhir, lalu dianonimkan; dihapus lebih awal bila diminta. Retensi data HR diatur di PRD P5 §16 #10 |
| 20 | Apakah Release 3 boleh dirilis bertahap per gelombang, atau club ingin satu go-live? | §14.2 | Management + Club | 🅰 | **Bertahap** per gelombang R3.1–R3.4 |

## 16.1 Default Tahap Pipeline (keputusan #2)

Probabilitas dipakai untuk forecast: *nilai tertimbang = nilai deal × probabilitas tahap*. Semua lini punya tahap **Lost** (0%) dengan alasan kalah; tahap dan persentase configurable di Settings.

| Lini | Tahap (probabilitas) |
|---|---|
| Wedding | New Inquiry 10% → Site Visit 25% → Food Tasting 40% → Quotation 50% → Negotiation 70% → DP Paid / Won 100% |
| MICE / Meeting | Inquiry 10% → Requirement Gathering 25% → Proposal / Quotation 50% → Negotiation 70% → Contract Signed / Won 100% |
| Corporate Membership | Lead 10% → Presentation 30% → Proposal 50% → Negotiation 70% → Agreement Signed / Won 100% |
| Sport Membership | Inquiry 10% → Facility Tour 30% → Trial / Offer 60% → Payment / Won 100% |
| Corporate Golf | Inquiry 10% → Proposal 40% → Negotiation 70% → Confirmed (DP) / Won 100% |
| Tournament (eksternal / sponsor) | Inquiry 10% → Proposal 40% → Sponsor / Format Agreed 70% → Contract & DP / Won 100% |
