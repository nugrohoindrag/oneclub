import React, { useState } from 'react';
import { Link, useParams } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { uuidv7, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import {
  Card, Checkbox, DataTable, Empty, ErrorAlert, Icon, Modal, PageHeader, QRCode, SelectField, Skeleton, StatusPill, TextArea, TextField, useToast,
} from '@oneclub/shell';
import { PaymentPanel } from './journey/pay';
import { MyCourseHandicap } from './journey/course';

type Row = Record<string, unknown>;
const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);
const idem = () => ({ 'Idempotency-Key': uuidv7() });

// ── Membership services: annual fees, pause, card replacement (FR-APP-P2-07) ─

export function MembershipServicesPage() {
  const toast = useToast();
  const memberships = useGet<Page<Schemas['Active']>>('/api/v1/member/memberships');
  const fees = useGet<Page<Schemas['Fee']>>('/api/v1/member/membership-fees');
  const pay = useSend<Row, Schemas['Payment']>('POST', (b) => `/api/v1/member/membership-fees/${b.id}:pay-online`, ['/api/v1/member/membership-fees']);
  const doPause = useSend<Row>('POST', (b) => `/api/v1/member/memberships/${b.id}:pause`, ['/api/v1/member/memberships']);
  const replace = useSend<Row>('POST', '/api/v1/member/card:replace', ['/api/v1/member/membership']);
  const [pause, setPause] = useState<string | null>(null);
  const [form, setForm] = useState({ from: '', until: '', reason: '' });
  const [checkout, setCheckout] = useState<Schemas['Payment'] | null>(null);
  return (
    <div className="oc-stack">
      <PageHeader title="Fees & Requests" actions={<button className="oc-btn oc-btn-outline" onClick={() => {
        const reason = window.prompt('Reason for the card replacement (lost, damaged…)');
        if (reason) replace.mutate({ reason }, { onSuccess: () => toast('New card issued; the old card is blocked') });
      }}><Icon name="credit_card" size={18} /> Replace card</button>} />
      <ErrorAlert error={pay.error ?? doPause.error ?? replace.error} />
      <Card title="Annual Fees" icon="receipt_long">
        <DataTable rows={fees.data?.items as unknown as Row[]} loading={fees.isLoading} columns={[
          { key: 'feeType', header: 'Fee' }, { key: 'dueDate', header: 'Due', render: (r) => formatDate(String(r.dueDate)) },
          { key: 'amount', header: 'Amount', render: (r) => money(r.amount) }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]}
          actions={(r) => r.status === 'due' ? (
            <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => pay.mutate({ id: r.id, method: 'qris' }, { onSuccess: (c) => setCheckout(c) })}>Pay</button>
          ) : null} />
      </Card>
      {memberships.data?.items.filter((m) => m.role === 'principal').map((m) => (
        <Card key={m.membershipId} title={`${m.programName} · ${m.typeName}`} icon="card_membership" actions={<StatusPill status={m.status} />}>
          <div className="oc-row-wrap oc-small"><span>No. {m.memberNo}</span>{m.endsOn && <span>Valid until {formatDate(m.endsOn)}</span>}</div>
          {m.status === 'active' && <button className="oc-btn oc-btn-neutral" style={{ marginTop: 12 }} onClick={() => setPause(m.membershipId)}>Request pause</button>}
        </Card>
      ))}
      <Modal open={!!pause} onClose={() => setPause(null)} title="Request a pause" actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setPause(null)}>Cancel</button>
        <button className="oc-btn oc-btn-ink" onClick={() => doPause.mutate({ id: pause, ...form }, { onSuccess: () => { toast('Pause request sent for approval'); setPause(null); } })}>Send request</button>
      </>}>
        <div className="oc-form">
          <TextField label="From" type="date" value={form.from} onChange={(v) => setForm({ ...form, from: v })} />
          <TextField label="Until" type="date" value={form.until} onChange={(v) => setForm({ ...form, until: v })} />
          <TextArea label="Reason" value={form.reason} onChange={(v) => setForm({ ...form, reason: v })} span />
        </div>
      </Modal>
      <CheckoutModal checkout={checkout} onClose={() => setCheckout(null)} />
    </div>
  );
}

/** Online payment of a fee, invoice, event or package (journey PaymentPanel, sandbox gateway included). */
export function CheckoutModal({ checkout, onClose }: { checkout: Schemas['Payment'] | null; onClose: () => void }) {
  const qc = useQueryClient();
  if (!checkout) return null;
  return (
    <Modal open onClose={onClose} title="Online payment" actions={<button className="oc-btn oc-btn-ink" onClick={onClose}>Close</button>}>
      <PaymentPanel payment={checkout} onPaid={() => void qc.invalidateQueries()} />
    </Modal>
  );
}

// ── Voucher & Prepaid (FR-APP-P2-05) ──────────────────────────────────────

export function VouchersPage() {
  const vouchers = useGet<Page<Schemas['Voucher']>>('/api/v1/member/vouchers');
  const balances = useGet<Page<Schemas['PrepaidBalance']>>('/api/v1/member/prepaid-balances');
  const history = useGet<Page<Schemas['LedgerEntry']>>('/api/v1/member/voucher-history?limit=30');
  return (
    <div className="oc-stack">
      <PageHeader title="Voucher & Prepaid" />
      <div className="oc-grid">
        {balances.data?.items.map((b) => (
          <div key={b.typeCode} className="oc-card oc-card-ink">
            <div className="oc-small" style={{ opacity: 0.7 }}>Prepaid Balance</div>
            <h3 style={{ margin: '4px 0' }}>{b.typeName}</h3>
            <div className="oc-metric">{formatNumber(Number(b.remaining))} {b.unit}</div>
            {b.nextExpiry && <div className="oc-small">Expires {formatDate(b.nextExpiry)}</div>}
          </div>
        ))}
      </div>
      <Card title="My Vouchers" icon="redeem">
        <div className="oc-grid">
          {vouchers.data?.items.map((v) => (
            <div key={v.id} className="oc-card">
              <div className="oc-row"><strong>{v.typeName}</strong><span className="oc-spacer" /><StatusPill status={v.status} /></div>
              <QRCode value={v.code} size={140} label={`Voucher ${v.code}`} />
              <div className="oc-code">{v.code}</div>
              <div className="oc-small">{formatNumber(Number(v.remainingQuantity))} {v.unit} left{v.expiresAt ? ` · until ${formatDate(v.expiresAt)}` : ''}</div>
            </div>
          ))}
          {vouchers.data?.items.length === 0 && <Empty title="No vouchers yet" icon="redeem" />}
        </div>
      </Card>
      <Card title="Voucher History & Redemption History" icon="history">
        <DataTable rows={history.data?.items as unknown as Row[]} columns={[
          { key: 'createdAt', header: 'When', render: (r) => formatDateTime(String(r.createdAt)) }, { key: 'voucherCode', header: 'Voucher' },
          { key: 'entryType', header: 'Entry' }, { key: 'quantity', header: 'Quantity' }, { key: 'balanceAfter', header: 'Balance' }]} />
      </Card>
    </div>
  );
}

// ── Golf: scores, handicap, Hall of Fame (FR-APP-P2-02/08) ─────────────────

export function ScoresPage() {
  const stats = useGet<Schemas['RoundStats']>('/api/v1/member/golf/stats');
  const hof = useGet<Schemas['MyHallOfFame']>('/api/v1/member/golf/hall-of-fame');
  const consent = useSend<Row>('POST', (b) => `/api/v1/member/golf/hall-of-fame/${b.id}:consent`, ['/api/v1/member/golf/hall-of-fame']);
  const s = stats.data;
  return (
    <div className="oc-stack">
      <PageHeader title="Scores & Handicap" />
      <div className="oc-grid">
        <div className="oc-card oc-card-ink"><div className="oc-small" style={{ opacity: 0.7 }}>Handicap Index</div>
          <div className="oc-metric">{s?.officialHandicap ?? s?.handicapIndex ?? '—'}</div><div className="oc-small">{s?.officialHandicap ? 'Official' : 'Local (WHS)'}</div></div>
        <div className="oc-card"><div className="oc-small oc-muted">Rounds</div><div className="oc-metric">{s?.rounds ?? '—'}</div></div>
        <div className="oc-card"><div className="oc-small oc-muted">Average gross</div><div className="oc-metric">{s?.averageGross ?? '—'}</div></div>
        <div className="oc-card"><div className="oc-small oc-muted">Birdies or better</div><div className="oc-metric">{s?.birdiesOrBetter ?? '—'}</div></div>
      </div>
      <MyCourseHandicap />
      <Card title="Round History" icon="golf_course">
        <DataTable rows={s?.history as unknown as Row[]} loading={stats.isLoading} columns={[
          { key: 'playedOn', header: 'Date', render: (r) => formatDate(String(r.playedOn)) }, { key: 'playingRouteName', header: 'Route' },
          { key: 'gross', header: 'Gross' }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]}
          actions={(r) => <Link className="oc-btn oc-btn-outline oc-btn-sm" to={`/golf/scores/${String(r.id)}`}>Scorecard</Link>} />
        {s?.history?.length === 0 && <Empty title="No rounds yet" icon="golf_course" />}
      </Card>
      <Card title="Hall of Fame" icon="emoji_events">
        <ErrorAlert error={consent.error} />
        {hof.data?.mine.map((e) => (
          <div key={e.id} className="oc-row">
            <strong>{e.title}</strong><span className="oc-spacer" />
            <Checkbox label="Show publicly" checked={e.consent === 'granted'} onChange={(v) => consent.mutate({ id: e.id, consent: v ? 'granted' : 'withdrawn' })} />
          </div>
        ))}
        <div className="oc-stack" style={{ marginTop: 8 }}>
          {hof.data?.public.slice(0, 10).map((e) => <div key={e.id} className="oc-small">{e.year ?? ''} · {e.title} — {e.playerName}</div>)}
        </div>
      </Card>
    </div>
  );
}

export function ScorecardPage() {
  const { id } = useParams();
  const toast = useToast();
  const path = `/api/v1/member/golf/scorecards/${id}`;
  const card = useGet<Schemas['Scorecard']>(path);
  const [draft, setDraft] = useState<Record<number, string>>({});
  const save = useSend<Row, Schemas['Scorecard']>('POST', `${path}/scores`, [path]);
  const submit = useSend<Row>('POST', `${path}:submit`, [path]);
  const c = card.data;
  if (!c) return <Skeleton rows={8} />;
  const locked = c.status === 'finalized';
  return (
    <div className="oc-stack">
      <PageHeader title={`Scorecard · ${formatDate(c.playedOn)}`} help={`${c.playingRouteName ?? ''} · ${c.teeSetName ?? ''} · Par ${c.par}${c.gross ? ` · Gross ${c.gross}` : ''}`}
        actions={<StatusPill status={c.status} />} />
      <ErrorAlert error={save.error ?? submit.error} />
      <div className="oc-card" style={{ overflowX: 'auto' }}>
        <table className="oc-table">
          <thead><tr><th>Hole</th>{c.scores.map((h) => <th key={h.seq}>{h.sectionCode.slice(-1)}{h.holeNumber}</th>)}</tr></thead>
          <tbody>
            <tr><td>Par</td>{c.scores.map((h) => <td key={h.seq}>{h.par}</td>)}</tr>
            <tr><td>SI</td>{c.scores.map((h) => <td key={h.seq}>{h.strokeIndex}</td>)}</tr>
            <tr><td>Score</td>{c.scores.map((h) => (
              <td key={h.seq}>{locked ? (h.strokes ?? '—') : (
                <input className="oc-input" style={{ width: 44, textAlign: 'center' }} inputMode="numeric" aria-label={`Hole ${h.seq} strokes`}
                  value={draft[h.seq] ?? (h.strokes ? String(h.strokes) : '')} onChange={(e) => setDraft({ ...draft, [h.seq]: e.target.value })} />
              )}</td>
            ))}</tr>
            <tr><td></td>{c.scores.map((h) => <td key={h.seq} className="oc-small">{h.term?.replace(/_/g, ' ') ?? ''}</td>)}</tr>
          </tbody>
        </table>
      </div>
      {!locked && (
        <div className="oc-row">
          <button className="oc-btn oc-btn-outline" onClick={() => save.mutate({ source: 'player', entries: Object.entries(draft).filter(([, v]) => v).map(([seq, v]) => ({ seq: Number(seq), strokes: Number(v), clientAt: new Date().toISOString() })) },
            { onSuccess: () => { setDraft({}); toast('Scores saved'); } })}>Save scores</button>
          <button className="oc-btn oc-btn-ink" onClick={() => submit.mutate({}, { onSuccess: () => toast('Submitted for finalization') })}>Submit</button>
        </div>
      )}
    </div>
  );
}

// ── Sport Club (FR-APP-P2-03) ─────────────────────────────────────────────

export function SportClubPage() {
  const toast = useToast();
  const sessions = useGet<Page<Schemas['SportclubSession']>>('/api/v1/member/sport-club/class-sessions?days=14');
  const classes = useGet<Page<Schemas['Enrollment']>>('/api/v1/member/sport-club/classes');
  const mine = useGet<Page<Schemas['SessionBooking']>>('/api/v1/member/sport-club/sessions');
  const book = useSend<Row>('POST', '/api/v1/member/sport-club/session-bookings', ['/api/v1/member/sport-club/']);
  return (
    <div className="oc-stack">
      <PageHeader title="Sport Club" help="Show your Digital Member Card QR at the gate for Facility Access." />
      <ErrorAlert error={book.error} />
      <Card title="My Classes" icon="school">
        <DataTable rows={classes.data?.items as unknown as Row[]} columns={[{ key: 'programName', header: 'Class' },
          { key: 'validUntil', header: 'Valid until', render: (r) => (r.validUntil ? formatDate(String(r.validUntil)) : '—') },
          { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
      </Card>
      <Card title="Book Class" icon="event">
        <DataTable rows={sessions.data?.items as unknown as Row[]} columns={[
          { key: 'start', header: 'When', render: (r) => formatDateTime(String(r.start)) }, { key: 'programName', header: 'Class' },
          { key: 'instructorName', header: 'Instructor' }, { key: 'booked', header: 'Booked', render: (r) => `${String(r.booked)}/${String(r.capacity)}` }]}
          actions={(r) => <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => book.mutate({ sessionId: r.id }, { onSuccess: () => toast('Session booked') })}>Book</button>} />
      </Card>
      <Card title="My Sessions" icon="event_available">
        <DataTable rows={mine.data?.items as unknown as Row[]} columns={[{ key: 'sessionId', header: 'Session', render: (r) => String(r.sessionId).slice(-6) },
          { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }, { key: 'quotaUsed', header: 'Quota used', render: (r) => (r.quotaUsed ? 'Yes' : 'No') }]} />
      </Card>
    </div>
  );
}

// ── Order Food (FR-APP-P2-06) ─────────────────────────────────────────────

export function OrderFoodPage() {
  const toast = useToast();
  const [outlet, setOutlet] = useState('');
  const outlets = useGet<Page<Schemas['MyOutlet']>>('/api/v1/member/outlets');
  const menu = useGet<Page<Schemas['MenuItem']>>(outlet ? `/api/v1/member/outlets/${outlet}/menu` : null);
  const orders = useGet<Page<Schemas['Order']>>('/api/v1/member/orders', { refetchInterval: 10000 });
  const [cart, setCart] = useState<Record<string, number>>({});
  const place = useSend<Row, Schemas['Order']>('POST', '/api/v1/member/orders', ['/api/v1/member/orders'], idem);
  return (
    <div className="oc-stack">
      <PageHeader title="Order Food" help="Pre-order to pick up, or have it delivered on course." />
      <div style={{ width: 320 }}><SelectField label="Outlet" value={outlet} onChange={setOutlet} placeholder="Choose outlet"
        options={(outlets.data?.items ?? []).map((o) => ({ value: o.id, label: o.name }))} /></div>
      <ErrorAlert error={place.error ?? menu.error} />
      {menu.data && (
        <Card title="Menu" icon="restaurant_menu">
          <div className="oc-stack">
            {menu.data.items.map((p) => (
              <div key={p.productId} className="oc-row"><span>{p.name}</span><span className="oc-small oc-muted">{money(p.memberPrice ?? p.price)}</span><span className="oc-spacer" />
                <button className="oc-icon-btn" aria-label={`Remove ${p.name}`} onClick={() => setCart({ ...cart, [p.productId]: Math.max(0, (cart[p.productId] ?? 0) - 1) })}><Icon name="remove" size={18} /></button>
                <strong>{cart[p.productId] ?? 0}</strong>
                <button className="oc-icon-btn" aria-label={`Add ${p.name}`} onClick={() => setCart({ ...cart, [p.productId]: (cart[p.productId] ?? 0) + 1 })}><Icon name="add" size={18} /></button>
              </div>
            ))}
          </div>
          <button className="oc-btn oc-btn-ink" style={{ marginTop: 12 }} disabled={!Object.values(cart).some((n) => n > 0)}
            onClick={() => place.mutate({ outletId: outlet, memberCharge: true, lines: Object.entries(cart).filter(([, n]) => n > 0).map(([productId, n]) => ({ productId, quantity: String(n) })) },
              { onSuccess: (o) => { setCart({}); toast(`Order ${o.orderNo} sent to the kitchen`); } })}>Order (member charge)</button>
        </Card>
      )}
      <Card title="My orders" icon="receipt">
        <DataTable rows={orders.data?.items as unknown as Row[]} columns={[{ key: 'orderNo', header: 'Order' }, { key: 'outletName', header: 'Outlet' },
          { key: 'total', header: 'Total', render: (r) => money(r.total) }, { key: 'serviceStatus', header: 'Kitchen', render: (r) => <StatusPill status={String(r.serviceStatus)} /> }]} />
      </Card>
    </div>
  );
}

// ── Preferences & consent (FR-APP-P2-09) ──────────────────────────────────

export function PreferencesPage() {
  const toast = useToast();
  const p = useGet<Schemas['MyPreferences']>('/api/v1/member/preferences', { retry: false });
  const add = useSend<Row>('POST', '/api/v1/member/preferences', ['/api/v1/member/preferences']);
  const remove = useSend<Row>('POST', (b) => `/api/v1/member/preferences/${b.id}:remove`, ['/api/v1/member/preferences']);
  const consent = useSend<Row>('POST', '/api/v1/member/consent', ['/api/v1/member/preferences']);
  const [cat, setCat] = useState('beverage');
  const [val, setVal] = useState('');
  if (!p.data) return p.isLoading ? <Skeleton rows={4} /> : <ErrorAlert error={p.error} />;
  return (
    <div className="oc-stack">
      <PageHeader title="Preferences" />
      <Card title="Preferences & consent" icon="tune">
        <ErrorAlert error={add.error ?? remove.error ?? consent.error} />
        <div className="oc-stack">
          <Checkbox label="Allow personalisation from my activity (profiling)" checked={Boolean(p.data.profile.consentProfiling)}
            onChange={(v) => consent.mutate({ profiling: v }, { onSuccess: () => toast('Saved') })} />
          <Checkbox label="Receive offers and news" checked={Boolean(p.data.profile.marketingOptIn)}
            onChange={(v) => consent.mutate({ marketing: v }, { onSuccess: () => toast('Saved') })} />
          {p.data.preferences.map((x) => (
            <div key={x.id} className="oc-row"><StatusPill status={x.category} label={x.category.replace(/_/g, ' ')} /><span>{x.value}</span><span className="oc-spacer" />
              <button className="oc-icon-btn" aria-label="Remove" onClick={() => remove.mutate({ id: x.id })}><Icon name="close" size={18} /></button></div>
          ))}
          <div className="oc-row-wrap">
            <div style={{ width: 180 }}><SelectField label="Category" value={cat} onChange={setCat}
              options={['beverage', 'food', 'diet', 'allergy', 'tee_time', 'facility', 'note'].map((c) => ({ value: c, label: c.replace(/_/g, ' ') }))} /></div>
            <div style={{ flex: 1, minWidth: 200 }}><TextField label="Preference" value={val} onChange={setVal} /></div>
            <button className="oc-btn oc-btn-ink" style={{ alignSelf: 'flex-end' }} disabled={!val.trim()}
              onClick={() => add.mutate({ category: cat, value: val }, { onSuccess: () => setVal('') })}>Add</button>
          </div>
        </div>
      </Card>
    </div>
  );
}

// ── routes (added to P1's member portal in main.tsx) ─────────────────────

/** Member Portal routes of P2, at the paths of the server navigation. */
export const P2_MEMBER_ROUTES = [
  { path: 'golf/scores', element: <ScoresPage /> },
  { path: 'golf/scores/:id', element: <ScorecardPage /> },
  { path: 'sport-club', element: <SportClubPage /> },
  { path: 'vouchers', element: <VouchersPage /> },
  { path: 'membership/services', element: <MembershipServicesPage /> },
  { path: 'order-food', element: <OrderFoodPage /> },
  { path: 'preferences', element: <PreferencesPage /> },
];
