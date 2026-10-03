/**
 * @license MIT
 * Skeleton Shimmer Loading Suite — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §18 Motion Design — restrained, Material easing
 *   §9  Accessibility — a shimmer that cannot be stopped is a reduced-motion
 *        violation, so the sweep runs in JS and halts when asked
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';
import { motion } from 'motion/react';
import { staggerChildren, useReducedMotionSafe, fadeSlide } from '../../motion/index.js';

export interface SkeletonProps {
  width?: string;
  height?: string;
  borderRadius?: string;
  className?: string;
  /** Per-placeholder delay, so a group of bars sweeps in sequence. */
  delay?: number;
}

export const Skeleton: React.FC<SkeletonProps> = ({
  width = '100%',
  height = '20px',
  borderRadius = 'var(--radius-sm)',
  className = '',
  delay = 0,
}) => {
  const reduced = useReducedMotionSafe();

  return (
    <div
      className={`morphic-skeleton-shimmer ${className}`}
      aria-hidden
      style={{
        width,
        height,
        borderRadius,
        backgroundColor: 'var(--md-sys-color-surface-container)',
        overflow: 'hidden',
        position: 'relative',
      }}
    >
      {/* Reduced motion (§9) leaves the flat placeholder — still legibly a
          loading state, just without the travelling highlight. */}
      {!reduced && (
        <motion.div
          style={{
            position: 'absolute',
            inset: 0,
            backgroundImage:
              'linear-gradient(90deg, rgba(255,255,255,0) 0%, rgba(255,255,255,0.3) 50%, rgba(255,255,255,0) 100%)',
          }}
          animate={{ x: ['-100%', '100%'] }}
          transition={{ duration: 1.6, repeat: Infinity, ease: 'easeInOut', delay }}
        />
      )}
    </div>
  );
};

/** Shared shell for the composed skeletons — fades the whole block in once. */
const SkeletonSurface: React.FC<{
  children: React.ReactNode;
  padding: string;
  gap: string;
}> = ({ children, padding, gap }) => (
  <motion.div
    initial="hidden"
    animate="visible"
    variants={{ hidden: {}, visible: { transition: staggerChildren('tight') } }}
    style={{
      borderRadius: 'var(--radius-xl)',
      backgroundColor: 'var(--md-sys-color-surface)',
      border: '1px solid var(--md-sys-color-border)',
      padding,
      display: 'flex',
      flexDirection: 'column',
      gap,
    }}
  >
    {children}
  </motion.div>
);

const row = fadeSlide('up', 4);

export const SkeletonKPI: React.FC = () => {
  return (
    <SkeletonSurface padding="20px 24px" gap="12px">
      <motion.div variants={row} style={{ display: 'flex', justifyContent: 'space-between' }}>
        <Skeleton width="120px" height="14px" />
        <Skeleton width="32px" height="32px" borderRadius="8px" delay={0.1} />
      </motion.div>
      <motion.div variants={row}>
        <Skeleton width="180px" height="28px" delay={0.15} />
      </motion.div>
      <motion.div variants={row}>
        <Skeleton width="100px" height="12px" delay={0.2} />
      </motion.div>
      <motion.div variants={row}>
        <Skeleton width="100%" height="4px" borderRadius="2px" delay={0.25} />
      </motion.div>
    </SkeletonSurface>
  );
};

export const SkeletonChart: React.FC = () => {
  return (
    <SkeletonSurface padding="24px 28px" gap="16px">
      <motion.div variants={row} style={{ display: 'flex', justifyContent: 'space-between' }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
          <Skeleton width="160px" height="18px" />
          <Skeleton width="220px" height="12px" delay={0.08} />
        </div>
        <Skeleton width="80px" height="20px" borderRadius="10px" delay={0.16} />
      </motion.div>
      <motion.div variants={row}>
        <Skeleton width="100%" height="160px" borderRadius="var(--radius-lg)" delay={0.24} />
      </motion.div>
    </SkeletonSurface>
  );
};

export const SkeletonTable: React.FC = () => {
  return (
    <SkeletonSurface padding="24px" gap="16px">
      <motion.div variants={row} style={{ display: 'flex', justifyContent: 'space-between' }}>
        <Skeleton width="200px" height="20px" />
        <Skeleton width="160px" height="32px" borderRadius="16px" delay={0.08} />
      </motion.div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
        {[1, 2, 3, 4].map((i) => (
          <motion.div
            key={i}
            variants={row}
            style={{ display: 'flex', gap: '16px', alignItems: 'center' }}
          >
            <Skeleton width="32px" height="32px" borderRadius="50%" delay={i * 0.08} />
            <Skeleton width="24%" height="16px" delay={i * 0.08} />
            <Skeleton width="30%" height="16px" delay={i * 0.08} />
            <Skeleton width="20%" height="16px" delay={i * 0.08} />
            <Skeleton width="15%" height="16px" delay={i * 0.08} />
          </motion.div>
        ))}
      </div>
    </SkeletonSurface>
  );
};
