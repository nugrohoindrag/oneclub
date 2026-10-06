import React, { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router';
import { request, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatNumber } from '@oneclub/i18n';
import { Card, DataTable, Empty, ErrorAlert, Icon, PageHeader, Skeleton, StatusPill } from '@oneclub/shell';

// Member App — PRD P5 advanced loyalty (tier progress, benefits, eligible and issued rewards; EP-18 / EP-26) and personal offers from the
// journeys (EP-19, §7.4). Registered in src/p3.tsx.

type Row = Record<string, unknown> & { id: string };
const pts = (v: unknown) => formatNumber(Number(v ?? 0));
const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const REASONS: Record<string, string> = { tier_required: 'Needs a higher tier', segment_required: 'For selected members', activity_required: 'Needs more visits',
  spend_required: 'Needs more spend', limit_reached: 'Already received', not_valid_today: 'Not available today' };

function LoyaltyNav() {
  return (
    <div className="oc-row-wrap" role="navigation" aria-label="Loyalty">
      <Link className="oc-chip" to="/loyalty">My Points</Link>
      <Link className="oc-chip" to="/loyalty/tier">Tier</Link>
      <Link className="oc-chip" to="/loyalty/progress">Tier Progress</Link>
      <Link className="oc-chip" to="/loyalty/rewards">Rewards</Link>
      <Link className="oc-chip" to="/loyalty/offers">Personal Offers</Link>
      <Link className="oc-chip" to="/loyalty/history">Points History</Link>
    </div>
  );
}

/** Tier class badge (PRD P5 member tier class): icon in the tier colour and the name. */
export function TierBadge({ b }: { b: Row }) {
  if (!b.tierName) return null;
  const color = String(b.tierColor ?? '#6B7280');
  return (
    <span className="oc-chip" role="img" aria-label={`Tier ${String(b.tierName)}`}
      style={{ borderColor: color, borderLeftWidth: 4, display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 16 }}>
      <span style={{ color, display: 'inline-flex' }}><Icon name={String(b.tierIcon ?? 'workspace_premium')} size={20} /></span>{String(b.tierName)}
    </span>
  );
}

function Benefits({ b }: { b: Row }) {
  const items: string[] = [];
  if (Number(b.pointsMultiplier) > 1) items.push(`Points × ${String(b.pointsMultiplier)}`);
  if (Number(b.bookingWindowDays) > 0) items.push(`Book tee times ${String(b.bookingWindowDays)} days earlier`);
  if (Number(b.fnbDiscountPercent) > 0) items.push(`${String(b.fnbDiscountPercent)}% F&B discount`);
  if (b.eventAccess) items.push('Invitations to VIP events');
  if (b.priorityService) items.push('Priority service');
  return items.length ? <ul style={{ margin: 0, paddingLeft: 20 }}>{items.map((x) => <li key={x}>{x}</li>)}</ul> : <p className="oc-muted">Earn points on every visit.</p>;
}

export function TierProgressPage() {
  const p = useGet<Row>('/api/v1/member/loyalty/progress');
  const el = useGet<Page<Row>>('/api/v1/member/loyalty/eligible-rewards', { retry: false });
  const issued = useGet<Page<Row>>('/api/v1/member/loyalty/issued-rewards');
  const x = p.data;
  const b = (x?.benefits ?? {}) as Row;
  const next = (x?.nextTier ?? null) as Row | null;
  const nb = (x?.nextTierBenefits ?? null) as Row | null;
  const progress = Number(x?.progressPercent ?? 0);
  return (
    <div className="oc-stack">
      <PageHeader title="Tier Progress" />
      <LoyaltyNav />
      {p.isLoading ? <Skeleton /> : !x?.enrolled ? <Empty title="Join Loyalty first" help="Open My Points to join the loyalty programme." icon="loyalty" /> : <>
        <Card title={`Your tier: ${String(b.tierName ?? '—')}`} icon="workspace_premium" ink>
          <p><TierBadge b={b} /></p>
          {b.overridden ? <p className="oc-small" role="status">Tier given by the club{b.overrideUntil ? ` until ${formatDate(String(b.overrideUntil))}` : ''}.</p> : null}
          {x.currentTierStatus === 'grace' && <p className="oc-alert oc-alert-warning" role="status">
            Your spend is below the threshold of {String(b.tierName)}. You keep it until {formatDate(String(x.graceUntil))}.</p>}
          <Benefits b={b} />
          <p className="oc-small">Qualifying spend {formatDate(String(x.windowFrom))} – {formatDate(String(x.windowTo))}: <strong>{money(x.spend)}</strong> · {pts(x.points)} points ·
            next evaluation {formatDate(String(x.nextEvaluationOn))}</p>
        </Card>
        {next && (
          <Card title={`Next: ${String(next.name)}`} icon="military_tech">
            <div role="progressbar" aria-valuenow={progress} aria-valuemin={0} aria-valuemax={100} aria-label={`Progress to ${String(next.name)}`}
              style={{ height: 12, borderRadius: 6, background: 'var(--oc-surface-2, #e5e7eb)', overflow: 'hidden' }}>
              <div style={{ width: `${progress}%`, height: '100%', background: 'var(--oc-accent, #0b6e4f)' }} />
            </div>
            <p>{Number(x.spendToNext) > 0 ? `${money(x.spendToNext)} more spend` : 'Threshold reached'}{Number(x.pointsToNext) > 0 ? ` and ${pts(x.pointsToNext)} points` : ''} to reach {String(next.name)}.</p>
            {nb && <Benefits b={nb} />}
          </Card>
        )}
        <Card title="Rewards for you" icon="redeem">
          {el.error ? <ErrorAlert error={el.error} /> : <DataTable rows={el.data?.items} loading={el.isLoading} columns={[{ key: 'name', header: 'Reward' },
            { key: 'pointsCost', header: 'Points', align: 'right', render: (r) => pts(r.pointsCost) },
            { key: 'eligible', header: 'Available', render: (r) => (r.eligible ? <StatusPill status="active" label={r.affordable ? 'Redeem now' : 'Collect more points'} />
              : <StatusPill status="inactive" label={REASONS[String(r.reason)] ?? 'Not available'} />) }]} />}
          <Link to="/loyalty/rewards">Redeem rewards</Link>
        </Card>
        <Card title="Rewards given to you" icon="card_giftcard">
          <DataTable rows={issued.data?.items} loading={issued.isLoading} columns={[{ key: 'createdAt', header: 'Date', render: (r) => formatDate(String(r.createdAt)) },
            { key: 'rewardName', header: 'Reward' }, { key: 'source', header: 'For', render: (r) => (r.source === 'top_spender' ? 'Top Spender' : label(r.source)) },
            { key: 'fulfilmentCode', header: 'Code', render: (r) => String(r.fulfilmentCode ?? '—') }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
        </Card>
      </>}
    </div>
  );
}

export function OffersPage() {
  const { token } = useParams();
  const offers = useGet<Page<Row>>('/api/v1/member/journey-offers');
  const read = useSend<Row, Row>('POST', (b) => `/api/v1/member/journey-offers/${String(b.id)}:read`, ['/api/v1/member/journey-offers']);
  const items = offers.data?.items ?? [];
  // Opened from a notification link: the newest unread offer counts as read.
  useEffect(() => {
    const first = items.find((o) => !o.readAt);
    if (token && first) read.mutate({ id: first.id } as Row);
  }, [token, items.length]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <div className="oc-stack">
      <PageHeader title="Personal Offers" />
      <LoyaltyNav />
      <PushCard />
      {offers.isLoading ? <Skeleton /> : items.length === 0 ? <Empty title="No offers right now" help="Personal offers from the club appear here." icon="local_offer" /> : (
        <div className="oc-grid">
          {items.map((o) => (
            <Card key={o.id} title={String(o.title)} icon="local_offer" ink={!o.readAt}>
              {o.message ? <p style={{ whiteSpace: 'pre-wrap' }}>{String(o.message)}</p> : null}
              {o.promoCode ? <p>Code: <strong>{String(o.promoCode)}</strong></p> : null}
              {o.expiresOn ? <p className="oc-small oc-muted">Valid until {formatDate(String(o.expiresOn))}</p> : null}
              {!o.readAt && <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={read.isPending} onClick={() => read.mutate({ id: o.id } as Row)}>Mark as read</button>}
            </Card>
          ))}
        </div>
      )}
      <ErrorAlert error={read.error} />
    </div>
  );
}

export const CRM_P5_MEMBER_ROUTES: { path: string; element: React.ReactNode }[] = [
  { path: 'loyalty/progress', element: <TierProgressPage /> },
  { path: 'loyalty/offers', element: <OffersPage /> },
  { path: 'loyalty/offers/:token', element: <OffersPage /> },
];

const b64ToBytes = (s: string) => {
  const raw = atob((s + '='.repeat((4 - (s.length % 4)) % 4)).replace(/-/g, '+').replace(/_/g, '/'));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
};
const bytesToB64 = (b: ArrayBuffer | null) => (b ? btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') : '');

/** PRD P5 FR-INT-P5-05: offers, tier changes and bookings as push notifications on the installed Member App. */
export function PushCard() {
  const cfg = useGet<{ enabled: boolean; publicKey: string }>('/api/v1/platform/push-config', { retry: false });
  const subs = useGet<Page<Row>>('/api/v1/platform/push-subscriptions', { retry: false });
  const [on, setOn] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const supported = typeof window !== 'undefined' && 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;
  useEffect(() => {
    if (!supported) return;
    void navigator.serviceWorker.getRegistration().then((r) => r?.pushManager.getSubscription()).then((x) => setOn(Boolean(x)));
  }, [supported]);
  if (!supported || !cfg.data?.enabled) return null;
  const toggle = async () => {
    setBusy(true);
    setErr(null);
    try {
      const reg = await navigator.serviceWorker.ready;
      const cur = await reg.pushManager.getSubscription();
      if (cur) {
        const row = (subs.data?.items ?? []).find((x) => cur.endpoint.includes(String(x.service)));
        if (row) await request('DELETE', `/api/v1/platform/push-subscriptions/${row.id}`);
        await cur.unsubscribe();
        setOn(false);
      } else {
        if ((await Notification.requestPermission()) !== 'granted') throw new Error('Notifications are blocked for this app in the phone settings.');
        const x = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64ToBytes(cfg.data!.publicKey) });
        await request('POST', '/api/v1/platform/push-subscriptions', { endpoint: x.endpoint, surface: 'member', userAgent: navigator.userAgent.slice(0, 300),
          keys: { p256dh: bytesToB64(x.getKey('p256dh')), auth: bytesToB64(x.getKey('auth')) } });
        setOn(true);
      }
      await subs.refetch();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title="Notifications on this phone" icon="notifications">
      <ErrorAlert error={err} />
      <p className="oc-muted">{on ? 'Offers and club news arrive on this phone.' : 'Get personal offers and booking reminders on this phone.'}</p>
      <button className={`oc-btn ${on ? 'oc-btn-neutral' : 'oc-btn-ink'}`} style={{ minHeight: 44 }} disabled={busy} onClick={() => void toggle()}>
        {on ? 'Turn off' : 'Turn on notifications'}</button>
    </Card>
  );
}
