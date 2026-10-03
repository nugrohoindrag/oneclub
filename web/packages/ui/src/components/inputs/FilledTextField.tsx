/**
 * @license MIT
 * Text Fields — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §19 Micro-interactions — the label floats, the focus line grows from the
 *        centre, the error message arrives rather than appearing
 *   §18 Motion Design      — 120–180ms for a field state change
 *   §9  Accessibility      — reduced motion keeps the state, drops the travel
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState } from 'react';
import { AnimatePresence, motion } from 'motion/react';
import { M3_TRANSITIONS, useReducedMotionSafe } from '../../motion/index.js';

export interface TextFieldProps extends React.InputHTMLAttributes<HTMLInputElement> {
  label: string;
  error?: string;
  supportingText?: string;
  leadingIcon?: React.ReactNode;
  trailingIcon?: React.ReactNode;
}

/** Colour of the label and indicator for the current state. */
const accentFor = (isError: boolean, isFocused: boolean) =>
  isError
    ? 'var(--md-sys-color-error)'
    : isFocused
    ? 'var(--md-sys-color-primary)'
    : 'var(--md-sys-color-on-surface-variant)';

/** Supporting text / error line — swaps its message without a jump. */
const SupportingLine: React.FC<{ error?: string; supportingText?: string }> = ({
  error,
  supportingText,
}) => {
  const reduced = useReducedMotionSafe();
  const message = error || supportingText;
  const isError = Boolean(error);

  return (
    <AnimatePresence mode="wait" initial={false}>
      {message && (
        <motion.span
          key={message}
          initial={{ opacity: 0, y: reduced ? 0 : -4 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, y: reduced ? 0 : -4 }}
          transition={M3_TRANSITIONS.button}
          style={{
            fontSize: '12px',
            paddingLeft: '16px',
            color: isError ? 'var(--md-sys-color-error)' : 'var(--md-sys-color-on-surface-variant)',
          }}
        >
          {message}
        </motion.span>
      )}
    </AnimatePresence>
  );
};

export const FilledTextField: React.FC<TextFieldProps> = ({
  label,
  error,
  supportingText,
  leadingIcon,
  trailingIcon,
  value,
  defaultValue,
  onChange,
  disabled,
  className = '',
  style,
  ...props
}) => {
  const [internalVal, setInternalVal] = useState(value || defaultValue || '');
  const [isFocused, setIsFocused] = useState(false);
  const reduced = useReducedMotionSafe();

  const hasValue = Boolean(value !== undefined ? value : internalVal);
  const isFloating = isFocused || hasValue;
  const isError = Boolean(error);
  const accent = accentFor(isError, isFocused);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '4px', width: '100%', ...style }} className={`m3-filled-textfield ${className}`}>
      <div
        style={{
          position: 'relative',
          height: '56px', // Official M3 56px height
          borderRadius: 'var(--radius-xs) var(--radius-xs) 0 0', // §7 shape scale, xs
          backgroundColor: 'var(--md-sys-color-surface-container-highest)',
          borderBottom: '1px solid var(--md-sys-color-on-surface-variant)',
          display: 'flex',
          alignItems: 'center',
          opacity: disabled ? 0.38 : 1,
        }}
      >
        {/* M3's active indicator: a 2px line that grows from the centre on
            focus and retracts on blur, over the resting 1px hairline. */}
        <motion.span
          initial={false}
          animate={{ scaleX: isFocused || isError ? 1 : 0, backgroundColor: accent }}
          transition={reduced ? { duration: 0 } : M3_TRANSITIONS.button}
          style={{
            position: 'absolute',
            left: 0,
            right: 0,
            bottom: '-1px',
            height: '2px',
            transformOrigin: 'center',
          }}
        />

        {leadingIcon && (
          <span style={{ paddingLeft: '16px', display: 'flex', color: 'var(--md-sys-color-on-surface-variant)', fontSize: '20px' }}>
            {leadingIcon}
          </span>
        )}

        {/* The label travels to its floated position rather than cutting
            between two font sizes (§19). */}
        <motion.label
          initial={false}
          animate={{
            top: isFloating ? '8px' : '50%',
            y: isFloating ? '0%' : '-50%',
            fontSize: isFloating ? '12px' : '16px',
            fontWeight: isFloating ? 500 : 400,
            color: accent,
          }}
          transition={reduced ? { duration: 0 } : M3_TRANSITIONS.button}
          style={{
            position: 'absolute',
            left: leadingIcon ? '48px' : '16px',
            pointerEvents: 'none',
            transformOrigin: 'left center',
          }}
        >
          {label}
        </motion.label>

        <input
          value={value !== undefined ? value : internalVal}
          onChange={(e) => {
            setInternalVal(e.target.value);
            onChange?.(e);
          }}
          onFocus={() => setIsFocused(true)}
          onBlur={() => setIsFocused(false)}
          disabled={disabled}
          aria-invalid={isError || undefined}
          style={{
            width: '100%',
            height: '100%',
            padding: isFloating
              ? `20px 16px 4px ${leadingIcon ? '48px' : '16px'}`
              : `0 16px 0 ${leadingIcon ? '48px' : '16px'}`,
            fontSize: '16px',
            color: 'var(--md-sys-color-on-surface)',
            backgroundColor: 'transparent',
            border: 'none',
            outline: 'none',
          }}
          {...props}
        />

        {trailingIcon && (
          <span style={{ paddingRight: '16px', display: 'flex', color: 'var(--md-sys-color-on-surface-variant)', fontSize: '20px' }}>
            {trailingIcon}
          </span>
        )}
      </div>

      <SupportingLine error={error} supportingText={supportingText} />
    </div>
  );
};

export const OutlinedTextField: React.FC<TextFieldProps> = ({
  label,
  error,
  supportingText,
  leadingIcon,
  trailingIcon,
  value,
  defaultValue,
  onChange,
  disabled,
  className = '',
  style,
  ...props
}) => {
  const [internalVal, setInternalVal] = useState(value || defaultValue || '');
  const [isFocused, setIsFocused] = useState(false);
  const reduced = useReducedMotionSafe();

  const hasValue = Boolean(value !== undefined ? value : internalVal);
  const isFloating = isFocused || hasValue;
  const isError = Boolean(error);
  const accent = accentFor(isError, isFocused);
  const outline = isError
    ? 'var(--md-sys-color-error)'
    : isFocused
    ? 'var(--md-sys-color-primary)'
    : 'var(--md-sys-color-outline)';

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '4px', width: '100%', ...style }} className={`m3-outlined-textfield ${className}`}>
      {/* The outline thickens and tints on focus in one tween (§18). */}
      <motion.div
        initial={false}
        animate={{
          borderColor: outline,
          boxShadow: isFocused ? `0 0 0 1px ${isError ? 'var(--md-sys-color-error)' : 'var(--md-sys-color-primary)'}` : '0 0 0 0px rgba(0,0,0,0)',
        }}
        transition={reduced ? { duration: 0 } : M3_TRANSITIONS.button}
        style={{
          position: 'relative',
          height: '56px',
          borderRadius: 'var(--radius-pill)', // Official M3 4px radius for Outlined text field
          border: '1px solid var(--md-sys-color-outline)',
          display: 'flex',
          alignItems: 'center',
          backgroundColor: 'transparent',
          opacity: disabled ? 0.38 : 1,
        }}
      >
        {leadingIcon && (
          <span style={{ paddingLeft: '16px', display: 'flex', color: 'var(--md-sys-color-on-surface-variant)', fontSize: '20px' }}>
            {leadingIcon}
          </span>
        )}

        {/* Notched Floating Label — it rides up into the outline notch. */}
        <motion.label
          initial={false}
          animate={{
            top: isFloating ? '0%' : '50%',
            fontSize: isFloating ? '12px' : '16px',
            fontWeight: isFloating ? 500 : 400,
            paddingLeft: isFloating ? '4px' : '0px',
            paddingRight: isFloating ? '4px' : '0px',
            color: accent,
            backgroundColor: isFloating
              ? 'var(--md-sys-color-background)'
              : 'rgba(0,0,0,0)',
          }}
          transition={reduced ? { duration: 0 } : M3_TRANSITIONS.button}
          style={{
            position: 'absolute',
            left: leadingIcon ? '48px' : '16px',
            y: '-50%',
            pointerEvents: 'none',
          }}
        >
          {label}
        </motion.label>

        <input
          value={value !== undefined ? value : internalVal}
          onChange={(e) => {
            setInternalVal(e.target.value);
            onChange?.(e);
          }}
          onFocus={() => setIsFocused(true)}
          onBlur={() => setIsFocused(false)}
          disabled={disabled}
          aria-invalid={isError || undefined}
          style={{
            width: '100%',
            height: '100%',
            padding: `0 16px 0 ${leadingIcon ? '48px' : '16px'}`,
            fontSize: '16px',
            color: 'var(--md-sys-color-on-surface)',
            backgroundColor: 'transparent',
            border: 'none',
            outline: 'none',
          }}
          {...props}
        />

        {trailingIcon && (
          <span style={{ paddingRight: '16px', display: 'flex', color: 'var(--md-sys-color-on-surface-variant)', fontSize: '20px' }}>
            {trailingIcon}
          </span>
        )}
      </motion.div>

      <SupportingLine error={error} supportingText={supportingText} />
    </div>
  );
};
