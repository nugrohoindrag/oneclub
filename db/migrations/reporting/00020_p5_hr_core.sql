-- PRD P5 core HR read models (EP-27 FR-RPT-P5-02: Headcount, Turnover,
-- Contract Expiry, Certification Expiry and Training reports; the HR KPI of
-- the BI area build on them). No identity numbers, bank accounts, salaries
-- or health data (FR-HR-03). Views use security_invoker so RLS of the
-- sources applies.

-- +goose Up
CREATE VIEW reporting.hr_employees WITH (security_invoker = true) AS
SELECT e.id AS employee_id, e.property_id, e.employee_no, e.full_name, e.gender, e.org_unit_id, ou.code AS org_unit_code, ou.name AS org_unit_name,
       ou.unit_type, ou.parent_id AS parent_org_unit_id, coalesce(e.cost_center, ou.cost_center) AS cost_center, e.position_id, p.code AS position_code,
       p.name AS position_name, p.workforce_role, e.grade_id, g.code AS grade_code, g.level AS grade_level, e.supervisor_id, e.employment_status,
       e.worker_category, e.join_date, e.probation_end_date, e.permanent_date, e.termination_date, e.termination_type, e.termination_status, e.status,
       e.archived_at
FROM hris.employees e
LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
LEFT JOIN hris.positions p ON p.id = e.position_id
LEFT JOIN hris.grades g ON g.id = e.grade_id;

CREATE VIEW reporting.hr_contracts WITH (security_invoker = true) AS
SELECT c.id AS contract_id, c.property_id, c.number, c.employee_id, e.employee_no, e.full_name, ou.name AS org_unit_name, p.name AS position_name,
       c.contract_type, c.sequence_no, c.start_date, c.end_date, c.ended_on, c.probation_end_date, c.status, c.end_reason, e.status AS employee_status
FROM hris.contracts c
JOIN hris.employees e ON e.id = c.employee_id
LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
LEFT JOIN hris.positions p ON p.id = c.position_id;

CREATE VIEW reporting.hr_certifications WITH (security_invoker = true) AS
SELECT c.id AS certification_id, c.property_id, c.certification_type_id, t.code AS type_code, t.name AS type_name, t.mandatory_for, c.holder_kind,
       c.employee_id, c.partner_id, c.holder_name, ou.name AS org_unit_name, c.certificate_no, c.issuer, c.issued_on, c.expires_on, c.status,
       c.archived_at
FROM hris.certifications c
JOIN hris.certification_types t ON t.id = c.certification_type_id
LEFT JOIN hris.employees e ON e.id = c.employee_id
LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id;

CREATE VIEW reporting.hr_training WITH (security_invoker = true) AS
SELECT pa.id AS participant_id, s.property_id, s.id AS session_id, s.title, s.starts_at, s.ends_at, s.status AS session_status, tp.code AS program_code,
       tp.name AS program_name, tp.category, tp.cost_per_participant, s.cost_total, pa.employee_id, e.employee_no, e.full_name, ou.name AS org_unit_name,
       pa.attendance, pa.result, pa.score
FROM hris.training_participants pa
JOIN hris.training_sessions s ON s.id = pa.session_id
JOIN hris.training_programs tp ON tp.id = s.program_id
JOIN hris.employees e ON e.id = pa.employee_id
LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
WHERE s.archived_at IS NULL;

-- Mandatory certifications per active employee (workforce role of the
-- position or required by the position) and whether a valid certificate is
-- on file today (Certification Compliance, FR-RPT-P5-01).
CREATE VIEW reporting.hr_certification_requirements WITH (security_invoker = true) AS
SELECT e.id AS employee_id, e.property_id, e.employee_no, e.full_name, ou.name AS org_unit_name, p.name AS position_name, t.code AS type_code,
       t.name AS type_name,
       EXISTS (SELECT 1 FROM hris.certifications c WHERE c.employee_id = e.id AND c.certification_type_id = t.id AND c.archived_at IS NULL
         AND c.status <> 'revoked' AND (c.issued_on IS NULL OR c.issued_on <= current_date)
         AND (c.expires_on IS NULL OR c.expires_on >= current_date)) AS valid
FROM hris.employees e
JOIN hris.positions p ON p.id = e.position_id
JOIN hris.certification_types t ON t.archived_at IS NULL AND t.status = 'active'
  AND ((p.workforce_role IS NOT NULL AND p.workforce_role = ANY (t.mandatory_for)) OR t.code = ANY (p.required_certifications))
LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
WHERE e.status = 'active' AND e.archived_at IS NULL;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.hr_certification_requirements, reporting.hr_training, reporting.hr_certifications, reporting.hr_contracts, reporting.hr_employees;
