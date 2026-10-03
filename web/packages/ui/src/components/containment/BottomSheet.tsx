import React from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';

export interface BottomSheetProps {
  isOpen: boolean;
  onClose: () => void;
  title?: string;
  children: React.ReactNode;
}

export const BottomSheet: React.FC<BottomSheetProps> = ({
  isOpen,
  onClose,
  title,
  children,
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
          {/* Bottom Sheet with Drag Gesture Physics */}
          <motion.div
            initial={{ y: '100%' }}
            animate={{ y: 0 }}
            exit={{ y: '100%' }}
            transition={M3_TRANSITIONS.enter}
            drag="y"
            dragConstraints={{ top: 0 }}
            dragSnapToOrigin
            dragElastic={{ top: 0.05, bottom: 0.5 }}
            onDragEnd={(_, info) => {
              if (info.offset.y > 140 || info.velocity.y > 500) {
                onClose();
              }
            }}
            style={{
              position: 'fixed',
              bottom: 0,
              left: 0,
              right: 0,
              maxHeight: '80vh',
              borderRadius: '28px 28px 0 0', // Official M3 28px top radius
              backgroundColor: 'var(--md-sys-color-surface-container-low)',
              color: 'var(--md-sys-color-on-surface)',
              boxShadow: 'var(--md-sys-elevation-level1)',
              zIndex: 999,
              display: 'flex',
              flexDirection: 'column',
              overflow: 'hidden',
            }}
          >
            {/* Drag Handle (32x4px) */}
            <div style={{ display: 'flex', justifyContent: 'center', padding: '16px 0 8px', cursor: 'grab' }}>
              <div
                style={{
                  width: '32px',
                  height: '4px',
                  borderRadius: 'var(--radius-pill)',
                  backgroundColor: 'var(--md-sys-color-outline-variant)',
                }}
              />
            </div>

            {title && (
              <div style={{ padding: '8px 24px 16px', fontSize: '18px', fontWeight: 600 }}>
                {title}
              </div>
            )}

            <div style={{ padding: '0 24px 32px', overflowY: 'auto' }}>
              {children}
            </div>
          </motion.div>
        </>
      )}
    </AnimatePresence>
  );
};

export const Divider: React.FC<{
  orientation?: 'horizontal' | 'vertical';
  inset?: boolean;
  className?: string;
}> = ({ orientation = 'horizontal', inset = false, className = '' }) => {
  const isHorizontal = orientation === 'horizontal';

  return (
    <div
      style={{
        backgroundColor: 'var(--md-sys-color-outline-variant)',
        width: isHorizontal ? '100%' : '1px',
        height: isHorizontal ? '1px' : '100%',
        margin: inset ? (isHorizontal ? '0 16px' : '16px 0') : '0',
      }}
      className={`m3-divider ${className}`}
    />
  );
};
