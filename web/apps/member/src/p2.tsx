import React, { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { getActiveProperty, request, uuidv7, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
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

const STAGES: [string, string][] = [['in_play', 'In play'], ['completed', 'Round completed'], ['submitted', 'Submitted'], ['finalized', 'Finalized']];

export function ScorecardPage() {
  const { id } = useParams();
  const toast = useToast();
  const path = `/api/v1/member/golf/scorecards/${id}`;
  const card = useGet<Schemas['Scorecard']>(path);
  const [draft, setDraft] = useState<Record<number, string>>({});
  const save = useSend<Row, Schemas['Scorecard']>('POST', `${path}/scores`, [path]);
  const submit = useSend<Row>('POST', `${path}:submit`, [path]);
  const share = useSend<Record<string, never>, Schemas['ScorecardShare']>('POST', `${path}:share`, []);
  const corrections = useGet<Page<Schemas['CorrectionRequest']>>(`${path}/correction-requests`);
  const ask = useSend<Row, Schemas['CorrectionRequest']>('POST', `${path}/correction-requests`, [`${path}/correction-requests`]);
  const [fix, setFix] = useState<{ seq: string; strokes: string; reason: string } | null>(null);
  const [link, setLink] = useState('');
  const c = card.data;
  if (!c) return <Skeleton rows={8} />;
  // after Complete Round the card is read-only: a wrong score goes through a correction request (demo feedback #36)
  const locked = c.locked || c.status === 'finalized';
  const stage = STAGES.findIndex(([k]) => k === c.stage);
  const pending = (corrections.data?.items ?? []).filter((x) => x.status === 'requested');
  const getLink = async () => {
    if (link) return link;
    const r = await share.mutateAsync({});
    setLink(r.link);
    return r.link;
  };
  const shareIt = async () => {
    const url = await getLink();
    const text = `My scorecard of ${formatDate(c.playedOn)}${c.gross ? ` — gross ${c.gross}` : ''}`;
    if (navigator.share) {
      try { await navigator.share({ title: 'Scorecard', text, url }); return; } catch { /* closed: fall back to the link */ }
    }
    window.open(`https://wa.me/?text=${encodeURIComponent(`${text}: ${url}`)}`, '_blank', 'noopener');
  };
  return (
    <div className="oc-stack">
      <PageHeader title={`Scorecard · ${formatDate(c.playedOn)}`} help={`${c.playingRouteName ?? ''} · ${c.teeSetName ?? ''} · Par ${c.par}${c.gross ? ` · Gross ${c.gross}` : ''}`}
        actions={<StatusPill status={c.status} />} />
      <ol className="oc-steps" aria-label="Scorecard stage">
        {STAGES.map(([k, l], i) => <li key={k} data-state={i < stage ? 'done' : i === stage ? 'current' : undefined} aria-current={i === stage ? 'step' : undefined}>
          <span className="oc-steps-dot">{i < stage ? <Icon name="check" size={14} /> : i + 1}</span>{l}</li>)}
      </ol>
      <ErrorAlert error={save.error ?? submit.error ?? share.error ?? ask.error} />
      <div className="oc-card" style={{ overflowX: 'auto' }}>
        <table className="oc-table">
          <thead><tr><th>Hole</th>{c.scores.map((h) => <th key={h.seq}>{h.sectionCode.slice(-1)}{h.holeNumber}</th>)}</tr></thead>
          <tbody>
            <tr><td>Par</td>{c.scores.map((h) => <td key={h.seq}>{h.par}</td>)}</tr>
            <tr><td>SI</td>{c.scores.map((h) => <td key={h.seq}>{h.strokeIndex}</td>)}</tr>
            <tr><td>Score</td>{c.scores.map((h) => (
              <td key={h.seq}>{locked ? <strong>{h.strokes ?? '—'}</strong> : (
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
        </div>
      )}
      {locked && (
        <div className="oc-row-wrap">
          <button className="oc-btn oc-btn-ink" disabled={share.isPending} onClick={() => void getLink().then((u) => window.open(u, '_blank', 'noopener'))}><Icon name="download" size={18} /> Download PDF</button>
          <button className="oc-btn oc-btn-outline" disabled={share.isPending} onClick={() => void getLink().then((u) => { const w = window.open(u, '_blank', 'noopener'); w?.addEventListener('load', () => w.print()); })}><Icon name="print" size={18} /> Print</button>
          <button className="oc-btn oc-btn-outline" disabled={share.isPending} onClick={() => void shareIt()}><Icon name="share" size={18} /> Share</button>
          {c.stage === 'completed' && <button className="oc-btn oc-btn-outline" disabled={submit.isPending} onClick={() => submit.mutate({}, { onSuccess: () => toast('Submitted for finalization') })}>Submit</button>}
          {c.status !== 'finalized' && <button className="oc-btn oc-btn-text" onClick={() => setFix({ seq: '', strokes: '', reason: '' })}>Ask for a correction</button>}
        </div>
      )}
      {link && <p className="oc-small oc-muted" style={{ margin: 0, wordBreak: 'break-all' }}>Link: {link}</p>}
      {fix && (
        <Card title="Ask for a score correction" icon="edit_note">
          <p className="oc-small oc-muted" style={{ marginTop: 0 }}>The round is completed, so the score is changed by the Marshal / handicap committee after they check it.</p>
          <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            <SelectField label="Hole" value={fix.seq} onChange={(v) => setFix({ ...fix, seq: v })} placeholder="Choose"
              options={c.scores.map((h) => ({ value: String(h.seq), label: `${h.sectionCode.slice(-1)}${h.holeNumber} · now ${h.strokes ?? '—'}` }))} />
            <TextField label="Correct strokes" value={fix.strokes} onChange={(v) => setFix({ ...fix, strokes: v.replace(/\D/g, '').slice(0, 2) })} inputMode="numeric" />
            <TextField label="Reason" value={fix.reason} onChange={(v) => setFix({ ...fix, reason: v })} />
          </div>
          <div className="oc-row" style={{ marginTop: 8 }}>
            <button className="oc-btn oc-btn-neutral" onClick={() => setFix(null)}>Cancel</button>
            <button className="oc-btn oc-btn-ink" disabled={!fix.seq || !fix.strokes || !fix.reason.trim() || ask.isPending}
              onClick={() => ask.mutate({ seq: Number(fix.seq), strokes: Number(fix.strokes), reason: fix.reason.trim() }, { onSuccess: () => { setFix(null); toast('Correction requested'); } })}>Send request</button>
          </div>
        </Card>
      )}
      {(corrections.data?.items ?? []).length > 0 && (
        <Card title="Correction requests" icon="history">
          {(corrections.data?.items ?? []).map((x) => (
            <div key={x.id} className="oc-row-wrap" style={{ marginBottom: 6 }}>
              <span>Hole {x.holeNumber}: {x.currentStrokes ?? '—'} → <strong>{x.strokes}</strong></span><span className="oc-muted">{x.reason}</span>
              <StatusPill status={x.status === 'requested' ? 'pending' : x.status} />{x.decisionNote ? <span className="oc-small oc-muted">{x.decisionNote}</span> : null}
            </div>
          ))}
          {pending.length > 0 && <p className="oc-small oc-muted" style={{ margin: 0 }}>Waiting for the Marshal / handicap committee.</p>}
        </Card>
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
      <PageHeader title="Kelas Sport Club" help="Daftar kelas, booking sesi, dan pakai kuota paket kelas. Lapangan dipesan di menu Pesan Lapangan." />
      <ErrorAlert error={book.error} />
      <MemberClassEnroll enrolled={(classes.data?.items ?? []).filter((c) => c.status === 'active').map((c) => c.programId)} onDone={() => void classes.refetch()} />
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

type SportPage = { facilities: { id: string; name: string; usageMode: string }[]; courts: { id: string; name: string; facilityId: string; resourceId?: string | null }[];
  classPrograms: { id: string; name: string; discipline: string }[] };
/** Book Class: register for a class program, then book its sessions (#39). */
function MemberClassEnroll({ enrolled, onDone }: { enrolled: string[]; onDone: () => void }) {
  const toast = useToast();
  const page = useGet<SportPage>(`/api/v1/public/sport-club?propertyId=${getActiveProperty()}`);
  const [program, setProgram] = useState('');
  const [charge, setCharge] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [payment, setPayment] = useState<Schemas['Payment'] | null>(null);
  const open = (page.data?.classPrograms ?? []).filter((p) => !enrolled.includes(p.id));
  if (!(page.data?.classPrograms ?? []).length) return null;
  const enroll = async () => {
    setBusy(true);
    setError(null);
    try {
      const e = await request<Schemas['Enrollment']>('POST', '/api/v1/member/sport-club/class-enrollments', { programId: program, memberCharge: charge }, { 'Idempotency-Key': uuidv7() });
      onDone();
      if (!charge && e.folioId) setPayment(await request<Schemas['Payment']>('POST', `/api/v1/member/folios/${e.folioId}:pay-online`, { method: 'qris' }));
      else toast(`Registered for ${e.programName}`);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title="Register for a Class" icon="how_to_reg">
      {payment ? <PaymentPanel payment={payment} onPaid={() => { setPayment(null); toast('Registration paid'); onDone(); }} onFailed={() => setPayment(null)} /> : (
        <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <SelectField label="Class" value={program} onChange={setProgram} placeholder={open.length ? 'Choose' : 'You are registered for every class'}
            options={open.map((p) => ({ value: p.id, label: p.name }))} />
          <Checkbox label="Charge to my member account" checked={charge} onChange={setCharge} />
          <button className="oc-btn oc-btn-ink" disabled={!program || busy} onClick={() => void enroll()}>Register</button>
        </div>
      )}
      <p className="oc-small oc-muted" style={{ marginBottom: 0 }}>After registering, book the sessions below; buy a 4x / 8x class package at member rates in <Link to="/membership/packages">Paket &amp; Voucher</Link>.</p>
      <ErrorAlert error={error} />
    </Card>
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
  const [dest, setDest] = useState('pickup');
  const [hole, setHole] = useState('');
  const place = useSend<Row, Schemas['Order']>('POST', '/api/v1/member/orders', ['/api/v1/member/orders'], idem);
  // FR-FNB-02 on-course order: delivered to the halfway house or the hole the flight is on
  const where = dest === 'hole' ? { orderType: 'on_course', servingDestination: 'hole', destinationRef: `Hole ${hole}` }
    : dest === 'halfway_house' ? { orderType: 'on_course', servingDestination: 'halfway_house', destinationRef: 'Halfway House' } : {};
  return (
    <div className="oc-stack">
      <PageHeader title="Order Food" help="Pre-order to pick up, or have it delivered on course: at the halfway house or to your hole." />
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
          <div className="oc-row-wrap" style={{ marginTop: 12, alignItems: 'flex-end' }}>
            <div style={{ width: 220 }}><SelectField label="Deliver to" value={dest} onChange={setDest}
              options={[{ value: 'pickup', label: 'Pick up' }, { value: 'halfway_house', label: 'Halfway House' }, { value: 'hole', label: 'My hole (on course)' }]} /></div>
            {dest === 'hole' && <div style={{ width: 120 }}><TextField label="Hole" value={hole} onChange={(v) => setHole(v.replace(/\D/g, '').slice(0, 2))} /></div>}
          </div>
          <button className="oc-btn oc-btn-ink" style={{ marginTop: 12 }} disabled={!Object.values(cart).some((n) => n > 0) || (dest === 'hole' && !hole)}
            onClick={() => place.mutate({ outletId: outlet, memberCharge: true, ...where, lines: Object.entries(cart).filter(([, n]) => n > 0).map(([productId, n]) => ({ productId, quantity: String(n) })) },
              { onSuccess: (o) => { setCart({}); toast(`Order ${o.orderNo} sent to the kitchen`); } })}>Order (member charge)</button>
        </Card>
      )}
      <Card title="My orders" icon="receipt">
        <DataTable rows={orders.data?.items as unknown as Row[]} columns={[{ key: 'orderNo', header: 'Order' }, { key: 'outletName', header: 'Outlet' },
          { key: 'destinationRef', header: 'Deliver to', render: (r) => String(r.destinationRef ?? 'Pick up') },
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
  { path: 'sport-club/classes', element: <SportClubPage /> },
  { path: 'vouchers', element: <VouchersPage /> },
  { path: 'membership/services', element: <MembershipServicesPage /> },
  { path: 'order-food', element: <OrderFoodPage /> },
  { path: 'preferences', element: <PreferencesPage /> },
];
