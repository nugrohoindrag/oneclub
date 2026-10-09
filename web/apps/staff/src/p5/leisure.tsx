import React, { useState } from 'react';
import { useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import {
  AutoResourcePage, Card, Checkbox, DataTable, Empty, ErrorAlert, Modal, MoneyField, PageHeader, SelectField, Skeleton, StatusPill, TextArea,
  TextField, useAuth, useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import type { AreaRoute, OpsRoute, OpsTile } from '../p3/types';

// PRD P5 — advanced package and tournament (EP-22–23). Back Office routes, ops tiles and ops routes of the area
// (registered in p3/index.tsx and ops/p3.tsx).
//
// Commercial → Packages: Capacity (capacity calendar with allotments, blackouts and time blocks, component rules,
// inventory requirement of the quota, payment templates) and Profitability (allocated revenue vs COGS and direct costs,
// cost rules). Golf → Tournaments: Series (Order of Merit, points tables, import of past seasons), Team Formats (formats,
// tournament teams, registration categories & early-bird) and Tournament History (archive, players, champions, federation).
// Ops: Tournament Desk → Team Scoring (one-ball team card, touch-first).

type Body = Record<string, unknown>;

const COM = '/api/v1/commercial';
const GOLF = '/api/v1/golf';
const TRN = `${GOLF}/tournaments`;
const SERIES = `${GOLF}/tournament-series`;
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const opts = (vals: string[], labels: Record<string, string> = {}): Option[] => vals.map((v) => ({ value: v, label: labels[v] ?? label(v) }));
const list = <T,>(v: unknown) => (Array.isArray(v) ? (v as T[]) : []);
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} />;
const addDays = (d: string, n: number) => {
  const x = new Date(`${d}T00:00:00`);
  x.setDate(x.getDate() + n);
  return `${x.getFullYear()}-${String(x.getMonth() + 1).padStart(2, '0')}-${String(x.getDate()).padStart(2, '0')}`;
};

function usePackages() {
  const p = useGet<Page<R>>(`${COM}/packages?limit=200`);
  return (p.data?.items ?? []).map((x) => ({ value: x.id, label: `${String(x.name)} (${String(x.code)})` }));
}

function useTournaments(status = '') {
  const t = useGet<Page<R>>(`${TRN}${qs({ 'filter[status]': status, 'filter[source]': 'oneclub', limit: 200 })}`);
  return (t.data?.items ?? []).map((x) => ({ value: x.id, label: `${String(x.name)} · ${formatDate(String(x.startDate))}` }));
}

function useTab(def: string): [string, (v: string) => void] {
  const [params, setParams] = useSearchParams();
  return [params.get('tab') ?? def, (v: string) => setParams({ tab: v })];
}

// ── Commercial → Packages → Capacity (FR-PKG-P5-01/02/03/05) ──────────────

export function PackageCapacityPage() {
  const [tab, setTab] = useTab('calendar');
  return (
    <div className="oc-stack">
      <PageHeader title="Package Capacity" help="Allotments, blackouts and time blocks per package and component, component rules (choice groups, sequence, service windows), the inventory requirement of large quotas and payment templates per package type." />
      <Tabs tabs={[{ value: 'calendar', label: 'Capacity Calendar' }, { value: 'capacity', label: 'Capacity Rules' }, { value: 'rules', label: 'Component Rules' },
        { value: 'templates', label: 'Payment Templates' }]} value={tab} onChange={setTab} />
      {tab === 'calendar' && <CapacityCalendar />}
      {tab === 'capacity' && <AutoResourcePage resourceKey="commercial.package_capacity" />}
      {tab === 'rules' && <AutoResourcePage resourceKey="commercial.package_component_rule" />}
      {tab === 'templates' && <AutoResourcePage resourceKey="commercial.package_payment_template" />}
    </div>
  );
}

function capText(c: R) {
  if (c.capacityType === 'blackout') return `Blackout${c.reason ? ` · ${String(c.reason)}` : ''}`;
  return `${label(c.capacityType)}: ${String(c.used)}${c.quota != null ? ` of ${String(c.quota)}` : ''} ${label(c.basis)}`;
}

function CapacityCalendar() {
  const packages = usePackages();
  const [pkg, setPkg] = useState('');
  const [date, setDate] = useState(today());
  const [days, setDays] = useState('7');
  const id = pkg || (packages[0]?.value ?? '');
  const cal = useGet<R>(id ? `${COM}/packages/${id}/capacity${qs({ date, days })}` : null);
  const rules = useGet<Page<R>>(id ? `${COM}/packages/${id}/component-rules` : null);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <SelectField label="Package" value={id} onChange={setPkg} options={packages} />
        <TextField label="From" type="date" value={date} onChange={setDate} />
        <SelectField label="Days" value={days} onChange={setDays} options={opts(['7', '14', '31'])} />
      </div>
      <ErrorAlert error={cal.error} />
      {cal.isLoading && <Skeleton />}
      <DataTable rows={list<R>(cal.data?.days)} rowKey={(r) => String(r.date)} columns={[
        { key: 'date', header: 'Date', render: (r) => formatDate(String(r.date)) },
        { key: 'available', header: 'Status', render: (r) => <StatusPill status={r.available ? 'available' : 'unavailable'} label={r.available ? 'Available' : String(r.reason || 'Unavailable')} /> },
        { key: 'bookings', header: 'Bookings / Pax', render: (r) => `${String(r.bookings)} / ${String(r.pax)}${r.dailyQuota != null ? ` (quota ${String(r.dailyQuota)})` : ''}` },
        { key: 'capacities', header: 'Package capacity', render: (r) => list<R>(r.capacities).map(capText).join('; ') || '—' },
        { key: 'components', header: 'Components', render: (r) => (
          <div className="oc-stack" style={{ gap: 4 }}>
            {list<R>(r.components).map((c) => (
              <div key={String(c.componentId)} className="oc-small">
                <strong>{String(c.name)}</strong>{c.choiceGroup ? ` (choice ${String(c.choiceGroup)})` : ''} · {formatDate(String(c.serviceDate))} · booked {String(c.booked)}
                {list<R>(c.capacities).filter((k) => k.capacityType !== 'time_block').map((k) => ` · ${capText(k)}`).join('')}
                {list<R>(c.timeBlocks).length > 0 && <> · blocks {list<R>(c.timeBlocks).map((b) => `${String(b.from)} ${String(b.left)}/${String(b.capacity)}`).join(', ')}</>}
                {!c.available && <> <StatusPill status="unavailable" label="Not available" /></>}
              </div>
            ))}
          </div>) },
        { key: 'largeQuota', header: 'Large quota', render: (r) => (r.largeQuota ? <StatusPill status="warning" label="Check inventory" /> : '—') },
      ]} />
      <Card title="Component rules" icon="rule">
        <DataTable rows={rules.data?.items} loading={rules.isLoading} error={rules.error} columns={[
          { key: 'componentName', header: 'Component' }, { key: 'choiceGroup', header: 'Choice', render: (r) => (r.choiceGroup ? `${String(r.choiceGroup)} (pick ${String(r.choicePick)})` : '—') },
          { key: 'afterComponent', header: 'After', render: (r) => (r.afterComponent ? `${String(r.afterComponent)} +${String(r.minGapMinutes)}${r.maxGapMinutes != null ? `–${String(r.maxGapMinutes)}` : ''} min` : '—') },
          { key: 'windowStart', header: 'Served', render: (r) => (r.windowStart || r.windowEnd ? `${String(r.windowStart ?? '…')}–${String(r.windowEnd ?? '…')}` : '—') },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      </Card>
      {id && <InventoryRequirement pkg={id} date={date} />}
    </div>
  );
}

/** FR-PKG-P5-03: BOM of the quota before the package is sold. */
function InventoryRequirement({ pkg, date }: { pkg: string; date: string }) {
  const [pax, setPax] = useState('');
  const [bookings, setBookings] = useState('');
  const req = useGet<R>(`${COM}/packages/${pkg}/inventory-requirement${qs({ date, pax, bookings })}`);
  const x = req.data;
  return (
    <Card title="Inventory requirement (BOM)" icon="inventory_2">
      <div className="oc-row-wrap">
        <TextField label="Pax per booking" type="number" min={1} value={pax} onChange={setPax} />
        <TextField label="Bookings (default: allotment of the date)" type="number" min={1} value={bookings} onChange={setBookings} />
      </div>
      <ErrorAlert error={req.error} />
      {x && (
        <div className="oc-stack">
          <div className="oc-row-wrap">
            <span>{String(x.bookings)} bookings · {String(x.pax)} pax</span>
            {x.largeQuota ? <StatusPill status="warning" label="Large quota" /> : null}
            {Number(x.shortages) > 0 ? <StatusPill status="rejected" label={`${String(x.shortages)} short`} /> : <StatusPill status="active" label="Stock sufficient" />}
            <span className="oc-spacer" /><strong>Cost {money(x.totalCost)}</strong>
          </div>
          <DataTable rows={list<R>(x.items)} rowKey={(r) => String(r.itemId)} columns={[
            { key: 'itemCode', header: 'Item' }, { key: 'itemName', header: 'Name' },
            { key: 'required', header: 'Required', align: 'right', render: (r) => `${String(r.required)} ${String(r.uom)}` },
            { key: 'onHand', header: 'On hand', align: 'right' }, { key: 'short', header: '', render: (r) => (r.short ? <StatusPill status="rejected" label="Short" /> : '') },
            { key: 'cost', header: 'Cost', align: 'right', render: (r) => money(r.cost) }]} empty={<Empty title="No BOM in the package" icon="inventory_2" />} />
        </div>
      )}
    </Card>
  );
}

// ── Commercial → Packages → Profitability (FR-PKG-P5-04) ──────────────────

export function PackageProfitabilityPage() {
  const [tab, setTab] = useTab('profitability');
  return (
    <div className="oc-stack">
      <PageHeader title="Package Profitability" help="Allocated revenue of the packages taking place in the period against COGS (BOM at the inventory cost: actual when served, estimated before), caddy fee, room cost, commission and other direct costs." />
      <Tabs tabs={[{ value: 'profitability', label: 'Profitability' }, { value: 'costs', label: 'Cost Rules' }]} value={tab} onChange={setTab} />
      {tab === 'profitability' ? <Profitability /> : <AutoResourcePage resourceKey="commercial.package_cost_rule" />}
    </div>
  );
}

const PROFIT_COLUMNS = [
  { key: 'bookings', header: 'Bookings', align: 'right' as const }, { key: 'pax', header: 'Pax', align: 'right' as const },
  { key: 'revenue', header: 'Revenue', align: 'right' as const, render: (r: R) => money(r.revenue) },
  { key: 'cogs', header: 'COGS', align: 'right' as const, render: (r: R) => money(r.cogs) },
  { key: 'caddyFee', header: 'Caddy fee', align: 'right' as const, render: (r: R) => money(r.caddyFee) },
  { key: 'roomCost', header: 'Room cost', align: 'right' as const, render: (r: R) => money(r.roomCost) },
  { key: 'commission', header: 'Commission', align: 'right' as const, render: (r: R) => money(r.commission) },
  { key: 'otherCost', header: 'Other', align: 'right' as const, render: (r: R) => money(r.otherCost) },
  { key: 'margin', header: 'Margin', align: 'right' as const, render: (r: R) => <strong>{money(r.margin)}</strong> },
  { key: 'marginPercent', header: 'Margin %', align: 'right' as const, render: (r: R) => (r.marginPercent != null ? `${String(r.marginPercent)}%` : '—') },
];

function Profitability() {
  const { can } = useAuth();
  const packages = usePackages();
  const [from, setFrom] = useState(`${today().slice(0, 8)}01`);
  const [to, setTo] = useState(today());
  const [pkg, setPkg] = useState('');
  const [groupBy, setGroupBy] = useState('package');
  const path = `${COM}/package-profitability${qs({ from, to, packageId: pkg, groupBy })}`;
  const p = useGet<R>(path);
  const head = groupBy === 'month' ? [{ key: 'period', header: 'Month' }] : groupBy === 'component' ? [{ key: 'component', header: 'Component' }] : [];
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <TextField label="Start dates from" type="date" value={from} onChange={setFrom} />
        <TextField label="Until" type="date" value={to} onChange={setTo} />
        <SelectField label="Package" value={pkg} onChange={setPkg} placeholder="All packages" options={packages} />
        <SelectField label="Group by" value={groupBy} onChange={setGroupBy} options={opts(['package', 'month', 'component'])} />
        <span className="oc-spacer" />
        {can('commercial.package_profitability.recalculate') && (
          <ActionButton label="Recalculate costs" path={`${COM}/package-profitability:recalculate`} body={{ from, to, ...(pkg ? { packageId: pkg } : {}) }}
            invalidate={[`${COM}/package-profitability`]} confirm="Recompute the cost lines of the bookings in the period with the cost rules and item costs in force?" />
        )}
      </div>
      <DataTable rows={list<R>(p.data?.rows)} loading={p.isLoading} error={p.error} rowKey={(r) => `${String(r.packageId)}${String(r.period ?? '')}${String(r.component ?? '')}`}
        columns={[{ key: 'packageName', header: 'Package', render: (r: R) => `${String(r.packageName)} (${String(r.packageCode)})` }, ...head, ...PROFIT_COLUMNS]} />
      {p.data?.total ? (
        <Card title="Total" icon="functions">
          <KV items={[['Revenue', money((p.data.total as R).revenue)], ['Pass-through (liabilities)', money((p.data.total as R).passThrough)],
            ['Total cost', money((p.data.total as R).totalCost)], ['Margin', money((p.data.total as R).margin)],
            ['Margin %', (p.data.total as R).marginPercent != null ? `${String((p.data.total as R).marginPercent)}%` : '—']]} />
        </Card>
      ) : null}
    </div>
  );
}

// ── Golf → Tournaments → Series (FR-TRN-P5-02) ────────────────────────────

export function SeriesPage() {
  const [tab, setTab] = useTab('series');
  return (
    <div className="oc-stack">
      <Tabs tabs={[{ value: 'series', label: 'Series & Order of Merit' }, { value: 'tables', label: 'Points Tables' }, { value: 'import', label: 'Import Past Seasons' }]}
        value={tab} onChange={setTab} />
      {tab === 'series' && <SeriesList />}
      {tab === 'tables' && <AutoResourcePage resourceKey="golf.series_points_table" />}
      {tab === 'import' && <ImportBox title="Import past Order of Merit seasons" path={`${SERIES}:import`}
        help="CSV with seriesCode, seriesName, season, rank, playerName, memberNo, customerCode, points, events, wins, publicConsent." />}
    </div>
  );
}

function SeriesList() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const [open, setOpen] = useState(false);
  const sel = params.get('id');
  return (
    <>
      <ListPage title="Tournament Series" help="Seasons of the Order of Merit: points per event (the final counts double), best N events, final standings and the champion."
        path={SERIES} search={false} statuses={opts(['draft', 'active', 'completed', 'cancelled'])}
        actions={can('golf.tournament_series.manage') ? <button className="oc-btn oc-btn-primary" onClick={() => setOpen(true)}>Create Series</button> : undefined}
        onRowClick={(r) => setParams({ tab: 'series', id: r.id })}
        columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Series' }, { key: 'season', header: 'Season' },
          { key: 'events', header: 'Events', render: (r) => `${String(r.countedEvents)} / ${String(r.events)}` },
          { key: 'pointsTableName', header: 'Points table', render: (r) => String(r.pointsTableName ?? '—') },
          { key: 'championName', header: 'Champion', render: (r) => String(r.championName ?? '—') }, { key: 'source', header: 'Source', render: (r) => label(r.source) },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {sel && <SeriesDetail id={sel} />}
      {open && <SeriesForm onClose={() => setOpen(false)} onDone={(id) => { setOpen(false); setParams({ tab: 'series', id }); }} />}
    </>
  );
}

function SeriesForm({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const tables = useGet<Page<R>>(`${GOLF}/series-points-tables?filter[status]=active&limit=100`);
  const [f, setF] = useState({ name: '', season: String(new Date().getFullYear()), pointsTableId: '', bestOf: '', minEvents: '0', category: 'primary' });
  const [pub, setPub] = useState(true);
  const send = useSend<Body, R>('POST', SERIES, [SERIES]);
  const set = (k: keyof typeof f) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  return (
    <Modal open onClose={onClose} title="Create Series" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ name: f.name, season: Number(f.season), category: f.category,
        public: pub, minEvents: Number(f.minEvents) || 0, ...(f.pointsTableId ? { pointsTableId: f.pointsTableId } : {}), ...(f.bestOf ? { bestOf: Number(f.bestOf) } : {}) },
      { onSuccess: (r) => onDone(String(r.id)) })}>Create</button></>}>
      <ErrorAlert error={send.error} />
      <div className="oc-form-grid">
        <TextField label="Name" value={f.name} onChange={set('name')} required />
        <TextField label="Season" type="number" value={f.season} onChange={set('season')} required />
        <SelectField label="Points table" value={f.pointsTableId} onChange={set('pointsTableId')} placeholder="Choose later"
          options={(tables.data?.items ?? []).map((x) => ({ value: x.id, label: String(x.name) }))} />
        <SelectField label="Results counted" value={f.category} onChange={set('category')} options={opts(['primary', 'gross', 'net', 'stableford'], { primary: 'Each event\'s primary category' })} />
        <TextField label="Best N events (empty = all)" type="number" min={1} value={f.bestOf} onChange={set('bestOf')} />
        <TextField label="Events needed to be ranked" type="number" min={0} value={f.minEvents} onChange={set('minEvents')} />
        <Checkbox label="Order of Merit on the website (names with consent)" checked={pub} onChange={setPub} />
      </div>
    </Modal>
  );
}

function SeriesDetail({ id }: { id: string }) {
  const { can } = useAuth();
  const base = `${SERIES}/${id}`;
  const d = useGet<R>(base);
  const oom = useGet<R>(`${base}/order-of-merit`);
  const tournaments = useTournaments();
  const [tid, setTid] = useState('');
  const [final, setFinal] = useState(false);
  const toast = useToast();
  const add = useSend<Body, R>('POST', `${base}/events`, [SERIES]);
  const inv = [SERIES];
  if (d.error) return <ErrorAlert error={d.error} />;
  if (!d.data) return <Skeleton />;
  const s = d.data;
  const manage = can('golf.tournament_series.manage');
  const editable = s.status === 'draft' || s.status === 'active';
  return (
    <Card title={`${String(s.name)} ${String(s.season)}`} icon="military_tech" actions={<div className="oc-row-wrap">
      {manage && s.status === 'draft' && <ActionButton kind="primary" label="Activate" path={`${base}:activate`} body={{}} invalidate={inv} />}
      {manage && s.status === 'active' && <ActionButton label="Recalculate" path={`${base}:recalculate`} body={{}} invalidate={inv} />}
      {manage && s.status === 'active' && <ActionButton kind="primary" label="Complete season" path={`${base}:complete`} body={{}} invalidate={inv}
        confirm="Freeze the final standings and add the champion to the Hall of Fame?" />}
      {manage && editable && <ActionButton danger label="Cancel" path={`${base}:cancel`} reason="required" invalidate={inv} />}
    </div>}>
      <div className="oc-stack">
        <KV items={[['Status', <StatusPill key="s" status={String(s.status)} />], ['Points table', d.data.pointsTable ? `${String((d.data.pointsTable as R).name)} · ${list<number>((d.data.pointsTable as R).points).join(' · ')}` : '—'],
          ['Counting', `${s.bestOf ? `best ${String(s.bestOf)} events` : 'every event'} · ranked from ${String(s.minEvents)} event(s) · ${label(s.category)}`],
          ['Website', s.public ? 'Public (names with consent)' : 'Not public'], ['Champion', String(s.championName ?? '—')]]} />
        <DataTable rows={list<R>(s.eventList)} rowKey={(r) => String(r.id)} columns={[
          { key: 'sequence', header: '#' }, { key: 'tournamentName', header: 'Event' }, { key: 'startDate', header: 'Date', render: (r) => formatDate(String(r.startDate)) },
          { key: 'teamFormat', header: 'Format', render: (r) => (r.teamFormat ? String(r.teamFormat) : label(r.format)) },
          { key: 'weight', header: 'Weight', render: (r) => `×${String(r.weight)}${r.isFinal ? ' (final)' : ''}` },
          { key: 'players', header: 'Players', align: 'right' }, { key: 'status', header: 'Points', render: pill('status') }]}
          actions={manage && editable ? (r) => <ActionButton danger kind="text" label="Remove" method="DELETE" path={`${base}/events/${String(r.id)}`} invalidate={inv}
            confirm="Remove the event and its points?" /> : undefined} />
        {manage && editable && (
          <div className="oc-row-wrap">
            <SelectField label="Add tournament" value={tid} onChange={setTid} options={tournaments} placeholder="Choose a tournament" />
            <Checkbox label="Final (counts double)" checked={final} onChange={setFinal} />
            <button className="oc-btn oc-btn-neutral" disabled={!tid || add.isPending}
              onClick={() => add.mutate({ tournamentId: tid, isFinal: final }, { onSuccess: () => { toast('Event added'); setTid(''); }, onError: (e) => toast(e.message, 'error') })}>Add event</button>
          </div>
        )}
        <h3 style={{ margin: 0 }}>Order of Merit{oom.data?.final ? ' (final)' : ''}</h3>
        <OrderOfMeritTable o={oom.data} loading={oom.isLoading} error={oom.error} />
        <a className="oc-small" href={`/reports/golf.tournament_series?series=${encodeURIComponent(String(s.code))}`}>Tournament Series Report</a>
      </div>
    </Card>
  );
}

export function OrderOfMeritTable({ o, loading, error }: { o?: R; loading?: boolean; error?: unknown }) {
  const events = list<R>(o?.events);
  return (
    <DataTable rows={list<R>(o?.standings)} loading={loading} error={error} rowKey={(r) => `${String(r.playerName)}${String(r.customerId ?? '')}`} columns={[
      { key: 'positionLabel', header: 'Pos' }, { key: 'playerName', header: 'Player' },
      ...events.map((e) => ({ key: String(e.id), header: `#${String(e.sequence)}`, align: 'right' as const,
        render: (r: R) => { const x = list<R>(r.eventPoints).find((p) => p.seriesEventId === e.id); return x ? `${String(x.points)} (${String(x.positionLabel)})` : '—'; } })),
      { key: 'events', header: 'Events', align: 'right' }, { key: 'wins', header: 'Wins', align: 'right' },
      { key: 'points', header: 'Points', align: 'right', render: (r) => <strong>{String(r.points)}</strong> }]} empty={<Empty title="No points yet" icon="military_tech" />} />
  );
}

function ImportBox({ title, path, help }: { title: string; path: string; help: string }) {
  const [csv, setCsv] = useState('');
  const [res, setRes] = useState<R | null>(null);
  const send = useSend<Body, R>('POST', path, [SERIES, TRN]);
  const run = (mode: string) => send.mutate({ mode, csv }, { onSuccess: (r) => setRes(r) });
  return (
    <Card title={title} icon="upload_file">
      <div className="oc-stack">
        <p className="oc-small oc-muted">{help}</p>
        <TextArea label="CSV" rows={8} value={csv} onChange={setCsv} />
        <ErrorAlert error={send.error} />
        <div className="oc-row-wrap">
          <button className="oc-btn oc-btn-neutral" disabled={!csv || send.isPending} onClick={() => run('preview')}>Preview</button>
          <button className="oc-btn oc-btn-primary" disabled={!csv || send.isPending} onClick={() => run('commit')}>Import</button>
        </div>
        {res && <KV items={Object.entries(res).filter(([k]) => k !== 'errors').map(([k, v]) => [label(k), String(v)] as [string, React.ReactNode])} />}
        {res && list<R>(res.errors).length > 0 && <DataTable rows={list<R>(res.errors)} rowKey={(r) => `${String(r.row)}${String(r.field)}`}
          columns={[{ key: 'row', header: 'Row' }, { key: 'field', header: 'Field' }, { key: 'message', header: 'Problem' }]} />}
      </div>
    </Card>
  );
}

// ── Golf → Tournaments → Team Formats (FR-TRN-P5-01, FR-TRN-P5-06) ────────

export function TeamFormatsPage() {
  const [tab, setTab] = useTab('formats');
  return (
    <div className="oc-stack">
      <PageHeader title="Team Formats" help="Scramble and Four-ball (Best Ball); Foursomes and Texas Scramble by activating their format. Handicap allowances per format (WHS by default); teams per tournament; registration categories and early-bird fees." />
      <Tabs tabs={[{ value: 'formats', label: 'Formats' }, { value: 'teams', label: 'Tournament Teams' }, { value: 'categories', label: 'Registration Categories' }]}
        value={tab} onChange={setTab} />
      {tab === 'formats' && <AutoResourcePage resourceKey="golf.team_format" />}
      {tab === 'teams' && <TournamentPicker render={(tid) => <TournamentTeams tid={tid} />} />}
      {tab === 'categories' && <TournamentPicker render={(tid) => <RegistrationCategories tid={tid} />} />}
    </div>
  );
}

function TournamentPicker({ render, status = '' }: { render: (tid: string) => React.ReactNode; status?: string }) {
  const ts = useTournaments(status);
  const [tid, setTid] = useState('');
  const id = tid || (ts[0]?.value ?? '');
  return (
    <div className="oc-stack">
      <SelectField label="Tournament" value={id} onChange={setTid} options={ts} />
      {id ? render(id) : <Empty title="No tournament" icon="emoji_events" />}
    </div>
  );
}

function TournamentTeams({ tid }: { tid: string }) {
  const { can } = useAuth();
  const toast = useToast();
  const base = `${TRN}/${tid}`;
  const setup = useGet<R>(`${base}/team-setup`);
  const formats = useGet<Page<R>>(`${GOLF}/team-formats?filter[status]=active&limit=100`);
  const regs = useGet<Page<R>>(`${base}/registrations?filter[status]=registered,checked_in&limit=500`);
  const lb = useGet<R>(setup.data?.format ? `${base}/team-leaderboard` : null);
  const [fmt, setFmt] = useState('');
  const [pick, setPick] = useState<string[]>([]);
  const [name, setName] = useState('');
  const inv = [TRN];
  const put = useSend<Body, R>('PUT', `${base}/team-setup`, inv);
  const create = useSend<Body, R>('POST', `${base}/teams`, inv);
  const manage = can('golf.tournament_team.manage');
  const s = setup.data;
  const inTeam = new Set(list<R>(s?.teams).flatMap((t) => list<R>(t.members).map((m) => String(m.registrationId))));
  const free = (regs.data?.items ?? []).filter((r) => !inTeam.has(String(r.id)));
  return (
    <div className="oc-stack">
      <ErrorAlert error={setup.error ?? put.error ?? create.error} />
      <div className="oc-row-wrap">
        <SelectField label="Team format" value={fmt || String(s?.teamFormatId ?? 'none')} onChange={setFmt}
          options={[{ value: 'none', label: 'Individual (no teams)' }, ...(formats.data?.items ?? []).map((f) => ({ value: f.id,
            label: `${String(f.name)} · ${label(f.formatType)} · ${list<number>(f.allowances).join('/')}%` }))]} />
        {manage && <button className="oc-btn oc-btn-neutral" disabled={put.isPending || !fmt} onClick={() => put.mutate({ teamFormatId: fmt === 'none' ? null : fmt },
          { onSuccess: () => { toast('Team format saved'); setFmt(''); } })}>Set format</button>}
        {manage && s?.format ? <ActionButton label="Form teams automatically" path={`${base}/teams:auto`} body={{ method: 'balanced' }} invalidate={inv} /> : null}
      </div>
      {s?.format ? (
        <>
          <DataTable rows={list<R>(s.teams)} rowKey={(r) => String(r.id)} columns={[
            { key: 'code', header: 'Team' }, { key: 'name', header: 'Name' },
            { key: 'members', header: 'Players', render: (r) => list<R>(r.members).map((m) => `${String(m.playerName)}${m.handicapIndex ? ` (${String(m.handicapIndex)})` : ''}${r.captainRegistrationId === m.registrationId ? ' ©' : ''}`).join(', ') },
            { key: 'teamHandicap', header: 'Team handicap', render: (r) => String(r.teamHandicap ?? '—') }]}
            actions={manage ? (r) => <ActionButton danger kind="text" label="Delete" method="DELETE" path={`${base}/teams/${String(r.id)}`} invalidate={inv} confirm="Delete the team?" /> : undefined} />
          {manage && free.length > 0 && (
            <Card title={`New team (${String((s.format as R).teamSize)} players)`} icon="group_add">
              <div className="oc-row-wrap">{free.map((r) => (
                <Checkbox key={r.id} label={`${String(r.playerName)}${r.handicapIndex ? ` (${String(r.handicapIndex)})` : ''}`} checked={pick.includes(r.id)}
                  onChange={(v) => setPick((p) => (v ? [...p, r.id] : p.filter((x) => x !== r.id)))} />))}
              </div>
              <div className="oc-row-wrap">
                <TextField label="Team name" value={name} onChange={setName} />
                <button className="oc-btn oc-btn-primary" disabled={pick.length < 2 || create.isPending}
                  onClick={() => create.mutate({ registrationIds: pick, ...(name ? { name } : {}) }, { onSuccess: () => { setPick([]); setName(''); toast('Team added'); } })}>Add team</button>
              </div>
            </Card>
          )}
          {lb.data && <TeamBoards lb={lb.data} />}
        </>
      ) : <Empty title="Individual tournament" help="Choose a team format to play in teams." icon="groups" />}
    </div>
  );
}

function TeamBoards({ lb }: { lb: R }) {
  return (
    <>
      {list<R>(lb.boards).map((b) => (
        <Card key={String(b.category)} title={`Team leaderboard · ${label(b.category)}`} icon="leaderboard">
          <DataTable rows={list<R>(b.entries)} rowKey={(r) => String(r.teamId)} columns={[
            { key: 'positionLabel', header: 'Pos' }, { key: 'teamName', header: 'Team' }, { key: 'players', header: 'Players', render: (r) => list<string>(r.players).join(', ') },
            { key: 'teamHandicap', header: 'Hcp', render: (r) => String(r.teamHandicap ?? '—') }, { key: 'thru', header: 'Thru' },
            { key: 'toPar', header: 'To par', render: (r) => (r.toPar == null ? '—' : Number(r.toPar) > 0 ? `+${String(r.toPar)}` : String(r.toPar)) },
            { key: 'score', header: b.category === 'stableford' ? 'Points' : 'Total', render: (r) => String((b.category === 'stableford' ? r.points : r.score) ?? '—') }]} />
        </Card>
      ))}
    </>
  );
}

function RegistrationCategories({ tid }: { tid: string }) {
  const { can } = useAuth();
  const toast = useToast();
  const path = `${TRN}/${tid}/registration-categories`;
  const d = useGet<R>(path);
  const save = useSend<Body, R>('PUT', path, [TRN]);
  const [quota, setQuota] = useState<Record<string, string>>({});
  const [fees, setFees] = useState<Record<string, R>>({});
  if (d.error) return <ErrorAlert error={d.error} />;
  if (!d.data) return <Skeleton />;
  const cats = list<R>(d.data.categories);
  const feeRows = list<R>(d.data.fees);
  const q = (c: R) => quota[String(c.category)] ?? (c.quota != null ? String(c.quota) : '');
  const fv = (f: R, k: string) => String((fees[String(f.feeId)] ?? {})[k] ?? f[k] ?? '');
  const setFee = (f: R, k: string, v: string) => setFees((x) => ({ ...x, [String(f.feeId)]: { ...(x[String(f.feeId)] ?? {}), [k]: v } }));
  const submit = () => save.mutate({
    quotas: cats.map((c) => ({ category: c.category, quota: q(c) === '' ? null : Number(q(c)) })),
    fees: feeRows.map((f) => ({ feeId: f.feeId, category: fv(f, 'category') || null, earlyBirdAmount: fv(f, 'earlyBirdAmount') || null,
      earlyBirdUntil: fv(f, 'earlyBirdUntil') || null })),
  }, { onSuccess: () => { toast('Registration categories saved'); setQuota({}); setFees({}); } });
  return (
    <div className="oc-stack">
      <ErrorAlert error={save.error} />
      <DataTable rows={cats} rowKey={(r) => String(r.category)} columns={[
        { key: 'category', header: 'Category', render: (r) => label(r.category) },
        { key: 'quota', header: 'Quota', render: (r) => <TextField label={`Quota ${label(r.category)}`} type="number" min={0} value={q(r)}
          onChange={(v) => setQuota((x) => ({ ...x, [String(r.category)]: v }))} /> },
        { key: 'registered', header: 'Registered', align: 'right' }, { key: 'waitlisted', header: 'Waitlisted', align: 'right' },
        { key: 'left', header: 'Left', align: 'right', render: (r) => String(r.left ?? '—') }]} />
      <DataTable rows={feeRows} rowKey={(r) => String(r.feeId)} columns={[
        { key: 'name', header: 'Fee' }, { key: 'amount', header: 'Amount', render: (r) => money(r.amount) },
        { key: 'category', header: 'Category', render: (r) => <SelectField label="Category" value={fv(r, 'category')} onChange={(v) => setFee(r, 'category', v)}
          placeholder="Every category" options={opts(['member', 'guest', 'sponsor_invitation'])} /> },
        { key: 'earlyBirdAmount', header: 'Early-bird', render: (r) => <MoneyField label="Early-bird amount" value={fv(r, 'earlyBirdAmount')} onChange={(v) => setFee(r, 'earlyBirdAmount', v)} /> },
        { key: 'earlyBirdUntil', header: 'Until', render: (r) => <TextField label="Last date" type="date" value={fv(r, 'earlyBirdUntil')} onChange={(v) => setFee(r, 'earlyBirdUntil', v)} /> },
        { key: 'earlyBirdOpen', header: 'Today', render: (r) => (r.earlyBirdOpen ? <StatusPill status="active" label="Early-bird" /> : '—') }]} />
      <div className="oc-row-wrap">
        <span className="oc-small oc-muted">{String(d.data.earlyBirds)} early-bird registrations · field {String(d.data.registered)} / {String(d.data.fieldSize)}</span>
        <span className="oc-spacer" />
        {can('golf.tournament.manage') && <button className="oc-btn oc-btn-primary" disabled={save.isPending} onClick={submit}>Save</button>}
      </div>
    </div>
  );
}

// ── Golf → Tournaments → Tournament History (FR-TRN-P5-04/05) ─────────────

export function TournamentHistoryPage() {
  const [tab, setTab] = useTab('archive');
  return (
    <div className="oc-stack">
      <PageHeader title="Tournament History" help="Archive of completed and imported tournaments with their champions, player statistics across tournaments, the champion history and the federation (PGI) handicaps and result reports." />
      <Tabs tabs={[{ value: 'archive', label: 'Archive' }, { value: 'players', label: 'Players' }, { value: 'champions', label: 'Champions' },
        { value: 'federation', label: 'Federation (PGI)' }]} value={tab} onChange={setTab} />
      {tab === 'archive' && <HistoryArchive />}
      {tab === 'players' && <HistoryPlayers />}
      {tab === 'champions' && <HistoryChampions />}
      {tab === 'federation' && <Federation />}
    </div>
  );
}

function HistoryArchive() {
  const [q, setQ] = useState('');
  const [from, setFrom] = useState(addDays(today(), -730));
  const h = useGet<Page<R>>(`${GOLF}/tournament-history${qs({ q, from, limit: 200 })}`);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap"><TextField label="Search" value={q} onChange={setQ} /><TextField label="From" type="date" value={from} onChange={setFrom} /></div>
      <DataTable rows={h.data?.items} loading={h.isLoading} error={h.error} rowKey={(r) => String(r.tournamentId)} columns={[
        { key: 'endDate', header: 'Date', render: (r) => formatDate(String(r.endDate)) }, { key: 'name', header: 'Tournament' },
        { key: 'format', header: 'Format', render: (r) => (r.teamFormat ? String(r.teamFormat) : label(r.format)) }, { key: 'players', header: 'Players', align: 'right' },
        { key: 'champions', header: 'Champions', render: (r) => list<R>(r.champions).map((c) => `${String(c.playerName)} (${label(c.category)} ${String(c.score ?? '')})`).join(', ') || '—' },
        { key: 'series', header: 'Series', render: (r) => list<string>(r.series).join(', ') || '—' }, { key: 'source', header: 'Source', render: (r) => label(r.source) }]} />
    </div>
  );
}

function HistoryPlayers() {
  const [q, setQ] = useState('');
  const [sel, setSel] = useState<R | null>(null);
  const p = useGet<Page<R>>(`${GOLF}/tournament-history/players${qs({ q, limit: 100 })}`);
  const ph = useGet<R>(sel ? `${GOLF}/tournament-history/player${qs(sel.customerId ? { customerId: String(sel.customerId) } : { name: String(sel.playerName) })}` : null);
  return (
    <div className="oc-stack">
      <TextField label="Player" value={q} onChange={setQ} />
      <DataTable rows={p.data?.items} loading={p.isLoading} error={p.error} rowKey={(r) => String(r.playerKey)} onRowClick={setSel} columns={[
        { key: 'playerName', header: 'Player' }, { key: 'events', header: 'Events', align: 'right' }, { key: 'wins', header: 'Wins', align: 'right' },
        { key: 'top3', header: 'Top 3', align: 'right' }, { key: 'top10', header: 'Top 10', align: 'right' },
        { key: 'bestGross', header: 'Best gross', align: 'right', render: (r) => String(r.bestGross ?? '—') },
        { key: 'averageGross', header: 'Average gross', align: 'right', render: (r) => String(r.averageGross ?? '—') },
        { key: 'lastPlayed', header: 'Last played', render: (r) => formatDate(String(r.lastPlayed)) }]} />
      {sel && (
        <Modal open wide onClose={() => setSel(null)} title={String(sel.playerName)}>
          <ErrorAlert error={ph.error} />
          <DataTable rows={list<R>(ph.data?.results)} loading={ph.isLoading} rowKey={(r) => `${String(r.tournamentId)}${String(r.category)}${String(r.division ?? '')}${String(r.teamName ?? '')}`}
            columns={[{ key: 'endDate', header: 'Date', render: (r) => formatDate(String(r.endDate)) }, { key: 'name', header: 'Tournament' },
              { key: 'category', header: 'Category', render: (r) => `${label(r.category)}${r.division ? ` · ${String(r.division)}` : ''}${r.teamName ? ` · ${String(r.teamName)}` : ''}` },
              { key: 'positionLabel', header: 'Pos' }, { key: 'score', header: 'Score', render: (r) => String(r.score ?? '—') }]} />
          <DataTable rows={list<R>(ph.data?.series)} rowKey={(r) => String(r.seriesId)} columns={[{ key: 'seriesName', header: 'Order of Merit' },
            { key: 'season', header: 'Season' }, { key: 'positionLabel', header: 'Pos' }, { key: 'points', header: 'Points', align: 'right' }]} />
        </Modal>
      )}
    </div>
  );
}

function HistoryChampions() {
  const [q, setQ] = useState('');
  const c = useGet<Page<R>>(`${GOLF}/tournament-history/champions${qs({ q })}`);
  return (
    <div className="oc-stack">
      <TextField label="Search" value={q} onChange={setQ} />
      <DataTable rows={c.data?.items} loading={c.isLoading} error={c.error} rowKey={(r) => `${String(r.eventId)}${String(r.category)}${String(r.division ?? '')}${String(r.playerName)}`}
        columns={[{ key: 'year', header: 'Year' }, { key: 'event', header: 'Tournament / Order of Merit' },
          { key: 'category', header: 'Category', render: (r) => `${label(r.category)}${r.division ? ` · ${String(r.division)}` : ''}` },
          { key: 'playerName', header: 'Champion' }, { key: 'score', header: 'Score / points', render: (r) => String(r.score ?? '—') }]} />
    </div>
  );
}

function Federation() {
  const { can } = useAuth();
  const toast = useToast();
  const [lines, setLines] = useState('');
  const [res, setRes] = useState<R | null>(null);
  const imp = useSend<Body, R>('POST', `${GOLF}/federation/handicaps:import`, []);
  const parse = () => lines.split('\n').map((l) => l.split(',').map((x) => x.trim())).filter((x) => x[0]).map(([who, index, no]) => ({
    ...(/^[0-9a-f-]{36}$/i.test(who) ? { customerId: who } : { memberNo: who }), handicapIndex: index ?? '', ...(no ? { federationNumber: no } : {}) }));
  const done = useGet<Page<R>>(`${TRN}${qs({ 'filter[status]': 'completed', limit: 200 })}`);
  const [tid, setTid] = useState('');
  const rep = useGet<R>(tid ? `${TRN}/${tid}/federation-report` : null);
  const [ref, setRef] = useState('');
  const csv = () => {
    const rows = [['Position', 'Player', 'Member No', 'Handicap Index', 'Source', 'Rounds', 'Total'],
      ...list<R>(rep.data?.lines).map((l) => [l.position, l.playerName, l.memberNo ?? '', l.handicapIndex ?? '', l.handicapSource ?? '', list<number>(l.rounds).join('/'), l.total ?? ''])];
    const blob = new Blob([rows.map((r) => r.map((x) => `"${String(x).replace(/"/g, '""')}"`).join(',')).join('\n')], { type: 'text/csv' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `PGI-${String(rep.data?.code ?? 'report')}.csv`;
    a.click();
  };
  return (
    <div className="oc-stack">
      {can('golf.federation.manage') && (
        <Card title="Official handicap index (PGI) — bulk entry" icon="badge">
          <p className="oc-small oc-muted">One player per line: member number (or customer id), handicap index, PGI number.</p>
          <TextArea label="Handicaps" rows={6} value={lines} onChange={setLines} />
          <ErrorAlert error={imp.error} />
          <button className="oc-btn oc-btn-primary" disabled={!lines || imp.isPending} onClick={() => imp.mutate({ federation: 'PGI', entries: parse() },
            { onSuccess: (r) => { setRes(r); toast(`${String(r.imported)} handicap(s) recorded`); } })}>Record handicaps</button>
          {res && list<R>(res.errors).length > 0 && <DataTable rows={list<R>(res.errors)} rowKey={(r) => String(r.row)}
            columns={[{ key: 'row', header: 'Line' }, { key: 'message', header: 'Problem' }]} />}
        </Card>
      )}
      <Card title="Result report to the federation" icon="send">
        <SelectField label="Completed tournament" value={tid} onChange={setTid} placeholder="Choose a tournament"
          options={(done.data?.items ?? []).map((t) => ({ value: t.id, label: `${String(t.name)} · ${formatDate(String(t.endDate))}` }))} />
        <ErrorAlert error={rep.error} />
        {rep.data && (
          <div className="oc-stack">
            <DataTable rows={list<R>(rep.data.lines)} rowKey={(r) => `${String(r.position)}${String(r.playerName)}`} columns={[
              { key: 'position', header: 'Pos' }, { key: 'playerName', header: 'Player' }, { key: 'memberNo', header: 'Member', render: (r) => String(r.memberNo ?? '—') },
              { key: 'handicapIndex', header: 'Hcp index', render: (r) => `${String(r.handicapIndex ?? '—')}${r.handicapSource === 'federation' ? ' (PGI)' : ''}` },
              { key: 'rounds', header: 'Rounds', render: (r) => list<number>(r.rounds).join(' / ') }, { key: 'total', header: 'Total', render: (r) => String(r.total ?? '—') }]} />
            <div className="oc-row-wrap">
              <button className="oc-btn oc-btn-neutral" onClick={csv}>Export CSV</button>
              <TextField label="Upload reference" value={ref} onChange={setRef} />
              {can('golf.federation.manage') && <ActionButton kind="primary" label="Record as uploaded" path={`${TRN}/${tid}/federation-report:submit`}
                body={{ method: 'manual_upload', reference: ref }} invalidate={[TRN]} />}
            </div>
            <DataTable rows={list<R>(rep.data.submissions)} rowKey={(r) => String(r.id)} columns={[{ key: 'submittedAt', header: 'When', render: (r) => formatDate(String(r.submittedAt)) },
              { key: 'method', header: 'Method', render: (r) => label(r.method) }, { key: 'reference', header: 'Reference', render: (r) => String(r.reference ?? '—') },
              { key: 'rows', header: 'Players', align: 'right' }, { key: 'status', header: 'Status', render: pill('status') }]} />
          </div>
        )}
      </Card>
    </div>
  );
}

// ── ops: Tournament Desk → Team Scoring (one-ball team card) ──────────────

export function TeamScoringPage() {
  const toast = useToast();
  const ts = useTournaments('in_progress');
  const [tid, setTid] = useState('');
  const id = tid || (ts[0]?.value ?? '');
  const setup = useGet<R>(id ? `${TRN}/${id}/team-setup` : null);
  const t = useGet<R>(id ? `${TRN}/${id}` : null);
  const [team, setTeam] = useState('');
  const [strokes, setStrokes] = useState<Record<number, number>>({});
  const send = useSend<Body, R>('POST', `${TRN}/${id}/team-scores`, [TRN]);
  const holes = 18;
  const teams = list<R>(setup.data?.teams);
  const format = setup.data?.format as R | undefined;
  const round = Number(t.data?.currentRound ?? 1);
  const step = (h: number, d: number) => setStrokes((s) => ({ ...s, [h]: Math.max(1, (s[h] ?? 4) + d) }));
  return (
    <div className="oc-stack">
      <PageHeader title="Team Scoring" help="Scramble, Texas Scramble and Foursomes: the team card is entered once and written on every member's scorecard; attest and validate on the scoring desk." />
      <div className="oc-row-wrap">
        <SelectField label="Tournament" value={id} onChange={setTid} options={ts} />
        <SelectField label="Team" value={team} onChange={setTeam} placeholder="Choose the team" options={teams.map((x) => ({ value: x.id, label: `${String(x.code)} · ${String(x.name)}` }))} />
      </div>
      {format && format.formatType === 'four_ball' && <Empty title="Four-ball" help="Every player enters their own score on the scoring desk." icon="group" />}
      {team && format && format.formatType !== 'four_ball' && (
        <>
          <div className="oc-grid" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))' }}>
            {Array.from({ length: holes }, (_, i) => i + 1).map((h) => (
              <div key={h} className="oc-card" style={{ padding: 12 }}>
                <div className="oc-small oc-muted">Hole {h}</div>
                <div className="oc-row" style={{ justifyContent: 'space-between' }}>
                  <button className="oc-btn oc-btn-neutral" style={{ minWidth: 48, minHeight: 48 }} aria-label={`Hole ${h} minus`} onClick={() => step(h, -1)}>−</button>
                  <strong style={{ fontSize: 28 }} aria-live="polite">{strokes[h] ?? '–'}</strong>
                  <button className="oc-btn oc-btn-neutral" style={{ minWidth: 48, minHeight: 48 }} aria-label={`Hole ${h} plus`} onClick={() => step(h, 1)}>+</button>
                </div>
              </div>
            ))}
          </div>
          <ErrorAlert error={send.error} />
          <button className="oc-btn oc-btn-primary" style={{ minHeight: 56 }} disabled={send.isPending || Object.keys(strokes).length === 0}
            onClick={() => send.mutate({ teamId: team, round, entries: Object.entries(strokes).map(([seq, s]) => ({ seq: Number(seq), strokes: s })) },
              { onSuccess: () => { toast('Team card saved'); setStrokes({}); } })}>Save team card (round {round})</button>
        </>
      )}
    </div>
  );
}

export const LEISURE_ROUTES: AreaRoute[] = [
  { path: 'commercial/package-capacity', perm: 'commercial.package_capacity.view', element: <PackageCapacityPage /> },
  { path: 'commercial/package-profitability', perm: 'commercial.package_profitability.view', element: <PackageProfitabilityPage /> },
  { path: 'golf/tournament-series', perm: 'golf.tournament_series.view', element: <SeriesPage /> },
  { path: 'golf/team-formats', perm: 'golf.team_format.view', element: <TeamFormatsPage /> },
  { path: 'golf/tournament-history', perm: 'golf.tournament_history.view', element: <TournamentHistoryPage /> },
];
export const LEISURE_OPS_TILES: OpsTile[] = [
  ['groups', 'Team Scoring', '/ops/tournament-desk/team-scoring', 'golf.tournament_score.enter'],
];
export const LEISURE_OPS_ROUTES: OpsRoute[] = [
  { path: 'tournament-desk/team-scoring', element: <TeamScoringPage /> },
];
