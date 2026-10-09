# OneClub — Accommodation & Bungalow Management Requirements

## 1. Objective

Membangun modul **Accommodation & Bungalow Management** untuk OneClub agar bungalow dapat dikelola dan disewakan dengan operational flow seperti hotel/resort pada umumnya.

Scope mencakup:
- Bungalow inventory
- Room type
- Reservation
- Availability
- Rate management
- Guest management
- Front office
- Check-in / check-out
- Housekeeping
- Maintenance
- Guest request
- Folio & billing
- Packages & add-ons
- Promotions
- Corporate booking
- Waitlist
- Accommodation dashboard
- Guest stay experience

### Out of Scope
- Access Control integration
- Digital Key
- OTA integration
- Channel Manager
- Sport Booking integration

Sport Booking tetap menjadi domain terpisah di OneClub.

---

# 2. Product Structure

```text
ONECLUB
│
├── Sport Club
│   └── Sport Booking
│
└── Accommodation
    ├── Overview
    ├── Reservations
    ├── Front Office
    ├── Bungalows
    ├── Housekeeping
    ├── Maintenance
    ├── Guests
    ├── Rates & Packages
    └── Billing
```

---

# 3. Accommodation Dashboard

Dashboard digunakan oleh management dan operational team untuk melihat kondisi accommodation secara keseluruhan.

## KPI
- Occupancy
- Available Bungalows
- Occupied Bungalows
- Today's Arrival
- Today's Departure
- In-house Guests
- Revenue
- ADR
- RevPAR
- Cancelled Reservations
- No-show
- Bungalows Ready
- Bungalows Dirty
- Bungalows Cleaning
- Bungalows Maintenance

## Today's Operation
- Today's arrivals
- Today's departures
- Expected check-ins
- Expected check-outs
- Pending payment
- Pending room preparation
- VIP / priority guest
- Guest requests
- Housekeeping tasks
- Maintenance issues

---

# 4. Reservation Management

Menu:
```text
Reservations
├── All Reservations
├── Confirmed
├── Pending Payment
├── Checked-in
├── Checked-out
├── Cancelled
└── No-show
```

Minimal reservation data:
- Reservation ID
- Booking source
- Guest
- Room type
- Bungalow
- Check-in date
- Check-out date
- Number of nights
- Adults / children
- Rate plan
- Room rate
- Add-ons
- Discount
- Tax / service charge
- Total amount
- Payment status
- Reservation status
- Special request
- Notes
- Created / updated by and date

---

# 5. Booking Flow

```text
Search Availability
        ↓
Select Room Type
        ↓
Select Rate
        ↓
Guest Information
        ↓
Add-ons
        ↓
Price Summary
        ↓
Payment
        ↓
Reservation Confirmation
```

System harus melakukan availability validation sebelum reservation dikonfirmasi.

---

# 6. Availability & Inventory

Availability dihitung berdasarkan:
- Room inventory
- Existing reservations
- Check-in / check-out date
- Room blocks
- Maintenance
- Room status
- Booking rules

System harus mencegah overlapping reservation untuk bungalow yang sama.

Status:
```text
Available
Reserved
Occupied
Dirty
Cleaning
Inspected
Ready
Maintenance
Out of Order
Blocked
```

---

# 7. Reservation Calendar / Room Rack

Operational room rack harus mendukung Day, Week, Month, room type, bungalow, dan reservation status filter.

Contoh:
```text
                 10   11   12   13   14   15

Bungalow 01      █████████████
Bungalow 02           █████████
Bungalow 03      ███
Bungalow 04                ██████████
Bungalow 05      Maintenance
```

Quick action:
- View reservation
- Create reservation
- Change bungalow
- Reschedule
- Cancel
- Block room

---

# 8. Room Type Management

Contoh room type:
- Deluxe Bungalow
- Family Bungalow
- Premium Bungalow
- Villa Bungalow

Data:
- Name
- Description
- Photos
- Capacity
- Adult capacity
- Child capacity
- Bed configuration
- Room size
- View
- Amenities
- Number of units
- Base rate
- Active / inactive

---

# 9. Bungalow Inventory Management

Setiap bungalow individual dikelola sebagai inventory.

Data:
- Bungalow ID
- Bungalow number/name
- Room type
- Location
- Capacity
- Operational status
- Housekeeping status
- Maintenance status
- Notes

Contoh:
```text
Bungalow 01 — Deluxe — Ready
Bungalow 02 — Deluxe — Occupied
Bungalow 03 — Family — Maintenance
```

---

# 10. Room Status Management

Reservation status dan kondisi fisik bungalow harus dipisahkan.

### Reservation Status
```text
Available
Reserved
Occupied
Checked-out
```

### Operational Status
```text
Ready
Dirty
Cleaning
Inspected
Maintenance
Out of Order
Blocked
```

Bungalow dengan status `Maintenance`, `Out of Order`, atau `Blocked` tidak boleh ditawarkan untuk reservation.

---

# 11. Rate Management

Minimal Rate Plan:
- Standard Rate
- Weekend Rate
- Peak Season Rate
- Holiday Rate
- Member Rate
- Corporate Rate
- Promotional Rate

Konfigurasi:
- Room type
- Rate name
- Base price
- Weekday / weekend price
- Minimum / maximum stay
- Included breakfast
- Cancellation policy
- Payment policy
- Validity period
- Active / inactive

---

# 12. Seasonal / Period Pricing

Admin dapat menetapkan harga berdasarkan periode.

```text
Normal Season 01 Jan–30 Jun     Rp 1.200.000
Peak Season 01 Jul–31 Aug      Rp 1.500.000
Holiday 20 Dec–05 Jan           Rp 1.800.000
```

Jika beberapa rule berlaku pada tanggal yang sama, system harus memiliki pricing priority.

---

# 13. Package & Add-on Management

Package dapat berisi kombinasi room dan layanan.

Contoh:
```text
Stay Package
├── Room
├── Breakfast
└── Dinner
```

Add-on:
- Breakfast
- Extra Bed
- Extra Pillow
- Extra Towel
- BBQ
- Dinner
- Lunch
- Transportation
- Laundry
- Other Service

Setiap item memiliki name, price, unit, availability, tax, service charge, active/inactive.

---

# 14. Guest Management

Guest profile menjadi centralized customer record.

Data:
- Full name
- Phone
- Email
- Address
- Identity information
- Date of birth
- Nationality
- Preferences
- Notes
- Booking history
- Stay history
- Payment history

Guest history:
- Total stays
- Last stay
- Total nights
- Total spending
- Favorite room type
- Previous requests
- Cancellation history
- No-show history

---

# 15. Front Office

Menu:
```text
Front Office
├── Today's Arrival
├── Today's Departure
├── In-house Guests
├── Check-in
├── Check-out
└── Room Status
```

Arrival list menampilkan guest, reservation, room type, assigned bungalow, guest count, check-in time, payment status, special request, dan preparation status.

---

# 16. Room Assignment

```text
Reservation
     ↓
Check Room Availability
     ↓
Select Bungalow
     ↓
Assign Room
```

Warning jika bungalow occupied, maintenance, blocked, dirty, atau tidak sesuai kapasitas.

---

# 17. Check-in / Early Check-in

Flow:
```text
Reservation → Guest Verification → Payment Validation → Room Assignment → Check-in → In-house
```

Catat actual check-in time, staff, guest, bungalow, guest count, payment status, dan notes.

Early check-in diperbolehkan berdasarkan availability dan policy; additional fee dapat masuk ke folio.

---

# 18. Check-out / Late Check-out

Flow:
```text
Guest → Review Folio → Additional Charges → Payment → Check-out → Bungalow: Dirty → Housekeeping
```

Late check-out harus configurable dengan grace period dan fee.

Contoh:
```text
Standard Check-out 12:00
Grace Period 30 min
13:00–15:00 Late Check-out Fee
After 15:00 Additional Night / configured charge
```

---

# 19. Cancellation, Reschedule & No-show

Cancellation policy mendukung:
- Free cancellation deadline
- Cancellation fee
- Refund percentage
- Non-refundable rate
- No-show charge

Reschedule dapat mengubah check-in, check-out, room type, bungalow, guest count, dan rate plan dengan availability serta price recalculation.

No-show dapat menerapkan charge, release bungalow, update guest history, dan notification.

---

# 20. Housekeeping

Core room workflow:
```text
Dirty → Cleaning → Cleaned → Inspected → Ready
```

Task memiliki:
- Task ID
- Bungalow
- Task type
- Priority
- Assigned staff
- Start time
- Completion time
- Status
- Notes

Task type:
- Checkout Cleaning
- Stayover Cleaning
- Deep Cleaning
- Turndown
- Inspection
- Other

Checklist dapat mencakup bedroom, bathroom, general area, amenities, AC, lighting, dan final inspection.

---

# 21. Room Inspection

```text
Cleaning Completed → Inspection → Passed → Ready
                         ↓
                    Failed → Cleaning
```

Catat inspector, timestamp, checklist, result, dan notes.

---

# 22. Maintenance

Menu:
```text
Maintenance
├── Work Orders
├── Preventive Maintenance
└── Maintenance History
```

Work order status:
```text
Open → Assigned → In Progress → Resolved → Closed
```

Preventive maintenance mendukung schedule seperti AC cleaning, water heater inspection, dan electrical inspection.

Jika maintenance membutuhkan bungalow ditutup, status menjadi `Out of Order` dan inventory tidak tersedia untuk booking.

---

# 23. Guest Request

Request dapat berupa:
- Extra Towel
- Extra Bed
- Room Cleaning
- Maintenance
- Laundry
- Transportation
- Food / Beverage
- Other

Workflow:
```text
Requested → Assigned → In Progress → Completed
```

Request yang berbayar dapat masuk ke folio.

---

# 24. Folio & Billing

Setiap stay memiliki Guest Folio.

Folio dapat berisi:
```text
Room Charge
Breakfast
Food & Beverage
Laundry
Extra Bed
Guest Request
Other Charges
Discount
Tax
Payment
Refund
```

Staff dapat melakukan `Charge to Room` dengan guest/reservation, bungalow, description, amount, tax, service charge, staff, dan timestamp.

---

# 25. Payment & Deposit

Payment status:
```text
Unpaid
Pending
Paid
Partially Paid
Refund Pending
Refunded
Failed
```

Deposit mendukung:
- Amount
- Date
- Payment method
- Refundable / non-refundable
- Refund status

Payment dapat terjadi pada booking, deposit, check-in, additional charge, dan check-out.

---

# 26. Guest Stay Experience

Guest dapat melihat:
```text
My Stay
Bungalow 03
10–13 October

Check-in 14:00
Check-out 12:00

Reservation
Guest Services
Folio
Stay Information
House Rules
Amenities
Property Information
```

Access Control dan Digital Key tidak termasuk scope.

---

# 27. Notification

Reservation:
- Created
- Payment success
- Confirmed
- Modified
- Cancelled
- Reminder

Stay:
- Check-in reminder
- Check-in confirmation
- Check-out reminder
- Late check-out notification
- Check-out confirmation

Operations:
- Guest request update
- Maintenance update
- Housekeeping status

---

# 28. Booking Source

Reservation mencatat source:
```text
Direct Website
Member App
Guest App
Front Desk
Phone
Walk-in
Corporate
```

OTA dan Channel Manager tidak termasuk scope.

---

# 29. Corporate Booking

Corporate account dapat memiliki:
- Corporate rate
- Billing arrangement
- Guest list
- Booking limit
- Payment terms
- Booking history

---

# 30. Waitlist

Jika room type penuh:
```text
Deluxe Bungalow
No Availability
[ Join Waitlist ]
```

Waitlist menyimpan guest, room type, preferred date, nights, guest count, created date, priority, dan status.

Ketika inventory tersedia, system dapat notify guest dan mengonversi waitlist menjadi reservation setelah confirmation.

---

# 31. Promotions

Promotion dapat berupa:
- Percentage Discount
- Fixed Discount
- Stay X Pay Y
- Early Booking
- Last Minute
- Long Stay
- Weekend Promotion
- Holiday Promotion
- Corporate Promotion

Configuration meliputi validity, eligible room type/date, minimum/maximum stay, booking source, usage limit, dan discount rule.

---

# 32. Reporting & Hotel KPI

Reporting minimal:
- Occupancy
- Occupied rooms
- Available rooms
- Out of order rooms
- Room revenue
- Add-on revenue
- Total accommodation revenue
- Booking volume
- Booking by room type
- Booking by source
- Cancellation
- No-show
- Average length of stay
- New / returning guests
- Guest spending
- Room turnaround time
- Housekeeping completion
- Maintenance issues
- Guest request completion

### KPI

Occupancy:
```text
Occupied Room Nights
──────────────────── × 100
Available Room Nights
```

ADR:
```text
Room Revenue
────────────
Rooms Sold
```

RevPAR:
```text
Room Revenue
──────────────────
Available Room Nights
```

Average Length of Stay:
```text
Total Room Nights
─────────────────
Number of Reservations
```

---

# 33. Audit Trail

Semua perubahan reservation dan stay harus tercatat.

Minimal:
- Action
- Actor
- Previous value
- New value
- Timestamp
- Source
- Reason, jika applicable

Contoh:
```text
Reservation created
Payment completed
Bungalow assigned
Guest checked-in
Additional charge added
Late checkout requested
Guest checked-out
```

---

# 34. Permission

Minimal role:
```text
Accommodation Manager
Front Office
Reservation Staff
Housekeeping
Maintenance
Finance / Cashier
Admin
```

Permission harus mengatur akses terhadap reservation, check-in/out, room status, housekeeping, maintenance, folio, payment, dan rate management.

---

# 35. Development Priority

## P0 — Accommodation Booking MVP

### Inventory
- Room Type
- Bungalow
- Room Status
- Availability

### Reservation
- Search Availability
- Create Reservation
- Reservation Detail
- Reservation Calendar / Room Rack
- Guest Profile
- Room Assignment
- Payment Status
- Confirmation
- Cancellation
- Reschedule
- No-show

### Rate
- Standard Rate
- Weekend Rate
- Basic Rate Rules

### Front Office
- Today's Arrival
- Today's Departure
- Check-in
- Check-out
- In-house Guest

## P1 — Hotel Operations

- Housekeeping
- Room Cleaning
- Room Inspection
- Maintenance
- Guest Request
- Folio
- Additional Charges
- Deposit
- Refund
- Late Check-out
- Early Check-in
- Waitlist

## P2 — Revenue & Guest Experience

- Rate Plans
- Seasonal Pricing
- Packages
- Add-ons
- Promotions
- Corporate Booking
- Guest Stay Experience
- Guest History
- Advanced Notification
- Accommodation Dashboard
- Hotel KPI
- Advanced Reporting

## P3 — Advanced Accommodation

- Dynamic Pricing
- Advanced Revenue Management
- Advanced Guest Segmentation
- Advanced Corporate Contract
- Advanced Housekeeping Optimization
- Preventive Maintenance Automation
- Advanced Financial Reporting
- Accounting Integration

---

# 36. Key Product Principles

## Room Type vs Bungalow

```text
Room Type
    ↓
Inventory
    ↓
Individual Bungalow
```

## Reservation vs Stay

```text
Reservation
    ↓
Check-in
    ↓
Stay
    ↓
Check-out
```

## Reservation Status vs Room Status

Keduanya harus dipisahkan.

```text
Reservation Status
Confirmed / Checked-in / Checked-out / Cancelled

Room Status
Ready / Dirty / Cleaning / Maintenance / Blocked
```

## Folio sebagai sumber transaksi selama stay

```text
Reservation
     ↓
Stay
     ↓
Folio
 ├── Room
 ├── Add-on
 ├── Service
 ├── Other Charge
 └── Payment
```

---

# 37. Acceptance Criteria

Accommodation Management dianggap siap apabila:

- Admin dapat membuat dan mengelola room type.
- Admin dapat membuat dan mengelola individual bungalow.
- System menghitung availability berdasarkan inventory dan reservation.
- System mencegah double booking.
- Admin dapat melihat reservation dalam room rack/calendar.
- Guest dapat melakukan reservation.
- Admin dapat membuat reservation dari dashboard.
- System mendukung rate plan.
- System mendukung cancellation dan no-show policy.
- System mendukung reschedule.
- System mendukung room assignment.
- Front Office dapat melakukan check-in.
- Front Office dapat melakukan check-out.
- Room otomatis berubah menjadi `Dirty` setelah check-out.
- Housekeeping dapat mengubah room menjadi `Cleaning`, `Inspected`, dan `Ready`.
- Maintenance dapat membuat work order.
- Room dapat menjadi `Out of Order` sehingga tidak dapat dibooking.
- Guest request dapat dibuat dan diproses.
- Guest memiliki folio selama stay.
- Additional charge dapat dimasukkan ke folio.
- Payment dan refund dapat dicatat.
- System mendukung deposit.
- System mendukung waitlist.
- System mendukung package, add-on, dan promotion.
- System memiliki accommodation dashboard.
- System memiliki audit trail.
- System memiliki role-based permission.
- Access Control, Digital Key, OTA/Channel Manager, dan Sport Integration tidak termasuk dalam scope.
