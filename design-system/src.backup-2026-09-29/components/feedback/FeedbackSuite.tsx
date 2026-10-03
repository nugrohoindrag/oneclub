/**
 * @license MIT
 * FeedbackSuite — Toast, SuccessState, LoadingSpinner
 * 
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState } from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { Icon } from '../communication/Icon.js';
import { Button } from '../actions/Button.js';

// 1. SuccessState Card
export interface SuccessStateProps {
  title?: string;
  description?: string;
  actionLabel?: string;
  onAction?: () => void;
  secondaryActionLabel?: string;
  onSecondaryAction?: () => void;
}

export const SuccessState: React.FC<SuccessStateProps> = ({
  title = 'Operation Completed Successfully',
  description = 'Your changes have been deployed and synchronized across all active regions.',
  actionLabel = 'View Live Dashboard',
  onAction,
  secondaryActionLabel = 'Done',
  onSecondaryAction,
}) => {
  return (
    <div
      style={{
        borderRadius: 'var(--radius-xl)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-success-container)',
        padding: '48px 32px',
        textAlign: 'center',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        gap: '14px',
      }}
    >
      <div
        style={{
          width: '56px',
          height: '56px',
          borderRadius: 'var(--radius-pill)',
          backgroundColor: 'var(--md-sys-color-success-container)',
          color: 'var(--md-sys-color-primary)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
        }}
      >
        <Icon name="check_circle" size={32} />
      </div>

      <h3 style={{ margin: 0, fontSize: '18px', fontWeight: 700 }}>{title}</h3>
      <p style={{ margin: 0, maxWidth: '420px', fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)', lineHeight: 1.5 }}>
        {description}
      </p>

      <div style={{ display: 'flex', gap: '8px', marginTop: '8px' }}>
        {actionLabel && (
          <Button variant="filled" size="sm" onClick={onAction}>
            {actionLabel}
          </Button>
        )}
        {secondaryActionLabel && (
          <Button variant="text" size="sm" onClick={onSecondaryAction}>
            {secondaryActionLabel}
          </Button>
        )}
      </div>
    </div>
  );
};

// 2. LoadingSpinner & Dots
export const LoadingSpinner: React.FC<{ size?: number; color?: string; label?: string }> = ({
  size = 28,
  color = 'var(--md-sys-color-primary)',
  label,
}) => {
  return (
    <div style={{ display: 'inline-flex', alignItems: 'center', gap: '10px' }}>
      <svg
        width={size}
        height={size}
        viewBox="0 0 24 24"
        style={{ animation: 'spin 1s linear infinite' }}
      >
        <circle cx="12" cy="12" r="10" stroke="var(--md-sys-color-border)" strokeWidth="3" fill="none" />
        <path
          d="M 12 2 A 10 10 0 0 1 22 12"
          stroke={color}
          strokeWidth="3"
          strokeLinecap="round"
          fill="none"
        />
      </svg>
      {label && <span style={{ fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 500 }}>{label}</span>}
    </div>
  );
};

// 3. Floating Toast Notification
export interface ToastItem {
  id: string;
  title: string;
  message?: string;
  type?: 'success' | 'error' | 'info' | 'warning';
}

export const Toast: React.FC<{
  toast: ToastItem;
  onDismiss: (id: string) => void;
}> = ({ toast, onDismiss }) => {
  const typeConfig = {
    success: { icon: 'check_circle', color: 'var(--md-sys-color-primary)', bg: 'var(--md-sys-color-success-container)' },
    error: { icon: 'error', color: 'var(--md-sys-color-error)', bg: 'var(--md-sys-color-error-container)' },
    warning: { icon: 'warning', color: 'var(--md-sys-color-warning)', bg: 'var(--md-sys-color-warning-container)' },
    info: { icon: 'info', color: 'var(--md-sys-color-info)', bg: 'var(--md-sys-color-info-container)' },
  }[toast.type || 'info'];

  return (
    <motion.div
      initial={{ opacity: 0, y: 16, scale: 0.95 }}
      animate={{ opacity: 1, y: 0, scale: 1 }}
      exit={{ opacity: 0, y: -16, scale: 0.95 }}
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: '12px',
        padding: '12px 18px',
        borderRadius: 'var(--radius-lg)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        boxShadow: 'var(--md-sys-elevation-level3)',
        minWidth: '280px',
        maxWidth: '400px',
      }}
    >
      <div style={{ color: typeConfig.color, display: 'flex', alignItems: 'center' }}>
        <Icon name={typeConfig.icon} size={20} />
      </div>

      <div style={{ flex: 1 }}>
        <div style={{ fontSize: '13px', fontWeight: 700 }}>{toast.title}</div>
        {toast.message && <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>{toast.message}</div>}
      </div>

      <button
        onClick={() => onDismiss(toast.id)}
        style={{ border: 'none', backgroundColor: 'transparent', cursor: 'pointer', color: 'var(--md-sys-color-on-surface-variant)', display: 'flex' }}
      >
        <Icon name="close" size={16} />
      </button>
    </motion.div>
  );
};
