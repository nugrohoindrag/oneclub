import React from 'react';
import { motion, HTMLMotionProps } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';

export interface IconButtonProps extends HTMLMotionProps<'button'> {
  variant?: 'standard' | 'filled' | 'tonal' | 'outlined';
  icon: React.ReactNode;
  selected?: boolean;
}

export const IconButton: React.FC<IconButtonProps> = ({
  variant = 'standard',
  icon,
  selected = false,
  className = '',
  disabled,
  style,
  ...props
}) => {
  const variantStyles: Record<string, React.CSSProperties> = {
    standard: {
      backgroundColor: 'transparent',
      color: selected ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-on-surface-variant)',
      border: 'none',
    },
    filled: {
      backgroundColor: selected ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-primary)',
      color: 'var(--md-sys-color-on-primary, #F0F4EC)',
      border: 'none',
    },
    tonal: {
      backgroundColor: selected ? 'var(--md-sys-color-secondary-container)' : 'var(--md-sys-color-surface-container-highest)',
      color: selected ? 'var(--md-sys-color-on-secondary-container)' : 'var(--md-sys-color-on-surface-variant)',
      border: 'none',
    },
    outlined: {
      backgroundColor: selected ? 'var(--md-sys-color-inverse-surface)' : 'transparent',
      color: selected ? 'var(--md-sys-color-inverse-on-surface)' : 'var(--md-sys-color-on-surface-variant)',
      border: '1px solid var(--md-sys-color-outline)',
    },
  };

  return (
    <motion.button
      whileHover={{ scale: disabled ? 1 : 1.08 }}
      whileTap={{ scale: disabled ? 1 : 0.92 }}
      transition={M3_TRANSITIONS.springSnappy}
      disabled={disabled}
      style={{
        width: '40px',
        height: '40px',
        borderRadius: 'var(--radius-xl)',
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        cursor: disabled ? 'not-allowed' : 'pointer',
        fontSize: '24px',
        opacity: disabled ? 0.38 : 1,
        ...variantStyles[variant],
        ...style,
      }}
      className={`m3-icon-button ${className}`}
      {...props}
    >
      {icon}
    </motion.button>
  );
};

export interface SegmentedButtonItem {
  value: string;
  label: string;
  icon?: React.ReactNode;
}

export interface SegmentedButtonProps {
  items: SegmentedButtonItem[];
  value: string;
  onChange: (val: string) => void;
  className?: string;
}

export const SegmentedButton: React.FC<SegmentedButtonProps> = ({
  items,
  value,
  onChange,
  className = '',
}) => {
  return (
    <div
      style={{
        display: 'inline-flex',
        borderRadius: 'var(--radius-xl)', // 20px stadium shape
        border: '1px solid var(--md-sys-color-outline)',
        overflow: 'hidden',
        height: '40px',
        backgroundColor: 'transparent',
        userSelect: 'none',
        position: 'relative',
      }}
      className={`m3-segmented-button ${className}`}
    >
      {items.map((item, index) => {
        const isSelected = item.value === value;
        return (
          <button
            key={item.value}
            onClick={() => onChange(item.value)}
            style={{
              position: 'relative',
              padding: '0 20px',
              border: 'none',
              borderRight: index < items.length - 1 ? '1px solid var(--md-sys-color-outline)' : 'none',
              backgroundColor: 'transparent',
              color: isSelected
                ? 'var(--md-sys-color-on-secondary-container)'
                : 'var(--md-sys-color-on-surface)',
              fontWeight: isSelected ? 600 : 500,
              fontSize: '14px',
              cursor: 'pointer',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              gap: '8px',
              zIndex: 1,
              transition: 'color 0.2s',
            }}
          >
            {/* Animated Active Background Pill with layoutId */}
            {isSelected && (
              <motion.div
                layoutId="segmentedActivePill"
                transition={M3_TRANSITIONS.springSnappy}
                style={{
                  position: 'absolute',
                  inset: 0,
                  backgroundColor: 'var(--md-sys-color-secondary-container)',
                  zIndex: -1,
                }}
              />
            )}
            {isSelected && <span style={{ fontSize: '14px', fontWeight: 'bold' }}>✓</span>}
            {item.icon && <span>{item.icon}</span>}
            <span>{item.label}</span>
          </button>
        );
      })}
    </div>
  );
};
