import { useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import {
  Amount, Avatar, Card, DashCard, DashGrid, DashIcon, DataTable, ErrorAlert, PageHeader, RankBars, SearchBox, Skeleton, SplitStats, StatusPill,
  useAuth, useDebounced, useToast,
} from '@oneclub/shell';
import { ActionButton, KV, money, type R } from './common';
import { CustomerTierBadge } from '../p5/tiers';
import './customer360.css';

// Customer 360 / Member 360 (FR-CUS-03/04): who the customer is, the
// membership, how they use the club, what the relationship is worth, what is
// coming (tee times, follow-ups) and the history. Without a customer it lists
// the customers to pick from.

const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '').replace(/_/g, '-')} />;
const LINES: Record<string, [string, string]> = {
  golf: ['Golf', 'var(--dash-blue)'], pos: ['F&B', 'var(--dash-lime)'], sportclub: ['Sport club', 'var(--dash-sky)'], stay: ['Bungalow & venue', 'var(--dash-amber)'],
  banquet: ['Events & banquet', 'var(--dash-ink)'], membership: ['Membership', 'var(--dash-green)'], package: ['Packages', 'var(--dash-red)'],
};
const RFM: Record<string, string> = { champions: 'VIP / High value', loyal: 'Loyal', potential: 'Potential', new: 'New', at_risk: 'At risk', lapsed: 'Dormant' };
const PREF: Record<string, string> = { tee_time: 'schedule', golf_cart: 'golf_course', favorite_caddy: 'person', food: 'restaurant', beverage: 'local_cafe',
  facility: 'meeting_room', diet: 'restaurant', allergy: 'warning', note: 'sticky_note_2' };
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase());
const ago = (iso: unknown) => {
  if (!iso) return '—';
  const d = Math.max(0, Math.round((Date.now() - new Date(String(iso)).getTime()) / 86400000));
  return d === 0 ? 'today' : d === 1 ? 'yesterday' : `${formatNumber(d)} days ago`;
};

export function Customer360Page() {
  const [params, setParams] = useSearchParams();
  const id = params.get('id');
  return id ? <Customer360 id={id} onSearch={() => setParams({})} /> : <CustomerPicker onPick={(c) => setParams({ id: c })} />;
}

/** The customers to open (the most recent first), searchable by name, phone or e-mail. */
function CustomerPicker({ onPick }: { onPick: (id: string) => void }) {
  const [q, setQ] = useState('');
  const query = useDebounced(q);
  const list = useGet<Page<R>>(`/api/v1/crm/customers${qs({ q: query, limit: '200' })}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Customer 360" help="Profile, membership, engagement, value, bookings and follow-ups of a customer in one page." />
      <SearchBox value={q} onChange={setQ} placeholder="Search customer by name, phone or e-mail" />
      <ErrorAlert error={list.error} />
      <DataTable rows={list.data?.items} loading={list.isLoading} onRowClick={(r) => onPick(r.id)} columns={[
        { key: 'name', header: 'Customer', render: (r) => <span className="oc-row" style={{ gap: 10 }}><Avatar name={String(r.name)} tone="white" />
          <span>{String(r.name)}<div className="oc-small oc-muted">{String(r.code)}</div></span></span> },
        { key: 'customerType', header: 'Type', render: (r) => label(r.customerType) },
        { key: 'phone', header: 'Phone' }, { key: 'email', header: 'E-mail' }, { key: 'city', header: 'City' },
        { key: 'status', header: 'Status', render: pill('status') }]} />
    </div>
  );
}

function Customer360({ id, onSearch }: { id: string; onSearch: () => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const ov = useGet<R & { profile: R; memberships: R[]; accounts: R[]; relationships: R[]; preferences: R[]; stats: R; recentHistory: R[]; relationship?: R }>(
    `/api/v1/crm/customers/${id}/overview`);
  const exp = useSend<Record<string, unknown>, R>('POST', `/api/v1/crm/customers/${id}:export-personal-data`, ['/api/v1/crm']);
  const x = ov.data;
  const rel = (x?.relationship ?? {}) as R & { lines?: R[]; upcomingBookings?: R[]; tasks?: R[] };
  const lines = rel.lines ?? [];
  const active = x?.memberships.find((m) => m.status === 'active') ?? x?.memberships[0];
  const daysLeft = active?.endsOn ? Math.round((new Date(String(active.endsOn)).getTime() - Date.now()) / 86400000) : null;
  return (
    <div className="oc-stack">
      <PageHeader title={x ? String(x.profile.name) : 'Customer 360'} help={x ? `${String(x.profile.code)} · ${String(x.profile.phone ?? '')}` : undefined}
        actions={<>
          <CustomerTierBadge customerId={id} />
          <button className="oc-btn oc-btn-neutral" onClick={onSearch}>All customers</button>
          {can('crm.customer.view') && <Link className="oc-btn oc-btn-outline" to={`/crm/customers/${id}`}>View all business lines</Link>}
          {can('crm.customer.export_personal_data') && <button className="oc-btn oc-btn-neutral" disabled={exp.isPending}
            onClick={() => exp.mutate({}, { onSuccess: (r) => { toast('Personal data exported'); window.open(String((r.file as R).url), '_blank'); } })}>Export personal data</button>}
          {can('crm.customer.erase') && <ActionButton label="Erase personal data" path={`/api/v1/crm/customers/${id}:erase`} invalidate={['/api/v1/crm']} reason="required" danger
            confirm="Identifiers are anonymised; financial records keep their amounts. This cannot be undone." />}
        </>} />
      {ov.isLoading && <Skeleton rows={8} />}
      <ErrorAlert error={ov.error ?? exp.error} />
      {x && (
        <>
          <DashGrid>
            {/* Who: identity, membership and the headline figures. */}
            <DashCard span={8}>
              <div className="oc-c360-hero">
                <Avatar name={String(x.profile.name)} tone="dark" />
                <div>
                  <h2>{String(x.profile.name)}</h2>
                  <div className="oc-row-wrap">
                    {active && <span className="oc-dash-tag">{String(active.typeName)}</span>}
                    {active ? <StatusPill status={String(active.status)} /> : <span className="oc-dash-tag">Non-member</span>}
                    {!!rel.rfmGroup && <span className="oc-dash-move" data-tone="new">{RFM[String(rel.rfmGroup)] ?? label(rel.rfmGroup)}</span>}
                  </div>
                </div>
              </div>
              <SplitStats items={[
                { label: 'Golf rounds', value: formatNumber(Number(x.stats.rounds ?? 0)), color: 'var(--dash-blue)' },
                { label: 'Last activity', value: ago(rel.lastActivity ?? x.stats.lastVisit), color: 'var(--dash-lime)' },
                { label: 'Total payments', value: money(x.stats.totalPayments), color: 'var(--dash-ink)' },
                { label: 'Handicap index', value: x.handicapIndex ? String(x.handicapIndex) : '—', color: 'var(--dash-sky)' },
              ]} />
            </DashCard>
            {/* Value. */}
            <DashCard span={4} variant="dark" icon="diamond" tone="blue" title="Relationship value">
              <Amount text={money(rel.clv ?? 0)} size="lg" />
              <span className="oc-c360-dim">Lifetime value · spend {money(rel.spend ?? 0)}</span>
              <div className="oc-c360-facts">
                <div><span>Guests brought</span><strong>{formatNumber(Number(rel.guests ?? 0))}</strong></div>
                <div><span>Interactions</span><strong>{formatNumber(Number(rel.interactions ?? 0))}</strong></div>
                <div><span>Upcoming tee times</span><strong>{formatNumber(rel.upcomingBookings?.length ?? 0)}</strong></div>
                <div><span>Open follow-ups</span><strong>{formatNumber(rel.tasks?.length ?? 0)}</strong></div>
              </div>
            </DashCard>
          </DashGrid>

          <DashGrid>
            <DashCard span={4} icon="card_membership" tone="green" title="Membership">
              {x.memberships.length === 0 ? <p className="oc-dash-empty">Not a member.</p> : x.memberships.map((m) => (
                <div key={String(m.membershipId)} className="oc-c360-row">
                  <span><strong>{String(m.typeName)}</strong><small>{String(m.memberNo)} · {label(m.role)}</small></span>
                  <span><StatusPill status={String(m.status)} /><small>{m.endsOn ? `Ends ${formatDate(String(m.endsOn))}` : 'No end date'}</small></span>
                </div>
              ))}
              {daysLeft != null && active?.status === 'active' && (
                <span className="oc-dash-move" data-tone={daysLeft <= 30 ? 'down' : daysLeft <= 90 ? 'same' : 'up'} style={{ alignSelf: 'flex-start' }}>
                  {daysLeft <= 0 ? 'Ends today' : `${formatNumber(daysLeft)} days left`}
                </span>
              )}
            </DashCard>
            <DashCard span={4} icon="local_activity" tone="blue" title="Engagement">
              {lines.length === 0 ? <p className="oc-dash-empty">No activity yet.</p> : (
                <RankBars ranked={false} format={(v) => `${formatNumber(v)} days`} rows={lines.map((l, i) => ({
                  key: String(l.line), rank: i + 1, name: LINES[String(l.line)]?.[0] ?? label(l.line),
                  value: `${formatNumber(Number(l.frequency))} visits · ${money(l.monetary)}`, total: Number(l.frequency),
                  parts: [{ label: String(l.line), value: Number(l.frequency), color: LINES[String(l.line)]?.[1] ?? 'var(--dash-grey)' }] }))} />
              )}
            </DashCard>
            <DashCard span={4} icon="event_upcoming" title="Coming up">
              {(rel.upcomingBookings ?? []).length === 0 && (rel.tasks ?? []).length === 0 && <p className="oc-dash-empty">Nothing scheduled.</p>}
              {(rel.upcomingBookings ?? []).map((b) => (
                <div key={String(b.bookingId)} className="oc-c360-item"><DashIcon name="golf_course" tone="blue" />
                  <span><strong>{b.startAt ? formatDateTime(String(b.startAt)) : formatDate(String(b.playDate))}</strong>
                    <small>{String(b.code)} · {String(b.courseName)} · {formatNumber(Number(b.players))} players</small></span></div>
              ))}
              {(rel.tasks ?? []).map((t) => (
                <div key={String(t.id)} className="oc-c360-item"><DashIcon name="task_alt" tone="dark" />
                  <span><strong>{String(t.subject)}</strong><small>{label(t.type)}{t.dueAt ? ` · due ${formatDate(String(t.dueAt))}` : ''}</small></span></div>
              ))}
            </DashCard>
          </DashGrid>

          <DashGrid>
            <DashCard span={6} icon="person" title="Profile">
              <KV items={[['Gender', label(x.profile.gender ?? '—')], ['Date of Birth', x.profile.birthDate ? formatDate(String(x.profile.birthDate)) : '—'],
                ['E-mail', String(x.profile.email ?? '—')], ['ID Number', String(x.profile.idNumber ?? '—')], ['City', String(x.profile.city ?? '—')],
                ['Consent', x.profile.consentAt ? formatDateTime(String(x.profile.consentAt)) : '—'],
                ['Last interaction', rel.lastInteraction ? formatDateTime(String(rel.lastInteraction)) : '—'], ['Status', <StatusPill key="s" status={String(x.profile.status)} />]]} />
            </DashCard>
            <DashCard span={6} icon="favorite" tone="red" title="Preferences & family">
              {x.preferences.length === 0 ? <p className="oc-dash-empty">No preference recorded.</p> : (
                <div className="oc-c360-prefs">
                  {x.preferences.map((p) => (
                    <div key={`${String(p.category)}-${String(p.key)}`}><DashIcon name={PREF[String(p.category)] ?? 'sticky_note_2'} />
                      <span><small>{label(p.category)}</small><strong>{String(p.value ?? p.key)}</strong></span></div>
                  ))}
                </div>
              )}
              {x.relationships.length > 0 && (
                <div className="oc-row-wrap">
                  {x.relationships.map((r) => (
                    <Link key={String(r.customerId)} className="oc-chip" to={`/crm/customer-360?id=${String(r.customerId)}`}>{String(r.name)} · {label(r.relationship)}</Link>
                  ))}
                </div>
              )}
            </DashCard>
          </DashGrid>

          {x.accounts.length > 0 && (
            <Card title="Accounts" icon="account_balance_wallet">
              <DataTable rows={x.accounts} rowKey={(a) => String(a.accountId)} columns={[{ key: 'number', header: 'Account' }, { key: 'accountType', header: 'Type', render: (a) => label(a.accountType) },
                { key: 'balance', header: 'Balance', align: 'right', render: (a) => money(a.balance) }, { key: 'creditLimit', header: 'Credit limit', align: 'right', render: (a) => money(a.creditLimit) }]} />
            </Card>
          )}
          <Card title="Customer History" icon="history">
            <DataTable rows={x.recentHistory} rowKey={(h) => `${String(h.kind)}-${String(h.reference)}-${String(h.occurredAt)}`}
              columns={[{ key: 'occurredAt', header: 'When', render: (h) => formatDateTime(String(h.occurredAt)) }, { key: 'kind', header: 'Kind', render: (h) => label(h.kind) },
                { key: 'reference', header: 'Reference' }, { key: 'description', header: 'Description' }, { key: 'amount', header: 'Amount', align: 'right', render: (h) => money(h.amount) },
                { key: 'status', header: 'Status', render: pill('status') }]} />
          </Card>
        </>
      )}
    </div>
  );
}
