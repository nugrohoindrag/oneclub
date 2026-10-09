import React, { useContext, useEffect, useId, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useNavigate } from 'react-router';
import { Icon, MoneyInput, type MoneyInputProps } from '@oneclub/ui';
import { Amount, Delta } from './dash';
import { ApiError, useGet } from '@oneclub/api-client';
import { useTranslation, validationMessage } from '@oneclub/i18n';

export { Icon };

// ── status pill (Naming Convention §31; FR-SH-09) ─────────────────────────

/** Colour of a status pill. */
export type StatusTone = 'success' | 'warning' | 'error' | 'info' | 'neutral';

const STATUS: Record<string, { label: string; tone: StatusTone }> = {
  draft: { label: 'Draft', tone: 'neutral' },
  pending: { label: 'Pending', tone: 'warning' },
  // Service-level states (first response, complaint SLA).
  met: { label: 'Met', tone: 'success' },
  overdue: { label: 'Overdue', tone: 'error' },
  breached: { label: 'Breached', tone: 'error' },
  escalated: { label: 'Escalated', tone: 'warning' },
  // Priorities (complaints, tasks).
  high: { label: 'High', tone: 'warning' },
  urgent: { label: 'Urgent', tone: 'error' },
  critical: { label: 'Critical', tone: 'error' },
  // Lead outcomes.
  qualified: { label: 'Qualified', tone: 'info' },
  converted: { label: 'Converted', tone: 'success' },
  waiting: { label: 'Waiting', tone: 'neutral' },
  confirmed: { label: 'Confirmed', tone: 'success' },
  approved: { label: 'Approved', tone: 'success' },
  rejected: { label: 'Rejected', tone: 'error' },
  cancelled: { label: 'Cancelled', tone: 'error' },
  skipped: { label: 'Skipped', tone: 'neutral' },
  rescheduled: { label: 'Rescheduled', tone: 'info' },
  'checked-in': { label: 'Checked-in', tone: 'success' },
  'checked-out': { label: 'Checked-out', tone: 'neutral' },
  available: { label: 'Available', tone: 'success' },
  reserved: { label: 'Reserved', tone: 'info' },
  occupied: { label: 'Occupied', tone: 'warning' },
  ready: { label: 'Ready', tone: 'success' },
  'not-ready': { label: 'Not Ready', tone: 'warning' },
  'in-use': { label: 'In Use', tone: 'info' },
  maintenance: { label: 'Maintenance', tone: 'warning' },
  'out-of-service': { label: 'Out of Service', tone: 'error' },
  active: { label: 'Active', tone: 'success' },
  inactive: { label: 'Inactive', tone: 'neutral' },
  expired: { label: 'Expired', tone: 'neutral' },
  suspended: { label: 'Suspended', tone: 'error' },
  finalized: { label: 'Finalized', tone: 'success' },
  refunded: { label: 'Refunded', tone: 'info' },
  completed: { label: 'Completed', tone: 'success' },
  // technical states shown in admin screens
  sent: { label: 'Sent', tone: 'success' },
  failed: { label: 'Failed', tone: 'error' },
  discarded: { label: 'Failed', tone: 'error' },
  retryable: { label: 'Retrying', tone: 'warning' },
  running: { label: 'Running', tone: 'info' },
  scheduled: { label: 'Scheduled', tone: 'neutral' },
  verified: { label: 'Verified', tone: 'info' },
  processed: { label: 'Completed', tone: 'success' },
  accepted: { label: 'Completed', tone: 'success' },
  duplicate: { label: 'Completed', tone: 'success' },
  queued: { label: 'Pending', tone: 'warning' },
  sending: { label: 'Pending', tone: 'info' },
  conflict: { label: 'Rejected', tone: 'error' },
  online: { label: 'Online', tone: 'success' },
  offline: { label: 'Offline', tone: 'neutral' },
  sandbox: { label: 'Sandbox', tone: 'info' },
  production: { label: 'Production', tone: 'success' },
  // P1 golf statuses (PRD P1 §6.5 additions to Naming Convention §31)
  'no-show': { label: 'No-show', tone: 'error' },
  'on-hold': { label: 'On Hold', tone: 'warning' },
  'in-play': { label: 'In Play', tone: 'info' },
  'not-started': { label: 'Not Started', tone: 'neutral' },
  charging: { label: 'Charging', tone: 'warning' },
  full: { label: 'Full', tone: 'neutral' },
  blocked: { label: 'Blocked', tone: 'error' },
  assigned: { label: 'Assigned', tone: 'info' },
  'not-available': { label: 'Not Available', tone: 'neutral' },
  booked: { label: 'Booked', tone: 'info' },
  dispatched: { label: 'In Play', tone: 'info' },
  returned: { label: 'Completed', tone: 'success' },
  issued: { label: 'Active', tone: 'success' },
  redeemed: { label: 'Completed', tone: 'neutral' },
  held: { label: 'Pending', tone: 'warning' },
  applied: { label: 'Completed', tone: 'success' },
  matched: { label: 'Completed', tone: 'success' },
  exceptions: { label: 'Pending', tone: 'warning' },
  open: { label: 'Active', tone: 'info' },
  closed: { label: 'Completed', tone: 'neutral' },
  present: { label: 'Available', tone: 'success' },
  absent: { label: 'Not Available', tone: 'neutral' },
  leave: { label: 'Not Available', tone: 'neutral' },
  dropped: { label: 'Checked-in', tone: 'info' },
  collected: { label: 'Completed', tone: 'success' },
};

/** Status pill; label and tone override the defaults of the status (e.g. invoice "Issued" is not the voucher "Active"). */
export function StatusPill({ status, label, tone }: { status: string; label?: string; tone?: StatusTone }) {
  const key = status.toLowerCase().replace(/_/g, '-');
  const s = STATUS[key] ?? { label: status.charAt(0).toUpperCase() + status.slice(1), tone: 'neutral' as const };
  return (
    <span className="oc-status" data-tone={tone ?? s.tone}>
      {label ?? s.label}
    </span>
  );
}

// ── page structure ────────────────────────────────────────────────────────

export function PageHeader({ title, help, actions }: { title: string; help?: string; actions?: React.ReactNode }) {
  useEffect(() => {
    document.title = title;
  }, [title]);
  return (
    <div className="oc-page-head">
      <div style={{ flex: 1, minWidth: 240 }}>
        <h1>{title}</h1>
        {help && <p>{help}</p>}
      </div>
      {actions && <div className="oc-row-wrap">{actions}</div>}
    </div>
  );
}

export function Card({ title, icon, actions, children, ink, style }: {
  title?: string; icon?: string; actions?: React.ReactNode; children?: React.ReactNode; ink?: boolean; style?: React.CSSProperties;
}) {
  return (
    <section className={`oc-card${ink ? ' oc-card-ink' : ''}`} style={style}>
      {(title || actions) && (
        <div className="oc-card-head">
          {icon && <span className="oc-icon-circle"><Icon name={icon} size={20} /></span>}
          {title && <h2>{title}</h2>}
          <span className="oc-spacer" />
          {actions}
        </div>
      )}
      {children}
    </section>
  );
}

/**
 * KPI tile of the Management Dashboard: muted label with an icon on the right,
 * the value, a status pill (top right), then a change pill against the previous period (`change` is a
 * ratio, 0.062 = +6.2%) and optional footer rows. Clickable when `onOpen` is set.
 */
export function StatTile({ label, value, icon, change, changeLabel, inverse, status, progress, children, onOpen, muted, title }: {
  label: string; value: React.ReactNode; icon?: string; change?: number | null; changeLabel?: string; status?: React.ReactNode;
  /** Lower is better (expenses, payables): a rise shows in the down tone. */
  inverse?: boolean;
  progress?: number | null; children?: React.ReactNode; onOpen?: () => void; muted?: boolean; title?: string;
}) {
  return (
    <div className="oc-stat" title={title} data-muted={muted || undefined} data-clickable={onOpen ? true : undefined}
      role={onOpen ? 'button' : undefined} tabIndex={onOpen ? 0 : undefined} aria-label={onOpen ? `${label}: open the drill-down` : undefined}
      onClick={onOpen} onKeyDown={onOpen && ((e) => (e.key === 'Enter' || e.key === ' ') && onOpen())}>
      <div className="oc-stat-head">
        <span className="oc-stat-label">{label}</span>
        {status}
        {icon && <span className="oc-stat-icon"><Icon name={icon} size={18} /></span>}
      </div>
      <div className="oc-stat-value">{typeof value === 'string' ? <Amount text={value} size="lg" /> : value}</div>
      {change != null && (
        <div className="oc-stat-meta">
          <Delta ratio={change} chip="" inverse={inverse} suffix={changeLabel} />
        </div>
      )}
      {progress != null && (
        <div className="oc-stat-bar" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(progress * 100)}>
          <span style={{ width: `${Math.max(0, Math.min(1, progress)) * 100}%` }} />
        </div>
      )}
      {children && <div className="oc-stat-foot">{children}</div>}
    </div>
  );
}

/** Compact single-date filter for a page header. */
export function DateFilter({ value, onChange, label = 'Date' }: { value: string; onChange: (v: string) => void; label?: string }) {
  return <input className="oc-input oc-filter" type="date" aria-label={label} value={value} onChange={(e) => e.target.value && onChange(e.target.value)} />;
}

/** Compact From – To date filter for a page header. */
export function DateRange({ from, to, onFrom, onTo }: { from: string; to: string; onFrom: (v: string) => void; onTo: (v: string) => void }) {
  return (
    <div className="oc-row" style={{ gap: 6 }}>
      <input className="oc-input oc-filter" type="date" aria-label="From" value={from} max={to} onChange={(e) => e.target.value && onFrom(e.target.value)} />
      <span className="oc-muted">–</span>
      <input className="oc-input oc-filter" type="date" aria-label="To" value={to} min={from} onChange={(e) => e.target.value && onTo(e.target.value)} />
    </div>
  );
}

/** Compact select filter for a list toolbar: no field label, the empty option names the filter ("All departments"). */
export function SelectFilter({ value, onChange, options, all, label }: {
  value: string; onChange: (v: string) => void; options: Option[]; all: string; label: string;
}) {
  return (
    <select className="oc-input oc-filter" aria-label={label} value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">{all}</option>
      {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  );
}

export function Skeleton({ rows = 4 }: { rows?: number }) {
  return (
    <div className="oc-stack" aria-busy="true">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="oc-skel" style={{ width: `${90 - i * 8}%` }} />
      ))}
    </div>
  );
}

export function Empty({ title, help, icon = 'inbox', action }: { title?: string; help?: string; icon?: string; action?: React.ReactNode }) {
  const { t } = useTranslation();
  return (
    <div className="oc-empty">
      <Icon name={icon} size={40} />
      <div style={{ fontWeight: 600, marginTop: 8 }}>{title ?? t('common.noData')}</div>
      <div className="oc-small">{help ?? t('common.noDataHelp')}</div>
      {action && <div style={{ marginTop: 16 }}>{action}</div>}
    </div>
  );
}

export function ErrorAlert({ error }: { error: unknown }) {
  if (!error) return null;
  const msg = error instanceof ApiError ? error.problem.detail || error.message : String((error as Error).message ?? error);
  const rid = error instanceof ApiError ? error.problem.requestId : undefined;
  return (
    <div className="oc-alert oc-alert-error" role="alert">
      {msg}
      {rid && <span className="oc-muted"> · request {rid}</span>}
    </div>
  );
}

// ── form fields ───────────────────────────────────────────────────────────

/** Maps API field errors to translated messages. */
export function fieldErrors(error: unknown): Record<string, string> {
  if (!(error instanceof ApiError)) return {};
  const out: Record<string, string> = {};
  for (const e of error.problem.errors ?? []) out[e.field] = validationMessage(e.code, e.message);
  return out;
}

interface FieldProps {
  label: string;
  help?: string;
  error?: string;
  required?: boolean;
  span?: boolean;
  children: (id: string) => React.ReactNode;
}

export function Field({ label, help, error, required, span, children }: FieldProps) {
  const id = useId();
  return (
    <div className={`oc-field${span ? ' oc-span' : ''}`} data-invalid={!!error}>
      <label htmlFor={id}>
        {label}
        {required && <span aria-hidden="true" style={{ color: 'var(--md-sys-color-error)' }}> *</span>}
      </label>
      {children(id)}
      {error ? <span className="oc-field-error" role="alert">{error}</span> : help ? <span className="oc-field-help">{help}</span> : null}
    </div>
  );
}

type InputProps = Omit<React.InputHTMLAttributes<HTMLInputElement>, 'onChange'> & {
  label: string; help?: string; error?: string; span?: boolean; onChange?: (v: string) => void;
};

export function TextField({ label, help, error, span, onChange, required, ...rest }: InputProps) {
  return (
    <Field label={label} help={help} error={error} required={required} span={span}>
      {(id) => <input id={id} className="oc-input" required={required} aria-invalid={!!error} {...rest} onChange={(e) => onChange?.(e.target.value)} />}
    </Field>
  );
}

type MoneyFieldProps = Omit<MoneyInputProps, 'className'> & { label: string; help?: string; error?: string; span?: boolean };

/** A money field: "Rp 500.000" while typing, the plain number in onChange
 * (demo feedback 10 Oct 2026 #13). Use it for every amount, price, fee,
 * deposit, tip or salary instead of a TextField. */
export function MoneyField({ label, help, error, span, required, ...rest }: MoneyFieldProps) {
  return (
    <Field label={label} help={help} error={error} required={required} span={span}>
      {(id) => <MoneyInput id={id} className="oc-input" required={required} aria-invalid={!!error} {...rest} />}
    </Field>
  );
}

export function PasswordField(props: InputProps) {
  const { t } = useTranslation();
  const [show, setShow] = useState(false);
  const { label, help, error, span, onChange, required, ...rest } = props;
  return (
    <Field label={label} help={help} error={error} required={required} span={span}>
      {(id) => (
        <div className="oc-input-wrap">
          <input id={id} className="oc-input" type={show ? 'text' : 'password'} required={required} aria-invalid={!!error} {...rest}
            onChange={(e) => onChange?.(e.target.value)} />
          <button type="button" className="oc-input-action" onClick={() => setShow((s) => !s)} aria-label={show ? t('auth.hidePassword') : t('auth.showPassword')}>
            <Icon name={show ? 'visibility_off' : 'visibility'} size={20} />
          </button>
        </div>
      )}
    </Field>
  );
}

export function TextArea({ label, help, error, span, value, onChange, required, rows = 4 }: {
  label: string; help?: string; error?: string; span?: boolean; value: string; onChange: (v: string) => void; required?: boolean; rows?: number;
}) {
  return (
    <Field label={label} help={help} error={error} required={required} span={span}>
      {(id) => <textarea id={id} className="oc-textarea" rows={rows} value={value} required={required} onChange={(e) => onChange(e.target.value)} />}
    </Field>
  );
}

export interface Option {
  value: string;
  label: string;
}

export function SelectField({ label, help, error, span, value, onChange, options, required, placeholder }: {
  label: string; help?: string; error?: string; span?: boolean; value: string; onChange: (v: string) => void; options: Option[];
  required?: boolean; placeholder?: string;
}) {
  return (
    <Field label={label} help={help} error={error} required={required} span={span}>
      {(id) => (
        <select id={id} className="oc-select" value={value} required={required} onChange={(e) => onChange(e.target.value)} aria-invalid={!!error}>
          {placeholder !== undefined && <option value="">{placeholder}</option>}
          {options.map((o) => (
            <option key={o.value} value={o.value}>{o.label}</option>
          ))}
        </select>
      )}
    </Field>
  );
}

export function Checkbox({ label, checked, onChange, disabled }: { label: string; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <label className="oc-check">
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span>{label}</span>
    </label>
  );
}

// ── overlays ──────────────────────────────────────────────────────────────

function useEscape(open: boolean, onClose: () => void) {
  useEffect(() => {
    if (!open) return;
    const h = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [open, onClose]);
}

/** Focus stays inside the dialog while it is open (accessibility audit gap #4). */
function useFocusTrap(open: boolean) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open || !ref.current) return;
    const prev = document.activeElement as HTMLElement | null;
    const el = ref.current;
    const focusables = () => el.querySelectorAll<HTMLElement>('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])');
    focusables()[0]?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Tab') return;
      const f = Array.from(focusables()).filter((x) => !x.hasAttribute('disabled'));
      if (f.length === 0) return;
      if (e.shiftKey && document.activeElement === f[0]) {
        e.preventDefault();
        f[f.length - 1].focus();
      } else if (!e.shiftKey && document.activeElement === f[f.length - 1]) {
        e.preventDefault();
        f[0].focus();
      }
    };
    el.addEventListener('keydown', onKey);
    return () => {
      el.removeEventListener('keydown', onKey);
      prev?.focus();
    };
  }, [open]);
  return ref;
}

export function Modal({ open, onClose, title, children, actions, wide }: {
  open: boolean; onClose: () => void; title: string; children: React.ReactNode; actions?: React.ReactNode; wide?: boolean;
}) {
  useEscape(open, onClose);
  const ref = useFocusTrap(open);
  if (!open) return null;
  return (
    <>
      <div className="oc-scrim" onClick={onClose} />
      <div className={`oc-modal${wide ? ' oc-modal-wide' : ''}`} role="dialog" aria-modal="true" aria-label={title} ref={ref}>
        <h2>{title}</h2>
        {children}
        {actions && <div className="oc-modal-actions">{actions}</div>}
      </div>
    </>
  );
}

export function Drawer({ open, onClose, title, children }: { open: boolean; onClose: () => void; title: string; children: React.ReactNode }) {
  useEscape(open, onClose);
  const ref = useFocusTrap(open);
  if (!open) return null;
  return (
    <>
      <div className="oc-scrim" onClick={onClose} />
      <aside className="oc-drawer" role="dialog" aria-modal="true" aria-label={title} ref={ref}>
        <div className="oc-row" style={{ marginBottom: 16 }}>
          <h2 style={{ margin: 0, fontSize: 22 }}>{title}</h2>
          <span className="oc-spacer" />
          <button className="oc-icon-btn" onClick={onClose} aria-label="Close"><Icon name="close" size={20} /></button>
        </div>
        {children}
      </aside>
    </>
  );
}

/** Confirmation with optional mandatory reason (Reject, Discard, Deactivate). */
export function ConfirmDialog({ open, onClose, onConfirm, title, message, confirmLabel, danger, reason, busy, error }: {
  open: boolean; onClose: () => void; onConfirm: (reason: string) => void; title: string; message?: string; confirmLabel: string;
  danger?: boolean; reason?: 'required' | 'optional'; busy?: boolean; error?: unknown;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState('');
  useEffect(() => {
    if (open) setText('');
  }, [open]);
  const missing = reason === 'required' && text.trim() === '';
  return (
    <Modal open={open} onClose={onClose} title={title}
      actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
        <button className={`oc-btn ${danger ? 'oc-btn-danger' : 'oc-btn-ink'}`} disabled={missing || busy} onClick={() => onConfirm(text.trim())}>{confirmLabel}</button>
      </>}>
      <div className="oc-stack">
        {message && <p className="oc-muted" style={{ margin: 0 }}>{message}</p>}
        {reason && <TextArea label={reason === 'required' ? 'Reason' : 'Reason (optional)'} value={text} onChange={setText} required={reason === 'required'}
          help={reason === 'required' ? t('common.reasonRequired') : undefined} rows={3} />}
        <ErrorAlert error={error} />
      </div>
    </Modal>
  );
}

// ── data table (dashboard-ui: search pill, filter pills, status pills) ───

export interface Column<T> {
  key: string;
  header: React.ReactNode;
  render?: (row: T) => React.ReactNode;
  width?: string | number;
  align?: 'left' | 'right';
}

/** Rows per page of every data table. */
export const PAGE_SIZE = 10;

/** Client-side paging of a list: the rows of the current page; back to page 1 when the list changes size. */
export function usePager<T>(rows: T[] | undefined, size = PAGE_SIZE) {
  const [page, setPage] = useState(1);
  const total = rows?.length ?? 0;
  useEffect(() => setPage(1), [total]);
  const pages = size > 0 ? Math.max(1, Math.ceil(total / size)) : 1;
  const current = Math.min(page, pages);
  const visible = !rows ? [] : size > 0 ? rows.slice((current - 1) * size, current * size) : rows;
  return { visible, page: current, pages, total, size, setPage };
}

/** Page numbers to show: first, last, the current one and its neighbours, gaps as 0. */
function pageList(page: number, pages: number): number[] {
  const set = [...new Set([1, pages, page - 1, page, page + 1])].filter((p) => p >= 1 && p <= pages).sort((x, y) => x - y);
  const out: number[] = [];
  set.forEach((p, i) => {
    if (i > 0 && p - set[i - 1] > 1) out.push(0);
    out.push(p);
  });
  return out;
}

/** "1–10 of 57" with previous / page numbers / next; hidden on a single page. */
export function Pager({ page, pages, total, size, setPage }: { page: number; pages: number; total: number; size: number; setPage: (p: number) => void }) {
  const { t } = useTranslation();
  if (pages <= 1) return null;
  const from = (page - 1) * size + 1;
  return (
    <nav className="oc-pager" aria-label="Pagination">
      <span className="oc-small oc-muted">{t('common.pageRange', { from, to: Math.min(total, page * size), total })}</span>
      <span className="oc-row" style={{ gap: 4 }}>
        <button type="button" className="oc-pager-btn" disabled={page <= 1} onClick={() => setPage(page - 1)} aria-label={t('common.previousPage')}>
          <Icon name="chevron_left" size={18} />
        </button>
        {pageList(page, pages).map((p, i) => (p === 0
          ? <span key={`gap${i}`} className="oc-pager-gap">…</span>
          : <button key={p} type="button" className="oc-pager-btn" aria-current={p === page ? 'page' : undefined} aria-label={t('common.page', { page: p })}
            onClick={() => setPage(p)}>{p}</button>))}
        <button type="button" className="oc-pager-btn" disabled={page >= pages} onClick={() => setPage(page + 1)} aria-label={t('common.nextPage')}>
          <Icon name="chevron_right" size={18} />
        </button>
      </span>
    </nav>
  );
}

/**
 * One page of a list from the server (?limit=&cursor=, see httpx/paged.go):
 * the rows of the current page and the pager; back to the first page when the
 * path (filters, search) changes.
 */
export function usePagedList<T, P extends { items: T[]; nextCursor?: string; total?: number; totalAtLeast?: number } = { items: T[]; nextCursor?: string; total?: number; totalAtLeast?: number }>(
  path: string | null, size = PAGE_SIZE) {
  const [cursors, setCursors] = useState<string[]>(['']);
  useEffect(() => setCursors(['']), [path]);
  const cursor = cursors[cursors.length - 1];
  const url = path === null ? null : `${path}${path.includes('?') ? '&' : '?'}limit=${size}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`;
  const q = useGet<P>(url, { placeholderData: (prev) => prev });
  const page = cursors.length;
  const rows = q.data?.items ?? [];
  const from = rows.length === 0 ? 0 : (page - 1) * size + 1;
  const total = q.data?.total;
  const totalAtLeast = q.data?.totalAtLeast;
  // Offset cursors (httpx/paged.go) can be built for any page, so the pager
  // jumps to a page number; keyset cursors only go back and forth.
  const jump = isOffsetCursor(page > 1 ? cursor : q.data?.nextCursor);
  return {
    ...q,
    rows: q.data ? rows : undefined,
    pager: {
      page, from, to: from === 0 ? 0 : from + rows.length - 1, total, totalAtLeast, hasPrev: page > 1,
      hasNext: !!q.data?.nextCursor,
      /** Known pages (a lower bound with totalAtLeast); 0 when unknown. */
      pages: jump ? Math.max(page, Math.ceil((total ?? totalAtLeast ?? 0) / size)) : 0,
      prev: () => setCursors((c) => (c.length > 1 ? c.slice(0, -1) : c)),
      next: () => q.data?.nextCursor && setCursors((c) => [...c, q.data!.nextCursor!]),
      goTo: (p: number) => setCursors(Array.from({ length: p }, (_, i) => (i === 0 ? '' : offsetCursor(i * size)))),
    },
  };
}

const offsetCursor = (offset: number) => btoa(`o:${offset}`).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
function isOffsetCursor(c?: string) {
  if (!c) return false;
  try {
    return atob(c.replace(/-/g, '+').replace(/_/g, '/')).startsWith('o:');
  } catch {
    return false;
  }
}

export type ServerPage = ReturnType<typeof usePagedList>['pager'];

/** Pager of a server-paged list: "11–20 of 57" ("of 50+" past the rows read ahead), previous / page numbers / next. */
export function ServerPager({ page, from, to, total, totalAtLeast, hasPrev, hasNext, pages, prev, next, goTo }: ServerPage) {
  const { t } = useTranslation();
  if (!hasPrev && !hasNext) return null;
  return (
    <nav className="oc-pager" aria-label="Pagination">
      <span className="oc-small oc-muted">{total != null ? t('common.pageRange', { from, to, total })
        : totalAtLeast != null ? t('common.pageRangeMore', { from, to, total: totalAtLeast }) : `${from}–${to}`}</span>
      <span className="oc-row" style={{ gap: 4 }}>
        <button type="button" className="oc-pager-btn" disabled={!hasPrev} onClick={prev} aria-label={t('common.previousPage')}><Icon name="chevron_left" size={18} /></button>
        {pages > 1
          ? <>
            {pageList(page, pages).map((p, i) => (p === 0
              ? <span key={`gap${i}`} className="oc-pager-gap">…</span>
              : <button key={p} type="button" className="oc-pager-btn" aria-current={p === page ? 'page' : undefined} aria-label={t('common.page', { page: p })}
                onClick={() => p !== page && goTo(p)}>{p}</button>))}
            {total == null && hasNext && <span className="oc-pager-gap">…</span>}
          </>
          : <span className="oc-pager-btn" aria-current="page" aria-label={t('common.page', { page })}>{page}</span>}
        <button type="button" className="oc-pager-btn" disabled={!hasNext} onClick={next} aria-label={t('common.nextPage')}><Icon name="chevron_right" size={18} /></button>
      </span>
    </nav>
  );
}

/**
 * How table rows show their actions: office layouts (sidebar) put them in
 * a ⋮ menu so rows stay clean; touch layouts (POS, starter, tablet) keep
 * large inline buttons for the operators.
 */
export const RowActionsStyle = React.createContext<'inline' | 'menu'>('inline');

/**
 * Elements of an actions render: fragments and layout wrappers (a div or
 * span with an oc-row class) flattened, empty slots (false, null) dropped.
 */
function actionNodes(node: React.ReactNode): React.ReactNode[] {
  return React.Children.toArray(node).flatMap((n) => {
    if (!React.isValidElement(n)) return [n];
    const props = n.props as { children?: React.ReactNode; className?: string };
    const wrapper = n.type === React.Fragment || ((n.type === 'div' || n.type === 'span') && /\boc-row(-wrap)?\b/.test(props.className ?? ''));
    return wrapper ? actionNodes(props.children) : [n];
  });
}

const DIALOG = '.oc-modal, .oc-drawer';

/**
 * The ⋮ menu of a table row. The actions are the row's own buttons and
 * links, listed as menu items. After one is chosen the menu disappears but
 * stays mounted while the dialog it opened is shown or its request runs,
 * so confirmations and reasons keep working.
 */
function RowMenu({ items }: { items: React.ReactNode[] }) {
  const [pos, setPos] = useState<{ top: number; right: number } | null>(null);
  const [used, setUsed] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const before = useRef<Set<Element>>(new Set());
  const close = () => { setPos(null); setUsed(false); };
  // Busy: a dialog of the chosen action is open, or a button disabled itself (request running).
  const busy = () => {
    const m = menu.current;
    if (!m) return false;
    return !!m.querySelector(DIALOG) || [...m.querySelectorAll('button:disabled')].some((b) => !before.current.has(b));
  };
  useEffect(() => {
    if (!pos) return;
    const outside = (e: Event) => {
      if (ref.current?.contains(e.target as Node) || menu.current?.contains(e.target as Node) || busy()) return;
      close();
    };
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape' && !busy()) close(); };
    const scroll = (e: Event) => { if (!used && !menu.current?.contains(e.target as Node)) close(); };
    document.addEventListener('mousedown', outside);
    document.addEventListener('keydown', esc);
    window.addEventListener('scroll', scroll, true);
    return () => {
      document.removeEventListener('mousedown', outside);
      document.removeEventListener('keydown', esc);
      window.removeEventListener('scroll', scroll, true);
    };
  }, [pos, used]);
  // After an action: close as soon as nothing of it is left on screen.
  useEffect(() => {
    if (!used || !menu.current) return;
    const check = () => { if (!busy()) close(); };
    const t = window.setTimeout(check, 0);
    const watch = new MutationObserver(check);
    watch.observe(menu.current, { subtree: true, childList: true, attributes: true, attributeFilter: ['disabled'] });
    return () => { window.clearTimeout(t); watch.disconnect(); };
  }, [used]);
  return (
    <div ref={ref} className="oc-row-menu-anchor">
      <button type="button" className="oc-btn oc-btn-sm oc-btn-text oc-kebab" aria-label="Row actions" aria-haspopup="menu" aria-expanded={!!pos && !used}
        onClick={(e) => {
          const b = e.currentTarget.getBoundingClientRect();
          setUsed(false);
          setPos(pos ? null : { top: b.bottom + 4, right: window.innerWidth - b.right });
        }}><span aria-hidden className="oc-kebab-dots">⋮</span></button>
      {pos && createPortal(
        <div ref={menu} className="oc-popover oc-row-menu oc-row-actions" role="menu" data-used={used || undefined}
          style={{ position: 'fixed', top: pos.top, right: pos.right }}
          onClickCapture={(e) => {
            if ((e.target as Element).closest(DIALOG) || used) return;
            if (!(e.target as Element).closest('button, a')) return;
            before.current = new Set(menu.current?.querySelectorAll('button:disabled') ?? []);
            setUsed(true);
          }}>
          {items}
        </div>, document.body,
      )}
    </div>
  );
}

/** Short plain text (dates, codes, names, amounts) stays on one line; long text wraps. */
function short(v: React.ReactNode): boolean {
  if (typeof v === 'string') return v.length <= 28;
  if (typeof v === 'number') return true;
  if (React.isValidElement(v)) {
    const c = (v.props as { children?: React.ReactNode }).children;
    return typeof c === 'string' ? c.length <= 28 : typeof c === 'number';
  }
  return false;
}

/**
 * A header with a parenthesis: a short unit stays as a small suffix
 * ("Age · days"), a longer explanation moves to the tooltip.
 */
function headerLabel(h: React.ReactNode): React.ReactNode {
  if (typeof h !== 'string') return h;
  const m = /^(.+?)\s*\(([^()]+)\)$/.exec(h);
  if (!m) return h;
  return m[2].length <= 6
    ? <>{m[1]} <span className="oc-th-unit">{m[2]}</span></>
    : <span title={m[2]} className="oc-th-hint">{m[1]}</span>;
}

export function DataTable<T extends Record<string, unknown>>({ columns, rows, loading, error, onRowClick, actions, actionsHeader, inlineActions, empty, rowKey, pageSize = PAGE_SIZE, server }: {
  columns: Column<T>[]; rows: T[] | undefined; loading?: boolean; error?: unknown; onRowClick?: (row: T) => void;
  actions?: (row: T) => React.ReactNode; empty?: React.ReactNode; rowKey?: (row: T) => string;
  /** Keep the row actions as buttons even in office layouts (the row's main operation, e.g. Tee-Off on the starter list). */
  inlineActions?: boolean;
  /** Title of the actions column (unlabelled by default). */
  actionsHeader?: string;
  /** Rows per page (default 10); 0 shows every row. */
  pageSize?: number;
  /** The rows are one page of a server-paged list (usePagedList): no paging in the browser, the server pager below. */
  server?: ServerPage;
}) {
  const pager = usePager(rows, server ? 0 : pageSize);
  const office = useContext(RowActionsStyle);
  const style = inlineActions ? 'inline' : office;
  if (error) return <ErrorAlert error={error} />;
  // .oc-datatable is a card on the page canvas and plain inside a card, modal or drawer.
  if (loading) return <div className="oc-datatable"><Skeleton rows={5} /></div>;
  if (!rows || rows.length === 0) return <div className="oc-datatable">{empty ?? <Empty />}{server && <ServerPager {...server} />}</div>;
  const rowActions = (r: T) => {
    if (!actions) return null;
    const node = actions(r);
    if (style !== 'menu') return node;
    const items = actionNodes(node);
    return items.length ? <RowMenu items={items} /> : null;
  };
  return (
    <div className="oc-datatable">
      <div className="oc-table-wrap">
        <table className="oc-table">
          <thead>
            <tr>
              {columns.map((c) => <th key={c.key} style={{ width: c.width, textAlign: c.align }}>{headerLabel(c.header)}</th>)}
              {actions && (actionsHeader && style !== 'menu' ? <th>{actionsHeader}</th> : <th className="oc-th-actions" aria-label="Actions" />)}
              {onRowClick && <th className="oc-th-go" aria-hidden />}
            </tr>
          </thead>
          <tbody>
            {pager.visible.map((r, i) => (
              <tr key={rowKey ? rowKey(r) : String(r.id ?? i)} data-clickable={!!onRowClick} onClick={onRowClick ? () => onRowClick(r) : undefined}>
                {columns.map((c) => {
                  const v = c.render ? c.render(r) : (r[c.key] as React.ReactNode);
                  const cls = [c.align === 'right' ? 'oc-num' : '', short(v) ? 'oc-nowrap' : ''].filter(Boolean).join(' ');
                  return (
                    <td key={c.key} className={cls || undefined} style={{ textAlign: c.align }}>
                      {v ?? <span className="oc-muted">—</span>}
                    </td>
                  );
                })}
                {actions && <td className="oc-actions" onClick={(e) => e.stopPropagation()}>{rowActions(r)}</td>}
                {onRowClick && <td className="oc-go" aria-hidden><Icon name="chevron_right" size={20} /></td>}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {server ? <ServerPager {...server} /> : <Pager {...pager} />}
    </div>
  );
}

export function SearchBox({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder?: string }) {
  const { t } = useTranslation();
  return (
    <div className="oc-search">
      <Icon name="search" size={20} />
      <input className="oc-input" type="search" value={value} placeholder={placeholder ?? t('common.searchPlaceholder')} aria-label="Search"
        onChange={(e) => onChange(e.target.value)} />
    </div>
  );
}

export function FilterPills({ options, value, onChange }: { options: Option[]; value: string; onChange: (v: string) => void }) {
  return (
    <div className="oc-row-wrap" role="group" aria-label="Filter">
      {options.map((o) => (
        <button key={o.value} type="button" className="oc-chip" aria-pressed={value === o.value} onClick={() => onChange(o.value)}>{o.label}</button>
      ))}
    </div>
  );
}

/** Debounced value for search inputs. */
export function useDebounced<T>(value: T, ms = 300) {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

export function Labeled({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="oc-small oc-muted">{label}</div>
      <div style={{ fontWeight: 500 }}>{children ?? '—'}</div>
    </div>
  );
}

export type ActionMenuItem = { label: string; icon: string; onClick?: () => void; to?: string; hidden?: boolean; danger?: boolean; separator?: boolean };

/**
 * Menu of secondary actions: "•••" on a table row, or a labelled button
 * ("Actions") on a page header. Opens on <body>, so sticky table cells do
 * not paint over it; closes on a click outside, Escape or scrolling.
 */
export function ActionMenu({ items, label }: { items: ActionMenuItem[]; label?: string }) {
  const [pos, setPos] = useState<{ top: number; right: number } | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();
  useEffect(() => {
    if (!pos) return;
    const close = (e: Event) => { if (!ref.current?.contains(e.target as Node) && !menu.current?.contains(e.target as Node)) setPos(null); };
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && setPos(null);
    document.addEventListener('mousedown', close);
    document.addEventListener('keydown', esc);
    window.addEventListener('scroll', () => setPos(null), { once: true, capture: true });
    return () => { document.removeEventListener('mousedown', close); document.removeEventListener('keydown', esc); };
  }, [pos]);
  const visible = items.filter((i) => !i.hidden);
  if (visible.length === 0) return null;
  const toggle = (e: React.MouseEvent<HTMLButtonElement>) => {
    const b = e.currentTarget.getBoundingClientRect();
    setPos(pos ? null : { top: b.bottom + 4, right: window.innerWidth - b.right });
  };
  return (
    <div ref={ref} style={{ display: 'inline-block' }}>
      {label
        ? <button className="oc-btn oc-btn-neutral" aria-haspopup="menu" aria-expanded={!!pos} onClick={toggle}>{label} <Icon name="expand_more" size={18} /></button>
        : <button className="oc-btn oc-btn-sm oc-btn-text" aria-label="More actions" aria-haspopup="menu" aria-expanded={!!pos} onClick={toggle}>•••</button>}
      {pos && createPortal(
        <div ref={menu} className="oc-popover oc-row-menu" role="menu" style={{ position: 'fixed', top: pos.top, right: pos.right }}>
          {visible.map((i) => (
            <React.Fragment key={i.label}>
              {i.separator && <div className="oc-menu-sep" role="separator" />}
              <button className="oc-menu-item" role="menuitem" data-danger={i.danger || undefined}
                onClick={() => { setPos(null); if (i.to) navigate(i.to); else i.onClick?.(); }}>
                <Icon name={i.icon} size={18} /> {i.label}
              </button>
            </React.Fragment>
          ))}
        </div>, document.body,
      )}
    </div>
  );
}
