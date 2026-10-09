'use client';
import { useState, type ReactNode } from 'react';
import { checkoutHref } from '../pay-link';

/*
 * Website booking flow (roadmap §33, PRD P2 EP-26): Select Service → Date /
 * Time → Availability → Guest Information → Additional Services → Payment →
 * Booking Confirmation. The visitor's data goes to the public API; the
 * honeypot field "website" stays hidden (bot protection).
 */

export interface FieldSpec {
  name: string;
  label: string;
  type?: 'text' | 'date' | 'datetime-local' | 'number' | 'select' | 'textarea' | 'email' | 'tel';
  options?: { value: string; label: string }[];
  required?: boolean;
  initial?: string;
}

type Result = Record<string, unknown>;

async function post(path: string, body: unknown): Promise<Result> {
  const r = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const data = (await r.json().catch(() => ({}))) as Result;
  if (!r.ok) throw new Error(String(data.detail ?? data.title ?? `Error ${r.status}`));
  return data;
}

/** Labels of the booking form in the page language (/id is Indonesian). */
const FORM = {
  en: { guest: 'Guest Information', name: 'Name', phone: 'Phone (WhatsApp)', email: 'E-mail', voucher: 'Voucher code', payment: 'Payment', card: 'Card',
    confirm: 'Booking Confirmation', reference: 'Reference', total: 'Total', due: 'due', complete: 'Complete the payment', va: 'to virtual account',
    open: 'open payment page', paid: 'Payment received — see you soon!' },
  id: { guest: 'Data Pemesan', name: 'Nama', phone: 'Nomor ponsel (WhatsApp)', email: 'E-mail', voucher: 'Kode voucher', payment: 'Pembayaran', card: 'Kartu kredit',
    confirm: 'Konfirmasi Pemesanan', reference: 'Kode', total: 'Total', due: 'sisa', complete: 'Selesaikan pembayaran', va: 'ke virtual account',
    open: 'buka halaman pembayaran', paid: 'Pembayaran diterima — sampai jumpa!' },
};

export function BookingForm({
  title, path, propertyId, fields, build, pay = true, voucher = false, submitLabel = 'Book', done, consent, lang = 'en',
}: {
  title: string;
  path: string;
  propertyId: string;
  fields: FieldSpec[];
  /** Turns the form values into the request body (without guest / property). */
  build: (v: Record<string, string>) => Record<string, unknown>;
  pay?: boolean;
  voucher?: boolean;
  submitLabel?: string;
  done?: (r: Result) => ReactNode;
  /** Marketing consent checkbox label (UU PDP, PRD P3 FR-WEB-P3-02): unticked by default, sent as `consent`. */
  consent?: string;
  lang?: 'id' | 'en';
}) {
  const L = FORM[lang];
  const [v, setV] = useState<Record<string, string>>(() => Object.fromEntries(fields.map((f) => [f.name, f.initial ?? ''])));
  const [guest, setGuest] = useState({ name: '', phone: '', email: '', website: '' });
  const [method, setMethod] = useState('qris');
  const [code, setCode] = useState('');
  const [optIn, setOptIn] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [result, setResult] = useState<Result | null>(null);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      const body: Record<string, unknown> = { propertyId, guest, ...build(v) };
      if (pay) body.payMethod = method;
      if (voucher && code) body.voucherCode = code;
      if (consent) body.consent = optIn;
      setResult(await post(path, body));
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };
  if (result) {
    const co = result.checkout as Result | undefined;
    const online = co?.online as Result | undefined;
    return (
      <div className="w-card" role="status">
        <h2 style={{ marginTop: 0 }}>{L.confirm}</h2>
        {result.reference ? <p>{L.reference} <strong>{String(result.reference)}</strong> · {String(result.status ?? '')}</p> : null}
        {done?.(result)}
        {co && <p>{L.total} {String(co.total)} · {L.due} {String(co.amountDue)}</p>}
        {online && online.status === 'pending' && (
          <p>
            {L.complete}{online.vaNumber ? <> {L.va} <strong>{String(online.vaNumber)}</strong></> : null}
            {online.checkoutUrl ? <> — <a className="w-btn" href={checkoutHref(String(online.checkoutUrl), lang) ?? undefined}>{L.open}</a></> : null}.
          </p>
        )}
        {online && online.status === 'paid' && <p>{L.paid}</p>}
      </div>
    );
  }
  const field = (f: FieldSpec) => {
    const common = { id: f.name, required: f.required, value: v[f.name], onChange: (e: { target: { value: string } }) => setV({ ...v, [f.name]: e.target.value }) };
    if (f.type === 'select') return <select {...common}>{f.options?.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select>;
    if (f.type === 'textarea') return <textarea {...common} rows={3} />;
    return <input {...common} type={f.type ?? 'text'} />;
  };
  return (
    <form className="w-card w-form" onSubmit={submit}>
      <h2 style={{ marginTop: 0 }}>{title}</h2>
      {fields.map((f) => <label key={f.name} htmlFor={f.name}>{f.label}{field(f)}</label>)}
      <fieldset>
        <legend>{L.guest}</legend>
        <label>{L.name}<input required value={guest.name} onChange={(e) => setGuest({ ...guest, name: e.target.value })} /></label>
        <label>{L.phone}<input type="tel" value={guest.phone} onChange={(e) => setGuest({ ...guest, phone: e.target.value })} /></label>
        <label>{L.email}<input type="email" value={guest.email} onChange={(e) => setGuest({ ...guest, email: e.target.value })} /></label>
        <label aria-hidden="true" style={{ position: 'absolute', left: -9999 }}>Website<input tabIndex={-1} autoComplete="off" value={guest.website}
          onChange={(e) => setGuest({ ...guest, website: e.target.value })} /></label>
      </fieldset>
      {consent && (
        <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>
          <input type="checkbox" name="consent" checked={optIn} onChange={(e) => setOptIn(e.target.checked)} style={{ width: 'auto', marginTop: 4 }} />
          <span>{consent}</span>
        </label>
      )}
      {voucher && <label>{L.voucher}<input value={code} onChange={(e) => setCode(e.target.value)} /></label>}
      {pay && (
        <label>{L.payment}
          <select value={method} onChange={(e) => setMethod(e.target.value)}>
            <option value="qris">QRIS</option><option value="virtual_account">Virtual Account</option><option value="card">{L.card}</option>
          </select>
        </label>
      )}
      {error && <p className="w-error" role="alert">{error}</p>}
      <button className="w-btn" disabled={busy || !guest.name || (!guest.phone && !guest.email)}>{busy ? '…' : submitLabel}</button>
    </form>
  );
}

type Opt = { id: string; name: string };
const opts = (xs: Opt[]) => xs.map((x) => ({ value: x.id, label: x.name }));
const iso = (local: string) => (local ? new Date(local).toISOString() : '');

export function ClassRegistration({ propertyId, programs, lang = 'en' }: { propertyId: string; programs: Opt[]; lang?: 'id' | 'en' }) {
  const id = lang === 'id';
  return (
    <BookingForm lang={lang} title={id ? 'Daftar kelas' : 'Register for a class'} path="/api/v1/public/class-enrollments" propertyId={propertyId}
      submitLabel={id ? 'Daftar' : 'Register'}
      fields={[
        { name: 'programId', label: id ? 'Kelas' : 'Class', type: 'select', options: opts(programs), required: true, initial: programs[0]?.id },
        { name: 'birthDate', label: id ? 'Tanggal lahir peserta' : 'Participant date of birth', type: 'date' },
      ]}
      build={(v) => ({ programId: v.programId, birthDate: v.birthDate || undefined })} />
  );
}

export function BungalowBooking({ propertyId, types }: { propertyId: string; types: Opt[] }) {
  return (
    <BookingForm title="Book Bungalow" path="/api/v1/public/stays" propertyId={propertyId} voucher
      fields={[
        { name: 'bungalowTypeId', label: 'Bungalow type', type: 'select', options: opts(types), required: true, initial: types[0]?.id },
        { name: 'arrivalDate', label: 'Arrival', type: 'date', required: true },
        { name: 'departureDate', label: 'Departure', type: 'date', required: true },
        { name: 'ratePlan', label: 'Rate plan (e.g. room only, with breakfast)', initial: '' },
        { name: 'adults', label: 'Adults', type: 'number', initial: '2' },
        { name: 'children', label: 'Children', type: 'number', initial: '0' },
        { name: 'specialRequests', label: 'Additional services / requests', type: 'textarea' },
      ]}
      build={(v) => ({ kind: 'bungalow', bungalowTypeId: v.bungalowTypeId, arrivalDate: v.arrivalDate, departureDate: v.departureDate, ratePlan: v.ratePlan,
        adults: Number(v.adults || 2), children: Number(v.children || 0), specialRequests: v.specialRequests })} />
  );
}

export function VIPRequest({ propertyId, suites }: { propertyId: string; suites: Opt[] }) {
  return (
    <BookingForm title="Request VIP Suite" path="/api/v1/public/stays" propertyId={propertyId} pay={false} submitLabel="Send request"
      fields={[
        { name: 'unitId', label: 'VIP Suite', type: 'select', options: opts(suites), required: true, initial: suites[0]?.id },
        { name: 'start', label: 'Date & time', type: 'datetime-local', required: true },
        { name: 'pax', label: 'Guests', type: 'number', initial: '10' },
        { name: 'specialRequests', label: 'Requests', type: 'textarea' },
      ]}
      build={(v) => ({ kind: 'vip_suite', unitId: v.unitId, start: iso(v.start), pax: Number(v.pax || 1), specialRequests: v.specialRequests })}
      done={() => <p>Our team will confirm your VIP Suite request shortly.</p>} />
  );
}

export function MeetingBooking({ propertyId, rooms }: { propertyId: string; rooms: Opt[] }) {
  return (
    <BookingForm title="Book Meeting Room" path="/api/v1/public/stays" propertyId={propertyId} voucher
      fields={[
        { name: 'unitId', label: 'Meeting room', type: 'select', options: opts(rooms), required: true, initial: rooms[0]?.id },
        { name: 'start', label: 'Date & time', type: 'datetime-local', required: true },
        { name: 'packageCode', label: 'Package (HALF_DAY, FULL_DAY …)', initial: 'FULL_DAY' },
        { name: 'pax', label: 'Participants', type: 'number', initial: '20' },
        { name: 'layout', label: 'Layout', type: 'select', initial: 'classroom', options: ['classroom', 'theater', 'u_shape', 'round_table', 'boardroom', 'cocktail'].map((x) => ({ value: x, label: x.replace('_', ' ') })) },
        { name: 'specialRequests', label: 'Additional services (equipment, catering)', type: 'textarea' },
      ]}
      build={(v) => ({ kind: 'meeting_room', unitId: v.unitId, start: iso(v.start), packageCode: v.packageCode, pax: Number(v.pax || 1), layout: v.layout,
        specialRequests: v.specialRequests })} />
  );
}

export function MembershipApply({ propertyId, types }: { propertyId: string; types: Opt[] }) {
  return (
    <BookingForm title="Apply for membership" path="/api/v1/public/membership-applications" propertyId={propertyId} pay={false} submitLabel="Send application"
      fields={[
        { name: 'typeId', label: 'Membership type', type: 'select', options: opts(types), required: true, initial: types[0]?.id },
        { name: 'birthDate', label: 'Date of birth', type: 'date' },
        { name: 'notes', label: 'Notes', type: 'textarea' },
      ]}
      build={(v) => ({ typeId: v.typeId, birthDate: v.birthDate || undefined, notes: v.notes })}
      done={(r) => <p>Application <strong>{String(r.applicationNo)}</strong> received — our membership team will contact you.</p>} />
  );
}

/** Marketing consent label of the website forms (explicit opt-in, UU PDP). */
export function consentLabel(lang?: string) {
  return lang === 'id'
    ? 'Kirimi saya berita dan penawaran melalui e-mail (opsional). Anda dapat berhenti berlangganan kapan saja.'
    : 'Send me news and offers by e-mail (optional). You can unsubscribe at any time.';
}
