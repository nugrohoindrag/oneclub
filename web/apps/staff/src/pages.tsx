import React, { useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime, formatNumber, formatRelative, useTranslation } from '@oneclub/i18n';
import {
  Card, ComingSoonPage, DataTable, ErrorAlert, Icon, PageHeader, SelectField, Skeleton, StatusPill, TextField, useAuth, useBootstrap, useToast,
} from '@oneclub/shell';

/** Back Office home (dashboard-ui.webp style). */
export function DashboardPage() {
  const { me, can } = useAuth();
  const boot = useBootstrap();
  const inbox = useGet<Page<Schemas['Request']>>('/api/v1/platform/approvals?box=inbox&limit=5');
  const unread = useGet<{ unread: number }>('/api/v1/platform/notifications/unread-count');
  const overview = useGet<Schemas['Dashboard']>(can('reporting.dashboard.view') ? '/api/v1/reporting/dashboards/executive-overview' : null);
  const hour = new Date().getHours();
  const greet = hour < 11 ? 'Good morning' : hour < 15 ? 'Good afternoon' : 'Good evening';
  const live = overview.data?.widgets.filter((w) => w.status === 'available') ?? [];
  return (
    <div className="oc-stack">
      <PageHeader title={`${greet}, ${me?.fullName.split(' ')[0] ?? ''}`} help={`${boot.branding.appName} · ${boot.timezone}`} />
      <div className="oc-grid">
        <div className="oc-card oc-card-ink">
          <div className="oc-card-head"><span className="oc-icon-circle"><Icon name="approval" size={20} /></span><h3>Approvals waiting</h3></div>
          <div className="oc-metric">{inbox.data ? inbox.data.items.length : '—'}</div>
          <Link to="/approvals" className="oc-btn oc-btn-primary" style={{ marginTop: 16 }}>Open Approvals</Link>
        </div>
        <div className="oc-card">
          <div className="oc-card-head"><span className="oc-icon-circle"><Icon name="notifications" size={20} /></span><h3>Unread notifications</h3></div>
          <div className="oc-metric">{unread.data?.unread ?? '—'}</div>
          <Link to="/notifications" className="oc-btn oc-btn-neutral" style={{ marginTop: 16 }}>View all</Link>
        </div>
        {live.map((w) => (
          <div className="oc-card" key={w.key}>
            <div className="oc-card-head"><span className="oc-icon-circle"><Icon name="insights" size={20} /></span><h3>{w.label}</h3></div>
            <div className="oc-metric">{formatNumber(w.value ?? 0)}</div>
          </div>
        ))}
      </div>
      <Card title="Waiting for my decision" icon="pending_actions" actions={<Link to="/approvals" className="oc-btn oc-btn-text oc-btn-sm">All</Link>}>
        <DataTable rows={inbox.data?.items as unknown as Record<string, unknown>[]} loading={inbox.isLoading}
          empty={<div className="oc-empty"><Icon name="task_alt" size={32} /><div>Nothing waiting for you</div></div>}
          columns={[
            { key: 'title', header: 'Document', render: (r) => <Link to={`/approvals/${r.id}`}>{String(r.title)}</Link> },
            { key: 'requesterName', header: 'Requested by' },
            { key: 'createdAt', header: 'Submitted', render: (r) => formatRelative(String(r.createdAt)) },
            { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> },
          ]} />
      </Card>
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
  { path: 'hris', title: 'HRIS', phase: 'P5', features: ['Employees', 'Attendance', 'Payroll'] },
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

export function ExecutiveOverviewPage() {
  const { t } = useTranslation();
  const d = useGet<Schemas['Dashboard']>('/api/v1/reporting/dashboards/executive-overview');
  const [period, setPeriod] = useState('today');
  if (!d.data) return <Skeleton rows={8} />;
  const live = d.data.widgets.filter((w) => w.status === 'available');
  const soon = d.data.widgets.filter((w) => w.status !== 'available');
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>Executive Overview</h1><p>Updated {formatRelative(d.data.generatedAt)}</p></div><span className="oc-spacer" />
        <div className="oc-row">{['today', 'week', 'month'].map((p) => <button key={p} className="oc-chip" aria-pressed={period === p} onClick={() => setPeriod(p)}>{p === 'today' ? 'Today' : p === 'week' ? 'This week' : 'This month'}</button>)}</div></div>
      <div className="oc-grid">
        {live.map((w, i) => (
          <div key={w.key} className={`oc-card${i === 0 ? ' oc-card-ink' : ''}`}>
            <div className="oc-card-head"><span className="oc-icon-circle"><Icon name={['group', 'apartment', 'location_on', 'pending'][i] ?? 'insights'} size={20} /></span><h3>{w.label}</h3></div>
            <div className="oc-metric">{formatNumber(w.value ?? 0)}</div>
          </div>
        ))}
      </div>
      <Card title="Golf KPIs" icon="golf_course">
        <p className="oc-muted oc-small" style={{ marginTop: 0 }}>{t('common.comingSoonHelp', { phase: 'P1' })}</p>
        <div className="oc-grid">
          {soon.map((w) => (
            <div key={w.key} className="oc-card" style={{ background: 'var(--md-sys-color-surface-container-low)' }}>
              <div className="oc-small oc-muted">{w.label}</div>
              <div className="oc-metric" style={{ opacity: 0.35 }}>—</div>
              <span className="oc-nav-soon">{w.phase}</span>
            </div>
          ))}
        </div>
      </Card>
    </div>
  );
}

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
