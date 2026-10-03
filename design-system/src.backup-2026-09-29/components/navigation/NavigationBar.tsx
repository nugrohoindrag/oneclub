import React from 'react';
import { motion } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';

export interface NavigationDestination {
  id: string;
  label: string;
  icon: React.ReactNode;
  activeIcon?: React.ReactNode;
  badge?: number | string;
}

export interface NavigationBarProps {
  destinations: NavigationDestination[];
  activeId: string;
  onChange: (id: string) => void;
  className?: string;
}

export const NavigationBar: React.FC<NavigationBarProps> = ({
  destinations,
  activeId,
  onChange,
  className = '',
}) => {
  return (
    <nav
      style={{
        height: '80px', // Official M3 80px height
        backgroundColor: 'var(--md-sys-color-surface-container)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-around',
        padding: '0 8px',
        borderTop: '1px solid var(--md-sys-color-outline-variant)',
        width: '100%',
        userSelect: 'none',
      }}
      className={`m3-navigation-bar ${className}`}
    >
      {destinations.map((dest) => {
        const isActive = dest.id === activeId;
        return (
          <button
            key={dest.id}
            onClick={() => onChange(dest.id)}
            style={{
              background: 'none',
              border: 'none',
              display: 'flex',
              flexDirection: 'column',
              alignItems: 'center',
              justifyContent: 'center',
              gap: '4px',
              cursor: 'pointer',
              padding: '4px 0',
              flex: 1,
              maxWidth: '120px',
              position: 'relative',
            }}
          >
            {/* Active Pill Indicator with Motion layoutId */}
            <div
              style={{
                width: '64px', // Official M3 64x32px pill
                height: '32px',
                borderRadius: 'var(--radius-lg)',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                color: isActive
                  ? 'var(--md-sys-color-on-secondary-container)'
                  : 'var(--md-sys-color-on-surface-variant)',
                position: 'relative',
                fontSize: '20px',
                zIndex: 1,
              }}
            >
              {isActive && (
                <motion.div
                  layoutId="activeNavBarPill"
                  transition={M3_TRANSITIONS.springSnappy}
                  style={{
                    position: 'absolute',
                    inset: 0,
                    borderRadius: 'var(--radius-lg)',
                    backgroundColor: 'var(--md-sys-color-secondary-container)',
                    zIndex: -1,
                  }}
                />
              )}
              {isActive && dest.activeIcon ? dest.activeIcon : dest.icon}
              {dest.badge && (
                <span
                  style={{
                    position: 'absolute',
                    top: '2px',
                    right: '10px',
                    height: '14px',
                    minWidth: '14px',
                    padding: '0 3px',
                    borderRadius: 'var(--radius-xs)',
                    backgroundColor: 'var(--md-sys-color-error)',
                    color: 'var(--md-sys-color-on-error)',
                    fontSize: '11px',
                    fontWeight: 600,
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                  }}
                >
                  {dest.badge}
                </span>
              )}
            </div>

            {/* Label Text */}
            <span
              style={{
                fontSize: '12px',
                fontWeight: isActive ? 600 : 500,
                color: isActive
                  ? 'var(--md-sys-color-on-surface)'
                  : 'var(--md-sys-color-on-surface-variant)',
                transition: 'color 0.2s',
              }}
            >
              {dest.label}
            </span>
          </button>
        );
      })}
    </nav>
  );
};

export interface NavigationRailProps {
  destinations: NavigationDestination[];
  activeId: string;
  onChange: (id: string) => void;
  headerAction?: React.ReactNode;
  className?: string;
}

export const NavigationRail: React.FC<NavigationRailProps> = ({
  destinations,
  activeId,
  onChange,
  headerAction,
  className = '',
}) => {
  return (
    <aside
      style={{
        width: '80px', // Official M3 80px width
        height: '100%',
        backgroundColor: 'var(--md-sys-color-surface)',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        padding: '24px 0',
        gap: '24px',
        borderRight: '1px solid var(--md-sys-color-outline-variant)',
        userSelect: 'none',
      }}
      className={`m3-navigation-rail ${className}`}
    >
      {headerAction && <div>{headerAction}</div>}

      <div style={{ display: 'flex', flexDirection: 'column', gap: '16px', width: '100%', alignItems: 'center' }}>
        {destinations.map((dest) => {
          const isActive = dest.id === activeId;
          return (
            <button
              key={dest.id}
              onClick={() => onChange(dest.id)}
              style={{
                background: 'none',
                border: 'none',
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
                gap: '4px',
                cursor: 'pointer',
                padding: '4px 0',
                width: '100%',
                position: 'relative',
              }}
            >
              <div
                style={{
                  width: '56px',
                  height: '32px',
                  borderRadius: 'var(--radius-lg)',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  color: isActive
                    ? 'var(--md-sys-color-on-secondary-container)'
                    : 'var(--md-sys-color-on-surface-variant)',
                  fontSize: '20px',
                  position: 'relative',
                  zIndex: 1,
                }}
              >
                {isActive && (
                  <motion.div
                    layoutId="activeNavRailPill"
                    transition={M3_TRANSITIONS.springSnappy}
                    style={{
                      position: 'absolute',
                      inset: 0,
                      borderRadius: 'var(--radius-lg)',
                      backgroundColor: 'var(--md-sys-color-secondary-container)',
                      zIndex: -1,
                    }}
                  />
                )}
                {isActive && dest.activeIcon ? dest.activeIcon : dest.icon}
              </div>
              <span
                style={{
                  fontSize: '12px',
                  fontWeight: isActive ? 600 : 500,
                  color: isActive
                    ? 'var(--md-sys-color-on-surface)'
                    : 'var(--md-sys-color-on-surface-variant)',
                }}
              >
                {dest.label}
              </span>
            </button>
          );
        })}
      </div>
    </aside>
  );
};
