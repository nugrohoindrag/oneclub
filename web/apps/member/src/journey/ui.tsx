import React from 'react';
import { Link, NavLink, Outlet, useLocation } from 'react-router';
import { formatNumber } from '@oneclub/i18n';
import { Icon, useNavigation } from '@oneclub/shell';

// Shared pieces of the member journey screens (journey.css).

export type Row = Record<string, unknown>;

export const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);

/** Local date as YYYY-MM-DD, `offset` days from today. */
export function isoDay(offset = 0, from?: string): string {
  const d = from ? new Date(`${from}T00:00:00`) : new Date();
  d.setDate(d.getDate() + offset);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

export function dayLabel(iso: string, opts: Intl.DateTimeFormatOptions = { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' }) {
  return new Date(`${iso.slice(0, 10)}T00:00:00`).toLocaleDateString('en-GB', opts);
}

export function initials(name: string) {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((w) => w[0]!.toUpperCase()).join('') || '?';
}

export function Head({ title, help, back, actions }: { title: string; help?: React.ReactNode; back?: [string, string]; actions?: React.ReactNode }) {
  return (
    <div className="mj-stack-head">
      {back && <Link className="mj-back" to={back[0]}><Icon name="arrow_back" size={18} /> {back[1]}</Link>}
      <div className="mj-head">
        <div><h1>{title}</h1>{help && <p>{help}</p>}</div>
        <span className="oc-spacer" />
        {actions}
      </div>
    </div>
  );
}

/**
 * The sub-menu of the current page as tabs, above every page of a top menu
 * (new journey pages and the earlier ones alike): the top menu whose child
 * path matches the URL best. The Book wizards keep their own steps.
 */
export function HubFrame() {
  const nav = useNavigation('member');
  const { pathname } = useLocation();
  let best: { top: string; children: { key: string; label: string; path: string }[]; len: number } | null = null;
  for (const top of nav.data?.items ?? []) {
    for (const c of top.children ?? []) {
      if (c.path !== '/' && (pathname === c.path || pathname.startsWith(`${c.path}/`)) && c.path.length > (best?.len ?? 0)) {
        best = { top: top.label, children: top.children!, len: c.path.length };
      }
    }
  }
  const show = best && best.children.length > 1 && !pathname.startsWith('/book/');
  return (
    <>
      {show && (
        <nav className="mj-tabs mj-hub" aria-label={best!.top}>
          {best!.children.map((c) => <NavLink key={c.key} to={c.path} end>{c.label}</NavLink>)}
        </nav>
      )}
      <Outlet />
    </>
  );
}

export type Tone = 'ok' | 'warn' | 'bad' | 'info' | undefined;

const TONES: Record<string, Tone> = {
  confirmed: 'ok', completed: 'ok', checked_in: 'ok', paid: 'ok', active: 'ok', reserved: 'ok', finalized: 'ok', registered: 'ok', approved: 'ok', in_play: 'info',
  in_progress: 'info', assigned: 'ok', submitted: 'info', checked_out: 'ok',
  pending: 'warn', draft: 'warn', requested: 'warn', due: 'warn', partially_paid: 'warn', waitlisted: 'warn', open: 'warn', issued: 'warn', expiring: 'warn',
  cancelled: 'bad', no_show: 'bad', rejected: 'bad', overdue: 'bad', failed: 'bad', expired: 'bad', withdrawn: 'bad', suspended: 'bad',
};

export function Chip({ tone, children }: { tone?: Tone; children: React.ReactNode }) {
  return <span className="mj-chip" data-tone={tone}>{children}</span>;
}

export function StatusChip({ status, label }: { status: string; label?: string }) {
  return <Chip tone={TONES[status]}>{label ?? status.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase())}</Chip>;
}

export function Steps({ steps, current }: { steps: string[]; current: number }) {
  return (
    <div className="mj-steps" aria-label={`Step ${current + 1} of ${steps.length}`}>
      {steps.map((s, i) => (
        <React.Fragment key={s}>
          {i > 0 && <span className="mj-step-sep" />}
          <span className="mj-step" data-state={i < current ? 'done' : i === current ? 'current' : 'next'} aria-current={i === current ? 'step' : undefined}>
            <i>{i < current ? '✓' : i + 1}</i>{s}
          </span>
        </React.Fragment>
      ))}
    </div>
  );
}

export function Rows({ rows }: { rows: [React.ReactNode, React.ReactNode][] }) {
  return <div className="mj-rows">{rows.map(([k, v], i) => <div key={i}><span>{k}</span><span>{v}</span></div>)}</div>;
}

export function Check({ done, children }: { done: boolean; children: React.ReactNode }) {
  return <div className="mj-check" data-done={done}><i aria-hidden>✓</i><span>{children}</span><span className="mj-sr-only">{done ? 'done' : 'to do'}</span></div>;
}

/** Add to Calendar: an .ics file of the visit. */
export function downloadICS({ title, start, end, location, description }: { title: string; start: Date; end: Date; location?: string; description?: string }) {
  const f = (d: Date) => d.toISOString().replace(/[-:]/g, '').replace(/\.\d{3}/, '');
  const esc = (s: string) => s.replace(/[\\,;]/g, (c) => `\\${c}`).replace(/\n/g, '\\n');
  const ics = ['BEGIN:VCALENDAR', 'VERSION:2.0', 'PRODID:-//OneClub//Member App//EN', 'BEGIN:VEVENT', `UID:${f(start)}-${Math.random().toString(36).slice(2)}@oneclub`,
    `DTSTAMP:${f(new Date())}`, `DTSTART:${f(start)}`, `DTEND:${f(end)}`, `SUMMARY:${esc(title)}`, location ? `LOCATION:${esc(location)}` : '',
    description ? `DESCRIPTION:${esc(description)}` : '', 'END:VEVENT', 'END:VCALENDAR'].filter(Boolean).join('\r\n');
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob([ics], { type: 'text/calendar' }));
  a.download = `${title.replace(/[^\w]+/g, '-').toLowerCase()}.ics`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}
