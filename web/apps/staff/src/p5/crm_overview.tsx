import React, { useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router';
import { qs, useGet, type Page } from '@oneclub/api-client';
import { currentLocale, formatDate, formatNumber } from '@oneclub/i18n';
import {
  Amount, Avatar, BreakdownList, ColumnChart, DashButton, DashCard, DashGrid, DashHead, DashIcon, DashTable, DataTable, Drawer, ErrorAlert, MiniCard, Note,
  PillSelect, RankBars, SegmentBar, Skeleton, SplitStats, StatusPill, type DashColumn,
} from '@oneclub/shell';
import { money, moneyShort, type R } from '../p1/common';
import './crm_overview.css';

// CRM Dashboard Overview: the customer / member relationship (who the
// members are, how active and engaged, what the relationship is worth and who
// needs a follow-up). Golf operations and revenue stay on their dashboards.
// Data: GET /crm/dashboard (reporting read models + RFM store).

interface Count { key: string; count: number }
interface Member {
  customerId: string; code: string; name: string; typeName: string; visits: number; value: number; clv: number;
  lastVisit: string | null; recencyDays: number | null; endsOn: string | null; reason?: string; action?: string;
}
interface Dash {
  from: string; to: string; today: string; types: string[];
  thresholds: { activeDays: number; occasionalDays: number; expiringDays: number };
  kpis: { totalMembers: number; newMembers: number; newMembersPrev: number; activeMembers: number; inactiveMembers: number; expiring: number;
    nonMembers: number; retention: number | null; avgClv: number };
  growth: { month: string; members: number; new: number; renewed: number; expired: number; cancelled: number; net: number }[];
  segments: Count[]; activity: Count[]; engagement: Count[]; byType: Count[]; byStatus: Count[];
  renewal: { due: number; renewed: number; pending: number; expired: number };
  expiry: { d30: number; d60: number; d90: number };
  acquisition: { newCustomers: number; sources: Count[] };
  guests: { visits: number; unique: number; repeat: number; converted: number; topReferrers: { customerId: string; name: string; guests: number }[] };
  highValue: Member[];
  atRisk: { total: number; noVisit: number; declining: number; expiring: number; members: Member[] };
  upcoming: { expiring: number; birthdays: number; vipArrivals: number; events: number; followUps: number };
  followUps: { dueToday: number; overdue: number; upcoming: number; items: { id: string; type: string; subject: string; customerId: string | null; customerName: string | null; dueAt: string | null }[] };
}

const C = { ink: 'var(--dash-ink)', blue: 'var(--dash-blue)', lime: 'var(--dash-lime)', sky: 'var(--dash-sky)', amber: 'var(--dash-amber)',
  green: 'var(--dash-green)', red: 'var(--dash-red)', grey: 'var(--dash-grey)' };
const SEGMENTS: Record<string, [string, string]> = { active: ['Active', C.green], occasional: ['Occasional', C.sky], inactive: ['Inactive', C.grey], expiring: ['Expiring', C.red] };
const ACTIVITIES: Record<string, [string, string]> = {
  golf_round: ['Golf rounds', C.blue], tee_time_booking: ['Tee time bookings', C.sky], fnb: ['F&B visits', C.lime], sport: ['Sport club', C.amber],
  bungalow_stay: ['Bungalow stays', C.ink], event: ['Event registrations', C.green],
};
const RFM: Record<string, [string, string, string]> = {
  champions: ['VIP / High value', C.ink, 'Frequent and high spend'], loyal: ['Loyal', C.blue, 'Come often'], potential: ['Potential', C.sky, 'Growing'],
  new: ['New', C.lime, 'Recently joined or first visits'], at_risk: ['At risk', C.amber, 'Were active, now declining'], lapsed: ['Dormant', C.red, 'Long without activity'],
  no_score: ['Not scored yet', C.grey, 'No activity to score'],
};
const STATUS: Record<string, string> = { active: C.green, expiring: C.amber, expired: C.red, suspended: C.ink, cancelled: C.grey };
const SOURCES: Record<string, string> = { member_referral: 'Member referral', website_form: 'Website', walk_in: 'Walk-in', whatsapp: 'WhatsApp', instagram: 'Instagram',
  phone: 'Phone', event: 'Event', direct: 'Direct (no lead)' };
const label = (v: string) => v.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase());
const pct = (n: number, of: number) => (of > 0 ? n / of : null);
const days = (n: number | null) => (n == null ? '—' : n === 0 ? 'today' : n === 1 ? 'yesterday' : `${formatNumber(n)} days ago`);

function parts(list: Count[], map: Record<string, readonly [string, string, ...string[]]>) {
  const order = Object.keys(map);
  return [...list].sort((a, b) => order.indexOf(a.key) - order.indexOf(b.key))
    .map((c) => ({ key: c.key, label: map[c.key]?.[0] ?? label(c.key), value: c.count, color: map[c.key]?.[1] ?? C.grey }));
}

export function CRMOverviewPage() {
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const period = params.get('period') ?? 'month';
  const type = params.get('type') ?? '';
  const set = (k: string, v: string) => { const n = new URLSearchParams(params); if (v) n.set(k, v); else n.delete(k); setParams(n, { replace: true }); };
  const d = useGet<Dash>(`/api/v1/crm/dashboard${qs({ period, type })}`);
  const [list, setList] = useState<{ list: string; days?: number; title: string } | null>(null);
  const open360 = (id: string) => nav(`/crm/customer-360?id=${id}`);

  const controls = (
    <>
      <PillSelect label="Period" value={period} onChange={(v) => set('period', v)}
        options={[{ value: 'month', label: 'This month' }, { value: 'quarter', label: 'This quarter' }, { value: 'year', label: 'This year' }]} />
      <PillSelect label="Membership type" value={type} onChange={(v) => set('type', v)}
        options={[{ value: '', label: 'All types' }, ...(d.data?.types ?? []).map((t) => ({ value: t, label: t }))]} />
    </>
  );
  if (d.error) return <div className="oc-dash-page"><DashHead title="CRM Overview" controls={controls} /><ErrorAlert error={d.error} /></div>;
  if (!d.data) return <div className="oc-dash-page"><DashHead title="CRM Overview" controls={controls} /><Skeleton rows={10} /></div>;
  const x = d.data;
  const k = x.kpis;
  const th = x.thresholds;
  const seg = parts(x.segments, SEGMENTS);
  const segTotal = seg.reduce((s, p) => s + p.value, 0);
  const act = parts(x.activity, ACTIVITIES);
  const actTotal = act.reduce((s, p) => s + p.value, 0);
  const eng = parts(x.engagement, RFM);
  const engTotal = eng.reduce((s, p) => s + p.value, 0);
  const typeTotal = x.byType.reduce((s, p) => s + p.count, 0);
  const src = x.acquisition.sources.map((s, i) => ({ label: SOURCES[s.key] ?? label(s.key), value: s.count, color: [C.blue, C.ink, C.lime, C.sky, C.amber, C.green, C.red][i % 7] }));
  const newDelta = k.newMembersPrev > 0 ? (k.newMembers - k.newMembersPrev) / k.newMembersPrev : null;
  const fmtMonth = (m: string) => new Intl.DateTimeFormat(currentLocale() === 'en' ? 'en-US' : 'id-ID', { month: 'short' }).format(new Date(`${m}-01T00:00:00`));
  const growth = x.growth;
  const last = growth[growth.length - 1];

  const memberCols: DashColumn<Member>[] = [
    { key: 'name', header: 'Member', render: (m) => <span className="oc-row" style={{ gap: 10 }}><Avatar name={m.name} tone="white" /><span>{m.name}<div className="oc-small oc-muted">{m.code} · {m.typeName}</div></span></span> },
    { key: 'visits', header: 'Activity', align: 'right', render: (m) => `${formatNumber(m.visits)} days` },
    { key: 'value', header: 'Value', align: 'right', render: (m) => <strong>{moneyShort(m.clv)}</strong> },
    { key: 'last', header: 'Last visit', render: (m) => days(m.recencyDays) },
  ];

  return (
    <div className="oc-dash-page">
      <DashHead title="CRM Overview" sub={`Members and customers · ${formatDate(x.from)} – ${formatDate(x.to)}`} controls={controls} />

      {/* 1 · KPIs */}
      <DashGrid>
        <MiniCard span={3} label="Total members" value={<Amount text={formatNumber(k.totalMembers)} size="md" />} />
        <MiniCard span={3} label="New members" value={<Amount text={formatNumber(k.newMembers)} size="md" />}
          delta={newDelta != null ? <span className="oc-dash-chip" data-good={newDelta >= 0}>{newDelta >= 0 ? '+' : '−'}{Math.abs(newDelta * 100).toFixed(0)}%</span> : undefined} />
        <MiniCard span={3} label="Active members" value={<Amount text={formatNumber(k.activeMembers)} size="md" />}
          delta={<span className="oc-dash-chip">{((pct(k.activeMembers, k.totalMembers) ?? 0) * 100).toFixed(0)}%</span>} />
        <MiniCard span={3} label="Inactive members" value={<Amount text={formatNumber(k.inactiveMembers)} size="md" />}
          delta={k.inactiveMembers > 0 ? <span className="oc-dash-chip" data-good="false">no activity</span> : undefined} />
        <MiniCard span={3} label={`Expiring ≤ ${th.expiringDays} days`} value={<Amount text={formatNumber(k.expiring)} size="md" />}
          delta={k.expiring > 0 ? <span className="oc-dash-chip" data-good="false">renew</span> : undefined} />
        <MiniCard span={3} label="Guest / non-member" value={<Amount text={formatNumber(k.nonMembers)} size="md" />}
          delta={<span className="oc-dash-chip-suffix">transacted</span>} />
        <MiniCard span={3} label="Member retention" value={<Amount text={k.retention == null ? '—' : `${(k.retention * 100).toFixed(1)}%`} size="md" />}
          delta={<span className="oc-dash-chip-suffix">12 months</span>} />
        <MiniCard span={3} label="Avg. lifetime value" value={<Amount text={moneyShort(k.avgClv)} size="md" />} />
      </DashGrid>

      {/* 2 · Growth  ·  3 · Segmentation */}
      <DashGrid>
        <DashCard span={8} icon="trending_up" tone="blue" title="Member growth">
          <div className="oc-dash-hero">
            <div>
              <Amount text={formatNumber(last?.members ?? 0)} size="lg" />
              <Note>Net <b>{(last?.net ?? 0) >= 0 ? '+' : ''}{formatNumber(last?.net ?? 0)}</b> this month</Note>
            </div>
          </div>
          <ColumnChart aLabel="Members" bLabel="New" format={(v) => formatNumber(v)}
            points={growth.map((g, i) => ({ label: fmtMonth(g.month), a: g.members, b: g.new, state: i === growth.length - 1 ? 'current' as const : 'past' as const,
              title: `${fmtMonth(g.month)} · renewed ${g.renewed} · expired ${g.expired}` }))} />
          <SplitStats items={[
            { label: 'New', value: formatNumber(growth.reduce((s, g) => s + g.new, 0)), color: C.blue },
            { label: 'Renewed', value: formatNumber(growth.reduce((s, g) => s + g.renewed, 0)), color: C.green },
            { label: 'Expired', value: formatNumber(growth.reduce((s, g) => s + g.expired, 0)), color: C.red },
            { label: 'Cancelled', value: formatNumber(growth.reduce((s, g) => s + g.cancelled, 0)), color: C.grey },
          ]} />
        </DashCard>
        <DashCard span={4} icon="donut_large" title="Member segment">
          <SegmentBar parts={seg} format={(v) => `${formatNumber(v)} members`} legend={false} />
          <BreakdownList rows={seg.map((p) => ({ label: p.label, value: formatNumber(p.value), share: pct(p.value, segTotal), color: p.color }))} />
          <p className="oc-dash-sub">Active: activity ≤ {th.activeDays} days · Occasional: last activity ≤ {th.occasionalDays} days · Inactive: none for longer ·
            Expiring: membership ends ≤ {th.expiringDays} days.</p>
        </DashCard>
      </DashGrid>

      {/* 4 · Activity  ·  5 · Engagement */}
      <DashGrid>
        <DashCard span={6} icon="local_activity" title="Member activity">
          <Note>What the members use the club for · <b>{formatNumber(actTotal)}</b> activities</Note>
          <RankBars ranked={false} format={(v) => formatNumber(v)} rows={act.map((p, i) => ({ key: p.key, rank: i + 1, name: p.label, value: formatNumber(p.value), total: p.value,
            parts: [{ label: p.label, value: p.value, color: p.color }] }))} />
        </DashCard>
        <DashCard span={6} icon="favorite" tone="red" title="Member engagement (RFM)">
          <SegmentBar parts={eng} format={(v) => `${formatNumber(v)} members`} legend={false} />
          <BreakdownList rows={eng.map((p) => ({ label: `${p.label} — ${RFM[p.key]?.[2] ?? ''}`, value: formatNumber(p.value), share: pct(p.value, engTotal), color: p.color }))} />
        </DashCard>
      </DashGrid>

      {/* 6 · Membership  ·  7 · Expiry */}
      <DashGrid>
        <DashCard span={4} icon="card_membership" title="Membership overview">
          <SegmentBar parts={x.byType.map((t, i) => ({ label: t.key, value: t.count, color: [C.ink, C.blue, C.lime, C.sky, C.amber][i % 5] }))} format={(v) => formatNumber(v)} legend={false} />
          <BreakdownList rows={x.byType.map((t, i) => ({ label: t.key, value: formatNumber(t.count), share: pct(t.count, typeTotal), color: [C.ink, C.blue, C.lime, C.sky, C.amber][i % 5] }))} />
          <SplitStats items={x.byStatus.map((s) => ({ label: label(s.key), value: formatNumber(s.count), color: STATUS[s.key] ?? C.grey }))} />
        </DashCard>
        <DashCard span={4} variant="dark" icon="autorenew" tone="green" title="Renewal this month">
          <div className="oc-crm-renewal">
            {[['Due this month', x.renewal.due], ['Renewed', x.renewal.renewed], ['Pending', x.renewal.pending], ['Expired', x.renewal.expired]].map(([l, v]) => (
              <div key={String(l)}><span>{l}</span><strong>{formatNumber(Number(v))}</strong></div>
            ))}
          </div>
        </DashCard>
        <DashCard span={4}>
          <div className="oc-dash-inset">
            <div className="oc-dash-card-head"><DashIcon name="event_busy" tone="red" /><h2>Membership expiring soon</h2></div>
            <div className="oc-crm-expiry">
              {([['30', x.expiry.d30], ['60', x.expiry.d60], ['90', x.expiry.d90]] as const).map(([n, v]) => (
                <button key={n} type="button" onClick={() => setList({ list: 'expiring', days: Number(n), title: `Membership expiring in ${n} days` })}>
                  <strong>{formatNumber(v)}</strong><span>Next {n} days</span>
                </button>
              ))}
            </div>
          </div>
          <div className="oc-dash-actions">
            <DashButton tone="blue" icon="groups" onClick={() => setList({ list: 'expiring', days: 90, title: 'Membership expiring in 90 days' })}>View members</DashButton>
          </div>
        </DashCard>
      </DashGrid>

      {/* 8 · Acquisition  ·  9 · Guests */}
      <DashGrid>
        <DashCard span={6} icon="person_add" tone="blue" title="Customer acquisition">
          <div className="oc-dash-figure"><Amount text={formatNumber(x.acquisition.newCustomers)} size="lg" /><span className="oc-dash-chip-suffix">new customers in the period</span></div>
          {src.length > 0 && <SegmentBar parts={src} format={(v) => `${formatNumber(v)} customers`} legend={false} />}
          <BreakdownList rows={src.map((s) => ({ label: s.label, value: formatNumber(s.value), share: pct(s.value, x.acquisition.newCustomers), color: s.color }))} />
        </DashCard>
        <DashCard span={6} icon="group_add" title="Member guests">
          <SplitStats items={[
            { label: 'Guest visits', value: formatNumber(x.guests.visits), color: C.blue },
            { label: 'Unique guests', value: formatNumber(x.guests.unique), color: C.sky },
            { label: 'Repeat guests', value: formatNumber(x.guests.repeat), color: C.lime },
            { label: 'Guest → member', value: formatNumber(x.guests.converted), color: C.green },
          ]} />
          <h3 className="oc-crm-sub">Top member referrers</h3>
          {x.guests.topReferrers.length === 0 ? <p className="oc-dash-empty">No member brought a guest in this period.</p> : (
            <RankBars format={(v) => `${formatNumber(v)} guests`} rows={x.guests.topReferrers.map((r, i) => ({ key: r.customerId, rank: i + 1, name: r.name,
              value: `${formatNumber(r.guests)} guests`, total: r.guests, onOpen: () => open360(r.customerId) }))} />
          )}
        </DashCard>
      </DashGrid>

      {/* 10 · High value  ·  11 · At risk */}
      <DashGrid>
        <DashTable<Member> span={7} icon="workspace_premium" title="High value members" rows={x.highValue} rowKey={(m) => m.customerId} columns={memberCols}
          onRow={(m) => open360(m.customerId)} empty="No member scored yet."
          action={<DashButton tone="grey" onClick={() => setList({ list: 'high_value', title: 'Members by lifetime value' })}>View all</DashButton>} />
        <DashCard span={5}>
          <div className="oc-dash-inset">
            <div className="oc-dash-card-head"><DashIcon name="warning" tone="red" /><h2>Members at risk</h2></div>
            <Amount text={formatNumber(x.atRisk.total)} size="lg" />
            <span className="oc-dash-sub">members need a retention action</span>
          </div>
          <SplitStats items={[
            { label: `No visit > ${th.activeDays} d`, value: formatNumber(x.atRisk.noVisit), color: C.red },
            { label: 'Declining', value: formatNumber(x.atRisk.declining), color: C.amber },
            { label: 'Expiring', value: formatNumber(x.atRisk.expiring), color: C.ink },
          ]} />
          <div className="oc-crm-risk">
            {x.atRisk.members.slice(0, 4).map((m) => (
              <button key={m.customerId} type="button" onClick={() => open360(m.customerId)}>
                <Avatar name={m.name} tone="white" />
                <span><strong>{m.name}</strong><small>Last visit {days(m.recencyDays)}{m.endsOn ? ` · ends ${formatDate(m.endsOn)}` : ''}</small></span>
                <span className="oc-dash-move" data-tone={m.reason === 'expiring' ? 'new' : 'down'}>{m.action}</span>
              </button>
            ))}
          </div>
          <div className="oc-dash-actions">
            <DashButton tone="dark" icon="groups" onClick={() => setList({ list: 'at_risk', title: 'Members at risk' })}>View members</DashButton>
          </div>
        </DashCard>
      </DashGrid>

      {/* 12 · Upcoming  ·  13 · Follow-ups */}
      <DashGrid>
        <DashCard span={4} icon="event_upcoming" title="Upcoming">
          <div className="oc-crm-upcoming">
            {([['event_busy', 'Membership expiry', x.upcoming.expiring, '30 days'], ['cake', 'Member birthdays', x.upcoming.birthdays, '30 days'],
              ['workspace_premium', 'VIP arrivals', x.upcoming.vipArrivals, '7 days'], ['celebration', 'Event registrations', x.upcoming.events, '30 days'],
              ['task_alt', 'Follow-ups due', x.upcoming.followUps, '7 days']] as const).map(([icon, l, v, w]) => (
              <div key={l}><DashIcon name={icon} /><span>{l}<small>next {w}</small></span><strong>{formatNumber(v)}</strong></div>
            ))}
          </div>
        </DashCard>
        <DashCard span={8} icon="task_alt" tone="dark" title="My follow-ups"
          action={<DashButton tone="grey" onClick={() => nav('/crm/follow-ups')}>Open follow-ups</DashButton>}>
          <SplitStats items={[
            { label: 'Due today', value: formatNumber(x.followUps.dueToday), color: C.blue },
            { label: 'Overdue', value: formatNumber(x.followUps.overdue), color: C.red },
            { label: 'Upcoming', value: formatNumber(x.followUps.upcoming), color: C.lime },
          ]} />
          {x.followUps.items.length === 0 ? <p className="oc-dash-empty">No open follow-up.</p> : (
            <div className="oc-crm-followups">
              {x.followUps.items.map((f) => {
                const overdue = !!f.dueAt && f.dueAt.slice(0, 10) < x.today.slice(0, 10);
                return (
                  <button key={f.id} type="button" onClick={() => (f.customerId ? open360(f.customerId) : nav('/crm/follow-ups'))}>
                    <DashIcon name={f.type === 'call' ? 'call' : f.type === 'email' ? 'email' : f.type === 'meeting' ? 'groups' : 'chat'} tone={overdue ? 'red' : 'plain'} />
                    <span><strong>{f.customerName ?? '—'}</strong><small>{f.subject}</small></span>
                    <span className="oc-dash-move" data-tone={overdue ? 'down' : 'same'}>{f.dueAt ? formatDate(f.dueAt) : '—'}</span>
                  </button>
                );
              })}
            </div>
          )}
        </DashCard>
      </DashGrid>

      {list && <MemberListDrawer {...list} type={type} onClose={() => setList(null)} onOpen={open360} />}
    </div>
  );
}

/** The members behind a widget (expiring, at risk, by value), each opening Customer 360. */
function MemberListDrawer({ list, days: within, title, type, onClose, onOpen }: { list: string; days?: number; title: string; type: string; onClose: () => void; onOpen: (id: string) => void }) {
  const q = useGet<Page<R>>(`/api/v1/crm/dashboard/members${qs({ list, days: within ? String(within) : '', type })}`);
  return (
    <Drawer open title={title} onClose={onClose}>
      <ErrorAlert error={q.error} />
      <DataTable rows={(q.data?.items ?? []).map((m) => ({ ...m, id: String(m.customerId) })) as R[]} loading={q.isLoading} onRowClick={(m) => onOpen(String(m.customerId))}
        columns={[
          { key: 'name', header: 'Member', render: (m) => <>{String(m.name)}<div className="oc-small oc-muted">{String(m.code)} · {String(m.typeName)}</div></> },
          { key: 'recencyDays', header: 'Last visit', render: (m) => days(m.recencyDays == null ? null : Number(m.recencyDays)) },
          { key: 'endsOn', header: 'Ends', render: (m) => (m.endsOn ? formatDate(String(m.endsOn)) : '—') },
          { key: 'clv', header: 'Value', align: 'right', render: (m) => money(m.clv) },
          ...(list === 'at_risk' ? [{ key: 'action', header: 'Action', render: (m: R) => <StatusPill status={String(m.reason)} label={String(m.action)} tone={m.reason === 'expiring' ? 'warning' : 'error'} /> }] : []),
        ]} />
    </Drawer>
  );
}
