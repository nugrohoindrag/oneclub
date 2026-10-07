import React, { useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import QRCode from 'qrcode';
import { request, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatMoney } from '@oneclub/i18n';
import {
  AuthFrame, Card, DataTable, ErrorAlert, Icon, ProfilePage, SelectField, Skeleton, StatusPill, TextField, useAuth, useToast,
} from '@oneclub/shell';

type R = Record<string, unknown> & { id: string };
const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : formatMoney(String(v)));
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '').replace(/_/g, '-')} />;

function Head({ title, help, actions }: { title: string; help?: string; actions?: React.ReactNode }) {
  return <div className="oc-page-head"><div><h1>{title}</h1>{help && <p>{help}</p>}</div><span className="oc-spacer" />{actions}</div>;
}

/** QR code image of a value. */
export function QR({ value, size = 200 }: { value: string; size?: number }) {
  const [src, setSrc] = useState('');
  useEffect(() => {
    void QRCode.toDataURL(value, { margin: 1, width: size }).then(setSrc);
  }, [value, size]);
  return src ? <img src={src} width={size} height={size} alt="QR code" style={{ background: '#fff', borderRadius: 12, padding: 8 }} /> : null;
}

// ── OTP login (FR-APP-01) ───────────────────────────────────────────────────

export function OtpLoginPage() {
  const { refresh } = useAuth();
  const nav = useNavigate();
  const [email, setEmail] = useState('');
  const [channel, setChannel] = useState('email');
  const [sent, setSent] = useState(false);
  const [code, setCode] = useState('');
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const go = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (!sent) {
        await request('POST', '/api/v1/auth/otp/request', { email, channel });
        setSent(true);
      } else {
        await request('POST', '/api/v1/auth/otp/verify', { email, code });
        await refresh();
        nav('/', { replace: true });
      }
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <AuthFrame>
      <form className="oc-stack" onSubmit={go}>
        <h1 style={{ fontSize: 32 }}>Log in with a code</h1>
        <p className="oc-muted" style={{ margin: 0 }}>{sent ? 'Enter the 6-digit code we sent you.' : 'We send a one-time code to your e-mail or WhatsApp.'}</p>
        <TextField label="E-mail" type="email" value={email} onChange={setEmail} required disabled={sent} autoComplete="username" />
        {!sent && <SelectField label="Send via" value={channel} onChange={setChannel} options={[{ value: 'email', label: 'E-mail' }, { value: 'whatsapp', label: 'WhatsApp' }]} />}
        {sent && <TextField label="Code" inputMode="numeric" maxLength={6} value={code} onChange={setCode} required autoComplete="one-time-code" autoFocus />}
        <ErrorAlert error={error} />
        <button className="oc-btn oc-btn-ink oc-btn-block" disabled={busy || !email || (sent && code.length !== 6)}>{sent ? 'Log in' : 'Send code'}</button>
        <Link className="oc-small" to="/login">Log in with password</Link>
      </form>
    </AuthFrame>
  );
}

// ── Membership (FR-APP-05) ──────────────────────────────────────────────────

const CARD_KEY = 'oneclub.memberCard';

function useMyMembership() {
  const m = useGet<{ profile: R; card?: R; benefits: string[]; clubName: string }>('/api/v1/member/membership');
  useEffect(() => {
    try {
      if (m.data?.card) localStorage.setItem(CARD_KEY, JSON.stringify({ card: m.data.card, name: m.data.profile.name, club: m.data.clubName }));
    } catch { /* ignore */ }
  }, [m.data]);
  return m;
}

export function MyApplications() {
  const apps = useGet<Page<R>>('/api/v1/member/membership-applications');
  if (!apps.data?.items.length) return null;
  return (
    <Card title="My applications" icon="assignment">
      <DataTable rows={apps.data.items} columns={[{ key: 'number', header: 'Application' }, { key: 'typeName', header: 'Type' }, { key: 'status', header: 'Status', render: pill('status') }]} />
    </Card>
  );
}

export function MemberCard({ name, club, card }: { name: string; club: string; card?: R | null }) {
  return (
    <div className="oc-card oc-card-ink" style={{ minHeight: 220, display: 'flex', flexDirection: 'column', gap: 12 }}>
      <div className="oc-row"><strong>{club}</strong><span className="oc-spacer" /><Icon name="contactless" size={24} /></div>
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        {card?.qrToken ? <QR value={`oneclub:card:${String(card.qrToken)}`} size={140} /> : null}
        <div>
          <div className="oc-small" style={{ opacity: 0.7 }}>Digital Member Card</div>
          <div style={{ fontSize: 22, fontWeight: 600 }}>{name}</div>
          {card && <div className="oc-small" style={{ opacity: 0.8 }}>{String(card.cardNumber)} · valid until {card.validUntil ? formatDate(String(card.validUntil)) : '—'}</div>}
        </div>
      </div>
    </div>
  );
}

/** Digital Member Card, available offline from the last copy (FR-APP-05). */
export function CardPage() {
  const m = useMyMembership();
  let cached: { card: R; name: string; club: string } | null = null;
  try {
    cached = JSON.parse(localStorage.getItem(CARD_KEY) ?? 'null');
  } catch { /* ignore */ }
  const card = m.data?.card ?? cached?.card;
  return (
    <div className="oc-stack">
      <Head title="Digital Member Card" help="Show the QR code at check-in. It also works offline." />
      {card ? <MemberCard name={String(m.data?.profile.name ?? cached?.name ?? '')} club={String(m.data?.clubName ?? cached?.club ?? '')} card={card} />
        : m.isLoading ? <Skeleton /> : <p className="oc-muted">No card issued yet.</p>}
    </div>
  );
}

export function BenefitsPage() {
  const m = useMyMembership();
  return (
    <div className="oc-stack">
      <Head title="Membership Benefits" />
      <Card title="Your benefits" icon="workspace_premium"><ul>{(m.data?.benefits ?? []).map((b) => <li key={b}>{b}</li>)}</ul></Card>
    </div>
  );
}

export function FamilyPage() {
  const m = useMyMembership();
  return (
    <div className="oc-stack">
      <Head title="Family Members" />
      <DataTable rows={(m.data?.profile.family as R[]) ?? []} loading={m.isLoading} columns={[{ key: 'name', header: 'Name' }, { key: 'relationship', header: 'Relationship' },
        { key: 'memberNo', header: 'Member No.' }, { key: 'status', header: 'Status', render: pill('status') }]} />
    </div>
  );
}

export function StatementsPage() {
  const list = useGet<Page<R>>('/api/v1/member/statements');
  return (
    <div className="oc-stack">
      <Head title="Membership Statement" />
      <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'periodStart', header: 'Period', render: (s) => `${String(s.periodStart)} – ${String(s.periodEnd)}` },
        { key: 'totalCharges', header: 'Charges', align: 'right', render: (s) => money(s.totalCharges) }, { key: 'totalPayments', header: 'Payments', align: 'right', render: (s) => money(s.totalPayments) },
        { key: 'closingBalance', header: 'Balance', align: 'right', render: (s) => money(s.closingBalance) }]}
        actions={(s) => (s.hasPdf ? <a className="oc-btn oc-btn-sm oc-btn-text" href={`/api/v1/member/statements/${s.id}/pdf`} target="_blank" rel="noreferrer">PDF</a> : null)} />
    </div>
  );
}

// ── Transactions (FR-APP-06) ────────────────────────────────────────────────

export function MyPaymentsPage() {
  const list = useGet<Page<R>>('/api/v1/member/payments');
  return (
    <div className="oc-stack">
      <Head title="Payments" />
      <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'number', header: 'Payment' }, { key: 'methodType', header: 'Method', render: (p) => String(p.methodType).replace(/_/g, ' ') },
        { key: 'amount', header: 'Amount', align: 'right', render: (p) => money(p.amount) }, { key: 'paidAt', header: 'Paid', render: (p) => (p.paidAt ? formatDateTime(String(p.paidAt)) : '—') },
        { key: 'status', header: 'Status', render: pill('status') }]}
        actions={(p) => (p.status === 'completed' ? <a className="oc-btn oc-btn-sm oc-btn-text" href={`/api/v1/member/payments/${p.id}/receipt`} target="_blank" rel="noreferrer">Receipt</a> : null)} />
    </div>
  );
}

export function MyChargesPage() {
  const list = useGet<Page<R>>('/api/v1/member/member-charges');
  return (
    <div className="oc-stack">
      <Head title="Member Charges" />
      <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'occurredAt', header: 'When', render: (e) => formatDateTime(String(e.occurredAt)) },
        { key: 'description', header: 'Description' }, { key: 'amount', header: 'Amount', align: 'right', render: (e) => money(e.amount) }]} />
    </div>
  );
}

// ── Profile (FR-APP-07) ─────────────────────────────────────────────────────

export function MemberProfilePage() {
  const p = useGet<R>('/api/v1/member/profile');
  const [phone, setPhone] = useState<string | null>(null);
  const [optIn, setOptIn] = useState<boolean | null>(null);
  const save = useSend<Record<string, unknown>>('PATCH', '/api/v1/member/profile', ['/api/v1/member/profile']);
  const toast = useToast();
  return (
    <div className="oc-stack">
      <ProfilePage />
      {p.data && (
        <Card title="Member profile" icon="badge">
          <div className="oc-form">
            <TextField label="Phone" value={phone ?? String(p.data.phone ?? '')} onChange={setPhone} />
            <SelectField label="Club news and offers" value={String(optIn ?? Boolean(p.data.marketingOptIn))} onChange={(v) => setOptIn(v === 'true')}
              options={[{ value: 'true', label: 'Yes, send me news' }, { value: 'false', label: 'No' }]} />
          </div>
          <button className="oc-btn oc-btn-ink" disabled={save.isPending} onClick={() => save.mutate({ phone: phone ?? undefined, marketingOptIn: optIn ?? undefined, consent: true },
            { onSuccess: () => toast('Profile saved') })}>Save</button>
          <ErrorAlert error={save.error} />
        </Card>
      )}
    </div>
  );
}
