# Improvement Requirements — Revenue & Billing / Billing Workspace

## 1. Objective

Improve **Revenue & Billing → Billing** so it functions as an operational billing workbench, not a reporting-only page.

Finance/Accountant must be able to:
- identify charges requiring billing action;
- review and validate billing data;
- resolve billing exceptions;
- prepare transactions for invoicing;
- generate invoices individually or in batch;
- consolidate or split billing when required;
- trace each billing record from operational source to invoice and AR.

Core principle:

> **Billing is an action workspace. Invoices is the document lifecycle. Accounts Receivable manages receivables and payment.**

## 2. Target Business Flow

```text
Golf / Resort / Event / F&B Transaction
                  ↓
              Customer Folio
                  ↓
             BILLING WORKSPACE
                  ↓
       ┌──────────┼───────────┐
       ↓          ↓           ↓
 Pending       Ready       Exception
 Billing      to Invoice      ↓
       │          │       Resolve
       │          ↓           │
       └──────→ Generate ←────┘
                   ↓
                Invoice
                   ↓
             Accounts Receivable
                   ↓
                Payment
                   ↓
             Reconciliation
                   ↓
                  GL
```

## 3. Current Problems

### 3.1 Billing looks like a report
Current table mainly shows Folio, Customer, Charges, Paid, To Bill/Outstanding, and Status. It needs direct actions.

### 3.2 Invoiced overlaps with Invoices
Once a folio is invoiced, the main workspace should be **Revenue & Billing → Invoices**. Billing only needs an invoice reference/link for traceability.

### 3.3 No clear exception handling
Surface blocking issues such as:
- missing customer;
- missing billing entity;
- incomplete tax information;
- unresolved folio;
- pricing mismatch;
- duplicate charge;
- invalid payment terms;
- incomplete billing address;
- missing supporting document.

### 3.4 No row-level action model
Every row must have a clear next action based on its status.

## 4. Target Billing Navigation

```text
Revenue & Billing
├── Billing
├── Invoices
├── Credit / Debit Notes
├── Revenue Adjustments
└── Revenue Reconciliation
```

Inside Billing:

```text
[Pending Billing] [Ready to Invoice] [Exceptions] [All]
```

Remove the primary **Invoiced** tab.

## 5. Billing Summary

Show actionable workload by business:

```text
Golf
Total Billable       Rp527.757.000
Pending Billing      366
Ready to Invoice     24
Exceptions            7

Events & Meetings
Total Billable       Rp492.751.500
Pending Billing       17
Ready to Invoice      13
Exceptions             2
```

Cards must link to filtered lists.

## 6. Pending Billing

Definition: charges exist but are not yet ready to generate an invoice.

Recommended table:

| Select | Folio | Source | Customer | Bill To | Charges | Paid/Deposit | To Bill | Exception | Status | Action |
|---|---|---|---|---|---:|---:|---:|---|---|---|

Row actions:

```text
[Review] [Prepare Billing] [•••]
```

More actions:
- View Folio
- View Source Transaction
- View Customer
- View Billing History
- Add Note
- Resolve Exception

## 7. Ready to Invoice

Definition: all billing validations have passed.

Validation:

```text
✓ Customer valid
✓ Bill To valid
✓ Billing address valid
✓ Charges validated
✓ Tax calculated
✓ Deposit applied
✓ Payment term available
✓ Revenue allocation available
✓ No blocking exception
```

Recommended table:

| Select | Folio | Customer | Bill To | Charges | Deposit | To Invoice | Payment Term | Status | Action |
|---|---|---|---|---:|---:|---:|---|---|---|

Row actions:

```text
[Review] [Generate Invoice] [•••]
```

More actions:
- Edit Billing
- Preview Invoice
- View Revenue Allocation
- View Tax
- View Source

## 8. Exceptions

Dedicated actionable queue.

Exception types:
- Missing Customer
- Missing Billing Entity
- Missing Billing Address
- Missing Tax Information
- Invalid Tax Configuration
- Pricing Mismatch
- Unresolved Folio
- Duplicate Charge
- Missing Supporting Document
- Invalid Payment Term
- Revenue Allocation Error

Recommended table:

| Folio | Customer | Amount | Exception | Severity | Created | Owner | Status | Action |
|---|---|---:|---|---|---|---|---|---|

Primary action:

```text
[Resolve]
```

## 9. All

For monitoring and traceability only.

| Folio | Source | Customer | Bill To | Charges | Paid | To Bill | Invoice | Status | Action |
|---|---|---|---|---:|---:|---:|---|---|---|

Possible statuses:
- Pending Billing
- Ready to Invoice
- Exception
- Invoiced
- Cancelled

## 10. Row-Level Actions — Mandatory

The final table column must be **Action**.

### Pending Billing
```text
[Review] [Prepare Billing] [•••]
```

### Ready to Invoice
```text
[Review] [Generate Invoice] [•••]
```

### Exception
```text
[Resolve] [View Details] [•••]
```

### Invoiced
```text
[View Invoice] [•••]
```

This is critical so the page does not look like a report.

## 11. Batch Actions

Because Billing can contain hundreds of records, support checkbox selection and bulk actions.

Example:

```text
3 selected
[Prepare Billing] [Generate Invoices] [Assign Owner] [Export] [•••]
```

For Ready to Invoice:

```text
12 selected
[Generate 12 Invoices]
```

Before generation:

```text
12 selected
10 can be invoiced
2 require attention

[Review 2 Exceptions]
[Generate 10 Invoices]
```

Never generate invoices for records with blocking errors.

## 12. Prepare Billing

Full-page/detail workspace:

```text
Folio
Customer
Bill To
Billing Date
Payment Term

Charges
Deposit
Discount
Tax
Service Charge
Amount to Invoice

[Save Billing]
[Mark Ready to Invoice]
```

## 13. Billing Consolidation

Support combining multiple folios:

```text
FOL-001  Golf       Rp20M
FOL-002  Meeting    Rp30M
FOL-003  F&B        Rp15M
                    ------
Total               Rp65M
```

Action:

```text
[Consolidate]
```

Result: one invoice with traceable links to all source folios.

## 14. Billing Split

Support splitting one folio across multiple billing parties.

Example:

```text
Folio Total     Rp100M
PT ABC           Rp70M
Guest             Rp30M
```

Action:

```text
[Split Billing]
```

Preserve the original folio and allocation history.

## 15. Deposit / Advance

Billing calculation:

```text
Gross Charges
- Deposit / Advance
- Applied Credit
- Discount
+ Tax / Charges
----------------
Amount to Invoice
```

Deposit application must link to the original payment.

## 16. Billing Adjustments

Authorized users may adjust:
- quantity;
- price;
- discount;
- service charge;
- tax;
- deposit application;
- revenue allocation.

Every adjustment records:
- user;
- timestamp;
- previous value;
- new value;
- reason;
- reference.

## 17. Invoice Generation

When **Generate Invoice** is clicked:

1. Validate billing record.
2. Validate customer.
3. Validate billing entity.
4. Validate tax.
5. Validate revenue allocation.
6. Validate payment terms.
7. Calculate totals.
8. Generate invoice number.
9. Create invoice.
10. Create accounting impact when posted.
11. Link invoice back to folio.
12. Move billing record to Invoiced.
13. Generate customer-facing PDF.
14. Publish invoice to Customer Portal.
15. Trigger customer notification.

## 18. Customer Invoice

After invoice is issued:

```text
Billing
   ↓
Invoice
   ↓
Customer Portal
```

Customer can:
- View invoice
- Download PDF
- View payment status
- View due date
- Pay online when supported
- Download tax invoice when available

Accountant can:
- Preview
- Download
- Send
- Resend
- Copy invoice link
- View delivery status

## 19. Audit Trail

Record:
- Charge created
- Billing opened
- Billing edited
- Deposit applied
- Discount changed
- Tax changed
- Billing approved
- Billing marked ready
- Invoice generated
- Invoice posted
- Invoice sent
- Invoice voided
- Credit note created

## 20. Permissions

### Accountant
- Review billing
- Prepare billing
- Generate invoice
- Apply permitted adjustments
- Resolve operational billing exceptions
- Send invoice

### Finance Manager
Additionally:
- Approve high-value billing
- Approve adjustments
- Approve write-off/discount exceptions
- Override billing validation
- Approve consolidated billing

### General Manager / Director
Approve according to configured approval thresholds.

## 21. UX Requirement

Every row must answer:

> **What is the next action for this record?**

Avoid:

```text
Folio | Customer | Charges | Paid | Outstanding | Status
```

Use:

```text
Folio | Customer | Bill To | Charges | To Bill | Status | Action
```

Example:

```text
FOL-260926-0056
Michael Hidayat
PT ABC Indonesia
Rp100.445.000
Rp100.445.000
Pending Billing

[Review] [Prepare Billing] [•••]
```

## 22. Empty States

### Pending Billing
> All billing is up to date. There are no transactions waiting for billing.

### Ready to Invoice
> Nothing ready to invoice. Billing records will appear here after validation.

### Exceptions
> No billing exceptions. All billing records are ready to proceed.

Avoid generic empty-state copy such as “Data yang Anda tambahkan akan tampil di sini.”

## 23. Success Criteria

- Finance can identify pending billing immediately.
- Finance can identify records ready to invoice.
- Finance can identify and resolve billing exceptions.
- Finance can perform row-level actions directly from the table.
- Finance can generate one or many invoices from Billing.
- Billing retains source-to-invoice traceability.
- Invoiced records link directly to Invoice.
- Customer invoices are automatically generated and available through Customer Portal.
- No duplicate manual data entry between operational transaction, Billing, Invoice, AR, and GL.
- Every material billing change has an audit trail.
