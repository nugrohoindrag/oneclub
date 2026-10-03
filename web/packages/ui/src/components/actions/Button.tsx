import React from 'react';
import { motion, HTMLMotionProps } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';

export interface ButtonProps extends Omit<HTMLMotionProps<'button'>, 'size' | 'children'> {
  children?: React.ReactNode;
  variant?: 'elevated' | 'filled' | 'tonal' | 'outlined' | 'text';
  size?: 'sm' | 'md' | 'lg';
  icon?: React.ReactNode;
  iconPosition?: 'start' | 'end';
}

export const Button: React.FC<ButtonProps> = ({
  children,
  variant = 'filled',
  size = 'md',
  icon,
  iconPosition = 'start',
  className = '',
  disabled,
  style,
  ...props
}) => {
  const sizeStyles: Record<string, React.CSSProperties> = {
    sm: { height: '32px', padding: '0 16px', fontSize: '13px', borderRadius: 'var(--radius-lg)' },
    md: { height: '40px', padding: '0 24px', fontSize: '14px', borderRadius: 'var(--radius-xl)' },
    lg: { height: '48px', padding: '0 28px', fontSize: '16px', borderRadius: 'var(--radius-xl)' },
  };

  const variantStyles: Record<string, React.CSSProperties> = {
    elevated: {
      backgroundColor: 'var(--md-sys-color-surface-container-low)',
      color: 'var(--md-sys-color-primary)',
      boxShadow: 'var(--md-sys-elevation-level1)',
      border: 'none',
    },
    filled: {
      backgroundColor: 'var(--md-sys-color-primary)',
      color: 'var(--md-sys-color-on-primary, #F0F4EC)',
      boxShadow: 'none',
      border: 'none',
    },
    tonal: {
      backgroundColor: 'var(--md-sys-color-secondary-container)',
      color: 'var(--md-sys-color-on-secondary-container)',
      boxShadow: 'none',
      border: 'none',
    },
    outlined: {
      backgroundColor: 'transparent',
      color: 'var(--md-sys-color-primary)',
      border: '1px solid var(--md-sys-color-outline)',
      boxShadow: 'none',
    },
    text: {
      backgroundColor: 'transparent',
      color: 'var(--md-sys-color-primary)',
      border: 'none',
      boxShadow: 'none',
      padding: '0 12px',
    },
  };

  return (
    /* §19 — hover lifts the button 1px and brightens the surface; press
       settles it to 0.98. The previous scale-up-on-hover with a spring
       overshot, which §18 rules out ("avoid bouncy animation") and which
       `buttonPress`/`pressable` in the motion layer already contradicted. */
    <motion.button
      whileHover={disabled ? undefined : { y: -1, filter: 'brightness(1.04)' }}
      whileTap={disabled ? undefined : { scale: 0.98, transition: { duration: 0.1 } }}
      transition={M3_TRANSITIONS.button}
      disabled={disabled}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        gap: '8px',
        fontWeight: 500,
        letterSpacing: '0.1px',
        cursor: disabled ? 'not-allowed' : 'pointer',
        position: 'relative',
        overflow: 'hidden',
        userSelect: 'none',
        opacity: disabled ? 0.38 : 1,
        ...sizeStyles[size],
        ...variantStyles[variant],
        ...style,
      }}
      className={`m3-common-btn ${className}`}
      {...props}
    >
      {icon && iconPosition === 'start' && <span style={{ display: 'flex', fontSize: '18px' }}>{icon}</span>}
      {children && <span>{children}</span>}
      {icon && iconPosition === 'end' && <span style={{ display: 'flex', fontSize: '18px' }}>{icon}</span>}
    </motion.button>
  );
};
