import React, { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router';
import { qs, uuidv7, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import {
  Card, Checkbox, DataTable, Empty, ErrorAlert, Icon, Modal, PageHeader, QRCode, SelectField, Skeleton, StatusPill, TextArea, TextField, useToast,
} from '@oneclub/shell';

type Row = Record<string, unknown>;
const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);
const idem = () => ({ 'Idempotency-Key': uuidv7() });

// ── Digital Member Card (FR-APP-P2-01) — cached for offline use ───────────

const CARD_KEY = 'oneclub.member.card';

export function DigitalCard() {
  const live = useGet<Schemas['DigitalCard']>('/api/v1/me/card', { retry: false });
  const [cached, setCached] = useState<Schemas['DigitalCard'] | null>(() => {
    try {
      return JSON.parse(localStorage.getItem(CARD_KEY) ?? 'null');
    } catch {
      return null;
    }
  });
  useEffect(() => {
    if (live.data) {
      setCached(live.data);
      try {
        localStorage.setItem(CARD_KEY, JSON.stringify(live.data));
      } catch {
        /* storage unavailable */
      }
    }
  }, [live.data]);
  const c = live.data ?? cached;
  if (!c) return live.isLoading ? <Skeleton rows={4} /> : <Empty title="No Digital Member Card yet" help="Your card appears here once your membership is active." icon="badge" />;
  return (
    <div className="oc-card oc-card-ink" style={{ display: 'flex', gap: 20, flexWrap: 'wrap', alignItems: 'center' }}>
      <QRCode value={c.card.qrToken} size={180} label="Member card QR for facility access" />
      <div className="oc-stack" style={{ gap: 6 }}>
        <div className="oc-small" style={{ opacity: 0.7 }}>Digital Member Card</div>
        <div style={{ fontSize: 22, fontWeight: 600 }}>{c.card.customerName}</div>
        <div className="oc-code">{c.card.cardNo}</div>
        {c.memberships.map((m) => <div key={m.membershipId} className="oc-small">{m.programName} · {m.typeName} · until {formatDate(m.endDate)}</div>)}
        {!live.data && <div className="oc-small" style={{ opacity: 0.7 }}>Offline copy from {formatDateTime(c.issuedAt)}</div>}
      </div>
    </div>
  );
}

// ── Membership (FR-APP-P2-07) ─────────────────────────────────────────────

export function MembershipPage() {
  const toast = useToast();
  const list = useGet<Page<Schemas['Membership']>>('/api/v1/me/memberships');
  const [pause, setPause] = useState<string | null>(null);
  const [form, setForm] = useState({ from: '', until: '', reason: '' });
  const pay = useSend<Row, Schemas['OnlineCheckout']>('POST', (b) => `/api/v1/me/memberships/${b.id}:pay-fee`, ['/api/v1/me/memberships']);
  const renew = useSend<Row>('POST', (b) => `/api/v1/me/memberships/${b.id}:renew`, ['/api/v1/me/memberships']);
  const doPause = useSend<Row>('POST', (b) => `/api/v1/me/memberships/${b.id}:pause`, ['/api/v1/me/memberships']);
  const replace = useSend<Row>('POST', '/api/v1/me/card:replace', ['/api/v1/me/card']);
  const [checkout, setCheckout] = useState<Schemas['OnlineCheckout'] | null>(null);
  return (
    <div className="oc-stack">
      <PageHeader title="Membership" actions={<button className="oc-btn oc-btn-outline" onClick={() => {
        const reason = window.prompt('Reason for the card replacement (lost, damaged…)');
        if (reason) replace.mutate({ reason }, { onSuccess: () => toast('New card issued; the old card is blocked') });
      }}><Icon name="credit_card" size={18} /> Replace card</button>} />
      <DigitalCard />
      <ErrorAlert error={pay.error ?? renew.error ?? doPause.error ?? replace.error} />
      {list.data?.items.map((m) => (
        <Card key={m.id} title={`${m.programName} · ${m.typeName}`} icon="card_membership" actions={<StatusPill status={m.status} />}>
          <div className="oc-row-wrap oc-small">
            <span>No. {m.membershipNo}</span><span>Valid {formatDate(m.startDate)} – {formatDate(m.endDate)}</span>
            {m.nextFeeDue && <span>Next fee due {formatDate(m.nextFeeDue)}</span>}
          </div>
          {m.fees && m.fees.length > 0 && (
            <DataTable rows={m.fees as unknown as Row[]} columns={[
              { key: 'feeType', header: 'Fee' }, { key: 'dueDate', header: 'Due', render: (r) => formatDate(String(r.dueDate)) },
              { key: 'amount', header: 'Amount', render: (r) => money(r.amount) }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
          )}
          <div className="oc-row" style={{ marginTop: 12 }}>
            <button className="oc-btn oc-btn-primary" onClick={() => pay.mutate({ id: m.id, method: 'qris' }, { onSuccess: (c) => setCheckout(c) })}>Pay annual fee</button>
            <button className="oc-btn oc-btn-outline" onClick={() => renew.mutate({ id: m.id }, { onSuccess: () => toast('Membership renewed') })}>Renew</button>
            <button className="oc-btn oc-btn-neutral" onClick={() => setPause(m.id)}>Request pause</button>
          </div>
          {m.events && m.events.length > 0 && (
            <details style={{ marginTop: 8 }}><summary className="oc-small">Membership History</summary>
              <div className="oc-stack oc-small">{m.events.map((e, i) => <div key={`${e.createdAt}-${i}`}>{formatDate(e.createdAt)} · {e.eventType.replace(/_/g, ' ')}{e.reason ? ` — ${e.reason}` : ''}</div>)}</div>
            </details>
          )}
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

function CheckoutModal({ checkout, onClose }: { checkout: { id: string; status: string; qrString?: string | null; vaNumber?: string | null; checkoutUrl?: string | null; amount: string } | null; onClose: () => void }) {
  const st = useGet<Schemas['OnlinePayment']>(checkout ? `/api/v1/me/online-payments/${checkout.id}` : null, { refetchInterval: 4000 });
  if (!checkout) return null;
  const status = st.data?.status ?? checkout.status;
  return (
    <Modal open onClose={onClose} title="Online payment" actions={<button className="oc-btn oc-btn-ink" onClick={onClose}>Close</button>}>
      <div className="oc-stack" style={{ alignItems: 'center' }}>
        <div className="oc-metric">{money(checkout.amount)}</div>
        <StatusPill status={status} />
        {status === 'pending' && checkout.qrString && <QRCode value={checkout.qrString} size={220} label="QRIS payment code" />}
        {status === 'pending' && checkout.vaNumber && <div className="oc-code">VA {checkout.vaNumber}</div>}
        {status === 'pending' && checkout.checkoutUrl && <a className="oc-btn oc-btn-outline" href={checkout.checkoutUrl} target="_blank" rel="noreferrer">Open payment page</a>}
        {status === 'paid' && <div className="oc-alert oc-alert-success">Payment received — thank you.</div>}
      </div>
    </Modal>
  );
}

// ── Transactions (NC §27) ─────────────────────────────────────────────────

export function TransactionsPage() {
  const folios = useGet<Page<Schemas['Folio']>>('/api/v1/me/folios');
  const statement = useGet<Schemas['Statement']>('/api/v1/me/statement', { retry: false });
  const pay = useSend<Row, Schemas['OnlinePayment']>('POST', (b) => `/api/v1/me/folios/${b.id}:pay-online`, ['/api/v1/me/folios']);
  const [checkout, setCheckout] = useState<Schemas['OnlinePayment'] | null>(null);
  return (
    <div className="oc-stack">
      <PageHeader title="Transactions" />
      {statement.data && (
        <Card title={`Member Charges · ${statement.data.month}`} icon="account_balance_wallet">
          <div className="oc-row-wrap">
            <div><div className="oc-small oc-muted">Charges</div><div className="oc-metric">{money(statement.data.totalCharges)}</div></div>
            <div><div className="oc-small oc-muted">Payments</div><div className="oc-metric">{money(statement.data.totalPayments)}</div></div>
            <div><div className="oc-small oc-muted">Balance</div><div className="oc-metric">{money(statement.data.closingBalance)}</div></div>
          </div>
        </Card>
      )}
      <ErrorAlert error={pay.error} />
      <div className="oc-card">
        <DataTable rows={folios.data?.items as unknown as Row[]} loading={folios.isLoading} columns={[
          { key: 'openedAt', header: 'Date', render: (r) => formatDate(String(r.openedAt)) }, { key: 'code', header: 'Folio' },
          { key: 'businessLine', header: 'Line' }, { key: 'totalCharges', header: 'Charges', render: (r) => money(r.totalCharges) },
          { key: 'balance', header: 'Balance', render: (r) => money(r.balance) }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]}
          actions={(r) => r.status === 'open' && Number(r.balance) > 0 ? (
            <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => pay.mutate({ id: r.id, method: 'qris' }, { onSuccess: (c) => setCheckout(c) })}>Pay</button>
          ) : null} />
      </div>
      <CheckoutModal checkout={checkout} onClose={() => setCheckout(null)} />
    </div>
  );
}

// ── Voucher & Prepaid (FR-APP-P2-05) ──────────────────────────────────────

export function VouchersPage() {
  const vouchers = useGet<Page<Schemas['Voucher']>>('/api/v1/me/vouchers');
  const balances = useGet<Page<Schemas['PrepaidBalance']>>('/api/v1/me/prepaid-balances');
  const history = useGet<Page<Schemas['LedgerEntry']>>('/api/v1/me/voucher-history?limit=30');
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

// ── Golf: flights, scorecard, handicap, Hall of Fame (FR-APP-P2-02/08) ────

export function GolfPage() {
  const toast = useToast();
  const stats = useGet<Schemas['RoundStats']>('/api/v1/me/golf-stats');
  const flights = useGet<Page<Schemas['Flight']>>('/api/v1/me/flights');
  const hof = useGet<Schemas['MyHallOfFame']>('/api/v1/me/hall-of-fame');
  const consent = useSend<Row>('POST', (b) => `/api/v1/me/hall-of-fame/${b.id}:consent`, ['/api/v1/me/hall-of-fame']);
  const rate = useSend<Row>('POST', (b) => `/api/v1/me/caddy-assignments/${b.id}:rate`, []);
  const s = stats.data;
  return (
    <div className="oc-stack">
      <PageHeader title="Golf" />
      <div className="oc-grid">
        <div className="oc-card oc-card-ink"><div className="oc-small" style={{ opacity: 0.7 }}>Handicap Index</div>
          <div className="oc-metric">{s?.officialHandicap ?? s?.handicapIndex ?? '—'}</div><div className="oc-small">{s?.officialHandicap ? 'Official' : 'Local (WHS)'}</div></div>
        <div className="oc-card"><div className="oc-small oc-muted">Rounds</div><div className="oc-metric">{s?.rounds ?? '—'}</div></div>
        <div className="oc-card"><div className="oc-small oc-muted">Average gross</div><div className="oc-metric">{s?.averageGross ?? '—'}</div></div>
        <div className="oc-card"><div className="oc-small oc-muted">Birdies or better</div><div className="oc-metric">{s?.birdiesOrBetter ?? '—'}</div></div>
      </div>
      <Card title="My Flights" icon="golf_course">
        <ErrorAlert error={rate.error} />
        <div className="oc-stack">
          {flights.data?.items.map((f) => {
            const me = f.players.find((p) => p.scorecardId);
            return (
              <div key={f.id} className="oc-row-wrap" style={{ borderBottom: '1px solid var(--md-sys-color-outline-variant)', paddingBottom: 8 }}>
                <strong>{formatDateTime(f.teeTime)}</strong><span>{f.routeName}</span><StatusPill status={f.status} />
                <span className="oc-small">Caddy: {f.caddies.map((c) => c.caddyName).join(', ') || '—'} · Buggy: {f.golfCarts.map((c) => c.cartCode).join(', ') || '—'}</span>
                <span className="oc-spacer" />
                {me?.scorecardId && <Link className="oc-btn oc-btn-outline oc-btn-sm" to={`/golf/scorecards/${me.scorecardId}`}>Scorecard</Link>}
                {f.status === 'finished' && f.caddies.filter((c) => c.status === 'completed').map((c) => (
                  <select key={c.id} className="oc-input" style={{ width: 150 }} defaultValue="" aria-label={`Rate ${c.caddyName}`}
                    onChange={(e) => rate.mutate({ id: c.id, rating: Number(e.target.value) }, { onSuccess: () => toast('Thank you for rating your caddy') })}>
                    <option value="" disabled>Rate {c.caddyName}</option>{[5, 4, 3, 2, 1].map((n) => <option key={n} value={n}>{'★'.repeat(n)}</option>)}
                  </select>
                ))}
              </div>
            );
          })}
          {flights.data?.items.length === 0 && <Empty title="No rounds yet" icon="golf_course" />}
        </div>
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
  const card = useGet<Schemas['Scorecard']>(`/api/v1/me/scorecards/${id}`);
  const [draft, setDraft] = useState<Record<number, string>>({});
  const save = useSend<Row, Schemas['Scorecard']>('POST', `/api/v1/me/scorecards/${id}/scores`, [`/api/v1/me/scorecards/${id}`]);
  const submit = useSend<Row>('POST', `/api/v1/me/scorecards/${id}:submit`, [`/api/v1/me/scorecards/${id}`]);
  const c = card.data;
  if (!c) return <Skeleton rows={8} />;
  const locked = c.status === 'finalized';
  return (
    <div className="oc-stack">
      <PageHeader title={`Scorecard · ${formatDate(c.playedOn)}`} help={`${c.routeName} · ${c.teeSetName} · Par ${c.par}${c.gross ? ` · Gross ${c.gross}` : ''}`}
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
  const sessions = useGet<Page<Schemas['Session']>>('/api/v1/me/class-sessions?days=14');
  const classes = useGet<Page<Schemas['Enrollment']>>('/api/v1/me/classes');
  const mine = useGet<Page<Schemas['SessionBooking']>>('/api/v1/me/sessions');
  const book = useSend<Row>('POST', '/api/v1/me/session-bookings', ['/api/v1/me/sessions', '/api/v1/me/class-sessions']);
  return (
    <div className="oc-stack">
      <PageHeader title="Sport Club" help="Show your Digital Member Card QR at the gate for Facility Access." />
      <ErrorAlert error={book.error} />
      <Card title="My Classes" icon="school">
        <DataTable rows={classes.data?.items as unknown as Row[]} columns={[{ key: 'programName', header: 'Class' },
          { key: 'validUntil', header: 'Valid until', render: (r) => formatDate(String(r.validUntil)) }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
      </Card>
      <Card title="Book Class" icon="event">
        <DataTable rows={sessions.data?.items as unknown as Row[]} columns={[
          { key: 'start', header: 'When', render: (r) => formatDateTime(String(r.start)) }, { key: 'programName', header: 'Class' },
          { key: 'instructorName', header: 'Instructor' }, { key: 'booked', header: 'Booked', render: (r) => `${r.booked}/${r.capacity}` }]}
          actions={(r) => <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => book.mutate({ sessionId: r.id }, { onSuccess: () => toast('Session booked') })}>Book</button>} />
      </Card>
      <Card title="My Sessions" icon="event_available">
        <DataTable rows={mine.data?.items as unknown as Row[]} columns={[{ key: 'sessionId', header: 'Session', render: (r) => String(r.sessionId).slice(-6) },
          { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }, { key: 'quotaUsed', header: 'Quota used', render: (r) => (r.quotaUsed ? 'Yes' : 'No') }]} />
      </Card>
    </div>
  );
}

// ── Stay & Venue (FR-APP-P2-04) ───────────────────────────────────────────

export function StayPage() {
  const toast = useToast();
  const [from, setFrom] = useState(new Date(Date.now() + 86400_000).toISOString().slice(0, 10));
  const [nights, setNights] = useState('1');
  const avail = useGet<Page<Schemas['TypeAvailability']>>(`/api/v1/me/bungalow-availability${qs({ from, nights })}`);
  const stays = useGet<Page<Schemas['Stay']>>('/api/v1/me/stays');
  const book = useSend<Row, Schemas['StayResult']>('POST', '/api/v1/me/stays', ['/api/v1/me/stays'], idem);
  const [ratePlan, setRatePlan] = useState('');
  return (
    <div className="oc-stack">
      <PageHeader title="Stay & Venue" />
      <Card title="Book Bungalow" icon="hotel">
        <div className="oc-row-wrap">
          <div style={{ width: 180 }}><TextField label="Arrival" type="date" value={from} onChange={setFrom} /></div>
          <div style={{ width: 120 }}><TextField label="Nights" type="number" value={nights} onChange={setNights} /></div>
          <div style={{ width: 200 }}><TextField label="Rate plan code" value={ratePlan} onChange={setRatePlan} placeholder="e.g. STAY_RO" /></div>
        </div>
        <ErrorAlert error={book.error} />
        <DataTable rows={(avail.data?.items ?? []).map((t) => ({ ...t, available: Math.min(...t.nights.map((n) => n.available)) })) as unknown as Row[]} rowKey={(r) => String(r.typeId)}
          columns={[{ key: 'typeName', header: 'Bungalow type' }, { key: 'available', header: 'Available' }, { key: 'units', header: 'Units' }]}
          actions={(r) => Number(r.available) > 0 ? (
            <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => {
              const dep = new Date(new Date(from).getTime() + Number(nights) * 86400_000).toISOString().slice(0, 10);
              book.mutate({ kind: 'bungalow', bungalowTypeId: r.typeId, arrivalDate: from, departureDate: dep, ratePlan, adults: 2, memberCharge: true },
                { onSuccess: (s) => toast(`Booked ${s.stay.stayNo}`) });
            }}>Book</button>
          ) : <span className="oc-small oc-muted">Full</span>} />
      </Card>
      <Card title="My Stay" icon="luggage">
        <DataTable rows={stays.data?.items as unknown as Row[]} columns={[{ key: 'stayNo', header: 'Booking' }, { key: 'kind', header: 'Kind' },
          { key: 'unitName', header: 'Unit' }, { key: 'startAt', header: 'From', render: (r) => formatDateTime(String(r.startAt)) },
          { key: 'endAt', header: 'To', render: (r) => formatDateTime(String(r.endAt)) }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
      </Card>
    </div>
  );
}

// ── Bookings (all lines) ──────────────────────────────────────────────────

export function BookingsPage() {
  const b = useGet<Schemas['Bookings']>('/api/v1/me/bookings');
  return (
    <div className="oc-stack">
      <PageHeader title="Bookings" />
      <Card title="Upcoming" icon="event_upcoming">
        <DataTable rows={b.data?.upcoming as unknown as Row[]} loading={b.isLoading} columns={[{ key: 'code', header: 'Booking' }, { key: 'businessLine', header: 'Line' },
          { key: 'resource', header: 'Where' }, { key: 'start', header: 'When', render: (r) => formatDateTime(String(r.start)) },
          { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
      </Card>
      <Card title="Recent" icon="history">
        <DataTable rows={b.data?.recent as unknown as Row[]} columns={[{ key: 'code', header: 'Booking' }, { key: 'businessLine', header: 'Line' },
          { key: 'resource', header: 'Where' }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
      </Card>
    </div>
  );
}

// ── Order Food (FR-APP-P2-06) ─────────────────────────────────────────────

export function OrderFoodPage() {
  const toast = useToast();
  const [outlet, setOutlet] = useState('');
  const outlets = useGet<Page<Schemas['MyOutlet']>>('/api/v1/me/outlets');
  const menu = useGet<Page<Schemas['MenuItem']>>(outlet ? `/api/v1/me/outlets/${outlet}/menu` : null);
  const orders = useGet<Page<Schemas['Order']>>('/api/v1/me/orders', { refetchInterval: 10000 });
  const [cart, setCart] = useState<Record<string, number>>({});
  const place = useSend<Row, Schemas['Order']>('POST', '/api/v1/me/orders', ['/api/v1/me/orders'], idem);
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

// ── Profile: preferences & consent (FR-APP-P2-09) ─────────────────────────

export function PreferencesCard() {
  const toast = useToast();
  const p = useGet<Schemas['MyProfile']>('/api/v1/me/profile', { retry: false });
  const add = useSend<Row>('POST', '/api/v1/me/preferences', ['/api/v1/me/profile']);
  const remove = useSend<Row>('POST', (b) => `/api/v1/me/preferences/${b.id}:remove`, ['/api/v1/me/profile']);
  const consent = useSend<Row>('POST', '/api/v1/me/consent', ['/api/v1/me/profile']);
  const [cat, setCat] = useState('beverage');
  const [val, setVal] = useState('');
  if (!p.data) return null;
  return (
    <Card title="Preferences & consent" icon="tune">
      <ErrorAlert error={add.error ?? consent.error} />
      <div className="oc-stack">
        <Checkbox label="Allow personalisation from my activity (profiling)" checked={p.data.profile.consentProfiling}
          onChange={(v) => consent.mutate({ profiling: v }, { onSuccess: () => toast('Saved') })} />
        <Checkbox label="Receive offers and news" checked={p.data.profile.consentMarketing}
          onChange={(v) => consent.mutate({ marketing: v }, { onSuccess: () => toast('Saved') })} />
        {p.data.preferences.map((x) => (
          <div key={x.id} className="oc-row"><StatusPill status={x.category} label={x.category.replace(/_/g, ' ')} /><span>{x.value}</span><span className="oc-spacer" />
            <button className="oc-icon-btn" aria-label="Remove" onClick={() => remove.mutate({ id: x.id })}><Icon name="close" size={18} /></button></div>
        ))}
        <div className="oc-row-wrap">
          <div style={{ width: 180 }}><SelectField label="Category" value={cat} onChange={setCat}
            options={['beverage', 'food', 'diet', 'allergy', 'tee_time', 'golf_cart', 'facility', 'note'].map((c) => ({ value: c, label: c.replace(/_/g, ' ') }))} /></div>
          <div style={{ flex: 1, minWidth: 200 }}><TextField label="Preference" value={val} onChange={setVal} /></div>
          <button className="oc-btn oc-btn-ink" style={{ alignSelf: 'flex-end' }} disabled={!val.trim()}
            onClick={() => add.mutate({ category: cat, value: val }, { onSuccess: () => setVal('') })}>Add</button>
        </div>
      </div>
    </Card>
  );
}
