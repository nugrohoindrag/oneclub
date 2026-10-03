import React, { useState } from 'react';
import { motion, Variants } from 'motion/react';
import { Icon } from '../communication/Icon.js';

export const StaggerList: React.FC = () => {
  const [items] = useState(['Desain Responsif', 'Tema Dinamis HCT', 'Animasi Gestur & Drag', 'Token Standar W3C']);

  const containerVariants: Variants = {
    hidden: { opacity: 0 },
    show: {
      opacity: 1,
      transition: {
        staggerChildren: 0.1,
      },
    },
  };

  const itemVariants: Variants = {
    hidden: { opacity: 0, y: 20, scale: 0.95 },
    show: { opacity: 1, y: 0, scale: 1, transition: { type: 'spring', stiffness: 400, damping: 25 } },
  };

  return (
    <motion.div
      variants={containerVariants}
      initial="hidden"
      animate="show"
      style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}
    >
      {items.map((item, idx) => (
        <motion.div
          key={idx}
          variants={itemVariants}
          style={{
            padding: '12px 16px',
            borderRadius: 'var(--radius-md)',
            backgroundColor: 'var(--md-sys-color-surface-container-highest)',
            display: 'flex',
            alignItems: 'center',
            gap: '12px',
            fontSize: '14px',
            fontWeight: 500,
          }}
        >
          <Icon name="check_circle" size={20} color="var(--md-sys-color-primary)" />
          <span>{item}</span>
        </motion.div>
      ))}
    </motion.div>
  );
};

export const SpringPlayground: React.FC = () => {
  const [bounced, setBounced] = useState(false);
  const [stiffness, setStiffness] = useState(400);
  const [damping, setDamping] = useState(15);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100px', backgroundColor: 'var(--md-sys-color-surface-container-high)', borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
        <motion.div
          animate={{
            x: bounced ? 140 : -140,
            rotate: bounced ? 180 : 0,
            borderRadius: bounced ? '50%' : '16px',
          }}
          transition={{ type: 'spring', stiffness, damping }}
          style={{
            width: '64px',
            height: '64px',
            backgroundColor: 'var(--md-sys-color-primary)',
            color: 'var(--md-sys-color-on-primary)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            fontWeight: 700,
            cursor: 'pointer',
          }}
          onClick={() => setBounced(!bounced)}
        >
          M3
        </motion.div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '16px' }}>
        <div>
          <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '12px', marginBottom: '4px' }}>
            <span>Stiffness (Kekakuan):</span>
            <strong>{stiffness}</strong>
          </div>
          <input
            type="range"
            min={100}
            max={1000}
            value={stiffness}
            onChange={(e) => setStiffness(Number(e.target.value))}
            style={{ width: '100%', accentColor: 'var(--md-sys-color-primary)' }}
          />
        </div>
        <div>
          <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '12px', marginBottom: '4px' }}>
            <span>Damping (Redaman):</span>
            <strong>{damping}</strong>
          </div>
          <input
            type="range"
            min={5}
            max={60}
            value={damping}
            onChange={(e) => setDamping(Number(e.target.value))}
            style={{ width: '100%', accentColor: 'var(--md-sys-color-primary)' }}
          />
        </div>
      </div>
    </div>
  );
};
