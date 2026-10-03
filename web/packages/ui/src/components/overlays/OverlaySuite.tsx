/**
 * @license MIT
 * 11. Overlays Suite — Morphic Design System
 * 
 * Includes: Modal, Popover, ContextMenu, Tooltip
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState, useRef, useEffect } from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';
import { Icon } from '../communication/Icon.js';

// 1. Modal (Universal Modal Frame)
export interface ModalProps {
  isOpen: boolean;
  onClose: () => void;
  title?: string;
  children: React.ReactNode;
  maxWidth?: string;
  className?: string;
}

export const Modal: React.FC<ModalProps> = ({
  isOpen,
  onClose,
  title,
  children,
  maxWidth = '520px',
  className = '',
}) => {
  return (
    <AnimatePresence>
      {isOpen && (
        <>
          <motion.div
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            onClick={onClose}
            style={{
              position: 'fixed',
              inset: 0,
              backgroundColor: 'var(--md-sys-color-scrim)',
              zIndex: 998,
              backdropFilter: 'blur(4px)',
            }}
          />

          <motion.div
            initial={{ scale: 0.9, opacity: 0, x: '-50%', y: '-50%' }}
            animate={{ scale: 1, opacity: 1, x: '-50%', y: '-50%' }}
            exit={{ scale: 0.92, opacity: 0, x: '-50%', y: '-50%' }}
            transition={M3_TRANSITIONS.enter}
            className={`morphic-modal ${className}`}
            style={{
              position: 'fixed',
              top: '50%',
              left: '50%',
              width: '90%',
              maxWidth,
              borderRadius: 'var(--radius-hero)', // 28px
              backgroundColor: 'var(--md-sys-color-surface)',
              color: 'var(--md-sys-color-on-surface)',
              padding: '28px',
              boxShadow: 'var(--md-sys-elevation-level3)',
              zIndex: 999,
              border: '1px solid var(--md-sys-color-border)',
            }}
          >
            {title && (
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px' }}>
                <h3 style={{ margin: 0, fontSize: '18px', fontWeight: 700 }}>{title}</h3>
                <button
                  onClick={onClose}
                  style={{ border: 'none', backgroundColor: 'transparent', cursor: 'pointer', color: 'var(--md-sys-color-on-surface-variant)' }}
                >
                  <Icon name="close" size={20} />
                </button>
              </div>
            )}
            {children}
          </motion.div>
        </>
      )}
    </AnimatePresence>
  );
};

// 2. Popover (Anchored Popover)
export interface PopoverProps {
  trigger: React.ReactNode;
  content: React.ReactNode;
  placement?: 'top' | 'bottom' | 'left' | 'right';
  className?: string;
}

export const Popover: React.FC<PopoverProps> = ({
  trigger,
  content,
  placement = 'bottom',
  className = '',
}) => {
  const [isOpen, setIsOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    };
    if (isOpen) document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [isOpen]);

  return (
    <div ref={containerRef} style={{ position: 'relative', display: 'inline-block' }} className={className}>
      <div onClick={() => setIsOpen(!isOpen)} style={{ cursor: 'pointer' }}>
        {trigger}
      </div>

      <AnimatePresence>
        {isOpen && (
          <motion.div
            initial={{ opacity: 0, scale: 0.95, y: placement === 'bottom' ? 4 : -4 }}
            animate={{ opacity: 1, scale: 1, y: 0 }}
            exit={{ opacity: 0, scale: 0.95, y: placement === 'bottom' ? 4 : -4 }}
            transition={{ duration: 0.15 }}
            style={{
              position: 'absolute',
              top: placement === 'bottom' ? 'calc(100% + 8px)' : 'auto',
              bottom: placement === 'top' ? 'calc(100% + 8px)' : 'auto',
              left: '0',
              zIndex: 100,
              backgroundColor: 'var(--md-sys-color-surface)',
              borderRadius: 'var(--radius-lg)',
              padding: '16px',
              boxShadow: 'var(--md-sys-elevation-level2)',
              border: '1px solid var(--md-sys-color-border)',
              minWidth: '220px',
            }}
          >
            {content}
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
};

// 3. ContextMenu Item & Menu
export interface ContextMenuItem {
  id: string;
  label: string;
  icon?: string;
  danger?: boolean;
  onClick?: () => void;
}

export const ContextMenu: React.FC<{
  items: ContextMenuItem[];
  onClose?: () => void;
}> = ({ items, onClose }) => {
  return (
    <div
      style={{
        borderRadius: 'var(--radius-md)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        boxShadow: 'var(--md-sys-elevation-level2)',
        padding: '6px',
        minWidth: '180px',
      }}
    >
      {items.map((item) => (
        <button
          key={item.id}
          onClick={() => {
            item.onClick?.();
            onClose?.();
          }}
          style={{
            width: '100%',
            padding: '8px 12px',
            borderRadius: 'var(--radius-sm)',
            border: 'none',
            backgroundColor: 'transparent',
            color: item.danger ? 'var(--md-sys-color-error)' : 'var(--md-sys-color-on-surface)',
            fontSize: '13px',
            fontWeight: 500,
            display: 'flex',
            alignItems: 'center',
            gap: '8px',
            cursor: 'pointer',
            textAlign: 'left',
            transition: 'background-color 0.12s ease',
          }}
          onMouseEnter={(e) => (e.currentTarget.style.backgroundColor = 'var(--md-sys-color-surface-container)')}
          onMouseLeave={(e) => (e.currentTarget.style.backgroundColor = 'transparent')}
        >
          {item.icon && <Icon name={item.icon} size={16} />}
          <span>{item.label}</span>
        </button>
      ))}
    </div>
  );
};
