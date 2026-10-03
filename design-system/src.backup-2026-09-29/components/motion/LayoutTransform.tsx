/**
 * @license MIT
 * Motion Lab — LayoutTransform Demo (Shared Layout layoutId)
 * Powered by Motion for React (https://motion.dev - MIT License)
 * Photography Assets: Unsplash Open License
 */

import React, { useState } from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';
import { Icon } from '../communication/Icon.js';
import { Button } from '../actions/Button.js';

export const LayoutTransformDemo: React.FC = () => {
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const items = [
    {
      id: 'item-1',
      title: 'Sunset in Bali',
      category: 'Wisata Alam',
      icon: 'landscape',
      imageUrl: 'https://images.unsplash.com/photo-1537996194471-e657df975ab4?w=800&auto=format&fit=crop&q=80',
    },
    {
      id: 'item-2',
      title: 'Bromo Sunrise',
      category: 'Taman Nasional',
      icon: 'wb_sunny',
      imageUrl: 'https://images.unsplash.com/photo-1588668214407-6ea9a6d8c272?w=800&auto=format&fit=crop&q=80',
    },
    {
      id: 'item-3',
      title: 'Raja Ampat Island',
      category: 'Wisata Bahari',
      icon: 'water',
      imageUrl: 'https://images.unsplash.com/photo-1516690561799-46d8f74f9abf?w=800&auto=format&fit=crop&q=80',
    },
  ];

  return (
    <div style={{ position: 'relative', minHeight: '220px' }}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: '16px' }}>
        {items.map((item) => (
          <motion.div
            layoutId={item.id}
            key={item.id}
            onClick={() => setSelectedId(item.id)}
            whileHover={{ scale: 1.03, y: -4 }}
            transition={M3_TRANSITIONS.containerTransform}
            style={{
              height: '200px',
              borderRadius: 'var(--radius-xl)',
              position: 'relative',
              overflow: 'hidden',
              cursor: 'pointer',
              boxShadow: 'var(--md-sys-elevation-level1)',
              display: 'flex',
              flexDirection: 'column',
              justifyContent: 'flex-end',
              padding: '20px',
              color: 'var(--md-sys-color-on-media)',
            }}
          >
            {/* Background Image & Gradient */}
            <img
              src={item.imageUrl}
              alt={item.title}
              style={{
                position: 'absolute',
                inset: 0,
                width: '100%',
                height: '100%',
                objectFit: 'cover',
                zIndex: 0,
              }}
            />
            <div
              style={{
                position: 'absolute',
                inset: 0,
                background: 'linear-gradient(to top, rgba(0,0,0,0.8) 0%, rgba(0,0,0,0.2) 60%, transparent 100%)',
                zIndex: 1,
              }}
            />

            <div style={{ position: 'relative', zIndex: 2 }}>
              <motion.div layoutId={`icon-${item.id}`} style={{ marginBottom: '6px' }}>
                <Icon name={item.icon} size={24} color="var(--md-sys-color-on-media)" />
              </motion.div>
              <motion.span layoutId={`cat-${item.id}`} style={{ fontSize: '11px', fontWeight: 600, opacity: 0.9, textTransform: 'uppercase', letterSpacing: '0.04em' }}>
                {item.category}
              </motion.span>
              <motion.h3 layoutId={`title-${item.id}`} style={{ margin: '2px 0 0', fontSize: '17px', fontWeight: 700 }}>
                {item.title}
              </motion.h3>
            </div>
          </motion.div>
        ))}
      </div>

      <AnimatePresence>
        {selectedId && (() => {
          const active = items.find((i) => i.id === selectedId);
          if (!active) return null;
          return (
            <>
              <motion.div
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                onClick={() => setSelectedId(null)}
                style={{
                  position: 'fixed',
                  inset: 0,
                  backgroundColor: 'var(--md-sys-color-scrim)',
                  zIndex: 990,
                }}
              />
              <div style={{ position: 'fixed', inset: 0, display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 991, pointerEvents: 'none' }}>
                <motion.div
                  layoutId={selectedId}
                  transition={M3_TRANSITIONS.containerTransform}
                  style={{
                    width: '90%',
                    maxWidth: '560px',
                    borderRadius: 'var(--radius-hero)',
                    overflow: 'hidden',
                    backgroundColor: 'var(--md-sys-color-surface)',
                    color: 'var(--md-sys-color-on-surface)',
                    boxShadow: 'var(--md-sys-elevation-level4)',
                    pointerEvents: 'auto',
                  }}
                >
                  <div style={{ height: '240px', position: 'relative' }}>
                    <img
                      src={active.imageUrl}
                      alt={active.title}
                      style={{ width: '100%', height: '100%', objectFit: 'cover' }}
                    />
                    <div style={{ position: 'absolute', inset: 0, background: 'linear-gradient(to top, rgba(0,0,0,0.7) 0%, transparent 60%)' }} />
                    <div style={{ position: 'absolute', top: '16px', right: '16px', zIndex: 2 }}>
                      <Button variant="tonal" size="sm" onClick={() => setSelectedId(null)}>
                        <Icon name="close" size={20} />
                      </Button>
                    </div>
                    <div style={{ position: 'absolute', bottom: '20px', left: '24px', zIndex: 2, color: 'var(--md-sys-color-on-media)' }}>
                      <motion.span layoutId={`cat-${active.id}`} style={{ fontSize: '12px', fontWeight: 600, opacity: 0.9, textTransform: 'uppercase' }}>
                        {active.category}
                      </motion.span>
                      <motion.h2 layoutId={`title-${active.id}`} style={{ margin: '4px 0 0', fontSize: '26px', fontWeight: 700 }}>
                        {active.title}
                      </motion.h2>
                    </div>
                  </div>

                  <div style={{ padding: '24px 28px' }}>
                    <motion.p
                      initial={{ opacity: 0, y: 20 }}
                      animate={{ opacity: 1, y: 0 }}
                      transition={{ delay: 0.15 }}
                      style={{ margin: '0 0 20px', fontSize: '14px', lineHeight: 1.6, color: 'var(--md-sys-color-on-surface-variant)' }}
                    >
                      Ini adalah demonstrasi <strong>Shared Layout Container Transform</strong> resmi Motion (motion.dev) menggunakan prop <code>layoutId</code>. Transisi dari kartu kecil ke modal layar penuh berlangsung secara otomatis dan mulus.
                    </motion.p>
                    <Button variant="filled" onClick={() => setSelectedId(null)}>
                      Tutup Modal
                    </Button>
                  </div>
                </motion.div>
              </div>
            </>
          );
        })()}
      </AnimatePresence>
    </div>
  );
};

export const SvgDrawMotion: React.FC<{ trigger?: 'load' | 'hover' }> = ({ trigger = 'hover' }) => {
  const [isHovered, setIsHovered] = useState(false);

  return (
    <div
      onMouseEnter={() => setIsHovered(true)}
      onMouseLeave={() => setIsHovered(false)}
      style={{ display: 'flex', justifyContent: 'center', cursor: 'pointer', padding: '16px' }}
    >
      <svg width="220" height="100" viewBox="0 0 220 100">
        <motion.path
          d="M 20 50 Q 65 10 110 50 T 200 50"
          fill="none"
          stroke="var(--md-sys-color-primary)"
          strokeWidth="4"
          strokeLinecap="round"
          initial={{ pathLength: 0 }}
          animate={{ pathLength: trigger === 'hover' ? (isHovered ? 1 : 0.2) : 1 }}
          transition={{ duration: 1.2, ease: [0.2, 0, 0, 1] }}
        />
        <motion.circle
          cx="20"
          cy="50"
          r="6"
          fill="var(--md-sys-color-secondary)"
          animate={{ scale: isHovered ? [1, 1.5, 1] : 1 }}
          transition={{ repeat: Infinity, duration: 1.5 }}
        />
        <motion.circle
          cx="200"
          cy="50"
          r="6"
          fill="var(--md-sys-color-tertiary)"
          animate={{ scale: isHovered ? [1, 1.5, 1] : 1 }}
          transition={{ repeat: Infinity, duration: 1.5, delay: 0.5 }}
        />
      </svg>
    </div>
  );
};

export const MagneticMotion: React.FC<{ children: React.ReactElement; strength?: number }> = ({
  children,
  strength = 0.4,
}) => {
  const [position, setPosition] = useState({ x: 0, y: 0 });

  const handleMouseMove = (e: React.MouseEvent<HTMLDivElement>) => {
    const { clientX, clientY, currentTarget } = e;
    const { left, top, width, height } = currentTarget.getBoundingClientRect();
    const x = (clientX - (left + width / 2)) * strength;
    const y = (clientY - (top + height / 2)) * strength;
    setPosition({ x, y });
  };

  const handleMouseLeave = () => {
    setPosition({ x: 0, y: 0 });
  };

  return (
    <motion.div
      onMouseMove={handleMouseMove}
      onMouseLeave={handleMouseLeave}
      animate={{ x: position.x, y: position.y }}
      transition={{ type: 'spring', stiffness: 350, damping: 20, mass: 0.5 }}
      style={{ display: 'inline-block' }}
    >
      {children}
    </motion.div>
  );
};
