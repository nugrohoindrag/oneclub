import React from 'react';
import { motion, HTMLMotionProps } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';

export interface FABProps extends HTMLMotionProps<'button'> {
  size?: 'small' | 'standard' | 'large';
  variant?: 'primary' | 'surface' | 'secondary' | 'tertiary';
  icon: React.ReactNode;
  label?: string; // Extended FAB
}

export const FAB: React.FC<FABProps> = ({
  size = 'standard',
  variant = 'primary',
  icon,
  label,
  className = '',
  style,
  ...props
}) => {
  const isExtended = Boolean(label);

  const sizeStyles: Record<string, React.CSSProperties> = {
    small: { width: '40px', height: '40px', borderRadius: 'var(--radius-md)', fontSize: '20px' },
    standard: { width: isExtended ? 'auto' : '56px', height: '56px', borderRadius: 'var(--radius-lg)', fontSize: '24px', padding: isExtended ? '0 20px 0 16px' : 0 },
    large: { width: '96px', height: '96px', borderRadius: 'var(--radius-hero)', fontSize: '28px' },
  };

  const variantStyles: Record<string, React.CSSProperties> = {
    primary: { backgroundColor: 'var(--md-sys-color-primary-container)', color: 'var(--md-sys-color-on-primary-container)' },
    surface: { backgroundColor: 'var(--md-sys-color-surface-container-high)', color: 'var(--md-sys-color-primary)' },
    secondary: { backgroundColor: 'var(--md-sys-color-secondary-container)', color: 'var(--md-sys-color-on-secondary-container)' },
    tertiary: { backgroundColor: 'var(--md-sys-color-tertiary-container)', color: 'var(--md-sys-color-on-tertiary-container)' },
  };

  return (
    <motion.button
      whileHover={{ scale: 1.05, boxShadow: 'var(--md-sys-elevation-level4)' }}
      whileTap={{ scale: 0.95 }}
      transition={M3_TRANSITIONS.springSnappy}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        gap: '12px',
        border: 'none',
        boxShadow: 'var(--md-sys-elevation-level3)',
        cursor: 'pointer',
        fontWeight: 600,
        ...sizeStyles[size],
        ...variantStyles[variant],
        ...style,
      }}
      className={`m3-fab ${className}`}
      {...props}
    >
      <span style={{ display: 'flex' }}>{icon}</span>
      {label && <span style={{ fontSize: '14px', fontWeight: 600 }}>{label}</span>}
    </motion.button>
  );
};
