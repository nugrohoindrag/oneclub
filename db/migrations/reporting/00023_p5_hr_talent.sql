-- PRD P5 recruitment and performance review read models (EP-27
-- FR-RPT-P5-01/02: Recruitment Funnel, Time to Hire, Performance Review and
-- Performance Rating Distribution reports; Time to Hire, Open Positions,
-- Review Completion and Review Score KPIs of HR Performance). No candidate
-- personal data, salary budgets or offer salaries (FR-HR-03, UU PDP): the
-- views read only the columns hris.harden_report_role_talent() grants to
-- the report role. security_invoker: RLS of the sources applies.

-- +goose Up
CREATE VIEW reporting.hr_requisitions WITH (security_invoker = true) AS
SELECT r.id AS requisition_id, r.property_id, r.number, r.title, r.org_unit_id, ou.name AS org_unit_name, p.name AS position_name, r.headcount,
       r.hired_count, r.reason, r.contract_type, r.worker_category, r.is_public, r.status, r.submitted_at, r.approved_at, r.filled_at, r.closed_at,
       r.created_at
FROM hris.job_requisitions r
JOIN hris.org_units ou ON ou.id = r.org_unit_id
LEFT JOIN hris.positions p ON p.id = r.position_id;

CREATE VIEW reporting.hr_applications WITH (security_invoker = true) AS
SELECT a.id AS application_id, a.property_id, a.number, a.requisition_id, r.number AS requisition_number, r.title AS requisition_title,
       r.org_unit_id, ou.name AS org_unit_name, a.source, a.stage, a.applied_at, a.stage_changed_at, a.closed_at, a.closed_stage, a.hired_at, a.rating,
       r.approved_at AS requisition_approved_at, c.talent_pool_consent, c.erased_at,
       EXISTS (SELECT 1 FROM hris.application_stage_events e WHERE e.application_id = a.id AND e.to_stage = 'screening') OR a.stage = 'screening'
         AS reached_screening,
       EXISTS (SELECT 1 FROM hris.application_stage_events e WHERE e.application_id = a.id AND e.to_stage = 'interview') OR a.stage = 'interview'
         AS reached_interview,
       EXISTS (SELECT 1 FROM hris.application_stage_events e WHERE e.application_id = a.id AND e.to_stage = 'offered') OR a.stage IN ('offered', 'hired')
         AS reached_offer
FROM hris.applications a
JOIN hris.job_requisitions r ON r.id = a.requisition_id
JOIN hris.org_units ou ON ou.id = r.org_unit_id
JOIN hris.candidates c ON c.id = a.candidate_id;

CREATE VIEW reporting.hr_performance_reviews WITH (security_invoker = true) AS
SELECT rv.id AS review_id, rv.property_id, rv.cycle_id, c.code AS cycle_code, c.name AS cycle_name, c.cycle_type, c.status AS cycle_status, c.period_start,
       c.period_end, rv.employee_id, e.employee_no, e.full_name, e.org_unit_id, ou.name AS org_unit_name, p.name AS position_name, g.code AS grade_code,
       rv.status, rv.self_score, rv.manager_score, rv.final_score, rv.recommended_rating, rv.final_rating, rv.recommendation, rv.self_submitted_at,
       rv.manager_submitted_at, rv.calibrated_at, rv.completed_at, rv.acknowledged_at, rv.employment_change_id IS NOT NULL AS promoted
FROM hris.performance_reviews rv
JOIN hris.review_cycles c ON c.id = rv.cycle_id
JOIN hris.employees e ON e.id = rv.employee_id
LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
LEFT JOIN hris.positions p ON p.id = e.position_id
LEFT JOIN hris.grades g ON g.id = e.grade_id;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.hr_performance_reviews, reporting.hr_applications, reporting.hr_requisitions;
