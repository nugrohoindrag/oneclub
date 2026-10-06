# HRIS — Product Requirements & UI/UX Improvement Specification

**Product:** Golf & Resort Management System  
**Domain:** Human Resources Information System (HRIS)  
**Scope:** HR Manager / HR Back Office + Employee Self Service (ESS)  
**Status:** Improvement / Gap Analysis  
**Baseline:** Existing developed application  
**Reference:** Mekari Talenta HRIS patterns and agreed Golf & Resort architecture

---

## 1. Objective

Menyempurnakan modul HRIS agar menjadi **operational HR workspace**, bukan sekadar kumpulan report atau dashboard.

Sistem harus mendukung proses HR end-to-end untuk employee management, attendance, scheduling, leave, overtime, payroll, performance, employee services, approval, dan HR reporting.

Karena sistem **sudah dikembangkan**, seluruh requirement di dokumen ini diperlakukan sebagai **gap-analysis dan improvement requirement**, bukan instruksi untuk membangun ulang seluruh sistem.

### Mandatory Development Rule

Sebelum membuat atau mengubah UI, developer **WAJIB mengecek terlebih dahulu apakah backend, database, API, service, workflow, permission, dan business logic untuk fungsi tersebut sudah tersedia**.

Prioritas implementasi:

```text
Existing Backend / Logic
        ↓
Existing API
        ↓
Existing Database / Data Model
        ↓
Existing Workflow / Permission
        ↓
UI/UX Gap
        ↓
Implement / Improve UI
        ↓
Backend enhancement only if genuinely missing
```

**Dilarang membuat mock UI yang tidak terhubung dengan backend apabila backend sebenarnya sudah tersedia.**

---

# 2. Product Principles

## 2.1 Operational First

Setiap halaman HRIS harus memungkinkan user melakukan pekerjaan, bukan hanya melihat angka.

Contoh:

```text
Headcount
→ View Employees
→ Filter
→ Open Employee
→ Edit / Update
```

```text
Attendance Exception
→ Review
→ Correct
→ Approve
→ Save
```

```text
Pending Leave
→ Review
→ Approve / Reject
→ Record History
```

```text
Payroll
→ Prepare
→ Validate
→ Calculate
→ Review Exception
→ Approve
→ Post to Finance
```

## 2.2 UI Must Reflect Existing System Capability

Sebelum menyimpulkan sebuah fitur belum ada, lakukan pemeriksaan:

- database schema
- migrations
- backend services
- API routes
- controllers
- repositories
- business logic
- workflow
- RBAC / permissions
- existing frontend routes
- existing components
- existing seed/demo data
- integrations

Jika backend sudah ada tetapi UI belum menampilkan atau mengaksesnya, **prioritaskan UI integration/improvement**.

## 2.3 No Report-Only Screens

Report tetap diperlukan, tetapi report harus menjadi output dari operational transaction.

```text
Attendance
    ↓
Attendance Exceptions
    ↓
Correction / Approval
    ↓
Attendance Records
    ↓
Attendance Report
```

Bukan hanya halaman report yang tidak menyediakan action.

---

# 3. HRIS Information Architecture

```text
HRIS
│
├── People
│   ├── Employees
│   ├── Organization
│   ├── Positions & Job Levels
│   ├── Workforce Planning
│   ├── Employee Lifecycle
│   └── Employee Documents
│
├── Time & Attendance
│   ├── Attendance
│   ├── Work Schedules
│   ├── Shift & Roster
│   ├── Leave
│   ├── Overtime
│   └── Timesheets
│
├── Payroll
│   ├── Payroll Processing
│   ├── Payroll Components
│   ├── Payslips
│   ├── Tax & BPJS
│   ├── Payroll Adjustments
│   └── Payroll Reports
│
├── Performance
│   ├── Goals & KPI
│   ├── Performance Review
│   ├── 360° Feedback
│   ├── Development Plan
│   └── Succession
│
├── Employee Services
│   ├── Reimbursement
│   ├── Cash Advance
│   ├── Employee Loans
│   ├── Benefits
│   └── HR Requests
│
└── HR Reports
    ├── Headcount
    ├── Attendance
    ├── Overtime
    ├── Leave
    ├── Payroll
    ├── Turnover
    └── HR Analytics

Global:
├── Approvals
├── Notifications
├── Settings
└── Employee Self Service
```

### Explicitly Out of Scope

Untuk scope HRIS ini, **jangan membuat module baru untuk**:

- Recruitment / ATS
- Productivity Management
- Task / Project Management
- Employee Activity Monitoring
- Accounting Ledger
- General Ledger
- Accounts Payable
- Cash & Bank

Financial transactions tetap terintegrasi ke domain **Finance & Accounting**.

---

# 4. Backend-First Audit Requirement

Sebelum development dilakukan, buat inventory hasil audit untuk setiap module.

| Layer | Yang harus dicek |
|---|---|
| Database | Table, relation, enum, status, migration |
| Backend | Service, controller, repository, domain logic |
| API | GET, POST, PUT/PATCH, DELETE, action endpoint |
| Workflow | Approval, submission, rejection, cancellation |
| Permission | Role, permission, scope/property access |
| Integration | Finance, payroll, attendance, notification |
| Audit | Created by, updated by, approved by, timestamps |
| Frontend | Existing page, route, component, form, table |
| Seed | Demo/seed data |
| Validation | Business rule dan validation |

Gunakan status:

- `EXISTS — READY`
- `EXISTS — UI GAP`
- `EXISTS — PARTIAL`
- `MISSING BACKEND`
- `MISSING UI`
- `NEEDS INTEGRATION`
- `NOT IN SCOPE`

Developer harus memperbaiki **UI GAP terlebih dahulu** jika backend sudah tersedia.

---

# 5. People

## 5.1 Employees

Employees adalah operational master untuk seluruh employee.

### Required capability

- Employee list
- Search
- Filter
- Sort
- Employee status
- Department
- Position
- Employment type
- Property / business unit
- Join date
- Contract end date
- Manager / supervisor
- Employee detail
- Create employee
- Edit employee
- Employee transfer
- Employee promotion
- Employee termination
- Employee history
- Employee documents

### Employee Detail

Minimal:

```text
Personal Information
Employment Information
Organization
Position
Payroll Information
Tax Information
Attendance
Leave
Overtime
Reimbursement
Loan
Benefits
Documents
Assets
History
```

Employee detail harus menjadi operational workspace dengan action seperti Edit, Transfer, Promote, Change Position, Change Manager, Extend Contract, Terminate, Upload Document, dan View History.

---

# 6. Organization

Manage organizational structure.

### Required

- Organization
- Property
- Department
- Division
- Team
- Position
- Job level
- Reporting line
- Manager assignment

Relationship:

```text
Organization
   ↓
Property
   ↓
Department
   ↓
Team
   ↓
Position
   ↓
Employee
```

Untuk multi-property, permission harus mengikuti organizational scope.

---

# 7. Positions & Job Levels

### Required

- Position master
- Job level
- Job title
- Department
- Reporting manager
- Position status
- Salary range
- Required headcount
- Current occupancy
- Job description

Position harus dapat digunakan oleh Employee, Workforce Planning, Payroll, Performance, dan Approval Workflow.

---

# 8. Workforce Planning

Karena Golf & Resort memiliki kebutuhan manpower yang berubah berdasarkan weekend, holiday, occupancy, tournament, event, dan seasonality, Workforce Planning harus operational.

### Required

- Workforce requirement
- Required headcount
- Available headcount
- Gap
- Department
- Position
- Shift
- Date / period
- Property
- Planning scenario

Contoh:

```text
Housekeeping
Required: 35
Available: 29
Gap: 6
```

User harus dapat Create Plan, Adjust Requirement, Assign Employee, Review Gap, dan Export Planning Result.

---

# 9. Employee Lifecycle

Lifecycle:

```text
Onboarding
    ↓
Active
    ↓
Transfer / Promotion
    ↓
Contract Extension
    ↓
Offboarding / Termination
```

### Required

- Lifecycle event
- Effective date
- Reason
- Previous value
- New value
- Approval
- Supporting document
- Audit history

Perubahan penting tidak boleh overwrite tanpa history.

---

# 10. Employee Documents

### Required

- Document type
- Document number
- Issue date
- Expiry date
- Attachment
- Status
- Employee relation
- Verification status

Support expiry monitoring untuk Contract, ID, Certification, License, Medical Document, dan Training Certificate.

---

# 11. Time & Attendance

Ini adalah operational core untuk Golf & Resort.

## 11.1 Attendance

### Required

- Daily attendance
- Check-in
- Check-out
- Late
- Early leave
- Absent
- Missing attendance
- Attendance correction
- Attendance exception
- Approval
- Attendance history

### UI requirement

Table harus menyediakan action:

```text
Review
Correct
Approve
Reject
View Detail
```

---

# 12. Work Schedules

### Required

- Schedule master
- Working hours
- Break
- Working days
- Effective period
- Department
- Position
- Property

Schedule harus digunakan oleh Attendance dan Payroll.

---

# 13. Shift & Roster

Golf & Resort membutuhkan shift-based workforce.

### Required

- Shift master
- Roster
- Employee assignment
- Shift swap
- Shift change
- Open shift
- Coverage
- Conflict detection
- Approval

Contoh:

```text
Morning     06:00–14:00
Afternoon   14:00–22:00
Night       22:00–06:00
```

Roster UI harus memungkinkan HR/Manager melakukan assignment secara langsung.

---

# 14. Leave

### Required

- Leave type
- Leave balance
- Leave request
- Approval
- Rejection
- Cancellation
- Supporting document
- Leave calendar
- Leave history

Workflow:

```text
Employee
   ↓
Leave Request
   ↓
Manager Approval
   ↓
HR Validation
   ↓
Approved
   ↓
Leave Balance Updated
```

---

# 15. Overtime

### Required

- Overtime request
- Actual overtime
- Approval
- Overtime calculation
- Overtime rate
- Overtime history
- Payroll integration

Workflow:

```text
Overtime Request
       ↓
Approval
       ↓
Actual Overtime
       ↓
Payroll
```

---

# 16. Timesheets

Timesheet digunakan bila diperlukan untuk mencatat actual working time pada pekerjaan/operasional tertentu.

### Required

- Employee
- Date
- Work period
- Hours
- Activity / assignment
- Status
- Approval

Timesheet tidak boleh berkembang menjadi Productivity Management.

---

# 17. Payroll

Payroll merupakan HR operational module yang terhubung ke Finance & Accounting.

## 17.1 Payroll Processing

### Required

- Payroll period
- Payroll run
- Employee selection
- Attendance input
- Overtime input
- Leave impact
- Allowance
- Deduction
- Tax
- BPJS
- Loan deduction
- Reimbursement
- Adjustment
- Payroll validation
- Payroll approval
- Payroll finalization

Workflow:

```text
Open Period
   ↓
Prepare Payroll
   ↓
Calculate
   ↓
Validate
   ↓
Resolve Exceptions
   ↓
Review
   ↓
Approve
   ↓
Finalize
   ↓
Generate Payslip
   ↓
Post Payroll Journal to Finance
```

---

# 18. Payroll Components

### Required

- Basic salary
- Allowance
- Overtime
- Bonus
- Deduction
- Loan
- Tax
- BPJS
- Other payroll components

Setiap component dengan financial impact harus memiliki accounting mapping.

Contoh:

```text
Basic Salary
→ Salary Expense Account

Overtime
→ Overtime Expense Account

Employer BPJS
→ Employee Benefit Expense Account
```

---

# 19. Payroll → Finance & Accounting Integration

Payroll tidak memiliki General Ledger sendiri.

```text
HRIS Payroll
      ↓
Payroll Result
      ↓
Payroll Journal
      ↓
Finance & Accounting
      ↓
General Ledger
```

Finance menerima payroll journal, payroll payable, employee payable, tax payable, BPJS payable, payroll expense allocation, dan cost center allocation.

### Integration status

Payroll UI harus menunjukkan:

```text
Draft
Calculated
Approved
Posted to Finance
Posting Failed
```

Jika posting gagal, user harus dapat View Error, Retry, View Journal Reference, dan View Posting History.

---

# 20. Payslips

### HR/Payroll

- Generate payslip
- View payslip
- Download PDF
- Publish payslip
- Re-generate if permitted
- View distribution status

### Employee

Employee dapat melihat payslip melalui ESS.

---

# 21. Tax & BPJS

### Required

- Employee tax profile
- PTKP
- Tax calculation
- PPh 21 data
- BPJS configuration
- Employer contribution
- Employee contribution
- Payroll tax output
- Tax adjustment

Financial posting tetap ke Finance & Accounting.

---

# 22. Payroll Adjustments

Untuk exception yang ditemukan setelah payroll preparation.

### Required

- Adjustment type
- Employee
- Amount
- Reason
- Supporting document
- Approval
- Accounting mapping
- Audit trail

Tidak boleh mengubah payroll history secara silent.

---

# 23. Performance

Performance tetap berada di HRIS.

## Goals & KPI

- Goal
- KPI
- Target
- Weight
- Period
- Employee
- Department
- Position
- Achievement

## Performance Review

```text
Set Goal
   ↓
Monitor
   ↓
Self Assessment
   ↓
Manager Review
   ↓
Final Review
   ↓
Result
```

## 360° Feedback

- Reviewer
- Reviewee
- Criteria
- Feedback
- Score
- Confidentiality

## Development Plan

- Development goal
- Training need
- Development action
- Target date
- Progress

## Succession

- Critical position
- Successor
- Readiness
- Development gap

---

# 24. Employee Services

## Reimbursement

```text
Employee
 ↓
Submit
 ↓
Approval
 ↓
Finance
 ↓
Payment
```

HRIS menyimpan request dan approval status. Finance menangani financial settlement.

Status:

```text
Draft
Submitted
Approved
Rejected
Sent to Finance
Paid
```

## Cash Advance

Required: request, amount, purpose, date, approval, settlement, outstanding balance.

Financial settlement:

```text
HRIS
 ↓
Approved Cash Advance
 ↓
Finance
 ↓
Payment
```

## Employee Loans

Required: loan request, principal, tenor, installment, start period, outstanding balance, approval, payroll deduction.

```text
Loan
 ↓
Approved
 ↓
Finance / Payroll
 ↓
Monthly Deduction
```

## Benefits

Required: benefit master, eligibility, enrollment, effective period, employer contribution, employee contribution, status.

Benefit financial impact dapat diteruskan ke Payroll/Finance.

## HR Requests

Centralized operational request workspace untuk employee data change, position change, transfer, promotion, contract extension, employment letter, document request, dan request HR lainnya.

Setiap request memiliki requester, request date, type, status, approver, approval history, attachment, dan audit trail.

---

# 25. HR Reports & Analytics

Reports adalah **secondary layer**, bukan primary workspace.

### Required

- Headcount
- Attendance
- Leave
- Overtime
- Payroll
- Turnover
- HR Analytics

Setiap report harus memiliki drill-down ke source transaction.

```text
Headcount Report
      ↓
Department
      ↓
Employee List
      ↓
Employee Detail
```

---

# 26. HR Dashboard

Dashboard HR Manager tidak boleh hanya berisi approval dan notification cards. Dashboard harus menjadi **operational control center**.

### KPI

- Total Headcount
- Present Today
- On Leave
- Absent
- Overtime
- Workforce Gap
- Payroll Status
- Contract Expiring
- Pending HR Requests

### Operational widgets

Attendance:

```text
Present
Late
Absent
Missing Check-in/out
```

Workforce:

```text
Required
Available
Gap
```

Employee Movement:

```text
New Joiners
Transfers
Promotions
Terminations
```

HR Attention:

```text
Attendance Exceptions
Contracts Expiring
Leave Requests Pending
Payroll Exceptions
Expired Employee Documents
```

Setiap item harus clickable menuju operational workspace terkait.

---

# 27. Approvals

Approval adalah centralized workflow layer untuk Leave, Overtime, Employee Change, Transfer, Promotion, Contract Extension, Reimbursement, Cash Advance, Loan, dan Payroll.

Approval harus mendukung:

- Approve
- Reject
- Request Revision
- View Detail
- Comment
- Attachment
- Approval history

---

# 28. Notifications

Notification digunakan untuk event/action dan harus deep-link ke transaction terkait.

Contoh:

- Leave request
- Overtime request
- Contract expiry
- Attendance exception
- Payroll approval
- Payroll posting result
- HR request

---

# 29. Employee Self Service

ESS adalah employee-facing interface.

```text
My Profile
My Attendance
My Schedule
My Leave
My Overtime
My Payslip
My Reimbursement
My Loan
My Benefits
My HR Requests
My Documents
```

Employee hanya dapat mengakses data miliknya sesuai permission.

---

# 30. Finance Integration Boundary

HRIS hanya mengelola HR domain.

```text
HRIS
├── Employee
├── Attendance
├── Leave
├── Overtime
├── Payroll
├── Reimbursement
├── Loan
└── Benefits
        │
        ▼
Finance & Accounting
├── Accounts Payable
├── Cash & Bank
├── General Ledger
├── Cost Center
├── Tax Accounting
└── Financial Reporting
```

Tidak boleh terjadi duplicate accounting logic.

---

# 31. Audit Trail

Semua perubahan penting harus memiliki audit trail.

Minimal:

- created_at
- created_by
- updated_at
- updated_by
- approved_at
- approved_by
- status history
- previous value
- new value
- reason

Critical events mencakup salary change, employee transfer, promotion, termination, payroll adjustment, payroll approval, payroll finalization, leave approval, overtime approval, dan reimbursement approval.

---

# 32. RBAC

Minimum role:

```text
HR Admin
HR Manager
HR Staff
Payroll Admin
Manager / Approver
Employee
```

Permission harus dapat dibatasi berdasarkan organization, property, department, employee scope, dan function.

Contoh: HR Staff Property A tidak otomatis dapat melihat employee Property B.

---

# 33. UI/UX Requirements

## Tables

Operational table harus memiliki:

- Search
- Filter
- Sort
- Pagination
- Column customization
- Bulk selection
- Bulk action
- Row action
- Status
- Detail drawer/page

## Forms

Form harus memiliki validation, required field indicator, inline error, dependent fields, autosave where appropriate, confirmation untuk destructive action, attachment, dan audit information.

## Detail Page

Detail page harus menyediakan:

- Overview
- Related transactions
- History
- Documents
- Actions

---

# 34. Status Model

Status harus merepresentasikan business lifecycle.

### Employee

```text
Draft
Active
On Leave
Suspended
Terminated
```

### Leave

```text
Draft
Submitted
Approved
Rejected
Cancelled
```

### Payroll

```text
Draft
Processing
Calculated
Under Review
Approved
Finalized
Posted
Posting Failed
```

### Reimbursement

```text
Draft
Submitted
Approved
Rejected
Sent to Finance
Paid
```

---

# 35. Validation & Exception Handling

System harus mendeteksi exception sebelum transaction final.

Contoh payroll:

```text
Missing attendance
Missing salary
Invalid tax profile
Invalid BPJS
Duplicate payroll
Negative payroll
Missing accounting mapping
```

Exception harus menjadi actionable queue:

```text
Exception
→ View
→ Fix
→ Revalidate
```

---

# 36. Implementation Strategy

Karena aplikasi sudah existing, implementasi menggunakan pendekatan berikut.

## Phase 1 — Audit Existing System

Periksa database, API, backend, frontend, routes, permissions, workflows, integrations, dan seed data.

Output wajib:

```text
Feature Matrix
```

Contoh:

| Feature | Backend | API | DB | UI | Status |
|---|---|---|---|---|---|
| Employees | Yes | Yes | Yes | Partial | UI GAP |
| Attendance | Yes | Yes | Yes | Partial | UI GAP |
| Leave | Yes | Yes | Yes | Yes | READY |
| Payroll | Partial | Partial | Yes | Yes | PARTIAL |
| Payroll → GL | No | No | No | No | MISSING BACKEND |

## Phase 2 — UI/UX Alignment

Prioritaskan:

1. Existing backend + missing UI
2. Existing API + incomplete UI
3. Existing workflow + incorrect UI
4. Existing data + missing navigation
5. UI consistency

Jangan langsung membuat backend baru jika functionality sebenarnya sudah tersedia.

## Phase 3 — Backend Gap

Hanya lakukan backend development untuk capability yang benar-benar belum tersedia.

Contoh:

```text
UI membutuhkan Payroll Posting
        ↓
Audit
        ↓
Payroll calculation exists
Finance API exists
GL journal service missing
        ↓
Develop missing integration
```

---

# 37. Definition of Done

Sebuah HRIS feature dianggap selesai apabila:

- Backend capability sudah diverifikasi
- Database relation sudah diverifikasi
- API sudah tersedia atau dibuat jika memang missing
- Business rules berjalan
- Permission berjalan
- Workflow berjalan
- UI menggunakan real data
- Loading state tersedia
- Empty state tersedia
- Error state tersedia
- Validation tersedia
- Audit trail tersedia untuk transaksi penting
- Related module integration berjalan
- Tidak menggunakan mock data sebagai permanent implementation
- Tidak membuat duplicate business logic
- Responsive untuk kebutuhan desktop/mobile yang relevan

---

# 38. Final UX Principle

HRIS harus terasa seperti:

```text
HR WORKSPACE
```

Setiap halaman utama harus menjawab minimal salah satu dari:

```text
What needs my attention?
What action should I take?
What is the current status?
What happened?
What should happen next?
```

Dashboard memberikan control overview.

Workspace melakukan pekerjaan.

Detail page melakukan investigation/action.

Report memberikan analysis.

Arsitektur tersebut harus konsisten di seluruh HRIS.

---

# 39. Implementation Status

**Update:** 7 Oktober 2026 · branch `staging`

## 39.1 Keputusan

| Topik | Keputusan |
|---|---|
| Recruitment / ATS | Modul Recruitment sudah ada (PRD P5 EP-03) → **tetap di-include** di HRIS (People → Recruitment), tidak dikembangkan lebih jauh |
| Status Employee & Payroll (§34) | Disetujui; ditambahkan **expand-only** (status lama tidak diganti) di Fase B |
| Urutan kerja | UI dulu (Fase A), lalu backend parsial (Fase B), lalu backend baru (Fase C) |

## 39.2 Feature Matrix (hasil audit Phase 1)

Backend HRIS existing: ±32 ribu baris Go, 426 endpoint HRIS/ESS; hampir semua sudah dipanggil UI. Gap utama ada di struktur UX, bukan fitur.

| Fitur | Status audit | Status sekarang |
|---|---|---|
| Employees (list, create, edit, transfer, promote, offboard) | EXISTS — UI GAP | **READY** (kolom Department/Position/Supervisor/Contract ends, filter) |
| Employee Detail sebagai workspace | EXISTS — UI GAP | **READY** (tab Attendance, Leave, Overtime, Payroll, Loans, Performance, Assets) |
| Organization, Position, Grade, Org chart | EXISTS — READY | READY |
| Employee Lifecycle (effective date, history) | EXISTS — READY | READY |
| Employee Documents + expiry | EXISTS — READY | READY (filter Expired) |
| Workforce Planning | EXISTS — PARTIAL | PARTIAL — coverage hari ini per unit di HR Dashboard; plan/skenario → Fase B |
| Attendance, koreksi, review | EXISTS — READY | **READY** (filter exception, aksi Correct per baris) |
| Work Schedule, Shift & Roster, swap, coverage | EXISTS — READY | READY (open shift belum) |
| Leave, Overtime (approval berjenjang) | EXISTS — READY | READY |
| Timesheets | MISSING BACKEND | Fase C |
| Payroll run, payslip, PPh 21/BPJS, adjustment | EXISTS — READY | READY |
| Payroll → Finance (jurnal GL) | NEEDS INTEGRATION (UI) | **READY** — status Posted to Finance / Posting failed / Posting in progress, nomor jurnal, Retry posting |
| Payroll exception queue (§35) | EXISTS — PARTIAL | Fase B |
| Loan / Cash Advance | EXISTS — PARTIAL | Fase B (request, approval, settlement, ESS) |
| Reimbursement | MISSING BACKEND | Fase C |
| Benefits (master, eligibility, enrollment) | EXISTS — PARTIAL | Fase C |
| HR Requests (pusat request) | MISSING | Fase C |
| Performance Review | EXISTS — READY | READY |
| Goals/KPI berbobot, 360°, Development Plan, Succession | MISSING BACKEND | Fase C |
| Approvals | EXISTS — PARTIAL | Request Revision → Fase B |
| ESS | READY (sebagian besar) | My Reimbursement/Loan/Benefits/HR Requests → Fase B/C |
| HR Dashboard (§26) | MISSING UI | **READY** |
| Status model (§34) | Berbeda | Label "Under review" / "Posted to Finance" di UI; status baru → Fase B |

## 39.3 Fase A — UI/UX Alignment (selesai)

| Item | Implementasi |
|---|---|
| HR Dashboard (§26) | `/hris/dashboard`, home HR Manager/HR Admin. KPI: Headcount, Present Today, On Leave, Absent, Workforce Gap, Overtime Pending, Contracts Expiring, Pending HR Requests. Kartu: Needs attention (14 antrean), Attendance today, Workforce coverage, Payroll, Employee movement, Headcount per department/status. Semua item klik ke workspace. Backend: `GET /api/v1/hris/dashboard` (read-only, per permission) |
| Information Architecture (§3) | Sidebar HRIS: HR Dashboard · People · Time & Attendance · Payroll · Performance · Reports & Migration |
| Deep link | Tab Attendance, Leave, Overtime, Schedules, Payroll, Employee Detail di URL (`?tab=`); filter dari URL |
| Attendance (§11) | Filter exception (`flag`: missing, any, out_of_area, unapproved_overtime, …) dan aksi **Correct** |
| Employees (§5) | Kolom dan filter Department/Position, Supervisor, Contract ends |
| Employee Detail (§5) | Tab Attendance, Leave, Overtime, Payroll (payslip + PDF, adjustment), Loans & Advances, Performance, Assets (Inventory custodian). Backend: `GET /api/v1/hris/payslips?employeeId=` |
| Payroll → Finance (§19) | Kartu Finance & Accounting di payroll run: jurnal dari Accounting, posting exception + Retry |

## 39.4 Berikutnya

- **Fase B:** antrean payroll exception (View → Fix → Revalidate), Loan & Cash Advance (request, approval, settlement, ESS), Workforce plan per department, status baru Employee (draft, on leave, suspended) dan Payroll (posting failed), Request Revision di approval.
- **Fase C:** Reimbursement, HR Requests, Timesheet, Goals/KPI, 360° Feedback, Development Plan, Succession, Benefits enrollment.
