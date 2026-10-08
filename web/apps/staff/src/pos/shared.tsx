import React, { useEffect } from 'react';
import { qs, type Page, type Schemas } from '@oneclub/api-client';
import { formatNumber, useTranslation } from '@oneclub/i18n';
import { Icon } from '@oneclub/shell';
import { OUTLET_KEY, read } from '../offline';
import { useCachedGet } from './offline';

// Shared by the POS Cashier screens (pos/*).

export type Order = Schemas['Order'];
export type OrderLine = Schemas['OrderLine'];
export type MenuItem = Schemas['MenuItem'] & { stockTracked?: boolean; available?: string | null; soldOut?: boolean };
export type TableState = Schemas['TableState'];
export type Row = Record<string, unknown>;

export const money = (v: unknown) => `Rp ${formatNumber(Math.round(Number(v ?? 0)))}`;

/** The outlet of this terminal (chosen on the POS, kept per device). */
export function useOutletId() {
  return read(OUTLET_KEY);
}

export function useOutlet(id: string) {
  return useCachedGet<Row>(id ? `/api/v1/commercial/outlets/${id}` : null, `outlet:${id}`, { staleTime: 5 * 60_000 });
}

/** The menu of the outlet now (photos, member prices, Sold Out). */
export function useMenu(outletId: string) {
  return useCachedGet<Page<MenuItem>>(outletId ? `/api/v1/commercial/outlets/${outletId}/menu` : null, `menu:${outletId}`, { refetchInterval: 60_000 });
}

/** The open POS shift of the outlet (payments need one). */
export function useShift(outletId: string) {
  const q = useCachedGet<Page<Row>>(outletId ? `/api/v1/commercial/shifts${qs({ 'filter[status]': 'open', 'filter[outletId]': outletId })}` : null, `shift:${outletId}`);
  return { ...q, shift: q.data?.items.find((s) => s.outletId === outletId) };
}

/** Kitchen state of an order (KDS), as the POS shows it. */
export const KITCHEN: Record<string, string> = {
  new: 'Not sent', sent: 'In the kitchen', preparing: 'Preparing', ready: 'Ready to serve', out_for_delivery: 'On the way', served: 'Served',
};

/** "Mon, 31 Jan 2022" in the UI language. */
export function TodayLabel() {
  const { i18n } = useTranslation();
  const label = new Intl.DateTimeFormat(i18n.language, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' }).format(new Date());
  return <span className="pos-date"><Icon name="calendar_today" size={18} />{label}</span>;
}

/** Re-render every `ms` (table timers). */
export function useTick(ms: number) {
  const [, set] = React.useState(0);
  useEffect(() => {
    const t = window.setInterval(() => set((n) => n + 1), ms);
    return () => window.clearInterval(t);
  }, [ms]);
}

/** Elapsed time since `iso` as hh:mm. */
export function elapsed(iso: string) {
  const min = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 60_000));
  return `${String(Math.floor(min / 60)).padStart(2, '0')}:${String(min % 60).padStart(2, '0')}`;
}

/** Product photo, or an icon of its type. */
export function ProductImage({ item, className }: { item?: { imageUrl?: string | null; productType?: string }; className: string }) {
  if (item?.imageUrl) return <img className={className} src={item.imageUrl} alt="" loading="lazy" />;
  const icon = item?.productType === 'beverage' ? 'local_cafe' : item?.productType === 'retail' ? 'sports_golf' : 'restaurant';
  return <span className={className} aria-hidden="true"><Icon name={icon} size={28} /></span>;
}

/** Category icon of the menu filter. */
export function categoryIcon(c: string) {
  const k = c.toLowerCase();
  if (/minum|drink|beverage|coffee|kopi/.test(k)) return 'local_cafe';
  if (/bar|beer|bir|wine/.test(k)) return 'local_bar';
  if (/dessert|cake|kue|snack/.test(k)) return 'cake';
  if (/shop|retail|golf/.test(k)) return 'sports_golf';
  return 'restaurant';
}

/** Dialog frame of the POS design. */
export function PosDialog({ title, sub, onClose, children, label, art }: {
  title: string; sub?: string; onClose: () => void; children: React.ReactNode; label?: string; art?: React.ReactNode;
}) {
  useEffect(() => {
    const k = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', k);
    return () => window.removeEventListener('keydown', k);
  }, [onClose]);
  return (
    <div className="pos-scrim pos-vars" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div className="pos-dialog" role="dialog" aria-modal="true" aria-label={label ?? title}>
        <button className="pos-dialog-close" onClick={onClose} aria-label="Close"><Icon name="close" size={22} /></button>
        {art}
        <h2 className="pos-dialog-title">{title}</h2>
        {sub && <p className="pos-dialog-sub">{sub}</p>}
        {children}
      </div>
    </div>
  );
}
