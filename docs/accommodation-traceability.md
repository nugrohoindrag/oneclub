# Accommodation & Bungalow Management — Traceability

Pemetaan `docs/oneclub-accommodation-bungalow-management-requirements.md` ke implementasi (modul `stay`, migration `stay/00004_accommodation.sql`).
Uji: `test/e2e/p6_accommodation_test.go` (TestAccommodation).

| § | Requirement | Backend (`internal/stay`) | UI |
|---|---|---|---|
| 2 | Struktur menu Accommodation | — | Back Office › Accommodation (`navigation.go`), Ops › Stay Front Desk / Housekeeping / Bungalow Maintenance |
| 3 | Dashboard KPI + today's operation | `dashboard.go` `GET /stay/dashboard` | `accommodation/overview.tsx` |
| 4 | Reservation (tab status, data minimal) | `stays.go`, `GET /stay/reservations?tab=` | `reservations.tsx`, `shared.tsx` (StayDrawer) |
| 5 | Booking flow + validasi availability | `rooms.go` `GET /stay/search`, `POST /stay/stays:quote`, `POST /stay/stays` | `NewReservationPage` (wizard 7 langkah) |
| 6 | Availability & inventory, anti overlap | Reservation Engine (EXCLUDE), room blocks = reservasi `block` | rack, search |
| 7 | Room rack Day/Week/Month + quick action | `GET /stay/room-rack` | `RoomRackPage` |
| 8 | Room type | `BungalowTypes` (+ foto, bed, ukuran, view, base/weekend rate) | Bungalows › Room Types |
| 9 | Bungalow inventory | `Bungalows` (+ lokasi, kapasitas, notes, hkStatus) | Bungalows › Inventory |
| 10 | Status reservasi vs operasional | `hk_status` + room blocks; `GET /stay/room-status` | `RoomStatusBoard` |
| 11–12 | Rate plan, seasonal pricing + prioritas | `rates.go` (BAR per malam, season, derivasi plan), resource rate plan/season | Rates & Packages |
| 13 | Package & add-on | `stay.packages`, `stay.addons`, `stay.stay_addons` | Rates & Packages, wizard, drawer |
| 14 | Guest profile & history | `guests.go` | `GuestsPage`, `GuestProfilePage` |
| 15 | Front office | `frontoffice.go` `GET /stay/front-office` | `FrontOfficePage` (BO & Ops) |
| 16 | Room assignment + warning | `GET /stay/stays/{id}/unit-options`, `:assign-unit` | `AssignModal`, check-in |
| 17 | Check-in / early check-in | `CheckIn` + `earlyCheckIn` (Stay Policies) | `CheckInModal` |
| 18 | Check-out / late check-out (grace, fee, malam tambahan) | `chargeLateCheckout`, `:late-checkout`, Dirty + tugas HK otomatis | `CheckOutModal`, `LateCheckoutModal` |
| 19 | Cancellation, reschedule, no-show | kebijakan rate plan (`cancellationFee`, `chargeNoShow`), `:reschedule` | `CancelModal`, `RescheduleModal` |
| 20–21 | Housekeeping + inspection | `housekeeping.go` | `HousekeepingPage` |
| 22 | Maintenance, preventive, Out of Order | `maintenance.go` | `StayMaintenancePage` |
| 23 | Guest request (berbayar → folio) | `requests.go` | `RequestsPage`, `RequestModal` |
| 24–25 | Folio, charge to room, payment, deposit, refund | Billing folio, `:charge`, addons | drawer › Folio, `PayModal`, Billing |
| 26 | Guest stay experience | `experience.go` (member + publik) | Member App `/activity/stays/:id`, website `/[lang]/my-stay` |
| 27 | Notifikasi | `notify.go` (template + job harian 07:00) | — |
| 28 | Booking source | `stays.booking_source` | wizard, filter |
| 29 | Corporate booking | `stay.corporate_terms` (rate, billing arrangement, limit, terms) | Rates › Corporate, wizard |
| 30 | Waitlist | `waitlist.go` (auto-offer saat kamar bebas) | `WaitlistPage`, Join Waitlist di wizard |
| 31 | Promotions | `stay.promotions` (percent, fixed, stay X pay Y, early, last minute, long stay, weekend, holiday, corporate) | Rates › Promotions |
| 32 | Reporting & KPI (Occupancy, ADR, RevPAR, ALOS) | `GET /stay/accommodation-report` | `AccommodationReportsPage` |
| 33 | Audit trail | `audit.Record` di setiap aksi, `GET /stay/stays/{id}/history` | drawer › History |
| 34 | Permission & role | `accommodation.go` + role template Accommodation Manager, Housekeeping, Maintenance | — |

Kompatibilitas P2: tipe bungalow tanpa base rate tetap dihargai lewat Commercial pricing rules (rate plan seperti `STAY_RO`). Paket Commercial (P3) tetap mengikuti paketnya.
Di luar scope (sesuai §1): Access Control, Digital Key, OTA/Channel Manager, integrasi Sport Booking. P3 dokumen (dynamic pricing, dsb.) belum dikerjakan.
