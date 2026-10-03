import React from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';
import { Icon } from '../communication/Icon.js';
import { Button } from '../actions/Button.js';

export interface RadioProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label?: string;
  disabled?: boolean;
  className?: string;
}

export const Radio: React.FC<RadioProps> = ({
  checked,
  onChange,
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
      className={`m3-radio ${className}`}
    >
      <div
        onClick={() => !disabled && onChange(true)}
        style={{
          width: '20px', // Official M3 20px radio circle
          height: '20px',
          borderRadius: '50%',
          border: `2px solid ${checked ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-outline)'}`,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          transition: 'border-color 0.2s',
        }}
      >
        {checked && (
          <motion.div
            initial={{ scale: 0 }}
            animate={{ scale: 1 }}
            transition={M3_TRANSITIONS.springSnappy}
            style={{
              width: '10px', // Official M3 10px inner dot
              height: '10px',
              borderRadius: '50%',
              backgroundColor: 'var(--md-sys-color-primary)',
            }}
          />
        )}
      </div>
      {label && <span style={{ fontSize: '14px', color: 'var(--md-sys-color-on-surface)' }}>{label}</span>}
    </label>
  );
};

export interface MenuItemProps {
  label: string;
  leadingIcon?: React.ReactNode;
  trailingText?: string;
  onClick?: () => void;
  disabled?: boolean;
}

export interface MenuProps {
  isOpen: boolean;
  onClose: () => void;
  items: MenuItemProps[];
  anchorEl?: HTMLElement | null;
  className?: string;
}

export const Menu: React.FC<MenuProps> = ({
  isOpen,
  onClose,
  items,
  className = '',
}) => {
  return (
    <AnimatePresence>
      {isOpen && (
        <>
          <div
            onClick={onClose}
            style={{ position: 'fixed', inset: 0, zIndex: 900 }}
          />
          <motion.div
            initial={{ scale: 0.85, opacity: 0, originX: 0, originY: 0 }}
            animate={{ scale: 1, opacity: 1 }}
            exit={{ scale: 0.9, opacity: 0 }}
            transition={M3_TRANSITIONS.enter}
            style={{
              position: 'absolute',
              minWidth: '160px',
              maxWidth: '280px',
              borderRadius: 'var(--radius-pill)', // Official M3 4px radius for Dropdown Menus
              backgroundColor: 'var(--md-sys-color-surface-container)',
              boxShadow: 'var(--md-sys-elevation-level2)',
              padding: '8px 0',
              zIndex: 901,
            }}
            className={`m3-menu ${className}`}
          >
            {items.map((item, idx) => (
              <button
                key={idx}
                onClick={() => {
                  if (!item.disabled) {
                    item.onClick?.();
                    onClose();
                  }
                }}
                disabled={item.disabled}
                style={{
                  width: '100%',
                  height: '48px',
                  border: 'none',
                  backgroundColor: 'transparent',
                  padding: '0 16px',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  gap: '12px',
                  cursor: item.disabled ? 'not-allowed' : 'pointer',
                  opacity: item.disabled ? 0.38 : 1,
                  color: 'var(--md-sys-color-on-surface)',
                  fontSize: '14px',
                  textAlign: 'left',
                  transition: 'background-color 0.15s',
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
                  {item.leadingIcon && <span style={{ display: 'flex' }}>{item.leadingIcon}</span>}
                  <span>{item.label}</span>
                </div>
                {item.trailingText && (
                  <span style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>
                    {item.trailingText}
                  </span>
                )}
              </button>
            ))}
          </motion.div>
        </>
      )}
    </AnimatePresence>
  );
};



export interface TimePickerModalProps {
  isOpen: boolean;
  onClose: () => void;
  onSelect?: (time: string) => void;
}

export const TimePickerModal: React.FC<TimePickerModalProps> = ({
  isOpen,
  onClose,
  onSelect,
}) => {
  const [hour, setHour] = React.useState('10');
  const [minute, setMinute] = React.useState('30');
  const [period, setPeriod] = React.useState<'AM' | 'PM'>('AM');

  return (
    <AnimatePresence>
      {isOpen && (
        <>
          <motion.div
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            onClick={onClose}
            style={{ position: 'fixed', inset: 0, backgroundColor: 'var(--md-sys-color-scrim)', zIndex: 998 }}
          />
          <motion.div
            initial={{ scale: 0.85, opacity: 0, x: '-50%', y: '-50%' }}
            animate={{ scale: 1, opacity: 1, x: '-50%', y: '-50%' }}
            exit={{ scale: 0.9, opacity: 0, x: '-50%', y: '-50%' }}
            transition={M3_TRANSITIONS.enter}
            style={{
              position: 'fixed',
              top: '50%',
              left: '50%',
              width: '340px',
              borderRadius: 'var(--radius-hero)', // Official M3 28px corners
              backgroundColor: 'var(--md-sys-color-surface-container-high)',
              color: 'var(--md-sys-color-on-surface)',
              padding: '24px',
              boxShadow: 'var(--md-sys-elevation-level3)',
              zIndex: 999,
              display: 'flex',
              flexDirection: 'column',
              gap: '24px',
            }}
          >
            <div style={{ fontSize: '14px', fontWeight: 600, color: 'var(--md-sys-color-on-surface-variant)' }}>
              Select Time
            </div>

            {/* Time Inputs Row */}
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '8px' }}>
              <div
                style={{
                  width: '80px',
                  height: '72px',
                  borderRadius: 'var(--radius-sm)',
                  backgroundColor: 'var(--md-sys-color-primary-container)',
                  color: 'var(--md-sys-color-on-primary-container)',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  fontSize: '28px',
                  fontWeight: 600,
                }}
              >
                {hour}
              </div>
              <span style={{ fontSize: '28px', fontWeight: 700 }}>:</span>
              <div
                style={{
                  width: '80px',
                  height: '72px',
                  borderRadius: 'var(--radius-sm)',
                  backgroundColor: 'var(--md-sys-color-surface-container-highest)',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  fontSize: '28px',
                  fontWeight: 600,
                }}
              >
                {minute}
              </div>

              {/* AM/PM Toggle */}
              <div style={{ display: 'flex', flexDirection: 'column', borderRadius: 'var(--radius-sm)', overflow: 'hidden', border: '1px solid var(--md-sys-color-outline-variant)' }}>
                <button
                  onClick={() => setPeriod('AM')}
                  style={{
                    padding: '8px 12px',
                    border: 'none',
                    backgroundColor: period === 'AM' ? 'var(--md-sys-color-tertiary-container)' : 'transparent',
                    fontWeight: 600,
                    fontSize: '12px',
                    cursor: 'pointer',
                  }}
                >
                  AM
                </button>
                <button
                  onClick={() => setPeriod('PM')}
                  style={{
                    padding: '8px 12px',
                    border: 'none',
                    backgroundColor: period === 'PM' ? 'var(--md-sys-color-tertiary-container)' : 'transparent',
                    fontWeight: 600,
                    fontSize: '12px',
                    cursor: 'pointer',
                  }}
                >
                  PM
                </button>
              </div>
            </div>

            {/* Actions */}
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '8px' }}>
              <Button variant="text" onClick={onClose}>Cancel</Button>
              <Button variant="filled" onClick={() => { onSelect?.(`${hour}:${minute} ${period}`); onClose(); }}>
                OK
              </Button>
            </div>
          </motion.div>
        </>
      )}
    </AnimatePresence>
  );
};
