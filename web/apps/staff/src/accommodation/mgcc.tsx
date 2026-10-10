import React, { useEffect, useRef, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { download, qs, request, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime } from '@oneclub/i18n';
import {
  Card, Checkbox, DataTable, Empty, ErrorAlert, Icon, Modal, MoneyField, PageHeader, SelectField, Skeleton, StatTile, StatusPill, TextArea, TextField, useAuth,
  useToast,
} from '@oneclub/shell';
import { CashierPage } from '../p3/billing';
import { KV, Tabs } from '../p1/common';
import { GuestPicker } from './reservations';
import { INV, PaymentStatus, StayDrawer, StayStatus, addDays, guestOf, label, money, todayISO, type Row, type Stay, type UnitOption } from './shared';

// The MGCC bungalow operations of the Staff App (docs/requirement-booking-
// hotel-mgcc.md Bagian B, C): the registration card (signature on the
// tablet, identity photo), room move and upgrade, void, the shift handover,
// the printed lists, group reservations with a rooming list, incidents, the
// front office night audit, the rate check of a channel, the finance
// reports and the cashier of the Stay Front Desk.

/** Opens a PDF of the API in a new tab (print) — with the session and the property. */
export async function openPdf(path: string, filename: string) {
  try {
    const blob = await request<Blob>('GET', path);
    const url = URL.createObjectURL(blob);
    const w = window.open(url, '_blank');
    if (!w) await download('GET', path, undefined, filename);
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
  } catch {
    await download('GET', path, undefined, filename);
  }
}

/** Signature of the registration card drawn with a finger or a pen (FR-H48). */
export function SignaturePad({ onChange }: { onChange: (dataUrl: string) => void }) {
  const ref = useRef<HTMLCanvasElement>(null);
  const drawing = useRef(false);
  const [empty, setEmpty] = useState(true);
  const ctx = () => ref.current?.getContext('2d') ?? null;
  const pos = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    return [(e.clientX - r.left) * (e.currentTarget.width / r.width), (e.clientY - r.top) * (e.currentTarget.height / r.height)] as const;
  };
  useEffect(() => {
    const c = ctx();
    if (c) {
      c.fillStyle = '#fff';
      c.fillRect(0, 0, 600, 200);
      c.lineWidth = 2.5;
      c.lineCap = 'round';
      c.strokeStyle = '#111';
    }
  }, []);
  const clear = () => {
    const c = ctx();
    if (c) {
      c.fillStyle = '#fff';
      c.fillRect(0, 0, 600, 200);
    }
    setEmpty(true);
    onChange('');
  };
  return (
    <div className="acc-signature">
      <canvas ref={ref} width={600} height={200} aria-label="Guest signature" style={{ touchAction: 'none' }}
        onPointerDown={(e) => { drawing.current = true; const c = ctx(); const [x, y] = pos(e); c?.beginPath(); c?.moveTo(x, y); e.currentTarget.setPointerCapture(e.pointerId); }}
        onPointerMove={(e) => { if (!drawing.current) return; const c = ctx(); const [x, y] = pos(e); c?.lineTo(x, y); c?.stroke(); setEmpty(false); }}
        onPointerUp={() => { drawing.current = false; if (ref.current && !empty) onChange(ref.current.toDataURL('image/png')); }}
        onPointerLeave={() => { if (drawing.current && ref.current) onChange(ref.current.toDataURL('image/png')); drawing.current = false; }} />
      <div className="oc-row" style={{ justifyContent: 'space-between' }}>
        <span className="oc-small oc-muted">{empty ? 'Sign above (finger or pen)' : 'Signed'}</span>
        <button type="button" className="oc-btn oc-btn-text oc-btn-sm" onClick={clear}>Clear</button>
      </div>
    </div>
  );
}

/** Photo of the identity card / passport (FR-H47): only staff with stay.identity.view see it again. */
export function IdentityPhoto({ stay, onDone }: { stay: Stay; onDone?: () => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [url, setUrl] = useState('');
  const [err, setErr] = useState<unknown>(null);
  useEffect(() => {
    let live = true;
    let u = '';
    if (stay.hasIdPhoto && can('stay.identity.view')) {
      request<Blob>('GET', `/api/v1/stay/stays/${stay.id}/identity-photo`).then((b) => { if (live) { u = URL.createObjectURL(b); setUrl(u); } }, () => undefined);
    }
    return () => { live = false; if (u) URL.revokeObjectURL(u); };
  }, [stay.id, stay.hasIdPhoto, can]);
  const upload = async (f: File) => {
    setBusy(true);
    setErr(null);
    const fd = new FormData();
    fd.append('file', f);
    try {
      await request('POST', `/api/v1/stay/stays/${stay.id}/identity-photo`, fd);
      setUrl(URL.createObjectURL(f));
      toast('Identity photo saved');
      onDone?.();
    } catch (x) {
      setErr(x);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
      {url ? <img src={url} alt="Identity" className="acc-idphoto" /> : <span className="oc-muted oc-small">{stay.hasIdPhoto ? 'Photo on file' : 'No photo yet'}</span>}
      <label className="oc-btn oc-btn-neutral oc-btn-sm" style={{ cursor: 'pointer' }}>
        <Icon name="photo_camera" size={16} /> {busy ? 'Uploading…' : stay.hasIdPhoto ? 'Replace photo' : 'Photo of KTP / passport'}
        <input type="file" accept="image/jpeg,image/png" capture="environment" hidden disabled={busy} onChange={(e) => { const f = e.target.files?.[0]; if (f) void upload(f); }} />
      </label>
      <span className="oc-small oc-muted">OCR is on hold: type the number by hand.</span>
      <ErrorAlert error={err} />
    </div>
  );
}

/** Units of a stay for a move / upgrade with their warnings. */
function UnitChoice({ stay, value, onChange, filter }: { stay: Stay; value: string; onChange: (v: string) => void; filter: (o: UnitOption) => boolean }) {
  const opts = useGet<Page<UnitOption>>(`/api/v1/stay/stays/${stay.id}/unit-options`);
  if (opts.isLoading) return <Skeleton rows={3} />;
  const list = (opts.data?.items ?? []).filter((o) => !o.current && filter(o));
  if (!list.length) return <Empty title="No bungalow available" help="Every bungalow is occupied, blocked or not Ready for these nights." icon="meeting_room" />;
  return (
    <div className="acc-units">
      {list.map((o) => (
        <button key={o.id} type="button" className="acc-unit" aria-pressed={value === o.id} data-warn={o.warnings.length > 0} onClick={() => onChange(o.id)}>
          <strong>{o.code}</strong> <span className="oc-small">{o.typeName}</span>
          <span className="oc-row-wrap" style={{ gap: 4 }}>
            <StatusPill status={o.operationalStatus} label={label(o.operationalStatus)} tone={o.operationalStatus === 'ready' ? 'success' : 'warning'} />
            {o.warnings.map((w) => <span key={w} className="acc-warn"><Icon name="warning" size={14} /> {label(w)}</span>)}
          </span>
        </button>
      ))}
    </div>
  );
}

/** Pindah Bungalow of an in-house guest (FR-H53). */
export function MoveModal({ stay, onClose }: { stay: Stay; onClose: () => void }) {
  const toast = useToast();
  const [v, setV] = useState({ unitId: '', reason: '', upgrade: false, charge: false });
  const send = useSend<Row>('POST', `/api/v1/stay/stays/${stay.id}:move`, INV);
  return (
    <Modal open wide onClose={onClose} title={`Room move · ${guestOf(stay)} · ${stay.unitName}`}
      actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
        <button className="oc-btn oc-btn-ink" disabled={!v.unitId || !v.reason || send.isPending}
          onClick={() => send.mutate({ unitId: v.unitId, reason: v.reason, upgrade: v.upgrade || undefined, chargeDifference: v.charge || undefined },
            { onSuccess: () => { toast('Moved — the old bungalow is Dirty for housekeeping'); onClose(); } })}>Move guest</button></>}>
      <p className="oc-muted oc-small">One folio stays; the old bungalow becomes Dirty with a cleaning task. Only Ready bungalows free until the departure are offered.</p>
      <UnitChoice stay={stay} value={v.unitId} onChange={(x) => setV({ ...v, unitId: x })} filter={(o) => o.free && o.operationalStatus === 'ready'} />
      <div className="oc-form" style={{ marginTop: 8 }}>
        <TextField label="Reason" value={v.reason} onChange={(x) => setV({ ...v, reason: x })} required placeholder="AC broken, guest request …" />
      </div>
      <Checkbox label="The guest asked for an upgrade (another room type)" checked={v.upgrade} onChange={(x) => setV({ ...v, upgrade: x })} />
      {v.upgrade && <Checkbox label="Charge the price difference of the nights left" checked={v.charge} onChange={(x) => setV({ ...v, charge: x })} />}
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

/** Upgrade before check-in (FR-H46): free keeps the price, paid reprices at the new type. */
export function UpgradeModal({ stay, onClose }: { stay: Stay; onClose: () => void }) {
  const toast = useToast();
  const [v, setV] = useState({ unitId: '', paid: false, reason: '' });
  const send = useSend<Row>('POST', `/api/v1/stay/stays/${stay.id}:upgrade`, INV);
  return (
    <Modal open wide onClose={onClose} title={`Upgrade · ${stay.stayNo}`}
      actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
        <button className="oc-btn oc-btn-ink" disabled={!v.unitId || !v.reason || send.isPending}
          onClick={() => send.mutate(v, { onSuccess: () => { toast(v.paid ? 'Upgraded and repriced' : 'Free upgrade'); onClose(); } })}>Upgrade</button></>}>
      <UnitChoice stay={stay} value={v.unitId} onChange={(x) => setV({ ...v, unitId: x })} filter={(o) => o.free && !o.sameType} />
      <div className="acc-chips" style={{ margin: '8px 0' }}>
        <button type="button" className="oc-chip" aria-pressed={!v.paid} onClick={() => setV({ ...v, paid: false })}>Free upgrade (price kept)</button>
        <button type="button" className="oc-chip" aria-pressed={v.paid} onClick={() => setV({ ...v, paid: true })}>Paid upgrade (new price)</button>
      </div>
      <TextField label="Reason" value={v.reason} onChange={(x) => setV({ ...v, reason: x })} required />
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

/** Void a reservation keyed in by mistake (supervisor, FR-H91). */
export function VoidModal({ stay, onClose }: { stay: Stay; onClose: () => void }) {
  const toast = useToast();
  const [reason, setReason] = useState('');
  const send = useSend<Row>('POST', `/api/v1/stay/stays/${stay.id}:void`, INV);
  return (
    <Modal open onClose={onClose} title={`Void ${stay.stayNo}`}
      actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
        <button className="oc-btn oc-btn-danger" disabled={!reason || send.isPending} onClick={() => send.mutate({ reason }, { onSuccess: () => { toast('Voided'); onClose(); } })}>Void</button></>}>
      <p className="oc-muted">For a reservation keyed in by mistake: no fee, the bungalow is released, the reason is kept in the audit trail. Payments received are refunded by Finance.</p>
      <TextArea label="Reason" rows={2} value={reason} onChange={setReason} required />
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── shift handover (FR-H61) and printed lists (FR-H62) ────────────────────

interface Handover { id: string; businessDate: string; category: string; body: string; stayNo: string | null; status: string; authorName: string | null; createdAt: string }
const NOTE_CATS: [string, string][] = [['general', 'General'], ['vip', 'VIP'], ['complaint', 'Complaint'], ['key', 'Keys'], ['lost_found', 'Lost & found'], ['payment', 'Payment']];

export function HandoverPanel() {
  const { can } = useAuth();
  const toast = useToast();
  const list = useGet<Page<Handover>>('/api/v1/stay/handover-notes?days=2', { refetchInterval: 60_000 });
  const [v, setV] = useState({ category: 'general', body: '' });
  const add = useSend<Row>('POST', '/api/v1/stay/handover-notes', ['/api/v1/stay/handover']);
  const done = useSend<{ id: string }>('POST', (b) => `/api/v1/stay/handover-notes/${b.id}:done`, ['/api/v1/stay/handover']);
  const open = (list.data?.items ?? []).filter((n) => n.status === 'open');
  return (
    <Card title={`Shift handover (${open.length})`} icon="swap_horiz">
      {open.length === 0 && <p className="oc-muted oc-small" style={{ margin: 0 }}>No open notes from the previous shift.</p>}
      <ul className="acc-notes">
        {open.map((n) => (
          <li key={n.id}>
            <StatusPill status={n.category} label={NOTE_CATS.find((c) => c[0] === n.category)?.[1] ?? label(n.category)} tone={n.category === 'vip' || n.category === 'complaint' ? 'warning' : 'info'} />
            <span>{n.body}{n.stayNo ? ` · ${n.stayNo}` : ''}</span>
            <span className="oc-small oc-muted">{n.authorName ?? ''} · {formatDateTime(n.createdAt)}</span>
            {can('stay.stay.update') && <button className="oc-btn oc-btn-text oc-btn-sm" disabled={done.isPending} onClick={() => done.mutate({ id: n.id })}>Done</button>}
          </li>
        ))}
      </ul>
      {can('stay.stay.update') && (
        <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <SelectField label="Category" value={v.category} onChange={(x) => setV({ ...v, category: x })} options={NOTE_CATS.map(([value, l]) => ({ value, label: l }))} />
          <TextField label="Note for the next shift" value={v.body} onChange={(x) => setV({ ...v, body: x })} />
          <button className="oc-btn oc-btn-neutral" disabled={!v.body || add.isPending}
            onClick={() => add.mutate(v, { onSuccess: () => { setV({ category: 'general', body: '' }); toast('Note saved'); } })}>Add note</button>
        </div>
      )}
      <ErrorAlert error={add.error ?? done.error} />
    </Card>
  );
}

export function PrintMenu({ date }: { date: string }) {
  const lists: [string, string][] = [['arrivals', 'Arrival List'], ['departures', 'Departure List'], ['in-house', 'In-house List'], ['foreign-guests', 'Daftar Tamu Asing']];
  return (
    <div className="oc-row-wrap">
      {lists.map(([k, l]) => (
        <button key={k} type="button" className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => void openPdf(`/api/v1/stay/print/${k}${qs({ date })}`, `${k}-${date}.pdf`)}>
          <Icon name="print" size={16} /> {l}</button>
      ))}
    </div>
  );
}

// ── groups (FR-H64) ──────────────────────────────────────────────────────

interface Group extends Row { id: string; groupNo: string; name: string; contactName: string | null; contactPhone: string | null; folioMode: string; bookingSource: string | null;
  status: string; bungalows: number; arrival: string | null; departure: string | null; total: string; paid: string; publicToken: string | null; createdAt: string }

export function GroupsPage({ ops }: { ops?: boolean }) {
  const { can } = useAuth();
  const nav = useNavigate();
  const [q, setQ] = useState('');
  const list = useGet<Page<Group>>(`/api/v1/stay/groups${qs({ q, limit: 100 })}`);
  const base = ops ? '/ops/stay-desk/groups' : '/accommodation/groups';
  return (
    <div className="oc-stack">
      <PageHeader title="Group reservations" help="One booker, several bungalows with their guests (rooming list), one group code and a combined or per-bungalow bill."
        actions={can('stay.stay.create') ? <Link className="oc-btn oc-btn-primary" to={`${base}/new`}><Icon name="add" size={18} /> New group</Link> : undefined} />
      <TextField label="Search" value={q} onChange={setQ} placeholder="Group code or name" />
      <div className="oc-card">
        <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} rowKey={(g) => g.id} onRowClick={(g) => nav(`${base}/${g.id}`)}
          empty={<Empty title="No group reservations" icon="groups" />} columns={[
            { key: 'groupNo', header: 'Group', render: (g) => <><strong>{g.groupNo}</strong><div className="oc-small oc-muted">{g.name}</div></> },
            { key: 'contact', header: 'Booker', render: (g) => <>{g.contactName ?? '—'}<div className="oc-small oc-muted">{g.contactPhone ?? ''}</div></> },
            { key: 'stay', header: 'Stay', render: (g) => (g.arrival ? `${formatDate(g.arrival)} → ${formatDate(g.departure ?? g.arrival)}` : '—') },
            { key: 'bungalows', header: 'Bungalows', align: 'right' },
            { key: 'folioMode', header: 'Bill', render: (g) => (g.folioMode === 'combined' ? 'Combined' : 'Per bungalow') },
            { key: 'total', header: 'Total', align: 'right', render: (g) => money(g.total) },
            { key: 'paid', header: 'Paid', align: 'right', render: (g) => money(g.paid) }]} />
      </div>
    </div>
  );
}

interface CartLine { index: number; typeName: string; ratePlanName: string; total: string; depositRequired: string; error?: string | null }
interface CartQuote { items: CartLine[]; subtotal: string; service: string; tax: string; total: string; depositNow: string; payAtHotel: string; ok: boolean; nights: number }
interface Item { bungalowTypeId: string; ratePlan: string; adults: string; children: string; occupantName: string }

export function NewGroupPage({ ops }: { ops?: boolean }) {
  const nav = useNavigate();
  const toast = useToast();
  const { can } = useAuth();
  const base = ops ? '/ops/stay-desk/groups' : '/accommodation/groups';
  const types = useGet<Page<Row>>('/api/v1/stay/bungalow-types?limit=100&filter[status]=active');
  const plans = useGet<Page<Row>>('/api/v1/stay/rate-plans?limit=100&filter[status]=active');
  const corps = useGet<Page<Row>>('/api/v1/crm/corporate-accounts?limit=200');
  const [h, setH] = useState({ name: '', arrival: todayISO(), departure: todayISO(1), folioMode: 'combined', source: 'phone', corporateAccountId: '', notes: '',
    override: false, reason: '', deposit: false, method: 'bank_transfer' });
  const [cust, setCust] = useState<{ id: string; name: string } | null>(null);
  const [guest, setGuest] = useState({ name: '', phone: '', email: '' });
  const [items, setItems] = useState<Item[]>([{ bungalowTypeId: '', ratePlan: '', adults: '2', children: '0', occupantName: '' }]);
  const body = () => ({
    name: h.name || undefined, arrivalDate: h.arrival, departureDate: h.departure, folioMode: h.folioMode, bookingSource: h.source,
    corporateAccountId: h.corporateAccountId || undefined, notes: h.notes || undefined, overrideRestrictions: h.override || undefined,
    supervisorReason: h.override ? h.reason : undefined, customerId: cust?.id, guest: cust ? undefined : { name: guest.name, phone: guest.phone || undefined, email: guest.email || undefined },
    items: items.filter((i) => i.bungalowTypeId).map((i) => ({ bungalowTypeId: i.bungalowTypeId, ratePlan: i.ratePlan || undefined, adults: Number(i.adults) || 1,
      children: Number(i.children) || 0, occupantName: i.occupantName || undefined })),
  });
  const quote = useSend<Row, CartQuote>('POST', '/api/v1/stay/stays:quote-cart');
  const book = useSend<Row, { group: Group }>('POST', '/api/v1/stay/groups', INV);
  const set = (i: number, p: Partial<Item>) => setItems(items.map((x, k) => (k === i ? { ...x, ...p } : x)));
  const ok = items.some((i) => i.bungalowTypeId) && (cust || guest.name) && h.departure > h.arrival;
  return (
    <div className="oc-stack">
      <PageHeader title="New group reservation" help="Several bungalows for one booker: the rooming list names the guest of every bungalow." actions={<Link className="oc-btn oc-btn-neutral" to={base}>Groups</Link>} />
      <Card title="Group" icon="groups">
        <div className="oc-form">
          <TextField label="Group name" value={h.name} onChange={(x) => setH({ ...h, name: x })} placeholder="e.g. PT Golf Outing" />
          <TextField label="Check-in" type="date" value={h.arrival} onChange={(x) => setH({ ...h, arrival: x, departure: x >= h.departure ? addDays(x, 1) : h.departure })} />
          <TextField label="Check-out" type="date" value={h.departure} onChange={(x) => setH({ ...h, departure: x })} />
          <SelectField label="Bill" value={h.folioMode} onChange={(x) => setH({ ...h, folioMode: x })}
            options={[{ value: 'combined', label: 'Combined (one bill)' }, { value: 'per_stay', label: 'Per bungalow' }]} />
          <SelectField label="Source" value={h.source} onChange={(x) => setH({ ...h, source: x })}
            options={[['front_desk', 'Front Desk'], ['phone', 'Telepon'], ['walk_in', 'Walk-in'], ['corporate', 'Korporat']].map(([value, l]) => ({ value, label: l }))} />
          <SelectField label="Corporate account" value={h.corporateAccountId} onChange={(x) => setH({ ...h, corporateAccountId: x, source: x ? 'corporate' : h.source })} placeholder="None"
            options={(corps.data?.items ?? []).map((c) => ({ value: String(c.id), label: String(c.name) }))} />
        </div>
        <strong>Booker</strong>
        <GuestPicker value={cust} onChange={(c) => setCust(c)} />
        {!cust && (
          <div className="oc-form">
            <TextField label="Booker name" value={guest.name} onChange={(x) => setGuest({ ...guest, name: x })} />
            <TextField label="Phone" value={guest.phone} onChange={(x) => setGuest({ ...guest, phone: x })} />
            <TextField label="E-mail" value={guest.email} onChange={(x) => setGuest({ ...guest, email: x })} />
          </div>
        )}
      </Card>
      <Card title="Rooming list" icon="list_alt">
        {items.map((it, i) => (
          <div key={i} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            <SelectField label={`Bungalow ${i + 1} · type`} value={it.bungalowTypeId} onChange={(x) => set(i, { bungalowTypeId: x })}
              options={(types.data?.items ?? []).map((t) => ({ value: String(t.id), label: String(t.name) }))} />
            <SelectField label="Rate plan" value={it.ratePlan} onChange={(x) => set(i, { ratePlan: x })} placeholder="Default"
              options={(plans.data?.items ?? []).map((p) => ({ value: String(p.code), label: String(p.name) }))} />
            <TextField label="Adults" type="number" value={it.adults} onChange={(x) => set(i, { adults: x })} />
            <TextField label="Children" type="number" value={it.children} onChange={(x) => set(i, { children: x })} />
            <TextField label="Guest staying" value={it.occupantName} onChange={(x) => set(i, { occupantName: x })} />
            {items.length > 1 && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setItems(items.filter((_, k) => k !== i))}>Remove</button>}
          </div>
        ))}
        <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setItems([...items, { bungalowTypeId: items[items.length - 1]?.bungalowTypeId ?? '', ratePlan: items[items.length - 1]?.ratePlan ?? '',
          adults: '2', children: '0', occupantName: '' }])}><Icon name="add" size={16} /> Add a bungalow</button>
        {can('stay.stay.supervise') && <Checkbox label="Pass a closed date (supervisor)" checked={h.override} onChange={(x) => setH({ ...h, override: x })} />}
        {h.override && <TextField label="Supervisor reason" value={h.reason} onChange={(x) => setH({ ...h, reason: x })} />}
        <TextArea label="Notes" rows={2} value={h.notes} onChange={(x) => setH({ ...h, notes: x })} />
        <div className="oc-row" style={{ marginTop: 8 }}>
          <button className="oc-btn oc-btn-neutral" disabled={!ok || quote.isPending} onClick={() => quote.mutate(body())}>Price the group</button>
        </div>
        <ErrorAlert error={quote.error} />
      </Card>
      {quote.data && (
        <Card title="Price summary" icon="receipt_long">
          <DataTable rows={quote.data.items as unknown as Row[]} rowKey={(l) => String(l.index)} columns={[
            { key: 'typeName', header: 'Bungalow', render: (l) => `${Number(l.index) + 1}. ${String(l.typeName)} · ${String(l.ratePlanName ?? '')}` },
            { key: 'total', header: 'Total', align: 'right', render: (l) => (l.error ? <span className="acc-full">{String(l.error)}</span> : money(l.total)) },
            { key: 'depositRequired', header: 'Deposit', align: 'right', render: (l) => money(l.depositRequired) }]} />
          <KV items={[['Total', <strong key="t">{money(quote.data.total)}</strong>], ['Deposit', money(quote.data.depositNow)], ['Pay at the hotel', money(quote.data.payAtHotel)]]} />
          <Checkbox label="Take the deposit now" checked={h.deposit} onChange={(x) => setH({ ...h, deposit: x })} />
          {h.deposit && <SelectField label="Method" value={h.method} onChange={(x) => setH({ ...h, method: x })}
            options={['cash', 'card', 'bank_transfer', 'qris'].map((m) => ({ value: m, label: label(m) }))} />}
          <div className="oc-row" style={{ marginTop: 12 }}>
            <button className="oc-btn oc-btn-ink" disabled={!quote.data.ok || book.isPending}
              onClick={() => book.mutate({ ...body(), payment: h.deposit ? { kind: 'deposit', methodType: h.method } : undefined },
                { onSuccess: (r) => { toast(`Group ${r.group.groupNo} booked`); nav(`${base}/${r.group.id}`); } })}><Icon name="check" size={18} /> Book the group</button>
          </div>
          <ErrorAlert error={book.error} />
        </Card>
      )}
    </div>
  );
}

export function GroupDetailPage({ ops }: { ops?: boolean }) {
  const { id = '' } = useParams();
  const { can } = useAuth();
  const toast = useToast();
  const d = useGet<{ group: Group; stays: Stay[] }>(`/api/v1/stay/groups/${id}`);
  const [open, setOpen] = useState<string | null>(null);
  const [dialog, setDialog] = useState<'' | 'in' | 'settle' | 'out'>('');
  const [v, setV] = useState({ idType: 'ktp', idNumber: '', method: 'card' });
  const act = useSend<Row, { done: string[]; failed: string[] }>('POST', (b) => `/api/v1/stay/groups/${id}:${String(b.op)}`, INV);
  const run = (op: string, body: Row) => act.mutate({ op, ...body }, {
    onSuccess: (r) => { toast(`${r.done.length} done${r.failed.length ? ` · ${r.failed.length} refused: ${r.failed.join('; ')}` : ''}`, r.failed.length ? 'error' : undefined); setDialog(''); void d.refetch(); },
  });
  if (d.isLoading) return <Skeleton rows={8} />;
  if (!d.data) return <ErrorAlert error={d.error} />;
  const g = d.data.group;
  return (
    <div className="oc-stack">
      <PageHeader title={`${g.groupNo} · ${g.name}`} help={`${g.bungalows} bungalow(s) · ${g.folioMode === 'combined' ? 'combined bill' : 'bill per bungalow'}`}
        actions={<Link className="oc-btn oc-btn-neutral" to={ops ? '/ops/stay-desk/groups' : '/accommodation/groups'}>Groups</Link>} />
      <div className="oc-row-wrap">
        <StatTile label="Total" value={money(g.total)} icon="receipt_long" />
        <StatTile label="Paid" value={money(g.paid)} icon="payments" />
        <StatTile label="Balance" value={money(Number(g.total) - Number(g.paid))} icon="account_balance_wallet" />
      </div>
      <div className="oc-row-wrap">
        {can('stay.stay.check_in') && <button className="oc-btn oc-btn-ink" onClick={() => setDialog('in')}><Icon name="login" size={18} /> Check in all</button>}
        {can('billing.payment.create') && <button className="oc-btn oc-btn-neutral" onClick={() => setDialog('settle')}><Icon name="payments" size={18} /> Pay all balances</button>}
        {can('stay.stay.check_out') && <button className="oc-btn oc-btn-primary" onClick={() => setDialog('out')}><Icon name="logout" size={18} /> Check out all</button>}
      </div>
      <div className="oc-card">
        <DataTable rows={d.data.stays} rowKey={(s) => s.id} onRowClick={(s) => setOpen(s.id)} columns={[
          { key: 'stayNo', header: 'Bungalow', render: (s) => <><strong>{s.unitAssigned ? s.unitCode : s.typeName}</strong><div className="oc-small oc-muted">{s.stayNo}</div></> },
          { key: 'occupant', header: 'Guest', render: (s) => s.occupantName ?? guestOf(s) },
          { key: 'stay', header: 'Stay', render: (s) => `${formatDate(s.start)} → ${formatDate(s.end)}` },
          { key: 'total', header: 'Total', align: 'right', render: (s) => money(s.totalDue) },
          { key: 'pay', header: 'Payment', render: (s) => <PaymentStatus status={s.paymentStatus} /> },
          { key: 'status', header: 'Status', render: (s) => <StayStatus s={s} /> }]} />
      </div>
      {dialog && (
        <Modal open onClose={() => setDialog('')} title={dialog === 'in' ? 'Check in the group' : dialog === 'out' ? 'Check out the group' : 'Pay the balances'}
          actions={<><button className="oc-btn oc-btn-neutral" onClick={() => setDialog('')}>Close</button>
            <button className="oc-btn oc-btn-ink" disabled={act.isPending || (dialog === 'in' && !v.idNumber)}
              onClick={() => run(dialog === 'in' ? 'check-in' : dialog === 'out' ? 'check-out' : 'settle',
                dialog === 'in' ? { idType: v.idType, idNumber: v.idNumber } : dialog === 'settle' ? { methodType: v.method } : {})}>Go</button></>}>
          {dialog === 'in' && <div className="oc-form">
            <SelectField label="Identity of the booker" value={v.idType} onChange={(x) => setV({ ...v, idType: x })}
              options={['ktp', 'passport', 'sim', 'kitas'].map((x) => ({ value: x, label: x.toUpperCase() }))} />
            <TextField label="ID number" value={v.idNumber} onChange={(x) => setV({ ...v, idNumber: x })} />
          </div>}
          {dialog === 'settle' && <SelectField label="Method" value={v.method} onChange={(x) => setV({ ...v, method: x })}
            options={['cash', 'card', 'bank_transfer', 'qris'].map((m) => ({ value: m, label: label(m) }))} />}
          {dialog === 'out' && <p className="oc-muted">Every in-house bungalow whose bill is paid is checked out; the others are listed with their reason.</p>}
          <ErrorAlert error={act.error} />
        </Modal>
      )}
      {open && <StayDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

// ── incidents (FR-H86) ──────────────────────────────────────────────────

interface Incident extends Row { id: string; number: string; bungalowCode: string | null; stayNo: string | null; category: string; severity: string; status: string;
  description: string; actionTaken: string | null; damageAmount: string | null; reportedBy: string | null; occurredAt: string }

export function IncidentsPage() {
  const { can } = useAuth();
  const toast = useToast();
  const [status, setStatus] = useState('open');
  const list = useGet<Page<Incident>>(`/api/v1/stay/incidents${qs({ status: status === 'all' ? '' : status })}`);
  const rooms = useGet<Page<Row>>('/api/v1/stay/bungalows?limit=200');
  const [report, setReport] = useState(false);
  const [close, setClose] = useState<Incident | null>(null);
  const [v, setV] = useState({ bungalowId: '', category: 'damage', severity: 'medium', description: '', damageAmount: '' });
  const [action, setAction] = useState('');
  const add = useSend<Row>('POST', '/api/v1/stay/incidents', ['/api/v1/stay/incidents']);
  const done = useSend<Row>('POST', () => `/api/v1/stay/incidents/${close?.id}:close`, ['/api/v1/stay/incidents']);
  return (
    <div className="oc-stack">
      <PageHeader title="Bungalow incidents" help="Damage, complaints, lost items and safety issues of the bungalows; they show on Management › Incidents."
        actions={can('stay.incident.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setReport(true)}><Icon name="add" size={18} /> Report</button> : undefined} />
      <Tabs value={status} onChange={setStatus} tabs={[{ value: 'open', label: 'Open' }, { value: 'closed', label: 'Closed' }, { value: 'all', label: 'All' }]} />
      <div className="oc-card">
        <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} rowKey={(r) => r.id} empty={<Empty title="No incidents" icon="report" />}
          actions={(r) => (r.status === 'open' && can('stay.incident.manage') ? <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setClose(r)}>Close</button> : null)}
          columns={[{ key: 'number', header: 'Incident', render: (r) => <><strong>{r.number}</strong><div className="oc-small oc-muted">{formatDateTime(r.occurredAt)}</div></> },
            { key: 'where', header: 'Bungalow', render: (r) => <>{r.bungalowCode ?? '—'}<div className="oc-small oc-muted">{r.stayNo ?? ''}</div></> },
            { key: 'category', header: 'Category', render: (r) => <>{label(r.category)}<div className="oc-small oc-muted">{label(r.severity)}</div></> },
            { key: 'description', header: 'Description', render: (r) => <>{r.description}{r.actionTaken ? <div className="oc-small oc-muted">→ {r.actionTaken}</div> : null}</> },
            { key: 'damage', header: 'Damage', align: 'right', render: (r) => (r.damageAmount ? money(r.damageAmount) : '—') },
            { key: 'status', header: 'Status', render: (r) => <StatusPill status={r.status} /> }]} />
      </div>
      {report && (
        <Modal open onClose={() => setReport(false)} title="Report an incident" actions={<><button className="oc-btn oc-btn-neutral" onClick={() => setReport(false)}>Close</button>
          <button className="oc-btn oc-btn-ink" disabled={!v.description || add.isPending}
            onClick={() => add.mutate({ ...v, bungalowId: v.bungalowId || undefined, damageAmount: v.damageAmount || undefined },
              { onSuccess: () => { toast('Incident reported'); setReport(false); } })}>Report</button></>}>
          <div className="oc-form">
            <SelectField label="Bungalow" value={v.bungalowId} onChange={(x) => setV({ ...v, bungalowId: x })} placeholder="General"
              options={(rooms.data?.items ?? []).map((b) => ({ value: String(b.id), label: `${String(b.code)} · ${String(b.name)}` }))} />
            <SelectField label="Category" value={v.category} onChange={(x) => setV({ ...v, category: x })}
              options={['damage', 'complaint', 'lost_item', 'safety', 'noise', 'other'].map((c) => ({ value: c, label: label(c) }))} />
            <SelectField label="Severity" value={v.severity} onChange={(x) => setV({ ...v, severity: x })} options={['low', 'medium', 'high'].map((c) => ({ value: c, label: label(c) }))} />
            <MoneyField label="Damage (optional)" value={v.damageAmount} onChange={(x) => setV({ ...v, damageAmount: x })} />
            <TextArea label="Description" span rows={2} value={v.description} onChange={(x) => setV({ ...v, description: x })} />
          </div>
          <ErrorAlert error={add.error} />
        </Modal>
      )}
      {close && (
        <Modal open onClose={() => setClose(null)} title={`Close ${close.number}`} actions={<><button className="oc-btn oc-btn-neutral" onClick={() => setClose(null)}>Close</button>
          <button className="oc-btn oc-btn-ink" disabled={!action || done.isPending} onClick={() => done.mutate({ actionTaken: action }, { onSuccess: () => { setClose(null); setAction(''); } })}>Save</button></>}>
          <TextArea label="Action taken" rows={2} value={action} onChange={setAction} />
          <ErrorAlert error={done.error} />
        </Modal>
      )}
    </div>
  );
}

// ── front office night audit (FR-H74) and Manager Flash (FR-H75) ─────────

interface AuditItem { stayId: string; stayNo: string; guest: string; unit: string; detail: string }
interface AuditCheck { key: string; label: string; severity: string; count: number; items: AuditItem[]; detail: string | null }
interface Flash extends Row { date: string; frozen: boolean; units: number; outOfOrder: number; available: number; sold: number; occupancy: string; adr: string; revpar: string;
  roomRevenue: string; addonRevenue: string; fnbChargeToRoom: string; otherRevenue: string; arrivals: number; departures: number; inHouse: number; inHouseGuests: number;
  noShows: number; cancellations: number; expired: number; mtd: Row & { occupancy: string; adr: string; revpar: string; roomRevenue: string; occupied: number; available: number };
  bySource: { key: string; count: number; nights: number }[] }
interface FOAudit { businessDate: string; closed: boolean; closedAt: string | null; checklist: AuditCheck[]; flash: Flash; notes: string | null }

export function FlashCard({ f }: { f: Flash }) {
  return (
    <Card title={`Manager Flash · ${formatDate(f.date)}${f.frozen ? ' · closed' : ''}`} icon="flash_on">
      <div className="oc-row-wrap">
        <StatTile label="Occupancy" value={`${f.occupancy}%`} icon="hotel" />
        <StatTile label="Sold / available" value={`${f.sold} / ${f.available}`} icon="bed" />
        <StatTile label="ADR" value={money(f.adr)} icon="sell" />
        <StatTile label="RevPAR" value={money(f.revpar)} icon="insights" />
      </div>
      <KV items={[['Room revenue (before tax)', money(f.roomRevenue)], ['Add-ons', money(f.addonRevenue)], ['F&B charged to rooms', money(f.fnbChargeToRoom)],
        ['Other', money(f.otherRevenue)], ['Arrivals · departures · in-house', `${f.arrivals} · ${f.departures} · ${f.inHouse} (${f.inHouseGuests} guests)`],
        ['No-show · cancelled · expired', `${f.noShows} · ${f.cancellations} · ${f.expired}`], ['Out of order', String(f.outOfOrder)],
        ['Month to date', `${f.mtd.occupancy}% · ADR ${money(f.mtd.adr)} · RevPAR ${money(f.mtd.revpar)} · ${money(f.mtd.roomRevenue)}`],
        ['Bookings today by channel', f.bySource.map((b) => `${label(b.key)} ${b.count}`).join(' · ') || '—']]} />
    </Card>
  );
}

export function NightAuditPage() {
  const { can } = useAuth();
  const toast = useToast();
  const [date, setDate] = useState(todayISO());
  const [notes, setNotes] = useState('');
  const a = useGet<FOAudit>(`/api/v1/stay/night-audit${qs({ date })}`);
  const run = useSend<Row, FOAudit>('POST', '/api/v1/stay/night-audit', INV);
  return (
    <div className="oc-stack">
      <PageHeader title="Front Office Night Audit" help="Close the business day: arrivals that did not come become No-show (Stay Policies), the room nights are posted, the figures are frozen. It runs the same step as the Billing night audit." />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Business date" type="date" value={date} onChange={setDate} />
        {a.data?.closed && <StatusPill status="closed" label={`Closed ${a.data.closedAt ? formatDateTime(a.data.closedAt) : ''}`} tone="success" />}
      </div>
      <ErrorAlert error={a.error} />
      {a.isLoading && <Skeleton rows={6} />}
      {a.data && (
        <>
          {a.data.checklist.map((c) => (
            <Card key={c.key} title={`${c.label} (${c.count})`} icon={c.severity === 'warning' && c.count ? 'warning' : 'check_circle'}>
              {c.detail && <p className="oc-small" style={{ margin: 0 }}>{c.detail}</p>}
              {c.items.length > 0 && <DataTable rows={c.items as unknown as Row[]} rowKey={(i) => String(i.stayId) + String(i.detail)} columns={[
                { key: 'stayNo', header: 'Reservation' }, { key: 'guest', header: 'Guest' }, { key: 'unit', header: 'Bungalow' }, { key: 'detail', header: 'Detail' }]} />}
            </Card>
          ))}
          <FlashCard f={a.data.flash} />
          {!a.data.closed && can('stay.night_audit.run') && (
            <Card title="Close the day" icon="nightlight">
              <TextArea label="Notes" rows={2} value={notes} onChange={setNotes} />
              <button className="oc-btn oc-btn-ink" disabled={run.isPending} onClick={() => run.mutate({ date, notes: notes || undefined }, { onSuccess: () => { toast('Day closed'); void a.refetch(); } })}>
                <Icon name="lock" size={18} /> Run night audit {formatDate(date)}</button>
              <ErrorAlert error={run.error} />
            </Card>
          )}
        </>
      )}
    </div>
  );
}

// ── rate check of a channel (FR-H70) ─────────────────────────────────────

interface PubRate { code: string; name: string; total: string; averagePerNight: string; listTotal: string; savePercent: number; includesBreakfast: boolean;
  taxIncluded: boolean; eligibility: string; nights: { date: string; price: string }[] }
interface PubType { id: string; name: string; available: number; full: boolean; closedReason: string | null; capacityNote: string | null; rates: PubRate[]; packages: PubRate[] }

export function RateCheckPage() {
  const [v, setV] = useState({ checkin: todayISO(1), checkout: todayISO(3), adults: '2', children: '0', promo: '', source: 'website' });
  const [run, setRun] = useState(false);
  const r = useGet<{ types: PubType[]; promoError: string | null; nights: number }>(run ? `/api/v1/stay/rate-check${qs(v)}` : null);
  return (
    <div className="oc-stack">
      <PageHeader title="Rate check" help="The prices exactly as the website or the Member App shows them for the dates — the same quote as the bill." />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Check-in" type="date" value={v.checkin} onChange={(x) => { setV({ ...v, checkin: x, checkout: x >= v.checkout ? addDays(x, 1) : v.checkout }); setRun(false); }} />
        <TextField label="Check-out" type="date" value={v.checkout} onChange={(x) => { setV({ ...v, checkout: x }); setRun(false); }} />
        <TextField label="Adults" type="number" value={v.adults} onChange={(x) => { setV({ ...v, adults: x }); setRun(false); }} />
        <TextField label="Children" type="number" value={v.children} onChange={(x) => { setV({ ...v, children: x }); setRun(false); }} />
        <SelectField label="Channel" value={v.source} onChange={(x) => { setV({ ...v, source: x }); setRun(false); }}
          options={[{ value: 'website', label: 'Website' }, { value: 'member_app', label: 'Member App' }]} />
        <TextField label="Promo code" value={v.promo} onChange={(x) => { setV({ ...v, promo: x.toUpperCase() }); setRun(false); }} />
        <button className="oc-btn oc-btn-primary" onClick={() => setRun(true)}><Icon name="search" size={18} /> Check</button>
      </div>
      <ErrorAlert error={r.error} />
      {r.data?.promoError && <div className="oc-alert oc-alert-warning">Promo code: {r.data.promoError}</div>}
      {r.data?.types.map((t) => (
        <Card key={t.id} title={`${t.name} · ${t.full ? 'sold out' : `${t.available} free`}`} icon="cottage">
          {t.closedReason && <p className="acc-full oc-small">{t.closedReason}</p>}
          {t.capacityNote && <p className="oc-small oc-muted">{t.capacityNote}</p>}
          <DataTable rows={[...t.rates, ...t.packages] as unknown as Row[]} rowKey={(x) => String(x.code)} empty={<span className="oc-muted">No rate for these dates on this channel.</span>}
            columns={[{ key: 'name', header: 'Rate', render: (x) => <>{String(x.name)}{x.eligibility === 'member' ? <span className="acc-vip">Member</span> : null}
              <div className="oc-small oc-muted">{x.includesBreakfast ? 'breakfast' : 'room only'} · {x.taxIncluded ? 'tax & service included' : 'excl. tax & service'}</div></> },
              { key: 'nights', header: 'Per night', render: (x) => (x.nights as { date: string; price: string }[]).map((n) => `${formatDate(n.date).slice(0, 6)} ${money(n.price)}`).join(' · ') },
              { key: 'total', header: `Total (${r.data?.nights} nights)`, align: 'right', render: (x) => <>{Number(x.savePercent) > 0 && <s className="oc-muted">{money(x.listTotal)} </s>}<strong>{money(x.total)}</strong></> }]} />
        </Card>
      ))}
    </div>
  );
}

// ── finance reports of the stays (FR-H75, FR-H79, FR-H81, FR-H85) ─────────

export function StayFinanceReports({ from, to }: { from: string; to: string }) {
  const [tab, setTab] = useState('flash');
  const [day, setDay] = useState(todayISO());
  const flash = useGet<Flash>(tab === 'flash' ? `/api/v1/stay/flash${qs({ date: day })}` : null);
  const tax = useGet<Page<Row>>(tab === 'tax' ? `/api/v1/stay/reports/tax${qs({ from, to })}` : null);
  const dep = useGet<Page<Row>>(tab === 'deposits' ? '/api/v1/stay/reports/deposits' : null);
  const set = useGet<Page<Row>>(tab === 'settlement' ? `/api/v1/stay/reports/settlement${qs({ from, to })}` : null);
  const promo = useGet<Page<Row>>(tab === 'promotions' ? '/api/v1/stay/reports/promotions' : null);
  const pc = useGet<Row & { types: Row[]; total: Row; ledgerCost: string }>(tab === 'profit' ? `/api/v1/stay/profit-center${qs({ from, to })}` : null);
  const csv = (path: string, name: string) => void download('GET', `${path}${path.includes('?') ? '&' : '?'}format=csv`, undefined, name);
  return (
    <div className="oc-stack">
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'flash', label: 'Manager Flash' }, { value: 'profit', label: 'Profit center by type' },
        { value: 'tax', label: 'Hotel tax (PB1)' }, { value: 'deposits', label: 'Deposits & city ledger' }, { value: 'settlement', label: 'Online settlement' },
        { value: 'promotions', label: 'Promotions & campaigns' }]} />
      {tab === 'flash' && (
        <>
          <TextField label="Date" type="date" value={day} onChange={setDay} />
          {flash.data ? <FlashCard f={flash.data} /> : <Skeleton rows={4} />}
          <button className="oc-btn oc-btn-neutral oc-btn-sm" style={{ width: 'fit-content' }}
            onClick={() => csv(`/api/v1/stay/reports/reservations${qs({ from, to })}`, `reservations-${from}.csv`)}><Icon name="download" size={16} /> Reservations of the period (CSV)</button>
        </>
      )}
      {tab === 'profit' && pc.data && (
        <Card title="Accommodation profit center" icon="pie_chart">
          <DataTable rows={[...pc.data.types, pc.data.total]} rowKey={(r) => String(r.typeName)} columns={[
            { key: 'typeName', header: 'Room type', render: (r) => <strong>{String(r.typeName)}</strong> },
            { key: 'sold', header: 'Sold / available', align: 'right', render: (r) => `${String(r.sold)} / ${String(r.available)}` },
            { key: 'occupancy', header: 'Occupancy', align: 'right', render: (r) => `${String(r.occupancy)}%` },
            { key: 'adr', header: 'ADR', align: 'right', render: (r) => money(r.adr) }, { key: 'revpar', header: 'RevPAR', align: 'right', render: (r) => money(r.revpar) },
            { key: 'roomRevenue', header: 'Room', align: 'right', render: (r) => money(r.roomRevenue) },
            { key: 'addonRevenue', header: 'Add-ons', align: 'right', render: (r) => money(r.addonRevenue) },
            { key: 'fnbChargeToRoom', header: 'F&B to room', align: 'right', render: (r) => money(r.fnbChargeToRoom) },
            { key: 'maintenance', header: 'Maintenance', align: 'right', render: (r) => money(r.maintenance) },
            { key: 'sharedCost', header: 'Shared cost', align: 'right', render: (r) => money(r.sharedCost) },
            { key: 'margin', header: 'Margin', align: 'right', render: (r) => <strong>{money(r.margin)}</strong> }]} />
          <p className="oc-small oc-muted">Shared cost: the Bungalow costs of the ledger ({money(pc.data.ledgerCost)}) by room nights sold. ALOS per type in the API.</p>
        </Card>
      )}
      {tab === 'tax' && (
        <Card title="Hotel tax (PB1) per day" icon="account_balance" actions={<button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => csv(`/api/v1/stay/reports/tax${qs({ from, to })}`, 'pb1.csv')}>CSV</button>}>
          <DataTable rows={tax.data?.items} loading={tax.isLoading} rowKey={(r) => `${String(r.date)}${String(r.component)}`} columns={[
            { key: 'date', header: 'Date', render: (r) => formatDate(String(r.date)) }, { key: 'component', header: 'Component', render: (r) => label(String(r.component)) },
            { key: 'net', header: 'Net', align: 'right', render: (r) => money(r.net) }, { key: 'service', header: 'Service', align: 'right', render: (r) => money(r.service) },
            { key: 'tax', header: 'Tax (PB1)', align: 'right', render: (r) => money(r.tax) }, { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) }]} />
        </Card>
      )}
      {tab === 'deposits' && (
        <Card title="Deposits held and city ledger" icon="savings" actions={<button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => csv('/api/v1/stay/reports/deposits', 'deposits.csv')}>CSV</button>}>
          <DataTable rows={dep.data?.items} loading={dep.isLoading} rowKey={(r) => String(r.number)} columns={[
            { key: 'kind', header: 'Kind', render: (r) => (r.kind === 'deposit' ? 'Deposit' : 'City ledger') }, { key: 'stayNo', header: 'Reservation' }, { key: 'guest', header: 'Guest' },
            { key: 'company', header: 'Company' }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) },
            { key: 'applied', header: 'Applied', align: 'right', render: (r) => money(r.applied) }, { key: 'status', header: 'Stay', render: (r) => label(String(r.status)) }]} />
        </Card>
      )}
      {tab === 'settlement' && (
        <Card title="Online payments (mock gateway)" icon="credit_card" actions={<button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => csv(`/api/v1/stay/reports/settlement${qs({ from, to })}`, 'settlement.csv')}>CSV</button>}>
          <DataTable rows={set.data?.items} loading={set.isLoading} rowKey={(r) => String(r.number)} columns={[
            { key: 'created', header: 'Created', render: (r) => formatDateTime(String(r.created)) }, { key: 'number', header: 'Payment' }, { key: 'stayNo', header: 'Reservation' },
            { key: 'method', header: 'Method', render: (r) => label(String(r.method)) }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) },
            { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
        </Card>
      )}
      {tab === 'promotions' && (
        <Card title="Promotion codes and their reservations" icon="campaign">
          <DataTable rows={promo.data?.items} loading={promo.isLoading} rowKey={(r) => String(r.code)} columns={[
            { key: 'code', header: 'Promotion', render: (r) => <>{String(r.name)}<div className="oc-small oc-muted">{String(r.promoCode ?? 'automatic')}</div></> },
            { key: 'campaign', header: 'Campaign', render: (r) => String(r.campaign ?? '—') }, { key: 'reservations', header: 'Reservations', align: 'right' },
            { key: 'nights', header: 'Nights', align: 'right' }, { key: 'discount', header: 'Discount', align: 'right', render: (r) => money(r.discount) },
            { key: 'revenue', header: 'Revenue', align: 'right', render: (r) => money(r.revenue) }]} />
        </Card>
      )}
    </div>
  );
}

/** Kasir of the Stay Front Desk (FR-H59, FR-H60): payments, deposits, refunds and the cashier shift of the stay desk. */
export function StayCashierPage() {
  return <CashierPage defaultStation="stay_desk" />;
}
