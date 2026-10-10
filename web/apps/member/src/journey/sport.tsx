import { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { API_BASE, getActiveProperty, qs, request, useGet, uuidv7, type Page, type Schemas } from '@oneclub/api-client';
import { ErrorAlert, Icon, QRCode, Skeleton, getMemberProgram, useAuth, useBootstrap, useNavigation, useToast } from '@oneclub/shell';
import { MemberCard } from '../golf';
import { PaymentPanel } from './pay';
import { DateStrip } from './teetime';
import { Chip, Head, Rows, Steps, money, type Row } from './ui';

// Sport Club in the Member App (docs/requirement-booking-sportclub-mgcc.md
// §8.4): Pesan Lapangan with the same steps, grid and price function as the
// website (FR-113) — no personal data to type, member charge or online
// payment (mock gateway, service fee to the member, FR-114) or a court
// package; Kelas, Kartu Masuk, Ajak Tamu (FR-115..117), the court bookings
// with their QR and e-ticket, the visits and the package quota. No
// cancellation and no refund (FR-118).

interface Facility { id: string; code: string; name: string; usageMode: string; sortOrder: number; courts: number; indoor: boolean; fromPrice: string | null;
  content: { nameEn?: string; slug?: string; icon?: string; photos?: string[]; description?: Record<string, string>; rules?: Record<string, string[]> } }
interface SportPage { facilities: Facility[]; courts: { id: string; name: string; facilityId: string; surface?: string | null; indoor: boolean; priceItem: string }[];
  windowDays: number; holdMinutes: number; taxIncluded: boolean; methods: { code: string; label: string; fee: string; percent: string }[]; terms: Record<string, string> }
interface Slot { start: string; end: string; status: string; price: string | null; listPrice: string | null }
interface Grid { courts: { court: { id: string; name: string; surface?: string | null; indoor: boolean; priceItem: string }; slots: Slot[]; free: number }[] }
interface Pick { courtId: string; courtName: string; start: string; end: string; price: number }
export interface CourtBooking {
  id: string; code: string; state: string; payStatus: string; channel: string; name: string; start: string | null; end: string | null; charges: string; paid: string;
  balance: string; tax: string; serviceFee: string; holdSeconds: number; token?: string; packageCode?: string; folioId: string | null;
  lines: { id: string; courtName: string; facilityName: string | null; start: string; end: string; state: string; amount: string | null }[];
}

const STATE: Record<string, [string, 'ok' | 'warn' | 'bad' | 'info' | undefined]> = {
  awaiting_payment: ['Menunggu Pembayaran', 'warn'], expired: ['Kedaluwarsa', 'bad'], scheduled: ['Terjadwal', 'ok'], late: ['Belum Datang', 'warn'],
  playing: ['Sedang Main', 'info'], finished: ['Selesai', 'ok'], no_show: ['No-show', 'bad'], void: ['Void', 'bad'],
};
export function StateChip({ state }: { state: string }) {
  const [l, t] = STATE[state] ?? [state, undefined];
  return <Chip tone={t}>{l}</Chip>;
}
const hm = (iso: string | null | undefined) => (iso ? new Date(iso).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : '—');
const day = (iso: string | null | undefined) => (iso ? new Date(iso).toLocaleDateString('id-ID', { weekday: 'short', day: 'numeric', month: 'short' }) : '—');
const ymd = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
const ICON: Record<string, string> = { TENNIS: 'sports_tennis', FUTSAL: 'sports_soccer', 'BASKET-VB': 'sports_volleyball', 'BASKET-IN': 'sports_basketball' };
const fileUrl = (token: string, file: string) => `${API_BASE}/api/v1/public/court-bookings/${token}/${file}?propertyId=${getActiveProperty()}`;

function useSportPage() {
  return useGet<SportPage>(`/api/v1/public/sport-club${qs({ propertyId: getActiveProperty() })}`);
}

// ── Golf | Sport Club switch (FR-109) ────────────────────────────────────────

/** The Member App domain of each program (surface.json), set at start-up. */
let programDomains: Record<string, string> = {};
export function setProgramDomains(d: Record<string, string> | undefined) {
  programDomains = d ?? {};
}

/** Opens the Member App of a program: its own domain, or (one domain, local) the remembered choice. */
export function openProgram(p: string) {
  const url = programDomains[p];
  if (url && !url.endsWith('://') && new URL(url).host !== window.location.host) {
    window.location.assign(url);
    return;
  }
  try { localStorage.setItem('oneclub.member.program', p); } catch { /* private window */ }
  window.location.assign('/');
}

/**
 * Golf and Sport Club are two Member Apps (FR-108..112): a member with both
 * memberships switches between them; a member of the other program only is
 * pointed to that app.
 */
export function ProgramSwitch() {
  const nav = useNavigation('member');
  const programs = nav.data?.programs ?? [];
  const cur = nav.data?.program ?? getMemberProgram();
  if (programs.length === 1 && programs[0] !== cur) {
    const other = programs[0] === 'sport_club' ? 'Sport Club' : 'Golf';
    return (
      <div className="mj-card oc-row-wrap">
        <span className="mj-icon"><Icon name="swap_horiz" size={20} /></span>
        <span style={{ flex: 1 }}><strong>Membership Anda: {other}</strong><br /><span className="mj-small mj-muted">Menu {other} ada di Member App {other}.</span></span>
        <button type="button" className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => openProgram(programs[0])}>Buka {other}</button>
      </div>
    );
  }
  if (programs.length < 2) return null;
  const pick = (p: string) => p !== cur && openProgram(p);
  return (
    <div className="mj-seg" role="group" aria-label="Membership">
      <button type="button" aria-pressed={cur === 'golf'} onClick={() => pick('golf')}>Golf</button>
      <button type="button" aria-pressed={cur === 'sport_club'} onClick={() => pick('sport_club')}>Sport Club</button>
    </div>
  );
}

// ── Home of a Sport Club member (§8.4) ──────────────────────────────────────

interface Summary { nextBooking: CourtBooking | null; nextClass: { id: string; sessionId: string } | null; packages: Row[]; membership: string | null; status: string | null }

export function SportHome() {
  const { me } = useAuth();
  const boot = useBootstrap();
  const s = useGet<Summary>('/api/v1/member/sport-club/summary', { retry: false });
  const nb = s.data?.nextBooking;
  const h = new Date().getHours();
  return (
    <div className="mj-page">
      <section className="mj-hero" aria-label="Sport Club">
        <div className="mj-muted">{boot.branding.appName} · Sport Club</div>
        <h1>{h < 11 ? 'Selamat pagi' : h < 15 ? 'Selamat siang' : h < 18 ? 'Selamat sore' : 'Selamat malam'}, {(me?.fullName ?? '').split(' ')[0]}</h1>
      </section>
      <ProgramSwitch />
      {s.data && (
        <div className="mj-card oc-row-wrap" style={{ marginBottom: 12 }}>
          <span className="mj-icon"><Icon name="sports_tennis" size={20} /></span>
          <span style={{ flex: 1 }}><strong>{s.data.membership ?? 'Sport Club (non-member)'}</strong><br />
            <span className="mj-small mj-muted">{s.data.status === 'active' ? 'Membership Sport Club aktif' : s.data.status ? `Membership ${s.data.status} — perpanjang untuk hak member` : 'Harga lapangan sama untuk member dan tamu'}</span></span>
          {!s.data.membership && <Link className="oc-btn oc-btn-primary oc-btn-sm" to="/join">Upgrade to Member</Link>}
        </div>
      )}
      <section>
        <h2 className="mj-section-title">Booking lapangan berikutnya <Link to="/activity/courts">Semua</Link></h2>
        {s.isLoading ? <div className="mj-card"><Skeleton rows={3} /></div> : nb ? <BookingCard b={nb} /> : (
          <div className="mj-card oc-row-wrap">
            <span className="mj-icon"><Icon name="event_available" size={20} /></span>
            <span style={{ flex: 1 }}><strong>Belum ada booking lapangan</strong><br /><span className="mj-small mj-muted">Pilih cabor, jam dan bayar — langsung dapat QR check-in.</span></span>
            <Link className="oc-btn oc-btn-primary" to="/sport-club/courts">Pesan Lapangan</Link>
          </div>
        )}
      </section>
      {s.data?.nextClass && (
        <Link className="mj-card oc-row-wrap" style={{ marginTop: 12, textDecoration: 'none', color: 'inherit' }} to="/sport-club/classes">
          <span className="mj-icon"><Icon name="school" size={20} /></span><span style={{ flex: 1 }}><strong>Sesi kelas berikutnya sudah dibooking</strong><br />
            <span className="mj-small mj-muted">Lihat jadwal di menu Kelas</span></span></Link>
      )}
      {(s.data?.packages ?? []).length > 0 && (
        <section style={{ marginTop: 12 }}>
          <h2 className="mj-section-title">Sisa kuota paket <Link to="/membership/packages">Detail</Link></h2>
          <div className="mj-list">{(s.data?.packages ?? []).map((p) => (
            <div key={String(p.id)} className="mj-item"><span className="mj-icon"><Icon name="card_membership" size={20} /></span>
              <div className="mj-item-body"><strong>{String(p.typeName)}</strong><span className="mj-small mj-muted">{String(p.code)}</span></div>
              <span className="mj-num">{Number(p.remaining)}x</span></div>
          ))}</div>
        </section>
      )}
      <section style={{ marginTop: 12 }}>
        <h2 className="mj-section-title">Quick Actions</h2>
        <div className="mj-quick">
          <Link to="/sport-club/courts"><span className="mj-icon"><Icon name="sports_tennis" size={22} /></span>Pesan Lapangan</Link>
          <Link to="/sport-club/classes"><span className="mj-icon"><Icon name="school" size={22} /></span>Kelas</Link>
          <Link to="/sport-club/access"><span className="mj-icon"><Icon name="qr_code_2" size={22} /></span>Kartu Masuk</Link>
          <Link to="/membership/sport-guests"><span className="mj-icon"><Icon name="group_add" size={22} /></span>Ajak Tamu</Link>
        </div>
      </section>
    </div>
  );
}

function BookingCard({ b, qr }: { b: CourtBooking; qr?: boolean }) {
  return (
    <div className="mj-card oc-stack" style={{ gap: 10 }}>
      <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
        <strong style={{ flex: 1 }}>{b.code}</strong><StateChip state={b.state} />
        {b.payStatus !== 'paid' && b.payStatus !== 'overpaid' && <Chip tone="warn">Sisa {money(b.balance)}</Chip>}
      </div>
      {b.lines.filter((l) => l.state !== 'void' && l.state !== 'expired').map((l) => (
        <div key={l.id} className="mj-small"><strong>{l.facilityName} · {l.courtName}</strong> — {day(l.start)} {hm(l.start)}–{hm(l.end)}</div>
      ))}
      {qr && b.token && ['scheduled', 'late', 'playing'].includes(b.state) && (
        <div className="oc-stack" style={{ alignItems: 'center' }}><QRCode value={b.token} size={180} label="QR check-in" />
          <span className="mj-small mj-muted">Scan di front desk Sport Club. Datang 15 menit sebelum jam main.</span></div>
      )}
      {b.token && (
        <div className="oc-row-wrap">
          <a className="oc-btn oc-btn-neutral oc-btn-sm" href={fileUrl(b.token, 'e-ticket.pdf')} target="_blank" rel="noreferrer"><Icon name="download" size={16} /> E-ticket</a>
          <a className="oc-btn oc-btn-neutral oc-btn-sm" href={fileUrl(b.token, 'calendar.ics')}><Icon name="event" size={16} /> Kalender</a>
          {!qr && <Link className="oc-btn oc-btn-neutral oc-btn-sm" to={`/activity/courts?open=${b.id}`}>QR</Link>}
        </div>
      )}
    </div>
  );
}

// ── Pesan Lapangan (FR-113) ─────────────────────────────────────────────────

const CART = 'oneclub.member.courtCart';
function readCart(): Pick[] {
  try { return JSON.parse(localStorage.getItem(CART) ?? '[]') as Pick[]; } catch { return []; }
}

export function SportCourtsPage() {
  const page = useSportPage();
  const toast = useToast();
  const nav = useNavigate();
  const [step, setStep] = useState(0);
  const [sport, setSport] = useState('');
  const sports = (page.data?.facilities ?? []).filter((f) => f.usageMode === 'slot_booking' && f.courts > 0).sort((a, b) => a.sortOrder - b.sortOrder);
  const sp = sports.find((f) => f.id === sport) ?? (sports.length === 1 ? sports[0] : undefined);
  const [date, setDate] = useState(ymd(new Date()));
  const [surface, setSurface] = useState('');
  const [open, setOpen] = useState('');
  const [cart, setCartState] = useState<Pick[]>(readCart);
  const setCart = (c: Pick[]) => { setCartState(c); try { localStorage.setItem(CART, JSON.stringify(c)); } catch { /* ignore */ } };
  const [method, setMethod] = useState('member');
  const [promo, setPromo] = useState('');
  const [pkg, setPkg] = useState('');
  const [agree, setAgree] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [result, setResult] = useState<{ booking: CourtBooking; payment: Schemas['Payment'] | null } | null>(null);
  const grid = useGet<Grid>(sp ? `/api/v1/public/sport-club/grid${qs({ propertyId: getActiveProperty(), facility: sp.id, date })}` : null, { refetchInterval: 30_000 });
  const packages = useGet<Page<Row>>('/api/v1/member/sport-club/packages', { retry: false });
  const [quote, setQuote] = useState<Row | null>(null);
  const [qerr, setQerr] = useState<unknown>(null);
  const online = method !== 'member' && method !== 'package';
  const qkey = JSON.stringify([cart.map((c) => [c.courtId, c.start]), promo, online ? method : '']);
  useEffect(() => {
    if (!cart.length) { setQuote(null); return undefined; }
    let live = true;
    request<Row>('POST', '/api/v1/member/sport-club/quote', { lines: cart.map((c) => ({ courtId: c.courtId, start: c.start, end: c.end })), promoCode: promo || undefined,
      method: online ? method : undefined }).then((q) => { if (live) { setQuote(q); setQerr(null); } }, (e) => { if (live) { setQuote(null); setQerr(e); } });
    return () => { live = false; };
  }, [qkey]);
  const taken = (p: Pick) => (grid.data?.courts ?? []).some((c) => c.court.id === p.courtId && c.slots.some((s) => s.start === p.start && s.status !== 'available'));
  const toggle = (c: { id: string; name: string }, s: Slot) => {
    const has = cart.some((p) => p.courtId === c.id && p.start === s.start);
    setCart(has ? cart.filter((p) => !(p.courtId === c.id && p.start === s.start))
      : [...cart, { courtId: c.id, courtName: c.name, start: s.start, end: s.end, price: Number(s.price ?? 0) }].sort((a, b) => a.start.localeCompare(b.start)));
  };
  const courtPkgs = (packages.data?.items ?? []).filter((p) => p.category === 'court_package');
  const pay = async () => {
    setBusy(true);
    setError(null);
    try {
      const body: Row = { lines: cart.map((c) => ({ courtId: c.courtId, start: c.start, end: c.end })), promoCode: promo || undefined };
      if (method === 'member') body.memberCharge = true;
      else if (method === 'package') body.packageCode = pkg;
      else body.payMethod = method;
      const r = await request<{ booking: CourtBooking; checkout: { online: Schemas['Payment'] | null } | null }>('POST', '/api/v1/member/sport-club/court-bookings', body,
        { 'Idempotency-Key': uuidv7() });
      setCart([]);
      setResult({ booking: r.booking, payment: r.checkout?.online ?? null });
      setStep(r.checkout?.online && r.checkout.online.status === 'pending' ? 3 : 4);
    } catch (e) {
      setError(e); // the cart stays (FR-138)
    } finally {
      setBusy(false);
    }
  };
  const reload = useCallback(async () => {
    if (!result) return;
    const list = await request<Page<CourtBooking>>('GET', '/api/v1/member/sport-club/court-bookings');
    const b = list.items.find((x) => x.id === result.booking.id);
    if (b) setResult({ ...result, booking: b });
  }, [result]);
  if (page.isLoading) return <div className="mj-page"><Skeleton rows={6} /></div>;
  const courts = (grid.data?.courts ?? []).filter((c) => !surface || (c.court.surface ?? '') === surface);
  const surfaces = Array.from(new Set((grid.data?.courts ?? []).map((c) => c.court.surface ?? '').filter(Boolean)));
  return (
    <div className="mj-page">
      <Head title="Pesan Lapangan" help="Pilih cabor, jam yang kosong, lalu bayar. Booking tidak dapat dibatalkan dan tidak ada refund." actions={step > 0 && step < 3 ? <button type="button" className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setStep(step - 1)}>Kembali</button> : undefined} />
      <Steps steps={['Cabor', 'Jadwal', 'Checkout', 'Pembayaran', 'Selesai']} current={step} />
      {step === 0 && (
        <div className="mj-grid-2">
          {sports.map((f) => (
            <button key={f.id} type="button" className="mj-card oc-stack" style={{ textAlign: 'left', gap: 6, cursor: 'pointer' }} onClick={() => { setSport(f.id); setOpen(''); setStep(1); }}>
              <span className="mj-icon"><Icon name={f.content?.icon ?? ICON[f.code] ?? 'sports'} size={22} /></span>
              <strong>{f.name}</strong>
              <span className="mj-small mj-muted">{f.courts} lapangan · {f.indoor ? 'Indoor' : 'Outdoor'}{f.fromPrice ? ` · mulai ${money(f.fromPrice)}/jam` : ''}</span>
            </button>
          ))}
          {sports.length === 0 && <div className="mj-card">Belum ada lapangan yang bisa dibooking online.</div>}
        </div>
      )}
      {step === 1 && sp && (
        <div className="oc-stack">
          <div className="mj-card oc-stack" style={{ gap: 6 }}>
            <strong>{sp.name}</strong>
            {sp.content?.description?.id && <span className="mj-small">{sp.content.description.id}</span>}
            {(sp.content?.rules?.id ?? []).length > 0 && <span className="mj-small mj-muted">Aturan: {(sp.content.rules?.id ?? []).join(' · ')}</span>}
          </div>
          <DateStrip value={date} onChange={setDate} days={Math.min(page.data?.windowDays ?? 30, 60)} />
          {surfaces.length > 1 && (
            <div className="mj-seg" role="group" aria-label="Jenis lapangan">
              <button type="button" aria-pressed={!surface} onClick={() => setSurface('')}>Semua jenis</button>
              {surfaces.map((x) => <button key={x} type="button" aria-pressed={surface === x} onClick={() => setSurface(x)}>{x}</button>)}
            </div>
          )}
          {grid.error ? <div className="mj-card"><ErrorAlert error={grid.error} /><button className="oc-btn oc-btn-neutral" onClick={() => void grid.refetch()}>Muat ulang</button></div>
            : grid.isLoading ? <Skeleton rows={4} /> : courts.map((c, i) => {
              const isOpen = open ? open === c.court.id : i === 0;
              return (
                <div key={c.court.id} className="mj-card oc-stack" style={{ gap: 8 }}>
                  <button type="button" className="oc-row" style={{ border: 0, background: 'none', padding: 0, font: 'inherit', color: 'inherit', textAlign: 'left' }}
                    aria-expanded={isOpen} onClick={() => setOpen(isOpen ? '-' : c.court.id)}>
                    <strong style={{ flex: 1 }}>{c.court.name}<br /><span className="mj-small mj-muted">{c.court.surface ?? ''} · {c.court.indoor ? 'Indoor' : 'Outdoor'}</span></strong>
                    <Chip tone={c.free ? 'ok' : 'bad'}>{c.free} jadwal tersedia</Chip>
                  </button>
                  {isOpen && (c.slots.length === 0 ? <span className="mj-small mj-muted">Tidak ada jadwal tanggal ini — coba tanggal lain.</span> : (
                    <div className="mj-slots">
                      {c.slots.map((s) => {
                        const chosen = cart.some((p) => p.courtId === c.court.id && p.start === s.start);
                        return (
                          <button key={s.start} type="button" className="mj-slot" data-full={s.status !== 'available' && !chosen} disabled={s.status !== 'available' && !chosen}
                            aria-pressed={chosen} onClick={() => toggle(c.court, s)}>
                            <small>60 menit</small><strong>{hm(s.start)}–{hm(s.end)}</strong>
                            <span>{chosen ? 'Dipilih' : s.status === 'available' ? <>{s.listPrice && <s>{money(s.listPrice)}</s>} {money(s.price)}</> : s.status === 'booked' ? 'Booked' : s.status === 'past' ? 'Lewat' : 'Tutup'}</span>
                          </button>
                        );
                      })}
                    </div>
                  ))}
                </div>
              );
            })}
          <span className="mj-small mj-muted">Harga per jam {page.data?.taxIncluded ? 'sudah' : 'belum'} termasuk pajak, sama untuk member dan tamu.</span>
          <div className="mj-sheet">
            <span><strong>{cart.length} jadwal</strong>{quote ? ` · ${money(quote.total)}` : ''}</span>
            {cart.some(taken) && <span className="mj-small" style={{ color: 'var(--mj-bad, #dc2626)' }}>Ada jadwal yang baru saja terisi — hapus dari keranjang.</span>}
            <button className="oc-btn oc-btn-primary" disabled={!cart.length || cart.some(taken)} onClick={() => setStep(2)}>Selanjutnya</button>
          </div>
        </div>
      )}
      {step === 2 && (
        <div className="oc-stack">
          <div className="mj-card oc-stack" style={{ gap: 6 }}>
            <strong>Jadwal booking</strong>
            {cart.map((p) => (
              <div key={p.courtId + p.start} className="oc-row" style={{ alignItems: 'center' }}>
                <span style={{ flex: 1 }} className="mj-small">{p.courtName} · {day(p.start)} {hm(p.start)}–{hm(p.end)}</span>
                <button type="button" className="oc-btn oc-btn-neutral oc-btn-sm" aria-label="Hapus" onClick={() => setCart(cart.filter((x) => x !== p))}>✕</button>
              </div>
            ))}
            <button type="button" className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setStep(1)}>Tambah jadwal</button>
          </div>
          <div className="mj-card oc-stack" style={{ gap: 6 }}>
            <strong>Metode pembayaran</strong>
            <div className="mj-choices" role="radiogroup">
              {[{ code: 'member', label: 'Member Charge', help: 'Ditagihkan ke rekening member (sesuai limit)' },
                ...(courtPkgs.length ? [{ code: 'package', label: 'Paket lapangan', help: 'Potong kuota paket 4x/8x' }] : []),
                ...(page.data?.methods ?? []).map((m) => ({ code: m.code, label: m.label, help: `Biaya layanan ${Number(m.percent) ? `${m.percent}%` : money(m.fee)}` }))].map((m) => (
                <button key={m.code} type="button" className="mj-choice" role="radio" aria-checked={method === m.code} aria-pressed={method === m.code} onClick={() => setMethod(m.code)}>
                  <span><strong>{m.label}</strong><small>{m.help}</small></span>
                </button>
              ))}
            </div>
            {method === 'package' && (
              <select className="oc-input" value={pkg} onChange={(e) => setPkg(e.target.value)} aria-label="Paket">
                <option value="">Pilih paket</option>
                {courtPkgs.map((p) => <option key={String(p.id)} value={String(p.code)}>{String(p.typeName)} · sisa {Number(p.remaining)}x</option>)}
              </select>
            )}
            {method !== 'package' && <input className="oc-input" placeholder="Kode promo / voucher" value={promo} onChange={(e) => setPromo(e.target.value)} />}
            {quote?.promoError && promo ? <span className="mj-small" style={{ color: 'var(--mj-bad, #dc2626)' }}>Kode tidak berlaku: {String(quote.promoError)}</span> : null}
            {quote?.voucher && promo ? <span className="mj-small mj-muted">Saldo voucher dipakai saat pembayaran.</span> : null}
          </div>
          {quote && method !== 'package' && (
            <div className="mj-card">
              <Rows rows={[['Biaya sewa', money(quote.rent)], ...(Number(quote.discount) > 0 ? [['Diskon', `−${money(quote.discount)}`] as [string, string]] : []),
                ['Pajak', money(quote.tax)], ...(online ? [['Biaya layanan', money(quote.serviceFee)] as [string, string]] : []), [<strong key="t">Total bayar</strong>, <strong key="v">{money(quote.total)}</strong>]]} />
            </div>
          )}
          <ErrorAlert error={qerr ?? error} />
          <label className="mj-small oc-row" style={{ alignItems: 'flex-start', gap: 8 }}>
            <input type="checkbox" checked={agree} onChange={(e) => setAgree(e.target.checked)} />
            <span>{page.data?.terms?.id ?? 'Booking tidak dapat dibatalkan dan tidak ada refund.'}</span>
          </label>
          <button className="oc-btn oc-btn-primary" disabled={busy || !agree || !cart.length || (method === 'package' && !pkg)} onClick={() => void pay()}>
            {busy ? '…' : method === 'package' ? 'Booking dengan paket' : `Bayar Sekarang${quote ? ` · ${money(quote.total)}` : ''}`}</button>
        </div>
      )}
      {step === 3 && result?.payment && (
        <div className="mj-card oc-stack" style={{ alignItems: 'center' }}>
          <span className="mj-small mj-muted">Selesaikan pembayaran dalam {Math.max(1, Math.ceil((result.booking.holdSeconds || 900) / 60))} menit; slot ditahan sampai itu.</span>
          <PaymentPanel payment={result.payment} onPaid={() => { toast('Pembayaran berhasil'); void reload().then(() => setStep(4)); }}
            onFailed={() => { toast('Pembayaran gagal — slot dilepas, silakan ulangi', 'error'); setStep(1); }} />
        </div>
      )}
      {step === 4 && result && (
        <div className="oc-stack">
          <div className="mj-card"><strong>Booking berhasil</strong><div className="mj-small mj-muted">Tunjukkan QR di front desk Sport Club.</div></div>
          <BookingCard b={result.booking} qr />
          <button className="oc-btn oc-btn-neutral" onClick={() => nav('/activity/courts')}>Lihat booking saya</button>
        </div>
      )}
    </div>
  );
}

// ── My Activity › Booking Lapangan ───────────────────────────────────────────

export function MyCourtsPage() {
  const list = useGet<Page<CourtBooking>>('/api/v1/member/sport-club/court-bookings');
  const [tab, setTab] = useState<'upcoming' | 'history'>('upcoming');
  const now = Date.now();
  const rows = (list.data?.items ?? []).filter((b) => (tab === 'upcoming' ? b.end && new Date(b.end).getTime() > now && !['expired', 'void'].includes(b.state)
    : !(b.end && new Date(b.end).getTime() > now) || ['expired', 'void'].includes(b.state)));
  const openId = new URLSearchParams(window.location.search).get('open');
  return (
    <div className="mj-page">
      <Head title="Booking Lapangan" help="QR check-in, e-ticket dan riwayat. Tidak ada pembatalan atau refund; perubahan jadwal hanya oleh petugas klub." />
      <div className="mj-seg" role="group" aria-label="Booking">
        <button type="button" aria-pressed={tab === 'upcoming'} onClick={() => setTab('upcoming')}>Akan datang</button>
        <button type="button" aria-pressed={tab === 'history'} onClick={() => setTab('history')}>Riwayat</button>
      </div>
      {list.isLoading ? <Skeleton rows={4} /> : rows.length === 0 ? (
        <div className="mj-card oc-row-wrap"><span style={{ flex: 1 }}>Belum ada booking.</span><Link className="oc-btn oc-btn-primary" to="/sport-club/courts">Pesan Lapangan</Link></div>
      ) : rows.map((b) => <BookingCard key={b.id} b={b} qr={tab === 'upcoming' && (rows.length === 1 || openId === b.id)} />)}
    </div>
  );
}

// ── Kartu Masuk (FR-116), Ajak Tamu (FR-117), visits, packages ──────────────

export function SportAccessPage() {
  const m = useGet<{ profile: Row; card?: Row; clubName: string }>('/api/v1/member/membership', { retry: false });
  const entries = useGet<Page<Row>>('/api/v1/member/sport-club/entries?limit=20', { retry: false });
  return (
    <div className="mj-page">
      <Head title="Kartu Masuk" help="Tunjukkan QR Digital Member Card di front desk Sport Club untuk masuk kolam, gym dan aerobic studio sesuai hak membership." />
      <div className="mj-card oc-stack" style={{ alignItems: 'center' }}>
        {m.isLoading ? <Skeleton rows={4} /> : m.data?.card ? <MemberCard name={String(m.data.profile.name ?? '')} club={m.data.clubName} card={m.data.card as never} />
          : <span>Kartu member belum tersedia. <Link to="/join">Upgrade to Member</Link></span>}
      </div>
      <div className="mj-card"><strong>Bawa tamu?</strong><div className="mj-small mj-muted">Beli tiket Guest With Member untuk tamu Anda.</div>
        <Link className="oc-btn oc-btn-primary oc-btn-sm" to="/membership/sport-guests" style={{ marginTop: 8 }}>Ajak Tamu</Link></div>
      <h2 className="mj-section-title">Kunjungan terakhir</h2>
      <VisitList rows={entries.data?.items ?? []} />
    </div>
  );
}

function VisitList({ rows }: { rows: Row[] }) {
  if (!rows.length) return <div className="mj-card mj-small mj-muted">Belum ada kunjungan.</div>;
  return (
    <div className="mj-list">{rows.map((e) => (
      <div key={String(e.id)} className="mj-item"><span className="mj-icon"><Icon name="pool" size={20} /></span>
        <div className="mj-item-body"><strong>{String(e.facilityName)}</strong><span className="mj-small mj-muted">{String(e.ticketNo)} · {day(String(e.visitDate))} · {String(e.entryType).replace(/_/g, ' ')}</span></div>
        <Chip tone={e.status === 'used' ? 'ok' : 'info'}>{e.status === 'used' ? 'Masuk' : String(e.status)}</Chip></div>
    ))}</div>
  );
}

export function MyVisitsPage() {
  const entries = useGet<Page<Row>>('/api/v1/member/sport-club/entries?limit=100', { retry: false });
  return <div className="mj-page"><Head title="Kunjungan Kolam/Gym" /><VisitList rows={entries.data?.items ?? []} /></div>;
}

export function SportGuestsPage() {
  const toast = useToast();
  const page = useSportPage();
  const entries = useGet<Page<Row>>('/api/v1/member/sport-club/entries?limit=50', { retry: false });
  const [f, setF] = useState({ guestName: '', guestPhone: '', visitDate: ymd(new Date()), method: 'member' });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [res, setRes] = useState<{ token: string; checkout: { online: Schemas['Payment'] | null } | null } | null>(null);
  const web = `${window.location.protocol}//${window.location.host.replace(/^(sport)?member\./, '')}`;
  const share = (t: string) => `${web}/id/sport-club/ticket/${t}?p=${getActiveProperty()}`;
  const buy = async () => {
    setBusy(true);
    setError(null);
    try {
      const r = await request<{ token: string; checkout: { online: Schemas['Payment'] | null } | null }>('POST', '/api/v1/member/sport-club/guest-tickets', {
        guestName: f.guestName, guestPhone: f.guestPhone || undefined, visitDate: f.visitDate, memberCharge: f.method === 'member',
        payMethod: f.method === 'member' ? undefined : f.method }, { 'Idempotency-Key': uuidv7() });
      setRes(r);
      void entries.refetch();
      toast('Tiket tamu dibuat');
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const guests = (entries.data?.items ?? []).filter((e) => e.entryType === 'guest_of_member');
  return (
    <div className="mj-page">
      <Head title="Ajak Tamu" help="Tiket Guest With Member: Rp145.000 hari kerja / Rp210.000 akhir pekan (sesuai brosur). Tamu masuk bersama Anda; bagikan tautan QR ke tamu." />
      <div className="mj-card oc-stack">
        <input className="oc-input" placeholder="Nama tamu" value={f.guestName} onChange={(e) => setF({ ...f, guestName: e.target.value })} />
        <input className="oc-input" placeholder="No. HP tamu (opsional)" value={f.guestPhone} onChange={(e) => setF({ ...f, guestPhone: e.target.value })} />
        <input className="oc-input" type="date" value={f.visitDate} min={ymd(new Date())} onChange={(e) => setF({ ...f, visitDate: e.target.value })} />
        <select className="oc-input" value={f.method} onChange={(e) => setF({ ...f, method: e.target.value })} aria-label="Pembayaran">
          <option value="member">Member Charge</option>
          {(page.data?.methods ?? []).map((m) => <option key={m.code} value={m.code}>{m.label}</option>)}
        </select>
        <ErrorAlert error={error} />
        <button className="oc-btn oc-btn-primary" disabled={busy || !f.guestName.trim()} onClick={() => void buy()}>Beli tiket tamu</button>
      </div>
      {res?.checkout?.online && res.checkout.online.status === 'pending' && <div className="mj-card"><PaymentPanel payment={res.checkout.online} /></div>}
      {res && (
        <div className="mj-card oc-stack" style={{ alignItems: 'center' }}>
          <QRCode value={res.token} size={180} label="QR tamu" />
          <span className="mj-small" style={{ wordBreak: 'break-all' }}>{share(res.token)}</span>
          <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => void navigator.clipboard?.writeText(share(res.token)).then(() => toast('Tautan disalin'))}>Salin tautan</button>
        </div>
      )}
      <h2 className="mj-section-title">Tiket tamu saya</h2>
      {guests.length === 0 ? <div className="mj-card mj-small mj-muted">Belum ada.</div> : (
        <div className="mj-list">{guests.map((e) => (
          <div key={String(e.id)} className="mj-item"><div className="mj-item-body"><strong>{String(e.guestName ?? 'Tamu')}</strong>
            <span className="mj-small mj-muted">{day(String(e.visitDate))} · {String(e.ticketNo)}</span></div>
            <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => void navigator.clipboard?.writeText(share(String(e.qrToken))).then(() => toast('Tautan disalin'))}>Bagikan</button></div>
        ))}</div>
      )}
    </div>
  );
}

export function SportPackagesPage() {
  const toast = useToast();
  const mine = useGet<Page<Row>>('/api/v1/member/sport-club/packages', { retry: false });
  const types = useGet<Page<Row>>('/api/v1/member/sport-club/package-types', { retry: false });
  const [error, setError] = useState<unknown>(null);
  const buy = async (id: string) => {
    setError(null);
    try {
      await request('POST', '/api/v1/member/vouchers:buy', { voucherTypeId: id, memberCharge: true }, { 'Idempotency-Key': uuidv7() });
      toast('Paket dibeli — ditagihkan ke rekening member');
      void mine.refetch();
    } catch (e) {
      setError(e);
    }
  };
  return (
    <div className="mj-page">
      <Head title="Paket & Voucher" help="Sisa kuota paket lapangan dan kelas. Paket berlaku untuk jenis hari dan jam yang sama (sesuai brosur)." />
      {(mine.data?.items ?? []).length === 0 ? <div className="mj-card mj-small mj-muted">Belum ada paket aktif.</div> : (
        <div className="mj-list">{(mine.data?.items ?? []).map((p) => (
          <div key={String(p.id)} className="mj-item"><span className="mj-icon"><Icon name="card_membership" size={20} /></span>
            <div className="mj-item-body"><strong>{String(p.typeName)}</strong><span className="mj-small mj-muted">{String(p.code)}{p.expiresAt ? ` · s.d. ${day(String(p.expiresAt))}` : ''}</span></div>
            <span className="mj-num">{Number(p.remaining)} / {Number(p.original)}</span></div>
        ))}</div>
      )}
      <h2 className="mj-section-title">Beli paket</h2>
      <ErrorAlert error={error} />
      <div className="mj-list">{(types.data?.items ?? []).map((t) => (
        <div key={String(t.id)} className="mj-item"><div className="mj-item-body"><strong>{String(t.name)}</strong><span className="mj-small mj-muted">{Number(t.uses)}x · {money(t.price)}</span></div>
          <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => void buy(String(t.id))}>Beli</button></div>
      ))}</div>
    </div>
  );
}

// ── demo access of the Sport Club Member App (sportmember domain) ───────────

const PERSONAS: [string, string, string, string][] = [
  ['sport.futsal@demo.oneclub.id', 'Futsal', 'sports_soccer', 'Rizal — paket futsal 4x, booking malam'],
  ['sport.tennis@demo.oneclub.id', 'Tennis', 'sports_tennis', 'Sinta — member residence, main pagi'],
  ['sport.basket@demo.oneclub.id', 'Basket & Volley', 'sports_volleyball', 'Kevin — member student'],
  ['sport.family@demo.oneclub.id', 'Basket Indoor', 'sports_basketball', 'Budi — member keluarga'],
  ['sport.swim@demo.oneclub.id', 'Kolam & Gym', 'pool', 'Laras — kolam, gym, kelas renang'],
  ['member@demo.oneclub.id', 'Golf + Sport Club', 'swap_horiz', 'Hendra — dua membership (pemilih Golf | Sport Club)'],
  ['sport.guest@demo.oneclub.id', 'Tamu (non-member)', 'person', 'Andi — tanpa membership, harga sama'],
];

export function MemberDemoPage() {
  const demo = useGet<{ accounts: { email: string; name: string }[] }>('/api/v1/public/demo-access', { retry: false });
  const sport = getMemberProgram() === 'sport_club';
  const have = new Set((demo.data?.accounts ?? []).map((a) => a.email));
  const list = sport ? PERSONAS : [['member@demo.oneclub.id', 'Golf', 'golf_course', 'Hendra — member golf'] as [string, string, string, string]];
  if (demo.error) return <div className="mj-page"><Head title="Demo" help="Halaman demo tidak tersedia di instance ini." /></div>;
  return (
    <div className="mj-page">
      <Head title={sport ? 'Demo Sport Club Member App' : 'Demo Member App'} help="Pilih member, lalu masuk dengan e-mail yang sudah terisi (kata sandi demo)." />
      {demo.isLoading ? <Skeleton rows={4} /> : (
        <div className="mj-grid-2">
          {list.filter(([e]) => have.has(e)).map(([email, sportName, icon, who]) => (
            <Link key={email} className="mj-card oc-stack" style={{ gap: 6, textDecoration: 'none', color: 'inherit' }} to={`/login?email=${encodeURIComponent(email)}`}>
              <span className="mj-icon"><Icon name={icon} size={22} /></span><strong>{sportName}</strong>
              <span className="mj-small mj-muted">{who}</span><span className="mj-small">{email}</span>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}

// ── My Activity › Kelas & Kehadiran ──────────────────────────────────────────

const SESSION: Record<string, [string, 'ok' | 'warn' | 'bad' | 'info' | undefined]> = {
  booked: ['Dibooking', 'info'], present: ['Hadir', 'ok'], absent: ['Tidak hadir', 'bad'], excused: ['Izin', 'warn'], cancelled: ['Batal', 'bad'],
};

export function MyClassesPage() {
  const classes = useGet<Page<Row>>('/api/v1/member/sport-club/classes', { retry: false });
  const sessions = useGet<Page<Row>>('/api/v1/member/sport-club/sessions', { retry: false });
  const list = sessions.data?.items ?? [];
  return (
    <div className="mj-page">
      <Head title="Kelas & Kehadiran" help="Kelas yang Anda ikuti dan kehadiran tiap sesi (kuota paket terpotong saat hadir)." actions={<Link className="oc-btn oc-btn-primary oc-btn-sm" to="/sport-club/classes">Booking sesi</Link>} />
      <h2 className="mj-section-title">Kelas saya</h2>
      {(classes.data?.items ?? []).length === 0 ? <div className="mj-card mj-small mj-muted">Belum terdaftar di kelas. <Link to="/sport-club/classes">Daftar kelas</Link></div> : (
        <div className="mj-list">{(classes.data?.items ?? []).map((c) => (
          <div key={String(c.id)} className="mj-item"><span className="mj-icon"><Icon name="school" size={20} /></span>
            <div className="mj-item-body"><strong>{String(c.programName)}</strong><span className="mj-small mj-muted">{c.validUntil ? `s.d. ${day(String(c.validUntil))}` : ''}</span></div>
            <Chip tone={c.status === 'active' ? 'ok' : undefined}>{String(c.status)}</Chip></div>
        ))}</div>
      )}
      <h2 className="mj-section-title">Kehadiran</h2>
      {list.length === 0 ? <div className="mj-card mj-small mj-muted">Belum ada sesi.</div> : (
        <div className="mj-list">{list.map((b) => {
          const [l, t] = SESSION[String(b.status)] ?? [String(b.status), undefined];
          return (
            <div key={String(b.id)} className="mj-item"><div className="mj-item-body"><strong>Sesi {String(b.sessionId).slice(-6)}</strong>
              <span className="mj-small mj-muted">{b.quotaUsed ? 'Kuota paket terpakai' : 'Tanpa kuota'}{b.markedAt ? ` · ${day(String(b.markedAt))}` : ''}</span></div>
              <Chip tone={t}>{l}</Chip></div>
          );
        })}</div>
      )}
    </div>
  );
}
