# OneClub — Product Roadmap

> **Source:** `OneClub — Product Overview v3.md`  
> **Baseline:** GolfOne untuk Golf Core Operations  
> **Target Client Baseline:** Modern Golf & Country Club, Kota Modern, Tangerang  
> **Version:** 1.0  
> **Date:** 3 October 2026
>
> Dokumen ini menerjemahkan seluruh Product Overview v3 menjadi roadmap delivery bertahap. Scope di bawah mempertahankan domain, capability, operational baseline, business flow, application surface, back-office capability, dan enterprise expansion yang tercantum pada Product Overview.

---

# 1. Product Roadmap Overview

## 1.1 Product Direction

OneClub dibangun sebagai **enterprise platform terintegrasi** yang menghubungkan:

```text
Acquire
  ↓
Discover
  ↓
Book
  ↓
Pay
  ↓
Check-in
  ↓
Play / Train / Stay / Celebrate
  ↓
Transact
  ↓
CRM
  ↓
Loyalty & Retention
  ↓
Inventory
  ↓
Procurement
  ↓
Accounting
  ↓
People Management
  ↓
Management & BI
```

GolfOne menjadi **operational source of truth untuk golf core**, sementara Membership, Reservation Engine, Commercial, Billing & Payment, CRM, Inventory, Procurement, Accounting, HRIS, Management & BI, serta Platform menjadi shared enterprise capability.

---

# 2. Roadmap Principles

Roadmap mengikuti prinsip berikut:

1. **Golf operational truth lebih dahulu** karena menjadi baseline produk dan kebutuhan parity terhadap Rhapsody.
2. **Core bersama dibangun sekali** untuk dipakai Golf, Sport Club, Stay & Venue, dan Banquet.
3. **Booking, Membership, Pricing, Billing, dan Customer** menjadi shared service.
4. **Operational workflow didahulukan sebelum advanced analytics dan commercialization.**
5. **Financial evidence harus traceable** dari pricing snapshot sampai payment, refund, adjustment, folio, dan accounting.
6. **Offline-first** digunakan untuk operational surfaces yang membutuhkan continuity, terutama POS dan operational apps.
7. **Dedicated Customer Instance** menjadi baseline multi-customer architecture.
8. Setiap fase menghasilkan capability yang dapat digunakan dan diuji secara end-to-end.
9. Feature expansion tidak boleh mengorbankan parity terhadap capability existing Rhapsody yang sudah dinyatakan wajib.
10. Open decisions dari club harus menjadi input discovery dan configuration sebelum capability terkait di-production-kan.

---

# 3. Phase Map

| Phase | Nama | Fokus | Mapping Product Overview |
|---|---|---|---|
| **P0** | Platform Foundation | Tenant, IAM, master data, architecture, audit, integration foundation | Sections 1–4, 46–48 |
| **P1** | Golf Core MVP | Golf booking sampai tee-off, customer, membership dasar, caddy/cart dasar, check-in | Product Overview MVP Fase 1 |
| **P2** | Complete Golf Experience & Shared Core | Golf operational lifecycle lengkap + customer app + sport club + stay + F&B + voucher | Product Overview MVP Fase 2 |
| **P3** | Commercial & Business Expansion | CRM, pricing/promotion, package, banquet/event, POS/BOM, tournament | Product Overview MVP Fase 3 |
| **P4** | Enterprise Back Office | Inventory, procurement, accounting, financial reporting, integrations | Product Overview MVP Fase 3 |
| **P5** | People & Advanced Enterprise | HRIS/payroll, advanced CRM/loyalty, BI, advanced package, tournament | Product Overview MVP Fase 4 |
| **P6** | SaaS Commercialization | Provisioning, subscription, tiers, white-label, custom domain, enterprise options | Product Overview Fase 3 SaaS |
| **P7** | Advanced Experience & Intelligence | Advanced golf experience, intelligence, automation, enterprise optimization | GolfOne future capability + Product Overview expansion |

> **Catatan:** Product Overview mendefinisikan MVP secara eksplisit dalam Fase 1–4. P0–P7 di roadmap ini memecah delivery tersebut menjadi milestone yang lebih operasional agar dependency dan sequencing lebih jelas.

---

# 4. P0 — Platform Foundation

## Objective

Membangun fondasi teknis dan platform capability sebelum transactional domain masuk production.

## Scope

### 4.1 Dedicated Customer Instance

- Customer Instance
- Organization
- Property
- Venue
- Multiple venue
- Multiple course
- Instance configuration
- Enabled modules
- Feature configuration
- Branding
- Locale
- Currency
- Timezone
- Custom domain configuration
- Integration configuration
- White-label reference
- Operational data isolation

## 4.2 Identity & Access

- User
- Role
- RBAC
- Super Admin
- Property Admin
- Management access
- Staff access
- Domain-based authorization
- Property-level isolation

## 4.3 Platform Master Data

Foundation untuk:

- Customer
- Guest
- Member
- Employee
- Department
- Product
- Facility
- Resource
- Property
- Venue
- Course
- Outlet
- Supplier
- Payment method
- Tax & service configuration

## 4.4 Platform Services

- Notification
- Approval
- Audit Log
- Integration Layer
- Background job
- Operational exception handling
- Recovery mechanism
- Reporting foundation

## 4.5 Technical Foundation

### Backend

- Go
- Modular monolith
- Domain/module boundaries
- REST
- OpenAPI
- pgx
- sqlc
- PostgreSQL-backed background job

### Database

- PostgreSQL
- Schema per domain
- `property_id` pada setiap table
- `tstzrange`
- Database-level anti double-booking
- `numeric` untuk monetary value
- Append-only accounting journal
- Append-only audit trail

### Frontend

- React
- TypeScript
- Vite SPA untuk back office
- Next.js untuk public website
- PWA baseline untuk member/staff
- Offline-capable architecture

## 4.6 Application Shell

- Website / Digital Channel
- Member & Guest Portal
- Staff App (satu aplikasi untuk semua staf; area per role; satu build di beberapa domain, Tech Doc §6.1):
  - `dashboard.<club>`: Back Office, Management Dashboard (dibatasi menu/permission), Platform Administration,
    Clubhouse Screen (role Screen, P2)
  - `cashier.<club>`: Operational Interface (POS, front desk, starter, caddy master, dll.; PIN perangkat)
  - `caddy.<club>`: Caddy Tablet (P2; PIN perangkat)
  - `kitchen.<club>`: Kitchen Display (P2; PIN perangkat)

## P0 Deliverables

```text
Platform
├── Tenant / Customer Instance
├── IAM / RBAC
├── Master Data Foundation
├── Audit
├── Notification
├── Approval
├── Integration Layer
├── Application Shell
└── Database / API Foundation
```

## Exit Criteria

- Customer instance dapat dibuat dan diisolasi.
- User dapat memiliki role dan access boundary.
- Core master data dapat dikelola.
- Semua transactional module memiliki audit strategy.
- API contract tersedia.
- Multi-property boundary tersedia.
- Application surfaces dapat login dan mengakses capability sesuai role.

---

# 5. P1 — Golf Core MVP

> Mapping langsung ke Product Overview **MVP Fase 1 — Core Platform & Golf Operations**.

## Objective

Mencapai golf operational parity minimum untuk menggantikan/menopang core workflow Rhapsody.

---

## 5.1 Customer & Customer 360 — Basic

### Scope

- Customer Profile
- Customer vs Guest distinction
- Guest
- Customer history
- Basic Customer 360
- Customer preferences foundation
- Family relationship foundation
- Corporate account foundation
- Corporate nominee foundation

### Customer Journey

```text
Customer
  ↓
View Availability
  ↓
Book
  ↓
Play
  ↓
Transaction
```

---

# 6. P1 — Golf Structure & Course

## 6.1 Golf Course

- Multi-course support
- Course
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

## 6.2 Hole

- Hole number
- Par
- Distance
- Stroke Index

## 6.3 Tee Set

- Black
- Blue
- Red
- White
- Course Rating
- Slope

## 6.4 Course Availability Blocking

- Maintenance
- Tournament
- Private Event
- Weather Closure
- Management Hold
- Date/Time Blocking

## 6.5 Handicap Foundation

- Handicap Index
- Course Rating
- Slope reference

---

# 7. P1 — Tee Time & Tee Sheet

## 7.1 Tee Time

- Tee Time
- Availability
- Tee Time interval
- Maximum players
- Minimum players
- Booking window
- Booking cut-off
- Peak time
- Off-peak time
- Member priority
- Guest availability
- Start tee

## 7.2 Tee Sheet Template

Support template berdasarkan:

- Day Type
- Session
- Weekday
- Weekend / Public Holiday
- Morning
- Afternoon
- Night Golf

## 7.3 Night Golf Foundation

- Dedicated session
- Dedicated rate
- Minimum 3 players
- Lighting status
- Night operating hours

## 7.4 Tee Operational State

- Readiness
- Caddy Readiness
- Golf Cart Readiness
- Queue Position
- Active Dispatch Queue
- Preserved Queue Position
- Actual Tee-Off
- Round Status

## 7.5 Operational Timestamp

Separate timestamp:

- Booking Time
- Check-in Time
- Ready Time
- Actual Tee-Off
- Round Start
- Round Finish

## 7.6 Starter Control

- Hold
- Skip +1
- Release
- Operational hold reason
- Queue re-evaluation

---

# 8. P1 — Golf Booking

## Features

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
- Rain Check
- Booking History
- Booking Modification

## Booking Model

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

## Booking Entities

- Booking
- Flight
- Booking Player
- Player Eligibility
- Player Pricing
- Player Entitlement
- Booking History
- Booking Modification
- Booking Cancellation

## Constraint

- Maximum 4 players per booking pada baseline saat ini.
- Booking interval configurable.
- Booking window configurable.
- Membership validation configurable.
- Player validation configurable.

---

# 9. P1 — Flight & Player

## Flight

- Flight
- Flight assignment
- Player grouping
- Flight status

## Player

- Member
- Guest of Member
- Reciprocal Member
- Non-Member

## Player Control

- Player validation
- Player eligibility
- Player pricing
- Player entitlement

---

# 10. P1 — Basic Membership

## Scope

- Member Registration
- Membership Program
- Membership Type
- Membership Package
- Membership Period
- Membership Status
- Membership Renewal foundation
- Membership Card
- Digital Member Card
- Member Privilege
- Guest Privilege
- Member Statement
- Member Account / Charge
- Membership History

## Lifecycle

```text
Application
  ↓
Approval
  ↓
Membership Fee
  ↓
Activation
  ↓
Membership Card
  ↓
Active Membership
```

## P1 Eligibility Foundation

- Age
- Residence
- Family composition
- Student status
- Gender

Advanced lifecycle and eligibility rules continue in P2.

---

# 11. P1 — Basic Caddy

## Scope

- Caddy Master
- Caddy Profile
- Caddy Availability
- Caddy Assignment
- Caddy Status
- Caddy Fee
- Caddy Tip
- Current Assignment
- Basic Caddy History

## Assignment

```text
Flight
├── Player → Caddy
├── Player → Caddy
├── Player → Caddy
└── Player → Caddy
```

---

# 12. P1 — Basic Golf Cart

## Scope

- Golf Cart Master
- Cart Number
- Cart Type
- Cart Status
- Availability
- Assignment
- Cart Fee
- Buggy sharing rule
- Surcharge buggy tambahan
- Basic Usage History

## Readiness State

- READY
- NOT_READY
- IN_USE
- Charging
- Maintenance
- Out of Service

---

# 13. P1 — Check-in, Starter & Locker

Parity dengan existing Rhapsody requirement.

## Scope

- Player Check-in
- Member Card
- QR
- Booking Code
- Bag Drop
- Bag Storage
- Locker Assignment
- Male Locker
- Female Locker
- Starter Sheet
- Flight Calling
- Marshal
- Course Open/Close Status
- Hole Closed Status
- Weather Status
- Rain Check

---

# 14. P1 — Pricing Foundation

## Scope

- Package Pricing
- Rate Plan
- Pricing Rule
- Pricing Resolution
- Pricing Snapshot
- Member Rate
- Guest Rate
- Non-Member Rate
- Effective Date
- Day Type
- Time Band
- Tax / Service rule foundation
- Nett / ++ flag

## Pricing Snapshot

Setiap transaksi golf menyimpan hasil pricing yang digunakan saat booking sehingga historical transaction tetap traceable.

---

# 15. P1 — Payment Foundation

## Scope

- Online Payment Framework
- Pay at Venue
- Payment Policy
- Payment
- Refund foundation
- Deposit foundation
- Payment Status
- Payment History

## Payment Methods

- Cash
- Bank Transfer
- Virtual Account
- QRIS
- Card
- Payment Gateway
- Member Account / Charge
- Voucher & Prepaid foundation

---

# 16. P1 — Staff Dashboard

## Operational Interfaces

- Starter
- Golf Staff
- Reservation Staff
- Front Desk
- Caddy Master
- Basic Golf Cart Operations
- Check-in
- Tee Sheet

---

# 17. P1 — Customer Mobile MVP

## Member / Guest

- Login
- View availability
- Golf booking
- Tee time selection
- Flight / player selection
- Caddy / cart
- Payment
- Booking confirmation
- My booking
- Transaction history
- Digital member card foundation

---

# 18. P1 — Rhapsody Migration

## Scope

- Source data inventory
- Rhapsody module inventory
- Data export assessment
- Data mapping
- Customer/member migration
- Golf booking history migration where available
- Membership migration
- Operational master migration
- Validation
- Reconciliation
- Migration cutover

## Mandatory parity focus

- Bag drop
- Locker
- Flight
- Caddy
- Tournament/scoring data where migration scope allows
- POS
- Membership
- Back-office data

---

# 19. P2 — Complete Golf Experience

> Mapping Product Overview **MVP Fase 2 — Complete Golf Experience & Shared Business Lines**.

---

# 20. P2 — Complete Membership Lifecycle

## Scope

- Membership Application & Approval
- Membership Fee History
- Annual Fee
- Due Date
- Postpone
- Reactivation
- Card Replacement Fee
- Membership Entitlement
- Booking Entitlement
- Member Rate
- Pause Eligibility
- Pause Period
- Benefit Impact
- Validity Adjustment
- Renewal Eligibility
- Expiry Management
- Suspension
- Cancellation
- Family Member
- Corporate Nominee
- Multi-program membership

## Programs

- Golf
- Sport Club
- Corporate
- Residence

---

# 21. P2 — Advanced Golf Caddy Lifecycle

## Scope

- Caddy Level
- Junior / Senior
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
- Caddy Performance / Rating
- Caddy Fee Settlement

## Rules

- Caddy promotion requires approval.
- Promotion generates level history.
- Caddy is treated as partner/non-employee unless club policy says otherwise.
- Caddy fee may be recorded as liability when included in all-in price.

---

# 22. P2 — Caddy Tablet & On-Course Operations

## Scope

- Caddy Tablet Application
- Assignment
- Round information
- Customer history
- On-course interaction
- Offline operation
- Safe sync
- Operational exception handling
- Caddy replacement
- Caddy status

---

# 23. P2 — Golf Cart Full Lifecycle

## Scope

```text
Inspection
  ↓
READY
  ↓
Assignment
  ↓
IN_USE
  ↓
Return
  ↓
Post-Operation Inspection
  ↓
AVAILABLE / Maintenance
  ↓
Release Inspection
  ↓
READY
```

### Capability

- Inspection
- Replacement
- Usage Hours
- Maintenance Release
- Incident
- Replacement History
- Assignment History
- Readiness
- Maintenance Status

---

# 24. P2 — Digital Scorecard & Playing

## Digital Scorecard

- Score Entry
- Score Validation
- Score Finalization
- Authorized Score Correction
- Correction Reason
- Score Audit History

## Finalized Score Rule

Finalized score tidak diedit melalui normal editing. Correction dilakukan melalui authorized correction workflow.

## Playing History

- Round History
- Score History
- Round Statistics
- Hole Progress
- Hole Duration
- Round Duration

## Playing Experience Foundation

- Course Map
- GPS Distance
- Panorama
- Pace-of-Play
- Golf Cart GPS Adapter foundation

---

# 25. P2 — Hall of Fame

## Scope

- Hole-in-One Wall
- Club Champion
- Course Record
- Albatross
- Eagle
- Tournament Champion
- Club History
- Website display
- Member App display
- Clubhouse screen

## Categories

- Men
- Ladies
- Senior
- Junior

## Privacy

- Public display requires player opt-in.

---

# 26. P2 — Driving Range

## Scope

- Indoor Range
- Outdoor Range
- Bay Assignment
- Ball Bucket Sale
- Prepaid Ball Balance
- Ball Expiry
- Ball Dispenser Integration
- Driving Range Usage Reporting

---

# 27. P2 — Reciprocal Club

## Scope

- Reciprocal Club Master
- Country
- City
- Contact
- Reciprocal Rate
- Member Verification
- Introduction Letter
- Settlement
- Reciprocal Visit Report

---

# 28. P2 — Hole-in-One

## Scope

- HIO Voucher / Insurance
- HIO Record
- Player
- Hole
- Tee
- Date
- Witness
- Caddy
- Insurance Claim
- Automatic Hall of Fame entry

---

# 29. P2 — Sport Club & Facility

## Facility Types

- Tennis indoor
- Squash
- Table Tennis
- Badminton
- Futsal
- Basketball
- Volleyball
- Olympic Pool
- Gym
- Aerobic Studio
- Spa
- Sauna
- Steam
- Jacuzzi
- Massage

## Scope

- Facility Master
- Court Master
- Slot Booking
- Time Band
- Custom Day Type
- Session Package
- Entry Ticket
- Walk-in Guest
- Guest With Member
- Child Entry
- Family Package
- Voucher
- Access Control
- Sport Locker

## Shared Core

Membership, Reservation Engine, Billing & Payment, Pricing & Promotion, Voucher & Prepaid.

---

# 30. P2 — Classes & Training

## Scope

- Class Program
- Swimming
- Tennis
- Aikido
- Aerobic
- Gym Class
- Instructor / Coach
- Class Schedule
- Registration
- Registration Fee
- Package 4x
- Package 8x
- Session Quota
- Attendance
- Quota Usage
- Member Price
- Guest Price
- Instructor Honorarium
- HRIS integration point

---

# 31. P2 — Stay & Venue

## 31.1 Bungalow

- Bungalow Master
- Bungalow Type
- Bungalow Number
- View
- Capacity
- Rate
- Rate Plan
- Room Only
- Long Stay
- Breakfast inclusion
- Day-use
- Hourly booking
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
- Facility Access

## Status

```text
Available
Reserved
Checked-in
Occupied
Checked-out
```

## 31.2 VIP Suite

- Block-hour rental
- Weekday rate
- Weekend rate
- Overtime
- Locker
- Shower
- Bar
- TV
- Karaoke
- Sound System
- Massage Room
- F&B Add-on

## 31.3 Meeting Room

- Meeting Room Master
- Capacity
- Layout
- Room Size
- Facilities
- Availability
- Booking
- Event Schedule
- Rate
- Package
- Additional Room Rental
- Equipment
- Catering
- Deposit
- Payment

## Layout

- Classroom
- U-Shape
- Theater
- Boardroom
- Round Table

---

# 32. P2 — Reservation Engine

## Resource Types

```text
Reservation
├── Golf / Tee Time
├── Sport Court
├── Class
├── Bungalow
├── VIP Suite
├── Meeting Room
├── Banquet Venue
└── Event
```

## Reservation Models

- Per time slot
- Per night
- Per hour / day-use
- Hour block + overtime
- Package per pax + duration
- Per class session
- 4x / 8x package

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

# 33. P2 — Non-Member Booking

## Supported

- Golf
- Sport Court
- Class
- Bungalow
- Meeting Room
- Banquet / Event inquiry

## Public Flow

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

# 34. P2 — Commercial Foundation / F&B

## POS

### Outlets

- Restaurant
- Pool Dining
- Clubhouse
- Bar
- Pro Shop
- Driving Range Counter
- Sport Club Reception
- Other Outlet

### Features

- Product
- Menu
- Modifier
- Variant
- Pricing
- Discount
- Promotion
- Order
- Kitchen Order
- KDS
- Split Bill
- Member Account Charge
- Payment
- Refund
- Shift
- Cashier
- Outlet
- Offline Mode
- Sync

## F&B Experience

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

---

# 35. P2 — BOM / Recipe

## Scope

- Recipe per Menu
- Recipe Line
- Sub-recipe
- Semi-finished item
- UOM conversion
- Yield
- Waste
- Modifier impact
- Combo / Package
- Automatic stock deduction
- Food Cost / COGS
- Theoretical Consumption
- Actual Consumption
- Production Order
- Store Requisition
- Shared recipe master

## BOM Use Cases

BOM berlaku untuk:

- Menu
- Golf tariff
- Meeting package
- Wedding package
- Birthday package
- Pre-wedding package
- Other service/package

---

# 36. P2 — Voucher & Prepaid

## Types

- Driving Range Ball Balance
- Sport Entry Voucher
- Class Package
- Court Package
- F&B Voucher
- Hole-in-One Voucher
- Gift Voucher
- Promo Voucher

## Features

- Issue
- Sell
- Redeem
- Expiry
- Remaining Balance
- Remaining Quota
- Transferability
- Expiry Notification
- Deferred Revenue
- Revenue Recognition on Usage
- Breakage

---

# 37. P2 — Customer Preferences & Personalization

## Scope

- Favorite Food / Drink
- Most Ordered
- Repeat Order
- Customer Preferences
- Golf Activity
- Sport Activity
- Booking behavior
- Transaction behavior
- Personalized customer context

---

# 38. P2 — CRM Foundation

## Customer 360

```text
Customer
├── Profile
├── Family / Corporate Relation
├── Membership
├── Golf Activity
├── Golf Booking
├── Sport Club Activity
├── Bungalow Booking
├── Meeting Booking
├── Banquet / Event
├── POS Transaction
├── Voucher / Prepaid
├── Payment
├── Feedback
├── Complaint
├── Campaign
└── Loyalty
```

## Foundation Features

- Customer Profile
- Customer History
- Interaction History
- Customer Segmentation
- Feedback
- Basic campaign foundation

---

# 39. P2 — Golf Operational KPI

## Dashboard

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
- RevPATT
- Member vs Guest
- Rounds per Member
- Caddy Utilization
- Golf Cart Utilization
- Driving Range Usage
- Golf Revenue

---

# 40. P3 — CRM & Sales Expansion

> Mapping Product Overview CRM & Sales dan Product Overview MVP Fase 3.

## 40.1 Lead Management

### Lead Sources

- WhatsApp
- Instagram
- Facebook
- TikTok
- Website Form
- Walk-in
- Member Referral

## 40.2 Sales Pipeline

Pipeline per:

- Wedding
- MICE / Meeting
- Corporate Membership
- Sport Membership
- Corporate Golf
- Tournament

## Features

- Lead Assignment
- Sales Assignment
- Sales Quotation
- Quotation → Booking / Banquet
- Corporate Account
- Sales Target
- Sales Commission
- Renewal Reminder
- Birthday Reminder
- Follow-up
- Marketing Campaign

---

# 41. P3 — Advanced Customer 360

## Segmentation

- Member Type
- Frequency
- Spend Value
- Age
- Residence
- Customer Type
- Corporate
- Outlet
- Other configurable segment

## Interaction

- WhatsApp
- Email
- Campaign
- Promotion
- Voucher
- Loyalty
- Feedback
- NPS
- Complaint
- SLA
- Escalation

---

# 42. P3 — Top Spender & Leaderboard

## Scope

- Monthly ranking
- Quarterly ranking
- Annual ranking
- Golf spend
- F&B spend
- Sport spend
- Bungalow spend
- Banquet spend
- Member filter
- Corporate filter
- Outlet filter
- Segment filter
- Most rounds
- Most active member

## Usage

- Loyalty tier foundation
- Reward foundation
- VIP invitation foundation

## Access

Internal sales and management only.

---

# 43. P3 — Loyalty

## Scope

- Loyalty account
- Cross-outlet points
- Activity-based earning
- Redemption foundation
- Tier foundation
- Reward foundation
- Customer engagement

---

# 44. P3 — Pricing & Promotion Engine

## Pricing Dimensions

### Segment

- Member
- Guest
- Guest of Member
- Non-Member
- Reciprocal
- Senior
- Ladies
- Junior
- Student
- Child
- Residence
- Corporate

### Day Type

- Weekday
- Weekend
- Public Holiday
- Custom day type
- Sen–Kam
- Jumat
- Sabtu
- Minggu / PH
- Mon–Fri
- Sat–Sun / PH

### Time Band

- AM
- PM
- Night
- 07–16
- 16–21

### Quantity / Duration

- Hour
- Block
- Pax
- Session
- 4x package
- 8x package

### Validity

- Effective Date
- End Date

## Pricing Rules

- Member Rate
- Guest Rate
- Guest of Member Rate
- Non-Member Rate
- Reciprocal Rate
- Senior Rate
- Ladies Rate
- Junior Rate
- Student Rate
- Child Rate
- Residence Rate
- Corporate Rate
- Weekday Rate
- Weekend Rate
- Custom Day Rate
- Time Band Rate
- Peak Rate
- Off-Peak Rate
- Holiday Rate
- Package Rate
- Nett
- ++

## Promotion

- Happy Hour
- Buy N Get X / Buy N Price X
- Bundle
- Member Discount
- Voucher
- Promo Code
- Period-based Promotion

---

# 45. P3 — Package Management

## Golf Package

- Tee Time
- Caddy
- Golf Cart
- Lunch

## Stay & Golf

- Bungalow
- Tee Time
- Caddy
- Breakfast

## Corporate Package

- Meeting Room
- Bungalow
- Golf
- Catering

## Wedding Package

- Ballroom
- Buffet
- VIP Family
- Food Stall
- Family Room
- Bungalow
- Food Tasting
- Technical Meeting

## Package Engine

- Component selection
- Resource bundling
- Pricing
- Pax
- Duration
- BOM
- Inventory impact
- Reservation allocation
- Billing

---

# 46. P3 — Event Management

## Event Types

- Corporate Gathering
- Golf Tournament
- Wedding
- Birthday
- Kids Birthday
- Family Gathering
- Company Outing
- Seminar
- Meeting
- Community Event
- Social Event

## Features

- Event Creation
- Event Type
- Event Schedule
- Venue
- Participants
- Guest Registration
- Package
- Catering
- Equipment
- Vendor
- Event Checklist
- Event Payment
- Event Report

## Event Structure

```text
Event
├── Venue
├── Schedule
├── Participants
├── Package
├── Catering
├── Equipment
├── Vendor
├── Bungalow
├── Golf Activity
└── Billing
```

---

# 47. P3 — Banquet, MICE & Wedding

## Sales Flow

```text
Inquiry
  ↓
Quotation
  ↓
Package & Menu
  ↓
BEO / Function Sheet
  ↓
DP
  ↓
Food Tasting & Technical Meeting
  ↓
Event
  ↓
Final Billing
```

## Scope

- Inquiry
- Quotation
- Package
- Menu Selection
- Pax
- Minimum Pax
- Menu Category Quota
- Buffet
- Food Stall
- Corkage
- Outdoor Venue Add-on
- Resource Bundling
- Ballroom
- Family Room
- Bungalow
- Golf Cart
- F&B Voucher
- Electricity Quota
- Payment Schedule
- Payment Reminder
- BEO
- BOM per pax
- Procurement requirement
- CRM Sales Pipeline
- Final Billing

## Payment Rules

- DP minimum
- Payment due date
- Example H-7 final settlement
- Automatic reminder

---

# 48. P3 — Tournament Management

## Core Tournament

- Tournament Creation
- Tournament Schedule
- Registration
- Participant Management
- Flighting
- Tee Assignment
- Shotgun Start
- Scoring
- Leaderboard
- Tournament Package
- Tournament Fee
- Sponsor
- Prize
- Tournament Report

## Integration

Tournament is an Event type with scoring capability.

---

# 49. P3 — Billing & Payment Expansion

## Unified Folio

All customer transactions feed one folio:

```text
Golf
Sport Club
Class
Bungalow
VIP Suite
Meeting
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

## Features

- Customer Folio
- Cross-line billing
- Member signing bill
- Monthly statement
- Split Bill
- Deposit
- DP
- Installment / Payment Term
- Refund
- Cashier Shift
- End-of-Day
- Night Audit
- Corporate AR Billing

## Financial Evidence

- Pricing Snapshot
- Payment
- Refund
- Adjustment
- Membership Charge
- Folio
- Final Settlement

---

# 50. P4 — Inventory & BOM

> Mapping Product Overview Inventory, BOM, Asset & Equipment.

## Inventory Coverage

- F&B
- Golf
- Pro Shop
- Sport Club
- Bungalow
- Event / Banquet
- Maintenance
- General Supplies

## Features

- Item Master
- Item Category
- UOM
- UOM Conversion
- Barcode
- Warehouse
- Stock Location
- Stock Balance
- Stock Movement
- Store Requisition
- Stock Transfer
- Stock Adjustment
- Stock Opname
- Stock Valuation
- Average Cost
- FIFO
- Minimum Stock
- Par Stock
- Reorder Point
- Automatic PR
- Batch Number
- Serial Number
- Expiry
- Waste / Spoilage
- BOM / Recipe
- Production
- Inventory Journal
- COGS

## Inventory Structure

```text
Main Store
├── Kitchen
├── Bar & Beverage
├── Pro Shop
├── Golf Ops
├── Sport Club
├── Bungalow
├── Engineering / Maintenance
└── General
```

## Asset & Equipment

- Buggy fleet
- Gym equipment
- Rental equipment
- Golf clubs
- Rackets
- Maintenance schedule
- Usage history

---

# 51. P4 — Procurement

## Procurement Flow

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

## Features

- Supplier Master
- Purchase Requisition
- Manual PR
- Reorder-based PR
- Banquet material PR
- Approval
- RFQ
- Vendor Quotation
- Purchase Order
- Goods Receipt
- Purchase Return
- Vendor Invoice
- Vendor Performance
- Procurement Report

## 3-Way Matching

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

# 52. P4 — Accounting

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

- Revenue Allocation
- Green Fee
- Caddy Fee
- Buggy Fee
- HIO
- Tax
- Deferred Revenue
- Membership Fee
- Voucher
- Prepaid
- Deposit / DP
- Breakage
- Tax configuration
- Caddy fee liability
- Service charge distribution
- e-Faktur / Coretax

## Financial Reports

- Profit & Loss
- Balance Sheet
- Cash Flow
- Trial Balance
- AR Aging
- AP Aging
- Revenue by Business Line

## Transition

Sebelum Accounting live:

```text
Operational Platform
      ↓
Accounting Export
      ↓
Existing Accounting System
```

Setelah Accounting live:

```text
Operational Platform
      ↓
Accounting
      ↓
Financial Reports
```

---

# 53. P4 — Landing Page & Digital Channel

## Public Pages

- Home
- Golf
- Course
- Hole by Hole
- Handicap Index
- Facilities
- Reciprocal
- Sport Club
- Bungalow
- VIP Suite
- Meeting Room
- MICE
- Wedding
- Banquet
- Event
- Membership
- Package
- Promotion / What's On
- Hall of Fame
- News
- Article
- Gallery
- Contact
- Location

## Online Booking

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

## Inquiry

Wedding & Banquet inquiry → CRM Lead.

## CMS

- Page Content
- Banner
- Image
- Package
- Promotion
- Structured Pricing
- Event
- News
- Gallery
- Course Guide
- Contact Information
- ID / EN bilingual content

---

# 54. P4 — Integration Layer

## Payment

- QRIS
- Virtual Account
- Card
- Payment Gateway

## Communication

- WhatsApp Business API
- Email

## Tax

- e-Faktur / Coretax

## External Data

- Modernland residence verification
- Rhapsody migration

## Hardware

- Locker
- Access / Turnstile
- Ball Dispenser
- Golf Cart GPS
- Other supported operational hardware

## Accounting

- Accounting integration
- Financial export during transition

---

# 55. P5 — HRIS & Payroll

> Product Overview memindahkan HR/Payroll ke dalam scope, dengan keputusan terbuka mengenai full payroll vs operational HR + third-party payroll.

---

# 56. P5 — Core HR

## Organization

- Department
- Organization Structure
- Golf
- Sport Club
- F&B
- Banquet
- Bungalow
- Sales
- Finance
- Engineering

## Employee

- Employee Profile
- Contract
- PKWT
- PKWTT
- Documents
- Recruitment
- Training
- Certification
- Performance Review

## Certifications

- Lifeguard
- Caddy
- Food Handler
- Other configurable certification

## Employee Self Service

- Mobile ESS
- Payslip
- Leave
- Attendance

---

# 57. P5 — Attendance & Workforce

## Attendance

- Fingerprint
- Face Recognition
- Mobile GPS

## Scheduling

- Shift
- F&B
- Reception
- Lifeguard
- Starter
- Other operational teams

## Leave & Overtime

- Overtime
- Leave
- Permission

---

# 58. P5 — Payroll

## Scope

- Salary
- Allowance
- Overtime
- PPh 21
- BPJS Kesehatan
- BPJS Ketenagakerjaan
- THR
- Service Charge Distribution
- Sales Commission
- Bonus

## Non-Employee Workforce

### Caddy

- Attendance
- Rotation
- Certification
- Rating
- Caddy Fee
- Tip

### Instructor

- Teaching Schedule
- Session Honor
- Per Student Honor

---

# 59. P5 — Advanced CRM & Loyalty

## Scope

- Advanced Customer Segmentation
- Cross-business behavior
- Loyalty tiers
- Loyalty reward
- Reward eligibility
- Campaign automation
- Renewal automation
- Birthday automation
- VIP segmentation
- Top Spender driven engagement
- NPS analytics
- Complaint SLA analytics
- Sales performance
- Sales commission analytics

---

# 60. P5 — Management Dashboard & BI

## Golf

- Golf Rounds
- Tee Time Utilization
- RevPATT
- Member vs Guest
- Rounds per Member
- Caddy Utilization
- Golf Cart Utilization
- Driving Range Usage
- Golf Revenue

## Sport Club

- Court Utilization
- Entry per Day
- Class Enrollment
- Attendance
- Sport Revenue

## Membership

- Active Members
- New Members
- Expiring Membership
- Renewal
- Member Activity
- Member Revenue

## Booking

- Golf Booking
- Sport Booking
- Bungalow Booking
- Bungalow Occupancy
- Meeting Booking
- Event Booking
- Cancellation
- No-show
- Booking Revenue

## Banquet

- Inquiry
- Quotation
- Deal
- Event Count
- Pax
- Revenue
- Outstanding DP
- Outstanding Settlement

## Commercial

- Total Sales
- POS Sales
- Package Sales
- Voucher Sold
- Voucher Redeemed
- Average Transaction
- Outlet Performance
- Food Cost %

## Inventory

- Inventory Value
- Stock Balance
- Low Stock
- Stock Movement
- Stock Opname
- Theoretical vs Actual Variance

## Procurement

- PR
- PO
- Outstanding PO
- Purchase Value
- Vendor Performance

## Finance

- Revenue by Business Line
- AR
- AP
- Cash
- Outstanding Payment
- Deferred Revenue
- P&L

## CRM

- Leads
- Conversion
- Active Customers
- Member Activity
- Campaign Performance
- Loyalty
- Top Spender

## HR

- Headcount
- Attendance
- Overtime
- Payroll Cost
- Caddy Attendance
- Caddy Rating

---

# 61. P5 — Advanced Package Management

## Scope

- Multi-business package
- Resource bundle
- Package component
- Capacity
- Pax
- Duration
- Time block
- Overtime
- BOM
- Inventory requirement
- Reservation allocation
- Payment schedule
- Revenue allocation
- Package profitability

---

# 62. P5 — Advanced Tournament

## Scope

- Tournament lifecycle
- Advanced registration
- Advanced flighting
- Shotgun start
- Scoring
- Leaderboard
- Sponsor
- Prize
- Tournament package
- Tournament billing
- Tournament reporting
- Historical tournament
- Hall of Fame integration

---

# 63. P6 — SaaS Commercialization

> Mapping Product Overview MVP Fase 3 SaaS commercialization.

## 63.1 Automated Customer Instance Provisioning

- Instance creation
- Default configuration
- Module activation
- Feature activation
- Seed master data
- Branding setup
- Initial admin
- Venue setup
- Course setup
- Integration setup

## 63.2 Subscription

- Subscription plan
- Subscription lifecycle
- Billing
- Trial
- Activation
- Suspension
- Renewal
- Cancellation

## 63.3 Feature Tier

- Module tier
- Feature tier
- Usage-based capability foundation
- Customer-level feature flags

## 63.4 White Label

- Branding
- White-label build automation
- Logo
- Theme
- Custom domain
- Public website configuration
- Customer-specific application reference

## 63.5 Enterprise Options

- Multiple Payment Providers
- Enterprise SSO
- Dedicated Database Option
- Advanced integration configuration

---

# 64. P7 — Advanced Experience & Intelligence

> Capability lanjutan mengikuti GolfOne alignment dan expansion direction Product Overview.

## Golf Intelligence

- Pace-of-Play intelligence
- Operational anomaly detection
- Queue optimization
- Tee utilization optimization
- Caddy utilization intelligence
- Golf cart utilization intelligence

## Customer Intelligence

- Personalization
- Recommendation
- Customer behavior insights
- Churn-risk signal
- Loyalty optimization
- VIP opportunity detection

## Revenue Intelligence

- Dynamic pricing optimization
- Revenue per available tee time
- Package profitability
- Outlet profitability
- Customer lifetime value
- Promotion effectiveness

## Operational Intelligence

- Inventory demand prediction
- Procurement planning
- Banquet material planning
- Workforce planning
- Capacity planning

## Exception & Recovery

- Operational exception detection
- Recovery workflow
- Auditability
- Escalation
- Notification

---

# 65. Cross-Phase Policy & Configuration

Policy configuration harus tersedia sebagai shared capability dan diperluas mengikuti modul.

## Golf Policy

- Tee Time Interval
- Tee Sheet Template
- Maximum Players
- Minimum Players
- Booking Window
- Booking Cut-off
- Member Priority
- Guest Policy
- Reciprocal Policy
- Caddy Policy
- Golf Cart Policy
- Buggy Sharing
- Buggy Surcharge
- Cancellation Policy
- No-show Policy
- Rain Check
- Weather Policy
- Refund Policy
- Dress Code
- Club Rules

## Sport Club Policy

- Facility Operating Hours
- Guest Access
- Member Guest Rule
- Child Age Limit
- Class Quota
- Class Cancellation
- Package Validity

## Banquet Policy

- Minimum DP
- Settlement Due Date
- Corkage Fee
- Minimum Pax

## Pricing Policy

- Member
- Guest
- Guest of Member
- Non-Member
- Reciprocal
- Senior
- Ladies
- Junior
- Student
- Child
- Residence
- Corporate
- Weekday
- Weekend
- Custom Day Type
- AM
- PM
- Night
- Peak
- Off-Peak
- Holiday
- Package
- Nett
- ++

---

# 66. Application Roadmap

## 66.1 Website + Online Booking

### P1

- Public shell
- Golf information
- Golf booking
- Guest booking
- Payment
- Confirmation

### P2

- Sport Club
- Bungalow
- Meeting Room
- Package
- Membership
- Hall of Fame

### P3

- Wedding inquiry
- Banquet inquiry
- Event
- Promotion
- CRM integration

### P4

- Full CMS
- Structured pricing
- Bilingual
- News
- Gallery
- Course guide

---

## 66.2 Member & Guest App

### P1

- Login
- Golf booking
- Tee time
- Player
- Caddy / cart
- Payment
- Booking history

### P2

- Digital member card
- Sport booking
- Class booking
- Bungalow booking
- Voucher
- Prepaid
- Score
- Hall of Fame
- Loyalty
- Personalization

### P3–P5

- Full transaction
- CRM interaction
- Loyalty
- Campaign
- Personalized offers

---

## 66.3 Back Office

### P0

- IAM
- Settings
- Master data

### P1

- Golf
- Membership
- Booking
- Customer
- Check-in
- Starter
- Caddy
- Cart

### P2

- Sport
- Stay
- F&B
- Voucher
- Reservation

### P3

- CRM
- Sales
- Pricing
- Promotion
- Package
- Event
- Banquet
- Tournament
- Billing

### P4

- Inventory
- Procurement
- Accounting

### P5

- HRIS
- Payroll
- Advanced BI
- Advanced CRM

---

## 66.4 Operational Interface & Caddy Tablet (area Staff App)

Domain perangkat (Tech Doc §6.1): Operational Interface di `cashier.<club>`, Caddy Tablet di `caddy.<club>`,
Kitchen Display di `kitchen.<club>`; semuanya login PIN per shift di perangkat terdaftar. Clubhouse Screen ada di
`dashboard.<club>` untuk role Screen. Fitur baru di P3–P6 yang dipakai di perangkat bersama masuk ke domain perangkat yang
sesuai (mis. banquet, warehouse di `cashier`), fitur kantor masuk ke `dashboard`.

### Operational Interface

- Starter
- Golf staff
- Caddy master
- Reservation
- Front desk
- Sport reception
- Banquet
- Warehouse
- POS
- Kitchen Display (domain `kitchen`)

### Caddy Tablet

- Assignment
- Round
- Player
- Customer context
- On-course operation
- Offline sync

---

## 66.5 Management Dashboard

Management dashboard covers:

- Executive KPI
- Golf KPI
- Sport KPI
- Membership KPI
- Booking KPI
- Banquet KPI
- Commercial KPI
- Inventory KPI
- Procurement KPI
- Finance KPI
- CRM KPI
- HR KPI

---

# 67. Domain Dependency Map

```text
P0 Platform
│
├── IAM
├── Customer Instance
├── Master Data
├── Notification
├── Audit
└── Integration
        │
        ▼
P1 Golf Core
│
├── Customer
├── Course
├── Tee Time
├── Flight
├── Booking
├── Membership Basic
├── Caddy Basic
├── Cart Basic
├── Check-in
├── Pricing
└── Payment
        │
        ▼
P2 Shared Operational Experience
│
├── Membership Lifecycle
├── Reservation Engine
├── Golf Experience
├── Caddy Lifecycle
├── Cart Lifecycle
├── Sport Club
├── Classes
├── Bungalow
├── VIP Suite
├── Meeting Room
├── F&B
├── BOM
├── Voucher
└── Customer Personalization
        │
        ▼
P3 Commercial & Customer Expansion
│
├── CRM
├── Sales
├── Pricing & Promotion
├── Package
├── Event
├── Banquet
├── Wedding
├── Tournament
└── Billing Expansion
        │
        ▼
P4 Enterprise Back Office
│
├── Inventory
├── Procurement
├── Accounting
├── Financial Reporting
├── Digital Channel CMS
└── Integrations
        │
        ▼
P5 Enterprise Management
│
├── HRIS
├── Payroll
├── Advanced CRM
├── Loyalty
├── Advanced BI
├── Advanced Tournament
└── Advanced Package
        │
        ▼
P6 SaaS
│
├── Provisioning
├── Subscription
├── Feature Tier
├── White Label
├── Custom Domain
├── SSO
└── Dedicated Database
        │
        ▼
P7 Intelligence
├── Revenue Intelligence
├── Customer Intelligence
├── Operational Intelligence
├── Golf Intelligence
└── Exception & Recovery
```

---

# 68. End-to-End Business Flow by Roadmap

## 68.1 Golf Member

```text
P1
Member
→ Login
→ Availability
→ Golf Booking
→ Tee Time
→ Players
→ Caddy / Cart
→ Payment / Member Charge
→ Confirmation
→ Check-in
→ Bag Drop
→ Locker
→ Starter
→ Golf

P2
→ Digital Score
→ Round History
→ Statistics
→ Hall of Fame

P3
→ Loyalty
→ Campaign
→ Personalization
```

---

## 68.2 Golf Non-Member

```text
P1
Landing Page
→ Golf
→ Date
→ Tee Time
→ Guest Data
→ Caddy / Cart
→ Payment
→ Confirmation
→ Check-in
→ Golf

P2
→ Score / Playing History

P3
→ CRM Customer Conversion
→ Promotion
→ Loyalty Eligibility
```

---

## 68.3 Sport Guest

```text
P2
Landing Page / Reception
→ Court / Class / Entry
→ Date & Time Band
→ Guest Data
→ Payment / Voucher / Package
→ Confirmation
→ Check-in
→ Facility Access
```

---

## 68.4 Corporate Event

```text
P3
Lead
→ CRM
→ Event Inquiry
→ Quotation
→ Package
→ Meeting Room
→ Bungalow
→ Golf
→ Catering
→ Booking
→ Deposit
→ Event
→ Final Billing
→ Accounting
```

---

## 68.5 Wedding

```text
P3
Inquiry
→ CRM
→ Sales Assignment
→ Quotation / Site Visit
→ Package / Menu
→ DP
→ Food Tasting
→ Technical Meeting
→ BEO
→ Settlement
→ Event
→ Final Billing
→ Accounting
```

---

# 69. Operational KPI Rollout

| Phase | KPI Focus |
|---|---|
| P1 | Booking, Players, Queue, Check-in, Caddy, Cart |
| P2 | Golf utilization, Rounds, Playing, Sport, Membership, Bungalow |
| P3 | Sales, CRM, Banquet, POS, Packages, Voucher |
| P4 | Inventory, Procurement, Finance |
| P5 | HR, Payroll, Loyalty, BI |
| P6 | SaaS usage, subscription, feature adoption |
| P7 | Optimization, prediction, intelligence |

---

# 70. Data Entity Rollout

## P0

- Organization
- Property
- User
- Role
- Employee
- Department
- Notification
- Approval
- Audit Log

## P1

- Customer
- Guest
- Member
- Membership Program
- Membership
- Membership Type
- Membership Package
- Eligibility Rule
- Golf Course
- Hole
- Tee Set
- Tee Sheet Template
- Tee Time
- Flight
- Player
- Caddy
- Golf Cart
- Locker
- Bag Storage
- Booking
- Price List
- Rate Rule
- Day Type
- Time Band
- Payment
- Customer Account
- Folio

## P2

- Golf Round
- Score
- Handicap
- Driving Range Bay
- Reciprocal Club
- Hole-in-One Record
- Hall of Fame Entry
- Facility
- Court
- Court Booking
- Class
- Class Schedule
- Class Enrollment
- Instructor
- Entry Ticket
- Bungalow
- Bungalow Type
- Rate Plan
- Bungalow Booking
- VIP Suite Booking
- Meeting Room
- Room Layout Capacity
- Meeting Room Booking
- Reservation
- Resource
- Availability Slot
- Voucher
- Prepaid Balance
- Voucher Redemption
- Product
- Menu
- Modifier
- Recipe / BOM
- Recipe Line
- POS
- Outlet
- Sales Order

## P3

- Event
- Event Participant
- Event Package
- Banquet Booking
- Banquet Package
- Menu Selection
- BEO
- Payment Schedule
- Promotion
- Package
- Lead
- Opportunity / Pipeline
- Sales Quotation
- Campaign
- Loyalty
- Customer Interaction
- Feedback
- Complaint Ticket
- Tournament

## P4

- Product Category
- Warehouse
- Inventory
- Stock Movement
- Store Requisition
- Asset
- Supplier
- Purchase Requisition
- RFQ
- Quotation
- Purchase Order
- Goods Receipt
- Vendor Invoice
- Chart of Account
- Journal
- Ledger
- AR
- AP
- Bank Account
- Deferred Revenue
- Revenue Allocation Rule
- Invoice
- Deposit

## P5

- Shift
- Attendance
- Payroll
- Employee Certification
- Training
- Performance Review

---

# 71. Fit-Gap Modern Golf Delivery Priority

Roadmap harus memperhatikan gap terhadap sistem existing Modern Golf.

| Area | Modern Golf Need | Roadmap |
|---|---|---|
| Sport Club | 10+ fasilitas, slot, class, training | P2 |
| Banquet & Wedding | Pax package, BEO, DP, H-7, corkage | P3 |
| HRIS | Attendance, shift, caddy, instructor, service charge | P5 |
| Voucher & Prepaid | Ball, entry, 4x/8x, expiry | P2 |
| Pricing | Segment × day type × time band, nett/++ | P3, foundation P1 |
| Membership | Multi-program, age, residence, family | P2 |
| POS | BOM, recipe, food cost, KDS | P2 |
| Golf | Locker, reciprocal, HIO, night golf, driving range, Hall of Fame | P1–P2 |
| CRM | Pipeline, WhatsApp leads, Top Spender | P3 |

---

# 72. Rhapsody Parity Roadmap

Capability yang harus dipastikan saat replacement/cutover:

## Golf Front Office

- Bag Drop
- Locker
- Flight
- Caddy
- Starter
- Check-in
- Tournament
- Scoring

## POS

- Product
- Order
- Payment
- Outlet
- Shift
- Cashier

## Membership

- Member
- Membership
- Member Card
- Member Account

## Back Office

- Operational reporting
- Master data
- Transaction history
- Financial handoff

## Migration

- Data extraction
- Mapping
- Validation
- Reconciliation
- Parallel run
- Cutover

---

# 73. Out of Scope

Capability berikut tetap berada di luar initial product scope:

- Golf Course Maintenance
- Course Agronomy
- Landscaping Management
- Full Hotel PMS Operations
- Housekeeping Operations
- Resort Engineering di luar asset buggy/equipment maintenance
- Resort Security Operations
- Laundry Management
- Full Hotel Accounting
- Advanced Menu Engineering
- Restaurant Table Reservation
- Smart Golf Course IoT

## Boundary Stay & Venue

Tetap tersedia sebagai:

- Bungalow booking inventory
- VIP Suite booking
- Meeting Room booking

Tanpa membangun full resort operational management pada fase awal.

---

# 74. Open Decisions & Roadmap Gates

Feature yang membutuhkan keputusan sebelum final configuration:

| # | Decision | Impacted Phase |
|---|---|---|
| 1 | Membership golf vs Sport Club terpisah atau unified | P1–P2 |
| 2 | Tipe membership yang berhak golf member rate | P1–P2 |
| 3 | Komponen harga all-in green fee/caddy/buggy/HIO | P1–P3 |
| 4 | Full HRIS/payroll vs operational HR + third party payroll | P5 |
| 5 | Modul Rhapsody dan data export | P1 |
| 6 | Hall of Fame public opt-in | P2 |
| 7 | Jumlah caddy, buggy, fee, tip | P1–P2 |
| 8 | Tournament calendar & golf academy/pro | P3/P5 |
| 9 | Outlet F&B & Pro Shop + consignment | P2/P4 |
| 10 | HIO insurance provider | P2 |
| 11 | Weekday/weekend tee pattern | P1 |
| 12 | Two-tee start / shotgun configuration | P1/P3 |

---

# 75. Release Strategy

## Release 0 — Foundation

```text
P0
Platform Foundation
```

Target: internal platform readiness.

## Release 1 — Golf Core MVP

```text
P0 + P1
```

Target: Modern Golf golf operational core.

## Release 2 — Golf Experience

```text
P0 + P1 + P2
```

Target: complete golf journey + shared Sport/Stay/F&B capability.

## Release 3 — Commercial Platform

```text
P0 + P1 + P2 + P3
```

Target: customer, sales, commercial, event, banquet, tournament.

## Release 4 — Enterprise Back Office

```text
P0–P4
```

Target: inventory, procurement, accounting, digital channel.

## Release 5 — Enterprise Management

```text
P0–P5
```

Target: HRIS, payroll, advanced CRM, loyalty, BI.

## Release 6 — SaaS Product

```text
P0–P6
```

Target: repeatable customer-instance commercialization.

## Release 7 — Intelligence

```text
P0–P7
```

Target: advanced optimization and intelligence.

---

# 76. Definition of Product Completion

Product dapat dianggap **enterprise-complete** ketika seluruh layer berikut tersedia:

```text
DIGITAL CHANNEL
├── Website
├── Online Booking
├── CMS
└── Public Content

CUSTOMER
├── Customer 360
├── Membership
├── Loyalty
├── CRM
└── Sales

GOLF
├── Course
├── Tee Time
├── Flight
├── Caddy
├── Golf Cart
├── Check-in
├── Starter
├── Score
├── Tournament
├── Driving Range
├── Reciprocal
├── HIO
└── Hall of Fame

SPORT
├── Facility
├── Court
├── Entry
├── Class
├── Instructor
└── Access

STAY & VENUE
├── Bungalow
├── VIP Suite
└── Meeting Room

EVENT
├── Event
├── Banquet
├── MICE
├── Wedding
└── Package

COMMERCIAL
├── POS
├── BOM
├── Pricing
├── Promotion
├── Voucher
└── Prepaid

BILLING
├── Customer Account
├── Folio
├── Invoice
├── Payment
├── Refund
├── Deposit
└── Settlement

BACK OFFICE
├── Inventory
├── Procurement
├── Accounting
└── HRIS / Payroll

PLATFORM
├── IAM
├── Multi-property
├── Notification
├── Approval
├── Integration
├── Audit
└── Configuration

MANAGEMENT
├── Dashboard
├── Reporting
└── BI
```

---

# 77. Final Roadmap Traceability

| Product Overview Domain / Capability | Roadmap |
|---|---|
| Product Ecosystem | P0 |
| Dedicated Customer Instance | P0 |
| Golf Operations | P1–P2 |
| Golf Booking | P1 |
| Tee Time | P1 |
| Flight | P1 |
| Caddy | P1–P2 |
| Golf Cart | P1–P2 |
| Course & Handicap | P1 |
| Check-in / Starter / Locker | P1 |
| Driving Range | P2 |
| Reciprocal Club | P2 |
| Hole-in-One | P2 |
| Tournament | P3/P5 |
| Golf Score | P2 |
| Hall of Fame | P2 |
| Sport Club & Facility | P2 |
| Classes & Training | P2 |
| Membership | P1–P2 |
| Member 360 | P1–P3 |
| Reservation Engine | P2 |
| Non-Member Booking | P1–P2 |
| Bungalow | P2 |
| VIP Suite | P2 |
| Meeting Room | P2 |
| Event Management | P3 |
| Banquet / MICE / Wedding | P3 |
| CRM & Sales | P2–P5 |
| Top Spender | P3 |
| Commercial / POS | P2–P3 |
| F&B Experience | P2 |
| BOM / Recipe | P2/P4 |
| Pricing & Promotion | P1–P3 |
| Package Management | P3/P5 |
| Billing & Payment | P1–P3 |
| Voucher & Prepaid | P2 |
| Inventory | P4 |
| Procurement | P4 |
| Accounting | P4 |
| Landing Page / Digital Channel | P1–P4 |
| HRIS & Payroll | P5 |
| Club Rules & Policy Configuration | P0–P5 |
| Customer Journey | P1–P5 |
| Management Dashboard | P2–P5 |
| User Roles | P0–P5 |
| Application Structure | P0–P5 |
| Product Architecture | P0–P7 |
| Tech Stack | P0 |
| Core Data Entities | P0–P5 |
| SaaS Commercialization | P6 |
| Out of Scope | Explicitly excluded |
| Product Positioning | Cross-phase |
| Open Decisions | Release gates |
| Modern Golf Fit-Gap | P1–P5 |
| GolfOne Alignment | P0–P7 |

---

# 78. Product Roadmap Summary

```text
P0  FOUNDATION
    │
    ├── Customer Instance
    ├── IAM / RBAC
    ├── Master Data
    ├── Audit
    ├── Notification
    └── Integration
    │
    ▼
P1  GOLF CORE MVP
    │
    ├── Customer
    ├── Membership Basic
    ├── Course / Route / Hole
    ├── Tee Time / Tee Sheet
    ├── Booking / Flight / Player
    ├── Pricing Snapshot
    ├── Payment
    ├── Caddy Basic
    ├── Cart Basic
    ├── Check-in / Bag Drop / Locker
    ├── Starter
    ├── Customer App
    ├── Staff Dashboard
    └── Rhapsody Migration
    │
    ▼
P2  COMPLETE GOLF + SHARED OPERATIONS
    │
    ├── Membership Lifecycle
    ├── Caddy Lifecycle
    ├── Cart Lifecycle
    ├── Digital Score
    ├── Playing Experience
    ├── Hall of Fame
    ├── Driving Range
    ├── Reciprocal
    ├── HIO
    ├── Sport Club
    ├── Classes
    ├── Bungalow
    ├── VIP Suite
    ├── Meeting Room
    ├── Reservation Engine
    ├── F&B / POS
    ├── BOM
    ├── Voucher / Prepaid
    └── CRM Foundation
    │
    ▼
P3  COMMERCIAL & BUSINESS EXPANSION
    │
    ├── CRM & Sales
    ├── Customer 360
    ├── Loyalty
    ├── Top Spender
    ├── Pricing / Promotion
    ├── Package
    ├── Event
    ├── Banquet
    ├── MICE
    ├── Wedding
    ├── Tournament
    └── Unified Billing
    │
    ▼
P4  ENTERPRISE BACK OFFICE
    │
    ├── Inventory
    ├── Asset
    ├── Procurement
    ├── 3-Way Matching
    ├── Accounting
    ├── AR / AP
    ├── Revenue Allocation
    ├── Deferred Revenue
    ├── Financial Reports
    ├── Digital CMS
    └── Enterprise Integrations
    │
    ▼
P5  PEOPLE & ADVANCED MANAGEMENT
    │
    ├── HRIS
    ├── Attendance
    ├── Payroll
    ├── Service Charge
    ├── Caddy / Instructor Workforce
    ├── Advanced CRM
    ├── Advanced Loyalty
    ├── Advanced BI
    ├── Advanced Tournament
    └── Advanced Package
    │
    ▼
P6  SAAS COMMERCIALIZATION
    │
    ├── Instance Provisioning
    ├── Subscription
    ├── Feature Tier
    ├── White Label
    ├── Custom Domain
    ├── Enterprise SSO
    └── Dedicated Database
    │
    ▼
P7  INTELLIGENCE & OPTIMIZATION
    │
    ├── Golf Intelligence
    ├── Revenue Intelligence
    ├── Customer Intelligence
    ├── Operational Intelligence
    ├── Workforce Intelligence
    └── Exception & Recovery
```

---

# 79. Roadmap Outcome

Roadmap ini menghasilkan satu platform dengan boundary berikut:

```text
                                ONECLUB
                               │
        ┌──────────────────────┼──────────────────────┐
        │                      │                      │
     GOLF CORE             SHARED CORE          BUSINESS LINES
     GolfOne              Services
        │                      │                      │
        │              ┌───────┼────────┐       ┌─────┼──────────┐
        │              │       │        │       │     │          │
        │         Membership Reservation Billing Sport Stay     Banquet
        │                       │        │       Club  & Venue  & Wedding
        │                       │        │
        └───────────────────────┼────────┘
                                │
                         COMMERCIAL / CRM
                                │
                    ┌───────────┼───────────┐
                    │           │           │
                   POS        CRM & Sales  Loyalty
                    │
                    ▼
              BACK OFFICE / ERP
                    │
          ┌─────────┼──────────┐
          │         │          │
      Inventory  Procurement Accounting
          │         │          │
          └─────────┼──────────┘
                    │
                 HRIS / Payroll
                    │
                    ▼
               MANAGEMENT & BI
                    │
                    ▼
               PLATFORM / IAM
```

**Final product direction:** satu enterprise platform yang menyediakan Golf Operations berbasis GolfOne sebagai operational core, shared customer/membership/reservation/billing/commercial services, business-line operations, enterprise back office, people management, management intelligence, dan SaaS customer-instance capability.
