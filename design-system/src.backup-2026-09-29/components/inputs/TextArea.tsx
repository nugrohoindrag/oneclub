/**
 * @license MIT
 * TextArea & NumberInput Components — Material Design 3 Inputs
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §19 Micro-interactions — focus tint, counter warning, stepper press
 *   §18 Motion Design      — 120–180ms for an input state change
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState } from 'react';
import { AnimatePresence, motion } from 'motion/react';
import { Icon } from '../communication/Icon.js';
import { M3_SPRING, M3_TRANSITIONS, useReducedMotionSafe } from '../../motion/index.js';

export interface TextAreaProps extends React.TextareaHTMLAttributes<HTMLTextAreaElement> {
  label: string;
  supportingText?: string;
  error?: string;
  maxLength?: number;
  showCounter?: boolean;
}

export const TextArea: React.FC<TextAreaProps> = ({
  label,
  supportingText,
  error,
  maxLength,
  showCounter = true,
  value,
  onChange,
  disabled = false,
  rows = 4,
  className = '',
  ...rest
}) => {
  const [charCount, setCharCount] = useState(typeof value === 'string' ? value.length : 0);
  const [isFocused, setIsFocused] = useState(false);
  const reduced = useReducedMotionSafe();

  const handleChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    setCharCount(e.target.value.length);
    onChange?.(e);
  };

  const accent = error
    ? 'var(--md-sys-color-error)'
    : isFocused
    ? 'var(--md-sys-color-primary)'
    : 'var(--md-sys-color-on-surface-variant)';

  const borderColor = error
    ? 'var(--md-sys-color-error)'
    : isFocused
    ? 'var(--md-sys-color-primary)'
    : 'var(--md-sys-color-border)';

  // The counter turns critical in the last 10% of the budget, and says so
  // with a single pulse rather than a colour that silently swaps (§19).
  const nearLimit = Boolean(maxLength) && charCount >= (maxLength as number) * 0.9;
  const message = error || supportingText;

  return (
    <div className={`morphic-textarea-container ${className}`} style={{ width: '100%' }}>
      <motion.div
        initial={false}
        animate={{
          borderColor,
          boxShadow: isFocused ? `0 0 0 1px ${borderColor}` : '0 0 0 0px rgba(0,0,0,0)',
        }}
        transition={reduced ? { duration: 0 } : M3_TRANSITIONS.button}
        style={{
          position: 'relative',
          borderRadius: 'var(--radius-md)',
          backgroundColor: 'var(--md-sys-color-surface-container)',
          border: '1px solid var(--md-sys-color-border)',
          padding: '10px 16px',
          opacity: disabled ? 0.38 : 1,
        }}
      >
        <motion.label
          initial={false}
          animate={{ color: accent }}
          transition={reduced ? { duration: 0 } : M3_TRANSITIONS.button}
          style={{
            display: 'block',
            fontSize: '11px',
            fontWeight: 600,
            marginBottom: '4px',
          }}
        >
          {label}
        </motion.label>
        <textarea
          rows={rows}
          maxLength={maxLength}
          disabled={disabled}
          value={value}
          onChange={handleChange}
          onFocus={() => setIsFocused(true)}
          onBlur={() => setIsFocused(false)}
          aria-invalid={Boolean(error) || undefined}
          style={{
            width: '100%',
            backgroundColor: 'transparent',
            border: 'none',
            outline: 'none',
            color: 'var(--md-sys-color-on-surface)',
            fontSize: '14px',
            fontFamily: 'inherit',
            resize: 'vertical',
            lineHeight: 1.5,
          }}
          {...rest}
        />
      </motion.div>

      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          fontSize: '11px',
          color: error ? 'var(--md-sys-color-error)' : 'var(--md-sys-color-on-surface-variant)',
          marginTop: '4px',
          padding: '0 4px',
        }}
      >
        <AnimatePresence mode="wait" initial={false}>
          <motion.span
            key={message || 'empty'}
            initial={{ opacity: 0, y: reduced ? 0 : -3 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: reduced ? 0 : -3 }}
            transition={M3_TRANSITIONS.button}
          >
            {message}
          </motion.span>
        </AnimatePresence>
        {showCounter && maxLength && (
          <motion.span
            initial={false}
            animate={{
              color: nearLimit ? 'var(--md-sys-color-error)' : 'var(--md-sys-color-on-surface-variant)',
              scale: reduced ? 1 : nearLimit ? 1.06 : 1,
            }}
            transition={M3_SPRING.snappy}
            style={{ fontFeatureSettings: '"tnum" 1', transformOrigin: 'right center' }}
          >
            {charCount}/{maxLength}
          </motion.span>
        )}
      </div>
    </div>
  );
};

export interface NumberInputProps {
  label: string;
  value: number;
  onChange: (value: number) => void;
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  disabled?: boolean;
  className?: string;
}

export const NumberInput: React.FC<NumberInputProps> = ({
  label,
  value,
  onChange,
  min = 0,
  max = 100,
  step = 1,
  unit,
  disabled = false,
  className = '',
}) => {
  const reduced = useReducedMotionSafe();
  // Which way the value last moved, so the digits roll in that direction.
  const [direction, setDirection] = useState(1);

  const atMin = disabled || value <= min;
  const atMax = disabled || value >= max;

  const handleDecrement = () => {
    if (!disabled && value - step >= min) {
      setDirection(-1);
      onChange(value - step);
    }
  };

  const handleIncrement = () => {
    if (!disabled && value + step <= max) {
      setDirection(1);
      onChange(value + step);
    }
  };

  const stepperStyle: React.CSSProperties = {
    width: '40px',
    height: '40px',
    border: 'none',
    backgroundColor: 'transparent',
    color: 'var(--md-sys-color-on-surface)',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
  };

  return (
    <div
      className={`morphic-number-input ${className}`}
      style={{
        display: 'inline-flex',
        flexDirection: 'column',
        gap: '4px',
      }}
    >
      <span style={{ fontSize: '11px', fontWeight: 600, color: 'var(--md-sys-color-on-surface-variant)' }}>
        {label}
      </span>

      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          borderRadius: 'var(--radius-md)',
          backgroundColor: 'var(--md-sys-color-surface-container)',
          border: '1px solid var(--md-sys-color-border)',
          overflow: 'hidden',
          opacity: disabled ? 0.38 : 1,
        }}
      >
        {/* Steppers press like every other button in the system: scale 0.98
            with the shared 120–180ms button transition (§19). */}
        <motion.button
          disabled={atMin}
          onClick={handleDecrement}
          whileTap={atMin || reduced ? undefined : { scale: 0.9 }}
          whileHover={atMin ? undefined : { backgroundColor: 'var(--md-sys-color-surface-container-high)' }}
          transition={M3_TRANSITIONS.button}
          aria-label={`Decrease ${label}`}
          style={{ ...stepperStyle, cursor: atMin ? 'not-allowed' : 'pointer' }}
        >
          <Icon name="remove" size={18} />
        </motion.button>

        <div
          style={{
            minWidth: '60px',
            textAlign: 'center',
            fontSize: '14px',
            fontWeight: 700,
            color: 'var(--md-sys-color-on-surface)',
            fontFeatureSettings: '"tnum" 1',
            padding: '0 8px',
            overflow: 'hidden',
          }}
        >
          {/* The number rolls up or down with the direction of the step. */}
          <AnimatePresence mode="popLayout" initial={false}>
            <motion.span
              key={value}
              initial={{ opacity: 0, y: reduced ? 0 : direction * 12 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, y: reduced ? 0 : direction * -12 }}
              transition={M3_TRANSITIONS.button}
              style={{ display: 'inline-block' }}
            >
              {value}
            </motion.span>
          </AnimatePresence>{' '}
          {unit && <span style={{ fontSize: '12px', fontWeight: 400, opacity: 0.7 }}>{unit}</span>}
        </div>

        <motion.button
          disabled={atMax}
          onClick={handleIncrement}
          whileTap={atMax || reduced ? undefined : { scale: 0.9 }}
          whileHover={atMax ? undefined : { backgroundColor: 'var(--md-sys-color-surface-container-high)' }}
          transition={M3_TRANSITIONS.button}
          aria-label={`Increase ${label}`}
          style={{ ...stepperStyle, cursor: atMax ? 'not-allowed' : 'pointer' }}
        >
          <Icon name="add" size={18} />
        </motion.button>
      </div>
    </div>
  );
};
