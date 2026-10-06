import React from 'react';
import { Link, useNavigate } from 'react-router';
import { useGet, type Schemas } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import { Card, Empty, ErrorAlert, Icon, PageHeader, Skeleton, StatTile, StatusPill, useAuth } from '@oneclub/shell';
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
};

const RUN: Record<string, [string, string]> = {
  draft: ['draft', 'Draft'], calculated: ['pending', 'Calculated'], submitted: ['pending', 'Under review'], approved: ['approved', 'Approved'],
  posted: ['confirmed', 'Posted to Finance'], paid: ['active', 'Paid'],
};

/** Clickable line of a card: label, count and the workspace it opens. */
function Line({ to, icon, label, value, tone }: { to: string; icon: string; label: string; value: React.ReactNode; tone?: string }) {
  return (
    <Link to={to} className="oc-fin-line" style={{ color: 'inherit', textDecoration: 'none' }}>
      <span><Icon name={icon} size={16} /> {label}</span>
      <span className="oc-row" style={{ gap: 8 }}>
        {tone ? <StatusPill status={tone} label={String(value)} /> : <span className="oc-num" style={{ fontWeight: 600 }}>{value}</span>}
        <Icon name="chevron_right" size={16} />
      </span>
    </Link>
  );
}

/** Horizontal bars of counts (single hue); each bar opens its drill-down. */
function CountBars({ rows }: { rows: { key: string; label: string; value: number; to: string }[] }) {
  const max = Math.max(1, ...rows.map((r) => r.value));
  return (
    <div className="oc-fin-bars">
      {rows.map((r) => (
        <Link key={r.key} to={r.to} className="oc-fin-bar" title={`${r.label}: ${r.value}`} style={{ color: 'inherit', textDecoration: 'none' }}>
          <span className="oc-fin-bar-label">{r.label}</span>
          <span className="oc-fin-bar-track"><span style={{ width: `${(r.value / max) * 100}%` }} /></span>
          <span className="oc-fin-bar-value oc-num">{r.value}</span>
        </Link>
      ))}
    </div>
  );
}

function AttendanceCard({ d }: { d: Dashboard }) {
  const a = d.attendance;
  if (!a) return null;
  return (
    <Card title="Attendance today" icon="how_to_reg"
      actions={<Link className="oc-btn oc-btn-sm oc-btn-outline" to={attendanceLink(d.date, {})}>Open Attendance</Link>}>
      <div className="oc-stat-value" style={{ marginBottom: 12 }}>{a.present} <span className="oc-muted oc-small">of {a.scheduled} scheduled</span></div>
      <Line to={attendanceLink(d.date, { status: 'present' })} icon="check_circle" label="Present" value={a.present} />
      <Line to={attendanceLink(d.date, { status: 'late' })} icon="schedule" label="Late" value={a.late} tone={a.late ? 'pending' : undefined} />
      <Line to={attendanceLink(d.date, { status: 'absent' })} icon="person_off" label="Absent" value={a.absent} tone={a.absent ? 'error' : undefined} />
      <Line to={attendanceLink(d.date, { flag: 'missing' })} icon="report" label="Missing clock-in / out" value={a.missingClock}
        tone={a.missingClock ? 'error' : undefined} />
      <Line to={attendanceLink(d.date, { status: 'on_leave' })} icon="beach_access" label="On leave" value={a.onLeave} />
    </Card>
  );
}

function WorkforceCard({ d }: { d: Dashboard }) {
  const w = d.workforce;
  if (!w) return null;
  return (
    <Card title="Workforce coverage today" icon="groups"
      actions={<Link className="oc-btn oc-btn-sm oc-btn-outline" to="/hris/schedules">Open Schedules</Link>}>
      <div className="oc-row" style={{ gap: 24, marginBottom: 12 }}>
        <div><div className="oc-small oc-muted">Required</div><div className="oc-stat-value">{w.required}</div></div>
        <div><div className="oc-small oc-muted">Scheduled</div><div className="oc-stat-value">{w.scheduled}</div></div>
        <div><div className="oc-small oc-muted">Gap</div><div className="oc-stat-value" style={{ color: w.gap ? 'var(--md-sys-color-error)' : undefined }}>{w.gap}</div></div>
      </div>
      {w.units.length === 0 ? (
        <Empty title="No staffing requirement for today" help="Set the minimum staff per unit and shift in Schedules → Staffing Requirements." />
      ) : (
        <div className="oc-table-wrap">
          <table className="oc-table oc-small">
            <thead><tr><th>Unit</th><th style={{ textAlign: 'right' }}>Required</th><th style={{ textAlign: 'right' }}>Scheduled</th><th style={{ textAlign: 'right' }}>Gap</th></tr></thead>
            <tbody>
              {w.units.map((u) => (
                <tr key={u.orgUnitId}>
                  <td><Link to={`/hris/schedules?orgUnitId=${u.orgUnitId}`}>{u.name}</Link></td>
                  <td className="oc-num" style={{ textAlign: 'right' }}>{u.required}</td>
                  <td className="oc-num" style={{ textAlign: 'right' }}>{u.scheduled}</td>
                  <td style={{ textAlign: 'right' }}>{u.gap ? <StatusPill status="error" label={String(u.gap)} /> : <StatusPill status="approved" label="Covered" />}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function AttentionCard({ d }: { d: Dashboard }) {
  return (
    // Anchor of the Pending HR Requests tile.
    <div id="hr-attention">
      <Card title="Needs attention" icon="notifications_active">
        {d.attention.length === 0 ? <Empty title="All clear" help="No request, exception or expiry waiting for HR." icon="task_alt" /> : (
          <div className="oc-stack" style={{ gap: 0 }}>
            {d.attention.map((x) => {
              const q = QUEUES[x.key];
              return q ? <Line key={x.key} to={q.path(d)} icon={q.icon} label={q.label} value={x.count} tone={q.tone} /> : null;
            })}
          </div>
        )}
      </Card>
    </div>
  );
}

function MovementCard({ d }: { d: Dashboard }) {
  const m = d.movement;
  const changes = '/hris/employees?tab=changes';
  return (
    <Card title="Employee movement this month" icon="compare_arrows" actions={<Link className="oc-btn oc-btn-sm oc-btn-outline" to={changes}>Employment changes</Link>}>
      <Line to={changes} icon="person_add" label="New joiners" value={m.newJoiners} />
      <Line to={changes} icon="swap_horiz" label="Transfers & rotations" value={m.transfers} />
      <Line to={changes} icon="trending_up" label="Promotions & demotions" value={m.promotions} />
      <Line to={changes} icon="logout" label="Resignations & terminations" value={m.terminations} />
    </Card>
  );
}

function PayrollCard({ d }: { d: Dashboard }) {
  const p = d.payroll;
  const { can } = useAuth();
  if (!can('hris.payroll_run.view')) return null;
  const [tone, l] = p ? RUN[p.status] ?? ['draft', p.status] : ['draft', ''];
  return (
    <Card title="Payroll" icon="payments" actions={<Link className="oc-btn oc-btn-sm oc-btn-outline" to={p ? `/hris/payroll/runs/${p.runId}` : '/hris/payroll'}>Open Payroll</Link>}>
      {!p ? <Empty title="No payroll run yet" help="Create the run of the period in HRIS → Payroll." icon="payments" /> : (
        <>
          <div className="oc-row" style={{ justifyContent: 'space-between', marginBottom: 12 }}>
            <div><div className="oc-small oc-muted">{p.number} · period {p.periodCode}</div><div className="oc-stat-value">{money(p.net)}</div></div>
            <StatusPill status={tone} label={l} />
          </div>
          <div className="oc-fin-line"><span>Employees</span><span className="oc-num">{p.headcount}</span></div>
          <div className="oc-fin-line"><span>Payment date</span><span>{formatDate(p.paymentDate)}</span></div>
          <div className="oc-fin-line"><span>Calculation warnings</span>
            {p.warnings ? <StatusPill status="error" label={String(p.warnings)} /> : <StatusPill status="approved" label="None" />}</div>
        </>
      )}
    </Card>
  );
}

export function HRDashboardPage() {
  const nav = useNavigate();
  const { can } = useAuth();
  const d = useGet<Dashboard>(API);
  const x = d.data;
  const requests = x?.attention.filter((a) => ['leave_requests', 'permission_requests', 'overtime_requests', 'shift_swaps', 'profile_changes',
    'attendance_corrections'].includes(a.key)).reduce((n, a) => n + a.count, 0) ?? 0;
  const count = (key: string) => x?.attention.find((a) => a.key === key)?.count ?? 0;
  return (
    <div className="oc-stack">
      <PageHeader title="HR Dashboard" help={x ? `Today ${formatDate(x.date)} · what needs HR's attention, the attendance of the day and the open queues` : undefined}
        actions={<Link className="oc-btn oc-btn-neutral" to="/hris/employees"><Icon name="badge" size={18} /> Employees</Link>} />
      <ErrorAlert error={d.error} />
      {!x && !d.error && <Skeleton rows={8} />}
      {x && (
        <>
          <div className="oc-stat-grid">
            <StatTile label="Total Headcount" icon="groups" value={x.headcount.total} onOpen={() => nav('/hris/employees')}
              title={`${x.headcount.permanent} permanent · ${x.headcount.contract} contract · ${x.headcount.probation} probation`} />
            {x.attendance && (
              <>
                <StatTile label="Present Today" icon="how_to_reg" value={`${x.attendance.present} / ${x.attendance.scheduled}`}
                  onOpen={() => nav(attendanceLink(x.date, {}))} />
                <StatTile label="On Leave" icon="beach_access" value={x.attendance.onLeave} onOpen={() => nav(attendanceLink(x.date, { status: 'on_leave' }))} />
                <StatTile label="Absent" icon="person_off" value={x.attendance.absent} onOpen={() => nav(attendanceLink(x.date, { status: 'absent' }))} />
              </>
            )}
            {x.workforce && <StatTile label="Workforce Gap" icon="group" value={x.workforce.gap} onOpen={() => nav('/hris/schedules')} />}
            {can('hris.overtime_request.view') && <StatTile label="Overtime Pending" icon="more_time" value={count('overtime_requests')} onOpen={() => nav('/hris/overtime?tab=requests')} />}
            {can('hris.contract.view') && <StatTile label="Contracts Expiring" icon="contract" value={count('contracts_expiring')} onOpen={() => nav('/hris/employees?tab=contracts&expiring=1')} />}
            <StatTile label="Pending HR Requests" icon="pending_actions" value={requests}
              onOpen={() => document.getElementById('hr-attention')?.scrollIntoView({ behavior: 'smooth' })} />
          </div>

          <div className="oc-fin-grid">
            <AttentionCard d={x} />
            <AttendanceCard d={x} />
          </div>
          <div className="oc-fin-grid">
            <WorkforceCard d={x} />
            <PayrollCard d={x} />
          </div>
          <div className="oc-fin-grid">
            <MovementCard d={x} />
            <Card title="Headcount by department" icon="account_tree" actions={<Link className="oc-btn oc-btn-sm oc-btn-outline" to="/hris/organization">Organization</Link>}>
              {x.departments.length === 0 ? <Empty title="No employee yet" /> : (
                <CountBars rows={x.departments.slice(0, 12).map((u) => ({ key: u.orgUnitId, label: u.name, value: u.headcount, to: `/hris/employees?orgUnitId=${u.orgUnitId}` }))} />
              )}
              <h3 className="oc-fin-sub">Employment status</h3>
              <CountBars rows={[
                { key: 'permanent', label: 'Permanent', value: x.headcount.permanent, to: '/hris/employees?employmentStatus=permanent' },
                { key: 'contract', label: 'Contract', value: x.headcount.contract, to: '/hris/employees?employmentStatus=contract' },
                { key: 'probation', label: 'Probation', value: x.headcount.probation, to: '/hris/employees?employmentStatus=probation' },
                { key: 'leaving', label: 'Leaving (scheduled)', value: x.headcount.leaving, to: '/hris/employees?terminationStatus=scheduled' },
              ]} />
            </Card>
          </div>
        </>
      )}
    </div>
  );
}
