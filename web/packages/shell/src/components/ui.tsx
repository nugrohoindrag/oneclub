import React, { useEffect, useId, useRef, useState } from 'react';
import { Icon } from '@oneclub/ui';
import { ApiError } from '@oneclub/api-client';
import { useTranslation, validationMessage } from '@oneclub/i18n';

export { Icon };

// ── status pill (Naming Convention §31; FR-SH-09) ─────────────────────────

const STATUS: Record<string, { label: string; tone: 'success' | 'warning' | 'error' | 'info' | 'neutral' }> = {
  draft: { label: 'Draft', tone: 'neutral' },
  pending: { label: 'Pending', tone: 'warning' },
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

export function StatusPill({ status, label }: { status: string; label?: string }) {
  const key = status.toLowerCase().replace(/_/g, '-');
  const s = STATUS[key] ?? { label: status.charAt(0).toUpperCase() + status.slice(1), tone: 'neutral' as const };
  return (
    <span className="oc-status" data-tone={s.tone}>
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
  header: string;
  render?: (row: T) => React.ReactNode;
  width?: string | number;
  align?: 'left' | 'right';
}

export function DataTable<T extends Record<string, unknown>>({ columns, rows, loading, error, onRowClick, actions, empty, rowKey }: {
  columns: Column<T>[]; rows: T[] | undefined; loading?: boolean; error?: unknown; onRowClick?: (row: T) => void;
  actions?: (row: T) => React.ReactNode; empty?: React.ReactNode; rowKey?: (row: T) => string;
}) {
  if (error) return <ErrorAlert error={error} />;
  if (loading) return <Skeleton rows={5} />;
  if (!rows || rows.length === 0) return <>{empty ?? <Empty />}</>;
  return (
    <div className="oc-table-wrap">
      <table className="oc-table">
        <thead>
          <tr>
            {columns.map((c) => <th key={c.key} style={{ width: c.width, textAlign: c.align }}>{c.header}</th>)}
            {actions && <th aria-label="Actions" />}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={rowKey ? rowKey(r) : String(r.id ?? i)} data-clickable={!!onRowClick} onClick={onRowClick ? () => onRowClick(r) : undefined}>
              {columns.map((c) => (
                <td key={c.key} style={{ textAlign: c.align }}>
                  {c.render ? c.render(r) : (r[c.key] as React.ReactNode) ?? <span className="oc-muted">—</span>}
                </td>
              ))}
              {actions && <td className="oc-actions" onClick={(e) => e.stopPropagation()}>{actions(r)}</td>}
            </tr>
          ))}
        </tbody>
      </table>
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
