import React from 'react';
import { Link } from 'react-router';
import { download, useGet, type Page } from '@oneclub/api-client';
import { currentLocale, formatDate } from '@oneclub/i18n';
import { Card, DataTable, Empty, StatusPill, useAuth, useToast, type Option } from '@oneclub/shell';
import { money, type R } from '../p1/common';

// Employee Detail as an operational workspace (HRIS → Employees → employee):
// the tabs that bring the employee's transactions together — attendance,
// leave, overtime, payroll, loans and performance reviews — each read from
// the existing HRIS lists filtered by the employee and linking to the
// workspace where they are worked. Mounted by EmployeeDetailPage (hr.tsx).

const HR = '/api/v1/hris';
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const val = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : String(v));
const date = (v: unknown) => (v ? formatDate(String(v).slice(0, 10)) : '—');
const time = (v: unknown) => (v ? new Date(String(v)).toLocaleTimeString(currentLocale() === 'id' ? 'id-ID' : 'en-GB', { hour: '2-digit', minute: '2-digit' }) : '—');
const hours = (min: unknown) => (Number(min) ? `${(Number(min) / 60).toFixed(1)} h` : '—');
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} label={label(r[k])} />;
const withIds = (rows: unknown, key: (r: R) => string) => ((rows as R[] | undefined) ?? []).map((r) => ({ ...r, id: key(r) } as R));
const daysAgo = (n: number) => {
  const d = new Date();
  d.setDate(d.getDate() - n);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
};

/** The workspace tabs of the Employee Detail with the permission each needs. */
export const EMPLOYEE_WORK_TABS: (Option & { perm: string })[] = [
  { value: 'attendance', label: 'Attendance', perm: 'hris.attendance.view' },
  { value: 'leave', label: 'Leave', perm: 'hris.leave_request.view' },
  { value: 'overtime', label: 'Overtime', perm: 'hris.overtime_request.view' },
  { value: 'payroll', label: 'Payroll', perm: 'hris.payroll_run.view' },
  { value: 'loans', label: 'Loans & Advances', perm: 'hris.employee_loan.view' },
  { value: 'reviews', label: 'Performance', perm: 'hris.performance_review.view' },
  { value: 'assets', label: 'Assets', perm: 'inventory.asset.view' },
];

function Open({ to, children }: { to: string; children: React.ReactNode }) {
  return <Link className="oc-btn oc-btn-sm oc-btn-outline" to={to}>{children}</Link>;
}

function AttendanceTab({ id }: { id: string }) {
  const from = daysAgo(30);
  const l = useGet<Page<R>>(`${HR}/attendance-days?employeeId=${id}&from=${from}`);
  const rows = l.data?.items ?? [];
  const count = (s: string) => rows.filter((r) => r.status === s).length;
  const exceptions = rows.filter((r) => ((r.flags as string[]) ?? []).length > 0).length;
  return (
    <Card title="Attendance — last 30 days" icon="how_to_reg" actions={<Open to={`/hris/attendance?tab=days&from=${from}`}>Open Attendance</Open>}>
      <div className="oc-row-wrap" style={{ marginBottom: 12 }}>
        <StatusPill status="approved" label={`${count('present') + count('late') + count('early_leave')} present`} />
        <StatusPill status="pending" label={`${count('late')} late`} />
        <StatusPill status="error" label={`${count('absent')} absent`} />
        <StatusPill status="draft" label={`${count('on_leave')} on leave`} />
        {exceptions > 0 && <StatusPill status="warning" label={`${exceptions} exceptions`} />}
      </div>
      <DataTable rows={withIds(rows, (r) => String(r.workDate))} loading={l.isLoading} error={l.error}
        empty={<Empty title="No attendance in the last 30 days" icon="event_busy" />} columns={[
          { key: 'workDate', header: 'Date', render: (r) => date(r.workDate) },
          { key: 'shiftCode', header: 'Shift', render: (r) => (r.scheduledStart ? `${val(r.shiftCode)} ${time(r.scheduledStart)}–${time(r.scheduledEnd)}` : '—') },
          { key: 'firstIn', header: 'In', render: (r) => time(r.firstIn) }, { key: 'lastOut', header: 'Out', render: (r) => time(r.lastOut) },
          { key: 'status', header: 'Status', render: pill('status') }, { key: 'workedMinutes', header: 'Worked', render: (r) => hours(r.workedMinutes) },
          { key: 'flags', header: 'Exceptions', render: (r) => ((r.flags as string[]) ?? []).map(label).join(', ') || '—' },
        ]} />
    </Card>
  );
}

function LeaveTab({ id }: { id: string }) {
  const { can } = useAuth();
  const bal = useGet<Page<R>>(can('hris.leave_balance.view') ? `${HR}/leave-balances?employeeId=${id}` : null);
  const req = useGet<Page<R>>(`${HR}/leave-requests?employeeId=${id}`);
  return (
    <div className="oc-stack">
      {can('hris.leave_balance.view') && (
        <Card title="Leave balances" icon="account_balance_wallet" actions={<Open to="/hris/leave?tab=balances">All balances</Open>}>
          <DataTable rows={bal.data?.items} loading={bal.isLoading} error={bal.error} empty={<Empty title="No leave balance yet" />} columns={[
            { key: 'leaveType', header: 'Type', render: (r) => label(r.leaveType) }, { key: 'year', header: 'Year', align: 'right' },
            { key: 'entitled', header: 'Entitled', align: 'right' }, { key: 'carriedOver', header: 'Carried over', align: 'right' },
            { key: 'used', header: 'Used', align: 'right' }, { key: 'pending', header: 'Pending', align: 'right' },
            { key: 'available', header: 'Available', align: 'right', render: (r) => <strong>{String(r.available)}</strong> },
          ]} />
        </Card>
      )}
      <Card title="Leave requests" icon="beach_access" actions={<Open to="/hris/leave?tab=requests">Leave workspace</Open>}>
        <DataTable rows={req.data?.items} loading={req.isLoading} error={req.error} empty={<Empty title="No leave request" />} columns={[
          { key: 'number', header: 'Request' }, { key: 'leaveType', header: 'Type', render: (r) => label(r.leaveType) },
          { key: 'startDate', header: 'Period', render: (r) => `${date(r.startDate)} – ${date(r.endDate)}` }, { key: 'days', header: 'Days', align: 'right' },
          { key: 'reason', header: 'Reason', render: (r) => val(r.reason) }, { key: 'status', header: 'Status', render: pill('status') },
        ]} />
      </Card>
    </div>
  );
}

function OvertimeTab({ id }: { id: string }) {
  const l = useGet<Page<R>>(`${HR}/overtime-requests?employeeId=${id}`);
  return (
    <Card title="Overtime" icon="more_time" actions={<Open to="/hris/overtime?tab=requests">Overtime workspace</Open>}>
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} empty={<Empty title="No overtime" />} columns={[
        { key: 'number', header: 'Request' }, { key: 'workDate', header: 'Day', render: (r) => `${date(r.workDate)} ${time(r.startsAt)}–${time(r.endsAt)}` },
        { key: 'hours', header: 'Hours', align: 'right' }, { key: 'payableHours', header: 'Payable', align: 'right' },
        { key: 'reason', header: 'Reason', render: (r) => val(r.reason) }, { key: 'status', header: 'Status', render: pill('status') },
      ]} />
    </Card>
  );
}

function PayrollTab({ id }: { id: string }) {
  const { can } = useAuth();
  const toast = useToast();
  const slips = useGet<Page<R>>(`${HR}/payslips?employeeId=${id}`);
  const adj = useGet<Page<R>>(can('hris.payroll_adjustment.view') ? `${HR}/payroll-adjustments?employeeId=${id}` : null);
  return (
    <div className="oc-stack">
      <Card title="Payslips" icon="receipt_long" actions={<Open to="/hris/payroll">Payroll</Open>}>
        <DataTable rows={slips.data?.items} loading={slips.isLoading} error={slips.error} empty={<Empty title="No payslip yet" icon="receipt_long" />} columns={[
          { key: 'periodCode', header: 'Period' },
          { key: 'runNumber', header: 'Run', render: (r) => <Link to={`/hris/payroll/runs/${String(r.runId)}`}>{String(r.runNumber)}</Link> },
          { key: 'runType', header: 'Type', render: (r) => label(r.runType) }, { key: 'gross', header: 'Gross', align: 'right', render: (r) => money(r.gross) },
          { key: 'pph21', header: 'PPh 21', align: 'right', render: (r) => money(r.pph21) },
          { key: 'net', header: 'Net', align: 'right', render: (r) => <strong>{money(r.net)}</strong> },
          { key: 'runStatus', header: 'Run status', render: pill('runStatus') },
        ]} actions={(r) => (
          <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => download('GET', `${HR}/payslips/${r.id}/pdf`, undefined, `payslip-${String(r.periodCode)}.pdf`)
            .catch((e: Error) => toast(e.message, 'error'))}>PDF</button>
        )} />
      </Card>
      {can('hris.payroll_adjustment.view') && (
        <Card title="Payroll adjustments & bonuses" icon="tune">
          <DataTable rows={adj.data?.items} loading={adj.isLoading} error={adj.error} empty={<Empty title="No adjustment" />} columns={[
            { key: 'number', header: 'No.' }, { key: 'periodCode', header: 'Period' }, { key: 'componentName', header: 'Component' },
            { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }, { key: 'reason', header: 'Reason', render: (r) => val(r.reason) },
            { key: 'status', header: 'Status', render: pill('status') },
          ]} />
        </Card>
      )}
    </div>
  );
}

function LoansTab({ id }: { id: string }) {
  const l = useGet<Page<R>>(`${HR}/loans?employeeId=${id}`);
  return (
    <Card title="Loans & cash advances" icon="savings" actions={<Open to="/hris/payroll?tab=loans">Manage</Open>}>
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} empty={<Empty title="No loan or cash advance" />} columns={[
        { key: 'number', header: 'Number', render: (r) => val(r.number ?? r.reference) }, { key: 'loanType', header: 'Type', render: (r) => label(r.loanType) },
        { key: 'principal', header: 'Principal', align: 'right', render: (r) => money(r.principal) },
        { key: 'installment', header: 'Installment', align: 'right', render: (r) => money(r.installment) },
        { key: 'repaid', header: 'Repaid', align: 'right', render: (r) => money(r.repaid) },
        { key: 'outstanding', header: 'Outstanding', align: 'right', render: (r) => <strong>{money(r.outstanding)}</strong> },
        { key: 'status', header: 'Status', render: pill('status') },
      ]} />
    </Card>
  );
}

function ReviewsTab({ id }: { id: string }) {
  const l = useGet<Page<R>>(`${HR}/employees/${id}/reviews`);
  return (
    <Card title="Performance reviews" icon="insights" actions={<Open to="/hris/performance">Review cycles</Open>}>
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} empty={<Empty title="No review yet" />} columns={[
        { key: 'cycleName', header: 'Cycle', render: (r) => <Link to={`/hris/performance/reviews/${String(r.id)}`}>{String(r.cycleName)}</Link> },
        { key: 'periodStart', header: 'Period', render: (r) => `${date(r.periodStart)} – ${date(r.periodEnd)}` },
        { key: 'reviewerName', header: 'Reviewer', render: (r) => val(r.reviewerName) },
        { key: 'finalScore', header: 'Score', align: 'right', render: (r) => val(r.finalScore ?? r.managerScore) },
        { key: 'finalRating', header: 'Rating', render: (r) => val(r.finalRating ?? r.recommendedRating) },
        { key: 'status', header: 'Status', render: pill('status') },
      ]} />
    </Card>
  );
}

// Assets in the employee's custody (Inventory asset register, custodian set
// on the asset; returned at offboarding, FR-HR-06).
function AssetsTab({ id }: { id: string }) {
  const l = useGet<Page<R>>(`/api/v1/inventory/assets?filter[custodianEmployeeId]=${id}&limit=100`);
  return (
    <Card title="Assets in custody" icon="inventory_2" actions={<Open to="/inventory/assets">Asset register</Open>}>
      <DataTable rows={(l.data?.items ?? []).filter((a) => a.status !== 'disposed')} loading={l.isLoading} error={l.error}
        empty={<Empty title="No asset in custody" />} columns={[
          { key: 'code', header: 'Code' }, { key: 'name', header: 'Asset' },
          { key: 'brand', header: 'Brand / model', render: (r) => [r.brand, r.model].filter(Boolean).map(String).join(' ') || '—' },
          { key: 'serialNo', header: 'Serial No.', render: (r) => val(r.serialNo) },
          { key: 'acquisitionDate', header: 'Acquired', render: (r) => date(r.acquisitionDate) },
          { key: 'status', header: 'Status', render: pill('status') },
        ]} />
    </Card>
  );
}

/** The content of one workspace tab of the Employee Detail (null for other tabs). */
export function EmployeeWorkTab({ tab, id }: { tab: string; id: string }) {
  switch (tab) {
    case 'attendance': return <AttendanceTab id={id} />;
    case 'leave': return <LeaveTab id={id} />;
    case 'overtime': return <OvertimeTab id={id} />;
    case 'payroll': return <PayrollTab id={id} />;
    case 'loans': return <LoansTab id={id} />;
    case 'reviews': return <ReviewsTab id={id} />;
    case 'assets': return <AssetsTab id={id} />;
    default: return null;
  }
}
