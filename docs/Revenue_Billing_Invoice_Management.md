# Invoice Management — Finance & Accounting

## 1. Purpose

Invoice Management digunakan Finance/Accountant untuk mengelola siklus billing dan invoice customer dari sumber transaksi sampai invoice diterbitkan, dikirim, dibayar, dan tercatat ke accounting.

Invoice sebaiknya **otomatis terbentuk dari transaksi bisnis yang eligible untuk billing**. Accountant tidak perlu membuat invoice manual untuk setiap transaksi.

---

## 2. Invoice Lifecycle

```text
Business Transaction
        ↓
Billing Eligible
        ↓
Invoice Draft / Auto Generated
        ↓
Validation
        ↓
Approved / Posted
        ↓
Invoice Issued
        ↓
Customer Notification
        ↓
Customer Download / View Invoice
        ↓
Payment
        ↓
Payment Reconciliation
        ↓
Paid
```

Status utama:

- Draft
- Issued
- Partially Paid
- Paid
- Overdue
- Void
- Cancelled

---

## 3. Invoice Sources

Invoice dapat berasal dari:

### Golf

- Green Fee
- Golf Package
- Golf Cart
- Caddie
- Driving Range
- Membership
- Other Golf Services

### Resort

- Room / Bungalow
- Meeting Room
- Event
- Other Resort Services

### Other Revenue

- F&B
- Merchandise
- Other Services

Source dapat berasal dari:

- Customer Folio
- Booking
- Membership
- Event
- Sales Transaction
- Corporate Contract
- Manual Billing

---

## 4. Automatic Invoice Generation

### Prinsip

Invoice customer sebaiknya otomatis dibuat ketika transaksi memenuhi billing rule.

Contoh:

```text
Golf Booking
      ↓
Booking Completed
      ↓
Billing Rule
      ↓
Invoice Generated
      ↓
Invoice Posted
      ↓
Customer Notified
```

Untuk corporate customer:

```text
Event Completed
      ↓
Billing Milestone Reached
      ↓
Invoice Generated
      ↓
Invoice Sent to Billing Contact
```

### Manual Invoice

Manual invoice tetap tersedia untuk exception:

- Adjustment
- Special billing
- Non-system transaction
- Correction
- One-off charge

Manual invoice harus mengikuti permission dan approval policy.

---

# 5. Create Invoice

Create Invoice sebaiknya berupa full-page workspace, bukan modal kecil.

## 5.1 Invoice Information

| Field | Requirement | Description |
|---|---|---|
| Invoice Number | Auto | System-generated |
| Invoice Type | Required | Sales Invoice |
| Invoice Date | Required | Date invoice |
| Posting Date | Required | Accounting posting date |
| Source Type | Required | Booking, Folio, Event, etc. |
| Source Reference | Required | Source transaction reference |

---

## 5.2 Customer & Billing

| Field | Requirement | Description |
|---|---|---|
| Customer | Required | Customer/account |
| Bill To | Required | Billing entity |
| Billing Address | Required | Invoice address |
| Company | Optional | Corporate company |
| Tax ID / NPWP | Conditional | Required for applicable tax invoice |
| Payment Term | Required | Net 0, Net 7, Net 30, etc. |
| Due Date | Auto | Derived from invoice date + payment term |
| Currency | Required | Default IDR |

Customer master data should pre-populate billing information.

---

# 6. Invoice Items

Invoice must support multiple line items.

| Field | Description |
|---|---|
| Product / Service | Revenue item |
| Description | Invoice description |
| Quantity | Quantity |
| Unit | Unit of measure |
| Unit Price | Price before discount/tax |
| Discount | Amount or percentage |
| Tax | Applicable tax |
| Revenue Account | GL revenue account |
| Total | Calculated line total |

Example:

```text
Golf Package       2 × Rp750.000
Golf Cart          1 × Rp250.000
--------------------------------
Subtotal             Rp1.750.000
Discount             Rp0
Service Charge       Rp175.000
PPN                  Rp192.500
--------------------------------
Grand Total          Rp2.117.500
```

---

# 7. Revenue Allocation

Revenue allocation should normally be automatic based on Allocation Rules.

Example:

```text
Golf Package
Rp1.500.000

Green Fee       Rp900.000
Golf Cart       Rp250.000
Caddie          Rp150.000
F&B             Rp200.000
```

Accountant can review or override allocation if permitted.

Flow:

```text
Product / Package
       ↓
Allocation Rule
       ↓
Revenue Allocation
       ↓
Accounting Entry
```

Allocation Rules belong to configuration, not daily transaction entry.

---

# 8. Deferred Revenue

Support deferred revenue for products/services whose revenue is recognized over a period.

Example:

```text
Annual Membership
Rp12.000.000

Deferred Revenue
Rp12.000.000

Monthly Recognition
Rp1.000.000
```

Required information:

- Recognition start date
- Recognition end date
- Recognition frequency
- Total amount
- Recognized amount
- Remaining amount
- Next recognition date
- Revenue account
- Deferred revenue account

---

# 9. Charges & Tax

Invoice calculation should support:

- Subtotal
- Discount
- Service Charge
- Taxable Amount
- PPN / applicable tax
- Other applicable charges
- Grand Total

Tax configuration comes from the Tax module.

Tax should not be hard-coded in the invoice form.

---

# 10. E-Faktur

For applicable Indonesian tax invoices:

- Tax invoice required
- Tax object/code
- Tax invoice number
- Tax invoice status
- Tax invoice issue date
- Tax reporting status

Possible status:

```text
Not Required
Pending
Issued
Rejected
Reported
```

---

# 11. Payment Terms

Support:

- Immediate
- Net 7
- Net 14
- Net 30
- Net 60
- Custom

Support payment schedules when applicable:

```text
30% Down Payment
70% Before Event
```

Payment itself is recorded through Accounts Receivable.

---

# 12. Customer PO & Contract

For corporate billing:

- Customer PO Number
- Contract Number
- Contract Reference
- Billing Reference
- Event Reference
- Booking Reference

These references should appear on the customer-facing invoice PDF.

---

# 13. Attachments & Notes

Support:

- PO
- Contract
- Booking confirmation
- Event confirmation
- Service evidence
- Supporting documents

Notes:

- Customer Notes
- Internal Notes

Internal notes must not appear on the customer invoice.

---

# 14. Accounting Preview

Before posting, Accountant can inspect the accounting impact.

Example:

```text
Accounts Receivable      Dr  Rp2.117.500
    Golf Revenue             Cr  Rp1.500.000
    Service Charge           Cr  Rp175.000
    Output VAT               Cr  Rp192.500
```

Action:

**View Journal Impact**

The final journal entry is generated automatically when the invoice is posted.

---

# 15. Approval & Posting

Invoice workflow:

```text
Draft
  ↓
Submitted
  ↓
Approved
  ↓
Posted
  ↓
Issued
  ↓
Sent
  ↓
Paid
```

Approval thresholds should be configurable.

Example:

```text
Below threshold
→ Accountant can post

Medium value
→ Finance Manager approval

High value
→ Manager / Director approval
```

---

# 16. Customer Invoice Delivery

Customer invoices should be available digitally after the invoice is issued.

## Customer Portal

Customer should be able to:

- View invoice
- Download invoice PDF
- View invoice status
- View payment due date
- View payment instructions
- View payment history
- Download tax invoice when available

Example:

```text
My Invoices

INV-2026-00091
Rp12.500.000
Due: 15 Oct 2026
Status: Unpaid

[View Invoice]
[Download PDF]
[Pay Now]
```

---

# 17. Automatic Customer Notification

After invoice is issued:

```text
Invoice Posted
      ↓
Generate Customer PDF
      ↓
Publish to Customer Portal
      ↓
Notify Customer
```

Notification channels may include:

- Email
- Customer Portal
- Mobile/PWA notification
- WhatsApp integration, if supported

The invoice PDF should be generated from the official invoice template.

---

# 18. Invoice PDF

Customer-facing invoice should contain:

### Header

- Golf & Resort logo
- Legal entity name
- Address
- Contact information
- Tax information

### Invoice Information

- Invoice number
- Invoice date
- Due date
- Customer
- Billing entity
- Billing address
- PO / contract reference

### Line Items

- Product/service
- Description
- Quantity
- Unit price
- Discount
- Tax
- Total

### Summary

- Subtotal
- Discount
- Service charge
- Tax
- Grand total

### Payment Information

- Bank account
- Payment instructions
- Payment reference
- QR / payment link when supported

### Tax Information

- NPWP
- Tax invoice information
- E-Faktur reference when applicable

---

# 19. Customer Download & Access

Invoice download should not require Accountant to manually send the PDF every time.

Recommended flow:

```text
Invoice Issued
      ↓
PDF Generated
      ↓
Customer Portal
      ↓
Customer can download
```

Accountant can still:

- Download
- Preview
- Send / resend invoice
- Copy invoice link
- View delivery status

---

# 20. Invoice Actions

From invoice detail:

```text
[Preview]
[Download PDF]
[Send Invoice]
[Resend Invoice]
[Copy Customer Link]
[Record Payment]
[Create Credit Note]
[Void]
```

Actions should depend on invoice status and user permission.

---

# 21. Invoice List

Recommended columns:

| Column | Description |
|---|---|
| Invoice No. | Invoice identifier |
| Customer | Customer |
| Source | Booking/Folio/Event/etc. |
| Invoice Date | Invoice date |
| Due Date | Payment due date |
| Amount | Invoice total |
| Paid | Amount paid |
| Outstanding | Remaining balance |
| Status | Invoice status |
| Tax Status | Tax/e-Faktur status |

Filters:

- Status
- Customer
- Invoice Date
- Due Date
- Source
- Payment Status
- Tax Status
- Amount
- Corporate / Individual

---

# 22. Revenue & Billing Navigation

Recommended navigation:

```text
Revenue & Billing

Transactions
├── Billing
├── Invoices
├── Credit / Debit Notes
├── Revenue Adjustments
└── Revenue Reconciliation

Revenue Recognition
├── Revenue Allocation
├── Deferred Revenue
└── Recognition Schedule

Reports
├── Revenue Summary
├── Revenue by Source
├── Revenue by Account
└── Billing Report
```

Tax should remain a separate module:

```text
Tax

Transactions
├── Tax Transactions
├── Tax Invoices
└── Tax Adjustments

Reports
├── PPN Report
├── Tax Summary
└── Tax Reconciliation

Configuration
├── Tax Codes
└── Tax Rules
```

---

# 23. Core Business Principle

The system should minimize manual invoice creation.

```text
Operational Transaction
        ↓
Billing Rule
        ↓
Automatic Invoice
        ↓
Accounting Posting
        ↓
Customer Portal
        ↓
Customer Download
        ↓
Payment
        ↓
AR Reconciliation
        ↓
GL
```

Manual invoice creation remains available as an exception and controlled by permissions.
