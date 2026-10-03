/**
 * @license MIT
 * Gestures — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Motion docs: https://motion.dev/docs/react-gestures
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §19 Micro-interactions — card hover lift, button pressed scale ~0.98
 *   §18 Motion Design      — hover 120–160ms, button 120–180ms, never bouncy
 *   §9  Accessibility      — keyboard behaviour, focus visibility, reduced motion
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useRef } from 'react';
import { motion, useMotionValue, useSpring } from 'motion/react';
import {
  M3_SPRING_OPTIONS,
  M3_TRANSITIONS,
  useReducedMotionSafe,
} from '../../motion/index.js';

/* ========================================================================= */
/* Magnetic — an element that leans toward the pointer                       */
/* ========================================================================= */

export interface MagneticProps {
  children: React.ReactNode;
  /**
   * How far the element follows the pointer, 0–1. Kept low by default: §18
   * asks for subtle movement, and a strong pull makes a control feel evasive.
   */
  strength?: number;
  /** Cap the travel in pixels, so a large target does not drift far. */
  maxOffset?: number;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * ```tsx
 * <Magnetic><IconButton icon={<Icon name="add" />} /></Magnetic>
 * ```
 *
 * Disabled entirely under `prefers-reduced-motion` (§9), and pointer-driven
 * only — keyboard users are unaffected, so nothing is lost.
 */
export const Magnetic: React.FC<MagneticProps> = ({
  children,
  strength = 0.25,
  maxOffset = 12,
  className = '',
  style,
}) => {
  const ref = useRef<HTMLDivElement>(null);
  const reduced = useReducedMotionSafe();
  const rawX = useMotionValue(0);
  const rawY = useMotionValue(0);
  const x = useSpring(rawX, M3_SPRING_OPTIONS.responsive);
  const y = useSpring(rawY, M3_SPRING_OPTIONS.responsive);

  const handleMove = (event: React.PointerEvent<HTMLDivElement>) => {
    if (reduced || !ref.current) return;
    const rect = ref.current.getBoundingClientRect();
    const dx = event.clientX - (rect.left + rect.width / 2);
    const dy = event.clientY - (rect.top + rect.height / 2);
    const clamp = (v: number) => Math.max(-maxOffset, Math.min(maxOffset, v * strength));
    rawX.set(clamp(dx));
    rawY.set(clamp(dy));
  };

  const reset = () => {
    rawX.set(0);
    rawY.set(0);
  };

  return (
    <motion.div
      ref={ref}
      onPointerMove={handleMove}
      onPointerLeave={reset}
      className={`morphic-magnetic ${className}`}
      style={{ display: 'inline-block', x: reduced ? 0 : x, y: reduced ? 0 : y, ...style }}
    >
      {children}
    </motion.div>
  );
};

/* ========================================================================= */
/* Pressable — the §19 press interaction on any element                      */
/* ========================================================================= */

export interface PressableProps {
  children: React.ReactNode;
  /** §19: "pressed scale ~0.98". */
  scale?: number;
  /** Lift on hover, as a card does. */
  lift?: boolean;
  /**
   * Motion shown while keyboard-focused (§9 "Focus state", "Focus
   * visibility"). Defaults to the same lift as hover, so keyboard and pointer
   * users get equivalent feedback. The focus RING itself is CSS
   * (`:focus-visible` in components.css) — this is the movement half.
   */
  whileFocus?: Parameters<typeof motion.div>[0]['whileFocus'];
  disabled?: boolean;
  onClick?: () => void;
  onHoverStart?: (event: PointerEvent) => void;
  onHoverEnd?: (event: PointerEvent) => void;
  onTapStart?: () => void;
  /** Fires when the pointer is released INSIDE the element. */
  onTap?: () => void;
  /** Fires when the pointer is released OUTSIDE the element. */
  onTapCancel?: () => void;
  as?: 'div' | 'button' | 'a' | 'li';
  className?: string;
  style?: React.CSSProperties;
}

/**
 * Wraps any content in the standard Morphic press/hover response, with the
 * §18 durations. When rendered as a `button` it keeps native keyboard
 * behaviour and focus handling (§9).
 */
export const Pressable: React.FC<PressableProps> = ({
  children,
  scale = 0.98,
  lift = false,
  whileFocus,
  disabled = false,
  onClick,
  onHoverStart,
  onHoverEnd,
  onTapStart,
  onTap,
  onTapCancel,
  as = 'div',
  className = '',
  style,
}) => {
  const reduced = useReducedMotionSafe();
  const Component = motion[as as 'div'] as typeof motion.div;
  const inert = disabled || reduced;

  return (
    <Component
      className={`morphic-pressable ${className}`}
      onClick={disabled ? undefined : onClick}
      aria-disabled={disabled || undefined}
      whileTap={inert ? undefined : { scale }}
      whileHover={inert || !lift ? undefined : { y: -1 }}
      whileFocus={inert ? undefined : (whileFocus ?? (lift ? { y: -1 } : undefined))}
      onHoverStart={onHoverStart}
      onHoverEnd={onHoverEnd}
      onTapStart={onTapStart}
      onTap={onTap}
      onTapCancel={onTapCancel}
      transition={M3_TRANSITIONS.button}
      style={{
        cursor: disabled ? 'not-allowed' : onClick ? 'pointer' : undefined,
        opacity: disabled ? 'var(--md-sys-opacity-disabled)' : undefined,
        ...style,
      }}
    >
      {children}
    </Component>
  );
};

/* ========================================================================= */
/* Tilt — a card that tips slightly toward the pointer (3D transforms)       */
/* ========================================================================= */

export interface TiltProps {
  children: React.ReactNode;
  /** Maximum rotation in degrees. Small by default (§18). */
  max?: number;
  /** CSS perspective in pixels. */
  perspective?: number;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * Motion docs: https://motion.dev/docs/react-motion-component#transform
 *
 * §32 rules out "excessive 3D effects", so `max` stays in single digits. Use
 * this on a hero or feature card, never across a dashboard grid.
 */
export const Tilt: React.FC<TiltProps> = ({
  children,
  max = 6,
  perspective = 800,
  className = '',
  style,
}) => {
  const ref = useRef<HTMLDivElement>(null);
  const reduced = useReducedMotionSafe();
  const rawX = useMotionValue(0);
  const rawY = useMotionValue(0);
  const rotateX = useSpring(rawX, M3_SPRING_OPTIONS.gentle);
  const rotateY = useSpring(rawY, M3_SPRING_OPTIONS.gentle);

  const handleMove = (event: React.PointerEvent<HTMLDivElement>) => {
    if (reduced || !ref.current) return;
    const rect = ref.current.getBoundingClientRect();
    const px = (event.clientX - rect.left) / rect.width - 0.5;
    const py = (event.clientY - rect.top) / rect.height - 0.5;
    rawX.set(-py * max * 2);
    rawY.set(px * max * 2);
  };

  const reset = () => {
    rawX.set(0);
    rawY.set(0);
  };

  return (
    <motion.div
      ref={ref}
      onPointerMove={handleMove}
      onPointerLeave={reset}
      className={`morphic-tilt ${className}`}
      style={{
        perspective,
        transformStyle: 'preserve-3d',
        rotateX: reduced ? 0 : rotateX,
        rotateY: reduced ? 0 : rotateY,
        ...style,
      }}
    >
      {children}
    </motion.div>
  );
};
