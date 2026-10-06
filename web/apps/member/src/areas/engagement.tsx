import React, { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router';
import { useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import { Card, Checkbox, DataTable, Empty, ErrorAlert, PageHeader, SelectField, Skeleton, StatusPill, TextArea, TextField, useToast } from '@oneclub/shell';

// Member App — Loyalty & Support (PRD P3 EP-07–09/19): Loyalty (My Points,
// Tier, Rewards, Points History; FR-APP-P3-01), Support (Feedback,
// Complaints with ticket status; FR-APP-P3-06) and Profile → Communication
// Preferences (opt-in per channel; FR-APP-P3-08).

type Row = Record<string, unknown> & { id: string };
const pts = (v: unknown) => formatNumber(Number(v ?? 0));
const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const LOYALTY = ['/api/v1/member/loyalty'];
const SUPPORT = ['/api/v1/member/tickets', '/api/v1/member/support'];

function LoyaltyNav() {
  return (
    <div className="oc-row-wrap" role="navigation" aria-label="Loyalty">
      <Link className="oc-chip" to="/loyalty">My Points</Link>
      <Link className="oc-chip" to="/loyalty/tier">Tier</Link>
      <Link className="oc-chip" to="/loyalty/rewards">Rewards</Link>
      <Link className="oc-chip" to="/loyalty/history">Points History</Link>
    </div>
  );
}

function useMyLoyalty() {
  return useGet<Row>('/api/v1/member/loyalty');
}

function JoinCard() {
  const join = useSend<Row, Row>('POST', '/api/v1/member/loyalty:join', LOYALTY);
  return (
    <Card title="Join Loyalty" icon="loyalty">
      <p>Earn points on every settled payment in golf, F&B, sport club and bungalow, and redeem them as payment or for rewards.</p>
      <button className="oc-btn oc-btn-primary" disabled={join.isPending} onClick={() => join.mutate({} as Row)}>Join now</button>
      <ErrorAlert error={join.error} />
    </Card>
  );
}

export function MyPointsPage() {
  const l = useMyLoyalty();
  const x = l.data;
  const a = (x?.account ?? null) as Row | null;
  const next = (x?.nextTier ?? null) as Row | null;
  return (
    <div className="oc-stack">
      <PageHeader title="Loyalty" />
      <LoyaltyNav />
      {l.isLoading ? <Skeleton /> : !x?.enrolled || !a ? <JoinCard /> : (
        <Card title="My Points" icon="stars" ink>
          <div style={{ fontSize: 40, fontWeight: 700 }}>{pts(a.balance)} <span className="oc-small">points</span></div>
          <p className="oc-muted">Worth {money(a.balanceValue)} · 1 point = {money(x.redemptionValue)} · 1 point per {money(x.amountPerPoint)} spent</p>
          <p>Tier: <strong>{String(a.tierName ?? '—')}</strong>{next ? <> · {pts(x.pointsToNext)} points to {String(next.name)}</> : null}</p>
          {Number(a.expiringPoints) > 0 && <p className="oc-alert oc-alert-warning" role="status">{pts(a.expiringPoints)} points expire on {formatDate(String(a.nextExpiry))}.</p>}
        </Card>
      )}
      {x?.enrolled && a ? <PayWithPoints balance={Number(a.balance)} value={Number(x.redemptionValue)} /> : null}
    </div>
  );
}

/** Pay an open bill with points (the loyalty_points tender of the folio). */
function PayWithPoints({ balance, value }: { balance: number; value: number }) {
  const toast = useToast();
  const tx = useGet<{ folios: Row[] }>('/api/v1/member/transactions');
  const [folio, setFolio] = useState('');
  const [points, setPoints] = useState('');
  const [key, setKey] = useState(() => crypto.randomUUID());
  const pay = useSend<Row, Row>('POST', `/api/v1/member/folios/${folio}:pay-with-points`, [...LOYALTY, '/api/v1/member/transactions'], () => ({ 'Idempotency-Key': key }));
  const open = (tx.data?.folios ?? []).filter((f) => f.status === 'open' && Number(f.balance) > 0);
  if (open.length === 0 || balance <= 0) return null;
  return (
    <Card title="Pay a bill with points" icon="payments">
      <div className="oc-form">
        <SelectField label="Open bill" value={folio} onChange={setFolio} placeholder="Select"
          options={open.map((f) => ({ value: f.id, label: `${String(f.number)} · ${money(f.balance)}` }))} />
        <TextField label="Points" type="number" inputMode="numeric" value={points} onChange={setPoints} help={`= ${money(Number(points || 0) * value)}`} />
      </div>
      <button className="oc-btn oc-btn-primary" disabled={!folio || !points || pay.isPending}
        onClick={() => pay.mutate({ points: Number(points) } as unknown as Row, { onSuccess: () => { toast('Paid with points'); setPoints(''); setKey(crypto.randomUUID()); } })}>Pay</button>
      <ErrorAlert error={pay.error} />
    </Card>
  );
}

export function TierPage() {
  const tiers = useGet<Page<Row>>('/api/v1/member/loyalty/tiers');
  return (
    <div className="oc-stack">
      <PageHeader title="Tier" />
      <LoyaltyNav />
      {tiers.isLoading ? <Skeleton /> : (tiers.data?.items ?? []).map((t) => (
        <Card key={t.id} title={String(t.name)} icon={t.current ? 'workspace_premium' : 'military_tech'} ink={Boolean(t.current)}>
          {t.current ? <StatusPill status="active" label="Your tier" /> : null}
          <p>From {pts(t.minPoints)} points{Number(t.minSpend) > 0 ? ` and ${money(t.minSpend)} spend` : ''} per evaluation period · points × {String(t.multiplier)}</p>
          {t.benefits ? <p className="oc-muted">{String(t.benefits)}</p> : null}
        </Card>
      ))}
    </div>
  );
}

export function RewardsPage() {
  const toast = useToast();
  const rewards = useGet<Page<Row>>('/api/v1/member/loyalty/rewards');
  const mine = useGet<Page<Row>>('/api/v1/member/loyalty/redemptions');
  const redeem = useSend<Row, Row>('POST', (b) => `/api/v1/member/loyalty/rewards/${String(b.id)}:redeem`, LOYALTY);
  return (
    <div className="oc-stack">
      <PageHeader title="Rewards" />
      <LoyaltyNav />
      <ErrorAlert error={redeem.error} />
      {(rewards.data?.items ?? []).length === 0 && !rewards.isLoading ? <Empty title="No rewards available" icon="redeem" /> : (
        <div className="oc-grid">
          {(rewards.data?.items ?? []).map((r) => (
            <Card key={r.id} title={String(r.name)} icon="redeem">
              {r.description ? <p className="oc-muted">{String(r.description)}</p> : null}
              <p><strong>{pts(r.pointsCost)} points</strong>{r.minTierName ? ` · ${String(r.minTierName)} and above` : ''}</p>
              <button className="oc-btn oc-btn-primary" disabled={!r.affordable || redeem.isPending}
                onClick={() => redeem.mutate({ id: r.id, quantity: 1 } as Row, { onSuccess: (x) => toast(`Redeemed ${String(x.number)}${x.fulfilmentCode ? ` · code ${String(x.fulfilmentCode)}` : ''}`) })}>
                Redeem
              </button>
            </Card>
          ))}
        </div>
      )}
      <Card title="My Rewards" icon="inventory_2">
        <DataTable rows={mine.data?.items} loading={mine.isLoading} columns={[{ key: 'createdAt', header: 'Date', render: (r) => formatDate(String(r.createdAt)) },
          { key: 'rewardName', header: 'Reward' }, { key: 'points', header: 'Points', align: 'right', render: (r) => pts(r.points) },
          { key: 'fulfilmentCode', header: 'Code', render: (r) => String(r.fulfilmentCode ?? '—') }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
      </Card>
    </div>
  );
}

export function PointsHistoryPage() {
  const h = useGet<Page<Row>>('/api/v1/member/loyalty/history?limit=200', { retry: false });
  return (
    <div className="oc-stack">
      <PageHeader title="Points History" />
      <LoyaltyNav />
      {h.error ? <JoinCard /> : (
        <DataTable rows={h.data?.items} loading={h.isLoading} columns={[{ key: 'occurredAt', header: 'Date', render: (r) => formatDateTime(String(r.occurredAt)) },
          { key: 'kind', header: 'Type', render: (r) => <StatusPill status={String(r.kind)} /> }, { key: 'description', header: 'Description' },
          { key: 'points', header: 'Points', align: 'right', render: (r) => (Number(r.points) > 0 ? `+${pts(r.points)}` : pts(r.points)) },
          { key: 'balanceAfter', header: 'Balance', align: 'right', render: (r) => pts(r.balanceAfter) }]} />
      )}
    </div>
  );
}

// ── Support ───────────────────────────────────────────────────────────────

function SupportNav() {
  return (
    <div className="oc-row-wrap" role="navigation" aria-label="Support">
      <Link className="oc-chip" to="/support/feedback">Feedback</Link>
      <Link className="oc-chip" to="/support/complaints">Complaints</Link>
    </div>
  );
}

export function FeedbackPage() {
  const toast = useToast();
  const [ctx, setCtx] = useState('other');
  const [rating, setRating] = useState('5');
  const [nps, setNps] = useState('');
  const [comment, setComment] = useState('');
  const fb = useSend<Row, Row>('POST', '/api/v1/member/support/feedback', SUPPORT);
  const npsSend = useSend<Row, Row>('POST', '/api/v1/member/nps', SUPPORT);
  const submit = () => fb.mutate({ contextType: ctx, rating: Number(rating), nps: nps === '' ? undefined : Number(nps), comment } as unknown as Row, {
    onSuccess: () => {
      if (nps !== '') npsSend.mutate({ score: Number(nps), comment } as unknown as Row);
      toast('Thank you for your feedback');
      setComment('');
    },
  });
  return (
    <div className="oc-stack">
      <PageHeader title="Support" />
      <SupportNav />
      <Card title="Send Feedback" icon="rate_review">
        <div className="oc-form">
          <SelectField label="About" value={ctx} onChange={setCtx} options={['round', 'fnb', 'stay', 'class', 'sport', 'meeting', 'other']
            .map((v) => ({ value: v, label: { round: 'Golf round', fnb: 'Food & beverage', stay: 'Bungalow / stay', class: 'Class', sport: 'Sport club', meeting: 'Meeting / event', other: 'Something else' }[v] ?? v }))} />
          <SelectField label="Rating" value={rating} onChange={setRating} options={['5', '4', '3', '2', '1'].map((v) => ({ value: v, label: '★'.repeat(Number(v)) }))} />
          <SelectField label="How likely are you to recommend us? (0–10)" value={nps} onChange={setNps} placeholder="—"
            options={Array.from({ length: 11 }, (_, i) => ({ value: String(10 - i), label: String(10 - i) }))} />
          <TextArea label="Comment" value={comment} onChange={setComment} span />
        </div>
        <button className="oc-btn oc-btn-primary" disabled={fb.isPending} onClick={submit}>Send</button>
        <ErrorAlert error={fb.error} />
      </Card>
    </div>
  );
}

export function ComplaintsPage() {
  const list = useGet<Page<Row>>('/api/v1/member/tickets');
  const cats = useGet<Page<Row>>('/api/v1/member/ticket-categories');
  const [cat, setCat] = useState('');
  const [subject, setSubject] = useState('');
  const [desc, setDesc] = useState('');
  const send = useSend<Row, Row>('POST', '/api/v1/member/tickets', SUPPORT);
  return (
    <div className="oc-stack">
      <PageHeader title="Complaints" />
      <SupportNav />
      <Card title="New Complaint" icon="report">
        <div className="oc-form">
          <SelectField label="Category" value={cat} onChange={setCat} placeholder="—" options={(cats.data?.items ?? []).map((c) => ({ value: c.id, label: String(c.name) }))} />
          <TextField label="Subject" value={subject} onChange={setSubject} required span />
          <TextArea label="What happened?" value={desc} onChange={setDesc} required span />
        </div>
        <button className="oc-btn oc-btn-primary" disabled={!subject || !desc || send.isPending}
          onClick={() => send.mutate({ categoryId: cat || undefined, subject, description: desc } as unknown as Row, { onSuccess: () => { setSubject(''); setDesc(''); } })}>Send</button>
        <ErrorAlert error={send.error} />
      </Card>
      <Card title="My Complaints" icon="support_agent">
        {(list.data?.items ?? []).length === 0 && !list.isLoading ? <Empty title="No complaints" icon="sentiment_satisfied" /> : (
          <DataTable rows={list.data?.items} loading={list.isLoading} columns={[
            { key: 'number', header: 'Ticket', render: (r) => <Link to={`/support/complaints/${r.id}`}>{String(r.number)}</Link> }, { key: 'subject', header: 'Subject' },
            { key: 'createdAt', header: 'Sent', render: (r) => formatDate(String(r.createdAt)) }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
        )}
      </Card>
    </div>
  );
}

export function ComplaintDetailPage() {
  const { id } = useParams();
  const d = useGet<Row & { events: Row[] }>(`/api/v1/member/tickets/${id}`);
  const [reply, setReply] = useState('');
  const [reason, setReason] = useState('');
  const send = useSend<Row, Row>('POST', `/api/v1/member/tickets/${id}:reply`, SUPPORT);
  const reopen = useSend<Row, Row>('POST', `/api/v1/member/tickets/${id}:reopen`, SUPPORT);
  const x = d.data;
  return (
    <div className="oc-stack">
      <PageHeader title={x ? `Complaint ${String(x.number)}` : 'Complaint'} actions={<Link className="oc-btn oc-btn-neutral" to="/support/complaints">Back</Link>} />
      {!x ? <Skeleton /> : (
        <>
          <Card title={String(x.subject)} icon="report" actions={<StatusPill status={String(x.status)} />}>
            <p style={{ whiteSpace: 'pre-wrap' }}>{String(x.description)}</p>
            {x.resolution ? <p><strong>Resolution:</strong> {String(x.resolution)}</p> : null}
          </Card>
          <Card title="Conversation" icon="forum">
            {(x.events ?? []).filter((e) => e.body).map((e) => (
              <div key={String(e.id)} style={{ padding: '6px 0', borderBottom: '1px solid var(--md-sys-color-outline-variant)' }}>
                <div className="oc-small oc-muted">{e.actorType === 'customer' ? 'You' : 'Club'} · {formatDateTime(String(e.createdAt))} · {label(e.kind)}</div>
                <div style={{ whiteSpace: 'pre-wrap' }}>{String(e.body)}</div>
              </div>
            ))}
            {x.status !== 'closed' && x.status !== 'resolved' && <>
              <TextArea label="Your reply" value={reply} onChange={setReply} />
              <button className="oc-btn oc-btn-primary" disabled={!reply || send.isPending} onClick={() => send.mutate({ body: reply } as unknown as Row, { onSuccess: () => setReply('') })}>Send</button>
            </>}
            {(x.status === 'resolved' || x.status === 'closed') && <>
              <TextField label="Not solved? Tell us why" value={reason} onChange={setReason} />
              <button className="oc-btn oc-btn-neutral" disabled={!reason || reopen.isPending} onClick={() => reopen.mutate({ reason } as unknown as Row)}>Reopen</button>
            </>}
            <ErrorAlert error={send.error ?? reopen.error} />
          </Card>
        </>
      )}
    </div>
  );
}

// ── Profile → Communication Preferences ──────────────────────────────────

export function CommunicationPreferencesPage() {
  const toast = useToast();
  const p = useGet<Row & { channels: Row[] }>('/api/v1/member/communication-preferences');
  const save = useSend<Row, Row>('POST', '/api/v1/member/communication-preferences', ['/api/v1/member/communication-preferences']);
  const [v, setV] = useState<Record<string, boolean>>({});
  useEffect(() => {
    if (p.data) setV(Object.fromEntries(p.data.channels.map((c) => [String(c.channel), Boolean(c.optedIn)])));
  }, [p.data]);
  return (
    <div className="oc-stack">
      <PageHeader title="Communication Preferences" actions={<Link className="oc-btn oc-btn-neutral" to="/profile">Profile</Link>} />
      <Card title="Offers and news" icon="mark_email_read">
        <p className="oc-muted">Choose how the club may send you offers, campaigns and reminders. Service messages (bookings, payments, complaints) are always sent.</p>
        {!p.data ? <Skeleton /> : <>
          <Checkbox label="E-mail" checked={!!v.email} onChange={(c) => setV((x) => ({ ...x, email: c }))} />
          <Checkbox label="WhatsApp" checked={!!v.whatsapp} onChange={(c) => setV((x) => ({ ...x, whatsapp: c }))} />
          <Checkbox label="In-app notifications" checked={!!v.in_app} onChange={(c) => setV((x) => ({ ...x, in_app: c }))} />
          {p.data.channels.some((c) => c.suppressed) && <p className="oc-small oc-muted">One of your addresses is blocked for marketing messages; contact the club to lift it.</p>}
          <button className="oc-btn oc-btn-primary" disabled={save.isPending} onClick={() => save.mutate({ email: !!v.email, whatsapp: !!v.whatsapp, inApp: !!v.in_app } as unknown as Row,
            { onSuccess: () => toast('Preferences saved') })}>Save</button>
        </>}
        <ErrorAlert error={save.error} />
      </Card>
    </div>
  );
}

/** In-app campaign message link: tracked, then redirected to the offer. */
export function CampaignLinkPage() {
  const { token } = useParams();
  useEffect(() => {
    window.location.assign(`/api/v1/public/campaign-links/${token}`);
  }, [token]);
  return <Skeleton />;
}

/** Member App routes of the area. */
export const ENGAGEMENT_MEMBER_ROUTES: { path: string; element: React.ReactNode }[] = [
  { path: 'loyalty', element: <MyPointsPage /> },
  { path: 'loyalty/tier', element: <TierPage /> },
  { path: 'loyalty/rewards', element: <RewardsPage /> },
  { path: 'loyalty/history', element: <PointsHistoryPage /> },
  { path: 'support', element: <FeedbackPage /> },
  { path: 'support/feedback', element: <FeedbackPage /> },
  { path: 'support/complaints', element: <ComplaintsPage /> },
  { path: 'support/complaints/:id', element: <ComplaintDetailPage /> },
  { path: 'profile/communication-preferences', element: <CommunicationPreferencesPage /> },
  { path: 'c/:token', element: <CampaignLinkPage /> },
];
