import React, { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router';
import { qs, request, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime } from '@oneclub/i18n';
import {
  AutoResourcePage, Card, Checkbox, DataTable, Drawer, Empty, ErrorAlert, Modal, PageHeader, SelectField, Skeleton, StatusPill, TextField,
  useAuth, useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import type { AreaRoute, OpsRoute, OpsTile } from './types';

// Promotions & Packages (PRD P3 EP-10–11) — Back Office screens, ops workstations.
// Commercial → Promotions, Packages, Package Bookings; Pricing → Discounts,
// Promo Codes (Naming Convention §13/§15/§16); the POS promotion panel
// (FR-OPS-P3-03) and the Package Use workstation (FR-PKG-06).

const COM = ['/api/v1/commercial'];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const opts = (vals: string[], labels: Record<string, string> = {}): Option[] => vals.map((v) => ({ value: v, label: labels[v] ?? label(v) }));
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} />;
const PROMO_TYPES = opts(['percent_discount', 'amount_discount', 'happy_hour', 'buy_n_get_x', 'buy_n_price_x', 'bundle', 'member_discount', 'promo_code',
  'period'], { buy_n_get_x: 'Buy N Get X', buy_n_price_x: 'Buy N Price X' });
const CHANNELS = opts(['pos', 'member_app', 'website', 'back_office', 'ops'], { pos: 'POS', ops: 'Operational' });
const PKG_CHANNELS = opts(['back_office', 'website', 'member_app', 'ops', 'quotation']);
const toLocalInput = (d: Date) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);

function Actions({ children }: { children: React.ReactNode }) {
  return <div className="oc-row-wrap">{children}</div>;
}

function benefit(p: R): string {
  switch (p.promoType) {
    case 'buy_n_get_x': return `Buy ${String(p.buyQuantity)} get ${String(p.getQuantity)}${p.discountPercent ? ` (${String(p.discountPercent)}% off)` : ' free'}`;
    case 'buy_n_price_x': return `Buy ${String(p.buyQuantity)} for ${money(p.bundlePrice)}`;
    case 'bundle': return `Bundle ${money(p.bundlePrice)}`;
    default: return p.discountPercent ? `${String(p.discountPercent)}%` : p.discountAmount ? `${money(p.discountAmount)}${p.perUnit ? ' / unit' : ''}` : '—';
  }
}

// ── Promotions (EP-10) ────────────────────────────────────────────────────

const PROMO_COLUMNS = [
  { key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'promoType', header: 'Type', render: (r: R) => label(r.promoType) },
  { key: 'benefit', header: 'Benefit', render: (r: R) => benefit(r) }, { key: 'priority', header: 'Priority', align: 'right' as const },
  { key: 'stackable', header: 'Stacking', render: (r: R) => (r.stackable ? `Stackable${r.stackGroup ? ` (${String(r.stackGroup)})` : ''}` : 'Exclusive') },
  { key: 'usedCount', header: 'Used', align: 'right' as const }, { key: 'usedAmount', header: 'Discount given', align: 'right' as const, render: (r: R) => money(r.usedAmount) },
  { key: 'status', header: 'Status', render: pill('status') },
];

function PromotionList({ title, help, types }: { title: string; help: string; types?: string[] }) {
  const [params, setParams] = useSearchParams();
  const [type, setType] = useState(types?.[0] ?? '');
  const open = params.get('id');
  return (
    <>
      <ListPage title={title} help={help} path="/api/v1/commercial/promotions"
        statuses={opts(['draft', 'pending', 'active', 'inactive', 'rejected', 'expired'])} extraQuery={{ 'filter[promoType]': type }}
        filters={<SelectField label="Type" value={type} onChange={setType} placeholder="All types"
          options={types ? PROMO_TYPES.filter((t) => types.includes(t.value)) : PROMO_TYPES} />}
        onRowClick={(r) => setParams({ ...Object.fromEntries(params), id: r.id })} columns={PROMO_COLUMNS} />
      {open && <PromotionDrawer id={open} onClose={() => { params.delete('id'); setParams(params); }} />}
    </>
  );
}

export function PromotionsPage() {
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'list';
  return (
    <div className="oc-stack">
      <Tabs tabs={[{ value: 'list', label: 'Promotions' }, { value: 'edit', label: 'Create & edit' }]} value={tab} onChange={(v) => setParams({ tab: v })} />
      {tab === 'list'
        ? <PromotionList title="Promotions" help="Happy hour, Buy N Get X, Buy N Price X, bundles, member discounts, promo codes and period promotions. A promotion runs only after it is activated (with approval when the Promotion Policies require it); an active promotion is deactivated before it is changed." />
        : <AutoResourcePage resourceKey="commercial.promotion" />}
    </div>
  );
}

export function DiscountsPage() {
  return (
    <div className="oc-stack">
      <PromotionList title="Discounts" types={['percent_discount', 'amount_discount', 'member_discount', 'period']}
        help="Percentage and amount discounts, member discounts and period promotions. Manual discount limits per role are in Settings → Club Policies → Pricing Policies." />
    </div>
  );
}

function PromotionDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const d = useGet<R>(`/api/v1/commercial/promotions/${id}`);
  const red = useGet<Page<R>>(`/api/v1/commercial/promotions/${id}/redemptions?limit=50`);
  const x = d.data;
  const base = `/api/v1/commercial/promotions/${id}`;
  return (
    <Drawer open onClose={onClose} title={x ? `Promotion ${String(x.code)}` : 'Promotion'}>
      {d.isLoading && <Skeleton />}
      <ErrorAlert error={d.error} />
      {x && (
        <div className="oc-stack">
          <div className="oc-row"><StatusPill status={String(x.status)} /><span className="oc-small oc-muted">v{String(x.version)}</span><span className="oc-spacer" />
            <strong>{benefit(x)}</strong></div>
          <Actions>
            {can('commercial.promotion.activate') && ['draft', 'inactive', 'rejected', 'expired'].includes(String(x.status)) &&
              <ActionButton kind="primary" label="Activate" path={`${base}:activate`} invalidate={COM} />}
            {can('commercial.promotion.approve') && x.status === 'pending' && <ActionButton kind="primary" label="Approve" path={`${base}:approve`} reason="optional" invalidate={COM} />}
            {can('commercial.promotion.activate') && ['active', 'pending'].includes(String(x.status)) &&
              <ActionButton danger label="Deactivate" path={`${base}:deactivate`} reason="optional" invalidate={COM} />}
          </Actions>
          <KV items={[['Name', String(x.name)], ['Type', label(x.promoType)], ['Priority', String(x.priority)],
            ['Channels', (x.channels as string[] | undefined)?.length ? (x.channels as string[]).map(label).join(', ') : 'All'],
            ['Business lines', (x.businessLines as string[] | undefined)?.length ? (x.businessLines as string[]).join(', ') : 'All'],
            ['Valid', `${x.validFrom ? formatDate(String(x.validFrom)) : 'open'} – ${x.validTo ? formatDate(String(x.validTo)) : 'open'}`],
            ['Hours', ((x.timeWindows as R[] | undefined) ?? []).map((w) => `${((w.days as number[] | undefined) ?? []).join(',') || 'daily'} ${String(w.start)}–${String(w.end)}`).join('; ') || 'Any time'],
            ['Minimum purchase', x.minPurchase ? money(x.minPurchase) : '—'], ['Requires code', x.requiresCode ? 'Yes' : 'No'],
            ['Budget', x.budgetAmount ? `${money(x.usedAmount)} of ${money(x.budgetAmount)}` : '—'],
            ['Redemptions', x.maxRedemptions ? `${String(x.usedCount)} of ${String(x.maxRedemptions)}` : String(x.usedCount)],
            ['Activated', x.activatedAt ? formatDateTime(String(x.activatedAt)) : '—']]} />
          <Simulate id={id} />
          <Card title="Usage (redemption ledger)" icon="receipt_long">
            <DataTable rows={red.data?.items} loading={red.isLoading} error={red.error} columns={[
              { key: 'createdAt', header: 'When', render: (r) => formatDateTime(String(r.createdAt)) }, { key: 'sourceType', header: 'Source', render: (r) => label(r.sourceType) },
              { key: 'sourceRef', header: 'Document', render: (r) => String(r.sourceRef ?? '—') }, { key: 'channel', header: 'Channel', render: (r) => label(r.channel) },
              { key: 'promoCode', header: 'Code', render: (r) => String(r.promoCode ?? '—') },
              { key: 'discount', header: 'Discount', align: 'right', render: (r) => money(r.discount) },
              { key: 'status', header: 'Status', render: (r) => <>{<StatusPill status={String(r.status)} />}{r.needsReview ? <StatusPill status="needs_review" label="Review" /> : null}</> }]} />
          </Card>
        </div>
      )}
    </Drawer>
  );
}

/** FR-PRM-08: example prices before the promotion is activated. */
function Simulate({ id }: { id: string }) {
  const products = useGet<Page<R>>('/api/v1/commercial/products?limit=200&filter[status]=active');
  const [product, setProduct] = useState('');
  const [serviceType, setServiceType] = useState('');
  const [qty, setQty] = useState('2');
  const [price, setPrice] = useState('');
  const [at, setAt] = useState(toLocalInput(new Date()));
  const [channel, setChannel] = useState('pos');
  const sim = useSend<R, R>('POST', `/api/v1/commercial/promotions/${id}:simulate`);
  const p = (products.data?.items ?? []).find((x) => x.id === product);
  const unit = price || (p ? String(p.price ?? '') : '');
  const res = (sim.data?.scenarios as R[] | undefined)?.[0];
  return (
    <Card title="Simulate" icon="science">
      <div className="oc-form">
        <SelectField label="Product" value={product} onChange={setProduct} placeholder="Service line instead"
          options={(products.data?.items ?? []).map((x) => ({ value: x.id, label: String(x.name) }))} />
        {!product && <TextField label="Service type" value={serviceType} onChange={setServiceType} help="e.g. sport_court, bungalow, package" />}
        <TextField label="Quantity" type="number" value={qty} onChange={setQty} />
        <TextField label="Unit price" type="number" value={unit} onChange={setPrice} />
        <TextField label="Sale time" type="datetime-local" value={at} onChange={setAt} />
        <SelectField label="Channel" value={channel} onChange={setChannel} options={CHANNELS} />
      </div>
      <Actions>
        <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!unit || sim.isPending} onClick={() => sim.mutate({ scenarios: [{ at: new Date(at).toISOString(),
          channel, lines: [{ productId: product || undefined, serviceType: product ? undefined : serviceType || undefined, quantity: qty, unitPrice: unit }] }] } as unknown as R)}>Simulate</button>
      </Actions>
      <ErrorAlert error={sim.error} />
      {res && <KV items={[['Subtotal', money(res.subtotal)], ['Discount', money(res.discount)], ['Total', <strong key="t">{money(res.total)}</strong>],
        ['Not applied', ((res.rejected as R[] | undefined) ?? []).map((r) => String(r.reason)).join('; ') || '—']]} />}
    </Card>
  );
}

// ── Promo codes (FR-PRM-04) ───────────────────────────────────────────────

export function PromoCodesPage() {
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'codes';
  return (
    <div className="oc-stack">
      <PageHeader title="Promo Codes" help="General codes and unique codes per recipient (campaigns), with total and per-customer limits. Codes are checked at the POS, the website checkout and in the member app." />
      <Tabs tabs={[{ value: 'codes', label: 'Codes' }, { value: 'generate', label: 'Generate' }, { value: 'check', label: 'Check a code' }]} value={tab}
        onChange={(v) => setParams({ tab: v })} />
      {tab === 'codes' && <AutoResourcePage resourceKey="commercial.promo_code" />}
      {tab === 'generate' && <GenerateCodes />}
      {tab === 'check' && <CheckCode />}
    </div>
  );
}

function usePromotionOptions(): Option[] {
  const list = useGet<Page<R>>('/api/v1/commercial/promotions?limit=200');
  return (list.data?.items ?? []).map((p) => ({ value: p.id, label: `${String(p.code)} · ${String(p.name)}` }));
}

function GenerateCodes() {
  const promotions = usePromotionOptions();
  const [f, setF] = useState<Record<string, string>>({ promotionId: '', count: '10', prefix: '', length: '8', maxUses: '1', maxUsesPerCustomer: '', expiresAt: '', campaignRef: '', customers: '' });
  const set = (k: string) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const send = useSend<R, R>('POST', '/api/v1/commercial/promo-codes:generate', COM);
  const customers = f.customers.split(/[\s,]+/).filter(Boolean);
  const num = (v: string) => (v ? Number(v) : undefined);
  return (
    <Card title="Generate unique codes" icon="confirmation_number">
      <div className="oc-form">
        <SelectField label="Promotion" value={f.promotionId} onChange={set('promotionId')} options={promotions} required placeholder="Select" />
        <TextField label="Number of codes" type="number" value={f.count} onChange={set('count')} help="Ignored when customers are given" />
        <TextField label="Prefix" value={f.prefix} onChange={set('prefix')} />
        <TextField label="Random length" type="number" value={f.length} onChange={set('length')} />
        <TextField label="Uses per code" type="number" value={f.maxUses} onChange={set('maxUses')} />
        <TextField label="Uses per customer" type="number" value={f.maxUsesPerCustomer} onChange={set('maxUsesPerCustomer')} />
        <TextField label="Expires" type="datetime-local" value={f.expiresAt} onChange={set('expiresAt')} />
        <TextField label="Campaign" value={f.campaignRef} onChange={set('campaignRef')} />
        <TextField label="Customer ids (one personal code each)" value={f.customers} onChange={set('customers')} span />
      </div>
      <Actions>
        <button className="oc-btn oc-btn-primary" disabled={!f.promotionId || send.isPending} onClick={() => send.mutate({ promotionId: f.promotionId,
          count: customers.length ? undefined : num(f.count), customerIds: customers.length ? customers : undefined, prefix: f.prefix || undefined,
          length: num(f.length), maxUses: num(f.maxUses), maxUsesPerCustomer: num(f.maxUsesPerCustomer),
          expiresAt: f.expiresAt ? new Date(f.expiresAt).toISOString() : undefined, campaignRef: f.campaignRef || undefined } as unknown as R)}>Generate</button>
      </Actions>
      <ErrorAlert error={send.error} />
      {send.data && <DataTable rows={((send.data.codes as R[]) ?? []).map((c) => ({ ...c, id: String(c.id) })) as R[]} columns={[{ key: 'code', header: 'Code' },
        { key: 'customerId', header: 'Customer', render: (r) => String(r.customerId ?? '—') }]} />}
    </Card>
  );
}

function CheckCode() {
  const [code, setCode] = useState('');
  const [amount, setAmount] = useState('');
  const [channel, setChannel] = useState('pos');
  const send = useSend<R, R>('POST', '/api/v1/commercial/promo-codes:check');
  const r = send.data;
  return (
    <Card title="Check a promo code" icon="fact_check">
      <div className="oc-form">
        <TextField label="Code" value={code} onChange={setCode} required />
        <TextField label="Purchase amount" type="number" value={amount} onChange={setAmount} />
        <SelectField label="Channel" value={channel} onChange={setChannel} options={CHANNELS} />
      </div>
      <Actions><button className="oc-btn oc-btn-neutral" disabled={!code || send.isPending}
        onClick={() => send.mutate({ code, amount: amount || undefined, channel } as unknown as R)}>Check</button></Actions>
      <ErrorAlert error={send.error} />
      {r && <KV items={[['Result', <StatusPill key="s" status={r.valid ? 'valid' : 'invalid'} label={r.valid ? 'Valid' : 'Not valid'} />],
        ['Reason', String(r.reason ?? '—')], ['Promotion', r.promotion ? String((r.promotion as R).name) : '—'],
        ['Uses left', r.usesLeft != null ? String(r.usesLeft) : '—'], ['Expires', r.expiresAt ? formatDateTime(String(r.expiresAt)) : '—']]} />}
    </Card>
  );
}

// ── Packages (EP-11) ──────────────────────────────────────────────────────

export function PackagesPage() {
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'list';
  const open = params.get('id');
  return (
    <div className="oc-stack">
      <Tabs tabs={[{ value: 'list', label: 'Packages' }, { value: 'edit', label: 'Create & edit' }, { value: 'components', label: 'Components' }]} value={tab}
        onChange={(v) => setParams({ tab: v })} />
      {tab === 'list' && (
        <ListPage title="Packages" path="/api/v1/commercial/packages" statuses={opts(['draft', 'active', 'inactive'])}
          help="Cross-line packages (Golf Day, Stay & Golf, Corporate, Wedding, Family): components, pricing per package / pax / night, availability and revenue allocation. Publishing freezes a version that bookings keep."
          onRowClick={(r) => setParams({ tab, id: r.id })} columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' },
            { key: 'packageType', header: 'Type', render: (r) => label(r.packageType) },
            { key: 'price', header: 'Price', align: 'right', render: (r) => `${money(r.price)} ${r.pricingMode === 'fixed' ? '' : `/ ${label(r.pricingMode).replace('per ', '')}`}` },
            { key: 'taxMode', header: 'Nett / ++', render: (r) => (r.taxMode === 'plus_plus' ? '++' : 'Nett') },
            { key: 'version', header: 'Version', align: 'right' }, { key: 'status', header: 'Status', render: pill('status') }]} />
      )}
      {tab === 'edit' && <AutoResourcePage resourceKey="commercial.package" />}
      {tab === 'components' && <AutoResourcePage resourceKey="commercial.package_component" />}
      {open && <PackageDrawer id={open} onClose={() => setParams({ tab })} />}
    </div>
  );
}

function PackageDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const d = useGet<R>(`/api/v1/commercial/packages/${id}`);
  const comps = useGet<Page<R>>(`/api/v1/commercial/package-components?filter[packageId]=${id}&limit=100`);
  const versions = useGet<Page<R>>(`/api/v1/commercial/packages/${id}/versions`);
  const [date, setDate] = useState(today());
  const [pax, setPax] = useState('');
  const [days, setDays] = useState('7');
  const [check, setCheck] = useState(false);
  const avail = useGet<Page<R>>(check ? `/api/v1/commercial/packages/${id}/availability${qs({ date, pax, days })}` : null);
  const x = d.data;
  return (
    <Drawer open onClose={onClose} title={x ? `Package ${String(x.code)}` : 'Package'}>
      {d.isLoading && <Skeleton />}
      {x && (
        <div className="oc-stack">
          <div className="oc-row"><StatusPill status={String(x.status)} /><span className="oc-small oc-muted">published v{String(x.version)}</span><span className="oc-spacer" />
            {can('commercial.package.publish') && <ActionButton kind="primary" label="Publish" path={`/api/v1/commercial/packages/${id}:publish`} invalidate={COM}
              confirm="Publish the current definition as a new version? Existing bookings keep their version." />}</div>
          <KV items={[['Name', String(x.name)], ['Type', label(x.packageType)], ['Price', `${money(x.price)} (${label(x.pricingMode)}, ${x.taxMode === 'plus_plus' ? '++' : 'nett'})`],
            ['Pax', `${String(x.minPax)}${x.maxPax ? `–${String(x.maxPax)}` : '+'}`], ['Nights', String(x.nights)], ['Allocation', label(x.allocationMethod)],
            ['On sale', `${x.sellFrom ? formatDate(String(x.sellFrom)) : 'open'} – ${x.sellTo ? formatDate(String(x.sellTo)) : 'open'}`],
            ['Daily quota', x.dailyQuota ? String(x.dailyQuota) : '—'],
            ['Cancellation', `free until ${String(x.cancellationHours)} h before, then ${String(x.cancellationFeePercent)}% of the unused part`]]} />
          <Card title="Components" icon="widgets">
            <DataTable rows={comps.data?.items} loading={comps.isLoading} columns={[{ key: 'seq', header: '#' }, { key: 'name', header: 'Component' },
              { key: 'componentType', header: 'Type', render: (r) => label(r.componentType) },
              { key: 'quantity', header: 'Qty', render: (r) => `${String(r.quantity)}${r.perPax ? ' / pax' : ''}${r.perNight ? ' / night' : ''}` },
              { key: 'standalonePrice', header: 'Standalone', align: 'right', render: (r) => money(r.standalonePrice) },
              { key: 'revenueComponent', header: 'Revenue', render: (r) => `${label(r.revenueComponent)}${r.liability ? ' (liability)' : ''}` },
              { key: 'optional', header: 'Add-on', render: (r) => (r.optional ? money(r.addonPrice) : '—') }]} />
          </Card>
          <Card title="Availability" icon="event_available">
            <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
              <TextField label="From" type="date" value={date} onChange={(v) => { setDate(v); setCheck(false); }} />
              <TextField label="Days" type="number" value={days} onChange={(v) => { setDays(v); setCheck(false); }} />
              <TextField label="Pax" type="number" value={pax} onChange={(v) => { setPax(v); setCheck(false); }} />
              <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setCheck(true)}>Check</button>
            </div>
            <ErrorAlert error={avail.error} />
            {avail.data && <DataTable rows={avail.data.items.map((a) => ({ ...a, id: String(a.date) })) as R[]} columns={[
              { key: 'date', header: 'Date', render: (r) => formatDate(String(r.date)) },
              { key: 'available', header: 'Available', render: (r) => <StatusPill status={r.available ? 'available' : 'full'} /> },
              { key: 'price', header: 'Price', align: 'right', render: (r) => money(r.price) },
              { key: 'quotaLeft', header: 'Quota left', render: (r) => (r.quotaLeft != null ? String(r.quotaLeft) : '—') },
              { key: 'reason', header: 'Reason', render: (r) => String(r.reason ?? '') }]} />}
          </Card>
          <Card title="Package History" icon="history">
            <DataTable rows={(versions.data?.items ?? []).map((v) => ({ ...v, id: String(v.version) })) as R[]} columns={[{ key: 'version', header: 'Version' },
              { key: 'publishedAt', header: 'Published', render: (r) => formatDateTime(String(r.publishedAt)) },
              { key: 'publishedBy', header: 'By', render: (r) => String(r.publishedBy ?? '—') },
              { key: 'price', header: 'Price', align: 'right', render: (r) => money((r.snapshot as R | undefined)?.price) }]} />
          </Card>
        </div>
      )}
    </Drawer>
  );
}

// ── Package bookings (FR-PKG-04..08) ──────────────────────────────────────

export function PackageBookingsPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const [date, setDate] = useState('');
  const [modal, setModal] = useState(false);
  const open = params.get('id');
  return (
    <>
      <ListPage title="Package Bookings" path="/api/v1/commercial/package-bookings" search={false}
        help="Package bookings allocate every component at once (all or nothing), split the price over the components and post them on the customer's folio."
        statuses={opts(['pending', 'confirmed', 'completed', 'cancelled', 'expired'])} extraQuery={{ date }}
        filters={<TextField label="Start date" type="date" value={date} onChange={setDate} />}
        actions={can('commercial.package_booking.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setModal(true)}>New Package Booking</button> : undefined}
        onRowClick={(r) => setParams({ id: r.id })} columns={[{ key: 'number', header: 'Booking' }, { key: 'packageName', header: 'Package' },
          { key: 'guest', header: 'Guest', render: (r) => String(r.customerName ?? r.guestName ?? '—') },
          { key: 'startDate', header: 'Start', render: (r) => formatDate(String(r.startDate)) }, { key: 'pax', header: 'Pax', align: 'right' },
          { key: 'channel', header: 'Channel', render: (r) => label(r.channel) }, { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {modal && <BookingForm onClose={() => setModal(false)} onDone={(b) => { setModal(false); setParams({ id: b }); }} />}
      {open && <BookingDrawer id={open} onClose={() => setParams({})} />}
    </>
  );
}

function BookingForm({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const pkgs = useGet<Page<R>>('/api/v1/commercial/packages?limit=200&filter[status]=active');
  const [f, setF] = useState<Record<string, string>>({ packageId: '', startDate: today(), pax: '', nights: '', customerId: '', guestName: '', guestPhone: '',
    guestEmail: '', channel: 'back_office', promoCodes: '', notes: '', method: '' });
  const [addons, setAddons] = useState<string[]>([]);
  const [q, setQ] = useState('');
  const customers = useGet<Page<R>>(`/api/v1/crm/customers${qs({ q, limit: 20, 'filter[status]': 'active' })}`);
  const comps = useGet<Page<R>>(f.packageId ? `/api/v1/commercial/package-components?filter[packageId]=${f.packageId}&limit=100` : null);
  const set = (k: string) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const send = useSend<R, R>('POST', '/api/v1/commercial/package-bookings', COM, () => ({ 'Idempotency-Key': crypto.randomUUID() }));
  const fe = send.error?.fieldErrors ?? {};
  const body = {
    packageId: f.packageId, startDate: f.startDate, pax: f.pax ? Number(f.pax) : undefined, nights: f.nights ? Number(f.nights) : undefined,
    customerId: f.customerId || undefined, guestName: f.guestName || undefined, guestPhone: f.guestPhone || undefined, guestEmail: f.guestEmail || undefined,
    channel: f.channel, addons: addons.length ? addons : undefined, notes: f.notes || undefined,
    promoCodes: f.promoCodes ? f.promoCodes.split(/[\s,]+/).filter(Boolean) : undefined,
    payment: f.method ? { methodType: f.method } : undefined,
  };
  return (
    <Modal open onClose={onClose} title="New Package Booking" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!f.packageId || !f.startDate || send.isPending}
        onClick={() => send.mutate(body as unknown as R, { onSuccess: (b) => onDone(b.id) })}>Book</button>
    </>}>
      <div className="oc-form">
        <SelectField label="Package" value={f.packageId} onChange={set('packageId')} required placeholder="Select"
          options={(pkgs.data?.items ?? []).map((p) => ({ value: p.id, label: `${String(p.name)} (${String(p.code)})` }))} />
        <TextField label="Start date" type="date" value={f.startDate} onChange={set('startDate')} required error={fe.startDate} />
        <TextField label="Pax" type="number" value={f.pax} onChange={set('pax')} help="Default: the package minimum" />
        <TextField label="Nights" type="number" value={f.nights} onChange={set('nights')} help="Default: the package nights" />
        <TextField label="Find customer" value={q} onChange={setQ} placeholder="Name, phone, e-mail or code" />
        <SelectField label="Customer" value={f.customerId} onChange={set('customerId')} placeholder="Walk-in guest"
          options={(customers.data?.items ?? []).map((c) => ({ value: c.id, label: String(c.name) }))} />
        {!f.customerId && <TextField label="Guest name" value={f.guestName} onChange={set('guestName')} error={fe.guestName} />}
        {!f.customerId && <TextField label="Guest phone" value={f.guestPhone} onChange={set('guestPhone')} />}
        {!f.customerId && <TextField label="Guest e-mail" type="email" value={f.guestEmail} onChange={set('guestEmail')} />}
        <SelectField label="Channel" value={f.channel} onChange={set('channel')} options={PKG_CHANNELS.filter((c) => !['website', 'member_app'].includes(c.value))} />
        <TextField label="Promo codes" value={f.promoCodes} onChange={set('promoCodes')} />
        <SelectField label="Take payment now" value={f.method} onChange={set('method')} placeholder="Later (folio)"
          options={opts(['cash', 'bank_transfer', 'qris', 'card', 'member_account'], { qris: 'QRIS' })} />
        <TextField label="Notes" value={f.notes} onChange={set('notes')} span />
      </div>
      {(comps.data?.items ?? []).filter((c) => c.optional).map((c) => (
        <Checkbox key={c.id} label={`Add-on: ${String(c.name)} (${money(c.addonPrice)}${c.perPax ? ' / pax' : ''})`} checked={addons.includes(c.id)}
          onChange={(on) => setAddons(on ? [...addons, c.id] : addons.filter((a) => a !== c.id))} />
      ))}
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function BookingDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const d = useGet<R>(`/api/v1/commercial/package-bookings/${id}`);
  const x = d.data;
  const comps = (x?.components as R[] | undefined) ?? [];
  const promos = (x?.promotions as R[] | undefined) ?? [];
  const schedule = x?.schedule as (R & { lines: R[] }) | undefined;
  const sum = comps.reduce((s, c) => s + Number(c.allocatedTotal ?? 0), 0);
  return (
    <Drawer open onClose={onClose} title={x ? `Package Booking ${String(x.number)}` : 'Package Booking'}>
      {d.isLoading && <Skeleton />}
      {x && (
        <div className="oc-stack">
          <div className="oc-row"><StatusPill status={String(x.status)} /><span className="oc-small oc-muted">{String(x.packageName)} v{String(x.packageVersion)}</span>
            <span className="oc-spacer" /><strong>{money(x.total)}</strong></div>
          {can('commercial.package_booking.cancel') && ['pending', 'confirmed'].includes(String(x.status)) &&
            <Actions><ActionButton danger label="Cancel booking" path={`/api/v1/commercial/package-bookings/${id}:cancel`} reason="required" invalidate={COM}
              confirm="Unused components are released and refunded; the late cancellation fee of the package applies." /></Actions>}
          <KV items={[['Guest', String(x.customerName ?? x.guestName ?? '—')], ['Start', formatDate(String(x.startDate))], ['Pax', String(x.pax)],
            ['Nights', String(x.nights)], ['Channel', label(x.channel)], ['List price', money(x.listTotal)], ['Promotion discount', money(x.discountTotal)],
            ['Net / service / tax', `${money(x.netTotal)} / ${money(x.serviceTotal)} / ${money(x.taxTotal)}`],
            ['Hold until', x.holdExpiresAt ? formatDateTime(String(x.holdExpiresAt)) : '—'], ['Cancellation fee', x.cancellationFee ? money(x.cancellationFee) : '—']]} />
          <Card title="Components & revenue allocation" icon="widgets">
            <DataTable rows={comps} columns={[{ key: 'name', header: 'Component' },
              { key: 'serviceDate', header: 'Date', render: (r) => formatDate(String(r.serviceDate)) },
              { key: 'allocationRef', header: 'Allocated', render: (r) => String(r.allocationRef ?? '—') },
              { key: 'qty', header: 'Used', render: (r) => `${String(r.consumedQuantity)} / ${String(r.quantity)}` },
              { key: 'revenueComponent', header: 'Revenue', render: (r) => `${label(r.revenueComponent)}${r.liability ? ' (liability)' : ''}` },
              { key: 'allocatedTotal', header: 'Allocation', align: 'right', render: (r) => money(r.allocatedTotal) },
              { key: 'status', header: 'Status', render: pill('status') }]}
              actions={(c) => (can('commercial.package_booking.consume') && x.status === 'confirmed' && c.status === 'unused'
                ? <ConsumeButton bookingId={id} component={c} /> : null)} />
            <div className="oc-row oc-small"><span className="oc-muted">Allocated in total</span><span className="oc-spacer" /><strong>{money(sum)}</strong></div>
          </Card>
          {promos.length > 0 && <Card title="Promotions" icon="sell">
            <DataTable rows={promos} columns={[{ key: 'promotionCode', header: 'Promotion' }, { key: 'promoCode', header: 'Code', render: (r) => String(r.promoCode ?? '—') },
              { key: 'discount', header: 'Discount', align: 'right', render: (r) => money(r.discount) }, { key: 'status', header: 'Status', render: pill('status') }]} />
          </Card>}
          {schedule && <Card title={`Payment schedule ${String(schedule.number ?? '')}`} icon="event_repeat">
            <DataTable rows={schedule.lines} columns={[{ key: 'label', header: 'Term' }, { key: 'dueDate', header: 'Due', render: (r) => formatDate(String(r.dueDate)) },
              { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }, { key: 'status', header: 'Status', render: pill('status') }]} />
          </Card>}
          {x.folio ? <KV items={[['Folio charges', money((x.folio as R).charges)], ['Paid', money((x.folio as R).payments)], ['Balance', money((x.folio as R).balance)]]} /> : null}
        </div>
      )}
    </Drawer>
  );
}

function ConsumeButton({ bookingId, component }: { bookingId: string; component: R }) {
  const left = Number(component.quantity) - Number(component.consumedQuantity);
  return <ActionButton kind="primary" label={left > 1 ? 'Use 1' : 'Use'} path={`/api/v1/commercial/package-bookings/${bookingId}/consumption`}
    body={{ bookingComponentId: component.id, quantity: '1' }} invalidate={COM} />;
}

// ── Ops: Package Use (FR-PKG-06) ──────────────────────────────────────────

export function PackageUsePage() {
  const [date, setDate] = useState(today());
  const [open, setOpen] = useState<string | null>(null);
  const list = useGet<Page<R>>(`/api/v1/commercial/package-bookings${qs({ date, 'filter[status]': 'confirmed', limit: 100 })}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Package Use" help="Mark the components of today's packages as used (breakfast, lunch, caddy …). Check-ins and voucher redemptions are recorded automatically." />
      <TextField label="Start date" type="date" value={date} onChange={setDate} />
      {list.isLoading && <Skeleton />}
      {(list.data?.items.length ?? 0) === 0 && !list.isLoading && <Empty title="No confirmed packages on this date" icon="card_travel" />}
      <div className="oc-grid">
        {(list.data?.items ?? []).map((b) => (
          <button key={b.id} className="oc-card" style={{ textAlign: 'left', minHeight: 100, cursor: 'pointer' }} onClick={() => setOpen(b.id)}>
            <strong>{String(b.number)}</strong><div>{String(b.packageName)}</div>
            <div className="oc-small oc-muted">{String(b.customerName ?? b.guestName ?? '')} · {String(b.pax)} pax</div>
          </button>
        ))}
      </div>
      {open && <BookingDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

// ── POS promotions (FR-PRM-06, FR-PRM-09, FR-OPS-P3-03) ──────────────────

type CacheRule = R & { promoType: string; priority: number; stackable: boolean };
interface PromotionCache { promotions: CacheRule[]; promoCodes: { code: string; promotionId: string; expiresAt?: string | null }[]; selection: string;
  maxStackedPercent: string; validUntil: string; timezone: string }
export interface PosLine { productId: string; quantity: number; unitPrice: number; category?: string }
export interface PosApplied { promotionId: string; code: string; name: string; discount: number; promoCode?: string }
export interface PosPromotionState {
  discount: number; applied: PosApplied[]; rejected: string[]; offline: boolean; codes: string[]; exclusions: string[];
  addCode: (c: string) => void; removeCode: (c: string) => void; exclude: (id: string) => void; restore: (id: string) => void;
  /** Fields for the order of the sale (sent through the sync queue). */
  orderFields: (total: number) => Record<string, unknown>;
}

const CACHE_KEY = (outlet: string) => `oc.pos.promotions.${outlet}`;

function readCache(outlet: string): PromotionCache | null {
  try {
    const raw = localStorage.getItem(CACHE_KEY(outlet));
    const c = raw ? (JSON.parse(raw) as PromotionCache) : null;
    return c && new Date(c.validUntil).getTime() > Date.now() ? c : null;
  } catch {
    return null;
  }
}

/** Local time parts of a moment in the club's timezone. */
function localParts(at: Date, tz: string) {
  const f = new Intl.DateTimeFormat('en-CA', { timeZone: tz || undefined, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
    hourCycle: 'h23', weekday: 'short' });
  const p = Object.fromEntries(f.formatToParts(at).map((x) => [x.type, x.value]));
  const wd = ({ Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6, Sun: 7 } as Record<string, number>)[p.weekday] ?? 1;
  return { day: `${p.year}-${p.month}-${p.day}`, hhmm: `${p.hour}:${p.minute}`, wd };
}

function inWindows(ws: R[], hhmm: string, wd: number): boolean {
  const prev = wd === 1 ? 7 : wd - 1;
  return ws.some((w) => {
    const days = (w.days as number[] | undefined) ?? [];
    const s = String(w.start), e = String(w.end);
    if (s < e) return (days.length === 0 || days.includes(wd)) && hhmm >= s && hhmm < e;
    return ((days.length === 0 || days.includes(wd)) && hhmm >= s) || ((days.length === 0 || days.includes(prev)) && hhmm < e);
  });
}

/**
 * The promotion engine of an offline terminal (FR-PRM-09): the same rules
 * as the server — candidates by channel, outlet, product, validity, weekday,
 * hours and promo code; priority (or best price) order; non-stackable
 * promotions take untouched lines; percent, amount, Buy N Get X, Buy N
 * Price X and bundles. Usage limits, budgets and customer segments are
 * checked by the server at sync, which flags a different total for review.
 */
export function evaluateOffline(cache: PromotionCache, outletId: string, lines: PosLine[], codes: string[], exclusions: string[], at = new Date()) {
  const { day, hhmm, wd } = localParts(at, cache.timezone);
  const known = new Map(cache.promoCodes.filter((c) => !c.expiresAt || new Date(c.expiresAt) > at).map((c) => [c.code, c.promotionId]));
  const entered = new Map<string, string>();
  codes.forEach((c) => { const p = known.get(c); if (p) entered.set(p, c); });
  const st = lines.map((l) => ({ l, gross: Math.round(l.unitPrice * l.quantity), remaining: Math.round(l.unitPrice * l.quantity), applied: [] as CacheRule[] }));
  const subtotal = st.reduce((s, x) => s + x.gross, 0);
  const lineOk = (p: CacheRule, l: PosLine) => {
    const prods = (p.productIds as string[] | undefined) ?? [];
    const cats = (p.categories as string[] | undefined) ?? [];
    const svc = (p.serviceTypes as string[] | undefined) ?? [];
    if (svc.length && !svc.includes('pos')) return false;
    if (prods.length && !prods.includes(l.productId)) return false;
    if (cats.length && !(l.category && cats.includes(l.category))) return false;
    if (p.promoType === 'bundle') return ((p.bundleItems as R[] | undefined) ?? []).some((b) => b.productId === l.productId);
    return true;
  };
  const eligible = (p: CacheRule) => {
    if (exclusions.includes(String(p.id))) return false;
    if (p.requiresCode && !entered.has(String(p.id))) return false;
    const ch = (p.channels as string[] | undefined) ?? [];
    if (ch.length && !ch.includes('pos')) return false;
    const outs = (p.outletIds as string[] | undefined) ?? [];
    if (outs.length && !outs.includes(outletId)) return false;
    if ((p.validFrom && day < String(p.validFrom)) || (p.validTo && day > String(p.validTo))) return false;
    const wds = (p.weekdays as number[] | undefined) ?? [];
    if (wds.length && !wds.includes(wd)) return false;
    const tw = (p.timeWindows as R[] | undefined) ?? [];
    if (tw.length && !inWindows(tw, hhmm, wd)) return false;
    if (((p.segments as string[] | undefined) ?? []).length || ((p.membershipTypes as string[] | undefined) ?? []).length) return false;
    if (p.minPurchase && subtotal < Number(p.minPurchase)) return false;
    return true;
  };
  type S = (typeof st)[number];
  const benefit = (p: CacheRule, use: S[]): number[] => {
    const per = use.map(() => 0);
    const units = use.reduce((s, x) => s + x.l.quantity, 0);
    if (p.minQuantity && units < Number(p.minQuantity)) return per;
    const pct = Number(p.discountPercent ?? 0), amt = Number(p.discountAmount ?? 0);
    const unitList = () => use.flatMap((x, i) => Array.from({ length: Math.floor(x.l.quantity) }, () => ({ i, v: x.remaining / x.l.quantity })))
      .sort((a, b) => b.v - a.v);
    if (p.promoType === 'buy_n_get_x') {
      const n = Number(p.buyQuantity), g = Number(p.getQuantity), free = pct > 0 ? pct : 100;
      const u = unitList();
      for (let k = 0; (k + 1) * (n + g) <= u.length; k++) u.slice(k * (n + g) + n, (k + 1) * (n + g)).forEach((x) => { per[x.i] += (x.v * free) / 100; });
    } else if (p.promoType === 'buy_n_price_x' || p.promoType === 'bundle') {
      const price = Number(p.bundlePrice ?? 0);
      let groups: { i: number; v: number }[][] = [];
      if (p.promoType === 'buy_n_price_x') {
        const n = Number(p.buyQuantity);
        const u = unitList();
        for (let k = 0; (k + 1) * n <= u.length; k++) groups.push(u.slice(k * n, (k + 1) * n));
      } else {
        const items = (p.bundleItems as R[] | undefined) ?? [];
        const pool = new Map<string, { i: number; v: number }[]>();
        use.forEach((x, i) => pool.set(x.l.productId, [...(pool.get(x.l.productId) ?? []), ...Array.from({ length: Math.floor(x.l.quantity) }, () => ({ i, v: x.remaining / x.l.quantity }))]));
        const count = Math.min(...items.map((b) => Math.floor((pool.get(String(b.productId))?.length ?? 0) / Math.max(Number(b.quantity), 1))));
        groups = Array.from({ length: Number.isFinite(count) ? count : 0 }, (_, k) => items.flatMap((b) => (pool.get(String(b.productId)) ?? [])
          .slice(k * Number(b.quantity), (k + 1) * Number(b.quantity))));
      }
      groups.forEach((g) => {
        const list = g.reduce((s, x) => s + x.v, 0);
        const d = list - price;
        if (d > 0 && list > 0) g.forEach((x) => { per[x.i] += (d * x.v) / list; });
      });
    } else if (pct > 0) {
      use.forEach((x, i) => { per[i] = Math.round((x.remaining * pct) / 100); });
    } else if (amt > 0 && p.perUnit) {
      use.forEach((x, i) => { per[i] = Math.min(Math.round(amt * x.l.quantity), x.remaining); });
    } else if (amt > 0) {
      const base = use.reduce((s, x) => s + x.remaining, 0);
      const total = Math.min(amt, base);
      let given = 0;
      use.forEach((x, i) => { per[i] = i === use.length - 1 ? total - given : Math.round((total * x.remaining) / base); given += per[i]; });
    }
    let out = per.map((v, i) => Math.min(Math.round(v), use[i].remaining));
    const tot = out.reduce((s, v) => s + v, 0);
    if (p.maxDiscount && tot > Number(p.maxDiscount)) out = out.map((v) => Math.round((v * Number(p.maxDiscount)) / tot));
    return out;
  };
  const usable = (p: CacheRule, fresh: boolean) => st.filter((x) => lineOk(p, x.l) && (fresh || (x.remaining > 0 && x.applied.every((a) => a.stackable && p.stackable
    && (!a.stackGroup || !p.stackGroup || a.stackGroup === p.stackGroup)))));
  const ranked = cache.promotions.filter(eligible).map((p) => ({ p, d: benefit(p, usable(p, true).map((x) => ({ ...x, remaining: x.gross }))).reduce((s, v) => s + v, 0) }))
    .filter((r) => r.d > 0).sort((a, b) => {
      if (cache.selection === 'best_price' && a.d !== b.d) return b.d - a.d;
      if (a.p.priority !== b.p.priority) return a.p.priority - b.p.priority;
      if (a.d !== b.d) return b.d - a.d;
      return String(a.p.code) < String(b.p.code) ? -1 : 1;
    });
  const applied: PosApplied[] = [];
  const ceiling = Number(cache.maxStackedPercent || 100);
  ranked.forEach(({ p }) => {
    const use = usable(p, false);
    const per = benefit(p, use).map((v, i) => (ceiling < 100 ? Math.max(Math.min(v, Math.round((use[i].gross * ceiling) / 100) - (use[i].gross - use[i].remaining)), 0) : v));
    const d = per.reduce((s, v) => s + v, 0);
    if (d <= 0) return;
    use.forEach((x, i) => { if (per[i] > 0) { x.remaining -= per[i]; x.applied.push(p); } });
    applied.push({ promotionId: String(p.id), code: String(p.code), name: String(p.name), discount: d, promoCode: entered.get(String(p.id)) });
  });
  return { discount: applied.reduce((s, a) => s + a.discount, 0), applied };
}

/** The promotions of a POS cart: server evaluation online, the shift cache offline. */
export function usePosPromotions(outletId: string | null, lines: PosLine[]): PosPromotionState {
  const [codes, setCodes] = useState<string[]>([]);
  const [exclusions, setExclusions] = useState<string[]>([]);
  const [online, setOnline] = useState(typeof navigator === 'undefined' ? true : navigator.onLine);
  const [server, setServer] = useState<{ discount: number; applied: PosApplied[]; rejected: string[] } | null>(null);
  const cacheQ = useGet<PromotionCache>(outletId && online ? `/api/v1/commercial/pos/promotions?outletId=${outletId}` : null, { staleTime: 15 * 60_000 });
  useEffect(() => {
    if (outletId && cacheQ.data) localStorage.setItem(CACHE_KEY(outletId), JSON.stringify(cacheQ.data));
  }, [outletId, cacheQ.data]);
  useEffect(() => {
    const on = () => setOnline(true), off = () => setOnline(false);
    window.addEventListener('online', on);
    window.addEventListener('offline', off);
    return () => { window.removeEventListener('online', on); window.removeEventListener('offline', off); };
  }, []);
  const key = JSON.stringify([lines, codes, exclusions]);
  useEffect(() => {
    if (!online || !outletId || lines.length === 0) { setServer(null); return; }
    let live = true;
    request<R>('POST', '/api/v1/commercial/promotions:evaluate', { channel: 'pos', businessLine: 'pos', outletId, promoCodes: codes, exclude: exclusions,
      lines: lines.map((l, i) => ({ key: String(i), productId: l.productId, quantity: String(l.quantity), unitPrice: String(l.unitPrice) })) })
      .then((r) => { if (live) setServer({ discount: Number(r.discount), rejected: ((r.rejected as R[]) ?? []).map((x) => `${String(x.code)}: ${String(x.reason)}`),
        applied: ((r.applied as R[]) ?? []).map((a) => ({ promotionId: String(a.promotionId), code: String(a.code), name: String(a.name), discount: Number(a.discount),
          promoCode: a.promoCode ? String(a.promoCode) : undefined })) }); })
      .catch(() => { if (live) setServer(null); });
    return () => { live = false; };
  }, [key, online, outletId]); // eslint-disable-line react-hooks/exhaustive-deps
  const local = useMemo(() => {
    const c = outletId ? readCache(outletId) : null;
    return c && lines.length ? evaluateOffline(c, outletId as string, lines, codes, exclusions) : { discount: 0, applied: [] as PosApplied[] };
  }, [key, outletId]); // eslint-disable-line react-hooks/exhaustive-deps
  const res = online && server ? server : { ...local, rejected: [] as string[] };
  return {
    discount: res.discount, applied: res.applied, rejected: res.rejected, offline: !(online && server), codes, exclusions,
    addCode: (c) => { const v = c.trim().toUpperCase(); if (v && !codes.includes(v)) setCodes([...codes, v]); },
    removeCode: (c) => setCodes(codes.filter((x) => x !== c)),
    exclude: (id) => setExclusions([...exclusions, id]),
    restore: (id) => setExclusions(exclusions.filter((x) => x !== id)),
    orderFields: (total) => ({ promoCodes: codes.length ? codes : undefined, promotionExclusions: exclusions.length ? exclusions : undefined,
      clientTotal: String(Math.max(total - res.discount, 0)), clientCreatedAt: new Date().toISOString() }),
  };
}

/** Apply Promotion panel of the POS: promotions of the cart, promo codes, removal with permission. */
export function PosPromotionPanel({ promo }: { promo: PosPromotionState }) {
  const { can } = useAuth();
  const toast = useToast();
  const [code, setCode] = useState('');
  const removable = can('commercial.pos.promotion_override');
  return (
    <div className="oc-stack" aria-label="Promotions">
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        {can('commercial.pos.promotion') && <>
          <div style={{ width: 200 }}><TextField label="Promo code" value={code} onChange={setCode} /></div>
          <button className="oc-btn oc-btn-neutral" style={{ alignSelf: 'flex-end' }} disabled={!code} onClick={() => { promo.addCode(code); setCode(''); }}>Apply Promotion</button>
        </>}
        {promo.codes.map((c) => <button key={c} className="oc-chip" onClick={() => promo.removeCode(c)} aria-label={`Remove code ${c}`}>{c} ×</button>)}
        {promo.offline && <span className="oc-small oc-muted">Offline prices (promotion cache)</span>}
      </div>
      {promo.applied.map((a) => (
        <div key={a.promotionId} className="oc-row oc-small">
          <StatusPill status="active" label={a.code} /><span>{a.name}{a.promoCode ? ` · ${a.promoCode}` : ''}</span><span className="oc-spacer" />
          <strong>− {money(a.discount)}</strong>
          {removable && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => { promo.exclude(a.promotionId); toast(`${a.code} removed from this sale`); }}>Remove</button>}
        </div>
      ))}
      {promo.exclusions.map((x) => (
        <div key={x} className="oc-row oc-small oc-muted"><span>A promotion was removed</span><span className="oc-spacer" />
          <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => promo.restore(x)}>Restore</button></div>
      ))}
      {promo.rejected.map((r) => <div key={r} className="oc-small oc-muted" role="status">{r}</div>)}
    </div>
  );
}

/** Back Office routes of the area. */
export const COMMERCIAL_P3_ROUTES: AreaRoute[] = [
  { path: 'commercial/promotions', perm: 'commercial.promotion.view', element: <PromotionsPage /> },
  { path: 'commercial/pricing/discounts', perm: 'commercial.promotion.view', element: <DiscountsPage /> },
  { path: 'commercial/pricing/promo-codes', perm: 'commercial.promo_code.view', element: <PromoCodesPage /> },
  { path: 'commercial/packages', perm: 'commercial.package.view', element: <PackagesPage /> },
  { path: 'commercial/package-bookings', perm: 'commercial.package_booking.view', element: <PackageBookingsPage /> },
];

/** Ops workstation tiles and routes of the area. */
export const COMMERCIAL_P3_OPS_TILES: OpsTile[] = [['card_travel', 'Package Use', '/ops/packages', 'commercial.package_booking.consume']];
export const COMMERCIAL_P3_OPS_ROUTES: OpsRoute[] = [{ path: 'packages', element: <PackageUsePage /> }];
