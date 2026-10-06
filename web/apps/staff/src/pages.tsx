import { useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime, formatNumber, formatRelative } from '@oneclub/i18n';
import {
  Amount, CircleButton, ComingSoonPage, DashButton, DashCard, DashGrid, DashHead, DashIcon, DashName, DashStatusPill, DashTable, DataTable, ErrorAlert,
  HeatBars, Icon, MiniCard, Note, PageHeader, PromoCard, ReportCard, SegmentBar, SelectField, Skeleton, SplitStats, StatusPill, TextField, useAuth,
  useBootstrap, useToast, type DashStatus,
} from '@oneclub/shell';
import { FinanceDashboardPage } from './p4/finance-dashboard';
import { HRDashboardPage } from './p5/hr-dashboard';

/**
 * Back Office home: finance roles without the Management Dashboard (the
 * Accountant) open the Finance Dashboard, HR roles (HR Manager, HR Admin)
 * the HR Dashboard; everyone else the general home.
 */
export function DashboardPage() {
  const { can } = useAuth();
  if (can('reporting.dashboard.view')) return <HomeDashboard />;
  if (can('accounting.dashboard.view')) return <FinanceDashboardPage />;
  return can('hris.employee.create') ? <HRDashboardPage /> : <HomeDashboard />;
}

/** Count of a list page: "50+" when more pages follow. */
const countOf = (p?: Page<unknown>) => (p ? `${p.total ?? p.items.length}${p.nextCursor && p.total == null ? '+' : ''}` : '—');

const REQUEST_TONE: Record<string, DashStatus> = { approved: 'good', rejected: 'bad', cancelled: 'neutral', pending: 'warn', submitted: 'warn', revision: 'warn' };

/** Back Office home of every role without a domain dashboard, on the dashboard kit. */
function HomeDashboard() {
  const nav = useNavigate();
  const { me, can } = useAuth();
  const boot = useBootstrap();
  const [q, setQ] = useState('');
  const inbox = useGet<Page<Schemas['Request']>>('/api/v1/platform/approvals?box=inbox&limit=50');
  const mine = useGet<Page<Schemas['Request']>>('/api/v1/platform/approvals?box=mine&limit=50');
  const unread = useGet<{ unread: number }>('/api/v1/platform/notifications/unread-count');
  const overview = useGet<Schemas['Dashboard']>(can('reporting.dashboard.view') ? '/api/v1/reporting/dashboards/executive-overview' : null);
  const hour = new Date().getHours();
  const greet = hour < 11 ? 'Good morning' : hour < 15 ? 'Good afternoon' : 'Good evening';
  const live = overview.data?.widgets.filter((w) => w.status === 'available') ?? [];
  const waiting = inbox.data?.items ?? [];
  const sent = mine.data?.items ?? [];
  const open = sent.filter((r) => !['approved', 'rejected', 'cancelled'].includes(String(r.status))).length;
  const rows = waiting.filter((r) => !q || `${r.title} ${r.requesterName}`.toLowerCase().includes(q.toLowerCase()));
  const oldest = waiting.reduce<string | null>((o, r) => (!o || String(r.createdAt) < o ? String(r.createdAt) : o), null);
  return (
    <div className="oc-dash-page">
      <DashHead title={`${greet}, ${me?.fullName.split(' ')[0] ?? ''}`} sub={`${boot.branding.appName} · ${boot.timezone}`} />
      <DashGrid>
        <DashCard span={5} icon="approval" title="Waiting for My Decision" action={<CircleButton arrow label="Open approvals" to="/approvals" />}>
          <div className="oc-dash-hero">
            <div>
              <Amount text={countOf(inbox.data)} size="hero" />
              <span className="oc-dash-delta">{oldest ? <>Oldest submitted <strong>{formatRelative(oldest)}</strong></> : 'Nothing waiting for you.'}</span>
            </div>
            <HeatBars values={live.slice(0, 8).map((w) => Number(w.value ?? 0))} />
          </div>
          <div className="oc-dash-actions">
            <DashButton tone="blue" icon="approval" to="/approvals">Approvals</DashButton>
            <DashButton tone="dark" icon="notifications" to="/notifications">Notifications</DashButton>
            <DashButton tone="grey" icon="person" to="/profile">Profile</DashButton>
          </div>
        </DashCard>
        <DashCard span={3} icon="notifications" tone="green" title="Notifications" action={<CircleButton arrow label="View all" to="/notifications" />}>
          <span className="oc-dash-amount" data-size="lg">{unread.data?.unread ?? '—'}</span>
          <Note tone={unread.data?.unread ? 'bad' : 'good'}><b>{unread.data?.unread ?? 0}</b> unread notification(s).</Note>
          <SplitStats items={[
            { label: 'My requests', value: countOf(mine.data), color: 'var(--dash-blue)', to: '/approvals?box=mine' },
            { label: 'Still open', value: open, color: 'var(--dash-lime)', to: '/approvals?box=mine' },
          ]} />
        </DashCard>
        <DashCard span={4}>
          <div className="oc-dash-inset">
            <div className="oc-dash-card-head"><DashIcon name="today" tone="blue" /><h2>{live[0]?.label ?? 'Today'}</h2><span className="oc-spacer" />
              {can('reporting.dashboard.view') && <CircleButton arrow label="Management" to="/management?view=today" />}</div>
            <span className="oc-dash-amount" data-size="lg">{live[0] ? formatNumber(live[0].value ?? 0) : '—'}</span>
            <span className="oc-dash-sub">{live.length ? 'Live figures of the property today' : 'Live figures show for management roles.'}</span>
          </div>
          {live.length > 1 && <SegmentBar parts={live.slice(1, 4).map((w) => ({ label: w.label, value: Number(w.value ?? 0) }))} format={(v) => formatNumber(v)} />}
        </DashCard>

        <DashTable<Schemas['Request']> span={8} title="Waiting for My Decision" icon="pending_actions" rows={rows} rowKey={(r) => String(r.id)} search={q} onSearch={setQ}
          empty="Nothing waiting for you." onRow={(r) => nav(`/approvals/${r.id}`)} info={(r) => String(r.title)}
          columns={[
            { key: 'title', header: 'Name', render: (r) => <DashName icon="description" name={String(r.title)} /> },
            { key: 'requesterName', header: 'Requested by', render: (r) => String(r.requesterName ?? '—') },
            { key: 'createdAt', header: 'Submitted', render: (r) => formatRelative(String(r.createdAt)) },
            { key: 'status', header: 'Status', align: 'center', render: (r) => <DashStatusPill tone={REQUEST_TONE[String(r.status)] ?? 'neutral'}>{String(r.status).replace(/_/g, ' ')}</DashStatusPill> },
          ]} />
        <div className="oc-dash-stack" data-span="4">
          {can('reporting.dashboard.view')
            ? <PromoCard span={4} badge="Management" title="Executive Overview" cta="Open" to="/management" />
            : <PromoCard span={4} badge="Reports" title="Reports of my modules" cta="Open" to="/reports" />}
          <MiniCard label="My open requests" value={<Amount text={String(open)} size="sm" />} to="/approvals?box=mine" />
          <ReportCard label="Track & Print Report" title="Reports" to="/reports" />
        </div>
      </DashGrid>
    </div>
  );
}

/** Module landing pages (Naming Convention §5) — features arrive in later phases. */
export const MODULE_PAGES: { path: string; title: string; phase: string; features: string[] }[] = [
  { path: 'golf', title: 'Golf', phase: 'P1', features: ['Tee Sheet', 'Bookings', 'Flights', 'Caddies', 'Golf Carts', 'Courses', 'Check-in', 'Starter', 'Driving Range'] },
  { path: 'sport-club', title: 'Sport Club', phase: 'P3', features: ['Facilities', 'Bookings', 'Classes', 'Instructors', 'Access', 'Lockers'] },
  { path: 'membership', title: 'Membership', phase: 'P1', features: ['Members', 'Membership Application', 'Membership Lifecycle', 'Member Card'] },
  { path: 'booking', title: 'Booking', phase: 'P1', features: ['Golf', 'Sport Club', 'Bungalow', 'VIP Suite', 'Meeting Room'] },
  { path: 'stay-venue', title: 'Stay & Venue', phase: 'P3', features: ['Bungalows', 'VIP Suites', 'Meeting Rooms'] },
  { path: 'banquet-event', title: 'Banquet & Event', phase: 'P3', features: ['Events', 'Banquet', 'BEO'] },
  { path: 'crm', title: 'CRM', phase: 'P4', features: ['Customers', 'Sales', 'Customer Engagement', 'Loyalty', 'Top Spender'] },
  { path: 'commercial', title: 'Commercial', phase: 'P2', features: ['POS', 'Pricing & Promotion', 'Package', 'Voucher & Prepaid'] },
  { path: 'inventory', title: 'Inventory', phase: 'P4', features: ['Stock Balance', 'Receiving', 'Issuing', 'Transfer', 'Stock Opname'] },
  { path: 'procurement', title: 'Procurement', phase: 'P4', features: ['Purchase Requisitions', 'Purchase Orders', 'Goods Receipt', 'Suppliers'] },
  { path: 'accounting', title: 'Accounting', phase: 'P2', features: ['General Accounting', 'Accounts Receivable', 'Accounts Payable', 'Cash & Bank', 'Financial Reports'] },
  { path: 'hris', title: 'Human Resources', phase: 'P5', features: ['Employees', 'Attendance', 'Payroll'] },
];

export function ModulePage({ path }: { path: string }) {
  const m = MODULE_PAGES.find((x) => x.path === path)!;
  return <ComingSoonPage title={m.title} phase={m.phase} features={m.features} />;
}

// ── Reports (EP-10) ───────────────────────────────────────────────────────

const REPORT_GROUPS: Record<string, { title: string; match: (r: Schemas['ReportInfo']) => boolean }> = {
  golf: { title: 'Golf Reports', match: (r) => r.module === 'golf' && r.code !== 'golf.bookings' && r.code !== 'golf.no_show_cancellation' },
  booking: { title: 'Booking Reports', match: (r) => r.code === 'golf.bookings' || r.code === 'golf.no_show_cancellation' },
  membership: { title: 'Membership Reports', match: (r) => r.module === 'membership' },
  billing: { title: 'Operational Reports', match: (r) => r.module === 'billing' || r.module === 'platform' },
  'billing-reports': { title: 'Billing Reports', match: (r) => r.module === 'billing' },
};

export function ReportsPage() {
  const reports = useGet<Page<Schemas['ReportInfo']>>('/api/v1/reporting/reports');
  const [search] = useSearchParams();
  const group = REPORT_GROUPS[search.get('module') ?? ''];
  return (
    <div className="oc-stack">
      <PageHeader title={group?.title ?? 'Reports'} help="Reports run on the read replica so they never slow down transactions." />
      {reports.isLoading && <Skeleton />}
      <div className="oc-grid">
        {reports.data?.items.filter((r) => !group || group.match(r)).map((r) => (
          <Link key={r.code} to={`/reports/${r.code}`} className="oc-card" style={{ textDecoration: 'none' }}>
            <div className="oc-card-head"><span className="oc-icon-circle"><Icon name="table_chart" size={20} /></span><h3>{r.name}</h3></div>
            <div className="oc-small oc-muted">{r.description}</div>
          </Link>
        ))}
      </div>
    </div>
  );
}

export function ReportRunPage() {
  const { code } = useParams();
  const toast = useToast();
  const [params, setParams] = useState<Record<string, string>>({});
  const [applied, setApplied] = useState<Record<string, string>>({});
  const q = Object.fromEntries(Object.entries(applied).map(([k, v]) => [`params[${k}]`, v]));
  const res = useGet<Schemas['Result']>(`/api/v1/reporting/reports/${code}${qs(q)}`);
  const exp = useSend<Record<string, unknown>>('POST', '/api/v1/reporting/exports', ['/api/v1/reporting/exports'], () => ({ 'Idempotency-Key': crypto.randomUUID() }));
  const r = res.data;
  return (
    <div className="oc-stack">
      <PageHeader title={r?.report.name ?? 'Report'} help={r?.report.description} actions={<>
        <button className="oc-btn oc-btn-outline" onClick={() => exp.mutate({ reportCode: code, format: 'csv', params: applied }, { onSuccess: () => toast('Export started — you will be notified when it is ready') })}>Export CSV</button>
        <button className="oc-btn oc-btn-outline" onClick={() => exp.mutate({ reportCode: code, format: 'xlsx', params: applied }, { onSuccess: () => toast('Export started — you will be notified when it is ready') })}>Export XLSX</button>
        <button className="oc-btn oc-btn-outline" onClick={() => exp.mutate({ reportCode: code, format: 'pdf', params: applied }, { onSuccess: () => toast('Export started — you will be notified when it is ready') })}>Export PDF</button>
      </>} />
      <ErrorAlert error={res.error ?? exp.error} />
      {r && (
        <div className="oc-card">
          <form className="oc-toolbar" onSubmit={(e) => { e.preventDefault(); setApplied(params); }}>
            {r.report.params.map((p) => p.type === 'enum'
              ? <div key={p.key} style={{ width: 180 }}><SelectField label={p.label} value={params[p.key] ?? ''} onChange={(v) => setParams({ ...params, [p.key]: v })} placeholder="All"
                options={(p.enum ?? []).map((x) => ({ value: x, label: x }))} /></div>
              : <div key={p.key} style={{ width: 220 }}><TextField label={p.label} value={params[p.key] ?? ''} onChange={(v) => setParams({ ...params, [p.key]: v })} /></div>)}
            <button className="oc-btn oc-btn-ink" style={{ alignSelf: 'flex-end' }}>Apply</button>
            <span className="oc-spacer" />
            <span className="oc-small oc-muted">Source: {r.source} · {formatDateTime(r.generatedAt)}{r.truncated && ' · first 1,000 rows (export for all)'}</span>
          </form>
          <DataTable rows={r.rows as Record<string, unknown>[]} rowKey={(x) => JSON.stringify(x)}
            columns={r.report.columns.map((c) => ({
              key: c.key, header: c.label,
              render: (row: Record<string, unknown>) => {
                const v = row[c.key];
                if (c.type === 'datetime') return v ? formatDateTime(String(v)) : '—';
                if (c.type === 'boolean') return v ? 'Yes' : 'No';
                if (c.key === 'status' || c.key === 'userStatus') return <StatusPill status={String(v)} />;
                return v === null || v === undefined ? '—' : String(v);
              },
            }))} />
        </div>
      )}
    </div>
  );
}

export function ExportsPage() {
  const list = useGet<Page<Schemas['Export']>>('/api/v1/reporting/exports', { refetchInterval: 5000 });
  return (
    <div className="oc-stack">
      <PageHeader title="Exports" help="Large exports run in the background; you are notified when the file is ready." />
      <div className="oc-card">
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading}
          columns={[{ key: 'createdAt', header: 'Requested', render: (e) => formatDateTime(String(e.createdAt)) }, { key: 'reportCode', header: 'Report' },
            { key: 'format', header: 'Format', render: (e) => String(e.format).toUpperCase() }, { key: 'rowCount', header: 'Rows', align: 'right' },
            { key: 'status', header: 'Status', render: (e) => <StatusPill status={String(e.status)} /> }]}
          actions={(e) => e.fileUrl ? <a className="oc-btn oc-btn-outline oc-btn-sm" href={String(e.fileUrl)}><Icon name="download" size={18} /> Download</a> : null} />
      </div>
    </div>
  );
}

// ── Management Dashboard (FR-REP-04) ─────────────────────────────────────

export function SettingsHomePage() {
  const { can } = useAuth();
  const items: [string, string, string, string][] = [
    ['organization', 'Organization', 'domain', 'platform.organization.view'], ['customer-instance', 'Customer Instance', 'dns', 'platform.instance.view'],
    ['venues', 'Venues', 'location_on', 'platform.venue.view'], ['courses', 'Courses', 'golf_course', 'golf.course.view'],
    ['users', 'Users', 'group', 'platform.user.view'], ['roles', 'Roles & Permissions', 'admin_panel_settings', 'platform.role.view'],
    ['features', 'Features', 'apps', 'platform.module.view'], ['feature-configuration', 'Feature Configuration', 'toggle_on', 'platform.feature_flag.view'],
    ['business-rules', 'Business Rules', 'rule', 'platform.business_rule.view'], ['club-policies', 'Club Policies', 'policy', 'platform.club_policy.view'],
    ['notifications', 'Notifications', 'notifications', 'platform.notification_template.view'], ['integrations', 'Integrations', 'hub', 'platform.integration.view'],
    ['payment-methods', 'Payment Methods', 'payments', 'billing.payment_method.view'], ['tax-service', 'Tax & Service', 'percent', 'commercial.tax_service.view'],
    ['approval-workflows', 'Approval Workflows', 'account_tree', 'platform.approval_workflow.view'], ['audit-logs', 'Audit Logs', 'history', 'audit.log.view'],
    ['localization', 'Localization', 'translate', 'platform.localization.update'], ['branding', 'Branding', 'palette', 'platform.branding.update'],
    ['system', 'System Settings', 'settings_applications', 'platform.system_settings.view'],
  ];
  return (
    <div className="oc-stack">
      <PageHeader title="Settings" />
      <div className="oc-grid">
        {items.filter((i) => can(i[3])).map(([p, label, icon]) => (
          <Link key={p} to={`/settings/${p}`} className="oc-card" style={{ textDecoration: 'none' }}>
            <div className="oc-card-head" style={{ marginBottom: 0 }}><span className="oc-icon-circle"><Icon name={icon} size={20} /></span><h3>{label}</h3></div>
          </Link>
        ))}
      </div>
    </div>
  );
}
