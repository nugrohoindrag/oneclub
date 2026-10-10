import React, { useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import {
  Card, Checkbox, DataTable, Empty, ErrorAlert, Icon, Modal, MoneyField, PageHeader, SearchBox, SelectField, Skeleton, StatusPill, TextArea, TextField,
  useAuth, useDebounced, useToast,
} from '@oneclub/shell';
import { KV, Tabs } from '../p1/common';
import {
  CheckInModal, INV, SOURCES, StayDrawer, addDays, label, money, sourceLabel, todayISO, useStayColumns, type Row, type Stay, type StayResult,
} from './shared';
import './accommodation.css';

// Reservations (requirements §4) and the booking flow (§5): search
// availability → room type → rate → guest → add-ons → price summary →
// payment → confirmation. The availability is validated again when the
// reservation is made (the database refuses overlapping stays).

const TABS = [['all', 'All Reservations'], ['confirmed', 'Confirmed'], ['awaiting_payment', 'Awaiting Payment'], ['pending_payment', 'Unpaid'], ['checked_in', 'In-house'],
  ['checked_out', 'Checked-out'], ['cancelled', 'Cancelled'], ['no_show', 'No-show'], ['expired', 'Expired'], ['void', 'Void'], ['requested', 'Requested']];

export function ReservationsPage({ ops }: { ops?: boolean }) {
  const [params, setParams] = useSearchParams();
  const { can } = useAuth();
  const tab = params.get('tab') ?? 'all';
  const open = params.get('id');
  const [q, setQ] = useState('');
  const [typeId, setType] = useState('');
  const [source, setSource] = useState('');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const query = useDebounced(q);
  const types = useGet<Page<Row>>('/api/v1/stay/bungalow-types?limit=100');
  const list = useGet<Page<Stay>>(`/api/v1/stay/reservations${qs({ tab, q: query, typeId, source, from, to, limit: 200 })}`);
  const cols = useStayColumns();
  const set = (k: string, v: string | null) => { const n = new URLSearchParams(params); if (v) n.set(k, v); else n.delete(k); setParams(n, { replace: true }); };
  return (
    <div className="oc-stack">
      <PageHeader title="Reservations" help="Bungalow reservations by status. Open a reservation for its detail, folio, requests and history."
        actions={can('stay.stay.create') ? <Link className="oc-btn oc-btn-primary" to={ops ? '/ops/stay-desk/new' : '/accommodation/reservations/new'}><Icon name="add" size={18} /> New reservation</Link> : undefined} />
      <Tabs value={tab} onChange={(v) => set('tab', v === 'all' ? null : v)} tabs={TABS.map(([value, l]) => ({ value, label: l }))} />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <SearchBox value={q} onChange={setQ} placeholder="Reservation, guest, company, bungalow" />
        <SelectField label="Room type" value={typeId} onChange={setType} placeholder="All" options={(types.data?.items ?? []).map((t) => ({ value: String(t.id), label: String(t.name) }))} />
        <SelectField label="Source" value={source} onChange={setSource} placeholder="All" options={SOURCES.map(([value, l]) => ({ value, label: l }))} />
        <TextField label="Stay from" type="date" value={from} onChange={setFrom} />
        <TextField label="to" type="date" value={to} onChange={setTo} />
      </div>
      <div className="oc-card">
        <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} rowKey={(s) => s.id} columns={cols} onRowClick={(s) => set('id', s.id)}
          empty={<Empty title="No reservations" icon="hotel" />} />
      </div>
      {open && <StayDrawer id={open} onClose={() => set('id', null)} />}
    </div>
  );
}

// ── booking flow ──────────────────────────────────────────────────────────

interface Rate { code: string; name: string; kind: string; total: string; averagePerNight: string; discount: string; promotionName: string | null;
  includesBreakfast: boolean; nonRefundable: boolean; freeCancelHours: number; paymentPolicy: string; error?: string | null }
interface SearchType { typeId: string; code: string; name: string; description: string | null; maxAdults: number; maxChildren: number; facilities: string[];
  photos: string[]; units: number; available: number; freeUnits: string[]; fitsGuests: boolean; rates: Rate[] }
interface Customer { id: string; name: string; phone?: string; email?: string; code?: string }

const STEPS = ['Search', 'Room & rate', 'Guest', 'Add-ons', 'Summary', 'Payment', 'Confirmation'];

export function NewReservationPage({ ops }: { ops?: boolean }) {
  const list = ops ? '/ops/stay-desk/reservations' : '/accommodation/reservations';
  const nav = useNavigate();
  const toast = useToast();
  const { can } = useAuth();
  const [step, setStep] = useState(0);
  const [s, setS] = useState({ arrival: todayISO(), departure: todayISO(1), adults: '2', children: '0', promoCode: '', corporateAccountId: '', source: 'front_desk' });
  const [pick, setPick] = useState<{ type: SearchType; rate: Rate } | null>(null);
  const [cust, setCust] = useState<Customer | null>(null);
  const [guest, setGuest] = useState({ name: '', phone: '', email: '' });
  const [extra, setExtra] = useState({ specialRequests: '', notes: '', vip: false, expectedArrival: '' });
  const [addons, setAddons] = useState<Record<string, number>>({});
  const [pay, setPay] = useState({ mode: 'deposit', methodType: 'bank_transfer', reference: '', amount: '' });
  const [done, setDone] = useState<StayResult | null>(null);
  const [waitlist, setWaitlist] = useState<SearchType | null>(null);
  const [sup, setSup] = useState({ override: false, reason: '', discount: '' });
  const [checkIn, setCheckIn] = useState(false);
  const searchPath = step >= 1 ? `/api/v1/stay/search${qs({ arrival: s.arrival, departure: s.departure, adults: s.adults, children: s.children, promoCode: s.promoCode,
    corporateAccountId: s.corporateAccountId, customerId: cust?.id, source: s.source })}` : null;
  const search = useGet<Page<SearchType>>(searchPath);
  const corps = useGet<Page<Row>>('/api/v1/crm/corporate-accounts?limit=200');
  const addonList = useGet<Page<Row>>('/api/v1/stay/addons?limit=100&filter[status]=active');
  const body = () => ({
    kind: 'bungalow', bungalowTypeId: pick?.type.typeId, arrivalDate: s.arrival, departureDate: s.departure, adults: Number(s.adults) || 1, children: Number(s.children) || 0,
    ratePlan: pick?.rate.kind === 'rate_plan' || pick?.rate.kind === 'commercial' ? pick.rate.code : undefined, stayPackage: pick?.rate.kind === 'package' ? pick.rate.code : undefined,
    promoCode: s.promoCode || undefined, corporateAccountId: s.corporateAccountId || undefined, bookingSource: s.source,
    customerId: cust?.id, guest: cust ? undefined : { name: guest.name, phone: guest.phone || undefined, email: guest.email || undefined },
    addons: Object.entries(addons).filter(([, n]) => n > 0).map(([addonId, quantity]) => ({ addonId, quantity })),
    specialRequests: extra.specialRequests || undefined, notes: extra.notes || undefined, vip: extra.vip || undefined, expectedArrival: extra.expectedArrival || undefined,
    overrideRestrictions: sup.override || undefined, manualDiscount: sup.discount || undefined, supervisorReason: sup.override || sup.discount ? sup.reason : undefined,
  });
  const quote = useSend<Row, StayResult>('POST', '/api/v1/stay/stays:quote');
  const book = useSend<Row, StayResult>('POST', '/api/v1/stay/stays', INV);
  const nights = Math.max(1, Math.round((new Date(s.departure).getTime() - new Date(s.arrival).getTime()) / 86_400_000));
  const next = (n: number) => {
    if (n === 4) quote.mutate(body(), { onSuccess: () => setStep(4) });
    else setStep(n);
  };
  const confirm = () => {
    const total = Number(quote.data?.total ?? 0);
    const amount = pay.mode === 'none' ? 0 : pay.mode === 'full' ? total : Number(pay.amount || quote.data?.depositRequired || 0);
    book.mutate({ ...body(), payment: amount > 0 ? { kind: pay.mode === 'full' ? 'payment' : 'deposit', methodType: pay.methodType, amount: String(amount),
      reference: pay.reference || undefined } : undefined }, { onSuccess: (r) => { setDone(r); setStep(6); toast(`Reservation ${r.stay.stayNo} made`); } });
  };
  return (
    <div className="oc-stack">
      <PageHeader title="New reservation" help="Search availability, choose the room type and rate, the guest and the add-ons; the price is calculated before the reservation is confirmed."
        actions={<Link className="oc-btn oc-btn-neutral" to={list}>Reservations</Link>} />
      <ol className="acc-steps">{STEPS.map((l, i) => <li key={l} data-state={i < step ? 'done' : i === step ? 'current' : 'next'}>{i + 1}. {l}</li>)}</ol>

      {step === 0 && (
        <Card title="Search availability" icon="search">
          <div className="oc-form">
            <TextField label="Check-in" type="date" value={s.arrival} onChange={(x) => setS({ ...s, arrival: x, departure: x >= s.departure ? addDays(x, 1) : s.departure })} />
            <TextField label="Check-out" type="date" value={s.departure} onChange={(x) => setS({ ...s, departure: x })} />
            <TextField label="Adults" type="number" value={s.adults} onChange={(x) => setS({ ...s, adults: x })} />
            <TextField label="Children" type="number" value={s.children} onChange={(x) => setS({ ...s, children: x })} />
            <SelectField label="Booking source" value={s.source} onChange={(x) => setS({ ...s, source: x })} options={SOURCES.map(([value, l]) => ({ value, label: l }))} />
            <SelectField label="Corporate account" value={s.corporateAccountId} onChange={(x) => setS({ ...s, corporateAccountId: x, source: x ? 'corporate' : s.source })} placeholder="None"
              options={(corps.data?.items ?? []).map((c) => ({ value: String(c.id), label: String(c.name) }))} />
            <TextField label="Promo code" value={s.promoCode} onChange={(x) => setS({ ...s, promoCode: x.toUpperCase() })} />
          </div>
          {can('stay.stay.supervise') && <Checkbox label="Pass a closed date (restriction) — supervisor" checked={sup.override} onChange={(x) => setSup({ ...sup, override: x })} />}
          {sup.override && <TextField label="Supervisor reason" value={sup.reason} onChange={(x) => setSup({ ...sup, reason: x })} />}
          <div className="oc-row" style={{ marginTop: 12 }}>
            <button className="oc-btn oc-btn-primary" disabled={s.departure <= s.arrival} onClick={() => next(1)}><Icon name="search" size={18} /> Search {nights} night(s)</button>
          </div>
        </Card>
      )}

      {step === 1 && (
        <div className="oc-stack">
          <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
            <span><strong>{formatDate(s.arrival)} → {formatDate(s.departure)}</strong> · {nights} night(s) · {s.adults} adult(s){Number(s.children) ? `, ${s.children} child(ren)` : ''}</span>
            <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setStep(0)}>Change</button>
          </div>
          {search.isLoading && <Skeleton rows={6} />}
          <ErrorAlert error={search.error} />
          {(search.data?.items ?? []).map((t) => (
            <section key={t.typeId} className="oc-card acc-type">
              <div className="acc-type-head">
                {t.photos[0] ? <img src={t.photos[0]} alt="" className="acc-type-photo" /> : <div className="acc-type-photo"><Icon name="cottage" size={32} /></div>}
                <div style={{ flex: 1 }}>
                  <h3 style={{ margin: 0 }}>{t.name}</h3>
                  <div className="oc-small oc-muted">Up to {t.maxAdults} adults, {t.maxChildren} children · {t.facilities.join(', ')}</div>
                  {t.description && <p className="oc-small" style={{ margin: '4px 0 0' }}>{t.description}</p>}
                </div>
                <div style={{ textAlign: 'right' }}>
                  <strong className={t.available ? 'acc-avail' : 'acc-full'}>{t.available ? `${t.available} of ${t.units} available` : 'No availability'}</strong>
                  {!t.fitsGuests && <div className="oc-small acc-full">Too many guests for this type</div>}
                </div>
              </div>
              {t.available > 0 && t.fitsGuests ? (
                <div className="acc-rates">
                  {t.rates.map((r) => (
                    <button key={r.kind + r.code} type="button" className="acc-rate" disabled={!!r.error} title={r.error ?? undefined}
                      aria-pressed={pick?.type.typeId === t.typeId && pick.rate.code === r.code} onClick={() => { setPick({ type: t, rate: r }); setStep(2); }}>
                      <span><strong>{r.name}</strong>{r.kind === 'package' ? <span className="oc-small"> · package</span> : null}
                        <span className="oc-small oc-muted" style={{ display: 'block' }}>
                          {r.error ? r.error : [r.includesBreakfast ? 'Breakfast included' : 'Room only', r.nonRefundable ? 'Non-refundable' : `Free cancellation ${r.freeCancelHours} h before`,
                            label(r.paymentPolicy), r.promotionName ? `Promotion: ${r.promotionName}` : ''].filter(Boolean).join(' · ')}</span></span>
                      {!r.error && <span className="acc-rate-price"><strong>{money(r.total)}</strong><span className="oc-small oc-muted">{money(r.averagePerNight)} / night</span>
                        {Number(r.discount) > 0 && <span className="oc-small acc-avail">−{money(r.discount)}</span>}</span>}
                    </button>
                  ))}
                  {t.rates.length === 0 && <span className="oc-muted">No rate configured for this room type.</span>}
                </div>
              ) : (
                <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
                  <span className="oc-muted">{t.available === 0 ? 'Fully booked for these dates.' : ''}</span>
                  {t.available === 0 && can('stay.waitlist.manage') && <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => setWaitlist(t)}><Icon name="hourglass_top" size={16} /> Join waitlist</button>}
                </div>
              )}
            </section>
          ))}
          {search.data?.items.length === 0 && <Empty title="No room types" help="Create room types and bungalows first." icon="cottage" />}
        </div>
      )}

      {step === 2 && pick && (
        <Card title="Guest information" icon="person">
          <GuestPicker value={cust} onChange={setCust} />
          {!cust && (
            <div className="oc-form" style={{ marginTop: 8 }}>
              <TextField label="New guest — full name" value={guest.name} onChange={(x) => setGuest({ ...guest, name: x })} required />
              <TextField label="Phone" value={guest.phone} onChange={(x) => setGuest({ ...guest, phone: x })} />
              <TextField label="E-mail" value={guest.email} onChange={(x) => setGuest({ ...guest, email: x })} />
            </div>
          )}
          <div className="oc-form" style={{ marginTop: 8 }}>
            <TextField label="Expected arrival" type="time" value={extra.expectedArrival} onChange={(x) => setExtra({ ...extra, expectedArrival: x })} />
            <TextArea label="Special request" span rows={2} value={extra.specialRequests} onChange={(x) => setExtra({ ...extra, specialRequests: x })} />
            <TextArea label="Internal notes" span rows={2} value={extra.notes} onChange={(x) => setExtra({ ...extra, notes: x })} />
          </div>
          <Checkbox label="VIP / priority guest" checked={extra.vip} onChange={(x) => setExtra({ ...extra, vip: x })} />
          <div className="oc-row" style={{ marginTop: 12 }}>
            <button className="oc-btn oc-btn-neutral" onClick={() => setStep(1)}>Back</button>
            <button className="oc-btn oc-btn-primary" disabled={!cust && !guest.name} onClick={() => next(3)}>Next: add-ons</button>
          </div>
        </Card>
      )}

      {step === 3 && pick && (
        <Card title="Add-ons" icon="add_shopping_cart">
          <div className="acc-addons">
            {(addonList.data?.items ?? []).filter((a) => a.availability !== 'in_stay').map((a) => {
              const id = String(a.id);
              return (
                <div key={id} className="acc-addon">
                  <div><strong>{String(a.name)}</strong><div className="oc-small oc-muted">{money(a.price)} {label(String(a.unit)).toLowerCase()}</div></div>
                  <div className="oc-row">
                    <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setAddons({ ...addons, [id]: Math.max((addons[id] ?? 0) - 1, 0) })}>−</button>
                    <strong style={{ minWidth: 24, textAlign: 'center' }}>{addons[id] ?? 0}</strong>
                    <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setAddons({ ...addons, [id]: (addons[id] ?? 0) + 1 })}>+</button>
                  </div>
                </div>
              );
            })}
            {addonList.data?.items.length === 0 && <span className="oc-muted">No add-ons configured.</span>}
          </div>
          <div className="oc-row" style={{ marginTop: 12 }}>
            <button className="oc-btn oc-btn-neutral" onClick={() => setStep(2)}>Back</button>
            <button className="oc-btn oc-btn-primary" disabled={quote.isPending} onClick={() => next(4)}>Next: price summary</button>
          </div>
          <ErrorAlert error={quote.error} />
        </Card>
      )}

      {step === 4 && pick && quote.data && (
        <Card title="Price summary" icon="receipt_long">
          <KV items={[['Room type', pick.type.name], ['Rate', pick.rate.name], ['Bungalow', quote.data.stay.unitName], ['Stay', `${formatDate(s.arrival)} → ${formatDate(s.departure)} · ${nights} night(s)`],
            ['Guest', cust?.name ?? guest.name], ['Source', sourceLabel(s.source)]]} />
          <DataTable rows={(quote.data.folio?.lines ?? []) as Row[]} rowKey={(l) => String(l.id)} columns={[{ key: 'description', header: 'Charge' },
            { key: 'total', header: 'Total', align: 'right', render: (l) => money(l.total) }]} empty={<span className="oc-muted">The room is charged per night.</span>} />
          <KV items={[['Room nights + add-ons', <strong key="t">{money(quote.data.total)}</strong>], ['Promotion', quote.data.stay.promotionCode ? `${quote.data.stay.promotionCode} −${money(quote.data.stay.discount)}` : '—'],
            ['Deposit required', money(quote.data.depositRequired)], ['Tax & service', 'included per the rate plan']]} />
          {can('stay.stay.supervise') && (
            <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
              <MoneyField label="Manual discount on the room (supervisor)" value={sup.discount} onChange={(x) => setSup({ ...sup, discount: x })} />
              <TextField label="Reason" value={sup.reason} onChange={(x) => setSup({ ...sup, reason: x })} />
              <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!!sup.discount && !sup.reason} onClick={() => quote.mutate(body())}>Apply</button>
            </div>
          )}
          {quote.data.addons.filter((a) => a.included).length > 0 && <p className="oc-small">Included: {quote.data.addons.filter((a) => a.included).map((a) => a.name).join(', ')}</p>}
          <div className="oc-row" style={{ marginTop: 12 }}>
            <button className="oc-btn oc-btn-neutral" onClick={() => setStep(3)}>Back</button>
            <button className="oc-btn oc-btn-primary" onClick={() => { setPay({ ...pay, amount: quote.data!.depositRequired }); setStep(5); }}>Next: payment</button>
          </div>
        </Card>
      )}

      {step === 5 && quote.data && (
        <Card title="Payment" icon="payments">
          <div className="acc-chips">
            {[['deposit', `Deposit ${money(quote.data.depositRequired)}`], ['full', `Full payment ${money(quote.data.total)}`], ['none', 'Pay later (at the hotel)']].map(([k, l]) => (
              <button key={k} type="button" className="oc-chip" aria-pressed={pay.mode === k} onClick={() => setPay({ ...pay, mode: k })}>{l}</button>
            ))}
          </div>
          {pay.mode !== 'none' && (
            <div className="oc-form" style={{ marginTop: 8 }}>
              {pay.mode === 'deposit' && <MoneyField label="Deposit amount" value={pay.amount} onChange={(x) => setPay({ ...pay, amount: x })} />}
              <SelectField label="Method" value={pay.methodType} onChange={(x) => setPay({ ...pay, methodType: x })}
                options={['cash', 'card', 'bank_transfer', 'qris', 'virtual_account'].map((m) => ({ value: m, label: label(m) }))} />
              <TextField label="Reference" value={pay.reference} onChange={(x) => setPay({ ...pay, reference: x })} />
            </div>
          )}
          <p className="oc-small oc-muted">The reservation is confirmed when the deposit is paid (or none is required); availability is validated again now.</p>
          <div className="oc-row" style={{ marginTop: 12 }}>
            <button className="oc-btn oc-btn-neutral" onClick={() => setStep(4)}>Back</button>
            <button className="oc-btn oc-btn-ink" disabled={book.isPending} onClick={confirm}><Icon name="check" size={18} /> Confirm reservation</button>
          </div>
          <ErrorAlert error={book.error} />
        </Card>
      )}

      {step === 6 && done && (
        <Card title="Reservation confirmation" icon="task_alt">
          <KV items={[['Reservation', `${done.stay.stayNo} · ${done.stay.reservationCode}`], ['Status', <StatusPill key="s" status={done.reservation.status as string} />],
            ['Bungalow', `${done.stay.unitName} (${done.stay.typeName ?? ''})`], ['Stay', `${formatDate(done.stay.start)} → ${formatDate(done.stay.end)}`],
            ['Total', money(done.total)], ['Paid', money(done.stay.paid)]]} />
          <div className="oc-row-wrap" style={{ marginTop: 12 }}>
            {done.stay.status === 'reserved' && done.stay.start.slice(0, 10) <= todayISO() && can('stay.stay.check_in') && (
              <button className="oc-btn oc-btn-ink" onClick={() => setCheckIn(true)}><Icon name="login" size={18} /> Check in now (walk-in)</button>)}
            <button className="oc-btn oc-btn-primary" onClick={() => nav(`${list}?id=${done.stay.id}`)}>Open reservation</button>
            <button className="oc-btn oc-btn-neutral" onClick={() => { setDone(null); setPick(null); setCust(null); setGuest({ name: '', phone: '', email: '' }); setAddons({}); setStep(0); }}>New reservation</button>
          </div>
        </Card>
      )}
      {checkIn && done && <CheckInModal stay={done.stay} onClose={() => { setCheckIn(false); nav(`${list}?id=${done.stay.id}`); }} />}
      {waitlist && <WaitlistModal type={waitlist} search={s} customer={cust} onClose={() => setWaitlist(null)} />}
    </div>
  );
}

/** Find an existing customer (central guest record) or type a new guest. */
export function GuestPicker({ value, onChange }: { value: Customer | null; onChange: (c: Customer | null) => void }) {
  const [q, setQ] = useState('');
  const query = useDebounced(q);
  const res = useGet<Page<Customer>>(query.length >= 2 ? `/api/v1/crm/customers${qs({ q: query, limit: 8 })}` : null);
  if (value) {
    return (
      <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
        <Icon name="person" size={20} /><strong>{value.name}</strong><span className="oc-muted">{[value.phone, value.email].filter(Boolean).join(' · ')}</span>
        <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => onChange(null)}>Change</button>
      </div>
    );
  }
  return (
    <div className="oc-stack">
      <SearchBox value={q} onChange={setQ} placeholder="Find a guest or member (name, phone, e-mail)" />
      {(res.data?.items ?? []).length > 0 && (
        <div className="acc-chips">
          {res.data!.items.map((c) => <button key={c.id} type="button" className="oc-chip" onClick={() => onChange(c)}>{c.name}{c.phone ? ` · ${c.phone}` : ''}</button>)}
        </div>
      )}
    </div>
  );
}

function WaitlistModal({ type, search, customer, onClose }: { type: SearchType; search: { arrival: string; departure: string; adults: string; children: string; source: string };
  customer: Customer | null; onClose: () => void }) {
  const toast = useToast();
  const [g, setG] = useState({ name: customer?.name ?? '', phone: customer?.phone ?? '', email: customer?.email ?? '', priority: 'normal', notes: '' });
  const send = useSend<Row>('POST', '/api/v1/stay/waitlist', INV);
  const nights = Math.max(1, Math.round((new Date(search.departure).getTime() - new Date(search.arrival).getTime()) / 86_400_000));
  return (
    <Modal open onClose={onClose} title={`Join waitlist · ${type.name}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-ink" disabled={send.isPending || (!customer && !g.name)}
        onClick={() => send.mutate({ bungalowTypeId: type.typeId, arrivalDate: search.arrival, nights, adults: Number(search.adults) || 1, children: Number(search.children) || 0,
          customerId: customer?.id, guest: customer ? undefined : { name: g.name, phone: g.phone || undefined, email: g.email || undefined }, priority: g.priority,
          bookingSource: search.source, notes: g.notes || undefined }, { onSuccess: () => { toast('Added to the waitlist'); onClose(); } })}>Join waitlist</button></>}>
      <p className="oc-muted">{formatDate(search.arrival)} · {nights} night(s). The guest is notified when a bungalow of this type frees up.</p>
      <div className="oc-form">
        {!customer && <TextField label="Guest name" value={g.name} onChange={(x) => setG({ ...g, name: x })} />}
        {!customer && <TextField label="Phone" value={g.phone} onChange={(x) => setG({ ...g, phone: x })} />}
        {!customer && <TextField label="E-mail" value={g.email} onChange={(x) => setG({ ...g, email: x })} />}
        <SelectField label="Priority" value={g.priority} onChange={(x) => setG({ ...g, priority: x })} options={['low', 'normal', 'high', 'vip'].map((p) => ({ value: p, label: label(p) }))} />
        <TextArea label="Notes" span rows={2} value={g.notes} onChange={(x) => setG({ ...g, notes: x })} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}
