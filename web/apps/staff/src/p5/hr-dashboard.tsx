import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useGet, type Schemas } from '@oneclub/api-client';
import { formatDate, formatNumber } from '@oneclub/i18n';
import {
  Amount, BarChart, CircleButton, DashButton, DashCard, DashGrid, DashHead, DashIcon, DashName, DashStatusPill, DashTable, Delta, ErrorAlert, Gauge,
  HeatBars, Icon, MiniCard, Note, ProgressRow, PromoCard, ReportCard, SegmentBar, Skeleton, SplitStats, useAuth, type DashStatus,
} from '@oneclub/shell';
import { money } from '../p1/common';

// HR Dashboard of the Back Office (HR Manager / HR Admin home, HRIS →
// Dashboard): the operational control centre of HRIS. Every figure comes
// from GET /api/v1/hris/dashboard and every tile, line and queue opens the
// workspace where the work is done (employees, attendance, leave, overtime,
// schedules, payroll, contracts, documents).

type Dashboard = Schemas['HRDashboard'];

const API = '/api/v1/hris/dashboard';

const addDays = (day: string, n: number) => {
  const d = new Date(`${day}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
};

/** Attendance days of one day filtered by status (or the exception flags). */
const attendanceLink = (day: string, extra: Record<string, string>) => `/hris/attendance?${new URLSearchParams({ tab: 'days', from: day, to: day, ...extra })}`;

/** The HR queues: label, icon and the workspace that works them. */
const QUEUES: Record<string, { label: string; icon: string; path: (d: Dashboard) => string; tone: 'error' | 'pending' }> = {
  attendance_review: { label: 'Clock-ins to review', icon: 'location_off', tone: 'pending', path: () => '/hris/attendance?tab=review' },
  attendance_missing: { label: 'Missing clock-in / out (30 days)', icon: 'report', tone: 'error',
    path: (d) => `/hris/attendance?${new URLSearchParams({ tab: 'days', from: addDays(d.date, -30), to: d.date, flag: 'missing' })}` },
  attendance_corrections: { label: 'Attendance corrections', icon: 'edit_calendar', tone: 'pending', path: () => '/hris/attendance?tab=corrections' },
  leave_requests: { label: 'Leave requests pending', icon: 'beach_access', tone: 'pending', path: () => '/hris/leave?tab=requests' },
  permission_requests: { label: 'Permission requests pending', icon: 'schedule', tone: 'pending', path: () => '/hris/leave?tab=permission' },
  overtime_requests: { label: 'Overtime requests pending', icon: 'more_time', tone: 'pending', path: () => '/hris/overtime?tab=requests' },
  overtime_unapproved: { label: 'Overtime worked without approval', icon: 'timer', tone: 'error', path: () => '/hris/overtime?tab=exceptions' },
  shift_swaps: { label: 'Shift swaps to approve', icon: 'swap_horiz', tone: 'pending', path: () => '/hris/schedules?tab=swaps' },
  profile_changes: { label: 'Personal data changes', icon: 'edit_note', tone: 'pending', path: () => '/hris/employees?tab=data' },
  contracts_expiring: { label: 'Contracts ending (30 days)', icon: 'contract', tone: 'pending', path: () => '/hris/employees?tab=contracts&expiring=1' },
  documents_expired: { label: 'Expired employee documents', icon: 'error', tone: 'error', path: () => '/hris/employees?tab=documents&within=0' },
  documents_expiring: { label: 'Documents expiring (30 days)', icon: 'description', tone: 'pending', path: () => '/hris/employees?tab=documents' },
  certifications_expired: { label: 'Expired certifications', icon: 'workspace_premium', tone: 'error', path: () => '/hris/training' },
  payroll_warnings: { label: 'Payroll warnings to resolve', icon: 'warning', tone: 'error',
    path: (d) => (d.payroll ? `/hris/payroll/runs/${d.payroll.runId}` : '/hris/payroll') },
  // HRIS phase B: posting to Finance, loans and cash advances, lifecycle statuses
  payroll_posting_failed: { label: 'Payroll posting to Finance failed', icon: 'report', tone: 'error', path: () => '/hris/payroll?tab=runs' },
  loan_requests: { label: 'Loan / cash advance requests', icon: 'account_balance_wallet', tone: 'pending', path: () => '/hris/loans' },
  loans_to_pay: { label: 'Approved loans to pay', icon: 'payments', tone: 'pending', path: () => '/hris/loans' },
  employees_draft: { label: 'Draft employees to activate', icon: 'person_add', tone: 'pending', path: () => '/hris/employees?status=draft' },
  employees_suspended: { label: 'Suspended employees', icon: 'person_off', tone: 'pending', path: () => '/hris/employees' },
  // HRIS phase C
  reimbursements_to_send: { label: 'Approved claims to send to Finance', icon: 'receipt', tone: 'pending', path: () => '/hris/reimbursements?tab=claims' },
  reimbursements_to_pay: { label: 'Reimbursement claims to pay', icon: 'payments', tone: 'pending', path: () => '/hris/reimbursements?tab=claims' },
  timesheets_pending: { label: 'Timesheets waiting for approval', icon: 'schedule', tone: 'pending', path: () => '/hris/timesheets' },
  open_shift_claims: { label: 'Open shift claims to decide', icon: 'event_available', tone: 'pending', path: () => '/hris/schedules' },
};

const RUN: Record<string, [DashStatus, string]> = {
  draft: ['neutral', 'Draft'], calculated: ['warn', 'Calculated'], submitted: ['warn', 'Under review'], approved: ['good', 'Approved'],
  posted: ['good', 'Posted to Finance'], paid: ['good', 'Paid'],
};

/** Where each HR queue sits (the "method" column of the attention table). */
const AREA: [RegExp, string, string][] = [
  [/^attendance|^timesheets|^open_shift|^shift/, 'Time & Attendance', 'how_to_reg'], [/^leave|^permission/, 'Leave', 'beach_access'],
  [/^overtime/, 'Overtime', 'more_time'], [/^payroll|^loan|^reimbursement/, 'Payroll & services', 'payments'],
  [/^contracts|^documents|^certifications|^profile|^employees/, 'Employees', 'badge'],
];
const areaOf = (key: string) => AREA.find(([rx]) => rx.test(key)) ?? [/./, 'HR', 'badge'];

type AttentionRow = { key: string; count: number; label: string; icon: string; tone: 'error' | 'pending'; to: string };

/** HR Dashboard (HR Manager / HR Admin home, Human Resources → HR Dashboard), on the dashboard kit. */
export function HRDashboardPage() {
  const nav = useNavigate();
  const { can } = useAuth();
  const d = useGet<Dashboard>(API);
  const [q, setQ] = useState('');
  const x = d.data;
  if (d.error) return <div className="oc-dash-page"><DashHead title="HR Dashboard" /><ErrorAlert error={d.error} /></div>;
  if (!x) return <div className="oc-dash-page"><DashHead title="HR Dashboard" /><Skeleton rows={8} /></div>;
  const h = x.headcount;
  const a = x.attendance;
  const w = x.workforce;
  const p = x.payroll;
  const m = x.movement;
  const net = m.newJoiners - m.terminations;
  const rate = a && a.scheduled ? a.present / a.scheduled : null;
  const coverage = w && w.required ? w.scheduled / w.required : null;
  const units = [...(w?.units ?? [])].sort((u, v) => v.gap - u.gap);
  const depts = x.departments.slice(0, 8);
  const biggest = depts.reduce((b, u, i) => (u.headcount > depts[b].headcount ? i : b), 0);
  const scheduledOf = (id: string) => w?.units.find((u) => u.orgUnitId === id)?.scheduled ?? null;
  const rows: AttentionRow[] = x.attention.flatMap((t) => {
    const qd = QUEUES[t.key];
    return qd ? [{ key: t.key, count: t.count, label: qd.label, icon: qd.icon, tone: qd.tone, to: qd.path(x) }] : [];
  }).filter((r) => !q || r.label.toLowerCase().includes(q.toLowerCase()));
  const runLabel = p ? RUN[p.status]?.[1] ?? p.status : '';
  return (
    <div className="oc-dash-page">
      <DashHead title="HR Dashboard" sub={`Today ${formatDate(x.date)} · what needs HR's attention, the attendance of the day and the open queues`} />
      <DashGrid>
        <DashCard span={5} icon="groups" title="Workforce" action={<CircleButton icon="account_tree" label="Organization" to="/hris/organization" />}>
          <div className="oc-dash-hero">
            <div>
              <Amount text={formatNumber(h.total)} size="hero" />
              {h.total > 0 && (
                <Delta ratio={net / h.total} text={`net this month · ${m.newJoiners} joined, ${m.terminations} left, ${m.transfers} transfers, ${m.promotions} promotions`} />
              )}
            </div>
            <HeatBars values={depts.map((u) => u.headcount)} />
          </div>
          <div className="oc-dash-actions">
            {can('hris.employee.create') && <DashButton tone="blue" icon="person_add" to="/hris/employees">Add employee</DashButton>}
            <DashButton tone="dark" icon="how_to_reg" to={attendanceLink(x.date, {})}>Attendance</DashButton>
            <DashButton tone="grey" icon="compare_arrows" to="/hris/employees?tab=changes">Movements</DashButton>
          </div>
        </DashCard>
        <DashCard span={3} icon="how_to_reg" tone="green" title="Present Today"
          action={<CircleButton arrow label="Open attendance" to={attendanceLink(x.date, { status: 'present' })} />}>
          <div className="oc-dash-figure">
            <span className="oc-dash-amount" data-size="lg">{a ? a.present : '—'}{a && <small>/{a.scheduled}</small>}</span>
            {rate != null && <span className="oc-dash-chip" data-good={rate >= 0.9}>{Math.round(rate * 100)}%</span>}
          </div>
          {rate != null && <Note tone={rate >= 0.9 ? 'good' : 'bad'}>Attendance is <b>{Math.round(rate * 100)}%</b> of the staff scheduled today.</Note>}
          <SplitStats items={[
            { label: 'Late', value: a ? a.late : '—', color: 'var(--dash-blue)', to: attendanceLink(x.date, { status: 'late' }) },
            { label: 'On leave', value: a ? a.onLeave : '—', color: 'var(--dash-lime)', to: attendanceLink(x.date, { status: 'on_leave' }) },
          ]} />
        </DashCard>
        <DashCard span={4}>
          <div className="oc-dash-inset">
            <div className="oc-dash-card-head">
              <DashIcon name="person_off" tone="red" /><h2>Absent Today</h2><span className="oc-spacer" />
              <CircleButton arrow label="Open absences" to={attendanceLink(x.date, { status: 'absent' })} />
            </div>
            <span className="oc-dash-amount" data-size="lg">{a ? a.absent : '—'}</span>
            {a && (
              <span className="oc-dash-delta-row">
                <span className="oc-dash-chip" data-good={a.missingClock === 0}>{a.missingClock}</span>
                <span className="oc-dash-chip-suffix">missing clock-in / out</span>
              </span>
            )}
          </div>
          <SegmentBar format={(v) => formatNumber(v)} parts={[
            { label: 'Permanent', value: h.permanent }, { label: 'Contract', value: h.contract }, { label: 'Probation', value: h.probation },
            ...(h.leaving ? [{ label: 'Leaving', value: h.leaving }] : []),
          ]} />
        </DashCard>

        <DashCard span={4} icon="target" title="Coverage Today" action={<CircleButton icon="tune" label="Staffing requirements" to="/hris/schedules" dot={!!w?.gap} />}>
          {!w || w.units.length === 0 ? (
            <p className="oc-dash-empty">No staffing requirement for today. Set the minimum staff per unit and shift in Shift &amp; Roster.</p>
          ) : (
            <>
              <Gauge title="Scheduled vs required" ratio={coverage} caption="Scheduled" value={formatNumber(w.scheduled)}
                sub={<>/ {formatNumber(w.required)} {w.gap === 0 && <Icon name="check_circle" size={14} />}</>} />
              <div className="oc-dash-list">
                {units.slice(0, 3).map((u) => (
                  <ProgressRow key={u.orgUnitId} label={u.name} ratio={u.required ? u.scheduled / u.required : null}
                    to={`/hris/schedules?orgUnitId=${u.orgUnitId}`} hint={`${u.scheduled} of ${u.required} scheduled`} />
                ))}
              </div>
            </>
          )}
        </DashCard>
        <DashCard span={4} icon="bar_chart" title="Headcount by Department" action={<CircleButton arrow label="Employees" to="/hris/employees" />}>
          {depts.length === 0 ? <p className="oc-dash-empty">No employee yet.</p> : (
            <BarChart aLabel="Headcount" bLabel={w ? 'Scheduled today' : undefined} format={(v) => formatNumber(v)} axis="Employees"
              points={depts.map((u, i) => ({ label: u.name.split(' ')[0].slice(0, 8), title: u.name, a: u.headcount, b: scheduledOf(u.orgUnitId),
                state: i === biggest ? 'current' : 'past' }))} />
          )}
        </DashCard>
        <div className="oc-dash-stack" data-span="4">
          {can('hris.payroll_run.view') && (
            <PromoCard span={4} badge={p ? runLabel : 'No run yet'} title={p ? `Payroll ${p.periodCode}` : 'Create the payroll run'} cta={p ? 'Open run' : 'Payroll'}
              to={p ? `/hris/payroll/runs/${p.runId}` : '/hris/payroll'} />
          )}
          <MiniCard label={p ? 'Net pay' : 'Leaving'} to={p ? `/hris/payroll/runs/${p.runId}` : '/hris/employees'}
            value={p ? <span title={money(p.net)}><Amount text={money(p.net)} size="sm" /></span> : formatNumber(h.leaving)}
            delta={p && p.warnings > 0 ? <span className="oc-dash-chip" data-good={false}>{p.warnings} warnings</span> : undefined} />
          <ReportCard label="Track & Print Report" title="HR Reports" to="/reports?module=hris" />
        </div>

        <DashTable<AttentionRow> title="Needs Attention" icon="notifications_active" rows={rows} rowKey={(r) => r.key} search={q} onSearch={setQ}
          empty="All clear: no request, exception or expiry waiting for HR." onRow={(r) => nav(r.to)} info={(r) => `Open ${r.label.toLowerCase()}`}
          columns={[
            { key: 'label', header: 'Name', render: (r) => <DashName icon={r.icon} name={r.label} tone={r.tone === 'error' ? 'red' : 'blue'} /> },
            { key: 'count', header: 'Items', render: (r) => <strong className="oc-dash-num">{formatNumber(r.count)}</strong> },
            { key: 'area', header: 'Area', render: (r) => { const [, l, i] = areaOf(r.key); return <DashName icon={i} name={l} />; } },
            { key: 'status', header: 'Status', align: 'center', render: (r) => <DashStatusPill tone={r.tone === 'error' ? 'bad' : 'warn'}>{r.tone === 'error' ? 'Urgent' : 'Pending'}</DashStatusPill> },
          ]} />
      </DashGrid>
    </div>
  );
}
