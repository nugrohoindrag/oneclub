import React, { useContext, useId, useState } from 'react';
import { Icon } from '../communication/Icon.js';

// OneClub Design System — dashboard kit: the building blocks of every
// dashboard page (Back Office home, Finance, HR, Management Executive Overview
// and the domain dashboards) and of the summary tiles, drawn after the
// dashboard overview reference (docs/product/dashboard-ui.webp). Styles and
// tokens: src/tokens/dashboard.css. Only the content differs per domain.

/** A select option of the kit's pills. */
export interface DashOption { value: string; label: string }

export interface DashLinkProps {
  to: string;
  className?: string;
  title?: string;
  style?: React.CSSProperties;
  children?: React.ReactNode;
  'aria-label'?: string;
  [data: `data-${string}`]: unknown;
}

/** Plain anchor: the default link of the kit outside a router. */
function Anchor({ to, ...rest }: DashLinkProps) {
  return <a href={to} {...rest} />;
}

/**
 * The link the kit renders (cards, buttons, rows that open a page). The
 * design system has no router; an app provides its router link here
 * (the Staff App shell provides react-router's Link).
 */
export const DashLinkContext = React.createContext<React.ComponentType<DashLinkProps>>(Anchor);

function Link(props: DashLinkProps) {
  const L = useContext(DashLinkContext);
  return <L {...props} />;
}

export type DashTone = 'plain' | 'green' | 'red' | 'blue' | 'dark';

/** The 12-column grid of a dashboard page. */
export function DashGrid({ children }: { children: React.ReactNode }) {
  return <div className="oc-dash">{children}</div>;
}

/** Page title row of a dashboard: title, subtitle and the page controls (period pills). */
export function DashHead({ title, sub, controls }: { title: string; sub?: React.ReactNode; controls?: React.ReactNode }) {
  React.useEffect(() => {
    document.title = title;
  }, [title]);
  return (
    <div className="oc-dash-head">
      <div><h1>{title}</h1>{sub && <p>{sub}</p>}</div>
      {controls && <div className="oc-dash-controls">{controls}</div>}
    </div>
  );
}

/** Icon in a circle: outlined (plain) or filled with a tone. */
export function DashIcon({ name, tone = 'plain' }: { name: string; tone?: DashTone }) {
  return <span className="oc-dash-icon" data-tone={tone}><Icon name={name} size={20} /></span>;
}

/**
 * A dashboard card: icon, title, controls (period pills) and a circle
 * action in the head; white, muted (grey) or dark.
 */
export function DashCard({ icon, tone, title, controls, action, children, span = 4, rows, variant = 'white', className, style }: {
  icon?: string; tone?: DashTone; title?: React.ReactNode; controls?: React.ReactNode; action?: React.ReactNode; children?: React.ReactNode;
  /** Columns of the 12-column grid (12 on small screens). */
  span?: number; rows?: number; variant?: 'white' | 'muted' | 'dark'; className?: string; style?: React.CSSProperties;
}) {
  return (
    <section className={`oc-dash-card${className ? ` ${className}` : ''}`} data-variant={variant}
      data-span={span} style={{ gridRow: rows ? `span ${rows}` : undefined, ...style }}>
      {(icon || title || controls || action) && (
        <div className="oc-dash-card-head">
          {icon && <DashIcon name={icon} tone={tone} />}
          {title && <h2>{title}</h2>}
          <span className="oc-spacer" />
          {controls}
          {action}
        </div>
      )}
      {children}
    </section>
  );
}

/** Uppercase pill select (period, currency, scope). */
export function PillSelect({ value, onChange, options, label }: { value: string; onChange: (v: string) => void; options: DashOption[]; label: string }) {
  return (
    <select className="oc-dash-pill" aria-label={label} value={value} onChange={(e) => onChange(e.target.value)}>
      {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  );
}

/** The last `count` months as pill options ("JUNE 2025"), newest first. */
export function monthOptions(locale: string, count = 13, from = new Date()): DashOption[] {
  const fmt = new Intl.DateTimeFormat(locale === 'en' ? 'en-US' : 'id-ID', { month: 'long', year: 'numeric' });
  return Array.from({ length: count }, (_, i) => {
    const d = new Date(from.getFullYear(), from.getMonth() - i, 1);
    return { value: `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`, label: fmt.format(d) };
  });
}

/** The ↗ arrow of the kit: a glyph, it is not in the self-hosted icon subset. */
function NorthEast({ size }: { size: number }) {
  return <span className="oc-dash-glyph" style={{ fontSize: size }} aria-hidden>↗</span>;
}

/** Round button with an icon (arrow, close, filter), dark or light. */
export function CircleButton({ icon, arrow, label, to, onClick, dark, dot }: {
  icon?: string; arrow?: boolean; label: string; to?: string; onClick?: () => void; dark?: boolean; dot?: boolean;
}) {
  const body = <>{arrow ? <NorthEast size={18} /> : <Icon name={icon ?? 'chevron_right'} size={18} />}{dot && <span className="oc-dash-dot" />}</>;
  return to
    ? <Link to={to} className="oc-dash-circle" data-dark={dark || undefined} aria-label={label} title={label}>{body}</Link>
    : <button type="button" className="oc-dash-circle" data-dark={dark || undefined} aria-label={label} title={label} onClick={onClick}>{body}</button>;
}

/** Pill buttons of a card: blue (primary), dark and grey. */
export function DashButton({ children, icon, tone = 'grey', to, onClick }: {
  children: React.ReactNode; icon?: string; tone?: 'blue' | 'dark' | 'grey'; to?: string; onClick?: () => void;
}) {
  const body = <>{icon && <Icon name={icon} size={18} />}{children}</>;
  return to
    ? <Link to={to} className="oc-dash-btn" data-tone={tone}>{body}</Link>
    : <button type="button" className="oc-dash-btn" data-tone={tone} onClick={onClick}>{body}</button>;
}

/**
 * A formatted amount with its fraction (decimals, or the compact unit)
 * set smaller: "Rp 9" + ",98 M", "$5,000" + ".00".
 */
export function Amount({ text, size = 'lg', muted }: { text: string; size?: 'hero' | 'lg' | 'md' | 'sm'; muted?: boolean }) {
  // Only decimals (1–2 digits) and compact units shrink: "Rp 527.757.000" keeps its thousands whole.
  const m = /^(.*?\d)([.,]\d{1,2}(?!\d)\s?[A-Za-z]*\.?|\s?[A-Za-z]+\.?)$/.exec(text);
  return (
    <span className="oc-dash-amount" data-size={size} data-muted={muted || undefined}>
      {m ? <>{m[1]}<small>{m[2]}</small></> : text}
    </span>
  );
}

/** Up / down mark in a filled circle. */
function Arrow({ up, good }: { up: boolean; good: boolean }) {
  return <span className="oc-dash-arrow" data-good={good}><Icon name={up ? 'trending_up' : 'trending_down'} size={12} /></span>;
}

/**
 * A change: "+12% Balance increase, good progress" (inline) or a chip
 * with the amount ("↗ Rp 456"). Lower is better for costs (inverse).
 */
export function Delta({ ratio, text, chip, inverse, suffix }: {
  ratio: number | null | undefined; text?: React.ReactNode; chip?: string; inverse?: boolean; suffix?: React.ReactNode;
}) {
  if (ratio == null || !isFinite(ratio)) return null;
  const up = ratio >= 0;
  const good = inverse ? !up : up;
  const pct = `${up ? '+' : '−'}${Math.abs(ratio * 100).toFixed(1)}%`;
  if (chip !== undefined) {
    return (
      <span className="oc-dash-delta-row">
        <span className="oc-dash-chip" data-good={good}><Arrow up={up} good={good} />{chip || pct}</span>
        {suffix && <span className="oc-dash-chip-suffix">{suffix}</span>}
      </span>
    );
  }
  return <span className="oc-dash-delta"><Arrow up={up} good={good} /><strong data-good={good}>{pct}</strong> {text}</span>;
}

/** Grey note line under a figure; <b> parts are highlighted green (or red with tone="bad"). */
export function Note({ children, tone = 'good' }: { children: React.ReactNode; tone?: 'good' | 'bad' }) {
  return <p className="oc-dash-note" data-tone={tone}>{children}</p>;
}

/** Figures side by side with a coloured bar on the left ("Salary · Freelance"). */
export function SplitStats({ items }: { items: { label: string; value: React.ReactNode; color: string; to?: string }[] }) {
  return (
    <div className="oc-dash-split">
      {items.map((i) => {
        const body = <><span>{i.label}</span><strong>{i.value}</strong></>;
        return i.to
          ? <Link key={i.label} to={i.to} className="oc-dash-split-item" style={{ borderColor: i.color }}>{body}</Link>
          : <div key={i.label} className="oc-dash-split-item" style={{ borderColor: i.color }}>{body}</div>;
      })}
    </div>
  );
}

/** Colours of the parts of a breakdown, in order; the rest ("Other") is grey. */
export const DASH_COLORS = ['var(--dash-ink)', 'var(--dash-blue)', 'var(--dash-lime)', 'var(--dash-sky)', 'var(--dash-amber)'];
export const DASH_OTHER = 'var(--dash-grey)';

/**
 * Shares of a whole in one bar: the share above each part, the highlighted
 * part with its amount in a chip, a legend of dots below.
 */
export function SegmentBar({ parts, highlight, format, legend = true }: {
  parts: { label: string; value: number; color?: string }[]; highlight?: number; format: (v: number) => string;
  /** Dots with the part names below (off when a BreakdownList names them). */
  legend?: boolean;
}) {
  const total = parts.reduce((s, p) => s + Math.max(0, p.value), 0) || 1;
  const hi = highlight ?? parts.reduce((b, p, i) => (p.value > parts[b].value ? i : b), 0);
  return (
    <div className="oc-dash-seg">
      <div className="oc-dash-seg-bar">
        {parts.map((p, i) => {
          const w = (Math.max(0, p.value) / total) * 100;
          if (w <= 0) return null;
          return (
            <div key={p.label} className="oc-dash-seg-part" data-hi={i === hi || undefined} style={{ flexGrow: w, flexBasis: 0 }}
              title={`${p.label}: ${format(p.value)} (${w.toFixed(1)}%)`}>
              <span className="oc-dash-seg-top">{i === hi ? <span className="oc-dash-seg-chip">{format(p.value)}</span> : `${Math.round(w)}%`}</span>
              <span className="oc-dash-seg-fill" style={{ background: p.color ?? DASH_COLORS[i % DASH_COLORS.length] }} />
            </div>
          );
        })}
      </div>
      {legend && <div className="oc-dash-legend">
        {parts.map((p, i) => <span key={p.label}><i style={{ background: p.color ?? DASH_COLORS[i % DASH_COLORS.length] }} />{p.label}</span>)}
      </div>}
    </div>
  );
}

/** Rows of a breakdown: colour dot, name, amount and share; each opens its page. */
export function BreakdownList({ rows }: { rows: { label: string; value: string; share?: number | null; color?: string; to?: string }[] }) {
  return (
    <div className="oc-dash-breakdown">
      {rows.map((r, i) => {
        const body = (
          <>
            <i style={{ background: r.color ?? DASH_COLORS[i % DASH_COLORS.length] }} />
            <span className="oc-dash-breakdown-label">{r.label}</span>
            <strong>{r.value}</strong>
            {r.share != null && <small>{(r.share * 100).toFixed(1)}%</small>}
          </>
        );
        return r.to
          ? <Link key={r.label} to={r.to} className="oc-dash-breakdown-row">{body}</Link>
          : <div key={r.label} className="oc-dash-breakdown-row">{body}</div>;
      })}
    </div>
  );
}

/** Semicircle gauge on a dark panel: the share reached of a target. */
export function Gauge({ title, ratio, value, caption, sub, action }: {
  title: string; ratio: number | null; value: React.ReactNode; caption?: string; sub?: React.ReactNode; action?: React.ReactNode;
}) {
  const r = 80;
  const len = Math.PI * r;
  const p = Math.max(0, Math.min(1, ratio ?? 0));
  const angle = Math.PI * (1 - p);
  const knob = { x: 100 + r * Math.cos(angle), y: 100 - r * Math.sin(angle) };
  return (
    <div className="oc-dash-gauge">
      <div className="oc-dash-gauge-head"><h3>{title}</h3>{action}</div>
      <div className="oc-dash-gauge-body">
        <svg viewBox="0 0 200 110" role="img" aria-label={`${title}: ${Math.round(p * 100)}%`}>
          <path d="M20 100 A80 80 0 0 1 180 100" fill="none" stroke="var(--dash-gauge-track)" strokeWidth="16" strokeLinecap="round" />
          {ratio != null && <path d="M20 100 A80 80 0 0 1 180 100" fill="none" stroke="var(--dash-blue)" strokeWidth="16" strokeLinecap="round"
            strokeDasharray={`${len * p} ${len}`} />}
          {ratio != null && <circle cx={knob.x} cy={knob.y} r="7" fill="#fff" stroke="var(--dash-blue)" strokeWidth="3" />}
        </svg>
        <div className="oc-dash-gauge-text">
          {caption && <span>{caption}</span>}
          <strong>{value}</strong>
          {sub && <small>{sub}</small>}
        </div>
      </div>
    </div>
  );
}

/** One line of a goals list: name, share reached and a small bar, opening its page. */
export function ProgressRow({ label, ratio, to, hint }: { label: string; ratio: number | null; to?: string; hint?: string }) {
  const p = ratio == null ? null : Math.max(0, ratio);
  const tone = p == null ? 'none' : p >= 0.9 ? 'good' : p >= 0.6 ? 'mid' : 'bad';
  const body = (
    <>
      <span className="oc-dash-prog-label" title={hint}>{label}</span>
      <span className="oc-dash-prog-pct">{p == null ? '—' : `${Math.round(p * 100)}%`}</span>
      <span className="oc-dash-prog-track"><span data-tone={tone} style={{ width: `${Math.min(1, p ?? 0) * 100}%` }} /></span>
      <span className="oc-dash-circle oc-dash-circle-sm" aria-hidden><Icon name="chevron_right" size={16} /></span>
    </>
  );
  return to ? <Link to={to} className="oc-dash-prog">{body}</Link> : <div className="oc-dash-prog">{body}</div>;
}

export interface ColumnPoint {
  label: string;
  /** Main series (blue on the highlighted bar). */
  a: number;
  /** Second series (lime on the highlighted bar), optional. */
  b?: number | null;
  state?: 'past' | 'current' | 'future';
  title?: string;
}

/**
 * Column chart of the reference: past columns grey, the current one blue with
 * the second series in lime and a dark tooltip, coming ones hatched.
 */
export function ColumnChart({ points, aLabel, bLabel, format, axis }: {
  points: ColumnPoint[]; aLabel: string; bLabel?: string; format: (v: number) => string; axis?: string;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const max = Math.max(1, ...points.map((p) => Math.max(p.a, p.b ?? 0)));
  const cur = points.findIndex((p) => p.state === 'current');
  const active = hover ?? (cur >= 0 ? cur : points.length - 1);
  const id = useId();
  return (
    <div className="oc-dash-chart" role="img" aria-label={`${aLabel}${bLabel ? ` and ${bLabel}` : ''} per period`}>
      {axis && active >= 0 && (
        <div className="oc-dash-chart-axis" style={{ bottom: `calc(${(points[active].a / max) * 100}% * 0.86 + 28px)` }}><span>{axis}</span></div>
      )}
      <div className="oc-dash-chart-cols">
        {points.map((p, i) => {
          const on = i === active;
          const state = p.state ?? 'past';
          return (
            <div key={`${id}-${p.label}-${i}`} className="oc-dash-col" onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)}
              onFocus={() => setHover(i)} tabIndex={0} aria-label={`${p.label}: ${aLabel} ${format(p.a)}${p.b != null && bLabel ? `, ${bLabel} ${format(p.b)}` : ''}`}>
              <div className="oc-dash-col-track">
                <div className="oc-dash-col-bar" data-state={on ? 'current' : state} style={{ height: `${Math.max(4, (p.a / max) * 100)}%` }}>
                  {on && p.b != null && <span className="oc-dash-col-b" style={{ height: `${Math.min(100, (p.b / Math.max(p.a, 1)) * 100)}%` }} />}
                </div>
                {on && (
                  <div className="oc-dash-tip" data-side={i > points.length / 2 ? 'left' : 'right'}>
                    <span className="oc-dash-tip-title">{p.title ?? p.label}</span>
                    <span className="oc-dash-tip-row"><i style={{ background: 'var(--dash-blue)' }} />{aLabel}<b>{format(p.a)}</b></span>
                    {p.b != null && bLabel && <span className="oc-dash-tip-row"><i style={{ background: 'var(--dash-lime)' }} />{bLabel}<b>{format(p.b)}</b></span>}
                  </div>
                )}
              </div>
              <span className="oc-dash-col-label">{p.label}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

/** Stacked squares per period (the balance card's pattern): taller = higher, the latest darkest. */
export function HeatBars({ values }: { values: number[] }) {
  const max = Math.max(1, ...values);
  return (
    <div className="oc-dash-heat" aria-hidden>
      {values.map((v, i) => {
        const n = Math.max(1, Math.round((v / max) * 6));
        return (
          <div key={i} className="oc-dash-heat-col">
            {Array.from({ length: 6 }, (_, j) => {
              const lit = 5 - j < n;
              const level = !lit ? 0 : 5 - j === n - 1 ? (i === values.length - 1 ? 3 : 2) : 1;
              return <span key={j} data-level={level} />;
            })}
          </div>
        );
      })}
    </div>
  );
}

/** Promotion-style card: badge, title, a dark pill and a round arrow. */
export function PromoCard({ badge, title, cta, to, span = 4 }: { badge: string; title: string; cta: string; to: string; span?: number }) {
  return (
    <section className="oc-dash-card oc-dash-promo" data-span={span}>
      <div className="oc-dash-card-head">
        <span className="oc-spacer" />
        <Link to={to} className="oc-dash-btn oc-dash-btn-sm" data-tone="dark">{cta}</Link>
        <CircleButton arrow label={cta} to={to} dark />
      </div>
      <div className="oc-dash-promo-body">
        <span className="oc-dash-badge">{badge}</span>
        <h3>{title}</h3>
      </div>
    </section>
  );
}

/** Small figure card ("Today Received"): label pill, amount, change chip, round arrow. */
export function MiniCard({ label, value, delta, to, span = 2, onClear }: {
  label: string; value: React.ReactNode; delta?: React.ReactNode; to?: string; span?: number; onClear?: () => void;
}) {
  return (
    <section className="oc-dash-card oc-dash-mini" data-span={span}>
      <span className="oc-dash-tag">{label}</span>
      <div className="oc-dash-mini-value">{value}{delta}</div>
      <div className="oc-dash-mini-foot">
        {onClear ? <CircleButton icon="close" label="Dismiss" onClick={onClear} /> : <span />}
        {to && <CircleButton arrow label={`Open ${label}`} to={to} dark />}
      </div>
    </section>
  );
}

/** Link card to a report ("Track & Print Report · Financial Report"). */
export function ReportCard({ label, title, to, icon = 'description', span = 2 }: { label: string; title: string; to: string; icon?: string; span?: number }) {
  return (
    <Link to={to} className="oc-dash-card oc-dash-report" data-span={span}>
      <div className="oc-dash-card-head">
        <span className="oc-dash-circle oc-dash-circle-sm" aria-hidden><Icon name={icon} size={16} /></span>
        <span className="oc-spacer" />
        <span className="oc-dash-circle" aria-hidden><NorthEast size={18} /></span>
      </div>
      <div className="oc-dash-report-body">
        <span className="oc-dash-tag">{label}</span>
        <h3>{title}</h3>
      </div>
    </Link>
  );
}

export type DashStatus = 'good' | 'bad' | 'warn' | 'neutral';

/** Solid status pill of the dashboard tables (Complete / Canceled). */
export function DashStatusPill({ tone, children }: { tone: DashStatus; children: React.ReactNode }) {
  return <span className="oc-dash-status" data-tone={tone}>{children}</span>;
}

export interface DashColumn<T> {
  key: string;
  header: string;
  render: (row: T) => React.ReactNode;
  align?: 'left' | 'right' | 'center';
}

/**
 * The table card of a dashboard ("Transaction history"): light uppercase
 * header, a search pill and a filter button in the head, an info mark per
 * row (its tooltip, or the row's page).
 */
export function DashTable<T>({ title, icon, rows, columns, rowKey, search, onSearch, filter, empty, info, onRow, span = 12, action }: {
  title: string; icon: string; rows: T[]; columns: DashColumn<T>[]; rowKey: (r: T) => string;
  search?: string; onSearch?: (v: string) => void; filter?: React.ReactNode; empty?: string; info?: (r: T) => string | undefined;
  onRow?: (r: T) => void; span?: number; action?: React.ReactNode;
}) {
  return (
    <DashCard icon={icon} title={title} span={span} className="oc-dash-table-card"
      controls={<>
        {onSearch && (
          <label className="oc-dash-search"><Icon name="search" size={18} />
            <input type="search" value={search ?? ''} onChange={(e) => onSearch(e.target.value)} placeholder="Search" aria-label={`Search ${title}`} />
          </label>
        )}
        {filter}
      </>} action={action}>
      {rows.length === 0 ? <p className="oc-dash-empty">{empty ?? 'Nothing to show.'}</p> : (
        <div className="oc-table-wrap">
          <table className="oc-dash-table">
            <thead>
              <tr>
                {columns.map((c) => <th key={c.key} style={{ textAlign: c.align }}>{c.header}</th>)}
                {info && <th aria-label="Details" />}
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={rowKey(r)} data-clickable={!!onRow || undefined} onClick={onRow ? () => onRow(r) : undefined}>
                  {columns.map((c) => <td key={c.key} style={{ textAlign: c.align }}>{c.render(r)}</td>)}
                  {info && <td className="oc-dash-info"><span title={info(r)} aria-label={info(r)}><Icon name="info" size={18} /></span></td>}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </DashCard>
  );
}

/** Name cell of a dashboard table: a small icon tile and the name. */
export function DashName({ icon, name, sub, tone }: { icon: string; name: React.ReactNode; sub?: React.ReactNode; tone?: DashTone }) {
  return (
    <span className="oc-dash-name">
      <span className="oc-dash-tile" data-tone={tone ?? 'plain'}><Icon name={icon} size={16} /></span>
      <span><span>{name}</span>{sub && <small>{sub}</small>}</span>
    </span>
  );
}

/** Solid colour of a board lane head (the dashboard's blue, ink, lime, sky, amber; green won, red lost). */
export type BoardTone = 'blue' | 'dark' | 'lime' | 'sky' | 'amber' | 'green' | 'red';

/** The lane colours in turn for open stages. */
export const BOARD_TONES: BoardTone[] = ['blue', 'dark', 'lime', 'sky', 'amber'];

/** A column of a board: title, count, badge (probability), its total and its colour. */
export interface BoardLane {
  key: string;
  title: string;
  count: number;
  badge?: string;
  /** Total of the column (an amount, drawn with Amount). */
  total?: string;
  tone?: BoardTone;
  children?: React.ReactNode;
}

/**
 * A board (pipeline, recruitment stages): white lanes side by side that
 * scroll sideways, each under a solid coloured head (title, count, badge,
 * total) like the Expense card's panel, holding BoardCards.
 */
export function Board({ label, lanes, empty = 'Nothing here' }: { label: string; lanes: BoardLane[]; empty?: string }) {
  return (
    <div className="oc-dash-board" role="list" aria-label={label}>
      {lanes.map((l, i) => (
        <section key={l.key} className="oc-dash-lane" role="listitem" aria-label={l.title}>
          <header className="oc-dash-lane-head" data-tone={l.tone ?? BOARD_TONES[i % BOARD_TONES.length]}>
            <div className="oc-dash-lane-title">
              <h3>{l.title}</h3>
              <span className="oc-dash-count">{l.count}</span>
              <span className="oc-spacer" />
              {l.badge && <span className="oc-dash-lane-badge">{l.badge}</span>}
            </div>
            {l.total && <Amount text={l.total} size="md" />}
          </header>
          <div className="oc-dash-lane-items">
            {React.Children.count(l.children) > 0 ? l.children : <p className="oc-dash-lane-empty">{empty}</p>}
          </div>
        </section>
      ))}
    </div>
  );
}

/** A chip of a board card: icon and text, soft (plain) or solid red / green / blue. */
export interface BoardChip { icon?: string; text: string; tone?: 'plain' | 'red' | 'green' | 'blue' }

/**
 * A card on a board: tag (number) and a dark open arrow, title, the party,
 * the amount, chips (close date, source, rating), the owner's avatar and a
 * footer (the blue move pill).
 */
export function BoardCard({ tag, title, party, amount, chips, owner, onOpen, footer }: {
  tag?: string; title: React.ReactNode; party?: React.ReactNode; amount?: string; chips?: (BoardChip | null | undefined | false)[];
  owner?: string; onOpen?: () => void; footer?: React.ReactNode;
}) {
  const shown = (chips ?? []).filter((c): c is BoardChip => !!c && !!c.text);
  return (
    <article className="oc-dash-kcard">
      <div className="oc-dash-kcard-head">
        {tag && <span className="oc-dash-tag">{tag}</span>}
        <span className="oc-spacer" />
        {onOpen && <CircleButton arrow dark label="Open" onClick={onOpen} />}
      </div>
      {onOpen
        ? <button type="button" className="oc-dash-kcard-title" onClick={onOpen}>{title}</button>
        : <strong className="oc-dash-kcard-title">{title}</strong>}
      {party && <span className="oc-dash-kcard-party"><span className="oc-dash-tile" data-tone="blue"><Icon name="person" size={14} /></span>{party}</span>}
      {amount && <Amount text={amount} size="sm" />}
      {shown.length > 0 && (
        <div className="oc-dash-kcard-chips">
          {shown.map((c, i) => <span key={i} className="oc-dash-kchip" data-tone={c.tone ?? 'plain'}>{c.icon && <Icon name={c.icon} size={14} />}{c.text}</span>)}
        </div>
      )}
      {(owner || footer) && (
        <div className="oc-dash-kcard-foot">
          {owner && <span title={owner}><Avatar name={owner} /></span>}
          {footer}
        </div>
      )}
    </article>
  );
}

/** A ranked entry (leaderboards, top spenders): rank, previous rank, name and its value. */
export interface RankItem {
  key: string;
  rank: number;
  /** Rank in the previous period; null = new in the ranking, undefined = not tracked. */
  previousRank?: number | null;
  name: string;
  sub?: React.ReactNode;
  /** The formatted value ("Rp 101.000.000", "12 rounds"). */
  value: string;
  onOpen?: () => void;
}

/** Movement against the previous period: ▲ 3 (green), ▼ 2 (red), NEW (blue), = (grey). */
export function RankMove({ rank, previousRank }: { rank: number; previousRank?: number | null }) {
  if (previousRank === undefined) return null;
  if (previousRank === null) return <span className="oc-dash-move" data-tone="new">NEW</span>;
  const d = previousRank - rank;
  if (d === 0) return <span className="oc-dash-move" data-tone="same" title="Same rank">=</span>;
  return <span className="oc-dash-move" data-tone={d > 0 ? 'up' : 'down'} title={`Was #${previousRank}`}>{d > 0 ? '▲' : '▼'} {Math.abs(d)}</span>;
}

/** Rank in a medal circle: gold, silver and bronze for the first three. */
export function RankBadge({ rank }: { rank: number }) {
  return <span className="oc-dash-medal" data-rank={rank <= 3 ? rank : undefined}>{rank}</span>;
}

/** Initials of a name in a round avatar. */
export function Avatar({ name, tone = 'lime' }: { name: string; tone?: 'lime' | 'blue' | 'dark' | 'white' }) {
  const text = name.split(/\s+/).filter(Boolean).slice(0, 2).map((w) => w.charAt(0).toUpperCase()).join('');
  return <span className="oc-dash-avatar" data-tone={tone} aria-hidden>{text}</span>;
}

/**
 * The first three of a ranking on a podium: #1 in the middle on the tallest
 * dark step, #2 blue on the left, #3 lime on the right.
 */
export function Podium({ items, empty = 'Nothing ranked yet.' }: { items: RankItem[]; empty?: string }) {
  const top = items.slice(0, 3);
  if (top.length === 0) return <p className="oc-dash-empty">{empty}</p>;
  const order = [top[1], top[0], top[2]].filter((x): x is RankItem => !!x);
  return (
    <div className="oc-dash-podium">
      {order.map((it) => (
        <div key={it.key} className="oc-dash-podium-col" data-place={it.rank}>
          <div className="oc-dash-podium-who">
            <Avatar name={it.name} tone={it.rank === 1 ? 'dark' : it.rank === 2 ? 'blue' : 'lime'} />
            {it.onOpen
              ? <button type="button" className="oc-dash-podium-name" onClick={it.onOpen}>{it.name}</button>
              : <strong className="oc-dash-podium-name">{it.name}</strong>}
            {it.sub && <span className="oc-dash-podium-sub">{it.sub}</span>}
          </div>
          <div className="oc-dash-podium-step">
            <span className="oc-dash-podium-rank">#{it.rank}</span>
            <Amount text={it.value} size={it.rank === 1 ? 'md' : 'sm'} />
            <RankMove rank={it.rank} previousRank={it.previousRank} />
          </div>
        </div>
      ))}
    </div>
  );
}

/** A part of a ranking bar (a business line of the spend). */
export interface RankPart { label: string; value: number; color: string }

/**
 * Horizontal ranking bars: rank, name, a bar as long as the value against
 * the first, split into coloured parts, the value and the movement.
 */
export function RankBars({ rows, format, legend, ranked = true }: {
  rows: (RankItem & { total: number; parts?: RankPart[] })[]; format: (v: number) => string; legend?: { label: string; color: string }[];
  /** Medal badges with the rank (off for plain bars, such as activities). */
  ranked?: boolean;
}) {
  const max = Math.max(1, ...rows.map((r) => r.total));
  return (
    <div className="oc-dash-rankbars">
      {legend && <div className="oc-dash-legend">{legend.map((l) => <span key={l.label}><i style={{ background: l.color }} />{l.label}</span>)}</div>}
      {rows.map((r) => {
        const parts = r.parts?.filter((p) => p.value > 0) ?? [];
        return (
          <div key={r.key} className="oc-dash-rankbar">
            {ranked && <RankBadge rank={r.rank} />}
            <div className="oc-dash-rankbar-main">
              <div className="oc-dash-rankbar-head">
                {r.onOpen
                  ? <button type="button" className="oc-dash-rankbar-name" onClick={r.onOpen}>{r.name}</button>
                  : <span className="oc-dash-rankbar-name">{r.name}</span>}
                {ranked && <RankMove rank={r.rank} previousRank={r.previousRank} />}
                <span className="oc-spacer" />
                <strong className="oc-dash-num">{r.value}</strong>
              </div>
              <div className="oc-dash-rankbar-track" title={parts.map((p) => `${p.label}: ${format(p.value)}`).join(' · ') || r.value}>
                <div className="oc-dash-rankbar-fill" style={{ width: `${Math.max(2, (r.total / max) * 100)}%` }}>
                  {parts.length > 0
                    ? parts.map((p) => <span key={p.label} style={{ flexGrow: p.value, background: p.color }} />)
                    : <span style={{ flexGrow: 1, background: 'var(--dash-blue)' }} />}
                </div>
              </div>
            </div>
          </div>
        );
      })}
    </div>
  );
}
