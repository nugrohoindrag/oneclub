import { useCallback, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { qs, request, useGet, type Page } from '@oneclub/api-client';
import { formatNumber } from '@oneclub/i18n';
import {
  Amount, BreakdownList, Card, ColumnChart, DashButton, DashCard, DashGrid, DashHead, DashTable, DataTable, Empty, ErrorAlert, Icon, MiniCard, Modal,
  PageHeader, ProgressRow, SelectField, Skeleton, StatusPill, TextField, useAuth, useToast,
} from '@oneclub/shell';
import {
  BookingDrawer, CHANNEL, ChannelTag, PayPill, STATE, StatePill, addDays, dayShort, hm, money, useBoardLive, useFacilities, ymd, type CourtBooking, type R,
} from './shared';
import './sport.css';

// Dashboard Admin Sport Club (docs/requirement-booking-sportclub-mgcc.md §6):
// Overview, Kalender Lapangan, Reservasi, Booking Rutin, Blokir Slot, Paket &
// Voucher and Laporan & Ekspor for the Sport Club Manager. The KPIs follow one
// definition (§13.2): utilisation = booked hours ÷ (open − blocked hours),
// revenue per day of play before tax, no-show rate on started lines.

export interface Report {
  from: string; to: string; bookings: number; bookedHours: string; utilization: string; utilizationNormal: string; revenue: string; serviceFees: string;
  extras: string; packageUse: string; packageSales: string; noShows: number; noShowRate: string; awaitingPayment: number;
  bySport: UtilRow[]; byCourt: UtilRow[]; heatmap: { weekday: number; hour: number; booked: number; open: number; rate: string }[];
  byChannel: { key: string; count: number; hours?: string; amount: string }[]; byMethod: { key: string; count: number; amount: string }[];
  noShowBySport: { key: string; label: string; count: number }[];
  topCustomers: { customerId: string | null; name: string; phone: string | null; bookings: number; hours: string; spend: string; noShows: number; member: boolean;
    favorite: string | null; prospect: boolean }[];
  upcoming: CourtBooking[]; alerts: { kind: string; message: string; bookingId?: string }[]; outsideNormalHours: CourtBooking[];
}
export interface UtilRow { key: string; label: string; facility?: string; openHours: string; normalHours: string; bookedHours: string; utilization: string;
  utilizationNormal: string; revenue: string; revenuePerHour: string; bookings: number; noShows: number }

const COLORS = ['var(--dash-blue)', 'var(--dash-ink)', 'var(--dash-lime)', 'var(--dash-sky)', 'var(--dash-amber)', 'var(--dash-green)', 'var(--dash-red)'];
const short = (v: unknown) => {
  const n = Number(v ?? 0);
  return n >= 1e9 ? `Rp ${(n / 1e9).toFixed(1)} M` : n >= 1e6 ? `Rp ${(n / 1e6).toFixed(1)} jt` : money(n);
};

function SportFilter({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const f = useFacilities();
  return (
    <select className="oc-input oc-filter" aria-label="Cabor" value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">Semua cabor</option>
      {f.list.map((x) => <option key={String(x.id)} value={String(x.id)}>{String(x.name)}</option>)}
    </select>
  );
}

// ── Overview (FR-71) ────────────────────────────────────────────────────────

export function SportOverviewPage() {
  const [from, setFrom] = useState(ymd());
  const [to, setTo] = useState(ymd());
  const [sport, setSport] = useState('');
  const [open, setOpen] = useState<string | null>(null);
  const r = useGet<Report>(`/api/v1/sportclub/overview${qs({ from, to, facilityId: sport || undefined })}`, { refetchInterval: 60_000 });
  useBoardLive(useCallback(() => { void r.refetch(); }, [r]));
  const controls = <>
    <input type="date" className="oc-input oc-filter" aria-label="Dari" value={from} onChange={(e) => e.target.value && setFrom(e.target.value)} />
    <input type="date" className="oc-input oc-filter" aria-label="Sampai" value={to} onChange={(e) => e.target.value && setTo(e.target.value)} />
    <SportFilter value={sport} onChange={setSport} />
    <DashButton icon="add" tone="blue" to="/ops/sport/new">Booking Baru</DashButton>
  </>;
  if (r.error) return <div className="oc-dash-page"><DashHead title="Sport Club Overview" controls={controls} /><ErrorAlert error={r.error} /></div>;
  if (!r.data) return <div className="oc-dash-page"><DashHead title="Sport Club Overview" controls={controls} /><Skeleton rows={10} /></div>;
  const x = r.data;
  return (
    <div className="oc-dash-page">
      <DashHead title="Sport Club Overview" sub={from === to ? dayShort(`${from}T00:00:00`) : `${from} – ${to}`} controls={controls} />
      <DashGrid>
        <MiniCard span={3} label="Booking" value={<Amount text={formatNumber(x.bookings)} size="md" />} delta={<span className="oc-dash-chip-suffix">{x.bookedHours} jam</span>}
          to="/sport-club/reservations" />
        <MiniCard span={3} label="Utilisasi lapangan" value={<Amount text={`${x.utilization}%`} size="md" />}
          delta={<span className="oc-dash-chip-suffix">jam buka normal {x.utilizationNormal}%</span>} />
        <MiniCard span={3} label="Pendapatan (sebelum pajak, per tanggal main)" value={<Amount text={short(x.revenue)} size="md" />} />
        <MiniCard span={3} label="No-show" value={<Amount text={`${x.noShows} · ${x.noShowRate}%`} size="md" />} />
      </DashGrid>
      <DashGrid>
        <DashCard span={6} icon="sports_tennis" tone="blue" title="Utilisasi per cabor">
          {x.bySport.map((s) => <ProgressRow key={s.key} label={`${s.label} · ${s.bookedHours} / ${s.openHours} jam`} ratio={Number(s.utilization) / 100} />)}
        </DashCard>
        <DashCard span={6} icon="stadium" title="Utilisasi per lapangan">
          <BreakdownList rows={x.byCourt.map((c, i) => ({ label: `${c.label} (${c.facility})`, value: `${c.utilization}% · ${short(c.revenue)}`,
            share: Number(c.utilization) / 100, color: COLORS[i % COLORS.length] }))} />
        </DashCard>
      </DashGrid>
      <DashGrid>
        <DashTable span={6} title={`Booking berikutnya (${x.upcoming.length})`} icon="event_upcoming" rows={x.upcoming} rowKey={(b) => b.id} onRow={(b) => setOpen(b.id)}
          empty="Tidak ada booking berikutnya" columns={[
            { key: 'when', header: 'Jam', render: (b) => <>{dayShort(b.start)} {hm(b.start)}<div className="oc-small oc-muted">{b.lines.map((l) => l.courtName).join(', ')}</div></> },
            { key: 'name', header: 'Penyewa', render: (b) => <>{b.name}<div className="oc-small"><ChannelTag channel={b.channel} /></div></> },
            { key: 'pay', header: 'Bayar', render: (b) => <PayPill status={b.payStatus} /> }]} />
        <DashCard span={6} icon="warning" tone="dark" title={`Peringatan (${x.alerts.length + x.outsideNormalHours.length})`}>
          {x.alerts.length === 0 && x.outsideNormalHours.length === 0 ? <span className="oc-muted oc-small">Tidak ada peringatan.</span> : (
            <div className="oc-stack" style={{ gap: 6 }}>
              {x.alerts.map((a, i) => <button key={i} type="button" className="sc-alert" data-kind={a.kind} onClick={() => a.bookingId && setOpen(a.bookingId)}>{a.message}</button>)}
              {x.outsideNormalHours.map((b) => (
                <button key={b.id} type="button" className="sc-alert" onClick={() => setOpen(b.id)}>Di luar jam normal (setelah mode 24 jam): {b.code} {b.name} {dayShort(b.start)} {hm(b.start)}</button>
              ))}
            </div>
          )}
        </DashCard>
      </DashGrid>
      {open && <BookingDrawer id={open} onClose={() => setOpen(null)} onChanged={() => void r.refetch()} />}
    </div>
  );
}

// ── Kalender Lapangan (FR-72) ───────────────────────────────────────────────

interface CalRow { resource: { id: string; name: string; attributes: R }; entries: { reservationId: string; lineId: string; code: string; kind: string; status: string;
  start: string; end: string; customer: string | null }[] }

export function SportCalendarPage() {
  const nav = useNavigate();
  const toast = useToast();
  const { can } = useAuth();
  const [view, setView] = useState<'day' | 'week' | 'month'>('day');
  const [date, setDate] = useState(ymd());
  const [sport, setSport] = useState('');
  const [status, setStatus] = useState('');
  const [open, setOpen] = useState<string | null>(null);
  const [drag, setDrag] = useState<{ reservationId: string; lineId: string; hours: number } | null>(null);
  const start = view === 'day' ? date : view === 'week' ? addDays(date, -((new Date(`${date}T00:00:00`).getDay() + 6) % 7)) : `${date.slice(0, 8)}01`;
  const days = view === 'day' ? 1 : view === 'week' ? 7 : new Date(Number(date.slice(0, 4)), Number(date.slice(5, 7)), 0).getDate();
  const cal = useGet<Page<CalRow>>(`/api/v1/reservation/calendar${qs({ from: start, days, resourceType: 'sport_court' })}`);
  const board = useGet<{ bookings: CourtBooking[]; courts: { id: string; resourceId?: string }[] }>(view === 'day' ? `/api/v1/sportclub/board${qs({ date, facilityId: sport || undefined })}` : null);
  useBoardLive(useCallback(() => { void cal.refetch(); void board.refetch(); }, [cal, board]));
  const rows = (cal.data?.items ?? []).filter((r) => !sport || String(r.resource.attributes.facilityId) === sport);
  const states = useMemo(() => {
    const m: Record<string, string> = {};
    for (const b of board.data?.bookings ?? []) for (const l of b.lines) m[l.id] = l.state;
    return m;
  }, [board.data]);
  const dates = Array.from({ length: days }, (_, i) => addDays(start, i));
  const hours = Array.from({ length: 24 }, (_, i) => i).filter((h) => h >= 6 || rows.some((r) => r.entries.some((e) => new Date(e.start).getHours() === h && e.start.startsWith(date))));
  const move = async (courtId: string, startAt: Date) => {
    if (!drag) return;
    const reason = window.prompt('Alasan pindah jadwal (wajib)');
    if (!reason) return;
    try {
      await request('POST', `/api/v1/sportclub/bookings/${drag.reservationId}:move`, { lineId: drag.lineId, courtId, start: startAt.toISOString(), reason });
      toast('Jadwal dipindah');
      void cal.refetch();
      void board.refetch();
    } catch (e) {
      toast((e as Error).message, 'error');
    } finally {
      setDrag(null);
    }
  };
  return (
    <div className="oc-stack">
      <PageHeader title="Kalender Lapangan" help="Perencanaan multi-hari per lapangan. Klik slot kosong untuk booking, seret booking untuk pindah jadwal (supervisor). Operasi hari ini ada di Papan Lapangan (front desk)." />
      <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
        {(['day', 'week', 'month'] as const).map((v) => <button key={v} className="oc-chip" aria-pressed={view === v} onClick={() => setView(v)}>{v === 'day' ? 'Hari' : v === 'week' ? 'Minggu' : 'Bulan'}</button>)}
        <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setDate(addDays(date, view === 'day' ? -1 : view === 'week' ? -7 : -28))}><Icon name="chevron_left" size={18} /></button>
        <input type="date" className="oc-input" style={{ width: 160 }} value={date} onChange={(e) => e.target.value && setDate(e.target.value)} />
        <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setDate(addDays(date, view === 'day' ? 1 : view === 'week' ? 7 : 28))}><Icon name="chevron_right" size={18} /></button>
        <SportFilter value={sport} onChange={setSport} />
        <select className="oc-input oc-filter" value={status} onChange={(e) => setStatus(e.target.value)} aria-label="Status">
          <option value="">Semua status</option>
          {['draft', 'confirmed', 'checked_in', 'completed'].map((s) => <option key={s} value={s}>{s.replace('_', ' ')}</option>)}
        </select>
        <span className="oc-spacer" />
        <Link className="oc-btn oc-btn-neutral" to="/sport-club/blocks"><Icon name="block" size={18} /> Blokir slot</Link>
      </div>
      <ErrorAlert error={cal.error} />
      {cal.isLoading ? <Skeleton rows={8} /> : rows.length === 0 ? <Empty title="Belum ada lapangan" icon="calendar_month" /> : view === 'day' ? (
        <div className="sc-board-wrap">
          <table className="sc-board">
            <thead><tr><th />{rows.map((r) => <th key={r.resource.id}>{r.resource.name}</th>)}</tr></thead>
            <tbody>
              {hours.map((h) => {
                const t = new Date(`${date}T${String(h).padStart(2, '0')}:00:00`);
                return (
                  <tr key={h}>
                    <th>{String(h).padStart(2, '0')}:00</th>
                    {rows.map((r) => {
                      const e = r.entries.find((x) => new Date(x.start) <= t && new Date(x.end) > t && (!status || x.status === status));
                      const courtId = String(r.resource.attributes.courtId ?? '');
                      if (e && new Date(e.start).getTime() !== t.getTime() && h !== hours[0]) return null;
                      if (e) {
                        const span = Math.max(1, Math.round((new Date(e.end).getTime() - Math.max(new Date(e.start).getTime(), t.getTime())) / 3600_000));
                        const state = e.kind === 'block' ? 'blocked' : states[e.lineId] ?? 'scheduled';
                        return (
                          <td key={r.resource.id} className="sc-cell" data-state={state} rowSpan={span}>
                            {e.kind === 'block' ? <><strong>Diblokir</strong><small>{e.customer ?? ''}</small></> : (
                              <button type="button" draggable={can('sportclub.booking.override')} onDragStart={() => setDrag({ reservationId: e.reservationId, lineId: e.lineId, hours: span })}
                                onClick={() => setOpen(e.reservationId)}>
                                <strong>{e.customer ?? e.code}</strong><small>{hm(e.start)}–{hm(e.end)} · {STATE[state]?.[0] ?? e.status}</small>
                              </button>
                            )}
                          </td>
                        );
                      }
                      return (
                        <td key={r.resource.id} className="sc-cell" data-state={t.getTime() < Date.now() ? 'past' : 'free'}
                          onDragOver={(ev) => drag && ev.preventDefault()} onDrop={() => void move(courtId, t)}>
                          {t.getTime() >= Date.now() && <button type="button" onClick={() => nav(`/ops/sport/new${qs({ facility: String(r.resource.attributes.facilityId ?? ''), court: courtId,
                            start: t.toISOString(), date })}`)}>+</button>}
                        </td>
                      );
                    })}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <div style={{ overflowX: 'auto' }}>
          <table className="sc-cal">
            <thead><tr><th style={{ width: 160 }}>Lapangan</th>{dates.map((d) => <th key={d}>{dayShort(`${d}T00:00:00`)}</th>)}</tr></thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.resource.id}>
                  <th>{r.resource.name}</th>
                  {dates.map((d) => {
                    const ev = r.entries.filter((e) => ymd(new Date(e.start)) === d && (!status || e.status === status));
                    return (
                      <td key={d}>
                        {ev.map((e) => (
                          <button key={e.lineId} type="button" className="sc-ev" style={{ background: e.kind === 'block' ? '#e2e8f0' : e.status === 'draft' ? '#fef3c7' : '#dbeafe' }}
                            onClick={() => (e.kind === 'block' ? undefined : setOpen(e.reservationId))}>
                            {hm(e.start)} {e.kind === 'block' ? 'Blokir' : e.customer ?? e.code}</button>
                        ))}
                        {view === 'week' && ev.length === 0 && <span className="oc-muted oc-small">—</span>}
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {open && <BookingDrawer id={open} onClose={() => setOpen(null)} onChanged={() => { void cal.refetch(); void board.refetch(); }} />}
    </div>
  );
}

// ── Reservasi (FR-73) ───────────────────────────────────────────────────────

export function SportReservationsPage() {
  const [from, setFrom] = useState(addDays(ymd(), -30));
  const [to, setTo] = useState(addDays(ymd(), 30));
  const [q, setQ] = useState('');
  const [sport, setSport] = useState('');
  const [state, setState] = useState('');
  const [channel, setChannel] = useState('');
  const [pay, setPay] = useState('');
  const [open, setOpen] = useState<string | null>(null);
  const list = useGet<Page<CourtBooking>>(`/api/v1/sportclub/bookings${qs({ from, to, q: q || undefined, 'filter[facilityId]': sport || undefined,
    'filter[state]': state || undefined, 'filter[channel]': channel || undefined, 'filter[payStatus]': pay || undefined, limit: 500 })}`);
  const exportUrl = `/reports/sportclub.court_bookings${qs({ from, to, facilityId: sport || undefined, channel: channel || undefined, payStatus: pay || undefined })}`;
  return (
    <div className="oc-stack">
      <PageHeader title="Reservasi" help="Semua booking lapangan: cari kode, nama atau HP; filter cabor, status, channel dan status bayar. Tidak ada aksi batal/refund." actions={
        <Link className="oc-btn oc-btn-neutral" to={exportUrl}><Icon name="download" size={18} /> Ekspor</Link>} />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Cari" value={q} onChange={setQ} />
        <TextField label="Dari" type="date" value={from} onChange={setFrom} />
        <TextField label="Sampai" type="date" value={to} onChange={setTo} />
        <div><span className="oc-small oc-muted">Cabor</span><br /><SportFilter value={sport} onChange={setSport} /></div>
        <SelectField label="Status" value={state} onChange={setState} placeholder="Semua" options={Object.entries(STATE).map(([k, [l]]) => ({ value: k, label: l }))} />
        <SelectField label="Channel" value={channel} onChange={setChannel} placeholder="Semua" options={Object.entries(CHANNEL).map(([k, [l]]) => ({ value: k, label: l }))} />
        <SelectField label="Bayar" value={pay} onChange={setPay} placeholder="Semua" options={[['unpaid', 'Belum bayar'], ['partially_paid', 'Sebagian'], ['paid', 'Lunas'],
          ['overpaid', 'Lebih bayar']].map(([v, l]) => ({ value: v, label: l }))} />
      </div>
      <ErrorAlert error={list.error} />
      <div className="oc-card">
        <DataTable rows={list.data?.items as unknown as R[]} loading={list.isLoading} rowKey={(r) => String(r.id)} onRowClick={(r) => setOpen(String(r.id))}
          empty={<Empty title="Tidak ada booking" icon="event_busy" />} columns={[
            { key: 'code', header: 'Booking', render: (r) => <><strong>{String(r.code)}</strong><div className="oc-small"><ChannelTag channel={String(r.channel)} /></div></> },
            { key: 'name', header: 'Penyewa', render: (r) => <>{String(r.name)}<div className="oc-small oc-muted">{String(r.phone ?? '')}</div></> },
            { key: 'lines', header: 'Lapangan & jam', render: (r) => (r as unknown as CourtBooking).lines.map((l) => <div key={l.id} className="oc-small">{l.courtName} · {dayShort(l.start)} {hm(l.start)}–{hm(l.end)}</div>) },
            { key: 'state', header: 'Status', render: (r) => <StatePill state={String(r.state)} /> },
            { key: 'pay', header: 'Bayar', render: (r) => <PayPill status={String(r.payStatus)} /> },
            { key: 'charges', header: 'Total', align: 'right', render: (r) => money(r.charges) }]} />
      </div>
      {open && <BookingDrawer id={open} onClose={() => setOpen(null)} onChanged={() => void list.refetch()} />}
    </div>
  );
}

// ── Booking Rutin (FR-75) ───────────────────────────────────────────────────

export function SportRecurringPage() {
  const toast = useToast();
  const { can } = useAuth();
  const list = useGet<Page<R>>('/api/v1/sportclub/recurring?limit=200');
  const [detail, setDetail] = useState<string | null>(null);
  const [action, setAction] = useState<{ id: string; kind: string } | null>(null);
  const [form, setForm] = useState({ from: ymd(), until: addDays(ymd(), 14), endDate: addDays(ymd(), 90), reason: '' });
  const d = useGet<R & { bookings: CourtBooking[] }>(detail ? `/api/v1/sportclub/recurring/${detail}` : null);
  const [open, setOpen] = useState<string | null>(null);
  const run = async () => {
    if (!action) return;
    try {
      await request('POST', `/api/v1/sportclub/recurring/${action.id}:${action.kind}`, action.kind === 'pause' ? { from: form.from, until: form.until, reason: form.reason }
        : action.kind === 'extend' ? { endDate: form.endDate } : { reason: form.reason });
      toast('Tersimpan');
      setAction(null);
      void list.refetch();
      void d.refetch();
    } catch (e) {
      toast((e as Error).message, 'error');
    }
  };
  return (
    <div className="oc-stack">
      <PageHeader title="Booking Rutin" help="Langganan komunitas, sekolah, kantor: jadwal per minggu, masa berlaku, status bayar. Jeda / hentikan hanya memengaruhi pertemuan yang belum dibayar dan belum terjadi; tidak ada refund."
        actions={can('sportclub.recurring.manage') ? <Link className="oc-btn oc-btn-primary" to="/ops/sport/new?mode=recurring"><Icon name="add" size={18} /> Booking rutin baru</Link> : undefined} />
      <div className="oc-card">
        <DataTable rows={list.data?.items} loading={list.isLoading} rowKey={(r) => String(r.id)} onRowClick={(r) => setDetail(String(r.id))}
          empty={<Empty title="Belum ada booking rutin" icon="event_repeat" />} columns={[
            { key: 'code', header: 'Kode', render: (r) => <><strong>{String(r.code)}</strong><div className="oc-small oc-muted">{String(r.holderName)}{r.corporateName ? ` · ${String(r.corporateName)}` : ''}</div></> },
            { key: 'court', header: 'Lapangan', render: (r) => `${String(r.courtName)} (${String(r.facilityName)})` },
            { key: 'when', header: 'Jadwal', render: (r) => `${(r.weekdays as number[]).map((w) => ['', 'Sen', 'Sel', 'Rab', 'Kam', 'Jum', 'Sab', 'Min'][w]).join(', ')} ${String(r.startTime)} · ${String(r.hours)} jam` },
            { key: 'period', header: 'Berlaku', render: (r) => `${String(r.startDate).slice(0, 10)} – ${String(r.endDate).slice(0, 10)}` },
            { key: 'meetings', header: 'Pertemuan', render: (r) => `${String(r.played)} main · ${String(r.upcoming)} akan datang` },
            { key: 'unpaid', header: 'Belum dibayar', align: 'right', render: (r) => money(r.unpaid) },
            { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} label={({ active: 'Aktif', paused: 'Dijeda', stopped: 'Dihentikan', ended: 'Berakhir' } as R)[String(r.status)] as string} /> }]}
          actions={(r) => (can('sportclub.recurring.manage') ? (
            <span className="oc-row" style={{ gap: 4 }}>
              {r.status === 'active' && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={(e) => { e.stopPropagation(); setAction({ id: String(r.id), kind: 'pause' }); }}>Jeda</button>}
              {r.status === 'paused' && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={(e) => { e.stopPropagation(); setAction({ id: String(r.id), kind: 'resume' }); }}>Lanjutkan</button>}
              {r.status !== 'stopped' && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={(e) => { e.stopPropagation(); setAction({ id: String(r.id), kind: 'extend' }); }}>Perpanjang</button>}
              {r.status !== 'stopped' && <button className="oc-btn oc-btn-danger oc-btn-sm" onClick={(e) => { e.stopPropagation(); setAction({ id: String(r.id), kind: 'stop' }); }}>Hentikan</button>}
            </span>) : null)} />
      </div>
      {detail && d.data && (
        <Card title={`Pertemuan ${String(d.data.code)}`} icon="calendar_month">
          <DataTable rows={d.data.bookings as unknown as R[]} rowKey={(r) => String(r.id)} onRowClick={(r) => setOpen(String(r.id))} columns={[
            { key: 'when', header: 'Tanggal', render: (r) => `${dayShort(String(r.start))} ${hm(String(r.start))}` },
            { key: 'state', header: 'Status', render: (r) => <StatePill state={String(r.state)} /> },
            { key: 'pay', header: 'Bayar', render: (r) => <PayPill status={String(r.payStatus)} /> },
            { key: 'charges', header: 'Tagihan', align: 'right', render: (r) => money(r.charges) }]} />
        </Card>
      )}
      {action && (
        <Modal open onClose={() => setAction(null)} title={({ pause: 'Jeda', resume: 'Lanjutkan', extend: 'Perpanjang', stop: 'Hentikan' } as Record<string, string>)[action.kind]}
          actions={<><button className="oc-btn oc-btn-neutral" onClick={() => setAction(null)}>Batal</button><button className="oc-btn oc-btn-ink" onClick={() => void run()}>Simpan</button></>}>
          <div className="oc-stack">
            {action.kind === 'pause' && <><TextField label="Dari" type="date" value={form.from} onChange={(v) => setForm({ ...form, from: v })} />
              <TextField label="Sampai" type="date" value={form.until} onChange={(v) => setForm({ ...form, until: v })} /></>}
            {action.kind === 'extend' && <TextField label="Berlaku sampai" type="date" value={form.endDate} onChange={(v) => setForm({ ...form, endDate: v })} />}
            {(action.kind === 'pause' || action.kind === 'stop') && <TextField label="Alasan" value={form.reason} onChange={(v) => setForm({ ...form, reason: v })} />}
            <span className="oc-small oc-muted">Pertemuan yang sudah dibayar tetap berlaku; tidak ada refund.</span>
          </div>
        </Modal>
      )}
      {open && <BookingDrawer id={open} onClose={() => setOpen(null)} onChanged={() => void d.refetch()} />}
    </div>
  );
}

// ── Blokir Slot (FR-74) ─────────────────────────────────────────────────────

export function SportBlocksPage() {
  const toast = useToast();
  const { can } = useAuth();
  const courts = useGet<Page<R>>('/api/v1/sportclub/courts?filter[status]=active&limit=100');
  const [from, setFrom] = useState(ymd());
  const list = useGet<Page<R>>(`/api/v1/sportclub/blocks${qs({ from, to: addDays(from, 60) })}`);
  const [form, setForm] = useState({ courtIds: [] as string[], from: addDays(ymd(), 1), to: '', startTime: '06:00', endTime: '08:00', weekdays: [] as number[],
    reason: 'maintenance', notes: '' });
  const [conflicts, setConflicts] = useState<CourtBooking[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [open, setOpen] = useState<string | null>(null);
  const save = async (dryRun: boolean) => {
    setError(null);
    try {
      const r = await request<{ blocks: string[]; conflicts: CourtBooking[]; periods: number }>('POST', '/api/v1/sportclub/blocks', { ...form, to: form.to || undefined, dryRun });
      setConflicts(r.conflicts);
      if (!dryRun && r.conflicts.length === 0) {
        toast(`${r.blocks.length} slot diblokir`);
        void list.refetch();
      } else if (r.conflicts.length) toast(`${r.conflicts.length} booking ada di periode ini — pindahkan dulu`, 'error');
      else toast(`Aman: ${r.periods} periode bisa diblokir`);
    } catch (e) {
      setError(e);
    }
  };
  const names = Object.fromEntries((courts.data?.items ?? []).map((c) => [String(c.id), String(c.name)]));
  return (
    <div className="oc-stack">
      <PageHeader title="Blokir Slot" help="Tutup lapangan/jam untuk perawatan, turnamen atau acara. Slot yang diblokir tidak bisa dibooking dari website maupun front desk." />
      <Card title="Blokir baru" icon="block">
        <div className="oc-stack">
          <div className="oc-row-wrap">{(courts.data?.items ?? []).map((c) => (
            <button key={String(c.id)} type="button" className="oc-chip" aria-pressed={form.courtIds.includes(String(c.id))}
              onClick={() => setForm({ ...form, courtIds: form.courtIds.includes(String(c.id)) ? form.courtIds.filter((x) => x !== c.id) : [...form.courtIds, String(c.id)] })}>{String(c.name)}</button>
          ))}</div>
          <div className="oc-row-wrap">
            <TextField label="Dari tanggal" type="date" value={form.from} onChange={(v) => setForm({ ...form, from: v })} />
            <TextField label="Sampai tanggal (opsional)" type="date" value={form.to} onChange={(v) => setForm({ ...form, to: v })} />
            <TextField label="Jam mulai" type="time" value={form.startTime} onChange={(v) => setForm({ ...form, startTime: v })} />
            <TextField label="Jam selesai" type="time" value={form.endTime} onChange={(v) => setForm({ ...form, endTime: v })} />
          </div>
          <div className="oc-row-wrap"><span className="oc-small">Berulang di hari:</span>{[[1, 'Sen'], [2, 'Sel'], [3, 'Rab'], [4, 'Kam'], [5, 'Jum'], [6, 'Sab'], [7, 'Min']].map(([d, l]) => (
            <button key={d} type="button" className="oc-chip" aria-pressed={form.weekdays.includes(d as number)}
              onClick={() => setForm({ ...form, weekdays: form.weekdays.includes(d as number) ? form.weekdays.filter((x) => x !== d) : [...form.weekdays, d as number] })}>{l}</button>
          ))}<span className="oc-small oc-muted">(kosong = setiap hari)</span></div>
          <div className="oc-row-wrap">
            <SelectField label="Alasan" value={form.reason} onChange={(v) => setForm({ ...form, reason: v })} options={[['maintenance', 'Perawatan'], ['tournament', 'Turnamen'],
              ['private_event', 'Acara'], ['management_hold', 'Ditahan manajemen'], ['weather_closure', 'Cuaca'], ['other', 'Lainnya']].map(([v, l]) => ({ value: v, label: l }))} />
            <TextField label="Catatan" value={form.notes} onChange={(v) => setForm({ ...form, notes: v })} />
          </div>
          <div className="oc-row-wrap">
            <button className="oc-btn oc-btn-neutral" disabled={!form.courtIds.length} onClick={() => void save(true)}>Cek bentrok</button>
            <button className="oc-btn oc-btn-primary" disabled={!form.courtIds.length || !can('sportclub.block.manage')} onClick={() => void save(false)}>Blokir</button>
          </div>
          <ErrorAlert error={error} />
          {conflicts && conflicts.length > 0 && (
            <div className="oc-alert oc-alert-error">
              <strong>{conflicts.length} booking di periode ini.</strong> Pindahkan dulu (klik untuk membuka), lalu blokir lagi.
              {conflicts.map((b) => <div key={b.id}><button className="oc-link" onClick={() => setOpen(b.id)}>{b.code} · {b.name} · {b.lines.map((l) => `${l.courtName} ${dayShort(l.start)} ${hm(l.start)}`).join(', ')}</button></div>)}
            </div>
          )}
        </div>
      </Card>
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}><TextField label="Tampilkan mulai" type="date" value={from} onChange={setFrom} /></div>
      <div className="oc-card">
        <DataTable rows={list.data?.items} loading={list.isLoading} rowKey={(r) => String(r.id)} empty={<Empty title="Tidak ada slot diblokir" icon="block" />} columns={[
          { key: 'court', header: 'Lapangan', render: (r) => names[String(r.courtId)] ?? '—' },
          { key: 'when', header: 'Waktu', render: (r) => `${dayShort(String(r.start))} ${hm(String(r.start))}–${hm(String(r.end))}` },
          { key: 'reason', header: 'Alasan', render: (r) => String(r.reason).replace(/_/g, ' ') }, { key: 'notes', header: 'Catatan' }]}
          actions={(r) => (can('sportclub.booking.override') ? <button className="oc-btn oc-btn-neutral oc-btn-sm"
            onClick={() => void request('POST', `/api/v1/sportclub/blocks/${String(r.id)}:remove`, { reason: 'dibuka oleh supervisor' }).then(() => { toast('Blokir dibuka'); void list.refetch(); })}>Buka</button> : null)} />
      </div>
      {open && <BookingDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

// ── Paket & Voucher (FR-76) ─────────────────────────────────────────────────

export function SportPackagesAdminPage() {
  const [q, setQ] = useState('');
  const types = useGet<Page<R>>('/api/v1/commercial/voucher-types?limit=300');
  const active = useGet<Page<R>>(`/api/v1/sportclub/packages${qs({ q: q || undefined })}`);
  const pkg = (types.data?.items ?? []).filter((v) => ['court_package', 'class_package', 'sport_entry'].includes(String(v.category)));
  return (
    <div className="oc-stack">
      <PageHeader title="Paket & Voucher" help="Paket 4x/8x sesuai brosur, voucher masuk, kode promo (dengan kuota, periode, cabor/jam berlaku) dan paket aktif per pelanggan beserta sisa kuotanya."
        actions={<><Link className="oc-btn oc-btn-neutral" to="/commercial/pricing/promo-codes">Kode promo</Link><Link className="oc-btn oc-btn-neutral" to="/commercial/operations">Kelola jenis voucher</Link></>} />
      <Card title="Jenis paket" icon="card_membership">
        <DataTable rows={pkg} loading={types.isLoading} rowKey={(r) => String(r.id)} columns={[{ key: 'code', header: 'Kode' }, { key: 'name', header: 'Nama' },
          { key: 'category', header: 'Jenis', render: (r) => ({ court_package: 'Paket lapangan', class_package: 'Paket kelas', sport_entry: 'Voucher masuk' } as R)[String(r.category)] as string },
          { key: 'faceValue', header: 'Kuota', render: (r) => `${Number(r.faceValue)}x` }, { key: 'price', header: 'Harga', align: 'right', render: (r) => money(r.price) },
          { key: 'validityMonths', header: 'Berlaku', render: (r) => (r.validityMonths ? `${String(r.validityMonths)} bln` : '—') }]} />
      </Card>
      <div style={{ maxWidth: 360 }}><TextField label="Cari pelanggan / kode" value={q} onChange={setQ} /></div>
      <Card title="Paket aktif per pelanggan" icon="person">
        <DataTable rows={active.data?.items} loading={active.isLoading} rowKey={(r) => String(r.id)} empty={<Empty title="Tidak ada paket aktif" icon="card_membership" />} columns={[
          { key: 'customerName', header: 'Pelanggan' }, { key: 'code', header: 'Kode' }, { key: 'typeName', header: 'Paket' },
          { key: 'remaining', header: 'Sisa', render: (r) => `${Number(r.remaining)} / ${Number(r.original)}` },
          { key: 'expiresAt', header: 'Berlaku sampai', render: (r) => (r.expiresAt ? dayShort(String(r.expiresAt)) : '—') },
          { key: 'pricePaid', header: 'Dibayar', align: 'right', render: (r) => money(r.pricePaid) }]} />
      </Card>
    </div>
  );
}

// ── Laporan & Ekspor (FR-79..84) ────────────────────────────────────────────

const WD = ['', 'Sen', 'Sel', 'Rab', 'Kam', 'Jum', 'Sab', 'Min'];

export function Heatmap({ cells }: { cells: Report['heatmap'] }) {
  const hours = Array.from(new Set(cells.map((c) => c.hour))).sort((a, b) => a - b);
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="sc-heat">
        <thead><tr><th />{hours.map((h) => <th key={h}>{h}</th>)}</tr></thead>
        <tbody>
          {[1, 2, 3, 4, 5, 6, 7].map((w) => (
            <tr key={w}><th>{WD[w]}</th>{hours.map((h) => {
              const c = cells.find((x) => x.weekday === w && x.hour === h);
              const r = c ? Number(c.rate) / 100 : 0;
              return <td key={h} title={c ? `${WD[w]} ${h}:00 · ${c.booked}/${c.open} jam · ${c.rate}%` : ''}
                style={{ background: c && c.open ? `rgba(37, 99, 235, ${0.08 + r * 0.92})` : 'transparent', color: r > 0.5 ? '#fff' : 'inherit' }}>{c && c.open ? Math.round(r * 100) : ''}</td>;
            })}</tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function SportReportsPage({ management }: { management?: boolean }) {
  const [from, setFrom] = useState(addDays(ymd(), -29));
  const [to, setTo] = useState(ymd());
  const [sport, setSport] = useState('');
  const r = useGet<Report>(`/api/v1/sportclub/court-report${qs({ from, to, facilityId: sport || undefined })}`);
  const controls = <>
    <input type="date" className="oc-input oc-filter" aria-label="Dari" value={from} onChange={(e) => e.target.value && setFrom(e.target.value)} />
    <input type="date" className="oc-input oc-filter" aria-label="Sampai" value={to} onChange={(e) => e.target.value && setTo(e.target.value)} />
    <SportFilter value={sport} onChange={setSport} />
  </>;
  const title = management ? 'Sport Club — lapangan' : 'Laporan & Ekspor';
  if (r.error) return <div className="oc-dash-page"><DashHead title={title} controls={controls} /><ErrorAlert error={r.error} /></div>;
  if (!r.data) return <div className="oc-dash-page"><DashHead title={title} controls={controls} /><Skeleton rows={10} /></div>;
  const x = r.data;
  const chTotal = x.byChannel.reduce((s, c) => s + c.count, 0);
  const exp = (code: string) => `/reports/${code}${qs({ from, to, facilityId: sport || undefined })}`;
  return (
    <div className="oc-dash-page">
      <DashHead title={title} sub={`${x.from} – ${x.to}`} controls={controls} />
      <DashGrid>
        <MiniCard span={3} label="Utilisasi" value={<Amount text={`${x.utilization}%`} size="md" />} delta={<span className="oc-dash-chip-suffix">jam normal {x.utilizationNormal}%</span>} />
        <MiniCard span={3} label="Pendapatan sewa + tambahan" value={<Amount text={short(x.revenue)} size="md" />} delta={<span className="oc-dash-chip-suffix">per tanggal main</span>} />
        <MiniCard span={3} label="Booking · jam" value={<Amount text={`${x.bookings} · ${x.bookedHours}`} size="md" />} />
        <MiniCard span={3} label="No-show rate" value={<Amount text={`${x.noShowRate}%`} size="md" />} delta={<span className="oc-dash-chip-suffix">{x.noShows} baris</span>} />
        <MiniCard span={3} label="Tambahan (sewa alat, minuman)" value={<Amount text={short(x.extras)} size="md" />} />
        <MiniCard span={3} label="Biaya layanan online" value={<Amount text={short(x.serviceFees)} size="md" />} />
        <MiniCard span={3} label="Paket terpakai (diakui)" value={<Amount text={short(x.packageUse)} size="md" />} />
        <MiniCard span={3} label="Paket terjual (diterima di muka)" value={<Amount text={short(x.packageSales)} size="md" />} />
      </DashGrid>
      <DashGrid>
        <DashCard span={7} icon="grid_on" tone="blue" title="Utilisasi hari × jam (jam sepi = peluang promo)"><Heatmap cells={x.heatmap} /></DashCard>
        <DashCard span={5} icon="call_split" title="Channel (dampak website)">
          <BreakdownList rows={x.byChannel.map((c, i) => ({ label: CHANNEL[c.key]?.[0] ?? c.key, value: `${c.count} booking · ${short(c.amount)}`,
            share: chTotal ? c.count / chTotal : null, color: COLORS[i % COLORS.length] }))} />
        </DashCard>
      </DashGrid>
      <DashGrid>
        <DashCard span={6} icon="sports_tennis" title="Per cabor">
          <ColumnChart aLabel="Pendapatan" format={(v) => short(v)} points={x.bySport.map((s) => ({ label: s.label, a: Number(s.revenue), title: `${s.label}: ${s.utilization}% · ${short(s.revenuePerHour)}/jam` }))} />
          <BreakdownList rows={x.bySport.map((s) => ({ label: s.label, value: `${s.utilization}% · ${short(s.revenue)} · ${short(s.revenuePerHour)}/jam buka` }))} />
        </DashCard>
        <DashCard span={6} icon="payments" title="Per metode bayar">
          <BreakdownList rows={x.byMethod.map((m, i) => ({ label: m.key.replace(/_/g, ' '), value: `${m.count} · ${short(m.amount)}`, color: COLORS[i % COLORS.length] }))} />
          <BreakdownList rows={x.noShowBySport.map((n) => ({ label: `No-show ${n.label}`, value: String(n.count) }))} />
        </DashCard>
      </DashGrid>
      <DashGrid>
        <DashTable span={8} title="Pelanggan teratas (prospek membership)" icon="star" rows={x.topCustomers} rowKey={(c) => `${c.customerId ?? c.name}`} empty="Belum ada"
          columns={[{ key: 'n', header: 'Pelanggan', render: (c) => <>{c.name}{c.prospect && <span className="acc-vip" style={{ marginLeft: 6 }}>Prospek</span>}<div className="oc-small oc-muted">{c.phone ?? ''} · {c.member ? 'Member' : 'Non-member'}</div></> },
            { key: 'b', header: 'Booking', render: (c) => `${c.bookings} · ${c.hours} jam` }, { key: 'f', header: 'Favorit', render: (c) => c.favorite ?? '—' },
            { key: 's', header: 'Belanja', render: (c) => short(c.spend) }, { key: 'x', header: 'No-show', render: (c) => String(c.noShows) }]} />
        <DashCard span={4} icon="download" title="Ekspor (Excel / CSV / PDF)">
          <div className="oc-stack">
            <Link className="oc-btn oc-btn-neutral" to={exp('sportclub.court_bookings')}><Icon name="table_view" size={18} /> Laporan booking</Link>
            <Link className="oc-btn oc-btn-neutral" to={exp('sportclub.settlement')}><Icon name="receipt_long" size={18} /> Settlement online per transaksi</Link>
            <Link className="oc-btn oc-btn-neutral" to={exp('sportclub.settlement_daily')}><Icon name="summarize" size={18} /> Settlement harian per metode</Link>
            <Link className="oc-btn oc-btn-neutral" to={exp('sportclub.court_utilization')}><Icon name="stadium" size={18} /> Utilisasi per lapangan</Link>
            <span className="oc-small oc-muted">Laporan dibuka di Reports dengan filter periode, cabor, lapangan, channel, status dan status bayar, lalu diekspor.</span>
          </div>
        </DashCard>
      </DashGrid>
    </div>
  );
}

/** Management › Sport Club Performance: the court KPIs on top of the KPI dashboard (FR-105). */
export function SportPerformanceCourts() {
  return <SportReportsPage management />;
}

