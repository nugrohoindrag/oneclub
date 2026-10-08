import React, { useEffect, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { request, useGet, type Page, type Schemas } from '@oneclub/api-client';
import { Checkbox, ErrorAlert, Icon, SelectField, Skeleton, TextArea, TextField, useBootstrap } from '@oneclub/shell';
import { MethodPicker, SandboxGateway, type PayMethod } from './pay';
import { Chip, money, Rows, Steps } from './ui';

// Discover → Become a Member, before the member has an account: membership
// benefits and packages, the application, then following it with its
// number and e-mail — approval by the club, the joining fee paid online
// (mock gateway in sandbox), and the activation e-mail of the Member App.

type PublicType = Schemas['PublicType'];
type Track = Schemas['ApplicationTrack'];

function useProperty() {
  const boot = useBootstrap();
  return boot.properties?.[0]?.id ?? '';
}

function JoinFrame({ children }: { children: React.ReactNode }) {
  const boot = useBootstrap();
  return (
    <div className="mj-join">
      <div className="mj-join-top">
        {boot.branding.logoUrl ? <img src={boot.branding.logoUrl} alt="" /> : <span className="mj-icon"><Icon name="sports_golf" size={22} /></span>}
        <strong>{boot.branding.appName}</strong>
        <span className="oc-spacer" />
        <Link className="oc-btn oc-btn-sm oc-btn-neutral" to="/join/status">Track application</Link>
        <Link className="oc-btn oc-btn-sm oc-btn-outline" to="/login">Member login</Link>
      </div>
      <div className="mj-page">{children}</div>
    </div>
  );
}

/** Member benefits in words (Entitlements). */
export function benefitLines(b: Record<string, unknown> | undefined): string[] {
  if (!b) return [];
  const out: string[] = [];
  if (b.memberRate) out.push('Member Rate on golf');
  if (b.golf) out.push('Golf course access');
  if (typeof b.bookingWindowDays === 'number' && b.bookingWindowDays > 0) out.push(`Preferred tee times: book up to ${b.bookingWindowDays} days ahead`);
  if (typeof b.guestQuotaPerMonth === 'number' && b.guestQuotaPerMonth > 0) out.push(`Bring up to ${b.guestQuotaPerMonth} guests a month at the guest-of-member rate`);
  if (b.memberCharge) out.push('Sign bills to your member account');
  if (b.freeEntry) out.push('Free entry to club facilities');
  if (Array.isArray(b.facilityAccess) && b.facilityAccess.length) out.push(`Facility access: ${b.facilityAccess.includes('*') ? 'all facilities' : b.facilityAccess.join(', ')}`);
  if (b.classDiscountPercent && Number(b.classDiscountPercent) > 0) out.push(`${b.classDiscountPercent}% off classes`);
  return out;
}

const PERIOD = (p: Schemas['PublicPackage']) => `${p.periodCount} ${p.periodUnit}${p.periodCount === 1 ? '' : 's'}`;

export function JoinPage() {
  const property = useProperty();
  const types = useGet<Page<PublicType>>(property ? `/api/v1/public/membership-types?propertyId=${property}` : null);
  const boot = useBootstrap();
  return (
    <JoinFrame>
      <section className="mj-hero">
        <h1>Become a Member of {boot.branding.appName}</h1>
        <p className="mj-muted" style={{ margin: '6px 0 0', maxWidth: 560, position: 'relative', zIndex: 1 }}>
          Preferred tee times, member rates for you and your guests, resort stays and club events — your digital companion from booking to the 18th hole.
        </p>
        <div className="mj-hero-row">
          {['Preferred Tee Time', 'Guest privileges', 'Member account', 'Events & tournaments'].map((x) => <span key={x} className="mj-hero-tag">✓ {x}</span>)}
        </div>
      </section>
      <h2 className="mj-section-title">Membership Packages</h2>
      {types.isLoading && <Skeleton rows={4} />}
      <ErrorAlert error={types.error} />
      {types.data?.items.length === 0 && <p className="mj-muted">Membership applications are not open online. Please contact the club.</p>}
      <div className="mj-grid-2">
        {(types.data?.items ?? []).map((t) => {
          const cheapest = [...t.packages].sort((a, b) => Number(a.joiningFee) + Number(a.periodFee) - Number(b.joiningFee) - Number(b.periodFee))[0];
          return (
            <div key={t.id} className="mj-card mj-plan">
              <div className="oc-row"><span className="mj-icon"><Icon name="workspace_premium" size={20} /></span><div style={{ flex: 1 }}><span className="mj-small mj-muted">{t.program}</span><h2 style={{ margin: 0 }}>{t.name}</h2></div><Chip tone="info">{t.category}</Chip></div>
              {cheapest && <div><span className="mj-price mj-num">{money(Number(cheapest.periodFee))}</span> <span className="mj-muted">/ {PERIOD(cheapest)}</span>
                {Number(cheapest.joiningFee) > 0 && <div className="mj-small mj-muted">+ joining fee {money(cheapest.joiningFee)}</div>}</div>}
              <ul>{benefitLines(t.benefits as Record<string, unknown>).map((b) => <li key={b}>{b}</li>)}</ul>
              <Link className="oc-btn oc-btn-primary" to={`/join/apply/${t.id}`}>Apply for {t.name}</Link>
            </div>
          );
        })}
      </div>
    </JoinFrame>
  );
}

export function ApplyPage() {
  const { typeId } = useParams();
  const property = useProperty();
  const nav = useNavigate();
  const types = useGet<Page<PublicType>>(property ? `/api/v1/public/membership-types?propertyId=${property}` : null);
  const t = types.data?.items.find((x) => x.id === typeId);
  const [f, setF] = useState({ name: '', email: '', phone: '', birthDate: '', packageId: '', notes: '' });
  const [consent, setConsent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const pkg = f.packageId || t?.packages[0]?.id || '';
  const chosen = t?.packages.find((p) => p.id === pkg);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const r = await request<Schemas['PublicApplicationResult']>('POST', '/api/v1/public/membership-applications', {
        propertyId: property, guest: { name: f.name, email: f.email, phone: f.phone }, typeId, packageId: pkg || undefined, birthDate: f.birthDate || undefined, notes: f.notes || undefined,
      });
      nav(`/join/status?no=${encodeURIComponent(r.applicationNo)}&email=${encodeURIComponent(f.email)}`);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <JoinFrame>
      <Link className="mj-back" to="/join"><Icon name="arrow_back" size={18} /> Membership</Link>
      <Steps steps={['Package', 'Your details', 'Approval', 'Payment', 'Activated']} current={1} />
      {!t && <Skeleton rows={6} />}
      {t && (
        <form className="mj-grid-2" onSubmit={(e) => void submit(e)}>
          <div className="mj-card oc-stack">
            <h2><Icon name="person" size={20} /> Registration</h2>
            <TextField label="Full name" value={f.name} onChange={(v) => setF({ ...f, name: v })} required autoComplete="name" />
            <TextField label="E-mail" type="email" value={f.email} onChange={(v) => setF({ ...f, email: v })} required autoComplete="email" help="You follow your application and activate your Member App account with it." />
            <TextField label="Phone / WhatsApp" value={f.phone} onChange={(v) => setF({ ...f, phone: v })} required autoComplete="tel" inputMode="tel" />
            <TextField label="Date of birth" type="date" value={f.birthDate} onChange={(v) => setF({ ...f, birthDate: v })} />
            {t.packages.length > 1 && <SelectField label="Package" value={pkg} onChange={(v) => setF({ ...f, packageId: v })}
              options={t.packages.map((p) => ({ value: p.id, label: `${p.name} · ${PERIOD(p)} · ${money(Number(p.joiningFee) + Number(p.periodFee))}` }))} />}
            <TextArea label="Notes (optional)" value={f.notes} onChange={(v) => setF({ ...f, notes: v })} rows={3} />
            <Checkbox label="I agree that the club processes my data to review this application." checked={consent} onChange={setConsent} />
            <ErrorAlert error={error} />
            <button className="oc-btn oc-btn-primary oc-btn-block" disabled={busy || !consent}>Submit Application</button>
          </div>
          <div className="mj-card mj-plan" style={{ alignSelf: 'start' }}>
            <h2><Icon name="workspace_premium" size={20} /> {t.name}</h2>
            {chosen && <Rows rows={[['Package', chosen.name], ['Period', PERIOD(chosen)], ['Joining fee', money(chosen.joiningFee)], ['Membership fee', money(chosen.periodFee)],
              [<strong>Total on approval</strong>, <strong className="mj-num">{money(Number(chosen.joiningFee) + Number(chosen.periodFee))}</strong>]]} />}
            <ul>{benefitLines(t.benefits as Record<string, unknown>).map((b) => <li key={b}>{b}</li>)}</ul>
            <p className="mj-small mj-muted" style={{ margin: 0 }}>The club reviews your application. Once approved you pay online here and your membership is activated.</p>
          </div>
        </form>
      )}
    </JoinFrame>
  );
}

const STAGE: Record<string, number> = { draft: 2, pending: 2, approved: 3, completed: 4, rejected: 2, cancelled: 2 };

export function ApplicationStatusPage() {
  const [params] = useSearchParams();
  const [no, setNo] = useState(params.get('no') ?? '');
  const [email, setEmail] = useState(params.get('email') ?? '');
  const [track, setTrack] = useState<Track | null>(null);
  const [method, setMethod] = useState<PayMethod>('qris');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const load = async (quiet = false) => {
    if (!no || !email) return;
    if (!quiet) { setBusy(true); setError(null); }
    try {
      setTrack(await request<Track>('POST', `/api/v1/public/membership-applications/${encodeURIComponent(no)}:track`, { email }));
    } catch (e) {
      if (!quiet) setError(e);
    } finally {
      if (!quiet) setBusy(false);
    }
  };
  useEffect(() => { if (params.get('no') && params.get('email')) void load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps
  // follow the approval and the payment
  useEffect(() => {
    if (!track || track.status === 'completed' || track.status === 'rejected' || track.status === 'cancelled') return;
    const t = setInterval(() => void load(true), track.payment ? 3000 : 15000);
    return () => clearInterval(t);
  }, [track]); // eslint-disable-line react-hooks/exhaustive-deps
  const pay = async () => {
    setBusy(true);
    setError(null);
    try {
      setTrack(await request<Track>('POST', `/api/v1/public/membership-applications/${encodeURIComponent(no)}:pay-online`, { email, method }));
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const p = track?.payment;
  return (
    <JoinFrame>
      <div className="mj-head"><div><h1>My Application</h1><p>Follow your membership application.</p></div></div>
      {!track && (
        <form className="mj-card oc-stack mj-narrow" onSubmit={(e) => { e.preventDefault(); void load(); }}>
          <TextField label="Application number" value={no} onChange={setNo} required />
          <TextField label="E-mail" type="email" value={email} onChange={setEmail} required />
          <ErrorAlert error={error} />
          <button className="oc-btn oc-btn-primary" disabled={busy}>Find my application</button>
        </form>
      )}
      {track && (
        <>
          <Steps steps={['Package', 'Your details', 'Approval', 'Payment', 'Activated']} current={STAGE[track.status] ?? 2} />
          <ErrorAlert error={error} />
          <div className="mj-grid-2">
            <div className="mj-card">
              <h2><Icon name="assignment" size={20} /> {track.typeName}</h2>
              <Rows rows={[['Application', <span className="mj-code">{track.applicationNo}</span>], ['Package', track.packageName], ['Fee', money(track.fee)],
                ['Status', <Chip tone={track.status === 'completed' || track.status === 'approved' ? 'ok' : track.status === 'rejected' || track.status === 'cancelled' ? 'bad' : 'warn'}>
                  {track.status === 'pending' || track.status === 'draft' ? 'Waiting for approval' : track.status === 'approved' ? 'Approved — payment due' : track.status === 'completed' ? 'Membership active' : track.status}</Chip>]]} />
              {track.decisionReason && <p className="mj-small mj-muted">{track.decisionReason}</p>}
            </div>
            <div className="mj-card oc-stack">
              {(track.status === 'pending' || track.status === 'draft') && (
                <><h2><Icon name="hourglass_top" size={20} /> Approval</h2><p className="mj-muted" style={{ margin: 0 }}>The club is reviewing your application. This page updates by itself; we also e-mail you once it is decided.</p></>
              )}
              {track.status === 'approved' && !p && (
                <>
                  <h2><Icon name="payments" size={20} /> Membership Fee</h2>
                  <div className="mj-price mj-num">{money(track.outstanding ?? track.fee)}</div>
                  <MethodPicker value={method} onChange={setMethod} memberCharge={false} />
                  <button className="oc-btn oc-btn-primary" disabled={busy} onClick={() => void pay()}>Pay now</button>
                </>
              )}
              {track.status === 'approved' && p && (
                <div className="oc-stack" style={{ alignItems: 'center' }}>
                  <div className="mj-price mj-num">{money(p.amount)}</div>
                  <Chip tone="warn">Waiting for payment</Chip>
                  {p.vaNumber && <span className="mj-code">VA {p.vaNumber}</span>}
                  <SandboxGateway number={p.number} onDone={() => void load(true)} />
                  <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setTrack({ ...track, payment: null })}>Choose another method</button>
                </div>
              )}
              {track.status === 'completed' && (
                <div className="mj-success">
                  <span className="mj-success-mark"><Icon name="check" size={34} /></span>
                  <h1>Membership Activated</h1>
                  {track.memberNo && <div>Member ID <span className="mj-code">{track.memberNo}</span></div>}
                  <p className="mj-muted" style={{ margin: 0 }}>We sent an activation e-mail to {email}. Set your password there, then log in to the Member App.</p>
                  <Link className="oc-btn oc-btn-primary" to="/login">Go to login</Link>
                </div>
              )}
              {(track.status === 'rejected' || track.status === 'cancelled') && <p className="mj-muted">This application is {track.status}. Please contact the club for details.</p>}
            </div>
          </div>
        </>
      )}
    </JoinFrame>
  );
}
