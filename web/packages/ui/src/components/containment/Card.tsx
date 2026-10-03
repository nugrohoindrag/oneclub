import React from 'react';
import { motion, AnimatePresence, HTMLMotionProps } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';

export interface CardProps extends HTMLMotionProps<'div'> {
  variant?: 'elevated' | 'filled' | 'outlined';
  clickable?: boolean;
  padding?: 'none' | 'sm' | 'md' | 'lg';
  overflow?: 'hidden' | 'visible' | 'auto';
}

export const Card: React.FC<CardProps> = ({
  variant = 'filled',
  clickable = false,
  padding = 'md',
  overflow = 'visible',
  children,
  className = '',
  style,
  ...props
}) => {
  const paddingMap = {
    none: '0',
    sm: '12px',
    md: '16px',
    lg: '24px',
  };

  const variantStyles: Record<string, React.CSSProperties> = {
    elevated: {
      backgroundColor: 'var(--md-sys-color-surface-container-low)',
      boxShadow: 'var(--md-sys-elevation-level1)',
      border: 'none',
    },
    filled: {
      backgroundColor: 'var(--md-sys-color-surface-container-highest)',
      boxShadow: 'none',
      border: 'none',
    },
    outlined: {
      backgroundColor: 'var(--md-sys-color-surface)',
      boxShadow: 'none',
      border: '1px solid var(--md-sys-color-outline-variant)',
    },
  };

  return (
    <motion.div
      whileHover={
        clickable
          ? { y: -2, boxShadow: 'var(--md-sys-elevation-level2)', transition: { duration: 0.2 } }
          : undefined
      }
      style={{
        borderRadius: 'var(--radius-lg)', // Official M3 16px radius for Cards
        padding: paddingMap[padding],
        color: 'var(--md-sys-color-on-surface)',
        cursor: clickable ? 'pointer' : 'default',
        position: 'relative',
        overflow: overflow,
        ...variantStyles[variant],
        ...style,
      }}
      className={`m3-card ${className}`}
      {...props}
    >
      {children}
    </motion.div>
  );
};

export interface DialogProps {
  isOpen: boolean;
  onClose: () => void;
  icon?: React.ReactNode;
  headline?: string;
  supportingText?: React.ReactNode;
  children?: React.ReactNode;
  actions?: React.ReactNode;
}

export const Dialog: React.FC<DialogProps> = ({
  isOpen,
  onClose,
  icon,
  headline,
  supportingText,
  children,
  actions,
}) => {
  return (
    <AnimatePresence>
      {isOpen && (
        <>
          {/* Scrim */}
          <motion.div
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.2 }}
            onClick={onClose}
            style={{
              position: 'fixed',
              inset: 0,
              backgroundColor: 'var(--md-sys-color-scrim)',
              zIndex: 998,
            }}
          />
          {/* Dialog Surface */}
          <motion.div
            initial={{ scale: 0.85, opacity: 0, x: '-50%', y: '-50%' }}
            animate={{ scale: 1, opacity: 1, x: '-50%', y: '-50%' }}
            exit={{ scale: 0.9, opacity: 0, x: '-50%', y: '-50%' }}
            transition={M3_TRANSITIONS.enter}
            style={{
              position: 'fixed',
              top: '50%',
              left: '50%',
              width: '90%',
              maxWidth: '560px',
              minWidth: '280px',
              borderRadius: 'var(--radius-hero)', // Official M3 28px radius
              padding: '24px',
              backgroundColor: 'var(--md-sys-color-surface-container-high)',
              color: 'var(--md-sys-color-on-surface)',
              boxShadow: 'var(--md-sys-elevation-level3)',
              zIndex: 999,
              display: 'flex',
              flexDirection: 'column',
            }}
          >
            {icon && (
              <div style={{ display: 'flex', justifyContent: 'center', marginBottom: '16px', fontSize: '24px', color: 'var(--md-sys-color-secondary)' }}>
                {icon}
              </div>
            )}
            {headline && (
              <h2 style={{ margin: '0 0 16px', fontSize: '24px', fontWeight: 500, lineHeight: 1.33, textAlign: icon ? 'center' : 'left' }}>
                {headline}
              </h2>
            )}
            {supportingText && (
              <div style={{ fontSize: '14px', color: 'var(--md-sys-color-on-surface-variant)', lineHeight: 1.43, marginBottom: '24px' }}>
                {supportingText}
              </div>
            )}
            {children}
            {actions && (
              <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '8px', marginTop: '24px' }}>
                {actions}
              </div>
            )}
          </motion.div>
        </>
      )}
    </AnimatePresence>
  );
};
