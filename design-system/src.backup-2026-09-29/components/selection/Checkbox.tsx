import React from 'react';
import { motion } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';

export interface CheckboxProps {
  checked: boolean;
  indeterminate?: boolean;
  onChange: (checked: boolean) => void;
  label?: string;
  disabled?: boolean;
  className?: string;
}

export const Checkbox: React.FC<CheckboxProps> = ({
  checked,
  indeterminate = false,
  onChange,
  label,
  disabled = false,
  className = '',
}) => {
  const isFilled = checked || indeterminate;

  return (
    <label
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: '12px',
        cursor: disabled ? 'not-allowed' : 'pointer',
        opacity: disabled ? 0.38 : 1,
        userSelect: 'none',
      }}
      className={`m3-checkbox ${className}`}
    >
      <motion.div
        whileTap={disabled ? undefined : { scale: 0.85 }}
        onClick={() => !disabled && onChange(!checked)}
        style={{
          width: '18px', // Official M3 18px checkbox
          height: '18px',
          borderRadius: 'var(--radius-pill)', // Official M3 2px radius
          backgroundColor: isFilled ? 'var(--md-sys-color-primary)' : 'transparent',
          border: `2px solid ${isFilled ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-outline)'}`,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          transition: 'background-color 0.15s, border-color 0.15s',
        }}
      >
        {checked && !indeterminate && (
          <motion.svg
            initial={{ scale: 0 }}
            animate={{ scale: 1 }}
            transition={M3_TRANSITIONS.springSnappy}
            width="12"
            height="12"
            viewBox="0 0 24 24"
            fill="none"
            stroke="var(--md-sys-color-on-primary)"
            strokeWidth="3.5"
            strokeLinecap="round"
            strokeLinejoin="round"
          >
            <polyline points="20 6 9 17 4 12" />
          </motion.svg>
        )}

        {indeterminate && (
          <motion.div
            initial={{ scale: 0 }}
            animate={{ scale: 1 }}
            transition={M3_TRANSITIONS.springSnappy}
            style={{
              width: '10px',
              height: '2.5px',
              borderRadius: 'var(--radius-pill)',
              backgroundColor: 'var(--md-sys-color-on-primary)',
            }}
          />
        )}
      </motion.div>
      {label && <span style={{ fontSize: '14px', color: 'var(--md-sys-color-on-surface)' }}>{label}</span>}
    </label>
  );
};

export interface SwitchProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  showIcon?: boolean;
  label?: string;
  disabled?: boolean;
  className?: string;
}

export const Switch: React.FC<SwitchProps> = ({
  checked,
  onChange,
  showIcon = true,
  label,
  disabled = false,
  className = '',
}) => {
  return (
    <label
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: '12px',
        cursor: disabled ? 'not-allowed' : 'pointer',
        opacity: disabled ? 0.38 : 1,
        userSelect: 'none',
      }}
      className={`m3-switch ${className}`}
    >
      <div
        onClick={() => !disabled && onChange(!checked)}
        style={{
          width: '52px', // Official M3 track 52x32px
          height: '32px',
          borderRadius: 'var(--radius-lg)',
          backgroundColor: checked
            ? 'var(--md-sys-color-primary)'
            : 'var(--md-sys-color-surface-container-highest)',
          border: checked ? 'none' : '2px solid var(--md-sys-color-outline)',
          display: 'flex',
          alignItems: 'center',
          padding: '0 4px',
          justifyContent: checked ? 'flex-end' : 'flex-start',
          transition: 'background-color 0.2s, border-color 0.2s',
          position: 'relative',
        }}
      >
        <motion.div
          layout
          transition={M3_TRANSITIONS.springSnappy}
          style={{
            width: checked ? '24px' : '16px', // Official M3 thumb 16px -> 24px
            height: checked ? '24px' : '16px',
            borderRadius: '50%',
            backgroundColor: checked
              ? 'var(--md-sys-color-on-primary)'
              : 'var(--md-sys-color-outline)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            fontSize: '11px',
            color: 'var(--md-sys-color-primary)',
          }}
        >
          {checked && showIcon && '✓'}
        </motion.div>
      </div>
      {label && <span style={{ fontSize: '14px', color: 'var(--md-sys-color-on-surface)' }}>{label}</span>}
    </label>
  );
};
