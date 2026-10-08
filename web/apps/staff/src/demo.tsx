import React from 'react';
import { Link, useSearchParams } from 'react-router';
import { useGet } from '@oneclub/api-client';
import { Icon, currentSurface, logoOf, surfaceUrl, useBootstrap } from '@oneclub/shell';
import './demo.css';

/*
 * Demo access (/demo, demo instances only): the presenter picks a business,
 * then the person to demo as, and lands on that person's login page with the
 * e-mail filled in (password and PIN are the demo ones). The accounts come
 * from GET /api/v1/public/demo-access, which answers 404 on production or
 * without the demo seed; a seat whose account does not exist is left out.
 * Each person opens on the domain of their area: dashboard (office),
 * cashier (front desk, starter, POS), caddy (tablet) or the Member App.
 */

/** "Demo access" under the login form, only where the demo accounts exist. */
export function DemoLink() {
  const q = useGet<{ accounts: unknown[] }>('/api/v1/public/demo-access', { retry: false, staleTime: 5 * 60_000 });
  if (!q.data?.accounts.length) return null;
  return <Link className="oc-small" to="/demo" style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}><Icon name="play_circle" size={18} /> Demo access</Link>;
}

type Surface = 'dashboard' | 'cashier' | 'caddy' | 'member';
interface Seat { email: string; role: string; area: string; surface: Surface }
interface Category { code: string; title: string; sub: string; image: string; seats: Seat[] }

const d = (local: string) => `${local}@demo.oneclub.id`;
const MEMBER: Seat = { email: d('member'), role: 'Member', area: 'Member App: booking, bill, activity', surface: 'member' };

const CATEGORIES: Category[] = [
  { code: 'golf', title: 'Golf', sub: 'Tee sheet, front desk, starter, caddy tablet, POS and member app', image: '/demo/golf.jpg', seats: [
    { email: d('golf.manager'), role: 'Golf Manager', area: 'Management Dashboard & Back Office', surface: 'dashboard' },
    { email: d('golf.admin'), role: 'Golf Admin', area: 'Back Office: tee sheet, rates, members', surface: 'dashboard' },
    { email: d('front.desk'), role: 'Front Desk & Caddy Master', area: 'Front Desk: booking, check-in, caddy, bill', surface: 'cashier' },
    { email: d('starter'), role: 'Starter / Marshal', area: 'Starter & Course Monitor', surface: 'cashier' },
    { email: d('caddy'), role: 'Caddy', area: 'Caddy Tablet: score, timer, cart view', surface: 'caddy' },
    { email: d('cashier'), role: 'Cashier', area: 'POS Cashier: golfer bill', surface: 'cashier' },
    { email: d('pos.proshop'), role: 'Pro Shop', area: 'POS Pro Shop', surface: 'cashier' },
    { email: d('pos.halfway'), role: 'Halfway House', area: 'POS Halfway House & Tee House', surface: 'cashier' },
    MEMBER,
  ] },
  { code: 'sportclub', title: 'Sport Club', sub: 'Courts, pool, classes, coaches and the sport café', image: '/demo/sportclub.jpg', seats: [
    { email: d('sport.manager'), role: 'Sport Club Manager', area: 'Management Dashboard & Back Office', surface: 'dashboard' },
    { email: d('sport.reception'), role: 'Sport Club Receptionist', area: 'Front Desk: entry, court booking, classes', surface: 'cashier' },
    { email: d('coach'), role: 'Instructor / Coach', area: 'Classes and attendance', surface: 'cashier' },
    { email: d('pos.sport'), role: 'Sport Café', area: 'POS Sport Café', surface: 'cashier' },
    MEMBER,
  ] },
  { code: 'bungalow', title: 'Bungalow', sub: 'Room types, stays, check-in and check-out', image: '/demo/bungalow.jpg', seats: [
    { email: d('resort.manager'), role: 'Resort Manager', area: 'Management Dashboard & Back Office', surface: 'dashboard' },
    { email: d('reservation'), role: 'Reservation Staff', area: 'Back Office: stays and reservations', surface: 'dashboard' },
    { email: d('front.desk'), role: 'Front Desk', area: 'Front Desk: check-in, check-out, bill', surface: 'cashier' },
    MEMBER,
  ] },
  { code: 'vip-suite', title: 'VIP Suite', sub: 'Suite blocks, requests and overtime', image: '/demo/vip-suite.jpg', seats: [
    { email: d('resort.manager'), role: 'Resort Manager', area: 'Management Dashboard & Back Office', surface: 'dashboard' },
    { email: d('reservation'), role: 'Reservation Staff', area: 'Back Office: VIP suite bookings', surface: 'dashboard' },
    MEMBER,
  ] },
  { code: 'wedding', title: 'Wedding, MICE & Banquet', sub: 'Inquiry to event day: quotation, BEO, venue and F&B', image: '/demo/wedding.jpg', seats: [
    { email: d('banquet.manager'), role: 'Banquet Manager', area: 'Management Dashboard & Back Office', surface: 'dashboard' },
    { email: d('banquet.sales'), role: 'Banquet Sales', area: 'Back Office: inquiries, quotations, events', surface: 'dashboard' },
    { email: d('sales'), role: 'Sales Executive', area: 'Back Office: leads and corporate deals', surface: 'dashboard' },
    { email: d('outlet.manager'), role: 'Outlet Manager', area: 'F&B, kitchen display and POS', surface: 'dashboard' },
  ] },
];

/** Login URL of a seat: its domain on a deployed demo (one domain in development: every staff area by path). */
function loginUrl(s: Seat) {
  const q = `/login?email=${encodeURIComponent(s.email)}`;
  if (s.surface !== 'member') return surfaceUrl(s.surface, q) ?? q;
  // Member App: member.<domain> next to dashboard.<domain>; the Vite dev server on :5174 locally.
  const { protocol, hostname } = window.location;
  const base = currentSurface() ? hostname.split('.').slice(1).join('.') : '';
  return base && base !== 'localhost' ? `${protocol}//member.${base}${q}` : `${protocol}//localhost:5174${q}`;
}

const initials = (name: string) => name.split(/\s+/).filter(Boolean).slice(0, 2).map((w) => w[0]).join('').toUpperCase();
const SURFACE_LABEL: Record<Surface, string> = { dashboard: 'Dashboard', cashier: 'Cashier', caddy: 'Caddy Tablet', member: 'Member App' };

export function DemoAccessPage() {
  const b = useBootstrap();
  const [params, setParams] = useSearchParams();
  const q = useGet<{ accounts: { email: string; name: string }[] }>('/api/v1/public/demo-access', { retry: false });
  const names = new Map((q.data?.accounts ?? []).map((a) => [a.email, a.name]));
  const cats = CATEGORIES.map((c) => ({ ...c, seats: c.seats.filter((s) => names.has(s.email)) })).filter((c) => c.seats.length > 0);
  const chosen = cats.find((c) => c.code === params.get('c'));
  const pick = (code: string) => {
    setParams({ c: code }, { replace: true });
    requestAnimationFrame(() => document.getElementById('demo-people')?.scrollIntoView({ behavior: 'smooth', block: 'start' }));
  };
  return (
    <div className="oc-demo">
      <header className="oc-demo-head">
        <img src={logoOf(b.branding)} alt="" className="oc-demo-logo" />
        <div>
          <h1>{b.branding.appName}</h1>
          <p>Demo access · choose a business, then the person to demo as</p>
        </div>
      </header>

      {q.isLoading && <p className="oc-demo-note">Loading…</p>}
      {q.error && <div className="oc-demo-empty"><Icon name="lock" size={28} /><p>Demo access is not available on this system.</p></div>}

      {cats.length > 0 && (
        <>
          <ol className="oc-demo-steps"><li data-on={!chosen || undefined}>Business</li><li data-on={!!chosen || undefined}>Person</li><li>Log in</li></ol>
          <section className="oc-demo-cards" aria-label="Business">
            {cats.map((c) => (
              <button key={c.code} type="button" className="oc-demo-card" data-on={chosen?.code === c.code || undefined} onClick={() => pick(c.code)}>
                <img src={c.image} alt="" loading="lazy" />
                <span className="oc-demo-card-text">
                  <strong>{c.title}</strong>
                  <small>{c.sub}</small>
                  <em>{c.seats.length} account{c.seats.length === 1 ? '' : 's'}</em>
                </span>
              </button>
            ))}
          </section>

          <section id="demo-people" className="oc-demo-people" aria-label="Person">
            {!chosen ? <p className="oc-demo-note">Pick a business above to see its demo accounts.</p> : (
              <>
                <h2>{chosen.title} · pick a person</h2>
                <div className="oc-demo-grid">
                  {chosen.seats.map((s) => (
                    <a key={s.email} className="oc-demo-person" href={loginUrl(s)}>
                      <span className="oc-demo-avatar" data-surface={s.surface}>{initials(names.get(s.email) ?? s.role)}</span>
                      <span className="oc-demo-person-text">
                        <strong>{names.get(s.email)}</strong>
                        <span>{s.role}</span>
                        <small>{s.area}</small>
                        <code>{s.email}</code>
                      </span>
                      <span className="oc-demo-chip" data-surface={s.surface}>{SURFACE_LABEL[s.surface]}</span>
                      <Icon name="arrow_forward" size={20} />
                    </a>
                  ))}
                </div>
                <p className="oc-demo-note">
                  The login opens with the e-mail filled in; enter the demo password. Cashier and caddy domains ask once to register the device,
                  then log in with the e-mail and the staff PIN.
                </p>
              </>
            )}
          </section>
        </>
      )}
    </div>
  );
}
