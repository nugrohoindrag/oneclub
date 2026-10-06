-- P4 gap fixes, Finance UX (PRD P4 FR-FIN-05, §7.1 / §7.3, FR-REV-07):
--  * report exports also as PDF;
--  * the e-Faktur (output tax invoice) of each billing invoice, read by the
--    Back Office Invoices screen and the Member App (Transactions → Invoices)
--    through accounting's read endpoints. The latest tax invoice wins: an
--    active one before a cancelled one (a cancelled faktur replaced by a new
--    draft shows the draft). security_invoker keeps the Row Level Security
--    of the sources.

-- +goose Up
ALTER TABLE reporting.exports DROP CONSTRAINT exports_format_check;
ALTER TABLE reporting.exports ADD CONSTRAINT exports_format_check CHECK (format IN ('csv', 'xlsx', 'pdf'));

CREATE VIEW reporting.billing_invoice_efaktur WITH (security_invoker = true) AS
SELECT DISTINCT ON (t.source_id)
       t.source_id AS invoice_id, t.property_id, i.number AS invoice_number, i.status AS invoice_status, i.customer_id, i.corporate_account_id,
       c.user_id AS customer_user_id, t.id AS tax_invoice_id, t.faktur_number, t.status, t.tax_period, t.uploaded_at, t.cancelled_at, t.created_at
FROM accounting.tax_invoices t
JOIN billing.invoices i ON i.id = t.source_id
LEFT JOIN crm.customers c ON c.id = i.customer_id
WHERE t.direction = 'output' AND t.source_type = 'billing.invoice'
ORDER BY t.source_id, (t.status <> 'cancelled') DESC, t.created_at DESC;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.billing_invoice_efaktur;
UPDATE reporting.exports SET format = 'csv' WHERE format = 'pdf';
ALTER TABLE reporting.exports DROP CONSTRAINT exports_format_check;
ALTER TABLE reporting.exports ADD CONSTRAINT exports_format_check CHECK (format IN ('csv', 'xlsx'));
