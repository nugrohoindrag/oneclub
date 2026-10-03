import React from 'react';
import { motion, HTMLMotionProps } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';

export interface ChipProps extends Omit<HTMLMotionProps<'button'>, 'children'> {
  variant?: 'assist' | 'filter' | 'input' | 'suggestion';
  selected?: boolean;
  icon?: React.ReactNode;
  trailingIcon?: React.ReactNode;
  onRemove?: () => void;
  children: React.ReactNode;
}

export const Chip: React.FC<ChipProps> = ({
  variant = 'assist',
  selected = false,
  icon,
  trailingIcon,
  onRemove,
  children,
  className = '',
  disabled,
  style,
  ...props
}) => {
  const isFilter = variant === 'filter';
  const isInput = variant === 'input';

  return (
    <motion.button
      whileHover={{ scale: disabled ? 1 : 1.02 }}
      whileTap={{ scale: disabled ? 1 : 0.96 }}
      transition={M3_TRANSITIONS.springSnappy}
      disabled={disabled}
      style={{
        height: '32px', // Official M3 32px height for Chips
        borderRadius: 'var(--radius-sm)', // Official M3 8px radius
        padding: selected && isFilter ? '0 12px 0 8px' : '0 16px',
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        gap: '8px',
        fontSize: '14px',
        fontWeight: 500,
        backgroundColor: selected
          ? 'var(--md-sys-color-secondary-container)'
          : 'var(--md-sys-color-surface-container-low)',
        color: selected
          ? 'var(--md-sys-color-on-secondary-container)'
          : 'var(--md-sys-color-on-surface)',
        border: `1px solid ${selected ? 'transparent' : 'var(--md-sys-color-outline-variant)'}`,
        cursor: disabled ? 'not-allowed' : 'pointer',
        opacity: disabled ? 0.38 : 1,
        ...style,
      }}
      className={`m3-chip m3-chip-${variant} ${className}`}
      {...props}
    >
      {isFilter && selected && <span style={{ fontSize: '12px', fontWeight: 'bold' }}>✓</span>}
      {icon && !selected && <span style={{ display: 'flex', fontSize: '16px' }}>{icon}</span>}
      <span>{children}</span>
      {isInput && onRemove && (
        <span
          onClick={(e) => {
            e.stopPropagation();
            onRemove();
          }}
          style={{ cursor: 'pointer', fontSize: '14px', marginLeft: '4px' }}
        >
          ✕
        </span>
      )}
      {trailingIcon && <span>{trailingIcon}</span>}
    </motion.button>
  );
};

export interface SliderProps {
  value: number;
  onChange: (val: number) => void;
  min?: number;
  max?: number;
  step?: number;
  discrete?: boolean;
  label?: string;
  className?: string;
}

export const Slider: React.FC<SliderProps> = ({
  value,
  onChange,
  min = 0,
  max = 100,
  step = 1,
  discrete = false,
  label,
  className = '',
}) => {
  const percentage = ((value - min) / (max - min)) * 100;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '8px', width: '100%' }} className={`m3-slider ${className}`}>
      {label && (
        <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '14px' }}>
          <span>{label}</span>
          <span style={{ fontWeight: 600, color: 'var(--md-sys-color-primary)' }}>{value}</span>
        </div>
      )}
      <div style={{ position: 'relative', display: 'flex', alignItems: 'center', height: '44px' }}>
        {/* Track Container */}
        <div
          style={{
            position: 'absolute',
            width: '100%',
            height: '16px', // Official M3 thick active track
            borderRadius: 'var(--radius-sm)',
            backgroundColor: 'var(--md-sys-color-surface-container-highest)',
            overflow: 'hidden',
          }}
        >
          <motion.div
            animate={{ width: `${percentage}%` }}
            transition={{ duration: 0.1 }}
            style={{
              height: '100%',
              backgroundColor: 'var(--md-sys-color-primary)',
            }}
          />
        </div>

        {/* Real Range Input */}
        <input
          type="range"
          min={min}
          max={max}
          step={step}
          value={value}
          onChange={(e) => onChange(Number(e.target.value))}
          style={{
            position: 'absolute',
            width: '100%',
            height: '100%',
            opacity: 0,
            cursor: 'pointer',
            zIndex: 2,
          }}
        />

        {/* Custom M3 Handle */}
        <motion.div
          animate={{ left: `${percentage}%` }}
          transition={{ duration: 0.1 }}
          style={{
            position: 'absolute',
            transform: 'translateX(-50%)',
            width: '4px', // M3 handle bar
            height: '44px',
            borderRadius: 'var(--radius-pill)',
            backgroundColor: 'var(--md-sys-color-primary)',
            pointerEvents: 'none',
            zIndex: 1,
          }}
        />
      </div>
    </div>
  );
};
