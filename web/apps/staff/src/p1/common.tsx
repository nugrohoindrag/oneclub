import React, { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { currentLocale, formatMoney } from '@oneclub/i18n';
import {
  ConfirmDialog, DataTable, ErrorAlert, FilterPills, PageHeader, SearchBox, SelectField, TextField, useAuth, useDebounced, usePagedList, useToast,
  type Column, type Option,
} from '@oneclub/shell';

export type R = Record<string, unknown> & { id: string };

/** Local date (YYYY-MM-DD) of today. */
export function today(): string {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

export function money(v: unknown, currency = 'IDR') {
  if (v === null || v === undefined || v === '') return '—';
  return formatMoney(String(v), currency);
}

/** IDR in short form for headline figures (Rp 9,98 M / IDR 9.98B). */
export function moneyShort(v: unknown) {
  if (v === null || v === undefined || v === '') return '—';
  return new Intl.NumberFormat(currentLocale() === 'en' ? 'en-US' : 'id-ID', {
    style: 'currency', currency: 'IDR', notation: 'compact', maximumFractionDigits: 2,
  }).format(Number(v));
}

/** Course + date selection kept in the URL (?courseId=&date=). */
export function useCourseDate() {
  const [params, setParams] = useSearchParams();
  const courses = useGet<Page<R>>('/api/v1/golf/courses?filter[status]=active&limit=100');
  const list = courses.data?.items ?? [];
  const courseId = params.get('courseId') ?? (list[0]?.id as string | undefined) ?? '';
  const date = params.get('date') ?? today();
  const set = (k: string, v: string) => {
    const next = new URLSearchParams(params);
    next.set(k, v);
    setParams(next, { replace: true });
  };
  return { courses: list, courseId, date, setCourse: (v: string) => set('courseId', v), setDate: (v: string) => set('date', v) };
}

export function CourseDateBar({ cd, noCourse }: { cd: ReturnType<typeof useCourseDate>; noCourse?: boolean }) {
  return (
    <div className="oc-row-wrap">
      {!noCourse && (
        <SelectField label="Course" value={cd.courseId} onChange={cd.setCourse}
          options={cd.courses.map((c) => ({ value: c.id, label: `${c.name} (${c.code})` }))} />
      )}
      <TextField label="Date" type="date" value={cd.date} onChange={cd.setDate} />
    </div>
  );
}

/** A list page over a collection endpoint with search and status filter. */
export function ListPage({ title, help, path, columns, statuses, actions, rowActions, onRowClick, extraQuery, filters, search = true }: {
  title: string; help?: string; path: string; columns: Column<R>[]; statuses?: Option[]; actions?: React.ReactNode;
  rowActions?: (r: R) => React.ReactNode; onRowClick?: (r: R) => void; extraQuery?: Record<string, string>; filters?: React.ReactNode; search?: boolean;
}) {
  const [q, setQ] = useState('');
  const [status, setStatus] = useState('');
  const query = useDebounced(q);
  // One page of 10 rows per request (server paging, httpx/paged.go).
  const list = usePagedList<R>(`${path}${path.includes('?') ? '&' : '?'}${qs({ q: search ? query : '', 'filter[status]': status, ...extraQuery }).slice(1)}`);
  return (
    <div className="oc-stack">
      <PageHeader title={title} help={help} actions={actions} />
      {(search || statuses || filters) && (
        <div className="oc-row-wrap">
          {search && <SearchBox value={q} onChange={setQ} placeholder="Search" />}
          {statuses && <FilterPills options={[{ value: '', label: 'All' }, ...statuses]} value={status} onChange={setStatus} />}
          {filters}
        </div>
      )}
      <DataTable rows={list.rows} loading={list.isLoading} error={list.error} columns={columns} actions={rowActions} onRowClick={onRowClick} server={list.pager} />
    </div>
  );
}

/** Button that POSTs an action (optionally asking for a reason). */
export function ActionButton({ label, path, body, invalidate, reason, danger, confirm, onDone, kind = 'neutral', method = 'POST', disabled }: {
  label: string; path: string; body?: Record<string, unknown>; invalidate: string[]; reason?: 'required' | 'optional'; danger?: boolean;
  confirm?: string; onDone?: (res: unknown) => void; kind?: 'neutral' | 'primary' | 'ink' | 'text'; method?: 'POST' | 'PUT' | 'DELETE' | 'PATCH';
  disabled?: boolean;
}) {
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const send = useSend<Record<string, unknown>>(method, path, invalidate);
  const run = (why?: string) =>
    send.mutate({ ...(body ?? {}), ...(why ? { reason: why } : {}) }, {
      onSuccess: (res) => {
        setOpen(false);
        toast(`${label}: done`);
        onDone?.(res);
      },
      onError: (e) => {
        if (!reason && !confirm) toast(e.message, 'error');
      },
    });
  return (
    <>
      <button className={`oc-btn oc-btn-sm ${danger ? 'oc-btn-danger' : `oc-btn-${kind}`}`} disabled={disabled || send.isPending}
        onClick={() => (reason || confirm ? setOpen(true) : run())}>{label}</button>
      {(reason || confirm) && (
        <ConfirmDialog open={open} onClose={() => setOpen(false)} onConfirm={(why) => run(why)} title={label} message={confirm}
          confirmLabel={label} danger={danger} reason={reason} busy={send.isPending} error={send.error} />
      )}
    </>
  );
}

/** Key–value summary. */
export function KV({ items }: { items: [string, React.ReactNode][] }) {
  return (
    <dl className="oc-kv" style={{ display: 'grid', gridTemplateColumns: 'max-content 1fr', gap: '6px 16px', margin: 0 }}>
      {items.map(([k, v]) => (
        <React.Fragment key={k}>
          <dt className="oc-muted">{k}</dt>
          <dd style={{ margin: 0 }}>{v ?? '—'}</dd>
        </React.Fragment>
      ))}
    </dl>
  );
}

/** Tabs (pill style). */
export function Tabs({ tabs, value, onChange }: { tabs: Option[]; value: string; onChange: (v: string) => void }) {
  return (
    <div className="oc-row-wrap" role="tablist">
      {tabs.map((t) => (
        <button key={t.value} role="tab" className="oc-chip" aria-pressed={value === t.value} aria-selected={value === t.value} onClick={() => onChange(t.value)}>
          {t.label}
        </button>
      ))}
    </div>
  );
}

/** Subscribes to a Server-Sent Events stream and calls onEvent per message. */
export function useStream(path: string | null, onEvent: () => void) {
  const { propertyId } = useAuth();
  useEffect(() => {
    if (!path || typeof EventSource === 'undefined') return;
    const es = new EventSource(`${path}${path.includes('?') ? '&' : '?'}propertyId=${propertyId}`, { withCredentials: true });
    const handler = () => onEvent();
    es.addEventListener('golf.tee_sheet', handler);
    es.addEventListener('golf.starter_queue', handler);
    es.addEventListener('golf.boards', handler);
    return () => es.close();
  }, [path, propertyId]); // eslint-disable-line react-hooks/exhaustive-deps
}

export { ErrorAlert };
