import React, { useEffect, useRef, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { useGet, type Schemas } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import { Icon, Skeleton } from '@oneclub/shell';

/*
 * Promo banners of the Member App home (demo feedback 10 Oct 2026 #37.5):
 * a carousel — swipe, slow auto-scroll, dots — of the promotions for this
 * member or non-member (targeted by segment / membership type in Commercial
 * › Promotions), each opening its detail: picture, description, period,
 * terms, for whom, and the action.
 */

type Promo = Schemas['PromotionView'];

function useOffers() {
  return useGet<Schemas['Offers']>('/api/v1/member/offers', { retry: false });
}

const period = (p: Promo) => (p.validFrom || p.validTo ? `${p.validFrom ? formatDate(p.validFrom) : '…'} – ${p.validTo ? formatDate(p.validTo) : '…'}` : '');
const headline = (p: Promo) => (p.discountPercent ? `${Number(p.discountPercent)}% off` : p.discountAmount ? `Rp ${Number(p.discountAmount).toLocaleString('id-ID')} off`
  : p.buyQuantity && p.getQuantity ? `Buy ${p.buyQuantity} get ${p.getQuantity}` : '');

export function PromoCarousel() {
  const offers = useOffers();
  const list = (offers.data?.promotions ?? []).slice(0, 8);
  const strip = useRef<HTMLDivElement>(null);
  const [at, setAt] = useState(0);
  // slow auto-scroll; a swipe or a tap on a dot takes over
  const [paused, setPaused] = useState(false);
  useEffect(() => {
    if (paused || list.length < 2) return undefined;
    const t = window.setInterval(() => setAt((i) => (i + 1) % list.length), 6000);
    return () => window.clearInterval(t);
  }, [paused, list.length]);
  useEffect(() => {
    const el = strip.current;
    if (el) el.scrollTo({ left: at * el.clientWidth, behavior: 'smooth' });
  }, [at]);
  if (!list.length) return null;
  return (
    <section aria-label="Promotions" className="mj-promo" onPointerDown={() => setPaused(true)}>
      <div className="mj-promo-strip" ref={strip} onScroll={(e) => {
        const el = e.currentTarget;
        const i = Math.round(el.scrollLeft / Math.max(el.clientWidth, 1));
        if (i !== at) setAt(i);
      }}>
        {list.map((p) => (
          <Link key={p.id} to={`/promo/${p.id}`} className="mj-promo-slide" style={p.imageUrl ? { backgroundImage: `url(${p.imageUrl})` } : undefined}>
            <span className="mj-promo-text">
              {headline(p) && <span className="mj-promo-tag">{headline(p)}</span>}
              <strong>{p.name}</strong>
              {p.validTo && <span className="mj-small">until {formatDate(p.validTo)}</span>}
            </span>
          </Link>
        ))}
      </div>
      {list.length > 1 && (
        <div className="mj-promo-dots" role="tablist" aria-label="Promotion">
          {list.map((p, i) => <button key={p.id} type="button" role="tab" aria-selected={i === at} aria-label={p.name} onClick={() => { setPaused(true); setAt(i); }} />)}
        </div>
      )}
    </section>
  );
}

const ACTION: Record<string, [string, string]> = { golf: ['Book a tee time', '/book/tee-time'], stay: ['Book a bungalow', '/book/bungalow'],
  pos: ['Order food', '/order-food'], sportclub: ['Sport Club', '/sport-club'] };

/** Promo Detail: picture, description, period, terms, for whom and the action. */
export function PromoDetailPage() {
  const { id = '' } = useParams();
  const nav = useNavigate();
  const offers = useOffers();
  const p = offers.data?.promotions.find((x) => x.id === id);
  if (offers.isLoading) return <Skeleton rows={6} />;
  if (!p) return <div className="mj-card">This promotion is no longer available. <Link to="/">Home</Link></div>;
  const line = p.businessLines.find((b) => ACTION[b]);
  const [label, to] = line ? ACTION[line] : ['Book now', '/book'];
  const forWhom = p.segments.length ? p.segments.map((s) => s.replace(/_/g, ' ')).join(', ') : p.membershipTypes.length ? p.membershipTypes.join(', ') : 'members and guests';
  return (
    <div className="mj-page mj-narrow">
      <button type="button" className="mj-back" onClick={() => nav(-1)}><Icon name="arrow_back" size={18} /> Back</button>
      <div className="mj-promo-hero" style={p.imageUrl ? { backgroundImage: `url(${p.imageUrl})` } : undefined}>
        {headline(p) && <span className="mj-promo-tag">{headline(p)}</span>}
      </div>
      <h1 style={{ margin: 0 }}>{p.name}</h1>
      {p.description && <p style={{ margin: 0 }}>{p.description}</p>}
      <div className="mj-card oc-stack" style={{ gap: 6 }}>
        {period(p) && <div><Icon name="event" size={16} /> Valid {period(p)}</div>}
        <div><Icon name="group" size={16} /> For {forWhom}</div>
        {p.minPurchase && <div><Icon name="payments" size={16} /> Minimum purchase Rp {Number(p.minPurchase).toLocaleString('id-ID')}</div>}
        {p.requiresCode && <div><Icon name="confirmation_number" size={16} /> Use the promo code at checkout</div>}
      </div>
      {p.terms && (
        <div className="mj-card">
          <strong>Terms &amp; conditions</strong>
          <p className="mj-small" style={{ whiteSpace: 'pre-line', marginBottom: 0 }}>{p.terms}</p>
        </div>
      )}
      <Link className="oc-btn oc-btn-primary" to={to}>{label}</Link>
    </div>
  );
}
