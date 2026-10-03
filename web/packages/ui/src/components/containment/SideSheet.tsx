import React from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';
import { Icon } from '../communication/Icon.js';

export interface SideSheetProps {
  isOpen: boolean;
  onClose: () => void;
  title?: string;
  children: React.ReactNode;
  actions?: React.ReactNode;
}

export const SideSheet: React.FC<SideSheetProps> = ({
  isOpen,
  onClose,
  title = 'Side Sheet',
  children,
  actions,
}) => {
  return (
    <AnimatePresence>
      {isOpen && (
        <>
          {/* Scrim */}
          <motion.div
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.2 }}
            onClick={onClose}
            style={{
              position: 'fixed',
              inset: 0,
              backgroundColor: 'var(--md-sys-color-scrim)',
              zIndex: 998,
            }}
          />
          {/* Side Sheet Surface */}
          <motion.div
            initial={{ x: '100%' }}
            animate={{ x: 0 }}
            exit={{ x: '100%' }}
            transition={M3_TRANSITIONS.enter}
            style={{
              position: 'fixed',
              top: 0,
              right: 0,
              bottom: 0,
              width: '400px', // Official M3 Side Sheet width
              maxWidth: '85vw',
              borderRadius: '28px 0 0 28px', // Official M3 28px corners
              backgroundColor: 'var(--md-sys-color-surface-container-low)',
              color: 'var(--md-sys-color-on-surface)',
              boxShadow: 'var(--md-sys-elevation-level1)',
              zIndex: 999,
              display: 'flex',
              flexDirection: 'column',
              overflow: 'hidden',
            }}
          >
            {/* Header */}
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '24px 24px 16px' }}>
              <h3 style={{ margin: 0, fontSize: '20px', fontWeight: 600 }}>{title}</h3>
              <button
                onClick={onClose}
                style={{
                  border: 'none',
                  background: 'none',
                  cursor: 'pointer',
                  color: 'inherit',
                  padding: '4px',
                  display: 'flex',
                }}
              >
                <Icon name="close" size={24} />
              </button>
            </div>

            {/* Content */}
            <div style={{ padding: '0 24px 24px', flex: 1, overflowY: 'auto' }}>
              {children}
            </div>

            {/* Actions */}
            {actions && (
              <div style={{ padding: '16px 24px', borderTop: '1px solid var(--md-sys-color-outline-variant)', display: 'flex', justifyContent: 'flex-end', gap: '8px' }}>
                {actions}
              </div>
            )}
          </motion.div>
        </>
      )}
    </AnimatePresence>
  );
};

export interface CarouselItem {
  id: string;
  title: string;
  subtitle?: string;
  image?: string;
  badge?: string;
}

export interface CarouselProps {
  items: CarouselItem[];
  variant?: 'multi-browse' | 'hero';
  className?: string;
}

export const Carousel: React.FC<CarouselProps> = ({
  items,
  variant = 'multi-browse',
  className = '',
}) => {
  const isHero = variant === 'hero';

  return (
    <div
      style={{
        display: 'flex',
        gap: '16px',
        overflowX: 'auto',
        padding: '8px 0',
        scrollSnapType: 'x mandatory',
        scrollbarWidth: 'none',
      }}
      className={`m3-carousel ${className}`}
    >
      {items.map((item) => (
        <motion.div
          key={item.id}
          whileHover={{ y: -4, transition: { duration: 0.2 } }}
          style={{
            flex: isHero ? '0 0 85%' : '0 0 300px',
            minWidth: isHero ? '85%' : '300px',
            height: isHero ? '320px' : '220px',
            borderRadius: 'var(--radius-hero)', // Official M3 28px radius for Carousel cards
            backgroundColor: 'var(--md-sys-color-surface-container-high)',
            color: item.image ? 'var(--md-sys-color-on-media)' : 'var(--md-sys-color-on-surface)',
            padding: '24px',
            display: 'flex',
            flexDirection: 'column',
            justifyContent: 'flex-end',
            position: 'relative',
            overflow: 'hidden',
            scrollSnapAlign: 'start',
            boxShadow: 'var(--md-sys-elevation-level1)',
            cursor: 'pointer',
          }}
        >
          {/* Background Image with Dark Gradient Overlay */}
          {item.image && (
            <>
              <img
                src={item.image}
                alt={item.title}
                style={{
                  position: 'absolute',
                  inset: 0,
                  width: '100%',
                  height: '100%',
                  objectFit: 'cover',
                  zIndex: 0,
                  transition: 'transform 0.3s ease',
                }}
              />
              <div
                style={{
                  position: 'absolute',
                  inset: 0,
                  background: 'linear-gradient(to top, rgba(0,0,0,0.85) 0%, rgba(0,0,0,0.3) 50%, rgba(0,0,0,0.1) 100%)',
                  zIndex: 1,
                }}
              />
            </>
          )}

          <div style={{ position: 'relative', zIndex: 2 }}>
            {item.badge && (
              <span
                style={{
                  display: 'inline-block',
                  marginBottom: '8px',
                  padding: '4px 12px',
                  borderRadius: 'var(--radius-pill)',
                  backgroundColor: item.image ? 'rgba(255, 255, 255, 0.25)' : 'var(--md-sys-color-primary-container)',
                  color: item.image ? 'var(--md-sys-color-on-media)' : 'var(--md-sys-color-on-primary-container)',
                  fontSize: '11px',
                  fontWeight: 600,
                }}
              >
                {item.badge}
              </span>
            )}
            <h3 style={{ margin: '0 0 4px', fontSize: isHero ? '24px' : '18px', fontWeight: 700, color: item.image ? 'var(--md-sys-color-on-media)' : 'inherit' }}>
              {item.title}
            </h3>
            {item.subtitle && (
              <p style={{ margin: 0, fontSize: '13px', color: item.image ? 'rgba(255,255,255,0.85)' : 'var(--md-sys-color-on-surface-variant)' }}>
                {item.subtitle}
              </p>
            )}
          </div>
        </motion.div>
      ))}
    </div>
  );
};

export interface ListItemProps {
  headline: string;
  supportingText?: string;
  leading?: React.ReactNode;
  trailing?: React.ReactNode;
  onClick?: () => void;
  className?: string;
}

export const ListItem: React.FC<ListItemProps> = ({
  headline,
  supportingText,
  leading,
  trailing,
  onClick,
  className = '',
}) => {
  return (
    <motion.div
      onClick={onClick}
      whileHover={onClick ? { backgroundColor: 'var(--md-sys-color-surface-container-high)' } : undefined}
      style={{
        minHeight: supportingText ? '72px' : '56px',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        padding: '8px 16px',
        backgroundColor: 'transparent',
        cursor: onClick ? 'pointer' : 'default',
        borderRadius: 'var(--radius-sm)',
        transition: 'background-color 0.15s',
      }}
      className={`m3-list-item ${className}`}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: '16px' }}>
        {leading && <div style={{ display: 'flex', color: 'var(--md-sys-color-on-surface-variant)' }}>{leading}</div>}
        <div style={{ display: 'flex', flexDirection: 'column' }}>
          <span style={{ fontSize: '16px', fontWeight: 500, color: 'var(--md-sys-color-on-surface)' }}>
            {headline}
          </span>
          {supportingText && (
            <span style={{ fontSize: '14px', color: 'var(--md-sys-color-on-surface-variant)' }}>
              {supportingText}
            </span>
          )}
        </div>
      </div>
      {trailing && <div style={{ display: 'flex', color: 'var(--md-sys-color-on-surface-variant)', fontSize: '12px' }}>{trailing}</div>}
    </motion.div>
  );
};
