/**
 * @license MIT
 * Badge & Progress Indicators — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §18 Motion Design      — restrained durations, Material easing
 *   §19 Micro-interactions — a badge appearing is a state change, so it moves
 *   §9  Accessibility      — every animation collapses under reduced motion
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';
import { AnimatePresence, motion } from 'motion/react';
import { M3_SPRING, M3_TRANSITIONS, useReducedMotionSafe } from '../../motion/index.js';

export interface BadgeProps {
  count?: number; // If not provided, renders Small Badge (6px dot)
  maxCount?: number;
  variant?: 'error' | 'primary' | 'secondary';
  className?: string;
  children?: React.ReactNode;
  /** Hide the badge, animating it out rather than unmounting it flatly (§19). */
  hidden?: boolean;
}

export const Badge: React.FC<BadgeProps> = ({
  count,
  maxCount = 999,
  variant = 'error',
  className = '',
  children,
  hidden = false,
}) => {
  const reduced = useReducedMotionSafe();
  const isSmall = count === undefined;
  const displayCount = count !== undefined && count > maxCount ? `${maxCount}+` : count;

  const bgMap = {
    error: 'var(--md-sys-color-error)',
    primary: 'var(--md-sys-color-primary)',
    secondary: 'var(--md-sys-color-secondary)',
  };

  const textMap = {
    error: 'var(--md-sys-color-on-error)',
    primary: 'var(--md-sys-color-on-primary)',
    secondary: 'var(--md-sys-color-on-secondary)',
  };

  // A badge arrives by scaling up from its own centre — M3's badge entrance.
  // Under reduced motion (§9) the fade stays and the scale collapses.
  const scaleFrom = reduced ? 1 : 0.4;

  const badgeElement = (
    <motion.span
      key="badge"
      initial={{ opacity: 0, scale: scaleFrom }}
      animate={{ opacity: 1, scale: 1 }}
      exit={{ opacity: 0, scale: scaleFrom, transition: M3_TRANSITIONS.exit }}
      transition={reduced ? M3_TRANSITIONS.enter : M3_SPRING.snappy}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        backgroundColor: bgMap[variant],
        color: textMap[variant],
        width: isSmall ? '6px' : 'auto',
        height: isSmall ? '6px' : '16px',
        minWidth: isSmall ? '6px' : '16px',
        padding: isSmall ? '0' : '0 4px',
        borderRadius: isSmall ? '50%' : '8px',
        fontSize: '11px',
        fontWeight: 600,
        lineHeight: 1,
      }}
      className={`m3-badge ${className}`}
    >
      {/* The count swaps rather than jumps: 3 → 4 rolls over in place. */}
      {!isSmall && (
        <AnimatePresence mode="popLayout" initial={false}>
          <motion.span
            key={String(displayCount)}
            initial={{ opacity: 0, y: reduced ? 0 : -6 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: reduced ? 0 : 6 }}
            transition={M3_TRANSITIONS.button}
          >
            {displayCount}
          </motion.span>
        </AnimatePresence>
      )}
    </motion.span>
  );

  if (!children) {
    return <AnimatePresence initial={false}>{!hidden && badgeElement}</AnimatePresence>;
  }

  return (
    <div style={{ position: 'relative', display: 'inline-flex' }}>
      {children}
      <span
        style={{
          position: 'absolute',
          top: isSmall ? '-2px' : '-6px',
          right: isSmall ? '-2px' : '-8px',
        }}
      >
        <AnimatePresence initial={false}>{!hidden && badgeElement}</AnimatePresence>
      </span>
    </div>
  );
};

export interface LinearProgressProps {
  progress?: number; // 0 to 100, if undefined renders indeterminate
  className?: string;
}

export const LinearProgress: React.FC<LinearProgressProps> = ({
  progress,
  className = '',
}) => {
  const reduced = useReducedMotionSafe();
  const isIndeterminate = progress === undefined;

  return (
    <div
      style={{
        width: '100%',
        height: '4px', // M3 4px track
        backgroundColor: 'var(--md-sys-color-surface-container-highest)',
        borderRadius: 'var(--radius-pill)',
        overflow: 'hidden',
        position: 'relative',
      }}
      className={`m3-linear-progress ${className}`}
      role="progressbar"
      aria-valuenow={isIndeterminate ? undefined : progress}
      aria-valuemin={0}
      aria-valuemax={100}
    >
      {isIndeterminate ? (
        // The sweep runs in JS so it can stop under reduced motion (§9)
        // instead of looping forever behind the viewer's back.
        <motion.div
          style={{
            height: '100%',
            width: '40%',
            backgroundColor: 'var(--md-sys-color-primary)',
            borderRadius: 'var(--radius-pill)',
          }}
          animate={reduced ? { x: '0%' } : { x: ['-100%', '250%'] }}
          transition={
            reduced ? { duration: 0 } : { duration: 1.5, repeat: Infinity, ease: 'easeInOut' }
          }
        />
      ) : (
        <motion.div
          style={{
            height: '100%',
            backgroundColor: 'var(--md-sys-color-primary)',
            borderRadius: 'var(--radius-pill)',
          }}
          initial={false}
          animate={{ width: `${progress}%` }}
          transition={M3_TRANSITIONS.chart}
        />
      )}
    </div>
  );
};

export interface CircularProgressProps {
  progress?: number; // 0 to 100
  size?: number;
  strokeWidth?: number;
  className?: string;
}

export const CircularProgress: React.FC<CircularProgressProps> = ({
  progress,
  size = 48,
  strokeWidth = 4,
  className = '',
}) => {
  const reduced = useReducedMotionSafe();
  const isIndeterminate = progress === undefined;
  const radius = (size - strokeWidth) / 2;
  const circumference = 2 * Math.PI * radius;
  const strokeDashoffset = isIndeterminate
    ? circumference * 0.75
    : circumference - ((progress ?? 0) / 100) * circumference;

  const spinning = isIndeterminate && !reduced;

  return (
    <motion.svg
      width={size}
      height={size}
      viewBox={`0 0 ${size} ${size}`}
      style={{ rotate: -90 }}
      animate={spinning ? { rotate: [-90, 270] } : { rotate: -90 }}
      transition={spinning ? { duration: 1.4, repeat: Infinity, ease: 'linear' } : { duration: 0 }}
      className={`m3-circular-progress ${className}`}
      role="progressbar"
      aria-valuenow={isIndeterminate ? undefined : progress}
      aria-valuemin={0}
      aria-valuemax={100}
    >
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        stroke="var(--md-sys-color-surface-container-highest)"
        strokeWidth={strokeWidth}
        fill="transparent"
      />
      {/* The arc draws to its new value rather than snapping (§19). */}
      <motion.circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        stroke="var(--md-sys-color-primary)"
        strokeWidth={strokeWidth}
        strokeDasharray={circumference}
        strokeLinecap="round"
        fill="transparent"
        initial={false}
        animate={{ strokeDashoffset }}
        transition={M3_TRANSITIONS.chart}
      />
    </motion.svg>
  );
};
