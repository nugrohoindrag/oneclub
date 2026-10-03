import React from 'react';
import { motion } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';

export interface DrawerItem {
  id: string;
  label: string;
  icon?: React.ReactNode;
  badge?: string | number;
}

export interface NavigationDrawerProps {
  headline?: string;
  items: DrawerItem[];
  activeId: string;
  onSelect: (id: string) => void;
  className?: string;
}

export const NavigationDrawer: React.FC<NavigationDrawerProps> = ({
  headline = 'Mailbox',
  items,
  activeId,
  onSelect,
  className = '',
}) => {
  return (
    <aside
      style={{
        width: '360px', // Official M3 360px width for Drawer
        height: '100%',
        backgroundColor: 'var(--md-sys-color-surface-container-low)',
        padding: '12px',
        display: 'flex',
        flexDirection: 'column',
        gap: '4px',
        userSelect: 'none',
      }}
      className={`m3-navigation-drawer ${className}`}
    >
      {headline && (
        <div style={{ padding: '16px 16px 12px', fontSize: '14px', fontWeight: 600, color: 'var(--md-sys-color-on-surface-variant)' }}>
          {headline}
        </div>
      )}

      {items.map((item) => {
        const isActive = item.id === activeId;
        return (
          <button
            key={item.id}
            onClick={() => onSelect(item.id)}
            style={{
              height: '56px', // Official M3 56px height for Drawer items
              borderRadius: 'var(--radius-hero)', // Stadium shape 28px
              border: 'none',
              padding: '0 24px 0 16px',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              backgroundColor: 'transparent',
              color: isActive
                ? 'var(--md-sys-color-on-secondary-container)'
                : 'var(--md-sys-color-on-surface-variant)',
              cursor: 'pointer',
              fontSize: '14px',
              fontWeight: isActive ? 600 : 500,
              position: 'relative',
              zIndex: 1,
            }}
          >
            {isActive && (
              <motion.div
                layoutId="activeDrawerStadium"
                transition={M3_TRANSITIONS.springSnappy}
                style={{
                  position: 'absolute',
                  inset: 0,
                  borderRadius: 'var(--radius-hero)',
                  backgroundColor: 'var(--md-sys-color-secondary-container)',
                  zIndex: -1,
                }}
              />
            )}
            <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
              {item.icon && <span style={{ fontSize: '20px' }}>{item.icon}</span>}
              <span>{item.label}</span>
            </div>
            {item.badge !== undefined && (
              <span style={{ fontSize: '12px', fontWeight: 600 }}>{item.badge}</span>
            )}
          </button>
        );
      })}
    </aside>
  );
};

export interface SearchBarProps {
  placeholder?: string;
  value?: string;
  onChange?: (val: string) => void;
  leadingIcon?: React.ReactNode;
  trailingAvatar?: React.ReactNode;
  onSearch?: () => void;
  className?: string;
}

export const SearchBar: React.FC<SearchBarProps> = ({
  placeholder = 'Telusuri di sini...',
  value,
  onChange,
  leadingIcon,
  trailingAvatar,
  onSearch,
  className = '',
}) => {
  return (
    <motion.div
      whileFocus={{ boxShadow: 'var(--md-sys-elevation-level3)' }}
      style={{
        height: '56px', // Official M3 56px height for Search Bar
        borderRadius: 'var(--radius-hero)', // Full pill shape
        backgroundColor: 'var(--md-sys-color-surface-container-high)',
        color: 'var(--md-sys-color-on-surface)',
        display: 'flex',
        alignItems: 'center',
        padding: '0 16px',
        gap: '12px',
        width: '100%',
        maxWidth: '720px',
        boxShadow: 'var(--md-sys-elevation-level1)',
      }}
      className={`m3-search-bar ${className}`}
    >
      <span style={{ display: 'flex', color: 'var(--md-sys-color-on-surface-variant)', fontSize: '20px' }}>
        {leadingIcon || '🔍'}
      </span>
      <input
        type="text"
        placeholder={placeholder}
        value={value}
        onChange={(e) => onChange?.(e.target.value)}
        onKeyDown={(e) => e.key === 'Enter' && onSearch?.()}
        style={{
          border: 'none',
          backgroundColor: 'transparent',
          outline: 'none',
          flex: 1,
          fontSize: '16px',
          color: 'inherit',
        }}
      />
      {trailingAvatar && <div>{trailingAvatar}</div>}
    </motion.div>
  );
};

