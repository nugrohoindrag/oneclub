/**
 * @license MIT
 * Entity Cards — Morphic Design System
 * 08. Data Display / Core
 *
 * Spec: Morphic-Design-System-adjusted.md §4 "Domain-Neutral Design Rules"
 *
 *   Avoid in Core:  Warehouse Card · Asset Card · Delivery Card · Employee Card
 *                   Invoice Card · Polda Card
 *   Prefer:         Entity Card · Resource Card · Location Card
 *                   Transaction Card · Profile Card · Activity Card · Status Card
 *
 * These are the generic Core cards. Industry-specific compositions belong in
 * the Examples / Templates layer (`src/examples`, `src/templates`) and should
 * be built by configuring these through props — not by forking them.
 *
 *   Generic:  Resource Card
 *   Example:  Asset Management Resource Card
 *
 * Anatomy is shared across the family (§5 Component Specification Standard):
 *
 *   Card
 *   ├── Header
 *   │   ├── Overline (code / category / metadata)
 *   │   ├── Title
 *   │   └── Status
 *   ├── Body        (metric, progress, or description)
 *   └── Footer      (supporting metric + action)
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';
import { motion } from 'motion/react';
import { Icon } from '../communication/Icon.js';
import { Button } from '../actions/Button.js';
import { StatusBadge } from '../data-display/DataDisplaySuite.js';
import { M3_TRANSITIONS, useReducedMotionSafe } from '../../motion/index.js';

/* ========================================================================= */
/* Shared types & shell                                                      */
/* ========================================================================= */

export type EntityStatus = 'online' | 'warning' | 'offline' | 'idle' | 'busy';

/** Semantic tone for status-driven surfaces (§11 State Contract). */
export type EntityTone = 'neutral' | 'success' | 'warning' | 'error' | 'info';

const TONE_TOKENS: Record<EntityTone, { fg: string; bg: string }> = {
  neutral: {
    fg: 'var(--md-sys-color-on-surface-variant)',
    bg: 'var(--md-sys-color-surface-container-high)',
  },
  success: {
    fg: 'var(--md-sys-color-success)',
    bg: 'var(--md-sys-color-success-container)',
  },
  warning: {
    fg: 'var(--md-sys-color-warning)',
    bg: 'var(--md-sys-color-warning-container)',
  },
  error: {
    fg: 'var(--md-sys-color-error)',
    bg: 'var(--md-sys-color-error-container)',
  },
  info: {
    fg: 'var(--md-sys-color-info)',
    bg: 'var(--md-sys-color-info-container)',
  },
};

export interface EntityCardBaseProps {
  className?: string;
  /** Render the card one tonal step up, for nesting inside another card (§19). */
  nested?: boolean;
}

/**
 * The shared Morphic card shell: surface contrast + a 1px low-contrast border,
 * no resting shadow (§19, §21, §22).
 */
const cardShellStyle = (nested?: boolean): React.CSSProperties => ({
  borderRadius: 'var(--radius-card)',
  backgroundColor: nested
    ? 'var(--md-sys-color-surface-container)'
    : 'var(--md-sys-color-surface)',
  border: '1px solid var(--md-sys-color-border)',
  padding: 'var(--md-sys-padding-card)',
  boxShadow: 'var(--md-sys-elevation-level0)',
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--md-sys-spacing-3)',
});

/**
 * The animated shell every card in this family renders through.
 *
 * §19's card hover, in order: brighter surface → subtle elevation →
 * translateY(-1px). It lives here in Motion rather than in CSS because these
 * cards do not carry the `.morphic-card` class, and a card that lifts in one
 * file and not another is how a system loses its rhythm.
 *
 * Under reduced motion (§9) the surface and border still change — the state is
 * never hidden — but nothing moves.
 */
const CardShell: React.FC<{
  className?: string;
  nested?: boolean;
  style?: React.CSSProperties;
  children: React.ReactNode;
}> = ({ className = '', nested, style, children }) => {
  const reduced = useReducedMotionSafe();

  return (
    <motion.div
      className={className}
      initial={false}
      whileHover={{
        y: reduced ? 0 : -1,
        backgroundColor: 'var(--md-sys-color-surface-container-low)',
        borderColor: 'var(--md-sys-color-outline-variant)',
        boxShadow: 'var(--md-sys-elevation-level1)',
      }}
      transition={M3_TRANSITIONS.card}
      style={{
        ...cardShellStyle(nested),
        // `elevation-level0` is the keyword `none`, which nothing can
        // interpolate out of. An explicit zero shadow looks identical and
        // lets the level-1 lift fade in and back out.
        boxShadow: '0 0 0 0 rgba(0, 0, 0, 0)',
        ...style,
      }}
    >
      {children}
    </motion.div>
  );
};

const overlineStyle: React.CSSProperties = {
  /* §6 — metadata 11–12px, 400–500 */
  fontSize: 'var(--md-sys-typescale-meta-size-sm)',
  fontWeight: 500,
  color: 'var(--md-sys-color-on-surface-variant)',
};

const titleStyle: React.CSSProperties = {
  margin: '2px 0 0',
  /* §6 — section title 16–18px, 600–650 */
  fontSize: 'var(--md-sys-typescale-section-title-size)',
  fontWeight: 620,
  lineHeight: 'var(--md-sys-typescale-section-title-line-height)',
  letterSpacing: 'var(--md-sys-typescale-section-title-tracking)',
  color: 'var(--md-sys-color-on-surface)',
};

const metaStyle: React.CSSProperties = {
  fontSize: 'var(--md-sys-typescale-meta-size)',
  color: 'var(--md-sys-color-on-surface-variant)',
};

const footerStyle: React.CSSProperties = {
  display: 'flex',
  justifyContent: 'space-between',
  alignItems: 'center',
  borderTop: '1px solid var(--md-sys-color-border)',
  paddingTop: 'var(--md-sys-spacing-3)',
};

/* ========================================================================= */
/* Progress track — shared by Resource and Transaction                        */
/* ========================================================================= */

export interface ProgressTrackProps {
  /** 0–100 */
  value: number;
  /** Above this value the track switches to the warning tone. */
  warnAbove?: number;
  /** Above this value the track switches to the error tone. */
  errorAbove?: number;
  label?: string;
  valueLabel?: string;
}

export const ProgressTrack: React.FC<ProgressTrackProps> = ({
  value,
  warnAbove,
  errorAbove,
  label,
  valueLabel,
}) => {
  const clamped = Math.max(0, Math.min(100, value));
  const fill =
    errorAbove !== undefined && clamped > errorAbove
      ? 'var(--md-sys-color-error)'
      : warnAbove !== undefined && clamped > warnAbove
        ? 'var(--md-sys-color-warning)'
        : 'var(--md-sys-color-primary)';

  return (
    <div>
      {(label || valueLabel) && (
        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            ...metaStyle,
            marginBottom: 'var(--md-sys-spacing-1)',
          }}
        >
          <span>{label}</span>
          <strong style={{ color: 'var(--md-sys-color-on-surface)' }}>{valueLabel}</strong>
        </div>
      )}
      <div
        role="progressbar"
        aria-valuenow={clamped}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={label}
        style={{
          height: '6px',
          borderRadius: 'var(--radius-pill)',
          backgroundColor: 'var(--md-sys-color-surface-container-high)',
          overflow: 'hidden',
        }}
      >
        {/* §19 — the fill grows to its new value on the chart duration, and
            the tone crossfades with it rather than snapping at the threshold. */}
        <motion.div
          initial={{ width: 0 }}
          animate={{ width: `${clamped}%`, backgroundColor: fill }}
          transition={M3_TRANSITIONS.chart}
          style={{
            height: '100%',
            borderRadius: 'var(--radius-pill)',
          }}
        />
      </div>
    </div>
  );
};

/* ========================================================================= */
/* 1. EntityCard — the generic base. Any titled record with metadata.         */
/* ========================================================================= */

export interface EntityCardProps extends EntityCardBaseProps {
  /** Small metadata line above the title: code, category, reference. */
  overline?: string;
  title: string;
  description?: string;
  icon?: string;
  status?: EntityStatus;
  /** Right-hand slot in the header, replacing the status badge. */
  headerAction?: React.ReactNode;
  /** Free-form body content. */
  children?: React.ReactNode;
  footerLabel?: React.ReactNode;
  actionLabel?: string;
  onAction?: () => void;
}

export const EntityCard: React.FC<EntityCardProps> = ({
  overline,
  title,
  description,
  icon,
  status,
  headerAction,
  children,
  footerLabel,
  actionLabel,
  onAction,
  nested,
  className = '',
}) => (
  <CardShell className={`morphic-entity-card ${className}`} nested={nested}>
    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 'var(--md-sys-spacing-3)' }}>
      <div style={{ display: 'flex', gap: 'var(--md-sys-spacing-3)', alignItems: 'flex-start', minWidth: 0 }}>
        {icon && (
          <span
            aria-hidden="true"
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              justifyContent: 'center',
              width: '36px',
              height: '36px',
              flexShrink: 0,
              borderRadius: 'var(--radius-sm)',
              backgroundColor: 'var(--md-sys-color-primary-container)',
              color: 'var(--md-sys-color-on-primary-container)',
            }}
          >
            <Icon name={icon} size={18} />
          </span>
        )}
        <div style={{ minWidth: 0 }}>
          {overline && <span style={overlineStyle}>{overline}</span>}
          <h4 style={titleStyle}>{title}</h4>
          {description && (
            <p style={{ margin: '4px 0 0', ...metaStyle, fontSize: 'var(--md-sys-typescale-body-size-sm)' }}>
              {description}
            </p>
          )}
        </div>
      </div>
      {headerAction ?? (status && <StatusBadge status={status} size="sm" />)}
    </div>

    {children}

    {(footerLabel || actionLabel) && (
      <div style={footerStyle}>
        <span style={metaStyle}>{footerLabel}</span>
        {actionLabel && (
          <Button variant="tonal" size="sm" onClick={onAction}>
            {actionLabel}
          </Button>
        )}
      </div>
    )}
  </CardShell>
);

/* ========================================================================= */
/* 2. ResourceCard — a capacity-bearing resource.                             */
/*    Replaces domain forks such as Warehouse / Asset / Machine cards (§4).   */
/* ========================================================================= */

export interface ResourceCardProps extends EntityCardBaseProps {
  /** Code, region, or category shown above the name. */
  overline?: string;
  name: string;
  icon?: string;
  status?: EntityStatus;
  /** Percentage of capacity in use, 0–100. */
  utilization: number;
  /** Human-readable total, e.g. "12,000 units" or "480 GB". */
  capacityLabel?: string;
  utilizationLabel?: string;
  warnAbove?: number;
  errorAbove?: number;
  /** Supporting metric shown in the footer. */
  metricLabel?: string;
  metricValue?: React.ReactNode;
  actionLabel?: string;
  onAction?: () => void;
}

export const ResourceCard: React.FC<ResourceCardProps> = ({
  overline,
  name,
  icon,
  status = 'online',
  utilization,
  capacityLabel,
  utilizationLabel = 'Utilization',
  warnAbove = 75,
  errorAbove = 90,
  metricLabel,
  metricValue,
  actionLabel,
  onAction,
  nested,
  className = '',
}) => (
  <EntityCard
    className={`morphic-resource-card ${className}`}
    nested={nested}
    overline={overline}
    title={name}
    icon={icon}
    status={status}
    footerLabel={
      metricLabel ? (
        <>
          {metricLabel}: <strong style={{ color: 'var(--md-sys-color-on-surface)' }}>{metricValue}</strong>
        </>
      ) : undefined
    }
    actionLabel={actionLabel}
    onAction={onAction}
  >
    <ProgressTrack
      value={utilization}
      warnAbove={warnAbove}
      errorAbove={errorAbove}
      label={utilizationLabel}
      valueLabel={capacityLabel ? `${utilization}% of ${capacityLabel}` : `${utilization}%`}
    />
  </EntityCard>
);

/* ========================================================================= */
/* 3. TransactionCard — anything moving between two parties with progress.    */
/*    Replaces domain forks such as Delivery / Invoice / Payment cards (§4).  */
/* ========================================================================= */

export interface TransactionCardProps extends EntityCardBaseProps {
  /** Reference number, tracking id, invoice number. */
  reference: string;
  /** Issuer, carrier, counterparty. */
  party?: string;
  from?: string;
  to?: string;
  /** 0–100. Omit for a transaction with no progress dimension. */
  progress?: number;
  /** Right-aligned financial or quantitative value (§16). */
  amount?: React.ReactNode;
  /** Label + value shown under the progress track, e.g. "Due" / "ETA". */
  dueLabel?: string;
  dueValue?: string;
  statusLabel?: string;
  tone?: EntityTone;
}

export const TransactionCard: React.FC<TransactionCardProps> = ({
  reference,
  party,
  from,
  to,
  progress,
  amount,
  dueLabel,
  dueValue,
  statusLabel,
  tone = 'success',
  nested,
  className = '',
}) => {
  const toneTokens = TONE_TOKENS[tone];

  return (
    <CardShell className={`morphic-transaction-card ${className}`} nested={nested}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 'var(--md-sys-spacing-3)' }}>
        <div style={{ minWidth: 0 }}>
          {party && <span style={overlineStyle}>{party}</span>}
          <h4 style={titleStyle}>{reference}</h4>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--md-sys-spacing-2)' }}>
          {/* §16 — financial values are right-aligned */}
          {amount !== undefined && (
            <strong
              style={{
                fontSize: 'var(--md-sys-typescale-metric-size-sm)',
                fontWeight: 'var(--md-sys-typescale-metric-weight)' as React.CSSProperties['fontWeight'],
                letterSpacing: 'var(--md-sys-typescale-metric-tracking)',
                fontVariantNumeric: 'tabular-nums',
                color: 'var(--md-sys-color-on-surface)',
              }}
            >
              {amount}
            </strong>
          )}
          {statusLabel && (
            <span
              style={{
                padding: '2px 8px',
                borderRadius: 'var(--radius-pill)',
                backgroundColor: toneTokens.bg,
                color: toneTokens.fg,
                fontSize: 'var(--md-sys-typescale-meta-size-sm)',
                fontWeight: 600,
                whiteSpace: 'nowrap',
              }}
            >
              {statusLabel}
            </span>
          )}
        </div>
      </div>

      {(from || to) && (
        <div style={{ display: 'flex', justifyContent: 'space-between', gap: 'var(--md-sys-spacing-3)', ...metaStyle }}>
          {from && <span>From: {from}</span>}
          {to && <span>To: {to}</span>}
        </div>
      )}

      {progress !== undefined && (
        <div>
          <ProgressTrack value={progress} />
          {(dueLabel || dueValue) && (
            <div
              style={{
                display: 'flex',
                justifyContent: 'space-between',
                marginTop: 'var(--md-sys-spacing-1)',
                ...metaStyle,
              }}
            >
              <span>{dueLabel}</span>
              <strong style={{ color: 'var(--md-sys-color-on-surface)' }}>{dueValue}</strong>
            </div>
          )}
        </div>
      )}
    </CardShell>
  );
};

/* ========================================================================= */
/* 4. LocationCard — a place, site, or region.                                */
/* ========================================================================= */

export interface LocationCardProps extends EntityCardBaseProps {
  name: string;
  address?: string;
  region?: string;
  status?: EntityStatus;
  /** Compact metric row, e.g. [{ label: 'Sites', value: 12 }]. */
  metrics?: Array<{ label: string; value: React.ReactNode }>;
  actionLabel?: string;
  onAction?: () => void;
}

export const LocationCard: React.FC<LocationCardProps> = ({
  name,
  address,
  region,
  status,
  metrics = [],
  actionLabel,
  onAction,
  nested,
  className = '',
}) => (
  <EntityCard
    className={`morphic-location-card ${className}`}
    nested={nested}
    overline={region}
    title={name}
    description={address}
    icon="location_on"
    status={status}
    actionLabel={actionLabel}
    onAction={onAction}
  >
    {metrics.length > 0 && (
      <div style={{ display: 'flex', gap: 'var(--md-sys-spacing-5)', flexWrap: 'wrap' }}>
        {metrics.map((m) => (
          <div key={m.label}>
            <div style={metaStyle}>{m.label}</div>
            <div
              style={{
                fontSize: 'var(--md-sys-typescale-metric-size-sm)',
                fontWeight: 'var(--md-sys-typescale-metric-weight)' as React.CSSProperties['fontWeight'],
                fontVariantNumeric: 'tabular-nums',
                color: 'var(--md-sys-color-on-surface)',
              }}
            >
              {m.value}
            </div>
          </div>
        ))}
      </div>
    )}
  </EntityCard>
);

/* ========================================================================= */
/* 5. ProfileCard — a person or account. Replaces Employee Card (§4).         */
/* ========================================================================= */

export interface ProfileCardProps extends EntityCardBaseProps {
  name: string;
  /** Role, team, or account type. */
  role?: string;
  email?: string;
  /** Image URL. Falls back to initials. */
  avatarUrl?: string;
  status?: EntityStatus;
  tags?: string[];
  actionLabel?: string;
  onAction?: () => void;
}

export const ProfileCard: React.FC<ProfileCardProps> = ({
  name,
  role,
  email,
  avatarUrl,
  status,
  tags = [],
  actionLabel,
  onAction,
  nested,
  className = '',
}) => {
  const initials = name
    .split(' ')
    .slice(0, 2)
    .map((part) => part.charAt(0).toUpperCase())
    .join('');

  return (
    <CardShell className={`morphic-profile-card ${className}`} nested={nested}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 'var(--md-sys-spacing-3)' }}>
        <div style={{ display: 'flex', gap: 'var(--md-sys-spacing-3)', alignItems: 'center', minWidth: 0 }}>
          <span
            aria-hidden="true"
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              justifyContent: 'center',
              width: '40px',
              height: '40px',
              flexShrink: 0,
              borderRadius: 'var(--radius-pill)',
              backgroundColor: 'var(--md-sys-color-primary-container)',
              color: 'var(--md-sys-color-on-primary-container)',
              fontSize: 'var(--md-sys-typescale-label-size)',
              fontWeight: 620,
              backgroundImage: avatarUrl ? `url(${avatarUrl})` : undefined,
              backgroundSize: 'cover',
              backgroundPosition: 'center',
            }}
          >
            {!avatarUrl && initials}
          </span>
          <div style={{ minWidth: 0 }}>
            <h4 style={titleStyle}>{name}</h4>
            {role && <span style={overlineStyle}>{role}</span>}
            {email && (
              <div style={{ ...metaStyle, marginTop: '2px', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                {email}
              </div>
            )}
          </div>
        </div>
        {status && <StatusBadge status={status} size="sm" />}
      </div>

      {tags.length > 0 && (
        <div style={{ display: 'flex', gap: 'var(--md-sys-spacing-2)', flexWrap: 'wrap' }}>
          {tags.map((tag) => (
            <span
              key={tag}
              style={{
                padding: '2px 8px',
                borderRadius: 'var(--radius-pill)',
                backgroundColor: 'var(--md-sys-color-surface-container-high)',
                color: 'var(--md-sys-color-on-surface-variant)',
                fontSize: 'var(--md-sys-typescale-meta-size-sm)',
                fontWeight: 500,
              }}
            >
              {tag}
            </span>
          ))}
        </div>
      )}

      {actionLabel && (
        <div style={footerStyle}>
          <span />
          <Button variant="tonal" size="sm" onClick={onAction}>
            {actionLabel}
          </Button>
        </div>
      )}
    </CardShell>
  );
};

/* ========================================================================= */
/* 6. ActivityCard — a single event in a feed (§3.14 Activity Feed).          */
/* ========================================================================= */

export interface ActivityCardProps extends EntityCardBaseProps {
  actor?: string;
  action: string;
  target?: string;
  timestamp: string;
  icon?: string;
  tone?: EntityTone;
}

export const ActivityCard: React.FC<ActivityCardProps> = ({
  actor,
  action,
  target,
  timestamp,
  icon = 'bolt',
  tone = 'neutral',
  nested,
  className = '',
}) => {
  const toneTokens = TONE_TOKENS[tone];

  return (
    <CardShell
      className={`morphic-activity-card ${className}`}
      nested={nested}
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: 'var(--md-sys-spacing-3)',
        padding: 'var(--md-sys-padding-card-compact)',
      }}
    >
      <span
        aria-hidden="true"
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          justifyContent: 'center',
          width: '32px',
          height: '32px',
          flexShrink: 0,
          borderRadius: 'var(--radius-pill)',
          backgroundColor: toneTokens.bg,
          color: toneTokens.fg,
        }}
      >
        <Icon name={icon} size={16} />
      </span>

      <div style={{ minWidth: 0, flex: 1 }}>
        <div style={{ fontSize: 'var(--md-sys-typescale-body-size-sm)', color: 'var(--md-sys-color-on-surface)' }}>
          {actor && <strong style={{ fontWeight: 600 }}>{actor} </strong>}
          {action}
          {target && <strong style={{ fontWeight: 600 }}> {target}</strong>}
        </div>
        <div style={metaStyle}>{timestamp}</div>
      </div>
    </CardShell>
  );
};

/* ========================================================================= */
/* 7. StatusCard — a single monitored condition (§3.14 Status Overview).      */
/* ========================================================================= */

export interface StatusCardProps extends EntityCardBaseProps {
  label: string;
  value: React.ReactNode;
  /** Supporting line under the value. */
  detail?: string;
  tone?: EntityTone;
  icon?: string;
}

export const StatusCard: React.FC<StatusCardProps> = ({
  label,
  value,
  detail,
  tone = 'neutral',
  icon,
  nested,
  className = '',
}) => {
  const toneTokens = TONE_TOKENS[tone];

  return (
    <CardShell
      className={`morphic-status-card ${className}`}
      nested={nested}
      style={{ gap: 'var(--md-sys-spacing-2)' }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--md-sys-spacing-2)' }}>
        {icon && (
          <span aria-hidden="true" style={{ color: toneTokens.fg, display: 'inline-flex' }}>
            <Icon name={icon} size={16} />
          </span>
        )}
        {/* §12 — labels are muted, the primary value dominates */}
        <span style={overlineStyle}>{label}</span>
      </div>

      <div
        style={{
          fontSize: 'var(--md-sys-typescale-metric-size)',
          fontWeight: 'var(--md-sys-typescale-metric-weight)' as React.CSSProperties['fontWeight'],
          lineHeight: 'var(--md-sys-typescale-metric-line-height)',
          letterSpacing: 'var(--md-sys-typescale-metric-tracking)',
          fontVariantNumeric: 'tabular-nums',
          color: tone === 'neutral' ? 'var(--md-sys-color-on-surface)' : toneTokens.fg,
        }}
      >
        {value}
      </div>

      {detail && <span style={metaStyle}>{detail}</span>}
    </CardShell>
  );
};
