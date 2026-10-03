/**
 * @license MIT
 * SplitButton Component — Material Design 3 Actions
 * 
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState, useRef, useEffect } from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { Icon } from '../communication/Icon.js';

export interface SplitButtonItem {
  id: string;
  label: string;
  icon?: string;
  onClick?: () => void;
  disabled?: boolean;
}

export interface SplitButtonProps {
  label: string;
  icon?: React.ReactNode;
  variant?: 'filled' | 'tonal' | 'outlined' | 'elevated';
  size?: 'sm' | 'md' | 'lg';
  items: SplitButtonItem[];
  onClick?: () => void;
  disabled?: boolean;
  className?: string;
}

export const SplitButton: React.FC<SplitButtonProps> = ({
  label,
  icon,
  variant = 'filled',
  size = 'md',
  items,
  onClick,
  disabled = false,
  className = '',
}) => {
  const [isOpen, setIsOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setIsOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  const sizeStyles = {
    sm: { height: '32px', fontSize: '12px', paddingMain: '0 12px', paddingArrow: '0 8px' },
    md: { height: '40px', fontSize: '13px', paddingMain: '0 16px', paddingArrow: '0 10px' },
    lg: { height: '48px', fontSize: '14px', paddingMain: '0 20px', paddingArrow: '0 12px' },
  }[size];

  const variantStyles = {
    filled: {
      bg: 'var(--md-sys-color-primary)',
      color: 'var(--md-sys-color-on-primary, #F0F4EC)',
      border: 'none',
      separator: 'rgba(255, 255, 255, 0.25)',
    },
    tonal: {
      bg: 'var(--md-sys-color-surface-container-high)',
      color: 'var(--md-sys-color-on-surface)',
      border: 'none',
      separator: 'var(--md-sys-color-outline-variant)',
    },
    outlined: {
      bg: 'transparent',
      color: 'var(--md-sys-color-primary)',
      border: '1px solid var(--md-sys-color-outline)',
      separator: 'var(--md-sys-color-outline)',
    },
    elevated: {
      bg: 'var(--md-sys-color-surface)',
      color: 'var(--md-sys-color-primary)',
      border: 'none',
      separator: 'var(--md-sys-color-outline-variant)',
      boxShadow: 'var(--md-sys-elevation-level1)',
    },
  }[variant];

  return (
    <div
      ref={containerRef}
      className={`morphic-split-button ${className}`}
      style={{ position: 'relative', display: 'inline-flex', zIndex: isOpen ? 50 : 1 }}
    >
      <div
        style={{
          display: 'inline-flex',
          borderRadius: 'var(--radius-pill)',
          overflow: 'hidden',
          backgroundColor: variantStyles.bg,
          border: variantStyles.border,
          boxShadow: (variantStyles as any).boxShadow,
          opacity: disabled ? 0.38 : 1,
        }}
      >
        {/* Main Action Button */}
        <button
          disabled={disabled}
          onClick={onClick}
          style={{
            height: sizeStyles.height,
            padding: sizeStyles.paddingMain,
            fontSize: sizeStyles.fontSize,
            fontWeight: 600,
            color: variantStyles.color,
            backgroundColor: 'transparent',
            border: 'none',
            display: 'inline-flex',
            alignItems: 'center',
            gap: '8px',
            cursor: disabled ? 'not-allowed' : 'pointer',
          }}
        >
          {icon}
          <span>{label}</span>
        </button>

        {/* Vertical Divider */}
        <div style={{ width: '1px', backgroundColor: variantStyles.separator, margin: '6px 0' }} />

        {/* Dropdown Trigger Arrow */}
        <button
          disabled={disabled}
          onClick={() => setIsOpen((prev) => !prev)}
          style={{
            height: sizeStyles.height,
            padding: sizeStyles.paddingArrow,
            backgroundColor: 'transparent',
            border: 'none',
            color: variantStyles.color,
            display: 'inline-flex',
            alignItems: 'center',
            justifyContent: 'center',
            cursor: disabled ? 'not-allowed' : 'pointer',
          }}
        >
          <Icon name={isOpen ? 'arrow_drop_up' : 'arrow_drop_down'} size={18} />
        </button>
      </div>

      {/* Popover Dropdown Menu */}
      <AnimatePresence>
        {isOpen && (
          <motion.div
            initial={{ opacity: 0, y: 6, scale: 0.96 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, y: 4, scale: 0.96 }}
            transition={{ duration: 0.15 }}
            style={{
              position: 'absolute',
              top: 'calc(100% + 6px)',
              right: 0,
              minWidth: '180px',
              borderRadius: 'var(--radius-md)',
              backgroundColor: 'var(--md-sys-color-surface)',
              color: 'var(--md-sys-color-on-surface)',
              border: '1px solid var(--md-sys-color-border)',
              boxShadow: 'var(--md-sys-elevation-level3)',
              padding: '6px',
              zIndex: 990,
              display: 'flex',
              flexDirection: 'column',
              gap: '2px',
            }}
          >
            {items.map((item) => (
              <button
                key={item.id}
                disabled={item.disabled}
                onClick={() => {
                  item.onClick?.();
                  setIsOpen(false);
                }}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: '8px',
                  padding: '8px 12px',
                  borderRadius: 'var(--radius-sm)',
                  backgroundColor: 'transparent',
                  border: 'none',
                  color: 'inherit',
                  fontSize: '13px',
                  fontWeight: 500,
                  cursor: item.disabled ? 'not-allowed' : 'pointer',
                  opacity: item.disabled ? 0.4 : 1,
                  textAlign: 'left',
                  width: '100%',
                  transition: 'background-color 0.12s ease',
                }}
                onMouseEnter={(e) => ((e.target as HTMLElement).style.backgroundColor = 'var(--md-sys-color-surface-container)')}
                onMouseLeave={(e) => ((e.target as HTMLElement).style.backgroundColor = 'transparent')}
              >
                {item.icon && <Icon name={item.icon} size={16} />}
                <span>{item.label}</span>
              </button>
            ))}
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
};
