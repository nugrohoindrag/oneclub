import React, { useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import {
  Card, DataTable, DateFilter, Drawer, ErrorAlert, Icon, Modal, PageHeader, ResourcePage, SearchBox, SelectField, Skeleton, StatTile, StatusPill, TextArea, TextField,
  statusCol, useAuth, useDebounced, useToast, type ResourceConfig,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from './common';
import { CustomerTierBadge, TierBadge, TierFilter } from '../p5/tiers';

const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '').replace(/_/g, '-')} />;
const st = { name: 'status', label: 'Status', type: 'select' as const, default: 'active', options: [{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }] };

// ── Membership (EP-06/07) ───────────────────────────────────────────────────

export function MembersPage() {
  const [open, setOpen] = useState<string | null>(null);
  // PRD P5 tier class: memberships with the tier of the member (crm read model) and the filter by tier.
  const { can } = useAuth();
  const tiers = can('crm.loyalty_account.view');
  const [tier, setTier] = useState('');
  return (
    <>
      <ListPage title="Members" path={tiers ? '/api/v1/crm/loyalty/member-tiers' : '/api/v1/membership/memberships'}
        statuses={['active', 'pending', 'expired', 'inactive'].map((v) => ({ value: v, label: v }))}
        extraQuery={tier ? { 'filter[tierId]': tier } : undefined} filters={tiers ? <TierFilter value={tier} onChange={setTier} /> : undefined}
        onRowClick={(r) => setOpen(String(r.memberId))}
        columns={[{ key: 'memberNo', header: 'Member No.' }, { key: 'memberName', header: 'Member' }, { key: 'typeName', header: 'Membership Type' },
          ...(tiers ? [{ key: 'tierName', header: 'Tier', render: (r: R) => <TierBadge t={r} compact /> }] : []),
          { key: 'role', header: 'Role' }, { key: 'startsOn', header: 'Starts' }, { key: 'endsOn', header: 'Ends' }, { key: 'status', header: 'Status', render: pill('status') }]} />
      {open && <MemberDrawer id={open} onClose={() => setOpen(null)} />}
    </>
  );
}

function MemberDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const p = useGet<R & { memberships?: R[]; cards?: R[] }>(`/api/v1/membership/members/${id}/profile`);
  const x = p.data;
  return (
    <Drawer open onClose={onClose} title={x ? String((x.member as R)?.name ?? x.name ?? 'Member') : 'Member'}>
      {p.isLoading && <Skeleton />}
      <ErrorAlert error={p.error} />
      {x && (
        <div className="oc-stack">
          <pre style={{ display: 'none' }} />
          <div className="oc-row-wrap">
            {can('membership.member.update') && <ActionButton label="Send portal activation" path={`/api/v1/membership/members/${id}:invite`} invalidate={['/api/v1/membership']} />}
            {x.customerId || (x.member as R)?.customerId ? <Link className="oc-btn oc-btn-sm oc-btn-text" to={`/crm/customer-360?id=${String(x.customerId ?? (x.member as R)?.customerId)}`}>Customer 360</Link> : null}
            <CustomerTierBadge customerId={String(x.customerId ?? (x.member as R)?.customerId ?? '') || null} />
          </div>
          {Object.entries(x).filter(([, v]) => Array.isArray(v)).map(([k, v]) => (
            <Card key={k} title={k.replace(/([A-Z])/g, ' $1').replace(/^./, (c) => c.toUpperCase())}>
              <DataTable rows={v as R[]} columns={Object.keys((v as R[])[0] ?? {}).filter((c) => !c.endsWith('Id') && c !== 'id' && c !== 'qrToken').slice(0, 6)
                .map((c) => ({ key: c, header: c.replace(/([A-Z])/g, ' $1'), render: (r: R) => (c === 'status' ? <StatusPill status={String(r[c]).replace(/_/g, '-')} /> : String(r[c] ?? '—')) }))} />
            </Card>
          ))}
        </div>
      )}
    </Drawer>
  );
}

export const programCfg: ResourceConfig = {
  title: 'Membership Programs', singular: 'Membership Program', path: '/api/v1/membership/programs', perm: 'membership.program',
  columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'programKind', header: 'Kind' }, { key: 'operational', header: 'Operational', render: (r) => (r.operational ? 'Yes' : '—') }, statusCol],
  fields: [{ name: 'code', label: 'Code', required: true }, { name: 'name', label: 'Name', required: true },
    { name: 'programKind', label: 'Kind', type: 'select', required: true, options: ['golf', 'sport_club', 'corporate', 'residence'].map((v) => ({ value: v, label: v.replace('_', ' ') })) },
    { name: 'operational', label: 'Operational in P1', type: 'boolean' }, { name: 'description', label: 'Description', type: 'textarea', span: true }, st],
};

export const typeCfg: ResourceConfig = {
  title: 'Membership Types', singular: 'Membership Type', path: '/api/v1/membership/types', perm: 'membership.program',
  columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'category', header: 'Category' }, { key: 'memberRate', header: 'Member Rate', render: (r) => (r.memberRate ? 'Yes' : '—') },
    { key: 'maxGuests', header: 'Guests', align: 'right' }, { key: 'bookingWindowDays', header: 'Booking window', align: 'right' }, statusCol],
  fields: [{ name: 'programId', label: 'Program', type: 'reference', required: true, ref: { path: '/api/v1/membership/programs', label: (r) => String(r.name) } },
    { name: 'code', label: 'Code', required: true }, { name: 'name', label: 'Name', required: true },
    { name: 'category', label: 'Category', type: 'select', required: true, options: [{ value: 'individual', label: 'Individual' }, { value: 'family', label: 'Family' }, { value: 'corporate', label: 'Corporate' }] },
    { name: 'memberRate', label: 'Plays at Member Rate', type: 'boolean', default: true }, { name: 'golfAccess', label: 'Golf access', type: 'boolean', default: true },
    { name: 'maxGuests', label: 'Max guests per round', type: 'number', default: 3 }, { name: 'bookingWindowDays', label: 'Booking window (days)', type: 'number', default: 14 },
    { name: 'maxFamilyMembers', label: 'Max family members', type: 'number', default: 0 }, { name: 'maxNominees', label: 'Max nominees', type: 'number', default: 0 },
    { name: 'eligibility', label: 'Eligibility rules (JSON)', type: 'textarea', span: true, help: '{"minAge":18,"maxChildAge":21,"maxChildren":3,"residentOnly":false}' }, st],
};

export const packageCfg: ResourceConfig = {
  title: 'Membership Packages', singular: 'Membership Package', path: '/api/v1/membership/packages', perm: 'membership.program',
  columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'periodCount', header: 'Period', render: (r) => `${String(r.periodCount)} ${String(r.periodUnit)}` },
    { key: 'joiningFee', header: 'Joining fee', align: 'right', render: (r) => money(r.joiningFee) }, { key: 'periodFee', header: 'Period fee', align: 'right', render: (r) => money(r.periodFee) }, statusCol],
  fields: [{ name: 'typeId', label: 'Membership Type', type: 'reference', required: true, ref: { path: '/api/v1/membership/types', label: (r) => String(r.name) } },
    { name: 'code', label: 'Code', required: true }, { name: 'name', label: 'Name', required: true },
    { name: 'periodUnit', label: 'Period unit', type: 'select', default: 'year', options: [{ value: 'year', label: 'Year' }, { value: 'month', label: 'Month' }] },
    { name: 'periodCount', label: 'Period count', type: 'number', default: 1 }, { name: 'joiningFee', label: 'Joining fee', type: 'decimal' }, { name: 'periodFee', label: 'Period fee', type: 'decimal' }, st],
};

export function ApplicationsPage() {
  const { can } = useAuth();
  const [open, setOpen] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  return (
    <>
      <ListPage title="Applications" help="Membership Application → eligibility → approval → fee → activation." path="/api/v1/membership/applications"
        statuses={['draft', 'pending', 'approved', 'rejected', 'completed', 'cancelled'].map((v) => ({ value: v, label: v }))}
        actions={can('membership.application.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setCreating(true)}>New Application</button> : undefined}
        onRowClick={(r) => setOpen(r.id)}
        columns={[{ key: 'number', header: 'Application' }, { key: 'customerName', header: 'Applicant' }, { key: 'typeName', header: 'Type' }, { key: 'packageName', header: 'Package' },
          { key: 'fee', header: 'Fee', align: 'right', render: (r) => money(r.fee) }, { key: 'channel', header: 'Channel', render: (r) => String(r.channel).replace('_', ' ') },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {creating && <ApplicationForm onClose={() => setCreating(false)} onDone={(id) => { setCreating(false); setOpen(id); }} />}
      {open && <ApplicationDrawer id={open} onClose={() => setOpen(null)} />}
    </>
  );
}

function CustomerPicker({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  const [q, setQ] = useState('');
  const query = useDebounced(q);
  const list = useGet<Page<R>>(`/api/v1/crm/customers${qs({ q: query, limit: 20, 'filter[status]': 'active' })}`);
  return (
    <div className="oc-stack" style={{ gap: 4 }}>
      <TextField label={`${label} — search`} value={q} onChange={setQ} placeholder="Name, phone or e-mail" />
      <SelectField label={label} value={value} onChange={onChange} options={(list.data?.items ?? []).map((c) => ({ value: c.id, label: `${String(c.name)} · ${String(c.phone ?? c.email ?? c.code)}` }))} />
    </div>
  );
}

function ApplicationForm({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const [customerId, setCustomer] = useState('');
  const [typeId, setType] = useState('');
  const [packageId, setPackage] = useState('');
  const [deps, setDeps] = useState<{ customerId: string; relationship: string }[]>([]);
  const types = useGet<Page<R>>('/api/v1/membership/types?filter[status]=active&limit=100');
  const pkgs = useGet<Page<R>>('/api/v1/membership/packages?filter[status]=active&limit=200');
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/membership/applications', ['/api/v1/membership']);
  return (
    <Modal open wide onClose={onClose} title="New Membership Application" actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!customerId || !typeId || !packageId || send.isPending}
        onClick={() => send.mutate({ customerId, typeId, packageId, dependents: deps.filter((d) => d.customerId) }, { onSuccess: (a) => onDone(a.id) })}>Create</button></>}>
      <div className="oc-form">
        <CustomerPicker label="Applicant" value={customerId} onChange={setCustomer} />
        <SelectField label="Membership Type" value={typeId} onChange={(v) => { setType(v); setPackage(''); }} options={(types.data?.items ?? []).map((t) => ({ value: t.id, label: String(t.name) }))} />
        <SelectField label="Package" value={packageId} onChange={setPackage}
          options={(pkgs.data?.items ?? []).filter((p) => p.typeId === typeId).map((p) => ({ value: p.id, label: `${String(p.name)} · ${money(p.periodFee)}` }))} />
      </div>
      <h3>Family members / nominees</h3>
      {deps.map((d, i) => (
        <div className="oc-row-wrap" key={i}>
          <CustomerPicker label="Customer" value={d.customerId} onChange={(v) => setDeps((x) => x.map((y, j) => (j === i ? { ...y, customerId: v } : y)))} />
          <SelectField label="Relationship" value={d.relationship} onChange={(v) => setDeps((x) => x.map((y, j) => (j === i ? { ...y, relationship: v } : y)))}
            options={['spouse', 'child', 'parent', 'sibling', 'other'].map((v) => ({ value: v, label: v }))} />
        </div>
      ))}
      <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setDeps((x) => [...x, { customerId: '', relationship: 'child' }])}>Add family member</button>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function ApplicationDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const a = useGet<R>(`/api/v1/membership/applications/${id}`);
  const x = a.data;
  const inv = ['/api/v1/membership', '/api/v1/billing'];
  const elig = x?.eligibility as R | undefined;
  return (
    <Drawer open onClose={onClose} title={x ? `Application ${String(x.number)}` : 'Application'}>
      {a.isLoading && <Skeleton />}
      <ErrorAlert error={a.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[['Status', <StatusPill key="s" status={String(x.status)} />], ['Applicant', String(x.customerName)], ['Type', String(x.typeName)],
            ['Package', String(x.packageName)], ['Fee', money(x.fee)], ['Submitted', x.submittedAt ? formatDateTime(String(x.submittedAt)) : '—'],
            ['Decision', String(x.decisionReason ?? '—')]]} />
          <div className="oc-row-wrap">
            {x.status === 'draft' && <ActionButton label="Check eligibility" path={`/api/v1/membership/applications/${id}:check-eligibility`} invalidate={inv} />}
            {x.status === 'draft' && can('membership.application.submit') && <ActionButton label="Submit for approval" path={`/api/v1/membership/applications/${id}:submit`} invalidate={inv} kind="primary" />}
            {x.approvalRequestId ? <Link className="oc-btn oc-btn-sm oc-btn-text" to={`/approvals/${String(x.approvalRequestId)}`}>Approval</Link> : null}
            {x.feeFolioId && can('billing.folio.view') ? <Link className="oc-btn oc-btn-sm oc-btn-text" to={`/billing/folios?id=${String(x.feeFolioId)}`}>Fee folio</Link> : null}
            {x.status === 'approved' && can('membership.application.activate') && <ActionButton label="Activate Membership" path={`/api/v1/membership/applications/${id}:activate`} invalidate={inv} kind="primary"
              reason="optional" confirm="Activation needs the fee paid; a reason waives the payment (permission required)." />}
            {['draft', 'pending', 'approved'].includes(String(x.status)) && <ActionButton label="Cancel" path={`/api/v1/membership/applications/${id}:cancel`} invalidate={inv} reason="required" danger />}
          </div>
          {elig && (
            <Card title={elig.eligible ? 'Eligible' : 'Not eligible'} icon={elig.eligible ? 'check_circle' : 'error'}>
              <DataTable rows={(elig.checks as R[]) ?? []} rowKey={(c) => `${String(c.rule)}-${String(c.subject)}`}
                columns={[{ key: 'rule', header: 'Rule', render: (c) => String(c.rule).replace(/_/g, ' ') }, { key: 'subject', header: 'Person' }, { key: 'message', header: 'Result' },
                  { key: 'passed', header: '', render: (c) => <Icon name={c.passed ? 'check' : 'close'} size={18} /> }]} />
            </Card>
          )}
        </div>
      )}
    </Drawer>
  );
}

export function CardsPage() {
  const { can } = useAuth();
  const [issuing, setIssuing] = useState(false);
  const [memberId, setMember] = useState('');
  const [cardType, setCardType] = useState('physical');
  const members = useGet<Page<R>>('/api/v1/membership/members?limit=500&filter[status]=active');
  const send = useSend<Record<string, unknown>>('POST', '/api/v1/membership/cards', ['/api/v1/membership/cards']);
  return (
    <>
      <ListPage title="Membership Cards" path="/api/v1/membership/cards" statuses={[{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }]}
        actions={can('membership.card.issue') ? <button className="oc-btn oc-btn-primary" onClick={() => setIssuing(true)}>Issue Card</button> : undefined}
        columns={[{ key: 'cardNumber', header: 'Card No.' }, { key: 'legacyNumber', header: 'Rhapsody card' }, { key: 'memberNo', header: 'Member No.' }, { key: 'memberName', header: 'Member' },
          { key: 'cardType', header: 'Type' }, { key: 'validUntil', header: 'Valid until' }, { key: 'status', header: 'Status', render: pill('status') }]}
        rowActions={(c) => can('membership.card.issue') && c.status === 'active' && <ActionButton label="Deactivate" path={`/api/v1/membership/cards/${c.id}:deactivate`} invalidate={['/api/v1/membership/cards']} reason="required" danger />} />
      <Modal open={issuing} onClose={() => setIssuing(false)} title="Issue Card" actions={<><button className="oc-btn oc-btn-neutral" onClick={() => setIssuing(false)}>Cancel</button>
        <button className="oc-btn oc-btn-primary" disabled={!memberId || send.isPending} onClick={() => send.mutate({ memberId, cardType }, { onSuccess: () => setIssuing(false) })}>Issue</button></>}>
        <div className="oc-form">
          <SelectField label="Member" value={memberId} onChange={setMember} options={(members.data?.items ?? []).map((m) => ({ value: m.id, label: `${String(m.name)} (${String(m.code)})` }))} />
          <SelectField label="Card type" value={cardType} onChange={setCardType} options={[{ value: 'physical', label: 'Physical' }, { value: 'digital', label: 'Digital' }]} />
        </div>
        <ErrorAlert error={send.error} />
      </Modal>
    </>
  );
}

export function RenewalsPage() {
  const { can } = useAuth();
  return (
    <ListPage title="Renewals" help="Active principal memberships; renewing creates the renewal fee folio." path="/api/v1/membership/memberships" extraQuery={{ 'filter[status]': 'active' }} search
      columns={[{ key: 'memberNo', header: 'Member No.' }, { key: 'memberName', header: 'Member' }, { key: 'typeName', header: 'Type' }, { key: 'endsOn', header: 'Ends' },
        { key: 'status', header: 'Status', render: pill('status') }]}
      rowActions={(m) => can('membership.membership.renew') && m.role === 'principal' && <ActionButton label="Renew" path={`/api/v1/membership/memberships/${m.id}:renew`} body={{}} invalidate={['/api/v1/membership']} />} />
  );
}

export function MembershipHistoryPage() {
  return (
    <ListPage title="Membership History" path="/api/v1/membership/history" search={false}
      columns={[{ key: 'occurredAt', header: 'When', render: (h) => formatDateTime(String(h.occurredAt)) }, { key: 'memberNo', header: 'Member No.' }, { key: 'memberName', header: 'Member' },
        { key: 'event', header: 'Event', render: (h) => String(h.event).replace(/_/g, ' ') }, { key: 'fromStatus', header: 'From' }, { key: 'toStatus', header: 'To' }]} />
  );
}

// ── CRM (EP-01) ─────────────────────────────────────────────────────────────

const customerCfg: ResourceConfig = {
  title: 'Customers', singular: 'Customer', path: '/api/v1/crm/customers', perm: 'crm.customer',
  columns: [{ key: 'code', header: 'Customer Code' }, { key: 'name', header: 'Name', render: (r) => <Link to={`/crm/customer-360?id=${r.id}`}>{String(r.name)}</Link> },
    { key: 'phone', header: 'Phone' }, { key: 'email', header: 'E-mail' }, { key: 'idNumber', header: 'ID Number' }, statusCol],
  fields: [{ name: 'code', label: 'Customer Code', required: true }, { name: 'name', label: 'Name', required: true },
    { name: 'customerType', label: 'Customer Type', type: 'select', default: 'individual', options: [{ value: 'individual', label: 'Individual' }, { value: 'corporate', label: 'Corporate' }] },
    { name: 'gender', label: 'Gender', type: 'select', options: [{ value: 'male', label: 'Male' }, { value: 'female', label: 'Female' }] },
    { name: 'birthDate', label: 'Date of Birth', type: 'date' }, { name: 'phone', label: 'Phone' }, { name: 'email', label: 'E-mail', type: 'email' },
    { name: 'idNumber', label: 'ID Number (NIK/Passport)' }, { name: 'address', label: 'Address', type: 'textarea', span: true }, { name: 'city', label: 'City' },
    { name: 'resident', label: 'Modernland Resident', type: 'boolean' }, { name: 'marketingOptIn', label: 'Marketing opt-in', type: 'boolean' },
    { name: 'duplicateAcknowledged', label: 'Duplicate warning acknowledged', type: 'boolean', help: 'Tick to save when the phone or e-mail already exists' },
    { name: 'notes', label: 'Notes', type: 'textarea', span: true }, st],
};

export function CustomersPage() {
  return <ResourcePage cfg={customerCfg} />;
}

export function Customer360Page() {
  const [params, setParams] = useSearchParams();
  const id = params.get('id');
  const { can } = useAuth();
  const [q, setQ] = useState('');
  const query = useDebounced(q);
  const search = useGet<Page<R>>(!id && query ? `/api/v1/crm/customers${qs({ q: query, limit: 20 })}` : null);
  const ov = useGet<R & { profile: R; memberships: R[]; accounts: R[]; relationships: R[]; preferences: R[]; stats: R; recentHistory: R[] }>(id ? `/api/v1/crm/customers/${id}/overview` : null);
  const x = ov.data;
  const toast = useToast();
  const exp = useSend<Record<string, unknown>, R>('POST', `/api/v1/crm/customers/${id}:export-personal-data`, ['/api/v1/crm']);
  if (!id) {
    return (
      <div className="oc-stack">
        <PageHeader title="Customer 360" help="Profile, membership, bookings, balance and handicap in one page." />
        <SearchBox value={q} onChange={setQ} placeholder="Search customer by name, phone or e-mail" />
        <DataTable rows={search.data?.items} loading={search.isLoading && !!query} onRowClick={(r) => setParams({ id: r.id })}
          columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'phone', header: 'Phone' }, { key: 'email', header: 'E-mail' }, statusCol]} />
      </div>
    );
  }
  return (
    <div className="oc-stack">
      <PageHeader title={x ? String(x.profile.name) : 'Customer 360'} help={x ? `${String(x.profile.code)} · ${String(x.profile.phone ?? '')}` : undefined}
        actions={<>
          <CustomerTierBadge customerId={id} />
          <button className="oc-btn oc-btn-neutral" onClick={() => setParams({})}>Search</button>
          {can('crm.customer.view') && <Link className="oc-btn oc-btn-outline" to={`/crm/customers/${id}`}>View all business lines</Link>}
          {can('crm.customer.export_personal_data') && <button className="oc-btn oc-btn-neutral" disabled={exp.isPending}
            onClick={() => exp.mutate({}, { onSuccess: (r) => { toast('Personal data exported'); window.open(String((r.file as R).url), '_blank'); } })}>Export personal data</button>}
          {can('crm.customer.erase') && <ActionButton label="Erase personal data" path={`/api/v1/crm/customers/${id}:erase`} invalidate={['/api/v1/crm']} reason="required" danger
            confirm="Identifiers are anonymised; financial records keep their amounts. This cannot be undone." />}
        </>} />
      {ov.isLoading && <Skeleton rows={8} />}
      <ErrorAlert error={ov.error ?? exp.error} />
      {x && (
        <>
          <div className="oc-grid">
            <Card title="Rounds" icon="golf_course"><div className="oc-metric">{formatNumber(Number(x.stats.rounds ?? 0))}</div></Card>
            <Card title="Last visit" icon="event"><div className="oc-metric" style={{ fontSize: 22 }}>{x.stats.lastVisit ? formatDate(String(x.stats.lastVisit)) : '—'}</div></Card>
            <Card title="Total payments" icon="payments"><div className="oc-metric" style={{ fontSize: 22 }}>{money(x.stats.totalPayments)}</div></Card>
            <Card title="Handicap Index" icon="sports_golf"><div className="oc-metric">{x.handicapIndex ? String(x.handicapIndex) : '—'}</div></Card>
          </div>
          <div className="oc-grid-2">
            <Card title="Profile" icon="person">
              <KV items={[['Gender', String(x.profile.gender ?? '—')], ['Date of Birth', x.profile.birthDate ? formatDate(String(x.profile.birthDate)) : '—'],
                ['E-mail', String(x.profile.email ?? '—')], ['ID Number', String(x.profile.idNumber ?? '—')], ['City', String(x.profile.city ?? '—')],
                ['Consent', x.profile.consentAt ? formatDateTime(String(x.profile.consentAt)) : '—'], ['Status', <StatusPill key="s" status={String(x.profile.status)} />]]} />
            </Card>
            <Card title="Membership" icon="card_membership">
              <DataTable rows={x.memberships} rowKey={(m) => String(m.membershipId)} columns={[{ key: 'memberNo', header: 'Member No.' }, { key: 'typeName', header: 'Type' }, { key: 'role', header: 'Role' },
                { key: 'endsOn', header: 'Ends', render: (m) => (m.endsOn ? formatDate(String(m.endsOn)) : '—') }, { key: 'status', header: 'Status', render: pill('status') }]} />
            </Card>
            <Card title="Accounts" icon="account_balance_wallet">
              <DataTable rows={x.accounts} rowKey={(a) => String(a.accountId)} columns={[{ key: 'number', header: 'Account' }, { key: 'accountType', header: 'Type' },
                { key: 'balance', header: 'Balance', align: 'right', render: (a) => money(a.balance) }, { key: 'creditLimit', header: 'Credit limit', align: 'right', render: (a) => money(a.creditLimit) }]} />
            </Card>
            <Card title="Family & preferences" icon="family_restroom">
              <DataTable rows={x.relationships} rowKey={(r) => String(r.customerId)} columns={[{ key: 'name', header: 'Name', render: (r) => <Link to={`/crm/customer-360?id=${String(r.customerId)}`}>{String(r.name)}</Link> },
                { key: 'relationship', header: 'Relationship' }]} />
              <DataTable rows={x.preferences} rowKey={(p) => `${String(p.category)}-${String(p.key)}`} columns={[{ key: 'category', header: 'Category' }, { key: 'key', header: 'Preference' }, { key: 'value', header: 'Value' }]} />
            </Card>
          </div>
          <Card title="Customer History" icon="history">
            <DataTable rows={x.recentHistory} rowKey={(h) => `${String(h.kind)}-${String(h.reference)}-${String(h.occurredAt)}`}
              columns={[{ key: 'occurredAt', header: 'When', render: (h) => formatDateTime(String(h.occurredAt)) }, { key: 'kind', header: 'Kind', render: (h) => String(h.kind).replace('_', ' ') },
                { key: 'reference', header: 'Reference' }, { key: 'description', header: 'Description' }, { key: 'amount', header: 'Amount', align: 'right', render: (h) => money(h.amount) },
                { key: 'status', header: 'Status', render: pill('status') }]} />
          </Card>
        </>
      )}
    </div>
  );
}

const corporateCfg: ResourceConfig = {
  title: 'Corporate Accounts', singular: 'Corporate Account', path: '/api/v1/crm/corporate-accounts', perm: 'crm.corporate_account',
  columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'npwp', header: 'NPWP' }, { key: 'contactName', header: 'Contact' }, statusCol],
  fields: [{ name: 'code', label: 'Code', required: true }, { name: 'name', label: 'Name', required: true }, { name: 'npwp', label: 'NPWP' }, { name: 'contactName', label: 'Contact' },
    { name: 'phone', label: 'Phone' }, { name: 'email', label: 'E-mail', type: 'email' }, { name: 'address', label: 'Address', type: 'textarea', span: true }, st],
};

const nomineeCfg: ResourceConfig = {
  title: 'Corporate Nominees', singular: 'Corporate Nominee', path: '/api/v1/crm/corporate-nominees', perm: 'crm.corporate_account',
  columns: [{ key: 'title', header: 'Title' }, { key: 'startsOn', header: 'From' }, { key: 'endsOn', header: 'To' }, statusCol],
  fields: [{ name: 'corporateAccountId', label: 'Corporate Account', type: 'reference', required: true, ref: { path: '/api/v1/crm/corporate-accounts', label: (r) => String(r.name) } },
    { name: 'customerId', label: 'Customer', type: 'reference', required: true, ref: { path: '/api/v1/crm/customers', label: (r) => `${String(r.name)} (${String(r.code)})` } },
    { name: 'title', label: 'Title' }, { name: 'startsOn', label: 'From', type: 'date' }, { name: 'endsOn', label: 'To', type: 'date' }, st],
};

export function CorporateAccountsPage() {
  const [tab, setTab] = useState('accounts');
  return (
    <div className="oc-stack">
      <Tabs tabs={[{ value: 'accounts', label: 'Corporate Accounts' }, { value: 'nominees', label: 'Corporate Nominees' }]} value={tab} onChange={setTab} />
      {tab === 'accounts' ? <ResourcePage cfg={corporateCfg} /> : <ResourcePage cfg={nomineeCfg} />}
    </div>
  );
}

// ── Commercial pricing (EP-08) ──────────────────────────────────────────────

const ratePlanCfg: ResourceConfig = {
  title: 'Rate Plans', singular: 'Rate Plan', path: '/api/v1/commercial/rate-plans', perm: 'commercial.pricing',
  columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'pricingMode', header: 'Pricing mode' }, { key: 'effectiveFrom', header: 'Effective from' },
    { key: 'effectiveTo', header: 'Effective to' }, statusCol],
  fields: [{ name: 'code', label: 'Code', required: true }, { name: 'name', label: 'Name', required: true },
    { name: 'pricingMode', label: 'Pricing mode', type: 'select', required: true, options: [{ value: 'nett', label: 'Nett (tax included)' }, { value: 'plus_plus', label: 'Plus-plus (++)' }] },
    { name: 'effectiveFrom', label: 'Effective from', type: 'date', required: true }, { name: 'effectiveTo', label: 'Effective to', type: 'date' },
    { name: 'description', label: 'Description', type: 'textarea', span: true }, st],
};

const SEGMENTS = ['member', 'guest', 'guest_of_member', 'non_member', 'reciprocal', 'senior', 'ladies', 'junior'];

const ruleCfg: ResourceConfig = {
  title: 'Pricing Rules', singular: 'Pricing Rule', path: '/api/v1/commercial/pricing-rules', perm: 'commercial.pricing',
  help: 'Resolution: priority, then most specific, then latest effective date and version. The cheapest eligible segment wins.',
  columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'chargeType', header: 'Charge' }, { key: 'segment', header: 'Segment' },
    { key: 'price', header: 'Price', align: 'right', render: (r) => money(r.price) }, { key: 'priority', header: 'Priority', align: 'right' }, { key: 'effectiveFrom', header: 'Effective' },
    { key: 'version', header: 'v', align: 'right' }, statusCol],
  fields: [{ name: 'ratePlanId', label: 'Rate Plan', type: 'reference', required: true, ref: { path: '/api/v1/commercial/rate-plans', label: (r) => String(r.name) } },
    { name: 'code', label: 'Code', required: true }, { name: 'name', label: 'Name', required: true },
    { name: 'chargeType', label: 'Charge type', type: 'select', default: 'golf_round', options: ['golf_round', 'caddy_fee', 'cart_fee', 'extra_cart'].map((v) => ({ value: v, label: v.replace('_', ' ') })) },
    { name: 'segment', label: 'Segment', type: 'select', options: SEGMENTS.map((v) => ({ value: v, label: v.replace(/_/g, ' ') })) },
    { name: 'dayTypeId', label: 'Day Type', type: 'reference', ref: { path: '/api/v1/commercial/day-types', label: (r) => String(r.name) } },
    { name: 'timeBandId', label: 'Time Band', type: 'reference', ref: { path: '/api/v1/commercial/time-bands', label: (r) => String(r.name) } },
    { name: 'playingRouteId', label: 'Playing Route', type: 'reference', ref: { path: '/api/v1/golf/playing-routes', label: (r) => String(r.name) } },
    { name: 'channel', label: 'Channel', type: 'select', options: ['member_app', 'website', 'back_office', 'walk_in'].map((v) => ({ value: v, label: v.replace('_', ' ') })) },
    { name: 'price', label: 'Price (all-in)', type: 'decimal', required: true }, { name: 'priority', label: 'Priority (lower first)', type: 'number', default: 100 },
    { name: 'effectiveFrom', label: 'Effective from', type: 'date', required: true }, { name: 'effectiveTo', label: 'Effective to', type: 'date' },
    { name: 'components', label: 'Components (JSON)', type: 'textarea', span: true,
      help: '[{"code":"green_fee","name":"Green Fee","type":"remainder"},{"code":"caddy_fee","name":"Caddy Fee","type":"amount","value":"150000","liability":true}]' }, st],
};

const dayTypeCfg: ResourceConfig = {
  title: 'Day Types', singular: 'Day Type', path: '/api/v1/commercial/day-types', perm: 'commercial.pricing',
  columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'weekdays', header: 'Weekdays (1=Mon)' }, { key: 'includesHolidays', header: 'Public holidays', render: (r) => (r.includesHolidays ? 'Yes' : '—') }, statusCol],
  fields: [{ name: 'code', label: 'Code', required: true }, { name: 'name', label: 'Name', required: true }, { name: 'weekdays', label: 'Weekdays', help: '1,2,3,4,5 = Mon–Fri' },
    { name: 'includesHolidays', label: 'Public holidays use this day type', type: 'boolean' }, { name: 'priority', label: 'Priority', type: 'number', default: 100 }, st],
};

const timeBandCfg: ResourceConfig = {
  title: 'Time Bands', singular: 'Time Band', path: '/api/v1/commercial/time-bands', perm: 'commercial.pricing',
  columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'session', header: 'Session' }, { key: 'startTime', header: 'From' }, { key: 'endTime', header: 'To' }, statusCol],
  fields: [{ name: 'code', label: 'Code', required: true }, { name: 'name', label: 'Name', required: true },
    { name: 'session', label: 'Session', type: 'select', options: ['morning', 'afternoon', 'night', 'other'].map((v) => ({ value: v, label: v })) },
    { name: 'startTime', label: 'From', required: true, placeholder: '05:00' }, { name: 'endTime', label: 'To', required: true, placeholder: '11:00' }, st],
};

export const RatePlansPage = () => <ResourcePage cfg={ratePlanCfg} />;
export const PricingRulesPage = () => (
  <div className="oc-stack">
    <ResourcePage cfg={ruleCfg} />
    <PriceSimulator />
  </div>
);
export function EffectiveDatesPage() {
  const [tab, setTab] = useState('days');
  return (
    <div className="oc-stack">
      <Tabs tabs={[{ value: 'days', label: 'Day Types' }, { value: 'bands', label: 'Time Bands' }, { value: 'calendar', label: 'Calendar' }]} value={tab} onChange={setTab} />
      {tab === 'days' && <ResourcePage cfg={dayTypeCfg} />}
      {tab === 'bands' && <ResourcePage cfg={timeBandCfg} />}
      {tab === 'calendar' && <ResourcePage cfg={{
        title: 'Calendar', singular: 'Calendar Day', path: '/api/v1/platform/calendar-days', perm: 'platform.calendar_day', help: 'Public holidays and special dates.',
        columns: [{ key: 'day', header: 'Date' }, { key: 'name', header: 'Name' }, { key: 'kind', header: 'Kind' }, { key: 'dayTypeCode', header: 'Day Type' }, statusCol],
        fields: [{ name: 'day', label: 'Date', type: 'date', required: true }, { name: 'name', label: 'Name', required: true },
          { name: 'kind', label: 'Kind', type: 'select', default: 'public_holiday', options: [{ value: 'public_holiday', label: 'Public holiday' }, { value: 'special', label: 'Special date' }] },
          { name: 'dayTypeCode', label: 'Day Type override', help: 'Empty = holiday day type' }, st],
      }} />}
    </div>
  );
}

function PriceSimulator() {
  const [v, setV] = useState({ date: today(), time: '07:00', segments: 'guest', chargeType: 'golf_round' });
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/commercial/pricing:resolve');
  const r = send.data;
  return (
    <Card title="Price simulator" icon="calculate">
      <div className="oc-row-wrap">
        <TextField label="Date" type="date" value={v.date} onChange={(x) => setV({ ...v, date: x })} />
        <TextField label="Tee time" type="time" value={v.time} onChange={(x) => setV({ ...v, time: x })} />
        <TextField label="Segments" value={v.segments} onChange={(x) => setV({ ...v, segments: x })} help="Comma separated, e.g. senior,guest" />
        <SelectField label="Charge" value={v.chargeType} onChange={(x) => setV({ ...v, chargeType: x })} options={['golf_round', 'caddy_fee', 'cart_fee', 'extra_cart'].map((c) => ({ value: c, label: c.replace('_', ' ') }))} />
        <button className="oc-btn oc-btn-ink" onClick={() => send.mutate({ ...v, segments: v.segments.split(',').map((s) => s.trim()).filter(Boolean) })}>Resolve</button>
      </div>
      <ErrorAlert error={send.error} />
      {r && (
        <div className="oc-stack">
          <KV items={[['Rule', `${String(r.ruleCode)} v${String(r.ruleVersion)} · ${String(r.ruleName)}`], ['Segment', String(r.segment)], ['Day type / band', `${String(r.dayTypeCode)} / ${String(r.timeBandCode)}`],
            ['Total', money(r.total)], ['Net', money(r.netAmount)], ['Tax', money(r.taxAmount)]]} />
          <DataTable rows={(r.components as R[]) ?? []} rowKey={(c) => String(c.code)} columns={[{ key: 'name', header: 'Component' },
            { key: 'amount', header: 'Amount', align: 'right', render: (c) => money(c.amount) }, { key: 'liability', header: 'Liability', render: (c) => (c.liability ? 'Held for third party' : '—') }]} />
        </div>
      )}
    </Card>
  );
}

// ── Billing & Payment (EP-12) ───────────────────────────────────────────────

export function FoliosPage() {
  const [params, setParams] = useSearchParams();
  const open = params.get('id');
  const { can } = useAuth();
  const [creating, setCreating] = useState(false);
  return (
    <>
      <ListPage title="Folios" path="/api/v1/billing/folios" statuses={[{ value: 'open', label: 'Open' }, { value: 'closed', label: 'Closed' }]}
        actions={can('billing.folio.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setCreating(true)}>New Folio</button> : undefined}
        onRowClick={(r) => setParams({ id: r.id })}
        columns={[{ key: 'number', header: 'Folio' }, { key: 'holderName', header: 'Holder' }, { key: 'sourceType', header: 'Source', render: (r) => String(r.sourceType).replace(/_/g, ' ') },
          { key: 'sourceRef', header: 'Reference' }, { key: 'charges', header: 'Charges', align: 'right', render: (r) => money(r.charges) },
          { key: 'balance', header: 'Balance', align: 'right', render: (r) => money(r.balance) }, { key: 'status', header: 'Status', render: pill('status') }]} />
      {creating && <NewFolio onClose={() => setCreating(false)} onDone={(id) => { setCreating(false); setParams({ id }); }} />}
      {open && <FolioDrawer id={open} onClose={() => setParams({})} />}
    </>
  );
}

function NewFolio({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const [holderName, setHolder] = useState('');
  const [sourceRef, setRef] = useState('');
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/billing/folios', ['/api/v1/billing']);
  return (
    <Modal open onClose={onClose} title="New Folio" actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!holderName || send.isPending} onClick={() => send.mutate({ holderName, sourceRef: sourceRef || undefined }, { onSuccess: (f) => onDone(f.id) })}>Create</button></>}>
      <div className="oc-form"><TextField label="Holder name" value={holderName} onChange={setHolder} required /><TextField label="Reference" value={sourceRef} onChange={setRef} /></div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

export function FolioDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const f = useGet<R & { summary: R; lines: R[]; payments: R[]; deposits: R[]; refunds: R[] }>(`/api/v1/billing/folios/${id}`);
  const [paying, setPaying] = useState(false);
  const [charging, setCharging] = useState(false);
  const x = f.data;
  const inv = ['/api/v1/billing', '/api/v1/golf'];
  return (
    <Drawer open onClose={onClose} title={x ? `Folio ${String(x.number)}` : 'Folio'}>
      {f.isLoading && <Skeleton />}
      <ErrorAlert error={f.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[['Holder', String(x.holderName)], ['Status', <StatusPill key="s" status={String(x.status)} />], ['Charges', money(x.summary.charges)],
            ['Paid', money(x.summary.payments)], ['Deposits held', money(x.summary.heldDeposits)], ['Balance', <strong key="b">{money(x.summary.balance)}</strong>]]} />
          <div className="oc-row-wrap">
            {x.status === 'open' && can('billing.payment.create') && Number(x.summary.balance) > 0 && <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setPaying(true)}>Take Payment</button>}
            {x.status === 'open' && can('billing.folio.add_charge') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setCharging(true)}>Add charge</button>}
            {x.status === 'open' && can('billing.folio.close') && <ActionButton label="Close folio" path={`/api/v1/billing/folios/${id}:close`} invalidate={inv} />}
            {x.status === 'closed' && can('billing.folio.reopen') && <ActionButton label="Reopen" path={`/api/v1/billing/folios/${id}:reopen`} invalidate={inv} reason="required" />}
          </div>
          <Card title="Charges" icon="receipt">
            <DataTable rows={x.lines} columns={[{ key: 'description', header: 'Description' }, { key: 'quantity', header: 'Qty', align: 'right' },
              { key: 'total', header: 'Total', align: 'right', render: (l) => money(l.total) }, { key: 'voidedAt', header: '', render: (l) => (l.voidedAt ? <StatusPill status="cancelled" label="Voided" /> : null) }]}
              actions={(l) => !l.voidedAt && x.status === 'open' && can('billing.folio.void') && <ActionButton label="Void" path={`/api/v1/billing/folios/${id}/lines/${l.id}:void`} invalidate={inv} reason="required" danger />} />
          </Card>
          <Card title="Payments" icon="payments">
            <DataTable rows={x.payments} columns={[{ key: 'number', header: 'Payment' }, { key: 'methodType', header: 'Method', render: (p) => String(p.methodType).replace(/_/g, ' ') },
              { key: 'amount', header: 'Amount', align: 'right', render: (p) => money(p.amount) }, { key: 'status', header: 'Status', render: pill('status') }]}
              actions={(p) => <PaymentActions p={p} />} />
          </Card>
          {x.deposits.length > 0 && (
            <Card title="Deposits" icon="savings">
              <DataTable rows={x.deposits} columns={[{ key: 'number', header: 'Deposit' }, { key: 'amount', header: 'Amount', align: 'right', render: (d) => money(d.amount) },
                { key: 'appliedAmount', header: 'Applied', align: 'right', render: (d) => money(d.appliedAmount) }, { key: 'status', header: 'Status', render: pill('status') }]}
                actions={(d) => d.status === 'held' && can('billing.payment.create') && <ActionButton label="Apply" path={`/api/v1/billing/deposits/${d.id}:apply`} invalidate={inv} />} />
            </Card>
          )}
          {paying && <PaymentModal folio={x} onClose={() => setPaying(false)} />}
          {charging && <ChargeModal folioId={id} onClose={() => setCharging(false)} />}
        </div>
      )}
    </Drawer>
  );
}

function PaymentActions({ p }: { p: R }) {
  const { can } = useAuth();
  const [refund, setRefund] = useState(false);
  return (
    <div className="oc-row">
      {p.status === 'pending' && can('billing.payment.create') && <ActionButton label="Cancel" path={`/api/v1/billing/payments/${p.id}:cancel`} invalidate={['/api/v1/billing']} reason="required" />}
      {p.status === 'completed' && <ActionButton label="Send receipt" path={`/api/v1/billing/payments/${p.id}:send-receipt`} invalidate={[]} kind="text" />}
      {['completed', 'refunded'].includes(String(p.status)) && can('billing.refund.create') && <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => setRefund(true)}>Refund</button>}
      {refund && <RefundModal payment={p} onClose={() => setRefund(false)} />}
    </div>
  );
}

function PaymentModal({ folio, onClose }: { folio: R & { summary: R }; onClose: () => void }) {
  const [v, setV] = useState({ methodType: 'cash', amount: String(folio.summary.balance), reference: '' });
  const send = useSend<Record<string, unknown>>('POST', '/api/v1/billing/payments', ['/api/v1/billing', '/api/v1/golf']);
  return (
    <Modal open onClose={onClose} title="Take Payment" actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ folioId: folio.id, amount: v.amount, methodType: v.methodType,
        channel: ['qris', 'virtual_account', 'payment_gateway'].includes(v.methodType) ? 'online' : 'venue', reference: v.reference || undefined }, { onSuccess: onClose })}>Pay</button></>}>
      <div className="oc-form">
        <SelectField label="Method" value={v.methodType} onChange={(x) => setV({ ...v, methodType: x })}
          options={['cash', 'card', 'bank_transfer', 'qris', 'virtual_account'].map((m) => ({ value: m, label: m.replace('_', ' ') }))} />
        <TextField label="Amount" value={v.amount} onChange={(x) => setV({ ...v, amount: x })} />
        <TextField label="Reference (EDC / transfer)" value={v.reference} onChange={(x) => setV({ ...v, reference: x })} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function ChargeModal({ folioId, onClose }: { folioId: string; onClose: () => void }) {
  const [v, setV] = useState({ chargeType: 'other', description: '', unitPrice: '', quantity: '1' });
  const send = useSend<Record<string, unknown>>('POST', `/api/v1/billing/folios/${folioId}/lines`, ['/api/v1/billing']);
  return (
    <Modal open onClose={onClose} title="Add charge" actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate(v, { onSuccess: onClose })}>Add</button></>}>
      <div className="oc-form">
        <SelectField label="Type" value={v.chargeType} onChange={(x) => setV({ ...v, chargeType: x })} options={['other', 'locker', 'bag_storage', 'discount'].map((c) => ({ value: c, label: c.replace('_', ' ') }))} />
        <TextField label="Description" value={v.description} onChange={(x) => setV({ ...v, description: x })} />
        <TextField label="Unit price" value={v.unitPrice} onChange={(x) => setV({ ...v, unitPrice: x })} />
        <TextField label="Quantity" value={v.quantity} onChange={(x) => setV({ ...v, quantity: x })} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function RefundModal({ payment, onClose }: { payment: R; onClose: () => void }) {
  const [v, setV] = useState({ amount: String(Number(payment.amount) - Number(payment.refundedAmount ?? 0)), reason: '', destination: 'original_method' });
  const send = useSend<Record<string, unknown>>('POST', '/api/v1/billing/refunds', ['/api/v1/billing']);
  return (
    <Modal open onClose={onClose} title={`Refund ${String(payment.number)}`} actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-danger" disabled={!v.reason || send.isPending} onClick={() => send.mutate({ paymentId: payment.id, ...v }, { onSuccess: onClose })}>Process Refund</button></>}>
      <p className="oc-muted">Refunds above the Refund Policy limit wait for approval.</p>
      <div className="oc-form">
        <TextField label="Amount" value={v.amount} onChange={(x) => setV({ ...v, amount: x })} />
        <SelectField label="Destination" value={v.destination} onChange={(x) => setV({ ...v, destination: x })} options={[{ value: 'original_method', label: 'Original method' }, { value: 'member_account', label: 'Member account' }]} />
        <TextArea label="Reason" value={v.reason} onChange={(x) => setV({ ...v, reason: x })} required span />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

export function PaymentsPage() {
  return (
    <ListPage title="Payments" path="/api/v1/billing/payments" statuses={['completed', 'pending', 'cancelled', 'refunded'].map((v) => ({ value: v, label: v }))}
      columns={[{ key: 'number', header: 'Payment' }, { key: 'folioNumber', header: 'Folio' }, { key: 'methodType', header: 'Method', render: (p) => String(p.methodType).replace(/_/g, ' ') },
        { key: 'channel', header: 'Channel' }, { key: 'amount', header: 'Amount', align: 'right', render: (p) => money(p.amount) },
        { key: 'paidAt', header: 'Paid', render: (p) => (p.paidAt ? formatDateTime(String(p.paidAt)) : '—') }, { key: 'status', header: 'Status', render: pill('status') }]}
      rowActions={(p) => <PaymentActions p={p} />} />
  );
}

export function DepositsPage() {
  return (
    <ListPage title="Deposits" path="/api/v1/billing/deposits" search={false} statuses={['held', 'applied', 'refunded'].map((v) => ({ value: v, label: v }))}
      columns={[{ key: 'number', header: 'Deposit' }, { key: 'amount', header: 'Amount', align: 'right', render: (d) => money(d.amount) },
        { key: 'appliedAmount', header: 'Applied', align: 'right', render: (d) => money(d.appliedAmount) }, { key: 'status', header: 'Status', render: pill('status') }]}
      rowActions={(d) => d.status === 'held' && <ActionButton label="Apply" path={`/api/v1/billing/deposits/${d.id}:apply`} invalidate={['/api/v1/billing']} />} />
  );
}

export function RefundsPage() {
  return (
    <ListPage title="Refunds" path="/api/v1/billing/refunds" search={false} statuses={['pending', 'completed', 'rejected'].map((v) => ({ value: v, label: v }))}
      columns={[{ key: 'number', header: 'Refund' }, { key: 'paymentNumber', header: 'Payment' }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) },
        { key: 'destination', header: 'Destination', render: (r) => String(r.destination).replace('_', ' ') }, { key: 'reason', header: 'Reason' },
        { key: 'createdAt', header: 'Requested', render: (r) => formatDateTime(String(r.createdAt)) }, { key: 'status', header: 'Status', render: pill('status') }]}
      rowActions={(r) => (r.approvalRequestId && r.status === 'pending' ? <Link className="oc-btn oc-btn-sm oc-btn-text" to={`/approvals/${String(r.approvalRequestId)}`}>Approval</Link> : null)} />
  );
}

export function CustomerAccountsPage() {
  const { can } = useAuth();
  const [open, setOpen] = useState<R | null>(null);
  return (
    <>
      <ListPage title="Customer Accounts" help="Member accounts (signing bill) and corporate / customer accounts." path="/api/v1/billing/customer-accounts"
        statuses={[{ value: 'active', label: 'Active' }, { value: 'suspended', label: 'Suspended' }]} onRowClick={setOpen}
        columns={[{ key: 'number', header: 'Account' }, { key: 'holderName', header: 'Holder' }, { key: 'accountType', header: 'Type' },
          { key: 'balance', header: 'Balance', align: 'right', render: (a) => money(a.balance) }, { key: 'creditLimit', header: 'Credit limit', align: 'right', render: (a) => money(a.creditLimit) },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {open && <AccountDrawer account={open} onClose={() => setOpen(null)} canAdjust={can('billing.customer_account.adjust')} />}
    </>
  );
}

function AccountDrawer({ account, onClose, canAdjust }: { account: R; onClose: () => void; canAdjust: boolean }) {
  const a = useGet<R & { entries: R[] }>(`/api/v1/billing/customer-accounts/${account.id}`);
  const [v, setV] = useState({ entryType: 'adjustment', amount: '', description: '' });
  const send = useSend<Record<string, unknown>>('POST', `/api/v1/billing/customer-accounts/${account.id}/entries`, ['/api/v1/billing']);
  return (
    <Drawer open onClose={onClose} title={`Account ${String(account.number)}`}>
      <div className="oc-stack">
        <KV items={[['Holder', String(account.holderName ?? '—')], ['Balance', money(a.data?.balance ?? account.balance)], ['Credit limit', money(account.creditLimit)]]} />
        {canAdjust && (
          <Card title="Adjustment" icon="tune">
            <div className="oc-row-wrap">
              <SelectField label="Type" value={v.entryType} onChange={(x) => setV({ ...v, entryType: x })} options={[{ value: 'adjustment', label: 'Adjustment' }, { value: 'opening_balance', label: 'Opening balance' }]} />
              <TextField label="Amount (+ owed, − credit)" value={v.amount} onChange={(x) => setV({ ...v, amount: x })} />
              <TextField label="Description" value={v.description} onChange={(x) => setV({ ...v, description: x })} />
              <button className="oc-btn oc-btn-ink" disabled={!v.amount || !v.description || send.isPending} onClick={() => send.mutate(v)}>Post</button>
            </div>
            <ErrorAlert error={send.error} />
          </Card>
        )}
        <DataTable rows={a.data?.entries} loading={a.isLoading} columns={[{ key: 'occurredAt', header: 'When', render: (e) => formatDateTime(String(e.occurredAt)) },
          { key: 'entryType', header: 'Type', render: (e) => String(e.entryType).replace('_', ' ') }, { key: 'description', header: 'Description' },
          { key: 'amount', header: 'Amount', align: 'right', render: (e) => money(e.amount) }]} />
      </div>
    </Drawer>
  );
}

export function MemberChargesPage() {
  const { can } = useAuth();
  const [period, setPeriod] = useState(today().slice(0, 7));
  return (
    <ListPage title="Member Charges" help="Charges to member accounts; statements are generated monthly." path="/api/v1/billing/member-charges" search={false}
      actions={can('billing.statement.generate') ? <div className="oc-row"><TextField label="Statement period" type="month" value={period} onChange={setPeriod} />
        <ActionButton label="Generate statements" path="/api/v1/billing/member-statements:generate" body={{ period }} invalidate={['/api/v1/billing']} kind="ink" /></div> : undefined}
      columns={[{ key: 'occurredAt', header: 'When', render: (e) => formatDateTime(String(e.occurredAt)) }, { key: 'accountNumber', header: 'Account' }, { key: 'holderName', header: 'Member' },
        { key: 'description', header: 'Description' }, { key: 'amount', header: 'Amount', align: 'right', render: (e) => money(e.amount) }]} />
  );
}

export function ReconciliationPage() {
  const { can } = useAuth();
  const [date, setDate] = useState(today());
  const list = useGet<Page<R>>('/api/v1/billing/reconciliations');
  const run = useSend<Record<string, unknown>>('POST', '/api/v1/billing/reconciliations', ['/api/v1/billing']);
  const exp = useSend<Record<string, unknown>, R>('POST', '/api/v1/billing/accounting-exports', ['/api/v1/billing']);
  return (
    <div className="oc-stack">
      <PageHeader title="Payment Reconciliation" help="Gateway payments of the day against the vendor settlement report." />
      {can('billing.reconciliation.manage') && (
        <div className="oc-row-wrap">
          <TextField label="Business date" type="date" value={date} onChange={setDate} />
          <button className="oc-btn oc-btn-ink" disabled={run.isPending} onClick={() => run.mutate({ date, integrationCode: 'mock-payment' })}>Reconcile with gateway</button>
          {can('billing.accounting_export.create') && <button className="oc-btn oc-btn-neutral" disabled={exp.isPending} onClick={() => exp.mutate({ date })}>Accounting export</button>}
        </div>
      )}
      <ErrorAlert error={run.error ?? exp.error} />
      {(list.data?.items ?? []).map((r) => (
        <Card key={r.id} title={`${String(r.businessDate)} · ${String(r.integrationCode)}`} icon="balance">
          <KV items={[['Result', <StatusPill key="s" status={String(r.status)} />], ['OneClub', `${String(r.oneclubCount)} · ${money(r.oneclubTotal)}`],
            ['Settlement', `${String(r.settlementCount)} · ${money(r.settlementTotal)}`], ['Exceptions', String(r.exceptionCount)]]} />
          <DataTable rows={((r.items as R[]) ?? []).filter((i) => i.result !== 'matched')} columns={[{ key: 'externalId', header: 'Transaction' },
            { key: 'result', header: 'Result', render: (i) => String(i.result).replace(/_/g, ' ') }, { key: 'oneclubAmount', header: 'OneClub', align: 'right', render: (i) => money(i.oneclubAmount) },
            { key: 'settlementAmount', header: 'Settlement', align: 'right', render: (i) => money(i.settlementAmount) }, { key: 'resolutionNote', header: 'Resolution' }]}
            actions={(i) => !i.resolvedAt && can('billing.reconciliation.manage') && <ActionButton label="Resolve" path={`/api/v1/billing/reconciliation-items/${i.id}:resolve`} invalidate={['/api/v1/billing']} reason="required" />} />
        </Card>
      ))}
    </div>
  );
}

// ── Management (EP-16) ──────────────────────────────────────────────────────

/** Icon per golf-today widget key (reporting/golf.go). */const GOLF_WIDGET_ICON: Record<string, string> = {  todays_bookings: 'event_available', todays_players: 'groups', current_queue: 'pending_actions', players_on_course: 'golf_course',  pending_check_in: 'how_to_reg', available_caddies: 'person', caddies_on_round: 'sports_golf', golf_carts_ready: 'electric_car',  golf_carts_in_use: 'electric_car', golf_carts_in_maintenance: 'build', avg_check_in_to_tee_off: 'timer',};
export function GolfPerformancePage() {
  const [date, setDate] = useState(today());
  const d = useGet<{ widgets: R[] }>(`/api/v1/reporting/dashboards/golf-today?date=${date}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Golf Performance" help="Live golf operation of the day." actions={<DateFilter value={date} onChange={setDate} />} />
      <ErrorAlert error={d.error} />
      <div className="oc-stat-grid">
        {(d.data?.widgets ?? []).map((w) => (
          <StatTile key={String(w.key)} label={String(w.label)} icon={GOLF_WIDGET_ICON[String(w.key)]}
            value={`${formatNumber(Number(w.value ?? 0))}${w.key === 'avg_check_in_to_tee_off' ? ' min' : ''}`} />
        ))}
      </div>
      <ReportLinks module="golf" />
    </div>
  );
}

export function ReportLinks({ module }: { module: string }) {
  const reports = useGet<Page<R>>('/api/v1/reporting/reports');
  return (
    <Card title="Reports" icon="monitoring">
      <div className="oc-row-wrap">
        {(reports.data?.items ?? []).filter((r) => r.module === module || (module === 'booking' && r.code === 'golf.bookings')).map((r) => (
          <Link key={String(r.code)} className="oc-btn oc-btn-neutral oc-btn-sm" to={`/reports/${String(r.code)}`}>{String(r.name)}</Link>
        ))}
      </div>
    </Card>
  );
}

export function MembershipPerformancePage() {
  const active = useGet<{ rows: R[] }>('/api/v1/reporting/reports/membership.active_members');
  const expiring = useGet<{ rows: R[] }>('/api/v1/reporting/reports/membership.expiring?params[days]=30');
  return (
    <div className="oc-stack">
      <PageHeader title="Membership Performance" />
      <div className="oc-stat-grid">
        <StatTile label="Active Members" icon="card_membership" value={active.data ? formatNumber(active.data.rows.length) : '—'} />
        <StatTile label="Expiring in 30 days" icon="schedule" value={expiring.data ? formatNumber(expiring.data.rows.length) : '—'} />
      </div>
      <ReportLinks module="membership" />
    </div>
  );
}

export function BookingPerformancePage() {
  const [date, setDate] = useState(today());
  const b = useGet<{ rows: R[] }>(`/api/v1/reporting/reports/golf.bookings?params[from]=${date}&params[to]=${date}`);
  const rows = b.data?.rows ?? [];
  const by = (k: string) => rows.reduce<Record<string, number>>((m, r) => ({ ...m, [String(r[k])]: (m[String(r[k])] ?? 0) + 1 }), {});
  return (
    <div className="oc-stack">
      <PageHeader title="Booking Performance" actions={<DateFilter value={date} onChange={setDate} />} />
      <div className="oc-stat-grid">
        <StatTile label="Bookings" icon="event_available" value={formatNumber(rows.length)} />
        {Object.entries(by('channel')).map(([k, n]) => <StatTile key={k} label={k.replace('_', ' ')} value={formatNumber(n)} />)}
      </div>
      <ReportLinks module="booking" />
    </div>
  );
}
