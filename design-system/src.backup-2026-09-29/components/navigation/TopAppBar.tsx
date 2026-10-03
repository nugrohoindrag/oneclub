import React from 'react';
import { motion } from 'motion/react';
import { Icon } from '../communication/Icon.js';

export interface TopAppBarProps {
  title: string;
  variant?: 'center-aligned' | 'small' | 'medium' | 'large';
  leadingIcon?: React.ReactNode;
  onLeadingClick?: () => void;
  actions?: React.ReactNode;
  scrolled?: boolean;
  className?: string;
}

export const TopAppBar: React.FC<TopAppBarProps> = ({
  title,
  variant = 'small',
  leadingIcon = <Icon name="arrow_back" />,
  onLeadingClick,
  actions,
  scrolled = false,
  className = '',
}) => {
  const isCenter = variant === 'center-aligned';
  const isMedium = variant === 'medium';
  const isLarge = variant === 'large';
  const isExpanded = isMedium || isLarge;

  const height = isLarge ? '152px' : isMedium ? '112px' : '64px';

  return (
    <motion.header
      animate={{
        backgroundColor: scrolled
          ? 'var(--md-sys-color-surface-container)'
          : 'var(--md-sys-color-surface)',
        boxShadow: scrolled
          ? 'var(--md-sys-elevation-level2)'
          : 'none',
      }}
      transition={{ duration: 0.2 }}
      style={{
        height,
        width: '100%',
        color: 'var(--md-sys-color-on-surface)',
        display: 'flex',
        flexDirection: isExpanded ? 'column' : 'row',
        justifyContent: isExpanded ? 'space-between' : 'space-between',
        alignItems: isExpanded ? 'stretch' : 'center',
        padding: isExpanded ? '0 16px 20px 16px' : '0 16px',
        position: 'relative',
        userSelect: 'none',
      }}
      className={`m3-top-app-bar m3-top-app-bar-${variant} ${className}`}
    >
      {/* Top Row for icons & center title */}
      <div style={{ display: 'flex', alignItems: 'center', height: '64px', width: '100%', position: 'relative' }}>
        {leadingIcon && (
          <button
            onClick={onLeadingClick}
            style={{
              background: 'none',
              border: 'none',
              cursor: 'pointer',
              color: 'inherit',
              display: 'flex',
              padding: '8px',
              borderRadius: '50%',
            }}
          >
            {leadingIcon}
          </button>
        )}

        {!isExpanded && (
          <h1
            style={{
              margin: 0,
              fontSize: '22px', // Title Large
              fontWeight: 400,
              flex: 1,
              textAlign: isCenter ? 'center' : 'left',
              paddingLeft: isCenter ? 0 : '12px',
              whiteSpace: 'nowrap',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
            }}
          >
            {title}
          </h1>
        )}

        {actions && (
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginLeft: 'auto' }}>
            {actions}
          </div>
        )}
      </div>

      {/* Expanded Title for Medium & Large TopAppBar */}
      {isExpanded && (
        <h1
          style={{
            margin: 0,
            fontSize: isLarge ? '32px' : '28px', // Headline Medium / Large
            fontWeight: 400,
            paddingLeft: '8px',
            color: 'var(--md-sys-color-on-surface)',
          }}
        >
          {title}
        </h1>
      )}
    </motion.header>
  );
};

export interface BottomAppBarProps {
  actions: React.ReactNode;
  fab?: React.ReactNode;
  className?: string;
}

export const BottomAppBar: React.FC<BottomAppBarProps> = ({
  actions,
  fab,
  className = '',
}) => {
  return (
    <div
      style={{
        height: '80px', // Official M3 80px height
        backgroundColor: 'var(--md-sys-color-surface-container)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        padding: '0 16px',
        borderTop: '1px solid var(--md-sys-color-outline-variant)',
        width: '100%',
        boxShadow: 'var(--md-sys-elevation-level2)',
      }}
      className={`m3-bottom-app-bar ${className}`}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
        {actions}
      </div>
      {fab && <div>{fab}</div>}
    </div>
  );
};
