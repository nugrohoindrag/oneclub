import React, { useState } from 'react';
import { useSearchParams } from 'react-router';
import { qs, useGet, type Page } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import { AutoResourcePage, DataTable, Empty, ErrorAlert, PageHeader, Skeleton, TextField } from '@oneclub/shell';
import { Tabs } from '../p1/common';
import { addDays, label, money, todayISO, type Row } from './shared';
import './accommodation.css';

// Rates & Packages (requirements §11, §12, §13, §29, §31): rate plans
// (standard, weekend, peak, holiday, member, corporate, promotional) with
// their policies and prices per room type, seasons with priority and season
// prices, add-ons, stay packages, promotions and corporate terms — and a
// rate check that prices a stay with all of them.

const TABS: [string, string, string | null][] = [
  ['check', 'Rate check', null], ['plans', 'Rate Plans', 'stay.rate_plan'], ['plan-prices', 'Rate Plan Prices', 'stay.rate_plan_price'],
  ['seasons', 'Seasons', 'stay.season'], ['season-prices', 'Season Prices', 'stay.season_price'], ['addons', 'Add-ons', 'stay.addon'],
  ['packages', 'Packages', 'stay.package'], ['promotions', 'Promotions', 'stay.promotion'], ['corporate', 'Corporate', 'stay.corporate_term'],
  ['types', 'Room Types (base rates)', 'stay.bungalow_type'],
];

export function RatesPage() {
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'check';
  const key = TABS.find((t) => t[0] === tab)?.[2];
  return (
    <div className="oc-stack">
      <PageHeader title="Rates & Packages" help="The price of a night is the season price of the room type (highest priority season), else its base / weekend rate; a rate plan takes it as is, adjusts it or has its own prices. One promotion applies (the best, or the promo code)." />
      <Tabs value={tab} onChange={(v) => setParams({ tab: v }, { replace: true })} tabs={TABS.map(([value, l]) => ({ value, label: l }))} />
      {key ? <AutoResourcePage key={key} resourceKey={key} /> : <RateCheck />}
    </div>
  );
}

interface Rate { code: string; name: string; kind: string; total: string; averagePerNight: string; discount: string; promotionName: string | null; includesBreakfast: boolean;
  nonRefundable: boolean; freeCancelHours: number; paymentPolicy: string; error?: string | null }
interface SearchType { typeId: string; name: string; units: number; available: number; rates: Rate[] }

function RateCheck() {
  const [v, setV] = useState({ arrival: todayISO(7), departure: todayISO(9), adults: '2', promoCode: '' });
  const r = useGet<Page<SearchType>>(v.departure > v.arrival ? `/api/v1/stay/search${qs({ arrival: v.arrival, departure: v.departure, adults: v.adults, promoCode: v.promoCode })}` : null);
  const nights = Math.max(1, Math.round((new Date(v.departure).getTime() - new Date(v.arrival).getTime()) / 86_400_000));
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Check-in" type="date" value={v.arrival} onChange={(x) => setV({ ...v, arrival: x, departure: x >= v.departure ? addDays(x, 1) : v.departure })} />
        <TextField label="Check-out" type="date" value={v.departure} onChange={(x) => setV({ ...v, departure: x })} />
        <TextField label="Adults" type="number" value={v.adults} onChange={(x) => setV({ ...v, adults: x })} />
        <TextField label="Promo code" value={v.promoCode} onChange={(x) => setV({ ...v, promoCode: x.toUpperCase() })} />
        <span className="oc-muted">{formatDate(v.arrival)} → {formatDate(v.departure)} · {nights} night(s)</span>
      </div>
      <ErrorAlert error={r.error} />
      {r.isLoading && <Skeleton rows={6} />}
      {(r.data?.items ?? []).map((t) => (
        <section key={t.typeId} className="oc-card">
          <div className="oc-row-wrap" style={{ justifyContent: 'space-between' }}><h3 style={{ margin: 0 }}>{t.name}</h3><span className="oc-muted">{t.available} of {t.units} free</span></div>
          <DataTable rows={t.rates as unknown as Row[]} rowKey={(x) => `${x.kind}${x.code}`} empty={<Empty title="No rate" icon="sell" />} columns={[
            { key: 'name', header: 'Rate', render: (x) => <><strong>{String(x.name)}</strong> <span className="oc-small oc-muted">{label(String(x.kind))}</span></> },
            { key: 'total', header: 'Total', align: 'right', render: (x) => (x.error ? <span className="acc-full oc-small">{String(x.error)}</span> : <strong>{money(x.total)}</strong>) },
            { key: 'avg', header: 'Per night', align: 'right', render: (x) => (x.error ? '' : money(x.averagePerNight)) },
            { key: 'discount', header: 'Promotion', render: (x) => (Number(x.discount) > 0 ? `${x.promotionName ?? ''} −${money(x.discount)}` : '—') },
            { key: 'terms', header: 'Terms', render: (x) => (x.error ? '' : [x.includesBreakfast ? 'Breakfast' : 'Room only', x.nonRefundable ? 'Non-refundable' : `Free cancel ${x.freeCancelHours} h`,
              label(String(x.paymentPolicy))].join(' · ')) }]} />
        </section>
      ))}
    </div>
  );
}
