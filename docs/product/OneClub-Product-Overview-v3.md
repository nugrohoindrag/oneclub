# OneClub

> **Versi 3 — diperbarui 3 Oktober 2026.** Versi ini menggabungkan riset produk awal dengan fit-gap terhadap klien pertama, **Modern Golf & Country Club** (Tangerang). Ringkasan perubahan dan sumber ada di [bagian 53](#53-fit-gap-modern-golf--country-club).
>
> Deck pendamping untuk tim: `Modern Golf - System Scope.pptx`.

## 1. Product Overview

**OneClub** adalah platform terintegrasi untuk mengelola customer journey dan operasional Golf & Country Club secara end-to-end, dengan **GolfOne sebagai baseline untuk golf core operations**. Platform mencakup golf operations, membership, booking, customer, caddy, golf cart, playing experience, F&B, payment/folio, sport club, stay & venue, banquet & event, CRM, inventory, procurement, accounting, HRIS, dan management reporting.

Platform dirancang untuk menangani seluruh perjalanan customer:

```text
Landing Page
    ↓
Discovery
    ↓
Booking
    ↓
Payment
    ↓
Check-in
    ↓
Golf / Sport / Bungalow / Meeting / Banquet / Event
    ↓
Transaction
    ↓
CRM
    ↓
Loyalty & Retention
    ↓
Accounting
```

### Operational Source of Truth

Untuk domain golf, platform menjadi operational source of truth untuk:

- Customer
- Membership
- Booking
- Flight & Player
- Tee Operations
- Caddy
- Golf Cart
- Playing Experience
- Digital Scoring
- Restaurant / Food & Beverage
- Payment
- Folio
- Customer Preferences
- Reporting
- Administration

Application surfaces yang terhubung ke **satu core platform** (satu backend, satu database per instance, satu identitas dan ACL):

1. Website / Digital Channel (publik)
2. Customer Mobile Application (Member & Guest App)
3. Staff App: satu aplikasi untuk semua staf. Back Office, Management Dashboard, Operational Interface (termasuk POS), Kitchen Display, Caddy Tablet, Clubhouse Screen dan Platform Administration adalah **area** di dalamnya; area yang terbuka ditentukan oleh role dan permission user. Aplikasi yang sama dibuka di domain `dashboard`, `cashier`, `caddy` dan `kitchen` sesuai perangkatnya (§45).
4. Integration Layer

Konsep **Dedicated Customer Instance** digunakan sebagai baseline platform golf: setiap customer instance memiliki configuration dan feature configuration sendiri, dengan isolation antar instance.

### Primary Customer

- Golf Club
- Golf & Country Club (golf + sport club)
- Golf Resort
- Golf Club dengan bungalow
- Golf Club dengan meeting/event/banquet facility

### Target Klien Pertama

**Modern Golf & Country Club**, Kota Modern, Tangerang (grup Modernland). Saat ini memakai **Rhapsody Golf** (PT Realta Chakradarma). Detail di [bagian 53](#53-fit-gap-modern-golf--country-club).

---

# 2. Product Ecosystem

Selain lini bisnis dan core bersama, platform menggunakan shared operational surfaces:
- Website / Digital Channel
- Customer Mobile Application (Member & Guest App)
- Staff App (Back Office, Management Dashboard, Operational Interface, Kitchen Display, Caddy Tablet, Clubhouse Screen, Platform Administration sebagai area per role; domain dashboard, cashier, caddy, kitchen)
- Integration Layer

Golf core menggunakan **Dedicated Customer Instance** sebagai model baseline; konfigurasi venue, course, capability, feature, locale, currency, timezone, branding, dan integration berada dalam scope customer instance.

```text
                          GOLF & RESORT
                        MANAGEMENT SYSTEM
                               │
   ┌──────────────┬────────────┼─────────────┬──────────────────┐
   │              │            │             │                  │
  GOLF       SPORT CLUB    STAY & VENUE   BANQUET &          (LINI BISNIS)
   │              │            │          WEDDING
 Tee Time      Court        Bungalow         │
 Flight        Class        VIP Suite      Package per pax
 Caddy         Entry        Meeting Room   BEO
 Buggy         Instructor                  Payment Schedule
 Locker
 Driving Range
 Tournament
 Hall of Fame
   │              │            │             │
   └──────────────┴─────┬──────┴─────────────┘
                        │
        ┌───────────────┼────────────────┬──────────────────┐
        │               │                │                  │
    MEMBERSHIP     RESERVATION       COMMERCIAL         BILLING &      (CORE BERSAMA)
   multi-program     ENGINE        POS + BOM            PAYMENT
                                   Pricing Engine      satu folio
                                   Voucher/Prepaid
        └───────────────┴────────┬───────┴──────────────────┘
                                 │
                          CRM & SALES                               (CUSTOMER)
                 Customer 360 · Pipeline · Loyalty · Top Spender
                                 │
        ┌───────────────┬────────┴───────┬──────────────────┐
        │               │                │                  │
    INVENTORY      PROCUREMENT       ACCOUNTING         HRIS &         (BACK OFFICE)
    & BOM                                               PAYROLL
        └───────────────┴────────┬───────┴──────────────────┘
                                 │
                MANAGEMENT & BI  ·  PLATFORM (IAM, notifikasi,       (FONDASI)
                                    integrasi, audit)
```

---

# 3. Core Product Domains

Platform terdiri dari **15 sistem dalam 5 lapis**:

| Lapis | Sistem |
|---|---|
| **Lini bisnis** | 1. Golf Operations · 2. Sport Club & Facility · 3. Stay & Venue · 4. Banquet, MICE & Wedding |
| **Core bersama** | 5. Membership · 6. Reservation Engine · 7. Commercial (POS + BOM, Pricing & Promotion, Voucher & Prepaid) · 8. Billing & Payment |
| **Customer** | 9. CRM & Sales (termasuk Loyalty dan Top Spender) |
| **Back office** | 10. Inventory & BOM · 11. Procurement · 12. Accounting · 13. HRIS & Payroll |
| **Fondasi** | 14. Management & BI · 15. Platform (IAM, master data, notifikasi, integrasi, approval, audit trail) |

Ditambah satu kanal publik: **Landing Page / Digital Channel** (website + online booking + CMS).

Dibanding versi 1 (10 domain):
- **Baru:** Sport Club & Facility, Banquet & Wedding, Voucher & Prepaid, HRIS & Payroll, Reservation Engine dan Billing & Payment sebagai sistem tersendiri
- **Diperluas:** Golf (locker, reciprocal, HIO, night golf, driving range, Hall of Fame), Membership (multi-program, eligibility), Pricing (matriks segmen × tipe hari × time band), POS (BOM/resep)

---

# 4. Prinsip Arsitektur: Core Bersama

Setiap lini bisnis (golf, sport club, stay, banquet) **tidak membangun membership, booking, dan payment sendiri**. Ketiganya dipakai bersama:

```text
                 MEMBERSHIP (satu untuk semua)
        Program: Golf │ Sport Club │ Corporate │ Residence
                            │
                RESERVATION ENGINE (satu mesin)
   Tee Time │ Court │ Class │ Bungalow │ VIP Suite │ Venue
        │        │       │          │          │
      GOLF     SPORT CLUB       STAY & VENUE    BANQUET
       OPS        OPS
        └────────────────┬─────────────────────┘
                 BILLING & PAYMENT (satu folio)
                            │
                        ACCOUNTING
```

Contoh: fungsi sport center dipecah sebagai berikut.

| Fungsi sport center | Masuk ke sistem |
|---|---|
| Member sport (Individual, Family, Residence, Student, Corporate, dst.) | Membership |
| Booking lapangan, kelas, tiket masuk | Reservation Engine |
| Pembayaran, voucher 5x masuk, paket 4x/8x | Billing & Payment + Voucher/Prepaid |
| Lapangan, jadwal kelas, instruktur, absensi, kontrol akses | Sport Club Operations |

Alasan:
- **Guest tetap bisa booking.** Walk-in, futsal publik, dan peserta kursus tidak wajib jadi member.
- **Satu mesin, satu aturan.** Booking dan tarif tidak dibangun dua kali.
- **Hak lintas program.** Contoh: tarif "Guest With Member" di sport club, atau diskon kelas untuk member golf.
- **Satu tagihan.** Golf, futsal, lalu makan di restoran masuk satu folio.

---

# 5. Golf Management

## 5.1 Golf Booking

Golf booking mengatur jadwal bermain dan pemain.

### Features

- Tee Time
- Tee Time Availability
- Golf Booking
- Member Booking
- Non-Member Booking
- Guest Booking
- Group Booking
- Corporate Booking
- Walk-in Booking
- Booking Confirmation
- Reschedule
- Cancellation
- No-show
- Rain check
- Booking History

### Booking Structure

```text
Golf Booking
    ↓
Tee Time
    ↓
Flight
    ↓
Players
    ├── Member
    ├── Guest of Member
    ├── Reciprocal Member
    └── Non-Member
```

---

### GolfOne Core Booking & Operational Baseline

Golf booking menggunakan relationship berikut:

```text
Availability
    ↓
Tee Hold
    ↓
Player Validation
    ↓
Pricing Resolution
    ↓
Pricing Snapshot
    ↓
Payment Policy
    ↓
Booking Confirmation
    ↓
Tee Sheet
```

Struktur golf mendukung:
- Customer / Member / Guest / Non-Member
- Booking
- Flight
- Booking Player
- Player Eligibility
- Player Pricing
- Player Entitlement
- Booking History
- Booking Modification
- Booking Cancellation

Booking golf menjadi container untuk Flight dan Booking Players. Baseline saat ini mendukung maksimal 4 pemain per booking, dengan booking interval, booking window, membership validation, dan player validation yang configurable.

# 6. Tee Time

**Tee Time** adalah slot waktu yang digunakan untuk memulai permainan golf.

Contoh:

```text
07:00 — Available
07:08 — Available
07:16 — Full
07:24 — Available
```

Setiap tee time dapat memiliki satu atau beberapa flight sesuai konfigurasi club.

### Configurable Rules

- Tee time interval
- Maximum players
- Minimum players (contoh: night golf minimal 3 pemain)
- Booking window
- Booking cut-off
- Peak time
- Off-peak time
- Member priority
- Guest availability
- Start tee (hole 1 saja, two-tee start 1 & 10, shotgun start untuk turnamen)

### Tee Sheet Template per Tipe Hari & Sesi

Tee sheet dibuat dari template yang berbeda per tipe hari dan sesi. Contoh dari Modern Golf (Golf Rates, berlaku 1 April 2026):

| Sesi | Weekday | Weekend / PH |
|---|---|---|
| Morning (AM) | 05.30 – 08.10 | 05.37 – 08.11 |
| Afternoon (PM) | 11.20 – 14.00 | 12.02 – 14.00 |
| Night Golf | 16.30 – 20.00 | 16.30 – 20.00 |

Jam mulai yang berbeda tipis antara weekday dan weekend mengindikasikan interval atau pola start yang berbeda; perlu dikonfirmasi ke club.

### Night Golf

- Sesi tersendiri dengan tarif tersendiri
- Minimal 3 pemain per flight
- Butuh status lampu lapangan dan jam operasional malam

---

### Operational Control Baseline

Tee Sheet juga merepresentasikan:
- Readiness
- Caddy Readiness
- Golf Cart Readiness
- Queue Position
- Active Dispatch Queue
- Preserved Queue Position
- Actual Tee-Off
- Round Status

Operational timestamp dipisahkan untuk menjaga operational truth:
- Booking Time
- Check-in Time
- Ready Time
- Actual Tee-Off
- Round Start
- Round Finish

Starter mendukung **Hold, Skip +1, Release**, sedangkan operational hold memiliki reason dan queue re-evaluation.

# 7. Flight

**Flight** adalah kelompok pemain yang bermain bersama.

Contoh:

```text
Tee Time: 07:08

Flight #12
├── Player 1 — Member
├── Player 2 — Member
├── Player 3 — Guest
└── Player 4 — Guest
```

Struktur:

```text
Tee Time
    ↓
Flight
    ↓
Players
```

---

# 8. Caddy Management

Caddy merupakan resource yang dapat dialokasikan ke player/flight.

### Features

- Caddy Master
- Caddy Profile
- Caddy Availability
- Caddy Assignment
- Caddy Rotation
- Caddy Fee
- Caddy Status
- Caddy History
- Caddy Performance / Rating
- Caddy Attendance
- Caddy Fee Settlement (pembayaran fee ke caddy)
- Caddy Tip

Contoh:

```text
Flight #12
├── Player 1 → Caddy A
├── Player 2 → Caddy B
├── Player 3 → Caddy C
└── Player 4 → Caddy D
```

Konfigurasi assignment mengikuti kebijakan masing-masing golf club.

Catatan:
- Caddy umumnya **mitra, bukan karyawan**. Absensi, rotasi, dan pembayaran fee tetap lewat sistem (terhubung ke HRIS, [bagian 39](#39-hris--payroll)).
- Jika caddy fee termasuk di tarif all-in, porsi caddy fee dicatat sebagai **titipan** (liability) untuk caddy, bukan pendapatan club.

---

### GolfOne Caddy Baseline

Caddy juga memiliki lifecycle dan historical capability:
- Caddy Level: Junior / Senior
- Level History
- Promotion Approval
- Current Assignment
- Next Assignment
- Caddy Replacement
- Round History
- Assignment History
- Customer History
- Repeat Customers
- Favorite Count
- Attendance
- Incidents
- Utilization

Promotion menghasilkan level history dan tidak dilakukan otomatis hanya berdasarkan durasi.

# 9. Golf Cart (Buggy) Management

### Features

- Golf Cart Master
- Cart Number
- Cart Type
- Cart Status
- Availability
- Assignment
- Usage History
- Maintenance Status
- Cart Fee
- Buggy sharing rule
- Surcharge buggy tambahan

Contoh:

```text
Flight #12
├── Golf Cart 01
└── Golf Cart 02
```

Aturan Modern Golf: **buggy sharing wajib** (2 pemain per buggy). Buggy tambahan dikenai Rp560.000 per buggy untuk non-member.

---

### GolfOne Cart Operational Baseline

Golf cart menggunakan readiness state:
- READY
- NOT_READY
- IN_USE
- Charging
- Maintenance
- Out of Service

Lifecycle:

```text
Inspection
→ READY
→ Assignment
→ IN_USE
→ Return
→ Post-Operation Inspection
→ AVAILABLE / Maintenance
→ Release Inspection
→ READY
```

Inspection, replacement, usage hours, maintenance release, incident, dan replacement history dipertahankan sebagai operational history.

# 10. Course Master & Handicap

Data lapangan menjadi dasar scorecard, handicap, dan Hall of Fame.

### Features

- Golf Course (mendukung multi-course)
- Hole: nomor, par, jarak, stroke index
- Tee Set: contoh Black, Blue, Red, White
- Distance Markers
- Course guide hole-by-hole (untuk website/app)
- Handicap Index per pemain
- Course rating / slope per tee set

Contoh Modern Golf: 18 hole championship, 6.350 m, desain Peter Thomson. Hole 1 par 5 (478 m), hole 3 par 3 (171 m), dan seterusnya.

---

### GolfOne Course Structure Baseline

Course configuration diperluas dengan:
- Venue
- Course Section
- Playing Route
- Route Hole
- Multiple Route Combination
- Front Nine
- Back Nine
- Championship 18
- A+B / B+C / C+A
- Course Map Asset
- Panorama Asset
- Hazard Coordinate / Polygon
- Point of Interest
- Green Front / Center / Back Point

Booking dan pricing mereferensikan **Playing Route**, sehingga course tidak bergantung pada fixed 9 atau 18 holes.

Course availability dapat diblok berdasarkan:
- Maintenance
- Tournament
- Private Event
- Weather Closure
- Management Hold
- Date/Time Blocking

# 11. Check-in, Starter & Locker

Bagian ini sudah ada di sistem lama (Rhapsody), jadi wajib setara.

### Features

- Check-in pemain (member card / QR / booking code)
- Bag drop & bag storage
- Locker assignment (pria & wanita)
- Starter sheet & panggilan flight
- Marshal & status lapangan (buka/tutup, hole ditutup, cuaca)
- Rain check / kebijakan cuaca

---

# 12. Driving Range

### Features

- Indoor & outdoor range
- Bay assignment
- Penjualan bola per bucket
- **Saldo bola prepaid** dengan masa berlaku (lihat [Voucher & Prepaid](#34-voucher--prepaid))
- Integrasi ball dispenser (bila ada)

Contoh Modern Golf: promo 5.000 bola Rp4.600.000, berlaku 6 bulan sejak pembelian.

---

# 13. Reciprocal Club

Member club mitra bisa bermain dengan tarif khusus, dan sebaliknya.

### Features

- Master reciprocal club (negara, kota, kontak)
- Tarif reciprocal
- Verifikasi member / surat pengantar
- Settlement antar club (bila ada)
- Laporan kunjungan reciprocal

Contoh Modern Golf: 17 club mitra di Indonesia, Malaysia, Singapura, Thailand, dan China.

---

# 14. Hole-in-One

### Features

- Voucher / asuransi Hole-in-One per pemain (termasuk dalam tarif golf Modern Golf)
- Pencatatan kejadian: pemain, hole, tee, tanggal, saksi/caddy
- Proses klaim ke provider asuransi
- Masuk otomatis ke Hall of Fame

---

# 15. Tournament Management

### Features

- Tournament Creation
- Tournament Schedule
- Registration
- Participant Management
- Flighting
- Tee Assignment (termasuk shotgun start)
- Scoring
- Leaderboard
- Tournament Package
- Tournament Fee
- Sponsor
- Prize
- Tournament Report

Tournament diperlakukan sebagai salah satu tipe Event ([bagian 27](#27-event-management)) yang punya modul scoring.

---

# 16. Golf Score

Golf score digunakan untuk mencatat hasil permainan.

### Scorecard

```text
Hole | Par | Player Score
-----|-----|-------------
1    | 4   | 5
2    | 4   | 4
3    | 3   | 3
4    | 5   | 6
```

### Common Terms

| Score | Term |
|---:|---|
| -3 | Albatross |
| -2 | Eagle |
| -1 | Birdie |
| 0 | Par |
| +1 | Bogey |
| +2 | Double Bogey |
| +3 | Triple Bogey |

---

### GolfOne Score Lifecycle Baseline

Digital scorecard mengikuti lifecycle:
- Score Entry
- Score Validation
- Score Finalization
- Authorized Score Correction
- Correction Reason
- Score Audit History

Score yang sudah **FINALIZED** tidak diedit melalui normal editing; perubahan dilakukan melalui authorized correction workflow.

Playing history mencakup:
- Round History
- Score History
- Round Statistics
- Hole Progress
- Hole Duration
- Round Duration

Future playing experience mencakup Course Map, GPS Distance, Panorama, Pace-of-Play, dan Golf Cart GPS Adapter.

# 17. Hall of Fame

Menampilkan prestasi pemain. Data diambil dari scorecard dan tournament.

### Features

- **Hole-in-One wall:** pemain, hole, tee, tanggal, saksi
- **Club Champion** per tahun dan kategori (men, ladies, senior, junior)
- **Course record** per tee set
- Albatross & eagle tercatat otomatis
- Juara turnamen & sejarah club
- Tampil di website, member app, dan layar clubhouse

Tampilan publik memerlukan **persetujuan pemain (opt-in)**.

---

# 18. Sport Club & Facility

Lini bisnis sport club menangani lapangan, tiket masuk, dan fasilitas. Membership, booking, dan pembayarannya memakai core bersama ([bagian 4](#4-prinsip-arsitektur-core-bersama)).

### Fasilitas (contoh Modern Golf)

| Fasilitas | Unit | Pemakaian |
|---|---:|---|
| Tennis indoor (di bawah dome) | 4 | Booking slot |
| Squash | 2 | Booking slot |
| Tenis meja | 2 | Booking slot |
| Badminton (dengan balkon penonton) | 3 | Booking slot |
| Futsal (1 taraflex, 2 rumput sintetis) | 3 | Booking slot, terbuka untuk publik |
| Basket & voli (arena, indoor) | 2 | Booking slot |
| Kolam renang olympic (dewasa & anak) | — | Tiket masuk / member |
| Gym, studio aerobik | — | Tiket masuk / member / kelas |
| Spa, sauna, steam, jacuzzi, massage | — | Tiket masuk / member |

### Features

- Court/Facility Master
- Booking per slot jam dengan **time band** (contoh: 07–16 dan 16–21/22)
- Tipe hari lebih dari dua (contoh: Sen–Kam, Jumat, Sabtu, Minggu/PH)
- Paket sesi 4x / 8x
- Tiket masuk: walk-in guest, guest with member, anak di bawah 12 tahun, paket keluarga
- Voucher 5x masuk
- Kontrol akses ke kolam, gym, spa
- Locker sport club

### Contoh Tarif (Sport Club Rates 2025)

| Item | Weekday | Weekend |
|---|---:|---:|
| Walk In Guest | 185.000 | 255.000 |
| Guest With Member | 145.000 | 210.000 |
| Children Under 12 | 95.000 | 135.000 |
| 5 Time Entry Voucher | 635.000 | 950.000 |
| Family Package (2 dewasa + 3 anak) | 425.000 | 615.000 |

Futsal sintetis Senin–Kamis: Rp175.000 (07–16) dan Rp245.000 (16–21); paket 4x Rp658.000 / Rp920.000.

---

# 19. Classes & Training

### Features

- Program kelas: renang, tenis, aikido, aerobik, dan kelas gym
- Instruktur / pelatih
- Jadwal kelas
- Pendaftaran & biaya registrasi
- Paket 4x / 8x (kuota sesi)
- Absensi & pemakaian kuota
- Harga member vs guest

Contoh: Swimming member registrasi Rp100.000, paket 4x Rp465.000, paket 8x Rp795.000; guest registrasi Rp200.000, paket 4x Rp535.000, paket 8x Rp957.000.

Honor instruktur terhubung ke HRIS.

---

# 20. Membership Management

Membership menjadi salah satu core module dan **mendukung beberapa program dalam satu sistem** (golf, sport club, corporate, residence).

## Features

- Member Registration
- Membership Program (Golf, Sport Club, Corporate)
- Membership Type
- Membership Package
- Membership Period (tahunan, bulanan)
- Membership Status
- Membership Renewal
- Membership Upgrade
- Membership Downgrade
- Family Member
- Corporate Nominee
- Member Card
- Digital Member Card
- Member Privilege
- Guest Privilege (contoh: tarif "Guest With Member")
- Member Statement
- Member Account / Charge (signing bill)
- Membership History

### GolfOne Membership Lifecycle Baseline

Membership juga mencakup lifecycle:

```text
Application
→ Approval
→ Membership Fee
→ Activation
→ Membership Card
→ Active Membership
→ Annual Fee / Renewal
→ Pause / Resume
→ Expiry / Suspension / Cancellation
```

Tambahan capability:
- Membership Application & Approval
- Membership Fee History
- Annual Fee & Due Date
- Postpone / Reactivation / Card Replacement Fee
- Membership Entitlement
- Booking Entitlement
- Member Rate
- Pause Eligibility
- Pause Period
- Benefit Impact
- Validity Adjustment
- Renewal Eligibility
- Expiry Management

### Eligibility Rules

Setiap tipe membership dapat memiliki syarat yang diverifikasi sistem:

- Umur (berbeda per program: senior golf ≥55 tahun, senior sport club >60 tahun, student <24 tahun, junior golf ≤17 tahun, anak <12 tahun)
- Status residen (contoh: penghuni Kota Modern)
- Komposisi keluarga (contoh: 2 dewasa + 3 anak maksimal 21 tahun dan belum menikah)
- Status pelajar (dokumen pendukung)
- Gender (contoh: tarif Ladies)

### Contoh Tipe Membership (Sport Club Rates 2025)

| Tipe | Harga (Rp) | Syarat |
|---|---:|---|
| Individual | 8.400.000 | Umum |
| Individual Residence | 7.350.000 | Residen Kota Modern |
| Couple | 12.600.000 | Pasangan |
| Family | 18.900.000 | 2 dewasa + 3 anak ≤21 tahun, belum menikah |
| Family Residence | 15.750.000 | Residen + syarat family |
| Senior | 5.250.000 | Di atas 60 tahun |
| Student | 4.725.000 | Di bawah 24 tahun |
| Monthly (Student) | 525.000 | Per bulan |
| Corporate Membership | 105.000.000 | Akun perusahaan |
| Bulk Entrance (Corporate) | 110.000 | Per kunjungan |

**Perlu konfirmasi:** apakah membership golf terpisah dari sport club? Tarif main golf "Member" Rp640.000/round tidak menyebut tipe membership mana yang berhak.

---

# 21. Member 360

Semua aktivitas member dikumpulkan dalam satu profile.

```text
Member
├── Membership (semua program)
├── Family
├── Guest
├── Golf Activity
├── Tee Time
├── Caddy
├── Golf Cart
├── Score & Handicap
├── Sport Club Activity
├── Class Enrollment
├── Bungalow Booking
├── Meeting Room Booking
├── Banquet / Event
├── POS Transaction
├── Voucher & Prepaid Balance
├── Payment
└── Loyalty
```

---

# 22. Booking & Reservation (Reservation Engine)

Central Reservation Engine menangani semua jenis booking.

```text
Reservation
├── Golf (tee time)
├── Sport Court
├── Class
├── Bungalow
├── VIP Suite
├── Meeting Room
├── Banquet Venue
└── Event
```

### Golf Booking Operational Baseline

Untuk golf, Reservation Engine menggunakan flow:

```text
Availability
→ Tee Hold
→ Player Validation
→ Pricing Resolution
→ Pricing Snapshot
→ Payment Policy
→ Booking Confirmation
→ Tee Sheet
```

Common control mencakup anti double-booking, booking status, booking history, modification, cancellation, refund, deposit, dan confirmation.

### Model Reservasi

Engine harus mendukung beberapa model:

| Model | Contoh |
|---|---|
| Per slot waktu | Tee time, lapangan futsal, tennis |
| Per malam | Bungalow |
| Per jam / day-use | Bungalow untuk pre-wedding (4 jam) |
| Per blok jam + overtime | VIP Suite (8 jam + Rp500rb/jam) |
| Per paket pax + durasi | Meeting half/full/one day, wedding |
| Per sesi kelas | Kursus renang 4x/8x |

## Common Features

- Availability
- Booking
- Booking Detail
- Customer
- Date & Time
- Rate
- Package
- Deposit
- Payment
- Confirmation
- Reschedule
- Cancellation
- Refund
- Booking History
- Anti double-booking

---

# 23. Non-Member Booking

Non-member dapat melakukan booking tanpa menjadi member.

### Supported Booking

- Golf
- Sport court (contoh: futsal yang terbuka untuk publik)
- Class
- Bungalow
- Meeting Room
- Banquet / Event (melalui inquiry)

### Flow

```text
Landing Page
    ↓
Select Service
    ↓
Select Date / Time
    ↓
Select Availability
    ↓
Guest Information
    ↓
Additional Services
    ↓
Payment
    ↓
Booking Confirmation
```

---

# 24. Bungalow Booking

Bungalow dikelola sebagai **booking inventory**, tanpa masuk ke resort operational management pada fase awal.

### Features

- Bungalow Master
- Bungalow Type
- Bungalow Number
- View (golf, danau, kolam)
- Capacity
- Rate
- Rate Plan (Room Only, Long Stay, dengan/tanpa sarapan)
- Day-use / per jam
- Availability
- Reservation
- Guest Information
- Check-in
- Check-out
- Cancellation
- Reschedule
- Deposit
- Payment
- Booking History
- Hak akses fasilitas untuk tamu menginap

### Status

```text
Available
Reserved
Checked-in
Occupied
Checked-out
```

### Contoh Modern Golf

14 bungalow (1–2 kamar) + 1 VIP.

| Tipe | Room Only (Rp) | Long Stay (Rp) |
|---|---:|---:|
| Birdie | 605.000 | 550.000 |
| Eagle | 770.000 | 715.000 |
| Albatros | 1.045.000 | 935.000 |
| VIP Room | 1.265.000 | 1.155.000 |

---

# 25. VIP Suite

Ruang privat untuk acara kecil, disewa per blok waktu.

### Features

- Sewa per blok jam (contoh: 8 jam)
- Tarif weekday vs weekend (contoh: Rp4 jt / Rp5 jt nett)
- Overtime per jam (contoh: Rp500.000/jam)
- Fasilitas: locker, shower, bar, TV, karaoke, sound system, ruang pijat
- Add-on F&B dari POS

---

# 26. Meeting Room Booking

Meeting room menjadi bagian dari reservation system.

### Features

- Meeting Room Master
- Capacity per layout
- Room Layout
- Room size
- Facilities
- Availability
- Booking
- Event Schedule
- Rate
- Package (per pax, per durasi)
- Additional room rental per jam
- Equipment
- Catering
- Deposit
- Payment

### Example

```text
Meeting Room A
Capacity: 50

Layout:
- Classroom
- U-Shape
- Theater
- Boardroom

Facilities:
- Projector
- Screen
- Sound System
- Microphone
- Wi-Fi
```

### Contoh Modern Golf

| Ruang | Round Table | Classroom | U-Shape | Theatre | Ukuran (m) |
|---|---:|---:|---:|---:|---|
| Sapphire | 30 | 30 | 30 | 50 | 11,5 × 10,5 |
| Emerald | 40 | 50 | 30 | 75 | 9,6 × 11,6 |
| Jade | 120 | 120 | 70 | 150 | 16 × 20,5 |
| Ruby | 80 | 90 | 50 | 150 | 8 × 36 |
| Ballroom | 150 | 140 | 70 | 400/500 | 8 × 72 |

| Paket | Harga per pax | Durasi ruang | Coffee break |
|---|---:|---|---|
| Half Day Meeting | Rp298.000++ | 5 jam | 1x |
| Full Day Meeting | Rp410.000++ | 9 jam | 2x |
| One Day Meeting | Rp570.000++ | 12 jam | 2x |

Minimal 30 pax, tambahan sewa ruang Rp2.200.000++/jam, dikenai pajak & service 15,5%.

---

# 27. Event Management

Event Management berbeda dari sekadar Meeting Room Booking.

Event dapat menggunakan berbagai fasilitas sekaligus.

### Event Types

- Corporate Gathering
- Golf Tournament
- Wedding
- Birthday / Kids Birthday
- Family Gathering
- Company Outing
- Seminar
- Meeting
- Community Event
- Social Event

### Features

- Event Creation
- Event Type
- Event Schedule
- Venue
- Participant
- Guest Registration
- Package
- Catering
- Equipment
- Vendor
- Event Checklist
- Event Payment
- Event Report

### Event Structure

```text
Event
├── Venue
├── Schedule
├── Participants
├── Package
├── Catering
├── Equipment
├── Bungalow
├── Golf Activity
└── Billing
```

---

# 28. Banquet, MICE & Wedding

Banquet adalah lini bisnis besar dan butuh alur lebih detail dari Event umum.

### Flow

```text
Inquiry
  ↓
Quotation
  ↓
Paket & pilihan menu
  ↓
BEO (Banquet Event Order) / function sheet
  ↓
DP (contoh: minimal 30%)
  ↓
Pelunasan (contoh: maksimal H-7)
  ↓
Technical meeting & food tasting
  ↓
Event
  ↓
Final billing
```

### Features

- Paket berbasis pax (harga per pax ++ dengan minimum pax, atau harga paket nett)
- Pilihan menu dengan **kuota per kategori** (contoh: 2 appetizer, rice, soup, noodle/pasta, chicken, fish, vegetable, 2 dessert)
- Tambahan buffet per pax (contoh: Rp190.000++/pax)
- **Corkage fee** untuk makanan/minuman dari luar
- Add-on venue outdoor dengan minimum pax (contoh: garden & lake view Rp15 jt, pool side Rp10 jt, minimal 100 pax)
- Bundling resource: ballroom, family room, 1 malam bungalow, golf cart, voucher F&B
- Jatah listrik (watt) per paket
- Termin pembayaran & pengingat otomatis
- **BOM per pax** → kebutuhan bahan → procurement
- Pipeline penjualan di CRM

### Contoh Paket Modern Golf

| Paket | Harga | Isi utama |
|---|---|---|
| Wedding | Rp88 jt nett | Buffet 300 pax + VIP family 20 pax, 2 food stall, ballroom 5 jam, 1 malam bungalow suite |
| Intimate Wedding | Rp28 jt nett | Buffet 100 pax, Jade Room 5 jam, 1 malam bungalow suite |
| Pre-wedding | Rp3,8 jt nett | Venue lapangan & danau 4 jam, bungalow 4 jam, golf cart, voucher F&B Rp500rb |
| Birthday | Rp18 jt nett | Buffet 100 pax, venue indoor/pool side 4 jam |
| Kids Birthday | Rp14,5 jt nett | 80 pax, kids buffet & stall, venue 4 jam |
| Social Event | Rp190rb++/pax | Minimal 30 pax, buffet, venue 4 jam |

---

# 29. CRM & Sales

CRM mengelola seluruh customer, member, guest, lead, dan interaction.

## Customer 360

```text
Customer
├── Profile
├── Relasi (keluarga, nominee corporate, tamu member)
├── Membership
├── Golf Activity
├── Golf Booking
├── Sport Club Activity
├── Bungalow Booking
├── Meeting Booking
├── Banquet / Event
├── POS Transaction
├── Voucher & Prepaid
├── Payment
├── Feedback
├── Complaint
├── Campaign
└── Loyalty
```

### Features

- Customer Profile
- Lead Management
- Sumber lead: WhatsApp (beberapa nomor sales), Instagram/Facebook/TikTok, form website, walk-in, referral member
- Sales Pipeline per lini: Wedding, MICE/Meeting, Corporate Membership, Sport Membership, Corporate Golf/Tournament
- Lead assignment ke sales
- Quotation → konversi ke booking/banquet
- Target & komisi sales (terhubung ke HRIS)
- Corporate Account
- Customer Segmentation (tipe member, frekuensi, nilai belanja, usia, residen)
- Customer History
- Interaction History
- Campaign (WhatsApp blast, email)
- Promotion
- Voucher
- Loyalty (poin lintas outlet)
- Renewal & birthday reminder
- Feedback / NPS
- Complaint ticketing dengan SLA & eskalasi
- Follow-up
- Marketing Campaign

## Top Spender & Leaderboard

- Ranking customer/member berdasarkan nilai belanja per bulan, kuartal, tahun
- Lintas lini: golf, F&B, sport, bungalow, banquet
- Filter: member, corporate, outlet, segmen
- Leaderboard lain: rounds terbanyak, member paling aktif
- Dasar tier loyalty, reward, dan undangan VIP
- Sumber data: folio/billing
- **Hanya untuk internal** (sales & manajemen)

---

# 30. Commercial / POS

Commercial layer menangani transaksi customer.

## POS

Possible outlets:

- Restaurant (contoh: The Spike Bar Restaurant)
- Pool dining
- Clubhouse
- Bar
- Pro Shop
- Driving range counter
- Sport club reception
- Other Outlet

### Features

- Product
- Menu
- Modifier & varian
- Pricing
- Discount
- Promotion
- Order
- Kitchen Order / Kitchen Display (KDS)
- Split bill
- Charge ke akun member
- Payment
- Refund
- Shift
- Cashier
- Outlet
- Mode offline + sync

### GolfOne F&B Experience Baseline

F&B juga mendukung:
- Restaurant Reservation
- Pre-Order
- On-Course Order
- Order Source
- Kitchen State
- Ready State
- Serving / Delivery
- Serving Destination
- Favorite Food / Drinks
- Most Ordered
- Repeat Order
- Customer Activity
- Folio Integration

## BOM / Recipe

POS memiliki BOM, sehingga penjualan langsung memotong stok bahan baku.

- Resep per menu (bahan, jumlah, satuan)
- Sub-resep / semi-finished (sambal, kaldu, saus)
- Konversi UOM (beli per kg/karton, stok per gram/pcs, pakai per porsi)
- Yield & waste
- Modifier yang mengubah pemakaian bahan
- Menu paket/combo
- Pemotongan stok otomatis per outlet saat transaksi
- Food cost / COGS per menu
- Theoretical vs actual consumption
- Production order & store requisition dapur

BOM juga berlaku untuk **jasa dan paket**, bukan hanya menu:

| Penjualan | Barang terpakai |
|---|---|
| Tarif golf | 1 botol air mineral, voucher HIO |
| Paket meeting | Coffee break, buffet, note pad & pensil, permen, air mineral |
| Paket wedding/birthday | Buffet per pax, food stall, free-flow minuman |
| Pre-wedding | Cool box package, cold towel |

Master resep berada di Inventory dan dipakai bersama oleh POS, Banquet, dan Package.

---

# 31. Pricing & Promotion Engine

Satu matriks tarif yang dipakai semua lini.

### Dimensi Tarif

- **Segmen:** member, guest, guest of member, non-member, reciprocal, senior, ladies, junior, student, anak, residen, corporate
- **Tipe hari:** dapat lebih dari dua (contoh: Sen–Kam, Jumat, Sabtu, Minggu/PH; atau Mon–Fri vs Sat–Sun/PH)
- **Sesi / time band:** AM, PM, Night; atau 07–16 vs 16–21
- **Durasi / kuantitas:** per jam, per blok, per pax, per sesi, paket 4x/8x
- **Periode berlaku:** effective date (contoh: Golf Rates berlaku 1 April 2026)

### Contoh Matriks Tarif Golf (berlaku 1 April 2026)

| Segmen | Hari | Sesi | Tarif (Rp) |
|---|---|---|---:|
| Member | Setiap hari | AM/PM/Night | 640.000 |
| Guest | Sen–Jum | AM/PM | 995.000 |
| Guest | Sab–Min/PH | AM | 2.960.000 |
| Guest | Sab–Min/PH | PM | 1.960.000 |
| Guest | Setiap hari | Night | 1.050.000 |
| Senior ≥55, Ladies, Junior ≤17 | Sen–Jum | AM/PM | 740.000 |

### Harga Bundling (All-in)

Tarif golf di atas sudah termasuk green fee, caddy fee, buggy fee, voucher Hole-in-One, air mineral, dan pajak 11%. Sistem harus bisa **memecah satu harga menjadi komponen pendapatan**:

```text
Tarif all-in
├── Green fee           → pendapatan golf
├── Caddy fee           → titipan caddy (liability)
├── Buggy fee           → pendapatan buggy
├── Voucher HIO         → biaya asuransi HIO
├── Pajak 11%           → utang pajak
└── Air mineral         → pemotongan stok (BOM)
```

Nominal tiap komponen perlu dikonfirmasi ke club.

### Pajak & Service

- Flag harga **nett** (sudah termasuk pajak & service) vs **++** (belum)
- Komponen pajak & service charge configurable (contoh: 11% di golf, 15,5% tax & service di meeting/banquet)

### Promotion Rules

- Promo berdasarkan jam (happy hour) dan tipe hari (contoh: promo minuman "Buy 2 Only 200K" dengan jam berlaku berbeda weekday vs weekend)
- Beli N harga X, bundling
- Diskon member, voucher, kode promo
- Periode promo

---

# 32. Package Management

Package menggabungkan beberapa service.

### Golf Package

```text
Golf Day Package
├── Tee Time
├── Caddy
├── Golf Cart
└── Lunch
```

### Stay & Golf

```text
Weekend Golf Package
├── Bungalow
├── Tee Time
├── Caddy
└── Breakfast
```

### Corporate Event

```text
Corporate Package
├── Meeting Room
├── Bungalow
├── Golf
└── Catering
```

### Wedding (contoh Modern Golf)

```text
Wedding Package
├── Ballroom 5 jam
├── Buffet 300 pax + VIP family 20 pax
├── 2 food stall
├── Family room
├── 1 malam bungalow suite
└── Food tasting & technical meeting
```

---

# 33. Billing & Payment

Seluruh transaksi terhubung dengan billing dalam **satu folio per customer**.

```text
Golf
Sport Club
Class
Bungalow
VIP Suite
Meeting Room
Banquet
Caddy
Golf Cart
Restaurant
Pro Shop
Driving Range
Membership
Event
Package
Voucher
    ↓
Customer Account
    ↓
Invoice / Folio
    ↓
Payment
    ↓
Accounting
```

### Features

- Folio per customer lintas lini
- Member account / signing bill dengan statement bulanan
- Split bill
- Deposit
- DP & termin pembayaran (contoh: DP 30%, pelunasan H-7)
- Refund
- Shift kasir & end-of-day / night audit
- Corporate billing (AR ke perusahaan)

### Billing Workspace (Revenue & Billing → Billing)

Billing adalah **ruang kerja aksi**, bukan laporan: Invoices mengelola siklus dokumen invoice, Accounts Receivable mengelola piutang dan pembayaran.

```text
Transaksi Golf / Resort / Event / F&B → Folio → BILLING WORKSPACE
   Pending Billing → (Prepare Billing) → Ready to Invoice → Generate → Invoice → AR → Payment → GL
                         ↘ Exception → Resolve ↗
```

- Ringkasan per bisnis (Golf, Resort, Events & Meetings, Membership, Sport Club, F&B & Retail): Total Billable, Pending, Ready, Exceptions — kartu membuka daftar terfilter
- Tab **Pending Billing · Ready to Invoice · Exceptions · All**; baris yang sudah ter-invoice hanya menautkan ke Invoices
- Setiap baris menjawab *"apa aksi berikutnya"*: Review + Prepare Billing, Review + Generate Invoice, Resolve + View Details, View Invoice, dan menu ••• (folio, sumber transaksi, customer, riwayat, catatan, split, owner)
- **Exception otomatis** (hilang sendiri setelah data diperbaiki), dengan severity: Missing Customer, Missing Billing Entity, Missing Billing Address, Missing Tax Information, Invalid Tax Configuration, Pricing Mismatch, Unresolved Folio, Duplicate Charge, Missing Supporting Document, Invalid Payment Term, Revenue Allocation Error, Approval Required. High: perbaiki data atau override Finance Manager; Medium: acknowledge dengan alasan
- **Prepare Billing**: bill-to (customer/perusahaan), alamat, NPWP, payment term, PO/kontrak/referensi, catatan, dokumen pendukung, checklist validasi, perhitungan (gross − deposit − pembayaran − diskon + service + pajak = amount to invoice), Save Billing dan Mark Ready to Invoice
- **Adjustment** sebelum invoice (qty, harga, diskon, service charge, pajak, alokasi revenue): baris koreksi tertaut ke charge asli, mencatat nilai lama/baru, alasan, referensi, user dan waktu; adjustment accountant menunggu approval Finance Manager
- **Generate Invoice** satu atau batch ("10 bisa di-invoice · 2 perlu perhatian"); record dengan exception blocking tidak pernah di-invoice. Invoice mendapat nomor, diposting ke AR & GL, tertaut ke folio, tampil di Customer Portal dengan PDF dan dikirim ke customer
- **Konsolidasi** beberapa folio satu payer menjadi satu invoice (Finance Manager) dan **Split Billing** ke pihak lain dengan folio asli tetap utuh
- **Approval berjenjang** (kebijakan *Billing validation & approval*): Finance Manager di atas ambang, General Manager / Director di atas ambang eksekutif
- Owner per record, catatan, export, dan riwayat lengkap dari charge sampai invoice, kirim, void, dan credit note

### GolfOne Financial Evidence Baseline

Financial transaction harus mempertahankan evidence:
- Pricing Snapshot
- Payment
- Refund
- Adjustment
- Membership Charge
- Folio

Folio dapat berasal dari customer, booking, round/player, dan F&B serta ditutup melalui final settlement.

### Payment Methods

- Cash
- Bank Transfer
- Virtual Account
- QRIS
- Card
- Payment Gateway
- Member Account / Charge
- Voucher & Prepaid Balance

---

# 34. Voucher & Prepaid

### Jenis

- Saldo bola driving range (contoh: 5.000 bola, berlaku 6 bulan)
- Voucher masuk (contoh: 5x entry sport club)
- Paket sesi (4x/8x kelas atau lapangan)
- Voucher F&B (contoh: Rp500rb dalam paket pre-wedding)
- Voucher Hole-in-One
- Gift voucher / promo voucher

### Features

- Penerbitan, penjualan, dan penukaran
- Masa berlaku & sisa saldo/kuota per customer
- Transfer / non-transferable
- Notifikasi menjelang kedaluwarsa
- Accounting: **deferred revenue** saat dijual, pengakuan saat dipakai, **breakage** untuk sisa kedaluwarsa

---

# 35. Inventory Management

Inventory digunakan untuk kebutuhan:

- F&B
- Golf
- Pro Shop
- Sport Club
- Bungalow
- Event / Banquet
- Maintenance
- General Supplies

### Features

- Item Master
- Item Category
- UOM & konversi
- Barcode
- Warehouse
- Stock Location
- Stock Balance
- Stock Movement
- Store Requisition (outlet → main store)
- Stock Transfer
- Stock Adjustment
- Stock Opname
- Stock Valuation (average / FIFO)
- Minimum Stock / Par Stock per outlet
- Reorder Point → PR otomatis
- Batch / Serial Number
- Expiry (F&B)
- Waste / spoilage
- BOM / Recipe & production ([bagian 30](#30-commercial--pos))
- Konsinyasi pro shop (perlu dikonfirmasi)
- Jurnal persediaan & HPP otomatis

### Inventory Structure (contoh Modern Golf)

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

### Asset & Equipment (sub-modul)

- Armada buggy, alat gym, peralatan sewa (stik, raket)
- Jadwal maintenance & riwayat pemakaian

---

# 36. Procurement Management

Procurement terintegrasi dengan inventory dan accounting.

### Procurement Flow

```text
Purchase Requisition
        ↓
Approval
        ↓
RFQ
        ↓
Vendor Quotation
        ↓
Purchase Order
        ↓
Goods Receipt
        ↓
Invoice
        ↓
Accounting
```

### Features

- Supplier Master
- Purchase Requisition (manual, dari reorder point, dan dari kebutuhan bahan banquet)
- Approval
- RFQ
- Vendor Quotation
- Purchase Order
- Goods Receipt
- Purchase Return
- Vendor Invoice
- Vendor Performance
- Procurement Report

### 3-Way Matching

```text
Purchase Order
      +
Goods Receipt
      +
Vendor Invoice
      ↓
3-Way Matching
      ↓
Accounting
```

---

# 37. Accounting

Accounting menjadi financial backbone.

## General Accounting

- Chart of Accounts
- Journal
- General Ledger
- Trial Balance
- Financial Period
- Closing

## Accounts Receivable

- Customer Invoice
- Member Billing
- Corporate Billing
- Receivable
- Payment
- Outstanding
- Aging

## Accounts Payable

- Vendor Invoice
- Payable
- Payment
- Aging

## Cash & Bank

- Cash Account
- Bank Account
- Bank Transaction
- Bank Reconciliation

## Revenue & Tax

- **Revenue allocation** dari harga bundling (green fee, caddy fee, buggy fee, HIO, pajak)
- **Deferred revenue:** membership fee, voucher & prepaid, deposit/DP
- Breakage voucher kedaluwarsa
- Pajak configurable (11% golf, tax & service 15,5% banquet/meeting)
- Titipan caddy fee
- Distribusi service charge ke karyawan (terhubung HRIS)
- e-Faktur (Coretax)

## Financial Reports

- Profit & Loss
- Balance Sheet
- Cash Flow
- Trial Balance
- AR Aging
- AP Aging
- Revenue per lini bisnis

---

# 38. Landing Page / Digital Channel

Landing Page menjadi public-facing layer untuk acquisition dan booking.

### Public Pages

- Home
- Golf (course, hole by hole, handicap index, facilities, reciprocal)
- Sport Club
- Bungalow
- VIP Suite
- Meeting Room / MICE
- Wedding & Banquet
- Event
- Membership
- Package
- Promotion / What's On
- Hall of Fame
- News / Article
- Gallery
- Contact
- Location

### Online Booking

```text
Landing Page
      ↓
Golf / Sport / Bungalow / Meeting / Event
      ↓
Booking Engine
      ↓
Payment
      ↓
Confirmation
```

Inquiry wedding & banquet masuk ke CRM sebagai lead.

### CMS

Admin dapat mengelola:

- Page Content
- Banner
- Image
- Package
- Promotion (dengan periode berlaku)
- Pricing (**tabel tarif terstruktur dari Pricing Engine**, bukan gambar flyer)
- Event
- News
- Gallery
- Course guide
- Contact Information
- Konten bilingual (ID/EN)

Catatan: website Modern Golf saat ini menampilkan tarif sebagai gambar flyer dan banyak yang usang (paket meeting 2022, copyright 2022).

---

# 39. HRIS & Payroll

Sebelumnya out of scope, kini masuk scope.

### Core HR

- Struktur organisasi per departemen (Golf, Sport Club, F&B, Banquet, Bungalow, Sales, Finance, Engineering)
- Data karyawan, kontrak (PKWT/PKWTT), dokumen
- Rekrutmen, training, sertifikasi (lifeguard, caddy, food handler), penilaian kinerja
- Employee self-service (mobile): slip gaji, cuti, absensi

### Waktu & Kehadiran

- Absensi (fingerprint, face recognition, mobile GPS)
- Jadwal shift (outlet F&B, resepsionis, lifeguard, starter)
- Lembur, cuti, izin

### Payroll

- Gaji, tunjangan, lembur
- PPh 21, BPJS Kesehatan & Ketenagakerjaan, THR
- Distribusi service charge
- Komisi sales & bonus

### Tenaga Non-Karyawan

- **Caddy (mitra):** absensi, rotasi, sertifikasi, rating, pembayaran caddy fee & tip
- **Instruktur / pelatih:** jadwal mengajar, honor per sesi atau per murid

**Keputusan terbuka:** HRIS penuh sampai payroll, atau operasional saja (absensi, shift, caddy, instruktur, service charge) dengan payroll memakai software lokal (contoh: Talenta, Gadjian).

---

# 40. Club Rules & Policy Configuration

Karena setiap golf club dapat memiliki aturan operasional berbeda, policy harus configurable.

### Golf Policy

- Tee Time Interval
- Tee Sheet Template per tipe hari & sesi
- Maximum Players
- Minimum Players (termasuk night golf)
- Booking Window
- Booking Cut-off
- Member Priority
- Guest Policy
- Reciprocal Policy
- Caddy Policy
- Golf Cart Policy (buggy sharing, surcharge)
- Cancellation Policy
- No-show Policy
- Rain Check / Weather Policy
- Refund Policy
- Dress Code
- Club Rules

### Sport Club Policy

- Jam operasional per fasilitas
- Aturan tiket masuk & tamu member
- Batas umur anak
- Kebijakan kelas (kuota, pembatalan, masa berlaku paket)

### Banquet Policy

- DP minimum & jatuh tempo pelunasan
- Corkage fee
- Minimum pax per venue / paket

### Pricing Rules

- Member Rate
- Guest Rate
- Guest of Member Rate
- Non-Member Rate
- Reciprocal Rate
- Senior / Ladies / Junior / Student / Child Rate
- Residence Rate
- Corporate Rate
- Weekday Rate
- Weekend Rate
- Tipe hari custom (Sen–Kam, Jumat, Sabtu, Minggu/PH)
- Time band rate (AM, PM, Night, 07–16, 16–21)
- Peak Rate
- Off-Peak Rate
- Holiday Rate
- Package Rate
- Nett vs ++ (tax-inclusive / exclusive)

Detail di [Pricing & Promotion Engine](#31-pricing--promotion-engine).

---

# 41. Customer Journey

## Member

```text
Member
  ↓
Login
  ↓
View Availability
  ↓
Book Golf
  ↓
Select Tee Time
  ↓
Select Flight / Players
  ↓
Caddy / Cart
  ↓
Payment / Member Charge
  ↓
Booking Confirmation
  ↓
Check-in / Bag Drop / Locker
  ↓
Golf
  ↓
Score
  ↓
Transaction History
```

## Non-Member

```text
Landing Page
  ↓
Select Golf
  ↓
Select Date
  ↓
Select Tee Time
  ↓
Enter Guest Data
  ↓
Caddy / Cart
  ↓
Payment
  ↓
Confirmation
  ↓
Check-in
  ↓
Golf
```

## Sport Club (Guest)

```text
Landing Page / Reception
  ↓
Select Court / Class / Entry
  ↓
Select Date & Time Band
  ↓
Guest Data
  ↓
Payment (atau pakai voucher / paket)
  ↓
Confirmation
  ↓
Check-in / Akses fasilitas
```

## Corporate Event

```text
Lead
  ↓
CRM
  ↓
Event Inquiry
  ↓
Quotation
  ↓
Package Selection
  ↓
Meeting Room
  +
Bungalow
  +
Golf
  +
Catering
  ↓
Booking
  ↓
Deposit
  ↓
Event
  ↓
Final Billing
  ↓
Accounting
```

## Wedding

```text
Inquiry (WhatsApp / website)
  ↓
CRM — assign ke sales wedding
  ↓
Quotation & site visit
  ↓
Paket & menu
  ↓
DP 30%
  ↓
Food tasting & technical meeting
  ↓
BEO
  ↓
Pelunasan H-7
  ↓
Event
  ↓
Final Billing
  ↓
Accounting
```

---

# 42. Core Business Flow

```text
                    CUSTOMER
                       │
                       ▼
                     CRM
                       │
                       ▼
          MEMBERSHIP + RESERVATION ENGINE
                       │
     ┌─────────┬───────┼─────────┬───────────┐
     ▼         ▼       ▼         ▼           ▼
   GOLF     SPORT   BUNGALOW  MEETING/    BANQUET/
            CLUB    VIP SUITE  EVENT      WEDDING
     │         │       │         │           │
     └─────────┴───────┼─────────┴───────────┘
                       ▼
                  COMMERCIAL
                       │
              ┌────────┼────────┐
              ▼        ▼        ▼
          POS+BOM   Billing   Payment / Voucher
                       │
                       ▼
                  ACCOUNTING  ◄──── HRIS (payroll, service charge,
                       ▲                caddy fee)
                       │
 PROCUREMENT ─── INVENTORY
```

---

# 43. Management Dashboard

## GolfOne Operational KPI Baseline

- Today Bookings
- Today Players
- Current Queue
- Players on Course
- Pending Check-in
- Caddies Available
- Caddies on Round
- Golf Carts Ready
- Golf Carts in Use
- Golf Carts in Maintenance
- Average Check-in to Tee-Off Time

## Golf KPI

- Golf Rounds
- Tee Time Utilization
- RevPATT (revenue per available tee time)
- Member vs Guest
- Rounds per Member
- Caddy Utilization
- Golf Cart Utilization
- Driving Range Usage
- Golf Revenue

## Sport Club KPI

- Court Utilization per fasilitas
- Entry per hari (walk-in, guest of member, member)
- Class Enrollment & Attendance
- Sport Revenue

## Membership KPI

- Active Members (per program)
- New Members
- Expiring Membership
- Renewal
- Member Activity
- Member Revenue

## Booking KPI

- Golf Booking
- Sport Court Booking
- Bungalow Booking & Occupancy
- Meeting Room Booking
- Event Booking
- Cancellation
- No-show
- Booking Revenue

## Banquet KPI

- Pipeline (inquiry → quotation → deal)
- Event Count & Pax
- Banquet Revenue
- Outstanding DP / Pelunasan

## Commercial KPI

- Total Sales
- POS Sales
- Package Sales
- Voucher Sold vs Redeemed
- Average Transaction
- Outlet Performance
- Food Cost %

## Inventory KPI

- Inventory Value
- Stock Balance
- Low Stock
- Stock Movement
- Stock Opname
- Theoretical vs Actual Variance

## Procurement KPI

- PR
- PO
- Outstanding PO
- Purchase Value
- Vendor Performance

## Finance KPI

- Revenue (per lini bisnis)
- AR
- AP
- Cash
- Outstanding Payment
- Deferred Revenue
- Profit & Loss

## CRM KPI

- Leads
- Conversion
- Active Customers
- Member Activity
- Campaign Performance
- Loyalty
- Top Spender

## HR KPI

- Headcount
- Attendance
- Overtime
- Payroll Cost
- Caddy Attendance & Rating

---

# 44. User Roles

### Management

- General Manager
- Club Manager
- Resort Manager
- Finance Manager

### Golf

- Golf Manager
- Golf Admin
- Starter / Marshal
- Caddy Manager
- Caddy
- Golf Staff
- Driving Range Staff

### Sport Club

- Sport Club Manager
- Sport Club Receptionist
- Instructor / Coach
- Lifeguard

### Membership

- Membership Admin
- Membership Manager

### Reservation

- Reservation Staff
- Front Desk

### Banquet & Event

- Banquet Manager
- Banquet Sales
- Event Manager
- Event Staff

### Commercial

- Cashier
- POS Staff
- Kitchen Staff
- Outlet Manager

### Sales & CRM

- Sales Executive
- CRM Admin
- Marketing Staff

### Warehouse

- Warehouse Staff
- Inventory Manager

### Procurement

- Procurement Staff
- Procurement Manager
- Approver

### Finance

- Accountant
- Finance Manager

### HR

- HR Admin
- HR Manager
- Employee (self-service)

### System

- Super Admin
- Property Admin
- Screen (TV Clubhouse Screen; hanya area Clubhouse Screen)

---

# 45. Recommended Application Structure

OneClub adalah **satu platform**: satu backend, satu database per customer instance, satu identitas, dan satu ACL (role + permission). Yang membedakan pengalaman tiap user adalah **role-nya**, bukan aplikasinya. Contoh: user dengan role Caddy hanya bisa membuka area Caddy Tablet; GM membuka Back Office dan Management Dashboard dengan akun yang sama.

Platform ditampilkan lewat tiga aplikasi, dipisah menurut **siapa penggunanya**, ditambah Integration Layer:

1. Website / Digital Channel (publik)
2. Customer Mobile Application (Member & Guest App)
3. Staff App: satu aplikasi untuk semua staf. Back Office, Management Dashboard, Operational Interface (termasuk POS), Kitchen Display, Caddy Tablet, Clubhouse Screen dan Platform Administration adalah **area** di dalamnya; area yang terbuka ditentukan oleh role dan permission user. Aplikasi yang sama dibuka di domain `dashboard`, `cashier`, `caddy` dan `kitchen` sesuai perangkatnya (§45).
4. Integration Layer

| Aplikasi | Pengguna | Fungsi utama | Teknologi |
|---|---|---|---|
| Website + Online Booking | Publik, non-member | Info, tarif terstruktur, booking golf/futsal/bungalow, inquiry wedding, ID/EN | Next.js (React, SSR) |
| Member & Guest App | Member, keluarga, tamu | Booking, kartu digital, statement, saldo voucher, skor, Hall of Fame | React PWA |
| Staff App | Semua staf club dan tim OneClub, sesuai role | Lihat area di bawah | React PWA, offline untuk area operasional |

Area di dalam Staff App (terbuka hanya bila role user memiliki permission area tersebut):

| Area | Pengguna (role, §44) | Fungsi utama | Offline |
|---|---|---|---|
| Back Office | Admin & back office | Setup semua modul, transaksi, approval, laporan | Tidak |
| Management Dashboard | GM & manajer | KPI real-time per lini bisnis | Tidak |
| Operational Interface | Staf lapangan | Starter, caddy master, POS, resepsionis sport, front desk, banquet | **Ya** (POS wajib) |
| Kitchen Display | Dapur / bar | Antrean pesanan per station | Tidak |
| Caddy Tablet | Caddy | Assignment, round, skor, on-course order, earnings | **Ya** |
| Platform Administration | Tim internal OneClub (Platform Admin) | Konfigurasi instance, module, branding, domain, integrasi | Tidak |
| Clubhouse Screen | Layar TV di clubhouse (role Screen) | Hall of Fame dan informasi club, layar penuh tanpa menu | Tidak |

Staff App tetap **satu aplikasi** (satu build), tetapi dibuka di beberapa **domain** sesuai tempat dan perangkatnya
*(diputuskan 5 Oktober 2026)*. Domain mengunci area; role tetap menentukan apa yang boleh dibuka.

| Domain (contoh) | Area | Dipakai di | Login |
|---|---|---|---|
| `dashboard.<club>` | Back Office, Management Dashboard, Platform Administration, Clubhouse Screen | Laptop/PC kantor, TV clubhouse | Email + password (+ MFA) |
| `cashier.<club>` | Operational Interface (POS, front desk, starter, caddy master, dll.) | Perangkat kasir dan konter | PIN staf per shift |
| `caddy.<club>` | Caddy Tablet | Tablet caddy di lapangan | PIN caddy |
| `kitchen.<club>` | Kitchen Display | Layar di dapur / bar | PIN |

Di `dashboard`, user langsung masuk ke Management Dashboard atau Back Office (sesuai role) dan berpindah area lewat user
menu; Management cukup dibatasi lewat menu dan permission. Perangkat bersama tidak pernah membuka area kantor.

Website dan Member & Guest App tetap aplikasi sendiri karena penggunanya pelanggan (publik dan member), bukan staf: Website butuh SSR untuk SEO, Member App adalah PWA ringan di ponsel.

## Admin Dashboard

Digunakan oleh management dan back-office.

```text
Dashboard
├── Golf
├── Sport Club
├── Membership
├── Booking
├── Stay & Venue
├── Banquet & Event
├── CRM
├── POS
├── Voucher
├── Inventory
├── Procurement
├── Accounting
├── HRIS
├── Reports
└── Settings
```

## Member / Guest Portal

```text
Home
├── Golf Booking
├── Sport Court & Class Booking
├── Bungalow Booking
├── Meeting / Event
├── My Booking
├── Membership
├── Voucher & Saldo
├── Score & Hall of Fame
├── Loyalty
├── My Transaction
└── Profile
```

## Staff Interface

Digunakan untuk kebutuhan operasional tertentu seperti:

- Starter & golf staff
- Caddy master
- Reservation staff / front desk
- Sport club reception
- Event & banquet staff
- Warehouse staff
- POS staff & kitchen display

---

# 46. Product Architecture

```text
┌─────────────────────────────────────────────┐
│              DIGITAL CHANNELS               │
│                                             │
│ Website │ Member App │ Staff App            │
│ (satu app, area per role, domain dashboard, │
│  cashier, caddy, kitchen)                   │
└──────────────────────┬──────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────┐
│               LINI BISNIS                   │
│                                             │
│ Golf Ops │ Sport Club │ Stay & Venue │      │
│ Banquet & Wedding                           │
└──────────────────────┬──────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────┐
│               CORE BERSAMA                  │
│                                             │
│ Membership │ Reservation Engine │           │
│ Commercial (POS+BOM, Pricing, Voucher) │    │
│ Billing & Payment │ CRM & Sales             │
└──────────────────────┬──────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────┐
│                BACK OFFICE                  │
│                                             │
│ Inventory │ Procurement │ Accounting │ HRIS │
└──────────────────────┬──────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────┐
│          MANAGEMENT & BI · PLATFORM         │
│                                             │
│ Dashboard │ Reports │ IAM │ Notifikasi │    │
│ Integrasi │ Approval │ Audit Trail          │
└─────────────────────────────────────────────┘
```

---

### Dedicated Customer Instance Baseline

GolfOne menjadi baseline untuk customer-instance architecture:

```text
Platform
└── Customer Instance
    ├── Configuration
    ├── Enabled Modules
    ├── Feature Configuration
    ├── Branding
    ├── Venue
    ├── Course
    ├── Users / Roles
    ├── Integrations
    └── Operational Data
```

Setiap customer instance dapat memiliki multiple venue dan course. Locale, currency, timezone, feature flags, custom domain, integration configuration, dan white-label reference dapat dikonfigurasi per instance.

# 47. Tech Stack

Keputusan awal: **Go (backend) + PostgreSQL + React (frontend), web-based.**

### Backend — Go

- **Modular monolith** dulu: satu deployable, modul per domain (golf, membership, reservation, billing, dst.)
- Batas modul tegas sehingga bisa dipecah menjadi service nanti
- REST + OpenAPI
- Akses data: pgx + sqlc
- Background job (notifikasi WhatsApp, end-of-day, renewal, pengingat pembayaran) dengan antrian berbasis Postgres (contoh: River)

### Database — PostgreSQL

- Schema per domain
- `property_id` di setiap tabel (multi-property)
- Anti double-booking di level database: `tstzrange` + `EXCLUDE` constraint
- Nilai uang memakai `numeric`, bukan float
- Jurnal accounting & audit trail bersifat append-only

### Frontend — React + TypeScript

- Back office: SPA (Vite)
- Website publik perlu SEO → **Next.js** (tetap React)
- Member app & staff app: PWA dulu, native bila nanti diperlukan
- **POS wajib bisa offline** dan sync saat koneksi kembali

### Integrasi

- Payment gateway (QRIS, Virtual Account, kartu)
- WhatsApp Business API
- e-Faktur (Coretax)
- Data residen Modernland (verifikasi tarif residence)
- Hardware bila ada: locker, akses/turnstile, ball dispenser
- **Migrasi data dari Rhapsody**

---

# 48. Core Data Entities

Entity utama yang perlu dipertimbangkan:

```text
Organization
Property
User
Role
Employee
Department
Shift
Attendance
Payroll

Customer
Member
Family Member
Corporate Account
Corporate Nominee
Guest

Membership Program
Membership
Membership Type
Membership Package
Eligibility Rule

Golf Course
Hole
Tee Set
Tee Sheet Template
Tee Time
Flight
Player
Caddy
Golf Cart
Locker
Bag Storage
Driving Range Bay
Golf Round
Score
Handicap
Tournament
Reciprocal Club
Hole-in-One Record
Hall of Fame Entry

Facility (court, pool, gym, spa)
Court
Court Booking
Class
Class Schedule
Class Enrollment
Instructor
Entry Ticket

Bungalow
Bungalow Type
Rate Plan
Bungalow Booking

VIP Suite Booking

Meeting Room
Room Layout Capacity
Meeting Room Booking

Event
Event Participant
Event Package
Banquet Booking
Banquet Package
Menu Selection
BEO
Payment Schedule

Reservation
Resource
Availability Slot

Price List
Rate Rule (segmen × tipe hari × time band)
Day Type
Time Band
Tax & Service Rule
Promotion
Package

Voucher
Prepaid Balance
Voucher Redemption

Product
Product Category
Menu
Modifier
Recipe / BOM
Recipe Line
Warehouse
Inventory
Stock Movement
Store Requisition
Asset

Supplier
Purchase Requisition
RFQ
Quotation
Purchase Order
Goods Receipt
Vendor Invoice

POS
Outlet
Sales Order
Kitchen Order
Customer Account
Folio
Invoice
Payment
Deposit

Chart of Account
Journal
Ledger
AR
AP
Bank Account
Deferred Revenue
Revenue Allocation Rule

Lead
Opportunity / Pipeline
Sales Quotation
Campaign
Loyalty
Customer Interaction
Feedback
Complaint Ticket

Notification
Approval
Audit Log
```

---

# 49. MVP Scope

> **Versi 3 — baseline delivery diperbarui mengikuti GolfOne.** Golf core mengikuti Phase 1–3 GolfOne, sedangkan SaaS commercialization dan business-line expansion tetap menjadi tahap lanjutan. Akan difinalisasi setelah discovery dengan club.

### Fase 1 — Core Platform & Golf Operations

- Dedicated Customer Instance Core
- Customer & Customer 360
- Basic Membership
- Venue, Course, Route & Hole
- Booking, Player & Flight
- Tee Sheet, Check-in, FIFO & Starter
- Package Pricing, Rate Plan, Pricing Snapshot
- Online Payment Framework & Pay at Venue
- Basic Caddy Assignment
- Basic Golf Cart Assignment & Readiness
- Customer Mobile
- Staff Dashboard
- RBAC & Audit
- Migrasi data Rhapsody

Sampai Accounting live di fase 3, jurnal diekspor ke sistem akuntansi yang sudah ada.

### Fase 2 — Complete Golf Experience & Shared Business Lines

- Complete Membership, Caddy & Golf Cart lifecycle
- Caddy Tablet & On-Course Operations
- Digital Scorecard, Playing History & Round Statistics
- Course Map / GPS / Panorama baseline
- Restaurant Reservation, Food Ordering & F&B
- Customer Preferences & Personalization
- Sport Club & Facility
- Classes & Training
- Stay & Venue (Bungalow, VIP Suite, Meeting Room)
- Voucher & Prepaid
- CRM dasar
- Hall of Fame & Top Spender

### Fase 3 — SaaS Commercialization, Integration & Business Expansion

- Automated Customer Instance Provisioning
- Subscription & Feature Tier
- White-Label Build Automation
- Custom Domain
- Multiple Payment Providers
- POS Integration
- Accounting Integration
- Enterprise SSO
- Dedicated Database Option
- Advanced Analytics
- Tournament Capability
- Banquet, MICE & Wedding
- Inventory & BOM
- Procurement
- Accounting penuh & Financial Reports

### Fase 4 — Enterprise Back Office & Advanced Capability

- HRIS & Payroll
- Tournament lanjutan
- Advanced CRM & Loyalty
- Advanced Analytics / BI
- Advanced Package Management

---

# 50. Out of Scope for Initial Product

Untuk menjaga MVP tetap fokus, berikut belum menjadi core:

- Golf Course Maintenance
- Course Agronomy
- Landscaping Management
- Hotel PMS Full Operations
- Housekeeping Operations
- Resort Engineering (di luar maintenance aset buggy & peralatan)
- Resort Security Operations
- Laundry Management
- Full Hotel Accounting
- Menu engineering lanjutan & reservasi meja restoran
- Smart Golf Course IoT

Fasilitas **bungalow, VIP suite, dan meeting room tetap tersedia sebagai booking inventory**, tanpa membangun seluruh resort operational management pada fase awal.

Perubahan dari versi 1:
- **HR / Payroll** dipindah ke dalam scope ([bagian 39](#39-hris--payroll)), cakupannya masih keputusan terbuka
- **Restaurant:** resep/BOM, food cost, dan pemotongan stok kini masuk scope; yang tetap di luar hanya menu engineering lanjutan dan reservasi meja

---

# 51. Product Positioning

### Product Name

**OneClub**

### Positioning

> An integrated Golf & Country Club enterprise platform with GolfOne-based golf operations, shared membership and reservation services, commercial transactions, customer engagement, business-line operations, back-office finance, and people management.

### Core Value Proposition

```text
Attract
   ↓
Book
   ↓
Play / Train / Stay / Celebrate
   ↓
Transact
   ↓
Manage Customer
   ↓
Manage Inventory
   ↓
Procure
   ↓
Account
   ↓
Manage People
   ↓
Analyze
```

### Core Differentiator

Platform menghubungkan **Golf Operations + Customer + Membership + Booking + Caddy + Golf Cart + Playing + F&B + Sport Club + Stay + Banquet + CRM + Commercial + ERP + HRIS** dalam satu ecosystem. GolfOne menjadi baseline operational source of truth untuk golf, sementara domain bisnis lain menggunakan shared membership, reservation, billing, customer, dan platform services.

---

# 52. Keputusan Terbuka

| # | Pertanyaan | Ke siapa |
|---|---|---|
| 1 | Apakah membership golf terpisah dari membership Sport Club? Tipe mana yang berhak tarif member golf Rp640rb? | Club |
| 2 | Nominal tiap komponen tarif all-in (green fee, caddy fee, buggy fee, HIO) | Club |
| 3 | HRIS penuh sampai payroll, atau operasional saja + payroll pihak ketiga? | Tim / Club |
| 4 | Modul Rhapsody yang dipakai dan cara ekspor datanya | Club |
| 5 | Hall of Fame publik: perlu opt-in pemain? | Club |
| 6 | Jumlah caddy & armada buggy, skema caddy fee & tip | Club |
| 7 | Kalender turnamen & golf academy/pro | Club |
| 8 | Daftar lengkap outlet F&B & pro shop; ada konsinyasi? | Club |
| 9 | Provider asuransi Hole-in-One | Club |
| 10 | Pola tee time weekday vs weekend (interval, two-tee start?) | Club |

---

# 53. Fit-Gap: Modern Golf & Country Club

### Profil

- **Lokasi:** Jl. Modern Golf Raya No. 99, Kota Modern, Tangerang, Banten (grup Modernland)
- **Golf:** 18 hole championship, 6.350 m, desain Peter Thomson; driving range indoor & outdoor; pro shop; chipping, bunker & putting green; locker pria & wanita
- **Sport Club:** tennis indoor 4, squash 2, tenis meja 2, badminton 3, futsal 3, basket & voli, kolam olympic, gym, aerobik, spa
- **Stay:** 14 bungalow + 1 VIP
- **Venue:** ballroom & 4 function room, kapasitas 30–1.000 pax; VIP Suite
- **Reciprocal:** 17 club di 5 negara
- **Jam operasional:** 06.00–21.00 (tee time mulai 05.30)
- **Kanal penjualan saat ini:** WhatsApp ke sales perorangan, email per departemen (reservation@, marketing@, banquet@), Instagram/Facebook/TikTok

### Sistem Saat Ini

- **Rhapsody Golf** (PT Realta Chakradarma): golf front office (bag drop, locker, flight, caddy, tournament & scoring), POS, membership, back office, BI
- Booking online publik di `http://103.4.165.15:8082/RhapsodyGolf/` **error database** saat dicek 2 Oktober 2026, dan diakses lewat IP tanpa HTTPS
- Implikasi: migrasi data dan feature parity (terutama bag drop & locker)

### Ringkasan Gap terhadap Versi 1

| Area | Versi 1 | Kebutuhan Modern Golf | Status |
|---|---|---|---|
| Sport Club | Belum ada | 10+ fasilitas, booking slot, kelas & training | Baru |
| Banquet & Wedding | Event umum | Paket per pax, BEO, DP 30%, pelunasan H-7, corkage | Baru |
| HRIS | Out of scope | Absensi, shift, caddy, instruktur, service charge | Baru |
| Voucher & Prepaid | Belum ada | Voucher bola, 5x masuk, paket 4x/8x, masa berlaku | Baru |
| Pricing Engine | Weekday/weekend | Segmen × 4 tipe hari × time band, harga ++ vs nett | Diperluas |
| Membership | Satu program | Multi-program, syarat umur, residen, keluarga | Diperluas |
| POS | Order & bayar | BOM/resep, food cost, kitchen display | Diperluas |
| Golf | Tee time, caddy, cart | Locker, reciprocal, HIO, night golf, driving range, Hall of Fame | Diperluas |
| CRM | Customer 360 | Pipeline per lini, lead WhatsApp, Top Spender | Diperluas |

### Sumber

- [moderngolf.co.id](https://www.moderngolf.co.id/): halaman About, Golf Course, Hole by Hole, Handicap Index, Reciprocal, Facilities, Sport Club, Bungalow, VIP Suite, MICE & Wedding, What's On, Contact
- Brosur di website: Golf Rates (berlaku 1 April 2026), Driving Range Promo 2025, Sport Club Rates 2025, Bungalow Rates, VIP Suite Price, Meeting Package 2022 (Half/Full/One Day), Wedding & Birthday Package, promo Wija Soju
- [Realta – Rhapsody Hospitality System](https://realta.co.id/hospitality-system/)


---

# 54. GolfOne Alignment

Versi ini menggunakan **GolfOne — Product Vision, Business Domain & Feature** sebagai baseline untuk golf core.

### Baseline yang diadopsi

- Dedicated Customer Instance
- Customer & Customer 360
- Customer vs Guest distinction
- Membership lifecycle
- Golf Structure & Course Configuration
- Playing Route
- Booking / Player / Flight
- Tee Operations
- FIFO Queue & Starter
- Operational Timestamp
- Package / Rate Plan / Dynamic Pricing
- Pricing Snapshot
- Payment / Refund / Adjustment / Folio
- Caddy lifecycle, assignment, history, performance
- Caddy Tablet & On-Course Operations
- Offline operation dan safe sync
- Playing & Digital Scorecard
- Golf Cart readiness, inspection, assignment, maintenance
- Restaurant & F&B ordering
- Customer Preferences & Personalization
- Customer Mobile Application
- Staff Web Dashboard
- Identity / RBAC / Audit
- Notification
- Reporting & Analytics
- Integration Layer
- Operational Exception & Recovery
- Future Intelligence

### Product Boundary

GolfOne capability menjadi **golf operational core**. Sport Club, Stay & Venue, Banquet & Wedding, CRM & Sales, Inventory, Procurement, Accounting, HRIS & Payroll tetap dipertahankan sebagai business-line dan enterprise expansion pada Product Overview.

Dengan boundary ini, Product Overview tetap menjadi **enterprise product overview**, sementara GolfOne menjadi **reference baseline untuk kedalaman dan operational truth pada domain golf**.
