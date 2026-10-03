/**
 * @license MIT
 * Surfaces & Containers Suite — Morphic Design System
 * 
 * Includes: SectionCard, Panel, Surface
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState } from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { Icon } from '../communication/Icon.js';
import { Card } from './Card.js';

// 1. Surface (Generic Morphic Surface)
export interface SurfaceProps extends React.HTMLAttributes<HTMLDivElement> {
  elevation?: 0 | 1 | 2 | 3 | 4 | 5;
  radius?: 'xs' | 'sm' | 'md' | 'lg' | 'xl' | 'hero' | 'pill';
  containerType?: 'surface' | 'surface-container-low' | 'surface-container' | 'surface-container-high' | 'glass';
  border?: boolean;
  padding?: 'none' | 'sm' | 'md' | 'lg' | 'xl';
  children: React.ReactNode;
}

export const Surface: React.FC<SurfaceProps> = ({
  elevation = 1,
  radius = 'xl',
  containerType = 'surface',
  border = true,
  padding = 'md',
  children,
  style,
  className = '',
  ...props
}) => {
  const radiusMap = {
    xs: 'var(--radius-xs)',
    sm: 'var(--radius-sm)',
    md: 'var(--radius-md)',
    lg: 'var(--radius-lg)',
    xl: 'var(--radius-xl)',
    hero: 'var(--radius-hero)',
    pill: 'var(--radius-pill)',
  };

  const paddingMap = {
    none: '0',
    sm: '12px',
    md: '20px',
    lg: '28px',
    xl: '36px',
  };

  const bgMap = {
    surface: 'var(--md-sys-color-surface)',
    'surface-container-low': 'var(--md-sys-color-surface-container-low)',
    'surface-container': 'var(--md-sys-color-surface-container)',
    'surface-container-high': 'var(--md-sys-color-surface-container-high)',
    glass: 'rgba(255, 255, 255, 0.08)',
  };

  return (
    <div
      className={`morphic-surface ${className}`}
      style={{
        borderRadius: radiusMap[radius],
        backgroundColor: bgMap[containerType],
        border: border ? '1px solid var(--md-sys-color-border)' : 'none',
        boxShadow: `var(--md-sys-elevation-level${elevation})`,
        padding: paddingMap[padding],
        ...style,
      }}
      {...props}
    >
      {children}
    </div>
  );
};

// 2. SectionCard (Structured Header, Body, Footer)
export interface SectionCardProps {
  title: string;
  subtitle?: string;
  action?: React.ReactNode;
  footer?: React.ReactNode;
  variant?: 'elevated' | 'filled' | 'outlined';
  children: React.ReactNode;
  className?: string;
}

export const SectionCard: React.FC<SectionCardProps> = ({
  title,
  subtitle,
  action,
  footer,
  variant = 'outlined',
  children,
  className = '',
}) => {
  return (
    <Card variant={variant} padding="none" className={`morphic-section-card ${className}`}>
      <div
        style={{
          padding: '20px 24px',
          borderBottom: '1px solid var(--md-sys-color-border)',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
        }}
      >
        <div>
          <h3 style={{ margin: 0, fontSize: '16px', fontWeight: 700 }}>{title}</h3>
          {subtitle && <p style={{ margin: '2px 0 0', fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>{subtitle}</p>}
        </div>
        {action && <div>{action}</div>}
      </div>

      <div style={{ padding: '24px' }}>{children}</div>

      {footer && (
        <div
          style={{
            padding: '14px 24px',
            borderTop: '1px solid var(--md-sys-color-border)',
            backgroundColor: 'var(--md-sys-color-surface-container-low)',
            display: 'flex',
            justifyContent: 'flex-end',
            alignItems: 'center',
            gap: '8px',
          }}
        >
          {footer}
        </div>
      )}
    </Card>
  );
};

// 3. Panel (Collapsible Accordion / Container Panel)
export interface PanelProps {
  title: string;
  subtitle?: string;
  icon?: string;
  defaultExpanded?: boolean;
  badge?: string;
  children: React.ReactNode;
  className?: string;
}

export const Panel: React.FC<PanelProps> = ({
  title,
  subtitle,
  icon,
  defaultExpanded = false,
  badge,
  children,
  className = '',
}) => {
  const [isExpanded, setIsExpanded] = useState(defaultExpanded);

  return (
    <div
      className={`morphic-panel ${className}`}
      style={{
        borderRadius: 'var(--radius-lg)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        overflow: 'hidden',
        boxShadow: 'var(--md-sys-elevation-level1)',
      }}
    >
      <button
        onClick={() => setIsExpanded(!isExpanded)}
        style={{
          width: '100%',
          padding: '16px 20px',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          backgroundColor: 'transparent',
          border: 'none',
          cursor: 'pointer',
          color: 'inherit',
          textAlign: 'left',
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
          {icon && <Icon name={icon} size={20} color="var(--md-sys-color-primary)" />}
          <div>
            <div style={{ fontSize: '14px', fontWeight: 700 }}>{title}</div>
            {subtitle && <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>{subtitle}</div>}
          </div>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
          {badge && (
            <span
              style={{
                fontSize: '11px',
                padding: '2px 8px',
                borderRadius: 'var(--radius-pill)',
                backgroundColor: 'var(--md-sys-color-primary-container)',
                color: 'var(--md-sys-color-on-primary-container)',
                fontWeight: 600,
              }}
            >
              {badge}
            </span>
          )}
          <motion.div animate={{ rotate: isExpanded ? 180 : 0 }} transition={{ duration: 0.2 }}>
            <Icon name="expand_more" size={20} />
          </motion.div>
        </div>
      </button>

      <AnimatePresence>
        {isExpanded && (
          <motion.div
            initial={{ height: 0, opacity: 0 }}
            animate={{ height: 'auto', opacity: 1 }}
            exit={{ height: 0, opacity: 0 }}
            transition={{ duration: 0.22, ease: [0.2, 0, 0, 1] }}
          >
            <div style={{ padding: '0 20px 20px', borderTop: '1px solid var(--md-sys-color-border)', paddingTop: '16px' }}>
              {children}
            </div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
};
