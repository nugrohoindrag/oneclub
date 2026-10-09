import React, { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { download, qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime } from '@oneclub/i18n';
import { cacheGet, cachePut, enqueue, useOnline } from '@oneclub/offline';
import {
  Card, Checkbox, DataTable, Empty, ErrorAlert, Icon, Modal, MoneyField, PageHeader, SelectField, StatusPill, TextArea, TextField, useAuth, useToast,
  type Option,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import type { AreaRoute, OpsRoute, OpsTile } from './types';

// Golf Tournament (PRD P3 EP-16, Naming Convention Golf → Tournaments):
// Tournament Schedule, Registration, Participants, Flighting, Tee
// Assignment, Scoring, Leaderboard, Tournament Packages, Tournament Fees,
// Sponsors, Prizes and Tournament Reports in the Back Office; the
// Tournament Desk (check-in, draw / start sheet, scoring desk from paper
// cards with the offline queue, leaderboard) and the Shotgun Start of the
// Starter on `ops`.

const API = '/api/v1/golf/tournaments';
const INV = [API, '/api/v1/golf/tournament-screen'];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const opts = (vals: string[], labels: Record<string, string> = {}): Option[] => vals.map((v) => ({ value: v, label: labels[v] ?? label(v) }));
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} />;
const opt = (v: string) => (v === '' ? undefined : v);
const num = (v: string) => (v === '' ? undefined : Number(v));
const list = <T,>(v: unknown) => (Array.isArray(v) ? (v as T[]) : []);

const TYPES = opts(['club', 'club_championship', 'corporate', 'invitational', 'sponsor', 'charity']);
const FORMATS = opts(['stroke_play', 'stableford']);
const BASIS = opts(['gross_and_net', 'gross', 'net'], { gross_and_net: 'Gross and net' });
const ELIGIBILITY = opts(['members_and_guests', 'members', 'invitation', 'open'], { members_and_guests: 'Members and guests', open: 'Open (public)' });
const START_TYPES = opts(['shotgun', 'tee_times'], { shotgun: 'Shotgun Start', tee_times: 'Tee times' });
const STATUSES = opts(['draft', 'open', 'closed', 'in_progress', 'completed', 'cancelled']);
const REG_STATUSES = opts(['registered', 'checked_in', 'waitlisted', 'withdrawn'], { checked_in: 'Checked-in' });
const COMPONENTS = opts(['entry_fee', 'green_fee', 'caddy_fee', 'cart_fee', 'dinner', 'goodie_bag', 'insurance', 'other'], { cart_fee: 'Golf cart fee', insurance: 'HIO insurance' });
const PLAYER_TYPES = opts(['any', 'member', 'guest']);
const LEVELS = opts(['title', 'platinum', 'gold', 'silver', 'bronze', 'hole', 'supporting', 'in_kind']);
const PRIZE_CATS = opts(['gross', 'net', 'stableford', 'nearest_to_pin', 'longest_drive', 'hole_in_one', 'lucky_draw', 'other'], { hole_in_one: 'Hole-in-One' });
const PAY_METHODS = opts(['cash', 'card', 'bank_transfer', 'qris', 'virtual_account', 'member_account'], { qris: 'QRIS' });

export const TOURNAMENT_SECTIONS: Option[] = [
  { value: 'overview', label: 'Overview' }, { value: 'registration', label: 'Registration' }, { value: 'participants', label: 'Participants' },
  { value: 'flighting', label: 'Flighting' }, { value: 'tee-assignment', label: 'Tee Assignment' }, { value: 'scoring', label: 'Scoring' },
  { value: 'leaderboard', label: 'Leaderboard' }, { value: 'packages', label: 'Tournament Packages' }, { value: 'fees', label: 'Tournament Fees' },
  { value: 'sponsors', label: 'Sponsors' }, { value: 'prizes', label: 'Prizes' }, { value: 'results', label: 'Results' },
];

/** Live updates of tournament screens (scores, draw, check-in) over SSE. */
export function useTournamentStream(tid: string | null, onEvent: () => void) {
  const { propertyId } = useAuth();
  useEffect(() => {
    if (!tid || typeof EventSource === 'undefined') return;
    const es = new EventSource(`${API}/stream${qs({ tournamentId: tid, propertyId })}`, { withCredentials: true });
    const h = () => onEvent();
    es.addEventListener('golf.tournament', h);
    return () => es.close();
  }, [tid, propertyId]); // eslint-disable-line react-hooks/exhaustive-deps
}

function Actions({ children }: { children: React.ReactNode }) {
  return <div className="oc-row-wrap">{children}</div>;
}

// ── Tournament Schedule ───────────────────────────────────────────────────

export function TournamentsPage() {
  const { can } = useAuth();
  const nav = useNavigate();
  const [modal, setModal] = useState<'' | 'new' | 'import'>('');
  const [type, setType] = useState('');
  const [source, setSource] = useState('');
  return (
    <>
      <ListPage title="Tournament Schedule" help="Club, corporate and sponsor tournaments: registration, flighting, shotgun start, live scoring and results (Tournament Management)."
        path={API} statuses={STATUSES} extraQuery={{ 'filter[type]': type, 'filter[source]': source }}
        filters={<>
          <SelectField label="Type" value={type} onChange={setType} placeholder="All types" options={TYPES} />
          <SelectField label="Source" value={source} onChange={setSource} placeholder="All" options={opts(['oneclub', 'import'], { oneclub: 'OneClub', import: 'Imported history' })} />
        </>}
        actions={<Actions>
          {can('golf.tournament.manage') && <button className="oc-btn oc-btn-primary" onClick={() => setModal('new')}>Create Tournament</button>}
          {can('golf.tournament.manage') && <button className="oc-btn oc-btn-neutral" onClick={() => setModal('import')}>Import History</button>}
        </Actions>}
        onRowClick={(r) => nav(`/golf/tournaments/${r.id}`)}
        columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Tournament' },
          { key: 'startDate', header: 'Date', render: (r) => (r.startDate === r.endDate ? formatDate(String(r.startDate)) : `${formatDate(String(r.startDate))} – ${formatDate(String(r.endDate))}`) },
          { key: 'format', header: 'Format', render: (r) => `${label(r.format)} · ${label(r.scoringBasis)}` },
          { key: 'startType', header: 'Start', render: (r) => label(r.startType) },
          { key: 'registered', header: 'Field', align: 'right', render: (r) => `${String(r.registered)} / ${String(r.fieldSize)}${Number(r.waitlisted) ? ` (+${String(r.waitlisted)})` : ''}` },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {modal === 'new' && <TournamentForm onClose={() => setModal('')} onDone={(id) => nav(`/golf/tournaments/${id}`)} />}
      {modal === 'import' && <ImportHistory onClose={() => setModal('')} />}
    </>
  );
}

/** Menu entry of a tournament section: pick the tournament. */
export function TournamentSectionPage() {
  const { tab = 'overview' } = useParams();
  const nav = useNavigate();
  const section = TOURNAMENT_SECTIONS.find((s) => s.value === tab)?.label ?? 'Tournaments';
  return (
    <ListPage title={section} help="Choose the tournament." path={API} extraQuery={{ 'filter[source]': 'oneclub' }}
      statuses={opts(['open', 'closed', 'in_progress', 'draft', 'completed'])} onRowClick={(r) => nav(`/golf/tournaments/${r.id}/${tab}`)}
      columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Tournament' }, { key: 'startDate', header: 'Date', render: (r) => formatDate(String(r.startDate)) },
        { key: 'registered', header: 'Field', align: 'right', render: (r) => `${String(r.registered)} / ${String(r.fieldSize)}` }, { key: 'status', header: 'Status', render: pill('status') }]} />
  );
}

function TournamentForm({ onClose, onDone, t }: { onClose: () => void; onDone: (id: string) => void; t?: R }) {
  const courses = useGet<Page<R>>('/api/v1/golf/courses?filter[status]=active&limit=100');
  const round0 = list<R>(t?.rounds)[0];
  const [f, setF] = useState<Record<string, string>>({
    name: String(t?.name ?? ''), description: String(t?.description ?? ''), tournamentType: String(t?.tournamentType ?? 'club'),
    courseId: String(t?.courseId ?? ''), format: String(t?.format ?? 'stableford'), scoringBasis: String(t?.scoringBasis ?? 'gross_and_net'),
    eligibility: String(t?.eligibility ?? 'members_and_guests'), fieldSize: String(t?.fieldSize ?? '72'), playersPerFlight: String(t?.playersPerFlight ?? '4'),
    startType: String(t?.startType ?? 'shotgun'), handicapAllowance: String(t?.handicapAllowance ?? ''), maxHandicap: String(t?.maxHandicap ?? ''),
    playDate: String(round0?.playDate ?? today()), startTime: String(round0?.startTime ?? '07:00'), days: String(list(t?.rounds).length || 1),
    startTees: String(round0?.startTees ?? '1'), teeIntervalMinutes: String(round0?.teeIntervalMinutes ?? '10'),
    cutAfterRound: t?.cutAfterRound != null ? String(t.cutAfterRound) : '', cutTop: t?.cutTop != null ? String(t.cutTop) : '', notes: String(t?.notes ?? ''),
  });
  const [pub, setPub] = useState(Boolean(t?.public));
  const [lbPub, setLbPub] = useState(t ? Boolean(t.leaderboardPublic) : true);
  const set = (k: string) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const send = useSend<R, R>(t ? 'PATCH' : 'POST', t ? `${API}/${t.id}` : API, INV);
  const rounds = Array.from({ length: Math.max(1, Number(f.days) || 1) }, (_, i) => {
    const d = new Date(`${f.playDate}T00:00:00`);
    d.setDate(d.getDate() + i);
    const ds = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
    return { playDate: ds, startTime: f.startTime, startTees: f.startType === 'tee_times' ? f.startTees : undefined,
      teeIntervalMinutes: f.startType === 'tee_times' ? num(f.teeIntervalMinutes) : undefined };
  });
  const body: Record<string, unknown> = {
    name: f.name, description: f.description, tournamentType: f.tournamentType, format: f.format, scoringBasis: f.scoringBasis, eligibility: f.eligibility,
    fieldSize: num(f.fieldSize), playersPerFlight: num(f.playersPerFlight), startType: f.startType, handicapAllowance: f.handicapAllowance,
    maxHandicap: f.maxHandicap, public: pub, leaderboardPublic: lbPub, notes: f.notes, cutAfterRound: num(f.cutAfterRound), cutTop: num(f.cutTop),
  };
  if (!t) Object.assign(body, { courseId: f.courseId, rounds });
  else if (t.status === 'draft' || t.status === 'open' || t.status === 'closed') body.rounds = rounds;
  if (t && t.status === 'in_progress') {
    for (const k of ['format', 'scoringBasis', 'handicapAllowance', 'maxHandicap', 'startType', 'rounds', 'cutAfterRound', 'cutTop', 'playersPerFlight']) delete body[k];
  }
  const fe = send.error?.fieldErrors ?? {};
  return (
    <Modal open onClose={onClose} title={t ? `Edit ${String(t.code)}` : 'Create Tournament'} wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!f.name || (!t && !f.courseId) || send.isPending}
        onClick={() => send.mutate(body as R, { onSuccess: (r) => onDone(r.id) })}>Save</button>
    </>}>
      <div className="oc-form">
        <TextField label="Name" value={f.name} onChange={set('name')} required error={fe.name} />
        <SelectField label="Type" value={f.tournamentType} onChange={set('tournamentType')} options={TYPES} />
        {!t && <SelectField label="Course" value={f.courseId} onChange={set('courseId')} required error={fe.courseId} placeholder="Select"
          options={(courses.data?.items ?? []).map((c) => ({ value: c.id, label: String(c.name) }))} />}
        <SelectField label="Format" value={f.format} onChange={set('format')} options={FORMATS} error={fe.format} />
        <SelectField label="Scoring" value={f.scoringBasis} onChange={set('scoringBasis')} options={BASIS} />
        <SelectField label="Eligibility" value={f.eligibility} onChange={set('eligibility')} options={ELIGIBILITY} />
        <TextField label="Field size (players)" type="number" value={f.fieldSize} onChange={set('fieldSize')} error={fe.fieldSize} />
        <TextField label="Players per flight" type="number" value={f.playersPerFlight} onChange={set('playersPerFlight')} />
        <SelectField label="Start" value={f.startType} onChange={set('startType')} options={START_TYPES} />
        <TextField label="First day" type="date" value={f.playDate} onChange={set('playDate')} error={fe['rounds[0].playDate']} />
        <TextField label="Days (rounds)" type="number" value={f.days} onChange={set('days')} />
        <TextField label={f.startType === 'shotgun' ? 'Shotgun time' : 'First tee time'} type="time" value={f.startTime} onChange={set('startTime')} />
        {f.startType === 'tee_times' && <SelectField label="Tees" value={f.startTees} onChange={set('startTees')} options={[{ value: '1', label: 'Tee 1' }, { value: '1,10', label: 'Tees 1 and 10' }]} />}
        {f.startType === 'tee_times' && <TextField label="Interval (minutes)" type="number" value={f.teeIntervalMinutes} onChange={set('teeIntervalMinutes')} />}
        <TextField label="Handicap allowance %" value={f.handicapAllowance} onChange={set('handicapAllowance')} help="Empty: Tournament Policies" error={fe.handicapAllowance} />
        <TextField label="Maximum handicap" value={f.maxHandicap} onChange={set('maxHandicap')} help="Empty: Tournament Policies" error={fe.maxHandicap} />
        {Number(f.days) > 1 && <TextField label="Cut after round" type="number" value={f.cutAfterRound} onChange={set('cutAfterRound')} error={fe.cutAfterRound} />}
        {Number(f.days) > 1 && <TextField label="Cut: top N and ties" type="number" value={f.cutTop} onChange={set('cutTop')} />}
        <TextArea label="Description" value={f.description} onChange={set('description')} span />
        <Checkbox label="Public: listed and open for registration on the website" checked={pub} onChange={setPub} />
        <Checkbox label="Public leaderboard on the website (names with consent)" checked={lbPub} onChange={setLbPub} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function ImportHistory({ onClose }: { onClose: () => void }) {
  const [csv, setCsv] = useState('tournamentRef,tournamentName,startDate,endDate,tournamentType,format,category,division,hallOfFameDivision,position,playerName,memberNo,score,toPar,publicConsent\n');
  const [result, setResult] = useState<R | null>(null);
  const send = useSend<R, R>('POST', `${API}:import`, INV);
  const run = (mode: string) => send.mutate({ mode, csv } as unknown as R, { onSuccess: setResult });
  return (
    <Modal open onClose={onClose} title="Import Tournament History" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-neutral" disabled={send.isPending} onClick={() => run('preview')}>Preview</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending || !result || result.mode !== 'preview'} onClick={() => run('commit')}>Import</button>
    </>}>
      <p className="oc-muted">Results of past tournaments (Rhapsody / Excel) as CSV: one row per player result. Tournaments become a read-only archive;
        winners (position 1) become Hall of Fame champions, shown publicly only with the player's consent. Importing the same file again changes nothing.</p>
      <TextArea label="CSV" value={csv} onChange={(v) => { setCsv(v); setResult(null); }} rows={10} />
      <ErrorAlert error={send.error} />
      {result && (
        <Card title={result.mode === 'preview' ? 'Preview' : 'Imported'} icon="upload">
          <KV items={[['Rows', String(result.totalRows)], ['Tournaments', String(result.tournaments)], ['Results', String(result.results)],
            ['Champions', String(result.champions)], ['Rejected', String(result.failed)]]} />
          {list<R>(result.errors).length > 0 && <DataTable rows={list<R>(result.errors).map((e, i) => ({ ...e, id: String(i) }))}
            columns={[{ key: 'row', header: 'Row' }, { key: 'field', header: 'Field' }, { key: 'message', header: 'Problem' }]} />}
        </Card>
      )}
    </Modal>
  );
}

// ── Tournament (tabs) ─────────────────────────────────────────────────────

export function TournamentPage() {
  const { id = '', tab = 'overview' } = useParams();
  const nav = useNavigate();
  const { can } = useAuth();
  const [edit, setEdit] = useState(false);
  const t = useGet<R>(`${API}/${id}`);
  useTournamentStream(id, () => void t.refetch());
  if (t.error) return <ErrorAlert error={t.error} />;
  if (!t.data) return <div className="oc-card">Loading…</div>;
  const d = t.data;
  const status = String(d.status);
  const rounds = list<R>(d.rounds);
  const cur = rounds.find((r) => r.roundNo === d.currentRound);
  const imported = String(d.code).startsWith('HIST-');
  return (
    <div className="oc-stack">
      <PageHeader title={`${String(d.name)}`} help={`${String(d.code)} · ${String(d.courseName)} (${String(d.playingRouteName)}) · ${label(d.format)} ${label(d.scoringBasis)} · ${label(d.startType)}`}
        actions={<Actions>
          <StatusPill status={status} />
          {can('golf.tournament.manage') && !imported && status !== 'completed' && status !== 'cancelled' && <button className="oc-btn oc-btn-neutral" onClick={() => setEdit(true)}>Edit</button>}
          {can('golf.tournament.manage') && (status === 'draft' || status === 'closed') && <ActionButton kind="primary" label="Open Registration" path={`${API}/${id}:open-registration`} body={{}} invalidate={INV} />}
          {can('golf.tournament.manage') && status === 'open' && <ActionButton label="Close Registration" path={`${API}/${id}:close-registration`} body={{}} invalidate={INV} reason="optional" />}
          {can('golf.tournament_draw.manage') && cur && (cur.status === 'drawn' || cur.status === 'published') && <ActionButton label="Publish Draw" path={`${API}/${id}:publish-draw`} body={{}} invalidate={INV} />}
          {can('golf.tournament.start') && cur?.status === 'published' && <ActionButton kind="ink" label={d.startType === 'shotgun' ? 'Shotgun Start' : 'Start Round'} path={`${API}/${id}:start`} invalidate={INV} confirm={`Start round ${String(cur.roundNo)} now?`} />}
          {can('golf.tournament.finalize') && status === 'in_progress' && <ActionButton kind="primary" label="Finalize" path={`${API}/${id}:finalize`} invalidate={INV} confirm="Lock the results, award prizes and create the Hall of Fame champions?" />}
          {can('golf.tournament.manage') && !['completed', 'cancelled'].includes(status) && <ActionButton danger label="Cancel Tournament" path={`${API}/${id}:cancel`} invalidate={INV} reason="required" />}
        </Actions>} />
      <Tabs tabs={TOURNAMENT_SECTIONS} value={tab} onChange={(v) => nav(`/golf/tournaments/${id}/${v}`)} />
      {tab === 'overview' && <Overview t={d} />}
      {(tab === 'registration' || tab === 'participants') && <Participants t={d} register={tab === 'registration'} />}
      {(tab === 'flighting' || tab === 'tee-assignment') && <DrawPanel t={d} />}
      {tab === 'scoring' && <ScoringPanel t={d} />}
      {tab === 'leaderboard' && <LeaderboardPanel tid={id} />}
      {(tab === 'packages' || tab === 'fees') && <PackagesFees t={d} />}
      {tab === 'sponsors' && <SponsorsPanel t={d} />}
      {tab === 'prizes' && <PrizesPanel t={d} />}
      {tab === 'results' && <ResultsPanel tid={id} />}
      {edit && <TournamentForm t={d} onClose={() => setEdit(false)} onDone={() => { setEdit(false); void t.refetch(); }} />}
    </div>
  );
}

function Overview({ t }: { t: R }) {
  const { can } = useAuth();
  const pol = (t.policy ?? {}) as R;
  return (
    <div className="oc-grid">
      <Card title="Tournament" icon="emoji_events">
        <KV items={[['Type', label(t.tournamentType)], ['Eligibility', label(t.eligibility)], ['Field', `${String(t.registered)} / ${String(t.fieldSize)} (waitlist ${String(t.waitlisted)})`],
          ['Checked-in', String(t.checkedIn)], ['Players per flight', String(t.playersPerFlight)], ['Registration', t.registrationClosesAt ? `until ${formatDateTime(String(t.registrationClosesAt))}` : '—'],
          ['Handicap allowance', `${String(t.handicapAllowance ?? pol.handicapAllowancePercent)}%`], ['Maximum handicap', String(t.maxHandicap ?? pol.maxHandicap)],
          ['Tie-break', label(t.tieBreak ?? pol.tieBreak)], ['Website', t.public ? 'Public' : 'Not listed'], ['Quotation', String(t.quotationNumber ?? '—')],
          ['Policy version', String(t.policyVersion)]]} />
      </Card>
      <Card title="Schedule (course blocked on the tee sheet)" icon="calendar_month">
        <DataTable rows={list<R>(t.rounds)} columns={[{ key: 'roundNo', header: 'Round' }, { key: 'playDate', header: 'Date', render: (r) => formatDate(String(r.playDate)) },
          { key: 'startTime', header: 'Start' }, { key: 'flights', header: 'Flights', align: 'right' }, { key: 'status', header: 'Status', render: pill('status') }]} />
      </Card>
      <Card title="Divisions" icon="category" actions={can('golf.tournament.manage') ? <DivisionForm tid={t.id} /> : undefined}>
        <DataTable rows={list<R>(t.divisions)} columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Division' },
          { key: 'handicapMin', header: 'Handicap', render: (r) => `${String(r.handicapMin ?? '—')} – ${String(r.handicapMax ?? '—')}` },
          { key: 'gender', header: 'Gender', render: (r) => label(r.gender) }, { key: 'hallOfFameDivision', header: 'Hall of Fame', render: (r) => label(r.hallOfFameDivision ?? '—') },
          { key: 'players', header: 'Players', align: 'right' }]}
          actions={(r) => (can('golf.tournament.manage') ? <ActionButton danger label="Delete" method="DELETE" path={`${API}/${t.id}/divisions/${r.id}`} invalidate={INV} confirm={`Delete ${String(r.name)}?`} /> : null)} />
      </Card>
    </div>
  );
}

function DivisionForm({ tid }: { tid: string }) {
  const [open, setOpen] = useState(false);
  const [f, setF] = useState<Record<string, string>>({ code: '', name: '', handicapMin: '', handicapMax: '', gender: 'any', hallOfFameDivision: '' });
  const set = (k: string) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const send = useSend<R, R>('POST', `${API}/${tid}/divisions`, INV);
  return (
    <>
      <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setOpen(true)}>Add Division</button>
      <Modal open={open} onClose={() => setOpen(false)} title="Add Division" actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setOpen(false)}>Cancel</button>
        <button className="oc-btn oc-btn-primary" disabled={!f.code || !f.name || send.isPending} onClick={() => send.mutate({ code: f.code, name: f.name,
          handicapMin: f.handicapMin, handicapMax: f.handicapMax, gender: f.gender, hallOfFameDivision: f.hallOfFameDivision } as unknown as R, { onSuccess: () => setOpen(false) })}>Save</button>
      </>}>
        <div className="oc-form">
          <TextField label="Code" value={f.code} onChange={set('code')} required />
          <TextField label="Name" value={f.name} onChange={set('name')} required />
          <TextField label="Handicap from" value={f.handicapMin} onChange={set('handicapMin')} />
          <TextField label="Handicap to" value={f.handicapMax} onChange={set('handicapMax')} />
          <SelectField label="Gender" value={f.gender} onChange={set('gender')} options={opts(['any', 'male', 'female'])} />
          <SelectField label="Hall of Fame division" value={f.hallOfFameDivision} onChange={set('hallOfFameDivision')} placeholder="Not kept" options={opts(['men', 'ladies', 'senior', 'junior', 'open'])} />
        </div>
        <ErrorAlert error={send.error} />
      </Modal>
    </>
  );
}

// ── Registration & Participants ───────────────────────────────────────────

function Participants({ t, register }: { t: R; register: boolean }) {
  const { can } = useAuth();
  const [modal, setModal] = useState(register && can('golf.tournament_registration.manage'));
  const [pay, setPay] = useState<R | null>(null);
  const tid = t.id;
  const base = `${API}/${tid}/registrations`;
  return (
    <>
      <ListPage title="Participants" path={base} statuses={REG_STATUSES}
        actions={can('golf.tournament_registration.manage') && !['completed', 'cancelled'].includes(String(t.status))
          ? <button className="oc-btn oc-btn-primary" onClick={() => setModal(true)}>Register Player</button> : undefined}
        columns={[{ key: 'number', header: 'No.' }, { key: 'playerName', header: 'Player', render: (r) => <>{String(r.playerName)}<div className="oc-small oc-muted">{label(r.playerType)} · {label(r.channel)}</div></> },
          { key: 'handicapIndex', header: 'HCP', render: (r) => `${String(r.handicapIndex ?? '—')} (${label(r.handicapSource)})` },
          { key: 'divisionName', header: 'Division', render: (r) => String(r.divisionName ?? '—') }, { key: 'packageName', header: 'Package', render: (r) => String(r.packageName ?? '—') },
          { key: 'feeTotal', header: 'Fee', align: 'right', render: (r) => money(r.feeTotal) }, { key: 'paymentStatus', header: 'Payment', render: pill('paymentStatus') },
          { key: 'status', header: 'Status', render: (r) => <>{<StatusPill status={String(r.status)} />}{r.waitlistPosition ? ` #${String(r.waitlistPosition)}` : ''}</> }]}
        rowActions={(r) => <Actions>
          {can('golf.tournament_registration.check_in') && r.status === 'registered' && <ActionButton label="Check-in" path={`${base}/${r.id}:check-in`} invalidate={INV} />}
          {can('golf.tournament_registration.manage') && r.paymentStatus === 'pending' && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setPay(r)}>Pay</button>}
          {can('golf.tournament_registration.waive_fee') && r.paymentStatus === 'pending' && <ActionButton label="Waive Fee" path={`${base}/${r.id}:waive-fee`} invalidate={INV} reason="required" />}
          {can('golf.tournament_registration.manage') && (r.status === 'registered' || r.status === 'waitlisted') && <ActionButton danger label="Withdraw" path={`${base}/${r.id}:withdraw`} invalidate={INV} reason="required" />}
        </Actions>} />
      {modal && <RegisterForm t={t} onClose={() => setModal(false)} />}
      {pay && <PayForm path={`${base}/${pay.id}:pay`} r={pay} onClose={() => setPay(null)} />}
    </>
  );
}

function RegisterForm({ t, onClose }: { t: R; onClose: () => void }) {
  const toast = useToast();
  const [mode, setMode] = useState<'member' | 'customer' | 'guest'>('member');
  const [q, setQ] = useState('');
  const customers = useGet<Page<R>>(mode === 'customer' ? `/api/v1/crm/customers${qs({ q, limit: 20, 'filter[status]': 'active' })}` : null);
  const [f, setF] = useState<Record<string, string>>({ memberNo: '', customerId: '', name: '', phone: '', email: '', gender: '', handicapIndex: '',
    packageId: '', shirtSize: '', pairingGroup: '', sponsorId: '', payment: 'pay_later' });
  const [consent, setConsent] = useState(false);
  const set = (k: string) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const send = useSend<R, R>('POST', `${API}/${t.id}/registrations`, INV);
  const body: Record<string, unknown> = { handicapIndex: f.handicapIndex, packageId: opt(f.packageId), shirtSize: f.shirtSize, pairingGroup: f.pairingGroup,
    sponsorId: opt(f.sponsorId), publicConsent: consent, payment: f.payment };
  if (mode === 'member') body.memberNo = f.memberNo;
  if (mode === 'customer') body.customerId = opt(f.customerId);
  if (mode === 'guest') body.guest = { name: f.name, phone: f.phone, email: f.email, gender: opt(f.gender) };
  const fe = send.error?.fieldErrors ?? {};
  return (
    <Modal open onClose={onClose} title={`Register · ${String(t.name)}`} wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate(body as R, {
        onSuccess: (r) => { toast(`${String(r.playerName)}: ${label(r.status)}${r.waitlistPosition ? ` #${String(r.waitlistPosition)}` : ''}`); setF((x) => ({ ...x, memberNo: '', customerId: '', name: '', phone: '', email: '', handicapIndex: '' })); },
      })}>Register</button>
    </>}>
      <Tabs tabs={[{ value: 'member', label: 'Member' }, { value: 'customer', label: 'Customer' }, { value: 'guest', label: 'Guest' }]} value={mode} onChange={(v) => setMode(v as typeof mode)} />
      <div className="oc-form">
        {mode === 'member' && <TextField label="Member / card number" value={f.memberNo} onChange={set('memberNo')} error={fe.memberNo} required />}
        {mode === 'customer' && <>
          <TextField label="Find customer" value={q} onChange={setQ} placeholder="Name, phone or code" />
          <SelectField label="Customer" value={f.customerId} onChange={set('customerId')} placeholder="Select" error={fe.customerId}
            options={(customers.data?.items ?? []).map((c) => ({ value: c.id, label: `${String(c.name)}${c.code ? ` (${String(c.code)})` : ''}` }))} />
        </>}
        {mode === 'guest' && <>
          <TextField label="Name" value={f.name} onChange={set('name')} required />
          <TextField label="Phone" value={f.phone} onChange={set('phone')} />
          <TextField label="E-mail" type="email" value={f.email} onChange={set('email')} />
          <SelectField label="Gender" value={f.gender} onChange={set('gender')} placeholder="—" options={opts(['male', 'female'])} />
        </>}
        <TextField label="Official handicap index" value={f.handicapIndex} onChange={set('handicapIndex')} help="Empty: OneClub / PGI handicap of the player" error={fe.handicapIndex} />
        <SelectField label="Package" value={f.packageId} onChange={set('packageId')} placeholder="Default package" options={list<R>(t.packages).map((p) => ({ value: p.id, label: `${String(p.name)} (${money(p.guestTotal)})` }))} />
        <TextField label="Shirt size" value={f.shirtSize} onChange={set('shirtSize')} />
        <TextField label="Pairing group" value={f.pairingGroup} onChange={set('pairingGroup')} help="Same group → same flight (sponsor guests)" />
        <SelectField label="Sponsor guest of" value={f.sponsorId} onChange={set('sponsorId')} placeholder="—" options={list<R>(t.sponsors).map((s) => ({ value: s.id, label: String(s.name) }))} />
        <SelectField label="Payment" value={f.payment} onChange={set('payment')} options={opts(['pay_later', 'member_charge', 'online'], { pay_later: 'Pay at the desk' })} />
        <Checkbox label="Name may be shown on the public leaderboard and Hall of Fame" checked={consent} onChange={setConsent} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function PayForm({ path, r, onClose }: { path: string; r: R; onClose: () => void }) {
  const [method, setMethod] = useState('cash');
  const [ref, setRef] = useState('');
  const send = useSend<R, R>('POST', path, INV);
  return (
    <Modal open onClose={onClose} title={`Tournament fee · ${String(r.playerName)}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ methodType: method, reference: ref } as unknown as R, { onSuccess: onClose })}>Take Payment</button>
    </>}>
      <KV items={[['Fee', money(r.feeTotal)], ['Payment', label(r.paymentStatus)]]} />
      <div className="oc-form">
        <SelectField label="Method" value={method} onChange={setMethod} options={PAY_METHODS} />
        <TextField label="Reference" value={ref} onChange={setRef} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── Flighting & Tee Assignment ────────────────────────────────────────────

function DrawPanel({ t, desk }: { t: R; desk?: boolean }) {
  const { can } = useAuth();
  const tid = t.id;
  const rounds = list<R>(t.rounds);
  const [round, setRound] = useState(String(t.currentRound ?? 1));
  const draw = useGet<R>(`${API}/${tid}/flights${qs({ round })}`);
  const caddies = useGet<Page<R>>(can('golf.tournament_draw.manage') ? '/api/v1/golf/caddies?filter[status]=active&limit=300' : null);
  const [method, setMethod] = useState('handicap');
  const [move, setMove] = useState<{ registrationId: string; toFlightId: string }>({ registrationId: '', toFlightId: '' });
  const moveSend = useSend<R, R>('POST', `${API}/${tid}/flights:move-player`, INV);
  const caddySend = useSend<R & { fid: string }, R>('PATCH', (b) => `${API}/${tid}/flights/${b.fid}`, INV);
  useTournamentStream(tid, () => void draw.refetch());
  const flights = list<R>(draw.data?.flights);
  const manage = can('golf.tournament_draw.manage') && !desk;
  const players = flights.flatMap((f) => list<R>(f.players).map((p) => ({ value: String(p.registrationId), label: `${String(p.playerName)} (F${String(f.flightNo)})` })));
  return (
    <div className="oc-stack">
      <Actions>
        {rounds.length > 1 && <SelectField label="Round" value={round} onChange={setRound} options={rounds.map((r) => ({ value: String(r.roundNo), label: `Round ${String(r.roundNo)} · ${formatDate(String(r.playDate))}` }))} />}
        {draw.data && <StatusPill status={String(draw.data.status)} />}
        {manage && <SelectField label="Flighting" value={method} onChange={setMethod} options={opts(['handicap', 'division', 'random', 'standings'])} />}
        {manage && <ActionButton kind="primary" label="Make Draw" path={`${API}/${tid}/flights:generate`} body={{ round: Number(round), method }} invalidate={INV} />}
        {manage && <ActionButton label="Publish Draw" path={`${API}/${tid}:publish-draw`} body={{ round: Number(round) }} invalidate={INV} />}
        <button className="oc-btn oc-btn-neutral" onClick={() => void download('GET', `${API}/${tid}/start-sheet.pdf${qs({ round })}`, undefined, `start-sheet-${String(t.code)}-R${round}.pdf`)}>
          <Icon name="print" size={18} /> Start Sheet</button>
      </Actions>
      {draw.data && list<string>(draw.data.excluded).length > 0 && <p className="oc-muted">Not drawn: {list<string>(draw.data.excluded).join(', ')}</p>}
      {manage && flights.length > 0 && (
        <Card title="Move player (manual flighting)" icon="swap_horiz">
          <div className="oc-row-wrap">
            <SelectField label="Player" value={move.registrationId} onChange={(v) => setMove({ ...move, registrationId: v })} placeholder="Select" options={players} />
            <SelectField label="To flight" value={move.toFlightId} onChange={(v) => setMove({ ...move, toFlightId: v })} placeholder="New flight"
              options={flights.map((f) => ({ value: f.id, label: `Flight ${String(f.flightNo)} · ${String(f.startLabel)} (${list(f.players).length})` }))} />
            <button className="oc-btn oc-btn-primary" disabled={!move.registrationId || moveSend.isPending}
              onClick={() => moveSend.mutate({ round: Number(round), registrationId: move.registrationId, toFlightId: opt(move.toFlightId) } as unknown as R)}>Move</button>
          </div>
          <ErrorAlert error={moveSend.error} />
        </Card>
      )}
      <ErrorAlert error={draw.error ?? caddySend.error} />
      {flights.length === 0 && <Empty title="No draw yet" help="Make the draw once registration closes." icon="groups" />}
      <div className="oc-grid">
        {flights.map((f) => (
          <Card key={f.id} title={`Flight ${String(f.flightNo)} · ${t.startType === 'shotgun' ? `Hole ${String(f.startLabel)}` : `Tee ${String(f.startLabel)}`} · ${String(f.localTime)}`} icon="flag"
            actions={<>
              <StatusPill status={String(f.status)} />
              {can('golf.tournament.start') && f.status === 'scheduled' && draw.data?.status === 'in_progress' && <ActionButton kind="ink" label="Tee-Off" path={`${API}/${tid}/flights/${f.id}:tee-off`} invalidate={INV} />}
            </>}>
            <div className="oc-stack">
              {list<R>(f.players).map((p) => (
                <div key={String(p.registrationId)} className="oc-row-wrap">
                  <strong>{String(p.position)}. {String(p.playerName)}</strong>
                  <span className="oc-muted">HCP {String(p.playingHandicap ?? p.handicapIndex ?? '—')} · {String(p.divisionName ?? '')}</span>
                  <span className="oc-spacer" />
                  {manage ? (
                    <SelectField label="Caddy" value={String(p.caddyId ?? '')} placeholder="No caddy"
                      onChange={(v) => caddySend.mutate({ fid: f.id, caddies: [{ registrationId: p.registrationId, caddyId: v || '00000000-0000-0000-0000-000000000000' }] } as unknown as R & { fid: string })}
                      options={(caddies.data?.items ?? []).map((c) => ({ value: c.id, label: `#${String(c.code)} ${String(c.name)}` }))} />
                  ) : <span>{p.caddyCode ? `Caddy #${String(p.caddyCode)}` : ''}</span>}
                </div>
              ))}
            </div>
          </Card>
        ))}
      </div>
    </div>
  );
}

// ── Scoring (scoring desk) ────────────────────────────────────────────────

function ScoringPanel({ t, desk }: { t: R; desk?: boolean }) {
  const { can } = useAuth();
  const tid = t.id;
  const [round, setRound] = useState(String(t.currentRound ?? 1));
  const [card, setCard] = useState<R | null>(null);
  const [status, setStatus] = useState('');
  const scores = useGet<Page<R>>(`${API}/${tid}/scores${qs({ round, 'filter[status]': status })}`);
  useTournamentStream(tid, () => void scores.refetch());
  const validate = can('golf.tournament_score.validate');
  return (
    <div className="oc-stack">
      <Actions>
        {list<R>(t.rounds).length > 1 && <SelectField label="Round" value={round} onChange={setRound} options={list<R>(t.rounds).map((r) => ({ value: String(r.roundNo), label: `Round ${String(r.roundNo)}` }))} />}
        <SelectField label="Card status" value={status} onChange={setStatus} placeholder="All" options={opts(['not_started', 'in_progress', 'submitted', 'finalized', 'dq', 'wd', 'nr'])} />
      </Actions>
      <DataTable rows={scores.data?.items} loading={scores.isLoading} error={scores.error} onRowClick={(r) => setCard(r)}
        columns={[{ key: 'flightNo', header: 'Flight' }, { key: 'playerName', header: 'Player' }, { key: 'playingHandicap', header: 'PH', align: 'right' },
          { key: 'thru', header: 'Thru', align: 'right' }, { key: 'gross', header: 'Gross', align: 'right' }, { key: 'net', header: 'Net', align: 'right' },
          { key: 'points', header: 'Points', align: 'right' }, { key: 'attestedBy', header: 'Marker' }, { key: 'status', header: 'Card', render: pill('status') }]}
        actions={(r) => <Actions>
          {validate && !desk && ['in_progress', 'not_started', 'submitted'].includes(String(r.status)) && (
            <ActionButton label="DQ" path={`${API}/${tid}/scores/${r.scoreId}:set-status`} body={{ status: 'dq' }} invalidate={INV} reason="required" danger />)}
          {validate && !desk && ['dq', 'wd', 'nr'].includes(String(r.status)) && <ActionButton label="Reinstate" path={`${API}/${tid}/scores/${r.scoreId}:set-status`} body={{ status: 'reinstate' }} invalidate={INV} reason="required" />}
          {validate && r.status === 'submitted' && <ActionButton kind="primary" label="Validate" path={`${API}/${tid}/scores/${r.scoreId}:validate`} invalidate={INV} />}
        </Actions>} />
      {card && <ScoreCardModal tid={tid} row={card} round={Number(round)} onClose={() => setCard(null)} />}
    </div>
  );
}

/** One player's card: strokes per hole (paper card), attest, correct. */
function ScoreCardModal({ tid, row, round, onClose }: { tid: string; row: R; round: number; onClose: () => void }) {
  const { can, propertyId } = useAuth();
  const toast = useToast();
  const online = useOnline();
  const card = useGet<R>(`${API}/${tid}/scores/${row.scoreId}`);
  const [strokes, setStrokes] = useState<Record<number, string>>({});
  const [marker, setMarker] = useState('');
  const [reason, setReason] = useState('');
  const enter = useSend<R, R>('POST', `${API}/${tid}/scores`, INV);
  const attest = useSend<R, R>('POST', `${API}/${tid}/scores/${row.scoreId}:attest`, INV);
  const correct = useSend<R, R>('POST', `${API}/${tid}/scores/${row.scoreId}:correct`, INV);
  const holes = list<R>(card.data?.holes);
  const finalized = card.data?.status === 'finalized';
  const entries = Object.entries(strokes).filter(([, v]) => v !== '').map(([s, v]) => ({ seq: Number(s), strokes: Number(v), clientAt: new Date().toISOString() }));
  const save = async () => {
    if (!online) {
      await enqueue('golf.tournament_score', { tournamentId: tid, registrationId: row.registrationId, round, entries }, propertyId);
      toast('Saved on this device — synced when the connection returns');
      setStrokes({});
      return;
    }
    enter.mutate({ registrationId: row.registrationId, round, entries, source: 'staff' } as unknown as R, { onSuccess: () => { setStrokes({}); void card.refetch(); } });
  };
  return (
    <Modal open onClose={onClose} title={`${String(row.playerName)} · round ${round}`} wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      {!finalized && <button className="oc-btn oc-btn-primary" disabled={entries.length === 0 || enter.isPending} onClick={() => void save()}>Save Scores</button>}
      {finalized && can('golf.tournament_score.correct') && <button className="oc-btn oc-btn-primary" disabled={entries.length === 0 || !reason || correct.isPending}
        onClick={() => correct.mutate({ entries, reason } as unknown as R, { onSuccess: () => { setStrokes({}); void card.refetch(); } })}>Save Correction</button>}
    </>}>
      {card.data && <KV items={[['Handicap', `${String(card.data.handicapIndex ?? '—')} → playing ${String(card.data.playingHandicap ?? '—')}`],
        ['Gross / Net', `${String(card.data.gross ?? '—')} / ${String(card.data.net ?? '—')}`], ['Points', String(card.data.points)], ['Card', label(card.data.status)]]} />}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(84px, 1fr))', gap: 8, marginTop: 12 }}>
        {holes.map((h) => (
          <TextField key={String(h.seq)} label={`${String(h.holeNumber)} · par ${String(h.par)}${Number(h.strokesReceived) ? ` (+${String(h.strokesReceived)})` : ''}`} type="number"
            value={strokes[Number(h.seq)] ?? (h.strokes != null ? String(h.strokes) : '')} onChange={(v) => setStrokes({ ...strokes, [Number(h.seq)]: v })}
            help={h.points != null ? `${String(h.points)} pts` : undefined} />
        ))}
      </div>
      {finalized && <TextField label="Correction reason" value={reason} onChange={setReason} required />}
      {!finalized && can('golf.tournament_score.validate') && (
        <div className="oc-row-wrap" style={{ marginTop: 12 }}>
          <TextField label="Marker (attests the card)" value={marker} onChange={setMarker} />
          <button className="oc-btn oc-btn-neutral" disabled={!marker || attest.isPending} onClick={() => attest.mutate({ attestedBy: marker } as unknown as R, { onSuccess: () => void card.refetch() })}>Attest</button>
        </div>
      )}
      <ErrorAlert error={card.error ?? enter.error ?? attest.error ?? correct.error} />
    </Modal>
  );
}

// ── Leaderboard ───────────────────────────────────────────────────────────

export function LeaderboardBoards({ lb, rows }: { lb: R; rows?: number }) {
  const boards = list<R>(lb.boards);
  const [sel, setSel] = useState(0);
  const b = boards[Math.min(sel, boards.length - 1)];
  if (!b) return <Empty title="No leaderboard yet" icon="leaderboard" />;
  const entries = list<R>(b.entries).slice(0, rows ?? 500).map((e, i) => ({ ...e, id: String(e.registrationId ?? i) }));
  const stb = b.category === 'stableford';
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap" role="tablist">
        {boards.map((x, i) => <button key={i} className="oc-chip" role="tab" aria-selected={i === sel} aria-pressed={i === sel} onClick={() => setSel(i)}>{label(x.category)} · {String(x.division)}</button>)}
      </div>
      <DataTable rows={entries as R[]} columns={[{ key: 'positionLabel', header: 'Pos' },
        { key: 'playerName', header: 'Player', render: (r) => <>{String(r.playerName)}{r.tieBreak ? <div className="oc-small oc-muted">{String(r.tieBreak)}</div> : null}</> },
        { key: 'playingHandicap', header: 'PH', align: 'right' }, { key: 'thru', header: 'Thru', align: 'right' },
        stb ? { key: 'points', header: 'Points', align: 'right' } : { key: 'toPar', header: 'To par', align: 'right', render: (r) => (r.toPar == null ? '—' : Number(r.toPar) > 0 ? `+${String(r.toPar)}` : Number(r.toPar) === 0 ? 'E' : String(r.toPar)) },
        { key: 'score', header: stb ? 'Total' : 'Strokes', align: 'right' }]} />
    </div>
  );
}

function LeaderboardPanel({ tid }: { tid: string }) {
  const lb = useGet<R>(`${API}/${tid}/leaderboard`, { refetchInterval: 30_000 });
  useTournamentStream(tid, () => void lb.refetch());
  if (lb.error) return <ErrorAlert error={lb.error} />;
  if (!lb.data) return <div className="oc-card">Loading…</div>;
  return (
    <Card title={`Live Leaderboard · ${lb.data.final ? 'Final' : `Round ${String(lb.data.currentRound)} (${label(lb.data.roundStatus)})`}`} icon="leaderboard">
      <LeaderboardBoards lb={lb.data} />
      <p className="oc-small oc-muted">Updated {formatDateTime(String(lb.data.updatedAt))} · tie-break {label(lb.data.tieBreak)}</p>
    </Card>
  );
}

// ── Packages & Fees ───────────────────────────────────────────────────────

function PackagesFees({ t }: { t: R }) {
  const { can } = useAuth();
  const manage = can('golf.tournament.manage') && !['completed', 'cancelled'].includes(String(t.status));
  const [pkg, setPkg] = useState({ code: '', name: '', playerType: 'any' });
  const [isDefault, setIsDefault] = useState(false);
  const [fee, setFee] = useState({ packageId: '', component: 'entry_fee', name: '', playerType: 'any', amount: '', taxCodes: '' });
  const pkgSend = useSend<R, R>('POST', `${API}/${t.id}/packages`, INV);
  const feeSend = useSend<R, R>('POST', `${API}/${t.id}/fees`, INV);
  const pkgName = (id: unknown) => String(list<R>(t.packages).find((p) => p.id === id)?.name ?? 'Every registration');
  return (
    <div className="oc-grid">
      <Card title="Tournament Packages" icon="inventory_2">
        <DataTable rows={list<R>(t.packages)} columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Package' }, { key: 'playerType', header: 'For', render: (r) => label(r.playerType) },
          { key: 'memberTotal', header: 'Member', align: 'right', render: (r) => money(r.memberTotal) }, { key: 'guestTotal', header: 'Guest', align: 'right', render: (r) => money(r.guestTotal) },
          { key: 'isDefault', header: 'Default', render: (r) => (r.isDefault ? 'Yes' : '') }]}
          actions={(r) => (manage ? <ActionButton danger label="Delete" method="DELETE" path={`${API}/${t.id}/packages/${r.id}`} invalidate={INV} confirm={`Delete ${String(r.name)}?`} /> : null)} />
        {manage && <div className="oc-row-wrap">
          <TextField label="Code" value={pkg.code} onChange={(v) => setPkg({ ...pkg, code: v })} />
          <TextField label="Name" value={pkg.name} onChange={(v) => setPkg({ ...pkg, name: v })} />
          <SelectField label="For" value={pkg.playerType} onChange={(v) => setPkg({ ...pkg, playerType: v })} options={PLAYER_TYPES} />
          <Checkbox label="Default" checked={isDefault} onChange={setIsDefault} />
          <button className="oc-btn oc-btn-primary" disabled={!pkg.code || !pkg.name || pkgSend.isPending} onClick={() => pkgSend.mutate({ ...pkg, isDefault } as unknown as R, { onSuccess: () => setPkg({ code: '', name: '', playerType: 'any' }) })}>Add Package</button>
        </div>}
        <ErrorAlert error={pkgSend.error} />
      </Card>
      <Card title="Tournament Fees" icon="payments">
        <DataTable rows={list<R>(t.fees)} columns={[{ key: 'name', header: 'Fee' }, { key: 'component', header: 'Component', render: (r) => label(r.component) },
          { key: 'packageId', header: 'Package', render: (r) => pkgName(r.packageId) }, { key: 'playerType', header: 'For', render: (r) => label(r.playerType) },
          { key: 'amount', header: 'Amount (nett)', align: 'right', render: (r) => money(r.amount) }, { key: 'liability', header: 'Liability', render: (r) => (r.liability ? 'Yes' : '') }]}
          actions={(r) => (manage ? <ActionButton danger label="Delete" method="DELETE" path={`${API}/${t.id}/fees/${r.id}`} invalidate={INV} confirm={`Delete ${String(r.name)}?`} /> : null)} />
        {manage && <div className="oc-row-wrap">
          <SelectField label="Package" value={fee.packageId} onChange={(v) => setFee({ ...fee, packageId: v })} placeholder="Every registration" options={list<R>(t.packages).map((p) => ({ value: p.id, label: String(p.name) }))} />
          <SelectField label="Component" value={fee.component} onChange={(v) => setFee({ ...fee, component: v })} options={COMPONENTS} />
          <TextField label="Name" value={fee.name} onChange={(v) => setFee({ ...fee, name: v })} />
          <SelectField label="For" value={fee.playerType} onChange={(v) => setFee({ ...fee, playerType: v })} options={PLAYER_TYPES} />
          <MoneyField label="Amount" value={fee.amount} onChange={(v) => setFee({ ...fee, amount: v })} />
          <TextField label="Tax & Service codes" value={fee.taxCodes} onChange={(v) => setFee({ ...fee, taxCodes: v })} help="Comma separated" />
          <button className="oc-btn oc-btn-primary" disabled={!fee.name || !fee.amount || feeSend.isPending} onClick={() => feeSend.mutate({ packageId: opt(fee.packageId), component: fee.component,
            name: fee.name, playerType: fee.playerType, amount: fee.amount, taxCodes: fee.taxCodes ? fee.taxCodes.split(',').map((s) => s.trim()).filter(Boolean) : undefined } as unknown as R,
          { onSuccess: () => setFee({ ...fee, name: '', amount: '' }) })}>Add Fee</button>
        </div>}
        <ErrorAlert error={feeSend.error} />
      </Card>
    </div>
  );
}

// ── Sponsors & Prizes ─────────────────────────────────────────────────────

function SponsorsPanel({ t }: { t: R }) {
  const { can } = useAuth();
  const corps = useGet<Page<R>>('/api/v1/crm/corporate-accounts?limit=200&filter[status]=active');
  const [f, setF] = useState({ name: '', sponsorLevel: 'gold', packageName: '', corporateAccountId: '', amount: '', logoUrl: '', holes: '', contactEmail: '' });
  const send = useSend<R, R>('POST', `${API}/${t.id}/sponsors`, INV);
  const manage = can('golf.tournament_sponsor.manage') && !['completed', 'cancelled'].includes(String(t.status));
  return (
    <Card title="Sponsors" icon="handshake">
      <DataTable rows={list<R>(t.sponsors)} columns={[{ key: 'name', header: 'Sponsor', render: (r) => <>{r.logoUrl ? <img src={String(r.logoUrl)} alt="" style={{ height: 24, marginRight: 8, verticalAlign: 'middle' }} /> : null}{String(r.name)}</> },
        { key: 'sponsorLevel', header: 'Level', render: (r) => label(r.sponsorLevel) }, { key: 'packageName', header: 'Package' },
        { key: 'holes', header: 'Holes', render: (r) => list<number>(r.holes).join(', ') || '—' }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) },
        { key: 'invoiceNumber', header: 'Invoice', render: (r) => (r.invoiceNumber ? `${String(r.invoiceNumber)} (${label(r.invoiceStatus)})` : '—') }, { key: 'status', header: 'Status', render: pill('status') }]}
        actions={(r) => <Actions>
          {can('golf.tournament_sponsor.invoice') && !r.invoiceId && Number(r.amount) > 0 && <ActionButton kind="primary" label="Invoice" path={`${API}/${t.id}/sponsors/${r.id}:invoice`} body={{}} invalidate={INV} />}
          {manage && !r.folioId && <ActionButton danger label="Delete" method="DELETE" path={`${API}/${t.id}/sponsors/${r.id}`} invalidate={INV} confirm={`Delete ${String(r.name)}?`} />}
        </Actions>} />
      {manage && <div className="oc-row-wrap">
        <TextField label="Name" value={f.name} onChange={(v) => setF({ ...f, name: v })} />
        <SelectField label="Level" value={f.sponsorLevel} onChange={(v) => setF({ ...f, sponsorLevel: v })} options={LEVELS} />
        <TextField label="Sponsor package" value={f.packageName} onChange={(v) => setF({ ...f, packageName: v })} />
        <SelectField label="Billed to" value={f.corporateAccountId} onChange={(v) => setF({ ...f, corporateAccountId: v })} placeholder="—" options={(corps.data?.items ?? []).map((c) => ({ value: c.id, label: String(c.name) }))} />
        <MoneyField label="Amount" value={f.amount} onChange={(v) => setF({ ...f, amount: v })} />
        <TextField label="Logo URL" value={f.logoUrl} onChange={(v) => setF({ ...f, logoUrl: v })} />
        <TextField label="Sponsored holes" value={f.holes} onChange={(v) => setF({ ...f, holes: v })} help="e.g. 3, 7" />
        <TextField label="Contact e-mail" value={f.contactEmail} onChange={(v) => setF({ ...f, contactEmail: v })} />
        <button className="oc-btn oc-btn-primary" disabled={!f.name || send.isPending} onClick={() => send.mutate({ name: f.name, sponsorLevel: f.sponsorLevel, packageName: f.packageName,
          corporateAccountId: opt(f.corporateAccountId), amount: f.amount || '0', logoUrl: f.logoUrl, contactEmail: f.contactEmail,
          holes: f.holes ? f.holes.split(',').map((s) => Number(s.trim())).filter(Boolean) : undefined } as unknown as R, { onSuccess: () => setF({ ...f, name: '', amount: '' }) })}>Add Sponsor</button>
      </div>}
      <ErrorAlert error={send.error} />
    </Card>
  );
}

function PrizesPanel({ t }: { t: R }) {
  const { can } = useAuth();
  const [f, setF] = useState({ category: 'stableford', divisionId: '', position: '1', holeNumber: '', name: '', value: '', sponsorId: '' });
  const [award, setAward] = useState<R | null>(null);
  const send = useSend<R, R>('POST', `${API}/${t.id}/prizes`, INV);
  const manage = can('golf.tournament_prize.manage') && !['completed', 'cancelled'].includes(String(t.status));
  const ranking = ['gross', 'net', 'stableford'].includes(f.category);
  return (
    <Card title="Prizes" icon="military_tech">
      <DataTable rows={list<R>(t.prizes)} columns={[{ key: 'name', header: 'Prize' }, { key: 'category', header: 'Category', render: (r) => `${label(r.category)}${r.position ? ` #${String(r.position)}` : ''}${r.holeNumber ? ` · hole ${String(r.holeNumber)}` : ''}` },
        { key: 'divisionName', header: 'Division', render: (r) => String(r.divisionName ?? 'Overall') }, { key: 'value', header: 'Value', align: 'right', render: (r) => money(r.value) },
        { key: 'sponsorName', header: 'Sponsor' }, { key: 'recipientName', header: 'Winner', render: (r) => (r.recipientName ? `${String(r.recipientName)}${r.resultText ? ` (${String(r.resultText)})` : ''}` : '—') },
        { key: 'status', header: 'Status', render: pill('status') }]}
        actions={(r) => <Actions>
          {can('golf.tournament_prize.award') && ['nearest_to_pin', 'longest_drive', 'lucky_draw', 'other'].includes(String(r.category)) && r.status === 'open' && ['in_progress', 'completed'].includes(String(t.status)) &&
            <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setAward(r)}>Award</button>}
          {can('golf.tournament_prize.award') && r.status === 'awarded' && <ActionButton label="Handed Over" path={`${API}/${t.id}/prizes/${r.id}:hand-over`} body={{}} invalidate={INV} />}
          {manage && r.status === 'open' && <ActionButton danger label="Delete" method="DELETE" path={`${API}/${t.id}/prizes/${r.id}`} invalidate={INV} confirm={`Delete ${String(r.name)}?`} />}
        </Actions>} />
      {manage && <div className="oc-row-wrap">
        <SelectField label="Category" value={f.category} onChange={(v) => setF({ ...f, category: v })} options={PRIZE_CATS} />
        {ranking && <SelectField label="Division" value={f.divisionId} onChange={(v) => setF({ ...f, divisionId: v })} placeholder="Overall" options={list<R>(t.divisions).map((d) => ({ value: d.id, label: String(d.name) }))} />}
        {ranking && <TextField label="Position" type="number" value={f.position} onChange={(v) => setF({ ...f, position: v })} />}
        {!ranking && <TextField label="Hole" type="number" value={f.holeNumber} onChange={(v) => setF({ ...f, holeNumber: v })} />}
        <TextField label="Name" value={f.name} onChange={(v) => setF({ ...f, name: v })} />
        <TextField label="Value" type="number" value={f.value} onChange={(v) => setF({ ...f, value: v })} />
        <SelectField label="Sponsor" value={f.sponsorId} onChange={(v) => setF({ ...f, sponsorId: v })} placeholder="—" options={list<R>(t.sponsors).map((s) => ({ value: s.id, label: String(s.name) }))} />
        <button className="oc-btn oc-btn-primary" disabled={!f.name || send.isPending} onClick={() => send.mutate({ category: f.category, divisionId: ranking ? opt(f.divisionId) : undefined,
          position: ranking ? num(f.position) : undefined, holeNumber: ranking ? undefined : num(f.holeNumber), name: f.name, value: f.value || '0', sponsorId: opt(f.sponsorId) } as unknown as R,
        { onSuccess: () => setF({ ...f, name: '', value: '' }) })}>Add Prize</button>
      </div>}
      <ErrorAlert error={send.error} />
      {award && <AwardForm t={t} prize={award} onClose={() => setAward(null)} />}
    </Card>
  );
}

function AwardForm({ t, prize, onClose }: { t: R; prize: R; onClose: () => void }) {
  const regs = useGet<Page<R>>(`${API}/${t.id}/registrations?filter[status]=registered,checked_in`);
  const [rid, setRid] = useState('');
  const [result, setResult] = useState('');
  const send = useSend<R, R>('POST', `${API}/${t.id}/prizes/${prize.id}:award`, INV);
  return (
    <Modal open onClose={onClose} title={`Award · ${String(prize.name)}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!rid || send.isPending} onClick={() => send.mutate({ registrationId: rid, resultText: result } as unknown as R, { onSuccess: onClose })}>Award</button>
    </>}>
      <div className="oc-form">
        <SelectField label="Player" value={rid} onChange={setRid} placeholder="Select" options={(regs.data?.items ?? []).map((r) => ({ value: r.id, label: String(r.playerName) }))} />
        <TextField label="Result" value={result} onChange={setResult} help="e.g. 1.35 m, 285 m" />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function ResultsPanel({ tid }: { tid: string }) {
  const res = useGet<R>(`${API}/${tid}/results`);
  if (res.error) return <ErrorAlert error={res.error} />;
  if (!res.data) return <div className="oc-card">Loading…</div>;
  const rows = list<R>(res.data.results);
  if (rows.length === 0) return <Empty title="Results are final after Finalize" icon="emoji_events" />;
  return (
    <div className="oc-stack">
      <Card title="Champions (Hall of Fame)" icon="emoji_events">
        <DataTable rows={list<R>(res.data.champions).map((c, i) => ({ ...c, id: String(i) }) as R)} columns={[{ key: 'division', header: 'Division' },
          { key: 'category', header: 'Category', render: (r) => label(r.category) }, { key: 'playerName', header: 'Champion' }, { key: 'score', header: 'Score', align: 'right' }]} />
      </Card>
      <Card title="Results" icon="format_list_numbered">
        <DataTable rows={rows} columns={[{ key: 'category', header: 'Category', render: (r) => label(r.category) }, { key: 'divisionName', header: 'Division', render: (r) => String(r.divisionName ?? 'Overall') },
          { key: 'positionLabel', header: 'Pos' }, { key: 'playerName', header: 'Player' }, { key: 'score', header: 'Score', align: 'right' }, { key: 'tieBreak', header: 'Tie-break' }]} />
      </Card>
    </div>
  );
}

// ── Tournament Desk (ops) ─────────────────────────────────────────────────

/** Tournament of the desk: today's (or the selected) running tournament. */
function useDeskTournament() {
  const [sel, setSel] = useState('');
  const list0 = useGet<Page<R>>(`${API}?filter[status]=open,closed,in_progress&filter[source]=oneclub&limit=50`);
  const items = useMemo(() => [...(list0.data?.items ?? [])].sort((a, b) => String(a.startDate).localeCompare(String(b.startDate))), [list0.data]);
  const id = sel || (items.find((t) => t.status === 'in_progress')?.id ?? items[0]?.id ?? '');
  const t = useGet<R>(id ? `${API}/${id}` : null);
  const picker = items.length > 1 ? <SelectField label="Tournament" value={id} onChange={setSel} options={items.map((x) => ({ value: x.id, label: `${String(x.name)} · ${formatDate(String(x.startDate))}` }))} /> : null;
  return { t: t.data, picker, loading: list0.isLoading || t.isLoading, empty: !list0.isLoading && items.length === 0 };
}

function DeskFrame({ title, children }: { title: string; children: (t: R) => React.ReactNode }) {
  const { t, picker, loading, empty } = useDeskTournament();
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>{title}</h1>{t && <p>{String(t.name)} · {formatDate(String(t.startDate))} · <StatusPill status={String(t.status)} /></p>}</div></div>
      <div className="oc-row-wrap">
        <Link className="oc-btn oc-btn-neutral" to="/ops/tournament-desk">Check-in</Link>
        <Link className="oc-btn oc-btn-neutral" to="/ops/tournament-desk/draw">Draw</Link>
        <Link className="oc-btn oc-btn-neutral" to="/ops/tournament-desk/scoring">Scoring</Link>
        <Link className="oc-btn oc-btn-neutral" to="/ops/tournament-desk/leaderboard">Leaderboard</Link>
        {picker}
      </div>
      {loading && !t && <div className="oc-card">Loading…</div>}
      {empty && <Empty title="No tournament today" icon="emoji_events" />}
      {t && children(t)}
    </div>
  );
}

/** Registration Check-in (touch-first; queued offline, FR-OPS-P3-05). */
function DeskCheckInPage() {
  return <DeskFrame title="Tournament Desk">{(t) => <DeskCheckIn t={t} />}</DeskFrame>;
}

function DeskCheckIn({ t }: { t: R }) {
  const { propertyId, can } = useAuth();
  const toast = useToast();
  const online = useOnline();
  const [q, setQ] = useState('');
  const [done, setDone] = useState<Record<string, boolean>>({});
  const regs = useGet<Page<R>>(`${API}/${t.id}/registrations${qs({ q, 'filter[status]': 'registered,checked_in' })}`);
  useTournamentStream(t.id, () => void regs.refetch());
  const checkIn = async (r: R) => {
    await enqueue('golf.tournament_check_in', { tournamentId: t.id, registrationId: r.id }, propertyId);
    setDone({ ...done, [r.id]: true });
    toast(`${String(r.playerName)} checked in${online ? '' : ' (queued)'}`);
  };
  const rows = regs.data?.items ?? [];
  return (
    <div className="oc-stack">
      <TextField label="Find player" value={q} onChange={setQ} placeholder="Name or registration number" />
      <KV items={[['Checked-in', `${rows.filter((r) => r.status === 'checked_in' || done[r.id]).length} / ${rows.length}`]]} />
      <div className="oc-stack">
        {rows.map((r) => {
          const inn = r.status === 'checked_in' || done[r.id];
          return (
            <div key={r.id} className="oc-card oc-row-wrap" style={{ minHeight: 64 }}>
              <div><strong>{String(r.playerName)}</strong><div className="oc-small oc-muted">{String(r.number)} · HCP {String(r.handicapIndex ?? '—')} · {String(r.divisionName ?? '')}</div></div>
              <span className="oc-spacer" />
              <StatusPill status={String(r.paymentStatus)} />
              {inn ? <StatusPill status="checked_in" /> : can('golf.tournament_registration.check_in') &&
                <button className="oc-btn oc-btn-ink" style={{ minHeight: 48, minWidth: 140 }} onClick={() => void checkIn(r)}>Check-in</button>}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function DeskDrawPage() {
  return <DeskFrame title="Draw & Start Sheet">{(t) => <DrawPanel t={t} desk />}</DeskFrame>;
}

function DeskScoringPage() {
  return <DeskFrame title="Scoring Desk">{(t) => <ScoringPanel t={t} desk />}</DeskFrame>;
}

function DeskLeaderboardPage() {
  return <DeskFrame title="Leaderboard">{(t) => <LeaderboardPanel tid={t.id} />}</DeskFrame>;
}

/** Shotgun Start (Starter): publish check and the start of the round. */
function ShotgunStartPage() {
  return (
    <DeskFrame title="Shotgun Start">{(t) => {
      const cur = list<R>(t.rounds).find((r) => r.roundNo === t.currentRound);
      return (
        <Card title={`Round ${String(cur?.roundNo ?? '')} · ${label(t.startType)} ${String(cur?.startTime ?? '')}`} icon="sports_golf">
          <KV items={[['Flights', String(cur?.flights ?? 0)], ['Checked-in', `${String(t.checkedIn)} / ${String(t.registered)}`], ['Round', label(cur?.status)]]} />
          <div className="oc-row-wrap" style={{ marginTop: 16 }}>
            {cur?.status === 'published'
              ? <ActionButton kind="ink" label={t.startType === 'shotgun' ? 'Shotgun Start — all flights tee off' : 'Start Round'} path={`${API}/${t.id}:start`} invalidate={INV}
                confirm={`Start round ${String(cur.roundNo)} of ${String(t.name)} now?`} />
              : <p className="oc-muted">{cur?.status === 'in_progress' ? 'The round is in play.' : 'The draw must be published first.'}</p>}
          </div>
        </Card>
      );
    }}</DeskFrame>
  );
}

/** Back Office routes of the area. */
export const TOURNAMENT_ROUTES: AreaRoute[] = [
  { path: 'golf/tournaments', perm: 'golf.tournament.view', element: <TournamentsPage /> },
  { path: 'golf/tournaments/:id', perm: 'golf.tournament.view', element: <TournamentPage /> },
  { path: 'golf/tournaments/:id/:tab', perm: 'golf.tournament.view', element: <TournamentPage /> },
  { path: 'golf/tournament-section/:tab', perm: 'golf.tournament.view', element: <TournamentSectionPage /> },
];

/** Ops workstation tiles and routes of the area. */
export const TOURNAMENT_OPS_TILES: OpsTile[] = [
  ['emoji_events', 'Tournament Desk', '/ops/tournament-desk', 'golf.tournament_registration.check_in'],
  ['sports_golf', 'Shotgun Start', '/ops/tournament-desk/start', 'golf.tournament.start'],
];
export const TOURNAMENT_OPS_ROUTES: OpsRoute[] = [
  { path: 'tournament-desk', element: <DeskCheckInPage /> },
  { path: 'tournament-desk/draw', element: <DeskDrawPage /> },
  { path: 'tournament-desk/scoring', element: <DeskScoringPage /> },
  { path: 'tournament-desk/leaderboard', element: <DeskLeaderboardPage /> },
  { path: 'tournament-desk/start', element: <ShotgunStartPage /> },
];

// ── Leaderboard Screen (FR-OPS-P3-04) ─────────────────────────────────────

/** Leaderboard Screen kiosk (`/screen/leaderboard`, role Screen): rotates the
 * boards of the tournaments in play; refreshes on every score (SSE). */
export function LeaderboardScreenPage() {
  const feed = useGet<R>('/api/v1/golf/tournament-screen', { refetchInterval: 60_000 });
  const boards = list<R>(feed.data?.leaderboards).flatMap((lb) => list<R>(lb.boards).map((b) => ({ lb, b })));
  const [i, setI] = useState(0);
  const tid = list<R>(feed.data?.leaderboards)[0]?.tournamentId as string | undefined;
  useTournamentStream(tid ?? null, () => void feed.refetch());
  useEffect(() => {
    const t = setInterval(() => setI((x) => x + 1), Number(feed.data?.rotateSeconds ?? 15) * 1000);
    return () => clearInterval(t);
  }, [feed.data?.rotateSeconds]);
  const cur = boards.length ? boards[i % boards.length] : null;
  if (!cur) {
    return <main className="oc-screen"><Icon name="leaderboard" size={72} /><h1 style={{ fontSize: 48 }}>Leaderboard</h1>
      {feed.isSuccess && <div style={{ opacity: 0.7, fontSize: 20 }}>No tournament in play.</div>}</main>;
  }
  const stb = cur.b.category === 'stableford';
  const sponsors = list<R>(cur.lb.sponsors);
  return (
    <main className="oc-screen" style={{ justifyContent: 'flex-start', paddingTop: 32 }}>
      <div className="oc-small" style={{ opacity: 0.7, textTransform: 'uppercase', letterSpacing: 2 }}>{label(cur.b.category)} · {String(cur.b.division)}</div>
      <h1 style={{ fontSize: 44, margin: '8px 0 16px' }}>{String(cur.lb.name)}</h1>
      <table style={{ width: 'min(1100px, 95vw)', fontSize: 26, borderCollapse: 'collapse' }}>
        <thead><tr style={{ opacity: 0.7, textAlign: 'left' }}><th>Pos</th><th>Player</th><th style={{ textAlign: 'right' }}>Thru</th><th style={{ textAlign: 'right' }}>{stb ? 'Points' : 'To par'}</th></tr></thead>
        <tbody>
          {list<R>(cur.b.entries).slice(0, Number(feed.data?.rows ?? 20)).map((e, k) => (
            <tr key={k} style={{ borderTop: '1px solid rgba(255,255,255,0.15)' }}>
              <td>{String(e.positionLabel)}</td><td>{String(e.playerName)}</td><td style={{ textAlign: 'right' }}>{String(e.thru)}</td>
              <td style={{ textAlign: 'right' }}>{stb ? String(e.points ?? '—') : e.toPar == null ? '—' : Number(e.toPar) > 0 ? `+${String(e.toPar)}` : Number(e.toPar) === 0 ? 'E' : String(e.toPar)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {sponsors.length > 0 && <div className="oc-row-wrap" style={{ marginTop: 24, opacity: 0.8 }}>
        {sponsors.map((s) => (s.logoUrl ? <img key={String(s.name)} src={String(s.logoUrl)} alt={String(s.name)} style={{ height: 48 }} /> : <span key={String(s.name)}>{String(s.name)}</span>))}
      </div>}
    </main>
  );
}

// ── Caddy Tablet: Scorecard in tournament format (PRD P3 §7.3) ────────────

/** Today's tournament flights on My Assignments. */
export function TabletTournamentCard() {
  const my = useGet<Page<R>>('/api/v1/golf/my-tournament-flights', { refetchInterval: 60_000, retry: false });
  const items = my.data?.items ?? [];
  if (items.length === 0) return null;
  return (
    <Card title="Tournament" icon="emoji_events">
      <div className="oc-stack">
        {items.map((f) => {
          const fl = (f.flight ?? {}) as R;
          return (
            <Link key={String(fl.id)} to={`/tablet/tournament/${String(f.tournamentId)}/${String(fl.id)}`} className="oc-card oc-card-ink" style={{ textDecoration: 'none' }}>
              <div className="oc-small" style={{ opacity: 0.7 }}>{String(f.tournamentName)} · round {String(f.roundNo)} · {label(f.format)}</div>
              <h2 style={{ margin: '4px 0' }}>Flight {String(fl.flightNo)} · start {String(fl.startLabel)} · {String(fl.localTime)}</h2>
              <div>{list<R>(fl.players).map((p) => String(p.playerName)).join(', ')}</div>
            </Link>
          );
        })}
      </div>
    </Card>
  );
}

/** Scorecard of a tournament flight on the tablet: strokes per hole for
 * each player with strokes received and stableford points, queued offline
 * and synced by device time (last write wins per hole). */
export function TabletTournamentPage() {
  const { tid = '', fid = '' } = useParams();
  const { propertyId } = useAuth();
  const toast = useToast();
  const online = useOnline();
  const live = useGet<R>(`${API}/${tid}/flights/${fid}/scorecards`, { retry: false });
  const [data, setData] = useState<R | null>(null);
  const [seq, setSeq] = useState(1);
  const [entered, setEntered] = useState<Record<string, Record<number, number>>>({});
  useEffect(() => {
    const key = `tournament:${fid}`;
    if (live.data) {
      setData(live.data);
      void cachePut(propertyId, key, live.data);
    } else if (live.isError) {
      void cacheGet<R>(propertyId, key).then((c) => c && setData(c));
    }
  }, [live.data, live.isError, fid, propertyId]);
  if (!data) return live.isLoading ? <div className="oc-card">Loading scorecard…</div> : <Empty title="Scorecard not available offline" icon="cloud_off" />;
  const cards = list<R>(data.scorecards);
  const holes = list<R>(cards[0]?.holes);
  const hole = holes[seq - 1];
  const stb = data.format === 'stableford';
  const value = (c: R, s: number) => entered[String(c.registrationId)]?.[s] ?? (list<R>(c.holes)[s - 1]?.strokes as number | undefined);
  const set = async (c: R, s: number, v: number) => {
    setEntered({ ...entered, [String(c.registrationId)]: { ...(entered[String(c.registrationId)] ?? {}), [s]: v } });
    await enqueue('golf.tournament_score', { tournamentId: tid, registrationId: c.registrationId, round: data.roundNo,
      entries: [{ seq: s, strokes: v, clientAt: new Date().toISOString() }], deviceId: 'tablet' }, propertyId);
    if (!online) toast('Saved on the tablet — synced when the signal returns');
  };
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>{String(data.tournamentName)}</h1><p>Round {String(data.roundNo)} · {label(data.format)} · <StatusPill status={String(data.roundStatus)} /></p></div></div>
      {hole && (
        <Card title={`Hole ${String(hole.holeNumber)} · par ${String(hole.par)} · SI ${String(hole.strokeIndex ?? '—')}`} icon="flag">
          <div className="oc-stack">
            {cards.map((c) => {
              const v = value(c, seq);
              const h = list<R>(c.holes)[seq - 1];
              const recv = Number(h?.strokesReceived ?? 0);
              const pts = v ? Math.max(0, 2 + Number(hole.par) + recv - v) : null;
              return (
                <div key={String(c.registrationId)} className="oc-row-wrap" style={{ minHeight: 56 }}>
                  <div><strong>{String(c.playerName)}</strong><div className="oc-small oc-muted">PH {String(c.playingHandicap ?? '—')}{recv ? ` · +${recv} on this hole` : ''}</div></div>
                  <span className="oc-spacer" />
                  {[-1, 0, 1, 2].map((d) => {
                    const s = Number(hole.par) + d;
                    return <button key={d} className={`oc-btn ${v === s ? 'oc-btn-ink' : 'oc-btn-outline'}`} style={{ minWidth: 52, minHeight: 48 }}
                      aria-label={`${String(c.playerName)} ${s} strokes`} onClick={() => void set(c, seq, s)}>{s}</button>;
                  })}
                  <TextField label="Strokes" type="number" value={v ? String(v) : ''} onChange={(x) => { if (x) void set(c, seq, Number(x)); }} />
                  {stb && <span className="oc-small">{pts ?? '—'} pts</span>}
                </div>
              );
            })}
          </div>
        </Card>
      )}
      <div className="oc-row-wrap">
        <button className="oc-btn oc-btn-outline" disabled={seq <= 1} onClick={() => setSeq(seq - 1)}>← Hole</button>
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-ink" disabled={seq >= holes.length} onClick={() => setSeq(seq + 1)}>Hole →</button>
      </div>
      <Card title="Scorecard" icon="scoreboard">
        <DataTable rows={cards.map((c) => ({ ...c, id: String(c.registrationId) }) as R)} columns={[{ key: 'playerName', header: 'Player' }, { key: 'thru', header: 'Thru', align: 'right' },
          { key: 'gross', header: 'Gross', align: 'right' }, { key: 'net', header: 'Net', align: 'right' }, { key: 'points', header: 'Points', align: 'right' }]} />
      </Card>
    </div>
  );
}
