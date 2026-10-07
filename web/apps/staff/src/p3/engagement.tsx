import React, { useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import {
  Amount, AutoResourcePage, Avatar, BreakdownList, Card, Checkbox, DashCard, DashGrid, DataTable, Drawer, Empty, ErrorAlert, Gauge, Modal, PageHeader, Podium,
  RankBadge, RankBars, RankMove, SegmentBar, SelectField, Skeleton, StatusPill, TextArea, TextField, useAuth, useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, moneyShort, today, type R } from '../p1/common';
import type { AreaRoute, OpsRoute, OpsTile } from './types';

// CRM engagement & loyalty (PRD P3 EP-05–09) — Back Office screens, ops workstations.
// Naming Convention §12: Campaigns (with segments, reminders and the
// suppression list), Loyalty, Top Spender, Feedback (with NPS) and
// Complaints; Corporate 360; ops Front Desk → Redeem Points.

const CRM = ['/api/v1/crm'];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const opts = (vals: string[], labels: Record<string, string> = {}): Option[] => vals.map((v) => ({ value: v, label: labels[v] ?? label(v) }));
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} />;
const opt = (v: string) => (v === '' ? undefined : v);
const num = (v: string) => (v === '' ? undefined : Number(v));
const pts = (v: unknown) => formatNumber(Number(v ?? 0));
const CHANNELS = opts(['email', 'whatsapp', 'in_app'], { whatsapp: 'WhatsApp', in_app: 'In-app' });
const LINES = opts(['golf', 'sportclub', 'stay', 'pos', 'membership', 'banquet', 'other'], { sportclub: 'Sport Club', stay: 'Bungalow / Venue', pos: 'F&B / POS' });
const PRIORITIES = opts(['low', 'medium', 'high', 'urgent']);
const rows = (v: unknown) => ((v as R[] | undefined) ?? []).map((x, i) => ({ ...x, id: String(x.id ?? i) })) as R[];

function Actions({ children }: { children: React.ReactNode }) {
  return <div className="oc-row-wrap">{children}</div>;
}

/** Customer search + select. */
function CustomerPicker({ value, onChange, required }: { value: string; onChange: (v: string) => void; required?: boolean }) {
  const [q, setQ] = useState('');
  const list = useGet<Page<R>>(`/api/v1/crm/customers${qs({ q, limit: 20, 'filter[status]': 'active' })}`);
  return (
    <>
      <TextField label="Find customer" value={q} onChange={setQ} placeholder="Name, phone, e-mail or code" />
      <SelectField label="Customer" value={value} onChange={onChange} required={required} placeholder="Select"
        options={(list.data?.items ?? []).map((c) => ({ value: c.id, label: `${String(c.name)} (${String(c.code)})` }))} />
    </>
  );
}

// ── Campaigns (EP-05/06) ──────────────────────────────────────────────────

const CAMPAIGN_TABS: Option[] = [{ value: 'campaigns', label: 'Campaigns' }, { value: 'segments', label: 'Segments' },
  { value: 'reminders', label: 'Birthday & Renewal Reminders' }, { value: 'suppressions', label: 'Suppression List' }];

export function CampaignsPage() {
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'campaigns';
  return (
    <div className="oc-stack">
      <PageHeader title="Campaigns" help="E-mail, WhatsApp and in-app campaigns to segments — only to customers who opted in on the channel; frequency cap, throttling and approval follow Settings → Club Policies → Campaign Policies." />
      <Tabs tabs={CAMPAIGN_TABS} value={tab} onChange={(v) => setParams({ tab: v })} />
      {tab === 'campaigns' && <CampaignList />}
      {tab === 'segments' && <SegmentList />}
      {tab === 'reminders' && <Reminders />}
      {tab === 'suppressions' && <AutoResourcePage resourceKey="crm.suppression" />}
    </div>
  );
}

function CampaignList() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const [form, setForm] = useState(false);
  const open = params.get('id');
  return (
    <>
      <ListPage title="Campaign List" path="/api/v1/crm/campaigns" statuses={opts(['draft', 'pending', 'scheduled', 'sent', 'cancelled'])}
        actions={can('crm.campaign.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setForm(true)}>New Campaign</button> : undefined}
        onRowClick={(r) => setParams({ tab: 'campaigns', id: r.id })}
        columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'channel', header: 'Channel', render: (r) => label(r.channel) },
          { key: 'recipientCount', header: 'Recipients', align: 'right' }, { key: 'sentCount', header: 'Sent', align: 'right' },
          { key: 'skippedCount', header: 'Skipped', align: 'right' },
          { key: 'scheduledAt', header: 'Scheduled', render: (r) => (r.scheduledAt ? formatDateTime(String(r.scheduledAt)) : '—') },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {form && <CampaignForm onClose={() => setForm(false)} onDone={(id) => { setForm(false); setParams({ tab: 'campaigns', id }); }} />}
      {open && <CampaignDrawer id={open} onClose={() => setParams({ tab: 'campaigns' })} />}
    </>
  );
}

function CampaignForm({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const segs = useGet<Page<R>>('/api/v1/crm/segments?limit=200&filter[status]=active');
  const [f, setF] = useState<Record<string, string>>({ code: '', name: '', segmentId: '', channel: 'email', subject: '', body: 'Halo {{.name}},\n\n',
    promoMode: 'none', promoCode: '', targetUrl: '', voucherTypeRef: '' });
  const set = (k: string) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const send = useSend<R, R>('POST', '/api/v1/crm/campaigns', CRM);
  const fe = send.error?.fieldErrors ?? {};
  const body = { code: f.code, name: f.name, segmentId: f.segmentId, channel: f.channel, templateEvent: 'crm.campaign_message', subject: f.subject,
    body: f.body, promoMode: f.promoMode, promoCode: opt(f.promoCode), targetUrl: opt(f.targetUrl), voucherTypeRef: opt(f.voucherTypeRef) };
  return (
    <Modal open onClose={onClose} title="New Campaign" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!f.code || !f.name || !f.segmentId || send.isPending}
        onClick={() => send.mutate(body as unknown as R, { onSuccess: (c) => onDone(c.id) })}>Save Draft</button>
    </>}>
      <div className="oc-form">
        <TextField label="Code" value={f.code} onChange={set('code')} required error={fe.code} />
        <TextField label="Name" value={f.name} onChange={set('name')} required error={fe.name} />
        <SelectField label="Segment" value={f.segmentId} onChange={set('segmentId')} required placeholder="Select"
          options={(segs.data?.items ?? []).map((s) => ({ value: s.id, label: `${String(s.name)} (${String(s.memberCount ?? 0)})` }))} />
        <SelectField label="Channel" value={f.channel} onChange={set('channel')} options={CHANNELS} help="WhatsApp needs an explicit opt-in" />
        <TextField label="Subject" value={f.subject} onChange={set('subject')} span />
        <TextArea label="Message" value={f.body} onChange={set('body')} span rows={6}
          help="Variables: {{.name}}, {{.promoCode}}, {{.link}} (tracked link). The unsubscribe link is added automatically." />
        <SelectField label="Promo code" value={f.promoMode} onChange={set('promoMode')}
          options={opts(['none', 'shared', 'unique'], { unique: 'Unique per recipient (issued by Commercial)' })} />
        {f.promoMode !== 'none' && <TextField label="Promotion code / prefix" value={f.promoCode} onChange={set('promoCode')} error={fe.promoCode} />}
        <TextField label="Link (tracked)" value={f.targetUrl} onChange={set('targetUrl')} placeholder="https://" error={fe.targetUrl} />
        <TextField label="Voucher per recipient (voucher type code)" value={f.voucherTypeRef} onChange={set('voucherTypeRef')} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function CampaignDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const c = useGet<R>(`/api/v1/crm/campaigns/${id}/detail`);
  const st = useGet<R>(`/api/v1/crm/campaigns/${id}/stats`);
  const draft = c.data?.status === 'draft';
  const pv = useGet<R>(draft ? `/api/v1/crm/campaigns/${id}/preview` : null);
  const rec = useGet<Page<R>>(draft ? null : `/api/v1/crm/campaigns/${id}/recipients?limit=100`);
  const [sendAt, setSendAt] = useState('');
  const x = c.data;
  const s = st.data;
  const skipped = (pv.data?.skipped ?? s?.skipped ?? {}) as Record<string, number>;
  return (
    <Drawer open onClose={onClose} title={x ? `Campaign ${String(x.code)}` : 'Campaign'}>
      {!x ? <Skeleton /> : (
        <div className="oc-stack">
          <div className="oc-row"><StatusPill status={String(x.status)} /><span className="oc-spacer" />{label(x.channel)}</div>
          <KV items={[['Name', String(x.name)], ['Segment', String(x.segmentName)], ['Promo code', x.promoCode ? `${String(x.promoCode)} (${label(x.promoMode)})` : '—'],
            ['Scheduled', x.scheduledAt ? formatDateTime(String(x.scheduledAt)) : '—'], ['Sent at', x.sentAt ? formatDateTime(String(x.sentAt)) : '—'],
            ['Cancel reason', String(x.cancelReason ?? '—')]]} />
          {pv.data && (
            <Card title="Preview" icon="preview">
              <p className="oc-small oc-muted">To {String(pv.data.customer)} · {String(pv.data.eligible)} of {String(pv.data.recipients)} would receive it now</p>
              {pv.data.subject ? <strong>{String(pv.data.subject)}</strong> : null}
              <pre style={{ whiteSpace: 'pre-wrap', margin: 0 }}>{String(pv.data.body)}</pre>
            </Card>
          )}
          {Object.keys(skipped).length > 0 && <KV items={Object.entries(skipped).map(([k, v]) => [`Skipped: ${label(k)}`, String(v)])} />}
          {s && !draft && (
            <Card title="Performance" icon="insights">
              <KV items={[['Recipients', String(s.recipients)], ['Queued', String(s.queued)], ['Sent', String(s.sent)], ['Delivered', String(s.delivered)],
                ['Read', String(s.read)], ['Clicked', String(s.clicked)], ['Converted', String(s.converted)], ['Unsubscribed', String(s.unsubscribed)],
                ['Click rate', `${(Number(s.clickRate) * 100).toFixed(1)}%`], ['Conversion rate', `${(Number(s.conversionRate) * 100).toFixed(1)}%`]]} />
            </Card>
          )}
          {rec.data && <DataTable rows={rec.data.items} columns={[{ key: 'customerName', header: 'Customer' }, { key: 'status', header: 'Status', render: pill('status') },
            { key: 'promoCode', header: 'Code', render: (r) => String(r.promoCode ?? '—') }, { key: 'deliveryStatus', header: 'BSP', render: (r) => String(r.deliveryStatus ?? '—') },
            { key: 'clickedAt', header: 'Clicked', render: (r) => (r.clickedAt ? formatDateTime(String(r.clickedAt)) : '—') },
            { key: 'convertedAt', header: 'Converted', render: (r) => (r.convertedAt ? formatDateTime(String(r.convertedAt)) : '—') }]} />}
          <Actions>
            {draft && can('crm.campaign.send') && <>
              <TextField label="Send at (optional)" type="datetime-local" value={sendAt} onChange={setSendAt} />
              <ActionButton label="Schedule" kind="primary" path={`/api/v1/crm/campaigns/${id}:schedule`} invalidate={CRM}
                body={sendAt ? { sendAt: new Date(sendAt).toISOString() } : {}} />
            </>}
            {['draft', 'pending', 'scheduled'].includes(String(x.status)) && can('crm.campaign.update') &&
              <ActionButton label="Cancel Campaign" danger reason="required" path={`/api/v1/crm/campaigns/${id}:cancel`} invalidate={CRM} />}
          </Actions>
        </div>
      )}
    </Drawer>
  );
}

function SegmentList() {
  const { can } = useAuth();
  const [form, setForm] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  return (
    <>
      <ListPage title="Customer Segmentation" path="/api/v1/crm/segments" statuses={opts(['active', 'inactive'])}
        actions={can('crm.segment.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setForm(true)}>New Segment</button> : undefined}
        onRowClick={(r) => setOpen(r.id)}
        columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'segmentType', header: 'Type', render: (r) => label(r.segmentType) },
          { key: 'memberCount', header: 'Members', align: 'right' },
          { key: 'refreshedAt', header: 'Refreshed', render: (r) => (r.refreshedAt || r.computedAt ? formatDateTime(String(r.refreshedAt ?? r.computedAt)) : '—') },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {form && <SegmentForm onClose={() => setForm(false)} />}
      {open && <SegmentDrawer id={open} onClose={() => setOpen(null)} />}
    </>
  );
}

const csv = (v: string) => v.split(',').map((x) => x.trim()).filter(Boolean);

function SegmentForm({ onClose }: { onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({ code: '', name: '', segmentType: 'dynamic', customerTypes: '', membershipTypes: '', gender: '',
    minAge: '', maxAge: '', resident: '', minVisits: '', minSpend: '', lookbackDays: '90', loyaltyTiers: '', rfmSegments: '', topSpenderRank: '',
    corporate: '', businessLines: '', outletIds: '', tags: '' });
  const [loyal, setLoyal] = useState(false);
  const set = (k: string) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const send = useSend<R, R>('POST', '/api/v1/crm/segments', CRM);
  const bool = (v: string) => (v === '' ? undefined : v === 'yes');
  const rules = {
    customerTypes: csv(f.customerTypes), membershipTypes: csv(f.membershipTypes), gender: opt(f.gender), minAge: num(f.minAge), maxAge: num(f.maxAge),
    resident: bool(f.resident), minVisits: num(f.minVisits), minSpend: opt(f.minSpend), lookbackDays: num(f.lookbackDays), loyaltyMember: loyal || undefined,
    loyaltyTiers: csv(f.loyaltyTiers), rfmSegments: csv(f.rfmSegments), topSpenderRank: num(f.topSpenderRank), corporate: bool(f.corporate),
    businessLines: csv(f.businessLines), outletIds: csv(f.outletIds), tags: csv(f.tags),
  };
  const yesNo = opts(['yes', 'no']);
  return (
    <Modal open onClose={onClose} title="New Segment" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!f.code || !f.name || send.isPending}
        onClick={() => send.mutate({ code: f.code, name: f.name, segmentType: f.segmentType, rules } as unknown as R, { onSuccess: onClose })}>Save</button>
    </>}>
      <div className="oc-form">
        <TextField label="Code" value={f.code} onChange={set('code')} required />
        <TextField label="Name" value={f.name} onChange={set('name')} required />
        <SelectField label="Type" value={f.segmentType} onChange={set('segmentType')}
          options={opts(['dynamic', 'static'], { dynamic: 'Dynamic (refreshed daily)', static: 'Static (snapshot / manual members)' })} />
        <TextField label="Activity window (days)" type="number" value={f.lookbackDays} onChange={set('lookbackDays')} />
        <TextField label="Customer types" value={f.customerTypes} onChange={set('customerTypes')} help="individual, corporate" />
        <TextField label="Member types (codes)" value={f.membershipTypes} onChange={set('membershipTypes')} />
        <SelectField label="Gender" value={f.gender} onChange={set('gender')} options={opts(['male', 'female'])} placeholder="Any" />
        <TextField label="Age from" type="number" value={f.minAge} onChange={set('minAge')} />
        <TextField label="Age to" type="number" value={f.maxAge} onChange={set('maxAge')} />
        <SelectField label="Residence (Modernland)" value={f.resident} onChange={set('resident')} options={yesNo} placeholder="Any" />
        <TextField label="Minimum visits (frequency)" type="number" value={f.minVisits} onChange={set('minVisits')} />
        <TextField label="Minimum spend" value={f.minSpend} onChange={set('minSpend')} inputMode="decimal" />
        <TextField label="Loyalty tiers (codes)" value={f.loyaltyTiers} onChange={set('loyaltyTiers')} />
        <TextField label="RFM segments" value={f.rfmSegments} onChange={set('rfmSegments')} help="champions, loyal, potential, new, at_risk, hibernating, inactive" />
        <TextField label="Top N spenders" type="number" value={f.topSpenderRank} onChange={set('topSpenderRank')} />
        <SelectField label="Corporate" value={f.corporate} onChange={set('corporate')} options={yesNo} placeholder="Any" />
        <TextField label="Business lines" value={f.businessLines} onChange={set('businessLines')} help="golf, pos, sportclub, stay, banquet" />
        <TextField label="Outlets (ids)" value={f.outletIds} onChange={set('outletIds')} />
        <TextField label="Tags" value={f.tags} onChange={set('tags')} span
          help="loyalty_member, complainant, campaign_responder, nps_promoter, nps_detractor, tournament_participant, wedding, deal_<line>" />
        <Checkbox label="Loyalty members only" checked={loyal} onChange={setLoyal} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function SegmentDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const members = useGet<Page<R>>(`/api/v1/crm/segments/${id}/members`);
  const [add, setAdd] = useState('');
  return (
    <Drawer open onClose={onClose} title="Segment members">
      <div className="oc-stack">
        <Actions>
          {can('crm.segment.update') && <ActionButton label="Refresh" kind="primary" path={`/api/v1/crm/segments/${id}:refresh`} invalidate={CRM} />}
          {can('crm.segment.export') && <a className="oc-btn oc-btn-neutral oc-btn-sm" href={`/api/v1/crm/segments/${id}/members.csv`}>Export CSV</a>}
        </Actions>
        {can('crm.segment.update') && (
          <Card title="Add to a static segment" icon="person_add">
            <CustomerPicker value={add} onChange={setAdd} />
            <ActionButton label="Add" path={`/api/v1/crm/segments/${id}/members:add`} invalidate={CRM} body={{ customerIds: [add] }} disabled={!add} />
          </Card>
        )}
        <DataTable rows={members.data?.items?.map((m) => ({ ...m, id: String(m.customerId) }))} loading={members.isLoading}
          columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'email', header: 'E-mail' }, { key: 'phone', header: 'Phone' }]}
          actions={can('crm.segment.update') ? (m) => <ActionButton label="Remove" path={`/api/v1/crm/segments/${id}/members:remove`} invalidate={CRM}
            body={{ customerIds: [m.id] }} /> : undefined} />
      </div>
    </Drawer>
  );
}

function Reminders() {
  const rules = useGet<Page<R>>('/api/v1/crm/reminder-rules?limit=100');
  const log = useGet<Page<R>>('/api/v1/crm/reminder-log?limit=100');
  const [preview, setPreview] = useState<string | null>(null);
  const pv = useGet<R>(preview ? `/api/v1/crm/reminder-rules/${preview}/preview` : null);
  return (
    <div className="oc-stack">
      <AutoResourcePage resourceKey="crm.reminder_rule" />
      <Card title="Who receives a reminder today" icon="cake">
        <SelectField label="Rule" value={preview ?? ''} onChange={(v) => setPreview(v || null)} placeholder="Select"
          options={(rules.data?.items ?? []).map((r) => ({ value: r.id, label: `${String(r.name)} (${label(r.kind)})` }))} />
        {pv.data && <DataTable rows={rows(pv.data.customers).map((c) => ({ ...c, id: String(c.customerId) }))} columns={[{ key: 'name', header: 'Customer' },
          { key: 'occurrence', header: 'Occurrence' }, { key: 'status', header: 'Status', render: pill('status') }]} />}
      </Card>
      <Card title="Reminder Log" icon="history">
        <DataTable rows={log.data?.items} loading={log.isLoading} columns={[{ key: 'createdAt', header: 'Sent', render: (r) => formatDateTime(String(r.createdAt)) },
          { key: 'ruleCode', header: 'Rule' }, { key: 'kind', header: 'Kind', render: (r) => label(r.kind) }, { key: 'customerName', header: 'Customer' },
          { key: 'channel', header: 'Channel', render: (r) => label(r.channel) }, { key: 'status', header: 'Status', render: pill('status') }]} />
      </Card>
    </div>
  );
}

// ── Loyalty (EP-09) ───────────────────────────────────────────────────────

const LOYALTY_TABS: Option[] = [{ value: 'accounts', label: 'Accounts' }, { value: 'redemptions', label: 'Reward Redemptions' },
  { value: 'adjustments', label: 'Adjust Points' }, { value: 'crm.loyalty_tier', label: 'Tiers' }, { value: 'crm.loyalty_earning_rule', label: 'Earning Rules' },
  { value: 'crm.loyalty_reward', label: 'Rewards' }, { value: 'liability', label: 'Liability' }, { value: 'import', label: 'Opening Balances' }];

export function LoyaltyPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'accounts';
  return (
    <div className="oc-stack">
      <PageHeader title="Loyalty" help="Points from settled payments in every outlet, redeemable as a tender or for rewards; tiers, expiry and the points liability follow Settings → Club Policies → Loyalty Policies."
        actions={can('crm.loyalty_tier.update') ? <ActionButton label="Evaluate Tiers" path="/api/v1/crm/loyalty/tiers:evaluate" invalidate={CRM} confirm="Re-evaluate the tier of every account now?" /> : undefined} />
      <Tabs tabs={LOYALTY_TABS} value={tab} onChange={(v) => setParams({ tab: v })} />
      {tab === 'accounts' && <LoyaltyAccounts />}
      {tab === 'redemptions' && <Redemptions />}
      {tab === 'adjustments' && <Adjustments />}
      {tab.startsWith('crm.') && <AutoResourcePage key={tab} resourceKey={tab} />}
      {tab === 'liability' && <Liability />}
      {tab === 'import' && <LoyaltyImport />}
    </div>
  );
}

function LoyaltyAccounts() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const [enrol, setEnrol] = useState(false);
  const open = params.get('id');
  return (
    <>
      <ListPage title="Loyalty Accounts" path="/api/v1/crm/loyalty/accounts" statuses={opts(['active', 'inactive', 'suspended'])}
        actions={can('crm.loyalty_account.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setEnrol(true)}>Enrol Customer</button> : undefined}
        onRowClick={(r) => setParams({ tab: 'accounts', id: r.id })}
        columns={[{ key: 'number', header: 'Account' }, { key: 'customerName', header: 'Customer' }, { key: 'tierName', header: 'Tier', render: (r) => String(r.tierName ?? '—') },
          { key: 'balance', header: 'Points', align: 'right', render: (r) => pts(r.balance) }, { key: 'balanceValue', header: 'Value', align: 'right', render: (r) => money(r.balanceValue) },
          { key: 'expiringPoints', header: 'Expiring soon', align: 'right', render: (r) => pts(r.expiringPoints) }, { key: 'status', header: 'Status', render: pill('status') }]} />
      {enrol && <EnrolModal onClose={() => setEnrol(false)} />}
      {open && <AccountDrawer id={open} onClose={() => setParams({ tab: 'accounts' })} />}
    </>
  );
}

function EnrolModal({ onClose }: { onClose: () => void }) {
  const [cust, setCust] = useState('');
  const send = useSend<R, R>('POST', '/api/v1/crm/loyalty/accounts', CRM);
  return (
    <Modal open onClose={onClose} title="Enrol in Loyalty" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!cust || send.isPending} onClick={() => send.mutate({ customerId: cust } as unknown as R, { onSuccess: onClose })}>Enrol</button>
    </>}>
      <p className="oc-muted">The customer agrees to join the loyalty programme (opt-in).</p>
      <div className="oc-form"><CustomerPicker value={cust} onChange={setCust} required /></div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function AccountDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const a = useGet<R>(`/api/v1/crm/loyalty/accounts/${id}`);
  const tiers = useGet<Page<R>>('/api/v1/crm/loyalty/tiers?limit=50&filter[status]=active');
  const rewards = useGet<Page<R>>('/api/v1/crm/loyalty/rewards?limit=100&filter[status]=active');
  const [points, setPoints] = useState('');
  const [reason, setReason] = useState('');
  const [tier, setTier] = useState('');
  const [lock, setLock] = useState(false);
  const [reward, setReward] = useState('');
  const x = a.data;
  return (
    <Drawer open onClose={onClose} title={x ? `Loyalty ${String(x.number)}` : 'Loyalty account'}>
      {!x ? <Skeleton /> : (
        <div className="oc-stack">
          <div className="oc-row"><StatusPill status={String(x.status)} /><span className="oc-spacer" /><strong>{pts(x.balance)} points</strong></div>
          <KV items={[['Customer', String(x.customerName)], ['Tier', `${String(x.tierName ?? '—')}${x.tierLocked ? ' (locked)' : ''}`],
            ['Value', money(x.balanceValue)], ['Lifetime points', pts(x.lifetimePoints)], ['Expiring soon', `${pts(x.expiringPoints)}${x.nextExpiry ? ` · next ${formatDate(String(x.nextExpiry))}` : ''}`],
            ['Enrolled', `${formatDate(String(x.optedInAt))} via ${label(x.enrolledVia)}`]]} />
          {can('crm.loyalty_account.adjust') && (
            <Card title="Adjust Points" icon="tune">
              <div className="oc-form">
                <TextField label="Points (+/−)" type="number" value={points} onChange={setPoints} />
                <TextField label="Reason" value={reason} onChange={setReason} required />
              </div>
              <ActionButton label="Submit for approval" path={`/api/v1/crm/loyalty/accounts/${id}:adjust`} invalidate={CRM}
                body={{ points: Number(points), reason }} disabled={!points || !reason} onDone={() => { setPoints(''); setReason(''); }} />
            </Card>
          )}
          {can('crm.loyalty_account.redeem') && (
            <Card title="Redeem a Reward" icon="redeem">
              <SelectField label="Reward" value={reward} onChange={setReward} placeholder="Select"
                options={(rewards.data?.items ?? []).map((r) => ({ value: r.id, label: `${String(r.name)} · ${pts(r.pointsCost)} points` }))} />
              <ActionButton label="Redeem" path={`/api/v1/crm/loyalty/accounts/${id}:redeem-reward`} invalidate={CRM} body={{ rewardId: reward }} disabled={!reward} />
            </Card>
          )}
          {can('crm.loyalty_account.update') && (
            <Card title="Tier & Status" icon="workspace_premium">
              <SelectField label="Tier" value={tier} onChange={setTier} placeholder="No tier"
                options={(tiers.data?.items ?? []).map((t) => ({ value: t.id, label: String(t.name) }))} />
              <Checkbox label="Keep this tier at the periodic evaluation" checked={lock} onChange={setLock} />
              <Actions>
                <ActionButton label="Set Tier" reason="required" path={`/api/v1/crm/loyalty/accounts/${id}:set-tier`} invalidate={CRM} body={{ tierId: tier || null, lock }} />
                {x.status !== 'active' && <ActionButton label="Activate" reason="required" path={`/api/v1/crm/loyalty/accounts/${id}:set-status`} invalidate={CRM} body={{ status: 'active' }} />}
                {x.status === 'active' && <ActionButton label="Suspend" danger reason="required" path={`/api/v1/crm/loyalty/accounts/${id}:set-status`} invalidate={CRM} body={{ status: 'suspended' }} />}
                {x.status === 'active' && <ActionButton label="Opt Out" danger reason="required" path={`/api/v1/crm/loyalty/accounts/${id}:set-status`} invalidate={CRM} body={{ status: 'inactive' }} />}
              </Actions>
            </Card>
          )}
          {rows(x.tierNotes).length > 0 && (
            <Card title="Tier Notes" icon="sticky_note_2">
              <DataTable rows={rows(x.tierNotes)} columns={[{ key: 'createdAt', header: 'Date', render: (r) => formatDate(String(r.createdAt)) }, { key: 'note', header: 'Note' },
                { key: 'suggestedTierName', header: 'Suggested tier', render: (r) => String(r.suggestedTierName ?? '—') }, { key: 'createdByName', header: 'By' }]} />
            </Card>
          )}
          <Card title="Points History" icon="history">
            <DataTable rows={rows(x.ledger)} columns={[{ key: 'occurredAt', header: 'Date', render: (r) => formatDateTime(String(r.occurredAt)) },
              { key: 'kind', header: 'Type', render: pill('kind') }, { key: 'points', header: 'Points', align: 'right', render: (r) => pts(r.points) },
              { key: 'balanceAfter', header: 'Balance', align: 'right', render: (r) => pts(r.balanceAfter) }, { key: 'description', header: 'Description' },
              { key: 'expiresOn', header: 'Valid until', render: (r) => (r.expiresOn ? formatDate(String(r.expiresOn)) : '—') }]} />
          </Card>
        </div>
      )}
    </Drawer>
  );
}

function Redemptions() {
  const { can } = useAuth();
  return (
    <ListPage title="Reward Redemptions" path="/api/v1/crm/loyalty/redemptions" search={false} statuses={opts(['pending', 'completed', 'cancelled'])}
      columns={[{ key: 'number', header: 'Number' }, { key: 'customerName', header: 'Customer' }, { key: 'rewardName', header: 'Reward' },
        { key: 'quantity', header: 'Qty', align: 'right' }, { key: 'points', header: 'Points', align: 'right', render: (r) => pts(r.points) },
        { key: 'fulfilmentCode', header: 'Code', render: (r) => String(r.fulfilmentCode ?? '—') }, { key: 'channel', header: 'Channel', render: (r) => label(r.channel) },
        { key: 'status', header: 'Status', render: pill('status') }]}
      rowActions={(r) => (r.status === 'pending' && can('crm.loyalty_redemption.fulfil') ? <Actions>
        <ActionButton label="Handed Over" kind="primary" path={`/api/v1/crm/loyalty/redemptions/${r.id}:complete`} invalidate={CRM} />
        <ActionButton label="Cancel" danger reason="required" path={`/api/v1/crm/loyalty/redemptions/${r.id}:cancel`} invalidate={CRM} />
      </Actions> : null)} />
  );
}

function Adjustments() {
  return (
    <ListPage title="Points Adjustments" help="Manual adjustments wait for approval (Approvals); approved ones are posted to the ledger."
      path="/api/v1/crm/loyalty/adjustments" search={false} statuses={opts(['pending', 'approved', 'rejected', 'cancelled'])}
      columns={[{ key: 'number', header: 'Number' }, { key: 'accountNumber', header: 'Account' }, { key: 'customerName', header: 'Customer' },
        { key: 'points', header: 'Points', align: 'right', render: (r) => pts(r.points) }, { key: 'reason', header: 'Reason' },
        { key: 'createdAt', header: 'Requested', render: (r) => formatDateTime(String(r.createdAt)) }, { key: 'status', header: 'Status', render: pill('status') }]} />
  );
}

function Liability() {
  const l = useGet<R>('/api/v1/crm/loyalty/liability');
  const x = l.data;
  return (
    <Card title="Loyalty Liability" icon="account_balance">
      {!x ? <Skeleton /> : <KV items={[['Outstanding points', pts(x.points)], ['Accounts with points', String(x.accounts)], ['Point value', money(x.redemptionValue)],
        ['Liability', <strong key="l">{money(x.liability)}</strong>], ['As of', formatDateTime(String(x.at))]]} />}
      <p className="oc-small oc-muted">Reconcile with <Link to={`/reports?module=crm&code=crm.loyalty_liability&to=${today()}`}>Loyalty Liability Report</Link>; the liability
        of each business day is in the night audit summary and the accounting export.</p>
    </Card>
  );
}

function LoyaltyImport() {
  const [text, setText] = useState('customerCode,points,expiresOn,tierCode,reference\n');
  const [result, setResult] = useState<R | null>(null);
  const send = useSend<R, R>('POST', '/api/v1/crm/loyalty/opening-balances:import', CRM);
  const run = (mode: string) => send.mutate({ mode, csv: text, filename: 'opening-balances.csv' } as unknown as R, { onSuccess: setResult });
  return (
    <Card title="Import Opening Balances" icon="upload">
      <p className="oc-muted">Point balances carried over from the previous system, one lot per customer and reference (a re-import adds nothing). Preview first.</p>
      <TextArea label="CSV" value={text} onChange={(v) => { setText(v); setResult(null); }} rows={8} />
      <Actions>
        <button className="oc-btn oc-btn-neutral" disabled={send.isPending} onClick={() => run('preview')}>Preview</button>
        <button className="oc-btn oc-btn-primary" disabled={send.isPending || !result || result.mode !== 'preview' || rows(result.errors).length > 0} onClick={() => run('commit')}>Import</button>
      </Actions>
      <ErrorAlert error={send.error} />
      {result && <KV items={[['Rows', String(result.totalRows)], ['Imported', String(result.imported)], ['Already imported', String(result.skipped)], ['Points', pts(result.points)]]} />}
      {result && rows(result.errors).length > 0 && <DataTable rows={rows(result.errors)} columns={[{ key: 'row', header: 'Row' }, { key: 'field', header: 'Field' },
        { key: 'message', header: 'Problem' }]} />}
    </Card>
  );
}

// ── Top Spender (EP-08) — internal only ───────────────────────────────────

export function TopSpenderPage() {
  const { can } = useAuth();
  const toast = useToast();
  const [view, setView] = useState('ranking');
  const [period, setPeriod] = useState('month');
  const [line, setLine] = useState('');
  const [memberType, setMemberType] = useState('');
  const [segment, setSegment] = useState('');
  const [outlet, setOutlet] = useState('');
  const [note, setNote] = useState<R | null>(null);
  const outlets = useGet<Page<R>>('/api/v1/commercial/outlets?limit=100&filter[status]=active', { retry: false });
  const [vip, setVip] = useState('');
  const segs = useGet<Page<R>>('/api/v1/crm/segments?limit=200');
  const list = useGet<R>(view === 'ranking' ? `/api/v1/crm/top-spenders${qs({ period, businessLine: line, memberType, segmentId: segment, outletId: outlet, limit: 100 })}` : null);
  const board = useGet<R>(view !== 'ranking' ? `/api/v1/crm/leaderboards/${view}?period=${period}` : null);
  const addSeg = useSend<R, R>('POST', '/api/v1/crm/top-spenders:add-to-segment', CRM);
  const items = rows(list.data?.items).map((r) => ({ ...r, id: String(r.customerId) })) as R[];
  const move = mover(items);
  return (
    <div className="oc-stack">
      <PageHeader title="Top Spender" help="Ranking by spend from folios (net charges − refunds − credit notes) — internal to sales and management." />
      <Tabs tabs={[{ value: 'ranking', label: 'Top Spender' }, { value: 'rounds', label: 'Most Rounds' }, { value: 'activity', label: 'Most Active Member' }]} value={view} onChange={setView} />
      <div className="oc-row-wrap">
        <SelectField label="Period" value={period} onChange={setPeriod} options={opts(['month', 'quarter', 'year'])} />
        {view === 'ranking' && <>
          <SelectField label="Business line" value={line} onChange={setLine} options={LINES} placeholder="All" />
          <SelectField label="Customers" value={memberType} onChange={setMemberType} options={opts(['member', 'non_member', 'corporate'])} placeholder="All" />
          <SelectField label="Segment" value={segment} onChange={setSegment} placeholder="All" options={(segs.data?.items ?? []).map((s) => ({ value: s.id, label: String(s.name) }))} />
          <SelectField label="Outlet" value={outlet} onChange={setOutlet} placeholder="All" options={(outlets.data?.items ?? []).map((o) => ({ value: o.id, label: String(o.name) }))} />
        </>}
      </div>
      {view === 'ranking' ? (
        <>
          <ErrorAlert error={list.error ?? addSeg.error} />
          {list.isLoading && <Skeleton rows={8} />}
          {list.data && <SpendOverview items={items} total={Number(list.data.total ?? 0)} from={String(list.data.from)} to={String(list.data.to)}
            vipAction={can('crm.top_spender.manage') && (
              <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
                <TextField label="VIP list (new static segment code)" value={vip} onChange={setVip} />
                <button className="oc-btn oc-btn-ink" disabled={!vip || addSeg.isPending} onClick={() => addSeg.mutate({ segmentCode: vip, segmentName: `VIP ${vip}`,
                  customerIds: items.slice(0, 10).map((i) => i.id) } as unknown as R, { onSuccess: (r) => toast(`${String(r.added)} customers in ${String(r.segmentCode)}`) })}>Top 10 to VIP list</button>
              </div>
            )} />}
          <Card title="Full ranking" icon="format_list_numbered">
            <DataTable rows={items} loading={list.isLoading} columns={[
              { key: 'rank', header: '#', render: (r) => <span className="oc-row"><RankBadge rank={Number(r.rank)} /><RankMove rank={Number(r.rank)} previousRank={move(r)} /></span> },
              { key: 'name', header: 'Customer', render: (r) => (
                <span className="oc-row" style={{ gap: 10 }}><Avatar name={String(r.name)} tone="white" />
                  <span>{String(r.name)}<div className="oc-small oc-muted">{String(r.code)}{r.member ? ' · Member' : ''}{r.corporate ? ' · Corporate' : ''}</div></span></span>
              ) },
              { key: 'spend', header: 'Spend', align: 'right', render: (r) => <strong>{money(r.spend)}</strong> },
              ...SPEND_LINES.map((l) => ({ key: l.key, header: l.label, align: 'right' as const, render: (r: R) => (Number(r[l.key] ?? 0) ? money(r[l.key]) : <span className="oc-muted">—</span>) })),
              { key: 'refunds', header: 'Refunds', align: 'right', render: (r) => (Number(r.refunds ?? 0) ? <span style={{ color: 'var(--dash-red)' }}>{money(r.refunds)}</span> : <span className="oc-muted">—</span>) },
              { key: 'visits', header: 'Visit days', align: 'right' }]}
              actions={can('crm.top_spender.manage') ? (r) => <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setNote(r)}>Tier Note</button> : undefined} />
          </Card>
        </>
      ) : (
        <LeaderboardView view={view} rows={rows(board.data?.items).map((r) => ({ ...r, id: String(r.customerId) })) as R[]} loading={board.isLoading} />
      )}
      {note && <TierNoteModal customer={note} onClose={() => setNote(null)} />}
    </div>
  );
}

/** Business lines of the spend, in the colours of the dashboard kit. */
const SPEND_LINES = [
  { key: 'golf', label: 'Golf', color: 'var(--dash-blue)' }, { key: 'fnb', label: 'F&B', color: 'var(--dash-lime)' },
  { key: 'sport', label: 'Sport', color: 'var(--dash-sky)' }, { key: 'bungalow', label: 'Bungalow', color: 'var(--dash-amber)' },
  { key: 'banquet', label: 'Banquet', color: 'var(--dash-ink)' },
];

/**
 * Rank movement of a row: its previous rank, null (NEW) when the previous
 * month's snapshot exists but lacks it, undefined when there is no snapshot.
 */
const mover = (items: R[]) => {
  const tracked = items.some((r) => r.previousRank != null);
  return (r: R) => (r.previousRank != null ? Number(r.previousRank) : tracked ? null : undefined);
};

const customerSub = (r: R) => [String(r.code ?? ''), r.member ? 'Member' : '', r.corporate ? 'Corporate' : ''].filter(Boolean).join(' · ');

/** Podium, total with the top-10 share, spend per business line and the top-10 bars. */
function SpendOverview({ items, total, from, to, vipAction }: { items: R[]; total: number; from: string; to: string; vipAction?: React.ReactNode }) {
  const move = mover(items);
  const ranked = items.map((r) => ({ key: r.id, rank: Number(r.rank), previousRank: move(r), name: String(r.name), sub: customerSub(r), value: money(r.spend) }));
  const top10 = items.slice(0, 10);
  const top10Spend = top10.reduce((s, r) => s + Number(r.spend ?? 0), 0);
  const sum = total || items.reduce((s, r) => s + Number(r.spend ?? 0), 0);
  const lines = SPEND_LINES.map((l) => ({ ...l, value: items.reduce((s, r) => s + Number(r[l.key] ?? 0), 0) }));
  const linesTotal = lines.reduce((s, l) => s + l.value, 0) || 1;
  return (
    <DashGrid>
      <DashCard icon="emoji_events" tone="dark" title="Top 3" span={8} controls={<span className="oc-dash-tag">{formatDate(from)} – {formatDate(to)}</span>}>
        <Podium items={ranked} empty="No spend in this period." />
      </DashCard>
      <DashCard icon="payments" tone="blue" title="Spend of the ranked" span={4}>
        <Amount text={money(sum)} size="lg" />
        <Gauge title="Top 10 share" ratio={sum > 0 ? top10Spend / sum : null} caption="Top 10 spend" value={moneyShort(top10Spend)}
          sub={sum > 0 ? `${((top10Spend / sum) * 100).toFixed(1)}% of the ranked spend` : undefined} />
        <span className="oc-dash-sub">{formatNumber(items.length)} customers ranked</span>
      </DashCard>
      <DashCard icon="donut_large" title="Spend by business line" span={5}>
        <SegmentBar parts={lines.map((l) => ({ label: l.label, value: l.value, color: l.color }))} format={moneyShort} legend={false} />
        <BreakdownList rows={lines.map((l) => ({ label: l.label, value: money(l.value), share: l.value / linesTotal, color: l.color }))} />
      </DashCard>
      <DashCard icon="leaderboard" title="Top 10" span={7}>
        <RankBars format={(v) => money(v)} legend={SPEND_LINES.map((l) => ({ label: l.label, color: l.color }))}
          rows={top10.map((r) => ({ key: r.id, rank: Number(r.rank), previousRank: move(r), name: String(r.name), value: money(r.spend),
            total: Number(r.spend ?? 0), parts: SPEND_LINES.map((l) => ({ label: l.label, value: Number(r[l.key] ?? 0), color: l.color })) }))} />
        {vipAction}
      </DashCard>
    </DashGrid>
  );
}

/** Most Rounds / Most Active Member: podium, top-10 bars and the full list. */
function LeaderboardView({ view, rows: list, loading }: { view: string; rows: R[]; loading: boolean }) {
  const unit = view === 'rounds' ? 'Rounds' : 'Active days';
  const fmt = (v: unknown) => `${formatNumber(Number(v ?? 0))} ${view === 'rounds' ? 'rounds' : 'days'}`;
  const ranked = list.map((r) => ({ key: r.id, rank: Number(r.rank), name: String(r.name), sub: String(r.code ?? ''), value: fmt(r.value) }));
  if (loading) return <Skeleton rows={8} />;
  return (
    <>
      <DashGrid>
        <DashCard icon={view === 'rounds' ? 'golf_course' : 'event_available'} tone="dark" title="Top 3" span={6}>
          <Podium items={ranked} empty="Nothing ranked in this period." />
        </DashCard>
        <DashCard icon="leaderboard" title="Top 10" span={6}>
          <RankBars format={(v) => fmt(v)} rows={list.slice(0, 10).map((r) => ({ key: r.id, rank: Number(r.rank), name: String(r.name), value: fmt(r.value), total: Number(r.value ?? 0) }))} />
        </DashCard>
      </DashGrid>
      <Card title="Full ranking" icon="format_list_numbered">
        <DataTable rows={list} columns={[{ key: 'rank', header: '#', render: (r) => <RankBadge rank={Number(r.rank)} /> },
          { key: 'name', header: 'Customer', render: (r) => <span className="oc-row" style={{ gap: 10 }}><Avatar name={String(r.name)} tone="white" />{String(r.name)}</span> },
          { key: 'code', header: 'Code' }, { key: 'value', header: unit, align: 'right', render: (r) => <strong>{formatNumber(Number(r.value ?? 0))}</strong> }]} />
      </Card>
    </>
  );
}

function TierNoteModal({ customer, onClose }: { customer: R; onClose: () => void }) {
  const tiers = useGet<Page<R>>('/api/v1/crm/loyalty/tiers?limit=50&filter[status]=active');
  const [text, setText] = useState('');
  const [tier, setTier] = useState('');
  const send = useSend<R, R>('POST', '/api/v1/crm/top-spenders:tier-note', CRM);
  return (
    <Modal open onClose={onClose} title={`Tier note · ${String(customer.name)}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!text || send.isPending}
        onClick={() => send.mutate({ customerId: customer.id, note: text, suggestedTierId: opt(tier) } as unknown as R, { onSuccess: onClose })}>Save</button>
    </>}>
      <div className="oc-form">
        <TextArea label="Note" value={text} onChange={setText} span required />
        <SelectField label="Suggested tier" value={tier} onChange={setTier} placeholder="—" options={(tiers.data?.items ?? []).map((t) => ({ value: t.id, label: String(t.name) }))} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── Feedback & NPS (EP-07) ────────────────────────────────────────────────

export function FeedbackPage() {
  const { can } = useAuth();
  const toast = useToast();
  const [line, setLine] = useState('');
  const nps = useGet<R>(`/api/v1/crm/nps${qs({ businessLine: line })}`);
  const ticket = useSend<R, R>('POST', (b) => `/api/v1/crm/feedback/${String(b.id)}:open-ticket`, CRM);
  const n = nps.data;
  const overall = (n?.overall ?? {}) as R;
  return (
    <div className="oc-stack">
      <PageHeader title="Feedback" help="Survey answers and NPS per business line; a low score opens a follow-up and, per the Complaint Policies, a complaint ticket." />
      <Card title="NPS" icon="sentiment_satisfied" actions={<SelectField label="Business line" value={line} onChange={setLine} options={LINES} placeholder="All" />}>
        {!n ? <Skeleton /> : (
          <div className="oc-stack">
            <KV items={[['NPS', <strong key="n">{String(overall.nps ?? '0')}</strong>], ['Responses', String(overall.responses ?? 0)],
              ['Promoters / Passives / Detractors', `${String(overall.promoters ?? 0)} / ${String(overall.passives ?? 0)} / ${String(overall.detractors ?? 0)}`],
              ['Period', `${formatDate(String(n.from))} – ${formatDate(String(n.to))}`]]} />
            <DataTable rows={rows(n.byLine).map((r) => ({ ...r, id: String(r.label) })) as R[]} columns={[{ key: 'label', header: 'Business line', render: (r) => label(r.label) },
              { key: 'responses', header: 'Responses', align: 'right' }, { key: 'nps', header: 'NPS', align: 'right' }]} />
            <DataTable rows={rows(n.trend).map((r) => ({ ...r, id: String(r.label) })) as R[]} columns={[{ key: 'label', header: 'Month' },
              { key: 'responses', header: 'Responses', align: 'right' }, { key: 'nps', header: 'NPS', align: 'right' }]} />
            <DataTable rows={rows(n.comments)} columns={[{ key: 'createdAt', header: 'Date', render: (r) => formatDate(String(r.createdAt)) },
              { key: 'score', header: 'Score', align: 'right' }, { key: 'businessLine', header: 'Line', render: (r) => label(r.businessLine) },
              { key: 'customerName', header: 'Customer', render: (r) => String(r.customerName ?? '—') }, { key: 'comment', header: 'Comment' }]} />
          </div>
        )}
      </Card>
      <ErrorAlert error={ticket.error} />
      <ListPage title="Feedback Received" path="/api/v1/crm/feedback" search={false}
        columns={[{ key: 'createdAt', header: 'Date', render: (r) => formatDateTime(String(r.createdAt)) }, { key: 'customerName', header: 'Customer', render: (r) => String(r.customerName ?? '—') },
          { key: 'contextLabel', header: 'Visit', render: (r) => String(r.contextLabel ?? label(r.contextType)) }, { key: 'rating', header: 'Rating', render: (r) => '★'.repeat(Number(r.rating)) },
          { key: 'nps', header: 'NPS', render: (r) => String(r.nps ?? '—') }, { key: 'comment', header: 'Comment', render: (r) => String(r.comment ?? '') },
          { key: 'followUp', header: 'Follow-up', render: pill('followUp') }]}
        rowActions={(r) => (r.lowScore && can('crm.ticket.create') ? <button className="oc-btn oc-btn-sm oc-btn-neutral" disabled={ticket.isPending}
          onClick={() => ticket.mutate({ id: r.id } as R, { onSuccess: (x) => toast(`Ticket ${String((x.ticket as R).number)}`) })}>Open Ticket</button> : null)} />
    </div>
  );
}

// ── Complaints (EP-07) ────────────────────────────────────────────────────

export function ComplaintsPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const [form, setForm] = useState(false);
  const [mine, setMine] = useState(false);
  const [overdue, setOverdue] = useState(false);
  const [priority, setPriority] = useState('');
  const [line, setLine] = useState('');
  const tab = params.get('tab') ?? 'tickets';
  const open = params.get('id');
  return (
    <div className="oc-stack">
      <Tabs tabs={[{ value: 'tickets', label: 'Complaint Tickets' }, { value: 'categories', label: 'Complaint Categories' }]} value={tab} onChange={(v) => setParams({ tab: v })} />
      {tab === 'categories' ? <AutoResourcePage resourceKey="crm.ticket_category" /> : (
        <>
          <ListPage title="Complaints" help="Complaint tickets with SLA in business hours; breaches escalate to the line manager, then the General Manager (Complaint Policies)."
            path="/api/v1/crm/tickets" statuses={opts(['open', 'in_progress', 'escalated', 'resolved', 'closed'])}
            extraQuery={{ mine: mine ? 'true' : '', overdue: overdue ? 'true' : '', 'filter[priority]': priority, 'filter[businessLine]': line }}
            filters={<>
              <SelectField label="Priority" value={priority} onChange={setPriority} options={PRIORITIES} placeholder="All" />
              <SelectField label="Business line" value={line} onChange={setLine} options={LINES} placeholder="All" />
              <Checkbox label="Assigned to me" checked={mine} onChange={setMine} />
              <Checkbox label="SLA overdue" checked={overdue} onChange={setOverdue} />
            </>}
            actions={can('crm.ticket.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setForm(true)}>New Ticket</button> : undefined}
            onRowClick={(r) => setParams({ id: r.id })}
            columns={[{ key: 'number', header: 'Ticket' }, { key: 'subject', header: 'Subject' },
              { key: 'customerName', header: 'Customer', render: (r) => String(r.customerName ?? r.contactName ?? '—') },
              { key: 'businessLine', header: 'Line', render: (r) => label(r.businessLine) }, { key: 'priority', header: 'Priority', render: pill('priority') },
              { key: 'resolutionDueAt', header: 'Due', render: (r) => <>{formatDateTime(String(r.resolutionDueAt))}{r.overdue ? <> <StatusPill status="overdue" /></> : null}</> },
              { key: 'assigneeName', header: 'Assigned', render: (r) => String(r.assigneeName ?? '—') }, { key: 'status', header: 'Status', render: pill('status') }]} />
          {form && <TicketForm onClose={() => setForm(false)} onDone={(id) => { setForm(false); setParams({ id }); }} />}
          {open && <TicketDrawer id={open} onClose={() => setParams({})} />}
        </>
      )}
    </div>
  );
}

function TicketForm({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const cats = useGet<Page<R>>('/api/v1/crm/ticket-categories?limit=100&filter[status]=active');
  const [cust, setCust] = useState('');
  const [f, setF] = useState<Record<string, string>>({ contactName: '', contactPhone: '', contactEmail: '', categoryId: '', businessLine: '', priority: '',
    channel: 'staff', subject: '', description: '' });
  const set = (k: string) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const send = useSend<R, R>('POST', '/api/v1/crm/tickets', CRM);
  const fe = send.error?.fieldErrors ?? {};
  const body = { customerId: opt(cust), contactName: opt(f.contactName), contactPhone: opt(f.contactPhone), contactEmail: opt(f.contactEmail),
    categoryId: opt(f.categoryId), businessLine: opt(f.businessLine), priority: opt(f.priority), channel: f.channel, subject: f.subject, description: f.description };
  return (
    <Modal open onClose={onClose} title="New Complaint Ticket" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!f.subject || !f.description || send.isPending}
        onClick={() => send.mutate(body as unknown as R, { onSuccess: (t) => onDone(t.id) })}>Open Ticket</button>
    </>}>
      <div className="oc-form">
        <CustomerPicker value={cust} onChange={setCust} />
        {!cust && <>
          <TextField label="Contact name" value={f.contactName} onChange={set('contactName')} error={fe.customerId} />
          <TextField label="Contact phone" value={f.contactPhone} onChange={set('contactPhone')} />
          <TextField label="Contact e-mail" type="email" value={f.contactEmail} onChange={set('contactEmail')} />
        </>}
        <SelectField label="Category" value={f.categoryId} onChange={set('categoryId')} placeholder="—" options={(cats.data?.items ?? []).map((c) => ({ value: c.id, label: String(c.name) }))} />
        <SelectField label="Business line" value={f.businessLine} onChange={set('businessLine')} options={LINES} placeholder="From the category" />
        <SelectField label="Priority" value={f.priority} onChange={set('priority')} options={PRIORITIES} placeholder="From the category" />
        <SelectField label="Received via" value={f.channel} onChange={set('channel')} options={opts(['staff', 'phone', 'whatsapp', 'email'], { whatsapp: 'WhatsApp' })} />
        <TextField label="Subject" value={f.subject} onChange={set('subject')} required span error={fe.subject} />
        <TextArea label="Description" value={f.description} onChange={set('description')} required span error={fe.description} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function TicketDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can, me } = useAuth();
  const d = useGet<R>(`/api/v1/crm/tickets/${id}`);
  const depts = useGet<Page<R>>('/api/v1/platform/departments?limit=100');
  const [reply, setReply] = useState('');
  const [internal, setInternal] = useState(false);
  const [resolution, setResolution] = useState('');
  const [dept, setDept] = useState('');
  const [comp, setComp] = useState(false);
  const x = d.data;
  const done = x?.status === 'resolved' || x?.status === 'closed';
  return (
    <Drawer open onClose={onClose} title={x ? `Complaint ${String(x.number)}` : 'Complaint'}>
      {!x ? <Skeleton /> : (
        <div className="oc-stack">
          <div className="oc-row"><StatusPill status={String(x.status)} /><StatusPill status={String(x.priority)} />{x.overdue ? <StatusPill status="overdue" /> : null}
            <span className="oc-spacer" />{label(x.businessLine)}</div>
          <KV items={[['Subject', String(x.subject)], ['Customer', String(x.customerName ?? x.contactName ?? '—')], ['Contact', [x.contactPhone, x.contactEmail].filter(Boolean).join(' · ') || '—'],
            ['Category', String(x.categoryName ?? '—')], ['Received via', label(x.channel)], ['Assigned', `${String(x.departmentName ?? '—')} · ${String(x.assigneeName ?? '—')}`],
            ['First response due', `${formatDateTime(String(x.firstResponseDueAt))}${x.firstRespondedAt ? ` · answered ${formatDateTime(String(x.firstRespondedAt))}` : ''}`],
            ['Resolution due', formatDateTime(String(x.resolutionDueAt))], ['Escalation', x.escalationLevel ? `Level ${String(x.escalationLevel)}` : '—'],
            ['Resolution', String(x.resolution ?? '—')]]} />
          <p style={{ whiteSpace: 'pre-wrap' }}>{String(x.description)}</p>
          {can('crm.ticket.manage') && !done && (
            <Card title="Reply / Note" icon="chat">
              <TextArea label={internal ? 'Internal note' : 'Reply to the customer'} value={reply} onChange={setReply} span />
              <Checkbox label="Internal note (not sent to the customer)" checked={internal} onChange={setInternal} />
              <ActionButton label={internal ? 'Add Note' : 'Send Reply'} kind="primary" path={`/api/v1/crm/tickets/${id}:comment`} invalidate={CRM}
                body={{ body: reply, internal }} disabled={!reply} onDone={() => setReply('')} />
            </Card>
          )}
          {can('crm.ticket.manage') && x.status !== 'closed' && (
            <Card title="Route" icon="alt_route">
              <SelectField label="Department" value={dept} onChange={setDept} placeholder="—" options={(depts.data?.items ?? []).map((t) => ({ value: t.id, label: String(t.name) }))} />
              <Actions>
                <ActionButton label="Assign Department" path={`/api/v1/crm/tickets/${id}:assign`} invalidate={CRM} body={{ departmentId: dept }} disabled={!dept} />
                {me?.id && <ActionButton label="Assign to Me" path={`/api/v1/crm/tickets/${id}:assign`} invalidate={CRM} body={{ assignedTo: me.id }} />}
              </Actions>
            </Card>
          )}
          <Actions>
            {!done && can('crm.ticket.escalate') && <ActionButton label="Escalate" danger reason="required" path={`/api/v1/crm/tickets/${id}:escalate`} invalidate={CRM} />}
            {done && can('crm.ticket.manage') && <ActionButton label="Reopen" reason="required" path={`/api/v1/crm/tickets/${id}:reopen`} invalidate={CRM} />}
            {x.status === 'resolved' && can('crm.ticket.manage') && <ActionButton label="Close" reason="optional" path={`/api/v1/crm/tickets/${id}:close`} invalidate={CRM} />}
            {can('crm.ticket.compensate') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setComp(true)}>Compensation</button>}
          </Actions>
          {!done && can('crm.ticket.manage') && (
            <Card title="Resolve" icon="task_alt">
              <TextArea label="Resolution" value={resolution} onChange={setResolution} span />
              <ActionButton label="Resolve" kind="primary" path={`/api/v1/crm/tickets/${id}:resolve`} invalidate={CRM} body={{ resolution }} disabled={!resolution} />
            </Card>
          )}
          {rows(x.compensations).length > 0 && <DataTable rows={rows(x.compensations)} columns={[{ key: 'number', header: 'Compensation' }, { key: 'type', header: 'Type', render: (r) => label(r.type) },
            { key: 'points', header: 'Points', render: (r) => (r.points ? pts(r.points) : '—') }, { key: 'amount', header: 'Amount', render: (r) => money(r.amount) },
            { key: 'status', header: 'Status', render: pill('status') }]} />}
          <Card title="History" icon="history">
            {rows(x.events).length === 0 ? <Empty title="No history yet" /> : rows(x.events).map((e) => (
              <div key={e.id} className="oc-small" style={{ borderBottom: '1px solid var(--md-sys-color-outline-variant)', padding: '6px 0' }}>
                <strong>{label(e.kind)}</strong> · {formatDateTime(String(e.createdAt))} · {String(e.actorName ?? label(e.actorType))}{e.internal ? ' · internal' : ''}
                {e.body ? <div style={{ whiteSpace: 'pre-wrap' }}>{String(e.body)}</div> : null}
                {e.kind === 'escalated' ? <div className="oc-muted">Level {String((e.details as R).level)} → {label((e.details as R).role)}</div> : null}
              </div>
            ))}
          </Card>
          {comp && <CompensationModal id={id} onClose={() => setComp(false)} />}
        </div>
      )}
    </Drawer>
  );
}

function CompensationModal({ id, onClose }: { id: string; onClose: () => void }) {
  const [type, setType] = useState('points');
  const [points, setPoints] = useState('');
  const [amount, setAmount] = useState('');
  const [desc, setDesc] = useState('');
  const send = useSend<R, R>('POST', `/api/v1/crm/tickets/${id}:compensate`, CRM);
  const body = { type, points: type === 'points' ? num(points) : undefined, amount: type !== 'points' ? opt(amount) : undefined, description: desc };
  return (
    <Modal open onClose={onClose} title="Propose Compensation" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!desc || send.isPending} onClick={() => send.mutate(body as unknown as R, { onSuccess: onClose })}>Submit for approval</button>
    </>}>
      <div className="oc-form">
        <SelectField label="Type" value={type} onChange={setType} options={opts(['points', 'voucher', 'refund', 'other'], { points: 'Loyalty points' })} />
        {type === 'points' ? <TextField label="Points" type="number" value={points} onChange={setPoints} />
          : <TextField label="Amount" value={amount} onChange={setAmount} inputMode="decimal" />}
        <TextField label="Description" value={desc} onChange={setDesc} required span />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── Corporate 360 (FR-C360-04) ────────────────────────────────────────────

export function Corporate360Page() {
  const { id } = useParams();
  const d = useGet<R>(`/api/v1/crm/corporate-accounts/${id}/360`);
  const x = d.data;
  const acct = (x?.account ?? {}) as R;
  const bill = (x?.billing ?? {}) as R;
  const golf = (x?.golfActivity ?? {}) as R;
  return (
    <div className="oc-stack">
      <PageHeader title={x ? `Corporate 360 · ${String(acct.name)}` : 'Corporate 360'} actions={<Link className="oc-btn oc-btn-neutral" to="/crm/corporate-accounts">Corporate Accounts</Link>} />
      {d.isLoading ? <Skeleton /> : !x ? <ErrorAlert error={d.error} /> : (
        <>
          <div className="oc-grid">
            <Card title="Company" icon="apartment"><KV items={[['Code', String(acct.code)], ['NPWP', String(acct.npwp ?? '—')], ['Contact', String(acct.contactName ?? '—')],
              ['E-mail', String(acct.email ?? '—')], ['Phone', String(acct.phone ?? '—')], ['Status', <StatusPill key="s" status={String(acct.status)} />]]} /></Card>
            <Card title="Billing & Receivables" icon="receipt_long"><KV items={[['Open invoices', String(bill.openInvoices)], ['Outstanding', money(bill.outstanding)],
              ['Overdue', money(bill.overdue)], ['Invoiced (12 months)', money(bill.invoiced12m)], ['Spend incl. nominees (12 months)', money((x.spend as R)?.last12Months)]]} /></Card>
            <Card title="Golf Activity of Nominees" icon="golf_course"><KV items={[['Rounds (90 days)', String(golf.rounds90)], ['Players', String(golf.players)]]} /></Card>
          </div>
          <Card title="Nominees" icon="groups"><DataTable rows={rows(x.nominees).map((n) => ({ ...n, id: String(n.customerId) })) as R[]} columns={[{ key: 'code', header: 'Code' },
            { key: 'name', header: 'Name' }, { key: 'title', header: 'Title', render: (r) => String(r.title ?? '—') }, { key: 'rounds90', header: 'Rounds (90 days)', align: 'right' },
            { key: 'status', header: 'Status', render: pill('status') }]} /></Card>
          <Card title="Open Invoices" icon="request_quote"><DataTable rows={rows(x.invoices)} columns={[{ key: 'number', header: 'Invoice' },
            { key: 'dueDate', header: 'Due', render: (r) => (r.dueDate ? formatDate(String(r.dueDate)) : '—') }, { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) },
            { key: 'outstanding', header: 'Outstanding', align: 'right', render: (r) => money(r.outstanding) }, { key: 'status', header: 'Status', render: pill('status') }]} /></Card>
          <Card title="Complaints of Nominees" icon="report"><DataTable rows={rows(x.tickets)} columns={[{ key: 'number', header: 'Ticket' }, { key: 'subject', header: 'Subject' },
            { key: 'status', header: 'Status', render: pill('status') }]} /></Card>
          {Object.entries((x.sections ?? {}) as Record<string, unknown>).map(([k, v]) => (
            <Card key={k} title={label(k)} icon="extension"><pre className="oc-small" style={{ whiteSpace: 'pre-wrap', margin: 0 }}>{JSON.stringify(v, null, 2)}</pre></Card>
          ))}
        </>
      )}
    </div>
  );
}

// ── ops: Redeem Points (FR-OPS-P3-03) ─────────────────────────────────────

export function RedeemPointsPage() {
  const toast = useToast();
  const [cust, setCust] = useState('');
  const [folio, setFolio] = useState('');
  const [points, setPoints] = useState('');
  const acct = useGet<Page<R>>(cust ? `/api/v1/crm/loyalty/accounts?filter[customerId]=${cust}` : null);
  const a = acct.data?.items?.[0];
  const folios = useGet<Page<R>>(cust ? `/api/v1/billing/folios?filter[customerId]=${cust}&filter[status]=open&limit=50` : null);
  const [key] = useState(() => crypto.randomUUID());
  const send = useSend<R, R>('POST', `/api/v1/crm/loyalty/accounts/${String(a?.id ?? '')}:redeem`, ['/api/v1/crm', '/api/v1/billing'], () => ({ 'Idempotency-Key': key }));
  const value = a ? Number(points || 0) * Number(a.redemptionValue ?? 0) : 0;
  return (
    <div className="oc-stack">
      <PageHeader title="Redeem Points" help="Pay an open folio with the customer's loyalty points (POS and Front Desk)." />
      <Card title="Customer" icon="person_search"><div className="oc-form"><CustomerPicker value={cust} onChange={(v) => { setCust(v); setFolio(''); }} required /></div></Card>
      {cust && !acct.isLoading && !a && <Empty title="Not a loyalty member" help="Enrol the customer in Back Office → CRM → Loyalty." icon="loyalty" />}
      {a && (
        <Card title={`${String(a.customerName)} · ${String(a.tierName ?? 'No tier')}`} icon="loyalty" ink>
          <KV items={[['Points', <strong key="p" style={{ fontSize: 28 }}>{pts(a.balance)}</strong>], ['Value', money(a.balanceValue)], ['Status', <StatusPill key="s" status={String(a.status)} />]]} />
          <div className="oc-form" style={{ marginTop: 12 }}>
            <SelectField label="Open folio" value={folio} onChange={setFolio} required placeholder="Select"
              options={(folios.data?.items ?? []).map((f) => ({ value: f.id, label: `${String(f.number)} · ${String(f.holderName ?? '')} · ${money(f.balance)}` }))} />
            <TextField label="Points to redeem" type="number" inputMode="numeric" value={points} onChange={setPoints} help={`= ${money(value)}`} />
          </div>
          <button className="oc-btn oc-btn-primary" style={{ minHeight: 56, marginTop: 12 }} disabled={!folio || !points || send.isPending || a.status !== 'active'}
            onClick={() => send.mutate({ folioId: folio, points: Number(points) } as unknown as R, {
              onSuccess: (r) => { toast(`Paid ${money((r.payment as R).amount)} with points`); setPoints(''); },
            })}>Redeem</button>
          <ErrorAlert error={send.error} />
        </Card>
      )}
    </div>
  );
}

/** Back Office routes of the area. */
export const ENGAGEMENT_ROUTES: AreaRoute[] = [
  { path: 'crm/campaigns', perm: 'crm.campaign.view', element: <CampaignsPage /> },
  { path: 'crm/loyalty', perm: 'crm.loyalty_account.view', element: <LoyaltyPage /> },
  { path: 'crm/top-spender', perm: 'crm.top_spender.view', element: <TopSpenderPage /> },
  { path: 'crm/feedback', perm: 'crm.feedback.view', element: <FeedbackPage /> },
  { path: 'crm/complaints', perm: 'crm.ticket.view', element: <ComplaintsPage /> },
  { path: 'crm/corporate-accounts/:id/360', perm: 'crm.corporate_account.view', element: <Corporate360Page /> },
];

/** Ops workstation tiles and routes of the area. */
export const ENGAGEMENT_OPS_TILES: OpsTile[] = [['loyalty', 'Redeem Points', '/ops/front-desk/redeem-points', 'crm.loyalty_account.redeem']];
export const ENGAGEMENT_OPS_ROUTES: OpsRoute[] = [{ path: 'front-desk/redeem-points', element: <RedeemPointsPage /> }];
