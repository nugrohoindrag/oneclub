import React, { useCallback, useEffect, useState } from 'react';
import { API_BASE, getActiveProperty, qs, request, useGet, uuidv7, type Page } from '@oneclub/api-client';
import { formatNumber } from '@oneclub/i18n';
import {
  Drawer, Empty, ErrorAlert, Icon, Modal, MoneyField, SelectField, Skeleton, StatusPill, TextArea, TextField, useAuth, useToast,
  type StatusTone,
} from '@oneclub/shell';
import { DeskPayDialog, type DeskTender } from '../ops/deskpay';

// Sport Club court booking (docs/requirement-booking-sportclub-mgcc.md): the
// types of the API, the one status table of every screen (§13.3), the
// booking drawer with the desk actions and the slot grid of the desk.

export type R = Record<string, unknown>;

export interface CourtInfo {
  id: string; code: string; name: string; facilityId: string; facilityCode: string; facilityName: string; facilityType?: string | null;
  surface?: string | null; indoor: boolean; resourceId?: string | null; priceItem: string; onlineBooking: boolean; status: string; sortOrder: number;
  photoUrl?: string | null; rules: { minHours?: number; maxHours?: number; eveningFrom?: string };
}
export interface BookingLine {
  id: string; reservationId: string; lineNo: number; courtId: string | null; courtName: string; facilityId: string | null; facilityCode: string | null;
  facilityName: string | null; facilityType: string | null; resourceId: string; start: string; end: string; status: string; amount: string | null;
  state: string; checkInFrom: string; canCheckIn: boolean; ready: boolean;
}
export interface CourtBooking {
  id: string; code: string; status: string; rawChannel: string; customerId: string | null; customerName: string | null; guestName: string | null;
  phone: string | null; email: string | null; corporateName: string | null; notes: string | null; holdExpiresAt: string | null; folioId: string | null;
  recurringGroupId: string | null; createdAt: string; checkedInAt: string | null; charges: string; paid: string; serviceFee: string; tax: string;
  channel: string; state: string; payStatus: string; balance: string; holdSeconds: number; name: string; start: string | null; end: string | null;
  packageCode?: string; package?: R; promoCode?: string; void: boolean; voidReason?: string; token?: string; lines: BookingLine[];
}
export interface BookingDetail {
  booking: CourtBooking;
  folio: (R & { id: string; summary: R; lines: R[]; payments: R[] }) | null;
  history: { action: string; fromStatus: string | null; toStatus: string | null; channel: string; reason: string | null; actorName: string | null; createdAt: string; details: R }[];
  payments?: R[];
}
export interface GridSlot { start: string; end: string; status: 'available' | 'booked' | 'blocked' | 'past'; price: string | null; listPrice: string | null }
export interface Grid { date: string; facilityId: string; courts: { court: CourtInfo; slots: GridSlot[]; free: number }[]; taxIncluded: boolean; windowDays: number }
export interface Quote {
  currency: string; lines: { courtId: string; courtName: string; facilityName: string; start: string; end: string; hours: number; listPrice: string; discount: string;
    net: string; tax: string; total: string; promotions: string[] }[]; rent: string; discount: string; net: string; tax: string; serviceFee: string; total: string;
  promoCode?: string; promoApplied: boolean; promoError?: string; methods: { code: string; label: string; fee: string; total: string; cheapest: boolean }[];
}
export interface CourtPolicy {
  windowDays: number; deskWindowDays: number; holdMinutes: number; minHours: number; maxHours: number; noShowMinutes: number; reminderMinutes: number;
  checkInMinutes: number; prospectVisits: number; taxIncluded: boolean;
  methods: { code: string; methodType: string; label: string; fee: string; percent: string; active: boolean }[];
  extras: { code: string; name: string; price: string; component: string }[];
  terms: Record<string, string>;
}

export const SC = ['/api/v1/sportclub', '/api/v1/billing', '/api/v1/reservation'];
export const idem = () => ({ 'Idempotency-Key': uuidv7() });
export const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Math.round(Number(v)))}`);
export const hm = (iso: string | null | undefined) => (iso ? new Date(iso).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : '—');
export const dayShort = (iso: string | null | undefined) =>
  (iso ? new Date(iso).toLocaleDateString('id-ID', { weekday: 'short', day: 'numeric', month: 'short' }) : '—');
export const ymd = (d = new Date()) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
export const addDays = (day: string, n: number) => {
  const d = new Date(`${day}T00:00:00`);
  d.setDate(d.getDate() + n);
  return ymd(d);
};

/** One status table for every screen (§13.3). */
export const STATE: Record<string, [string, StatusTone, string]> = {
  awaiting_payment: ['Menunggu Pembayaran', 'warning', '#f59e0b'],
  expired: ['Kedaluwarsa', 'neutral', '#9ca3af'],
  scheduled: ['Terjadwal', 'info', '#2563eb'],
  late: ['Belum Datang', 'error', '#dc2626'],
  playing: ['Sedang Main', 'success', '#16a34a'],
  finished: ['Selesai', 'neutral', '#64748b'],
  no_show: ['No-show', 'error', '#7f1d1d'],
  void: ['Void', 'neutral', '#6b7280'],
};
export const PAY: Record<string, [string, StatusTone]> = {
  unpaid: ['Belum bayar', 'error'], partially_paid: ['Sebagian', 'warning'], paid: ['Lunas', 'success'], overpaid: ['Lebih bayar', 'info'],
};
export const CHANNEL: Record<string, [string, string]> = {
  website: ['Website', 'language'], member_app: ['Member App', 'smartphone'], walk_in: ['Walk-in', 'directions_walk'], phone: ['Telepon', 'call'],
  recurring: ['Booking Rutin', 'event_repeat'],
};
export function StatePill({ state }: { state: string }) {
  const [l, t] = STATE[state] ?? [state, 'neutral'];
  return <StatusPill status={state} label={l} tone={t} />;
}
export function PayPill({ status }: { status: string }) {
  const [l, t] = PAY[status] ?? [status, 'neutral'];
  return <StatusPill status={status} label={l} tone={t} />;
}
export function ChannelTag({ channel }: { channel: string }) {
  const [l, i] = CHANNEL[channel] ?? [channel, 'help'];
  return <span className="sc-channel"><Icon name={i} size={14} /> {l}</span>;
}

/** Re-renders when the court board changes (SSE of the sportclub topic) and every 30 s (FR-58, FR-134). */
export function useBoardLive(onEvent: () => void) {
  const { propertyId } = useAuth();
  const cb = useCallback(onEvent, [onEvent]);
  useEffect(() => {
    if (!propertyId || typeof EventSource === 'undefined') return undefined;
    const es = new EventSource(`${API_BASE}/api/v1/sportclub/board/stream?propertyId=${propertyId}`, { withCredentials: true });
    es.addEventListener('sportclub.board', () => cb());
    return () => es.close();
  }, [propertyId, cb]);
}

export function useFacilities() {
  const f = useGet<Page<R>>('/api/v1/sportclub/facilities?limit=50');
  const list = (f.data?.items ?? []).filter((x) => x.usageMode === 'slot_booking' && x.status === 'active')
    .sort((a, b) => Number(a.sortOrder ?? 0) - Number(b.sortOrder ?? 0));
  return { ...f, list };
}

// ── customer ────────────────────────────────────────────────────────────────

export type Who = { customerId?: string; name: string; phone: string; email?: string };

/** A member or customer found by name, member no. or phone, or a guest typed in (FR-62: known by phone). */
export function WhoField({ value, onChange, label = 'Pelanggan' }: { value: Who; onChange: (w: Who) => void; label?: string }) {
  const [q, setQ] = useState('');
  const found = useGet<Page<R>>(q.trim().length >= 2 ? `/api/v1/crm/customers${qs({ q: q.trim(), limit: 8 })}` : null);
  if (value.customerId) {
    return (
      <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
        <span><Icon name="person" size={18} /> <strong>{value.name}</strong>{value.phone ? ` · ${value.phone}` : ''}</span>
        <button type="button" className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => { onChange({ name: '', phone: '' }); setQ(''); }}>Ganti</button>
      </div>
    );
  }
  return (
    <div className="oc-stack" style={{ gap: 8 }}>
      <TextField label={`${label}: cari member / pelanggan (nama, No. Member, HP)`} value={q} onChange={setQ} />
      {(found.data?.items ?? []).length > 0 && (
        <div className="oc-row-wrap">
          {(found.data?.items ?? []).map((c) => (
            <button key={String(c.id)} type="button" className="oc-chip" style={{ height: 40 }}
              onClick={() => onChange({ customerId: String(c.id), name: String(c.name), phone: String(c.phone ?? '') })}>
              {String(c.name)}{c.code ? ` · ${String(c.code)}` : ''}{c.phone ? ` · ${String(c.phone)}` : ''}</button>
          ))}
        </div>
      )}
      <div className="oc-row-wrap">
        <TextField label="Atau tamu: nama" value={value.name} onChange={(v) => onChange({ ...value, name: v })} />
        <TextField label="No. HP" value={value.phone} onChange={(v) => onChange({ ...value, phone: v })} inputMode="tel" />
      </div>
    </div>
  );
}

// ── slot grid of the desk (FR-61) ──────────────────────────────────────────

export interface Pick { courtId: string; courtName: string; start: string; end: string; price: number }

/** The hours of every court of a sport: free (price before tax), booked, blocked, past; tap to choose. */
export function SlotGrid({ facilityId, date, picks, onToggle, compact }: {
  facilityId: string; date: string; picks: Pick[]; onToggle: (p: Pick) => void; compact?: boolean;
}) {
  const g = useGet<Grid>(facilityId ? `/api/v1/sportclub/grid${qs({ facilityId, date })}` : null, { refetchInterval: 30_000 });
  if (!facilityId) return <Empty title="Pilih cabang olahraga" icon="sports_tennis" />;
  if (g.isLoading) return <Skeleton rows={4} />;
  if (g.error) return <div className="oc-stack"><ErrorAlert error={g.error} /><button className="oc-btn oc-btn-neutral" onClick={() => void g.refetch()}>Muat ulang</button></div>;
  const courts = g.data?.courts ?? [];
  if (!courts.length) return <Empty title="Tidak ada lapangan aktif untuk cabor ini" icon="sports_tennis" />;
  return (
    <div className="oc-stack">
      {courts.map(({ court, slots, free }) => (
        <div key={court.id} className="sc-grid-court">
          <div className="sc-grid-head"><strong>{court.name}</strong><span className="oc-small oc-muted">{court.surface ?? ''} · {court.indoor ? 'Indoor' : 'Outdoor'}</span>
            <span className="oc-spacer" /><span className="oc-small">{free} jadwal tersedia</span></div>
          {slots.length === 0 ? <div className="oc-muted oc-small">Tutup pada tanggal ini.</div> : (
            <div className={`sc-slots${compact ? ' sc-slots-compact' : ''}`}>
              {slots.map((s) => {
                const chosen = picks.some((p) => p.courtId === court.id && p.start === s.start);
                const label = chosen ? 'Dipilih' : s.status === 'available' ? money(s.price) : s.status === 'booked' ? 'Booked' : s.status === 'blocked' ? 'Tutup' : 'Lewat';
                return (
                  <button key={s.start} type="button" className="sc-slot" data-status={chosen ? 'chosen' : s.status} disabled={s.status !== 'available' && !chosen}
                    aria-pressed={chosen} onClick={() => onToggle({ courtId: court.id, courtName: court.name, start: s.start, end: s.end, price: Number(s.price ?? 0) })}>
                    <strong>{hm(s.start)}–{hm(s.end)}</strong>
                    <span>{s.listPrice && !chosen ? <s className="oc-muted">{money(s.listPrice)}</s> : null} {label}</span>
                  </button>
                );
              })}
            </div>
          )}
        </div>
      ))}
      <span className="oc-small oc-muted">Harga per jam sesuai brosur Rates 2025, {g.data?.taxIncluded ? 'sudah termasuk' : 'belum termasuk'} pajak.</span>
    </div>
  );
}

/** The quote of the chosen hours (the one price function of every screen, FR-122). */
export function useQuote(lines: Pick[], promoCode = '', customerId?: string) {
  const key = JSON.stringify([lines.map((l) => [l.courtId, l.start, l.end]), promoCode, customerId]);
  const [quote, setQuote] = useState<Quote | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    if (!lines.length) { setQuote(null); setError(null); return undefined; }
    let live = true;
    request<Quote>('POST', '/api/v1/sportclub/quote', { lines: lines.map((l) => ({ courtId: l.courtId, start: l.start, end: l.end })), promoCode: promoCode || undefined,
      customerId }).then((q) => { if (live) { setQuote(q); setError(null); } }, (e) => { if (live) { setQuote(null); setError(e); } });
    return () => { live = false; };
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  return { quote, error };
}

// ── booking drawer (FR-55, FR-66..69, FR-129) ──────────────────────────────

export function BookingDrawer({ id, onClose, onChanged }: { id: string; onClose: () => void; onChanged?: () => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const [data, setData] = useState<BookingDetail | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [dialog, setDialog] = useState<string | null>(null);
  const load = useCallback(() => request<BookingDetail>('GET', `/api/v1/sportclub/bookings/${id}`).then(setData, setError), [id]);
  useEffect(() => { void load(); }, [load]);
  useBoardLive(useCallback(() => { void load(); }, [load]));
  const act = async (path: string, body: R = {}, done = 'Tersimpan') => {
    setBusy(true);
    setError(null);
    try {
      const out = await request<BookingDetail | R>('POST', `/api/v1/sportclub/bookings/${id}:${path}`, body, idem());
      if ('booking' in out && out.booking) setData(out as BookingDetail);
      else await load();
      const msg = (out as R).message;
      toast(typeof msg === 'string' && msg ? msg : done);
      onChanged?.();
      return out;
    } catch (e) {
      setError(e); // the form stays as it was (FR-138)
      return null;
    } finally {
      setBusy(false);
    }
  };
  const b = data?.booking;
  const bal = Number(b?.balance ?? 0);
  const playing = b?.lines.some((l) => l.state === 'playing');
  return (
    <Drawer open onClose={onClose} title={b ? `${b.code} · ${b.name}` : 'Booking'}>
      {!data && !error && <Skeleton rows={8} />}
      <ErrorAlert error={error} />
      {b && (
        <div className="oc-stack">
          <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
            <StatePill state={b.state} /><PayPill status={b.payStatus} /><ChannelTag channel={b.channel} />
            {b.packageCode && <StatusPill status="package" label={`Paket ${b.packageCode}`} tone="info" />}
            {b.state === 'awaiting_payment' && b.holdSeconds > 0 && <span className="oc-small oc-muted">batas bayar {Math.ceil(b.holdSeconds / 60)} menit</span>}
          </div>
          <div className="sc-actions">
            {can('sportclub.booking.operate') && b.lines.some((l) => l.canCheckIn) && (
              <button className="oc-btn oc-btn-primary" disabled={busy || bal > 0} title={bal > 0 ? 'Lunasi dulu sebelum check-in' : undefined}
                onClick={() => void act('check-in', {}, 'Check-in berhasil')}><Icon name="how_to_reg" size={18} /> Check-in</button>
            )}
            {can('sportclub.booking.operate') && bal > 0 && !b.void && b.state !== 'expired' && (
              <button className="oc-btn oc-btn-ink" disabled={busy} onClick={() => setDialog('pay')}><Icon name="payments" size={18} /> Terima Pembayaran</button>
            )}
            {can('sportclub.booking.operate') && (playing || b.state === 'scheduled' || b.state === 'late') && (
              <button className="oc-btn oc-btn-neutral" disabled={busy} onClick={() => setDialog('extras')}><Icon name="add_shopping_cart" size={18} /> Tambahan</button>
            )}
            {can('sportclub.booking.operate') && b.lines.some((l) => l.status === 'checked_in' || l.status === 'confirmed') && (
              <button className="oc-btn oc-btn-neutral" disabled={busy} onClick={() => setDialog('extend')}><Icon name="more_time" size={18} /> Perpanjang</button>
            )}
            {can('sportclub.booking.override') && b.lines.some((l) => l.status === 'confirmed' || l.status === 'held') && (
              <button className="oc-btn oc-btn-neutral" disabled={busy} onClick={() => setDialog('move')}><Icon name="swap_horiz" size={18} /> Pindah Lapangan/Jam</button>
            )}
            {can('sportclub.booking.operate') && b.lines.some((l) => l.state === 'late') && (
              <button className="oc-btn oc-btn-neutral" disabled={busy} onClick={() => setDialog('no-show')}><Icon name="person_off" size={18} /> Tandai No-show</button>
            )}
            {can('sportclub.booking.operate') && playing && (
              <button className="oc-btn oc-btn-neutral" disabled={busy} onClick={() => (bal > 0 ? setDialog('pay') : void act('complete', {}, 'Selesai main'))}>
                <Icon name="sports_score" size={18} /> Selesai Main</button>
            )}
            <button className="oc-btn oc-btn-neutral" onClick={() => printReceipt(data)}><Icon name="print" size={18} /> Cetak struk</button>
            {b.token && <a className="oc-btn oc-btn-neutral" href={`${API_BASE}/api/v1/public/court-bookings/${b.token}/e-ticket.pdf?propertyId=${getActiveProperty()}`}
              target="_blank" rel="noreferrer"><Icon name="confirmation_number" size={18} /> E-ticket</a>}
            {can('sportclub.booking.override') && b.folioId && bal > 0 && (
              <button className="oc-btn oc-btn-neutral" disabled={busy} onClick={() => setDialog('discount')}><Icon name="sell" size={18} /> Diskon</button>
            )}
            {can('sportclub.booking.override') && !b.void && ['scheduled', 'late', 'awaiting_payment'].includes(b.state) && (
              <button className="oc-btn oc-btn-danger" disabled={busy} onClick={() => setDialog('void')}><Icon name="block" size={18} /> Void</button>
            )}
          </div>
          <span className="oc-small oc-muted">Booking tidak dapat dibatalkan dan tidak ada refund. Pindah jadwal hanya oleh supervisor.</span>
          <table className="sc-table">
            <thead><tr><th>Lapangan</th><th>Jam</th><th>Status</th><th style={{ textAlign: 'right' }}>Harga</th></tr></thead>
            <tbody>
              {b.lines.map((l) => (
                <tr key={l.id}>
                  <td>{l.courtName}<div className="oc-small oc-muted">{l.facilityName}</div></td>
                  <td>{dayShort(l.start)} {hm(l.start)}–{hm(l.end)}</td>
                  <td><StatePill state={l.state} />{l.ready && <div className="oc-small" style={{ color: 'var(--md-sys-color-success, #16a34a)' }}>Lapangan siap</div>}</td>
                  <td style={{ textAlign: 'right' }}>{money(l.amount)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="sc-kv">
            <span>Penyewa</span><strong>{b.name}{b.phone ? ` · ${b.phone}` : ''}</strong>
            {b.corporateName && <><span>Perusahaan / komunitas</span><strong>{b.corporateName}</strong></>}
            <span>Tagihan</span><strong>{money(b.charges)}</strong>
            <span>Sudah dibayar</span><strong>{money(b.paid)}</strong>
            <span>Sisa</span><strong style={{ color: bal > 0 ? 'var(--md-sys-color-error, #dc2626)' : undefined }}>{money(b.balance)}</strong>
            {b.promoCode && <><span>Kode promo</span><strong>{b.promoCode}</strong></>}
            {b.void && <><span>Alasan void</span><strong>{b.voidReason}</strong></>}
            {b.notes && <><span>Catatan</span><span>{b.notes}</span></>}
          </div>
          {data?.folio && (
            <details open>
              <summary><strong>Rincian tagihan</strong></summary>
              <table className="sc-table">
                <tbody>
                  {data.folio.lines.filter((l) => !l.voidedAt).map((l) => (
                    <tr key={String(l.id)}><td>{String(l.description)}</td><td style={{ textAlign: 'right' }}>{money(l.total)}</td></tr>
                  ))}
                  {data.folio.payments.filter((p) => p.status === 'completed' || p.status === 'pending').map((p) => (
                    <tr key={String(p.id)}><td>Pembayaran {String(p.methodType).replace(/_/g, ' ')}{p.payerName ? ` · ${String(p.payerName)}` : ''}
                      {p.status === 'pending' ? ' (menunggu)' : ''}</td><td style={{ textAlign: 'right' }}>−{money(p.amount)}</td></tr>
                  ))}
                  {b.packageCode && <tr><td>Paket {b.packageCode} (sisa {String(b.package?.remaining ?? '—')}x)</td><td style={{ textAlign: 'right' }}>Lunas</td></tr>}
                </tbody>
              </table>
            </details>
          )}
          <details>
            <summary><strong>Riwayat</strong></summary>
            {(data?.history ?? []).map((h, i) => (
              <div key={i} className="oc-small">{new Date(h.createdAt).toLocaleString('id-ID')} · {h.action.replace(/_/g, ' ')}{h.reason ? ` — ${h.reason}` : ''} · {h.actorName ?? 'system'}</div>
            ))}
          </details>
        </div>
      )}
      {b && dialog === 'pay' && (
        <DeskPayDialog amount={bal} summary={[['Booking', b.code], ['Penyewa', b.name], ['Lapangan', b.lines.map((l) => `${l.courtName} ${hm(l.start)}`).join(', ')]]}
          members={b.customerId ? [{ customerId: b.customerId, name: b.name }] : []} onClose={() => setDialog(null)}
          pay={async (t: DeskTender) => {
            const out = await request<BookingDetail>('POST', `/api/v1/sportclub/bookings/${id}:pay`, { methodType: t.methodType, reference: t.reference,
              customerId: t.customerId }, idem());
            setData(out);
            onChanged?.();
            return out.payments ?? [];
          }}
          onFinish={() => { setDialog(null); void load(); }} />
      )}
      {b && dialog === 'extras' && <ExtrasModal onClose={() => setDialog(null)} onSave={(items) => act('extras', { items }, 'Tambahan ditagihkan').then(() => setDialog(null))} />}
      {b && dialog === 'extend' && <ExtendModal b={b} onClose={() => setDialog(null)} onSave={(lineId, hours) => act('extend', { lineId, hours }, 'Diperpanjang').then(() => setDialog(null))} />}
      {b && dialog === 'move' && <MoveModal b={b} onClose={() => setDialog(null)} onSave={(body) => act('move', body, 'Jadwal dipindah').then((o) => o && setDialog(null))} />}
      {b && dialog === 'no-show' && <ReasonModal title="Tandai No-show" label="Keterangan" onClose={() => setDialog(null)}
        onSave={(reason) => act('no-show', { reason }, 'Ditandai No-show').then(() => setDialog(null))} />}
      {b && dialog === 'void' && <ReasonModal title="Void booking (salah input)" label="Alasan void (wajib)" danger onClose={() => setDialog(null)}
        help="Void hanya untuk salah input di meja. Tidak ada refund otomatis: pembayaran yang sudah masuk ditangani Finance."
        onSave={(reason) => act('void', { reason }, 'Booking di-void').then(() => setDialog(null))} />}
      {b && dialog === 'discount' && <DiscountModal max={bal} onClose={() => setDialog(null)}
        onSave={(amount, reason) => act('discount', { amount, reason }, 'Diskon diberikan').then(() => setDialog(null))} />}
    </Drawer>
  );
}

export function ReasonModal({ title, label, help, danger, onClose, onSave }: {
  title: string; label: string; help?: string; danger?: boolean; onClose: () => void; onSave: (reason: string) => void;
}) {
  const [v, setV] = useState('');
  return (
    <Modal open onClose={onClose} title={title} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Batal</button>
      <button className={`oc-btn ${danger ? 'oc-btn-danger' : 'oc-btn-ink'}`} disabled={!v.trim()} onClick={() => onSave(v.trim())}>Simpan</button></>}>
      {help && <p className="oc-small oc-muted">{help}</p>}
      <TextArea label={label} value={v} onChange={setV} rows={3} required />
    </Modal>
  );
}

function ExtrasModal({ onClose, onSave }: { onClose: () => void; onSave: (items: R[]) => void }) {
  const pol = useGet<CourtPolicy>('/api/v1/sportclub/court-policy');
  const [qty, setQty] = useState<Record<string, number>>({});
  const items = Object.entries(qty).filter(([, n]) => n > 0).map(([code, quantity]) => ({ code, quantity }));
  const total = (pol.data?.extras ?? []).reduce((s, x) => s + Number(x.price) * (qty[x.code] ?? 0), 0);
  return (
    <Modal open onClose={onClose} title="Tambahan saat main" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Batal</button>
      <button className="oc-btn oc-btn-ink" disabled={!items.length} onClick={() => onSave(items)}>Tagihkan {money(total)}</button></>}>
      <div className="oc-stack">
        {(pol.data?.extras ?? []).map((x) => (
          <div key={x.code} className="oc-row" style={{ alignItems: 'center' }}>
            <span style={{ flex: 1 }}>{x.name}<div className="oc-small oc-muted">{money(x.price)} (belum termasuk pajak)</div></span>
            <button type="button" className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setQty({ ...qty, [x.code]: Math.max(0, (qty[x.code] ?? 0) - 1) })}>−</button>
            <strong style={{ minWidth: 24, textAlign: 'center' }}>{qty[x.code] ?? 0}</strong>
            <button type="button" className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setQty({ ...qty, [x.code]: (qty[x.code] ?? 0) + 1 })}>+</button>
          </div>
        ))}
        <span className="oc-small oc-muted">Minuman dari Sport Café dipesan di POS dengan memilih booking ini.</span>
      </div>
    </Modal>
  );
}

function ExtendModal({ b, onClose, onSave }: { b: CourtBooking; onClose: () => void; onSave: (lineId: string, hours: number) => void }) {
  const lines = b.lines.filter((l) => l.status === 'checked_in' || l.status === 'confirmed');
  const [line, setLine] = useState(lines[lines.length - 1]?.id ?? '');
  const [hours, setHours] = useState(1);
  return (
    <Modal open onClose={onClose} title="Perpanjang / overtime" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Batal</button>
      <button className="oc-btn oc-btn-ink" disabled={!line} onClick={() => onSave(line, hours)}>Perpanjang</button></>}>
      <div className="oc-stack">
        <SelectField label="Lapangan" value={line} onChange={setLine} options={lines.map((l) => ({ value: l.id, label: `${l.courtName} ${hm(l.start)}–${hm(l.end)}` }))} />
        <div className="oc-row" style={{ alignItems: 'center' }}>
          <span>Tambah</span>
          <button type="button" className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setHours(Math.max(1, hours - 1))}>−</button>
          <strong>{hours} jam</strong>
          <button type="button" className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setHours(Math.min(4, hours + 1))}>+</button>
        </div>
        <span className="oc-small oc-muted">Hanya bila jam berikutnya kosong; harga sesuai jam tersebut.</span>
      </div>
    </Modal>
  );
}

function MoveModal({ b, onClose, onSave }: { b: CourtBooking; onClose: () => void; onSave: (body: R) => void }) {
  const lines = b.lines.filter((l) => l.status === 'confirmed' || l.status === 'held');
  const [line, setLine] = useState(lines[0]?.id ?? '');
  const l = lines.find((x) => x.id === line);
  const [date, setDate] = useState(l ? ymd(new Date(l.start)) : ymd());
  const [pick, setPick] = useState<Pick | null>(null);
  const [reason, setReason] = useState('');
  const hours = l ? Math.round((new Date(l.end).getTime() - new Date(l.start).getTime()) / 3600_000) : 1;
  return (
    <Modal open wide onClose={onClose} title="Pindah lapangan / jam (supervisor)" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Batal</button>
      <button className="oc-btn oc-btn-ink" disabled={!pick || !reason.trim()}
        onClick={() => pick && onSave({ lineId: line, courtId: pick.courtId, start: pick.start, reason: reason.trim() })}>Pindahkan</button></>}>
      <div className="oc-stack">
        <div className="oc-row-wrap">
          <SelectField label="Baris" value={line} onChange={(v) => { setLine(v); setPick(null); }}
            options={lines.map((x) => ({ value: x.id, label: `${x.courtName} ${dayShort(x.start)} ${hm(x.start)}–${hm(x.end)}` }))} />
          <TextField label="Tanggal" type="date" value={date} onChange={(v) => { setDate(v); setPick(null); }} />
        </div>
        <span className="oc-small oc-muted">Pilih jam mulai baru ({hours} jam). Sistem menolak bila bentrok; selisih harga hanya ditagih bila lebih mahal.</span>
        {l?.facilityId && <SlotGrid facilityId={l.facilityId} date={date} compact picks={pick ? [pick] : []} onToggle={(p) => setPick(pick?.start === p.start && pick.courtId === p.courtId ? null : p)} />}
        <TextField label="Alasan (wajib)" value={reason} onChange={setReason} />
      </div>
    </Modal>
  );
}

function DiscountModal({ max, onClose, onSave }: { max: number; onClose: () => void; onSave: (amount: string, reason: string) => void }) {
  const [amount, setAmount] = useState('');
  const [reason, setReason] = useState('');
  return (
    <Modal open onClose={onClose} title="Diskon manual (supervisor)" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Batal</button>
      <button className="oc-btn oc-btn-ink" disabled={!Number(amount) || Number(amount) > max || !reason.trim()} onClick={() => onSave(amount, reason.trim())}>Simpan</button></>}>
      <div className="oc-stack">
        <MoneyField label={`Diskon (maks. ${money(max)})`} value={amount} onChange={setAmount} />
        <TextField label="Alasan (wajib, tercatat di audit)" value={reason} onChange={setReason} />
      </div>
    </Modal>
  );
}

/** Prints the receipt of a booking (FR-63). */
export function printReceipt(d: BookingDetail | null) {
  if (!d) return;
  const b = d.booking;
  const rows = (d.folio?.lines ?? []).filter((l) => !l.voidedAt).map((l) => `<tr><td>${String(l.description)}</td><td style="text-align:right">${money(l.total)}</td></tr>`).join('');
  const pays = (d.folio?.payments ?? []).filter((p) => p.status === 'completed').map((p) => `<tr><td>Bayar ${String(p.methodType).replace(/_/g, ' ')}</td><td style="text-align:right">${money(p.amount)}</td></tr>`).join('');
  const w = window.open('', '_blank', 'width=380,height=640');
  if (!w) return;
  w.document.write(`<html><head><title>${b.code}</title><style>body{font:13px system-ui;margin:16px}table{width:100%;border-collapse:collapse}td{padding:3px 0}
    h2{margin:0 0 4px}.m{color:#666}</style></head><body><h2>Sport Club</h2><div class="m">${b.code} · ${new Date().toLocaleString('id-ID')}</div>
    <p><strong>${b.name}</strong></p><table>${b.lines.map((l) => `<tr><td>${l.courtName}</td><td style="text-align:right">${dayShort(l.start)} ${hm(l.start)}–${hm(l.end)}</td></tr>`).join('')}</table>
    <hr/><table>${rows}<tr><td><strong>Total</strong></td><td style="text-align:right"><strong>${money(b.charges)}</strong></td></tr>${pays}
    <tr><td>Sisa</td><td style="text-align:right">${money(b.balance)}</td></tr></table>
    <p class="m">Booking tidak dapat dibatalkan dan tidak ada refund.</p><script>window.print()</script></body></html>`);
  w.document.close();
}

export function useCourtPolicy() {
  return useGet<CourtPolicy>('/api/v1/sportclub/court-policy');
}

export function Legend() {
  return (
    <div className="sc-legend">
      {Object.entries(STATE).filter(([k]) => !['expired', 'void'].includes(k)).map(([k, [l, , c]]) => (
        <span key={k}><i style={{ background: c }} /> {l}</span>
      ))}
      <span><i style={{ background: '#cbd5e1' }} /> Diblokir</span>
    </div>
  );
}
