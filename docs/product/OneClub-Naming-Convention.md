# OneClub
## Naming Convention — Module, Feature & Menu

**Version:** 1.0  
**Based on:** Product Overview v3  
**Date:** 3 October 2026

---

# 1. Purpose

Dokumen ini menjadi standar penamaan untuk:

- Module
- Sub-module
- Menu
- Feature
- Page
- Action

Naming convention digunakan secara konsisten pada:

- Staff App, dengan area (domain dalam kurung, Tech Doc §6.1):
  - Back Office (`dashboard`)
  - Management Dashboard (`dashboard`)
  - Platform Administration (`dashboard`)
  - Clubhouse Screen (`dashboard`, role Screen)
  - Operational Interface (`cashier`)
  - Caddy Tablet (`caddy`)
  - Kitchen Display (`kitchen`)
- Customer / Member Portal
- Landing Page / Digital Channel

Dokumen ini hanya mengatur **label yang tampil pada product interface**. Tidak mencakup database naming, API naming, coding convention, atau architecture naming.

---

# 2. General Naming Principles

## 2.1 Module

Module menggunakan **Business Domain Name**.

Format:

```text
[Business Domain]
```

Contoh:

```text
Golf
Sport Club
Membership
Booking
CRM
Inventory
Accounting
HRIS
```

Gunakan istilah yang mudah dipahami oleh user operasional.

---

## 2.2 Menu

Menu menggunakan **noun / business object / operational activity**.

Contoh:

```text
Tee Sheet
Bookings
Flights
Caddies
Golf Carts
Members
Customers
Invoices
Payments
Purchase Orders
Employees
Reports
```

Hindari penggunaan label yang terlalu teknis seperti:

```text
Booking Management Module
Customer Data Management
Golf Operational Transaction Processing
```

Gunakan:

```text
Bookings
Customers
Golf Operations
```

---

## 2.3 Feature

Feature menggunakan nama **business capability**.

Format:

```text
[Object / Action]
```

Contoh:

```text
Create Booking
Assign Caddy
Assign Golf Cart
Check-in Player
Process Payment
Issue Voucher
Approve Purchase Requisition
Generate Invoice
```

---

## 2.4 Action

Action menggunakan **verb + object**.

Standard verbs:

| Verb | Penggunaan |
|---|---|
| Add | Menambahkan data |
| Create | Membuat transaksi/entity |
| Edit | Mengubah data |
| View | Melihat detail |
| Assign | Menetapkan resource |
| Approve | Menyetujui |
| Reject | Menolak |
| Cancel | Membatalkan |
| Reschedule | Mengubah jadwal |
| Check-in | Memproses kedatangan |
| Check-out | Memproses keberangkatan |
| Confirm | Mengonfirmasi |
| Finalize | Mengunci transaksi |
| Refund | Mengembalikan pembayaran |
| Redeem | Menggunakan voucher |
| Transfer | Memindahkan |
| Submit | Mengirim untuk proses |
| Export | Mengekspor data |

---

# 3. Language Standard

Primary interface language:

**English**

Bahasa Indonesia dapat digunakan untuk:

- helper text
- description
- tooltip
- notification
- validation message
- operational instruction

Contoh:

```text
Menu:
Tee Sheet

Description:
Kelola jadwal tee time dan kesiapan flight.
```

---

# 4. Product-Level Naming

| Layer | Standard Label |
|---|---|
| Product | OneClub |
| Golf Core | Golf |
| Sport Business | Sport Club |
| Accommodation / Venue | Stay & Venue |
| Event Business | Banquet & Event |
| Membership | Membership |
| Booking Core | Booking |
| Commercial | Commercial |
| Customer | CRM |
| Back Office | Inventory |
| Procurement | Procurement |
| Finance | Accounting |
| People | HRIS |
| Analytics | Reports |
| System Administration | Settings |

---

# 5. Admin Dashboard

Struktur utama:

```text
Dashboard

Golf
Sport Club
Membership
Booking
Stay & Venue
Banquet & Event
CRM
Commercial
Inventory
Procurement
Accounting
HRIS
Reports
Settings
```

> `Commercial` menjadi parent menu untuk POS, Pricing, Promotion, Package, dan Voucher.

---

# 6. Golf

## Module

```text
Golf
```

## Menu

```text
Tee Sheet
Bookings
Flights
Players
Caddies
Golf Carts
Course
Check-in
Starter
Driving Range
Reciprocal Clubs
Tournaments
Scoring
Hall of Fame
Golf Settings
```

## Features

### Tee Sheet

```text
Tee Time
Tee Time Availability
Tee Sheet Template
Tee Time Hold
Queue
Dispatch Queue
Tee-Off
Round Status
```

### Booking

```text
Create Booking
Member Booking
Guest Booking
Non-Member Booking
Group Booking
Corporate Booking
Walk-in Booking
Booking Confirmation
Reschedule Booking
Cancel Booking
Rain Check
No-show
Booking History
```

### Flight

```text
Create Flight
Assign Player
Remove Player
Player Eligibility
Player Pricing
Player Entitlement
Flight Status
Flight History
```

### Caddy

```text
Caddy Master
Caddy Profile
Caddy Availability
Caddy Assignment
Caddy Rotation
Caddy Status
Caddy Fee
Caddy Tip
Caddy Attendance
Caddy Performance
Caddy History
```

### Golf Cart

```text
Golf Cart Master
Golf Cart Availability
Golf Cart Assignment
Golf Cart Readiness
Golf Cart Inspection
Golf Cart Usage
Golf Cart Maintenance
Golf Cart Fee
Golf Cart History
```

### Course

```text
Courses
Course Sections
Playing Routes
Route Holes
Tee Sets
Distance Markers
Course Maps
Panoramas
Hazards
Points of Interest
Course Availability
```

### Check-in

```text
Player Check-in
Bag Drop
Bag Storage
Locker Assignment
Starter Sheet
Flight Call
Marshal
Course Status
Weather Status
```

### Driving Range

```text
Range Facilities
Bay Assignment
Ball Sales
Prepaid Ball Balance
Ball Redemption
Range Usage
```

### Reciprocal Clubs

```text
Reciprocal Clubs
Reciprocal Rates
Member Verification
Introduction Letters
Reciprocal Visits
Club Settlement
```

### Tournaments

```text
Tournaments
Tournament Schedule
Registration
Participants
Flighting
Tee Assignment
Scoring
Leaderboard
Tournament Packages
Tournament Fees
Sponsors
Prizes
Tournament Reports
```

### Scoring

```text
Scorecard
Score Entry
Score Validation
Score Finalization
Score Correction
Round History
Score History
Round Statistics
Hole Progress
Hole Duration
Round Duration
```

### Hall of Fame

```text
Hole-in-One
Club Champions
Course Records
Albatross
Eagle
Tournament Champions
Player Achievements
```

---

# 7. Sport Club

## Module

```text
Sport Club
```

## Menu

```text
Facilities
Bookings
Classes
Instructors
Access
Lockers
Sport Club Reports
Sport Club Settings
```

## Features

### Facilities

```text
Facility Master
Courts
Swimming Pool
Gym
Studio
Spa
Sauna
Steam Room
Jacuzzi
```

### Bookings

```text
Facility Availability
Court Booking
Slot Booking
Time Band
Booking History
```

### Classes

```text
Class Programs
Class Schedule
Class Registration
Class Packages
Session Quota
Attendance
Class History
```

### Instructors

```text
Instructor Master
Instructor Schedule
Instructor Assignment
Instructor Attendance
Instructor Fee
```

### Access

```text
Facility Access
Entry Ticket
Guest Entry
Member Entry
Voucher Entry
Access History
```

### Lockers

```text
Locker Master
Locker Assignment
Locker Availability
Locker Status
Locker History
```

---

# 8. Membership

## Module

```text
Membership
```

## Menu

```text
Members
Membership Programs
Membership Types
Membership Packages
Applications
Approvals
Membership Fees
Membership Cards
Entitlements
Renewals
Membership History
Membership Settings
```

## Features

### Members

```text
Member Profile
Family Members
Corporate Nominees
Member Account
Member Statement
Member Privileges
Guest Privileges
Membership History
```

### Membership Application

```text
Membership Application
Eligibility Check
Document Verification
Membership Approval
Membership Fee
Membership Activation
```

### Membership Lifecycle

```text
Activate Membership
Renew Membership
Upgrade Membership
Downgrade Membership
Pause Membership
Resume Membership
Suspend Membership
Reactivate Membership
Cancel Membership
Expire Membership
```

### Member Card

```text
Member Card
Digital Member Card
Card Replacement
Card Replacement Fee
```

---

# 9. Booking

## Module

```text
Booking
```

## Menu

```text
All Bookings
Availability
Golf
Sport Club
Classes
Bungalow
VIP Suite
Meeting Rooms
Banquet & Event
Booking Calendar
Booking History
Cancellations
Refunds
Booking Settings
```

## Common Features

```text
Availability
Create Booking
Booking Detail
Customer
Date & Time
Rate
Package
Deposit
Payment
Confirmation
Reschedule
Cancellation
Refund
Booking History
```

---

# 10. Stay & Venue

## Module

```text
Stay & Venue
```

## Menu

```text
Bungalows
VIP Suites
Meeting Rooms
Availability
Reservations
Guest Check-in
Guest Check-out
Stay Reports
Stay Settings
```

## Bungalows

```text
Bungalow Master
Bungalow Types
Bungalow Availability
Bungalow Rates
Rate Plans
Day-use Booking
Guest Information
Check-in
Check-out
Cancellation
Deposit
Payment
Booking History
```

## VIP Suites

```text
VIP Suite Master
Availability
Block Booking
Overtime
Facilities
Add-ons
Booking History
```

## Meeting Rooms

```text
Meeting Room Master
Room Layouts
Room Capacity
Facilities
Availability
Room Booking
Event Schedule
Rates
Packages
Equipment
Catering
Deposit
Payment
```

---

# 11. Banquet & Event

## Module

```text
Banquet & Event
```

## Menu

```text
Events
Banquet
MICE
Weddings
Packages
Venues
BEO
Event Schedule
Event Checklist
Event Billing
Event Reports
```

## Event

```text
Event Creation
Event Type
Event Schedule
Venue
Participants
Guest Registration
Package
Catering
Equipment
Vendors
Event Checklist
Event Payment
Event Report
```

## Banquet

```text
Inquiry
Quotation
Package Selection
Menu Selection
BEO
Payment Schedule
Deposit
Food Tasting
Technical Meeting
Final Billing
```

## BEO

Use:

```text
Banquet Event Order
```

Avoid:

```text
Banquet Event Ordering
```

---

# 12. CRM

## Module

```text
CRM
```

## Menu

```text
Customers
Customer 360
Leads
Opportunities
Sales Pipeline
Quotations
Corporate Accounts
Interactions
Campaigns
Promotions
Loyalty
Top Spender
Feedback
Complaints
Follow-ups
CRM Reports
```

## Customer

```text
Customer Profile
Customer History
Customer Segmentation
Customer Preferences
Customer Interaction
Customer Transactions
Customer Bookings
Customer Memberships
```

## Sales

```text
Lead Management
Lead Assignment
Opportunity
Sales Pipeline
Quotation
Quotation Conversion
Sales Target
Sales Commission
```

## Customer Engagement

```text
Campaign
Promotion
Voucher
Loyalty
Birthday Reminder
Renewal Reminder
Feedback
NPS
Complaint Ticket
SLA
Escalation
Follow-up
```

---

# 13. Commercial

## Module

```text
Commercial
```

## Menu

```text
POS
Products
Menus
Orders
Outlets
Pricing
Promotions
Packages
Vouchers
Prepaid
BOM & Recipes
Commercial Reports
```

---

# 14. POS

## Menu

```text
POS
Orders
Cashier
Shifts
Outlets
Products
Menus
Modifiers
Kitchen
Transactions
Refunds
```

## Features

```text
Create Order
Add Item
Add Modifier
Apply Discount
Apply Promotion
Split Bill
Charge to Member
Process Payment
Refund Transaction
Open Shift
Close Shift
Offline Mode
Sync Transactions
```

---

# 15. Pricing & Promotion

## Menu

```text
Pricing
Rate Plans
Pricing Rules
Promotions
Discounts
Promo Codes
Effective Dates
Tax & Service
```

## Features

```text
Member Rate
Guest Rate
Guest of Member Rate
Non-Member Rate
Reciprocal Rate
Senior Rate
Ladies Rate
Junior Rate
Student Rate
Child Rate
Residence Rate
Corporate Rate
Weekday Rate
Weekend Rate
Time Band Rate
Peak Rate
Off-Peak Rate
Holiday Rate
Package Rate
Nett Pricing
Plus-Plus Pricing
```

---

# 16. Package

## Menu

```text
Packages
Package Types
Package Components
Package Pricing
Package Availability
Package History
```

## Features

```text
Create Package
Add Service
Add Product
Add Venue
Set Package Price
Set Package Availability
Package Booking
Package Consumption
Package History
```

---

# 17. Voucher & Prepaid

## Module

```text
Voucher & Prepaid
```

## Menu

```text
Vouchers
Prepaid Balances
Voucher Types
Issuance
Redemption
Transfer
Expiry
Voucher History
```

## Features

```text
Issue Voucher
Sell Voucher
Redeem Voucher
Transfer Voucher
Check Balance
Adjust Balance
Extend Expiry
View Usage History
```

---

# 18. Billing & Payment

## Module

```text
Billing & Payment
```

## Menu

```text
Customer Accounts
Folios
Invoices
Payments
Deposits
Refunds
Member Charges
Corporate Billing
Payment Reconciliation
Cashier
Night Audit
Billing Reports
```

## Features

```text
Create Folio
Add Charge
Split Bill
Apply Deposit
Generate Invoice
Process Payment
Apply Member Charge
Process Refund
Close Folio
Reopen Folio
View Payment History
```

## Payment Methods

```text
Cash
Bank Transfer
Virtual Account
QRIS
Card
Payment Gateway
Member Account
Voucher & Prepaid
```

---

# 19. Inventory

## Module

```text
Inventory
```

## Menu

```text
Items
Categories
UOM
Warehouses
Stock Locations
Stock Balance
Stock Movement
Store Requisition
Stock Transfer
Stock Adjustment
Stock Opname
Stock Valuation
Par Stock
Reorder Point
BOM & Recipes
Production
Assets & Equipment
Inventory Reports
```

## Features

```text
Create Item
Set UOM
Set Barcode
Receive Stock
Issue Stock
Transfer Stock
Adjust Stock
Perform Stock Opname
Set Par Stock
Set Reorder Point
Create BOM
Create Recipe
Create Production Order
Record Waste
Track Batch
Track Serial Number
Track Expiry
```

---

# 20. Procurement

## Module

```text
Procurement
```

## Menu

```text
Suppliers
Purchase Requisitions
Approvals
RFQ
Vendor Quotations
Purchase Orders
Goods Receipts
Purchase Returns
Vendor Invoices
Vendor Performance
Procurement Reports
```

## Features

```text
Create Purchase Requisition
Submit Requisition
Approve Requisition
Reject Requisition
Create RFQ
Record Vendor Quotation
Create Purchase Order
Approve Purchase Order
Receive Goods
Create Purchase Return
Record Vendor Invoice
Perform 3-Way Matching
```

---

# 21. Accounting

## Module

```text
Accounting
```

## Menu

```text
General Ledger
Accounts Receivable
Accounts Payable
Cash & Bank
Revenue & Tax
Financial Periods
Closing
Financial Reports
```

## General Accounting

```text
Chart of Accounts
Journal
General Ledger
Trial Balance
Financial Period
Period Closing
```

## Accounts Receivable

```text
Customer Invoices
Member Billing
Corporate Billing
Receivables
Payments
Outstanding
AR Aging
```

## Accounts Payable

```text
Vendor Invoices
Payables
Payments
AP Aging
```

## Cash & Bank

```text
Cash Accounts
Bank Accounts
Bank Transactions
Bank Reconciliation
```

## Revenue & Tax

```text
Revenue Allocation
Deferred Revenue
Voucher Breakage
Tax Configuration
Service Charge
Caddy Fee Liability
e-Faktur
```

## Financial Reports

```text
Profit & Loss
Balance Sheet
Cash Flow
Trial Balance
AR Aging
AP Aging
Revenue by Business Line
```

---

# 22. HRIS

## Module

```text
HRIS
```

## Menu

```text
Employees
Organization
Recruitment
Training & Certification
Attendance
Schedules
Leave & Permission
Overtime
Payroll
Benefits
Service Charge
Commissions
Caddy
Instructors
HR Reports
```

## Employee

```text
Employee Profile
Employment Contract
Employee Documents
Performance
Training
Certification
```

## Attendance

```text
Attendance
Shift Schedule
Overtime
Leave
Permission
Attendance History
```

## Payroll

```text
Payroll
Salary
Allowances
Overtime
PPh 21
BPJS
THR
Service Charge
Sales Commission
Bonus
Payslip
```

## Non-Employee

```text
Caddy
Caddy Attendance
Caddy Rotation
Caddy Fee
Caddy Tip

Instructor
Instructor Schedule
Instructor Sessions
Instructor Fee
```

---

# 23. Reports

## Module

```text
Reports
```

## Menu

```text
Golf Reports
Sport Club Reports
Membership Reports
Booking Reports
Banquet & Event Reports
CRM Reports
Commercial Reports
Inventory Reports
Procurement Reports
Financial Reports
HR Reports
Operational Reports
```

## Management Dashboard

```text
Executive Overview
Golf Performance
Sport Club Performance
Membership Performance
Booking Performance
Banquet Performance
Commercial Performance
Inventory Performance
Procurement Performance
Financial Performance
HR Performance
```

## Golf KPI Labels

```text
Today's Bookings
Today's Players
Current Queue
Players on Course
Pending Check-in
Available Caddies
Caddies on Round
Golf Carts Ready
Golf Carts in Use
Golf Carts in Maintenance
Average Check-in to Tee-Off Time
Golf Rounds
Tee Time Utilization
Revenue per Available Tee Time
Member vs Guest
Rounds per Member
Caddy Utilization
Golf Cart Utilization
Driving Range Usage
Golf Revenue
```

---

# 24. Settings

## Module

```text
Settings
```

## Menu

```text
Organization
Customer Instance
Venues
Courses
Users
Roles & Permissions
Features
Feature Configuration
Business Rules
Club Policies
Pricing Rules
Notifications
Integrations
Payment Methods
Tax & Service
Approval Workflows
Audit Logs
Localization
Branding
System Settings
```

## Platform Administration

```text
Customer Instances
Instance Configuration
Enabled Modules
Feature Configuration
Branding
Custom Domain
Users
Roles
Permissions
Integrations
Feature Flags
Locale
Currency
Timezone
```

---

# 25. Club Rules & Policy

Untuk policy yang spesifik terhadap golf club, gunakan label:

```text
Club Policies
```

Sub-menu:

```text
Golf Policies
Sport Club Policies
Banquet Policies
Pricing Policies
Cancellation Policies
Refund Policies
Guest Policies
Member Policies
Caddy Policies
Golf Cart Policies
Weather Policies
```

---

# 26. Landing Page / Digital Channel

## Public Navigation

```text
Home
Golf
Sport Club
Bungalow
VIP Suite
Meeting & MICE
Wedding & Banquet
Events
Membership
Packages
Promotions
Hall of Fame
News
Gallery
Contact
Location
```

## Golf

```text
Golf Course
Course Guide
Hole-by-Hole
Handicap
Facilities
Reciprocal Clubs
```

## Booking

```text
Book Golf
Book Sport Club
Book Bungalow
Book Meeting Room
Book Event
```

## CMS

```text
Pages
Banners
Images
Packages
Promotions
Pricing
Events
News
Gallery
Course Guide
Contact Information
Languages
```

---

# 27. Member / Guest Portal

Navigasi Member App mengikuti member journey **Book → Arrive → Play/Stay → Pay → History → Return** (keputusan product owner, 7 Okt 2026): enam menu utama; fitur lain berada di bawah menu tempatnya.

## Main Navigation

```text
Home
Book
My Activity
Membership
Transactions
Profile
```

## Book

```text
Tee Time
Bungalow
Meeting Room
Event
Tournaments
Sport Club
Order Food
Packages
Offers
```

## Book Tee Time (journey)

```text
Date & Time
Players
Caddy
Review
Payment
Confirmed
```

Caddy per player:

```text
No Caddy
Request Caddy
Preferred Caddy
No Preference
Pending Assignment
Assigned
Rate Caddy
```

## My Activity

```text
Bookings
Golf History
Stay History
My Events
My Tournaments
Feedback
```

## Membership

```text
Membership
Digital Member Card
Benefits
Guests
Family Members
Loyalty
Voucher & Prepaid
Fees & Requests
Membership Statement
Renew Membership
```

## Transactions

```text
My Transactions
Invoices
Payments
Member Charges
```

## Profile

```text
Profile
Preferences
Communication Preferences
Complaints
```

## Join Membership (sebelum login)

```text
Become a Member
Membership Packages
Registration
Submit Application
Track Application
Membership Fee
Membership Activated
```

---

# 28. Caddy Application

## Main Navigation

```text
Home
My Assignments
Current Round
Customer
Attendance
Earnings
History
Profile
```

## Features

```text
View Assignment
Accept Assignment
Start Round
View Player
Update Round Status
Record Customer Preference
Complete Round
View Caddy Fee
View Tip
Attendance
Assignment History
```

---

# 29. Staff Operational Interface

Operational interfaces menggunakan istilah sesuai pekerjaan:

### Starter

```text
Tee Sheet
Queue
Ready Flights
Dispatch
Check-in
Tee-Off
Round Status
```

### Caddy Master

```text
Caddy Queue
Caddy Availability
Caddy Assignment
Caddy Rotation
Caddy Attendance
Caddy History
```

### Front Desk

```text
Reservations
Check-in
Check-out
Guest
Payments
Folios
```

### Sport Reception

```text
Facility Availability
Bookings
Entry
Membership
Vouchers
Access
```

### POS

```text
Orders
Tables / Orders
Products
Payment
Shift
Cashier
```

### Warehouse

```text
Stock Balance
Receiving
Issuing
Transfer
Stock Opname
Store Requisition
```

---

# 30. Standard Terminology

Gunakan istilah berikut secara konsisten.

| Standard | Avoid |
|---|---|
| Customer | Client |
| Member | Membership Customer |
| Guest | Visitor |
| Non-Member | Non Member / Nonmember |
| Booking | Reservation untuk UI operasional |
| Reservation Engine | Booking Engine sebagai nama sistem |
| Tee Time | Tee Slot |
| Flight | Golf Group |
| Player | Golfer |
| Caddy | Caddie |
| Golf Cart | Buggy sebagai label utama |
| Course | Golf Course sebagai menu jika konteks sudah jelas |
| Playing Route | Course Route |
| Check-in | Check In |
| Check-out | Check Out |
| Folio | Customer Bill |
| Payment | Payment Transaction |
| Refund | Payment Reversal |
| Voucher | Coupon |
| Prepaid Balance | Prepaid Credit |
| Purchase Requisition | Purchase Request |
| Purchase Order | PO sebagai label menu |
| Goods Receipt | Receiving |
| Supplier | Vendor sebagai label utama |
| Customer 360 | Customer 360 Profile |
| Loyalty | Loyalty Program |
| Top Spender | Top Customer |
| Hall of Fame | Achievement |
| Golf Cart | Cart |
| Member Rate | Membership Rate |
| Guest of Member | Guest With Member |
| Weekday | Weekday |
| Weekend / Public Holiday | Weekend / Holiday |
| Time Band | Time Slot |
| Package | Package |
| Outlet | Outlet |
| Product | Product |
| Menu Item | Menu |
| Modifier | Modifier |
| BOM | Bill of Materials |
| Recipe | Recipe |
| Stock Opname | Stock Count |
| General Ledger | GL |
| Accounts Receivable | AR |
| Accounts Payable | AP |
| Human Resources Information System | HRIS |

---

# 31. Naming Rules for Status

Gunakan bentuk **Title Case** pada UI.

Standard:

```text
Draft
Pending
Confirmed
Approved
Rejected
Cancelled
Rescheduled
Checked-in
Checked-out
Available
Reserved
Occupied
Ready
Not Ready
In Use
Maintenance
Out of Service
Active
Inactive
Expired
Suspended
Finalized
Refunded
Completed
```

Untuk status internal/API, casing dapat mengikuti technical convention terpisah.

---

# 32. Naming Rules for Reports

Format:

```text
[Business Domain] + [Report Type]
```

Contoh:

```text
Golf Revenue Report
Tee Time Utilization Report
Caddy Utilization Report
Membership Revenue Report
Booking Revenue Report
Inventory Valuation Report
Procurement Performance Report
Accounts Receivable Aging Report
Payroll Cost Report
```

Untuk dashboard:

```text
Golf Performance
Membership Performance
Commercial Performance
Financial Performance
```

---

# 33. Naming Rules for Configuration

Gunakan:

```text
[Business Object] + Configuration
```

Contoh:

```text
Tee Time Configuration
Booking Configuration
Membership Configuration
Pricing Configuration
Payment Configuration
Voucher Configuration
Tax Configuration
Notification Configuration
Integration Configuration
```

Untuk rule:

```text
[Business Object] + Policy
```

Contoh:

```text
Cancellation Policy
Guest Policy
Caddy Policy
Golf Cart Policy
Refund Policy
Weather Policy
```

---

# 34. Naming Hierarchy

Struktur label yang digunakan dalam product:

```text
Product
└── Module
    └── Menu
        └── Sub-menu
            └── Feature
                └── Action
```

Contoh:

```text
Golf
└── Caddies
    └── Caddy Assignment
        └── Assign Caddy
```

Contoh lain:

```text
Membership
└── Members
    └── Membership Detail
        └── Membership Lifecycle
            ├── Renew Membership
            ├── Upgrade Membership
            ├── Pause Membership
            └── Cancel Membership
```

---

# 35. Final Standard

Untuk seluruh product, gunakan prinsip berikut:

```text
MODULE
= Business Domain

MENU
= Business Object / Operational Area

FEATURE
= Business Capability

ACTION
= Verb + Business Object

REPORT
= Business Domain + Report Type

CONFIGURATION
= Business Object + Configuration

POLICY
= Business Object + Policy
```

Contoh final:

```text
Golf
  └── Caddies
       └── Caddy Assignment
            └── Assign Caddy

Membership
  └── Members
       └── Membership Detail
            └── Renew Membership

Booking
  └── Golf
       └── Booking Detail
            └── Reschedule Booking

Inventory
  └── Stock Transfer
       └── Transfer Detail
            └── Confirm Transfer

Procurement
  └── Purchase Orders
       └── Purchase Order Detail
            └── Approve Purchase Order
```