import React from 'react';
import { AnimatePresence, motion } from 'motion/react';
import { toast as toastVariants } from '../../motion/index.js';

export interface SnackbarProps {
  isOpen: boolean;
  message: string;
  actionLabel?: string;
  onAction?: () => void;
  onClose: () => void;
  showCloseIcon?: boolean;
}

export const Snackbar: React.FC<SnackbarProps> = ({
  isOpen,
  message,
  actionLabel,
  onAction,
  onClose,
  showCloseIcon = false,
}) => {
  /* §18 — a snackbar rises on enter and drops on exit, using the shared
     `toast` presence variant so every transient surface moves alike.
     AnimatePresence keeps the exit animation, which the previous imperative
     version could not: it unmounted before the tween ran. */
  return (
    <AnimatePresence>
      {isOpen && (
        <motion.div
          role="status"
          aria-live="polite"
          variants={toastVariants}
          initial="hidden"
          animate="visible"
          exit="exit"
          style={{
        position: 'fixed',
        bottom: '24px',
        left: '24px',
        zIndex: 1000,
        minWidth: '320px',
        maxWidth: '560px',
        minHeight: '48px',
        padding: '12px 16px',
        borderRadius: 'var(--radius-pill)', // M3 4px radius for Snackbar
        backgroundColor: 'var(--md-sys-color-inverse-surface)', // M3 Inverse Surface
        color: 'var(--md-sys-color-inverse-on-surface)', // M3 Inverse On-Surface
        boxShadow: 'var(--md-sys-elevation-level3)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        gap: '12px',
        fontSize: '14px',
        lineHeight: 1.4,
          }}
          className="m3-snackbar"
        >
          <span>{message}</span>
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
        {actionLabel && (
          <button
            onClick={onAction}
            style={{
              background: 'none',
              border: 'none',
              color: 'var(--md-sys-color-inverse-primary)', // M3 Inverse Primary
              fontWeight: 600,
              fontSize: '14px',
              cursor: 'pointer',
              padding: '0 8px',
            }}
          >
            {actionLabel}
          </button>
        )}
        {showCloseIcon && (
          <button
            onClick={onClose}
            style={{
              background: 'none',
              border: 'none',
              color: 'var(--md-sys-color-inverse-on-surface)',
              cursor: 'pointer',
              fontSize: '16px',
            }}
          >
            ✕
          </button>
        )}
          </div>
        </motion.div>
      )}
    </AnimatePresence>
  );
};

export interface TooltipProps {
  content: string;
  headline?: string; // If provided, becomes Rich Tooltip
  children: React.ReactNode;
}

export const Tooltip: React.FC<TooltipProps> = ({
  content,
  headline,
  children,
}) => {
  const [visible, setVisible] = React.useState(false);
  const isRich = Boolean(headline);

  return (
    <div
      onMouseEnter={() => setVisible(true)}
      onMouseLeave={() => setVisible(false)}
      style={{ position: 'relative', display: 'inline-flex' }}
    >
      {children}
      {visible && (
        <div
          style={{
            position: 'absolute',
            bottom: '100%',
            left: '50%',
            transform: 'translateX(-50%)',
            marginBottom: '8px',
            zIndex: 100,
            padding: isRich ? '12px 16px' : '4px 8px',
            backgroundColor: isRich
              ? 'var(--md-sys-color-surface-container)'
              : 'var(--md-sys-color-inverse-surface)', // Inverse surface for plain
            color: isRich ? 'var(--md-sys-color-on-surface)' : 'var(--md-sys-color-inverse-on-surface)',
            borderRadius: isRich ? '12px' : '4px',
            fontSize: '12px',
            boxShadow: 'var(--md-sys-elevation-level2)',
            whiteSpace: isRich ? 'normal' : 'nowrap',
            minWidth: isRich ? '200px' : 'auto',
            pointerEvents: 'none',
          }}
        >
          {headline && (
            <div style={{ fontWeight: 600, fontSize: '14px', marginBottom: '4px' }}>
              {headline}
            </div>
          )}
          <div>{content}</div>
        </div>
      )}
    </div>
  );
};
