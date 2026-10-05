import React, { useState } from 'react';
import { useGet, type Page } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import { Card, DataTable, Empty, ErrorAlert, PageHeader, Skeleton, StatusPill } from '@oneclub/shell';

// Member App — PRD P5 tournament series standings and history, multi-business packages (EP-22–23). Registered in src/p3.tsx.
//
// Golf → Order of Merit: the seasons of the club's Tournament Series with my position and points; the standing of a
// season (other players without public consent are shown by their initials, UU PDP). Golf → Tournament History: my
// results across tournaments (team events included), my statistics and my Order of Merit seasons.

type Row = Record<string, unknown> & { id?: string };
const API = '/api/v1/member/golf';
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const list = <T,>(v: unknown) => (Array.isArray(v) ? (v as T[]) : []);

/** Order of Merit: seasons with my standing, then the standing of a season. */
export function OrderOfMeritPage() {
  const ls = useGet<Page<Row>>(`${API}/tournament-series`);
  const [sel, setSel] = useState('');
  const id = sel || String(ls.data?.items[0]?.id ?? '');
  return (
    <div className="oc-stack">
      <PageHeader title="Order of Merit" help="Points from every event of the season; the best results count. Your line is highlighted." />
      <ErrorAlert error={ls.error} />
      {ls.isLoading && <Skeleton />}
      {ls.data?.items.length === 0 && <Empty title="No Order of Merit yet" icon="military_tech" />}
      <div className="oc-row-wrap" role="tablist">
        {ls.data?.items.map((s) => (
          <button key={String(s.id)} role="tab" className="oc-chip" aria-selected={id === s.id} aria-pressed={id === s.id} onClick={() => setSel(String(s.id))}>
            {String(s.name)} {String(s.season)}{s.myPosition && s.myPosition !== '-' ? ` · #${String(s.myPosition)}` : ''}
          </button>
        ))}
      </div>
      {id && <SeasonStanding id={id} season={ls.data?.items.find((s) => s.id === id)} />}
    </div>
  );
}

function SeasonStanding({ id, season }: { id: string; season?: Row }) {
  const o = useGet<Row>(`${API}/tournament-series/${id}/order-of-merit`);
  if (o.error) return <ErrorAlert error={o.error} />;
  if (!o.data) return <Skeleton />;
  const me = list<Row>(o.data.standings).find((e) => e.me);
  return (
    <div className="oc-stack">
      <Card title={`${String(o.data.name)} ${String(o.data.season)}`} icon="military_tech">
        <div className="oc-row-wrap">
          <StatusPill status={String(o.data.status)} label={o.data.final ? 'Final' : label(o.data.status)} />
          <span>{String(season?.countedEvents ?? 0)} of {String(season?.events ?? 0)} events played</span>
          {o.data.bestOf ? <span className="oc-small oc-muted">Best {String(o.data.bestOf)} events count</span> : null}
          {o.data.champion ? <strong>Champion: {String(o.data.champion)}</strong> : null}
        </div>
        {me ? <p style={{ marginBottom: 0 }}>You are <strong>{String(me.positionLabel)}</strong> with <strong>{String(me.points)}</strong> points from {String(me.events)} event(s).</p>
          : <p className="oc-muted" style={{ marginBottom: 0 }}>You have no points in this season yet.</p>}
      </Card>
      <DataTable rows={list<Row>(o.data.standings)} rowKey={(r) => `${String(r.positionLabel)}${String(r.playerName)}`} columns={[
        { key: 'positionLabel', header: 'Pos' },
        { key: 'playerName', header: 'Player', render: (r) => (r.me ? <strong>{String(r.playerName)} (you)</strong> : String(r.playerName)) },
        { key: 'events', header: 'Events', align: 'right' }, { key: 'wins', header: 'Wins', align: 'right' },
        { key: 'points', header: 'Points', align: 'right', render: (r) => <strong>{String(r.points)}</strong> }]} />
      <DataTable rows={list<Row>(o.data.events)} rowKey={(r) => String(r.id)} columns={[
        { key: 'sequence', header: '#' }, { key: 'tournamentName', header: 'Event' }, { key: 'startDate', header: 'Date', render: (r) => formatDate(String(r.startDate)) },
        { key: 'weight', header: 'Points', render: (r) => `×${String(r.weight)}${r.isFinal ? ' · final' : ''}` },
        { key: 'status', header: 'Status', render: (r) => <StatusPill status={r.status === 'counted' ? 'completed' : 'scheduled'} label={r.status === 'counted' ? 'Counted' : 'Upcoming'} /> }]} />
    </div>
  );
}

/** Tournament History: my results, statistics and Order of Merit seasons. */
export function MyTournamentHistoryPage() {
  const h = useGet<Row>(`${API}/tournament-history`);
  if (h.error) return <ErrorAlert error={h.error} />;
  if (!h.data) return <Skeleton />;
  const s = (h.data.stats ?? {}) as Row;
  return (
    <div className="oc-stack">
      <PageHeader title="Tournament History" help="Your tournaments, results and Order of Merit seasons at the club." />
      <div className="oc-grid">
        {([['Events', s.events], ['Wins', s.wins], ['Top 3', s.top3], ['Best gross', s.bestGross], ['Average gross', s.averageGross],
          ['Best stableford', s.bestStableford]] as [string, unknown][]).map(([k, v]) => (
          <Card key={k} title={k}><strong style={{ fontSize: 24 }}>{v == null || v === '' ? '—' : String(v)}</strong></Card>
        ))}
      </div>
      <DataTable rows={list<Row>(h.data.results)} rowKey={(r) => `${String(r.tournamentId)}${String(r.category)}${String(r.division ?? '')}${String(r.teamName ?? '')}`}
        empty={<Empty title="No tournament results yet" icon="emoji_events" />} columns={[
          { key: 'endDate', header: 'Date', render: (r) => formatDate(String(r.endDate)) }, { key: 'name', header: 'Tournament' },
          { key: 'category', header: 'Category', render: (r) => `${label(r.category)}${r.division ? ` · ${String(r.division)}` : ''}${r.teamName ? ` · team ${String(r.teamName)}` : ''}` },
          { key: 'positionLabel', header: 'Position' }, { key: 'score', header: 'Score', align: 'right', render: (r) => String(r.score ?? '—') }]} />
      {list<Row>(h.data.series).length > 0 && (
        <Card title="Order of Merit" icon="military_tech">
          <DataTable rows={list<Row>(h.data.series)} rowKey={(r) => String(r.seriesId)} columns={[{ key: 'seriesName', header: 'Season', render: (r) => `${String(r.seriesName)} ${String(r.season)}` },
            { key: 'positionLabel', header: 'Position' }, { key: 'points', header: 'Points', align: 'right' }, { key: 'final', header: '', render: (r) => (r.final ? 'Final' : 'Live') }]} />
        </Card>
      )}
    </div>
  );
}

export const LEISURE_MEMBER_ROUTES: { path: string; element: React.ReactNode }[] = [
  { path: 'golf/order-of-merit', element: <OrderOfMeritPage /> },
  { path: 'golf/tournament-history', element: <MyTournamentHistoryPage /> },
];
