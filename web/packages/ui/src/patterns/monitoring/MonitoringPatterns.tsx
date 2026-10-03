/**
 * @license MIT
 * Monitoring Patterns — Morphic Design System
 * 14. Patterns
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §3.14 Patterns → Monitoring
 *         ├── Status Overview
 *         ├── Alert Summary
 *         ├── Activity Feed
 *         └── Event Timeline
 *   §16 A pattern is production-ready when it uses existing components, is
 *       responsive, supports empty/loading/error states, introduces no one-off
 *       tokens, and can be reused across industries.
 *
 * These were previously `AlertCenter` and `OperationalStatus` with hard-coded
 * cluster/ops vocabulary baked into the markup. §4 requires Core and Patterns
 * to stay domain-neutral, so every label is now a prop with a generic default.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';
import { AnimatePresence, motion } from 'motion/react';
import { Icon } from '../../components/communication/Icon.js';
import { Button } from '../../components/actions/Button.js';
import { StatusBadge } from '../../components/data-display/DataDisplaySuite.js';
import { EntityStatus } from '../../components/entity/EntityCards.js';
import {
  M3_TRANSITIONS,
  fadeSlide,
  staggerChildren,
  useReducedMotionSafe,
} from '../../motion/index.js';

/* ========================================================================= */
/* Shared pattern shell                                                      */
/* ========================================================================= */

const patternShell: React.CSSProperties = {
  borderRadius: 'var(--radius-card)',
  backgroundColor: 'var(--md-sys-color-surface)',
  border: '1px solid var(--md-sys-color-border)',
  padding: 'var(--md-sys-padding-card)',
  /* §21 — surface contrast, not a resting shadow */
  boxShadow: 'var(--md-sys-elevation-level0)',
};

const patternHeader: React.CSSProperties = {
  display: 'flex',
  justifyContent: 'space-between',
  alignItems: 'center',
  gap: 'var(--md-sys-spacing-3)',
  marginBottom: 'var(--md-sys-spacing-3)',
};

const patternTitle: React.CSSProperties = {
  margin: 0,
  /* §6 — section title 16–18px, 600–650 */
  fontSize: 'var(--md-sys-typescale-section-title-size)',
  fontWeight: 620,
  letterSpacing: 'var(--md-sys-typescale-section-title-tracking)',
  color: 'var(--md-sys-color-on-surface)',
};

const patternMeta: React.CSSProperties = {
  fontSize: 'var(--md-sys-typescale-meta-size)',
  color: 'var(--md-sys-color-on-surface-variant)',
};

/* ========================================================================= */
/* Alert Summary (§3.14)                                                     */
/* ========================================================================= */

export type AlertSeverity = 'high' | 'medium' | 'low';

export interface AlertItem {
  id: string;
  title: string;
  /** Where the alert came from: service, device, account, region. */
  source: string;
  time: string;
  severity: AlertSeverity;
}

export interface AlertSummaryProps {
  alerts: AlertItem[];
  /** Defaults to a generic label; pass a domain title in an Example. */
  title?: string;
  dismissLabel?: string;
  /** §16 — patterns must support the empty state. */
  emptyLabel?: string;
  loading?: boolean;
  onAcknowledge?: (id: string) => void;
  className?: string;
}

const SEVERITY_TOKENS: Record<AlertSeverity, { icon: string; fg: string; bg: string; border: string }> = {
  high: {
    icon: 'error',
    fg: 'var(--md-sys-color-error)',
    bg: 'var(--md-sys-color-error-container)',
    border: 'var(--md-sys-color-error)',
  },
  medium: {
    icon: 'warning',
    fg: 'var(--md-sys-color-warning)',
    bg: 'var(--md-sys-color-warning-container)',
    border: 'var(--md-sys-color-border)',
  },
  low: {
    icon: 'info',
    fg: 'var(--md-sys-color-info)',
    bg: 'var(--md-sys-color-surface-container-high)',
    border: 'var(--md-sys-color-border)',
  },
};

export const AlertSummary: React.FC<AlertSummaryProps> = ({
  alerts,
  title = 'Alerts',
  dismissLabel = 'Dismiss',
  emptyLabel = 'No active alerts',
  loading = false,
  onAcknowledge,
  className = '',
}) => {
  const reduced = useReducedMotionSafe();

  return (
  <section className={`morphic-alert-summary ${className}`} style={patternShell} aria-busy={loading}>
    <div style={patternHeader}>
      <h4 style={patternTitle}>{title}</h4>
      <span style={patternMeta}>{alerts.length} active</span>
    </div>

    {/* §11 — empty state. It crossfades with the list rather than replacing
        it instantly, so acknowledging the last alert reads as a resolution. */}
    <AnimatePresence mode="wait" initial={false}>
    {alerts.length === 0 && !loading ? (
      <motion.div
        key="empty"
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        exit={{ opacity: 0 }}
        transition={M3_TRANSITIONS.enter}
        style={{
          ...patternMeta,
          padding: 'var(--md-sys-spacing-6)',
          textAlign: 'center',
        }}
      >
        {emptyLabel}
      </motion.div>
    ) : (
      /* Alerts arrive in sequence and collapse out when acknowledged, so the
         rows below close the gap instead of jumping up (§19). */
      <motion.div
        key="list"
        initial="hidden"
        animate="visible"
        variants={{ hidden: {}, visible: { transition: staggerChildren('tight') } }}
        style={{ display: 'flex', flexDirection: 'column', gap: 'var(--md-sys-spacing-2)' }}
      >
        <AnimatePresence initial={false} mode="popLayout">
        {alerts.map((alert) => {
          const tone = SEVERITY_TOKENS[alert.severity];
          return (
            <motion.div
              key={alert.id}
              layout
              variants={fadeSlide('up', 6)}
              exit={{
                opacity: 0,
                height: 0,
                marginTop: 0,
                overflow: 'hidden',
                transition: M3_TRANSITIONS.exit,
              }}
              whileHover={reduced ? undefined : { x: 2 }}
              transition={M3_TRANSITIONS.layout}
              style={{
                padding: 'var(--md-sys-spacing-3) var(--md-sys-spacing-4)',
                borderRadius: 'var(--radius-md)',
                backgroundColor: alert.severity === 'high' ? tone.bg : 'var(--md-sys-color-surface-container)',
                /* §22 — 1px, low contrast */
                border: `1px solid ${alert.severity === 'high' ? tone.border : 'var(--md-sys-color-border)'}`,
                display: 'flex',
                justifyContent: 'space-between',
                alignItems: 'center',
                gap: 'var(--md-sys-spacing-3)',
              }}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--md-sys-spacing-3)', minWidth: 0 }}>
                <Icon name={tone.icon} size={18} color={tone.fg} />
                <div style={{ minWidth: 0 }}>
                  <div
                    style={{
                      fontSize: 'var(--md-sys-typescale-body-size-sm)',
                      fontWeight: 600,
                      color: 'var(--md-sys-color-on-surface)',
                    }}
                  >
                    {alert.title}
                  </div>
                  <div style={patternMeta}>
                    {alert.source} • {alert.time}
                  </div>
                </div>
              </div>

              <Button variant="text" size="sm" onClick={() => onAcknowledge?.(alert.id)}>
                {dismissLabel}
              </Button>
            </motion.div>
          );
        })}
        </AnimatePresence>
      </motion.div>
    )}
    </AnimatePresence>
  </section>
  );
};

/* ========================================================================= */
/* Status Overview (§3.14)                                                   */
/* ========================================================================= */

export interface StatusMetric {
  label: string;
  value: React.ReactNode;
  /** Emphasise this metric with the primary colour. */
  emphasis?: boolean;
}

export interface StatusOverviewProps {
  /** Any number of metrics; the grid adapts (§10 Responsive Contract). */
  metrics: StatusMetric[];
  title?: string;
  status?: EntityStatus;
  statusLabel?: string;
  className?: string;
}

export const StatusOverview: React.FC<StatusOverviewProps> = ({
  metrics,
  title = 'Status Overview',
  status = 'online',
  statusLabel = 'All systems healthy',
  className = '',
}) => (
  <section className={`morphic-status-overview ${className}`} style={patternShell}>
    <div style={{ ...patternHeader, marginBottom: 'var(--md-sys-spacing-4)' }}>
      <h4 style={patternTitle}>{title}</h4>
      <StatusBadge status={status} label={statusLabel} size="sm" />
    </div>

    {/* The tiles reveal in sequence on first paint — §19's "charts and
        metrics animate on first render", applied to the metric grid. */}
    <motion.div
      initial="hidden"
      animate="visible"
      variants={{ hidden: {}, visible: { transition: staggerChildren('tight') } }}
      style={{
        display: 'grid',
        /* §25 — reflows to fewer columns instead of shrinking (§10) */
        gridTemplateColumns: 'repeat(auto-fit, minmax(120px, 1fr))',
        gap: 'var(--md-sys-gap-card)',
        textAlign: 'center',
      }}
    >
      {metrics.map((metric) => (
        <motion.div
          key={metric.label}
          variants={fadeSlide('up', 6)}
          style={{
            padding: 'var(--md-sys-spacing-3)',
            borderRadius: 'var(--radius-md)',
            /* §19 — nested surfaces step through the tonal ladder, they do
               not gain a shadow */
            backgroundColor: 'var(--md-sys-color-surface-container)',
          }}
        >
          {/* §12 — labels muted, value dominates */}
          <div style={patternMeta}>{metric.label}</div>
          <div
            style={{
              fontSize: 'var(--md-sys-typescale-metric-size-sm)',
              fontWeight: 'var(--md-sys-typescale-metric-weight)' as React.CSSProperties['fontWeight'],
              letterSpacing: 'var(--md-sys-typescale-metric-tracking)',
              fontVariantNumeric: 'tabular-nums',
              color: metric.emphasis ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-on-surface)',
              marginTop: '2px',
            }}
          >
            {/* A metric that changes swaps in place instead of blinking. */}
            <AnimatePresence mode="wait" initial={false}>
              <motion.span
                key={String(metric.value)}
                initial={{ opacity: 0, y: 6 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, y: -6 }}
                transition={M3_TRANSITIONS.button}
                style={{ display: 'inline-block' }}
              >
                {metric.value}
              </motion.span>
            </AnimatePresence>
          </div>
        </motion.div>
      ))}
    </motion.div>
  </section>
);

/* ========================================================================= */
/* Deprecated aliases — the previous domain-flavoured names.                  */
/* Removed from Core in the §4 domain-neutrality pass; kept so existing       */
/* application code keeps compiling. Prefer the generic names above.          */
/* ========================================================================= */

/** @deprecated Use {@link AlertSummary}. */
export const AlertCenter = AlertSummary;

/** @deprecated Use {@link StatusOverview} with a `metrics` array. */
export const OperationalStatus: React.FC<{
  uptime?: string;
  clustersOnline?: number;
  totalClusters?: number;
  apiSuccessRate?: string;
}> = ({ uptime = '99.98%', clustersOnline = 142, totalClusters = 144, apiSuccessRate = '99.94%' }) => (
  <StatusOverview
    metrics={[
      { label: '30-Day Uptime', value: uptime, emphasis: true },
      { label: 'Active Nodes', value: `${clustersOnline}/${totalClusters}` },
      { label: 'API Success', value: apiSuccessRate, emphasis: true },
    ]}
  />
);
