# OneClub
## Product Requirements Document — P5 People & Advanced Enterprise

**Version:** 1.1 (Draft): keputusan & asumsi Open Questions diterapkan  
**Phase:** P5 — People & Advanced Enterprise (Release 5 — Enterprise Management)  
**Based on:** `product-roadmap.md` (§3, §55–62, §65–75, §77), `OneClub — Product Overview v3.md` (§8, §19, §29, §37, §39, §43–44, §49, §52), `OneClub — Naming Convention.md` (§6, §16, §22–25, §28–33), `OneClub — Technical Documentation.md` (§4, §6, §7.6, §8.2, §9.3, §13–15), `OneClub — PRD P0 Platform Foundation.md`, `OneClub — PRD P1 Golf Core MVP.md`, `OneClub — PRD P2 Complete Golf Experience & Shared Core.md`, `OneClub — PRD P3 Commercial & Business Expansion.md`, `OneClub — PRD P4 Enterprise Back Office.md`  
**Date:** 4 October 2026

> Requirement yang ditandai *(usulan)* adalah turunan PRD ini dan belum tercantum eksplisit di roadmap atau Product Overview. Ketidaksesuaian antar dokumen sumber dan keputusan PRD ini atas masing-masing ada di [bagian 6](#6-hasil-pengecekan-dokumen-sumber). Keputusan dan asumsi atas open question ada di [bagian 16](#16-open-questions).

> **Pembaruan arsitektur aplikasi (5 Oktober 2026).** Staff App tetap **satu aplikasi** (satu build) dengan area per role, dan dibuka di empat domain (Product Overview §45, Technical Documentation §6.1): `dashboard.<club>` untuk kantor (Back Office, Management Dashboard yang dibatasi menu/permission, Platform Administration, dan Clubhouse Screen untuk role **Screen**; login email + password + MFA), `cashier.<club>` untuk perangkat kasir dan konter (Operational Interface, PIN per shift), `caddy.<club>` untuk Caddy Tablet, dan `kitchen.<club>` untuk Kitchen Display. Domain mengunci area; perangkat bersama tidak membuka area kantor. Untuk P5: HRIS, payroll, BI dan konfigurasi enterprise ada di `dashboard`; Employee Self Service dibuka di `dashboard` dengan menu terbatas sesuai role karyawan, dan absensi di perangkat bersama (kiosk/tablet) memakai `cashier` *(usulan)*.

> **Keputusan HRIS.** Payroll dijalankan **penuh di OneClub** (Mode A, [§16 #1](#16-open-questions)), menjawab keputusan terbuka di Product Overview §39 dan Tech Doc keputusan #6. Opsi payroll pihak ketiga (Mode B) tidak dibangun; perbandingannya disimpan di [bagian 5.5](#55-mode-payroll) sebagai catatan keputusan.

---

# 1. Ringkasan

P5 menyelesaikan OneClub sebagai **platform enterprise**: orang yang menjalankan club (karyawan, caddy mitra, instruktur) dikelola dari kontrak sampai pembayaran, dan manajemen mendapatkan kemampuan lanjutan untuk retensi customer, analitik lintas domain, paket multi-bisnis, dan tournament. Targetnya adalah **Release 5 — Enterprise Management** (roadmap §75): *HRIS, payroll, advanced CRM, loyalty, BI* untuk **Modern Golf & Country Club**, Tangerang.

P5 juga menutup pembayaran yang sejak P2–P4 sengaja berhenti di tahap "dihitung, disetujui, diekspor": caddy fee settlement dan honor instruktur (P2), komisi sales (P3), dan pool service charge (P4).

P5 menghasilkan:

```text
People & Advanced Enterprise
├── CORE HR
│   ├── Organization & Employee (melanjutkan Department & Employee P0)
│   ├── Employment Contract (PKWT / PKWTT) & Employee Documents
│   ├── Recruitment, Training & Certification (lifeguard, caddy, food handler)
│   └── Performance Review
├── ATTENDANCE & WORKFORCE
│   ├── Shift Schedule (F&B, reception, lifeguard, starter, dst.)
│   ├── Attendance (fingerprint, face recognition, mobile GPS)
│   └── Leave, Permission & Overtime
├── PAYROLL
│   ├── Salary, Allowances, Overtime, Bonus, THR
│   ├── PPh 21, BPJS Kesehatan & Ketenagakerjaan
│   ├── Service Charge Distribution, Sales Commission payout
│   ├── Caddy (mitra): caddy fee & tip payout
│   ├── Instructor: session / per-student honor payout
│   ├── Payroll journal → Accounting P4, payment file bank
│   └── Pelaporan e-Bupot PPh 21 & iuran BPJS
├── EMPLOYEE SELF SERVICE
│   └── Mobile ESS: payslip, leave, attendance, schedule, documents
├── ADVANCED CRM & LOYALTY
│   ├── Advanced Segmentation, Cross-business Behavior, VIP Segmentation
│   ├── Loyalty Tiers, Rewards, Reward Eligibility (engine)
│   ├── Campaign, Renewal & Birthday Automation (journey)
│   └── NPS, Complaint SLA, Sales Performance & Commission Analytics
├── MANAGEMENT & BI
│   └── Executive dashboard lintas domain, HR KPI, target, drill-down, scheduled report
├── ADVANCED PACKAGE
│   └── Multi-business package, capacity & time block, profitability
├── ADVANCED TOURNAMENT
│   └── Format tim, series / order of merit, historical tournament, integrasi federasi
└── OPERATIONS
    ├── HR & Workforce Policies, integrasi device absensi & payroll
    └── Migrasi gelombang 5 & Release 5
```

Alur inti baru yang harus berjalan end-to-end di production:

```text
Hire:      Recruitment → Offer → Employee + Contract → User & Role (P0) → Training & Certification
Work:      Shift Schedule → Clock-in (fingerprint / face / GPS) → Overtime / Leave Request → Approval
Pay:       Attendance + Overtime + Allowance + Service Charge + Commission + THR
           → Payroll Run (PPh 21, BPJS) → Approval → Payslip (ESS) → Bank File → Payroll Journal (P4)
Partner:   Caddy fee & tip (P2) / Instructor honor (P2) → Payout Run → Statement → Payment → Journal
Retain:    Behavior & Segment → Journey (renewal, birthday, win-back) → Offer / Reward → Tier
Manage:    Data operasional P1–P4 → Analytics store → Executive & domain BI → Target vs Actual
```

---

# 2. Latar Belakang & Problem

- **Data karyawan baru sebatas master.** P0 hanya menyimpan Department dan Employee dasar (nomor, nama, department, jabatan, relasi ke User) untuk approval dan audit (PRD P0 FR-MD-01/02). Kontrak, dokumen, sertifikasi, jadwal, absensi, cuti, dan gaji masih di Excel atau sistem terpisah.
- **Operasional club berbasis shift.** Outlet F&B, resepsionis sport club, lifeguard, starter, dan dapur bekerja dalam shift; lembur dan pertukaran shift dicatat manual, sehingga lembur sulit dikendalikan dan dibayar tepat (Product Overview §39).
- **Pembayaran berhenti di "siap bayar".** Caddy fee settlement dan honor instruktur (PRD P2 §6 #9), komisi sales (PRD P3 FR-COM-04), dan pool service charge (PRD P4 FR-REV-06) sudah dihitung dan dibukukan, tetapi pembayaran ke orangnya masih manual.
- **Kepatuhan ketenagakerjaan berisiko.** PPh 21, BPJS Kesehatan & Ketenagakerjaan, THR, dan aturan lembur dihitung manual atau di aplikasi terpisah tanpa terhubung ke absensi dan akuntansi.
- **Sertifikasi wajib tidak terpantau.** Lifeguard, caddy, dan food handler memerlukan sertifikasi yang berlaku (roadmap §56); jadwal saat ini tidak mencegah staf tanpa sertifikasi aktif bertugas.
- **Retensi dan analitik masih manual.** P3 membangun fondasi loyalty, segmentasi, campaign, dan Top Spender, tetapi tier, reward eligibility, journey otomatis, dan analitik NPS/SLA/sales masih dikerjakan tim secara manual (PRD P3 §5.2). Dashboard per domain sudah ada sejak P1–P4, tetapi belum ada pandangan eksekutif lintas domain dengan target dan drill-down.

---

# 3. Goals & Non-Goals

## 3.1 Goals

| # | Goal | Ukuran keberhasilan |
|---|---|---|
| G1 | Satu sumber data karyawan & tenaga mitra | 100% karyawan aktif, caddy, dan instruktur memiliki profil, status, dan dokumen di OneClub; kontrak kedaluwarsa terdeteksi H-30 |
| G2 | Jadwal & absensi terkendali | Seluruh departemen shift memakai Shift Schedule; ≥ 95% kehadiran tercatat otomatis dari device/mobile *(usulan target)*; lembur hanya dibayar bila disetujui |
| G3 | Payroll tepat waktu dan patuh | Payroll bulanan selesai tanpa koreksi manual di luar sistem; PPh 21 & BPJS sesuai uji kasus konsultan pajak |
| G4 | Semua pembayaran ke orang tertutup | Caddy fee & tip, honor instruktur, komisi sales, dan service charge dibayar lewat payout/payroll run; saldo kewajiban terkait di GL P4 kembali nol setelah pembayaran |
| G5 | Sertifikasi wajib ditegakkan | 0 shift lifeguard/caddy/food handler yang dijadwalkan dengan sertifikasi kedaluwarsa |
| G6 | Retensi otomatis | Renewal, birthday, dan minimal satu journey win-back berjalan otomatis; tier loyalty dievaluasi otomatis; kinerja journey terukur |
| G7 | Manajemen melihat seluruh club | Executive dashboard menampilkan KPI semua domain (roadmap §60) dengan target vs aktual dan drill-down; angka = laporan sumber |
| G8 | Paket & tournament lanjutan | Paket multi-bisnis dengan profitability; minimal satu series/order of merit dan format tim berjalan |
| G9 | Tanpa regresi Release 1–4 | Seluruh suite P1–P4 tetap lulus |

## 3.2 Non-Goals (P5)

- Provisioning multi-customer, subscription, feature tier, white-label, SSO enterprise: **P6** (roadmap §63)
- Prediksi, optimasi, dan intelligence (demand forecasting, churn prediction, dynamic pricing): **P7** (roadmap §64). P5 menyediakan analitik deskriptif dan aturan
- Learning management system lengkap (kursus daring, kuis) *(tidak tercantum di roadmap)*; P5 mencatat training & sertifikasi
- Manajemen kinerja berbasis OKR lanjutan dan kompensasi berbasis pasar *(tidak tercantum di roadmap)*
- Portal rekrutmen publik dengan job board eksternal *(usulan Should, lihat EP-03)*
- Payroll multi-negara dan multi-currency
- Full hotel HR (housekeeping roster, laundry): Out of Scope (roadmap §73)

---

# 4. Users & Personas

Role mengikuti Product Overview §44. Role template sudah di-seed di P0 (FR-IAM-06); P5 mengisi permission domain baru dan mengaktifkan role yang mulai login.

| Persona | Label role (UI) | Kebutuhan di P5 |
|---|---|---|
| **HR** | **HR Admin, HR Manager** | **Mulai aktif penuh di P5**: karyawan, kontrak, rekrutmen, training, sertifikasi, jadwal, absensi, cuti, lembur, payroll |
| **Karyawan** | **Employee (self-service)** | **Mulai login di P5** lewat ESS: jadwal, clock-in, cuti/izin/lembur, payslip, dokumen |
| Kepala departemen | Outlet Manager, Golf Manager, Sport Club Manager, Banquet Manager, dst. | Membuat jadwal shift, menyetujui cuti/lembur, penilaian kinerja tim |
| Keuangan | Finance Manager, Accountant | Payroll approval, payroll journal, pembayaran, service charge, komisi, caddy & instruktur payout |
| Caddy master | Caddy Manager | Absensi & rotasi caddy (P1–P2), sertifikasi, rating, payout caddy |
| **Caddy** | Caddy | Statement caddy fee & tip, riwayat payout di Caddy App (P2) |
| Instruktur | Instructor / Coach | Jadwal mengajar, sesi, statement honor |
| Lifeguard | Lifeguard | Jadwal, sertifikasi, clock-in |
| Sales & CRM | Sales Executive, CRM Admin, Marketing Staff | Journey otomatis, VIP segment, analitik sales & komisi, NPS & SLA analytics |
| Manajemen | General Manager, Club Manager, Resort Manager | Executive BI lintas domain, HR KPI, target vs aktual |
| Pengelola golf | Golf Manager, Golf Admin | Tournament series, format tim, historical, federasi |
| Commercial | Outlet Manager, Reservation Staff | Paket multi-bisnis & profitability |
| Tim internal OneClub | Platform Admin | Device absensi, integrasi payroll/BPJS/DJP, migrasi gelombang 5, Release 5 |

---

# 5. Scope

## 5.1 In Scope

| Area | Referensi roadmap | Epic |
|---|---|---|
| Organization & Employee | §56 | EP-01 |
| Employment Contract & Documents | §56 | EP-02 |
| Recruitment | §56 | EP-03 |
| Training & Certification | §56 | EP-04 |
| Performance Review | §56 | EP-05 |
| Shift Scheduling | §57 | EP-06 |
| Attendance | §57 | EP-07 |
| Leave, Permission & Overtime | §57 | EP-08 |
| Payroll Engine | §58 | EP-09 |
| PPh 21 & BPJS | §58 | EP-10 |
| Service Charge Distribution | §58 | EP-11 |
| Sales Commission & Bonus Payout | §58, §59 | EP-12 |
| Non-Employee Workforce: Caddy | §58 | EP-13 |
| Non-Employee Workforce: Instructor | §58 | EP-14 |
| Payroll Accounting, Payment & Payslip | §58, §52 | EP-15 |
| Employee Self Service | §56 | EP-16 |
| Advanced Segmentation & VIP | §59 | EP-17 |
| Advanced Loyalty | §59 | EP-18 |
| Campaign & Lifecycle Automation | §59 | EP-19 |
| CRM & Sales Analytics | §59 | EP-20 |
| Management Dashboard & BI | §60, §66.5 | EP-21 |
| Advanced Package Management | §61 | EP-22 |
| Advanced Tournament | §62 | EP-23 |
| HR & Workforce Policies | §65 *(usulan perluasan)* | EP-24 |
| Integrasi P5 | §54 (lanjutan), §57 | EP-25 |
| Operational Interfaces & Apps P5 | §66.4 | EP-26 |
| HR KPI & Reports | §60, §69 | EP-27 |
| Migrasi gelombang 5 | §72 *(usulan perluasan)* | EP-28 |
| Production Readiness (Release 5) | §75 | EP-29 |

## 5.2 Batas P5 vs P6/P7 per Area

| Area | Dibangun di P5 | Lanjut di P6/P7 |
|---|---|---|
| HRIS | Core HR, kontrak, dokumen, rekrutmen, training, sertifikasi, performance review, jadwal, absensi, cuti, lembur, ESS | Workforce forecasting & optimasi jadwal otomatis (P7) |
| Payroll | Engine payroll lengkap Indonesia (PPh 21, BPJS, THR); payout mitra (caddy, instruktur) | Payroll sebagai fitur tier SaaS (P6) |
| CRM & Loyalty | Segmentasi lanjutan, VIP, tier & reward engine, journey otomatis, analitik NPS/SLA/sales | Churn prediction, next-best-offer (P7) |
| BI | Analytics store, executive & domain dashboard, target, drill-down, scheduled & self-service report | Revenue/customer/operational intelligence (P7) |
| Package | Multi-business package, capacity & time block, profitability | Dynamic package pricing (P7) |
| Tournament | Format tim, series/order of merit, historical, federasi | Golf intelligence (P7) |

## 5.3 Dependensi ke P0–P4

| Kapabilitas sebelumnya | Dipakai P5 untuk |
|---|---|
| Department, Employee dasar, relasi User (P0 FR-MD-01/02, FR-IAM-01) | Employee master HRIS tanpa migrasi destruktif (EP-01) |
| IAM, role template HR (P0 FR-IAM-06), Devices + PIN (FR-IAM-09) | Akses ESS, kiosk absensi |
| Approval engine (P0 EP-06) | Cuti, lembur, tukar shift, payroll run, payout, offer rekrutmen |
| Notification (P0 EP-05, WhatsApp BSP P1) | Reminder kontrak/sertifikasi, payslip, journey CRM |
| Bridge agent hardware (P0 FR-INT-07, P2–P4) | Device fingerprint & face recognition (EP-25) |
| Caddy attendance, rotation, level, rating, settlement, tip (P1 EP-09, P2 EP-05) | Caddy payout & sertifikasi (EP-13) |
| Class schedule, attendance, instructor fee (P2 EP-15) | Instructor payout (EP-14) |
| Sales target & commission statement (P3 EP-04) | Payout komisi & analitik (EP-12, EP-20) |
| Loyalty foundation, segment, campaign, ticket, NPS, Top Spender (P2 EP-24, P3 EP-05–09) | Advanced CRM & Loyalty (EP-17–20) |
| Package engine (P3 EP-11), Tournament (P3 EP-16) | Advanced Package & Tournament (EP-22–23) |
| Accounting: CoA, posting rule, service charge pool, AP, bank file (P4 EP-16–21) | Payroll journal, kewajiban PPh 21/BPJS, pembayaran (EP-11, EP-15) |
| KPI & read model P1–P4, read replica (Tech Doc §7.6) | Analytics store & BI (EP-21) |

## 5.4 Pengembangan Paralel dengan P6

Gelombang 1 = P1 ‖ P2 dan gelombang 2 = P3 ‖ P4 (PRD P2–P4 §5.4). PRD ini mengusulkan **gelombang 3 = P5 ‖ P6** *(usulan)*: P6 (SaaS Commercialization) bekerja pada platform/provisioning, sedangkan P5 pada domain club, sehingga konflik module kecil. Tech Doc belum memuat §12.3 ([bagian 6](#6-hasil-pengecekan-dokumen-sumber) #1).

### 5.4.1 Kepemilikan Module *(usulan)*

| Module (Tech Doc §4.1) | Pemilik gelombang 3 | Bagian P5 | Aturan |
|---|---|---|---|
| `hris` (baru) | **P5** | EP-01–16, EP-24, EP-27 | Module & schema baru; data Employee P0 dipindah dengan pola expand → migrate → contract (Tech Doc §7.5) |
| `platform` (department, employee) | Bersama | Pindah kepemilikan Employee ke `hris` | API P0 dipertahankan sebagai fasad selama satu rilis |
| `golf`, `sportclub` | P1–P3 | Payout memakai interface publik settlement P2 | P5 tidak menulis tabel caddy/instruktur |
| `crm` | **P5** | `crm/journey`, `crm/loyalty` (lanjutan), `crm/analytics` | Aditif terhadap P3 |
| `commercial/package`, `golf/tournament` | **P5** | Lanjutan P3 | Aditif; contract test P3 tetap lulus |
| `reporting` | **P5** | Analytics store & BI | Read model P1–P4 tetap; analytics store terpisah (Tech Doc §7.6) |
| `accounting` | P4 | — | P5 hanya publish event payroll & payout; posting rule ditambah (aditif) |
| `platform/provisioning`, tier, white-label | **P6** | — | P5 menyediakan daftar module & seed HR untuk feature tier (kontrak H6) |

**Arah dependensi** (Tech Doc §4.2): `hris` berada di lapis back office. Module lini bisnis tidak memanggil `hris`; `hris` membaca settlement caddy, honor instruktur, dan komisi lewat interface publik atau event (`golf.caddy_settlement_approved`, `sportclub.instructor_fee_approved`, `crm.commission_approved`). Payroll memposting ke `accounting` lewat event `hris.payroll_posted` (Tech Doc §4.3).

### 5.4.2 Kontrak

| # | Kontrak | Pemilik | Dipakai P5 untuk |
|---|---|---|---|
| H1 | Caddy settlement & tip (approved) | P2 | Caddy payout (EP-13) |
| H2 | Instructor fee (approved) | P2 | Instructor payout (EP-14) |
| H3 | Commission statement (approved) | P3 | Payout komisi (EP-12) |
| H4 | Service charge pool & dasar pembagian per departemen | P4 | Distribusi ke karyawan (EP-11) |
| H5 | Posting rule payroll & payout, AP/bank payment | P4 | Jurnal payroll, kewajiban PPh 21/BPJS, pembayaran (EP-15) |
| H6 *(disediakan P5)* | Module HRIS & seed role/policy HR untuk provisioning dan feature tier | P5 → P6 | Paket SaaS (P6) |
| H7 *(disediakan P5)* | `hris.employee_terminated` dan `hris.certification_expired` | P5 | IAM menonaktifkan user; golf/sportclub mencegah penugasan staf tanpa sertifikasi |

## 5.5 Mode Payroll

| Aspek | **Mode A — Payroll penuh di OneClub** | **Mode B — HR operasional + payroll pihak ketiga** |
|---|---|---|
| Dibangun OneClub | Seluruh EP-01–16 | EP-01–08, EP-11–14 (payout mitra & komisi), EP-16 (tanpa payslip gaji), EP-25 (integrasi) |
| Gaji, PPh 21, BPJS, THR | Dihitung OneClub (EP-09–10) | Dihitung penyedia (mis. Talenta, Gadjian); OneClub mengirim input |
| Input dari OneClub | — | Karyawan, kontrak, komponen gaji tetap, absensi, lembur disetujui, cuti, service charge, komisi |
| Payslip | ESS OneClub | Dari penyedia; ESS menautkan bila API tersedia |
| Jurnal | Payroll journal otomatis (EP-15) | Jurnal dari ringkasan payroll penyedia (import) |
| Payout caddy & instruktur | OneClub (keduanya tenaga mitra) | OneClub |

**Keputusan: Mode A** ([§16 #1](#16-open-questions)). Draft awal PRD merekomendasikan Mode B karena risiko kepatuhan lebih rendah; club memilih payroll penuh di OneClub. Konsekuensinya, uji kasus PPh 21/BPJS oleh konsultan pajak dan parallel run payroll minimal satu periode menjadi **Must** (EP-10, EP-28). Requirement Mode B tidak dibangun.

---

# 6. Hasil Pengecekan Dokumen Sumber

Pengecekan silang roadmap, Product Overview, Naming Convention, Technical Documentation, PRD P0–P4, dan kode `oneclub` (branch `staging`, commit `a48e6a3`) menemukan hal berikut. Keputusan di kolom kanan berlaku untuk PRD ini sampai dikonfirmasi di [Open Questions](#16-open-questions).

| # | Temuan | Sumber | Keputusan PRD P5 |
|---|---|---|---|
| 1 | **Tech Doc §12.3 tidak ada**, padahal PRD P2–P4 merujuk aturan gelombang di sana; gelombang untuk P5 belum ditentukan di dokumen mana pun | PRD P2–P4 §5.4; Tech Doc | Usulan gelombang 3 = P5 ‖ P6 (§5.4). Tech Doc perlu menambahkan §12.3 (OQ #2) |
| 2 | **Full payroll vs payroll pihak ketiga** belum diputuskan | PO §39; Tech Doc §15 #6; roadmap §55 | **Mode A**: payroll penuh di OneClub (§5.5, keputusan §16 #1) |
| 3 | **Advanced Tournament P5 tumpang tindih dengan P3** (shotgun start, scoring, leaderboard, sponsor, prize) | Roadmap §48 vs §62; PRD P3 §6 #3 | Mengikuti PRD P3: P5 = format tim, series/order of merit, historical, federasi (EP-23) |
| 4 | **Advanced Package P5 tumpang tindih dengan P3** (resource bundle, BOM, revenue allocation) | Roadmap §45 vs §61; PRD P3 §6 #4 | Mengikuti PRD P3: P5 = multi-business package dengan kapasitas & time block lanjutan, profitability (EP-22) |
| 5 | **Management Dashboard & BI §60** memuat KPI semua domain, padahal dashboard per domain sudah dibangun P1–P4 | Roadmap §60 vs §69; PRD P1–P4 | P5 = **analytics store**, executive dashboard lintas domain, HR KPI baru, target vs aktual, drill-down, scheduled & self-service report; KPI domain tidak dibangun ulang (EP-21) |
| 6 | **Caddy adalah mitra**, bukan karyawan (PO §8, §39), tetapi tercantum di Payroll §58; P2 menyebut "Payroll/BPJS caddy bila dipilih" | PO §39; roadmap §58; PRD P2 §5.2 | Caddy & instruktur sebagai **Non-Employee Workforce** dengan **payout run** terpisah dari payroll karyawan; perlakuan pajak & BPJS mitra diputuskan (OQ #4) |
| 7 | **Caddy attendance & rotation** sudah ada di P1–P2 (Caddy Master), dan muncul lagi di P5 §58 | PRD P1 EP-09; PRD P2 EP-05; roadmap §58 | Tidak dibangun ulang. P5 menambah sertifikasi caddy, statement & payout, serta opsi clock-in device |
| 8 | **ESS mobile** dibutuhkan, tetapi Tech Doc §6.1 tidak punya app ESS; role "Employee (self-service)" ada di P0 seed | Tech Doc §6.1; PO §44; roadmap §56 | ESS sebagai area **personal login** di shell `ops` (PWA) *(usulan)*, terpisah dari mode device/PIN shift. OQ #6 |
| 9 | **Biometrik (fingerprint, face recognition)** adalah data pribadi spesifik menurut UU PDP | Roadmap §57; Tech Doc §9.3 | Template biometrik tetap di device/vendor; OneClub menyimpan event kehadiran, bukan citra wajah/sidik jari; consent tertulis; alternatif non-biometrik tersedia |
| 10 | **Renewal & birthday automation** sudah ada sebagian di P3 (reminder terjadwal) dan P2 menundanya ke P5 | PRD P2 §5.2; PRD P3 FR-CMP-06; roadmap §59 | P5 = journey bertahap (multi-langkah, kondisi, A/B) di atas reminder P3 (EP-19) |
| 11 | **Service charge**: pool & kewajiban di P4, distribusi ke karyawan di P5; dasar pembagian belum diputuskan | PRD P4 FR-REV-06, OQ #16; PO §37 | Engine distribusi dengan rule configurable (poin jabatan, kehadiran, departemen) (EP-11) |
| 12 | **Recruitment** tercantum di roadmap §56, tetapi menu NC §22 sudah ada; portal pelamar publik tidak disebut | Roadmap §56; NC §22 | Recruitment internal Must; halaman karier publik Should (EP-03) |
| 13 | **Status HR belum ada di NC §31** (kontrak, cuti, lembur, payroll run, payout, rekrutmen) | NC §31 | Usulan status di §7.6 |
| 14 | **HR Policies** tidak ada di Club Policies NC §25 | NC §25, §33 | Usulan label HR & Workforce Configuration/Policies (§7.6) |
| 15 | **Data Entity Rollout §70 (P5)** hanya memuat Shift, Attendance, Payroll, Employee Certification, Training, Performance Review | Roadmap §70 | Entitas tambahan (kontrak, cuti, lembur, payout, journey, analytics) di §10 |
| 16 | **Kode saat ini**: `platform.departments` & `platform.employees` (P0) ada; module `hris` belum ada; P2–P4 masih PRD | Repo `oneclub` | P5 dimulai setelah Release 4; Employee P0 dipindah ke `hris` dengan fasad API |
| 17 | **Product Overview §49 Fase 4** (HRIS & Payroll, Tournament lanjutan, Advanced CRM & Loyalty, Advanced Analytics/BI, Advanced Package) konsisten dengan roadmap P5 | PO §49; roadmap §3 | Tidak ada konflik |
| 18 | **Ketentuan ketenagakerjaan** (PPh 21 TER, BPJS, THR, lembur, cuti tahunan) berubah lewat regulasi dan tidak dirinci di dokumen sumber | — | Semua tarif, batas upah, dan rumus **configurable dan berversi**; nilai awal diverifikasi konsultan pajak/hukum sebelum go-live (OQ #3) |

---

# 7. Information Architecture

Label mengikuti `OneClub — Naming Convention.md`. Module dan menu P5 muncul hanya bila module aktif (P0 FR-INS-04) dan user berizin.

## 7.1 Back Office — Module & Menu Baru/Aktif di P5

| Module | Menu baru di P5 | Catatan |
|---|---|---|
| HRIS | **Employees, Organization, Recruitment, Training & Certification, Attendance, Schedules, Leave & Permission, Overtime, Payroll, Benefits, Service Charge, Commissions, Caddy, Instructors, HR Reports** | NC §22 |
| Settings → Organization | Departments & Employees dialihkan ke HRIS | Data P0 dipindah (§6 #16) |
| CRM | **Journeys** *(usulan)*, Segments (lanjutan), Loyalty (Tiers, Rewards, Eligibility), CRM Reports (analytics) | Melengkapi menu P3 |
| Commercial | Packages: + Profitability, Capacity | Lanjutan P3 |
| Golf | Tournaments: + Series, Team Formats, Tournament History | Lanjutan P3 |
| Reports | **HR Reports** | NC §23 |
| Dashboard | **HR Performance**; Executive Overview lintas domain dengan target | NC §23 |
| Settings | **HR Configuration, Payroll Configuration, Attendance Configuration** *(usulan)*; Club Policies: HR Policies *(usulan)* | §7.6 |

## 7.2 Operational Staff (`ops`) & Employee Self Service

| Interface | Menu (NC + usulan) | Pengguna |
|---|---|---|
| **Employee Self Service** *(usulan, personal login di `ops`)* | My Schedule, Clock In / Out, Attendance History, Leave & Permission, Overtime, Payslip, My Documents, My Training, Profile | Employee (self-service) |
| Manager (di ESS) *(usulan)* | Team Schedule, Approvals (cuti, lembur, tukar shift), Team Attendance | Kepala departemen |
| Attendance Kiosk *(usulan)* | Clock In / Out dengan QR/PIN/face (device terdaftar) | Semua staf di lokasi |
| Caddy Master | + Caddy Certification | Caddy Manager |
| Instructor | + My Sessions, Honor Statement | Instructor / Coach |

## 7.3 Caddy Application (`caddy`)

Mengikuti NC §28: **Earnings** (Caddy Fee, Tip, Settlement) ditambah **Payout History** dan **Statement** *(usulan)*; **Attendance** dapat memakai clock-in device bila club memilih.

## 7.4 Member & Guest Portal (`member`) dan Website (`web`)

- **Loyalty**: tier benefit, progres ke tier berikutnya, reward yang eligible (EP-18).
- **Offers**: penawaran dari journey (EP-19).
- **Golf → Tournaments**: series standing (order of merit), riwayat tournament (EP-23).
- **Website**: halaman karier *(Should, EP-03)*; leaderboard series publik sesuai consent.

## 7.5 Struktur Organisasi (roadmap §56)

```text
Modern Golf & Country Club
├── Golf (golf ops, starter, caddy master, driving range, pro shop)
├── Sport Club (reception, lifeguard, instruktur, gym, spa)
├── F&B (restaurant, bar, kitchen, banquet kitchen)
├── Banquet
├── Bungalow
├── Sales
├── Finance
└── Engineering
```

## 7.6 Usulan Tambahan ke Naming Convention

| Usulan label | Lokasi | Alasan |
|---|---|---|
| Employee Self Service | `ops` (personal login) | NC §29 belum punya interface karyawan ([bagian 6](#6-hasil-pengecekan-dokumen-sumber) #8) |
| Attendance Kiosk | `ops` | Clock-in bersama per lokasi |
| Journeys | CRM | Campaign automation multi-langkah (roadmap §59) |
| Payout Run | HRIS → Caddy, Instructors, Commissions | Pembayaran tenaga mitra & komisi, berbeda dari Payroll karyawan |
| HR Configuration, Payroll Configuration, Attendance Configuration | Settings | Pola NC §33 |
| HR Policies (Leave Policy, Overtime Policy, Attendance Policy, Service Charge Policy) | Settings → Club Policies | NC §25 belum punya policy HR |
| Tournament Series, Order of Merit, Team Formats, Tournament History | Golf → Tournaments | Roadmap §62 |
| Package Profitability | Commercial → Packages | Roadmap §61 |
| Status **Probation**, **Permanent**, **Contract**, **Resigned**, **Terminated** | NC §31 | Status karyawan |
| Status **Expiring**, **Renewed** | NC §31 | Kontrak & sertifikasi |
| Status **Applied**, **Screening**, **Interview**, **Offered**, **Hired**, **Withdrawn** | NC §31 | Recruitment |
| Status **Submitted**, **Approved**, **Rejected**, **Cancelled** | NC §31 | Cuti, izin, lembur (sebagian sudah standar) |
| Status **Present**, **Late**, **Absent**, **On Leave**, **Off** | NC §31 | Attendance |
| Status **Calculated**, **Approved**, **Posted**, **Paid** | NC §31 | Payroll run & payout run |
| Status **Active**, **Paused**, **Completed** | NC §31 | Journey |

---

# 8. Functional Requirements

Format sama dengan PRD P0–P4. Prioritas: **Must** = wajib untuk go-live Release 5, **Should** = sebaiknya ada di P5, boleh bergeser ke awal P6/P7 bila club setuju. Payroll dijalankan penuh di OneClub (Mode A, §5.5).

## EP-01 — Organization & Employee

**User story:** Sebagai HR Admin, saya ingin satu profil karyawan yang memuat organisasi, jabatan, kontrak, dan akun sistemnya, agar data karyawan tidak tersebar.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-HR-01 | **Organization**: department (P0) dengan hierarki, cost center (selaras dimensi P4), posisi/jabatan, grade, atasan langsung; per property | Must |
| FR-HR-02 | **Employee Profile** melanjutkan Employee P0: data pribadi, NIK, NPWP, status PTKP, rekening bank, kontak darurat, tanggal masuk, status (Probation, Contract, Permanent, Resigned, Terminated) | Must |
| FR-HR-03 | Data sensitif (NIK, NPWP, rekening, gaji, kesehatan) dimasking dan hanya terlihat oleh permission HR/payroll (UU PDP, Tech Doc §9.3) | Must |
| FR-HR-04 | **Relasi User** (P0): pembuatan akun & role saat onboarding; `hris.employee_terminated` menonaktifkan user (H7) | Must |
| FR-HR-05 | **Mutasi, promosi, rotasi** dengan tanggal efektif dan riwayat | Must |
| FR-HR-06 | **Offboarding**: resign/termination, checklist (aset P4 dikembalikan, akses dicabut, settlement akhir) | Must |
| FR-HR-07 | **Org chart** per property/department | Should |
| FR-HR-08 | Migrasi Employee P0 ke `hris` tanpa memutus relasi User dan approval yang ada | Must |

**Acceptance criteria:**

- Karyawan yang di-terminate pada tanggal efektif tidak dapat login sejak tanggal tersebut dan namanya hilang dari approver aktif; approval yang tertunda dialihkan ke atasan.

## EP-02 — Employment Contract & Documents

| ID | Requirement | Prioritas |
|---|---|---|
| FR-CTR-01 | **Employment Contract** PKWT (berjangka) dan PKWTT (tetap): tanggal mulai/akhir, jabatan, komponen gaji tetap, masa percobaan | Must |
| FR-CTR-02 | **Perpanjangan & pengangkatan** dengan riwayat; peringatan kontrak berakhir H-30/H-7 ke HR & atasan | Must |
| FR-CTR-03 | Batas jangka & perpanjangan PKWT sesuai regulasi yang berlaku, configurable *(OQ #3)* | Must |
| FR-CTR-04 | **Employee Documents** (KTP, NPWP, ijazah, BPJS, sertifikat, SP) dengan masa berlaku dan akses terbatas | Must |
| FR-CTR-05 | Template surat (perjanjian kerja, surat keterangan, SP) dari data karyawan | Should |

## EP-03 — Recruitment

| ID | Requirement | Prioritas |
|---|---|---|
| FR-RCT-01 | **Job Requisition** dari kepala departemen dengan approval dan headcount | Must |
| FR-RCT-02 | **Candidate & Application**: Applied → Screening → Interview → Offered → Hired / Rejected / Withdrawn | Must |
| FR-RCT-03 | Jadwal & hasil interview, penilaian | Must |
| FR-RCT-04 | **Offer** dengan approval; Hired membuat Employee + kontrak + onboarding checklist | Must |
| FR-RCT-05 | **Halaman karier** publik dan formulir lamaran dengan consent | Should |
| FR-RCT-06 | Retensi data pelamar yang tidak diterima sesuai UU PDP *(OQ #10)* | Must |

## EP-04 — Training & Certification

| ID | Requirement | Prioritas |
|---|---|---|
| FR-TRC-01 | **Certification types** (roadmap §56): Lifeguard, Caddy, Food Handler, dan lain configurable; masa berlaku, lembaga penerbit | Must |
| FR-TRC-02 | **Employee Certification** (juga caddy & instruktur mitra) dengan dokumen dan tanggal kedaluwarsa; peringatan H-60/H-30 | Must |
| FR-TRC-03 | **Penegakan**: posisi/peran yang mewajibkan sertifikasi tidak dapat dijadwalkan (EP-06) atau ditugaskan (caddy P1–P2) bila sertifikasi kedaluwarsa; event `hris.certification_expired` (H7) | Must |
| FR-TRC-04 | **Training**: program, jadwal, peserta, kehadiran, hasil, biaya | Must |
| FR-TRC-05 | Matriks training wajib per jabatan dan kepatuhan per departemen | Should |

**Acceptance criteria:**

- Lifeguard dengan sertifikasi kedaluwarsa tidak dapat ditempatkan pada shift kolam; caddy dengan sertifikasi kedaluwarsa tidak muncul di Caddy Queue untuk assignment.

## EP-05 — Performance Review

| ID | Requirement | Prioritas |
|---|---|---|
| FR-PRF-HR-01 | **Review cycle** (tahunan/semester/probation) dengan template per jabatan | Must |
| FR-PRF-HR-02 | Self assessment, penilaian atasan, kalibrasi HR; skor & catatan | Must |
| FR-PRF-HR-03 | **Data operasional sebagai input** *(usulan)*: kehadiran, rating caddy (P2), NPS/feedback per tim (P3), pencapaian target sales (P3) | Should |
| FR-PRF-HR-04 | Hasil review menjadi dasar kenaikan gaji/bonus (EP-09) dan pengangkatan (EP-02) | Must |

## EP-06 — Shift Scheduling

**User story:** Sebagai Outlet Manager, saya ingin menyusun jadwal shift mingguan dengan cepat dan melihat siapa yang benar-benar hadir, agar outlet tidak kekurangan staf dan lembur terkendali.

Tim (roadmap §57): F&B, Reception, Lifeguard, Starter, dan tim operasional lain.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-SCH-01 | **Shift templates** per departemen (jam mulai/selesai, istirahat, lintas tengah malam) | Must |
| FR-SCH-02 | **Shift Schedule** mingguan/bulanan per lokasi dengan pola berulang dan salin minggu | Must |
| FR-SCH-03 | Validasi: kebutuhan minimal staf per posisi, sertifikasi wajib (EP-04), batas jam kerja & hari istirahat sesuai HR Policies, bentrok dengan cuti | Must |
| FR-SCH-04 | **Kebutuhan staf dari operasional** *(usulan)*: tee time terisi (P1), event & BEO (P3), okupansi kolam/court (P2) ditampilkan sebagai acuan saat menyusun jadwal | Should |
| FR-SCH-05 | **Publish** jadwal ke ESS dan notifikasi perubahan | Must |
| FR-SCH-06 | **Tukar shift** antar karyawan dengan approval | Must |
| FR-SCH-07 | Jadwal tersedia offline di ESS | Should |

## EP-07 — Attendance

| ID | Requirement | Prioritas |
|---|---|---|
| FR-ATT-01 | Metode (roadmap §57): **Fingerprint**, **Face Recognition** (device via bridge agent, EP-25), **Mobile GPS** (ESS dengan geofence lokasi club) | Must |
| FR-ATT-02 | **Attendance Kiosk** di lokasi (device terdaftar) dengan QR/PIN sebagai alternatif non-biometrik | Must |
| FR-ATT-03 | Pencocokan clock-in/out ke shift: Present, Late, Early Leave, Absent, On Leave, Off; toleransi per Attendance Policy | Must |
| FR-ATT-04 | **Koreksi absensi** oleh karyawan/atasan dengan alasan dan approval | Must |
| FR-ATT-05 | Clock-in mobile mencatat koordinat & akurasi; di luar geofence ditolak atau ditandai untuk review | Must |
| FR-ATT-06 | **Biometrik**: template tidak disimpan di OneClub; consent tertulis; alternatif tanpa biometrik (FR-ATT-02) | Must |
| FR-ATT-07 | Clock-in offline (kiosk/ESS) tersinkron idempotent | Must |
| FR-ATT-08 | Opsi clock-in device untuk caddy & instruktur mitra, terhubung ke absensi caddy P1–P2 | Should |

**Acceptance criteria:**

- Clock-in mobile 400 m dari geofence (radius 150 m *(contoh)*) ditandai Out of Area dan masuk antrean review atasan.
- Event clock-in yang terkirim dua kali dari kiosk offline hanya tercatat sekali.

## EP-08 — Leave, Permission & Overtime

| ID | Requirement | Prioritas |
|---|---|---|
| FR-LVE-01 | **Leave types**: cuti tahunan, sakit (dengan surat), melahirkan, menikah, duka, besar, dan lain sesuai regulasi/kebijakan; saldo & akrual configurable *(OQ #3)* | Must |
| FR-LVE-02 | **Leave & Permission request** dari ESS dengan approval berjenjang; saldo berkurang saat disetujui | Must |
| FR-LVE-03 | Kalender cuti tim untuk atasan; cuti bentrok dengan jadwal ditandai | Must |
| FR-OVT-01 | **Overtime request** sebelum atau sesudah (dengan alasan) dengan approval; hanya lembur disetujui yang dibayar | Must |
| FR-OVT-02 | Perhitungan jam lembur dari absensi aktual vs jadwal; batas lembur harian/mingguan per Overtime Policy | Must |
| FR-OVT-03 | **Upah lembur** sesuai rumus regulasi yang dikonfigurasi (pembagi upah per jam, pengali per jam ke-n, hari kerja vs hari libur) | Must |
| FR-OVT-04 | Lembur tanpa approval tampil sebagai exception untuk atasan | Must |

**Acceptance criteria:**

- Upah bulanan Rp5.190.000 dengan pembagi 1/173 *(konfigurasi)* menghasilkan upah per jam Rp30.000; lembur 3 jam di hari kerja dengan pengali 1,5× jam pertama dan 2× jam berikutnya *(konfigurasi)* = Rp45.000 + Rp120.000 = Rp165.000.

## EP-09 — Payroll Engine

**User story:** Sebagai HR Manager, saya ingin menjalankan payroll bulanan dari data absensi, lembur, dan komponen yang sudah ada di OneClub, lalu mengirim slip gaji dan file bank tanpa Excel.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-PAY-01 | **Salary structure**: gaji pokok, tunjangan tetap & tidak tetap, komponen per grade/jabatan, efektif per tanggal | Must |
| FR-PAY-02 | **Payroll Run** per periode & property: tarik absensi, lembur disetujui, cuti tidak dibayar, service charge (EP-11), komisi (EP-12), bonus, potongan (pinjaman, kasbon) | Must |
| FR-PAY-03 | **Proration** untuk karyawan masuk/keluar di tengah periode | Must |
| FR-PAY-04 | **THR** sesuai regulasi yang dikonfigurasi (masa kerja ≥ 12 bulan 1× upah; < 12 bulan proporsional) | Must |
| FR-PAY-05 | **Bonus** (kinerja, tahunan) dari hasil review (EP-05) dengan approval | Must |
| FR-PAY-06 | Status Run: Draft → Calculated → Approved (Finance & HR) → Posted → Paid; run Approved terkunci; koreksi lewat run penyesuaian | Must |
| FR-PAY-07 | **Final settlement** karyawan keluar (gaji berjalan, cuti tersisa, pesangon/kompensasi sesuai regulasi *(OQ #3)*) | Must |
| FR-PAY-08 | Simulasi payroll sebelum approve dan perbandingan dengan periode sebelumnya | Must |

**Acceptance criteria:**

- THR karyawan dengan upah Rp6.000.000 dan masa kerja 6 bulan = 6/12 × Rp6.000.000 = Rp3.000.000.
- Payroll Run yang sudah Approved tidak dapat diubah; perubahan absensi sesudahnya masuk run penyesuaian periode berikutnya.

## EP-10 — PPh 21 & BPJS

| ID | Requirement | Prioritas |
|---|---|---|
| FR-TAX-HR-01 | **PPh 21** karyawan tetap & tidak tetap sesuai metode yang berlaku (mis. tarif efektif bulanan dan perhitungan tahunan di masa terakhir) dengan status PTKP; tabel tarif **configurable & berversi** | Must |
| FR-TAX-HR-02 | **PPh 21 bukan pegawai** untuk caddy & instruktur mitra bila diputuskan (OQ #4) | Must |
| FR-TAX-HR-03 | **BPJS Kesehatan** dan **BPJS Ketenagakerjaan** (JHT, JKK, JKM, JP) dengan porsi perusahaan/karyawan dan batas upah **configurable & berversi** | Must |
| FR-TAX-HR-04 | Bukti potong & laporan untuk pelaporan (format DJP / e-Bupot) dan iuran BPJS (format SIPP/EDABU) *(EP-25)* | Must |
| FR-TAX-HR-05 | Uji kasus kalkulasi diverifikasi konsultan pajak sebelum go-live dan setiap perubahan regulasi | Must |

**Acceptance criteria:**

- Upah Rp6.000.000 dengan tarif BPJS Kesehatan 4% perusahaan & 1% karyawan *(konfigurasi)* menghasilkan Rp240.000 dan Rp60.000; JHT 3,7% & 2% = Rp222.000 dan Rp120.000; JP 2% & 1% = Rp120.000 dan Rp60.000.
- Hasil PPh 21 untuk seluruh uji kasus konsultan sama sampai rupiah.

## EP-11 — Service Charge Distribution

| ID | Requirement | Prioritas |
|---|---|---|
| FR-SVC-01 | Ambil **pool service charge** per periode & property dari P4 (H4) | Must |
| FR-SVC-02 | **Rule distribusi** configurable: porsi per departemen, poin per jabatan/grade, faktor kehadiran, pengecualian (probation, SP) *(OQ #5)* | Must |
| FR-SVC-03 | Simulasi & approval; hasil per karyawan masuk payroll | Must |
| FR-SVC-04 | Total terdistribusi = pool; selisih pembulatan ke akun yang dikonfigurasi; kewajiban service charge di GL P4 menjadi nol setelah dibayar | Must |

**Acceptance criteria:**

- Pool Rp100.000.000 dibagi rata ke 50 karyawan eligible dengan poin sama = Rp2.000.000 per orang; karyawan yang hadir 20 dari 25 hari kerja dengan faktor kehadiran *(konfigurasi)* menerima 80% bagiannya, dan sisa didistribusikan ulang sesuai rule.

## EP-12 — Sales Commission & Bonus Payout

| ID | Requirement | Prioritas |
|---|---|---|
| FR-CMS-HR-01 | Ambil **commission statement** Approved dari P3 (H3) per periode | Must |
| FR-CMS-HR-02 | Bayar lewat payroll; clawback P3 dipotong di periode berikutnya | Must |
| FR-CMS-HR-03 | Status statement P3 berubah menjadi Paid setelah payroll/payout Posted | Must |

## EP-13 — Non-Employee Workforce: Caddy

**User story:** Sebagai Finance Manager, saya ingin caddy fee dan tip dibayar ke caddy mitra lewat statement yang jelas, dengan kewajiban titipan caddy di neraca yang tertutup.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-CDY-01 | **Caddy profile** sebagai tenaga mitra: status kemitraan, rekening, NPWP/NIK, sertifikasi (EP-04), level & rating (P2) | Must |
| FR-CDY-02 | **Caddy Payout Run** per periode settlement P2 (H1): caddy fee, tip non-tunai, potongan sesuai Caddy Policies, pajak mitra (OQ #4) | Must |
| FR-CDY-03 | **Statement** per caddy di Caddy App dan PDF; persetujuan Finance; status Calculated → Approved → Paid | Must |
| FR-CDY-04 | **Payment file** bank massal dan pencatatan pembayaran tunai | Must |
| FR-CDY-05 | Jurnal payout mengurangi **caddy fee liability** P4 (H5) | Must |
| FR-CDY-06 | BPJS untuk caddy bila club memilih (OQ #4) | Should |

**Acceptance criteria:**

- Settlement P2 periode ini Rp6.000.000 + tip non-tunai Rp400.000 untuk satu caddy menghasilkan statement Rp6.400.000 sebelum pajak/potongan; setelah Paid, saldo caddy fee liability caddy tersebut di GL = 0.

## EP-14 — Non-Employee Workforce: Instructor

| ID | Requirement | Prioritas |
|---|---|---|
| FR-INS-HR-01 | **Instructor profile** (karyawan atau mitra), sertifikasi, tarif honor per sesi atau per murid (P2 FR-CLS-08) | Must |
| FR-INS-HR-02 | **Teaching Schedule** dari class schedule P2 dan kehadiran sesi | Must |
| FR-INS-HR-03 | **Instructor Payout** dari honor Approved (H2): instruktur karyawan lewat payroll, instruktur mitra lewat payout run | Must |
| FR-INS-HR-04 | Statement honor di interface Instructor | Must |

**Acceptance criteria:**

- Instruktur mitra dengan 8 sesi × Rp150.000 *(contoh tarif)* menerima statement Rp1.200.000 sebelum pajak; sesi yang dibatalkan tidak dihitung.

## EP-15 — Payroll Accounting, Payment & Payslip

| ID | Requirement | Prioritas |
|---|---|---|
| FR-PPY-01 | **Payroll journal** otomatis lewat posting rule P4 (H5): beban gaji per department/cost center, kewajiban PPh 21, BPJS, service charge, komisi, utang gaji | Must |
| FR-PPY-02 | **Bank payment file** massal per bank; konfirmasi pembayaran memperbarui status Paid dan AP/bank P4 | Must |
| FR-PPY-03 | **Payslip** digital di ESS (PDF, terlindungi) | Must |
| FR-PPY-04 | Laporan payroll per department, per komponen, rekap tahunan per karyawan | Must |
| FR-PPY-05 | Akses data payroll hanya untuk role payroll; review 2 orang untuk perubahan rule payroll (Tech Doc §13) | Must |

## EP-16 — Employee Self Service

| ID | Requirement | Prioritas |
|---|---|---|
| FR-ESS-01 | **Personal login** karyawan (bukan device/PIN shift) di PWA mobile `ops` *(OQ #6)* | Must |
| FR-ESS-02 | **My Schedule**, **Clock In / Out** (GPS), **Attendance History** | Must |
| FR-ESS-03 | **Leave & Permission**, **Overtime** request dan status | Must |
| FR-ESS-04 | **Payslip**; dokumen & sertifikasi saya; training saya | Must |
| FR-ESS-05 | **Manager view**: jadwal tim, approval, kehadiran tim | Must |
| FR-ESS-06 | Pembaruan data pribadi (alamat, rekening, kontak darurat) dengan verifikasi HR | Must |
| FR-ESS-07 | Notifikasi push/WhatsApp untuk jadwal, approval, payslip | Must |

## EP-17 — Advanced Segmentation & VIP

Melanjutkan segmentasi P2–P3 (roadmap §59).

| ID | Requirement | Prioritas |
|---|---|---|
| FR-SEG-01 | **Cross-business behavior**: kombinasi aktivitas lintas lini (golf + F&B + stay + banquet), recency/frequency/monetary per lini | Must |
| FR-SEG-02 | **RFM scoring** dan kelompok (Champions, Loyal, At Risk, Lapsed) *(usulan)* | Must |
| FR-SEG-03 | **VIP segmentation** dari Top Spender (P3), tier, dan kriteria manual; benefit & penanganan khusus (penanda di check-in/POS) | Must |
| FR-SEG-04 | Segmen dinamis dihitung terjadwal di analytics store tanpa membebani database transaksi | Must |
| FR-SEG-05 | Perbandingan segmen dan perpindahan antar segmen per periode | Should |

## EP-18 — Advanced Loyalty

| ID | Requirement | Prioritas |
|---|---|---|
| FR-LOY-P5-01 | **Loyalty tiers** otomatis: kriteria poin/spend/aktivitas per periode, evaluasi berkala, naik/turun dengan masa tenggang | Must |
| FR-LOY-P5-02 | **Tier benefit**: pengali poin, diskon, prioritas booking window, akses event; benefit dibaca pricing/booking lewat interface publik | Must |
| FR-LOY-P5-03 | **Reward catalog** dengan stok, biaya, masa berlaku | Must |
| FR-LOY-P5-04 | **Reward eligibility engine**: aturan berdasarkan tier, segmen, aktivitas, periode, batas per customer | Must |
| FR-LOY-P5-05 | **Top Spender driven engagement**: reward atau undangan otomatis untuk peringkat teratas per periode | Must |
| FR-LOY-P5-06 | Biaya program loyalty (reward, benefit) per periode untuk analitik ROI | Should |

**Acceptance criteria:**

- Member yang mencapai ambang Gold di akhir periode naik tier otomatis, menerima notifikasi, dan pengali poin baru berlaku untuk transaksi berikutnya.

## EP-19 — Campaign & Lifecycle Automation

| ID | Requirement | Prioritas |
|---|---|---|
| FR-JRN-01 | **Journey** multi-langkah: trigger (event, segmen masuk, tanggal), tunggu, kondisi, cabang, aksi (WhatsApp, email, push member app, voucher, poin, tugas ke sales) | Must |
| FR-JRN-02 | **Renewal automation** membership (melanjutkan P2–P3): H-60/H-30/H-7, penawaran perpanjangan, eskalasi ke sales bila belum renew | Must |
| FR-JRN-03 | **Birthday automation** dengan penawaran sesuai tier | Must |
| FR-JRN-04 | Template journey: welcome member baru, win-back customer lapsed, post-event follow-up, abandoned booking *(usulan)* | Must |
| FR-JRN-05 | Consent, frequency cap, quiet hours, dan suppression P3 berlaku di setiap langkah | Must |
| FR-JRN-06 | **A/B test** pesan dan **control group** untuk mengukur dampak | Should |
| FR-JRN-07 | Kinerja per journey & langkah: terkirim, dibaca, klik, konversi, pendapatan teratribusi | Must |

**Acceptance criteria:**

- Member yang membership-nya berakhir dalam 30 hari dan belum renew menerima pesan sesuai urutan; journey berhenti otomatis setelah member membayar renewal.

## EP-20 — CRM & Sales Analytics

| ID | Requirement | Prioritas |
|---|---|---|
| FR-CRA-01 | **NPS analytics**: tren per lini/outlet/periode, driver dari komentar (tag/kategori), korelasi dengan retensi | Must |
| FR-CRA-02 | **Complaint SLA analytics**: kepatuhan SLA, waktu penyelesaian, kategori berulang, eskalasi per lini | Must |
| FR-CRA-03 | **Sales performance**: konversi per tahap/sales/lini, siklus penjualan, win/loss reason, forecast vs aktual | Must |
| FR-CRA-04 | **Sales commission analytics**: komisi vs pendapatan, efektivitas skema | Must |
| FR-CRA-05 | **Customer lifetime value** dan retensi per kohort *(usulan)* | Should |

## EP-21 — Management Dashboard & BI

**User story:** Sebagai General Manager, saya ingin satu dashboard yang menunjukkan kinerja seluruh club dibanding target, dan bisa ditelusuri sampai transaksi, tanpa meminta laporan ke tiap departemen.

KPI per domain (roadmap §60) sudah tersedia sejak P1–P4 ([bagian 6](#6-hasil-pengecekan-dokumen-sumber) #5); P5 menambah lapisan analitik.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-BI-01 | **Analytics store** terpisah dari database transaksi (Tech Doc §7.6: `reporting` kandidat diekstrak), diisi dari read model & event P1–P5 dengan latensi ≤ 15 menit *(usulan)* | Must |
| FR-BI-02 | **Executive Overview** lintas domain: Golf, Sport Club, Membership, Booking, Banquet, Commercial, Inventory, Procurement, Finance, CRM, HR (roadmap §60, §66.5) | Must |
| FR-BI-03 | **Target** per KPI per periode (budget operasional) dan **target vs aktual** dengan indikator | Must |
| FR-BI-04 | **Drill-down** dari KPI ke dimensi (property, lini, outlet, segmen) sampai daftar transaksi sumber | Must |
| FR-BI-05 | **Scheduled reports** (email/WhatsApp PDF/XLSX) harian/mingguan/bulanan per role | Must |
| FR-BI-06 | **Self-service report builder** sederhana (pilih dataset, dimensi, metrik, filter) dengan permission per dataset | Should |
| FR-BI-07 | Definisi KPI terdokumentasi dan konsisten (satu definisi per KPI, label NC §23) | Must |
| FR-BI-08 | Perbandingan periode (MoM, YoY) dan per property | Must |

**Acceptance criteria:**

- Setiap KPI di Executive Overview sama dengan laporan sumbernya untuk tanggal dan property yang sama.
- Drill-down Golf Revenue bulan ini → per hari → per komponen → daftar folio line sumber.

## EP-22 — Advanced Package Management

Melanjutkan Package Engine P3 (roadmap §61).

| ID | Requirement | Prioritas |
|---|---|---|
| FR-PKG-P5-01 | **Multi-business package** dengan komponen bersyarat (pilihan antar komponen, komponen opsional), aturan urutan & jarak waktu antar komponen | Must |
| FR-PKG-P5-02 | **Capacity & time block** paket: kuota paket per tanggal yang memperhitungkan kapasitas setiap komponen; overtime per komponen | Must |
| FR-PKG-P5-03 | **Inventory requirement** paket (BOM, P4) tampil sebelum menjual paket berkuota besar | Must |
| FR-PKG-P5-04 | **Package profitability**: pendapatan teralokasi vs biaya (COGS P4, caddy fee, biaya ruang) per paket & periode | Must |
| FR-PKG-P5-05 | Payment schedule paket mengikuti template per tipe paket | Must |

## EP-23 — Advanced Tournament

Melanjutkan Tournament P3 (roadmap §62; batas sesuai PRD P3 §6 #3).

| ID | Requirement | Prioritas |
|---|---|---|
| FR-TRN-P5-01 | **Format tim**: Scramble, Best Ball / Four-ball, Foursomes, Texas Scramble dengan handicap allowance configurable | Must |
| FR-TRN-P5-02 | **Tournament Series** dan **Order of Merit**: poin per event, standing musim, final | Must |
| FR-TRN-P5-03 | **Multi-round** dengan cut setelah ronde tertentu dan re-pairing berdasarkan posisi | Must |
| FR-TRN-P5-04 | **Tournament History**: arsip hasil, statistik pemain lintas tournament, champion history (melengkapi Hall of Fame) | Must |
| FR-TRN-P5-05 | **Integrasi federasi/asosiasi** untuk handicap resmi dan pelaporan hasil bila API tersedia *(OQ #11)* | Should |
| FR-TRN-P5-06 | **Advanced registration**: kategori (member, tamu, undangan sponsor), kuota per kategori, early-bird fee | Must |

## EP-24 — HR & Workforce Policies

Memakai framework policy P0 (berversi + effective date) dan pola NC §33.

| ID | Requirement | Prioritas |
|---|---|---|
| FR-POL-P5-01 | **Attendance Policy** *(usulan label)*: toleransi terlambat, geofence, metode per lokasi, koreksi | Must |
| FR-POL-P5-02 | **Leave Policy**: jenis, saldo, akrual, carry-over, approval | Must |
| FR-POL-P5-03 | **Overtime Policy**: batas, pengali, approval | Must |
| FR-POL-P5-04 | **Payroll Configuration**: komponen, PTKP, tarif PPh 21, BPJS, THR, pembulatan | Must |
| FR-POL-P5-05 | **Service Charge Policy**: rule distribusi (EP-11) | Must |
| FR-POL-P5-06 | **Caddy Policies** lanjutan (P2): potongan, pajak mitra, jadwal payout | Must |
| FR-POL-P5-07 | Setiap payroll/payout run menyimpan versi policy yang dipakai | Must |

## EP-25 — Integrasi P5

| ID | Requirement | Prioritas |
|---|---|---|
| FR-INT-P5-01 | **Device absensi** fingerprint/face recognition lewat bridge agent (P0 FR-INT-07): tarik/terima event kehadiran, sinkron daftar karyawan; vendor diputuskan (OQ #7) | Must |
| FR-INT-P5-02 | ~~Payroll pihak ketiga (export/API ke Talenta, Gadjian)~~: **tidak dibangun**, payroll penuh di OneClub (keputusan §16 #1) | — |
| FR-INT-P5-03 | **Pajak & BPJS**: file e-Bupot PPh 21 dan iuran BPJS sesuai format resmi | Must |
| FR-INT-P5-04 | **Bank payroll file** per bank club (melanjutkan P4) | Must |
| FR-INT-P5-05 | **Push notification** ESS & member app | Should |
| FR-INT-P5-06 | Template notifikasi P5 (ID/EN): jadwal, approval, payslip, kontrak/sertifikasi kedaluwarsa, journey CRM | Must |

## EP-26 — Operational Interfaces & Apps P5

| ID | Requirement | Prioritas |
|---|---|---|
| FR-OPS-P5-01 | **Employee Self Service** dan **Manager view** di `ops` (§7.2) | Must |
| FR-OPS-P5-02 | **Attendance Kiosk** mode device terdaftar | Must |
| FR-OPS-P5-03 | **Caddy App**: Payout History & Statement | Must |
| FR-OPS-P5-04 | **Instructor**: My Sessions, Honor Statement | Must |
| FR-OPS-P5-05 | ESS dapat dipasang sebagai PWA di ponsel pribadi; data payroll tidak di-cache offline | Must |

## EP-27 — HR KPI & Reports

| ID | Requirement | Prioritas |
|---|---|---|
| FR-RPT-P5-01 | **HR Performance** (roadmap §60): Headcount, Attendance, Overtime, Payroll Cost, Caddy Attendance, Caddy Rating; ditambah Turnover, Certification Compliance *(usulan)* | Must |
| FR-RPT-P5-02 | **Reports** *(usulan daftar)*: Headcount Report, Turnover Report, Contract Expiry Report, Certification Expiry Report, Training Report, Attendance Report, Late & Absence Report, Overtime Report, Leave Balance Report, Shift Coverage Report, Payroll Summary Report, Payroll Cost Report, PPh 21 Report, BPJS Report, Service Charge Distribution Report, Commission Payout Report, Caddy Payout Report, Instructor Payout Report, Performance Review Report | Must |
| FR-RPT-P5-03 | Laporan CRM lanjutan: Journey Performance Report, Loyalty Tier Report, Reward Redemption Report, NPS Analytics Report, Complaint SLA Report, Sales Performance Report; Package Profitability Report; Tournament Series Report | Must |
| FR-RPT-P5-04 | Permission per report; laporan gaji hanya untuk role payroll; export CSV/XLSX/PDF | Must |

## EP-28 — Migrasi Gelombang 5

| ID | Requirement | Prioritas |
|---|---|---|
| FR-MIG-P5-01 | **Karyawan, organisasi, kontrak, dokumen** dari sistem HR/Excel club (Master Data Import P0) | Must |
| FR-MIG-P5-02 | **Saldo cuti** per karyawan pada tanggal cutover | Must |
| FR-MIG-P5-03 | **Struktur gaji**, nomor BPJS, status PTKP, dan **data payroll year-to-date** untuk perhitungan PPh 21 tahunan | Must |
| FR-MIG-P5-04 | **Sertifikasi** karyawan, caddy, instruktur dengan masa berlaku | Must |
| FR-MIG-P5-05 | **Reconciliation**: headcount, saldo cuti, total gaji periode terakhir sistem lama = parallel run OneClub; ditandatangani HR Manager & Finance Manager | Must |
| FR-MIG-P5-06 | **Parallel run payroll** minimal satu periode (dibandingkan dengan perhitungan payroll club saat ini) dan dry run ×2 di Staging | Must |

## EP-29 — Production Readiness (Release 5)

| ID | Requirement | Referensi |
|---|---|---|
| FR-REL-P5-01 | **Load test** k6: clock-in serentak pergantian shift, payroll run seluruh karyawan, journey ke 10.000 customer, query BI pada data 2 tahun | Tech Doc §13 |
| FR-REL-P5-02 | **E2E** Playwright untuk flow §9 | Tech Doc §13 |
| FR-REL-P5-03 | **Regression suite P1–P4** lulus | G9 |
| FR-REL-P5-04 | **Integration test** payroll (uji kasus pajak/BPJS), service charge, payout, dan posting jurnal | Tech Doc §13 |
| FR-REL-P5-05 | **Pen test** ESS (login personal, payslip), kiosk, data gaji, integrasi device | Tech Doc §9 |
| FR-REL-P5-06 | **DPIA / penilaian dampak UU PDP** untuk biometrik, data gaji, dan profiling CRM lanjutan *(usulan)* | Tech Doc §9.3 |
| FR-REL-P5-07 | Build VPS dengan `garble` dan tanpa source map | Tech Doc §9.4 |
| FR-REL-P5-08 | Training per peran (HR, kepala departemen, karyawan ESS, finance payroll, marketing journey) dan **hypercare** satu periode payroll *(usulan)* | — |

---

# 9. End-to-End Flows (Demo Exit P5)

Skenario UAT *(usulan)*.

## 9.1 Hire to First Pay

```text
Job requisition Lifeguard → approval → kandidat → interview → offer → Hired (EP-03)
  → Employee + kontrak PKWT + user ESS + role Lifeguard (EP-01, EP-02)
  → sertifikasi lifeguard diunggah (EP-04) → dijadwalkan di shift kolam (EP-06)
  → clock-in face recognition / GPS (EP-07) → lembur 2 jam disetujui (EP-08)
  → payroll run: proration, lembur, BPJS, PPh 21 (EP-09–10)
  → payslip di ESS → bank file → payroll journal (EP-15)
```

## 9.2 Service Charge & Commission

```text
Pool service charge September (P4) → rule distribusi per departemen & kehadiran (EP-11)
  → approval → masuk payroll → kewajiban service charge GL = 0
Commission statement Approved (P3) → payroll → statement P3 Paid; clawback periode berikutnya (EP-12)
```

## 9.3 Caddy & Instructor Payout

```text
Caddy settlement periode (P2) + tip non-tunai → Caddy Payout Run (EP-13)
  → approval Finance → statement di Caddy App → bank file → caddy fee liability GL = 0
Instructor fee Approved (P2) → payout mitra / payroll karyawan (EP-14)
```

## 9.4 Retention Journey

```text
Segmen "At Risk": member Gold, 60 hari tanpa round (EP-17)
  → journey win-back: WhatsApp penawaran → tunggu 7 hari → bila belum booking: voucher F&B
  → bila booking: poin bonus & catatan sales (EP-19)
  → tier dievaluasi akhir periode (EP-18) → Journey Performance Report (EP-27)
```

## 9.5 Executive Review

```text
GM membuka Executive Overview → Golf Revenue di bawah target 8% (EP-21)
  → drill-down: weekday PM rendah → segmen guest turun → NPS weekday PM turun (EP-20)
  → scheduled report mingguan ke manajemen; target bulan berikutnya diperbarui
```

## 9.6 Tournament Series

```text
Club Championship Series 4 event (EP-23): event 2 Four-ball → poin order of merit
  → final multi-round dengan cut → champion → Tournament History & Hall of Fame
```

---

# 10. Data Entities P5

Sesuai Data Entity Rollout roadmap §70 (P5), ditambah entity pendukung. Schema mengikuti Tech Doc §4.1.

| Entity | Schema | Catatan |
|---|---|---|
| Organization Unit, Position, Grade, Cost Center link | `hris` | Melanjutkan Department P0 |
| Employee (pindahan P0), Employee Bank Account, Emergency Contact, Employment History | `hris` | |
| Employment Contract, Employee Document | `hris` | |
| Job Requisition, Candidate, Application, Interview, Offer, Onboarding Checklist | `hris` | |
| Certification Type, Employee Certification, Training Program, Training Session, Training Attendance | `hris` | Employee Certification, Training (§70) |
| Review Cycle, Performance Review, Review Score | `hris` | Performance Review (§70) |
| Shift Template, Shift Schedule, Shift Assignment, Shift Swap | `hris` | Shift (§70) |
| Attendance Event, Attendance Day, Attendance Correction, Geofence, Attendance Device | `hris` | Attendance (§70) |
| Leave Type, Leave Balance, Leave Request, Permission Request, Overtime Request | `hris` | |
| Salary Structure, Pay Component, Payroll Run, Payroll Line, Payslip, Tax Profile (PTKP), BPJS Profile, Statutory Rate Table | `hris` | Payroll (§70) |
| Service Charge Distribution, Distribution Line | `hris` | Pool dari P4 |
| Payout Run, Payout Line (caddy, instruktur, komisi) | `hris` | Tenaga mitra |
| Journey, Journey Step, Journey Enrollment, Journey Event, Experiment | `crm` | |
| Loyalty Tier (perluasan), Tier Evaluation, Reward Catalog, Reward Eligibility Rule | `crm` | |
| RFM Score, Segment Membership History | `crm` / analytics | |
| Analytics datasets & materialized KPI, KPI Target | `reporting` / analytics store | |
| Package Component Rule, Package Capacity, Package Profitability | `commercial` | Lanjutan P3 |
| Tournament Series, Series Points, Team, Round Cut, Tournament Archive | `golf` | Lanjutan P3 |

---

# 11. API Surface P5

Konvensi Tech Doc §8.1 (`/api/v1/<module>/<resource>`, cursor pagination, RFC 9457, `Idempotency-Key`, `ETag`/`If-Match`).

| Area | Endpoint utama |
|---|---|
| Organization & Employee | `/api/v1/hris/org-units`, `/hris/positions`, `/hris/grades`, `/hris/employees` (+ `:transfer`, `:promote`, `:terminate`), `/hris/employees/{id}/documents` |
| Contract | `/api/v1/hris/contracts` (+ `:renew`, `:make-permanent`, `:end`) |
| Recruitment | `/api/v1/hris/job-requisitions` (+ `:approve`), `/hris/candidates`, `/hris/applications` (+ `:move-stage`, `:offer`, `:hire`, `:reject`), `POST /api/v1/public/careers/applications` |
| Training & Certification | `/api/v1/hris/certification-types`, `/hris/certifications`, `/hris/training-programs`, `/hris/training-sessions` |
| Performance | `/api/v1/hris/review-cycles`, `/hris/reviews` (+ `:submit`, `:calibrate`) |
| Scheduling | `/api/v1/hris/shift-templates`, `/hris/schedules` (+ `:publish`), `/hris/shift-swaps` (+ `:approve`) |
| Attendance | `POST /api/v1/hris/attendance:clock`, `/hris/attendance-days`, `/hris/attendance-corrections` (+ `:approve`), `/hris/geofences`, `/hris/attendance-devices` |
| Leave & Overtime | `/api/v1/hris/leave-types`, `GET /hris/leave-balances`, `/hris/leave-requests` (+ `:approve`, `:reject`, `:cancel`), `/hris/overtime-requests` (+ `:approve`) |
| Payroll | `/api/v1/hris/salary-structures`, `/hris/payroll-runs` (+ `:calculate`, `:approve`, `:post`, `:mark-paid`), `GET /hris/payslips/{id}/pdf`, `/hris/statutory-rates` |
| Service charge & payout | `/api/v1/hris/service-charge-distributions` (+ `:simulate`, `:approve`), `/hris/payout-runs` (+ `:calculate`, `:approve`, `:mark-paid`), `GET /hris/payout-runs/{id}/bank-file` |
| ESS | `GET /api/v1/ess/me`, `/ess/schedule`, `/ess/attendance`, `/ess/leave-requests`, `/ess/overtime-requests`, `/ess/payslips`, `/ess/team` (manager) |
| CRM lanjutan | `/api/v1/crm/journeys` (+ `:activate`, `:pause`), `/crm/journeys/{id}/enrollments`, `/crm/experiments`, `/crm/loyalty/tier-evaluations`, `/crm/loyalty/reward-rules`, `GET /crm/analytics/{nps|sla|sales|rfm|clv}` |
| BI | `GET /api/v1/reporting/executive`, `/reporting/kpi-targets`, `GET /reporting/drilldown`, `/reporting/scheduled-reports`, `/reporting/datasets` (Should) |
| Package & Tournament | `/api/v1/commercial/packages/{id}/capacity`, `GET /commercial/package-profitability`, `/golf/tournament-series`, `GET /golf/tournament-series/{id}/order-of-merit`, `GET /golf/tournament-history` |
| Migration | CLI `oneclub import hris --employees|--contracts|--leave-balances|--payroll-ytd|--certifications` |

Event outbox P5: `hris.employee_hired`, `hris.employee_terminated`, `hris.contract_expiring`, `hris.certification_expired`, `hris.schedule_published`, `hris.attendance_recorded`, `hris.leave_approved`, `hris.overtime_approved`, `hris.payroll_calculated`, `hris.payroll_posted`, `hris.payroll_paid`, `hris.payout_posted`, `hris.payout_paid`, `crm.journey_step_executed`, `crm.tier_evaluated`, `crm.reward_issued`, `golf.series_standing_updated`.

---

# 12. Non-Functional Requirements

Mengacu Tech Doc §14 dan PRD P4 §12. Semua target P1–P4 tetap berlaku.

| Area | Requirement P5 |
|---|---|
| Availability | 99.5% per bulan; clock-in kiosk & ESS tetap berfungsi saat koneksi terputus (offline queue) |
| Performance | Clock-in < 2 detik; payroll run seluruh karyawan instance < 5 menit *(usulan)*; query BI p95 < 5 detik di analytics store |
| Data latency | Analytics store ≤ 15 menit dari transaksi *(usulan)*; executive dashboard menampilkan waktu data terakhir |
| Payroll integrity | Run Approved immutable; total payroll journal = total payroll run; total distribusi service charge = pool; total payout = settlement/fee Approved |
| Compliance | Tabel PPh 21, BPJS, THR, lembur, cuti berversi dengan effective date; uji kasus konsultan per perubahan regulasi |
| Privacy | UU PDP: biometrik tidak disimpan di OneClub; data gaji & NIK/NPWP terenkripsi at-rest *(usulan)* dan dimasking; akses berbasis role & property; DPIA untuk biometrik dan profiling |
| Security | OWASP ASVS Level 2; login personal ESS dengan MFA opsional untuk akses payslip; review 2 orang untuk rule payroll & migration (Tech Doc §13) |
| Retention | Data payroll & pajak ≥ 10 tahun sesuai ketentuan perpajakan *(usulan, OQ #10)*; audit log ≥ 5 tahun (Tech Doc §14) |
| Device | Ponsel pribadi karyawan (Android/iOS, PWA), kiosk tablet, device fingerprint/face lewat bridge agent |
| Regresi | Seluruh suite P1–P4 lulus di setiap merge P5 |
| Bahasa | English (label) + Bahasa Indonesia (helper text, pesan, payslip & dokumen HR ID) |

---

# 13. Exit Criteria & Definition of Done

## 13.1 Exit Criteria Release 5 *(usulan)*

Roadmap tidak mencantumkan exit criteria eksplisit untuk P5. Kriteria berikut diturunkan dari target Release 5 (roadmap §75: *HRIS, payroll, advanced CRM, loyalty, BI*) dan Product Overview §39.

| # | Exit criteria | Bukti |
|---|---|---|
| 1 | Data karyawan & mitra lengkap | Headcount, kontrak, sertifikasi terekonsiliasi dengan data HR club (EP-01–04, EP-28) |
| 2 | Jadwal & absensi berjalan | Seluruh departemen shift memakai jadwal & clock-in selama satu periode; exception tertangani (EP-06–08) |
| 3 | Payroll benar | Parallel run satu periode sama per karyawan dengan perhitungan club saat ini atau selisih terjelaskan; uji kasus PPh 21/BPJS konsultan pajak lulus (EP-09–10, EP-28) |
| 4 | Pembayaran mitra & komisi tertutup | Payout caddy, instruktur, komisi, dan service charge satu periode dibayar; kewajiban terkait di GL = 0 (EP-11–14) |
| 5 | Sertifikasi ditegakkan | 0 penugasan dengan sertifikasi kedaluwarsa selama UAT (EP-04) |
| 6 | Retensi otomatis | Renewal, birthday, dan satu journey win-back berjalan; tier dievaluasi otomatis (EP-18–19) |
| 7 | BI konsisten | Setiap KPI executive = laporan sumber; drill-down sampai transaksi (EP-21) |
| 8 | Paket & tournament lanjutan | Satu paket multi-bisnis dengan profitability dan satu series/format tim berjalan (EP-22–23) |
| 9 | Tanpa regresi Release 1–4 | Suite P1–P4 lulus (G9) |
| 10 | UAT disetujui | Skenario §9 lulus di Staging dan di-sign-off HR Manager, Finance Manager, dan GM |

## 13.2 Definition of Done per Story

Sama dengan PRD P4 §13.2, ditambah:

- Kalkulasi payroll, pajak, BPJS, lembur, THR, service charge, dan payout memiliki unit test tabel uji kasus (termasuk batas & pembulatan).
- Fitur dengan data gaji/biometrik/profiling melewati review privasi.
- Kontrak H1–H7 memiliki contract test.

---

# 14. Milestones & Gelombang Rilis

## 14.1 Milestones *(usulan)*

| Milestone | Isi | Bergantung pada | Hasil yang bisa didemo |
|---|---|---|---|
| **M1 — Core HR** | EP-01–02, EP-04, migrasi Employee P0 → `hris`, kontrak H7 | Release 4 | Profil karyawan, kontrak, sertifikasi & peringatan |
| **M2 — Workforce** | EP-06–08, EP-16 (ESS tanpa payslip), EP-25 device absensi, EP-24 | M1 | Jadwal, clock-in kiosk/GPS, cuti & lembur dengan approval |
| **M3 — Pay** | EP-09–10, EP-15; EP-11–14 (service charge, komisi, caddy, instruktur) | M2, P4 (H4–H5), P2–P3 (H1–H3) | Skenario §9.1–9.3 |
| **M4 — Recruitment & Performance** | EP-03, EP-05 | M1 | Hire flow; review cycle |
| **M5 — Advanced CRM & Loyalty** | EP-17–20 | Release 3–4 data | Skenario §9.4 |
| **M6 — BI** | EP-21, EP-27 | M1–M5 | Skenario §9.5 |
| **M7 — Package & Tournament** | EP-22–23 | Release 3 | Skenario §9.6 |
| **M8 — Migrasi & Release 5** | EP-26, EP-28–29 | M1–M7 | Parallel run payroll, dry run ×2, demo exit §9 |

M4, M5, dan M7 dapat berjalan paralel dengan M2–M3.

## 14.2 Gelombang Rilis Release 5 *(usulan)*

| Gelombang | Isi | Alasan urutan |
|---|---|---|
| R5.1 | Core HR, sertifikasi, shift & absensi, cuti & lembur, ESS | Dasar data untuk payroll; nilai cepat ke operasional |
| R5.2 | Payout mitra (caddy, instruktur), komisi, service charge, payroll | Butuh satu periode absensi bersih; dimulai di awal periode payroll |
| R5.3 | Advanced CRM & Loyalty, BI | Bergantung data lintas domain stabil |
| R5.4 | Advanced Package & Tournament, Recruitment & Performance | Mengikuti kalender tournament & siklus review club |

---

# 15. Risiko & Mitigasi *(usulan)*

| # | Risiko | Dampak | Mitigasi |
|---|---|---|---|
| 1 | Kesalahan perhitungan PPh 21/BPJS/THR | Sanksi pajak, keluhan karyawan | Uji kasus konsultan pajak wajib sebelum go-live, tabel berversi, parallel run payroll |
| 2 | Regulasi ketenagakerjaan/pajak berubah | Kalkulasi usang | Semua tarif configurable & berversi; pemantauan regulasi oleh club/konsultan |
| 3 | Penolakan biometrik atau kegagalan device | Absensi tidak lengkap | Alternatif QR/PIN/GPS, template di device, consent, kiosk cadangan |
| 4 | Karyawan tanpa ponsel pribadi atau enggan memasang ESS | Adopsi ESS rendah | Kiosk bersama, akses ESS lewat kiosk, notifikasi WhatsApp |
| 5 | Status hukum caddy mitra (pajak, BPJS) belum jelas | Payout tertunda | OQ #4 sebelum M3; payout tanpa pajak tidak dirilis tanpa keputusan |
| 6 | Data gaji bocor | Kerugian & sanksi UU PDP | Masking, enkripsi, permission ketat, audit akses, pen test |
| 7 | Journey otomatis mengirim terlalu banyak pesan | Opt-out massal, biaya BSP | Frequency cap, quiet hours, control group, approval journey |
| 8 | Analytics store tidak sinkron | Angka BI berbeda dari laporan | Satu definisi KPI, test konsistensi harian, waktu data ditampilkan |
| 9 | Scope P5 luas (29 epic) dan paralel dengan P6 | Release 5 terlambat | Gelombang §14.2; Should ditunda ke P6/P7 (OQ #12) |
| 10 | Migrasi Employee P0 merusak relasi User/approval | Akses & approval terganggu | Expand → migrate → contract, fasad API P0, test approval sebelum/sesudah |

---

# 16. Open Questions

Status per 4 Oktober 2026 (sumber: `OneClub — Open Questions P3–P5.md`):

- ✅ **Diputuskan**: dijawab langsung oleh club / product, dan sudah diterapkan ke requirement terkait.
- 🅰 **Asumsi**: belum dijawab. Nilai di kolom kanan dipakai sebagai dasar pengembangan sampai dikonfirmasi pemiliknya. Semua angka, tarif, dan aturan configurable di Settings.

| # | Pertanyaan | Dampak | Pemilik | Status | Keputusan / Asumsi |
|---|---|---|---|---|---|
| 1 | **Mode payroll**: payroll penuh di OneClub (Mode A) atau HR operasional + payroll pihak ketiga (Mode B, mis. Talenta/Gadjian)? Penyedia mana yang dipakai saat ini? (PO §39, Tech Doc §15 #6) | EP-09–10, EP-15, EP-25 | Club Management + HR + Finance | ✅ | **Mode A — payroll penuh di OneClub.** Requirement khusus Mode B (export ke Talenta/Gadjian) tidak dibangun. Uji kasus PPh 21/BPJS oleh konsultan pajak dan parallel run minimal 1 periode menjadi wajib |
| 2 | Tech Doc §12.3 perlu ditulis; setuju gelombang 3 = P5 ‖ P6 (§5.4)? | Seluruh P5 | Engineering | 🅰 | **Setuju** gelombang 3 = P5 ‖ P6 |
| 3 | Kebijakan ketenagakerjaan club: PKWT, cuti (jenis, akrual, carry-over), lembur (pembagi, pengali, batas), THR, pesangon; rujukan peraturan perusahaan/PKB | EP-02, EP-08, EP-09 | HR + konsultan hukum | 🅰 | Mengikuti UU Cipta Kerja & PP 35/2021, configurable. **PKWT** maks 5 tahun termasuk perpanjangan; kompensasi akhir PKWT 1× upah per 12 bulan (proporsional). **Probation** hanya untuk PKWTT, maks 3 bulan. **Cuti tahunan** 12 hari setelah 12 bulan kerja; carry-over maks 6 hari, hangus 31 Maret. **Lembur:** upah per jam = 1/173 upah bulanan; hari kerja: jam pertama 1,5×, jam berikutnya 2×; hari libur mengikuti tabel PP 35/2021; batas 4 jam/hari dan 18 jam/minggu. **THR** 1× upah untuk masa kerja ≥ 12 bulan, proporsional untuk ≥ 1 bulan. **Pesangon** sesuai PP 35/2021 |
| 4 | Status caddy dan instruktur mitra: perlakuan PPh 21 bukan pegawai, BPJS, potongan, jadwal payout | EP-13–14 | Club + Finance + konsultan pajak | 🅰 | **Bukan pegawai.** **PPh 21 bukan pegawai:** tarif Pasal 17 × 50% penghasilan bruto. **BPJS Ketenagakerjaan BPU** (JKK & JKM) didaftarkan club, iuran dipotong dari payout. Potongan lain mengikuti Caddy Policies. **Jadwal payout:** caddy dua mingguan (tanggal 15 & akhir bulan), termasuk tip non-tunai; instruktur mitra bulanan. Wajib diverifikasi konsultan pajak |
| 5 | Dasar distribusi service charge (porsi departemen, poin jabatan, faktor kehadiran, pengecualian) | EP-11 | Club Management + HR + Finance | 🅰 | **95%** pool dibagi ke karyawan, **5%** cadangan breakage & loss. Dibagi **rata per karyawan eligible**, dikalikan faktor kehadiran (hari hadir ÷ hari kerja). **Eligible:** karyawan tetap & kontrak yang lulus probation. **Tidak eligible:** probation, pekerja harian, karyawan dengan SP2 ke atas, cuti tanpa upah sepanjang periode. Caddy mitra tidak menerima service charge (sudah ada caddy fee). Dibayar bulanan bersama payroll. |
| 6 | ESS di shell `ops` (personal login) disetujui, atau perlu app terpisah? | EP-16, EP-26 | Product + Engineering | 🅰 | **Di shell `ops`** (PWA) dengan personal login, sesuai usulan PRD |
| 7 | Vendor & tipe device absensi (fingerprint, face recognition), jumlah titik, dan lokasi geofence | EP-07, EP-25 | Club + Engineering | 🅰 | Device **face recognition + fingerprint** (mis. ZKTeco) di **6 titik**: staff entrance clubhouse, caddy house / golf operations, sport club, back of house F&B, banquet, engineering & gudang. **Mobile GPS** untuk staf lapangan (course maintenance, sales) dengan geofence radius 300 m dari area club |
| 8 | Struktur organisasi, jabatan, grade, dan atasan per departemen | EP-01 | HR | 🅰 | **General Manager** membawahi: Golf Operations (Golf Manager), Course Maintenance (Course Superintendent), Sport Club (Sport Club Manager), F&B (F&B Manager, Executive Chef), Sales & Banquet (Sales & Marketing Manager, Banquet Manager), Bungalow (Front Office Manager), Finance & Accounting (Finance Manager), HR (HR Manager), Engineering (Chief Engineer). **Grade:** G1 Staff · G2 Senior Staff · G3 Supervisor · G4 Assistant Manager · G5 Manager · G6 Head of Department · G7 General Manager |
| 9 | Sertifikasi wajib per jabatan dan lembaga penerbitnya (lifeguard, caddy, food handler, lainnya) | EP-04 | HR + Operasional | 🅰 | **Lifeguard:** sertifikat lifeguard + CPR/BLS, berlaku 2 tahun. **Caddy:** sertifikat caddy dari training internal club, berlaku 2 tahun. **Food handler:** sertifikat penjamah makanan (Dinas Kesehatan), berlaku 3 tahun. **Engineering:** lisensi K3 sesuai bidang (listrik, boiler). **Course maintenance:** sertifikat aplikator pestisida. **Instruktur** gym/renang/tenis: sertifikasi cabang olahraga terkait. **Security & sport staff:** first aid |
| 10 | Retensi data pelamar, data payroll, dan data biometrik (UU PDP & perpajakan) | EP-03, §12 | Club + Legal | 🅰 | **Pelamar ditolak:** 1 tahun (dengan consent talent pool), lalu dihapus. **Payroll, pajak, BPJS, dan absensi pendukung:** 10 tahun. **Biometrik:** tidak disimpan di OneClub; template di device dihapus ≤ 30 hari setelah karyawan keluar. **Lead non-customer:** 2 tahun (PRD P3 §16 #19) |
| 11 | Federasi/asosiasi golf yang perlu diintegrasikan (handicap resmi, pelaporan hasil) dan format tim yang dipakai club | EP-23 | Club (Golf) | 🅰 | Federasi: **PGI** (handicap index diinput manual; integrasi API tetap Should). Format tim awal: **Scramble** dan **Four-ball (Best Ball)**; Foursomes & Texas Scramble menyusul lewat konfigurasi |
| 12 | Prioritas jika kapasitas tidak cukup: urutan gelombang R5.1–R5.4 disetujui? Epic Should mana yang boleh bergeser ke P6/P7? | Milestones | Management + Club | 🅰 | Urutan **sesuai PRD P5 §14.2**. Epic Should boleh bergeser ke P6/P7 |
| 13 | Target KPI per periode: siapa yang menetapkan dan dari dokumen anggaran mana? | EP-21 | Management + Finance | 🅰 | Ditetapkan **Finance Manager bersama GM** dari **anggaran tahunan**, dipecah per bulan, disetujui Direksi/Owner, dan diinput ke OneClub setiap awal tahun |
| 14 | Aturan tier & reward lanjutan, benefit per tier, dan anggaran program loyalty | EP-18 | Club + Finance | 🅰 | **Silver** (default), **Gold** (spend ≥ Rp25 jt per 12 bulan), **Platinum** (≥ Rp75 jt). Gold: poin 1,25×, booking window +2 hari, diskon F&B 5%. Platinum: poin 1,5×, booking window +4 hari, diskon F&B 10%, undangan event VIP. Evaluasi tahunan; masa tenggang 3 bulan sebelum turun tier. **Anggaran loyalty** maks 2% net revenue lini peserta |
| 15 | Journey otomatis yang diprioritaskan dan batas frekuensi pesan per customer | EP-19 | Club Marketing | 🅰 | **Prioritas:** (1) renewal membership, (2) birthday, (3) welcome member baru, (4) win-back 60 hari tanpa kunjungan, (5) follow-up pasca event banquet. **Frequency cap:** maks 2 pesan marketing per minggu dan 6 per bulan per customer; **quiet hours** 21.00–08.00. Pesan transaksional tidak dihitung |
| 16 | Persetujuan usulan label dan status baru (§7.6) dan pembaruan NC §22, §25, §29, §31 | IA | Product | 🅰 | **Disetujui** sesuai usulan PRD P5 §7.6 |
