/**
 * @license MIT
 * StateBanners & Feedback Cards — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §18 Motion Design      — enter slow, leave fast, Material easing
 *   §19 Micro-interactions — a banner is an interruption; it announces itself
 *   §9  Accessibility      — presets fall back to a plain fade
 *
 * Every banner takes `open` (default `true`) and runs its own
 * `<AnimatePresence>`, so dismissing one plays the exit rather than ripping
 * the node out mid-tween — the same contract as `<Snackbar>`.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';
import { AnimatePresence, motion } from 'motion/react';
import { Icon } from '../communication/Icon.js';
import { Button } from '../actions/Button.js';
import {
  M3_SPRING,
  M3_TRANSITIONS,
  collapse,
  fadeSlide,
  staggerChildren,
  useReducedMotionSafe,
} from '../../motion/index.js';

export interface BannerProps {
  title: string;
  description: string;
  actionLabel?: string;
  onAction?: () => void;
  onDismiss?: () => void;
  className?: string;
  /** Drives the enter/exit animation. Defaults to `true`. */
  open?: boolean;
}

/* A banner drops in from above and collapses its own height on the way out,
   so the rows beneath it slide up instead of snapping. */
const bannerVariants = {
  hidden: { opacity: 0, y: -8, height: 0, overflow: 'hidden' },
  visible: {
    opacity: 1,
    y: 0,
    height: 'auto',
    transition: M3_TRANSITIONS.enter,
    transitionEnd: { overflow: 'visible' },
  },
  exit: { opacity: 0, y: -8, height: 0, overflow: 'hidden', transition: M3_TRANSITIONS.exit },
};

const bannerLayout: React.CSSProperties = {
  borderRadius: 'var(--radius-lg)',
  padding: '16px 20px',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '16px',
};

export const InfoBanner: React.FC<BannerProps> = ({
  title,
  description,
  actionLabel,
  onAction,
  onDismiss,
  className = '',
  open = true,
}) => {
  return (
    <AnimatePresence initial={false}>
      {open && (
        <motion.div
          role="status"
          variants={bannerVariants}
          initial="hidden"
          animate="visible"
          exit="exit"
          className={`morphic-info-banner ${className}`}
          style={{
            ...bannerLayout,
            backgroundColor: 'var(--md-sys-color-info-container)',
            border: '1px solid var(--md-sys-color-info-container)',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
            <div style={{ color: 'var(--md-sys-color-info)', display: 'flex', alignItems: 'center' }}>
              <Icon name="info" size={22} />
            </div>
            <div>
              <h4 style={{ margin: '0 0 2px', fontSize: '14px', fontWeight: 700, color: 'var(--md-sys-color-on-surface)' }}>{title}</h4>
              <p style={{ margin: 0, fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)' }}>{description}</p>
            </div>
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            {actionLabel && <Button variant="filled" size="sm" onClick={onAction}>{actionLabel}</Button>}
            {onDismiss && <Button variant="text" size="sm" onClick={onDismiss}><Icon name="close" size={18} /></Button>}
          </div>
        </motion.div>
      )}
    </AnimatePresence>
  );
};

export const WarningBanner: React.FC<BannerProps> = ({
  title,
  description,
  actionLabel,
  onAction,
  onDismiss,
  className = '',
  open = true,
}) => {
  const reduced = useReducedMotionSafe();

  return (
    <AnimatePresence initial={false}>
      {open && (
        <motion.div
          role="alert"
          variants={bannerVariants}
          initial="hidden"
          animate="visible"
          exit="exit"
          className={`morphic-warning-banner ${className}`}
          style={{
            ...bannerLayout,
            backgroundColor: 'var(--md-sys-color-warning-container)',
            border: '1px solid var(--md-sys-color-warning-container)',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
            {/* The warning icon pulses once on arrival — §19's "communicate
                urgency without a loop that never stops". */}
            <motion.div
              initial={{ scale: reduced ? 1 : 0.6 }}
              animate={{ scale: 1 }}
              transition={M3_SPRING.snappy}
              style={{ color: 'var(--md-sys-color-warning)', display: 'flex', alignItems: 'center' }}
            >
              <Icon name="warning" size={22} />
            </motion.div>
            <div>
              <h4 style={{ margin: '0 0 2px', fontSize: '14px', fontWeight: 700, color: 'var(--md-sys-color-on-surface)' }}>{title}</h4>
              <p style={{ margin: 0, fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)' }}>{description}</p>
            </div>
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            {actionLabel && <Button variant="tonal" size="sm" onClick={onAction}>{actionLabel}</Button>}
            {onDismiss && <Button variant="text" size="sm" onClick={onDismiss}><Icon name="close" size={18} /></Button>}
          </div>
        </motion.div>
      )}
    </AnimatePresence>
  );
};

export const AlertCallout: React.FC<{
  type?: 'success' | 'error' | 'warning' | 'info';
  title: string;
  message: string;
  open?: boolean;
}> = ({ type = 'info', title, message, open = true }) => {
  const config = {
    success: { bg: 'var(--md-sys-color-success-container)', border: 'var(--md-sys-color-success)', icon: 'check_circle', color: 'var(--md-sys-color-success)' },
    error: { bg: 'var(--md-sys-color-error-container)', border: 'var(--md-sys-color-error)', icon: 'error', color: 'var(--md-sys-color-error)' },
    warning: { bg: 'var(--md-sys-color-warning-container)', border: 'var(--md-sys-color-warning)', icon: 'warning', color: 'var(--md-sys-color-warning)' },
    info: { bg: 'var(--md-sys-color-info-container)', border: 'var(--md-sys-color-info)', icon: 'info', color: 'var(--md-sys-color-info)' },
  }[type];

  return (
    <AnimatePresence initial={false} mode="wait">
      {open && (
        // Keyed on `type` so switching severity crossfades the whole callout
        // instead of recolouring it mid-sentence.
        <motion.div
          key={type}
          variants={collapse}
          initial="hidden"
          animate="visible"
          exit="exit"
          style={{
            borderRadius: 'var(--radius-md)',
            padding: '14px 18px',
            backgroundColor: config.bg,
            borderLeft: `4px solid ${config.border}`,
            display: 'flex',
            alignItems: 'flex-start',
            gap: '12px',
          }}
        >
          <Icon name={config.icon} size={20} color={config.color} />
          <div>
            <h5 style={{ margin: '0 0 2px', fontSize: '14px', fontWeight: 700 }}>{title}</h5>
            <div style={{ fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)' }}>{message}</div>
          </div>
        </motion.div>
      )}
    </AnimatePresence>
  );
};

/* Empty and error states fill a region on their own, so they reveal in
   sequence — icon, heading, copy, action — rather than all at once (§19). */
const stateContainer = {
  hidden: {},
  visible: { transition: staggerChildren('relaxed') },
};
const statePiece = fadeSlide('up', 6);

const stateSurface: React.CSSProperties = {
  borderRadius: 'var(--radius-xl)',
  backgroundColor: 'var(--md-sys-color-surface)',
  padding: '48px 32px',
  textAlign: 'center',
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: '12px',
};

export const EmptyState: React.FC<{
  title?: string;
  description?: string;
  icon?: string;
  actionLabel?: string;
  onAction?: () => void;
}> = ({
  title = 'No Data Found',
  description = 'No activity records or transaction entities match the selected criteria.',
  icon = 'inbox',
  actionLabel = 'Add New Record',
  onAction,
}) => {
  const reduced = useReducedMotionSafe();

  return (
    <motion.div
      variants={stateContainer}
      initial="hidden"
      animate="visible"
      style={{ ...stateSurface, border: '1px dashed var(--md-sys-color-border)' }}
    >
      <motion.div
        variants={statePiece}
        initial={{ scale: reduced ? 1 : 0.85 }}
        animate={{ scale: 1 }}
        transition={M3_SPRING.gentle}
        style={{
          width: '56px',
          height: '56px',
          borderRadius: 'var(--radius-pill)',
          backgroundColor: 'var(--md-sys-color-surface-container)',
          color: 'var(--md-sys-color-on-surface-variant)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
        }}
      >
        <Icon name={icon} size={28} />
      </motion.div>
      <motion.h3 variants={statePiece} style={{ margin: 0, fontSize: '18px', fontWeight: 700 }}>
        {title}
      </motion.h3>
      <motion.p
        variants={statePiece}
        style={{ margin: 0, maxWidth: '420px', fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)', lineHeight: 1.5 }}
      >
        {description}
      </motion.p>
      {actionLabel && (
        <motion.div variants={statePiece} style={{ marginTop: '8px' }}>
          <Button variant="filled" size="sm" onClick={onAction}>
            {actionLabel}
          </Button>
        </motion.div>
      )}
    </motion.div>
  );
};

export const ErrorState: React.FC<{
  title?: string;
  description?: string;
  onRetry?: () => void;
}> = ({
  title = 'Failed to Load Cluster Data',
  description = 'Network timeout or authentication issue occurred while contacting the API gateway.',
  onRetry,
}) => {
  const reduced = useReducedMotionSafe();

  return (
    <motion.div
      role="alert"
      variants={stateContainer}
      initial="hidden"
      animate="visible"
      style={{ ...stateSurface, border: '1px solid var(--md-sys-color-error-container)' }}
    >
      <motion.div
        variants={statePiece}
        initial={{ scale: reduced ? 1 : 0.85 }}
        animate={{ scale: 1 }}
        transition={M3_SPRING.gentle}
        style={{
          width: '56px',
          height: '56px',
          borderRadius: 'var(--radius-pill)',
          backgroundColor: 'var(--md-sys-color-error-container)',
          color: 'var(--md-sys-color-error)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
        }}
      >
        <Icon name="cloud_off" size={28} />
      </motion.div>
      <motion.h3
        variants={statePiece}
        style={{ margin: 0, fontSize: '18px', fontWeight: 700, color: 'var(--md-sys-color-error)' }}
      >
        {title}
      </motion.h3>
      <motion.p
        variants={statePiece}
        style={{ margin: 0, maxWidth: '420px', fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)', lineHeight: 1.5 }}
      >
        {description}
      </motion.p>
      {onRetry && (
        <motion.div variants={statePiece} style={{ marginTop: '8px' }}>
          <Button variant="filled" size="sm" icon={<Icon name="refresh" size={16} />} onClick={onRetry}>
            Try Again
          </Button>
        </motion.div>
      )}
    </motion.div>
  );
};
