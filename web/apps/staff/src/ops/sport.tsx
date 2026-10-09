import React, { useEffect, useMemo, useState } from 'react';
import { getActiveProperty, qs, request, useGet, useSend, uuidv7, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import { enqueue } from '@oneclub/offline';
import { Card, DataTable, ErrorAlert, Icon, MoneyField, SelectField, StatusPill, TextField, useAuth, useToast } from '@oneclub/shell';
import { DeskPayDialog, type DeskTender } from './deskpay';
import { Btn, Head, money, today } from './golf';

/*
 * Sport Reception (demo feedback 10 Oct 2026 #39): one desk for the whole
 * Sport Club in the blue POS look, like the golf Front Desk — sell the entry
 * ticket (walk-in, guest of a member, child, family, voucher), book courts
 * (several hours and courts, or a 4x/8x package), register for a class and
 * book its session, sell packages and vouchers, lockers, and the access
 * scan with the live occupancy. Every payment goes through the payment
 * page (method, confirm, receipt).
 */

type R = Record<string, unknown> & { id: string };
type Who = { customerId?: string; name: string; phone: string };
const idem = () => ({ 'Idempotency-Key': uuidv7() });
const SCAN_KEY = 'oneclub.sport.scanFacility';

const TABS = [['entry', 'Entry Ticket', 'confirmation_number'], ['courts', 'Court Booking', 'sports_tennis'], ['classes', 'Classes', 'school'],
  ['packages', 'Packages & Vouchers', 'card_membership'], ['lockers', 'Lockers', 'lock'], ['access', 'Access & Occupancy', 'qr_code_scanner']] as const;
type Tab = (typeof TABS)[number][0];

export function SportReceptionPage() {
  const [tab, setTab] = useState<Tab>('entry');
  return (
    <div className="oc-stack">
      <Head title="Sport Reception" help="Entry tickets, courts, classes, packages and lockers of the Sport Club — paid on the payment page." />
      <div className="oc-row-wrap" role="tablist">
        {TABS.map(([k, l, icon]) => (
          <button key={k} type="button" role="tab" aria-selected={tab === k} className={`oc-btn ${tab === k ? 'oc-btn-ink' : 'oc-btn-neutral'}`} style={{ minHeight: 48 }}
            onClick={() => setTab(k)}><Icon name={icon} size={18} /> {l}</button>
        ))}
      </div>
      {tab === 'entry' && <EntryTab />}
      {tab === 'courts' && <CourtsTab />}
      {tab === 'classes' && <ClassesTab />}
      {tab === 'packages' && <PackagesTab />}
      {tab === 'lockers' && <LockersTab />}
      {tab === 'access' && <AccessTab />}
    </div>
  );
}

/** A member / customer found by name, member no. or phone — or a guest typed by name and phone. */
function WhoField({ value, onChange, guest = true, label = 'Customer' }: { value: Who; onChange: (w: Who) => void; guest?: boolean; label?: string }) {
  const [q, setQ] = useState('');
  const found = useGet<Page<R>>(q.trim().length >= 2 ? `/api/v1/crm/customers${qs({ q: q.trim(), limit: 8 })}` : null);
  if (value.customerId) {
    return (
      <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
        <span><Icon name="person" size={18} /> <strong>{value.name}</strong></span>
        <Btn label="Change" onClick={() => { onChange({ name: '', phone: '' }); setQ(''); }} />
      </div>
    );
  }
  return (
    <div className="oc-stack" style={{ gap: 8 }}>
      <TextField label={`${label}: search member / customer (name, member no., phone)`} value={q} onChange={setQ} />
      {(found.data?.items ?? []).length > 0 && (
        <div className="oc-row-wrap">
          {(found.data?.items ?? []).map((c) => (
            <button key={c.id} type="button" className="oc-chip" style={{ height: 40 }} onClick={() => onChange({ customerId: c.id, name: String(c.name), phone: String(c.phone ?? '') })}>
              {String(c.name)}{c.code ? ` · ${String(c.code)}` : ''}</button>
          ))}
        </div>
      )}
      {guest && (
        <div className="oc-row-wrap">
          <TextField label="Or a guest: name" value={value.name} onChange={(v) => onChange({ ...value, name: v })} />
          <TextField label="Phone" value={value.phone} onChange={(v) => onChange({ ...value, phone: v })} inputMode="tel" />
        </div>
      )}
    </div>
  );
}

/** A customer id for a guest typed at the desk (a class needs a customer). */
async function customerOf(w: Who): Promise<string> {
  if (w.customerId) return w.customerId;
  const c = await request<R>('POST', '/api/v1/crm/customers', { name: w.name, phone: w.phone || undefined, customerType: 'individual' });
  return c.id;
}

/** The price of a service from the rate card (tax included). */
function useQuote(body: Record<string, unknown> | null) {
  const key = JSON.stringify(body);
  const [price, setPrice] = useState<number | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    if (!body) { setPrice(null); return; }
    let live = true;
    setError(null);
    request<Schemas['LinePrice']>('POST', '/api/v1/commercial/pricing:resolve-line', body)
      .then((p) => { if (live) setPrice(Number(p.tax.total)); }, (e) => { if (live) { setPrice(null); setError(e); } });
    return () => { live = false; };
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  return { price, error };
}

const ENTRY_TYPES: [string, string, string][] = [['walk_in_guest', 'Walk-in guest', 'walk_in'], ['guest_of_member', 'Guest with member', 'guest_of_member'],
  ['child', 'Child (under 12)', 'child'], ['family_package', 'Family (2 adults + 3 children)', 'family'], ['voucher', '5-time voucher', ''],
  ['staying_guest', 'Staying guest', 'staying_guest']];

function EntryTab() {
  const toast = useToast();
  const facilities = useGet<Page<R>>('/api/v1/sportclub/facilities?filter[status]=active&filter[usageMode]=entry&limit=50');
  const entries = useGet<Page<R>>(`/api/v1/sportclub/entries${qs({ date: today(), limit: 100 })}`, { refetchInterval: 30_000 });
  const cancel = useSend<{ id: string; reason?: string }>('POST', (v) => `/api/v1/sportclub/entries/${v.id}:cancel`, ['/api/v1/sportclub/entries']);
  const list = (facilities.data?.items ?? []).filter((f) => f.usageMode === 'entry');
  const [facility, setFacility] = useState('');
  const fac = list.find((f) => f.id === facility) ?? list[0];
  const [type, setType] = useState('walk_in_guest');
  const [who, setWho] = useState<Who>({ name: '', phone: '' });
  const [host, setHost] = useState<Who>({ name: '', phone: '' });
  const [birth, setBirth] = useState('');
  const [voucher, setVoucher] = useState('');
  const [paying, setPaying] = useState(false);
  const seg = ENTRY_TYPES.find((x) => x[0] === type)?.[2] ?? '';
  const q = useQuote(fac && seg ? { serviceType: 'facility_entry', itemRef: String(fac.priceItem ?? fac.code), segment: seg, start: new Date().toISOString() } : null);
  const body = () => ({ facilityId: fac?.id, entryType: type, customerId: who.customerId, guest: who.customerId ? undefined : { name: who.name || 'Guest', phone: who.phone || undefined },
    hostCustomerId: type === 'guest_of_member' ? host.customerId : undefined, birthDate: type === 'child' && birth ? birth : undefined,
    adults: type === 'family_package' ? 2 : undefined, children: type === 'family_package' ? 3 : undefined, voucherCode: type === 'voucher' ? voucher : undefined, channel: 'ops' });
  const free = type === 'voucher' || q.price === 0;
  const ready = !!fac && (who.customerId || who.name) && (type !== 'guest_of_member' || host.customerId) && (type !== 'voucher' || voucher);
  const issue = async (t?: DeskTender) => {
    await request('POST', '/api/v1/sportclub/entries', { ...body(), payment: t && !free ? { methodType: t.methodType, reference: t.reference } : undefined }, idem());
    return [] as Record<string, unknown>[];
  };
  const done = () => { setPaying(false); toast('Entry ticket issued'); setWho({ name: '', phone: '' }); setVoucher(''); void entries.refetch(); };
  return (
    <>
      <Card title="Sell entry ticket" icon="confirmation_number">
        <div className="oc-stack">
          <div className="oc-row-wrap">
            {list.map((f) => <Btn key={f.id} label={String(f.name)} kind={fac?.id === f.id ? 'ink' : 'neutral'} onClick={() => setFacility(f.id)} />)}
          </div>
          <span className="oc-small oc-muted">One Sport Club ticket covers the pool, the gym and the aerobic studio for the day.</span>
          <div className="oc-row-wrap">
            {ENTRY_TYPES.map(([k, l]) => <button key={k} type="button" className="oc-chip" style={{ height: 40 }} aria-pressed={type === k} onClick={() => setType(k)}>{l}</button>)}
          </div>
          <WhoField value={who} onChange={setWho} label={type === 'family_package' ? 'Family' : 'Visitor'} />
          {type === 'guest_of_member' && <WhoField value={host} onChange={setHost} guest={false} label="Member who brings the guest" />}
          {type === 'child' && !who.customerId && <TextField label="Child's date of birth" type="date" value={birth} onChange={setBirth} />}
          {type === 'voucher' && <TextField label="Voucher code" value={voucher} onChange={setVoucher} />}
          <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
            <strong style={{ fontSize: 22 }}>{type === 'voucher' ? 'Voucher' : q.price == null ? '—' : money(q.price)}</strong>
            <Btn label={free ? 'Issue ticket' : 'Pay & issue'} kind="primary" disabled={!ready || (!free && q.price == null)}
              onClick={() => (free ? void issue().then(done, (e) => toast((e as Error).message, 'error')) : setPaying(true))} />
          </div>
          <ErrorAlert error={q.error} />
        </div>
      </Card>
      <Card title="Today's tickets" icon="list">
        <DataTable rows={entries.data?.items} loading={entries.isLoading} columns={[{ key: 'ticketNo', header: 'Ticket' }, { key: 'facilityName', header: 'Facility' },
          { key: 'entryType', header: 'Type', render: (e) => String(e.entryType).replace(/_/g, ' ') },
          { key: 'customerName', header: 'Visitor', render: (e) => String(e.customerName ?? e.guestName ?? '—') }, { key: 'amount', header: 'Amount', align: 'right', render: (e) => money(e.amount) },
          { key: 'status', header: 'Status', render: (e) => <StatusPill status={String(e.status)} /> }]}
          actions={(e) => e.status === 'issued' && <Btn label="Cancel" onClick={() => cancel.mutate({ id: e.id, reason: 'cancelled at the reception' }, { onSuccess: () => toast('Ticket cancelled') })} />} />
        <ErrorAlert error={entries.error ?? cancel.error} />
      </Card>
      {paying && q.price != null && (
        <DeskPayDialog amount={q.price} summary={[['Entry', `${String(fac?.name ?? '')} · ${ENTRY_TYPES.find((x) => x[0] === type)?.[1] ?? ''}`], ['Visitor', who.name || '—']]}
          members={who.customerId ? [{ customerId: who.customerId, name: who.name }] : []} onClose={() => setPaying(false)} pay={issue} onFinish={done} />
      )}
    </>
  );
}

type Slot = { start: string; end: string; status: string; price?: string | null };
type Pick = { courtId: string; courtName: string; start: string; end: string; price: number };
const hhmm = (iso: string) => new Date(iso).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });

function CourtsTab() {
  const toast = useToast();
  const page = useGet<{ facilities: R[]; courts: R[] }>(`/api/v1/public/sport-club${qs({ propertyId: getActiveProperty() })}`);
  const sports = (page.data?.facilities ?? []).filter((f) => f.usageMode === 'slot_booking');
  const [sport, setSport] = useState('');
  const sp = sport || String(sports[0]?.id ?? '');
  const courts = (page.data?.courts ?? []).filter((c) => c.facilityId === sp && c.resourceId);
  const [date, setDate] = useState(today());
  const [grid, setGrid] = useState<Record<string, Slot[]>>({});
  const [picks, setPicks] = useState<Pick[]>([]);
  const [who, setWho] = useState<Who>({ name: '', phone: '' });
  const [pkg, setPkg] = useState('');
  const [paying, setPaying] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const load = () => Promise.all(courts.map(async (c) => {
    const a = await request<{ resources: { slots: Slot[] }[] }>('GET', `/api/v1/public/availability${qs({ propertyId: getActiveProperty(), resourceType: 'sport_court',
      resourceId: String(c.resourceId), date })}`);
    return [c.id, a.resources[0]?.slots ?? []] as const;
  })).then((xs) => setGrid(Object.fromEntries(xs)), setError);
  const key = courts.map((c) => c.id).join(',');
  useEffect(() => { void load(); }, [key, date]); // eslint-disable-line react-hooks/exhaustive-deps
  const total = picks.reduce((n, p) => n + p.price, 0);
  const picked = (c: R, s: Slot) => picks.some((p) => p.courtId === c.id && p.start === s.start);
  const toggle = (c: R, s: Slot) => setPicks((ps) => (picked(c, s) ? ps.filter((p) => !(p.courtId === c.id && p.start === s.start))
    : [...ps, { courtId: c.id, courtName: String(c.name), start: s.start, end: s.end, price: Number(s.price ?? 0) }]));
  const body = () => ({ lines: picks.map((p) => ({ courtId: p.courtId, start: p.start, end: p.end })), customerId: who.customerId,
    guest: who.customerId ? undefined : { name: who.name, phone: who.phone || undefined }, channel: 'ops', packageCode: pkg || undefined });
  const book = async (t?: DeskTender) => {
    await request('POST', '/api/v1/sportclub/bookings', { ...body(), payment: t ? { methodType: t.methodType, reference: t.reference } : undefined }, idem());
    return [] as Record<string, unknown>[];
  };
  const done = () => { setPaying(false); setPicks([]); setPkg(''); toast('Court booked'); void load(); };
  const ready = picks.length > 0 && (who.customerId || who.name);
  return (
    <>
      <Card title="Courts" icon="sports_tennis">
        <div className="oc-stack">
          <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            {sports.map((f) => <Btn key={f.id} label={String(f.name)} kind={sp === f.id ? 'ink' : 'neutral'} onClick={() => { setSport(f.id); setPicks([]); }} />)}
            <TextField label="Date" type="date" value={date} onChange={(v) => { setDate(v); setPicks([]); }} />
          </div>
          {courts.map((c) => (
            <div key={c.id}>
              <strong>{String(c.name)}</strong> <span className="oc-small oc-muted">{String(c.surface ?? '')}</span>
              <div className="oc-row-wrap" style={{ marginTop: 6 }}>
                {(grid[c.id] ?? []).map((s) => (
                  <button key={s.start} type="button" className={`oc-btn ${picked(c, s) ? 'oc-btn-ink' : 'oc-btn-neutral'}`} style={{ minHeight: 56, minWidth: 104, flexDirection: 'column' }}
                    disabled={s.status !== 'available' && !picked(c, s)} onClick={() => toggle(c, s)}>
                    <strong>{hhmm(s.start)}</strong>
                    <span className="oc-small">{s.status === 'available' ? money(s.price) : s.status === 'reserved' ? 'Booked' : 'Closed'}</span>
                  </button>
                ))}
                {(grid[c.id] ?? []).length === 0 && <span className="oc-muted">No hours on this date.</span>}
              </div>
            </div>
          ))}
          <ErrorAlert error={error} />
        </div>
      </Card>
      <Card title="Booking" icon="shopping_cart">
        <div className="oc-stack">
          {picks.length === 0 ? <span className="oc-muted">Tap the free hours (several hours and courts in one booking).</span>
            : picks.map((p) => <div key={p.courtId + p.start}>{p.courtName} · {hhmm(p.start)}–{hhmm(p.end)} · {money(p.price)}</div>)}
          <WhoField value={who} onChange={setWho} />
          <TextField label="Court package code (4x/8x, optional — one hour at a time)" value={pkg} onChange={setPkg} />
          <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
            <strong style={{ fontSize: 22 }}>{pkg ? 'Package' : money(total)}</strong>
            <Btn label={pkg ? 'Book with the package' : 'Pay & book'} kind="primary" disabled={!ready || (!!pkg && picks.length !== 1)}
              onClick={() => (pkg ? void book().then(done, (e) => toast((e as Error).message, 'error')) : setPaying(true))} />
          </div>
        </div>
      </Card>
      {paying && (
        <DeskPayDialog amount={total} summary={[['Courts', picks.map((p) => `${p.courtName} ${hhmm(p.start)}`).join(', ')], ['Customer', who.name || '—']]}
          members={who.customerId ? [{ customerId: who.customerId, name: who.name }] : []} onClose={() => setPaying(false)} pay={book} onFinish={done} />
      )}
    </>
  );
}

function ClassesTab() {
  const toast = useToast();
  const programs = useGet<Page<R>>('/api/v1/sportclub/class-programs?filter[status]=active&limit=50');
  const sessions = useGet<Page<R>>(`/api/v1/sportclub/class-sessions${qs({ from: today(), days: 7, limit: 100 })}`);
  const enrollments = useGet<Page<R>>('/api/v1/sportclub/enrollments?limit=300');
  const bookSession = useSend<Record<string, unknown>>('POST', '/api/v1/sportclub/session-bookings', ['/api/v1/sportclub']);
  const [program, setProgram] = useState('');
  const [segment, setSegment] = useState<'member' | 'guest'>('guest');
  const [who, setWho] = useState<Who>({ name: '', phone: '' });
  const [paying, setPaying] = useState(false);
  const [session, setSession] = useState('');
  const [student, setStudent] = useState('');
  const prog = (programs.data?.items ?? []).find((p) => p.id === program);
  const q = useQuote(prog ? { serviceType: 'class_registration', itemRef: String(prog.code), segment: segment === 'member' ? 'member' : undefined, start: new Date().toISOString() } : null);
  const enroll = async (t?: DeskTender) => {
    const cid = await customerOf(who);
    await request('POST', '/api/v1/sportclub/enrollments', { programId: program, customerId: cid, segment, channel: 'ops',
      payment: t ? { methodType: t.methodType, reference: t.reference } : undefined }, idem());
    return [] as Record<string, unknown>[];
  };
  const done = () => { setPaying(false); toast('Registered — the class packages are on Packages & Vouchers'); setWho({ name: '', phone: '' }); void enrollments.refetch(); };
  const sess = (sessions.data?.items ?? []).find((s) => s.id === session);
  const students = (enrollments.data?.items ?? []).filter((e) => e.status === 'active' && (!sess || e.programId === sess.programId));
  return (
    <>
      <Card title="Class registration" icon="how_to_reg">
        <div className="oc-stack">
          <div className="oc-row-wrap">
            {(programs.data?.items ?? []).map((p) => <Btn key={p.id} label={String(p.name)} kind={program === p.id ? 'ink' : 'neutral'} onClick={() => setProgram(p.id)} />)}
          </div>
          <div className="oc-row-wrap">
            <Btn label="Member rate" kind={segment === 'member' ? 'ink' : 'neutral'} onClick={() => setSegment('member')} />
            <Btn label="Guest rate" kind={segment === 'guest' ? 'ink' : 'neutral'} onClick={() => setSegment('guest')} />
          </div>
          <WhoField value={who} onChange={setWho} label="Participant" />
          <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
            <strong style={{ fontSize: 22 }}>{q.price == null ? '—' : money(q.price)}</strong>
            <Btn label="Pay registration" kind="primary" disabled={!program || !(who.customerId || who.name) || q.price == null} onClick={() => setPaying(true)} />
          </div>
          <ErrorAlert error={q.error} />
        </div>
      </Card>
      <Card title="Book a class session" icon="event_seat">
        <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <SelectField label="Session (next 7 days)" value={session} onChange={(v) => { setSession(v); setStudent(''); }} placeholder="Choose"
            options={(sessions.data?.items ?? []).map((s) => ({ value: s.id, label: `${String(s.programName)} · ${formatDateTime(String(s.start))}` }))} />
          <SelectField label="Registered participant" value={student} onChange={setStudent} placeholder={students.length ? 'Choose' : 'Nobody registered yet'}
            options={students.map((e) => ({ value: String(e.customerId), label: String(e.customerName) }))} />
          <Btn label="Book seat" kind="primary" disabled={!session || !student || bookSession.isPending}
            onClick={() => bookSession.mutate({ sessionId: session, customerId: student, channel: 'ops' }, { onSuccess: () => toast('Seat booked (package session used)') })} />
        </div>
        <ErrorAlert error={bookSession.error} />
      </Card>
      {paying && q.price != null && (
        <DeskPayDialog amount={q.price} summary={[['Class', String(prog?.name ?? '')], ['Participant', who.name || '—'], ['Rate', segment]]}
          members={who.customerId ? [{ customerId: who.customerId, name: who.name }] : []} onClose={() => setPaying(false)} pay={enroll} onFinish={done} />
      )}
    </>
  );
}

const PACKAGE_CATEGORIES = ['sport_entry', 'court_package', 'class_package'];

function PackagesTab() {
  const toast = useToast();
  const types = useGet<Page<R>>('/api/v1/commercial/voucher-types?filter[status]=active&limit=200');
  const list = (types.data?.items ?? []).filter((v) => PACKAGE_CATEGORIES.includes(String(v.category)));
  const [cat, setCat] = useState('court_package');
  const [type, setType] = useState('');
  const [count, setCount] = useState(1);
  const [who, setWho] = useState<Who>({ name: '', phone: '' });
  const [paying, setPaying] = useState(false);
  const vt = list.find((v) => v.id === type);
  const amount = Number(vt?.price ?? 0) * count;
  const sell = async (t?: DeskTender) => {
    await request('POST', '/api/v1/commercial/vouchers:sell', { voucherTypeId: type, customerId: who.customerId, guestName: who.customerId ? undefined : who.name,
      count, channel: 'ops', payment: t ? { methodType: t.methodType, reference: t.reference } : undefined }, idem());
    return [] as Record<string, unknown>[];
  };
  const done = () => { setPaying(false); toast('Package sold — the code is on the receipt and in the customer profile'); setType(''); };
  const shown = useMemo(() => list.filter((v) => v.category === cat), [list, cat]);
  return (
    <>
      <Card title="Sell a package or voucher" icon="card_membership">
        <div className="oc-stack">
          <div className="oc-row-wrap">
            {[['court_package', 'Court 4x / 8x'], ['class_package', 'Class 4x / 8x'], ['sport_entry', 'Entry vouchers']].map(([k, l]) => (
              <Btn key={k} label={l} kind={cat === k ? 'ink' : 'neutral'} onClick={() => { setCat(k); setType(''); }} />
            ))}
          </div>
          <div className="oc-row-wrap">
            {shown.map((v) => (
              <button key={v.id} type="button" className="oc-chip" style={{ height: 'auto', minHeight: 44, padding: '6px 12px' }} aria-pressed={type === v.id} onClick={() => setType(v.id)}>
                {String(v.name)} · {money(v.price)}</button>
            ))}
            {types.data && shown.length === 0 && <span className="oc-muted">No package of this kind yet.</span>}
          </div>
          <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
            <span>Quantity</span>
            <Btn label="−" onClick={() => setCount(Math.max(1, count - 1))} /><strong>{count}</strong><Btn label="+" onClick={() => setCount(count + 1)} />
          </div>
          <WhoField value={who} onChange={setWho} />
          <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
            <strong style={{ fontSize: 22 }}>{money(amount)}</strong>
            <Btn label="Pay & sell" kind="primary" disabled={!type || !(who.customerId || who.name)} onClick={() => setPaying(true)} />
          </div>
        </div>
      </Card>
      {paying && (
        <DeskPayDialog amount={amount} summary={[['Package', `${String(vt?.name ?? '')} × ${count}`], ['Customer', who.name || '—']]}
          members={who.customerId ? [{ customerId: who.customerId, name: who.name }] : []} onClose={() => setPaying(false)} pay={sell} onFinish={done} />
      )}
    </>
  );
}

function LockersTab() {
  const toast = useToast();
  const lockers = useGet<Page<R>>('/api/v1/sportclub/lockers?limit=300');
  const active = useGet<Page<R>>('/api/v1/sportclub/locker-assignments?filter[status]=active&limit=200');
  const ret = useSend<{ id: string }>('POST', (v) => `/api/v1/sportclub/locker-assignments/${v.id}:return`, ['/api/v1/sportclub']);
  const [locker, setLocker] = useState('');
  const [kind, setKind] = useState<'daily' | 'rental'>('daily');
  const [until, setUntil] = useState('');
  const [fee, setFee] = useState('');
  const [who, setWho] = useState<Who>({ name: '', phone: '' });
  const [paying, setPaying] = useState(false);
  const free = (lockers.data?.items ?? []).filter((l) => l.status === 'available');
  const assign = async (t?: DeskTender) => {
    await request('POST', '/api/v1/sportclub/locker-assignments', { lockerId: locker, customerId: who.customerId, guestName: who.customerId ? undefined : who.name,
      assignmentType: kind, endAt: kind === 'rental' && until ? new Date(`${until}T23:59:00`).toISOString() : undefined, fee: fee || undefined,
      payment: t && Number(fee) > 0 ? { methodType: t.methodType, reference: t.reference } : undefined }, idem());
    return [] as Record<string, unknown>[];
  };
  const done = () => { setPaying(false); toast('Locker assigned'); setLocker(''); setFee(''); void lockers.refetch(); void active.refetch(); };
  return (
    <>
      <Card title="Assign a locker" icon="lock">
        <div className="oc-stack">
          <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            <SelectField label={`Free locker (${free.length})`} value={locker} onChange={setLocker} placeholder={free.length ? 'Choose' : 'No free locker'}
              options={free.map((l) => ({ value: l.id, label: `${String(l.code)} · ${String(l.area ?? '')}` }))} />
            <Btn label="Daily" kind={kind === 'daily' ? 'ink' : 'neutral'} onClick={() => setKind('daily')} />
            <Btn label="Rental" kind={kind === 'rental' ? 'ink' : 'neutral'} onClick={() => setKind('rental')} />
            {kind === 'rental' && <TextField label="Until" type="date" value={until} onChange={setUntil} />}
            <MoneyField label="Fee (optional)" value={fee} onChange={setFee} />
          </div>
          <WhoField value={who} onChange={setWho} />
          <div><Btn label={Number(fee) > 0 ? 'Pay & assign' : 'Assign'} kind="primary" disabled={!locker || !(who.customerId || who.name)}
            onClick={() => (Number(fee) > 0 ? setPaying(true) : void assign().then(done, (e) => toast((e as Error).message, 'error')))} /></div>
        </div>
      </Card>
      <Card title="Lockers in use" icon="list">
        <DataTable rows={active.data?.items} loading={active.isLoading} columns={[{ key: 'lockerCode', header: 'Locker' },
          { key: 'customerName', header: 'Customer', render: (a) => String(a.customerName ?? a.guestName ?? '—') },
          { key: 'assignmentType', header: 'Type' }, { key: 'startAt', header: 'Since', render: (a) => (a.startAt ? formatDateTime(String(a.startAt)) : '—') }]}
          actions={(a) => <Btn label="Return" onClick={() => ret.mutate({ id: a.id }, { onSuccess: () => { toast('Locker returned'); void lockers.refetch(); } })} />} />
        <ErrorAlert error={active.error ?? ret.error} />
      </Card>
      {paying && (
        <DeskPayDialog amount={Number(fee)} summary={[['Locker', String(free.find((l) => l.id === locker)?.code ?? '')], ['Customer', who.name || '—']]}
          members={who.customerId ? [{ customerId: who.customerId, name: who.name }] : []} onClose={() => setPaying(false)} pay={assign} onFinish={done} />
      )}
    </>
  );
}

function AccessTab() {
  const toast = useToast();
  const { propertyId } = useAuth();
  const facilities = useGet<Page<R>>('/api/v1/sportclub/facilities?filter[status]=active');
  const occupancy = useGet<Page<Schemas['Occupancy']>>('/api/v1/sportclub/occupancy', { refetchInterval: 30_000 });
  const [facility, setFacility] = useState(() => { try { return localStorage.getItem(SCAN_KEY) ?? ''; } catch { return ''; } });
  const [code, setCode] = useState('');
  const [result, setResult] = useState<Schemas['AccessResult'] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const validate = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      if (!navigator.onLine) {
        await enqueue('sportclub.access_validate', { code, facilityId: facility, direction: 'in', terminal: 'reception' }, propertyId);
        toast('Offline: access recorded in the sync queue');
      } else {
        setResult(await request<Schemas['AccessResult']>('POST', '/api/v1/sportclub/access:validate', { code, facilityId: facility, direction: 'in', terminal: 'reception' }));
      }
      setCode('');
      void occupancy.refetch();
    } catch (err) {
      setError(err);
    }
  };
  return (
    <>
      <Card title="Facility Access" icon="qr_code_scanner">
        <form className="oc-row-wrap" onSubmit={validate}>
          <div style={{ width: 260 }}><SelectField label="Facility" value={facility} onChange={(v) => { setFacility(v); try { localStorage.setItem(SCAN_KEY, v); } catch { /* ignore */ } }}
            options={(facilities.data?.items ?? []).map((f) => ({ value: String(f.id), label: String(f.name) }))} placeholder="Select facility" /></div>
          <div style={{ flex: 1, minWidth: 240 }}><TextField label="Scan member card, ticket or booking QR" value={code} onChange={setCode} autoFocus /></div>
          <button className="oc-btn oc-btn-ink" style={{ alignSelf: 'flex-end', minHeight: 48 }} disabled={!facility || !code}>Validate</button>
        </form>
        <ErrorAlert error={error} />
        {result && (
          <div className={`oc-alert ${result.result === 'granted' ? 'oc-alert-success' : 'oc-alert-error'}`} style={{ marginTop: 12 }}>
            <strong>{result.result === 'granted' ? 'Access granted' : 'Access denied'}</strong> · {result.credentialType}{result.customerName ? ` · ${result.customerName}` : ''}
            {result.reason ? ` — ${result.reason}` : ''}
          </div>
        )}
      </Card>
      <Card title="Facility Occupancy" icon="groups">
        <DataTable rows={occupancy.data?.items as unknown as R[]} rowKey={(r) => String(r.facilityId)} columns={[{ key: 'facilityName', header: 'Facility' },
          { key: 'inside', header: 'Inside' }, { key: 'capacity', header: 'Capacity' }, { key: 'entriesToday', header: 'Entries today' }, { key: 'booked', header: 'Booked' }]} />
      </Card>
    </>
  );
}
