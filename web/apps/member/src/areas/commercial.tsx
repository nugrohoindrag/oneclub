import React, { useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import { Card, Checkbox, DataTable, Empty, ErrorAlert, Modal, PageHeader, SelectField, Skeleton, StatusPill, TextField } from '@oneclub/shell';
import { CheckoutModal } from '../p2';

// Member App — Offers & Packages (PRD P3 EP-10–11/19): Offers (promotions
// for my segment and my personal promo codes, FR-APP-P3-02) and Packages
// (buy a cross-line package, My Packages, FR-APP-P3-05).

type Row = Record<string, unknown>;
const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');

function offerText(p: Row): string {
  switch (p.promoType) {
    case 'buy_n_get_x': return `Buy ${String(p.buyQuantity)}, get ${String(p.getQuantity)}`;
    case 'buy_n_price_x': return `${String(p.buyQuantity)} for ${money(p.bundlePrice)}`;
    case 'bundle': return `Bundle for ${money(p.bundlePrice)}`;
    default: return p.discountPercent ? `${String(p.discountPercent)}% off` : p.discountAmount ? `${money(p.discountAmount)} off` : '';
  }
}

function hours(p: Row): string {
  const ws = (p.timeWindows as Row[] | undefined) ?? [];
  const day = (d: number) => ['', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'][d];
  return ws.map((w) => `${((w.days as number[] | undefined) ?? []).map(day).join(', ') || 'Daily'} ${String(w.start)}–${String(w.end)}`).join(' · ');
}

export function OffersPage() {
  const o = useGet<Row>('/api/v1/member/offers');
  const promos = (o.data?.promotions as Row[] | undefined) ?? [];
  const codes = (o.data?.promoCodes as Row[] | undefined) ?? [];
  const pkgs = (o.data?.packages as Row[] | undefined) ?? [];
  return (
    <div className="oc-stack">
      <PageHeader title="Offers" help="Promotions for you, your personal promo codes and packages." />
      {o.isLoading && <Skeleton />}
      <ErrorAlert error={o.error} />
      {codes.length > 0 && (
        <Card title="My promo codes" icon="confirmation_number">
          <DataTable rows={codes.map((c) => ({ ...c, id: String(c.code) })) as (Row & { id: string })[]} columns={[
            { key: 'code', header: 'Code', render: (r) => <strong className="oc-code">{String(r.code)}</strong> }, { key: 'promotionName', header: 'Offer' },
            { key: 'expiresAt', header: 'Valid until', render: (r) => (r.expiresAt ? formatDateTime(String(r.expiresAt)) : '—') },
            { key: 'uses', header: 'Uses', render: (r) => (r.maxUses ? `${String(r.usedCount)} / ${String(r.maxUses)}` : String(r.usedCount)) }]} />
        </Card>
      )}
      <div className="oc-grid">
        {promos.map((p) => (
          <Card key={String(p.id)} title={String(p.name)} icon="local_offer">
            <div className="oc-metric">{offerText(p)}</div>
            {p.description ? <p>{String(p.description)}</p> : null}
            <p className="oc-small oc-muted">{[hours(p), p.validTo ? `until ${formatDate(String(p.validTo))}` : '', p.minPurchase ? `min. ${money(p.minPurchase)}` : '']
              .filter(Boolean).join(' · ')}</p>
          </Card>
        ))}
        {pkgs.map((p) => (
          <Card key={String(p.id)} title={String(p.name)} icon="card_travel">
            <div className="oc-metric">{money(p.price)}<span className="oc-small"> {p.pricingMode === 'fixed' ? '' : `/ ${label(p.pricingMode).replace('per ', '')}`}</span></div>
            <Link className="oc-btn oc-btn-primary oc-btn-sm" to={`/packages?code=${String(p.code)}`}>Book</Link>
          </Card>
        ))}
      </div>
      {!o.isLoading && promos.length === 0 && codes.length === 0 && pkgs.length === 0 && <Empty title="No offers right now" icon="local_offer" />}
    </div>
  );
}

export function PackagesPage() {
  const o = useGet<Row>('/api/v1/member/offers');
  const mine = useGet<Page<Row & { id: string }>>('/api/v1/member/package-bookings');
  const [params, setParams] = useSearchParams();
  const code = params.get('code') ?? '';
  const setCode = (c: string) => setParams(c ? { code: c } : {});
  const pkgs = (o.data?.packages as Row[] | undefined) ?? [];
  return (
    <div className="oc-stack">
      <PageHeader title="Packages" help="Golf, stay and dining in one booking — every part is reserved together." />
      {o.isLoading && <Skeleton />}
      <div className="oc-grid">
        {pkgs.map((p) => (
          <Card key={String(p.id)} title={String(p.name)} icon="card_travel">
            {p.description ? <p>{String(p.description)}</p> : null}
            <ul className="oc-small">{((p.components as Row[] | undefined) ?? []).map((c, i) => (
              <li key={i}>{String(c.name)}{c.perPax ? ' (per person)' : ''}{Number(c.dayOffset) > 0 ? ` — day ${Number(c.dayOffset) + 1}` : ''}</li>
            ))}</ul>
            <div className="oc-row"><strong>{money(p.price)}</strong><span className="oc-small oc-muted">{p.pricingMode === 'fixed' ? '' : label(p.pricingMode)}
              {p.taxMode === 'plus_plus' ? ' ++' : ' nett'}</span><span className="oc-spacer" />
              <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => setCode(String(p.code))}>Book</button></div>
          </Card>
        ))}
      </div>
      <Card title="My Packages" icon="event_available">
        <DataTable rows={mine.data?.items} loading={mine.isLoading} columns={[{ key: 'number', header: 'Booking' }, { key: 'packageName', header: 'Package' },
          { key: 'startDate', header: 'Date', render: (r) => formatDate(String(r.startDate)) }, { key: 'pax', header: 'Pax' },
          { key: 'total', header: 'Total', render: (r) => money(r.total) }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
      </Card>
      {code && pkgs.find((p) => p.code === code) && <BookPackage pkg={pkgs.find((p) => p.code === code) as Row} onClose={() => setCode('')} />}
    </div>
  );
}

function BookPackage({ pkg, onClose }: { pkg: Row; onClose: () => void }) {
  const [date, setDate] = useState('');
  const [pax, setPax] = useState(String(pkg.minPax ?? 1));
  const [nights, setNights] = useState(pkg.nights ? String(pkg.nights) : '');
  const [addons, setAddons] = useState<string[]>([]);
  const [promo, setPromo] = useState('');
  const [method, setMethod] = useState('qris');
  const [checkout, setCheckout] = useState<Schemas['Payment'] | null>(null);
  const [done, setDone] = useState<Row | null>(null);
  const send = useSend<Row, Row>('POST', '/api/v1/member/package-bookings', ['/api/v1/member/package-bookings']);
  const book = () => send.mutate({ packageCode: pkg.code, startDate: date, pax: Number(pax) || undefined, nights: Number(nights) || undefined,
    addons: addons.length ? addons : undefined, promoCodes: promo ? [promo] : undefined, paymentMethod: method || undefined }, {
    onSuccess: (r) => {
      setDone(r.booking as Row);
      const online = (r.checkout as Row | null)?.online as Schemas['Payment'] | undefined;
      if (online) setCheckout(online);
    },
  });
  const b = done;
  return (
    <Modal open onClose={onClose} title={`Book ${String(pkg.name)}`} actions={b ? <button className="oc-btn oc-btn-ink" onClick={onClose}>Close</button> : <>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!date || send.isPending} onClick={book}>Book</button>
    </>}>
      {!b && (
        <div className="oc-form">
          <TextField label="Date" type="date" value={date} onChange={setDate} required />
          <TextField label="Persons" type="number" value={pax} onChange={setPax} help={`Minimum ${String(pkg.minPax ?? 1)}`} />
          {Number(pkg.nights) > 0 && <TextField label="Nights" type="number" value={nights} onChange={setNights} />}
          <TextField label="Promo code" value={promo} onChange={setPromo} />
          <SelectField label="Payment" value={method} onChange={setMethod} options={[{ value: 'qris', label: 'QRIS' }, { value: 'virtual_account', label: 'Virtual Account' },
            { value: 'card', label: 'Card' }, { value: 'member_account', label: 'Member account' }, { value: '', label: 'Pay later' }]} />
          {((pkg.addons as Row[] | undefined) ?? []).map((a) => (
            <Checkbox key={String(a.id)} label={`${String(a.name)} (+${money(a.price)}${a.perPax ? ' per person' : ''})`} checked={addons.includes(String(a.id))}
              onChange={(on) => setAddons(on ? [...addons, String(a.id)] : addons.filter((x) => x !== String(a.id)))} />
          ))}
        </div>
      )}
      <ErrorAlert error={send.error} />
      {b && (
        <div className="oc-stack">
          <div className="oc-row"><StatusPill status={String(b.status)} /><strong>{String(b.number)}</strong><span className="oc-spacer" /><strong>{money(b.total)}</strong></div>
          {Number(b.discountTotal) > 0 && <p className="oc-small">Promotion −{money(b.discountTotal)}</p>}
          {b.status === 'pending' && <p className="oc-small">Held until {formatDateTime(String(b.holdExpiresAt))} — complete the payment to confirm.</p>}
        </div>
      )}
      <CheckoutModal checkout={checkout} onClose={() => setCheckout(null)} />
    </Modal>
  );
}

/** Member App routes of the area. */
export const COMMERCIAL_MEMBER_ROUTES: { path: string; element: React.ReactNode }[] = [
  { path: 'offers', element: <OffersPage /> },
  { path: 'packages', element: <PackagesPage /> },
];
