import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { qs, request, useGet, type Page } from '@oneclub/api-client';
import {
  Card, Checkbox, DataTable, Empty, ErrorAlert, Icon, Modal, SelectField, Skeleton, TextArea, TextField, useAuth, useToast,
} from '@oneclub/shell';
import { DeskPayDialog, type DeskTender } from '../ops/deskpay';
import { ScanField } from '../p4/inventory';
import {
  BookingDrawer, CHANNEL, ChannelTag, Legend, PayPill, SlotGrid, STATE, StatePill, WhoField, addDays, dayShort, hm, idem, money, useBoardLive,
  useFacilities, useQuote, ymd, type CourtBooking, type Pick, type R, type Who,
} from './shared';
import './sport.css';

// Front desk of the Sport Club (docs/requirement-booking-sportclub-mgcc.md
// §5): one desk for every sport on the cashier domain in the blue POS look —
// Papan Lapangan, Booking Baru (and Booking Rutin), Check-in, Pembayaran,
// Selesai Main, Riwayat, the court staff phone screen and incidents.

interface Board {
  date: string; from: string; to: string; courts: { id: string; name: string; facilityId: string; facilityName: string; surface?: string | null }[];
  bookings: CourtBooking[]; blocks: { id: string; courtId: string; start: string; end: string; reason: string; notes: string | null; issueId: string | null }[];
  summary: { bookings: number; inUse: number; courts: number; awaitingPayment: number; late: number; revenue: string; revenueBySport: { facility: string; amount: string }[] };
  onDuty: { employeeId: string; name: string; role: string; from: string; to: string }[];
  alerts: { kind: string; message: string; courtId?: string; bookingId?: string }[];
  issues: { id: string; courtName: string; note: string | null }[];
}

function DateNav({ date, onChange }: { date: string; onChange: (d: string) => void }) {
  return (
    <div className="oc-row" style={{ alignItems: 'center', gap: 6 }}>
      <button className="oc-btn oc-btn-neutral oc-btn-sm" aria-label="Hari sebelumnya" onClick={() => onChange(addDays(date, -1))}><Icon name="chevron_left" size={18} /></button>
      <input type="date" className="oc-input" style={{ width: 160 }} value={date} onChange={(e) => e.target.value && onChange(e.target.value)} />
      <button className="oc-btn oc-btn-neutral oc-btn-sm" aria-label="Hari berikutnya" onClick={() => onChange(addDays(date, 1))}><Icon name="chevron_right" size={18} /></button>
      {date !== ymd() && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => onChange(ymd())}>Hari ini</button>}
    </div>
  );
}

function SportChips({ value, onChange, all = true }: { value: string; onChange: (v: string) => void; all?: boolean }) {
  const f = useFacilities();
  return (
    <div className="oc-row-wrap">
      {all && <button type="button" className="oc-chip" aria-pressed={value === ''} onClick={() => onChange('')}>Semua cabor</button>}
      {f.list.map((x) => <button key={String(x.id)} type="button" className="oc-chip" aria-pressed={value === x.id} onClick={() => onChange(String(x.id))}>{String(x.name)}</button>)}
    </div>
  );
}

// ── Papan Lapangan (FR-52..60) ──────────────────────────────────────────────

export function CourtBoardPage() {
  const nav = useNavigate();
  const [date, setDate] = useState(ymd());
  const [sport, setSport] = useState('');
  const [open, setOpen] = useState<string | null>(null);
  const b = useGet<Board>(`/api/v1/sportclub/board${qs({ date, facilityId: sport || undefined })}`, { refetchInterval: 30_000 });
  useBoardLive(useCallback(() => { void b.refetch(); }, [b]));
  const x = b.data;
  const hours = useMemo(() => {
    if (!x) return [] as Date[];
    const out: Date[] = [];
    for (let t = new Date(x.from).getTime(); t < new Date(x.to).getTime(); t += 3600_000) out.push(new Date(t));
    return out;
  }, [x]);
  const nowH = new Date();
  return (
    <div className="oc-stack sc-desk">
      <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
        <h1 style={{ margin: 0, fontSize: 22 }}>Papan Lapangan</h1>
        <span className="oc-spacer" />
        <DateNav date={date} onChange={setDate} />
        <Link className="oc-btn oc-btn-primary" to="/ops/sport/new"><Icon name="add" size={18} /> Booking Baru</Link>
      </div>
      <SportChips value={sport} onChange={setSport} />
      {b.error && <ErrorAlert error={b.error} />}
      {!x ? <Skeleton rows={8} /> : (
        <>
          <div className="sc-tiles">
            <div className="sc-tile"><span>Booking hari ini</span><strong>{x.summary.bookings}</strong></div>
            <div className="sc-tile"><span>Lapangan terpakai sekarang</span><strong>{x.summary.inUse} / {x.summary.courts}</strong></div>
            <Link to="/ops/sport/payments" className="sc-tile" data-tone={x.summary.awaitingPayment ? 'warn' : undefined}><span>Menunggu pembayaran</span><strong>{x.summary.awaitingPayment}</strong></Link>
            <Link to="/ops/sport/check-in" className="sc-tile" data-tone={x.summary.late ? 'bad' : undefined}><span>Belum check-in (lewat jam mulai)</span><strong>{x.summary.late}</strong></Link>
            <div className="sc-tile"><span>Pendapatan hari ini (sebelum pajak)</span><strong>{money(x.summary.revenue)}</strong>
              <small>{x.summary.revenueBySport.map((r) => `${r.facility} ${money(r.amount)}`).join(' · ') || '—'}</small></div>
          </div>
          {x.alerts.length > 0 && (
            <div className="sc-alerts" role="status">
              {x.alerts.slice(0, 6).map((a, i) => (
                <button key={i} type="button" className="sc-alert" data-kind={a.kind} onClick={() => a.bookingId && setOpen(a.bookingId)}>
                  <Icon name={a.kind === 'issue' ? 'report' : a.kind === 'late' ? 'schedule' : a.kind === 'reminder' ? 'lightbulb' : 'payments'} size={18} /> {a.message}
                </button>
              ))}
            </div>
          )}
          {x.courts.length === 0 ? <Empty title="Belum ada lapangan aktif" icon="sports_tennis" /> : (
            <div className="sc-board-wrap">
              <table className="sc-board">
                <thead>
                  <tr><th />{x.courts.map((c) => <th key={c.id}>{c.name}<small>{c.facilityName}</small></th>)}</tr>
                </thead>
                <tbody>
                  {hours.map((h, hi) => {
                    const current = date === ymd() && h.getHours() === nowH.getHours();
                    return (
                      <tr key={h.toISOString()} data-now={current || undefined}>
                        <th>{hm(h.toISOString())}</th>
                        {x.courts.map((c) => {
                          const hs = h.getTime();
                          const block = x.blocks.find((bl) => bl.courtId === c.id && new Date(bl.start).getTime() <= hs && new Date(bl.end).getTime() > hs);
                          if (block) {
                            const first = new Date(block.start).getTime() === hs || hi === 0;
                            return first ? <td key={c.id} className="sc-cell" data-state="blocked" rowSpan={Math.max(1, Math.round((Math.min(new Date(block.end).getTime(), new Date(x.to).getTime()) - hs) / 3600_000))}>
                              <strong>Diblokir</strong><small>{block.reason.replace(/_/g, ' ')}{block.notes ? ` · ${block.notes}` : ''}</small></td> : null;
                          }
                          const hit = x.bookings.flatMap((bk) => bk.lines.filter((l) => l.courtId === c.id && new Date(l.start).getTime() <= hs && new Date(l.end).getTime() > hs
                            && !['void', 'expired'].includes(l.state)).map((l) => ({ bk, l })))[0];
                          if (hit) {
                            const first = new Date(hit.l.start).getTime() === hs || hi === 0;
                            if (!first) return null;
                            const span = Math.max(1, Math.round((Math.min(new Date(hit.l.end).getTime(), new Date(x.to).getTime()) - hs) / 3600_000));
                            return (
                              <td key={c.id} className="sc-cell" data-state={hit.l.state} rowSpan={span}>
                                <button type="button" onClick={() => setOpen(hit.bk.id)}>
                                  <strong>{hit.bk.name}</strong>
                                  <small>{hm(hit.l.start)}–{hm(hit.l.end)} · {CHANNEL[hit.bk.channel]?.[0] ?? hit.bk.channel}</small>
                                  <small>{STATE[hit.l.state]?.[0]} · {hit.bk.payStatus === 'paid' ? 'Lunas' : hit.bk.payStatus === 'overpaid' ? 'Lebih bayar' : `Sisa ${money(hit.bk.balance)}`}</small>
                                </button>
                              </td>
                            );
                          }
                          const past = date < ymd() || (date === ymd() && h.getTime() + 3600_000 <= Date.now());
                          return (
                            <td key={c.id} className="sc-cell" data-state={past ? 'past' : 'free'}>
                              {!past && <button type="button" aria-label={`Booking ${c.name} ${hm(h.toISOString())}`}
                                onClick={() => nav(`/ops/sport/new${qs({ facility: c.facilityId, court: c.id, start: h.toISOString(), date })}`)}>+</button>}
                            </td>
                          );
                        })}
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
          <Legend />
          <Card title="Petugas bertugas (roster HRIS)" icon="badge">
            {x.onDuty.length === 0 ? <span className="oc-muted oc-small">Belum ada petugas Sport Club di roster hari ini — atur di HRIS › Shift & Roster.</span> : (
              <div className="oc-row-wrap">{x.onDuty.map((s) => <span key={s.employeeId + s.from} className="oc-chip">{s.name} · {s.role.replace(/_/g, ' ')} · {hm(s.from)}–{hm(s.to)}</span>)}</div>
            )}
          </Card>
        </>
      )}
      {open && <BookingDrawer id={open} onClose={() => setOpen(null)} onChanged={() => void b.refetch()} />}
    </div>
  );
}

// ── Booking Baru (FR-61..65) ────────────────────────────────────────────────

export function NewCourtBookingPage() {
  const [sp] = useSearchParams();
  const [mode, setMode] = useState<'once' | 'recurring'>(sp.get('mode') === 'recurring' ? 'recurring' : 'once');
  return (
    <div className="oc-stack sc-desk">
      <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
        <h1 style={{ margin: 0, fontSize: 22 }}>Booking Baru</h1>
        <span className="oc-spacer" />
        <button type="button" className="oc-chip" aria-pressed={mode === 'once'} onClick={() => setMode('once')}>Sekali main</button>
        <button type="button" className="oc-chip" aria-pressed={mode === 'recurring'} onClick={() => setMode('recurring')}>Booking Rutin</button>
      </div>
      {mode === 'once' ? <OnceBooking /> : <RecurringBooking />}
    </div>
  );
}

function OnceBooking() {
  const [sp] = useSearchParams();
  const toast = useToast();
  const { can } = useAuth();
  const nav = useNavigate();
  const fac = useFacilities();
  const [sport, setSport] = useState(sp.get('facility') ?? '');
  const sportId = sport || String(fac.list[0]?.id ?? '');
  const [date, setDate] = useState(sp.get('date') ?? ymd());
  const [picks, setPicks] = useState<Pick[]>([]);
  const [who, setWho] = useState<Who>({ name: '', phone: '' });
  const [via, setVia] = useState<'walk_in' | 'phone'>('walk_in');
  const [pkg, setPkg] = useState('');
  const [promo, setPromo] = useState('');
  const [corporate, setCorporate] = useState('');
  const [notes, setNotes] = useState('');
  const [paying, setPaying] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const { quote, error: qerr } = useQuote(picks, promo, who.customerId);
  const packages = useGet<Page<R>>(who.customerId ? `/api/v1/sportclub/packages${qs({ customerId: who.customerId })}` : null);
  // a free slot clicked on the board opens here with the court and hour chosen (FR-54)
  useEffect(() => {
    const court = sp.get('court');
    const start = sp.get('start');
    if (court && start) {
      setPicks([{ courtId: court, courtName: '', start, end: new Date(new Date(start).getTime() + 3600_000).toISOString(), price: 0 }]);
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const toggle = (p: Pick) => setPicks((ps) => (ps.some((x) => x.courtId === p.courtId && x.start === p.start) ? ps.filter((x) => !(x.courtId === p.courtId && x.start === p.start))
    : [...ps, p].sort((a, b) => a.start.localeCompare(b.start))));
  const body = (t?: DeskTender) => ({
    lines: picks.map((p) => ({ courtId: p.courtId, start: p.start, end: p.end })), customerId: who.customerId,
    guest: who.customerId ? undefined : { name: who.name, phone: who.phone || undefined }, channel: 'ops', via, packageCode: pkg || undefined,
    promoCode: promo || undefined, corporateName: corporate || undefined, notes: notes || undefined,
    payment: t ? { methodType: t.methodType, reference: t.reference } : undefined,
  });
  const ready = picks.length > 0 && (who.customerId || who.name.trim()) && !qerr;
  const save = async (t?: DeskTender) => {
    setBusy(true);
    setError(null);
    try {
      const out = await request<{ reservation: R }>('POST', '/api/v1/sportclub/bookings', body(t), idem());
      return out;
    } catch (e) {
      setError(e); // the cart and the form stay (FR-138)
      throw e;
    } finally {
      setBusy(false);
    }
  };
  const done = (msg: string) => { setPaying(false); toast(msg); setPicks([]); setPkg(''); setPromo(''); nav('/ops/sport'); };
  return (
    <div className="sc-new">
      <div className="oc-stack">
        <Card title="Pilih lapangan dan jam" icon="sports_tennis">
          <div className="oc-stack">
            <SportChips value={sportId} onChange={(v) => { setSport(v); }} all={false} />
            <div className="oc-row-wrap" style={{ alignItems: 'center' }}><span>Tanggal</span><DateNav date={date} onChange={setDate} /></div>
            <SlotGrid facilityId={sportId} date={date} picks={picks} onToggle={toggle} />
          </div>
        </Card>
      </div>
      <div className="oc-stack">
        <Card title={`Keranjang (${picks.length})`} icon="shopping_cart">
          <div className="oc-stack">
            {picks.length === 0 ? <span className="oc-muted">Ketuk jam yang tersedia — boleh beberapa jam dan beberapa lapangan.</span> : (
              <table className="sc-table">
                <tbody>
                  {(quote?.lines ?? []).map((l) => (
                    <tr key={l.courtId + l.start}><td>{l.courtName}<div className="oc-small oc-muted">{dayShort(l.start)} {hm(l.start)}–{hm(l.end)} · {l.hours} jam</div></td>
                      <td style={{ textAlign: 'right' }}>{Number(l.discount) > 0 && <s className="oc-muted oc-small">{money(l.listPrice)} </s>}{money(l.net)}</td></tr>
                  ))}
                  {quote && <>
                    <tr><td>Sewa</td><td style={{ textAlign: 'right' }}>{money(quote.net)}</td></tr>
                    <tr><td>Pajak</td><td style={{ textAlign: 'right' }}>{money(quote.tax)}</td></tr>
                    <tr><td><strong>Total</strong></td><td style={{ textAlign: 'right' }}><strong>{money(quote.total)}</strong></td></tr>
                  </>}
                </tbody>
              </table>
            )}
            {picks.length > 0 && <button type="button" className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setPicks([])}>Kosongkan</button>}
            <ErrorAlert error={qerr} />
            {quote?.promoError && promo && <span className="oc-small" style={{ color: 'var(--md-sys-color-error, #dc2626)' }}>Kode promo tidak berlaku: {quote.promoError}</span>}
          </div>
        </Card>
        <Card title="Pelanggan" icon="person">
          <div className="oc-stack">
            <WhoField value={who} onChange={(w) => { setWho(w); setPkg(''); }} />
            <div className="oc-row-wrap">
              <button type="button" className="oc-chip" aria-pressed={via === 'walk_in'} onClick={() => setVia('walk_in')}>Walk-in</button>
              <button type="button" className="oc-chip" aria-pressed={via === 'phone'} onClick={() => setVia('phone')}>Telepon</button>
            </div>
            <TextField label="Perusahaan / komunitas (opsional)" value={corporate} onChange={setCorporate} />
            {(packages.data?.items ?? []).filter((p) => p.category === 'court_package').length > 0 && (
              <SelectField label="Pakai paket (potong kuota)" value={pkg} onChange={setPkg} placeholder="Tanpa paket"
                options={(packages.data?.items ?? []).filter((p) => p.category === 'court_package').map((p) => ({ value: String(p.code), label: `${String(p.typeName)} · sisa ${String(p.remaining)}x` }))} />
            )}
            {!who.customerId && <TextField label="Kode paket (opsional)" value={pkg} onChange={setPkg} />}
            <TextField label="Kode promo (opsional)" value={promo} onChange={setPromo} />
            <TextArea label="Catatan" value={notes} onChange={setNotes} rows={2} />
          </div>
        </Card>
        <ErrorAlert error={error} />
        <div className="oc-row-wrap">
          {pkg ? (
            <button className="oc-btn oc-btn-primary" disabled={!ready || busy}
              onClick={() => void save().then(() => done('Booking dibuat — dibayar dengan paket'), () => undefined)}>Booking dengan paket</button>
          ) : (
            <>
              <button className="oc-btn oc-btn-primary" disabled={!ready || busy || !quote} onClick={() => setPaying(true)}>Bayar & simpan {quote ? money(quote.total) : ''}</button>
              {can('sportclub.booking.create') && (
                <button className="oc-btn oc-btn-neutral" disabled={!ready || busy}
                  onClick={() => void save().then(() => done('Booking dibuat — belum dibayar'), () => undefined)}>Simpan, bayar nanti</button>
              )}
            </>
          )}
        </div>
        <span className="oc-small oc-muted">Biaya layanan payment gateway hanya untuk pembayaran online; pembayaran di meja tanpa biaya layanan.</span>
      </div>
      {paying && quote && (
        <DeskPayDialog amount={Number(quote.total)} summary={[['Lapangan', picks.map((p) => `${p.courtName || 'Lapangan'} ${hm(p.start)}`).join(', ')], ['Pelanggan', who.name || '—']]}
          members={who.customerId ? [{ customerId: who.customerId, name: who.name }] : []} onClose={() => setPaying(false)}
          pay={async (t) => { await save(t); return []; }} onFinish={() => done('Booking dibayar')} />
      )}
    </div>
  );
}

const WEEKDAYS: [number, string][] = [[1, 'Sen'], [2, 'Sel'], [3, 'Rab'], [4, 'Kam'], [5, 'Jum'], [6, 'Sab'], [7, 'Min']];

/** Booking Rutin (FR-64): the weekly pattern, the clashes shown before saving. */
export function RecurringBooking() {
  const toast = useToast();
  const nav = useNavigate();
  const courts = useGet<Page<R>>('/api/v1/sportclub/courts?filter[status]=active&limit=100');
  const [court, setCourt] = useState('');
  const [days, setDays] = useState<number[]>([2]);
  const [time, setTime] = useState('19:00');
  const [hours, setHours] = useState(2);
  const [from, setFrom] = useState(addDays(ymd(), 1));
  const [to, setTo] = useState(addDays(ymd(), 90));
  const [who, setWho] = useState<Who>({ name: '', phone: '' });
  const [corporate, setCorporate] = useState('');
  const [mode, setMode] = useState('pay_per_visit');
  const [pkg, setPkg] = useState('');
  const [skip, setSkip] = useState(true);
  const [preview, setPreview] = useState<{ dates: R[]; count: number; conflicts: number; total: string } | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const body = () => ({ courtId: court, weekdays: days, startTime: time, hours, startDate: from, endDate: to, customerId: who.customerId,
    guest: who.customerId ? undefined : { name: who.name || corporate, phone: who.phone || undefined }, corporateName: corporate || undefined, paymentMode: mode,
    packageCode: mode === 'package' ? pkg : undefined, skipConflicts: skip });
  const run = async (path: string) => {
    setBusy(true);
    setError(null);
    try {
      if (path === 'preview') setPreview(await request('POST', '/api/v1/sportclub/recurring:preview', body()));
      else {
        await request('POST', '/api/v1/sportclub/recurring', body(), idem());
        toast('Booking rutin dibuat');
        nav('/ops/sport');
      }
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="sc-new">
      <Card title="Pola mingguan" icon="event_repeat">
        <div className="oc-stack">
          <SelectField label="Lapangan" value={court} onChange={setCourt} placeholder="Pilih lapangan"
            options={(courts.data?.items ?? []).map((c) => ({ value: String(c.id), label: String(c.name) }))} />
          <div className="oc-row-wrap">{WEEKDAYS.map(([d, l]) => (
            <button key={d} type="button" className="oc-chip" aria-pressed={days.includes(d)} onClick={() => setDays(days.includes(d) ? days.filter((x) => x !== d) : [...days, d])}>{l}</button>
          ))}</div>
          <div className="oc-row-wrap">
            <TextField label="Jam mulai" type="time" value={time} onChange={setTime} />
            <SelectField label="Durasi" value={String(hours)} onChange={(v) => setHours(Number(v))} options={[1, 2, 3, 4].map((h) => ({ value: String(h), label: `${h} jam` }))} />
          </div>
          <div className="oc-row-wrap">
            <TextField label="Mulai" type="date" value={from} onChange={setFrom} />
            <TextField label="Sampai" type="date" value={to} onChange={setTo} />
          </div>
          <WhoField value={who} onChange={setWho} label="Penanggung jawab" />
          <TextField label="Komunitas / sekolah / kantor" value={corporate} onChange={setCorporate} />
          <SelectField label="Pembayaran" value={mode} onChange={setMode} options={[{ value: 'pay_per_visit', label: 'Bayar tiap pertemuan' },
            { value: 'package', label: 'Pakai paket 4x/8x' }, { value: 'invoice', label: 'Invoice perusahaan (member / corporate charge)' }]} />
          {mode === 'package' && <TextField label="Kode paket" value={pkg} onChange={setPkg} />}
          <Checkbox label="Lewati tanggal yang bentrok" checked={skip} onChange={setSkip} />
          <div className="oc-row-wrap">
            <button className="oc-btn oc-btn-neutral" disabled={!court || !days.length || busy} onClick={() => void run('preview')}>Cek tanggal</button>
            <button className="oc-btn oc-btn-primary" disabled={!preview || busy || (!who.customerId && !who.name && !corporate) || (preview.conflicts > 0 && !skip)}
              onClick={() => void run('create')}>Simpan booking rutin</button>
          </div>
          <ErrorAlert error={error} />
        </div>
      </Card>
      <Card title="Pertemuan" icon="calendar_month">
        {!preview ? <span className="oc-muted">Klik "Cek tanggal" untuk melihat pertemuan dan bentrokan sebelum disimpan.</span> : (
          <div className="oc-stack">
            <strong>{preview.count} pertemuan · {preview.conflicts} bentrok · total {money(preview.total)}</strong>
            <table className="sc-table">
              <tbody>
                {preview.dates.map((d) => (
                  <tr key={String(d.date)} data-conflict={d.status === 'conflict' || undefined}>
                    <td>{dayShort(String(d.start))} {hm(String(d.start))}–{hm(String(d.end))}</td>
                    <td>{d.status === 'conflict' ? <span style={{ color: 'var(--md-sys-color-error, #dc2626)' }}>{String(d.reason)}</span> : money(d.price)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}

// ── lists: check-in, payments, finish, history ──────────────────────────────

function useBookings(params: Record<string, string | undefined>) {
  const q = useGet<Page<CourtBooking>>(`/api/v1/sportclub/bookings${qs({ limit: '300', ...params })}`, { refetchInterval: 30_000 });
  useBoardLive(useCallback(() => { void q.refetch(); }, [q]));
  return q;
}

function BookingTable({ rows, loading, onOpen, action, empty }: {
  rows: CourtBooking[] | undefined; loading?: boolean; onOpen: (b: CourtBooking) => void; action?: (b: CourtBooking) => React.ReactNode; empty: React.ReactNode;
}) {
  return (
    <div className="oc-card">
      <DataTable rows={rows as unknown as R[]} loading={loading} rowKey={(r) => String(r.id)} onRowClick={(r) => onOpen(r as unknown as CourtBooking)} empty={empty}
        inlineActions actions={action ? (r) => action(r as unknown as CourtBooking) : undefined}
        columns={[
          { key: 'code', header: 'Booking', render: (r) => { const b = r as unknown as CourtBooking; return <><strong>{b.code}</strong><div className="oc-small"><ChannelTag channel={b.channel} /></div></>; } },
          { key: 'name', header: 'Penyewa', render: (r) => { const b = r as unknown as CourtBooking; return <>{b.name}<div className="oc-small oc-muted">{b.phone ?? ''}</div></>; } },
          { key: 'lines', header: 'Lapangan', render: (r) => (r as unknown as CourtBooking).lines.map((l) => <div key={l.id} className="oc-small">{l.courtName} · {dayShort(l.start)} {hm(l.start)}–{hm(l.end)}</div>) },
          { key: 'state', header: 'Status', render: (r) => <StatePill state={String(r.state)} /> },
          { key: 'pay', header: 'Bayar', render: (r) => <><PayPill status={String(r.payStatus)} />{Number(r.balance) > 0 && <div className="oc-small">sisa {money(r.balance)}</div>}</> },
        ]} />
    </div>
  );
}

/** Check-in (FR-66, FR-127, FR-136): scan or type, the reason when refused, or pick today's booking. */
export function CourtCheckInPage() {
  const toast = useToast();
  const [result, setResult] = useState<R | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [q, setQ] = useState('');
  const list = useBookings({ date: ymd(), q: q || undefined });
  const rows = (list.data?.items ?? []).filter((b) => ['scheduled', 'late'].includes(b.state) || b.lines.some((l) => l.canCheckIn)); // FR-125
  const scan = async (code: string) => {
    setError(null);
    try {
      const r = await request<R>('POST', '/api/v1/sportclub/check-in:scan', { code });
      setResult(r);
      if (r.result === 'checked_in') toast(String(r.message));
      void list.refetch();
    } catch (e) {
      setError(e);
    }
  };
  const booking = result?.booking as CourtBooking | undefined;
  return (
    <div className="oc-stack sc-desk">
      <h1 style={{ margin: 0, fontSize: 22 }}>Check-in</h1>
      <Card title="Scan QR / kode booking" icon="qr_code_scanner">
        <ScanField label="Scan QR e-ticket atau ketik kode booking" placeholder="Scan QR atau ketik kode (RSV-…), lalu Enter" onCode={(c) => void scan(c)} autoFocus />
        <ErrorAlert error={error} />
        {result && (
          <div className={`oc-alert ${result.result === 'checked_in' ? 'oc-alert-success' : 'oc-alert-error'}`} style={{ marginTop: 12 }} role="status">
            <strong>{result.result === 'checked_in' ? 'Check-in berhasil' : result.result === 'not_found' ? 'Tidak ditemukan' : 'Check-in ditolak'}</strong> — {String(result.message ?? '')}
            <div className="oc-row-wrap" style={{ marginTop: 8 }}>
              {result.nextAction === 'take_payment' && booking && <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={() => setOpen(booking.id)}>Terima Pembayaran</button>}
              {booking && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setOpen(booking.id)}>Buka booking</button>}
              {result.nextAction === 'search' && <span className="oc-small">Cari nama / HP di daftar di bawah.</span>}
            </div>
          </div>
        )}
        <p className="oc-small oc-muted" style={{ marginBottom: 0 }}>Kartu member dan tiket kolam/gym di-scan di <Link to="/ops/sport/tickets">Tiket Masuk</Link>.</p>
      </Card>
      <div style={{ maxWidth: 360 }}><TextField label="Cari nama, HP atau kode" value={q} onChange={setQ} /></div>
      <BookingTable rows={rows} loading={list.isLoading} onOpen={(b) => setOpen(b.id)} empty={<Empty title="Tidak ada booking yang menunggu check-in hari ini" icon="how_to_reg" />}
        action={(b) => (b.lines.some((l) => l.canCheckIn) ? <button className="oc-btn oc-btn-primary oc-btn-sm"
          onClick={(e) => { e.stopPropagation(); void request<R>('POST', `/api/v1/sportclub/bookings/${b.id}:check-in`, {}).then((r) => { setResult(r); void list.refetch(); }, setError); }}>Check-in</button> : null)} />
      {open && <BookingDrawer id={open} onClose={() => setOpen(null)} onChanged={() => void list.refetch()} />}
    </div>
  );
}

/** Pembayaran: every bill not paid yet (FR-125: only unpaid lines). */
export function CourtPaymentsPage() {
  const [open, setOpen] = useState<string | null>(null);
  const [pay, setPay] = useState<CourtBooking | null>(null);
  const list = useBookings({ from: addDays(ymd(), -7), to: addDays(ymd(), 60), 'filter[payStatus]': 'unpaid,partially_paid' });
  const rows = (list.data?.items ?? []).filter((b) => !b.void && b.state !== 'expired' && b.state !== 'no_show' && Number(b.balance) > 0);
  const total = rows.reduce((s, b) => s + Number(b.balance), 0);
  return (
    <div className="oc-stack sc-desk">
      <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
        <h1 style={{ margin: 0, fontSize: 22 }}>Pembayaran</h1><span className="oc-spacer" /><strong>{rows.length} tagihan · {money(total)}</strong>
      </div>
      <BookingTable rows={rows} loading={list.isLoading} onOpen={(b) => setOpen(b.id)} empty={<Empty title="Semua booking sudah lunas" icon="task_alt" />}
        action={(b) => <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={(e) => { e.stopPropagation(); setPay(b); }}>Terima {money(b.balance)}</button>} />
      {pay && (
        <DeskPayDialog amount={Number(pay.balance)} summary={[['Booking', pay.code], ['Penyewa', pay.name]]} members={pay.customerId ? [{ customerId: pay.customerId, name: pay.name }] : []}
          onClose={() => setPay(null)} pay={async (t) => (await request<{ payments?: R[] }>('POST', `/api/v1/sportclub/bookings/${pay.id}:pay`,
            { methodType: t.methodType, reference: t.reference, customerId: t.customerId }, idem())).payments ?? []}
          onFinish={() => { setPay(null); void list.refetch(); }} />
      )}
      {open && <BookingDrawer id={open} onClose={() => setOpen(null)} onChanged={() => void list.refetch()} />}
    </div>
  );
}

/** Selesai Main (FR-69): the courts in play and just played; settle (split per player) and close. */
export function CourtFinishPage() {
  const toast = useToast();
  const [open, setOpen] = useState<string | null>(null);
  const [settle, setSettle] = useState<CourtBooking | null>(null);
  const [split, setSplit] = useState(1);
  const list = useBookings({ date: ymd() });
  const rows = (list.data?.items ?? []).filter((b) => b.state === 'playing' || (b.state === 'finished' && Number(b.balance) > 0));
  const finish = (b: CourtBooking) => request('POST', `/api/v1/sportclub/bookings/${b.id}:complete`, {}).then(() => { toast('Selesai main — lapangan kosong'); void list.refetch(); },
    (e) => toast((e as Error).message, 'error'));
  const share = settle ? Math.ceil(Number(settle.balance) / split) : 0;
  return (
    <div className="oc-stack sc-desk">
      <h1 style={{ margin: 0, fontSize: 22 }}>Selesai Main</h1>
      <span className="oc-small oc-muted">Rincian tagihan: sewa, tambahan, F&B dari POS (Tagihan Lapangan), overtime. Lunasi (bisa dibagi per pemain), lalu tutup.</span>
      <BookingTable rows={rows} loading={list.isLoading} onOpen={(b) => setOpen(b.id)} empty={<Empty title="Tidak ada lapangan yang sedang main" icon="sports_score" />}
        action={(b) => (Number(b.balance) > 0
          ? <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={(e) => { e.stopPropagation(); setSplit(1); setSettle(b); }}>Settle {money(b.balance)}</button>
          : <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={(e) => { e.stopPropagation(); void finish(b); }}>Selesai</button>)} />
      {settle && (
        <Modal open onClose={() => setSettle(null)} title={`Settle ${settle.code}`} actions={<button className="oc-btn oc-btn-neutral" onClick={() => setSettle(null)}>Tutup</button>}>
          <div className="oc-stack">
            <div className="oc-row" style={{ alignItems: 'center' }}>
              <span>Bagi per pemain</span>
              <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setSplit(Math.max(1, split - 1))}>−</button><strong>{split}</strong>
              <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setSplit(split + 1)}>+</button>
              <span className="oc-spacer" /><strong>{money(share)} / orang</strong>
            </div>
            <SettleSplit b={settle} split={split} onDone={() => { setSettle(null); void list.refetch(); }} />
          </div>
        </Modal>
      )}
      {open && <BookingDrawer id={open} onClose={() => setOpen(null)} onChanged={() => void list.refetch()} />}
    </div>
  );
}

function SettleSplit({ b, split, onDone }: { b: CourtBooking; split: number; onDone: () => void }) {
  const [paying, setPaying] = useState<number | null>(null);
  const [left, setLeft] = useState(Number(b.balance));
  const [paid, setPaid] = useState(0);
  const amount = paid + 1 >= split ? left : Math.min(left, Math.ceil(Number(b.balance) / split));
  return (
    <>
      <div className="oc-row-wrap">
        {Array.from({ length: split }, (_, i) => (
          <button key={i} className={`oc-btn ${i < paid ? 'oc-btn-neutral' : 'oc-btn-ink'}`} disabled={i !== paid || left <= 0} onClick={() => setPaying(i)}>
            {i < paid ? `Pemain ${i + 1} ✓` : `Pemain ${i + 1}: ${money(amount)}`}</button>
        ))}
      </div>
      {paying !== null && (
        <DeskPayDialog amount={amount} summary={[['Booking', b.code], ['Pemain', `${paying + 1} dari ${split}`]]} members={b.customerId ? [{ customerId: b.customerId, name: b.name }] : []}
          onClose={() => setPaying(null)}
          pay={async (t) => (await request<{ payments?: R[] }>('POST', `/api/v1/sportclub/bookings/${b.id}:pay`, { methodType: t.methodType, reference: t.reference,
            customerId: t.customerId, amount: String(amount), payer: `Pemain ${paying + 1}` }, idem())).payments ?? []}
          onFinish={() => {
            setPaying(null);
            const l = left - amount;
            setLeft(l);
            setPaid(paid + 1);
            if (l <= 0) void request('POST', `/api/v1/sportclub/bookings/${b.id}:complete`, {}).finally(onDone);
          }} />
      )}
    </>
  );
}

/** Riwayat: today's and earlier bookings (FR-141: reprint, filters). */
export function CourtHistoryPage() {
  const [date, setDate] = useState(ymd());
  const [state, setState] = useState('');
  const [channel, setChannel] = useState('');
  const [q, setQ] = useState('');
  const [open, setOpen] = useState<string | null>(null);
  const list = useBookings({ date, q: q || undefined, 'filter[state]': state || undefined, 'filter[channel]': channel || undefined });
  return (
    <div className="oc-stack sc-desk">
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <h1 style={{ margin: 0, fontSize: 22 }}>Riwayat</h1><span className="oc-spacer" />
        <DateNav date={date} onChange={setDate} />
      </div>
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Cari kode, nama, HP" value={q} onChange={setQ} />
        <SelectField label="Status" value={state} onChange={setState} placeholder="Semua" options={Object.entries(STATE).map(([k, [l]]) => ({ value: k, label: l }))} />
        <SelectField label="Channel" value={channel} onChange={setChannel} placeholder="Semua" options={Object.entries(CHANNEL).map(([k, [l]]) => ({ value: k, label: l }))} />
      </div>
      <BookingTable rows={list.data?.items} loading={list.isLoading} onOpen={(b) => setOpen(b.id)} empty={<Empty title="Belum ada booking pada tanggal ini" icon="history" />} />
      {open && <BookingDrawer id={open} onClose={() => setOpen(null)} onChanged={() => void list.refetch()} />}
    </div>
  );
}

// ── court staff (FR-119) ────────────────────────────────────────────────────

interface StaffView { now: string; courts: { id: string; name: string; facilityName: string }[]; bookings: CourtBooking[];
  issues: { id: string; courtId: string; courtName: string; note: string | null; until: string | null; createdBy: string | null }[];
  ready: { id: string; courtId: string; reservationId: string | null; createdAt: string }[] }

export function CourtStaffPage() {
  const toast = useToast();
  const v = useGet<StaffView>('/api/v1/sportclub/court-staff', { refetchInterval: 30_000 });
  useBoardLive(useCallback(() => { void v.refetch(); }, [v]));
  const [issue, setIssue] = useState<string | null>(null);
  const [note, setNote] = useState('');
  const [hours, setHours] = useState(1);
  const [error, setError] = useState<unknown>(null);
  const report = async (body: R) => {
    setError(null);
    try {
      const r = await request<{ affected: CourtBooking[] }>('POST', '/api/v1/sportclub/court-reports', body);
      toast(body.kind === 'ready' ? 'Lapangan ditandai siap' : `Masalah dilaporkan — front desk diberi tahu${r.affected.length ? ` (${r.affected.length} booking perlu dipindah)` : ''}`);
      setIssue(null);
      setNote('');
      void v.refetch();
    } catch (e) {
      setError(e);
    }
  };
  const x = v.data;
  return (
    <div className="oc-stack sc-staff">
      <h1 style={{ margin: 0, fontSize: 22 }}>Petugas Lapangan</h1>
      <span className="oc-small oc-muted">Booking 2 jam ke depan. Siapkan lapangan dan nyalakan lampu 15 menit sebelum main.</span>
      <ErrorAlert error={v.error ?? error} />
      {!x ? <Skeleton rows={6} /> : x.courts.map((c) => {
        const next = x.bookings.flatMap((b) => b.lines.filter((l) => l.courtId === c.id && new Date(l.end) > new Date()).map((l) => ({ b, l })))
          .sort((a, b) => a.l.start.localeCompare(b.l.start));
        const issues = x.issues.filter((i) => i.courtId === c.id);
        return (
          <div key={c.id} className="sc-staff-court" data-issue={issues.length > 0 || undefined}>
            <div className="oc-row" style={{ alignItems: 'center' }}><strong>{c.name}</strong><span className="oc-small oc-muted">{c.facilityName}</span></div>
            {issues.map((i) => (
              <div key={i.id} className="oc-alert oc-alert-error">
                <Icon name="report" size={16} /> {i.note}{i.until ? ` · ditutup s.d. ${hm(i.until)}` : ''}
                <button className="oc-btn oc-btn-neutral oc-btn-sm" style={{ marginLeft: 8 }}
                  onClick={() => void request('POST', `/api/v1/sportclub/court-reports/${i.id}:resolve`, {}).then(() => { toast('Masalah selesai'); void v.refetch(); }, setError)}>Selesai</button>
              </div>
            ))}
            {next.length === 0 ? <span className="oc-small oc-muted">Tidak ada booking 2 jam ke depan.</span> : next.map(({ b, l }) => {
              const ready = x.ready.some((r) => r.reservationId === b.id);
              return (
                <div key={l.id} className="oc-row" style={{ alignItems: 'center' }}>
                  <span style={{ flex: 1 }}>{hm(l.start)}–{hm(l.end)} · {b.name} <StatePill state={l.state} /></span>
                  {ready ? <span className="oc-small" style={{ color: '#16a34a' }}>✓ Siap</span>
                    : <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => void report({ courtId: c.id, kind: 'ready', reservationId: b.id })}>Lapangan siap</button>}
                </div>
              );
            })}
            <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setIssue(c.id)}><Icon name="report" size={16} /> Lapor masalah</button>
          </div>
        );
      })}
      {issue && (
        <Modal open onClose={() => setIssue(null)} title="Lapor masalah lapangan" actions={<>
          <button className="oc-btn oc-btn-neutral" onClick={() => setIssue(null)}>Batal</button>
          <button className="oc-btn oc-btn-danger" disabled={!note.trim()} onClick={() => void report({ courtId: issue, kind: 'issue', note: note.trim(), hours })}>Laporkan</button></>}>
          <div className="oc-stack">
            <TextArea label="Masalah (mis. lampu mati, lantai basah, net rusak)" value={note} onChange={setNote} rows={3} />
            <SelectField label="Tutup lapangan sementara" value={String(hours)} onChange={(v2) => setHours(Number(v2))}
              options={[1, 2, 3, 4].map((h) => ({ value: String(h), label: `${h} jam` }))} />
            <span className="oc-small oc-muted">Jam yang kosong diblokir; booking yang terdampak tampil sebagai peringatan di front desk untuk dipindah.</span>
          </div>
        </Modal>
      )}
    </div>
  );
}

// ── incidents (FR-107) ──────────────────────────────────────────────────────

export function SportIncidentsPage() {
  const toast = useToast();
  const { can } = useAuth();
  const list = useGet<Page<R>>('/api/v1/sportclub/incidents?limit=100');
  const courts = useGet<Page<R>>('/api/v1/sportclub/courts?limit=100');
  const [form, setForm] = useState({ category: 'complaint', severity: 'medium', courtId: '', personName: '', description: '', actionTaken: '' });
  const [error, setError] = useState<unknown>(null);
  const save = async () => {
    setError(null);
    try {
      await request('POST', '/api/v1/sportclub/incidents', { ...form, courtId: form.courtId || undefined });
      toast('Insiden dicatat');
      setForm({ ...form, personName: '', description: '', actionTaken: '' });
      void list.refetch();
    } catch (e) {
      setError(e);
    }
  };
  return (
    <div className="oc-stack sc-desk">
      <h1 style={{ margin: 0, fontSize: 22 }}>Insiden</h1>
      <Card title="Catat insiden" icon="report">
        <div className="oc-stack">
          <div className="oc-row-wrap">
            <SelectField label="Jenis" value={form.category} onChange={(v) => setForm({ ...form, category: v })} options={[{ value: 'injury', label: 'Cedera' },
              { value: 'damage', label: 'Kerusakan' }, { value: 'complaint', label: 'Keluhan' }, { value: 'lost_item', label: 'Barang hilang' }, { value: 'other', label: 'Lainnya' }]} />
            <SelectField label="Tingkat" value={form.severity} onChange={(v) => setForm({ ...form, severity: v })} options={[{ value: 'low', label: 'Rendah' },
              { value: 'medium', label: 'Sedang' }, { value: 'high', label: 'Tinggi' }, { value: 'critical', label: 'Kritis' }]} />
            <SelectField label="Lapangan" value={form.courtId} onChange={(v) => setForm({ ...form, courtId: v })} placeholder="—"
              options={(courts.data?.items ?? []).map((c) => ({ value: String(c.id), label: String(c.name) }))} />
            <TextField label="Nama orang terkait" value={form.personName} onChange={(v) => setForm({ ...form, personName: v })} />
          </div>
          <TextArea label="Kejadian" value={form.description} onChange={(v) => setForm({ ...form, description: v })} rows={3} />
          <TextArea label="Tindakan" value={form.actionTaken} onChange={(v) => setForm({ ...form, actionTaken: v })} rows={2} />
          <div><button className="oc-btn oc-btn-primary" disabled={!form.description.trim()} onClick={() => void save()}>Simpan</button></div>
          <ErrorAlert error={error} />
        </div>
      </Card>
      <div className="oc-card">
        <DataTable rows={list.data?.items} loading={list.isLoading} rowKey={(r) => String(r.id)} columns={[
          { key: 'number', header: 'No.' }, { key: 'category', header: 'Jenis' }, { key: 'severity', header: 'Tingkat' }, { key: 'place', header: 'Tempat' },
          { key: 'description', header: 'Kejadian' }, { key: 'occurredAt', header: 'Waktu', render: (r) => new Date(String(r.occurredAt)).toLocaleString('id-ID') },
          { key: 'status', header: 'Status' }]}
          actions={(r) => (r.status === 'open' && can('sportclub.incident.manage') ? <button className="oc-btn oc-btn-neutral oc-btn-sm"
            onClick={() => { const a = window.prompt('Tindakan penutupan'); if (a) void request('POST', `/api/v1/sportclub/incidents/${String(r.id)}:close`, { reason: a }).then(() => void list.refetch()); }}>
            Tutup</button> : null)} />
      </div>
    </div>
  );
}

